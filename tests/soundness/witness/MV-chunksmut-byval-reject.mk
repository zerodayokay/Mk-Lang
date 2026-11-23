// verdict: reject E0505
// note: ChunksMut[T] borrows an external mut slice behind a raw pointer; its [0]mut T marker gives refSteps an exclusive step so storing a local into a chunk yielded from a by-value ChunksMut[?&i32] is caught as E0505
PutSlice(dst: mut []?&i32, p: &i32 into dst) {
    if dst.Len() > 0 {
        dst[0] = some(p)
    }
}

Leak(var cs: ChunksMut[?&i32]) {
    var local: i32 = 42
    if c := cs.Next() {
        PutSlice(c, &local) // <- E0505
    }
}

export Main(sys: &System): void {
    _ = sys
    var buf: [2]?&i32 = [none; 2]
    s := mut buf[..]
    cs := s.ChunksMut(1)
    Leak(cs)
}
