// Why: QUIC stream receiving maintains the RFC 9000 3.2 state machine,
// reassembles out-of-order stream data in bounded memory, enforces final-size
// and stream/connection flow-control rules, and rejects packets that exceed the
// 32-gap cap unacknowledged.
//
// What: RecvState, OooSeg, OooBuf, and StreamRecv (New, Free, OnStream,
// OnReset, Stop, Read, CanSendMaxStreamData, OnStopLost, IsFinished).
//
// How: in-order bytes append directly to a ByteRing for O(1) streaming;
// out-of-order bytes sit in OooBuf (one backing HeapList[u8] and a fixed
// 32-slot OooSeg table), validated for gap count before mutating stream or
// connection flow state.

import std.net

// RFC 9000 3.2 states for the receiving part of a stream.
enum RecvState {
    Recv
    SizeKnown
    DataRecvd
    DataRead
    ResetRecvd
    ResetRead
}

// Metadata for one contiguous out-of-order slice stored inside OooBuf.buf.
struct OooSeg {
    offset: u64
    start: int
    len: int
}

// Reassembly buffer for at most MaxRecvGaps (32) out-of-order segments.
// Linear: owns a single HeapList[u8] backing buffer.
linear struct OooBuf {
    segs: [32]OooSeg
    count: int
    buf: HeapList[u8]
}

OooBuf.New(a: &Alloc): OooBuf {
    return OooBuf(
        segs: [OooSeg(offset: 0, start: 0, len: 0); 32],
        count: 0,
        buf: HeapList[u8].New(a),
    )
}

OooBuf.Free(self, a: &Alloc) {
    OooBuf(segs: _, count: _, buf) := self
    buf.Free(a)
}

OooBuf.Clear(mut self) {
    self.count = 0
    self.buf.Clear()
}

// Counts how many disjoint segments OooBuf would contain after inserting
// [lo, hi) without mutating self. O(count) <= O(32).
OooBuf.ProjectedGaps(&self, lo: u64, hi: u64): int {
    var before := 0
    var after := 0
    for i in 0..self.count {
        s := self.segs[i]
        send := s.offset + Cast[u64](s.len)
        if send < lo {
            before += 1
        } else if s.offset > hi {
            after += 1
        }
    }
    return before + 1 + after
}

// Inserts [ins_lo, ins_lo + payload.Len()) into self, merging any overlapping
// or adjacent segments. Caller must check ProjectedGaps <= MaxRecvGaps first.
OooBuf.Insert(mut self, a: &Alloc, ins_lo: u64, payload: []u8): OutOfMemory!void {
    if payload.Len() == 0 {
        return
    }
    ins_hi := ins_lo + Cast[u64](payload.Len())
    var first := 0
    for first < self.count {
        s := self.segs[first]
        send := s.offset + Cast[u64](s.len)
        if send >= ins_lo {
            break
        }
        first += 1
    }
    var last := first
    for last < self.count && self.segs[last].offset <= ins_hi {
        last += 1
    }
    if first == last {
        Assert(self.count < MaxRecvGaps)
        start := self.buf.Len()
        try self.buf.Extend(a, payload)
        var j := self.count
        for j > first {
            self.segs[j] = self.segs[j - 1]
            j -= 1
        }
        self.segs[first] = OooSeg(offset: ins_lo, start: start, len: payload.Len())
        self.count += 1
        return
    }
    // Merge [first..last) with [ins_lo, ins_hi) into a new slice at buf's tail.
    first_off := self.segs[first].offset
    merged_lo := if first_off < ins_lo { first_off } else { ins_lo }
    last_s := self.segs[last - 1]
    last_end := last_s.offset + Cast[u64](last_s.len)
    merged_hi := if last_end > ins_hi { last_end } else { ins_hi }
    total_len := Cast[int](merged_hi - merged_lo)

    new_start := self.buf.Len()
    var zeros: [64]u8 = [0; 64]
    for self.buf.Len() < new_start + total_len {
        rem := new_start + total_len - self.buf.Len()
        k := if rem < 64 { rem } else { 64 }
        try self.buf.Extend(a, zeros[0..k])
    }
    items := self.buf.ItemsMut()
    for idx in first..last {
        s := self.segs[idx]
        dst_at := new_start + Cast[int](s.offset - merged_lo)
        for p in 0..s.len {
            items[dst_at + p] = items[s.start + p]
        }
    }
    pat := new_start + Cast[int](ins_lo - merged_lo)
    (mut items[pat..pat + payload.Len()]).CopyFrom(payload)

    self.segs[first] = OooSeg(offset: merged_lo, start: new_start, len: total_len)
    removed := last - first - 1
    if removed > 0 {
        for idx in first + 1..self.count - removed {
            self.segs[idx] = self.segs[idx + removed]
        }
        self.count -= removed
    }
}

// Drains any leading segments in OooBuf that now touch or overlap contig_offset
// into contig, advancing contig_offset. Compacts buf when empty.
OooBuf.DrainContig(mut self, a: &Alloc, contig: mut ByteRing, contig_offset: u64): OutOfMemory!u64 {
    var cur := contig_offset
    var i := 0
    for i < self.count && self.segs[i].offset <= cur {
        s := self.segs[i]
        seg_end := s.offset + Cast[u64](s.len)
        if seg_end > cur {
            skip := Cast[int](cur - s.offset)
            slice := self.buf.Items()[s.start + skip..s.start + s.len]
            try contig.Push(a, slice)
            cur = seg_end
        }
        i += 1
    }
    if i > 0 {
        rem := self.count - i
        for j in 0..rem {
            self.segs[j] = self.segs[i + j]
        }
        self.count = rem
        if self.count == 0 {
            self.buf.Clear()
        }
    }
    return cur
}

// Receiving half of one QUIC stream (RFC 9000 3.2). Linear: owns ByteRing and
// OooBuf.
linear struct StreamRecv {
    state: RecvState
    contig: ByteRing
    ooo: OooBuf
    read_offset: u64
    contig_offset: u64
    largest_rcvd: u64
    final_size: ?u64
    credit: Credit
    auto_credit: bool
    reset_code: u64
    stop_sent: bool
    stop_pending: bool
    stop_code: u64
}

// Initializes a StreamRecv in Recv state with initial_window advertised.
// auto_credit is false for server-accepted bidirectional streams (admitted via
// Credit) and true for client and unidirectional streams. O(1).
StreamRecv.New(a: &Alloc, initial_window: u64, auto_credit: bool): StreamRecv {
    return StreamRecv(
        state: RecvState.Recv,
        contig: ByteRing.New(a),
        ooo: OooBuf.New(a),
        read_offset: 0,
        contig_offset: 0,
        largest_rcvd: 0,
        final_size: none,
        credit: Credit.New(initial_window),
        auto_credit: auto_credit,
        reset_code: 0,
        stop_sent: false,
        stop_pending: false,
        stop_code: 0,
    )
}

// Releases all reassembly buffers back to a. O(1).
StreamRecv.Free(self, a: &Alloc) {
    StreamRecv(
        state: _,
        contig,
        ooo,
        read_offset: _,
        contig_offset: _,
        largest_rcvd: _,
        final_size: _,
        credit: _,
        auto_credit: _,
        reset_code: _,
        stop_sent: _,
        stop_pending: _,
        stop_code: _,
    ) := self
    contig.Free(a)
    ooo.Free(a)
}

// Reports whether no more bytes can arrive. A released stream need not be read.
StreamRecv.IsFinished(&self): bool {
    return self.state == RecvState.DataRecvd || self.state == RecvState.DataRead ||
        self.state == RecvState.ResetRecvd || self.state == RecvState.ResetRead
}

// Processes a received STREAM frame (RFC 9000 3.2, 4.1, 4.5).
// Checks final-size consistency, stream and connection flow control, and the
// 32-gap cap before mutating stream or connection flow state.
StreamRecv.OnStream(mut self, a: &Alloc, conn_flow: mut ConnFlow, offset: u64, data: []u8, fin: bool): Error!void {
    dlen := Cast[u64](data.Len())
    if dlen > MaxOffset - offset {
        fail TransportError(code: ErrFrameEncoding, frame: FrameStreamBase)
    }
    end := offset + dlen
    match self.final_size {
        some(fs) {
            if fin && end != fs {
                fail TransportError(code: ErrFinalSize, frame: FrameStreamBase)
            }
            if end > fs {
                fail TransportError(code: ErrFinalSize, frame: FrameStreamBase)
            }
        }
        none {
            if fin && end < self.largest_rcvd {
                fail TransportError(code: ErrFinalSize, frame: FrameStreamBase)
            }
        }
    }
    if end > self.credit.max_sent {
        fail TransportError(code: ErrFlowControl, frame: FrameStreamBase)
    }
    new_bytes := if end > self.largest_rcvd { end - self.largest_rcvd } else { 0 }
    if new_bytes > conn_flow.rx_credit.max_sent - conn_flow.rx_total {
        fail TransportError(code: ErrFlowControl, frame: FrameStreamBase)
    }

    discard := self.stop_sent || self.state == RecvState.DataRecvd || self.state == RecvState.DataRead || self.state == RecvState.ResetRecvd || self.state == RecvState.ResetRead
    if discard {
        try conn_flow.AccountRecv(new_bytes)
        if end > self.largest_rcvd {
            self.largest_rcvd = end
        }
        if fin && self.final_size.IsNone() {
            self.final_size = some(end)
        }
        if self.stop_sent && (self.state == RecvState.Recv || self.state == RecvState.SizeKnown) {
            if fin {
                self.state = RecvState.SizeKnown
            }
            match self.final_size {
                some(fs) {
                    if self.largest_rcvd == fs {
                        self.state = RecvState.DataRecvd
                        self.stop_pending = false
                    }
                }
                none {}
            }
        }
        return
    }

    ins_lo := if offset > self.contig_offset { offset } else { self.contig_offset }
    if ins_lo > self.contig_offset && ins_lo < end {
        if self.ooo.ProjectedGaps(ins_lo, end) > MaxRecvGaps {
            fail GapLimit
        }
        slice := data[Cast[int](ins_lo - offset)..]
        try self.ooo.Insert(a, ins_lo, slice)
    } else if ins_lo < end {
        slice := data[Cast[int](ins_lo - offset)..]
        try self.contig.Push(a, slice)
        self.contig_offset = end
        self.contig_offset = try self.ooo.DrainContig(a, mut self.contig, self.contig_offset)
    }

    try conn_flow.AccountRecv(new_bytes)
    if end > self.largest_rcvd {
        self.largest_rcvd = end
    }
    if fin {
        self.final_size = some(end)
    }
    match self.final_size {
        some(fs) {
            if self.contig_offset == fs {
                self.state = RecvState.DataRecvd
                self.stop_pending = false
            } else if self.state == RecvState.Recv {
                self.state = RecvState.SizeKnown
            }
        }
        none {}
    }
}

// Processes a received RESET_STREAM frame (RFC 9000 3.2, 4.5).
// Checks final size and connection flow control; suppresses transition when
// already in DataRecvd or DataRead so buffered complete data stays readable.
StreamRecv.OnReset(mut self, conn_flow: mut ConnFlow, code: u64, final_size: u64): Error!void {
    if final_size > MaxOffset {
        fail TransportError(code: ErrFrameEncoding, frame: FrameResetStream)
    }
    if final_size < self.largest_rcvd {
        fail TransportError(code: ErrFinalSize, frame: FrameResetStream)
    }
    match self.final_size {
        some(fs) {
            if final_size != fs {
                fail TransportError(code: ErrFinalSize, frame: FrameResetStream)
            }
        }
        none {}
    }
    if final_size > self.credit.max_sent {
        fail TransportError(code: ErrFlowControl, frame: FrameResetStream)
    }
    delta := final_size - self.largest_rcvd
    try conn_flow.AccountRecv(delta)
    self.largest_rcvd = final_size
    if self.final_size.IsNone() {
        self.final_size = some(final_size)
    }
    if self.state == RecvState.DataRecvd || self.state == RecvState.DataRead {
        return
    }
    if self.state == RecvState.ResetRecvd || self.state == RecvState.ResetRead {
        return
    }
    self.state = RecvState.ResetRecvd
    self.reset_code = code
    self.stop_pending = false
    self.contig.Clear()
    self.ooo.Clear()
}

// Aborts reading on this stream, queuing STOP_SENDING while in Recv or
// SizeKnown (RFC 9000 3.5). O(1).
StreamRecv.Stop(mut self, code: u64) {
    if self.state != RecvState.Recv && self.state != RecvState.SizeKnown {
        return
    }
    if !self.stop_sent {
        self.stop_sent = true
        self.stop_code = code
        self.stop_pending = true
    }
    self.contig.Clear()
    self.ooo.Clear()
}

// Reads contiguous buffered bytes into buf. Returns some(n > 0) when bytes
// are read, some(0) at clean EOF, none when empty and waiting for more data,
// or fails with net.StreamReset when reset by the peer. O(n).
StreamRecv.Read(mut self, buf: mut []u8): Error!?int {
    if self.state == RecvState.ResetRecvd {
        self.state = RecvState.ResetRead
        fail net.StreamReset(code: self.reset_code)
    }
    if self.state == RecvState.ResetRead {
        fail net.StreamReset(code: self.reset_code)
    }
    if self.contig.len > 0 {
        n := self.contig.PopInto(buf)
        self.read_offset += Cast[u64](n)
        if self.auto_credit && self.state == RecvState.Recv {
            self.credit.Consumed(Cast[u64](n))
        }
        if self.state == RecvState.DataRecvd && self.contig.len == 0 {
            self.state = RecvState.DataRead
        }
        return some(n)
    }
    if self.state == RecvState.DataRecvd {
        self.state = RecvState.DataRead
        return some(0)
    }
    if self.state == RecvState.DataRead {
        return some(0)
    }
    return none
}

// Returns whether MAX_STREAM_DATA may still be sent (only in Recv state,
// RFC 9000 13.3).
StreamRecv.CanSendMaxStreamData(&self): bool {
    return self.state == RecvState.Recv && !self.stop_sent
}

// Re-queues STOP_SENDING on loss only while in Recv or SizeKnown (RFC 9000 13.3).
StreamRecv.OnStopLost(mut self) {
    if self.state == RecvState.Recv || self.state == RecvState.SizeKnown {
        self.stop_pending = true
    }
}
