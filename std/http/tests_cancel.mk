// Why: an interrupted connection must never be reused.
// What: client cases (Do, Fetch, a body read) and a server write, cut short.
// How: a deadline swept across one exchange on the Sim's clock, over
// several seeds; the peer is tests_client.mk's.

import std.net
import std.raw
import std.time
import std.url

// CnSpan is the sweep's end in milliseconds: past every scripted reply's
// last byte (50 ms of sleep and at most 1 ms of the Sim's latencies).
const CnSpan = 56

// CnFetchIn fetches u with c inside a scope whose deadline is d ms:
// CtGet's code.
CnFetchIn(c: &Client, h: &Heap, io: Io, u: str, want: str, d: i64): int {
    var r := 0
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(d))
        r = CtGet(c, h, io, u, want)
    }
    return r
}

// CnFetchSweep: for each deadline from 0 to CnSpan ms, a Fetch of u either
// delivers want with its connection kept, or fails Canceled (Do) or
// ReadError (the body) with no slot busy and no connection kept; either
// way the next request, with no deadline, is answered, over a new
// connection exactly when the interrupted one was dropped.
CnFetchSweep(io: Io, h: &Heap, port: int, path: str, want: str): bool {
    var ab: [64]u8 = [0; 64]
    addr := CnAddr(mut ab, "127.0.0.1:", port, "")
    l := net.Listen(io, addr) else {
        return false
    }
    n := raw.Atomic[u32].New(0)
    var ok := true
    var c := Client.New(h, DefaultClientLimits)
    var ub: [64]u8 = [0; 64]
    u := CnAddr(mut ub, "http://127.0.0.1:", port, path)
    var ob: [64]u8 = [0; 64]
    uok := CnAddr(mut ob, "http://127.0.0.1:", port, "/ok")
    var fails := 0
    var wins := 0
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        for d in 0..CnSpan + 1 {
            r := CnFetchIn(&c, h, io, u, want, i64(d))
            if r == 200 {
                wins += 1
                ok = ok && CtBusy(&c) == 0 && CtKept(&c, io) == 1
                before := n.Load()
                ok = ok && CtGet(&c, h, io, uok, "ok") == 200 && n.Load() == before
            } else {
                fails += 1
                ok = ok && (r == -7 || r == -4) && CtBusy(&c) == 0 && CtKept(&c, io) == 0
                before := n.Load()
                ok = ok && CtGet(&c, h, io, uok, "ok") == 200 && n.Load() == before + 1
            }
            if !ok {
                break
            }
        }
        srv.Cancel()
    }
    c.Close()
    l.Close()
    // The sweep reached both ends: early deadlines fail, late ones do not.
    return ok && fails > 0 && wins > 0
}

// CnAddr writes pre, the decimal port and post into b and returns it.
CnAddr(b: mut [64]u8, pre: str, port: int, post: str): str from b {
    t := ThAddr(b, pre, port)
    n := t.Len()
    p := post.AsBytes()
    for i in 0..p.Len() {
        b[n + i] = p[i]
    }
    return str.From(b[0..n + p.Len()]).Must()
}

CnSweepBody(io: Io, h: &Heap): bool {
    return CnFetchSweep(io, h, 7450, "/slowbody", "abcd")
}

CnSweepChunked(io: Io, h: &Heap): bool {
    return CnFetchSweep(io, h, 7451, "/slowchunked", "abcd")
}

CnSweepHead(io: Io, h: &Heap): bool {
    return CnFetchSweep(io, h, 7452, "/slowhead", "ok")
}

test "a Fetch interrupted at each await point is closed, never pooled, and the client goes on (Content-Length body)" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), CnSweepBody))
    }
}

test "a Fetch interrupted at each await point is closed, never pooled, and the client goes on (chunked body)" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), CnSweepChunked))
    }
}

test "a Fetch interrupted at each await point is closed, never pooled, and the client goes on (slow head)" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), CnSweepHead))
    }
}

// CnReadAll reads ex's body into buf until its end: the bytes read, -1
// when a read failed.
CnReadAll(ex: mut Exchange, buf: mut []u8): int {
    var none_buf: [0]u8 = []
    var r := ex.Body(mut none_buf[..])
    var n := 0
    for n < buf.Len() {
        k := r.Read(mut buf[n..buf.Len()]) else {
            return -1
        }
        if k == 0 {
            return n
        }
        n += k
    }
    return n
}

// CnReadOnce is one read of ex's body into buf: the bytes, -1 on a failure.
CnReadOnce(ex: mut Exchange, buf: mut []u8): int {
    var none_buf: [0]u8 = []
    var r := ex.Body(mut none_buf[..])
    k := r.Read(buf) else {
        return -1
    }
    return k
}

// CnBodyOnce: Do with no deadline, then the body read under a deadline of
// d ms. 1 when the body came whole and Close kept the connection; 2 when
// the read failed, a later read with no deadline failed too, and Close
// left the pool empty; 0 otherwise.
CnBodyOnce(c: &Client, h: &Heap, io: Io, u: str, d: i64): int {
    var ar := Arena.New(h) else {
        return 0
    }
    defer ar.Free()
    pu := url.Parse(&ar, u, none) else {
        return 0
    }
    hs := List[Header].New(&ar).Done()
    req := ClientRequest(method: "GET", url: &pu, headers: hs[..], body: "".AsBytes())
    var ex := c.Do(&ar, io, &req) else {
        return 0
    }
    var buf: [16]u8 = [0; 16]
    var got := 0
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(d))
        got = CnReadAll(mut ex, mut buf[..])
    }
    out := if got < 0 {
        if CnReadAll(mut ex, mut buf[..]) < 0 { 2 } else { 0 }
    } else {
        if got == 4 && buf[0] == 97 && buf[3] == 100 { 1 } else { 0 }
    }
    // After a failed read Close fails too (its
    // connection closed), and the Exchange is the caller's to Abandon.
    var closed := true
    ex.Close(io) else {
        ex.Abandon()
        closed = false
    }
    if closed != (out == 1) {
        return 0
    }
    kept := CtKept(c, io)
    if out == 1 && kept != 1 || out == 2 && kept != 0 || CtBusy(c) != 0 {
        return 0
    }
    return out
}

// CnBodySweep: the body read of /slowbody under each deadline from 0 to
// CnSpan ms is whole or Failed, and both happen.
CnBodySweep(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7453") else {
        return false
    }
    n := raw.Atomic[u32].New(0)
    var ok := true
    var c := Client.New(h, DefaultClientLimits)
    var wins := 0
    var fails := 0
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        for d in 0..CnSpan + 1 {
            r := CnBodyOnce(&c, h, io, "http://127.0.0.1:7453/slowbody", i64(d))
            if r == 1 {
                wins += 1
            } else if r == 2 {
                fails += 1
            } else {
                ok = false
                break
            }
        }
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return ok && wins > 0 && fails > 0
}

test "an Exchange's body read interrupted at each await point fails every later read, and Close fails and does not pool it" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), CnBodySweep))
    }
}

// CnCancelledRead: a body read in a scope already cancelled, with no byte
// buffered, fails, and so does the next read in a live scope.
CnCancelledRead(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7454") else {
        return false
    }
    n := raw.Atomic[u32].New(0)
    var ok := false
    var c := Client.New(h, DefaultClientLimits)
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        ok = CnCancelledOnce(&c, h, io)
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return ok
}

CnCancelledOnce(c: &Client, h: &Heap, io: Io): bool {
    var ar := Arena.New(h) else {
        return false
    }
    defer ar.Free()
    pu := url.Parse(&ar, "http://127.0.0.1:7454/slowbody", none) else {
        return false
    }
    hs := List[Header].New(&ar).Done()
    req := ClientRequest(method: "GET", url: &pu, headers: hs[..], body: "".AsBytes())
    var ex := c.Do(&ar, io, &req) else {
        return false
    }
    var buf: [16]u8 = [0; 16]
    // "ab" came with the head; read it, so the next read must wait.
    first := CnReadOnce(mut ex, mut buf[0..2])
    var cut := 0
    scope s := io.Scope() {
        s.Cancel()
        cut = CnReadAll(mut ex, mut buf[..])
    }
    again := CnReadAll(mut ex, mut buf[..])
    // Close after the failed read fails too, yet
    // releases the connection; Abandon then ends the Exchange.
    var closed := true
    ex.Close(io) else {
        ex.Abandon()
        closed = false
    }
    return first == 2 && cut < 0 && again < 0 && !closed && CtKept(c, io) == 0 && CtBusy(c) == 0
}

test "a body read in a cancelled scope marks the Exchange failed, so a later read in a live scope fails too, and so does Close" (sys: &System) {
    Assert(CtSim(sys, 5, CnCancelledRead))
}

// CnApp is the server cases' context.
struct CnApp {
    n: int
}

// CnLate sleeps 20 ms (cut short by a cancel) and answers "late".
CnLate(c: &CnApp, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    io.Sleep(time.Millis(20)) else {}
    try res.Body(b"late")
}

// CnServeOne accepts one connection and serves it with ServeConnLoop.
CnServeOne(io: Io, l: &net.Listener, h: &Heap) {
    st := l.Accept(io) else {
        return
    }
    conn := st.Conn()
    app := CnApp(n: 0)
    ServeOwned(conn, h, io, &DefaultServerOptions, &app, CnLate)
}

// CnCancelAt cancels s after ms milliseconds.
CnCancelAt(s: &Scope, io: Io, ms: i64) {
    io.Sleep(time.Millis(ms)) else {
        return
    }
    s.Cancel()
}

// CnServer serves in a scope cancelled at 10 ms, while CnLate sleeps.
CnServer(io: Io, l: &net.Listener, h: &Heap) {
    scope sv := io.Scope() {
        sv.go CnServeOne(io, l, h)
        sv.go CnCancelAt(&sv, io, 10)
    }
}

// CnWriteCut: two pipelined requests; the server's scope is cancelled
// while its handler runs, so the first response's write is interrupted.
// The client reads the connection's end with no byte of either reply:
// the connection was closed, and the second request never served.
CnWriteCut(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7455") else {
        return false
    }
    var total := -1
    scope top := io.Scope() {
        top.go CnServer(io, &l, h)
        total = CnPipelined(io)
    }
    l.Close()
    return total == 0
}

// CnPipelined dials, sends two requests at once and counts the bytes read
// until the connection's end; -1 on an error.
CnPipelined(io: Io): int {
    c := net.Dial(io, "127.0.0.1:7455") else {
        return -1
    }
    c.WriteAll(io, "GET /a HTTP/1.1\r\nHost: x\r\n\r\nGET /b HTTP/1.1\r\nHost: x\r\n\r\n".AsBytes()) else {
        c.Close()
        return -1
    }
    var buf: [256]u8 = [0; 256]
    var total := 0
    for {
        k := c.Read(io, mut buf[..]) else {
            c.Close()
            return -1
        }
        if k == 0 {
            break
        }
        total += k
    }
    c.Close()
    return total
}

test "a server connection whose response write is interrupted is closed, and a pipelined request after it is never served" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), CnWriteCut))
    }
}
