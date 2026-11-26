// Why: each hop may name its own Connection fields independently.
// What: a forwarding policy check using ordinary HTTP headers.
// How: give the relay two Connection fields and check both nominated names.

import std.http

test "relay removes connection-specific fields and preserves an end-to-end field" (sys: &System) {
    headers: [3]http.Header = [
        http.Header(name: "Connection", value: "Keep-Alive, X-Local"),
        http.Header(name: "connection", value: "X-Trace-Local"),
        http.Header(name: "X-Trace", value: "ordinary end-to-end value"),
    ]
    a := sys.arena
    local := try ConnectionFields(a, headers[..])
    Assert(!(try ForwardField(a, "x-local", &local)))
    Assert(!(try ForwardField(a, "X-TRACE-LOCAL", &local)))
    Assert(!(try ForwardField(a, "Keep-Alive", &local)))
    Assert(!(try ForwardField(a, "Content-Length", &local)))
    Assert(try ForwardField(a, "X-Trace", &local))
}
