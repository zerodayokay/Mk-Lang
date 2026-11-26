// Why: a heap container that drops a linear element or leaks one breaks the
// release protocol; these tests pin it for HeapDeque and HeapBTree.
// What: tests of Free, FreeEach, FreeEmpty and Replace on both containers,
// with linear and plain elements, wrapped rings and split trees.
// How: they run with the counting heap, so a block that leaves heap memory
// live fails with exit 102. Helper names start with Cr, because every file of
// std/coll is one module.

// CrRes owns a heap text, as a resource a program defines would.
linear struct CrRes {
    id: int
    name: HeapText
}

impl Free for CrRes {
    Free(self, a: &Alloc) {
        CrRes(id: _, name) := self
        name.Free(a)
    }
}

// CrKey is an ordered key that owns memory: ordered by id, and Free.
linear struct CrKey {
    id: int
    name: HeapText
}

impl Ord for CrKey {
    Less(&self, other: &CrKey): bool {
        return self.id < other.id
    }
}

impl Free for CrKey {
    Free(self, a: &Alloc) {
        CrKey(id: _, name) := self
        name.Free(a)
    }
}

linear struct CrTok {
    n: int
}

impl Ord for CrTok {
    Less(&self, other: &CrTok): bool {
        return self.n < other.n
    }
}

CrClose(t: CrTok): int {
    CrTok(n) := t
    return n
}

CrText(a: &Alloc, n: int): OutOfMemory!HeapText from a {
    var t := HeapText.New(a)
    onerr t.Free(a)
    try t.Set(a, "a text the element owns, long enough to need the heap"[..n % 50])
    return t
}

CrNewRes(a: &Alloc, id: int): OutOfMemory!CrRes from a {
    return CrRes(id: id, name: try CrText(a, id))
}

CrNewKey(a: &Alloc, id: int): OutOfMemory!CrKey from a {
    return CrKey(id: id, name: try CrText(a, id + 7))
}

// CrLog records what FreeEach hands over: the count, whether the order
// held, and the last id seen.
struct CrLog {
    n: Cell[int]
    ordered: Cell[bool]
    last: Cell[int]
}

CrNewLog(): CrLog {
    return CrLog(n: Cell[int].New(0), ordered: Cell[bool].New(true), last: Cell[int].New(-1))
}

CrSee(l: &CrLog, id: int) {
    if id <= l.last.Load() {
        l.ordered.Store(false)
    }
    l.last.Store(id)
    l.n.Store(l.n.Load() + 1)
}

test "HeapDeque.Free releases every element, the ring wrapped, front and back pushes" (sys: &System) {
    a := sys.heap.Alloc()
    for n in [0, 1, 3, 4, 5, 17, 100] {
        var q := HeapDeque[HeapText].New(a)
        onerr q.Free(a)
        for i in 0..n {
            t := try CrText(a, i)
            if i % 3 == 0 {
                q.PushFront(a, t) else {
                    t.Free(a)
                    fail OutOfMemory
                }
            } else {
                q.Push(a, t) else {
                    t.Free(a)
                    fail OutOfMemory
                }
            }
        }
        // Pop a few from the front so head moves, then push again.
        for _ in 0..n / 4 {
            if t := q.PopFront() {
                t.Free(a)
            }
        }
        for i in 0..n / 4 {
            t := try CrText(a, i + 3)
            q.Push(a, t) else {
                t.Free(a)
                fail OutOfMemory
            }
        }
        Assert(q.Len() == n)
        q.Free(a)
    }
}

test "HeapDeque.Free of plain elements and of a program's Free type" (sys: &System) {
    a := sys.heap.Alloc()
    var p := HeapDeque[int].New(a)
    onerr p.Free(a)
    for i in 0..1000 {
        try p.Push(a, i)
    }
    p.Free(a)
    var q := HeapDeque[CrRes].New(a)
    onerr q.Free(a)
    for i in 0..64 {
        r := try CrNewRes(a, i)
        q.PushFront(a, r) else {
            r.Free(a)
            fail OutOfMemory
        }
    }
    q.Free(a)
}

test "HeapDeque.FreeEach hands every element front to back" (sys: &System) {
    a := sys.heap.Alloc()
    var q := HeapDeque[CrTok].New(a)
    onerr q.FreeEach(a, fn(t: CrTok) { _ = CrClose(t) })
    // Front pushes of 49..0 and back pushes of 50..99 leave 0..99 in order.
    for i in 0..50 {
        f := CrTok(n: 49 - i)
        q.PushFront(a, f) else {
            _ = CrClose(f)
            fail OutOfMemory
        }
        b := CrTok(n: 50 + i)
        q.Push(a, b) else {
            _ = CrClose(b)
            fail OutOfMemory
        }
    }
    l := CrNewLog()
    q.FreeEach(a, fn(t: CrTok) { CrSee(&l, CrClose(t)) })
    Assert(l.n.Load() == 100 && l.ordered.Load() && l.last.Load() == 99)
}

test "HeapDeque.FreeEmpty frees an emptied deque of linear elements" (sys: &System) {
    a := sys.heap.Alloc()
    var q := HeapDeque[CrTok].New(a)
    for i in 0..10 {
        t := CrTok(n: i)
        q.Push(a, t) else {
            _ = CrClose(t)
        }
    }
    for t := q.Pop() {
        _ = CrClose(t)
    }
    q.FreeEmpty(a)
    e := HeapDeque[CrTok].New(a)
    e.FreeEmpty(a)
}

test "HeapDeque.FreeEmpty of a non-empty deque panics [exit 101: HeapDeque.FreeEmpty of a non-empty deque]" (sys: &System) {
    a := sys.heap.Alloc()
    var q := HeapDeque[CrTok].New(a)
    t := CrTok(n: 1)
    q.Push(a, t) else {
        _ = CrClose(t)
        q.FreeEmpty(a)
        return
    }
    q.FreeEmpty(a)
}

test "HeapBTree.Free releases every key and value, across splits and merges" (sys: &System) {
    a := sys.heap.Alloc()
    for n in [0, 1, 2, 15, 16, 17, 200, 1000] {
        var t := HeapBTree[CrKey, CrRes].New(a)
        onerr t.Free(a)
        for i in 0..n {
            id := i * 7919 % 1009
            k := try CrNewKey(a, id)
            v := CrNewRes(a, id) else {
                k.Free(a)
                fail OutOfMemory
            }
            t.Set(a, k, v) else {
                k.Free(a)
                v.Free(a)
                fail OutOfMemory
            }
        }
        Assert(t.Len() == n)
        t.Free(a)
    }
    for n in [0, 1, 300] {
        var u := HeapBTree[int, CrRes].New(a)
        onerr u.Free(a)
        for i in 0..n {
            id := i * 7919 % 1009
            v := try CrNewRes(a, id)
            u.Set(a, id, v) else {
                v.Free(a)
                fail OutOfMemory
            }
        }
        // Remove every third key, so nodes merge and go to the spare lists
        // (Remove drops the stored key, so it takes a plain key).
        for i in 0..n {
            if i % 3 == 0 {
                id := i * 7919 % 1009
                if v := u.Remove(&id) {
                    v.Free(a)
                }
            }
        }
        u.Free(a)
    }
}

test "HeapBTree.Free of plain keys and values walks nothing and frees every node" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, int].New(a)
    onerr t.Free(a)
    for i in 0..5000 {
        try t.Set(a, i * 7919 % 10007, i)
    }
    for i in 0..2500 {
        k := i * 7919 % 10007
        _ = t.Remove(&k)
    }
    t.Free(a)
}

test "HeapBTree.FreeEach hands every entry in key order" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, CrRes].New(a)
    onerr t.Free(a)
    for i in 0..777 {
        k := i * 7919 % 1009
        v := try CrNewRes(a, k)
        t.Set(a, k, v) else {
            v.Free(a)
            fail OutOfMemory
        }
    }
    l := CrNewLog()
    bad := Cell[bool].New(false)
    t.FreeEach(a, fn(k: int, v: CrRes) {
        if v.id != k {
            bad.Store(true)
        }
        v.Free(a)
        CrSee(&l, k)
    })
    Assert(l.n.Load() == 777 && l.ordered.Load() && !bad.Load())
}

test "HeapBTree.FreeEmpty frees an emptied tree of linear values" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, CrRes].New(a)
    onerr t.Free(a)
    for i in 0..300 {
        v := try CrNewRes(a, i)
        t.Set(a, i, v) else {
            v.Free(a)
            fail OutOfMemory
        }
    }
    for i in 0..300 {
        if v := t.Remove(&i) {
            v.Free(a)
        }
    }
    t.FreeEmpty(a)
}

test "HeapBTree.FreeEmpty of a non-empty tree panics [exit 101: HeapBTree.FreeEmpty of a non-empty tree]" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, int].New(a)
    t.Set(a, 1, 1) else {
        t.FreeEmpty(a)
        return
    }
    t.FreeEmpty(a)
}

test "CollRelease of a linear value that is not Free panics, and the build has no Release for it [exit 101: release of a linear value that is not Free]" (sys: &System) {
    CollRelease(CrTok(n: 1), sys.heap.Alloc())
}

test "HeapBTree.Set over a present key releases the old value and the given key" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, HeapText].New(a)
    onerr t.Free(a)
    for round in 0..3 {
        for i in 0..40 {
            v := try CrText(a, i + round)
            t.Set(a, i, v) else {
                v.Free(a)
                fail OutOfMemory
            }
        }
    }
    Assert(t.Len() == 40)
    for i in 0..40 {
        if v := t.Get(&i) {
            Assert(v.Len() == (i + 2) % 50)
        } else {
            Assert(false)
        }
    }
    t.Free(a)
    var u := HeapBTree[CrKey, CrRes].New(a)
    onerr u.Free(a)
    for _ in 0..2 {
        for i in 0..20 {
            k := try CrNewKey(a, i)
            v := CrNewRes(a, i) else {
                k.Free(a)
                fail OutOfMemory
            }
            u.Set(a, k, v) else {
                k.Free(a)
                v.Free(a)
                fail OutOfMemory
            }
        }
    }
    Assert(u.Len() == 20)
    u.Free(a)
}

// CrNone is whether o is none; a token in it is closed.
CrNone(o: ?CrTok): bool {
    if t := o {
        _ = CrClose(t)
        return false
    }
    return true
}

test "HeapBTree.Replace hands back the old value and releases the given key" (sys: &System) {
    a := sys.heap.Alloc()
    var u := HeapBTree[CrKey, CrRes].New(a)
    onerr u.Free(a)
    for round in 0..2 {
        for i in 0..40 {
            k := try CrNewKey(a, i)
            v := CrNewRes(a, i + 100 * round) else {
                k.Free(a)
                fail OutOfMemory
            }
            old := u.Replace(a, k, v) else {
                k.Free(a)
                v.Free(a)
                fail OutOfMemory
            }
            if o := old {
                Assert(round == 1 && o.id == i)
                o.Free(a)
            } else {
                Assert(round == 0)
            }
        }
    }
    Assert(u.Len() == 40)
    var it := u.Iter()
    var n := 0
    for e := it.Next() {
        Assert(e.key.id == n && e.value.id == n + 100)
        n += 1
    }
    Assert(n == 40)
    u.Free(a)
}

test "HeapBTree.Replace fills a tree of linear values that are not Free" (sys: &System) {
    a := sys.heap.Alloc()
    var t := HeapBTree[int, CrTok].New(a)
    for i in 0..300 {
        Assert(CrNone(t.Replace(a, i * 7919 % 1009, CrTok(n: i)).Must()))
    }
    var back := 0
    for i in 0..300 {
        if o := t.Replace(a, i * 7919 % 1009, CrTok(n: 1000 + i)).Must() {
            Assert(CrClose(o) == i)
            back += 1
        }
    }
    Assert(back == 300 && t.Len() == 300)
    l := CrNewLog()
    bad := Cell[bool].New(false)
    t.FreeEach(a, fn(k: int, v: CrTok) {
        if CrClose(v) < 1000 {
            bad.Store(true)
        }
        CrSee(&l, k)
    })
    Assert(l.n.Load() == 300 && l.ordered.Load() && !bad.Load())
    var u := HeapBTree[int, CrTok].New(a)
    for i in 0..300 {
        Assert(CrNone(u.Replace(a, i, CrTok(n: i)).Must()))
    }
    for i in 0..300 {
        if v := u.Remove(&i) {
            Assert(CrClose(v) == i)
        }
    }
    u.FreeEmpty(a)
}

// An arena BTree holds no linear value: HeapBTree's test above covers
// Replace of linear values.
test "BTree.Replace hands back the old value" (sys: &System) {
    var t := BTree[int, int].New(sys.arena)
    for i in 0..500 {
        Assert(t.Replace(i * 7919 % 1009, i).Must().IsNone())
    }
    for i in 0..500 {
        Assert(t.Replace(i * 7919 % 1009, -i).Must().Must() == i)
    }
    k1 := 7919 % 1009
    Assert(t.Len() == 500 && *t.Get(&k1).Must() == -1)
}
