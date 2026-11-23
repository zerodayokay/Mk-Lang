// Why: a server built on std.http's route table needs a place to run shared
// steps (headers, logging, auth) before every route; this file is the
// framework's entry point.
//
// What: Middleware[C], a function run before the route's handler, and Run,
// which runs the middleware and then the route.
//
// How: the application writes a `const` table of `http.Route[C]` and,
// optionally, a `const` list of middleware; its one handler for `http.Serve`
// calls `web.Run`, which runs the middleware in order and then
// `http.Dispatch`. Path parameters, query parameters and JSON bodies are read
// through params.mk, query.mk and body.mk; `gen web.Validate(T, rules)`
// writes a request struct's checks (validate.mk).

import std.http

/// One middleware: a plain function over the application's state C, run
/// before the route's handler with the same request and response. It
/// returns true to go on to the next middleware and then the route, or
/// false when it has answered the request itself (its status and body are
/// in `res`). Its failure ends the request as a handler's does.
export type Middleware[C] = fn(c: &C, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!bool

/// Runs each of `mws` in order and, when every one returns true, the route
/// of `routes` that `http.Dispatch` picks (404 and 405 as Dispatch fails
/// them). A middleware that returns false or fails ends the request there:
/// the ones after it and the route do not run. O(middleware) calls plus
/// Dispatch's scan.
export Run[C](a: &Arena, io: Io, mws: []Middleware[C], routes: []http.Route[C], c: &C, req: &http.Request, res: mut http.Response): http.Error!void *routes into a {
    for m in mws {
        go_on := try m(c, a, io, req, res)
        if !go_on {
            return
        }
    }
    try http.Dispatch(a, io, routes, c, req, res)
}
