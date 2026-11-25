// verdict: reject E0505
// note: csr-sh1 with t built directly over &u.x (no Put): t's type T holds only a &i32, so the store cut of R11 would stop at t, but the walk of a stored value's own loans does not trust a holder's type. Keep stores S(a: mut t, b: &n) into h; that walk reaches t, then u through t's loan, and u holds a Cell inline, so it takes n's loan: E0505 when n dies. A walk that takes the cut checks this clean; one that reads shallow marks but takes no cut still rejects it
struct U {
    c: Cell[?&i32]
    x: i32
}

struct T {
    r: &i32
}

struct S {
    a: mut T
    b: &i32
}

struct H {
    s: ?S
}

Keep(h: mut H, x: S into h) {
    h.s = some(x)
}

Inner(u: &U) {
    var t := T(r: &u.x)
    n: i32 = 7
    var h := H(s: none)
    Keep(mut h, S(a: mut t, b: &n)) // <- E0505
}

UseU(u: &U): i32 {
    Inner(u)
    var v: i32 = -1
    if r := u.c.Load() {
        v = *r
    }
    return v
}
