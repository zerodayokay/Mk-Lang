// Why: TCP, Unix streams, TLS and in-memory pipes should all look like one
// connection type, so code above them (http, websocket) takes any of them.
// What: Conn, Transport, NewConn, ConnSlot (ReserveConn, Fill, Free),
// Stream.Conn, and Conn's Read, WriteAll, Shutdown, PeerAddr, Close,
// Abandon, Reader and Writer.
// How: a Conn holds its transport's operations as fn values, so each is one
// indirect call. NewConn boxes a transport on a &Heap through std.core's
// erasure (the only unsafe it needs is std.core's); a stream is its OS handle,
// stored in place of the box, so Stream.Conn allocates nothing. Reads take
// one lock and writes another: one Read runs beside one WriteAll in two
// tasks, and a WriteAll never interleaves with another.

import std.core
import std.rt

/// What a Conn runs over: a program's own byte stream (an in-memory pipe,
/// a wrapper that counts bytes) behind NewConn. Provisional: it may change
/// in a minor release. It has the operations of Conn on &self, and
/// Abandon, the one release, which never blocks or fails. Close speaks to
/// the peer (a TLS close_notify) and may fail and be called again; a
/// transport whose Close only releases does nothing in Close and releases
/// in Abandon. A transport is Send and Share, so a Conn over it is: state
/// it changes is under its own Mutex, and its memory is on a &Heap, never
/// an arena.
///
/// A Conn asks three things of its transport.
///
/// - Overlap: the Conn runs at most one Read and one WriteAll at a time,
///   but Shutdown and PeerAddr take no lock, so either may run beside a
///   Read, a WriteAll and each other, in any task. Close and Abandon run
///   alone.
/// - Cancel-safety: a Read that returns Canceled has consumed nothing (a
///   transport that took bytes from below keeps them for the next Read,
///   as a TLS record layer keeps a partial record). WriteAll may leave a
///   prefix sent. A Close that speaks to the peer is cancellable and
///   bounded by a time limit.
/// - Shutdown ends a call waiting in another task: after Shutdown(Read) or
///   Shutdown(Both) a waiting Read returns 0 (or the transport's failure),
///   and after Shutdown(Both) a waiting WriteAll fails, at once, not at
///   its own deadline. std.tls and std.websocket rely on this to fail
///   every call of a broken connection. A stream's Conn meets it on Linux;
///   on Windows a waiting Read or WriteAll is not woken and stays until
///   its deadline or cancel.
// The Windows gap needs a runtime fix not yet made: a shutdown that wakes
// a waiting Read or WriteAll; the blocks
// in tests_async.mk assert it there with MK_NET3_BLOCKED=1 until it lands.
export interface Transport {
    Read(&self, io: Io, buf: mut []u8): Error!int
    WriteAll(&self, io: Io, data: []u8): Error!void
    Shutdown(&self, io: Io, how: Shutdown): Error!void
    PeerAddr(&self): ?Addr
    Close(&self, io: Io): Error!void
    Abandon(self)
}

/// A connected byte stream over any transport: a TCP or Unix stream
/// (Stream.Conn), a TLS connection, or a Transport of the program's
/// (NewConn). std.http and std.websocket take one, so code above them runs
/// over any of these. Linear, Send and Share: tasks may share it by
/// reference, one reading while another writes. Reads take one lock and
/// writes another, so a WriteAll never interleaves with another. Close or
/// Abandon releases it. It keeps the &Heap it was made with, and has no
/// Free.
export linear view struct Conn {
    // the boxed transport, none for one OS handle (sock)
    box: ?core.SealedBox
    sock: uint
    // the handle's peer, read when its stream was made; a boxed
    // transport is asked through do_peer
    peer: ?Addr
    // the heap box lives on; the process heap for a handle, which has
    // none
    heap: &Heap
    reads: Mutex[u8]
    writes: Mutex[u8]
    do_read: fn(&Conn, Io, mut []u8): Error!int
    do_write: fn(&Conn, Io, []u8): Error!void
    do_shutdown: fn(&Conn, Io, Shutdown): Error!void
    do_peer: fn(&Conn): ?Addr
    do_close: fn(&Conn, Io): Error!void
    do_abandon: fn(core.SealedBox, &Heap)
}

/// Boxes the transport t on h as a Conn, which keeps h and t's loans and
/// frees the box at Close or Abandon. Provisional, as Transport is.
/// Errors: OutOfMemory, with t still the caller's (it is not consumed on
/// failure). O(1); allocates one box of ?T's size on h. It is ReserveConn
/// then ConnSlot.Fill, in one call.
export NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t {
    s := try ReserveConn[T](h)
    return s.Fill(t)
}

/// A box on a heap reserved for one transport of type T, empty until Fill
/// moves the transport in and makes the Conn. Provisional, as Transport
/// is. A function that takes a Conn or a transport by value and must leave
/// it with its caller when it fails (a TLS connector) reserves first, does
/// its fallible work, and fills last, so nothing can fail after the move
/// (E0411). Linear: Fill or Free (with the heap's allocator) ends it.
export linear view struct ConnSlot[T: Transport + Send + Share] {
    box: Box[?T]
    heap: &Heap
}

/// Reserves on h the box a Conn over a T will live in. Provisional.
/// Errors: OutOfMemory. O(1); allocates one box of ?T's size on h, freed
/// by the Conn's Close or Abandon, or by Free.
export ReserveConn[T: Transport + Send + Share](h: &Heap): OutOfMemory!ConnSlot[T] from h {
    empty: ?T = none
    b := Box[?T].New(h.Alloc(), empty) else e {
        Vacant(empty)
        fail e
    }
    return ConnSlot[T](box: b, heap: h)
}

/// Moves t into the reserved box and makes the Conn over it, which keeps
/// the slot's heap and t's loans. t is stored only into the box, which
/// the Conn alone reaches, so a slot reserved on sys.heap takes a
/// transport that borrows a local. Cannot fail; O(1); allocates nothing.
export ConnSlot[T].Fill(self, t: T): Conn from self, t {
    ConnSlot(box, heap) := self
    return Conn(box: some(core.SealedBox.Fill[T](box, t)), sock: 0, peer: none, heap: heap, reads: Mutex[u8].New(0),
        writes: Mutex[u8].New(0), do_read: BoxRead[T], do_write: BoxWrite[T], do_shutdown: BoxShutdown[T],
        do_peer: BoxPeer[T], do_close: BoxClose[T], do_abandon: BoxAbandon[T])
}

/// Gives the reserved box back unused: a slot owns only allocator memory,
/// so it implements Free. a must be the allocator of the heap it was
/// reserved on (h.Alloc()); any other panics with code 12. O(1); one
/// free, and it allocates nothing.
impl[T: Transport + Send + Share] Free for ConnSlot[T] {
    Free(self, a: &Alloc) {
        ConnSlot(box, heap: _) := self
        Vacant(box.Take(a))
    }
}

// Vacant ends what a slot held before Fill, which is always none.
Vacant[T: Transport](x: ?T) {
    match x {
        some(t) {
            t.Abandon()
            Panic("net.ConnSlot: a slot filled twice")
        }
        none {}
    }
}

/// The stream as a Conn, with its peer's address kept (none for a Unix
/// stream, another Io's stream, or a TCP peer the OS could not name when
/// the stream was made). The Conn's Close only releases, so it cannot
/// fail and is not cancellable. O(1); allocates nothing: the handle is
/// stored where a transport's box would be.
export Stream.Conn(self): Conn {
    Stream(h, peer, kind: _, nosh: _) := self
    return Conn(box: none, sock: h, peer: peer, heap: rt.ProcessHeap(), reads: Mutex[u8].New(0), writes: Mutex[u8].New(0),
        do_read: SockConnRead, do_write: SockConnWrite, do_shutdown: SockConnShutdown, do_peer: SockConnPeer,
        do_close: SockConnClose, do_abandon: NoBox)
}

/// Reads once, as Stream.Read: at most buf.Len() bytes, 0 at the end of
/// the stream. Reads of this Conn run one at a time; a Read waits for the
/// lock, and is Canceled while it waits. A Read that returns Canceled has
/// consumed nothing: call it again. Errors: the transport's. One indirect
/// call, O(1) here plus the transport's read; allocates nothing.
export Conn.Read(&self, io: Io, buf: mut []u8): Error!int {
    g := try self.reads.Lock(io)
    defer g.Unlock()
    f := self.do_read
    return try f(self, io, buf)
}

/// Sends every byte of data before any other WriteAll of this Conn sends
/// one. A call interrupted by a cancel has sent an unknown prefix of data:
/// close the connection. Errors: the transport's. One indirect call, O(1)
/// here plus the transport's write; allocates nothing.
export Conn.WriteAll(&self, io: Io, data: []u8): Error!void {
    g := try self.writes.Lock(io)
    defer g.Unlock()
    f := self.do_write
    try f(self, io, data)
}

/// Ends one or both directions, as Stream.Shutdown. It takes neither lock,
/// so it never waits behind a Read or a WriteAll of another task, and it
/// ends them: a Read waiting in another task returns 0 after Shutdown(Read)
/// or Shutdown(Both), and a waiting WriteAll fails after Shutdown(Both).
/// Over a stream on Windows the waiting call is not woken and stays until
/// its deadline or cancel. Errors: the transport's. One indirect call,
/// O(1) here plus the transport's shutdown; allocates nothing.
export Conn.Shutdown(&self, io: Io, how: Shutdown): Error!void {
    f := self.do_shutdown
    try f(self, io, how)
}

/// The peer's address; none when the transport has none to give (a Unix
/// stream, an in-memory transport, a stream of an Io other than the native
/// one). One indirect call, O(1) here plus the transport's; allocates
/// nothing.
export Conn.PeerAddr(&self): ?Addr {
    f := self.do_peer
    return f(self)
}

/// Closes the connection: the transport's Close, which may speak to the
/// peer, then the release. On failure the Conn stays the caller's,
/// unchanged, to Close again or Abandon (`onerr c.Abandon()` with
/// `try c.Close(io)`). Over a stream it only releases, cannot fail and
/// is not cancellable. Over a transport whose Close speaks to the peer
/// (TLS) it is cancellable (Canceled) and bounded by the transport's close
/// limit (time.TimedOut). One indirect call, O(1) here plus the
/// transport's Close; allocates nothing; frees the box.
export Conn.Close(self, io: Io): Error!void {
    f := self.do_close
    try f(&self, io)
    self.Abandon()
}

/// Releases the connection without speaking to the peer: the transport's
/// Abandon, or the handle's close. Never blocks or fails. One indirect
/// call, O(1) here plus the transport's Abandon; allocates nothing; frees
/// the box.
export Conn.Abandon(self) {
    Conn(box, sock, heap, reads, writes, do_abandon, ..) := self
    _ = reads.Into()
    _ = writes.Into()
    match box {
        some(b) { do_abandon(b, heap) }
        none { rt.Abandon(sock) }
    }
}

// The reads of a Conn's Reader and the writes of its Writer: a Conn's
// error is the Reader's ReadError or the Writer's WriteError.
ConnFill(c: &Conn, io: Io, dst: mut []u8): ReadError!int {
    return c.Read(io, dst) else {
        fail ReadError
    }
}

ConnSend(c: &Conn, io: Io, b: []u8): WriteError!void {
    c.WriteAll(io, b) else {
        fail WriteError
    }
}

// A Conn has no flush of its own: WriteAll has sent every byte.
ConnFlush(c: &Conn, io: Io): WriteError!void {
}

const ConnReads = core.ReadPort[Conn].New(ConnFill)
const ConnWrites = core.WritePort[Conn].New(ConnSend, ConnFlush)

/// A buffered Reader over this Conn, using buf as its buffer. Each fill is
/// one Conn.Read, under the read lock, so two Readers of one Conn take
/// turns per fill. Any error becomes its ReadError. Bytes it buffered and
/// the caller did not read are lost when it is dropped. O(1); allocates
/// nothing.
export Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf {
    return (&ConnReads).Reader(io, self, buf)
}

/// A buffered Writer over this Conn, using buf as its buffer. Each flush
/// is one Conn.WriteAll, so the bytes of one flush never interleave with
/// another writer's. Any error becomes its WriteError. Flush before
/// dropping it. O(1); allocates nothing.
export Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf {
    return (&ConnWrites).Writer(io, self, buf)
}

// The operations of a boxed transport: each runs through SealedBox.With so no
// reference escapes the box and Conn stays read-only for loans. The box is
// some(t) for every Conn these are stored in (Fill); anything else here is a
// bug of this file.
DoBoxRead[T: Transport + Send + Share](x: &?T, a: (io: Io, buf: mut []u8, out: mut (n: int, err: ?Error))) {
    (io, buf, out) := a
    if t := &*x {
        match t.Read(io, buf) {
            ok(n) { *out = (n: n, err: none) }
            err(e) { *out = (n: 0, err: some(e)) }
        }
        return
    }
    Panic("net.Conn: a boxed operation on an empty box")
}

DoBoxWrite[T: Transport + Send + Share](x: &?T, a: (io: Io, data: []u8, err: mut ?Error)) {
    (io, data, err) := a
    if t := &*x {
        match t.WriteAll(io, data) {
            ok(_) { *err = none }
            err(e) { *err = some(e) }
        }
        return
    }
    Panic("net.Conn: a boxed operation on an empty box")
}

DoBoxShutdown[T: Transport + Send + Share](x: &?T, a: (io: Io, how: Shutdown, err: mut ?Error)) {
    (io, how, err) := a
    if t := &*x {
        match t.Shutdown(io, how) {
            ok(_) { *err = none }
            err(e) { *err = some(e) }
        }
        return
    }
    Panic("net.Conn: a boxed operation on an empty box")
}

DoBoxPeer[T: Transport + Send + Share](x: &?T, out: mut ?Addr) {
    if t := &*x {
        *out = t.PeerAddr()
        return
    }
    Panic("net.Conn: a boxed operation on an empty box")
}

DoBoxClose[T: Transport + Send + Share](x: &?T, a: (io: Io, err: mut ?Error)) {
    (io, err) := a
    if t := &*x {
        match t.Close(io) {
            ok(_) { *err = none }
            err(e) { *err = some(e) }
        }
        return
    }
    Panic("net.Conn: a boxed operation on an empty box")
}

BoxRead[T: Transport + Send + Share](c: &Conn, io: Io, buf: mut []u8): Error!int {
    if b := &c.box {
        var out: (n: int, err: ?Error) = (n: 0, err: none)
        b.With[?T, (io: Io, buf: mut []u8, out: mut (n: int, err: ?Error))]((io: io, buf: buf, out: mut out), DoBoxRead[T])
        if e := out.err {
            fail e
        }
        return out.n
    }
    Panic("net.Conn: a boxed operation on a handle")
}

BoxWrite[T: Transport + Send + Share](c: &Conn, io: Io, data: []u8): Error!void {
    if b := &c.box {
        var err: ?Error = none
        b.With[?T, (io: Io, data: []u8, err: mut ?Error)]((io: io, data: data, err: mut err), DoBoxWrite[T])
        if e := err {
            fail e
        }
        return
    }
    Panic("net.Conn: a boxed operation on a handle")
}

BoxShutdown[T: Transport + Send + Share](c: &Conn, io: Io, how: Shutdown): Error!void {
    if b := &c.box {
        var err: ?Error = none
        b.With[?T, (io: Io, how: Shutdown, err: mut ?Error)]((io: io, how: how, err: mut err), DoBoxShutdown[T])
        if e := err {
            fail e
        }
        return
    }
    Panic("net.Conn: a boxed operation on a handle")
}

BoxPeer[T: Transport + Send + Share](c: &Conn): ?Addr {
    if b := &c.box {
        var out: ?Addr = none
        b.With[?T, mut ?Addr](mut out, DoBoxPeer[T])
        return out
    }
    Panic("net.Conn: a boxed operation on a handle")
}

// Close speaks to the peer through &T, so a failure changes nothing the
// Conn owns.
BoxClose[T: Transport + Send + Share](c: &Conn, io: Io): Error!void {
    if b := &c.box {
        var err: ?Error = none
        b.With[?T, (io: Io, err: mut ?Error)]((io: io, err: mut err), DoBoxClose[T])
        if e := err {
            fail e
        }
        return
    }
    Panic("net.Conn: a boxed operation on a handle")
}

DoBoxAbandon[T: Transport + Send + Share](box: Box[?T], h: &Heap) {
    match box.Take(h.Alloc()) {
        some(t) { t.Abandon() }
        none {}
    }
}

BoxAbandon[T: Transport + Send + Share](b: core.SealedBox, h: &Heap) {
    b.TakeWith[?T, &Heap](h, DoBoxAbandon[T])
}

// The operations of a Conn over one OS handle: Stream's, on c.sock.
SockConnRead(c: &Conn, io: Io, buf: mut []u8): Error!int {
    if buf.Len() == 0 {
        try CancelPoint(io)
        return 0
    }
    return try CheckCount(core.IoRecv(io, c.sock, buf), buf.Len())
}

SockConnWrite(c: &Conn, io: Io, data: []u8): Error!void {
    try SendAll(io, c.sock, data)
}

SockConnShutdown(c: &Conn, io: Io, how: Shutdown): Error!void {
    try CancelPoint(io)
    _ = try Check(rt.SockShutdown(io, c.sock, ShutdownCode(how)))
}

SockConnPeer(c: &Conn): ?Addr {
    return c.peer
}

// A socket's close only releases, which Abandon does.
SockConnClose(c: &Conn, io: Io): Error!void {
}

// A handle Conn has no box: Abandon closes the handle instead.
NoBox(b: core.SealedBox, h: &Heap) {
    Panic("net.Conn: a handle with a box")
}
