// Why: HPACK's and QPACK's dynamic tables are the same structure, a FIFO
// of (name, value) entries bounded by a size in bytes, and the peer's
// table size decides the memory, so it must be allocated once and never
// grow per field.
//
// What: Table, the dynamic table: New, Free, Insert (Fit then Add),
// SetCap, Evict, the lookups by absolute index (Has, NameLen, ValueLen,
// NameAt, ValueAt, NameIndex, CopyName, CopyValue, NameIs, ValueIs), Ring and
// Whole for views of the byte ring, and EntrySize.
//
// How: a byte ring of max bytes holds the entries' names and values back
// to back, oldest first, and a ring of Ent records holds each entry's
// start and lengths. The bytes in use are the table's size less 32 per
// entry (RFC 7541 4.1), never more than max, so the byte ring cannot
// overrun; an entry may wrap from the end of the ring to its start, so
// every read and write is at most two copies. An entry is named by its
// absolute index, the count of insertions before it (RFC 9204 3.2.4), so
// QPACK uses the same table; HPACK index 62 is the newest entry (RFC 7541
// 2.3.3). Insert and Evict are O(1) per entry plus the bytes copied.

import std.core

// One entry: where its name starts in the byte ring, its lengths, and the
// static-table index of its name (0 for none), kept so a field read from
// the entry needs no name lookup. 16 bytes.
struct Ent {
    at: u32
    name: u32
    value: u32
    index: u8
}

// A chunk of zeros, which New appends to the rings until they reach their
// size: a growth from empty to a power of two is exact (NextCap doubles).
const Zeros: [64]u8 = [0; 64]

// The dynamic table. Linear: it owns two heap buffers.
struct Table {
    // The byte ring, max bytes long.
    bytes: HeapList[u8]
    // The entry ring, max / 32 records long.
    ents: HeapList[Ent]
    // The byte ring's length.
    max: int
    // The capacity in force (RFC 7541 4.2), at most max.
    cap: int
    // The size (RFC 7541 4.1): names and values plus 32 per entry.
    size: int
    // The ring slot of the oldest entry, and the number of entries.
    first: int
    count: int
    // Where the next entry's bytes start in the byte ring.
    head: int
    // The number of insertions ever: the absolute index of the next entry.
    inserted: int
}

// EntrySize is the size of an entry of these lengths (RFC 7541 4.1).
EntrySize(name: int, value: int): int {
    return name + value + EntryOverhead
}

// Table.New is an empty table of capacity max with its rings allocated
// from a, once. max below 0 is 0. O(max); allocates about max + max / 32 ×
// 12 bytes.
Table.New(a: &Alloc, max: int): OutOfMemory!Table {
    m := if max < 0 { 0 } else { max }
    var t := Table(bytes: HeapList[u8].New(a), ents: HeapList[Ent].New(a), max: m, cap: m, size: 0, first: 0, count: 0, head: 0, inserted: 0)
    onerr t.Free(a)
    for t.bytes.Len() < m {
        k := if m - t.bytes.Len() < Zeros.Len() { m - t.bytes.Len() } else { Zeros.Len() }
        try t.bytes.Extend(a, Zeros[0..k])
    }
    slots := m / EntryOverhead
    for t.ents.Len() < slots {
        try t.ents.Push(a, Ent(at: 0, name: 0, value: 0, index: 0))
    }
    return t
}

// Table.Free releases the rings. O(1).
Table.Free(self, a: &Alloc) {
    Table(bytes, ents, ..) := self
    bytes.Free(a)
    ents.Free(a)
}

// Table.Oldest is the absolute index of the oldest entry (Inserted() when
// the table is empty). O(1).
Table.Oldest(&self): int {
    return self.inserted - self.count
}

// Table.Has is whether the entry of absolute index abs is in the table.
// O(1).
Table.Has(&self, abs: int): bool {
    return abs >= self.inserted - self.count && abs < self.inserted
}

// Table.Slot is the entry ring slot of absolute index abs, which must be
// in the table.
Table.Slot(&self, abs: int): int {
    return (self.first + (abs - (self.inserted - self.count))) % self.ents.Len()
}

// Table.Evict drops the oldest entry, when there is one. O(1).
Table.Evict(mut self) {
    if self.count == 0 {
        return
    }
    e := self.ents[self.first]
    self.size -= EntrySize(int(e.name), int(e.value))
    self.first = (self.first + 1) % self.ents.Len()
    self.count -= 1
    if self.count == 0 {
        self.first = 0
        self.head = 0
    }
}

// Table.SetCap makes the capacity c (clamped to [0, max]) and evicts the
// oldest entries until the size fits (RFC 7541 4.3). O(entries evicted).
Table.SetCap(mut self, c: int) {
    self.cap = if c < 0 { 0 } else if c > self.max { self.max } else { c }
    for self.size > self.cap {
        self.Evict()
    }
}

// Table.Insert adds (name, value) as the newest entry, first evicting the
// oldest entries until it fits; an entry larger than the capacity empties
// the table and is not added (RFC 7541 4.4). Neither slice may be a view of
// the table, which the caller's loans already rule out. O(name + value +
// entries evicted); allocates nothing.
Table.Insert(mut self, name: []u8, value: []u8, index: int) {
    if self.Fit(EntrySize(name.Len(), value.Len())) {
        self.Add(name, value, index)
    }
}

// Table.Fit evicts the oldest entries until an entry of size need fits,
// and says whether it does; when need is above the capacity it empties the
// table (RFC 7541 4.4). O(entries evicted).
Table.Fit(mut self, need: int): bool {
    for self.count > 0 && self.size + need > self.cap {
        self.Evict()
    }
    return need <= self.cap
}

// Table.Add adds (name, value) as the newest entry, with index the static
// index of its name; Fit has made room for it. O(name + value).
Table.Add(mut self, name: []u8, value: []u8, index: int) {
    need := EntrySize(name.Len(), value.Len())
    slot := (self.first + self.count) % self.ents.Len()
    self.ents[slot] = Ent(at: Trunc[u32](self.head), name: Trunc[u32](name.Len()), value: Trunc[u32](value.Len()), index: Trunc[u8](index))
    self.head = self.Put(self.head, name)
    self.head = self.Put(self.head, value)
    self.count += 1
    self.size += need
    self.inserted += 1
}

// Table.Put copies b into the byte ring at at, wrapping at the end, and
// returns the position after it. b is at most max bytes.
Table.Put(mut self, at: int, b: []u8): int {
    n := b.Len()
    if n == 0 {
        return at
    }
    ring := self.bytes.ItemsMut()
    first := if n < self.max - at { n } else { self.max - at }
    (mut ring[at..at + first]).CopyFrom(b[0..first])
    if first < n {
        (mut ring[0..n - first]).CopyFrom(b[first..n])
    }
    return (at + n) % self.max
}

// Table.Get copies n bytes from the byte ring at at into out[0..n],
// wrapping at the end.
Table.Get(&self, at: int, n: int, out: mut []u8) {
    if n == 0 {
        return
    }
    ring := self.bytes.Items()
    first := if n < self.max - at { n } else { self.max - at }
    (mut out[0..first]).CopyFrom(ring[at..at + first])
    if first < n {
        (mut out[first..n]).CopyFrom(ring[0..n - first])
    }
}

// Table.Same is whether the n bytes of the byte ring at at equal b.
Table.Same(&self, at: int, n: int, b: []u8): bool {
    if b.Len() != n {
        return false
    }
    if n == 0 {
        return true
    }
    ring := self.bytes.Items()
    first := if n < self.max - at { n } else { self.max - at }
    if !core.BytesEqual(ring, at, b[0..first]) {
        return false
    }
    return first == n || core.BytesEqual(ring, 0, b[first..n])
}

// Table.NameLen and Table.ValueLen are the lengths of the entry of
// absolute index abs, which must be in the table. O(1).
Table.NameLen(&self, abs: int): int {
    return int(self.ents[self.Slot(abs)].name)
}

Table.ValueLen(&self, abs: int): int {
    return int(self.ents[self.Slot(abs)].value)
}

// Table.CopyName and Table.CopyValue copy the name or the value of the
// entry of absolute index abs, which must be in the table, into out, which
// holds at least its length. O(length).
Table.CopyName(&self, abs: int, out: mut []u8) {
    e := self.ents[self.Slot(abs)]
    self.Get(int(e.at), int(e.name), out)
}

Table.CopyValue(&self, abs: int, out: mut []u8) {
    e := self.ents[self.Slot(abs)]
    self.Get((int(e.at) + int(e.name)) % self.max, int(e.value), out)
}

// Table.NameIs and Table.ValueIs compare the name or the value of the
// entry of absolute index abs, which must be in the table, with b. O(b).
Table.NameIs(&self, abs: int, b: []u8): bool {
    e := self.ents[self.Slot(abs)]
    return self.Same(int(e.at), int(e.name), b)
}

Table.ValueIs(&self, abs: int, b: []u8): bool {
    e := self.ents[self.Slot(abs)]
    return self.Same((int(e.at) + int(e.name)) % self.max, int(e.value), b)
}

// Table.NameAt and Table.ValueAt are where the name and the value of the
// entry of absolute index abs, which must be in the table, start in the
// byte ring. O(1).
Table.NameAt(&self, abs: int): int {
    return int(self.ents[self.Slot(abs)].at)
}

Table.ValueAt(&self, abs: int): int {
    e := self.ents[self.Slot(abs)]
    return (int(e.at) + int(e.name)) % self.max
}

// Table.NameIndex is the static-table index of the name of the entry of
// absolute index abs, which must be in the table, or 0. O(1).
Table.NameIndex(&self, abs: int): int {
    return int(self.ents[self.Slot(abs)].index)
}

// Table.Whole is whether the n bytes at at lie in the byte ring without
// wrapping past its end, so Ring can view them. O(1).
Table.Whole(&self, at: int, n: int): bool {
    return n <= self.max - at
}

// Table.Ring is a view of the n bytes of the byte ring at at, which Whole
// has checked. O(1).
Table.Ring(&self, at: int, n: int): []u8 {
    return self.bytes.Items()[at..at + n]
}
