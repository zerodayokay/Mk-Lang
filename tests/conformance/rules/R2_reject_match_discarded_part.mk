// verdict: reject E0402
// rule: R2
// note: an arm of a by-value match that tests or takes one part and discards a linear sibling inside a constructor leaves the sibling in the temporary
linear struct S {
    a: ?Res
    b: ?Res
    o: ?int
}

enum K {
    A(a: ?Res, o: ?int)
    B
}

Narrowed(s: S): int {
    return match s { // <- E0402
        S(a: some(_), b: none, o: none) { 0 }
        S(a, b, o: _) {
            Take(a)
            Take(b)
            1
        }
    }
}

Taken(s: S): int {
    return match s { // <- E0402
        S(a: some(r), b: some(_), o: _) {
            r.Close()
            0
        }
        S(a, b, o: _) {
            Take(a)
            Take(b)
            1
        }
    }
}

Variant(k: K): int {
    return match k { // <- E0402
        .A(a: some(_), o: none) { 0 }
        .A(a, o: _) {
            Take(a)
            1
        }
        .B { 2 }
    }
}

enum J {
    A(r: Res)
    B(n: int, r: ?Res)
}

Sequential(j: J): int {
    return match j { // <- E0402
        .B(n: 0..5, r: none) { 1 }
        .A(r) {
            r.Close()
            2
        }
        .B(n: _, r: some(_)) { 3 }
        .B(n: m, r: none) { m }
    }
}
