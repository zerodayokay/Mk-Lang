// verdict: ok
// note: a plain read-only view struct keeps its field split across a shared whole borrow, and a read-only container view passed by & requires no self-store clause
view struct RTwo {
    a: &i32
    b: &i32
}

Touch(v: &RTwo): i32 {
    return *v.a + *v.b
}

KeepSplit(t: &i32): i32 {
    var n: i32 = 7
    v := RTwo(a: t, b: &n)
    _ = Touch(&v)
    n = 42
    return *v.a
}

struct Prog {
    names: List[str]
}

CopyNames(a: &Arena, prog: &Prog, out: mut List[str]): OutOfMemory!void *prog into out, *prog into a {
    for s in prog.names.Items() {
        try out.Push(s)
    }
}

export Main(sys: &System): void {
    x: i32 = 1
    _ = KeepSplit(&x)
}
