// verdict: reject E0605
// note: a view made by `V.Global(): V`, with no parameter, implements the sourced `Src[R].Get(&self): R from self` and hands out &GM from an unsafe block, through a generic forwarder. The impl's Get hands out &Mutex[?&i32] from self, so V.Global's result holds the global node: E0605 at the store. If accepted, a run reads a dead frame (`before 5 after 1515870810`, dev and release). Mutant M-D756-viewkeeps-impl, which cuts the impl scan, accepts it. pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Global(): V {
    return V(p: raw.Of(&GM))
}

interface Src[R] {
    Get(&self): R from self
}

impl Src[&Mutex[?&i32]] for V {
    Get(&self): &Mutex[?&i32] {
        unsafe "probe: p was set by Of from GM" {
            return &*self.p
        }
    }
}

Fwd[T, S: Src[T]](s: &S): T from s {
    return s.Get()
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    v := V.Global()
    m := Fwd[&Mutex[?&i32], V](&v)
    var g := m.Lock(io) else {
        return -2
    }
    g.Store(some(&n)) // <- E0605
    g.Unlock()
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
