// verdict: reject E0802
// note: a call's E!T assigned into an element or through a reference is E0802, as in an array literal
error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

Use(xs: mut [2](r: Bad!int), p: mut (Bad!int)) {
    xs[1].r = Get(-1) // <- E0802
    *p = Get(-2)      // <- E0802
}
