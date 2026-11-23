// verdict: reject E0609
// rule: D-252
// note: storing a parameter's loan or another capture's loan into a mutably-captured carrier is E0609
WithRef(const f: fn(&i32)) {
    x: i32 = 1
    f(&x)
}

WithStr(const f: fn(str)) {
    var buf: [2]u8 = [97, 98]
    s := str.From(buf[..]) else {
        return
    }
    f(s)
}

StoreParamRef() {
    var best: ?&i32 = none
    WithRef(fn(p: &i32) {
        best = some(p) // <- E0609
    })
    _ = best
}

StoreParamStr(a: &Arena) {
    var out := List[str].New(a)
    WithStr(fn(s: str) {
        out.Push(s) else { // <- E0609
        }
    })
    _ = out.Len()
}
