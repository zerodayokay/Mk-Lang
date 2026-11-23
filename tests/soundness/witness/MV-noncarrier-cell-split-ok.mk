// verdict: ok
// note: non-carrier Cell[i32], Mutex[i32], Chan[i32] and Spin[i32] are not loan-interior-writable: a struct holding one beside borrowed fields stays split across &, and a global Mutex[i32] or Spin[i32] returned under a source is accepted (no E0602)
import std.atomic

var GM: Mutex[i32] = Mutex[i32].New(0)
var GS: atomic.Spin[i32] = atomic.Spin[i32].New(0)

struct WithCell {
    c: Cell[i32]
    a: &i32
    b: &i32
}

struct WithMutex {
    m: Mutex[i32]
    a: &i32
    b: &i32
}

struct WithSpin {
    s: atomic.Spin[i32]
    a: &i32
    b: &i32
}

BumpCell(w: &WithCell) {
    w.c.Store(w.c.Load() + 1)
}

BumpMutex(w: &WithMutex, io: Io) {
    var g := w.m.Lock(io) else {
        return
    }
    g.Store(*g.Ref() + 1)
    g.Unlock()
}

BumpSpin(w: &WithSpin) {
    var g := w.s.Lock()
    g.Store(*g.Ref() + 1)
    g.Unlock()
}

SplitNonCarrier(t: &i32, io: Io): i32 {
    var n: i32 = 7
    var wc := WithCell(c: Cell[i32].New(0), a: t, b: &n)
    BumpCell(&wc)
    n = 10
    r1 := *wc.a

    var n2: i32 = 8
    var wm := WithMutex(m: Mutex[i32].New(0), a: t, b: &n2)
    BumpMutex(&wm, io)
    n2 = 11
    r2 := *wm.a

    var n3: i32 = 9
    var ws := WithSpin(s: atomic.Spin[i32].New(0), a: t, b: &n3)
    BumpSpin(&ws)
    n3 = 12
    return r1 + r2 + *ws.a
}

PickMutex(d: &Mutex[i32]): &Mutex[i32] from d {
    _ = d
    return &GM
}

PickSpin(d: &atomic.Spin[i32]): &atomic.Spin[i32] from d {
    _ = d
    return &GS
}

export Main(sys: &System): void {
    x: i32 = 1
    _ = SplitNonCarrier(&x, sys.io)
    m := Mutex[i32].New(0)
    _ = PickMutex(&m)
    s := atomic.Spin[i32].New(0)
    _ = PickSpin(&s)
}
