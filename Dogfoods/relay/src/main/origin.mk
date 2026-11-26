// Why: a local origin can verify precisely what a forwarded request carried.
// What: Backend, echo payloads and observed cancellation counts.
// How: validate the method, query, header and bytes; for cancellation
// start the response and write enough to exceed the receiver's window.

import std.http
import std.atomic

struct Origin {
    seen: atomic.AtomicInt
    canceled: atomic.AtomicInt
    bytes: atomic.AtomicInt
    bad: atomic.AtomicInt
    round: atomic.AtomicInt
}

Backend(c: &Origin, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = c.seen.Add(1)
    _ = c.bytes.Add(i64(req.body.Len()))
    if req.method != "POST" || req.query != "echo=yes%20please" ||
        !HasField(req.headers, "x-demo", "relay") || !CheckPayload(req.body, 0) {
        _ = c.bad.Add(1)
        fail http.Status(code: 400)
    }
    try res.Header("x-origin-version", WireName(res.Version()))
    try res.Header("x-origin-secure", if res.Secure() { "yes" } else { "no" })
    try res.Header("set-cookie", "first=1; Path=/")
    try res.Header("set-cookie", "second=2; Path=/")
    if c.round.Load() > 0 {
        round := c.round.Load()
        try res.Header("x-origin-round", try a.Fmt("{round}", (round)))
    }
    if res.Version() == http.HttpVersion.Http11 {
        try res.Header("Connection", "X-Origin-Local")
        try res.Header("X-Origin-Local", "origin")
    }
    if req.path == "/cancel" {
        try res.Flush(io, link)
        var block: [CopyBlock]u8 = [b'x'; CopyBlock]
        for _ in 0..MaxBody / block.Len() {
            try res.Body(block[..])
            res.Flush(io, link) else e {
                _ = c.canceled.Add(1)
                fail e
            }
        }
        return
    }
    if req.path != "/echo" {
        fail http.Status(code: 404)
    }
    try res.Body(req.body)
}

PayloadByte(i: int): u8 {
    return Cast[u8]((i % 251))
}

CheckPayload(b: []u8, offset: int): bool {
    for i in 0..b.Len() {
        if b[i] != PayloadByte(offset + i) {
            return false
        }
    }
    return true
}
