// Why: every TLS 1.3 key comes from one key schedule; getting it exactly
// right, and wiping what it derives, is the core of the protocol's
// security.
//
// What: Extract, ExpandLabel, DeriveSecret, the traffic keys and IVs, the
// Finished value, the KeyUpdate secret, and the transcript hash with
// HelloRetryRequest's synthetic message_hash.
//
// How: RFC 8446 section 7 over std.crypto's HKDF (RFC 5869). HKDF-Extract
// is the HMAC it is defined as, so every secret is held as bytes;
// HKDF-Expand-Label builds its Prk with Prk.FromBytes and wipes it after.
// The hash is SHA-256 or SHA-384, so a secret is at most 48 bytes.

import std.crypto
import std.hash

// Suite is a TLS 1.3 cipher suite (RFC 8446 B.4): its AEAD and its hash.
enum Suite {
    // TLS_AES_128_GCM_SHA256, 0x1301
    Aes128Gcm
    // TLS_AES_256_GCM_SHA384, 0x1302
    Aes256Gcm
    // TLS_CHACHA20_POLY1305_SHA256, 0x1303
    Chacha20Poly1305
}

// SuiteCode is the suite's code point on the wire.
SuiteCode(s: Suite): u16 {
    return match s {
        .Aes128Gcm { 0x1301 }
        .Aes256Gcm { 0x1302 }
        .Chacha20Poly1305 { 0x1303 }
    }
}

// SuiteOf is the suite of a code point, none for one std.tls does not
// build.
SuiteOf(code: u16): ?Suite {
    return match code {
        0x1301 { some(Suite.Aes128Gcm) }
        0x1302 { some(Suite.Aes256Gcm) }
        0x1303 { some(Suite.Chacha20Poly1305) }
        _ { none }
    }
}

// SuiteHash is the suite's transcript and HKDF hash.
SuiteHash(s: Suite): crypto.HashKind {
    return match s {
        .Aes256Gcm { crypto.HashKind.Sha384 }
        _ { crypto.HashKind.Sha256 }
    }
}

// SuiteKeyLen is the AEAD's key length in bytes.
SuiteKeyLen(s: Suite): int {
    return match s {
        .Aes128Gcm { 16 }
        _ { 32 }
    }
}

// HashLen is the digest length of a suite hash: 32 or 48.
HashLen(h: crypto.HashKind): int {
    return match h {
        .Sha256 { 32 }
        .Sha384 { 48 }
        .Sha512 { 64 }
    }
}

// Secret is one secret of the schedule: HashLen(kind) bytes of b.
struct Secret {
    kind: crypto.HashKind
    b: [48]u8
}

// Secret.Bytes is the secret's HashLen bytes.
Secret.Bytes(&self): []u8 from self {
    return self.b[0..HashLen(self.kind)]
}

// Secret.Wipe clears the secret with crypto.Wipe's kept stores.
Secret.Wipe(mut self) {
    crypto.Wipe(mut self.b[..])
}

// Mac is HMAC under the suite hash h (h is SHA-256 or SHA-384): out gets
// HashLen(h) bytes. The key is secret; HMAC is constant time in it.
Mac(h: crypto.HashKind, key: []u8, data: []u8, out: mut []u8) {
    match h {
        .Sha384 {
            var t := crypto.Hmac384(key, data)
            (mut out[0..48]).CopyFrom(t[..])
            crypto.Wipe(mut t[..])
        }
        _ {
            var t := crypto.Hmac256(key, data)
            (mut out[0..32]).CopyFrom(t[..])
            crypto.Wipe(mut t[..])
        }
    }
}

// Extract is HKDF-Extract(salt, ikm) under h (RFC 5869 2.2): HMAC with
// the salt as its key. An empty salt keys HMAC as HashLen zero bytes do.
Extract(h: crypto.HashKind, salt: []u8, ikm: []u8): Secret {
    var s := Secret(kind: h, b: [0; 48])
    Mac(h, salt, ikm, mut s.b[..])
    return s
}

// ExpandLabel is HKDF-Expand-Label(secret, label, context, out.Len())
// (RFC 8446 7.1): HKDF-Expand over the HkdfLabel struct, length (u16),
// "tls13 " + label (u8 length) and context (u8 length). Labels are this
// module's constants, at most 12 bytes; context is a transcript hash or
// empty, at most 48; out is at most 48 bytes.
ExpandLabel(secret: &Secret, label: str, context: []u8, out: mut []u8) {
    // The secret is HashLen bytes, label is 1..12 bytes, context <= 48,
    // and out <= 48, so neither BadKey nor BadLength can happen.
    var prk := crypto.Prk.FromBytes(secret.kind, secret.Bytes()).Must()
    crypto.HkdfExpandLabel(&prk, label, context, out).Must()
    prk.Wipe()
}

// DeriveSecret is Derive-Secret(secret, label, messages) (RFC 8446 7.1)
// given the transcript hash of the messages: HKDF-Expand-Label of HashLen
// bytes.
DeriveSecret(secret: &Secret, label: str, hash: []u8): Secret {
    var s := Secret(kind: secret.kind, b: [0; 48])
    ExpandLabel(secret, label, hash, mut s.b[0..HashLen(secret.kind)])
    return s
}

// EmptyHash is the hash of no messages under h, Derive-Secret's context
// for "derived".
EmptyHash(h: crypto.HashKind, out: mut []u8) {
    match h {
        .Sha384 {
            d := hash.Sha384(b"")
            (mut out[0..48]).CopyFrom(d[..])
        }
        _ {
            d := hash.Sha256(b"")
            (mut out[0..32]).CopyFrom(d[..])
        }
    }
}

// Schedule is the secrets RFC 8446 7.1 derives in order: the early
// secret (no PSK: HKDF-Extract(0, 0)), the handshake secret over the
// (EC)DHE shared secret, and the master secret.
struct Schedule {
    kind: crypto.HashKind
    early: Secret
    handshake: Secret
    master: Secret
}

// Schedule.New is the early secret under h, with no PSK: HKDF-Extract
// with a zero salt over HashLen zero bytes.
Schedule.New(h: crypto.HashKind): Schedule {
    zero: [48]u8 = [0; 48]
    early := Extract(h, b"", zero[0..HashLen(h)])
    return Schedule(kind: h, early: early, handshake: Secret(kind: h, b: [0; 48]), master: Secret(kind: h, b: [0; 48]))
}

// Schedule.DeriveHandshake derives the handshake secret from the shared
// secret: HKDF-Extract(Derive-Secret(early, "derived", ""), shared).
Schedule.DeriveHandshake(mut self, shared: []u8) {
    var empty: [48]u8 = [0; 48]
    EmptyHash(self.kind, mut empty[..])
    n := HashLen(self.kind)
    var salt := DeriveSecret(&self.early, "derived", empty[0..n])
    self.handshake = Extract(self.kind, salt.Bytes(), shared)
    salt.Wipe()
}

// Schedule.DeriveMaster derives the master secret: HKDF-Extract(Derive-Secret(
// handshake, "derived", ""), 0).
Schedule.DeriveMaster(mut self) {
    var empty: [48]u8 = [0; 48]
    EmptyHash(self.kind, mut empty[..])
    n := HashLen(self.kind)
    var salt := DeriveSecret(&self.handshake, "derived", empty[0..n])
    zero: [48]u8 = [0; 48]
    self.master = Extract(self.kind, salt.Bytes(), zero[0..n])
    salt.Wipe()
}

// Schedule.Wipe clears every secret of the schedule.
Schedule.Wipe(mut self) {
    self.early.Wipe()
    self.handshake.Wipe()
    self.master.Wipe()
}

// TrafficKey is the AEAD key and IV of a traffic secret (RFC 8446 7.3):
// HKDF-Expand-Label(secret, "key", "", key length) and ("iv", "", 12).
// key holds SuiteKeyLen(s) bytes.
struct TrafficKey {
    suite: Suite
    key: [32]u8
    iv: [12]u8
}

// TrafficKeyOf derives the key and IV of the traffic secret t for s.
TrafficKeyOf(s: Suite, t: &Secret): TrafficKey {
    var k := TrafficKey(suite: s, key: [0; 32], iv: [0; 12])
    ExpandLabel(t, "key", b"", mut k.key[0..SuiteKeyLen(s)])
    ExpandLabel(t, "iv", b"", mut k.iv[..])
    return k
}

// TrafficKey.Wipe clears the key and the IV.
TrafficKey.Wipe(mut self) {
    crypto.Wipe(mut self.key[..])
    crypto.Wipe(mut self.iv[..])
}

// NextSecret is the next application traffic secret after a KeyUpdate
// (RFC 8446 7.2): HKDF-Expand-Label(t, "traffic upd", "", HashLen).
NextSecret(t: &Secret): Secret {
    var s := Secret(kind: t.kind, b: [0; 48])
    ExpandLabel(t, "traffic upd", b"", mut s.b[0..HashLen(t.kind)])
    return s
}

// FinishedValue writes the verify_data of a Finished message (RFC 8446
// 4.4.4) to out: HMAC(finished_key, transcript hash), finished_key =
// HKDF-Expand-Label(base, "finished", "", HashLen). out is HashLen bytes.
FinishedValue(base: &Secret, hash: []u8, out: mut []u8) {
    n := HashLen(base.kind)
    var key: [48]u8 = [0; 48]
    ExpandLabel(base, "finished", b"", mut key[0..n])
    Mac(base.kind, key[0..n], hash, mut out[0..n])
    crypto.Wipe(mut key[..])
}

// Transcript is the running hash of the handshake messages (RFC 8446
// 4.4.1). Until the server chooses a suite, both hashes run; after Choose,
// only the chosen one.
struct Transcript {
    s256: hash.Sha256State
    s384: hash.Sha384State
    known: bool
    kind: crypto.HashKind
}

// Transcript.New is the hash of no messages.
Transcript.New(): Transcript {
    return Transcript(s256: hash.Sha256State.New(), s384: hash.Sha384State.New(), known: false, kind: crypto.HashKind.Sha256)
}

// Transcript.Add absorbs one handshake message, its four-byte header
// included.
Transcript.Add(mut self, msg: []u8) {
    if !self.known || self.kind == crypto.HashKind.Sha256 {
        self.s256.Bytes(msg)
    }
    if !self.known || self.kind == crypto.HashKind.Sha384 {
        self.s384.Bytes(msg)
    }
}

// Transcript.Choose keeps only h's hash from now on.
Transcript.Choose(mut self, h: crypto.HashKind) {
    self.known = true
    self.kind = h
}

// Transcript.Sum writes the hash of the messages so far under h to out
// (HashLen(h) bytes) and keeps absorbing: Sum works on a copy of the
// state.
Transcript.Sum(&self, h: crypto.HashKind, out: mut []u8) {
    match h {
        .Sha384 {
            d := self.s384.Sum()
            (mut out[0..48]).CopyFrom(d[..])
        }
        _ {
            d := self.s256.Sum()
            (mut out[0..32]).CopyFrom(d[..])
        }
    }
}

// Transcript.Restart replaces the messages so far, the first
// ClientHello, by the synthetic message_hash message RFC 8446 4.4.1 puts
// in its place after a HelloRetryRequest: handshake type 254, a 24-bit
// length of HashLen(h), then Hash(ClientHello1). The hash h is chosen.
Transcript.Restart(mut self, h: crypto.HashKind) {
    var d: [48]u8 = [0; 48]
    self.Sum(h, mut d[..])
    n := HashLen(h)
    var t := Transcript.New()
    t.Choose(h)
    head: [4]u8 = [254, 0, 0, Trunc[u8](n)]
    t.Add(head[..])
    t.Add(d[0..n])
    *self = t
}
