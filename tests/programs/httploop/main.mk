// std.http's client and server, over loopback in one process:
// http.ServeWith with server options answers in the main task while a task drives one
// http.Client: Fetch with a query and with a body, a redirect followed, a streamed body read
// through Client.Do's Exchange, a missing path, a head over the server's header_bytes, and the
// same client shared by two tasks. The task cancels the scope when it is done, which
// ends ServeWith. Ports are never printed.
import std.http
import std.net
import std.time
import std.url

// NoHeaders and NoBody are a request's empty header list and body.
const NoHeaders: []http.Header = []
const NoBody: []u8 = []

// NotYet is a Shared before its task ran.
const NotYet = Shared(status: 0, body: false, failure: none)

// App is the server's context.
struct App {
    big: int
}

// Seen is what the client task found, printed by Main after the join.
struct Seen {
    export hello: int
    export hello_body: bool
    export echo: int
    export echo_body: bool
    export redirect: int
    export redirect_body: bool
    export streamed: int
    export streamed_bytes: i64
    export missing: int
    export first: Shared
    export second: Shared
    export large: int
    export failure: ?http.Error
}

// Shared is what one of the two tasks that share the client found.
struct Shared {
    export status: int
    export body: bool
    export failure: ?http.Error
}

// Handle answers /hello with the query, /echo with the body, /redirect with a 302 to /hello,
// /big with c.big bytes, and anything else with 404.
Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = link
    match req.path {
        "/hello" {
            text := try a.Fmt("hello {q}", (q: req.query))
            try res.Header("Content-Type", "text/plain")
            try res.Body(text.AsBytes())
        }
        "/echo" {
            n := try a.Fmt("{n}", (n: req.body.Len()))
            try res.Header("X-Length", n)
            try res.Body(req.body)
        }
        "/redirect" {
            res.Status(302)
            try res.Header("Location", "/hello?from=redirect")
        }
        "/big" {
            data := try a.Slice[u8](c.big)
            for i in 0..c.big {
                data[i] = b'x'
            }
            try res.Body(data[..])
        }
        _ {
            res.Status(404)
        }
    }
}

// Get fetches path from the server on port with client, in a.
Get(a: &Arena, io: Io, client: &http.Client, port: int, path: str, headers: []http.Header, body: []u8): http.Error!http.Reply from a {
    // url.Parse stores its input into its arena: the URL lives in a
    // scratch arena, so Get's signature states no store of a into a.
    var scratch := try a.Sub()
    defer scratch.Free()
    text := try scratch.Fmt("http://127.0.0.1:{port}{path}", (port: port, path: path))
    u := url.Parse(&scratch, text, none) else {
        fail http.BadUrl
    }
    method := if body.Len() > 0 { "POST" } else { "GET" }
    req := http.ClientRequest(method: method, url: &u, headers: headers, body: body)
    return try client.Fetch(a, io, &req)
}

// Streamed reads /big through Client.Do's Exchange and counts the body's bytes.
Streamed(h: &Heap, io: Io, client: &http.Client, port: int): http.Error!(status: int, bytes: i64) {
    var a := try Arena.New(h)
    defer a.Free()
    text := try a.Fmt("http://127.0.0.1:{port}/big", (port: port))
    u := url.Parse(&a, text, none) else {
        fail http.BadUrl
    }
    req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: NoBody)
    var ex := try client.Do(&a, io, &req)
    status := ex.status
    total := Drain(mut ex) else e {
        ex.Abandon()
        fail e
    }
    ex.Close(io) else e {
        ex.Abandon()
        fail e
    }
    return (status: status, bytes: total)
}

// Drain reads the body of ex to its end, 4 KiB at a time, and returns its length.
Drain(ex: mut http.Exchange): http.Error!i64 {
    var buf: [1_024]u8 = [0; 1_024]
    var chunk: [4_096]u8 = [0; 4_096]
    var r := ex.Body(mut buf[..])
    var total: i64 = 0
    for {
        n := try r.Read(mut chunk[..])
        if n == 0 {
            return total
        }
        total += i64(n)
    }
}

// Ready fetches /hello until the server answers, for up to 5 s, and returns the reply.
Ready(a: &Arena, io: Io, client: &http.Client, port: int): http.Error!http.Reply from a {
    for _ in 0..500 {
        reply := Get(a, io, client, port, "/hello?name=mk", NoHeaders, NoBody) else e {
            match e {
                net.Refused {
                    io.Sleep(time.Millis(10)) else {
                        fail Canceled
                    }
                    continue
                }
                _ {
                    fail e
                }
            }
        }
        return reply
    }
    fail net.Refused
}

// Visit drives the client against the server on port, keeps what it saw in seen, and cancels s
// when it is done, which ends the server.
Visit(io: Io, h: &Heap, port: int, seen: mut Seen, s: &Scope) {
    defer s.Cancel()
    var a := Arena.New(h) else {
        seen.failure = some(OutOfMemory)
        return
    }
    defer a.Free()
    client := http.Client.New(h, http.DefaultClientLimits)
    defer client.Close()
    Steps(&a, h, io, &client, port, seen) else e {
        seen.failure = some(e)
    }
}

// Is reports whether body holds exactly the bytes of text.
Is(body: []u8, text: str): bool {
    got := str.From(body) else {
        return false
    }
    return got == text
}

// Steps runs every request of Visit in order.
Steps(a: &Arena, h: &Heap, io: Io, client: &http.Client, port: int, seen: mut Seen): http.Error!void {
    hello := try Ready(a, io, client, port)
    seen.hello = hello.status
    seen.hello_body = Is(hello.body, "hello name=mk")
    echo := try Get(a, io, client, port, "/echo", NoHeaders, "a body sent with POST"[..].AsBytes())
    seen.echo = echo.status
    seen.echo_body = Is(echo.body, "a body sent with POST")
    redirect := try Get(a, io, client, port, "/redirect", NoHeaders, NoBody)
    seen.redirect = redirect.status
    seen.redirect_body = Is(redirect.body, "hello from=redirect")
    (status, bytes) := try Streamed(h, io, client, port)
    seen.streamed = status
    seen.streamed_bytes = bytes
    missing := try Get(a, io, client, port, "/missing", NoHeaders, NoBody)
    seen.missing = missing.status
    scope t := io.Scope() {
        t.go Fetcher(io, h, client, port, "/hello?task=1", "hello task=1", mut seen.first)
        t.go Fetcher(io, h, client, port, "/hello?task=2", "hello task=2", mut seen.second)
    }
    pad := try a.Slice[u8](2_000)
    for i in 0..pad.Len() {
        pad[i] = b'p'
    }
    value := str.From(pad[..]) else {
        fail http.BadUrl
    }
    headers := [http.Header(name: "X-Pad", value: value)]
    large := try Get(a, io, client, port, "/hello", headers[..], NoBody)
    seen.large = large.status
}

// Fetcher fetches path with the shared client in an arena of its own and keeps the status, whether
// the body was want, and the failure, in got.
Fetcher(io: Io, h: &Heap, client: &http.Client, port: int, path: str, want: str, got: mut Shared) {
    var a := Arena.New(h) else {
        got.failure = some(OutOfMemory)
        return
    }
    defer a.Free()
    reply := Get(&a, io, client, port, path, NoHeaders, NoBody) else e {
        got.failure = some(e)
        return
    }
    got.status = reply.status
    got.body = Is(reply.body, want)
}

export Main(sys: &System): WriteError!void {
    io := sys.io
    probe := net.Listen(io, "127.0.0.1:0") else e {
        try sys.Print("listen: {e}\n", (e: e))
        return
    }
    port := probe.Port()
    probe.Close()
    addr := sys.arena.Fmt("127.0.0.1:{port}", (port: port)) else {
        return
    }
    var o := http.DefaultServerOptions
    o.connections = 8
    o.header_bytes = 1_024
    app := App(big: 20_000)
    var seen := Seen(hello: 0, hello_body: false, echo: 0, echo_body: false, redirect: 0, redirect_body: false, streamed: 0, streamed_bytes: 0, missing: 0, first: NotYet, second: NotYet, large: 0, failure: none)
    var served := "ended"
    scope s := io.Scope() {
        s.go Visit(io, sys.heap, port, mut seen, &s)
        http.ServeWith(sys, addr, &o, &app, Handle) else e {
            served = sys.arena.Fmt("{e}", (e: e)) else {
                "?"
            }
        }
    }
    if f := seen.failure {
        try sys.Print("client: {f}\n", (f: f))
    }
    try sys.Print("fetch with a query: {seen.hello}, body as sent {seen.hello_body}\n", (seen: seen))
    try sys.Print("fetch with a body: {seen.echo}, echoed {seen.echo_body}\n", (seen: seen))
    try sys.Print("redirect followed: {seen.redirect}, body of the target {seen.redirect_body}\n", (seen: seen))
    try sys.Print("streamed through an exchange: {seen.streamed}, {seen.streamed_bytes} bytes\n", (seen: seen))
    try sys.Print("missing path: {seen.missing}\n", (seen: seen))
    for got in [seen.first, seen.second] {
        if f := got.failure {
            try sys.Print("a task sharing the client: {f}\n", (f: f))
        }
        try sys.Print("a task sharing the client: {got.status}, body as sent {got.body}\n", (got: got))
    }
    try sys.Print("head over the limit: {seen.large}\n", (seen: seen))
    try sys.Print("server: {served}\n", (served: served))
}
