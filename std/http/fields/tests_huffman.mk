// Why: the Huffman code covers 256 symbols, of which RFC 7541 Appendix C
// uses a few dozen; every one must code and decode, and the decoder's
// bounds and errors must hold for any input.
//
// What: tests of HuffLen, HuffPut, HuffMaxOut, HuffDecode and HuffValid:
// every byte both ways, the length and the output bound, a short output
// buffer, and the C.4 strings.
//
// How: strings of every byte, in order and shuffled by a fixed step, coded
// and decoded back; the C.4 bytes copied from the RFC.

test "every byte value codes and decodes back" (sys: &System) {
    a := sys.arena
    var s := try a.Slice[u8](256 * 3)
    for i in 0..s.Len() {
        s[i] = Trunc[u8](i * 97 + i / 256)
    }
    n := HuffLen(s[..])
    out := try a.Slice[u8](n)
    Assert(HuffPut(out, s[..]) == n)
    back := try a.Slice[u8](HuffMaxOut(n))
    got := HuffDecode(back, out[..]).Must()
    Assert(got == s.Len() && BytesEq(back[0..got], s[..]))
    Assert(HuffValid(out[..]).Must() == s.Len())
}

test "each single byte codes to its row's length and decodes back" {
    var buf: [8]u8 = [0; 8]
    var back: [8]u8 = [0; 8]
    for c in 0..256 {
        one: [1]u8 = [Trunc[u8](c)]
        n := HuffPut(mut buf[..], one[..])
        Assert(n == (int(HuffBits[c]) + 7) / 8)
        got := HuffDecode(mut back[..], buf[0..n]).Must()
        Assert(got == 1 && back[0] == one[0])
    }
}

test "the C.4.1 string codes to the RFC's bytes" (sys: &System) {
    a := sys.arena
    s := "www.example.com".AsBytes()
    out := try a.Slice[u8](HuffLen(s))
    _ = HuffPut(out, s)
    Assert(BytesEq(out[..], try Unhex(a, "f1e3 c2e5 f23a 6ba0 ab90 f4ff")))
}

test "HuffMaxOut bounds the output of five-bit codes exactly" (sys: &System) {
    a := sys.arena
    // '0', '1', '2', 'a', 'c', 'e', 'i', 'o', 's', 't' are the 5-bit codes.
    s := "0120120120120aceiost".AsBytes()
    n := HuffLen(s)
    out := try a.Slice[u8](n)
    _ = HuffPut(out, s)
    Assert(HuffMaxOut(n) >= s.Len())
    Assert(HuffMaxOut(0) == 0 && HuffMaxOut(5) == 8 && HuffMaxOut(1) == 1 && HuffMaxOut(4) == 6)
}

test "a decode into a buffer too short fails, and HuffValid tells it from a bad string" (sys: &System) {
    a := sys.arena
    s := "custom-value".AsBytes()
    out := try a.Slice[u8](HuffLen(s))
    _ = HuffPut(out, s)
    var small: [4]u8 = [0; 4]
    Assert(HuffDecode(mut small[..], out[..]).IsNone())
    Assert(HuffValid(out[..]).Must() == s.Len())
    bad: [1]u8 = [0x1E]
    Assert(HuffValid(bad[..]).IsNone())
}

test "the empty string is valid and decodes to nothing" {
    var out: [1]u8 = [0; 1]
    e: [1]u8 = [0]
    Assert(HuffDecode(mut out[..], e[0..0]).Must() == 0)
    Assert(HuffLen(e[0..0]) == 0)
}
