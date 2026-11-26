// Why: HTTP/2 multiplexes concurrent streams over one net.Conn under strict
// lock ordering, non-cancellable bounded writes, coalesced owed control
// frames, and immediate fault propagation without leaking buffers or stranding
// waiting tasks.
//
// What: Limits, Conn, Conn.Client, Conn.Server, Conn.Close, Conn.Abandon,
// Conn.Reset, Conn.Usable, Conn.StreamId, Conn.GoawayLast, Unprocessed, and
// connection state/fault helpers.
//
// How: Conn boxes its read state, write state, write-token semaphore, and
// mutex-protected stream/flow/owed state on h, enforcing lock order rd -> wl
// -> wr -> st (H1), never holding st or wr across I/O, and draining owed
// GOAWAY frames before shutting down the socket on protocol errors (H4).

import std.core
import std.http.fields
import std.net
import std.rt
import std.sync
import std.sync.lead
import std.task
import std.time

/// The limits one HTTP/2 connection runs under.
export struct Limits {
    /// Maximum concurrent streams accepted (server) or opened (client).
    export streams: int
    /// Initial receive window per stream (client: h2_window; server: ServerInitialStreamWindow).
    export window: int
    /// Maximum compressed and decoded field-block size in octets.
    export header_bytes: int
    /// Server request-body admission budget in octets.
    export body_bytes: int
    /// Maximum wait for a single socket WriteAll or request-header / progress window.
    export write_limit: time.Duration
    /// Maximum wait for closing GOAWAY and transport shutdown.
    export close_limit: time.Duration
    /// Idle connection lifetime on the server (keep_alive); zero disables idle close.
    export idle_limit: time.Duration
}

// DiscardSink drops decoded fields while keeping the HPACK dynamic table in sync.
struct DiscardSink {
    unused: u8
}

impl fields.Sink for DiscardSink {
    Add(mut self, a: &Alloc, name: []u8, value: []u8, never_index: bool, name_index: int): OutOfMemory!void {
    }
}

linear view struct State {
    streams: StreamTable
    reset_set: ResetSet
    reserved_remote: HeapList[u32]
    owed: Owed
    admission: Admission
    peer_settings: Settings
    our_settings_acked: bool
    acked_table_bytes: int
    pending_table_bytes: int
    our_initial_window: int
    conn_send_win: Window
    conn_recv_win: Window
    conn_recv_credit: Credit
    conn_send_gate: UpdateGate
    reset_bucket: Bucket
    control_bucket: Bucket
    highest_peer_id: u32
    next_stream_id: u32
    last_processed_id: u32
    retained_head_bytes: int
    goaway_recvd: bool
    goaway_last: u32
    goaway_code: u32
    fault: ?Error
    token: lead.Token
    writer_done: bool
    reader_done: bool
    drive_idle_since: ?time.Instant
    drive_idle_limit: time.Duration
    drive_stop: bool
}

State.Free(self, a: &Alloc) {
    State(streams, reserved_remote, owed, admission, token, ..) := self
    streams.Free(a)
    reserved_remote.Free(a)
    owed.Free(a)
    admission.Free(a)
    token.Free(a)
}

linear view struct ConnCore {
    rd: Mutex[ReadState]
    wr: Mutex[WriteState]
    wl: sync.Semaphore
    wout: Mutex[HeapList[u8]]
    st: Mutex[State]
    wr_wake: sync.Notify
    client_wr_wake: sync.Notify
    parties: HeapList[sync.Notify]
}

ConnCore.Free(self, a: &Alloc) {
    ConnCore(rd, wr, wl: _, wout, st, wr_wake: _, client_wr_wake: _, parties) := self
    rd.Into().Free(a)
    wr.Into().Free(a)
    wout.Into().Free(a)
    st.Into().Free(a)
    parties.Free(a)
}

/// One HTTP/2 connection over a net.Conn, either role. Linear, Send and Share;
/// boxed on h.
export linear mut view struct Conn {
    heap: &Heap
    conn: ?net.Conn
    lim: Limits
    is_client: bool
    core: Box[ConnCore]
}

Conn.Core(&self): &ConnCore {
    return self.core.Ref()
}

Conn.NetConn(&self): &net.Conn {
    if c := &self.conn {
        return c
    }
    Panic("h2.Conn: missing transport")
}

TableCap(streams: int): int {
    return streams + (if streams < 32 { 32 } else { streams })
}

/// A client connection: sends the preface and SETTINGS. O(streams) setup plus
/// one socket write. Errors: net.Error, time.TimedOut, OutOfMemory.
export Conn.Client(h: &Heap, io: Io, conn: net.Conn into h, l: Limits): Error!Box[Conn] from h, conn {
    a := h.Alloc()
    lim := SanitizeLimits(l)
    now := time.Now(io)
    var rd := try ReadState.New(a, lim.header_bytes, true)
    onerr rd.Free(a)
    var wr := try WriteState.New(a)
    onerr wr.Free(a)
    party_count := 1 + 2 * TableCap(lim.streams)
    var st := try NewState(a, lim, true, party_count, now)
    onerr st.Free(a)
    var parties := HeapList[sync.Notify].New(a)
    onerr parties.Free(a)
    for _ in 0..party_count {
        try parties.Push(a, sync.Notify.New())
    }
    cc := ConnCore(
        rd: Mutex[ReadState].New(rd),
        wr: Mutex[WriteState].New(wr),
        wl: sync.Semaphore.New(1),
        wout: Mutex[HeapList[u8]].New(HeapList[u8].New(a)),
        st: Mutex[State].New(st),
        wr_wake: sync.Notify.New(),
        client_wr_wake: sync.Notify.New(),
        parties: parties,
    )
    var cbox := Box[ConnCore].New(a, cc) else e {
        cc.Free(a)
        fail e
    }
    c := Conn(heap: h, conn: none, lim: lim, is_client: true, core: cbox)
    var box := Box[Conn].New(a, c) else e {
        c.Abandon()
        fail e
    }
    var preface: [128]u8 = [0; 128]
    plen := BuildPreface(mut preface[..], lim, true)
    var err: ?Error = none
    var expired := false
    scope s := io.Scope() {
        s.CancelAfter(lim.write_limit)
        conn.WriteAll(io, preface[0..plen]) else e {
            err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
        }
    }
    if e := err {
        box.Take(a).Abandon()
        if expired {
            fail time.TimedOut
        }
        fail e
    }
    match core.Replace(mut box.Mut().conn, some(conn)) {
        some(old) { old.Abandon() }
        none {}
    }
    return box
}

/// A server connection after the preface was read (or sniffed by h2c, with the
/// bytes already read in pre). Sends the server's initial SETTINGS and
/// connection WINDOW_UPDATE immediately. O(1) plus one socket write.
export Conn.Server(h: &Heap, io: Io, conn: net.Conn into h, l: Limits, pre: []u8): Error!Box[Conn] from h, conn {
    a := h.Alloc()
    lim := SanitizeLimits(l)
    now := time.Now(io)
    var rd := try ReadState.New(a, lim.header_bytes, false)
    onerr rd.Free(a)
    if pre.Len() > 0 {
        try rd.Seed(a, pre)
    }
    var wr := try WriteState.New(a)
    onerr wr.Free(a)
    var st := try NewState(a, lim, false, 0, now)
    onerr st.Free(a)
    var parties := HeapList[sync.Notify].New(a)
    onerr parties.Free(a)
    for _ in 0..TableCap(lim.streams) {
        try parties.Push(a, sync.Notify.New())
    }
    cc := ConnCore(
        rd: Mutex[ReadState].New(rd),
        wr: Mutex[WriteState].New(wr),
        wl: sync.Semaphore.New(1),
        wout: Mutex[HeapList[u8]].New(HeapList[u8].New(a)),
        st: Mutex[State].New(st),
        wr_wake: sync.Notify.New(),
        client_wr_wake: sync.Notify.New(),
        parties: parties,
    )
    var cbox := Box[ConnCore].New(a, cc) else e {
        cc.Free(a)
        fail e
    }
    c := Conn(heap: h, conn: none, lim: lim, is_client: false, core: cbox)
    var box := Box[Conn].New(a, c) else e {
        c.Abandon()
        fail e
    }
    var preface: [128]u8 = [0; 128]
    plen := BuildPreface(mut preface[..], lim, false)
    var err: ?Error = none
    var expired := false
    scope s := io.Scope() {
        s.CancelAfter(lim.write_limit)
        conn.WriteAll(io, preface[0..plen]) else e {
            err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
        }
    }
    if e := err {
        box.Take(a).Abandon()
        if expired {
            fail time.TimedOut
        }
        fail e
    }
    match core.Replace(mut box.Mut().conn, some(conn)) {
        some(old) { old.Abandon() }
        none {}
    }
    return box
}

SanitizeLimits(l: Limits): Limits {
    var out := l
    if out.streams < 1 {
        out.streams = 1
    }
    if out.window < 0 {
        out.window = 0
    }
    if out.window > MaxWindow {
        out.window = MaxWindow
    }
    if out.header_bytes < 1024 {
        out.header_bytes = 1024
    }
    if out.body_bytes < 0 {
        out.body_bytes = 0
    }
    if out.write_limit.Nanos() <= 0 {
        out.write_limit = core.DurationOf(30_000_000_000)
    }
    if out.close_limit.Nanos() <= 0 {
        out.close_limit = core.DurationOf(5_000_000_000)
    }
    return out
}

NewState(a: &Alloc, lim: Limits, is_client: bool, party_count: int, now: time.Instant): OutOfMemory!State {
    var streams := try StreamTable.New(a, TableCap(lim.streams))
    onerr streams.Free(a)
    var token := try lead.Token.New(a, party_count)
    onerr token.Free(a)
    return State(
        streams: streams,
        reset_set: ResetSet.New(),
        reserved_remote: HeapList[u32].New(a),
        owed: Owed.New(a),
        admission: Admission.New(a, lim.body_bytes),
        peer_settings: InitialSettings,
        our_settings_acked: false,
        acked_table_bytes: fields.DefaultTableBytes,
        pending_table_bytes: fields.DefaultTableBytes,
        our_initial_window: lim.window,
        conn_send_win: Window.New(InitialWindow),
        conn_recv_win: Window.New(ConnWindow),
        conn_recv_credit: Credit.New(ConnWindow),
        conn_send_gate: UpdateGate.New(),
        reset_bucket: Bucket.New(ResetBudget, ResetRefill, now),
        control_bucket: Bucket.New(ControlBudget, ControlRefill, now),
        highest_peer_id: 0,
        next_stream_id: 1,
        last_processed_id: 0,
        retained_head_bytes: 0,
        goaway_recvd: false,
        goaway_last: MaxStreamId,
        goaway_code: NoError,
        fault: none,
        token: token,
        writer_done: false,
        reader_done: false,
        drive_idle_since: some(now),
        drive_idle_limit: core.DurationOf(0),
        drive_stop: false,
    )
}

BuildPreface(out: mut []u8, lim: Limits, is_client: bool): int {
    var off := 0
    if is_client {
        (mut out[0..24]).CopyFrom(Preface[..])
        off = 24
    }
    var s := InitialSettings
    s.enable_push = false
    if !is_client {
        s.max_streams = some(lim.streams)
    }
    s.initial_window = lim.window
    s.max_header_list = some(lim.header_bytes)
    off += WriteSettings(mut out[off..], &s).Must()
    wu := ConnWindow - InitialWindow
    if wu > 0 {
        off += WriteWindowUpdate(mut out[off..], 0, Trunc[u32](wu)).Must()
    }
    return off
}

/// Reports whether a new client stream can be opened right now. Checks
/// retirement, the stream ID space, and both stream limits. O(1); allocates nothing.
export Conn.Usable(&self): bool {
    nc := core.IoMasked(rt.NativeIo())
    g := self.Core().st.Lock(nc).Must()
    st := g.Ref()
    cap := EffectiveMaxStreams(&st.peer_settings, self.lim.streams)
    ok := !st.ClientDraining() && st.streams.open < cap && st.streams.active < st.streams.cap_limit
    g.Unlock()
    return ok
}

/// Returns the stream identifier for s, or 0 if stale. O(1); allocates nothing.
export Conn.StreamId(&self, s: StreamRef): u32 {
    nc := core.IoMasked(rt.NativeIo())
    g := self.Core().st.Lock(nc).Must()
    st := g.Ref()
    var id: u32 = 0
    if s.index >= 0 && s.index < st.streams.slots.Len() {
        slot := &st.streams.slots.Items()[s.index]
        if slot.gen_ == s.gen_ && slot.state != StIdle {
            id = slot.id
        }
    }
    g.Unlock()
    return id
}

/// Returns the last-stream-id from any GOAWAY received from the peer, or none.
/// O(1); allocates nothing.
export Conn.GoawayLast(&self): ?u32 {
    nc := core.IoMasked(rt.NativeIo())
    g := self.Core().st.Lock(nc).Must()
    last: ?u32 = if g.Ref().goaway_recvd { some(g.Ref().goaway_last) } else { none }
    g.Unlock()
    return last
}

/// Returns true when stream id was not processed by a peer that sent GOAWAY
/// with last (RFC 9113 6.8: id > last). O(1); allocates nothing.
export Unprocessed(id: u32, last: u32): bool {
    return id > last
}

/// Queues a stream reset with code if s is still live (used by handlers/callers
/// on failure). Never waits. O(1); allocates nothing.
export Conn.Reset(&self, s: StreamRef, code: u32) {
    nc := core.IoMasked(rt.NativeIo())
    var wake := false
    var g := self.Core().st.Lock(nc).Must()
    st := mut *g.Mut()
    if s.index >= 0 && s.index < st.streams.slots.Len() {
        var admitted_ids: [16]u32 = [0; 16]
        admitted_n := ResetStreamSlot(st, s.index, s.gen_, code, mut admitted_ids[..])
        if admitted_n >= 0 {
            if admitted_n > 0 {
                AdmitStreams(st, admitted_ids[0..admitted_n])
            }
            EnqueueOwedStream(st, s.index)
            wake = true
        }
    }
    g.Unlock()
    if wake {
        self.WakeControlWriter()
        self.WakeStream(s.index)
    }
}

ResetStreamSlot(st: mut State, idx: int, gen_: u32, code: u32, admitted_ids: mut []u32): int {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.gen_ != gen_ || slot.state == StIdle || slot.state == StClosed {
        return -1
    }
    sid := slot.id
    slot.owed_reset = some(code)
    slot.reset = some(code)
    slot.progress.Idle()
    reserve := slot.admit_reserve
    was_admitted := slot.admitted
    slot.admit_reserve = 0
    slot.admitted = false
    st.reset_set.Add(sid)
    st.streams.CloseSlot(idx)
    if reserve > 0 {
        if was_admitted {
            return st.admission.Return(reserve, admitted_ids)
        }
        st.admission.Cancel(sid)
    }
    return 0
}

Conn.SendCloseGoaway(&self, io: Io): Error!void {
    nc := io.NoCancel()
    var cg := self.Core().st.Lock(nc).Must()
    cst := mut *cg.Mut()
    cst.drive_stop = true
    if cst.fault.IsNone() {
        cst.owed.QueueGoaway(cst.last_processed_id, NoError)
    }
    cg.Unlock()
    if self.is_client {
        self.Party(PartyDrive).NotifyOne()
    }
    var expired := false
    var err: ?Error = none
    scope s := io.Scope() {
        s.CancelAfter(self.lim.close_limit)
        self.Core().wl.Acquire(io, 1) else e {
            err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
        }
        if err.IsNone() {
            _ = self.WriteTurnLocked(io, true) else e {
                err = some(e)
                canceled := match e {
                    Canceled { true }
                    _ { false }
                }
                expired = task.LimitHit(&s, canceled)
            }
            self.Core().wl.Release(1)
        }
    }
    if e := err {
        if expired {
            fail time.TimedOut
        }
        fail e
    }
}

Conn.FinishClose(self, io: Io) {
    Conn(heap, conn, lim, is_client: _, core) := self
    a := heap.Alloc()
    core.Take(a).Free(a)
    if c := conn {
        scope s := io.Scope() {
            s.CancelAfter(lim.close_limit)
            c.Close(io) else {
                c.Abandon()
            }
        }
    }
}

/// Ends the connection without consuming it, for a pool that cannot close
/// it while others hold it: sends GOAWAY(NO_ERROR) within the close limit if
/// it can, then shuts the transport down both ways, which ends every waiter
/// (a Drive task included) with an error. The connection is freed later by
/// Close or Abandon. Never fails: a GOAWAY that cannot be sent is skipped.
/// O(streams) for the frames owed; allocates nothing.
export Conn.Evict(&self, io: Io) {
    self.SendCloseGoaway(io) else {}
    self.FailConn(io.NoCancel(), Closed, none, true)
}

/// Sends GOAWAY(NO_ERROR) best-effort within close_limit, closes the underlying
/// transport, and frees all connection buffers. On error, self stays the
/// caller's to Abandon. O(streams); allocates nothing.
export Conn.Close(self, io: Io): Error!void {
    try self.SendCloseGoaway(io)
    self.FinishClose(io)
}

/// Releases the connection and all buffers immediately without speaking to the
/// peer. Never blocks or fails. O(streams).
export Conn.Abandon(self) {
    Conn(heap, conn, lim: _, is_client: _, core) := self
    a := heap.Alloc()
    core.Take(a).Free(a)
    match conn {
        some(c) { c.Abandon() }
        none {}
    }
}

EffectiveMaxStreams(ps: &Settings, our_limit: int): int {
    lim := if our_limit < 1 { 1 } else { our_limit }
    if m := ps.max_streams {
        return if m < lim { m } else { lim }
    }
    // RFC 9113 6.5.2 / 5.1.2: assume 100 until server SETTINGS arrive.
    return if 100 < lim { 100 } else { lim }
}

EnqueueOwedStream(st: mut State, idx: int) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.in_owed {
        return
    }
    slot.in_owed = true
    slot.owed_next = -1
    if st.owed.stream_tail >= 0 {
        prev := st.owed.stream_tail
        slot.owed_prev = prev
        st.streams.slots.ItemsMut()[prev].owed_next = idx
        st.owed.stream_tail = idx
    } else {
        slot.owed_prev = -1
        st.owed.stream_head = idx
        st.owed.stream_tail = idx
    }
}

UnlinkOwedStream(st: mut State, idx: int) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if !slot.in_owed {
        return
    }
    pv := slot.owed_prev
    nx := slot.owed_next
    slot.in_owed = false
    slot.owed_prev = -1
    slot.owed_next = -1
    if pv >= 0 {
        st.streams.slots.ItemsMut()[pv].owed_next = nx
    } else {
        st.owed.stream_head = nx
    }
    if nx >= 0 {
        st.streams.slots.ItemsMut()[nx].owed_prev = pv
    } else {
        st.owed.stream_tail = pv
    }
}

AdmitStreams(st: mut State, ids: []u32) {
    for sid in ids {
        idx := st.streams.Find(sid)
        if idx < 0 {
            continue
        }
        if AdmitOneStream(st, idx) {
            EnqueueOwedStream(st, idx)
        }
    }
}

AdmitOneStream(st: mut State, idx: int): bool {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.state == StIdle || slot.state == StClosed {
        return false
    }
    slot.admitted = true
    var owed := false
    if slot.admit_reserve > slot.admitted_win {
        delta := slot.admit_reserve - slot.admitted_win
        slot.recv_win.Grow(delta) else {}
        slot.owed_credit += delta
        slot.admitted_win = slot.admit_reserve
        owed = true
    }
    if slot.expect_100 {
        slot.owed_100 = true
        owed = true
    }
    return owed
}

// FailConn records the first connection fault in st, queues GOAWAY when a
// protocol violation code is given, shuts down the transport on I/O faults,
// and wakes all waiting tasks.
Conn.FailConn(&self, nc: Io, err: Error, goaway_code: ?u32, shutdown_now: bool) {
    var wake_writer := false
    var g := self.Core().st.Lock(nc).Must()
    st := mut *g.Mut()
    if st.fault.IsNone() {
        st.fault = some(err)
        if code := goaway_code {
            st.owed.QueueGoaway(st.last_processed_id, code)
            wake_writer = true
        }
    }
    g.Unlock()
    if shutdown_now {
        self.NetConn().Shutdown(nc, net.Shutdown.Both) else {}
    }
    if wake_writer {
        self.Core().wr_wake.NotifyOne()
    }
    self.WakeAll()
}

// Reset and Release can owe a frame even if the client is blocked in Read.
Conn.WakeControlWriter(&self) {
    core_ := self.Core()
    if self.is_client {
        core_.client_wr_wake.NotifyOne()
    } else {
        core_.wr_wake.NotifyOne()
    }
}

// Each party has at most one waiter. NotifyOne also covers the interval
// between observing the blocked condition and starting its wait.
Conn.WakeAll(&self) {
    for party in &self.Core().parties.Items() {
        party.NotifyOne()
    }
}

Conn.WriterNotify(&self, idx: int): &sync.Notify {
    return self.Party(if self.is_client { WriterParty(idx) } else { idx })
}

Conn.WakeStream(&self, idx: int) {
    if self.is_client {
        self.Party(ReaderParty(idx)).NotifyOne()
    }
    self.WriterNotify(idx).NotifyOne()
}

Conn.WakeBlockedWriters(&self, st: &State, reason: u8) {
    for i in 0..st.streams.slots.Len() {
        if st.streams.slots.Items()[i].blocked_on == reason {
            self.WriterNotify(i).NotifyOne()
        }
    }
}
