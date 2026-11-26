// Why: The cases drive the server from scripts and compare a transcript, so a
// test reads like a conversation between named clients.
//
// What: Replay, BadScript, TooManyClients, ScriptError, and the connection
// kinds in Wire: TCP chat, HTTP to the web front end, or a WebSocket.
//
// How: Each script line is `name: command`; a name's first command opens its
// connection. Every send and every line read is printed as `name> ...` or
// `name< ...` in script order, so the transcript is deterministic.

import std.http
import std.net
import std.proc
import std.time
import std.url
import std.websocket
import src.proto

/// A script line that is not `name: command`, with its line number.
export error BadScript(line: int)
/// A script with more clients than `MaxScriptClients`.
export error TooManyClients(line: int)

/// Why a replay stopped: a bad script, memory, or the transcript could not be written.
export type ScriptError = BadScript | TooManyClients | OutOfMemory | WriteError

/// The most clients one script may use.
export const MaxScriptClients: int = 16

/// How a scripted client is connected: a TCP stream (the chat protocol, or
/// HTTP to the web front end), or a WebSocket.
enum Wire {
    Tcp(s: net.Stream)
    Ws(c: websocket.Conn)
    Events(r: proc.PipeReader)
}

/// Replay's web address and, for HTTPS, its TLS connector.
export struct WebTarget {
    export addr: str
    export secure: ?net.Secure
}

/// One scripted client: its name, its connection and what it has read.
struct Scripted {
    name: proto.Name
    wire: Wire
    lb: proto.LineBuf
}

/// Hangs up: a TCP stream is closed, a WebSocket closed with its handshake.
Wire.Hangup(self, io: Io) {
    match self {
        .Tcp(s) {
            s.Close()
        }
        .Ws(c) {
            c.Close(io, 1000, "bye") else {
                c.Abandon()
            }
        }
        .Events(r) { r.Close() }
    }
}

/// Runs `script` against the server at `addr`, and its web front end at
/// `web` when there is one, writing a transcript to `out`: `name> command`
/// for what a client sends, `name< line` for what it reads, `name! ...` for
/// its connection's events. Each command is read up to its `OK` or `ERR`
/// line, and what other clients were sent meanwhile shows when they next
/// act, so the transcript is the same in every run. Fails with `BadScript`
/// at the first malformed line.
export Replay(h: &Heap, io: Io, addr: str, web: &WebTarget, script: str, out: mut Writer): ScriptError!void {
    var arena := try Arena.New(h)
    defer arena.Free()
    var limits := http.DefaultClientLimits
    limits.secure = web.secure
    limits.h2 = web.secure.IsSome()
    web_client := http.Client.New(h, limits)
    defer web_client.Close()
    scope events := io.Scope() {
        defer events.Cancel()
        a := h.Alloc()
        var clients := HeapList[Scripted].New(a)
        defer clients.FreeEach(a, fn(c: Scripted) {
            Scripted(wire, ..) := c
            wire.Hangup(io)
        })
        var n := 0
        for raw in script.Lines() {
            n += 1
            line := raw.Trim()
            if line.Len() == 0 || line.StartsWith("#") {
                continue
            }
            at := line.Find(": ") else {
                fail BadScript(line: n)
            }
            name := line[..at]
            cmd := line[at + 2..].Trim()
            if !proto.ValidNick(name) || cmd.Len() == 0 {
                fail BadScript(line: n)
            }
            if cmd.StartsWith("!get ") {
                try Get(&arena, io, &web_client, web, name, "GET", cmd[5..], out)
                continue
            }
            if cmd.StartsWith("!post ") {
                try Get(&arena, io, &web_client, web, name, "POST", cmd[6..], out)
                continue
            }
            var k := Find(&clients, name) else {
                -1
            }
            if k < 0 {
                if clients.Len() == MaxScriptClients {
                    fail TooManyClients(line: n)
                }
                if cmd.StartsWith("!ws ") || cmd.StartsWith("!events ") {
                    try Print(out, "{name}> {cmd}\n", (name, cmd))
                }
                wire := if web.secure.IsSome() && cmd.StartsWith("!events ") {
                    (r, w) := proc.Pipe(io) else e {
                        try Print(out, "{name}! connect failed: {e}\n", (name, e))
                        continue
                    }
                    events.go ReadEvents(h, io, &web_client, web.addr, cmd[8..].Trim(), w)
                    Wire.Events(r: r)
                } else { Connect(h, &arena, io, addr, web, cmd) else e {
                    match e {
                        NoWeb {
                            try Print(out, "{name}! connect failed: no web front end; start it with --web\n", (name))
                        }
                        _ {
                            try Print(out, "{name}! connect failed: {e}\n", (name, e))
                        }
                    }
                    continue
                } }
                sc := Scripted(name: proto.Name.Of(name), wire: wire, lb: proto.LineBuf.New())
                clients.Push(a, sc) else {
                    Scripted(wire: lost, ..) := sc
                    lost.Hangup(io)
                    fail OutOfMemory
                }
                k = clients.Len() - 1
                try Print(out, "{name}! connected\n", (name))
                if p := Protocol(&clients[k].wire) {
                    try Print(out, "{name}! speaks {p}\n", (name, p))
                }
                if cmd.StartsWith("!events ") {
                    try Head(h, io, mut clients[k], out)
                    continue
                }
                try Upto(h, io, mut clients[k], out)
                if cmd.StartsWith("!ws ") {
                    // the replies to the NICK and JOIN the URL asked for
                    for _ in 0..Asked(cmd) {
                        try Upto(h, io, mut clients[k], out)
                    }
                    continue
                }
            }
            match cmd {
                "!eof" {
                    for {
                        got := Next(h, io, mut clients[k]) else e {
                            try Print(out, "{name}! read failed: {e}\n", (name, e))
                            break
                        }
                        l := got else {
                            try Print(out, "{name}! closed by the server\n", (name))
                            break
                        }
                        try Print(out, "{name}< {l}\n", (name, l))
                    }
                    Drop(mut clients, k, io)
                }
                "!close" {
                    try Print(out, "{name}! hangs up\n", (name))
                    Drop(mut clients, k, io)
                }
                "!shutdown" {
                    try Print(out, "{name}! shuts down its side\n", (name))
                    try Polite(h, io, mut clients[k], out)
                    Drop(mut clients, k, io)
                }
                "!read" {
                    got := Next(h, io, mut clients[k]) else {
                        none
                    }
                    if l := got {
                        try Print(out, "{name}< {l}\n", (name, l))
                    }
                }
                _ {
                    var bytes: [520]u8 = [0; 520]
                    len := if cmd.StartsWith("!hex ") {
                        m := Unhex(cmd[5..], mut bytes[..512]) else {
                            fail BadScript(line: n)
                        }
                        bytes[m] = b'\n'
                        m + 1
                    } else {
                        Frame(cmd, mut bytes[..])
                    }
                    try Print(out, "{name}> {cmd}\n", (name, cmd))
                    Transmit(io, &clients[k].wire, bytes[..len]) else e {
                        try Print(out, "{name}! write failed: {e}\n", (name, e))
                        continue
                    }
                    try Upto(h, io, mut clients[k], out)
                }
            }
        }
        // everyone still connected says goodbye, in the order they joined
        for i in 0..clients.Len() {
            name := clients[i].name
            try Print(out, "{n}> QUIT\n", (n: name.Str()))
            Transmit(io, &clients[i].wire, b"QUIT\n") else {
                continue
            }
            try Upto(h, io, mut clients[i], out)
        }
    }
}

/// A web command in a replay run without `--web`.
error NoWeb

/// Why a scripted client could not connect.
type ConnectError = websocket.Error | url.Error | NoWeb

/// Connects a new client the way its first command says: `!ws PATH [ORIGIN]`
/// opens a WebSocket to the web front end, `!events PATH` asks it for an
/// event stream over plain HTTP, and anything else is a chat connection.
Connect(h: &Heap, a: &Arena into a, io: Io, addr: str, web: &WebTarget, cmd: str): ConnectError!Wire from h {
    if !cmd.StartsWith("!ws ") && !cmd.StartsWith("!events ") {
        return Wire.Tcp(s: try net.Dial(io, addr))
    }
    if web.addr.Len() == 0 {
        fail NoWeb
    }
    if cmd.StartsWith("!events ") {
        path := cmd[8..].Trim()
        s := try Dial(io, web.addr)
        if !Request(&s, io, path, web.addr) {
            s.Close()
            fail net.Reset
        }
        return Wire.Tcp(s: s)
    }
    args := cmd[4..].Trim()
    var path := args
    var origin := ""
    if sp := args.Find(" ") {
        path = args[..sp]
        origin = args[sp + 1..].Trim()
    }
    scheme := if web.secure.IsSome() { "wss" } else { "ws" }
    u := try url.Parse(a, try a.Fmt("{scheme}://{web.addr}{path}", (scheme, web, path)), none)
    headers := [http.Header(name: "Origin", value: origin)]
    protocols := ["chat.v1"]
    var o := websocket.DefaultOptions
    o.secure = web.secure
    o.protocols = protocols[..]
    if origin.Len() > 0 {
        o.headers = headers[..]
    }
    // the front end starts beside the script, so its first connect may be early
    var wait := time.Millis(10)
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        for {
            c := websocket.Dial(h, a, io, &u, &o) else e {
                match e {
                    net.Refused {
                        io.Sleep(wait) else {
                            fail e
                        }
                        wait = time.Nanos(wait.Nanos() * 2)
                        continue
                    }
                    _ {
                        fail e
                    }
                }
            }
            return Wire.Ws(c: c)
        }
    }
}

/// Connects to the web front end's address, retrying while it is not yet
/// listening, for at most five seconds.
Dial(io: Io, web: str): net.Error!net.Stream {
    var wait := time.Millis(10)
    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        for {
            c := net.Dial(io, web) else e {
                if s.Expired() {
                    fail time.TimedOut
                }
                match e {
                    net.Refused {
                        io.Sleep(wait) else {
                            fail time.TimedOut
                        }
                        wait = time.Nanos(wait.Nanos() * 2)
                        continue
                    }
                    _ {
                        fail e
                    }
                }
            }
            return c
        }
    }
}

/// Sends `data`, a line and its newline, on a chat connection, or as one
/// text message without the newline on a WebSocket.
Transmit(io: Io, w: &Wire, data: []u8): websocket.Error|WriteError!void {
    match w {
        .Tcp(s) {
            try s.WriteAll(io, data)
        }
        .Ws(c) {
            kind := if IsText(data) { websocket.Kind.Text } else { websocket.Kind.Binary }
            try c.Send(io, kind, data[..data.Len() - 1])
        }
        .Events { fail WriteError }
    }
}

/// Sends one request for `path` to the web front end and prints the status
/// and the body's lines. The client keeps the connection for the next GET.
Get(a: &Arena into a, io: Io, client: &http.Client, web: &WebTarget, name: str, method: str, path: str, out: mut Writer): ScriptError!void {
    try Print(out, "{name}> {method} {path}\n", (name, method, path))
    if web.addr.Len() == 0 {
        try Print(out, "{name}! GET failed: no web front end; start it with --web\n", (name))
        return
    }
    scheme := if web.secure.IsSome() { "https" } else { "http" }
    text := try a.Fmt("{scheme}://{web.addr}{path}", (scheme, web, path))
    u := url.Parse(a, text, none) else e {
        try Print(out, "{name}! bad URL: {e}\n", (name, e))
        return
    }
    blank := [http.Header(name: "", value: "")]
    req := http.ClientRequest(method: method, url: &u, headers: blank[..0], body: b"")
    reply := client.Fetch(a, io, &req) else e {
        try Print(out, "{name}! GET failed: {e}\n", (name, e))
        return
    }
    try Print(out, "{name}< {s}\n", (name, s: reply.status))
    body := str.From(reply.body) else {
        try Print(out, "{name}< (not UTF-8)\n", (name))
        return
    }
    for l in body.Lines() {
        try Print(out, "{name}< {l}\n", (name, l))
    }
}

/// Prints the head of an event stream's response, up to its blank line.
Head(h: &Heap, io: Io, c: mut Scripted, out: mut Writer): WriteError!void {
    name := c.name
    for {
        got := Next(h, io, c) else e {
            try Print(out, "{n}! read failed: {e}\n", (n: name.Str(), e))
            return
        }
        l := got else {
            try Print(out, "{n}! closed by the server\n", (n: name.Str()))
            return
        }
        try Print(out, "{n}< {l}\n", (n: name.Str(), l))
        if l.Len() == 0 {
            return
        }
    }
}

/// Closes the client's sending side and prints what the server still sends
/// until it closes too. A WebSocket closes with its handshake instead.
Polite(h: &Heap, io: Io, c: mut Scripted, out: mut Writer): WriteError!void {
    name := c.name
    match &c.wire {
        .Tcp(s) {
            s.Shutdown(io, net.Shutdown.Write) else e {
                try Print(out, "{n}! shutdown failed: {e}\n", (n: name.Str(), e))
                return
            }
        }
        .Ws | .Events {
            return
        }
    }
    for {
        got := Next(h, io, c) else e {
            try Print(out, "{n}! read failed: {e}\n", (n: name.Str(), e))
            return
        }
        l := got else {
            try Print(out, "{n}! closed by the server\n", (n: name.Str()))
            return
        }
        try Print(out, "{n}< {l}\n", (n: name.Str(), l))
    }
}

/// The index of the client called `name`.
Find(clients: &HeapList[Scripted], name: str): ?int {
    for i in 0..clients.Len() {
        if clients[i].name.Str() == name {
            return some(i)
        }
    }
    return none
}

/// Closes and forgets client `k`.
Drop(clients: mut HeapList[Scripted], k: int, io: Io) {
    Scripted(wire, ..) := clients.Remove(k)
    wire.Hangup(io)
}

/// Prints every line client `c` reads up to and including its next `OK` or
/// `ERR` line, or up to the end of its stream.
Upto(h: &Heap, io: Io, c: mut Scripted, out: mut Writer): WriteError!void {
    name := c.name
    for {
        got := Next(h, io, c) else e {
            try Print(out, "{n}! read failed: {e}\n", (n: name.Str(), e))
            return
        }
        l := got else {
            try Print(out, "{n}! closed by the server\n", (n: name.Str()))
            return
        }
        try Print(out, "{n}< {l}\n", (n: name.Str(), l))
        if Final(l) {
            return
        }
    }
}

/// The next line client `c` reads, or `none` at the end of its stream. A
/// WebSocket message is one line, and a close from the server is the end.
Next(h: &Heap, io: Io, c: mut Scripted): websocket.Error|proc.Error!?str from c {
    for {
        match c.lb.Next() {
            .Line(len) {
                text := c.lb.Line(len) else {
                    return some("(not UTF-8)")
                }
                return some(text)
            }
            .Overlong {
                return some("(line too long)")
            }
            .More {}
        }
        n := try Fill(h, io, &c.wire, c.lb.Space())
        if n == 0 {
            return none
        }
        c.lb.Fill(n)
    }
}

/// Reads what the connection has into `buf`: bytes from a stream, or one
/// message and a newline from a WebSocket, cut to fit. Returns 0 at the end.
Fill(h: &Heap, io: Io, w: &Wire, buf: mut []u8): websocket.Error|proc.Error!int {
    match w {
        .Tcp(s) {
            return try s.Read(io, buf)
        }
        .Ws(c) {
            var a := try Arena.New(h)
            defer a.Free()
            m := c.Recv(&a, io) else e {
                match e {
                    websocket.PeerClosed {
                        return 0
                    }
                    _ {
                        fail e
                    }
                }
            }
            var n := m.data.Len()
            if n > buf.Len() - 1 {
                n = buf.Len() - 1
            }
            (mut buf[..n]).CopyFrom(m.data[..n])
            buf[n] = b'\n'
            return n + 1
        }
        .Events(r) { return try r.Read(io, buf) }
    }
}

/// Whether `data` is UTF-8, so a WebSocket sends it as text.
IsText(data: []u8): bool {
    _ = str.From(data) else {
        return false
    }
    return true
}

/// How many commands a `!ws PATH` line makes the server run for the client:
/// a `JOIN` for a path under `/rooms/`, and a `NICK` for a `nick` parameter.
Asked(cmd: str): int {
    path := cmd[4..].Trim()
    var n := 0
    if path.StartsWith("/rooms/") {
        n += 1
    }
    if path.Find("?nick=").IsSome() || path.Find("&nick=").IsSome() {
        n += 1
    }
    return n
}

test "Asked counts the commands a WebSocket URL runs" {
    Assert(Asked("!ws /ws") == 0)
    Assert(Asked("!ws /ws?nick=a") == 1)
    Assert(Asked("!ws /rooms/x/ws?room=1&nick=a http://localhost") == 2)
}

/// Writes a GET request for `path` on `s`, asking the server to close the
/// connection after its answer: an event stream is the one request on it,
/// and a refused one ends with its body. Returns false when the stream
/// cannot be written to.
Request(s: &net.Stream, io: Io, path: str, host: str): bool {
    var buf: [600]u8 = [0; 600]
    var w := s.Writer(io, mut buf[..])
    Print(mut w, "GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n", (path, host)) else {
        return false
    }
    w.Flush() else {
        return false
    }
    return true
}

/// The subprotocol a WebSocket client and the server agreed on, `none` for
/// a TCP client or when they agreed on none.
Protocol(w: &Wire): ?str from w {
    return match w {
        .Ws(c) { c.Protocol() }
        .Tcp | .Events { none }
    }
}
