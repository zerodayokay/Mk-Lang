// std.net's surface, over loopback in one process: a
// listener made with options, a stream's no-delay and keep-alive options, its local and peer
// addresses, a half-close the peer reads as the end of input, the same stream erased into a
// net.Conn, a Unix-domain socket, an Accept cut by a deadline and called again, and
// the errors of a refused dial and a bad address. Ports and paths are never printed.
import std.fs
import std.net
import std.time

// Upper answers one connection on l: it reads until the peer's half-close, then writes the bytes
// back in upper case and closes its side. A failure is kept in failed.
Upper(io: Io, l: &net.Listener, failed: mut ?net.Error) {
    s := l.Accept(io) else e {
        *failed = some(e)
        return
    }
    defer s.Close()
    var buf: [256]u8 = [0; 256]
    var n := 0
    for n < buf.Len() {
        got := s.Read(io, mut buf[n..]) else e {
            *failed = some(e)
            return
        }
        if got == 0 {
            break
        }
        n += got
    }
    for i in 0..n {
        if buf[i] >= b'a' && buf[i] <= b'z' {
            buf[i] -= 32
        }
    }
    s.WriteAll(io, buf[..n]) else e {
        *failed = some(e)
        return
    }
    s.Shutdown(io, net.Shutdown.Write) else e {
        *failed = some(e)
    }
}

// Drain reads c until the end of input into buf and returns how many bytes it read.
Drain(io: Io, c: &net.Conn, buf: mut []u8): net.Error!int {
    var n := 0
    for n < buf.Len() {
        got := try c.Read(io, mut buf[n..])
        if got == 0 {
            break
        }
        n += got
    }
    return n
}

// Exchange sends text over c, half-closes it, and returns what the peer sent back.
Exchange(a: &Arena, io: Io, c: &net.Conn, text: str): net.Error|OutOfMemory!str from a {
    try c.WriteAll(io, text.AsBytes())
    try c.Shutdown(io, net.Shutdown.Write)
    buf := try a.Slice[u8](256)
    n := try Drain(io, c, mut buf[..])
    return str.From(buf[..n]) else {
        "(not UTF-8)"
    }
}

// Talk erases stream into a Conn, exchanges text through it, closes it, and returns the reply,
// or what failed.
Talk(a: &Arena, io: Io, stream: net.Stream, text: str): str from a {
    conn := stream.Conn()
    reply := Exchange(a, io, &conn, text) else e {
        conn.Abandon()
        return Describe(a, "exchange", e)
    }
    conn.Close(io) else e {
        conn.Abandon()
        return Describe(a, "close", e)
    }
    return reply
}

// Describe formats what failed and why, in a.
Describe(a: &Arena, what: str, e: net.Error | OutOfMemory): str from a {
    return a.Fmt("{what}: {e}", (what: what, e: e)) else {
        "(out of memory)"
    }
}

// Tuned sets stream's options and reports whether its local and peer addresses are loopback
// ones that differ, the peer's on port.
Tuned(a: &Arena, stream: &net.Stream, port: int): net.Error|OutOfMemory!bool {
    try stream.SetNoDelay(true)
    try stream.SetKeepAlive(some(time.Seconds(30)))
    try stream.SetKeepAlive(none)
    local := try a.Fmt("{x}", (x: stream.LocalAddr()))
    peer := try a.Fmt("{x}", (x: stream.PeerAddr()))
    suffix := try a.Fmt(":{port}", (port: port))
    return peer.EndsWith(suffix) && local.StartsWith("127.0.0.1:") && local != peer
}

// Unlink removes path, ignoring a failure.
Unlink(io: Io, path: str) {
    fs.Remove(io, path) else {
    }
}

// Tcp serves one connection with Upper and talks to it through a stream made a Conn.
Tcp(w: mut Writer, a: &Arena, io: Io): WriteError!void {
    var o := net.DefaultListenOptions
    o.backlog = 8
    o.no_delay = true
    l := net.ListenWith(io, "127.0.0.1:0", o) else e {
        try Print(w, "listen: {e}\n", (e: e))
        return
    }
    defer l.Close()
    port := l.Port()
    var failed: ?net.Error = none
    var reply := ""
    var addrs := false
    scope s := io.Scope() {
        s.go Upper(io, &l, mut failed)
        addr := a.Fmt("127.0.0.1:{port}", (port: port)) else {
            s.Cancel()
            return
        }
        stream := net.Dial(io, addr) else e {
            try Print(w, "dial: {e}\n", (e: e))
            s.Cancel()
            return
        }
        addrs = Tuned(a, &stream, port) else e {
            reply = Describe(a, "options", e)
            false
        }
        reply = Talk(a, io, stream, "half-closed hello")
    }
    if f := failed {
        try Print(w, "server: {f}\n", (f: f))
    }
    try Print(w, "tcp: addresses {addrs}, reply {reply}\n", (addrs: addrs, reply: reply))
}

// Unix serves one connection with Upper. On Windows the corpus runs in
// its own directory, giving the socket a short relative name. Else use the
// system's temporary directory (a WSL run directory cannot host a socket).
Unix(w: mut Writer, a: &Arena, io: Io): WriteError!void {
    dir := if Target.os == Os.Windows { "." } else { "" }
    (file, path) := fs.CreateTemp(a, io, dir, "mk-sockets-") else e {
        try Print(w, "temp name: {e}\n", (e: e))
        return
    }
    file.Abandon()
    fs.Remove(io, path) else e {
        try Print(w, "remove temp name: {e}\n", (e: e))
        return
    }
    defer Unlink(io, path)
    l := net.ListenUnix(io, path) else e {
        try Print(w, "listen unix: {e}\n", (e: e))
        return
    }
    defer l.Close()
    var failed: ?net.Error = none
    var reply := ""
    scope s := io.Scope() {
        s.go Upper(io, &l, mut failed)
        stream := net.DialUnix(io, path) else e {
            try Print(w, "dial unix: {e}\n", (e: e))
            s.Cancel()
            return
        }
        reply = Talk(a, io, stream, "over a unix socket")
    }
    if f := failed {
        try Print(w, "unix server: {f}\n", (f: f))
    }
    try Print(w, "unix: reply {reply}\n", (reply: reply))
}

// Accepts cuts an Accept with a 20 ms deadline, then calls it again for a client that comes.
Accepts(w: mut Writer, a: &Arena, io: Io): WriteError!void {
    l := net.Listen(io, "127.0.0.1:0") else e {
        try Print(w, "listen: {e}\n", (e: e))
        return
    }
    defer l.Close()
    var cut := ""
    var expired := false
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(20))
        match l.Accept(io) {
            ok(c) {
                c.Close()
                cut = "accepted"
            }
            err(e) {
                cut = a.Fmt("{e}", (e: e)) else {
                    "?"
                }
            }
        }
        expired = s.Expired()
    }
    var failed: ?net.Error = none
    var reply := ""
    scope s := io.Scope() {
        s.go Upper(io, &l, mut failed)
        addr := a.Fmt("127.0.0.1:{port}", (port: l.Port())) else {
            s.Cancel()
            return
        }
        stream := net.Dial(io, addr) else e {
            try Print(w, "dial: {e}\n", (e: e))
            s.Cancel()
            return
        }
        reply = Talk(a, io, stream, "again")
    }
    try Print(w, "accept under a deadline: {cut}, expired {expired}; called again: {reply}\n", (cut: cut, expired: expired, reply: reply))
}

// Refusals dials a port nothing listens on, and parses a bad address.
Refusals(w: mut Writer, a: &Arena, io: Io): WriteError!void {
    l := net.Listen(io, "127.0.0.1:0") else e {
        try Print(w, "listen: {e}\n", (e: e))
        return
    }
    port := l.Port()
    l.Close()
    addr := a.Fmt("127.0.0.1:{port}", (port: port)) else {
        return
    }
    match net.Dial(io, addr) {
        ok(s) {
            s.Close()
            try Print(w, "dial a closed port: connected\n")
        }
        err(e) {
            try Print(w, "dial a closed port: {e}\n", (e: e))
        }
    }
    _ = net.ParseAddr("127.0.0.256:80") else e {
        try Print(w, "parse 127.0.0.256:80: {e}\n", (e: e))
    }
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    try Tcp(mut out, sys.arena, sys.io)
    try Unix(mut out, sys.arena, sys.io)
    try Accepts(mut out, sys.arena, sys.io)
    try Refusals(mut out, sys.arena, sys.io)
}
