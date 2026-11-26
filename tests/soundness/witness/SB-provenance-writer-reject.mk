// verdict: reject E0505
// note: the provenance cut stops only at a holder whose type can write no loan below itself; a view over a field that holds a reference still passes the store on to the parameter, so a local's loan stored through it outlives the local
struct Slot {
    r: &i32
}

view struct H {
    s: mut Slot
}

view struct W {
    s: mut Slot
}

W.Set(mut self, x: &i32 into self) {
    self.s.r = x
}

Through(h: mut H) {
    short: i32 = 2
    var w := W(s: mut *h.s)
    w.Set(&short) // <- E0505
}

export Main(sys: &System) {
    _ = sys
    long: i32 = 1
    var slot := Slot(r: &long)
    var h := H(s: mut slot)
    Through(mut h)
    _ = *h.s.r
}
