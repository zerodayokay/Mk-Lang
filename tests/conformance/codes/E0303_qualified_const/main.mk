// verdict: reject E0303
// note: T(m.C) of another module's const, named qualified or through a third module, is judged by the const's value as T(C) of the module's own is; a u64 above the largest i64 never fits a signed type, and a const of an enum type or of char is judged by its type, as its tag or a char literal is
import lib
import relay

const Top: u64 = 9223372036854775808

Fits(): i64 {
    a := i8(lib.C) + i8(lib.Low) + i8(relay.C) + i8(lib.Al) + i8(-lib.C) + i8((lib.C))
    b := u8(lib.U) + Cast[u8](i16(lib.U))
    c := i32(lib.L)
    d := f32(lib.Half) + f32(lib.C)
    return i64(a) + i64(b) + i64(c) + Cast[i64](d)
}

TooBig(): i8 {
    return i8(lib.Big) // <- E0303
}

TooBigRelay(): i8 {
    return i8(relay.Big) // <- E0303
}

TooBigOperator(): u8 {
    return u8(lib.BigOp) // <- E0303
}

Negative(): u8 {
    return u8(lib.Neg) // <- E0303
}

Rounds(): f32 {
    return f32(lib.Tenth) // <- E0303
}

Bounds(): i8 {
    return i8(lib.Over) + i8(lib.Under) // <- E0303 E0303
}

PastInt64(): i64 {
    return i64(lib.Wide) // <- E0303
}

PastInt64Own(): i64 {
    return i64(Top) // <- E0303
}

Surrogate(): char {
    return char(lib.Surrogate) // <- E0303
}

enum W: i32 {
    Lo = -2
    Hi = 300
}

const Kw: W = W.Hi

EnumConst(): i8 {
    return i8(lib.Kw) + i8(Kw) // <- E0303 E0303
}

const Ch: char = '\u{1F600}'

CharConst(): u8 {
    return u8(lib.Ch) + u8(Ch) // <- E0303 E0303
}
