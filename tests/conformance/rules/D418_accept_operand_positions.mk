// verdict: ok
// rule: D-418
// note: an if or match is a value wherever an expression stands: an operand of a binary or unary operator, before an operator or a suffix, an index, a condition, a scrutinee and a repeat literal's value
Operands(c: bool, n: int): int {
    a := 1 + if c { 2 } else { 3 }
    b := -if c { 2 } else { 3 }
    d := match n {
        0 { 1 }
        _ { 2 }
    } + 1
    e := if c { "ab" } else { "abc" }.Len()
    return a + b + d + e
}

Places(xs: []int, c: bool): int {
    var r := xs[if c { 0 } else { 1 }]
    if if c { r > 0 } else { false } {
        r += 1
    }
    ys := [if c { 1 } else { 2 }; 3]
    return r + ys[0] + match if c { 1 } else { 2 } {
        1 { 10 }
        _ { 20 }
    }
}
