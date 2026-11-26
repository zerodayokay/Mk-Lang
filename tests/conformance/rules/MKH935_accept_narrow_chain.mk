// verdict: ok
// rule: MKH-935
// note: inside `if T is U { if U is u32 { .. } }` T is u32: its values, the types written T (a type argument, a local's type, Cast[T]) and the result
Id[T](x: T): T {
    return x
}

Ch[T: Plain, U: Plain](x: T, n: int): T {
    if T is U {
        if U is u32 {
            a := Id[T](x) + 1
            var b: T = x + 2
            return a + b + Cast[T](n)
        }
    }
    return x
}

Run(): u32 {
    return Ch[u32, u32](1, 2)
}
