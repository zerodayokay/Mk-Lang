// verdict: reject E0207
// note: a variant written without its dot is a binding, which must be snake_case
enum Cmd {
    Move(dx: int)
    Quit
}

Steps(c: Cmd): int {
    match c {
        .Move(dx) {
            return dx
        }
        Quit { // <- E0207
            _ = Quit
            return 0
        }
    }
}
