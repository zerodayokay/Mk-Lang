// verdict: reject E0505
// note: Guard[T] borrows an external Mutex mutably behind a raw pointer; its [0]mut T marker gives refSteps an exclusive step so passing Guard[?&i32] by value and storing a local into it is caught as E0505
Leak(var g: Guard[?&i32]) {
    var local: i32 = 42
    g.Store(some(&local)) // <- E0505
    g.Unlock()
}

export Main(sys: &System): void {
    m := Mutex[?&i32].New(none)
    g := m.Lock(sys.io) else {
        return
    }
    Leak(g)
}
