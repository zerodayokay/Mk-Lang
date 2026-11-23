// Why: a client reply's body can be larger than memory, so the reply is
// handed back holding its connection, and the body is read as the caller
// asks for it.
//
// What: Exchange with Body, Close and Abandon; Line, one connection with
// its read buffer and the reply's framing state; and the Line's readers
// (LineFill, LineRead, LineTake) and closers (TakeConn, CloseBounded).
//
// How: the connection and its 8 KiB read buffer live in a Line under a
// Mutex in the arena Do was given, and the body is read from the Line by
// its framing (Content-Length, chunked, or to the connection's end, RFC
// 9112 6.3) through a Reader in the same arena.

// A content decoder's Reader runs over that Reader when the reply is
// encoded (compress.Content.wrap). The Exchange holds only references,
// never a view of itself, so the Readers can outlive any one call (Mk has
// no self-reference).

import std.core
import std.net
import std.raw
import std.task
import std.time

// The ways a reply's body is delimited (RFC 9112 6.3).
const BodyNone = 0
const BodyLength = 1
const BodyChunked = 2
const BodyClose = 3

// LineBuf is a connection's read buffer: 8 KiB, as Go's bufio and nginx's
// header buffer.
const LineBuf = 8192

// Line is one connection with its read buffer and its reply's framing
// state: rb[s..e] holds bytes read and not yet used. done is true once the
// body was read to its end, failed once a read or a parse failed (then
// every later read fails, and the connection is never pooled), reuse while
// the connection may serve another request. meta bounds a chunked body's
// extensions and trailers (the client's header_bytes); coded: a decoder
// undoes the body, so its framing length is not its decoded size.
struct Line {
    slot: raw.Atomic[*Slot]
    conn: ?net.Conn
    rb: [8192]u8
    s: int
    e: int
    mode: int
    left: int
    ck: Chunk
    meta: int
    done: bool
    failed: bool
    reuse: bool
    fresh: bool
    coded: bool
}

// NewLine is an empty Line, on no slot (mem_slot.mk), its connection none.
NewLine(): Line {
    return Line(slot: raw.Atomic[*Slot].New(raw.Null[Slot]()), conn: none, rb: [0; 8192], s: 0, e: 0, mode: BodyNone, left: 0, ck: Chunk(state: CkSize, size: 0, digits: 0, off: 0, meta: 0), meta: MetaMax, done: true, failed: false, reuse: true, fresh: true, coded: false)
}

/// A reply that Client.Do read: the status, the header fields, and the
/// body through Body. It holds the connection until Close or Abandon. It
/// is linear, and it borrows its arena and the client.
///
/// When the client decoded the body, headers leaves out Content-Encoding
/// and Content-Length, which describe the encoded bytes.
export linear struct Exchange {
    /// The reply's status code, such as 200.
    export status: int
    /// The reply's header fields in the order received.
    export headers: []Header
    client: &Client
    line: &Mutex[Line]
    reader: mut Reader
    io: Io
    key: Origin
}

// ExFill is an Exchange's body Reader's fill: the decoded or raw body
// Reader in the arena. At the body's end the raw body is read on to its
// framing's end (SettleLine), so a decoder that stops at its own end still
// leaves the connection fit for the pool.
ExFill(x: mut Exchange, io: Io, dst: mut []u8): ReadError!int {
    n := try x.reader.Read(dst)
    if n == 0 && dst.Len() > 0 {
        try SettleLine(x.line, io)
    }
    return n
}

// SettleMax is the most raw body bytes SettleLine reads past a decoder's
// end: a zlib stream's last chunk and trailers fit it many times over.
const SettleMax = 4096

// SettleLine reads the raw body after a decoder's end, up to SettleMax
// bytes, to its framing's end: a zlib stream ends at its trailer and never
// reads the chunked body's last chunk (compress's Inflater.NewZlib), which
// left the Line not done and the connection closed instead of pooled.
// Nothing for a body already at its end or delimited by the connection's
// end (never pooled). More than SettleMax bytes after the decoder's end
// leave the Line not done, so the connection is closed, not pooled. A
// failed read or framing is ReadError and marks the Line failed (LineFill).
// O(SettleMax); allocates nothing.
SettleLine(m: &Mutex[Line], io: Io): ReadError!void {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    settled := g.Ref().done || g.Ref().failed || g.Ref().mode == BodyClose
    g.Unlock()
    if settled {
        return
    }
    var buf: [512]u8 = [0; 512]
    var total := 0
    for total < SettleMax {
        k := try LineFill(m, io, mut buf[..])
        if k == 0 {
            return
        }
        total += k
    }
}

const ExReads = core.ReadPortMut[Exchange].New(ExFill)

/// A Reader of the body, with buf as its buffer. O(1); allocates nothing.
///
/// The body is decoded when the reply's Content-Encoding names one coding
/// that is among the client's decoders; with none, another, or several
/// codings, the bytes come as sent. Read returns 0 at the body's end, never
/// a byte of the next reply. Only the reply's framing limits the body, so
/// bound a streamed body's decoded size yourself. A failed read (the
/// connection, the framing, the decoder, or a cancel) is ReadError, and
/// every later read fails too; the connection then never returns to the
/// pool.
export Exchange.Body(mut self, buf: mut []u8): Reader from self, buf {
    io := self.io
    return (&ExReads).Reader(io, self, buf)
}

/// Ends the exchange. When the body was read to its end and the reply let
/// the connection stay open, the connection goes back to the client's pool
/// for the next request to its origin; otherwise it is closed. O(1);
/// allocates nothing.
///
/// Closing a secure connection speaks to the peer, bounded by the client's
/// close limit: time.TimedOut past it, a cancel after it passed included,
/// and Canceled under a cancelled scope before. After a failed body read,
/// Close fails with ReadError and the connection is closed. On any error
/// the connection is released anyway and the Exchange stays the caller's:
/// write `onerr x.Abandon()` with `try x.Close(io)`.
export Exchange.Close(self, io: Io): Error!void {
    failed := LineFailed(self.line, self.io)
    match TakeConn(self.line, self.io, true) {
        some(conn) {
            if e := CloseBounded(conn, io, self.client.limits.close) {
                fail e
            }
        }
        none {}
    }
    if failed {
        fail ReadError
    }
    Exchange(status: _, headers: _, client: _, line: _, reader: _, io: _, key: _) := self
}

// LineFailed: a read or a parse of the Line failed, so the Line is
// never pooled.
LineFailed(m: &Mutex[Line], io: Io): bool {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    failed := g.Ref().failed
    g.Unlock()
    return failed
}

// LineOver: the reply's body, not decoded, is framed by a Content-Length
// over limit, so Fetch refuses it before reading a byte.
LineOver(m: &Mutex[Line], io: Io, limit: i64): bool {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    l := g.Ref()
    over := l.mode == BodyLength && !l.coded && i64(l.left) > limit
    g.Unlock()
    return over
}

/// Ends the exchange and closes its connection without speaking to the
/// peer. Use it on error paths. Never blocks or fails. O(1); allocates
/// nothing.
export Exchange.Abandon(self) {
    DropConn(TakeConn(self.line, self.io, false))
    Exchange(status: _, headers: _, client: _, line: _, reader: _, io: _, key: _) := self
}

// TakeConn ends the Line's hold on its connection. A pooled connection stays in
// its slot, idle, when keep is asked and the body was read to its end with
// nothing left over, nothing failed and the reply let it stay open; otherwise
// the connection is returned for the caller to close (none when there is none).
// The Line's lock is taken under a NoCancel Io masked here, which no scope's
// cancel reaches, and whose cancellation check goes on in a dead Sim run, under
// a freed Recorder or Replayer, and under a Replayer that cannot replay it, so
// it cannot fail: a Line is locked only for one read at a time, so the lock
// never waits.
TakeConn(m: &Mutex[Line], io: Io, keep: bool): ?net.Conn {
    var g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    l := g.Mut()
    ok := keep && l.done && !l.failed && l.reuse && l.s == l.e
    own := Replace(mut l.conn, none)
    s := l.slot.Swap(raw.Null[Slot]())
    g.Unlock()
    if !raw.IsNull(s) {
        DropConn(own)
        return Release(SlotOf(s), io, ok)
    }
    return own
}

// LineConnReadFrom reads from a Line's slot or direct connection into dst;
// 0 at its end. A byte read marks the Line touched. Taking the disjoint
// fields instead of mut Line keeps dst (which may slice l.rb) disjoint from
// the other arguments under R5.
LineConnReadFrom(slot: &raw.Atomic[*Slot], conn: &?net.Conn, fresh: mut bool, io: Io, dst: mut []u8): Error!int {
    var n := 0
    sp := slot.Load()
    if !raw.IsNull(sp) {
        g := try SlotOf(sp).st.Lock(io)
        if c := &g.Ref().conn {
            n = c.Read(io, dst) else e {
                g.Unlock()
                fail e
            }
            g.Unlock()
        } else {
            g.Unlock()
            fail Closed
        }
    } else if c := &*conn {
        n = try c.Read(io, dst)
    } else {
        fail Closed
    }
    if n > 0 {
        *fresh = false
    }
    return n
}

// LineConnRead reads from the Line's connection, in its slot or its own,
// into dst; 0 at its end. A byte read marks the Line touched.
LineConnRead(l: mut Line, io: Io, dst: mut []u8): Error!int {
    return try LineConnReadFrom(&l.slot, &l.conn, mut l.fresh, io, dst)
}

// CloseBounded closes conn, its peer-facing part bounded by limit: Canceled
// under a cancelled scope, time.TimedOut past limit; on either the
// connection is abandoned, so nothing leaks.
CloseBounded(conn: net.Conn, io: Io, limit: time.Duration): ?Error {
    var expired := false
    var err: ?net.Error = none
    var left: ?net.Conn = some(conn)
    scope s := io.Scope() {
        s.CancelAfter(limit)
        if c := Replace(mut left, none) {
            c.Close(io) else e {
                err = some(e)
                canceled := match e {
                    Canceled { true }
                    _ { false }
                }
                expired = task.LimitHit(&s, canceled)
                c.Abandon()
            }
        }
    }
    DropConn(left)
    if e := err {
        if expired {
            return some(time.TimedOut)
        }
        return some(e)
    }
    return none
}

// DropConn releases c, if it holds a connection.
DropConn(c: ?net.Conn) {
    match c {
        some(x) {
            x.Abandon()
        }
        none {}
    }
}

// LineFill is the raw body Reader's fill: one locked read by the framing.
// Any failure marks the Line failed, so every later read fails and the
// connection is never pooled. The lock is taken under a
// NoCancel Io, so a read in a cancelled scope reaches LineRead, whose
// connection read is Canceled and marks the Line failed, rather than
// failing at the lock and leaving the Line as it was.
LineFill(m: &Mutex[Line], io: Io, dst: mut []u8): ReadError!int {
    var g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    l := g.Mut()
    n := LineRead(l, io, dst) else {
        l.failed = true
        l.reuse = false
        g.Unlock()
        fail ReadError
    }
    g.Unlock()
    return n
}

const LineReads = core.ReadPort[Mutex[Line]].New(LineFill)

// LineRead reads at most dst.Len() body bytes by l's framing; 0 at the
// body's end. A body cut short is BadRequest; a chunked body's framing is
// checked as ChunkStep checks it, its extensions and trailers bounded by
// l.meta, the client's header_bytes.
LineRead(l: mut Line, io: Io, dst: mut []u8): Error!int {
    if l.failed {
        fail ReadError
    }
    if l.done || dst.Len() == 0 {
        return 0
    }
    if l.mode == BodyLength {
        k := if dst.Len() > l.left { l.left } else { dst.Len() }
        n := try LineTake(l, io, mut dst[0..k])
        if n == 0 {
            fail BadRequest(offset: l.left)
        }
        l.left -= n
        if l.left == 0 {
            l.done = true
        }
        return n
    }
    if l.mode == BodyClose {
        n := try LineTake(l, io, dst)
        if n == 0 {
            l.done = true
        }
        return n
    }
    for {
        if l.ck.state == CkDone {
            l.done = true
            return 0
        }
        if l.ck.state == CkData {
            k := if dst.Len() > l.ck.size { l.ck.size } else { dst.Len() }
            n := try LineTake(l, io, mut dst[0..k])
            if n == 0 {
                fail BadRequest(offset: l.ck.off)
            }
            l.ck.size -= n
            l.ck.off += n
            if l.ck.size == 0 {
                l.ck.state = CkDataCR
            }
            return n
        }
        if l.s == l.e {
            try LineFillBuf(l, io)
        }
        k := try ChunkStep(mut l.ck, l.rb[l.s..l.e], MaxBody, 0, l.meta)
        l.s += k
    }
}

// MaxBody bounds a streamed body's chunk sizes: the largest int, so a size
// with more hex digits than fit is TooLarge before it overflows.
const MaxBody = 9223372036854775807

// MetaMax is a new Line's bound on chunk extensions and trailers, the
// default head limit, 1 MiB; Client.Exchange sets the client's own
// header_bytes.
const MetaMax = 1048576

// LineTake copies buffered bytes into dst, or reads into dst from the
// connection when none are buffered; 0 at the connection's end.
LineTake(l: mut Line, io: Io, dst: mut []u8): Error!int {
    if l.s < l.e {
        k := if dst.Len() > l.e - l.s { l.e - l.s } else { dst.Len() }
        (mut dst[0..k]).CopyFrom(l.rb[l.s..l.s + k])
        l.s += k
        return k
    }
    return try LineConnRead(l, io, dst)
}

// LineFillBuf reads more of the connection into the empty buffer; the end
// of the connection there is a body cut short.
LineFillBuf(l: mut Line, io: Io): Error!void {
    l.s = 0
    l.e = 0
    n := try LineConnReadFrom(&l.slot, &l.conn, mut l.fresh, io, mut l.rb[..])
    if n == 0 {
        fail BadRequest(offset: l.ck.off)
    }
    l.e = n
}
