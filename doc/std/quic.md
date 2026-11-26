# std.quic

`std.quic` implements QUIC version 1 (RFC 9000, RFC 9001, RFC 9002) over UDP and hands applications a [`net.Mux`](net.md) connection of multiplexed streams or a [`net.MuxListener`](net.md) of incoming connections. Handshakes use TLS 1.3 via [`std.tls`](tls.md) and [`std.x509`](x509.md).

`std.quic` is [Experimental](README.md#9-stability) and has not had an outside security review. Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.quic
```

## Contents

- [The main pieces](#the-main-pieces)
- [What it speaks](#what-it-speaks)
- [Configuration](#configuration)
- [Connection lifecycle and security limits](#connection-lifecycle-and-security-limits)
- [Errors](#errors)
- [Examples](#examples)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Settings | `Config`, starting from `DefaultConfig` |
| Client dialer | `Connector(&cfg)` giving a `net.MuxDialer` |
| Server listener factory | `Acceptor(&cfg)` giving a `net.MuxServer` |
| Open or accept streams | `Mux.Open` and `Mux.Accept` from [`std.net`](net.md) |
| Read, write and close | `Mux.Read`, `Mux.Write`, `Mux.Close`, `Mux.Abandon` |
| Protocol version constant | `Version1` (`0x00000001`) |

## What it speaks

- QUIC v1 (RFC 9000), with version negotiation, Retry (RFC 9000 8.1), 3x anti-amplification before address validation, connection ID issuance/retirement, and stateless resets.
- QUIC-TLS 1.3 (RFC 9001) across Initial, Handshake and 1-RTT spaces with AES-128-GCM, AES-256-GCM, and ChaCha20-Poly1305, key-phase updates in both directions, and AEAD confidentiality/integrity limits.
- Loss detection, NewReno congestion control, persistent congestion, and 1.25x pacing (RFC 9002).
- `ACK_ECN` frames are parsed and ECN counts ignored; `DATAGRAM` and `0-RTT` are not enabled.
- NAT port rebinding on an established connection keeps congestion state; an IP address change triggers path validation and resets RTT/cwnd.

## Configuration

`Config` is a sealed plain struct; start from `DefaultConfig` and override the fields you need:

| Field | Default | Meaning |
|---|---|---|
| `tls` | `none` | `&tls.Config` holding `roots`, `identities`, and handshake timeouts (`none` uses `tls.DefaultConfig`) |
| `idle` | `30 s` | `max_idle_timeout` advertised to the peer (the smaller of both sides' limits applies) |
| `streams` | `100` | `initial_max_streams_bidi` granted by a server (`0` for clients by default) |
| `half_open` | `1000` | Maximum concurrent server handshakes in progress before dropping new Initials |
| `retry_above` | `256` | Send stateless Retry packets once active handshakes exceed this threshold (`0` forces Retry; `>= half_open` disables) |

## Connection lifecycle and security limits

- A `net.MuxListener` returned by `Acceptor(&cfg).Listen(...)` requires a background task driving `listener.Run(io)` to read incoming UDP datagrams and route them to connections.
- `Mux` connections have no hidden background tasks; `Read`, `Write`, `Open`, `Accept`, `Wait`, and `Close` advance timers, retransmissions, and ACKs cooperatively.
- On the server, at most 64 unaccepted handshakes queue (`Accept` expiry tracks the handshake limit); stateless resets are capped at 64/sec, out-of-order `CRYPTO` buffers at 64 KiB per space, and ACK gap lists at 32 ranges.

## Errors

`Connector` and `Acceptor` surface failures through `net.Error`: `TimedOut` on idle/handshake expiry, `SecureFailed` on TLS/certificate failure, `MuxClosed` on a peer or local `CONNECTION_CLOSE`, `StreamReset` on peer stream reset/stop, `Unsupported` on version negotiation rejection, and `Canceled` when cancelled.

## Examples

### Client and server over `net.Mux`

```mk
// verdict: ok
import std.net
import std.quic
import std.tls

RunExample(h: &Heap, io: Io, tls_srv: &tls.Config, tls_cli: &tls.Config): net.Error!void {
    var scfg := quic.DefaultConfig
    scfg.tls = some(tls_srv)
    var ccfg := quic.DefaultConfig
    ccfg.tls = some(tls_cli)

    var ac := quic.Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()

    var cn := quic.Connector(&ccfg)
    _ = cn
    _ = quic.Version1
}
```

## API

<!-- api:begin -->
Stability: Experimental (not yet reviewed by an outside cryptographer).

### `Acceptor`

```
export Acceptor(c: &Config): net.MuxServer from c
```

- Flows: The result borrows from c.

The server's, for `http.ServerOptions.h3`. O(1); allocates nothing
until it listens.

### `Config`

```
export struct Config
```

The settings of QUIC connections: the TLS configuration, the idle
limit, the streams a server grants, and the server's handshake limits.
Start from DefaultConfig; a literal does not compile outside std.

Fields:

- `tls: ?&tls.Config`: The TLS settings (roots, identities, handshake limit); none uses
  tls.DefaultConfig. ALPN comes from the caller (std.http: "h3").
- `idle: time.Duration`: max_idle_timeout this side sends (RFC 9000 10.1); the smaller of
  both sides' applies.
- `streams: int`: initial_max_streams_bidi a server grants, renewed as streams end;
  a client grants none.
- `half_open: int`: A server's handshakes in progress at once; more Initials are dropped.
- `retry_above: int`: A server answers Initials with Retry while more than this many
  handshakes are in progress; 0 always, half_open or more never.

### `Connector`

```
export Connector(c: &Config): net.MuxDialer from c
```

- Flows: The result borrows from c.

The client connector over `c`, for `http.ClientLimits.h3` or any program
that wants a `net.Mux`. O(1); allocates nothing until it dials.

### `DefaultConfig`

```
export const DefaultConfig: Config
```

30 s idle, 100 streams, 1,000 half-open, Retry above 256.

### `Version1`

```
export const Version1: u32 = 0x00000001
```

QUIC version 1, the version this module speaks (RFC 9000 15).

<!-- api:end -->
