// verdict: reject E0402
// note: a by-value match whose arm takes one linear field (a: some(r)) and discards its linear sibling inside a constructor (b: some(_)). The take moves a's Res out; b's is still in the temporary. Accepted, b's Res is dropped unconsumed. Mutant M-LM-whole, which lets one taken or narrowed part consume the whole temporary, accepts it
linear struct S {
    a: ?Res
    b: ?Res
}

Drain(s: S): int {
    return match s { // <- E0402
        S(a: some(r), b: some(_)) {
            r.Close()
            0
        }
        S(a, b: none) {
            match a {
                some(r) { r.Close() }
                none {}
            }
            1
        }
        S(a: none, b: some(r)) {
            r.Close()
            2
        }
    }
}
