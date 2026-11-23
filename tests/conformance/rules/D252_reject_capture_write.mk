// verdict: reject E0508
// rule: D-252
// note: mutating or borrowing `mut` a captured local that is not `var` is E0508
Bump(n: mut int) {
    *n += 1
}

Sorted(xs: mut []i32): int {
    swaps := 0
    xs.Sort(fn(a: &i32, b: &i32): bool {
        swaps += 1 // <- E0508
        return *a < *b
    })
    return swaps
}

Counted(xs: mut []i32): int {
    calls := 0
    xs.Sort(fn(a: &i32, b: &i32): bool {
        Bump(mut calls) // <- E0508
        return *a < *b
    })
    return calls
}
