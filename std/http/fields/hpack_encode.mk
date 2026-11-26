// Why: an HTTP/2 endpoint encodes every header block it sends; the
// encoder decides what the peer's dynamic table holds, so its choices
// cost compression and memory on both sides, and it must never write past
// the caller's buffer or leave its table out of step with what it wrote.
//
// What: Encoder, with New, Free, SetMaxTableSize, Bound, BlockBound,
// StartBound, FieldBound, Encode, EncodeBlock, Start, Write, TableSize and
// TableLen (RFC 7541 4, 6).
//
// How: each field is looked up in the static table (one probe) and in two
// direct-mapped caches of the dynamic table, by (name, value) and by
// name, each 256 slots of an absolute index; a slot is checked against the
// table before use, so a stale or colliding slot only costs compression.
// The representation and its exact length are decided before anything is
// written, so a buffer that is too short changes nothing.

// The caches' size; a power of two.
const CacheSlots: u32 = 256

/// An HPACK encoder: the dynamic table the peer's decoder mirrors. Linear:
/// it owns heap memory; release it with Free. Send and Share: it holds only
/// owned buffers and numbers.
export linear view struct Encoder {
    table: Table
    huffman: bool
    // Whether size updates are owed at the next Start, and the smallest
    // capacity since the last one (RFC 7541 4.2).
    pending: bool
    low: int
    // The newest absolute index plus 1 of each slot's (name, value) and
    // name; 0 for none.
    pairs: [256]int
    names: [256]int
}

/// An encoder whose table starts with capacity table_bytes, which must be
/// the peer decoder's starting size (DefaultTableBytes for HTTP/2), and
/// never holds more than table_bytes even when the peer allows more.
/// huffman Huffman-codes each string whose code is not longer than its raw
/// bytes (5.2); false always sends raw bytes. Values below 0 are 0. Fails
/// with OutOfMemory. O(table_bytes); allocates about table_bytes +
/// table_bytes / 32 × 16 bytes from a, once; the struct holds two 2 KiB
/// caches.
export Encoder.New(a: &Alloc, table_bytes: int, huffman: bool): OutOfMemory!Encoder from a {
    t := try Table.New(a, table_bytes)
    return Encoder(table: t, huffman: huffman, pending: false, low: 0, pairs: [0; 256], names: [0; 256])
}

/// Releases the encoder's tables. O(1).
export Encoder.Free(self, a: &Alloc) {
    Encoder(table, ..) := self
    table.Free(a)
}

/// The dynamic table's size (RFC 7541 4.1). O(1).
export Encoder.TableSize(&self): int {
    return self.table.size
}

/// The number of dynamic-table entries. O(1).
export Encoder.TableLen(&self): int {
    return self.table.count
}

/// Takes the peer's new SETTINGS_HEADER_TABLE_SIZE, between blocks: the
/// capacity becomes min(size, New's table_bytes), and the next Start writes
/// the size updates RFC 7541 4.2 asks for: the smallest capacity since the
/// last block when it is below the final one, then the final one. O(entries
/// evicted); allocates nothing.
export Encoder.SetMaxTableSize(mut self, size: int) {
    c := if size < 0 { 0 } else if size > self.table.max { self.table.max } else { size }
    if c == self.table.cap && !self.pending {
        return
    }
    self.low = if self.pending && self.low < c { self.low } else { c }
    self.pending = true
    self.table.SetCap(c)
}

/// An upper bound of the bytes Encode writes for list, whatever the tables
/// hold: StartBound plus each field's FieldBound. O(fields); allocates
/// nothing.
export Encoder.Bound[F: Field](&self, list: []F): int {
    var n := self.StartBound()
    for f in &list {
        n += self.FieldBound(f.Name(), f.Value())
    }
    return n
}

/// An upper bound of the bytes Start writes. O(1); allocates nothing.
export Encoder.StartBound(&self): int {
    return 2 * IntLen(self.table.max, 5)
}

/// An upper bound of the bytes Write writes for (name, value) in any mode,
/// whatever the tables hold: the longest index, then both strings raw (a
/// Huffman string is never longer). O(1); allocates nothing.
export Encoder.FieldBound(&self, name: []u8, value: []u8): int {
    nl := name.Len()
    vl := value.Len()
    return IntLen(StaticLen + self.table.ents.Len(), 4) + IntLen(nl, 7) + nl + IntLen(vl, 7) + vl
}

/// Encodes one whole header block of list into out, from out[0], and
/// returns the length written: the size updates owed (Start), then each
/// field in order, in Mode.Never when it is Sensitive and Mode.Index
/// otherwise. Fails with TooSmall(need) when out is shorter than
/// Bound(list), and then writes nothing and changes nothing. O(Σ name +
/// value); one static probe and two cache probes per field; allocates
/// nothing.
export Encoder.Encode[F: Field](mut self, out: mut []u8, list: []F): TooSmall!int {
    need := self.Bound(list)
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    // Bound covers the updates and every field in its longest form, so no
    // step below can find out short.
    var o := self.Start(out).Must()
    for f in &list {
        mode := if f.Sensitive() { Mode.Never } else { Mode.Index }
        o += self.Write(mut out[o..], f.Name(), f.Value(), mode).Must()
    }
    return o
}

/// An upper bound of the bytes EncodeBlock writes for b, whatever the
/// tables hold. O(fields); allocates nothing.
export Encoder.BlockBound(&self, b: &FieldBlock): int {
    var n := self.StartBound()
    for f in b.Iter() {
        n += self.FieldBound(f.name.AsBytes(), f.value.AsBytes())
    }
    return n
}

/// Encode for the fields of a FieldBlock, a never-indexed field again never
/// indexed (RFC 7541 7.1.3), as a proxy forwards them. Fails with
/// TooSmall(need) when out is shorter than BlockBound(b), and then writes
/// nothing and changes nothing. O(Σ name + value); allocates nothing.
export Encoder.EncodeBlock(mut self, out: mut []u8, b: &FieldBlock): TooSmall!int {
    need := self.BlockBound(b)
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    // BlockBound covers every step, as Bound does for Encode.
    var o := self.Start(out).Must()
    for f in b.Iter() {
        mode := if f.sensitive { Mode.Never } else { Mode.Index }
        o += self.Write(mut out[o..], f.name.AsBytes(), f.value.AsBytes(), mode).Must()
    }
    return o
}
/// Writes the size updates owed since the last block into out, from
/// out[0], and returns their length (0 when none is owed). Call it first in
/// every block built with Write. Fails with TooSmall(need) when out is too
/// short, and then changes nothing. O(1); allocates nothing.
export Encoder.Start(mut self, out: mut []u8): TooSmall!int {
    if !self.pending {
        return 0
    }
    c := self.table.cap
    two := self.low < c
    need := if two { IntLen(self.low, 5) + IntLen(c, 5) } else { IntLen(c, 5) }
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    var o := 0
    if two {
        o = PutInt(out, self.low, 5, 0x20)
    }
    o += PutInt(mut out[o..], c, 5, 0x20)
    self.pending = false
    return o
}

/// Writes one field in mode into out, from out[0], and returns its length;
/// the dynamic table and the caches change as the peer's decoder will. The
/// name is written as given. Fails with TooSmall(need) when out is too
/// short, and then changes nothing. O(name + value); allocates nothing.
export Encoder.Write(mut self, out: mut []u8, name: []u8, value: []u8, mode: Mode): TooSmall!int {
    (index: sidx, exact) := StaticFind(name, value)
    nh := Fnv(FnvBasis, name)
    pslot := PairSlot(nh, value)
    if mode != Mode.Never {
        if exact {
            return try Indexed(out, sidx)
        }
        if abs := self.Cached(self.pairs[pslot], name, value, true) {
            return try Indexed(out, self.Index(abs))
        }
    }
    nslot := int(nh & (CacheSlots - 1))
    var nidx := sidx
    if nidx == 0 {
        if abs := self.Cached(self.names[nslot], name, value, false) {
            nidx = self.Index(abs)
        }
    }
    insert := mode == Mode.Index && EntrySize(name.Len(), value.Len()) <= self.table.cap / 2
    (p, flags) := match mode {
        .Never { (p: 4, flags: Trunc[u8](0x10)) }
        _ {
            if insert { (p: 6, flags: Trunc[u8](0x40)) } else { (p: 4, flags: Trunc[u8](0x00)) }
        }
    }
    nc := StrCost(name, 7, self.huffman)
    vc := StrCost(value, 7, self.huffman)
    need := IntLen(nidx, p) + (if nidx == 0 { nc.len } else { 0 }) + vc.len
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    var o := PutInt(out, nidx, p, flags)
    if nidx == 0 {
        o += PutStr(mut out[o..], name, 7, 0, nc.huff)
    }
    o += PutStr(mut out[o..], value, 7, 0, vc.huff)
    if insert {
        abs := self.table.inserted
        self.table.Insert(name, value, if sidx == 0 { 0 } else { int(FirstIndex[sidx]) })
        self.pairs[pslot] = abs + 1
        self.names[nslot] = abs + 1
    }
    return o
}

// Encoder.Cached is the absolute index of cache slot value slot when it
// still names an entry of the table with this name (and this value, when
// both is set).
Encoder.Cached(&self, slot: int, name: []u8, value: []u8, both: bool): ?int {
    abs := slot - 1
    if slot == 0 || !self.table.Has(abs) || !self.table.NameIs(abs, name) {
        return none
    }
    if both && !self.table.ValueIs(abs, value) {
        return none
    }
    return some(abs)
}

// Encoder.Index is the HPACK index of the entry of absolute index abs
// (2.3.3: the newest dynamic entry is 62).
Encoder.Index(&self, abs: int): int {
    return StaticLen + self.table.inserted - abs
}

// Indexed writes an indexed field (6.1) of index into out.
Indexed(out: mut []u8, index: int): TooSmall!int {
    need := IntLen(index, 7)
    if out.Len() < need {
        fail TooSmall(need: need)
    }
    return PutInt(out, index, 7, 0x80)
}

// Fnv is FNV-1a over b, from h.
Fnv(h: u32, b: []u8): u32 {
    var x := h
    for c in b {
        x = (x ^ u32(c)) *% 16777619
    }
    return x
}

// The FNV-1a offset basis.
const FnvBasis: u32 = 2166136261

// PairSlot is the cache slot of a (name, value) from the name's hash nh:
// FNV-1a goes on over a separator byte that no name holds, then the value.
// A name's own slot is nh's low bits.
PairSlot(nh: u32, value: []u8): int {
    sep: [1]u8 = [0x0A]
    return int(Fnv(Fnv(nh, sep[..]), value) & (CacheSlots - 1))
}
