// Why: QUIC connections must transition through Closing and Draining states per
// RFC 9000 10.2, rate-limiting repeated CONNECTION_CLOSE responses and honoring
// 3x amplification when keyless, and endpoints must rate-limit stateless resets
// to 64 per second.
//
// What: ClosePhase, CloseState, and ResetLimiter (64/sec token bucket + stateless
// reset packet builder).
//
// How: CloseState records the closing/draining deadline (3*PTO), rate-limits
// replies (every 2nd packet received, or 1 if keyless up to 3x received bytes),
// and rejects unvalidated addresses. ResetLimiter enforces 64/sec and
// strictly smaller packet length.

import std.crypto
import std.net
import std.raw
import std.time

// Maximum stateless resets an endpoint sends per second (Section 3.11).
const MaxStatelessResetsPerSec: i64 = 64

// Connection close lifecycle phase (RFC 9000 10.2).
enum ClosePhase {
    Open
    Closing
    Draining
}

// Per-connection closing/draining state (RFC 9000 10.2.1, 10.2.2).
struct CloseState {
    phase: ClosePhase
    code: u64
    app: bool
    frame_type: u64
    until: ?time.Instant
    rx_count: i64
    rx_bytes: i64
    tx_bytes: i64
    keyless: bool
    validated_addr: net.Addr
    has_validated: bool
}

// Adds duration d to instant t. O(1).
InstantAdd(t: time.Instant, d: time.Duration): time.Instant {
    var out := t
    unsafe "time.Instant is a single-field struct { nanos: i64 }" {
        p := raw.PtrCast[time.Instant, i64](raw.OfMut(mut out))
        *p = *p + d.Nanos()
    }
    return out
}

// Returns instant t in nanoseconds relative to the clock epoch. O(1).
InstantNanos(t: time.Instant): i64 {
    var out: i64 = 0
    unsafe "time.Instant is a single-field struct { nanos: i64 }" {
        out = *raw.PtrCast[time.Instant, i64](raw.Of(&t))
    }
    return out
}

// Zero placeholder network address used in uninitialized slots/buffers.
const QuicZeroAddr = net.Addr(ip: [0; 16], port: 0, v6: false)

// Creates an open CloseState. O(1).
CloseState.New(): CloseState {
    return CloseState(
        phase: ClosePhase.Open,
        code: 0,
        app: false,
        frame_type: 0,
        until: none,
        rx_count: 0,
        rx_bytes: 0,
        tx_bytes: 0,
        keyless: false,
        validated_addr: QuicZeroAddr,
        has_validated: false,
    )
}

// Enters the Closing state for 3*PTO (RFC 9000 10.2.1). O(1).
CloseState.Close(mut self, code: u64, app: bool, frame_type: u64, now: time.Instant, pto: time.Duration) {
    if self.phase != ClosePhase.Open {
        return
    }
    self.phase = ClosePhase.Closing
    self.code = code
    self.app = app
    self.frame_type = frame_type
    dur := if pto.Nanos() > 0 { 3 * pto.Nanos() } else { 3 * InitialPathPtoNs }
    self.until = some(InstantAdd(now, time.Nanos(dur)))
}

// Sets the validated peer address so Closing state ignores packets from
// unvalidated addresses (RFC 9000 10.2.1). O(1).
CloseState.SetValidatedAddr(mut self, peer: net.Addr) {
    self.validated_addr = peer
    self.has_validated = true
}

// Marks this closing state as keyless (e.g. stored pre-built close packet),
// enabling the 3x received-bytes cap (RFC 9000 10.2.1). O(1).
CloseState.SetKeyless(mut self, keyless: bool) {
    self.keyless = keyless
}

// Enters the silent Draining state for 3*PTO upon receiving CONNECTION_CLOSE or
// a stateless reset (RFC 9000 10.2.2, 10.3.1). O(1).
CloseState.OnPeerClose(mut self, code: u64, app: bool, now: time.Instant, pto: time.Duration) {
    if self.phase == ClosePhase.Draining {
        return
    }
    self.phase = ClosePhase.Draining
    self.code = code
    self.app = app
    dur := if pto.Nanos() > 0 { 3 * pto.Nanos() } else { 3 * InitialPathPtoNs }
    self.until = some(InstantAdd(now, time.Nanos(dur)))
}

// Reports whether the connection is in Closing or Draining. O(1).
CloseState.IsClosed(&self): bool {
    return self.phase != ClosePhase.Open
}

// Returns the end of the Closing/Draining period, or none if Open. O(1).
CloseState.Due(&self): ?time.Instant {
    return self.until
}

// Reports whether the Closing/Draining period has expired at now. O(1).
CloseState.Expired(&self, now: time.Instant): bool {
    if t := self.until {
        return now.Since(t).Nanos() >= 0
    }
    return false
}

// Decides whether an incoming packet of `pkt_bytes` from `from` in Closing
// state should be answered with a CONNECTION_CLOSE frame.
// Rate-limited to at most 1 reply per 2 packets, discards unvalidated addresses,
// and respects 3x amplification when keyless. O(1).
CloseState.Answer(mut self, from: &net.Addr, pkt_bytes: int, reply_size: int): bool {
    if self.phase != ClosePhase.Closing {
        return false
    }
    if self.has_validated && !AddrEqual(from, &self.validated_addr) {
        return false
    }
    self.rx_count += 1
    self.rx_bytes += i64(pkt_bytes)
    // Answer at most every second packet (1st, 3rd, 5th, ... -> falling rate).
    if self.rx_count % 2 == 0 {
        return false
    }
    if self.keyless {
        if self.tx_bytes + i64(reply_size) > self.rx_bytes * 3 {
            return false
        }
    }
    self.tx_bytes += i64(reply_size)
    return true
}

// Token bucket enforcing at most 64 stateless resets per second per endpoint
// (RFC 9000 10.3.3, Section 3.11).
struct ResetLimiter {
    tokens: i64
    last_ns: i64
    has_last: bool
}

// Creates a full 64-token ResetLimiter. O(1).
ResetLimiter.New(): ResetLimiter {
    return ResetLimiter(tokens: MaxStatelessResetsPerSec, last_ns: 0, has_last: false)
}

// Consumes one stateless reset permit at now, returning false when exhausted. O(1).
ResetLimiter.Allow(mut self, now: time.Instant): bool {
    now_ns := InstantNanos(now)
    if !self.has_last {
        self.last_ns = now_ns
        self.has_last = true
    } else if now_ns > self.last_ns {
        elapsed := now_ns - self.last_ns
        // Refill at 64 tokens per 1_000_000_000 ns.
        add := (elapsed * MaxStatelessResetsPerSec) / 1_000_000_000
        if add > 0 {
            self.tokens += add
            if self.tokens > MaxStatelessResetsPerSec {
                self.tokens = MaxStatelessResetsPerSec
            }
            self.last_ns += (add * 1_000_000_000) / MaxStatelessResetsPerSec
        }
    }
    if self.tokens <= 0 {
        return false
    }
    self.tokens -= 1
    return true
}

// Builds a Stateless Reset packet strictly shorter than trigger_len (at least
// 21 bytes, RFC 9000 10.3, 10.3.3). Returns bytes written, or
// 0 if trigger_len <= 21 or out is too short. O(len).
WriteStatelessReset(io: Io, out: mut []u8, token: &[16]u8, trigger_len: int): int {
    if trigger_len <= MinShortPacketLen {
        return 0
    }
    var n := trigger_len - 1
    if n > 43 {
        n = 42
    }
    if n < MinShortPacketLen {
        return 0
    }
    if out.Len() < n {
        return 0
    }
    prefix := n - 16
    crypto.Random(io, mut out[0..prefix]) else {}
    // Short-header shape: bit 0x80 = 0 (short), bit 0x40 = 1 (fixed bit).
    out[0] = (out[0] & 0x3f) | 0x40
    (mut out[prefix..n]).CopyFrom(token[..])
    return n
}
