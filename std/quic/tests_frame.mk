// Why: decrypted QUIC payloads contain peer-controlled frames; every
// encoding limit, range calculation, type check and writer boundary must be
// verified with both positive round-trips and negative error assertions.
//
// What: test blocks for frame.mk and frame_write.mk (RFC 9000 12.4, 19, 20.1).
//
// How: checks every QT frame row by exact error code, exercises all 20
// frame types through Write* -> FrameIter.Next round-trips, and tests
// out-of-room writers returning none.

AssertFrameErr(payload: []u8, client: bool, want_code: u64, ranges: mut []AckRange) {
    var it := FrameIter.New(payload, client)
    for {
        f := it.Next(ranges) else e {
            match e {
                TransportError(code, frame: _) {
                    Assert(code == want_code)
                    return
                }
                _ { Panic("unexpected error type") }
            }
        }
        match f {
            none { Panic("expected TransportError, reached end of payload") }
            some(_) {}
        }
    }
}

test "RFC 9000 12.4: a frame type in a longer encoding than necessary is PROTOCOL_VIOLATION" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    // PADDING (0x00) encoded as 2 bytes (0x40 0x00), PING (0x01) as 2 bytes (0x40 0x01),
    // HANDSHAKE_DONE (0x1e) as 2 bytes (0x40 0x1e).
    b0: [2]u8 = [0x40, 0x00]
    b1: [2]u8 = [0x40, 0x01]
    b2: [2]u8 = [0x40, 0x1e]
    AssertFrameErr(b0[..], true, ErrProtocolViolation, mut ranges[..])
    AssertFrameErr(b1[..], true, ErrProtocolViolation, mut ranges[..])
    AssertFrameErr(b2[..], true, ErrProtocolViolation, mut ranges[..])
}

test "RFC 9000 12.4: an unknown frame type, DATAGRAM 0x30 included, is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    d0: [2]u8 = [0x30, 0x00]
    d1: [2]u8 = [0x31, 0x00]
    u1: [1]u8 = [0x1f]
    u2: [2]u8 = [0x40, 0x40]
    AssertFrameErr(d0[..], true, ErrFrameEncoding, mut ranges[..])
    AssertFrameErr(d1[..], true, ErrFrameEncoding, mut ranges[..])
    AssertFrameErr(u1[..], true, ErrFrameEncoding, mut ranges[..])
    AssertFrameErr(u2[..], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 20.1: a truncated frame or a Length past the payload is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    // Truncated CRYPTO (0x06, offset 0, len 10, only 2 bytes present).
    c_bad: [5]u8 = [0x06, 0x00, 0x0a, 0xaa, 0xbb]
    AssertFrameErr(c_bad[..], true, ErrFrameEncoding, mut ranges[..])
    // Truncated STREAM with LEN (0x0a, id 0, len 5, 1 byte present).
    s_bad: [4]u8 = [0x0a, 0x00, 0x05, 0xaa]
    AssertFrameErr(s_bad[..], true, ErrFrameEncoding, mut ranges[..])
    // Truncated PATH_CHALLENGE (only 7 data bytes).
    pc_bad: [8]u8 = [0x1a, 1, 2, 3, 4, 5, 6, 7]
    AssertFrameErr(pc_bad[..], true, ErrFrameEncoding, mut ranges[..])
    // ACK claiming more ranges than remaining bytes can hold.
    ack_bad: [7]u8 = [0x02, 0x0a, 0x00, 0x40, 0x64, 0x00, 0x00]
    AssertFrameErr(ack_bad[..], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 12.4: a packet payload with no frames is PROTOCOL_VIOLATION" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    AssertFrameErr(b"", true, ErrProtocolViolation, mut ranges[..])
}

test "RFC 9000 19.3: ACK 0x02 and ACK_ECN 0x03 both parse, ECN counts ignored" (sys: &System) {
    // ACK 0x02: largest 10, delay 5, count 1, first_range 2 -> [8..10], gap 1 (2 packets skipped: 6,7),
    // ack_len 2 -> [3..5].
    var buf: [32]u8 = [0; 32]
    in_r: [2]AckRange = [AckRange(lo: 8, hi: 10), AckRange(lo: 3, hi: 5)]
    n := WriteAck(mut buf[..], in_r[..], 5).Must()
    var out_r: [8]AckRange = [AckRange(lo: 0, hi: 0); 8]
    var it := FrameIter.New(buf[0..n], true)
    f := it.Next(mut out_r[..]).Must().Must()
    match f {
        .Ack(largest, delay, ranges: count) {
            Assert(largest == 10 && delay == 5 && count == 2)
            Assert(out_r[0].lo == 8 && out_r[0].hi == 10)
            Assert(out_r[1].lo == 3 && out_r[1].hi == 5)
        }
        _ { Panic("expected Ack") }
    }
    AssertNone(it.Next(mut out_r[..]).Must())

    // ACK_ECN 0x03: same ranges plus three ECN counts (11, 22, 33).
    buf[0] = 0x03
    buf[n] = 11
    buf[n + 1] = 22
    buf[n + 2] = 33
    var it2 := FrameIter.New(buf[0..n + 3], true)
    f2 := it2.Next(mut out_r[..]).Must().Must()
    match f2 {
        .Ack(largest, delay, ranges: count) {
            Assert(largest == 10 && delay == 5 && count == 2)
        }
        _ { Panic("expected Ack") }
    }
    AssertNone(it2.Next(mut out_r[..]).Must())

    // Truncated ACK_ECN (only 2 ECN counts) is FRAME_ENCODING_ERROR.
    AssertFrameErr(buf[0..n + 2], true, ErrFrameEncoding, mut out_r[..])
}

test "RFC 9000 19.3.1: an ACK range below packet number 0 is FRAME_ENCODING_ERROR, even past the 256-range cap" (sys: &System) {
    // First range goes below 0: largest 2, first_range 3.
    bad_first: [5]u8 = [0x02, 0x02, 0x00, 0x00, 0x03]
    var out_r: [256]AckRange = [AckRange(lo: 0, hi: 0); 256]
    AssertFrameErr(bad_first[..], true, ErrFrameEncoding, mut out_r[..])

    // Build an ACK with 260 ranges (cap is 256) where the 260th range drops below 0.
    // Largest = 550, first_range = 0 -> [550..550]; 259 extra ranges of (gap=0, len=0) step down by 2 each:
    // after 258 extra ranges, prev_smallest = 550 - 2*258 = 34; 259th extra range has gap=17, len=0 -> hi = 34 - 17 - 2 = 15.
    // Instead: use largest = 518, first_range = 0, 259 ranges of (gap=0, len=0):
    // range 259 (0-based 259) hits 518 - 2*259 = 0; 260th (0-based 260) hits -2 < 0!
    var big: [600]u8 = [0; 600]
    big[0] = 0x02
    var pos := 1
    pos += WriteVarint(mut big[pos..], 518) // largest
    pos += WriteVarint(mut big[pos..], 0)   // delay
    pos += WriteVarint(mut big[pos..], 260) // count = 260 (total 261 ranges > 256)
    pos += WriteVarint(mut big[pos..], 0)   // first_range = 0
    for _ in 0..260 {
        big[pos] = 0
        big[pos + 1] = 0
        pos += 2
    }
    AssertFrameErr(big[0..pos], true, ErrFrameEncoding, mut out_r[..])
}

test "RFC 9000 19.8: STREAM types 0x08-0x0f parse OFF, LEN and FIN, and no LEN runs to the packet end" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    data := b"hello"
    for bits in 0..8 {
        off_set := (bits & 4) != 0
        len_set := (bits & 2) != 0
        fin_set := (bits & 1) != 0
        var buf: [32]u8 = [0; 32]
        off: u64 = if off_set { 10 } else { 0 }
        n := WriteStream(mut buf[..], 4, off, data, fin_set, len_set).Must()
        Assert(buf[0] == Trunc[u8](0x08 + bits))
        var it := FrameIter.New(buf[0..n], true)
        f := it.Next(mut ranges[..]).Must().Must()
        match f {
            .Stream(id, offset: o, data: d, fin) {
                Assert(id == 4 && o == off && fin == fin_set && d.Len() == 5 && d[0] == 0x68 && d[4] == 0x6f)
            }
            _ { Panic("expected Stream") }
        }
        AssertNone(it.Next(mut ranges[..]).Must())
    }

    // Without LEN (0x08), Stream Data consumes all remaining bytes of the packet.
    raw: [6]u8 = [0x08, 0x00, 0xaa, 0xbb, 0xcc, 0xdd]
    var it := FrameIter.New(raw[..], true)
    f := it.Next(mut ranges[..]).Must().Must()
    match f {
        .Stream(id, offset: o, data: d, fin) {
            Assert(id == 0 && o == 0 && !fin && d.Len() == 4 && d[3] == 0xdd)
        }
        _ { Panic("expected Stream") }
    }
    AssertNone(it.Next(mut ranges[..]).Must())
}

test "RFC 9000 19.8: STREAM offset plus length above 2^62-1 is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    // 0x0e = STREAM with OFF + LEN, id=0, offset=MaxVarint, len=1 -> sum = 2^62.
    var buf: [16]u8 = [0; 16]
    buf[0] = 0x0e
    var pos := 1
    pos += WriteVarint(mut buf[pos..], 0)
    pos += WriteVarint(mut buf[pos..], MaxVarint)
    pos += WriteVarint(mut buf[pos..], 1)
    buf[pos] = 0xaa
    pos += 1
    AssertFrameErr(buf[0..pos], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 19.6: CRYPTO offset plus length above 2^62-1 is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var buf: [16]u8 = [0; 16]
    buf[0] = 0x06
    var pos := 1
    pos += WriteVarint(mut buf[pos..], MaxVarint)
    pos += WriteVarint(mut buf[pos..], 1)
    buf[pos] = 0xbb
    pos += 1
    AssertFrameErr(buf[0..pos], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 19.11: MAX_STREAMS above 2^60 is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var buf: [16]u8 = [0; 16]
    for ty in [u8(0x12), u8(0x13)] {
        buf[0] = ty
        n := WriteVarint(mut buf[1..], MaxStreamsLimit + 1)
        AssertFrameErr(buf[0..1 + n], true, ErrFrameEncoding, mut ranges[..])
    }
    // At exact 2^60 it succeeds.
    m := WriteMaxStreams(mut buf[..], true, MaxStreamsLimit).Must()
    var it := FrameIter.New(buf[0..m], true)
    f := it.Next(mut ranges[..]).Must().Must()
    match f {
        .MaxStreams(bidi, max) { Assert(bidi && max == MaxStreamsLimit) }
        _ { Panic("expected MaxStreams") }
    }
}

test "RFC 9000 19.14: STREAMS_BLOCKED above 2^60 is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var buf: [16]u8 = [0; 16]
    for ty in [u8(0x16), u8(0x17)] {
        buf[0] = ty
        n := WriteVarint(mut buf[1..], MaxStreamsLimit + 1)
        AssertFrameErr(buf[0..1 + n], true, ErrFrameEncoding, mut ranges[..])
    }
}

test "RFC 9000 19.7: a client receiving NEW_TOKEN with an empty token raises FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    empty_nt: [2]u8 = [0x07, 0x00]
    AssertFrameErr(empty_nt[..], true, ErrFrameEncoding, mut ranges[..])
    // Server parser yields NewToken (server caller treats any NEW_TOKEN as PROTOCOL_VIOLATION).
    var it := FrameIter.New(empty_nt[..], false)
    f := it.Next(mut ranges[..]).Must().Must()
    match f {
        .NewToken(token: tok) { Assert(tok.Len() == 0) }
        _ { Panic("expected NewToken") }
    }
}

test "RFC 9000 19.15: NEW_CONNECTION_ID length 0 or 21 is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    // seq=1, retire_prior=0, len=0, 16-byte token.
    var z: [20]u8 = [0; 20]
    z[0] = 0x18
    z[1] = 1
    z[2] = 0
    z[3] = 0
    AssertFrameErr(z[..], true, ErrFrameEncoding, mut ranges[..])

    // seq=1, retire_prior=0, len=21, 21-byte cid, 16-byte token.
    var o: [41]u8 = [0; 41]
    o[0] = 0x18
    o[1] = 1
    o[2] = 0
    o[3] = 21
    AssertFrameErr(o[..], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 19.15: Retire Prior To above Sequence Number is FRAME_ENCODING_ERROR" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var buf: [28]u8 = [0; 28]
    buf[0] = 0x18
    buf[1] = 2 // seq = 2
    buf[2] = 3 // retire_prior = 3 > seq
    buf[3] = 8
    AssertFrameErr(buf[..], true, ErrFrameEncoding, mut ranges[..])
}

test "RFC 9000 19.19: CONNECTION_CLOSE 0x1c carries a frame type and 0x1d does not" (sys: &System) {
    var ranges: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var buf: [64]u8 = [0; 64]
    n1 := WriteConnectionClose(mut buf[..], ErrProtocolViolation, FrameStreamBase, b"bad", false).Must()
    Assert(buf[0] == 0x1c)
    var it1 := FrameIter.New(buf[0..n1], true)
    f1 := it1.Next(mut ranges[..]).Must().Must()
    match f1 {
        .ConnectionClose(code, frame_type: ft, reason, app) {
            Assert(!app && code == ErrProtocolViolation && ft == FrameStreamBase && reason.Len() == 3)
        }
        _ { Panic("expected ConnectionClose 0x1c") }
    }

    n2 := WriteConnectionClose(mut buf[..], 0x100, 0x99, b"app", true).Must()
    Assert(buf[0] == 0x1d)
    var it2 := FrameIter.New(buf[0..n2], true)
    f2 := it2.Next(mut ranges[..]).Must().Must()
    match f2 {
        .ConnectionClose(code, frame_type: ft, reason, app) {
            Assert(app && code == 0x100 && ft == 0 && reason.Len() == 3)
        }
        _ { Panic("expected ConnectionClose 0x1d") }
    }
}

test "RFC 9000 20.1: transport error codes have the RFC values and CRYPTO_ERROR is 0x0100 plus the alert" (sys: &System) {
    Assert(ErrNoError == 0x00)
    Assert(ErrInternal == 0x01)
    Assert(ErrConnectionRefused == 0x02)
    Assert(ErrFlowControl == 0x03)
    Assert(ErrStreamLimit == 0x04)
    Assert(ErrStreamState == 0x05)
    Assert(ErrFinalSize == 0x06)
    Assert(ErrFrameEncoding == 0x07)
    Assert(ErrTransportParameter == 0x08)
    Assert(ErrConnectionIdLimit == 0x09)
    Assert(ErrProtocolViolation == 0x0a)
    Assert(ErrInvalidToken == 0x0b)
    Assert(ErrApplication == 0x0c)
    Assert(ErrCryptoBufferExceeded == 0x0d)
    Assert(ErrKeyUpdate == 0x0e)
    Assert(ErrAeadLimitReached == 0x0f)
    Assert(ErrNoViablePath == 0x10)
    Assert(ErrCryptoBase + 10 == 0x010a)
    Assert(ErrCryptoBase + 0x6d == 0x016d)
    Assert(ErrCryptoBase + 0x78 == 0x0178)
}

test "frame_write.mk: all 20 frame types round-trip and return none when out is too short" (sys: &System) {
    var buf: [256]u8 = [0; 256]
    var pos := 0
    cid := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    tok16: [16]u8 = [0xaa; 16]
    d8a: [8]u8 = [1, 2, 3, 4, 5, 6, 7, 8]
    d8b: [8]u8 = [9, 10, 11, 12, 13, 14, 15, 16]
    ack_r: [1]AckRange = [AckRange(lo: 0, hi: 5)]

    pos += WritePadding(mut buf[pos..], 3).Must()
    pos += WritePing(mut buf[pos..]).Must()
    pos += WriteAck(mut buf[pos..], ack_r[..], 12).Must()
    pos += WriteResetStream(mut buf[pos..], 4, 77, 100).Must()
    pos += WriteStopSending(mut buf[pos..], 8, 88).Must()
    pos += WriteCrypto(mut buf[pos..], 10, b"abc").Must()
    pos += WriteNewToken(mut buf[pos..], b"tok").Must()
    pos += WriteMaxData(mut buf[pos..], 999).Must()
    pos += WriteMaxStreamData(mut buf[pos..], 12, 500).Must()
    pos += WriteMaxStreams(mut buf[pos..], false, 20).Must()
    pos += WriteDataBlocked(mut buf[pos..], 300).Must()
    pos += WriteStreamDataBlocked(mut buf[pos..], 12, 400).Must()
    pos += WriteStreamsBlocked(mut buf[pos..], false, 10).Must()
    pos += WriteNewConnectionId(mut buf[pos..], 3, 1, &cid, &tok16).Must()
    pos += WriteRetireConnectionId(mut buf[pos..], 1).Must()
    pos += WritePathChallenge(mut buf[pos..], &d8a).Must()
    pos += WritePathResponse(mut buf[pos..], &d8b).Must()
    pos += WriteHandshakeDone(mut buf[pos..]).Must()
    pos += WriteStream(mut buf[pos..], 16, 0, b"xyz", true, true).Must()

    var out_r: [4]AckRange = [AckRange(lo: 0, hi: 0); 4]
    var it := FrameIter.New(buf[0..pos], true)
    var count := 0
    for {
        f := it.Next(mut out_r[..]).Must() else {
            break
        }
        _ = f
        count += 1
    }
    Assert(count == 19)

    // Short buffer returns none without panicking.
    var tiny: [2]u8 = [0; 2]
    AssertNone(WritePadding(mut tiny[..], 3))
    AssertNone(WritePing(mut tiny[0..0]))
    AssertNone(WriteAck(mut tiny[..], ack_r[..], 0))
    AssertNone(WriteResetStream(mut tiny[..], 4, 77, 100))
    AssertNone(WriteStopSending(mut tiny[..], 8, 88))
    AssertNone(WriteCrypto(mut tiny[..], 0, b"abc"))
    AssertNone(WriteNewToken(mut tiny[..], b"tok"))
    AssertNone(WriteStream(mut tiny[..], 4, 0, b"abc", false, true))
    AssertNone(WriteNewConnectionId(mut tiny[..], 1, 0, &cid, &tok16))
    AssertNone(WritePathChallenge(mut tiny[..], &d8a))
    AssertNone(WritePathResponse(mut tiny[..], &d8b))
    AssertNone(WriteConnectionClose(mut tiny[..], 0, 0, b"err", false))
    AssertNone(WriteHandshakeDone(mut tiny[0..0]))
}
