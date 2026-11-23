// verdict: reject E0609
// note: storing a callee-local parameter loan into a mutably-captured carrier would dangle once the callee's frame returns
WithRef(const f: fn(&i32)) {
    local: i32 = 42
    f(&local)
}

Caller() {
    var best: ?&i32 = none
    WithRef(fn(p: &i32) {
        best = some(p) // <- E0609
    })
    if r := best {
        Use(r)
    }
}
