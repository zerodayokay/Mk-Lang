// Why: List, HeapList and the Slice[T] functions are the prelude's sequences;
// their sorts must end and stay permutations even under a broken order.
// What: test blocks for List, HeapList and every Slice[T] function on []T,
// [N]T, List and HeapList: sorts, search, Extend, Truncate and the views.
// How: run by `mk test --std=std.core` with the counting heap (a leak fails
// with exit 102, so SortStable's scratch is checked freed). Helper names
// start with Tl, because every file of std/core is one module. The rows that
// panic are whole programs in std/std-coll_bounds_test.go,
// std-coll_linear_test.go and std-coll_slice_test.go.

import std.raw

struct TlPair {
    k: int
    id: int
}

TlLess(a: &int, b: &int): bool {
    return *a < *b
}

TlByK(a: &TlPair, b: &TlPair): bool {
    return a.k < b.k
}

TlKeyLess(a: &TlPair, k: &int): bool {
    return a.k < *k
}

TlSorted(s: []int): bool {
    for i in 1..s.Len() {
        if s[i] < s[i - 1] {
            return false
        }
    }
    return true
}

// A seeded generator, so a block's inputs are fixed.
TlNext(s: mut u64): int {
    *s = *s *% 6364136223846793005 +% 1442695040888963407
    return Trunc[int](*s >> 33)
}

// Relations that are no strict weak order: TlChaos is a fixed
// coin per ordered pair of ids, so it is neither irreflexive, asymmetric nor
// transitive; TlTrue says every element is below every other; TlFalse that
// all are equal. A sort under any of them must end, not panic, and leave a
// permutation of its own slice and nothing outside it touched.
TlCoin(x: int, y: int, seed: u64): bool {
    var h := (Trunc[u64](x) *% 0x9E3779B97F4A7C15) ^ (Trunc[u64](y) *% 0xC2B2AE3D27D4EB4F) ^ seed
    h = (h ^ (h >> 31)) *% 0xBF58476D1CE4E5B9
    return (h >> 63) == 1
}

TlChaos(a: &TlPair, b: &TlPair): bool {
    return TlCoin(a.id, b.id, 0)
}

TlChaosKey(a: &TlPair, k: &int): bool {
    return TlCoin(a.id, *k, 7)
}

TlTrue(a: &TlPair, b: &TlPair): bool {
    return true
}

TlFalse(a: &TlPair, b: &TlPair): bool {
    return false
}

// TlSortBy sorts s with one of the six sorts (way 0 Sort, 1 Sort with a
// capturing literal, 2 SortStable from h, 3 SortStable with a capturing
// literal, 4 SortStableInPlace, 5 SortStableInPlace with a capturing literal;
// the literals replace the old With forms) under one relation (rel 0
// TlChaos, 1 TlTrue, 2 TlFalse; the literals read seed, so they capture).
TlSortBy(s: mut []TlPair, h: &Alloc, way: int, rel: int, seed: &u64): OutOfMemory!void {
    if rel == 0 {
        if way == 0 {
            s.Sort(TlChaos)
        } else if way == 1 {
            s.Sort(fn(a: &TlPair, b: &TlPair): bool { return TlCoin(a.id, b.id, *seed) })
        } else if way == 2 {
            try s.SortStable(h, TlChaos)
        } else if way == 3 {
            try s.SortStable(h, fn(a: &TlPair, b: &TlPair): bool { return TlCoin(a.id, b.id, *seed) })
        } else if way == 4 {
            s.SortStableInPlace(TlChaos)
        } else {
            s.SortStableInPlace(fn(a: &TlPair, b: &TlPair): bool { return TlCoin(a.id, b.id, *seed) })
        }
    } else if rel == 1 {
        if way == 0 {
            s.Sort(TlTrue)
        } else if way == 1 {
            s.Sort(fn(a: &TlPair, b: &TlPair): bool { return *seed == *seed })
        } else if way == 2 {
            try s.SortStable(h, TlTrue)
        } else if way == 3 {
            try s.SortStable(h, fn(a: &TlPair, b: &TlPair): bool { return *seed == *seed })
        } else if way == 4 {
            s.SortStableInPlace(TlTrue)
        } else {
            s.SortStableInPlace(fn(a: &TlPair, b: &TlPair): bool { return *seed == *seed })
        }
    } else {
        if way == 0 {
            s.Sort(TlFalse)
        } else if way == 1 {
            s.Sort(fn(a: &TlPair, b: &TlPair): bool { return *seed != *seed })
        } else if way == 2 {
            try s.SortStable(h, TlFalse)
        } else if way == 3 {
            try s.SortStable(h, fn(a: &TlPair, b: &TlPair): bool { return *seed != *seed })
        } else if way == 4 {
            s.SortStableInPlace(TlFalse)
        } else {
            s.SortStableInPlace(fn(a: &TlPair, b: &TlPair): bool { return *seed != *seed })
        }
    }
}

// TlIdsArePerm is true when the ids of s are 0 to s.Len() - 1, each once.
TlIdsArePerm(a: &Arena, s: []TlPair): OutOfMemory!bool {
    var ids := List[int].New(a)
    for i in 0..s.Len() {
        try ids.Push(s[i].id)
    }
    ids.Sort(TlLess)
    for i in 0..ids.Len() {
        if ids[i] != i {
            return false
        }
    }
    return true
}

// A zero-size element: no field, so every TlZ equals every other.
struct TlZ {}

impl Eq for TlZ {
    Eq(&self, other: &TlZ): bool {
        return true
    }
}

TlZLess(a: &TlZ, b: &TlZ): bool {
    return false
}

test "List grows from nothing and keeps every element" (sys: &System) {
    var l := List[int].New(sys.arena)
    Assert(l.Len() == 0)
    for i in 0..10_000 {
        try l.Push(i * 3)
        Assert(l.Len() == i + 1)
    }
    for i in 0..10_000 {
        Assert(l[i] == i * 3)
    }
}

test "Pop returns the last element, none when empty" (sys: &System) {
    var l := List[int].New(sys.arena)
    Assert(l.Pop().IsNone())
    try l.Push(1)
    try l.Push(2)
    Assert(l.Pop().Must() == 2)
    Assert(l.Pop().Must() == 1)
    Assert(l.Pop().IsNone())
    Assert(l.Len() == 0)
}

test "Clear keeps the capacity and the list refills" (sys: &System) {
    var l := List[int].New(sys.arena)
    for i in 0..100 {
        try l.Push(i)
    }
    l.Clear()
    Assert(l.Len() == 0)
    for i in 0..100 {
        try l.Push(100 - i)
    }
    Assert(l.Len() == 100 && l[0] == 100 && l[99] == 1)
}

test "Insert and Remove shift and keep order" (sys: &System) {
    var l := List[int].New(sys.arena)
    try l.Insert(0, 5)
    try l.Insert(0, 1)
    try l.Insert(2, 9)
    try l.Insert(1, 3)
    Assert(l.Len() == 4 && l[0] == 1 && l[1] == 3 && l[2] == 5 && l[3] == 9)
    Assert(l.Remove(1) == 3)
    Assert(l.Remove(2) == 9)
    Assert(l.Remove(0) == 1)
    Assert(l.Len() == 1 && l[0] == 5)
    // Every position of a 40-element list, against the arithmetic.
    var m := List[int].New(sys.arena)
    for i in 0..40 {
        try m.Push(i)
    }
    for p in 0..41 {
        try m.Insert(p, -1)
        for i in 0..41 {
            if i < p {
                Assert(m[i] == i)
            } else if i == p {
                Assert(m[i] == -1)
            } else {
                Assert(m[i] == i - 1)
            }
        }
        Assert(m.Remove(p) == -1)
    }
}

test "Done gives the elements as a slice" (sys: &System) {
    var l := List[int].New(sys.arena)
    for i in 0..5 {
        try l.Push(i)
    }
    s := l.Done()
    Assert(s.Len() == 5 && s[4] == 4)
    s[0] = 7
    Assert(s[0] == 7)
}

test "every Slice function on []T, [N]T, List and HeapList" (sys: &System) {
    a := sys.heap.Alloc()
    var arr: [6]int = [5, 1, 4, 1, 5, 9]
    var l := List[int].New(sys.arena)
    var h := HeapList[int].New(a)
    defer h.Free(a)
    for x in arr {
        try l.Push(x)
        try h.Push(a, x)
    }
    Assert(arr.Len() == 6 && l.Len() == 6 && h.Len() == 6 && arr[..].Len() == 6)
    Assert(*arr.Get(2).Must() == 4 && *l.Get(2).Must() == 4 && *h.Get(2).Must() == 4)
    Assert(arr.Get(6).IsNone() && l.Get(-1).IsNone() && h.Get(6).IsNone())
    *arr.GetMut(0).Must() = 2
    *l.GetMut(0).Must() = 2
    *h.GetMut(0).Must() = 2
    Assert(arr.Index(&9).Must() == 5 && l.Index(&1).Must() == 1 && h.Index(&5).Must() == 4)
    Assert(arr.Has(&2) && !l.Has(&3) && h.Has(&4) && arr.Index(&7).IsNone())
    arr.Sort(TlLess)
    l.Sort(TlLess)
    h.Sort(TlLess)
    Assert(TlSorted(arr[..]) && TlSorted(l.Items()) && TlSorted(h.Items()))
    arr.Reverse()
    l.Reverse()
    h.Reverse()
    Assert(arr[0] == 9 && l[0] == 9 && h[0] == 9 && arr[5] == 1)
    arr.Swap(0, 5)
    l.Swap(0, 5)
    h.Swap(0, 5)
    h.Swap(3, 3)
    Assert(arr[0] == 1 && l[5] == 9 && h[0] == 1)
    for i in 0..6 {
        Assert(arr[i] == l[i] && l[i] == h[i])
    }
    l.Fill(4)
    h.CopyFrom(l.Items())
    arr.CopyFrom(h.Items())
    Assert(arr[0] == 4 && arr[5] == 4 && h[3] == 4)
    s := mut arr[1..4]
    s.Fill(0)
    Assert(arr[0] == 4 && arr[1] == 0 && arr[3] == 0 && arr[4] == 4)
}

test "Index of a byte at every offset and length" (sys: &System) {
    var b: [80]u8 = [0; 80]
    for i in 0..80 {
        b[i] = Trunc[u8](i % 7 + 1)
    }
    b[63] = 0xEE
    for o in 0..64 {
        for n in 0..80 - o {
            got := b[o..o + n].Index(&0xEE)
            if o + n > 63 && o <= 63 {
                Assert(got.Must() == 63 - o)
            } else {
                Assert(got.IsNone())
            }
        }
    }
}

test "Sort, and Sort with a capturing literal, order 1,000 seeded pairs" (sys: &System) {
    var s: u64 = 3
    var l := List[TlPair].New(sys.arena)
    for i in 0..1_000 {
        try l.Push(TlPair(k: TlNext(mut s) % 100, id: i))
    }
    l.Sort(TlByK)
    for i in 1..1_000 {
        Assert(l[i - 1].k <= l[i].k)
    }
    m := 7
    l.Sort(fn(a: &TlPair, b: &TlPair): bool { return a.k % m < b.k % m })
    for i in 1..1_000 {
        Assert(l[i - 1].k % 7 <= l[i].k % 7)
    }
}

test "SortStable and SortStableInPlace keep equal elements in their order, from a heap, an arena, or in place" (sys: &System) {
    var s: u64 = 13
    for n in [0, 1, 2, 19, 20, 21, 31, 32, 33, 64, 65, 256, 257, 1_000, 5_000] {
        var l := List[TlPair].New(sys.arena)
        var l2 := List[TlPair].New(sys.arena)
        for i in 0..n {
            k := TlNext(mut s) % 10
            try l.Push(TlPair(k: k, id: i))
            try l2.Push(TlPair(k: k, id: i))
        }
        try l.SortStable(sys.heap.Alloc(), TlByK)
        l2.SortStableInPlace(TlByK)
        for i in 0..n {
            Assert(l[i].id == l2[i].id)
            if i > 0 {
                Assert(l[i - 1].k < l[i].k || (l[i - 1].k == l[i].k && l[i - 1].id < l[i].id))
            }
        }
        // A second key, stably, with scratch from the arena vs in place.
        m := 3
        try l.SortStable(sys.arena.Alloc(), fn(a: &TlPair, b: &TlPair): bool { return a.k % m < b.k % m })
        l2.SortStableInPlace(fn(a: &TlPair, b: &TlPair): bool { return a.k % m < b.k % m })
        for i in 0..n {
            Assert(l[i].id == l2[i].id)
            if i > 0 {
                x := l[i - 1]
                y := l[i]
                Assert(x.k % 3 < y.k % 3 || (x.k % 3 == y.k % 3 && (x.k < y.k || (x.k == y.k && x.id < y.id))))
            }
        }
    }
}

test "SortStable and SortStableInPlace on an array, a sub-slice and a HeapList" (sys: &System) {
    a := sys.heap.Alloc()
    var arr: [40]TlPair = [TlPair(k: 0, id: 0); 40]
    var arr2: [40]TlPair = [TlPair(k: 0, id: 0); 40]
    for i in 0..40 {
        p := TlPair(k: (40 - i) / 3, id: i)
        arr[i] = p
        arr2[i] = p
    }
    try arr.SortStable(a, TlByK)
    arr2.SortStableInPlace(TlByK)
    // Keys 0 (ids 38, 39), 1 (35 to 37), ..., 13 (ids 0, 1).
    Assert(arr[0].id == 38 && arr[1].id == 39 && arr[2].id == 35 && arr[38].id == 0 && arr[39].id == 1)
    for i in 0..40 {
        Assert(arr[i].id == arr2[i].id)
    }
    var h := HeapList[int].New(a)
    defer h.Free(a)
    for i in 0..100 {
        try h.Push(a, (i * 37) % 100)
    }
    sub := mut h.ItemsMut()[10..90]
    try sub.SortStable(a, TlLess)
    Assert(TlSorted(h.Items()[10..90]) && h[0] == 0 && h[9] == (9 * 37) % 100 && h[90] == (90 * 37) % 100)
    try h.SortStable(a, TlLess)
    for i in 0..100 {
        Assert(h[i] == i)
    }
    var h2 := HeapList[int].New(a)
    defer h2.Free(a)
    for i in 0..50 {
        try h2.Push(a, (i * 37) % 50)
    }
    sub2 := mut h2.ItemsMut()[10..40]
    sub2.SortStableInPlace(TlLess)
    Assert(TlSorted(h2.Items()[10..40]) && h2[0] == 0 && h2[49] == (49 * 37) % 50)
    h2.SortStableInPlace(TlLess)
    for i in 0..50 {
        Assert(h2[i] == i)
    }
}

// An allocator that has no memory: every request fails.
TlNoAlloc(ctx: *u8, size: int, align: int): *u8 {
    return raw.Null[u8]()
}

TlNoResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

TlNoFree(ctx: *u8, p: *u8, size: int, align: int) {
}

const TlNoTable = AllocTable(alloc: TlNoAlloc, resize: TlNoResize, free: TlNoFree)

test "SortStable out of memory fails before any element moves" (sys: &System) {
    h := MakeHeap(raw.Null[u8](), raw.Of(&TlNoTable))
    none_left := h.Alloc()
    // 300 16-byte elements exceed the 4,096-byte stack scratch (256 slots),
    // so SortStable asks none_left and fails before moving any element.
    var l := List[TlPair].New(sys.arena)
    for i in 0..300 {
        try l.Push(TlPair(k: 299 - i, id: i))
    }
    var failed := false
    l.SortStable(none_left, TlByK) else {
        failed = true
    }
    Assert(failed)
    for i in 0..300 {
        Assert(l[i].id == i)
    }
    // Up to 256 16-byte elements fit in the 4,096-byte stack scratch, so
    // SortStable allocates nothing from none_left and sorts them.
    sub := mut l.ItemsMut()[0..256]
    try sub.SortStable(none_left, TlByK)
    Assert(l[0].id == 255 && l[255].id == 0 && l[256].id == 256)
    // A zero-size element allocates nothing either.
    var z := List[TlZ].New(sys.arena)
    for _ in 0..300 {
        try z.Push(TlZ())
    }
    try z.SortStable(none_left, TlZLess)
    Assert(z.Len() == 300)
}

test "Search returns the insertion point" (sys: &System) {
    xs: [7]int = [1, 3, 3, 3, 5, 8, 8]
    Assert(xs.Search(&0, TlLess) == 0)
    Assert(xs.Search(&1, TlLess) == 0)
    Assert(xs.Search(&2, TlLess) == 1)
    Assert(xs.Search(&3, TlLess) == 1)
    Assert(xs.Search(&4, TlLess) == 4)
    Assert(xs.Search(&8, TlLess) == 5)
    Assert(xs.Search(&9, TlLess) == 7)
    e: [0]int = []
    Assert(e.Search(&1, TlLess) == 0)
    // A key of another type than the element.
    ps: [4]TlPair = [TlPair(k: 2, id: 0), TlPair(k: 4, id: 1), TlPair(k: 4, id: 2), TlPair(k: 6, id: 3)]
    Assert(ps.Search(&4, TlKeyLess) == 1 && ps.Search(&5, TlKeyLess) == 3 && ps.Search(&7, TlKeyLess) == 4)
    // Against a linear scan, over a sorted list of 2,000 seeded keys.
    var s: u64 = 5
    var l := List[int].New(sys.arena)
    for _ in 0..2_000 {
        try l.Push(TlNext(mut s) % 500)
    }
    l.Sort(TlLess)
    for q in -1..502 {
        var want := 0
        for want < l.Len() && l[want] < q {
            want += 1
        }
        Assert(l.Search(&q, TlLess) == want)
    }
}

test "HeapList grows, shrinks and frees every byte" (sys: &System) {
    a := sys.heap.Alloc()
    var h := HeapList[int].New(a)
    defer h.Free(a)
    for i in 0..5_000 {
        try h.Push(a, i)
    }
    try h.Insert(a, 0, -1)
    Assert(h.Len() == 5_001 && h[0] == -1 && h[5_000] == 4_999)
    Assert(h.Remove(0) == -1)
    for i in 0..2_500 {
        Assert(h.Pop().Must() == 4_999 - i)
    }
    h.Clear()
    Assert(h.Len() == 0 && h.Pop().IsNone())
}

test "HeapText grows and Str gives the bytes" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapText.New(a)
    defer t.Free(a)
    try t.Push(a, "")
    for _ in 0..1_000 {
        try t.Push(a, "ab")
    }
    try t.Push(a, "é")
    s := t.Str()
    Assert(s.Len() == 2_002 && s.Byte(0) == 0x61 && s.Byte(1_999) == 0x62 && s.EndsWith("é"))
}

test "every sort under a less that is no order ends with a permutation and touches nothing outside" (sys: &System) {
    seed: u64 = 0x5DEECE66D
    // 31 to 33 and 63 to 65 cross the stable sorts' first merge levels; 256,
    // 1_000 and 5_000 reach both internal buffers of the block merge.
    for n in [0, 1, 2, 3, 12, 13, 19, 20, 21, 31, 32, 33, 40, 49, 50, 51, 63, 64, 65, 200, 256, 1_000, 5_000] {
        for form in 0..18 {
            way := form % 6
            rel := form / 6
            // Ten sentinels on each side of the sorted sub-slice.
            var l := List[TlPair].New(sys.arena)
            for i in 0..n + 20 {
                try l.Push(TlPair(k: i, id: i - 10))
            }
            s := mut l.ItemsMut()[10..n + 10]
            try TlSortBy(s, sys.heap.Alloc(), way, rel, &seed)
            for i in 0..10 {
                Assert(l[i].id == i - 10 && l[n + 10 + i].id == n + i)
            }
            Assert(try TlIdsArePerm(sys.arena, l.Items()[10..n + 10]))
            if rel == 2 && way >= 2 {
                // All equal: a stable sort keeps the input order.
                for i in 0..n {
                    Assert(l[10 + i].id == i)
                }
            }
            if rel == 0 {
                for q in [-1, 0, 5, 1_000_000] {
                    r := l.Items()[10..n + 10].Search(&q, TlChaosKey)
                    Assert(r >= 0 && r <= n)
                }
            }
        }
    }
}

test "a List of a zero-size type grows, shifts, sorts and searches with no memory" (sys: &System) {
    a := sys.heap.Alloc()
    var l := List[TlZ].New(sys.arena)
    var h := HeapList[TlZ].New(a)
    defer h.Free(a)
    for i in 0..1_000 {
        try l.Push(TlZ())
        try h.Push(a, TlZ())
        try l.Insert(i / 2, TlZ())
        try h.Insert(a, 0, TlZ())
    }
    Assert(l.Len() == 2_000 && h.Len() == 2_000)
    _ = l.Remove(1_999)
    _ = h.Remove(0)
    _ = l.Pop().Must()
    Assert(l.Len() == 1_998 && h.Len() == 1_999)
    l.Sort(TlZLess)
    try l.SortStable(a, TlZLess)
    l.SortStableInPlace(TlZLess)
    h.Sort(TlZLess)
    try h.SortStable(a, TlZLess)
    h.SortStableInPlace(TlZLess)
    l.Reverse()
    h.Swap(0, 1_998)
    z := TlZ()
    Assert(l.Index(&z).Must() == 0 && h.Has(&z) && l.Search(&z, TlZLess) == 0)
    l.Clear()
    h.Clear()
    Assert(l.Len() == 0 && h.Len() == 0 && l.Pop().IsNone() && h.Pop().IsNone())
}

// TlExtendModel extends a list with xs in pieces of step elements, pushes
// the same elements one at a time onto a second list, and reports whether
// the two hold the same elements.
TlExtendModel(a: &Alloc, xs: []int, step: int): OutOfMemory!bool {
    var e := HeapList[int].New(a)
    defer e.Free(a)
    var p := HeapList[int].New(a)
    defer p.Free(a)
    var at := 0
    for at < xs.Len() {
        end := if xs.Len() - at < step { xs.Len() } else { at + step }
        try e.Extend(a, xs[at..end])
        at = end
    }
    for x in xs {
        try p.Push(a, x)
    }
    if e.Len() != p.Len() {
        return false
    }
    for i in 0..e.Len() {
        if e[i] != p[i] {
            return false
        }
    }
    return true
}

test "HeapList.Extend equals a Push per element at every split and across growth" (sys: &System) {
    a := sys.heap.Alloc()
    var xs: [300]int = [0; 300]
    for i in 0..300 {
        xs[i] = i * 7 - 1000
    }
    for n in [0, 1, 2, 7, 8, 9, 63, 64, 65, 300] {
        for step in [1, 2, 3, 5, 16, 64, 299, 300, 301] {
            Assert(try TlExtendModel(a, xs[0..n], step))
        }
    }
}

// TlExtendPair is a plain struct, to extend with more than scalars.
struct TlExtendPair {
    k: str
    v: u16
}

test "HeapList.Extend of bytes, of an empty slice and of a plain struct, mixed with Push" (sys: &System) {
    a := sys.heap.Alloc()
    var b := HeapList[u8].New(a)
    defer b.Free(a)
    try b.Extend(a, "".AsBytes())
    Assert(b.Len() == 0)
    try b.Extend(a, "Hel".AsBytes())
    try b.Push(a, 0x6C)
    try b.Extend(a, "o, world".AsBytes())
    Assert((str.From(b.Items()) else { "" }) == "Hello, world")
    var big: [70000]u8 = [0; 70000]
    for i in 0..70000 {
        big[i] = Cast[u8](i % 251)
    }
    try b.Extend(a, big[..])
    Assert(b.Len() == 70012 && b[12] == 0 && b[70011] == Cast[u8](69999 % 251))
    var s := HeapList[TlExtendPair].New(a)
    defer s.Free(a)
    ps: [3]TlExtendPair = [TlExtendPair(k: "a", v: 1), TlExtendPair(k: "bc", v: 2), TlExtendPair(k: "", v: 65535)]
    try s.Extend(a, ps[..])
    try s.Extend(a, ps[1..3])
    Assert(s.Len() == 5 && s[0].k == "a" && s[3].k == "bc" && s[4].v == 65535)
}

// Extend copies through one CopyFrom (std/heaplist_extend_test.go checks
// the code): a zero-size element copies no byte, and n still counts every
// element.
test "HeapList.Extend of zero-size elements counts them and copies nothing" (sys: &System) {
    a := sys.heap.Alloc()
    var z := HeapList[TlZ].New(a)
    defer z.Free(a)
    zs: [1000]TlZ = [TlZ(); 1000]
    try z.Extend(a, zs[0..0])
    Assert(z.Len() == 0)
    try z.Extend(a, zs[..])
    try z.Push(a, TlZ())
    try z.Extend(a, zs[0..7])
    Assert(z.Len() == 1008)
    var n := 0
    for _ in z.Items() {
        n += 1
    }
    Assert(n == 1008)
}

// The first Extend grows into one allocation, of 4 slots (NextCap's
// least); the second fits in them and allocates nothing; the third needs
// a growth, which the spent budget refuses: OutOfMemory, and the buffer,
// the length and every element are as they were.
test "HeapList.Extend out of memory on a second growth leaves the list unchanged" (sys: &System) {
    var b := TtBudget(left: 1, inner: raw.Of(sys.heap.Alloc()))
    var hp := TtBudgetHeap(raw.OfMut(mut b))
    a := hp.Alloc()
    var l := HeapList[u64].New(a)
    defer l.Free(a)
    var xs: [40]u64 = [0; 40]
    for i in 0..40 {
        xs[i] = Trunc[u64](i) * 0x9E37_79B9 + 1
    }
    try l.Extend(a, xs[0..3])
    p := raw.Of(&l.Items()[0])
    try l.Extend(a, xs[3..4])
    Assert(l.Len() == 4 && raw.Same(raw.Of(&l.Items()[0]), p))
    var failed := false
    l.Extend(a, xs[..]) else {
        failed = true
    }
    Assert(failed)
    Assert(l.Len() == 4 && raw.Same(raw.Of(&l.Items()[0]), p))
    for i in 0..4 {
        Assert(l[i] == xs[i])
    }
}

// List.Truncate keeps the first n elements and the capacity.
test "List.Truncate keeps the first n, n at or past Len is a no-op, and the list refills" (sys: &System) {
    var l := List[int].New(sys.arena)
    l.Truncate(0)
    Assert(l.Len() == 0)
    for i in 0..100 {
        try l.Push(i)
    }
    l.Truncate(100)
    l.Truncate(1000)
    Assert(l.Len() == 100)
    l.Truncate(40)
    Assert(l.Len() == 40 && l[0] == 0 && l[39] == 39)
    p := raw.Of(&l.Items()[0])
    try l.Push(7)
    Assert(l.Len() == 41 && l[40] == 7)
    Assert(raw.Same(raw.Of(&l.Items()[0]), p)) // the capacity was kept: no growth
    l.Truncate(0)
    Assert(l.Len() == 0 && l.Pop().IsNone())
}

// The panics (a negative n, a linear element dropped) are whole programs in
// std/std-exitpanics_test.go, since a panic fails a block here.
