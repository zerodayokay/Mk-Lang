// Why: Response.Flush's bytes on the wire must be exact for each framing.
// What: first and later flushes and Send for chunked, HTTP/1.0, no body,
// HEAD and a non-final status, and a failing Flush under ServeWith's loop.
// How: each case writes into an arena Text and compares the bytes whole.

import std.net

// FlStep runs FlushTo (or Send, when send) on res into a fresh Text and
// compares what it wrote with want; false on an error or a difference.
FlStep(a: &Arena, io: Io, res: mut Response, send: bool, want: str): bool {
    var t := Text.New(a)
    ok := FlWrite(io, res, mut t, send)
    return ok && t.Str() == want
}

// FlWrite runs FlushTo or Send on res into t.
FlWrite(io: Io, res: mut Response, t: mut Text, send: bool): bool {
    var w := t.Writer()
    if send {
        res.Send(io, mut w) else {
            return false
        }
        return true
    }
    res.FlushTo(io, mut w) else {
        return false
    }
    return true
}

// FlAdd appends s to res's body through its Writer.
FlAdd(res: mut Response, s: str) {
    var w := res.Writer()
    w.Str(s).Must()
}

test "Flush sends the head with chunked framing and the body so far, each later flush what was added, and Send the rest and the last chunk" (sys: &System) {
    a := sys.arena
    var res := Response.New(a)
    res.Header("X-A", "1").Must()
    res.Header("Content-Length", "99").Must()
    res.Header("Transfer-Encoding", "gzip").Must()
    res.Body("abc".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "HTTP/1.1 200 OK\r\nX-A: 1\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n"))
    FlAdd(mut res, "de")
    Assert(FlStep(a, sys.io, mut res, false, "2\r\nde\r\n"))
    Assert(FlStep(a, sys.io, mut res, false, ""))
    FlAdd(mut res, "fgh")
    Assert(FlStep(a, sys.io, mut res, true, "3\r\nfgh\r\n0\r\n\r\n"))
}

test "Flush to an HTTP/1.0 request sends no length and ends the body with the connection" (sys: &System) {
    a := sys.arena
    var res := Response.New(a)
    res.old = true
    res.Body("abc".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\nabc"))
    Assert(res.close)
    FlAdd(mut res, "de")
    Assert(FlStep(a, sys.io, mut res, true, "de"))
}

test "Flush of a 204 or a reply to HEAD sends the head alone, and Send nothing after it" (sys: &System) {
    a := sys.arena
    var res := Response.New(a)
    res.Status(204)
    res.Body("ignored".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "HTTP/1.1 204 No Content\r\n\r\n"))
    Assert(FlStep(a, sys.io, mut res, true, ""))
    var hr := Response.New(a)
    hr.head = true
    hr.Body("abc".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut hr, false, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"))
    Assert(FlStep(a, sys.io, mut hr, true, ""))
}

test "Flush with a status that is not final fails with Status and sends nothing; a Body set after a Flush is sent whole" (sys: &System) {
    a := sys.arena
    var res := Response.New(a)
    res.Status(101)
    Assert(!FlStep(a, sys.io, mut res, false, ""))
    Assert(!res.fl.head)
    res.Status(200)
    res.Body("abcdef".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n6\r\nabcdef\r\n"))
    res.Body("xy".AsBytes()).Must()
    FlAdd(mut res, "tail")
    Assert(FlStep(a, sys.io, mut res, true, "2\r\nxy\r\n4\r\ntail\r\n0\r\n\r\n"))
}

test "Body, Flush, a longer Body, Flush, a shorter Body: chunks of 4, 10 and 2 bytes, then the last chunk" (sys: &System) {
    a := sys.arena
    var res := Response.New(a)
    res.Body("AAAA".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nAAAA\r\n"))
    res.Body("BBBBBBBBBB".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, false, "a\r\nBBBBBBBBBB\r\n"))
    res.Body("CC".AsBytes()).Must()
    Assert(FlStep(a, sys.io, mut res, true, "2\r\nCC\r\n0\r\n\r\n"))
}

// FcCut flushes inside a scope it has cancelled, so the Flush fails, then
// ignores the error and returns as if all went well.
FcCut(c: &SwApp, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    try res.Body("abc".AsBytes())
    scope s := io.Scope() {
        s.Cancel()
        res.Flush(io, link) else {}
    }
}

// FcServeOne serves one connection with FcCut.
FcServeOne(io: Io, l: &net.Listener, h: &Heap) {
    st := l.Accept(io) else {
        return
    }
    app := SwApp(n: 0)
    ServeOwned(st.Conn(), h, io, &DefaultServerOptions, &app, FcCut)
}

// FcCase sends two pipelined requests to a handler whose Flush fails: the
// bytes that come back before the connection ends (the server must send
// nothing after a failed Flush, and never answer the second request).
FcCase(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7486") else {
        return false
    }
    var got := -1
    scope top := io.Scope() {
        top.go FcServeOne(io, &l, h)
        got = FcClient(io)
    }
    l.Close()
    return got == 0
}

FcClient(io: Io): int {
    c := net.Dial(io, "127.0.0.1:7486") else {
        return -1
    }
    var n := 0
    if CtSend(io, &c, "GET /a HTTP/1.1\r\nHost: x\r\n\r\nGET /b HTTP/1.1\r\nHost: x\r\n\r\n") {
        var b: [256]u8 = [0; 256]
        for {
            k := c.Read(io, mut b[..]) else {
                break
            }
            if k == 0 {
                break
            }
            n += k
        }
    }
    c.Close()
    return n
}

test "a Flush that fails marks the response failed: the server sends nothing more on the connection and never answers a pipelined request" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), FcCase))
    }
}
