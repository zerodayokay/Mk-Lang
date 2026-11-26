// .Must() on stored E!T values, not calls: a typed local, a field, an
// array element, a deref, a receiver behind a reference, an if and a match
// value, a global, a const, a value a fn literal reads, and a linear
// payload. Each operand is read once. With an argument, Must on a field
// that holds an error panics with the error, as Must on a fallible call
// does. It was an internal compiler error in lower (MKH-915).
import std.mem

error Bad

linear struct Ticket {
    seat: int
}

Ticket.Redeem(self): int {
    Ticket(seat) := self
    return seat
}

Get(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

Issue(n: int): Bad!Ticket {
    if n < 0 {
        fail Bad
    }
    return Ticket(seat: n)
}

struct H {
    r: Bad!int
    n: int
}

var G: Bad!int = Get(11)

const C: Bad!int = Get(12)

Apply(const f: fn(): int): int {
    return f()
}

Pick(n: mut int): int {
    *n = *n + 1
    return 1
}

Holding(n: int): H {
    var h := mem.Zeroed[H]()
    h.n = n
    return h
}

export Main(sys: &System): WriteError!void {
    var reads := 0
    var v: Bad!int = Get(4)
    var w: Bad!int = Get(5)
    h := Holding(6)
    a := mem.Zeroed[[2]H]()
    p := &w
    local := v.Must()
    field := h.r.Must() + h.n
    elem := a[Pick(mut reads)].r.Must()
    deref := (*p).Must()
    ref := p.Must()
    iff := (if Pick(mut reads) == 1 { v } else { w }).Must()
    matched := (match Pick(mut reads) {
        1 { w }
        _ { v }
    }).Must()
    glob := G.Must()
    cnst := C.Must()
    captured := Apply(fn(): int { return v.Must() })
    var t: Bad!Ticket = Issue(7)
    seat := t.Must().Redeem()
    try sys.Print("{local} {field} {elem} {deref} {ref} {iff} {matched} {glob} {cnst} {captured} {seat} {reads}\n",
        (local, field, elem, deref, ref, iff, matched, glob, cnst, captured, seat, reads))
    if sys.args.Len() > 1 {
        var bad: Bad!int = Get(-1)
        var k := mem.Zeroed[H]()
        k.r = bad
        x := k.r.Must()
        try sys.Print("{x}\n", (x))
    }
}
