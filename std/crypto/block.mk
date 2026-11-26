// Why: protocols built on a raw cipher, such as QUIC's header protection
// (RFC 9001 5.4), need one AES block or one ChaCha20 block under a key of
// their own, which no mode of encryption offers.
//
// What: Aes with New, Encrypt and Wipe, the shared one-block AesBlock that
// AesGcm also uses, and ChaCha20Block.
//
// How: the AES of aes.mk and std.simd.kernel, the code AesGcm runs: the key
// schedule AesSchedule, and per block the kernel's AesEncryptBlocks with
// AesEncrypt4 as the portable fallback, which bitslices four blocks and so
// is given one block and three of zeros. Aes is a key of its own, never an
// AesGcm's: one block under a GCM key would hand out E(K, J0), which masks
// the GCM tag. ChaCha20Block is chacha20.mk's ChaBlock, serialized little
// endian (RFC 8439 2.3). Constant time as the code they reuse is: what
// branches is the key's length, the CPU's features and the wiped mark.

import std.simd.kernel

/// Aes is an AES-128 or AES-256 key for encrypting single 16-byte blocks
/// (FIPS 197's Cipher), expanded once. It is a building block for
/// protocols defined on the raw cipher, such as QUIC's header protection
/// (RFC 9001 5.4.3), and not a mode of encryption: never encrypt data
/// block by block with it. Use AesGcm for that. It uses the CPU's AES
/// instructions where they exist and a constant-time portable path where
/// they do not; both give the same bytes. It owns no memory: a copy is a
/// copy of the key, and Wipe clears only the copy it is called on. After
/// Wipe, Encrypt fails with BadKey.
export struct Aes {
    nr: int
    rk: [240]u8
    sk: [240]u8
}

/// Expands key, 16 bytes for AES-128 or 32 for AES-256, into an Aes. Fails
/// with BadLength for any other key length. O(1); allocates nothing.
export Aes.New(key: []u8): Error!Aes {
    if key.Len() != 16 && key.Len() != 32 {
        fail BadLength
    }
    var rk: [240]u8 = [0; 240]
    var sk: [240]u8 = [0; 240]
    nr := AesSchedule(key, mut rk, mut sk)
    return Aes(nr: nr, rk: rk, sk: sk)
}

/// Returns AES(K, block), block encrypted under the key. Fails with BadKey
/// after Wipe, rather than encrypt under an all-zero key. Constant time in
/// the key and the block. O(1); allocates nothing.
export Aes.Encrypt(&self, block: &[16]u8): Error![16]u8 {
    if self.nr == 0 {
        fail BadKey
    }
    return AesBlock(self.rk[..], self.sk[..], self.nr, block)
}

/// Clears the round keys with Wipe's kept stores and marks the key wiped:
/// Encrypt then fails with BadKey. O(1); allocates nothing. It cannot run
/// in compile-time evaluation (E0901), as Wipe cannot.
export Aes.Wipe(mut self) {
    Wipe(mut self.rk[..])
    Wipe(mut self.sk[..])
    self.nr = 0
}

// AesBlock is AES(K, b) for the round keys rk (FIPS 197's w, for the
// kernel) and sk (bitsliced, for AesEncrypt4) of nr rounds. The portable
// path encrypts b beside three zero blocks; their output, E(K, 0), is
// AES-GCM's hash key H, so it is wiped.
AesBlock(rk: []u8, sk: []u8, nr: int, b: &[16]u8): [16]u8 {
    var e: [64]u8 = [0; 64]
    if !kernel.AesEncryptBlocks(rk[0..16 * (nr + 1)], b[..], mut e[0..16]) {
        var s: [64]u8 = [0; 64]
        (mut s[0..16]).CopyFrom(b[..])
        AesEncrypt4(sk, nr, s[..], mut e[..])
        Wipe(mut e[16..64])
    }
    var r: [16]u8 = [0; 16]
    (mut r[..]).CopyFrom(e[0..16])
    return r
}

/// Returns the 64-byte ChaCha20 block of key, counter and nonce (RFC 8439
/// 2.3): the keystream from byte 64 * counter (2.4). Its first five bytes
/// are QUIC's ChaCha20 header protection mask (RFC 9001 5.4.4, with the
/// counter the little-endian first four bytes of the sample and the nonce
/// the other twelve). A building block for protocols, not a mode of encryption: use
/// ChaCha20Poly1305 to encrypt. Cannot fail. Constant time in key, counter
/// and nonce. O(1); allocates nothing.
export ChaCha20Block(key: &[32]u8, counter: u32, nonce: &[12]u8): [64]u8 {
    k := ChaKeyOf(key, nonce)
    ks := ChaBlock(&k, counter)
    var b: [64]u8 = [0; 64]
    for w in 0..16 {
        PutLe32(mut b[..], 4 * w, ks[w])
    }
    return b
}
