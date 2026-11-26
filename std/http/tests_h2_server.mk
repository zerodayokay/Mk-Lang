// Why: ServeWith, ServeOn, Response, and Link must serve HTTP/2 over TLS
// ALPN ("h2", falling back to "http/1.1" when negotiated) and cleartext h2c
// (without stalling shorter HTTP/1.0 requests), while enforcing HTTP/2 header,
// cookie, Flush, Takeover, error, cancel, and idle connection invariants.
//
// What: Unit and simulated/loopback integration tests for std.http HTTP/2
// server integration (A3a).
//
// How: Pairs ServeOn with h2.Conn.Client and scripted raw net.Conn peers over
// loopback and simulated sockets, plus a net.Secure.OfAlpn test acceptor.

import std.core
import std.http.fields
import std.http.h2
import std.net
import std.raw
import std.rt
import std.time

struct H2SrvFakeAlpn {
    choose_h2: bool
}

type H2SrvConnectFn = fn(&H2SrvFakeAlpn, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn
type H2SrvNegotiateFn = fn(&H2SrvFakeAlpn, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn

H2SrvFakeConnect(s: &H2SrvFakeAlpn, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    _ = s
    _ = h
    _ = io
    _ = host
    return conn
}

H2SrvFakeNegotiate(s: &H2SrvFakeAlpn, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    _ = h
    _ = io
    _ = host
    if !s.choose_h2 {
        for i in 0..offer.Len() {
            if offer[i] == "http/1.1" {
                return (conn: conn, chosen: i)
            }
        }
        return (conn: conn, chosen: -1)
    }
    for i in 0..offer.Len() {
        if offer[i] == "h2" {
            return (conn: conn, chosen: i)
        }
    }
    return (conn: conn, chosen: -1)
}

H2SrvFakeSecure(s: &H2SrvFakeAlpn): net.Secure from s {
    c := core.TypedCall[H2SrvConnectFn].Of[H2SrvFakeAlpn](s, H2SrvFakeConnect)
    n := core.TypedCall[H2SrvNegotiateFn].Of[H2SrvFakeAlpn](s, H2SrvFakeNegotiate)
    return net.Secure.OfAlpn[H2SrvFakeAlpn](c, n)
}

struct H2SrvField {
    name: str
    value: str
}

impl fields.Field for H2SrvField {
    Name(&self): []u8 {
        return self.name.AsBytes()
    }

    Value(&self): []u8 {
        return self.value.AsBytes()
    }

    Sensitive(&self): bool {
        return false
    }
}

struct H2SrvRec {
    seen_ver: raw.Atomic[int]
    seen_sec: raw.Atomic[int]
    link_ver: raw.Atomic[int]
    takeover_nil: raw.Atomic[int]
    cookie_ok: raw.Atomic[int]
    host_ok: raw.Atomic[int]
}

NewH2SrvRec(): H2SrvRec {
    return H2SrvRec(
        seen_ver: raw.Atomic[int].New(-1),
        seen_sec: raw.Atomic[int].New(-1),
        link_ver: raw.Atomic[int].New(-1),
        takeover_nil: raw.Atomic[int].New(0),
        cookie_ok: raw.Atomic[int].New(0),
        host_ok: raw.Atomic[int].New(0),
    )
}

VerCode(v: HttpVersion): int {
    match v {
        HttpVersion.Http10 { return 10 }
        HttpVersion.Http11 { return 11 }
        HttpVersion.Http2 { return 20 }
        HttpVersion.Http3 { return 30 }
        _ { return 99 }
    }
}

H2SrvBasicHandler(rec: &H2SrvRec, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    _ = a
    _ = io
    rec.seen_ver.Store(VerCode(res.Version()))
    rec.seen_sec.Store(if res.Secure() { 1 } else { 0 })
    rec.link_ver.Store(VerCode(link.Version()))
    if taken := link.Takeover() {
        Taken(conn: tconn, rest: _) := taken
        tconn.Abandon()
    } else {
        rec.takeover_nil.Store(1)
    }
    if cv := req.Header("cookie") {
        if cv == "a=1; b=2" {
            rec.cookie_ok.Store(1)
        }
    }
    if hv := req.Header("host") {
        if hv == "example.com" {
            rec.host_ok.Store(1)
        }
    }
    res.code = 200
    try res.Header("X-Mixed-Case", "val")
    try res.Header("Connection", "keep-alive")
    try res.Header("Transfer-Encoding", "chunked")
    try res.Header("Keep-Alive", "timeout=5")
    try res.Header("Content-Length", "999")
    try res.Body("hello-h2".AsBytes())
}

H2SrvServeTask[C: Share](io: Io, h: &Heap, l: &net.Listener, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void) {
    ServeOn(h, io, l, o, c, handle) else {}
}

H2CliDriveTask(io: Io, c: &h2.Conn) {
    c.Drive(io, time.Seconds(5)) else {}
}

H2CliWriterTask(io: Io, c: &h2.Conn) {
    c.Writer(io) else {}
}

H2TestLim(): h2.Limits {
    return h2.Limits(
        streams: 32,
        window: 65535,
        header_bytes: 16384,
        body_bytes: 1 << 20,
        write_limit: time.Seconds(5),
        close_limit: time.Seconds(2),
        idle_limit: time.Seconds(5),
    )
}

H2CloseBox(h: &Heap, io: Io, box: Box[h2.Conn]) {
    c := box.Take(h.Alloc())
    c.Close(io.NoCancel()) else {
        c.Abandon()
    }
}

H2DialClient(h: &Heap, io: Io, addr: str): ?Box[h2.Conn] from h {
    s := net.Dial(io, addr) else { return none }
    conn := s.Conn()
    return some(h2.Conn.Client(h, io, conn, H2TestLim()) else {
        conn.Abandon()
        return none
    })
}

test "ServeOn with TLS ALPN h2 serves HTTP/2, lowercases headers, joins cookies, strips hop-by-hop, and returns Takeover==none" (sys: &System) {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    alpn := H2SrvFakeAlpn(choose_h2: true)
    var o := DefaultServerOptions
    o.h2 = true
    o.secure = some(H2SrvFakeSecure(&alpn))
    o.keep_alive = time.Seconds(5)

    rec := NewH2SrvRec()
    scope srv := sys.io.Scope() {
        srv.go H2SrvServeTask(sys.io, sys.heap, &l, &o, &rec, H2SrvBasicHandler)
        cli_box := H2DialClient(sys.heap, sys.io, addr).Must()
        scope cs := sys.io.Scope() {
            cs.go H2CliDriveTask(sys.io, cli_box.Ref())
            cs.go H2CliWriterTask(sys.io, cli_box.Ref())
            p := fields.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/ok")
            var hs: [3]H2SrvField = [
                H2SrvField(name: "host", value: "EXAMPLE.COM:443"),
                H2SrvField(name: "cookie", value: "a=1"),
                H2SrvField(name: "cookie", value: "b=2"),
            ]
            s := cli_box.Ref().TryOpen(sys.io, &p, hs[..], true).Must().Must()
            var a := Arena.New(sys.heap).Must()
            defer a.Free()
            res := cli_box.Ref().Head(&a, sys.io, s).Must()
            Assert(res.status == 200)
            var seen_mixed := false
            var seen_conn := false
            var seen_te := false
            var seen_ka := false
            var cl_val := ""
            for f in res.list {
                fn_ := f.name
                fv := f.value
                if fn_ == "x-mixed-case" && fv == "val" {
                    seen_mixed = true
                }
                if fn_ == "connection" { seen_conn = true }
                if fn_ == "transfer-encoding" { seen_te = true }
                if fn_ == "keep-alive" { seen_ka = true }
                if fn_ == "content-length" { cl_val = fv }
            }
            Assert(seen_mixed && !seen_conn && !seen_te && !seen_ka && cl_val == "8")
            var buf: [64]u8 = [0; 64]
            var got := 0
            for {
                n := cli_box.Ref().Read(sys.io, s, mut buf[got..]).Must()
                if n == 0 {
                    break
                }
                got += n
            }
            Assert(str.From(buf[0..got]).Must() == "hello-h2")
            cli_box.Ref().Release(s)
            cs.Cancel()
        }
        H2CloseBox(sys.heap, sys.io, cli_box)
        srv.Cancel()
    }
    Assert(rec.seen_ver.Load() == 20)
    Assert(rec.seen_sec.Load() == 1)
    Assert(rec.link_ver.Load() == 20)
    Assert(rec.takeover_nil.Load() == 1)
    Assert(rec.cookie_ok.Load() == 1)
    Assert(rec.host_ok.Load() == 1)
}

H2SrvH1Handler(rec: &H2SrvRec, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    _ = a
    _ = io
    _ = req
    rec.seen_ver.Store(VerCode(res.Version()))
    rec.seen_sec.Store(if res.Secure() { 1 } else { 0 })
    rec.link_ver.Store(VerCode(link.Version()))
    res.code = 200
    try res.Body("h1-ok".AsBytes())
}

test "ServeOn with h2=true falls back to HTTP/1.1 when ALPN chooses http/1.1" (sys: &System) {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    alpn := H2SrvFakeAlpn(choose_h2: false)
    var o := DefaultServerOptions
    o.h2 = true
    o.secure = some(H2SrvFakeSecure(&alpn))
    rec := NewH2SrvRec()
    scope srv := sys.io.Scope() {
        srv.go H2SrvServeTask(sys.io, sys.heap, &l, &o, &rec, H2SrvH1Handler)
        c := net.Dial(sys.io, addr) else e {
            srv.Cancel()
            fail e
        }
        defer c.Close()
        req := "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n".AsBytes()
        c.WriteAll(sys.io, req).Must()
        var buf: [256]u8 = [0; 256]
        var total := 0
        for {
            n := c.Read(sys.io, mut buf[total..]) else { break }
            if n == 0 { break }
            total += n
        }
        txt := str.From(buf[0..total]).Must()
        Assert(txt.Find("h1-ok").IsSome())
        srv.Cancel()
    }
    Assert(rec.seen_ver.Load() == 11)
    Assert(rec.seen_sec.Load() == 1)
    Assert(rec.link_ver.Load() == 11)
}

test "ServeOn with h2c=true handles cleartext HTTP/2 and answers 18-byte HTTP/1.0 without stalling" (sys: &System) {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var o := DefaultServerOptions
    o.h2c = true
    o.keep_alive = time.Seconds(5)
    o.request_header = time.Seconds(5)
    rec := NewH2SrvRec()
    scope srv := sys.io.Scope() {
        srv.go H2SrvServeTask(sys.io, sys.heap, &l, &o, &rec, H2SrvH1Handler)

        // 1) 18-byte HTTP/1.0 request without closing client write side must not stall.
        c1 := net.Dial(sys.io, addr) else e {
            srv.Cancel()
            fail e
        }
        req10 := "GET / HTTP/1.0\r\n\r\n".AsBytes()
        Assert(req10.Len() == 18)
        c1.WriteAll(sys.io, req10).Must()
        var buf1: [256]u8 = [0; 256]
        var tot1 := 0
        for {
            n := c1.Read(sys.io, mut buf1[tot1..]) else { break }
            if n == 0 { break }
            tot1 += n
        }
        Assert(str.From(buf1[0..tot1]).Must().Find("h1-ok").IsSome())
        c1.Close()
        Assert(rec.seen_ver.Load() == 10)
        Assert(rec.seen_sec.Load() == 0)

        // 2) Cleartext h2c client preface upgrades to HTTP/2.
        cli_box := H2DialClient(sys.heap, sys.io, addr).Must()
        scope cs := sys.io.Scope() {
            cs.go H2CliDriveTask(sys.io, cli_box.Ref())
            cs.go H2CliWriterTask(sys.io, cli_box.Ref())
            p := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/h2c")
            var empty: [0]H2SrvField = []
            s := cli_box.Ref().TryOpen(sys.io, &p, empty[..], true).Must().Must()
            var a := Arena.New(sys.heap).Must()
            defer a.Free()
            res := cli_box.Ref().Head(&a, sys.io, s).Must()
            Assert(res.status == 200)
            var b2: [32]u8 = [0; 32]
            var got := 0
            for {
                n := cli_box.Ref().Read(sys.io, s, mut b2[got..]).Must()
                if n == 0 { break }
                got += n
            }
            Assert(str.From(b2[0..got]).Must() == "h1-ok")
            cli_box.Ref().Release(s)
            cs.Cancel()
        }
        H2CloseBox(sys.heap, sys.io, cli_box)
        Assert(rec.seen_ver.Load() == 20)
        Assert(rec.seen_sec.Load() == 0)
        srv.Cancel()
    }
}

H2SrvFlushAndErrorHandler(mode: &raw.Atomic[int], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    _ = a
    m := mode.Load()
    if req.path == "/flush" {
        res.code = 200
        try res.Body("part1-".AsBytes())
        try res.Flush(io, link)
        try res.Body("part2-".AsBytes())
        res.Writer().Str("part3") else { fail WriteError }
        try res.Flush(io, link)
        res.Writer().Str("-end") else { fail WriteError }
        return
    }
    if req.path == "/badstatus" {
        res.code = 150
        res.Flush(io, link) else e {
            match e {
                Status(code) { Assert(code == 150) }
                _ { Assert(false) }
            }
            fail e
        }
        return
    }
    if req.path == "/badcode" {
        res.code = 150
        return
    }
    if req.path == "/err" {
        if m == 1 {
            res.code = 200
            try res.Body("started".AsBytes())
            try res.Flush(io, link)
        }
        fail BadRequest(offset: 0)
    }
    if req.path == "/100" {
        res.code = 200
        try res.Body(req.body)
        return
    }
    res.code = 404
}

test "ServeOn H2 supports Flush streaming, 500 error fallback, post-flush error RST_STREAM, CONNECT 501, and Expect 100-continue" (sys: &System) {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var o := DefaultServerOptions
    o.h2c = true
    o.keep_alive = time.Seconds(5)
    mode := raw.Atomic[int].New(0)
    scope srv := sys.io.Scope() {
        srv.go H2SrvServeTask(sys.io, sys.heap, &l, &o, &mode, H2SrvFlushAndErrorHandler)
        cli_box := H2DialClient(sys.heap, sys.io, addr).Must()
        scope cs := sys.io.Scope() {
            cs.go H2CliDriveTask(sys.io, cli_box.Ref())
            cs.go H2CliWriterTask(sys.io, cli_box.Ref())
            var empty: [0]H2SrvField = []
            var a := Arena.New(sys.heap).Must()
            defer a.Free()

            // 1) Multi-chunk Flush + Writer remainder.
            p1 := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/flush")
            s1 := cli_box.Ref().TryOpen(sys.io, &p1, empty[..], true).Must().Must()
            r1 := cli_box.Ref().Head(&a, sys.io, s1).Must()
            Assert(r1.status == 200)
            var b1: [64]u8 = [0; 64]
            var g1 := 0
            for {
                n := cli_box.Ref().Read(sys.io, s1, mut b1[g1..]).Must()
                if n == 0 { break }
                g1 += n
            }
            Assert(str.From(b1[0..g1]).Must() == "part1-part2-part3-end")
            cli_box.Ref().Release(s1)

            // 2) Pre-flush handler failure maps to 500; /badstatus Status(150) maps to 150 -> 500.
            p2 := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/err")
            s2 := cli_box.Ref().TryOpen(sys.io, &p2, empty[..], true).Must().Must()
            a.Reset()
            r2 := cli_box.Ref().Head(&a, sys.io, s2).Must()
            Assert(r2.status == 500)
            cli_box.Ref().Release(s2)

            p2b := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/badcode")
            s2b := cli_box.Ref().TryOpen(sys.io, &p2b, empty[..], true).Must().Must()
            a.Reset()
            r2b := cli_box.Ref().Head(&a, sys.io, s2b).Must()
            Assert(r2b.status == 500)
            cli_box.Ref().Release(s2b)

            p2c := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/badstatus")
            s2c := cli_box.Ref().TryOpen(sys.io, &p2c, empty[..], true).Must().Must()
            a.Reset()
            _ = cli_box.Ref().Head(&a, sys.io, s2c) else {}
            cli_box.Ref().Release(s2c)

            // 3) Post-flush handler failure resets stream (Cancel).
            mode.Store(1)
            p3 := fields.Pseudo(method: "GET", scheme: "http", authority: "localhost", path: "/err")
            s3 := cli_box.Ref().TryOpen(sys.io, &p3, empty[..], true).Must().Must()
            a.Reset()
            var saw_reset := false
            var got_h3 := false
            var elist: [0]fields.FieldView = []
            r3 := cli_box.Ref().Head(&a, sys.io, s3) else {
                saw_reset = true
                (content_length: none, list: elist[..], status: 0)
            }
            if !saw_reset {
                got_h3 = true
                Assert(r3.status == 200)
            }
            if got_h3 {
                var b3: [32]u8 = [0; 32]
                for {
                    n := cli_box.Ref().Read(sys.io, s3, mut b3[..]) else {
                        saw_reset = true
                        break
                    }
                    if n == 0 { break }
                }
            }
            Assert(saw_reset)
            cli_box.Ref().Release(s3)

            // 4) Expect: 100-continue + POST body.
            p5 := fields.Pseudo(method: "POST", scheme: "http", authority: "localhost", path: "/100")
            var h5: [1]H2SrvField = [H2SrvField(name: "expect", value: "100-continue")]
            s5 := cli_box.Ref().TryOpen(sys.io, &p5, h5[..], false).Must().Must()
            cli_box.Ref().Send(sys.io, s5, "payload".AsBytes(), true).Must()
            a.Reset()
            r5 := cli_box.Ref().Head(&a, sys.io, s5).Must()
            Assert(r5.status == 200)
            var b5: [32]u8 = [0; 32]
            var g5 := 0
            for {
                n := cli_box.Ref().Read(sys.io, s5, mut b5[g5..]).Must()
                if n == 0 { break }
                g5 += n
            }
            Assert(str.From(b5[0..g5]).Must() == "payload")
            cli_box.Ref().Release(s5)
            cs.Cancel()
        }
        H2CloseBox(sys.heap, sys.io, cli_box)

        // 5) Raw scripted CONNECT -> 501 before handler.
        rc_conn := net.Dial(sys.io, addr) else e {
            srv.Cancel()
            fail e
        }
        defer rc_conn.Close()
        var enc := fields.Encoder.New(sys.heap.Alloc(), 4096, false).Must()
        defer enc.Free(sys.heap.Alloc())
        var hp_buf: [128]u8 = [0; 128]
        var ho := enc.Start(mut hp_buf[..]).Must()
        ho += enc.Write(mut hp_buf[ho..], ":method".AsBytes(), "CONNECT".AsBytes(), fields.Mode.NoIndex).Must()
        ho += enc.Write(mut hp_buf[ho..], ":authority".AsBytes(), "example.com:443".AsBytes(), fields.Mode.NoIndex).Must()
        var wire: [256]u8 = [0; 256]
        (mut wire[0..24]).CopyFrom(h2.Preface[..])
        wire[27] = 4 // SETTINGS len=0
        wire[33] = Trunc[u8]((ho >> 16) & 0xff)
        wire[34] = Trunc[u8]((ho >> 8) & 0xff)
        wire[35] = Trunc[u8](ho & 0xff)
        wire[36] = 1 // HEADERS
        wire[37] = 5 // END_STREAM | END_HEADERS
        wire[41] = 1 // stream 1
        (mut wire[42..42 + ho]).CopyFrom(hp_buf[0..ho])
        rc_conn.WriteAll(sys.io, wire[0..42 + ho]).Must()
        var reply: [256]u8 = [0; 256]
        var rn := 0
        var found_501 := false
        for !found_501 && rn < reply.Len() {
            n := rc_conn.Read(sys.io, mut reply[rn..]) else { break }
            if n == 0 { break }
            rn += n
            var pos := 0
            for pos + 9 <= rn {
                flen := (int(reply[pos]) << 16) | (int(reply[pos + 1]) << 8) | int(reply[pos + 2])
                if pos + 9 + flen > rn {
                    break
                }
                ftype := reply[pos + 3]
                fflags := reply[pos + 4]
                fstream := (int(reply[pos + 5] & 0x7f) << 24) | (int(reply[pos + 6]) << 16) | (int(reply[pos + 7]) << 8) | int(reply[pos + 8])
                if ftype == 1 && fstream == 1 && (fflags & 1) != 0 {
                    found_501 = true
                    break
                }
                pos += 9 + flen
            }
        }
        Assert(found_501)
        srv.Cancel()
    }
}

Idle1000Worker(io: Io, addr: str, ready: &Mutex[int], failed: &raw.Atomic[int], hold: &Chan[int]) {
    nc := io.NoCancel()
    c := net.Dial(io, addr) else {
        _ = failed.Add(1)
        return
    }
    defer c.Close()
    var intro: [42]u8 = [0; 42]
    // 24-byte preface + 9-byte empty SETTINGS + 9-byte SETTINGS ACK.
    (mut intro[0..24]).CopyFrom(h2.Preface[..])
    // intro[24..33]: len=0, type=4(SETTINGS), flags=0, sid=0 -> already zeroed except type.
    intro[27] = 4
    // intro[33..42]: len=0, type=4(SETTINGS), flags=1(ACK), sid=0.
    intro[36] = 4
    intro[37] = 1
    c.WriteAll(io, intro[..]) else {
        _ = failed.Add(1)
        return
    }
    var srv_set: [64]u8 = [0; 64]
    var got := 0
    for got < 9 {
        n := c.Read(io, mut srv_set[got..]) else {
            _ = failed.Add(1)
            return
        }
        if n == 0 {
            _ = failed.Add(1)
            return
        }
        got += n
    }
    var g := ready.Lock(nc).Must()
    *g.Mut() += 1
    g.Unlock()
    _ = hold.Recv(io) else {}
}

test "ServeOn holds 1000 idle HTTP/2 connections under 128 KiB each and exits cleanly on cancel" (sys: &System) {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var o := DefaultServerOptions
    o.h2c = true
    o.connections = 1100
    o.keep_alive = time.Seconds(60)
    rec := NewH2SrvRec()
    ready := Mutex[int].New(0)
    failed := raw.Atomic[int].New(0)
    var hold := try Chan[int].New(sys.heap.Alloc(), 1)
    defer hold.Free(sys.heap.Alloc())
    base_bytes := rt.LiveAllocations().bytes
    var per_conn := 0
    scope top := sys.io.Scope() {
        top.go H2SrvServeTask(sys.io, sys.heap, &l, &o, &rec, H2SrvH1Handler)
        for wave in 0..20 {
            target := (wave + 1) * 50
            for _ in 0..50 {
                top.go Idle1000Worker(sys.io, addr, &ready, &failed, &hold)
            }
            for {
                var g := ready.Lock(sys.io).Must()
                n := *g.Ref()
                g.Unlock()
                if failed.Load() > 0 || n >= target {
                    break
                }
                sys.io.Sleep(time.Millis(2)) else { break }
            }
            if failed.Load() > 0 {
                break
            }
        }
        Assert(failed.Load() == 0)
        live := rt.LiveAllocations().bytes
        delta := if live > base_bytes { live - base_bytes } else { 0 }
        per_conn = delta / 1000
        Assert(per_conn < 128 * 1024)
        hold.Close()
        top.Cancel()
    }
}
