// verdict: reject E0402
// rule: R2
// note: each element bound by an array destructure must be consumed
Unconsumed() {
    [a, b] := [Acquire("a"), Acquire("b")] // <- E0402
    a.Close()
    Use(&b)
}
