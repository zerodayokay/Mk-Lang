// Why: SortStable takes scratch memory from the stack or from its explicit
// allocator and fails cleanly on OutOfMemory, so where its memory comes
// from, how much it takes, that it gives it back, and its half-slice
// fallback and OOM behavior all need tests, as do the lengths around the
// scratch's limits.
// What: tests of mem_drift.mk: the scratch's block and its free through a
// counting allocator, the half-slice fallback and OutOfMemory before any
// move, every length to 1,100 and on to 5,000, lengths whose scratch is
// only half the slice, and a less that panics mid-sort.
// How: TdCount wraps the process heap and counts; the sorts are checked by
// tests_sort.mk's TsRun (order, stability, permutation, sentinels). Helper
// names start with Td, because every file of std/core is one module.

import std.raw

// TdCount counts the calls of TdTable's entries on the allocator at inner,
// and fails every allocation when refuse is set, or one of more than limit
// bytes when limit is above 0.
struct TdCount {
    inner: *Alloc
    allocs: int
    frees: int
    bytes: int
    align: int
    free_align: int
    refuse: bool
    limit: int
}

TdCtx(c: mut TdCount): *u8 {
    unsafe "a pointer cast: TdTable's entries cast it back to the TdCount" {
        return raw.PtrCast[TdCount, u8](raw.OfMut(c))
    }
}

TdAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is the TdCount the test made, alive while it sorts, and inner its process heap's Alloc" {
        c := raw.PtrCast[u8, TdCount](ctx)
        (*c).allocs += 1
        (*c).bytes = size
        (*c).align = align
        if (*c).refuse || ((*c).limit > 0 && size > (*c).limit) {
            return raw.Null[u8]()
        }
        return AllocBytes(&*(*c).inner, size, align)
    }
}

TdResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

TdFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is the TdCount the test made, alive while it sorts, and inner its process heap's Alloc" {
        c := raw.PtrCast[u8, TdCount](ctx)
        (*c).frees += 1
        (*c).free_align = align
        FreeBytes(&*(*c).inner, p, size, align)
    }
}

const TdTable = AllocTable(alloc: TdAlloc, resize: TdResize, free: TdFree)

TdLess(a: &TsP16, b: &TsP16): bool {
    return a.k < b.k
}

// TdFill sets xs to keys below 100 (many repeats) in id order.
TdFill(xs: mut []TsP16) {
    var s: u64 = 5
    for i in 0..xs.Len() {
        xs[i] = TsP16(k: TlNext(mut s) % 100, id: i)
    }
}

// TdStable is whether xs is in key order with equal keys in id order.
TdStable(xs: []TsP16): bool {
    for i in 1..xs.Len() {
        p := &xs[i - 1]
        x := &xs[i]
        if p.k > x.k || (p.k == x.k && p.id > x.id) {
            return false
        }
    }
    return true
}

test "SortStable uses stack scratch up to 4,096 bytes, takes one block of DriftScratchLen elements from its allocator beyond that, falls back to Len() / 2, and fails OutOfMemory before moving anything" (sys: &System) {
    var c := TdCount(inner: raw.Of(sys.heap.Alloc()), allocs: 0, frees: 0, bytes: 0, align: 0, free_align: 0, refuse: false, limit: 0)
    h := MakeHeap(TdCtx(mut c), raw.Of(&TdTable))
    a := h.Alloc()
    xs := try sys.arena.Slice[TsP16](3_000)
    for n in [20, 21, 256, 257, 1_000, 3_000] {
        c.allocs = 0
        c.frees = 0
        c.bytes = 0
        TdFill(mut xs[0..n])
        try (mut xs[0..n]).SortStable(a, TdLess)
        Assert(TdStable(xs[0..n]))
        if n <= DriftInsertMax || DriftScratchLen[TsP16](n) <= DriftStackCap[TsP16]() {
            Assert(c.allocs == 0 && c.frees == 0)
        } else {
            Assert(c.allocs == 1 && c.frees == 1 && c.bytes == DriftScratchLen[TsP16](n) * 16)
        }
    }
    // When the allocator refuses DriftScratchLen, SortStable falls back to
    // Len() / 2 elements (rounded down) and frees that block before returning.
    c.allocs = 0
    c.frees = 0
    c.limit = 1_499 * 16
    TdFill(mut xs[0..2_999])
    try (mut xs[0..2_999]).SortStable(a, TdLess)
    Assert(TdStable(xs[0..2_999]) && c.frees == 1 && c.bytes == 1_499 * 16)
    // Below Len() / 2 elements, SortStable fails with OutOfMemory before
    // moving any element, leaving the slice unchanged.
    c.limit = 1_498 * 16
    c.frees = 0
    TdFill(mut xs[0..2_999])
    var refused := false
    (mut xs[0..2_999]).SortStable(a, TdLess) else {
        refused = true
    }
    var unchanged := true
    for i in 0..2_999 {
        unchanged = unchanged && xs[i].id == i
    }
    Assert(refused && unchanged && c.frees == 0)
    c.limit = 0
    // Refused altogether: fails OutOfMemory, nothing to free, slice unchanged.
    c.refuse = true
    c.allocs = 0
    c.frees = 0
    TdFill(mut xs[..])
    var oom := false
    xs.SortStable(a, TdLess) else {
        oom = true
    }
    var still := true
    for i in 0..3_000 {
        still = still && xs[i].id == i
    }
    Assert(oom && still && c.frees == 0)
}

align(64) struct TdA64 {
    k: int
    id: int
    pad: [6]u64
}

TdA64Less(a: &TdA64, b: &TdA64): bool {
    return a.k < b.k
}

TdA16Less(a: &TsA16, b: &TsA16): bool {
    return a.k < b.k
}

TdBigLess(a: &TsBig, b: &TsBig): bool {
    return a.k < b.k
}

struct TdEmpty {}

TdEmptyLess(a: &TdEmpty, b: &TdEmpty): bool {
    return false
}

test "SortStable never uses stack scratch for over-aligned or over-4,096-byte elements, passes exact alignment, handles narrow half-slice fallback, and allocates nothing for zero-size types" (sys: &System) {
    Assert(AlignOf[TdA64]() == 64 && SizeOf[TdA64]() == 64 && DriftStackCap[TdA64]() == 0)
    Assert(DriftScratchLen[TdEmpty](100) == 0 && DriftStackCap[TdEmpty]() == 0)
    var c := TdCount(inner: raw.Of(sys.heap.Alloc()), allocs: 0, frees: 0, bytes: 0, align: 0, free_align: 0, refuse: false, limit: 0)
    h := MakeHeap(TdCtx(mut c), raw.Of(&TdTable))
    a := h.Alloc()
    // At n = 20, even over-aligned TdA64 allocates nothing.
    var c20: [20]TdA64 = [TdA64(k: 0, id: 0, pad: [0; 6]); 20]
    for i in 0..20 {
        c20[i] = TdA64(k: (20 - i) % 5, id: i, pad: [0; 6])
    }
    c.refuse = true
    try c20.SortStable(a, TdA64Less)
    Assert(c.allocs == 0 && c.frees == 0)
    for i in 1..20 {
        Assert(c20[i - 1].k < c20[i].k || (c20[i - 1].k == c20[i].k && c20[i - 1].id < c20[i].id))
    }
    // Past 20 elements, align(64) skips stack scratch even when n * 64 <= 4,096:
    // refused fails OutOfMemory before any element moves; narrow half-slice fallback
    // succeeds with exact alignment 64 and frees its block.
    for n in [21, 31, 47, 48] {
        xs := try sys.arena.Slice[TdA64](n)
        for i in 0..n {
            xs[i] = TdA64(k: (n - i) % 7, id: i, pad: [0; 6])
        }
        c.allocs = 0
        c.frees = 0
        c.align = 0
        c.free_align = 0
        c.refuse = true
        c.limit = 0
        var oom := false
        xs.SortStable(a, TdA64Less) else {
            oom = true
        }
        Assert(oom && c.allocs == 2 && c.frees == 0 && c.align == 64)
        for i in 0..n {
            Assert(xs[i].id == i)
        }
        c.refuse = false
        c.allocs = 0
        c.frees = 0
        c.limit = (n / 2) * 64
        try xs.SortStable(a, TdA64Less)
        Assert(c.allocs == 2 && c.frees == 1 && c.bytes == (n / 2) * 64 && c.align == 64 && c.free_align == 64)
        for i in 1..n {
            Assert(xs[i - 1].k < xs[i].k || (xs[i - 1].k == xs[i].k && xs[i - 1].id < xs[i].id))
        }
    }
    // Same check for align(16) TsA16 at n = 21 (336 bytes <= 4,096).
    var a21: [21]TsA16 = [TsA16(k: 0, id: 0); 21]
    for i in 0..21 {
        a21[i] = TsA16(k: (21 - i) % 6, id: i)
    }
    c.refuse = false
    c.allocs = 0
    c.frees = 0
    c.align = 0
    c.free_align = 0
    c.limit = 10 * 16 - 1
    var a16_oom := false
    a21.SortStable(a, TdA16Less) else {
        a16_oom = true
    }
    Assert(a16_oom && c.allocs == 2 && c.frees == 0 && c.align == 16)
    for i in 0..21 {
        Assert(a21[i].id == i)
    }
    c.allocs = 0
    c.frees = 0
    c.limit = 10 * 16
    try a21.SortStable(a, TdA16Less)
    Assert(c.allocs == 2 && c.frees == 1 && c.bytes == 10 * 16 && c.align == 16 && c.free_align == 16)
    for i in 1..21 {
        Assert(a21[i - 1].k < a21[i].k || (a21[i - 1].k == a21[i].k && a21[i - 1].id < a21[i].id))
    }
    // Elements larger than 4,096 bytes (TsBig, 4,104 bytes) skip stack scratch at n = 21.
    big := try sys.arena.Slice[TsBig](21)
    for i in 0..21 {
        big[i].TsSet((21 - i) % 5, i)
    }
    c.allocs = 0
    c.frees = 0
    c.limit = 10 * SizeOf[TsBig]() - 1
    var big_oom := false
    big.SortStable(a, TdBigLess) else {
        big_oom = true
    }
    Assert(big_oom && c.allocs == 2 && c.frees == 0)
    for i in 0..21 {
        Assert(big[i].id == i && big[i].TsIntact())
    }
    c.allocs = 0
    c.frees = 0
    c.limit = 10 * SizeOf[TsBig]()
    try big.SortStable(a, TdBigLess)
    Assert(c.allocs == 2 && c.frees == 1 && c.bytes == 10 * SizeOf[TsBig]())
    for i in 1..21 {
        Assert(big[i].TsIntact() && (big[i - 1].k < big[i].k || (big[i - 1].k == big[i].k && big[i - 1].id < big[i].id)))
    }
    // Zero-size T allocates nothing at any length.
    c.refuse = true
    c.limit = 0
    c.allocs = 0
    c.frees = 0
    for n in [20, 21, 48, 300] {
        zs := try sys.arena.Slice[TdEmpty](n)
        try zs.SortStable(a, TdEmptyLess)
    }
    Assert(c.allocs == 0 && c.frees == 0)
}

test "the scratch is the whole slice up to 8,000,000 bytes and half of it beyond, 48 elements at least, and the stack holds 4,096 bytes of it" {
    Assert(DriftScratchLen[u64](21) == 48 && DriftScratchLen[u64](60) == 60 && DriftScratchLen[u64](1_000_000) == 1_000_000)
    Assert(DriftScratchLen[u64](1_500_000) == 1_000_000 && DriftScratchLen[u64](3_000_001) == 1_500_001)
    Assert(DriftScratchLen[TsP40](200_000) == 200_000 && DriftScratchLen[TsP40](450_001) == 225_001)
    Assert(DriftStackCap[u64]() == 512 && DriftStackCap[TsP40]() == 102 && DriftStackCap[TsA16]() == 0)
}

test "SortStable at every length to 1,100 and every 7th to 5,000, on runs and repeated keys, is stable and a permutation" (sys: &System) {
    h := sys.heap.Alloc()
    buf := try sys.arena.Slice[TsP16](5_008)
    var n := 0
    for n <= 5_000 {
        for pat in [3, 4, 5, 6] {
            try TsRun(sys.arena, h, buf, n, pat, 1, 0, 1_000)
        }
        n += if n < 1_100 { 1 } else { 7 }
    }
}

test "SortStable of 450,000 40-byte elements, whose scratch is half the slice, is stable and a permutation" (sys: &System) {
    h := sys.heap.Alloc()
    n := 450_000
    Assert(DriftScratchLen[TsP40](n) == n - n / 2)
    buf := try sys.arena.Slice[TsP40](n + 8)
    for pat in [1, 3, 4, 6] {
        try TsRun(sys.arena, h, buf, n, pat, 1, 0, 0x7FFF_FFFF_FFFF)
        try TsRun(sys.arena, h, buf, n, pat, 2, 0, 0x7FFF_FFFF_FFFF)
    }
    try TsRun(sys.arena, h, buf, n, 4, 1, 3, 0x7FFF_FFFF_FFFF)
}

test "a less that panics in the middle of SortStable ends the program with its panic [exit 101: less panics at its 5,000th call]" (sys: &System) {
    xs := try sys.arena.Slice[u64](10_000)
    var s: u64 = 7
    for i in 0..xs.Len() {
        xs[i] = Cast[u64](TlNext(mut s))
    }
    calls := Cell[int].New(0)
    try xs.SortStable(sys.heap.Alloc(), fn(a: &u64, b: &u64): bool {
        calls.Store(calls.Load() + 1)
        if calls.Load() == 5_000 {
            Panic("less panics at its 5,000th call")
        }
        return *a < *b
    })
}

// TdEvalWords sorts 150 TsWord (3,600 bytes, so the scratch is on the
// stack) stably by length, so at compile time the stack scratch holds
// values with pointers, and digests the ids.
TdEvalWords(a: &Arena): u64 {
    letters := "abcdefghi"
    var xs: [150]TsWord = [TsWord(w: "", id: 0); 150]
    var s: u64 = 13
    for i in 0..150 {
        n := 1 + TlNext(mut s) % 9
        xs[i] = TsWord(w: letters[0..n], id: i)
    }
    xs.SortStable(a.Alloc(), TsWordLess) else {
        return 0
    }
    var h: u64 = 14695981039346656037
    for i in 0..150 {
        h = (h ^ Trunc[u64](xs[i].id * 16 + xs[i].w.Len())) *% 1099511628211
    }
    return h
}

const TdEvalWordsC = TdEvalWords(EvalArena()) fuel 200_000_000

test "SortStable through the stack scratch of elements holding pointers at compile time gives the native result" (sys: &System) {
    Assert(DriftScratchLen[TsWord](150) * SizeOf[TsWord]() <= DriftStackCap[TsWord]() * SizeOf[TsWord]())
    Assert(TdEvalWordsC == TdEvalWords(sys.arena))
}

TdBigTask(h: &Heap, done: &raw.Atomic[i64]) {
    var xs: [2_000]u64 = [0; 2_000]
    var s: u64 = 9
    for i in 0..2_000 {
        xs[i] = Cast[u64](TlNext(mut s))
    }
    xs.SortStable(h.Alloc(), TsLessU64) else {
        return
    }
    var ok := true
    for i in 1..2_000 {
        ok = ok && xs[i - 1] <= xs[i]
    }
    if ok {
        _ = done.Add(1)
    }
}

// 2,000 random u64 (16 KiB on the task's stack, the scratch from the heap)
// take DriftSort's quicksort to its recursion and DriftStable's frame.
test "SortStable of 2,000 elements, through the heap's scratch, runs on a task's smallest stack (64 KiB)" (sys: &System) {
    done := raw.Atomic[i64].New(0)
    scope s := sys.io.ScopeWith(Limits(tasks: 1, stack: StackSize.Bytes(n: 65_536))) {
        s.go TdBigTask(sys.heap, &done)
    }
    Assert(done.Load() == 1)
}
