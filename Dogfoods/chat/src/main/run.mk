// Why: Serve, replay and load all start the same server and differ only in what
// drives it.
//
// What: Run, Drill, and the helpers that open the history log, wait for the
// bots, run a replay or a load, and give up after a time limit.
//
// How: Run builds the hub, starts the accept loop, the bots and the history
// writer as tasks in one scope, runs the mode's driver, then cancels the scope
// and reports the traffic and how each connection ended.

import std.atomic
import std.fs
import std.http
import std.log
import std.net
import std.time
import std.tls
import std.x509
import src.client
import src.drill
import src.hub
import src.server
import src.web

/// Starts the server and runs `mode` against it. Returns the exit status:
/// 0, 1 when a file, the listener or memory failed, 2 for a usage error.
Run(sys: &System, o: &Options, mode: Mode): WriteError!int {
    cfg := try Settings(sys, o) else {
        return 2
    }
    deadline := try Bounded(sys, o.deadline, 20, 0, 3600, "deadline") else {
        return 2
    }
    level := try Level(sys, o) else {
        return 2
    }
    if mode == Mode.Load && !o.slow {
        clients := o.clients else { 100 }
        rooms := o.rooms else { 10 }
        if clients < 1 || clients + cfg.bots > cfg.max_clients {
            try sys.PrintErr("chat: --clients must be from 1 to --max-clients less the bots ({n})\n", (n: cfg.max_clients - cfg.bots))
            return 2
        }
        if rooms < 1 || rooms > hub.MaxRooms {
            try sys.PrintErr("chat: --rooms must be from 1 to {m}\n", (m: hub.MaxRooms))
            return 2
        }
    }
    var script := ""
    if mode == Mode.Replay {
        if o.args.Len() != 2 {
            try sys.PrintErr("chat: replay needs one SCRIPT file\n")
            return 2
        }
        script = fs.ReadText(sys.arena, sys.io, o.args[1]) else e {
            try sys.PrintErr("chat: {p}: {e}\n", (p: o.args[1], e))
            return 1
        }
    }
    var motd := "Welcome to chat."
    if path := o.motd {
        text := fs.ReadText(sys.arena, sys.io, path) else e {
            try sys.PrintErr("chat: {path}: {e}\n", (path, e))
            return 1
        }
        motd = text.Lines().Next() else { "" }
    }
    var hfile := OpenLog(sys.io, o.history) else e {
        try sys.PrintErr("chat: {p}: {e}\n", (p: o.history else { "" }, e))
        return 1
    }
    defer CloseLog(sys, hfile)
    default_addr := if mode == Mode.Serve { "127.0.0.1:7070" } else { "127.0.0.1:0" }
    addr := o.addr else { default_addr }
    l := net.Listen(sys.io, addr) else e {
        try sys.PrintErr("chat: listen {addr}: {e}\n", (addr, e))
        return 1
    }
    defer l.Close()
    port := l.Port()
    local := sys.arena.Fmt("127.0.0.1:{port}", (port)) else {
        try sys.PrintErr("chat: out of memory\n")
        return 1
    }
    var waddr := ""
    if wanted := o.web {
        waddr = WebAddress(sys.arena, sys.io, wanted) else e {
            try sys.PrintErr("chat: web {wanted}: {e}\n", (wanted, e))
            return 1
        }
    }
    if o.web_tls_chain.IsSome() != o.web_tls_key.IsSome() || (waddr.Len() == 0 && (o.web_tls_chain.IsSome() || o.web_cacert.IsSome())) {
        try sys.PrintErr("chat: HTTPS needs --web, --web-tls-chain and --web-tls-key\n")
        return 2
    }
    if o.web_cacert.IsSome() && o.web_tls_chain.IsNone() {
        try sys.PrintErr("chat: --web-cacert needs an HTTPS web front end\n")
        return 2
    }
    var web_server := http.DefaultServerOptions
    web_server.connections = cfg.max_clients
    var web_tls := tls.DefaultConfig
    if chain := o.web_tls_chain {
        web_tls.identities = WebIdentity(sys.arena, sys.io, chain, o.web_tls_key else { "" }) else e {
            try sys.PrintErr("chat: cannot load web TLS identity: {e}\n", (e))
            return 1
        }
        web_server.secure = some(tls.Acceptor(&web_tls))
        web_server.h2 = true
    }
    var replay_tls := tls.DefaultConfig
    no_certs: [0]x509.Certificate = []
    var roots := x509.Pool.New(no_certs[..])
    if path := o.web_cacert {
        pem := fs.ReadText(sys.arena, sys.io, path) else e {
            try sys.PrintErr("chat: cannot read web roots: {e}\n", (e))
            return 1
        }
        certs := x509.ParsePem(sys.arena, pem) else e {
            try sys.PrintErr("chat: cannot read web roots: {e}\n", (e))
            return 1
        }
        roots = x509.Pool.New(certs.Done()[..])
        replay_tls.roots = some(&roots)
    }
    web_secure := web_server.secure.IsSome()
    replay_web := client.WebTarget(addr: waddr, secure: if web_secure { some(tls.Connector(&replay_tls)) } else { none })
    hb := hub.Hub.New(sys.heap, sys.io, cfg, motd, log.Stderr(level)) else {
        try sys.PrintErr("chat: out of memory for {n} clients\n", (n: cfg.max_clients))
        return 1
    }
    // the accept loop holds one admission permit while it waits, so the web
    // front end serves one client fewer than --max-clients at once
    bridges := if waddr.Len() > 0 && cfg.max_clients > 1 { cfg.max_clients - 1 } else { 1 }
    front := web.Front.New(sys.heap, &hb, bridges) else {
        _ = hb.Release(sys.io)
        try sys.PrintErr("chat: out of memory for {n} web clients\n", (n: bridges))
        return 1
    }
    var web_failed: ?http.Error = none
    ends := server.Endings.New()
    tally := client.Tally.New()
    var failed: ?hub.LogError = none
    var written := 0
    var status := 0
    late := atomic.AtomicBool.New(false)
    var transcript := HeapText.New(sys.heap.Alloc())
    defer transcript.Free(sys.heap.Alloc())
    var bad: ?client.ScriptError = none
    io := sys.io
    // A replay may close both a pooled HTTP/2 connection and its TLS state
    // on its task stack. Reserve a larger stack in HTTPS replay mode.
    stack := if mode == Mode.Replay && web_secure { StackSize.Large } else { StackSize.Default }
    scope root := io.ScopeWith(Limits(tasks: 16, stack: stack)) {
        root.go server.Stopper(&root, io, &hb)
        root.go server.Serve(&l, io, &hb, &ends)
        root.go hub.HistoryWriter(io, &hb.history, mut hfile, mut failed, mut written)
        root.go server.Refill(io, &hb)
        if waddr.Len() > 0 {
            root.go web.Admit(io, &front, &hb, &ends)
        }
        if mode != Mode.Serve && deadline > 0 {
            root.go GiveUp(&root, io, deadline, &late)
        }
        for i in 0..cfg.bots {
            root.go client.Bot(io, local, &hb, i)
        }
        _ = hb.bots_ready.Wait(io) else {}
        match mode {
            .Serve {
                root.go server.OnSignal(io, &hb)
                sys.PrintErr("chat: listening on 127.0.0.1:{port}\n", (port)) else {}
                if waddr.Len() > 0 {
                    scheme := if web_secure { "https" } else { "http" }
                    sys.PrintErr("chat: web front end on {scheme}://{waddr}/rooms\n", (scheme, waddr)) else {}
                }
            }
            .Replay {
                root.go Replay(sys.heap, io, &hb, local, &replay_web, script, mut transcript, mut bad)
            }
            .Load {
                spec := client.LoadSpec(clients: o.clients else { 100 }, rooms: o.rooms else { 10 }, messages: o.messages else { 5 }, shutdown: o.shutdown)
                root.go Load(io, local, &hb, spec, o.slow, &tally)
            }
        }
        // a workaround: http.ServeWith takes the System, which no task may share, so the
        // web front end is served here and the work above runs in tasks
        if waddr.Len() > 0 {
            web.Serve(sys, waddr, &web_server, &front, mut web_failed)
        }
    }
    front.Free()
    r := hb.Release(io)
    try sys.Print("{t}", (t: transcript.Str()))
    if e := bad {
        try sys.PrintErr("chat: {p}: {e}\n", (p: o.args[1], e))
        status = 2
    }
    // the web front end logged why it stopped
    if web_failed.IsSome() {
        status = 1
    }
    if late.Load() {
        try sys.PrintErr("chat: gave up after {deadline} s\n", (deadline))
        status = 1
    }
    if e := failed {
        try sys.PrintErr("chat: history file: {e}\n", (e))
        status = 1
    }
    if mode == Mode.Load && o.slow {
        try sys.Print("slow consumer: flooder saw it leave={n} it read to the end={c}\n", (n: tally.notices.Load(), c: tally.closed.Load()))
    } else if mode == Mode.Load {
        try sys.Print("clients joined={j} failed={f}\n", (j: tally.joined.Load(), f: tally.failed.Load()))
        try sys.Print("messages sent={s} delivered={d} refused={r}\n", (s: tally.sent.Load(), d: tally.delivered.Load(), r: tally.refused.Load()))
        try sys.Print("shutdown notices={n} closed={c}\n", (n: tally.notices.Load(), c: tally.closed.Load()))
        try ReportFailure(sys, &tally)
    }
    if mode == Mode.Serve {
        try sys.PrintErr("chat: wrote {f} frames, {b} bytes; {u} never delivered\n", (f: r.traffic.frames, b: r.traffic.bytes, u: r.undelivered))
    }
    if mode == Mode.Replay {
        // a replay runs one command at a time, so the history is the same in
        // every run; what the bots were sent as the server stopped is not
        try sys.Print("-- server: history={h}\n", (h: written))
    }
    try sys.Print("-- endings: quit={q} eof={e} dropped={d} kicked={k} closing={c} broken={b} canceled={x}\n", (
        q: ends.quit.Load(),
        e: ends.eof.Load(),
        d: ends.dropped.Load(),
        k: ends.kicked.Load(),
        c: ends.closing.Load(),
        b: ends.broken.Load(),
        x: ends.canceled.Load(),
    ))
    if cfg.idle_ms > 0 {
        try sys.Print("-- idle: {i}\n", (i: ends.idle.Load()))
    }
    return status
}

/// Says on stderr which load client failed first, where, and why.
ReportFailure(sys: &System, t: &client.Tally): WriteError!void {
    var g := t.first.Lock(sys.io.NoCancel()) else {
        return
    }
    f := *g.Ref()
    g.Unlock()
    first := f else {
        return
    }
    stage := match first.stage {
        .Dial { "connect" }
        .Memory { "memory" }
        .Join { "join" }
    }
    if e := first.err {
        try sys.PrintErr("chat: client {first.client} failed at {stage}: {e}\n", (first, stage, e))
        return
    }
    try sys.PrintErr("chat: client {first.client} failed at {stage}\n", (first, stage))
}

/// Cancels `root` once `secs` seconds have passed, so a run that hangs
/// still reports what it saw; `late` says it fired.
GiveUp(root: &Scope, io: Io, secs: int, late: &atomic.AtomicBool) {
    io.Sleep(time.Seconds(i64(secs))) else {
        return
    }
    late.Store(true)
    root.Cancel()
}

/// The history file at `path`, created empty, or `none` without a path.
OpenLog(io: Io, path: ?str): fs.Error!?fs.File {
    p := path else {
        return none
    }
    return some(try fs.Create(io, p))
}

/// Closes the history file, reporting a failed close.
CloseLog(sys: &System, log: ?fs.File) {
    f := log else {
        return
    }
    f.Close(sys.io) else e {
        f.Abandon()
        sys.PrintErr("chat: history file: {e}\n", (e)) else {}
    }
}

/// Runs the cancellation drill; its exit status says whether every wait
/// was cancelled.
Drill(sys: &System): WriteError!int {
    var w := sys.Stdout()
    return drill.Run(sys.heap, sys.io, mut w) else e {
        try sys.PrintErr("chat: drill: {e}\n", (e))
        1
    }
}

// Waits up to five seconds until only the bots' connections are left.
WaitForBots(io: Io, hb: &hub.Hub, bots: int) {
    for _ in 0..500 {
        if hb.stats.online.Load(atomic.Acquire) <= i64(bots) {
            return
        }
        io.Sleep(time.Millis(10)) else {
            return
        }
    }
}

/// The `replay` command's work, as a task beside the servers: runs the
/// script, keeps its transcript in `out` and a script error in `bad`, then
/// begins shutdown.
Replay(h: &Heap, io: Io, hb: &hub.Hub, local: str, web: &client.WebTarget, script: str, out: mut HeapText, bad: mut ?client.ScriptError) {
    var arena := Arena.New(h) else {
        *bad = some(OutOfMemory)
        hb.stop.NotifyOne()
        return
    }
    var text := Text.New(&arena)
    var w := text.Writer()
    client.Replay(h, io, local, web, script, mut w) else e {
        *bad = some(e)
        // The script's connections are closed: let their sessions see the
        // end of the stream before the shutdown notice can reach them, so
        // they end as eof on every target.
        WaitForBots(io, hb, hb.cfg.bots)
    }
    out.Push(h.Alloc(), text.Str()) else {
        *bad = some(OutOfMemory)
    }
    arena.Free()
    hb.stop.NotifyOne()
}

/// The `load` command's work, as a task beside the servers: the load, or
/// the slow-consumer run, then shutdown unless the load shut the server
/// down itself.
Load(io: Io, local: str, hb: &hub.Hub, spec: client.LoadSpec, slow: bool, tally: &client.Tally) {
    if slow {
        client.LoadSlow(io, local, hb, 100_000, tally)
        hb.stop.NotifyOne()
        return
    }
    client.Load(io, local, hb, spec, tally)
    if !spec.shutdown {
        hb.stop.NotifyOne()
    }
}
