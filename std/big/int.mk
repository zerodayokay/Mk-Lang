// Why: arbitrary-precision integers (cryptography, exact arithmetic, parsers
// of big literals) need one value type with exactly one form per value.
//
// What: the Int type, the TooLarge error, the constructors FromI64,
// FromBytes and FromWords, the queries (Sign, BitLen, Bit, ToI64, ToF64,
// Bytes, Words) and Int's Eq, Ord and Hash.
//
// How: an Int is a sign and a magnitude, a little-endian slice of u64 words
// with no zero top word, so every value has exactly one form (zero is the
// empty magnitude, never negative) and Eq is word equality. The words are
// immutable: every operation writes a new magnitude into the arena it is
// given, or, for FromWords, views the caller's. Nothing in std.big is
// constant time: running time and memory follow the operands' sizes and, for
// division and ModPow, their values. No other std module imports it. Each
// divide-and-conquer recursion (the products, the squares, the recursive
// division, Text, Parse and Sqrt) takes a u8 depth that falls by one per
// level and stops the recursion at zero, so `mk query stack` bounds every
// operation's stack.

import std.raw

// DepthLimit is the depth each recursion starts with. Every recursion here
// at least halves its operand's length per level (Toom-3 divides it by
// three, Text and Parse halve the digits), so none goes past 64 levels for
// a length below 2^63, and the limit is never reached; if it were, the
// recursion would finish with its leaf method (the schoolbook product or
// square, Knuth's division, Horner's rule, the chunk loop, Newton's root),
// which is correct at any length.
const DepthLimit: u8 = 255

/// Parse's text has more digits than its max_digits allows.
export error TooLarge

/// Error is every failure of std.big: ParseError for text that is not an
/// integer in the base, TooLarge past Parse's max_digits, and OutOfMemory
/// when an arena cannot give the words. It is open, so a match on it
/// outside std keeps a _ arm.
export open type Error = ParseError | TooLarge | OutOfMemory

/// Int is an arbitrary-precision signed integer. It is an immutable view of
/// words in an arena, or of the caller's words for FromWords, and has no
/// heap form. It is unique: assigning an Int or passing it by value moves
/// it, so pass &x to borrow it and Clone it to keep a second copy. Every
/// value has exactly one form, so equal values have equal words. Each
/// operation takes an arena and writes its result there; the result lives
/// as long as that arena. To keep a value past its arena, copy it with
/// Clone into a longer-lived arena, or copy Words out and rebuild it with
/// FromWords.
///
/// Int implements Eq, Ord and Hash: compare with x.Eq(&y) and x.Less(&y),
/// as the == and < operators do not apply to it. Nothing in std.big is
/// constant time: running time follows the operands' sizes and, for
/// division and ModPow, their values, so it is not for secret data.
export view struct Int {
    // mag is the magnitude, little-endian, with no zero top word.
    mag: []u64
    // negative is true only for a value below zero.
    negative: bool
}

// Make is the Int of the magnitude z, trimmed of zero top words, with sign
// neg (dropped for zero).
Make(z: []u64, neg: bool): Int from z {
    m := Norm(z)
    return Int(mag: m, negative: neg && m.Len() > 0)
}

// Zero is the Int zero, with an empty magnitude in a.
Zero(a: &Arena): OutOfMemory!Int from a {
    z := try a.Slice[u64](0)
    return Int(mag: z[..], negative: false)
}

// Dup is a copy of the magnitude x in a, with sign neg.
Dup(a: &Arena, x: []u64, neg: bool): OutOfMemory!Int from a {
    z := try a.Slice[u64](x.Len())
    Copy(mut z[..], x)
    return Make(z[..], neg)
}

/// The Int whose value is x. Fails with OutOfMemory when a cannot give one
/// word. O(1); allocates one word in a (none for zero).
export FromI64(a: &Arena, x: i64): OutOfMemory!Int from a {
    if x == 0 {
        return try Zero(a)
    }
    z := try a.Slice[u64](1)
    z[0] = if x < 0 { 0 -% Trunc[u64](x) } else { Trunc[u64](x) }
    return Int(mag: z[..], negative: x < 0)
}

/// The non-negative Int whose big-endian bytes are b, leading zero bytes
/// allowed. Fails with OutOfMemory when a cannot give the words. O(n) in
/// b.Len(); allocates ceil(b.Len() / 8) words in a.
export FromBytes(a: &Arena, b: []u8): OutOfMemory!Int from a {
    n := b.Len()
    z := try a.Slice[u64]((n + 7) / 8)
    // Byte k from the end is bits 8 (k mod 8) of word k / 8.
    for k in 0..n {
        z[k >> 3] = z[k >> 3] | (u64(b[n - 1 - k]) << ((k & 7) * 8))
    }
    return Make(z[..], false)
}

/// The Int whose magnitude is words, little-endian, negative when negative
/// is true and the value is not zero. Views words without copying: zero top
/// words are left out of the view. Cannot fail. O(number of zero top
/// words); allocates nothing.
export FromWords(words: []u64, negative: bool): Int from words {
    return Make(words, negative)
}

/// A copy of the value whose words are in a, so it outlives the arena or
/// words the original views. Fails with OutOfMemory when a cannot give the
/// words. O(n); allocates n words in a.
export Int.Clone(&self, a: &Arena): OutOfMemory!Int from a {
    return try Dup(a, self.mag, self.negative)
}

/// The magnitude, little-endian u64 words with no zero top word (empty for
/// zero); the sign is Sign's. To keep a value after its arena is reset,
/// copy these words into storage you own, such as a HeapList[u64], and make
/// the value again with FromWords. O(1); allocates nothing.
export Int.Words(&self): []u64 from self {
    return self.mag
}

/// -x. Fails with OutOfMemory when a cannot give the words. O(n); allocates
/// n words in a.
export Int.Neg(&self, a: &Arena): OutOfMemory!Int from a {
    return try Dup(a, self.mag, !self.negative)
}

/// -1, 0 or 1 as x is below, equal to or above y. O(n) in the shorter
/// magnitude when the lengths are equal, else O(1); allocates nothing.
export Int.Cmp(&self, y: &Int): int {
    if self.negative != y.negative {
        return if self.negative { -1 } else { 1 }
    }
    c := CmpVV(self.mag, y.mag)
    return if self.negative { -c } else { c }
}

/// -1, 0 or 1 as x is below, equal to or above zero. O(1); allocates
/// nothing.
export Int.Sign(&self): int {
    if self.mag.Len() == 0 {
        return 0
    }
    return if self.negative { -1 } else { 1 }
}

/// The number of bits of |x|, 0 for zero. O(1); allocates nothing.
export Int.BitLen(&self): int {
    n := self.mag.Len()
    if n == 0 {
        return 0
    }
    return 64 * n - raw.Clz64(self.mag[n - 1])
}

/// Bit i of x in two's complement, as for the fixed integer types, so a
/// negative value has every bit above its length set. Panics when i is
/// negative. O(i / 64) for a negative x, else O(1); allocates nothing.
export Int.Bit(&self, i: int): bool {
    if i < 0 {
        Panic("big.Int.Bit: negative bit index")
    }
    w := i >> 6
    s := i & 63
    if !self.negative {
        return w < self.mag.Len() && (self.mag[w] >> s) & 1 == 1
    }
    // -m is ~(m - 1): the bit of m - 1, complemented.
    if w >= self.mag.Len() {
        return true
    }
    var b := self.mag[w]
    var low_zero := true
    for k in 0..w {
        if self.mag[k] != 0 {
            low_zero = false
            break
        }
    }
    if low_zero {
        b = b -% 1
    }
    return (b >> s) & 1 == 0
}

/// x as an i64, or none when it does not fit. O(1); allocates nothing.
export Int.ToI64(&self): ?i64 {
    n := self.mag.Len()
    if n == 0 {
        return some(0)
    }
    if n > 1 {
        return none
    }
    w := self.mag[0]
    if self.negative {
        if w > 0x8000000000000000 {
            return none
        }
        return some(Trunc[i64](0 -% w))
    }
    if w > 0x7FFFFFFFFFFFFFFF {
        return none
    }
    return some(Trunc[i64](w))
}

/// x rounded to the nearest f64, ties to even; past f64's range, an
/// infinity of x's sign. O(n) for the sticky bit; allocates nothing.
export Int.ToF64(&self): f64 {
    n := self.mag.Len()
    if n == 0 {
        return 0.0
    }
    bits := self.BitLen()
    var f: f64 = 0.0
    if bits <= 64 {
        f = Cast[f64](self.mag[0])
    } else if bits > 1024 {
        f = f64.FromBits(0x7FF0000000000000)
    } else {
        // The top 64 bits, with every bit below them folded into the
        // lowest as a sticky bit: that bit lies 11 places under f64's last,
        // so the one rounding of Cast is the rounding of the whole value.
        shift := bits - 64
        w := shift >> 6
        s := shift & 63
        var top := self.mag[w] >> s
        if s != 0 {
            top = top | (self.mag[w + 1] << (64 - s))
        }
        var sticky := s != 0 && self.mag[w] << (64 - s) != 0
        for k in 0..w {
            if self.mag[k] != 0 {
                sticky = true
                break
            }
        }
        if sticky {
            top = top | 1
        }
        // 2^shift is exact: shift <= 960.
        f = Cast[f64](top) * f64.FromBits(Trunc[u64](1023 + shift) << 52)
    }
    return if self.negative { -f } else { f }
}

/// |x| as big-endian bytes with no leading zero byte (empty for zero); the
/// sign is dropped. Fails with OutOfMemory when a cannot give the bytes.
/// O(n); allocates ceil(BitLen() / 8) bytes in a.
export Int.Bytes(&self, a: &Arena): OutOfMemory![]u8 from a {
    len := (self.BitLen() + 7) / 8
    out := try a.Slice[u8](len)
    for k in 0..len {
        out[len - 1 - k] = Trunc[u8](self.mag[k >> 3] >> ((k & 7) * 8))
    }
    return out[..]
}

impl Eq for Int {
    /// Whether the two values are equal. O(n); allocates nothing.
    Eq(&self, other: &Int): bool {
        return self.negative == other.negative && CmpVV(self.mag, other.mag) == 0
    }
}

impl Ord for Int {
    /// Whether x is below other. O(n); allocates nothing.
    Less(&self, other: &Int): bool {
        return self.Cmp(other) < 0
    }
}

impl Hash for Int {
    /// Hashes the sign, the words and their count. Every value has one form,
    /// so equal values hash alike. O(n); allocates nothing.
    Hash(&self, h: mut Hasher) {
        h.U64(Bit1(self.negative))
        for w in self.mag {
            h.U64(w)
        }
        h.U64(Trunc[u64](self.mag.Len()))
    }
}
