// verdict: ok
// rule: R2
// note: a by-value match compiled arm by arm whose arm narrows an enum temporary to a variant that holds nothing linear
linear struct Ticket {
    id: int
}

enum K {
    A(t: Ticket)
    B
}

Close(t: Ticket): int {
    Ticket(id) := t
    return id
}

struct Outer { k: K }

Seq(o: Outer): int {
    return match o {
        Outer(k:.A(t: Ticket(id: 1))) { 0 }
        Outer(k:.B) { 0 }
        Outer(k:.A(t)) { Close(t) }
    }
}

DriveSeq(n: int): int {
    return Seq(Outer(k: K.B)) + Seq(Outer(k: K.A(t: Ticket(id: 7))))
}
