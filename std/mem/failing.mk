// Why: code that handles OutOfMemory is only known to be right once every
// allocation it makes has been seen to fail, and no real allocator fails
// on demand.
//
// What: Failing, an allocator that wraps another and fails the requests a
// test picks: New fails exactly one, NewFrom that one and every later one;
// Alloc, Requests and Failed.
//
// How: each table entry numbers the requests that can fail, from zero (an
// allocation, or a resize to more bytes), and refuses the picked ones
// without asking inner; everything else passes through. The counts sit in
// Cells because entries run under shared loans. The identity and Cell rules
// are described in mem.mk's header.
//
// A test runs its code once per request number, from 0 up, until a run
// meets no failure: then every allocation of the code has failed once.

import std.core
import std.raw

// The requests numbered at to last, inclusive, fail. nreqs counts the
// requests seen and nfails the ones failed. Both change under shared loans,
// so each is a Cell. &Alloc is neither Send nor Share, so one task at a
// time reaches a Failing: plain loads and stores suffice.
/// An allocator that passes requests to another and fails the ones a test
/// picks by number, to drive code down its OutOfMemory paths.
export mut view struct Failing {
    own: Own
    inner: &Alloc
    at: int
    last: int
    nreqs: Cell[int]
    nfails: Cell[int]
}

const FailingTable = core.AllocTable(alloc: FailingAllocEntry, resize: FailingResizeEntry, free: FailingFreeEntry)

// FailingLast is NewFrom's last: no run makes that many requests.
const FailingLast = 9223372036854775807

/// A Failing over inner that fails exactly one request: the one numbered
/// at, counting from zero the allocations and the resizes to more bytes
/// made through it. A negative at fails none. The result borrows inner.
/// O(1); allocates nothing.
export Failing.New(inner: &Alloc, at: int): Failing {
    return FailingMake(inner, at, at)
}

/// A Failing over inner that fails the request numbered at and every one
/// after it, as an exhausted allocator does. A negative at fails none. The
/// result borrows inner. O(1); allocates nothing.
export Failing.NewFrom(inner: &Alloc, at: int): Failing {
    return FailingMake(inner, at, FailingLast)
}

// A negative at is an empty range, so nothing fails.
FailingMake(inner: &Alloc, at: int, last: int): Failing {
    var lo := at
    var hi := last
    if at < 0 {
        lo = 0
        hi = -1
    }
    return Failing(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&FailingTable))), inner: inner, at: lo, last: hi, nreqs: Cell[int].New(0), nfails: Cell[int].New(0))
}

/// The allocator that fails the picked requests with OutOfMemory, without
/// asking inner, and passes every other one on. A free, and a resize to
/// the same or fewer bytes, is never failed and never counted. O(1) per
/// request on top of inner's.
export Failing.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[Failing, u8](raw.Of(self)), raw.Of(&FailingTable))
    }
    return OwnAlloc(&self.own)
}

/// The requests made through self that could have failed: the allocations
/// and the resizes to more bytes, the failed ones included. O(1).
export Failing.Requests(&self): int {
    return self.nreqs.Load()
}

/// The requests self failed. Zero after a run means the run made no
/// request with the number picked, so a loop over request numbers stops
/// there. O(1).
export Failing.Failed(&self): int {
    return self.nfails.Load()
}

// FailingOf is the Failing whose Alloc.ctx is ctx.
FailingOf(ctx: *u8): &Failing {
    unsafe "ctx is the address of a live Failing, stored by Failing.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, Failing](ctx)
    }
}

// Picked numbers one request and says whether it is one to fail. The count
// wraps instead of trapping: no run makes 2^63 requests.
Picked(f: &Failing): bool {
    n := f.nreqs.Load()
    f.nreqs.Store(n +% 1)
    if n < f.at || n > f.last {
        return false
    }
    f.nfails.Store(f.nfails.Load() +% 1)
    return true
}

FailingAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    f := FailingOf(ctx)
    if Picked(f) {
        return raw.Null[u8]()
    }
    return core.AllocBytes(f.inner, size, align)
}

// Only a resize to more bytes is a request that can fail: a container
// treats a refused shrink as nothing to do, so failing one would test
// nothing.
FailingResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    f := FailingOf(ctx)
    if new > old && Picked(f) {
        return false
    }
    return core.ResizeBytes(f.inner, p, old, new, align)
}

FailingFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    core.FreeBytes(FailingOf(ctx).inner, p, size, align)
}
