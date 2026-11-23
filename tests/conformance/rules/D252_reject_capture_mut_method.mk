// verdict: reject E0508
// rule: D-252
// note: calling a `mut self` method on a captured binding that is not `var` is E0508
Grow(a: &Arena, xs: mut []i32): OutOfMemory!int {
    l := List[i32].New(a)
    xs.Sort(fn(p: &i32, q: &i32): bool {
        l.Push(*p) else { // <- E0508
        }
        return *p < *q
    })
    return l.Len()
}
