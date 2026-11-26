// verdict: reject E0208
// note: an unused snake_case pattern binding
First(x: ?int): int {
    match x {
        some(v) { // <- E0208
            return 1
        }
        none {
            return 0
        }
    }
}
