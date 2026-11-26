// Why: Browsers reach the chat through a WebSocket or a server-sent event
// stream.
//
// What: Protocols, the origin check LocalOrigin, Socket, Events, and the
// helpers that pump lines between a connection and its bridge.
//
// How: A WebSocket carries one command or line per text message. An event
// stream takes over the connection and writes each event as an HTTP chunk from
// a stack buffer.

import std.http
import std.net
import std.time
import std.websocket
import src.proto

/// The WebSocket subprotocols the front end speaks: `chat.v1` is one
/// command or reply line per text message.
export const Protocols: [1]str = ["chat.v1"]

/// Whether a WebSocket from `origin` may connect: a client that sends no
/// origin (not a browser), or a page served from this machine. A page from
/// anywhere else could otherwise drive a visitor's browser into the chat.
LocalOrigin(origin: str): bool {
    if origin.Len() == 0 {
        return true
    }
    address := if origin.StartsWith("https://") { origin[8..] } else if origin.StartsWith("http://") { origin[7..] } else { return false }
    for host in ["127.0.0.1", "localhost", "[::1]"] {
        if address == host || address.StartsWith(host) && address.Byte(host.Len()) == b':' {
            return true
        }
    }
    return false
}

/// Opens a session for a web client on bridge `k` and, when the route names
/// a room, sends the `NICK` and `JOIN` lines the URL asked for, as if the
/// client had typed them. Fails with `Canceled`.
Begin(c: &Front, io: Io, k: int, peer: ?net.Addr, room: str, nick: ?str): Canceled!void {
    b := &c.bridges[k]
    c.arrivals.Send(io, Arrival(bridge: k, peer: peer)) else {
        fail Canceled
    }
    if n := nick {
        b.up.Send(io, Command("NICK ", n)) else {
            fail Canceled
        }
    }
    if room.Len() > 0 {
        b.up.Send(io, Command("JOIN ", room)) else {
            fail Canceled
        }
    }
}

/// The line `verb` `arg` as a packet.
Command(verb: str, arg: str): Packet {
    var f := proto.Frame.Empty(proto.Kind.Line)
    f.Push(verb)
    f.Push(arg)
    f.Push("\n")
    return Packet.Line(f)
}

/// Ends the web side of bridge `b` and lets go of it. Unless the session's
/// end was already read (`ended`), tells the session the client is gone and
/// reads what it still sends until its end, so it is never left waiting.
Finish(b: &Bridge, io: Io, ended: bool) {
    if ended {
        b.Let()
        return
    }
    calm := io.NoCancel()
    // a session that never started sends no end, so the wait is bounded
    scope s := calm.Scope() {
        s.CancelAfter(time.Seconds(5))
        b.up.Send(calm, Packet.End()) else {}
        for {
            p := b.down.Recv(calm) else {
                break
            }
            if p.done {
                break
            }
        }
    }
    b.Let()
}

/// Gives back a bridge claimed for a client whose session never started.
Unclaim(b: &Bridge) {
    b.Let()
    b.Let()
}

/// Takes the connection over as a WebSocket and pumps its messages to and
/// from a session until either side ends. Answers 503 when every bridge is
/// in use. The handshake refuses a foreign origin with 403.
Socket(c: &Front, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link, room: str, nick: ?str): http.Error!void {
    k := c.Claim(io) else {
        res.Status(503)
        try res.Body(b"too many web clients\n")
        return
    }
    c.Enter()
    defer c.Leave()
    b := &c.bridges[k]
    t := link.Takeover() else {
        Unclaim(b)
        return
    }
    peer := t.conn.PeerAddr()
    ws := Accept(c, io, req, t) else e {
        t.Abandon()
        Unclaim(b)
        c.hb.log.Warn(io, "web handshake refused: {e}", (e)) else {}
        return
    }
    Begin(c, io, k, peer, room, nick) else {
        // only a shutdown cancels a handler; the session sees the end
        Finish(b, io, false)
        ws.Abandon()
        return
    }
    var ended := false
    scope p := io.Scope() {
        p.go Upstream(&ws, c.heap, io, b)
        ended = Downstream(&ws, io, b)
        p.Cancel()
    }
    Finish(b, io, ended)
    ws.Close(io.NoCancel(), 1000, "bye") else {
        ws.Abandon()
    }
}

/// The handshake, in one place: `websocket.Accept` with the front end's
/// origin rule and subprotocols.
Accept(c: &Front, io: Io, req: &http.Request, t: http.Taken): websocket.Error!websocket.Conn from c, t {
    return try websocket.Accept(c.heap, io, req, t, Protocols[..], LocalOrigin)
}

/// Sends the session's lines to the client, one text message each, until
/// the session ends or the client cannot be written to. Returns whether the
/// session ended.
Downstream(ws: &websocket.Conn, io: Io, b: &Bridge): bool {
    for {
        p := b.down.Recv(io) else {
            return false
        }
        if p.done {
            return true
        }
        line := p.frame.Bytes()
        ws.Send(io, websocket.Kind.Text, line[..line.Len() - 1]) else {
            return false
        }
    }
}

/// Hands each text message the client sends to its session as one line,
/// until the client closes or fails, and then tells the session.
Upstream(ws: &websocket.Conn, h: &Heap, io: Io, b: &Bridge) {
    for {
        var a := Arena.New(h) else {
            break
        }
        m := ws.Recv(&a, io) else {
            a.Free()
            break
        }
        text := str.From(m.data) else {
            ""
        }
        f := proto.Frame.Of(proto.Kind.Line, text.Trim())
        a.Free()
        b.up.Send(io, Packet.Line(f)) else {
            return
        }
    }
    b.up.Send(io, Packet.End()) else {}
}

/// Streams a room's traffic as server-sent events: the session joins the
/// room under `nick` (or `events-K`, K the bridge), and each line it is sent
/// that is not a reply becomes one `data:` event, until `count` events (0
/// for no limit), the client leaves, or the server shuts down.
Events(c: &Front, a: &Arena, io: Io, res: mut http.Response, link: &http.Link, room: str, nick: ?str, count: int): http.Error!void {
    k := c.Claim(io) else {
        res.Status(503)
        try res.Body(b"too many web clients\n")
        return
    }
    c.Enter()
    defer c.Leave()
    b := &c.bridges[k]
    who := nick else {
        try a.Fmt("events-{k}", (k))
    }
    Begin(c, io, k, none, room, some(who)) else {
        Finish(b, io, false)
        fail Canceled
    }
    // the greeting, then the replies to NICK and JOIN
    var replies := 0
    for replies < 3 {
        p := b.down.Recv(io) else {
            Finish(b, io, false)
            fail Canceled
        }
        if p.done {
            Finish(b, io, true)
            res.Status(503)
            try res.Body(b"the server is closing\n")
            return
        }
        line := p.frame.Text()
        if line.StartsWith("ERR") {
            Finish(b, io, false)
            res.Status(409)
            try res.Body(p.frame.Bytes())
            return
        }
        if line.StartsWith("OK") {
            replies += 1
        }
    }
    res.Status(200)
    try res.Header("Content-Type", "text/event-stream")
    try res.Header("Cache-Control", "no-cache")
    var w := res.Writer()
    try Print(mut w, ": {who} joined {room}\n\n", (who, room))
    res.Flush(io, link) else e {
        Finish(b, io, false)
        fail e
    }
    if link.Version() == http.HttpVersion.Http2 {
        ended := PumpH2(io, res, link, b, count) else e {
            Finish(b, io, false)
            fail e
        }
        Finish(b, io, ended)
        return
    }
    // the events are written as chunks on the taken connection, each from a
    // buffer on the stack: a Body per event would copy each one into the
    // response's arena for the whole life of the stream
    t := link.Takeover() else {
        Finish(b, io, false)
        return
    }
    ended := Pump(&t.conn, io, b, count) else {
        t.Abandon()
        Finish(b, io, false)
        return
    }
    t.Abandon()
    Finish(b, io, ended)
}

// Send one event per Flush. Body reuses the buffer instead of retaining all
// the bytes written since the event stream opened.
PumpH2(io: Io, res: mut http.Response, link: &http.Link, b: &Bridge, count: int): http.Error!bool {
    var sent := 0
    for count == 0 || sent < count {
        p := b.down.Recv(io) else { return false }
        if p.done {
            return true
        }
        line := p.frame.Text().Trim()
        if line.StartsWith("OK") || line.StartsWith("ERR") {
            continue
        }
        var event: [520]u8 = [0; 520]
        var n := 0
        for part in ["data: ", line, "\n\n"] {
            (mut event[n..n + part.Len()]).CopyFrom(part.AsBytes())
            n += part.Len()
        }
        try res.Body(event[..n])
        try res.Flush(io, link)
        sent += 1
    }
    return false
}

/// Writes each line the session is sent, replies left out, as one event in
/// a chunk of its own, then the last chunk. Returns whether the session
/// ended first. Fails when the client cannot be written to.
Pump(conn: &net.Conn, io: Io, b: &Bridge, count: int): net.Error!bool {
    var sent := 0
    var ended := false
    for count == 0 || sent < count {
        p := b.down.Recv(io) else {
            break
        }
        if p.done {
            ended = true
            break
        }
        line := p.frame.Text()
        if line.StartsWith("OK") || line.StartsWith("ERR") {
            continue
        }
        var buf: [600]u8 = [0; 600]
        n := Chunk(line.Trim(), mut buf[..])
        try conn.WriteAll(io, buf[..n])
        sent += 1
    }
    try conn.WriteAll(io, b"0\r\n\r\n")
    return ended
}

/// Writes `line` into `buf` as one chunk holding one event: the size in hex,
/// CRLF, `data: `, the line, a blank line, CRLF. Returns the length. `buf`
/// holds 600 bytes and a line at most 511. O(line); allocates nothing.
Chunk(line: str, buf: mut []u8): int {
    size := "data: ".Len() + line.Len() + 2
    hex := "0123456789abcdef"
    var n := 0
    var shift := 8
    for shift >= 0 {
        d := (size >> shift) & 0xf
        if d != 0 || n > 0 || shift == 0 {
            buf[n] = hex.Byte(d)
            n += 1
        }
        shift -= 4
    }
    for part in ["\r\ndata: ", line, "\n\n\r\n"] {
        for i in 0..part.Len() {
            buf[n + i] = part.Byte(i)
        }
        n += part.Len()
    }
    return n
}

test "Chunk frames one event in hex-sized chunked encoding" {
    var buf: [600]u8 = [0; 600]
    n := Chunk("hi", mut buf[..])
    Assert(str.From(buf[..n]).Must() == "a\r\ndata: hi\n\n\r\n")
}
