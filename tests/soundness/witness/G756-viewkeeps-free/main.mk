// verdict: reject E0605
// note: G756-viewkeeps-store with its constructor free: `Of(m: &Mutex[?&i32]): V from m`, a non-generic function of the view's module, keeps m, and `Put(&self, io: Io, x: &i32 into self)` stores x into the Mutex it keeps. MakeV, source-less, returns Of(&GM), so the caller's Put(&n) reaches GM: E0605 at the Put. Accepted, `mk run` reads a dead frame. Acceptance means the hole reopened (mutant M-D756-viewkeeps-free). pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

V.Put(&self, io: Io, x: &i32 into self) {
    unsafe "probe: p was set by Of from the reference V borrows" {
        m := &*self.p
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
}

MakeV(): V {
    return Of(&GM)
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    v := MakeV()
    v.Put(io, &n) // <- E0605
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
