# std.tls

`std.tls` turns a [`net.Conn`](net.md) into a TLS connection: TLS 1.3 as a client or a server, and TLS 1.2 as a client when you allow it. Use it directly for a TLS protocol of your own, or hand its connectors to [`std.http`](http.md) and [`std.websocket`](websocket.md) for `https` and `wss`. It checks the server's certificate chain with [`std.x509`](x509.md).

`std.tls` is [Experimental](README.md#9-stability), and it has not had an outside security review. `https` in [`std.http`](http.md) and `wss` in [`std.websocket`](websocket.md) go through it, so they are Experimental too. Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.tls
```

## Contents

- [The main pieces](#the-main-pieces)
- [What it speaks](#what-it-speaks)
- [Configuration](#configuration)
- [Connections](#connections)
- [Connectors for HTTP and WebSockets](#connectors-for-http-and-websockets)
- [Errors](#errors)
- [Time limits and cancellation](#time-limits-and-cancellation)
- [Examples](#examples): [a TLS echo](#a-tls-echo), [talk to a server](#talk-to-a-server), [serve https](#serve-https)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Connect as a client | `Client(h, io, conn, &cfg, host)` |
| Accept as a server | `Server(h, io, conn, &cfg)` with `cfg.identities` set |
| Settings | `Config`, from `DefaultConfig` |
| A server's certificate and key | `Identity` |
| Read and write | `Conn.Read`, `Conn.WriteAll`, `Conn.Reader`, `Conn.Writer` |
| The protocol the handshake chose | `Conn.Alpn` |
| End a connection | `Conn.Close`, or `Conn.Abandon` on an error path |
| `https` and `wss` clients | `Connector(&cfg)` in `http.ClientLimits.secure` or `websocket.Options.secure` |
| An `https` server | `Acceptor(&cfg)` in `http.ServerOptions.secure` |

## What it speaks

- TLS 1.3 (RFC 8446), client and server, with X25519 and P-256 key shares, the cipher suites TLS_AES_128_GCM_SHA256, TLS_AES_256_GCM_SHA384 and TLS_CHACHA20_POLY1305_SHA256, HelloRetryRequest, KeyUpdate, ALPN and SNI. A server's key is P-256 or Ed25519.
- A client accepts a server certificate whose key is P-256, P-384, RSA of 2048 bits or more, or Ed25519.
- A TLS 1.2 client (RFC 5246) with ECDHE and AEAD suites and the extended master secret. It is off unless `min_version` is `Tls12`. A TLS 1.2 server without the extended master secret is refused with `Unsupported`.
- There is no resumption, PSK, 0-RTT, client certificate, renegotiation or compression. A client asked for a certificate answers with none; a TLS 1.2 server's renegotiation request fails the connection with `Protocol`.

Every key and secret lives in a `std.crypto` key object or in a buffer of the connection that is wiped when it is replaced or the connection ends. No branch, index or loop bound depends on a secret.

## Configuration

A `Config` is a plain record of settings: references and slices into your memory, `Send` and `Share`, so one `Config` serves every task. Start from `DefaultConfig` and assign the fields you change; a literal of it does not compile outside std.

| Field | Default | Meaning |
|---|---|---|
| `roots` | `none` | The certificates a client trusts; `none` is the system's |
| `identities` | empty | A server's certificate chains and keys; it presents the first whose key the client's signature schemes allow |
| `alpn` | empty | The application protocols offered (a client) or accepted (a server), in preference order |
| `min_version` | `Tls13` | The lowest version a client accepts; `Tls12` turns on the TLS 1.2 client |
| `handshake` | 10 s | The longest a handshake may take |
| `close` | 5 s | The longest `Close` may wait for the peer |

With `roots` set to `none`, a client verifies against the system's roots: `x509.SystemPool`, or `x509.SystemRoots` under a `Sim`'s, `Recorder`'s or `Replayer`'s `Io`. See [the system's roots](x509.md#the-systems-roots).

A client checks the server's chain with `x509.Verify` for the `host` it was given, at the wall clock. `Client` refuses an empty `host` with `Unsupported` before it sends anything, since an empty name would match any certificate.

## Connections

`Client` and `Server` take the `net.Conn` and run the handshake. On success the returned `Conn` holds the connection; on failure the connection stays yours, mid-handshake, to `Close` or `Abandon`.

A `tls.Conn` is linear, `Send` and `Share`. Its read half and its write half each have a lock, so one task can `Read` while another calls `WriteAll`. Its state, about 66 KiB, is on the heap you pass. It keeps that heap and the connection, but never its `Config`. It copies what it needs of the `Config` and the host name, such as the chosen ALPN protocol, so it outlives both.

The signature states it: the result is `from h, conn`, and the `Config` is a local that ends with the function.

```mk
// verdict: ok
// tls.Client's result is from h and conn
import std.net
import std.tls

Secure(h: &Heap, io: Io, conn: net.Conn, host: str): tls.Error!tls.Conn from h, conn {
    cfg := tls.DefaultConfig
    return try tls.Client(h, io, conn, &cfg, host)
}
```

- `Read` returns 0 once the peer sent `close_notify`. A connection that ends without one fails with `net.Reset`.
- `WriteAll` sends records of at most 16 KiB. After `close_notify` was sent, or the write side was shut down, it fails with `Closed`; reading still works, which is a half-close.
- `Close` sends `close_notify`, then releases the connection and wipes every key. `Abandon` releases it without speaking to the peer and never fails.
- `tls.Conn` does not implement `Free`; a container of them is released with `FreeEach`.

## Connectors for HTTP and WebSockets

`std.http` and `std.websocket` import no TLS. They take a [`net.Secure`](net.md#conn-one-connection-type):

- `Connector(&cfg)` makes `https` and `wss` connections through `Client` with `cfg`, for the URL's host. Put it in `http.ClientLimits.secure` or `websocket.Options.secure`.
- `Acceptor(&cfg)` makes each accepted connection go through `Server` with `cfg`. Put it in `http.ServerOptions.secure`.

Both borrow `cfg`, so whatever holds them cannot outlive the `Config`. Through `net.Secure.Negotiate` (provisional) a caller offers, or accepts, its own protocols in place of `cfg.alpn` for one handshake, with the same checks, and learns the one chosen as an index into its list. A failed handshake through them is `net.SecureFailed(why)`: `Handshake` for an alert or a protocol fault, and, from a client, `BadCertificate`, `UnknownAuthority`, `Expired` or `NameMismatch` for the chain. One past the handshake limit is `time.TimedOut`. Past the handshake, an alert or protocol fault on the connection is `net.SecureFailed(why: Protocol)`. See [Make an HTTPS request](../cookbook.md#make-an-https-request).

## Errors

`tls.Error` holds the module's own three errors, the certificate check's and the connection's:

| Member | Meaning |
|---|---|
| `Alert(code)` | The peer sent an alert; `code` is its description, such as 40 `handshake_failure` or 70 `protocol_version` |
| `Protocol` | The peer broke the protocol; the matching alert was sent |
| `Unsupported` | The peer chose a version, cipher suite, group or signature scheme that std.tls does not offer or the `Config` refuses, or `host` was empty |
| `x509.Error` | The server's chain failed `x509.Verify`: `UnknownAuthority`, `Expired`, `NameMismatch` and the rest |
| `net.Error` | The connection's errors, with `Canceled` and `time.TimedOut` |

A `close_notify` is not an error: `Read` reports it as the end of the stream. `tls.Error` is open, so a `match` on it outside std keeps a `_` arm. The TLS parsers never panic on their input, and each holds memory and stack bounded by its limits.

## Time limits and cancellation

The handshake and a `Close` have their own limits in the `Config`, and neither is zero by default. Past either, the call fails with `time.TimedOut` ([cancellation](README.md#6-cancellation)). The close limit holds under a `NoCancel` `Io` too.

- `Conn.Read` loses nothing on `Canceled`: call it again.
- `Client` and `Server` cancelled mid-handshake leave the connection with you, with an unknown part of the handshake sent. Close it.
- An interrupted `WriteAll` or `Close` leaves the `Conn` failed: every later call but `Abandon` fails, and a call blocked in another task returns the same failure at once. So does a `net.Conn` over a `tls.Conn` whose `Shutdown` was interrupted.
- `Close` speaks to the peer, so it can be cancelled. On `Canceled` or `time.TimedOut` the `Conn` stays yours, failed: write `onerr c.Abandon()` with `try c.Close(io)`.

## Examples

### A TLS echo

A server task and a client in one program, over a loopback port. The root, the leaf and the leaf's key are inline so the program runs as written; a real server reads its key from a file and never prints it.

```mk
// verdict: ok
import std.crypto
import std.encoding
import std.net
import std.tls
import std.x509

// An example root, a leaf for "localhost" that it signed, and the leaf's
// private key, all Ed25519. Never use a key printed in a document.
const RootPem = """
    -----BEGIN CERTIFICATE-----
    MIIBIDCB06ADAgECAgEBMAUGAytlcDAXMRUwEwYDVQQDEwxFeGFtcGxlIFJvb3Qw
    IBcNMDAwMTAxMDAwMDAwWhgPMjIwMDAxMDEwMDAwMDBaMBcxFTATBgNVBAMTDEV4
    YW1wbGUgUm9vdDAqMAUGAytlcAMhANnzsXfsxcIj/ETRasmLo4vbmKlc9XxZQLVI
    cfe2TgSLo0IwQDAOBgNVHQ8BAf8EBAMCAgQwDwYDVR0TAQH/BAUwAwEB/zAdBgNV
    HQ4EFgQU67ajDfD2Xw/oHcBQKOmkzyN2aV8wBQYDK2VwA0EAuZ/QycTfOdC2S0Ar
    KkZnIBer6PaHPdXjx2MO2E2j7D8eFN/c6jVvrlhO+NxFPI45H0nPSqHSI89AUf75
    BUgODw==
    -----END CERTIFICATE-----
    """

const LeafPem = """
    -----BEGIN CERTIFICATE-----
    MIIBOTCB7KADAgECAgECMAUGAytlcDAXMRUwEwYDVQQDEwxFeGFtcGxlIFJvb3Qw
    IBcNMDAwMTAxMDAwMDAwWhgPMjIwMDAxMDEwMDAwMDBaMBQxEjAQBgNVBAMTCWxv
    Y2FsaG9zdDAqMAUGAytlcAMhANBmwSeoHpT9eCt6+f7oFxmsIeJrouNCTnTFekRB
    vvYco14wXDAOBgNVHQ8BAf8EBAMCB4AwEwYDVR0lBAwwCgYIKwYBBQUHAwEwHwYD
    VR0jBBgwFoAU67ajDfD2Xw/oHcBQKOmkzyN2aV8wFAYDVR0RBA0wC4IJbG9jYWxo
    b3N0MAUGAytlcANBAN2ehnvlk4S43y+eKSSrij4BmOiOWm1AfNivFi78oBHEKi7O
    bRBZ0BXOtSNCIamPqmC9DV5sHFvzPN9egmDxOg8=
    -----END CERTIFICATE-----
    """

const KeyPem = """
    -----BEGIN PRIVATE KEY-----
    MC4CAQAwBQYDK2VwBCIEIKi7HZo6/jdLCaguwKT9fGA9zRbVgyCPODSSIDJuXtqg
    -----END PRIVATE KEY-----
    """

const Protocols: []str = ["echo/1"]

// Echo accepts one connection, makes it a TLS server connection, and
// sends back what it reads.
Echo(h: &Heap, io: Io, l: &net.Listener, cfg: &tls.Config) {
    stream := l.Accept(io) else {
        return
    }
    conn := stream.Conn()
    c := tls.Server(h, io, conn, cfg) else {
        conn.Abandon() // on failure the connection stays ours
        return
    }
    var buf: [64]u8 = [0; 64]
    n := c.Read(io, mut buf[..]) else {
        c.Abandon()
        return
    }
    c.WriteAll(io, buf[..n]) else {
        c.Abandon()
        return
    }
    c.Close(io) else {
        c.Abandon()
    }
}

export Main(sys: &System): tls.Error|encoding.Error|crypto.Error|WriteError!void {
    roots := try x509.ParsePem(sys.arena, RootPem)
    chain := try x509.ParsePem(sys.arena, LeafPem)
    blocks := try encoding.ParsePem(sys.arena, KeyPem)
    key := try crypto.ParsePrivateKey(blocks.Items()[0].der)

    ids := [tls.Identity(chain: chain.Items(), key: &key)]
    var server := tls.DefaultConfig
    server.identities = ids[..]
    server.alpn = Protocols

    pool := x509.Pool.New(roots.Items())
    var client := tls.DefaultConfig
    client.roots = some(&pool) // trust this root, not the system's
    client.alpn = Protocols

    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    addr := try sys.arena.Fmt("127.0.0.1:{port}", (port: l.Port()))
    scope s := sys.io.Scope() {
        s.go Echo(sys.heap, sys.io, &l, &server)
        stream := try net.Dial(sys.io, addr)
        conn := stream.Conn()
        c := tls.Client(sys.heap, sys.io, conn, &client, "localhost") else e {
            conn.Abandon()
            fail e
        }
        onerr c.Abandon()
        proto := c.Alpn() else { "none" }
        try c.WriteAll(sys.io, "hello over TLS".AsBytes())
        var buf: [64]u8 = [0; 64]
        n := try c.Read(sys.io, mut buf[..])
        reply := str.From(buf[..n]) else { "?" }
        try sys.Print("protocol {proto}\nreply {reply}\n", (proto, reply))
        try c.Close(sys.io) // sends close_notify
    }
}
```

```text
$ mk run echo.mk
protocol echo/1
reply hello over TLS
```

- `client.roots` names the example root, so the client trusts only it. With `DefaultConfig`'s `none`, it would use the system's roots.
- Both sides list `echo/1` in `alpn`, so the handshake chooses it.
- On a failed handshake the `net.Conn` stays with the caller, who abandons it.

### Talk to a server

A client of a TLS protocol other than HTTP: dial, run the handshake, read the server's greeting and close. It needs the network, so this block only compiles and has no output to show.

```mk
// verdict: ok
import std.net
import std.tls

const Protocols: []str = ["imap"]

// Greeting connects to host:993, accepts TLS 1.2 as well as 1.3, and
// returns the server's first line, copied into a.
Greeting(h: &Heap, a: &Arena, io: Io, host: str): tls.Error|ReadError|OutOfMemory!str from a {
    var cfg := tls.DefaultConfig // the system's roots
    cfg.min_version = tls.Version.Tls12
    cfg.alpn = Protocols
    addr := try a.Fmt("{host}:993", (host))
    stream := try net.Dial(io, addr)
    conn := stream.Conn()
    c := tls.Client(h, io, conn, &cfg, host) else e {
        conn.Abandon() // the handshake failed: the connection is still ours
        fail e
    }
    onerr c.Abandon()
    var buf: [512]u8 = [0; 512]
    var r := c.Reader(io, mut buf[..])
    line := try r.Line(a) else { "" } // none: the server closed first
    try c.Close(io)
    return line
}
```

- `DefaultConfig` checks the chain against the system's roots. `min_version` set to `Tls12` lets the server choose TLS 1.2.
- `c.Reader` buffers reads; each fill is one `Conn.Read`.
- `Close` sends `close_notify` and can fail, so it is a `try` at the end, with `onerr c.Abandon()` for the paths that fail before it.

### Serve https

`Acceptor` gives `std.http`'s server a TLS handshake on every connection. The server reads its chain from `cert.pem` and its key from `key.pem`. This block compiles; it serves until the program is stopped, so it has no output to show.

```mk
// verdict: ok
import std.crypto
import std.encoding
import std.fs
import std.http
import std.tls
import std.x509

struct Site {
    name: str
}

Home(c: &Site, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    var w := res.Writer()
    try Print(mut w, "welcome to {name}\n", (name: c.name))
}

// Main serves https on port 8443 with the chain in cert.pem and the key in
// key.pem, until the program is stopped.
export Main(sys: &System): http.Error|fs.Error|x509.Error|encoding.Error|crypto.Error!void {
    chain := try x509.ParsePem(sys.arena, try fs.ReadText(sys.arena, sys.io, "cert.pem"))
    blocks := try encoding.ParsePem(sys.arena, try fs.ReadText(sys.arena, sys.io, "key.pem"))
    key := try crypto.ParsePrivateKey(blocks.Items()[0].der)
    ids := [tls.Identity(chain: chain.Items(), key: &key)]
    var cfg := tls.DefaultConfig
    cfg.identities = ids[..]
    var o := http.DefaultServerOptions
    o.secure = some(tls.Acceptor(&cfg)) // every connection goes through tls.Server
    site := Site(name: "example")
    try http.ServeWith(sys, ":8443", &o, &site, Home)
}
```

- `ServeWith` sends each connection through `tls.Server` before it reads a request, and a failed handshake closes the connection.
- The identity's key must be P-256 or Ed25519.

## API

Generated from the module's doc comments by `mk doc --std std.tls` ([reading the API](README.md#reading-an-api-section)).

<!-- api:begin -->
Stability: Experimental (not yet reviewed by an outside cryptographer).

### `Acceptor`

```
export Acceptor(c: &Config): net.Secure from c
```

- Flows: The result borrows from c.

A server connector over c, to put in http.ServerOptions.secure: each
connection the server accepts goes through tls.Server with c. A failed
handshake is net.SecureFailed(why: Handshake), and one past c.handshake
is time.TimedOut. Its Negotiate accepts the caller's protocols, in
their order, in place of c.alpn. It is a plain value that borrows c;
copy it freely. O(1); allocates nothing until it accepts.

### `Alert`

```
export error Alert(code: u8)
```

The peer sent an alert; code is the alert's description, such as 40
handshake_failure or 70 protocol_version. A close_notify is not an
error: Read reports it as the end of the stream.

### `Client`

```
export Client(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str): Error!Conn from h, conn
```

- Flows: The result borrows from h and conn.
- Errors: Error (Canceled, Closed, OutOfMemory, net.AddrInUse, net.InvalidAddress, net.MuxClosed(code: u64, app: bool, peer: bool), net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.StreamReset(code: u64), net.Unreachable, net.Unsupported, time.TimedOut, Alert(code: u8), Protocol, Unsupported, x509.BadSignature, x509.BadUsage, x509.Expired, x509.Malformed(offset: int), x509.NameMismatch, x509.TooComplex, x509.UnknownAuthority, x509.Unsupported)

Runs the client side of a TLS handshake over conn with the server
named host, and returns the TLS connection. On success the Conn holds
conn; on failure conn stays the caller's, mid-handshake, to Close or
Abandon. Two scalar multiplications and the chain's checks; allocates
the connection's state on h (about 66 KiB, freed by Close or Abandon)
and the handshake's buffers on an arena of h, freed before it returns.

It speaks TLS 1.3 (RFC 8446), or TLS 1.2 (RFC 5246, ECDHE and AEAD
suites) when c.min_version is Tls12 and the server chooses it; a TLS
1.2 server without the extended master secret is refused with
Unsupported. A TLS 1.2 connection's Read fails with Protocol on a
HelloRequest, since there is no renegotiation. The handshake uses c's
roots, protocols and limits, and x509.Verify checks the server's chain
for host at the wall clock. A server's request for a client certificate
is answered with none.

With roots none, the chain is checked against the system's pool,
x509.SystemPool: on Linux the whole store is read and parsed by the
process's first such handshake that finds a certificate, and kept on h
until exit. Under a simulated or taped Io, SystemRoots' pool is loaded
through that Io at each handshake.

Errors: the peer's Alert; Protocol and Unsupported, after the matching
alert was sent; Unsupported also, before any I/O, for an empty host,
which names nothing to check; x509.Error's members for the chain; the
connection's net.Error, and time.TimedOut past c.handshake;
OutOfMemory. A cancel leaves conn with the caller mid-handshake, with
an unknown part of the handshake sent: close it.

### `Config`

```
export struct Config
```

The settings of a TLS connection: what it trusts, presents and allows.
A plain record of references and slices into the caller's memory, Send
and Share, so one Config serves every task. Start from DefaultConfig and
assign the fields to change; a literal of it does not compile outside
std. A Conn copies what it needs, so a Config may go once the handshake
is done.

Fields:

- `roots: ?&x509.Pool`: The certificates a client trusts. none means the system's
  (x509.SystemPool), loaded by the first handshake that needs it and
  kept for the process; on Linux the store is read once and kept when
  it holds a certificate. Under a simulated or taped Io (a Sim's,
  Recorder's or Replayer's), which SystemPool refuses, each handshake
  loads SystemRoots' pool through that Io instead.
- `identities: []Identity`: A server's chains and keys. The server presents the first whose key
  the client's signature schemes allow.
- `alpn: []str`: The application protocols offered (a client) or accepted (a
  server), in preference order (RFC 7301); empty offers none.
- `min_version: Version`: The lowest version a client accepts. Tls12 turns on the TLS 1.2
  client; the default, Tls13, offers TLS 1.3 only.
- `handshake: time.Duration`: The longest a handshake may take before it fails with
  time.TimedOut; zero or less fails at once.
- `close: time.Duration`: The longest a Close may wait for the peer before it fails with
  time.TimedOut, also under a NoCancel Io.

### `Conn`

```
export linear view struct Conn
```

A TLS connection, made by Client or Server. It is linear, Send and
Share: tasks share it by reference, one reading while another writes,
since its read and write halves each have a lock. End it with Close or
Abandon.

Its state is on the heap given to Client or Server. It keeps that heap
and its connection, never its Config: it copies what it needs of the
Config and the host name, such as the chosen ALPN protocol, so it
outlives both.

### `Conn.Abandon`

```
export Conn.Abandon(self)
```

Releases the connection without speaking to the peer: every key wiped,
the state freed, the transport abandoned. Use it on error paths. Never
blocks or fails.

### `Conn.Alpn`

```
export Conn.Alpn(&self): ?str from self
```

- Flows: The result borrows from self.

The application protocol (ALPN, RFC 7301) the handshake chose, or none
when none was offered or the peer chose none. O(1); allocates nothing.

### `Conn.Close`

```
export Conn.Close(self, io: Io): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, net.AddrInUse, net.InvalidAddress, net.MuxClosed(code: u64, app: bool, peer: bool), net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.StreamReset(code: u64), net.Unreachable, net.Unsupported, time.TimedOut, Alert(code: u8), Protocol, Unsupported, x509.BadSignature, x509.BadUsage, x509.Expired, x509.Malformed(offset: int), x509.NameMismatch, x509.TooComplex, x509.UnknownAuthority, x509.Unsupported)

Sends close_notify to the peer, then releases the connection and its
state, every key wiped. Frees the state; allocates nothing.

Errors: Canceled under a cancelled scope; time.TimedOut past the
Config's close limit, also under a NoCancel Io and for a cancel that
comes after the limit passed; the connection's net.Error; an earlier
failure of the Conn. On any of them the Conn stays the caller's, failed,
to Abandon: write `onerr c.Abandon()` with `try c.Close(io)`.

### `Conn.Read`

```
export Conn.Read(&self, io: Io, buf: mut []u8): Error!int
```

- Errors: Error (Canceled, Closed, OutOfMemory, net.AddrInUse, net.InvalidAddress, net.MuxClosed(code: u64, app: bool, peer: bool), net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.StreamReset(code: u64), net.Unreachable, net.Unsupported, time.TimedOut, Alert(code: u8), Protocol, Unsupported, x509.BadSignature, x509.BadUsage, x509.Expired, x509.Malformed(offset: int), x509.NameMismatch, x509.TooComplex, x509.UnknownAuthority, x509.Unsupported)

Reads at most buf.Len() bytes of application data and returns their
count, 0 once the peer sent close_notify. Reads of this Conn run one at
a time. Opens at most one record per call into its own buffer;
allocates nothing.

Errors: the peer's Alert, Protocol (an alert was sent), and the
connection's net.Error, with Reset when it ends without close_notify.
After a failure every call reports it. A cancelled Read loses
nothing, so it can be called again.

### `Conn.Reader`

```
export Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Reader for this Conn, with buf as its buffer. Each fill is
one Conn.Read, so two Readers of one Conn take turns per fill. Any
error is its ReadError; whether it was a timeout is read from the
scope. Bytes it buffered and the caller did not read are lost when it is
dropped. O(1); allocates nothing.

### `Conn.WriteAll`

```
export Conn.WriteAll(&self, io: Io, data: []u8): Error!void
```

- Errors: Error (Canceled, Closed, OutOfMemory, net.AddrInUse, net.InvalidAddress, net.MuxClosed(code: u64, app: bool, peer: bool), net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.StreamReset(code: u64), net.Unreachable, net.Unsupported, time.TimedOut, Alert(code: u8), Protocol, Unsupported, x509.BadSignature, x509.BadUsage, x509.Expired, x509.Malformed(offset: int), x509.NameMismatch, x509.TooComplex, x509.UnknownAuthority, x509.Unsupported)

Sends every byte of data, as records of at most 16 KiB, before any
other WriteAll of this Conn sends one. It first sends the KeyUpdate the
peer asked for, if one is owed. O(data); allocates nothing.

Errors: the connection's net.Error, and Closed once close_notify was
sent or the write side shut down, which leaves reading working (a
half-close). After a failure every call reports it. An interrupted
WriteAll, a cancelled one included, leaves the connection failed, and
every later call but Abandon fails, unless a shutdown ended it (Closed).

### `Conn.Writer`

```
export Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
```

- Flows: The result borrows from self and buf.

A buffered Writer for this Conn, with buf as its buffer. Each flush of
the buffer is one Conn.WriteAll. Any error is its WriteError. Flush it
before you drop it. O(1); allocates nothing.

### `Connector`

```
export Connector(c: &Config): net.Secure from c
```

- Flows: The result borrows from c.

A client connector over c, to put in http.ClientLimits.secure or
websocket.Options.secure: their https and wss connections then go through
tls.Client with c, for the URL's host. O(1); allocates nothing until it
connects.

A failed handshake is net.SecureFailed(why): Handshake for an alert or
a protocol fault, and BadCertificate, UnknownAuthority, Expired or
NameMismatch for the chain. One past c.handshake is time.TimedOut.
Its Negotiate offers the caller's protocols in place of c.alpn, with the
same checks. It is a plain value that borrows c; copy it freely.

### `DefaultConfig`

```
export const DefaultConfig: Config
```

The default settings: trust the system's roots (roots is none), offer
no application protocol, TLS 1.3 only, a 10-second handshake limit and
a 5-second close limit, and no server identities.

### `Error`

```
export open type Error = Alert | Protocol | Unsupported | x509.Error | net.Error
```

Every failure of std.tls: its own three, the certificate check's
(x509.Error), and the connection's (net.Error), which include Canceled
and time.TimedOut for a handshake or a Close past its limit. The set is
open, so a match on it outside std keeps a `_` arm.

### `Identity`

```
export struct Identity
```

What a server presents: a certificate chain, leaf first, and the leaf's
private key, which is P-256 or Ed25519.

Fields:

- `chain: []x509.Certificate`: The chain, leaf first, as the server sends it.
- `key: &crypto.PrivateKey`: The leaf's private key.

### `Protocol`

```
export error Protocol
```

The peer broke the protocol: a malformed or unexpected message or
record, a bad Finished or signature, or a value RFC 8446 forbids. The
connection sent the matching alert before it failed.

### `Server`

```
export Server(h: &Heap, io: Io, conn: net.Conn, c: &Config): Error!Conn from h, conn
```

- Flows: The result borrows from h and conn.
- Errors: Error (Canceled, Closed, OutOfMemory, net.AddrInUse, net.InvalidAddress, net.MuxClosed(code: u64, app: bool, peer: bool), net.Os(code: i64), net.Refused, net.Reset, net.SecureFailed(why: SecureFailure), net.StreamReset(code: u64), net.Unreachable, net.Unsupported, time.TimedOut, Alert(code: u8), Protocol, Unsupported, x509.BadSignature, x509.BadUsage, x509.Expired, x509.Malformed(offset: int), x509.NameMismatch, x509.TooComplex, x509.UnknownAuthority, x509.Unsupported)

Runs the server side of a TLS 1.3 handshake (RFC 8446) over conn, and
returns the TLS connection. On success the Conn holds conn; on failure
conn stays the caller's, mid-handshake, to Close or Abandon. One
signature and two scalar multiplications; allocates the connection's
state on h (about 66 KiB, freed by Close or Abandon) and the
handshake's buffers on an arena of h, freed before it returns.

It presents the first of c's identities whose key the client's
signature schemes allow (P-256 or Ed25519), and uses c's protocols and
limits. It asks for no client certificate.

Errors: the peer's Alert; Protocol and Unsupported, after the matching
alert was sent (Unsupported: no common version, suite, group, scheme or
protocol); the connection's net.Error, and time.TimedOut past
c.handshake; OutOfMemory. A cancel leaves conn with the caller
mid-handshake, with an unknown part of the handshake sent: close it.

### `Unsupported`

```
export error Unsupported
```

The peer chose what std.tls does not support or the configuration
refuses: a version below the configured minimum, or a cipher suite,
group or signature scheme std.tls does not offer.

### `Version`

```
export enum Version
```

A protocol version std.tls speaks: TLS 1.2 (client only) or TLS 1.3.

Variants:

- `Tls12`
- `Tls13`

<!-- api:end -->
