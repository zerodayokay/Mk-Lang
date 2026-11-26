// Why: QUIC connections manage Initial, Handshake, and 1-RTT packet protection
// keys across key discards, ahead-derived key updates with 3*PTO retention,
// and AEAD confidentiality and integrity limits without leaking secrets or
// timing signals.
//
// What: KeySuiteOf, KeyRing (New, Wipe, ReinitInitial, DiscardInitial,
// DiscardHandshake, InstallSecret, SealPacket, OpenLong, OpenShort,
// InitiateKeyUpdate, CompletePeerKeyUpdate, OnAck1Rtt, ExpirePrev, Due), and
// AEAD limit test hooks.
//
// How: RFC 9001 sections 4.9, 5.1-5.7, and 6.1-6.6. Next 1-RTT receive keys
// are derived in advance upon installing or rotating current keys (preserving
// the original header protection key), old receive keys expire 3*PTO after the
// first new-phase packet opens, and all key material is wiped on discard/Wipe.

import std.crypto
import std.time
import std.tls

// AEAD confidentiality and integrity limits (RFC 9001 5.3, 6.6).
const AesGcmConfLimit: u64 = 8_388_608              // 2^23 packets sealed
const AesGcmIntegLimit: u64 = 4_503_599_627_370_496 // 2^52 failed decryptions
const ChaChaIntegLimit: u64 = 68_719_476_736        // 2^36 failed decryptions
const NoConfLimit: u64 = 4_611_686_018_427_387_903  // 2^62 - 1 (none for ChaCha20)

// Maps tls.QuicSuite to std.quic's protect.mk Suite. O(1).
KeySuiteOf(s: tls.QuicSuite): Suite {
    return match s {
        .Aes128Gcm { Suite.Aes128Gcm }
        .Aes256Gcm { Suite.Aes256Gcm }
        .Chacha20Poly1305 { Suite.Chacha20Poly1305 }
    }
}

// Wipes a HeaderKey in place.
WipeHeaderKey(hp: mut HeaderKey) {
    match hp {
        .Aes(a) { a.Wipe() }
        .ChaCha(k) { crypto.Wipe(mut k[..]) }
    }
}

// Wipes an optional Keys slot and sets it to none.
WipeOptKeys(slot: mut ?Keys) {
    match *slot {
        some(k) {
            var tmp := k
            tmp.Wipe()
        }
        none {}
    }
    *slot = none
}

// Derives next-generation Keys from suite and secret in place while preserving
// the connection's original header protection key (RFC 9001 5.4, 6.1).
DeriveNextKeepingHp(suite: Suite, secret: mut []u8, orig_hp: &HeaderKey): Error!Keys {
    var k := try NextKeys(suite, secret)
    WipeHeaderKey(mut k.hp)
    k.hp = *orig_hp
    return k
}

// Lazily heap-allocated 1-RTT key state so half-open connections stay compact.
linear struct AppKeyState {
    app_suite: Suite
    app_read_installed: bool
    app_write_installed: bool
    app_secret_len: int
    app_read_secret: [48]u8
    app_write_secret: [48]u8
    app_read_cur: ?Keys
    app_read_next: ?Keys
    app_read_prev: ?Keys
    app_write_cur: ?Keys
    app_write_next: ?Keys
}

AppKeyState.New(): AppKeyState {
    return AppKeyState(
        app_suite: Suite.Aes128Gcm,
        app_read_installed: false,
        app_write_installed: false,
        app_secret_len: 0,
        app_read_secret: [0; 48],
        app_write_secret: [0; 48],
        app_read_cur: none,
        app_read_next: none,
        app_read_prev: none,
        app_write_cur: none,
        app_write_next: none,
    )
}

AppKeyState.Wipe(self) {
    AppKeyState(
        app_suite: _,
        app_read_installed: _,
        app_write_installed: _,
        app_secret_len: _,
        app_read_secret,
        app_write_secret,
        app_read_cur,
        app_read_next,
        app_read_prev,
        app_write_cur,
        app_write_next,
    ) := self
    var arc := app_read_cur
    var arn := app_read_next
    var arp := app_read_prev
    var awc := app_write_cur
    var awn := app_write_next
    var ars := app_read_secret
    var aws := app_write_secret
    WipeOptKeys(mut arc)
    WipeOptKeys(mut arn)
    WipeOptKeys(mut arp)
    WipeOptKeys(mut awc)
    WipeOptKeys(mut awn)
    crypto.Wipe(mut ars[..])
    crypto.Wipe(mut aws[..])
}

// Per-connection packet and header protection key manager for all levels.
// Linear: Wipe must be called when the connection closes or is abandoned.
linear struct KeyRing {
    is_client: bool
    init_read: ?Keys
    init_write: ?Keys
    init_discarded: bool
    hs_read: ?Keys
    hs_write: ?Keys
    hs_discarded: bool
    app: ?PtrBox[AppKeyState]
    key_phase: bool
    updates_initiated: u64
    write_phase_min_pn: i64
    write_phase_acked: bool
    last_update_acked_at: ?time.Instant
    min_pn_cur_read: ?i64
    prev_expire_at: ?time.Instant
    handshake_confirmed: bool
    sealed_in_phase: u64
    failed_decrypts: u64
    conf_limit_override: ?u64
    integ_limit_override: ?u64
}

// Creates a KeyRing with Initial keys derived from dcid (RFC 9001 5.2). O(1).
KeyRing.New(dcid: &Cid, is_client: bool): KeyRing {
    (read: ir, write: iw) := InitialKeys(dcid, is_client)
    return KeyRing(
        is_client: is_client,
        init_read: some(ir),
        init_write: some(iw),
        init_discarded: false,
        hs_read: none,
        hs_write: none,
        hs_discarded: false,
        app: none,
        key_phase: false,
        updates_initiated: 0,
        write_phase_min_pn: 0,
        write_phase_acked: false,
        last_update_acked_at: none,
        min_pn_cur_read: none,
        prev_expire_at: none,
        handshake_confirmed: false,
        sealed_in_phase: 0,
        failed_decrypts: 0,
        conf_limit_override: none,
        integ_limit_override: none,
    )
}

// Wipes all keys and traffic secrets and consumes the KeyRing. O(1).
KeyRing.Wipe(self) {
    KeyRing(
        is_client: _,
        init_read,
        init_write,
        init_discarded: _,
        hs_read,
        hs_write,
        hs_discarded: _,
        app,
        key_phase: _,
        updates_initiated: _,
        write_phase_min_pn: _,
        write_phase_acked: _,
        last_update_acked_at: _,
        min_pn_cur_read: _,
        prev_expire_at: _,
        handshake_confirmed: _,
        sealed_in_phase: _,
        failed_decrypts: _,
        conf_limit_override: _,
        integ_limit_override: _,
    ) := self
    var ir := init_read
    var iw := init_write
    var hr := hs_read
    var hw := hs_write
    WipeOptKeys(mut ir)
    WipeOptKeys(mut iw)
    WipeOptKeys(mut hr)
    WipeOptKeys(mut hw)
    if ab := app {
        st := ab.Free()
        st.Wipe()
    }
}

// Re-derives Initial keys from the Retry SCID (RFC 9001 5.2). O(1).
KeyRing.ReinitInitial(mut self, retry_scid: &Cid) {
    WipeOptKeys(mut self.init_read)
    WipeOptKeys(mut self.init_write)
    (read: ir, write: iw) := InitialKeys(retry_scid, self.is_client)
    self.init_read = some(ir)
    self.init_write = some(iw)
    self.init_discarded = false
}

// Discards Initial keys per RFC 9001 4.9.1. O(1).
KeyRing.DiscardInitial(mut self) {
    if self.init_discarded {
        return
    }
    WipeOptKeys(mut self.init_read)
    WipeOptKeys(mut self.init_write)
    self.init_discarded = true
}

// Discards Handshake keys per RFC 9001 4.9.2. O(1).
KeyRing.DiscardHandshake(mut self) {
    if self.hs_discarded {
        return
    }
    WipeOptKeys(mut self.hs_read)
    WipeOptKeys(mut self.hs_write)
    self.hs_discarded = true
}

// Marks the handshake confirmed (enabling key updates, RFC 9001 6.1). O(1).
KeyRing.SetHandshakeConfirmed(mut self) {
    self.handshake_confirmed = true
}

// Sets a lowered confidentiality limit for testing (QS-57). O(1).
KeyRing.SetConfLimitForTest(mut self, limit: ?u64) {
    self.conf_limit_override = limit
}

// Sets a lowered integrity limit for testing (QS-58). O(1).
KeyRing.SetIntegLimitForTest(mut self, limit: ?u64) {
    self.integ_limit_override = limit
}

// Returns the active AEAD confidentiality packet limit (RFC 9001 5.3, 6.6).
KeyRing.ConfLimit(&self): u64 {
    if l := self.conf_limit_override {
        return l
    }
    return match &self.app {
        none { AesGcmConfLimit }
        some(ab) {
            match ab.Ref().app_suite {
                .Chacha20Poly1305 { NoConfLimit }
                _ { AesGcmConfLimit }
            }
        }
    }
}

// Returns the active AEAD integrity failure limit (RFC 9001 6.6).
KeyRing.IntegLimit(&self): u64 {
    if l := self.integ_limit_override {
        return l
    }
    return match &self.app {
        none { AesGcmIntegLimit }
        some(ab) {
            st := ab.Ref()
            if !st.app_read_installed && !st.app_write_installed {
                AesGcmIntegLimit
            } else {
                match st.app_suite {
                    .Chacha20Poly1305 { ChaChaIntegLimit }
                    _ { AesGcmIntegLimit }
                }
            }
        }
    }
}

// Reports whether write keys exist for space. O(1).
KeyRing.HasWriteKeys(&self, space: Space): bool {
    return match space {
        .Initial { !self.init_discarded && self.init_write.IsSome() }
        .Handshake { !self.hs_discarded && self.hs_write.IsSome() }
        .App {
            match &self.app {
                none { false }
                some(ab) { ab.Ref().app_write_installed && ab.Ref().app_write_cur.IsSome() }
            }
        }
    }
}

// Reports whether read keys exist for space. O(1).
KeyRing.HasReadKeys(&self, space: Space): bool {
    return match space {
        .Initial { !self.init_discarded && self.init_read.IsSome() }
        .Handshake { !self.hs_discarded && self.hs_read.IsSome() }
        .App {
            match &self.app {
                none { false }
                some(ab) { ab.Ref().app_read_installed && ab.Ref().app_read_cur.IsSome() }
            }
        }
    }
}

// Ensures AppKeyState box is allocated on a and returns a mutable reference.
KeyRing.EnsureApp(mut self, a: &Alloc): Error!mut AppKeyState from self {
    if self.app.IsNone() {
        var aks := AppKeyState.New()
        onerr aks.Wipe()
        box := try PtrBox[AppKeyState].New(a, aks)
        self.app = some(box)
    }
    match mut self.app {
        some(ab) { return ab.Mut() }
        none { fail OutOfMemory }
    }
}

// Installs TLS traffic secrets for Handshake or App in one direction, pre-deriving
// next-generation 1-RTT receive and send keys in advance (RFC 9001 5.1, 6.3, 9.5).
KeyRing.InstallSecret(mut self, a: &Alloc, level: Space, write: bool, suite: Suite, secret: []u8): Error!void {
    k := try KeysFrom(suite, secret)
    match level {
        .Initial {
            var tmp := k
            tmp.Wipe()
            fail TransportError(code: ErrInternal, frame: 0)
        }
        .Handshake {
            if self.hs_discarded {
                var tmp := k
                tmp.Wipe()
                return
            }
            if write {
                WipeOptKeys(mut self.hs_write)
                self.hs_write = some(k)
            } else {
                WipeOptKeys(mut self.hs_read)
                self.hs_read = some(k)
            }
        }
        .App {
            slen := SuiteSecretLen(suite)
            if secret.Len() != slen {
                var tmp := k
                tmp.Wipe()
                fail crypto.BadLength
            }
            st := self.EnsureApp(a) else err {
                var tmp := k
                tmp.Wipe()
                fail err
            }
            st.app_suite = suite
            st.app_secret_len = slen
            if write {
                WipeOptKeys(mut st.app_write_cur)
                WipeOptKeys(mut st.app_write_next)
                (mut st.app_write_secret[0..slen]).CopyFrom(secret)
                orig_hp := k.hp
                st.app_write_cur = some(k)
                st.app_write_next = some(try DeriveNextKeepingHp(suite, mut st.app_write_secret[0..slen], &orig_hp))
                st.app_write_installed = true
                self.sealed_in_phase = 0
            } else {
                WipeOptKeys(mut st.app_read_cur)
                WipeOptKeys(mut st.app_read_next)
                WipeOptKeys(mut st.app_read_prev)
                (mut st.app_read_secret[0..slen]).CopyFrom(secret)
                orig_hp := k.hp
                st.app_read_cur = some(k)
                st.app_read_next = some(try DeriveNextKeepingHp(suite, mut st.app_read_secret[0..slen], &orig_hp))
                st.app_read_installed = true
            }
        }
    }
}

// Reports whether this endpoint may initiate a key update at now (RFC 9001 6.1, 6.5).
// When for_limit is true, skips the 3*PTO cooldown SHOULD so confidentiality limits
// are never exceeded when an update is otherwise legal. O(1).
KeyRing.CanInitiateUpdate(&self, now: time.Instant, pto: time.Duration, for_limit: bool): bool {
    if !self.handshake_confirmed {
        return false
    }
    ok_app := match &self.app {
        none { false }
        some(ab) { ab.Ref().app_write_installed && ab.Ref().app_read_installed }
    }
    if !ok_app {
        return false
    }
    if self.updates_initiated > 0 && !self.write_phase_acked {
        return false
    }
    if !for_limit {
        if t := self.last_update_acked_at {
            if now.Since(t).Nanos() < 3 * pto.Nanos() {
                return false
            }
        }
    }
    return true
}

// Initiates a local key update (RFC 9001 6.1, 6.3). Rotates both write and read
// keys to the pre-derived next generation, keeps old read keys in app_read_prev
// until a new-phase packet opens + 3*PTO, and records next_write_pn as the lowest
// packet number of the new write phase. O(1).
KeyRing.InitiateKeyUpdate(mut self, next_write_pn: i64, now: time.Instant, pto: time.Duration, for_limit: bool): Error!void {
    if !self.CanInitiateUpdate(now, pto, for_limit) {
        fail TransportError(code: ErrKeyUpdate, frame: 0)
    }
    try self.RotateKeys(next_write_pn, true)
}

// Rotates both write and read keys to the next generation while keeping original
// header protection keys (RFC 9001 5.4, 6.1, 6.2).
KeyRing.RotateKeys(mut self, next_write_pn: i64, local_initiated: bool): Error!void {
    st := match mut self.app {
        some(ab) { ab.Mut() }
        none { fail TransportError(code: ErrInternal, frame: 0) }
    }
    slen := st.app_secret_len
    w_next := st.app_write_next else {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    r_next := st.app_read_next else {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    WipeOptKeys(mut st.app_write_cur)
    st.app_write_cur = some(w_next)
    st.app_write_next = none
    w_hp := w_next.hp
    st.app_write_next = some(try DeriveNextKeepingHp(st.app_suite, mut st.app_write_secret[0..slen], &w_hp))

    WipeOptKeys(mut st.app_read_prev)
    st.app_read_prev = st.app_read_cur
    st.app_read_cur = some(r_next)
    st.app_read_next = none
    r_hp := r_next.hp
    st.app_read_next = some(try DeriveNextKeepingHp(st.app_suite, mut st.app_read_secret[0..slen], &r_hp))

    self.key_phase = !self.key_phase
    self.sealed_in_phase = 0
    self.write_phase_min_pn = next_write_pn
    self.write_phase_acked = false
    self.last_update_acked_at = none
    self.min_pn_cur_read = none
    self.prev_expire_at = none
    if local_initiated {
        self.updates_initiated += 1
    } else if self.updates_initiated == 0 {
        // Peer-initiated rotation also establishes a new current write phase that
        // must be acknowledged before this side can initiate a subsequent update.
        self.updates_initiated = 1
    }
}

// Notes that a 1-RTT packet up to largest_acked was acknowledged at now, enabling
// a subsequent key update once 3*PTO passes (RFC 9001 6.1, 6.5). O(1).
KeyRing.OnAck1Rtt(mut self, largest_acked: i64, now: time.Instant) {
    if !self.write_phase_acked && largest_acked >= self.write_phase_min_pn {
        self.write_phase_acked = true
        self.last_update_acked_at = some(now)
    }
}

// Discards retained previous read keys if 3*PTO has elapsed since the first
// packet opened in the new phase (RFC 9001 6.5). O(1).
KeyRing.ExpirePrev(mut self, now: time.Instant) {
    if t := self.prev_expire_at {
        if now.Since(t).Nanos() >= 0 {
            match mut self.app {
                some(ab) { WipeOptKeys(mut ab.Mut().app_read_prev) }
                none {}
            }
            self.prev_expire_at = none
        }
    }
}

// Returns the next instant at which old read keys expire, or none. O(1).
KeyRing.Due(&self): ?time.Instant {
    return self.prev_expire_at
}

// Increments failed decryption count and fails with AEAD_LIMIT_REACHED if it
// exceeds the integrity limit (RFC 9001 6.6). O(1).
KeyRing.NoteDecryptFailure(mut self): Error!void {
    self.failed_decrypts += 1
    if self.failed_decrypts > self.IntegLimit() {
        fail TransportError(code: ErrAeadLimitReached, frame: 0)
    }
}

// Seals and header-protects a packet at space into out_pkt[0..], returning total
// packet bytes written. Automatically initiates a key update when the 1-RTT
// confidentiality limit is reached, or fails with AEAD_LIMIT_REACHED when an
// update is not allowed (RFC 9001 5.3, 6.6). O(plain.Len() + header.Len()).
KeyRing.SealPacket(
    mut self,
    space: Space,
    pn: i64,
    header_len: int,
    pn_offset: int,
    pn_len: int,
    plain: []u8,
    out_pkt: mut []u8,
    now: time.Instant,
    pto: time.Duration,
): Error!int {
    if space == Space.App {
        if self.sealed_in_phase >= self.ConfLimit() {
            if self.CanInitiateUpdate(now, pto, true) {
                try self.InitiateKeyUpdate(pn, now, pto, true)
                // Caller must update header's key_phase bit before calling or
                // rewrite bit 0x04 on out_pkt[0] before header protection!
                if self.key_phase {
                    out_pkt[0] = out_pkt[0] | 0x04
                } else {
                    out_pkt[0] = out_pkt[0] & 0xfb
                }
            } else {
                fail TransportError(code: ErrAeadLimitReached, frame: 0)
            }
        }
    }
    k := match space {
        .Initial {
            self.init_write else {
                fail TransportError(code: ErrInternal, frame: 0)
            }
        }
        .Handshake {
            self.hs_write else {
                fail TransportError(code: ErrInternal, frame: 0)
            }
        }
        .App {
            match &self.app {
                none { fail TransportError(code: ErrInternal, frame: 0) }
                some(ab) {
                    ab.Ref().app_write_cur else {
                        fail TransportError(code: ErrInternal, frame: 0)
                    }
                }
            }
        }
    }
    var aad: [256]u8 = [0; 256]
    if header_len > 256 {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    (mut aad[0..header_len]).CopyFrom(out_pkt[0..header_len])
    ct_len := Seal(&k, pn, aad[0..header_len], plain, mut out_pkt[header_len..])
    if ct_len == 0 {
        fail TransportError(code: ErrInternal, frame: 0)
    }
    total := header_len + ct_len
    Protect(&k, mut out_pkt[0..total], pn_offset, pn_len)
    if space == Space.App {
        self.sealed_in_phase += 1
    }
    return total
}

// Removes header and packet protection on an Initial or Handshake packet in pkt.
// Returns none on authentication failure (dropping the packet and counting against
// the integrity limit, RFC 9001 5.5, 6.6), or fails with PROTOCOL_VIOLATION on
// non-zero reserved bits (RFC 9000 17.2) or AEAD_LIMIT_REACHED. O(pkt.Len()).
KeyRing.OpenLong(
    mut self,
    space: Space,
    pkt: mut []u8,
    pn_offset: int,
    largest_rx_pn: i64,
    out: mut []u8,
): Error!?(pn: i64, plain_len: int) {
    k := match space {
        .Initial {
            if self.init_discarded {
                return none
            }
            self.init_read else {
                return none
            }
        }
        .Handshake {
            if self.hs_discarded {
                return none
            }
            self.hs_read else {
                return none
            }
        }
        .App {
            return none
        }
    }
    pn_len := Unprotect(&k, pkt, pn_offset) else {
        try self.NoteDecryptFailure()
        return none
    }
    if pn_offset + pn_len + 16 > pkt.Len() {
        try self.NoteDecryptFailure()
        return none
    }
    trunc := ReadPn(pkt[..], pn_offset, pn_len)
    pn := DecodePn(largest_rx_pn, trunc, pn_len * 8)
    header_len := pn_offset + pn_len
    n := Open(&k, pn, pkt[0..header_len], pkt[header_len..], out) else {
        try self.NoteDecryptFailure()
        return none
    }
    try CheckReservedBits(pkt[0], true)
    return some((pn: pn, plain_len: n))
}

// Removes header and packet protection on a 1-RTT short-header packet in pkt,
// handling key-phase selection, peer-initiated key updates (RFC 9001 6.2, 6.3),
// and KEY_UPDATE_ERROR for old keys used past a newer-key packet (6.4).
// O(pkt.Len()).
KeyRing.OpenShort(
    mut self,
    pkt: mut []u8,
    hdr: &ShortHeader,
    largest_rx_pn: i64,
    next_write_pn: i64,
    now: time.Instant,
    pto: time.Duration,
    out: mut []u8,
): Error!?(pn: i64, plain_len: int, key_updated: bool) {
    read_inst := match &self.app {
        none { false }
        some(ab0) { ab0.Ref().app_read_installed }
    }
    if !read_inst {
        return none
    }
    self.ExpirePrev(now)
    cur := match &self.app {
        none { return none }
        some(ab) {
            ab.Ref().app_read_cur else {
                return none
            }
        }
    }
    has_prev := match &self.app {
        none { false }
        some(ab) { ab.Ref().app_read_prev.IsSome() }
    }
    pn_len := Unprotect(&cur, pkt, hdr.pn_offset) else {
        try self.NoteDecryptFailure()
        return none
    }
    if hdr.pn_offset + pn_len + 16 > pkt.Len() {
        try self.NoteDecryptFailure()
        return none
    }
    trunc := ReadPn(pkt[..], hdr.pn_offset, pn_len)
    pn := DecodePn(largest_rx_pn, trunc, pn_len * 8)
    header_len := hdr.pn_offset + pn_len
    pkt_kp := (pkt[0] & 0x04) != 0

    if pkt_kp == self.key_phase {
        n := Open(&cur, pn, pkt[0..header_len], pkt[header_len..], out) else {
            try self.NoteDecryptFailure()
            return none
        }
        try CheckReservedBits(pkt[0], false)
        match self.min_pn_cur_read {
            none {
                self.min_pn_cur_read = some(pn)
                if has_prev {
                    self.prev_expire_at = some(InstantAdd(now, time.Nanos(3 * pto.Nanos())))
                }
            }
            some(m) {
                if pn < m {
                    self.min_pn_cur_read = some(pn)
                }
            }
        }
        return some((pn: pn, plain_len: n, key_updated: false))
    }

    // Key phase differs from current phase.
    use_prev := match self.min_pn_cur_read {
        none { has_prev }
        some(m) { pn < m }
    }
    if use_prev {
        prev_opt := match &self.app {
            none { none }
            some(ab) { ab.Ref().app_read_prev }
        }
        if prev := prev_opt {
            n := Open(&prev, pn, pkt[0..header_len], pkt[header_len..], out) else {
                try self.NoteDecryptFailure()
                return none
            }
            try CheckReservedBits(pkt[0], false)
            return some((pn: pn, plain_len: n, key_updated: false))
        }
        try self.NoteDecryptFailure()
        return none
    }

    // pn > min_pn_cur_read with flipped key phase: try pre-derived next keys (RFC 9001 6.3).
    nxt_opt := match &self.app {
        none { none }
        some(ab) { ab.Ref().app_read_next }
    }
    if nxt := nxt_opt {
        var opened := true
        n := Open(&nxt, pn, pkt[0..header_len], pkt[header_len..], out) else {
            opened = false
            0
        }
        if opened {
            try CheckReservedBits(pkt[0], false)
            try self.RotateKeys(next_write_pn, false)
            self.min_pn_cur_read = some(pn)
            self.prev_expire_at = some(InstantAdd(now, time.Nanos(3 * pto.Nanos())))
            return some((pn: pn, plain_len: n, key_updated: true))
        }
    }

    // Check whether an old-phase packet used a higher PN than a newer-phase packet (RFC 9001 6.4).
    prev_opt2 := match &self.app {
        none { none }
        some(ab) { ab.Ref().app_read_prev }
    }
    if prev := prev_opt2 {
        var opened_old := true
        _ = Open(&prev, pn, pkt[0..header_len], pkt[header_len..], out) else {
            opened_old = false
            0
        }
        if opened_old {
            fail TransportError(code: ErrKeyUpdate, frame: 0)
        }
    }

    try self.NoteDecryptFailure()
    return none
}
