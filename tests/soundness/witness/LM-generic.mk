// verdict: reject E0402
// note: a field of a type parameter matched by `_` in a by-value match (b: _). The field's type is not closed, so check's E0408 does not judge it; an unbounded parameter may be linear, so the arm leaves it in the temporary. Accepted, Get[Res, Res] drops b's Res unconsumed. Mutant M-LM-whole accepts it
struct S[A, B] {
    a: ?A
    b: B
}

Get[A, B](s: S[A, B]): ?A {
    return match s { // <- E0402
        S(a: some(x), b: _) { some(x) }
        S(a: none, b: _) { none }
    }
}
