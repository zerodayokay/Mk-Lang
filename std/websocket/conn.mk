// Why: after the opening handshake, one WebSocket connection is shared by a
// task that receives and tasks that send, and must survive cancels,
// deadlines and a misbehaving peer without losing or tearing a frame
// (RFC 6455 sections 5 and 7).
//
// What: Conn with Recv, Send, Ping, Close, Abandon and Protocol, and the
// shared State on the heap the connection was made with.
//
// How: four locks and the rules below.
// - Lock order: rd, then wl, then wr, then sh. wr is taken only by the
//   holder of wl, so it is never waited on; sh is never held across I/O.
//   No two tasks wait on each other's lock.
// - Halves: one Recv at a time holds rd for its whole call (a second Recv
//   waits); a writer (Send, Ping, Close, a reply) holds wl for one whole
//   frame, so frames are never interleaved and one task receives while
//   others send.
// - Repeatable Recv: every byte read and not yet given to a caller stays in
//   rd: a partial header in the read-ahead, the frame in progress (its
//   header, the bytes left, the mask phase), a fragmented message on the
//   heap, a control frame's payload. A Recv interrupted by a cancel or a
//   deadline loses nothing, and so does one whose arena is full
//   (OutOfMemory before it consumes anything); the next Recv continues.
// - Owed pongs: a Ping read by Recv adds one to sh.owed and keeps its
//   payload (RFC 6455 5.5.3 lets one Pong answer several Pings). Recv then
//   tries wl without waiting; a writer that holds wl sends what is owed
//   before its frame, and every holder of wl looks at owed again after it
//   releases wl. So a pong is never lost: Recv's attempt fails only while
//   another task holds wl, and that task's look comes after Recv's add.
//   More than pending_pongs owed fails the connection with
//   Protocol(1008): the peer pings while it does not read.
// - Failed: an error of a write or a close (a cancel included), or any
//   error of Recv but Canceled and a full arena, stores the error in
//   sh.fault, sends a Close frame with the status the error names when wl
//   is free (best effort, within the close limit), and shuts the transport
//   down; every later call but Abandon reports the stored error, and a call
//   blocked on the transport in another task returns at once. The fault is
//   stored before the lock the failing call holds is released: a writer
//   stores it holding wl, Recv holding rd. Every holder of wl looks at
//   sh.fault after it takes wl, and Recv after it takes rd and before each
//   frame header, so no frame follows this side's Close frame or a partial
//   frame (RFC 6455 5.5.1), and nothing more is read from the peer once the
//   connection failed (7.1.7). A peer's Close is stored as PeerClosed when
//   Recv reads it, before the echo waits for wl. A write that fails marks
//   the write half torn, and a torn half writes no frame again.
// - Replies: a Pong or Close frame Recv writes is written with the
//   cancellation masked and within the close limit, so a cancel never
//   leaves half a frame on the wire and a peer that stops reading cannot
//   hold Recv past the limit.

import std.net
import std.simd
import std.sync
import std.task
import std.time

// ReadAhead is the read-ahead's size, which is also the most handshake
// bytes it holds.
const ReadAhead: int = HeadMax

// WriteChunk is the most payload bytes masked per write: a frame's header
// and a chunk go out in one WriteAll.
const WriteChunk: int = 16384

// KeyPool is the bytes of masking keys fetched per io.Random call: 64
// keys, each used once.
const KeyPool: int = 256

// Direct is the payload left from which Recv reads into the message
// itself, past the read-ahead.
const Direct: int = 8192

// ReadHalf is the receiving direction: the bytes read ahead
// (buf[start..have]), the frame in progress, a data message in progress
// (msg_op its opcode, 0 when none; msg its bytes so far, on the heap;
// ready when it is whole and waits for an arena), and a control frame's
// payload so far.
struct ReadHalf {
    buf: [ReadAhead]u8
    start: int
    have: int
    in_frame: bool
    head: Head
    left: int
    phase: int
    msg_op: u8
    msg: HeapList[u8]
    ready: bool
    ctl: [MaxControl]u8
    ctl_n: int
}

// WriteHalf is the sending direction's buffer: a frame's header and a masked
// chunk, the masking keys not yet used (keys[used..]), and whether a write
// failed, leaving an unknown prefix of a frame on the wire (rule Failed).
struct WriteHalf {
    buf: [MaxHead + WriteChunk]u8
    keys: [KeyPool]u8
    used: int
    torn: bool
}

// Shared is what both directions read: the failure every later call reports,
// whether a Close frame was sent, and the pongs owed (rule Owed pongs).
struct Shared {
    fault: ?Error
    close_sent: bool
    owed: int
    pong: [MaxControl]u8
    pong_n: int
}

// State is a connection's state on its heap, the limits it copied, and
// the subprotocol the server chose (empty for none), written once by Dial
// or Accept before the Conn exists and only read after.
struct State {
    rd: Mutex[ReadHalf]
    wl: sync.Semaphore
    wr: Mutex[WriteHalf]
    sh: Mutex[Shared]
    protocol: HeapList[u8]
    client: bool
    message_bytes: int
    frame_bytes: int
    pending_pongs: int
    close_limit: time.Duration
}

// State.New is the state of a connection on the client side or not, with
// o's limits, whose read-ahead starts with rest (the bytes a server read
// past the request, at most ReadAhead).
State.New(h: &Heap, client: bool, o: &Options, rest: []u8): State from h {
    var r := ReadHalf(buf: [0; ReadAhead], start: 0, have: rest.Len(), in_frame: false,
        head: Head(fin: false, op: 0, masked: false, key: [0; 4], len: 0, size: 0), left: 0, phase: 0, msg_op: 0,
        msg: HeapList[u8].New(h.Alloc()), ready: false, ctl: [0; MaxControl], ctl_n: 0)
    (mut r.buf[0..rest.Len()]).CopyFrom(rest)
    return State(rd: Mutex[ReadHalf].New(r), wl: sync.Semaphore.New(1),
        wr: Mutex[WriteHalf].New(WriteHalf(buf: [0; MaxHead + WriteChunk], keys: [0; KeyPool], used: KeyPool, torn: false)),
        sh: Mutex[Shared].New(Shared(fault: none, close_sent: false, owed: 0, pong: [0; MaxControl], pong_n: 0)),
        protocol: HeapList[u8].New(h.Alloc()), client: client, message_bytes: o.message_bytes, frame_bytes: o.frame_bytes, pending_pongs: o.pending_pongs,
        close_limit: o.close)
}

// FreeState frees the state in b and the message buffer it holds.
FreeState(b: Box[State], h: &Heap) {
    b.Take(h.Alloc()).Drop(h)
}

// State.Drop frees the message buffer of a state not in a box.
State.Drop(self, h: &Heap) {
    State(rd, wl, wr, sh, protocol, ..) := self
    _ = wl
    _ = sh.Into()
    _ = wr.Into()
    protocol.Free(h.Alloc())
    ReadHalf(msg, ..) := rd.Into()
    msg.Free(h.Alloc())
}

/// A WebSocket connection (RFC 6455, no extensions) made by Dial or
/// Accept. Linear, Send and Share: tasks share it by reference, one
/// receiving while others send. Each frame is written whole under a write
/// lock, so frames of two senders never interleave. A client masks its
/// frames with keys from io.Random. It keeps its heap and its net.Conn,
/// never the request, URL or Options it was made from. Close or Abandon
/// releases it; it has no Free.
export linear mut view struct Conn {
    conn: net.Conn
    heap: &Heap
    box: Box[State]
}

// Wrap makes the Conn over conn once its opening handshake is done, on
// the client side or not, with o's limits; rest is what was read past the
// handshake (at most ReadAhead bytes). OutOfMemory leaves conn the
// caller's. O(rest); allocates the state on h.
Wrap(h: &Heap, conn: net.Conn, client: bool, o: &Options, rest: []u8): OutOfMemory!Conn from h, conn {
    st := State.New(h, client, o, rest)
    b := Box[State].New(h.Alloc(), st) else e {
        st.Drop(h)
        fail e
    }
    return Conn(conn: conn, heap: h, box: b)
}

// State.Check fails with the stored failure once the connection failed
// (rule Failed). sh is held for a load, so its lock is taken with the
// cancellation masked.
State.Check(&self, io: Io): Error!void {
    g := self.sh.Lock(io.NoCancel()) else e {
        fail e
    }
    if f := g.Ref().fault {
        g.Unlock()
        fail f
    }
    g.Unlock()
}

// CodeFor is the close status sent for a failure, 0 for none: a violation
// names its own, a message over the limit is 1009, and a peer's close, a
// lost transport or a cancel send nothing.
CodeFor(e: &Error): u16 {
    return match *e {
        Protocol(code) { code }
        TooLarge { CloseTooBig }
        _ { 0 }
    }
}

// State.Fail stores e as the connection's failure (State.Store) and ends the
// connection (State.Finish): every call reports the stored failure from now on
// (rule Failed). A writer calls it holding wl, so its own Close frame cannot
// follow the partial frame it left; Recv stores first, holding rd, and finishes
// after it releases rd.
State.Fail(&self, io: Io, conn: &net.Conn, e: Error): Error {
    (f, code) := self.Store(io, e)
    self.Finish(io, conn, code)
    return f
}

// State.Store stores e as the connection's failure unless one is stored:
// the stored failure, and the close status this side is to send for it (0
// for none: CodeFor's 0, or a Close frame already sent or decided).
State.Store(&self, io: Io, e: Error): (f: Error, code: u16) {
    var g := self.sh.Lock(io.NoCancel()) else {
        return (f: e, code: 0)
    }
    if g.Ref().fault.IsNone() {
        g.Mut().fault = some(e)
    }
    f := g.Ref().fault else { e }
    code := CodeFor(&f)
    send := code != 0 && !g.Ref().close_sent
    if send {
        g.Mut().close_sent = true
    }
    g.Unlock()
    return (f: f, code: if send { code } else { 0 })
}

// State.Finish sends a Close frame with code (none for 0) when wl is free,
// best effort within the close limit, and shuts the transport down so a
// call blocked on it in another task returns (rule Failed).
State.Finish(&self, io: Io, conn: &net.Conn, code: u16) {
    q := io.NoCancel()
    if code != 0 && self.wl.TryAcquire(1) {
        var body: [2]u8 = [Trunc[u8](code >> 8), Trunc[u8](code)]
        self.WriteWithin(conn, q, OpClose, body[..]) else {}
        self.wl.Release(1)
    }
    conn.Shutdown(q, net.Shutdown.Both) else {}
}

// State.WriteWithin writes one frame, the caller holding wl, within the
// close limit: a scope opened from io, so a NoCancel io still gives up at
// the limit, with time.TimedOut (rule Replies).
State.WriteWithin(&self, conn: &net.Conn, io: Io, op: u8, data: []u8): net.Error!void {
    scope s := io.Scope() {
        s.CancelAfter(self.close_limit)
        self.WriteFrame(conn, io, op, data) else e {
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            if task.LimitHit(&s, canceled) {
                fail time.TimedOut
            }
            fail e
        }
    }
}

// State.WriteFrame writes one final frame of op over data, the caller
// holding wl: masked with a fresh key on the client side (RFC 6455 5.3),
// in chunks of WriteChunk; on the server side small frames go out in one
// WriteAll and large ones as the header and the data. The cancellation
// of io applies (a cancelled write leaves an unknown prefix: the half is
// torn, and the caller fails the connection, rule Failed). A torn half writes
// nothing and fails with Closed.
State.WriteFrame(&self, conn: &net.Conn, io: Io, op: u8, data: []u8): net.Error!void {
    var g := self.wr.Lock(io.NoCancel()) else e {
        fail e
    }
    WriteLocked(g.Mut(), self.client, conn, io, op, data) else e {
        g.Unlock()
        fail e
    }
    g.Unlock()
}

// WriteLocked is WriteFrame under wr.
WriteLocked(w: mut WriteHalf, client: bool, conn: &net.Conn, io: Io, op: u8, data: []u8): net.Error!void {
    if w.torn {
        fail Closed
    }
    WriteBytes(w, client, conn, io, op, data) else e {
        // A failed WriteAll sent an unknown prefix (net.Conn.WriteAll's
        // prefix class); a failed key fetch sent nothing, but the
        // connection fails either way.
        w.torn = true
        fail e
    }
}

// WriteBytes is WriteLocked's frame, its half not torn.
WriteBytes(w: mut WriteHalf, client: bool, conn: &net.Conn, io: Io, op: u8, data: []u8): net.Error!void {
    n := data.Len()
    if !client {
        hn := PutHead(mut w.buf[..], op, n, false, [0; 4])
        if n <= WriteChunk {
            (mut w.buf[hn..hn + n]).CopyFrom(data)
            try conn.WriteAll(io, w.buf[0..hn + n])
            return
        }
        try conn.WriteAll(io, w.buf[0..hn])
        try conn.WriteAll(io, data)
        return
    }
    if w.used == KeyPool {
        // The keys come from the OS's random source (5.3: unpredictable);
        // masked, so a cancel cannot leave the pool half filled.
        io.NoCancel().Random(mut w.keys[..]) else {
            fail Canceled
        }
        w.used = 0
    }
    key: [4]u8 = [w.keys[w.used], w.keys[w.used + 1], w.keys[w.used + 2], w.keys[w.used + 3]]
    w.used += 4
    var off := PutHead(mut w.buf[..], op, n, true, key)
    var at := 0
    var phase := 0
    for {
        k := if n - at < WriteChunk { n - at } else { WriteChunk }
        (mut w.buf[off..off + k]).CopyFrom(data[at..at + k])
        phase = Mask(mut w.buf[off..off + k], key, phase)
        try conn.WriteAll(io, w.buf[0..off + k])
        at += k
        off = 0
        if at >= n {
            return
        }
    }
}

// State.TakeOwed takes the owed pong's payload into out, when one is owed:
// its length, or -1 when none is.
State.TakeOwed(&self, io: Io, out: mut [MaxControl]u8): Canceled!int {
    var g := try self.sh.Lock(io.NoCancel())
    if g.Ref().owed == 0 {
        g.Unlock()
        return -1
    }
    n := g.Ref().pong_n
    (mut out[0..n]).CopyFrom(g.Ref().pong[0..n])
    g.Mut().owed = 0
    g.Unlock()
    return n
}

// State.SendOwed sends the owed pong, if one is, the caller holding wl
// (rules Owed pongs and Replies).
State.SendOwed(&self, conn: &net.Conn, io: Io): net.Error!void {
    var p: [MaxControl]u8 = [0; MaxControl]
    n := try self.TakeOwed(io, mut p)
    if n >= 0 {
        try self.WriteWithin(conn, io.NoCancel(), OpPong, p[0..n])
    }
}

// State.Owes reports whether a pong is owed.
State.Owes(&self, io: Io): bool {
    g := self.sh.Lock(io.NoCancel()) else {
        return false
    }
    o := g.Ref().owed > 0
    g.Unlock()
    return o
}

// State.Answer sends what is owed while wl is free, looking again after each
// release (rule Owed pongs). Fails with the stored failure once the connection
// failed (nothing is sent then); a failed pong fails the connection with its
// error, stored holding wl (rule Failed).
State.Answer(&self, conn: &net.Conn, io: Io): Error!void {
    for self.Owes(io) {
        if !self.wl.TryAcquire(1) {
            // The holder of wl looks at owed after its release.
            return
        }
        self.Check(io) else e {
            self.wl.Release(1)
            fail e
        }
        self.SendOwed(conn, io) else e {
            f := self.Fail(io, conn, e)
            self.wl.Release(1)
            fail f
        }
        self.wl.Release(1)
    }
}

// State.Write sends one frame of op over data as a writer: waits for wl
// (Canceled while it waits writes nothing and leaves the connection usable),
// fails with the stored failure when the connection failed while it waited
// (rule Failed: nothing after a Close frame or a partial frame), sends an owed
// pong first, then the frame, releases wl and answers what was owed meanwhile.
// A failed write fails the connection, the failure stored before wl is
// released, so the next writer reports it and sends nothing.
State.Write(&self, conn: &net.Conn, io: Io, op: u8, data: []u8): Error!void {
    try self.wl.Acquire(io, 1)
    self.Check(io) else e {
        self.wl.Release(1)
        fail e
    }
    self.SendOwed(conn, io) else e {
        f := self.Fail(io, conn, e)
        self.wl.Release(1)
        fail f
    }
    self.WriteFrame(conn, io, op, data) else e {
        f := self.Fail(io, conn, e)
        self.wl.Release(1)
        fail f
    }
    self.wl.Release(1)
    // A failed pong is stored; this frame went out whole, so this call
    // succeeded, and the next call reports the failure.
    self.Answer(conn, io) else {}
}

// State.OwePong records a Ping's payload as owed (rule Owed pongs):
// Protocol(1008) past pending_pongs; nothing is owed once a Close frame was
// sent.
State.OwePong(&self, io: Io, payload: []u8): Error!void {
    var g := try self.sh.Lock(io.NoCancel())
    if g.Ref().close_sent {
        g.Unlock()
        return
    }
    g.Mut().owed += 1
    if g.Ref().owed > self.pending_pongs {
        g.Unlock()
        fail Protocol(code: ClosePolicy)
    }
    (mut g.Mut().pong[0..payload.Len()]).CopyFrom(payload)
    g.Mut().pong_n = payload.Len()
    g.Unlock()
}

// State.Echo stores the peer's Close as the connection's failure,
// PeerClosed(code), unless one is stored, so a writer waiting for wl sends
// nothing after the echo (rule Failed), and answers it with a Close frame of
// the same status (7.1.1, 5.5.1), unless one was sent or the connection had
// failed, waiting for wl within the close limit with the cancellation masked
// (rule Replies); a reply that cannot be sent in time is dropped, as the
// connection is ending.
State.Echo(&self, conn: &net.Conn, io: Io, code: u16) {
    q := io.NoCancel()
    var g := self.sh.Lock(q) else {
        return
    }
    sent := g.Ref().close_sent || g.Ref().fault.IsSome()
    if g.Ref().fault.IsNone() {
        g.Mut().fault = some(PeerClosed(code: code))
    }
    g.Mut().close_sent = true
    g.Unlock()
    if sent {
        return
    }
    var body: [2]u8 = [Trunc[u8](code >> 8), Trunc[u8](code)]
    n := if code == CloseNoStatus { 0 } else { 2 }
    scope s := q.Scope() {
        s.CancelAfter(self.close_limit)
        self.wl.Acquire(q, 1) else {
            return
        }
        self.WriteFrame(conn, q, OpClose, body[0..n]) else {}
        self.wl.Release(1)
    }
}

// ReadHalf.Consume drops the first n bytes of the read-ahead.
ReadHalf.Consume(mut self, n: int) {
    self.start += n
    if self.start == self.have {
        self.start = 0
        self.have = 0
    }
}

// ReadHalf.Fill reads more bytes into the read-ahead, moving what is left
// to the front only when the end is reached, so each byte moves at most
// once. The transport's end is PeerClosed(1006): no Close frame came.
ReadHalf.Fill(mut self, conn: &net.Conn, io: Io): Error!void {
    if self.have == ReadAhead {
        rest := self.have - self.start
        for i in 0..rest {
            self.buf[i] = self.buf[self.start + i]
        }
        self.start = 0
        self.have = rest
    }
    k := try conn.Read(io, mut self.buf[self.have..])
    if k == 0 {
        fail PeerClosed(code: CloseAbnormal)
    }
    self.have += k
}

// ReadHalf.NextHead reads the next frame's header and makes it the frame
// in progress, after the checks a header alone cannot make (5.4): a
// continuation with no message begun, a new data frame inside a
// fragmented message, and a message over message_bytes, all before any
// payload byte is read.
ReadHalf.NextHead(mut self, hv: &State, conn: &net.Conn, io: Io): Error!void {
    for {
        if x := try ParseHead(self.buf[self.start..self.have], !hv.client, hv.frame_bytes) {
            if !IsControl(x.op) {
                if (x.op == OpContinue) != (self.msg_op != 0) {
                    fail Protocol(code: CloseProtocol)
                }
                if x.len > hv.message_bytes - self.msg.Len() {
                    fail TooLarge
                }
                if x.op != OpContinue {
                    self.msg_op = x.op
                }
            }
            self.Consume(x.size)
            self.head = x
            self.left = x.len
            self.phase = 0
            self.ctl_n = 0
            self.in_frame = true
            return
        }
        try self.Fill(conn, io)
    }
}

// ReadHalf.Control reads the payload of the control frame in progress and acts
// on it: a Ping is owed a Pong (rule Owed pongs), a Pong is dropped, and a
// Close is answered (State.Echo) and reported as PeerClosed.
ReadHalf.Control(mut self, hv: &State, conn: &net.Conn, io: Io): Error!void {
    n := self.head.len
    for self.ctl_n < n {
        if self.start == self.have {
            try self.Fill(conn, io)
        }
        avail := self.have - self.start
        k := if avail < n - self.ctl_n { avail } else { n - self.ctl_n }
        (mut self.ctl[self.ctl_n..self.ctl_n + k]).CopyFrom(self.buf[self.start..self.start + k])
        self.Consume(k)
        self.ctl_n += k
    }
    if self.head.masked {
        _ = Mask(mut self.ctl[0..n], self.head.key, 0)
    }
    self.in_frame = false
    op := self.head.op
    if op == OpPing {
        try hv.OwePong(io, self.ctl[0..n])
        try hv.Answer(conn, io)
    } else if op == OpClose {
        code := try CloseBody(self.ctl[0..n])
        hv.Echo(conn, io, code)
        fail PeerClosed(code: code)
    }
}

// ReadHalf.Gather appends the rest of the data frame in progress to msg,
// unmasked; at its end the frame is done, and the message is ready when the
// frame was its last. Each read-ahead's worth is consumed once it is appended
// (one HeapList.Extend), so a cancel loses nothing (rule Repeatable Recv).
ReadHalf.Gather(mut self, h: &Heap, conn: &net.Conn, io: Io): Error!void {
    for self.left > 0 {
        if self.start == self.have {
            try self.Fill(conn, io)
        }
        avail := self.have - self.start
        k := if avail < self.left { avail } else { self.left }
        at := self.start
        if self.head.masked {
            self.phase = Mask(mut self.buf[at..at + k], self.head.key, self.phase)
        }
        try self.msg.Extend(h.Alloc(), self.buf[at..at + k])
        self.Consume(k)
        self.left -= k
    }
    self.in_frame = false
    if self.head.fin {
        self.ready = true
    }
}

// ReadHalf.Skip drops the rest of the data frame in progress (a closing
// connection reads to the peer's Close frame and keeps no data).
ReadHalf.Skip(mut self, conn: &net.Conn, io: Io): Error!void {
    for self.left > 0 {
        if self.start == self.have {
            try self.Fill(conn, io)
        }
        avail := self.have - self.start
        k := if avail < self.left { avail } else { self.left }
        self.Consume(k)
        self.left -= k
    }
    self.in_frame = false
    if self.head.fin {
        self.msg_op = 0
        self.msg.Clear()
    }
}

// KindOf is the kind of a message that began with op.
KindOf(op: u8): Kind {
    return if op == OpText { Kind.Text } else { Kind.Binary }
}

// ReadHalf.Deliver gives the ready message to the caller in a: none when a
// cannot hold it (the message stays ready, rule Repeatable Recv),
// Protocol(1007) for text that is not UTF-8 (5.6, 8.1). A buffer grown past
// 64 KiB goes back to the heap.
ReadHalf.Deliver(mut self, h: &Heap, a: &Arena): Error!?Message from a {
    n := self.msg.Len()
    out := a.Slice[u8](n) else {
        return none
    }
    out.CopyFrom(self.msg.Items())
    kind := KindOf(self.msg_op)
    self.msg_op = 0
    self.ready = false
    if n > 65536 {
        Replace(mut self.msg, HeapList[u8].New(h.Alloc())).Free(h.Alloc())
    } else {
        self.msg.Clear()
    }
    if kind == Kind.Text && !simd.ValidUtf8(out[..]) {
        fail Protocol(code: CloseBadData)
    }
    return some(Message(kind: kind, data: out[..]))
}

// FirstRoom is the most of a long payload Whole reserves before its bytes
// arrive; the reservation doubles as they do, so a peer that announces a
// long frame and sends little makes the connection reserve little.
const FirstRoom: int = 65536

// ReadHalf.Whole reads a message of one frame straight into a, the common
// case: a is asked for room for the payload, at most FirstRoom before any
// payload byte is consumed (none when it cannot, the frame as it was) and
// twice as much each time the bytes fill it, the bytes in the read-ahead
// are copied, and a long rest is read into the message itself; a read is
// made only when the read-ahead is empty, so the bytes stay in order. An error
// in the middle, a full arena included, moves what was read to msg, so the
// next Recv continues on the general path (rule Repeatable Recv).
ReadHalf.Whole(mut self, h: &Heap, conn: &net.Conn, a: &Arena, io: Io): Error!?Message from a {
    n := self.left
    var room := if n < FirstRoom { n } else { FirstRoom }
    var out := a.Slice[u8](room) else {
        return none
    }
    var got := if self.have - self.start < room { self.have - self.start } else { room }
    (mut out[0..got]).CopyFrom(self.buf[self.start..self.start + got])
    self.Consume(got)
    for got < n {
        if got == room {
            room = if n - room < room { n } else { 2 * room }
            bigger := a.Slice[u8](room) else {
                try self.Keep(h, out[0..got])
                return none
            }
            (mut bigger[0..got]).CopyFrom(out[0..got])
            out = bigger
        }
        // The read-ahead is emptied first: a Fill may have brought more than
        // the room it was called for held, and a read now would
        // wait for bytes already here, or put later bytes before them.
        if self.start == self.have {
            if room - got >= Direct {
                k := conn.Read(io, mut out[got..room]) else e {
                    try self.Keep(h, out[0..got])
                    fail e
                }
                if k == 0 {
                    fail PeerClosed(code: CloseAbnormal)
                }
                got += k
                continue
            }
            self.Fill(conn, io) else e {
                try self.Keep(h, out[0..got])
                fail e
            }
        }
        k := if self.have - self.start < room - got { self.have - self.start } else { room - got }
        (mut out[got..got + k]).CopyFrom(self.buf[self.start..self.start + k])
        self.Consume(k)
        got += k
    }
    if self.head.masked {
        _ = Mask(mut out[..], self.head.key, 0)
    }
    self.in_frame = false
    self.msg_op = 0
    kind := KindOf(self.head.op)
    if kind == Kind.Text && !simd.ValidUtf8(out[..]) {
        fail Protocol(code: CloseBadData)
    }
    return some(Message(kind: kind, data: out[..]))
}

// ReadHalf.Keep moves the got bytes of an interrupted one-frame message
// to msg, unmasked, so the frame continues on the general path.
ReadHalf.Keep(mut self, h: &Heap, got: []u8): OutOfMemory!void {
    try self.msg.Extend(h.Alloc(), got)
    if self.head.masked {
        self.phase = Mask(mut self.msg.ItemsMut()[..], self.head.key, 0)
    }
    self.left -= got.Len()
}

// ReadHalf.Next reads frames until a data message is whole and gives it in
// a, answering control frames on the way; none when a cannot hold it
// (NoRoom, repeatable Recv: the message stays for the next call).
ReadHalf.Next(mut self, hv: &State, h: &Heap, conn: &net.Conn, a: &Arena, io: Io): Error!?Message from a {
    for {
        if self.ready {
            return try self.Deliver(h, a)
        }
        if !self.in_frame {
            // A failure stored by another task (a writer's) ends the read
            // before the next frame (rule Failed, RFC 6455 7.1.7).
            try hv.Check(io)
            try self.NextHead(hv, conn, io)
        }
        if IsControl(self.head.op) {
            try self.Control(hv, conn, io)
            continue
        }
        if self.head.fin && self.head.op != OpContinue && self.left == self.head.len && self.msg.Len() == 0 {
            return try self.Whole(h, conn, a, io)
        }
        try self.Gather(h, conn, io)
    }
}

// ReadHalf.Drain reads frames, dropping data, until the peer's Close
// frame: PeerClosed, which a closing connection waits for (7.1.2).
ReadHalf.Drain(mut self, hv: &State, conn: &net.Conn, io: Io): Error!void {
    if self.ready {
        // A whole message no arena could hold is dropped with the rest.
        self.ready = false
        self.msg_op = 0
        self.msg.Clear()
    }
    for {
        if !self.in_frame {
            try self.NextHead(hv, conn, io)
        }
        if IsControl(self.head.op) {
            try self.Control(hv, conn, io)
            continue
        }
        try self.Skip(conn, io)
    }
}

/// Receives the next data message, its fragments joined, into a, after
/// answering the Pings and the Close that come before it. A second Recv of
/// this Conn waits for the first.
///
/// Errors: PeerClosed when the peer closed (its Close frame was answered);
/// Protocol when the peer broke RFC 6455: a control frame over 125 bytes
/// or fragmented, a reserved bit set, a frame masked the wrong way, or
/// more pings owed than pending_pongs; TooLarge past the frame or message
/// limit, checked before anything is allocated. On those the connection
/// sends its Close frame. Also the connection's net.Error, and OutOfMemory
/// when a cannot hold the message, which stays for the next call. After a
/// failure every call reports it. A Recv interrupted by a cancel or a
/// deadline loses nothing: the connection keeps a partly read frame or
/// message, and a reply it owes, and the next Recv goes on. O(message);
/// allocates the message in a, and a fragmented one first on the heap.
export Conn.Recv(&self, a: &Arena, io: Io): Error!Message from a {
    hv := self.box.Ref()
    try hv.Check(io)
    var g := try hv.rd.Lock(io)
    // A failure stored while this Recv waited for rd (rule Failed).
    hv.Check(io) else e {
        g.Unlock()
        fail e
    }
    m := g.Mut().Next(hv, self.heap, &self.conn, a, io) else e {
        match e {
            Canceled {
                g.Unlock()
                fail e
            }
            _ {
                // Stored before rd is released, so a Recv waiting for rd
                // reports it and reads nothing more (rule Failed); the Close
                // frame and the shutdown come after, so that Recv does not wait
                // for them.
                (f, code) := hv.Store(io, e)
                g.Unlock()
                hv.Finish(io, &self.conn, code)
                fail f
            }
        }
    }
    g.Unlock()
    msg := m else {
        fail OutOfMemory
    }
    return msg
}

/// Sends data as one message of kind, in one frame, masked on the client
/// side. Errors: Protocol(1007) for text that is not UTF-8, which sends
/// nothing and leaves the connection usable; Canceled while waiting for
/// the write lock, likewise; the connection's net.Error; PeerClosed after
/// the peer's Close. A write interrupted part way fails the connection:
/// after it every call but Abandon reports that failure, and a call
/// waiting in another task returns it at once. O(data); allocates
/// nothing.
export Conn.Send(&self, io: Io, kind: Kind, data: []u8): Error!void {
    hv := self.box.Ref()
    try hv.Check(io)
    if kind == Kind.Text && !simd.ValidUtf8(data) {
        fail Protocol(code: CloseBadData)
    }
    op := if kind == Kind.Text { OpText } else { OpBinary }
    try hv.Write(&self.conn, io, op, data)
}

/// The subprotocol the server chose: on a Conn from Dial, the one of
/// Options.protocols the server's 101 named, none when it named none; on a
/// Conn from Accept, the one Accept chose from its protocols, none when it
/// chose none. O(1); allocates nothing.
export Conn.Protocol(&self): ?str from self {
    p := self.box.Ref().protocol.Items()
    if p.Len() == 0 {
        return none
    }
    // A token (ReplyHead or Choose checked it), so it is ASCII.
    name := str.From(p) else {
        return none
    }
    return some(name)
}

/// Sends a Ping with data, at most 125 bytes (TooLarge otherwise, which
/// sends nothing). Recv reads the peer's Pong and drops it. Errors, and
/// what an interrupted write does, as for Send. O(data); allocates
/// nothing.
export Conn.Ping(&self, io: Io, data: []u8): Error!void {
    hv := self.box.Ref()
    try hv.Check(io)
    if data.Len() > MaxControl {
        fail TooLarge
    }
    try hv.Write(&self.conn, io, OpPing, data)
}

// SentCode reports whether a Close frame may be sent with code: those
// ReceivedCode allows, or 0 for a Close frame with no status.
SentCode(code: u16): bool {
    return code == 0 || ReceivedCode(code)
}

// CloseHandshake sends this side's Close frame, unless one was sent, then
// reads to the peer's (7.1.2); the transport's end after this side's
// frame also ends it.
CloseHandshake(hv: &State, conn: &net.Conn, io: Io, body: []u8): Error!void {
    try hv.wl.Acquire(io, 1)
    var g := hv.sh.Lock(io.NoCancel()) else e {
        hv.wl.Release(1)
        fail e
    }
    sent := g.Ref().close_sent
    g.Mut().close_sent = true
    g.Unlock()
    if !sent {
        hv.WriteFrame(conn, io, OpClose, body) else e {
            hv.wl.Release(1)
            fail e
        }
    }
    hv.wl.Release(1)
    var r := try hv.rd.Lock(io)
    r.Mut().Drain(hv, conn, io) else e {
        r.Unlock()
        match e {
            PeerClosed { return }
            _ { fail e }
        }
    }
    r.Unlock()
}

/// Closes the connection: sends a Close frame with code and reason (code
/// 0 sends none and no reason), reads to the peer's Close frame, then
/// frees its state and closes its net.Conn with net.Conn.Close (over TLS,
/// a close_notify) within what is left of the close limit, abandoning it
/// if that fails, since the WebSocket close is already done. A connection
/// whose peer closed first, answered by Recv, only releases (Recv shut
/// its transport down). Errors: Protocol(code) for a status RFC 6455 7.4
/// does not let an endpoint send, TooLarge for a reason over 123 bytes,
/// each before anything is sent; Canceled under a cancelled scope;
/// time.TimedOut past the close limit (also under a NoCancel Io, and for a
/// cancel that comes after the limit passed); the connection's net.Error;
/// the stored failure. On any of them the Conn stays the caller's, in the
/// failed state, to Abandon (`onerr c.Abandon()` with
/// `try c.Close(io, code, reason)`). Frees the state; allocates nothing.
export Conn.Close(self, io: Io, code: u16, reason: str): Error!void {
    if !SentCode(code) || (code == 0 && reason.Len() > 0) {
        fail Protocol(code: code)
    }
    if reason.Len() > MaxControl - 2 {
        fail TooLarge
    }
    hv := self.box.Ref()
    if f := hv.Fault(io) {
        match f {
            PeerClosed {
                self.Abandon()
                return
            }
            _ { fail f }
        }
    }
    var body: [MaxControl]u8 = [0; MaxControl]
    var n := 0
    if code != 0 {
        body[0] = Trunc[u8](code >> 8)
        body[1] = Trunc[u8](code)
        (mut body[2..2 + reason.Len()]).CopyFrom(reason.AsBytes())
        n = 2 + reason.Len()
    }
    scope s := io.Scope() {
        s.CancelAfter(hv.close_limit)
        CloseHandshake(hv, &self.conn, io, body[0..n]) else e {
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            err: Error = if task.LimitHit(&s, canceled) { time.TimedOut } else { e }
            fail hv.Fail(io, &self.conn, err)
        }
        // The closing handshake is done; the transport is closed with its
        // own Close, which over TLS sends close_notify (RFC 6455 7.1.1),
        // in what is left of the close limit. Its failure cannot be
        // reported (this Conn is spent), so the transport is then
        // abandoned, as std.http's CloseConn does.
        Conn(conn, heap, box) := self
        FreeState(box, heap)
        conn.Close(io) else {
            conn.Abandon()
        }
    }
}

// State.Fault is the stored failure, none while the connection works.
State.Fault(&self, io: Io): ?Error {
    g := self.sh.Lock(io.NoCancel()) else {
        return none
    }
    f := g.Ref().fault
    g.Unlock()
    return f
}

/// Releases the connection without speaking to the peer: its state freed,
/// its net.Conn abandoned. Never blocks or fails. O(1); allocates
/// nothing.
export Conn.Abandon(self) {
    Conn(conn, heap, box) := self
    FreeState(box, heap)
    conn.Abandon()
}
