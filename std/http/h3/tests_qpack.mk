// Why: QPACK encoder/decoder stream rules (RFC 9204 4.2) at both capacity 0
// and capacity > 0, HTTP/3 malformed-message field rules (RFC 9114 4.1.2-4.3,
// 10.3), header-size 431 handling, and concurrent Serve + bounded-pipe
// streaming must all be pinned by exact tests.
//
// What: tests for QPACK dynamic-table blocking, section acknowledgments, and
// concurrent Session.Serve over a 64-byte bounded PipeMux.
//
// How: encodes raw QPACK sections and encoder/decoder instructions over
// std.net.pipe's MuxPair and verifies exact Violation codes, 431 survival,
// QPACK unblocking under Serve, and multi-stream body integrity.

import hf = std.http.fields
import std.net.pipe
import std.time

// Helper: sends a raw HEADERS request and asserts server RequestHead fails with stream H3_MESSAGE_ERROR.
AssertBadRawRequest(sys: &System, pairs: [](name: str, value: str)): Error!void {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    b := (try cm.Open(sys.io, false, true)).Must()
    defer cm.Release(b)
    try WriteRawHeaders(sys.io, &cm, &b, pairs, true)

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    AssertReqViolation(&sex, sys.io, mut fb, H3MessageError, false)
}

test "RFC 9114 4.2: an uppercase field name is a stream H3_MESSAGE_ERROR" (sys: &System) {
    var p: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
        (name: "X-Upper", value: "1"),
    ]
    try AssertBadRawRequest(sys, p[..])
}

test "RFC 9114 4.2: a connection-specific field is a stream H3_MESSAGE_ERROR" (sys: &System) {
    for cname in ["connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"] {
        var p: [5](name: str, value: str) = [
            (name: ":method", value: "GET"),
            (name: ":scheme", value: "https"),
            (name: ":authority", value: "example.com"),
            (name: ":path", value: "/"),
            (name: cname, value: "x"),
        ]
        try AssertBadRawRequest(sys, p[..])
    }
}

test "RFC 9114 4.2: te other than trailers is a stream H3_MESSAGE_ERROR" (sys: &System) {
    var p_req: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
        (name: "te", value: "gzip"),
    ]
    try AssertBadRawRequest(sys, p_req[..])

    // Valid te: trailers in request succeeds; te: trailers in response fails.
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
    var resp: [2](name: str, value: str) = [(name: ":status", value: "200"), (name: "te", value: "trailers")]
    try WriteRawHeaders(sys.io, &sm, &srv_s, resp[..], true)
    AssertHeadViolation(&cex, sys.arena, sys.io, H3MessageError, false)
}

test "RFC 9114 4.3: pseudo-header ordering, validity, and response :status rules are stream H3_MESSAGE_ERROR" (sys: &System) {
    // 1) Pseudo-header in wrong message (request pseudo in response).
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
    var bad_resp: [2](name: str, value: str) = [(name: ":status", value: "200"), (name: ":method", value: "GET")]
    try WriteRawHeaders(sys.io, &sm, &srv_s, bad_resp[..], true)
    AssertHeadViolation(&cex, sys.arena, sys.io, H3MessageError, false)

    // 2) Undefined pseudo-header :protocol.
    var proto: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
        (name: ":protocol", value: "websocket"),
    ]
    try AssertBadRawRequest(sys, proto[..])

    // 3) Pseudo-header after regular field.
    var after: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: "x-a", value: "1"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
    ]
    try AssertBadRawRequest(sys, after[..])

    // 4) :authority with userinfo.
    var userinfo: [4](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "user@example.com"),
        (name: ":path", value: "/"),
    ]
    try AssertBadRawRequest(sys, userinfo[..])

    // 5) Empty :path.
    var empty_path: [4](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: ""),
    ]
    try AssertBadRawRequest(sys, empty_path[..])

    // 6) Repeated :method.
    var rep_m: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":method", value: "POST"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
    ]
    try AssertBadRawRequest(sys, rep_m[..])

    // 7) Missing :authority and Host, and mismatched :authority vs Host.
    var no_auth: [3](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":path", value: "/"),
    ]
    try AssertBadRawRequest(sys, no_auth[..])
    var mismatch: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "a.example"),
        (name: ":path", value: "/"),
        (name: "host", value: "b.example"),
    ]
    try AssertBadRawRequest(sys, mismatch[..])

    // 8) Response without :status.
    (client: cm2, server: sm2) := try pipe.MuxPair(sys.heap, 4096)
    defer cm2.Abandon()
    defer sm2.Abandon()
    cs2 := try Session.Client(sys.heap, sys.io, &cm2, 4096)
    defer cs2.Free(sys.heap.Alloc())
    cex2 := try Exchange.Open(cs2.Ref(), sys.io, &p, empty[..], true)
    defer cex2.Release(cs2.Ref())
    srv2 := (try sm2.Accept(sys.io, false, true)).Must()
    defer sm2.Release(srv2)
    var no_st: [1](name: str, value: str) = [(name: "server", value: "x")]
    try WriteRawHeaders(sys.io, &sm2, &srv2, no_st[..], true)
    AssertHeadViolation(&cex2, sys.arena, sys.io, H3MessageError, false)
}

test "RFC 9114 10.3: invalid bytes in field name or value are stream H3_MESSAGE_ERROR" (sys: &System) {
    var bad_name: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
        (name: "bad name", value: "1"),
    ]
    try AssertBadRawRequest(sys, bad_name[..])

    var bad_val: [5](name: str, value: str) = [
        (name: ":method", value: "GET"),
        (name: ":scheme", value: "https"),
        (name: ":authority", value: "example.com"),
        (name: ":path", value: "/"),
        (name: "x-v", value: "a\nb"),
    ]
    try AssertBadRawRequest(sys, bad_val[..])
}

test "RFC 9114 4.2.2: server RequestHead returns TooLarge keeping stream open for 431" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    // Client sends before receiving server's SETTINGS (128 byte limit).
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/big")
    var hdrs: [1]Kv = [Kv(n: "x-big", v: "0123456789abcdef0123456789abcdef0123456789abcdef")]
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, hdrs[..], true)
    defer cex.Release(cs.Ref())

    ss := try Session.Server(sys.heap, sys.io, &sm, 128)
    defer ss.Free(sys.heap.Alloc())

    sex := try Exchange.Accept(ss.Ref(), sys.io)
    defer sex.Release(ss.Ref())
    var fb := hf.FieldBlock.New(sys.heap.Alloc())
    defer fb.Free(sys.heap.Alloc())
    var got_too_large := false
    sex.RequestHead(sys.io, mut fb) else e {
        match e {
            TooLarge { got_too_large = true }
            _ { Assert(false) }
        }
    }
    Assert(got_too_large)
    var empty: [0]Kv = []
    try sex.Respond(sys.io, 431, empty[..], true)
    h := try cex.Head(sys.arena, sys.io)
    Assert(h.status == 431)
}

test "RFC 9204 4.2: a second QPACK encoder or decoder stream is a connection H3_STREAM_CREATION_ERROR" (sys: &System) {
    for stype in [StreamQpackEncoder, StreamQpackDecoder] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        u1 := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(u1)
        var b1: [4]u8 = [0; 4]
        n1 := WriteVarint(mut b1[..], stype)
        try sm.Write(sys.io, &u1, b1[0..n1], false)
        try cs.Ref().Poll(sys.io)

        u2 := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(u2)
        try sm.Write(sys.io, &u2, b1[0..n1], false)
        AssertPollFatal(cs.Ref(), sys.io, H3StreamCreationError)
    }
}

test "RFC 9204 4.2: closing a QPACK stream is a connection H3_CLOSED_CRITICAL_STREAM" (sys: &System) {
    for stype in [StreamQpackEncoder, StreamQpackDecoder] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        u := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(u)
        var b: [4]u8 = [0; 4]
        n := WriteVarint(mut b[..], stype)
        try sm.Write(sys.io, &u, b[0..n], true)
        AssertPollFatal(cs.Ref(), sys.io, H3ClosedCriticalStream)
    }
}

test "RFC 9204 4.2: a client with capacity 0 accepts the server's QPACK streams" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    qe := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(qe)
    qd := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(qd)

    // Server opens 0x02 with SetCapacity(0) (0x20) and 0x03 with StreamCancel(0) (0x40).
    var eb: [2]u8 = [0x02, 0x20]
    try sm.Write(sys.io, &qe, eb[..], false)
    var db: [2]u8 = [0x03, 0x40]
    try sm.Write(sys.io, &qd, db[..], false)
    try cs.Ref().Poll(sys.io)

    // Invalid Section Acknowledgment on 0x03 when our encoder never inserts -> QPACK_DECODER_STREAM_ERROR.
    var bad_dec: [1]u8 = [0x80]
    try sm.Write(sys.io, &qd, bad_dec[..], false)
    AssertPollFatal(cs.Ref(), sys.io, QpackDecoderStreamError)
}

// Helper task: runs Session.Serve until scope cancels or connection closes.
H3ServeTask(s: &Session, io: Io) {
    s.Serve(io) else {
    }
}

test "QPACK capacity > 0: dynamic table insert unblocks HEADERS and emits Section Ack on 0x03" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    // Client advertises qpack_cap = 4096, qpack_blocked = 4.
    cs := try Session.ClientWithQpack(sys.heap, sys.io, &cm, 4096, 4096, 4)
    defer cs.Free(sys.heap.Alloc())

    // Server receives client's control stream + QPACK decoder stream (0x03).
    u_ctrl := (try sm.Accept(sys.io, true, true)).Must()
    defer sm.Release(u_ctrl)
    u_qdec := (try sm.Accept(sys.io, true, true)).Must()
    defer sm.Release(u_qdec)

    var rbuf: [64]u8 = [0; 64]
    qn := (try sm.Read(sys.io, &u_qdec, mut rbuf[..], true)).Must()
    Assert(qn >= 1 && rbuf[0] == 0x03)

    // Client opens a request; server replies with a HEADERS frame referencing dynamic entry 0 (RIC=1, Base=0, index 0 = 0x80).
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/dyn")
    var empty: [0]Kv = []
    cex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    defer cex.Release(cs.Ref())

    srv_s := (try sm.Accept(sys.io, false, true)).Must()
    defer sm.Release(srv_s)
    // Drain request HEADERS.
    _ = (try sm.Read(sys.io, &srv_s, mut rbuf[..], true)).Must()

    // Write blocked response HEADERS: RIC=1 (0x02), Base=0 (0x00), Indexed dynamic abs 0 (0x80).
    var resp: [16]u8 = [0; 16]
    rn := WriteFrameHeader(mut resp[..], FrameHeaders, 3)
    resp[rn] = 0x02
    resp[rn + 1] = 0x00
    resp[rn + 2] = 0x80
    try sm.Write(sys.io, &srv_s, resp[0..rn + 3], true)

    // Open server's QPACK encoder stream (0x02); delay insert briefly while client Serve/Head runs!
    u_qenc := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(u_qenc)
    var qenc_hdr: [1]u8 = [0x02]
    try sm.Write(sys.io, &u_qenc, qenc_hdr[..], false)

    scope sc := sys.io.Scope() {
        sc.go H3ServeTask(cs.Ref(), sys.io)
        try sys.io.Sleep(time.Millis(5))
        // Encoder instruction: SetCapacity(220) -> 0x3f 0xbd 0x01, then InsertWithNameRef(static 25=:status, "200") -> 0xd9 0x03 '2' '0' '0'.
        // Even simpler: SetCapacity(64) = 0x3f 0x21, InsertWithNameRef(static 25=:status, "200") = 0xd9 0x03 0x32 0x30 0x30.
        var ins: [7]u8 = [0x3f, 0x21, 0xd9, 0x03, 0x32, 0x30, 0x30]
        try sm.Write(sys.io, &u_qenc, ins[..], false)

        h := try cex.Head(sys.arena, sys.io)
        Assert(h.status == 200)

        // Client must have sent Section Acknowledgment (0x80 | stream 0 = 0x80) on 0x03!
        an := (try sm.Read(sys.io, &u_qdec, mut rbuf[..], true)).Must()
        Assert(an >= 1 && rbuf[0] == 0x80)
        sc.Cancel()
    }
}

test "Concurrent Session.Serve and bounded 64-byte PipeMux bulk streaming" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 64)
    defer cm.Abandon()
    defer sm.Abandon()

    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    var payload: [4096]u8 = [0; 4096]
    for i in 0..4096 {
        payload[i] = Trunc[u8](i * 7 + 3)
    }

    scope sc := sys.io.Scope() {
        sc.go H3ServeTask(cs.Ref(), sys.io)
        sc.go H3ServeTask(ss.Ref(), sys.io)

        p := hf.Pseudo(method: "POST", scheme: "https", authority: "example.com", path: "/echo")
        var cl: [1]Kv = [Kv(n: "content-length", v: "4096")]
        cex := try Exchange.Open(cs.Ref(), sys.io, &p, cl[..], false)
        defer cex.Release(cs.Ref())

        sex := try Exchange.Accept(ss.Ref(), sys.io)
        defer sex.Release(ss.Ref())
        var fb := hf.FieldBlock.New(sys.heap.Alloc())
        defer fb.Free(sys.heap.Alloc())
        try sex.RequestHead(sys.io, mut fb)

        // Stream 4096 bytes client -> server across 64-byte bounded MuxPair!
        scope w1 := sys.io.Scope() {
            w1.go MuxBodyWrite(&cex, sys.io, payload[..])

            var got: [4096]u8 = [0; 4096]
            var off := 0
            for {
                n := try sex.Read(sys.io, mut got[off..])
                if n == 0 {
                    break
                }
                off += n
            }
            Assert(off == 4096)
            for i in 0..4096 {
                Assert(got[i] == payload[i])
            }

            // Server streams 4096 bytes back to client.
            try sex.Respond(sys.io, 200, cl[..], false)
            scope w2 := sys.io.Scope() {
                w2.go MuxBodyWrite(&sex, sys.io, got[..])
                h := try cex.Head(sys.arena, sys.io)
                Assert(h.status == 200 && h.content_length.Must() == 4096)
                var back: [4096]u8 = [0; 4096]
                var boff := 0
                for {
                    n := try cex.Read(sys.io, mut back[boff..])
                    if n == 0 {
                        break
                    }
                    boff += n
                }
                Assert(boff == 4096)
                for i in 0..4096 {
                    Assert(back[i] == payload[i])
                }
            }
        }
        sc.Cancel()
    }
}

MuxBodyWrite(ex: &Exchange, io: Io, data: []u8) {
    ex.Write(io, data, true) else {
    }
}
