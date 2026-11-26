// Why: each rule the decoder enforces on peer bytes (RFC 7541 4.2, 5.1,
// 5.2, 6.1, 6.3 and the list budget of 4.1) must fail a block that breaks
// it with exactly its error, and the list budget must leave the table in
// sync with the peer's.
//
// What: one test per rule, each with the exact error and offset; the
// size updates owed after a lowered maximum; a decoder that refuses every
// block after a failed one; the eviction of an entry that names the field
// being inserted (4.4), in both of the decoder's paths; an entry larger
// than the table; 2^16 one-byte fields under and over the budget; huge
// string lengths and integers at every prefix; and every prefix of valid
// blocks, which must fail cleanly or decode.
//
// How: blocks are written as hex; RunHex prints Decode's outcome, or its error
// as errors print themselves, so a test compares the error's name and fields.

// RunHex is "ok N" or "too large N" for the block hex, N the fields the sink
// kept, or the error Decode gives, as it prints, then "after N".
RunHex(a: &Arena, h: &Alloc, dec: mut Decoder, hex: str): OutOfMemory!str from a {
    wire := try Unhex(a, hex)
    var got := FieldBlock.New(h)
    defer got.Free(h)
    o := dec.Decode(h, mut got, wire) else e {
        return try a.Fmt("{e} after {n}", (e, n: got.Len()))
    }
    word := if o == Outcome.Fits { "ok" } else { "too large" }
    return try a.Fmt("{word} {n}", (word, n: got.Len()))
}

// RunNew is RunHex on a decoder of its own (a table of 4096 bytes, a list
// budget of 64 KiB): a decoder refuses every block after an error.
RunNew(a: &Arena, h: &Alloc, hex: str): OutOfMemory!str from a {
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    return try RunHex(a, h, mut dec, hex)
}

// ErrIs is whether out, an Outcome, is want with any module path before it.
ErrIs(out: str, want: str): bool {
    if out == want {
        return true
    }
    return out.EndsWith(want) && out.Len() > want.Len() && out.Byte(out.Len() - want.Len() - 1) == b'.'
}

test "a size update above the decoder's maximum is BadSizeUpdate" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    // 0x3f e2 1f: a size update to 4097.
    out := try RunHex(sys.arena, h, mut dec, "3fe2 1f")
    Assert(ErrIs(out, "BadSizeUpdate(offset: 0, size: 4097) after 0"))
}

test "a size update after a field is BadSizeUpdate" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    out := try RunHex(sys.arena, h, mut dec, "82 20")
    Assert(ErrIs(out, "BadSizeUpdate(offset: 1, size: 0) after 1"))
}

test "size updates at the start of a block, two of them, are taken" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    // Insert one entry, then empty the table with an update to 0 and
    // raise it again to 4096 (RFC 7541 4.2).
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"), "ok 1"))
    Assert(dec.TableLen() == 1)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "20 3fe1 1f 82"), "ok 1"))
    Assert(dec.TableLen() == 0 && dec.table.cap == 4096)
}

test "a lowered maximum without a size update at the next block is MissingSizeUpdate" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    dec.SetMaxTableSize(100)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "82"), "MissingSizeUpdate after 0"))
    // An empty block cannot carry the update either.
    var dec2 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec2.Free(h)
    dec2.SetMaxTableSize(100)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec2, ""), "MissingSizeUpdate after 0"))
    // An update to more than the new maximum is refused; to it, taken.
    var dec3 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec3.Free(h)
    dec3.SetMaxTableSize(100)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec3, "3f46 82"), "BadSizeUpdate(offset: 0, size: 101) after 0"))
    var dec4 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec4.Free(h)
    dec4.SetMaxTableSize(100)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec4, "3f45 82"), "ok 1"))
    Assert(dec4.table.cap == 100)
    // Raising the maximum again asks for nothing.
    dec4.SetMaxTableSize(4096)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec4, "82"), "ok 1"))
}

test "a maximum lowered then raised before a block wants a first update at most the lowest" (sys: &System) {
    h := sys.heap.Alloc()
    // One entry of 55 bytes in the table first.
    custom := "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, custom), "ok 1"))
    dec.SetMaxTableSize(20)
    dec.SetMaxTableSize(4096)
    // The final size alone skips the eviction the peer owes: refused.
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "3fe1 1f 82"), "BadSizeUpdate(offset: 0, size: 4096) after 0"))
    var dec2 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec2.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec2, custom), "ok 1"))
    dec2.SetMaxTableSize(20)
    dec2.SetMaxTableSize(4096)
    // The lowest, which evicts, then the final (RFC 7541 4.2).
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec2, "34 3fe1 1f 82"), "ok 1"))
    Assert(dec2.TableLen() == 0 && dec2.table.cap == 4096)
    // Lowered twice: the first update is held to the lower.
    var dec3 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec3.Free(h)
    dec3.SetMaxTableSize(200)
    dec3.SetMaxTableSize(100)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec3, "3f a901 82"), "BadSizeUpdate(offset: 0, size: 200) after 0"))
}

test "after a compression error the decoder refuses every later block with Unusable" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "80"), "BadIndex(offset: 0, index: 0) after 0"))
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "82"), "Unusable after 0"))
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, ""), "Unusable after 0"))
}

// A sink that fails with OutOfMemory from its field number fail_at on.
struct FailSink {
    n: int
    fail_at: int
}

impl Sink for FailSink {
    Add(mut self, a: &Alloc, name: []u8, value: []u8, never_index: bool, name_index: int): OutOfMemory!void {
        _ = a
        _ = name
        _ = value
        _ = never_index
        _ = name_index
        self.n += 1
        if self.n > self.fail_at {
            fail OutOfMemory
        }
    }
}

test "an OutOfMemory from the sink leaves the decoder refusing later blocks" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    // C.2.1: the peer inserts custom-key: custom-header; the sink fails
    // first, so this table never gets it.
    var s := FailSink(n: 0, fail_at: 0)
    match dec.Decode(h, mut s, try Unhex(a, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572")) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                OutOfMemory {}
                _ { Assert(false) }
            }
        }
    }
    Assert(dec.TableLen() == 0)
    // Index 62 is the peer's entry; this table would misread it.
    Assert(ErrIs(try RunHex(a, h, mut dec, "be"), "Unusable after 0"))
}

test "a block of 2^16 one-byte fields stops at the budget, reads to its end and stays in sync" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    custom := try Unhex(a, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572")
    n := 1 << 16
    block := try a.Slice[u8](n + custom.Len())
    for i in 0..n {
        block[i] = 0x82
    }
    (mut block[n..]).CopyFrom(custom)
    for limit in [1 << 16, 0] {
        var dec := try Decoder.New(h, DefaultTableBytes, limit)
        defer dec.Free(h)
        var got := FieldBlock.New(h)
        defer got.Free(h)
        // :method GET is 42 bytes of list: 1,560 fit in 64 KiB, none in 0.
        Assert(try dec.Decode(h, mut got, block[..]) == Outcome.TooLarge)
        Assert(got.Len() == limit / 42)
        // The literal after the limit is in the table, as in the peer's.
        Assert(dec.TableLen() == 1 && dec.TableSize() == 55)
    }
}

test "a string length of 2^31 or 2^62 in a short block is Truncated" (sys: &System) {
    h := sys.heap.Alloc()
    var buf: [16]u8 = [0; 16]
    for claim in [1 << 31, 1 << 62] {
        // A literal without indexing, new name, whose name claims that many
        // bytes; three bytes follow.
        buf[0] = 0x00
        k := PutInt(mut buf[1..], claim, 7, 0x80)
        var got := FieldBlock.New(h)
        defer got.Free(h)
        var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
        defer dec.Free(h)
        _ = dec.Decode(h, mut got, buf[0..1 + k + 3]) else e {
            match e {
                Truncated(offset) { Assert(offset == 1) }
                _ { Assert(false) }
            }
            Outcome.Fits
        }
        Assert(got.Len() == 0 && dec.scratch.Len() <= ScratchStart)
    }
}

test "an integer past the int range or of endless continuation bytes is BadInteger at every prefix" (sys: &System) {
    h := sys.heap.Alloc()
    var big: [10]u8 = [0xFF; 10]
    big[9] = 0x7F
    var endless: [24]u8 = [0x80; 24]
    for n in 1..9 {
        mask := Trunc[u8]((1 << n) - 1)
        big[0] = mask
        endless[0] = mask
        // The prefix full, then nine groups of 127: 2^n - 1 + 2^63 - 1.
        _ = GetInt(big[..], 0, n) else e {
            match e {
                BadInteger(offset) { Assert(offset == 0) }
                _ { Assert(false) }
            }
            (value: 0, next: 0)
        }
        _ = GetInt(endless[..], 0, n) else e {
            match e {
                BadInteger(offset) { Assert(offset == 0) }
                _ { Assert(false) }
            }
            (value: 0, next: 0)
        }
    }
    // An indexed field's index, the same through Decode.
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "ff 8080 8080 8080 8080 8080 8080 8080 8080"), "BadInteger(offset: 0) after 0"))
}

test "an index one past a table that holds entries is BadIndex, as is a reference after an update to 0" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    custom := "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, custom), "ok 1"))
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "be"), "ok 1"))
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "bf"), "BadIndex(offset: 0, index: 63) after 0"))
    var dec2 := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec2.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec2, custom), "ok 1"))
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec2, "20 be"), "BadIndex(offset: 1, index: 62) after 0"))
}

test "duplicate dynamic entries are taken, not an error (RFC 7541 2.3.2)" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    custom := "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, try sys.arena.Fmt("{custom} {custom} be bf", (custom))), "ok 4"))
    Assert(dec.TableLen() == 2 && dec.TableSize() == 110)
}

test "an index past the int range is BadInteger" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "82 ffff ffff ffff ffff ff7f"), "BadInteger(offset: 1) after 1"))
}

test "an integer of more than 10 bytes is BadInteger" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(ErrIs(try RunNew(sys.arena, h, "ff80 8080 8080 8080 8080 00"), "BadInteger(offset: 0) after 0"))
    // A string length is held to the same bound.
    Assert(ErrIs(try RunNew(sys.arena, h, "04 ff80 8080 8080 8080 8080 00"), "BadInteger(offset: 1) after 0"))
}

test "Huffman padding longer than 7 bits is BadHuffman" (sys: &System) {
    h := sys.heap.Alloc()
    // :path, without indexing, value "a" (00011) then 11 bits of ones.
    Assert(ErrIs(try RunNew(sys.arena, h, "04 82 1fff"), "BadHuffman(offset: 1) after 0"))
    // The same "a" with 3 bits of padding decodes.
    Assert(ErrIs(try RunNew(sys.arena, h, "04 81 1f"), "ok 1"))
    // Eight '0's end on a byte boundary; a whole byte of ones after them is
    // 8 bits of padding.
    Assert(ErrIs(try RunNew(sys.arena, h, "04 86 0000 0000 00ff"), "BadHuffman(offset: 1) after 0"))
}

test "Huffman padding that is not all ones is BadHuffman" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(ErrIs(try RunNew(sys.arena, h, "04 81 1e"), "BadHuffman(offset: 1) after 0"))
}

test "EOS inside a Huffman string is BadHuffman" (sys: &System) {
    h := sys.heap.Alloc()
    // 32 ones: the 30 of EOS, then two.
    Assert(ErrIs(try RunNew(sys.arena, h, "04 84 ffff ffff"), "BadHuffman(offset: 1) after 0"))
    // EOS in a literal name is caught the same way.
    Assert(ErrIs(try RunNew(sys.arena, h, "00 84 ffff ffff 01 61"), "BadHuffman(offset: 1) after 0"))
}

test "index 0 and an index past the tables are BadIndex" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(ErrIs(try RunNew(sys.arena, h, "80"), "BadIndex(offset: 0, index: 0) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "82 be"), "BadIndex(offset: 1, index: 62) after 1"))
    // A literal's name index is checked the same way.
    Assert(ErrIs(try RunNew(sys.arena, h, "7e 01 61"), "BadIndex(offset: 0, index: 62) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "0f 2f 01 61"), "BadIndex(offset: 0, index: 62) after 0"))
}

test "a block that ends inside a representation is Truncated" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(ErrIs(try RunNew(sys.arena, h, "ff"), "Truncated(offset: 0) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "04 85 61"), "Truncated(offset: 1) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "40 0a 63"), "Truncated(offset: 1) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "40 01 61"), "Truncated(offset: 3) after 0"))
    Assert(ErrIs(try RunNew(sys.arena, h, "04"), "Truncated(offset: 1) after 0"))
}

test "a list over its budget is TooLarge after the whole block, and the table stays in sync" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 60)
    defer dec.Free(h)
    // :method GET (42) fits; custom-key: custom-header (55) would make 97,
    // so it is not given to the sink, but it is still added to the table.
    out := try RunHex(sys.arena, h, mut dec, "82 400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572")
    Assert(ErrIs(out, "too large 1"))
    entries := ["custom-key: custom-header"]
    Assert(TableIs(&dec.table, entries[..], 55))
    // The next block names the entry by index, as the peer's table has it.
    a := sys.arena
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, try Unhex(a, "be"))
    want := ["custom-key: custom-header"]
    Assert(PairsAre(&got, want[..]))
}

test "a Huffman literal over the budget is still added to the table" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 60)
    defer dec.Free(h)
    // C.4.3's custom-key: custom-value, Huffman coded, after :method GET.
    out := try RunHex(sys.arena, h, mut dec, "82 4088 25a8 49e9 5ba9 7d7f 8925 a849 e95b b8e8 b4bf")
    Assert(ErrIs(out, "too large 1"))
    entries := ["custom-key: custom-value"]
    Assert(TableIs(&dec.table, entries[..], 54))
    // Alone it fits: 54 bytes of list.
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "be"), "ok 1"))
}

test "a compression error after the budget is passed is what Decode returns" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, DefaultTableBytes, 40)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "82 80"), "BadIndex(offset: 1, index: 0) after 0"))
}

test "a field whose name entry its own insertion evicts keeps its name (RFC 7541 4.4)" (sys: &System) {
    h := sys.heap.Alloc()
    // A table of 100 bytes: custom-key: custom-header (55), then a field
    // naming entry 62 whose entry (52) evicts it.
    var dec := try Decoder.New(h, 100, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"), "ok 1"))
    a := sys.arena
    var got := FieldBlock.New(h)
    defer got.Free(h)
    try Fits(h, mut dec, mut got, try Unhex(a, "7e 0a 6161 6161 6161 6161 6161"))
    want := ["custom-key: aaaaaaaaaa"]
    Assert(PairsAre(&got, want[..]))
    entries := ["custom-key: aaaaaaaaaa"]
    Assert(TableIs(&dec.table, entries[..], 52))
}

test "over the budget, a field whose name entry its own insertion evicts keeps its name" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, 100, 70)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"), "ok 1"))
    // :method GET (42) first, so the next field (52) is over the budget.
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "82 7e 0a 6161 6161 6161 6161 6161"), "too large 1"))
    entries := ["custom-key: aaaaaaaaaa"]
    Assert(TableIs(&dec.table, entries[..], 52))
}

test "an entry larger than the table empties it and is not added" (sys: &System) {
    h := sys.heap.Alloc()
    var dec := try Decoder.New(h, 100, 1 << 16)
    defer dec.Free(h)
    Assert(ErrIs(try RunHex(sys.arena, h, mut dec, "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"), "ok 1"))
    // x: 70 bytes of "a", 103 bytes of entry.
    a := sys.arena
    var t := Text.New(a)
    try t.Push("40 01 78 46")
    for _ in 0..70 {
        try t.Push(" 61")
    }
    Assert(ErrIs(try RunHex(a, h, mut dec, t.Str()), "ok 1"))
    Assert(dec.TableLen() == 0 && dec.TableSize() == 0)
}

test "every prefix of a valid block fails cleanly or decodes" (sys: &System) {
    a := sys.arena
    h := sys.heap.Alloc()
    blocks := ["8286 8441 8cf1 e3c2 e5f2 3a6b a0ab 90f4 ff", "4882 6402 5885 aec3 771a 4b61 96d0 7abe 9410 54d4 44a8 2005 9504 0b81 66e0 82a6 2d1b ff6e 919d 29ad 1718 63c7 8f0b 97c8 e9ae 82ae 43d3", "400a 6375 7374 6f6d 2d6b 6579 0d63 7573 746f 6d2d 6865 6164 6572"]
    for hex in blocks {
        wire := try Unhex(a, hex)
        for n in 0..wire.Len() + 1 {
            var dec := try Decoder.New(h, 256, 1 << 16)
            var got := FieldBlock.New(h)
            _ = dec.Decode(h, mut got, wire[0..n]) else e {
                _ = e
                Outcome.Fits
            }
            got.Free(h)
            dec.Free(h)
        }
    }
}
