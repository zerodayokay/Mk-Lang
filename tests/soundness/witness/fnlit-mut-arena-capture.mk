// verdict: reject E0609
// note: storing a shorter-lived capture loan into a captured Arena (which outlives the inner block) must be rejected rather than dropped by allocSelf
Call(const f: fn()) {
    f()
}

StoreShortInArena(a: &Arena) {
    if true {
        short: i32 = 42
        Call(fn() {
            _ = a.Put(&short) else { // <- E0609
                return
            }
        })
    }
}
