// verdict: reject E0501
// note: passing a mut reference or mut slice by ArgReborrow beside a mut-capturing literal that borrows the same reference or slice creates aliasing exclusive references
CallMut(r: mut i32, const f: fn()) {
    *r = 10
    f()
    *r += 1
}

ReborrowMutConflict(p: mut i32) {
    CallMut(p, fn() { // <- E0501
        *p += 100
    })
}

SortMutSliceConflict(xs: mut []i32) {
    xs.Sort(fn(a: &i32, b: &i32): bool { // <- E0501
        xs[0] = 99
        return *a < *b
    })
}
