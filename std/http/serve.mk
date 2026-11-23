// Why: the simplest HTTP/1.1 server: bind an address, call one handler per
// request, with keep-alive and pipelining, and memory bounded per
// connection.
//
// What: Serve, the per-connection loop Conn, HeadScan (the LF scan the
// client and ServeWith also use), ErrorReply, LingerWait and the accept
// Backoff.
//
// How: one task and one arena per connection, the arena reset after each
// response. Each connection task owns an 8 KiB read buffer and a 4 KiB
// write buffer on its own stack, never in the request's arena. More below.

// The head is found in the read buffer by one scan for LF (CRLF only, as
// parse.mk; simd.IndexByte), copied once into the arena and parsed there;
// the bytes after it stay in the buffer for the body and the next,
// pipelined, request. The chunked decoder runs over the same buffer. A head
// that does not fit 8 KiB is read on into a 32 KiB buffer in the request's
// arena (nginx's client header and large header buffers), and a longer one
// is answered 431, as nginx's defaults and Go's 1 MB bound a head.
//
// Not built: an idle and a header timeout per connection (Io has no
// deadline); ServeWith has both.

import std.core
import std.net
import std.rt
import std.simd
import std.time

// HeadBuf and OutBuf are the sizes of a connection's read and write
// buffers, on its task's stack: 8 KiB, as nginx's large header buffer and
// Go's bufio sizes, and 4 KiB. A head that does not fit HeadBuf is read on
// into a HeadMax buffer in the request's arena, and one longer than that is
// answered 431 (nginx's default bound on a head is 32 KiB).
const HeadBuf = 8192
const HeadMax = 32768
const OutBuf = 4096

// DrainMax is the most body bytes Serve reads and drops before it answers
// 413 and closes (Go's maxPostHandlerReadBytes).
const DrainMax = 262144

/// Listens on addr and serves every connection in a task of its own, with
/// keep-alive and pipelining, until the caller's scope is cancelled; then
/// it returns, once every connection's task has ended. Each request is
/// read with limit as its bound (head plus body) and handed to handle with
/// an arena of its own, reset after each response. Every task calls handle
/// with the one c, so C is Share. Chunked request bodies are read whole
/// under limit, and `Expect: 100-continue` is answered before the body is
/// read. Use ServeWith for time limits, TLS, a cap on connections and the
/// other settings. O(bytes served) per
/// connection, with O(fields) per request for the head's parse. Each
/// connection allocates one arena from sys.heap; its read and write
/// buffers are on its task's stack.
///
/// Replies the server makes itself: a handler's Status(code) is answered
/// with that code, the fields it set and an empty body. A code outside 200
/// to 999, or any other handler error, is answered 500, and the connection
/// ends. A handler's `Connection: close` ends the connection after the
/// reply. A malformed request is answered 400, a request over limit 413,
/// and a head over 32 KiB 431, and the connection ends.
///
/// Errors: Serve fails with Listen's error, or with Accept's Closed or
/// InvalidAddress. Another Accept error (out of handles, or an error of the
/// connection being accepted) waits 5 ms and tries again, doubling the wait
/// up to 1 s. Serve has no time limits: a client that sends nothing keeps
/// its connection.
export Serve[C: Share](sys: &System, addr: str, limit: int, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void): Error!void {
    l := try net.Listen(sys.io, addr)
    defer l.Close()
    io := sys.io
    var delay := 0
    scope s := io.Scope() {
        for {
            conn := l.Accept(io) else e {
                match e {
                    Canceled {
                        // The scope's exit joins every connection task,
                        // which the cancel ends at its await.
                        return
                    }
                    Closed {
                        fail e
                    }
                    net.InvalidAddress {
                        fail e
                    }
                    _ {
                        // Out of handles or memory (net.Os), or an error of
                        // the connection being accepted that accept(2)
                        // passes on (Linux: ENETDOWN, EHOSTUNREACH,
                        // ENETUNREACH as net.Unreachable; ECONNRESET as
                        // net.Reset): the listener is still good, so back
                        // off and accept again.
                        delay = Backoff(delay)
                        io.Sleep(core.DurationOf(i64(delay))) else {
                            return
                        }
                        continue
                    }
                }
            }
            delay = 0
            s.go Conn(conn, sys.heap, io, limit, c, handle)
        }
    }
}

// Backoff is the next accept retry delay in nanoseconds after delay: 5 ms
// first, then doubling, clamped at 1 s (Go's Server.Serve).
Backoff(delay: int): int {
    if delay <= 0 {
        return 5_000_000
    }
    if delay >= 500_000_000 {
        return 1_000_000_000
    }
    return delay * 2
}

// Conn serves one connection until the peer closes it, a request asks to
// close it, an error ends it, or the scope is cancelled.
Conn[C: Share](conn: net.Stream, heap: &Heap, io: Io, limit: int, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): Error!void) {
    defer conn.Close()
    var arena := Arena.New(heap) else {
        return
    }
    defer arena.Free()
    var rb: [8192]u8 = [0; 8192]
    var wb: [4096]u8 = [0; 4096]
    var s := 0
    var e := 0
    for {
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
                ErrorReply(&conn, io, mut wb, &arena, 413)
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
            }
            if e == HeadBuf {
                big = true
                break
            }
            n := conn.Read(io, mut rb[e..HeadBuf]) else {
                return
            }
            if n == 0 {
                // The peer closed: between requests that is the normal end;
                // inside a head there is no one to answer.
                return
            }
            e += n
        }
        if !big && p.hend >= 0 && p.skipped + (p.hend - p.s) > limit {
            // A whole head longer than limit, read in one go.
            ErrorReply(&conn, io, mut wb, &arena, 413)
            return
        }
        var hb := "".AsBytes()
        var used := 0
        var skipped := p.skipped
        if big {
            // The large-header path (nginx's large_client_header_buffers):
            // rb is full and holds the head's first HeadBuf bytes. They go
            // into one HeadMax buffer in the request's arena, where the head
            // is read on, at most HeadBuf bytes a read, so the bytes after
            // the head (fewer than the last read) fit rb again.
            var bb := arena.Slice[u8](HeadMax) else {
                ErrorReply(&conn, io, mut wb, &arena, 500)
                return
            }
            for x in 0..e {
                bb[x] = rb[x]
            }
            var be := e
            var q := HeadPos(s: 0, scan: p.scan, skipped: p.skipped, hend: -1, bad: -1)
            for q.hend < 0 && q.bad < 0 {
                if be == HeadMax {
                    ErrorReply(&conn, io, mut wb, &arena, 431)
                    return
                }
                top := if be + HeadBuf > HeadMax { HeadMax } else { be + HeadBuf }
                n := conn.Read(io, mut bb[be..top]) else {
                    return
                }
                if n == 0 {
                    return
                }
                be += n
                q = HeadScan(bb[..], q, be)
                if q.hend < 0 && q.bad < 0 && q.skipped + q.scan >= limit {
                    ErrorReply(&conn, io, mut wb, &arena, 413)
                    return
                }
            }
            if q.bad >= 0 {
                ErrorReply(&conn, io, mut wb, &arena, 400)
                return
            }
            if q.skipped + q.hend > limit {
                ErrorReply(&conn, io, mut wb, &arena, 413)
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
                ErrorReply(&conn, io, mut wb, &arena, 400)
                return
            }
            cb := arena.Slice[u8](p.hend - p.s) else {
                ErrorReply(&conn, io, mut wb, &arena, 500)
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
            ErrorReply(&conn, io, mut wb, &arena, 400)
            return
        }
        h := ParseHead(&arena, text, false, skipped) else err {
            match err {
                TooLarge {
                    ErrorReply(&conn, io, mut wb, &arena, 413)
                }
                OutOfMemory {
                    ErrorReply(&conn, io, mut wb, &arena, 500)
                }
                _ {
                    ErrorReply(&conn, io, mut wb, &arena, 400)
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
        if !h.chunked && length > limit - used {
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
                    n := conn.Read(io, mut rb[0..top]) else {
                        return
                    }
                    if n == 0 {
                        return
                    }
                    left -= n
                }
            }
            ErrorReply(&conn, io, mut wb, &arena, 413)
            return
        }
        if cont {
            // RFC 9110 10.1.1: the client waits for this before it sends
            // the body.
            conn.WriteAll(io, "HTTP/1.1 100 Continue\r\n\r\n".AsBytes()) else {
                return
            }
        }
        // The body is read into a buffer that grows as its bytes arrive
        // (GrowFor: BodyStep, then doubling up to what is still to come),
        // so a declared length or chunk size commits no memory before its
        // bytes do.
        var body := arena.Slice[u8](0) else {
            ErrorReply(&conn, io, mut wb, &arena, 500)
            return
        }
        var bn := 0
        if h.chunked {
            var st := Chunk(state: CkSize, size: 0, digits: 0, off: 0, meta: 0)
            budget := limit - used
            var fault := 0
            for st.state != CkDone && fault == 0 {
                if s == e {
                    s = 0
                    e = 0
                    n := conn.Read(io, mut rb[0..HeadBuf]) else {
                        return
                    }
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
                    k := ChunkStep(mut st, rb[s..e], budget, used, budget) else err {
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
                ErrorReply(&conn, io, mut wb, &arena, fault)
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
                    n := conn.Read(io, mut rb[0..top]) else {
                        return
                    }
                    if n == 0 {
                        return
                    }
                    e = n
                }
                if bn == body.Len() {
                    body = GrowFor(&arena, body[0..bn], length - bn) else {
                        ErrorReply(&conn, io, mut wb, &arena, 500)
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
                n := conn.Read(io, mut body[bn..body.Len()]) else {
                    return
                }
                if n == 0 {
                    return
                }
                bn += n
            }
        }
        req := Request(method: h.method, path: h.path, query: h.query, headers: h.headers, params: List[Header].New(&arena).Done()[..], body: body[0..bn])
        var res := Response.New(&arena)
        res.head = h.method == "HEAD"
        res.close = h.close
        if h.minor == 0 && !h.close {
            // An HTTP/1.0 client keeps the connection only when the reply
            // says so (RFC 9112 9.3, appendix C.2.2).
            res.Header("Connection", "keep-alive") else {
                return
            }
        }
        handle(c, &arena, io, &req, mut res) else err {
            match err {
                Status(code) {
                    // The fields stay (405's Allow, say); the body goes, and
                    // Send writes the framing of the empty body.
                    res.code = code
                    res.data.Clear()
                    res.text.Clear()
                }
                _ {
                    res = Response.New(&arena)
                    res.code = 500
                    res.close = true
                }
            }
        }
        if res.code < 200 || res.code > 999 {
            // Not a final status (a 1xx is interim, RFC 9110 15.2; outside
            // 100 to 999 is no status at all): the handler's bug answers 500
            // on this connection, not a panic that ends every connection.
            res = Response.New(&arena)
            res.code = 500
            res.close = true
        }
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
        // Set last, so a handler's own Response.New cannot clear it: an
        // HTTP/1.0 client gets no chunked framing (RFC 9112 6.1).
        res.old = h.minor == 0
        var w := conn.Writer(io, mut wb[..])
        res.Send(io, mut w) else {
            return
        }
        if res.close {
            return
        }
        arena.Reset()
    }
}

// HeadPos is a scan for the end of a head: s is where the head starts
// (after skipped bytes of empty lines), scan the next byte to look at;
// hend is the index after its CRLF CRLF once found, bad the offset of a
// bare CR or LF (counted from the request's first byte), else -1.
struct HeadPos {
    s: int
    scan: int
    skipped: int
    hend: int
    bad: int
}

// HeadScan goes on with p over b[p.scan..e]: empty lines before the head
// move p.s (RFC 9112 2.2), a bare LF or a CR without its LF there is bad,
// and CRLF CRLF ends the head. It stops at e or at either outcome; one
// pass, each line's LF found by simd.IndexByte and only the bytes beside it
// looked at again (TestSTDVECTOR_HttpScansDiff holds it to a byte loop).
HeadScan(b: []u8, var p: HeadPos, e: int): HeadPos {
    for p.scan < e {
        if p.scan == p.s && b[p.scan] == CR {
            if p.scan + 1 >= e {
                break
            }
            if b[p.scan + 1] != LF {
                p.bad = p.skipped + 1
                break
            }
            p.s += 2
            p.skipped += 2
            p.scan = p.s
            continue
        }
        // The next LF, 16 or more bytes a step (simd.IndexByte, memchr's
        // shape); every byte before it is head text, checked later.
        k := simd.IndexByte(b[p.scan..e], LF) else {
            p.scan = e
            break
        }
        p.scan += k
        if p.scan == p.s || b[p.scan - 1] != CR {
            p.bad = p.skipped + p.scan - p.s
            break
        }
        if p.scan - p.s >= 3 && b[p.scan - 2] == LF {
            p.hend = p.scan + 1
            break
        }
        p.scan += 1
    }
    return p
}

// ErrorReply answers code with an empty body and Connection: close, then,
// for a 413 or 431, closes lingering (below); a failure to send it ends the
// connection anyway.
ErrorReply(conn: &net.Stream, io: Io, wb: mut [4096]u8, a: &Arena, code: int) {
    var res := Response.New(a)
    res.code = code
    res.close = true
    var w := conn.Writer(io, mut wb[..])
    res.Send(io, mut w) else {
        return
    }
    if code != 413 && code != 431 {
        return
    }
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

// A 413 or 431 is followed by a lingering close (nginx's
// lingering_close): the server stopped reading a request too large for it,
// and a close with unread bytes resets the connection, which can destroy
// the reply before the client reads it (RFC 9112 9.6). So the reply is
// sent, the write side shut, and what the client still sends is read and
// dropped, up to LingerBytes, until it closes, LingerIdle nanoseconds pass
// with no byte, or LingerTime nanoseconds pass in all (nginx's
// lingering_time, 30 s by default), so a client that trickles a byte
// before each idle limit cannot hold the connection, and a ServeWith
// permit, for long. Other error replies close at once: a lingering close
// makes the server the side that closes first, which leaves a TIME_WAIT
// entry on the server per connection (measured under
// FuzzStdHttp_ServeWith: 811 entries on one server port in 30 s).
const LingerBytes = 262144
const LingerIdle = 500000000
const LingerTime = 30000000000

// LingerWait is how long the next lingering read may wait, in nanoseconds:
// LingerIdle, or what is left of LingerTime since t0 when that is less; 0
// or less when LingerTime has passed.
LingerWait(io: Io, t0: time.Instant): i64 {
    left := i64(LingerTime) - time.Now(io).Since(t0).Nanos()
    return if left < i64(LingerIdle) { left } else { i64(LingerIdle) }
}
