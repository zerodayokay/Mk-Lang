// verdict: reject E0701
// rule: MKH-596
// also: R2
// note: an arena container is not linear and ends with its arena, which frees what it holds without consuming it, so every call that moves a value into arena memory refuses a linear one; HeapList, HeapDeque, HeapMap, HeapBTree and Box hold one and end through FreeEach or FreeEmpty
import std.coll
import std.mem

linear struct Prio {
    n: int
}

impl Ord for Prio {
    Less(&self, other: &Prio): bool {
        return self.n < other.n
    }
}

InList(l: mut List[Res], r: Res, s: Res) {
    l.Push(r) else { // <- E0701
        Panic("out of memory")
    }
    l.Insert(0, s) else { // <- E0701
        Panic("out of memory")
    }
}

InDeque(d: mut coll.Deque[Res], r: Res, s: Res) {
    d.Push(r) else { // <- E0701
        Panic("out of memory")
    }
    d.PushFront(s) else { // <- E0701
        Panic("out of memory")
    }
}

InQueue(q: mut coll.PriorityQueue[Prio], p: Prio) {
    q.Push(p) else { // <- E0701
        Panic("out of memory")
    }
}

InPool(p: mut mem.Pool[Res], r: Res) {
    h := p.Add(r) else { // <- E0701
        Panic("out of memory")
    }
    _ = h
}

InMap(m: mut Map[int, Res], t: mut coll.BTree[int, Res], r: Res, s: Res) {
    o := m.Replace(1, r) else { // <- E0701
        Panic("out of memory")
    }
    if old := o {
        old.Close()
    }
    o2 := t.Replace(1, s) else { // <- E0701
        Panic("out of memory")
    }
    if old := o2 {
        old.Close()
    }
}

InArena(a: &Arena, r: Res) {
    x := a.Put(r) else { // <- E0701
        Panic("out of memory")
    }
    x.id = 1
}
