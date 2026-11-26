// verdict: reject E0902
// note: a `var` global's initializer runs out of the default fuel; a global cannot state `fuel`, so the alternative names a `const` with `fuel` that the global is initialized from (R16_accept_global_from_fuel_const)
Spin(): int {
    var n := 0
    for i in 0..10_000_000 {
        n += i % 7
    }
    return n
}

var Total: int = Spin() // <- E0902
