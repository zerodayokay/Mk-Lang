// Why: a client and a server need limits (time, bytes, connections) that a
// program can set field by field and that are never unlimited by mistake.
//
// What: ClientRequest, one request of a Client; ClientLimits with
// DefaultClientLimits; ServerOptions with DefaultServerOptions.
//
// How: both options types are plain, with one unexported field, so a
// program makes one from its Default constant and assigns the fields it
// changes. No limit is zero by default; each time limit, when it passes, is
// time.TimedOut, never a bare Canceled.

import std.compress
import std.net
import std.time
import std.url

/// One request for a Client: the method, the URL (http or https, parsed by
/// std.url), the header fields to send and the body, which is sent with a
/// Content-Length. A plain record that borrows all four.
export struct ClientRequest {
    /// The method, such as "GET".
    export method: str
    /// The URL, http or https.
    export url: &url.Url
    /// The header fields to send.
    export headers: []Header
    /// The body, sent with a Content-Length.
    export body: []u8
}

/// The settings of a Client: how long it waits, how much it reads, where it
/// connects and what it decodes. Start from DefaultClientLimits and assign
/// the fields to change; a literal of it does not compile outside std. No
/// limit is zero by default, and a time limit that passes fails with
/// time.TimedOut.
export struct ClientLimits {
    /// The time for resolving, connecting and the secure handshake, per
    /// connection.
    export connect: time.Duration
    /// The time from the request's last byte sent to the reply's head read
    /// whole.
    export response_header: time.Duration
    /// How long an idle pooled connection may wait for its next request.
    export idle: time.Duration
    /// The time a Close that speaks to the peer may take, such as a secure
    /// connection's close.
    export close: time.Duration
    /// The bytes of a reply's head, interim replies included.
    export header_bytes: int
    /// The decoded bytes Client.Fetch reads into its Reply; more is
    /// TooLarge.
    export body_bytes: i64
    /// The redirects followed before TooManyRedirects; 0 follows none and
    /// returns the 3xx reply as it is.
    export redirects: int
    /// The connections the client keeps for reuse, over every origin, at
    /// most 16; 0 keeps none.
    export idle_connections: int
    /// The content codings the client advertises in Accept-Encoding and
    /// undoes, in order of preference; empty sends no Accept-Encoding and
    /// undoes nothing.
    export decoders: []compress.Content
    /// Asked before every connect, redirects included, with the address
    /// about to be dialled; false refuses it, and an address refused fails
    /// the connect as Refused. A service that fetches URLs it is given
    /// refuses loopback and private ranges here. With a proxy, the address
    /// dialled is the proxy's.
    export allow: ?fn(&net.Addr): bool
    /// The connector that https URLs go through, made by tls.Connector.
    /// With none, an https URL fails with NoSecureTransport.
    export secure: ?net.Secure
    /// The HTTP proxy every connection goes through: an http URL with a
    /// host, an optional port (80 by default) and no userinfo, else BadUrl.
    /// Its path and query are not used. An http request is sent to it in
    /// absolute form, and an https one through a CONNECT tunnel, then the
    /// secure handshake with the origin. allow is then asked about the
    /// proxy's addresses only: the proxy, not the client, resolves the
    /// origin's name. none connects directly. std reads no proxy variable
    /// from the environment.
    export proxy: ?&url.Url
    /// Offer h2 and http/1.1 by ALPN on https; use h2 when the server picks it.
    export h2: bool
    /// Send every http:// request as HTTP/2 with prior knowledge (RFC 9113 3.3).
    export h2c: bool
    /// Streams this client opens at once on one h2 or h3 connection (also
    /// obeying the peer's limit).
    export h2_streams: int
    /// The receive window of each stream (h2 SETTINGS_INITIAL_WINDOW_SIZE).
    export h2_window: int
    /// Made by quic.Connector; https origins may then use HTTP/3.
    export h3: ?net.MuxDialer
    /// Use h3 for every https origin without waiting for Alt-Svc.
    export h3_direct: bool
    /// Time limit for a QUIC handshake before falling back to TCP.
    export h3_connect: time.Duration
    /// Read Alt-Svc: h3=":port" from https replies when h3 is enabled.
    export alt_svc: bool
    sealed: u8
}

/// The client's default settings: 30 s to connect and 30 s to wait for a
/// reply's head, 90 s idle, 5 s to close, a 1 MiB head, a 64 MiB body for
/// Client.Fetch, 10 redirects, 16 kept connections, no decoders, every
/// address allowed, no https and no proxy.
export const DefaultClientLimits: ClientLimits = ClientLimits(connect: time.Seconds(30), response_header: time.Seconds(30),
    idle: time.Seconds(90), close: time.Seconds(5), header_bytes: 1048576, body_bytes: 67108864, redirects: 10,
    idle_connections: 16, decoders: [], allow: none, secure: none, proxy: none, h2: false, h2c: false,
    h2_streams: 100, h2_window: 1048576, h3: none, h3_direct: false, h3_connect: time.Seconds(3), alt_svc: true,
    sealed: 0)

/// The settings of ServeWith, ServeOn and ServeChan: the connections served
/// at once, the bytes of a request's head and body, the time a request may
/// take, and the time a kept-alive connection may wait for the next
/// request. Start from DefaultServerOptions and assign the fields to
/// change; a literal of it does not compile outside std.
export struct ServerOptions {
    /// The connections served at once; more wait in the listen queue.
    export connections: int
    /// The bytes of a request's head; a longer head is answered 431. A
    /// head over 8 KiB is read into a buffer that grows with its bytes up
    /// to this.
    export header_bytes: int
    /// The bytes of a request's body, chunked framing included; a longer
    /// body is answered 413. A chunked body is read whole under this limit.
    export body_bytes: int
    /// The time from a request's first byte to its head read whole. It also
    /// bounds each read of the body (a body that sends no byte for this
    /// long closes the connection) and each write of the server's own (a
    /// reply, or 100 Continue) waiting for the client to take a piece.
    export request_header: time.Duration
    /// The time from a reply sent to the next request's first byte.
    export keep_alive: time.Duration
    /// The acceptor each connection goes through, made by tls.Acceptor.
    /// With none, the server speaks plain TCP.
    export secure: ?net.Secure
    /// With secure, accept h2 by ALPN.
    export h2: bool
    /// Without secure, serve a connection starting with the HTTP/2 preface as HTTP/2.
    export h2c: bool
    /// SETTINGS_MAX_CONCURRENT_STREAMS for h2 and active handlers per h3 connection.
    export h2_streams: int
    /// Made by quic.Acceptor; ServeWith and ServeOn also bind UDP when set.
    export h3: ?net.MuxServer
    /// With h3, add Alt-Svc header to h1 and h2 replies that set none.
    export alt_svc: bool
    h3_port: int
    sealed: u8
}

/// The server's default settings: 10,000 connections, a 32 KiB head, an
/// 8 MiB body, 30 s for a request's head and 75 s between requests, and
/// plain TCP.
export const DefaultServerOptions: ServerOptions = ServerOptions(connections: 10000, header_bytes: 32768, body_bytes: 8388608,
    request_header: time.Seconds(30), keep_alive: time.Seconds(75), secure: none, h2: false, h2c: false,
    h2_streams: 100, h3: none, alt_svc: true, h3_port: 0, sealed: 0)
