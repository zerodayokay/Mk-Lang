// A typed E!T local keeps what is assigned to it, a call's E!T or another
// local's, and is handled later; a field holding one is made by
// mem.Zeroed and read with else. Assigning an E!T into the field is E0802.
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

export Main(sys: &System): WriteError!void {
    var c: Bad!int = Get(1)
    a := c else { 7 }
    c = Get(-1)
    b := c else { 8 }
    w: Bad!int = Get(5)
    c = w
    d := c else { 9 }
    h := mem.Zeroed[H]()
    e := h.r else { 10 }
    try sys.Print("{a} {b} {d} {e}\n", (a, b, d, e))
}
