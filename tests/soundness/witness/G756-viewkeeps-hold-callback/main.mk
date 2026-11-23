// verdict: reject E0605
// note: the
// depth-1 callback route: Stash takes a storing callback by value, a fn-typed PARAMETER that declares a
// store into a V, and stores a local through the source-less MakeV's view with it; Go passes a storing
// fn literal. No item's signature but Stash's declares a store into a V, so only fnStoresInto over a
// signature's parameters sees it. If accepted, it reads a dead frame
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

MakeV(): V {
    return V.Of(&GM)
}

Stash(io: Io, depth: int, f: fn(vv: &V, io: Io, x: &i32 into vv)): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1, f)
    }
    var n: i32 = 5
    v := MakeV()
    f(&v, io, &n) // <- E0605
    return 0
}

Go(gio: Io): int {
    return Stash(gio, 3, fn(vv: &V, io: Io, x: &i32 into vv) {
        unsafe "probe: p was set by Of from the reference V borrows" {
            m := &*vv.p
            var g := m.Lock(io) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    })
}

Paint(depth: int, p: &i32): i32 {
    var x: [64]i32 = [*p; 64]
    if depth == 0 {
        return x[3]
    }
    return Paint(depth - 1, &x[1]) +% x[2]
}

// Over repeats Go's call shape (Over, then four levels like Stash's) with the canary in every frame.
Over(io: Io, c: &i32): i32 {
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
    if Go(sys.io) == -1000 {
        return
    }
    before := Read(sys.io)
    var c: i32 = 1515870810
    _ = Paint(8, &c)
    _ = Over(sys.io, &c)
    after := Read(sys.io)
    try sys.Print("before {before} after {after}\n", (before, after))
}
