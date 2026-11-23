// Why: splitting a slice into parts that are written at once (two halves,
// two elements, fixed-size chunks) needs exclusive views the loan rules
// cannot prove disjoint on their own.
//
// What: Slice.SplitAt, Slice.GetPair, Slice.Chunks and Slice.ChunksMut, with
// the iterators Chunks and ChunksMut.
//
// How: each view holds the loan its receiver held, and the unsafe block
// under each says why its views never overlap: a trusted-library invariant,
// as Arena.Put's. None allocates.

import std.raw

/// An iterator over the pieces of a shared slice, n elements each and the
/// last one shorter; made by Slice.Chunks.
export view struct Chunks[T] {
    xs: []T
    pos: int
    n: int
}

/// An iterator over the writable pieces of a mut slice, n elements each and
/// the last one shorter, each handed out once; made by Slice.ChunksMut. The
/// pieces borrow the slice, not the iterator, so several may be live at
/// once, as `for c in xs.ChunksMut(n) { s.go Work(c) }` needs.
export view struct ChunksMut[T] {
    ptr: *T
    left: int
    n: int
    marker: [0]mut T
}

/// The first i elements and the rest, both mut. Panics (code 2) unless
/// 0 <= i <= Len(). O(1). The compiler cannot prove two views of one slice
/// apart; the library answers for it, and each half holds the loan the
/// receiver held.
export Slice[T].SplitAt(mut self, i: int): (left: mut []T, right: mut []T) from self {
    s := self.ItemsMut()
    n := s.Len()
    if i < 0 || i > n {
        raw.Fail(2, "Slice.SplitAt: index out of range")
    }
    p := raw.DataMut(s)
    unsafe "[0, i) and [i, n) are disjoint ranges of the n initialized elements of the mut slice self lends, so the two views never alias; both carry self's loan" {
        return (left: raw.SliceMut(p, i), right: raw.SliceMut(raw.Offset(p, i), n - i))
    }
}

/// Both elements i and j, mut; none when i == j or either is out of
/// range, never a panic. O(1). As for SplitAt, the library answers for the
/// two being apart, and each holds the receiver's loan.
export Slice[T].GetPair(mut self, i: int, j: int): ?(a: mut T, b: mut T) from self {
    s := self.ItemsMut()
    n := s.Len()
    if i == j || i < 0 || i >= n || j < 0 || j >= n {
        return none
    }
    p := raw.DataMut(s)
    unsafe "i != j and both are below n: two distinct initialized elements of the mut slice self lends, so the two references never alias; both carry self's loan" {
        return some((a: mut *raw.Offset(p, i), b: mut *raw.Offset(p, j)))
    }
}

/// The elements in pieces of n, the last one shorter; an empty slice
/// gives none. Panics (code 8) unless n > 0. O(1); each Next is O(1).
export Slice[T].Chunks(&self, n: int): Chunks[T] from self {
    if n <= 0 {
        raw.Fail(8, "Slice.Chunks: the piece length must be above 0")
    }
    return Chunks[T](xs: self.Items(), pos: 0, n: n)
}

/// The elements in mut pieces of n, the last one shorter, each handed out
/// once. Panics (code 8) unless n > 0. O(1); each Next is O(1).
export Slice[T].ChunksMut(mut self, n: int): ChunksMut[T] from self {
    if n <= 0 {
        raw.Fail(8, "Slice.ChunksMut: the piece length must be above 0")
    }
    s := self.ItemsMut()
    k := s.Len()
    return ChunksMut[T](ptr: raw.DataMut(s), left: k, n: n, marker: [])
}

/// The next piece, or none after the last. O(1); allocates nothing.
export Chunks[T].Next(mut self): ?[]T from self.xs {
    start := self.pos
    k := self.xs.Len() - start
    if k <= 0 {
        return none
    }
    m := if k < self.n { k } else { self.n }
    self.pos = start + m
    return some(self.xs[start..start + m])
}

/// The next mut piece, or none after the last. O(1); allocates nothing.
export ChunksMut[T].Next(mut self): ?mut []T from self.ptr {
    if self.left == 0 {
        return none
    }
    m := if self.left < self.n { self.left } else { self.n }
    p := self.ptr
    unsafe "p holds left initialized elements of the slice whose mut loan this view holds; [p, p + m) is handed out once and ptr moves past it, so no two pieces overlap" {
        self.ptr = raw.Offset(p, m)
        self.left -= m
        return some(raw.SliceMut(p, m))
    }
}
