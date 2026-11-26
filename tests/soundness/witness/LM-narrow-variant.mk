// verdict: reject E0402
// note: the enum variant form of LM-narrow-struct: .A(a: some(_), o: none), which the decision tree compiles. Accepted, a's Res is dropped unconsumed. Mutant M-LM-whole accepts it
enum K {
    A(a: ?Res, o: ?int)
    B
}

Drain(k: K): int {
    return match k { // <- E0402
        .A(a: some(_), o: none) { 0 }
        .A(a: some(r), o: some(n)) {
            r.Close()
            n
        }
        .A(a: none, o: _) { 1 }
        .B { 2 }
    }
}
