// verdict: reject E0409
// rule: R4
// note: an element of a temporary array is not moved out by index: the others would leak
Pair(): [2]Res {
    return [Acquire("a"), Acquire("b")]
}

First() {
    Take(Pair()[0]) // <- E0409
}
