// verdict: ok
// note: `*h into h` on a view holding an arena-backed List[u64] beside another borrowed piece: a List of non-carriers can write no loan into its arena's storage (its raw storage holds its type argument; its `&Alloc` is a shared step to memory that takes no store), so the arena does not receive the loan of `piece`, and `piece` can be released before the arena. Before, the walk hopped from the list to its arena and the arena held `piece` (E0504 at the defer)
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
    defer piece.Close()
    var cells := List[u64].New(&a)
    cells.Push(0) else {}
    var h := Heap(cells: mut cells, piece: mut piece)
    Through(mut h)
}
