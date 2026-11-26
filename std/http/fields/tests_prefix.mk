// Why: prefix integers carry every index and length of a block, so their
// encoding must match RFC 7541 C.1 bit for bit and their decoding must
// refuse what 5.1 and the 10-byte bound refuse.
//
// What: tests of IntLen, PutInt, GetInt, StrHead and PutStr: the C.1
// examples both ways, the bounds, and the errors.
//
// How: each test builds or reads a few bytes and checks every result.

test "C.1.1: 10 with a 5-bit prefix is one byte" {
    var out: [4]u8 = [0xE0; 4]
    Assert(IntLen(10, 5) == 1)
    Assert(PutInt(mut out[..], 10, 5, 0xE0) == 1)
    Assert(out[0] == 0xEA)
    (value, next) := GetInt(out[..], 0, 5).Must()
    Assert(value == 10 && next == 1)
}

test "C.1.2: 1337 with a 5-bit prefix is 1f 9a 0a" {
    var out: [4]u8 = [0; 4]
    Assert(IntLen(1337, 5) == 3)
    Assert(PutInt(mut out[..], 1337, 5, 0) == 3)
    Assert(out[0] == 0x1F && out[1] == 0x9A && out[2] == 0x0A)
    (value, next) := GetInt(out[..], 0, 5).Must()
    Assert(value == 1337 && next == 3)
}

test "C.1.3: 42 with an 8-bit prefix is one byte" {
    var out: [2]u8 = [0; 2]
    Assert(PutInt(mut out[..], 42, 8, 0) == 1)
    Assert(out[0] == 42)
    (value, next) := GetInt(out[..], 0, 8).Must()
    Assert(value == 42 && next == 1)
}

test "a value of exactly 2^N - 1 takes a second byte of 0" {
    var out: [4]u8 = [0; 4]
    Assert(IntLen(31, 5) == 2)
    Assert(PutInt(mut out[..], 31, 5, 0) == 2)
    Assert(out[0] == 0x1F && out[1] == 0)
    (value, next) := GetInt(out[..], 0, 5).Must()
    Assert(value == 31 && next == 2)
}

test "prefix integers round-trip at every prefix width" {
    var out: [12]u8 = [0; 12]
    vals := [0, 1, 6, 7, 126, 127, 128, 254, 255, 256, 16383, 16384, 1 << 31, 1 << 40, 0x7FFF_FFFF_FFFF_FFFF]
    for n in 1..9 {
        for v in vals {
            k := PutInt(mut out[..], v, n, 0)
            Assert(k == IntLen(v, n))
            Assert(k <= MaxIntBytes)
            (value, next) := GetInt(out[0..k], 0, n).Must()
            Assert(value == v && next == k)
        }
    }
}

test "the flags above the prefix are kept and not read as the value" {
    var out: [4]u8 = [0; 4]
    _ = PutInt(mut out[..], 5, 4, 0x10)
    Assert(out[0] == 0x15)
    (value, ..) := GetInt(out[..], 0, 4).Must()
    Assert(value == 5)
}

test "an integer cut by the end of the block is Truncated at its start" {
    b: [3]u8 = [0x40, 0x1F, 0x9A]
    match GetInt(b[..], 1, 5) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                Truncated(offset) { Assert(offset == 1) }
                _ { Assert(false) }
            }
        }
    }
    e: [1]u8 = [0x00]
    _ = GetInt(e[0..0], 0, 5) else {
        return
    }
    Assert(false)
}

test "an integer longer than 10 bytes is BadInteger" {
    // The prefix and ten continuation bytes, the last ending the integer.
    b: [11]u8 = [0x1F, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00]
    match GetInt(b[..], 0, 5) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadInteger(offset) { Assert(offset == 0) }
                _ { Assert(false) }
            }
        }
    }
    // Ten bytes in all is the most allowed.
    c: [10]u8 = [0x1F, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00]
    (value, next) := GetInt(c[..], 0, 5).Must()
    Assert(value == 31 && next == 10)
}

test "an integer past the int range is BadInteger" {
    // 31 + 127 × 2^56 + ... with the ninth group 0x7F: above 2^63 - 1.
    b: [10]u8 = [0x1F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F]
    match GetInt(b[..], 0, 5) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                BadInteger(offset) { Assert(offset == 0) }
                _ { Assert(false) }
            }
        }
    }
}

test "a string whose length passes the block is Truncated" {
    b: [3]u8 = [0x05, 0x61, 0x62]
    match StrHead(b[..], 0, 7) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                Truncated(offset) { Assert(offset == 0) }
                _ { Assert(false) }
            }
        }
    }
    // A length near the int range does not overflow the bound check.
    c: [11]u8 = [0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x3F, 0x61]
    _ = StrHead(c[..], 0, 7) else {
        return
    }
    Assert(false)
}

test "PutStr writes raw or Huffman with the flag above the prefix" (sys: &System) {
    var out: [32]u8 = [0; 32]
    s := "custom-key".AsBytes()
    raw := StrCost(s, 7, false)
    Assert(!raw.huff && raw.len == 11)
    Assert(PutStr(mut out[..], s, 7, 0, false) == 11)
    Assert(out[0] == 0x0A)
    huff := StrCost(s, 7, true)
    Assert(huff.huff && huff.len == 9)
    Assert(PutStr(mut out[..], s, 7, 0, true) == 9)
    want := try Unhex(sys.arena, "88 25a8 49e9 5ba9 7d7f")
    Assert(BytesEq(out[0..9], want))
    (huffman, start, end) := StrHead(out[0..9], 0, 7).Must()
    Assert(huffman && start == 1 && end == 9)
}
