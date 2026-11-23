// verdict: reject E0502
// note: a plain view struct propagates LoanInterior from its type arguments, so Box[Mutex[?&i32]] is loan-interior-writable and a struct holding it beside borrowed fields collapses on &p (mutant M-D756-loan-interior)
struct Pair {
    bx: Box[Mutex[?&i32]]
    a: &i32
    b: &i32
}

Stash(p: &Pair, io: Io) *p into p {
    var g := p.bx.Ref().Lock(io) else {
        return
    }
    g.Store(some(p.b))
    g.Unlock()
}

Bad(h: &Heap, io: Io, t: &i32): OutOfMemory!i32 {
    var n: i32 = 7
    bx := try Box[Mutex[?&i32]].New(h.Alloc(), Mutex[?&i32].New(none))
    p := Pair(bx: bx, a: t, b: &n)
    Stash(&p, io)
    n = 42 // <- E0502
    r := *p.a
    Pair(bx: b2, a: _, b: _) := p
    b2.Free(h.Alloc())
    return r
}

export Main(sys: &System): void {
    x: i32 = 1
    _ = Bad(sys.heap, sys.io, &x) else { 0 }
}
