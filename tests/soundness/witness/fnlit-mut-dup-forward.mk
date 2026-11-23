// verdict: reject E0705
// note: passing the same const fn parameter twice in one call would pass its hidden mut environment twice as aliasing exclusive references
Two(const a: fn(), const b: fn()) {
    a()
    b()
}

ForwardDup(const f: fn()) {
    Two(f, f) // <- E0705
}
