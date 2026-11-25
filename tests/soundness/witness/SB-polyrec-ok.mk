// verdict: ok
// note: a holder of a polymorphically recursive type (G[int] reaches G[?int], G[??int], ... through its references) ends the type walk of the store cut at recur's bound, which answers as an ANY step does, so the store goes on as before. Without the bound the walk never ended and mk check hung
struct G[T] {
    next: ?&G[?T]
    v: T
}

Set(g: mut G[int], r: &G[?int] into g) {
    g.next = some(r)
}

export Main(sys: &System) {
    _ = sys
    h := G[?int](next: none, v: none)
    var g := G[int](next: none, v: 1)
    Set(mut g, &h)
    _ = g.v
}
