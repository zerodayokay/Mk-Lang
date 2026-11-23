// Why: tests and profiling need to know how much memory a piece of code
// holds, and whether it gives everything back.
//
// What: Counting, an allocator that wraps another and counts the bytes and
// allocations live through it and the peak: New, Alloc, Bytes, Live, Peak.
//
// How: each table entry passes the request to inner and, on success,
// updates the counts, which sit in Cells because entries run under shared
// loans. The identity and Cell rules are described in mem.mk's header.

import std.core
import std.raw

// bytes and live count what went through this allocator and was not given
// back; peak is the most bytes has been. Each changes under shared loans
// (an entry runs while containers hold `&Alloc from self`), so each is a
// Cell. &Alloc is neither Send nor Share, so one task at a time
// reaches a Counting: plain loads and stores suffice, whatever inner is.
/// An allocator that passes every request to another and counts the bytes
/// and allocations live through it, and the peak.
export mut view struct Counting {
    own: Own
    inner: &Alloc
    nbytes: Cell[int]
    nlive: Cell[int]
    npeak: Cell[int]
}

const CountingTable = core.AllocTable(alloc: CountingAllocEntry, resize: CountingResizeEntry, free: CountingFreeEntry)

/// A Counting over inner, with every count zero. The result borrows inner.
/// O(1); allocates nothing.
export Counting.New(inner: &Alloc): Counting {
    return Counting(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&CountingTable))), inner: inner, nbytes: Cell[int].New(0), nlive: Cell[int].New(0), npeak: Cell[int].New(0))
}

/// The allocator that counts and passes each request on to inner; it
/// fails exactly when inner does. O(1) per request on top of inner's.
export Counting.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[Counting, u8](raw.Of(self)), raw.Of(&CountingTable))
    }
    return OwnAlloc(&self.own)
}

/// The bytes allocated through self and not freed: the sizes callers
/// asked for, not what inner rounded them up to. O(1).
export Counting.Bytes(&self): int {
    return self.nbytes.Load()
}

/// The allocations made through self and not freed. Zero at the end of a
/// test means nothing leaked; a negative count means something freed
/// through self a block that self did not allocate. O(1).
export Counting.Live(&self): int {
    return self.nlive.Load()
}

/// The most Bytes() has been since New. O(1).
export Counting.Peak(&self): int {
    return self.npeak.Load()
}

// CountingOf is the Counting whose Alloc.ctx is ctx.
CountingOf(ctx: *u8): &Counting {
    unsafe "ctx is the address of a live Counting, stored by Counting.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, Counting](ctx)
    }
}

// Grow adds d bytes (d may be negative) and n allocations. Every count is
// bounded by what inner really holds, which fits the address space, so the
// checked operators never trap on a correct caller.
Grow(c: &Counting, d: int, n: int) {
    b := c.nbytes.Load() + d
    c.nbytes.Store(b)
    c.nlive.Store(c.nlive.Load() + n)
    if b > c.npeak.Load() {
        c.npeak.Store(b)
    }
}

CountingAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    c := CountingOf(ctx)
    p := core.AllocBytes(c.inner, size, align)
    if !raw.IsNull(p) {
        Grow(c, size, 1)
    }
    return p
}

CountingResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    c := CountingOf(ctx)
    if !core.ResizeBytes(c.inner, p, old, new, align) {
        return false
    }
    Grow(c, new - old, 0)
    return true
}

CountingFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    c := CountingOf(ctx)
    core.FreeBytes(c.inner, p, size, align)
    Grow(c, 0 - size, -1)
}
