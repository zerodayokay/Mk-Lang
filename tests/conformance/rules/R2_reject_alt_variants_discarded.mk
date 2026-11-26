// verdict: reject E0402
// rule: R2
// note: an alternative that leaves a linear part is E0402 wherever it is; an alternative inside a field is taken apart after the arm's tests, only when every side has the same shape, since a later test of the arm may fail after the side matched
linear struct Ticket {
    id: int
}

linear struct S {
    a: ?Ticket
    n: int
}

linear struct Two {
    a: ?Ticket
    b: ?Ticket
}

Close(t: Ticket): int {
    Ticket(id) := t
    return id
}

OneSide(o: ?Ticket): int {
    return match o { // <- E0402
        some(Ticket(id: 1)) | some(_) { 0 }
        none { 1 }
    }
}

Neither(o: ?Ticket): int {
    return match o { // <- E0402
        some(_) | none { 0 }
    }
}

PartLeft(w: Two): int {
    return match w { // <- E0402
        Two(a: some(Ticket(id: 1)), b: some(_)) | Two(a: none, b: none) { 0 }
        Two(a: some(t), b: some(u)) { Close(t) + Close(u) }
        Two(a: some(t), b: none) { Close(t) }
        Two(a: none, b: some(u)) { Close(u) }
    }
}

InField(s: S): int {
    return match s { // <- E0402
        S(a: some(Ticket(id: _)) | none, n: 5) { 0 }
        S(a: some(_), n: m) { m }
        S(a: none, n: m) { m }
    }
}

LaterArm(o: ?Ticket): int {
    return match o { // <- E0402
        some(Ticket(id: 1)) | none { 0 }
        some(_) { 1 }
    }
}
