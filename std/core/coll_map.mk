// Why: hash maps and sets that live in an arena need no free and no
// allocator check; they share one Swiss table with the heap maps.
//
// What: Map[K, V] (New, NewKeyed, Get, GetMut, Set, Replace, Ensure, Has,
// Remove, Len, Iter), MapIter, which also walks a HeapMap, and Set[T], a Map
// with no values.
//
// How: thin wrappers over coll_table.mk's Table, whose arrays come from the
// arena and are abandoned to it when the table grows. New hashes under a fixed
// function, so the order repeats in every run and at compile time; NewKeyed
// uses SipHash-1-3 under a secret key. Keys and values a call would drop must
// not be linear (E0701 at the call).

import std.raw

/// A hash map in an arena, a Swiss table. When the table grows, the old one
/// is left to the arena. Iteration order is unspecified; it depends only on
/// the key type, the insertions and removals, and the HashKey if there is
/// one.
export view struct Map[K: Hash + Eq, V] {
    t: Table[K, V]
    alloc: &Alloc
}

/// An iterator over a Map's or HeapMap's entries, in the map's iteration
/// order. The map stays borrowed while the iterator lives.
export view struct MapIter[K, V] {
    map: &Table[K, V]
    i: int
}

/// A hash set in an arena: a Map with no values.
export view struct Set[T: Hash + Eq] {
    t: Table[T, [0]u8]
    alloc: &Alloc
}

/// An empty map in a. It hashes with a fixed function, so its order is the
/// same in every run and at compile time. The function, and so the order,
/// may change between releases. Whoever picks the keys can make them collide
/// and each operation O(n), so make a map of keys from untrusted input with
/// NewKeyed. O(1); allocates nothing until the first insert.
export Map[K, V].New(a: &Arena): Map[K, V] {
    return Map[K, V](t: EmptyTable[K, V](), alloc: a.Alloc())
}

/// As New, but hashing with SipHash-1-3 under key. While key is secret, an
/// attacker cannot choose keys that collide. O(1); allocates nothing.
export Map[K, V].NewKeyed(a: &Arena, key: HashKey): Map[K, V] {
    return Map[K, V](t: EmptyTableKeyed[K, V](key), alloc: a.Alloc())
}

/// The value stored for k, or none. Expected O(1); allocates nothing.
export Map[K, V].Get(&self, k: &K): ?&V {
    i := TableFind(&self.t, TableHash(&self.t, k), k)
    if i < 0 {
        return none
    }
    return some(SlotRef(ValueAt(self.t.slots, i), 0))
}

/// The value stored for k, writable in place, or none. Expected O(1); allocates
/// nothing.
export Map[K, V].GetMut(mut self, k: &K): ?mut V {
    i := TableFind(&self.t, TableHash(&self.t, k), k)
    if i < 0 {
        return none
    }
    return some(SlotMut(ValueAt(self.t.slots, i), 0))
}

/// Sets k's value to v. When k is present the stored key stays, and the
/// given k and the old value are dropped, so a call with a linear K or V is
/// E0701. OutOfMemory when the table cannot grow for a new key, and then the
/// caller still owns k and v. Expected O(1); growth is amortized O(1) and
/// allocates from the arena.
export Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void {
    hash := TableHash(&self.t, &k)
    r := TableLookup(&self.t, hash, &k)
    if r.at >= 0 {
        i := r.at
        if raw.IsLinear[K]() || raw.IsLinear[V]() {
            raw.Fail(8, "Map.Set would drop a linear value")
        }
        Discard(SlotRead(ValueAt(self.t.slots, i), 0))
        SlotWrite(ValueAt(self.t.slots, i), 0, v)
        Discard(k)
        return
    }
    j := TablePlace(mut self.t, self.alloc, false, hash, r.free)
    if j < 0 {
        fail OutOfMemory
    }
    SlotWrite(KeyAt(self.t.slots, j), 0, k)
    SlotWrite(ValueAt(self.t.slots, j), 0, v)
}

/// Sets k's value to v and returns the old one, or none when k was absent.
/// When k is present the stored key stays and the given k is dropped, so a
/// call with a linear K is E0701. V may be linear, since the old value comes
/// back. OutOfMemory as Set. Expected O(1); growth is amortized O(1) and
/// allocates from the arena.
export Map[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V {
    hash := TableHash(&self.t, &k)
    r := TableLookup(&self.t, hash, &k)
    if r.at >= 0 {
        if raw.IsLinear[K]() {
            raw.Fail(8, "Map.Replace would drop a linear key")
        }
        Discard(k)
        return some(Replace(SlotMut(ValueAt(self.t.slots, r.at), 0), v))
    }
    j := TablePlace(mut self.t, self.alloc, false, hash, r.free)
    if j < 0 {
        fail OutOfMemory
    }
    SlotWrite(KeyAt(self.t.slots, j), 0, k)
    SlotWrite(ValueAt(self.t.slots, j), 0, v)
    return none
}

/// The value for k, writable, inserting v first when k is absent. When k is
/// present the given k and v are dropped, so a call with a linear K or V is
/// E0701. OutOfMemory as Set. Expected O(1); growth is amortized O(1) and
/// allocates from the arena.
export Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self {
    hash := TableHash(&self.t, &k)
    r := TableLookup(&self.t, hash, &k)
    if r.at >= 0 {
        if raw.IsLinear[K]() || raw.IsLinear[V]() {
            raw.Fail(8, "Map.Ensure would drop a linear value")
        }
        Discard(k)
        Discard(v)
        return SlotMut(ValueAt(self.t.slots, r.at), 0)
    }
    j := TablePlace(mut self.t, self.alloc, false, hash, r.free)
    if j < 0 {
        fail OutOfMemory
    }
    SlotWrite(KeyAt(self.t.slots, j), 0, k)
    SlotWrite(ValueAt(self.t.slots, j), 0, v)
    return SlotMut(ValueAt(self.t.slots, j), 0)
}

/// Whether k is present. Expected O(1); allocates nothing.
export Map[K, V].Has(&self, k: &K): bool {
    return TableFind(&self.t, TableHash(&self.t, k), k) >= 0
}

/// Removes k and returns its value, or none when k is absent. The stored key
/// is dropped, so a call with a linear K is E0701. Expected O(1); the arena
/// keeps the memory.
export Map[K, V].Remove(mut self, k: &K): ?V {
    i := TableFind(&self.t, TableHash(&self.t, k), k)
    if i < 0 {
        return none
    }
    if raw.IsLinear[K]() {
        raw.Fail(8, "Map.Remove would drop a linear value")
    }
    v := SlotRead(ValueAt(self.t.slots, i), 0)
    Discard(SlotRead(KeyAt(self.t.slots, i), 0))
    TableErase(mut self.t, i)
    return some(v)
}

/// The number of entries. O(1).
export Map[K, V].Len(&self): int {
    return self.t.Count()
}

/// An iterator over the entries, in the map's unspecified order. O(1); a full
/// walk is O(capacity).
export Map[K, V].Iter(&self): MapIter[K, V] {
    return MapIter[K, V](map: &self.t, i: 0)
}

/// The next entry, or none after the last; allocates nothing. A full walk is
/// O(capacity): one 8-byte group read per 8 slots. A table never shrinks,
/// since removal only marks slots, so one call on a map that removals left
/// sparse may read up to capacity / 8 groups.
export MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map {
    i := TableNext(self.map, self.i)
    if i < 0 {
        self.i = self.map.cap
        return none
    }
    self.i = i + 1
    return some((key: SlotRef(KeyAt(self.map.slots, i), 0), value: SlotRef(ValueAt(self.map.slots, i), 0)))
}

/// An empty set in a. It hashes with a fixed function, so its order is the
/// same in every run and at compile time. The function may change between
/// releases, and it is not safe for keys from untrusted input: use NewKeyed.
/// O(1); allocates nothing until the first Add.
export Set[T].New(a: &Arena): Set[T] {
    return Set[T](t: EmptyTable[T, [0]u8](), alloc: a.Alloc())
}

/// As New, but hashing with SipHash-1-3 under key, for members from
/// untrusted input. O(1); allocates nothing.
export Set[T].NewKeyed(a: &Arena, key: HashKey): Set[T] {
    return Set[T](t: EmptyTableKeyed[T, [0]u8](key), alloc: a.Alloc())
}

/// Adds x and returns true when it was not a member. When it was, the stored
/// member stays and x is dropped, so a call with a linear T is E0701.
/// OutOfMemory when the table cannot grow, and then the caller still owns x.
/// Expected O(1); growth is amortized O(1) and allocates from the arena.
export Set[T].Add(mut self, x: T into self): OutOfMemory!bool {
    hash := TableHash(&self.t, &x)
    r := TableLookup(&self.t, hash, &x)
    if r.at >= 0 {
        if raw.IsLinear[T]() {
            raw.Fail(8, "Set.Add would drop a linear value")
        }
        Discard(x)
        return false
    }
    j := TablePlace(mut self.t, self.alloc, false, hash, r.free)
    if j < 0 {
        fail OutOfMemory
    }
    SlotWrite(KeyAt(self.t.slots, j), 0, x)
    return true
}

/// Whether x is a member. Expected O(1); allocates nothing.
export Set[T].Has(&self, x: &T): bool {
    return TableFind(&self.t, TableHash(&self.t, x), x) >= 0
}

/// Removes x and returns whether it was a member. The stored member is
/// dropped, so a call with a linear T is E0701. Expected O(1); the arena
/// keeps the memory.
export Set[T].Remove(mut self, x: &T): bool {
    i := TableFind(&self.t, TableHash(&self.t, x), x)
    if i < 0 {
        return false
    }
    if raw.IsLinear[T]() {
        raw.Fail(8, "Set.Remove would drop a linear value")
    }
    Discard(SlotRead(KeyAt(self.t.slots, i), 0))
    TableErase(mut self.t, i)
    return true
}
