// verdict: reject E0602 E0605
// note: the hand-out declared only on `impl Src for &V`, through `Fetch[S: Src](s: S): &Mutex[?&i32] from s`. Fetch is refused (E0602: the interface method names no source, so its result may be the global node). implsFor and implArgs index and bind an impl whose For is &V or mut V as one for V (typewalk.go implFor), so the caller's store is E0605 too. If both are accepted, a dev build prints `before 5 after 1515870810`, a dead read. No mutant row names it, as the E0602 at Fetch stays under every cut; G756-viewkeeps-implref-get is its form that flips. pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

interface Src {
    Get(self): &Mutex[?&i32]
}

impl Src for &V {
    Get(self): &Mutex[?&i32] from self {
        unsafe "probe: p was set by Of from the reference V borrows" {
            return &*self.p
        }
    }
}

Fetch[S: Src](s: S): &Mutex[?&i32] from s {
    return s.Get() // <- E0602
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
    m := Fetch[&V](&v)
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
