// verdict: reject E0306
// note: Fetch at T = Empty!int returns a Late!(Empty!int); ok(ok(v)) and err(_) leave ok(err(_)) uncovered
error Late
error Empty

Fetch[T: Plain](x: &T, late: bool): Late!T {
    if late {
        fail Late
    }
    return *x
}

Show(r: &(Empty!int)): int {
    return match Fetch(r, false) { // <- E0306
        ok(ok(v)) { v }
        err(_) { 0 }
    }
}
