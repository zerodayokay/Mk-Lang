// verdict: ok
// rule: MKH-920
// note: a shared borrow into the error side of an E!T whose ok payload is more aligned than its errors (F2's u8 beside an int) is into the union itself, so it is returned through from r: a payload field, the whole error, nested under ok(..) and some(..)
error E
error F
error F2(code: u8)

Field(r: &(F|F2!int), d: &u8): &u8 from r, d {
    return match r {
        err(F2(code: c)) { c }
        _ { d }
    }
}

Whole(r: &(F|F2!int)): ?&(F | F2) from r {
    return match r {
        err(e) { some(e) }
        _ { none }
    }
}

UnderOk(r: &(E!(F|F2!int)), d: &u8): &u8 from r, d {
    return match r {
        ok(err(F2(code: c))) { c }
        _ { d }
    }
}

UnderSome(r: &?(F|F2!int), d: &u8): &u8 from r, d {
    return match r {
        some(err(F2(code: c))) { c }
        _ { d }
    }
}
