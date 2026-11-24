// verdict: reject E0507
// note: a `plain view struct` cannot accept holder-store clauses on by-value or `&` parameters (Invariant B)
plain view struct Sym {
    id: u32
}

Store(v: Sym, x: &i32 into v) { // <- E0507
    _ = v
    _ = x
}

export Main(sys: &System): void {
    _ = sys
}
