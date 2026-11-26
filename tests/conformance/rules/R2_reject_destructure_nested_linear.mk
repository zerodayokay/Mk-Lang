// verdict: reject E0402
// rule: R2
// note: taking one nested linear struct apart does not permit discarding its sibling
linear struct Ticket {
    id: int
}

linear struct Two {
    a: Ticket
    b: Ticket
}

Drop(w: Two): int {
    Two(a: Ticket(id), b: _) := w // <- E0402
    return id
}
