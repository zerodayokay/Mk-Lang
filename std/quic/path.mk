// Why: QUIC servers must validate peer address changes (NAT rebinding) with
// 3x amplification budgets, old-path challenges, 1200-byte MTU verification,
// congestion/RTT reset rules, and fallback on validation timeout.
//
// What: PathStep, PathState (New, OnPacket, OnResponse, OnTimeout, OnPto,
// Destination, Allowance, RecordSend, QueueChallengeResponse, TakeOwedResponse,
// TakePendingChallenge, Due).
//
// How: RFC 9000 sections 8.2 and 9. At most one pending path validation runs at
// a time, rate-limited to one new path per 3*PTO; PATH_RESPONSE echoes once on
// the path where PATH_CHALLENGE arrived.

import std.crypto
import std.net
import std.time

// Minimum path validation timeout floor (3 * kInitialRtt = 999 ms, QT-318).
const InitialPathPtoNs: i64 = 999_000_000

// Result of inspecting a received packet's source address.
enum PathStep {
    Same
    Probe
    Migrated
}

// Pending PATH_CHALLENGE validation state for a peer migration / NAT rebind.
struct PendingPath {
    new_addr: net.Addr
    challenge: [8]u8
    challenge_pending: bool
    needs_1200_reprobe: bool
    last_challenge_padded: bool
    rx_bytes: i64
    tx_bytes: i64
    deadline: time.Instant
    old_challenge: [8]u8
    old_challenge_pending: bool
    has_old_challenge: bool
}

// Queued PATH_RESPONSE to send back on the address the challenge arrived from.
struct OwedPathResponse {
    data: [8]u8
    to: net.Addr
}

// Compares two net.Addr values for exact IP and port equality (unmapped). O(1).
AddrEqual(a: &net.Addr, b: &net.Addr): bool {
    if a.port != b.port || a.v6 != b.v6 {
        return false
    }
    n := if a.v6 { 16 } else { 4 }
    for i in 0..n {
        if a.ip[i] != b.ip[i] {
            return false
        }
    }
    return true
}

// Compares only the IP addresses of a and b (ignoring port, RFC 9000 9.4). O(1).
IpEqual(a: &net.Addr, b: &net.Addr): bool {
    if a.v6 != b.v6 {
        return false
    }
    n := if a.v6 { 16 } else { 4 }
    for i in 0..n {
        if a.ip[i] != b.ip[i] {
            return false
        }
    }
    return true
}

// Per-connection path validation and NAT rebinding state.
// Plain value; allocates nothing.
struct PathState {
    validated: net.Addr
    current: net.Addr
    largest_non_probing_pn: i64
    probing: ?PendingPath
    last_new_path_at: ?time.Instant
    owed_resp: [4]OwedPathResponse
    owed_resp_len: int
}

// Creates a PathState anchored at the initial validated/current peer address. O(1).
PathState.New(peer: net.Addr): PathState {
    zero := OwedPathResponse(data: [0; 8], to: peer)
    return PathState(
        validated: peer,
        current: peer,
        largest_non_probing_pn: -1,
        probing: none,
        last_new_path_at: none,
        owed_resp: [zero; 4],
        owed_resp_len: 0,
    )
}

// Updates the initial validated address (e.g. after client Retry/Initial). O(1).
PathState.SetValidated(mut self, peer: net.Addr) {
    self.validated = peer
    self.current = peer
    self.probing = none
}

// Returns the current destination address for outgoing packets. O(1).
PathState.Destination(&self): net.Addr {
    return self.current
}

// Returns the last validated peer address. O(1).
PathState.ValidatedAddr(&self): net.Addr {
    return self.validated
}

// Returns the remaining bytes sendable to `to` under the per-path 3x
// anti-amplification rule (QT-303), or -1 when `to` is already validated. O(1).
PathState.Allowance(&self, to: net.Addr): int {
    if AddrEqual(&to, &self.validated) {
        return -1
    }
    if p := self.probing {
        if AddrEqual(&to, &p.new_addr) {
            allow := p.rx_bytes * 3 - p.tx_bytes
            return if allow <= 0 { 0 } else { int(allow) }
        }
    }
    return 0
}

// Records bytes sent to `to` against the unvalidated path amplification budget. O(1).
PathState.RecordSend(mut self, to: net.Addr, bytes: int) {
    if AddrEqual(&to, &self.validated) {
        return
    }
    if p := self.probing {
        if AddrEqual(&to, &p.new_addr) {
            var next := p
            next.tx_bytes += i64(bytes)
            self.probing = some(next)
        }
    }
}

// Queues a PATH_RESPONSE to be sent once on the path `from` (QT-312, QT-313,
// QT-316). Replaces the oldest queued response when full. O(1).
PathState.QueueChallengeResponse(mut self, data: &[8]u8, from: net.Addr) {
    if self.owed_resp_len == 4 {
        for i in 0..3 {
            self.owed_resp[i] = self.owed_resp[i + 1]
        }
        self.owed_resp_len = 3
    }
    self.owed_resp[self.owed_resp_len] = OwedPathResponse(data: *data, to: from)
    self.owed_resp_len += 1
}

// Pops the next owed PATH_RESPONSE, or none. O(1).
PathState.TakeOwedResponse(mut self): ?OwedPathResponse {
    if self.owed_resp_len == 0 {
        return none
    }
    item := self.owed_resp[0]
    for i in 1..self.owed_resp_len {
        self.owed_resp[i - 1] = self.owed_resp[i]
    }
    self.owed_resp_len -= 1
    return some(item)
}

// Processes a decrypted 1-RTT packet from `from` of `bytes` carrying `pn`
// (non_probing true when it contains any frame other than PATH_CHALLENGE,
// PATH_RESPONSE, NEW_CONNECTION_ID, PADDING). Initiates path validation on
// non-probing migration with highest PN (RFC 9000 9, 9.3, QT-299..QT-305).
PathState.OnPacket(
    mut self,
    io: Io,
    from: net.Addr,
    bytes: int,
    pn: i64,
    non_probing: bool,
    pto: time.Duration,
    now: time.Instant,
): PathStep {
    if p := self.probing {
        if AddrEqual(&from, &p.new_addr) {
            var next := p
            next.rx_bytes += i64(bytes)
            self.probing = some(next)
        }
    }
    if AddrEqual(&from, &self.current) {
        if non_probing && pn > self.largest_non_probing_pn {
            self.largest_non_probing_pn = pn
        }
        return PathStep.Same
    }
    if !non_probing || pn <= self.largest_non_probing_pn {
        return PathStep.Probe
    }
    self.largest_non_probing_pn = pn
    if AddrEqual(&from, &self.validated) {
        self.current = from
        self.probing = none
        return PathStep.Same
    }
    // Rate-limit new path validation starts to at most 1 per 3*PTO (Section 3.7.3).
    if t := self.last_new_path_at {
        if now.Since(t).Nanos() < 3 * pto.Nanos() {
            return PathStep.Probe
        }
    }
    var ch: [8]u8 = [0; 8]
    var old_ch: [8]u8 = [0; 8]
    crypto.Random(io, mut ch[..]) else {}
    crypto.Random(io, mut old_ch[..]) else {}
    eff_pto := if pto.Nanos() > InitialPathPtoNs { pto.Nanos() } else { InitialPathPtoNs }
    deadline := InstantAdd(now, time.Nanos(3 * eff_pto))
    self.current = from
    self.last_new_path_at = some(now)
    self.probing = some(PendingPath(
        new_addr: from,
        challenge: ch,
        challenge_pending: true,
        needs_1200_reprobe: false,
        last_challenge_padded: false,
        rx_bytes: i64(bytes),
        tx_bytes: 0,
        deadline: deadline,
        old_challenge: old_ch,
        old_challenge_pending: true,
        has_old_challenge: true,
    ))
    return PathStep.Migrated
}

// Processes a received PATH_RESPONSE frame (RFC 9000 8.2.2, 8.2.3, 9.3.3, 9.4).
// Accepts matching responses from any path (QT-314). Returns whether the new path
// is now validated, whether the IP changed (requiring CC/RTT reset per QT-306),
// and whether the old path won and reverted the migration.
PathState.OnResponse(
    mut self,
    io: Io,
    data: &[8]u8,
    pto: time.Duration,
    now: time.Instant,
): (validated: bool, ip_changed: bool, reverted: bool) {
    p := self.probing else {
        return (validated: false, ip_changed: false, reverted: false)
    }
    if p.has_old_challenge && crypto.Equal(p.old_challenge[..], data[..]) {
        self.current = self.validated
        self.probing = none
        return (validated: false, ip_changed: false, reverted: true)
    }
    if crypto.Equal(p.challenge[..], data[..]) {
        if !p.last_challenge_padded {
            // QT-310: first challenge could not be padded to 1200 bytes; run a
            // second validation at 1200 bytes now that the path responded.
            var ch2: [8]u8 = [0; 8]
            crypto.Random(io, mut ch2[..]) else {}
            eff_pto := if pto.Nanos() > InitialPathPtoNs { pto.Nanos() } else { InitialPathPtoNs }
            var next := p
            next.challenge = ch2
            next.challenge_pending = true
            next.needs_1200_reprobe = true
            next.last_challenge_padded = false
            next.has_old_challenge = false
            next.old_challenge_pending = false
            next.deadline = InstantAdd(now, time.Nanos(3 * eff_pto))
            self.probing = some(next)
            return (validated: false, ip_changed: false, reverted: false)
        }
        ip_changed := !IpEqual(&self.validated, &p.new_addr)
        self.validated = p.new_addr
        self.current = p.new_addr
        self.probing = none
        return (validated: true, ip_changed: ip_changed, reverted: false)
    }
    return (validated: false, ip_changed: false, reverted: false)
}

// Checks whether the active path validation expired at now (3*PTO, QT-304, QT-318).
// Returns true if validation failed and sending reverted to last validated addr.
PathState.OnTimeout(mut self, now: time.Instant): bool {
    p := self.probing else {
        return false
    }
    if now.Since(p.deadline).Nanos() >= 0 {
        self.current = self.validated
        self.probing = none
        return true
    }
    return false
}

// Re-queues pending PATH_CHALLENGE on a PTO tick (no faster than PTO, QT-318).
PathState.OnPto(mut self) {
    if p := self.probing {
        var next := p
        next.challenge_pending = true
        self.probing = some(next)
    }
}

// Returns the next pending new-path challenge if one is owed, clearing its flag.
PathState.TakeNewChallenge(mut self): ?(to: net.Addr, data: [8]u8) {
    p := self.probing else {
        return none
    }
    if !p.challenge_pending {
        return none
    }
    return some((to: p.new_addr, data: p.challenge))
}

// Marks whether the sent new-path challenge was padded to 1200 bytes (QT-309, QT-310).
PathState.MarkNewChallengeSent(mut self, padded_1200: bool) {
    if p := self.probing {
        var next := p
        next.challenge_pending = false
        next.last_challenge_padded = padded_1200
        if !padded_1200 {
            next.needs_1200_reprobe = true
        }
        self.probing = some(next)
    }
}

// Pops the pending old-path challenge if owed (QT-305).
PathState.TakeOldChallenge(mut self): ?(to: net.Addr, data: [8]u8) {
    p := self.probing else {
        return none
    }
    if !p.has_old_challenge || !p.old_challenge_pending {
        return none
    }
    var next := p
    next.old_challenge_pending = false
    self.probing = some(next)
    return some((to: self.validated, data: p.old_challenge))
}

// Returns the path-validation expiry instant if probing, or none. O(1).
PathState.Due(&self): ?time.Instant {
    if p := self.probing {
        return some(p.deadline)
    }
    return none
}
