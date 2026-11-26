// verdict: reject E0507
// rule: R12
// note: both an implicit exclusive receiver and the explicit exclusive view need writable memory
FillShared(xs: []u64) {
    xs[0..1].Fill(3) // <- E0507
    (mut xs[0..1]).Fill(3) // <- E0507
}
