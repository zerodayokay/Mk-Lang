// verdict: reject E0207
// note: a name in a match that names no const in scope would bind and match every value
const Limit: int = 8

Size(n: int): int {
    return match n {
        Limt { 1 } // <- E0207
        _ { 0 }
    }
}
