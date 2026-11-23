// Why: the process Heap is reached through a core.AllocTable, and a test
// build needs a second table that counts live allocations to find leaks.
//
// What: HeapTable, which calls the heap directly; CountingTable, swapped in
// by TrackLeaks in a test build, which also counts live allocations and
// bytes; and HeapInit.
//
// How: each entry forwards to HeapAlloc, HeapResize or HeapFree; the
// counting entries add to Rt.live_count and Rt.live_bytes. ctx is unused.

import std.core
import std.raw
import std.sys

/// HeapTable is the process Heap's table: each entry calls the heap.
export const HeapTable = core.AllocTable(alloc: HeapAllocEntry, resize: HeapResizeEntry, free: HeapFreeEntry)

/// CountingTable is HeapTable plus the live-allocation counters of
/// LiveAllocations, two atomic adds per call.
export const CountingTable = core.AllocTable(alloc: CountingAlloc, resize: CountingResize, free: CountingFree)

HeapAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    return HeapAlloc(size, align)
}

HeapResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return HeapResize(p, old, new, align)
}

HeapFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    HeapFree(p, size, align)
}

CountingAlloc(ctx: *u8, size: int, align: int): *u8 {
    p := HeapAlloc(size, align)
    if !raw.IsNull(p) {
        // Edge: none needed; counters summed at Finish, after every task ended.
        _ = Rt.live_count.Add(1)
        _ = Rt.live_bytes.Add(i64(size))
    }
    return p
}

CountingResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    if HeapResize(p, old, new, align) {
        // Edge: none needed; a counter (as CountingAlloc).
        _ = Rt.live_bytes.Add(i64(new) - i64(old))
        return true
    }
    return false
}

CountingFree(ctx: *u8, p: *u8, size: int, align: int) {
    // Edge: none needed; a counter (as CountingAlloc).
    _ = Rt.live_count.Add(-1)
    _ = Rt.live_bytes.Add(-i64(size))
    HeapFree(p, size, align)
}

/// HeapInit makes the process Heap over HeapTable (during Start).
/// O(1) besides MagInit's mapping; allocates nothing from the Heap.
export HeapInit() {
    unsafe "Init ran, so R() is the page it mapped, which lives until exit; HeapTable is a constant" {
        (*R()).heap = core.MakeHeap(raw.Null[u8](), raw.Of(&HeapTable))
    }
    // Without magazines every allocation takes the shared lock; nothing
    // else changes.
    _ = MagInit()
    // Huge blocks reserve room to grow in place unless the address space
    // is limited (HugeSpareBudget).
    if !sys.AddressLimited() {
        unsafe "the RtState page, before any task exists" {
            (*HS()).spare.Store(HugeSpareBudget)
        }
    }
}
