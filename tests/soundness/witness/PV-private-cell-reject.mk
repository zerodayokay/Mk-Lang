// verdict: reject E0408
// note: a `plain view struct` cannot hold interior-mutable or non-Freeze fields such as Cell[u32] (Invariant A)
plain view struct BadHandle {
    c: Cell[u32] // <- E0408
}

export Main(sys: &System): void {
    _ = sys
}
