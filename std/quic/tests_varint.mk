// Why: QUIC variable-length integer decoding and encoding underlie every
// packet header, frame and transport parameter; off-by-one masks or missing
// truncation checks break wire interoperability and safety.
//
// What: test blocks for RFC 9000 section 16 and Appendix A.1 varints.
//
// How: decodes the RFC 9000 A.1 vectors, checks non-minimal acceptance,
// checks truncated and out-of-range boundaries, and verifies WriteVarint
// round-trips using the shortest encoding.

AssertNone[T](x: ?T) {
    match x {
        some(_) { Panic("expected none") }
        none {}
    }
}

test "RFC 9000 16: varints decode the A.1 samples, and a cut-short varint is FRAME_ENCODING_ERROR" (sys: &System) {
    // 0xc2197c5eff14e88c -> 151,288,809,941,952,652
    s8: [8]u8 = [0xc2, 0x19, 0x7c, 0x5e, 0xff, 0x14, 0xe8, 0x8c]
    r8 := ReadVarint(s8[..], 0).Must()
    Assert(r8.v == 151_288_809_941_952_652 && r8.n == 8)
    // 0x9d7f3e7d -> 494,878,333
    s4: [4]u8 = [0x9d, 0x7f, 0x3e, 0x7d]
    r4 := ReadVarint(s4[..], 0).Must()
    Assert(r4.v == 494_878_333 && r4.n == 4)
    // 0x7bbd -> 15,293
    s2: [2]u8 = [0x7b, 0xbd]
    r2 := ReadVarint(s2[..], 0).Must()
    Assert(r2.v == 15_293 && r2.n == 2)
    // 0x25 -> 37
    s1: [1]u8 = [0x25]
    r1 := ReadVarint(s1[..], 0).Must()
    Assert(r1.v == 37 && r1.n == 1)
    // 0x4025 -> 37
    s2nm: [2]u8 = [0x40, 0x25]
    r2nm := ReadVarint(s2nm[..], 0).Must()
    Assert(r2nm.v == 37 && r2nm.n == 2)

    // Truncated varints return none from ReadVarint and FRAME_ENCODING_ERROR in FrameIter.
    AssertNone(ReadVarint(b"", 0))
    AssertNone(ReadVarint(s2[0..1], 0))
    AssertNone(ReadVarint(s4[0..3], 0))
    AssertNone(ReadVarint(s8[0..7], 0))
    AssertNone(ReadVarint(s8[..], -1))
    AssertNone(ReadVarint(s8[..], 8))

    // Cut-short varint inside a frame payload (MAX_DATA 0x10 with truncated 8-byte varint).
    bad_frame: [5]u8 = [0x10, 0xc0, 0x00, 0x00, 0x00]
    var it := FrameIter.New(bad_frame[..], true)
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var got_err := false
    _ = it.Next(mut ranges[..]) else e {
        match e {
            TransportError(code, frame) {
                Assert(code == ErrFrameEncoding && frame == FrameMaxData)
                got_err = true
            }
            _ { Panic("unexpected error") }
        }
        none
    }
    Assert(got_err)
}

test "RFC 9000 16: a non-minimal varint is accepted outside the frame type" (sys: &System) {
    // MAX_DATA (0x10) with value 37 encoded on 2, 4 and 8 bytes.
    b2: [3]u8 = [0x10, 0x40, 0x25]
    b4: [5]u8 = [0x10, 0x80, 0x00, 0x00, 0x25]
    b8: [9]u8 = [0x10, 0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x25]
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    for buf in [b2[..], b4[..], b8[..]] {
        var it := FrameIter.New(buf, true)
        f := it.Next(mut ranges[..]).Must().Must()
        match f {
            .MaxData(max: v) { Assert(v == 37) }
            _ { Panic("expected MaxData") }
        }
        AssertNone(it.Next(mut ranges[..]).Must())
    }

    // Writer always emits the shortest encoding.
    var out: [8]u8 = [0; 8]
    Assert(VarintLen(0) == 1 && VarintLen(63) == 1)
    Assert(VarintLen(64) == 2 && VarintLen(16383) == 2)
    Assert(VarintLen(16384) == 4 && VarintLen(1_073_741_823) == 4)
    Assert(VarintLen(1_073_741_824) == 8 && VarintLen(MaxVarint) == 8)
    Assert(VarintLen(MaxVarint + 1) == 0)

    vals: [8]u64 = [0, 37, 63, 64, 15_293, 494_878_333, 151_288_809_941_952_652, MaxVarint]
    for v in vals {
        n := WriteVarint(mut out[..], v)
        Assert(n == VarintLen(v))
        dec := ReadVarint(out[0..n], 0).Must()
        Assert(dec.v == v && dec.n == n)
    }
    // Too-short output buffer and >MaxVarint write 0 bytes.
    Assert(WriteVarint(mut out[0..1], 64) == 0)
    Assert(WriteVarint(mut out[..], MaxVarint + 1) == 0)
}
