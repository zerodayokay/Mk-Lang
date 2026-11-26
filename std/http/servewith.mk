// Why: a production server needs limits Serve lacks (connections, head and
// body bytes, time per head and between requests), a secure slot for TLS,
// and the option of a listener the caller owns.
//
// What: ServeWith (binds its own listener), ServeOn (the caller's
// listener), ServeChan (ServeOn with a channel the handler may store into),
// and what they share: AcceptLoop, ServeConnLoop, CallHandler, HeadRead
// and BodyRead.
//
// How: one accept loop and one per-connection loop over net.Conn, so the
// secure slot can wrap each connection. The per-connection loop is Serve's
// (serve.mk), with the head and body limits apart and two time limits.
// More below.

// The loop is Serve's: an 8 KiB read buffer and a 4 KiB write buffer on
// the task's stack, the head found by HeadScan, the large-head path,
// chunked bodies decoded in place. The time limits are
// the first byte of a request within keep_alive and the rest of its head
// within request_header, each a scope deadline around the read. At most
// connections connections are served at once: a semaphore permit is taken
// before each Accept, so the rest wait in the listen queue.

import std.core
import std.net
import std.rt
import std.sync
import std.time

// MaxHeadRoom bounds the large-head buffer whatever header_bytes says:
// 1 GiB, far past any head a server should take.
const MaxHeadRoom = 1073741824

// DirectFn is the handler of ServeWith and ServeOn.
type DirectFn[C] = fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void

// RelayFn is ServeChan's handler, which also takes the server's heap and
// the channel, and may store the heap into it.
type RelayFn[C, T] = fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void

/// Listens on addr and serves every connection in a task of its own until
/// the caller's scope is cancelled, as Serve does, with o's settings and a
/// Link beside each response. O(bytes served) per connection; each
/// connection allocates one arena from sys.heap, reset after each
/// response.
///
/// It serves at most o.connections at once; more wait in the listen queue.
/// A head over header_bytes is answered 431 and a body over body_bytes
/// 413. A connection is closed when it waits longer than keep_alive
/// between requests, when a head takes longer than request_header from its
/// first byte, when a read of a body waits longer than request_header, and
/// when its client takes no piece of a reply (64 KiB at most) within
/// request_header. Chunked request bodies are read whole into the
/// request's arena, and `Expect: 100-continue` is answered before the body
/// is read. With o.secure, each connection goes through the acceptor first,
/// and a failed handshake closes it.
///
/// Fails with Listen's error, or with Accept's Closed or InvalidAddress;
/// other Accept errors wait and try again as Serve's do. ServeWith is
/// net.Listen, then ServeOn over the listener, then its Close.
export ServeWith[C: Share](sys: &System, addr: str, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void {
    l := try net.Listen(sys.io, addr)
    defer l.Close()
    try ServeOn(sys.heap, sys.io, &l, o, c, handle)
}

/// Serves every connection l accepts, as ServeWith does with o's settings,
/// until the caller's scope is cancelled. The caller makes the listener, so
/// it can read l.Port() before it serves (a listener on port 0), and l
/// stays open when ServeOn returns, to close or serve again. O(bytes
/// served) per connection; each connection allocates one arena from h,
/// reset after each response.
///
/// It takes h and io where ServeWith takes &System, which is not Share, so
/// a task of the program can run it. A cancel closes the connections being
/// served, unanswered; connections still waiting in l's queue are left for
/// the next Accept.
///
/// l must be a listener of io, or of the Io that io forwards to (a
/// sim.Recorder's): a machine listener served with a Sim's Io fails with
/// Closed, and a Sim's listener served with the machine's Io waits and
/// tries again until the cancel. Fails with Accept's Closed or
/// InvalidAddress; other Accept errors wait and try again as Serve's do.
export ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void {
    try AcceptLoop[C, u8](io, h, h, l, o, c, none, some(handle), none)
}

/// Serves every connection l accepts as ServeOn does, with o's settings,
/// until the caller's scope is cancelled, and hands each call of handle
/// the heap h and the channel ch beside c: for a server whose handlers
/// send values to the program's other tasks, such as a chat server's
/// rooms. O(bytes served) per connection; each connection allocates one
/// arena from h, reset after each response.
///
/// The handler may store h into ch (`h: &Heap into ch`), so it may send a
/// value made from h, such as a sync.Shared frame, into ch. No other loan
/// may reach ch:
///
/// - not c's loans, so a frame comes from h and not from a heap that c
///   holds;
/// - not the request, its arena or the link, so bytes of a request are
///   copied into the frame;
/// - not a value the handler received from ch that holds a loan, such as a
///   frame, so a handler does not send on a frame it received. A plain
///   value such as an int it may send.
///
/// The call declares the same store, so the caller's ch may hold h, and the
/// caller empties and frees ch before h goes. ch is reached from every
/// connection's task, so T is Send. Fails with Accept's Closed or
/// InvalidAddress; other Accept errors wait and try again as Serve's do.
export ServeChan[C: Share, T: Send](h: &Heap into ch, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, ch: &Chan[T], handle: fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void): Error!void {
    try AcceptLoop[C, T](io, h, h, l, o, c, some(ch), none, some(handle))
}

// AcceptLoop is ServeOn's and ServeChan's accept loop: a permit, an
// Accept and a task of ServeRelayConn per connection, until the scope's
// cancel. Connections' arenas come from h; rh is the heap the relay
// handler gets, which may go into ch (ServeChan passes its h as both).
// Exactly one of plain and relay is some, and ch is some with relay.
AcceptLoop[C: Share, T: Send](io: Io, h: &Heap, rh: &Heap into ch, l: &net.Listener, o: &ServerOptions, c: &C, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T]): Error!void {
    permits := if o.connections < 1 { 1 } else { o.connections }
    sem := sync.Semaphore.New(permits)
    if h3_server := o.h3 {
        a := try Arena.New(h)
        defer a.Free()
        addr := try a.Fmt("{addr}", (addr: l.Addr()))
        mux := try h3_server.Listen(h, io, addr, "h3")
        defer mux.Close()
        var opts := *o
        opts.h3_port = if o.alt_svc { mux.Port() } else { 0 }
        scope listeners := io.Scope() {
            defer listeners.Cancel()
            listeners.go RunH3Listener(&mux, io)
            listeners.go AcceptH3(&mux, h, rh, io, &opts, &sem, c, ch, plain, relay)
            try AcceptTcp(io, h, rh, l, &opts, &sem, c, ch, plain, relay, true)
        }
        return
    }
    try AcceptTcp(io, h, rh, l, o, &sem, c, ch, plain, relay, false)
}

AcceptTcp[C: Share, T: Send](io: Io, h: &Heap, rh: &Heap into ch, l: &net.Listener, o: &ServerOptions, sem: &sync.Semaphore, c: &C, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T], mixed: bool): Error!void {
    var delay := 0
    scope s := io.Scope() {
        for {
            if !mixed {
                sem.Acquire(io, 1) else { return }
            }
            st := l.Accept(io) else e {
                if !mixed {
                    sem.Release(1)
                }
                match e {
                    Canceled {
                        return
                    }
                    Closed {
                        fail e
                    }
                    net.InvalidAddress {
                        fail e
                    }
                    _ {
                        delay = Backoff(delay)
                        io.Sleep(core.DurationOf(i64(delay))) else {
                            return
                        }
                        continue
                    }
                }
            }
            delay = 0
            if mixed {
                sem.Acquire(io, 1) else {
                    st.Close()
                    return
                }
            }
            s.go ServeRelayConn(st.Conn(), h, io, o, sem, c, rh, ch, plain, relay)
        }
    }
}

// ServeConn serves one accepted connection, through the acceptor when o
// has one, then releases it and its permit.
ServeConn[C: Share](conn: net.Conn, heap: &Heap into conn, io: Io, o: &ServerOptions, sem: &sync.Semaphore, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void) {
    ServeRelayConn[C, u8](conn, heap, io, o, sem, c, heap, none, some(handle), none)
}

// ServeRelayConn is ServeConn with either handler (AcceptLoop's rh, ch,
// plain and relay).
ServeRelayConn[C: Share, T: Send](conn: net.Conn, heap: &Heap into conn, io: Io, o: &ServerOptions, sem: &sync.Semaphore, c: &C, rh: &Heap into ch, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T]) {
    defer sem.Release(1)
    if sec := o.secure {
        if o.h2 {
            var offer: [2]str = ["h2", "http/1.1"]
            (conn: sc, chosen) := sec.Negotiate(heap, io, conn, "", offer[..]) else {
                conn.Abandon()
                return
            }
            if chosen == 0 {
                var empty_pre: [0]u8 = []
                ServeH2(sc, heap, io, o, c, rh, ch, plain, relay, empty_pre[..], true)
                return
            }
            ServeRelayOwned(sc, heap, io, o, c, rh, ch, plain, relay, true)
            return
        }
        sc := sec.Connect(heap, io, conn, "") else {
            conn.Abandon()
            return
        }
        ServeRelayOwned(sc, heap, io, o, c, rh, ch, plain, relay, true)
        return
    }
    if o.h2c {
        cell := Mutex[?net.Conn].New(some(conn))
        var pre: [24]u8 = [0; 24]
        (is_h2, n: pn) := SniffH2c(&cell, io, o, mut pre)
        if is_h2 {
            if raw_conn := CellTake(&cell, io) {
                ServeH2(raw_conn, heap, io, o, c, rh, ch, plain, relay, pre[0..pn], false)
            }
            CloseCell(cell.Into(), io)
            return
        }
        ServeConnLoop(&cell, heap, io, o, c, rh, ch, plain, relay, pre[0..pn], false)
        CloseCell(cell.Into(), io)
        return
    }
    ServeRelayOwned(conn, heap, io, o, c, rh, ch, plain, relay, false)
}

// ServeOwned serves conn through a cell (link.mk) and closes it, unless a
// handler took it.
ServeOwned[C: Share](conn: net.Conn, heap: &Heap, io: Io, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void) {
    ServeRelayOwned[C, u8](conn, heap, io, o, c, heap, none, some(handle), none, false)
}

// ServeRelayOwned is ServeOwned with either handler.
ServeRelayOwned[C: Share, T: Send](conn: net.Conn, heap: &Heap, io: Io, o: &ServerOptions, c: &C, rh: &Heap into ch, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T], secure: bool) {
    cell := Mutex[?net.Conn].New(some(conn))
    var empty_pre: [0]u8 = []
    ServeConnLoop(&cell, heap, io, o, c, rh, ch, plain, relay, empty_pre[..], secure)
    CloseCell(cell.Into(), io)
}

// CallHandler calls the loop's handler for one request: plain with c, or
// relay with c, rh and the channel. With neither it fails with 500, which
// no entry point lets happen.
CallHandler[C: Share, T: Send](c: &C, rh: &Heap into ch, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    if f := plain {
        try f(c, a, io, req, res, link)
        return
    }
    if g := relay {
        if k := ch {
            try g(c, rh, k, a, io, req, res, link)
            return
        }
    }
    fail Status(code: 500)
}

// Clear makes the response an empty one of code that ends the connection
// (a handler's error, or its status that is not final).
Response.Clear(mut self, code: int) {
    self.code = code
    self.headers.Clear()
    self.data.Clear()
    self.text.Clear()
    self.close = true
}

// CloseConn closes conn, releasing it when the close fails.
CloseConn(conn: net.Conn, io: Io) {
    conn.Close(io) else {
        conn.Abandon()
    }
}

// HeadRead is one read of a head: the request's first byte within
// keep_alive (started is none), the rest within request_header of that
// byte (started, set here when the read that brought it returns, so the
// idle wait before it never counts against request_header). 0 when the
// peer closed, a limit passed or the read failed.
HeadRead(conn: &Mutex[?net.Conn], io: Io, buf: mut []u8, o: &ServerOptions, started: mut ?time.Instant): int {
    var wait := o.keep_alive
    if t := *started {
        left := o.request_header.Nanos() - time.Now(io).Since(t).Nanos()
        if left <= 0 {
            return 0
        }
        wait = time.Nanos(left)
    }
    var n := 0
    scope s := io.Scope() {
        s.CancelAfter(wait)
        n = CellRead(conn, io, buf)
    }
    if n > 0 && started.IsNone() {
        *started = some(time.Now(io))
    }
    return n
}

// BodyRead is one read of a request's body: 0 when the peer closed, the
// read failed, or no byte came within request_header, the idle limit
// between a body's reads (nginx's client_body_timeout), so a client that
// stops sending in the middle of a body cannot hold the connection.
BodyRead(conn: &Mutex[?net.Conn], io: Io, buf: mut []u8, o: &ServerOptions): int {
    var n := 0
    scope s := io.Scope() {
        s.CancelAfter(o.request_header)
        n = CellRead(conn, io, buf)
    }
    return n
}

// ConnErrorReply answers code with an empty body and Connection: close
// over a net.Conn, as ErrorReply does over a stream.
ConnErrorReply(conn: &Mutex[?net.Conn], io: Io, wb: mut [4096]u8, a: &Arena, pace: time.Duration, code: int) {
    var res := Response.New(a)
    res.code = code
    res.close = true
    if CellSend(conn, io, wb, &res, pace) && (code == 413 || code == 431) {
        CellLinger(conn, io)
    }
}

// CellLinger is ErrorReply's lingering close over the cell's connection.
CellLinger(cell: &Mutex[?net.Conn], io: Io) {
    g := CellLock(cell, io)
    if conn := &*g.Ref() {
        LingerConn(conn, io)
    }
    g.Unlock()
}

// LingerConn shuts conn's write side, then reads and drops what comes, as
// ErrorReply does.
LingerConn(conn: &net.Conn, io: Io) {
    conn.Shutdown(io, net.Shutdown.Write) else {
        return
    }
    var buf: [4096]u8 = [0; 4096]
    var total := 0
    t0 := time.Now(io)
    for total < LingerBytes {
        wait := LingerWait(io, t0)
        if wait <= 0 {
            return
        }
        var n := 0
        scope s := io.Scope() {
            s.CancelAfter(core.DurationOf(wait))
            n = conn.Read(io, mut buf[..]) else {
                0
            }
        }
        if n == 0 {
            return
        }
        total += n
    }
}

// ServeConnLoop serves one connection as Serve's Conn does, with o's
// limits: a head of at most header_bytes (431), a body of at most
// body_bytes (413), the first byte of each request within keep_alive, the
// rest of its head within request_header of that byte, and each read of
// its body within request_header (the connection is closed when one
// passes). It returns when the peer
// closes, a request asks to close, an error ends it or the scope is
// cancelled; a response write that fails ends it, so a connection whose
// write was cut off is never reused.
ServeConnLoop[C: Share, T: Send](conn: &Mutex[?net.Conn], heap: &Heap, io: Io, o: &ServerOptions, c: &C, rh: &Heap into ch, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T], pre: []u8, secure: bool) {
    limit := o.header_bytes
    var arena := Arena.New(heap) else {
        return
    }
    defer arena.Free()
    var rb: [8192]u8 = [0; 8192]
    var wb: [4096]u8 = [0; 4096]
    var s := 0
    var e := pre.Len()
    if e > 0 {
        (mut rb[0..e]).CopyFrom(pre)
    }
    for {
        // The request's first byte: none until a read brings it, or now
        // when the last read brought bytes of this request already
        // (pipelined), so the rest of its head has request_header.
        var started: ?time.Instant = if e > s { some(time.Now(io)) } else { none }
        // Find the head in rb: skip empty lines before it (RFC 9112 2.2),
        // refuse a bare LF, stop after CRLF CRLF. A head that does not fit
        // rb goes on in the arena (the large-header path below).
        var p := HeadPos(s: s, scan: s, skipped: 0, hend: -1, bad: -1)
        var big := false
        for p.hend < 0 && p.bad < 0 {
            p = HeadScan(rb[..], p, e)
            if p.hend >= 0 || p.bad >= 0 {
                break
            }
            if p.skipped + (p.scan - p.s) >= limit {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 431)
                return
            }
            if p.s > 0 {
                k := e - p.s
                for x in 0..k {
                    rb[x] = rb[p.s + x]
                }
                p.scan -= p.s
                e = k
                p.s = 0
                s = 0
            }
            if e == HeadBuf {
                big = true
                break
            }
            n := HeadRead(conn, io, mut rb[e..HeadBuf], o, mut started)
            if n == 0 {
                // The peer closed: between requests that is the normal end;
                // inside a head there is no one to answer.
                return
            }
            e += n
        }
        if !big && p.hend >= 0 && p.skipped + (p.hend - p.s) > limit {
            // A whole head longer than limit, read in one go:
            // 431 for a head (RFC 6585).
            ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 431)
            return
        }
        var hb := "".AsBytes()
        var used := 0
        var skipped := p.skipped
        if big {
            // The large-header path (nginx's large_client_header_buffers):
            // rb is full and holds the head's first HeadBuf bytes. They go
            // into a buffer in the request's arena that starts at twice
            // HeadBuf and doubles up to limit plus HeadBuf, where the head
            // is read on, at most HeadBuf bytes a read, so the bytes after
            // the head (fewer than the last read) fit rb again. Memory
            // follows the bytes that came, never the limit.
            var bb := arena.Slice[u8](2 * HeadBuf) else {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
                return
            }
            for x in 0..e {
                bb[x] = rb[x]
            }
            var be := e
            // limit is clamped so room cannot overflow.
            capped := if limit > MaxHeadRoom { MaxHeadRoom } else { limit }
            room := capped + HeadBuf
            var q := HeadPos(s: 0, scan: p.scan, skipped: p.skipped, hend: -1, bad: -1)
            for q.hend < 0 && q.bad < 0 {
                if be == bb.Len() {
                    if bb.Len() >= room {
                        ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 431)
                        return
                    }
                    grown := if bb.Len() > room / 2 { room } else { bb.Len() * 2 }
                    nb := arena.Slice[u8](grown) else {
                        ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
                        return
                    }
                    (mut nb[0..be]).CopyFrom(bb[0..be])
                    bb = nb
                }
                top := if be + HeadBuf > bb.Len() { bb.Len() } else { be + HeadBuf }
                n := HeadRead(conn, io, mut bb[be..top], o, mut started)
                if n == 0 {
                    return
                }
                be += n
                q = HeadScan(bb[..], q, be)
                if q.hend < 0 && q.bad < 0 && q.skipped + q.scan >= limit {
                    ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 431)
                    return
                }
            }
            if q.bad >= 0 {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 400)
                return
            }
            if q.skipped + q.hend > limit {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 431)
                return
            }
            rest := be - q.hend
            for x in 0..rest {
                rb[x] = bb[q.hend + x]
            }
            s = 0
            e = rest
            hb = bb[0..q.hend]
            used = q.skipped + q.hend
        } else {
            if p.bad >= 0 {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 400)
                return
            }
            cb := arena.Slice[u8](p.hend - p.s) else {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
                return
            }
            for x in 0..p.hend - p.s {
                cb[x] = rb[p.s + x]
            }
            hb = cb[..]
            used = p.skipped + p.hend - p.s
            s = p.hend
        }
        text := rt.StrFrom(hb[..]) else {
            ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 400)
            return
        }
        h := ParseHead(&arena, text, false, skipped) else err {
            match err {
                TooLarge {
                    // A Content-Length past what an int holds: the body's
                    // limit, so 413.
                    ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 413)
                }
                OutOfMemory {
                    ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
                }
                _ {
                    ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 400)
                }
            }
            return
        }
        // The body: what the buffer holds, then reads.
        length := if h.length < 0 { 0 } else { h.length }
        var cont := false
        if (h.chunked || length > 0) && h.minor == 1 {
            match Lookup(h.headers, "expect") {
                some(ex) {
                    cont = EqFold(ex, "100-continue")
                }
                none {}
            }
        }
        if !h.chunked && length > o.body_bytes {
            // A close with unread bytes in the socket resets the
            // connection, and the reset can destroy the 413 before the
            // client reads it; so a body of at most DrainMax is read and
            // dropped first (Go's server does the same up to 256 KiB). A
            // client waiting for 100 Continue sends nothing more.
            if !cont && length <= DrainMax {
                var left := length
                k := if e - s > left { left } else { e - s }
                s += k
                left -= k
                for left > 0 {
                    top := if left > HeadBuf { HeadBuf } else { left }
                    n := BodyRead(conn, io, mut rb[0..top], o)
                    if n == 0 {
                        return
                    }
                    left -= n
                }
            }
            ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 413)
            return
        }
        if cont {
            // RFC 9110 10.1.1: the client waits for this before it sends
            // the body.
            if !CellWrite(conn, io, "HTTP/1.1 100 Continue\r\n\r\n".AsBytes(), o.request_header) {
                return
            }
        }
        // The body is read into a buffer that grows as its bytes arrive
        // (GrowFor: BodyStep, then doubling up to what is still to come),
        // so a declared length or chunk size commits no memory before its
        // bytes do.
        var body := arena.Slice[u8](0) else {
            ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
            return
        }
        var bn := 0
        if h.chunked {
            var st := Chunk(state: CkSize, size: 0, digits: 0, off: 0, meta: 0)
            budget := o.body_bytes
            var fault := 0
            for st.state != CkDone && fault == 0 {
                if s == e {
                    s = 0
                    e = 0
                    n := BodyRead(conn, io, mut rb[0..HeadBuf], o)
                    if n == 0 {
                        return
                    }
                    e = n
                }
                if st.state == CkData {
                    k := if e - s > st.size { st.size } else { e - s }
                    if bn + k > body.Len() {
                        // k is at most HeadBuf, below BodyStep, and at most
                        // st.size, below what the budget leaves, so the new
                        // buffer holds it.
                        body = GrowFor(&arena, body[0..bn], budget - bn) else {
                            fault = 500
                            break
                        }
                    }
                    for x in 0..k {
                        body[bn + x] = rb[s + x]
                    }
                    bn += k
                    s += k
                    st.size -= k
                    st.off += k
                    if st.size == 0 {
                        st.state = CkDataCR
                    }
                } else {
                    k := ChunkStep(mut st, rb[s..e], budget, used, o.header_bytes) else err {
                        match err {
                            TooLarge {
                                fault = 413
                            }
                            _ {
                                fault = 400
                            }
                        }
                        break
                    }
                    s += k
                }
            }
            if fault != 0 {
                ConnErrorReply(conn, io, mut wb, &arena, o.request_header, fault)
                return
            }
        } else {
            for bn < length {
                if s == e && bn == body.Len() {
                    // No room and nothing buffered: read into rb, so the
                    // arena grows only for bytes that came.
                    s = 0
                    e = 0
                    top := if length - bn > HeadBuf { HeadBuf } else { length - bn }
                    n := BodyRead(conn, io, mut rb[0..top], o)
                    if n == 0 {
                        return
                    }
                    e = n
                }
                if bn == body.Len() {
                    body = GrowFor(&arena, body[0..bn], length - bn) else {
                        ConnErrorReply(conn, io, mut wb, &arena, o.request_header, 500)
                        return
                    }
                }
                if s < e {
                    // The bytes after the head that the buffer holds.
                    k := if e - s > body.Len() - bn { body.Len() - bn } else { e - s }
                    for x in 0..k {
                        body[bn + x] = rb[s + x]
                    }
                    s += k
                    bn += k
                    continue
                }
                n := BodyRead(conn, io, mut body[bn..body.Len()], o)
                if n == 0 {
                    return
                }
                bn += n
            }
        }
        req := Request(method: h.method, path: h.path, query: h.query, headers: h.headers, params: List[Header].New(&arena).Done()[..], body: body[0..bn])
        req_ver := if h.minor == 0 { HttpVersion.Http10 } else { HttpVersion.Http11 }
        var res := Response.New(&arena)
        res.head = h.method == "HEAD"
        res.close = h.close
        res.version = req_ver
        res.secure = secure
        link := Link(version: req_ver, cell: some(conn), h2: none, h3: none, h3_port: o.h3_port, io: io, rest: rb[s..e], pace: o.request_header)
        if h.minor == 0 && !h.close {
            // An HTTP/1.0 client keeps the connection only when the reply
            // says so (RFC 9112 9.3, appendix C.2.2).
            res.Header("Connection", "keep-alive") else {
                return
            }
        }
        // Set before the handler, for Flush, and again after it, so a
        // handler's own Response.New cannot clear it: an HTTP/1.0 client
        // gets no chunked framing (RFC 9112 6.1).
        res.old = h.minor == 0
        res.version = req_ver
        res.secure = secure
        var failed := false
        CallHandler(c, rh, ch, plain, relay, &arena, io, &req, mut res, &link) else err {
            match err {
                Status(code) {
                    if res.fl.head {
                        // Too late for a status: the head went out.
                        failed = true
                    } else {
                        // The fields stay (405's Allow, say); the body goes,
                        // and Send writes the framing of the empty body.
                        res.code = code
                        res.data.Clear()
                        res.text.Clear()
                    }
                }
                _ {
                    failed = true
                }
            }
        }
        if CellTaken(conn, io) {
            // Takeover: the connection is the handler's, and so are the
            // bytes after the request.
            return
        }
        if res.fl.failed || res.fl.head && failed {
            // A Flush that failed left the stream in an unknown state, and
            // a flushed head cannot be taken back: either way the body is
            // cut short (no last chunk), the client sees an incomplete
            // reply, and the connection is never reused.
            return
        }
        if !res.fl.head && (failed || res.code < 200 || res.code > 999) {
            // A handler's error, or a status that is not final (a 1xx is
            // interim, RFC 9110 15.2; outside 100 to 999 is no status at
            // all): 500 on this connection, not a panic that ends every
            // connection.
            res.Clear(500)
        }
        res.AdvertiseH3(o.h3_port) else { return }
        if !res.close {
            // A handler's Connection: close ends the connection after this
            // reply (RFC 9112 9.6): no later request on it is served.
            hs := res.headers.Items()
            for i in 0..hs.Len() {
                if EqFold(hs[i].name, "connection") && ConnFlags(hs[i].value) & 1 != 0 {
                    res.close = true
                }
            }
        }
        res.old = h.minor == 0
        if !CellSend(conn, io, mut wb, &res, o.request_header) {
            return
        }
        if res.close {
            return
        }
        arena.Reset()
    }
}
