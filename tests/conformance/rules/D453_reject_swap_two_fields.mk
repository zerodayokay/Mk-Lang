// verdict: reject E0604
// rule: D-453
// note: Swap keeps R11's reading of `*a into b`, a store at any depth below b, so a swap of two mut view fields of self needs `*self into self` (read-only view fields such as HeapText do not: D453_accept_swap_readonly_view)
mut view struct Buf {
    n: int
}

struct Buffers {
    front: Buf
    back: Buf
}

Buffers.Flip(mut self) {
    Swap(mut self.front, mut self.back) // <- E0604
}
