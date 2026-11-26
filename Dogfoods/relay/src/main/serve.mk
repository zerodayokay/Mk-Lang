// Why: the relay and its origin should also be usable as separate processes.
// What: standalone relay and origin modes, with signal-aware lifecycle.
// How: bind to loopback and give the relay a single configured upstream.

import std.http
import std.net
import std.proc
import std.quic
import std.tls
import std.x509

struct Endpoint {
    front: http.HttpVersion
    back: ?http.HttpVersion
    port: int
    origin_port: int
    tls_dir: str
}

error SamePort
error TLSFiles
error DriverStopped

ParseEndpoint(args: []str): ?Endpoint from args {
    if args.Len() < 3 {
        return none
    }
    front := Wire(args[1]) else { return none }
    if args[0] == "origin" && args.Len() <= 4 {
        port := Pos(args[2], 65535) else { return none }
        dir := if args.Len() == 4 { args[3] } else { "" }
        if front == http.HttpVersion.Http3 && dir == "" {
            return none
        }
        return some(Endpoint(front: front, back: none, port: port, origin_port: 0, tls_dir: dir))
    }
    if args[0] != "serve" || args.Len() < 5 || args.Len() > 6 {
        return none
    }
    back := Wire(args[2]) else { return none }
    port := Pos(args[3], 65535) else { return none }
    origin := Pos(args[4], 65535) else { return none }
    dir := if args.Len() == 6 { args[5] } else { "" }
    if origin == 0 || port == origin ||
        (front == http.HttpVersion.Http3 || back == http.HttpVersion.Http3) && dir == "" {
        return none
    }
    return some(Endpoint(front: front, back: some(back), port: port, origin_port: origin, tls_dir: dir))
}

ServeEndpoint(sys: &System, o: &Endpoint): http.Error|SamePort|TLSFiles|DriverStopped!void {
    io := sys.io
    network := try sys.arena.Fmt("127.0.0.1:{port}", (port: o.port))
    listener := try net.Listen(io, network)
    defer listener.Close()
    if listener.Port() == o.origin_port {
        fail SamePort
    }
    var server := tls.DefaultConfig
    if o.front == http.HttpVersion.Http3 {
        server.identities = FixtureIdentity(sys.arena, io, o.tls_dir) else { fail TLSFiles }
    }
    var q := quic.DefaultConfig
    q.tls = some(&server)
    opts := ServeSettings(o.front, &server, &q)
    scope serving := io.Scope() {
        defer serving.Cancel()
        serving.go StopOnSignal(io, &serving, proc.Signal.Interrupt)
        serving.go StopOnSignal(io, &serving, proc.Signal.Terminate)
        if back := o.back {
            try ServeProxy(sys, io, &listener, &opts, o, back)
        } else {
            try Announce(sys, o.front, listener.Port())
            try http.ServeOn(sys.heap, io, &listener, &opts, &OriginApp, Echo)
        }
    }
}

ServeProxy(sys: &System, io: Io, l: &net.Listener, opts: &http.ServerOptions, o: &Endpoint, back: http.HttpVersion): http.Error|TLSFiles|DriverStopped!void {
    secure := back == http.HttpVersion.Http3
    certs := FixtureCerts(sys.arena, io, o.tls_dir, "root.pem", secure) else { fail TLSFiles }
    roots := x509.Pool.New(certs)
    var cfg := tls.DefaultConfig
    cfg.roots = some(&roots)
    var q := quic.DefaultConfig
    q.tls = some(&cfg)
    client := http.Client.New(sys.heap, ClientSettings(back, &q))
    defer client.Close()
    origin := try Base(sys.arena, back, o.origin_port)
    relay := NewRelay(&client, origin, o.front, back, false)
    scope drivers := io.Scope() {
        defer client.Stop()
        drivers.go Drive(io, &client)
        if !(try client.WaitDriving(io)) {
            fail DriverStopped
        }
        try Announce(sys, o.front, l.Port())
        try http.ServeOn(sys.heap, io, l, opts, &relay, Proxy)
    }
}

StopOnSignal(io: Io, s: &Scope, sig: proc.Signal) {
    proc.WaitSignal(io, sig) else { return }
    s.Cancel()
}

Announce(sys: &System, v: http.HttpVersion, port: int): OutOfMemory|WriteError!void {
    address := try Base(sys.arena, v, port)
    try sys.Print("listening {address} ({version})\n", (address, version: WireName(v)))
}

const OriginApp = 0

Echo(c: &int, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    _ = c
    _ = a
    _ = io
    if req.path != "/echo" {
        fail http.Status(code: 404)
    }
    try res.Header("x-origin-version", WireName(link.Version()))
    try res.Header("content-type", "application/octet-stream")
    body := if req.method == "GET" { "relay origin\n".AsBytes() } else { req.body }
    try res.Body(body)
}
