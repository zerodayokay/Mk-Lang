// verdict: reject E0604
// note: in a generic body a holder whose type mentions a type parameter may write a loan below itself at some instance (T = &i32), so the store goes on through what it holds by provenance, as before
view struct H[T] {
    a: mut T
}

view struct D[T] {
    a: mut T
}

D[T].Mix(mut self) *self into self {
    _ = self
}

Through[T](h: mut H[T]) {
    var d := D[T](a: mut *h.a)
    d.Mix() // <- E0604
}

export Main(sys: &System) {
    _ = sys
    var n := 1
    var h := H[int](a: mut n)
    Through[int](mut h)
}
