// Why: an HTTP/3 connection owns connection-wide state over a net.Mux:
// control stream framing, peer SETTINGS, GOAWAY tracking, incoming stream
// dispatch, and the QPACK decoder/encoder pair.
//
// What: Session, with Client, Server, ClientWithQpack, ServerWithQpack, Poll,
// Serve, Goaway, SendGoaway, PeerSettings, Close, and its Free impl.
//
// How: Session borrows &net.Mux and separates uni-stream polling (poll_mu),
// control/decoder stream writes (ctrl_wr_mu, qdec_wr_mu), and leaf shared
// state (state_mu) so no lock is ever held across a blocking Mux call.

import std.core
import std.http.fields
import std.net
import std.raw
import std.rt
import std.sync

// PendingUni tracks a newly accepted unidirectional stream whose 1..8-byte
// stream-type varint has not yet completed.
linear struct PendingUni {
    stream: net.MuxStream
    buf: [8]u8
    len: int
}

// CtrlReader buffers and parses frames incrementally from the peer's control
// stream (type 0x00).
linear view struct CtrlReader {
    stream: net.MuxStream
    buf: HeapList[u8]
    pos: int
    end: int
    seen_settings: bool
    in_frame: bool
    have_hdr: bool
    frame_kind: u64
    frame_len: u64
    fin: bool
}

CtrlReader.New(a: &Alloc, s: net.MuxStream): OutOfMemory!CtrlReader {
    var b := HeapList[u8].New(a)
    onerr b.Free(a)
    for _ in 0..4096 {
        try b.Push(a, 0)
    }
    return CtrlReader(
        stream: s,
        buf: b,
        pos: 0,
        end: 0,
        seen_settings: false,
        in_frame: false,
        have_hdr: false,
        frame_kind: 0,
        frame_len: 0,
        fin: false,
    )
}

CtrlReader.Free(self, a: &Alloc, m: &net.Mux) {
    CtrlReader(stream, buf, ..) := self
    m.Release(stream)
    buf.Free(a)
}

// UniBufStream buffers partial QPACK encoder/decoder stream instructions.
linear view struct UniBufStream {
    stream: net.MuxStream
    buf: HeapList[u8]
    len: int
}

UniBufStream.New(a: &Alloc, s: net.MuxStream, cap: int): OutOfMemory!UniBufStream {
    var b := HeapList[u8].New(a)
    onerr b.Free(a)
    for _ in 0..cap {
        try b.Push(a, 0)
    }
    return UniBufStream(stream: s, buf: b, len: 0)
}

UniBufStream.Free(self, a: &Alloc, m: &net.Mux) {
    UniBufStream(stream, buf, ..) := self
    m.Release(stream)
    buf.Free(a)
}

// PollState holds peer unidirectional stream readers serialized by poll_mu.
linear view struct PollState {
    peer_ctrl: ?CtrlReader
    peer_qenc: ?UniBufStream
    peer_qdec: ?UniBufStream
    pending_uni: HeapList[PendingUni]
}

PollState.Free(self, a: &Alloc, m: &net.Mux) {
    PollState(peer_ctrl, peer_qenc, peer_qdec, pending_uni) := self
    match peer_ctrl {
        some(c) { c.Free(a, m) }
        none {}
    }
    match peer_qenc {
        some(q) { q.Free(a, m) }
        none {}
    }
    match peer_qdec {
        some(q) { q.Free(a, m) }
        none {}
    }
    var pu := pending_uni
    for {
        p := pu.Pop() else {
            break
        }
        PendingUni(stream, ..) := p
        m.Release(stream)
    }
    pu.FreeEmpty(a)
}

// SessionInner holds connection state protected by state_mu (strict leaf lock).
linear view struct SessionInner {
    accept_q: HeapList[net.MuxStream]
    peer_settings: Settings
    peer_settings_received: bool
    goaway_recv: ?i64
    goaway_sent: ?i64
    max_push_id_recv: ?u64
    fatal_err: ?Violation
    closed: bool
    serving: bool
    reader_done: bool
    has_qdec_out: bool
    qdec: fields.QDecoder
    qenc: fields.QEncoder
}

SessionInner.Free(self, a: &Alloc, m: &net.Mux) {
    SessionInner(accept_q, qdec, ..) := self
    var aq := accept_q
    for {
        s := aq.Pop() else {
            break
        }
        m.Release(s)
    }
    aq.FreeEmpty(a)
    qdec.Free(a)
}

/// The HTTP/3 state of one connection over a net.Mux: control stream, peer
/// SETTINGS, GOAWAY tracking, incoming stream dispatch, and QPACK state.
/// Linear: free with Box.Free or Session.Close. Send and Share.
export linear mut view struct Session {
    m: &net.Mux
    heap: &Heap
    is_server: bool
    header_bytes: int
    qpack_cap: int
    qpack_blocked: int
    poll_mu: Mutex[PollState]
    ctrl_wr_mu: Mutex[?net.MuxStream]
    qdec_wr_mu: Mutex[?net.MuxStream]
    state_mu: Mutex[SessionInner]
    accept_ev: sync.Notify
    qpack_epoch: raw.Atomic[u32]
}

/// Releases all owned MuxStreams and buffers of self. O(open streams);
/// allocates nothing.
impl Free for Session {
    Free(self, a: &Alloc) {
        Session(m, heap, poll_mu, ctrl_wr_mu, qdec_wr_mu, state_mu, accept_ev, qpack_epoch, ..) := self
        _ = heap
        _ = accept_ev
        _ = qpack_epoch
        poll_mu.Into().Free(a, m)
        ReleaseOptStream(m, ctrl_wr_mu.Into())
        ReleaseOptStream(m, qdec_wr_mu.Into())
        state_mu.Into().Free(a, m)
    }
}

// NormMuxErr maps unknown application close/reset codes to H3NoError (RFC 9114 8).
NormMuxErr(e: net.Error): Error {
    return match e {
        net.StreamReset(code) { net.StreamReset(code: NormError(code)) }
        net.MuxClosed(code, app, peer) {
            net.MuxClosed(code: if app { NormError(code) } else { code }, app: app, peer: peer)
        }
        _ { e }
    }
}

ReleaseOptStream(m: &net.Mux, opt: ?net.MuxStream) {
    if s := opt {
        m.Release(s)
    }
}

// RecordFatal stores the first connection-level Violation and wakes waiters.
Session.RecordFatal(&self, io: Io, code: u64): Violation {
    var g := self.state_mu.Lock(io.NoCancel()).Must()
    st := mut *g.Mut()
    v := if prev := st.fatal_err {
        prev
    } else {
        nv := Violation(code: code, stream: -1)
        st.fatal_err = some(nv)
        nv
    }
    g.Unlock()
    self.accept_ev.NotifyAll()
    self.WakeQpack(io)
    return v
}

// CheckFatal returns an error if the session has already failed fatally.
Session.CheckFatal(&self, io: Io): Error!void {
    var g := self.state_mu.Lock(io.NoCancel()).Must()
    st := &*g.Ref()
    fe := st.fatal_err
    cl := st.closed
    g.Unlock()
    if v := fe {
        fail v
    }
    if cl {
        fail Closed
    }
}

// OpenQpackDecoderStream opens the QPACK decoder stream (0x03) when qc > 0.
OpenQpackDecoderStream(io: Io, m: &net.Mux): Error!net.MuxStream {
    qd := (m.Open(io, true, true) else e {
        fail NormMuxErr(e)
    }) else {
        fail Closed
    }
    onerr m.Release(qd)
    var qbuf: [8]u8 = [0; 8]
    qn := WriteVarint(mut qbuf[..], StreamQpackDecoder)
    m.Write(io, &qd, qbuf[0..qn], false) else e {
        fail NormMuxErr(e)
    }
    return qd
}

// NewSession creates a client or server HTTP/3 Session, opens its control
// stream (and QPACK decoder stream if qpack_cap > 0) and sends SETTINGS
// immediately without waiting for peer data (RFC 9114 6.2.1, 7.2.4.2).
NewSession(h: &Heap, io: Io, m: &net.Mux, header_bytes: int, is_server: bool, qpack_cap: int, qpack_blocked: int): Error!Box[Session] from h, m {
    hb := if header_bytes < 0 { 0 } else { header_bytes }
    qc := if qpack_cap < 0 { 0 } else { qpack_cap }
    qb := if qpack_blocked < 0 { 0 } else { qpack_blocked }
    a := h.Alloc()

    var qdec := try fields.QDecoder.New(a, qc, qb, hb)
    onerr qdec.Free(a)

    ctrl := (m.Open(io, true, true) else e {
        fail NormMuxErr(e)
    }) else {
        fail Closed
    }
    onerr m.Release(ctrl)

    var sbuf: [128]u8 = [0; 128]
    tlen := WriteVarint(mut sbuf[..], StreamControl)
    slen := EncodeSettings(mut sbuf[tlen..], some(Cast[u64](hb)), Cast[u64](qc), Cast[u64](qb), true)
    if tlen == 0 || slen == 0 {
        fail Violation(code: H3InternalError, stream: -1)
    }
    m.Write(io, &ctrl, sbuf[0..tlen + slen], false) else e {
        fail NormMuxErr(e)
    }

    qdec_out: ?net.MuxStream = if qc > 0 {
        some(try OpenQpackDecoderStream(io, m))
    } else {
        none
    }

    ps := PollState(
        peer_ctrl: none,
        peer_qenc: none,
        peer_qdec: none,
        pending_uni: HeapList[PendingUni].New(a),
    )
    inner := SessionInner(
        accept_q: HeapList[net.MuxStream].New(a),
        peer_settings: InitialSettings,
        peer_settings_received: false,
        goaway_recv: none,
        goaway_sent: none,
        max_push_id_recv: none,
        fatal_err: none,
        closed: false,
        serving: false,
        reader_done: false,
        has_qdec_out: qc > 0,
        qdec: qdec,
        qenc: fields.QEncoder.New(),
    )
    sess := Session(
        m: m,
        heap: h,
        is_server: is_server,
        header_bytes: hb,
        qpack_cap: qc,
        qpack_blocked: qb,
        poll_mu: Mutex[PollState].New(ps),
        ctrl_wr_mu: Mutex[?net.MuxStream].New(some(ctrl)),
        qdec_wr_mu: Mutex[?net.MuxStream].New(qdec_out),
        state_mu: Mutex[SessionInner].New(inner),
        accept_ev: sync.Notify.New(),
        qpack_epoch: raw.Atomic[u32].New(0),
    )
    return Box[Session].New(a, sess) else e {
        sess.Free(a)
        fail e
    }
}

/// Creates a client HTTP/3 Session over m, immediately sending the control
/// stream and SETTINGS with MAX_FIELD_SECTION_SIZE = header_bytes (RFC 9114
/// 6.2.1, 7.2.4.2). O(1) besides stream open; allocates on h.
export Session.Client(h: &Heap, io: Io, m: &net.Mux, header_bytes: int): Error!Box[Session] from h, m {
    return try NewSession(h, io, m, header_bytes, false, 0, 0)
}

/// Creates a server HTTP/3 Session over m, immediately sending the control
/// stream and SETTINGS with MAX_FIELD_SECTION_SIZE = header_bytes (RFC 9114
/// 6.2.1, 7.2.4.2). O(1) besides stream open; allocates on h.
export Session.Server(h: &Heap, io: Io, m: &net.Mux, header_bytes: int): Error!Box[Session] from h, m {
    return try NewSession(h, io, m, header_bytes, true, 0, 0)
}

/// Creates a client HTTP/3 Session with explicit QPACK dynamic table limits
/// (qpack_cap, qpack_blocked) advertised in SETTINGS. O(qpack_cap); allocates
/// on h.
export Session.ClientWithQpack(h: &Heap, io: Io, m: &net.Mux, header_bytes: int, qpack_cap: int, qpack_blocked: int): Error!Box[Session] from h, m {
    return try NewSession(h, io, m, header_bytes, false, qpack_cap, qpack_blocked)
}

/// Creates a server HTTP/3 Session with explicit QPACK dynamic table limits
/// (qpack_cap, qpack_blocked) advertised in SETTINGS. O(qpack_cap); allocates
/// on h.
export Session.ServerWithQpack(h: &Heap, io: Io, m: &net.Mux, header_bytes: int, qpack_cap: int, qpack_blocked: int): Error!Box[Session] from h, m {
    return try NewSession(h, io, m, header_bytes, true, qpack_cap, qpack_blocked)
}

/// Returns the identifier of the last GOAWAY received, or none if none has
/// arrived (RFC 9114 7.2.6). O(1); allocates nothing.
export Session.Goaway(&self): ?i64 {
    var g := self.state_mu.Lock(core.IoMasked(rt.NativeIo())).Must()
    v := (&*g.Ref()).goaway_recv
    g.Unlock()
    return v
}

/// Returns the peer's current HTTP/3 settings (defaults until SETTINGS
/// arrives, RFC 9114 7.2.4.2). O(1); allocates nothing.
export Session.PeerSettings(&self): Settings {
    var g := self.state_mu.Lock(core.IoMasked(rt.NativeIo())).Must()
    v := (&*g.Ref()).peer_settings
    g.Unlock()
    return v
}

/// Sends a GOAWAY frame with identifier id on the connection's control stream
/// (RFC 9114 5.2, 7.2.6). On a server, id must be a non-negative client-bidi
/// stream id (id % 4 == 0) and may not exceed a previously sent GOAWAY id.
/// Errors: Violation(H3IdError, -1), Closed, or net.Error. O(1); allocates
/// nothing.
export Session.SendGoaway(&self, io: Io, id: i64): Error!void {
    try self.CheckFatal(io)
    if id < 0 || (self.is_server && id % 4 != 0) {
        fail Violation(code: H3IdError, stream: -1)
    }
    var cw := try self.ctrl_wr_mu.Lock(io)
    defer cw.Unlock()

    var g := self.state_mu.Lock(io.NoCancel()).Must()
    st := mut *g.Mut()
    if v := st.fatal_err {
        g.Unlock()
        fail v
    }
    if st.closed {
        g.Unlock()
        fail Closed
    }
    if prev := st.goaway_sent {
        if id > prev {
            g.Unlock()
            fail Violation(code: H3IdError, stream: -1)
        }
    }
    st.goaway_sent = some(id)
    g.Unlock()

    var buf: [16]u8 = [0; 16]
    n := WriteSingleIdFrame(mut buf[..], FrameGoaway, Cast[u64](id))
    if n == 0 {
        fail Violation(code: H3IdError, stream: -1)
    }
    if ctrl := &*cw.Ref() {
        self.m.Write(io, ctrl, buf[0..n], false) else e {
            fail NormMuxErr(e)
        }
    } else {
        fail Closed
    }
}

/// Sends an empty reserved frame on the control stream to keep the
/// transport active (RFC 9114 7.2.8). Errors: Closed or net.Error.
/// O(1) besides the write; allocates nothing.
export Session.KeepAlive(&self, io: Io): Error!void {
    try self.CheckFatal(io)
    cw := try self.ctrl_wr_mu.Lock(io)
    defer cw.Unlock()
    if ctrl := &*cw.Ref() {
        var buf: [16]u8 = [0; 16]
        n := WriteFrameHeader(mut buf[..], GreaseId(0), 0)
        self.m.Write(io, ctrl, buf[0..n], false) else e { fail NormMuxErr(e) }
        return
    }
    fail Closed
}

/// Marks the session closed and releases its resources on a. Does not close
/// the borrowed net.Mux itself (its owner calls Mux.Close or Mux.Abandon).
/// O(open streams); allocates nothing.
export Session.Close(self, a: &Alloc) {
    self.Free(a)
}

// FlushQpackDecoder writes any owed QPACK decoder-stream instructions to
// qdec_out if open.
Session.FlushQpackDecoder(&self, io: Io): Error!void {
    var qw := try self.qdec_wr_mu.Lock(io)
    defer qw.Unlock()
    var buf: [256]u8 = [0; 256]
    for {
        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        n := st.qdec.Owed(mut buf[..])
        g.Unlock()
        if n == 0 {
            return
        }
        if qout := &*qw.Ref() {
            self.m.Write(io, qout, buf[0..n], false) else e {
                fail NormMuxErr(e)
            }
        } else {
            return
        }
    }
}

// CancelQpackStream cancels QPACK decoder state for stream id and flushes
// any resulting Stream Cancellation instruction.
Session.CancelQpackStream(&self, stream_id: i64) {
    io := core.IoMasked(rt.NativeIo())
    var g := self.state_mu.Lock(io).Must()
    st := mut *g.Mut()
    st.qdec.Cancel(stream_id)
    has_qout := st.has_qdec_out
    g.Unlock()
    if has_qout {
        self.FlushQpackDecoder(io) else {
        }
    }
}

// ApplyControlFrame validates and applies one parsed control-stream frame.
Session.ApplyControlFrame(&self, io: Io, kind: u64, payload: []u8): Error!void {
    a := self.heap.Alloc()
    if kind == FrameSettings {
        var next := InitialSettings
        next.Apply(a, payload) else e {
            match e {
                Violation(code, stream: _) { fail self.RecordFatal(io, code) }
                OutOfMemory { fail OutOfMemory }
            }
        }
        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        st.peer_settings = next
        st.peer_settings_received = true
        g.Unlock()
        return
    }
    if kind == FrameGoaway {
        id := ParseSingleIdPayload(payload) else e {
            fail self.RecordFatal(io, e.code)
        }
        if !self.is_server && id % 4 != 0 {
            fail self.RecordFatal(io, H3IdError)
        }
        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        if prev := st.goaway_recv {
            if Cast[i64](id) > prev {
                g.Unlock()
                fail self.RecordFatal(io, H3IdError)
            }
        }
        st.goaway_recv = some(Cast[i64](id))
        g.Unlock()
        return
    }
    if kind == FrameCancelPush {
        _ = ParseSingleIdPayload(payload) else e {
            fail self.RecordFatal(io, e.code)
        }
        fail self.RecordFatal(io, H3IdError)
    }
    if kind == FrameMaxPushId {
        push_id := ParseSingleIdPayload(payload) else e {
            fail self.RecordFatal(io, e.code)
        }
        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        if prev := st.max_push_id_recv {
            if push_id < prev {
                g.Unlock()
                fail self.RecordFatal(io, H3IdError)
            }
        }
        st.max_push_id_recv = some(push_id)
        g.Unlock()
        return
    }
    // Unknown or GREASE frame on the control stream: ignore payload (H3-17).
}

// CompactCtrl shifts unread bytes to the start of cr.buf.
CompactCtrl(cr: mut CtrlReader) {
    avail := cr.end - cr.pos
    if cr.pos > 0 && avail > 0 {
        items := cr.buf.ItemsMut()
        for i in 0..avail {
            items[i] = items[cr.pos + i]
        }
    }
    cr.pos = 0
    cr.end = avail
}

// DrainControl reads all immediately available bytes and frames on cr.
Session.DrainControl(&self, io: Io, cr: mut CtrlReader): Error!void {
    a := self.heap.Alloc()
    for {
        for {
            if !cr.have_hdr {
                avail := cr.end - cr.pos
                cr.in_frame = avail > 0
                if avail == 0 {
                    break
                }
                hdr := ParseFrameHeader(cr.buf.Items()[cr.pos..cr.end]) else {
                    break
                }
                cr.pos += hdr.header_len
                cr.in_frame = true
                if !cr.seen_settings {
                    if hdr.kind != FrameSettings {
                        fail self.RecordFatal(io, H3MissingSettings)
                    }
                    cr.seen_settings = true
                } else {
                    if hdr.kind == FrameSettings || IsReservedH2Frame(hdr.kind) || hdr.kind == FrameData || hdr.kind == FrameHeaders || hdr.kind == FramePushPromise {
                        fail self.RecordFatal(io, H3FrameUnexpected)
                    }
                    if hdr.kind == FrameMaxPushId && !self.is_server {
                        fail self.RecordFatal(io, H3FrameUnexpected)
                    }
                }
                if hdr.length > Cast[u64](MaxControlFrameBytes) {
                    fail self.RecordFatal(io, H3ExcessiveLoad)
                }
                cr.have_hdr = true
                cr.frame_kind = hdr.kind
                cr.frame_len = hdr.length
            }
            need := Cast[int](cr.frame_len)
            if cr.end - cr.pos < need {
                break
            }
            try self.ApplyControlFrame(io, cr.frame_kind, cr.buf.Items()[cr.pos..cr.pos + need])
            cr.pos += need
            cr.have_hdr = false
            cr.in_frame = cr.end > cr.pos
        }

        CompactCtrl(cr)
        need_cap := if cr.have_hdr { Cast[int](cr.frame_len) } else { 64 }
        for cr.buf.Len() < need_cap {
            try cr.buf.Push(a, 0)
        }
        got := (self.m.Read(io, &cr.stream, mut cr.buf.ItemsMut()[cr.end..], false) else e {
            match e {
                net.StreamReset(code: _) { fail self.RecordFatal(io, H3ClosedCriticalStream) }
                _ { fail NormMuxErr(e) }
            }
        }) else {
            return
        }
        if got == 0 {
            if cr.in_frame || cr.have_hdr || cr.end > cr.pos {
                fail self.RecordFatal(io, H3FrameError)
            }
            fail self.RecordFatal(io, H3ClosedCriticalStream)
        }
        cr.end += got
    }
}

// DrainQpackEnc reads available QPACK encoder-stream instructions (type 0x02).
Session.DrainQpackEnc(&self, io: Io, qs: mut UniBufStream): Error!void {
    a := self.heap.Alloc()
    for {
        if qs.len == qs.buf.Len() {
            cap_limit := if self.qpack_cap > 0 { self.qpack_cap * 2 + 4096 } else { 8192 }
            if qs.buf.Len() >= cap_limit {
                fail self.RecordFatal(io, QpackEncoderStreamError)
            }
            for _ in 0..4096 {
                try qs.buf.Push(a, 0)
            }
        }
        got := (self.m.Read(io, &qs.stream, mut qs.buf.ItemsMut()[qs.len..], false) else e {
            match e {
                net.StreamReset(code: _) { fail self.RecordFatal(io, H3ClosedCriticalStream) }
                _ { fail NormMuxErr(e) }
            }
        }) else {
            return
        }
        if got == 0 {
            fail self.RecordFatal(io, H3ClosedCriticalStream)
        }
        qs.len += got

        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        cons := st.qdec.Instructions(a, qs.buf.Items()[0..qs.len]) else e {
            g.Unlock()
            match e {
                OutOfMemory { fail OutOfMemory }
                _ { fail self.RecordFatal(io, QpackEncoderStreamError) }
            }
        }
        var woke := false
        if cons > 0 {
            var tmp: [32]i64 = [0; 32]
            for {
                u := st.qdec.Unblocked(mut tmp[..])
                if u > 0 {
                    woke = true
                }
                if u < tmp.Len() {
                    break
                }
            }
        }
        g.Unlock()
        if cons > 0 {
            rem := qs.len - cons
            items := qs.buf.ItemsMut()
            for i in 0..rem {
                items[i] = items[cons + i]
            }
            qs.len = rem
        }
        if woke {
            self.WakeQpack(io)
        }
    }
}

// DrainQpackDec validates available QPACK decoder-stream bytes (type 0x03).
Session.DrainQpackDec(&self, io: Io, qs: mut UniBufStream): Error!void {
    for {
        got := (self.m.Read(io, &qs.stream, mut qs.buf.ItemsMut()[qs.len..], false) else e {
            match e {
                net.StreamReset(code: _) { fail self.RecordFatal(io, H3ClosedCriticalStream) }
                _ { fail NormMuxErr(e) }
            }
        }) else {
            return
        }
        if got == 0 {
            fail self.RecordFatal(io, H3ClosedCriticalStream)
        }
        qs.len += got

        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := &*g.Ref()
        cons := st.qenc.PeerInstructions(qs.buf.Items()[0..qs.len]) else {
            g.Unlock()
            fail self.RecordFatal(io, QpackDecoderStreamError)
        }
        g.Unlock()
        if cons > 0 {
            rem := qs.len - cons
            items := qs.buf.ItemsMut()
            for i in 0..rem {
                items[i] = items[cons + i]
            }
            qs.len = rem
        }
        if qs.len == qs.buf.Len() {
            fail self.RecordFatal(io, QpackDecoderStreamError)
        }
    }
}

// StepPendingUni reads bytes of a pending unidirectional stream until its
// stream-type varint completes or the stream closes/resets before its header.
Session.StepPendingUni(&self, io: Io, p: mut PendingUni): Error!(done: bool, stype: u64) {
    if p.len == 0 {
        got := (self.m.Read(io, &p.stream, mut p.buf[0..1], false) else e {
            match e {
                net.StreamReset(code: _) { return (done: true, stype: u64(0xFFFF)) }
                _ { fail NormMuxErr(e) }
            }
        }) else {
            return (done: false, stype: 0)
        }
        if got == 0 {
            return (done: true, stype: u64(0xFFFF))
        }
        p.len = 1
    }
    need := 1 << int(p.buf[0] >> 6)
    for p.len < need {
        got := (self.m.Read(io, &p.stream, mut p.buf[p.len..need], false) else e {
            match e {
                net.StreamReset(code: _) { return (done: true, stype: u64(0xFFFF)) }
                _ { fail NormMuxErr(e) }
            }
        }) else {
            return (done: false, stype: 0)
        }
        if got == 0 {
            return (done: true, stype: u64(0xFFFF))
        }
        p.len += got
    }
    r := ReadVarint(p.buf[0..need], 0).Must()
    return (done: true, stype: r.v)
}

// DispatchUniStream assigns a newly identified unidirectional stream by type,
// consuming s on every path and returning any error instead of failing.
Session.DispatchUniStream(&self, io: Io, ps: mut PollState, s: net.MuxStream, stype: u64): ?Error {
    a := self.heap.Alloc()
    if stype == StreamControl {
        if ps.peer_ctrl.IsSome() {
            self.m.Release(s)
            return some(self.RecordFatal(io, H3StreamCreationError))
        }
        var cr := CtrlReader.New(a, s) else e {
            self.m.Release(s)
            return some(e)
        }
        self.DrainControl(io, mut cr) else e {
            cr.Free(a, self.m)
            return some(e)
        }
        if old := core.Replace(mut ps.peer_ctrl, some(cr)) {
            old.Free(a, self.m)
        }
        return none
    }
    if stype == StreamPush {
        self.m.Release(s)
        if self.is_server {
            return some(self.RecordFatal(io, H3StreamCreationError))
        }
        return some(self.RecordFatal(io, H3IdError))
    }
    if stype == StreamQpackEncoder {
        if ps.peer_qenc.IsSome() {
            self.m.Release(s)
            return some(self.RecordFatal(io, H3StreamCreationError))
        }
        cap := if self.qpack_cap > 0 { self.qpack_cap * 2 + 4096 } else { 4096 }
        var qe := UniBufStream.New(a, s, cap) else e {
            self.m.Release(s)
            return some(e)
        }
        self.DrainQpackEnc(io, mut qe) else e {
            qe.Free(a, self.m)
            return some(e)
        }
        if old := core.Replace(mut ps.peer_qenc, some(qe)) {
            old.Free(a, self.m)
        }
        return none
    }
    if stype == StreamQpackDecoder {
        if ps.peer_qdec.IsSome() {
            self.m.Release(s)
            return some(self.RecordFatal(io, H3StreamCreationError))
        }
        var qd := UniBufStream.New(a, s, 256) else e {
            self.m.Release(s)
            return some(e)
        }
        self.DrainQpackDec(io, mut qd) else e {
            qd.Free(a, self.m)
            return some(e)
        }
        if old := core.Replace(mut ps.peer_qdec, some(qd)) {
            old.Free(a, self.m)
        }
        return none
    }
    // H3-85: unknown or GREASE stream type -> STOP_SENDING(H3_STREAM_CREATION_ERROR) and discard.
    self.m.Stop(&s, H3StreamCreationError)
    self.m.Release(s)
    return none
}

/// Reads every peer unidirectional stream (and accepts incoming bidirectional
/// streams: queued for Accept on server, H3StreamCreationError on client)
/// without waiting. O(bytes available); allocates only when uni-stream
/// buffers are created or grow.
export Session.Poll(&self, io: Io): Error!void {
    try self.PollStreams(io, true)
}

// Only Accept (or a dedicated Session.Serve) should remove bidirectional
// streams from the transport. Request tasks can otherwise move a stream
// into accept_q as Accept starts waiting for transport readiness.
Session.PollStreams(&self, io: Io, accept_bidi: bool): Error!void {
    try self.CheckFatal(io)
    var pg := try self.poll_mu.Lock(io)
    defer pg.Unlock()
    try self.CheckFatal(io)
    ps := mut *pg.Mut()
    a := self.heap.Alloc()

    // 1. Accept any newly opened peer bidirectional streams.
    for accept_bidi {
        bs := (self.m.Accept(io, false, false) else e {
            fail NormMuxErr(e)
        }) else {
            break
        }
        if !self.is_server {
            self.m.Release(bs)
            fail self.RecordFatal(io, H3StreamCreationError)
        }
        var g := self.state_mu.Lock(io.NoCancel()).Must()
        st := mut *g.Mut()
        st.accept_q.Push(a, bs) else {
            g.Unlock()
            self.m.Release(bs)
            fail OutOfMemory
        }
        g.Unlock()
        self.accept_ev.NotifyOne()
    }

    // 2. Accept any newly opened peer unidirectional streams.
    for {
        us := (self.m.Accept(io, true, false) else e {
            fail NormMuxErr(e)
        }) else {
            break
        }
        var pu := PendingUni(stream: us, buf: [0; 8], len: 0)
        res := self.StepPendingUni(io, mut pu) else e {
            PendingUni(stream: strm, ..) := pu
            self.m.Release(strm)
            fail e
        }
        if res.done {
            PendingUni(stream: strm, ..) := pu
            if res.stype == u64(0xFFFF) {
                self.m.Release(strm)
            } else {
                if err := self.DispatchUniStream(io, ps, strm, res.stype) {
                    fail err
                }
            }
        } else {
            ps.pending_uni.Push(a, pu) else {
                PendingUni(stream: strm, ..) := pu
                self.m.Release(strm)
                fail OutOfMemory
            }
        }
    }

    // 3. Advance any previously pending uni stream headers.
    var idx := 0
    for idx < ps.pending_uni.Len() {
        res := try self.StepPendingUni(io, mut ps.pending_uni.ItemsMut()[idx])
        if res.done {
            pu := ps.pending_uni.Remove(idx)
            PendingUni(stream: strm, ..) := pu
            if res.stype == u64(0xFFFF) {
                self.m.Release(strm)
            } else {
                if err := self.DispatchUniStream(io, ps, strm, res.stype) {
                    fail err
                }
            }
        } else {
            idx += 1
        }
    }

    // 4. Drain open critical streams.
    if pc := mut ps.peer_ctrl {
        try self.DrainControl(io, pc)
    }
    if qe := mut ps.peer_qenc {
        try self.DrainQpackEnc(io, qe)
    }
    if qd := mut ps.peer_qdec {
        try self.DrainQpackDec(io, qd)
    }
}

// CollectUniIds fills ids_buf with up to 16 active peer uni stream ids.
Session.CollectUniIds(&self, io: Io, ids_buf: mut [16]i64): int {
    var pg := self.poll_mu.Lock(io.NoCancel()).Must()
    ps := &*pg.Ref()
    var n := 0
    if pc := &ps.peer_ctrl {
        ids_buf[n] = pc.stream.Id()
        n += 1
    }
    if qe := &ps.peer_qenc {
        ids_buf[n] = qe.stream.Id()
        n += 1
    }
    if qd := &ps.peer_qdec {
        ids_buf[n] = qd.stream.Id()
        n += 1
    }
    for i in 0..ps.pending_uni.Len() {
        if n >= 16 {
            break
        }
        ids_buf[n] = ps.pending_uni[i].stream.Id()
        n += 1
    }
    pg.Unlock()
    return n
}

// WaitAnyUni waits for any peer uni stream (or incoming stream if incoming is
// true) to make progress, then returns.
Session.WaitStep(&self, io: Io, incoming: bool): Error!void {
    var ids_buf: [16]i64 = [0; 16]
    n := self.CollectUniIds(io, mut ids_buf)
    _ = self.m.Wait(io, ids_buf[0..n], incoming) else e {
        fail NormMuxErr(e)
    }
}

// Updating the generation before waking preserves inserts that land
// after Decode registers but before a blocked stream starts waiting.
Session.WakeQpack(&self, io: Io) {
    _ = self.qpack_epoch.Add(1)
    core.SyncWake(io, &self.qpack_epoch, true)
}

Session.StopServing(&self, io: Io) {
    var g := self.state_mu.Lock(io.NoCancel()).Must()
    g.Mut().reader_done = true
    g.Unlock()
    self.accept_ev.NotifyOne()
    self.WakeQpack(io)
}

// SetServing updates st.serving under state_mu.
Session.SetServing(&self, val: bool) {
    var g := self.state_mu.Lock(core.IoMasked(rt.NativeIo())).Must()
    (*g.Mut()).serving = val
    g.Unlock()
}

/// Continuously reads peer unidirectional streams (and incoming request
/// streams on server) until the connection closes, fails, or io is cancelled
/// (RFC 9114 6.2). O(bytes received); allocates only for stream buffers.
export Session.Serve(&self, io: Io): Error!void {
    try self.ServeLoop(io, none)
}

/// Runs the dedicated server reader and signals ready before its first
/// read. Wakes Accept and blocked QPACK decoders when the reader exits.
/// Errors: Canceled, a protocol violation or net.Error. O(bytes received);
/// allocates only when stream buffers grow.
export Session.ServeReady(&self, io: Io, ready: &sync.Notify): Error!void {
    defer self.StopServing(io)
    try self.ServeLoop(io, some(ready))
}

Session.ServeLoop(&self, io: Io, ready: ?&sync.Notify): Error!void {
    self.SetServing(true)
    defer self.SetServing(false)
    if r := ready {
        r.NotifyOne()
    }
    for {
        try self.Poll(io)
        try self.WaitStep(io, true)
    }
}
