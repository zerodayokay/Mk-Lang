// verdict: reject E0505
// note: atomic.SpinGuard[T] borrows an external Spin mutably behind a raw pointer; its [0]mut T marker gives refSteps an exclusive step so passing SpinGuard[?&i32] by value and storing a local into it is caught as E0505
import std.atomic

Leak(var g: atomic.SpinGuard[?&i32]) {
    var local: i32 = 42
    g.Store(some(&local)) // <- E0505
    g.Unlock()
}

export Main(sys: &System): void {
    _ = sys
    s := atomic.Spin[?&i32].New(none)
    g := s.Lock()
    Leak(g)
}
