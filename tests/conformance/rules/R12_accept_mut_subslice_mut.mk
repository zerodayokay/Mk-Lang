// verdict: ok
// rule: R12
// note: an exclusive sub-slice can be formed from a mutable array or slice
FillExclusive(xs: mut []u64) {
    var local: [2]u64 = [1, 2]
    (mut local[0..1]).Fill(3)
    (mut xs[0..1]).Fill(3)
}
