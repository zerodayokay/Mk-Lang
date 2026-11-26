// verdict: reject E0507
// rule: R16
// note: a global is never borrowed `mut`, so a `mut self` method cannot be called on it: E0507 names the global (`P is a global, which is never borrowed mut`); `AtomicInt.Add` takes `&self` (EV526_accept_global_shared)
import std.atomic

struct Pair {
    a: atomic.AtomicInt
    n: int
}

Pair.Bump(mut self) {
    self.n += 1
}

var P: Pair = Pair(a: atomic.AtomicInt.New(0), n: 0)

Call() {
    P.Bump() // <- E0507
}
