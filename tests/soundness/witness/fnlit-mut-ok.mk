// verdict: ok
// note: a non-escaping const fn literal may mutate a captured var local during the call, and its exclusive borrow ends at the statement boundary
Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

Caller(): i32 {
    xs: [3]i32 = [1, 2, 3]
    var sum: i32 = 0
    Each(xs[..], fn(x: &i32) {
        sum += *x
    })
    sum += 1
    return sum
}
