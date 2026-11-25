// Why: a float printer for another language (JavaScript's toString,
// toFixed, toPrecision, toExponential) needs a float's decimal digits
// without std's own layout.
//
// What: Digits (the digits, their count, the decimal exponent and the
// sign), DigitMode, ShortestDigits, ShortestDigitsInto (the shortest digits
// into a caller's buffer of 17 bytes) and ExactDigits.
//
// How: both run std.core's float formatting (core_fmtfloat.mk's
// FloatDigits): Go's unrounded scaling (R. Cox, "Floating-Point Printing
// and Parsing Can Be Simple And Fast") for the shortest digits and for at
// most 18 digits, else the exact multiprecision Decimal. Nothing allocates.

import std.core
import std.raw

/// A float's decimal digits. The magnitude is d.ddd... * 10^exp, with the
/// digits in `digits[0..len]` as ASCII, the first and the last not '0';
/// trailing zeros of the value are in exp. `neg` is the sign bit, so -0.0 is
/// neg. The 768 bytes hold an f64's longest exact expansion, 767 digits, so no
/// result is cut. A zero, a value rounded away to zero, NaN and the infinities
/// have len 0 and exp 0; tell them apart by the value itself.
export struct Digits {
    /// The digits as ASCII, in digits[0..len].
    export digits: [768]u8
    /// The number of digits; 0 for a zero, NaN and the infinities.
    export len: int
    /// The decimal exponent: the magnitude is d.ddd... * 10^exp.
    export exp: int
    /// The sign bit, so -0.0 is neg.
    export neg: bool
}

/// How ExactDigits counts its n digits and rounds the exact binary value.
/// `Significant` keeps n significant digits and `Fraction` n digits after the
/// point, both with ties to even. `SignificantHalfUp` and `FractionHalfUp`
/// break ties away from zero, as ECMA-262's toPrecision, toExponential and
/// toFixed do.
export enum DigitMode {
    /// n significant digits, ties to even.
    Significant
    /// n digits after the point, ties to even.
    Fraction
    /// n significant digits, ties away from zero.
    SignificantHalfUp
    /// n digits after the point, ties away from zero.
    FractionHalfUp
}

/// The shortest digits that read back to x's exact bits (round to nearest
/// even); when two digit strings of that length do, the correctly rounded one.
/// These are the digits `{x}` prints. ShortestDigitsInto with a Digits around
/// it. O(1); allocates nothing.
export ShortestDigits(x: f64): Digits {
    var d := Digits(digits: [0; 768], len: 0, exp: 0, neg: false)
    r := ShortestDigitsInto(mut d.digits[..], x)
    d.len = r.len
    d.exp = r.exp
    d.neg = (raw.F64Bits(x) >> 63) != 0
    return d
}

/// ShortestDigits's digits written into out[0..len] as ASCII, the first and
/// the last not '0', with the decimal exponent exp of d.ddd... * 10^exp, as
/// Digits holds them. An f64 has at most 17 shortest digits, so out must hold
/// at least 17 bytes: a shorter out panics with code 8, whatever x is. Only
/// out[0..17] is written, and nothing is zeroed: past len it may hold
/// trimmed '0's. A zero, NaN and the infinities have len 0 and exp 0; the
/// sign is x's. O(1); allocates nothing.
export ShortestDigitsInto(out: mut []u8, x: f64): (len: int, exp: int) {
    if out.Len() < 17 {
        raw.Fail(8, "math.ShortestDigitsInto: out holds fewer than 17 bytes")
    }
    r := core.FloatDigits(out, x, 0, 0)
    return (len: r.nd, exp: DigitsExp(r.nd, r.dp))
}

/// x's exact value rounded by mode to n digits: n significant digits (n at
/// least 1) or n digits after the point (n at least 0). Any other n panics with
/// code 8. An n past the exact expansion rounds nothing. O(1) when the result
/// has at most 18 digits, else O(767); allocates nothing.
export ExactDigits(x: f64, n: int, mode: DigitMode): Digits {
    m := match mode {
        .Significant { 1 }
        .Fraction { 2 }
        .SignificantHalfUp { 3 }
        .FractionHalfUp { 4 }
    }
    if (m == 1 || m == 3) && n < 1 {
        raw.Fail(8, "math.ExactDigits: fewer than one significant digit")
    }
    if n < 0 {
        raw.Fail(8, "math.ExactDigits: a negative digit count")
    }
    var d := Digits(digits: [0; 768], len: 0, exp: 0, neg: false)
    // Past 1,100 digits after the point, or 768 significant ones, nothing
    // is left to round: the exact expansion ends before (2^-1074 has 1,074
    // digits after the point, 751 of them significant).
    r := core.FloatDigits(mut d.digits[..], x, m, IntMinD(n, 1100))
    d.len = r.nd
    d.exp = DigitsExp(r.nd, r.dp)
    d.neg = (raw.F64Bits(x) >> 63) != 0
    return d
}

// DigitsExp is the scientific exponent of 0.d * 10^dp: dp - 1, and 0 when
// there are no digits.
DigitsExp(nd: int, dp: int): int {
    if nd == 0 {
        return 0
    }
    return dp - 1
}

IntMinD(a: int, b: int): int {
    if a < b {
        return a
    }
    return b
}
