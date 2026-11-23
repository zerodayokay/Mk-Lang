// Why: Sort, SortStable and SortStableInPlace move elements with raw moves
// guarded by one range check each, so every length, size and threshold must
// keep the slice whole, even under an order that is none.
// What: boundary tests of core_sort.mk's raw moves (PdqSmall, PdqInsertion,
// PdqPartitionLomuto, SymSwapRange, BlkMergeInternal), of mem_drift.mk's
// (DriftSort's partition, merges and small sort) and of coll_sort.mk's
// SortStable: lengths 0 to 70 and past each threshold (8, 9, 12, 13, 16, 17,
// 20, 32, 64 and the block merge's buffer sizes), elements of 1, 2, 4, 8, 9,
// 16 and 40 bytes, owned elements, and the same sorts at compile time.
// How: sentinels that every less asserts it never sees and that must end as
// they began, an order and three relations that are none (always true, always
// false, a coin per pair of ids), and owned elements under the counting heap's
// leak check (exit 102), so a value lost or doubled fails. Helper names start
// with Ts, because every file of std/core is one module.

import std.raw

// TsElem is an element under test: a key, its input position (a 1-byte
// element carries no position: its id is its key) and, for the sentinels,
// a number 0 to 7. TsIntact is false when the bytes beside the key and id
// do not match them, so a torn move shows.
interface TsElem {
    TsSet(mut self, k: int, id: int)
    TsSetSent(mut self, j: int)
    TsKey(&self): int
    TsId(&self): int
    TsSent(&self): int
    TsIntact(&self): bool
}

// 1 byte: keys below 248, sentinels 248 to 255.
impl TsElem for u8 {
    TsSet(mut self, k: int, id: int) {
        *self = Cast[u8](k)
    }
    TsSetSent(mut self, j: int) {
        *self = Cast[u8](248 + j)
    }
    TsKey(&self): int {
        return Cast[int](*self)
    }
    TsId(&self): int {
        return Cast[int](*self)
    }
    TsSent(&self): int {
        return if *self >= 248 { Cast[int](*self) - 248 } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// 2 bytes: the key in the high byte, the id in the low one, ids below 248.
impl TsElem for u16 {
    TsSet(mut self, k: int, id: int) {
        *self = Cast[u16](k * 256 + id)
    }
    TsSetSent(mut self, j: int) {
        *self = Cast[u16](255 * 256 + 248 + j)
    }
    TsKey(&self): int {
        return Cast[int](*self >> 8)
    }
    TsId(&self): int {
        return Cast[int](*self & 255)
    }
    TsSent(&self): int {
        i := Cast[int](*self & 255)
        return if i >= 248 { i - 248 } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// 4 bytes: the key in the high 16 bits, the id in the low 16, ids below
// 65,528.
impl TsElem for u32 {
    TsSet(mut self, k: int, id: int) {
        *self = Cast[u32](k * 65536 + id)
    }
    TsSetSent(mut self, j: int) {
        *self = Cast[u32](65528 + j)
    }
    TsKey(&self): int {
        return Cast[int](*self >> 16)
    }
    TsId(&self): int {
        return Cast[int](*self & 65535)
    }
    TsSent(&self): int {
        i := Cast[int](*self & 65535)
        return if i >= 65528 { i - 65528 } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// 8 bytes: the key in the high 32 bits, the id in the low 32.
impl TsElem for u64 {
    TsSet(mut self, k: int, id: int) {
        *self = (Cast[u64](k) << 32) | Cast[u64](id)
    }
    TsSetSent(mut self, j: int) {
        *self = Cast[u64](0xFFFF_FFF8 + j)
    }
    TsKey(&self): int {
        return Cast[int](*self >> 32)
    }
    TsId(&self): int {
        return Cast[int](*self & 0xFFFF_FFFF)
    }
    TsSent(&self): int {
        i := Cast[int](*self & 0xFFFF_FFFF)
        return if i >= 0xFFFF_FFF8 { i - 0xFFFF_FFF8 } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// 9 bytes, alignment 1: a 16-bit key, a 16-bit id, a sentinel byte and
// four bytes derived from the id.
struct TsB9 {
    k0: u8
    k1: u8
    i0: u8
    i1: u8
    sent: u8
    p0: u8
    p1: u8
    p2: u8
    p3: u8
}

impl TsElem for TsB9 {
    TsSet(mut self, k: int, id: int) {
        self.k0 = Cast[u8](k / 256)
        self.k1 = Cast[u8](k % 256)
        self.i0 = Cast[u8](id / 256)
        self.i1 = Cast[u8](id % 256)
        self.sent = 0
        self.p0 = self.i1 ^ 0x5A
        self.p1 = self.i0 ^ 0xA5
        self.p2 = self.k1
        self.p3 = self.k0
    }
    TsSetSent(mut self, j: int) {
        self.TsSet(0, 0)
        self.sent = Cast[u8](1 + j)
    }
    TsKey(&self): int {
        return Cast[int](self.k0) * 256 + Cast[int](self.k1)
    }
    TsId(&self): int {
        return Cast[int](self.i0) * 256 + Cast[int](self.i1)
    }
    TsSent(&self): int {
        return Cast[int](self.sent) - 1
    }
    TsIntact(&self): bool {
        return self.p0 == (self.i1 ^ 0x5A) && self.p1 == (self.i0 ^ 0xA5) && self.p2 == self.k1 && self.p3 == self.k0
    }
}

// 16 bytes: the largest key, an id; sentinels have ids -1 to -8.
struct TsP16 {
    k: int
    id: int
}

impl TsElem for TsP16 {
    TsSet(mut self, k: int, id: int) {
        self.k = k
        self.id = id
    }
    TsSetSent(mut self, j: int) {
        self.k = 0
        self.id = -1 - j
    }
    TsKey(&self): int {
        return self.k
    }
    TsId(&self): int {
        return self.id
    }
    TsSent(&self): int {
        return if self.id < 0 { -1 - self.id } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// 40 bytes, over PdqLomutoBytes: Hoare's partitions; three words derived
// from the key and id.
struct TsP40 {
    k: int
    id: int
    a: int
    b: int
    c: int
}

impl TsElem for TsP40 {
    TsSet(mut self, k: int, id: int) {
        self.k = k
        self.id = id
        self.a = id * 3 + 1
        self.b = k
        self.c = -id
    }
    TsSetSent(mut self, j: int) {
        self.TsSet(0, -1 - j)
    }
    TsKey(&self): int {
        return self.k
    }
    TsId(&self): int {
        return self.id
    }
    TsSent(&self): int {
        return if self.id < 0 { -1 - self.id } else { -1 }
    }
    TsIntact(&self): bool {
        return self.a == self.id * 3 + 1 && self.b == self.k && self.c == -self.id
    }
}

// The relations. Each asserts that neither argument is a sentinel, so a
// sort that reads one slot outside its range fails here.
TsSee[T: TsElem](a: &T, b: &T) {
    Assert(a.TsSent() < 0 && b.TsSent() < 0)
}

TsOrd[T: TsElem](a: &T, b: &T): bool {
    TsSee(a, b)
    return a.TsKey() < b.TsKey()
}

TsTrue[T: TsElem](a: &T, b: &T): bool {
    TsSee(a, b)
    return true
}

TsFalse[T: TsElem](a: &T, b: &T): bool {
    TsSee(a, b)
    return false
}

TsCoin[T: TsElem](a: &T, b: &T): bool {
    TsSee(a, b)
    return TlCoin(a.TsId(), b.TsId(), 0x2545F4914F6CDD1D)
}

// TsSortBy sorts s by way 0 Sort, 1 SortStable from h, 2 SortStable with
// scratch capped at Len() / 2 elements (half-slice fallback), 3
// SortStableInPlace, under rel 0 TsOrd, 1 TsTrue, 2 TsFalse, 3 TsCoin.
TsSortBy[T: TsElem](s: mut []T, h: &Alloc, way: int, rel: int): OutOfMemory!void {
    if way == 0 {
        if rel == 0 {
            s.Sort(TsOrd[T])
        } else if rel == 1 {
            s.Sort(TsTrue[T])
        } else if rel == 2 {
            s.Sort(TsFalse[T])
        } else {
            s.Sort(TsCoin[T])
        }
    } else if way == 1 {
        if rel == 0 {
            try s.SortStable(h, TsOrd[T])
        } else if rel == 1 {
            try s.SortStable(h, TsTrue[T])
        } else if rel == 2 {
            try s.SortStable(h, TsFalse[T])
        } else {
            try s.SortStable(h, TsCoin[T])
        }
    } else if way == 2 {
        var c := TdCount(inner: raw.Of(h), allocs: 0, frees: 0, bytes: 0, refuse: false, limit: (s.Len() / 2) * SizeOfT[T]())
        var ctx := raw.Null[u8]()
        unsafe "a pointer cast: TdTable's entries cast it back to the TdCount" {
            ctx = raw.PtrCast[TdCount, u8](raw.OfMut(mut c))
        }
        lim := MakeHeap(ctx, raw.Of(&TdTable))
        if rel == 0 {
            try s.SortStable(lim.Alloc(), TsOrd[T])
        } else if rel == 1 {
            try s.SortStable(lim.Alloc(), TsTrue[T])
        } else if rel == 2 {
            try s.SortStable(lim.Alloc(), TsFalse[T])
        } else {
            try s.SortStable(lim.Alloc(), TsCoin[T])
        }
    } else {
        if rel == 0 {
            s.SortStableInPlace(TsOrd[T])
        } else if rel == 1 {
            s.SortStableInPlace(TsTrue[T])
        } else if rel == 2 {
            s.SortStableInPlace(TsFalse[T])
        } else {
            s.SortStableInPlace(TsCoin[T])
        }
    }
}

// TsKeyAt is the key of element i of n under pattern pat: 0 sorted, 1
// reversed, 2 all equal, 3 four distinct keys, 4 random below kmax, 5 a
// sawtooth of period 7, 6 organ pipe. Every key is below kmax for the n a
// type is run at.
TsKeyAt(pat: int, i: int, n: int, s: mut u64, kmax: int): int {
    if pat == 0 {
        return i
    }
    if pat == 1 {
        return n - i
    }
    if pat == 2 {
        return 7
    }
    if pat == 5 {
        return i % 7
    }
    if pat == 6 {
        return if i < n - i { i } else { n - i }
    }
    r := TlNext(s)
    return if pat == 3 { r % 4 } else { r % kmax }
}

// TsRun sorts n elements of T between four sentinels on each side and
// checks: the sentinels unchanged, every element intact, the ids the same
// multiset as before (a permutation; for 1-byte elements, of the keys),
// and, under the order, the keys in order and, for a stable way, equal keys
// in input order; under always false a stable way changes nothing. buf
// holds at least n + 8 elements.
TsRun[T: TsElem](a: &Arena, h: &Alloc, buf: mut []T, n: int, pat: int, way: int, rel: int, kmax: int): OutOfMemory!void {
    xs := mut buf[0..n + 8]
    for j in 0..4 {
        xs[j].TsSetSent(j)
        xs[n + 4 + j].TsSetSent(4 + j)
    }
    var seed: u64 = Trunc[u64](n) *% 1000003 +% Trunc[u64](pat) *% 7919 +% 1
    for i in 0..n {
        xs[4 + i].TsSet(TsKeyAt(pat, i, n, mut seed, kmax), i)
    }
    var before := List[int].New(a)
    for i in 0..n {
        try before.Push(xs[4 + i].TsId())
    }
    try TsSortBy(mut xs[4..n + 4], h, way, rel)
    for j in 0..4 {
        Assert(xs[j].TsSent() == j && xs[j].TsIntact())
        Assert(xs[n + 4 + j].TsSent() == 4 + j && xs[n + 4 + j].TsIntact())
    }
    var after := List[int].New(a)
    for i in 0..n {
        x := &xs[4 + i]
        Assert(x.TsSent() < 0 && x.TsIntact())
        try after.Push(x.TsId())
        if rel == 0 && i > 0 {
            p := &xs[3 + i]
            Assert(p.TsKey() <= x.TsKey())
            if way > 0 && p.TsKey() == x.TsKey() {
                Assert(p.TsId() <= x.TsId())
            }
        }
        if rel == 2 && way > 0 {
            Assert(after[i] == before[i])
        }
    }
    before.Sort(TlLess)
    after.Sort(TlLess)
    for i in 0..n {
        Assert(after[i] == before[i])
    }
}

// TsLengths are the lengths every type runs at: 0 to 70, then past the
// block merge's levels and buffers.
TsLengths(a: &Arena, big: bool): OutOfMemory!mut []int {
    var l := List[int].New(a)
    for n in 0..71 {
        try l.Push(n)
    }
    for n in [96, 127, 128, 129, 200] {
        try l.Push(n)
    }
    if big {
        for n in [255, 256, 257, 1_000, 1_023, 1_024, 1_025, 5_000] {
            try l.Push(n)
        }
    }
    return l.Done()
}

// TsAll runs TsRun over every length, every pattern under the order and,
// since the other relations ignore the keys, one pattern under each of them,
// for the four ways; buf holds at least 5,008 elements.
TsAll[T: TsElem](a: &Arena, h: &Alloc, buf: mut []T, big: bool, kmax: int): OutOfMemory!void {
    for n in try TsLengths(a, big) {
        for way in 0..4 {
            for pat in 0..7 {
                try TsRun(a, h, buf, n, pat, way, 0, kmax)
            }
            for rel in 1..4 {
                try TsRun(a, h, buf, n, 4, way, rel, kmax)
            }
        }
    }
}

test "the element types under test are 1, 2, 4, 8, 9, 16 and 40 bytes, so each reaches its sort path" {
    Assert(SizeOf[u8]() == 1 && SizeOf[u16]() == 2 && SizeOf[u32]() == 4 && SizeOf[u64]() == 8)
    Assert(SizeOf[TsB9]() == 9 && AlignOf[TsB9]() == 1)
    Assert(SizeOf[TsP16]() == 16 && SizeOf[TsP40]() == 40)
}

test "every sort of 1- to 8-byte elements at every length to 200, under an order and under none, is a permutation that touches nothing outside" (sys: &System) {
    h := sys.heap.Alloc()
    try TsAll(sys.arena, h, try sys.arena.Slice[u8](5_008), false, 248)
    try TsAll(sys.arena, h, try sys.arena.Slice[u16](5_008), false, 256)
    try TsAll(sys.arena, h, try sys.arena.Slice[u32](5_008), true, 65_536)
    try TsAll(sys.arena, h, try sys.arena.Slice[u64](5_008), true, 0x7FFF_FFFF)
}

test "every sort of 9-, 16- and 40-byte elements at every length to 5,000, under an order and under none, is a permutation that touches nothing outside" (sys: &System) {
    h := sys.heap.Alloc()
    try TsAll(sys.arena, h, try sys.arena.Slice[TsB9](5_008), true, 65_536)
    try TsAll(sys.arena, h, try sys.arena.Slice[TsP16](5_008), true, 0x7FFF_FFFF_FFFF)
    try TsAll(sys.arena, h, try sys.arena.Slice[TsP40](5_008), true, 0x7FFF_FFFF_FFFF)
}

// TsA16 is 16 bytes aligned to 16, over the stack cache's 8, so SortStable
// merges it without the cache (BlkCacheCap 0).
align(16) struct TsA16 {
    k: int
    id: int
}

impl TsElem for TsA16 {
    TsSet(mut self, k: int, id: int) {
        self.k = k
        self.id = id
    }
    TsSetSent(mut self, j: int) {
        self.k = 0
        self.id = -1 - j
    }
    TsKey(&self): int {
        return self.k
    }
    TsId(&self): int {
        return self.id
    }
    TsSent(&self): int {
        return if self.id < 0 { -1 - self.id } else { -1 }
    }
    TsIntact(&self): bool {
        return true
    }
}

// TsP4096 is 4,096 bytes, a cache of one slot; TsBig 4,104, too big for
// the cache. The first and last pad words repeat the id and the key.
struct TsP4096 {
    k: int
    id: int
    pad: [510]u64
}

impl TsElem for TsP4096 {
    TsSet(mut self, k: int, id: int) {
        self.k = k
        self.id = id
        self.pad[0] = Cast[u64](id + 8)
        self.pad[509] = Cast[u64](k)
    }
    TsSetSent(mut self, j: int) {
        self.TsSet(0, -1 - j)
    }
    TsKey(&self): int {
        return self.k
    }
    TsId(&self): int {
        return self.id
    }
    TsSent(&self): int {
        return if self.id < 0 { -1 - self.id } else { -1 }
    }
    TsIntact(&self): bool {
        return self.pad[0] == Cast[u64](self.id + 8) && self.pad[509] == Cast[u64](self.k)
    }
}

struct TsBig {
    k: int
    id: int
    pad: [511]u64
}

impl TsElem for TsBig {
    TsSet(mut self, k: int, id: int) {
        self.k = k
        self.id = id
        self.pad[0] = Cast[u64](id + 8)
        self.pad[510] = Cast[u64](k)
    }
    TsSetSent(mut self, j: int) {
        self.TsSet(0, -1 - j)
    }
    TsKey(&self): int {
        return self.k
    }
    TsId(&self): int {
        return self.id
    }
    TsSent(&self): int {
        return if self.id < 0 { -1 - self.id } else { -1 }
    }
    TsIntact(&self): bool {
        return self.pad[0] == Cast[u64](self.id + 8) && self.pad[510] == Cast[u64](self.k)
    }
}

test "every sort of elements the stack cache holds one of or none of (aligned to 16, 4,096 and 4,104 bytes) is a permutation that touches nothing outside" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(AlignOf[TsA16]() == 16 && BlkCacheCap[TsA16]() == 0)
    Assert(SizeOf[TsP4096]() == 4_096 && BlkCacheCap[TsP4096]() == 1)
    Assert(SizeOf[TsBig]() == 4_104 && BlkCacheCap[TsBig]() == 0)
    Assert(BlkCacheCap[u8]() == 4_096 && BlkCacheCap[TsB9]() == 455 && BlkCacheCap[TsP40]() == 102)
    try TsAll(sys.arena, h, try sys.arena.Slice[TsA16](5_008), true, 0x7FFF_FFFF_FFFF)
    try TsAll(sys.arena, h, try sys.arena.Slice[TsP4096](208), false, 0x7FFF_FFFF_FFFF)
    try TsAll(sys.arena, h, try sys.arena.Slice[TsBig](208), false, 0x7FFF_FFFF_FFFF)
}

// TsDeep recurses depth times with 128 bytes of locals and the 512 of xs
// each (about 860 bytes a level in release and 1.3 KiB in dev, measured by
// the deepest recursion a 64 KiB task survives), then sorts xs stably in
// place, so SortStableInPlace runs with its cache on what stack is left.
TsDeep(depth: int, done: &raw.Atomic[i64]): u64 {
    var pad: [16]u64 = [0; 16]
    for i in 0..16 {
        pad[i] = Cast[u64](depth * 131 + i)
    }
    if depth > 0 {
        return TsDeep(depth - 1, done) +% pad[depth % 16]
    }
    var xs: [64]u64 = [0; 64]
    var s: u64 = 3
    for i in 0..64 {
        xs[i] = Cast[u64](TlNext(mut s) % 1_000)
    }
    xs.SortStableInPlace(TsLessU64)
    var ok := true
    for i in 1..64 {
        ok = ok && xs[i - 1] <= xs[i]
    }
    if ok {
        _ = done.Add(1)
    }
    return pad[7] +% xs[0]
}

TsLessU64(a: &u64, b: &u64): bool {
    return *a < *b
}

TsDeepTask(depth: int, done: &raw.Atomic[i64]) {
    _ = TsDeep(depth, done)
}

// On Windows a 64 KiB task keeps 4 KiB of guard and 16 KiB of guarantee,
// so about 44 KiB is usable. SortStableInPlace's 4 KiB cache takes some
// levels of recursion from that; SortStableInPlace of 64 u64 (the cache
// path) must still fit under 15 levels in dev and in release.
test "SortStableInPlace, with its 4,096-byte cache, runs on a task's smallest stack (64 KiB) under 15 levels of recursion" (sys: &System) {
    done := raw.Atomic[i64].New(0)
    scope s := sys.io.ScopeWith(Limits(tasks: 1, stack: StackSize.Bytes(n: 65_536))) {
        s.go TsDeepTask(15, &done)
    }
    Assert(done.Load() == 1)
}

// TsOwned is a linear element that owns heap memory holding its id: a value
// the sort loses leaks (exit 102 from the counting heap), and one it
// doubles shows as an id seen twice before the frees.
interface TsOwned {
    TsOId(&self): int
    TsOFree(self, a: &Alloc)
}

// TsOwn8 is 8 bytes: Sort's networks and held-value insertion.
linear view struct TsOwn8 {
    p: *int
}

TsOwn8New(a: &Alloc, id: int): OutOfMemory!TsOwn8 {
    p := GrowSlots(a, Dangling[int](), 0, 0, 1, false)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    SlotWrite(p, 0, id)
    return TsOwn8(p: p)
}

impl TsOwned for TsOwn8 {
    TsOId(&self): int {
        return *SlotRef(self.p, 0)
    }
    TsOFree(self, a: &Alloc) {
        TsOwn8(p) := self
        FreeSlots(a, p, 1)
    }
}

// TsOwn16 is a Box, 16 bytes: the Lomuto partitions.
linear struct TsOwn16 {
    b: Box[int]
}

impl TsOwned for TsOwn16 {
    TsOId(&self): int {
        return *self.b.Ref()
    }
    TsOFree(self, a: &Alloc) {
        TsOwn16(b) := self
        b.Free(a)
    }
}

// TsOwn40 is a Box and three words, 40 bytes: Hoare's partitions.
linear struct TsOwn40 {
    b: Box[int]
    x: int
    y: int
    z: int
}

impl TsOwned for TsOwn40 {
    TsOId(&self): int {
        return *self.b.Ref()
    }
    TsOFree(self, a: &Alloc) {
        TsOwn40(b, x, y, z) := self
        Assert(x == y && y == z)
        b.Free(a)
    }
}

// TsOPush pushes x; out of memory, it frees x and panics, which fails the
// block.
TsOPush[T: TsOwned](l: mut List[T], h: &Alloc, x: T into l) {
    l.Push(x) else {
        x.TsOFree(h)
        Panic("TsOPush: out of memory")
    }
}

TsOKey[T: TsOwned](a: &T): int {
    return (a.TsOId() * 7) % 5
}

TsOOrd[T: TsOwned](a: &T, b: &T): bool {
    return TsOKey(a) < TsOKey(b)
}

TsOTrue[T: TsOwned](a: &T, b: &T): bool {
    return true
}

TsOFalse[T: TsOwned](a: &T, b: &T): bool {
    return false
}

TsOCoin[T: TsOwned](a: &T, b: &T): bool {
    return TlCoin(a.TsOId(), b.TsOId(), 0x9E3779B97F4A7C15)
}

TsOSortBy[T: TsOwned](s: mut []T, h: &Alloc, way: int, rel: int): OutOfMemory!void {
    if way == 0 {
        if rel == 0 {
            s.Sort(TsOOrd[T])
        } else if rel == 1 {
            s.Sort(TsOTrue[T])
        } else if rel == 2 {
            s.Sort(TsOFalse[T])
        } else {
            s.Sort(TsOCoin[T])
        }
    } else if way == 1 {
        if rel == 0 {
            try s.SortStable(h, TsOOrd[T])
        } else if rel == 1 {
            try s.SortStable(h, TsOTrue[T])
        } else if rel == 2 {
            try s.SortStable(h, TsOFalse[T])
        } else {
            try s.SortStable(h, TsOCoin[T])
        }
    } else if way == 2 {
        var c := TdCount(inner: raw.Of(h), allocs: 0, frees: 0, bytes: 0, refuse: false, limit: (s.Len() / 2) * SizeOfT[T]())
        var ctx := raw.Null[u8]()
        unsafe "a pointer cast: TdTable's entries cast it back to the TdCount" {
            ctx = raw.PtrCast[TdCount, u8](raw.OfMut(mut c))
        }
        lim := MakeHeap(ctx, raw.Of(&TdTable))
        if rel == 0 {
            try s.SortStable(lim.Alloc(), TsOOrd[T])
        } else if rel == 1 {
            try s.SortStable(lim.Alloc(), TsOTrue[T])
        } else if rel == 2 {
            try s.SortStable(lim.Alloc(), TsOFalse[T])
        } else {
            try s.SortStable(lim.Alloc(), TsOCoin[T])
        }
    } else {
        if rel == 0 {
            s.SortStableInPlace(TsOOrd[T])
        } else if rel == 1 {
            s.SortStableInPlace(TsOTrue[T])
        } else if rel == 2 {
            s.SortStableInPlace(TsOFalse[T])
        } else {
            s.SortStableInPlace(TsOCoin[T])
        }
    }
}

// TsOCheck sorts l, n owned elements with ids 0 to n - 1, by way and rel,
// checks every id once (and the keys in order under the order), then frees
// every element.
TsOCheck[T: TsOwned](a: &Arena, h: &Alloc, l: mut List[T], n: int, way: int, rel: int): OutOfMemory!void {
    try TsOSortBy(l.ItemsMut(), h, way, rel)
    var ids := List[int].New(a)
    for i in 0..l.Len() {
        try ids.Push(l[i].TsOId())
        if rel == 0 && i > 0 {
            Assert(TsOKey(&l[i - 1]) <= TsOKey(&l[i]))
        }
    }
    ids.Sort(TlLess)
    Assert(ids.Len() == n)
    for i in 0..n {
        Assert(ids[i] == i)
    }
    for l.Len() > 0 {
        x := l.Pop().Must()
        x.TsOFree(h)
    }
}

test "every sort of owned elements of 8, 16 and 40 bytes, under an order and under none, moves each value once: nothing leaks or frees twice" (sys: &System) {
    h := sys.heap.Alloc()
    Assert(SizeOf[TsOwn8]() == 8 && SizeOf[TsOwn16]() == 16 && SizeOf[TsOwn40]() == 40)
    for n in [0, 1, 2, 3, 8, 9, 12, 13, 16, 17, 20, 21, 31, 32, 33, 63, 64, 65, 100, 300, 1_000] {
        for way in 0..4 {
            for rel in 0..4 {
                var l8 := List[TsOwn8].New(sys.arena)
                var l16 := List[TsOwn16].New(sys.arena)
                var l40 := List[TsOwn40].New(sys.arena)
                for i in 0..n {
                    TsOPush(mut l8, h, try TsOwn8New(h, i))
                    TsOPush(mut l16, h, TsOwn16(b: try Box[int].New(h, i)))
                    TsOPush(mut l40, h, TsOwn40(b: try Box[int].New(h, i), x: i, y: i, z: i))
                }
                try TsOCheck(sys.arena, h, mut l8, n, way, rel)
                try TsOCheck(sys.arena, h, mut l16, n, way, rel)
                try TsOCheck(sys.arena, h, mut l40, n, way, rel)
            }
        }
    }
}

// TsEvalDigest sorts fixed inputs of 1, 2, 4, 8 and 16 bytes with Sort and
// SortStableInPlace, with no allocator, so it runs at compile time too: the
// networks, both Lomuto partitions, the spread pivot and the block merge's
// raw swaps in the evaluator.
TsEvalOne[T: TsElem](xs: mut []T, way: int, kmax: int): u64 {
    var s: u64 = 99
    for i in 0..xs.Len() {
        xs[i].TsSet(TlNext(mut s) % kmax, i % 248)
    }
    if way == 0 {
        xs.Sort(TsOrd[T])
    } else {
        xs.SortStableInPlace(TsOrd[T])
    }
    var h: u64 = 14695981039346656037
    for i in 0..xs.Len() {
        h = (h ^ Trunc[u64](xs[i].TsKey() * 1_000_003 + xs[i].TsId())) *% 1099511628211
    }
    return h
}

TsEvalDigest(): u64 {
    var b1: [300]u8 = [0; 300]
    var b2: [200]u16 = [0; 200]
    var b4: [300]u32 = [0; 300]
    var b8: [300]u64 = [0; 300]
    var b16: [300]TsP16 = [TsP16(k: 0, id: 0); 300]
    var h: u64 = 0
    for way in 0..2 {
        h = h *% 31 +% TsEvalOne(mut b1[..], way, 37)
        h = h *% 31 +% TsEvalOne(mut b2[..], way, 37)
        h = h *% 31 +% TsEvalOne(mut b4[..], way, 37)
        h = h *% 31 +% TsEvalOne(mut b8[..], way, 37)
        h = h *% 31 +% TsEvalOne(mut b16[..], way, 37)
    }
    return h
}

const TsEvalC = TsEvalDigest() fuel 200_000_000

test "Sort and SortStableInPlace of 1- to 16-byte elements at compile time give the native result" {
    Assert(TsEvalC == TsEvalDigest())
}

// TsWord holds a str, a pointer the evaluator tags, beside its id: 24
// bytes, a cache of 170.
struct TsWord {
    w: str
    id: int
}

TsWordLess(a: &TsWord, b: &TsWord): bool {
    return a.w.Len() < b.w.Len()
}

// TsEvalWords sorts 400 words of 1 to 9 letters stably in place by length,
// so at compile time the cache holds values with pointers, and digests the ids.
TsEvalWords(): u64 {
    letters := "abcdefghi"
    var xs: [400]TsWord = [TsWord(w: "", id: 0); 400]
    var s: u64 = 11
    for i in 0..400 {
        n := 1 + TlNext(mut s) % 9
        xs[i] = TsWord(w: letters[0..n], id: i)
    }
    xs.SortStableInPlace(TsWordLess)
    var h: u64 = 14695981039346656037
    for i in 0..400 {
        h = (h ^ Trunc[u64](xs[i].id * 16 + xs[i].w.Len())) *% 1099511628211
    }
    return h
}

const TsEvalWordsC = TsEvalWords() fuel 200_000_000

test "SortStableInPlace of elements holding pointers at compile time gives the native result" {
    Assert(TsEvalWordsC == TsEvalWords())
}
