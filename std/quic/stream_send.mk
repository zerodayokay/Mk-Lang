// Why: QUIC stream sending maintains the RFC 9000 3.1 state machine, retains
// unacknowledged bytes for identical retransmission, stops transmitting upon
// RESET_STREAM, and enforces stream-level send credit and final size.
//
// What: SendState, ByteRange, ByteRing (circular byte buffer), and StreamSend
// (New, Free, Write, Reset, OnStop, OnMaxStreamData, OnAcked, OnLost, Next).
//
// How: StreamSend keeps unacknowledged bytes in a power-of-two circular ring
// starting at base_offset, tracks disjoint acknowledged and lost byte ranges
// above base_offset, and pops leading acknowledged bytes in O(1).

import std.net

// RFC 9000 3.1 states for the sending part of a stream.
enum SendState {
    Ready
    Send
    DataSent
    DataRecvd
    ResetSent
    ResetRecvd
}

// Half-open byte interval [lo, hi) within a stream or CRYPTO flow.
struct ByteRange {
    lo: u64
    hi: u64
}

// Circular byte ring used by send and contiguous receive buffers.
// Linear: owns a HeapList[u8] backing buffer.
linear struct ByteRing {
    buf: HeapList[u8]
    head: int
    len: int
}

// Creates an empty ByteRing using a. O(1); allocates nothing until first push.
ByteRing.New(a: &Alloc): ByteRing {
    return ByteRing(buf: HeapList[u8].New(a), head: 0, len: 0)
}

// Releases the backing buffer back to a. O(1).
ByteRing.Free(self, a: &Alloc) {
    ByteRing(buf, head: _, len: _) := self
    buf.Free(a)
}

// Discards all buffered bytes while keeping allocated capacity. O(1).
ByteRing.Clear(mut self) {
    self.head = 0
    self.len = 0
}

// Ensures capacity for at least need bytes, linearizing wrapped bytes on grow.
ByteRing.Ensure(mut self, a: &Alloc, need: int): OutOfMemory!void {
    cap := self.buf.Len()
    if need <= cap {
        return
    }
    var new_cap := if cap == 0 { 4096 } else { cap * 2 }
    for new_cap < need {
        new_cap *= 2
    }
    var zeros: [64]u8 = [0; 64]
    for self.buf.Len() < new_cap {
        k := if new_cap - self.buf.Len() < 64 { new_cap - self.buf.Len() } else { 64 }
        try self.buf.Extend(a, zeros[0..k])
    }
    if cap > 0 && self.head + self.len > cap {
        var tmp := HeapList[u8].New(a)
        defer tmp.Free(a)
        first := cap - self.head
        try tmp.Extend(a, self.buf.Items()[self.head..cap])
        try tmp.Extend(a, self.buf.Items()[0..self.len - first])
        (mut self.buf.ItemsMut()[0..self.len]).CopyFrom(tmp.Items())
        self.head = 0
    } else if cap > 0 && self.head > 0 && self.len > 0 {
        var tmp := HeapList[u8].New(a)
        defer tmp.Free(a)
        try tmp.Extend(a, self.buf.Items()[self.head..self.head + self.len])
        (mut self.buf.ItemsMut()[0..self.len]).CopyFrom(tmp.Items())
        self.head = 0
    } else if self.len == 0 {
        self.head = 0
    }
}

// Appends src to the tail of the ring. Amortized O(src.Len()).
ByteRing.Push(mut self, a: &Alloc, src: []u8): OutOfMemory!void {
    if src.Len() == 0 {
        return
    }
    try self.Ensure(a, self.len + src.Len())
    cap := self.buf.Len()
    tail := (self.head + self.len) % cap
    first := cap - tail
    items := self.buf.ItemsMut()
    if src.Len() <= first {
        (mut items[tail..tail + src.Len()]).CopyFrom(src)
    } else {
        (mut items[tail..cap]).CopyFrom(src[0..first])
        (mut items[0..src.Len() - first]).CopyFrom(src[first..])
    }
    self.len += src.Len()
}

// Copies dst.Len() bytes starting at relative offset rel into dst. O(dst.Len()).
ByteRing.CopyAt(&self, rel: int, dst: mut []u8) {
    n := dst.Len()
    if n == 0 {
        return
    }
    Assert(rel >= 0 && rel + n <= self.len)
    cap := self.buf.Len()
    pos := (self.head + rel) % cap
    first := cap - pos
    items := self.buf.Items()
    if n <= first {
        dst.CopyFrom(items[pos..pos + n])
    } else {
        (mut dst[0..first]).CopyFrom(items[pos..cap])
        (mut dst[first..n]).CopyFrom(items[0..n - first])
    }
}

// Pops n bytes from the front of the ring into dst and returns n. O(n).
ByteRing.PopInto(mut self, dst: mut []u8): int {
    n := if dst.Len() < self.len { dst.Len() } else { self.len }
    if n == 0 {
        return 0
    }
    self.CopyAt(0, mut dst[0..n])
    self.DropFront(n)
    return n
}

// Discards n bytes from the front of the ring. O(1).
ByteRing.DropFront(mut self, n: int) {
    k := if n > self.len { self.len } else { n }
    if k <= 0 {
        return
    }
    if k == self.len {
        self.head = 0
        self.len = 0
        return
    }
    self.head = (self.head + k) % self.buf.Len()
    self.len -= k
}

// Merges [lo, hi) into a sorted list of non-overlapping, non-adjacent ByteRanges.
MergeRange(a: &Alloc, list: mut HeapList[ByteRange], lo: u64, hi: u64): OutOfMemory!void {
    if lo >= hi {
        return
    }
    var nlo := lo
    var nhi := hi
    var i := 0
    for i < list.Len() && list[i].hi < nlo {
        i += 1
    }
    var del := 0
    for i + del < list.Len() && list[i + del].lo <= nhi {
        r := list[i + del]
        if r.lo < nlo {
            nlo = r.lo
        }
        if r.hi > nhi {
            nhi = r.hi
        }
        del += 1
    }
    if del == 0 {
        try list.Insert(a, i, ByteRange(lo: nlo, hi: nhi))
        return
    }
    items := list.ItemsMut()
    items[i] = ByteRange(lo: nlo, hi: nhi)
    for _ in 1..del {
        _ = list.Remove(i + 1)
    }
}

// Removes [lo, hi) from a sorted list of disjoint ByteRanges.
SubtractRange(a: &Alloc, list: mut HeapList[ByteRange], lo: u64, hi: u64): OutOfMemory!void {
    if lo >= hi {
        return
    }
    var i := 0
    for i < list.Len() {
        r := list[i]
        if r.hi <= lo {
            i += 1
            continue
        }
        if r.lo >= hi {
            break
        }
        if r.lo < lo && r.hi > hi {
            items := list.ItemsMut()
            items[i].hi = lo
            try list.Insert(a, i + 1, ByteRange(lo: hi, hi: r.hi))
            break
        }
        if r.lo < lo {
            items := list.ItemsMut()
            items[i].hi = lo
            i += 1
        } else if r.hi > hi {
            items := list.ItemsMut()
            items[i].lo = hi
            break
        } else {
            _ = list.Remove(i)
        }
    }
}

// Sending half of one QUIC stream (RFC 9000 3.1). Linear: owns ByteRing and
// two HeapList[ByteRange] lists.
linear struct StreamSend {
    state: SendState
    ring: ByteRing
    base_offset: u64
    send_offset: u64
    write_offset: u64
    fin_set: bool
    fin_sent: bool
    fin_lost: bool
    fin_acked: bool
    acked_ranges: HeapList[ByteRange]
    lost_ranges: HeapList[ByteRange]
    tx_max_stream_data: u64
    blocked_sent_at: ?u64
    blocked_pending: bool
    reset_code: u64
    reset_final_size: u64
    reset_pending: bool
}

// Initializes a StreamSend in Ready state (RFC 9000 3.1).
// O(1); allocates nothing until Write.
StreamSend.New(a: &Alloc, peer_max_data: u64): StreamSend {
    return StreamSend(
        state: SendState.Ready,
        ring: ByteRing.New(a),
        base_offset: 0,
        send_offset: 0,
        write_offset: 0,
        fin_set: false,
        fin_sent: false,
        fin_lost: false,
        fin_acked: false,
        acked_ranges: HeapList[ByteRange].New(a),
        lost_ranges: HeapList[ByteRange].New(a),
        tx_max_stream_data: peer_max_data,
        blocked_sent_at: none,
        blocked_pending: false,
        reset_code: 0,
        reset_final_size: 0,
        reset_pending: false,
    )
}

// Releases all send buffers back to a. O(1).
StreamSend.Free(self, a: &Alloc) {
    StreamSend(
        state: _,
        ring,
        base_offset: _,
        send_offset: _,
        write_offset: _,
        fin_set: _,
        fin_sent: _,
        fin_lost: _,
        fin_acked: _,
        acked_ranges,
        lost_ranges,
        tx_max_stream_data: _,
        blocked_sent_at: _,
        blocked_pending: _,
        reset_code: _,
        reset_final_size: _,
        reset_pending: _,
    ) := self
    ring.Free(a)
    acked_ranges.Free(a)
    lost_ranges.Free(a)
}

// Reports whether the send half has reached a terminal state (DataRecvd or ResetRecvd).
StreamSend.IsTerminal(&self): bool {
    return self.state == SendState.DataRecvd || self.state == SendState.ResetRecvd
}

// Queues data (and optional FIN) for sending. Fails with net.StreamReset after
// reset, Closed if FIN was already set, or TransportError(ErrFinalSizeError)
// past 2^62 - 1 (RFC 9000 4.5). Amortized O(data.Len()).
StreamSend.Write(mut self, a: &Alloc, data: []u8, fin: bool): Error!int {
    if self.state == SendState.ResetSent || self.state == SendState.ResetRecvd {
        fail net.StreamReset(code: self.reset_code)
    }
    if self.fin_set || self.state == SendState.DataSent || self.state == SendState.DataRecvd {
        fail Closed
    }
    dlen := Cast[u64](data.Len())
    if dlen > MaxOffset - self.write_offset {
        fail TransportError(code: ErrFinalSize, frame: FrameStreamBase)
    }
    try self.ring.Push(a, data)
    self.write_offset += dlen
    if fin {
        self.fin_set = true
    }
    return data.Len()
}

// Transitions to ResetSent (if not already terminal/reset), drops queued and
// lost STREAM data, and queues RESET_STREAM with final_size = send_offset
// (RFC 9000 3.3, 13.3). O(1).
StreamSend.Reset(mut self, code: u64) {
    if self.state == SendState.DataRecvd || self.state == SendState.ResetSent || self.state == SendState.ResetRecvd {
        return
    }
    self.state = SendState.ResetSent
    self.reset_code = code
    self.reset_final_size = self.send_offset
    self.reset_pending = true
    self.blocked_pending = false
    self.fin_lost = false
    self.ring.Clear()
    self.lost_ranges.Clear()
    self.acked_ranges.Clear()
}

// Handles peer STOP_SENDING: resets the send half with the peer's code in
// Ready, Send, or DataSent (RFC 9000 3.5). O(1).
StreamSend.OnStop(mut self, code: u64) {
    if self.state == SendState.Ready || self.state == SendState.Send || self.state == SendState.DataSent {
        self.Reset(code)
    }
}

// Raises peer's MAX_STREAM_DATA; non-increasing values are ignored (RFC 9000 4.1).
StreamSend.OnMaxStreamData(mut self, v: u64) {
    if v > self.tx_max_stream_data {
        self.tx_max_stream_data = v
        self.blocked_pending = false
    }
}

// Available stream-level send credit in bytes. O(1).
StreamSend.TxAvail(&self): u64 {
    if self.tx_max_stream_data > self.send_offset {
        return self.tx_max_stream_data - self.send_offset
    }
    return 0
}

// Records STREAM_DATA_BLOCKED owed when unsent data exists and TxAvail == 0.
StreamSend.NoteBlocked(mut self) {
    if self.state == SendState.ResetSent || self.state == SendState.ResetRecvd || self.state == SendState.DataRecvd {
        return
    }
    if self.send_offset >= self.write_offset || self.TxAvail() > 0 {
        return
    }
    need := match self.blocked_sent_at {
        none { true }
        some(prev) { prev != self.tx_max_stream_data }
    }
    if need {
        self.blocked_sent_at = some(self.tx_max_stream_data)
        self.blocked_pending = true
    }
}

// Re-queues STREAM_DATA_BLOCKED on loss only while still blocked at limit
// and not in a reset/terminal state (RFC 9000 3.3, 13.3).
StreamSend.OnBlockedLost(mut self, limit: u64) {
    if self.state == SendState.ResetSent || self.state == SendState.ResetRecvd || self.state == SendState.DataRecvd {
        return
    }
    if self.send_offset < self.write_offset && self.TxAvail() == 0 && limit == self.tx_max_stream_data {
        self.blocked_pending = true
    }
}

// Processes acknowledgment of a STREAM frame [offset, offset + len) + fin.
StreamSend.OnStreamAcked(mut self, a: &Alloc, offset: u64, len: int, fin: bool) {
    if self.state == SendState.ResetSent || self.state == SendState.ResetRecvd || self.state == SendState.DataRecvd {
        return
    }
    end := offset + Cast[u64](len)
    if end > self.base_offset {
        lo := if offset > self.base_offset { offset } else { self.base_offset }
        SubtractRange(a, mut self.lost_ranges, lo, end) else {
            Assert(false)
        }
        MergeRange(a, mut self.acked_ranges, lo, end) else {
            Assert(false)
        }
        if self.acked_ranges.Len() > 0 && self.acked_ranges[0].lo == self.base_offset {
            front := self.acked_ranges.Remove(0)
            advance := Cast[int](front.hi - self.base_offset)
            self.ring.DropFront(advance)
            self.base_offset = front.hi
        }
    }
    if fin {
        self.fin_acked = true
        self.fin_lost = false
    }
    if self.fin_acked && self.base_offset == self.write_offset {
        self.state = SendState.DataRecvd
    }
}

// Processes loss of a STREAM frame [offset, offset + len) + fin.
// Ignores loss once in ResetSent or terminal state (RFC 9000 3.3).
StreamSend.OnStreamLost(mut self, a: &Alloc, offset: u64, len: int, fin: bool) {
    if self.state == SendState.ResetSent || self.state == SendState.ResetRecvd || self.state == SendState.DataRecvd {
        return
    }
    end := offset + Cast[u64](len)
    if end > self.base_offset {
        lo := if offset > self.base_offset { offset } else { self.base_offset }
        MergeRange(a, mut self.lost_ranges, lo, end) else {
            Assert(false)
        }
        for i in 0..self.acked_ranges.Len() {
            ar := self.acked_ranges[i]
            SubtractRange(a, mut self.lost_ranges, ar.lo, ar.hi) else {
                Assert(false)
            }
        }
    }
    if fin && !self.fin_acked {
        self.fin_lost = true
    }
}

// Processes acknowledgment of RESET_STREAM.
StreamSend.OnResetAcked(mut self) {
    if self.state == SendState.ResetSent {
        self.state = SendState.ResetRecvd
        self.reset_pending = false
    }
}

// Processes loss of RESET_STREAM: re-queues with identical code and final size (RFC 9000 13.3).
StreamSend.OnResetLost(mut self) {
    if self.state == SendState.ResetSent {
        self.reset_pending = true
    }
}
