// verdict: reject E0505
// note: a struct holding &Arena passed by & reaches its allocator node on the direct p#1 -> p#2 hop (fromP1 in store.go), so storing a local via Holder.Put(&self, x into self) is caught as E0505 (mutant M-R11-fromp1)
struct Holder {
    a: &Arena
}

Put(h: &Holder, x: &i32 into h) {
    _ = h.a.Put(x) else {
        return
    }
}

Leak(h: &Holder) {
    var local: i32 = 1
    Put(h, &local) // <- E0505
}

export Main(sys: &System): void {
    h := Holder(a: sys.arena)
    Leak(&h)
}
