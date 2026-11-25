// Why: the signed operations are thin layers over unsigned kernels; keeping
// the sign logic in one file keeps the kernels free of it.
//
// What: Int.Add, Sub, Shl, Shr, DivMod and Mod, with AddMag, SubMag, DivMag,
// RemMag and ModMag.
//
// How: signs are handled here, magnitudes in arith.mk and div.mk. Each
// result is new words in the arena.

// AddMag is x + y for magnitudes, in a, normalized.
AddMag(a: &Arena, u: []u64, v: []u64): OutOfMemory![]u64 from a {
    // x is the longer operand, y the shorter.
    x := if u.Len() < v.Len() { v } else { u }
    y := if u.Len() < v.Len() { u } else { v }
    n := x.Len()
    m := y.Len()
    z := try a.Slice[u64](n + 1)
    var c := AddVV(mut z[..m], x, y)
    for i in m..n {
        s := x[i] +% c
        c = Bit1(s < c)
        z[i] = s
    }
    z[n] = c
    return Norm(z[..])
}

// SubMag is x - y for magnitudes with x >= y, in a, normalized.
SubMag(a: &Arena, x: []u64, y: []u64): OutOfMemory![]u64 from a {
    n := x.Len()
    m := y.Len()
    z := try a.Slice[u64](n)
    var b := SubVV(mut z[..m], x, y)
    for i in m..n {
        d := x[i]
        z[i] = d -% b
        b = Bit1(d < b)
    }
    return Norm(z[..])
}

// AddSigned is (-1)^xn |x| + (-1)^yn |y|, in a.
AddSigned(a: &Arena, x: []u64, xn: bool, y: []u64, yn: bool): OutOfMemory!Int from a {
    if xn == yn {
        return Make(try AddMag(a, x, y), xn)
    }
    if CmpVV(x, y) >= 0 {
        return Make(try SubMag(a, x, y), xn)
    }
    return Make(try SubMag(a, y, x), yn)
}

/// x + y. Fails with OutOfMemory when a cannot give the words. O(n);
/// allocates max(n, m) + 1 words in a.
export Int.Add(&self, a: &Arena, y: &Int): OutOfMemory!Int from a {
    return try AddSigned(a, self.mag, self.negative, y.mag, y.negative)
}

/// x - y. Fails with OutOfMemory when a cannot give the words. O(n);
/// allocates max(n, m) + 1 words in a.
export Int.Sub(&self, a: &Arena, y: &Int): OutOfMemory!Int from a {
    return try AddSigned(a, self.mag, self.negative, y.mag, !y.negative)
}

/// x * 2^n. Panics when n is negative. Fails with OutOfMemory when a cannot
/// give the words. O(len + n / 64); allocates len + n / 64 + 1 words in a.
export Int.Shl(&self, a: &Arena, n: int): OutOfMemory!Int from a {
    if n < 0 {
        Panic("big.Int.Shl: negative shift")
    }
    x := self.mag
    if x.Len() == 0 {
        return try Zero(a)
    }
    w := n >> 6
    s := n & 63
    z := try a.Slice[u64](x.Len() + w + 1)
    if s == 0 {
        Copy(mut z[w..], x)
    } else {
        z[x.Len() + w] = ShlVU(mut z[w..x.Len() + w], x, s)
    }
    return Make(z[..], self.negative)
}

/// x / 2^n rounded toward negative infinity, as >> is on the fixed signed
/// types: -1 >> n is -1. Panics when n is negative. Fails with OutOfMemory
/// when a cannot give the words. O(len); allocates len - n / 64 words in a.
export Int.Shr(&self, a: &Arena, n: int): OutOfMemory!Int from a {
    if n < 0 {
        Panic("big.Int.Shr: negative shift")
    }
    x := self.mag
    w := n >> 6
    s := n & 63
    if w >= x.Len() {
        if self.negative {
            return try FromI64(a, -1)
        }
        return try Zero(a)
    }
    // A negative value whose shifted-out bits are not all zero rounds one
    // further from zero.
    var lost := false
    if self.negative {
        for k in 0..w {
            if x[k] != 0 {
                lost = true
                break
            }
        }
    }
    z := try a.Slice[u64](x.Len() - w + 1)
    if s == 0 {
        Copy(mut z[..], x[w..])
    } else {
        out := ShrVU(mut z[..x.Len() - w], x[w..], s, 0)
        lost = lost || out != 0
    }
    if self.negative && lost {
        _ = AddW(mut z[..], 1)
    }
    return Make(z[..], self.negative)
}

// DivMag sets q = x / y and r = x mod y for magnitudes, y not zero, both
// in a, normalized.
DivMag(a: &Arena, x: []u64, y: []u64): OutOfMemory!(q: []u64, r: []u64) from a {
    if CmpVV(x, y) < 0 {
        q := try a.Slice[u64](0)
        r := try a.Slice[u64](x.Len())
        Copy(mut r[..], x)
        return (q: q[..], r: r[..])
    }
    q := try a.Slice[u64](x.Len() - y.Len() + 1)
    r := try a.Slice[u64](y.Len())
    if y.Len() == 1 {
        r[0] = DivVW(mut q[..], x, y[0])
    } else if y.Len() >= DivRecWords {
        try DivLarge(a, mut q[..], mut r[..], x, y)
    } else {
        scratch := try a.Slice[u64](x.Len() + 1)
        DivNat(mut q[..], mut r[..], x, y, mut scratch[..])
    }
    return (q: Norm(q[..]), r: Norm(r[..]))
}

/// The truncated quotient and remainder of x / y: q rounds toward zero, and
/// r = x - q * y has x's sign, as / and % do on the fixed integer types.
/// Panics when y is zero. Fails with OutOfMemory when a cannot give the
/// words.
///
/// For n-word x and m-word y below 60 words, it costs O(m * (n - m + 1))
/// word operations (Knuth's Algorithm D). From 60 words of y it costs
/// O(M(m) log m) per m words of quotient (Burnikel-Ziegler, M the product's
/// cost). It allocates about 2n + 2 words in a. From 60 words of y it also
/// takes O(n log n) scratch words from a sub-arena it frees before
/// returning.
export Int.DivMod(&self, a: &Arena, y: &Int): OutOfMemory!(q: Int, r: Int) from a {
    if y.mag.Len() == 0 {
        Panic("big.Int.DivMod: division by zero")
    }
    (q, r) := try DivMag(a, self.mag, y.mag)
    return (q: Make(q, self.negative != y.negative), r: Make(r, self.negative))
}

/// The Euclidean remainder of x / m: 0 <= r < |m| whatever the signs.
/// Panics when m is zero. Fails with OutOfMemory when a cannot give the
/// words. It costs and allocates as DivMod: O(m * (n - m + 1)) word
/// operations below 60 words of m, recursive division from there, about
/// 2n + 2 words in a, and from 60 words of m O(n log n) scratch words in a
/// sub-arena it frees before returning.
export Int.Mod(&self, a: &Arena, m: &Int): OutOfMemory!Int from a {
    if m.mag.Len() == 0 {
        Panic("big.Int.Mod: division by zero")
    }
    r := try ModMag(a, self.mag, m.mag)
    if self.negative && r.Len() > 0 {
        return Make(try SubMag(a, m.mag, r), false)
    }
    return Make(r, false)
}

// RemBlockWords is the least block, in words, by which RemMag reduces a
// long x: each block costs one division, so a larger block spends fewer
// copies per word of x, and the scratch stays O(n + RemBlockWords).
const RemBlockWords: int = 256

// RemMag is x mod y for magnitudes, y not zero, in a, normalized: what
// ModPow, ModInverse and Gcd reduce first. Up to 2n words of x, for n =
// y.Len(), it is ModMag, about 2 x.Len() + 2 words in a. A longer x is
// reduced one block of b = max(n, RemBlockWords) words at a time from the
// top, the remainder so far above the next block, so only the n-word
// result stays in a and the blocks' buffers, at most 3 (n + b) + 2 words,
// and each recursive division's own, are in a sub-arena freed before it
// returns (also on error), however long x is. O(x.Len() * n) word
// operations below DivRecWords words of y, O(x.Len() / n * M(n) log n)
// from it, as one division of x would cost.
RemMag(a: &Arena, x: []u64, y: []u64): OutOfMemory![]u64 from a {
    n := y.Len()
    if n == 1 || x.Len() <= 2 * n {
        return try ModMag(a, x, y)
    }
    r := try a.Slice[u64](n)
    b := if n > RemBlockWords { n } else { RemBlockWords }
    var s := try a.Sub()
    defer s.Free()
    u := try s.Slice[u64](n + b)
    q := try s.Slice[u64](b + 1)
    sc := try s.Slice[u64](if n >= DivRecWords { 0 } else { n + b + 1 })
    var pos := x.Len()
    var take := pos % b
    if take == 0 {
        take = b
    }
    var lr := 0
    for pos > 0 {
        // u = r B^take + x[pos - take..pos]; r < y, so u < y B^take and
        // the quotient has at most take + 1 words.
        lo := pos - take
        Copy(mut u[..take], x[lo..pos])
        Copy(mut u[take..take + lr], r[..lr])
        un := Norm(u[..take + lr])
        if CmpVV(un, y) < 0 {
            Clear(mut r[..])
            Copy(mut r[..], un)
        } else {
            nq := un.Len() - n + 1
            if n >= DivRecWords {
                try DivLarge(&s, mut q[..nq], mut r[..], un, y)
            } else {
                DivNat(mut q[..nq], mut r[..], un, y, mut sc[..])
            }
        }
        lr = Norm(r[..]).Len()
        pos = lo
        take = b
    }
    return Norm(r[..])
}

// ModMag is x mod y for magnitudes, y not zero, in a, normalized.
ModMag(a: &Arena, x: []u64, y: []u64): OutOfMemory![]u64 from a {
    if y.Len() == 1 && x.Len() > 0 {
        r := try a.Slice[u64](1)
        r[0] = ModVW(x, y[0])
        return Norm(r[..])
    }
    return (try DivMag(a, x, y)).r
}
