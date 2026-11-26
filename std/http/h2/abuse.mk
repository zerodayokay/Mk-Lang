// Why: HTTP/2 peers can exhaust server resources via rapid stream resets,
// control-frame floods, tiny unsolicited WINDOW_UPDATEs, or slow data dribble
// (RFC 9113 10.5); pure rate-limiting and progress state lets the engine and
// fake-clock tests check these defences deterministically.
//
// What: abuse-limit constants, Bucket, Progress, and UpdateGate.
//
// How: Bucket uses exact nanosecond-remainder accumulation so sub-second calls
// refill at rate/sec without drift or overflow; Progress records when a stream
// first became pending and compares cumulative bytes moved over at least
// window against floor; UpdateGate tracks whether DATA went out since the last
// WINDOW_UPDATE and whether the increment meets MinWindowGrant.

import std.time

/// Maximum CONTINUATION frames allowed on one field block.
export const MaxContinuations: int = 32

/// Maximum owed PING ACK payloads queued at once.
export const MaxOwedPings: int = 32

/// Maximum owed SETTINGS ACKs queued at once (RFC 9113 10.5).
export const MaxOwedSettingsAcks: int = 32

/// Initial capacity of the per-connection reset budget (RFC 9113 10.5).
export const ResetBudget: int = 200

/// Refill rate per second of ResetBudget.
export const ResetRefill: int = 100

/// Initial capacity of the per-connection control-frame budget (RFC 9113 10.5).
export const ControlBudget: int = 1000

/// Refill rate per second of ControlBudget.
export const ControlRefill: int = 100

/// Minimum average throughput in bytes per second required while a stream has
/// pending data over request_header.
export const ProgressFloor: int = 1024

/// Minimum WINDOW_UPDATE increment (in octets) that is not charged to
/// ControlBudget when following sent DATA (RFC 9113 10.5).
export const MinWindowGrant: int = 1024

const NsPerSec: i64 = 1_000_000_000
const MaxI64: i64 = 0x7FFF_FFFF_FFFF_FFFF

/// A token bucket of capacity cap refilled continuously at rate tokens per
/// second.
export struct Bucket {
    cap: int
    rate: int
    tokens: int
    rem_ns: i64
    last: time.Instant
}

/// A full token bucket of capacity cap refilling at rate tokens per second
/// starting at now. O(1); allocates nothing.
export Bucket.New(cap: int, rate: int, now: time.Instant): Bucket {
    c := if cap < 0 { 0 } else { cap }
    r := if rate < 0 { 0 } else { rate }
    return Bucket(cap: c, rate: r, tokens: c, rem_ns: 0, last: now)
}

/// Refills tokens elapsed since the last call up to now and attempts to take
/// one token; returns true if taken, false when the bucket is empty. O(1);
/// allocates nothing.
export Bucket.Take(mut self, now: time.Instant): bool {
    elapsed := now.Since(self.last).Nanos()
    if elapsed > 0 {
        self.last = now
        if self.rate > 0 && self.tokens < self.cap {
            room := self.cap - self.tokens
            secs := elapsed / NsPerSec
            ns := elapsed % NsPerSec
            // Cap rate multiplication against i64 overflow if rate > 1e9.
            r64 := if i64(self.rate) > NsPerSec { NsPerSec } else { i64(self.rate) }
            if secs >= i64(room) / r64 + 1 {
                self.tokens = self.cap
                self.rem_ns = 0
            } else {
                var add := int(secs * r64)
                acc := self.rem_ns + ns * r64
                add += int(acc / NsPerSec)
                self.rem_ns = acc % NsPerSec
                if add >= room {
                    self.tokens = self.cap
                    self.rem_ns = 0
                } else {
                    self.tokens += add
                }
            }
        } else if self.tokens >= self.cap {
            self.rem_ns = 0
        }
    }
    if self.tokens <= 0 {
        return false
    }
    self.tokens -= 1
    return true
}

/// Current whole tokens available without advancing time. O(1).
export Bucket.Tokens(&self): int {
    return self.tokens
}

/// Tracks one stream's cumulative progress while it has data pending to send
/// or receive, defeating slow-read / one-byte-window-update dribble attacks
/// (RFC 9113 10.5).
export struct Progress {
    active: bool
    since: ?time.Instant
    moved: int
}

/// An idle progress tracker. O(1); allocates nothing.
export Progress.New(): Progress {
    return Progress(active: false, since: none, moved: 0)
}

/// Marks the stream as having pending data at now if it was idle; repeated
/// calls while already pending keep the original start time and moved count.
/// O(1); allocates nothing.
export Progress.Pending(mut self, now: time.Instant) {
    if !self.active {
        self.active = true
        self.since = some(now)
        self.moved = 0
    }
}

/// Records n bytes moved while pending. Ignores n <= 0 or idle state. O(1);
/// allocates nothing.
export Progress.Moved(mut self, n: int) {
    if self.active && n > 0 {
        self.moved += n
    }
}

/// Marks the stream as having no data pending and clears its stall window.
/// O(1); allocates nothing.
export Progress.Idle(mut self) {
    self.active = false
    self.since = none
    self.moved = 0
}

/// Returns true if the stream has been continuously pending for at least
/// window and its average throughput since Pending is strictly below floor
/// bytes per second. O(1); allocates nothing.
export Progress.Starved(&self, now: time.Instant, window: time.Duration, floor: int): bool {
    if !self.active || floor <= 0 {
        return false
    }
    start := self.since else {
        return false
    }
    elapsed := now.Since(start).Nanos()
    win := window.Nanos()
    if win <= 0 || elapsed < win {
        return false
    }
    fl := i64(floor)
    secs := elapsed / NsPerSec
    ns := elapsed % NsPerSec
    if secs > (MaxI64 / fl) - 1 {
        return true
    }
    // Compare moved * 1e9 < elapsed * floor without multiplying moved by 1e9:
    // moved < ceil(elapsed * floor / 1e9).
    need := secs * fl + (ns * fl + (NsPerSec - 1)) / NsPerSec
    return i64(self.moved) < need
}

/// Tracks whether DATA was sent on a stream or connection since the previous
/// WINDOW_UPDATE, to classify unsolicited WINDOW_UPDATEs (RFC 9113 10.5).
export struct UpdateGate {
    sent: bool
}

/// An UpdateGate with no DATA sent yet. O(1); allocates nothing.
export UpdateGate.New(): UpdateGate {
    return UpdateGate(sent: false)
}

/// Records that non-empty DATA was sent on this stream or connection. O(1);
/// allocates nothing.
export UpdateGate.SentData(mut self) {
    self.sent = true
}

/// Classifies a received WINDOW_UPDATE of increment octets: returns true when
/// it should be charged to ControlBudget (no DATA went out since the previous
/// WINDOW_UPDATE, or increment < MinWindowGrant), and clears sent so each DATA
/// burst authorizes at most one large update. O(1); allocates nothing.
export UpdateGate.Charge(mut self, increment: int): bool {
    ok := self.sent && increment >= MinWindowGrant
    self.sent = false
    return !ok
}
