// Why: QUIC loss detection, RTT estimation, PTO backoff, anti-deadlock probes,
// and persistent congestion must follow RFC 9002 Appendix A and B across all
// three packet number spaces with a single timer feeding the connection.
//
// What: Recovery (New, Free, OnSent, OnProbeSent, OnAck, OnTimeout, Discard,
// Timer, CanSend, Pto, plus role/amplification/Retry/pacing helpers).
//
// How: composes three SentSpace rings and one Congestion controller, pins the
// A.7 RTT update order (rttvar before smoothed_rtt), tracks a 128-entry ring
// of recent acknowledged send times for cross-space persistent congestion, and
// expresses all instants relative to a latched epoch so arbitrary simulated or
// monotonic clocks work uniformly.

import std.time

// Packet threshold for reordering loss detection (RFC 9002 A.2).
const PacketThreshold: i64 = 3

// Local timer granularity: 1 ms (RFC 9002 A.2).
const GranularityNs: i64 = 1_000_000

// Initial RTT before any sample is observed: 333 ms (RFC 9002 A.2).
const InitialRttNs: i64 = 333_000_000

// Recent acknowledged packet send times tracked for cross-space persistent
// congestion checks (RFC 9002 7.6.2).
const AckedTimeRingCap = 128

// Returns |x| for i64.
AbsI64(x: i64): i64 {
    return if x < 0 { -x } else { x }
}

// Loss detection, RTT estimator, and congestion controller for one connection
// (RFC 9002 Appendix A, B). Linear: owns three SentSpace ring buffers.
linear struct Recovery {
    spaces: [3]SentSpace
    cc: Congestion
    epoch: ?time.Instant
    latest_rtt: i64
    smoothed_rtt: i64
    rttvar: i64
    min_rtt: i64
    has_rtt_sample: bool
    first_rtt_sample_ns: i64
    peer_max_ack_delay: i64
    pto_count: int
    is_server: bool
    has_handshake_keys: bool
    handshake_confirmed: bool
    received_handshake_ack: bool
    server_amp_blocked: bool
    timer_ns: ?i64
    last_now_ns: i64
    acked_times_ns: [128]i64
    acked_times_head: int
    acked_times_len: int
    last_acked_pkts: int
    last_lost_pkts: int
    pto_space: Space
    anti_deadlock: bool
}

// Creates a new Recovery state using a (RFC 9002 A.4, B.3).
// O(1); allocates nothing until packets are sent.
Recovery.New(a: &Alloc): OutOfMemory!Recovery {
    return Recovery(
        spaces: [SentSpace.New(a), SentSpace.New(a), SentSpace.New(a)],
        cc: Congestion.New(),
        epoch: none,
        latest_rtt: 0,
        smoothed_rtt: InitialRttNs,
        rttvar: InitialRttNs / 2,
        min_rtt: 0,
        has_rtt_sample: false,
        first_rtt_sample_ns: 0,
        peer_max_ack_delay: DefaultMaxAckDelayNs,
        pto_count: 0,
        is_server: false,
        has_handshake_keys: false,
        handshake_confirmed: false,
        received_handshake_ack: false,
        server_amp_blocked: false,
        timer_ns: none,
        last_now_ns: 0,
        acked_times_ns: [0; 128],
        acked_times_head: 0,
        acked_times_len: 0,
        last_acked_pkts: 0,
        last_lost_pkts: 0,
        pto_space: Space.Initial,
        anti_deadlock: false,
    )
}

// Releases all sent-packet rings back to a. O(1).
Recovery.Free(self, a: &Alloc) {
    Recovery(
        spaces,
        cc: _,
        epoch: _,
        latest_rtt: _,
        smoothed_rtt: _,
        rttvar: _,
        min_rtt: _,
        has_rtt_sample: _,
        first_rtt_sample_ns: _,
        peer_max_ack_delay: _,
        pto_count: _,
        is_server: _,
        has_handshake_keys: _,
        handshake_confirmed: _,
        received_handshake_ack: _,
        server_amp_blocked: _,
        timer_ns: _,
        last_now_ns: _,
        acked_times_ns: _,
        acked_times_head: _,
        acked_times_len: _,
        last_acked_pkts: _,
        last_lost_pkts: _,
        pto_space: _,
        anti_deadlock: _,
    ) := self
    [s0, s1, s2] := spaces
    s0.Free(a)
    s1.Free(a)
    s2.Free(a)
}

// Converts now to nanoseconds relative to self.epoch, latching epoch on first call.
Recovery.Ns(mut self, now: time.Instant): i64 {
    return match self.epoch {
        some(e) { now.Since(e).Nanos() }
        none {
            self.epoch = some(now)
            0
        }
    }
}

// Converts now to nanoseconds relative to self.epoch without mutating.
Recovery.NsRead(&self, now: time.Instant): i64 {
    return match self.epoch {
        some(e) { now.Since(e).Nanos() }
        none { 0 }
    }
}

// Sets whether this endpoint is the server (controls address-validation and
// anti-amplification timer rules, RFC 9002 A.8). O(1).
Recovery.SetRole(mut self, is_server: bool) {
    self.is_server = is_server
}

// Sets the peer's max_ack_delay transport parameter (RFC 9002 5.3, 6.2.1). O(1).
Recovery.SetPeerMaxAckDelay(mut self, d: time.Duration) {
    self.peer_max_ack_delay = if d.Nanos() < 0 { 0 } else { d.Nanos() }
}

// Records whether Handshake keys are currently available for anti-deadlock PTO
// selection (RFC 9002 6.2.2.1, A.8). O(1).
Recovery.SetHandshakeKeys(mut self, has: bool) {
    self.has_handshake_keys = has
}

// Marks the handshake confirmed and rearms the timer at now (RFC 9002 5.3, 6.2.1). O(1).
Recovery.SetHandshakeConfirmed(mut self, confirmed: bool, now: time.Instant) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    self.handshake_confirmed = confirmed
    self.SetLossDetectionTimer(now_ns)
}

// Marks whether the server is currently blocked by the 3x anti-amplification
// limit (RFC 9002 6.2.2.1, A.8). O(1).
Recovery.SetServerAmpBlocked(mut self, blocked: bool, now: time.Instant) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    self.server_amp_blocked = blocked
    self.SetLossDetectionTimer(now_ns)
}

// Marks whether the sender is application- or flow-control-limited (RFC 9002 7.8). O(1).
Recovery.SetAppLimited(mut self, limited: bool) {
    self.cc.app_limited = limited
}

// Allocates the next packet number in space (RFC 9000 12.3). O(1).
Recovery.NextPn(mut self, space: Space): i64 {
    return self.spaces[SpaceIndex(space)].AllocPn()
}

// Records an intentionally skipped packet number in space (RFC 9000 21.4). O(1).
Recovery.SkipPn(mut self, space: Space, pn: i64) {
    self.spaces[SpaceIndex(space)].RecordSkip(pn)
}

// Reports whether the peer has completed address validation (RFC 9002 A.8).
Recovery.PeerCompletedAddressValidation(&self): bool {
    if self.is_server {
        return true
    }
    return self.received_handshake_ack || self.handshake_confirmed
}

// Reports whether any space currently holds an ack-eliciting packet in flight.
Recovery.AnyAckElicitingInFlight(&self): bool {
    return self.spaces[0].ack_eliciting_in_flight > 0 || self.spaces[1].ack_eliciting_in_flight > 0 || self.spaces[2].ack_eliciting_in_flight > 0
}

// Returns the earliest time-threshold loss time and its space (RFC 9002 A.8).
Recovery.GetLossTimeAndSpace(&self): (t_ns: ?i64, space: Space) {
    var earliest: ?i64 = none
    var best := Space.Initial
    for i in 0..3 {
        match self.spaces[i].loss_time_ns {
            some(lt) {
                take := match earliest {
                    none { true }
                    some(e) { lt < e }
                }
                if take {
                    earliest = some(lt)
                    best = IndexToSpace(i)
                }
            }
            none {}
        }
    }
    return (t_ns: earliest, space: best)
}

// Computes the base PTO duration without max_ack_delay (RFC 9002 6.2.1, A.8).
Recovery.BasePtoNs(&self): i64 {
    v := 4 * self.rttvar
    var base := self.smoothed_rtt + if v > GranularityNs { v } else { GranularityNs }
    if base < GranularityNs {
        base = GranularityNs
    }
    shift := if self.pto_count > 30 { 30 } else { self.pto_count }
    if shift > 0 {
        max_base := i64(0x3FFF_FFFF_FFFF_FFFF) >> Cast[u64](shift)
        if base > max_base {
            return 0x3FFF_FFFF_FFFF_FFFF
        }
        base = base << Cast[u64](shift)
    }
    return base
}

// Computes the PTO deadline and target space (RFC 9002 A.8).
Recovery.GetPtoTimeAndSpace(&self, now_ns: i64): (t_ns: ?i64, space: Space, anti_deadlock: bool) {
    dur := self.BasePtoNs()
    if !self.AnyAckElicitingInFlight() {
        if !self.PeerCompletedAddressValidation() {
            sp := if self.has_handshake_keys { Space.Handshake } else { Space.Initial }
            return (t_ns: some(now_ns + dur), space: sp, anti_deadlock: true)
        }
        return (t_ns: none, space: Space.Initial, anti_deadlock: false)
    }
    var earliest: ?i64 = none
    var best := Space.Initial
    for i in 0..3 {
        if self.spaces[i].ack_eliciting_in_flight == 0 {
            continue
        }
        sp := IndexToSpace(i)
        var space_dur := dur
        if sp == Space.App {
            if !self.handshake_confirmed {
                return (t_ns: earliest, space: best, anti_deadlock: false)
            }
            shift := if self.pto_count > 30 { 30 } else { self.pto_count }
            mad := if self.peer_max_ack_delay > (i64(0x3FFF_FFFF_FFFF_FFFF) >> Cast[u64](shift)) {
                0x3FFF_FFFF_FFFF_FFFF
            } else {
                self.peer_max_ack_delay << Cast[u64](shift)
            }
            space_dur += mad
        }
        match self.spaces[i].last_ae_sent_ns {
            some(sent_t) {
                cand := sent_t + space_dur
                take := match earliest {
                    none { true }
                    some(e) { cand < e }
                }
                if take {
                    earliest = some(cand)
                    best = sp
                }
            }
            none {}
        }
    }
    return (t_ns: earliest, space: best, anti_deadlock: false)
}

// Recomputes self.timer_ns per RFC 9002 Appendix A.8.
Recovery.SetLossDetectionTimer(mut self, now_ns: i64) {
    loss := self.GetLossTimeAndSpace()
    match loss.t_ns {
        some(_) {
            self.timer_ns = loss.t_ns
            self.anti_deadlock = false
            return
        }
        none {}
    }
    if self.is_server && self.server_amp_blocked {
        self.timer_ns = none
        self.anti_deadlock = false
        return
    }
    if !self.AnyAckElicitingInFlight() && self.PeerCompletedAddressValidation() {
        self.timer_ns = none
        self.anti_deadlock = false
        return
    }
    pto := self.GetPtoTimeAndSpace(now_ns)
    self.timer_ns = pto.t_ns
    self.pto_space = pto.space
    self.anti_deadlock = pto.anti_deadlock
}

// Records a newly sent packet (RFC 9002 A.5, B.4).
// Amortized O(frames.Len()).
Recovery.OnSent(mut self, a: &Alloc, space: Space, pn: i64, bytes: int, ack_eliciting: bool, in_flight: bool, frames: []SentFrame, now: time.Instant): OutOfMemory!void {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    si := SpaceIndex(space)
    try self.spaces[si].Push(a, pn, now_ns, bytes, ack_eliciting, in_flight, frames)
    if in_flight {
        self.cc.OnSent(bytes, true, now_ns, self.smoothed_rtt)
        if ack_eliciting {
            self.SetLossDetectionTimer(now_ns)
        }
    }
}

// Records a PTO or anti-deadlock probe packet (RFC 9002 7.5: never blocked by
// cwnd, counts in bytes_in_flight). Amortized O(frames.Len()).
Recovery.OnProbeSent(mut self, a: &Alloc, space: Space, pn: i64, bytes: int, frames: []SentFrame, now: time.Instant): OutOfMemory!void {
    try self.OnSent(a, space, pn, bytes, true, true, frames, now)
}

// Appends an acknowledged packet's send time to the cross-space ring.
Recovery.RecordAckedTime(mut self, sent_ns: i64) {
    if self.acked_times_len < AckedTimeRingCap {
        pos := (self.acked_times_head + self.acked_times_len) % AckedTimeRingCap
        self.acked_times_ns[pos] = sent_ns
        self.acked_times_len += 1
    } else {
        self.acked_times_ns[self.acked_times_head] = sent_ns
        self.acked_times_head = (self.acked_times_head + 1) % AckedTimeRingCap
    }
}

// Reports whether any packet across all spaces sent strictly between t1 and t2
// was acknowledged (RFC 9002 7.6.2). O(acked_times_len).
Recovery.AckedBetween(&self, t1: i64, t2: i64): bool {
    for i in 0..self.acked_times_len {
        t := self.acked_times_ns[(self.acked_times_head + i) % AckedTimeRingCap]
        if t > t1 && t < t2 {
            return true
        }
    }
    return false
}

// Updates smoothed_rtt, rttvar, and min_rtt per RFC 9002 A.7 (rttvar first,
// then smoothed_rtt, lines 1661-1662). O(1); allocates nothing.
Recovery.UpdateRtt(mut self, ack_delay_ns: i64, now_ns: i64) {
    if !self.has_rtt_sample {
        self.min_rtt = self.latest_rtt
        self.smoothed_rtt = self.latest_rtt
        self.rttvar = self.latest_rtt / 2
        self.has_rtt_sample = true
        self.first_rtt_sample_ns = now_ns
        return
    }
    if self.latest_rtt < self.min_rtt {
        self.min_rtt = self.latest_rtt
    }
    var delay := if ack_delay_ns < 0 { 0 } else { ack_delay_ns }
    if self.handshake_confirmed && delay > self.peer_max_ack_delay {
        delay = self.peer_max_ack_delay
    }
    var adjusted := self.latest_rtt
    if self.latest_rtt >= self.min_rtt + delay {
        adjusted = self.latest_rtt - delay
    }
    // RFC 9002 A.7 l. 1661-1662: update rttvar before smoothed_rtt.
    diff := AbsI64(self.smoothed_rtt - adjusted)
    self.rttvar = (3 * self.rttvar + diff) / 4
    self.smoothed_rtt = (7 * self.smoothed_rtt + adjusted) / 8
}

// Computes the time-threshold loss delay: max(9/8 * max(latest, smoothed), 1ms)
// (RFC 9002 6.1.2, A.10).
Recovery.LossDelayNs(&self): i64 {
    r := if self.latest_rtt > self.smoothed_rtt { self.latest_rtt } else { self.smoothed_rtt }
    d := (r / 8) * 9 + ((r % 8) * 9) / 8
    return if d > GranularityNs { d } else { GranularityNs }
}

// Computes the persistent congestion duration (RFC 9002 7.6.1): includes
// peer_max_ack_delay regardless of space.
Recovery.PersistentCongestionDurationNs(&self): i64 {
    v := 4 * self.rttvar
    base := self.smoothed_rtt + (if v > GranularityNs { v } else { GranularityNs }) + self.peer_max_ack_delay
    return base * PersistentCongestionThreshold
}

// Detects and removes lost packets in space per RFC 9002 A.10 and applies
// NewReno + persistent congestion per Appendix B.8. Returns (lost_frames, pc_triggered).
// O(active packets in space).
Recovery.DetectLost(mut self, space: Space, now_ns: i64, lost_out: mut []SentFrame): (lost: int, pc: bool) {
    si := SpaceIndex(space)
    largest_acked := self.spaces[si].largest_acked
    if largest_acked < 0 {
        return (lost: 0, pc: false)
    }
    self.spaces[si].loss_time_ns = none
    loss_delay := self.LossDelayNs()
    lost_send_time := now_ns - loss_delay

    var lost_frames := 0
    var lost_pkts := 0
    var last_lost_in_flight_ns: ?i64 = none
    var pc_run_start: ?i64 = none
    var pc_run_end: ?i64 = none
    var pc_triggered := false
    pc_dur := self.PersistentCongestionDurationNs()

    pcap := self.spaces[si].pkts.Len()
    for k in 0..self.spaces[si].pkt_len {
        idx := (self.spaces[si].pkt_head + k) % pcap
        pkt := self.spaces[si].pkts[idx]
        if pkt.pn > largest_acked {
            break
        }
        if pkt.status != SentActive {
            continue
        }
        if pkt.sent_ns <= lost_send_time || largest_acked >= pkt.pn + PacketThreshold {
            items := self.spaces[si].pkts.ItemsMut()
            items[idx].status = SentLost
            lost_pkts += 1
            lost_frames = self.spaces[si].CopyFrames(&pkt, lost_out, lost_frames)
            if pkt.in_flight {
                self.spaces[si].bytes_in_flight -= pkt.bytes
                self.cc.bytes_in_flight -= pkt.bytes
                if self.cc.bytes_in_flight < 0 {
                    self.cc.bytes_in_flight = 0
                }
                if pkt.ack_eliciting {
                    self.spaces[si].ack_eliciting_in_flight -= 1
                }
                last_lost_in_flight_ns = match last_lost_in_flight_ns {
                    none { some(pkt.sent_ns) }
                    some(t) { if pkt.sent_ns > t { some(pkt.sent_ns) } else { some(t) } }
                }
                if pkt.ack_eliciting && self.has_rtt_sample && pkt.sent_ns > self.first_rtt_sample_ns {
                    match pc_run_start {
                        some(rs) {
                            re := pc_run_end.Must()
                            if self.AckedBetween(re, pkt.sent_ns) {
                                pc_run_start = some(pkt.sent_ns)
                                pc_run_end = some(pkt.sent_ns)
                            } else {
                                pc_run_end = some(pkt.sent_ns)
                                if pkt.sent_ns - rs > pc_dur {
                                    pc_triggered = true
                                }
                            }
                        }
                        none {
                            pc_run_start = some(pkt.sent_ns)
                            pc_run_end = some(pkt.sent_ns)
                        }
                    }
                }
            }
        } else {
            lt := pkt.sent_ns + loss_delay
            self.spaces[si].loss_time_ns = match self.spaces[si].loss_time_ns {
                none { some(lt) }
                some(cur) { if lt < cur { some(lt) } else { some(cur) } }
            }
        }
    }
    self.last_lost_pkts = lost_pkts
    match last_lost_in_flight_ns {
        some(lt) { self.cc.OnCongestionEvent(lt, now_ns) }
        none {}
    }
    if pc_triggered {
        self.cc.OnPersistentCongestion()
        if self.has_rtt_sample {
            self.min_rtt = self.latest_rtt
        }
    }
    return (lost: lost_frames, pc: pc_triggered)
}

// Processes an ACK frame for space (RFC 9002 A.7). Fails with
// TransportError(ErrProtocolViolation) if any range names an unsent or
// skipped packet number (RFC 9000 13.1, 21.4). Processes at most 256 ranges
// while validating all ranges. O(ranges.Len() + log N + newly_acked + lost).
Recovery.OnAck(
    mut self,
    space: Space,
    ranges: []AckRange,
    ack_delay: time.Duration,
    now: time.Instant,
    acked: mut []SentFrame,
    lost: mut []SentFrame,
): Error!(acked: int, lost: int) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    self.last_acked_pkts = 0
    self.last_lost_pkts = 0
    si := SpaceIndex(space)
    if !self.spaces[si].ValidateRanges(ranges) {
        fail TransportError(code: ErrProtocolViolation)
    }
    if ranges.Len() == 0 {
        return (acked: 0, lost: 0)
    }
    cap_ranges := if ranges.Len() > 256 { 256 } else { ranges.Len() }
    var ack_largest: i64 = -1
    var acked_n := 0
    var acked_pkts := 0
    var max_newly_pn: i64 = -1
    var max_newly_ns: i64 = 0
    var has_ae := false

    for i in 0..cap_ranges {
        r := ranges[i]
        if r.hi > ack_largest {
            ack_largest = r.hi
        }
        res := self.spaces[si].AckRange(r.lo, r.hi, acked, acked_n)
        acked_n = res.acked_n
        acked_pkts += res.acked_pkts
        if res.max_pn > max_newly_pn {
            max_newly_pn = res.max_pn
            max_newly_ns = res.max_ns
        }
        if res.has_ae {
            has_ae = true
        }
    }
    if ack_largest > self.spaces[si].largest_acked {
        self.spaces[si].largest_acked = ack_largest
    }
    self.last_acked_pkts = acked_pkts
    if acked_pkts == 0 {
        return (acked: 0, lost: 0)
    }
    if space == Space.Handshake && !self.is_server {
        self.received_handshake_ack = true
    }
    pcap := self.spaces[si].pkts.Len()
    for k in 0..self.spaces[si].pkt_len {
        idx := (self.spaces[si].pkt_head + k) % pcap
        pkt := self.spaces[si].pkts[idx]
        if pkt.pn > ack_largest {
            break
        }
        if pkt.status == SentAckedPending {
            self.RecordAckedTime(pkt.sent_ns)
        }
    }
    if max_newly_pn == ack_largest && has_ae {
        dt := now_ns - max_newly_ns
        self.latest_rtt = if dt < 0 { 0 } else { dt }
        self.UpdateRtt(ack_delay.Nanos(), now_ns)
    }
    dl := self.DetectLost(space, now_ns, lost)
    for k in 0..self.spaces[si].pkt_len {
        idx := (self.spaces[si].pkt_head + k) % pcap
        pkt := self.spaces[si].pkts[idx]
        if pkt.pn > ack_largest {
            break
        }
        if pkt.status == SentAckedPending {
            items := self.spaces[si].pkts.ItemsMut()
            items[idx].status = SentAcked
            if dl.pc {
                self.cc.RemoveDiscarded(if pkt.in_flight { pkt.bytes } else { 0 })
            } else {
                self.cc.OnAcked(pkt.bytes, pkt.sent_ns, pkt.in_flight)
            }
        }
    }
    self.spaces[si].TrimHead()
    if self.PeerCompletedAddressValidation() {
        self.pto_count = 0
    }
    self.SetLossDetectionTimer(now_ns)
    return (acked: acked_n, lost: dl.lost)
}

// Handles expiration of the loss-detection or PTO timer (RFC 9002 A.9).
// Time-threshold loss marks packets lost (probes = 0); PTO marks nothing lost
// (RFC 9002 6.2) and increments pto_count, returning 1 probe for anti-deadlock
// or 2 for normal PTO. O(active packets in space).
Recovery.OnTimeout(mut self, now: time.Instant, lost: mut []SentFrame): (lost: int, probes: int) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    self.last_lost_pkts = 0
    loss := self.GetLossTimeAndSpace()
    match loss.t_ns {
        some(_) {
            dl := self.DetectLost(loss.space, now_ns, lost)
            self.spaces[SpaceIndex(loss.space)].TrimHead()
            self.SetLossDetectionTimer(now_ns)
            return (lost: dl.lost, probes: 0)
        }
        none {}
    }
    if !self.AnyAckElicitingInFlight() {
        self.anti_deadlock = true
        self.pto_space = if self.has_handshake_keys { Space.Handshake } else { Space.Initial }
        self.pto_count += 1
        self.SetLossDetectionTimer(now_ns)
        return (lost: 0, probes: 1)
    }
    pto := self.GetPtoTimeAndSpace(now_ns)
    self.anti_deadlock = false
    self.pto_space = pto.space
    self.pto_count += 1
    self.SetLossDetectionTimer(now_ns)
    return (lost: 0, probes: 2)
}

// Unblocks an amplification-limited server when a datagram arrives from the
// client (RFC 9002 6.2.2.1, A.6) and runs an overdue PTO immediately.
Recovery.OnDatagramReceived(mut self, now: time.Instant, lost: mut []SentFrame): (lost: int, probes: int) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    if self.is_server && self.server_amp_blocked {
        self.server_amp_blocked = false
        self.SetLossDetectionTimer(now_ns)
        match self.timer_ns {
            some(t) {
                if t <= now_ns {
                    return self.OnTimeout(now, lost)
                }
            }
            none {}
        }
    }
    return (lost: 0, probes: 0)
}

// Discards Initial or Handshake recovery state when its keys are dropped
// (RFC 9002 6.2.2, A.11, B.9): removes its bytes from flight, resets
// pto_count, and rearms the loss/PTO timer. O(1).
Recovery.Discard(mut self, space: Space) {
    if space == Space.App {
        return
    }
    si := SpaceIndex(space)
    self.cc.RemoveDiscarded(self.spaces[si].bytes_in_flight)
    self.spaces[si].Clear(false)
    self.pto_count = 0
    self.SetLossDetectionTimer(self.last_now_ns)
}

// Resets congestion control, loss recovery, and timers upon receiving a Retry
// packet (RFC 9002 6.3), leaving CRYPTO buffers untouched. O(1).
Recovery.ResetForRetry(mut self, now: time.Instant) {
    now_ns := self.Ns(now)
    self.last_now_ns = now_ns
    for i in 0..3 {
        self.spaces[i].Clear(true)
    }
    self.cc.Reset()
    self.latest_rtt = 0
    self.smoothed_rtt = InitialRttNs
    self.rttvar = InitialRttNs / 2
    self.min_rtt = 0
    self.has_rtt_sample = false
    self.first_rtt_sample_ns = 0
    self.pto_count = 0
    self.received_handshake_ack = false
    self.timer_ns = none
    self.acked_times_head = 0
    self.acked_times_len = 0
    self.SetLossDetectionTimer(now_ns)
}

// Returns the remaining duration until the next loss-detection or PTO timer
// deadline at now (0 when overdue), or none when idle (RFC 9002 A.8).
// O(1); allocates nothing.
Recovery.Timer(&self, now: time.Instant): ?time.Duration {
    now_ns := self.NsRead(now)
    return match self.timer_ns {
        none { none }
        some(t) {
            rem := t - now_ns
            some(time.Nanos(if rem < 0 { 0 } else { rem }))
        }
    }
}

// Reports whether an in-flight packet of bytes may be sent at now under cwnd
// and the pacer (RFC 9002 7, 7.7). ACK-only packets and PTO probes bypass this
// check. O(1); allocates nothing.
Recovery.CanSend(&self, bytes: int, now: time.Instant): bool {
    now_ns := self.NsRead(now)
    return self.cc.CanSend(bytes, now_ns, self.smoothed_rtt)
}

// Returns the current single-PTO period (including max_ack_delay when the
// handshake is confirmed, RFC 9002 6.2.1). O(1); allocates nothing.
Recovery.Pto(&self): time.Duration {
    v := 4 * self.rttvar
    var d := self.smoothed_rtt + if v > GranularityNs { v } else { GranularityNs }
    if self.handshake_confirmed {
        d += self.peer_max_ack_delay
    }
    if d < GranularityNs {
        d = GranularityNs
    }
    return time.Nanos(d)
}

// Returns the remaining delay until the pacer releases bytes at now, or none
// when ready. O(1).
Recovery.NextPaceDelay(&self, bytes: int, now: time.Instant): ?time.Duration {
    now_ns := self.NsRead(now)
    return self.cc.NextPaceDelay(bytes, now_ns, self.smoothed_rtt)
}

// Accessors for connection state and test verification.
Recovery.SmoothedRtt(&self): time.Duration {
    return time.Nanos(self.smoothed_rtt)
}

Recovery.RttVar(&self): time.Duration {
    return time.Nanos(self.rttvar)
}

Recovery.MinRtt(&self): time.Duration {
    return time.Nanos(self.min_rtt)
}

Recovery.LatestRtt(&self): time.Duration {
    return time.Nanos(self.latest_rtt)
}

Recovery.BytesInFlight(&self): int {
    return self.cc.bytes_in_flight
}

Recovery.Cwnd(&self): int {
    return self.cc.cwnd
}

Recovery.Ssthresh(&self): int {
    return self.cc.ssthresh
}

Recovery.PtoCount(&self): int {
    return self.pto_count
}

Recovery.PtoSpace(&self): Space {
    return self.pto_space
}

Recovery.IsAntiDeadlock(&self): bool {
    return self.anti_deadlock
}

Recovery.LargestAcked(&self, space: Space): ?i64 {
    l := self.spaces[SpaceIndex(space)].largest_acked
    if l < 0 {
        return none
    }
    return some(l)
}
