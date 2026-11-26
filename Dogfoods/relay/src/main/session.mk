// Why: the two sides of a relay may use different HTTP versions.
// What: the listeners, clients and driver tasks for one offline run.
// How: bind before starting tasks and stop the drivers before closing
// either client. For HTTP/3, use the fixture TLS identity and roots.

import std.atomic
import std.crypto
import std.encoding
import std.fs
import std.http
import std.net
import std.quic
import std.time
import std.tls
import std.x509

error RunFailed
error BadFixture

Run(sys: &System, io: Io, w: &Work): RunFailed|WriteError!void {
    secure := w.front == http.HttpVersion.Http3 || w.back == http.HttpVersion.Http3
    certs := FixtureCerts(sys.arena, sys.io, w.tls_dir, "root.pem", secure) else { fail RunFailed }
    roots := x509.Pool.New(certs)
    var server := tls.DefaultConfig
    var client := tls.DefaultConfig
    if secure {
        server.identities = FixtureIdentity(sys.arena, sys.io, w.tls_dir) else { fail RunFailed }
        client.roots = some(&roots)
    }
    var server_quic := quic.DefaultConfig
    server_quic.tls = some(&server)
    var client_quic := quic.DefaultConfig
    client_quic.tls = some(&client)

    ingress := net.Listen(io, "127.0.0.1:0") else { fail RunFailed }
    defer ingress.Close()
    backend := net.Listen(io, "127.0.0.1:0") else { fail RunFailed }
    defer backend.Close()
    front_opts := ServeSettings(w.front, &server, &server_quic)
    back_opts := ServeSettings(w.back, &server, &server_quic)
    downstream := http.Client.New(sys.heap, ClientSettings(w.front, &client_quic))
    defer downstream.Close()
    upstream := http.Client.New(sys.heap, ClientSettings(w.back, &client_quic))
    defer upstream.Close()
    back_base := Base(sys.arena, w.back, backend.Port()) else { fail RunFailed }
    front_base := Base(sys.arena, w.front, ingress.Port()) else { fail RunFailed }
    payload := sys.arena.Slice[u8](w.bytes) else { fail RunFailed }
    for i in 0..payload.Len() {
        payload[i] = PayloadByte(i)
    }
    origin := Origin(
        seen: atomic.AtomicInt.New(0),
        canceled: atomic.AtomicInt.New(0),
        bytes: atomic.AtomicInt.New(0),
        bad: atomic.AtomicInt.New(0),
        round: atomic.AtomicInt.New(0),
    )
    relay := NewRelay(&upstream, back_base, w.front, w.back, w.cancel > 0)
    result := Results(
        ok: atomic.AtomicInt.New(0),
        canceled: atomic.AtomicInt.New(0),
        bytes: atomic.AtomicInt.New(0),
    )
    var ready := false
    scope run := io.Scope() {
        run.CancelAfter(time.Seconds(20))
        run.go Serve(sys.heap, io, &ingress, &front_opts, &relay, Proxy)
        scope drive := io.Scope() {
            drive.go Drive(io, &downstream)
            drive.go Drive(io, &upstream)
            ready = (downstream.WaitDriving(io) else { false }) && (upstream.WaitDriving(io) else { false })
            if ready {
                if w.mode == Mode.Restart {
                    scope first := io.Scope() {
                        origin.round.Store(1)
                        first.go Serve(sys.heap, io, &backend, &back_opts, &origin, Backend)
                        Transfer(sys.heap, io, &downstream, front_base, w, payload[..], false, "1", &result)
                        first.Cancel()
                    }
                    origin.round.Store(2)
                }
                scope last := io.Scope() {
                    last.go Serve(sys.heap, io, &backend, &back_opts, &origin, Backend)
                    Exercise(sys.heap, io, &downstream, front_base, w, payload[..], &result)
                    ready = Settled(io, &relay, &origin, w.cancel)
                    last.Cancel()
                }
            }
            downstream.Stop()
            upstream.Stop()
        }
        run.Cancel()
    }
    try Report(sys, w, &relay, &origin, &result, ready)
}

const NoCerts: []x509.Certificate = []

Serve[C: Share](h: &Heap, io: Io, listener: &net.Listener, o: &http.ServerOptions, c: &C, handle: fn(&C, &Arena, Io, &http.Request, mut http.Response, &http.Link): http.Error!void) {
    http.ServeOn(h, io, listener, o, c, handle) else {}
}

Drive(io: Io, c: &http.Client) {
    c.Drive(io) else {}
}

ServeSettings(v: http.HttpVersion, tls_cfg: &tls.Config, quic_cfg: &quic.Config): http.ServerOptions from tls_cfg, quic_cfg {
    var o := http.DefaultServerOptions
    o.connections = MaxStreams
    o.header_bytes = MaxHead
    o.body_bytes = MaxBody
    o.h2_streams = MaxStreams
    o.h2c = v == http.HttpVersion.Http2
    o.alt_svc = false
    if v == http.HttpVersion.Http3 {
        o.secure = some(tls.Acceptor(tls_cfg))
        o.h3 = some(quic.Acceptor(quic_cfg))
    }
    return o
}

ClientSettings(v: http.HttpVersion, q: &quic.Config): http.ClientLimits from q {
    var o := http.DefaultClientLimits
    o.redirects = 0
    o.header_bytes = MaxHead
    o.body_bytes = i64(MaxBody)
    o.h2_streams = MaxStreams
    o.h2_window = Window
    o.h2c = v == http.HttpVersion.Http2
    o.alt_svc = false
    if v == http.HttpVersion.Http3 {
        o.h3 = some(quic.Connector(q))
        o.h3_direct = true
    }
    return o
}

Base(a: &Arena, v: http.HttpVersion, port: int): OutOfMemory!str from a {
    scheme := if v == http.HttpVersion.Http3 { "https" } else { "http" }
    return try a.Fmt("{scheme}://127.0.0.1:{port}", (scheme, port))
}

FixtureCerts(a: &Arena, io: Io, dir: str, file: str, needed: bool): OutOfMemory|fs.Error|x509.Error|BadFixture![]x509.Certificate from a {
    if !needed {
        return NoCerts
    }
    path := try a.Fmt("{dir}/{file}", (dir, file))
    pem := try fs.ReadText(a, io, path)
    certs := try x509.ParsePem(a, pem)
    if certs.Len() == 0 {
        fail BadFixture
    }
    return certs.Done()[..]
}

FixtureIdentity(a: &Arena into a, io: Io, dir: str): OutOfMemory|fs.Error|x509.Error|encoding.Error|crypto.Error|BadFixture![]tls.Identity from a {
    chain := try FixtureCerts(a, io, dir, "server.pem", true)
    path := try a.Fmt("{dir}/server.key", (dir))
    pem := try fs.ReadText(a, io, path)
    for block in (try encoding.ParsePem(a, pem)) {
        if block.label == "PRIVATE KEY" || block.label == "EC PRIVATE KEY" {
            key := try a.Put(try crypto.ParsePrivateKey(block.der))
            var identities := List[tls.Identity].New(a)
            try identities.Push(tls.Identity(chain: chain, key: &*key))
            return identities.Done()[..]
        }
    }
    fail BadFixture
}
