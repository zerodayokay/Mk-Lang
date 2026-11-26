// verdict: reject E0402
// note: a match compiled arm by arm: the first arm tests a to none, then fails b's literal test, so the second arm's tests start from a join with the path where a holds a Res. Only what both paths cover may count: the second arm's b: none edge leaves a's Res in the temporary. Accepted, the Res is dropped unconsumed. Mutant M-LM-meet, which keeps what either path covers, accepts it
linear struct S {
    a: ?Res
    b: ?int
}

Drain(s: S): int {
    return match s { // <- E0402
        S(a: none, b: some(0)) { 0 }
        S(a: some(_), b: none) { 1 }
        S(a: some(r), b: _) {
            r.Close()
            2
        }
        S(a: none, b: _) { 3 }
    }
}
