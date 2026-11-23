// verdict: reject E0605
// note: the
// depth-2 callback route: WithKeep hands a storing fn literal to its callback's parameter; no named
// function stores into a V, so only fnStoresInto over a parameter's fn type (the callback's own
// parameter) sees the store. If accepted, it reads a dead frame
// (`before 5 after 1515870810` in dev). With the fn-type scan cut
// (M-D756-viewkeeps-hold-fncarrier) it is accepted. pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

WithKeep(wio: Io, cb: fn(io: Io, k: fn(v: &V, io: Io, x: &i32 into v)): int): int {
    return cb(wio, fn(v: &V, io: Io, x: &i32 into v) {
        unsafe "probe: p was set by Of from the reference V borrows" {
            m := &*v.p
            var g := m.Lock(io) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    })
}

MakeV(): V {
    return V.Of(&GM)
}

UseKeep(io: Io, k: fn(v: &V, io: Io, x: &i32 into v)): int {
    return Stash(io, 3, k)
}

Stash(io: Io, depth: int, k: fn(v: &V, io: Io, x: &i32 into v)): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1, k)
    }
    var n: i32 = 5
    v := MakeV()
    k(&v, io, &n) // <- E0605
    return 0
}

Paint(depth: int, p: &i32): i32 {
    var x: [64]i32 = [*p; 64]
    if depth == 0 {
        return x[3]
    }
    return Paint(depth - 1, &x[1]) +% x[2]
}

// Over repeats the store's call shape (WithKeep, UseKeep, then four levels like Stash's) with the
// canary in every frame.
Over(io: Io, c: &i32): i32 {
    return Over2(io, c)
}

Over2(io: Io, c: &i32): i32 {
    return Deep(io, 3, c)
}

Deep(io: Io, depth: int, c: &i32): i32 {
    var x: [64]i32 = [*c; 64]
    if depth > 0 {
        return x[1] +% Deep(io, depth - 1, &x[2])
    }
    return x[3]
}

Read(io: Io): i32 {
    var g := GM.Lock(io) else {
        return -1
    }
    var r: i32 = -1
    if q := *g.Ref() {
        r = *q
    }
    g.Unlock()
    return r
}

export Main(sys: &System): WriteError!void {
    if WithKeep(sys.io, UseKeep) == -1000 {
        return
    }
    before := Read(sys.io)
    var c: i32 = 1515870810
    _ = Paint(8, &c)
    _ = Over(sys.io, &c)
    after := Read(sys.io)
    try sys.Print("before {before} after {after}\n", (before, after))
}
