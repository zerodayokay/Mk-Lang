// Why: QUIC servers protect against handshake flooding by issuing encrypted,
// address-bound, time-limited Retry tokens when half-open connections exceed
// retry_above, and validate Retry integrity tags without storing per-Retry state.
//
// What: RetryTokenKey, RetryTokenResult, MakeRetryToken, VerifyRetryToken, and
// WriteRetryPacket.
//
// How: RFC 9000 sections 8.1 and 17.2.5, RFC 9001 5.8. Retry tokens carry a
// 1-byte Retry marker (0x52) followed by a 12-byte random nonce and AES-128-GCM
// ciphertext binding client IP, port, issue timestamp (10 s lifetime), and
// original DCID.

import std.crypto
import std.net

// Unencrypted prefix byte distinguishing Retry-issued tokens from unknown tokens
// (RFC 9000 8.1.1, QT-273, QT-278).
const RetryTokenMarker: u8 = 0x52

// Retry token lifetime: 10 seconds (Section 3.7.2, QT-275).
const RetryTokenMaxAgeNs: i64 = 10_000_000_000

// Maximum encoded Retry token length: 1 + 12 + 28 + 20 + 16 = 77 bytes.
const MaxRetryTokenLen: int = 77

// Outcome of validating a token carried in a client Initial packet.
enum RetryTokenResult {
    Valid(odcid: Cid)
    InvalidRetry
    NotRetry
}

// Per-endpoint key for sealing and verifying Retry tokens (RFC 9000 8.1.4).
struct RetryTokenKey {
    gcm: crypto.AesGcm
}

// Creates a RetryTokenKey from 16 random bytes (QT-274). O(1).
RetryTokenKey.New(io: Io): Canceled!RetryTokenKey {
    var k: [16]u8 = [0; 16]
    try crypto.Random(io, mut k[..])
    gcm := crypto.AesGcm.New(k[..]).Must()
    crypto.Wipe(mut k[..])
    return RetryTokenKey(gcm: gcm)
}

// Wipes the underlying AES-GCM key schedule. O(1).
RetryTokenKey.Wipe(mut self) {
    self.gcm.Wipe()
}

// Seals a Retry token binding client addr, odcid, and now_ns into out[0..],
// returning bytes written (or 0 if out is too short). O(odcid.n).
MakeRetryToken(io: Io, key: &RetryTokenKey, client: &net.Addr, odcid: &Cid, now_ns: i64, out: mut []u8): int {
    plain_len := 1 + 16 + 2 + 1 + 8 + 1 + odcid.n
    total := 1 + 12 + plain_len + 16
    if out.Len() < total || odcid.n < 0 || odcid.n > MaxCidLen {
        return 0
    }
    var plain: [49]u8 = [0; 49]
    plain[0] = RetryTokenMarker
    (mut plain[1..17]).CopyFrom(client.ip[..])
    PutBe16(mut plain[..], 17, client.port)
    plain[19] = if client.v6 { 1 } else { 0 }
    PutBe32(mut plain[..], 20, Trunc[u32](Trunc[u64](now_ns) >> 32))
    PutBe32(mut plain[..], 24, Trunc[u32](Trunc[u64](now_ns)))
    plain[28] = Trunc[u8](odcid.n)
    (mut plain[29..29 + odcid.n]).CopyFrom(odcid.b[0..odcid.n])

    out[0] = RetryTokenMarker
    var nonce: [12]u8 = [0; 12]
    crypto.Random(io, mut nonce[..]) else {}
    (mut out[1..13]).CopyFrom(nonce[..])
    aad: [1]u8 = [RetryTokenMarker]
    _ = key.gcm.Seal(&nonce, aad[..], plain[0..plain_len], mut out[13..total]).Must()
    crypto.Wipe(mut plain[..])
    return total
}

// Validates token against client address, key, and max_age_ns at now_ns
// (QT-273..QT-278). O(token.Len()).
VerifyRetryToken(key: &RetryTokenKey, token: []u8, client: &net.Addr, now_ns: i64, max_age_ns: i64): RetryTokenResult {
    if token.Len() == 0 {
        return RetryTokenResult.NotRetry
    }
    if token[0] != RetryTokenMarker {
        return RetryTokenResult.NotRetry
    }
    // Minimum valid length: 1 marker + 12 nonce + 29 min plain + 16 tag = 58.
    if token.Len() < 58 || token.Len() > MaxRetryTokenLen {
        return RetryTokenResult.InvalidRetry
    }
    var nonce: [12]u8 = [0; 12]
    (mut nonce[..]).CopyFrom(token[1..13])
    aad: [1]u8 = [RetryTokenMarker]
    var plain: [49]u8 = [0; 49]
    plen := key.gcm.Open(&nonce, aad[..], token[13..], mut plain[..]) else {
        crypto.Wipe(mut plain[..])
        return RetryTokenResult.InvalidRetry
    }
    if plen < 29 || plain[0] != RetryTokenMarker {
        crypto.Wipe(mut plain[..])
        return RetryTokenResult.InvalidRetry
    }
    for i in 0..16 {
        if plain[1 + i] != client.ip[i] {
            crypto.Wipe(mut plain[..])
            return RetryTokenResult.InvalidRetry
        }
    }
    port := ReadBe16(plain[..], 17)
    v6 := plain[19] != 0
    if port != client.port || v6 != client.v6 {
        crypto.Wipe(mut plain[..])
        return RetryTokenResult.InvalidRetry
    }
    hi := Trunc[u64](ReadBe32(plain[..], 20))
    lo := Trunc[u64](ReadBe32(plain[..], 24))
    issued_ns := Trunc[i64]((hi << 32) | lo)
    age := now_ns - issued_ns
    if age < 0 || age > max_age_ns {
        crypto.Wipe(mut plain[..])
        return RetryTokenResult.InvalidRetry
    }
    odcid_len := int(plain[28])
    if odcid_len > MaxCidLen || 29 + odcid_len != plen {
        crypto.Wipe(mut plain[..])
        return RetryTokenResult.InvalidRetry
    }
    odcid := CidFromSlice(plain[29..29 + odcid_len])
    crypto.Wipe(mut plain[..])
    return RetryTokenResult.Valid(odcid: odcid)
}

// Writes a Retry packet with its 16-byte Retry Integrity Tag into out[0..]
// (RFC 9000 17.2.5, RFC 9001 5.8, QT-279). Returns bytes written, or 0 if invalid.
WriteRetryPacket(out: mut []u8, dcid: &Cid, scid: &Cid, token: []u8, odcid: &Cid): int {
    if dcid.n > MaxCidLen || scid.n > MaxCidLen || token.Len() == 0 {
        return 0
    }
    body_len := 7 + dcid.n + scid.n + token.Len()
    total := body_len + 16
    if out.Len() < total {
        return 0
    }
    out[0] = 0xc0 | (PktRetry << 4)
    PutBe32(out, 1, Version1)
    out[5] = Trunc[u8](dcid.n)
    (mut out[6..6 + dcid.n]).CopyFrom(dcid.b[0..dcid.n])
    var pos := 6 + dcid.n
    out[pos] = Trunc[u8](scid.n)
    pos += 1
    (mut out[pos..pos + scid.n]).CopyFrom(scid.b[0..scid.n])
    pos += scid.n
    (mut out[pos..pos + token.Len()]).CopyFrom(token)
    pos += token.Len()
    tag := RetryTag(odcid, out[0..pos])
    (mut out[pos..pos + 16]).CopyFrom(tag[..])
    return total
}
