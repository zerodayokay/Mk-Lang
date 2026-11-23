// verdict: reject E0605
// note: G756-viewkeeps-result with Put moved into `impl Keep[&Mutex[?&i32]] for V` (`m into self`) and Get into `impl Src[&Mutex[?&i32]] for V` (`from self.p`), so V's own members declare nothing. viewKeepsScan reads the impl methods of the view type, so MakeV's result holds the global node: E0605 at the store. If accepted, a dev build prints `before 5 after 1515870810`, a dead read. Mutant M-D756-viewkeeps-impl, which cuts the impl scan, accepts it. pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.New(): V {
    return V(p: raw.Null[Mutex[?&i32]]())
}

interface Keep[R] {
    Put(mut self, m: R into self)
}

impl Keep[&Mutex[?&i32]] for V {
    Put(mut self, m: &Mutex[?&i32] into self) {
        self.p = raw.Of(m)
    }
}

interface Src[R] {
    Get(&self): R
}

impl Src[&Mutex[?&i32]] for V {
    Get(&self): &Mutex[?&i32] from self.p {
        unsafe "probe: p was set by Put from a reference stored into self" {
            return &*self.p
        }
    }
}

MakeV(): V {
    var v := V.New()
    v.Put(&GM)
    return v
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    var v := MakeV()
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
