// Why: QUIC NewReno congestion control and leaky-bucket pacing prevent
// bursting past cwnd or flooding the path while keeping ACK-only packets
// unpaced and probes unblocked.
//
// What: Congestion (New, Reset, OnSent, OnAcked, OnCongestionEvent,
// OnPersistentCongestion, RemoveDiscarded, CanSend, NextPaceDelay)
// and RFC 9002 Appendix B / Section 7 constants.
//
// How: integer-only NewReno (initial window 12,000, minimum window 2,400,
// 0.5x reduction once per recovery period, congestion avoidance remainder
// tracking) paired with a 1.25x cwnd/smoothed_rtt leaky bucket capped at a
// 10-packet (12,000-byte) burst. Zero heap allocation; overflow-safe i64 math.

import std.time

// Maximum payload size per QUIC datagram (RFC 9002 B.2; fixed at 1,200 in v1).
const MaxDatagramSize: int = 1200

// Initial congestion window: min(10 * 1200, max(14720, 2 * 1200)) = 12,000 (RFC 9002 7.2).
const InitialWindow: int = 12000

// Minimum congestion window: 2 * max_datagram_size = 2,400 (RFC 9002 7.2).
const MinimumWindow: int = 2400

// Maximum burst the leaky-bucket pacer permits at once: 10 packets = 12,000 bytes (RFC 9002 7.7).
const PacerBurstBytes: i64 = 12000

// Persistent congestion threshold in PTO periods (RFC 9002 7.6.1, B.1).
const PersistentCongestionThreshold: i64 = 3

// Infinite slow-start threshold sentinel.
const InfiniteSsthresh: int = 0x7FFF_FFFF_FFFF_FFFF

// NewReno congestion controller and leaky-bucket pacer (RFC 9002 7, Appendix B).
// Plain value; allocates nothing.
struct Congestion {
    cwnd: int
    ssthresh: int
    bytes_in_flight: int
    recovery_start_ns: ?i64
    ca_rem: int
    app_limited: bool
    recovery_probe_owed: bool
    pacer_tokens: i64
    pacer_last_ns: ?i64
}

// Initializes a Congestion controller with RFC 9002 B.3 defaults. O(1).
Congestion.New(): Congestion {
    return Congestion(
        cwnd: InitialWindow,
        ssthresh: InfiniteSsthresh,
        bytes_in_flight: 0,
        recovery_start_ns: none,
        ca_rem: 0,
        app_limited: false,
        recovery_probe_owed: false,
        pacer_tokens: PacerBurstBytes,
        pacer_last_ns: none,
    )
}

// Resets all congestion control and pacer state (used on Retry, RFC 9002 6.3). O(1).
Congestion.Reset(mut self) {
    *self = Congestion.New()
}

// Reports whether a packet sent at sent_ns falls inside the current recovery
// period (RFC 9002 B.5). O(1).
Congestion.InRecovery(&self, sent_ns: i64): bool {
    return match self.recovery_start_ns {
        none { false }
        some(rs) { sent_ns <= rs }
    }
}

// Computes the pacer token count at now_ns without mutating self (RFC 9002 7.7:
// rate = 1.25 * cwnd / smoothed_rtt, burst capped at PacerBurstBytes).
// Overflow-safe across arbitrary elapsed durations. O(1).
Congestion.PacerTokensAt(&self, now_ns: i64, smoothed_rtt_ns: i64): i64 {
    last := match self.pacer_last_ns {
        none { return self.pacer_tokens }
        some(t) { t }
    }
    if now_ns <= last {
        return self.pacer_tokens
    }
    if self.pacer_tokens >= PacerBurstBytes {
        return PacerBurstBytes
    }
    elapsed_ns := now_ns - last
    rtt_ns := if smoothed_rtt_ns > 1 { smoothed_rtt_ns } else { 1 }
    rate_num := i64(5) * i64(self.cwnd)
    rate_den := i64(4) * rtt_ns
    need := PacerBurstBytes - self.pacer_tokens
    if rtt_ns <= 100_000_000_000_000 {
        time_for_full := (need * rate_den) / rate_num
        if elapsed_ns >= time_for_full {
            return PacerBurstBytes
        }
        added := (elapsed_ns * rate_num) / rate_den
        tok := self.pacer_tokens + added
        return if tok > PacerBurstBytes { PacerBurstBytes } else { tok }
    }
    added := (elapsed_ns / rate_den) * rate_num
    tok := self.pacer_tokens + added
    return if tok > PacerBurstBytes { PacerBurstBytes } else { tok }
}

// Updates bytes_in_flight and pacer tokens when a packet is sent (RFC 9002 B.4, 7.7).
// ACK-only packets (in_flight == false) are neither counted nor paced. O(1).
Congestion.OnSent(mut self, bytes: int, in_flight: bool, now_ns: i64, smoothed_rtt_ns: i64) {
    if !in_flight {
        return
    }
    tok := self.PacerTokensAt(now_ns, smoothed_rtt_ns)
    var next_tok := tok - i64(bytes)
    if next_tok < -PacerBurstBytes {
        next_tok = -PacerBurstBytes
    }
    self.pacer_tokens = next_tok
    self.pacer_last_ns = some(now_ns)
    self.bytes_in_flight += bytes
    self.recovery_probe_owed = false
}

// Processes one newly acknowledged packet (RFC 9002 B.5). Ignores packets not
// in flight, skips window growth when app-limited or still in recovery, and
// grows cwnd by bytes in slow start or <= MaxDatagramSize per cwnd in CA. O(1).
Congestion.OnAcked(mut self, bytes: int, sent_ns: i64, in_flight: bool) {
    if !in_flight {
        return
    }
    self.bytes_in_flight -= bytes
    if self.bytes_in_flight < 0 {
        self.bytes_in_flight = 0
    }
    if self.app_limited {
        return
    }
    if self.InRecovery(sent_ns) {
        return
    }
    self.recovery_start_ns = none
    if self.cwnd < self.ssthresh {
        self.cwnd += bytes
    } else {
        num := MaxDatagramSize * bytes + self.ca_rem
        self.cwnd += num / self.cwnd
        self.ca_rem = num % self.cwnd
    }
}

// Enters congestion recovery if sent_ns is not already covered by an active
// recovery period (RFC 9002 B.6): halves cwnd (clamped at MinimumWindow) and
// allows one packet to be sent to speed loss recovery. O(1).
Congestion.OnCongestionEvent(mut self, sent_ns: i64, now_ns: i64) {
    if self.InRecovery(sent_ns) {
        return
    }
    self.recovery_start_ns = some(now_ns)
    self.ssthresh = self.cwnd / 2
    self.cwnd = if self.ssthresh > MinimumWindow { self.ssthresh } else { MinimumWindow }
    self.ca_rem = 0
    self.recovery_probe_owed = true
}

// Applies persistent congestion response (RFC 9002 7.6.2, B.8): drops cwnd to
// MinimumWindow and clears congestion_recovery_start_time. O(1).
Congestion.OnPersistentCongestion(mut self) {
    self.cwnd = MinimumWindow
    self.recovery_start_ns = none
    self.ca_rem = 0
}

// Removes discarded Initial or Handshake bytes from bytes_in_flight (RFC 9002 B.9). O(1).
Congestion.RemoveDiscarded(mut self, bytes: int) {
    self.bytes_in_flight -= bytes
    if self.bytes_in_flight < 0 {
        self.bytes_in_flight = 0
    }
}

// Reports whether an in-flight packet of bytes may be sent at now_ns under cwnd
// (with the single recovery-entry packet exception) and the pacer. O(1).
Congestion.CanSend(&self, bytes: int, now_ns: i64, smoothed_rtt_ns: i64): bool {
    if bytes <= 0 {
        return true
    }
    cwnd_ok := self.bytes_in_flight + bytes <= self.cwnd || self.recovery_probe_owed
    if !cwnd_ok {
        return false
    }
    need := if i64(bytes) < PacerBurstBytes { i64(bytes) } else { PacerBurstBytes }
    return self.PacerTokensAt(now_ns, smoothed_rtt_ns) >= need
}

// Returns the delay until the pacer allows sending bytes, or none when ready. O(1).
Congestion.NextPaceDelay(&self, bytes: int, now_ns: i64, smoothed_rtt_ns: i64): ?time.Duration {
    if bytes <= 0 {
        return none
    }
    need := if i64(bytes) < PacerBurstBytes { i64(bytes) } else { PacerBurstBytes }
    tok := self.PacerTokensAt(now_ns, smoothed_rtt_ns)
    if tok >= need {
        return none
    }
    deficit := need - tok
    rtt_ns := if smoothed_rtt_ns > 1 { smoothed_rtt_ns } else { 1 }
    rate_num := i64(5) * i64(self.cwnd)
    rate_den := i64(4) * rtt_ns
    wait_ns := (deficit * rate_den + rate_num - 1) / rate_num
    return some(time.Nanos(wait_ns))
}
