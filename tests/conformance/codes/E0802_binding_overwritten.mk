// verdict: reject E0802
// note: a binding that writes an E!T type, assigned again while the error it holds may be unread, is E0802, also in a loop
error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

Twice(): int {
    var c: Bad!int = Get(-1)
    c = Get(1) // <- E0802
    return c else { 7 }
}

Loop(): int {
    var c: Bad!int = Get(-1)
    for i in 0..2 {
        c = Get(i) // <- E0802
    }
    return c else { 7 }
}
