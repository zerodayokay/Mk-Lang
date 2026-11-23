// verdict: reject E0605
// note: a function of V's module returns a fn
// LITERAL that stores into a V parameter; no named function declares the store, so holdStores reads the
// fn type on Mk's result (fnStoresInto on a function signature). If accepted, it
// reads a dead frame. With the fn-type scan cut
// (M-D756-viewkeeps-hold-fncarrier) it is accepted. pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

Mk(): fn(v: &V, io: Io, x: &i32 into v) {
    return fn(v: &V, io: Io, x: &i32 into v) {
        unsafe "probe: p was set by Of from the reference V borrows" {
            m := &*v.p
            var g := m.Lock(io) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    }
}

MakeV(): V {
    return V.Of(&GM)
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    v := MakeV()
    f := Mk()
    f(&v, io, &n) // <- E0605
    return 0
}

Paint(depth: int, p: &i32): i32 {
    var x: [64]i32 = [*p; 64]
    if depth == 0 {
        return x[3]
    }
    return Paint(depth - 1, &x[1]) +% x[2]
}

Read(io: Io): i32 {
    var g := GM.Lock(io) else {
        return -1
    }
    var v: i32 = -1
    if r := *g.Ref() {
        v = *r
    }
    g.Unlock()
    return v
}

export Main(sys: &System): WriteError!void {
    if Stash(sys.io, 3) == -1000 {
        return
    }
    before := Read(sys.io)
    var c: i32 = 1515870810
    _ = Paint(8, &c)
    after := Read(sys.io)
    try sys.Print("before {before} after {after}\n", (before, after))
}
