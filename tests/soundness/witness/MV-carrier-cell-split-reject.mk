// verdict: reject E0502
// note: a struct holding carrier Cell[?&i32] collapses on a shared whole borrow, so moving b into c through &p keeps n's loan live in p and writing n is E0502
struct Pair {
    c: Cell[?&i32]
    a: &i32
    b: &i32
}

Stash(p: &Pair) {
    p.c.Store(some(p.b))
}

Bad(t: &i32): i32 {
    var n: i32 = 7
    p := Pair(c: Cell[?&i32].New(none), a: t, b: &n)
    Stash(&p)
    n = 42 // <- E0502
    return *p.a
}

export Main(sys: &System): void {
    x: i32 = 1
    _ = Bad(&x)
}
