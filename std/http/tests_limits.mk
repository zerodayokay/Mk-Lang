// Why: the client's idle and close limits and the pool's claim protocol
// are where timing bugs hide.
// What: the idle and close limits, a client target with bytes RFC 3986
// does not allow sent to ServeWith, and the pool's claim protocol.
// How: a simulated Io; the claim protocol runs over a hundred schedules.
// Run by mk test --std=std.http.

import std.core
import std.net
import std.raw
import std.time
import std.url

// LmIdleCase fetches /ok twice from a client whose idle limit is 50 ms,
// nap ms apart: the scripted peer's accepts.
LmIdleCase(io: Io, h: &Heap, port: str, url: str, nap: i64): int {
    l := net.Listen(io, port) else {
        return -1
    }
    n := raw.Atomic[u32].New(0)
    var lim := DefaultClientLimits
    lim.idle = time.Millis(50)
    var c := Client.New(h, lim)
    var ok := true
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        ok = CtGet(&c, h, io, url, "ok") == 200
        ok = ok && CtNap(io, nap)
        ok = ok && CtGet(&c, h, io, url, "ok") == 200
        ok = ok && CtBusy(&c) == 0
        srv.Cancel()
    }
    c.Close()
    l.Close()
    // Edge: none needed; the count is read after the scope's join.
    return if ok { Cast[int](n.Load()) } else { -1 }
}

LmIdle(io: Io, h: &Heap): bool {
    stale := LmIdleCase(io, h, "127.0.0.1:7520", "http://127.0.0.1:7520/ok", 60)
    fresh := LmIdleCase(io, h, "127.0.0.1:7521", "http://127.0.0.1:7521/ok", 40)
    return stale == 2 && fresh == 1
}

test "a pooled connection idle past ClientLimits.idle is closed, not reused, and one inside it is reused" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), LmIdle))
    }
}

// SlowClose is a transport over a connection whose Close waits delay ms
// before it shuts the connection: a secure close that speaks to the peer.
linear view struct SlowClose {
    inner: net.Conn
    delay: i64
}

impl net.Transport for SlowClose {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return try self.inner.Read(io, buf)
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        try self.inner.WriteAll(io, data)
    }
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        try self.inner.Shutdown(io, how)
    }
    PeerAddr(&self): ?net.Addr {
        return none
    }
    Close(&self, io: Io): net.Error!void {
        try io.Sleep(time.Millis(self.delay))
        try self.inner.Shutdown(io, net.Shutdown.Both)
    }
    Abandon(self) {
        SlowClose(inner, delay: _) := self
        inner.Abandon()
    }
}

// SlowSec is the connector's state: the close delay of each connection.
struct SlowSec {
    delay: i64
}

type SlowType = fn(&SlowSec, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn

// SlowConnect wraps conn in a SlowClose (no handshake).
SlowConnect(s: &SlowSec, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    // The box first, so a failure leaves conn with the caller.
    slot := try net.ReserveConn[SlowClose](h)
    return slot.Fill(SlowClose(inner: conn, delay: s.delay))
}

// SlowSecure is the net.Secure over s.
SlowSecure(s: &SlowSec): net.Secure from s {
    return net.Secure.Of[SlowSec](core.ErasedCall.Of[SlowSec, SlowType](s, SlowConnect))
}

// LmCloseCase fetches https://port/close (a reply with Connection: close,
// so Exchange.Close closes the connection) from a client whose close limit
// is 20 ms, through connections whose Close takes delay ms: CtGet's code,
// and -50 when a slot stays busy or a connection stays kept.
LmCloseCase(io: Io, h: &Heap, port: str, delay: i64): int {
    l := net.Listen(io, port) else {
        return -60
    }
    n := raw.Atomic[u32].New(0)
    sec := SlowSec(delay: delay)
    var lim := DefaultClientLimits
    lim.close = time.Millis(20)
    lim.secure = some(SlowSecure(&sec))
    var c := Client.New(h, lim)
    var r := 0
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        var ub: [64]u8 = [0; 64]
        r = CtGet(&c, h, io, LmUrl(mut ub, port, "/close"), "ok")
        if CtBusy(&c) != 0 || CtKept(&c, io) != 0 {
            r = -50
        }
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return r
}

// LmUrl is "https://", addr and path, in b.
LmUrl(b: mut [64]u8, addr: str, path: str): str from b {
    var n := 0
    for part in ["https://".AsBytes(), addr.AsBytes(), path.AsBytes()] {
        for x in part {
            b[n] = x
            n += 1
        }
    }
    return str.From(b[0..n]).Must()
}

LmClose(io: Io, h: &Heap): bool {
    quick := LmCloseCase(io, h, "127.0.0.1:7522", 5)
    slow := LmCloseCase(io, h, "127.0.0.1:7523", 50)
    return quick == 200 && slow == -6
}

test "Exchange.Close of a connection whose close outlasts ClientLimits.close is time.TimedOut, and the connection is released" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), LmClose))
    }
}

// LmCancelClose: Do and the body read whole, then Close in a scope
// already cancelled, through a connection whose Close takes 50 ms: Close
// is Canceled, the connection released (no slot busy, none kept), and the
// Exchange the caller's to Abandon.
LmCancelClose(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7524") else {
        return false
    }
    n := raw.Atomic[u32].New(0)
    sec := SlowSec(delay: 50)
    var lim := DefaultClientLimits
    lim.secure = some(SlowSecure(&sec))
    var c := Client.New(h, lim)
    var ok := false
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        ok = LmCancelOnce(&c, h, io) && CtBusy(&c) == 0 && CtKept(&c, io) == 0
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return ok
}

LmCancelOnce(c: &Client, h: &Heap, io: Io): bool {
    var ar := Arena.New(h) else {
        return false
    }
    defer ar.Free()
    var ub: [64]u8 = [0; 64]
    pu := url.Parse(&ar, LmUrl(mut ub, "127.0.0.1:7524", "/close"), none) else {
        return false
    }
    hs := List[Header].New(&ar).Done()
    req := ClientRequest(method: "GET", url: &pu, headers: hs[..], body: "".AsBytes())
    var ex := c.Do(&ar, io, &req) else {
        return false
    }
    var buf: [16]u8 = [0; 16]
    got := CnReadAll(mut ex, mut buf[..])
    var code := 0
    scope s := io.Scope() {
        s.Cancel()
        ex.Close(io) else e {
            code = CtCode(e)
            ex.Abandon()
        }
    }
    return got == 2 && code == -7
}

test "Exchange.Close under a cancelled scope is Canceled and releases the connection" (sys: &System) {
    Assert(CtSim(sys, 1, LmCancelClose))
}

// LmTarget fetches a URL whose path and query hold bytes RFC 3986 does not
// allow from ServeWith: the client escapes them, so the server answers
// 200, not 400.
LmTarget(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7525") else {
        return false
    }
    var o := DefaultServerOptions
    var c := Client.New(h, DefaultClientLimits)
    var code := 0
    scope top := io.Scope() {
        top.go SwServeOne(io, &l, h, &o)
        code = CtGet(&c, h, io, "http://127.0.0.1:7525/a|b[c]{d}^?q={1}|^`", "")
        top.Cancel()
    }
    c.Close()
    l.Close()
    return code == 200
}

test "a client target with bytes RFC 3986 does not allow is escaped, so std.http's own ServeWith answers it" (sys: &System) {
    Assert(CtSim(sys, 1, LmTarget))
}

test "the pool's claim protocol over a hundred schedules: shared clients, overflow and retries leave no slot busy" (sys: &System) {
    for seed in 1..101 {
        Assert(CtSim(sys, Cast[u64](seed), CtShare))
        Assert(CtSim(sys, Cast[u64](seed), CtRetry))
        Assert(CtSim(sys, Cast[u64](seed), CtLeak))
    }
}

// LmLateServe accepts one connection on l, reads one request head, sleeps
// 1 ms (the client then arms its response_header deadline and waits for
// the head), moves the simulated clock nanos further with clock reads,
// each 1 us of it and no choice, so no deadline fires meanwhile, then
// cancels o, the scope the client's request runs in, and closes.
LmLateServe(io: Io, l: &net.Listener, o: &Scope, nanos: i64) {
    st := l.Accept(io) else {
        return
    }
    var rb: [4096]u8 = [0; 4096]
    var e := 0
    for CtHeadEnd(rb[0..e]) < 0 {
        if e == rb.Len() {
            st.Close()
            return
        }
        n := st.Read(io, mut rb[e..rb.Len()]) else {
            st.Close()
            return
        }
        if n == 0 {
            st.Close()
            return
        }
        e += n
    }
    io.Sleep(time.Millis(1)) else {
        st.Close()
        return
    }
    t0 := time.Now(io)
    for time.Now(io).Since(t0).Nanos() < nanos {
    }
    o.Cancel()
    // A wait, so the client takes its Canceled before the close reaches it.
    io.NoCancel().Sleep(time.Millis(1)) else {}
    st.Close()
}

// LmLateCase fetches from a client whose response_header limit is 20 ms
// while LmLateServe cancels the request's scope nanos after the client
// waits for the head: CtGet's code.
LmLateCase(io: Io, h: &Heap, port: str, u: str, nanos: i64): int {
    l := net.Listen(io, port) else {
        return -60
    }
    var lim := DefaultClientLimits
    lim.response_header = time.Millis(20)
    var c := Client.New(h, lim)
    var code := 0
    scope o := io.Scope() {
        o.go LmLateServe(io, &l, &o, nanos)
        code = CtGet(&c, h, io, u, "ok")
    }
    c.Close()
    l.Close()
    return code
}

// LmLate: a cancel that reaches the request once its response_header
// deadline has passed, before that deadline fired, is time.TimedOut (with
// one timer heap per carrier an ancestor's later deadline can fire first);
// one that comes before the deadline is Canceled.
LmLate(io: Io, h: &Heap): bool {
    late := LmLateCase(io, h, "127.0.0.1:7527", "http://127.0.0.1:7527/x", 30_000_000)
    early := LmLateCase(io, h, "127.0.0.1:7528", "http://127.0.0.1:7528/x", 5_000_000)
    return late == -6 && early == -7
}

test "a cancel that reaches a request past its response_header limit, before the limit's deadline fired, is time.TimedOut; one before the limit is Canceled" (sys: &System) {
    for seed in 1..5 {
        Assert(CtSim(sys, Cast[u64](seed), LmLate))
    }
}
