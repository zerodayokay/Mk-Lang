// verdict: reject E0501
// rule: D-252
// note: mut captures form exclusive borrows at the call site under R13, so R5 catches conflicting borrows with the receiver or another const fn argument
Two(const f: fn(), const g: fn()) {
    f()
    g()
}

MutReceiverAndCapture(a: &Arena) {
    var xs := List[i32].New(a)
    xs.Sort(fn(p: &i32, q: &i32): bool { // <- E0501
        xs.Push(1) else {
        }
        return *p < *q
    })
}

TwoMutSame() {
    var n := 0
    Two(fn() { n += 1 }, fn() { n += 2 }) // <- E0501
    _ = n
}
