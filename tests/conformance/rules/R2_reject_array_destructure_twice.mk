// verdict: reject E0401
// rule: R2
// note: an element bound by an array destructure is consumed once
Twice() {
    [a, b] := [Acquire("a"), Acquire("b")]
    a.Close()
    a.Close() // <- E0401
    b.Close()
}
