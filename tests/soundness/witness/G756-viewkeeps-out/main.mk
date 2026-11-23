// verdict: reject E0604 E0605
// note: a view with no type argument keeps a &Mutex in raw storage and says so in its signatures (`Put(mut self, m: &Mutex[?&i32] into self)`, `Get(&self): &Mutex[?&i32] from self`). Fill, safe code, stores &GM into its out-parameter through Put; if storesFirst counted a view only through its type arguments, the caller's v would hold nothing and n's loan stored through v.Get() would reach GM. A view counts what its own signatures declare it keeps or hands out (typewalk.go viewKeeps): E0604 at Fill's Put, and E0605 at the caller's store, since V.New names no source. If accepted, a dev build prints `before 5 after 1515870810`. Acceptance means the hole reopened (mutant M-D756-viewkeeps). This package's pkg.mk lists `.` in unsafe_modules, for V.Get's unsafe block and std.raw
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.New(): V {
    return V(p: raw.Null[Mutex[?&i32]]())
}

V.Put(mut self, m: &Mutex[?&i32] into self) {
    self.p = raw.Of(m)
}

V.Get(&self): &Mutex[?&i32] from self {
    unsafe "probe: p was set by Put from a reference stored into self" {
        return &*self.p
    }
}

Fill(out: mut V) {
    out.Put(&GM) // <- E0604
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    var v := V.New()
    Fill(mut v)
    m := v.Get()
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
