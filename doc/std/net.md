# std.net

`std.net` is TCP and Unix-domain stream sockets, UDP datagrams, addresses and name resolution, and `net.Conn`, the one connection type that `std.http`, `std.websocket` and `std.tls` run over. Its handles allocate nothing and hide no buffers: a read reads once into your slice. Reach for it to write a server or a client of your own protocol. For HTTP and WebSockets use [`std.http`](http.md) and [`std.websocket`](websocket.md), which take a `net.Conn` or make one for you. It works on Linux and Windows, and under a simulated `Io` from `std.sim`.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.net
```

## Contents

- [The main pieces](#the-main-pieces)
- [Addresses](#addresses)
- [Streams and listeners](#streams-and-listeners)
- [Socket options](#socket-options)
- [Datagrams](#datagrams)
- [Unix-domain sockets and passing handles](#unix-domain-sockets-and-passing-handles)
- [Conn: one connection type](#conn-one-connection-type)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Cancellation](#cancellation)
- [Examples](#examples): [a TCP server and client](#a-tcp-server-and-client), [addresses](#parse-print-and-resolve-addresses), [UDP](#send-and-receive-datagrams), [read lines with a deadline](#read-lines-with-a-deadline)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Accept TCP connections | `Listen` or `ListenWith`, then `Listener.Accept` |
| Connect to a server | `Dial` |
| Read and write a stream | `Stream.Read`, `Stream.Write`, `Stream.WriteAll`, `Stream.Shutdown` |
| Read lines or buffer writes | `Stream.Reader`, `Stream.Writer` |
| Tune a socket | `ListenOptions`, `Stream.SetNoDelay`, `Stream.SetKeepAlive` |
| Who is at each end | `Stream.PeerAddr`, `Stream.LocalAddr`, `Listener.Port` |
| Send and receive UDP | `Bind`, then `Datagram.Send` and `Datagram.Recv` |
| Parse, print and look up addresses | `ParseAddr`, `{a}` for an `Addr`, `Resolve` |
| Unix-domain sockets | `ListenUnix`, `DialUnix` |
| Pass handles to another process | `Stream.SendHandles`, `Stream.RecvHandles`, `Passed` |
| One type for any connection | `Stream.Conn`, `NewConn`, `Conn` |

Every call that touches the network takes an `io: Io` ([`Io`](README.md#4-io-and-the-system-handle)).

## Addresses

An `Addr` is an IP address and a port, a plain value you can copy and change. It has no IPv6 zone. `ParseAddr` reads `"a.b.c.d:port"` or `"[v6]:port"`, and `{a}` prints the same forms, IPv6 in its canonical short form (`[2001:db8::1]:443`).

`ParseAddr` refuses a zone (`"[fe80::1%eth0]:80"`) with `InvalidAddress`, since a zone dropped in silence would send packets out of the wrong link. It refuses host names too: `Resolve(a, io, host)` looks one up and returns at most 32 addresses, each with port 0, in the order the system gives them. An unknown name is `InvalidAddress`, and so is an answer that carries a zone. On Linux std reads `/etc/hosts` and asks the nameservers in `/etc/resolv.conf` itself; on Windows the system resolver answers.

The calls that take an address string accept these forms:

| Call | Forms |
|---|---|
| `Dial` | `"a.b.c.d:port"`, `"[v6]:port"` (a numeric `%zone` allowed), `"localhost:port"` or `":port"` (both 127.0.0.1), `"name:port"` |
| `Listen`, `ListenWith`, `Bind` | `"a.b.c.d:port"`, `"[v6]:port"` (a numeric `%zone` allowed), `"localhost:port"` (127.0.0.1), `":port"` (every IPv4 interface); `"[::]:port"` is every interface of both families |
| `ListenUnix`, `DialUnix` | a file system path |

`Dial` resolves a host name itself. On Linux it dials each answer of the lookup `Resolve` makes, in order and as the lookup gives it (a zoned or IPv4-mapped answer too, which `Resolve` refuses or unmaps), until one connects, and fails with the first one's error, as Go's dialer does; on Windows it dials the system's first IPv4 answer, else its first IPv6 one. A name that does not resolve is `InvalidAddress`, as from `Resolve`. To choose among the addresses yourself, as `std.http` does with its `allow` check, call `Resolve` and dial each one.

Port 0 in `Listen` or `Bind` asks the OS for a free port; `Port()` tells which.

## Streams and listeners

A `Listener` is a listening socket and a `Stream` a connected one. Both are **linear**: the compiler rejects a path that drops one without `Close`, which cannot fail and is not cancellable. A `Listener` is `Send` and `Share`, so several tasks may wait in `Accept` on one listener. A `Stream` is `Send` but not `Share`: one task uses it at a time. To read in one task while another writes, turn it into a `Conn` (below).

- `Stream.Read` reads once and returns the count, 0 at the end of the stream. `Stream.Write` sends once and may send less than asked. `Stream.WriteAll` loops until every byte is sent.
- `Stream.Reader` and `Stream.Writer` add a buffer you pass in. A `Writer` must be flushed before it is dropped.
- `Stream.Shutdown(how)` ends one direction or both, and the handle stays open until `Close`. `Shutdown.Write` is the half-close a client sends after its request: the server reads the end of the stream.
- `PeerAddr` is read once, when the stream is made. An accepted stream's comes from the accept itself, with no further system call, so a client that reset before `Accept` returned is still named. On a Unix stream, or a stream of an `Io` that is not the native one, `LocalAddr` and `PeerAddr` are the zero `Addr`.

In a worker that `proc.Supervise` started, a `Listen` of the parent's port, or of port 0, returns the listener the parent passed down, once. A `Listen` of any other port binds anew.

## Socket options

`ListenWith(io, addr, o)` takes a `ListenOptions`, an [options type](../codebase.md#options-types): start from `DefaultListenOptions` and assign the fields you change.

```mk
// verdict: ok
import std.net

Open(io: Io): net.Error!net.Listener {
    var o := net.DefaultListenOptions
    o.backlog = 1024   // the queue asked for; the OS may round it up
    o.no_delay = true  // every accepted stream sends small writes at once
    return try net.ListenWith(io, "[::]:8080", o)
}
```

| Field | Default | Meaning |
|---|---|---|
| `backlog` | 0 | A positive value is the queue of unaccepted connections asked for; 0 or less is the OS's largest |
| `reuse_addr` | true | `SO_REUSEADDR`: rebind a port whose connections are still in TIME_WAIT |
| `no_delay` | false | Set `TCP_NODELAY` on every accepted stream |

On Linux a port in TIME_WAIT rebinds only when the new listener and the socket that left it both set `reuse_addr`; otherwise `Listen` fails with `AddrInUse`. So a listener made with `reuse_addr` false holds its port until its TIME_WAIT entries expire. Windows binds over TIME_WAIT either way, and with `reuse_addr` false keeps every other socket off the port while the listener holds it.

On a stream, `SetNoDelay(on)` turns Nagle's algorithm off or on. `SetKeepAlive(some(d))` sends keep-alive probes after `d` of silence; `none` turns them off, and `d` zero or less turns them on and keeps the socket's idle time. Both act on the OS socket, so on a Unix stream or under `std.sim` they fail with `Unsupported`.

## Datagrams

`Bind(io, addr)` makes a `Datagram`, a bound UDP socket, linear like a `Stream`, `Send` but not `Share`.

- `Send(io, data, to)` sends one whole datagram or fails; a datagram longer than the OS takes is `Os` with the OS's code.
- `Recv(io, buf)` waits for one datagram and returns its length and sender. A datagram longer than `buf` is cut to `buf.Len()` bytes and the rest is dropped, so size `buf` for the largest datagram you expect (65,507 bytes holds any IPv4 datagram).
- `Recv` reports an IPv4 sender as IPv4 (`v6` false) whatever the socket's family.
- Link-local IPv6: `Bind` takes a numeric zone, which the socket keeps. `Send` sends a link-local `to` in that zone. `Recv` fails with `InvalidAddress`, consuming the datagram, when a link-local sender's zone is another, since an `Addr` cannot name it and a reply would go to the wrong link.

## Unix-domain sockets and passing handles

`ListenUnix(io, path)` and `DialUnix(io, path)` work as `Listen` and `Dial` over a socket file. They need Linux, or Windows 10 1803 or later; elsewhere they fail with `Unsupported`.

- The socket file stays after `Listener.Close`: remove it with `fs.Remove` before listening at that path again.
- A path is 1 to 107 bytes with no NUL; anything else is `InvalidAddress`, before any system call.
- On Windows a read that is waiting when the peer closes may never see the end of the stream, an OS defect. A server that reads Unix streams on Windows puts a deadline on each read.

On Linux a Unix stream carries handles too. `Stream.SendHandles(io, data, handles)` sends `data` with at most 64 handles (each a `Pass`: a file, a stream or a listener) attached to its first byte, through `SCM_RIGHTS`. The rest of `data` follows as `WriteAll`'s bytes. `Stream.RecvHandles` returns bytes as `Read` does and pushes the handles that came with them onto a `HeapList[Passed]`, in the order sent. A peer that reads that byte with a plain `Read` gets the byte, and the handles are closed. On Windows, on a TCP stream and under a simulated `Io` both calls are `Unsupported`.

A received handle is a `Passed`, typed by what the OS says it is:

- a listening stream socket is a `Listener`;
- any other stream socket is a `Stream` (on Windows, where a parent passes only listeners, any other socket is `Unsupported`);
- anything else, a pipe end included, is an `fs.File`, which reads and writes from position 0, or at the stream position when the handle has none.

A socket passed on is shared: closing either process's copy releases only that copy, with no shutdown, so the peer sees the end when the last copy closes. A child process inherits handles through [`proc.Command`](proc.md)'s `handles` field and reads them with [`proc.Inherited`](proc.md).

## Conn: one connection type

A `net.Conn` is a connected byte stream over any transport: a TCP or Unix stream (`Stream.Conn`), a TLS connection from [`std.tls`](tls.md), or a `Transport` of your own (`NewConn`). Code that takes a `Conn` runs over all of them. It is linear, `Send` and `Share`.

- It serializes `Read` calls with one lock and `WriteAll` calls with another, so one task reads while another writes, and two `WriteAll` calls never interleave. Each operation is one indirect call.
- `Shutdown` and `PeerAddr` take neither lock, so they run beside a waiting `Read` or `WriteAll`. After `Shutdown(Read)` or `Shutdown(Both)` a `Read` waiting in another task returns 0; after `Shutdown(Both)` a waiting `WriteAll` fails at once. On Windows a stream's waiting call is not woken, and stays until its deadline or cancel.
- `Close` over a stream only releases. Over a transport that speaks to the peer, such as TLS, it can fail, is cancellable and has a time limit: write `onerr c.Abandon()` with `try c.Close(io)`. `Abandon` never blocks or fails.
- `Stream.Conn` allocates nothing. `NewConn(h, t)` boxes the transport on `h`, which the `Conn` keeps with the transport's loans and frees at `Close` or `Abandon`; on `OutOfMemory` the transport stays with the caller.

`Transport`, `NewConn`, `ReserveConn` and `ConnSlot` are [Provisional](../codebase.md#7-stability-levels): they may change in a minor release. A transport's `Read` must consume nothing when it returns `Canceled`, its `WriteAll` may leave a prefix sent, and a `Close` that speaks to the peer must be cancellable and bounded. The `Conn` keeps its transport and what the transport borrows, so it cannot outlive a local the transport refers to:

```mk
// verdict: reject E0603
// net.NewConn's result is from h and t
import std.net

struct Lent {
    m: &Mutex[int]
}

impl net.Transport for Lent {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return 0
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {}

    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {}

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io): net.Error!void {}

    Abandon(self) {}
}

Make(h: &Heap): OutOfMemory!net.Conn {
    m := Mutex[int].New(0)
    return try net.NewConn(h, Lent(m: &m)) // <- E0603
}
```

`net.Secure` is a plain value that makes a connection secure. Only std makes one, through [`tls.Connector`](tls.md) and `tls.Acceptor`, and you pass it on to `std.http` and `std.websocket`, which import no TLS of their own. A secure connect that fails reports `SecureFailed(why)`, with a `SecureFailure` saying at which step.

`Secure.Negotiate` is `Connect` with an application protocol (ALPN, RFC 7301) chosen for this one handshake. You pass the protocols in your order of preference, and they take the place of the connector's own list. A client offers them; a server accepts them and picks the first of its list the client also offered. You get back the connection and `chosen`, the index in your list of the protocol the handshake chose, or -1 when the peer chose none. With an empty list it is `Connect`, and `chosen` is -1. A connector that negotiates nothing, such as a test's own made with `Secure.Of`, answers `Negotiate` with its `Connect`: the list is unused and `chosen` is -1. No protocol string crosses the connector: `std.http` learns `h2` or `http/1.1` as an index, without importing `std.tls`. On a client, a list `std.tls` cannot send (a name that is empty or longer than 255 bytes, or names of more than 65,535 bytes in all) fails with `SecureFailed(why: Handshake)` before any I/O, so the peer sees nothing; a server never matches such a name. A server that shares none of the client's protocols sends the `no_application_protocol` alert, and both sides fail with `SecureFailed(why: Handshake)`.

## Errors

Every function fails with members of one error set, `net.Error`.

| Member | Meaning |
|---|---|
| `Refused` | Nothing listens at the address dialled |
| `Reset` | The peer dropped the connection |
| `AddrInUse` | The address is already bound |
| `Unreachable` | No route leads to the address |
| `InvalidAddress` | The address does not parse, a name `Resolve` looks up does not resolve, or the address cannot be used here |
| `Unsupported` | This target, this kind of socket or this `Io` cannot do it |
| `SecureFailed(why)` | A secure connector could not make the connection secure |
| `StreamReset(code)` | The peer reset one stream of a multiplexed connection, or asked it to stop sending; `code` is its error code (provisional) |
| `MuxClosed(code, app, peer)` | A multiplexed connection ended, failing its open streams: `code` is in the application's space when `app`, the transport's otherwise, and `peer` says the peer closed it (provisional) |
| `Os(code)` | Any other OS failure, with the OS's error code |
| `Closed` | The handle is gone (from the prelude) |
| `Canceled` | The call's scope was cancelled (from the prelude) |
| `TimedOut` | A time limit of the connection's own passed: a handshake, a `Close` that speaks to the peer (from `std.time`) |
| `OutOfMemory` | An allocation failed (from the prelude) |

An OS error with a member of its own becomes that member on both targets. `Os(code: 0)` means the OS layer answered something impossible, or a simulated `Io` injected a fault that has no member of its own. For example, `Fault.Denied` on `Op.Accept` reaches `Accept`'s caller as `net.Os(code: 0)` (see [sim.md](sim.md)). `TimedOut` is never your own scope's deadline, which is `Canceled`. `tls.Error` and `websocket.Error` include all of `net.Error`. The set is open, so a `match` on it outside std keeps a `_` arm.

## Memory and cost

`std.net` allocates only where a signature takes an allocator. `Resolve` puts its list in an arena, `NewConn` and `ReserveConn` box a transport on a heap, and `RecvHandles` grows the list you pass. Everything else, every read and write included, allocates nothing and copies only into the slices you give it.

Each function's doc states its cost. "One runtime call" means one request to the OS through std's runtime, which waits without holding up the threads that run other tasks. `WriteAll` is O(n), a loop of sends.

## Cancellation

Cancellation works as the [library guide](README.md#6-cancellation) says; what a cancelled call leaves depends on the call:

| After a cancel | Calls |
|---|---|
| Nothing consumed: call again | `Stream.Read`, `Conn.Read`, `Listener.Accept`, `Datagram.Recv`, `DialUnix`, `RecvHandles`, `SendHandles` (before anything is sent), `Reader.Read` and `Reader.Line` on a stream's reader |
| An unknown prefix was sent: close the stream | `Stream.Write`, `Stream.WriteAll`, `Conn.WriteAll`, a `Writer` over a stream, `SendHandles` (after the handles were sent), `Secure.Connect`, `Secure.Negotiate` |
| Not cancellable | `Stream.Close`, `Listener.Close`, `Datagram.Close`, `Passed.Close`, `Conn.Abandon`, a `Conn.Close` over a stream |

A cancelled `Dial`, `Listen`, `Bind` or `Resolve` leaves nothing to release.

A `Conn.Close` that speaks to the peer is cancellable and bounded: on `Canceled` or `TimedOut` the `Conn` stays with you, and you call `Abandon`. No call takes a timeout argument. A deadline is a scope around the call, as in the last example below and the cookbook's [Time out a read, an idle connection and a retry](../cookbook.md#time-out-a-read-an-idle-connection-and-a-retry).

## Examples

Each program below runs as written with `mk run`. They use the loopback address and port 0, so they need no network and no free port of their own.

### A TCP server and client

```mk
// verdict: ok
import std.net

// Upper answers one connection: it reads until the client shuts its side
// down, then sends the bytes back in upper case.
Upper(l: &net.Listener, io: Io): net.Error!void {
    conn := try l.Accept(io)
    defer conn.Close()
    var buf: [256]u8 = [0; 256]
    var n := 0
    for n < buf.Len() {
        got := try conn.Read(io, mut buf[n..])
        if got == 0 {
            break // the client sent everything
        }
        n += got
    }
    for i in 0..n {
        if buf[i] >= 97 && buf[i] <= 122 { // a to z
            buf[i] -= 32
        }
    }
    try conn.WriteAll(io, buf[..n])
}

export Main(sys: &System): net.Error|Utf8Error|OutOfMemory|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0") // port 0: the OS picks a free port
    defer l.Close()
    target := try sys.arena.Fmt("127.0.0.1:{p}", (p: l.Port()))
    var reply: [256]u8 = [0; 256]
    var n := 0
    try scope s := sys.io.ScopeOf[net.Error]() {
        s.go Upper(&l, sys.io)
        c := try net.Dial(sys.io, target)
        defer c.Close()
        try c.WriteAll(sys.io, "hello, server\n".AsBytes())
        try c.Shutdown(sys.io, net.Shutdown.Write) // half-close: the request is complete
        for {
            got := try c.Read(sys.io, mut reply[n..])
            if got == 0 {
                break // the server closed
            }
            n += got
        }
    }
    try sys.Print("{r}", (r: try str.From(reply[..n])))
}
```

```text
$ mk run upper.mk
HELLO, SERVER
```

- `Read` may return any part of what was sent, so both sides loop until `Read` returns 0.
- `ScopeOf[net.Error]` lets the server task fail with a `net.Error`; the first error cancels the scope and `try scope` returns it.
- A server for many clients accepts in a loop and starts a task per stream: see the cookbook's [Serve each connection in its own task](../cookbook.md#serve-each-connection-in-its-own-task).

### Parse, print and resolve addresses

```mk
// verdict: ok
import std.net

export Main(sys: &System): net.Error|OutOfMemory|WriteError!void {
    for text in ["192.168.1.20:8080", "[2001:DB8:0:0:0:0:0:1]:443", "[::ffff:10.0.0.1]:53", "[fe80::1%eth0]:80", "example.com:80"] {
        a := net.ParseAddr(text) else {
            try sys.Print("{text}: not an address\n", (text))
            continue
        }
        try sys.Print("{text} -> {a} (IPv6: {v6})\n", (text, a: &a, v6: a.v6))
    }
    addrs := try net.Resolve(sys.arena, sys.io, "localhost")
    for a in addrs[..] {
        try sys.Print("localhost: {a}\n", (a: &a))
    }
}
```

```text
$ mk run addrs.mk
192.168.1.20:8080 -> 192.168.1.20:8080 (IPv6: false)
[2001:DB8:0:0:0:0:0:1]:443 -> [2001:db8::1]:443 (IPv6: true)
[::ffff:10.0.0.1]:53 -> [::ffff:10.0.0.1]:53 (IPv6: true)
[fe80::1%eth0]:80: not an address
example.com:80: not an address
localhost: 127.0.0.1:0
localhost: [::1]:0
```

- A zone and a host name are both refused by `ParseAddr`; look names up with `Resolve`.
- `Resolve` answers `localhost` itself, and gives every address port 0.

### Send and receive datagrams

```mk
// verdict: ok
import std.net

export Main(sys: &System): net.Error|Utf8Error|WriteError!void {
    server := try net.Bind(sys.io, "127.0.0.1:0") // port 0: the OS picks one
    defer server.Close()
    client := try net.Bind(sys.io, "127.0.0.1:0")
    defer client.Close()

    var to := try net.ParseAddr("127.0.0.1:0")
    to.port = Trunc[u16](server.Port()) // Addr is plain: change a copy freely
    try client.Send(sys.io, "ping".AsBytes(), to)

    var buf: [1500]u8 = [0; 1500]
    got := try server.Recv(sys.io, mut buf[..])
    from_client := got.peer.port == Trunc[u16](client.Port())
    try sys.Print("server got {msg} (from the client: {from_client})\n", (msg: try str.From(buf[..got.n]), from_client))
    try server.Send(sys.io, "pong".AsBytes(), got.peer) // answer the sender
    back := try client.Recv(sys.io, mut buf[..])
    try sys.Print("client got {msg}\n", (msg: try str.From(buf[..back.n])))
}
```

```text
$ mk run udp.mk
server got ping (from the client: true)
client got pong
```

- Each `Send` is one datagram and each `Recv` returns one, whole or cut to `buf`.
- UDP may drop or reorder datagrams on a real network; on loopback they arrive.

### Read lines with a deadline

```mk
// verdict: ok
import std.net
import std.time

// Talk sends three lines, then waits until the client hangs up.
Talk(l: &net.Listener, io: Io): net.Error!void {
    conn := try l.Accept(io)
    defer conn.Close()
    try conn.WriteAll(io, "one\ntwo\nthree\n".AsBytes())
    var buf: [16]u8 = [0; 16]
    for try conn.Read(io, mut buf[..]) != 0 {} // 0: the client closed
}

export Main(sys: &System): net.Error|ReadError|OutOfMemory|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    target := try sys.arena.Fmt("127.0.0.1:{p}", (p: l.Port()))
    try scope s := sys.io.ScopeOf[net.Error]() {
        s.go Talk(&l, sys.io)
        var conn := (try net.Dial(sys.io, target)).Conn() // a Conn: any transport, shared by tasks
        onerr conn.Abandon()
        var buf: [512]u8 = [0; 512]
        var input := conn.Reader(sys.io, mut buf[..])
        for {
            var line: ?str = none
            scope t := sys.io.Scope() {
                t.CancelAfter(time.Seconds(1)) // a deadline for this one read
                line = input.Line(sys.arena) else e {
                    if !t.Expired() {
                        fail e
                    }
                    none
                }
            }
            text := line else {
                try sys.Print("no line for a second\n", ())
                break
            }
            try sys.Print("line: {text}\n", (text))
        }
        try conn.Close(sys.io) // the server's Read returns 0
    }
}
```

```text
$ mk run lines.mk
line: one
line: two
line: three
no line for a second
```

- The deadline is a scope around one read. When it fires the read fails, and `t.Expired()` tells a timeout from any other failure.
- A cancelled `Line` consumes nothing, so the next one goes on where it stopped.
- A failure inside the `Reader` is `ReadError`, without the `net.Error` behind it. Call `Conn.Read` directly when you need it.

## API

<!-- api:begin -->
Stability: Stable.

### `Addr`

```
export struct Addr
```

An IP address and a port. ip is in network order; an IPv4 address is
in ip[0..4], with the rest 0 and v6 false. An Addr has no IPv6 zone.
The zero Addr (0.0.0.0:0) is what a call reports when there is no
address to give. Plain: copy it freely. `{a}` prints it as ParseAddr
reads it.

Fields:

- `ip: [16]u8`: The address in network order; an IPv4 address in ip[0..4], the rest 0.
- `port: u16`: The port.
- `v6: bool`: Whether ip holds an IPv6 address.

### `AddrInUse`

```
export error AddrInUse
```

The address is already bound.

### `Bind`

```
export Bind(io: Io, addr: str): Error!Datagram
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Binds a UDP socket to addr: "a.b.c.d:port", "[v6]:port" (a numeric
"%zone" allowed, which the socket keeps), "localhost:port" (127.0.0.1)
or ":port" (every IPv4 interface; "[::]:port" is every interface of
both families). These are Listen's forms, the port one to five decimal
digits. Port 0 lets the OS choose; Port tells which. Errors:
InvalidAddress for any other form, AddrInUse when the port is taken,
Canceled. One runtime call; allocates nothing.

### `Conn`

```
export linear view struct Conn
```

A connected byte stream over any transport: a TCP or Unix stream
(Stream.Conn), a TLS connection, or a Transport of the program's
(NewConn). std.http and std.websocket take one, so code above them runs
over any of these. Linear, Send and Share: tasks may share it by
reference, one reading while another writes. Reads take one lock and
writes another, so a WriteAll never interleaves with another. Close or
Abandon releases it. It keeps the &Heap it was made with, and has no
Free.

### `Conn.Abandon`

```
export Conn.Abandon(self)
```

Releases the connection without speaking to the peer: the transport's
Abandon, or the handle's close. Never blocks or fails. One indirect
call, O(1) here plus the transport's Abandon; allocates nothing; frees
the box.

### `Conn.Close`

```
export Conn.Close(self, io: Io): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Closes the connection: the transport's Close, which may speak to the
peer, then the release. On failure the Conn stays the caller's,
unchanged, to Close again or Abandon (`onerr c.Abandon()` with
`try c.Close(io)`). Over a stream it only releases, cannot fail and
is not cancellable. Over a transport whose Close speaks to the peer
(TLS) it is cancellable (Canceled) and bounded by the transport's close
limit (time.TimedOut). One indirect call, O(1) here plus the
transport's Close; allocates nothing; frees the box.

### `Conn.PeerAddr`

```
export Conn.PeerAddr(&self): ?Addr
```

The peer's address; none when the transport has none to give (a Unix
stream, an in-memory transport, a stream of an Io other than the native
one). One indirect call, O(1) here plus the transport's; allocates
nothing.

### `Conn.Read`

```
export Conn.Read(&self, io: Io, buf: mut []u8): Error!int
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Reads once, as Stream.Read: at most buf.Len() bytes, 0 at the end of
the stream. Reads of this Conn run one at a time; a Read waits for the
lock, and is Canceled while it waits. A Read that returns Canceled has
consumed nothing: call it again. Errors: the transport's. One indirect
call, O(1) here plus the transport's read; allocates nothing.

### `Conn.Reader`

```
export Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Reader over this Conn, using buf as its buffer. Each fill is
one Conn.Read, under the read lock, so two Readers of one Conn take
turns per fill. Any error becomes its ReadError. Bytes it buffered and
the caller did not read are lost when it is dropped. O(1); allocates
nothing.

### `Conn.Shutdown`

```
export Conn.Shutdown(&self, io: Io, how: Shutdown): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Ends one or both directions, as Stream.Shutdown. It takes neither lock,
so it never waits behind a Read or a WriteAll of another task, and it
ends them: a Read waiting in another task returns 0 after Shutdown(Read)
or Shutdown(Both), and a waiting WriteAll fails after Shutdown(Both).
Over a stream on Windows the waiting call is not woken and stays until
its deadline or cancel. Errors: the transport's. One indirect call,
O(1) here plus the transport's shutdown; allocates nothing.

### `Conn.WriteAll`

```
export Conn.WriteAll(&self, io: Io, data: []u8): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends every byte of data before any other WriteAll of this Conn sends
one. A call interrupted by a cancel has sent an unknown prefix of data:
close the connection. Errors: the transport's. One indirect call, O(1)
here plus the transport's write; allocates nothing.

### `Conn.Writer`

```
export Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Writer over this Conn, using buf as its buffer. Each flush
is one Conn.WriteAll, so the bytes of one flush never interleave with
another writer's. Any error becomes its WriteError. Flush before
dropping it. O(1); allocates nothing.

### `ConnSlot`

```
export linear view struct ConnSlot[T: Transport + Send + Share]
```

A box on a heap reserved for one transport of type T, empty until Fill
moves the transport in and makes the Conn. Provisional, as Transport
is. A function that takes a Conn or a transport by value and must leave
it with its caller when it fails (a TLS connector) reserves first, does
its fallible work, and fills last, so nothing can fail after the move
(E0411). Linear: Fill or Free (with the heap's allocator) ends it.

### `ConnSlot.Fill`

```
export ConnSlot[T].Fill(self, t: T): Conn from self, t
```

- Flows: The result borrows from self and t.

Moves t into the reserved box and makes the Conn over it, which keeps
the slot's heap and t's loans. t is stored only into the box, which
the Conn alone reaches, so a slot reserved on sys.heap takes a
transport that borrows a local. Cannot fail; O(1); allocates nothing.

### `Datagram`

```
export linear struct Datagram
```

A bound UDP socket. Linear: Close releases it. Send but not Share, as
Stream: one task uses it at a time.

### `Datagram.Close`

```
export Datagram.Close(self)
```

Closes the socket. Infallible and not cancellable: the OS's answer to
the close is ignored. O(1); allocates nothing.

### `Datagram.Connect`

```
export Datagram.Connect(&self, io: Io, to: Addr): Error!void
```

Connects the UDP socket to `to`: only `to`'s datagrams are received.
Errors: InvalidAddress, Canceled, or the OS's. O(1); allocates nothing.

### `Datagram.LocalAddr`

```
export Datagram.LocalAddr(&self): Addr
```

The bound address of this socket (zero Addr for another Io's socket).
O(1): one system call; allocates nothing.

### `Datagram.Port`

```
export Datagram.Port(&self): int
```

The port the socket is bound to. O(1); allocates nothing.

### `Datagram.Recv`

```
export Datagram.Recv(&self, io: Io, buf: mut []u8): Error!(n: int, peer: Addr)
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Waits for one datagram: its bytes in buf[0..n], and the peer that sent
it. Errors: Canceled when the scope is cancelled, at entry or while it
waits, with nothing consumed; InvalidAddress for a peer an Addr cannot
name (below); any other failure is the OS's. One runtime call;
allocates nothing.

A datagram longer than buf is cut to buf.Len() bytes and the rest is
lost (POSIX recvfrom), so size buf to the largest datagram expected
(65,507 bytes holds any IPv4 datagram). An empty buf discards one
datagram. An IPv4 peer is IPv4 (v6 false) whatever the socket's family.
A link-local peer in a zone other than the socket's (a sender on another
link than the zone Bind was given, or any link-local sender seen by a
socket bound without a zone) cannot be named by an Addr, so its
datagram is consumed and the call is InvalidAddress: a reply in zone 0
would go to the wrong link.

### `Datagram.Send`

```
export Datagram.Send(&self, io: Io, data: []u8, to: Addr): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends data as one datagram to the address to: all of it, or an error.
A datagram is never sent in part; one longer than the OS takes is Os
with the OS's code (10040 on Windows, 90 on Linux). Errors: Canceled;
InvalidAddress as below; the OS's. One runtime call; allocates nothing.

From an IPv6 socket an IPv4 to goes as ::ffff:a.b.c.d; from an IPv4
socket an IPv4-mapped to goes as IPv4, and any other IPv6 to is
InvalidAddress. A link-local to (fe80::/10) goes in the socket's zone,
the one Bind was given (0 when none, which the OS may refuse). Empty
data sends an empty datagram.

### `Datagram.SetBuffers`

```
export Datagram.SetBuffers(&self, recv: int, send: int): Error!void
```

Asks the OS for receive and send buffer sizes of `recv` and `send`
bytes (0 keeps the current size). Errors: Unsupported for another Io's
socket; Os for the OS's refusal. O(1): at most two system calls;
allocates nothing.

### `Datagram.Share`

```
export Datagram.Share(self): SharedDatagram
```

Converts the socket into a SharedDatagram that tasks share by
reference. O(1); allocates nothing.

### `DefaultListenOptions`

```
export const DefaultListenOptions: ListenOptions
```

What Listen does: the OS's largest backlog, rebinding over TIME_WAIT
allowed, Nagle's algorithm on.

### `Dial`

```
export Dial(io: Io, addr: str): Error!Stream
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Connects to addr: "a.b.c.d:port", "[v6]:port" (a numeric %zone
allowed), "localhost:port" or ":port" (both 127.0.0.1), or
"name:port", resolved first: on Windows the system's first IPv4
answer is dialled, else its first IPv6 one; on Linux each answer of the
lookup Resolve makes (/etc/hosts, then the nameservers), in order and as
the lookup gives it (a zoned or IPv4-mapped one too), until one connects.
Errors: Refused when nothing listens there, Unreachable when no route
leads there, InvalidAddress for an addr that does not parse or a name
that does not resolve (as Resolve reports it), Canceled; for a name
dialled at several addresses, the first one's error. One runtime call
(with a name, a lookup and a connect per address tried); allocates
nothing.

The peer's address is read once here and kept for Stream.PeerAddr:
one system call more, which does not wait.

### `DialUnix`

```
export DialUnix(io: Io, path: str): Error!Stream
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Connects to the Unix-domain listener at path. While its backlog is full
the dial waits, holding nothing, so a cancel or a deadline ends it with
Canceled and nothing lost: call again.
Errors: InvalidAddress as ListenUnix; Refused when nothing listens at
path (or, on Linux, nothing exists there); Unsupported as ListenUnix;
Canceled; Os for any other refusal. O(1) system calls per try;
allocates nothing.

### `Error`

```
export open type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | SecureFailed | StreamReset | MuxClosed | OutOfMemory
```

Every way a net operation fails. It holds the prelude's Canceled and
Closed, and time.TimedOut, which reports a limit of the connection's
own (a Close that speaks to the peer, a handshake), never a deadline of
the caller's scope: that is Canceled. std.tls and std.websocket include
this whole set in theirs. It is open: a match on it outside std keeps a
`_` arm.

### `InvalidAddress`

```
export error InvalidAddress
```

The address does not parse, a name Resolve looks up does not resolve,
or the address cannot be used here.

### `Listen`

```
export Listen(io: Io, addr: str): Error!Listener
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Binds addr and listens, with the OS's largest backlog. addr is
"a.b.c.d:port", "[v6]:port" (a numeric "%zone" allowed),
"localhost:port" (127.0.0.1) or ":port" (every IPv4 interface;
"[::]:port" is every interface of both families). Port 0 lets the OS
choose; Port tells which. Errors: AddrInUse when the port is taken,
InvalidAddress for any other form, Canceled in a cancelled scope,
checked first. O(1) runtime calls; allocates nothing.

In a worker that proc.Supervise started, a Listen of the parent's
address returns the listener the parent passed down, with its bound
port. A Listen whose port is that listener's port, or 0 (as the
parent's own address may be), takes it, once. A Listen of any other
port binds as above, so a worker may open an admin or metrics port
before or after it, from another task too. Two Listens of port 0
cannot be told apart: the first takes the inherited listener. A
cancelled Listen leaves the inherited listener for the next.

### `ListenOptions`

```
export struct ListenOptions
```

How ListenWith binds, and what the streams its Accept makes carry.
Plain. Start from DefaultListenOptions and assign the fields you
change: a literal of it does not compile outside std, so a field added
later breaks no program.

Fields:

- `backlog: int`: The queue of connections not yet accepted. A positive value is the
  queue asked for, which the OS may round up; 0 or less is the OS's
  largest, which Listen asks for.
- `reuse_addr: bool`: Rebind a port whose connections are still in TIME_WAIT
  (SO_REUSEADDR on Linux). Windows rebinds without it, and false there
  keeps every other socket off the port while the listener holds it.
- `no_delay: bool`: Every accepted stream sends small writes at once (TCP_NODELAY).

### `ListenUnix`

```
export ListenUnix(io: Io, path: str): Error!Listener
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Listens at path with a Unix-domain stream socket (the OS's largest
backlog). The socket file stays after Listener.Close: remove it before
listening at path again. Unix sockets need Linux, or Windows 10 1803 or
later. On Windows a read that is waiting when the peer closes may never
see the end of the stream, an OS defect, so a server reading Unix
streams there puts a deadline on each read.
Errors: InvalidAddress for an empty path, one holding a NUL, or one of
108 bytes or more (sun_path), before any system call; AddrInUse when a
file exists at path; Unsupported where the target or its file system
has no Unix sockets; Canceled in a cancelled scope, checked first; Os
for any other refusal of the bind. O(1) system calls; allocates
nothing.

### `ListenWith`

```
export ListenWith(io: Io, addr: str, o: ListenOptions): Error!Listener
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Binds addr and listens as Listen does, with o's choices. A positive
backlog is the queue the OS is asked for (listen(2) may round it up).

With reuse_addr false, Linux refuses a port whose connections are
still in TIME_WAIT (AddrInUse). Linux rebinds over TIME_WAIT only when
the socket that left it also set SO_REUSEADDR, so the TIME_WAIT
entries of a listener made with reuse_addr false hold its port against
both choices until they expire. Windows binds over TIME_WAIT either
way, and with reuse_addr false keeps any other socket off the port
while the listener holds it (SO_EXCLUSIVEADDRUSE).

Port 0 and the address forms are Listen's, and so is a worker's
inherited listener, which keeps the options the parent listened with,
except no_delay, which applies at each Accept. Errors: Listen's
(Canceled is checked first, before the inherited listener is looked
for); Unsupported for a positive backlog or reuse_addr false under an
Io that cannot honour them (a Recorder's Tape). O(1) runtime calls;
allocates nothing.

### `Listener`

```
export linear struct Listener
```

A bound, listening socket. Linear: Close releases it. Send and Share:
several tasks may wait in Accept on one listener at once.

### `Listener.Accept`

```
export Listener.Accept(&self, io: Io): Error!Stream
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Waits for the next connection and returns its stream. Errors: Canceled
when the scope is cancelled, at entry or while it waits (nothing is
lost: call again); Closed when the listener's handle is gone; any other
failure is the OS's, as Reset, Os and the other members. The peer's
address comes from the accept itself, so a client that reset before
Accept returned is still named. One runtime call, plus reading the
peer's address from the accept with no system call (native Io), plus
one system call that sets TCP_NODELAY when ListenWith asked for
no_delay (a failure of that one is ignored: the stream works without
it); allocates nothing.

### `Listener.Addr`

```
export Listener.Addr(&self): Addr
```

The address the listener is bound to: zero Addr for a Unix-domain
listener or another Io's listener. O(1): one system call; allocates
nothing.

### `Listener.Close`

```
export Listener.Close(self)
```

Closes the listener. Infallible and not cancellable: the OS's answer
to the close is ignored. O(1); allocates nothing.

A Unix-domain listener leaves its socket file behind: remove it with
fs.Remove before listening at that path again.

### `Listener.Port`

```
export Listener.Port(&self): int
```

The port the listener is bound to; 0 for a Unix-domain
listener. O(1); allocates nothing.

### `Mux`

```
export linear view struct Mux
```

One multiplexed connection (a QUIC connection or an in-memory engine of
std.net.pipe): linear, Send and Share; many tasks call it by reference.
Provisional.

### `Mux.Abandon`

```
export Mux.Abandon(self)
```

Releases the connection without waiting or speaking to the peer.
Provisional.

### `Mux.Accept`

```
export Mux.Accept(&self, io: Io, uni: bool, wait: bool): Error!?MuxStream
```

Returns the next stream the peer opened, or none at once when `wait` is
false. Provisional.

### `Mux.Close`

```
export Mux.Close(self, io: Io, code: u64): Error!void
```

Closes the connection with application `code`. On failure, `self` stays
the caller's to Abandon or retry. Provisional.

### `Mux.Credit`

```
export Mux.Credit(&self, s: &MuxStream, total: i64)
```

Raises the bytes the peer may send on `s` to at least `total`. Never
waits. Provisional.

### `Mux.Limits`

```
export Mux.Limits(&self): MuxLimits
```

Returns the peer's current stream and idle limits. O(1). Provisional.

### `Mux.Of`

```
export Mux.Of[T: MuxEngine + Send + Share](h: &Heap, b: core.SealedBox): Mux from h, b
```

Wraps an engine boxed in `b` on `h` as a Mux. Only std builds one.
Provisional.

### `Mux.Open`

```
export Mux.Open(&self, io: Io, uni: bool, wait: bool): Error!?MuxStream
```

Opens a bidirectional or unidirectional stream; returns none immediately
when `wait` is false and the stream limit is reached. Provisional.

### `Mux.PeerAddr`

```
export Mux.PeerAddr(&self): ?Addr
```

Returns the peer's address, if known. O(1). Provisional.

### `Mux.Read`

```
export Mux.Read(&self, io: Io, s: &MuxStream, buf: mut []u8, wait: bool): Error!?int
```

Reads into `buf`: byte count, 0 at end of stream, or none when `wait` is
false and nothing is queued. Provisional.

### `Mux.Release`

```
export Mux.Release(&self, s: MuxStream)
```

Releases the stream handle `s` back to the connection. O(1). Provisional.

### `Mux.Reset`

```
export Mux.Reset(&self, s: &MuxStream, code: u64)
```

Abandons sending on `s` with error `code`. Never waits. Provisional.

### `Mux.Stop`

```
export Mux.Stop(&self, s: &MuxStream, code: u64)
```

Abandons receiving on `s` with error `code`. Never waits. Provisional.

### `Mux.Usable`

```
export Mux.Usable(&self, io: Io): bool
```

Processes queued packets without waiting and reports whether the
connection can take a new stream. Provisional.

### `Mux.Wait`

```
export Mux.Wait(&self, io: Io, ids: []i64, incoming: bool): Error!MuxReady
```

Waits until one of `ids` is readable or, when `incoming` is true, a new
peer stream can be accepted. Provisional.

### `Mux.Write`

```
export Mux.Write(&self, io: Io, s: &MuxStream, data: []u8, fin: bool): Error!void
```

Writes all of `data`, then the stream's end when `fin` is true, waiting
for flow-control credit. Provisional.

### `MuxClosed`

```
export error MuxClosed(code: u64, app: bool, peer: bool)
```

A multiplexed connection ended, failing every stream still open on it.
code is the error code of the close: in the application's space (an
HTTP/2 GOAWAY's or HTTP/3's code) when app, the transport's (QUIC's)
otherwise; peer is true when the peer closed it, false when this side
did. Provisional.

### `MuxDialer`

```
export plain view struct MuxDialer
```

A client connector of multiplexed connections, made only by std.
Provisional.

### `MuxDialer.Dial`

```
export MuxDialer.Dial(&self, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h
```

Opens a multiplexed connection to `to`, verifies `host`, and negotiates
`alpn`. Provisional.

### `MuxDialer.Of`

```
export MuxDialer.Of[T: Share](c: core.ErasedCall): MuxDialer from c
```

Wraps `c` as a MuxDialer. Only std calls this. Provisional.

### `MuxEngine`

```
export interface MuxEngine
```

The contract std's multiplexed transports implement behind Mux.Of.
Provisional.

### `MuxLimits`

```
export struct MuxLimits {
    export bidi: i64
    export uni: i64
    export idle: time.Duration
}
```

Stream and idle-timeout limits reported by the peer. Provisional.

### `MuxListener`

```
export linear view struct MuxListener
```

A bound multiplexed server endpoint. Release with Close. Provisional.

### `MuxListener.Accept`

```
export MuxListener.Accept(&self, io: Io): Error!Mux
```

Accepts the next incoming multiplexed connection on `self`'s heap.
Provisional.

### `MuxListener.Close`

```
export MuxListener.Close(self)
```

Releases the listener and its socket. Provisional.

### `MuxListener.Of`

```
export MuxListener.Of[T: MuxListening + Send + Share](h: &Heap, b: core.SealedBox): MuxListener from h, b
```

Wraps a listening endpoint boxed in `b` on `h`. Only std calls this.
Provisional.

### `MuxListener.Port`

```
export MuxListener.Port(&self): int
```

Returns the bound port. O(1). Provisional.

### `MuxListener.Run`

```
export MuxListener.Run(&self, io: Io): Error!void
```

Reads packets and routes them until `io`'s scope is cancelled.
Provisional.

### `MuxListening`

```
export interface MuxListening
```

The contract a multiplexed server listener implements for MuxListener.Of.
Provisional.

### `MuxReady`

```
export enum MuxReady {
    Readable(index: int)
    Incoming(uni: bool)
}
```

Result of Mux.Wait: either `ids[index]` is readable or an incoming
stream (`uni` tells whether unidirectional) is ready to accept.
Provisional.

### `MuxServer`

```
export plain view struct MuxServer
```

A server endpoint factory of multiplexed connections, made only by std.
Provisional.

### `MuxServer.Listen`

```
export MuxServer.Listen(&self, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, self
```

Binds UDP on `addr` and returns a MuxListener. Provisional.

### `MuxServer.Of`

```
export MuxServer.Of[T: Share](c: core.ErasedCall): MuxServer from c
```

Wraps `c` as a MuxServer. Only std calls this. Provisional.

### `MuxStream`

```
export linear struct MuxStream
```

One stream of a Mux. Linear: Mux.Release consumes it. Provisional.

### `MuxStream.Id`

```
export MuxStream.Id(&self): i64
```

The stream's QUIC stream id. O(1); allocates nothing. Provisional.

### `NewConn`

```
export NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t
```

- Flows: The result borrows from h and t.
- Errors: OutOfMemory

Boxes the transport t on h as a Conn, which keeps h and t's loans and
frees the box at Close or Abandon. Provisional, as Transport is.
Errors: OutOfMemory, with t still the caller's (it is not consumed on
failure). O(1); allocates one box of ?T's size on h. It is ReserveConn
then ConnSlot.Fill, in one call.

### `Os`

```
export error Os(code: i64)
```

Any other OS failure, with the OS's error code. The code is 0 when the
OS layer answered something impossible (a count out of range, a send
that made no progress), and for a fault a simulated Io injects that
has no member of its own (Denied, for one).

### `ParseAddr`

```
export ParseAddr(s: str): Error!Addr
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Parses "a.b.c.d:port" or "[v6]:port". Anything else is InvalidAddress.
O(len s); allocates nothing.

An unbracketed host is a dotted IPv4 address: four decimal fields, 0
to 255, with no leading zero. A bracketed host is an IPv6 address in
RFC 4291's text form: groups of one to four hex digits, one "::" for
one or more zero groups, and an optional dotted IPv4 tail in the last
32 bits. The port is decimal digits, at most 65535. A zone ("%eth0") is
InvalidAddress, since an Addr has none and a zone dropped in silence
would address the wrong link. Host names are Resolve's.

### `Pass`

```
export open enum Pass
```

A handle sent beside a Unix stream's bytes (Stream.SendHandles), lent:
the receiver gets its own copy and the sender keeps this one. Plain.

Variants:

- `File(f: &fs.File)`: An open file's handle.
- `Stream(s: &Stream)`: A connected stream.
- `Listener(l: &Listener)`: A listener.

### `Passed`

```
export open enum Passed
```

A handle passed to this process, beside a Unix stream's bytes
(Stream.RecvHandles) or named by its parent's Command (proc.Inherited),
typed by what the OS says it is: a listening stream socket is a
Listener, any other stream socket a Stream (on Windows, where only a
listener is inherited, any other socket is Unsupported), anything else
(a file, a pipe end, a device) a File, which reads and writes from
position 0, or at the stream position when the handle has none.
Linear: take it apart with match, or Close it. A socket in it is
shared with the process it came from: closing either copy releases
only that copy, with no shutdown, and the peer sees the end when the
last copy closes.

Variants:

- `File(f: fs.File)`: Any handle that is not a stream socket: a file, a pipe end or a device.
- `Stream(s: Stream)`: A stream socket that is not listening.
- `Listener(l: Listener)`: A listening stream socket.

### `Passed.Close`

```
export Passed.Close(self)
```

Closes the handle, whatever its kind: Close for a Stream or a Listener,
Abandon for a File, whose close has nothing to report here. Infallible
and not cancellable. O(1); allocates nothing.

### `Refused`

```
export error Refused
```

Nothing listens at the address dialled.

### `ReserveConn`

```
export ReserveConn[T: Transport + Send + Share](h: &Heap): OutOfMemory!ConnSlot[T] from h
```

- Flows: The result borrows from h.
- Errors: OutOfMemory

Reserves on h the box a Conn over a T will live in. Provisional.
Errors: OutOfMemory. O(1); allocates one box of ?T's size on h, freed
by the Conn's Close or Abandon, or by Free.

### `Reset`

```
export error Reset
```

The peer dropped the connection.

### `Resolve`

```
export Resolve(a: &Arena, io: Io, host: str): Error|OutOfMemory!List[Addr] from a
```

- Flows: The result borrows from a.
- Errors: Error|OutOfMemory (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Resolves a host name to its addresses, in the order the system gives
them, each with port 0. Errors: InvalidAddress for an unknown, empty or
over-long name; Canceled; OutOfMemory. One runtime
call (a network round trip at worst); allocates the List of at most 32
Addrs in a.

A numeric address answers itself, and "localhost" is 127.0.0.1 then
::1. IPv4-mapped answers come back as IPv4. An answer with an IPv6 zone
("fe80::1%5", or a zoned hosts line) is InvalidAddress, as ParseAddr
refuses one. On Windows the system's resolver answers
(GetAddrInfoW); on Linux std reads /etc/hosts, then asks the
nameservers of /etc/resolv.conf.

### `Secure`

```
export struct Secure
```

A plain value that makes a connection secure, made only by std:
tls.Connector gives one for a client, tls.Acceptor for a server. Pass it
to std.http and std.websocket, which import no TLS of their own. Copy
it freely; it borrows the configuration it was made from. call is
std's; a program has no use for it.

### `Secure.Connect`

```
export Secure.Connect(&self, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn
```

- Flows: The result borrows from h and conn.
- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Makes conn secure for host (empty on a server) by running the
connector's handshake, through one indirect call (O(1) here plus the
handshake's cost). On success conn is inside the result. On failure it
stays the caller's, mid-handshake, to Close or Abandon. Errors: the
connector's; a failed handshake is SecureFailed(why), its time limit
time.TimedOut, a cancel Canceled, and after a cancel part of the
handshake may have been sent. It allocates on h what the connector's
state needs. The connection is boxed on h and holds conn, so h's loan
may reach what conn reaches (`h: &Heap into conn`).

### `Secure.Negotiate`

```
export Secure.Negotiate(&self, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn
```

- Flows: The result borrows from h and conn.
- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Like Connect, but negotiates the application protocol (ALPN, RFC
7301) from offer, in preference order, in place of the connector's own
list, for this one handshake: a client offers them, a server accepts
them and picks the first of offer the client also offered. chosen is
the index in offer of the protocol the handshake chose, -1 when the
peer chose none (a client that sent no ALPN, a server that answered
none); with offer empty the handshake is Connect's, and chosen is -1.
A connector made by Of negotiates nothing: this is its Connect, offer
unused, and chosen is -1. Provisional. Errors: Connect's. On a client,
an offer std.tls cannot send (a name that is empty or over 255 bytes,
or names of more than 65,535 bytes in all) is SecureFailed(why:
Handshake) before any I/O; a server never matches such a name. A
server that shares none of the client's protocols sends the
no_application_protocol alert, and both sides fail with
SecureFailed(why: Handshake). O(1) here plus the handshake's cost,
which reads the names; allocates what Connect does.

### `SecureFailed`

```
export error SecureFailed(why: SecureFailure)
```

A secure connector (net.Secure) could not make the connection secure;
why says at which step.

### `SecureFailure`

```
export open enum SecureFailure
```

Why a secure connect failed, as the connector reports it through
net.Secure, where no connection exists yet to ask.

Variants:

- `Handshake`: The handshake failed: an alert, a version or cipher mismatch.
- `BadCertificate`: The peer's certificate does not parse or is not usable.
- `UnknownAuthority`: The chain ends at no trusted root.
- `Expired`: A certificate of the chain is outside its validity period.
- `NameMismatch`: The certificate does not name the host dialled.
- `Protocol`: The peer broke the protocol after the handshake began.

### `SharedDatagram`

```
export linear struct SharedDatagram
```

A bound UDP socket shared across tasks by reference (`Datagram.Share`).
Read-side calls take the read lock and write-side calls take the write
lock, so one Recv or TryRecv and one Send or SendBatch run concurrently.
Linear: Close releases it.

### `SharedDatagram.Close`

```
export SharedDatagram.Close(self)
```

Closes the socket. Infallible and not cancellable. O(1); allocates
nothing.

### `SharedDatagram.LocalAddr`

```
export SharedDatagram.LocalAddr(&self): Addr
```

The bound address of this socket (zero Addr for another Io's socket).
O(1): one system call; allocates nothing.

### `SharedDatagram.Port`

```
export SharedDatagram.Port(&self): int
```

The port the socket is bound to. O(1); allocates nothing.

### `SharedDatagram.Recv`

```
export SharedDatagram.Recv(&self, io: Io, buf: mut []u8): Error!(n: int, peer: Addr)
```

Waits for one datagram into `buf` under the read lock, as Datagram.Recv.
O(1) runtime call; allocates nothing.

### `SharedDatagram.Send`

```
export SharedDatagram.Send(&self, io: Io, data: []u8, to: ?Addr): Error!void
```

Sends `data` as one datagram to `to`, or to the connected peer when `to`
is none, under the write lock. Fails with InvalidAddress when `to` is
none on an unconnected socket. O(1) runtime call; allocates nothing.

### `SharedDatagram.SendBatch`

```
export SharedDatagram.SendBatch(&self, io: Io, buf: []u8, seg: int, to: ?Addr): Error!void
```

Sends `buf` split into datagrams of at most `seg` bytes each, in order,
to `to` (or connected peer when none) under the write lock. Fails with
Os(code: 0) when `seg <= 0`. O(buf.Len() / seg) syscalls; allocates
nothing.

### `SharedDatagram.TryRecv`

```
export SharedDatagram.TryRecv(&self, io: Io, buf: mut []u8): Error!?(n: int, peer: Addr)
```

Receives one queued datagram into `buf` without waiting under the read
lock, returning none immediately when nothing is queued. One runtime
call; allocates nothing.

### `Shutdown`

```
export open enum Shutdown
```

Which directions Stream.Shutdown and Conn.Shutdown end.

Variants:

- `Read`: No more reads: later reads return 0, and a read waiting in
  another task returns 0.
- `Write`: No more writes: the peer reads the end of the stream after what was
  sent.
- `Both`: No more reads and no more writes.

### `Stream`

```
export linear struct Stream
```

A connected TCP or Unix-domain stream. Linear: Close releases it. Send
but not Share, so one task reads and writes it at a time; to read in
one task while another writes, make it a Conn (Stream.Conn).

### `Stream.Close`

```
export Stream.Close(self)
```

Closes the stream. Infallible and not cancellable. O(1); allocates
nothing.

### `Stream.Conn`

```
export Stream.Conn(self): Conn
```

The stream as a Conn, with its peer's address kept (none for a Unix
stream, another Io's stream, or a TCP peer the OS could not name when
the stream was made). The Conn's Close only releases, so it cannot
fail and is not cancellable. O(1); allocates nothing: the handle is
stored where a transport's box would be.

### `Stream.LocalAddr`

```
export Stream.LocalAddr(&self): Addr
```

This end's address: the IP and port the stream is bound to. The zero
Addr for a Unix stream, for another Io's stream, and when the OS cannot
tell (the socket was reset). O(1): one system call; allocates nothing.

### `Stream.PeerAddr`

```
export Stream.PeerAddr(&self): Addr
```

The peer's address, read when the stream was made (by Dial or Accept),
so a peer that has since gone still has one; an accepted stream's comes
from the accept itself. The zero Addr for a Unix stream, for another
Io's stream, and for a dialled peer already gone when the stream was
made. An IPv4 peer of an IPv6 listener is its IPv4 address. O(1);
allocates nothing.

### `Stream.Read`

```
export Stream.Read(&self, io: Io, buf: mut []u8): Error!int
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Reads once: at most buf.Len() bytes, the count read, 0 at the end of
the stream (the peer shut its side down). Errors: Reset when the peer
dropped the connection; Canceled when the scope is cancelled, before
or while it waits, with nothing consumed (call again). One runtime
call; allocates nothing.

An empty buf returns 0 without reading, after the cancellation check.
A count outside 0..buf.Len() from the OS layer is Os(code: 0).

### `Stream.Reader`

```
export Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Reader over this stream, using buf as its buffer. Each fill
is one Read; any net.Error becomes the Reader's ReadError. Bytes it
buffered and the caller did not read are lost when it is dropped.
O(1); allocates nothing.

### `Stream.RecvHandles`

```
export Stream.RecvHandles(&self, a: &Alloc, io: Io, buf: mut []u8, into: mut HeapList[Passed]): Error!int
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Receives at most buf.Len() bytes, and the handles that came with them
(SendHandles' first byte), each pushed onto into in the order sent:
the bytes, 0 at the end of the stream. Bytes with no handles are
returned as Read returns them, so a caller that expects handles at a
known place in the stream reads up to it with Read and then calls this.
Waits while nothing is queued; a cancel or a deadline ends the wait with
Canceled and nothing taken: call again.
Errors: Os(code: 0) for an empty buf; Unsupported as SendHandles, and
for a handle received that is a socket but no stream socket (a foreign
sender's: the bytes are consumed and every handle of the message
closed); Os(code: 90) for more than 64 handles in one message (the
same); OutOfMemory when into cannot grow or std cannot allocate the
record it keeps for a handle (the same; the handles already pushed stay in
into); Canceled; Reset for a peer gone; Os for any other failure. O(1)
system calls per try; allocates a runtime record per handle and what
into's Push takes from a.

### `Stream.SendHandles`

```
export Stream.SendHandles(&self, io: Io, data: []u8, handles: []Pass): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends data with the handles attached to its first byte (SCM_RIGHTS):
the peer takes them with RecvHandles, which reads that byte.
The rest of data follows as WriteAll's bytes. Each handle is lent: the
peer gets its own copy, and a socket among them is shared from then on
(closing a copy releases only that one, with no shutdown). A peer that
reads the handles' byte with Read instead gets the bytes, and the
handles are closed in its process.
Errors: Os(code: 0) for an empty data, no handles, more than 64, or a
handle with no OS handle behind it (a simulated Io's); Unsupported on
Windows (an AF_UNIX socket there carries no handles), for a stream that
is not a Unix stream, and under a simulated Io; Canceled when the scope
is cancelled, at entry or while the socket's buffer is full (nothing
sent: call again), or while the rest is written (the handles and a
prefix of data sent: close the stream); Closed or Reset for a peer gone;
Os for any other failure. O(1) system calls per try, plus WriteAll's
for the rest; while the buffer is full it sleeps between tries (1 ms
doubling to 50 ms); allocates nothing.

### `Stream.SetBuffers`

```
export Stream.SetBuffers(&self, recv: int, send: int): Error!void
```

Asks the OS for receive and send buffer sizes of `recv` and `send`
bytes (0 keeps the current size). Errors: Unsupported on another Io's
stream; Os for the OS's refusal. O(1): at most two system calls;
allocates nothing.

### `Stream.SetKeepAlive`

```
export Stream.SetKeepAlive(&self, idle: ?time.Duration): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Turns TCP keep-alive probes on, the first after idle without traffic
(rounded up to whole seconds), or off with none. An idle of zero or
less turns probes on and keeps the socket's idle time: the OS's
default (Linux tcp_keepalive_time, two hours on Windows), or the one
an earlier SetKeepAlive on this stream set.
Errors:
Unsupported on a Unix stream or another Io's stream, as SetNoDelay; Os
for the OS's refusal, an idle time past its range included (Linux takes
at most 32,767 seconds). O(1): one or two system calls; allocates
nothing.

### `Stream.SetNoDelay`

```
export Stream.SetNoDelay(&self, on: bool): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends small writes at once (on true: TCP_NODELAY, Nagle's algorithm
off) or lets the OS gather them (false). Errors: Unsupported on a Unix
stream, or on a stream an Io other than the native one made (std.sim's,
a Recorder's); Os for the OS's refusal. O(1): one system call;
allocates nothing.

### `Stream.Shutdown`

```
export Stream.Shutdown(&self, io: Io, how: Shutdown): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Ends one or both directions of the stream (shutdown(2)); the handle
stays open until Close. Shutdown(Write) is the half-close a
request-response protocol sends after its request. Errors: Canceled in
a cancelled scope, checked first, and not cancellable after that;
Unsupported for another Io's stream;
Os for the OS's refusal (ENOTCONN on Linux, for a stream the peer
reset). O(1): one system call, which does not wait; allocates nothing.

### `Stream.Write`

```
export Stream.Write(&self, io: Io, data: []u8): Error!int
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends once and returns the count sent, which may be less than
data.Len(). Errors: Reset when the peer dropped the connection;
Canceled. Empty data sends nothing, after the cancellation check. A
Write that returns Canceled may have sent some or all of data, so
calling it again may send those bytes twice: close the stream. One
runtime call; allocates nothing.

### `Stream.WriteAll`

```
export Stream.WriteAll(&self, io: Io, data: []u8): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, AddrInUse, InvalidAddress, MuxClosed(code: u64, app: bool, peer: bool), Os(code: i64), Refused, Reset, SecureFailed(why: SecureFailure), StreamReset(code: u64), Unreachable, Unsupported, time.TimedOut)

Sends every byte of data, looping over partial sends, and fails with
the first failed send's error. On Canceled an unknown prefix of data
was sent: close the stream. O(n); allocates nothing.

A send that makes no progress on non-empty data, which the OS layer
never answers, is reported as Os(code: 0) instead of looping forever.

### `Stream.Writer`

```
export Stream.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Writer over this stream, using buf as its buffer. Each
flush sends with WriteAll; any failure becomes the Writer's WriteError.
Dropping it with buffered bytes loses them: Flush first. O(1);
allocates nothing.

### `StreamReset`

```
export error StreamReset(code: u64)
```

The peer reset a stream of a multiplexed connection or asked it to
stop sending (HTTP/2's RST_STREAM, QUIC's RESET_STREAM and
STOP_SENDING); code is the peer's error code. The connection and its
other streams go on. Provisional.

### `Transport`

```
export interface Transport {
    Read(&self, io: Io, buf: mut []u8): Error!int
    WriteAll(&self, io: Io, data: []u8): Error!void
    Shutdown(&self, io: Io, how: Shutdown): Error!void
    PeerAddr(&self): ?Addr
    Close(&self, io: Io): Error!void
    Abandon(self)
}
```

### `Unreachable`

```
export error Unreachable
```

No route leads to the address.

### `Unsupported`

```
export error Unsupported
```

What this target, this kind of socket or this Io cannot do: a Unix
socket before Windows 10 1803, NoDelay on a Unix stream, a socket
option under std.sim.

<!-- api:end -->
