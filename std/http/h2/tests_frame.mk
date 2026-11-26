// Why: every per-frame parse rule and writer of RFC 9113 4, 6 and 7 must be
// pinned by exact positive and negative tests before connection state builds on
// them.
//
// What: tests for ParseHeader, Parse, Preface, and all Write* frame writers.
//
// How: constructs raw frame buffers in test arenas and checks exact Frame
// variants, Violation(code, stream), fields.TooSmall(need), and byte-level
// invariants (no PADDED flag, zeroed unused flags and reserved bits).

import std.core

// TestFrameBuild encodes a 9-byte header and payload p into a new slice.
TestFrameBuild(a: &Arena, kind: u8, flags: u8, stream: u32, p: []u8): OutOfMemory![]u8 from a {
    out := try a.Slice[u8](9 + p.Len())
    len := p.Len()
    out[0] = Trunc[u8](len >> 16)
    out[1] = Trunc[u8](len >> 8)
    out[2] = Trunc[u8](len)
    out[3] = kind
    out[4] = flags
    out[5] = Trunc[u8](stream >> 24)
    out[6] = Trunc[u8](stream >> 16)
    out[7] = Trunc[u8](stream >> 8)
    out[8] = Trunc[u8](stream)
    (mut out[9..]).CopyFrom(p)
    return out[..]
}

// TestFrameParse builds and parses one frame with max_frame.
TestFrameParse(a: &Arena, kind: u8, flags: u8, stream: u32, p: []u8, max_frame: int): Violation|OutOfMemory!Frame from a {
    raw := try TestFrameBuild(a, kind, flags, stream, p)
    h := ParseHeader(raw).Must()
    return try Parse(&h, raw, max_frame)
}

// TestExpectViolation checks that Parse fails with Violation(code, stream).
TestExpectViolation(a: &Arena, kind: u8, flags: u8, stream: u32, p: []u8, max_frame: int, want_code: u32, want_stream: u32): OutOfMemory!void {
    raw := try TestFrameBuild(a, kind, flags, stream, p)
    h := ParseHeader(raw).Must()
    match Parse(&h, raw, max_frame) {
        ok(_) { Assert(false) }
        err(e) {
            Assert(e.code == want_code && e.stream == want_stream)
        }
    }
}

test "RFC 9113 4.1: unused flags and the reserved bit are ignored" (sys: &System) {
    a := sys.arena
    // ParseHeader returns none under 9 bytes.
    var short: [8]u8 = [0; 8]
    Assert(ParseHeader(short[..]).IsNone())
    // Reserved bit set in header stream id and in WINDOW_UPDATE / GOAWAY / PUSH_PROMISE.
    wu: [4]u8 = [0x80, 0x00, 0x04, 0x00]
    f := try TestFrameParse(a, TypeWindowUpdate, 0xFF, 0x8000_0003, wu[..], 16384)
    match f {
        .WindowUpdate(stream, increment) {
            Assert(stream == 3 && increment == 1024)
        }
        _ { Assert(false) }
    }
    // Unused flags on DATA (0xF6 unused bits) ignored, only END_STREAM/PADDED count.
    payload := "hi".AsBytes()
    fd := try TestFrameParse(a, TypeData, 0xF6, 0x8000_0001, payload, 16384)
    match fd {
        .Data(stream, data, padded, end) {
            Assert(stream == 1 && core.BytesEqual(data, 0, payload) && padded == 0 && !end)
        }
        _ { Assert(false) }
    }
    // Reserved bit in PUSH_PROMISE promised id (0xF7: unused bits + END_HEADERS, no PADDED).
    pp: [6]u8 = [0x80, 0x00, 0x00, 0x02, 0xAA, 0xBB]
    fpp := try TestFrameParse(a, TypePushPromise, 0xF7, 1, pp[..], 16384)
    match fpp {
        .PushPromise(stream, promised, block, end_headers) {
            Assert(stream == 1 && promised == 2 && block.Len() == 2 && end_headers)
        }
        _ { Assert(false) }
    }
}

test "RFC 9113 4.2: a 16384-byte payload is accepted" (sys: &System) {
    a := sys.arena
    big := try a.Slice[u8](16384)
    big[0] = 0x42
    big[16383] = 0x99
    f := try TestFrameParse(a, TypeData, 0x01, 5, big[..], 16384)
    match f {
        .Data(stream, data, padded, end) {
            Assert(stream == 5 && data.Len() == 16384 && data[0] == 0x42 && data[16383] == 0x99 && padded == 0 && end)
        }
        _ { Assert(false) }
    }
}

test "RFC 9113 4.2: a frame over SETTINGS_MAX_FRAME_SIZE is a connection FRAME_SIZE_ERROR" (sys: &System) {
    a := sys.arena
    big := try a.Slice[u8](16385)
    try TestExpectViolation(a, TypeData, 0, 1, big[..], 16384, FrameSizeError, 0)
    // Also a mismatched buffer length vs h.length fails cleanly without panicking.
    raw := try TestFrameBuild(a, TypeData, 0, 1, "abc".AsBytes())
    h := Header(length: 4, kind: TypeData, flags: 0, stream: 1)
    match Parse(&h, raw, 16384) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.code == FrameSizeError && e.stream == 0) }
    }
}

test "RFC 9113 4.2: a frame too short for its fields is a connection FRAME_SIZE_ERROR" (sys: &System) {
    a := sys.arena
    empty: [0]u8 = []
    four: [4]u8 = [0; 4]
    five: [5]u8 = [0; 5]
    three: [3]u8 = [0; 3]
    seven: [7]u8 = [0; 7]
    // PADDED DATA with length 0.
    try TestExpectViolation(a, TypeData, 0x08, 1, empty[..], 16384, FrameSizeError, 0)
    // PADDED HEADERS with length 0.
    try TestExpectViolation(a, TypeHeaders, 0x08, 1, empty[..], 16384, FrameSizeError, 0)
    // HEADERS with PRIORITY under 5 bytes, and PADDED+PRIORITY under 6 bytes.
    try TestExpectViolation(a, TypeHeaders, 0x20, 1, four[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypeHeaders, 0x28, 1, five[..], 16384, FrameSizeError, 0)
    // PUSH_PROMISE under 4 bytes, and PADDED under 5 bytes.
    try TestExpectViolation(a, TypePushPromise, 0x00, 1, three[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypePushPromise, 0x08, 1, empty[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypePushPromise, 0x08, 1, four[..], 16384, FrameSizeError, 0)
    // GOAWAY under 8 bytes.
    try TestExpectViolation(a, TypeGoaway, 0x00, 0, seven[..], 16384, FrameSizeError, 0)
}

test "RFC 9113 5.5: an unknown frame type is discarded on any stream" (sys: &System) {
    a := sys.arena
    p: [4]u8 = [1, 2, 3, 4]
    f0 := try TestFrameParse(a, 0x42, 0xFF, 0, p[..], 16384)
    match f0 {
        .Unknown(kind, stream) { Assert(kind == 0x42 && stream == 0) }
        _ { Assert(false) }
    }
    f99 := try TestFrameParse(a, 0xFE, 0, 99, p[..], 16384)
    match f99 {
        .Unknown(kind, stream) { Assert(kind == 0xFE && stream == 99) }
        _ { Assert(false) }
    }
}

test "RFC 9113 6: a stream-0 DATA, HEADERS, PRIORITY, RST_STREAM, PUSH_PROMISE or CONTINUATION is a connection PROTOCOL_ERROR" (sys: &System) {
    a := sys.arena
    p4: [4]u8 = [0; 4]
    p5: [5]u8 = [0; 5]
    try TestExpectViolation(a, TypeData, 0, 0, p4[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypeHeaders, 0x04, 0, p4[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypePriority, 0, 0, p5[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypeRstStream, 0, 0, p4[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypePushPromise, 0x04, 0, p4[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypeContinuation, 0x04, 0, p4[..], 16384, ProtocolError, 0)
}

test "RFC 9113 6: SETTINGS, PING or GOAWAY on a non-zero stream is a connection PROTOCOL_ERROR" (sys: &System) {
    a := sys.arena
    empty: [0]u8 = []
    p8: [8]u8 = [0; 8]
    try TestExpectViolation(a, TypeSettings, 0, 1, empty[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypePing, 0, 1, p8[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypeGoaway, 0, 1, p8[..], 16384, ProtocolError, 0)
}

test "RFC 9113 6.1: a pad length equal to or over the payload length is a connection PROTOCOL_ERROR" (sys: &System) {
    a := sys.arena
    // DATA: Pad Length = 3 in 3-byte payload (equal to payload), and 4 in 3-byte payload.
    d_eq: [3]u8 = [3, 0, 0]
    d_gt: [3]u8 = [4, 0, 0]
    try TestExpectViolation(a, TypeData, 0x08, 1, d_eq[..], 16384, ProtocolError, 0)
    try TestExpectViolation(a, TypeData, 0x08, 1, d_gt[..], 16384, ProtocolError, 0)
    // Pad Length = 0 in 1-byte payload leaves 0 data bytes and reports padded = 1.
    d_zero: [1]u8 = [0]
    fd := try TestFrameParse(a, TypeData, 0x08, 1, d_zero[..], 16384)
    match fd {
        .Data(stream, data, padded, end) {
            Assert(stream == 1 && data.Len() == 0 && padded == 1 && !end)
        }
        _ { Assert(false) }
    }
    // Non-zero padding octets are ignored (RFC 9113 6.1 MAY).
    d_nz: [5]u8 = [2, 0x61, 0x62, 0xAA, 0xBB]
    fd2 := try TestFrameParse(a, TypeData, 0x08, 1, d_nz[..], 16384)
    match fd2 {
        .Data(stream, data, padded, end) {
            Assert(stream == 1 && core.BytesEqual(data, 0, "ab".AsBytes()) && padded == 3 && !end)
        }
        _ { Assert(false) }
    }
    // HEADERS PADDED + PRIORITY: pad longer than remaining bytes after pad+prio is PROTOCOL_ERROR.
    hp_bad: [7]u8 = [2, 0, 0, 0, 1, 16, 0]
    try TestExpectViolation(a, TypeHeaders, 0x28, 1, hp_bad[..], 16384, ProtocolError, 0)
    // PUSH_PROMISE PADDED: pad longer than remaining bytes after pad+promised is PROTOCOL_ERROR.
    pp_bad: [6]u8 = [2, 0, 0, 0, 2, 0]
    try TestExpectViolation(a, TypePushPromise, 0x08, 1, pp_bad[..], 16384, ProtocolError, 0)
}

test "RFC 9113 6.1: written DATA and HEADERS carry no PADDED flag" {
    var out: [64]u8 = [0; 64]
    n1 := try WriteData(mut out[..], 0x8000_0001, "hello".AsBytes(), true)
    h1 := ParseHeader(out[0..n1]).Must()
    Assert(h1.kind == TypeData && (h1.flags & 0x08) == 0 && h1.flags == 0x01 && (out[5] & 0x80) == 0)
    n2 := try WriteHeadersHead(mut out[..], 0x8000_0003, 12, true, false)
    h2 := ParseHeader(out[0..n2]).Must()
    Assert(h2.kind == TypeHeaders && (h2.flags & 0x08) == 0 && h2.flags == 0x04 && (out[5] & 0x80) == 0)
}

test "RFC 9113 6.2: HEADERS with PRIORITY and PADDED parses its fragment" (sys: &System) {
    a := sys.arena
    // Pad Length = 2, 5 priority bytes, 3 fragment bytes (0x82, 0x86, 0x84), 2 pad bytes.
    p: [11]u8 = [2, 0x80, 0, 0, 0, 15, 0x82, 0x86, 0x84, 0, 0]
    f := try TestFrameParse(a, TypeHeaders, 0x2D, 7, p[..], 16384)
    match f {
        .Headers(stream, block, padded, end_headers, end_stream) {
            want: [3]u8 = [0x82, 0x86, 0x84]
            Assert(stream == 7 && core.BytesEqual(block, 0, want[..]) && padded == 3 && end_headers && end_stream)
        }
        _ { Assert(false) }
    }
}

test "RFC 9113 6.3: a PRIORITY of length other than 5 is a stream FRAME_SIZE_ERROR" (sys: &System) {
    a := sys.arena
    p4: [4]u8 = [0; 4]
    p6: [6]u8 = [0; 6]
    try TestExpectViolation(a, TypePriority, 0, 9, p4[..], 16384, FrameSizeError, 9)
    try TestExpectViolation(a, TypePriority, 0, 9, p6[..], 16384, FrameSizeError, 9)
    p5: [5]u8 = [0; 5]
    f := try TestFrameParse(a, TypePriority, 0, 9, p5[..], 16384)
    match f {
        .Priority(stream) { Assert(stream == 9) }
        _ { Assert(false) }
    }
}

test "RFC 9113 6: fixed-length RST_STREAM, PING, WINDOW_UPDATE of the wrong length are a connection FRAME_SIZE_ERROR" (sys: &System) {
    a := sys.arena
    p3: [3]u8 = [0; 3]
    p5: [5]u8 = [0; 5]
    p7: [7]u8 = [0; 7]
    p9: [9]u8 = [0; 9]
    try TestExpectViolation(a, TypeRstStream, 0, 1, p3[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypeRstStream, 0, 1, p5[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypePing, 0, 0, p7[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypePing, 0, 0, p9[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypeWindowUpdate, 0, 0, p3[..], 16384, FrameSizeError, 0)
    try TestExpectViolation(a, TypeWindowUpdate, 0, 1, p5[..], 16384, FrameSizeError, 0)
}

test "RFC 9113 6.8: GOAWAY debug data is parsed and dropped" (sys: &System) {
    a := sys.arena
    // Last stream = 13, code = ProtocolError (1), debug = "secret".
    p: [14]u8 = [0x80, 0, 0, 13, 0, 0, 0, 1, b's', b'e', b'c', b'r', b'e', b't']
    f := try TestFrameParse(a, TypeGoaway, 0, 0, p[..], 16384)
    match f {
        .Goaway(last, code, debug) {
            Assert(last == 13 && code == ProtocolError && core.BytesEqual(debug, 0, "secret".AsBytes()))
        }
        _ { Assert(false) }
    }
}

test "RFC 9113 7: an unknown error code reads as INTERNAL_ERROR" (sys: &System) {
    a := sys.arena
    // Code 0xFF in RST_STREAM and 0x0E in GOAWAY both map to InternalError (0x02).
    rst: [4]u8 = [0, 0, 0, 0xFF]
    fr := try TestFrameParse(a, TypeRstStream, 0, 3, rst[..], 16384)
    match fr {
        .RstStream(stream, code) { Assert(stream == 3 && code == InternalError) }
        _ { Assert(false) }
    }
    goa: [8]u8 = [0, 0, 0, 5, 0, 0, 0, 0x0E]
    fg := try TestFrameParse(a, TypeGoaway, 0, 0, goa[..], 16384)
    match fg {
        .Goaway(last, code, debug) { Assert(last == 5 && code == InternalError && debug.Len() == 0) }
        _ { Assert(false) }
    }
}

test "frame writers round-trip and fail with TooSmall without mutating out" {
    // Preface matches RFC 9113 3.4 exact 24 octets.
    Assert(core.BytesEqual(Preface[..], 0, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n".AsBytes()))
    var buf: [64]u8 = [0xAA; 64]
    // Short buffers fail with TooSmall and leave buf untouched.
    match WriteData(mut buf[0..11], 1, "abc".AsBytes(), false) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 12 && buf[0] == 0xAA && buf[10] == 0xAA) }
    }
    match WriteHeadersHead(mut buf[0..8], 1, 5, true, true) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 9 && buf[0] == 0xAA) }
    }
    match WriteContinuationHead(mut buf[0..8], 1, 5, true) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 9 && buf[0] == 0xAA) }
    }
    match WriteRstStream(mut buf[0..12], 1, Cancel) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 13 && buf[0] == 0xAA) }
    }
    match WriteSettingsAck(mut buf[0..8]) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 9 && buf[0] == 0xAA) }
    }
    ping_data: [8]u8 = [1, 2, 3, 4, 5, 6, 7, 8]
    match WritePing(mut buf[0..16], &ping_data, true) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 17 && buf[0] == 0xAA) }
    }
    match WriteGoaway(mut buf[0..16], 7, NoError) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 17 && buf[0] == 0xAA) }
    }
    match WriteWindowUpdate(mut buf[0..12], 0, 4096) {
        ok(_) { Assert(false) }
        err(e) { Assert(e.need == 13 && buf[0] == 0xAA) }
    }
    // Round-trips.
    n := try WriteRstStream(mut buf[..], 5, RefusedStream)
    hr := ParseHeader(buf[0..n]).Must()
    match try Parse(&hr, buf[0..n], 16384) {
        .RstStream(stream, code) { Assert(stream == 5 && code == RefusedStream) }
        _ { Assert(false) }
    }
    np := try WritePing(mut buf[..], &ping_data, true)
    hp := ParseHeader(buf[0..np]).Must()
    match try Parse(&hp, buf[0..np], 16384) {
        .Ping(ack, data) { Assert(ack && core.BytesEqual(data[..], 0, ping_data[..])) }
        _ { Assert(false) }
    }
    ng := try WriteGoaway(mut buf[..], 11, EnhanceYourCalm)
    hg := ParseHeader(buf[0..ng]).Must()
    match try Parse(&hg, buf[0..ng], 16384) {
        .Goaway(last, code, debug) { Assert(last == 11 && code == EnhanceYourCalm && debug.Len() == 0) }
        _ { Assert(false) }
    }
    nw := try WriteWindowUpdate(mut buf[..], 0, 65536)
    hw := ParseHeader(buf[0..nw]).Must()
    match try Parse(&hw, buf[0..nw], 16384) {
        .WindowUpdate(stream, increment) { Assert(stream == 0 && increment == 65536) }
        _ { Assert(false) }
    }
    na := try WriteSettingsAck(mut buf[..])
    ha := ParseHeader(buf[0..na]).Must()
    match try Parse(&ha, buf[0..na], 16384) {
        .Settings(ack, entries) { Assert(ack && entries.Len() == 0) }
        _ { Assert(false) }
    }
}
