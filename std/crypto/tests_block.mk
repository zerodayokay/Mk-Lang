// Why: QUIC's packet keys and header protection fail silently when a byte of
// a label or a mask is wrong, so only published vectors can prove them.
// What: test blocks for Aes, ChaCha20Block and HkdfExpandLabel
// ("mk test --std std.crypto -run Block").
// How: compares hex outputs with the vectors below and checks the errors,
// the untouched output on failure and the wiped key.

// The oracles, each fetched or run on 2026-11-26, values copied from the
// text or the program's output, never typed:
// - RFC 9001 Appendix A (rfc9001.txt, sha256
//   3bbaecdf5afd278052a2c48348ce118c4ff8d0cf6b9915549858171b3f98a591): A.1's
//   HkdfLabel bytes of its five labels, its initial, client and server
//   secrets with their key, iv and hp; A.2's payload, header, sample, mask
//   and tag, A.3's payload, header and packet; A.5's key, iv, hp and ku,
//   its nonce, header, ciphertext, mask and packet;
// - RFC 8439 (rfc8439.txt, sha256
//   25bef70fbf7a07ff45c2fe4cb7c6ce954eac687413d8610603268b4e4415324c):
//   section 2.3.2's ChaCha20 block and Appendix A.1's five blocks;
// - FIPS 197 Appendix B, C.1 and C.3 as Go 1.27's crypto/aes tests hold
//   them (aes_test.go's encryptTests; the FIPS text was not fetched), and
//   Go 1.27's crypto/aes for the whole 16 bytes of A.2's and A.3's
//   AES-ECB(hp, sample), of which the RFC prints five;
// - Go's crypto/hmac with crypto/sha256 and crypto/sha512, as HKDF-Expand-
//   Label written out from RFC 8446 7.1 (rfc8446.txt, sha256
//   47871bc8820a2c3b6ea89f061055577058862cf543686b82d10131239702b3bd), for
//   the labels and contexts RFC 9001 does not reach: a 249-byte label with
//   a 255-byte context, a context of three bytes, the last 16 bytes of 255
//   SHA-256 blocks, and SHA-384.

import std.encoding

// TxBlFill copies the bytes of the hex s into out, which has their count.
TxBlFill(a: &Arena, s: str, out: mut []u8) {
    b := encoding.HexDecode(a, s).Must() // the tests' hex is valid
    out.CopyFrom(b)
}

// TxBl16 is the 16 bytes of the hex s.
TxBl16(a: &Arena, s: str): [16]u8 {
    var r: [16]u8 = [0; 16]
    TxBlFill(a, s, mut r[..])
    return r
}

// TxBl32 is the 32 bytes of the hex s.
TxBl32(a: &Arena, s: str): [32]u8 {
    var r: [32]u8 = [0; 32]
    TxBlFill(a, s, mut r[..])
    return r
}

// TxBlAes is the hex of AES(key, block) through Aes, or the error's name.
TxBlAes(a: &Arena, key: str, block: str): str from a {
    k := encoding.HexDecode(a, key).Must() // the tests' hex is valid
    b := TxBl16(a, block)
    c := Aes.New(k) else {
        return "New failed"
    }
    e := c.Encrypt(&b) else {
        return "Encrypt failed"
    }
    return TxA2Hex(a, e[..])
}

// TxBlPortable is the hex of AES(key, block) through AesSchedule and
// AesEncrypt4, the path a CPU without AES instructions runs, which the
// kernel's path hides on a CPU that has them.
TxBlPortable(a: &Arena, key: str, block: str): str from a {
    k := encoding.HexDecode(a, key).Must() // the tests' hex is valid
    var rk: [240]u8 = [0; 240]
    var sk: [240]u8 = [0; 240]
    nr := AesSchedule(k, mut rk, mut sk)
    var s: [64]u8 = [0; 64]
    TxBlFill(a, block, mut s[0..16])
    var e: [64]u8 = [0; 64]
    AesEncrypt4(sk[..], nr, s[..], mut e[..])
    return TxA2Hex(a, e[0..16])
}

// TxBlNewWhy is the error Aes.New fails with for an n-byte key, or "".
TxBlNewWhy(n: int): str {
    var k: [40]u8 = [9; 40]
    _ = Aes.New(k[0..n]) else e {
        return match e {
            BadLength { "BadLength" }
            _ { "other" }
        }
    }
    return ""
}

// TxBlBadKey reports whether c.Encrypt(b) fails with BadKey.
TxBlBadKey(c: &Aes, b: &[16]u8): bool {
    _ = c.Encrypt(b) else e {
        return match e {
            BadKey { true }
            _ { false }
        }
    }
    return false
}

// TxBlLabel is the hex of HkdfExpandLabel(prk, label, context, n bytes),
// or the error's name, or "changed" when it failed having written to out.
TxBlLabel(a: &Arena, prk: &Prk, label: str, context: []u8, n: int): str from a {
    out := a.Slice[u8](n).Must() // a test arena
    for i in 0..n {
        out[i] = 7
    }
    HkdfExpandLabel(prk, label, context, mut out[..]) else e {
        for i in 0..n {
            if out[i] != 7 {
                return "changed"
            }
        }
        return match e {
            BadLength { "BadLength" }
            BadKey { "BadKey" }
            _ { "other" }
        }
    }
    return TxA2Hex(a, out[..])
}

// TxBlSecret is HkdfExpandLabel of n bytes from prk under label with an
// empty context, as a Prk for the next step.
TxBlSecret(prk: &Prk, label: str, n: int): Prk {
    var s: [64]u8 = [0; 64]
    HkdfExpandLabel(prk, label, "".AsBytes(), mut s[0..n]).Must() // a valid label and length
    p := Prk.FromBytes(prk.kind, s[0..n]).Must()                  // n is the hash's length
    Wipe(mut s[..])
    return p
}

// TxBlInitial is RFC 9001 A.1's initial secret: HKDF-Extract(initial_salt,
// the client's Destination Connection ID 0x8394c8f03e515708).
TxBlInitial(a: &Arena): Prk {
    salt := encoding.HexDecode(a, "38762cf7f55934b34d179ae6a4c80cadccbb7f0a").Must() // valid hex
    cid := encoding.HexDecode(a, "8394c8f03e515708").Must()                          // valid hex
    return HkdfExtract(salt, cid, HashKind.Sha256)
}

// TxBlMask applies RFC 9001 5.4.1's header protection to pkt in place: the
// packet number of pn bytes at po masked by mask[1..1 + pn], and pkt[0]'s
// low four bits (a long header) or five (a short one) by mask[0].
TxBlMask(pkt: mut []u8, po: int, pn: int, mask: []u8) {
    low: u8 = if (pkt[0] & 0x80) != 0 { 0x0f } else { 0x1f }
    pkt[0] = pkt[0] ^ (mask[0] & low)
    for i in 0..pn {
        pkt[po + i] = pkt[po + i] ^ mask[1 + i]
    }
}

// TxBlNonce is RFC 9001 5.3's AEAD nonce: iv XOR the packet number pn,
// left-padded to iv's 12 bytes.
TxBlNonce(iv: &[12]u8, pn: u64): [12]u8 {
    var n := *iv
    for i in 0..8 {
        n[11 - i] = n[11 - i] ^ Trunc[u8](pn >> (8 * i))
    }
    return n
}

// TxBlKeys is a packet protection key of kn bytes, its 12-byte iv and a
// header protection key of kn bytes, derived from secret (RFC 9001 5.1).
TxBlKeys(secret: &Prk, kn: int): (key: [32]u8, iv: [12]u8, hp: [32]u8) {
    var key: [32]u8 = [0; 32]
    var iv: [12]u8 = [0; 12]
    var hp: [32]u8 = [0; 32]
    HkdfExpandLabel(secret, "quic key", "".AsBytes(), mut key[0..kn]).Must() // a valid label and length
    HkdfExpandLabel(secret, "quic iv", "".AsBytes(), mut iv[..]).Must()      // a valid label and length
    HkdfExpandLabel(secret, "quic hp", "".AsBytes(), mut hp[0..kn]).Must()   // a valid label and length
    return (key: key, iv: iv, hp: hp)
}

// TxBlInfoSame reports whether HkdfExpandLabel(prk, label, "", n) is
// HkdfExpand(prk, info, n) for the hex info, an HkdfLabel the RFC prints.
TxBlInfoSame(a: &Arena, prk: &Prk, label: str, info: str, n: int): bool {
    var x: [64]u8 = [0; 64]
    var y: [64]u8 = [0; 64]
    HkdfExpandLabel(prk, label, "".AsBytes(), mut x[0..n]).Must()           // a valid label and length
    HkdfExpand(prk, encoding.HexDecode(a, info).Must(), mut y[0..n]).Must() // valid hex and length
    return Equal(x[..], y[..])
}

// TxBlEvalBlocks is FIPS 197 C.1's and C.3's blocks through Aes, the first
// 16 bytes of RFC 8439 2.3.2's block through ChaCha20Block and RFC 9001
// A.1's client hp key through HkdfExpandLabel, in that order; the
// evaluator runs it for TxBlEval.
TxBlEvalBlocks(): [64]u8 {
    var key: [32]u8 = [0; 32]
    var pt: [16]u8 = [0; 16]
    for i in 0..32 {
        key[i] = Trunc[u8](i)
    }
    for i in 0..16 {
        pt[i] = Trunc[u8](0x11 * i)
    }
    var r: [64]u8 = [0; 64]
    c1 := Aes.New(key[0..16]).Must() // a 16-byte key
    e1 := c1.Encrypt(&pt).Must()     // a live key
    (mut r[0..16]).CopyFrom(e1[..])
    c3 := Aes.New(key[..]).Must() // a 32-byte key
    e3 := c3.Encrypt(&pt).Must()  // a live key
    (mut r[16..32]).CopyFrom(e3[..])
    var nonce: [12]u8 = [0; 12]
    nonce[3] = 0x09
    nonce[7] = 0x4a
    b := ChaCha20Block(&key, 1, &nonce)
    (mut r[32..48]).CopyFrom(b[0..16])
    salt: [20]u8 = [0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a]
    cid: [8]u8 = [0x83, 0x94, 0xc8, 0xf0, 0x3e, 0x51, 0x57, 0x08]
    init := HkdfExtract(salt[..], cid[..], HashKind.Sha256)
    var cs: [32]u8 = [0; 32]
    HkdfExpandLabel(&init, "client in", "".AsBytes(), mut cs[..]).Must() // a valid label and length
    p := Prk.FromBytes(HashKind.Sha256, cs[..]).Must()                   // 32 bytes
    HkdfExpandLabel(&p, "quic hp", "".AsBytes(), mut r[48..64]).Must()   // a valid label and length
    return r
}

// TxBlEval is TxBlEvalBlocks computed at compile time.
const TxBlEval: [64]u8 = TxBlEvalBlocks()

test "Block: compile-time evaluation runs Aes, ChaCha20Block and HkdfExpandLabel and agrees with native code" (sys: &System) {
    a := sys.arena
    Assert(TxA2Hex(a, TxBlEval[0..32]) == "69c4e0d86a7b0430d8cdb78070b4c55a8ea2b7ca516745bfeafc49904b496089")
    Assert(TxA2Hex(a, TxBlEval[32..64]) == "10f1e7e4d13b5915500fdd1fa32071c49f50449e04a0e810283a1e9933adedd2")
    run := TxBlEvalBlocks()
    Assert(Equal(run[..], TxBlEval[..]))
}

test "Block: Aes encrypts FIPS 197 Appendix B, C.1 and C.3's blocks under AES-128 and AES-256, on the kernel path and the portable one" (sys: &System) {
    a := sys.arena
    pt := "00112233445566778899aabbccddeeff"
    k128 := "000102030405060708090a0b0c0d0e0f"
    k256 := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
    Assert(TxBlAes(a, k128, pt) == "69c4e0d86a7b0430d8cdb78070b4c55a")
    Assert(TxBlAes(a, k256, pt) == "8ea2b7ca516745bfeafc49904b496089")
    Assert(TxBlPortable(a, k128, pt) == "69c4e0d86a7b0430d8cdb78070b4c55a")
    Assert(TxBlPortable(a, k256, pt) == "8ea2b7ca516745bfeafc49904b496089")
    kb := "2b7e151628aed2a6abf7158809cf4f3c"
    pb := "3243f6a8885a308d313198a2e0370734"
    Assert(TxBlAes(a, kb, pb) == "3925841d02dc09fbdc118597196a0b32")
    Assert(TxBlPortable(a, kb, pb) == "3925841d02dc09fbdc118597196a0b32")
}

test "Block: Aes gives RFC 9001 A.2 and A.3's header protection masks from the Initial hp keys and samples" (sys: &System) {
    a := sys.arena
    // A.2, the client: hp 9f50..., sample d1b1...; the mask's first five
    // bytes are the RFC's 437b9aec36.
    Assert(TxBlAes(a, "9f50449e04a0e810283a1e9933adedd2", "d1b1c98dd7689fb8ec11d242b123dc9b") ==
        "437b9aec36be423400cdd115d9db3241")
    Assert(TxBlPortable(a, "9f50449e04a0e810283a1e9933adedd2", "d1b1c98dd7689fb8ec11d242b123dc9b") ==
        "437b9aec36be423400cdd115d9db3241")
    // A.3, the server: the RFC's 2ec0d8356a.
    Assert(TxBlAes(a, "c206b8d9b9f0f37644430b490eeaa314", "2cd0991cd25b0aac406a5816b6394100") ==
        "2ec0d8356af53be647a841efa6c1ee97")
    Assert(TxBlPortable(a, "c206b8d9b9f0f37644430b490eeaa314", "2cd0991cd25b0aac406a5816b6394100") ==
        "2ec0d8356af53be647a841efa6c1ee97")
}

test "Block: Aes.New takes 16- and 32-byte keys only and fails with BadLength for any other length, AES-192's 24 included" (sys: &System) {
    for n in [0, 1, 15, 17, 24, 31, 33, 40] {
        Assert(TxBlNewWhy(n) == "BadLength")
    }
    Assert(TxBlNewWhy(16) == "")
    Assert(TxBlNewWhy(32) == "")
}

test "Block: Aes.Wipe zeroes both round key forms and Encrypt then fails with BadKey; a copy taken before is unaffected" (sys: &System) {
    var k: [32]u8 = [0x42; 32]
    for n in [16, 32] {
        var c := Aes.New(k[0..n]).Must() // a valid key length
        kept := c
        b: [16]u8 = [1; 16]
        before := c.Encrypt(&b).Must() // a live key
        c.Wipe()
        Assert(TxZero(c.rk[..]))
        Assert(TxZero(c.sk[..]))
        Assert(c.nr == 0)
        Assert(TxBlBadKey(&c, &b))
        again := kept.Encrypt(&b).Must() // the copy is live
        Assert(Equal(before[..], again[..]))
    }
}

test "Block: ChaCha20Block gives RFC 8439 2.3.2's serialized block and RFC 9001 A.5's header protection mask" (sys: &System) {
    a := sys.arena
    key := TxBl32(a, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
    var nonce: [12]u8 = [0; 12]
    TxBlFill(a, "000000090000004a00000000", mut nonce[..])
    b := ChaCha20Block(&key, 1, &nonce)
    Assert(TxA2Hex(a, b[0..32]) == "10f1e7e4d13b5915500fdd1fa32071c4c7d1f4c733c068030422aa9ac3d46c4e")
    Assert(TxA2Hex(a, b[32..64]) == "d2826446079faa0914c2d705d98b02a2b5129cd1de164eb9cbd083e8a2503c4e")
    // A.5: the counter is the sample's first four bytes, little endian,
    // and the nonce its other twelve; the mask is the block's first five.
    hp := TxBl32(a, "25a282b9e82f06f21f488917a4fc8f1b73573685608597d0efcb076b0ab7a7a4")
    sample := TxBl16(a, "5e5cd55c41f69080575d7999c25a5bfb")
    var n2: [12]u8 = [0; 12]
    (mut n2[..]).CopyFrom(sample[4..16])
    m := ChaCha20Block(&hp, Le32(sample[..], 0), &n2)
    Assert(TxA2Hex(a, m[0..5]) == "aefefe7d03")
}

test "Block: ChaCha20Block gives RFC 8439 Appendix A.1's five block function vectors" (sys: &System) {
    a := sys.arena
    var key: [32]u8 = [0; 32]
    var nonce: [12]u8 = [0; 12]
    b1 := ChaCha20Block(&key, 0, &nonce)
    Assert(TxA2Hex(a, b1[..]) ==
        "76b8e0ada0f13d90405d6ae55386bd28bdd219b8a08ded1aa836efcc8b770dc7da41597c5157488d7724e03fb8d84a376a43b8f41518a11cc387b669b2ee6586")
    b2 := ChaCha20Block(&key, 1, &nonce)
    Assert(TxA2Hex(a, b2[..]) ==
        "9f07e7be5551387a98ba977c732d080dcb0f29a048e3656912c6533e32ee7aed29b721769ce64e43d57133b074d839d531ed1f28510afb45ace10a1f4b794d6f")
    key[31] = 1
    b3 := ChaCha20Block(&key, 1, &nonce)
    Assert(TxA2Hex(a, b3[..]) ==
        "3aeb5224ecf849929b9d828db1ced4dd832025e8018b8160b82284f3c949aa5a8eca00bbb4a73bdad192b5c42f73f2fd4e273644c8b36125a64addeb006c13a0")
    key[31] = 0
    key[1] = 0xff
    b4 := ChaCha20Block(&key, 2, &nonce)
    Assert(TxA2Hex(a, b4[..]) ==
        "72d54dfbf12ec44b362692df94137f328fea8da73990265ec1bbbea1ae9af0ca13b25aa26cb4a648cb9b9d1be65b2c0924a66c54d545ec1b7374f4872e99f096")
    key[1] = 0
    nonce[11] = 2
    b5 := ChaCha20Block(&key, 0, &nonce)
    Assert(TxA2Hex(a, b5[..]) ==
        "c2c64d378cd536374ae204b9ef933fcd1a8b2288b3dfa49672ab765b54ee27c78a970e0e955c14f3a88e741b97c286f75f8fc299e8148362fa198a39531bed6d")
}

test "Block: HkdfExpandLabel expands over RFC 9001 A.1's printed HkdfLabel bytes for each of its five labels" (sys: &System) {
    a := sys.arena
    var init := TxBlInitial(a)
    var c := TxBlSecret(&init, "client in", 32)
    Assert(TxBlInfoSame(a, &init, "client in", "00200f746c73313320636c69656e7420696e00", 32))
    Assert(TxBlInfoSame(a, &init, "server in", "00200f746c7331332073657276657220696e00", 32))
    Assert(TxBlInfoSame(a, &c, "quic key", "00100e746c7331332071756963206b657900", 16))
    Assert(TxBlInfoSame(a, &c, "quic iv", "000c0d746c733133207175696320697600", 12))
    Assert(TxBlInfoSame(a, &c, "quic hp", "00100d746c733133207175696320687000", 16))
    c.Wipe()
    init.Wipe()
}

test "Block: RFC 9001 A.2's client Initial seals to the printed sample, tag and protected header through AesGcm and Aes" (sys: &System) {
    a := sys.arena
    var init := TxBlInitial(a)
    var c := TxBlSecret(&init, "client in", 32)
    k := TxBlKeys(&c, 16)
    // The CRYPTO frame, then PADDING to a 1162-byte payload.
    payload := a.Slice[u8](1162).Must() // a test arena
    TxBlFill(a, "060040f1010000ed0303ebf8fa56f12939b9584a3896472ec40bb863cfd3e86804fe3a47f06a2b69484c000004130113", mut payload[0..48])
    TxBlFill(a, "02010000c000000010000e00000b6578616d706c652e636f6dff01000100000a00080006001d00170018001000070005", mut payload[48..96])
    TxBlFill(a, "04616c706e000500050100000000003300260024001d00209370b2c9caa47fbabaf4559fedba753de171fa71f50f1ce1", mut payload[96..144])
    TxBlFill(a, "5d43e994ec74d748002b0003020304000d0010000e0403050306030203080408050806002d00020101001c0002400100", mut payload[144..192])
    TxBlFill(a, "3900320408ffffffffffffffff05048000ffff07048000ffff0801100104800075300901100f088394c8f03e51570806", mut payload[192..240])
    TxBlFill(a, "048000ffff", mut payload[240..245])
    header := encoding.HexDecode(a, "c300000001088394c8f03e5157080000449e00000002").Must() // valid hex
    pkt := a.Slice[u8](22 + 1162 + 16).Must()                                              // a test arena
    (mut pkt[0..22]).CopyFrom(header)
    g := AesGcm.New(k.key[0..16]).Must() // a 16-byte key
    nonce := TxBlNonce(&k.iv, 2)
    Assert(g.Seal(&nonce, header, payload[..], mut pkt[22..1200]).Must() == 1178) // a long enough out
    Assert(TxA2Hex(a, pkt[22..38]) == "d1b1c98dd7689fb8ec11d242b123dc9b")
    Assert(TxA2Hex(a, pkt[1184..1200]) == "e221af44860018ab0856972e194cd934")
    var sample: [16]u8 = [0; 16]
    (mut sample[..]).CopyFrom(pkt[22..38])
    var hp := Aes.New(k.hp[0..16]).Must() // a 16-byte key
    mask := hp.Encrypt(&sample).Must()    // a live key
    TxBlMask(mut pkt[..], 18, 4, mask[..])
    Assert(TxA2Hex(a, pkt[0..22]) == "c000000001088394c8f03e5157080000449e7b9aec34")
    hp.Wipe()
    c.Wipe()
    init.Wipe()
}

test "Block: RFC 9001 A.3's server Initial protects to the printed packet through AesGcm and Aes" (sys: &System) {
    a := sys.arena
    var init := TxBlInitial(a)
    var s := TxBlSecret(&init, "server in", 32)
    k := TxBlKeys(&s, 16)
    payload := a.Slice[u8](99).Must() // a test arena
    TxBlFill(a, "02000000000600405a020000560303eefce7f7b37ba1d1632e96677825ddf73988cfc79825df566dc5430b9a045a1200", mut payload[0..48])
    TxBlFill(a, "130100002e00330024001d00209d3c940d89690b84d08a60993c144eca684d1081287c834d5311bcf32bb9da1a002b00", mut payload[48..96])
    TxBlFill(a, "020304", mut payload[96..99])
    header := encoding.HexDecode(a, "c1000000010008f067a5502a4262b50040750001").Must() // valid hex
    pkt := a.Slice[u8](20 + 99 + 16).Must()                                            // a test arena
    (mut pkt[0..20]).CopyFrom(header)
    g := AesGcm.New(k.key[0..16]).Must() // a 16-byte key
    nonce := TxBlNonce(&k.iv, 1)
    Assert(g.Seal(&nonce, header, payload[..], mut pkt[20..135]).Must() == 115) // a long enough out
    var sample: [16]u8 = [0; 16]
    (mut sample[..]).CopyFrom(pkt[22..38])
    var hp := Aes.New(k.hp[0..16]).Must() // a 16-byte key
    mask := hp.Encrypt(&sample).Must()    // a live key
    TxBlMask(mut pkt[..], 18, 2, mask[..])
    Assert(TxA2Hex(a, pkt[0..64]) ==
        "cf000000010008f067a5502a4262b5004075c0d95a482cd0991cd25b0aac406a5816b6394100f37a1c69797554780bb38cc5a99f5ede4cf73c3ec2493a1839b3")
    Assert(TxA2Hex(a, pkt[64..135]) ==
        "dbcba3f6ea46c5b7684df3548e7ddeb9c3bf9c73cc3f3bded74b562bfb19fb84022f8ef4cdd93795d77d06edbb7aaf2f58891850abbdca3d20398c276456cbc42158407dd074ee")
    hp.Wipe()
    s.Wipe()
    init.Wipe()
}

test "Block: RFC 9001 A.5's short header packet protects to the printed packet through ChaCha20Poly1305 and ChaCha20Block" (sys: &System) {
    a := sys.arena
    var sec: [32]u8 = [0; 32]
    TxBlFill(a, "9ac312a7f877468ebe69422748ad00a15443f18203a07d6060f688f30f21632b", mut sec[..])
    var p := Prk.FromBytes(HashKind.Sha256, sec[..]).Must() // 32 bytes
    k := TxBlKeys(&p, 32)
    nonce := TxBlNonce(&k.iv, 654360564)
    Assert(TxA2Hex(a, nonce[..]) == "e0459b3474bdd0e46d417eb0")
    var header: [4]u8 = [0; 4]
    TxBlFill(a, "4200bff4", mut header[..])
    var pkt: [21]u8 = [0; 21]
    (mut pkt[0..4]).CopyFrom(header[..])
    plain: [1]u8 = [0x01]
    ChaCha20Poly1305.Seal(&k.key, &nonce, header[..], plain[..], mut pkt[4..21]).Must() // out is 17 bytes
    Assert(TxA2Hex(a, pkt[4..21]) == "655e5cd55c41f69080575d7999c25a5bfb")
    // The sample starts four bytes past the packet number's offset, 1.
    var n2: [12]u8 = [0; 12]
    (mut n2[..]).CopyFrom(pkt[9..21])
    m := ChaCha20Block(&k.hp, Le32(pkt[..], 5), &n2)
    TxBlMask(mut pkt[..], 1, 3, m[0..5])
    Assert(TxA2Hex(a, pkt[..]) == "4cfe4189655e5cd55c41f69080575d7999c25a5bfb")
    p.Wipe()
}

test "Block: HkdfExpandLabel derives RFC 9001 A.1's Initial secrets and their key, iv and hp for both sides" (sys: &System) {
    a := sys.arena
    var init := TxBlInitial(a)
    Assert(TxA2Hex(a, init.key[0..32]) == "7db5df06e7a69e432496adedb00851923595221596ae2ae9fb8115c1e9ed0a44")
    var c := TxBlSecret(&init, "client in", 32)
    Assert(TxA2Hex(a, c.key[0..32]) == "c00cf151ca5be075ed0ebfb5c80323c42d6b7db67881289af4008f1f6c357aea")
    Assert(TxBlLabel(a, &c, "quic key", "".AsBytes(), 16) == "1f369613dd76d5467730efcbe3b1a22d")
    Assert(TxBlLabel(a, &c, "quic iv", "".AsBytes(), 12) == "fa044b2f42a3fd3b46fb255c")
    Assert(TxBlLabel(a, &c, "quic hp", "".AsBytes(), 16) == "9f50449e04a0e810283a1e9933adedd2")
    var s := TxBlSecret(&init, "server in", 32)
    Assert(TxA2Hex(a, s.key[0..32]) == "3c199828fd139efd216c155ad844cc81fb82fa8d7446fa7d78be803acdda951b")
    Assert(TxBlLabel(a, &s, "quic key", "".AsBytes(), 16) == "cf3a5331653c364c88f0f379b6067e37")
    Assert(TxBlLabel(a, &s, "quic iv", "".AsBytes(), 12) == "0ac1493ca1905853b0bba03e")
    Assert(TxBlLabel(a, &s, "quic hp", "".AsBytes(), 16) == "c206b8d9b9f0f37644430b490eeaa314")
    c.Wipe()
    s.Wipe()
    init.Wipe()
}

test "Block: HkdfExpandLabel derives RFC 9001 A.5's key, iv, hp and ku, and matches Go on long labels, contexts, outputs and SHA-384" (sys: &System) {
    a := sys.arena
    var sec: [32]u8 = [0; 32]
    TxBlFill(a, "9ac312a7f877468ebe69422748ad00a15443f18203a07d6060f688f30f21632b", mut sec[..])
    p := Prk.FromBytes(HashKind.Sha256, sec[..]).Must() // 32 bytes
    Assert(TxBlLabel(a, &p, "quic key", "".AsBytes(), 32) == "c6d98ff3441c3fe1b2182094f69caa2ed4b716b65488960a7a984979fb23e1c8")
    Assert(TxBlLabel(a, &p, "quic iv", "".AsBytes(), 12) == "e0459b3474bdd0e44a41c144")
    Assert(TxBlLabel(a, &p, "quic hp", "".AsBytes(), 32) == "25a282b9e82f06f21f488917a4fc8f1b73573685608597d0efcb076b0ab7a7a4")
    Assert(TxBlLabel(a, &p, "quic ku", "".AsBytes(), 32) == "1223504755036d556342ee9361d253421a826c9ecdf3c7148684b36b714881f9")
    init := TxBlInitial(a)
    // The longest HkdfLabel: a 249-byte label and a 255-byte context.
    var lb: [250]u8 = [0x61; 250]
    long := str.From(lb[0..249]).Must() // ASCII
    var ctx: [256]u8 = [0; 256]
    for i in 0..256 {
        ctx[i] = Trunc[u8](i)
    }
    Assert(TxBlLabel(a, &init, long, ctx[0..255], 40) ==
        "d8e3e5d244498398c93ab0b50df28493790dfcfe788f9e87d37f38a39851e0ff4941a9516b4b0f52")
    Assert(TxBlLabel(a, &init, "c hs traffic", ctx[1..4], 32) == "354159c0b42db4373de26e24f973e6313434b59be0d111e74584a3410abe0e84")
    // The longest output, 255 SHA-256 blocks: its last 16 bytes.
    full := a.Slice[u8](255 * 32).Must()                           // a test arena
    HkdfExpandLabel(&init, "x", "".AsBytes(), mut full[..]).Must() // the longest valid output
    Assert(TxA2Hex(a, full[255 * 32 - 16..255 * 32]) == "b9ee163d94ee663eeecec808140bc834")
    salt := encoding.HexDecode(a, "38762cf7f55934b34d179ae6a4c80cadccbb7f0a").Must() // valid hex
    cid := encoding.HexDecode(a, "8394c8f03e515708").Must()                          // valid hex
    p3 := HkdfExtract(salt, cid, HashKind.Sha384)
    Assert(TxA2Hex(a, p3.key[0..48]) == "2096c726e601a7fcb1a62aed4b61f09aa4076a03ad613299514a80c0919303f30fa95c2f73fbeaa2e0d4228846a6a342")
    Assert(TxBlLabel(a, &p3, "c hs traffic", ctx[1..4], 64) ==
        "c18655196cf06614ce0ba16ce8dedc17ada949fa45146bfbf313876e13bc3a3eb727f33ffcff64a85aed18e052ad1635c9ff294ddf00f7b5361dd3ab39a553b6")
}

test "Block: HkdfExpandLabel fails with BadLength on an empty or 250-byte label, a 256-byte context or a too long output, and BadKey after Wipe, writing nothing" (sys: &System) {
    a := sys.arena
    var init := TxBlInitial(a)
    var lb: [250]u8 = [0x61; 250]
    var ctx: [256]u8 = [0; 256]
    Assert(TxBlLabel(a, &init, "", "".AsBytes(), 16) == "BadLength")
    Assert(TxBlLabel(a, &init, str.From(lb[..]).Must(), "".AsBytes(), 16) == "BadLength")     // ASCII
    Assert(TxBlLabel(a, &init, str.From(lb[0..249]).Must(), "".AsBytes(), 16) != "BadLength") // ASCII
    Assert(TxBlLabel(a, &init, "quic key", ctx[..], 16) == "BadLength")
    Assert(TxBlLabel(a, &init, "quic key", ctx[0..255], 16) != "BadLength")
    Assert(TxBlLabel(a, &init, "quic key", "".AsBytes(), 255 * 32 + 1) == "BadLength")
    init.Wipe()
    Assert(TxBlLabel(a, &init, "quic key", "".AsBytes(), 16) == "BadKey")
}
