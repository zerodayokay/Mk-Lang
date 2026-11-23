# std.http

`std.http` is an HTTP/1.1 client and server. The client keeps a pool of connections that every task of a program shares. The server runs one task per connection and calls your handler with each request, either directly or through a table of routes. Use it for web services, web clients and anything that speaks HTTP/1.1. For a plain byte stream use [`std.net`](net.md); for WebSockets, [`std.websocket`](websocket.md), whose server side takes its connections from this module's server.

`std.http` does not import `std.tls`. A client speaks `https`, and a server TLS, when its settings name a connector from [`std.tls`](tls.md), so `import std.tls` is the opt-in. `std.http` is Stable, but `https` and a TLS server are [Experimental](README.md#a-stable-module-over-an-experimental-one) as long as `std.tls` is. Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.http
```

## Contents

- [The main pieces](#the-main-pieces)
- [The client](#the-client)
- [The server](#the-server)
- [Routes](#routes)
- [What the parsers refuse](#what-the-parsers-refuse)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Time limits and cancellation](#time-limits-and-cancellation)
- [Examples](#examples): [route requests](#route-requests), [serve and fetch](#serve-and-fetch), [stream a reply](#stream-a-reply)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Fetch one URL, whole, with no setup | `Fetch` (plain `http` only) |
| Make requests from many tasks | `Client.New` with `ClientLimits`, then `Client.Fetch` or `Client.Do` |
| Read a large reply as it arrives | `Client.Do`, then `Exchange.Body`, then `Exchange.Close` |
| Upload a body from a `Reader` | `Client.DoStreaming` |
| Use `https` | `ClientLimits.secure` set to `some(tls.Connector(&cfg))` |
| Serve with the defaults | `Serve` |
| Serve with limits, keep-alive and TLS | `ServeWith` with `ServerOptions` |
| Serve a listener you made, from a task | `ServeOn`, or `ServeChan` to send values to other tasks |
| Route by method and path | a `const []Route[C]` and `Dispatch` |
| Build a reply | `Response.Status`, `Response.Header`, `Response.Body`, `Response.Writer` |
| Send part of a reply early | `Response.Flush` |
| Hand a connection to another protocol | `Link.Takeover`, which returns a `Taken` |
| Read one request from any `Reader` | `ReadRequest` |

The records are plain data: `Header` is one field, `Request` is what the server read, `Reply` is a whole reply, and `ClientRequest` is what a client sends. A `Request`'s and a `Reply`'s strings and slices are views of the arena they were read into. A `ClientRequest` borrows the URL, fields and body you give it.

## The client

A `Client` keeps at most `idle_connections` connections for reuse (16 by default, and never more than 16) over every origin. A request that finds all of them busy opens a connection of its own, which is closed when its `Exchange` ends. One client serves every task of a program: it is linear, `Send` and `Share`, and any number of tasks can use its pool at once. Make it with `Client.New` and end it with `Client.Close`.

- `Client.Do` sends a request, reads the reply's head and returns an `Exchange`, which holds the connection. Read the body with `Exchange.Body`, then end the exchange with `Close`, or with `Abandon` on an error path. `Close` returns the connection to the pool when the body was read to its end, and closes it otherwise.
- `Client.Fetch` is `Do`, the body read whole under the limits' `body_bytes`, and `Close`.
- `Client.DoStreaming` sends a body from a `Reader`, with a `Content-Length` when you give its length and chunked when you do not. A redirect that would send the body again (307, 308, or a 301 or 302 after a method other than POST) fails with `BodyNotReplayable`; a body whose given length is 0 never fails. A 303, or a 301 or 302 after POST, is followed with GET and no body.
- The body an `Exchange` reads is decoded, and only the reply's framing limits it, so bound a streamed body's decoded size yourself.
- The client advertises in `Accept-Encoding` exactly `ClientLimits.decoders`, and decodes those codings. `compress.GzipContent`, `compress.DeflateContent` and `compress.ZstdContent` are the values to put there.
- It reads no body for `HEAD`, or for a 1xx, 204 or 304 status.
- It follows at most `redirects` redirects, 10 by default, and only to `http` and `https`. With 0 it follows none and returns the 3xx reply as it is. When a redirect changes the origin, it does not send `Authorization`, `Proxy-Authorization` or `Cookie` to the new one, and it never sends URL userinfo.
- It calls `allow` with each address before it connects, redirects included. An address it refuses is not dialled, and when it refuses every one the connect fails with `Refused`. A service that fetches URLs it is given refuses loopback and private ranges here.

Start from `DefaultClientLimits`:

| Field | Default | Past it |
|---|---|---|
| `connect` | 30 s | `time.TimedOut` |
| `response_header` | 30 s | `time.TimedOut` |
| `idle` | 90 s | the pooled connection is closed |
| `close` | 5 s | `time.TimedOut` |
| `header_bytes` | 1 MiB | `TooLarge` |
| `body_bytes` | 64 MiB | `TooLarge` from `Client.Fetch` |
| `redirects` | 10 | `TooManyRedirects` |
| `idle_connections` | 16 | the connection is closed instead of kept |
| `decoders` | empty | no `Accept-Encoding` |
| `allow`, `secure`, `proxy` | `none` | every address allowed, no `https`, no proxy |

A client keeps its limits, and with them what they borrow, such as the `tls.Config` behind `secure`. So it cannot outlive that `Config`:

```mk
// verdict: reject E0603
// Client.New's result is from h and l
import std.http
import std.tls

Make(h: &Heap): http.Client {
    cfg := tls.DefaultConfig
    var l := http.DefaultClientLimits
    l.secure = some(tls.Connector(&cfg))
    return http.Client.New(h, l) // <- E0603
}
```

An `https` URL with `secure` set to `none` fails with `NoSecureTransport`. A secure connect that fails reports `net.SecureFailed(why)`. See [Make an HTTPS request](../cookbook.md#make-an-https-request) and [Download a large file over HTTP](../cookbook.md#download-a-large-file-over-http).

**Proxies.** `ClientLimits.proxy` names an HTTP proxy: an `http` URL with a host and no userinfo, else `BadUrl`. Every connection then goes to the proxy:

- `allow` is asked about the proxy's addresses, never the origin's. The proxy resolves the origin's name.
- An `http` request goes to the proxy in absolute form.
- An `https` request goes through a `CONNECT` tunnel to the origin's host and port. The connect limit and `header_bytes` bound the tunnel's reply head. Then the TLS handshake runs with the origin's name, and no `Proxy-Authorization` goes inside the tunnel.
- A `CONNECT` status other than 2xx is `ProxyRefused(code)`, whether a body follows or not. A byte that arrives with a 2xx head is `BadRequest`.
- The client writes no proxy credentials of its own. A request's own `Proxy-Authorization` field reaches the proxy on an `http` request only, and not after a redirect to another origin. None goes with the `CONNECT`.
- std reads no proxy variable, such as `HTTP_PROXY`, from the environment.

`Fetch`, the plain function, is the smallest client: one request over a new connection with `Connection: close`, the whole reply in memory, no pool, no redirects and no `https`.

## The server

`Serve` listens on an address and runs one task per connection until the caller's scope is cancelled; then it returns, once every connection's task has ended. Each connection's task has one arena, reset after each reply; a request and its response live there. Every task calls the handler with the one context `c`, so its type `C` is `Share`.

A handler fills in the `Response` it is given; the server sends it when the handler returns.

- `Response.Header` and `Response.Body` copy what they are given into the response's arena, so a handler stores no loan of its own into the response.
- `Response.Writer` appends UTF-8 text to the body. `Response.Body` sets bytes of any kind.
- A handler that fails with `Status(code)` sends that status, the fields it set and an empty body. Any other error, or a code outside 200 to 999, sends 500 and ends the connection.
- The server writes the framing itself: a `Content-Length` that matches the body, or chunks when the handler set `Transfer-Encoding: chunked`. A handler's `Connection: close` ends the connection after the reply.

`Serve` keeps connections alive between requests, reads pipelined requests in order, reads chunked request bodies whole under its `limit`, and answers `Expect: 100-continue`. It has no time limits, no cap on connections and no TLS. `limit` bounds each request's head and body together: past it the reply is 413, and a head over 32 KiB is 431. `ServeWith` adds the time limits, the cap and TLS with a `ServerOptions`, and reads chunked bodies whole under `body_bytes`. Start from `DefaultServerOptions`:

| Field | Default | Past it |
|---|---|---|
| `connections` | 10,000 | more connections wait in the listen queue |
| `header_bytes` | 32 KiB | 431, and the connection ends |
| `body_bytes` | 8 MiB | 413, and the connection ends |
| `request_header` | 30 s | the connection is closed; it also bounds each read of a body and each write of a reply |
| `keep_alive` | 75 s | the idle connection is closed |
| `secure` | `none` (plain TCP) | set it to `some(tls.Acceptor(&cfg))` for TLS |

- `ServeOn` is `ServeWith` over a listener the caller made, with an `Io` and a heap where `ServeWith` takes `&System`, which is not `Share`. A task can run it, the caller can read `Listener.Port` before it serves, and the listener stays open when it returns.
- `ServeChan` is `ServeOn` with a channel handed to the handler beside `c`, and the heap `h` with it. The handler may store `h` into the channel, so it can send a value made from `h`, such as a `sync.Shared` frame, to the program's other tasks. No other loan may reach the channel:
  - not `c`'s loans, so a frame comes from `h` and not from a heap `c` holds;
  - not the request, its arena or the link;
  - not a value received from the channel that holds a loan, so a handler does not send on a frame it received.

  A plain value such as an `int` it may send.
- A `ServeWith` handler gets a `Link` beside its response: the connection's handle for this request. The server owns the connection, and no store into the response reaches it.
- `Response.Flush` sends the head with chunked framing, and what the body holds so far, through the link. Later flushes send what was added since; the server sends the rest when the handler returns.
- `Link.Takeover` gives the connection, and the bytes already read past the request, to `websocket.Accept` or a protocol of your own. It returns `none` when the connection was already taken. A `Taken` holds the link's loans, so it lives inside the handler; `Taken.Abandon` closes it.

`ReadRequest` reads one request from any `Reader`, with the parser the server uses, bounded by `limit` (head and body together). `Response.Send` writes a response to any `Writer`. Together with `Dispatch` they serve requests from a transport of your own; the first example uses them on bytes in memory.

## Routes

A route table is a `const []Route[C]`: static data that every connection's task may read. Each `Route` has a method, a pattern and a handler. A pattern is `/`-separated segments: a literal, a `{name}` that matches one segment, or a last `{name...}` that matches the rest of the path. The handler reads them with `Request.Param` and `Request.ParamInt`.

`Serve` and `ServeWith` take one handler: write one that calls `Dispatch(a, io, Routes, c, req, res)`. A route's handler gets no `Link`, so under `ServeWith` answer a path that flushes or takes the connection over before you call `Dispatch`.

`Dispatch` picks the most specific route whose pattern matches:

- At the first segment where two matching patterns differ, a literal beats `{name}`, which beats `{name...}`. So a literal segment beats a parameter, and a longer pattern beats a shorter one ending in `{name...}`. Between equal patterns, the earlier route wins.
- A literal segment matches the path's segment percent-decoded, so `/ab` matches `/a%62`.
- A `HEAD` request also matches `GET` routes, and its reply carries no body.
- When only the method differs, `Dispatch` fails with `Status(405)` and sets an `Allow` field naming the methods that match. When no pattern matches, it fails with `Status(404)`.
- A `.` or `..` path segment, plain or percent-encoded, is `Status(400)` before any route is tried.

See [Serve HTTP with routes](../cookbook.md#serve-http-with-routes) for a full server, and the [web](../../kits/web/README.md) framework for middleware, parameters, JSON bodies and validation on top of the route table.

## What the parsers refuse

The client and the server read heads and bodies from bytes that a peer controls, with one strict parser. A server's request target is never a URL. `ReadRequest` accepts the four forms of RFC 9112:

- origin-form, `/path?query`;
- absolute-form, for `http` and `https`;
- authority-form, for `CONNECT` only;
- `*`, for `OPTIONS` only.

It refuses with `BadRequest`:

- a byte outside RFC 3986's path and query sets;
- a `%` not followed by two hex digits;
- an origin-form target that does not start with `/`;
- a form the method may not use.

`Request.path` and `Request.query` are the bytes received, never decoded or cleaned.

Every entry point, client and server, refuses with an `http.Error` and closes the connection on:

- both `Transfer-Encoding` and `Content-Length`;
- a `Content-Length` that is not one run of digits, two that differ, or one over the body limit;
- a transfer coding other than a final `chunked`, or any on HTTP/1.0;
- a bare LF or CR in a head, a chunk line or trailers;
- a chunk size that is not hex, or that exceeds the limit;
- chunk extensions or trailers past the head limit;
- obsolete line folding, or whitespace before the first header;
- a header name with whitespace before the colon, or a byte that is not a token byte;
- a NUL or control byte in a value;
- an HTTP/1.1 request with no `Host`, two, or one that is not a host and optional port.

None of the parsers panics on its input, and each holds memory and stack bounded by its limits.

## Errors

Every function fails with members of one error set, `http.Error`:

| Member | Meaning |
|---|---|
| `BadRequest(offset)` | A malformed request or reply; `offset` is the byte where reading stopped |
| `TooLarge` | A request or reply passed its byte limit |
| `Status(code)` | A handler's answer, or `Dispatch`'s 400, 404 or 405 |
| `NoSecureTransport` | An `https` URL, and no connector in `ClientLimits.secure` |
| `BodyNotReplayable` | A redirect asked to send a streamed body again |
| `TooManyRedirects` | Still a redirect after `redirects` were followed |
| `BadUrl` | A URL or `Location` the client cannot send to, or a bad `proxy` |
| `ProxyRefused(code)` | The proxy answered `CONNECT` with a status other than 2xx |
| `ReadError`, `WriteError` | A failed read of the `Reader` a request or reply comes from, or a failed write |
| `net.Error` | The connection's errors: `Refused`, `Reset`, `Closed`, `Canceled`, `time.TimedOut`, `SecureFailed` and the rest |
| `OutOfMemory` | An arena or the heap is full |

`ReadRequest` and `Fetch` fail with `TooLarge` past `limit` bytes. `ReadRequest` fails with `Closed`, the prelude's error, when its reader ends before the request's first byte, and with `BadRequest` when it ends inside a request. `http.Error` is open, so a `match` on it outside std keeps a `_` arm.

## Memory and cost

- The server gives each connection one arena from the heap, reset after each response. The request, its fields and its body live there, and the response copies what the handler gives it there. A connection's read and write buffers are on its task's stack.
- A client's pooled connections live on the process heap, so a pooled connection outlives every request's arena. `Client.Do` puts the reply's head, fields and read buffer in the arena you pass, and that arena holds the connection until the `Exchange` ends.
- `Client.Fetch` and `ReadRequest` read a body into a buffer that grows as its bytes arrive, so a declared length commits no memory before its bytes come. The outgrown buffers stay in the arena. A body with a `Content-Length` of n bytes takes up to about 3n of it through `ReadRequest` and the server. A reply body takes up to about 4n (at least 16 KiB) through `Client.Fetch`, which grows its buffer without the declared length. A chunked request body of n bytes, whose size is not known before its end, takes up to about 4n (at least 16 KiB), as a reply body does.
- `Client`, `Exchange` and `Taken` own a connection or a pool and do not implement `Free`. End them with their own `Close` or `Abandon`, which take no allocator. A container of them is released with `FreeEach`.
- Each function's doc states its cost; most are O(bytes sent and read).

## Time limits and cancellation

The client's limits and the server's options hold their own time limits, and none is zero by default. The client has connect, response-header and idle limits, and one for a close that speaks to the peer; the server has request-header and keep-alive limits. A limit that passes fails with `time.TimedOut` ([cancellation](README.md#6-cancellation)). `Fetch` and `Serve` take no limits record.

- A client connection whose `Do`, `Fetch` or body read was interrupted is closed and never returned to the pool, and its `Exchange` fails every later call but `Abandon`.
- A server connection whose response write was interrupted is closed and never reused.
- `Exchange.Close` over a secure connection speaks to the peer, so it can be cancelled and is bounded by the client's `close` limit. On either failure the `Exchange` stays the caller's: write `onerr x.Abandon()` with `try x.Close(io)`.
- Cancelling the scope of `ServeWith` or `ServeOn` makes it return, and the connections being served are closed. `ServeOn` leaves connections still in the listen queue for the next `Accept`; `ServeWith` closes its listener, and the queue with it.

## Examples

Each program below runs as written with `mk run`. The second and third serve on a loopback port the OS picks, and stop the server by cancelling its scope.

### Route requests

`Dispatch` and a route table, fed requests from memory with `ReadRequest`, and the replies written with `Response.Send`. A server does the same for each request it reads.

```mk
// verdict: ok
import std.http

struct Shop {
    name: str
}

Item(c: &Shop, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400) // not a number
    }
    var w := res.Writer()
    try Print(mut w, "{shop}: item {id}\n", (shop: c.name, id))
}

Files(c: &Shop, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    path := req.Param("path") else {
        fail http.Status(code: 404)
    }
    try res.Header("Content-Type", "text/plain")
    var w := res.Writer()
    try Print(mut w, "file {path}\n", (path))
}

const Routes: []http.Route[Shop] = [
    http.Route[Shop](method: "GET", pattern: "/items/{id}", handler: Item),
    http.Route[Shop](method: "GET", pattern: "/files/{path...}", handler: Files),
]

// Answer reads one request from raw and writes the reply to standard output.
Answer(sys: &System, shop: &Shop, raw: str): http.Error!void {
    var r := Reader.Of(raw.AsBytes())
    req := try http.ReadRequest(sys.arena, sys.io, mut r, 65536)
    var res := http.Response.New(sys.arena)
    http.Dispatch(sys.arena, sys.io, Routes, shop, &req, mut res) else e {
        match e {
            http.Status(code) { res.Status(code) } // what Serve does for a handler
            _ { fail e }
        }
    }
    var out := sys.Stdout()
    try res.Send(sys.io, mut out)
    try sys.Print("\n", ())
}

export Main(sys: &System): http.Error!void {
    shop := Shop(name: "corner shop")
    try Answer(sys, &shop, "GET /items/42 HTTP/1.1\r\nHost: shop\r\n\r\n")
    try Answer(sys, &shop, "GET /files/docs/a.txt HTTP/1.1\r\nHost: shop\r\n\r\n")
    try Answer(sys, &shop, "POST /items/42 HTTP/1.1\r\nHost: shop\r\nContent-Length: 0\r\n\r\n")
    try Answer(sys, &shop, "GET /files/../secret HTTP/1.1\r\nHost: shop\r\n\r\n")
}
```

```text
$ mk run route.mk
HTTP/1.1 200 OK
Content-Length: 21

corner shop: item 42

HTTP/1.1 200 OK
Content-Type: text/plain
Content-Length: 16

file docs/a.txt

HTTP/1.1 405 Method Not Allowed
Allow: GET, HEAD
Content-Length: 0


HTTP/1.1 400 Bad Request
Content-Length: 0
```

- `{path...}` matches the rest of the path, slashes included.
- The `POST` matches a route's pattern but not its method, so `Dispatch` answers 405 with an `Allow` field. The `..` segment is refused before any route is tried.

### Serve and fetch

A server task and a client in one program. `ServeOn` serves a listener the program made, so the program knows its port.

```mk
// verdict: ok
import std.http
import std.net
import std.url

struct Greeter {
    word: str
}

Hello(c: &Greeter, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    agent := req.Header("user-agent") else { "nobody" } // names ignore case
    var w := res.Writer()
    try Print(mut w, "{word}, {agent}: {path} with {query}\n", (word: c.word, agent, path: req.path, query: req.query))
}

// Server serves l until its scope is cancelled.
Server(h: &Heap, io: Io, l: &net.Listener, g: &Greeter) {
    http.ServeOn(h, io, l, &http.DefaultServerOptions, g, Hello) else {}
}

const Agent: []http.Header = [http.Header(name: "User-Agent", value: "example")]
const NoBody: []u8 = []

Get(c: &http.Client, a: &Arena, io: Io, link: str into a): http.Error|url.Error!http.Reply from a {
    u := try url.Parse(a, link, none)
    req := http.ClientRequest(method: "GET", url: &u, headers: Agent, body: NoBody)
    return try c.Fetch(a, io, &req)
}

export Main(sys: &System): http.Error|url.Error|Utf8Error|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0") // port 0: the OS picks a free port
    defer l.Close()
    link := try sys.arena.Fmt("http://127.0.0.1:{port}/hello?lang=en", (port: l.Port()))
    g := Greeter(word: "hello")
    c := http.Client.New(sys.heap, http.DefaultClientLimits)
    defer c.Close()
    scope s := sys.io.Scope() {
        s.go Server(sys.heap, sys.io, &l, &g)
        onerr s.Cancel() // a failure below stops the server too
        reply := try Get(&c, sys.arena, sys.io, link)
        s.Cancel() // stop the server; the scope waits for its task
        body := try str.From(reply.body) // the body is bytes
        try sys.Print("{status}\n{body}", (status: reply.status, body))
    }
}
```

```text
$ mk run serve.mk
200
hello, example: /hello with lang=en
```

- `Request.Header` compares names without regard to case.
- `onerr s.Cancel()` stops the server when the fetch fails, so the scope does not wait for it forever.
- One `Client` serves every task; it lives until `c.Close()`.

### Stream a reply

The handler sends each line as it is made with `Response.Flush`; the client reads the body as it arrives with `Client.Do` and `Exchange.Body`.

```mk
// verdict: ok
import std.http
import std.net
import std.url

struct Counter {
    n: int
}

// Count sends one line per Flush, each as a chunk of its own.
Count(c: &Counter, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    for i in 1..c.n + 1 {
        var w := res.Writer()
        try Print(mut w, "line {i}\n", (i))
        try res.Flush(io, link) // w's loan of res has ended here
    }
}

Server(h: &Heap, io: Io, l: &net.Listener, c: &Counter) {
    http.ServeOn(h, io, l, &http.DefaultServerOptions, c, Count) else {}
}

const NoHeaders: []http.Header = []
const NoBody: []u8 = []

// Show prints the body of link as it arrives.
Show(c: &http.Client, a: &Arena, io: Io, out: mut Writer, link: str into a): http.Error|url.Error!void {
    u := try url.Parse(a, link, none)
    req := http.ClientRequest(method: "GET", url: &u, headers: NoHeaders, body: NoBody)
    var x := try c.Do(a, io, &req) // the head is read; the body is not yet
    onerr x.Abandon()
    var buf: [256]u8 = [0; 256]
    var body := x.Body(mut buf[..])
    var piece: [64]u8 = [0; 64]
    for {
        n := try body.Read(mut piece[..])
        if n == 0 {
            break // the end of the body
        }
        try out.Bytes(piece[..n])
    }
    try x.Close(io) // the body was read to its end, so the connection goes back to the pool
}

export Main(sys: &System): http.Error|url.Error|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    link := try sys.arena.Fmt("http://127.0.0.1:{port}/count", (port: l.Port()))
    counter := Counter(n: 3)
    c := http.Client.New(sys.heap, http.DefaultClientLimits)
    defer c.Close()
    scope s := sys.io.Scope() {
        s.go Server(sys.heap, sys.io, &l, &counter)
        onerr s.Cancel()
        var out := sys.Stdout()
        try Show(&c, sys.arena, sys.io, mut out, link)
        s.Cancel()
    }
}
```

```text
$ mk run stream.mk
line 1
line 2
line 3
```

- The first `Flush` sends the head with `Transfer-Encoding: chunked`; each one after sends what was added since as a chunk.
- A `Writer` borrows the response, so the handler makes a new one for each line and flushes after its last use.
- `x.Close` after the whole body returns the connection to the client's pool for the next request to the same origin.

## API

Generated from the module's doc comments by `mk doc --std std.http` ([reading the API](README.md#reading-an-api-section)).

<!-- api:begin -->
Stability: Stable.

### `BadRequest`

```
export error BadRequest(offset: int)
```

A malformed request or reply. offset is the byte where reading stopped,
counted from the first byte read for it.

### `BadUrl`

```
export error BadUrl
```

A client request's URL, or a redirect's Location, is not an http or
https URL the client can send to, or ClientLimits.proxy is not an http
URL with a host and no userinfo.

### `BodyNotReplayable`

```
export error BodyNotReplayable
```

A redirect asked to send again a body that DoStreaming read from a
Reader, which cannot be read twice.

### `Client`

```
export linear mut view struct Client
```

An HTTP/1.1 client with a pool of kept-alive connections. One client
serves every task of a program: it is linear, Send and Share, and its
pool is behind a lock of its own. End it with Close.

It keeps h and its limits, with what they borrow, such as the
tls.Config behind secure, so it cannot outlive them. Its connections,
secure ones included, live on the process heap, so a pooled connection
outlives every request's arena.

### `Client.Close`

```
export Client.Close(self)
```

Releases the client: every idle connection is closed without speaking
to the peer, and the pool's memory goes back to the heap. An open
Exchange borrows the client, so none can be open here. Never blocks or
fails. O(idle connections); allocates nothing.

### `Client.Do`

```
export Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Exchange from a, self
```

- Flows: The result borrows from a and self.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Sends req and reads the reply's head, following redirects, and returns
an Exchange that holds the connection and reads the body. End the
Exchange with Close or Abandon. It uses an idle pooled connection to
the same origin when there is one. O(bytes sent and read); allocates
the head, the fields, the connection's read buffer and the body's
Readers in a, which holds the connection until the Exchange ends.

Redirects: a 301, 302, 303, 307 or 308 whose Location is an http or
https URL is followed, up to the limits' redirects (TooManyRedirects
past them; with 0 none is followed and the 3xx reply is returned as it
is). The redirect's body is read and dropped, up to 64 KiB, else its
connection is closed. A 303, and a 301 or 302 after a POST, is followed
with GET and no body. When a redirect changes the origin, Authorization,
Proxy-Authorization and Cookie are not sent to the new one; the client
never sends URL userinfo. A Location that is not an http or https URL
ends the following, and that reply is returned.

Each connection: allow is asked about each resolved address, and the
client dials the allowed ones in turn until one connects (Refused when
allow refuses every one, else the last dial's error). https
needs secure (NoSecureTransport). The connect limit bounds resolving,
dialling and the handshake. With the limits' proxy, every connection is
the proxy's: allow is asked about its addresses, and the proxy resolves
the origin's name. An http request goes to it in absolute form, and an
https one through a CONNECT tunnel, whose reply head the connect limit
and header_bytes bound. A CONNECT status other than 2xx is
ProxyRefused(code), and a proxy that is not an http URL with a host and
no userinfo is BadUrl.

The reply's head is bounded by header_bytes (TooLarge) and its arrival
by response_header (time.TimedOut). Its framing is checked as strictly
as a request's: BadRequest at the offset in the reply. The fields sent
are the request's, with Host, Connection, Content-Length,
Transfer-Encoding and, when the client has decoders, Accept-Encoding
written by the client itself. A pooled connection that the server
closed while it was idle is retried once on a new connection, when
nothing of the reply was read and the method is idempotent. A cancel
while it waits is Canceled, or time.TimedOut once response_header has
passed; either way its connection is closed.

### `Client.DoStreaming`

```
export Client.DoStreaming(&self, a: &Arena, io: Io, req: &ClientRequest, body: mut Reader, len: ?i64): Error!Exchange from a, self
```

- Flows: The result borrows from a and self.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Sends req as Do does, with the request's body read from body instead of
req.body, which is not sent: for an upload that need not fit in memory.
With some(len), it sends len bytes with `Content-Length: len`; with
none, every byte up to body's end, in chunks of at most 16 KiB, with
`Transfer-Encoding: chunked`. O(bytes sent and read); the body passes
through a 16 KiB buffer on the stack, and it allocates as Do does.

A redirect that would send the body again (307, 308, and 301 or 302 for
a method other than POST) fails with BodyNotReplayable after its reply
is drained. One that drops the body (303, or 301 and 302 after POST) is
followed as Do follows it. When body fails, or ends before len bytes,
the call fails with ReadError and the connection is closed. A reply the
server sends before the whole body is sent is read only after it. A
pooled connection the server closed is retried on a new one only when
no byte was read from body. Otherwise it fails as Do does.

### `Client.Fetch`

```
export Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Reply from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Sends req as Do does, reads the whole body into a, and closes the
exchange: for a reply that fits in memory. O(bytes sent and read);
allocates the reply's fields and body in a, the body in buffers that
double from 16 KiB, up to about four times its size, and Do's working
memory in an arena of its own, freed before it returns.

A decoded body over the limits' body_bytes is TooLarge, and so is a
reply not decoded whose Content-Length passes it, before a byte of its
body is read. A failed body read is ReadError, or Canceled under a
cancelled scope. Otherwise it fails as Do does.

### `Client.New`

```
export Client.New(h: &Heap, l: ClientLimits): Client from h, l
```

- Flows: The result borrows from h and l.

A client with the limits l and an empty pool. O(16): the pool has 16 slots;
allocates nothing: the pool's table is inside the client.

### `ClientLimits`

```
export struct ClientLimits
```

The settings of a Client: how long it waits, how much it reads, where it
connects and what it decodes. Start from DefaultClientLimits and assign
the fields to change; a literal of it does not compile outside std. No
limit is zero by default, and a time limit that passes fails with
time.TimedOut.

Fields:

- `connect: time.Duration`: The time for resolving, connecting and the secure handshake, per
  connection.
- `response_header: time.Duration`: The time from the request's last byte sent to the reply's head read
  whole.
- `idle: time.Duration`: How long an idle pooled connection may wait for its next request.
- `close: time.Duration`: The time a Close that speaks to the peer may take, such as a secure
  connection's close.
- `header_bytes: int`: The bytes of a reply's head, interim replies included.
- `body_bytes: i64`: The decoded bytes Client.Fetch reads into its Reply; more is
  TooLarge.
- `redirects: int`: The redirects followed before TooManyRedirects; 0 follows none and
  returns the 3xx reply as it is.
- `idle_connections: int`: The connections the client keeps for reuse, over every origin, at
  most 16; 0 keeps none.
- `decoders: []compress.Content`: The content codings the client advertises in Accept-Encoding and
  undoes, in order of preference; empty sends no Accept-Encoding and
  undoes nothing.
- `allow: ?fn(&net.Addr): bool`: Asked before every connect, redirects included, with the address
  about to be dialled; false refuses it, and an address refused fails
  the connect as Refused. A service that fetches URLs it is given
  refuses loopback and private ranges here. With a proxy, the address
  dialled is the proxy's.
- `secure: ?net.Secure`: The connector that https URLs go through, made by tls.Connector.
  With none, an https URL fails with NoSecureTransport.
- `proxy: ?&url.Url`: The HTTP proxy every connection goes through: an http URL with a
  host, an optional port (80 by default) and no userinfo, else BadUrl.
  Its path and query are not used. An http request is sent to it in
  absolute form, and an https one through a CONNECT tunnel, then the
  secure handshake with the origin. allow is then asked about the
  proxy's addresses only: the proxy, not the client, resolves the
  origin's name. none connects directly. std reads no proxy variable
  from the environment.

### `ClientRequest`

```
export struct ClientRequest
```

One request for a Client: the method, the URL (http or https, parsed by
std.url), the header fields to send and the body, which is sent with a
Content-Length. A plain record that borrows all four.

Fields:

- `method: str`: The method, such as "GET".
- `url: &url.Url`: The URL, http or https.
- `headers: []Header`: The header fields to send.
- `body: []u8`: The body, sent with a Content-Length.

### `DefaultClientLimits`

```
export const DefaultClientLimits: ClientLimits
```

The client's default settings: 30 s to connect and 30 s to wait for a
reply's head, 90 s idle, 5 s to close, a 1 MiB head, a 64 MiB body for
Client.Fetch, 10 redirects, 16 kept connections, no decoders, every
address allowed, no https and no proxy.

### `DefaultServerOptions`

```
export const DefaultServerOptions: ServerOptions
```

The server's default settings: 10,000 connections, a 32 KiB head, an
8 MiB body, 30 s for a request's head and 75 s between requests, and
plain TCP.

### `Dispatch`

```
export Dispatch[C](a: &Arena, io: Io, routes: []Route[C], c: &C, req: &Request, res: mut Response): Error!void *routes into a
```

- Flows: Stores the loans routes holds in a.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Calls the handler of the most specific route whose method and pattern
match req, and passes its errors through. One scan of the table,
O(routes x segments); allocates the params and their values in a.

Matching: a literal segment matches the path's segment percent-decoded,
so /ab matches /a%62. Where two patterns match, the first segment where
they differ decides: a literal beats {name}, which beats {name...}, so a
longer pattern beats a shorter one ending in {name...}. Between equal
patterns, a HEAD request prefers a HEAD route to a GET route, then the
earlier route wins. A HEAD request also matches a GET route, and its
reply carries no body.

The handler gets a copy of req whose params hold the path's segments
for the pattern's {name} and {name...} segments, decoded and copied
into a, so a holds no loan of req. The names are views of the route's
pattern (`*routes into a`).

Errors: a path with a "." or ".." segment, plain or percent-encoded, is
Status(400) before any route is tried, since Request.path is never
cleaned. When routes match the path with other methods only, it is
Status(405) with res's Allow field naming them; when none matches, it
is Status(404).

### `Error`

```
export open type Error = BadRequest | TooLarge | Status | net.Error | OutOfMemory | ReadError | WriteError | NoSecureTransport | BodyNotReplayable | TooManyRedirects | BadUrl | ProxyRefused
```

Every way std.http fails. ReadError is a failed read of the Reader a
request or reply comes from, and WriteError a failed write. net.Error
brings Canceled, Closed and time.TimedOut. The set is open, so a match
on it outside std keeps a `_` arm.

### `Exchange`

```
export linear struct Exchange
```

A reply that Client.Do read: the status, the header fields, and the
body through Body. It holds the connection until Close or Abandon. It
is linear, and it borrows its arena and the client.

When the client decoded the body, headers leaves out Content-Encoding
and Content-Length, which describe the encoded bytes.

Fields:

- `status: int`: The reply's status code, such as 200.
- `headers: []Header`: The reply's header fields in the order received.

### `Exchange.Abandon`

```
export Exchange.Abandon(self)
```

Ends the exchange and closes its connection without speaking to the
peer. Use it on error paths. Never blocks or fails. O(1); allocates
nothing.

### `Exchange.Body`

```
export Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
```

- Flows: The result borrows from self and buf.

A Reader of the body, with buf as its buffer. O(1); allocates nothing.

The body is decoded when the reply's Content-Encoding names one coding
that is among the client's decoders; with none, another, or several
codings, the bytes come as sent. Read returns 0 at the body's end, never
a byte of the next reply. Only the reply's framing limits the body, so
bound a streamed body's decoded size yourself. A failed read (the
connection, the framing, the decoder, or a cancel) is ReadError, and
every later read fails too; the connection then never returns to the
pool.

### `Exchange.Close`

```
export Exchange.Close(self, io: Io): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Ends the exchange. When the body was read to its end and the reply let
the connection stay open, the connection goes back to the client's pool
for the next request to its origin; otherwise it is closed. O(1);
allocates nothing.

Closing a secure connection speaks to the peer, bounded by the client's
close limit: time.TimedOut past it, a cancel after it passed included,
and Canceled under a cancelled scope before. After a failed body read,
Close fails with ReadError and the connection is closed. On any error
the connection is released anyway and the Exchange stays the caller's:
write `onerr x.Abandon()` with `try x.Close(io)`.

### `Fetch`

```
export Fetch(a: &Arena, io: Io, method: str, url: str, headers: []Header, body: []u8, limit: int): Error!Reply from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Sends one request over a new plain connection and reads the whole
reply into a. url is an http URL; Fetch has no https, no pool, no
redirects and no limits record: use a Client for those. O(n) in the
bytes sent and read; allocates the request head and the reply, head
and body, in a.

Errors: a URL that is not http://host[:port][/path][?query] is
BadRequest at the offset in url where it stops being one. The reply's
bytes, heads and body together, interim replies included, are bounded
by limit: TooLarge one byte past it (a negative limit acts as 0). A
malformed reply is BadRequest at its offset. A read or write cut off by
a cancel of io's scope is Canceled; another read failure is ReadError,
and a write failure WriteError. Connecting fails with net.Error.

The request carries Host, then headers as given, with CR, LF and NUL in
a value replaced by a space. A field whose name is not a token is left
out, and so are Host, Connection, Content-Length and Transfer-Encoding,
which Fetch writes itself, with `Connection: close`. Content-Length is
written when body is not empty or the method is POST, PUT or PATCH. Up
to 5 interim 1xx replies other than 101 are read and dropped; a sixth is
BadRequest. The reply's fields are views of its head in a.

### `Header`

```
export struct Header
```

One header field: its name and value, as they were read or as they are
to be written.

Fields:

- `name: str`: The field name, such as "Content-Type".
- `value: str`: The field value.

### `Link`

```
export view struct Link
```

A ServeWith handler's handle on the connection its request came on.
Response.Flush writes through it, and Takeover takes the connection
from it. The server makes one per request and passes it beside the
response, never inside it, so nothing a handler stores into its
response reaches the connection.

### `Link.Takeover`

```
export Link.Takeover(&self): ?Taken
```

- Flows: The result borrows from self.

Takes the connection the handler's response would be sent on, with the
bytes the server read after the request, for websocket.Accept or a
protocol of your own; none when it was already taken. O(1); allocates
nothing.

After it, the server writes nothing on the connection, not even the
handler's response, and stops serving the connection when the handler
returns. The Taken holds the link's loans, so it lives inside the
handler.

### `NoSecureTransport`

```
export error NoSecureTransport
```

An https URL, and the client's limits name no secure connector. Put a
tls.Connector in ClientLimits.secure to send https requests.

### `ProxyRefused`

```
export error ProxyRefused(code: int)
```

The client's proxy answered its CONNECT with code, a status other than
2xx, so no tunnel was made.

### `ReadRequest`

```
export ReadRequest(a: &Arena, io: Io, r: mut Reader, limit: int): Error!Request from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Reads one request from r into a: the head, then a Content-Length or
chunked body. Use it to read requests from a stream of your own; Serve
and ServeWith read theirs the same way. O(n) in the request's bytes;
the head and the body are allocated in a. A Content-Length body takes
buffers of 16 KiB and then doubling, up to about three times its size;
a chunked body, whose size is not known before its end, the same up to
about four times its size (at least 16 KiB). The request's fields are
views of its head in a.

The head is read one line per Reader.Until, from the reader's buffer,
never a byte past the head's blank line, so no byte of a pipelined next
request is taken. The body grows into a buffer as its bytes arrive
(from 16 KiB, doubling), so a declared length or chunk size commits no
memory before its bytes come.

Errors: limit bounds the head plus the body's bytes, and one byte over
is TooLarge. End of input before the first byte is Closed, and end
of input inside the request, or a malformed request, is BadRequest at
its offset. A failed read of r is Canceled when io's scope is
cancelled, else ReadError.

### `Reply`

```
export struct Reply
```

A whole reply, as Fetch and Client.Fetch return it: the status code, the
header fields in the order received, and the body. Every string and
slice is a view of the arena it was read into.

Fields:

- `status: int`: The status code, such as 200.
- `headers: []Header`: The header fields in the order received.
- `body: []u8`: The body.

### `Request`

```
export struct Request
```

A request as the server read it: the method, the target split into path
and query, the header fields in the order received, the path parameters
Dispatch matched, and the body. path and query are the bytes received,
never decoded or cleaned. Every string and slice is a view of the
request's arena.

Fields:

- `method: str`: The method, such as "GET".
- `path: str`: The target's path, as received.
- `query: str`: The target's query, as received.
- `headers: []Header`: The header fields in the order received.
- `params: []Header`: The path parameters Dispatch matched: each pattern name with the text it
  matched.
- `body: []u8`: The body.

### `Request.Header`

```
export Request.Header(&self, name: str): ?str from self
```

- Flows: The result borrows from self.

The value of the first header field named name, or none when there is
none. Names compare without regard to ASCII case. O(fields times name
length); allocates nothing.

### `Request.Param`

```
export Request.Param(&self, name: str): ?str from self
```

- Flows: The result borrows from self.

The value of the path parameter name, which Dispatch filled from the
route's pattern, or none when the route has no such parameter. Names
compare exactly. O(parameters); allocates nothing.

### `Request.ParamInt`

```
export Request.ParamInt(&self, name: str): ?i64
```

The path parameter name read as a decimal i64, or none when it is
absent or not a number. O(parameters plus the value's length); allocates
nothing.

### `Response`

```
export view struct Response
```

A response under construction: a status, header fields and a body, all
in the request's arena. The server hands one to each handler and sends
it when the handler returns.

Header and Body copy what they are given into the arena, so a handler
stores no loan of its own into the response.

### `Response.Body`

```
export Response.Body(mut self, data: []u8): OutOfMemory!void
```

- Errors: OutOfMemory

Sets the body to a copy of data, in the response's arena; what Writer
writes follows it. Fails with OutOfMemory when the arena is full. O(n);
allocates data's length in the arena.

### `Response.Flush`

```
export Response.Flush(mut self, io: Io, link: &Link): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Sends what the response holds so far, before the handler returns: for
a reply produced over time, such as a long listing or server events.
O(bytes sent); allocates nothing.

The first Flush sends the head, with Transfer-Encoding: chunked in place
of the handler's framing fields, and the body so far as a chunk. Each
later Flush sends what was added since as another chunk: the bytes of a
Body set after the last Flush, whole, and what the Writer appended
since. When the handler returns, the server sends the rest and the last
chunk. To an HTTP/1.0 request the head carries no length and the body
ends with the connection; a 204, a 304 or a reply to HEAD sends its
head alone. Status and fields set after the first Flush are not sent,
and an error the handler returns after it cuts the body short.

Errors: a status that is not final (below 200) fails with Status(code)
and sends nothing. A write that fails, or a piece the client does not
take within the server's request_header, is WriteError (Canceled under
a cancelled scope). The server then sends nothing more on the
connection and closes it after the handler, whatever the handler
returns, and every later Flush fails with WriteError and sends
nothing. When link's connection was taken, Flush sends nothing and
does not fail.

### `Response.Header`

```
export Response.Header(mut self, name: str, value: str): OutOfMemory!void
```

- Errors: OutOfMemory

Adds a header field, copied into the response's arena. Fails with
OutOfMemory when the arena is full. O(n) in the field's length;
allocates name and value in the arena.

CR, LF and NUL in value become spaces, so a value cannot split the
response. A field whose name is not a token is not sent.

### `Response.New`

```
export Response.New(a: &Arena): Response
```

- Flows: The result borrows from a.

A 200 response with no fields and an empty body, in a. O(1); allocates
nothing until a field or body bytes are added.

### `Response.Send`

```
export Response.Send(&self, io: Io, w: mut Writer): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Writes the response to w, then flushes w. The server calls it; call it
yourself to answer a request read with ReadRequest. Fails with w's
WriteError. O(n) in the reply's bytes; allocates nothing.

Send writes the framing itself, so it always matches the body. A 1xx,
204 or 304 carries no body and no Content-Length or Transfer-Encoding,
and a response to HEAD carries its length but not its bytes. With the
handler's `Transfer-Encoding: chunked`, the body goes out in chunks of
at most 16 KiB and the last chunk, except to an HTTP/1.0 request, which
gets no Transfer-Encoding. Otherwise Send writes the body's own
Content-Length in place of the handler's. A code outside 100 to 999
panics; the server answers 500 before that. After Flush, Send writes
only the body bytes added since, in the framing Flush chose, and the
last chunk.

### `Response.Status`

```
export Response.Status(mut self, code: int)
```

Sets the status code. O(1); allocates nothing.

The server answers a code outside 200 to 999 with 500 and ends the
connection, since an interim 1xx is never a final reply. Send called
directly panics on a code outside 100 to 999.

### `Response.Writer`

```
export Response.Writer(mut self): Writer from self
```

- Flows: The result borrows from self.

A Writer that appends text to the body, after the Body bytes. The bytes
written must be UTF-8, else the write fails with WriteError; use Body
for other bytes. O(1); allocates nothing until written to.

### `Route`

```
export struct Route[C]
```

One row of a Dispatch table: a method, a path pattern, and the handler
that answers it. A pattern is literal segments, `{name}` segments that
match one segment, and a last `{name...}` segment that matches the rest
of the path. The handler gets the context C that Dispatch is given.
Declare the table as a `const []Route[C]`, so every task can read it.

Fields:

- `method: str`: The method the row answers, such as "GET".
- `pattern: str`: The path pattern: literal segments, `{name}` segments and a last
  `{name...}` segment.
- `handler: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void`: The function that answers a request that matches, given the context
  Dispatch is given.

### `Serve`

```
export Serve[C: Share](sys: &System, addr: str, limit: int, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Listens on addr and serves every connection in a task of its own, with
keep-alive and pipelining, until the caller's scope is cancelled; then
it returns, once every connection's task has ended. Each request is
read with limit as its bound (head plus body) and handed to handle with
an arena of its own, reset after each response. Every task calls handle
with the one c, so C is Share. Chunked request bodies are read whole
under limit, and `Expect: 100-continue` is answered before the body is
read. Use ServeWith for time limits, TLS, a cap on connections and the
other settings. O(bytes served) per
connection, with O(fields) per request for the head's parse. Each
connection allocates one arena from sys.heap; its read and write
buffers are on its task's stack.

Replies the server makes itself: a handler's Status(code) is answered
with that code, the fields it set and an empty body. A code outside 200
to 999, or any other handler error, is answered 500, and the connection
ends. A handler's `Connection: close` ends the connection after the
reply. A malformed request is answered 400, a request over limit 413,
and a head over 32 KiB 431, and the connection ends.

Errors: Serve fails with Listen's error, or with Accept's Closed or
InvalidAddress. Another Accept error (out of handles, or an error of the
connection being accepted) waits 5 ms and tries again, doubling the wait
up to 1 s. Serve has no time limits: a client that sends nothing keeps
its connection.

### `ServeChan`

```
export ServeChan[C: Share, T: Send](h: &Heap into ch, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, ch: &Chan[T], handle: fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Serves every connection l accepts as ServeOn does, with o's settings,
until the caller's scope is cancelled, and hands each call of handle
the heap h and the channel ch beside c: for a server whose handlers
send values to the program's other tasks, such as a chat server's
rooms. O(bytes served) per connection; each connection allocates one
arena from h, reset after each response.

The handler may store h into ch (`h: &Heap into ch`), so it may send a
value made from h, such as a sync.Shared frame, into ch. No other loan
may reach ch:

- not c's loans, so a frame comes from h and not from a heap that c
  holds;
- not the request, its arena or the link, so bytes of a request are
  copied into the frame;
- not a value the handler received from ch that holds a loan, such as a
  frame, so a handler does not send on a frame it received. A plain
  value such as an int it may send.

The call declares the same store, so the caller's ch may hold h, and the
caller empties and frees ch before h goes. ch is reached from every
connection's task, so T is Send. Fails with Accept's Closed or
InvalidAddress; other Accept errors wait and try again as Serve's do.

### `ServeOn`

```
export ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Serves every connection l accepts, as ServeWith does with o's settings,
until the caller's scope is cancelled. The caller makes the listener, so
it can read l.Port() before it serves (a listener on port 0), and l
stays open when ServeOn returns, to close or serve again. O(bytes
served) per connection; each connection allocates one arena from h,
reset after each response.

It takes h and io where ServeWith takes &System, which is not Share, so
a task of the program can run it. A cancel closes the connections being
served, unanswered; connections still waiting in l's queue are left for
the next Accept.

l must be a listener of io, or of the Io that io forwards to (a
sim.Recorder's): a machine listener served with a Sim's Io fails with
Closed, and a Sim's listener served with the machine's Io waits and
tries again until the cancel. Fails with Accept's Closed or
InvalidAddress; other Accept errors wait and try again as Serve's do.

### `ServeWith`

```
export ServeWith[C: Share](sys: &System, addr: str, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, ReadError, WriteError, BadRequest(offset: int), BadUrl, BodyNotReplayable, NoSecureTransport, ProxyRefused(code: int), Status(code: int), TooLarge, TooManyRedirects, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut)

Listens on addr and serves every connection in a task of its own until
the caller's scope is cancelled, as Serve does, with o's settings and a
Link beside each response. O(bytes served) per connection; each
connection allocates one arena from sys.heap, reset after each
response.

It serves at most o.connections at once; more wait in the listen queue.
A head over header_bytes is answered 431 and a body over body_bytes
413. A connection is closed when it waits longer than keep_alive
between requests, when a head takes longer than request_header from its
first byte, when a read of a body waits longer than request_header, and
when its client takes no piece of a reply (64 KiB at most) within
request_header. Chunked request bodies are read whole into the
request's arena, and `Expect: 100-continue` is answered before the body
is read. With o.secure, each connection goes through the acceptor first,
and a failed handshake closes it.

Fails with Listen's error, or with Accept's Closed or InvalidAddress;
other Accept errors wait and try again as Serve's do. ServeWith is
net.Listen, then ServeOn over the listener, then its Close.

### `ServerOptions`

```
export struct ServerOptions
```

The settings of ServeWith, ServeOn and ServeChan: the connections served
at once, the bytes of a request's head and body, the time a request may
take, and the time a kept-alive connection may wait for the next
request. Start from DefaultServerOptions and assign the fields to
change; a literal of it does not compile outside std.

Fields:

- `connections: int`: The connections served at once; more wait in the listen queue.
- `header_bytes: int`: The bytes of a request's head; a longer head is answered 431. A
  head over 8 KiB is read into a buffer that grows with its bytes up
  to this.
- `body_bytes: int`: The bytes of a request's body, chunked framing included; a longer
  body is answered 413. A chunked body is read whole under this limit.
- `request_header: time.Duration`: The time from a request's first byte to its head read whole. It also
  bounds each read of the body (a body that sends no byte for this
  long closes the connection) and each write of the server's own (a
  reply, or 100 Continue) waiting for the client to take a piece.
- `keep_alive: time.Duration`: The time from a reply sent to the next request's first byte.
- `secure: ?net.Secure`: The acceptor each connection goes through, made by tls.Acceptor.
  With none, the server speaks plain TCP.

### `Status`

```
export error Status(code: int)
```

A handler's answer with a status code: the server replies with code and
an empty body. Dispatch fails with it too, for 400, 404 and 405.

### `Taken`

```
export linear struct Taken
```

A connection taken from its server, as Link.Takeover returns it: conn,
and rest, the bytes the server read past the request, such as a
client's first frames sent right behind its handshake. rest comes
before anything read from conn. It is linear: websocket.Accept consumes
it, or Abandon closes it. It keeps the link's loans, so it lives inside
the handler that took it.

Fields:

- `conn: net.Conn`: The connection.
- `rest: []u8`: The bytes the server read past the request, which come before anything
  read from conn.

### `Taken.Abandon`

```
export Taken.Abandon(self)
```

Closes the taken connection at once and sends nothing: a TLS connection
sends no close_notify. Never blocks or fails. O(1); frees the
connection's box, if it has one.

### `TooLarge`

```
export error TooLarge
```

A request or reply passed its byte limit.

### `TooManyRedirects`

```
export error TooManyRedirects
```

The reply was still a redirect after the client followed its limit of
redirects.

<!-- api:end -->
