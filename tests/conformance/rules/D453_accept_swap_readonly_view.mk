// verdict: ok
// rule: D-453
// note: HeapText is a read-only view (not mut view), so swapping two HeapText fields of self writes only self's first level and needs no `*self into self`
struct Buffers {
    front: HeapText
    back: HeapText
}

Buffers.Flip(mut self) {
    Swap(mut self.front, mut self.back)
}
