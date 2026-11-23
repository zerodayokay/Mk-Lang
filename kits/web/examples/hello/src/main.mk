// Why: the smallest complete server on kits/web, showing routes, a
// middleware, path and query parameters, JSON bodies and validation.
//
// What: a small JSON API:
//   GET  /                 "hello from web"
//   GET  /greet?name=N     {"text":"hello, N"}
//   GET  /items/{id}       {"id":id,"name":"item id","qty":1}
//   POST /items            {"name":"...","qty":n} checked, echoed with id 1
// Usage: hello [addr], default 127.0.0.1:8080.
//
// How: a `const` route table run through web.Run, with one middleware that
// sets `X-Served-By: mk-web` on every reply.

import std.http
import std.json
import web

/// The server's state: nothing yet but the name the middleware writes.
struct App {
    served_by: str
}

/// A new item, as a client posts it.
struct NewItem {
    export name: str
    export qty: i64
}

/// An item, as the server answers it.
struct Item {
    export id: i64
    export name: str
    export qty: i64
}

/// A greeting.
struct Greeting {
    export text: str
}

gen json.Codec(NewItem)
gen json.Codec(Item)
gen json.Codec(Greeting)
gen web.Body(NewItem)
gen web.Validate(NewItem, "name: nonempty, max 64; qty: min 0, max 1000")

Root(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var w := res.Writer()
    try w.Str("hello from web")
}

Greet(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    name := try web.Query(a, req, "name") else {
        "world"
    }
    g := Greeting(text: try a.Fmt("hello, {name}", (name,)))
    try web.Reply(res, 200, &g)
}

GetItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try web.ParamInt(req, "id")
    item := Item(id: id, name: try a.Fmt("item {id}", (id,)), qty: 1)
    try web.Reply(res, 200, &item)
}

PostItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    n := try NewItem.FromRequest(a, req)
    try n.Validate()
    item := Item(id: 1, name: n.name, qty: n.qty)
    try web.Reply(res, 201, &item)
}

/// Marks every reply with the server's name.
ServedBy(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!bool {
    try res.Header("X-Served-By", c.served_by)
    return true
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/", handler: Root),
    http.Route[App](method: "GET", pattern: "/greet", handler: Greet),
    http.Route[App](method: "GET", pattern: "/items/{id}", handler: GetItem),
    http.Route[App](method: "POST", pattern: "/items", handler: PostItem),
]

const Middleware: []web.Middleware[App] = [ServedBy]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try web.Run(a, io, Middleware, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    addr := if sys.args.Len() > 1 { sys.args[1] } else { "127.0.0.1:8080" }
    app := App(served_by: "mk-web")
    try sys.Print("serving on {addr}\n", (addr,))
    var out := sys.Stdout()
    try out.Flush()
    try http.Serve(sys, addr, 1_000_000, &app, Handle)
}
