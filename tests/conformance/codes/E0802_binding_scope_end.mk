// verdict: reject E0802
// note: a binding that writes an E!T type is E0802 at its declaration when its scope ends, on any path, while the error it holds may be unread: read on one path only, or left by a try exit
error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

error Other

Other2(n: int): Other!int {
    if n < 0 {
        fail Other
    }
    return n
}

OnePath(n: int): int {
    var c: Bad!int = Get(n) // <- E0802
    if n > 0 {
        return c else { 7 }
    }
    return 0
}

TryExit(n: int): Other!int {
    var c: Bad!int = Get(n) // <- E0802
    k := try Other2(n)
    return k + (c else { 7 })
}
