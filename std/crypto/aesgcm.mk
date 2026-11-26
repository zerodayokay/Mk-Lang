// Why: TLS and most protocols use AES-GCM, and a record layer must not pay
// for key expansion on every message.
//
// What: AesGcm with New, Seal, Open and Wipe, over the private Ctr, Block,
// Ghash and Tag.
//
// How: AES-128-GCM and AES-256-GCM of NIST SP 800-38D with a 96-bit nonce (J0
// = nonce || 0^31 || 1, section 7.1) and a 128-bit tag, as a key object
// that holds the round keys and the GHASH table, so a TLS record pays no
// key expansion. The arguments follow ChaCha20Poly1305's order (aad
// before the text). Open computes the tag over the ciphertext first,
// compares it with Equal, and only then decrypts, so a forged message
// writes nothing.
//
// Two paths, the same bits: the portable one (aes.mk's aes_ct64
// and ghash.mk's ghash_ctmul64), always built and the reference, and
// std.simd.kernel's AesCtr, AesEncryptBlocks and GhashBlocks, each of
// which reports false, having written nothing, on a CPU without its
// instructions; each call then falls back alone, so a CPU with aes and no
// pclmul runs the AES instructions with the portable GHASH. Constant time
// on both: what branches is the lengths, the CPU's features and the
// verdict of the tag comparison.

import std.simd.kernel

/// AesGcm is an AES-GCM key, AES-128 or AES-256 with a 12-byte nonce and a
/// 16-byte tag (NIST SP 800-38D), expanded once so that each message pays
/// no key setup. It uses the CPU's AES and carry-less multiply instructions
/// where they exist and a constant-time portable path where they do not;
/// both give the same bytes. It owns no memory: a copy is a copy of the
/// key, and Wipe clears only the copy it is called on. After Wipe, Seal and
/// Open fail with BadKey.
export struct AesGcm {
    nr: int
    rk: [240]u8
    sk: [240]u8
    h: [16]u8
    powers: [64]u8
    ghni: bool
}

// GcmMax is the longest plaintext: 2^32 - 2 blocks of 16 bytes (SP 800-38D
// section 5.2.1.1, 2^39 - 256 bits), since the 32-bit counter starts at 2
// and must not wrap back onto J0.
const GcmMax: int = 68_719_476_704

/// Expands key, 16 bytes for AES-128 or 32 for AES-256, into an AesGcm.
/// Fails with BadLength for any other key length. O(1); allocates nothing.
export AesGcm.New(key: []u8): Error!AesGcm {
    if key.Len() != 16 && key.Len() != 32 {
        fail BadLength
    }
    var rk: [240]u8 = [0; 240]
    var sk: [240]u8 = [0; 240]
    nr := AesSchedule(key, mut rk, mut sk)
    var zero: [64]u8 = [0; 64]
    var e: [64]u8 = [0; 64]
    AesEncrypt4(sk[..], nr, zero[..], mut e[..])
    var h: [16]u8 = [0; 16]
    (mut h[..]).CopyFrom(e[0..16])
    var powers: [64]u8 = [0; 64]
    ghni := kernel.GhashKey(&h, mut powers)
    return AesGcm(nr: nr, rk: rk, sk: sk, h: h, powers: powers, ghni: ghni)
}

/// Encrypts plain under nonce and writes the ciphertext, then a 16-byte tag
/// over aad and the ciphertext, to the first plain.Len() + 16 bytes of out,
/// and returns that count. Fails with BadKey after Wipe, and with BadLength
/// when out is shorter or plain is over 68,719,476,704 bytes (2^32 - 2
/// blocks), writing nothing either way. Never use a nonce twice under one
/// key: Seal cannot detect it, and a repeat reveals the XOR of the two
/// plaintexts and lets an attacker forge messages. O(plain + aad);
/// allocates nothing.
export AesGcm.Seal(&self, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): Error!int {
    if self.nr == 0 {
        fail BadKey
    }
    n := plain.Len()
    if n > GcmMax || out.Len() < n + 16 {
        fail BadLength
    }
    j0 := GcmJ0(nonce)
    var ctr := j0
    ctr[15] = 2
    self.Ctr(mut ctr, plain, mut out[0..n])
    tag := self.Tag(&j0, aad, out[0..n])
    (mut out[n..n + 16]).CopyFrom(tag[..])
    return n + 16
}

/// Checks the tag at the end of sealed (ciphertext, then a 16-byte tag)
/// over aad, then writes the plaintext to the first sealed.Len() - 16 bytes
/// of out and returns that count. Fails with BadKey after Wipe; with
/// BadLength when sealed is under 16 bytes, its ciphertext is over Seal's
/// limit, or out is shorter; with BadTag when the tag does not match, found
/// before any byte of out is written. out is unchanged on every failure.
/// The tag comparison is constant time. O(sealed + aad); allocates nothing.
export AesGcm.Open(&self, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): Error!int {
    if self.nr == 0 {
        fail BadKey
    }
    if sealed.Len() < 16 {
        fail BadLength
    }
    n := sealed.Len() - 16
    if n > GcmMax || out.Len() < n {
        fail BadLength
    }
    j0 := GcmJ0(nonce)
    tag := self.Tag(&j0, aad, sealed[0..n])
    if !Equal(tag[..], sealed[n..n + 16]) {
        fail BadTag
    }
    var ctr := j0
    ctr[15] = 2
    self.Ctr(mut ctr, sealed[0..n], mut out[0..n])
    return n
}

/// Clears the round keys and the authentication key with Wipe's kept
/// stores, and marks the key wiped: Seal and Open then fail with BadKey
/// rather than run under an all-zero key. O(1); allocates nothing. It
/// cannot run in compile-time evaluation (E0901), as Wipe cannot.
export AesGcm.Wipe(mut self) {
    Wipe(mut self.rk[..])
    Wipe(mut self.sk[..])
    Wipe(mut self.h[..])
    Wipe(mut self.powers[..])
    self.nr = 0
    self.ghni = false
}

// GcmJ0 is the pre-counter block of a 96-bit nonce: nonce || 0^31 || 1.
GcmJ0(nonce: &[12]u8): [16]u8 {
    var j: [16]u8 = [0; 16]
    (mut j[0..12]).CopyFrom(nonce[..])
    j[15] = 1
    return j
}

// AesGcm.Ctr sets dst, src's length, to src XOR the keystream of the
// counter blocks from counter (GCTR, SP 800-38D section 6.5, with inc32).
AesGcm.Ctr(&self, counter: mut [16]u8, src: []u8, dst: mut []u8) {
    if kernel.AesCtr(self.rk[0..16 * (self.nr + 1)], counter, src, dst) {
        return
    }
    n := src.Len()
    var cb: [64]u8 = [0; 64]
    var ks: [64]u8 = [0; 64]
    for j in 0..4 {
        (mut cb[16 * j..16 * j + 12]).CopyFrom(counter[0..12])
    }
    var x := Be32(counter[..], 12)
    var i := 0
    for i < n {
        for j in 0..4 {
            PutBe32(mut cb[..], 16 * j + 12, x +% Trunc[u32](j))
        }
        AesEncrypt4(self.sk[..], self.nr, cb[..], mut ks[..])
        if n - i >= 64 {
            for w in 0..8 {
                PutLe64(mut dst[..], i + 8 * w, Le64(src, i + 8 * w) ^ Le64(ks[..], 8 * w))
            }
            i += 64
        } else {
            for t in 0..n - i {
                dst[i + t] = src[i + t] ^ ks[t]
            }
            i = n
        }
        x = x +% 4
    }
}

// AesGcm.Block is AES(K, b), one block (block.mk's AesBlock).
AesGcm.Block(&self, b: &[16]u8): [16]u8 {
    return AesBlock(self.rk[..], self.sk[..], self.nr, b)
}

// AesGcm.Ghash absorbs data into y, its last partial block zero-padded
// (the padding of aad and of the ciphertext in SP 800-38D section 7.1).
AesGcm.Ghash(&self, y: mut [16]u8, data: []u8) {
    whole := data.Len() / 16 * 16
    if !(self.ghni && kernel.GhashBlocks(&self.powers, y, data[0..whole])) {
        GhashPortable(&self.h, y, data[0..whole])
    }
    if whole == data.Len() {
        return
    }
    var last: [16]u8 = [0; 16]
    (mut last[0..data.Len() - whole]).CopyFrom(data[whole..data.Len()])
    if !(self.ghni && kernel.GhashBlocks(&self.powers, y, last[..])) {
        GhashPortable(&self.h, y, last[..])
    }
}

// AesGcm.Tag is the tag of ciphertext ct with associated data aad:
// AES(K, J0) XOR GHASH(aad || pad || ct || pad || bits(aad) || bits(ct)).
AesGcm.Tag(&self, j0: &[16]u8, aad: []u8, ct: []u8): [16]u8 {
    var y: [16]u8 = [0; 16]
    self.Ghash(mut y, aad)
    self.Ghash(mut y, ct)
    var lens: [16]u8 = [0; 16]
    PutBe64(mut lens[..], 0, Trunc[u64](aad.Len()) << 3)
    PutBe64(mut lens[..], 8, Trunc[u64](ct.Len()) << 3)
    self.Ghash(mut y, lens[..])
    e := self.Block(j0)
    var t: [16]u8 = [0; 16]
    for i in 0..16 {
        t[i] = e[i] ^ y[i]
    }
    return t
}
