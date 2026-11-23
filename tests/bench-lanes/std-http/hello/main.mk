// hello, Mk: the Mk side of the idiomatic table's http row (the
// tests/bench/idiomatic/http/http.json's task): std.http.Serve answering
// GET / with 200 OK, Content-Type: text/plain and the 14-byte body
// "Hello, World!\n" (Send adds Content-Length) over HTTP/1.1 keep-alive,
// one task per connection. Usage: hello <port>. Serves until killed.
//
//
import std.http

struct App {}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Content-Type", "text/plain")
    try res.Body(b"Hello, World!\n")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/", handler: Hello),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    if sys.args.Len() != 2 {
        var e := sys.Stderr()
        Print(mut e, "usage: hello <port>\n") else {}
        fail http.Status(code: 2)
    }
    addr := try sys.arena.Fmt("127.0.0.1:{port}", (port: sys.args[1]))
    app := App()
    try http.Serve(sys, addr, 65536, &app, Handle)
}
