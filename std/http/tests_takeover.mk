// Why: after Link.Takeover the server must not touch the connection.
// What: Link.Takeover under ServeWith's loop.
// How: a simulated Io; the handler takes the connection and the test checks
// the bytes. Run by mk test --std=std.http.

import std.net

// TkTake takes the connection, writes "taken:" and rest on it, and
// abandons it; a second Takeover must be none.
TkTake(c: &SwApp, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    t := link.Takeover() else {
        fail Status(code: 500)
    }
    Taken(conn, rest) := t
    TkWrite(io, &conn, rest)
    conn.Abandon()
    if again := link.Takeover() {
        again.Abandon()
        Panic("a second Takeover gave the connection again")
    }
}

// TkWrite writes "taken:" and rest on conn.
TkWrite(io: Io, conn: &net.Conn, rest: []u8) {
    conn.WriteAll(io, "taken:".AsBytes()) else {
        return
    }
    conn.WriteAll(io, rest) else {
        return
    }
}

// TkServeOne serves one connection with TkTake.
TkServeOne(io: Io, l: &net.Listener, h: &Heap) {
    st := l.Accept(io) else {
        return
    }
    app := SwApp(n: 0)
    ServeOwned(st.Conn(), h, io, &DefaultServerOptions, &app, TkTake)
}

// TkTakeCase sends a request with bytes pipelined after it: the server
// answers nothing of its own, only what the handler wrote on the taken
// connection, and then the connection ends.
TkTakeCase(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7481") else {
        return false
    }
    var ok := false
    scope top := io.Scope() {
        top.go TkServeOne(io, &l, h)
        ok = TkClient(io)
    }
    l.Close()
    return ok
}

TkClient(io: Io): bool {
    c := net.Dial(io, "127.0.0.1:7481") else {
        return false
    }
    var ok := false
    if CtSend(io, &c, "GET /ws HTTP/1.1\r\nHost: x\r\n\r\nNEXT") {
        var b: [64]u8 = [0; 64]
        var n := 0
        for n < b.Len() {
            k := c.Read(io, mut b[n..b.Len()]) else {
                break
            }
            if k == 0 {
                break
            }
            n += k
        }
        ok = CtSame(b[0..n], "taken:NEXT")
    }
    c.Close()
    return ok
}

test "A handler that takes the connection gets the bytes after the request, and the server sends nothing of its own" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), TkTakeCase))
    }
}
