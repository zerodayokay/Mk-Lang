// verdict: reject E0605
// note: G756-viewkeeps-implref-store with the impl written `impl Sink for mut V`. implsFor and implArgs index and bind an impl whose For is &V or mut V as one for V (typewalk.go implFor): E0605 at the call of Hand. If accepted, a dev build prints `before 5 after 1515870810`, a dead read. Acceptance means the hole reopened (mutant M-D756-viewkeeps-impl). pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

interface Sink {
    Put(self, io: Io, x: &i32 into self)
}

impl Sink for mut V {
    Put(self, io: Io, x: &i32 into self) {
    unsafe "probe: p was set by Of from the reference V borrows" {
            m := &*self.p
            var g := m.Lock(io) else {
                return
            }
            g.Store(some(x))
            g.Unlock()
        }
    }
}

Hand[S: Sink](s: S, io: Io, x: &i32 into s) {
    s.Put(io, x)
}

MakeV(): V {
    return V.Of(&GM)
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    var v := MakeV()
    Hand[mut V](mut v, io, &n) // <- E0605
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
