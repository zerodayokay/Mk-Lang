// verdict: reject E0503 E0505
// note: line 1 and the markers are the verdict with shallow loans, line 3 the verdict without them. Put[T: Plain] (an arena List takes no linear T, MKH-596) with T = &Cell[str]: the cell loan lands in l deep (the argument's type as passed is &Cell[str]); Put(mut names, y) lands y shallow in names, which nothing reads after; G's y into l reaches cell
// before FLD-3: reject E0503 E0505 at 27 E0503, 29 E0505 (measured by M9 track P2 on m9-item2-r 5f07967, the corpus test harness, 2026-11-15)
Put[T: Plain](l: mut List[T], x: T into l): OutOfMemory!void {
    try l.Push(x)
}

G(l: mut List[&Cell[str]], y: str into l) {
    l[0].Store(y)
}

Yes(): bool {
    return true
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var ar := Arena.New(sys.heap) else { return }
    defer ar.Free()
    cell := Cell[str].New("init")
    var names := List[str].New(&ar)
    var l := List[&Cell[str]].New(&ar)
    Put(mut names, "a") else { return }
    Put(mut l, &cell) else { return }
    if Yes() {
        var ar2 := Arena.New(sys.heap) else { return }
        defer ar2.Free() // <- E0503
        y := ar2.Dup("secret-old") else { return }
        Put(mut names, y) else { return } // <- E0505
        G(mut l, y)
    }
    v := cell.Load()
    try Print(mut out, "cell={v}\n", (v,))
}
