// verdict: ok
// rule: R16
// note: a constant can be read, borrowed shared, or copied into a mutable local
struct ConstPair {
    x: int
}

const Pairs: [1]ConstPair = [ConstPair(x: 0)]

CopyConst(): int {
    p := &Pairs
    var copy := Pairs
    copy[0].x = p[0].x + 1
    q := mut copy[0].x
    *q += 1
    return copy[0].x
}
