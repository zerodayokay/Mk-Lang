// Why: kits/web is written against std.http's Request and Response alone,
// so it must run unchanged when the server speaks HTTP/2; these tests prove
// it on a real loopback connection.
// What: tests that serve the example's routes through http.ServeOn with
// h2c on, and call them with a client that sends HTTP/2 with prior
// knowledge, and one that speaks HTTP/1.1 to the same listener.
// How: Served adapts Handle to ServeOn's handler, which also takes the
// Link; each test runs the server in a task of a scope it cancels when the
// requests are done.

import std.http
import std.net
import std.time
import std.url

// Served is Handle with the Link ServeOn passes beside the response, which
// the routes do not use.
Served(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = link
    try Handle(c, a, io, req, res)
}

// ServeH2c serves app on l until the scope is cancelled, with cleartext
// HTTP/2 allowed beside HTTP/1.1.
ServeH2c(h: &Heap, io: Io, l: &net.Listener, app: &App) {
    var o := http.DefaultServerOptions
    o.h2c = true
    http.ServeOn(h, io, l, &o, app, Served) else {}
}

// Call sends one request to the example's server on port, reads the whole
// reply and returns it with the version it came over.
Call(a: &Arena, io: Io, c: &http.Client, port: int, method: str, path: str, body: str): http.Error!(version: http.HttpVersion, status: int, headers: []http.Header, body: str) from a {
    text := try a.Fmt("http://127.0.0.1:{port}{path}", (port, path))
    u := url.Parse(a, text, none) else { fail http.BadUrl }
    headers := [http.Header(name: "Content-Type", value: "application/json")]
    req := http.ClientRequest(method: method, url: &u, headers: headers[..], body: body.AsBytes())
    var x := try c.Do(a, io, &req)
    onerr x.Abandon()
    version := x.Version()
    status := x.status
    var copied := List[http.Header].New(a)
    for h in x.headers {
        try copied.Push(http.Header(name: try a.Dup(h.name), value: try a.Dup(h.value)))
    }
    sent := copied.Done()[..]
    var buf: [4096]u8 = [0; 4096]
    var into: [4096]u8 = [0; 4096]
    n := ReadBody(mut x, mut buf[..], mut into[..]) else {
        fail http.BadRequest(offset: 0)
    }
    got := try a.Dup(str.From(into[..n]) else { "" })
    try x.Close(io)
    return (version: version, status: status, headers: sent, body: got)
}

// ReadBody reads x's body into into, through buf, and returns its length.
ReadBody(x: mut http.Exchange, buf: mut []u8, into: mut []u8): ReadError!int {
    var r := x.Body(buf)
    return try r.ReadAll(into)
}

// Field is the value of the reply field named name, compared exactly, so a
// name that was not lower-cased on the way is not found.
Field(headers: []http.Header, name: str): ?str from headers {
    for h in headers {
        if h.name == name {
            return some(h.value)
        }
    }
    return none
}

test "the routes answer over HTTP/2 cleartext through ServeOn, with the middleware's field lower-cased" (sys: &System) {
    io := sys.io
    var l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    port := l.Port()
    app := App(served_by: "mk-web")
    var lim := http.DefaultClientLimits
    lim.h2c = true
    c := http.Client.New(sys.heap, lim)
    defer c.Close()
    var ok := false
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(20))
        s.go ServeH2c(sys.heap, io, &l, &app)
        posted := Call(sys.arena, io, &c, port, "POST", "/items", "{\"name\": \"nut\", \"qty\": 3}") else {
            s.Cancel()
            Assert(false)
            return
        }
        greeted := Call(sys.arena, io, &c, port, "GET", "/greet?name=h2", "") else {
            s.Cancel()
            Assert(false)
            return
        }
        refused := Call(sys.arena, io, &c, port, "POST", "/items", "{\"name\": \"\", \"qty\": 3}") else {
            s.Cancel()
            Assert(false)
            return
        }
        s.Cancel()
        Assert(posted.version == http.HttpVersion.Http2)
        Assert(posted.status == 201)
        Assert(posted.body == "{\"id\":1,\"name\":\"nut\",\"qty\":3}")
        Assert((Field(posted.headers, "x-served-by") else { "" }) == "mk-web")
        Assert((Field(posted.headers, "content-type") else { "" }) == "application/json")
        Assert(Field(posted.headers, "X-Served-By").IsNone())
        Assert(greeted.version == http.HttpVersion.Http2)
        Assert(greeted.status == 200)
        Assert(greeted.body == "{\"text\":\"hello, h2\"}")
        Assert(refused.status == 422)
        ok = true
    }
    Assert(ok)
}

test "the same h2c listener still answers an HTTP/1.1 client" (sys: &System) {
    io := sys.io
    var l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    port := l.Port()
    app := App(served_by: "mk-web")
    c := http.Client.New(sys.heap, http.DefaultClientLimits)
    defer c.Close()
    var ok := false
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(20))
        s.go ServeH2c(sys.heap, io, &l, &app)
        got := Call(sys.arena, io, &c, port, "GET", "/items/7", "") else {
            s.Cancel()
            Assert(false)
            return
        }
        s.Cancel()
        Assert(got.version == http.HttpVersion.Http11)
        Assert(got.status == 200)
        Assert(got.body == "{\"id\":7,\"name\":\"item 7\",\"qty\":1}")
        Assert((Field(got.headers, "X-Served-By") else { "" }) == "mk-web")
        ok = true
    }
    Assert(ok)
}
