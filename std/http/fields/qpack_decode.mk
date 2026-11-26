// Why: HTTP/3 receives QPACK-encoded field sections on request and control
// streams from an untrusted peer; the decoder must reconstruct Required Insert
// Count across modulo wrap-around, track blocked streams within max_blocked,
// enforce list_bytes without dropping out of validation, and emit transient
// views to the caller's Sink without allocating on warm sections.
//
// What: QOutcome, QDecoder, QDecoder.New, QDecoder.Free, QDecoder.Decode,
// QDecoder.Unblocked, QDecoder.Cancel, QDecoder.TableSize, QDecoder.TableLen,
// and QDecoder.InsertCount.
//
// How: reuses Table, prefix.mk, huffman.mk and hpack_decode.mk's Src/Loc/Budget
// model. Decode parses the section prefix (Required Insert Count + Base),
// checks for blocking against table.inserted, then walks every field line
// representation (static/dynamic indexed, post-base indexed, name-ref literal,
// post-base name-ref literal, literal name) into a Sink. Blocked sections are
// recorded by (stream, ric) so Instructions/Unblocked can wake them.

// A blocked field section waiting for table.inserted to reach ric.
struct QBlocked {
    stream: i64
    ric: int
}

// An acknowledged field section awaiting Section Acknowledgment emission.
struct QAck {
    stream: i64
    ric: int
}

// Prefix-integer limits in RFC 9204 4.1.1 (up to 62 bits).
const QMaxInt: int = 0x3FFF_FFFF_FFFF_FFFF

// Pre-allocated capacity headroom for owed decoder instructions when Cancel
// is called without an allocator.
const QCancelHeadroom = 64

/// The outcome of QDecoder.Decode: Fits when all fields reached the sink,
/// TooLarge when the decoded list size passed list_bytes (the whole section
/// was still validated), or Blocked when Required Insert Count exceeds the
/// inserts received so far.
export enum QOutcome {
    Fits
    TooLarge
    Blocked
}

/// The QPACK decoder of one HTTP/3 connection: its dynamic table (capacity
/// initially 0, bounded by max_capacity), its blocked-stream list (at most
/// max_blocked), and owed decoder-stream instructions. Linear: owns heap
/// buffers; release with Free. Send and Share.
export linear view struct QDecoder {
    table: Table
    scratch: HeapList[u8]
    scratch_max: int
    max_blocked: int
    list_bytes: int
    known_received: int
    blocked: HeapList[QBlocked]
    acks: HeapList[QAck]
    cancels: HeapList[i64]
    cancel_head: int
    cancel_count: int
}

/// A QPACK decoder whose dynamic table starts at capacity 0 and may grow up
/// to max_capacity (what this side advertised in
/// SETTINGS_QPACK_MAX_TABLE_CAPACITY), holding at most max_blocked blocked
/// streams and decoding sections up to list_bytes of decoded list size.
/// Values below 0 are clamped to 0; max_capacity and list_bytes are held to
/// 2^32 - 1. Errors: OutOfMemory. O(max_capacity + max_blocked); allocates
/// from a once.
export QDecoder.New(a: &Alloc, max_capacity: int, max_blocked: int, list_bytes: int): OutOfMemory!QDecoder {
    var t := try Table.New(a, max_capacity)
    onerr t.Free(a)
    // RFC 9204 3.2.2: initial dynamic table capacity is zero.
    t.SetCap(0)
    mb := if max_blocked < 0 { 0 } else { max_blocked }
    lb := if list_bytes < 0 { 0 } else if list_bytes > MaxListBytes { MaxListBytes } else { list_bytes }
    smax := if t.max > lb { t.max } else { lb }
    var sc := HeapList[u8].New(a)
    onerr sc.Free(a)
    try Zeroes(a, mut sc, if smax < ScratchStart { smax } else { ScratchStart })
    var blk := HeapList[QBlocked].New(a)
    onerr blk.Free(a)
    var acks := HeapList[QAck].New(a)
    onerr acks.Free(a)
    var cancels := HeapList[i64].New(a)
    onerr cancels.Free(a)
    if mb > 0 {
        for _ in 0..(mb * 2 + QCancelHeadroom) {
            try cancels.Push(a, 0)
        }
    }
    return QDecoder(
        table: t,
        scratch: sc,
        scratch_max: smax,
        max_blocked: mb,
        list_bytes: lb,
        known_received: 0,
        blocked: blk,
        acks: acks,
        cancels: cancels,
        cancel_head: 0,
        cancel_count: 0,
    )
}

/// Releases the decoder's buffers. O(1).
export QDecoder.Free(self, a: &Alloc) {
    QDecoder(table, scratch, blocked, acks, cancels, ..) := self
    table.Free(a)
    scratch.Free(a)
    blocked.Free(a)
    acks.Free(a)
    cancels.Free(a)
}

/// The dynamic table's current size in octets (RFC 9204 3.2.1). O(1).
export QDecoder.TableSize(&self): int {
    return self.table.size
}

/// The number of entries currently in the dynamic table. O(1).
export QDecoder.TableLen(&self): int {
    return self.table.count
}

/// Total number of dynamic-table insertions and duplications processed so far.
/// O(1).
export QDecoder.InsertCount(&self): int {
    return self.table.inserted
}

/// Decodes one QPACK field section for stream into sink, calling Sink.Add in
/// section order with name_index set to (first QPACK static index + 1), or 0
/// when not in the static table. If Required Insert Count exceeds the inserts
/// received so far, records stream as blocked (up to max_blocked) and returns
/// QOutcome.Blocked. Errors: QpackError (BadInteger maps to a stream error
/// on a request stream per RFC 9204 7.4; all others are connection errors),
/// OutOfMemory. O(section + decoded bytes); allocates nothing once warm.
export QDecoder.Decode[S: Sink](mut self, a: &Alloc, sink: mut S, stream: i64, section: []u8): QpackError|OutOfMemory!QOutcome {
    if section.Len() == 0 {
        fail Truncated(offset: 0)
    }
    (value: enc_ric, next: base_at) := try QGetInt(section, 0, 8)
    ric := try DecodeRIC(self.table.max, self.table.inserted, enc_ric)
    if base_at >= section.Len() {
        fail Truncated(offset: base_at)
    }
    sign := (section[base_at] & 0x80) != 0
    (value: delta_base, next: pos) := try QGetInt(section, base_at, 7)
    base := if sign {
        if ric <= delta_base {
            fail BadBase(offset: base_at)
        }
        ric - delta_base - 1
    } else {
        if delta_base > QMaxInt - ric {
            fail BadBase(offset: base_at)
        }
        ric + delta_base
    }
    if ric > self.table.inserted {
        // Section is blocked waiting for encoder-stream inserts.
        for i in 0..self.blocked.Len() {
            if self.blocked[i].stream == stream {
                self.blocked[i] = QBlocked(stream: stream, ric: ric)
                return QOutcome.Blocked
            }
        }
        if self.blocked.Len() >= self.max_blocked {
            fail BlockedLimit
        }
        try self.blocked.Push(a, QBlocked(stream: stream, ric: ric))
        return QOutcome.Blocked
    }
    need := self.table.max + HuffMaxOut(section.Len())
    bound := if need < self.scratch_max { need } else { self.scratch_max }
    over := if bound <= self.table.max + ScratchKeep {
        try QRun(&self.table, mut self.scratch, a, sink, section, pos, ric, base, self.list_bytes, bound)
    } else {
        var big := HeapList[u8].New(a)
        onerr big.Free(a)
        r := try QRun(&self.table, mut big, a, sink, section, pos, ric, base, self.list_bytes, bound)
        big.Free(a)
        r
    }
    if ric > 0 {
        try self.acks.Push(a, QAck(stream: stream, ric: ric))
    }
    return if over { QOutcome.TooLarge } else { QOutcome.Fits }
}

/// Writes stream ids whose previously blocked sections can now decode into
/// out, removes them from the blocked set, and returns the count written.
/// O(blocked); allocates nothing.
export QDecoder.Unblocked(mut self, out: mut []i64): int {
    var count := 0
    var i := 0
    for i < self.blocked.Len() && count < out.Len() {
        if self.blocked[i].ric <= self.table.inserted {
            out[count] = self.blocked[i].stream
            count += 1
            _ = self.blocked.Remove(i)
        } else {
            i += 1
        }
    }
    return count
}

/// Marks stream as reset or abandoned: removes any blocked entry for stream
/// (freeing its blocked slot) and queues a Stream Cancellation instruction if
/// stream referenced the dynamic table. O(blocked + owed); allocates nothing.
export QDecoder.Cancel(mut self, stream: i64) {
    var referenced := false
    var i := 0
    for i < self.blocked.Len() {
        if self.blocked[i].stream == stream {
            _ = self.blocked.Remove(i)
            referenced = true
        } else {
            i += 1
        }
    }
    var j := 0
    for j < self.acks.Len() {
        if self.acks[j].stream == stream {
            _ = self.acks.Remove(j)
            referenced = true
        } else {
            j += 1
        }
    }
    if referenced && self.cancel_count < self.cancels.Len() {
        slot := (self.cancel_head + self.cancel_count) % self.cancels.Len()
        self.cancels.ItemsMut()[slot] = stream
        self.cancel_count += 1
    }
}

// QGetInt reads an n-bit prefix integer up to 62 bits (RFC 9204 4.1.1).
QGetInt(src: []u8, at: int, n: int): Truncated|BadInteger!(value: int, next: int) {
    r := try GetInt(src, at, n)
    if r.value > QMaxInt {
        fail BadInteger(offset: at)
    }
    return r
}

// QStrHead reads a string literal header with an n-bit length prefix and
// Huffman flag above it (RFC 9204 4.1.2).
QStrHead(src: []u8, at: int, n: int): Truncated|BadInteger!(huffman: bool, start: int, end: int) {
    if at < 0 || at >= src.Len() {
        fail Truncated(offset: at)
    }
    huffman := (int(src[at]) >> n) & 1 == 1
    (value, next) := try QGetInt(src, at, n)
    if value > src.Len() - next {
        fail Truncated(offset: at)
    }
    return (huffman, start: next, end: next + value)
}

// DecodeRIC reconstructs Required Insert Count using RFC 9204 4.5.1.1's
// modulo wrap-around algorithm.
DecodeRIC(max_cap: int, total_inserts: int, enc: int): BadRequiredInsertCount!int {
    if enc == 0 {
        return 0
    }
    max_entries := max_cap / EntryOverhead
    full_range := 2 * max_entries
    if enc > full_range {
        fail BadRequiredInsertCount(offset: 0)
    }
    max_value := total_inserts + max_entries
    max_wrapped := (max_value / full_range) * full_range
    var ric := max_wrapped + enc - 1
    if ric > max_value {
        if ric <= full_range {
            fail BadRequiredInsertCount(offset: 0)
        }
        ric -= full_range
    }
    if ric == 0 {
        fail BadRequiredInsertCount(offset: 0)
    }
    return ric
}

// QStaticSrc builds a Src pointing to QPACK static entry idx (0..98).
QStaticSrc(at: int, idx: int, part: int): BadIndex!Src {
    if idx < 0 || idx >= QStaticLen {
        fail BadIndex(offset: at, index: idx)
    }
    return Src(kind: SrcStatic, i: idx, part: part, start: 0, end: 0, at: at)
}

// QDynamicSrc builds a Src pointing to dynamic entry abs, checking
// 0 <= abs < ric and that abs has not been evicted (RFC 9204 2.2.3).
QDynamicSrc(t: &Table, at: int, idx: int, abs: int, ric: int, part: int): BadIndex!Src {
    if abs < 0 || abs >= ric || !t.Has(abs) {
        fail BadIndex(offset: at, index: idx)
    }
    return Src(kind: SrcDynamic, i: abs, part: part, start: 0, end: 0, at: at)
}

// QRun decodes all field line representations in section[pos..] against t.
QRun[S: Sink](t: &Table, sc: mut HeapList[u8], a: &Alloc, sink: mut S, section: []u8, pos: int, ric: int, base: int, list_bytes: int, max: int): QpackError|OutOfMemory!bool {
    var budget := Budget(used: 0, limit: list_bytes, over: false)
    var at := pos
    n := section.Len()
    for at < n {
        b := section[at]
        if b & 0x80 != 0 {
            // 4.5.2: Indexed Field Line ('1' T + 6-bit index).
            stat := (b & 0x40) != 0
            (value: idx, next) := try QGetInt(section, at, 6)
            name := if stat {
                try QStaticSrc(at, idx, 0)
            } else {
                if idx > base {
                    fail BadIndex(offset: at, index: idx)
                }
                try QDynamicSrc(t, at, idx, base - 1 - idx, ric, 0)
            }
            val := Src(kind: name.kind, i: name.i, part: 1, start: 0, end: 0, at: at)
            try QEmit(t, sc, a, sink, section, mut budget, name, val, false, max)
            at = next
        } else if b & 0xC0 == 0x40 {
            // 4.5.4: Literal Field Line with Name Reference ('01' N T + 4-bit index).
            never_index := (b & 0x20) != 0
            stat := (b & 0x10) != 0
            (value: idx, next) := try QGetInt(section, at, 4)
            name := if stat {
                try QStaticSrc(at, idx, 0)
            } else {
                if idx > base {
                    fail BadIndex(offset: at, index: idx)
                }
                try QDynamicSrc(t, at, idx, base - 1 - idx, ric, 0)
            }
            (huffman, start, end) := try QStrHead(section, next, 7)
            val := Src(kind: if huffman { SrcHuffman } else { SrcRaw }, i: 0, part: 1, start: start, end: end, at: next)
            try QEmit(t, sc, a, sink, section, mut budget, name, val, never_index, max)
            at = end
        } else if b & 0xE0 == 0x20 {
            // 4.5.6: Literal Field Line with Literal Name ('001' N H + 3-bit length).
            never_index := (b & 0x10) != 0
            (huffman: nh, start: ns, end: ne) := try QStrHead(section, at, 3)
            name := Src(kind: if nh { SrcHuffman } else { SrcRaw }, i: 0, part: 0, start: ns, end: ne, at: at)
            (huffman: vh, start: vs, end: ve) := try QStrHead(section, ne, 7)
            val := Src(kind: if vh { SrcHuffman } else { SrcRaw }, i: 0, part: 1, start: vs, end: ve, at: ne)
            try QEmit(t, sc, a, sink, section, mut budget, name, val, never_index, max)
            at = ve
        } else if b & 0xF0 == 0x10 {
            // 4.5.3: Indexed Field Line with Post-Base Index ('0001' + 4-bit index).
            (value: idx, next) := try QGetInt(section, at, 4)
            if idx > ric - base {
                fail BadIndex(offset: at, index: idx)
            }
            name := try QDynamicSrc(t, at, idx, base + idx, ric, 0)
            val := Src(kind: SrcDynamic, i: name.i, part: 1, start: 0, end: 0, at: at)
            try QEmit(t, sc, a, sink, section, mut budget, name, val, false, max)
            at = next
        } else {
            // 4.5.5: Literal Field Line with Post-Base Name Reference ('0000' N + 3-bit index).
            never_index := (b & 0x08) != 0
            (value: idx, next) := try QGetInt(section, at, 3)
            if idx > ric - base {
                fail BadIndex(offset: at, index: idx)
            }
            name := try QDynamicSrc(t, at, idx, base + idx, ric, 0)
            (huffman, start, end) := try QStrHead(section, next, 7)
            val := Src(kind: if huffman { SrcHuffman } else { SrcRaw }, i: 0, part: 1, start: start, end: end, at: next)
            try QEmit(t, sc, a, sink, section, mut budget, name, val, never_index, max)
            at = end
        }
    }
    return budget.over
}

// QEmit places (name, val) within budget and hands views to sink; past the
// budget it validates any Huffman strings without writing to sink.
QEmit[S: Sink](t: &Table, sc: mut HeapList[u8], a: &Alloc, sink: mut S, section: []u8, budget: mut Budget, name: Src, val: Src, never_index: bool, max: int): BadHuffman|OutOfMemory!void {
    room := budget.limit - budget.used - EntryOverhead
    if budget.over || room < 0 {
        budget.over = true
        _ = try QLength(t, section, name)
        _ = try QLength(t, section, val)
        return
    }
    rn := Need(t, name, room, false)
    try Grow(sc, a, rn, max)
    nl := try QPlace(t, sc.ItemsMut(), section, name, 0, rn, room)
    if ln := nl {
        vroom := room - ln.len
        vat := if ln.where == InScratch { ln.start + ln.len } else { 0 }
        rv := Need(t, val, vroom, false)
        try Grow(sc, a, vat + rv, max)
        vl := try QPlace(t, sc.ItemsMut(), section, val, vat, rv, vroom)
        if lv := vl {
            budget.used += EntrySize(ln.len, lv.len)
            nb := QLocView(t, sc.Items(), section, ln)
            vb := QLocView(t, sc.Items(), section, lv)
            nidx := QNameIndexOf(t, name, nb)
            try sink.Add(a, nb, vb, never_index, nidx)
            return
        }
    }
    budget.over = true
    _ = try QLength(t, section, name)
    _ = try QLength(t, section, val)
}

// QNameIndexOf returns (first QPACK static index + 1) for name, or 0.
QNameIndexOf(t: &Table, s: Src, b: []u8): int {
    return match s.kind {
        SrcStatic { int(QFirstIndex[s.i]) }
        SrcDynamic { t.NameIndex(s.i) }
        _ { QStaticNameIndex(b) }
    }
}

// QPlace places a QPACK string s within room.
QPlace(t: &Table, buf: mut []u8, section: []u8, s: Src, at: int, cap: int, room: int): BadHuffman!?Loc {
    if s.kind == SrcStatic {
        e := QStaticTable[s.i]
        len := if s.part == 0 { e.name.Len() } else { e.value.Len() }
        if len > room {
            return none
        }
        return some(Loc(where: InStatic, i: s.i, part: s.part, start: 0, len: len))
    }
    return try Place(t, buf, section, s, at, cap, room, false)
}

// QLength checks and returns s's decoded length.
QLength(t: &Table, section: []u8, s: Src): BadHuffman!int {
    if s.kind == SrcStatic {
        e := QStaticTable[s.i]
        return if s.part == 0 { e.name.Len() } else { e.value.Len() }
    }
    return try Length(t, section, s)
}

// QLocView returns a view of l from QStaticTable, section, t's ring or scratch.
QLocView(t: &Table, scratch: []u8, section: []u8, l: Loc): []u8 from t, scratch, section {
    if l.where == InStatic {
        e := QStaticTable[l.i]
        return if l.part == 0 { e.name.AsBytes() } else { e.value.AsBytes() }
    }
    return LocView(t, scratch, section, l)
}
