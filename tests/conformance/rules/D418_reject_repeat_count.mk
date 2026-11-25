// verdict: reject E0705
// rule: D-418
// note: an if may be a repeat literal's count, but the count must be a compile-time constant, and this condition is not
Pick(c: bool): int {
    xs := [0; if c { 1 } else { 2 }] // <- E0705
    return xs[0]
}
