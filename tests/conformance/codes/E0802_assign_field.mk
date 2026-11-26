// verdict: reject E0802
// note: an E!T assigned into a field is E0802, as it is in a construction's field; only a binding that writes its type keeps one
error Bad

struct H {
    r: Bad!int
}

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

Use(h: mut H): int {
    var bad: Bad!int = Get(-1)
    h.r = bad // <- E0802
    return 0
}
