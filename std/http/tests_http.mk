// Why: std.http's internal functions are tested directly, below the public
// API.
// What: ReadRequest, Send's framing fields, Dispatch's choice of route,
// Fetch's limit, GrowFor and Serve's backoff.
// How: run by std/std-http_blocks_test.go through mk test --std=std.http,
// dev and release, with the counting heap.

import std.core
import std.mem
import std.net
import std.raw

// ThNone is a source with nothing in it: every fill is the end of input.
struct ThNone {
    n: int
}

ThNoneFill(s: &ThNone, io: Io, dst: mut []u8): ReadError!int {
    return 0
}

const ThNoneReads = core.ReadPort[ThNone].New(ThNoneFill)

// ThReadEmpty is ReadRequest over an empty source: 0 for net.Closed (end
// of input before the first byte), 1 for Canceled, 2 for another error, 3
// for a request.
ThReadEmpty(a: &Arena, io: Io): int {
    src := ThNone(n: 0)
    var rb: [64]u8 = [0; 64]
    var r := (&ThNoneReads).Reader(io, &src, mut rb[..])
    _ = ReadRequest(a, io, mut r, 1024) else e {
        return match e {
            Closed { 0 }
            Canceled { 1 }
            _ { 2 }
        }
    }
    return 3
}

// ReadRequest checks for cancellation first (CancelCheck, std's one
// check, core.SyncCancel). On an Io over core.InertIo, whose sleep entry
// fails with Other, the check fails with something other than Canceled:
// not a cancellation, so the read goes on and reports the end of input,
// plain or NoCancel. In a cancelled scope the native Io is Canceled.
test "ReadRequest's cancellation check is a cancellation only when it answers Canceled" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    inert := core.MakeIo(raw.Of(&core.InertIo), 0)
    Assert(ThReadEmpty(&ar, inert) == 0)
    Assert(ThReadEmpty(&ar, inert.NoCancel()) == 0)
    Assert(ThReadEmpty(&ar, sys.io) == 0)
    var cancelled := 9
    var shielded := 9
    scope s := sys.io.Scope() {
        s.Cancel()
        cancelled = ThReadEmpty(&ar, sys.io)
        shielded = ThReadEmpty(&ar, sys.io.NoCancel())
    }
    Assert(cancelled == 1)
    Assert(shielded == 0)
}

// ThSend is what Send writes for res, as text.
ThSend(a: &Arena, io: Io, res: &Response): Error!str from a {
    var t := Text.New(a)
    var w := t.Writer()
    try res.Send(io, mut w)
    return try a.Dup(t.Str())
}

test "Serve's accept backoff doubles from 5 ms and clamps at 1 s" {
    want: [12]int = [5, 10, 20, 40, 80, 160, 320, 640, 1000, 1000, 1000, 1000]
    var d := 0
    for i in 0..12 {
        d = Backoff(d)
        Assert(d == want[i] * 1_000_000)
    }
}

test "Send writes the empty body's length over a handler's Content-Length" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    // What Serve leaves of a handler that set Content-Length: 5 and then
    // failed Status(code: 404): the fields, no body.
    var res := Response.New(&ar)
    try res.Header("Content-Length", "5")
    try res.Header("X-Kept", "1")
    res.Status(404)
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 404 Not Found\r\nX-Kept: 1\r\nContent-Length: 0\r\n\r\n")
}

test "Send replaces a Content-Length that disagrees with the body" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := Response.New(&ar)
    try res.Header("Content-Length", "3")
    try res.Body(b"0123456789")
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n0123456789")
}

test "Send drops a Transfer-Encoding other than chunked" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := Response.New(&ar)
    try res.Header("Transfer-Encoding", "gzip")
    try res.Body(b"abc")
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\nabc")
}

test "Send gives a chunked reply no Content-Length and one Transfer-Encoding" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := Response.New(&ar)
    try res.Header("Content-Length", "99")
    try res.Header("Transfer-Encoding", "chunked")
    try res.Header("Transfer-Encoding", "chunked")
    try res.Body(b"abc")
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n")
}

test "Send gives a reply to an HTTP/1.0 request no Transfer-Encoding: the handler's chunked becomes a Content-Length" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := Response.New(&ar)
    res.old = true
    try res.Header("Transfer-Encoding", "chunked")
    try res.Body(b"hello")
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
}

// ThClose is ParseHead's close verdict for a request head: 1 close, 2
// keep, 0 when the head does not parse.
ThClose(a: &Arena, text: str into a): int {
    h := ParseHead(a, text, false, 0) else {
        return 0
    }
    return if h.close { 1 } else { 2 }
}

test "A close option ends the connection in HTTP/1.0 and 1.1, with keep-alive beside it or not" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    a := &ar
    Assert(ThClose(a, "GET / HTTP/1.0\r\nConnection: close, keep-alive\r\n\r\n") == 1)
    Assert(ThClose(a, "GET / HTTP/1.0\r\nConnection: keep-alive, close\r\n\r\n") == 1)
    Assert(ThClose(a, "GET / HTTP/1.0\r\nConnection: keep-alive\r\nConnection: close\r\n\r\n") == 1)
    Assert(ThClose(a, "GET / HTTP/1.1\r\nHost: h\r\nConnection: keep-alive, close\r\n\r\n") == 1)
    Assert(ThClose(a, "GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n") == 2)
    Assert(ThClose(a, "GET / HTTP/1.0\r\n\r\n") == 1)
    Assert(ThClose(a, "GET / HTTP/1.1\r\nHost: h\r\n\r\n") == 2)
}

test "Send gives 204, 304 and 1xx no framing fields" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    codes: [3]int = [204, 304, 103]
    for i in 0..3 {
        var res := Response.New(&ar)
        try res.Header("Content-Length", "5")
        try res.Header("Transfer-Encoding", "chunked")
        try res.Header("X-A", "b")
        try res.Body(b"dropped")
        res.Status(codes[i])
        out := try ThSend(&ar, sys.io, &res)
        Assert(!out.Has("Content-Length") && !out.Has("Transfer-Encoding") && out.EndsWith("X-A: b\r\n\r\n"))
    }
}

test "Send keeps the handler's Content-Length on a reply to HEAD and sends no body" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var res := Response.New(&ar)
    res.head = true
    try res.Header("Content-Length", "1234")
    try res.Header("Content-Length", "1234")
    Assert(try ThSend(&ar, sys.io, &res) == "HTTP/1.1 200 OK\r\nContent-Length: 1234\r\n\r\n")
    var bad := Response.New(&ar)
    bad.head = true
    try bad.Header("Content-Length", "12x")
    try bad.Body(b"abcd")
    Assert(try ThSend(&ar, sys.io, &bad) == "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\n")
}

test "Matches compares a literal segment with the path's segment percent-decoded" {
    Assert(Matches("/ab", "/a%62"))
    Assert(Matches("/ab", "/ab"))
    Assert(Matches("/a%", "/a%"))
    Assert(Matches("/notes/{id}", "/notes/new"))
    Assert(!Matches("/ab", "/a%6"))
    Assert(!Matches("/ab", "/abc"))
    Assert(!Matches("/a/b", "/a%2Fb"))
    Assert(SegEq("a/b", "a%2fb"))
    Assert(!SegEq("ab", "a%62c"))
}

test "GrowFor never commits more than the bytes still to come" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    b0 := try GrowFor(&ar, "".AsBytes(), 65000)
    Assert(b0.Len() == BodyStep)
    b1 := try GrowFor(&ar, b0[..], 65000 - BodyStep)
    Assert(b1.Len() == 2 * BodyStep)
    b2 := try GrowFor(&ar, b1[0..10], 5)
    Assert(b2.Len() == 15)
    b3 := try GrowFor(&ar, "".AsBytes(), 7)
    Assert(b3.Len() == 7)
}

// ThChunks is a chunked POST of n one-byte chunks, made as it is read:
// ThChunkHead, then "1\r\nx\r\n" n times, then "0\r\n\r\n". pos is the
// next byte's offset.
struct ThChunks {
    n: int
    pos: raw.Atomic[i64]
}

const ThChunkHead: str = "POST /up HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n"

ThChunksFill(s: &ThChunks, io: Io, dst: mut []u8): ReadError!int {
    head := ThChunkHead.AsBytes()
    one := "1\r\nx\r\n".AsBytes()
    last := "0\r\n\r\n".AsBytes()
    total := head.Len() + s.n * one.Len() + last.Len()
    var p := Trunc[int](s.pos.Load())
    var k := 0
    for k < dst.Len() && p < total {
        dst[k] = if p < head.Len() {
            head[p]
        } else if p < head.Len() + s.n * one.Len() {
            one[(p - head.Len()) % one.Len()]
        } else {
            last[p - head.Len() - s.n * one.Len()]
        }
        k += 1
        p += 1
    }
    s.pos.Store(i64(p))
    return k
}

const ThChunksReads = core.ReadPort[ThChunks].New(ThChunksFill)

test "ReadRequest's arena use for a chunked body of many small chunks is linear in the body" (sys: &System) {
    // Past two doublings of the body's buffer (16, 32, then 64 KiB).
    n := 40000
    c := mem.Counting.New(sys.heap.Alloc())
    var ar := try Arena.NewIn(c.Alloc())
    defer ar.Free()
    src := ThChunks(n: n, pos: raw.Atomic[i64].New(0))
    var rb: [256]u8 = [0; 256]
    var r := (&ThChunksReads).Reader(sys.io, &src, mut rb[..])
    req := try ReadRequest(&ar, sys.io, mut r, 1 << 20)
    Assert(req.body.Len() == n && req.body[0] == 120 && req.body[n - 1] == 120)
    // The body's buffer doubles from BodyStep, so the arena holds a few
    // times the body; one chunk more per buffer would be n * n / 2 bytes.
    Assert(c.Peak() > n && c.Peak() < 8 * n)
}

// The serving path over a real loopback connection: Conn,
// Serve's per-connection loop, answers Fetch, and a cancel ends Conn at its
// read. TestHttp_LinuxBlocks runs these on io_uring and on epoll too.

struct ThApp {
    n: int
}

ThHello(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"hi\n")
}

// ThServeOne accepts one connection on l and serves it with Conn.
ThServeOne(l: &net.Listener, heap: &Heap, io: Io, c: &ThApp) {
    conn := l.Accept(io) else {
        return
    }
    Conn(conn, heap, io, 65536, c, ThHello)
}

// ThStop cancels s after ms milliseconds.
ThStop(s: &Scope, io: Io, ms: i64) {
    io.Sleep(core.DurationOf(ms * 1_000_000)) else {}
    s.Cancel()
}

// ThAddr writes "127.0.0.1:<port>" after pre into b and returns it.
ThAddr(b: mut [64]u8, pre: str, port: int): str from b {
    p := pre.AsBytes()
    for i in 0..p.Len() {
        b[i] = p[i]
    }
    var d: [8]u8 = [0; 8]
    var n := 0
    var x := port
    for {
        d[n] = Trunc[u8](48 + x % 10)
        n += 1
        x = x / 10
        if x == 0 {
            break
        }
    }
    for i in 0..n {
        b[p.Len() + i] = d[n - 1 - i]
    }
    return str.From(b[0..p.Len() + n]).Must()
}

test "Fetch is answered by Conn over loopback" (sys: &System) {
    io := sys.io
    l := net.Listen(io, "127.0.0.1:0") else {
        Panic("net.Listen failed")
    }
    defer l.Close()
    app := ThApp(n: 0)
    var ab: [64]u8 = [0; 64]
    url := ThAddr(mut ab, "http://127.0.0.1:", l.Port())
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    hs := List[Header].New(&ar).Done()
    var status := 0
    var blen := 0
    scope s := io.Scope() {
        s.go ThServeOne(&l, sys.heap, io, &app)
        reply := Fetch(&ar, io, "GET", url, hs[..], "".AsBytes(), 65536) else {
            Panic("Fetch failed")
        }
        status = reply.status
        blen = reply.body.Len()
    }
    Assert(status == 200 && blen == 3)
}

test "A cancel ends Conn waiting in the middle of a head" (sys: &System) {
    io := sys.io
    l := net.Listen(io, "127.0.0.1:0") else {
        Panic("net.Listen failed")
    }
    defer l.Close()
    app := ThApp(n: 0)
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())
    var ended := 0
    scope s := io.Scope() {
        s.go ThServeOne(&l, sys.heap, io, &app)
        s.go ThStop(&s, io, 200)
        conn := net.Dial(io, addr) else {
            Panic("net.Dial failed")
        }
        defer conn.Close()
        conn.WriteAll(io, "GET /hel".AsBytes()) else {}
        var b: [64]u8 = [0; 64]
        // Conn ends at the cancel and closes the connection, or this read
        // is cancelled itself: either way it returns.
        _ = conn.Read(io, mut b[..]) else {
            ended = 1
        }
        ended += 1
    }
    Assert(ended >= 1)
}

// A route table where each handler answers its name as the body.
ThId(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"id")
}

ThNew(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"new")
}

ThRest(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"rest")
}

ThTwo(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"two")
}

ThLit(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"lit")
}

ThPost(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"post")
}

ThGet(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"get")
}

ThHeadOnly(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"head")
}

ThT1(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"t1")
}

ThT2(c: &ThApp, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void {
    try res.Body(b"t2")
}

const ThRoutes: []Route[ThApp] = [
    Route[ThApp](method: "GET", pattern: "/n/{id}", handler: ThId),
    Route[ThApp](method: "GET", pattern: "/n/new", handler: ThNew),
    Route[ThApp](method: "GET", pattern: "/f/{rest...}", handler: ThRest),
    Route[ThApp](method: "GET", pattern: "/f/{a}/{b...}", handler: ThTwo),
    Route[ThApp](method: "GET", pattern: "/f/x/{b...}", handler: ThLit),
    Route[ThApp](method: "POST", pattern: "/p", handler: ThPost),
    Route[ThApp](method: "PUT", pattern: "/p", handler: ThPost),
    Route[ThApp](method: "GET", pattern: "/h", handler: ThGet),
    Route[ThApp](method: "HEAD", pattern: "/h", handler: ThHeadOnly),
    Route[ThApp](method: "GET", pattern: "/g", handler: ThGet),
    Route[ThApp](method: "GET", pattern: "/t/{x}", handler: ThT1),
    Route[ThApp](method: "GET", pattern: "/t/{y}", handler: ThT2),
]

// ThRoute is the reply Dispatch gives method path over ThRoutes, as Serve
// would send it: a Status(code) keeps the fields Dispatch set.
ThRoute(a: &Arena, io: Io, method: str, path: str): Error!str from a {
    var res := Response.New(a)
    empty := List[Header].New(a).Done()
    req := Request(method: method, path: path, query: "", headers: empty[..], params: empty[..], body: "".AsBytes())
    app := ThApp(n: 0)
    Dispatch(a, io, ThRoutes, &app, &req, mut res) else err {
        match err {
            Status(code) {
                res.Status(code)
            }
            _ {
                fail err
            }
        }
    }
    return try ThSend(a, io, &res)
}

test "the most specific pattern wins, then the request's own method, then the first" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    io := sys.io
    Assert((try ThRoute(&ar, io, "GET", "/n/new")).EndsWith("\r\n\r\nnew"))
    Assert((try ThRoute(&ar, io, "GET", "/n/7")).EndsWith("\r\n\r\nid"))
    Assert((try ThRoute(&ar, io, "GET", "/f/x/y")).EndsWith("\r\n\r\nlit"))
    Assert((try ThRoute(&ar, io, "GET", "/f/q/y/z")).EndsWith("\r\n\r\ntwo"))
    Assert((try ThRoute(&ar, io, "GET", "/f/q")).EndsWith("\r\n\r\nrest"))
    Assert((try ThRoute(&ar, io, "GET", "/t/1")).EndsWith("\r\n\r\nt1"))
    Assert(Specific("/n/new", "/n/{id}") == 1)
    Assert(Specific("/n/{id}", "/n/new") == -1)
    Assert(Specific("/f/{a}/{b...}", "/f/{rest...}") == 1)
    Assert(Specific("/t/{x}", "/t/{y}") == 0)
    Assert(Specific("/a", "/a") == 0)
}

test "HEAD matches GET with no body sent, and a HEAD route beats a GET one" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    io := sys.io
    Assert(try ThRoute(&ar, io, "HEAD", "/g") == "HTTP/1.1 200 OK\r\nContent-Length: 3\r\n\r\n")
    Assert(try ThRoute(&ar, io, "HEAD", "/h") == "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\n")
    Assert((try ThRoute(&ar, io, "GET", "/h")).EndsWith("\r\n\r\nget"))
}

test "only the method differs: 405 with Allow, sorted, HEAD beside GET; else 404" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    io := sys.io
    Assert(try ThRoute(&ar, io, "DELETE", "/p") == "HTTP/1.1 405 Method Not Allowed\r\nAllow: POST, PUT\r\nContent-Length: 0\r\n\r\n")
    Assert(try ThRoute(&ar, io, "POST", "/g") == "HTTP/1.1 405 Method Not Allowed\r\nAllow: GET, HEAD\r\nContent-Length: 0\r\n\r\n")
    Assert(try ThRoute(&ar, io, "POST", "/h") == "HTTP/1.1 405 Method Not Allowed\r\nAllow: GET, HEAD\r\nContent-Length: 0\r\n\r\n")
    Assert(try ThRoute(&ar, io, "GET", "/nothing") == "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
    Assert(try ThRoute(&ar, io, "GET", "/p/q") == "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
}

// ThServeN accepts n connections on l, one after another, and serves each
// with Conn.
ThServeN(l: &net.Listener, heap: &Heap, io: Io, c: &ThApp, n: int) {
    for _ in 0..n {
        conn := l.Accept(io) else {
            return
        }
        Conn(conn, heap, io, 65536, c, ThHello)
    }
}

// ThFetchCode fetches url with limit: the status, or -1 for TooLarge, -2
// for another error.
ThFetchCode(a: &Arena, io: Io, url: str, limit: int): int {
    hs := List[Header].New(a).Done()
    reply := Fetch(a, io, "GET", url, hs[..], "".AsBytes(), limit) else err {
        return match err {
            TooLarge { -1 }
            _ { -2 }
        }
    }
    return reply.status
}

test "Fetch's limit bounds the reply's bytes, head and body, and a negative one acts as 0" (sys: &System) {
    io := sys.io
    l := net.Listen(io, "127.0.0.1:0") else {
        Panic("net.Listen failed")
    }
    defer l.Close()
    app := ThApp(n: 0)
    var ab: [64]u8 = [0; 64]
    url := ThAddr(mut ab, "http://127.0.0.1:", l.Port())
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    // The reply is a head and three bytes: the smallest limit that takes it is
    // its length, and every smaller one is TooLarge.
    var codes: [64]int = [0; 64]
    scope s := io.Scope() {
        s.go ThServeN(&l, sys.heap, io, &app, 64)
        codes[0] = ThFetchCode(&ar, io, url, -5)
        codes[1] = ThFetchCode(&ar, io, url, 65536)
        for k in 2..64 {
            codes[k] = ThFetchCode(&ar, io, url, k + 10)
        }
    }
    Assert(codes[0] == -1)
    Assert(codes[1] == 200)
    var first := -1
    for k in 2..64 {
        if codes[k] == 200 && first < 0 {
            first = k
        }
        if first < 0 {
            Assert(codes[k] == -1)
        } else {
            Assert(codes[k] == 200)
        }
    }
    Assert(first > 2)
}
