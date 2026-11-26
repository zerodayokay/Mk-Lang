// Why: an HTTP/2 endpoint decodes every header block the peer sends, in the
// task that reads the connection; the block, its sizes and its indexes are
// the peer's, so the decoder must be bounded and total, must own all its
// memory (it lives in a connection that tasks share), and must keep its
// dynamic table in sync with the peer's even when it refuses the fields.
//
// What: Decoder, with New, Free, Decode, SetMaxTableSize, TableSize and
// TableLen (RFC 7541 3, 4, 6).
//
// How: one pass over a whole block. Each field's name and value are first
// described (Src: a static entry, a dynamic entry, raw or Huffman bytes of
// the block), then placed (Loc): a static string, raw bytes and a dynamic
// entry that does not wrap stay where they are, a Huffman string is decoded
// into a scratch buffer, within what is left of the list budget, and the
// sink gets views of them for the one call. A field that is inserted has its
// name copied to scratch first, since the insertion may evict the entry it
// names (4.4). Scratch grows before each string is placed, the value's
// reserve within what the name left of the budget, so one field never needs
// more than the budget or the table's size. The decoder keeps a scratch
// buffer of at most the table's size plus ScratchKeep; a block whose fields
// could need more decodes into a buffer of its own, freed at its end, so no
// peer can make a connection hold more. Once a field would pass the budget
// the sink gets nothing more, but the decoder reads on: it checks each
// Huffman string in one pass with no output, and assembles in scratch only
// an entry it must insert (at most the table's size). That work is
// O(compressed bytes) but for one case: an inserted field whose name is a
// dynamic entry copies that name (up to the table's size) for a two-byte
// representation. A failed block leaves the decoder broken: the table may
// be out of step, so it refuses every later block.

// What a Src describes.
const SrcStatic = 0
const SrcDynamic = 1
const SrcRaw = 2
const SrcHuffman = 3

// A name or value before it is placed: a static entry's (index i, value
// when part is 1), a dynamic entry's (absolute index i, value when part is
// 1), or the block's bytes from start to end, raw or Huffman coded. at is
// where its representation starts, for errors.
struct Src {
    kind: int
    i: int
    part: int
    start: int
    end: int
    at: int
}

// Where a placed string lies.
const InStatic = 0
const InBlock = 1
const InRing = 2
const InScratch = 3

// A placed string: a static entry's (index i, part as in Src), or len bytes
// from start of the block, the byte ring or scratch.
struct Loc {
    where: int
    i: int
    part: int
    start: int
    len: int
}

// The list budget of one block: the decoded list size so far, its limit,
// and whether a field has passed it.
struct Budget {
    used: int
    limit: int
    over: bool
}

// The limits a decoder enforces: the maximum table size in force (RFC
// 7541 4.2); whether the next block must start with a size update, and the
// smallest maximum since the last block, which that first update may not
// pass (RFC 7541 4.2, RFC 9113 4.3.1); the list budget of a block.
struct Limits {
    limit: int
    need_update: bool
    low: int
    list_bytes: int
}

// The scratch buffer's starting size.
const ScratchStart = 4096

// The scratch a decoder keeps beyond its table's size. A block whose fields
// could need more (the table's size plus 8/5 of the block's bytes, within
// the budget) decodes into a buffer of its own, freed at its end, so a peer
// cannot make every connection hold up to list_bytes of scratch.
const ScratchKeep = 65536

// The largest list budget: a FieldBlock's spans hold u32 offsets.
const MaxListBytes = 0xFFFF_FFFF

/// An HPACK decoder: the dynamic table of one direction of one connection,
/// the limits it enforces, and a scratch buffer. Linear: it owns heap
/// memory; release it with Free. Send and Share: it holds only owned
/// buffers and numbers (Decode takes mut self, so two tasks never decode
/// with one at once).
export linear view struct Decoder {
    table: Table
    lim: Limits
    // Huffman output, wrapped dynamic strings and entries to insert.
    scratch: HeapList[u8]
    // The most scratch one field can need: max(table, list).
    scratch_max: int
    // Set while a block decodes and left set when it fails: the table may
    // be out of step with the peer's.
    broken: bool
}

/// A decoder whose dynamic table holds at most table_bytes, the maximum the
/// peer's encoder may use (SETTINGS_HEADER_TABLE_SIZE as it will be in
/// force; DefaultTableBytes until the first SETTINGS is acknowledged), and
/// whose blocks decode to at most list_bytes of list size (RFC 7541 4.1).
/// The table starts with capacity table_bytes. Values below 0 are 0;
/// table_bytes is held to 2^32 - 1 and list_bytes too. Fails with
/// OutOfMemory. O(table_bytes); allocates from a, once, the byte ring
/// (table_bytes), its entry records (table_bytes / 32 × 16 bytes) and a
/// scratch buffer of min(4 KiB, its bound). Decode grows the scratch buffer
/// up to table_bytes + 64 KiB, and a block that could need more gets a
/// buffer of its own for its length.
export Decoder.New(a: &Alloc, table_bytes: int, list_bytes: int): OutOfMemory!Decoder {
    var t := try Table.New(a, table_bytes)
    onerr t.Free(a)
    lb := if list_bytes < 0 { 0 } else if list_bytes > MaxListBytes { MaxListBytes } else { list_bytes }
    smax := if t.max > lb { t.max } else { lb }
    var scratch := HeapList[u8].New(a)
    onerr scratch.Free(a)
    try Zeroes(a, mut scratch, if smax < ScratchStart { smax } else { ScratchStart })
    max := t.max
    return Decoder(table: t, lim: Limits(limit: max, need_update: false, low: max, list_bytes: lb), scratch: scratch, scratch_max: smax, broken: false)
}

/// Releases the decoder's buffers. O(1).
export Decoder.Free(self, a: &Alloc) {
    Decoder(table, scratch, ..) := self
    table.Free(a)
    scratch.Free(a)
}

/// Sets the decoder's maximum table size when a new
/// SETTINGS_HEADER_TABLE_SIZE takes effect (when the peer acknowledges it),
/// clamped to [0, New's table_bytes]. When it is below the table's
/// capacity, the next block must start with a size update to at most the
/// lowest maximum set since the last block (a maximum lowered and raised
/// again between two blocks asks for the lowest, then the final), or Decode
/// fails with MissingSizeUpdate or BadSizeUpdate (RFC 7541 4.2, RFC 9113
/// 4.3.1). O(1); allocates nothing.
export Decoder.SetMaxTableSize(mut self, size: int) {
    l := if size < 0 { 0 } else if size > self.table.max { self.table.max } else { size }
    self.lim.limit = l
    if l < self.table.cap {
        self.lim.low = if self.lim.need_update && self.lim.low < l { self.lim.low } else { l }
        self.lim.need_update = true
    }
}

/// The dynamic table's size (RFC 7541 4.1: names and values plus 32 per
/// entry). O(1).
export Decoder.TableSize(&self): int {
    return self.table.size
}

/// The number of dynamic-table entries. O(1).
export Decoder.TableLen(&self): int {
    return self.table.count
}

/// Decodes one whole header block into sink, field by field, in order,
/// with views valid for each Add call only. a is New's allocator: it grows
/// the scratch buffer, and the sink gets it. The result is Outcome.Fits, or
/// Outcome.TooLarge when the list passed list_bytes; then the block was
/// still read to its end and the table is in sync. Errors: DecodeError's,
/// each at the block offset of the representation that broke the rule, and
/// OutOfMemory from a or from sink; after any of them the table may be out
/// of step and every later call fails with Unusable. O(block bytes +
/// decoded bytes), where past the list budget only a field inserted under
/// a dynamic name still costs its name's length (at most table_bytes); a
/// prefix integer is at most 10 bytes. Allocates nothing once scratch has
/// grown for blocks of up to about 40 KB; a longer block takes one buffer
/// for its length and gives it back.
export Decoder.Decode[S: Sink](mut self, a: &Alloc, sink: mut S, block: []u8): DecodeError|OutOfMemory!Outcome {
    if self.broken {
        fail Unusable
    }
    self.broken = true
    // The most scratch a field of this block can need (see Run).
    need := self.table.max + HuffMaxOut(block.Len())
    bound := if need < self.scratch_max { need } else { self.scratch_max }
    o := if bound <= self.table.max + ScratchKeep {
        try Run(mut self.table, mut self.lim, mut self.scratch, a, sink, block, bound)
    } else {
        var big := HeapList[u8].New(a)
        onerr big.Free(a)
        r := try Run(mut self.table, mut self.lim, mut big, a, sink, block, bound)
        big.Free(a)
        r
    }
    self.broken = false
    return o
}

// Run is Decode's pass over the block, with sc as scratch, grown up to max:
// at most the budget or the table's size per field, and at most the
// table's size plus the Huffman bound of the block's bytes, since a field's
// strings are a dynamic entry's (at most the table's size) or come from the
// block.
Run[S: Sink](t: mut Table, lim: mut Limits, sc: mut HeapList[u8], a: &Alloc, sink: mut S, block: []u8, max: int): DecodeError|OutOfMemory!Outcome {
    var budget := Budget(used: 0, limit: lim.list_bytes, over: false)
    var started := false
    var at := 0
    n := block.Len()
    for at < n {
        b := block[at]
        if b & 0xE0 == 0x20 {
            // Dynamic table size update (6.3): only before the first field,
            // and the first after a lowered maximum at most the lowest.
            (value, next) := try GetInt(block, at, 5)
            bound := if lim.need_update { lim.low } else { lim.limit }
            if started || value > bound {
                fail BadSizeUpdate(offset: at, size: value)
            }
            t.SetCap(value)
            lim.need_update = false
            at = next
            continue
        }
        if lim.need_update {
            fail MissingSizeUpdate
        }
        started = true
        if b & 0x80 != 0 {
            // Indexed header field (6.1).
            (value, next) := try GetInt(block, at, 7)
            name := try IndexSrc(&*t, at, value, 0)
            val := Src(kind: name.kind, i: name.i, part: 1, start: 0, end: 0, at: at)
            try Emit(t, sc, a, sink, block, mut budget, name, val, false, false, max)
            at = next
        } else if b & 0xC0 == 0x40 {
            // Literal with incremental indexing (6.2.1).
            at = try Literal(t, sc, a, sink, block, mut budget, at, 6, true, false, max)
        } else {
            // Without indexing (6.2.2) or never indexed (6.2.3).
            at = try Literal(t, sc, a, sink, block, mut budget, at, 4, false, b & 0x10 != 0, max)
        }
    }
    if lim.need_update {
        fail MissingSizeUpdate
    }
    return if budget.over { Outcome.TooLarge } else { Outcome.Fits }
}

// IndexSrc is the name (part 0) or value (part 1) of the entry of HPACK
// index index, read at at: static for 1 to 61, then t newest first
// (2.3.3). Fails with BadIndex for 0 and past the tables.
IndexSrc(t: &Table, at: int, index: int, part: int): BadIndex!Src {
    if index >= 1 && index <= StaticLen {
        return Src(kind: SrcStatic, i: index, part: part, start: 0, end: 0, at: at)
    }
    if index <= StaticLen || index - StaticLen - 1 >= t.count {
        fail BadIndex(offset: at, index: index)
    }
    abs := t.inserted - 1 - (index - StaticLen - 1)
    return Src(kind: SrcDynamic, i: abs, part: part, start: 0, end: 0, at: at)
}

// Literal decodes the literal field at at whose index has a p-bit prefix
// (6.2): its name, by index or literal, then its value. index says whether
// it is added to the dynamic table, never_index whether it is never
// indexed. It returns the offset after the field.
Literal[S: Sink](t: mut Table, sc: mut HeapList[u8], a: &Alloc, sink: mut S, block: []u8, budget: mut Budget, at: int, p: int, index: bool, never_index: bool, max: int): DecodeError|OutOfMemory!int {
    (value: ni, next) := try GetInt(block, at, p)
    var pos := next
    name := if ni == 0 {
        (huffman: nh, start: ns, end: ne) := try StrHead(block, pos, 7)
        pos = ne
        Src(kind: if nh { SrcHuffman } else { SrcRaw }, i: 0, part: 0, start: ns, end: ne, at: next)
    } else {
        try IndexSrc(&*t, at, ni, 0)
    }
    (huffman, start, end) := try StrHead(block, pos, 7)
    val := Src(kind: if huffman { SrcHuffman } else { SrcRaw }, i: 0, part: 1, start: start, end: end, at: pos)
    try Emit(t, sc, a, sink, block, budget, name, val, index, never_index, max)
    return end
}

// Emit gives the field (name, value) to sink when it fits the budget, and
// adds it to t when index is set. Once the budget is passed it hands over
// nothing and only Remembers the field.
Emit[S: Sink](t: mut Table, sc: mut HeapList[u8], a: &Alloc, sink: mut S, block: []u8, budget: mut Budget, name: Src, val: Src, index: bool, never_index: bool, max: int): BadHuffman|OutOfMemory!void {
    room := budget.limit - budget.used - EntryOverhead
    if budget.over || room < 0 {
        budget.over = true
        try Remember(t, sc, a, block, name, val, index, max)
        return
    }
    // Scratch grows before each string is placed. A Loc is an offset, not
    // a view, so a growth for the value keeps the name; no view is taken
    // until both are placed. The value's reserve is within what the name
    // left of room, so scratch never passes room.
    rn := Need(&*t, name, room, index)
    try Grow(sc, a, rn, max)
    nl := try Place(&*t, sc.ItemsMut(), block, name, 0, rn, room, index)
    if ln := nl {
        vroom := room - ln.len
        vat := if ln.where == InScratch { ln.start + ln.len } else { 0 }
        rv := Need(&*t, val, vroom, false)
        try Grow(sc, a, vat + rv, max)
        vl := try Place(&*t, sc.ItemsMut(), block, val, vat, rv, vroom, false)
        if lv := vl {
            budget.used += EntrySize(ln.len, lv.len)
            nb := LocView(&*t, sc.Items(), block, ln)
            vb := LocView(&*t, sc.Items(), block, lv)
            nidx := NameIndexOf(&*t, name, nb)
            try sink.Add(a, nb, vb, never_index, nidx)
            if index {
                t.Insert(NoRing(sc.Items(), block, ln), NoRing(sc.Items(), block, lv), nidx)
            }
            return
        }
    }
    budget.over = true
    try Remember(t, sc, a, block, name, val, index, max)
}

// NameIndexOf is the static index of a field's name: from the static
// entry, from the dynamic entry's record, else by one probe of the name's
// bytes b.
NameIndexOf(t: &Table, s: Src, b: []u8): int {
    return match s.kind {
        SrcStatic { int(FirstIndex[s.i]) }
        SrcDynamic { t.NameIndex(s.i) }
        _ { StaticNameIndex(b) }
    }
}

// Need is the scratch bytes s may need when placed within room: a Huffman
// string's output bound, a dynamic string that wraps or must be copied
// (force), else none.
Need(t: &Table, s: Src, room: int, force: bool): int {
    return match s.kind {
        SrcHuffman {
            bound := HuffMaxOut(s.end - s.start)
            if bound < room { bound } else { room }
        }
        SrcDynamic {
            at := if s.part == 0 { t.NameAt(s.i) } else { t.ValueAt(s.i) }
            len := if s.part == 0 { t.NameLen(s.i) } else { t.ValueLen(s.i) }
            if len > room { 0 } else if force || !t.Whole(at, len) { len } else { 0 }
        }
        _ { 0 }
    }
}

// Place places s within room, using buf from at for cap bytes (Need's
// count): none when s is longer than room. Fails with BadHuffman for a bad
// Huffman string.
Place(t: &Table, buf: mut []u8, block: []u8, s: Src, at: int, cap: int, room: int, force: bool): BadHuffman!?Loc {
    match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            len := if s.part == 0 { e.name.Len() } else { e.value.Len() }
            if len > room {
                return none
            }
            return some(Loc(where: InStatic, i: s.i, part: s.part, start: 0, len: len))
        }
        SrcRaw {
            len := s.end - s.start
            if len > room {
                return none
            }
            return some(Loc(where: InBlock, i: 0, part: 0, start: s.start, len: len))
        }
        SrcDynamic {
            len := if s.part == 0 { t.NameLen(s.i) } else { t.ValueLen(s.i) }
            if len > room {
                return none
            }
            if !force && cap == 0 {
                start := if s.part == 0 { t.NameAt(s.i) } else { t.ValueAt(s.i) }
                return some(Loc(where: InRing, i: 0, part: 0, start: start, len: len))
            }
            CopyEntry(t, mut buf[at..at + len], s)
            return some(Loc(where: InScratch, i: 0, part: 0, start: at, len: len))
        }
        _ {
            src := block[s.start..s.end]
            if got := HuffDecode(mut buf[at..at + cap], src) {
                // cap is at most room, so got is too.
                return some(Loc(where: InScratch, i: 0, part: 0, start: at, len: got))
            }
            if HuffValid(src).IsNone() {
                fail BadHuffman(offset: s.at)
            }
            return none
        }
    }
}

// Grow makes sc at least need bytes long (at most max, which every
// caller's need is within). Amortised: it at least doubles.
Grow(sc: mut HeapList[u8], a: &Alloc, need: int, max: int): OutOfMemory!void {
    have := sc.Len()
    if need <= have {
        return
    }
    var to := if have * 2 > need { have * 2 } else { need }
    if to > max {
        to = max
    }
    try Zeroes(a, sc, to)
}

// Length is s's decoded length. Fails with BadHuffman for a bad Huffman
// string. O(s) for a Huffman string, else O(1).
Length(t: &Table, block: []u8, s: Src): BadHuffman!int {
    return match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            if s.part == 0 { e.name.Len() } else { e.value.Len() }
        }
        SrcDynamic {
            if s.part == 0 { t.NameLen(s.i) } else { t.ValueLen(s.i) }
        }
        SrcRaw { s.end - s.start }
        _ {
            HuffValid(block[s.start..s.end]) else {
                fail BadHuffman(offset: s.at)
            }
        }
    }
}

// Remember handles a field past the budget: it checks its strings and,
// when index is set, adds it to t, its bytes put together in sc; an entry
// larger than the capacity only empties the table (4.4).
Remember(t: mut Table, sc: mut HeapList[u8], a: &Alloc, block: []u8, name: Src, val: Src, index: bool, max: int): BadHuffman|OutOfMemory!void {
    nl := try Length(&*t, block, name)
    vl := try Length(&*t, block, val)
    if !index {
        return
    }
    need := EntrySize(nl, vl)
    if need > t.cap {
        _ = t.Fit(need)
        return
    }
    // The entry fits the capacity, so both strings fit scratch's bound.
    // They are copied before Fit evicts: the name may be an entry it evicts.
    try Grow(sc, a, nl + vl, max)
    nidx := match name.kind {
        SrcStatic { int(FirstIndex[name.i]) }
        SrcDynamic { t.NameIndex(name.i) }
        _ { -1 }
    }
    buf := sc.ItemsMut()
    Fill(&*t, mut buf[0..nl], block, name)
    Fill(&*t, mut buf[nl..nl + vl], block, val)
    _ = t.Fit(need)
    nb := sc.Items()[0..nl]
    t.Add(nb, sc.Items()[nl..nl + vl], if nidx < 0 { StaticNameIndex(nb) } else { nidx })
}

// Fill copies s's bytes, of which Length has checked the length and the
// Huffman code, into out; a dynamic string comes from t.
Fill(t: &Table, out: mut []u8, block: []u8, s: Src) {
    match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            out.CopyFrom(if s.part == 0 { e.name.AsBytes() } else { e.value.AsBytes() })
        }
        SrcDynamic { CopyEntry(t, out, s) }
        SrcRaw { out.CopyFrom(block[s.start..s.end]) }
        _ { _ = HuffDecode(out, block[s.start..s.end]) }
    }
}

// CopyEntry copies the name or value of the dynamic entry s names into
// out, which has its length.
CopyEntry(t: &Table, out: mut []u8, s: Src) {
    if s.part == 0 {
        t.CopyName(s.i, out)
    } else {
        t.CopyValue(s.i, out)
    }
}

// LocView is the bytes of l: static data, or a view of block, of t's byte
// ring or of scratch.
LocView(t: &Table, scratch: []u8, block: []u8, l: Loc): []u8 from t, scratch, block {
    return match l.where {
        InRing { t.Ring(l.start, l.len) }
        _ { NoRing(scratch, block, l) }
    }
}

// NoRing is LocView for a string that is not in the byte ring, which Emit
// ensures for every string it inserts (a dynamic name it inserts is copied
// to scratch).
NoRing(scratch: []u8, block: []u8, l: Loc): []u8 from scratch, block {
    return match l.where {
        InStatic {
            e := StaticTable[l.i - 1]
            if l.part == 0 { e.name.AsBytes() } else { e.value.AsBytes() }
        }
        InBlock { block[l.start..l.start + l.len] }
        InScratch { scratch[l.start..l.start + l.len] }
        _ { Panic("NoRing: a string in the byte ring") }
    }
}
