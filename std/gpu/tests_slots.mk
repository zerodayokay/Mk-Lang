// Why: handle tables must detect stale generations, grow across 64-entry
// chunks, track ref-counts, and free without leaking heap memory.
//
// What: unit tests for slots.mk (alloc, stale generation, chunk growth,
// ref-count defer, FreeAll).
//
// How: runs against the counting heap in test profile.

test "slots alloc, multi-chunk growth, generation bump, and ref-counts" (sys: &System) {
    a := sys.heap.Alloc()
    var tbl := SlotTable[int].New(a)
    defer tbl.FreeAll(a, fn(v: int) { _ = v })

    var slots: [150]u32 = [0; 150]
    var gens: [150]u32 = [0; 150]
    for i in 0..150 {
        var s: u32 = 0
        var g: u32 = 0
        match tbl.Alloc(a, i * 10) {
            .Ok(slot, gen_) {
                s = slot
                g = gen_
            }
            .Oom(val: _) { fail OutOfMemory }
        }
        Assert(s != 0 && g >= 1)
        slots[i] = s
        gens[i] = g
        Assert(tbl.IsLive(s, g))
        Assert(*tbl.Get(s) == i * 10)
    }
    Assert(tbl.LiveCount() == 150)

    // Release middle slot, verify stale generation, then reallocate and verify generation bumped.
    Assert(tbl.MarkUserClosed(slots[5], gens[5]))
    Assert(!tbl.IsLive(slots[5], gens[5]))
    old := tbl.Release(a, slots[5])
    match old {
        some(v) { Assert(v == 50) }
        none { Assert(false) }
    }
    Assert(tbl.LiveCount() == 149)

    var rs: u32 = 0
    var rg: u32 = 0
    match tbl.Alloc(a, 999) {
        .Ok(slot, gen_) {
            rs = slot
            rg = gen_
        }
        .Oom(val: _) { fail OutOfMemory }
    }
    Assert(rs == slots[5])
    Assert(rg == gens[5] + 1)
    Assert(!tbl.IsLive(slots[5], gens[5]))
    Assert(tbl.IsLive(rs, rg))

    // Ref-count prevents early release until DecRef reaches 0.
    tbl.IncRef(slots[10])
    Assert(!tbl.MarkUserClosed(slots[10], gens[10]))
    Assert(!tbl.IsLive(slots[10], gens[10]))
    Assert(tbl.IsAllocated(slots[10], gens[10]))
    Assert(tbl.DecRef(slots[10]))
    _ = tbl.Release(a, slots[10])
}
