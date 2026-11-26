// verdict: reject E0802
error Bad

Get(): Bad!int {
    fail Bad
}

UnusedLoan(): int {
    var r: Bad!int = Get() // <- E0802
    p := &r
    _ = p
    return 8
}

PartialRead(b: bool): int {
    var r: Bad!int = Get() // <- E0802
    p := &r
    if b {
        return *p else { 9 }
    }
    return 8
}

EitherLoan(b: bool): int {
    var r: Bad!int = Get() // <- E0802
    var s: Bad!int = Get() // <- E0802
    p := if b { &r } else { &s }
    return *p else { 10 }
}
