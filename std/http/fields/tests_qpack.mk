// Why: RFC 9204 QPACK defines modulo Required Insert Count wrap-around, five
// field-line representations, four encoder-stream instructions, three
// decoder-stream instructions, blocked-stream limits and capacity-0 client
// rules; every rule and RFC Appendix B vector must be pinned by tests.
//
// What: unit tests for QStaticTable, QDecoder, QEncoder, and RFC 9204
// Appendix B.1-B.5.
//
// How: verifies Appendix B.1-B.5 hex vectors and dynamic table contents,
// wrap-around tables (100-byte and 4096-byte), blocked sections at max_blocked,
// capacity-0 refusals, cut encoder/decoder instructions, and round-trips.

test "RFC 9204 Appendix A: the static table has entries 0 to 98 as listed" {
    Assert(QStaticLen == 99)
    Assert(QStaticTable[0].name == ":authority" && QStaticTable[0].value == "")
    Assert(QStaticTable[1].name == ":path" && QStaticTable[1].value == "/")
    Assert(QStaticTable[17].name == ":method" && QStaticTable[17].value == "GET")
    Assert(QStaticTable[25].name == ":status" && QStaticTable[25].value == "200")
    Assert(QStaticTable[63].name == ":status" && QStaticTable[63].value == "100")
    Assert(QStaticTable[71].name == ":status" && QStaticTable[71].value == "500")
    Assert(QStaticTable[84].name == "authorization" && QStaticTable[84].value == "")
    Assert(QStaticTable[98].name == "x-frame-options" && QStaticTable[98].value == "sameorigin")
    // Lookup finds non-contiguous :status entries (25 and 64) and maps name_index to first + 1.
    r1 := QStaticFind(":status".AsBytes(), "200".AsBytes())
    Assert(r1.index == 25 && r1.exact)
    r2 := QStaticFind(":status".AsBytes(), "204".AsBytes())
    Assert(r2.index == 64 && r2.exact)
    Assert(QStaticNameIndex(":status".AsBytes()) == 25)
    Assert(QStaticNameIndex("access-control-allow-headers".AsBytes()) == 34)
    Assert(QStaticNameIndex("x-missing".AsBytes()) == 0)
}

test "RFC 9204 Appendix B.1-B.5: encoder/decoder streams, dynamic table, cancellation and eviction match the RFC byte for byte" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    var owed: [64]u8 = [0; 64]

    // B.1: Literal Field Line with Name Reference (static index 1 -> :path=/index.html), RIC=0.
    b1 := try Unhex(a, "0000 510b 2f69 6e64 6578 2e68 746d 6c")
    Assert(try dec.Decode(h, mut fb, 0, b1) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":path: /index.html"][..]))
    Assert(fb.Get(0).name_index == 2)
    Assert(dec.Owed(mut owed[..]) == 0)

    // B.2: Set capacity 220, insert :authority=www.example.com and :path=/sample/path.
    enc_b2 := try Unhex(a, "3fbd01 c00f 7777 772e 6578 616d 706c 652e 636f 6d c10c 2f73 616d 706c 652f 7061 7468")
    Assert(try dec.Instructions(h, enc_b2) == enc_b2.Len())
    Assert(TableIs(&dec.table, [":path: /sample/path", ":authority: www.example.com"][..], 106))
    fb.Reset()
    sec_b2 := try Unhex(a, "0381 10 11")
    Assert(try dec.Decode(h, mut fb, 4, sec_b2) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":authority: www.example.com", ":path: /sample/path"][..]))
    n_b2 := dec.Owed(mut owed[..])
    want_ack_b2 := try Unhex(a, "84")
    Assert(BytesEq(owed[0..n_b2], want_ack_b2))

    // B.3: Speculative insert custom-key=custom-value -> Insert Count Increment (1).
    enc_b3 := try Unhex(a, "4a63 7573 746f 6d2d 6b65 790c 6375 7374 6f6d 2d76 616c 7565")
    Assert(try dec.Instructions(h, enc_b3) == enc_b3.Len())
    Assert(TableIs(&dec.table, ["custom-key: custom-value", ":path: /sample/path", ":authority: www.example.com"][..], 160))
    n_b3 := dec.Owed(mut owed[..])
    want_inc_b3 := try Unhex(a, "01")
    Assert(BytesEq(owed[0..n_b3], want_inc_b3))

    // B.4: Stream 8 arrives before Duplicate(2) -> blocks, Cancel(8) emits 0x48; then Duplicate(2) + decode succeeds.
    sec_b4 := try Unhex(a, "0500 80 c1 81")
    fb.Reset()
    Assert(try dec.Decode(h, mut fb, 8, sec_b4) == QOutcome.Blocked)
    dec.Cancel(8)
    n_b4c := dec.Owed(mut owed[..])
    want_cancel_b4 := try Unhex(a, "48")
    Assert(BytesEq(owed[0..n_b4c], want_cancel_b4))
    enc_b4 := try Unhex(a, "02")
    Assert(try dec.Instructions(h, enc_b4) == enc_b4.Len())
    Assert(TableIs(&dec.table, [":authority: www.example.com", "custom-key: custom-value", ":path: /sample/path", ":authority: www.example.com"][..], 217))
    Assert(try dec.Decode(h, mut fb, 8, sec_b4) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":authority: www.example.com", ":path: /", "custom-key: custom-value"][..]))
    _ = dec.Owed(mut owed[..])

    // B.5: Insert with dynamic name ref (rel=1 -> custom-key) = custom-value2 evicts abs 0 (:authority=www.example.com).
    enc_b5 := try Unhex(a, "810d 6375 7374 6f6d 2d76 616c 7565 32")
    Assert(try dec.Instructions(h, enc_b5) == enc_b5.Len())
    Assert(TableIs(&dec.table, ["custom-key: custom-value2", ":authority: www.example.com", "custom-key: custom-value", ":path: /sample/path"][..], 215))
}

test "RFC 9204 2.1: the encoder keeps the field order" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    enc := QEncoder.New()
    lines := [":method: GET", ":scheme: https", ":path: /", "x-one: alpha", "x-two: beta"]
    fs := try ToFields(a, lines[..])
    out := try a.Slice[u8](enc.Bound(fs))
    n := try enc.Encode(out, fs)
    var dec := try QDecoder.New(h, 0, 0, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    Assert(try dec.Decode(h, mut fb, 0, out[0..n]) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":method: GET", ":scheme: https", ":path: /", "x-one: alpha", "x-two: beta"][..]))
}

test "RFC 9204 2.2: the decoder hands fields to the sink in section order" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 0, 0, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Static :method GET (17 -> 0xd1), :scheme https (23 -> 0xd7), :path / (1 -> 0xc1).
    sec := try Unhex(a, "0000 d1 d7 c1")
    Assert(try dec.Decode(h, mut fb, 0, sec) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":method: GET", ":scheme: https", ":path: /"][..]))
}

test "RFC 9204 3.1: a static index past 98 in a field line is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 0, 0, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Indexed static 99: 11_111111 (0xff) 0x24 (63 + 36 = 99).
    bad_idx := try Unhex(a, "0000 ff24")
    match dec.Decode(h, mut fb, 0, bad_idx) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index) { Assert(index == 99) }
                _ { Assert(false) }
            }
        }
    }
    // Literal with static name ref 99: 01_0_1_1111 (0x5f) 0x54 (15 + 84 = 99), value "x".
    bad_lit := try Unhex(a, "0000 5f54 01 78")
    match dec.Decode(h, mut fb, 0, bad_lit) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index) { Assert(index == 99) }
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 3.1: a static index past 98 on the encoder stream is QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Capacity 256 (0x3f 0xe1 0x01), then Insert Name Ref T=1 idx=99 (0xff 0x24) value "x".
    ins := try Unhex(a, "3fe101 ff24 01 78")
    match dec.Instructions(h, ins) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index) { Assert(index == 99) }
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 3.2: duplicate entries and the Duplicate instruction are accepted" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Capacity 256, insert x-a: 1 twice, then Duplicate(0).
    ins := try Unhex(a, "3fe101 43 782d61 01 31  43 782d61 01 31  00")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    Assert(dec.TableLen() == 3 && dec.InsertCount() == 3)
}

test "RFC 9204 3.2.2: an insert before any Set Dynamic Table Capacity is QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    ins := try Unhex(a, "c0 01 61")
    match dec.Instructions(h, ins) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                CapacityExceeded(size: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 3.2.2: an entry larger than the capacity is QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Capacity 33 (0x3f 0x02), insert literal "a": "b" (size 34 > 33).
    ins := try Unhex(a, "3f02 41 61 01 62")
    match dec.Instructions(h, ins) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                CapacityExceeded(size) { Assert(size == 34) }
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 3.2.2: an insert whose name entry it evicts keeps its name" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Capacity 64 (0x3f 0x21). Insert "longname" (8) + "val1" (4) -> 44 bytes (abs 0).
    // Then Insert Name Ref T=0 rel=0 (0x80) with "val222222222222" (15) -> 8+15+32=55 <= 64, evicts abs 0!
    ins := try Unhex(a, "3f21 48 6c6f6e676e616d65 04 76616c31  80 0f 76616c323232323232323232323232")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    Assert(TableIs(&dec.table, ["longname: val222222222222"][..], 55))
}

test "RFC 9204 3.2.2: a lower capacity evicts the oldest entries" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Cap 128 -> insert a:1 (34) and b:2 (34) -> size 68 -> lower cap to 40 -> evicts a:1, keeps b:2 (34).
    ins := try Unhex(a, "3f61 41 61 01 31  41 62 01 32  3f09")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    Assert(TableIs(&dec.table, ["b: 2"][..], 34))
}

test "RFC 9204 4.3.1: a capacity above the advertised maximum is QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    // Client capacity 0 refuses Set Dynamic Table Capacity 1 (0x21); accepts 0 (0x20).
    var client := try QDecoder.New(h, 0, 0, 65536)
    defer client.Free(h)
    z := try Unhex(a, "20")
    Assert(try client.Instructions(h, z) == 1)
    one := try Unhex(a, "21")
    match client.Instructions(h, one) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                CapacityExceeded(size) { Assert(size == 1) }
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.3.2: insert with a static and a dynamic name reference" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Cap 256, static 0 (:authority) = a.test, dynamic rel 0 (:authority) = b.test.
    ins := try Unhex(a, "3fe101 c0 06 612e74657374  80 06 622e74657374")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    Assert(TableIs(&dec.table, [":authority: b.test", ":authority: a.test"][..], 96))
}

test "RFC 9204 4.3.3: insert with a literal name" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Split mid-instruction across two Instructions calls.
    ins := try Unhex(a, "3fe101 43 782d6b 03 76616c")
    consumed := try dec.Instructions(h, ins[0..6])
    Assert(consumed == 3)
    Assert(try dec.Instructions(h, ins[consumed..]) == ins.Len() - consumed)
    Assert(TableIs(&dec.table, ["x-k: val"][..], 38))
}

test "RFC 9204 2.2.3: an encoder instruction naming an evicted entry is QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    // Cap 40 -> insert a:1 (34) -> insert b:2 (34) evicts abs 0 -> Duplicate(1) names evicted abs 0.
    ins := try Unhex(a, "3f09 41 61 01 31  41 62 01 32  01")
    match dec.Instructions(h, ins) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index) { Assert(index == 1) }
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.1.1: a 62-bit integer decodes at every prefix width" {
    max62: int = 0x3FFF_FFFF_FFFF_FFFF
    var buf: [16]u8 = [0; 16]
    for n in 1..9 {
        k := PutInt(mut buf[..], max62, n, 0)
        Assert(k <= 10)
        r := try QGetInt(buf[0..k], 0, n)
        Assert(r.value == max62 && r.next == k)
    }
}

test "RFC 9204 4.1.2: bad Huffman padding in a field line is QPACK_DECOMPRESSION_FAILED and on the encoder stream QPACK_ENCODER_STREAM_ERROR" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Literal name with Huffman string of 0x00 (bad padding, not all ones): 001_0_1_001 (0x29) 0x00 0x01 0x61.
    bad_sec := try Unhex(a, "0000 29 00 01 61")
    match dec.Decode(h, mut fb, 0, bad_sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadHuffman(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
    // Encoder stream: cap 256, Insert Literal Name H=1 len=1 byte 0x00.
    bad_enc := try Unhex(a, "3fe101 61 00 01 61")
    match dec.Instructions(h, bad_enc) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadHuffman(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 7.4: an integer too large on a request stream is a stream QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 0, 0, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Static indexed with 63-bit integer (> QMaxInt).
    huge := try Unhex(a, "0000 ff 8080 8080 8080 8080 80 01")
    match dec.Decode(h, mut fb, 0, huge) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadInteger(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.1: a section cut inside its prefix is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    empty: [0]u8 = []
    match dec.Decode(h, mut fb, 0, empty[..]) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                Truncated(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
    one := try Unhex(a, "00")
    match dec.Decode(h, mut fb, 0, one) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                Truncated(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.1: our sections start with 00 00 and reference only the static table" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    enc := QEncoder.New()
    lines := [":method: GET", "x-custom: value"]
    fs := try ToFields(a, lines[..])
    out := try a.Slice[u8](enc.Bound(fs))
    n := try enc.Encode(out, fs)
    Assert(n >= 2 && out[0] == 0x00 && out[1] == 0x00)
    // Decoder with capacity 0 decodes our encoder's sections with zero encoder stream.
    var client := try QDecoder.New(h, 0, 0, 65536)
    defer client.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    Assert(try client.Decode(h, mut fb, 0, out[0..n]) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":method: GET", "x-custom: value"][..]))
    // PeerInstructions accepts Stream Cancellation and rejects Section Ack and Insert Count Inc.
    canc := try Unhex(a, "44")
    Assert(try enc.PeerInstructions(canc) == 1)
    ack := try Unhex(a, "84")
    match enc.PeerInstructions(ack) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadInstruction(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
    inc := try Unhex(a, "01")
    match enc.PeerInstructions(inc) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadInstruction(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.1.1: a non-zero Required Insert Count at capacity 0 is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var client := try QDecoder.New(h, 0, 0, 65536)
    defer client.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    sec := try Unhex(a, "0100")
    match client.Decode(h, mut fb, 0, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadRequiredInsertCount(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.1.1: an encoded insert count above FullRange is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    // 4096-byte table: MaxEntries = 128, FullRange = 256. Encoded 257 (0xff 0x02) > 256.
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    sec := try Unhex(a, "ff02 00")
    match dec.Decode(h, mut fb, 0, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadRequiredInsertCount(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.1.1: Required Insert Count decodes across the wrap (100-byte table, 10 inserts, 4 is 9)" {
    // RFC 9204 4.5.1.1 example: MaxEntries=3, FullRange=6, TotalInserts=10, Enc=4 -> RIC=9.
    Assert(try DecodeRIC(100, 10, 4) == 9)
    Assert(try DecodeRIC(100, 10, 0) == 0)
    // Boundary wrap-around table: (max_cap, total_inserts, ric) -> enc -> ric.
    for ric in 1..20 {
        enc := (ric % 6) + 1
        Assert(try DecodeRIC(100, ric, enc) == ric)
    }
}

test "RFC 9204 4.5.1.1: Required Insert Count decodes after 300 inserts into a 4096-byte table" {
    // MaxEntries = 128, FullRange = 256. RIC = 300 -> enc = (300 % 256) + 1 = 45.
    Assert(try DecodeRIC(4096, 300, 45) == 300)
    Assert(try DecodeRIC(4096, 256, 1) == 256)
    Assert(try DecodeRIC(4096, 512, 256) == 511)
}

test "RFC 9204 4.5.1.1: a non-zero encoding that decodes to 0 is QPACK_DECOMPRESSION_FAILED" {
    // MaxEntries=3, FullRange=6, TotalInserts=0, Enc=1 -> MaxValue=3, MaxWrapped=0 -> ric=0 -> error.
    match DecodeRIC(100, 0, 1) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadRequiredInsertCount(offset: _) {}
            }
        }
    }
}

test "RFC 9204 4.5.1.2: a negative Base is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // RIC=0, S=1, DeltaBase=0 (0x00 0x80) -> invalid negative Base.
    sec := try Unhex(a, "0080")
    match dec.Decode(h, mut fb, 0, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadBase(offset: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 2.1.2: blocked sections unblock on inserts and fail BlockedLimit past max_blocked" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 2, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Two blocked sections (RIC=1 -> enc=2, S=0, DeltaBase=0, post-base? No, rel=0 Base=1 -> 02 00 80).
    sec := try Unhex(a, "0200 80")
    Assert(try dec.Decode(h, mut fb, 4, sec) == QOutcome.Blocked)
    Assert(try dec.Decode(h, mut fb, 8, sec) == QOutcome.Blocked)
    // A 3rd distinct stream exceeds max_blocked=2 -> BlockedLimit.
    match dec.Decode(h, mut fb, 12, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BlockedLimit {}
                _ { Assert(false) }
            }
        }
    }
    // Cancel stream 4 frees one slot -> stream 12 can now block.
    dec.Cancel(4)
    Assert(try dec.Decode(h, mut fb, 12, sec) == QOutcome.Blocked)
    // Insert 1 entry -> Unblocked returns 8 and 12; both decode.
    ins := try Unhex(a, "3fe101 43 782d61 01 31")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    var unb: [4]i64 = [0; 4]
    nu := dec.Unblocked(mut unb[..])
    Assert(nu == 2 && unb[0] == 8 && unb[1] == 12)
    Assert(try dec.Decode(h, mut fb, 8, sec) == QOutcome.Fits)
    Assert(PairsAre(&fb, ["x-a: 1"][..]))
    fb.Reset()
    Assert(try dec.Decode(h, mut fb, 12, sec) == QOutcome.Fits)
    Assert(PairsAre(&fb, ["x-a: 1"][..]))
}

test "RFC 9204 2.2.3: a reference at or past the Required Insert Count is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Insert 2 entries (abs 0, 1).
    ins := try Unhex(a, "3fe101 41 61 01 31  41 62 01 32")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    // Section declares RIC=1 (enc=2), Base=2 (S=0, DeltaBase=1), references rel=0 -> abs=1 >= RIC(1)!
    sec := try Unhex(a, "0201 80")
    match dec.Decode(h, mut fb, 0, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 2.2.3: a reference to an evicted entry is QPACK_DECOMPRESSION_FAILED" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Cap 40: insert a:1 (evicted) then b:2 -> inserted=2, abs 0 evicted.
    ins := try Unhex(a, "3f09 41 61 01 31  41 62 01 32")
    Assert(try dec.Instructions(h, ins) == ins.Len())
    // Section declares RIC=2 (enc=3), Base=2, references rel=1 -> abs=0 (evicted).
    sec := try Unhex(a, "0300 81")
    match dec.Decode(h, mut fb, 0, sec) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadIndex(offset: _, index: _) {}
                _ { Assert(false) }
            }
        }
    }
}

test "RFC 9204 4.5.2-4.5.6: all five field-line representations, post-Base and sensitive N bit" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 4096, 16, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Insert 9 entries (abs 0..8); abs 5 is "x-five: 5", abs 7 is "x-seven: 7".
    var enc_list := List[u8].New(a)
    try enc_list.Extend(try Unhex(a, "3fe101"))
    for i in 0..9 {
        // 41 ('x') 01 ('0'+i) -> insert "x": "0"+i
        try enc_list.Push(0x41)
        try enc_list.Push(b'x')
        try enc_list.Push(0x01)
        try enc_list.Push(b'0' + Trunc[u8](i))
    }
    ins := enc_list.Done()[..]
    Assert(try dec.Instructions(h, ins) == ins.Len())
    // RIC=9 (enc=10=0x0a), S=1, DeltaBase=2 (0x82) -> Base = 9 - 2 - 1 = 6.
    // 1) 4.5.2 Indexed static 17 (:method GET): 0xd1
    // 2) 4.5.2 Indexed dynamic rel 1 (abs = 6-1-1 = 4 -> x:4): 0x81
    // 3) 4.5.3 Indexed post-Base 1 (abs = 6+1 = 7 -> x:7): 0x11
    // 4) 4.5.4 Literal name ref static 0 (:authority), N=1 -> 01_1_1_0000 (0x70) + "h"
    // 5) 4.5.5 Literal post-Base name ref 2 (abs = 6+2 = 8 -> x), N=1 -> 0000_1_010 (0x0a) + "v8"
    // 6) 4.5.6 Literal with literal name, N=1, H=1 ("www.example.com" Huffman), value raw "z":
    //    001_1_1_100 (0x3c -> len=12: 7 + 5 -> 0x3f 0x05) + f1e3c2e5f23a6ba0ab90f4ff + 0x01 + "z"
    sec := try Unhex(a, "0a82 d1 81 11 70 01 68 0a 02 7638 3f05 f1e3c2e5f23a6ba0ab90f4ff 01 7a")
    Assert(try dec.Decode(h, mut fb, 4, sec) == QOutcome.Fits)
    Assert(PairsAre(&fb, [":method: GET", "x: 4", "x: 7", ":authority: h", "x: v8", "www.example.com: z"][..]))
    Assert(!fb.Get(0).sensitive && !fb.Get(1).sensitive && !fb.Get(2).sensitive)
    Assert(fb.Get(3).sensitive && fb.Get(4).sensitive && fb.Get(5).sensitive)

    // Re-encoding FieldBlock via EncodeBlock preserves sensitive (N bit) and TooSmall leaves out unchanged.
    qenc := QEncoder.New()
    var short: [4]u8 = [0xCC; 4]
    match qenc.EncodeBlock(mut short[..], &fb) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need > 4 && short[0] == 0xCC) }
    }
    buf := try a.Slice[u8](qenc.BlockBound(&fb))
    m := try qenc.EncodeBlock(buf, &fb)
    var dec2 := try QDecoder.New(h, 0, 0, 65536)
    defer dec2.Free(h)
    var fb2 := FieldBlock.New(h)
    defer fb2.Free(h)
    Assert(try dec2.Decode(h, mut fb2, 0, buf[0..m]) == QOutcome.Fits)
    Assert(PairsAre(&fb2, [":method: GET", "x: 4", "x: 7", ":authority: h", "x: v8", "www.example.com: z"][..]))
    Assert(!fb2.Get(0).sensitive && fb2.Get(3).sensitive && fb2.Get(4).sensitive && fb2.Get(5).sensitive)
}

test "RFC 9204 7.1.3: a sensitive field is sent with the N bit" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    enc := QEncoder.New()
    fs := [
        TField(name: "authorization", value: "Bearer tok", sensitive: true),
        TField(name: "x-secret", value: "hidden", sensitive: true),
        TField(name: ":status", value: "200", sensitive: false),
    ]
    buf := try a.Slice[u8](enc.Bound(fs[..]))
    n := try enc.Encode(buf, fs[..])
    var dec := try QDecoder.New(h, 0, 0, 65536)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    Assert(try dec.Decode(h, mut fb, 0, buf[0..n]) == QOutcome.Fits)
    Assert(fb.Get(0).sensitive && fb.Get(1).sensitive && !fb.Get(2).sensitive)
}

test "QPACK decoder list_bytes TooLarge validates the remainder and stays bounded" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try QDecoder.New(h, 0, 0, 50)
    defer dec.Free(h)
    var fb := FieldBlock.New(h)
    defer fb.Free(h)
    // Two fields: :path=/ (38 bytes) + x-custom=123456 (46 bytes > remaining 12).
    sec := try Unhex(a, "0000 c1 2701 782d637573746f6d 06 313233343536")
    Assert(try dec.Decode(h, mut fb, 0, sec) == QOutcome.TooLarge)
    Assert(fb.Len() == 1 && PairsAre(&fb, [":path: /"][..]))
}
