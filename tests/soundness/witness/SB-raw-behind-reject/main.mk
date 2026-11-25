// verdict: reject E0505
// note: a raw pointer behind a shared reference to a struct that holds no loan (`r: &Bare`) is still opaque to the walk: the holder's `mut self` code stores through it into `node`, on which the holder holds a loan through `t`, so the holder passes the store on and `short` reaches the cell. With such a referent taken to reach nothing (mutant M-SB-rawbehind) the program checks clean. This package's pkg.mk lists `.` in unsafe_modules
import std.raw

struct Node {
    v: u64
    c: &Cell[?&i32]
}

struct Bare {
    p: *Node
}

struct H {
    r: &Bare
    t: &u64
}

H.Poke(mut self, x: &i32 into self) {
    p := self.r.p
    unsafe "witness: p points at node, which t borrows" {
        (&*p).c.Store(some(x))
    }
}

export Main(sys: &System) {
    _ = sys
    cell := Cell[?&i32].New(none)
    node := Node(v: 0, c: &cell)
    bare := Bare(p: raw.Of(&node))
    if true {
        short: i32 = 2
        var h := H(r: &bare, t: &node.v)
        h.Poke(&short) // <- E0505
    }
    _ = cell.Load()
}
