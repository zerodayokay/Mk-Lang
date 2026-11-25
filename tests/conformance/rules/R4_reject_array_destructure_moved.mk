// verdict: reject E0401
// rule: R4
// note: an array destructure moves the whole array
Again() {
    rs := [Acquire("a"), Acquire("b")]
    [a, b] := rs
    a.Close()
    b.Close()
    [c, d] := rs // <- E0401
    c.Close()
    d.Close()
}
