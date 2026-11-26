// verdict: reject E0301
// rule: R16
// note: an assignment or mut borrow of a constant cannot change its stored value
struct ConstPair {
    x: int
}

ConstPair.Set(mut self) {
    self.x = 1
}

const Pairs: [1]ConstPair = [ConstPair(x: 0)]
const Count = 1
const Maybe: ?ConstPair = some(ConstPair(x: 0))

WriteConst() {
    Count = 2 // <- E0301
    (Pairs[0].x) += 1 // <- E0301
    _ = mut Pairs[0].x // <- E0301
    _ = mut Pairs[..] // <- E0301
    Pairs[0].Set() // <- E0301
    for p in mut Pairs { // <- E0301
        p.Set()
    }
    if p := mut Maybe { // <- E0301
        p.Set()
    }
    match mut Pairs[0] { // <- E0301
        ConstPair(x) { *x = 2 }
    }
}
