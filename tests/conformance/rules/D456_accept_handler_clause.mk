// verdict: ok
// rule: D-456
// note: http.Response is a read-only view, so handlers calling Header and Body need no `*res into res` and fit http.Route and http.Serve directly
import std.http

struct App {
    greeting: str
}

Greet(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("content-type", "text/plain")
    try res.Body(b"hello\n")
}

Gone(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    fail http.Status(code: 410)
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/hello", handler: Greet),
    http.Route[App](method: "GET", pattern: "/old", handler: Gone),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

Run(sys: &System): http.Error!void {
    app := App(greeting: "hello")
    try http.Serve(sys, "127.0.0.1:0", 1_000, &app, Handle)
}

export Main(sys: &System): void {
    _ = Routes.Len()
}
