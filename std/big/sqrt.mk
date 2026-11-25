// Why: an integer square root by Newton's method alone costs O(log n)
// full-size divisions; the recursive method costs about a few.
//
// What: Int.Sqrt, with SqrtRem, SqrtNewton and the threshold SqrtLeafWords.
//
// How: Zimmermann's Karatsuba square root ("Karatsuba Square Root", INRIA
// research report 3805, 1999; Brent and Zimmermann, Modern Computer
// Arithmetic, 1.5.2, SqrtRem; GMP's mpn_dc_sqrtrem). The value is shifted
// left by an even number of bits to 2N words whose top word has one of its
// two top bits set; the root of the top 2h words (h = N - N/2) is found
// recursively, with its remainder, and one division of that remainder by
// twice the root gives the next N/2 words of the root, corrected by at most
// one step down when the new remainder is negative. Below SqrtLeafWords
// words of root, Newton's iteration (Go's nat.sqrt) finishes. The cost is
// that of one division and one squaring at each of log2(n) levels of
// halving size. The recursion halves its length at each level: at most
// log2(n) deep.

import std.raw

// SqrtLeafWords is the root length, in words, at or below which SqrtRem
// runs Newton's iteration instead of recursing (measured at 4 to 4,000
// words of x: 1 to 4 within noise of each other, 8 up to 35 percent
// slower at 64 words and 32 up to seven times).
const SqrtLeafWords: int = 2

// SqrtNewton is floor(sqrt(x)) for the normalized, nonzero x, in a, by
// Newton's iteration z' = (z + x / z) / 2 from 2^ceil(bits / 2) > sqrt(x),
// which decreases strictly until it reaches the root.
SqrtNewton(a: &Arena, x: []u64): OutOfMemory![]u64 from a {
    n := x.Len()
    bits := 64 * n - raw.Clz64(x[n - 1])
    k := (bits + 1) / 2
    zw := (k >> 6) + 2
    var z := try a.Slice[u64](zw)
    var z2 := try a.Slice[u64](zw)
    z[k >> 6] = u64(1) << (k & 63)
    var lz := (k >> 6) + 1
    q := try a.Slice[u64](n + 1)
    r := try a.Slice[u64](n + 1)
    s := try a.Slice[u64](n + 1)
    for {
        // z2 = (x / z + z) / 2.
        (lq, lr: _) := QuoRemInto(mut q[..], mut r[..], x, z[..lz], mut s[..])
        Clear(mut z2[..])
        Copy(mut z2[..], q[..lq])
        _ = AddTo(mut z2[..], z[..lz])
        Halve(mut z2[..])
        l2 := Norm(z2[..]).Len()
        if CmpVV(Norm(z2[..l2]), z[..lz]) >= 0 {
            return z[..lz]
        }
        Swap(mut z, mut z2)
        lz = l2
    }
}

// TwiceLess1 is 2 x - 1 for the normalized, nonzero x, in a, normalized.
TwiceLess1(a: &Arena, x: []u64): OutOfMemory![]u64 from a {
    t := try a.Slice[u64](x.Len() + 1)
    t[x.Len()] = ShlVU(mut t[..x.Len()], x, 1)
    _ = SubW(mut t[..], 1)
    return Norm(t[..])
}

// SqrtRem is the root floor(sqrt(x)) and the remainder x - root^2 of x of
// 2N words whose top word has one of its two top bits set, both
// normalized, in a. At depth 0 it takes SqrtNewton.
SqrtRem(a: &Arena, x: []u64, depth: u8): OutOfMemory!(root: []u64, rem: []u64) from a {
    nn := x.Len() / 2
    if nn <= SqrtLeafWords || depth == 0 {
        root := try SqrtNewton(a, x)
        return (root: root, rem: try SubMag(a, x, try MulMag(a, root, root)))
    }
    l := nn / 2
    // x = hi B^2l + x1 B^l + x0, B = 2^64, hi of 2h words, normalized.
    (root: s1, rem: r1) := try SqrtRem(a, x[2 * l..], depth - 1)
    // (q, u) = (r1 B^l + x1) / (2 s1).
    num := try a.Slice[u64](l + r1.Len())
    Copy(mut num[..l], x[l..2 * l])
    Copy(mut num[l..], r1)
    den := try a.Slice[u64](s1.Len() + 1)
    den[s1.Len()] = ShlVU(mut den[..s1.Len()], s1, 1)
    (q, r: u) := try DivMag(a, Norm(num[..]), Norm(den[..]))
    // root = s1 B^l + q (q may be B^l).
    w := try a.Slice[u64](l + s1.Len() + 1)
    Copy(mut w[l..], s1)
    _ = AddTo(mut w[..], q)
    var root := Norm(w[..])
    // rem = u B^l + x0 - q^2, negative at most once (SqrtRem's Theorem).
    p := try a.Slice[u64](l + u.Len())
    Copy(mut p[..], x[..l])
    Copy(mut p[l..], u)
    pos := Norm(p[..])
    qq := if q.Len() == 0 { q } else { try MulMag(a, q, q) }
    if CmpVV(pos, qq) >= 0 {
        return (root: root, rem: try SubMag(a, pos, qq))
    }
    // The remainder is -d: step the root down, adding 2 root - 1, until it
    // is not negative.
    var d := try SubMag(a, qq, pos)
    for {
        t := try TwiceLess1(a, root)
        _ = SubW(mut w[..], 1)
        root = Norm(w[..])
        if CmpVV(t, d) >= 0 {
            return (root: root, rem: try SubMag(a, t, d))
        }
        d = try SubMag(a, d, t)
    }
}

/// floor(sqrt(x)). Panics when x is negative. Fails with OutOfMemory when a
/// cannot give the words.
///
/// For n-word x below about 240 words it costs O(n^2) word operations.
/// Above, it costs O(M(n) log n), M being the product's cost (Zimmermann's
/// Karatsuba square root over recursive division). For x of up to 4 words
/// it allocates at most 23 words in a. From 5 words it allocates the root's
/// n/2 + 1 words in a, and about 14n scratch words plus each division's own
/// in a sub-arena it frees before returning.
export Int.Sqrt(&self, a: &Arena): OutOfMemory!Int from a {
    if self.negative {
        Panic("big.Int.Sqrt: negative value")
    }
    x := self.mag
    n := x.Len()
    if n == 0 {
        return try Zero(a)
    }
    if n <= 2 * SqrtLeafWords {
        return Make(try SqrtNewton(a, x), false)
    }
    var s := try a.Sub()
    defer s.Free()
    // xn = x 2^(2 sh) over an even number of words, top two bits not both
    // zero; the root of x is then the root of xn shifted right by sh.
    t := raw.Clz64(x[n - 1]) / 2
    odd := n % 2 == 1
    m := if odd { n + 1 } else { n }
    off := if odd { 1 } else { 0 }
    sh := t + 32 * off
    xn := try s.Slice[u64](m)
    if t == 0 {
        Copy(mut xn[off..], x)
    } else {
        _ = ShlVU(mut xn[off..], x, 2 * t)
    }
    (root: r, rem: _) := try SqrtRem(&s, xn[..], DepthLimit)
    z := try a.Slice[u64](r.Len())
    if sh == 0 {
        Copy(mut z[..], r)
    } else {
        _ = ShrVU(mut z[..], r, sh, 0)
    }
    return Make(z[..], false)
}
