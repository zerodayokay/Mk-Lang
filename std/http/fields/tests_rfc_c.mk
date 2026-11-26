// Why: RFC 7541 Appendix C fixes the bytes of a decoder's input and an
// encoder's output for four sequences of blocks and four single
// representations; both directions must match them byte for byte, and the
// dynamic table must hold what the RFC lists after each block.
//
// What: the C.2 representations (one test each) and the C.3 to C.6
// sequences (one test each, three blocks on one decoder and one encoder).
//
// How: the hex dumps, header lists and table listings are copied from the
// RFC's text by a script, not typed. Block checks a sequence's block both
// ways; the C.2 tests write their one field in the mode the RFC shows.

test "C.2.1: literal header field with indexing decodes and encodes as the RFC shows" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    wire := try Unhex(a, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572")
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, wire)
    want := ["custom-key: custom-header"]
    Assert(PairsAre(&got, want[..]))
    Assert(got.Get(0).sensitive == false)
    entries := ["custom-key: custom-header"]
    Assert(TableIs(&dec.table, entries[..], 55))
    var out: [64]u8 = [0; 64]
    n := try enc.Write(mut out[..], "custom-key".AsBytes(), "custom-header".AsBytes(), Mode.Index)
    Assert(BytesEq(out[0..n], wire))
    Assert(enc.TableLen() == dec.TableLen() && enc.TableSize() == dec.TableSize())
}

test "C.2.2: literal header field without indexing decodes and encodes as the RFC shows" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    wire := try Unhex(a, "040c 2f73 616d 706c 652f 7061 7468")
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, wire)
    want := [":path: /sample/path"]
    Assert(PairsAre(&got, want[..]))
    Assert(got.Get(0).sensitive == false)
    Assert(dec.TableLen() == 0 && dec.TableSize() == 0)
    var out: [64]u8 = [0; 64]
    n := try enc.Write(mut out[..], ":path".AsBytes(), "/sample/path".AsBytes(), Mode.NoIndex)
    Assert(BytesEq(out[0..n], wire))
    Assert(enc.TableLen() == dec.TableLen() && enc.TableSize() == dec.TableSize())
}

test "C.2.3: literal header field never indexed decodes and encodes as the RFC shows" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    wire := try Unhex(a, "1008 7061 7373 776f 7264 0673 6563 7265 74")
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, wire)
    want := ["password: secret"]
    Assert(PairsAre(&got, want[..]))
    Assert(got.Get(0).sensitive == true)
    Assert(dec.TableLen() == 0 && dec.TableSize() == 0)
    var out: [64]u8 = [0; 64]
    n := try enc.Write(mut out[..], "password".AsBytes(), "secret".AsBytes(), Mode.Never)
    Assert(BytesEq(out[0..n], wire))
    Assert(enc.TableLen() == dec.TableLen() && enc.TableSize() == dec.TableSize())
}

test "C.2.4: indexed header field decodes and encodes as the RFC shows" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    wire := try Unhex(a, "82")
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, wire)
    want := [":method: GET"]
    Assert(PairsAre(&got, want[..]))
    Assert(got.Get(0).sensitive == false)
    Assert(dec.TableLen() == 0 && dec.TableSize() == 0)
    var out: [64]u8 = [0; 64]
    n := try enc.Write(mut out[..], ":method".AsBytes(), "GET".AsBytes(), Mode.Index)
    Assert(BytesEq(out[0..n], wire))
    Assert(enc.TableLen() == dec.TableLen() && enc.TableSize() == dec.TableSize())
}

test "C.3: three requests without Huffman coding match the RFC both ways, table after each" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, false)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // C.3.1, First Request
    list1 := [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com"]
    table1 := [":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8286 8441 0f77 7777 2e65 7861 6d70 6c65 2e63 6f6d", list1[..], table1[..], 57)
    // C.3.2, Second Request
    list2 := [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com", "cache-control: no-cache"]
    table2 := ["cache-control: no-cache", ":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8286 84be 5808 6e6f 2d63 6163 6865", list2[..], table2[..], 110)
    // C.3.3, Third Request
    list3 := [":method: GET", ":scheme: https", ":path: /index.html", ":authority: www.example.com", "custom-key: custom-value"]
    table3 := ["custom-key: custom-value", "cache-control: no-cache", ":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8287 85bf 400a 6375 7374 6f6d 2d6b 6579 0c63 7573 746f 6d2d 7661 6c75 65", list3[..], table3[..], 164)
}

test "C.4: three requests with Huffman coding match the RFC both ways, table after each" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, DefaultTableBytes, true)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // C.4.1, First Request
    list1 := [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com"]
    table1 := [":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8286 8441 8cf1 e3c2 e5f2 3a6b a0ab 90f4 ff", list1[..], table1[..], 57)
    // C.4.2, Second Request
    list2 := [":method: GET", ":scheme: http", ":path: /", ":authority: www.example.com", "cache-control: no-cache"]
    table2 := ["cache-control: no-cache", ":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8286 84be 5886 a8eb 1064 9cbf", list2[..], table2[..], 110)
    // C.4.3, Third Request
    list3 := [":method: GET", ":scheme: https", ":path: /index.html", ":authority: www.example.com", "custom-key: custom-value"]
    table3 := ["custom-key: custom-value", "cache-control: no-cache", ":authority: www.example.com"]
    try Block(a, h, mut dec, mut enc, mut got, "8287 85bf 4088 25a8 49e9 5ba9 7d7f 8925 a849 e95b b8e8 b4bf", list3[..], table3[..], 164)
}

test "C.5: three responses without Huffman coding, with evictions match the RFC both ways, table after each" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, 256, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, 256, false)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // C.5.1, First Response
    list1 := [":status: 302", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"]
    table1 := ["location: https://www.example.com", "date: Mon, 21 Oct 2013 20:13:21 GMT", "cache-control: private", ":status: 302"]
    try Block(a, h, mut dec, mut enc, mut got, "4803 3330 3258 0770 7269 7661 7465 611d 4d6f 6e2c 2032 3120 4f63 7420 3230 3133 2032 303a 3133 3a32 3120 474d 546e 1768 7474 7073 3a2f 2f77 7777 2e65 7861 6d70 6c65 2e63 6f6d", list1[..], table1[..], 222)
    // C.5.2, Second Response
    list2 := [":status: 307", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"]
    table2 := [":status: 307", "location: https://www.example.com", "date: Mon, 21 Oct 2013 20:13:21 GMT", "cache-control: private"]
    try Block(a, h, mut dec, mut enc, mut got, "4803 3330 37c1 c0bf", list2[..], table2[..], 222)
    // C.5.3, Third Response
    list3 := [":status: 200", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:22 GMT", "location: https://www.example.com", "content-encoding: gzip", "set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"]
    table3 := ["set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1", "content-encoding: gzip", "date: Mon, 21 Oct 2013 20:13:22 GMT"]
    try Block(a, h, mut dec, mut enc, mut got, "88c1 611d 4d6f 6e2c 2032 3120 4f63 7420 3230 3133 2032 303a 3133 3a32 3220 474d 54c0 5a04 677a 6970 7738 666f 6f3d 4153 444a 4b48 514b 425a 584f 5157 454f 5049 5541 5851 5745 4f49 553b 206d 6178 2d61 6765 3d33 3630 303b 2076 6572 7369 6f6e 3d31", list3[..], table3[..], 215)
}

test "C.6: three responses with Huffman coding, with evictions match the RFC both ways, table after each" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, 256, 1 << 16)
    defer dec.Free(h)
    var enc := try Encoder.New(h, 256, true)
    defer enc.Free(h)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    // C.6.1, First Response
    list1 := [":status: 302", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"]
    table1 := ["location: https://www.example.com", "date: Mon, 21 Oct 2013 20:13:21 GMT", "cache-control: private", ":status: 302"]
    try Block(a, h, mut dec, mut enc, mut got, "4882 6402 5885 aec3 771a 4b61 96d0 7abe 9410 54d4 44a8 2005 9504 0b81 66e0 82a6 2d1b ff6e 919d 29ad 1718 63c7 8f0b 97c8 e9ae 82ae 43d3", list1[..], table1[..], 222)
    // C.6.2, Second Response
    list2 := [":status: 307", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:21 GMT", "location: https://www.example.com"]
    table2 := [":status: 307", "location: https://www.example.com", "date: Mon, 21 Oct 2013 20:13:21 GMT", "cache-control: private"]
    try Block(a, h, mut dec, mut enc, mut got, "4883 640e ffc1 c0bf", list2[..], table2[..], 222)
    // C.6.3, Third Response
    list3 := [":status: 200", "cache-control: private", "date: Mon, 21 Oct 2013 20:13:22 GMT", "location: https://www.example.com", "content-encoding: gzip", "set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"]
    table3 := ["set-cookie: foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1", "content-encoding: gzip", "date: Mon, 21 Oct 2013 20:13:22 GMT"]
    try Block(a, h, mut dec, mut enc, mut got, "88c1 6196 d07a be94 1054 d444 a820 0595 040b 8166 e084 a62d 1bff c05a 839b d9ab 77ad 94e7 821d d7f2 e6c7 b335 dfdf cd5b 3960 d5af 2708 7f36 72c1 ab27 0fb5 291f 9587 3160 65c0 03ed 4ee5 b106 3d50 07", list3[..], table3[..], 215)
}
