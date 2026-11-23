// http, Mk through http.ServeWith: the Mk side of the idiomatic table's
// http row (tests/bench/idiomatic/http/http.json) with the 1.2 server,
// ServeWith and DefaultServerOptions (10,000 connections, keep-alive),
// answering GET / with 200 OK, Content-Type: text/plain and the 14-byte
// body "Hello, World!\n" (Send adds Content-Length), one task and one arena
// per connection. It prices ServeWith's per-read and per-write deadline
// scopes and the connection cell's lock against Serve's loop (the row's
// own Mk column) and Go's net/http server. README.md says how it runs.
// Usage: http <port>. Serves 127.0.0.1:<port> until killed.
import std.http

struct App {}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Content-Type", "text/plain")
    try res.Body(b"Hello, World!\n")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/", handler: Hello),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    if sys.args.Len() != 2 {
        var e := sys.Stderr()
        Print(mut e, "usage: http <port>\n") else {}
        fail http.Status(code: 2)
    }
    addr := try sys.arena.Fmt("127.0.0.1:{port}", (port: sys.args[1]))
    app := App()
    try http.ServeWith(sys, addr, &http.DefaultServerOptions, &app, Handle)
}
