// Why: FieldBlock is the sink std.http keeps, so it must hold every field
// exactly, bytes that are not UTF-8 included, give each its static name
// index however it was sent, tell text from other bytes, and be reusable;
// and a view of the dynamic table that wraps the ring's end must still
// reach the sink whole.
//
// What: tests of FieldBlock (Get, Text, Iter, Reset), of the name indexes
// the decoder gives, and of a wrapped dynamic entry.
//
// How: blocks are encoded by an Encoder or written as hex, decoded into a
// FieldBlock, and read back.

test "FieldBlock keeps the fields in order with their flags, and Reset empties it" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // :method GET, then password: secret never indexed (C.2.3).
    try Fits(h, mut dec, mut got, try Unhex(a, "82 1008 7061 7373 776f 7264 0673 6563 7265 74"))
    Assert(got.Len() == 2 && got.Bytes() == 7 + 3 + 8 + 6)
    var it := got.Iter()
    first := it.Next().Must()
    Assert(Same(first.name, ":method") && Same(first.value, "GET") && !first.sensitive && first.name_index == 2)
    second := it.Next().Must()
    Assert(Same(second.name, "password") && Same(second.value, "secret") && second.sensitive && second.name_index == 0)
    Assert(it.Next().IsNone())
    text := got.Text(1).Must()
    Assert(text.name == "password" && text.value == "secret" && text.sensitive && text.name_index == 0)
    got.Reset()
    Assert(got.Len() == 0 && got.Bytes() == 0)
    try Fits(h, mut dec, mut got, try Unhex(a, "82"))
    Assert(got.Len() == 1 && Same(got.Get(0).value, "GET"))
}

test "a field that is not UTF-8 is kept as its bytes, and Text refuses it" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // :path with the value ff (not UTF-8), added to the table; then
    // content-length 5 without indexing, and x-v: a, obs-text e9, b (RFC
    // 9110 5.5).
    try Fits(h, mut dec, mut got, try Unhex(a, "44 01ff 0f0d 0135 00 0378 2d76 0361 e962"))
    Assert(got.Len() == 3)
    path := got.Get(0)
    Assert(Same(path.name, ":path") && path.value.Len() == 1 && path.value[0] == 0xFF && path.name_index == 4)
    Assert(got.Text(0).IsNone())
    Assert(got.Text(1).Must().name == "content-length" && got.Text(1).Must().value == "5")
    obs := got.Get(2)
    Assert(Same(obs.name, "x-v") && obs.value.Len() == 3 && obs.value[1] == 0xE9)
    Assert(got.Text(2).IsNone())
    // The entry is in the table as it came.
    Assert(dec.TableLen() == 1 && dec.TableSize() == 5 + 1 + 32)
}

test "the static name index comes with every field, however it was sent" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    lines := [":status: 307", "cache-control: private", "x-custom: 1", "date: today"]
    fs := try ToFields(a, lines[..])
    var out: [256]u8 = [0; 256]
    // First by static name and literal; then cache-control and x-custom come
    // back from the dynamic table.
    for _ in 0..2 {
        n := try enc.Encode(mut out[..], fs)
        try Fits(h, mut dec, mut got, out[0..n])
        Assert(got.Get(0).name_index == 8 && got.Get(1).name_index == 24)
        Assert(got.Get(2).name_index == 0 && got.Get(3).name_index == 33)
    }
    // A static name sent as a literal name is found too: "accept" is 19.
    try Fits(h, mut dec, mut got, try Unhex(a, "00 0661 6363 6570 7401 2a"))
    Assert(Same(got.Get(0).name, "accept") && got.Get(0).name_index == 19)
}

test "a dynamic entry that wraps the ring's end reaches the sink whole" (sys: &System) {
    h := sys.heap.Alloc()
    // A 100-byte table holds two entries of 50 (1 + 17 + 32); the sixth
    // insertion starts at 90 and wraps to the ring's start.
    var dec := try Decoder.New(h, 100, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, 100, false)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    var out: [64]u8 = [0; 64]
    vals := ["aaaaaaaaaaaaaaaa1", "aaaaaaaaaaaaaaaa2", "aaaaaaaaaaaaaaaa3", "aaaaaaaaaaaaaaaa4", "aaaaaaaaaaaaaaaa5", "aaaaaaaaaaaaaaaa6"]
    for v in vals {
        n := try enc.Write(mut out[..], "n".AsBytes(), v.AsBytes(), Mode.Index)
        try Fits(h, mut dec, mut got, out[0..n])
    }
    Assert(dec.table.head == 8 && dec.TableLen() == 2)
    // Both entries again, by index: 62 (wrapped) and 63.
    m := try enc.Write(mut out[..], "n".AsBytes(), vals[5].AsBytes(), Mode.Index)
    Assert(m == 1 && out[0] == 0xBE)
    try Fits(h, mut dec, mut got, out[0..1])
    Assert(Same(got.Get(0).name, "n") && Same(got.Get(0).value, vals[5]))
    out[0] = 0xBF
    try Fits(h, mut dec, mut got, out[0..1])
    Assert(Same(got.Get(0).value, vals[4]))
}

test "EncodeBlock forwards a FieldBlock as it came, never-indexed fields still never indexed" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // C.4.1, Huffman coded: encoded again with Huffman, the same bytes.
    wire := try Unhex(a, "8286 8441 8cf1 e3c2 e5f2 3a6b a0ab 90f4 ff")
    try Fits(h, mut dec, mut got, wire)
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    out := try a.Slice[u8](enc.BlockBound(&got))
    n := try enc.EncodeBlock(out, &got)
    Assert(BytesEq(out[0..n], wire))
    // A short buffer is TooSmall and changes nothing.
    var small: [4]u8 = [0; 4]
    _ = enc.EncodeBlock(mut small[..], &got) else e {
        match e {
            TooSmall(need) { Assert(need == enc.BlockBound(&got)) }
        }
        0
    }
    Assert(enc.TableLen() == 1)
    // C.2.3's password: secret goes out never indexed again.
    try Fits(h, mut dec, mut got, try Unhex(a, "1008 7061 7373 776f 7264 0673 6563 7265 74"))
    var enc2 := try Encoder.New(h, DefaultTableBytes, false)
    defer enc2.Free(h)
    var buf: [64]u8 = [0; 64]
    m := try enc2.EncodeBlock(mut buf[..], &got)
    Assert(BytesEq(buf[0..m], try Unhex(a, "1008 7061 7373 776f 7264 0673 6563 7265 74")))
    Assert(enc2.TableLen() == 0)
}
