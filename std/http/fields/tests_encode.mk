// Why: the encoder decides what the peer's table holds and writes into
// the caller's buffer, so a short buffer must change nothing, a sensitive
// field must never be indexed, and a table-size change must reach the
// peer as RFC 7541 4.2 asks.
//
// What: tests of Encoder: TooSmall from Encode, Start and Write with
// nothing changed; size updates after SetMaxTable, read back by a decoder;
// sensitive fields; the dynamic and static choices; Huffman only when not
// longer.
//
// How: each test encodes, and where it matters decodes the bytes again
// with a Decoder and compares the two tables.

test "Encode into a buffer shorter than Bound is TooSmall and changes nothing" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    lines := ["custom-key: custom-value", ":path: /x"]
    fs := try ToFields(a, lines[..])
    need := enc.Bound(fs)
    var out: [8]u8 = [0; 8]
    match enc.Encode(mut out[..], fs) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                TooSmall(need: n) { Assert(n == need) }
            }
        }
    }
    Assert(enc.TableLen() == 0 && out[0] == 0)
    big := try a.Slice[u8](need)
    n := try enc.Encode(big, fs)
    Assert(n <= need && enc.TableLen() == 2)
}

test "Write and Start into a short buffer are TooSmall and change nothing" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    var out: [4]u8 = [0; 4]
    match enc.Write(mut out[..], "custom-key".AsBytes(), "v".AsBytes(), Mode.Index) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                TooSmall(need) { Assert(need == 1 + 11 + 2) }
            }
        }
    }
    Assert(enc.TableLen() == 0)
    enc.SetMaxTableSize(100)
    match enc.Start(mut out[0..1]) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                TooSmall(need) { Assert(need == 2) }
            }
        }
    }
    Assert(try enc.Start(mut out[..]) == 2)
    Assert(out[0] == 0x3F && out[1] == 0x45)
    Assert(try enc.Start(mut out[..]) == 0)
}

test "a lowered then raised table size sends the smallest size, then the final one" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    lines := ["custom-key: custom-header"]
    fs := try ToFields(a, lines[..])
    var out: [128]u8 = [0; 128]
    n := try enc.Encode(mut out[..], fs)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, out[0..n])
    Assert(enc.TableLen() == 1 && dec.TableLen() == 1)
    enc.SetMaxTableSize(0)
    enc.SetMaxTableSize(4096)
    Assert(enc.TableLen() == 0)
    m := try enc.Encode(mut out[..], fs)
    // 20: size 0; 3f e1 1f: size 4096; then the field, indexed again.
    Assert(out[0] == 0x20 && out[1] == 0x3F && out[2] == 0xE1 && out[3] == 0x1F)
    try Fits(h, mut dec, mut got, out[0..m])
    Assert(dec.TableLen() == 1 && dec.TableSize() == enc.TableSize())
    Assert(got.Len() == 1)
}

test "the same table size again sends no update" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    enc.SetMaxTableSize(DefaultTableBytes)
    enc.SetMaxTableSize(1 << 20)
    var out: [8]u8 = [0; 8]
    Assert(try enc.Start(mut out[..]) == 0)
}

test "a sensitive field is never indexed, and the decoder says so" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    fs := [TField(name: "authorization", value: "secret", sensitive: true), TField(name: ":method", value: "GET", sensitive: true)]
    var out: [64]u8 = [0; 64]
    for _ in 0..2 {
        n := try enc.Encode(mut out[..], fs[..])
        // authorization is static name 23: 0x1f 0x08 (never indexed, index 23).
        Assert(out[0] == 0x1F && out[1] == 0x08)
        // :method GET is static 2, sent as a never-indexed literal anyway.
        var got := FieldBlock.New(h)
        defer got.Free(h)
        try Fits(h, mut dec, mut got, out[0..n])
        Assert(got.Len() == 2 && got.Get(0).sensitive && got.Get(1).sensitive)
        Assert(got.Get(1).value == "GET" && got.Get(1).name_index == 2 && got.Get(0).name_index == 23)
    }
    Assert(enc.TableLen() == 0 && dec.TableLen() == 0)
}

test "a field the dynamic table holds is one index byte, newest first" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    var out: [64]u8 = [0; 64]
    _ = try enc.Write(mut out[..], "x-a".AsBytes(), "1".AsBytes(), Mode.Index)
    _ = try enc.Write(mut out[..], "x-b".AsBytes(), "2".AsBytes(), Mode.Index)
    Assert(try enc.Write(mut out[..], "x-a".AsBytes(), "1".AsBytes(), Mode.NoIndex) == 1)
    Assert(out[0] == 0xBF)
    Assert(try enc.Write(mut out[..], "x-b".AsBytes(), "2".AsBytes(), Mode.Index) == 1)
    Assert(out[0] == 0xBE)
    // A new value of a dynamic name names the entry: 0x7e is index 62.
    Assert(try enc.Write(mut out[..], "x-b".AsBytes(), "3".AsBytes(), Mode.Index) == 3)
    Assert(out[0] == 0x7E && out[1] == 0x01 && out[2] == b'3')
    Assert(enc.TableLen() == 3)
}

test "an entry over half the table is sent without indexing" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, 100, false)
    defer enc.Free(h)
    var out: [64]u8 = [0; 64]
    // 1 + 18 + 32 = 51 bytes of entry, above 50.
    n := try enc.Write(mut out[..], "x".AsBytes(), "aaaaaaaaaaaaaaaaaa".AsBytes(), Mode.Index)
    Assert(out[0] == 0x00 && n == 1 + 2 + 19)
    Assert(enc.TableLen() == 0)
}

test "Huffman is used when its code is not longer than the raw bytes" (sys: &System) {
    h := sys.heap.Alloc()
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    var out: [64]u8 = [0; 64]
    // "307" codes to 3 bytes, as long as raw: Huffman (RFC 7541 C.6.2).
    n := try enc.Write(mut out[..], ":status".AsBytes(), "307".AsBytes(), Mode.NoIndex)
    Assert(n == 5 && out[0] == 0x08 && out[1] == 0x83)
    // Bytes above 0x7f code to 20 bits or more: raw.
    hi: [2]u8 = [0xF0, 0xF1]
    m := try enc.Write(mut out[..], ":path".AsBytes(), hi[..], Mode.NoIndex)
    Assert(m == 4 && out[1] == 0x02)
}
