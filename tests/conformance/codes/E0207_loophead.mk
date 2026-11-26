// verdict: reject E0207
// note: a loop head's nested binding is a destructure's, which takes a local's case rule
Sum(rs: [](a: int, b: int)): int {
    var s := 0
    for (a, b: B) in rs { // <- E0207
        s += a + B
    }
    return s
}
