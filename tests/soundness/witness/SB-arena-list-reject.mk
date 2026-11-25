// verdict: reject E0504
// note: `*h into h` on a view holding an arena-backed List[u64] beside another borrowed piece. The store walk of R11 cuts at the list (a List of non-carriers writes no loan into its arena), but the walk of a stored value's own loans trusts no type (it guards against an unsafe or extern body that writes past its signature, as it reads no shallow mark, csr-sh1): it hops from the list to its arena, the arena takes `piece`'s loan, and `piece` cannot be released before the arena (E0504 at the defer). A walk that takes the cut there checks this clean, and accepts csr-sh1 too
struct Piece {
    n: int
}

Piece.Close(self) {
    _ = self
}

view struct Heap {
    cells: mut List[u64]
    piece: mut Piece
}

interface Mixer {
    Mix(mut self) *self into self
}

impl Mixer for Heap {
    Mix(mut self) *self into self {
        self.cells.ItemsMut()[0] = Trunc[u64](self.piece.n)
    }
}

Gen[S: Mixer](s: mut S) *s into s {
    s.Mix()
}

Through(h: mut Heap) *h into h {
    Gen[Heap](mut *h)
}

export Main(sys: &System) {
    var a := Arena.New(sys.heap) else {
        return
    }
    defer a.Free()
    var piece := Piece(n: 1)
    defer piece.Close() // <- E0504
    var cells := List[u64].New(&a)
    cells.Push(0) else {}
    var h := Heap(cells: mut cells, piece: mut piece)
    Through(mut h)
}
