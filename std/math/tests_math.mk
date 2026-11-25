// Why: std.math's results are part of its contract: the same bits
// everywhere, exact where exact, correctly rounded where promised.
// What: test blocks for Min, Max, Abs, Sqrt, the float remainder, the
// constants, NaN bits, rounding, Fma, FrExp, Gcd, the rotations, the
// double-double functions, large-argument trig and ShortestDigitsInto.
// How: run by `mk test --std=std.math` with contracts on and the counting
// heap; each compares exact bits.

test "Min and Max on every Ord kind" {
    Assert(Min(3, -4) == -4)
    Assert(Max(3, -4) == 3)
    Assert(Min(7, 7) == 7)
    Assert(Min(1.5, 2.5) == 1.5)
    Assert(Max(-0.5, -1.5) == -0.5)
    Assert(Min("abc", "abd") == "abc")
    Assert(Max("b", "abc") == "b")
    Assert(Min('z', 'a') == 'a')
    a: u8 = 200
    b: u8 = 100
    Assert(Max(a, b) == 200)
    c: i64 = -9223372036854775807 - 1
    Assert(Min(c, 0) == c)
}

test "Abs clears only the sign" {
    Assert(Abs(-2.5) == 2.5)
    Assert(Abs(2.5) == 2.5)
    Assert(Abs(-0.0).Bits() == 0)
    Assert(Abs(f64.FromBits(0xFFF0000000000000)).Bits() == 0x7FF0000000000000)
    Assert(Abs(f64.FromBits(0x8000000000000001)).Bits() == 1)
    n := Abs(f64.FromBits(0xFFF8000000000001))
    Assert(n != n)
}

test "Sqrt is correctly rounded" {
    Assert(Sqrt(4.0) == 2.0)
    Assert(Sqrt(2.0) == 1.4142135623730951)
    Assert(Sqrt(1e-320) == 9.99994433575849e-161)
    Assert(Sqrt(-0.0).Bits() == 0x8000000000000000)
    Assert(Sqrt(f64.FromBits(0x7FF0000000000000)).Bits() == 0x7FF0000000000000)
    m := Sqrt(-1.0)
    Assert(m != m)
    // Every square of a 26-bit integer has an exact root.
    for i in 0..10_000 {
        k := Cast[f64](i * 6007 + 1)
        Assert(Sqrt(k * k) == k)
    }
}

// MathRem and MathRem32 take the operands as parameters, so the remainder
// runs in Rem64 and Rem32 and is not folded at compile time.
MathRem(a: f64, b: f64): f64 {
    return a % b
}

MathRem32(a: f32, b: f32): f32 {
    return a % b
}

test "float remainder is exact" {
    Assert(MathRem(7.5, 2.0) == 1.5)
    Assert(MathRem(-7.5, 2.0) == -1.5)
    Assert(MathRem(7.5, -2.0) == 1.5)
    Assert(MathRem(-4.0, 2.0).Bits() == 0x8000000000000000)
    Assert(MathRem(1e300, 3.0) == 0.0)
    Assert(MathRem(5e-324, 1.0) == 5e-324)
    Assert(MathRem(3.0, 1e300) == 3.0)
    z := MathRem(1.0, 0.0)
    Assert(z != z)
    i := MathRem(f64.FromBits(0x7FF0000000000000), 2.0)
    Assert(i != i)
    Assert(MathRem32(7.5, 2.0) == 1.5)
    Assert(MathRem32(-1e30, 7.0) == -1.0)
}

test "the constants are the nearest f64s" {
    Assert(Pi.Bits() == 0x400921FB54442D18)
    Assert(E.Bits() == 0x4005BF0A8B145769)
    Assert(Ln2.Bits() == 0x3FE62E42FEFA39EF)
    Assert(Sqrt2.Bits() == 0x3FF6A09E667F3BCD)
    Assert(IsInf(Inf) && Inf > 0.0)
    Assert(Nan.Bits() == 0x7FF8000000000000)
}

test "a NaN result is Nan's bits whatever the input's" {
    Assert(Sqrt(-2.0).Bits() == Nan.Bits())
    Assert(Log(-1.0).Bits() == Nan.Bits())
    Assert(Acos(2.0).Bits() == Nan.Bits())
    Assert(Floor(f64.FromBits(0xFFF8000000000123)).Bits() == Nan.Bits())
    Assert(SqrtF32(-1.0).Bits() == 0x7FC00000)
}

test "the rounding functions keep the sign of a zero" {
    Assert(RoundToZero(-0.5).Bits() == 0x8000000000000000)
    Assert(Ceil(-0.5).Bits() == 0x8000000000000000)
    Assert(Floor(-0.5) == -1.0)
    Assert(Round(2.5) == 3.0 && RoundEven(2.5) == 2.0 && RoundEven(3.5) == 4.0)
    Assert(Fract(-3.25) == -0.25 && Fract(-3.0).Bits() == 0x8000000000000000)
}

test "Fma rounds once" {
    // (1 + 2^-52)^2 - (1 + 2^-51) is 2^-104, lost by a separate multiply.
    a := 1.0 + f64.FromBits(0x3CB0000000000000)
    b := 1.0 + f64.FromBits(0x3CC0000000000000)
    Assert(Fma(a, a, -b) == f64.FromBits(0x3970000000000000))
    Assert(a * a - b == 0.0)
}

test "FrExp and LdExp invert each other" {
    r := FrExp(48.0)
    Assert(r.frac == 0.75 && r.exp == 6)
    Assert(LdExp(r.frac, r.exp) == 48.0)
    Assert(LdExp(1.0, -1074).Bits() == 1)
    Assert(LdExp(1.0, -1075) == 0.0)
    Assert(IsInf(LdExp(1.0, 1024)))
}

test "Gcd and CheckedPow at their edges" {
    Assert(Gcd(0, 0) == 0 && Gcd(0, 12) == 12 && Gcd(48, 18) == 6)
    Assert((CheckedPowU64(2, 63) else { 0 }) == 0x8000000000000000)
    Assert((CheckedPowU64(2, 64) else { 7 }) == 7)
    Assert((CheckedPow(-2, 63) else { 0 }) == -9223372036854775807 - 1)
    Assert((CheckedPow(2, 63) else { 7 }) == 7)
    Assert((CheckedPow(0, 0) else { 0 }) == 1)
}

test "the rotations take the amount modulo the width" {
    Assert(RotateLeft8(0x81, 1) == 0x03 && RotateLeft8(0x81, 9) == 0x03 && RotateLeft8(0x81, -1) == 0xC0)
    Assert(RotateRight32(1, 1) == 0x80000000 && RotateRight64(1, -1) == 2)
    Assert(LeadingZeros16(0) == 16 && TrailingZeros32(0) == 32 && OnesCount64(0xFF00) == 8)
    Assert(ReverseBits8(0x01) == 0x80 && ReverseBytes32(0x11223344) == 0x44332211)
}

test "the double-double functions keep a zero's sign and their exact points" {
    Assert(Sinh(-0.0).Bits() == 0x8000000000000000)
    Assert(Tanh(-0.0).Bits() == 0x8000000000000000)
    Assert(Asinh(-0.0).Bits() == 0x8000000000000000)
    Assert(Atanh(-0.0).Bits() == 0x8000000000000000)
    Assert(Cosh(0.0) == 1.0)
    Assert(Acosh(1.0).Bits() == 0)
    Assert(Tanh(30.0) == 1.0)
    Assert(Tanh(-30.0) == -1.0)
    Assert(Sinh(711.0) == Inf)
    Assert(Cosh(-711.0) == Inf)
    Assert(Atanh(1.0) == Inf)
    Assert(Atanh(-1.0) == -Inf)
    Assert(Atan2(0.0, -1.0) == Pi)
    Assert(Atan2(-0.0, -1.0) == -Pi)
    Assert(Atan2(1.0, 0.0) == Pi / 2.0)
    Assert(Atan2(1.0, 1.0) == Pi / 4.0)
    Assert(Atan2(-1.0, -1.0) == -3.0 * Pi / 4.0)
}

test "PowF32 and HypotF32 settle a double rounding at an f32 midpoint" {
    // Pow and Hypot land exactly on the midpoint in f64; the true value
    // lies just below it (Pow, the first) or above it (the others).
    Assert(PowF32(f32.FromBits(0x3FB34573), 123.5).Bits() == 0x5D81E239)
    Assert(PowF32(f32.FromBits(0x3FC47BD8), 175.5).Bits() == 0x75B5BD8B)
    Assert(HypotF32(f32.FromBits(0x4B23C9BD), f32.FromBits(0x48BF8A81)).Bits() == 0x4B23E5BB)
    Assert(HypotF32(f32.FromBits(0x4B048D37), f32.FromBits(0x49ADE3F7)).Bits() == 0x4B06526F)
    // True midpoints stay ties, broken to even: 11^7 = 19487171 and
    // 8955999^2 + 14407200^2 = 16964001^2.
    Assert(PowF32(121.0, 3.5) == 19487172.0)
    Assert(PowF32(-11.0, 7.0) == -19487172.0)
    Assert(HypotF32(8955999.0, 14407200.0) == 16964000.0)
    // Atan2 returns y / x = 3 2^-150, the midpoint 1.5 2^-149, and atan
    // lies just below it, so the result is 2^-149, not the even 2^-148.
    Assert(Atan2F32(f32.FromBits(0x00180000), 1048576.0).Bits() == 0x00000001)
}

test "Sin, Cos and Tan at the f64 nearest a multiple of pi/2" {
    // 6381956970095103 2^797: cos x is about -4.687e-19.
    x := f64.FromBits(0x7506AC5B262CA1FF)
    Assert(Sin(x).Bits() == 0x3FF0000000000000)
    Assert(Cos(x).Bits() == 0xBC214AE72E6BA22F)
    Assert(Tan(x).Bits() == 0xC3BD9BA9A7975636)
    Assert(Sin(-x).Bits() == 0xBFF0000000000000)
    Assert(Tan(-x).Bits() == 0x43BD9BA9A7975636)
}

// TdNext is a seeded splitmix64 step, so the block's inputs are fixed.
TdNext(s: mut u64): u64 {
    *s = *s +% 0x9E37_79B9_7F4A_7C15
    var z: u64 = *s
    z = (z ^ (z >> 30)) *% 0xBF58_476D_1CE4_E5B9
    z = (z ^ (z >> 27)) *% 0x94D0_49BB_1331_11EB
    return z ^ (z >> 31)
}

// TdSame reports whether a and b hold the same bytes.
TdSame(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

// TdInto reports whether ShortestDigitsInto, into exactly 17 bytes, gives
// ShortestDigits's digits and exponent for x.
TdInto(x: f64): bool {
    var buf: [17]u8 = [0xAA; 17]
    r := ShortestDigitsInto(mut buf[..], x)
    d := ShortestDigits(x)
    return r.len == d.len && r.exp == d.exp && TdSame(buf[0..r.len], d.digits[0..d.len])
}

// TdIs reports whether ShortestDigitsInto gives exactly the digits want
// and the exponent exp for x.
TdIs(x: f64, want: str, exp: int): bool {
    var buf: [17]u8 = [0; 17]
    r := ShortestDigitsInto(mut buf[..], x)
    return r.exp == exp && TdSame(buf[0..r.len], want.AsBytes())
}

test "ShortestDigitsInto fits any f64 in 17 bytes and agrees with ShortestDigits" {
    Assert(TdIs(-1.7976931348623157e308, "17976931348623157", 308))
    Assert(TdIs(2.2250738585072014e-308, "22250738585072014", -308))
    Assert(TdIs(f64.FromBits(1), "5", -324))
    Assert(TdIs(0.1, "1", -1))
    Assert(TdIs(1e21, "1", 21))
    Assert(TdIs(123456789012345680.0, "12345678901234568", 17))
    Assert(TdIs(0.0, "", 0) && TdIs(-0.0, "", 0))
    Assert(TdIs(f64.FromBits(0x7FF8000000000000), "", 0))
    Assert(TdIs(f64.FromBits(0x7FF0000000000000), "", 0) && TdIs(f64.FromBits(0xFFF0000000000000), "", 0))
    var big: [40]u8 = [0; 40]
    r := ShortestDigitsInto(mut big[3..40], 2.5)
    Assert(r.len == 2 && r.exp == 0 && big[3] == 0x32 && big[4] == 0x35 && big[0] == 0)
    var s: u64 = 288
    for _ in 0..200000 {
        Assert(TdInto(f64.FromBits(TdNext(mut s))))
    }
    // Each exponent's power of two (the skewed interval) and largest mantissa.
    for e in 0..2047 {
        Assert(TdInto(f64.FromBits(Trunc[u64](e) << 52)) && TdInto(f64.FromBits((Trunc[u64](e) << 52) | 0xF_FFFF_FFFF_FFFF)))
    }
}
