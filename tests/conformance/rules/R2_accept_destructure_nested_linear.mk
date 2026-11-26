// verdict: ok
// rule: R2
// note: each nested linear struct is consumed by its own struct pattern, even when the bindings copy plain fields
linear struct Ticket {
    id: int
}

linear struct Empty {}

linear struct Two {
    a: Ticket
    b: Ticket
    e: Empty
}

SumTickets(w: Two): int {
    Two(a: Ticket(id), b: Ticket(id: j), e: Empty()) := w
    return id + j
}

Drive(n: int): int {
    return SumTickets(Two(a: Ticket(id: n), b: Ticket(id: 2), e: Empty()))
}
