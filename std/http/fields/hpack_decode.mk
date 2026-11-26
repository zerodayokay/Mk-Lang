// Why: an HTTP/2 endpoint decodes every header block the peer sends; the
// block, its sizes and its indexes are the peer's, so the decoder must be
// bounded and total, and must keep its dynamic table in sync with the
// peer's even when it refuses the block's fields.
//
// What: Decoder, with New, Free, Decode, SetMaxTable, TableSize and
// TableLen (RFC 7541 3, 4, 6).
//
// How: one pass over a whole block. Each field's name and value are first
// described (Src: a static entry, a dynamic entry, raw bytes or Huffman
// bytes of the block), then copied once into the caller's arena, within
// what is left of the list budget, and given to the sink. A static string
// is given as a view, uncopied. Once a field would pass the budget, the
// decoder stops copying and adding but reads on: an entry it must still
// insert is put together in a scratch buffer of the table's size (a larger
// one cannot be inserted anyway), so the table ends where the peer's does.

// What a Src describes.
const SrcStatic = 0
const SrcDynamic = 1
const SrcRaw = 2
const SrcHuffman = 3

// A name or value before it is copied: a static entry's (index i, value
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

// The list budget of one block: the decoded list size so far, its limit,
// and whether a field has passed it.
struct Budget {
    used: int
    limit: int
    over: bool
}

/// An HPACK decoder: the dynamic table of one direction of one connection,
/// and the limits it enforces. Linear: it owns heap memory; release it with
/// Free.
export linear view struct Decoder {
    table: Table
    // The maximum table size in force (RFC 7541 4.2): New's table_bytes,
    // or SetMaxTable's.
    limit: int
    // Whether the next block must start with a size update to at most
    // limit.
    need_update: bool
    list_bytes: int
    // Where an entry is put together while the list is over its budget.
    scratch: HeapList[u8]
}

/// A decoder whose dynamic table holds at most table_bytes, the maximum the
/// peer's encoder may use (SETTINGS_HEADER_TABLE_SIZE as it will be in
/// force; DefaultTableBytes until the first SETTINGS is acknowledged), and
/// whose blocks decode to at most list_bytes of list size (RFC 7541 4.1).
/// The table starts with capacity table_bytes. Values below 0 are 0. Fails
/// with OutOfMemory. O(table_bytes); allocates about 2 × table_bytes +
/// table_bytes / 32 × 12 bytes from a, once.
export Decoder.New(a: &Alloc, table_bytes: int, list_bytes: int): OutOfMemory!Decoder from a {
    var t := try Table.New(a, table_bytes)
    onerr t.Free(a)
    var scratch := HeapList[u8].New(a)
    onerr scratch.Free(a)
    for scratch.Len() < t.max {
        k := if t.max - scratch.Len() < Zeros.Len() { t.max - scratch.Len() } else { Zeros.Len() }
        try scratch.Extend(a, Zeros[0..k])
    }
    lb := if list_bytes < 0 { 0 } else { list_bytes }
    max := t.max
    return Decoder(table: t, limit: max, need_update: false, list_bytes: lb, scratch: scratch)
}

/// Releases the decoder's tables. O(1).
export Decoder.Free(self, a: &Alloc) {
    Decoder(table, scratch, ..) := self
    table.Free(a)
    scratch.Free(a)
}

/// Sets the decoder's maximum table size when a new
/// SETTINGS_HEADER_TABLE_SIZE takes effect (when the peer acknowledges it),
/// clamped to [0, New's table_bytes]. When it is below the table's
/// capacity, the next block must start with a size update to at most size,
/// or Decode fails with MissingSizeUpdate. O(1); allocates nothing.
export Decoder.SetMaxTable(mut self, size: int) {
    self.limit = if size < 0 { 0 } else if size > self.table.max { self.table.max } else { size }
    if self.limit < self.table.cap {
        self.need_update = true
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

/// Decodes one whole header block into sink, field by field, in order. A
/// literal, and a string of the dynamic table, is copied once into a; a
/// static-table string is a view of static data. Errors: DecodeError's, each
/// at the block offset of the representation that broke the rule;
/// OutOfMemory from a or from sink. After any error but ListTooLarge the
/// decoder must not be used again. The list budget is checked before each
/// copy. O(block bytes + decoded bytes); a prefix integer is at most 10
/// bytes. Allocates in a only: the strings it gives the sink, a Huffman one
/// taking at most 8/5 of its coded length.
export Decoder.Decode[S: Sink](mut self, a: &Arena into sink, sink: mut S, block: []u8): DecodeError|OutOfMemory!void {
    var budget := Budget(used: 0, limit: self.list_bytes, over: false)
    var started := false
    var at := 0
    n := block.Len()
    for at < n {
        b := block[at]
        if b & 0xE0 == 0x20 {
            // Dynamic table size update (6.3): only before the first field.
            (value, next) := try GetInt(block, at, 5)
            if started || value > self.limit {
                fail BadSizeUpdate(offset: at, size: value)
            }
            self.table.SetCap(value)
            self.need_update = false
            at = next
            continue
        }
        if self.need_update {
            fail MissingSizeUpdate
        }
        started = true
        if b & 0x80 != 0 {
            // Indexed header field (6.1).
            (value, next) := try GetInt(block, at, 7)
            name := try self.Indexed(at, value, 0)
            val := Src(kind: name.kind, i: name.i, part: 1, start: 0, end: 0, at: at)
            try self.Emit(a, sink, block, mut budget, name, val, false, false)
            at = next
        } else if b & 0xC0 == 0x40 {
            // Literal with incremental indexing (6.2.1).
            at = try self.Literal(a, sink, block, mut budget, at, 6, true, false)
        } else {
            // Without indexing (6.2.2) or never indexed (6.2.3).
            at = try self.Literal(a, sink, block, mut budget, at, 4, false, b & 0x10 != 0)
        }
    }
    if self.need_update {
        fail MissingSizeUpdate
    }
    if budget.over {
        fail ListTooLarge(limit: budget.limit)
    }
}

// Decoder.Indexed is the name (part 0) or value (part 1) of the entry of
// HPACK index index, read at at: static for 1 to 61, then the dynamic
// table newest first (2.3.3). Fails with BadIndex for 0 and past the
// tables.
Decoder.Indexed(&self, at: int, index: int, part: int): BadIndex!Src {
    if index >= 1 && index <= StaticLen {
        return Src(kind: SrcStatic, i: index, part: part, start: 0, end: 0, at: at)
    }
    if index <= StaticLen || index - StaticLen - 1 >= self.table.count {
        fail BadIndex(offset: at, index: index)
    }
    abs := self.table.inserted - 1 - (index - StaticLen - 1)
    return Src(kind: SrcDynamic, i: abs, part: part, start: 0, end: 0, at: at)
}

// Decoder.Literal decodes the literal field at at whose index has a p-bit
// prefix (6.2): its name, by index or literal, then its value. index says
// whether it is added to the dynamic table, never whether it is never
// indexed. It returns the offset after the field.
Decoder.Literal[S: Sink](mut self, a: &Arena into sink, sink: mut S, block: []u8, budget: mut Budget, at: int, p: int, index: bool, never_index: bool): DecodeError|OutOfMemory!int {
    (value: ni, next) := try GetInt(block, at, p)
    var pos := next
    name := if ni == 0 {
        (huffman: nh, start: ns, end: ne) := try StrHead(block, pos, 7)
        pos = ne
        Src(kind: if nh { SrcHuffman } else { SrcRaw }, i: 0, part: 0, start: ns, end: ne, at: next)
    } else {
        try self.Indexed(at, ni, 0)
    }
    (huffman, start, end) := try StrHead(block, pos, 7)
    val := Src(kind: if huffman { SrcHuffman } else { SrcRaw }, i: 0, part: 1, start: start, end: end, at: pos)
    try self.Emit(a, sink, block, budget, name, val, index, never_index)
    return end
}

// Decoder.Emit gives the field (name, value) to sink when it fits the
// budget, and adds it to the dynamic table when index is set. Once the
// budget is passed it only checks the strings and, when index is set,
// adds the entry from scratch.
Decoder.Emit[S: Sink](mut self, a: &Arena into sink, sink: mut S, block: []u8, budget: mut Budget, name: Src, val: Src, index: bool, never_index: bool): BadHuffman|OutOfMemory!void {
    if !budget.over {
        room := budget.limit - budget.used - EntryOverhead
        n := try self.Copy(a, block, name, room)
        if nb := n {
            v := try self.Copy(a, block, val, room - nb.Len())
            if vb := v {
                budget.used += EntrySize(nb.Len(), vb.Len())
                try sink.Add(nb, vb, never_index)
                if index {
                    self.table.Insert(nb, vb)
                }
                return
            }
        }
        budget.over = true
    }
    try self.Remember(block, name, val, index)
}

// Decoder.Copy is s's bytes in a (a view, for a static string), or none
// when they are more than room bytes. Fails with BadHuffman for a bad
// Huffman string. O(s); allocates at most min(s's length bound, room)
// bytes in a.
Decoder.Copy(&self, a: &Arena, block: []u8, s: Src, room: int): BadHuffman|OutOfMemory!?[]u8 from a {
    if room < 0 {
        return none
    }
    match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            b := if s.part == 0 { e.name.AsBytes() } else { e.value.AsBytes() }
            if b.Len() > room {
                return none
            }
            return some(b)
        }
        SrcDynamic {
            len := if s.part == 0 { self.table.NameLen(s.i) } else { self.table.ValueLen(s.i) }
            if len > room {
                return none
            }
            out := try a.Slice[u8](len)
            if s.part == 0 {
                self.table.CopyName(s.i, out)
            } else {
                self.table.CopyValue(s.i, out)
            }
            return some(out[..])
        }
        SrcRaw {
            len := s.end - s.start
            if len > room {
                return none
            }
            out := try a.Slice[u8](len)
            out.CopyFrom(block[s.start..s.end])
            return some(out[..])
        }
        _ {
            src := block[s.start..s.end]
            bound := HuffMaxOut(src.Len())
            out := try a.Slice[u8](if bound < room { bound } else { room })
            if got := HuffDecode(out, src) {
                return some(out[0..got])
            }
            if HuffValid(src).IsNone() {
                fail BadHuffman(offset: s.at)
            }
            return none
        }
    }
}

// Decoder.Length is s's decoded length. Fails with BadHuffman for a bad
// Huffman string. O(s) for a Huffman string, else O(1).
Decoder.Length(&self, block: []u8, s: Src): BadHuffman!int {
    return match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            if s.part == 0 { e.name.Len() } else { e.value.Len() }
        }
        SrcDynamic {
            if s.part == 0 { self.table.NameLen(s.i) } else { self.table.ValueLen(s.i) }
        }
        SrcRaw { s.end - s.start }
        _ {
            HuffValid(block[s.start..s.end]) else {
                fail BadHuffman(offset: s.at)
            }
        }
    }
}

// Decoder.Remember checks the field's strings and, when index is set,
// adds the field to the dynamic table, its bytes put together in scratch;
// an entry larger than the capacity only empties the table (4.4).
Decoder.Remember(mut self, block: []u8, name: Src, val: Src, index: bool): BadHuffman!void {
    nl := try self.Length(block, name)
    vl := try self.Length(block, val)
    if !index {
        return
    }
    need := EntrySize(nl, vl)
    if need > self.table.cap {
        _ = self.table.Fit(need)
        return
    }
    // The entry fits the capacity, so both strings fit scratch. They are
    // copied before Fit evicts: the name may be an entry it evicts (4.4).
    buf := self.scratch.ItemsMut()
    Fill(&self.table, mut buf[0..nl], block, name)
    Fill(&self.table, mut buf[nl..nl + vl], block, val)
    _ = self.table.Fit(need)
    self.table.Add(self.scratch.Items()[0..nl], self.scratch.Items()[nl..nl + vl])
}

// Fill copies s's bytes, of which Decoder.Length has checked the length
// and the Huffman code, into out; a dynamic string comes from t.
Fill(t: &Table, out: mut []u8, block: []u8, s: Src) {
    match s.kind {
        SrcStatic {
            e := StaticTable[s.i - 1]
            out.CopyFrom(if s.part == 0 { e.name.AsBytes() } else { e.value.AsBytes() })
        }
        SrcDynamic {
            if s.part == 0 {
                t.CopyName(s.i, out)
            } else {
                t.CopyValue(s.i, out)
            }
        }
        SrcRaw {
            out.CopyFrom(block[s.start..s.end])
        }
        _ {
            _ = HuffDecode(out, block[s.start..s.end])
        }
    }
}
