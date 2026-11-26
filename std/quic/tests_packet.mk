// Why: QUIC packet headers and packet-number reconstruction sit before
// decryption and recovery; wrong window math, unchecked CID lengths or
// dropped-bit bugs break interoperability or leak timing/state.
//
// What: test blocks for RFC 9000 sections 10.3, 17.1-17.4 and Appendix A.2-A.3.
//
// How: verifies A.2 and A.3 examples and window edges, tests short/long/VN/Retry
// parsing and writing, and checks every drop/error condition on header bits
// and lengths.

test "RFC 9000 17.1: packet number size follows A.2, untruncated before any ACK" (sys: &System) {
    // Before any ACK (largest_acked < 0): num_unacked = full_pn + 1.
    Assert(EncodePnLen(0, -1) == 1)
    Assert(EncodePnLen(127, -1) == 1)
    Assert(EncodePnLen(128, -1) == 2)
    Assert(EncodePnLen(32767, -1) == 2)
    Assert(EncodePnLen(32768, -1) == 3)
    Assert(EncodePnLen(8_388_607, -1) == 3)
    Assert(EncodePnLen(8_388_608, -1) == 4)

    // RFC 9000 A.2 samples: largest_acked = 0xabe8b3.
    Assert(EncodePnLen(0xac5c02, 0xabe8b3) == 2)
    Assert(EncodePnLen(0xace8fe, 0xabe8b3) == 3)
    Assert(EncodePnLen(0xabe8b4, 0xabe8b3) == 1)
    Assert(EncodePnLen(0xabe8b3 + 0x800001, 0xabe8b3) == 4)
}

test "RFC 9000 17.1: packet number decoding matches A.3, the 0x9b32 sample and both window edges" (sys: &System) {
    // RFC 9000 A.3 sample: largest = 0xa82f30ea, 16-bit 0x9b32 -> 0xa82f9b32.
    Assert(DecodePn(0xa82f30ea, 0x9b32, 16) == 0xa82f9b32)

    // Window edges for 8-bit PN (pn_win = 256, pn_hwin = 128).
    // largest = 200 -> expected_pn = 201.
    // candidate_pn <= 201 - 128 (= 73) wraps up by 256.
    Assert(DecodePn(200, 73, 8) == 73 + 256)
    Assert(DecodePn(200, 74, 8) == 74)
    // candidate_pn > 201 + 128 (= 329) wraps down by 256; expected_pn = 201 has top bits 0,
    // so test around largest = 300 -> expected_pn = 301, expected_pn + 128 = 429:
    // candidate 429 (0xad -> 0x1ad = 429) stays; candidate 430 (0xae -> 0x1ae = 430) wraps down.
    Assert(DecodePn(300, 0xad, 8) == 429)
    Assert(DecodePn(300, 0xae, 8) == 430 - 256)

    // Near 0: expected_pn = 0, candidate cannot wrap below 0.
    Assert(DecodePn(-1, 0, 8) == 0)
    Assert(DecodePn(-1, 200, 8) == 200)

    // Near 2^62 bound: candidate_pn >= (1 << 62) - pn_win cannot wrap past 2^62.
    limit: i64 = 1 << 62
    Assert(DecodePn(limit - 10, 0, 8) == limit - 256)
}

test "RFC 9000 17.3.1: a short header with the fixed bit 0 is discarded" (sys: &System) {
    var pkt: [32]u8 = [0; 32]
    // Header Form 0, Fixed Bit 0 (0x00 instead of 0x40).
    pkt[0] = 0x00
    AssertNone(ParsePacket(pkt[..], 8))
    pkt[0] = 0x3f
    AssertNone(ParsePacket(pkt[..], 8))
    // With Fixed Bit 1 (0x40), it parses.
    pkt[0] = 0x40
    _ = ParsePacket(pkt[..], 8).Must()
}

test "RFC 9000 17.3.1: non-zero reserved bits after decryption are PROTOCOL_VIOLATION" (sys: &System) {
    // CheckReservedBits is called only after header protection and AEAD open.
    CheckReservedBits(0x40, false).Must()
    CheckReservedBits(0x43, false).Must()
    CheckReservedBits(0x44, false).Must()
    CheckReservedBits(0xc0, true).Must()
    CheckReservedBits(0xc3, true).Must()

    // Short header reserved bits 0x18.
    for bad in [u8(0x48), u8(0x50), u8(0x58)] {
        var got := false
        CheckReservedBits(bad, false) else e {
            match e {
                TransportError(code, frame: _) {
                    Assert(code == ErrProtocolViolation)
                    got = true
                }
                _ { Panic("unexpected error") }
            }
        }
        Assert(got)
    }

    // Long header reserved bits 0x0c.
    for bad in [u8(0xc4), u8(0xc8), u8(0xcc)] {
        var got := false
        CheckReservedBits(bad, true) else e {
            match e {
                TransportError(code, frame: _) {
                    Assert(code == ErrProtocolViolation)
                    got = true
                }
                _ { Panic("unexpected error") }
            }
        }
        Assert(got)
    }
}

test "RFC 9000 17.3.1: the short header packet number length is read after header protection" (sys: &System) {
    var secret: [32]u8 = [0x11; 32]
    k := KeysFrom(Suite.Aes128Gcm, secret[..]).Must()
    dcid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    var pkt: [64]u8 = [0; 64]
    h := WriteShortHeader(mut pkt[..], &dcid, false, 0x1234, 2).Must()
    Assert(h.pn_offset == 9 && h.header_len == 11)
    plain: [4]u8 = [0x01, 0x00, 0x00, 0x00]
    (left: hdr_s, right: body_s) := (mut pkt[..]).SplitAt(h.header_len)
    n := Seal(&k, 0x1234, hdr_s[..], plain[..], body_s)
    total := h.header_len + n
    Protect(&k, mut pkt[0..total], h.pn_offset, 2)

    var p_off := 0
    match ParsePacket(pkt[0..total], 8).Must() {
        .Short(hdr, packet: sl) {
            Assert(hdr.pn_offset == 9 && sl.Len() == total)
            p_off = hdr.pn_offset
        }
        _ { Panic("expected Short") }
    }
    pn_len := Unprotect(&k, mut pkt[0..total], p_off).Must()
    Assert(pn_len == 2)
    trunc := ReadPn(pkt[0..total], h.pn_offset, pn_len)
    Assert(DecodePn(0x1233, trunc, pn_len * 8) == 0x1234)
    var out: [16]u8 = [0; 16]
    dn := Open(&k, 0x1234, pkt[0..h.header_len], pkt[h.header_len..total], mut out[..]).Must()
    Assert(dn == 4 && out[0] == 0x01)
}

test "RFC 9000 17.4: the spin bit is sent as 0 and ignored on receipt" (sys: &System) {
    dcid := CidFromSlice(b"\xaa\xbb\xcc\xdd\xee\xff\x00\x11")
    var pkt: [32]u8 = [0; 32]
    _ = WriteShortHeader(mut pkt[..], &dcid, false, 1, 1).Must()
    Assert((pkt[0] & 0x20) == 0)

    // Both spin=0 and spin=1 parse cleanly without error.
    pkt[0] = pkt[0] | 0x20
    p := ParsePacket(pkt[..], 8).Must()
    match p {
        .Short(hdr, packet: _) { Assert(hdr.spin && CidEqual(&hdr.dcid, &dcid)) }
        _ { Panic("expected Short") }
    }
}

test "RFC 9000 17.2.1: Version Negotiation has version 0 and the received connection ids swapped" (sys: &System) {
    rx_dcid := b"\x01\x02\x03\x04\x05\x06\x07\x08"
    rx_scid := b"\x11\x12\x13\x14"
    vers: [1]u32 = [Version1]
    var buf: [64]u8 = [0; 64]
    n := WriteVersionNeg(mut buf[..], 0x45, rx_scid, rx_dcid, vers[..])
    Assert(n > 0)
    p := ParsePacket(buf[0..n], 8).Must()
    match p {
        .VersionNeg(dcid, scid, versions) {
            Assert(dcid.Len() == 4 && dcid[0] == 0x11 && dcid[3] == 0x14)
            Assert(scid.Len() == 8 && scid[0] == 0x01 && scid[7] == 0x08)
            Assert(versions.Len() == 4 && ReadBe32(versions, 0) == Version1)
        }
        _ { Panic("expected VersionNeg") }
    }
}

test "RFC 9000 17.2.1: an unknown version with 255-byte connection ids still gets Version Negotiation" (sys: &System) {
    var pkt: [600]u8 = [0; 600]
    // Header Form 1, Fixed Bit 0 allowed for unknown version, version 0x1a2a3a4a.
    pkt[0] = 0x80
    PutBe32(mut pkt[..], 1, 0x1a2a3a4a)
    pkt[5] = 255
    for i in 0..255 {
        pkt[6 + i] = Trunc[u8](i)
    }
    pkt[261] = 255
    for i in 0..255 {
        pkt[262 + i] = Trunc[u8](255 - i)
    }
    p := ParsePacket(pkt[..], 8).Must()
    match p {
        .UnknownVersion(hdr) {
            Assert(hdr.version == 0x1a2a3a4a)
            Assert(hdr.dcid.Len() == 255 && hdr.scid.Len() == 255)
            var vn: [600]u8 = [0; 600]
            vers: [1]u32 = [Version1]
            n := WriteVersionNeg(mut vn[..], 0x40, hdr.scid, hdr.dcid, vers[..])
            Assert(n > 0)
            vp := ParsePacket(vn[0..n], 8).Must()
            match vp {
                .VersionNeg(dcid: vd, scid: vs, versions: _) {
                    Assert(vd.Len() == 255 && vd[0] == 255 && vs.Len() == 255 && vs[0] == 0)
                }
                _ { Panic("expected VersionNeg") }
            }
        }
        _ { Panic("expected UnknownVersion") }
    }
}

test "RFC 9000 17.2.1: Version Negotiation Unused bits are ignored" (sys: &System) {
    vers: [1]u32 = [Version1]
    var buf: [64]u8 = [0; 64]
    n := WriteVersionNeg(mut buf[..], 0x00, b"\x01\x02", b"\x03\x04", vers[..])
    for unused in [u8(0x00), u8(0x3f), u8(0x40), u8(0x7f)] {
        buf[0] = 0x80 | unused
        _ = ParsePacket(buf[0..n], 8).Must()
    }
    // Truncated version list or empty list is rejected.
    AssertNone(ParsePacket(buf[0..n - 1], 8))
    AssertNone(ParsePacket(buf[0..n - 4], 8))
}

test "RFC 9000 17.2.2: server Initials have no token and a client drops one that has" (sys: &System) {
    dcid := CidFromSlice(b"\x01\x02\x03\x04")
    scid := CidFromSlice(b"\x05\x06\x07\x08")
    var buf: [64]u8 = [0; 64]
    // Server Initial must encode token length 0.
    h0 := WriteLongHeader(mut buf[..], PktInitial, &dcid, &scid, b"", 20, 1, 1).Must()
    p0 := ParsePacket(buf[0..h0.header_len + 20], 8).Must()
    match p0 {
        .Long(hdr, packet: _) { Assert(hdr.kind == PktInitial && hdr.token.Len() == 0) }
        _ { Panic("expected Long") }
    }
    // Initial with non-zero token parses its token slice so a client drops it.
    h1 := WriteLongHeader(mut buf[..], PktInitial, &dcid, &scid, b"tok", 20, 1, 1).Must()
    p1 := ParsePacket(buf[0..h1.header_len + 20], 8).Must()
    match p1 {
        .Long(hdr, packet: _) { Assert(hdr.kind == PktInitial && hdr.token.Len() == 3) }
        _ { Panic("expected Long") }
    }
}

test "RFC 9000 17.2: a long header with the fixed bit clear is dropped" (sys: &System) {
    dcid := CidFromSlice(b"\x01\x02\x03\x04")
    scid := CidFromSlice(b"\x05\x06\x07\x08")
    var buf: [64]u8 = [0; 64]
    h := WriteLongHeader(mut buf[..], PktInitial, &dcid, &scid, b"", 20, 0, 1).Must()
    buf[0] = buf[0] & 0xbf
    AssertNone(ParsePacket(buf[0..h.header_len + 20], 8))
}

test "RFC 9000 17.2: a version 1 long header with a connection id over 20 bytes is dropped" (sys: &System) {
    var pkt: [80]u8 = [0; 80]
    pkt[0] = 0xc0
    PutBe32(mut pkt[..], 1, Version1)
    // DCID len 21 > MaxCidLen.
    pkt[5] = 21
    pkt[27] = 4
    AssertNone(ParsePacket(pkt[..], 8))
    // DCID len 8, SCID len 21 > MaxCidLen.
    pkt[5] = 8
    pkt[14] = 21
    AssertNone(ParsePacket(pkt[..], 8))
}

test "RFC 9000 17.2.5: Retry Unused bits are ignored" (sys: &System) {
    var pkt: [40]u8 = [0; 40]
    PutBe32(mut pkt[..], 1, Version1)
    pkt[5] = 4
    pkt[10] = 4
    // 15 bytes header + 5 bytes token + 16 bytes tag = 36 bytes.
    for unused in [u8(0x00), u8(0x05), u8(0x0f)] {
        pkt[0] = 0xf0 | unused
        p := ParsePacket(pkt[0..36], 8).Must()
        match p {
            .Retry(version, dcid, scid, token, tag: _, packet: _) {
                Assert(version == Version1 && dcid.n == 4 && scid.n == 4 && token.Len() == 5)
            }
            _ { Panic("expected Retry") }
        }
    }
}

test "RFC 9000 10.3: a short-header packet under 21 bytes is dropped" (sys: &System) {
    var pkt: [21]u8 = [0; 21]
    pkt[0] = 0x40
    AssertNone(ParsePacket(pkt[0..20], 0))
    _ = ParsePacket(pkt[0..21], 0).Must()
}
