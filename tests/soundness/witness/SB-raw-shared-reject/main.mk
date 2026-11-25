// verdict: reject E0505
// note: SB-raw-holder-reject's struct reached behind a shared reference: a raw pointer counts behind `&` too, since `&self` code may store through it, so a holder of `&S` passes the store on to `s` and through `s`'s loan on `node` to the cell. With raw pointers taken to reach nothing (mutant M-SB-raw) the program checks clean. This package's pkg.mk lists `.` in unsafe_modules
import std.raw

struct Node {
    v: u64
    c: &Cell[?&i32]
}

struct S {
    p: *Node
    t: &u8
}

S.Of(n: &Node, t: &u8): S from n, t {
    return S(p: raw.Of(n), t: t)
}

S.Poke(&self, x: &i32 into self) {
    p := self.p
    unsafe "witness: p points at the node Of borrowed, alive while self is" {
        (&*p).c.Store(some(x))
    }
}

struct Holder {
    r: &S
}

Holder.Poke(mut self, x: &i32 into self) {
    self.r.Poke(x)
}

export Main(sys: &System) {
    _ = sys
    b: u8 = 0
    cell := Cell[?&i32].New(none)
    node := Node(v: 0, c: &cell)
    if true {
        short: i32 = 2
        s := S.Of(&node, &b)
        var h := Holder(r: &s)
        h.Poke(&short) // <- E0505
    }
    _ = cell.Load()
}
