// verdict: reject E0505
// note: a struct that is no view but holds a raw pointer beside a reference holds, by its maker's signature (`from n`), a loan on what the pointer reaches; the walk cannot see behind the pointer, so the struct passes a store on through that loan and `short` reaches the cell its `mut self` method stores into. With raw pointers taken to reach nothing (mutant M-SB-raw) the program checks clean. This package's pkg.mk lists `.` in unsafe_modules
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

S.Poke(mut self, x: &i32 into self) {
    p := self.p
    unsafe "witness: p points at the node Of borrowed, alive while self is" {
        (&*p).c.Store(some(x))
    }
}

Inner(node: &Node, t: &u8) {
    short: i32 = 2
    var s := S.Of(node, t)
    s.Poke(&short) // <- E0505
}

export Main(sys: &System) {
    _ = sys
    b: u8 = 0
    cell := Cell[?&i32].New(none)
    node := Node(v: 0, c: &cell)
    Inner(&node, &b)
    _ = cell.Load()
}
