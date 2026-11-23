// verdict: reject E0605
// note: atomic.Spin[?&i32] holds a carrier, so SpinCore gives it LoanInterior = true and storing a local into a global Spin[?&i32] is E0605
import std.atomic

var GS: atomic.Spin[?&i32] = atomic.Spin[?&i32].New(none)

Stash() {
    var n: i32 = 5
    var g := GS.Lock()
    g.Store(some(&n)) // <- E0605
    g.Unlock()
}

export Main(sys: &System): void {
    Stash()
}
