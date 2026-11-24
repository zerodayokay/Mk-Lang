// verdict: reject E0507
// note: a `plain view struct` cannot appear in an `ends` clause (Invariant B)
plain view struct Sym {
    id: u32
}

Sym.Release(self) ends self { // <- E0507
    _ = self
}

export Main(sys: &System): void {
    _ = sys
}
