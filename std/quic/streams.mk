// Why: QUIC multiplexes bidirectional and unidirectional streams over one
// connection, enforcing stream-ID roles, concurrency limits, stream and
// connection flow control, and returning MAX_STREAMS credit only as closed and
// released stream records are freed.
//
// What: Streams (New, Free, Open, Accept, OnStream, OnReset, OnStop,
// OnMaxData, OnMaxStreamData, OnMaxStreams, OnStreamDataBlocked,
// OnStreamsBlocked, Next, OnAcked, OnLost, Read, Write, Reset, Stop, Credit,
// Release, SetPeerLimits, HasPending).
//
// How: stores active streams in a HeapMap[i64, StreamEntry] alongside an
// active-ID list for bounded Next walks, and tracks emitted control frames in
// a slot-reused CtrlRec table indexed by SentKind.Control(index).

// Kind of stream/flow control frame recorded in SentKind.Control(index).
enum CtrlKind {
    MaxData(seq: u64)
    MaxStreamData(id: i64, seq: u64)
    MaxStreams(bidi: bool, seq: u64)
    ResetStream(id: i64)
    StopSending(id: i64)
    DataBlocked(limit: u64)
    StreamDataBlocked(id: i64, limit: u64)
    StreamsBlocked(bidi: bool, limit: u64)
}

// Entry in the Streams control-frame tracking table.
struct CtrlRec {
    live: bool
    kind: CtrlKind
}

// Slot-reused control-frame tracking table for SentKind.Control(index).
linear view struct CtrlTable {
    recs: HeapList[CtrlRec]
    free: HeapList[int]
}

CtrlTable.New(a: &Alloc): CtrlTable {
    return CtrlTable(recs: HeapList[CtrlRec].New(a), free: HeapList[int].New(a))
}

CtrlTable.Free(self, a: &Alloc) {
    CtrlTable(recs, free) := self
    recs.Free(a)
    free.Free(a)
}

CtrlTable.Emit(mut self, a: &Alloc, kind: CtrlKind, frames: mut []SentFrame, fn_cnt: int): int {
    var idx := -1
    if self.free.Len() > 0 {
        idx = self.free.Pop().Must()
        items := self.recs.ItemsMut()
        items[idx] = CtrlRec(live: true, kind: kind)
    } else {
        idx = self.recs.Len()
        self.recs.Push(a, CtrlRec(live: true, kind: kind)) else {
            return fn_cnt
        }
    }
    frames[fn_cnt] = SentFrame(kind: SentKind.Control(index: idx))
    return fn_cnt + 1
}

CtrlTable.Take(mut self, a: &Alloc, idx: int): ?CtrlKind {
    if idx < 0 || idx >= self.recs.Len() || !self.recs[idx].live {
        return none
    }
    k := self.recs[idx].kind
    items := self.recs.ItemsMut()
    items[idx].live = false
    self.free.Push(a, idx) else {}
    return some(k)
}

// Combined send and receive state for one open stream.
// Linear: owns StreamSend and StreamRecv.
linear struct StreamEntry {
    id: i64
    is_local: bool
    is_bidi: bool
    has_send: bool
    has_recv: bool
    send: StreamSend
    recv: StreamRecv
    released: bool
}

impl Free for StreamEntry {
    Free(self, a: &Alloc) {
        StreamEntry(id: _, is_local: _, is_bidi: _, has_send: _, has_recv: _, send, recv, released: _) := self
        send.Free(a)
        recv.Free(a)
    }
}

// Reports whether both active halves have finished on the wire.
StreamEntry.BothTerminal(&self): bool {
    send_done := !self.has_send || self.send.IsTerminal()
    recv_done := !self.has_recv || self.recv.IsFinished()
    return send_done && recv_done
}

// Stream table, stream-ID allocator, and connection/stream flow coordinator
// for one QUIC connection. Linear: owns heap-allocated stream map and queues.
linear view struct Streams {
    client: bool
    limits: StreamLimits
    conn_flow: ConnFlow
    map: HeapMap[i64, StreamEntry]
    active_ids: HeapList[i64]
    accept_bidi: HeapList[i64]
    accept_uni: HeapList[i64]
    next_local_bidi_seq: u64
    next_local_uni_seq: u64
    next_peer_bidi_seq: u64
    next_peer_uni_seq: u64
    local_max_streams_bidi: u64
    local_max_streams_uni: u64
    max_streams_bidi_seq: u64
    max_streams_uni_seq: u64
    max_streams_bidi_pending: bool
    max_streams_uni_pending: bool
    streams_blocked_bidi_at: ?u64
    streams_blocked_uni_at: ?u64
    streams_blocked_bidi_pending: bool
    streams_blocked_uni_pending: bool
    ctrl: CtrlTable
    rr_cursor: int
}

// Creates a Streams table for client or server with initial limits.
// O(1); allocates nothing until streams open.
Streams.New(a: &Alloc, client: bool, limits: &StreamLimits): OutOfMemory!Streams from a {
    return Streams(
        client: client,
        limits: *limits,
        conn_flow: ConnFlow.New(limits.max_data, limits.peer_max_data),
        map: HeapMap[i64, StreamEntry].New(a),
        active_ids: HeapList[i64].New(a),
        accept_bidi: HeapList[i64].New(a),
        accept_uni: HeapList[i64].New(a),
        next_local_bidi_seq: 0,
        next_local_uni_seq: 0,
        next_peer_bidi_seq: 0,
        next_peer_uni_seq: 0,
        local_max_streams_bidi: limits.max_streams_bidi,
        local_max_streams_uni: limits.max_streams_uni,
        max_streams_bidi_seq: 0,
        max_streams_uni_seq: 0,
        max_streams_bidi_pending: false,
        max_streams_uni_pending: false,
        streams_blocked_bidi_at: none,
        streams_blocked_uni_at: none,
        streams_blocked_bidi_pending: false,
        streams_blocked_uni_pending: false,
        ctrl: CtrlTable.New(a),
        rr_cursor: 0,
    )
}

// Releases all streams and tables back to a. O(open_streams).
Streams.Free(self, a: &Alloc) {
    Streams(
        client: _,
        limits: _,
        conn_flow: _,
        map,
        active_ids,
        accept_bidi,
        accept_uni,
        next_local_bidi_seq: _,
        next_local_uni_seq: _,
        next_peer_bidi_seq: _,
        next_peer_uni_seq: _,
        local_max_streams_bidi: _,
        local_max_streams_uni: _,
        max_streams_bidi_seq: _,
        max_streams_uni_seq: _,
        max_streams_bidi_pending: _,
        max_streams_uni_pending: _,
        streams_blocked_bidi_at: _,
        streams_blocked_uni_at: _,
        streams_blocked_bidi_pending: _,
        streams_blocked_uni_pending: _,
        ctrl,
        rr_cursor: _,
    ) := self
    map.Free(a)
    active_ids.Free(a)
    accept_bidi.Free(a)
    accept_uni.Free(a)
    ctrl.Free(a)
}

// Applies peer transport parameter limits once received. O(1).
Streams.SetPeerLimits(mut self, limits: &StreamLimits) {
    self.limits.peer_max_data = limits.peer_max_data
    self.limits.peer_max_stream_data_bidi_local = limits.peer_max_stream_data_bidi_local
    self.limits.peer_max_stream_data_bidi_remote = limits.peer_max_stream_data_bidi_remote
    self.limits.peer_max_stream_data_uni = limits.peer_max_stream_data_uni
    self.limits.peer_max_streams_bidi = limits.peer_max_streams_bidi
    self.limits.peer_max_streams_uni = limits.peer_max_streams_uni
    self.conn_flow.OnMaxData(limits.peer_max_data)
}

// Classifies stream id relative to our role (RFC 9000 2.1).
Streams.Classify(&self, id: i64): (valid: bool, is_local: bool, is_bidi: bool, seq: u64) {
    if id < 0 || Cast[u64](id) > MaxOffset {
        return (valid: false, is_local: false, is_bidi: false, seq: 0)
    }
    uid := Cast[u64](id)
    initiator_server := (uid & 0x01) != 0
    is_uni := (uid & 0x02) != 0
    is_local := initiator_server != self.client
    return (valid: true, is_local: is_local, is_bidi: !is_uni, seq: uid >> 2)
}

// Computes the stream ID for (is_local, is_bidi, seq) per RFC 9000 2.1.
Streams.MakeId(&self, is_local: bool, is_bidi: bool, seq: u64): i64 {
    server_bit := if (is_local && !self.client) || (!is_local && self.client) { u64(1) } else { u64(0) }
    uni_bit := if is_bidi { u64(0) } else { u64(2) }
    return Cast[i64]((seq << 2) | uni_bit | server_bit)
}

// Allocates a StreamEntry with initial windows matching its type and role.
Streams.CreateEntry(mut self, a: &Alloc into self, id: i64, is_local: bool, is_bidi: bool): OutOfMemory!void {
    has_send := is_bidi || is_local
    has_recv := is_bidi || !is_local
    peer_tx := if !is_bidi {
        self.limits.peer_max_stream_data_uni
    } else if is_local {
        self.limits.peer_max_stream_data_bidi_remote
    } else {
        self.limits.peer_max_stream_data_bidi_local
    }
    local_rx := if !is_bidi {
        self.limits.max_stream_data_uni
    } else if is_local {
        self.limits.max_stream_data_bidi_local
    } else {
        self.limits.max_stream_data_bidi_remote
    }
    // Server peer-initiated bidi streams grant additional credit only via Credit (3.4.3).
    auto_credit := self.client || !is_bidi || is_local
    var entry := StreamEntry(
        id: id,
        is_local: is_local,
        is_bidi: is_bidi,
        has_send: has_send,
        has_recv: has_recv,
        send: StreamSend.New(a, peer_tx),
        recv: StreamRecv.New(a, local_rx, auto_credit),
        released: false,
    )
    onerr entry.Free(a)
    try self.map.Set(a, id, entry)
    try self.active_ids.Push(a, id)
}

// Opens all peer-initiated streams of type is_bidi up through seq (RFC 9000 3.2).
// Enforces local_max_streams_* (RFC 9000 4.6).
Streams.EnsurePeerStream(mut self, a: &Alloc into self, id: i64, is_bidi: bool, seq: u64): Error!bool {
    limit := if is_bidi { self.local_max_streams_bidi } else { self.local_max_streams_uni }
    if seq >= limit {
        fail TransportError(code: ErrStreamLimit, frame: 0)
    }
    next_seq := if is_bidi { self.next_peer_bidi_seq } else { self.next_peer_uni_seq }
    if seq < next_seq {
        return self.map.Get(&id).IsSome()
    }
    var cur := next_seq
    for cur <= seq {
        cid := self.MakeId(false, is_bidi, cur)
        try self.CreateEntry(a, cid, false, is_bidi)
        if is_bidi {
            try self.accept_bidi.Push(a, cid)
            self.next_peer_bidi_seq = cur + 1
        } else {
            try self.accept_uni.Push(a, cid)
            self.next_peer_uni_seq = cur + 1
        }
        cur += 1
    }
    return true
}

// Frees a stream record and returns MAX_STREAMS credit if both halves are
// terminal and the application has called Release (QT-51). O(active_ids.Len()).
Streams.MaybeReap(mut self, a: &Alloc, id: i64) {
    reap := match self.map.Get(&id) {
        none { false }
        some(e) { e.released && e.BothTerminal() }
    }
    if !reap {
        return
    }
    entry := self.map.Remove(&id).Must()
    was_local := entry.is_local
    was_bidi := entry.is_bidi
    entry.Free(a)
    for i in 0..self.active_ids.Len() {
        if self.active_ids[i] == id {
            _ = self.active_ids.Remove(i)
            break
        }
    }
    if !was_local {
        if was_bidi {
            if self.local_max_streams_bidi < MaxStreamCount {
                self.local_max_streams_bidi += 1
                self.max_streams_bidi_pending = true
            }
        } else {
            if self.local_max_streams_uni < MaxStreamCount {
                self.local_max_streams_uni += 1
                self.max_streams_uni_pending = true
            }
        }
    }
}

// Opens a new locally-initiated stream of direction uni, or returns none when
// the peer's stream limit is reached (queuing STREAMS_BLOCKED, RFC 9000 4.6).
// Stream IDs rise by 4 per type and are never reused (RFC 9000 2.1).
Streams.Open(mut self, a: &Alloc into self, uni: bool): Error!?i64 {
    is_bidi := !uni
    seq := if is_bidi { self.next_local_bidi_seq } else { self.next_local_uni_seq }
    limit := if is_bidi { self.limits.peer_max_streams_bidi } else { self.limits.peer_max_streams_uni }
    if seq >= limit {
        if is_bidi {
            need := match self.streams_blocked_bidi_at {
                none { true }
                some(prev) { prev != limit }
            }
            if need {
                self.streams_blocked_bidi_at = some(limit)
                self.streams_blocked_bidi_pending = true
            }
        } else {
            need := match self.streams_blocked_uni_at {
                none { true }
                some(prev) { prev != limit }
            }
            if need {
                self.streams_blocked_uni_at = some(limit)
                self.streams_blocked_uni_pending = true
            }
        }
        return none
    }
    id := self.MakeId(true, is_bidi, seq)
    try self.CreateEntry(a, id, true, is_bidi)
    if is_bidi {
        self.next_local_bidi_seq = seq + 1
    } else {
        self.next_local_uni_seq = seq + 1
    }
    return some(id)
}

// Returns the next unopened peer-initiated stream ID of direction uni, or none. O(1).
Streams.Accept(mut self, uni: bool): ?i64 {
    if uni {
        if self.accept_uni.Len() == 0 {
            return none
        }
        return some(self.accept_uni.Remove(0))
    }
    if self.accept_bidi.Len() == 0 {
        return none
    }
    return some(self.accept_bidi.Remove(0))
}

// Processes a received STREAM frame (RFC 9000 3.2, 4.1, 4.5, 4.6, 19.8).
Streams.OnStream(mut self, a: &Alloc into self, id: i64, offset: u64, data: []u8, fin: bool): Error!void {
    c := self.Classify(id)
    if !c.valid {
        fail TransportError(code: ErrFrameEncoding, frame: FrameStreamBase)
    }
    if c.is_local && !c.is_bidi {
        fail TransportError(code: ErrStreamState, frame: FrameStreamBase)
    }
    if c.is_local {
        if c.seq >= self.next_local_bidi_seq {
            fail TransportError(code: ErrStreamState, frame: FrameStreamBase)
        }
        if self.map.Get(&id).IsNone() {
            return
        }
    } else {
        alive := try self.EnsurePeerStream(a, id, c.is_bidi, c.seq)
        if !alive {
            return
        }
    }
    entry := self.map.GetMut(&id).Must()
    try entry.recv.OnStream(a, mut self.conn_flow, offset, data, fin)
    self.MaybeReap(a, id)
}

// Processes a received RESET_STREAM frame (RFC 9000 3.2, 4.5, 19.4).
Streams.OnReset(mut self, a: &Alloc into self, id: i64, code: u64, final_size: u64): Error!void {
    c := self.Classify(id)
    if !c.valid {
        fail TransportError(code: ErrFrameEncoding, frame: FrameResetStream)
    }
    if c.is_local && !c.is_bidi {
        fail TransportError(code: ErrStreamState, frame: FrameResetStream)
    }
    if c.is_local {
        if c.seq >= self.next_local_bidi_seq {
            fail TransportError(code: ErrStreamState, frame: FrameResetStream)
        }
        if self.map.Get(&id).IsNone() {
            return
        }
    } else {
        alive := try self.EnsurePeerStream(a, id, c.is_bidi, c.seq)
        if !alive {
            return
        }
    }
    entry := self.map.GetMut(&id).Must()
    try entry.recv.OnReset(mut self.conn_flow, code, final_size)
    self.MaybeReap(a, id)
}

// Processes a received STOP_SENDING frame (RFC 9000 3.2, 3.5, 19.5).
Streams.OnStop(mut self, a: &Alloc into self, id: i64, code: u64): Error!void {
    c := self.Classify(id)
    if !c.valid {
        fail TransportError(code: ErrFrameEncoding, frame: FrameStopSending)
    }
    if !c.is_local && !c.is_bidi {
        fail TransportError(code: ErrStreamState, frame: FrameStopSending)
    }
    if c.is_local {
        next_seq := if c.is_bidi { self.next_local_bidi_seq } else { self.next_local_uni_seq }
        if c.seq >= next_seq {
            fail TransportError(code: ErrStreamState, frame: FrameStopSending)
        }
        if self.map.Get(&id).IsNone() {
            return
        }
    } else {
        alive := try self.EnsurePeerStream(a, id, true, c.seq)
        if !alive {
            return
        }
    }
    entry := self.map.GetMut(&id).Must()
    entry.send.OnStop(code)
}

// Processes a received MAX_DATA frame (RFC 9000 4.1, 19.9).
Streams.OnMaxData(mut self, v: u64) {
    self.conn_flow.OnMaxData(v)
}

// Processes a received MAX_STREAM_DATA frame (RFC 9000 3.2, 4.1, 19.10).
Streams.OnMaxStreamData(mut self, a: &Alloc into self, id: i64, v: u64): Error!void {
    c := self.Classify(id)
    if !c.valid {
        fail TransportError(code: ErrFrameEncoding, frame: FrameMaxStreamData)
    }
    if !c.is_local && !c.is_bidi {
        fail TransportError(code: ErrStreamState, frame: FrameMaxStreamData)
    }
    if c.is_local {
        next_seq := if c.is_bidi { self.next_local_bidi_seq } else { self.next_local_uni_seq }
        if c.seq >= next_seq {
            fail TransportError(code: ErrStreamState, frame: FrameMaxStreamData)
        }
        if self.map.Get(&id).IsNone() {
            return
        }
    } else {
        alive := try self.EnsurePeerStream(a, id, true, c.seq)
        if !alive {
            return
        }
    }
    entry := self.map.GetMut(&id).Must()
    entry.send.OnMaxStreamData(v)
}

// Processes a received MAX_STREAMS frame (RFC 9000 4.6, 19.11).
Streams.OnMaxStreams(mut self, bidi: bool, v: u64): Error!void {
    if v > MaxStreamCount {
        ty := if bidi { FrameMaxStreamsBidi } else { FrameMaxStreamsUni }
        fail TransportError(code: ErrFrameEncoding, frame: ty)
    }
    if bidi {
        if v > self.limits.peer_max_streams_bidi {
            self.limits.peer_max_streams_bidi = v
            self.streams_blocked_bidi_pending = false
        }
    } else {
        if v > self.limits.peer_max_streams_uni {
            self.limits.peer_max_streams_uni = v
            self.streams_blocked_uni_pending = false
        }
    }
}

// Processes a received STREAM_DATA_BLOCKED frame (RFC 9000 19.13).
Streams.OnStreamDataBlocked(mut self, a: &Alloc into self, id: i64, limit: u64): Error!void {
    _ = limit
    c := self.Classify(id)
    if !c.valid {
        fail TransportError(code: ErrFrameEncoding, frame: FrameStreamDataBlocked)
    }
    if c.is_local && !c.is_bidi {
        fail TransportError(code: ErrStreamState, frame: FrameStreamDataBlocked)
    }
    if c.is_local {
        if c.seq >= self.next_local_bidi_seq {
            fail TransportError(code: ErrStreamState, frame: FrameStreamDataBlocked)
        }
        return
    }
    _ = try self.EnsurePeerStream(a, id, c.is_bidi, c.seq)
}

// Processes a received DATA_BLOCKED frame (RFC 9000 19.12).
Streams.OnDataBlocked(mut self, limit: u64) {
    self.conn_flow.OnDataBlocked(limit)
}

// Processes a received STREAMS_BLOCKED frame (RFC 9000 19.14).
Streams.OnStreamsBlocked(mut self, bidi: bool, limit: u64) {
    _ = bidi
    _ = limit
}

// Aborts reading on stream id, queuing STOP_SENDING with code (RFC 9000 3.5).
Streams.Stop(mut self, id: i64, code: u64) {
    match self.map.GetMut(&id) {
        some(entry) {
            if entry.has_recv {
                entry.recv.Stop(code)
            }
        }
        none {}
    }
}

// Raises stream id's receive limit to at least total from stream start (3.4.3).
Streams.Credit(mut self, id: i64, total: u64) {
    match self.map.GetMut(&id) {
        some(entry) {
            if entry.has_recv && entry.recv.CanSendMaxStreamData() {
                entry.recv.credit.Admit(total)
            }
        }
        none {}
    }
}

// Releases the application's handle to stream id; frees and returns MAX_STREAMS
// credit once both halves are terminal (QT-51).
Streams.Release(mut self, a: &Alloc, id: i64) {
    match self.map.GetMut(&id) {
        some(entry) {
            entry.released = true
            self.MaybeReap(a, id)
        }
        none {}
    }
}

// Reads bytes from stream id's receive half.
Streams.Read(mut self, a: &Alloc, id: i64, buf: mut []u8): Error!?int {
    entry := self.map.GetMut(&id) else {
        fail Closed
    }
    if !entry.has_recv {
        fail TransportError(code: ErrStreamState, frame: 0)
    }
    res := entry.recv.Read(buf) else e {
        self.MaybeReap(a, id)
        fail e
    }
    self.MaybeReap(a, id)
    return res
}

// Writes data (and optional fin) to stream id's send half.
Streams.Write(mut self, a: &Alloc, id: i64, data: []u8, fin: bool): Error!int {
    entry := self.map.GetMut(&id) else {
        fail Closed
    }
    if !entry.has_send {
        fail TransportError(code: ErrStreamState, frame: 0)
    }
    return try entry.send.Write(a, data, fin)
}

// Resets the send half of stream id with code (RFC 9000 3.3).
Streams.Reset(mut self, id: i64, code: u64) {
    match self.map.GetMut(&id) {
        some(entry) {
            if entry.has_send {
                entry.send.Reset(code)
            }
        }
        none {}
    }
}

// Emits stream control and STREAM frames into out[0..max] and records their
// SentFrames into frames, returning bytes written and frame count.
Streams.Next(mut self, a: &Alloc, out: mut []u8, max: int, frames: mut []SentFrame): (n: int, frames: int) {
    limit := if max < out.Len() { max } else { out.Len() }
    var off := 0
    var fn_cnt := 0

    // 1. Connection-level MAX_DATA.
    if fn_cnt < frames.Len() {
        match self.conn_flow.rx_credit.Owed() {
            some(peek) {
                need := 1 + VarintLen(peek)
                if off + need <= limit {
                    v := self.conn_flow.rx_credit.TakeOwed().Must()
                    w := WriteMaxData(mut out[off..limit], v) else { 0 }
                    if w > 0 {
                        prev := fn_cnt
                        fn_cnt = self.ctrl.Emit(a, CtrlKind.MaxData(seq: self.conn_flow.rx_credit.tx_seq), frames, fn_cnt)
                        if fn_cnt > prev {
                            off += w
                        }
                    }
                }
            }
            none {}
        }
    }

    // 2. MAX_STREAMS (bidi, uni).
    if fn_cnt < frames.Len() && self.max_streams_bidi_pending {
        w := WriteMaxStreams(mut out[off..limit], true, self.local_max_streams_bidi) else { 0 }
        if w > 0 {
            self.max_streams_bidi_pending = false
            self.max_streams_bidi_seq += 1
            prev := fn_cnt
            fn_cnt = self.ctrl.Emit(a, CtrlKind.MaxStreams(bidi: true, seq: self.max_streams_bidi_seq), frames, fn_cnt)
            if fn_cnt > prev {
                off += w
            }
        }
    }
    if fn_cnt < frames.Len() && self.max_streams_uni_pending {
        w := WriteMaxStreams(mut out[off..limit], false, self.local_max_streams_uni) else { 0 }
        if w > 0 {
            self.max_streams_uni_pending = false
            self.max_streams_uni_seq += 1
            prev := fn_cnt
            fn_cnt = self.ctrl.Emit(a, CtrlKind.MaxStreams(bidi: false, seq: self.max_streams_uni_seq), frames, fn_cnt)
            if fn_cnt > prev {
                off += w
            }
        }
    }

    // 3. Per-stream control frames (RESET_STREAM, STOP_SENDING, MAX_STREAM_DATA).
    for i in 0..self.active_ids.Len() {
        if fn_cnt >= frames.Len() || off >= limit {
            break
        }
        id := self.active_ids[i]
        entry := self.map.GetMut(&id).Must()
        if entry.has_send && entry.send.reset_pending {
            w := WriteResetStream(mut out[off..limit], id, entry.send.reset_code, entry.send.reset_final_size) else { 0 }
            if w > 0 {
                entry.send.reset_pending = false
                prev := fn_cnt
                fn_cnt = self.ctrl.Emit(a, CtrlKind.ResetStream(id: id), frames, fn_cnt)
                if fn_cnt > prev {
                    off += w
                }
            }
        }
        if fn_cnt >= frames.Len() {
            break
        }
        if entry.has_recv && entry.recv.stop_pending {
            w := WriteStopSending(mut out[off..limit], id, entry.recv.stop_code) else { 0 }
            if w > 0 {
                entry.recv.stop_pending = false
                prev := fn_cnt
                fn_cnt = self.ctrl.Emit(a, CtrlKind.StopSending(id: id), frames, fn_cnt)
                if fn_cnt > prev {
                    off += w
                }
            }
        }
        if fn_cnt >= frames.Len() {
            break
        }
        if entry.has_recv && entry.recv.CanSendMaxStreamData() {
            match entry.recv.credit.Owed() {
                some(peek) {
                    need := 1 + VarintLen(Cast[u64](id)) + VarintLen(peek)
                    if off + need <= limit {
                        v := entry.recv.credit.TakeOwed().Must()
                        w := WriteMaxStreamData(mut out[off..limit], id, v) else { 0 }
                        if w > 0 {
                            prev := fn_cnt
                            fn_cnt = self.ctrl.Emit(a, CtrlKind.MaxStreamData(id: id, seq: entry.recv.credit.tx_seq), frames, fn_cnt)
                            if fn_cnt > prev {
                                off += w
                            }
                        }
                    }
                }
                none {}
            }
        }
    }

    // 4. STREAMS_BLOCKED.
    if fn_cnt < frames.Len() && self.streams_blocked_bidi_pending {
        lim := self.limits.peer_max_streams_bidi
        w := WriteStreamsBlocked(mut out[off..limit], true, lim) else { 0 }
        if w > 0 {
            self.streams_blocked_bidi_pending = false
            prev := fn_cnt
            fn_cnt = self.ctrl.Emit(a, CtrlKind.StreamsBlocked(bidi: true, limit: lim), frames, fn_cnt)
            if fn_cnt > prev {
                off += w
            }
        }
    }
    if fn_cnt < frames.Len() && self.streams_blocked_uni_pending {
        lim := self.limits.peer_max_streams_uni
        w := WriteStreamsBlocked(mut out[off..limit], false, lim) else { 0 }
        if w > 0 {
            self.streams_blocked_uni_pending = false
            prev := fn_cnt
            fn_cnt = self.ctrl.Emit(a, CtrlKind.StreamsBlocked(bidi: false, limit: lim), frames, fn_cnt)
            if fn_cnt > prev {
                off += w
            }
        }
    }

    // 5. STREAM data (lost retransmissions first, then new data within both flow limits).
    scount := self.active_ids.Len()
    if scount > 0 {
        start := self.rr_cursor % scount
        for step in 0..scount {
            if fn_cnt >= frames.Len() || off >= limit {
                break
            }
            id := self.active_ids[(start + step) % scount]
            entry := self.map.GetMut(&id).Must()
            if !entry.has_send || entry.send.state == SendState.ResetSent || entry.send.state == SendState.ResetRecvd || entry.send.state == SendState.DataRecvd {
                continue
            }
            // Lost data ranges.
            for entry.send.lost_ranges.Len() > 0 && fn_cnt < frames.Len() && off < limit {
                lr := entry.send.lost_ranges[0]
                avail := Cast[int](lr.hi - lr.lo)
                hdr := StreamHeaderLen(id, lr.lo, avail)
                if off + hdr >= limit {
                    break
                }
                room := limit - off - hdr
                take := if avail < room { avail } else { room }
                if take <= 0 {
                    break
                }
                rel := Cast[int](lr.lo - entry.send.base_offset)
                var scratch: [1500]u8 = [0; 1500]
                t := if take < 1500 { take } else { 1500 }
                entry.send.ring.CopyAt(rel, mut scratch[0..t])
                is_fin := entry.send.fin_set && (lr.lo + Cast[u64](t) == entry.send.write_offset)
                w := WriteStream(mut out[off..limit], id, lr.lo, scratch[0..t], is_fin, true) else { 0 }
                if w == 0 {
                    break
                }
                off += w
                frames[fn_cnt] = SentFrame(kind: SentKind.Stream(id: id, offset: lr.lo, len: t, fin: is_fin))
                fn_cnt += 1
                if is_fin {
                    entry.send.fin_lost = false
                }
                if lr.lo + Cast[u64](t) == lr.hi {
                    _ = entry.send.lost_ranges.Remove(0)
                } else {
                    items := entry.send.lost_ranges.ItemsMut()
                    items[0].lo += Cast[u64](t)
                }
            }
            // Lost empty FIN.
            if entry.send.fin_lost && fn_cnt < frames.Len() && off < limit {
                w := WriteStream(mut out[off..limit], id, entry.send.write_offset, "".AsBytes(), true, true) else { 0 }
                if w > 0 {
                    entry.send.fin_lost = false
                    off += w
                    frames[fn_cnt] = SentFrame(kind: SentKind.Stream(id: id, offset: entry.send.write_offset, len: 0, fin: true))
                    fn_cnt += 1
                }
            }
            // New unsent data / FIN.
            if fn_cnt < frames.Len() && off < limit {
                unsent := entry.send.write_offset - entry.send.send_offset
                need_fin := entry.send.fin_set && !entry.send.fin_sent
                if unsent > 0 || need_fin {
                    s_avail := entry.send.TxAvail()
                    c_avail := self.conn_flow.TxAvail()
                    fc_avail := if s_avail < c_avail { s_avail } else { c_avail }
                    if unsent > 0 && fc_avail == 0 {
                        if s_avail == 0 {
                            entry.send.NoteBlocked()
                            if entry.send.blocked_pending {
                                w := WriteStreamDataBlocked(mut out[off..limit], id, entry.send.tx_max_stream_data) else { 0 }
                                if w > 0 {
                                    entry.send.blocked_pending = false
                                    prev := fn_cnt
                                    fn_cnt = self.ctrl.Emit(a, CtrlKind.StreamDataBlocked(id: id, limit: entry.send.tx_max_stream_data), frames, fn_cnt)
                                    if fn_cnt > prev {
                                        off += w
                                    }
                                }
                            }
                        } else {
                            self.conn_flow.NoteBlocked()
                        }
                    } else {
                        can_send := if unsent < fc_avail { unsent } else { fc_avail }
                        hdr := StreamHeaderLen(id, entry.send.send_offset, Cast[int](can_send))
                        if off + hdr <= limit {
                            room := limit - off - hdr
                            var take := if Cast[int](can_send) < room { Cast[int](can_send) } else { room }
                            if take > 1500 {
                                take = 1500
                            }
                            if take > 0 || (need_fin && unsent == 0) {
                                var scratch: [1500]u8 = [0; 1500]
                                if take > 0 {
                                    rel := Cast[int](entry.send.send_offset - entry.send.base_offset)
                                    entry.send.ring.CopyAt(rel, mut scratch[0..take])
                                }
                                is_fin := need_fin && (entry.send.send_offset + Cast[u64](take) == entry.send.write_offset)
                                w := WriteStream(mut out[off..limit], id, entry.send.send_offset, scratch[0..take], is_fin, true) else { 0 }
                                if w > 0 {
                                    send_off := entry.send.send_offset
                                    entry.send.send_offset += Cast[u64](take)
                                    self.conn_flow.tx_sent += Cast[u64](take)
                                    if is_fin {
                                        entry.send.fin_sent = true
                                        entry.send.state = SendState.DataSent
                                    } else if entry.send.state == SendState.Ready {
                                        entry.send.state = SendState.Send
                                    }
                                    off += w
                                    frames[fn_cnt] = SentFrame(kind: SentKind.Stream(id: id, offset: send_off, len: take, fin: is_fin))
                                    fn_cnt += 1
                                }
                            }
                        }
                    }
                }
            }
        }
        self.rr_cursor = (self.rr_cursor + 1) % scount
    }

    // 6. Connection-level DATA_BLOCKED.
    if fn_cnt < frames.Len() && self.conn_flow.blocked_pending {
        lim := self.conn_flow.tx_max_data
        w := WriteDataBlocked(mut out[off..limit], lim) else { 0 }
        if w > 0 {
            self.conn_flow.blocked_pending = false
            prev := fn_cnt
            fn_cnt = self.ctrl.Emit(a, CtrlKind.DataBlocked(limit: lim), frames, fn_cnt)
            if fn_cnt > prev {
                off += w
            }
        }
    }

    return (n: off, frames: fn_cnt)
}

// Processes acknowledgment of a SentFrame previously emitted by Next.
Streams.OnAcked(mut self, a: &Alloc, f: &SentFrame) {
    match f.kind {
        .Stream(id, offset, len, fin) {
            match self.map.GetMut(&id) {
                some(entry) {
                    entry.send.OnStreamAcked(a, offset, len, fin)
                    self.MaybeReap(a, id)
                }
                none {}
            }
        }
        .Control(index: idx) {
            match self.ctrl.Take(a, idx) {
                some(kind) {
                    match kind {
                        .ResetStream(id) {
                            match self.map.GetMut(&id) {
                                some(entry) {
                                    entry.send.OnResetAcked()
                                    self.MaybeReap(a, id)
                                }
                                none {}
                            }
                        }
                        _ {}
                    }
                }
                none {}
            }
        }
        _ {}
    }
}

// Processes loss of a SentFrame previously emitted by Next (RFC 9000 13.3).
Streams.OnLost(mut self, a: &Alloc, f: &SentFrame) {
    match f.kind {
        .Stream(id, offset, len, fin) {
            match self.map.GetMut(&id) {
                some(entry) { entry.send.OnStreamLost(a, offset, len, fin) }
                none {}
            }
        }
        .Control(index: idx) {
            match self.ctrl.Take(a, idx) {
                some(kind) {
                    match kind {
                        .MaxData(seq) { self.conn_flow.rx_credit.OnLost(seq) }
                        .MaxStreamData(id, seq) {
                            match self.map.GetMut(&id) {
                                some(entry) {
                                    if entry.recv.CanSendMaxStreamData() {
                                        entry.recv.credit.OnLost(seq)
                                    }
                                }
                                none {}
                            }
                        }
                        .MaxStreams(bidi, seq) {
                            if bidi {
                                if seq == self.max_streams_bidi_seq {
                                    self.max_streams_bidi_pending = true
                                }
                            } else {
                                if seq == self.max_streams_uni_seq {
                                    self.max_streams_uni_pending = true
                                }
                            }
                        }
                        .ResetStream(id) {
                            match self.map.GetMut(&id) {
                                some(entry) { entry.send.OnResetLost() }
                                none {}
                            }
                        }
                        .StopSending(id) {
                            match self.map.GetMut(&id) {
                                some(entry) { entry.recv.OnStopLost() }
                                none {}
                            }
                        }
                        .DataBlocked(limit) { self.conn_flow.OnBlockedLost(limit) }
                        .StreamDataBlocked(id, limit) {
                            match self.map.GetMut(&id) {
                                some(entry) { entry.send.OnBlockedLost(limit) }
                                none {}
                            }
                        }
                        .StreamsBlocked(bidi, limit) {
                            if bidi {
                                if limit == self.limits.peer_max_streams_bidi && self.next_local_bidi_seq >= limit {
                                    self.streams_blocked_bidi_pending = true
                                }
                            } else {
                                if limit == self.limits.peer_max_streams_uni && self.next_local_uni_seq >= limit {
                                    self.streams_blocked_uni_pending = true
                                }
                            }
                        }
                    }
                }
                none {}
            }
        }
        _ {}
    }
}

// Reports whether Read on stream id will make progress without waiting
// (queued data, EOF, reset, or missing stream). O(1).
Streams.IsReadable(&self, id: i64): bool {
    entry := self.map.Get(&id) else {
        return true
    }
    if !entry.has_recv {
        return true
    }
    if entry.recv.contig.len > 0 {
        return true
    }
    return entry.recv.state == RecvState.DataRecvd || entry.recv.state == RecvState.DataRead ||
        entry.recv.state == RecvState.ResetRecvd || entry.recv.state == RecvState.ResetRead
}

// Reports whether a peer-initiated stream of direction uni is queued for Accept. O(1).
Streams.HasAccept(&self, uni: bool): bool {
    if uni {
        return self.accept_uni.Len() > 0
    }
    return self.accept_bidi.Len() > 0
}

// Reports whether stream id can accept a buffered write or would immediately fail. O(1).
Streams.CanWriteNow(&self, id: i64, max_buf: u64): bool {
    entry := self.map.Get(&id) else {
        return true
    }
    if !entry.has_send {
        return true
    }
    if entry.send.state == SendState.ResetSent || entry.send.state == SendState.ResetRecvd ||
        entry.send.fin_set || entry.send.state == SendState.DataSent || entry.send.state == SendState.DataRecvd {
        return true
    }
    queued := entry.send.write_offset - entry.send.send_offset
    if queued >= max_buf {
        return false
    }
    return entry.send.TxAvail() > 0 && self.conn_flow.TxAvail() > 0 || queued == 0
}

// Reports whether a locally initiated stream of direction uni can be opened now. O(1).
Streams.CanOpen(&self, uni: bool): bool {
    if !uni {
        return self.next_local_bidi_seq < self.limits.peer_max_streams_bidi
    }
    return self.next_local_uni_seq < self.limits.peer_max_streams_uni
}

// Returns remaining locally-openable bidi and uni stream counts. O(1).
Streams.OpenLimits(&self): (bidi: i64, uni: i64) {
    b := if self.limits.peer_max_streams_bidi > self.next_local_bidi_seq {
        Cast[i64](self.limits.peer_max_streams_bidi - self.next_local_bidi_seq)
    } else { 0 }
    u := if self.limits.peer_max_streams_uni > self.next_local_uni_seq {
        Cast[i64](self.limits.peer_max_streams_uni - self.next_local_uni_seq)
    } else { 0 }
    return (bidi: b, uni: u)
}

// Reports whether any stream or flow-control frame is ready to be emitted by Next.
Streams.HasPending(&self): bool {
    if self.conn_flow.rx_credit.Owed().IsSome() || self.conn_flow.blocked_pending {
        return true
    }
    if self.max_streams_bidi_pending || self.max_streams_uni_pending ||
        self.streams_blocked_bidi_pending || self.streams_blocked_uni_pending {
        return true
    }
    c_avail := self.conn_flow.TxAvail()
    for i in 0..self.active_ids.Len() {
        id := self.active_ids[i]
        entry := self.map.Get(&id).Must()
        if entry.has_send && entry.send.reset_pending {
            return true
        }
        if entry.has_recv && entry.recv.stop_pending {
            return true
        }
        if entry.has_recv && entry.recv.CanSendMaxStreamData() && entry.recv.credit.Owed().IsSome() {
            return true
        }
        if entry.has_send && entry.send.state != SendState.ResetSent &&
            entry.send.state != SendState.ResetRecvd && entry.send.state != SendState.DataRecvd {
            if entry.send.lost_ranges.Len() > 0 || entry.send.fin_lost {
                return true
            }
            unsent := entry.send.write_offset - entry.send.send_offset
            need_fin := entry.send.fin_set && !entry.send.fin_sent
            if need_fin && unsent == 0 {
                return true
            }
            if unsent > 0 {
                s_avail := entry.send.TxAvail()
                if s_avail > 0 && c_avail > 0 {
                    return true
                }
                if s_avail == 0 {
                    need_sb := match entry.send.blocked_sent_at {
                        none { true }
                        some(prev) { prev != entry.send.tx_max_stream_data }
                    }
                    if need_sb || entry.send.blocked_pending {
                        return true
                    }
                } else {
                    need_db := match self.conn_flow.blocked_sent_at {
                        none { true }
                        some(prev) { prev != self.conn_flow.tx_max_data }
                    }
                    if need_db || self.conn_flow.blocked_pending {
                        return true
                    }
                }
            }
        }
    }
    return false
}
