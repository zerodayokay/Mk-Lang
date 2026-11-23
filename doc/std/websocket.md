# std.websocket

`std.websocket` is WebSocket connections (RFC 6455, without extensions). Its client dials a `ws` or `wss` URL, and its server side takes over a request from an [`std.http`](http.md) handler. Either way you get a connection that sends and receives whole messages. Reach for it when a browser or a peer needs a long-lived, two-way channel of messages. For plain request and response use `std.http`, and for your own byte protocol use [`std.net`](net.md). It works on Linux and Windows, and under a simulated `Io` from `std.sim`.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.websocket
```

## Contents

- [The main pieces](#the-main-pieces)
- [Connecting and accepting](#connecting-and-accepting)
- [The connection](#the-connection)
- [Closing](#closing)
- [Options](#options)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Cancellation](#cancellation)
- [Examples](#examples): [an echo server and client](#an-echo-server-and-client), [a server that closes](#a-server-that-closes), [dial a URL that may need TLS](#dial-a-url-that-may-need-tls)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Connect to a server | `Dial(h, a, io, &u, &o)` with a parsed `url.Url` and `Options` |
| Accept a client in an HTTP handler | `link.Takeover()`, then `Accept(h, io, req, t, protocols, allow)` |
| Send a message | `Conn.Send(io, kind, data)` with `Kind.Text` or `Kind.Binary` |
| Receive a message | `Conn.Recv(a, io)`, which returns a `Message` |
| Check that the peer is there | `Conn.Ping` |
| The subprotocol agreed | `Conn.Protocol` |
| End the connection | `Conn.Close(io, code, reason)`, or `Conn.Abandon` |

## Connecting and accepting

`Dial` opens a TCP connection to the URL's host, makes it secure for `wss`, and runs the opening handshake. It offers the subprotocols in `o.protocols` and adds the header fields in `o.headers` (an `Origin`, a cookie, a token). Before any I/O, it fails with `net.InvalidAddress` for a URL that is not `ws` or `wss` or has no host. It fails with `Handshake(0)` for a header field or subprotocol that is malformed, a subprotocol offered twice, or a field the handshake writes itself.

A `wss` URL needs a secure connector in `o.secure`, a `net.Secure` from [`tls.Connector`](tls.md). `std.websocket` imports no TLS of its own, so a program that never names `std.tls` links none. `wss` is [Experimental](README.md#a-stable-module-over-an-experimental-one) as long as `std.tls` is. With `o.secure` `none`, a `wss` URL fails with `http.NoSecureTransport`, which is a member of `websocket.Error`.

The server side starts in an HTTP handler served by `http.ServeWith`, `http.ServeOn` or `http.ServeChan`. `link.Takeover()` takes the connection out of the HTTP server, and `Accept` checks the request, answers with the 101 reply and returns the `Conn`:

- `Accept` answers 400 to a request that is no WebSocket upgrade and 426 to a version other than 13. It answers 403 when `allow` refuses the request's `Origin` (`""` when it has none). It then fails with `Handshake(status)`. On failure the `Taken` connection stays with the caller, to `Abandon`.
- It picks the first subprotocol the client offered that `protocols` lists, and none when they share none. `Conn.Protocol` names the choice on both sides.
- It uses `DefaultOptions`' limits.

## The connection

A `Conn` is **linear**, `Send` and `Share`: tasks share it by reference, one receiving while others send. End it with `Close` or `Abandon`; it has no `Free`.

- `Send` writes one message as one frame, under the connection's write lock, so frames of two senders never interleave. A client masks every frame with a key from `io.Random`.
- `Send` refuses text that is not UTF-8 with `Protocol(1007)`, before anything is sent; the connection stays usable.
- `Recv` returns the next data message, its fragments joined, into the arena you pass. It answers each `Ping` and the peer's `Close` frame itself. Only one `Recv` runs at a time; a second waits for the first.
- `Recv` fails the connection with `Protocol` on a control frame over 125 bytes or fragmented. It does the same on a frame with a reserved bit set or masked the wrong way, and on more unanswered pings than `pending_pongs`. It fails with `TooLarge` past `frame_bytes` or `message_bytes`, before it allocates. In both cases it sends its Close frame first.
- After the connection fails, every call but `Abandon` reports the same failure, and a call waiting in another task returns it at once.

A `Conn` keeps its heap and its `net.Conn`, never the request, URL or `Options` it was made from. One from `Accept` also keeps the `Taken`'s loans, the response's arena among them, so it lives inside the handler that took the connection.

## Closing

`Close(io, code, reason)` sends a Close frame with that status (code 0 sends none), reads up to the peer's Close frame, and closes the `net.Conn`. It fails with `Protocol(code)` for a status an endpoint may not send, and `TooLarge` for a reason over 123 bytes, before anything is sent.

`Close` speaks to the peer, so it can fail. Under a cancelled scope it fails with `Canceled`. When the peer does not answer within `o.close` (5 seconds by default) it fails with `time.TimedOut`, also under a `NoCancel` `Io`. On any failure the `Conn` stays with you, so write `onerr c.Abandon()` with `try c.Close(io, code, reason)`. `Abandon` releases the connection without a word to the peer, and never blocks or fails.

When the peer closes first, `Recv` answers its Close frame and fails with `PeerClosed(code)`; a later `Close` only releases.

## Options

`Options` is an [options type](../codebase.md#options-types): start from `DefaultOptions` and assign the fields you change.

| Field | Default | Meaning |
|---|---|---|
| `headers` | empty | Header fields `Dial` adds to its request |
| `protocols` | empty | Subprotocols `Dial` offers, in order of preference |
| `secure` | `none` | The connector for `wss` URLs |
| `frame_bytes` | 16 MiB | The longest frame payload `Recv` takes |
| `message_bytes` | 16 MiB | The longest message `Recv` takes, fragments joined |
| `pending_pongs` | 16 | Pongs that may be owed while another task holds the write lock |
| `close` | 5 s | How long a `Close`, or a reply `Recv` writes, waits for the peer |

The connection copies the limits, and keeps no reference into `Options`.

## Errors

| Member | Meaning |
|---|---|
| `Protocol(code)` | The peer broke RFC 6455, or a value you passed is one it forbids. `code` is the close status sent for it: 1002 for a bad frame, 1007 for text that is not UTF-8, 1008 for a peer that pings faster than its pongs can go out |
| `TooLarge` | A frame or message over its limit (the peer is sent status 1009), a handshake head over 16 KiB, or a ping payload or close reason too long for a control frame |
| `PeerClosed(code)` | The peer closed: `code` is its status, 1005 when it sent none, 1006 when the connection ended without a Close frame |
| `Handshake(status)` | The opening handshake failed: the HTTP status the server answered or `Accept` sent, 0 when no request went out or the reply was not HTTP/1.1, 101 for a 101 reply that breaks the handshake rules |
| `http.NoSecureTransport` | A `wss` URL with no secure connector |
| any `net.Error` | The connection's failure, `time.TimedOut` for a `Close` past its limit included |
| `OutOfMemory` | An allocation failed (from the prelude) |

`websocket.Error` is open, so a `match` on it outside std keeps a `_` arm. A peer's bytes never make a `Conn` panic, and its memory stays within the limits in `Options`.

## Memory and cost

The connection's state lives on the heap passed to `Dial` or `Accept`, and is freed by `Close` or `Abandon`. It is about 33 KiB: a 16 KiB read buffer, a 16 KiB write buffer and the limits. `Dial` also puts its request and the resolved addresses in its arena. `Recv` puts each message in the arena you pass, and a fragmented message on the heap until it is whole. `Send`, `Ping` and `Close` allocate nothing. `Send`, `Ping` and `Recv` cost O(n) in the bytes they move, `Dial` costs O(n) in the size of its request and reply heads, and `Accept` O(the request's fields times `protocols`).

## Cancellation

- `Recv` can be cancelled without loss: the connection keeps a partly read frame or message, and any reply it owes, and the next `Recv` goes on.
- `Send` and `Ping` return `Canceled` with nothing sent while they wait for the write lock. A write cancelled part way fails the connection.
- `Dial` cancelled part way releases what it made. `Accept` cancelled part way may have sent part of its reply.
- `Close` is cancellable and bounded, as above.

## Examples

The first two programs run as written with `mk run`: each serves HTTP on a loopback port in one task and connects to it from `Main`.

### An echo server and client

```mk
// verdict: ok
import std.http
import std.net
import std.url
import std.websocket

// App is what every handler task shares: here only the heap that
// WebSocket connections keep their state on.
struct App {
    heap: &Heap
}

AnyOrigin(origin: str): bool {
    return true
}

// Echo is the HTTP handler: it takes the connection over as a WebSocket
// and sends back each message it receives until the client closes.
Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    t := link.Takeover() else {
        return
    }
    protocols: [1]str = ["echo"]
    ws := websocket.Accept(c.heap, io, req, t, protocols[..], AnyOrigin) else {
        t.Abandon()
        return
    }
    for {
        m := ws.Recv(a, io) else {
            ws.Abandon() // the client closed, or the connection failed
            return
        }
        ws.Send(io, m.kind, m.data) else {
            ws.Abandon()
            return
        }
    }
}

// Server serves HTTP on l until its scope is cancelled.
Server(h: &Heap, io: Io, l: &net.Listener, o: &http.ServerOptions, app: &App) {
    http.ServeOn(h, io, l, o, app, Echo) else {}
}

export Main(sys: &System): http.Error|websocket.Error|url.Error|Utf8Error|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    app := App(heap: sys.heap)
    o := http.DefaultServerOptions
    link := try sys.arena.Fmt("ws://127.0.0.1:{p}/echo", (p: l.Port()))
    u := try url.Parse(sys.arena, link, none)
    scope s := sys.io.Scope() {
        s.go Server(sys.heap, sys.io, &l, &o, &app)
        var wo := websocket.DefaultOptions
        offered: [1]str = ["echo"]
        wo.protocols = offered[..]
        var ws := try websocket.Dial(sys.heap, sys.arena, sys.io, &u, &wo)
        onerr ws.Abandon()
        try sys.Print("subprotocol: {p}\n", (p: ws.Protocol() else { "none" }))
        for text in ["hello", "WebSocket"] {
            try ws.Send(sys.io, websocket.Kind.Text, text.AsBytes())
            m := try ws.Recv(sys.arena, sys.io)
            try sys.Print("echo: {t}\n", (t: try str.From(m.data)))
        }
        try ws.Close(sys.io, 1000, "done")
        s.Cancel() // stop the server
    }
}
```

```text
$ mk run echo.mk
subprotocol: echo
echo: hello
echo: WebSocket
```

- The handler's `Recv` fails with `PeerClosed` when the client closes; it has already answered the client's Close frame, so `Abandon` only releases.
- The handler receives into `a`, the request's arena, which lives as long as the handler, so every message stays there until the connection ends. A long-lived connection receives into an arena it resets after each message.

### A server that closes

```mk
// verdict: ok
import std.http
import std.net
import std.url
import std.websocket

// App is what every handler task shares: here only the heap that
// WebSocket connections keep their state on.
struct App {
    heap: &Heap
}

AnyOrigin(origin: str): bool {
    return true
}

// Bye is the HTTP handler: it takes the connection over as a WebSocket,
// sends one message and closes with status 1001, "going away".
Bye(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    t := link.Takeover() else {
        return
    }
    ws := websocket.Accept(c.heap, io, req, t, websocket.DefaultOptions.protocols, AnyOrigin) else {
        t.Abandon()
        return
    }
    ws.Send(io, websocket.Kind.Text, "server restarting".AsBytes()) else {
        ws.Abandon()
        return
    }
    ws.Close(io, 1001, "restart") else { // waits for the client's Close frame
        ws.Abandon()
    }
}

// Server serves HTTP on l until its scope is cancelled.
Server(h: &Heap, io: Io, l: &net.Listener, o: &http.ServerOptions, app: &App) {
    http.ServeOn(h, io, l, o, app, Bye) else {}
}

export Main(sys: &System): http.Error|websocket.Error|url.Error|Utf8Error|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    app := App(heap: sys.heap)
    o := http.DefaultServerOptions
    link := try sys.arena.Fmt("ws://127.0.0.1:{p}/news", (p: l.Port()))
    u := try url.Parse(sys.arena, link, none)
    scope s := sys.io.Scope() {
        s.go Server(sys.heap, sys.io, &l, &o, &app)
        wo := websocket.DefaultOptions
        ws := try websocket.Dial(sys.heap, sys.arena, sys.io, &u, &wo)
        onerr ws.Abandon()
        for {
            m := ws.Recv(sys.arena, sys.io) else e {
                match e {
                    websocket.PeerClosed(code) { try sys.Print("peer closed with {code}\n", (code)) }
                    _ { try sys.Print("failed: {e}\n", (e)) }
                }
                break
            }
            try sys.Print("message: {t}\n", (t: try str.From(m.data)))
        }
        try ws.Close(sys.io, 0, "") // the peer closed first: this only releases
        s.Cancel() // stop the server
    }
}
```

```text
$ mk run bye.mk
message: server restarting
peer closed with 1001
```

- `Recv` answers the server's Close frame before it fails with `PeerClosed`, so the server's `Close` returns.

### Dial a URL that may need TLS

This function compiles as written; it needs a server to talk to, so it has no output here. It returns `none` for a `wss` URL, since its options name no secure connector.

```mk
// verdict: ok
// websocket.Dial's result is from h; its error set holds NoSecureTransport
import std.http
import std.url
import std.websocket

Open(h: &Heap, a: &Arena, io: Io, link: str into a): url.Error|websocket.Error!?websocket.Conn from h {
    u := try url.Parse(a, link, none)
    o := websocket.DefaultOptions
    c := websocket.Dial(h, a, io, &u, &o) else e {
        match e {
            http.NoSecureTransport { return none }
            _ { fail e }
        }
    }
    return some(c)
}
```

- To dial `wss`, set `o.secure = some(tls.Connector(&cfg))` with a [`tls.Config`](tls.md) that outlives the call.
- The `Conn` keeps only `h`: `u`, `o` and the arena may go once `Dial` returns.

## API

<!-- api:begin -->
Stability: Stable.

### `Accept`

```
export Accept(h: &Heap, io: Io, req: &http.Request, t: http.Taken, protocols: []str, const allow: fn(origin: str): bool): Error!Conn from h, t
```

- Flows: The result borrows from h and t.
- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Accepts the WebSocket handshake of req on the server side, over t, the
connection the HTTP handler took with Link.Takeover. The connection
uses DefaultOptions' limits. allow decides on the request's Origin
field ("" when absent).

The 101 reply names the first subprotocol the client offered (in the
order of its Sec-WebSocket-Protocol fields and of each field's list)
that protocols lists byte for byte, and none when they share none or
protocols is empty; Conn.Protocol then names it. An offered name that
is not a token, or is longer than 16,243 bytes, is never chosen.

On success the Conn holds t's connection and starts with the bytes t
had read past the request. It keeps t's loans, the response's arena
among them, so it lives inside the handler. On failure t stays the
caller's, to Abandon. Errors: Handshake(400) for a request RFC 6455
4.2.1 refuses (not GET, no Upgrade of websocket, no Connection holding
upgrade, a key that is not 16 bytes in base64 or appears twice, more
read past the request than 16 KiB), Handshake(403) when allow refuses
the origin, Handshake(426) for a version other than 13, each after
answering the client with that status; the connection's net.Error;
OutOfMemory, before any byte of the 101 is written. After a cancel,
part of the reply may have been sent: abandon t. O(the request's
fields times protocols); allocates the connection's state (about 33
KiB) and the chosen name on h.

### `Conn`

```
export linear mut view struct Conn
```

A WebSocket connection (RFC 6455, no extensions) made by Dial or
Accept. Linear, Send and Share: tasks share it by reference, one
receiving while others send. Each frame is written whole under a write
lock, so frames of two senders never interleave. A client masks its
frames with keys from io.Random. It keeps its heap and its net.Conn,
never the request, URL or Options it was made from. Close or Abandon
releases it; it has no Free.

### `Conn.Abandon`

```
export Conn.Abandon(self)
```

Releases the connection without speaking to the peer: its state freed,
its net.Conn abandoned. Never blocks or fails. O(1); allocates
nothing.

### `Conn.Close`

```
export Conn.Close(self, io: Io, code: u16, reason: str): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Closes the connection: sends a Close frame with code and reason (code
0 sends none and no reason), reads to the peer's Close frame, then
frees its state and closes its net.Conn with net.Conn.Close (over TLS,
a close_notify) within what is left of the close limit, abandoning it
if that fails, since the WebSocket close is already done. A connection
whose peer closed first, answered by Recv, only releases (Recv shut
its transport down). Errors: Protocol(code) for a status RFC 6455 7.4
does not let an endpoint send, TooLarge for a reason over 123 bytes,
each before anything is sent; Canceled under a cancelled scope;
time.TimedOut past the close limit (also under a NoCancel Io, and for a
cancel that comes after the limit passed); the connection's net.Error;
the stored failure. On any of them the Conn stays the caller's, in the
failed state, to Abandon (`onerr c.Abandon()` with
`try c.Close(io, code, reason)`). Frees the state; allocates nothing.

### `Conn.Ping`

```
export Conn.Ping(&self, io: Io, data: []u8): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Sends a Ping with data, at most 125 bytes (TooLarge otherwise, which
sends nothing). Recv reads the peer's Pong and drops it. Errors, and
what an interrupted write does, as for Send. O(data); allocates
nothing.

### `Conn.Protocol`

```
export Conn.Protocol(&self): ?str from self
```

- Flows: The result borrows from self.

The subprotocol the server chose: on a Conn from Dial, the one of
Options.protocols the server's 101 named, none when it named none; on a
Conn from Accept, the one Accept chose from its protocols, none when it
chose none. O(1); allocates nothing.

### `Conn.Recv`

```
export Conn.Recv(&self, a: &Arena, io: Io): Error!Message from a
```

- Flows: The result borrows from a.
- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Receives the next data message, its fragments joined, into a, after
answering the Pings and the Close that come before it. A second Recv of
this Conn waits for the first.

Errors: PeerClosed when the peer closed (its Close frame was answered);
Protocol when the peer broke RFC 6455: a control frame over 125 bytes
or fragmented, a reserved bit set, a frame masked the wrong way, or
more pings owed than pending_pongs; TooLarge past the frame or message
limit, checked before anything is allocated. On those the connection
sends its Close frame. Also the connection's net.Error, and OutOfMemory
when a cannot hold the message, which stays for the next call. After a
failure every call reports it. A Recv interrupted by a cancel or a
deadline loses nothing: the connection keeps a partly read frame or
message, and a reply it owes, and the next Recv goes on. O(message);
allocates the message in a, and a fragmented one first on the heap.

### `Conn.Send`

```
export Conn.Send(&self, io: Io, kind: Kind, data: []u8): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Sends data as one message of kind, in one frame, masked on the client
side. Errors: Protocol(1007) for text that is not UTF-8, which sends
nothing and leaves the connection usable; Canceled while waiting for
the write lock, likewise; the connection's net.Error; PeerClosed after
the peer's Close. A write interrupted part way fails the connection:
after it every call but Abandon reports that failure, and a call
waiting in another task returns it at once. O(data); allocates
nothing.

### `DefaultOptions`

```
export const DefaultOptions: Options
```

Frames and messages up to 16 MiB, 16 owed pongs, a 5-second close
limit, no subprotocol, no extra field and no secure connector. Accept
uses these limits.

### `Dial`

```
export Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): Error!Conn from h
```

- Flows: The result borrows from h.
- Errors: Error (Canceled, Closed, OutOfMemory, http.NoSecureTransport, net.AddrInUse, net.InvalidAddress, net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.Unreachable, net.Unsupported, time.TimedOut, Handshake(status: int), PeerClosed(code: u16), Protocol(code: u16), TooLarge)

Opens a WebSocket connection to u, a ws or wss URL, with o's header
fields, subprotocols, connector and limits. The Conn keeps h, never u
or o.

Errors: net.InvalidAddress for a URL that is not ws or wss or has no
host; http.NoSecureTransport for wss with o.secure none; Handshake(0)
for a field or subprotocol of o that is not a token and value, a
subprotocol offered twice, or a field the handshake writes itself (each
before any I/O); the connection's net.Error, and its secure
connector's; Handshake(status)
for a reply other than a 101 that RFC 6455 4.1 accepts; TooLarge for a
reply head over 16 KiB; OutOfMemory. A Dial interrupted by a cancel
releases what it made. O(the request and the reply heads); allocates
the request and the resolved addresses in a, and the connection's
state (about 33 KiB) on h.

### `Error`

```
export open type Error = Protocol | TooLarge | PeerClosed | Handshake | net.Error | http.NoSecureTransport | OutOfMemory
```

Every failure of std.websocket: its own four, the connection's
net.Error (with time.TimedOut for a Close past its limit),
http.NoSecureTransport for a wss URL with no secure connector, and
OutOfMemory. It is open: a match on it outside std keeps a `_` arm.

### `Handshake`

```
export error Handshake(status: int)
```

The opening handshake failed: status is the HTTP status the server
answered (Dial), or the one Accept answered with (400, 403 or 426), and
0 when the request was never sent or the reply was not an HTTP/1.1
head. A 101 reply that breaks RFC 6455's handshake rules is status
101.

### `Kind`

```
export enum Kind
```

What a data message holds: UTF-8 text (checked on both sides) or bytes.

Variants:

- `Text`: UTF-8 text, checked on both sides.
- `Binary`: Bytes.

### `Message`

```
export struct Message
```

One data message, its fragments joined; data is in the arena Recv was
given.

Fields:

- `kind: Kind`: Whether the message is text or bytes.
- `data: []u8`: The message's bytes, its fragments joined, in the arena Recv was given.

### `Options`

```
export struct Options
```

What Dial sends and what a connection allows. Plain: references and
slices into the caller's memory, never kept by the connection, which
copies the limits. Start from DefaultOptions and assign the fields you
change: a literal of it does not compile outside std. Dial fails with
Handshake(0), before any I/O, for a malformed or reserved field.

Fields:

- `headers: []http.Header`: Fields Dial adds to its request (an Origin, a cookie, a token).
  None may be one the handshake sets: Host, Upgrade, Connection or a
  Sec-WebSocket- field.
- `protocols: []str`: The subprotocols Dial offers, in preference order; empty offers
  none.
- `secure: ?net.Secure`: The connector Dial uses for a wss URL; none fails a wss URL with
  http.NoSecureTransport.
- `frame_bytes: int`: The longest frame payload Recv takes; a longer one is TooLarge.
- `message_bytes: int`: The longest message Recv takes, fragments joined; more is TooLarge.
- `pending_pongs: int`: How many pongs may be owed while another task holds the write lock;
  one ping more fails the connection with Protocol(1008).
- `close: time.Duration`: The longest a Close, or a reply Recv writes, may wait for the peer
  before it fails with time.TimedOut, also under a NoCancel Io.

### `PeerClosed`

```
export error PeerClosed(code: u16)
```

The peer closed: code is the status of its Close frame, 1005 when it
sent none, and 1006 when the connection ended without a Close frame.
The connection answered with its own Close frame when it could.

### `Protocol`

```
export error Protocol(code: u16)
```

The peer broke RFC 6455, or a value given to Send, Ping or Close is one
RFC 6455 forbids. code is the close status the connection sent, or
would send, for it: 1002 for a malformed or unexpected frame, 1007 for
text that is not UTF-8, 1008 for a peer that pings faster than its
pongs can be sent.

### `TooLarge`

```
export error TooLarge
```

A frame or message over the connection's limit (the peer is sent close
status 1009), a handshake head over 16 KiB, or a ping payload or close
reason over what a control frame holds.

<!-- api:end -->
