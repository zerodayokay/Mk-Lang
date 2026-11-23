// Why: web's routing, middleware, path and query parameters and Reply must
// behave as documented.
// What: tests of Run, Param, ParamInt, Query, QueryInt and Reply. Body and
// Validate are generators, which run only in a module that imports web, so
// their tests are in examples/hello.
// How: builds requests by hand and reads what the response sends.

import std.http
import std.json

// TwApp is the tests' application state.
struct TwApp {
    greeting: str
}

const TwNoHeaders: []http.Header = []

// TwItem is a handler: "<greeting> <id>" for /items/{id}.
TwItem(c: &TwApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try ParamInt(req, "id")
    var w := res.Writer()
    try Print(mut w, "{g} {id}", (g: c.greeting, id))
}

// TwHello answers "hello" at /.
TwHello(c: &TwApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var w := res.Writer()
    try w.Str("hello")
}

const TwRoutes: []http.Route[TwApp] = [
    http.Route[TwApp](method: "GET", pattern: "/", handler: TwHello),
    http.Route[TwApp](method: "GET", pattern: "/items/{id}", handler: TwItem),
]

// TwFirst and TwSecond each add an X-Order field; TwGuard answers 401 and
// stops when the request has an X-Stop field.
TwFirst(c: &TwApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!bool {
    try res.Header("X-Order", "first")
    return true
}

TwSecond(c: &TwApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!bool {
    try res.Header("X-Order", "second")
    return true
}

TwGuard(c: &TwApp, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!bool {
    if req.Header("X-Stop").IsSome() {
        res.Status(401)
        return false
    }
    return true
}

const TwMiddleware: []Middleware[TwApp] = [TwFirst, TwGuard, TwSecond]

const TwNone: []Middleware[TwApp] = []

// TwSend is what res sends, as text.
TwSend(a: &Arena, io: Io, res: &http.Response): http.Error!str from a {
    var t := Text.New(a)
    var w := t.Writer()
    try res.Send(io, mut w)
    return try a.Dup(t.Str())
}

// TwReq is a request with no fields and no body.
TwReq(method: str, path: str, query: str): http.Request from method, path, query {
    return http.Request(method: method, path: path, query: query, headers: TwNoHeaders, params: TwNoHeaders, body: b"")
}

// TwStatus is the code of a Status failure, else -1.
TwStatus(e: http.Error): int {
    match e {
        http.Status(code) {
            return code
        }
        _ {
            return -1
        }
    }
}

test "Run dispatches to the route and its path parameter" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    app := TwApp(greeting: "item")
    req := TwReq("GET", "/items/42", "")
    var res := http.Response.New(&ar)
    try Run(&ar, sys.io, TwNone, TwRoutes, &app, &req, mut res)
    Assert(try TwSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\nitem 42")
}

test "Run passes Dispatch's 404 and 405 through" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    app := TwApp(greeting: "item")
    missing := TwReq("GET", "/nothing", "")
    var res := http.Response.New(&ar)
    Run(&ar, sys.io, TwNone, TwRoutes, &app, &missing, mut res) else e {
        Assert(TwStatus(e) == 404)
        post := TwReq("POST", "/", "")
        var res2 := http.Response.New(&ar)
        Run(&ar, sys.io, TwNone, TwRoutes, &app, &post, mut res2) else e2 {
            Assert(TwStatus(e2) == 405)
            return
        }
        Assert(false)
    }
    Assert(false)
}

test "a path parameter that is not a number is a 400" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    app := TwApp(greeting: "item")
    req := TwReq("GET", "/items/x7", "")
    var res := http.Response.New(&ar)
    Run(&ar, sys.io, TwNone, TwRoutes, &app, &req, mut res) else e {
        Assert(TwStatus(e) == 400)
        return
    }
    Assert(false)
}

test "Param fails 400 for a parameter the route does not have" {
    req := TwReq("GET", "/", "")
    _ = Param(&req, "id") else e {
        Assert(TwStatus(e) == 400)
        return
    }
    Assert(false)
}

test "middleware runs in order before the route" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    app := TwApp(greeting: "item")
    req := TwReq("GET", "/", "")
    var res := http.Response.New(&ar)
    try Run(&ar, sys.io, TwMiddleware, TwRoutes, &app, &req, mut res)
    Assert(try TwSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nX-Order: first\r\nX-Order: second\r\nContent-Length: 5\r\n\r\nhello")
}

test "a middleware that returns false ends the request" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    app := TwApp(greeting: "item")
    stop := [http.Header(name: "X-Stop", value: "1")]
    req := http.Request(method: "GET", path: "/", query: "", headers: stop[..], params: TwNoHeaders, body: b"")
    var res := http.Response.New(&ar)
    try Run(&ar, sys.io, TwMiddleware, TwRoutes, &app, &req, mut res)
    // TwSecond and the route did not run: no second field, no body.
    Assert(try TwSend(&ar, sys.io, &res) == "HTTP/1.1 401 Unauthorized\r\nX-Order: first\r\nContent-Length: 0\r\n\r\n")
}

test "Query decodes plus and percent escapes and finds the first pair" (sys: &System) {
    req := TwReq("GET", "/", "q=two+words%21&tag=x&q=later")
    q := try Query(sys.arena, &req, "q")
    Assert(q.Must() == "two words!")
    tag := try Query(sys.arena, &req, "tag")
    Assert(tag.Must() == "x")
}

test "Query gives none for a missing name and empty for a bare key" (sys: &System) {
    req := TwReq("GET", "/", "a=1&&b")
    missing := try Query(sys.arena, &req, "c")
    Assert(missing.IsNone())
    bare := try Query(sys.arena, &req, "b")
    Assert(bare.Must() == "")
}

test "Query fails 400 on a malformed escape" (sys: &System) {
    req := TwReq("GET", "/", "q=%G1")
    _ = Query(sys.arena, &req, "q") else e {
        Assert(TwStatus(e) == 400)
        return
    }
    Assert(false)
}

test "Query skips another pair whose name does not decode" (sys: &System) {
    req := TwReq("GET", "/", "bad%G1=x&name=ok")
    name := try Query(sys.arena, &req, "name")
    Assert(name.Must() == "ok")
    missing := try Query(sys.arena, &req, "bad")
    Assert(missing.IsNone())
}

test "QueryInt reads a number, none when absent, 400 when not a number" (sys: &System) {
    req := TwReq("GET", "/", "limit=25&page=two")
    n := try QueryInt(sys.arena, &req, "limit")
    Assert(n.Must() == 25)
    none_ := try QueryInt(sys.arena, &req, "offset")
    Assert(none_.IsNone())
    _ = QueryInt(sys.arena, &req, "page") else e {
        Assert(TwStatus(e) == 400)
        return
    }
    Assert(false)
}

// TwOut is a reply body with a codec.
struct TwOut {
    export id: i64
    export name: str
}

gen json.Codec(TwOut)

test "Reply writes the status, the JSON type and the codec's text" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := http.Response.New(&ar)
    out := TwOut(id: 7, name: "bolt")
    try Reply(mut res, 201, &out)
    Assert(try TwSend(&ar, sys.io, &res) == "HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: 22\r\n\r\n{\"id\":7,\"name\":\"bolt\"}")
}
