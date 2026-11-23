// Why: programs need allocators beyond the heap and the arena: one over a
// caller's buffer that never touches the system, and one that refuses
// every request.
//
// What: Fixed (a bump allocator over a caller's buffer, with New, Alloc and
// Reset) and Null (an allocator on which every request fails). Counting is
// counting.mk, Pool is pool.mk, and Page is the runtime's.
//
// How: every allocator of this module hands out an &Alloc that is a field of the
// value itself, so its identity (std.core's rule: the address of the Alloc
// value) is stable exactly while a loan pins the value: Alloc(&self) returns
// `&Alloc from self`, and every container made from it holds that loan. The
// table entries find the allocator's state through Alloc.ctx, and Alloc
// stores the value's own address there on every call, which is the same
// address for as long as any earlier result is live. The state an entry
// changes sits in Cells, because entries write it while shared loans cover
// it: that write is defined only inside a Cell, and a Cell also
// keeps the back end from marking a shared reference to the value readonly.

import std.core
import std.raw

// Own has the layout of std.core's Alloc: {ctx: *u8, table: &AllocTable}
// (a raw pointer and a reference are one pointer each, and Cell[T] is T
// inline). Alloc's fields are private to std.core, so this
// module builds the same bytes and casts.
struct Own {
    ctx: Cell[*u8]
    table: Cell[*core.AllocTable]
}

// Refresh stores both words of o, so that the loads AllocBytes makes right
// after an inlined Alloc() see the stores and fold: the table becomes a
// constant, its entry a direct call, and the entry inlines. Stored once in
// New only, the table word would be reloaded after every call, because an
// entry could have written it, and every request would be an indirect call.
Refresh(o: &Own, ctx: *u8, t: *core.AllocTable) {
    o.ctx.Store(ctx)
    o.table.Store(t)
}

// OwnAlloc is o as the Alloc it has the layout of. The result holds no loan
// of its own; each caller's signature says `from self`, which pins the value
// that holds o.
OwnAlloc(o: &Own): &Alloc {
    unsafe "Own has Alloc's layout, and o lives as long as the caller's from-self loan" {
        return &*raw.PtrCast[Own, Alloc](raw.Of(o))
    }
}

// MaxAlign is the largest alignment Fixed serves (a 64-byte cache line, a
// 4 KiB page and an align(65536) type all fit); a larger one fails as an
// unsupported request, null, as the AllocTable contract says. The skew below
// keeps the address bits under it.
const MaxAlign = 1073741824

// ---------------------------------------------------------------------------
// Fixed: a bump allocator over a caller's buffer (Zig's FixedBufferAllocator;
// bumpalo's in-place resize and free of the last block). Upward bumping, so
// the last block grows in place: a lone growing List or HeapList in a Fixed
// costs no copies.
//
// Unlike Zig's FixedBufferAllocator and bumpalo, which give back only the
// block itself and keep the alignment padding in front of it, a Fixed gives
// the padding back too, so any LIFO sequence of frees, of any depth and any
// mix of alignments, returns the buffer to where it was.
// Two rules do it, and they need no memory of their own:
//
//   - Granules. New moves base up to the buffer's first 8-byte boundary of
//     the address space (up to 7 bytes are not used), so every block starts
//     on a granule boundary, and used is kept rounded
//     up to the granule after the last block's end (Round). A request
//     aligned to 8 or less then needs no padding at all, the last block is
//     the one whose rounded end is used, and freeing it moves used back to
//     its start, which is the rounded end of the block before. A block of
//     fewer than 8 bytes keeps the rest of its granule: the price of LIFO
//     frees (a growing list grows in place, so only its last granule is
//     rounded).
//   - Headers. A request aligned to more than 8 may need padding in front.
//     It gets at least one granule of it when used > 0, and used as it was
//     before the request goes into the padding's last 8 bytes, just below
//     the block; freeing the block reads it back. When used == 0 the header
//     is written only if the block starts 8 or more bytes in; a block that
//     starts lower starts at 0 and rolls back to 0. A free or resize passes
//     the alignment the block was allocated with (the AllocTable contract);
//     passing a smaller one only keeps the padding.

// base[0..used] is handed out, base[used..size] is free. base is on an
// 8-byte boundary and used is a multiple of 8 that may pass size by up to 7
// (the last granule's rounding: size - used is then negative and every
// request fails). A buffer that holds no 8-byte boundary keeps its own
// base and has size -8, so every request fails, a zero-byte one too. skew
// is the low bits of base's address (a multiple of 8 but for that case), so
// a wider alignment is computed on offsets.
/// A bump allocator over a caller's buffer, which never touches the
/// system: an allocation is O(1), the last block grows and frees in place,
/// and Reset frees everything at once.
///
/// Only the last block can be freed, and freeing it gives back the
/// alignment padding before it too, so frees in any last-in, first-out
/// order return the buffer to where it was. Each block starts on at least
/// an 8-byte boundary, so a block under 8 bytes takes 8. Alignments up to
/// 2^30 are served; a larger one fails.
export mut view struct Fixed {
    own: Own
    base: *u8
    size: int
    skew: int
    used: Cell[int]
}

const FixedTable = core.AllocTable(alloc: FixedAllocEntry, resize: FixedResizeEntry, free: FixedFreeEntry)

/// A Fixed over buf, which stays borrowed (mut) for as long as the Fixed
/// and anything allocated from it lives. Up to 7 bytes in front of buf's
/// first 8-byte boundary are never handed out; a buf with no such boundary
/// (fewer than 8 bytes, or an empty one at an odd address) refuses every
/// request. O(1); allocates nothing.
export Fixed.New(buf: mut []u8): Fixed {
    n := buf.Len()
    var p := raw.DataMut(buf)
    lead := Trunc[int]((0 -% raw.Addr(p)) & 7)
    var size := -8
    if lead <= n {
        size = n - lead
        unsafe "lead <= n: base + lead is inside buf, or its end" {
            p = raw.Offset(p, lead)
        }
    }
    skew := Trunc[int](raw.Addr(p) & Trunc[uint](MaxAlign - 1))
    return Fixed(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&FixedTable))), base: p, size: size, skew: skew, used: Cell[int].New(0))
}

/// The allocator that hands out self's buffer; a request it cannot fit
/// fails with OutOfMemory. A container made from it borrows self. O(1).
export Fixed.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[Fixed, u8](raw.Of(self)), raw.Of(&FixedTable))
    }
    return OwnAlloc(&self.own)
}

/// Makes the whole buffer free again. It ends self, so nothing allocated
/// from self can be used after it. O(1).
export Fixed.Reset(mut self) ends self {
    self.used.Store(0)
}

// FixedOf is the Fixed whose Alloc.ctx is ctx.
FixedOf(ctx: *u8): &Fixed {
    unsafe "ctx is the address of a live Fixed, stored by Fixed.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, Fixed](ctx)
    }
}

// Round is what used becomes after a last block that ends at x: the next
// multiple of 8. Exact for 0 <= x <= size: size is the length of a real
// buffer, far below 2^62.
Round(x: int): int {
    return (x +% 7) & -8
}

// The fast path (alignment 1, 2, 4 or 8) is one load, two compares for
// the request, one for the fit, three wrapping operations and one store:
// used is a multiple of 8 and base is 8-aligned, so no padding is ever
// needed. Why the wrapping ones are exact: used <= size + 7, so size - used
// >= -7 (or size is -8 and used 0); when the compare passes, used + size
// <= size. Every other alignment, and a negative size, goes to
// FixedAllocWide, which refuses what is not valid.
FixedAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    f := FixedOf(ctx)
    if Trunc[uint](align -% 1) >= 8 || (align & (align -% 1)) != 0 || size < 0 {
        return FixedAllocWide(f, size, align)
    }
    used := f.used.Load()
    if size > f.size -% used {
        return raw.Null[u8]()
    }
    f.used.Store(Round(used +% size))
    unsafe "used + size <= f.size: inside the buffer" {
        return raw.Offset(f.base, used)
    }
}

// FixedAllocWide refuses a negative size and an alignment that is not a
// power of two from 1 to MaxAlign, and serves one from 16 up: at least one
// granule of padding when used > 0, and used written into its last 8 bytes
// (the header). off is a multiple of align past base's skew, so the header
// at off - 8 is 8-aligned, and it lies in [used, off), bytes no block holds.
// The wrapping operators are exact, and keep panic paths out of the entry
// this inlines into: used <= size + 7 and size is a real buffer's length,
// far below 2^62, so lo and off stay under 2^62 + 2^30; off + size <= f.size
// once the fit compare passes.
FixedAllocWide(f: &Fixed, size: int, align: int): *u8 {
    if size < 0 || align < 16 || align > MaxAlign || (align & (align -% 1)) != 0 {
        return raw.Null[u8]()
    }
    used := f.used.Load()
    var lo := used
    if used != 0 {
        lo = used +% 8
    }
    off := lo +% ((0 -% (f.skew +% lo)) & (align -% 1))
    if size > f.size -% off {
        return raw.Null[u8]()
    }
    if off >= 8 {
        unsafe "used <= off - 8 and off <= f.size: the header slot is free bytes of the buffer, 8-aligned" {
            raw.Write[int](raw.PtrCast[u8, int](raw.Offset(f.base, off -% 8)), used)
        }
    }
    f.used.Store(Round(off +% size))
    unsafe "off + size <= f.size: inside the buffer" {
        return raw.Offset(f.base, off)
    }
}

// FixedOffset is p's offset from base, wrapped: a null or foreign p gives
// any value, which the callers' unsigned compares against used refuse.
FixedOffset(f: &Fixed, p: *u8): int {
    return Trunc[int](raw.Addr(p) -% raw.Addr(f.base))
}

// FixedBackWide is what used was before the last block, at s and of an
// alignment over 8, was allocated: its header, or 0 for a block that
// starts below 8. A header FixedAllocWide could not have written (only when
// a caller passes another alignment than the allocation's), or a start past
// size (a pointer the caller did not get: its header slot could lie past
// the buffer), leaves used at s rounded down to its granule, which is
// always safe. The wrapping operators are exact: 8 <= s <= size, the tests
// run left to right so s - 8 - h is taken only for 0 <= h <= s - 8.
FixedBackWide(f: &Fixed, s: int, align: int): int {
    if s < 8 {
        return 0
    }
    if s > f.size {
        return s & -8
    }
    var h := 0
    unsafe "the header slot below the wide block at s, written by FixedAllocWide: 8-aligned, inside the buffer" {
        h = raw.Read[int](raw.PtrCast[u8, int](raw.Offset(f.base, (s - 8) & -8)))
    }
    if h >= 0 && h <= s -% 8 && s -% 8 -% h < align && (h & 7) == 0 {
        return h
    }
    return s & -8
}

// The last block (the one whose rounded end is used) grows or shrinks in
// place while it fits. Any other block may shrink (its tail is simply not
// reused), as Zig's FixedBufferAllocator.resize, but never grow. The
// unsigned compares refuse a null or foreign p (its offset passes used), a
// negative old and an old past used; s <= used <= size + 7 makes the rest
// exact. used is a multiple of 8, so a block ending at e <= used is last
// exactly when used - e < 8.
FixedResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    f := FixedOf(ctx)
    used := f.used.Load()
    s := FixedOffset(f, p)
    if Trunc[uint](s) > Trunc[uint](used) || Trunc[uint](old) > Trunc[uint](used -% s) || new < 0 {
        return false
    }
    if used -% s -% old >= 8 {
        return new <= old
    }
    if new > f.size -% s {
        return false
    }
    f.used.Store(Round(s +% new))
    return true
}

// Freeing the last block gives back its bytes and the padding in front of
// it, so LIFO temporaries cost nothing however deep they nest; any other
// block stays until Reset, and so does every block below it. A zero-byte
// block counts too: it is last while nothing was allocated after it, and
// freeing it gives back a wide one's padding. The fast path is one load,
// an unsigned compare and a sign test for the bounds (a null or foreign p
// gives an offset past used; a negative size is a no-op), one unsigned
// compare for the last block, and a store. That compare is exact: with
// 0 <= s <= used and 0 <= size < 2^63, used - s - size in [0, 8) as an
// unsigned number means s + size really lies in (used - 8, used], and used
// is a multiple of 8. A block's start is a multiple of 8; the mask keeps
// used one when a caller frees a pointer it did not get.
FixedFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    f := FixedOf(ctx)
    used := f.used.Load()
    s := FixedOffset(f, p)
    if Trunc[uint](s) > Trunc[uint](used) || size < 0 {
        return
    }
    if Trunc[uint](used -% s -% size) >= 8 {
        return
    }
    if align > 8 {
        f.used.Store(FixedBackWide(f, s, align))
        return
    }
    f.used.Store(s & -8)
}

// ---------------------------------------------------------------------------
// Null: an allocator on which every request fails. A code path given
// mem.Null() and run proves it allocates nothing on that run.

const NullTable = core.AllocTable(alloc: NullAllocEntry, resize: NullResizeEntry, free: NullFreeEntry)

// NullRepr is Alloc's layout too, in a form a const may hold: no Cell and
// no raw pointer (E1002). ctx is the integer 0, which has a null pointer's
// bytes; no Null entry reads it.
struct NullRepr {
    ctx: uint
    table: &core.AllocTable
}

// The one Null owner, in static memory: a reference re-made from its address
// holds no loan, as Heap.Alloc's does.
const NullOwn = NullRepr(ctx: 0, table: &NullTable)

/// An allocator on which every request fails with OutOfMemory. Code that
/// runs to the end with it allocated nothing on that run. O(1).
export Null(): &Alloc {
    unsafe "NullOwn is static, immutable, and has Alloc's layout" {
        return &*raw.PtrCast[NullRepr, Alloc](raw.Of(&NullOwn))
    }
}

NullAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    return raw.Null[u8]()
}

NullResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

// Nothing was ever allocated, so there is nothing to give back.
NullFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
}
