// verdict: ok
// note: a local view built over one field of a `mut` parameter holds the parameter's own loan by provenance (R12), but nothing its type can write below its own bytes holds a loan, so `*self into self` on it stores nothing into the parameter. Before, the walk hopped from the view through that loan and demanded `h: mut H into h` (E0604), which then locked every caller (E0501, E0504)
struct Inner {
    n: int
}

view struct H {
    a: mut Inner
}

view struct D {
    a: mut Inner
}

D.Mix(mut self) *self into self {
    self.a.n = self.a.n + 1
}

Through(h: mut H) *h into h {
    var d := D(a: mut *h.a)
    d.Mix()
}

export Main(sys: &System) {
    _ = sys
    var inner := Inner(n: 1)
    var h := H(a: mut inner)
    Through(mut h)
    Through(mut h)
    _ = h.a.n
}
