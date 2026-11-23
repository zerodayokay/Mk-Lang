// Why: most containers stay small, and a small one fits a buffer on the
// stack; but a Fixed alone fails the rare large one, and the heap alone
// pays a heap allocation for every small one.
//
// What: Fallback, an allocator that serves requests from a caller's buffer
// while they fit and from another allocator once they do not: New, Alloc
// and Reset.
//
// How: the buffer is a Fixed inside the Fallback, reached through Fixed's
// own table entries. A block is told apart by its address: one inside the
// buffer goes back to the Fixed, any other to inner. That needs every block
// of inner to hold a byte, which then lies outside the buffer; a zero-byte
// block of inner could sit anywhere, at the buffer's first byte too. So a
// zero-byte request never comes from the buffer, where the Fixed could
// place it at the buffer's end, an address inner may own, and inner is
// asked for one byte in its place (InnerSize). The identity and Cell rules
// are described in mem.mk's header.

import std.core
import std.raw

// lim is the buffer's usable bytes (0 when the Fixed refuses everything):
// a block of the Fixed starts at an offset below it.
/// An allocator that serves a request from a caller's buffer when it fits
/// there, and from another allocator when it does not. A container that
/// stays small then never reaches that allocator, and one that outgrows
/// the buffer still works.
///
/// The buffer is a Fixed: only its last block is freed or grown in place.
/// A block that cannot grow in place is allocated again by its container,
/// in the buffer if it fits there and in the other allocator if not.
export mut view struct Fallback {
    own: Own
    fixed: Fixed
    inner: &Alloc
    lim: uint
}

const FallbackTable = core.AllocTable(alloc: FallbackAllocEntry, resize: FallbackResizeEntry, free: FallbackFreeEntry)

/// A Fallback that serves from buf first, then from inner. inner stays
/// borrowed, and buf borrowed (mut), for as long as the Fallback and
/// anything allocated from it lives. As for Fixed.New, up to 7 bytes in
/// front of buf's first 8-byte boundary are never handed out. O(1);
/// allocates nothing.
export Fallback.New(inner: &Alloc, buf: mut []u8): Fallback from inner, buf {
    f := Fixed.New(buf)
    var lim: uint = 0
    if f.size > 0 {
        lim = Trunc[uint](f.size)
    }
    return Fallback(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&FallbackTable))), fixed: f, inner: inner, lim: lim)
}

/// The allocator that tries the buffer first and inner second; it fails
/// with OutOfMemory only when inner does. A block goes back to where it
/// came from, whichever that was. A zero-byte request goes to inner, as
/// a request for one byte. O(1) per request on top of inner's.
export Fallback.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[Fallback, u8](raw.Of(self)), raw.Of(&FallbackTable))
    }
    return OwnAlloc(&self.own)
}

/// Makes the whole buffer free again. It ends self, so nothing allocated
/// from self can be used after it; what came from the other allocator was
/// freed there by then. O(1).
export Fallback.Reset(mut self) ends self {
    self.fixed.used.Store(0)
}

// FallbackOf is the Fallback whose Alloc.ctx is ctx.
FallbackOf(ctx: *u8): &Fallback {
    unsafe "ctx is the address of a live Fallback, stored by Fallback.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, Fallback](ctx)
    }
}

// FixedCtx is the context Fixed's entries take for b's buffer: the address
// of the Fixed inside b, which b's loan pins as it pins b.
FixedCtx(b: &Fallback): *u8 {
    unsafe "a pointer cast of the address of b's own field; nothing is dereferenced" {
        return raw.PtrCast[Fixed, u8](raw.Of(&b.fixed))
    }
}

// InBuffer says whether p is a block of b's Fixed: its offset from the
// buffer's base, as an unsigned number, is below the usable bytes. A null
// p and a block of inner give an offset at or past them: the Fallback
// holds the buffer exclusively, so no byte of a block of inner lies inside
// it, and every block of inner has a byte (InnerSize).
InBuffer(b: &Fallback, p: *u8): bool {
    return raw.Addr(p) -% raw.Addr(b.fixed.base) < b.lim
}

// InnerSize is a size as inner is told it: a zero-byte block of the caller
// is a block of one byte there, from its allocation to its free, so inner
// never holds a zero-byte block of this Fallback. Every other size is
// itself, a negative one too, which inner refuses.
InnerSize(size: int): int {
    if size == 0 {
        return 1
    }
    return size
}

// A request of one byte or more that the Fixed refuses (no room, or an
// alignment it does not serve) goes to inner, which judges it again.
FallbackAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    b := FallbackOf(ctx)
    if size > 0 {
        p := FixedAllocEntry(FixedCtx(b), size, align)
        if !raw.IsNull(p) {
            return p
        }
    }
    return core.AllocBytes(b.inner, InnerSize(size), align)
}

// A block never moves between the two in place: one in the buffer that
// cannot grow there is refused, and the caller allocates again, which may
// reach inner, copies, and frees the old block back to the buffer.
FallbackResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    b := FallbackOf(ctx)
    if InBuffer(b, p) {
        return FixedResizeEntry(FixedCtx(b), p, old, new, align)
    }
    return core.ResizeBytes(b.inner, p, InnerSize(old), InnerSize(new), align)
}

FallbackFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    b := FallbackOf(ctx)
    if InBuffer(b, p) {
        FixedFreeEntry(FixedCtx(b), p, size, align)
        return
    }
    core.FreeBytes(b.inner, p, InnerSize(size), align)
}
