// verdict: reject E0605
// note: the hand-out of &GM from self is declared only on `impl Src[&Mutex[?&i32]] for &V`, reached through `Src[R].Get(self): R from self`, whose bare R the caller trusts, so the generic forwarder `Fetch[R, S: Src[R]]` is not refused. implsFor and implArgs index and bind an impl whose For is &V or mut V as one for V (typewalk.go implFor), so V hands out &GM from self and the source-less MakeV's result holds the global node: E0605 at the store. If accepted, a dev build prints `before 5 after 1515870810`, a dead read. Acceptance means the hole reopened (mutants M-D756-viewkeeps-implref, which cuts only the reference stripping, and M-D756-viewkeeps-impl). pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

interface Src[R] {
    Get(self): R from self
}

impl Src[&Mutex[?&i32]] for &V {
    Get(self): &Mutex[?&i32] from self {
        unsafe "probe: p was set by Of from the reference V borrows" {
            return &*self.p
        }
    }
}

Fetch[R, S: Src[R]](s: S): R from s {
    return s.Get()
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
    m := Fetch[&Mutex[?&i32], &V](&v)
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
