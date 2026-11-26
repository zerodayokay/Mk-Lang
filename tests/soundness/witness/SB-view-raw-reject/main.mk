// verdict: reject E0505
// note: a read-only view whose raw pointer points at a type other than one of its type parameters (`*Node[T]`) may reach, behind that pointer, memory its type arguments do not describe: here a `&Cell[?&i32]` its `mut self` method stores through. So V[u64] passes a store on through the loan it holds on `node`, and `short` reaches the cell. With the pointee judged as typed (mutant M-SB-viewraw) the program checks clean and `mk run` prints r= a dead local's slot. This package's pkg.mk lists `.` in unsafe_modules
import std.raw

struct Node[T] {
    v: T
    c: &Cell[?&i32]
}

view struct V[T] {
    p: *Node[T]
}

V[T].Of(n: &Node[T]): V[T] from n {
    return V[T](p: raw.Of(n))
}

V[T].Poke(mut self, x: &i32 into self) {
    p := self.p
    unsafe "witness: p points at the node Of borrowed, alive while self is" {
        (&*p).c.Store(some(x))
    }
}

Inner(node: &Node[u64]) {
    short: i32 = 2
    var v := V[u64].Of(node)
    v.Poke(&short) // <- E0505
}

Clobber(n: i32): i32 {
    var acc: i32 = n
    var w0: i32 = n + 1001
    var w1: i32 = n + 2002
    for _ in 0..4 {
        acc = acc + w0 + w1
        w0 = w0 + 1
        w1 = w1 + 1
    }
    return acc
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    cell := Cell[?&i32].New(none)
    node := Node[u64](v: 0, c: &cell)
    Inner(&node)
    k := Clobber(5)
    if r := cell.Load() {
        v := *r
        try Print(mut out, "r={v} k={k}\n", (v, k))
    }
}
