// verdict: reject E0305
// rule: R4
// note: an array destructure names every element, so no linear element is dropped
Partial() {
    rs := [Acquire("a"), Acquire("b")]
    [a] := rs // <- E0305
    a.Close()
}
