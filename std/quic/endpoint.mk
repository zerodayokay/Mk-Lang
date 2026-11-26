// Why: a QUIC server endpoint receives all incoming UDP datagrams on one
// socket, routes packets by connection ID without per-datagram allocation,
// enforces half-open/Retry/stateless-reset/Version-Negotiation rules, and
// owns closing/draining state after QConn is released.
//
// What: DemuxTable, ConnSlot, EndpointShared, Endpoint (Listen, Run, Accept,
// Port, Abandon), and EndpointShared helpers called by QConn.
//
// How: DemuxTable is a seed-randomized open-addressing hash table; EndpointShared
// holds the SharedDatagram, 1,024-bucket Notify array, DgramPool, ConnSlot table,
// and 64-entry accept queue with handshake-limit expiry; EpBox[EndpointShared]
// safely shares the hub between Endpoint and accepted QConns.

import std.core
import std.crypto
import std.net
import std.sync
import std.time

// Maximum unaccepted connections queued before dropping new Initials (3.7.2).
const AcceptQueueCap: int = 64

// Number of inline inbox Notify buckets on EndpointShared.
const EpNotifyBuckets: int = 1024

// Slot state in open-addressing DemuxTable.
const DemuxEmpty: u8 = 0
const DemuxUsed: u8 = 1
const DemuxTomb: u8 = 2

struct DemuxEntry {
    state: u8
    cid: Cid
    conn_idx: int
    conn_gen: u32
}

// Seed-randomized open-addressing hash table mapping CIDs to (conn_idx, conn_gen).
// Linear: owns HeapList[DemuxEntry].
linear struct DemuxTable {
    seed: u64
    entries: HeapList[DemuxEntry]
    used: int
    tombs: int
}

DemuxTable.New(a: &Alloc, seed: u64): OutOfMemory!DemuxTable {
    var entries := HeapList[DemuxEntry].New(a)
    onerr entries.Free(a)
    empty := DemuxEntry(state: DemuxEmpty, cid: Cid(b: [0; MaxCidLen], n: 0), conn_idx: -1, conn_gen: 0)
    for _ in 0..128 {
        try entries.Push(a, empty)
    }
    return DemuxTable(seed: seed, entries: entries, used: 0, tombs: 0)
}

DemuxTable.Free(self, a: &Alloc) {
    DemuxTable(seed: _, entries, used: _, tombs: _) := self
    entries.Free(a)
}

DemuxHash(seed: u64, cid: &Cid): u64 {
    var h := seed ^ 0x9e3779b97f4a7c15
    h = h ^ Cast[u64](cid.n)
    for i in 0..cid.n {
        h = (h ^ Cast[u64](cid.b[i])) *% 0xbf58476d1ce4e5b9
        h = h ^ (h >> 27)
    }
    h = (h ^ (h >> 30)) *% 0x94d049bb133111eb
    return h ^ (h >> 31)
}

DemuxTable.Rehash(mut self, a: &Alloc, new_cap: int): OutOfMemory!void {
    var old := HeapList[DemuxEntry].New(a)
    onerr old.Free(a)
    cur := self.entries.Items()
    for i in 0..cur.Len() {
        try old.Push(a, cur[i])
    }
    self.entries.Clear()
    empty := DemuxEntry(state: DemuxEmpty, cid: Cid(b: [0; MaxCidLen], n: 0), conn_idx: -1, conn_gen: 0)
    for _ in 0..new_cap {
        self.entries.Push(a, empty) else err {
            old.Free(a)
            fail err
        }
    }
    mask := new_cap - 1
    nitems := self.entries.ItemsMut()
    old_items := old.Items()
    var count := 0
    for i in 0..old_items.Len() {
        if old_items[i].state == DemuxUsed {
            var idx := Cast[int](DemuxHash(self.seed, &old_items[i].cid) & Cast[u64](mask))
            for nitems[idx].state == DemuxUsed {
                idx = (idx + 1) & mask
            }
            nitems[idx] = old_items[i]
            count += 1
        }
    }
    old.Free(a)
    self.used = count
    self.tombs = 0
}

// Looks up cid in the table, returning (conn_idx, conn_gen) or none. O(1) expected.
DemuxTable.Lookup(&self, cid: &Cid): ?(idx: int, gen_: u32) {
    cap := self.entries.Len()
    if cap == 0 {
        return none
    }
    mask := cap - 1
    var pos := Cast[int](DemuxHash(self.seed, cid) & Cast[u64](mask))
    items := self.entries.Items()
    for _ in 0..cap {
        st := items[pos].state
        if st == DemuxEmpty {
            return none
        }
        if st == DemuxUsed && CidEqual(&items[pos].cid, cid) {
            return some((idx: items[pos].conn_idx, gen_: items[pos].conn_gen))
        }
        pos = (pos + 1) & mask
    }
    return none
}

// Inserts or updates cid -> (conn_idx, conn_gen). O(1) amortized.
DemuxTable.Insert(mut self, a: &Alloc, cid: &Cid, conn_idx: int, conn_gen: u32): OutOfMemory!void {
    cap := self.entries.Len()
    if (self.used + self.tombs + 1) * 10 >= cap * 7 {
        target := if self.used * 2 >= cap { cap * 2 } else { cap }
        try self.Rehash(a, target)
    }
    ncap := self.entries.Len()
    mask := ncap - 1
    var pos := Cast[int](DemuxHash(self.seed, cid) & Cast[u64](mask))
    var first_tomb := -1
    items := self.entries.ItemsMut()
    for _ in 0..ncap {
        st := items[pos].state
        if st == DemuxEmpty {
            dst := if first_tomb >= 0 { first_tomb } else { pos }
            if items[dst].state == DemuxTomb {
                self.tombs -= 1
            }
            items[dst] = DemuxEntry(state: DemuxUsed, cid: *cid, conn_idx: conn_idx, conn_gen: conn_gen)
            self.used += 1
            return
        }
        if st == DemuxUsed && CidEqual(&items[pos].cid, cid) {
            items[pos].conn_idx = conn_idx
            items[pos].conn_gen = conn_gen
            return
        }
        if st == DemuxTomb && first_tomb < 0 {
            first_tomb = pos
        }
        pos = (pos + 1) & mask
    }
}

// Removes cid from the table if present. O(1) expected.
DemuxTable.Remove(mut self, cid: &Cid) {
    cap := self.entries.Len()
    if cap == 0 {
        return
    }
    mask := cap - 1
    var pos := Cast[int](DemuxHash(self.seed, cid) & Cast[u64](mask))
    items := self.entries.ItemsMut()
    for _ in 0..cap {
        st := items[pos].state
        if st == DemuxEmpty {
            return
        }
        if st == DemuxUsed && CidEqual(&items[pos].cid, cid) {
            items[pos].state = DemuxTomb
            self.used -= 1
            self.tombs += 1
            return
        }
        pos = (pos + 1) & mask
    }
}

// Per-connection inbox ring and post-abandon closing/draining state in Endpoint.
struct ConnSlot {
    gen_: u32
    alive: bool
    closing: bool
    ring: [ConnInboxQuota]u16
    head: int
    tail: int
    len: int
    close_state: CloseState
    close_pkt: [128]u8
    close_len: int
    cids: [16]Cid
    cid_len: int
}

EmptyConnSlot(gen_: u32): ConnSlot {
    return ConnSlot(
        gen_: gen_,
        alive: false,
        closing: false,
        ring: [0; ConnInboxQuota],
        head: 0,
        tail: 0,
        len: 0,
        close_state: CloseState.New(),
        close_pkt: [0; 128],
        close_len: 0,
        cids: [Cid(b: [0; MaxCidLen], n: 0); 16],
        cid_len: 0,
    )
}

// Records a CID on the slot so post-abandon closing cleanup can unregister all of them.
ConnSlot.TrackCid(mut self, cid: &Cid) {
    for i in 0..self.cid_len {
        if CidEqual(&self.cids[i], cid) {
            return
        }
    }
    if self.cid_len < 16 {
        self.cids[self.cid_len] = *cid
        self.cid_len += 1
    }
}

ConnSlot.UntrackCid(mut self, cid: &Cid) {
    for i in 0..self.cid_len {
        if CidEqual(&self.cids[i], cid) {
            self.cids[i] = self.cids[self.cid_len - 1]
            self.cid_len -= 1
            return
        }
    }
}

// Queued unaccepted connection on Endpoint.accept_q.
linear struct AcceptEntry {
    conn: QueuedConn
    created_at: time.Instant
}

// Mutable Endpoint state guarded by EndpointShared.st.
linear struct EndpointState {
    closed: bool
    pool: DgramPool
    demux: DemuxTable
    slots: HeapList[ConnSlot]
    free_slots: HeapList[int]
    accept_q: HeapList[AcceptEntry]
    half_open: int
    reset_limiter: ResetLimiter
    retry_key: RetryTokenKey
    cfg: EngineCfg
    alpn_buf: [255]u8
    alpn_len: int
    hs_limit: time.Duration
}

EndpointState.Free(self, h: &Heap) {
    EndpointState(
        closed: _,
        pool,
        demux,
        slots,
        free_slots,
        accept_q: aq,
        half_open: _,
        reset_limiter: _,
        retry_key: rk,
        cfg: _,
        alpn_buf: _,
        alpn_len: _,
        hs_limit: _,
    ) := self
    var accept_q := aq
    var retry_key := rk
    a := h.Alloc()
    retry_key.Wipe()
    for {
        entry := accept_q.Pop() else { break }
        AcceptEntry(conn, created_at: _) := entry
        conn.Unpack(h).MuxAbandon()
    }
    accept_q.FreeEmpty(a)
    slots.Free(a)
    free_slots.Free(a)
    demux.Free(a)
    pool.Free(a)
}

// Builds a [64][16]sync.Notify array (1,024 buckets) inline without heap allocation.
Notify1024(): [64][16]sync.Notify {
    return [
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
        Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(), Notify16(),
    ]
}

// Reference-counted shared server endpoint hub.
linear struct EndpointShared {
    h: &Heap
    nc: Io
    sock: net.SharedDatagram
    static_key: [32]u8
    tls_cfg: OwnedTlsCfg
    notifies: [64][16]sync.Notify
    accept_notify: sync.Notify
    st: Mutex[EndpointState]
}

EndpointShared.Free(self, h: &Heap) {
    EndpointShared(h: _, nc: _, sock, static_key: sk, tls_cfg, notifies: _, accept_notify: _, st) := self
    var static_key := sk
    crypto.Wipe(mut static_key[..])
    tls_cfg.Free(h.Alloc())
    sock.Close()
    st.Into().Free(h)
}

// Returns the inbox Notify for connection slot idx. O(1).
EndpointShared.InboxNotify(&self, idx: int): &sync.Notify {
    b := idx % EpNotifyBuckets
    return &self.notifies[b / 16][b % 16]
}

EndpointShared.WakeSlot(&self, idx: int, total_slots: int) {
    b := idx % EpNotifyBuckets
    if total_slots > EpNotifyBuckets {
        self.notifies[b / 16][b % 16].NotifyAll()
    } else {
        self.notifies[b / 16][b % 16].NotifyOne()
    }
}

// Sends one datagram on the endpoint UDP socket.
EndpointShared.SendDatagram(&self, io: Io, data: []u8, to: net.Addr): net.Error!void {
    try self.sock.Send(io, data, some(to))
}

// Sends a batch of seg-byte datagrams on the endpoint UDP socket.
EndpointShared.SendBatch(&self, io: Io, buf: []u8, seg: int, to: net.Addr): net.Error!void {
    try self.sock.SendBatch(io, buf, seg, some(to))
}

// Pops one pool slot index from connection slot idx's ring, or none. O(1).
EndpointShared.PopInbox(&self, idx: int, gen_: u32): ?u16 {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    if idx < 0 || idx >= s.slots.Len() {
        g.Unlock()
        return none
    }
    items := s.slots.ItemsMut()
    if !items[idx].alive || items[idx].gen_ != gen_ || items[idx].len == 0 {
        g.Unlock()
        return none
    }
    slot_idx := items[idx].ring[items[idx].head]
    items[idx].head = (items[idx].head + 1) % ConnInboxQuota
    items[idx].len -= 1
    g.Unlock()
    return some(slot_idx)
}

// Copies datagram bytes out of pool slot idx and returns the slot to DgramPool. O(len).
EndpointShared.ReadAndFreeSlot(&self, slot_idx: u16, dst: mut []u8): (len: int, from: net.Addr) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    ps := s.pool.SlotRef(slot_idx)
    n := if ps.len < dst.Len() { ps.len } else { dst.Len() }
    (mut dst[0..n]).CopyFrom(ps.data[0..n])
    from := ps.from
    s.pool.FreeSlot(slot_idx)
    g.Unlock()
    return (len: n, from: from)
}

EndpointShared.DecHalfOpen(&self) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    if s.half_open > 0 {
        s.half_open -= 1
    }
    g.Unlock()
}

EndpointShared.RetireHandshakeCids(&self, orig_dcid: &Cid, retry_scid: ?Cid) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    s.demux.Remove(orig_dcid)
    if r := retry_scid {
        s.demux.Remove(&r)
    }
    g.Unlock()
}

EndpointShared.RetireLocalCid(&self, cid: &Cid) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    s.demux.Remove(cid)
    g.Unlock()
}

EndpointShared.UnregisterConnCids(&self, cids: &CidState, orig_dcid: &Cid, retry_scid: ?Cid) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    s.demux.Remove(orig_dcid)
    if r := retry_scid {
        s.demux.Remove(&r)
    }
    for i in 0..cids.local.Len() {
        s.demux.Remove(&cids.local[i].cid)
    }
    g.Unlock()
}

// Issues spare local CIDs for conn up to min(peer_active_limit, 4) and registers them in demux.
EndpointShared.IssueSpareCids(&self, io: Io, e: mut Engine) {
    nc := io.NoCancel()
    a := e.h.Alloc()
    cap_lim := if e.cids.peer_active_limit < OurActiveCidLimit { Cast[int](e.cids.peer_active_limit) } else { Cast[int](OurActiveCidLimit) }
    for e.cids.ActiveLocalCount() < cap_lim && e.cids.local_total_issued < MaxIssuedCids {
        cand := RandomLocalCid(nc) else { break }
        if e.cids.HasLocalCid(&cand) {
            continue
        }
        var g := self.st.Lock(nc).Must()
        s := mut *g.Mut()
        // QT-333: never collide with any live or closing CID on this endpoint.
        if s.demux.Lookup(&cand).IsSome() {
            g.Unlock()
            continue
        }
        if entry := e.cids.IssueLocal(a, &cand) else { none } {
            _ = s.demux.Insert(self.h.Alloc(), &entry.cid, e.ep_conn_idx, e.ep_conn_gen) else {}
            if e.ep_conn_idx >= 0 && e.ep_conn_idx < s.slots.Len() {
                slots := s.slots.ItemsMut()
                slots[e.ep_conn_idx].TrackCid(&entry.cid)
            }
        }
        g.Unlock()
    }
}

// Hands closing packet + 3*PTO state to Endpoint when QConn.MuxClose completes.
EndpointShared.SetClosingState(
    &self,
    idx: int,
    gen_: u32,
    pkt: []u8,
    until: time.Instant,
    val_addr: net.Addr,
    has_val: bool,
) {
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    if idx < 0 || idx >= s.slots.Len() {
        g.Unlock()
        return
    }
    items := s.slots.ItemsMut()
    if items[idx].gen_ != gen_ {
        g.Unlock()
        return
    }
    items[idx].alive = false
    items[idx].closing = true
    items[idx].close_state = CloseState.New()
    items[idx].close_state.phase = ClosePhase.Closing
    items[idx].close_state.until = some(until)
    items[idx].close_state.SetKeyless(true)
    if has_val {
        items[idx].close_state.SetValidatedAddr(val_addr)
    }
    n := if pkt.Len() < 128 { pkt.Len() } else { 128 }
    (mut items[idx].close_pkt[0..n]).CopyFrom(pkt[0..n])
    items[idx].close_len = n
    g.Unlock()
}

// Releases a ConnSlot when QConn.Abandon runs (keeping it if closing until 3*PTO expires).
EndpointShared.ReleaseConn(&self, h: &Heap, idx: int, gen_: u32) {
    _ = h
    nc := self.nc
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    if idx < 0 || idx >= s.slots.Len() {
        g.Unlock()
        return
    }
    items := s.slots.ItemsMut()
    if items[idx].gen_ != gen_ {
        g.Unlock()
        return
    }
    for items[idx].len > 0 {
        slot_idx := items[idx].ring[items[idx].head]
        items[idx].head = (items[idx].head + 1) % ConnInboxQuota
        items[idx].len -= 1
        s.pool.FreeSlot(slot_idx)
    }
    items[idx].alive = false
    if !items[idx].closing {
        for i in 0..items[idx].cid_len {
            s.demux.Remove(&items[idx].cids[i])
        }
        items[idx].cid_len = 0
        items[idx].gen_ += 1
        s.free_slots.Push(self.h.Alloc(), idx) else {}
    }
    g.Unlock()
}

// Sweeps expired closing slots under st lock.
SweepClosingLocked(s: mut EndpointState, a: &Alloc, now: time.Instant) {
    items := s.slots.ItemsMut()
    for i in 0..items.Len() {
        if items[i].closing && items[i].close_state.Expired(now) {
            items[i].closing = false
            items[i].close_len = 0
            for k in 0..items[i].cid_len {
                s.demux.Remove(&items[i].cids[k])
            }
            items[i].cid_len = 0
            items[i].gen_ += 1
            s.free_slots.Push(a, i) else {}
        }
    }
}

// Sweeps expired unaccepted connections from accept_q into expired_out.
SweepExpiredAcceptLocked(s: mut EndpointState, now: time.Instant, expired_out: mut HeapList[QueuedConn], h: &Heap) {
    a := h.Alloc()
    for s.accept_q.Len() > 0 {
        age := now.Since(s.accept_q[0].created_at)
        if age.Nanos() < s.hs_limit.Nanos() {
            break
        }
        entry := s.accept_q.Remove(0)
        if s.half_open > 0 {
            s.half_open -= 1
        }
        AcceptEntry(conn, created_at: _) := entry
        expired_out.Push(a, conn) else {
            // Best-effort abandon if OOM.
            conn.Unpack(h).MuxAbandon()
        }
    }
}

// Allocates a free ConnSlot inside EndpointState.
AllocConnSlotLocked(s: mut EndpointState, a: &Alloc): OutOfMemory!(idx: int, gen_: u32) {
    if s.free_slots.Len() > 0 {
        idx := s.free_slots.Pop().Must()
        items := s.slots.ItemsMut()
        items[idx].alive = true
        items[idx].closing = false
        items[idx].head = 0
        items[idx].tail = 0
        items[idx].len = 0
        items[idx].cid_len = 0
        return (idx: idx, gen_: items[idx].gen_)
    }
    idx := s.slots.Len()
    var slot := EmptyConnSlot(1)
    slot.alive = true
    try s.slots.Push(a, slot)
    return (idx: idx, gen_: 1)
}

// Copies a datagram payload and source address into PoolSlot ps.
WritePoolSlot(ps: mut PoolSlot, dgram: []u8, from: net.Addr) {
    n := if dgram.Len() < PoolSlotBytes { dgram.Len() } else { PoolSlotBytes }
    (mut ps.data[0..n]).CopyFrom(dgram[0..n])
    ps.len = n
    ps.from = from
}

// Pushes dgram into ConnSlot idx's inbox ring; returns false if pool or quota full.
PushToSlotLocked(s: mut EndpointState, a: &Alloc into s, idx: int, dgram: []u8, from: net.Addr): bool {
    items := s.slots.ItemsMut()
    if items[idx].len >= ConnInboxQuota {
        return false
    }
    pidx := s.pool.AllocSlot(a) else { return false }
    WritePoolSlot(s.pool.SlotMut(pidx), dgram, from)
    items[idx].ring[items[idx].tail] = pidx
    items[idx].tail = (items[idx].tail + 1) % ConnInboxQuota
    items[idx].len += 1
    return true
}

// Sends a stateless INVALID_TOKEN close packet in Initial space (QT-277).
BuildInvalidTokenClose(client_dcid: &Cid, client_scid: &Cid, out: mut []u8, now: time.Instant): int {
    var keys := KeyRing.New(client_dcid, false)
    defer keys.Wipe()
    scid := Cid(b: [0; MaxCidLen], n: 0)
    var plain: [64]u8 = [0; 64]
    wn := WriteConnectionClose(mut plain[..], ErrInvalidToken, 0, b"", false) else { return 0 }
    plen := PadPlain(mut plain[..], wn, 1, 0)
    hdr := WriteLongHeader(out, PktInitial, client_scid, &scid, b"", plen + 16, 0, 1) else { return 0 }
    return keys.SealPacket(Space.Initial, 0, hdr.header_len, hdr.pn_offset, 1, plain[0..plen], out, now, time.Millis(999)) else { 0 }
}

// Dispatches one non-new-Initial datagram under EndpointShared lock.
EndpointShared.DispatchLocked(&self, nc: Io, now: time.Instant, parsed: &Parsed, dgram: []u8, from: net.Addr, reply_buf: mut []u8, expired: mut HeapList[QueuedConn]): (reply_len: int, wake_slot: int, wake_slots_n: int) {
    a := self.h.Alloc()
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    if s.closed {
        g.Unlock()
        return (reply_len: 0, wake_slot: -1, wake_slots_n: 0)
    }
    SweepClosingLocked(s, a, now)
    SweepExpiredAcceptLocked(s, now, expired, self.h)

    var reply_len := 0
    var wake_slot := -1
    var wake_slots_n := 0
    match *parsed {
        .VersionNeg {
            // QT-227: never answer VN with VN.
        }
        .Retry {
            // QT-223: server drops Retry packets.
        }
        .UnknownVersion(hdr) {
            if dgram.Len() >= MinInitialDatagramLen {
                vers: [1]u32 = [Version1]
                reply_len = WriteVersionNeg(reply_buf, 0x40, hdr.scid, hdr.dcid, vers[..])
                if reply_len > dgram.Len() {
                    reply_len = 0
                }
            }
        }
        .Short(hdr, packet) {
            _ = packet
            if hit := s.demux.Lookup(&hdr.dcid) {
                items := s.slots.ItemsMut()
                if items[hit.idx].gen_ == hit.gen_ {
                    if items[hit.idx].closing {
                        if items[hit.idx].close_state.Answer(&from, dgram.Len(), items[hit.idx].close_len) {
                            cl := items[hit.idx].close_len
                            (mut reply_buf[0..cl]).CopyFrom(items[hit.idx].close_pkt[0..cl])
                            reply_len = cl
                        }
                    } else if items[hit.idx].alive {
                        if PushToSlotLocked(s, a, hit.idx, dgram, from) {
                            wake_slot = hit.idx
                            wake_slots_n = s.slots.Len()
                        }
                    }
                }
            } else if dgram.Len() > MinShortPacketLen && hdr.dcid.n == LocalCidLen {
                // QT-334..QT-336: stateless reset for unknown short-header CID.
                if s.reset_limiter.Allow(now) {
                    tok := MakeStatelessResetToken(&self.static_key, &hdr.dcid)
                    reply_len = WriteStatelessReset(nc, reply_buf, &tok, dgram.Len())
                }
            }
        }
        .Long(hdr, packet) {
            _ = packet
            if hit := s.demux.Lookup(&hdr.dcid) {
                if hdr.kind == PktInitial && dgram.Len() < MinInitialDatagramLen {
                    // QT-268: server drops Initial in datagram under 1200 bytes.
                    _ = 0
                } else {
                    items := s.slots.ItemsMut()
                    if items[hit.idx].gen_ == hit.gen_ {
                        if items[hit.idx].closing {
                            if items[hit.idx].close_state.Answer(&from, dgram.Len(), items[hit.idx].close_len) {
                                cl := items[hit.idx].close_len
                                (mut reply_buf[0..cl]).CopyFrom(items[hit.idx].close_pkt[0..cl])
                                reply_len = cl
                            }
                        } else if items[hit.idx].alive {
                            if PushToSlotLocked(s, a, hit.idx, dgram, from) {
                                wake_slot = hit.idx
                                wake_slots_n = s.slots.Len()
                            }
                        }
                    }
                }
            } else if hdr.kind == PktInitial && dgram.Len() >= MinInitialDatagramLen {
                var addr_val := false
                var invalid_tok := false
                if hdr.token.Len() > 0 {
                    now_ns := InstantNanos(now)
                    match VerifyRetryToken(&s.retry_key, hdr.token, &from, now_ns, RetryTokenMaxAgeNs) {
                        .Valid(odcid) {
                            _ = odcid
                            addr_val = true
                        }
                        .InvalidRetry { invalid_tok = true }
                        .NotRetry {}
                    }
                }
                if invalid_tok {
                    reply_len = BuildInvalidTokenClose(&hdr.dcid, &hdr.scid, reply_buf, now)
                } else {
                    send_retry := !addr_val && ((s.cfg.retry_above <= 0) || (s.cfg.retry_above < s.cfg.half_open && s.half_open > s.cfg.retry_above))
                    if send_retry {
                        var new_scid := RandomLocalCid(nc) else { Cid(b: [0; MaxCidLen], n: 0) }
                        if new_scid.n == LocalCidLen && !CidEqual(&new_scid, &hdr.dcid) {
                            var tok_buf: [MaxRetryTokenLen]u8 = [0; MaxRetryTokenLen]
                            now_ns := InstantNanos(now)
                            tlen := MakeRetryToken(nc, &s.retry_key, &from, &hdr.dcid, now_ns, mut tok_buf[..])
                            if tlen > 0 {
                                reply_len = WriteRetryPacket(reply_buf, &hdr.scid, &new_scid, tok_buf[0..tlen], &hdr.dcid)
                            }
                        }
                    }
                }
            }
        }
    }
    g.Unlock()
    return (reply_len: reply_len, wake_slot: wake_slot, wake_slots_n: wake_slots_n)
}

// Dispatches one received UDP datagram on the server endpoint.
EndpointShared.DispatchDatagram(&self, io: Io, dgram: []u8, from: net.Addr) {
    nc := io.NoCancel()
    now := time.Now(nc)
    a := self.h.Alloc()

    parsed := ParsePacket(dgram, LocalCidLen) else { return }

    var reply_buf: [MaxSendDatagram]u8 = [0; MaxSendDatagram]
    var expired := HeapList[QueuedConn].New(a)
    res := self.DispatchLocked(nc, now, &parsed, dgram, from, mut reply_buf[..], mut expired)
    AbandonExpiredList(self.h, expired)
    if res.wake_slot >= 0 {
        self.WakeSlot(res.wake_slot, res.wake_slots_n)
    }
    if res.reply_len > 0 {
        self.SendDatagram(nc, reply_buf[0..res.reply_len], from) else {}
    }
}

AbandonExpiredList(h: &Heap, expired: HeapList[QueuedConn]) {
    a := h.Alloc()
    var list := expired
    for {
        qc := list.Pop() else { break }
        qc.Unpack(h).MuxAbandon()
    }
    list.FreeEmpty(a)
}

// Bound server QUIC endpoint implementing net.MuxListening.
linear view struct Endpoint {
    h: &Heap
    core: EpBox[EndpointShared]
}

// Binds UDP on addr and creates a server Endpoint (Section 3.7).
Endpoint.Listen(h: &Heap, io: Io, cfg: &Config, addr: str, alpn: str): net.Error!Endpoint from h {
    core.SyncCancel(io) else { fail Canceled }
    var udp := try net.Bind(io, addr)
    udp.SetBuffers(2 * 1024 * 1024, 0) else {}
    SetDontFragmentUdp(&udp)
    nc := io.NoCancel()
    var static_key: [32]u8 = [0; 32]
    crypto.Random(nc, mut static_key[..]) else {}
    var rng_b: [8]u8 = [0; 8]
    crypto.Random(nc, mut rng_b[..]) else {}
    seed := (Cast[u64](ReadBe32(rng_b[..], 0)) << 32) | Cast[u64](ReadBe32(rng_b[..], 4))
    var rk := RetryTokenKey.New(nc) else {
        udp.Close()
        fail Canceled
    }
    a := h.Alloc()
    var demux := DemuxTable.New(a, seed) else {
        rk.Wipe()
        udp.Close()
        fail OutOfMemory
    }
    var tls_owned := OwnedTlsCfg.New(a, cfg.Tls()) else {
        demux.Free(a)
        rk.Wipe()
        udp.Close()
        fail OutOfMemory
    }
    var alpn_buf: [255]u8 = [0; 255]
    ab := alpn.AsBytes()
    alen := if ab.Len() < 255 { ab.Len() } else { 255 }
    (mut alpn_buf[0..alen]).CopyFrom(ab[0..alen])

    st := EndpointState(
        closed: false,
        pool: DgramPool.New(a),
        demux: demux,
        slots: HeapList[ConnSlot].New(a),
        free_slots: HeapList[int].New(a),
        accept_q: HeapList[AcceptEntry].New(a),
        half_open: 0,
        reset_limiter: ResetLimiter.New(),
        retry_key: rk,
        cfg: cfg.ToEngineCfg(),
        alpn_buf: alpn_buf,
        alpn_len: alen,
        hs_limit: cfg.Tls().handshake,
    )
    var shared := EndpointShared(
        h: h,
        nc: nc,
        sock: udp.Share(),
        static_key: static_key,
        tls_cfg: tls_owned,
        notifies: Notify1024(),
        accept_notify: sync.Notify.New(),
        st: Mutex[EndpointState].New(st),
    )
    box := EpBox[EndpointShared].New(h, shared) else {
        shared.Free(h)
        fail OutOfMemory
    }
    return Endpoint(h: h, core: box)
}

// Returns the bound UDP port. O(1).
Endpoint.EpPort(&self): int {
    return self.core.Ref().sock.Port()
}

// Runs the demux receive loop until io's scope is cancelled (Section 3.7.1).
Endpoint.EpRun(&self, io: Io): net.Error!void {
    ep_ref := self.core.Ref()
    var rx_buf: [PoolSlotBytes]u8 = [0; PoolSlotBytes]
    for {
        got := ep_ref.sock.Recv(io, mut rx_buf[..]) else e {
            fail e
        }
        DispatchWithEp(&self.core, io, rx_buf[0..got.n], got.peer)
    }
}

CreateServerQConnOpt(
    h: &Heap,
    nc: Io,
    ep_box: &EpBox[EndpointShared],
    conn_idx: int,
    conn_gen: u32,
    client_scid: &Cid,
    first_dcid: &Cid,
    orig_dcid: &Cid,
    retry_scid: ?Cid,
    local_scid: &Cid,
    from: net.Addr,
    addr_val: bool,
    first_rx_bytes: i64,
    cfg: &EngineCfg,
    alpn: str,
): ?QueuedConn {
    ep_clone := ep_box.Clone(h)
    var qc := QConn.NewServer(
        h,
        nc,
        ep_clone,
        conn_idx,
        conn_gen,
        client_scid,
        first_dcid,
        orig_dcid,
        retry_scid,
        local_scid,
        from,
        addr_val,
        first_rx_bytes,
        cfg,
        alpn,
    ) else {
        if es := ep_clone.Release(h) {
            es.Free(h)
        }
        return none
    }
    packed := QueuedConn.Pack(h, qc) else {
        qc.MuxAbandon()
        return none
    }
    return some(packed)
}

// Dispatches a datagram using the Endpoint's EpBox so newly accepted QConns
// receive cloned EpBox handles.
DispatchWithEp(ep_box: &EpBox[EndpointShared], io: Io, dgram: []u8, from: net.Addr) {
    ep_ref := ep_box.Ref()
    h := ep_ref.h
    nc := io.NoCancel()
    now := time.Now(nc)
    a := h.Alloc()

    parsed := ParsePacket(dgram, LocalCidLen) else { return }

    // Fast path for non-new-connection packets: delegate directly to DispatchDatagram,
    // except Long PktInitial with unknown DCID which creates QConn with a cloned EpBox.
    var is_new_initial := false
    match parsed {
        .Long(hdr, packet) {
            _ = packet
            if hdr.kind == PktInitial && dgram.Len() >= MinInitialDatagramLen {
                var g := ep_ref.st.Lock(nc).Must()
                known := g.Mut().demux.Lookup(&hdr.dcid).IsSome()
                g.Unlock()
                if !known {
                    is_new_initial = true
                }
            }
        }
        _ {}
    }
    if !is_new_initial {
        ep_ref.DispatchDatagram(io, dgram, from)
        return
    }

    // Handle new Initial packet with potential QConn creation (requiring ep_box.Clone()).
    var reply_buf: [MaxSendDatagram]u8 = [0; MaxSendDatagram]
    var reply_len := 0
    var expired := HeapList[QueuedConn].New(a)
    var wake_accept := false
    var wake_slot := -1
    var wake_total := 0

    match parsed {
        .Long(hdr, packet) {
            _ = packet
            var g := ep_ref.st.Lock(nc).Must()
            s := mut *g.Mut()
            if s.closed {
                g.Unlock()
                expired.FreeEmpty(a)
                return
            }
            SweepClosingLocked(s, a, now)
            SweepExpiredAcceptLocked(s, now, mut expired, h)

            // Re-check demux after locking.
            if hit := s.demux.Lookup(&hdr.dcid) {
                items := s.slots.ItemsMut()
                if items[hit.idx].gen_ == hit.gen_ && items[hit.idx].alive {
                    if PushToSlotLocked(s, a, hit.idx, dgram, from) {
                        wake_slot = hit.idx
                        wake_total = s.slots.Len()
                    }
                }
                g.Unlock()
            } else {
                var addr_val := false
                var orig_dcid := hdr.dcid
                var rscid: ?Cid = none
                var invalid_tok := false

                if hdr.token.Len() > 0 {
                    now_ns := InstantNanos(now)
                    match VerifyRetryToken(&s.retry_key, hdr.token, &from, now_ns, RetryTokenMaxAgeNs) {
                        .Valid(odcid) {
                            addr_val = true
                            orig_dcid = odcid
                            rscid = some(hdr.dcid)
                        }
                        .InvalidRetry { invalid_tok = true }
                        .NotRetry {}
                    }
                }

                if invalid_tok {
                    reply_len = BuildInvalidTokenClose(&hdr.dcid, &hdr.scid, mut reply_buf[..], now)
                    g.Unlock()
                } else {
                    send_retry := !addr_val && ((s.cfg.retry_above <= 0) || (s.cfg.retry_above < s.cfg.half_open && s.half_open > s.cfg.retry_above))
                    if !addr_val && s.half_open >= s.cfg.half_open && s.cfg.retry_above >= s.cfg.half_open {
                        g.Unlock()
                    } else if send_retry {
                        var new_scid := RandomLocalCid(nc) else { Cid(b: [0; MaxCidLen], n: 0) }
                        if new_scid.n == LocalCidLen && !CidEqual(&new_scid, &hdr.dcid) {
                            var tok_buf: [MaxRetryTokenLen]u8 = [0; MaxRetryTokenLen]
                            now_ns := InstantNanos(now)
                            tlen := MakeRetryToken(nc, &s.retry_key, &from, &hdr.dcid, now_ns, mut tok_buf[..])
                            if tlen > 0 {
                                reply_len = WriteRetryPacket(mut reply_buf[..], &hdr.scid, &new_scid, tok_buf[0..tlen], &hdr.dcid)
                            }
                        }
                        g.Unlock()
                    } else if s.half_open < s.cfg.half_open && s.accept_q.Len() < AcceptQueueCap {
                        var local_scid := RandomLocalCid(nc) else { Cid(b: [0; MaxCidLen], n: 0) }
                        var attempts := 0
                        for attempts < 8 && (local_scid.n != LocalCidLen || s.demux.Lookup(&local_scid).IsSome()) {
                            local_scid = RandomLocalCid(nc) else { Cid(b: [0; MaxCidLen], n: 0) }
                            attempts += 1
                        }
                        if local_scid.n == LocalCidLen && s.demux.Lookup(&local_scid).IsNone() {
                            var has_slot := true
                            slot := AllocConnSlotLocked(s, a) else {
                                has_slot = false
                                (idx: -1, gen_: 0)
                            }
                            if has_slot {
                                alpn_s := str.From(s.alpn_buf[0..s.alpn_len]).Must()
                                cfg_copy := s.cfg
                                match CreateServerQConnOpt(
                                    h,
                                    nc,
                                    ep_box,
                                    slot.idx,
                                    slot.gen_,
                                    &hdr.scid,
                                    &hdr.dcid,
                                    &orig_dcid,
                                    rscid,
                                    &local_scid,
                                    from,
                                    addr_val,
                                    i64(dgram.Len()),
                                    &cfg_copy,
                                    alpn_s,
                                ) {
                                    none {
                                        s.slots.ItemsMut()[slot.idx].alive = false
                                        s.slots.ItemsMut()[slot.idx].gen_ += 1
                                        s.free_slots.Push(a, slot.idx) else {}
                                    }
                                    some(qbox) {
                                        _ = s.demux.Insert(a, &local_scid, slot.idx, slot.gen_) else {}
                                        _ = s.demux.Insert(a, &hdr.dcid, slot.idx, slot.gen_) else {}
                                        slots_m := s.slots.ItemsMut()
                                        slots_m[slot.idx].TrackCid(&local_scid)
                                        slots_m[slot.idx].TrackCid(&hdr.dcid)
                                        if PushToSlotLocked(s, a, slot.idx, dgram, from) {
                                            wake_slot = slot.idx
                                            wake_total = s.slots.Len()
                                        }
                                        prev_q := s.accept_q.Len()
                                        var ae := AcceptEntry(conn: qbox, created_at: now)
                                        s.accept_q.Push(a, ae) else {
                                            AcceptEntry(conn: qb, created_at: _) := ae
                                            qb.Unpack(h).MuxAbandon()
                                        }
                                        if s.accept_q.Len() > prev_q {
                                            s.half_open += 1
                                            wake_accept = true
                                        }
                                    }
                                }
                            }
                        }
                        g.Unlock()
                    } else {
                        g.Unlock()
                    }
                }
            }
        }
        _ {}
    }

    AbandonExpiredList(h, expired)
    if wake_slot >= 0 {
        ep_ref.WakeSlot(wake_slot, wake_total)
    }
    if wake_accept {
        ep_ref.accept_notify.NotifyOne()
    }
    if reply_len > 0 {
        ep_ref.SendDatagram(nc, reply_buf[0..reply_len], from) else {}
    }
}

// Pops an expired-swept incoming connection from accept_q under st lock.
EndpointShared.PopAcceptLocked(&self, nc: Io, expired: mut HeapList[QueuedConn]): (picked: ?QueuedConn, closed: bool) {
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    SweepExpiredAcceptLocked(s, time.Now(nc), expired, self.h)
    var closed := false
    picked := if s.accept_q.Len() > 0 {
        AcceptEntry(conn, created_at: _) := s.accept_q.Remove(0)
        some(conn)
    } else {
        if s.closed {
            closed = true
        }
        none
    }
    g.Unlock()
    return (picked: picked, closed: closed)
}

// Marks endpoint closed and drains accept_q under st lock.
EndpointShared.DrainAcceptLocked(&self, nc: Io, drained: mut HeapList[QueuedConn]) {
    a := self.h.Alloc()
    var g := self.st.Lock(nc).Must()
    s := mut *g.Mut()
    s.closed = true
    for s.accept_q.Len() > 0 {
        entry := s.accept_q.Remove(0)
        AcceptEntry(conn, created_at: _) := entry
        drained.Push(a, conn) else {
            conn.Unpack(self.h).MuxAbandon()
        }
    }
    g.Unlock()
}

// Accepts the next queued incoming connection, wrapping it in net.Mux on h.
Endpoint.EpAccept(&self, h: &Heap, io: Io): net.Error!net.Mux from h {
    ep_ref := self.core.Ref()
    nc := io.NoCancel()
    a := self.h.Alloc()
    for {
        var expired := HeapList[QueuedConn].New(a)
        res := ep_ref.PopAcceptLocked(nc, mut expired)
        AbandonExpiredList(self.h, expired)
        (picked, closed) := res
        if raw_qc := picked {
            qc := raw_qc.Unpack(h)
            var empty_qc: ?QConn = none
            var b := Box[?QConn].New(h.Alloc(), empty_qc) else {
                if x := empty_qc {
                    x.MuxAbandon()
                }
                qc.MuxAbandon()
                fail OutOfMemory
            }
            sb := core.SealedBox.Fill[QConn](b, qc)
            return net.Mux.Of[QConn](h, sb)
        }
        if closed {
            fail Closed
        }
        try ep_ref.accept_notify.Wait(io)
    }
}

// Closes the endpoint accept queue and releases Endpoint's handle to EndpointShared.
Endpoint.EpAbandon(self) {
    Endpoint(h, core) := self
    ep_ref := core.Ref()
    nc := ep_ref.nc
    a := h.Alloc()
    var drained := HeapList[QueuedConn].New(a)
    ep_ref.DrainAcceptLocked(nc, mut drained)
    ep_ref.accept_notify.NotifyAll()
    AbandonExpiredList(h, drained)
    if es := core.Release(h) {
        es.Free(h)
    }
}
