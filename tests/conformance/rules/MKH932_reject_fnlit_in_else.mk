// verdict: reject E0703
// rule: MKH-932
// note: a fn literal in the else branch of `if T is u32` keeps T opaque, so an integer literal there needs a bound
Lit[T: Plain](x: T): T {
    if T is u32 {
        return x
    } else {
        h := fn(y: T): T { return y + 1 } // <- E0703
        return h(x)
    }
}
