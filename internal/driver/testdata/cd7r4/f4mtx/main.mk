struct G[T] {
    v: T
    next: ?&G[?T]
}

var GI: G[?&Mutex[?&i32]] = G[?&Mutex[?&i32]](v: none, next: none)

Pick(x: &i32): &G[?&Mutex[?&i32]] from x {
    return &GI
}

export Main(sys: &System) {
    n: i32 = 7
    g := Pick(&n)
    if w := g.v {
        _ = w
        sys.Print("some\n") else {}
    } else {
        sys.Print("none\n") else {}
    }
}
