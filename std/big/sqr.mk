// Why: squaring is common (ModPow, Sqrt, parsing and printing powers) and
// needs about half the word products of a general multiplication.
//
// What: SqrEq and its methods SqrBasic, KaraSqr and Toom3Sqr, with
// SqrScratch and the thresholds SqrBasicWords, KaraSqrWords and ToomSqrWords.
//
// How: taken by Int.Mul when both operands hold the same value (Go's Int.Mul
// and GMP's mpz_mul do the same). The schoolbook square (GMP's
// mpn_sqr_basecase) computes each cross product x[i] x[j], i < j, once,
// doubles their sum and adds the squares x[i]^2; Karatsuba's square takes
// three half-length squares (GMP's mpn_toom2_sqr, Go's karatsubaSqr);
// Toom-3's takes five third-length squares and the product's interpolation
// (GMP's mpn_toom3_sqr). Each recursion level divides the length by 2 or 3,
// so the depth is at most log2(n) levels.

import std.raw

// SqrBasicWords is the length, in words, from which the schoolbook square
// replaces MulBasic(x, x). Measured at 8 to 64 words with 4, 8 and 16: 4
// and 8 were within noise of each other, and 16 was up to 20 percent
// slower at 12 words (Go's basicSqrThreshold is 12).
const SqrBasicWords: int = 8

// KaraSqrWords is the length, in words, from which Karatsuba's square
// replaces the schoolbook one. Measured at 48 to 4,096 words with 40, 60,
// 80 and 120: 80 was the best or within noise of it at every size, and 40
// was up to 35 percent slower at 48 words (Go's karatsubaSqrThreshold is
// 80).
const KaraSqrWords: int = 80

// ToomSqrWords is the length, in words, from which Toom-3's square replaces
// Karatsuba's. Measured at 256 to 4,096 words with 200, 300 and 400 and
// with Toom-3 off: 200 to 400 were within noise of each other, and Toom-3
// off was 10 to 25 percent slower from 1,024 words.
const ToomSqrWords: int = 300

// SqrBasic sets z = x * x for x of n >= 2 words and z.Len() = 2n.
// O(n^2 / 2) word products.
SqrBasic(z: mut []u64, x: []u64) {
    n := x.Len()
    Clear(z)
    // The cross products, sum over i < j of x[i] x[j] B^(i + j), in
    // z[1..2n - 1]: rows i and i + 1 share x[i + 2..] (one AddMul2VVW
    // pass at offset 2i + 2), and x[i] x[i + 1] goes in at 2i + 1. The sum
    // of the rows so far stays below B^(n + i), so the words AddMul2VVW
    // sets are still zero, and a carry never leaves the words written.
    var i := 0
    for i + 1 <= n - 2 {
        l := n - i - 2
        if l > 0 {
            (lo, hi) := AddMul2VVW(mut z[2 * i + 2..2 * i + 2 + l], x[i + 2..], x[i], x[i + 1])
            z[n + i] = lo
            z[n + i + 1] = hi
        }
        p := raw.Mul128(x[i], x[i + 1])
        s0 := z[2 * i + 1] +% p.lo
        z[2 * i + 1] = s0
        s1 := z[2 * i + 2] +% p.hi
        c1 := Bit1(s1 < p.hi)
        s2 := s1 +% Bit1(s0 < p.lo)
        z[2 * i + 2] = s2
        _ = AddW(mut z[2 * i + 3..], c1 +% Bit1(s2 < s1))
        i += 2
    }
    if i == n - 2 {
        // The last row alone: x[n - 1] x[n - 2] at 2n - 3.
        p := raw.Mul128(x[n - 2], x[n - 1])
        s0 := z[2 * n - 3] +% p.lo
        z[2 * n - 3] = s0
        z[2 * n - 2] = z[2 * n - 2] +% p.hi +% Bit1(s0 < p.lo)
    }
    // z = 2 z + the squares, in one pass over word pairs: each pair is
    // shifted left by one bit (the top bit of the pair below shifted in),
    // then x[i]^2 and the carry added.
    var up: u64 = 0
    var c: u64 = 0
    for k in 0..n {
        p := raw.Mul128(x[k], x[k])
        w0 := z[2 * k]
        w1 := z[2 * k + 1]
        d0 := (w0 << 1) | up
        d1 := (w1 << 1) | (w0 >> 63)
        up = w1 >> 63
        t0 := d0 +% p.lo
        a0 := Bit1(t0 < p.lo)
        t1 := d1 +% p.hi
        a1 := Bit1(t1 < p.hi)
        s0 := t0 +% c
        k0 := Bit1(s0 < c) +% a0
        s1 := t1 +% k0
        c = a1 +% Bit1(s1 < k0)
        z[2 * k] = s0
        z[2 * k + 1] = s1
    }
}

// SqrScratch is the scratch SqrEq needs for an n-word operand at depth. It
// is at most EqScratch(n, depth) for every n and depth, by induction on n:
// each threshold here is at least its product's (KaratsubaWords,
// ToomWords), both recursions lower the depth once per level, Karatsuba's
// square has the product's layout, and Toom-3's square needs 9p words
// beside its recursion where the product needs 12p. So a square never
// needs more than the product of two n-word operands.
SqrScratch(n: int, depth: u8): int {
    if n < KaraSqrWords || depth == 0 {
        return 0
    }
    d := depth - 1
    if n < ToomSqrWords {
        m := (n + 1) / 2
        inner := 4 * m + SqrScratch(m, d)
        return if inner > 6 * m + 1 { inner } else { 6 * m + 1 }
    }
    k := (n + 2) / 3
    r := n - 2 * k
    p := k + 1
    var inner := SqrScratch(p, d)
    sk := SqrScratch(k, d)
    if sk > inner {
        inner = sk
    }
    sr := SqrScratch(r, d)
    if sr > inner {
        inner = sr
    }
    // Three evaluations of p words and three squares of 2p words.
    return 3 * p + 6 * p + inner
}

// SqrEq sets z = x * x for x of n words and z.Len() = 2n, with t,
// SqrScratch(n, depth) words, as scratch; at depth 0, the schoolbook
// square.
SqrEq(z: mut []u64, x: []u64, t: mut []u64, depth: u8) {
    n := x.Len()
    if n >= ToomSqrWords && depth > 0 {
        Toom3Sqr(z, x, t, depth - 1)
    } else if n >= KaraSqrWords && depth > 0 {
        KaraSqr(z, x, t, depth - 1)
    } else if n >= SqrBasicWords {
        SqrBasic(z, x)
    } else {
        MulBasic(z, x, x)
    }
}

// KaraSqr sets z = x * x for x of n words and z.Len() = 2n, with t,
// SqrScratch(n, depth + 1) words, as scratch: z0 = x0^2, z2 = x1^2 and z1 =
// z0 + z2 - (x0 - x1)^2, the last square never negative, each square
// SqrEq's at depth.
KaraSqr(z: mut []u64, x: []u64, t: mut []u64, depth: u8) {
    n := x.Len()
    m := (n + 1) / 2
    h := n - m
    SqrEq(mut z[..2 * m], x[..m], mut t[..], depth)
    SqrEq(mut z[2 * m..], x[m..], mut t[..], depth)
    _ = AbsDiff(mut t[..m], x[..m], x[m..])
    KaraSqrMiddle(t, m, depth)
    (left: lo, right: hi) := t.SplitAt(4 * m)
    w := mut hi[..2 * m + 1]
    w[2 * m] = AddPad(mut w[..2 * m], z[..2 * m], z[2 * m..2 * m + 2 * h])
    _ = SubFrom(mut w[..], lo[2 * m..4 * m])
    _ = AddTo(mut z[m..], Norm(w[..]))
}

// KaraSqrMiddle sets t[2m..4m] = t[..m]^2, with t[4m..] as scratch, by
// SqrEq at depth.
KaraSqrMiddle(t: mut []u64, m: int, depth: u8) {
    (left: d, right: rest) := t.SplitAt(2 * m)
    (left: p, right: more) := rest.SplitAt(2 * m)
    SqrEq(p, d[..m], more, depth)
}

// Toom3Sqr sets z = x * x for x of n >= ToomSqrWords words and z.Len() =
// 2n, with t, SqrScratch(n, depth + 1) words, as scratch: Toom3's
// evaluation and interpolation (toom.mk), with the five products squares,
// SqrEq's at depth, so every value at a point is non-negative.
Toom3Sqr(z: mut []u64, x: []u64, t: mut []u64, depth: u8) {
    n := x.Len()
    k := (n + 2) / 3
    p := k + 1
    l := 2 * p
    (left: e1, right: t1) := t.SplitAt(p)
    (left: em1, right: t2) := t1.SplitAt(p)
    (left: em2, right: t3) := t2.SplitAt(p)
    (left: a, right: t4) := t3.SplitAt(l)
    (left: b, right: t5) := t4.SplitAt(l)
    (left: c, right: rest) := t5.SplitAt(l)
    _ = ToomEval(mut e1[..], mut em1[..], mut em2[..], x[..k], x[k..2 * k], x[2 * k..])
    SqrEq(mut a[..], e1[..], mut rest[..], depth)
    SqrEq(mut b[..], em1[..], mut rest[..], depth)
    SqrEq(mut c[..], em2[..], mut rest[..], depth)
    SqrEq(mut z[..2 * k], x[..k], mut rest[..], depth)
    SqrEq(mut z[4 * k..], x[2 * k..], mut rest[..], depth)
    ToomInterp(z, mut a[..], mut b[..], mut c[..], k)
}
