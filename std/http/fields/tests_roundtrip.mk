// Why: the examples cover a few blocks; an encoder and a decoder that
// disagree on one eviction or one index only show it many blocks later,
// so they must agree over long generated sequences.
//
// What: Rng, a seeded xorshift generator; BField, a field of any bytes;
// and the round-trip tests: generated lists through Encode and Write, with
// table sizes changed between blocks, decoded back exactly, the two
// tables equal after every block.
//
// How: names come from the static table, a small pool (so the dynamic
// table is hit) and random bytes; values from the static table, a pool and
// random bytes of any value; one field in eight is sensitive.

// A seeded xorshift64 generator.
struct Rng {
    s: u64
}

Rng.Next(mut self): u64 {
    var x := self.s
    x = x ^ (x << 13)
    x = x ^ (x >> 7)
    x = x ^ (x << 17)
    self.s = x
    return x
}

// Rng.Below is a value in [0, n), n above 0.
Rng.Below(mut self, n: int): int {
    return Cast[int](self.Next() % Cast[u64](n))
}

// A field of any bytes.
struct BField {
    name: []u8
    value: []u8
    sensitive: bool
}

impl Field for BField {
    Name(&self): []u8 {
        return self.name
    }

    Value(&self): []u8 {
        return self.value
    }

    Sensitive(&self): bool {
        return self.sensitive
    }
}

// The pools that make the dynamic table hit.
const Pool = ["x-trace", "x-request-id", "cookie", "user-agent", "x-a"]
const Values = ["", "0", "gzip, deflate", "Mozilla/5.0 (X11)", "id=12345; path=/"]

// Bytes is n random bytes in a; lower case letters and '-' when name.
Bytes(a: &Arena, r: mut Rng, n: int, name: bool): OutOfMemory![]u8 from a {
    out := try a.Slice[u8](n)
    for i in 0..n {
        out[i] = if name { Trunc[u8](if r.Below(27) == 26 { 0x2D } else { 0x61 + r.Below(26) }) } else { Trunc[u8](r.Next()) }
    }
    return out[..]
}

// RandomList is up to max random fields in a.
RandomList(a: &Arena, r: mut Rng, max: int): OutOfMemory![]BField from a {
    var out := List[BField].New(a)
    n := r.Below(max + 1)
    for _ in 0..n {
        name := match r.Below(3) {
            0 { StaticTable[r.Below(StaticLen)].name.AsBytes() }
            1 { Pool[r.Below(Pool.Len())].AsBytes() }
            _ { try Bytes(a, r, 1 + r.Below(12), true) }
        }
        value := match r.Below(3) {
            0 { StaticTable[r.Below(StaticLen)].value.AsBytes() }
            1 { Values[r.Below(Values.Len())].AsBytes() }
            _ { try Bytes(a, r, r.Below(60), false) }
        }
        try out.Push(BField(name: name, value: value, sensitive: r.Below(8) == 0))
    }
    return out.Done()[..]
}

// A Sink that copies every field, any bytes, for the round trips.
struct RawBlock {
    bytes: HeapList[u8]
    spans: HeapList[Span]
}

impl Sink for RawBlock {
    Add(mut self, a: &Alloc, name: []u8, value: []u8, never_index: bool, name_index: int): OutOfMemory!void {
        at := self.bytes.Len()
        try self.bytes.Extend(a, name)
        try self.bytes.Extend(a, value)
        try self.spans.Push(a, Span(at: Cast[u32](at), name: Cast[u32](name.Len()), value: Cast[u32](value.Len()), never_index: never_index, index: Trunc[u8](name_index)))
    }
}

// RawBlock.Free releases its buffers.
RawBlock.Free(self, a: &Alloc) {
    RawBlock(bytes, spans) := self
    bytes.Free(a)
    spans.Free(a)
}

// Matches is whether got holds exactly list, never-indexed flags and
// static name indexes included.
Matches(got: &RawBlock, list: []BField): bool {
    if got.spans.Len() != list.Len() {
        return false
    }
    b := got.bytes.Items()
    for i, f in &list {
        s := got.spans[i]
        at := int(s.at)
        mid := at + int(s.name)
        if !BytesEq(b[at..mid], f.name) || !BytesEq(b[mid..mid + int(s.value)], f.value) || s.never_index != f.sensitive {
            return false
        }
        if int(s.index) != StaticNameIndex(f.name) {
            return false
        }
    }
    return true
}
// Sequence runs blocks generated blocks from seed through one encoder and
// one decoder of table size size, Huffman on or off, changing the table
// size between some blocks, and checks each block and both tables.
Sequence(sys: &System, seed: u64, size: int, huffman: bool, blocks: int): DecodeError|OutOfMemory|TooSmall!void {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, size, huffman)
    defer enc.Free(h)
    var dec := try Decoder.New(h, size, 1 << 20)
    defer dec.Free(h)
    var r := Rng(s: seed)
    for b in 0..blocks {
        var ar := try Arena.New(sys.heap)
        defer ar.Free()
        a := &ar
        if r.Below(6) == 0 {
            // A new SETTINGS_HEADER_TABLE_SIZE, acknowledged before the block.
            ms := r.Below(size + 1)
            dec.SetMaxTableSize(ms)
            enc.SetMaxTableSize(ms)
        }
        list := try RandomList(a, mut r, 12)
        out := try a.Slice[u8](enc.Bound(list))
        n := if b % 2 == 0 {
            try enc.Encode(out, list)
        } else {
            // The streaming form, with the modes Encode would choose.
            var o := try enc.Start(out)
            for f in &list {
                o += try enc.Write(mut out[o..], f.name, f.value, if f.sensitive { Mode.Never } else { Mode.Index })
            }
            o
        }
        var got := RawBlock(bytes: HeapList[u8].New(h), spans: HeapList[Span].New(h))
        defer got.Free(h)
        o := try dec.Decode(h, mut got, out[0..n])
        Assert(o == Outcome.Fits && Matches(&got, list))
        Assert(dec.TableSize() == enc.TableSize() && dec.TableLen() == enc.TableLen())
    }
}

test "generated lists round-trip through Huffman encoders and decoders of 4096 bytes" (sys: &System) {
    for seed in 1..41 {
        try Sequence(sys, Cast[u64](seed) *% 0x9E37_79B9_7F4A_7C15, DefaultTableBytes, true, 12)
    }
}

test "generated lists round-trip without Huffman and through small tables" (sys: &System) {
    sizes := [0, 31, 32, 64, 100, 256]
    for seed in 1..31 {
        try Sequence(sys, Cast[u64](seed) *% 0xD1B5_4A32_D192_ED03, sizes[seed % sizes.Len()], seed % 2 == 0, 12)
    }
}
