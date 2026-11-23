// verdict: reject E0608
// rule: D-456
// note: a function that declares `*res into res` has that flow as part of its type: a fn type without the clause does not cover it (E0608). D456_reject_handler_without_clause is the E0604 unit for a caller missing the clause on a mut view method
import std.http

struct App {
    greeting: str
}

struct Hook {
    path: str
    handler: fn(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void
}

Greet(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void *res into res {
    try res.Header("content-type", "text/plain")
    try res.Body(b"hello\n")
}

export Main(sys: &System): void {
    h := Hook(path: "/hello", handler: Greet) // <- E0608
    _ = h.path.Len()
}
