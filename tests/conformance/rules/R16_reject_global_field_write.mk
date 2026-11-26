// verdict: reject E0507
// rule: R16
// note: a global is never assigned, a field included: E0507 names the global (`P.n is part of the global P`)
import std.atomic

struct Pair {
    a: atomic.AtomicInt
    n: int
}

var P: Pair = Pair(a: atomic.AtomicInt.New(0), n: 0)

Set() {
    P.n = 3 // <- E0507
}
