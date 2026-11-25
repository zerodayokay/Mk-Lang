// Why: past a few hundred words Karatsuba's O(n^1.585) is beaten by Toom-3's
// O(n^1.465).
//
// What: Toom3, ToomEval and ToomInterp, the equal-length MulEq, and the
// threshold ToomWords.
//
// How: Toom 1963, Cook 1966. Each operand is cut in three, evaluated at 0, 1,
// -1, -2 and infinity, the five products are taken (by Toom-3 again or
// Karatsuba), and the product is interpolated by Bodrato's sequence (Bodrato
// and Zanoni 2007, as GMP's mpn_toom33_mul and mpn_toom3_interpolate).
// Signed values are two's complement over a fixed width, so the signed steps
// are word additions and subtractions, one exact division by 3 (Jebelean
// 1993, as GMP's mpn_divexact_by3) and an arithmetic shift. The recursion
// divides the length by about 3 at each level.

// ToomWords is the operand length, in words, from which Toom-3 replaces
// Karatsuba for equal-length products. Measured at 64 to 4,096 words with
// thresholds of 100 to 500 and with Toom-3 off: Toom-3 is within noise of
// Karatsuba at 256 to 400 words and 8 to 19 percent faster from 640 to
// 4,096, and 300 was the best or within noise of it at every size.
const ToomWords: int = 300

// MulEq sets z = x * y for x.Len() = y.Len() = n and z.Len() = 2n, with t,
// EqScratch(n, depth) words, as scratch: Toom-3 from ToomWords words,
// Karatsuba (or the schoolbook product) below; at depth 0, the schoolbook
// product.
MulEq(z: mut []u64, x: []u64, y: []u64, t: mut []u64, depth: u8) {
    if x.Len() >= ToomWords && depth > 0 {
        Toom3(z, x, y, t, depth - 1)
        return
    }
    Karatsuba(z, x, y, t, depth)
}

// EqScratch is the scratch MulEq needs for two n-word operands at depth.
EqScratch(n: int, depth: u8): int {
    if n < ToomWords || depth == 0 {
        return KaraScratch(n, depth)
    }
    d := depth - 1
    k := (n + 2) / 3
    r := n - 2 * k
    p := k + 1
    var inner := EqScratch(p, d)
    sk := EqScratch(k, d)
    if sk > inner {
        inner = sk
    }
    sr := EqScratch(r, d)
    if sr > inner {
        inner = sr
    }
    // Six evaluations of p words and three products of 2p words.
    return 6 * p + 6 * p + inner
}

// IsNegTC is whether the two's complement value z is negative.
IsNegTC(z: []u64): bool {
    return z[z.Len() - 1] >> 63 == 1
}

// NegTC sets z = -z in two's complement over z.Len() words.
NegTC(z: mut []u64) {
    var c: u64 = 1
    for i in 0..z.Len() {
        v := ~z[i] +% c
        c = Bit1(v < c)
        z[i] = v
    }
}

// Sar1 sets z = z / 2 for an even two's complement z (an arithmetic shift
// right by one bit, the sign bit kept).
Sar1(z: mut []u64) {
    n := z.Len()
    for i in 0..n - 1 {
        z[i] = (z[i] >> 1) | (z[i + 1] << 63)
    }
    top := z[n - 1]
    z[n - 1] = (top >> 1) | (top & 0x8000000000000000)
}

// Dbl1 sets z = 2 z modulo 2^(64 z.Len()).
Dbl1(z: mut []u64) {
    var i := z.Len() - 1
    for i > 0 {
        z[i] = (z[i] << 1) | (z[i - 1] >> 63)
        i -= 1
    }
    z[0] = z[0] << 1
}

// DivExact3 sets z = z / 3 for z a multiple of 3, two's complement over
// z.Len() words: each quotient word is the remaining word times the
// inverse of 3 modulo 2^64, and the high word of 3 times it is the borrow
// into the next word (Jebelean's exact division).
DivExact3(z: mut []u64) {
    var c: u64 = 0
    for i in 0..z.Len() {
        x := z[i]
        s := x -% c
        b := Bit1(x < c)
        q := s *% 0xAAAAAAAAAAAAAAAB
        z[i] = q
        // 3 q = s + 2^64 h, so x - c = 3 q - 2^64 (h + b).
        // h is 0, 1 or 2 by where q lies against 2^64 / 3 and 2^65 / 3.
        h := Bit1(q >= 0x5555555555555556) + Bit1(q >= 0xAAAAAAAAAAAAAAAB)
        c = h +% b
    }
}

// ToomEval sets e1 = x0 + x1 + x2, em1 = x0 - x1 + x2 and em2 = x0 - 2 x1
// + 4 x2 in two's complement over e1.Len() = k + 1 words (x0 and x1 of k
// words, x2 of at most k), and returns whether em1 and em2 were negative;
// em1 and em2 are left as their magnitudes. |em1| < 2^(64 k) and
// -2^(64 k + 1) < em2 < 5 2^(64 k), so each fits with its sign.
ToomEval(e1: mut []u64, em1: mut []u64, em2: mut []u64, x0: []u64, x1: []u64, x2: []u64): (n1: bool, n2: bool) {
    k := x0.Len()
    e1[k] = AddPad(mut e1[..k], x0, x2)
    b := SubVV(mut em1[..k], e1[..k], x1)
    em1[k] = e1[k] -% b
    _ = AddTo(mut e1[..], x1)
    // em2 = 2 (em1 + x2) - x0.
    Copy(mut em2[..], em1[..])
    _ = AddTo(mut em2[..], x2)
    Dbl1(mut em2[..])
    _ = SubFrom(mut em2[..], x0)
    n1 := IsNegTC(em1[..])
    if n1 {
        NegTC(mut em1[..])
    }
    n2 := IsNegTC(em2[..])
    if n2 {
        NegTC(mut em2[..])
    }
    return (n1: n1, n2: n2)
}

// Toom3 sets z = x * y for x.Len() = y.Len() = n >= ToomWords and z.Len()
// = 2n, with t, EqScratch(n, depth + 1) words, as scratch; its five
// products are MulEq's at depth.
Toom3(z: mut []u64, x: []u64, y: []u64, t: mut []u64, depth: u8) {
    n := x.Len()
    k := (n + 2) / 3
    p := k + 1
    l := 2 * p
    (left: ex1, right: t1) := t.SplitAt(p)
    (left: exm1, right: t2) := t1.SplitAt(p)
    (left: exm2, right: t3) := t2.SplitAt(p)
    (left: ey1, right: t4) := t3.SplitAt(p)
    (left: eym1, right: t5) := t4.SplitAt(p)
    (left: eym2, right: t6) := t5.SplitAt(p)
    (left: a, right: t7) := t6.SplitAt(l)
    (left: b, right: t8) := t7.SplitAt(l)
    (left: c, right: rest) := t8.SplitAt(l)
    sx := ToomEval(mut ex1[..], mut exm1[..], mut exm2[..], x[..k], x[k..2 * k], x[2 * k..])
    sy := ToomEval(mut ey1[..], mut eym1[..], mut eym2[..], y[..k], y[k..2 * k], y[2 * k..])
    // a = r(1), b = r(-1), c = r(-2); r(0) and r(inf) go straight to z.
    MulEq(mut a[..], ex1[..], ey1[..], mut rest[..], depth)
    MulEq(mut b[..], exm1[..], eym1[..], mut rest[..], depth)
    if sx.n1 != sy.n1 {
        NegTC(mut b[..])
    }
    MulEq(mut c[..], exm2[..], eym2[..], mut rest[..], depth)
    if sx.n2 != sy.n2 {
        NegTC(mut c[..])
    }
    MulEq(mut z[..2 * k], x[..k], y[..k], mut rest[..], depth)
    MulEq(mut z[4 * k..], x[2 * k..], y[2 * k..], mut rest[..], depth)
    ToomInterp(z, mut a[..], mut b[..], mut c[..], k)
}

// ToomInterp sets z = r0 + a B^k + b B^2k + c B^3k + rinf B^4k from the
// values at the five points by Bodrato's interpolation, two's complement
// over a.Len() = b.Len() = c.Len() words: on entry z[..2k] holds r(0),
// z[4k..] r(inf), and a, b and c the values at 1, -1 and -2.
ToomInterp(z: mut []u64, a: mut []u64, b: mut []u64, c: mut []u64, k: int) {
    _ = SubFrom(mut c[..], a[..])
    DivExact3(mut c[..])
    _ = SubFrom(mut a[..], b[..])
    Sar1(mut a[..])
    _ = SubFrom(mut b[..], z[..2 * k])
    _ = SubFrom(mut c[..], b[..])
    NegTC(mut c[..])
    Sar1(mut c[..])
    _ = AddTo(mut c[..], z[4 * k..])
    _ = AddTo(mut c[..], z[4 * k..])
    _ = AddTo(mut b[..], a[..])
    _ = SubFrom(mut b[..], z[4 * k..])
    _ = SubFrom(mut a[..], c[..])
    // z = r0 + a B^k + b B^2k + c B^3k + rinf B^4k; a, b and c are the
    // product's coefficients, so non-negative.
    Clear(mut z[2 * k..4 * k])
    _ = AddTo(mut z[k..], Norm(a[..]))
    _ = AddTo(mut z[2 * k..], Norm(b[..]))
    _ = AddTo(mut z[3 * k..], Norm(c[..]))
}
