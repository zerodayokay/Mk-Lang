# std.crypto

`std.crypto` has the cryptography a program needs to protect data and talk to peers. It covers authenticated encryption, signatures, key agreement, key derivation, message authentication, password hashing, secure random bytes, and parsing of DER keys. Reach for it whenever a secret or an attacker is involved. For plain digests and checksums use [`std.hash`](hash.md); for TLS connections and certificates use [`std.tls`](tls.md) and [`std.x509`](x509.md).

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.crypto
```

## Contents

- [The main pieces](#the-main-pieces)
- [Secrets, timing and random bytes](#secrets-timing-and-random-bytes)
- [Authenticated encryption](#authenticated-encryption)
- [Signatures and key agreement](#signatures-and-key-agreement)
- [Keys](#keys)
- [HMAC and HKDF](#hmac-and-hkdf)
- [Protocol building blocks](#protocol-building-blocks)
- [Passwords](#passwords)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [encrypt a message](#encrypt-a-message), [sign and verify](#sign-and-verify), [agree on a key](#agree-on-a-key), [hash a password](#hash-a-password), [sign with P-256](#sign-with-p-256)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Secure random bytes | `Random` |
| Encrypt and authenticate | `ChaCha20Poly1305.Seal` and `Open`, or `AesGcm.New`, then `Seal` and `Open` |
| Sign and verify | `Ed25519.Sign` and `Verify`; `EcdsaSign` and `EcdsaVerify` (P-256) |
| Make a signing key | `Ed25519.Keypair`, `Ed25519.FromSeed`, `EcSecret.Generate`, `EcSecret.FromBytes` |
| Agree on a shared secret | `X25519`, `EcdhShared` (P-256) |
| Verify an RSA signature | `RsaPublic.New`, `RsaVerifyPkcs1`, `RsaVerifyPss` |
| Read a DER key | `ParsePublicKey`, `ParsePrivateKey` |
| Authenticate a message with a key | `Hmac256`, `Hmac384`, `Hmac512` |
| Derive keys from a secret | `HkdfExtract`, then `HkdfExpand` |
| Build a protocol on the raw primitives (QUIC) | `Aes`, `ChaCha20Block`, `HkdfExpandLabel` |
| Hash and check a password | `Argon2id`, `Argon2idVerify` with an `Argon2Cost` |
| Compare secret bytes | `Equal` |
| Clear a secret from memory | `Wipe`, and each key object's `Wipe` |

Keys, nonces, tags and signatures are byte arrays and slices. Every output goes into an `out: mut []u8` you pass, or comes back as an array by value.

## Secrets, timing and random bytes

- `Equal`, the tag checks of `Open`, and the arithmetic on secret scalars take time independent of secret data. Lengths are public, and so are associated data, ciphertexts, tags, messages to sign, public keys and signatures: time may depend on them. Every other path that depends on a secret takes time independent of it too, except Argon2id's, which reads memory at indices derived from the password, as RFC 9106 designs it. Verification functions, whose inputs are all public, run in variable time.
- Compare tags, MACs and other secret-derived bytes with `Equal`, never with a loop that stops at the first difference.
- `Random` fills a buffer from the operating system's secure generator (`getrandom` on Linux, `ProcessPrng` on Windows) through `Io.Random`. No generator of std's own runs in between, so std holds no state to leak or to repeat after a fork. Under a simulated `Io` from `std.sim` the bytes come from the simulation's seed: they repeat from run to run and are not secret.
- `Wipe` sets bytes to zero with stores the compiler keeps even when the buffer is never read again. Every key object's `Wipe` uses it. Neither runs in compile-time evaluation (E0901).
- A wiped `AesGcm`, `Aes`, `Prk` or `EcSecret` fails with `BadKey`: it never runs under the zeroed key. These types own no memory, so a copy is a copy of the key, and `Wipe` clears only the copy it is called on.

## Authenticated encryption

An AEAD encrypts a message and adds a 16-byte tag over the ciphertext and some associated data (`aad`), which is authenticated but not encrypted. `Open` checks the tag before it writes anything, so on `BadTag` the output buffer is unchanged.

| | `ChaCha20Poly1305` | `AesGcm` |
|---|---|---|
| Standard | RFC 8439 | NIST SP 800-38D |
| Key | 32 bytes, passed to each call | 16 bytes (AES-128) or 32 (AES-256), expanded once by `AesGcm.New` |
| Nonce, tag | 12 bytes, 16 bytes | 12 bytes, 16 bytes |
| `Seal` output | exactly `plain.Len() + 16` bytes | the first `plain.Len() + 16` bytes of an `out` at least that long; returns the count |
| `Open` output | exactly `sealed.Len() - 16` bytes | the first `sealed.Len() - 16` bytes of an `out` at least that long; returns the count |
| Longest message | 2^38 - 64 bytes | 2^36 - 32 bytes |

- A wrong output length, a message over the limit, or a `sealed` shorter than 16 bytes is `BadLength`, and nothing is written. `AesGcm.New` refuses any other key length with `BadLength`; there is no AES-192.
- `ChaCha20Poly1305` holds no state: call `Seal` and `Open` on the type. It needs no AES instructions and is constant time on x86_64 and aarch64.
- `AesGcm` holds the round keys and the GHASH table, so each message pays no key setup. It uses the CPU's AES and carry-less multiply instructions where they exist and a constant-time portable path where they do not; both give the same bytes.
- Never use a nonce twice under one key. `Seal` cannot detect a repeat, and a repeat reveals the XOR of the two plaintexts and lets an attacker forge messages. Use a counter, or 12 bytes from `Random` for each message.

## Signatures and key agreement

**X25519** (RFC 7748) computes a shared secret from your 32-byte secret and a peer's 32-byte public key. With the base point (9, then 31 zero bytes) it computes your public key. It clamps the secret, ignores bit 255 of the point, and returns all zeros for a low-order point, with no error. A protocol that refuses such a point, as TLS 1.3 does, compares the result with zero. Pass a shared secret through `HkdfExtract` before using it as a key.

**Ed25519** (RFC 8032) signs and verifies. A secret key is 64 bytes: the 32-byte seed, then the 32-byte public key, the layout Go and libsodium use. `FromSeed` derives it from a seed, and `Keypair` from 32 random bytes. Signing is deterministic and reads the public half without recomputing it, so the secret key must come from `FromSeed` or `Keypair`. A public half that does not belong to the seed leaks the secret scalar, which lets anyone sign with the key. `Verify` requires a canonical `S`, checks the cofactorless equation, and compares `R` by its canonical encoding.

**P-256 and P-384** (NIST curves, `Curve.P256` and `Curve.P384`):

- P-256 has key generation, ECDH (`EcdhShared`), and ECDSA signing and verification. P-384 has ECDSA verification only: a P-384 secret, ECDH or signature fails with `BadKey`.
- `EcdsaSign` derives its nonce from the secret and the digest (RFC 6979), so it needs no random source and the same digest always gives the same signature. RFC 6979's HMAC hash is SHA-384 for a 48-byte digest, SHA-512 for 64 bytes and SHA-256 otherwise.
- Sign and verify a digest, not the message: hash it first, with `hash.Sha256` for P-256. A digest longer than the curve's field is cut to its leftmost bytes. An empty digest is `BadLength` to `EcdsaSign` and false to `EcdsaVerify`.
- Signatures come in two forms, `SigFormat.Der` (the DER SEQUENCE that X.509 and TLS use, at most 72 bytes for P-256) and `SigFormat.Fixed` (`r` then `s`, 64 bytes for P-256).
- Output sizes: `EcdsaSign` writes 64 bytes `Fixed` or at most 72 `Der` into an `out` at least that long, and returns the count. `EcSecret.Public` writes the 65-byte uncompressed point into an `out` at least that long. `EcdhShared` writes the 32-byte x coordinate into an `out` of exactly 32 bytes. An `out` of the wrong size is `BadLength`, and nothing is written on any failure.
- Points are SEC 1 uncompressed (65 bytes for P-256, 97 for P-384). A compressed point, the identity and a point off the curve are `BadKey` to `EcdhShared` and false to `EcdsaVerify`.

**RSA** is signature verification only: PKCS#1 v1.5 (`RsaVerifyPkcs1`) and PSS (`RsaVerifyPss`), with SHA-256, SHA-384 or SHA-512. std.crypto does not make RSA signatures, encrypt, decrypt or generate RSA keys.

- `RsaPublic.New` takes a 2048- to 8192-bit odd modulus and an odd exponent from 3 to 2^32 - 1, else `BadKey`.
- The PSS salt length is the digest length.
- A signature not exactly the modulus's length, or not below the modulus, does not verify.

## Keys

`ParsePublicKey` and `ParsePrivateKey` read one DER key, with nothing after it. Read PEM text through `encoding.ParsePem` first. Neither panics on its input, and both use bounded memory and stack whatever the input.

- Public keys: a SubjectPublicKeyInfo of an EC (P-256 or P-384), RSA or Ed25519 key, or a bare PKCS#1 `RSAPublicKey`. The result, a `PublicKey`, views the bytes of `der`.
- Private keys: an unencrypted PKCS#8 key of P-256 or Ed25519, or a SEC 1 EC key of P-256. A P-384 private key, in either form, is `BadKey`. The secret is copied out, so `der` may be wiped after. A `PrivateKey` holds an `EcSecret` or an Ed25519 seed, which `Ed25519.FromSeed` turns into a secret key.
- An encrypted PKCS#8 key is `Encrypted`. An RSA private key is `BadKey` in PKCS#8 and `BadEncoding` as PKCS#1.
- A SEC 1 scalar may carry leading zero bytes, which are skipped; a non-zero excess byte is `BadEncoding`.

## HMAC and HKDF

`Hmac256`, `Hmac384` and `Hmac512` are HMAC (RFC 2104) over SHA-256, SHA-384 and SHA-512. They take any key and data, the empty key included, and return the tag by value. Check a received tag with `Equal`.

HKDF (RFC 5869) turns a shared secret into keys in two steps. `HkdfExtract(salt, ikm, h)` makes a pseudorandom key, a `Prk`, under the hash `h` (a `HashKind`). `HkdfExpand(&prk, info, out)` fills `out` with key material; use a different `info` for each key you derive from one `Prk`. `out` may be at most 255 times the hash's length (8,160 bytes for SHA-256), else `BadLength`.

`Prk.FromBytes` makes a `Prk` from a key that `HkdfExtract` did not make, such as a TLS 1.3 secret that an earlier `HkdfExpand` wrote (RFC 8446's Derive-Secret). It takes exactly the hash's length, else `BadLength`, and copies the key.

## Protocol building blocks

`Aes`, `ChaCha20Block` and `HkdfExpandLabel` are building blocks for protocols defined on the raw primitives, such as QUIC's packet protection (RFC 9001 section 5). They are not modes of encryption: to encrypt data, use `AesGcm` or `ChaCha20Poly1305`, which add the counter, the nonce and the tag that these leave out. A message encrypted block by block with `Aes` (ECB) shows which of its blocks repeat.

- `Aes.New(key)` expands a 16-byte (AES-128) or 32-byte (AES-256) key, else `BadLength`. `Encrypt(&block)` returns one 16-byte block encrypted under it; QUIC's AES header protection mask is `Encrypt` of the 16-byte sample (RFC 9001 5.4.3). It runs the code `AesGcm` runs, with the AES instructions where the CPU has them, but on a key of its own. No `AesGcm` hands out a raw block, since one block under a GCM key would reveal the value that masks its tags.
- `ChaCha20Block(&key, counter, &nonce)` returns the 64-byte ChaCha20 block of RFC 8439 2.3, the keystream from byte `64 * counter`. QUIC's ChaCha20 header protection mask is its first five bytes, with the sample's first four bytes, read little endian, as the counter and its other twelve as the nonce (RFC 9001 5.4.4).
- `HkdfExpandLabel(&prk, label, context, out)` is TLS 1.3's HKDF-Expand-Label (RFC 8446 7.1): `HkdfExpand` over the output length, `"tls13 "` and `label`, then `context`. QUIC derives its Initial secrets, and each level's key, IV and header protection key, with it (the labels `"client in"`, `"quic key"` and so on). The label must be 1 to 249 bytes, the context at most 255 and `out` at most 255 times the hash's length, else `BadLength`; nothing is written on failure.

## Passwords

`Argon2id` is RFC 9106's Argon2id, version 0x13, and std's one password hash. There is no PBKDF2. It fills `out` with a tag of `out.Len()` bytes from a password, a salt, an optional `secret` (the RFC's K) and optional associated data `ad` (its X). An empty `secret` or `ad` is absent. `Argon2idVerify` computes the tag again and compares it with `Equal`: a wrong password is `false`, not an error.

- `Argon2Cost` holds `memory` in KiB, `passes` and `lanes`. Each changes the tag, so store the cost and the salt beside the tag. RFC 9106 recommends 2 GiB of memory, 1 pass and 4 lanes, or, where memory is short, 64 MiB, 3 passes and 4 lanes. A 16-byte salt from `Random` is the RFC's recommendation, and any salt length the RFC allows is taken.
- A cost outside RFC 9106's limits is `BadCost`. A tag outside 4 to 2^32 - 1 bytes, or an input over 2^32 - 1 bytes, is `BadLength`. Nothing is written on failure.
- The memory, `memory` KiB rounded down to a multiple of 4 times `lanes`, comes from the `&Heap` you pass, and is wiped and freed before the call returns. The first hash and every BLAKE2b state it makes are wiped too.
- The lanes run one after another on the calling task, so `lanes` changes the tag but not the time. The call never waits: it holds the thread running the task for the whole hash.

## Errors

| Member | Meaning |
|---|---|
| `BadTag` | A sealed message does not authenticate under the key, nonce and associated data |
| `BadLength` | A key, digest, output, tag or input has the wrong length, or is past the mode's limit |
| `BadKey` | A key of the right length is refused: an EC secret that is zero or not below the order, a point not on the curve, an RSA key out of range, a curve or kind the operation does not take, or a key object used after its `Wipe` |
| `Encrypted` | A PKCS#8 private key is encrypted |
| `BadEncoding` | Bytes that should hold a key or a signature are not its encoding |
| `BadCost` | An Argon2 cost is outside RFC 9106's limits |
| `Canceled` | `Random`, `Keypair` or `EcSecret.Generate` was cancelled (from the prelude) |
| `OutOfMemory` | `Argon2id`'s heap refused its memory (from the prelude) |

`crypto.Error` is the six members of this module. It is open, so a `match` on it outside std keeps a `_` arm. The verify functions return `bool`: a bad signature is `false`, never an error.

## Memory and cost

Each function's doc states its cost and what it allocates. Everything except `Argon2id` and `Argon2idVerify` allocates nothing: results go into your buffers or come back by value. The encryption and MAC functions are O(n) in their input. Signing and key agreement cost a fixed number of scalar multiplications. RSA verification is O(k^2 log e) word operations for a k-byte modulus. Argon2id is O(memory times passes) block compressions.

## Examples

Each program runs as written with `mk run`. The first, third, fourth and fifth use random bytes, so their bytes differ from run to run; they print only what does not.

### Encrypt a message

```mk
// verdict: ok
import std.crypto

export Main(sys: &System): Canceled|crypto.Error|WriteError!void {
    var key: [32]u8 = [0; 32]
    try crypto.Random(sys.io, mut key[..]) // a fresh secret key
    defer crypto.Wipe(mut key[..])
    var nonce: [12]u8 = [0; 12]
    try crypto.Random(sys.io, mut nonce[..]) // never reuse a nonce under one key

    plain := "attack at dawn".AsBytes()
    aad := "header v1".AsBytes() // authenticated, not encrypted
    var sealed: [30]u8 = [0; 30] // plain.Len() + 16 for the tag
    try crypto.ChaCha20Poly1305.Seal(&key, &nonce, aad, plain, mut sealed[..])

    var opened: [14]u8 = [0; 14] // sealed.Len() - 16
    try crypto.ChaCha20Poly1305.Open(&key, &nonce, aad, sealed[..], mut opened[..])
    text := str.From(opened[..]) else {
        return
    }
    try sys.Print("opened: {text}\n", (text))

    sealed[3] = sealed[3] ^ 1 // one flipped bit
    crypto.ChaCha20Poly1305.Open(&key, &nonce, aad, sealed[..], mut opened[..]) else e {
        match e {
            crypto.BadTag {
                try sys.Print("tampered: BadTag\n")
                return
            }
            _ {
                fail e
            }
        }
    }
}
```

```text
$ mk run seal.mk
opened: attack at dawn
tampered: BadTag
```

- Send the nonce with the sealed message; it need not be secret, only never repeated under one key.

### Sign and verify

```mk
// verdict: ok
import std.crypto
import std.encoding

export Main(sys: &System): encoding.Error|OutOfMemory|WriteError!void {
    // RFC 8032's first test key. A real program gets its seed from
    // Ed25519.Keypair or from storage.
    seed := try encoding.HexDecode(sys.arena, "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
    var s: [32]u8 = [0; 32]
    (mut s[..]).CopyFrom(seed)
    var secret := crypto.Ed25519.FromSeed(&s) // seed, then public key
    defer crypto.Wipe(mut secret[..])
    var public: [32]u8 = [0; 32]
    (mut public[..]).CopyFrom(secret[32..64])
    try sys.Print("public {p}\n", (p: try encoding.HexEncode(sys.arena, public[..])))

    msg := "release 1.0".AsBytes()
    sig := crypto.Ed25519.Sign(&secret, msg) // deterministic: no random source
    try sys.Print("valid:     {ok}\n", (ok: crypto.Ed25519.Verify(&public, msg, &sig)))
    try sys.Print("other msg: {ok}\n", (ok: crypto.Ed25519.Verify(&public, "release 1.1".AsBytes(), &sig)))
}
```

```text
$ mk run sign.mk
public d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a
valid:     true
other msg: false
```

- The public key is the last 32 bytes of the secret key; publish it, keep the seed secret.

### Agree on a key

```mk
// verdict: ok
import std.crypto

// Derive turns an X25519 shared secret into a 32-byte session key.
Derive(shared: &[32]u8): crypto.Error![32]u8 {
    var prk := crypto.HkdfExtract("example salt".AsBytes(), shared[..], crypto.HashKind.Sha256)
    defer prk.Wipe()
    var key: [32]u8 = [0; 32]
    try crypto.HkdfExpand(&prk, "session key v1".AsBytes(), mut key[..]) // one info per key
    return key
}

export Main(sys: &System): Canceled|crypto.Error|WriteError!void {
    var base: [32]u8 = [0; 32]
    base[0] = 9 // the curve's base point

    var alice: [32]u8 = [0; 32]
    try crypto.Random(sys.io, mut alice[..])
    defer crypto.Wipe(mut alice[..])
    var bob: [32]u8 = [0; 32]
    try crypto.Random(sys.io, mut bob[..])
    defer crypto.Wipe(mut bob[..])
    alice_pub := crypto.X25519(&alice, &base) // sent to Bob
    bob_pub := crypto.X25519(&bob, &base) // sent to Alice

    a_shared := crypto.X25519(&alice, &bob_pub)
    b_shared := crypto.X25519(&bob, &alice_pub)
    zero: [32]u8 = [0; 32]
    if crypto.Equal(a_shared[..], zero[..]) {
        try sys.Print("low-order point: refuse it\n")
        return
    }
    a_key := try Derive(&a_shared)
    b_key := try Derive(&b_shared)
    try sys.Print("same session key: {same}\n", (same: crypto.Equal(a_key[..], b_key[..])))
}
```

```text
$ mk run agree.mk
same session key: true
```

- Both sides run in one program here; in a real protocol each side sends only its public key.

### Hash a password

```mk
// verdict: ok
import std.crypto

export Main(sys: &System): Canceled|crypto.BadLength|crypto.BadCost|OutOfMemory|WriteError!void {
    // A small cost so the example runs fast. For real passwords use
    // RFC 9106's setting for limited memory: 64 MiB (65536 KiB), 3 passes, 4 lanes.
    cost := crypto.Argon2Cost(memory: 1024, passes: 2, lanes: 1)
    var salt: [16]u8 = [0; 16]
    try crypto.Random(sys.io, mut salt[..]) // store the salt and cost with the tag
    var tag: [32]u8 = [0; 32]
    try crypto.Argon2id(sys.heap, cost, "correct horse".AsBytes(), salt[..], "".AsBytes(), "".AsBytes(), mut tag[..])

    good := try crypto.Argon2idVerify(sys.heap, cost, "correct horse".AsBytes(), salt[..], "".AsBytes(), "".AsBytes(), tag[..])
    bad := try crypto.Argon2idVerify(sys.heap, cost, "wrong horse".AsBytes(), salt[..], "".AsBytes(), "".AsBytes(), tag[..])
    try sys.Print("right password: {good}\nwrong password: {bad}\n", (good, bad))
}
```

```text
$ mk run password.mk
right password: true
wrong password: false
```

- The empty `secret` and `ad` arguments are absent, as RFC 9106 has it.

### Sign with P-256

```mk
// verdict: ok
import std.crypto
import std.hash

export Main(sys: &System): Canceled|crypto.Error|WriteError!void {
    var secret := try crypto.EcSecret.Generate(sys.io, crypto.Curve.P256)
    defer secret.Wipe()
    var public: [65]u8 = [0; 65]
    n := try secret.Public(mut public[..]) // 65 bytes: 0x04, x, y
    try sys.Print("public key: {n} bytes\n", (n))

    digest := hash.Sha256("invoice 42".AsBytes()) // sign the hash, not the message
    var sig: [72]u8 = [0; 72] // a Der signature is at most 72 bytes
    m := try crypto.EcdsaSign(&secret, digest[..], crypto.SigFormat.Der, mut sig[..])
    ok := crypto.EcdsaVerify(public[..], crypto.Curve.P256, digest[..], crypto.SigFormat.Der, sig[..m])
    try sys.Print("signature verifies: {ok}\n", (ok))

    other := hash.Sha256("invoice 43".AsBytes())
    bad := crypto.EcdsaVerify(public[..], crypto.Curve.P256, other[..], crypto.SigFormat.Der, sig[..m])
    try sys.Print("other digest: {bad}\n", (bad))
}
```

```text
$ mk run p256.mk
public key: 65 bytes
signature verifies: true
other digest: false
```

- `EcdsaSign` returns the signature's length; pass `sig[..m]` on, not the whole buffer.

## API

<!-- api:begin -->
Stability: Stable.

### `Aes`

```
export struct Aes
```

Aes is an AES-128 or AES-256 key for encrypting single 16-byte blocks
(FIPS 197's Cipher), expanded once. It is a building block for
protocols defined on the raw cipher, such as QUIC's header protection
(RFC 9001 5.4.3), and not a mode of encryption: never encrypt data
block by block with it. Use AesGcm for that. It uses the CPU's AES
instructions where they exist and a constant-time portable path where
they do not; both give the same bytes. It owns no memory: a copy is a
copy of the key, and Wipe clears only the copy it is called on. After
Wipe, Encrypt fails with BadKey.

### `Aes.Encrypt`

```
export Aes.Encrypt(&self, block: &[16]u8): Error![16]u8
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Returns AES(K, block), block encrypted under the key. Fails with BadKey
after Wipe, rather than encrypt under an all-zero key. Constant time in
the key and the block. O(1); allocates nothing.

### `Aes.New`

```
export Aes.New(key: []u8): Error!Aes
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Expands key, 16 bytes for AES-128 or 32 for AES-256, into an Aes. Fails
with BadLength for any other key length. O(1); allocates nothing.

### `Aes.Wipe`

```
export Aes.Wipe(mut self)
```

Clears the round keys with Wipe's kept stores and marks the key wiped:
Encrypt then fails with BadKey. O(1); allocates nothing. It cannot run
in compile-time evaluation (E0901), as Wipe cannot.

### `AesGcm`

```
export struct AesGcm
```

AesGcm is an AES-GCM key, AES-128 or AES-256 with a 12-byte nonce and a
16-byte tag (NIST SP 800-38D), expanded once so that each message pays
no key setup. It uses the CPU's AES and carry-less multiply instructions
where they exist and a constant-time portable path where they do not;
both give the same bytes. It owns no memory: a copy is a copy of the
key, and Wipe clears only the copy it is called on. After Wipe, Seal and
Open fail with BadKey.

### `AesGcm.New`

```
export AesGcm.New(key: []u8): Error!AesGcm
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Expands key, 16 bytes for AES-128 or 32 for AES-256, into an AesGcm.
Fails with BadLength for any other key length. O(1); allocates nothing.

### `AesGcm.Open`

```
export AesGcm.Open(&self, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): Error!int
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Checks the tag at the end of sealed (ciphertext, then a 16-byte tag)
over aad, then writes the plaintext to the first sealed.Len() - 16 bytes
of out and returns that count. Fails with BadKey after Wipe; with
BadLength when sealed is under 16 bytes, its ciphertext is over Seal's
limit, or out is shorter; with BadTag when the tag does not match, found
before any byte of out is written. out is unchanged on every failure.
The tag comparison is constant time. O(sealed + aad); allocates nothing.

### `AesGcm.Seal`

```
export AesGcm.Seal(&self, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): Error!int
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Encrypts plain under nonce and writes the ciphertext, then a 16-byte tag
over aad and the ciphertext, to the first plain.Len() + 16 bytes of out,
and returns that count. Fails with BadKey after Wipe, and with BadLength
when out is shorter or plain is over 68,719,476,704 bytes (2^32 - 2
blocks), writing nothing either way. Never use a nonce twice under one
key: Seal cannot detect it, and a repeat reveals the XOR of the two
plaintexts and lets an attacker forge messages. O(plain + aad);
allocates nothing.

### `AesGcm.Wipe`

```
export AesGcm.Wipe(mut self)
```

Clears the round keys and the authentication key with Wipe's kept
stores, and marks the key wiped: Seal and Open then fail with BadKey
rather than run under an all-zero key. O(1); allocates nothing. It
cannot run in compile-time evaluation (E0901), as Wipe cannot.

### `Argon2Cost`

```
export struct Argon2Cost
```

Argon2Cost is the cost of an Argon2id hash (RFC 9106): memory in
kibibytes (from 8 * lanes to 2^32 - 1), passes over it (1 to 2^32 - 1)
and lanes (1 to 2^24 - 1). Each one changes the tag, so store the cost
beside the hash. RFC 9106 recommends passes 1, lanes 4 and memory
2097152 (2 GiB), or, where memory is short, passes 3, lanes 4 and memory
65536 (64 MiB).

Fields:

- `memory: int`: The memory in kibibytes, from 8 * lanes to 2^32 - 1.
- `passes: int`: The passes over the memory, 1 to 2^32 - 1.
- `lanes: int`: The lanes, 1 to 2^24 - 1.

### `Argon2id`

```
export Argon2id(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, out: mut []u8): BadLength|BadCost|OutOfMemory!void
```

- Errors: BadLength, BadCost, OutOfMemory

Fills out with the Argon2id tag of password and salt under the cost c,
keyed by secret (RFC 9106's K) and bound to ad (its X); an empty secret
or ad is absent, as the RFC has it. The tag length is out.Len(). A salt
of 16 bytes from Random is the RFC's recommendation; any length the RFC
allows is taken. Fails with BadCost when c is outside RFC 9106's limits,
with BadLength when out is not 4 to 2^32 - 1 bytes or an input is over
2^32 - 1 bytes, and with OutOfMemory when the heap refuses the memory;
either way out is unchanged.

Not constant time: after the first half of its first pass, it reads
memory at positions derived from the password, as RFC 9106 designs
Argon2id. The memory, the first hash H_0 and every BLAKE2b state it
makes are wiped before it returns.
The lanes run one after another on the calling task, so lanes changes
the tag but not the time. The call never waits, so it holds the thread
running the calling task for the whole hash. O(m * t) block compressions
for memory m and passes t; allocates c.memory KiB from h, rounded down
to a multiple of 4 * c.lanes, and frees them before it returns.

### `Argon2idVerify`

```
export Argon2idVerify(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, tag: []u8): BadLength|BadCost|OutOfMemory!bool
```

- Errors: BadLength, BadCost, OutOfMemory

Reports whether tag is the Argon2id tag of password and salt under c,
secret and ad, the tag length being tag.Len(). It computes the tag as
Argon2id does and compares the two with Equal, in time that depends only
on the length. Fails as Argon2id does, BadLength for a tag outside 4 to
2^32 - 1 bytes included; a wrong password is false, never an error. O(m
* t) block compressions; allocates as Argon2id does, plus tag.Len()
bytes, all freed before it returns.

### `BadCost`

```
export error BadCost
```

An Argon2 cost is outside RFC 9106's limits: memory below 8 KiB per lane
or above 2^32 - 1 KiB, passes outside 1 to 2^32 - 1, or lanes outside 1
to 2^24 - 1.

### `BadEncoding`

```
export error BadEncoding
```

Bytes that should hold a key or a signature are not its encoding.

### `BadKey`

```
export error BadKey
```

A key of the right length is refused: an EC secret that is zero or not
below the group order, a public point not on the curve, an RSA modulus
outside 2048 to 8192 bits or an exponent that is even or outside 3 to
2^32 - 1, a key of a curve or kind the operation does not take, or a key
object used after its Wipe.

### `BadLength`

```
export error BadLength
```

A buffer has the wrong length for the operation: a key, digest, output,
tag or input, or a message past the mode's limit.

### `BadTag`

```
export error BadTag
```

A sealed message does not authenticate: its tag does not match the key,
nonce, associated data and ciphertext.

### `ChaCha20Block`

```
export ChaCha20Block(key: &[32]u8, counter: u32, nonce: &[12]u8): [64]u8
```

Returns the 64-byte ChaCha20 block of key, counter and nonce (RFC 8439
2.3): the keystream from byte 64 * counter (2.4), whose first bytes are QUIC's ChaCha20 header protection mask (RFC 9001
5.4.4, with the counter the little-endian first four bytes of the
sample). A building block for protocols, not a mode of encryption: use
ChaCha20Poly1305 to encrypt. Cannot fail. Constant time in key, counter
and nonce. O(1); allocates nothing.

### `ChaCha20Poly1305`

```
export struct ChaCha20Poly1305
```

ChaCha20Poly1305 is the ChaCha20-Poly1305 AEAD of RFC 8439: a 32-byte
key, a 12-byte nonce and a 16-byte tag. It holds no state: call Seal and
Open on the type. It needs no AES instructions and is constant time on
x86_64 and aarch64.

### `ChaCha20Poly1305.Open`

```
export ChaCha20Poly1305.Open(key: &[32]u8, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): Error!void
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Checks the tag at the end of sealed over aad and the ciphertext, then
writes the plaintext to out. out must be exactly sealed.Len() - 16
bytes, and sealed at least 16; otherwise BadLength. A tag that does not
match is BadTag, found before any byte of out is written, so out is
unchanged. The tag comparison is constant time. O(sealed + aad);
allocates nothing.

### `ChaCha20Poly1305.Seal`

```
export ChaCha20Poly1305.Seal(key: &[32]u8, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): Error!void
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Encrypts plain under key and nonce and writes the ciphertext, then a
16-byte tag over aad and the ciphertext, to out. out must be exactly
plain.Len() + 16 bytes and plain at most 274,877,906,880 bytes;
otherwise it fails with BadLength and writes nothing. Never use a nonce
twice under one key: Seal cannot detect it, and a repeat reveals the XOR
of the two plaintexts and lets an attacker forge messages. O(plain +
aad); allocates nothing.

### `Curve`

```
export open enum Curve
```

Curve names an elliptic curve: P256 (NIST P-256: secrets, ECDH, signing
and verification) or P384 (NIST P-384: verification only).

Variants:

- `P256`
- `P384`

### `EcSecret`

```
export struct EcSecret
```

EcSecret is an elliptic-curve secret key: a scalar d in [1, n - 1] with
its curve. Only P-256 secrets exist; asking for a P-384 one is BadKey.
It owns no memory: a copy is a copy of the secret, and Wipe clears only
the copy it is called on.

### `EcSecret.FromBytes`

```
export EcSecret.FromBytes(curve: Curve, b: []u8): Error!EcSecret
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Returns the secret whose scalar is b, big-endian (32 bytes for P-256;
SEC 1 2.3.6). Fails with BadLength when b is not 32 bytes, and with
BadKey when the scalar is zero or not below the order n, or for P384. b
is copied. O(1); allocates nothing.

### `EcSecret.Generate`

```
export EcSecret.Generate(io: Io, curve: Curve): Canceled|Error!EcSecret
```

- Errors: Canceled|Error (Canceled, BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Returns a new secret for curve from Random: 32 bytes are drawn until
they are a scalar in [1, n - 1] (FIPS 186-5 A.4.2's rejection sampling;
a draw is refused with probability below 2^-32). Fails with Canceled as
Io.Random does, and with BadKey for P384. A cancelled call keeps no
state, so it may be called again. One system call per draw; allocates
nothing.

### `EcSecret.Public`

```
export EcSecret.Public(&self, out: mut []u8): Error!int
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Writes the public key d G, an uncompressed SEC 1 point (65 bytes for
P-256), to the front of out and returns its length. Fails with
BadLength, writing nothing, when out is shorter. Constant time in the
secret: one scalar multiplication and one inversion; allocates nothing.

### `EcSecret.Wipe`

```
export EcSecret.Wipe(mut self)
```

Sets the secret's scalar to zero with Wipe, so it does not stay in
memory; the secret is then unusable (BadKey). Cannot fail. O(1);
allocates nothing.

### `EcdhShared`

```
export EcdhShared(curve: Curve, secret: &EcSecret, peer: []u8, out: mut []u8): Error!void
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Writes the ECDH shared secret of secret and the peer's public key (an
uncompressed SEC 1 point) to out: the x coordinate of d Q, 32 bytes for
P-256 (SEC 1 3.3.1). Pass it through HkdfExtract before using it as a
key. Fails with BadKey when curve or the secret's curve is not P-256,
when peer is not an uncompressed point on the curve, or when the secret
was wiped; with BadLength when out is not 32 bytes. Writes nothing on
failure. Constant time in the secret: one scalar multiplication and one
inversion; allocates nothing.

### `EcdsaSign`

```
export EcdsaSign(secret: &EcSecret, digest: []u8, format: SigFormat, out: mut []u8): Error!int
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Writes the ECDSA signature (FIPS 186-5) of digest under secret, in
format, to the front of out and returns its length: 64 bytes Fixed, at
most 72 Der. digest is the hash of the message, not the message: hash it
first, with hash.Sha256 for P-256.

The nonce is derived from the secret and the digest (RFC 6979), so
signing needs no random source and the same digest always gives the same
signature. RFC 6979's HMAC hash is SHA-384 for a 48-byte digest, SHA-512
for 64 bytes and SHA-256 otherwise; a digest longer than 32 bytes is cut
to its first 32. Fails with BadLength for an empty digest or an out
shorter than the signature, writing nothing, and with BadKey for a P-384
or wiped secret. Constant time in the secret and the nonce: one scalar
multiplication and two inversions; allocates nothing.

### `EcdsaVerify`

```
export EcdsaVerify(public: []u8, curve: Curve, digest: []u8, format: SigFormat, sig: []u8): bool
```

Reports whether sig, in format, is a valid ECDSA signature (FIPS 186-5)
of digest under public, an uncompressed SEC 1 point (65 bytes for P-256,
97 for P-384) on curve. A digest longer than the curve's field is cut to
its leftmost bytes. False, never an error, for an empty digest, a point
off the curve or in another form, a signature not in format's exact
encoding, or r or s outside [1, n - 1]. Variable time, since every input
is public: one double multiplication and one inversion; allocates
nothing.

### `Ed25519`

```
export struct Ed25519
```

Ed25519 is the Ed25519 signature scheme of RFC 8032 (pure, no context).
A secret key is 64 bytes, the 32-byte seed then the 32-byte public key;
a public key is 32 bytes and a signature 64. Call FromSeed, Keypair,
Sign and Verify on the type.

### `Ed25519.FromSeed`

```
export Ed25519.FromSeed(seed: &[32]u8): [64]u8
```

Returns the secret key of a 32-byte seed: the seed, then the public key
A, the encoding of [s]B with s the clamped first half of SHA-512(seed)
(RFC 8032 5.1.5). The same seed always gives the same key, so storing
the seed stores the key. Constant time in the seed: one SHA-512 and one
fixed-base multiply; allocates nothing.

### `Ed25519.Keypair`

```
export Ed25519.Keypair(io: Io): Canceled![64]u8
```

- Errors: Canceled

Returns a new secret key, seed then public key, from 32 bytes of Random,
through FromSeed. The public key is secret[32..64]. Fails with Canceled
as Io.Random does. FromSeed's cost plus the system call; allocates
nothing.

### `Ed25519.Sign`

```
export Ed25519.Sign(secret: &[64]u8, msg: []u8): [64]u8
```

Returns the signature R || S of msg under secret (RFC 8032 5.1.6).
Signing is deterministic: the same key and message give the same
signature, and no random source is used.

The public key is read from secret[32..64] and not checked. Signing with
a public half that does not belong to the seed leaks the secret scalar,
which lets anyone sign with the key, so secret must come from Keypair or
FromSeed, or be stored from one. Constant time in the secret: two SHA-512
passes over msg and one fixed-base multiply; allocates nothing.

### `Ed25519.Verify`

```
export Ed25519.Verify(public: &[32]u8, msg: []u8, sig: &[64]u8): bool
```

Reports whether sig is a valid signature of msg under public (RFC 8032
5.1.7). It refuses an S at or above the group order, accepts a public
key whose y is not reduced below p, and checks without the cofactor:
[S]B - [k]A must encode to R byte for byte. A public key that is not a
point is false. Variable time, since every input is public: one SHA-512
pass over msg and one double-scalar multiply; allocates nothing.

### `Encrypted`

```
export error Encrypted
```

A PKCS#8 private key is encrypted. std.crypto reads only unencrypted
keys.

### `Equal`

```
export Equal(a: []u8, b: []u8): bool
```

Reports whether a and b hold the same bytes, in time that depends only
on their lengths. Unequal lengths are false at once; otherwise every
byte pair is compared and the answer is decided once, at the end.
Compare tags, MACs and other secret-derived bytes with Equal. Cannot
fail. O(n); allocates nothing.

### `Error`

```
export open type Error = BadTag | BadLength | BadKey | Encrypted | BadEncoding | BadCost
```

Error is every failure of std.crypto. It is open, so a match on it
outside std keeps a _ arm.

### `HashKind`

```
export open enum HashKind
```

HashKind names a SHA-2 hash of std.hash: the hash under HKDF, or the
hash that made the digest an RSA signature covers.

Variants:

- `Sha256`
- `Sha384`
- `Sha512`

### `HkdfExpand`

```
export HkdfExpand(prk: &Prk, info: []u8, out: mut []u8): Error!void
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Fills out with HKDF-Expand (RFC 5869) of prk and info: the first
out.Len() bytes of T(1) || T(2) || ..., where T(i) = HMAC(prk, T(i - 1)
|| info || i). Use a different info for each key you derive from one
prk. Fails with BadKey when prk was wiped, and with BadLength when out
is longer than 255 times the hash's output (8,160 bytes for SHA-256),
writing nothing either way. Constant time in prk. O(out + info)
compressions per output block; allocates nothing.

### `HkdfExpandLabel`

```
export HkdfExpandLabel(prk: &Prk, label: str, context: []u8, out: mut []u8): Error!void
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Fills out with HKDF-Expand-Label(prk, label, context, out.Len()) of TLS
1.3 (RFC 8446 7.1): HkdfExpand of prk over the HkdfLabel structure, the
output length as two bytes, then "tls13 " and label, then context, each
after a one-byte length. QUIC derives its packet keys with it (RFC 9001
5.1). Fails with BadKey when prk was wiped, and with BadLength when
label is empty or over 249 bytes, context is over 255 bytes, or out is
longer than 255 times the hash's output (8,160 bytes for SHA-256),
writing nothing on any failure. Constant time in prk; the lengths and
the label are public. O(out + label + context) compressions per output
block; allocates nothing (the 514-byte HkdfLabel is on the stack).

### `HkdfExtract`

```
export HkdfExtract(salt: []u8, ikm: []u8, h: HashKind): Prk
```

Returns HKDF-Extract (RFC 5869) of ikm with salt under the hash h:
HMAC(salt, ikm), the pseudorandom key that HkdfExpand stretches. Any
lengths; an empty salt is the same as a salt of zero bytes as long as
h's output. Constant time in ikm and salt. Cannot fail. O(salt + ikm);
allocates nothing.

### `Hmac256`

```
export Hmac256(key: []u8, data: []u8): [32]u8
```

Returns HMAC-SHA256 of data under key (RFC 2104): SHA256((K ^ opad) ||
SHA256((K ^ ipad) || data)), where K is key zero-padded to 64 bytes, or
SHA256(key) zero-padded when key is longer than 64 bytes. Any lengths,
the empty key included. Constant time in key and data: only their
lengths show. Compare a received tag with Equal. Cannot fail. O(key +
data); it allocates nothing (two 64-byte pad blocks on the stack).

### `Hmac384`

```
export Hmac384(key: []u8, data: []u8): [48]u8
```

Returns HMAC-SHA384 of data under key (RFC 2104 over SHA-384's 128-byte
block): K is key zero-padded to 128 bytes, or SHA384(key) zero-padded
when key is longer. Any lengths, the empty key included. Constant time
in key and data. Cannot fail. O(key + data); allocates nothing.

### `Hmac512`

```
export Hmac512(key: []u8, data: []u8): [64]u8
```

Returns HMAC-SHA512 of data under key (RFC 2104 over SHA-512's 128-byte
block): K is key zero-padded to 128 bytes, or SHA512(key) zero-padded
when key is longer. Any lengths, the empty key included. Constant time
in key and data. Cannot fail. O(key + data); allocates nothing.

### `ParsePrivateKey`

```
export ParsePrivateKey(der: []u8): Error!PrivateKey
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Reads one DER private key: an unencrypted PKCS#8 PrivateKeyInfo (version
0, or 1 with a public key) of a P-256 or Ed25519 key, or a SEC 1
ECPrivateKey naming P-256, with nothing after it. A SEC 1 scalar may be
shorter than 32 bytes, or longer with leading zero bytes, which are
skipped.

Fails with Encrypted for a PKCS#8 EncryptedPrivateKeyInfo; with
BadEncoding when der is not one of these, a PKCS#1 RSAPrivateKey
included, or when a scalar's excess leading byte is not zero; and with
BadKey for an RSA or P-384 key in PKCS#8, another algorithm or curve, or
a scalar outside [1, n - 1]. The secret is copied out of der, so der may
be wiped after. Never panics on input. O(der); allocates nothing.

### `ParsePublicKey`

```
export ParsePublicKey(der: []u8): Error!PublicKey from der
```

- Flows: The result borrows from der.
- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Reads one DER public key: a SubjectPublicKeyInfo of an EC (P-256 or
P-384, uncompressed point), RSA or Ed25519 key, or a PKCS#1
RSAPublicKey, with nothing after it. An EC point and an RSA modulus are
views of der. Fails with BadEncoding when der is not that DER, and with
BadKey for another algorithm or curve, an EC point off its curve or not
uncompressed, or an RSA key RsaPublic.New refuses. Never panics on
input. O(der) plus one point check; allocates nothing.

### `PrivateKey`

```
export enum PrivateKey
```

PrivateKey is a parsed private key: a P-256 secret, or an Ed25519 seed,
which Ed25519.FromSeed turns into a secret key.

Variants:

- `Ec(secret: EcSecret)`: A P-256 secret.
- `Ed25519(seed: [32]u8)`: An Ed25519 seed, which Ed25519.FromSeed turns into a secret key.

### `Prk`

```
export struct Prk
```

Prk is HKDF's pseudorandom key: as many bytes as the hash's output (32,
48 or 64), with the hash that made them. HkdfExtract and Prk.FromBytes
make one; HkdfExpand turns it into keys. After Wipe, HkdfExpand fails
with BadKey.

### `Prk.FromBytes`

```
export Prk.FromBytes(h: HashKind, key: []u8): Error!Prk
```

- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Makes a Prk from key, a pseudorandom key that HkdfExtract did not make,
such as a TLS 1.3 secret that an earlier HkdfExpand wrote. Fails with
BadLength unless key is as long as h's output (32, 48 or 64 bytes). key
is copied, so it may be wiped after. O(1); allocates nothing.

### `Prk.Wipe`

```
export Prk.Wipe(mut self)
```

Sets the key's bytes to zero with Wipe and marks the Prk wiped:
HkdfExpand then fails with BadKey rather than expand under an all-zero
key. Cannot fail. O(1); allocates nothing.

### `PublicKey`

```
export enum PublicKey
```

PublicKey is a parsed public key: an EC point with its curve (an
uncompressed SEC 1 point, a view of the parsed bytes), an RSA key, or an
Ed25519 key.

Variants:

- `Ec(curve: Curve, point: []u8)`: An EC public key: its curve and its uncompressed SEC 1 point, a view of
  the parsed bytes.
- `Rsa(key: RsaPublic)`: An RSA public key.
- `Ed25519(key: [32]u8)`: An Ed25519 public key, 32 bytes.

### `Random`

```
export Random(io: Io, buf: mut []u8): Canceled!void
```

- Errors: Canceled

Fills buf with bytes from the operating system's cryptographically
secure generator (ProcessPrng on Windows, getrandom on Linux), through
Io.Random. No generator of std's own runs in between, so std holds no
state to leak or to repeat after a fork. Fails with Canceled exactly when
Io.Random does; buf may then hold anything and must not be used. O(n)
plus the system call; allocates nothing.

Under a simulated Io from std.sim the bytes come from the simulation's
seeded stream: they repeat from run to run and are not secret.

### `RsaPublic`

```
export view struct RsaPublic
```

RsaPublic is an RSA public key for signature verification: a view of the
modulus's big-endian bytes (leading zeros dropped) and the public
exponent. std.crypto verifies RSA signatures; it does not make them,
encrypt, decrypt or generate RSA keys.

### `RsaPublic.New`

```
export RsaPublic.New(modulus: []u8, exponent: u64): Error!RsaPublic from modulus
```

- Flows: The result borrows from modulus.
- Errors: Error (BadCost, BadEncoding, BadKey, BadLength, BadTag, Encrypted)

Returns the key (modulus, exponent), modulus big-endian with any leading
zero bytes, as a view of modulus. Fails with BadKey unless the modulus
is odd and 2048 to 8192 bits long and the exponent odd and from 3 to
2^32 - 1. O(n); allocates nothing.

### `RsaVerifyPkcs1`

```
export RsaVerifyPkcs1(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool
```

Reports whether sig is an RSASSA-PKCS1-v1_5 signature (RFC 8017 8.2.2)
of digest, made with hash h, under public. sig^e mod n must equal the
expected encoding (0x00 0x01 0xFF... 0x00, then the DigestInfo of h and
digest) byte for byte; no padding parser runs. False, never an error,
when digest is not h's length or sig is not the modulus's length or not
below it. Variable time, since every input is public. O(k^2 log e) word
operations for a k-byte modulus; allocates nothing.

### `RsaVerifyPss`

```
export RsaVerifyPss(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool
```

Reports whether sig is an RSASSA-PSS signature (RFC 8017 8.1.2) of
digest under public, with hash h for the digest and MGF1, and a salt
exactly h's length. False, never an error, when digest is not h's
length, sig is not the modulus's length or not below it, or any check of
EMSA-PSS-VERIFY fails. Variable time, since every input is public. O(k^2
log e) word operations; allocates nothing.

### `SigFormat`

```
export enum SigFormat
```

SigFormat is the encoding of an ECDSA signature: Der, the DER SEQUENCE
of two INTEGERs that X.509 and TLS use, or Fixed, r then s, each as long
as the curve's field (IEEE P1363).

Variants:

- `Der`: The DER SEQUENCE of the two INTEGERs r and s, as X.509 and TLS use.
- `Fixed`: r then s, each as long as the curve's field (IEEE P1363).

### `Wipe`

```
export Wipe(buf: mut []u8)
```

Sets every byte of buf to 0 with stores the compiler keeps even when buf
is never read again, so a key does not stay in memory after its use.
Every key object's Wipe calls it. Cannot fail. O(n); allocates nothing.
It cannot run in compile-time evaluation (E0901).

### `X25519`

```
export X25519(secret: &[32]u8, point: &[32]u8): [32]u8
```

Returns the X25519 function of RFC 7748: the scalar secret times the
curve point point, as a 32-byte u-coordinate. With the base point (9
followed by 31 zero bytes) it gives secret's public key; with a peer's
public key it gives the shared secret. secret is clamped as the RFC says
(bits 0, 1, 2 and 255 cleared, bit 254 set); point is read little-endian
with bit 255 ignored, and a u at or above p is accepted and reduced.

A point of small order gives the all-zero result, which is returned as
it is: a protocol that must refuse it, as TLS 1.3 does, compares the
result with zero using Equal. Pass the shared secret through HkdfExtract
before using it as a key. Constant time in secret and point: a fixed 255
ladder steps and one inversion; allocates nothing.

<!-- api:end -->
