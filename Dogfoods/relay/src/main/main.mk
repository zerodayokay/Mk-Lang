// Why: a relay example should run without any external network service.
// What: Main and the bounds on a load run.
// How: choose the wire version of each hop, then run a bounded workload
// through two listeners on ephemeral loopback ports.

import std.http
import std.sim

const MaxStreams = 16
const MaxBody = 65536
const MaxHead = 8192
const Window = 8192
const CopyBlock = 4096

enum Mode {
    Load
    Slow
    Restart
    SimLoss
}

struct Work {
    mode: Mode
    front: http.HttpVersion
    back: http.HttpVersion
    streams: int
    bytes: int
    cancel: int
    tls_dir: str
}

/// Runs a loopback load session and reports its result. Prints errors and
/// returns a failure status. O(streams * bytes); allocates bounded arenas
/// for the requests, replies and TLS fixture material.
export Main(sys: &System): int {
    args := sys.args[1..]
    if args.Len() > 0 && (args[0] == "serve" || args[0] == "origin") {
        o := ParseEndpoint(args) else { return Usage(sys) }
        ServeEndpoint(sys, &o) else e {
            sys.PrintErr("relay: {e}\n", (e)) else {}
            return 1
        }
        return 0
    }
    w := Parse(args) else { return Usage(sys) }
    Launch(sys, &w) else e {
        sys.PrintErr("relay: {e}\n", (e)) else {}
        return 1
    }
    return 0
}

Usage(sys: &System): int {
    sys.PrintErr("usage: relay load|slow|goaway|sim-loss FRONT BACK STREAMS BODY_BYTES CANCEL_PERCENT [TLS_DIR]\n") else {}
    sys.PrintErr("       relay serve FRONT BACK LISTEN_PORT ORIGIN_PORT [TLS_DIR]\n") else {}
    sys.PrintErr("       relay origin PROTOCOL LISTEN_PORT [TLS_DIR]\n") else {}
    return 2
}

Launch(sys: &System, w: &Work): RunFailed|OutOfMemory|WriteError!void {
    if w.mode == Mode.SimLoss {
        var network := try sim.Sim.New(sys.arena, 17)
        defer network.Free()
        var faults := sim.DefaultNetFaults
        faults.drop = 100_000
        network.SetNetFaults(faults)
        try Run(sys, network.Io(), w)
        return
    }
    try Run(sys, sys.io, w)
}

Parse(args: []str): ?Work from args {
    if args.Len() < 6 || args.Len() > 7 {
        return none
    }
    mode := match args[0] {
        "load" { Mode.Load }
        "slow" { Mode.Slow }
        "goaway" { Mode.Restart }
        "sim-loss" { Mode.SimLoss }
        _ { return none }
    }
    front := Wire(args[1]) else { return none }
    back := Wire(args[2]) else { return none }
    streams := Pos(args[3], MaxStreams) else { return none }
    bytes := Pos(args[4], MaxBody) else { return none }
    percent := Pos(args[5], 100) else { return none }
    cancel := streams * percent / 100
    tls_dir := if args.Len() == 7 { args[6] } else { "" }
    if streams == 0 || (cancel > 0 && (front != http.HttpVersion.Http2 || back != http.HttpVersion.Http2)) {
        return none
    }
    if (front == http.HttpVersion.Http3 || back == http.HttpVersion.Http3) && tls_dir == "" {
        return none
    }
    if mode == Mode.Restart && (streams < 2 || back != http.HttpVersion.Http2 || cancel != 0) {
        return none
    }
    if mode == Mode.SimLoss && front != http.HttpVersion.Http3 && back != http.HttpVersion.Http3 {
        return none
    }
    return some(Work(mode: mode, front: front, back: back, streams: streams, bytes: bytes, cancel: cancel, tls_dir: tls_dir))
}

Pos(s: str, max: int): ?int {
    n := s.ParseInt() else { return none }
    if n < 0 || n > i64(max) {
        return none
    }
    return some(Cast[int](n))
}

Wire(s: str): ?http.HttpVersion {
    return match s {
        "h1" { some(http.HttpVersion.Http11) }
        "h2" { some(http.HttpVersion.Http2) }
        "h3" { some(http.HttpVersion.Http3) }
        _ { none }
    }
}

WireName(v: http.HttpVersion): str {
    return match v {
        http.HttpVersion.Http11 { "h1" }
        http.HttpVersion.Http2 { "h2" }
        http.HttpVersion.Http3 { "h3" }
        _ { "unknown" }
    }
}
