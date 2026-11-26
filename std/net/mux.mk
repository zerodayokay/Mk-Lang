// Why: HTTP/3 and QUIC share a single multiplexed-connection abstraction
// without importing each other, and tests swap in an in-memory engine
// (std.net.pipe) without sockets.
//
// What: Mux, MuxStream, MuxReady, MuxLimits, MuxEngine, MuxListener,
// MuxListening, MuxDialer and MuxServer.
//
// How: Mux and MuxListener box their std-built engines in core.SealedBox
// and dispatch through function-pointer thunks instantiated in Of[T];
// MuxDialer and MuxServer wrap core.ErasedCall, as Secure does.

import std.core
import std.time

/// What Mux.Wait found ready.
export enum MuxReady {
    /// `ids[index]` has data, its end, or a reset to read.
    Readable(index: int)
    /// A new stream of the peer is ready to Accept (`uni` says which kind).
    Incoming(uni: bool)
}

/// What the peer currently allows on a Mux connection.
export struct MuxLimits {
    /// Streams this side may open now, bidirectional.
    export bidi: i64
    /// Streams this side may open now, unidirectional.
    export uni: i64
    /// The negotiated idle timeout.
    export idle: time.Duration
}

/// One stream of a Mux. Linear: Mux.Release consumes it, so a forgotten
/// stream is a compile error, not a leaked record.
export linear struct MuxStream {
    id: i64
    gen_: u32
}

/// The stream's id (its QUIC stream id). O(1); allocates nothing.
export MuxStream.Id(&self): i64 {
    return self.id
}

/// The contract std's multiplexed transports implement. It is std's:
/// implementing it gives a program nothing, since only std can build the
/// core.SealedBox that Mux.Of takes, and its methods may change between
/// releases.
export interface MuxEngine {
    Open(&self, io: Io, uni: bool, wait: bool): Error!?(id: i64, gen_: u32)
    Accept(&self, io: Io, uni: bool, wait: bool): Error!?(id: i64, gen_: u32)
    Read(&self, io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): Error!?int
    Write(&self, io: Io, id: i64, gen_: u32, data: []u8, fin: bool): Error!void
    Credit(&self, id: i64, gen_: u32, total: i64)
    Wait(&self, io: Io, ids: []i64, incoming: bool): Error!MuxReady
    Reset(&self, id: i64, gen_: u32, code: u64)
    Stop(&self, id: i64, gen_: u32, code: u64)
    Release(&self, id: i64, gen_: u32)
    Limits(&self): MuxLimits
    Usable(&self, io: Io): bool
    PeerAddr(&self): ?Addr
    Close(&self, io: Io, code: u64): Error!void
    Abandon(self)
}

/// One multiplexed connection (a QUIC connection of std.quic, or an in-memory
/// test fake): linear, Send and Share; many tasks call it by reference. Only
/// std builds one (Mux.Of takes a core.SealedBox over ?T). Release with
/// Close or Abandon.
export linear view struct Mux {
    box: core.SealedBox
    heap: &Heap
    do_open: fn(&Mux, Io, bool, bool): Error!?(id: i64, gen_: u32)
    do_accept: fn(&Mux, Io, bool, bool): Error!?(id: i64, gen_: u32)
    do_read: fn(&Mux, Io, i64, u32, mut []u8, bool): Error!?int
    do_write: fn(&Mux, Io, i64, u32, []u8, bool): Error!void
    do_credit: fn(&Mux, i64, u32, i64)
    do_wait: fn(&Mux, Io, []i64, bool): Error!MuxReady
    do_reset: fn(&Mux, i64, u32, u64)
    do_stop: fn(&Mux, i64, u32, u64)
    do_release: fn(&Mux, i64, u32)
    do_limits: fn(&Mux): MuxLimits
    do_usable: fn(&Mux, Io): bool
    do_peer: fn(&Mux): ?Addr
    do_close: fn(&Mux, Io, u64): Error!void
    do_abandon: fn(core.SealedBox, &Heap)
}

DoMuxOpen[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, uni: bool, wait: bool, val: mut ?(id: i64, gen_: u32), err: mut ?Error)) {
    (io, uni, wait, val, err) := a
    if t := &*x {
        match t.Open(io, uni, wait) {
            ok(v) {
                *val = v
                *err = none
            }
            err(e) {
                *val = none
                *err = some(e)
            }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxAccept[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, uni: bool, wait: bool, val: mut ?(id: i64, gen_: u32), err: mut ?Error)) {
    (io, uni, wait, val, err) := a
    if t := &*x {
        match t.Accept(io, uni, wait) {
            ok(v) {
                *val = v
                *err = none
            }
            err(e) {
                *val = none
                *err = some(e)
            }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxRead[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool, val: mut ?int, err: mut ?Error)) {
    (io, id, gen_, buf, wait, val, err) := a
    if t := &*x {
        match t.Read(io, id, gen_, buf, wait) {
            ok(v) {
                *val = v
                *err = none
            }
            err(e) {
                *val = none
                *err = some(e)
            }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxWrite[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, id: i64, gen_: u32, data: []u8, fin: bool, out: mut ?Error)) {
    (io, id, gen_, data, fin, out) := a
    if t := &*x {
        match t.Write(io, id, gen_, data, fin) {
            ok(_) { *out = none }
            err(e) { *out = some(e) }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxCredit[T: MuxEngine + Send + Share](x: &?T, a: (id: i64, gen_: u32, total: i64)) {
    (id, gen_, total) := a
    if t := &*x {
        t.Credit(id, gen_, total)
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxWait[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, ids: []i64, incoming: bool, val: mut MuxReady, err: mut ?Error)) {
    (io, ids, incoming, val, err) := a
    if t := &*x {
        match t.Wait(io, ids, incoming) {
            ok(v) {
                *val = v
                *err = none
            }
            err(e) { *err = some(e) }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxReset[T: MuxEngine + Send + Share](x: &?T, a: (id: i64, gen_: u32, code: u64)) {
    (id, gen_, code) := a
    if t := &*x {
        t.Reset(id, gen_, code)
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxStop[T: MuxEngine + Send + Share](x: &?T, a: (id: i64, gen_: u32, code: u64)) {
    (id, gen_, code) := a
    if t := &*x {
        t.Stop(id, gen_, code)
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxRelease[T: MuxEngine + Send + Share](x: &?T, a: (id: i64, gen_: u32)) {
    (id, gen_) := a
    if t := &*x {
        t.Release(id, gen_)
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxLimits[T: MuxEngine + Send + Share](x: &?T, out: mut MuxLimits) {
    if t := &*x {
        *out = t.Limits()
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxUsable[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, out: mut bool)) {
    (io, out) := a
    if t := &*x {
        *out = t.Usable(io)
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxPeer[T: MuxEngine + Send + Share](x: &?T, out: mut ?Addr) {
    if t := &*x {
        *out = t.PeerAddr()
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxClose[T: MuxEngine + Send + Share](x: &?T, a: (io: Io, code: u64, out: mut ?Error)) {
    (io, code, out) := a
    if t := &*x {
        match t.Close(io, code) {
            ok(_) { *out = none }
            err(e) { *out = some(e) }
        }
        return
    }
    Panic("net.Mux: an empty box")
}

DoMuxAbandon[T: MuxEngine + Send + Share](box: Box[?T], h: &Heap) {
    match box.Take(h.Alloc()) {
        some(t) { t.Abandon() }
        none {}
    }
}

MuxBoxOpen[T: MuxEngine + Send + Share](m: &Mux, io: Io, uni: bool, wait: bool): Error!?(id: i64, gen_: u32) {
    var val: ?(id: i64, gen_: u32) = none
    var err: ?Error = none
    m.box.With[?T, (io: Io, uni: bool, wait: bool, val: mut ?(id: i64, gen_: u32), err: mut ?Error)]((io: io, uni: uni, wait: wait, val: mut val, err: mut err), DoMuxOpen[T])
    if e := err {
        fail e
    }
    return val
}

MuxBoxAccept[T: MuxEngine + Send + Share](m: &Mux, io: Io, uni: bool, wait: bool): Error!?(id: i64, gen_: u32) {
    var val: ?(id: i64, gen_: u32) = none
    var err: ?Error = none
    m.box.With[?T, (io: Io, uni: bool, wait: bool, val: mut ?(id: i64, gen_: u32), err: mut ?Error)]((io: io, uni: uni, wait: wait, val: mut val, err: mut err), DoMuxAccept[T])
    if e := err {
        fail e
    }
    return val
}

MuxBoxRead[T: MuxEngine + Send + Share](m: &Mux, io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): Error!?int {
    var val: ?int = none
    var err: ?Error = none
    m.box.With[?T, (io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool, val: mut ?int, err: mut ?Error)]((io: io, id: id, gen_: gen_, buf: buf, wait: wait, val: mut val, err: mut err), DoMuxRead[T])
    if e := err {
        fail e
    }
    return val
}

MuxBoxWrite[T: MuxEngine + Send + Share](m: &Mux, io: Io, id: i64, gen_: u32, data: []u8, fin: bool): Error!void {
    var out: ?Error = none
    m.box.With[?T, (io: Io, id: i64, gen_: u32, data: []u8, fin: bool, out: mut ?Error)]((io: io, id: id, gen_: gen_, data: data, fin: fin, out: mut out), DoMuxWrite[T])
    if e := out {
        fail e
    }
}

MuxBoxCredit[T: MuxEngine + Send + Share](m: &Mux, id: i64, gen_: u32, total: i64) {
    m.box.With[?T, (id: i64, gen_: u32, total: i64)]((id: id, gen_: gen_, total: total), DoMuxCredit[T])
}

MuxBoxWait[T: MuxEngine + Send + Share](m: &Mux, io: Io, ids: []i64, incoming: bool): Error!MuxReady {
    var val := MuxReady.Incoming(uni: false)
    var err: ?Error = none
    m.box.With[?T, (io: Io, ids: []i64, incoming: bool, val: mut MuxReady, err: mut ?Error)]((io: io, ids: ids, incoming: incoming, val: mut val, err: mut err), DoMuxWait[T])
    if e := err {
        fail e
    }
    return val
}

MuxBoxReset[T: MuxEngine + Send + Share](m: &Mux, id: i64, gen_: u32, code: u64) {
    m.box.With[?T, (id: i64, gen_: u32, code: u64)]((id: id, gen_: gen_, code: code), DoMuxReset[T])
}

MuxBoxStop[T: MuxEngine + Send + Share](m: &Mux, id: i64, gen_: u32, code: u64) {
    m.box.With[?T, (id: i64, gen_: u32, code: u64)]((id: id, gen_: gen_, code: code), DoMuxStop[T])
}

MuxBoxRelease[T: MuxEngine + Send + Share](m: &Mux, id: i64, gen_: u32) {
    m.box.With[?T, (id: i64, gen_: u32)]((id: id, gen_: gen_), DoMuxRelease[T])
}

MuxBoxLimits[T: MuxEngine + Send + Share](m: &Mux): MuxLimits {
    var out := MuxLimits(bidi: 0, uni: 0, idle: core.DurationOf(0))
    m.box.With[?T, mut MuxLimits](mut out, DoMuxLimits[T])
    return out
}

MuxBoxUsable[T: MuxEngine + Send + Share](m: &Mux, io: Io): bool {
    var out := false
    m.box.With[?T, (io: Io, out: mut bool)]((io: io, out: mut out), DoMuxUsable[T])
    return out
}

MuxBoxPeer[T: MuxEngine + Send + Share](m: &Mux): ?Addr {
    var out: ?Addr = none
    m.box.With[?T, mut ?Addr](mut out, DoMuxPeer[T])
    return out
}

MuxBoxClose[T: MuxEngine + Send + Share](m: &Mux, io: Io, code: u64): Error!void {
    var out: ?Error = none
    m.box.With[?T, (io: Io, code: u64, out: mut ?Error)]((io: io, code: code, out: mut out), DoMuxClose[T])
    if e := out {
        fail e
    }
}

MuxBoxAbandon[T: MuxEngine + Send + Share](box: core.SealedBox, h: &Heap) {
    box.TakeWith[?T, &Heap](h, DoMuxAbandon[T])
}

/// The Mux over the engine boxed in `b` (made with core.SealedBox.Fill[T])
/// on `h`. Only std builds one. O(1); allocates nothing.
export Mux.Of[T: MuxEngine + Send + Share](h: &Heap, b: core.SealedBox): Mux from h, b {
    return Mux(
        box: b,
        heap: h,
        do_open: MuxBoxOpen[T],
        do_accept: MuxBoxAccept[T],
        do_read: MuxBoxRead[T],
        do_write: MuxBoxWrite[T],
        do_credit: MuxBoxCredit[T],
        do_wait: MuxBoxWait[T],
        do_reset: MuxBoxReset[T],
        do_stop: MuxBoxStop[T],
        do_release: MuxBoxRelease[T],
        do_limits: MuxBoxLimits[T],
        do_usable: MuxBoxUsable[T],
        do_peer: MuxBoxPeer[T],
        do_close: MuxBoxClose[T],
        do_abandon: MuxBoxAbandon[T],
    )
}

/// Opens a stream. With `wait` false, returns none immediately when the
/// peer's stream limit is reached; with `wait` true, waits for credit.
/// Errors: Canceled, MuxClosed, or engine errors. O(1) besides any wait.
export Mux.Open(&self, io: Io, uni: bool, wait: bool): Error!?MuxStream {
    f := self.do_open
    res := try f(self, io, uni, wait)
    match res {
        some(p) {
            (id, gen_) := p
            return some(MuxStream(id: id, gen_: gen_))
        }
        none {
            return none
        }
    }
}

/// Returns the next stream the peer opened; none at once when `wait` is
/// false. Errors: Canceled, MuxClosed, or engine errors. O(1) besides any
/// wait.
export Mux.Accept(&self, io: Io, uni: bool, wait: bool): Error!?MuxStream {
    f := self.do_accept
    res := try f(self, io, uni, wait)
    match res {
        some(p) {
            (id, gen_) := p
            return some(MuxStream(id: id, gen_: gen_))
        }
        none {
            return none
        }
    }
}

/// Reads into `buf`: the byte count, 0 at the stream's end, or none at once
/// when `wait` is false and nothing is queued. Fails with StreamReset when
/// the peer reset the stream, MuxClosed when the connection ended, or
/// Canceled (with nothing consumed). O(buf.Len()) besides any wait.
export Mux.Read(&self, io: Io, s: &MuxStream, buf: mut []u8, wait: bool): Error!?int {
    f := self.do_read
    return try f(self, io, s.id, s.gen_, buf, wait)
}

/// Writes all of `data`, then the stream's end when `fin` is true. Waits for
/// flow-control credit. Fails with StreamReset when the peer sent
/// STOP_SENDING, MuxClosed, or Canceled. O(data.Len()) besides any wait.
export Mux.Write(&self, io: Io, s: &MuxStream, data: []u8, fin: bool): Error!void {
    f := self.do_write
    try f(self, io, s.id, s.gen_, data, fin)
}

/// Raises the bytes the peer may send on `s` to at least `total` (from the
/// stream's start). Never waits; queues a MAX_STREAM_DATA. O(1); allocates
/// nothing.
export Mux.Credit(&self, s: &MuxStream, total: i64) {
    f := self.do_credit
    f(self, s.id, s.gen_, total)
}

/// Waits until one of `ids` is readable (data, end, or reset) or, when
/// `incoming` is true, a new peer stream can be accepted. Errors: Canceled,
/// MuxClosed. O(ids.Len()) per wake.
export Mux.Wait(&self, io: Io, ids: []i64, incoming: bool): Error!MuxReady {
    f := self.do_wait
    return try f(self, io, ids, incoming)
}

/// Abandons sending on `s` (RESET_STREAM) with `code`. Never waits. O(1).
export Mux.Reset(&self, s: &MuxStream, code: u64) {
    f := self.do_reset
    f(self, s.id, s.gen_, code)
}

/// Abandons receiving on `s` (STOP_SENDING) with `code`. Never waits. O(1).
export Mux.Stop(&self, s: &MuxStream, code: u64) {
    f := self.do_stop
    f(self, s.id, s.gen_, code)
}

/// Releases the stream handle `s` back to the connection. O(1).
export Mux.Release(&self, s: MuxStream) {
    MuxStream(id, gen_) := s
    f := self.do_release
    f(self, id, gen_)
}

/// Returns the peer's current stream and idle limits. O(1).
export Mux.Limits(&self): MuxLimits {
    f := self.do_limits
    return f(self)
}

/// Processes what is queued without waiting and reports whether the
/// connection can take a new stream. O(datagrams queued).
export Mux.Usable(&self, io: Io): bool {
    f := self.do_usable
    return f(self, io)
}

/// Returns the peer's address, if known. O(1).
export Mux.PeerAddr(&self): ?Addr {
    f := self.do_peer
    return f(self)
}

/// Closes the connection with application `code` (no draining wait). On
/// failure, `self` stays the caller's to Abandon or retry. O(1) besides
/// sending the close frame.
export Mux.Close(self, io: Io, code: u64): Error!void {
    f := self.do_close
    try f(&self, io, code)
    self.Abandon()
}

/// Releases the connection without waiting or speaking to the peer. O(1)
/// here plus the engine's Abandon.
export Mux.Abandon(self) {
    Mux(box, heap, do_abandon, ..) := self
    do_abandon(box, heap)
}

/// The contract a multiplexed server listener implements for MuxListener.Of.
export interface MuxListening {
    Run(&self, io: Io): Error!void
    Accept(&self, h: &Heap, io: Io): Error!Mux from h
    Port(&self): int
    Abandon(self)
}

/// A bound multiplexed server endpoint. Run reads the socket and routes
/// datagrams until its scope is cancelled; Accept takes the next connection
/// whose handshake may begin. Linear: release with Close.
export linear view struct MuxListener {
    box: core.SealedBox
    heap: &Heap
    do_run: fn(&MuxListener, Io): Error!void
    do_accept: fn(&MuxListener, h: &Heap, Io): Error!Mux from h
    do_port: fn(&MuxListener): int
    do_abandon: fn(core.SealedBox, &Heap)
}

DoListRun[T: MuxListening + Send + Share](x: &?T, a: (io: Io, out: mut ?Error)) {
    (io, out) := a
    if t := &*x {
        match t.Run(io) {
            ok(_) { *out = none }
            err(e) { *out = some(e) }
        }
        return
    }
    Panic("net.MuxListener: an empty box")
}

DoListAccept[T: MuxListening + Send + Share](x: &?T, h: &Heap into out, a: (io: Io, err: mut ?Error), out: mut ?Mux) {
    (io, err) := a
    if t := &*x {
        match t.Accept(h, io) {
            ok(m) {
                match core.Replace(mut *out, some(m)) {
                    some(old) { old.Abandon() }
                    none {}
                }
                *err = none
            }
            err(e) {
                *err = some(e)
            }
        }
        return
    }
    Panic("net.MuxListener: an empty box")
}

DoListPort[T: MuxListening + Send + Share](x: &?T, out: mut int) {
    if t := &*x {
        *out = t.Port()
        return
    }
    Panic("net.MuxListener: an empty box")
}

DoListAbandon[T: MuxListening + Send + Share](box: Box[?T], h: &Heap) {
    match box.Take(h.Alloc()) {
        some(t) { t.Abandon() }
        none {}
    }
}

ListBoxRun[T: MuxListening + Send + Share](l: &MuxListener, io: Io): Error!void {
    var out: ?Error = none
    l.box.With[?T, (io: Io, out: mut ?Error)]((io: io, out: mut out), DoListRun[T])
    if e := out {
        fail e
    }
}

ListBoxAccept[T: MuxListening + Send + Share](l: &MuxListener, h: &Heap, io: Io): Error!Mux from h {
    var val: ?Mux = none
    var err: ?Error = none
    l.box.WithInto[?T, &Heap, (io: Io, err: mut ?Error), ?Mux](h, (io: io, err: mut err), mut val, DoListAccept[T])
    if e := err {
        match val {
            some(m) { m.Abandon() }
            none {}
        }
        fail e
    }
    return val else {
        fail Closed
    }
}

ListBoxPort[T: MuxListening + Send + Share](l: &MuxListener): int {
    var out := 0
    l.box.With[?T, mut int](mut out, DoListPort[T])
    return out
}

ListBoxAbandon[T: MuxListening + Send + Share](box: core.SealedBox, h: &Heap) {
    box.TakeWith[?T, &Heap](h, DoListAbandon[T])
}

/// The MuxListener over the listening endpoint boxed in `b` on `h`. Only std
/// builds one. O(1); allocates nothing.
export MuxListener.Of[T: MuxListening + Send + Share](h: &Heap, b: core.SealedBox): MuxListener from h, b {
    return MuxListener(
        box: b,
        heap: h,
        do_run: ListBoxRun[T],
        do_accept: ListBoxAccept[T],
        do_port: ListBoxPort[T],
        do_abandon: ListBoxAbandon[T],
    )
}

/// Reads the listener's socket and routes packets until `io`'s scope is
/// cancelled. Errors: Canceled or socket errors.
export MuxListener.Run(&self, io: Io): Error!void {
    f := self.do_run
    try f(self, io)
}

/// Accepts the next incoming multiplexed connection, allocated on `self`'s
/// heap. Errors: Canceled, Closed, or engine errors.
export MuxListener.Accept(&self, io: Io): Error!Mux {
    f := self.do_accept
    return try f(self, self.heap, io)
}

/// Returns the bound port. O(1).
export MuxListener.Port(&self): int {
    f := self.do_port
    return f(self)
}

/// Releases the listener and its socket. O(1) here plus Abandon.
export MuxListener.Close(self) {
    MuxListener(box, heap, do_abandon, ..) := self
    do_abandon(box, heap)
}

/// A client connector of multiplexed connections, made only by std.
export plain view struct MuxDialer {
    call: core.ErasedCall
    thunk: fn(&MuxDialer, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h
}

DialerCall[T: Share](d: &MuxDialer, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h {
    f := d.call.Fn[T, fn(&T, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h]()
    st := d.call.State[T, fn(&T, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h]()
    return try f(st, h, io, to, host, alpn)
}

/// Wraps `c` (made with core.ErasedCall.Of[T, F]) as a MuxDialer. Only std
/// calls this. O(1); allocates nothing.
export MuxDialer.Of[T: Share](c: core.ErasedCall): MuxDialer from c {
    return MuxDialer(call: c, thunk: DialerCall[T])
}

/// Opens a multiplexed connection to `to`, checks the peer's certificate for
/// `host`, and asks for `alpn`. Errors: Canceled, SecureFailed, or network
/// errors. O(1) here plus handshake cost; allocates on `h`.
export MuxDialer.Dial(&self, h: &Heap, io: Io, to: Addr, host: str, alpn: str): Error!Mux from h {
    thunk := self.thunk
    return try thunk(self, h, io, to, host, alpn)
}

/// A server endpoint factory of multiplexed connections, made only by std.
export plain view struct MuxServer {
    call: core.ErasedCall
    thunk: fn(s: &MuxServer, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, s
}

ServerCall[T: Share](s: &MuxServer, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, s {
    f := s.call.Fn[T, fn(st: &T, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, st]()
    st := s.call.State[T, fn(st: &T, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, st]()
    return try f(st, h, io, addr, alpn)
}

/// Wraps `c` (made with core.ErasedCall.Of[T, F]) as a MuxServer. Only std
/// calls this. O(1); allocates nothing.
export MuxServer.Of[T: Share](c: core.ErasedCall): MuxServer from c {
    return MuxServer(call: c, thunk: ServerCall[T])
}

/// Binds UDP on `addr` ("host:port") and returns a MuxListener. Errors:
/// InvalidAddress, AddrInUse, Canceled, or network errors. O(1) here plus
/// socket setup; allocates on `h`.
export MuxServer.Listen(&self, h: &Heap, io: Io, addr: str, alpn: str): Error!MuxListener from h, self {
    thunk := self.thunk
    return try thunk(self, h, io, addr, alpn)
}
