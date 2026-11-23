// verdict: reject E0505
// note: a tagged field whose type reaches &Arena alongside another reference (so allocRefs is false, reachesAlloc is true) wrapped in a local struct still reaches p#A, catching a local store as E0505 (mutant M-R11-fromp1)
struct Wrap {
    a: &Arena
    n: &i32
}

struct Outer {
    w: Wrap
}

struct LocalBox {
    w: Wrap
}

PutBox(b: &LocalBox, p: &i32 into b) {
    _ = b.w.a.Put(p) else {
        return
    }
}

Leak(h: &Outer) {
    var local: i32 = 42
    w := h.w
    b := LocalBox(w: w)
    PutBox(&b, &local) // <- E0505
}

export Main(sys: &System): void {
    x: i32 = 1
    o := Outer(w: Wrap(a: sys.arena, n: &x))
    Leak(&o)
}
