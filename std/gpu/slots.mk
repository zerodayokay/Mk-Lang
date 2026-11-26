// Why: GPU resource handles are compact (slot, generation, device serial)
// triples that must never alias a freed slot or dangle after reallocation.
//
// What: SlotTable[T] — generation-checked, ref-counted handle table with
// deferred release support and O(1) lookup.
//
// How: plain metadata indexed by slot (reserving slot 0 as invalid sentinel)
// paired with dense HeapList[T] storage and slot back-indices.

struct SlotMeta {
    gen_: u32
    occupied: bool
    user_alive: bool
    refs: u32
    last_use: u64
    data_idx: int
}

enum SlotAllocRes[T] {
    Ok(slot: u32, gen_: u32)
    Oom(val: T)
}

struct SlotTable[T] {
    meta: HeapList[SlotMeta]
    items: HeapList[T]
    item_slots: HeapList[u32]
    free: HeapList[u32]
    count: u32
    live: u32
}

SlotTable[T].New(a: &Alloc): SlotTable[T] {
    return SlotTable[T](
        meta: HeapList[SlotMeta].New(a),
        items: HeapList[T].New(a),
        item_slots: HeapList[u32].New(a),
        free: HeapList[u32].New(a),
        count: 1,
        live: 0,
    )
}

SlotTable[T].Alloc(mut self, a: &Alloc, val: T into self): SlotAllocRes[T] {
    reusing := self.free.Len() > 0
    slot := if reusing { self.free[self.free.Len() - 1] } else { self.count }
    if !reusing {
        for self.meta.Len() <= int(slot) {
            self.meta.Push(a, SlotMeta(gen_: 1, occupied: false, user_alive: false, refs: 0, last_use: 0, data_idx: -1)) else {
                return SlotAllocRes.Oom(val: val)
            }
        }
    }
    self.item_slots.Push(a, slot) else {
        return SlotAllocRes.Oom(val: val)
    }
    data_idx := self.items.Len()
    self.items.Push(a, val) else {
        _ = self.item_slots.Pop()
        return SlotAllocRes.Oom(val: val)
    }
    if reusing {
        _ = self.free.Pop()
    } else {
        self.count += 1
    }
    self.meta.ItemsMut()[int(slot)].occupied = true
    self.meta.ItemsMut()[int(slot)].user_alive = true
    self.meta.ItemsMut()[int(slot)].refs = 0
    self.meta.ItemsMut()[int(slot)].last_use = 0
    self.meta.ItemsMut()[int(slot)].data_idx = data_idx
    self.live += 1
    return SlotAllocRes.Ok(slot: slot, gen_: self.meta[int(slot)].gen_)
}

SlotTable[T].IsLive(&self, slot: u32, gen_: u32): bool {
    if slot == 0 || slot >= self.count {
        return false
    }
    m := &self.meta[int(slot)]
    return m.occupied && m.user_alive && m.gen_ == gen_
}

SlotTable[T].IsAllocated(&self, slot: u32, gen_: u32): bool {
    if slot == 0 || slot >= self.count {
        return false
    }
    m := &self.meta[int(slot)]
    return m.occupied && m.gen_ == gen_
}

SlotTable[T].Get(&self, slot: u32): &T from self {
    idx := self.meta[int(slot)].data_idx
    if idx < 0 {
        Panic("gpu: slot table empty cell")
    }
    return &self.items[idx]
}

SlotTable[T].GetMut(mut self, slot: u32): mut T from self {
    idx := self.meta[int(slot)].data_idx
    if idx < 0 {
        Panic("gpu: slot table empty cell")
    }
    return mut self.items.ItemsMut()[idx]
}

SlotTable[T].LastUse(&self, slot: u32): u64 {
    if slot == 0 || slot >= self.count {
        return 0
    }
    return self.meta[int(slot)].last_use
}

SlotTable[T].StampUse(mut self, slot: u32, serial: u64) {
    if slot == 0 || slot >= self.count {
        return
    }
    if serial > self.meta[int(slot)].last_use {
        self.meta.ItemsMut()[int(slot)].last_use = serial
    }
}

SlotTable[T].IncRef(mut self, slot: u32) {
    self.meta.ItemsMut()[int(slot)].refs += 1
}

// DecRef decrements refs and reports whether the slot is now eligible for
// release (user closed and refs == 0).
SlotTable[T].DecRef(mut self, slot: u32): bool {
    if self.meta[int(slot)].refs > 0 {
        self.meta.ItemsMut()[int(slot)].refs -= 1
    }
    m := &self.meta[int(slot)]
    return !m.user_alive && m.refs == 0 && m.occupied
}

// MarkUserClosed marks the user handle dead; returns true if refs == 0 so the
// slot can be queued for release immediately.
SlotTable[T].MarkUserClosed(mut self, slot: u32, gen_: u32): bool {
    if !self.IsLive(slot, gen_) {
        return false
    }
    self.meta.ItemsMut()[int(slot)].user_alive = false
    return self.meta[int(slot)].refs == 0
}

// Release takes out the slot payload, bumps its generation, and pushes the
// slot onto the free list.
SlotTable[T].Release(mut self, a: &Alloc, slot: u32): ?T {
    if slot == 0 || slot >= self.count {
        return none
    }
    idx := self.meta[int(slot)].data_idx
    if !self.meta[int(slot)].occupied || idx < 0 {
        return none
    }
    old := self.items.Remove(idx)
    _ = self.item_slots.Remove(idx)
    for k in idx..self.item_slots.Len() {
        self.meta.ItemsMut()[int(self.item_slots[k])].data_idx = k
    }
    self.meta.ItemsMut()[int(slot)].occupied = false
    self.meta.ItemsMut()[int(slot)].user_alive = false
    self.meta.ItemsMut()[int(slot)].refs = 0
    self.meta.ItemsMut()[int(slot)].last_use = 0
    self.meta.ItemsMut()[int(slot)].data_idx = -1
    ng := self.meta[int(slot)].gen_ + 1
    self.meta.ItemsMut()[int(slot)].gen_ = if ng == 0 { u32(1) } else { ng }
    if self.live > 0 {
        self.live -= 1
    }
    self.free.Push(a, slot) else {}
    return some(old)
}

SlotTable[T].LiveCount(&self): int {
    return int(self.live)
}

SlotTable[T].DrainPop(mut self, a: &Alloc): ?T {
    _ = a
    return self.items.Pop()
}

SlotTable[T].FreeEmpty(self, a: &Alloc) {
    SlotTable(meta, items, item_slots, free, count: _, live: _) := self
    meta.Free(a)
    item_slots.Free(a)
    free.Free(a)
    items.FreeEmpty(a)
}

SlotTable[T].FreeAll(self, a: &Alloc, const release: fn(T)) {
    SlotTable(meta, items: items0, item_slots, free, count: _, live: _) := self
    meta.Free(a)
    item_slots.Free(a)
    free.Free(a)
    var items := items0
    for items.Len() > 0 {
        release(items.Pop().Must())
    }
    items.FreeEmpty(a)
}
