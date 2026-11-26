// A fn literal written inside `if T is X` sees the narrowing: its
// signature, the types written T in its body, a captured value of type T
// and a nested literal all read X; a fn value naming T is X's there too,
// returned or assigned. The else branch keeps T opaque. Each instance
// takes its own branch.
Do(const f: fn(): int): int {
    return f()
}

Bump(const f: fn()) {
    f()
}

Id[T](x: T): T {
    return x
}

Inc(y: u32): u32 {
    return y + 1
}

Lit[T: Plain](x: T): T {
    if T is u32 {
        h := fn(y: T): T { return Id[T](y) }
        return h(x) + 1
    }
    return x
}

Cap[T: Plain](x: T): int {
    if T is u32 {
        var y: T = x + 1
        return Do(fn(): int { return int(y) + int(x) * 10 })
    }
    return -1
}

Mut[T: Plain](x: T): T {
    var v: T = x
    if T is u32 {
        Bump(fn() { v = v + 5 })
        return v
    }
    return v
}

Nest[T: Plain, U: Plain](x: T): int {
    if T is U {
        if U is i64 {
            g := fn(a: T): T {
                h := fn(b: T): T { return Id[T](b) * 2 }
                return h(a) + 1
            }
            return int(g(x))
        }
    }
    return -1
}

Els[T: Plain](x: T): int {
    if T is u8 {
        return 1
    } else {
        k := fn(a: T): T { return a }
        _ = k(x)
        return 2
    }
}

Pick[T: Plain](x: T): fn(T): T {
    if T is u32 {
        return fn(y: T): T { return y + 1 }
    }
    return fn(y: T): T { return y }
}

Var[T: Plain](x: T): T {
    var f: fn(T): T = fn(y: T): T { return y }
    if T is u32 {
        f = fn(y: T): T { return y * 10 }
    }
    return f(x)
}

Param[T: Plain](x: T, f: fn(T): T): T {
    if T is u32 {
        return f(x) + 1
    }
    return x
}

export Main(sys: &System): WriteError!void {
    try sys.Print("{a} {b} {c} {d}\n", (a: Lit[u32](3), b: Lit[int](3), c: Cap[u32](4), d: Cap[i8](4)))
    try sys.Print("{a} {b} {c} {d}\n", (a: Mut[u32](1), b: Mut[i8](1), c: Nest[i64, i64](3), d: Nest[i64, u8](3)))
    p := Pick[u32](0)
    q := Pick[i64](0)
    try sys.Print("{a} {b} {c} {d} {e} {f} {g}\n", (a: Els[u8](1), b: Els[int](1), c: p(4), d: q(4), e: Var[u32](3), f: Var[i64](3), g: Param[u32](3, Inc)))
}
