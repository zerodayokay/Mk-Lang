// verdict: reject E0207
// note: an if binding always binds, so it takes a local's case rule
First(o: ?int): int {
    if X := o { // <- E0207
        return X
    }
    return 0
}
