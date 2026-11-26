// verdict: ok
// rule: MKH-933
// note: a binding that writes an E!T type, handled before it is assigned again and before its scope ends on every path: by try, else, match or .Must(), returned as a value, handed to another such binding, or borrowed
error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

type R = Bad!int

Copy[T: Plain](x: &T): T {
    return *x
}

Returned(n: int): R {
    var r: Bad!int = Get(n)
    return r
}

Tried(n: int): Bad!int {
    var r: Bad!int = Get(n)
    return try r
}

Matched(n: int): int {
    var r: Bad!int = Get(n)
    match r {
        ok(v) {
            return v
        }
        err(_) {
            return 3
        }
    }
}

Handed(n: int): int {
    var r: Bad!int = Get(n)
    var s: Bad!int = r
    return s else { 4 }
}

Again(n: int): int {
    var r: Bad!int = Get(n)
    a := r.Must()
    r = Get(a + 1)
    return r else { 6 }
}

Paths(n: int, c: bool): int {
    var r: Bad!int = Get(n)
    if c {
        return r else { 7 }
    }
    return r else { 8 }
}

Loop(n: int): int {
    var t := 0
    var r: Bad!int = Get(n)
    for i in 0..3 {
        t += r else { 9 }
        r = Get(i - 1)
    }
    return t + (r else { 10 })
}

Borrowed(n: int): int {
    var r: Bad!int = Get(n)
    return Copy(&r) else { 11 }
}
