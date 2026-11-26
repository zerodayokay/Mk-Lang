// verdict: reject E0402
// note: nested ok and err patterns: P(a: ok(_), r: err(_)) on a P[F!Res, G!int]. The err edge proves only r holds nothing linear; a's Res is still in the temporary. Accepted, the Res is dropped unconsumed. Mutant M-LM-whole accepts it
error F
error G

struct P[A, B] {
    a: A
    r: B
}

Drain(p: P[F!Res, G!int]): int {
    return match p { // <- E0402
        P(a: ok(_), r: err(_)) { 0 }
        P(a: ok(x), r: ok(n)) {
            x.Close()
            n
        }
        P(a: err(_), r: _) { 1 }
    }
}
