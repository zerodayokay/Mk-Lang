// verdict: reject E0508
// rule: MKH-406
// note: a `mut self` method of an error needs a var receiver, as for a struct
error Bad(code: int)

Bad.Bump(mut self) {
    self.code += 1
}

Once() {
    e := Bad(code: 0)
    e.Bump() // <- E0508
}
