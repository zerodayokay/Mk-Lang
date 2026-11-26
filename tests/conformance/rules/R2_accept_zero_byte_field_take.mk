// verdict: ok
// rule: R2
// note: a linear temporary can be taken apart through plain fields with zero-byte layouts
struct Marker {}

linear struct Token { empty: Marker }

struct Pair {
    first: Token
    second: Token
}

DriveToken(n: int): int {
    a := Token(empty: Marker())
    Token(empty: _) := a
    return n + 40
}

DrivePair(n: int): int {
    p := Pair(first: Token(empty: Marker()), second: Token(empty: Marker()))
    Pair(first: Token(empty: _), second: Token(empty: _)) := p
    return n + 2
}
