// Why: the secure slots and the connect limit are checked without real TLS.
// What: the client's connect limit and ServeWith's secure slot.
// How: FakeSec's handshake sleeps on the Sim's clock, then hands the
// connection back unchanged or refuses it.

import std.core
import std.net
import std.raw
import std.sync
import std.time

// FakeSec is the connector's state.
struct FakeSec {
    delay: i64
    refuse: bool
}

// FakeType is the type of FakeConnect, the connector's function.
type FakeType = fn(&FakeSec, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn

// FakeConnect sleeps s.delay ms, then returns conn (or refuses it, the
// connection staying the caller's).
FakeConnect(s: &FakeSec, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    io.Sleep(time.Millis(s.delay)) else {
        fail Canceled
    }
    if s.refuse {
        fail net.Refused
    }
    return (conn: conn, chosen: -1)
}

// FakeSecure is the net.Secure over s.
FakeSecure(s: &FakeSec): net.Secure from s {
    return net.Secure.Of[FakeSec](core.ErasedCall.Of[FakeSec, FakeType](s, FakeConnect))
}

// ScFetch fetches https://127.0.0.1:port/ok with connect ms and a fake
// connector of delay ms: CtGet's code.
ScFetch(io: Io, h: &Heap, port: str, connect: i64, delay: i64): int {
    l := net.Listen(io, port) else {
        return -60
    }
    n := raw.Atomic[u32].New(0)
    sec := FakeSec(delay: delay, refuse: false)
    var lim := DefaultClientLimits
    lim.connect = time.Millis(connect)
    lim.secure = some(FakeSecure(&sec))
    var c := Client.New(h, lim)
    var r := 0
    scope srv := io.Scope() {
        srv.go CtServe(io, &l, &n)
        var ub: [64]u8 = [0; 64]
        r = CtGet(&c, h, io, ScUrl(mut ub, port), "ok")
        srv.Cancel()
    }
    c.Close()
    l.Close()
    return r
}

// ScUrl is "https://" then port's address, then "/ok", in b.
ScUrl(b: mut [64]u8, addr: str): str from b {
    pre := "https://".AsBytes()
    a := addr.AsBytes()
    post := "/ok".AsBytes()
    var n := 0
    for x in pre {
        b[n] = x
        n += 1
    }
    for x in a {
        b[n] = x
        n += 1
    }
    for x in post {
        b[n] = x
        n += 1
    }
    return str.From(b[0..n]).Must()
}

ScConnect(io: Io, h: &Heap): bool {
    // A handshake inside the limit, then plain HTTP over the connection.
    fast := ScFetch(io, h, "127.0.0.1:7482", 30, 5)
    // A handshake that outlasts the connect limit.
    slow := ScFetch(io, h, "127.0.0.1:7483", 20, 50)
    return fast == 200 && slow == -6
}

test "The connect limit bounds the secure handshake: inside it the request is answered, past it the request is time.TimedOut" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), ScConnect))
    }
}

// ScServeOne accepts one connection and serves it through ServeConn with
// the fake acceptor.
ScServeOne(io: Io, l: &net.Listener, h: &Heap, o: &ServerOptions) {
    st := l.Accept(io) else {
        return
    }
    sem := sync.Semaphore.New(1)
    sem.Acquire(io, 1) else {
        st.Close()
        return
    }
    app := SwApp(n: 0)
    ServeConn(st.Conn(), h, io, o, &sem, &app, SwEcho)
}

// ScServe serves one connection whose acceptor fails or not, and sends a
// request: the reply's status, 0 when the connection ends first.
ScServe(io: Io, h: &Heap, port: str, refuse: bool): int {
    l := net.Listen(io, port) else {
        return -1
    }
    sec := FakeSec(delay: 2, refuse: refuse)
    var o := DefaultServerOptions
    o.secure = some(FakeSecure(&sec))
    var code := -1
    scope top := io.Scope() {
        top.go ScServeOne(io, &l, h, &o)
        code = ScClient(io, port)
        top.Cancel()
    }
    l.Close()
    return code
}

ScClient(io: Io, port: str): int {
    c := net.Dial(io, port) else {
        return -1
    }
    var code := 0
    if CtSend(io, &c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n") {
        code = SwStatus(io, &c)
    }
    c.Close()
    return code
}

ScServeCases(io: Io, h: &Heap): bool {
    ok := ScServe(io, h, "127.0.0.1:7484", false)
    refused := ScServe(io, h, "127.0.0.1:7485", true)
    return ok == 200 && refused == 0
}

test "ServeWith's secure slot runs the acceptor before the loop, and closes a connection whose handshake fails" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), ScServeCases))
    }
}
