// Why: SipHash is the keyed hash that resists hash flooding, the standard
// choice for hash tables fed untrusted keys.
//
// What: Sip13, SipHash-1-3 (Aumasson and Bernstein 2012, c = 1, d = 3,
// Rust's DefaultHasher) of a byte string under a HashKey, and Sip13State,
// the same hash over bytes that arrive in pieces.
//
// How: words little-endian, the length's low byte on top of the last word.
// std.core's table hash is the same compression over a key's Hash bytes (a
// str adds 0xFF); this one adds nothing. Sip13State keeps the four words, a
// partial word of up to 7 bytes and the length, and runs the one-shot
// form's rounds and finish, so any split gives Sip13's value; the one-shot
// form keeps its own loop-free short path.

import std.core
import std.raw

/// Returns SipHash-1-3 of data under key. It is a keyed hash: without the
/// key an attacker cannot predict outputs or choose inputs that collide, so
/// a hash table keyed with a random HashKey (HashKey.New) resists flooding
/// while the key stays secret. SipHash-1-3 is the reduced-round variant for
/// hash tables; to authenticate messages, use crypto.Hmac256. Cannot fail.
/// O(data.Len()); allocates nothing. Inputs of up to 16 bytes take a short
/// path with no loop.
export Sip13(data: []u8, key: HashKey): u64 {
    w := core.HashKeyWords(&key)
    if data.Len() <= 16 {
        return SipShort(data, w.k0, w.k1)
    }
    return SipKeyed(data, w.k0, w.k1)
}

/// Sip13State is a SipHash-1-3 hash in progress under a HashKey, for input
/// that arrives in pieces. It is not zero-valid: a zero-filled state does
/// not hold the key's starting words, and Arena.Slice[Sip13State] is E0701.
/// Make one with New.
export struct Sip13State {
    v0: u64
    v1: u64
    v2: u64
    v3: u64
    tail: u64
    tn: int
    total: u64
    invariant tn >= 0 && tn < 8
}

/// Returns the state of the empty message under key. O(1); allocates
/// nothing.
export Sip13State.New(key: HashKey): Sip13State {
    w := core.HashKeyWords(&key)
    return Sip13State(v0: 0x736f6d6570736575 ^ w.k0, v1: 0x646f72616e646f6d ^ w.k1, v2: 0x6c7967656e657261 ^ w.k0, v3: 0x7465646279746573 ^ w.k1, tail: 0, tn: 0, total: 0)
}

/// Absorbs data. Bytes in any split gives the same value as Sip13 of the
/// concatenation. Whole 8-byte words are read from data in place; a
/// partial word of up to 7 bytes waits in the state. O(data.Len());
/// allocates nothing.
export Sip13State.Bytes(mut self, data: []u8) {
    n := data.Len()
    self.total = self.total +% Trunc[u64](n)
    var v0 := self.v0
    var v1 := self.v1
    var v2 := self.v2
    var v3 := self.v3
    var i := 0
    if self.tn > 0 {
        var m := self.tail
        var tn := self.tn
        for tn < 8 && i < n {
            m = m | (u64(data[i]) << (8 * (tn & 7)))
            tn += 1
            i += 1
        }
        if tn < 8 {
            self.tail = m
            self.tn = tn
            return
        }
        v3 = v3 ^ m
        SipRound(mut v0, mut v1, mut v2, mut v3)
        v0 = v0 ^ m
    }
    for n - i >= 8 {
        m := Le64(data, i)
        v3 = v3 ^ m
        SipRound(mut v0, mut v1, mut v2, mut v3)
        v0 = v0 ^ m
        i += 8
    }
    // SipLast's top byte is the length byte of n; the 0 to 7 bytes below
    // it are the partial word.
    self.tail = SipLast(data, i, n) & 0x00FFFFFFFFFFFFFF
    self.tn = n - i
    self.v0 = v0
    self.v1 = v1
    self.v2 = v2
    self.v3 = v3
}

/// Returns SipHash-1-3 of everything absorbed under the state's key; the
/// state can keep absorbing. O(1): the last word and the four final
/// rounds. It allocates nothing.
export Sip13State.Sum(self): u64 {
    return SipFinish(self.v0, self.v1, self.v2, self.v3, ((self.total & 0xFF) << 56) | self.tail)
}

// SipRound is one SipRound on the state (v0, v1, v2, v3).
SipRound(v0: mut u64, v1: mut u64, v2: mut u64, v3: mut u64) {
    *v0 = *v0 +% *v1
    *v1 = raw.RotL64(*v1, 13) ^ *v0
    *v0 = raw.RotL64(*v0, 32)
    *v2 = *v2 +% *v3
    *v3 = raw.RotL64(*v3, 16) ^ *v2
    *v0 = *v0 +% *v3
    *v3 = raw.RotL64(*v3, 21) ^ *v0
    *v2 = *v2 +% *v1
    *v1 = raw.RotL64(*v1, 17) ^ *v2
    *v2 = raw.RotL64(*v2, 32)
}

// SipLast is the last word of b's bytes s to n (n - s < 8, s >= 0, n =
// b.Len()) with the length's low byte on top, built with no loop: from 4 to
// 7 bytes two 4-byte loads that overlap (an overlapping byte lands on the
// same bits in both, so the or is exact), from 1 to 3 bytes the first, the
// middle and the last byte (the same rule). The mask on t changes nothing
// for a valid call and lets LLVM prove every shift amount below 64, so no
// shift check is left.
SipLast(b: []u8, s: int, n: int): u64 {
    t := (n - s) & 7
    tail := if t >= 4 {
        u64(Le32(b, s)) | (u64(Le32(b, n - 4)) << (8 * (t - 4)))
    } else if t > 0 {
        h := t >> 1
        u64(b[s]) | (u64(b[s + h]) << (8 * h)) | (u64(b[n - 1]) << (8 * (t - 1)))
    } else {
        0
    }
    return ((Trunc[u64](n) & 0xFF) << 56) | tail
}

// SipFinish absorbs the last word m and runs the three final rounds.
SipFinish(v0: u64, v1: u64, v2: u64, v3: u64, m: u64): u64 {
    var a := v0
    var b := v1
    var c := v2
    var d := v3 ^ m
    SipRound(mut a, mut b, mut c, mut d)
    a = a ^ m
    c = c ^ 0xFF
    SipRound(mut a, mut b, mut c, mut d)
    SipRound(mut a, mut b, mut c, mut d)
    SipRound(mut a, mut b, mut c, mut d)
    return a ^ b ^ c ^ d
}

// SipShort is SipHash-1-3 of b, b.Len() <= 16, under (k0, k1): at most two
// whole words, then the last word, with no loop.
SipShort(b: []u8, k0: u64, k1: u64): u64 {
    var v0: u64 = 0x736f6d6570736575 ^ k0
    var v1: u64 = 0x646f72616e646f6d ^ k1
    var v2: u64 = 0x6c7967656e657261 ^ k0
    var v3: u64 = 0x7465646279746573 ^ k1
    n := b.Len()
    var s := 0
    if n >= 8 {
        w := b[0..8]
        m := Le64(w, 0)
        v3 = v3 ^ m
        SipRound(mut v0, mut v1, mut v2, mut v3)
        v0 = v0 ^ m
        s = 8
        if n == 16 {
            // The 16-byte table key: no tail, the last word is the
            // length byte alone.
            u := b[8..16]
            m2 := Le64(u, 0)
            v3 = v3 ^ m2
            SipRound(mut v0, mut v1, mut v2, mut v3)
            v0 = v0 ^ m2
            return SipFinish(v0, v1, v2, v3, 0x1000000000000000)
        }
    }
    return SipFinish(v0, v1, v2, v3, SipLast(b, s, n))
}

// SipKeyed is SipHash-1-3 of b under the key words (k0, k1). O(n): one
// compression round per 8 bytes, the state in four locals, then the last
// word with no loop. No allocation.
SipKeyed(b: []u8, k0: u64, k1: u64): u64 {
    var v0: u64 = 0x736f6d6570736575 ^ k0
    var v1: u64 = 0x646f72616e646f6d ^ k1
    var v2: u64 = 0x6c7967656e657261 ^ k0
    var v3: u64 = 0x7465646279746573 ^ k1
    n := b.Len()
    var i := 0
    for n - i >= 8 {
        m := Le64(b, i)
        v3 = v3 ^ m
        SipRound(mut v0, mut v1, mut v2, mut v3)
        v0 = v0 ^ m
        i += 8
    }
    return SipFinish(v0, v1, v2, v3, SipLast(b, i, n))
}
