// verdict: reject E0505
// note: a List whose type argument holds loans takes them into its arena's storage, so a holder of a List[&i32] passes a store on; the store walk cuts only a List of non-carriers (SB-arena-list-reject)
view struct LV {
    l: mut List[&i32]
}

LV.Add(mut self, x: &i32 into self) {
    self.l.Push(x) else {}
}

export Main(sys: &System) {
    var a := Arena.New(sys.heap) else {
        return
    }
    defer a.Free()
    var l := List[&i32].New(&a)
    if true {
        short: i32 = 2
        var v := LV(l: mut l)
        v.Add(&short) // <- E0505
    }
    _ = l.Len()
}
