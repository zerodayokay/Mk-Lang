// Serve the normal scripted routes through std.http's multiplexed server.

import std.http
import std.net
import std.time

export struct Context {
    export script: &Script
    export addrs: &Addrs
}

/// Serves the script on l using the protocol options in o.
export ServeHttp(l: &net.Listener, h: &Heap, io: Io, c: &Context, o: &http.ServerOptions) {
    http.ServeOn(h, io, l, o, c, AnswerHttp) else {}
}

AnswerHttp(c: &Context, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = link
    route := Find(c.script, req.method, req.path) else {
        res.Status(404)
        try res.Body("not found\n".AsBytes())
        return
    }
    if route.websocket || route.cut >= 0 {
        res.Status(501)
        return
    }
    if route.stall {
        try io.Sleep(time.Hours(24))
        return
    }
    reply := try Shape(a, route, req, c.addrs)
    res.Status(reply.status)
    for line in reply.headers.Split("\r\n") {
        if colon := line.Find(":") {
            try res.Header(line[..colon].Trim(), line[colon + 1..].Trim())
        }
    }
    try res.Body(reply.body)
}
