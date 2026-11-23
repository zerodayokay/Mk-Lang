# web

`web` is a small web framework on top of [`std.http`](../../doc/std/http.md): middleware before the routes, path and query parameters, JSON request and response bodies, and generated validation of request structs. It is plain Mk on std alone, so it needs no `unsafe` grant.

## Depend on it

`web` is a package of its own, in `kits/web` of the Mk repository. Name it in your manifest's `deps` with the path to a checkout, relative to the manifest ([dependencies](../../doc/packages.md)):

```mk
const Package = pkg.Package(
    name: "shop",
    bins: [pkg.Bin(name: "shop", root: "src", subsystem: "console")],
    deps: [pkg.Dep(name: "web", path: "../Mk-Lang/kits/web")],
)
```

Then `import web` in any module of the package.

## Routes and middleware

The routes are a `const []http.Route[C]` table, as in `std.http`, over your application's state `C`. A middleware is a `web.Middleware[C]`: a plain function with a handler's parameters that returns `true` to go on, or `false` after it has answered the request itself (a 401, say). `http.Serve`'s one handler calls `web.Run`, which runs the middleware in order and then the route that `http.Dispatch` picks:

```mk
const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/items/{id}", handler: GetItem),
    http.Route[App](method: "POST", pattern: "/items", handler: PostItem),
]

const Middleware: []web.Middleware[App] = [ServedBy]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try web.Run(a, io, Middleware, Routes, c, req, res)
}
```

A middleware that fails ends the request there, and `http.Serve` answers the error.

## Parameters

| Call | Gives | Fails with |
|---|---|---|
| `web.Param(req, "id")` | The `{id}` segment of the route's pattern, as `str` | `Status(400)` when the route has no such parameter |
| `web.ParamInt(req, "id")` | The same segment as an `i64` | `Status(400)` when it is missing or not a number |
| `web.Query(a, req, "name")` | The decoded value of the first query pair named `name`, or `none` | `Status(400)` on a bad escape in that pair |
| `web.QueryInt(a, req, "limit")` | The same value as an `i64`, or `none` | `Status(400)` when it is present but not a number |

`Query` decodes as browsers encode a form: `+` is a space and `%XX` a byte. `web.Unescape` is that decoder alone.

## JSON bodies and validation

```mk
struct NewItem {
    export name: str
    export qty: i64
}

gen json.Codec(NewItem)
gen web.Body(NewItem)
gen web.Validate(NewItem, "name: nonempty, max 64; qty: min 0, max 1000")

PostItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    n := try NewItem.FromRequest(a, req) // the body, read as JSON
    try n.Validate()                     // 422 with the first broken rule
    try web.Reply(res, 201, &n)          // the value, written as JSON
}
```

- `gen web.Body(T)` writes `T.FromRequest`, which reads the request's body as JSON into `T`. `T` needs its `gen json.Codec(T)` line.
- `gen web.Validate(T, rules)` writes `T.Problem`, the message of the first broken rule or `none`, and `T.Validate`, which fails with `Status(422)` when there is a problem. The rules are `field: rule, rule; field: rule`, with `nonempty`, `min N` and `max N`. On a `str` field they bound its length in bytes, on an integer field its value. A rule the struct's fields cannot take is a compile-time error at the `gen` line (E0903).
- `web.Reply(res, code, &v)` answers with the status, `Content-Type: application/json` and `v` written by its codec.

## A complete server

[`examples/hello`](examples/hello/src/main.mk) is a small JSON API with all of the above: a middleware, a path parameter, a query parameter, a JSON body and validation. From `kits/web/examples/hello`, `mk run` starts it on `127.0.0.1:8080`, and `mk test` runs its tests.
