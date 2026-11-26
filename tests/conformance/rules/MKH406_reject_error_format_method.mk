// verdict: reject E0206
// rule: MKH-406
// note: an error's Format is the compiler's impl method, so a Type.Name Format beside it, of any signature, is a second member of that name, as beside a struct's written impl
error Bad(code: int)

Bad.Format(&self): int { // <- E0206
    return self.code
}
