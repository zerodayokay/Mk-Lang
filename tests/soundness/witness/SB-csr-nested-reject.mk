// verdict: reject E0505
// note: csr-sh1's class one level down: u's Cell sits in a field (w) beside the loaned place u.x, and Put stores a shallow &i32 of u.x into t. The walk of a stored value's own loans does not trust a holder's type: from S it reaches t, then u through t's loan (R5: a loan of u.x overlaps u), and u holds W, which holds a Cell inline, so it takes n's loan: E0505 when n dies. A walk that takes the store cut of R11 at t, or skips t's shallow loan, checks this clean
struct W {
    c: Cell[?&i32]
}

struct U {
    w: W
    x: i32
}

struct T {
    r: &i32
}

Put(t: mut T, r: &i32 into t) {
    t.r = r
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
    dummy: i32 = 0
    var t := T(r: &dummy)
    Put(mut t, &u.x)
    n: i32 = 7
    var h := H(s: none)
    Keep(mut h, S(a: mut t, b: &n)) // <- E0505
}

UseU(u: &U): i32 {
    Inner(u)
    var v: i32 = -1
    if r := u.w.c.Load() {
        v = *r
    }
    return v
}
