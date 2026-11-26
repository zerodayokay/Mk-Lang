// verdict: ok
// rule: MKH-922
// note: a mut binding into the error side of an E!T writes the union itself, whatever the ok payload's alignment: a payload field of a stored value, the same nested under ok(..) and some(..), the whole error replaced by another, and a field behind a mut parameter
error E
error F
error F2(code: u8)
error W(w: u64)

Field(s: mut (F|F2!int)) {
    match s {
        err(F2(code: c)) { *c = 9 }
        _ {}
    }
}

UnderOk(r: mut (E!(F|F2!int))) {
    match r {
        ok(err(F2(code: c))) { *c = 9 }
        _ {}
    }
}

UnderSome(o: mut ?(F|F2!int)) {
    match o {
        some(err(F2(code: c))) { *c = 9 }
        _ {}
    }
}

Mk(n: int): F2|W!u8 {
    if n < 0 {
        fail W(w: 5)
    }
    return 1
}

Replace(n: int): int {
    var s: F2|W!u8 = Mk(n)
    match mut s {
        err(e) { *e = F2(code: 3) }
        _ {}
    }
    return match s {
        err(F2(code: c)) { int(c) }
        err(W(w: w)) { Cast[int](w) }
        ok(v) { int(v) }
    }
}
