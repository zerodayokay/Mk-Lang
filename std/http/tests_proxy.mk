// Why: a proxied client must send exact heads and never resolve the origin.
// What: absolute-form requests, CONNECT tunnels, refused tunnels, reply
// bounds, allow, bad proxy URLs and redirects, under a simulated Io.
// How: a scripted proxy checks each head byte for byte; the origin,
// origin.invalid, cannot be resolved by the Sim.

import std.core
import std.net
import std.raw
import std.time
import std.url

// What the scripted proxy answers a CONNECT with.
const PxOk = 0
const PxRefuse = 1
const PxSlow = 2
const PxBig = 3
const PxCut = 4
const PxBare = 5
const PxExtra = 6
const PxPage = 7
const PxReply = 8

// PxState is the scripted proxy: its CONNECT answer (for PxReply, reply in
// one write, the connection kept as a tunnel when keep), the heads it
// expects (a CONNECT, an absolute-form request answered with fwd_reply, or
// 200 "ok" when that is empty, a second one fwd2, a request inside the
// tunnel), and what it counts: connections, CONNECTs and heads that
// differed.
struct PxState {
    mode: int
    want: str
    fwd: str
    inner: str
    reply: str
    keep: bool
    fwd_reply: str
    fwd2: str
    accepts: raw.Atomic[u32]
    connects: raw.Atomic[u32]
    bad: raw.Atomic[u32]
}

// PxNew is a proxy state with nothing counted.
PxNew(mode: int, want: str, fwd: str, inner: str): PxState from want, fwd, inner {
    return PxState(mode: mode, want: want, fwd: fwd, inner: inner, reply: "", keep: false, fwd_reply: "", fwd2: "", accepts: raw.Atomic[u32].New(0), connects: raw.Atomic[u32].New(0),
        bad: raw.Atomic[u32].New(0))
}

// PxSec is the tests' connector: the origin name it accepts, and the
// handshakes it ran.
struct PxSec {
    host: str
    calls: raw.Atomic[u32]
}

type PxSecType = fn(&PxSec, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn

// PxHandshake counts a handshake and gives conn back, or refuses a name
// other than s.host (the connection staying the caller's).
PxHandshake(s: &PxSec, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    // Edge: none needed; the count is read after the client's calls return.
    _ = s.calls.Add(1)
    if host != s.host {
        fail net.Refused
    }
    return conn
}

// PxSecure is the net.Secure over s.
PxSecure(s: &PxSec): net.Secure from s {
    return net.Secure.Of[PxSec](core.ErasedCall.Of[PxSec, PxSecType](s, PxHandshake))
}

// PxStarts: b begins with s.
PxStarts(b: []u8, s: str): bool {
    sb := s.AsBytes()
    if b.Len() < sb.Len() {
        return false
    }
    for i in 0..sb.Len() {
        if b[i] != sb[i] {
            return false
        }
    }
    return true
}

// PxMiss counts a head that differed and answers 502; the connection ends.
PxMiss(io: Io, c: &net.Stream, st: &PxState): bool {
    // Edge: none needed; the count is read after the scope's join.
    _ = st.bad.Add(1)
    _ = CtSend(io, c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
    return false
}

// PxConnect answers the CONNECT head h by st.mode. True when the tunnel is
// made and the connection goes on.
PxConnect(io: Io, c: &net.Stream, st: &PxState, h: []u8): bool {
    if !CtSame(h, st.want) {
        return PxMiss(io, c, st)
    }
    // Edge: none needed; the count is read after the scope's join.
    _ = st.connects.Add(1)
    m := st.mode
    if m == PxOk {
        return CtSend(io, c, "HTTP/1.1 200 Connection established\r\n\r\n")
    }
    if m == PxRefuse {
        _ = CtSend(io, c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"p\"\r\nContent-Length: 0\r\n\r\n")
        return false
    }
    if m == PxSlow {
        return CtNap(io, 100) && CtSend(io, c, "HTTP/1.0 200 OK\r\n\r\n")
    }
    if m == PxBig {
        if !CtSend(io, c, "HTTP/1.1 200 OK\r\nX-Big: ") {
            return false
        }
        var xs: [1000]u8 = [120; 1000]
        for _ in 0..2 {
            c.WriteAll(io, xs[..]) else {
                return false
            }
        }
        _ = CtSend(io, c, "\r\n\r\n")
        return false
    }
    if m == PxBare {
        _ = CtSend(io, c, "HTTP/1.1 200 OK\n\n")
        return false
    }
    if m == PxExtra {
        _ = CtSend(io, c, "HTTP/1.1 200 OK\r\n\r\nX")
        return false
    }
    if m == PxPage {
        // A refusal with its error page, head and body in one write.
        _ = CtSend(io, c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 6\r\n\r\ndenied")
        return false
    }
    if m == PxReply {
        return CtSend(io, c, st.reply) && st.keep
    }
    // PxCut: the connection ends with no reply.
    return false
}

// PxAnswer answers a request head h that must be want with 200 "ok".
PxAnswer(io: Io, c: &net.Stream, st: &PxState, h: []u8, want: str): bool {
    if !CtSame(h, want) {
        return PxMiss(io, c, st)
    }
    return CtSend(io, c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
}

// PxConn serves one connection of the proxy: heads are read whole (these
// requests have no body); a CONNECT first makes a tunnel, after which
// every head is the origin's, else every head is a forwarded request.
PxConn(io: Io, c: net.Stream, st: &PxState) {
    var rb: [8192]u8 = [0; 8192]
    var e := 0
    var tunnel := false
    for {
        var end := CtHeadEnd(rb[0..e])
        for end < 0 {
            if e == rb.Len() {
                c.Close()
                return
            }
            n := c.Read(io, mut rb[e..rb.Len()]) else {
                c.Close()
                return
            }
            if n == 0 {
                c.Close()
                return
            }
            e += n
            end = CtHeadEnd(rb[0..e])
        }
        var keep := false
        if tunnel {
            keep = PxAnswer(io, &c, st, rb[0..end], st.inner)
        } else if PxStarts(rb[0..end], "CONNECT ") {
            keep = PxConnect(io, &c, st, rb[0..end])
            tunnel = keep
        } else if st.fwd2.Len() > 0 && CtSame(rb[0..end], st.fwd2) {
            keep = CtSend(io, &c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
        } else if st.fwd_reply.Len() > 0 && CtSame(rb[0..end], st.fwd) {
            keep = CtSend(io, &c, st.fwd_reply)
        } else {
            keep = PxAnswer(io, &c, st, rb[0..end], st.fwd)
        }
        k := e - end
        for x in 0..k {
            rb[x] = rb[end + x]
        }
        e = k
        if !keep {
            c.Close()
            return
        }
    }
}

// PxServe accepts on l until its scope is cancelled, each connection
// served by PxConn in a task of its own.
PxServe(io: Io, l: &net.Listener, st: &PxState) {
    scope cs := io.Scope() {
        for {
            c := l.Accept(io) else {
                return
            }
            // Edge: none needed; the count is read after the scope's join.
            _ = st.accepts.Add(1)
            cs.go PxConn(io, c, st)
        }
    }
}

// PxCode is a number for e: ProxyRefused(code) -1000 - code, BadUrl -20,
// Refused -21, Closed -22, BadRequest(offset) -5000 - offset, else CtCode.
PxCode(e: Error): int {
    match e {
        ProxyRefused(code) {
            return -1000 - code
        }
        BadUrl {
            return -20
        }
        net.Refused {
            return -21
        }
        Closed {
            return -22
        }
        BadRequest(offset) {
            return -5000 - offset
        }
        _ {
            return CtCode(e)
        }
    }
}

// PxGet fetches u with c, sending X-A and a Proxy-Authorization: the
// reply's status when its body is "ok", -2 when it differs, PxCode of the
// error otherwise.
PxGet(c: &Client, h: &Heap, io: Io, u: str): int {
    var ar := Arena.New(h) else {
        return -100
    }
    defer ar.Free()
    pu := url.Parse(&ar, u, none) else {
        return -101
    }
    hs := [Header(name: "X-A", value: "1"), Header(name: "Proxy-Authorization", value: "Basic eDp5")]
    req := ClientRequest(method: "GET", url: &pu, headers: hs[..], body: "".AsBytes())
    r := c.Fetch(&ar, io, &req) else e {
        return PxCode(e)
    }
    if !CtSame(r.body, "ok") {
        return -2
    }
    return r.status
}

// PxFwd is the head the proxy gets for a GET of
// http://origin.invalid:8080/ok: the absolute form, Host, and the
// caller's fields, Proxy-Authorization among them.
const PxFwd = "GET http://origin.invalid:8080/ok HTTP/1.1\r\nHost: origin.invalid:8080\r\nX-A: 1\r\nProxy-Authorization: Basic eDp5\r\n\r\n"

// PxPlain: through the proxy at addr (a URL), two GETs of
// http://origin.invalid:8080/ok are answered over one connection to the
// proxy at listen, and no CONNECT is sent.
PxPlain(io: Io, h: &Heap, listen: str, addr: str): bool {
    l := net.Listen(io, listen) else {
        return false
    }
    st := PxNew(PxOk, "", PxFwd, "")
    var ar := Arena.New(h) else {
        l.Close()
        return false
    }
    defer ar.Free()
    pu := url.Parse(&ar, addr, none) else {
        l.Close()
        return false
    }
    var lim := DefaultClientLimits
    lim.proxy = some(&pu)
    var c := Client.New(h, lim)
    var ok := true
    scope srv := io.Scope() {
        srv.go PxServe(io, &l, &st)
        ok = PxGet(&c, h, io, "http://origin.invalid:8080/ok") == 200
        ok = ok && PxGet(&c, h, io, "http://origin.invalid:8080/ok") == 200
        ok = ok && CtBusy(&c) == 0 && CtKept(&c, io) == 1
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return ok && st.accepts.Load() == 1 && st.connects.Load() == 0 && st.bad.Load() == 0
}

PxPlainCases(io: Io, h: &Heap): bool {
    stated := PxPlain(io, h, "127.0.0.1:7500", "http://127.0.0.1:7500/ignored?x=1")
    // No port: the proxy's is 80, and its name is the client's to resolve.
    dflt := PxPlain(io, h, "127.0.0.1:80", "http://localhost")
    // An IPv6 proxy: its address is dialled without the brackets.
    v6 := PxPlain(io, h, "[::1]:7508", "http://[::1]:7508")
    return stated && dflt && v6
}

test "With a proxy, an http request goes to it in absolute form with Host and the caller's fields, a proxy URL with no port is port 80, an IPv6 proxy is dialled, and the connection is pooled" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxPlainCases))
    }
}

// PxTunnel: through the proxy at listen, two GETs of u are answered over
// one CONNECT tunnel, asked for with the head want, after one handshake
// with the name host, the requests inside it being inner.
PxTunnel(io: Io, h: &Heap, listen: str, u: str, want: str, inner: str, host: str): bool {
    l := net.Listen(io, listen) else {
        return false
    }
    st := PxNew(PxOk, want, "", inner)
    sec := PxSec(host: host, calls: raw.Atomic[u32].New(0))
    var ar := Arena.New(h) else {
        l.Close()
        return false
    }
    defer ar.Free()
    var pb: [64]u8 = [0; 64]
    pu := url.Parse(&ar, PxCat(mut pb, "http://", listen), none) else {
        l.Close()
        return false
    }
    var lim := DefaultClientLimits
    lim.proxy = some(&pu)
    lim.secure = some(PxSecure(&sec))
    var c := Client.New(h, lim)
    var ok := true
    scope srv := io.Scope() {
        srv.go PxServe(io, &l, &st)
        ok = PxGet(&c, h, io, u) == 200
        ok = ok && PxGet(&c, h, io, u) == 200
        ok = ok && CtBusy(&c) == 0 && CtKept(&c, io) == 1
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return ok && st.accepts.Load() == 1 && st.connects.Load() == 1 && st.bad.Load() == 0 && sec.calls.Load() == 1
}

// PxCat is x then y in b.
PxCat(b: mut [64]u8, x: str, y: str): str from b {
    var n := 0
    for part in [x.AsBytes(), y.AsBytes()] {
        for v in part {
            b[n] = v
            n += 1
        }
    }
    return str.From(b[0..n]).Must()
}

PxTunnelCases(io: Io, h: &Heap): bool {
    name := PxTunnel(io, h, "127.0.0.1:7501", "https://origin.invalid/ok", "CONNECT origin.invalid:443 HTTP/1.1\r\nHost: origin.invalid:443\r\n\r\n",
        "GET /ok HTTP/1.1\r\nHost: origin.invalid\r\nX-A: 1\r\n\r\n", "origin.invalid")
    // An IPv6 origin: the CONNECT target keeps its brackets, the
    // connector gets the address without them.
    v6 := PxTunnel(io, h, "127.0.0.1:7502", "https://[2001:db8::1]:8443/ok", "CONNECT [2001:db8::1]:8443 HTTP/1.1\r\nHost: [2001:db8::1]:8443\r\n\r\n",
        "GET /ok HTTP/1.1\r\nHost: [2001:db8::1]:8443\r\nX-A: 1\r\n\r\n", "2001:db8::1")
    return name && v6
}

test "With a proxy, an https request asks for a CONNECT tunnel to host:port, then the handshake checks the origin's name, the request goes in origin form without Proxy-Authorization, and the tunnel is pooled" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxTunnelCases))
    }
}

// PxWant is the CONNECT head for https://origin.invalid/ok, and PxInner the
// request inside its tunnel.
const PxWant = "CONNECT origin.invalid:443 HTTP/1.1\r\nHost: origin.invalid:443\r\n\r\n"
const PxInner = "GET /ok HTTP/1.1\r\nHost: origin.invalid\r\nX-A: 1\r\n\r\n"

// PxFaultOn is the outcome of a GET of https://origin.invalid/ok through a
// proxy at listen scripted by st, with the limits' connect at ms and
// header_bytes at hb: PxGet's code, or -999 when a head the proxy got
// differed, or when tunnel is false and a handshake ran or the pool kept
// the connection, or when tunnel is true and the request was not answered
// inside one CONNECT tunnel after one handshake.
PxFaultOn(io: Io, h: &Heap, listen: str, st: &PxState, ms: i64, hb: int, tunnel: bool): int {
    l := net.Listen(io, listen) else {
        return -998
    }
    sec := PxSec(host: "origin.invalid", calls: raw.Atomic[u32].New(0))
    var ar := Arena.New(h) else {
        l.Close()
        return -998
    }
    defer ar.Free()
    var pb: [64]u8 = [0; 64]
    pu := url.Parse(&ar, PxCat(mut pb, "http://", listen), none) else {
        l.Close()
        return -998
    }
    var lim := DefaultClientLimits
    lim.proxy = some(&pu)
    lim.secure = some(PxSecure(&sec))
    lim.connect = time.Millis(ms)
    lim.header_bytes = hb
    var c := Client.New(h, lim)
    var r := 0
    var kept := 0
    scope srv := io.Scope() {
        srv.go PxServe(io, &l, st)
        r = PxGet(&c, h, io, "https://origin.invalid/ok")
        kept = CtBusy(&c) + CtKept(&c, io)
        srv.Cancel()
    }
    c.Close()
    l.Close()
    if st.bad.Load() != 0 {
        return -999
    }
    if tunnel {
        if sec.calls.Load() != 1 || st.connects.Load() != 1 {
            return -999
        }
        return r
    }
    if sec.calls.Load() != 0 || kept != 0 {
        return -999
    }
    return r
}

// PxFault is PxFaultOn for a proxy that answers the CONNECT by mode and
// makes no tunnel.
PxFault(io: Io, h: &Heap, listen: str, mode: int, ms: i64, hb: int): int {
    st := PxNew(mode, PxWant, "", PxInner)
    return PxFaultOn(io, h, listen, &st, ms, hb, false)
}

// PxAnswerWith is PxFaultOn for a proxy that answers the CONNECT with reply
// in one write and, when tunnel, keeps the connection as the tunnel; the
// connect limit is 1 s.
PxAnswerWith(io: Io, h: &Heap, listen: str, reply: str, hb: int, tunnel: bool): int {
    var st := PxNew(PxReply, PxWant, "", PxInner)
    st.reply = reply
    st.keep = tunnel
    return PxFaultOn(io, h, listen, &st, 1000, hb, tunnel)
}

// PxPad is a 2xx reply head of total bytes, total >= 28: the status line,
// one X-Pad field and the blank line.
PxPad(a: &Arena, total: int): str from a {
    var b := a.Slice[u8](total) else {
        return ""
    }
    pre := "HTTP/1.1 200 OK\r\nX-Pad: ".AsBytes()
    for i in 0..pre.Len() {
        b[i] = pre[i]
    }
    for i in pre.Len()..total - 4 {
        b[i] = 112
    }
    end := "\r\n\r\n".AsBytes()
    for i in 0..4 {
        b[total - 4 + i] = end[i]
    }
    return str.From(b[0..total]).Must()
}

PxRefusedCase(io: Io, h: &Heap): bool {
    bare := PxFault(io, h, "127.0.0.1:7503", PxRefuse, 1000, 1024)
    // A body after a refusal's head does not make it BadRequest.
    page := PxFault(io, h, "127.0.0.1:7507", PxPage, 1000, 1024)
    // The statuses just outside 2xx are refusals, and 299 makes the tunnel.
    low := PxAnswerWith(io, h, "127.0.0.1:7520", "HTTP/1.1 199 Odd\r\n\r\n", 1024, false)
    high := PxAnswerWith(io, h, "127.0.0.1:7521", "HTTP/1.1 300 Multiple Choices\r\nContent-Length: 0\r\n\r\n", 1024, false)
    top := PxAnswerWith(io, h, "127.0.0.1:7522", "HTTP/1.1 299 Odd\r\n\r\n", 1024, true)
    // An interim 1xx before the 2xx is a refusal too, as Go's Transport has
    // it: the first head is the reply.
    interim := PxAnswerWith(io, h, "127.0.0.1:7523", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\n\r\n", 1024, false)
    return bare == -1407 && page == -1403 && low == -1199 && high == -1300 && top == 200 && interim == -1100
}

test "A CONNECT the proxy answers with a status outside 2xx, 199 and 300 included, with a body or none, or with an interim 1xx first, is ProxyRefused with that status, and no handshake runs; a 299 makes the tunnel" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxRefusedCase))
    }
}

PxFaultCases(io: Io, h: &Heap): bool {
    var ar := Arena.New(h) else {
        return false
    }
    defer ar.Free()
    // An answer after the connect limit.
    slow := PxFault(io, h, "127.0.0.1:7510", PxSlow, 30, 1024)
    // A head past header_bytes.
    big := PxFault(io, h, "127.0.0.1:7511", PxBig, 1000, 1024)
    // The connection's end before any byte.
    cut := PxFault(io, h, "127.0.0.1:7512", PxCut, 1000, 1024)
    // A bare LF in the head.
    bare := PxFault(io, h, "127.0.0.1:7513", PxBare, 1000, 1024)
    // A byte after the head, at offset 19, in the same write. Tunnel sees
    // it only when it arrives in the head's read, as it does under these
    // seeds and with the cases above run first; a later byte reaches the
    // connector. So this case stays fifth.
    extra := PxFault(io, h, "127.0.0.1:7514", PxExtra, 1000, 1024)
    // A head that never ends: 20,000 bytes of one field, then the close.
    // The client stops reading at header_bytes.
    endless := PxAnswerWith(io, h, "127.0.0.1:7516", PxPad(&ar, 20028)[0..20000], 1024, false)
    // A head 10 bytes over header_bytes that ends within one read.
    over := PxAnswerWith(io, h, "127.0.0.1:7517", PxPad(&ar, 210), 200, false)
    // A head of exactly header_bytes makes the tunnel.
    exact := PxAnswerWith(io, h, "127.0.0.1:7518", PxPad(&ar, 200), 200, true)
    // A 2xx head is framed as any reply's: both framing fields, offset 36.
    both := PxAnswerWith(io, h, "127.0.0.1:7524", "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n", 1024, false)
    // The same slow answer inside the limit makes the tunnel.
    sl := PxNew(PxSlow, PxWant, "", PxInner)
    inside := PxFaultOn(io, h, "127.0.0.1:7515", &sl, 1000, 1024, true)
    return slow == -6 && big == -3 && endless == -3 && over == -3 && exact == 200 && cut == -22 && bare <= -5000 && both == -5036 && extra == -5019 && inside == 200
}

test "The CONNECT reply is bounded by the connect limit and by header_bytes, read as it grows or whole, and a head of exactly header_bytes makes the tunnel; its end before a byte is Closed, and a bare LF, both framing fields or a byte after its head is BadRequest" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxFaultCases))
    }
}

// PxOnly7504 allows the address 127.0.0.1:7504 alone.
PxOnly7504(a: &net.Addr): bool {
    return a.port == 7504 && !a.v6 && a.ip[0] == 127 && a.ip[3] == 1
}

// PxAllowed: with allow letting only 127.0.0.1:7504 through, a request
// through a proxy there reaches the origin, and one through a proxy at
// 127.0.0.1:7505 is Refused with no connection made.
PxAllowed(io: Io, h: &Heap): bool {
    la := net.Listen(io, "127.0.0.1:7504") else {
        return false
    }
    lb := net.Listen(io, "127.0.0.1:7505") else {
        la.Close()
        return false
    }
    sa := PxNew(PxOk, "", PxFwd, "")
    sb := PxNew(PxOk, "", PxFwd, "")
    var ar := Arena.New(h) else {
        la.Close()
        lb.Close()
        return false
    }
    defer ar.Free()
    pa := url.Parse(&ar, "http://127.0.0.1:7504", none) else {
        la.Close()
        lb.Close()
        return false
    }
    pb := url.Parse(&ar, "http://127.0.0.1:7505", none) else {
        la.Close()
        lb.Close()
        return false
    }
    var lima := DefaultClientLimits
    lima.allow = some(PxOnly7504)
    lima.proxy = some(&pa)
    var limb := lima
    limb.proxy = some(&pb)
    var ca := Client.New(h, lima)
    var cb := Client.New(h, limb)
    var ok := true
    scope srv := io.Scope() {
        srv.go PxServe(io, &la, &sa)
        srv.go PxServe(io, &lb, &sb)
        ok = PxGet(&ca, h, io, "http://origin.invalid:8080/ok") == 200
        ok = ok && PxGet(&cb, h, io, "http://origin.invalid:8080/ok") == -21
        srv.Cancel()
    }
    ca.Close()
    cb.Close()
    la.Close()
    lb.Close()
    return ok && sa.accepts.Load() == 1 && sb.accepts.Load() == 0 && sa.bad.Load() == 0
}

test "With a proxy, allow is asked about the proxy's address, and the origin's name is never resolved by the client" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxAllowed))
    }
}

// PxBadOne: a GET of http://origin.invalid:8080/ok and one of
// https://origin.invalid/ok through the proxy p, with a connector, are
// both BadUrl.
PxBadOne(io: Io, h: &Heap, p: str): bool {
    var ar := Arena.New(h) else {
        return false
    }
    defer ar.Free()
    pu := url.Parse(&ar, p, none) else {
        return false
    }
    sec := PxSec(host: "origin.invalid", calls: raw.Atomic[u32].New(0))
    var lim := DefaultClientLimits
    lim.proxy = some(&pu)
    lim.secure = some(PxSecure(&sec))
    var c := Client.New(h, lim)
    ok := PxGet(&c, h, io, "http://origin.invalid:8080/ok") == -20 && PxGet(&c, h, io, "https://origin.invalid/ok") == -20
    c.Close()
    return ok && sec.calls.Load() == 0
}

PxBadCases(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7506") else {
        return false
    }
    st := PxNew(PxOk, "", PxFwd, "")
    var ok := true
    scope srv := io.Scope() {
        srv.go PxServe(io, &l, &st)
        ok = PxBadOne(io, h, "https://127.0.0.1:7506")
        ok = ok && PxBadOne(io, h, "socks5://127.0.0.1:7506")
        ok = ok && PxBadOne(io, h, "http://u:p@127.0.0.1:7506")
        ok = ok && PxBadOne(io, h, "http://u@127.0.0.1:7506")
        ok = ok && PxBadOne(io, h, "file:///tmp/proxy")
        srv.Cancel()
    }
    l.Close()
    return ok && st.accepts.Load() == 0
}

test "A proxy that is not an http URL with a host and no userinfo is BadUrl, before any connection" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxBadCases))
    }
}

// PxRedir is the head the proxy gets for a GET of
// http://origin.invalid:8080/redir.
const PxRedir = "GET http://origin.invalid:8080/redir HTTP/1.1\r\nHost: origin.invalid:8080\r\nX-A: 1\r\nProxy-Authorization: Basic eDp5\r\n\r\n"

// PxRedirect: a GET of http://origin.invalid:8080/redir through the proxy
// at listen, answered with the redirect moved, is followed through the
// same proxy, the next hop being the absolute-form head fwd2, or a CONNECT
// want with inner inside it after a handshake with host. True when it ends
// in 200 "ok" with tunnels CONNECTs and handshakes and no head differing.
PxRedirect(io: Io, h: &Heap, listen: str, moved: str, want: str, fwd2: str, inner: str, host: str, tunnels: u32): bool {
    l := net.Listen(io, listen) else {
        return false
    }
    var st := PxNew(PxOk, want, PxRedir, inner)
    st.fwd_reply = moved
    st.fwd2 = fwd2
    sec := PxSec(host: host, calls: raw.Atomic[u32].New(0))
    var ar := Arena.New(h) else {
        l.Close()
        return false
    }
    defer ar.Free()
    var pb: [64]u8 = [0; 64]
    pu := url.Parse(&ar, PxCat(mut pb, "http://", listen), none) else {
        l.Close()
        return false
    }
    var lim := DefaultClientLimits
    lim.proxy = some(&pu)
    lim.secure = some(PxSecure(&sec))
    var c := Client.New(h, lim)
    var r := 0
    scope srv := io.Scope() {
        srv.go PxServe(io, &l, &st)
        r = PxGet(&c, h, io, "http://origin.invalid:8080/redir")
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return r == 200 && st.bad.Load() == 0 && st.connects.Load() == tunnels && sec.calls.Load() == tunnels
}

PxRedirectCases(io: Io, h: &Heap): bool {
    // To https on another origin: a CONNECT to it through the same proxy.
    tls := PxRedirect(io, h, "127.0.0.1:7525", "HTTP/1.1 302 Found\r\nLocation: https://other.invalid/ok\r\nContent-Length: 0\r\n\r\n",
        "CONNECT other.invalid:443 HTTP/1.1\r\nHost: other.invalid:443\r\n\r\n", "", "GET /ok HTTP/1.1\r\nHost: other.invalid\r\nX-A: 1\r\n\r\n", "other.invalid", 1)
    // To http on another origin: the same proxy in absolute form, without
    // the caller's Proxy-Authorization, which a change of origin strips
    // with the other credentials.
    plain := PxRedirect(io, h, "127.0.0.1:7526", "HTTP/1.1 302 Found\r\nLocation: http://other.invalid:8080/ok\r\nContent-Length: 0\r\n\r\n", "",
        "GET http://other.invalid:8080/ok HTTP/1.1\r\nHost: other.invalid:8080\r\nX-A: 1\r\n\r\n", "", "other.invalid", 0)
    return tls && plain
}

test "With a proxy, a redirect is followed through the same proxy, to https as a CONNECT to the new origin and to http in absolute form, and a change of origin strips the caller's Proxy-Authorization" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), PxRedirectCases))
    }
}
