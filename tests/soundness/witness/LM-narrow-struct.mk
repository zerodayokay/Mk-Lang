// verdict: reject E0402
// note: a by-value match whose arm tests one field to a plain variant (o: none) and discards a linear sibling inside a constructor (a: some(_)). The none edge proves only o holds nothing linear; a's Res is still in the temporary. Accepted, the Res is dropped unconsumed. Mutant M-LM-whole, which lets one taken or narrowed part consume the whole temporary, accepts it
linear struct S {
    a: ?Res
    o: ?int
}

Drain(s: S): int {
    return match s { // <- E0402
        S(a: some(_), o: none) { 0 }
        S(a: some(r), o: some(k)) {
            r.Close()
            k
        }
        S(a: none, o: _) { 1 }
    }
}
