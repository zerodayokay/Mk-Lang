// Why: standalone mode must use exactly the origin it was configured with.
// What: a real request across independently configured loopback listeners.
// How: send to an HTTP/2 relay with a standalone HTTP/1 echo origin and
// check the application result as well as both observed versions.

import std.http
import std.net
import std.quic
import std.time
import std.tls
import std.url

test "standalone echo forwards binary bytes across fixed endpoints" (sys: &System) {
    io := sys.io
    a := sys.arena
    relay_listener := try net.Listen(io, "127.0.0.1:0")
    defer relay_listener.Close()
    echo_listener := try net.Listen(io, "127.0.0.1:0")
    defer echo_listener.Close()
    front := http.HttpVersion.Http2
    back := http.HttpVersion.Http11
    config := tls.DefaultConfig
    q := quic.DefaultConfig
    relay_opts := ServeSettings(front, &config, &q)
    echo_opts := ServeSettings(back, &config, &q)
    front_base := try Base(a, front, relay_listener.Port())
    back_base := try Base(a, back, echo_listener.Port())
    upstream := http.Client.New(sys.heap, ClientSettings(back, &q))
    defer upstream.Close()
    client := http.Client.New(sys.heap, ClientSettings(front, &q))
    defer client.Close()
    relay := NewRelay(&upstream, back_base, front, back, false)
    scope network := io.Scope() {
        defer network.Cancel()
        network.CancelAfter(time.Seconds(10))
        network.go Serve(sys.heap, io, &echo_listener, &echo_opts, &OriginApp, Echo)
        network.go Serve(sys.heap, io, &relay_listener, &relay_opts, &relay, Proxy)
        response := try EchoRequest(a, io, &client, front_base, "/echo")
        Assert(response.status == 200)
        Assert(response.body.Len() == 3)
        Assert(response.body[0] == 1 && response.body[1] == 128 && response.body[2] == 255)
        Assert(HasField(response.headers, "x-relay-ingress", "h2"))
        Assert(HasField(response.headers, "x-origin-version", "h1"))
        missing := try EchoRequest(a, io, &client, front_base, "/other")
        Assert(missing.status == 404)
        Assert(missing.body.Len() == 0)
    }
}

EchoRequest(a: &Arena, io: Io, client: &http.Client, base: str, path: str): http.Error|url.Error!http.Reply from a {
    text := try a.Fmt("{base}{path}", (base, path))
    dest := try url.Parse(a, text, none)
    body: [3]u8 = [1, 128, 255]
    headers: [0]http.Header = []
    request := http.ClientRequest(method: "POST", url: &dest, headers: headers[..], body: body[..])
    return try client.Fetch(a, io, &request)
}

test "standalone endpoint settings reject conflicting ports and missing H3 TLS" {
    valid: [5]str = ["serve", "h2", "h1", "0", "8091"]
    relay := ParseEndpoint(valid[..]).Must()
    Assert(relay.port == 0 && relay.origin_port == 8091)
    Assert(relay.front == http.HttpVersion.Http2)
    Assert(relay.back.Must() == http.HttpVersion.Http11)

    conflicting: [5]str = ["serve", "h1", "h2", "8091", "8091"]
    Assert(ParseEndpoint(conflicting[..]).IsNone())

    no_roots: [5]str = ["serve", "h2", "h3", "8090", "8091"]
    Assert(ParseEndpoint(no_roots[..]).IsNone())

    no_identity: [3]str = ["origin", "h3", "8091"]
    Assert(ParseEndpoint(no_identity[..]).IsNone())

    origin_args: [4]str = ["origin", "h3", "8091", "fixtures"]
    origin := ParseEndpoint(origin_args[..]).Must()
    Assert(origin.front == http.HttpVersion.Http3 && origin.back.IsNone())
    Assert(origin.tls_dir == "fixtures")
}
