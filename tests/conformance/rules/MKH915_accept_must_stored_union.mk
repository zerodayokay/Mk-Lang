// verdict: ok
// rule: MKH-915
// note: .Must() on a stored E!T, not a call (a typed local, a field, an element, a deref, a receiver behind a reference, an if value, a global, a const, the E!T of ?(E!T).Must()), switches on the value as try and else do
import std.mem

error Bad

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

struct H {
    r: Bad!int
}

var G: Bad!int = Get(1)

const C: Bad!int = Get(2)

Sum(c: bool, o: ?(Bad!int)): int {
    var v: Bad!int = Get(3)
    var w: Bad!int = Get(4)
    h := mem.Zeroed[H]()
    a := mem.Zeroed[[2]H]()
    p := &v
    return v.Must() + h.r.Must() + a[1].r.Must() + (*p).Must() + p.Must() + (if c { v } else { w }).Must() + G.Must() + C.Must() + o.Must().Must()
}
