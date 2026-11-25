// verdict: ok
// rule: R4
// note: an array destructure moves each element of a linear array into its own binding
Pair(): [2]Res {
    return [Acquire("a"), Acquire("b")]
}

Split[T](xs: [2]T): (first: T, second: T) {
    [first, second] := xs
    return (first, second)
}

Release() {
    rs := Pair()
    [a, b] := rs
    a.Close()
    b.Close()
    (first, second) := Split(Pair())
    second.Close()
    first.Close()
    grid := [Pair(), Pair()]
    [top, bottom] := grid
    [c, d] := top
    var [e, f] := bottom
    c.Close()
    d.Close()
    e.Close()
    f.Close()
}
