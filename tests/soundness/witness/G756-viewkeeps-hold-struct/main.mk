// verdict: reject E0605
// note: the store declared on a method of a struct H that holds V inline, `H.Put(&self, io: Io, x: &i32 into self)`; no function of V's namespace or of an impl for V stores into V. A function of V's module that declares a store into a parameter whose type is, refers to or holds V counts as a store into the view (typewalk.go holdStores): E0605 at h.Put. If accepted, a dev build prints `before 5 after 1515870810`, a dead read. Acceptance means the hole reopened (mutant M-D756-viewkeeps-hold). pkg.mk lists `.` in unsafe_modules
import std.raw

var GM: Mutex[?&i32] = Mutex[?&i32].New(none)

mut view struct V {
    p: *Mutex[?&i32]
}

V.Of(m: &Mutex[?&i32]): V from m {
    return V(p: raw.Of(m))
}

struct H {
    v: V
}

H.Put(&self, io: Io, x: &i32 into self) {
    unsafe "probe: v.p was set by Of from the reference V borrows" {
        m := &*self.v.p
        var g := m.Lock(io) else {
            return
        }
        g.Store(some(x))
        g.Unlock()
    }
}

MakeH(): H {
    return H(v: V.Of(&GM))
}

Stash(io: Io, depth: int): int {
    if depth > 0 {
        return depth - Stash(io, depth - 1)
    }
    var n: i32 = 5
    h := MakeH()
    h.Put(io, &n) // <- E0605
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
