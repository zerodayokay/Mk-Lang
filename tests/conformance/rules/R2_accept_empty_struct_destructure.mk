// verdict: ok
// rule: R2
// note: a destructure takes a linear struct with no fields apart with a struct pattern that names no field, at the top or nested
linear struct Empty {}

linear struct S {
    e: Empty
    n: int
}

linear struct Only {
    e: Empty
}

Close(e: Empty) {
    Empty() := e
}

Nested(s: S): int {
    S(e: Empty(), n) := s
    return n
}

Alone(o: Only) {
    Only(e: Empty()) := o
}
