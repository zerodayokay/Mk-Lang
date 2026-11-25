// Why: multiplication sets the cost of nearly everything else in std.big, so
// it switches to asymptotically faster methods as operands grow.
//
// What: Int.Mul, MulMag, MulNat, the schoolbook MulBasic, Karatsuba with its
// scratch sizing, and the threshold KaratsubaWords.
//
// How: the schoolbook product (TAOCP vol. 2, 4.3.1, Algorithm M) below
// KaratsubaWords words, and above it Karatsuba's (1962) in its subtractive
// form, z1 = z0 + z2 + (x0 - x1)(y1 - y0), whose middle factors stay at the
// half length (GMP's mpn_kara_mul_n, Go's karatsuba), and from ToomWords
// words Toom-3 (toom.mk). An unbalanced product is cut into pieces of the
// shorter operand's length. The recursion halves the length at each level,
// so its depth is at most log2(n) levels, below 64.

// KaratsubaWords is the operand length, in words, from which Karatsuba's
// product replaces the schoolbook one (Go's karatsubaThreshold is 40;
// measured here, 24 to 48 words are within noise of each other and 64 is
// slower from 64 words up).
const KaratsubaWords: int = 40

// MulBasic sets z = x * y, z.Len() = x.Len() + y.Len(). O(x.Len() * y.Len()).
MulBasic(z: mut []u64, x: []u64, y: []u64) {
    n := x.Len()
    m := y.Len()
    if m == 0 || n == 0 {
        Clear(z)
        return
    }
    z[n] = MulAddVWW(mut z[..n], x, y[0], 0)
    // Two rows a pass (AddMul2VVW), then the odd row left.
    var j := 1
    for j + 1 < m {
        (lo, hi) := AddMul2VVW(mut z[j..j + n], x, y[j], y[j + 1])
        z[n + j] = lo
        z[n + j + 1] = hi
        j += 2
    }
    if j < m {
        z[n + j] = AddMulVVW(mut z[j..j + n], x, y[j])
    }
}

// KaraScratch is the number of scratch words Karatsuba needs for two
// n-word operands at depth: 4 ceil(n / 2) at this level beside the
// recursion's, and at least 6 ceil(n / 2) + 1 for the middle sum.
KaraScratch(n: int, depth: u8): int {
    if n < KaratsubaWords || depth == 0 {
        return 0
    }
    m := (n + 1) / 2
    inner := 4 * m + KaraScratch(m, depth - 1)
    return if inner > 6 * m + 1 { inner } else { 6 * m + 1 }
}

// CmpPad orders x and y as numbers, the shorter one zero-extended: -1, 0
// or 1.
CmpPad(x: []u64, y: []u64): int {
    var i := if x.Len() > y.Len() { x.Len() } else { y.Len() }
    i -= 1
    for i >= 0 {
        xi: u64 = if i < x.Len() { x[i] } else { 0 }
        yi: u64 = if i < y.Len() { y[i] } else { 0 }
        if xi != yi {
            return if xi < yi { -1 } else { 1 }
        }
        i -= 1
    }
    return 0
}

// AbsDiff sets z = |x - y| over z.Len() words (x and y zero-extended to it)
// and reports whether x < y: one comparison from the top, which stops at
// the first differing word, then one subtraction pass.
AbsDiff(z: mut []u64, x: []u64, y: []u64): bool {
    neg := CmpPad(x, y) < 0
    hi := if neg { y } else { x }
    lo := if neg { x } else { y }
    // hi >= lo, so lo's words above hi.Len() are zero.
    nl := if lo.Len() < hi.Len() { lo.Len() } else { hi.Len() }
    var b := SubVV(mut z[..nl], hi, lo)
    for i in nl..hi.Len() {
        d := hi[i]
        z[i] = d -% b
        b = Bit1(d < b)
    }
    Clear(mut z[hi.Len()..])
    return neg
}

// AddPad sets z = x + y for z.Len() = x.Len() >= y.Len() and returns the
// carry out.
AddPad(z: mut []u64, x: []u64, y: []u64): u64 {
    ny := y.Len()
    var c := AddVV(mut z[..ny], x, y)
    for i in ny..z.Len() {
        s := x[i] +% c
        z[i] = s
        c = Bit1(s < c)
    }
    return c
}

// Karatsuba sets z = x * y for x.Len() = y.Len() = n and z.Len() = 2n,
// using t, KaraScratch(n, depth) words, as scratch; at depth 0 it takes the
// schoolbook product.
Karatsuba(z: mut []u64, x: []u64, y: []u64, t: mut []u64, depth: u8) {
    n := x.Len()
    if n < KaratsubaWords || depth == 0 {
        MulBasic(z, x, y)
        return
    }
    m := (n + 1) / 2
    h := n - m
    d := depth - 1
    // z0 = x0 y0 in z[..2m], z2 = x1 y1 in z[2m..].
    Karatsuba(mut z[..2 * m], x[..m], y[..m], mut t[..], d)
    Karatsuba(mut z[2 * m..], x[m..], y[m..], mut t[..], d)
    // p = (x0 - x1)(y1 - y0), its sign the product of the two differences'.
    xneg := AbsDiff(mut t[..m], x[..m], x[m..])
    yneg := AbsDiff(mut t[m..2 * m], y[m..], y[..m])
    KaraMiddle(t, m, d)
    // w = z0 + z2 + p (p subtracted when the signs differ), then z += w B^m.
    (left: lo, right: hi) := t.SplitAt(4 * m)
    w := mut hi[..2 * m + 1]
    w[2 * m] = AddPad(mut w[..2 * m], z[..2 * m], z[2 * m..2 * m + 2 * h])
    if xneg == yneg {
        _ = AddTo(mut w[..], lo[2 * m..4 * m])
    } else {
        _ = SubFrom(mut w[..], lo[2 * m..4 * m])
    }
    _ = AddTo(mut z[m..], Norm(w[..]))
}

// KaraMiddle sets t[2m..4m] = t[..m] * t[m..2m], with t[4m..] as scratch,
// by Karatsuba at depth.
KaraMiddle(t: mut []u64, m: int, depth: u8) {
    (left: dx, right: rest) := t.SplitAt(m)
    (left: dy, right: rest2) := rest.SplitAt(m)
    (left: p, right: more) := rest2.SplitAt(2 * m)
    Karatsuba(p, dx[..], dy[..], more, depth)
}

// MulNat sets z = x * y, z.Len() = x.Len() + y.Len(), with scratch t of
// MulScratch(x.Len(), y.Len()) words.
//
// Operands of unequal lengths n > k are cut into pieces of k words, each
// multiplied by the shorter operand and added into z at its offset; the
// last piece, shorter than k, then takes the shorter operand's role, as in
// Euclid's algorithm on the two lengths. Each round's products sit in t
// after the round before's, and its leftover product is added straight
// into z at the offset where it belongs.
MulNat(z: mut []u64, x: []u64, y: []u64, t: mut []u64) {
    // a is the longer operand, b the shorter.
    var a := if x.Len() < y.Len() { y } else { x }
    var b := if x.Len() < y.Len() { x } else { y }
    if b.Len() < KaratsubaWords {
        MulBasic(z, a, b)
        return
    }
    if a.Len() == b.Len() {
        MulEq(z, a, b, t, DepthLimit)
        return
    }
    Clear(z)
    var off := 0  // a * b is added into z at off
    var at := 0   // this round's scratch is t[at..]
    var last := 0 // the round before's 2k-word product is t[last..at]
    for {
        n := a.Len()
        k := b.Len()
        if k < KaratsubaWords {
            // n + k <= 2 (the round before's k): its product's words.
            p := mut t[last..last + n + k]
            MulBasic(p, a, b)
            _ = AddTo(mut z[off..], p[..])
            return
        }
        (left: prod, right: kt) := (mut t[at..]).SplitAt(2 * k)
        var i := 0
        for i + k <= n {
            MulEq(mut prod[..], a[i..i + k], b, mut kt[..], DepthLimit)
            _ = AddTo(mut z[off + i..], prod[..])
            i += k
        }
        if i == n {
            return
        }
        // The rest a[i..], shorter than b, times b, at off + i.
        rest := a[i..]
        a = b
        b = rest
        off += i
        last = at
        at += 2 * k
    }
}

// MulScratch is the scratch MulNat needs for operands of n and k words:
// over MulNat's rounds, the most that a round's 2k-word product, after the
// rounds before's, and its products' scratch need.
MulScratch(n: int, k: int): int {
    var a := if n < k { k } else { n }
    var b := if n < k { n } else { k }
    if b < KaratsubaWords {
        return 0
    }
    if a == b {
        return EqScratch(a, DepthLimit)
    }
    var at := 0
    var most := 0
    for b >= KaratsubaWords {
        need := at + 2 * b + EqScratch(b, DepthLimit)
        if need > most {
            most = need
        }
        rest := a % b
        if rest == 0 {
            break
        }
        at += 2 * b
        a = b
        b = rest
    }
    return most
}

// MulMag is x * y for magnitudes, in a, normalized, with its scratch in a:
// a square (sqr.mk) when x and y hold the same value, found in O(n) by
// comparing the words, which the product's cost dwarfs.
MulMag(a: &Arena, x: []u64, y: []u64): OutOfMemory![]u64 from a {
    z := try a.Slice[u64](x.Len() + y.Len())
    if x.Len() == y.Len() && CmpVV(x, y) == 0 {
        t := try a.Slice[u64](SqrScratch(x.Len(), DepthLimit))
        SqrEq(mut z[..], x, mut t[..], DepthLimit)
        return Norm(z[..])
    }
    t := try a.Slice[u64](MulScratch(x.Len(), y.Len()))
    MulNat(mut z[..], x, y, mut t[..])
    return Norm(z[..])
}

/// x * y. Fails with OutOfMemory when a cannot give the words. For
/// operands of n and m words: O(n * m) below 40 words, O(n^1.585)
/// (Karatsuba) below 300 and O(n^1.465) (Toom-3) above. When x and y hold
/// the same value, x.Mul(a, &x) included, it squares, with about half the
/// word products. It allocates n + m words for the product and, from 40
/// words, at most 10 min(n, m) scratch words in a.
export Int.Mul(&self, a: &Arena, y: &Int): OutOfMemory!Int from a {
    return Make(try MulMag(a, self.mag, y.mag), self.negative != y.negative)
}
