// Why: a named const is a match pattern compared by value; this checks the
// value tests at run time, beside literals, in each place a pattern nests.
// What: Main prints one line from the matches below.
// How: consts of this module and of codes, as `K` and `codes.K`.

import codes

const Limit = 8
const Hello = "hi"
const Neg: i8 = -1
const Star = '*'
const On = true

struct P {
    a: str
    b: int
}

Op(o: u8): str {
    return match o {
        codes.OpAdd { "add" }
        codes.OpSub | 3 { "sub" }
        _ { "?" }
    }
}

Kind(n: int): int {
    return match n {
        Limit { 1 }
        _ { 0 }
    }
}

Rec(s: str, n: int): int {
    return match P(a: s, b: n) {
        P(a: Hello, b: 1) { 1 }
        P(a: "hi", b: 2) { 2 }
        _ { 0 }
    }
}

Sign(n: i8): int {
    return match n {
        Neg { -1 }
        0 { 0 }
        _ { 1 }
    }
}

Flag(b: bool): int {
    return match b {
        On { 1 }
        false { 0 }
    }
}

Ch(c: char): int {
    return match c {
        Star | '+' { 1 }
        _ { 0 }
    }
}

Opt(o: ?u8): int {
    return match o {
        some(codes.OpAdd) { 1 }
        some(_) { 2 }
        none { 3 }
    }
}

export Main(sys: &System): WriteError!void {
    o := Op(1)
    p := Op(3)
    q := Op(9)
    k := Kind(8) + Kind(7)
    r := Rec("hi", 1) * 100 + Rec("hi", 2) * 10 + Rec("ho", 1)
    s := Sign(-1) + Sign(9) * 10
    f := Flag(true) + Flag(false)
    c := Ch('*') + Ch('a')
    x := Opt(some(1)) * 100 + Opt(some(2)) * 10 + Opt(none)
    try sys.Print("{o} {p} {q} {k} {r} {s} {f} {c} {x}\n", (o, p, q, k, r, s, f, c, x))
}
