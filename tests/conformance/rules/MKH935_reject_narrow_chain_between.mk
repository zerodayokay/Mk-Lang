// verdict: reject E0703
// rule: MKH-935
// note: between `if T is U` and `if U is u32` T is U, which no test has narrowed yet and has no bound for an integer literal
Ch[T: Plain, U: Plain](x: T): T {
    if T is U {
        if U is u32 {
            return x + 1
        }
        return x + 1 // <- E0703
    }
    return x
}
