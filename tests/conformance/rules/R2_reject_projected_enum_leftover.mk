// verdict: reject E0408
// rule: R2
// note: narrowing one enum field leaves an owning variant in another field to consume
linear struct Ticket {
    id: int
}

enum K {
    A(t: Ticket)
    B
}

struct Pair {
    first: K
    second: K
}

Close(t: Ticket): int {
    Ticket(id) := t
    return id
}

Seq(p: Pair): int {
    return match p {
        Pair(first:.A(t: Ticket(id: 1)), second:.B) { 0 }
        Pair(first:.B, second:.A(t: _)) { 0 } // <- E0408
        Pair(first:.B, second:.B) { 0 }
        Pair(first:.A(t), second:.A(t: u)) { Close(t) + Close(u) }
        Pair(first:.A(t), second:.B) { Close(t) }
    }
}
