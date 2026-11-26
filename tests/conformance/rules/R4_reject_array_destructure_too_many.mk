// verdict: reject E0305
// rule: R4
// note: an array destructure names exactly the array's elements
TooMany() {
    rs := [Acquire("a"), Acquire("b")]
    [a, b, c] := rs // <- E0305
    a.Close()
    b.Close()
    c.Close()
}
