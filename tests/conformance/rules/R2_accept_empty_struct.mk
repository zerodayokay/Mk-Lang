// verdict: ok
// rule: R2
// note: a linear struct with no fields is consumed by a struct pattern that names it in a by-value match, at the top or inside a field, an optional, a variant or an ok payload
error Bad

linear struct Empty {}

linear struct S {
    e: Empty
    n: int
}

linear struct Tok[T] {}

enum K {
    A(e: Empty)
    B(n: int)
}

Get(n: int): Bad!Empty {
    if n < 0 {
        fail Bad
    }
    return Empty()
}

Optional(e: ?Empty): int {
    return match e {
        some(Empty()) { 1 }
        none { 0 }
    }
}

Whole(e: Empty): int {
    return match e {
        Empty() { 2 }
    }
}

Field(s: ?S): int {
    return match s {
        some(S(e: Empty(), n: 0..3)) { 30 }
        some(S(e: Empty(), n)) { n }
        none { 9 }
    }
}

Variant(k: K): int {
    return match k {
        .A(e: Empty()) { 1 }
        .B(n) { n }
    }
}

Ok(n: int): int {
    return match Get(n) {
        ok(Empty()) { 1 }
        err(_) { 0 }
    }
}

Generic(t: Tok[int]): int {
    return match t {
        Tok() { 7 }
    }
}

Drive(n: int): int {
    s := some(S(e: Empty(), n: n))
    return Optional(some(Empty())) + Optional(none) + Whole(Empty()) + Field(s) + Variant(K.A(e: Empty())) + Ok(n) + Generic(Tok[int]())
}
