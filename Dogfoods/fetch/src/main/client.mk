// Why: the transfer mode, fetch's main job: one or many URLs over one HTTP
// client.
// What: Client, Simulated, Session, Transfers, One, MakeJob and Shared.
// How: builds the TLS config and the http.Client with the limits the
// options ask for, starts the in-process origin when a script is given,
// then runs each URL as a transfer.Job, in order or all at once with
// --parallel; under --seed the session runs on std.sim with a fault armed.

import std.crypto
import std.encoding
import std.fs
import std.http
import std.mem
import std.net
import std.quic
import std.sim
import std.time
import std.tls
import std.url
import std.x509
import src.cookie
import src.origin
import src.transfer

const NoCerts: []x509.Certificate = []
const NoAddrs = origin.Addrs(origin: "", origin2: "", tls: "", tls2: "")
const ServerAlpn: []str = ["http/1.1"]

/// What every transfer of a run shares: the headers, the body, the query
/// pairs and the conditional date.
struct Common {
    headers: []http.Header
    body: []u8
    query: str
    since: ?str
}

// Client runs the transfers the command line asks for and returns the
// exit status.
Client(sys: &System, o: &Options): int *o into sys {
    if (o.origin_h2 || o.origin_h3) && o.origin.IsNone() {
        return Fatal(sys, 2, "--origin-h2 and --origin-h3 need --origin")
    }
    if o.origin_h3 && o.tls_chain.IsNone() {
        return Fatal(sys, 2, "--origin-h3 needs --tls-chain and --tls-key")
    }
    if o.h3_only && (o.follow || o.handshake || o.ws_kinds.Len() > 0) {
        return Fatal(sys, 2, "--http3-only cannot be combined with redirects, a TLS handshake check, or WebSocket")
    }
    if seed := o.seed {
        return Simulated(sys, o, seed)
    }
    return Session(sys, sys.io, o)
}

// Simulated runs the session under std.sim with seed, after arming each
// --fault for the next operation of its kind.
Simulated(sys: &System, o: &Options, seed: u64): int *o into sys {
    var sm := sim.Sim.New(sys.arena, seed) else {
        return Fatal(sys, 1, "out of memory")
    }
    for f in o.faults.Items() {
        match f {
            "connect-refused" { sm.FailNext(sim.Op.Connect, sim.Fault.Refused) }
            "read-reset" { sm.FailNext(sim.Op.Read, sim.Fault.Reset) }
            _ { sm.FailNext(sim.Op.Accept, sim.Fault.Denied) }
        }
    }
    code := Session(sys, sm.Io(), o)
    sm.Free()
    return code
}

// Session makes the run's one client and hands it to every transfer.
Session(sys: &System, io: Io, o: &Options): int *o into sys {
    a := sys.arena
    var jar := cookie.NewJar(a)
    if path := o.cookies {
        jar.Load(a, sys.io, path, time.Wall(sys.io)) else e {
            return Fatal(sys, 26, a.Fmt("cannot read the cookie file {path}: {e}", (path, e)) else { "cannot read the cookie file" })
        }
    }
    var roots := x509.Pool.New(NoCerts)
    var client_cfg := tls.DefaultConfig
    if path := o.cacert {
        certs := LoadCerts(a, sys.io, path) else e {
            return Fatal(sys, 77, a.Fmt("cannot use the roots in {path}: {e}", (path, e)) else { "bad --cacert" })
        }
        roots = x509.Pool.New(certs)
        client_cfg.roots = some(&roots)
    }
    client_cfg.alpn = o.alpn.Items()
    client_cfg.min_version = if o.tls12 { tls.Version.Tls12 } else { tls.Version.Tls13 }
    decoders := transfer.Decoders(a, o.decoders.Items()) else {
        return Fatal(sys, 1, "out of memory")
    }
    if o.deny.Len() > 0 {
        transfer.Deny(o.deny.Items())
    }
    // without -L the client follows none and hands back the 3xx reply
    redirects := if o.follow { o.max_redirs } else { 0 }
    var qcfg := quic.DefaultConfig
    qcfg.tls = some(&client_cfg)
    secure := if o.h3_only { none } else { some(tls.Connector(&client_cfg)) }
    var limits := transfer.Limits(redirects, o.fetch_limit, decoders, o.deny.Len() > 0, secure)
    limits.h2 = o.h2
    limits.h2c = o.h2c
    if window_bytes := o.h2_window {
        limits.h2_window = window_bytes
    }
    if o.h3 || o.h3_only || o.drive {
        limits.h3 = some(quic.Connector(&qcfg))
        limits.h3_direct = o.h3 || o.h3_only
    }
    c := http.Client.New(sys.heap, limits)
    defer c.Close()
    var result := 0
    scope background := io.Scope() {
        if limits.h3.IsSome() {
            background.go Drive(&c, io)
        }
        result = TransferSession(sys, io, o, &c, &client_cfg, mut jar)
        c.Stop()
    }
    return result
}

Drive(c: &http.Client, io: Io) {
    c.Drive(io) else {}
}

TransferSession(sys: &System, io: Io, o: &Options, c: &http.Client, cfg: &tls.Config, jar: mut cookie.Jar): int *sys into jar {
    if path := o.origin {
        return WithOrigin(sys, io, o, c, cfg, jar, path)
    }
    return Transfers(sys, io, o, c, cfg, jar, &NoAddrs)
}

// WithOrigin starts the scripted loopback origins of --origin in tasks,
// runs the transfers against them, and stops them.
WithOrigin(sys: &System, io: Io, o: &Options, c: &http.Client, cfg: &tls.Config, jar: mut cookie.Jar, path: str): int *sys into jar {
    a := sys.arena
    script := origin.Load(a, sys.io, path) else e {
        return Fatal(sys, 26, a.Fmt("cannot load the origin script {path}: {e}", (path, e)) else { "bad --origin" })
    }
    var server_cfg := tls.DefaultConfig
    secure := o.tls_chain.IsSome()
    if chain := o.tls_chain {
        key_path := o.tls_key else {
            return Fatal(sys, 2, "--tls-chain needs --tls-key")
        }
        ids := Identity(a, sys.io, chain, key_path) else e {
            return Fatal(sys, 26, a.Fmt("cannot load the TLS identity {chain}: {e}", (chain, e)) else { "bad --tls-chain" })
        }
        server_cfg.identities = ids
        server_cfg.alpn = ServerAlpn
    }
    l1 := net.Listen(io, "127.0.0.1:0") else e {
        return Fatal(sys, 7, a.Fmt("cannot listen on loopback: {e}", (e)) else { "cannot listen" })
    }
    defer l1.Close()
    l2 := net.Listen(io, "127.0.0.1:0") else e {
        return Fatal(sys, 7, a.Fmt("cannot listen on loopback: {e}", (e)) else { "cannot listen" })
    }
    defer l2.Close()
    l3 := net.Listen(io, "127.0.0.1:0") else e {
        return Fatal(sys, 7, a.Fmt("cannot listen on loopback: {e}", (e)) else { "cannot listen" })
    }
    defer l3.Close()
    l4 := net.Listen(io, "127.0.0.1:0") else e {
        return Fatal(sys, 7, a.Fmt("cannot listen on loopback: {e}", (e)) else { "cannot listen" })
    }
    defer l4.Close()
    addrs := origin.Addrs(origin: Loopback(a, l1.Port()), origin2: Loopback(a, l2.Port()), tls: Loopback(a, l3.Port()), tls2: Loopback(a, l4.Port()))
    context := origin.Context(script: &script, addrs: &addrs)
    var plain := http.DefaultServerOptions
    plain.h2c = o.origin_h2
    var tls_options := http.DefaultServerOptions
    tls_options.h2 = o.origin_h2
    var qcfg := quic.DefaultConfig
    qcfg.tls = some(&server_cfg)
    if secure {
        tls_options.secure = some(tls.Acceptor(&server_cfg))
    }
    if o.origin_h3 {
        tls_options.h3 = some(quic.Acceptor(&qcfg))
    }
    var code := 0
    scope srv := io.Scope() {
        if o.origin_h2 {
            srv.go origin.ServeHttp(&l1, sys.heap, io, &context, &plain)
            srv.go origin.ServeHttp(&l2, sys.heap, io, &context, &plain)
        } else {
            srv.go origin.Serve(&l1, sys.heap, io, &script, &addrs)
            srv.go origin.Serve(&l2, sys.heap, io, &script, &addrs)
        }
        if secure {
            if o.origin_h2 || o.origin_h3 {
                srv.go origin.ServeHttp(&l3, sys.heap, io, &context, &tls_options)
                srv.go origin.ServeHttp(&l4, sys.heap, io, &context, &tls_options)
            } else {
                srv.go origin.ServeTls(&l3, sys.heap, io, &script, &addrs, &server_cfg)
                srv.go origin.ServeAccepted(&l4, sys.heap, io, &script, &addrs, tls.Acceptor(&server_cfg))
            }
        }
        code = Transfers(sys, io, o, c, cfg, jar, &addrs)
        srv.Cancel()
    }
    return code
}

// Transfers runs one transfer per URL, one after another or all at once,
// prints their output in the order of the command line, and keeps the
// cookies they received. Its result is the first failure's status.
Transfers(sys: &System, io: Io, o: &Options, c: &http.Client, cfg: &tls.Config, jar: mut cookie.Jar, addrs: &origin.Addrs): int *sys into jar {
    a := sys.arena
    al := sys.heap.Alloc()
    common := Shared(a, sys.io, o) else e {
        text := match e {
            Unreadable(which, why) {
                _ = which
                path := o.time_cond else { "" }
                a.Fmt("cannot read --time-cond {path}: {why}", (path, why)) else { "cannot read --time-cond" }
            }
            OutOfMemory { "out of memory" }
        }
        return Fatal(sys, 26, text)
    }
    var code := 0
    if o.handshake {
        for link in o.urls.Items() {
            code = Checks(sys, io, o, cfg, link, addrs, code)
        }
        return code
    }
    if o.ws_kinds.Len() > 0 {
        for link in o.urls.Items() {
            done := Socket(sys, io, o, cfg, link, addrs)
            code = if code != 0 { code } else { done }
        }
        return code
    }
    if o.parallel && o.max_memory > 0 {
        return Fatal(sys, 2, "--max-memory cannot be used with --parallel")
    }
    if !o.parallel {
        // the budget each transfer's output is held in, when --max-memory sets one
        held := a.Slice[u8](o.max_memory) else {
            return Fatal(sys, 27, "out of memory")
        }
        // one at a time, so each transfer sends the cookies the ones before it received
        for link in o.urls.Items() {
            job := MakeJob(a, sys.io, o, &common, link, jar, addrs) else {
                return Fatal(sys, 1, "out of memory")
            }
            if o.max_memory == 0 {
                code = One(sys, io, c, &job, addrs, jar, al, code)
                continue
            }
            fixed := mem.Fixed.New(held)
            code = One(sys, io, c, &job, addrs, jar, fixed.Alloc(), code)
        }
    } else {
        var jobs := List[transfer.Job].New(a)
        for link in o.urls.Items() {
            job := MakeJob(a, sys.io, o, &common, link, jar, addrs) else {
                return Fatal(sys, 1, "out of memory")
            }
            jobs.Push(job) else {
                return Fatal(sys, 1, "out of memory")
            }
        }
        var outs := HeapList[transfer.Outcome].New(al)
        defer outs.Free(al)
        for _ in 0..jobs.Len() {
            var fresh := transfer.NewOutcome(al)
            outs.Push(al, fresh) else {
                fresh.Free(al)
                return Fatal(sys, 1, "out of memory")
            }
        }
        scope s := io.Scope() {
            var i := 0
            for slot in outs.ChunksMut(1) {
                s.go transfer.Run(c, sys.heap, io, &jobs[i], addrs, mut slot[0])
                i += 1
            }
        }
        for i in 0..outs.Len() {
            Absorb(a, sys.io, jar, &jobs[i], &outs[i])
            code = Show(sys, &outs[i], code)
        }
    }
    if path := o.jar {
        jar.Save(a, sys.io, path, time.Wall(sys.io)) else e {
            return Fatal(sys, 23, a.Fmt("cannot write the cookie jar {path}: {e}", (path, e)) else { "cannot write the cookie jar" })
        }
    }
    return code
}

// One runs one transfer with its output held on al, prints it and keeps
// its cookies; it returns code, or the transfer's status if code is 0.
One(sys: &System, io: Io, c: &http.Client, job: &transfer.Job, addrs: &origin.Addrs, jar: mut cookie.Jar, al: &Alloc, code: int): int *sys into jar, *job into jar, *job into sys {
    var res := transfer.NewOutcome(al)
    transfer.RunIn(c, sys.heap, al, io, job, addrs, mut res)
    Absorb(sys.arena, sys.io, jar, job, &res)
    done := Show(sys, &res, code)
    res.Free(al)
    return done
}

// MakeJob is the transfer of one URL argument: its placeholders expanded,
// io being the real one, whose wall clock decides which cookies are live,
// and the cookies the jar holds for it.
// Every text from o, common and link is copied into a, so the job holds
// loans of a alone and can be kept in a list.
MakeJob(a: &Arena into a, io: Io, o: &Options, common: &Common, link: str, jar: mut cookie.Jar, addrs: &origin.Addrs): OutOfMemory!transfer.Job from a {
    expanded := try origin.Expand(a, link, addrs)
    var cookies: ?str = none
    // a URL that does not parse gets no cookies; its transfer reports it
    match url.Parse(a, expanded, none) {
        ok(u) { cookies = try jar.Header(a, u.Hostname(), u.Path(), u.Scheme() == "https", time.Wall(io)) }
        err(_) {}
    }
    var output: ?str = none
    if path := o.output {
        output = some(try a.Dup(path))
    }
    var since: ?str = none
    if date := common.since {
        since = some(try a.Dup(date))
    }
    var headers := List[http.Header].New(a)
    for h in common.headers {
        try headers.Push(http.Header(name: try a.Dup(h.name), value: try a.Dup(h.value)))
    }
    body := try a.Slice[u8](common.body.Len())
    body.CopyFrom(common.body)
    return transfer.Job(
        method: try a.Dup(o.method),
        link: expanded,
        shown: try a.Dup(link),
        query: try a.Dup(common.query),
        headers: headers.Done()[..],
        body: body[..],
        data_file: if f := o.data_file { some(try a.Dup(f)) } else { none },
        chunked: o.chunked,
        output: output,
        resume: o.resume,
        include: o.include,
        head: o.head,
        versioned: o.h2 || o.h2c || o.h3 || o.h3_only || o.drive,
        h3_only: o.h3_only,
        fetch_limit: int(o.fetch_limit),
        fail_status: o.fail_status,
        verbose: o.verbose,
        silent: o.silent,
        deny: o.deny.Len() > 0,
        max_time: o.max_time,
        retry: o.retry,
        retry_delay: o.retry_delay,
        cookie: cookies,
        since: since,
    )
}

// Shared builds what every transfer sends: -H headers, Basic credentials
// from -u, the body of -d, the --query pairs and the
// --time-cond date.
// The texts of o are copied into a, as in MakeJob.
Shared(a: &Arena, io: Io, o: &Options): Unreadable|OutOfMemory!Common from a {
    var headers := List[http.Header].New(a)
    for h in o.headers.Items() {
        try headers.Push(http.Header(name: try a.Dup(h.name), value: try a.Dup(h.value)))
    }
    if user := o.user {
        token := try encoding.Base64Encode(a, user.AsBytes(), encoding.Base64)
        try headers.Push(http.Header(name: "Authorization", value: try a.Fmt("Basic {token}", (token))))
    }
    var body: []u8 = NoBody
    if d := o.data {
        body = (try a.Dup(d)).AsBytes()
    }
    var pairs := List[(name: str, value: str)].New(a)
    for q in o.queries.Items() {
        eq := q.Find("=") else { q.Len() }
        value := if eq < q.Len() { q[eq + 1..] } else { "" }
        try pairs.Push((name: try a.Dup(q[..eq]), value: try a.Dup(value)))
    }
    query := try url.FormatQuery(a, pairs[..])
    var since: ?str = none
    if cond := o.time_cond {
        since = some(Since(a, io, cond) else e {
            fail Unreadable(which: 1, why: e)
        })
    }
    return Common(headers: headers.Done()[..], body: body, query: query, since: since)
}

const NoBody: []u8 = []

// A file named by --time-cond (which 1) that cannot be read, and why.
error Unreadable(which: int, why: fs.Error | OutOfMemory)

// Since is the If-Modified-Since value of --time-cond: the argument when
// it is an HTTP date, else the modification time of the file it names.
Since(a: &Arena, io: Io, cond: str): fs.Error|OutOfMemory!str from a {
    match time.ParseHttpDate(cond) {
        ok(_) { return try a.Dup(cond) }
        err(_) {}
    }
    info := try fs.Stat(io, cond)
    return try time.FormatHttpDate(a, info.modified)
}

// Absorb puts the cookies a transfer received into the jar, as set for
// the URL it asked for.
Absorb(a: &Arena into jar, io: Io, jar: mut cookie.Jar, job: &transfer.Job, res: &transfer.Outcome) *job into a {
    if res.cookies.Len() == 0 {
        return
    }
    u := url.Parse(a, job.link, none) else {
        return
    }
    now := time.Wall(io)
    for line in res.cookies.Str().Lines() {
        jar.Receive(a, u.Hostname(), u.Path(), line, now) else {
            return
        }
    }
}

// Show prints a transfer's output and messages, and returns the run's
// status so far: the first failure's.
Show(sys: &System, res: &transfer.Outcome, code: int): int {
    out := res.out.Str()
    sys.Print("{out}", (out)) else {}
    err := res.err.Str()
    sys.PrintErr("{err}", (err)) else {}
    return if code != 0 { code } else { res.code }
}

// LoadCerts reads every certificate of a PEM file.
LoadCerts(a: &Arena, io: Io, path: str): fs.Error|x509.Error|Empty![]x509.Certificate from a {
    text := try fs.ReadText(a, io, path)
    certs := try x509.ParsePem(a, text)
    if certs.Len() == 0 {
        fail Empty
    }
    return certs.Done()[..]
}

error Empty
error NoKey

// Identity is the one TLS identity of --tls-chain and --tls-key: the chain
// in order, leaf first, and the first PRIVATE KEY block of the key file.
Identity(a: &Arena into a, io: Io, chain: str, key: str): fs.Error|x509.Error|encoding.Error|crypto.Error|Empty|NoKey![]tls.Identity from a {
    certs := try LoadCerts(a, io, chain)
    text := try fs.ReadText(a, io, key)
    blocks := try encoding.ParsePem(a, text)
    for b in blocks {
        if b.label != "PRIVATE KEY" && b.label != "EC PRIVATE KEY" {
            continue
        }
        k := try crypto.ParsePrivateKey(b.der)
        held := try a.Put(k)
        var ids := List[tls.Identity].New(a)
        try ids.Push(tls.Identity(chain: certs, key: &*held))
        return ids.Done()[..]
    }
    fail NoKey
}

// Loopback is the address text of a loopback port.
Loopback(a: &Arena, port: int): str from a {
    return a.Fmt("127.0.0.1:{port}", (port)) else { "127.0.0.1:0" }
}

// Fatal reports a failure that ends the run before or around its
// transfers, and returns its status.
Fatal(sys: &System, code: int, text: str): int {
    sys.PrintErr("fetch: {text}\n", (text)) else {}
    return code
}
