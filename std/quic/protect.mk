// Why: QUIC protects packet payloads with AEAD_AES_128_GCM, AEAD_AES_256_GCM
// or AEAD_CHACHA20_POLY1305 and masks the first byte and packet number with a
// separate header protection key; Retry packets carry an AES-128-GCM tag.
//
// What: Suite, Aead, HeaderKey, Keys, InitialKeys, KeysFrom, NextKeys, Seal,
// Open, Protect, Unprotect, RetryTag, VerifyRetryTag and MinSamplePadding.
//
// How: RFC 9001 sections 5.1-5.4, 5.8 and 6.1 over std.crypto's HKDF,
// AesGcm, ChaCha20Poly1305, Aes and ChaCha20Block. All intermediate key
// material on the stack is wiped with crypto.Wipe after expansion; packet
// nonces XOR the 62-bit packet number into the 12-byte IV without branches.

import std.crypto

// Suite is the TLS 1.3 cipher suite protecting a QUIC encryption level
// (RFC 9001 5.3). Matches tls.QuicSuite's three variants.
enum Suite {
    Aes128Gcm
    Aes256Gcm
    Chacha20Poly1305
}

// Aead holds the expanded AEAD state for one direction and generation.
enum Aead {
    Aes(g: crypto.AesGcm)
    ChaCha(k: [32]u8)
}

// HeaderKey holds the header-protection cipher key for one direction.
enum HeaderKey {
    Aes(a: crypto.Aes)
    ChaCha(k: [32]u8)
}

// Keys holds the packet and header protection keys and base IV for one
// direction and key generation (RFC 9001 5.1).
struct Keys {
    suite: Suite
    aead: Aead
    iv: [12]u8
    hp: HeaderKey
}

// RFC 9001 5.2 QUIC v1 Initial salt.
const InitialSalt: [20]u8 = [0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c,
    0xad, 0xcc, 0xbb, 0x7f, 0x0a]

// RFC 9001 5.8 Retry Integrity Tag fixed key and nonce.
const RetryKey: [16]u8 = [0xbe, 0x0c, 0x69, 0x0b, 0x9f, 0x66, 0x57, 0x5a, 0x1d, 0x76, 0x6b, 0x54, 0xe3, 0x68, 0xc8, 0x4e]
const RetryNonce: [12]u8 = [0x46, 0x15, 0x99, 0xd3, 0x5d, 0x63, 0x2b, 0xf2, 0x23, 0x98, 0x25, 0xbb]

// SuiteHashKind returns the HKDF hash algorithm for s.
SuiteHashKind(s: Suite): crypto.HashKind {
    return match s {
        .Aes256Gcm { crypto.HashKind.Sha384 }
        _ { crypto.HashKind.Sha256 }
    }
}

// SuiteSecretLen returns the traffic secret length in bytes for s.
SuiteSecretLen(s: Suite): int {
    return match s {
        .Aes256Gcm { 48 }
        _ { 32 }
    }
}

// SuiteKeySize returns the AEAD and HP key length in bytes for s.
SuiteKeySize(s: Suite): int {
    return match s {
        .Aes128Gcm { 16 }
        _ { 32 }
    }
}

// Keys.Wipe clears the AEAD, IV and header protection key material. O(1).
Keys.Wipe(mut self) {
    match mut self.aead {
        .Aes(g) { g.Wipe() }
        .ChaCha(k) { crypto.Wipe(mut k[..]) }
    }
    crypto.Wipe(mut self.iv[..])
    match mut self.hp {
        .Aes(a) { a.Wipe() }
        .ChaCha(k) { crypto.Wipe(mut k[..]) }
    }
}

// InitialKeys derives the read and write Initial protection keys from the
// client's Destination Connection ID (RFC 9001 5.2). O(1); allocates nothing.
InitialKeys(dcid: &Cid, client: bool): (read: Keys, write: Keys) {
    n := if dcid.n < 0 { 0 } else if dcid.n > MaxCidLen { MaxCidLen } else { dcid.n }
    var initial_secret := crypto.HkdfExtract(InitialSalt[..], dcid.b[0..n], crypto.HashKind.Sha256)
    var client_secret: [32]u8 = [0; 32]
    var server_secret: [32]u8 = [0; 32]
    crypto.HkdfExpandLabel(&initial_secret, "client in", b"", mut client_secret[..]).Must()
    crypto.HkdfExpandLabel(&initial_secret, "server in", b"", mut server_secret[..]).Must()
    initial_secret.Wipe()
    ck := KeysFrom(Suite.Aes128Gcm, client_secret[..]).Must()
    sk := KeysFrom(Suite.Aes128Gcm, server_secret[..]).Must()
    crypto.Wipe(mut client_secret[..])
    crypto.Wipe(mut server_secret[..])
    if client {
        return (read: sk, write: ck)
    }
    return (read: ck, write: sk)
}

// KeysFrom derives packet key, IV and header protection key from secret for
// suite using HKDF-Expand-Label with "quic key", "quic iv" and "quic hp"
// (RFC 9001 5.1). Fails with crypto.BadLength when secret has the wrong
// length. O(1); allocates nothing.
KeysFrom(suite: Suite, secret: []u8): Error!Keys {
    h := SuiteHashKind(suite)
    var prk := try crypto.Prk.FromBytes(h, secret[..])
    klen := SuiteKeySize(suite)
    var key: [32]u8 = [0; 32]
    var iv: [12]u8 = [0; 12]
    var hp: [32]u8 = [0; 32]
    crypto.HkdfExpandLabel(&prk, "quic key", b"", mut key[0..klen]) else e {
        prk.Wipe()
        crypto.Wipe(mut key[..])
        fail e
    }
    crypto.HkdfExpandLabel(&prk, "quic iv", b"", mut iv[..]) else e {
        prk.Wipe()
        crypto.Wipe(mut key[..])
        crypto.Wipe(mut iv[..])
        fail e
    }
    crypto.HkdfExpandLabel(&prk, "quic hp", b"", mut hp[0..klen]) else e {
        prk.Wipe()
        crypto.Wipe(mut key[..])
        crypto.Wipe(mut iv[..])
        crypto.Wipe(mut hp[..])
        fail e
    }
    prk.Wipe()
    var aead: Aead = match suite {
        .Chacha20Poly1305 {
            var ck: [32]u8 = [0; 32]
            (mut ck[..]).CopyFrom(key[..])
            Aead.ChaCha(k: ck)
        }
        _ {
            g := crypto.AesGcm.New(key[0..klen]) else e {
                crypto.Wipe(mut key[..])
                crypto.Wipe(mut iv[..])
                crypto.Wipe(mut hp[..])
                fail e
            }
            Aead.Aes(g: g)
        }
    }
    var hkey: HeaderKey = match suite {
        .Chacha20Poly1305 {
            var hk: [32]u8 = [0; 32]
            (mut hk[..]).CopyFrom(hp[..])
            HeaderKey.ChaCha(k: hk)
        }
        _ {
            a := crypto.Aes.New(hp[0..klen]) else e {
                crypto.Wipe(mut key[..])
                crypto.Wipe(mut iv[..])
                crypto.Wipe(mut hp[..])
                fail e
            }
            HeaderKey.Aes(a: a)
        }
    }
    crypto.Wipe(mut key[..])
    crypto.Wipe(mut hp[..])
    return Keys(suite: suite, aead: aead, iv: iv, hp: hkey)
}

// NextKeys updates secret in place via HKDF-Expand-Label(secret, "quic ku",
// "", Hash.length) and returns the next generation's Keys (RFC 9001 6.1).
// Callers keep the original header protection key across key updates (5.4).
// O(1); allocates nothing.
NextKeys(suite: Suite, secret: mut []u8): Error!Keys {
    h := SuiteHashKind(suite)
    slen := SuiteSecretLen(suite)
    var prk := try crypto.Prk.FromBytes(h, secret[..])
    var next: [48]u8 = [0; 48]
    crypto.HkdfExpandLabel(&prk, "quic ku", b"", mut next[0..slen]) else e {
        prk.Wipe()
        crypto.Wipe(mut next[..])
        fail e
    }
    prk.Wipe()
    (mut secret[0..slen]).CopyFrom(next[0..slen])
    crypto.Wipe(mut next[..])
    return try KeysFrom(suite, secret[0..slen])
}

// MakeNonce XORs the 62-bit packet number left-padded to 12 bytes into iv
// (RFC 9001 5.3). Constant time; allocates nothing.
MakeNonce(iv: &[12]u8, pn: i64): [12]u8 {
    var nonce := *iv
    u := Trunc[u64](pn)
    for i in 0..8 {
        nonce[11 - i] = nonce[11 - i] ^ Trunc[u8](u >> (8 * i))
    }
    return nonce
}

// Seal encrypts plain with unprotected header as AAD under k and packet
// number pn, writing ciphertext + 16-byte tag into out and returning the
// total bytes written, or 0 if pn < 0 or out is too short (RFC 9001 5.3).
// O(plain + header); allocates nothing.
Seal(k: &Keys, pn: i64, header: []u8, plain: []u8, out: mut []u8): int {
    n := plain.Len()
    if pn < 0 || out.Len() < n + 16 {
        return 0
    }
    nonce := MakeNonce(&k.iv, pn)
    total := n + 16
    match &k.aead {
        .Aes(g) {
            _ = g.Seal(&nonce, header, plain, mut out[0..total]) else {
                return 0
            }
        }
        .ChaCha(k: key) {
            crypto.ChaCha20Poly1305.Seal(key, &nonce, header, plain, mut out[0..total]) else {
                return 0
            }
        }
    }
    return total
}

// Open authenticates and decrypts sealed (ciphertext + 16-byte tag) with
// unprotected header as AAD under k and pn, writing plaintext into out and
// returning its length (RFC 9001 5.3). Fails with crypto.BadLength or
// crypto.BadTag without modifying out on error. O(sealed + header);
// allocates nothing.
Open(k: &Keys, pn: i64, header: []u8, sealed: []u8, out: mut []u8): Error!int {
    if pn < 0 || sealed.Len() < 16 {
        fail crypto.BadLength
    }
    n := sealed.Len() - 16
    if out.Len() < n {
        fail crypto.BadLength
    }
    nonce := MakeNonce(&k.iv, pn)
    match &k.aead {
        .Aes(g) {
            _ = try g.Open(&nonce, header, sealed, mut out[0..n])
        }
        .ChaCha(k: key) {
            try crypto.ChaCha20Poly1305.Open(key, &nonce, header, sealed, mut out[0..n])
        }
    }
    return n
}

// HpMask computes the 5-byte header protection mask for sample (RFC 9001
// 5.4.3, 5.4.4). O(1); allocates nothing.
HpMask(hp: &HeaderKey, sample: &[16]u8): [5]u8 {
    var mask: [5]u8 = [0; 5]
    match hp {
        .Aes(a) {
            block := a.Encrypt(sample).Must()
            (mut mask[..]).CopyFrom(block[0..5])
        }
        .ChaCha(k: key) {
            ctr := ReadLe32(sample[..], 0)
            var nonce: [12]u8 = [0; 12]
            (mut nonce[..]).CopyFrom(sample[4..16])
            block := crypto.ChaCha20Block(key, ctr, &nonce)
            (mut mask[..]).CopyFrom(block[0..5])
        }
    }
    return mask
}

// Protect applies header protection in place to packet whose unprotected
// Packet Number starts at pn_offset with length pn_len (1..4) (RFC 9001
// 5.4.1). Does nothing if bounds do not hold a full 16-byte sample at
// pn_offset + 4. O(1); allocates nothing.
Protect(k: &Keys, packet: mut []u8, pn_offset: int, pn_len: int) {
    if pn_len < 1 || pn_len > 4 || pn_offset < 1 || packet.Len() - pn_offset < 20 {
        return
    }
    var sample: [16]u8 = [0; 16]
    (mut sample[..]).CopyFrom(packet[pn_offset + 4..pn_offset + 20])
    mask := HpMask(&k.hp, &sample)
    if (packet[0] & 0x80) != 0 {
        packet[0] = packet[0] ^ (mask[0] & 0x0f)
    } else {
        packet[0] = packet[0] ^ (mask[0] & 0x1f)
    }
    for i in 0..pn_len {
        packet[pn_offset + i] = packet[pn_offset + i] ^ mask[1 + i]
    }
}

// Unprotect removes header protection in place from packet whose Packet
// Number starts at pn_offset, returning the decoded packet number length
// (1..4), or none if packet is too short to hold the 16-byte sample at
// pn_offset + 4 (RFC 9001 5.4.1, 5.4.2). O(1); allocates nothing.
Unprotect(k: &Keys, packet: mut []u8, pn_offset: int): ?int {
    if pn_offset < 1 || packet.Len() - pn_offset < 20 {
        return none
    }
    var sample: [16]u8 = [0; 16]
    (mut sample[..]).CopyFrom(packet[pn_offset + 4..pn_offset + 20])
    mask := HpMask(&k.hp, &sample)
    if (packet[0] & 0x80) != 0 {
        packet[0] = packet[0] ^ (mask[0] & 0x0f)
    } else {
        packet[0] = packet[0] ^ (mask[0] & 0x1f)
    }
    pn_len := int(packet[0] & 0x03) + 1
    for i in 0..pn_len {
        packet[pn_offset + i] = packet[pn_offset + i] ^ mask[1 + i]
    }
    return some(pn_len)
}

// RetryTag computes the 16-byte Retry Integrity Tag for a Retry packet
// (without its trailing tag) in response to odcid (RFC 9001 5.8).
// O(retry.Len()); allocates nothing.
RetryTag(odcid: &Cid, retry: []u8): [16]u8 {
    odcid_len := if odcid.n < 0 { 0 } else if odcid.n > MaxCidLen { MaxCidLen } else { odcid.n }
    if retry.Len() > 4096 - 1 - odcid_len {
        return [0; 16]
    }
    var aad: [4096]u8 = [0; 4096]
    aad[0] = Trunc[u8](odcid_len)
    (mut aad[1..1 + odcid_len]).CopyFrom(odcid.b[0..odcid_len])
    total := 1 + odcid_len + retry.Len()
    (mut aad[1 + odcid_len..total]).CopyFrom(retry)
    var g := crypto.AesGcm.New(RetryKey[..]).Must()
    var tag: [16]u8 = [0; 16]
    _ = g.Seal(&RetryNonce, aad[0..total], b"", mut tag[..]).Must()
    g.Wipe()
    return tag
}

// VerifyRetryTag reports in constant time whether tag matches the Retry
// Integrity Tag computed for odcid and retry_without_tag (RFC 9000 17.2.5.2,
// RFC 9001 5.8). O(retry_without_tag.Len()); allocates nothing.
VerifyRetryTag(odcid: &Cid, retry_without_tag: []u8, tag: &[16]u8): bool {
    want := RetryTag(odcid, retry_without_tag)
    return crypto.Equal(want[..], tag[..])
}

// MinSamplePadding returns the number of PADDING bytes needed so that a
// packet with pn_len packet-number bytes and plain_len frame bytes produces
// enough ciphertext to contain the 16-byte header protection sample
// (RFC 9001 5.4.2). O(1).
MinSamplePadding(pn_len: int, plain_len: int): int {
    need := 4 - pn_len - plain_len
    return if need > 0 { need } else { 0 }
}
