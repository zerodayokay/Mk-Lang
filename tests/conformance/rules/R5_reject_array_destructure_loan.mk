// verdict: reject E0502
// rule: R5
// note: the bindings of an array destructure keep the elements' loans
Hold(): i32 {
    var x: i32 = 1
    var y: i32 = 2
    [p, q] := [&x, &y]
    x = 5 // <- E0502
    return *p + *q
}
