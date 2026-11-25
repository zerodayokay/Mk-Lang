// verdict: reject E0303
// note: f32(x) passes a float literal or named const whose value is exactly an f32, and no other; an operator over float consts is not judged by its value; a float const to an integer is never T(x)
const Half: f64 = 0.5
const Tenth: f64 = 0.1
const Big = 1e300

Exact(): f32 {
    return f32(0.5) + f32(Half) + f32(-1.5e3)
}

Rounds(): f32 {
    return f32(0.1) // <- E0303
}

RoundsConst(): f32 {
    return f32(Tenth) // <- E0303
}

Overflows(): f32 {
    return f32(Big) // <- E0303
}

ToInt(): u8 {
    return u8(Half) // <- E0303
}

Operator(): f32 {
    return f32(Half + Half) // <- E0303
}

OperatorToInt(): i64 {
    return i64(Big * Big) // <- E0303
}
