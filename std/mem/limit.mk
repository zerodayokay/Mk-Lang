// Why: a program that promises to stay under a memory budget needs an
// allocator that refuses the request that would pass it, without setting
// the whole budget aside in a buffer first.
//
// What: Limit, an allocator that wraps another and refuses whatever would
// take the bytes live through it past a limit: New, Alloc, Bytes, Left.
//
// How: each table entry checks the request against the bytes left, passes
// it to inner and, on success, updates the count, which sits in a Cell
// because entries run under shared loans. The identity and Cell rules are
// described in mem.mk's header. Blocks freed in any order give their bytes
// back, since inner keeps the memory: the limit bounds what is live, where
// a Fixed bounds what was ever handed out between two Resets.

import std.core
import std.raw

// used counts the bytes callers asked for and have not given back. It
// changes under shared loans (an entry runs while containers hold `&Alloc
// from self`), so it is a Cell. &Alloc is neither Send nor Share, so one
// task at a time reaches a Limit: plain loads and stores suffice.
/// An allocator that passes requests to another while the bytes live
/// through it stay within a limit, and refuses the request that would
/// pass it.
export mut view struct Limit {
    own: Own
    inner: &Alloc
    max: int
    used: Cell[int]
}

const LimitTable = core.AllocTable(alloc: LimitAllocEntry, resize: LimitResizeEntry, free: LimitFreeEntry)

/// A Limit of max bytes over inner, with nothing counted yet; a max below
/// zero is zero, which refuses every request of one byte or more. The
/// result borrows inner. O(1); allocates nothing.
export Limit.New(inner: &Alloc, max: int): Limit {
    var m := max
    if m < 0 {
        m = 0
    }
    return Limit(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&LimitTable))), inner: inner, max: m, used: Cell[int].New(0))
}

/// The allocator that passes each request on to inner unless it would
/// take Bytes() past the limit: then it fails with OutOfMemory and inner
/// is not asked. It also fails whenever inner does. O(1) per request on
/// top of inner's.
export Limit.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[Limit, u8](raw.Of(self)), raw.Of(&LimitTable))
    }
    return OwnAlloc(&self.own)
}

/// The bytes allocated through self and not freed: the sizes callers
/// asked for, not what inner rounded them up to. O(1).
export Limit.Bytes(&self): int {
    return self.used.Load()
}

/// The bytes a request may still take: the limit less Bytes(). O(1).
export Limit.Left(&self): int {
    return self.max -% self.used.Load()
}

// LimitOf is the Limit whose Alloc.ctx is ctx.
LimitOf(ctx: *u8): &Limit {
    unsafe "ctx is the address of a live Limit, stored by Limit.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, Limit](ctx)
    }
}

// A request fits when size <= max - used. used is at most max on a correct
// caller, and the wrapping operators keep an incorrect one (a free of a
// block this Limit did not allocate drives used below zero) from trapping.
// A negative size is refused here, so inner never sees it.
LimitAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    l := LimitOf(ctx)
    used := l.used.Load()
    if size < 0 || size > l.max -% used {
        return raw.Null[u8]()
    }
    p := core.AllocBytes(l.inner, size, align)
    if !raw.IsNull(p) {
        l.used.Store(used +% size)
    }
    return p
}

// A resize is charged the difference: growing by more than what is left is
// refused before inner is asked, and shrinking gives bytes back. A refused
// resize changes nothing.
LimitResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    l := LimitOf(ctx)
    used := l.used.Load()
    d := new -% old
    if new < 0 || d > l.max -% used {
        return false
    }
    if !core.ResizeBytes(l.inner, p, old, new, align) {
        return false
    }
    l.used.Store(used +% d)
    return true
}

LimitFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    l := LimitOf(ctx)
    core.FreeBytes(l.inner, p, size, align)
    l.used.Store(l.used.Load() -% size)
}
