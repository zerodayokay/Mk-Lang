// verdict: reject E0402
// rule: R2
// note: `_` consumes nothing, also on a linear struct with no fields; an arm whose struct pattern fails a later test has consumed nothing
linear struct Empty {}

linear struct P {
    e: ?Empty
    n: int
}

Discarded(e: ?Empty): int {
    return match e { // <- E0402
        some(_) { 1 }
        none { 0 }
    }
}

Alternative(e: Empty): int {
    return match e { // <- E0402
        Empty() | _ { 1 }
    }
}

Failed(p: P): int {
    return match p { // <- E0402
        P(e: some(Empty()), n: 5) { 0 }
        P(e: some(_), n: m) { m }
        P(e: none, n: m) { m }
    }
}

Shared(e: Empty): int { // <- E0402
    return match &e {
        Empty() { 1 }
    }
}
