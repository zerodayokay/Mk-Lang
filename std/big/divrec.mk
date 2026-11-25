// Why: schoolbook division is quadratic; long divisions (in Int.DivMod,
// Int.Mod and Int.Text) need a method that rides on fast multiplication.
//
// What: DivLarge, the recursive division, with DivRecStep, DivFix, DivLeaf and
// the threshold DivRecWords.
//
// How: Burnikel and Ziegler, "Fast Recursive Division" (Max-Planck-Institut
// fuer Informatik, 1998), in the block form of Go's nat.divRecursiveStep.
// The quotient is produced B = n/2 words at a time: each block's estimate is
// a recursive division of the top of the remainder by the top n - B + 1
// words of the divisor, too large by at most two (the paper's Lemma 2),
// corrected by one product with the divisor's low words. Its cost is that of
// O(m / n) products of n/2 words at each of log2(n / DivRecWords) levels, so
// O(M(n) log n) for a 2n-by-n division. The recursion halves the divisor at
// each level: at most log2(n) deep.

import std.raw

// DivRecWords is the divisor length, in words, from which division
// recurses. Measured with the two-row kernels and Toom-3 at 2n by n words
// for n = 200 to 4,000: 40 to 100 were within noise of each other, 200 was
// 1 to 7 percent slower and 300 up to 20 percent. Go's
// divRecursiveThreshold is 40.
const DivRecWords: int = 60

// DivLarge sets q = x / y and r = x mod y for normalized x >= y, y of at
// least DivRecWords words, as DivNat does, with scratch in a sub-arena of a
// freed before it returns.
DivLarge(a: &Arena, q: mut []u64, r: mut []u64, x: []u64, y: []u64): OutOfMemory!void {
    var s := try a.Sub()
    defer s.Free()
    n := y.Len()
    sh := raw.Clz64(y[n - 1])
    u := try s.Slice[u64](x.Len() + 1)
    v := try s.Slice[u64](n)
    if sh == 0 {
        Copy(mut u[..], x)
        Copy(mut v[..], y)
    } else {
        u[x.Len()] = ShlVU(mut u[..x.Len()], x, sh)
        _ = ShlVU(mut v[..], y, sh)
    }
    Clear(q)
    try DivRecStep(&s, q, mut u[..], v[..], DepthLimit)
    if sh == 0 {
        Copy(r, u[..n])
    } else {
        _ = ShrVU(r, u[..n], sh, 0)
    }
}

// DivRecStep divides u by v in place: the quotient is added into q, whose
// words at the quotient's positions are zero on entry, and the remainder
// replaces u. v is normalized (its top bit set) and u < v * 2^(64 (q.Len()));
// its scratch is allocated in s. At depth 0 it divides by DivLeaf.
DivRecStep(s: &Arena, q: mut []u64, u: mut []u64, v: []u64, depth: u8): OutOfMemory!void {
    ul := Norm(u[..]).Len()
    n := v.Len()
    if ul < n || (ul == n && CmpVV(u[..ul], v) < 0) {
        return
    }
    if n < DivRecWords || depth == 0 {
        try DivLeaf(s, q, mut u[..ul], v)
        return
    }
    m := ul - n
    d := depth - 1
    b := n / 2
    sh := b - 1
    var j := m
    for j > b {
        // The block u[j - b .. j + n]: its quotient's estimate from the top
        // words of u and of v, then the correction by v's low sh words.
        uu := mut u[j - b..ul]
        qhat := try s.Slice[u64](b + 3)
        try DivRecStep(s, mut qhat[..], mut uu[sh..b + n], v[sh..], d)
        try DivFix(s, mut qhat[..], mut uu[..], v, sh)
        _ = AddTo(mut q[j - b..], Norm(qhat[..]))
        j -= b
    }
    // The last block, below 2^(64 b) times v.
    qhat := try s.Slice[u64](j + 3)
    try DivRecStep(s, mut qhat[..], mut u[sh..ul], v[sh..], d)
    try DivFix(s, mut qhat[..], mut u[..ul], v, sh)
    _ = AddTo(mut q[..], Norm(qhat[..]))
}

// DivFix completes one block: uu holds rh 2^(64 sh) + ul after the top
// division by v[sh..], and the remainder is uu - qhat * v[..sh]; while that
// is negative (at most twice), qhat is one too large and v[sh..] goes back
// into rh.
DivFix(s: &Arena, qhat: mut []u64, uu: mut []u64, v: []u64, sh: int): OutOfMemory!void {
    qh := Norm(qhat[..])
    p := try s.Slice[u64](qh.Len() + sh)
    t := try s.Slice[u64](MulScratch(qh.Len(), sh))
    MulNat(mut p[..], qh, v[..sh], mut t[..])
    for _ in 0..2 {
        if CmpVV(Norm(p[..]), Norm(uu[..])) <= 0 {
            break
        }
        _ = SubW(qhat, 1)
        _ = SubFrom(mut p[..], v[..sh])
        _ = AddTo(mut uu[sh..], v[sh..])
    }
    _ = SubFrom(uu, Norm(p[..]))
}

// DivLeaf divides u by v (normalized, at least two words, u >= v) by
// Algorithm D, adding the quotient into q and leaving the remainder in u.
DivLeaf(s: &Arena, q: mut []u64, u: mut []u64, v: []u64): OutOfMemory!void {
    n := v.Len()
    ul := u.Len()
    w := try s.Slice[u64](ul + 1)
    Copy(mut w[..], u[..])
    qq := try s.Slice[u64](ul - n + 1)
    if n == 1 {
        r := DivVW(mut qq[..], u[..], v[0])
        Clear(u)
        u[0] = r
    } else {
        DivKnuth(mut qq[..], mut w[..], v)
        Clear(u)
        Copy(u, w[..n])
    }
    _ = AddTo(q, Norm(qq[..]))
}
