// Why: string code (UTF-16 interop, ASCII fast paths, reverse search, WTF-8
// paths on Windows) needs the same vector speed as the byte routines.
//
// What: IsAscii, Utf16Len and Utf16LenBytes, LastIndexByte and ValidWtf8,
// with their vector bodies and scalar forms.
//
// How: each has one body generic over the lane count N, dispatched as
// bytes.mk's routines are (avx512bw from WideMin bytes, avx2 from
// DispatchMin, else the 16-lane baseline; the word or scalar form on
// x86_64-freestanding, which may hold no vector, E1108), and a scalar form,
// the reference each vector form is tested against bit for bit
// (std/std-simd_vecpaths_test.go). No routine allocates; each is O(n).

import std.raw

/// Reports whether every byte of s is below 0x80; true for an empty s.
/// O(n); it allocates nothing. Each step tests four vectors at once, and
/// the tail is one overlapping load.
export IsAscii(s: []u8): bool {
    if Target.os == Os.Freestanding {
        return IsAsciiScalar(s)
    }
    if s.Len() >= WideMin && Cpu().Has("avx512bw") {
        unsafe "Cpu() reports avx512bw, which IsAsciiAvx512's clause names" {
            return IsAsciiAvx512(s)
        }
    }
    if s.Len() >= DispatchMin && Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which IsAsciiAvx2's clause names" {
            return IsAsciiAvx2(s)
        }
    }
    return IsAsciiN[16](s)
}

IsAsciiAvx2(s: []u8): bool cpu "avx2" {
    return IsAsciiN[32](s)
}

IsAsciiAvx512(s: []u8): bool cpu "avx512bw" {
    return IsAsciiN[64](s)
}

IsAsciiN[const N: int](s: []u8): bool
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {
    n := s.Len()
    if n < N {
        return IsAsciiScalar(s)
    }
    high := Vec[u8, N].Splat(0x80)
    var i := 0
    for i + 4 * N <= n {
        w := s[i..i + 4 * N]
        v := Vec[u8, N].FromSlice(w, 0) | Vec[u8, N].FromSlice(w, N) | Vec[u8, N].FromSlice(w, 2 * N) |
            Vec[u8, N].FromSlice(w, 3 * N)
        if (v >= high).Any() {
            return false
        }
        i += 4 * N
    }
    for i + N <= n {
        if (Vec[u8, N].FromSlice(s, i) >= high).Any() {
            return false
        }
        i += N
    }
    if i < n {
        // The last N bytes; those already seen were ASCII.
        return !(Vec[u8, N].FromSlice(s, n - N) >= high).Any()
    }
    return true
}

// IsAsciiScalar is IsAscii one byte at a time.
IsAsciiScalar(s: []u8): bool {
    for x in s {
        if x >= 0x80 {
            return false
        }
    }
    return true
}

/// Returns the number of UTF-16 code units s takes: one per scalar, two
/// for a scalar above U+FFFF. O(n); it allocates nothing. It is
/// Utf16LenBytes over s's bytes.
export Utf16Len(s: str): int {
    return Utf16LenBytes(StrBytes(s))
}

/// Returns the number of UTF-16 code units the UTF-8 or WTF-8 bytes b
/// take: one per scalar, two for a scalar above U+FFFF, and one for a
/// lone surrogate's three bytes. O(n); it allocates nothing.
///
/// It counts the bytes that are not continuation bytes (10xxxxxx) and adds
/// one for each byte from 0xF0 on, a four-byte lead. That count is the
/// answer on any bytes, so it never depends on the CPU, though on bytes
/// that are not UTF-8 or WTF-8 it is not a decoder's count.
export Utf16LenBytes(b: []u8): int {
    if Target.os == Os.Freestanding {
        return Utf16LenScalar(b)
    }
    if b.Len() >= WideMin && Cpu().Has("avx512bw") {
        unsafe "Cpu() reports avx512bw, which Utf16LenAvx512's clause names" {
            return Utf16LenAvx512(b)
        }
    }
    if b.Len() >= DispatchMin && Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which Utf16LenAvx2's clause names" {
            return Utf16LenAvx2(b)
        }
    }
    return Utf16LenN[16](b)
}

// StrBytes is s's bytes: std.simd sits below the prelude, which holds
// str.AsBytes, so they come through std.raw.
StrBytes(s: str): []u8 from s {
    unsafe "a str's data pointer holds s.Len() bytes for as long as s lives, and the result holds s's loan" {
        return raw.Slice(raw.StrData(s), s.Len())
    }
}

Utf16LenAvx2(b: []u8): int cpu "avx2" {
    return Utf16LenN[32](b)
}

// The fold bound holds at any width: each byte lane of a counter takes at
// most one from each of the step's two vectors, so 127 steps add at most
// 254 to it, below 256, whatever N is.
Utf16LenAvx512(b: []u8): int cpu "avx512bw" {
    return Utf16LenN[64](b)
}

Utf16LenN[const N: int](b: []u8): int
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {
    n := b.Len()
    if n < N {
        return Utf16LenScalar(b)
    }
    c0 := Vec[u8, N].Splat(0xC0)
    x80 := Vec[u8, N].Splat(0x80)
    f0 := Vec[u8, N].Splat(0xF0)
    ones := Vec[u8, N].Splat(255)
    zero := Vec[u8, N].Zero()
    var units := n
    var i := 0
    for i + 2 * N <= n {
        // At most 127 steps: each counter takes two vectors a step, so it
        // reaches 254 at most and never wraps.
        steps := if (n - i) / (2 * N) > 127 { 127 } else { (n - i) / (2 * N) }
        run := b[i..i + steps * (2 * N)]
        var cont := zero
        var four := zero
        for j in 0..steps {
            w := run[j * (2 * N)..j * (2 * N) + 2 * N]
            v0 := Vec[u8, N].FromSlice(w, 0)
            v1 := Vec[u8, N].FromSlice(w, N)
            cont = cont -% ((v0 & c0) == x80).Select(ones, zero) -% ((v1 & c0) == x80).Select(ones, zero)
            four = four -% (v0 >= f0).Select(ones, zero) -% (v1 >= f0).Select(ones, zero)
        }
        i += steps * (2 * N)
        units = units - Cast[int](cont.ReduceSumWide()) + Cast[int](four.ReduceSumWide())
    }
    for i + N <= n {
        v := Vec[u8, N].FromSlice(b, i)
        units = units - ((v & c0) == x80).CountSet() + (v >= f0).CountSet()
        i += N
    }
    if i < n {
        v := Vec[u8, N].FromSlice(b, n - N)
        sh := N - (n - i)
        units = units - raw.Popcount64(((v & c0) == x80).Bits() >> sh) + raw.Popcount64((v >= f0).Bits() >> sh)
    }
    return units
}

// Utf16LenScalar is Utf16LenBytes one byte at a time, the reference: one
// unit for each byte that is not a continuation byte, and one more for
// each byte from 0xF0 on.
Utf16LenScalar(b: []u8): int {
    var units := 0
    for x in b {
        if x & 0xC0 != 0x80 {
            units += 1
        }
        if x >= 0xF0 {
            units += 1
        }
    }
    return units
}

/// Returns the offset of the last byte of s equal to b, or none. O(n); it
/// allocates nothing. It works as IndexByte does, from the end of s.
export LastIndexByte(s: []u8, b: u8): ?int {
    if Target.os == Os.Freestanding {
        return LastIndexByteScalar(s, b)
    }
    if s.Len() >= WideMin && Cpu().Has("avx512bw") {
        unsafe "Cpu() reports avx512bw, which LastIndexByteAvx512's clause names" {
            return LastIndexByteAvx512(s, b)
        }
    }
    if s.Len() >= DispatchMin && Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which LastIndexByteAvx2's clause names" {
            return LastIndexByteAvx2(s, b)
        }
    }
    return LastIndexByteN[16](s, b)
}

LastIndexByteAvx2(s: []u8, b: u8): ?int cpu "avx2" {
    return LastIndexByteN[32](s, b)
}

LastIndexByteAvx512(s: []u8, b: u8): ?int cpu "avx512bw" {
    return LastIndexByteN[64](s, b)
}

LastIndexByteN[const N: int](s: []u8, b: u8): ?int
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 {
    n := s.Len()
    if n < N {
        return LastIndexByteScalar(s, b)
    }
    nd := Vec[u8, N].Splat(b)
    zero := Vec[u8, N].Zero()
    // The bytes from i on were seen and hold no b.
    var i := n
    for i >= 4 * N {
        w := s[i - 4 * N..i]
        x0 := Vec[u8, N].FromSlice(w, 0) ^ nd
        x1 := Vec[u8, N].FromSlice(w, N) ^ nd
        x2 := Vec[u8, N].FromSlice(w, 2 * N) ^ nd
        x3 := Vec[u8, N].FromSlice(w, 3 * N) ^ nd
        if (x0.Min(x1).Min(x2.Min(x3)) == zero).Any() {
            break
        }
        i -= 4 * N
    }
    for i >= N {
        m := (Vec[u8, N].FromSlice(s, i - N) == nd).Bits()
        if m != 0 {
            return some(i - N + 63 - raw.Clz64(m))
        }
        i -= N
    }
    if i > 0 {
        // The first N bytes, of which the last N - i were seen.
        m := (Vec[u8, N].FromSlice(s, 0) == nd).Bits() & LowBits(i)
        if m != 0 {
            return some(63 - raw.Clz64(m))
        }
    }
    return none
}

// LastIndexByteScalar is LastIndexByte one byte at a time from the end.
LastIndexByteScalar(s: []u8, b: u8): ?int {
    var i := s.Len()
    for i > 0 {
        i -= 1
        if s[i] == b {
            return some(i)
        }
    }
    return none
}

/// Reports whether s is well-formed WTF-8, as the WTF-8 specification
/// defines it: UTF-8 by Unicode's table 3-7, except that a surrogate's
/// three bytes (ED A0..BF 80..BF) are allowed. A lead surrogate (ED A0..AF)
/// followed at once by a trail surrogate (ED B0..BF) is refused, since
/// WTF-8 writes that pair as its scalar's four bytes. So every str is
/// WTF-8, and a JavaScript string's lone surrogates fit. O(n); it
/// allocates nothing.
///
/// Windows file names are converted more leniently: that conversion also
/// takes a lead and trail surrogate written as two three-byte sequences.
export ValidWtf8(s: []u8): bool {
    if Target.os == Os.Freestanding {
        return ValidWtf8Scalar(s)
    }
    if s.Len() >= WideMin && Cpu().Has("avx512bw") {
        unsafe "Cpu() reports avx512bw, which ValidWtf8Avx512's clause names" {
            return ValidWtf8Avx512(s)
        }
    }
    if s.Len() >= DispatchMin && Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which ValidWtf8Avx2's clause names" {
            return ValidWtf8Avx2(s)
        }
    }
    return ValidWtf8N[16](s)
}

ValidWtf8Avx2(s: []u8): bool cpu "avx2" {
    return ValidWtf8N[32](s)
}

ValidWtf8Avx512(s: []u8): bool cpu "avx512bw" {
    return ValidWtf8N[64](s)
}

ValidWtf8N[const N: int](s: []u8): bool
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires N == 16 || N == 32 || N == 64 {
    if s.Len() < N {
        return ValidWtf8Scalar(s)
    }
    return Utf8LookupN[N](s, true)
}

// ValidWtf8Scalar is the WTF-8 grammar one sequence at a time: table 3-7
// with ED's second byte 80..BF, and no lead surrogate's sequence just
// before a trail surrogate's.
ValidWtf8Scalar(s: []u8): bool {
    n := s.Len()
    var i := 0
    var lead := false
    for i < n {
        var k := 0
        if s[i] == 0xED {
            if i + 2 < n && s[i + 1] >= 0x80 && s[i + 1] <= 0xBF && s[i + 2] >= 0x80 && s[i + 2] <= 0xBF {
                k = 3
            }
        } else {
            k = Utf8SeqLen(s, i)
        }
        if k == 0 {
            return false
        }
        is_lead := s[i] == 0xED && s[i + 1] >= 0xA0 && s[i + 1] <= 0xAF
        is_trail := s[i] == 0xED && s[i + 1] >= 0xB0
        if lead && is_trail {
            return false
        }
        lead = is_lead
        i += k
    }
    return true
}
