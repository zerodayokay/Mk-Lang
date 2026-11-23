// verdict: reject E0609
// rule: D-252
// note: a captured `var` linear local is borrowed and cannot be moved out of inside a const fn literal
linear struct Token {
    id: int
}

Token.Close(self) {
}

Each(xs: []i32, const f: fn(&i32)) {
    for x in xs {
        f(&x)
    }
}

DropInLiteral(xs: []i32) {
    var tok := Token(id: 1)
    Each(xs, fn(x: &i32) {
        _ = x
        tok.Close() // <- E0609
    })
    tok.Close()
}
