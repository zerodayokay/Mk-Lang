// Why: a decoder lives as long as its connection and its peer picks every
// length, so the memory it holds between blocks must not follow the peer's
// largest string, a failed allocation must never leave it decoding with a
// table out of step, and a warm decoder or encoder must allocate nothing.
//
// What: the scratch bound and its return after a long block; an
// OutOfMemory at every allocation of a sequence of blocks; the allocation
// count of warm decoding into a reused FieldBlock and of warm encoding.
//
// How: the decoder, its blocks and the encoder take their memory from
// std.mem's Counting (bytes and peak) or Failing (fails one numbered
// request; with a negative number it fails none and counts them).

import std.mem

// HuffZeros is a block of one literal without indexing whose name and
// value are each n bytes of Huffman code for '0' (five zero bits a
// symbol): 8n / 5 bytes decoded apiece.
HuffZeros(a: &Arena, n: int): OutOfMemory![]u8 from a {
    var head: [12]u8 = [0; 12]
    k := PutInt(mut head[..], n, 7, 0x80)
    out := try a.Slice[u8](1 + 2 * (k + n))
    out[0] = 0x00
    (mut out[1..1 + k]).CopyFrom(head[0..k])
    (mut out[1 + k + n..1 + 2 * k + n]).CopyFrom(head[0..k])
    return out[..]
}

test "scratch for one field stays within the list budget, and a block that grew it past 64 KiB gives it back" (sys: &System) {
    a := sys.arena
    c := mem.Counting.New(sys.heap.Alloc())
    ca := c.Alloc()
    list := 1 << 18
    var dec := try Decoder.New(ca, DefaultTableBytes, list)
    defer dec.Free(ca)
    var got := FieldBlock.New(ca)
    defer got.Free(ca)
    // 200,000 coded bytes a string, 320,000 decoded: each alone passes the
    // budget, so only the name is ever reserved for, within the budget.
    Assert(try dec.Decode(ca, mut got, try HuffZeros(a, 200000)) == Outcome.TooLarge)
    Assert(got.Len() == 0)
    Assert(c.Peak() < 2 * list)
    // The block grew scratch past ScratchKeep: it is back to ScratchStart.
    Assert(dec.scratch.Len() == ScratchStart && c.Bytes() < ScratchKeep)
    // A long field within the budget reaches the sink, and scratch is
    // given back after it too: 50,000 coded bytes, 80,000 decoded apiece.
    Assert(try dec.Decode(ca, mut got, try HuffZeros(a, 50000)) == Outcome.Fits)
    Assert(got.Len() == 1 && got.Get(0).name.Len() == 80000 && got.Get(0).value.Len() == 80000)
    Assert(dec.scratch.Len() == ScratchStart)
}

test "an OutOfMemory at any allocation leaves the decoder Unusable, and never a wrong field" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var wires := List[[]u8].New(a)
    for x in C6 {
        try wires.Push(try Unhex(a, x))
    }
    // HuffZeros(5000) needs scratch past its first 4 KiB.
    try wires.Push(try HuffZeros(a, 5000))
    var at := 0
    var fails := 0
    for {
        f := mem.Failing.New(h, at)
        fa := f.Alloc()
        at += 1
        var dec := Decoder.New(fa, 256, 1 << 16) else {
            continue
        }
        var got := FieldBlock.New(fa)
        var broken := false
        for i, w in &wires {
            got.Reset()
            match dec.Decode(fa, mut got, *w) {
                ok(o) {
                    Assert(!broken)
                    if i < 3 {
                        Assert(o == Outcome.Fits && PairsAre(&got, L6[i]))
                    } else {
                        Assert(o == Outcome.Fits && got.Len() == 1 && got.Get(0).value.Len() == 8000)
                    }
                }
                err(e) {
                    match e {
                        OutOfMemory {
                            Assert(!broken)
                            broken = true
                            fails += 1
                        }
                        Unusable { Assert(broken) }
                        _ { Assert(false) }
                    }
                }
            }
        }
        got.Free(fa)
        dec.Free(fa)
        if f.Failed() == 0 {
            break
        }
    }
    // Some request inside Decode, not only in New, was failed.
    Assert(fails > 0)
}

test "a warm decoder into a reused FieldBlock and a warm encoder allocate nothing" (sys: &System) {
    a := sys.arena
    f := mem.Failing.New(sys.heap.Alloc(), -1)
    fa := f.Alloc()
    var dec := try Decoder.New(fa, 256, 1 << 16)
    defer dec.Free(fa)
    var enc := try Encoder.New(fa, 256, true)
    defer enc.Free(fa)
    var got := FieldBlock.New(fa)
    defer got.Free(fa)
    var wires := List[[]u8].New(a)
    for x in C6 {
        try wires.Push(try Unhex(a, x))
    }
    var lists := List[[]TField].New(a)
    for l in L6 {
        try lists.Push(try ToFields(a, l))
    }
    out := try a.Slice[u8](1024)
    var before := 0
    for round in 0..3 {
        if round == 2 {
            before = f.Requests()
        }
        // Each round from empty tables, as the RFC's sequence starts.
        dec.table.SetCap(0)
        dec.table.SetCap(256)
        enc.table.SetCap(0)
        enc.table.SetCap(256)
        for i in 0..3 {
            got.Reset()
            Assert(try dec.Decode(fa, mut got, wires[i]) == Outcome.Fits)
            _ = try enc.Encode(out, lists[i])
            _ = try enc.EncodeBlock(out, &got)
        }
    }
    Assert(f.Requests() == before && f.Failed() == 0)
}
