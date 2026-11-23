// verdict: reject E0505
// note: dereferencing &&Holder and wrapping *hh in a local struct still hops onto p#2 via untagged p.inner, which reaches p#A so storing a local into the wrapped arena is caught as E0505 (mutant M-R11-fromp1)
struct Holder {
    a: &Arena
}

struct Boxed {
    h: &Holder
}

PutBox(b: &Boxed, p: &i32 into b) {
    _ = b.h.a.Put(p) else {
        return
    }
}

Leak(hh: &&Holder) {
    var local: i32 = 42
    h := *hh
    b := Boxed(h: h)
    PutBox(&b, &local) // <- E0505
}

export Main(sys: &System): void {
    h := Holder(a: sys.arena)
    r := &h
    Leak(&r)
}
