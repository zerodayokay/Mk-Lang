// verdict: reject E0402
// note: LM-narrow-struct inside an optional: some(S(a: none, b: some(_))). The none edge proves only a holds nothing linear; b's Res is still in the temporary. Accepted, the Res is dropped unconsumed. Mutant M-LM-whole accepts it
linear struct S {
    a: ?int
    b: ?Res
}

Drain(o: ?S): int {
    return match o { // <- E0402
        some(S(a: none, b: some(_))) { 0 }
        some(S(a: some(n), b: some(y))) {
            y.Close()
            n
        }
        some(S(a: _, b: none)) { 2 }
        none { 3 }
    }
}
