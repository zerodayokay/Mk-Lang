// verdict: reject E0505
// note: a holder whose type reaches a Cell of a carrier through a shared reference takes loan stores below itself (R11: a step through a Cell restores writing), so the cut does not apply
view struct CV {
    c: &Cell[?&i32]
}

CV.Put(mut self, x: &i32 into self) {
    self.c.Store(some(x))
}

export Main(sys: &System) {
    _ = sys
    cell := Cell[?&i32].New(none)
    if true {
        short: i32 = 2
        var v := CV(c: &cell)
        v.Put(&short) // <- E0505
    }
    if r := cell.Load() {
        _ = *r
    }
}
