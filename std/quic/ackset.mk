// Why: QUIC acknowledges received packets independently per packet number
// space, suppresses duplicates even past dropped old ranges, and encodes
// ACK frames with the newest ranges first so stale ranges drop cleanly when
// a packet is tight.
//
// What: AckSet (Add, Due, Write, plus space queries and PING-per-RTT pacing)
// and SpaceAck.
//
// How: each of the three spaces keeps a fixed 64-entry sorted array of
// disjoint AckRanges, a duplicate floor raised only when the oldest range is
// evicted, and the largest received packet number. Timestamps are tracked as
// i64 nanoseconds relative to the first instant seen. Zero heap allocation;
// Add is O(1) in order and O(64) out of order.

import std.time

// Maximum disjoint ACK ranges retained per packet number space.
const MaxAckRanges = 64

// Default max_ack_delay advertised in transport parameters (25 ms, RFC 9000 18.2).
const DefaultMaxAckDelayNs: i64 = 25_000_000

// Maps a Space enum to its 0..2 index.
SpaceIndex(space: Space): int {
    return match space {
        .Initial { 0 }
        .Handshake { 1 }
        .App { 2 }
    }
}

// Maps a 0..2 index back to Space.
IndexToSpace(i: int): Space {
    if i == 0 {
        return Space.Initial
    }
    if i == 1 {
        return Space.Handshake
    }
    return Space.App
}

// Received-packet tracking state for one packet number space.
struct SpaceAck {
    ranges: [64]AckRange
    count: int
    floor: i64
    largest: i64
    largest_ns: i64
    ack_eliciting_since_ack: int
    has_pending_ack: bool
    ack_due_ns: ?i64
}

// Initializes an empty SpaceAck.
SpaceAck.New(): SpaceAck {
    return SpaceAck(
        ranges: [AckRange(lo: 0, hi: 0); 64],
        count: 0,
        floor: 0,
        largest: -1,
        largest_ns: 0,
        ack_eliciting_since_ack: 0,
        has_pending_ack: false,
        ack_due_ns: none,
    )
}

// Inserts pn into s.ranges, enforcing the 64-range cap and duplicate floor
// (RFC 9000 12.3, 13.2.3). Returns false if pn is a duplicate or below floor.
SpaceAck.InsertPn(mut self, pn: i64, now_ns: i64): bool {
    if pn < 0 || Cast[u64](pn) > MaxVarint || pn < self.floor {
        return false
    }
    n := self.count
    // Fast path: empty or extending/above the largest range.
    if n == 0 {
        self.ranges[0] = AckRange(lo: pn, hi: pn)
        self.count = 1
        self.largest = pn
        self.largest_ns = now_ns
        return true
    }
    top := self.ranges[n - 1]
    if pn == top.hi + 1 {
        self.ranges[n - 1].hi = pn
        self.largest = pn
        self.largest_ns = now_ns
        return true
    }
    if pn > top.hi + 1 {
        if n == MaxAckRanges {
            self.floor = self.ranges[0].hi + 1
            for i in 0..MaxAckRanges - 1 {
                self.ranges[i] = self.ranges[i + 1]
            }
            self.ranges[MaxAckRanges - 1] = AckRange(lo: pn, hi: pn)
        } else {
            self.ranges[n] = AckRange(lo: pn, hi: pn)
            self.count = n + 1
        }
        self.largest = pn
        self.largest_ns = now_ns
        return true
    }
    // Out-of-order: locate the first range with hi >= pn.
    var pos := 0
    for pos < n && self.ranges[pos].hi < pn {
        pos += 1
    }
    if pn >= self.ranges[pos].lo {
        return false
    }
    merge_prev := pos > 0 && self.ranges[pos - 1].hi + 1 == pn
    merge_next := self.ranges[pos].lo - 1 == pn
    if merge_prev && merge_next {
        self.ranges[pos - 1].hi = self.ranges[pos].hi
        for i in pos..n - 1 {
            self.ranges[i] = self.ranges[i + 1]
        }
        self.count = n - 1
        return true
    }
    if merge_prev {
        self.ranges[pos - 1].hi = pn
        return true
    }
    if merge_next {
        self.ranges[pos].lo = pn
        return true
    }
    // New isolated range at pos.
    if n == MaxAckRanges {
        if pos == 0 {
            if pn + 1 > self.floor {
                self.floor = pn + 1
            }
            return true
        }
        self.floor = self.ranges[0].hi + 1
        for i in 0..pos - 1 {
            self.ranges[i] = self.ranges[i + 1]
        }
        self.ranges[pos - 1] = AckRange(lo: pn, hi: pn)
        return true
    }
    var j := n
    for j > pos {
        self.ranges[j] = self.ranges[j - 1]
        j -= 1
    }
    self.ranges[pos] = AckRange(lo: pn, hi: pn)
    self.count = n + 1
    return true
}

// Per-connection received packet number tracker for all three spaces.
// Plain value; allocates nothing.
struct AckSet {
    spaces: [3]SpaceAck
    epoch: ?time.Instant
    max_ack_delay: time.Duration
    ack_delay_exponent: int
    last_ping_ns: ?i64
}

// Creates a fresh AckSet with RFC 9000 defaults (25 ms max_ack_delay,
// exponent 3). O(1); allocates nothing.
AckSet.New(): AckSet {
    return AckSet(
        spaces: [SpaceAck.New(), SpaceAck.New(), SpaceAck.New()],
        epoch: none,
        max_ack_delay: time.Nanos(DefaultMaxAckDelayNs),
        ack_delay_exponent: Cast[int](DefaultAckDelayExponent),
        last_ping_ns: none,
    )
}

// Converts now to nanoseconds relative to self.epoch, latching epoch on first call.
AckSet.Ns(mut self, now: time.Instant): i64 {
    return match self.epoch {
        some(e) { now.Since(e).Nanos() }
        none {
            self.epoch = some(now)
            0
        }
    }
}

// Converts now to nanoseconds relative to self.epoch without mutating.
AckSet.NsRead(&self, now: time.Instant): i64 {
    return match self.epoch {
        some(e) { now.Since(e).Nanos() }
        none { 0 }
    }
}

// Configures local max_ack_delay and ack_delay_exponent used when encoding
// ACK Delay fields (RFC 9000 18.2). O(1); allocates nothing.
AckSet.SetAckDelayParams(mut self, max_delay: time.Duration, exponent: int) {
    self.max_ack_delay = max_delay
    self.ack_delay_exponent = if exponent < 0 { 0 } else if exponent > 20 { 20 } else { exponent }
}

// Records receipt of an authenticated packet pn in space. Returns false when
// pn is a duplicate or below the duplicate floor (RFC 9000 12.3).
// Arms immediate or delayed ACK timers per RFC 9000 13.2.1-13.2.2.
// O(1) in order, O(64) out of order; allocates nothing.
AckSet.Add(mut self, space: Space, pn: i64, ack_eliciting: bool, now: time.Instant): bool {
    now_ns := self.Ns(now)
    si := SpaceIndex(space)
    prev_largest := self.spaces[si].largest
    if !self.spaces[si].InsertPn(pn, now_ns) {
        return false
    }
    if !ack_eliciting {
        return true
    }
    self.spaces[si].has_pending_ack = true
    self.spaces[si].ack_eliciting_since_ack += 1
    immediate := match space {
        .Initial { true }
        .Handshake { true }
        .App {
            reordered := prev_largest >= 0 && pn < prev_largest
            gapped := prev_largest >= 0 && pn > prev_largest + 1
            reordered || gapped || self.spaces[si].ack_eliciting_since_ack >= 2
        }
    }
    if immediate {
        self.spaces[si].ack_due_ns = some(now_ns)
    } else if self.spaces[si].ack_due_ns.IsNone() {
        self.spaces[si].ack_due_ns = some(now_ns + self.max_ack_delay.Nanos())
    }
    return true
}

// Returns the remaining duration until the earliest scheduled ACK deadline
// across all three spaces (0 when overdue), or none when no ACK timer is
// pending (RFC 9000 13.2.1). O(1); allocates nothing.
AckSet.Due(&self, now: time.Instant): ?time.Duration {
    now_ns := self.NsRead(now)
    var earliest: ?i64 = none
    for i in 0..3 {
        match self.spaces[i].ack_due_ns {
            some(t) {
                earliest = match earliest {
                    none { some(t) }
                    some(e) { if t < e { some(t) } else { some(e) } }
                }
            }
            none {}
        }
    }
    return match earliest {
        none { none }
        some(t) {
            rem := t - now_ns
            some(time.Nanos(if rem < 0 { 0 } else { rem }))
        }
    }
}

// Reports whether an ACK-only packet is due at now in space. O(1); allocates nothing.
AckSet.NeedsAckOnly(&self, space: Space, now: time.Instant): bool {
    now_ns := self.NsRead(now)
    return match self.spaces[SpaceIndex(space)].ack_due_ns {
        none { false }
        some(t) { t <= now_ns }
    }
}

// Reports whether space has an unacknowledged ack-eliciting packet pending. O(1).
AckSet.HasPendingAck(&self, space: Space): bool {
    return self.spaces[SpaceIndex(space)].has_pending_ack
}

// Returns the largest processed packet number in space, or none if no packet
// has been processed yet (RFC 9000 13.2.3). O(1); allocates nothing.
AckSet.Largest(&self, space: Space): ?i64 {
    l := self.spaces[SpaceIndex(space)].largest
    if l < 0 {
        return none
    }
    return some(l)
}

// Returns the current duplicate floor for space. O(1); allocates nothing.
AckSet.Floor(&self, space: Space): i64 {
    return self.spaces[SpaceIndex(space)].floor
}

// Returns the number of disjoint ACK ranges currently kept in space. O(1).
AckSet.RangeCount(&self, space: Space): int {
    return self.spaces[SpaceIndex(space)].count
}

// Clears all ACK state for space (used when Initial or Handshake keys are
// discarded or on Retry). O(1); allocates nothing.
AckSet.ClearSpace(mut self, space: Space) {
    self.spaces[SpaceIndex(space)] = SpaceAck.New()
}

// Reports whether an occasional PING may be added to an otherwise ACK-only
// packet to solicit an ACK from the peer: at most once per RTT (RFC 9000
// 13.2.1, 13.2.4). O(1); allocates nothing.
AckSet.AllowAckPing(mut self, now: time.Instant, rtt: time.Duration): bool {
    now_ns := self.Ns(now)
    match self.last_ping_ns {
        some(prev) {
            if now_ns - prev < rtt.Nanos() {
                return false
            }
        }
        none {}
    }
    self.last_ping_ns = some(now_ns)
    return true
}

// Writes an ACK frame (type 0x02) for space into out and clears that space's
// pending ACK trigger so at most one ACK-only packet answers a trigger
// (RFC 9000 13.2.1, 13.2.3, 13.2.5, 19.3). Includes the largest range and drops
// the oldest ranges first if out is too small. Returns bytes written, or 0 if
// nothing is recorded or out cannot fit the largest range. O(count); allocates nothing.
AckSet.Write(mut self, space: Space, out: mut []u8, now: time.Instant): int {
    now_ns := self.Ns(now)
    si := SpaceIndex(space)
    n := self.spaces[si].count
    if n == 0 {
        return 0
    }
    top := self.spaces[si].ranges[n - 1]
    largest := Cast[u64](top.hi)
    first_range := Cast[u64](top.hi - top.lo)
    var delay_val: u64 = 0
    if now_ns > self.spaces[si].largest_ns {
        us := Cast[u64]((now_ns - self.spaces[si].largest_ns) / 1000)
        delay_val = us >> Cast[u64](self.ack_delay_exponent)
    }
    hdr_len := 1 + VarintLen(largest) + VarintLen(delay_val) + 1 + VarintLen(first_range)
    if out.Len() < hdr_len {
        return 0
    }
    var extra := 0
    var used := hdr_len
    var prev_lo := top.lo
    var idx := n - 2
    for idx >= 0 {
        r := self.spaces[si].ranges[idx]
        gap := Cast[u64](prev_lo - r.hi - 2)
        rlen := Cast[u64](r.hi - r.lo)
        need := VarintLen(gap) + VarintLen(rlen)
        if used + need > out.Len() {
            break
        }
        used += need
        extra += 1
        prev_lo = r.lo
        idx -= 1
    }
    var off := 0
    off += WriteVarint(mut out[off..], 0x02)
    off += WriteVarint(mut out[off..], largest)
    off += WriteVarint(mut out[off..], delay_val)
    off += WriteVarint(mut out[off..], Cast[u64](extra))
    off += WriteVarint(mut out[off..], first_range)
    prev_lo = top.lo
    idx = n - 2
    for _ in 0..extra {
        r := self.spaces[si].ranges[idx]
        gap := Cast[u64](prev_lo - r.hi - 2)
        rlen := Cast[u64](r.hi - r.lo)
        off += WriteVarint(mut out[off..], gap)
        off += WriteVarint(mut out[off..], rlen)
        prev_lo = r.lo
        idx -= 1
    }
    self.spaces[si].ack_eliciting_since_ack = 0
    self.spaces[si].has_pending_ack = false
    self.spaces[si].ack_due_ns = none
    return off
}
