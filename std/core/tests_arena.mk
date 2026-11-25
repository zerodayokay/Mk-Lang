// Why: Arena is the prelude's bump allocator; it must give fresh, zero-filled,
// aligned memory and hand every chunk back at Reset and Free.
// What: test blocks for Arena.New, Put, Slice, SliceAligned, Dup, Fmt, Sub,
// Alloc and Reset, and for aligned static tables made at compile time.
// How: `mk test --std=std.core` runs them with the counting heap, so a block
// that leaves heap memory live fails with exit 102; internal/stdtest's
// TestStdMkTests judges them. Helper names start with Ta, because every file
// of std/core is one module.

import std.raw

struct TaPair {
    a: int
    b: i64
}

struct TaUnit {}

test "New gives an arena that Free returns whole" (sys: &System) {
    for i in 0..100 {
        var ar := try Arena.New(sys.heap)
        defer ar.Free()
        p := try ar.Put(i)
        Assert(*p == i)
    }
}

test "Put gives fresh disjoint memory" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    a := try ar.Put(1)
    b := try ar.Put(TaPair(a: 2, b: 3))
    c := try ar.Put(u8(4))
    d := try ar.Put(5)
    *a = 10
    b.a = 20
    *c = 40
    *d = 50
    Assert(*a == 10 && b.a == 20 && b.b == 3 && *c == 40 && *d == 50)
    var sum := 0
    for i in 0..20_000 {
        p := try ar.Put(i)
        *p += 1
        sum += *p
    }
    Assert(sum == 200_010_000)
    Assert(*a == 10 && *d == 50)
}

test "Put of a zero-size value" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for _ in 0..10_000 {
        _ = try ar.Put(TaUnit())
    }
    x := try ar.Put(7)
    Assert(*x == 7)
}

test "Slice is zero-filled, also after Reset" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for round in 0..3 {
        s := try ar.Slice[int](1000)
        Assert(s.Len() == 1000)
        for x in s {
            Assert(x == 0)
        }
        for i in 0..s.Len() {
            s[i] = i + round + 1
        }
        e := try ar.Slice[u8](0)
        Assert(e.Len() == 0)
        ar.Reset()
    }
}

// A byte count past the largest int is OutOfMemory, not an overflow panic,
// and leaves the arena usable; a zero-size element takes no bytes at any
// count.
test "Slice past the largest byte count is OutOfMemory" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var failed := 0
    _ = ar.Slice[u64](1_152_921_504_606_846_976) else {
        failed += 1
    }
    _ = ar.Slice[TaPair](9_223_372_036_854_775_807 / 16 + 1) else {
        failed += 1
    }
    Assert(failed == 2)
    z := try ar.Slice[TaUnit](9_223_372_036_854_775_807)
    Assert(z.Len() == 9_223_372_036_854_775_807)
    s := try ar.Slice[u8](10)
    s[9] = 1
    Assert(s[0] == 0 && s[9] == 1)
}

test "Dup copies the bytes" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    s := try ar.Dup("h\u{e9}llo, arena")
    Assert(s == "h\u{e9}llo, arena")
    Assert(s.Len() == 13)
    e := try ar.Dup("")
    Assert(e.Len() == 0)
    var prev := try ar.Dup("0123456789abcdef0123456789abcdef")
    for _ in 0..1_000 {
        prev = try ar.Dup(prev)
    }
    Assert(prev == "0123456789abcdef0123456789abcdef")
}

test "Fmt formats into the arena" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    s := try ar.Fmt("a{x}b{y}c", (x: -5, y: "q"))
    Assert(s == "a-5bqc")
    t := try ar.Fmt("{x}{y}", (x: 1, y: true))
    Assert(t == "1true")
}

test "Sub is independent of its parent" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    x := try ar.Put(7)
    for _ in 0..10 {
        var sub := try ar.Sub()
        defer sub.Free()
        for i in 0..1000 {
            _ = try sub.Put(i)
        }
        _ = try sub.Slice[u8](70_000)
    }
    Assert(*x == 7)
}

test "Alloc is one owner for Box and HeapList" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert(raw.Same(raw.Of(ar.Alloc()), raw.Of(ar.Alloc())))
    for i in 0..10_000 {
        b := try Box[int].New(ar.Alloc(), i)
        Assert(b.Take(ar.Alloc()) == i)
    }
    var xs := HeapList[int].New(ar.Alloc())
    defer xs.Free(ar.Alloc())
    for i in 0..10_000 {
        try xs.Push(ar.Alloc(), i)
    }
    Assert(xs.Len() == 10_000 && xs[9_999] == 9_999)
}

test "Reset reuses memory and returns the rest" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for _ in 0..50 {
        for i in 0..1000 {
            p := try ar.Put(i)
            Assert(*p == i)
        }
        _ = try ar.Slice[u8](9_000)
        ar.Reset()
    }
}

// Requests over half the next chunk, reset in a loop:
// every cycle's memory is fresh and zero-filled, and Free returns it all.
test "Reset after requests over half a chunk" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for cycle in 0..20 {
        for k in 0..5 {
            s := try ar.Slice[i64](513 + k * 300)
            Assert(s[0] == 0 && s[s.Len() - 1] == 0)
            s[0] = Cast[i64](cycle)
            s[s.Len() - 1] = Cast[i64](k)
        }
        ar.Reset()
    }
}

test "Chunks grow past 1 MiB" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var total := 0
    for i in 0..3_000 {
        s := try ar.Slice[u8](1_024)
        s[0] = Cast[u8](i % 256)
        s[1_023] = Cast[u8](i % 251)
        Assert(s[0] == Cast[u8](i % 256) && s[1_023] == Cast[u8](i % 251))
        total += s.Len()
    }
    Assert(total == 3_072_000)
}

test "A request over half a chunk gets its own" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    a := try ar.Put(1)
    big := try ar.Slice[u8](5_000)
    b := try ar.Put(2)
    huge := try ar.Slice[u8](3_000_000)
    c := try ar.Put(3)
    big[4_999] = 9
    huge[2_999_999] = 8
    Assert(*a == 1 && *b == 2 && *c == 3 && big[4_999] == 9 && huge[2_999_999] == 8 && huge[0] == 0)
}

// ArenaHeld counts each chunk's heap bytes: the first chunk alone at New, a
// small value bumps in it, a large request adds a chunk of about its size,
// and Reset gives that chunk back.
test "ArenaHeld counts the heap bytes of the arena's chunks" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert(ArenaHeld(&ar) == FirstChunk)
    _ = try ar.Put(1)
    Assert(ArenaHeld(&ar) == FirstChunk)
    huge := try ar.Slice[u8](3_000_000)
    huge[0] = 1
    held := ArenaHeld(&ar)
    Assert(held >= FirstChunk + 3_000_000 && held <= FirstChunk + 3_000_000 + FirstChunk)
    ar.Reset()
    Assert(ArenaHeld(&ar) < held)
}

// The table behind Alloc(): a HeapList grown through it resizes its last
// block in place, and a LIFO Box frees its bytes back, so 100,000 Box
// round trips stay in the first chunk's bytes.
test "Alloc resizes and frees the last block" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    first := try ar.Put(0)
    for i in 0..100_000 {
        b := try Box[TaPair].New(ar.Alloc(), TaPair(a: i, b: 1))
        Assert(b.Take(ar.Alloc()).a == i)
    }
    next := try ar.Put(0)
    Assert(raw.Addr(raw.Of(&*next)) - raw.Addr(raw.Of(&*first)) == 8)
    var xs := HeapList[u8].New(ar.Alloc())
    defer xs.Free(ar.Alloc())
    for i in 0..100_000 {
        try xs.Push(ar.Alloc(), Trunc[u8](i))
    }
    Assert(xs.Len() == 100_000 && xs[99_999] == Trunc[u8](99_999))
}

// A type aligned beyond 8 sits at its alignment in an arena,
// through Put, Slice and the Alloc() table, between ordinary requests that
// leave the bump offset odd; Reset and Free give every aligned chunk back
// (the counting heap's leak check, exit 102).
align(64) struct TaLine {
    a: u64
}

// Put takes its value by copy, so a 64 KiB align(65536) value needs a
// 64 KiB-aligned frame on a test task's stack. Such a frame once could skip
// the task's guard page on x86_64 Windows (an access violation;
// tests/programs/alignframe is its test), so 65,536 is checked through the
// table here. TaBig and TaHuge are the over-a-page static data below.
align(4096) struct TaPage {
    a: u64
}

align(8192) struct TaBig {
    a: u64
}

align(65536) struct TaHuge {
    a: u64
}

TaOff[T](p: *T, n: uint): uint {
    return raw.Addr(p) & (n - 1)
}

test "Put and Slice of an align(64) type are aligned" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var sum: u64 = 0
    for i in 0..200 {
        _ = try ar.Put(u8(1))
        q := try ar.Put(TaLine(a: Trunc[u64](i)))
        Assert(TaOff(raw.Of(&*q), 64) == 0)
        s := try ar.Slice[TaLine](3)
        Assert(TaOff(raw.Of(&s[0]), 64) == 0 && TaOff(raw.Of(&s[2]), 64) == 0 && s[1].a == 0)
        s[2].a = 7
        sum += q.a + s[2].a
    }
    Assert(sum == 199 * 200 / 2 + 7 * 200)
}

test "align(4096) and the Alloc table are aligned; past MaxAlign is null" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for i in 0..4 {
        q := try ar.Put(TaPage(a: Trunc[u64](i)))
        Assert(TaOff(raw.Of(&*q), 4096) == 0 && q.a == Trunc[u64](i))
    }
    for k in 4..17 {
        al := 1 << k
        _ = try ar.Put(u8(1))
        p := AllocBytes(ar.Alloc(), 24, al)
        Assert(!raw.IsNull(p) && TaOff(p, Trunc[uint](al)) == 0)
    }
    Assert(raw.IsNull(AllocBytes(ar.Alloc(), 8, 131072)))
}

test "Reset returns aligned chunks and the arena stays usable" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for round in 0..50 {
        for i in 0..20 {
            q := try ar.Put(TaLine(a: Trunc[u64](round + i)))
            Assert(TaOff(raw.Of(&*q), 64) == 0)
            _ = try ar.Put(TaPair(a: i, b: 2))
        }
        ar.Reset()
    }
    p := try ar.Put(5)
    Assert(*p == 5)
}

// An arena at compile time serves an align(64) type through Put and Slice. Evaluation memory
// has no addresses, so the alignment cannot be observed there; a page
// source that refused it would make the evaluation return 0.
TaBuildAligned(ar: &Arena): u64 {
    s := ar.Slice[TaLine](3) else {
        return 0
    }
    q := ar.Put(TaLine(a: 5)) else {
        return 0
    }
    s[2].a = 2
    return s[1].a + s[2].a + q.a
}

const TaAlignedAtCompileTime = TaBuildAligned(EvalArena())

test "an align(64) type is served by a compile-time arena" (sys: &System) {
    Assert(TaAlignedAtCompileTime == 7)
}

// A heap that counts the calls it forwards to sys.heap: ctx is a
// TaCount in the test's frame.
struct TaCount {
    allocs: int
    frees: int
    inner: *Alloc
}

TaCountAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is a live TaCount" {
        c := raw.PtrCast[u8, TaCount](ctx)
        (*c).allocs += 1
        return AllocBytes(&*(*c).inner, size, align)
    }
}

TaCountResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is a live TaCount" {
        return ResizeBytes(&*(*raw.PtrCast[u8, TaCount](ctx)).inner, p, old, new, align)
    }
}

TaCountFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is a live TaCount" {
        c := raw.PtrCast[u8, TaCount](ctx)
        (*c).frees += 1
        FreeBytes(&*(*c).inner, p, size, align)
    }
}

const TaCountTable = AllocTable(alloc: TaCountAlloc, resize: TaCountResize, free: TaCountFree)

TaCountHeap(c: *TaCount): Heap {
    unsafe "c is the test's TaCount, live while the heap is used" {
        return MakeHeap(raw.PtrCast[TaCount, u8](c), raw.Of(&TaCountTable))
    }
}

// TaCalls is the heap calls made so far, TaAllocs the blocks live.
TaCalls(c: *TaCount): int {
    unsafe "c is the test's live TaCount" {
        return (*c).allocs
    }
}

TaAllocs(c: *TaCount): int {
    unsafe "c is the test's live TaCount" {
        return (*c).allocs - (*c).frees
    }
}

// Over-aligned requests bump in a current aligned chunk, which Reset
// keeps, so a Reset loop of align(64) Puts reaches a steady state with no
// heap call, and 1,000 requests at 16 KiB
// alignment take a few growing chunks, not 1,000 huge blocks. The counts are
// live blocks (allocs - frees), read after each round; the counting heap
// under the test (TaCount forwards to it) judges Free's leak at the end.
test "aligned requests bump in a chunk that Reset keeps" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var ar := try Arena.New(&hp)
    defer ar.Free()
    var steady := 0
    for round in 0..100 {
        for i in 0..64 {
            q := try ar.Put(TaLine(a: Trunc[u64](round * 64 + i)))
            Assert(TaOff(raw.Of(&*q), 64) == 0 && q.a == Trunc[u64](round * 64 + i))
        }
        ar.Reset()
        if round == 2 {
            steady = TaCalls(cp)
        }
    }
    Assert(TaCalls(cp) == steady)
    // Live after Reset: the first chunk and the kept aligned chunk.
    Assert(TaAllocs(cp) == 2)
    before := TaCalls(cp)
    for _ in 0..1000 {
        p := AllocBytes(ar.Alloc(), 8, 16384)
        Assert(!raw.IsNull(p) && TaOff(p, 16384) == 0)
    }
    Assert(TaCalls(cp) - before <= 25)
    // The last aligned block grows and shrinks in place, and a LIFO free
    // gives its bytes back.
    p := AllocBytes(ar.Alloc(), 64, 64)
    Assert(!raw.IsNull(p) && ResizeBytes(ar.Alloc(), p, 64, 256, 64) && ResizeBytes(ar.Alloc(), p, 256, 32, 64))
    FreeBytes(ar.Alloc(), p, 32, 64)
    p2 := AllocBytes(ar.Alloc(), 64, 64)
    Assert(raw.Same(p, p2))
}

// Reset's walk when aligned chunks sit between ordinary ones and
// the ordinary current chunk is not the first: each round puts aligned
// values before, between and after more than 8 KiB of ordinary ones (the
// first chunk holds 4096 - HdrRoom bytes, so the ordinary current chunk is a
// later one), and a 600 KiB aligned request that takes a chunk of its own
// every round, the one heap call of a round once it is steady.
// After each Reset the next ordinary and aligned Puts land in the kept
// chunks (no heap call), every value reads back, and Free leaves nothing
// live (the test heap's leak check).
test "Reset relinks the kept chunks around aligned ones" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var ar := try Arena.New(&hp)
    defer ar.Free()
    var live := 0
    for round in 0..30 {
        r0 := TaCalls(cp)
        a := try ar.Put(TaLine(a: 1))
        var sum := 0
        for i in 0..300 {
            q := try ar.Put(TaPair(a: i, b: 2))
            sum += q.a
        }
        b := try ar.Put(TaLine(a: 2))
        big := AllocBytes(ar.Alloc(), 600_000, 4096)
        Assert(!raw.IsNull(big) && TaOff(big, 4096) == 0)
        for i in 0..300 {
            q := try ar.Put(TaPair(a: i, b: 3))
            sum += q.a
        }
        c := try ar.Put(TaLine(a: 3))
        Assert(sum == 2 * 299 * 300 / 2 && a.a + b.a + c.a == 6)
        Assert(TaOff(raw.Of(&*a), 64) == 0 && TaOff(raw.Of(&*c), 64) == 0)
        // From round 2 the one heap call of a round is the own chunk: it is
        // linked below the current aligned chunk, which stays current.
        Assert(round < 2 || TaCalls(cp) - r0 == 1)
        ar.Reset()
        if round == 0 {
            live = TaAllocs(cp)
            // The first chunk, the ordinary current one and the aligned one.
            Assert(live == 3)
        }
        Assert(TaAllocs(cp) == live)
        n := TaCalls(cp)
        x := try ar.Put(TaPair(a: 7, b: 8))
        y := try ar.Put(TaLine(a: 9))
        Assert(TaCalls(cp) == n && x.a == 7 && y.a == 9 && TaOff(raw.Of(&*y), 64) == 0)
    }
}

// Arena.SliceAligned: every power of two from 1 to MaxAlign, for
// bytes, for f32 and for an align(64) type, between Puts that leave the
// bump offset odd, zero-filled, after a Reset too; an align below the
// type's own keeps the type's; n = 0; a count large enough for a chunk of
// its own; past the largest byte count is OutOfMemory. The counting heap's
// leak check (exit 102) judges Reset and Free.
test "SliceAligned starts at its alignment and is zero-filled" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    for _ in 0..3 {
        for k in 0..17 {
            al := 1 << k
            _ = try ar.Put(u8(1))
            b := try ar.SliceAligned[u8](37 + k, al)
            Assert(b.Len() == 37 + k && TaOff(raw.Data(b[..]), Trunc[uint](al)) == 0)
            for x in b {
                Assert(x == 0)
            }
            b.Fill(0xAB)
            f := try ar.SliceAligned[f32](5, al)
            Assert(TaOff(raw.Data(f[..]), Trunc[uint](IntMax(al, 4))) == 0 && f[4] == 0.0)
            l := try ar.SliceAligned[TaLine](2, al)
            Assert(TaOff(raw.Data(l[..]), Trunc[uint](IntMax(al, 64))) == 0 && l[1].a == 0)
        }
        ar.Reset()
    }
    e := try ar.SliceAligned[u64](0, 64)
    Assert(e.Len() == 0)
    big := try ar.SliceAligned[u8](3000000, 4096)
    Assert(big.Len() == 3000000 && TaOff(raw.Data(big[..]), 4096) == 0 && big[2999999] == 0)
    var failed := 0
    _ = ar.SliceAligned[u64](MaxBytes / 8 + 1, 64) else {
        failed += 1
    }
    Assert(failed == 1)
}

// TaBuildAlignedSlice is TaBuildAligned's run through SliceAligned, with
// each align at most its type's: the evaluator serves it (no address is
// read), with the same values. An align over the type's is E0901 there
// (std/core_slicealigned_test.go), since static data keeps only the
// type's alignment; TaLine, an align(64) type, is how a static table is
// aligned.
TaBuildAlignedSlice(ar: &Arena): u64 {
    s := ar.SliceAligned[u64](4, 8) else {
        return 0
    }
    t := ar.SliceAligned[u8](3, 1) else {
        return 0
    }
    l := ar.SliceAligned[TaLine](2, 16) else {
        return 0
    }
    s[3] = 5
    t[2] = 2
    return s[0] + s[3] + u64(t[2]) + u64(t[0]) + l[1].a
}

const TaAlignedSliceAtCompileTime = TaBuildAlignedSlice(EvalArena())

test "SliceAligned runs at compile time at its type's alignment" (sys: &System) {
    Assert(TaAlignedSliceAtCompileTime == 7)
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert(TaBuildAlignedSlice(&ar) == TaAlignedSliceAtCompileTime)
}

// The alignment promised for a static table: const slices
// of an align(n) element type, made in an evaluation by Slice and by
// SliceAligned at the type's alignment, sit at n in static data for n up
// to 4096 on every profile, with odd-sized byte consts between them. Over
// 4096 the dev profile did not always keep it, so nothing here goes past
// 4096.
TaStBytes(a: &Arena, n: int): []u8 from a {
    s := a.Slice[u8](n) else {
        raw.Fail(8, "TaStBytes: out of memory")
    }
    return s[..]
}

TaStLines(a: &Arena): []TaLine from a {
    s := a.Slice[TaLine](3) else {
        raw.Fail(8, "TaStLines: out of memory")
    }
    s[2].a = 5
    return s[..]
}

TaStPages(a: &Arena): []TaPage from a {
    s := a.Slice[TaPage](2) else {
        raw.Fail(8, "TaStPages: out of memory")
    }
    s[1].a = 6
    return s[..]
}

TaStPagesAligned(a: &Arena): []TaPage from a {
    s := a.SliceAligned[TaPage](2, 4096) else {
        raw.Fail(8, "TaStPagesAligned: out of memory")
    }
    s[1].a = 7
    return s[..]
}

const TaStP1 = TaStBytes(EvalArena(), 3)
const TaStL = TaStLines(EvalArena())
const TaStP2 = TaStBytes(EvalArena(), 5)
const TaStPg = TaStPages(EvalArena())
const TaStP3 = TaStBytes(EvalArena(), 7)
const TaStPgA = TaStPagesAligned(EvalArena())

test "a static table of an align(n) type made at compile time sits at n, up to 4096" (sys: &System) {
    Assert(TaStP1.Len() == 3 && TaStP2.Len() == 5 && TaStP3.Len() == 7)
    Assert(TaStL.Len() == 3 && TaStL[0].a == 0 && TaStL[2].a == 5)
    Assert(TaOff(raw.Data(TaStL), 64) == 0)
    Assert(TaStPg.Len() == 2 && TaStPg[0].a == 0 && TaStPg[1].a == 6)
    Assert(TaOff(raw.Data(TaStPg), 4096) == 0)
    Assert(TaStPgA.Len() == 2 && TaStPgA[0].a == 0 && TaStPgA[1].a == 7)
    Assert(TaOff(raw.Data(TaStPgA), 4096) == 0)
}

// Over a page too, static data sits at its alignment: x86_64 Windows places
// such data in sections of its own, in an image whose section alignment is
// the largest (dev once put TaStB and TaStH off it).
TaStBigs(a: &Arena): []TaBig from a {
    s := a.Slice[TaBig](2) else {
        raw.Fail(8, "TaStBigs: out of memory")
    }
    s[1].a = 8
    return s[..]
}

TaStHuges(a: &Arena): []TaHuge from a {
    s := a.Slice[TaHuge](2) else {
        raw.Fail(8, "TaStHuges: out of memory")
    }
    s[1].a = 9
    return s[..]
}

const TaStP4 = TaStBytes(EvalArena(), 9)
const TaStB = TaStBigs(EvalArena())
const TaStP5 = TaStBytes(EvalArena(), 11)
const TaStH = TaStHuges(EvalArena())
const TaStHv = TaHuge(a: 10)

// TaStaticAlign is the alignment a static of an align(n) type keeps: n,
// except on macOS, where dyld slides the executable by whole pages, so a
// static keeps at most the OS page (16 KiB on arm64, 4 KiB on x86_64).
TaStaticAlign(n: uint): uint {
    if Target.os != Os.Macos {
        return n
    }
    page: uint = if Target.arch == Arch.Arm64 { 16384 } else { 4096 }
    return if n < page { n } else { page }
}

test "a static table of an align(n) type over a page made at compile time sits at n" (sys: &System) {
    Assert(TaStP4.Len() == 9 && TaStP5.Len() == 11)
    Assert(TaStB.Len() == 2 && TaStB[0].a == 0 && TaStB[1].a == 8)
    Assert(TaOff(raw.Data(TaStB), TaStaticAlign(8192)) == 0)
    Assert(TaStH.Len() == 2 && TaStH[0].a == 0 && TaStH[1].a == 9)
    Assert(TaOff(raw.Data(TaStH), TaStaticAlign(65536)) == 0)
    Assert(TaStHv.a == 10 && TaOff(raw.Of(&TaStHv), TaStaticAlign(65536)) == 0)
}

// A Slice of LargeSlice bytes or more starts on a cache line (mem_arena.mk
// BumpLine): dot's two 8 KiB f32 slices (the second opens a new chunk,
// which must not leave it at 16 mod 64), indexbyte's 16 KiB, one byte over LargeSlice, then
// exactly LargeSlice (after the odd one, so the chunk pads), and 3 MB (a
// chunk of its own), between Puts that leave the bump offset odd,
// zero-filled, in every round of a Reset loop; from the third round the
// one heap call of a round is the 3 MB own chunk, and Reset keeps the first
// chunk and the current one, as for any requests. Below LargeSlice a slice
// bumps right after the bytes before it. The counting heap's
// leak check (exit 102) judges Reset and Free.
test "a Slice of 4 KiB or more starts on 64 bytes, a smaller one bumps right after the bytes before it" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var ar := try Arena.New(&hp)
    defer ar.Free()
    for round in 0..6 {
        r0 := TaCalls(cp)
        _ = try ar.Put(u8(1))
        x := try ar.Slice[f32](2048)
        y := try ar.Slice[f32](2048)
        _ = try ar.Put(u8(2))
        b := try ar.Slice[u8](16384)
        o := try ar.Slice[u8](LargeSlice + 1)
        e := try ar.Slice[u8](LargeSlice)
        _ = try ar.Put(u8(3))
        h := try ar.Slice[u8](3_000_000)
        Assert(TaOff(raw.Data(x[..]), 64) == 0 && TaOff(raw.Data(y[..]), 64) == 0 && TaOff(raw.Data(b[..]), 64) == 0)
        Assert(TaOff(raw.Data(o[..]), 64) == 0 && TaOff(raw.Data(e[..]), 64) == 0 && TaOff(raw.Data(h[..]), 64) == 0)
        Assert(x[0] == 0.0 && x[2047] == 0.0 && y[0] == 0.0 && y[2047] == 0.0 && b[16383] == 0)
        Assert(o[LargeSlice] == 0 && e[0] == 0 && e[LargeSlice - 1] == 0 && h[0] == 0 && h[2_999_999] == 0)
        x[2047] = 1.0
        y[0] = 2.0
        o[LargeSlice] = 5
        e[0] = 6
        Assert(x[2047] == 1.0 && y[0] == 2.0 && o[LargeSlice] == 5 && e[0] == 6 && b[0] == 0)
        s1 := try ar.Slice[u8](10)
        s2 := try ar.Slice[u8](1000)
        Assert(raw.Addr(raw.Data(s2[..])) == raw.Addr(raw.Data(s1[..])) + 10)
        Assert(round < 2 || TaCalls(cp) - r0 == 1)
        ar.Reset()
        Assert(round < 1 || TaAllocs(cp) == 2)
    }
}

// What a large Slice costs (BumpLine): a request over half the next chunk
// size takes an own chunk of LineAlign + size bytes, 48 more than with the
// 16-byte record at the type's alignment; the next one (dot's second 8 KiB
// slice) starts the chunk of the next size 48 bytes in, on a line. The
// chunk layout is std's, measured by ArenaHeld; no public fact.
// HdrRoom and Chunk's record put every chunk's bytes at LineSkew mod
// LineAlign, which BumpLine relies on.
test "a large Slice costs 48 bytes more than at the type's alignment" (sys: &System) {
    Assert(HdrRoom() % LineAlign == LineSkew && SizeOf[Chunk]() == LineSkew && LineBit > MaxChunk)
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    b := try ar.Slice[u8](16384)
    Assert(ArenaHeld(&ar) == FirstChunk + LineAlign + 16384 && TaOff(raw.Data(b[..]), 64) == 0)
    var dot := try Arena.New(sys.heap)
    defer dot.Free()
    x := try dot.Slice[f32](2048)
    Assert(ArenaHeld(&dot) == FirstChunk + LineAlign + 8192)
    y := try dot.Slice[f32](2048)
    Assert(ArenaHeld(&dot) == FirstChunk + LineAlign + 8192 + 2 * FirstChunk * 2)
    Assert(TaOff(raw.Data(x[..]), 64) == 0 && TaOff(raw.Data(y[..]), 64) == 0)
}

// TaFillCheck fills b with v and reports whether a, a large slice made
// before it, still holds only w: no later block overlaps a line slice.
TaFillCheck(a: []u8, w: u8, b: mut []u8, v: u8): bool {
    for i in 0..b.Len() {
        b[i] = v
    }
    for i in 0..a.Len() {
        if a[i] != w {
            return false
        }
    }
    return true
}

// The block bumped right after each large slice never overlaps it, on each
// of BumpLine's three paths: an own chunk (16 KiB in a fresh arena), a new
// chunk (8 KiB starting the next one, 48 bytes in), and the current chunk
// (4 KiB after it); NewIn arenas never take a line, so their slices sit
// right after the bytes before them.
test "the block after a large Slice does not overlap it, on every line path" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    sizes := [16384, 8192, 4096]
    for k in 0..3 {
        l := try ar.Slice[u8](sizes[k])
        for i in 0..l.Len() {
            l[i] = 0x5A
        }
        Assert(TaOff(raw.Data(l[..]), 64) == 0)
        s := try ar.Slice[u8](200)
        Assert(TaFillCheck(l[..], 0x5A, s, 0xA5))
    }
    var plain := try Arena.NewIn(sys.heap.Alloc())
    defer plain.Free()
    big := try plain.Slice[u8](16384)
    Assert(TaOff(raw.Data(big[..]), 64) == 16)
}

// A frame: an arena on another arena's Alloc(), taken and freed per call,
// nested as calls nest. Its chunks come from the arena under it and go back
// last-in first-out, so a call gives back what it took and nothing else.
// TaFrame takes a frame, fills small, large and align(4096) slices, nests
// depth more frames, and checks its own bytes after the inner ones are gone.
// Its aligned slice is two pages, or big's bytes when big is over half of
// MaxChunk: four frames' aligned chunks then fit in one current aligned
// chunk of the arena under them, or take chunks of their own there.
TaFrame(under: &Arena, depth: int, big: int): OutOfMemory!void {
    var f := try Arena.NewIn(under.Alloc())
    defer f.Free()
    small := try f.Slice[u8](100)
    large := try f.Slice[u8](big)
    var np := 2
    if big > MaxChunk / 2 {
        np = big / 4096 + 1
    }
    pages := try f.Slice[TaPage](np)
    for i in 0..small.Len() {
        small[i] = 0x11
    }
    for i in 0..large.Len() {
        large[i] = 0x22
    }
    pages[pages.Len() - 1].a = 0x33
    Assert(TaOff(raw.Of(&pages[0]), 4096) == 0)
    if depth > 0 {
        try TaFrame(under, depth - 1, big)
    }
    _ = try f.Dup("after the inner frames")
    Assert(TaFillCheck(small[..], 0x11, large, 0x22) && pages[pages.Len() - 1].a == 0x33)
}

// Frames whose large requests take chunks of their own in the arena under
// them (600,000 bytes, over half of MaxChunk, and as many align(4096)
// bytes), nested three deep: once warm, the heap blocks live and the
// arena under the frames stay the same from round to round (a request that
// size takes its chunk from the heap and returns it on every round).
test "frames on an arena's Alloc give their chunks back, large ones included" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var root := try Arena.New(&hp)
    defer root.Free()
    keep := try root.Put(TaPair(a: 1, b: 2))
    for big in [3_000, 100_000, 600_000] {
        var live := 0
        var held := 0
        for round in 0..12 {
            try TaFrame(&root, 3, big)
            if round == 3 {
                live = TaAllocs(cp)
                held = ArenaHeld(&root)
            }
        }
        Assert(TaAllocs(cp) == live && ArenaHeld(&root) == held)
    }
    Assert(keep.a == 1 && keep.b == 2)
}

// Frames freed out of order, each with a request over MaxChunk, which takes
// a chunk of its own in the arena under them: the older one's is not the newest,
// so it stays in the arena under them, and the newer frame's bytes are
// untouched; freeing the newer one then returns its two large chunks (one
// ordinary, one align(4096)), the newest. Free of the root returns the
// rest (the counting heap's leak check).
test "a frame freed before a newer one leaves the newer one's bytes alone" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var root := try Arena.New(&hp)
    defer root.Free()
    var older := try Arena.NewIn(root.Alloc())
    a := older.Slice[u8](1_100_000) else {
        raw.Fail(8, "out of memory")
    }
    var newer := Arena.NewIn(root.Alloc()) else {
        raw.Fail(8, "out of memory")
    }
    b := newer.Slice[u8](1_100_000) else {
        raw.Fail(8, "out of memory")
    }
    p := newer.Slice[TaPage](150) else {
        raw.Fail(8, "out of memory")
    }
    for i in 0..b.Len() {
        b[i] = 0x44
    }
    p[149].a = 7
    a[0] = 1
    live := TaAllocs(cp)
    older.Free()
    Assert(TaAllocs(cp) == live)
    var ok := true
    for i in 0..b.Len() {
        ok = ok && b[i] == 0x44
    }
    Assert(ok && p[149].a == 7)
    newer.Free()
    Assert(TaAllocs(cp) == live - 2)
}

// A frame's aligned chunk is a 4096-aligned block of the current aligned
// chunk under it. Here that block fills the root's 8 KiB aligned chunk to
// its end, above a slice of the root's at 32, and the root starts a new
// aligned chunk while the frame lives: the old one is right below the
// current aligned chunk, with an own chunk's shift, size and offset for
// the frame's block. Freeing the frame gives back its block only; the
// chunk stays with the slice in it.
test "a frame's aligned block that fills an aligned chunk under it leaves that chunk" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var root := try Arena.New(&hp)
    defer root.Free()
    first := try root.Put(TaLine(a: 1))
    s := try root.SliceAligned[u8](4050, 16)
    for i in 0..s.Len() {
        s[i] = 0x5a
    }
    var f := try Arena.NewIn(root.Alloc())
    inner := f.Put(TaLine(a: 2)) else {
        raw.Fail(8, "out of memory")
    }
    next := root.Put(TaLine(a: 3)) else {
        raw.Fail(8, "out of memory")
    }
    Assert(inner.a == 2)
    live := TaAllocs(cp)
    f.Free()
    Assert(TaAllocs(cp) == live)
    var ok := true
    for x in s {
        ok = ok && x == 0x5a
    }
    Assert(ok && first.a == 1 && next.a == 3)
}

// A fuzz of FreeOwnChunk, the free that returns a chunk to the source: a
// chunk returned while a block in it lives is a use after free. The root's
// source is a ledger that poisons each block it is given back and refuses
// a free of anything it did not give; every live block's bytes and every
// header's chunk list are checked after each operation.

// The ledger: each block live at the source, with its size and alignment.
// A free of anything else counts as bad and is not forwarded; a block freed
// is filled with 0xDD first, so a chunk returned with a live block in it
// shows in that block's bytes.
struct TaFzLedger {
    inner: *Alloc
    p: [1024]*u8
    size: [1024]int
    align: [1024]int
    n: int
    allocs: int
    frees: int
    bad: int
    qp: [64]*u8
    qsize: [64]int
    qalign: [64]int
    qn: int
}

// TaFzQuit gives the source the oldest quarantined block, or all of them.
TaFzQuit(l: *TaFzLedger, all: bool) {
    unsafe "l is the test's live TaFzLedger; its quarantine holds freed blocks" {
        for (*l).qn > 0 && (all || (*l).qn == 64) {
            FreeBytes(&*(*l).inner, (*l).qp[0], (*l).qsize[0], (*l).qalign[0])
            n := (*l).qn - 1
            for j in 0..n {
                (*l).qp[j] = (*l).qp[j + 1]
                (*l).qsize[j] = (*l).qsize[j + 1]
                (*l).qalign[j] = (*l).qalign[j + 1]
            }
            (*l).qn = n
        }
    }
}

// Eight poison bytes read as one word.
const TaFzPoison = -2459565876494606883

TaFzLAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is the test's live TaFzLedger" {
        l := raw.PtrCast[u8, TaFzLedger](ctx)
        p := AllocBytes(&*(*l).inner, size, align)
        if raw.IsNull(p) {
            return p
        }
        if (*l).n == 1024 {
            (*l).bad += 1000
            return p
        }
        i := (*l).n
        (*l).p[i] = p
        (*l).size[i] = size
        (*l).align[i] = align
        (*l).n = i + 1
        (*l).allocs += 1
        return p
    }
}

TaFzLResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

TaFzLFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is the test's live TaFzLedger" {
        l := raw.PtrCast[u8, TaFzLedger](ctx)
        var at := -1
        for i in 0..(*l).n {
            if raw.Same((*l).p[i], p) {
                at = i
            }
        }
        if at < 0 || (*l).size[at] != size || (*l).align[at] != align {
            (*l).bad += 1
            return
        }
        raw.Fill(p, 0xDD, size)
        n := (*l).n - 1
        (*l).p[at] = (*l).p[n]
        (*l).size[at] = (*l).size[n]
        (*l).align[at] = (*l).align[n]
        (*l).n = n
        (*l).frees += 1
        // Quarantined, so the poison stays readable until the checks after
        // this operation; the source gets it 64 frees later.
        TaFzQuit(l, false)
        q := (*l).qn
        (*l).qp[q] = p
        (*l).qsize[q] = size
        (*l).qalign[q] = align
        (*l).qn = q + 1
    }
}

const TaFzLTable = AllocTable(alloc: TaFzLAlloc, resize: TaFzLResize, free: TaFzLFree)

// TaFzHas reports whether the ledger holds p with size bytes at align.
TaFzHas(l: *TaFzLedger, p: *u8, size: int, align: int): bool {
    unsafe "l is the test's live TaFzLedger" {
        for i in 0..(*l).n {
            if raw.Same((*l).p[i], p) {
                return (*l).size[i] == size && (*l).align[i] == align
            }
        }
    }
    return false
}

// The blocks the fuzz holds: from an arena's Alloc() (typed false), or a
// typed value of the root's (Put, Slice, SliceAligned), never freed.
struct TaFzState {
    p: [160]*u8
    size: [160]int
    align: [160]int
    tag: [160]u8
    owner: [160]int
    typed: [160]bool
    from: [160]*Alloc
    n: int
    seed: u64
    ids: int
    bad: int
    ops: int
    frees: int
    rets: int
    arets: int
    ledger: *TaFzLedger
}

TaFzRand(sp: *TaFzState, n: int): int {
    unsafe "sp is the test's live state" {
        s := (*sp).seed *% 6364136223846793005 +% 1442695040888963407
        (*sp).seed = s
        return Trunc[int](s >> 33) % n
    }
}

TaFzBad(sp: *TaFzState, why: int) {
    unsafe "sp is the test's live state" {
        if (*sp).bad == 0 {
            (*sp).bad = why
        }
    }
}

TaFzOk(sp: *TaFzState): bool {
    unsafe "sp is the test's live state" {
        return (*sp).bad == 0
    }
}

// TaFzCheck samples every held block: its first and last byte and every
// 509th must still be its tag.
TaFzCheck(sp: *TaFzState) {
    if !TaFzOk(sp) {
        return
    }
    unsafe "sp is the test's live state; its blocks are live" {
        for i in 0..(*sp).n {
            p := (*sp).p[i]
            n := (*sp).size[i]
            t := (*sp).tag[i]
            var j := 0
            for j < n {
                if raw.Read(raw.Offset(p, j)) != t {
                    TaFzBad(sp, 10)
                }
                j += 509
            }
            if n > 0 && raw.Read(raw.Offset(p, n - 1)) != t {
                TaFzBad(sp, 11)
            }
        }
    }
}

// TaFzHdr checks an arena's chunk list: CurBit only on the head, never
// with OwnBit; OwnBit only on an aligned chunk; an ordinary chunk's top
// byte 0; the last chunk the first (the header right after its record);
// the current free bytes in one listed chunk; and, for the root, every
// chunk a live ledger block of its size and alignment.
TaFzHdr(sp: *TaFzState, a: &Arena, root: bool) {
    if !TaFzOk(sp) {
        return
    }
    unsafe "a's header is live" {
        h := a.hdr
        var c := (*h).chunks
        var k := 0
        var base := false
        for !raw.IsNull(c) {
            k += 1
            if k > 100000 {
                TaFzBad(sp, 20)
                return
            }
            sz := (*c).size
            // A chunk record the ledger poisoned: a chunk of the arena
            // under this one was returned with this one's chunk in it.
            if sz == TaFzPoison || raw.Read(raw.PtrCast[Chunk, int](c)) == TaFzPoison {
                TaFzBad(sp, 28)
                return
            }
            sh := ChunkShift(c)
            if (sz & CurBit) != 0 && (k != 1 || (sz & OwnBit) != 0 || sh == 0) {
                TaFzBad(sp, 21)
            }
            if (sz & OwnBit) != 0 && sh == 0 {
                TaFzBad(sp, 22)
            }
            if sh == 0 && (sz >> ShiftAt) != 0 {
                TaFzBad(sp, 23)
            }
            if raw.IsNull((*c).prev) {
                if !raw.Same(raw.Offset(raw.PtrCast[Chunk, u8](c), SizeOf[Chunk]()), raw.PtrCast[ArenaHdr, u8](h)) {
                    TaFzBad(sp, 24)
                }
            }
            if sh == 0 && IsCurrent(h, c) {
                base = true
            }
            if root {
                var al := ChunkSrc(h)
                if sh != 0 {
                    al = 1 << sh
                }
                if !TaFzHas((*sp).ledger, raw.PtrCast[Chunk, u8](c), sz & SizeMask(), al) {
                    TaFzBad(sp, 25)
                }
            }
            c = (*c).prev
        }
        if !base {
            TaFzBad(sp, 26)
        }
        if root && k != (*(*sp).ledger).n {
            TaFzBad(sp, 27)
        }
    }
}

TaFzAdd(sp: *TaFzState, p: *u8, size: int, align: int, owner: int, typed: bool, from: *Alloc) {
    unsafe "sp is the test's live state; p a fresh block of size bytes" {
        i := (*sp).n
        t := Trunc[u8]((*sp).ops % 200 + 1)
        raw.Fill(p, t, size)
        (*sp).p[i] = p
        (*sp).size[i] = size
        (*sp).align[i] = align
        (*sp).tag[i] = t
        (*sp).owner[i] = owner
        (*sp).typed[i] = typed
        (*sp).from[i] = from
        (*sp).n = i + 1
    }
}

// TaFzRemove drops entry i, keeping the order of the rest.
TaFzRemove(sp: *TaFzState, i: int) {
    unsafe "sp is the test's live state" {
        n := (*sp).n - 1
        for j in i..n {
            (*sp).p[j] = (*sp).p[j + 1]
            (*sp).size[j] = (*sp).size[j + 1]
            (*sp).align[j] = (*sp).align[j + 1]
            (*sp).tag[j] = (*sp).tag[j + 1]
            (*sp).owner[j] = (*sp).owner[j + 1]
            (*sp).typed[j] = (*sp).typed[j + 1]
            (*sp).from[j] = (*sp).from[j + 1]
        }
        (*sp).n = n
    }
}

// TaFzDrop forgets the blocks of an arena about to end.
TaFzDrop(sp: *TaFzState, owner: int) {
    unsafe "sp is the test's live state" {
        var i := 0
        for i < (*sp).n {
            if (*sp).owner[i] == owner {
                TaFzRemove(sp, i)
            } else {
                i += 1
            }
        }
    }
}

// TaFzRoom is the room left for a block at align in a's current chunk,
// or in its current aligned chunk for an align over ChunkAlign: a size that
// fills it to its end.
TaFzRoom(a: &Arena, align: int): int {
    unsafe "a's header is live" {
        h := a.hdr
        if align <= ChunkAlign {
            return (*h).size - AlignUp((*h).used, align)
        }
        c := (*h).chunks
        if !IsAlignedCur(c) || align > (1 << ChunkShift(c)) {
            return -1
        }
        ac := raw.PtrCast[Chunk, AlignedChunk](c)
        return ((*ac).size & SizeMask()) - AlignUp((*ac).used, align)
    }
}

// TaFzAlign: a third at most ChunkAlign, a third from 16 to 65536, and a
// third the alignment of a's current aligned chunk (an aligned block there
// can fill it to its end: the shape a returned chunk must not take).
TaFzAlign(sp: *TaFzState, a: &Arena): int {
    k := TaFzRand(sp, 3)
    if k == 0 {
        return 1 << TaFzRand(sp, 4)
    }
    if k == 2 {
        unsafe "a's header is live" {
            c := (*a.hdr).chunks
            if IsAlignedCur(c) {
                return 1 << ChunkShift(c)
            }
        }
    }
    return 1 << (4 + TaFzRand(sp, 13))
}

// TaFzSize: zero, small, medium, a power of two from 4 KiB to 1 MiB, the
// room left at align (a block to its chunk's end), a little more than that
// room (a new chunk with little in it), or over 500,000 bytes (a chunk of
// its own, on either side of the 512 KiB line).
TaFzSize(sp: *TaFzState, a: &Arena, align: int): int {
    cls := TaFzRand(sp, 100)
    if cls < 8 {
        return 0
    }
    if cls < 34 {
        return TaFzRand(sp, 200)
    }
    if cls < 48 {
        return TaFzRand(sp, 9000)
    }
    if cls < 58 {
        return 4096 << TaFzRand(sp, 9)
    }
    r := TaFzRoom(a, align)
    if cls < 76 {
        if r >= 0 {
            return r
        }
        return TaFzRand(sp, 5000)
    }
    if cls < 90 {
        if r >= 0 {
            return r + 1 + TaFzRand(sp, 100)
        }
        return TaFzRand(sp, 5000)
    }
    return 500000 + TaFzRand(sp, 700000)
}

// One operation on arena a (id owner): an allocation from its Alloc(), a
// free of one of its blocks (the last or any), a resize of its last block
// to its chunk's end, or, on the root, a typed value.
TaFzOp(sp: *TaFzState, a: &Arena, owner: int, isroot: bool) {
    if !TaFzOk(sp) {
        return
    }
    op := TaFzRand(sp, 100)
    unsafe "sp is the test's live state" {
        (*sp).ops += 1
        if (*sp).n >= 150 {
            // Full: free instead.
            TaFzFreeOne(sp, owner, true)
            return
        }
        if op < 42 {
            align := TaFzAlign(sp, a)
            size := TaFzSize(sp, a, align)
            if size > 1200000 {
                return
            }
            p := AllocBytes(a.Alloc(), size, align)
            if raw.IsNull(p) {
                TaFzBad(sp, 30)
                return
            }
            TaFzAdd(sp, p, size, align, owner, false, raw.Of(a.Alloc()))
            return
        }
        if op < 72 {
            TaFzFreeOne(sp, owner, TaFzRand(sp, 2) == 0)
            return
        }
        if op < 84 {
            var i := (*sp).n - 1
            for i >= 0 && ((*sp).owner[i] != owner || (*sp).typed[i]) {
                i -= 1
            }
            if i < 0 {
                return
            }
            old := (*sp).size[i]
            al := (*sp).align[i]
            if TaFzRoom(a, al) < 0 {
                return
            }
            // To its chunk's end from the block's end, and one byte either
            // side: only the last block, the one that ends where the free
            // bytes start, resizes in place.
            var room := 0
            if al <= ChunkAlign {
                h := a.hdr
                room = (*h).size - (*h).used
            } else {
                ac := raw.PtrCast[Chunk, AlignedChunk]((*a.hdr).chunks)
                room = ((*ac).size & SizeMask()) - (*ac).used
            }
            var new := old + room + TaFzRand(sp, 3) - 1
            if new < 0 {
                new = 0
            }
            p := (*sp).p[i]
            if ResizeBytes(a.Alloc(), p, old, new, al) {
                if new > old {
                    raw.Fill(raw.Offset(p, old), (*sp).tag[i], new - old)
                }
                (*sp).size[i] = new
            }
            return
        }
        if !isroot {
            return
        }
        TaFzTyped(sp, a, owner)
    }
}

// TaFzFreeOne frees a block of owner's: its last one, or any.
TaFzFreeOne(sp: *TaFzState, owner: int, last: bool) {
    unsafe "sp is the test's live state" {
        var cand := 0
        for i in 0..(*sp).n {
            if (*sp).owner[i] == owner && !(*sp).typed[i] {
                cand += 1
            }
        }
        if cand == 0 {
            return
        }
        var pick := cand - 1
        if !last {
            pick = TaFzRand(sp, cand)
        }
        for i in 0..(*sp).n {
            if (*sp).owner[i] == owner && !(*sp).typed[i] {
                if pick == 0 {
                    l := (*sp).ledger
                    f0 := (*l).frees
                    FreeBytes(&*(*sp).from[i], (*sp).p[i], (*sp).size[i], (*sp).align[i])
                    if (*l).frees > f0 {
                        (*sp).rets += 1
                        if (*sp).align[i] > ChunkAlign {
                            (*sp).arets += 1
                        }
                    }
                    (*sp).frees += 1
                    TaFzRemove(sp, i)
                    return
                }
                pick -= 1
            }
        }
    }
}

// A typed value of the root's: an align(64) Put, a SliceAligned at any
// alignment, or a Slice (a line one on a line arena when 4 KiB or more).
TaFzTyped(sp: *TaFzState, a: &Arena, owner: int) {
    k := TaFzRand(sp, 3)
    if k == 0 {
        q := a.Put(TaLine(a: 1)) else {
            TaFzBad(sp, 31)
            return
        }
        unsafe "q lives in the root until its Free" {
            TaFzAdd(sp, raw.PtrCast[TaLine, u8](raw.OfMut(q)), 64, 64, owner, true, raw.Of(a.Alloc()))
        }
        return
    }
    if k == 1 {
        al := 1 << TaFzRand(sp, 17)
        var n := TaFzRand(sp, 6000)
        r := TaFzRoom(a, al)
        if TaFzRand(sp, 2) == 0 && r >= 0 {
            n = r
        }
        s := a.SliceAligned[u8](n, al) else {
            TaFzBad(sp, 32)
            return
        }
        unsafe "s lives in the root until its Free" {
            TaFzAdd(sp, raw.DataMut(s), n, al, owner, true, raw.Of(a.Alloc()))
        }
        return
    }
    n := 4096 + TaFzRand(sp, 20000)
    s := a.Slice[u8](n) else {
        TaFzBad(sp, 33)
        return
    }
    unsafe "s lives in the root until its Free" {
        TaFzAdd(sp, raw.DataMut(s), n, 1, owner, true, raw.Of(a.Alloc()))
    }
}

TaFzNewId(sp: *TaFzState): int {
    unsafe "sp is the test's live state" {
        (*sp).ids += 1
        return (*sp).ids
    }
}

// TaFzAll checks the blocks and the root's chunk list.
TaFzAll(sp: *TaFzState, root: &Arena) {
    TaFzCheck(sp)
    TaFzHdr(sp, root, true)
}

// TaFzForget ends an arena whose chunk list a failed check found broken,
// without walking it.
TaFzForget(a: Arena) {
    Arena(hdr: p) := a
    unsafe "a failed run: the arena's chunks are left to the process" {
        raw.Forget(p)
    }
}

// A level: two frames f and g on under (the root or a frame), operations
// on the root, under, f and g in any order, deeper levels on f or g, and
// the two frames freed in either order. Every header is checked after
// each step; after a failed check nothing more runs and nothing is freed.
TaFzLevel(sp: *TaFzState, root: &Arena, under: &Arena, uid: int, level: int, steps: int) {
    var f := Arena.NewIn(under.Alloc()) else {
        TaFzBad(sp, 40)
        return
    }
    var g := Arena.NewIn(under.Alloc()) else {
        TaFzBad(sp, 41)
        f.Free()
        return
    }
    fid := TaFzNewId(sp)
    gid := TaFzNewId(sp)
    var step := 0
    for step < steps && TaFzOk(sp) {
        step += 1
        w := TaFzRand(sp, 100)
        if w < 15 {
            TaFzOp(sp, root, 0, true)
        } else if w < 35 {
            TaFzOp(sp, under, uid, uid == 0)
        } else if w < 60 {
            TaFzOp(sp, &f, fid, false)
        } else if w < 85 {
            TaFzOp(sp, &g, gid, false)
        } else if w < 93 {
            if level < 3 {
                if TaFzRand(sp, 2) == 0 {
                    TaFzLevel(sp, root, &f, fid, level + 1, steps / 2)
                } else {
                    TaFzLevel(sp, root, &g, gid, level + 1, steps / 2)
                }
            }
        } else if TaFzRand(sp, 3) == 0 {
            step = steps
        }
        TaFzAll(sp, root)
        TaFzHdr(sp, under, uid == 0)
        TaFzHdr(sp, &f, false)
        TaFzHdr(sp, &g, false)
    }
    if !TaFzOk(sp) {
        TaFzForget(f)
        TaFzForget(g)
        return
    }
    if TaFzRand(sp, 2) == 0 {
        TaFzDrop(sp, gid)
        g.Free()
        TaFzAll(sp, root)
        TaFzHdr(sp, &f, false)
        TaFzDrop(sp, fid)
        f.Free()
    } else {
        TaFzDrop(sp, fid)
        f.Free()
        TaFzAll(sp, root)
        TaFzHdr(sp, &g, false)
        TaFzDrop(sp, gid)
        g.Free()
    }
    TaFzAll(sp, root)
    TaFzHdr(sp, under, uid == 0)
}

// TaFzRounds runs rounds of levels on root, with a Reset of the root
// after some, then frees it (or, after a failed check, forgets it).
TaFzRounds(sp: *TaFzState, root: Arena, rounds: int, steps: int) {
    var r := root
    for _ in 0..rounds {
        if TaFzOk(sp) {
            TaFzLevel(sp, &r, &r, 0, 0, steps)
        }
        if TaFzOk(sp) && TaFzRand(sp, 3) == 0 {
            TaFzDrop(sp, 0)
            r.Reset()
            TaFzAll(sp, &r)
        }
    }
    TaFzDrop(sp, 0)
    if TaFzOk(sp) {
        r.Free()
    } else {
        TaFzForget(r)
    }
}

// TaFzRun runs one seed on a root over a fresh ledger: a line arena
// (Arena.New on the ledger's heap) or an arena by NewIn. It returns the
// first failure's code: 0 when every check held and the ledger is empty
// after the root's Free.
TaFzRun(sys: &System, seed: u64, line: bool, rounds: int, steps: int, stats: mut [4]int): int {
    var l := TaFzLedger(inner: raw.Of(sys.heap.Alloc()), p: [raw.Null[u8](); 1024], size: [0; 1024], align: [0; 1024], n: 0, allocs: 0, frees: 0, bad: 0, qp: [raw.Null[u8](); 64], qsize: [0; 64], qalign: [0; 64], qn: 0)
    lp := raw.OfMut(mut l)
    var st := TaFzState(p: [raw.Null[u8](); 160], size: [0; 160], align: [0; 160], tag: [0; 160], owner: [0; 160], typed: [false; 160], from: [raw.Null[Alloc](); 160], n: 0, seed: seed, ids: 0, bad: 0, ops: 0, frees: 0, rets: 0, arets: 0, ledger: lp)
    sp := raw.OfMut(mut st)
    unsafe "l and st are this frame's, live while the arenas are" {
        ctx := raw.PtrCast[TaFzLedger, u8](lp)
        var hp := MakeHeap(ctx, raw.Of(&TaFzLTable))
        la := MakeAlloc(ctx, &TaFzLTable)
        if line {
            root := Arena.New(&hp) else {
                return 1
            }
            TaFzRounds(sp, root, rounds, steps)
        } else {
            root := Arena.NewIn(&la) else {
                return 2
            }
            TaFzRounds(sp, root, rounds, steps)
        }
        TaFzQuit(lp, true)
        stats[0] += (*sp).ops
        stats[1] += (*sp).frees
        stats[2] += (*sp).rets
        stats[3] += (*sp).arets
        if (*sp).bad != 0 {
            return (*sp).bad
        }
        if (*lp).bad != 0 {
            return 50
        }
        if (*lp).n != 0 || (*lp).allocs != (*lp).frees {
            return 51
        }
    }
    return 0
}

// Seeds 1000 and 650358 (k 0 and 82) find R1 (mem_arena.mk at 24aa785e).
// Across the 96 seeds, frees through an Alloc() return ordinary and aligned
// chunks to the source, so the path under test runs.
test "fuzz: frames and Alloc() frees on an arena keep every live block" (sys: &System) {
    var fails := 0
    var stats: [4]int = [0; 4]
    for k in 0..96 {
        seed := Trunc[u64](1000 + k * 7919)
        code := TaFzRun(sys, seed, k % 2 == 0, 6, 60, mut stats)
        if code != 0 {
            fails += 1
            try sys.Print("seed {s} line {l}: code {c}\n", (s: seed, l: k % 2 == 0, c: code))
        }
    }
    Assert(fails == 0 && stats[2] - stats[3] > 100 && stats[3] > 100)
}

// R1 at align(4096): a frame's value of 254 pages (1,040,384 bytes) takes
// an own chunk in the frame, a 4096-aligned block of 1,044,480 bytes in the
// root. The root's current aligned chunk is a fresh 1 MiB one (its 512 KiB
// one filled to 256 bytes short, then a 300-byte align(16) slice at 32), so
// the block fills it from 4096 to its end: an own chunk's shift, size and
// offset. The root starts a new aligned chunk while the frame lives;
// freeing the frame gives back its block only, and the chunk stays with
// the slice in it.
test "a frame's align(4096) chunk that fills a 1 MiB aligned chunk under it leaves that chunk" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var root := try Arena.New(&hp)
    defer root.Free()
    _ = try root.SliceAligned[u8](200_000, 16)
    _ = try root.SliceAligned[u8](324_000, 16)
    s := try root.SliceAligned[u8](300, 16)
    for i in 0..s.Len() {
        s[i] = 0x5a
    }
    var f := try Arena.NewIn(root.Alloc())
    pages := f.Slice[TaPage](254) else {
        raw.Fail(8, "out of memory")
    }
    pages[253].a = 9
    next := root.Put(TaLine(a: 3)) else {
        raw.Fail(8, "out of memory")
    }
    Assert(pages[253].a == 9)
    live := TaAllocs(cp)
    f.Free()
    Assert(TaAllocs(cp) == live)
    var ok := true
    for x in s {
        ok = ok && x == 0x5a
    }
    Assert(ok && next.a == 3)
}

// Frames of align(65536) values: a small one bumps in the frame's aligned
// chunk, a 64 KiB-aligned block of the root's current aligned chunk, and a
// 983,040-byte one takes an own chunk in the frame and an own chunk
// (OwnBit) in the root, the newest; freeing the frame returns that chunk
// to the heap and rewinds the root's aligned chunk. Once warm, rounds make
// one heap allocation and one free each (the large chunk), and the root's
// own align(65536) and align(16) values keep their bytes; a root value
// taken while the last frame lives stays too.
test "frames of align(65536) values give their own chunks back to the root" (sys: &System) {
    var cnt := TaCount(allocs: 0, frees: 0, inner: raw.Of(sys.heap.Alloc()))
    cp := raw.OfMut(mut cnt)
    var hp := TaCountHeap(cp)
    var root := try Arena.New(&hp)
    defer root.Free()
    h0 := try root.Slice[TaHuge](1)
    h0[0].a = 1
    t := try root.SliceAligned[u8](100, 16)
    for i in 0..t.Len() {
        t[i] = 0x77
    }
    var live := 0
    var calls := 0
    for round in 0..8 {
        var f := try Arena.NewIn(root.Alloc())
        small := f.Slice[TaHuge](1) else {
            raw.Fail(8, "out of memory")
        }
        big := f.Slice[TaHuge](15) else {
            raw.Fail(8, "out of memory")
        }
        small[0].a = 6
        big[14].a = 5
        Assert(small[0].a == 6 && big[14].a == 5)
        before := TaAllocs(cp)
        f.Free()
        if round >= 2 {
            Assert(TaAllocs(cp) == before - 1)
        }
        if round == 2 {
            live = TaAllocs(cp)
            calls = TaCalls(cp)
        }
    }
    Assert(TaAllocs(cp) == live && TaCalls(cp) == calls + 5)
    var f := try Arena.NewIn(root.Alloc())
    big := f.Slice[TaHuge](15) else {
        raw.Fail(8, "out of memory")
    }
    big[0].a = 2
    q := root.SliceAligned[u8](64, 16) else {
        raw.Fail(8, "out of memory")
    }
    for i in 0..q.Len() {
        q[i] = 0x33
    }
    Assert(big[0].a == 2)
    f.Free()
    var ok := h0[0].a == 1
    for x in t {
        ok = ok && x == 0x77
    }
    for x in q {
        ok = ok && x == 0x33
    }
    Assert(ok)
}

// A frame at compile time: an arena on the evaluation's arena, whose
// 600,000-byte slice and 150 align(4096) pages take chunks of their own
// there (over half of MaxChunk), returned by the frame's Free
// (FreeOwnChunk's ordinary and aligned paths, in the evaluator). The
// result is the bytes the arena under the frame held while the frame
// lived, over what it held before, or -1 when Free left it holding more.
// One warm-up frame first, so the frame's first chunk has its place.
TaFrameHeld(ar: &Arena): int {
    var w := Arena.NewIn(ar.Alloc()) else {
        return -2
    }
    w.Free()
    before := ArenaHeld(ar)
    var f := Arena.NewIn(ar.Alloc()) else {
        return -3
    }
    s := f.Slice[u8](600_000) else {
        f.Free()
        return -4
    }
    p := f.Slice[TaPage](150) else {
        f.Free()
        return -5
    }
    s[599_999] = 7
    p[149].a = 9
    during := ArenaHeld(ar)
    ok := s[599_999] == 7 && p[149].a == 9
    f.Free()
    if !ok || ArenaHeld(ar) != before {
        return -1
    }
    return during - before
}

const TaFrameHeldAtCompileTime = TaFrameHeld(EvalArena()) fuel 400_000_000

test "a frame on the evaluation's arena gives its own chunks back at compile time" (sys: &System) {
    // 600,016 + 16 bytes of record, and 150 pages + 4096 in the frame,
    // one page more in the arena under it.
    want := 600_032 + 150 * 4096 + 2 * 4096
    Assert(TaFrameHeldAtCompileTime == want)
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert(TaFrameHeld(&ar) == want)
}
