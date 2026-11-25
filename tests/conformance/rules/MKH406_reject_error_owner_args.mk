// verdict: reject E0304
// rule: MKH-406
// note: an error takes no type parameters, so a method's prefix names none; the written one is opaque in the body and at a call, which check without an internal error
error Bad(code: int)

Bad[T].Echo(&self, x: T): T { // <- E0304
    return x
}

EchoFive(e: Bad): int {
    return e.Echo(5)
}
