// verdict: ok
// rule: R2
// note: a by-value match consumes a linear temporary when an arm binds every linear part, destructures it, or tests it to a variant that holds none, also when an arm compiled by sequential testing follows one whose test failed after a narrowing
error F
error G

linear struct S {
    a: ?Res
    b: ?Res
}

enum K {
    A(r: Res)
    B(n: int)
}

linear struct H {
    r: Res
    o: ?int
}

struct P[A, B] {
    a: A
    r: B
}

Parts(s: S): int {
    return match s {
        S(a: some(x), b: some(y)) {
            x.Close()
            y.Close()
            0
        }
        S(a: some(x), b: none) {
            x.Close()
            1
        }
        S(a: none, b: some(y)) {
            y.Close()
            2
        }
        S(a: none, b: none) { 3 }
    }
}

Sequential(k: K): int {
    return match k {
        .B(n: 0..5) { 1 }
        .A(r) {
            r.Close()
            1000
        }
        .B(n: m) { m }
    }
}

Nested(p: P[F!Res, G!int]): int {
    return match p {
        P(a: err(_), r: _) { 0 }
        P(a: ok(x), r: err(_)) {
            x.Close()
            1
        }
        P(a: ok(x), r: ok(n)) {
            x.Close()
            n
        }
    }
}

Destructured(h: H): i32 {
    return match h {
        H(r: Res(id), o: none) { id }
        H(r: Res(id), o: some(_)) { id }
    }
}

Payload(o: ?Res): i32 {
    return match o {
        some(Res(id)) { id }
        none { 0 }
    }
}

Alternatives(h: H): i32 {
    return match h {
        H(r: Res(id: 1), o: _) | H(r: Res(id: 2), o: none) { 0 }
        H(r: Res(id), o: _) { id }
    }
}

PayloadAlternatives(o: ?Res): i32 {
    return match o {
        some(Res(id: 1) | Res(id: 2)) { 0 }
        some(Res(id)) { id }
        none { 1 }
    }
}

SameVariant(k: K): i32 {
    return match k {
        .A(r: Res(id: 1)) | .A(r: Res(id: 2)) { 0 }
        .A(r: Res(id)) { id }
        .B(n: _) { 1 }
    }
}

SameSome(o: ?Res): i32 {
    return match o {
        some(Res(id: 1)) | some(Res(id: 2)) { 0 }
        some(Res(id)) { id }
        none { 1 }
    }
}
