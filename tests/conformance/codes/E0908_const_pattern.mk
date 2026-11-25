// verdict: reject E0908
// note: a const named in a match pattern is evaluated before the module's own functions are ready, so its value may not call one of them
const Volume = Cube(4)

Cube(n: int): int {
    return n * n * n
}

IsVolume(n: int): bool {
    return match n {
        Volume { true } // <- E0908
        _ { false }
    }
}
