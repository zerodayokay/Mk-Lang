# std.hash

`std.hash` computes hashes and checksums of bytes. It has fast non-cryptographic hashes for tables and caches, a keyed hash for tables that face untrusted keys, CRC-32 for file formats, and the SHA-2, SHA-1 and BLAKE3 digests. Every hash comes as one function call, and most also as a streaming state for input that arrives in pieces. For message authentication, signatures and passwords, use [`std.crypto`](crypto.md), which builds on these digests.

Read the [standard library guide](README.md) first.

```mk
// verdict: parse
import std.hash
```

## Contents

- [The main pieces](#the-main-pieces)
- [Choosing a hash](#choosing-a-hash)
- [Streaming states](#streaming-states)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [one-shot digests](#one-shot-digests), [hash input in pieces](#hash-input-in-pieces), [keyed and seeded hashes](#keyed-and-seeded-hashes)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Hash a key for a table or a cache | `Xx` (XXH3-64), `Xx64` (XXH64 with a seed) |
| A `Map` or `Set` with keys an attacker may choose | `Map.NewKeyed` or `Set.NewKeyed` with [`HashKey.New(io)`](core.md#maps-and-sets); `Sip13` (SipHash-1-3) for a table of your own |
| Checksum for gzip, PNG or ZIP | `Crc32` |
| Cryptographic digest | `Sha256`, `Sha384`, `Sha512`, `Blake3` |
| A digest a format requires (WebSocket, Git) | `Sha1` |
| Hash input that arrives in pieces | `Sha256State`, `Sha384State`, `Sha512State`, `Sha1State`, `Blake3State`, `Crc32State`, `Xx64State`, `Sip13State` |

Each function takes a `[]u8` and returns the hash by value: a `u64` or `u32`, or a byte array. The arrays are `[32]u8` for SHA-256 and BLAKE3, `[48]u8` for SHA-384, `[64]u8` for SHA-512 and `[20]u8` for SHA-1. To hash a `str`, pass `s.AsBytes()`. To print a digest in hex, use `encoding.HexEncode`.

## Choosing a hash

| Function | Algorithm | Output | Use it for |
|---|---|---|---|
| `Xx` | XXH3-64, seed 0 | `u64` | Table keys, cache keys, deduplication |
| `Xx64` | XXH64 under a seed | `u64` | The same, where a format names XXH64 or a seed |
| `Sip13` | SipHash-1-3 under a 128-bit key | `u64` | Tables keyed by untrusted input |
| `Crc32` | CRC-32/ISO-HDLC | `u32` | Error detection in gzip, PNG, ZIP, Ethernet |
| `Sha256`, `Sha384`, `Sha512` | FIPS 180-4 SHA-2 | 32, 48, 64 bytes | Integrity, content addresses, signatures |
| `Blake3` | BLAKE3, 32-byte output | 32 bytes | A fast cryptographic digest of large data |
| `Sha1` | FIPS 180-4 SHA-1 | 20 bytes | Only where a format requires it |

- `Xx`, `Xx64` and `Crc32` are not cryptographic: anyone can find two inputs with the same value. When an attacker chooses the keys of a table, make the map with `NewKeyed` and a random key from `HashKey.New(io)`. In a table of your own, call `Sip13` with that key. Without the key an attacker cannot predict outputs or force collisions, so the table resists flooding while the key stays secret.
- `Sip13` is for hash tables, not for authenticating messages: use `crypto.Hmac256` for that.
- SHA-1 is broken for collisions: a chosen-prefix collision is practical. Use it only where a format requires it, such as the WebSocket handshake of RFC 6455, Git object names and legacy identifiers. Never use it for signatures or for integrity against an attacker.
- `Sha256` uses the CPU's SHA instructions where they exist. Both paths give the same digest and take time that depends only on the input's length.

## Streaming states

A state hashes input that arrives in pieces: make one with `New`, pass each piece to `Bytes`, and read the result with `Sum`. Any split of the input gives the same value as the one-shot function over the whole input.

- `Sum` takes the state by value and works on a copy, so the state can keep absorbing after it. Read a running digest, add more input, and read again.
- `Crc32State` holds the finished CRC, so its zero value is the state of the empty input: a zero-filled `Crc32State` is a fresh one.
- The other states are not zero-valid: a zero-filled `Sha256State`, `Blake3State`, `Xx64State` or `Sip13State` is not the start of a hash. Make them with `New`. Asking an arena for a slice of them, as `a.Slice[Sha256State](n)` would, is refused at compile time (E0701).
- `Xx64State.New` takes the seed and `Sip13State.New` the `HashKey`. `Xx` has no streaming state.

The cookbook shows a whole file hashed through one fixed buffer: [Hash a file in pieces](../cookbook.md#hash-a-file-in-pieces).

## Memory and cost

Every function in `std.hash` is total: it cannot fail and never panics. None allocates; states live wherever you put them, usually on the stack.

- Every hash is O(n) in its input. `Sum` costs one or two block compressions for the SHA states, O(1) for `Crc32State`, `Xx64State` and `Sip13State` (one round for the last word, then the three final rounds), and one compression per level of its stack for `Blake3State`.
- `Blake3State` holds a stack of up to 54 chaining values (1,728 bytes), enough for 2^64 bytes of input. `Blake3` builds no state, so a short input does not pay for that stack.
- `Xx` takes one of five fixed paths for inputs of up to 240 bytes and reads longer inputs in 64-byte stripes. It is small enough to inline.

## Examples

Each program runs as written with `mk run`.

### One-shot digests

```mk
// verdict: ok
import std.encoding
import std.hash

export Main(sys: &System): OutOfMemory|WriteError!void {
    data := "abc".AsBytes()
    sha := hash.Sha256(data) // a [32]u8, on the stack
    try sys.Print("sha256 {h}\n", (h: try encoding.HexEncode(sys.arena, sha[..])))
    b3 := hash.Blake3(data)
    try sys.Print("blake3 {h}\n", (h: try encoding.HexEncode(sys.arena, b3[..])))
    try sys.Print("crc32  {c:x}\n", (c: hash.Crc32(data)))
    try sys.Print("xxh3   {x:x}\n", (x: hash.Xx(data)))
}
```

```text
$ mk run digests.mk
sha256 ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
blake3 6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85
crc32  352441c2
xxh3   78af5f94892f3950
```

- The digests are plain arrays; `sha[..]` is the `[]u8` view that `HexEncode` takes.
- `{c:x}` prints an integer in lower-case hex, through the prelude's [`Format`](core.md#formatting-and-printing).

### Hash input in pieces

```mk
// verdict: ok
import std.encoding
import std.hash

export Main(sys: &System): OutOfMemory|WriteError!void {
    parts := ["The quick brown fox ", "jumps over ", "the lazy dog"]
    var st := hash.Sha256State.New() // not zero-valid: start from New
    for p in parts {
        st.Bytes(p.AsBytes())
    }
    head := st.Sum() // Sum works on a copy, so st can keep absorbing
    st.Bytes(".".AsBytes())
    full := st.Sum()
    whole := hash.Sha256("The quick brown fox jumps over the lazy dog".AsBytes())
    a := try encoding.HexEncode(sys.arena, head[..])
    b := try encoding.HexEncode(sys.arena, whole[..])
    c := try encoding.HexEncode(sys.arena, full[..])
    try sys.Print("pieces   {a}\none shot {b}\nsame: {same}\n", (a, b, same: a == b))
    try sys.Print("with dot {c}\n", (c))
}
```

```text
$ mk run pieces.mk
pieces   d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592
one shot d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592
same: true
with dot ef537f25c895bfa782526529a9b63d97aa631564d5d789c2b765448c8635fb6c
```

- `Bytes` takes `mut self`, so the state is a `var`.
- To compare digests in constant time, as for a received MAC, use `crypto.Equal`.

### Keyed and seeded hashes

```mk
// verdict: ok
import std.hash

export Main(sys: &System): Canceled|WriteError!void {
    key := try HashKey.New(sys.io) // random, from the OS; keep it secret
    a := hash.Sip13("user:alice".AsBytes(), key)
    b := hash.Sip13("user:alice".AsBytes(), key)
    try sys.Print("same key, same hash: {same}\n", (same: a == b))
    fixed := HashKey.Of(1, 2) // a fixed key gives reproducible values
    try sys.Print("sip13 {v:x}\n", (v: hash.Sip13("user:alice".AsBytes(), fixed)))
    var st := hash.Sip13State.New(fixed) // the same hash, fed in pieces
    st.Bytes("user:".AsBytes())
    st.Bytes("alice".AsBytes())
    try sys.Print("pieces same: {same}\n", (same: st.Sum() == hash.Sip13("user:alice".AsBytes(), fixed)))
    try sys.Print("xxh64 {v:x}\n", (v: hash.Xx64("user:alice".AsBytes(), 7)))
}
```

```text
$ mk run keyed.mk
same key, same hash: true
sip13 dafe9f4b07bb2747
pieces same: true
xxh64 dccb6e2bb4421736
```

- `HashKey` is the prelude's key type; `Map[K, V].NewKeyed` and the other keyed containers take one too.
- A random key changes the values from run to run, so never store a `Sip13` value made under a random key.

## API

<!-- api:begin -->
Stability: Stable.

### `Blake3`

```
export Blake3(data: []u8): [32]u8
```

Returns the 32-byte BLAKE3 hash of data. It builds no Blake3State, so a
short input does not pay for the state's 1,728-byte stack.
O(data.Len()); allocates nothing.

### `Blake3State`

```
export struct Blake3State
```

Blake3State is a BLAKE3 hash in progress. It is not zero-valid: a
zero-filled state is not the start of a hash, so
Arena.Slice[Blake3State] is E0701. Make one with New. It holds a stack
of up to 54 chaining values (1,728 bytes), enough for 2^64 bytes of
input.

### `Blake3State.Bytes`

```
export Blake3State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same hash as Blake3 of the
concatenation. Runs of four whole 1 KiB chunks are hashed side by side
with SIMD. O(data.Len()); allocates nothing.

### `Blake3State.New`

```
export Blake3State.New(): Blake3State
```

Returns the state of the empty input. O(1); allocates nothing.

### `Blake3State.Sum`

```
export Blake3State.Sum(self): [32]u8
```

Returns the 32-byte hash of everything absorbed. It works on a copy of
the state, so the state can keep absorbing. O(stack depth) compressions;
allocates nothing.

### `Crc32`

```
export Crc32(data: []u8): u32
```

Returns the CRC-32 of data (CRC-32/ISO-HDLC), the checksum of gzip, PNG,
ZIP and Ethernet. Cannot fail. O(n); allocates nothing.

### `Crc32State`

```
export struct Crc32State
```

Crc32State is a CRC-32 in progress. Its zero value is the CRC of the
empty message, so a zero-filled Crc32State is a fresh one: the type is
zero-valid.

### `Crc32State.Bytes`

```
export Crc32State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same CRC as Crc32 of the
concatenation. O(data.Len()); allocates nothing and copies nothing.

### `Crc32State.New`

```
export Crc32State.New(): Crc32State
```

Returns the state of the empty message. O(1); allocates nothing.

### `Crc32State.Sum`

```
export Crc32State.Sum(self): u32
```

Returns the CRC-32 of everything absorbed; the state can keep absorbing.
O(1); allocates nothing.

### `Sha1`

```
export Sha1(data: []u8): [20]u8
```

Returns the SHA-1 digest of data. SHA-1 is broken for collisions: a
chosen-prefix collision is practical. Use it only where a format
requires it (the WebSocket handshake, Git object names, legacy
identifiers), never for signatures or for integrity against an attacker.
O(data.Len()); allocates nothing.

### `Sha1State`

```
export struct Sha1State
```

Sha1State is a SHA-1 hash in progress. It is not zero-valid: a
zero-filled state is not the start of a hash, so Arena.Slice[Sha1State]
is E0701. Make one with New. See Sha1 for where SHA-1 may be used.

### `Sha1State.Bytes`

```
export Sha1State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same digest as Sha1 of the
concatenation. Whole blocks are hashed straight from data, without a
copy. O(data.Len()); allocates nothing.

### `Sha1State.New`

```
export Sha1State.New(): Sha1State
```

Returns the state of the empty message. O(1); allocates nothing.

### `Sha1State.Sum`

```
export Sha1State.Sum(self): [20]u8
```

Returns the digest of everything absorbed. It pads a copy of the state,
so the state can keep absorbing. One or two block compressions;
allocates nothing.

### `Sha256`

```
export Sha256(data: []u8): [32]u8
```

Returns the SHA-256 digest of data. It uses the CPU's SHA instructions
where they exist; both paths give the same digest and run in time that
depends only on data's length. O(data.Len()); allocates nothing.

### `Sha256State`

```
export struct Sha256State
```

Sha256State is a SHA-256 hash in progress. It is not zero-valid: a
zero-filled state is not the start of a hash, so
Arena.Slice[Sha256State] is E0701. Make one with New.

### `Sha256State.Bytes`

```
export Sha256State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same digest as Sha256 of the
concatenation. Whole blocks are hashed straight from data, without a
copy. O(data.Len()); allocates nothing.

### `Sha256State.New`

```
export Sha256State.New(): Sha256State
```

Returns the state of the empty message. O(1); allocates nothing.

### `Sha256State.Sum`

```
export Sha256State.Sum(self): [32]u8
```

Returns the digest of everything absorbed. The length in the padding is
exact below 2^61 bytes. It pads a copy of the state, so the state can
keep absorbing. One or two block compressions; allocates nothing.

### `Sha384`

```
export Sha384(data: []u8): [48]u8
```

Returns the SHA-384 digest of data, SHA-512 truncated to 48 bytes from
its own starting values. O(data.Len()); allocates nothing.

### `Sha384State`

```
export struct Sha384State
```

Sha384State is a SHA-384 hash in progress. It is not zero-valid: a
zero-filled state is not the start of a hash, so
Arena.Slice[Sha384State] is E0701. Make one with New.

### `Sha384State.Bytes`

```
export Sha384State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same digest as Sha384 of the
concatenation. Whole blocks are hashed straight from data, without a
copy. O(data.Len()); allocates nothing.

### `Sha384State.New`

```
export Sha384State.New(): Sha384State
```

Returns the state of the empty message. O(1); allocates nothing.

### `Sha384State.Sum`

```
export Sha384State.Sum(self): [48]u8
```

Returns the 48-byte digest of everything absorbed. It pads a copy of the
state, so the state can keep absorbing. One or two block compressions;
allocates nothing.

### `Sha512`

```
export Sha512(data: []u8): [64]u8
```

Returns the SHA-512 digest of data. O(data.Len()); allocates nothing.

### `Sha512State`

```
export struct Sha512State
```

Sha512State is a SHA-512 hash in progress. It is not zero-valid: a
zero-filled state is not the start of a hash, so
Arena.Slice[Sha512State] is E0701. Make one with New.

### `Sha512State.Bytes`

```
export Sha512State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same digest as Sha512 of the
concatenation. Whole blocks are hashed straight from data, without a
copy. O(data.Len()); allocates nothing.

### `Sha512State.New`

```
export Sha512State.New(): Sha512State
```

Returns the state of the empty message. O(1); allocates nothing.

### `Sha512State.Sum`

```
export Sha512State.Sum(self): [64]u8
```

Returns the digest of everything absorbed. It pads a copy of the state,
so the state can keep absorbing. One or two block compressions;
allocates nothing.

### `Sip13`

```
export Sip13(data: []u8, key: HashKey): u64
```

Returns SipHash-1-3 of data under key. It is a keyed hash: without the
key an attacker cannot predict outputs or choose inputs that collide, so
a hash table keyed with a random HashKey (HashKey.New) resists flooding
while the key stays secret. SipHash-1-3 is the reduced-round variant for
hash tables; to authenticate messages, use crypto.Hmac256. Cannot fail.
O(data.Len()); allocates nothing. Inputs of up to 16 bytes take a short
path with no loop.

### `Sip13State`

```
export struct Sip13State
```

Sip13State is a SipHash-1-3 hash in progress under a HashKey, for input
that arrives in pieces. It is not zero-valid: a zero-filled state does
not hold the key's starting words, and Arena.Slice[Sip13State] is E0701.
Make one with New.

### `Sip13State.Bytes`

```
export Sip13State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same value as Sip13 of the
concatenation. Whole 8-byte words are read from data in place; a
partial word of up to 7 bytes waits in the state. O(data.Len());
allocates nothing.

### `Sip13State.New`

```
export Sip13State.New(key: HashKey): Sip13State
```

Returns the state of the empty message under key. O(1); allocates
nothing.

### `Sip13State.Sum`

```
export Sip13State.Sum(self): u64
```

Returns SipHash-1-3 of everything absorbed under the state's key; the
state can keep absorbing. O(1): one round for the last word, then the
three final rounds. It allocates nothing.

### `Xx`

```
export Xx(data: []u8): u64
```

Returns XXH3-64 of data with seed 0 and the default secret. It is not
cryptographic: for keys an attacker chooses use Sip13. Inputs of up to
240 bytes take one of five fixed paths of at most 15 multiplies; longer
inputs are read in 64-byte stripes. The entry is small enough to inline,
so hashing a 9 to 16 byte key costs no call. O(n); allocates nothing.

### `Xx64`

```
export Xx64(data: []u8, seed: u64): u64
```

Returns XXH64 of data with seed. It is not cryptographic: an attacker
can find inputs that collide, so for keys an attacker chooses use Sip13.
O(n); allocates nothing.

### `Xx64State`

```
export struct Xx64State
```

Xx64State is an XXH64 hash in progress under a seed. It is not
zero-valid: a zero-filled state does not hold the seed's starting
values. Make one with New.

### `Xx64State.Bytes`

```
export Xx64State.Bytes(mut self, data: []u8)
```

Absorbs data. Bytes in any split gives the same value as Xx64 of the
concatenation. Whole 32-byte stripes are read from data in place.
O(data.Len()); allocates nothing, and copies at most 31 bytes.

### `Xx64State.New`

```
export Xx64State.New(seed: u64): Xx64State
```

Returns the state of the empty message under seed. O(1); allocates
nothing.

### `Xx64State.Sum`

```
export Xx64State.Sum(self): u64
```

Returns XXH64 of everything absorbed under the state's seed; the state
can keep absorbing. O(1): at most 31 bytes of tail. It allocates
nothing.

<!-- api:end -->
