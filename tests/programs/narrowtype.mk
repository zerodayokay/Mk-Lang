// A type written T inside `if T is X` is X: a type argument, a local's
// declared type, a conversion, a nested generic and a generic owner's
// method, in an `else if T is` chain; the else branch keeps T opaque.
// Each instance takes its own branch.
Id[T](x: T): T {
    return x
}

struct Box[T: Plain] {
    v: T
}

Box[T].Get(&self): T {
    return self.v
}

Wrap[T: Plain](x: T): Box[T] {
    return Box[T](v: x)
}

Shapes[T: Plain](x: T): T {
    if T is u32 {
        a := Id[T](x) * 2
        var b: T = a + 1
        c := T(b) + 1
        d := Id[Box[T]](Box[T](v: c)).Get() + 1
        e := Wrap[T](d).v + 1
        return e
    } else if T is i64 {
        var f: T = x - 1
        return Id[T](f) * 10
    } else {
        return Id[T](x)
    }
}

export Main(sys: &System): WriteError!void {
    a := Shapes[u32](3)
    b := Shapes[i64](3)
    c := Shapes[int](3)
    try sys.Print("{a} {b} {c}\n", (a, b, c))
}
