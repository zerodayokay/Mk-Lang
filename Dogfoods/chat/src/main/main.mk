// Why: The entry point turns the command line into a mode and a server
// configuration.
//
// What: Options (parsed by `gen cli.Args`), Main, Help, Mode, and the checks
// Level, Bounded and Settings.
//
// How: Each option is checked against its range before anything starts; a bad
// value prints a message and exits with the usage code.

import std.cli
import std.log
import src.hub
import src.proto

/// chat: a multi-room chat server over TCP, with bots in the same process.
///
/// Commands: serve, replay SCRIPT, load, drill.
struct Options {
    /// Show this help
    export help: bool
    /// Address to listen on (serve: 127.0.0.1:7070; others: a free loopback port)
    export addr: ?str
    /// File whose first line is the message of the day
    export motd: ?str
    /// File to write every room message to
    export history: ?str
    /// Connections served at once (default 64, at most 1024)
    export max_clients: ?int
    /// Room messages queued per client before it is dropped as too slow (default 64)
    export queue: ?int
    /// Messages a client may send between refills, 0 for no limit (default 20)
    export burst: ?int
    /// Milliseconds between rate refills (default 1000)
    export refill_ms: ?int
    /// Bytes each connection may use for its own state (default 4096)
    export client_memory: ?int
    /// Operator token for OPER and SHUTDOWN (default: none)
    export token: ?str
    /// Bots to start, 0 to 2 (default 2)
    export bots: ?int
    /// load: clients to connect (default 100)
    export clients: ?int
    /// load: rooms to spread them over (default 10)
    export rooms: ?int
    /// load: messages each client sends (default 5)
    export messages: ?int
    /// load: end by shutting the server down under every client
    export shutdown: bool
    /// load: one client that never reads, and one that floods its room
    export slow: bool
    /// replay and load: seconds before giving up and reporting what was seen, 0 for never (default 20)
    export deadline: ?int
    /// Milliseconds a connection may sit with nothing read or sent before it is dropped, 0 for never (default 0)
    export idle_ms: ?int
    /// Log every byte each TCP connection reads and writes (implies --log info)
    export record: bool
    /// Log level on stderr: debug, info, warn or error (default warn)
    export log: ?str
    /// Also serve the web front end (WebSocket and event streams) on this address
    export web: ?str
    /// PEM certificate chain for the HTTPS web front end
    export web_tls_chain: ?str
    /// PEM private key for the HTTPS web front end
    export web_tls_key: ?str
    /// PEM roots to trust when replay connects to the HTTPS web front end
    export web_cacert: ?str
    /// The command and its file
    export args: []str
}

gen cli.Args(Options)

/// Parses the command line and runs the command; the exit status is 0, 1 on a
/// failed file, listener, memory or deadline, and 2 on a usage error.
export Main(sys: &System): WriteError!int {
    opts := Options.Parse(sys.arena, sys.args[1..]) else e {
        try sys.PrintErr("chat: {e}\n", (e))
        try Help(sys)
        return 2
    }
    if opts.help || opts.args.Len() == 0 {
        try Help(sys)
        return if opts.help { 0 } else { 2 }
    }
    return match opts.args[0] {
        "serve" { try Run(sys, &opts, Mode.Serve) }
        "replay" { try Run(sys, &opts, Mode.Replay) }
        "load" { try Run(sys, &opts, Mode.Load) }
        "drill" { try Drill(sys) }
        _ {
            try sys.PrintErr("chat: unknown command {c}\n", (c: opts.args[0]))
            2
        }
    }
}

Help(sys: &System): WriteError!void {
    var w := sys.Stdout()
    try w.Str("usage: chat [options] serve | replay SCRIPT | load | drill\n\n")
    try w.Str("serve    serve clients until Ctrl-C or SHUTDOWN\n")
    try w.Str("replay   run SCRIPT's clients against an in-process server and print the transcript\n")
    try w.Str("load     connect many clients at once and print what they saw\n")
    try w.Str("drill    cancel the server at each of its waits and report how each ended\n\n")
    try Options.Usage(mut w)
}

enum Mode {
    Serve
    Replay
    Load
}

/// The level `--log` names, at most `Info` with `--record`, or `none` after
/// reporting a bad name.
Level(sys: &System, o: &Options): WriteError!?log.Level {
    name := o.log else { "warn" }
    var level := match name {
        "debug" { log.Level.Debug }
        "info" { log.Level.Info }
        "warn" { log.Level.Warn }
        "error" { log.Level.Error }
        _ {
            try sys.PrintErr("chat: --log must be debug, info, warn or error, not {name}\n", (name))
            return none
        }
    }
    if o.record && (level == log.Level.Warn || level == log.Level.Error) {
        level = log.Level.Info
    }
    return some(level)
}

/// The option `v`, or `d` when it was not given, checked against `lo..hi`.
Bounded(sys: &System, v: ?int, d: int, lo: int, hi: int, flag: str): WriteError!?int {
    x := v else { d }
    if x < lo || x > hi {
        try sys.PrintErr("chat: --{flag} must be from {lo} to {hi}, not {x}\n", (flag, lo, hi, x))
        return none
    }
    return some(x)
}

/// The settings from the options, or `none` after reporting a bad one.
Settings(sys: &System, o: &Options): WriteError!?hub.Config {
    max := try Bounded(sys, o.max_clients, 64, 1, hub.MaxClients, "max-clients") else {
        return none
    }
    queue := try Bounded(sys, o.queue, 64, 1, 4096, "queue") else {
        return none
    }
    burst := try Bounded(sys, o.burst, 20, 0, 1_000_000, "burst") else {
        return none
    }
    refill := try Bounded(sys, o.refill_ms, 1000, 1, 3_600_000, "refill-ms") else {
        return none
    }
    memory := try Bounded(sys, o.client_memory, 4096, 64, 1 << 20, "client-memory") else {
        return none
    }
    bots := try Bounded(sys, o.bots, 2, 0, 2, "bots") else {
        return none
    }
    idle := try Bounded(sys, o.idle_ms, 0, 0, 3_600_000, "idle-ms") else {
        return none
    }
    token := o.token else { "" }
    if token.Len() > 0 && !proto.ValidNick(token) {
        try sys.PrintErr("chat: --token must be 1 to 24 letters, digits, - or _\n")
        return none
    }
    return some(hub.Config(max_clients: max, queue: queue, burst: burst, refill_ms: refill, client_memory: memory, token: proto.Name.Of(token), bots: bots, idle_ms: idle, record: o.record))
}
