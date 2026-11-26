// Why: loss detection and congestion control track sent packets per packet
// number space without allocating per packet on a warm connection, and reject
// acknowledgments of unsent or skipped packet numbers.
//
// What: SentPkt, SentSpace (New, Free, Clear, AllocPn, RecordSkip, Push,
// ValidateRanges, AckRange, TrimHead), and SentStatus constants.
//
// How: each space keeps two power-of-two circular ring buffers (SentPkt and
// SentFrame) that grow only when in-flight depth exceeds capacity and advance
// their heads in lockstep as leading packets are acknowledged or declared lost.
// Range lookups use binary search over monotonic packet numbers.

// Per-packet status in a SentSpace ring.
const SentActive: u8 = 0
const SentAcked: u8 = 1
const SentLost: u8 = 2
const SentAckedPending: u8 = 3

// Maximum skipped packet numbers remembered per space for optimistic-ACK
// detection (RFC 9000 21.4).
const MaxSkippedPns = 16

// Metadata for one transmitted packet in a packet number space (RFC 9002 A.1).
struct SentPkt {
    pn: i64
    sent_ns: i64
    bytes: int
    ack_eliciting: bool
    in_flight: bool
    status: u8
    frame_start: int
    frame_count: int
}

// Default blank SentPkt used to pre-populate power-of-two ring slots.
ZeroSentPkt(): SentPkt {
    return SentPkt(
        pn: 0,
        sent_ns: 0,
        bytes: 0,
        ack_eliciting: false,
        in_flight: false,
        status: SentAcked,
        frame_start: 0,
        frame_count: 0,
    )
}

// Default blank SentFrame used to pre-populate frame ring slots.
ZeroSentFrame(): SentFrame {
    return SentFrame(kind: SentKind.Control(index: 0))
}

// Sent-packet history for one packet number space. Linear: owns two HeapList
// ring buffers.
linear struct SentSpace {
    pkts: HeapList[SentPkt]
    pkt_head: int
    pkt_len: int
    frames: HeapList[SentFrame]
    frm_head: int
    frm_len: int
    next_pn: i64
    last_sent_pn: i64
    largest_acked: i64
    last_ae_sent_ns: ?i64
    loss_time_ns: ?i64
    ack_eliciting_in_flight: int
    bytes_in_flight: int
    skipped: [16]i64
    skipped_head: int
    skipped_len: int
}

// Creates an empty SentSpace using a. O(1); allocates nothing until first send.
SentSpace.New(a: &Alloc): SentSpace {
    return SentSpace(
        pkts: HeapList[SentPkt].New(a),
        pkt_head: 0,
        pkt_len: 0,
        frames: HeapList[SentFrame].New(a),
        frm_head: 0,
        frm_len: 0,
        next_pn: 0,
        last_sent_pn: -1,
        largest_acked: -1,
        last_ae_sent_ns: none,
        loss_time_ns: none,
        ack_eliciting_in_flight: 0,
        bytes_in_flight: 0,
        skipped: [-1; 16],
        skipped_head: 0,
        skipped_len: 0,
    )
}

// Releases the space's ring buffers back to a. O(1).
SentSpace.Free(self, a: &Alloc) {
    SentSpace(
        pkts,
        pkt_head: _,
        pkt_len: _,
        frames,
        frm_head: _,
        frm_len: _,
        next_pn: _,
        last_sent_pn: _,
        largest_acked: _,
        last_ae_sent_ns: _,
        loss_time_ns: _,
        ack_eliciting_in_flight: _,
        bytes_in_flight: _,
        skipped: _,
        skipped_head: _,
        skipped_len: _,
    ) := self
    pkts.Free(a)
    frames.Free(a)
}

// Clears all in-flight records in this space (on key discard or Retry).
// Preserves next_pn unless reset_pn is true (Retry resets everything, RFC 9002 6.3).
SentSpace.Clear(mut self, reset_pn: bool) {
    self.pkt_head = 0
    self.pkt_len = 0
    self.frm_head = 0
    self.frm_len = 0
    self.last_ae_sent_ns = none
    self.loss_time_ns = none
    self.ack_eliciting_in_flight = 0
    self.bytes_in_flight = 0
    if reset_pn {
        self.next_pn = 0
        self.last_sent_pn = -1
        self.largest_acked = -1
        self.skipped_head = 0
        self.skipped_len = 0
    }
}

// Returns and advances the next packet number to send (RFC 9000 12.3). O(1).
SentSpace.AllocPn(mut self): i64 {
    pn := self.next_pn
    self.next_pn += 1
    return pn
}

// Records pn as an intentionally skipped packet number (for optimistic ACK
// detection, RFC 9000 21.4) and advances next_pn past it if needed. O(1).
SentSpace.RecordSkip(mut self, pn: i64) {
    if pn < 0 {
        return
    }
    if self.skipped_len < MaxSkippedPns {
        pos := (self.skipped_head + self.skipped_len) % MaxSkippedPns
        self.skipped[pos] = pn
        self.skipped_len += 1
    } else {
        self.skipped[self.skipped_head] = pn
        self.skipped_head = (self.skipped_head + 1) % MaxSkippedPns
    }
    if pn >= self.next_pn {
        self.next_pn = pn + 1
    }
}

// Ensures pkts ring has room for at least need elements.
SentSpace.EnsurePkts(mut self, a: &Alloc, need: int): OutOfMemory!void {
    cap := self.pkts.Len()
    if need <= cap {
        return
    }
    var new_cap := if cap == 0 { 16 } else { cap * 2 }
    for new_cap < need {
        new_cap *= 2
    }
    for self.pkts.Len() < new_cap {
        try self.pkts.Push(a, ZeroSentPkt())
    }
    if cap > 0 && self.pkt_head + self.pkt_len > cap {
        var tmp := HeapList[SentPkt].New(a)
        defer tmp.Free(a)
        for k in 0..self.pkt_len {
            try tmp.Push(a, self.pkts[(self.pkt_head + k) % cap])
        }
        items := self.pkts.ItemsMut()
        for k in 0..self.pkt_len {
            items[k] = tmp[k]
        }
        self.pkt_head = 0
    } else if cap > 0 && self.pkt_head > 0 && self.pkt_len > 0 {
        items := self.pkts.ItemsMut()
        for k in 0..self.pkt_len {
            items[k] = items[self.pkt_head + k]
        }
        self.pkt_head = 0
    } else if self.pkt_len == 0 {
        self.pkt_head = 0
    }
}

// Ensures frames ring has room for at least need elements.
SentSpace.EnsureFrames(mut self, a: &Alloc, need: int): OutOfMemory!void {
    cap := self.frames.Len()
    if need <= cap {
        return
    }
    var new_cap := if cap == 0 { 32 } else { cap * 2 }
    for new_cap < need {
        new_cap *= 2
    }
    for self.frames.Len() < new_cap {
        try self.frames.Push(a, ZeroSentFrame())
    }
    if cap > 0 && self.frm_head + self.frm_len > cap {
        var tmp := HeapList[SentFrame].New(a)
        defer tmp.Free(a)
        for k in 0..self.frm_len {
            try tmp.Push(a, self.frames[(self.frm_head + k) % cap])
        }
        items := self.frames.ItemsMut()
        for k in 0..self.frm_len {
            items[k] = tmp[k]
        }
        self.frm_head = 0
    } else if cap > 0 && self.frm_head > 0 && self.frm_len > 0 {
        items := self.frames.ItemsMut()
        for k in 0..self.frm_len {
            items[k] = items[self.frm_head + k]
        }
        self.frm_head = 0
    } else if self.frm_len == 0 {
        self.frm_head = 0
    }
}

// Appends a sent packet record and its frames. Enforces strictly increasing
// packet numbers (RFC 9000 12.3). Amortized O(fs.Len()).
SentSpace.Push(mut self, a: &Alloc, pn: i64, sent_ns: i64, bytes: int, ack_eliciting: bool, in_flight: bool, fs: []SentFrame): OutOfMemory!void {
    Assert(pn > self.last_sent_pn)
    try self.EnsurePkts(a, self.pkt_len + 1)
    try self.EnsureFrames(a, self.frm_len + fs.Len())
    fcap := self.frames.Len()
    fstart := (self.frm_head + self.frm_len) % fcap
    if fs.Len() > 0 {
        fitems := self.frames.ItemsMut()
        for i in 0..fs.Len() {
            fitems[(fstart + i) % fcap] = fs[i]
        }
        self.frm_len += fs.Len()
    }
    pcap := self.pkts.Len()
    ppos := (self.pkt_head + self.pkt_len) % pcap
    pitems := self.pkts.ItemsMut()
    pitems[ppos] = SentPkt(
        pn: pn,
        sent_ns: sent_ns,
        bytes: bytes,
        ack_eliciting: ack_eliciting,
        in_flight: in_flight,
        status: SentActive,
        frame_start: fstart,
        frame_count: fs.Len(),
    )
    self.pkt_len += 1
    self.last_sent_pn = pn
    if pn >= self.next_pn {
        self.next_pn = pn + 1
    }
    if in_flight {
        self.bytes_in_flight += bytes
        if ack_eliciting {
            self.ack_eliciting_in_flight += 1
            self.last_ae_sent_ns = some(sent_ns)
        }
    }
}

// Advances the ring heads past leading packets that are no longer active or
// pending CC notification. O(1) amortized.
SentSpace.TrimHead(mut self) {
    if self.pkt_len == 0 {
        return
    }
    pcap := self.pkts.Len()
    for self.pkt_len > 0 {
        head := self.pkts[self.pkt_head]
        if head.status == SentActive || head.status == SentAckedPending {
            break
        }
        self.pkt_head = (self.pkt_head + 1) % pcap
        self.pkt_len -= 1
        if head.frame_count > 0 && self.frames.Len() > 0 {
            fcap := self.frames.Len()
            self.frm_head = (self.frm_head + head.frame_count) % fcap
            self.frm_len -= head.frame_count
        }
    }
}

// Checks whether every ACK range in ranges names only packet numbers actually
// sent (no negative, inverted, >= next_pn, or skipped PN; RFC 9000 13.1, 21.4).
// Checks all ranges even past the 256-range processing cap. O(ranges.Len()).
SentSpace.ValidateRanges(&self, ranges: []AckRange): bool {
    for r in ranges {
        if r.lo < 0 || r.hi < r.lo || r.hi >= self.next_pn {
            return false
        }
        for k in 0..self.skipped_len {
            sp := self.skipped[(self.skipped_head + k) % MaxSkippedPns]
            if sp >= r.lo && sp <= r.hi {
                return false
            }
        }
    }
    return true
}

// Binary-searches pkt_len entries for the smallest relative index with pn >= target.
SentSpace.FindFirstGe(&self, target: i64): int {
    if self.pkt_len == 0 {
        return 0
    }
    pcap := self.pkts.Len()
    var lo := 0
    var hi := self.pkt_len
    for lo < hi {
        mid := lo + (hi - lo) / 2
        pn := self.pkts[(self.pkt_head + mid) % pcap].pn
        if pn < target {
            lo = mid + 1
        } else {
            hi = mid
        }
    }
    return lo
}

// Copies frames of packet pkt into dst starting at dst_n, up to dst.Len().
SentSpace.CopyFrames(&self, pkt: &SentPkt, dst: mut []SentFrame, dst_n: int): int {
    if pkt.frame_count == 0 || dst_n >= dst.Len() {
        return dst_n
    }
    fcap := self.frames.Len()
    var out := dst_n
    for i in 0..pkt.frame_count {
        if out >= dst.Len() {
            break
        }
        dst[out] = self.frames[(pkt.frame_start + i) % fcap]
        out += 1
    }
    return out
}

// Marks active packets in [lo..hi] as SentAckedPending, updating space
// counters and copying acknowledged frames into acked_out starting at acked_n.
// Returns updated acked_n, newly acked packet count, largest newly acked
// pn/ns, and whether any ack-eliciting packet was newly acked.
// O(log pkt_len + newly_acked_in_range).
SentSpace.AckRange(
    mut self,
    lo: i64,
    hi: i64,
    acked_out: mut []SentFrame,
    acked_n_in: int,
): (acked_n: int, acked_pkts: int, max_pn: i64, max_ns: i64, has_ae: bool) {
    var acked_n := acked_n_in
    var acked_pkts := 0
    var max_pn: i64 = -1
    var max_ns: i64 = 0
    var has_ae := false
    if self.pkt_len == 0 {
        return (acked_n, acked_pkts, max_pn, max_ns, has_ae)
    }
    pcap := self.pkts.Len()
    var k := self.FindFirstGe(lo)
    for k < self.pkt_len {
        idx := (self.pkt_head + k) % pcap
        pkt := self.pkts[idx]
        if pkt.pn > hi {
            break
        }
        if pkt.status == SentActive {
            items := self.pkts.ItemsMut()
            items[idx].status = SentAckedPending
            if pkt.in_flight {
                self.bytes_in_flight -= pkt.bytes
                if pkt.ack_eliciting {
                    self.ack_eliciting_in_flight -= 1
                    has_ae = true
                }
            } else if pkt.ack_eliciting {
                has_ae = true
            }
            acked_n = self.CopyFrames(&pkt, acked_out, acked_n)
            if pkt.pn > max_pn {
                max_pn = pkt.pn
                max_ns = pkt.sent_ns
            }
            acked_pkts += 1
        }
        k += 1
    }
    return (acked_n, acked_pkts, max_pn, max_ns, has_ae)
}
