// verdict: ok
// rule: D-252
// note: a non-escaping const fn literal may mutate `var` captures and reborrow `mut T` / `mut []T` captures for the call's statement
Bump(n: mut int) {
    *n += 1
}

Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

CountAndCollect(a: &Arena, xs: mut []i32, limit: i32, total: mut i32, out_buf: mut []i32): OutOfMemory!int {
    var compares := 0
    var seen := List[i32].New(a)
    xs.Sort(fn(p: &i32, q: &i32): bool {
        Bump(mut compares)
        if *p > limit {
            seen.Push(*p) else {
            }
        }
        return *p < *q
    })
    var idx := 0
    Each(xs[..], fn(x: &i32) {
        *total += *x
        if idx < out_buf.Len() {
            out_buf[idx] = *x
        }
        idx += 1
    })
    return compares + seen.Len()
}
