// verdict: ok
// rule: R2
// note: an arm whose pattern is an alternative consumes a linear value when each alternative alone would: the struct pattern of the side that matched takes its part apart, also when the sides differ in variant; a `_` side reached only where nothing linear is left consumes nothing and needs nothing
error Bad

linear struct Ticket {
    id: int
}

linear struct Empty {}

enum K {
    A(t: Ticket)
    B
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

Get(n: int): Bad!Ticket {
    if n < 0 {
        fail Bad
    }
    return Ticket(id: n)
}

Optional(o: ?Ticket): int {
    return match o {
        some(Ticket(id: 1)) | none { 0 }
        some(Ticket(id)) { id }
    }
}

Variant(k: K): int {
    return match k {
        .A(t: Ticket(id: 1)) | .B { 0 }
        .A(t: Ticket(id)) { id }
    }
}

Three(o: ?Ticket): int {
    return match o {
        some(Ticket(id: 1)) | some(Ticket(id: 2)) | none { 0 }
        some(t) { Close(t) }
    }
}

NoFields(e: ?Empty): int {
    return match e {
        some(Empty()) | none { 5 }
    }
}

Field(s: S): int {
    return match s {
        S(a: some(Ticket(id: 1)), n: 0) | S(a: none, n: _) { 0 }
        S(a: some(t), n) { Close(t) + n }
    }
}

Range(o: ?Ticket): int {
    return match o {
        some(Ticket(id: 1..5)) | none { 0 }
        some(Ticket(id)) { id }
    }
}

Both(w: Two): int {
    return match w {
        Two(a: some(Ticket(id: 1)), b: none) | Two(a: none, b: some(Ticket(id: 2))) | Two(a: none, b: none) { 0 }
        Two(a: some(t), b: some(u)) { Close(t) + Close(u) }
        Two(a: some(t), b: none) { Close(t) }
        Two(a: none, b: some(u)) { Close(u) }
    }
}

WildOnNone(o: ?Ticket): int {
    return match o {
        some(Ticket(id: _)) | _ { 4 }
    }
}

Call(n: int): int {
    return match Get(n) {
        ok(Ticket(id: 1)) | err(_) { 0 }
        ok(t) { Close(t) }
    }
}

Drive(n: int): int {
    a := Optional(some(Ticket(id: n))) + Optional(none) + Variant(K.A(t: Ticket(id: n))) + Variant(K.B)
    b := Three(some(Ticket(id: n))) + NoFields(some(Empty())) + Field(S(a: some(Ticket(id: n)), n: 0)) + Range(none)
    return a + b + Both(Two(a: some(Ticket(id: n)), b: none)) + Call(n) + WildOnNone(some(Ticket(id: n))) + WildOnNone(none)
}
