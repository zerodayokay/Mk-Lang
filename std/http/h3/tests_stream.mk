// Why: every HTTP/3 request-stream framing rule, stream-vs-connection error
// mapping, unidirectional stream dispatch rule, and cancellation rule of RFC
// 9114 4.1-4.6, 6.1-6.2, 7.2 and 8 must be pinned by exact tests.
//
// What: test blocks covering H3-11..H3-16, H3-19, H3-20, H3-24, H3-27, H3-28,
// H3-50, H3-54, H3-57, H3-66, H3-70, H3-75, H3-77, H3-78, H3-82, H3-85,
// H3-87..H3-89.
//
// How: drives client/server Exchanges and raw Mux streams over std.net.pipe's
// MuxPair and checks exact Violation codes, StreamReset codes, and connection
// survival after stream errors.

import hf = std.http.fields
import std.net
import std.net.pipe

// Helper: encodes a static-only QPACK section from name/value pairs into out.
EncodeRawSection(out: mut []u8, pairs: [](name: str, value: str)): int {
    qenc := hf.QEncoder.New()
    var o := qenc.Start(out).Must()
    for p in pairs {
        o += qenc.Write(mut out[o..], p.name.AsBytes(), p.value.AsBytes(), false).Must()
    }
    return o
}

// Helper: writes a HEADERS frame with the given QPACK pairs to a raw MuxStream.
WriteRawHeaders(io: Io, m: &net.Mux, s: &net.MuxStream, pairs: [](name: str, value: str), fin: bool): net.Error!void {
    var sec: [512]u8 = [0; 512]
    slen := EncodeRawSection(mut sec[16..], pairs)
    hlen := 1 + VarintLen(Cast[u64](slen))
    start := 16 - hlen
    _ = WriteFrameHeader(mut sec[start..16], FrameHeaders, Cast[u64](slen))
    try m.Write(io, s, sec[start..16 + slen], fin)
}

// Helper: asserts RequestHead fails with Violation(want_code, want_stream).
AssertReqViolation(ex: &Exchange, io: Io, block: mut hf.FieldBlock, want_code: u64, want_is_conn: bool) {
    var code: u64 = 0
    var stream: i64 = -99
    ex.RequestHead(io, block) else e {
        match e {
            Violation(code: c, stream: s) {
                code = c
                stream = s
            }
            _ { Assert(false) }
        }
    }
    Assert(code == want_code)
    if want_is_conn {
        Assert(stream == -1)
    } else {
        Assert(stream >= 0)
    }
}

// Helper: asserts Head fails with Violation(want_code, want_is_conn).
AssertHeadViolation(ex: &Exchange, a: &Arena, io: Io, want_code: u64, want_is_conn: bool) {
    var code: u64 = 0
    var stream: i64 = -99
    match ex.Head(a, io) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                Violation(code: c, stream: s) {
                    code = c
                    stream = s
                }
                _ { Assert(false) }
            }
        }
    }
    Assert(code == want_code)
    if want_is_conn {
        Assert(stream == -1)
    } else {
        Assert(stream >= 0)
    }
}

test "RFC 9114 4.1: the client sends one request per stream and closes its send side after it" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "POST", scheme: "https", authority: "example.com", path: "/up")
    var hdrs: [1]Kv = [Kv(n: "content-length", v: "5")]
    ex := try Exchange.Open(cs.Ref(), sys.io, &p, hdrs[..], false)
    defer ex.Release(cs.Ref())
    try ex.Write(sys.io, "hello".AsBytes(), true)

    // Subsequent Write after FIN must fail.
    var post_fin_err := false
    ex.Write(sys.io, "!".AsBytes(), true) else {
        post_fin_err = true
    }
    Assert(post_fin_err)

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try sex.RequestHead(sys.io, mut fb)
    var buf: [16]u8 = [0; 16]
    n := try sex.Read(sys.io, mut buf[..])
    Assert(n == 5 && buf[0] == b'h' && buf[4] == b'o')
    Assert((try sex.Read(sys.io, mut buf[..])) == 0)
}

test "RFC 9114 4.1: the server closes its send side after the final response" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try sex.RequestHead(sys.io, mut fb)
    var rhdrs: [1]Kv = [Kv(n: "x-test", v: "ok")]
    try sex.Respond(sys.io, 200, rhdrs[..], false)
    try sex.Write(sys.io, "pong".AsBytes(), true)

    h := try cex.Head(sys.arena, sys.io)
    Assert(h.status == 200 && h.headers.Len() == 1 && h.headers[0].name == "x-test" && h.headers[0].value == "ok")
    var rbuf: [16]u8 = [0; 16]
    Assert((try cex.Read(sys.io, mut rbuf[..])) == 4)
    Assert((try cex.Read(sys.io, mut rbuf[..])) == 0)
}

test "RFC 9114 4.1: a second response after the final response is a stream H3_MESSAGE_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    srv_s := (try sm.Accept(sys.io, false, true)).Must()
    defer sm.Release(srv_s)
    var h1: [1](name: str, value: str) = [(name: ":status", value: "200")]
    try WriteRawHeaders(sys.io, &sm, &srv_s, h1[..], false)
    // Second HEADERS containing :status is treated as invalid trailers (H3_MESSAGE_ERROR).
    var h2: [1](name: str, value: str) = [(name: ":status", value: "200")]
    try WriteRawHeaders(sys.io, &sm, &srv_s, h2[..], true)

    h := try cex.Head(sys.arena, sys.io)
    Assert(h.status == 200)
    var buf: [8]u8 = [0; 8]
    var code: u64 = 0
    _ = cex.Read(sys.io, mut buf[..]) else e {
        match e {
            Violation(code: c, stream: strm) {
                Assert(strm >= 0)
                code = c
            }
            _ { Assert(false) }
        }
        0
    }
    Assert(code == H3MessageError)
}

test "RFC 9114 4.1: DATA before any HEADERS is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    var d: [4]u8 = [0; 4]
    n := WriteFrameHeader(mut d[..], FrameData, 1)
    d[n] = 0x41
    try cm.Write(sys.io, &b, d[0..n + 1], true)

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    AssertReqViolation(&sex, sys.io, mut fb, H3FrameUnexpected, true)
}

test "RFC 9114 4.1: HEADERS or DATA after the trailing HEADERS is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    for bad in [FrameData, FrameHeaders] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
        var empty: [0]Kv = []
        cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
        defer cex.Release(cs.Ref())

        srv_s := (try sm.Accept(sys.io, false, true)).Must()
        defer sm.Release(srv_s)
        var h1: [1](name: str, value: str) = [(name: ":status", value: "200")]
        try WriteRawHeaders(sys.io, &sm, &srv_s, h1[..], false)
        var tr: [1](name: str, value: str) = [(name: "x-digest", value: "123")]
        try WriteRawHeaders(sys.io, &sm, &srv_s, tr[..], false)
        var f: [8]u8 = [0; 8]
        fn_ := WriteFrameHeader(mut f[..], bad, 0)
        try sm.Write(sys.io, &srv_s, f[0..fn_], true)

        _ = try cex.Head(sys.arena, sys.io)
        var buf: [8]u8 = [0; 8]
        var code: u64 = 0
        _ = cex.Read(sys.io, mut buf[..]) else e {
            match e {
                Violation(code: c, stream: strm) {
                    Assert(strm == -1)
                    code = c
                }
                _ { Assert(false) }
            }
            0
        }
        Assert(code == H3FrameUnexpected)
    }
}

test "RFC 9114 4.1: 1xx responses before the final are consumed; FIN after a 1xx is a stream H3_MESSAGE_ERROR" (sys: &System) {
    // Positive: 103 + 100 + 200.
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try sex.RequestHead(sys.io, mut fb)
    var link_h: [1]Kv = [Kv(n: "link", v: "</a.css>; rel=preload")]
    try sex.Respond(sys.io, 103, link_h[..], false)
    try sex.Respond(sys.io, 100, empty[..], false)
    try sex.Respond(sys.io, 200, empty[..], true)

    h := try cex.Head(sys.arena, sys.io)
    Assert(h.status == 200 && h.headers.Len() == 0)

    // Negative: FIN after 1xx -> stream H3_MESSAGE_ERROR.
    (client: cm2, server: sm2) := try pipe.MuxPair(sys.heap, 4096)
    defer cm2.Abandon()
    defer sm2.Abandon()
    cs2 := try Session.Client(sys.heap, sys.io, &cm2, 4096)
    defer cs2.Free(sys.heap.Alloc())
    cex2 := try Exchange.Open(cs2.Ref(), sys.io, &p, empty[..], true)
    defer cex2.Release(cs2.Ref())
    srv2 := (try sm2.Accept(sys.io, false, true)).Must()
    defer sm2.Release(srv2)
    var i103: [1](name: str, value: str) = [(name: ":status", value: "103")]
    try WriteRawHeaders(sys.io, &sm2, &srv2, i103[..], true)
    AssertHeadViolation(&cex2, sys.arena, sys.io, H3MessageError, false)

    // Negative: DATA after 1xx -> stream H3_MESSAGE_ERROR.
    (client: cm3, server: sm3) := try pipe.MuxPair(sys.heap, 4096)
    defer cm3.Abandon()
    defer sm3.Abandon()
    cs3 := try Session.Client(sys.heap, sys.io, &cm3, 4096)
    defer cs3.Free(sys.heap.Alloc())
    cex3 := try Exchange.Open(cs3.Ref(), sys.io, &p, empty[..], true)
    defer cex3.Release(cs3.Ref())
    srv3 := (try sm3.Accept(sys.io, false, true)).Must()
    defer sm3.Release(srv3)
    try WriteRawHeaders(sys.io, &sm3, &srv3, i103[..], false)
    var df: [8]u8 = [0; 8]
    dn := WriteFrameHeader(mut df[..], FrameData, 1)
    df[dn] = b'x'
    try sm3.Write(sys.io, &srv3, df[0..dn + 1], true)
    AssertHeadViolation(&cex3, sys.arena, sys.io, H3MessageError, false)
}

test "RFC 9114 4.1: a request stream that ends before its HEADERS is a stream H3_REQUEST_INCOMPLETE" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    var empty: [0]u8 = []
    try cm.Write(sys.io, &b, empty[..], true)

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    AssertReqViolation(&sex, sys.io, mut fb, H3RequestIncomplete, false)
}

test "RFC 9114 4.1.1: a cancelled request sends RESET_STREAM and STOP_SENDING with H3_REQUEST_CANCELLED" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "POST", scheme: "https", authority: "example.com", path: "/slow")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], false)
    defer cex.Release(cs.Ref())

    srv_s := (try sm.Accept(sys.io, false, true)).Must()
    defer sm.Release(srv_s)
    // Drain request HEADERS first.
    var rbuf: [64]u8 = [0; 64]
    _ = (try sm.Read(sys.io, &srv_s, mut rbuf[..], true)).Must()

    cex.Cancel()

    var rd_code: u64 = 0
    _ = sm.Read(sys.io, &srv_s, mut rbuf[..], true) else e {
        match e {
            net.StreamReset(code) { rd_code = code }
            _ { Assert(false) }
        }
        none
    }
    Assert(rd_code == H3RequestCancelled)

    var wr_code: u64 = 0
    sm.Write(sys.io, &srv_s, "nope".AsBytes(), false) else e {
        match e {
            net.StreamReset(code) { wr_code = code }
            _ { Assert(false) }
        }
    }
    Assert(wr_code == H3RequestCancelled)
}

test "RFC 9114 4.1.2: Content-Length unequal to the DATA total is a stream H3_MESSAGE_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    // 1) Too few bytes (Content-Length: 5, DATA: 3 bytes + FIN).
    p := hf.Pseudo(method: "POST", scheme: "https", authority: "example.com", path: "/cl")
    var cl5: [1]Kv = [Kv(n: "content-length", v: "5")]
    c1 := try Exchange.Open(cs.Ref(), sys.io, &p, cl5[..], false)
    defer c1.Release(cs.Ref())
    try c1.Write(sys.io, "abc".AsBytes(), true)

    s1 := try Exchange.Accept(ss.Ref(), sys.io)
    onerr s1.Release(ss.Ref())
    defer s1.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try s1.RequestHead(sys.io, mut fb)
    var buf: [16]u8 = [0; 16]
    Assert((try s1.Read(sys.io, mut buf[..])) == 3)
    var e1: u64 = 0
    _ = s1.Read(sys.io, mut buf[..]) else e {
        match e {
            Violation(code, stream) {
                Assert(stream >= 0)
                e1 = code
            }
            _ { Assert(false) }
        }
        0
    }
    Assert(e1 == H3MessageError)

    // 2) Too many bytes (Content-Length: 2, DATA: 4 bytes).
    var cl2: [1]Kv = [Kv(n: "content-length", v: "2")]
    c2 := try Exchange.Open(cs.Ref(), sys.io, &p, cl2[..], false)
    defer c2.Release(cs.Ref())
    try c2.Write(sys.io, "abcd".AsBytes(), true)

    s2 := try Exchange.Accept(ss.Ref(), sys.io)
    defer s2.Release(ss.Ref())
    fb.Reset()
    try s2.RequestHead(sys.io, mut fb)
    var e2: u64 = 0
    _ = s2.Read(sys.io, mut buf[..]) else e {
        match e {
            Violation(code, stream) {
                Assert(stream >= 0)
                e2 = code
            }
            _ { Assert(false) }
        }
        0
    }
    Assert(e2 == H3MessageError)

    // 3) HEAD response with Content-Length: 100 and 0 DATA bytes is exempt.
    ph := hf.Pseudo(method: "HEAD", scheme: "https", authority: "example.com", path: "/head")
    var empty: [0]Kv = []
    c3 := try Exchange.Open(cs.Ref(), sys.io, &ph, empty[..], true)
    defer c3.Release(cs.Ref())
    s3 := try Exchange.Accept(ss.Ref(), sys.io)
    defer s3.Release(ss.Ref())
    fb.Reset()
    try s3.RequestHead(sys.io, mut fb)
    var cl100: [1]Kv = [Kv(n: "content-length", v: "100")]
    try s3.Respond(sys.io, 200, cl100[..], true)
    h3 := try c3.Head(sys.arena, sys.io)
    Assert(h3.status == 200 && h3.content_length.Must() == 100)
    Assert((try c3.Read(sys.io, mut buf[..])) == 0)
}

test "RFC 9114 7.2: CANCEL_PUSH, SETTINGS, GOAWAY or MAX_PUSH_ID on a request stream is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    for bad in [FrameCancelPush, FrameSettings, FrameGoaway, FrameMaxPushId] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())
        ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
        defer ss.Free(sys.heap.Alloc())

        b := (try cm.Open(sys.io, false, true)).Must()
        defer cm.Release(b)
        var f: [8]u8 = [0; 8]
        n := WriteFrameHeader(mut f[..], bad, 0)
        try cm.Write(sys.io, &b, f[0..n], false)

        sex := try Exchange.Accept(ss.Ref(), sys.io)
        defer sex.Release(ss.Ref())
        var fb := hf.FieldBlock.New(sys.heap.Alloc())
        defer fb.Free(sys.heap.Alloc())
        AssertReqViolation(&sex, sys.io, mut fb, H3FrameUnexpected, true)
    }
}

test "RFC 9114 8: a stream error resets both halves with its code and a connection error closes the Mux with its code" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    onerr sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    onerr ss.Free(sys.heap.Alloc())

    // Malformed request on stream 0: server resets both halves with H3MessageError, connection stays up.
    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    var bad_req: [1](name: str, value: str) = [(name: ":method", value: "GET")]
    try WriteRawHeaders(sys.io, &cm, &b, bad_req[..], false)

    s0 := try Exchange.Accept(ss.Ref(), sys.io)
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    AssertReqViolation(&s0, sys.io, mut fb, H3MessageError, false)
    s0.Release(ss.Ref())

    var tmp: [8]u8 = [0; 8]
    var rcode: u64 = 0
    _ = cm.Read(sys.io, &b, mut tmp[..], true) else e {
        match e {
            net.StreamReset(code) { rcode = code }
            _ { Assert(false) }
        }
        none
    }
    Assert(rcode == H3MessageError)

    // Connection stays up: next Exchange succeeds!
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/ok")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())
    s1 := try Exchange.Accept(ss.Ref(), sys.io)
    onerr s1.Release(ss.Ref())
    fb.Reset()
    try s1.RequestHead(sys.io, mut fb)
    try s1.Respond(sys.io, 204, empty[..], true)
    s1.Release(ss.Ref())
    h := try cex.Head(sys.arena, sys.io)
    Assert(h.status == 204)

    // Connection error propagated via Mux.Close: peer observes MuxClosed(code, app: true).
    ss.Free(sys.heap.Alloc())
    try sm.Close(sys.io, H3FrameUnexpected)
    var closed_code: u64 = 0
    _ = cs.Ref().Poll(sys.io) else e {
        match e {
            net.MuxClosed(code, app, peer) {
                Assert(app && peer)
                closed_code = code
            }
            _ { Assert(false) }
        }
    }
    Assert(closed_code == H3FrameUnexpected)
}

test "RFC 9114 6.1: a server-initiated bidirectional stream is a connection H3_STREAM_CREATION_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    sb := (try sm.Open(sys.io, false, true)).Must()
    defer sm.Release(sb)
    var code: u64 = 0
    cs.Ref().Poll(sys.io) else e {
        match e {
            Violation(code: c, stream: s) {
                Assert(s == -1)
                code = c
            }
            _ { Assert(false) }
        }
    }
    Assert(code == H3StreamCreationError)
}

test "RFC 9114 7.2.5: PUSH_PROMISE on a request stream at a client that sent no MAX_PUSH_ID is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    srv_s := (try sm.Accept(sys.io, false, true)).Must()
    defer sm.Release(srv_s)
    var f: [8]u8 = [0; 8]
    n := WriteSingleIdFrame(mut f[..], FramePushPromise, 0)
    try sm.Write(sys.io, &srv_s, f[0..n], false)

    AssertHeadViolation(&cex, sys.arena, sys.io, H3IdError, true)
}

test "RFC 9114 7.2.5: PUSH_PROMISE on a request stream at a server is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    var f: [8]u8 = [0; 8]
    n := WriteSingleIdFrame(mut f[..], FramePushPromise, 0)
    try cm.Write(sys.io, &b, f[0..n], false)

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    AssertReqViolation(&sex, sys.io, mut fb, H3FrameUnexpected, true)
}

test "RFC 9114 8: an unknown error code is handled as H3_NO_ERROR" (sys: &System) {
    Assert(NormError(0x9999) == H3NoError)
    Assert(NormError(GreaseId(3)) == H3NoError)
    Assert(NormError(H3MessageError) == H3MessageError)

    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    srv_s := (try sm.Accept(sys.io, false, true)).Must()
    defer sm.Release(srv_s)
    sm.Reset(&srv_s, GreaseId(7))
    var got_code: u64 = 999
    match cex.Head(sys.arena, sys.io) {
        ok(_) { Assert(false) }
        err(e) {
            match e {
                net.StreamReset(code) { got_code = code }
                _ { Assert(false) }
            }
        }
    }
    Assert(got_code == H3NoError)
}

test "RFC 9114 6.2: an unknown or reserved stream type gets STOP_SENDING H3_STREAM_CREATION_ERROR and the connection stays up" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    // Server opens GREASE uni stream 0x21 + payload.
    gu := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(gu)
    var gbuf: [8]u8 = [0; 8]
    gn := WriteVarint(mut gbuf[..], 0x21)
    gbuf[gn] = 0xAA
    try sm.Write(sys.io, &gu, gbuf[0..gn + 1], false)

    try cs.Ref().Poll(sys.io)
    var stop_code: u64 = 0
    sm.Write(sys.io, &gu, "more".AsBytes(), false) else e {
        match e {
            net.StreamReset(code) { stop_code = code }
            _ { Assert(false) }
        }
    }
    Assert(stop_code == H3StreamCreationError)

    // Connection stays up: request succeeds.
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/ok")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())
    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    try sex.RequestHead(sys.io, mut fb)
}

test "RFC 9114 6.2: a unidirectional stream reset before its type is ignored" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    // 1) Reset before any byte.
    u1 := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(u1)
    sm.Reset(&u1, H3NoError)
    // 2) Clean FIN before any byte.
    u2 := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(u2)
    var empty: [0]u8 = []
    try sm.Write(sys.io, &u2, empty[..], true)
    // 3) Partial 2-byte varint then reset.
    u3 := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(u3)
    var part: [1]u8 = [0x40]
    try sm.Write(sys.io, &u3, part[..], false)
    try cs.Ref().Poll(sys.io)
    sm.Reset(&u3, H3NoError)
    try cs.Ref().Poll(sys.io)
}

test "RFC 9114 4.6: a push stream at a client that sent no MAX_PUSH_ID is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    pu := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(pu)
    var b: [4]u8 = [0x01, 0x00, 0, 0]
    try sm.Write(sys.io, &pu, b[0..2], false)
    var code: u64 = 0
    cs.Ref().Poll(sys.io) else e {
        match e {
            Violation(code: c, stream: s) {
                Assert(s == -1)
                code = c
            }
            _ { Assert(false) }
        }
    }
    Assert(code == H3IdError)
}

test "RFC 9114 6.2.2: a push stream at a server is a connection H3_STREAM_CREATION_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    pu := (try cm.Open(sys.io, true, true)).Must()
    defer cm.Release(pu)
    var b: [4]u8 = [0x01, 0x00, 0, 0]
    try cm.Write(sys.io, &pu, b[0..2], false)
    var code: u64 = 0
    ss.Ref().Poll(sys.io) else e {
        match e {
            Violation(code: c, stream: s) {
                Assert(s == -1)
                code = c
            }
            _ { Assert(false) }
        }
    }
    Assert(code == H3StreamCreationError)
}

test "reading an HTTP/3 request does not consume the next stream's incoming notification" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/")
    empty: [0]Kv = []
    first := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer first.Release(cs.Ref())
    ex := try Exchange.Accept(ss.Ref(), sys.io)
    defer ex.Release(ss.Ref())
    second := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer second.Release(cs.Ref())
    a := sys.heap.Alloc()
    var head := hf.FieldBlock.New(a)
    defer head.Free(a)
    try ex.RequestHead(sys.io, mut head)
    next := (try sm.Accept(sys.io, false, false)).Must()
    defer sm.Release(next)
    Assert(next.Id() == second.Id())
}
