// Why: a QUIC connection coordinates keys, recovery, streams, flow control,
// CIDs, paths, closing state, and concurrent application tasks through a single
// lock-ordered Engine and the read-token protocol (lead.Token).
//
// What: Engine, SendState, PartyWant, QConn (NewClient, NewServer, Handshake,
// EnsureHandshake, Open, Accept, Read, Write, Credit, Wait, Reset, Stop,
// Release, Limits, Usable, PeerAddr, Close, Abandon, Inbox), and timer/wake
// helpers.
//
// How: lock order is sl (Mutex[SendState]), then eng (Mutex[Engine]), then
// endpoint locks; eng is held for CPU work only. Up to 128 inline sync.Notify
// buckets back arbitrary numbers of waiting parties via modulo bucketing.

import std.crypto
import std.net
import std.sync
import std.sync.lead
import std.time
import std.tls

// Handshake lifecycle stages inside Engine.
const HsInit: u8 = 0
const HsRunning: u8 = 1
const HsComplete: u8 = 2
const HsConfirmed: u8 = 3

// Number of inline sync.Notify slots on QConn.
const NotifyBuckets: int = 128

// Maximum per-stream buffered unsent bytes before Write applies backpressure.
const MaxStreamSendBuf: u64 = 65_536

// Maximum datagrams assembled in one FlushSend batch (Section 3.6.1).
const MaxBatchDatagrams: int = 10

// Maximum QUIC UDP payload size we send (RFC 9000 14.2).
const MaxSendDatagram: int = 1200

// Condition a waiting party checks under Engine lock in Driven.Begin.
enum PartyWant {
    Free
    Handshake(level: Space)
    Open(uni: bool)
    Accept(uni: bool)
    Read(id: i64)
    Write(id: i64)
    Wait(incoming: bool)
    Drive
}

// Per-party state in Engine.
linear struct PartyInfo {
    want: PartyWant
    wait_ids: HeapList[i64]
    ready: ?net.MuxReady
}

PartyInfo.New(a: &Alloc): PartyInfo {
    return PartyInfo(want: PartyWant.Free, wait_ids: HeapList[i64].New(a), ready: none)
}

PartyInfo.Free(self, a: &Alloc) {
    PartyInfo(want: _, wait_ids, ready: _) := self
    wait_ids.Free(a)
}

// Connection-level sent control frame records (negative SentKind.Control indices).
enum ConnCtrlKind {
    HandshakeDone
    NewCid(seq: u64)
    RetireCid(seq: u64)
}

struct ConnCtrlRec {
    live: bool
    kind: ConnCtrlKind
}

// Per-connection send batch buffer guarded by QConn.sl.
// Linear: owns HeapList batch buffer allocated lazily on first send.
linear struct ConnSendState {
    batch_buf: HeapList[u8]
}

ConnSendState.New(a: &Alloc): ConnSendState {
    return ConnSendState(batch_buf: HeapList[u8].New(a))
}

ConnSendState.Free(self, h: &Heap) {
    ConnSendState(batch_buf) := self
    batch_buf.Free(h.Alloc())
}

// All mutable QUIC connection state guarded by QConn.eng.
linear struct Engine {
    h: &Heap
    cfg: EngineCfg
    is_server: bool
    keys: KeyRing
    cids: CidState
    path: PathState
    close: CloseState
    orig_dcid: Cid
    local_scid: Cid
    retry_scid: ?Cid
    peer_initial_scid: ?Cid
    peer_scid_locked: bool
    retry_received: bool
    retry_token: [128]u8
    retry_token_len: int
    any_pkt_processed: bool

    hs_state: u8
    hs_done_pending: bool
    hs_done_acked: bool
    hs_fault: ?Error
    hs_net_err: ?net.Error
    hs_alert_pending: bool
    hs_alert_code: u8
    peer_params: ?Params
    peer_params_authed: bool
    alpn_buf: [255]u8
    alpn_len: int

    addr_validated: bool
    amp_rx_bytes: i64
    amp_tx_bytes: i64

    acks: AckSet
    rec: Recovery
    ack_frames: HeapList[SentFrame]
    lost_frames: HeapList[SentFrame]
    streams: Streams
    crypto: CryptoStreams
    post_hs_buf: HeapList[u8]
    ctrl_recs: HeapList[ConnCtrlRec]
    ctrl_free: HeapList[int]

    next_skip_app_pn: i64
    skip_rng: u64
    pn_exhausted: bool

    idle_effective_ns: i64
    idle_deadline: ?time.Instant
    sent_ae_since_rx: bool

    next_timer: ?time.Instant
    drive_stop: bool
    abandoned: bool
    close_reply_owed: bool
    close_first_pending: bool
    probe_owed_space: ?Space
    probe_owed_count: int

    tok: lead.Token
    parties: HeapList[PartyInfo]
    free_parties: HeapList[int]
    bucket_counts: [NotifyBuckets]int

    pending_retire_ep: HeapList[Cid]
    ep_conn_idx: int
    ep_conn_gen: u32
    ep_half_open_cleared: bool
    ep_hs_cids_cleaned: bool
    need_spare_cids: bool
}

// Creates an Engine for client or server.
Engine.New(
    h: &Heap,
    io: Io,
    is_server: bool,
    cfg: &EngineCfg,
    orig_dcid: &Cid,
    first_dcid: &Cid,
    local_scid: &Cid,
    peer_init_scid: ?Cid,
    retry_scid: ?Cid,
    peer_addr: net.Addr,
    addr_validated: bool,
    first_rx_bytes: i64,
    alpn: str,
    static_key: ?&[32]u8,
    ep_conn_idx: int,
    ep_conn_gen: u32,
): Error!Engine from h {
    a := h.Alloc()
    var keys := KeyRing.New(first_dcid, !is_server)
    onerr keys.Wipe()
    first_peer := match peer_init_scid {
        some(c) { c }
        none { *first_dcid }
    }
    var cids := try CidState.New(a, !is_server, &first_peer)
    onerr cids.Free(a)
    if sk := static_key {
        cids.SetStaticKey(sk)
    }
    try cids.AddLocalInitial(a, local_scid)

    var rec := try Recovery.New(a)
    onerr rec.Free(a)
    rec.SetRole(is_server)
    if is_server && !addr_validated {
        // Server starts with first_rx_bytes already credited.
        rec.SetServerAmpBlocked(false, time.Now(io.NoCancel()))
    }

    slim := cfg.StreamLimits(!is_server)
    var streams := try Streams.New(a, !is_server, &slim)
    onerr streams.Free(a)

    var cs := CryptoStreams.New(a)
    onerr cs.Free(a)

    var tok := try lead.Token.New(a, 16)
    onerr tok.Free(a)

    var rng_b: [8]u8 = [0; 8]
    crypto.Random(io.NoCancel(), mut rng_b[..]) else {}
    var rng := (Cast[u64](ReadBe32(rng_b[..], 0)) << 32) | Cast[u64](ReadBe32(rng_b[..], 4))
    if rng == 0 {
        rng = 0x9e3779b97f4a7c15
    }
    skip_pn := Cast[i64](256 + (rng % 768))

    var alpn_buf: [255]u8 = [0; 255]
    ab := alpn.AsBytes()
    alen := if ab.Len() < 255 { ab.Len() } else { 255 }
    (mut alpn_buf[0..alen]).CopyFrom(ab[0..alen])

    var path := PathState.New(peer_addr)
    var close := CloseState.New()
    if addr_validated {
        close.SetValidatedAddr(peer_addr)
    }

    idle_ms := cfg.idle.Nanos() / 1_000_000
    idle_ns := if idle_ms <= 0 { i64(0) } else { idle_ms * 1_000_000 }

    return Engine(
        h: h,
        cfg: *cfg,
        is_server: is_server,
        keys: keys,
        cids: cids,
        path: path,
        close: close,
        orig_dcid: *orig_dcid,
        local_scid: *local_scid,
        retry_scid: retry_scid,
        peer_initial_scid: peer_init_scid,
        peer_scid_locked: is_server,
        retry_received: retry_scid.IsSome(),
        retry_token: [0; 128],
        retry_token_len: 0,
        any_pkt_processed: false,
        hs_state: HsInit,
        hs_done_pending: false,
        hs_done_acked: false,
        hs_fault: none,
        hs_net_err: none,
        hs_alert_pending: false,
        hs_alert_code: 0,
        peer_params: none,
        peer_params_authed: false,
        alpn_buf: alpn_buf,
        alpn_len: alen,
        addr_validated: addr_validated,
        amp_rx_bytes: first_rx_bytes,
        amp_tx_bytes: 0,
        acks: AckSet.New(),
        rec: rec,
        ack_frames: HeapList[SentFrame].New(a),
        lost_frames: HeapList[SentFrame].New(a),
        streams: streams,
        crypto: cs,
        post_hs_buf: HeapList[u8].New(a),
        ctrl_recs: HeapList[ConnCtrlRec].New(a),
        ctrl_free: HeapList[int].New(a),
        next_skip_app_pn: skip_pn,
        skip_rng: rng,
        pn_exhausted: false,
        idle_effective_ns: idle_ns,
        idle_deadline: none,
        sent_ae_since_rx: false,
        next_timer: none,
        drive_stop: false,
        abandoned: false,
        close_reply_owed: false,
        close_first_pending: false,
        probe_owed_space: none,
        probe_owed_count: 0,
        tok: tok,
        parties: HeapList[PartyInfo].New(a),
        free_parties: HeapList[int].New(a),
        bucket_counts: [0; NotifyBuckets],
        pending_retire_ep: HeapList[Cid].New(a),
        ep_conn_idx: ep_conn_idx,
        ep_conn_gen: ep_conn_gen,
        ep_half_open_cleared: false,
        ep_hs_cids_cleaned: false,
        need_spare_cids: false,
    )
}

// Frees all resources owned by Engine back to its Heap.
Engine.Free(self) {
    Engine(
        h,
        cfg: _,
        is_server: _,
        keys,
        cids,
        path: _,
        close: _,
        orig_dcid: _,
        local_scid: _,
        retry_scid: _,
        peer_initial_scid: _,
        peer_scid_locked: _,
        retry_received: _,
        retry_token: _,
        retry_token_len: _,
        any_pkt_processed: _,
        hs_state: _,
        hs_done_pending: _,
        hs_done_acked: _,
        hs_fault: _,
        hs_net_err: _,
        hs_alert_pending: _,
        hs_alert_code: _,
        peer_params: _,
        peer_params_authed: _,
        alpn_buf: _,
        alpn_len: _,
        addr_validated: _,
        amp_rx_bytes: _,
        amp_tx_bytes: _,
        acks: _,
        rec,
        ack_frames,
        lost_frames,
        streams,
        crypto,
        post_hs_buf,
        ctrl_recs,
        ctrl_free,
        next_skip_app_pn: _,
        skip_rng: _,
        pn_exhausted: _,
        idle_effective_ns: _,
        idle_deadline: _,
        sent_ae_since_rx: _,
        next_timer: _,
        drive_stop: _,
        abandoned: _,
        close_reply_owed: _,
        close_first_pending: _,
        probe_owed_space: _,
        probe_owed_count: _,
        tok,
        parties,
        free_parties,
        bucket_counts: _,
        pending_retire_ep,
        ep_conn_idx: _,
        ep_conn_gen: _,
        ep_half_open_cleared: _,
        ep_hs_cids_cleaned: _,
        need_spare_cids: _,
    ) := self
    a := h.Alloc()
    keys.Wipe()
    cids.Free(a)
    rec.Free(a)
    ack_frames.Free(a)
    lost_frames.Free(a)
    streams.Free(a)
    crypto.Free(a)
    post_hs_buf.Free(a)
    ctrl_recs.Free(a)
    ctrl_free.Free(a)
    tok.Free(a)
    var ps := parties
    for {
        p := ps.Pop() else {
            break
        }
        p.Free(a)
    }
    ps.FreeEmpty(a)
    free_parties.Free(a)
    pending_retire_ep.Free(a)
}

// Advances the PRNG state and returns the next pseudo-random u64. O(1).
Engine.NextRng(mut self): u64 {
    var x := self.skip_rng
    x = x ^ (x << 13)
    x = x ^ (x >> 7)
    x = x ^ (x << 17)
    self.skip_rng = x
    return x
}

// Allocates a negative control-frame tracking index for conn-level frames.
Engine.AllocCtrl(mut self, kind: ConnCtrlKind): int {
    a := self.h.Alloc()
    var slot := -1
    if self.ctrl_free.Len() > 0 {
        slot = self.ctrl_free.Pop().Must()
        items := self.ctrl_recs.ItemsMut()
        items[slot] = ConnCtrlRec(live: true, kind: kind)
    } else {
        slot = self.ctrl_recs.Len()
        self.ctrl_recs.Push(a, ConnCtrlRec(live: true, kind: kind)) else {
            return 0
        }
    }
    return -1 - slot
}

// Takes and frees a negative control-frame tracking index.
Engine.TakeCtrl(mut self, index: int): ?ConnCtrlKind {
    if index >= 0 {
        return none
    }
    slot := -1 - index
    if slot < 0 || slot >= self.ctrl_recs.Len() || !self.ctrl_recs[slot].live {
        return none
    }
    k := self.ctrl_recs[slot].kind
    items := self.ctrl_recs.ItemsMut()
    items[slot].live = false
    self.ctrl_free.Push(self.h.Alloc(), slot) else {}
    return some(k)
}

// Allocates a party index for lead.Wait. O(1) amortized.
Engine.AllocParty(mut self, want: PartyWant): OutOfMemory!int {
    a := self.h.Alloc()
    if self.free_parties.Len() > 0 {
        p := self.free_parties.Pop().Must()
        items := self.parties.ItemsMut()
        items[p].want = want
        items[p].ready = none
        items[p].wait_ids.Clear()
        self.bucket_counts[p % NotifyBuckets] += 1
        return p
    }
    p := self.parties.Len()
    try self.tok.Grow(a, p + 1)
    var pi := PartyInfo(want: want, wait_ids: HeapList[i64].New(a), ready: none)
    self.parties.Push(a, pi) else err {
        PartyInfo(want: _, wait_ids: wl, ready: _) := pi
        wl.Free(a)
        fail err
    }
    self.bucket_counts[p % NotifyBuckets] += 1
    return p
}

// Releases party index p back to the free list. O(1).
Engine.FreeParty(mut self, p: int) {
    if p < 0 || p >= self.parties.Len() {
        return
    }
    items := self.parties.ItemsMut()
    items[p].want = PartyWant.Free
    items[p].ready = none
    items[p].wait_ids.Clear()
    if self.bucket_counts[p % NotifyBuckets] > 0 {
        self.bucket_counts[p % NotifyBuckets] -= 1
    }
    self.free_parties.Push(self.h.Alloc(), p) else {}
}

// Reports whether the connection has failed, closed, or been abandoned. O(1).
Engine.IsDead(&self): bool {
    return self.abandoned || self.pn_exhausted || self.close.IsClosed() || self.hs_fault.IsSome() || self.hs_net_err.IsSome()
}

// Returns the net.Error representing why the connection ended. O(1).
Engine.ClosedError(&self): net.Error {
    if e := self.hs_net_err {
        return e
    }
    if e := self.hs_fault {
        return MapQuicError(&e, false)
    }
    if self.close.phase == ClosePhase.Draining {
        return net.MuxClosed(code: self.close.code, app: self.close.app, peer: true)
    }
    if self.close.phase == ClosePhase.Closing {
        return net.MuxClosed(code: self.close.code, app: self.close.app, peer: false)
    }
    return Closed
}

// Records a fatal transport/crypto error and enters Closing.
Engine.FailTransport(mut self, code: u64, frame: u64, now: time.Instant) {
    if self.hs_fault.IsNone() {
        self.hs_fault = some(TransportError(code: code, frame: frame))
    }
    if !self.close.IsClosed() && !self.pn_exhausted {
        self.close.Close(code, false, frame, now, self.rec.Pto())
        self.close_first_pending = true
    }
    self.RecomputeTimer(now)
}

// Restarts the idle timer upon processing an incoming packet.
Engine.RestartIdleOnRx(mut self, now: time.Instant) {
    self.sent_ae_since_rx = false
    if self.idle_effective_ns <= 0 {
        self.idle_deadline = none
        return
    }
    three_pto := 3 * self.rec.Pto().Nanos()
    dur := if three_pto > self.idle_effective_ns { three_pto } else { self.idle_effective_ns }
    self.idle_deadline = some(InstantAdd(now, time.Nanos(dur)))
}

// Restarts the idle timer on the first ack-eliciting packet sent since last Rx.
Engine.RestartIdleOnTx(mut self, now: time.Instant) {
    if self.sent_ae_since_rx {
        return
    }
    self.sent_ae_since_rx = true
    if self.idle_effective_ns <= 0 {
        self.idle_deadline = none
        return
    }
    three_pto := 3 * self.rec.Pto().Nanos()
    dur := if three_pto > self.idle_effective_ns { three_pto } else { self.idle_effective_ns }
    self.idle_deadline = some(InstantAdd(now, time.Nanos(dur)))
}

// Computes the earliest active timer instant across recovery, ACK delay, pacer,
// key-update prev expiry, path validation, idle timeout, and closing/draining (3.6.2).
Engine.RecomputeTimer(mut self, now: time.Instant) {
    var best: ?time.Instant = none
    if d := self.rec.Timer(now) {
        best = EarliestInstant(best, some(InstantAdd(now, d)))
    }
    if d := self.acks.Due(now) {
        can_tx := !self.IsDead() && !self.pn_exhausted && (!self.is_server || self.addr_validated || self.RemainingAmpBudget(&self.path.Destination()) >= 64)
        if can_tx {
            best = EarliestInstant(best, some(InstantAdd(now, d)))
        }
    }
    if d := self.rec.NextPaceDelay(MaxSendDatagram, now) {
        best = EarliestInstant(best, some(InstantAdd(now, d)))
    } else if !self.IsDead() && !self.pn_exhausted && self.streams.HasPending() && self.rec.CanSend(MaxSendDatagram, now) {
        if !self.is_server || self.addr_validated || self.RemainingAmpBudget(&self.path.Destination()) >= 64 {
            best = EarliestInstant(best, some(now))
        }
    }
    best = EarliestInstant(best, self.keys.Due())
    best = EarliestInstant(best, self.path.Due())
    best = EarliestInstant(best, self.idle_deadline)
    best = EarliestInstant(best, self.close.Due())
    self.next_timer = best
}

// Returns the earlier of two optional instants. O(1).
EarliestInstant(a: ?time.Instant, b: ?time.Instant): ?time.Instant {
    return match a {
        none { b }
        some(ta) {
            match b {
                none { a }
                some(tb) { if tb.Since(ta).Nanos() < 0 { b } else { a } }
            }
        }
    }
}

// Checks whether party p's condition is currently satisfied (under eng lock).
Engine.PartyReady(mut self, p: int): bool {
    if self.IsDead() || self.drive_stop {
        return true
    }
    want := self.parties[p].want
    return match want {
        .Free { true }
        .Handshake(level) { self.crypto.UnreadBytes(level) > 0 }
        .Open(uni) { self.streams.CanOpen(uni) }
        .Accept(uni) { self.streams.HasAccept(uni) }
        .Read(id) { self.streams.IsReadable(id) }
        .Write(id) { self.streams.CanWriteNow(id, MaxStreamSendBuf) }
        .Wait(incoming) {
            for i in 0..self.parties[p].wait_ids.Len() {
                wid := self.parties[p].wait_ids[i]
                if self.streams.IsReadable(wid) {
                    items := self.parties.ItemsMut()
                    items[p].ready = some(net.MuxReady.Readable(index: i))
                    return true
                }
            }
            if incoming {
                if self.streams.HasAccept(false) {
                    items := self.parties.ItemsMut()
                    items[p].ready = some(net.MuxReady.Incoming(uni: false))
                    return true
                }
                if self.streams.HasAccept(true) {
                    items := self.parties.ItemsMut()
                    items[p].ready = some(net.MuxReady.Incoming(uni: true))
                    return true
                }
            }
            false
        }
        .Drive { false }
    }
}

// Builds an array of 16 sync.Notify slots without requiring Copy/Plain.
Notify16(): [16]sync.Notify {
    return [
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
    ]
}

// Builds [NotifyBuckets]sync.Notify (128 slots) inline without heap allocation.
Notify128(): [NotifyBuckets]sync.Notify {
    return [
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
    ]
}

// One QUIC connection (client or server): linear, Send and Share.
linear view struct QConn {
    h: &Heap
    nc: Io
    sock: ?net.SharedDatagram
    ep: ?EpBox[EndpointShared]
    ep_conn_idx: int
    ep_conn_gen: u32
    sl: Mutex[ConnSendState]
    eng: Mutex[Engine]
    hs_mu: Mutex[u8]
    parties: [NotifyBuckets]sync.Notify
}

// Wakes a single party p using NotifyOne (or NotifyAll if its bucket is shared).
QConn.WakeParty(&self, p: int, bucket_count: int) {
    if p < 0 {
        return
    }
    b := p % NotifyBuckets
    if bucket_count > 1 {
        self.parties[b].NotifyAll()
    } else {
        self.parties[b].NotifyOne()
    }
}

// Wakes every waiting party whose condition is now satisfied (or all on fault).
// Note: collects bucket wake info under lock or wakes directly since Notify is
// independent of eng lock (Notify takes only its own spin lock).
QConn.WakeSatisfied(&self, eng: mut Engine) {
    for p in 0..eng.parties.Len() {
        match eng.parties[p].want {
            .Free {}
            .Drive {
                if eng.IsDead() || eng.drive_stop {
                    self.WakeParty(p, eng.bucket_counts[p % NotifyBuckets])
                }
            }
            _ {
                if eng.PartyReady(p) {
                    self.WakeParty(p, eng.bucket_counts[p % NotifyBuckets])
                }
            }
        }
    }
}

// Creates a new client QConn over connected net.SharedDatagram sock.
// Consumes sock only on success.
QConn.NewClient(h: &Heap, io: Io, sock: net.SharedDatagram, peer: net.Addr, c: &Config): net.Error!QConn from h {
    nc := io.NoCancel()
    orig_dcid := try RandomLocalCid(io)
    local_scid := try RandomLocalCid(io)
    ecfg := c.ToEngineCfg()
    var no_retry: ?Cid = none
    var no_scid: ?Cid = none
    var no_sk: ?&[32]u8 = none
    var eng := Engine.New(
        h,
        nc,
        false,
        &ecfg,
        &orig_dcid,
        &orig_dcid,
        &local_scid,
        no_scid,
        no_retry,
        peer,
        true,
        0,
        "",
        no_sk,
        0,
        0,
    ) else e {
        fail MapQuicError(&e, false)
    }
    send := ConnSendState.New(h.Alloc())
    return QConn(
        h: h,
        nc: nc,
        sock: some(sock),
        ep: none,
        ep_conn_idx: 0,
        ep_conn_gen: 0,
        sl: Mutex[ConnSendState].New(send),
        eng: Mutex[Engine].New(eng),
        hs_mu: Mutex[u8].New(0),
        parties: Notify128(),
    )
}

// Creates a new server QConn for an accepted Initial datagram.
// Consumes ep only on success.
QConn.NewServer(
    h: &Heap,
    io: Io,
    ep: EpBox[EndpointShared],
    conn_idx: int,
    conn_gen: u32,
    client_scid: &Cid,
    first_dcid: &Cid,
    orig_dcid: &Cid,
    retry_scid: ?Cid,
    local_scid: &Cid,
    peer: net.Addr,
    addr_validated: bool,
    first_rx_bytes: i64,
    c: &EngineCfg,
    alpn: str,
): net.Error!QConn from h, ep {
    nc := io.NoCancel()
    var init_scid: ?Cid = some(*client_scid)
    var eng := Engine.New(
        h,
        nc,
        true,
        c,
        orig_dcid,
        first_dcid,
        local_scid,
        init_scid,
        retry_scid,
        peer,
        addr_validated,
        first_rx_bytes,
        alpn,
        some(&ep.Ref().static_key),
        conn_idx,
        conn_gen,
    ) else e {
        fail MapQuicError(&e, false)
    }
    send := ConnSendState.New(h.Alloc())
    return QConn(
        h: h,
        nc: nc,
        sock: none,
        ep: some(ep),
        ep_conn_idx: conn_idx,
        ep_conn_gen: conn_gen,
        sl: Mutex[ConnSendState].New(send),
        eng: Mutex[Engine].New(eng),
        hs_mu: Mutex[u8].New(0),
        parties: Notify128(),
    )
}

// Releases all resources of QConn without waiting on the peer.
QConn.MuxAbandon(self) {
    QConn(h, nc: _, sock, ep, ep_conn_idx, ep_conn_gen, sl, eng, hs_mu, parties: _) := self
    var e := eng.Into()
    if !e.abandoned {
        e.abandoned = true
        if ep_b := &ep {
            if !e.ep_half_open_cleared {
                ep_b.Ref().DecHalfOpen()
                e.ep_half_open_cleared = true
            }
            ep_b.Ref().UnregisterConnCids(&e.cids, &e.orig_dcid, e.retry_scid)
        }
    }
    e.Free()
    sl.Into().Free(h)
    _ = hs_mu.Into()
    if s := sock {
        s.Close()
    }
    if ep_b := ep {
        ep_b.Ref().ReleaseConn(h, ep_conn_idx, ep_conn_gen)
        if es := ep_b.Release(h) {
            es.Free(h)
        }
    }
}

QConn.BeginHandshake(&self, nc: Io, params_buf: mut []u8): net.Error!(party: int, is_srv: bool, plen: int) {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    e.hs_state = HsRunning
    party := e.AllocParty(PartyWant.Handshake(level: Space.Initial)) else {
        g.Unlock()
        fail OutOfMemory
    }
    is_srv := e.is_server
    var p := e.cfg.ToParams(!is_srv)
    p.initial_scid = some(e.local_scid)
    if is_srv {
        p.original_dcid = some(e.orig_dcid)
        p.retry_scid = e.retry_scid
        p.stateless_reset_token = some(e.cids.local[0].token)
    }
    g.Unlock()
    plen := EncodeParams(params_buf, &p)
    return (party: party, is_srv: is_srv, plen: plen)
}

// Runs the TLS 1.3 handshake inline (client or server).
QConn.Handshake(&self, h: &Heap, io: Io, c: &tls.Config, host: str, alpn: []str): net.Error!tls.QuicDone {
    nc := io.NoCancel()
    var params_buf: [512]u8 = [0; 512]
    prep := try self.BeginHandshake(nc, mut params_buf[..])
    party := prep.party
    is_srv := prep.is_srv
    plen := prep.plen
    var wire := HandshakeWire(conn: self, party: party, is_server: is_srv)
    done := if is_srv {
        tls.QuicServer(h, io, mut wire, c, alpn, params_buf[0..plen]) else err_tls {
            self.FinishHandshakeFailure(nc, party, &err_tls)
            fail self.HandshakeNetError(&err_tls, nc)
        }
    } else {
        tls.QuicClient(h, io, mut wire, c, host, alpn, params_buf[0..plen]) else err_tls {
            self.FinishHandshakeFailure(nc, party, &err_tls)
            fail self.HandshakeNetError(&err_tls, nc)
        }
    }
    try self.FinishHandshakeSuccess(nc, party)
    self.FlushSend(nc) else {}
    return done
}

QConn.FinishHandshakeFailure(&self, nc: Io, party: int, err_tls: &tls.Error) {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.FreeParty(party)
    if e.hs_net_err.IsNone() {
        e.hs_net_err = some(MapTlsError(err_tls))
    }
    if e.is_server {
        if ep := &self.ep {
            if !e.ep_half_open_cleared {
                ep.Ref().DecHalfOpen()
                e.ep_half_open_cleared = true
            }
        }
    }
    self.WakeSatisfied(e)
    g.Unlock()
    self.FlushSend(nc) else {}
}

QConn.HandshakeNetError(&self, err_tls: &tls.Error, nc: Io): net.Error {
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    if ne := e.hs_net_err {
        g.Unlock()
        return ne
    }
    g.Unlock()
    return MapTlsError(err_tls)
}

QConn.FinishHandshakeSuccess(&self, nc: Io, party: int): net.Error!void {
    now := time.Now(nc)
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.FreeParty(party)
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    if pp := e.peer_params {
        e.cids.OnHandshakeAuthed(e.h.Alloc(), &pp) else err {
            e.FailTransport(ErrProtocolViolation, 0, now)
            ne := MapQuicError(&err, false)
            e.hs_net_err = some(ne)
            g.Unlock()
            fail ne
        }
        e.peer_params_authed = true
    }
    if e.is_server {
        e.hs_state = HsConfirmed
        e.hs_done_pending = true
        e.keys.SetHandshakeConfirmed()
        e.keys.DiscardHandshake()
        e.rec.Discard(Space.Handshake)
        e.crypto.Discard(Space.Handshake)
        e.acks.ClearSpace(Space.Handshake)
        e.rec.SetHandshakeConfirmed(true, now)
        if ep_q := &self.ep {
            if !e.ep_half_open_cleared {
                ep_q.Ref().DecHalfOpen()
                e.ep_half_open_cleared = true
            }
            if !e.ep_hs_cids_cleaned {
                ep_q.Ref().RetireHandshakeCids(&e.orig_dcid, e.retry_scid)
                e.ep_hs_cids_cleaned = true
            }
            // Issue spare local CIDs up to peer's limit.
            ep_q.Ref().IssueSpareCids(nc, e)
        }
    } else {
        e.hs_state = HsComplete
    }
    e.RestartIdleOnRx(now)
    e.RecomputeTimer(now)
    self.WakeSatisfied(e)
    g.Unlock()
}

QConn.CheckServerHsFast(&self, nc: Io): net.Error!bool {
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    if !e.is_server || e.hs_state == HsComplete || e.hs_state == HsConfirmed {
        g.Unlock()
        return true
    }
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    g.Unlock()
    return false
}

QConn.PrepareServerHs(&self, nc: Io): net.Error!?(alpn_buf: [255]u8, alpn_len: int) {
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    if e.hs_state == HsComplete || e.hs_state == HsConfirmed {
        g.Unlock()
        return none
    }
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    alpn_buf := e.alpn_buf
    alpn_len := e.alpn_len
    g.Unlock()
    return some((alpn_buf: alpn_buf, alpn_len: alpn_len))
}

// Runs TLS server handshake using tls_cfg from EndpointShared.
QConn.RunServerHandshake(&self, h: &Heap, io: Io, alpn: []str): net.Error!tls.QuicDone {
    nc := io.NoCancel()
    var params_buf: [512]u8 = [0; 512]
    prep := try self.BeginHandshake(nc, mut params_buf[..])
    party := prep.party
    plen := prep.plen
    var wire := HandshakeWire(conn: self, party: party, is_server: true)
    ep := match &self.ep {
        some(b) { b.Ref() }
        none { fail Closed }
    }
    done := ep.tls_cfg.RunQuicServer(h, io, mut wire, alpn, params_buf[0..plen]) else err_tls {
        self.FinishHandshakeFailure(nc, party, &err_tls)
        fail self.HandshakeNetError(&err_tls, nc)
    }
    try self.FinishHandshakeSuccess(nc, party)
    self.FlushSend(nc) else {}
    return done
}

// Ensures the server handshake has completed before accepting/opening streams (3.6.3).
QConn.EnsureHandshake(&self, io: Io): net.Error!void {
    nc := io.NoCancel()
    if try self.CheckServerHsFast(nc) {
        return
    }
    hg := try self.hs_mu.Lock(io)
    defer hg.Unlock()
    prep := try self.PrepareServerHs(nc) else { return }
    alpn_str := str.From(prep.alpn_buf[0..prep.alpn_len]).Must()
    alpns: [1]str = [alpn_str]
    _ = try self.RunServerHandshake(self.h, io, alpns[..])
}

QConn.TryReadCrypto(&self, nc: Io, party: int, level: Space, buf: mut []u8): net.Error!int {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    n := e.crypto.Read(level, buf)
    if n > 0 {
        g.Unlock()
        return n
    }
    items := e.parties.ItemsMut()
    items[party].want = PartyWant.Handshake(level: level)
    g.Unlock()
    return 0
}

QConn.QueueCryptoSend(&self, nc: Io, level: Space, data: []u8): net.Error!void {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    e.crypto.Queue(e.h.Alloc(), level, data) else {
        g.Unlock()
        fail OutOfMemory
    }
    g.Unlock()
}

// HandshakeWire callbacks invoked by tls.QuicClient / tls.QuicServer.
QConn.HsRecv(&self, io: Io, party: int, level: Space, buf: mut []u8): net.Error!int {
    nc := io.NoCancel()
    for {
        n := try self.TryReadCrypto(nc, party, level, buf)
        if n > 0 {
            return n
        }
        try lead.Wait(self, io, party)
    }
}

QConn.HsSend(&self, io: Io, level: Space, data: []u8): net.Error!void {
    nc := io.NoCancel()
    try self.QueueCryptoSend(nc, level, data)
    try self.FlushSend(io)
}

QConn.HsSecret(&self, level: Space, write: bool, suite: Suite, secret: []u8): net.Error!void {
    nc := self.nc
    now := time.Now(nc)
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.IsDead() {
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    if !write {
        // RFC 9001 4.1.3: reject unread CRYPTO bytes at the previous level.
        prev := match level {
            .Handshake { Space.Initial }
            .App { Space.Handshake }
            .Initial { Space.Initial }
        }
        if level != Space.Initial && e.crypto.UnreadBytes(prev) > 0 {
            e.FailTransport(ErrProtocolViolation, 0, now)
            e.hs_net_err = some(net.SecureFailed(why: net.SecureFailure.Protocol))
            g.Unlock()
            fail net.SecureFailed(why: net.SecureFailure.Protocol)
        }
    }
    e.keys.InstallSecret(e.h.Alloc(), level, write, suite, secret) else {
        e.FailTransport(ErrInternal, 0, now)
        g.Unlock()
        fail net.SecureFailed(why: net.SecureFailure.Handshake)
    }
    if level == Space.Handshake {
        e.rec.SetHandshakeKeys(true)
    }
    g.Unlock()
}

QConn.HsPeerParams(&self, raw: []u8): net.Error!void {
    nc := self.nc
    now := time.Now(nc)
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    p := ValidatePeerParams(e.is_server, raw, &e.orig_dcid, e.retry_scid, e.peer_initial_scid) else err {
        e.FailTransport(ErrTransportParameter, 0, now)
        ne := MapQuicError(&err, false)
        e.hs_net_err = some(ne)
        g.Unlock()
        fail ne
    }
    e.peer_params = some(p)
    // Apply flow-control and stream limits before handshake completes.
    var lim := e.cfg.StreamLimits(!e.is_server)
    lim.peer_max_data = p.initial_max_data
    lim.peer_max_stream_data_bidi_local = p.initial_max_stream_data_bidi_local
    lim.peer_max_stream_data_bidi_remote = p.initial_max_stream_data_bidi_remote
    lim.peer_max_stream_data_uni = p.initial_max_stream_data_uni
    lim.peer_max_streams_bidi = p.initial_max_streams_bidi
    lim.peer_max_streams_uni = p.initial_max_streams_uni
    e.streams.SetPeerLimits(&lim)
    e.acks.SetAckDelayParams(time.Millis(Cast[i64](DefaultMaxAckDelay)), Cast[int](DefaultAckDelayExponent))
    e.rec.SetPeerMaxAckDelay(time.Millis(Cast[i64](p.max_ack_delay)))

    // Compute effective idle timeout (RFC 9000 10.1).
    peer_idle_ns := Cast[i64](p.max_idle_timeout) * 1_000_000
    if e.idle_effective_ns > 0 && peer_idle_ns > 0 {
        if peer_idle_ns < e.idle_effective_ns {
            e.idle_effective_ns = peer_idle_ns
        }
    } else if peer_idle_ns > 0 {
        e.idle_effective_ns = peer_idle_ns
    }
    e.RestartIdleOnRx(now)
    e.RecomputeTimer(now)
    g.Unlock()
}

QConn.RecordHsAlert(&self, nc: Io, code: u8, now: time.Instant) {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    qcode := ErrCryptoBase + Cast[u64](code)
    e.FailTransport(qcode, 0, now)
    e.hs_net_err = some(net.SecureFailed(why: net.SecureFailure.Handshake))
    self.WakeSatisfied(e)
    g.Unlock()
}

QConn.HsAlert(&self, io: Io, code: u8) {
    nc := io.NoCancel()
    now := time.Now(nc)
    self.RecordHsAlert(nc, code, now)
    self.FlushSend(nc) else {}
}
