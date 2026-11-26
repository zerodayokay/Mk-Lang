// Why: a single reverse proxy handler should work across the three HTTP
// versions without changing how each hop manages its connection.
// What: Proxy, its sharing context, and the bounded response copy.
// How: build the origin URL from a fixed loopback base and the parsed
// request target. Copy each response piece only after the preceding flush.

import std.http
import std.atomic
import std.url

struct Relay {
    upstream: &http.Client
    base: str
    expected: http.HttpVersion
    front: http.HttpVersion
    cancellation_probe: bool
    budget: Budget
    seen: atomic.AtomicInt
}

Proxy(c: &Relay, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    try c.budget.Start(io, req.body.Len())
    var retained := req.body.Len()
    Forward(c, a, io, req, res, link, mut retained) else e {
        c.budget.Finish(io, retained)
        fail e
    }
    c.budget.Finish(io, retained)
}

Forward(c: &Relay, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link, retained: mut int): http.Error!void {
    _ = c.seen.Add(1)
    incoming := link.Version()
    if res.Version() != incoming || incoming != c.front ||
        res.Secure() != (incoming == http.HttpVersion.Http3) {
        fail http.Status(code: 505)
    }
    if !req.path.StartsWith("/") {
        fail http.Status(code: 400)
    }
    suffix := if req.query == "" { "" } else { try a.Fmt("?{query}", (query: req.query)) }
    text := try a.Fmt("{base}{path}{suffix}", (base: c.base, path: req.path, suffix))
    dest := url.Parse(a, text, none) else {
        fail http.Status(code: 400)
    }
    if dest.Path() != req.path || dest.Query() != req.query {
        fail http.Status(code: 400)
    }
    request_local := try ConnectionFields(a, req.headers)
    var headers := List[http.Header].New(a)
    for h in req.headers {
        if try ForwardField(a, h.name, &request_local) {
            try headers.Push(http.Header(name: try a.Dup(h.name), value: try a.Dup(h.value)))
        }
    }
    outgoing := http.ClientRequest(method: req.method, url: &dest, headers: headers.Items(), body: req.body)
    var ex := c.upstream.Do(a, io, &outgoing) else {
        fail http.Status(code: 502)
    }
    onerr ex.Abandon()
    if ex.Version() != c.expected {
        fail http.Status(code: 502)
    }
    res.Status(ex.status)
    try res.Header("x-relay-ingress", WireName(incoming))
    response_local := try ConnectionFields(a, ex.headers)
    for h in ex.headers {
        if try ForwardField(a, h.name, &response_local) {
            try res.Header(h.name, h.value)
        }
    }
    probe := c.cancellation_probe && req.path == "/cancel"
    if probe {
        try res.Flush(io, link)
    }
    try CopyReply(&c.budget, io, mut ex, res, link, retained)
    try ex.Close(io)
}

NewRelay(upstream: &http.Client, base: str, front: http.HttpVersion, back: http.HttpVersion, probe: bool): Relay from upstream, base {
    return Relay(
        upstream: upstream,
        base: base,
        expected: back,
        front: front,
        cancellation_probe: probe,
        budget: NewBudget(),
        seen: atomic.AtomicInt.New(0),
    )
}

CopyReply(budget: &Budget, io: Io, ex: mut http.Exchange, res: mut http.Response, link: &http.Link, retained: mut int): http.Error!void {
    var scratch: [CopyBlock]u8 = [0; CopyBlock]
    var piece: [CopyBlock]u8 = [0; CopyBlock]
    var reader := ex.Body(mut scratch[..])
    var left := MaxBody
    for {
        n := try reader.Read(mut piece[..])
        if n == 0 {
            return
        }
        if n > left {
            fail http.TooLarge
        }
        left -= n
        try res.Body(piece[..n])
        *retained += n
        budget.RecordCopy(io, n)
        try res.Flush(io, link)
    }
}
