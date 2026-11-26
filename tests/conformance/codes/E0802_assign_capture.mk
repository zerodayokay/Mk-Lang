// verdict: reject E0802
// note: an E!T assigned, inside a fn literal, to a binding the literal captures is E0802: the literal writes the enclosing binding through its environment, where nothing sees whether the value it held was handled
error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

Twice(const f: fn()) {
    f()
    f()
}

Use(): int {
    var c: Bad!int = Get(1)
    a := c else { 0 }
    Twice(fn() { c = Get(-1) }) // <- E0802
    return a + (c else { 7 })
}
