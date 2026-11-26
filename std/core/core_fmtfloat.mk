// Why: printing a float must give the shortest digits that read back to the
// same value, or exactly N digits after the point, without allocating and
// fast; this file holds that formatting apart from the integer runner.
//
// What: FmtF64, FmtF32 and FmtFloatBits (what Print's runner reaches for a
// float hole), FmtFixed and FmtDigitsF for fixed output, and FloatDigits and
// FloatDigitsPut, the raw digit generator std.math builds on.
//
// How: fast unrounded scaling, ported from Go 1.27's internal/strconv
// (uscale.go, ftoa.go; R. Cox, "Floating-Point Printing and Parsing Can Be
// Simple And Fast", research.swtch.com/fp): one 128-bit multiply per scaled
// value against Pow10Tab, exact by construction (the result keeps a sticky
// bit, so every rounding is decided right). Shortest digits take two
// scalings and a third only when needed; fixed digits with at most 18
// significant digits take one; longer fixed output takes the multiprecision
// Decimal of core_parse.mk. The scaling (FpPrescale, FpUscale, FpShortest,
// FpFixedWidth) and Pow10Tab are in core_fmt.mk.

import std.raw

/// Writes x by the kind in spec's bits 0-3. Kind 0 writes the shortest
/// digits that read back as x: fixed notation for a decimal exponent from
/// -4 to 20, else d.ddde+XX or d.ddde-XX with at least two exponent digits.
/// Kind 2 writes N digits after the point (N in bits 8-15), the exact value
/// rounded to nearest, ties to even. nan, inf, -inf and -0 are written as
/// shown; nan takes no sign, and neither nan nor inf takes zero padding.
/// O(1) for kind 0; allocates nothing.
export FmtF64(w: mut Writer, x: f64, spec: u32): WriteError!void {
    try FmtFloatBits(w, raw.F64Bits(x), true, spec)
}

/// FmtF64 for an f32: kind 0 writes the shortest digits that read back as
/// the same f32.
export FmtF32(w: mut Writer, x: f32, spec: u32): WriteError!void {
    try FmtFloatBits(w, u64(raw.F32Bits(x)), false, spec)
}

// FmtFloatBits formats the IEEE bits b of an f64 (wide) or an f32.
FmtFloatBits(w: mut Writer, b: u64, wide: bool, spec: u32): WriteError!void {
    var mant_bits := 52
    var exp_bits := 11
    var bias := -1023
    var min_exp := -1085
    if !wide {
        mant_bits = 23
        exp_bits = 8
        bias = -127
        min_exp = -189
    }
    neg := (b >> (exp_bits + mant_bits)) != 0
    exp_mask := (1 << exp_bits) - 1
    var exp := Trunc[int](b >> mant_bits) & exp_mask
    var mant := b & ((Trunc[u64](1) << mant_bits) - 1)
    if exp == exp_mask {
        if mant != 0 {
            try w.Str("nan")
        } else if neg {
            try w.Str("-inf")
        } else {
            try w.Str("inf")
        }
        return
    }
    if exp == 0 {
        exp = 1
    } else {
        mant = mant | (Trunc[u64](1) << mant_bits)
    }
    exp += bias
    if spec & 15 == 2 {
        try FmtFixed(w, neg, mant, exp, mant_bits, Trunc[int]((spec >> 8) & 255))
        return
    }
    var buf: [32]u8 = [0; 32]
    var o := 0
    if neg {
        buf[0] = 0x2D
        o = 1
    }
    if mant == 0 {
        buf[o] = 0x30
        try w.Bytes(buf[0..o + 1])
        return
    }
    s := 64 - BitLen64(mant)
    sd := FpShortest(mant << s, exp - s - mant_bits, mant_bits, min_exp)
    var digs: [24]u8 = [0; 24]
    nd0 := FpNumDigits(sd.d)
    FpPutDigits(mut digs[0..nd0], sd.d)
    dp := nd0 + sd.p
    var nd := nd0
    for nd > 1 && digs[nd - 1] == 0x30 {
        nd -= 1
    }
    x := dp - 1
    if x >= -4 && x < 21 {
        // Fixed: the digits, zeros up to the point, then the fraction.
        if dp > 0 {
            m := IntMin(nd, dp)
            for i in 0..m {
                buf[o] = digs[i]
                o += 1
            }
            for _ in m..dp {
                buf[o] = 0x30
                o += 1
            }
            if nd > dp {
                buf[o] = 0x2E
                o += 1
                for i in dp..nd {
                    buf[o] = digs[i]
                    o += 1
                }
            }
        } else {
            buf[o] = 0x30
            buf[o + 1] = 0x2E
            o += 2
            for _ in dp..0 {
                buf[o] = 0x30
                o += 1
            }
            for i in 0..nd {
                buf[o] = digs[i]
                o += 1
            }
        }
    } else {
        buf[o] = digs[0]
        o += 1
        if nd > 1 {
            buf[o] = 0x2E
            o += 1
            for i in 1..nd {
                buf[o] = digs[i]
                o += 1
            }
        }
        buf[o] = 0x65
        var e := x
        if e < 0 {
            buf[o + 1] = 0x2D
            e = -e
        } else {
            buf[o + 1] = 0x2B
        }
        o += 2
        if e >= 100 {
            buf[o] = Trunc[u8](e / 100) + 0x30
            o += 1
        }
        buf[o] = Trunc[u8](e / 10 % 10) + 0x30
        buf[o + 1] = Trunc[u8](e % 10) + 0x30
        o += 2
    }
    try w.Bytes(buf[0..o])
}

// FmtFixed writes mant * 2^(exp - mant_bits) with prec digits after the
// point (Go's ftoa 'f' path): at most 18 significant digits through one
// scaling, else the exact Decimal, rounded half to even.
FmtFixed(w: mut Writer, neg: bool, mant: u64, exp: int, mant_bits: int, prec: int): WriteError!void {
    digits := if exp >= 0 {
        1 + FpLog10Pow2(1 + exp) + prec
    } else {
        1 + prec - FpLog10Pow2(-exp)
    }
    if mant == 0 || digits <= 18 {
        var digs: [24]u8 = [0; 24]
        var dp := 0
        var nd := 0
        if mant != 0 && digits > 0 {
            s := 64 - BitLen64(mant)
            fd := FpFixedWidth(mant << s, exp - s - mant_bits, digits, prec)
            if fd.d != 0 {
                nd = FpNumDigits(fd.d)
                FpPutDigits(mut digs[0..nd], fd.d)
                dp = nd + fd.p
                for nd > 0 && digs[nd - 1] == 0x30 {
                    nd -= 1
                }
            }
        }
        try FmtDigitsF(w, neg, digs[0..nd], dp, prec)
        return
    }
    var d := Decimal.Zero()
    d.Assign(mant)
    d.Shift(exp - mant_bits)
    d.Round(d.dp + prec)
    try FmtDigitsF(w, neg, d.d[0..d.nd], d.dp, prec)
}

// FmtDigitsF lays out the digits s (the value 0.s * 10^dp) with prec
// digits after the point: Go's fmtEFG for 'f'.
FmtDigitsF(w: mut Writer, neg: bool, s: []u8, dp: int, prec: int): WriteError!void {
    nd := s.Len()
    if neg {
        try w.Str("-")
    }
    if dp > 0 {
        m := IntMin(nd, dp)
        try w.Bytes(s[0..m])
        try FmtZeros(w, dp - m)
    } else {
        try w.Str("0")
    }
    if prec > 0 {
        try w.Str(".")
        lz := IntMin(prec, IntMax(0, -dp))
        off := dp + lz
        m := IntMin(prec - lz, IntMax(0, nd - off))
        try FmtZeros(w, lz)
        if m > 0 {
            try w.Bytes(s[off..off + m])
        }
        try FmtZeros(w, prec - lz - m)
    }
}

// FloatDigits writes the decimal digits of x into out (ASCII) and returns
// how many (nd) and where the point is (dp): the magnitude of x is
// 0.out[0..nd] * 10^dp, out[0] is not '0' and the last digit is not '0'
// (trailing zeros trimmed), and nd is 0 for a zero, a rounded-away value,
// NaN and the infinities (dp 0). For std.math's ShortestDigits,
// ShortestDigitsInto and ExactDigits. mode: 0 the shortest round-trip
// digits (FpShortest, as FmtF64); 1 n significant digits; 2 n digits after
// the point; 1 and 2 round the exact value half to even (strconv 'e' with
// n-1 and 'f' with n), 3 and 4 as 1 and 2 with ties away from zero
// (ECMA-262's toPrecision, toExponential and toFixed). n is at least 1 for
// modes 1 and 3 and at least 0 for 2 and 4 (std.math checks). Mode 0 needs
// an out of 17 bytes: FpShortest scales x's ulp into [1, 10) (into
// [4/3, 40/3) just above a power of two, whose mantissa is 2^52), so its d
// is below 10 * 2^53 < 10^17 and has at most 17 digits. The other modes
// need 768, an f64's longest exact expansion.
// O(1) through one 128-bit scaling (Go 1.27's fixedWidthFloat) when the
// result has at most 18 digits, else the exact Decimal: O(767) digits,
// as Go's slow path. Allocates nothing.
export FloatDigits(out: mut []u8, x: f64, mode: int, n: int): (nd: int, dp: int) {
    b := raw.F64Bits(x)
    var exp := Trunc[int](b >> 52) & 0x7FF
    var mant := b & 0xFFFFFFFFFFFFF
    if exp == 0x7FF {
        return (nd: 0, dp: 0)
    }
    if exp == 0 {
        exp = 1
    } else {
        mant = mant | (Trunc[u64](1) << 52)
    }
    exp -= 1023
    if mant == 0 {
        return (nd: 0, dp: 0)
    }
    s := 64 - BitLen64(mant)
    if mode == 0 {
        sd := FpShortest(mant << s, exp - s - 52, 52, -1085)
        return FloatDigitsPut(out, sd.d, sd.p)
    }
    frac := mode == 2 || mode == 4
    up := mode == 3 || mode == 4
    digits := if !frac {
        n
    } else if exp >= 0 {
        1 + FpLog10Pow2(1 + exp) + n
    } else {
        1 + n - FpLog10Pow2(-exp)
    }
    if digits <= 0 {
        // Below 0.2 * 10^-n (ftoa64's "guaranteed all zeros"), so zero
        // under either rounding.
        return (nd: 0, dp: 0)
    }
    if digits <= 18 {
        prec := if frac {
            n
        } else {
            0x40000000
        }
        var p := digits - 1 - FpLog10Pow2(exp - s - 52 + 63)
        pre := FpPrescale(exp - s - 52, p, FpLog2Pow10(p))
        var u := FpUscale(mant << s, pre)
        pow := &FpPow10
        if u >= FpUnmin(pow[digits]) {
            u = FpDiv10(u)
            p -= 1
        }
        for p > prec {
            u = FpDiv10(u)
            p -= 1
        }
        d := if up {
            (u + 2) >> 2
        } else {
            FpRound(u)
        }
        if d == 0 {
            return (nd: 0, dp: 0)
        }
        return FloatDigitsPut(out, d, -p)
    }
    var dec := Decimal.Zero()
    dec.Assign(mant)
    dec.Shift(exp - 52)
    at := if frac {
        dec.dp + n
    } else {
        n
    }
    if at < 0 {
        return (nd: 0, dp: 0)
    }
    if up {
        dec.RoundHalfUp(at)
    } else {
        dec.Round(at)
    }
    dec.Trim()
    m := IntMin(dec.nd, out.Len())
    for i in 0..m {
        out[i] = dec.d[i]
    }
    return (nd: m, dp: dec.dp)
}

// FloatDigitsPut writes d * 10^p (d >= 1) as trimmed digits into out.
FloatDigitsPut(out: mut []u8, d: u64, p: int): (nd: int, dp: int) {
    nd0 := FpNumDigits(d)
    FpPutDigits(mut out[0..nd0], d)
    var nd := nd0
    for nd > 1 && out[nd - 1] == 0x30 {
        nd -= 1
    }
    return (nd: nd, dp: nd0 + p)
}
