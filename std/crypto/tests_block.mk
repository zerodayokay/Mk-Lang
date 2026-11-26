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
//   initial, client and server secrets with their key, iv and hp; the
//   five mask bytes of A.2, A.3 and A.5; A.5's key and hp;
// - RFC 8439 section 2.3.2's ChaCha20 block (rfc8439.txt, sha256
//   25bef70fbf7a07ff45c2fe4cb7c6ce954eac687413d8610603268b4e4415324c);
// - Go 1.25's crypto/aes for FIPS 197 Appendix C.1 and C.3 (the FIPS text
//   was not fetched) and for the whole 16 bytes of A.2's and A.3's
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

test "Block: Aes encrypts FIPS 197 Appendix C.1 and C.3's block under AES-128 and AES-256, on the kernel path and the portable one" (sys: &System) {
    a := sys.arena
    pt := "00112233445566778899aabbccddeeff"
    k128 := "000102030405060708090a0b0c0d0e0f"
    k256 := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
    Assert(TxBlAes(a, k128, pt) == "69c4e0d86a7b0430d8cdb78070b4c55a")
    Assert(TxBlAes(a, k256, pt) == "8ea2b7ca516745bfeafc49904b496089")
    Assert(TxBlPortable(a, k128, pt) == "69c4e0d86a7b0430d8cdb78070b4c55a")
    Assert(TxBlPortable(a, k256, pt) == "8ea2b7ca516745bfeafc49904b496089")
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

test "Block: HkdfExpandLabel derives RFC 9001 A.5's key and hp, and matches Go on long labels, contexts, outputs and SHA-384" (sys: &System) {
    a := sys.arena
    var sec: [32]u8 = [0; 32]
    TxBlFill(a, "9ac312a7f877468ebe69422748ad00a15443f18203a07d6060f688f30f21632b", mut sec[..])
    p := Prk.FromBytes(HashKind.Sha256, sec[..]).Must() // 32 bytes
    Assert(TxBlLabel(a, &p, "quic key", "".AsBytes(), 32) == "c6d98ff3441c3fe1b2182094f69caa2ed4b716b65488960a7a984979fb23e1c8")
    Assert(TxBlLabel(a, &p, "quic hp", "".AsBytes(), 32) == "25a282b9e82f06f21f488917a4fc8f1b73573685608597d0efcb076b0ab7a7a4")
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
