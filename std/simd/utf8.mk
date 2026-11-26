// Why: every str is checked as UTF-8 where it enters a program, so the check
// must run at memory speed.
//
// What: simd.ValidUtf8, its avx2 and avx512bw forms, and ValidUtf8Scalar,
// the reference.
//
// How: John Keiser and Daniel Lemire, "Validating UTF-8 In Less Than One
// Instruction Per Byte", Software: Practice and Experience 51(5), 2021, the
// lookup algorithm of simdjson and simdutf. Every error of Unicode section
// 3.9's table 3-7 (too short, too long, overlong, surrogate, above U+10FFFF,
// a stray continuation) shows as a bit in a 16-entry table looked up by the
// previous byte's high nibble, its low nibble and the current byte's high
// nibble; the AND of the three is nonzero exactly when the two-byte window
// is wrong. Third and fourth bytes are checked with two saturating
// subtracts. A block of four all-ASCII vectors costs one test. O(n), no
// allocation; below 16 bytes one scalar pass.

// The error bits of the three tables (simdjson's names).
const TooShort: u8 = 1
const TooLong: u8 = 2
const Overlong3: u8 = 4
const TooLarge: u8 = 8
const Surrogate: u8 = 16
const Overlong2: u8 = 32
const TooLarge1000: u8 = 64
const Overlong4: u8 = 64
const TwoConts: u8 = 128
const Carry: u8 = 131

/// Reports whether s is well-formed UTF-8, as Unicode's table 3-7 defines
/// it: no overlong form, no surrogate D800 to DFFF, nothing above 10FFFF,
/// no truncated sequence and no stray continuation byte. O(n); it
/// allocates nothing. It uses avx2 or avx512bw when the CPU has them; the
/// answer never depends on the CPU.
export ValidUtf8(s: []u8): bool {
    if Target.os == Os.Freestanding {
        return ValidUtf8Words(s)
    }
    // Below DispatchMin, an all-ASCII s (a CSV record, a field, a short
    // line: the common case) is valid with no table lookup: the OR of its
    // bytes has no high bit. From 16 bytes the OR is over the 16-byte loads
    // at 0, 16, 32 and 48 that fit and one of the last 16 bytes, which
    // overlaps them; below 16, over the bytes one at a time. A byte at 0x80
    // or above sends s on to the full check, so the answer is always the
    // full check's.
    n := s.Len()
    if n < DispatchMin {
        if n >= 16 {
            var any := Vec[u8, 16].FromSlice(s, n - 16)
            var i := 0
            for i + 16 <= n {
                any = any | Vec[u8, 16].FromSlice(s, i)
                i += 16
            }
            if !(any >= Vec[u8, 16].Splat(0x80)).Any() {
                return true
            }
        } else {
            var hi: u8 = 0
            for c in s {
                hi = hi | c
            }
            if hi < 0x80 {
                return true
            }
        }
    }
    if s.Len() >= WideMin && Cpu().Has("avx512bw") {
        unsafe "Cpu() reports avx512bw, which ValidUtf8Avx512's clause names" {
            return ValidUtf8Avx512(s)
        }
    }
    if s.Len() >= DispatchMin && Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which ValidUtf8Avx2's clause names" {
            return ValidUtf8Avx2(s)
        }
    }
    return ValidUtf8N[16](s)
}

ValidUtf8Avx2(s: []u8): bool cpu "avx2" {
    return ValidUtf8N[32](s)
}

ValidUtf8Avx512(s: []u8): bool cpu "avx512bw" {
    return ValidUtf8N[64](s)
}

ValidUtf8N[const N: int](s: []u8): bool
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires N == 16 || N == 32 || N == 64 {
    if s.Len() < N {
        return ValidUtf8Scalar(s)
    }
    return Utf8LookupN[N](s, false)
}

// Utf8LookupN is the lookup validation of s, s.Len() >= N. With wtf, a lead
// nibble E no longer raises Surrogate, so ED A0..BF passes (the three
// tables' AND needs all three bits): WTF-8's surrogates, whose pairs
// Wtf8Pair finds in the same pass, four bytes back.
Utf8LookupN[const N: int](s: []u8, const wtf: bool): bool
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires N == 16 || N == 32 || N == 64 {
    n := s.Len()
    e: u8 = if wtf { TooShort | Overlong3 } else { TooShort | Overlong3 | Surrogate }
    t1 := Vec[u8, 16].FromArray([TooLong, TooLong, TooLong, TooLong, TooLong, TooLong, TooLong, TooLong,
        TwoConts, TwoConts, TwoConts, TwoConts,
        TooShort | Overlong2, TooShort, e,
        TooShort | TooLarge | TooLarge1000 | Overlong4])
    t2 := Vec[u8, 16].FromArray([Carry | Overlong3 | Overlong2 | Overlong4, Carry | Overlong2, Carry, Carry,
        Carry | TooLarge, Carry | TooLarge | TooLarge1000, Carry | TooLarge | TooLarge1000,
        Carry | TooLarge | TooLarge1000, Carry | TooLarge | TooLarge1000, Carry | TooLarge | TooLarge1000,
        Carry | TooLarge | TooLarge1000, Carry | TooLarge | TooLarge1000, Carry | TooLarge | TooLarge1000,
        Carry | TooLarge | TooLarge1000 | Surrogate, Carry | TooLarge | TooLarge1000,
        Carry | TooLarge | TooLarge1000])
    t3 := Vec[u8, 16].FromArray([TooShort, TooShort, TooShort, TooShort, TooShort, TooShort, TooShort, TooShort,
        TooLong | Overlong2 | TwoConts | Overlong3 | TooLarge1000 | Overlong4,
        TooLong | Overlong2 | TwoConts | Overlong3 | TooLarge,
        TooLong | Overlong2 | TwoConts | Surrogate | TooLarge,
        TooLong | Overlong2 | TwoConts | Surrogate | TooLarge,
        TooShort, TooShort, TooShort, TooShort])
    high := Vec[u8, N].Splat(0x80)
    zero := Vec[u8, N].Zero()
    // The last three lanes allow only leads with room for their sequence.
    // Build the limit once, in reverse order to keep the lane indices literal.
    end_max := Vec[u8, N].Splat(255).With(0, 0xC0 - 1).With(1, 0xE0 - 1).With(2, 0xF0 - 1).Reverse()
    var err := zero
    var prev := zero
    var pending := zero // unfinished sequence flags from the last vector checked
    var i := 0
    for i + 4 * N <= n {
        w := s[i..i + 4 * N]
        v0 := Vec[u8, N].FromSlice(w, 0)
        v1 := Vec[u8, N].FromSlice(w, N)
        v2 := Vec[u8, N].FromSlice(w, 2 * N)
        v3 := Vec[u8, N].FromSlice(w, 3 * N)
        if ((v0 | v1 | v2 | v3) >= high).Any() {
            err = err | Utf8Check[N](v0, prev, t1, t2, t3) | Utf8Check[N](v1, v0, t1, t2, t3) |
                Utf8Check[N](v2, v1, t1, t2, t3) | Utf8Check[N](v3, v2, t1, t2, t3)
            if wtf {
                err = err | Wtf8Pair[N](v0, prev) | Wtf8Pair[N](v1, v0) | Wtf8Pair[N](v2, v1) | Wtf8Pair[N](v3, v2)
            }
            pending = v3.SubSat(end_max)
        } else {
            err = err | pending
            pending = zero
        }
        prev = v3
        i += 4 * N
    }
    for i + N <= n {
        v := Vec[u8, N].FromSlice(s, i)
        err = err | Utf8Check[N](v, prev, t1, t2, t3)
        if wtf {
            err = err | Wtf8Pair[N](v, prev)
        }
        pending = v.SubSat(end_max)
        prev = v
        i += N
    }
    if i < n {
        // The last N bytes, overlapping what was checked: each lane's
        // check reads only its own byte and the three before it, so
        // checking a byte twice gives the same answer, and the three
        // bytes before the window are put in prev's top lanes (built
        // in the low lanes and reversed: mono does not yet take a const
        // lane index N - 1). Wtf8Pair reads four back, but the window's
        // lane 0 was checked already (p < i), so three suffice for it
        // too, and its lane 0 meets a zero lane, which is no ED.
        p := n - N
        v := Vec[u8, N].FromSlice(s, p)
        var w := zero
        if p >= 1 {
            w = w.With(0, s[p - 1])
        }
        if p >= 2 {
            w = w.With(1, s[p - 2])
        }
        if p >= 3 {
            w = w.With(2, s[p - 3])
        }
        err = err | Utf8Check[N](v, w.Reverse(), t1, t2, t3)
        if wtf {
            err = err | Wtf8Pair[N](v, w.Reverse())
        }
        pending = v.SubSat(end_max)
    }
    return (err | pending).ReduceOr() == 0
}

// Utf8Check is the error vector of v given the vector before it: nonzero in
// a lane whose byte, with the bytes before it, breaks table 3-7.
Utf8Check[const N: int](v: Vec[u8, N], prev: Vec[u8, N], t1: Vec[u8, 16], t2: Vec[u8, 16], t3: Vec[u8, 16]): Vec[u8, N]
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires N == 16 || N == 32 || N == 64 {
    p1 := v.Slide(prev, 1)
    special := Lookup16[N](p1 >> 4, t1) & Lookup16[N](p1 & Vec[u8, N].Splat(15), t2) & Lookup16[N](v >> 4, t3)
    third := v.Slide(prev, 2).SubSat(Vec[u8, N].Splat(0xE0 - 0x80))
    fourth := v.Slide(prev, 3).SubSat(Vec[u8, N].Splat(0xF0 - 0x80))
    must := (third | fourth) & Vec[u8, N].Splat(0x80)
    return must ^ special
}

// Wtf8Pair is nonzero in a lane that is the second byte of a trail
// surrogate's sequence (ED B0..BF) right after a lead surrogate's (ED
// A0..AF, then a continuation): WTF-8 section 4.2's one error beyond table
// 3-7 with surrogates allowed, found in the vector pass by a lookbehind
// of four bytes (no IndexByte scan of the ED leads after it). Where the
// window is well formed, which the lookup's error vector decides in the
// same pass, an ED byte is a lead and its sequence three bytes, so ED four
// lanes back is the lead of the sequence just before, and a continuation
// lane at B0 or above is at most BF.
Wtf8Pair[const N: int](v: Vec[u8, N], prev: Vec[u8, N]): Vec[u8, N]
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires N == 16 || N == 32 || N == 64 {
    ed := Vec[u8, N].Splat(0xED)
    p3 := v.Slide(prev, 3)
    pair := (v >= Vec[u8, N].Splat(0xB0)) & (v.Slide(prev, 1) == ed) & (v.Slide(prev, 4) == ed) &
        (p3 >= Vec[u8, N].Splat(0xA0)) & (p3 <= Vec[u8, N].Splat(0xAF))
    return pair.Select(Vec[u8, N].Splat(1), Vec[u8, N].Zero())
}

// ValidUtf8Scalar is table 3-7 one sequence at a time: the reference the
// vector form is tested against, and the form below N bytes.
ValidUtf8Scalar(s: []u8): bool {
    n := s.Len()
    var i := 0
    for i < n {
        k := Utf8SeqLen(s, i)
        if k == 0 {
            return false
        }
        i += k
    }
    return true
}

// Utf8SeqLen is the length, 1 to 4, of the well-formed sequence at s[i], or
// 0 when none starts there (table 3-7, with Go's utf8.Valid's per-lead
// ranges for the second byte); i < s.Len().
Utf8SeqLen(s: []u8, i: int): int {
    n := s.Len()
    c := s[i]
    if c < 0x80 {
        return 1
    }
    var need := 0
    var lo: u8 = 0x80
    var hi: u8 = 0xBF
    if c >= 0xC2 && c <= 0xDF {
        need = 1
    } else if c == 0xE0 {
        need = 2
        lo = 0xA0
    } else if c >= 0xE1 && c <= 0xEC || c == 0xEE || c == 0xEF {
        need = 2
    } else if c == 0xED {
        need = 2
        hi = 0x9F
    } else if c == 0xF0 {
        need = 3
        lo = 0x90
    } else if c >= 0xF1 && c <= 0xF3 {
        need = 3
    } else if c == 0xF4 {
        need = 3
        hi = 0x8F
    } else {
        return 0
    }
    if i + need >= n {
        return 0
    }
    d := s[i + 1]
    if d < lo || d > hi {
        return 0
    }
    for k in 2..need + 1 {
        e := s[i + k]
        if e < 0x80 || e > 0xBF {
            return 0
        }
    }
    return need + 1
}
