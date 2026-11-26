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

Seq(k: K): int {
    return match k {
        .A(t: Ticket(id: 1)) { 0 }
        .B { 0 }
        .A(t) { Close(t) }
    }
}

DriveSeq(n: int): int {
    return Seq(K.B)
}
