// verdict: ok
// rule: R16
// note: a global cannot state `fuel`; the work that needs more runs in a `const` with `fuel`, and the global is initialized from that const
Spin(): int {
    var n := 0
    for i in 0..400_000 {
        n += i & 1
    }
    return n
}

const TotalValue: int = Spin() fuel 8_000_000

var Total: int = TotalValue

Read(): int {
    return Total
}
