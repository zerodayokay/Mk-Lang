// Why: a program on a memory budget, a kernel and an embedded target each
// hold one region of memory and must free and reuse blocks inside it in any
// order; a Fixed gives back only its last block.
//
// What: FixedHeap, a general-purpose allocator over a caller's buffer: New,
// Alloc and Reset.
//
// How: two-level segregated fit (Masmano, Ripoll, Crespo and Real, "TLSF: a
// new dynamic memory allocator for real-time systems", ECRTS 2004): every
// request, resize and free is a bounded number of steps whatever the buffer
// holds, and a freed block merges with its free neighbours at once. All of
// its state is in the buffer, so the FixedHeap value itself never changes
// after New. The rest of this header gives the layout, the lists, the
// invariants and the costs.
//
// Granules. New moves base up to the buffer's first 8-byte boundary, and
// everything is counted from there in granules of 8 bytes: granule g is the
// 8 bytes at base + 8 g. A FixedHeap uses at most 2^31 - 1 granules (16
// GiB); the rest of a longer buffer is left alone. Every word below is a
// u32.
//
// The buffer holds, in order:
//   - The control words: the first-level map at byte 0, one second-level
//     map per first level from byte 4, and, from the next 8-byte boundary
//     (heads), 16 list heads per first level.
//   - The blocks, from granule first. A block is a header of one granule
//     and its payload. The header is prev, the size in granules of the
//     block before it (0 for the first block), then size, the block's own
//     size in granules with its header, with FhFree set while the block is
//     free. A free block's payload starts with next and back, the granules
//     of its neighbours on its list, 0 for none (granule 0 holds control
//     words, never a block).
//   - The end marker at granule last: a header alone, never free, so the
//     last block has a neighbour to read.
//
// Lists. A free block of g granules is on list FhList(g): one list per size
// below 32 granules, then 16 lists per doubling. Bit s of second-level map
// f says list 16 f + s holds a block, and bit f of the first-level map says
// map f is not zero, so two count-trailing-zeros find the first list at or
// after any index that holds one. A request takes the head of the first
// list whose every block is large enough (FhFit), and gives the part it
// does not need back as a free block.
//
// Invariants (ThWalk in tests_fixedheap.mk checks each after every
// operation of random runs, and std's Go tests compare every offset with a
// model):
// - H1: the blocks tile [first, last): a block's size leads to the next
//   block's header, whose prev is that size, and the last block leads to
//   the end marker.
// - H2: no two neighbours are both free.
// - H3: a free block is on exactly the list of its size, a used block on
//   none, and the maps mirror the heads.
// - H4: a block is at least 2 granules: a header, and room for the links.
//
// Costs. An allocation, a resize and a free are each O(1): no loop, a few
// map words and a few words of the blocks read and written. A block costs
// its header, 8 bytes, and its size rounds up to 8 bytes, so a block of 1
// to 8 bytes takes 16; a block also keeps a rest of 8 bytes, too few to be
// a block. The control words take 4 + 68 bytes per first level, rounded to
// 8: one first level for the blocks under 128 bytes and one per doubling of
// the largest block's size above that (416 bytes of a 4 KiB buffer, 960 of
// a 1 MiB one). Where one more first level would cost more than the larger
// block it allows is worth, up to 72 bytes at the buffer's end are left
// unused instead (FhShape). Fit, at an alignment of 8 or less: a request of
// up to 248 bytes is served whenever a free block of its size exists; a
// larger one is sure to be served when a free block at least one sixteenth
// larger exists, and is served by a block less than that larger only when
// that block heads its list. A request aligned over 8 asks for a block
// larger than it by the alignment and 8 bytes (FhAllocWide).

import std.core
import std.raw

// FhFree is the bit of a header's size that says the block is free, and
// FhSize the bits that hold the size.
const FhFree = 0x80000000
const FhSize = 0x7FFFFFFF

// FhMaxGranules is the most granules a FixedHeap uses, so that a block's
// size fits FhSize.
const FhMaxGranules = 0x7FFFFFFF

// base is on an 8-byte boundary. fls is the number of first levels, 0 for
// a buffer too small to hold one block: then first, last and max are 0 and
// every request fails. heads is the byte offset of the list heads. The
// blocks' headers are at granules first to last - 1 and the end marker's at
// last; nothing past the end marker is touched. max is the largest request
// in bytes: the payload of one block that spans them all. No field changes
// after New: what an entry changes is in the buffer, which it reaches
// through base.
/// A general-purpose allocator over a caller's buffer, which never touches
/// the system: any block can be freed, in any order, and its bytes are
/// used again. An allocation, a resize and a free are each O(1), whatever
/// the buffer holds.
///
/// A freed block merges with the free blocks beside it at once, and a
/// block grows in place into a free block after it. Each block costs 8
/// bytes besides its own, rounded up to 8, and starts on an 8-byte
/// boundary; alignments up to 2^30 are served. The bookkeeping lives at
/// the start of the buffer: 416 bytes of a 4 KiB buffer, 960 of a 1 MiB
/// one. A buffer over 16 GiB is used up to 16 GiB.
export mut view struct FixedHeap {
    own: Own
    base: *u8
    fls: int
    heads: int
    first: int
    last: int
    max: int
}

const FixedHeapTable = core.AllocTable(alloc: FixedHeapAllocEntry, resize: FixedHeapResizeEntry, free: FixedHeapFreeEntry)

/// A FixedHeap over buf, which stays borrowed (mut) for as long as the
/// FixedHeap and anything allocated from it lives. Up to 7 bytes in front
/// of buf's first 8-byte boundary are never handed out, and the
/// bookkeeping takes the start of what follows; a buf too small for it
/// and one block (under 96 bytes from that boundary) refuses every
/// request. O(1) besides clearing the bookkeeping; allocates nothing.
export FixedHeap.New(buf: mut []u8): FixedHeap {
    n := buf.Len()
    var p := raw.DataMut(buf)
    lead := Trunc[int]((0 -% raw.Addr(p)) & 7)
    var g := 0
    if lead <= n {
        g = (n - lead) >> 3
        unsafe "lead <= n: base + lead is inside buf, or its end" {
            p = raw.Offset(p, lead)
        }
    }
    if g > FhMaxGranules {
        g = FhMaxGranules
    }
    // The heads follow the maps, the first block the heads, and the end
    // marker the granules the blocks span.
    (fls, span) := FhShape(g)
    var heads := 0
    var first := 0
    var last := 0
    var max := 0
    if fls != 0 {
        heads = FhHeads(fls)
        first = (heads + 64 * fls) >> 3
        last = first + span
        max = 8 * span - 8
    }
    h := FixedHeap(own: Own(ctx: Cell[*u8].New(raw.Null[u8]()), table: Cell[*core.AllocTable].New(raw.Of(&FixedHeapTable))), base: p, fls: fls, heads: heads, first: first, last: last, max: max)
    FhFormat(&h)
    return h
}

/// The allocator that hands out self's buffer; a request it cannot fit
/// fails with OutOfMemory. A container made from it borrows self. O(1).
export FixedHeap.Alloc(&self): &Alloc from self {
    unsafe "a pointer cast of self's own address; nothing is dereferenced" {
        Refresh(&self.own, raw.PtrCast[FixedHeap, u8](raw.Of(self)), raw.Of(&FixedHeapTable))
    }
    return OwnAlloc(&self.own)
}

/// Makes the whole buffer free again. It ends self, so nothing allocated
/// from self can be used after it. O(1) besides clearing the bookkeeping.
export FixedHeap.Reset(mut self) ends self {
    FhFormat(&*self)
}

// FixedHeapOf is the FixedHeap whose Alloc.ctx is ctx.
FixedHeapOf(ctx: *u8): &FixedHeap {
    unsafe "ctx is the address of a live FixedHeap, stored by FixedHeap.Alloc, and pinned by its loan" {
        return &*raw.PtrCast[u8, FixedHeap](ctx)
    }
}

// FhR is the u32 at byte off of the buffer, widened. Every caller passes a
// multiple of 4 that is a control word or a field of a block's header or
// links, and base is 8-aligned.
FhR(b: *u8, off: int): int {
    unsafe "off is a control word or a block's field: aligned, inside the buffer the FixedHeap borrows" {
        return int(raw.Read[u32](raw.PtrCast[u8, u32](raw.Offset(b, off))))
    }
}

// FhW stores v, which fits 32 bits, as the u32 at byte off of the buffer.
FhW(b: *u8, off: int, v: int) {
    unsafe "off is a control word or a block's field: aligned, inside the buffer the FixedHeap borrows" {
        raw.Write[u32](raw.PtrCast[u8, u32](raw.Offset(b, off)), Trunc[u32](v))
    }
}

// FhList is the list of a free block of g granules: g itself below 32
// (first levels 0 and 1, one size per list), then, with b the top bit of g,
// first level b - 3 and the four bits of g below b.
FhList(g: int): int {
    if g < 32 {
        return g
    }
    b := 63 - raw.Clz64(Trunc[u64](g))
    return ((b - 3) << 4) + ((g >> (b - 4)) & 15)
}

// FhFit is the first list whose every block holds g granules: g's own list
// below 32, where a list holds one size, else the list of g rounded up to
// the next list boundary.
FhFit(g: int): int {
    if g < 32 {
        return g
    }
    b := 63 - raw.Clz64(Trunc[u64](g))
    return FhList(g + (1 << (b - 4)) - 1)
}

// FhHeads is the byte offset of the list heads when there are fls first
// levels: after the first-level map and fls second-level maps, on an
// 8-byte boundary.
FhHeads(fls: int): int {
    return (4 + 4 * fls + 7) & -8
}

// FhShape is the shape of a heap of g granules: its number of first
// levels, and the granules its blocks span, which is its largest block.
// With fls first levels the control words leave room granules before the
// end marker, and the lists hold blocks under 8 << fls granules; the span
// is the smaller of the two, and FhShape picks the fls that makes it
// largest, the fewest on a tie. So the largest block always needs every
// first level there is, and a longer buffer never has a smaller one. fls
// is 0 when no number of levels leaves a block of 2 granules. One level
// more than the list of g itself needs lists every block of the buffer, so
// the loop stops there, within 28 steps.
FhShape(g: int): (fls: int, span: int) {
    top := (FhList(g) >> 4) + 1
    var best := 0
    var span := 1
    for fls in 1..top + 1 {
        room := g - 1 - ((FhHeads(fls) + 64 * fls) >> 3)
        if room < 2 {
            break
        }
        var n := (8 << fls) - 1
        if room < n {
            n = room
        }
        if n > span {
            best = fls
            span = n
        }
    }
    return (fls: best, span: span)
}

// FhFormat writes an empty heap into the buffer: zeroed control words, one
// free block from first to last, and the end marker. A buffer too small
// for that is left untouched.
FhFormat(h: &FixedHeap) {
    if h.fls == 0 {
        return
    }
    b := h.base
    unsafe "the control words are the 8 * first bytes at the start of the buffer, which New found room for" {
        raw.Fill(b, 0, h.first << 3)
    }
    FhW(b, h.first << 3, 0)
    FhW(b, (h.last << 3) + 4, 1)
    FhPut(h, h.first, h.last - h.first)
}

// The granule numbers and byte offsets below lie inside the buffer, under
// 2^34, so the wrapping operators in the list and block helpers are exact;
// they are used to keep overflow branches out of every request. For the
// same reason a first level f (under fls, at most 28: a map is a u32) is
// masked to 5 bits where it is a shift count, which changes no value and
// keeps the shift check's panic, and the frame it needs, out of the
// helpers.

// FhPut makes the n granules at g one free block: its size with FhFree,
// the prev of the block after it, and its place at the head of its list.
FhPut(h: &FixedHeap, g: int, n: int) {
    b := h.base
    at := g << 3
    FhW(b, at +% 4, n | FhFree)
    FhW(b, (g +% n) << 3, n)
    i := FhList(n)
    hp := h.heads +% (i << 2)
    head := FhR(b, hp)
    FhW(b, at +% 8, head)
    FhW(b, at +% 12, 0)
    FhW(b, hp, g)
    if head != 0 {
        // The list held a block, so both of its bits are set already.
        FhW(b, (head << 3) +% 12, g)
        return
    }
    f := i >> 4
    sp := 4 +% (f << 2)
    FhW(b, sp, FhR(b, sp) | (1 << (i & 15)))
    FhW(b, 0, FhR(b, 0) | (1 << (f & 31)))
}

// FhEmpty clears the bit of list i, which just lost its last block, and
// its first level's bit when that leaves the second-level map zero.
FhEmpty(h: &FixedHeap, i: int) {
    b := h.base
    f := i >> 4
    sp := 4 +% (f << 2)
    m := FhR(b, sp) & ~(1 << (i & 15))
    FhW(b, sp, m)
    if m == 0 {
        FhW(b, 0, FhR(b, 0) & ~(1 << (f & 31)))
    }
}

// FhUnlink takes the free block at g, of n granules, off its list. Only a
// block at the head of its list needs the list's index.
FhUnlink(h: &FixedHeap, g: int, n: int) {
    b := h.base
    at := g << 3
    nx := FhR(b, at +% 8)
    bk := FhR(b, at +% 12)
    if nx != 0 {
        FhW(b, (nx << 3) +% 12, bk)
    }
    if bk != 0 {
        FhW(b, (bk << 3) +% 8, nx)
        return
    }
    i := FhList(n)
    FhW(b, h.heads +% (i << 2), nx)
    if nx == 0 {
        FhEmpty(h, i)
    }
}

// FhTake unlinks and returns the head of the first list at or after i that
// holds a block, or 0 when none does: the rest of i's second-level map,
// then the first later first level with any list. A list index past the
// first levels the buffer has (a request rounded up past every block)
// finds nothing.
FhTake(h: &FixedHeap, i: int): int {
    b := h.base
    var f := i >> 4
    if f >= h.fls {
        return 0
    }
    var m := FhR(b, 4 +% (f << 2)) & (-1 << (i & 15))
    if m == 0 {
        fm := FhR(b, 0) & (-1 << ((f +% 1) & 31))
        if fm == 0 {
            return 0
        }
        f = raw.Ctz64(Trunc[u64](fm))
        m = FhR(b, 4 +% (f << 2))
    }
    j := (f << 4) +% raw.Ctz64(Trunc[u64](m))
    hp := h.heads +% (j << 2)
    g := FhR(b, hp)
    nx := FhR(b, (g << 3) +% 8)
    FhW(b, hp, nx)
    if nx != 0 {
        FhW(b, (nx << 3) +% 12, 0)
    } else {
        FhEmpty(h, j)
    }
    return g
}

// FhFind unlinks and returns a free block of at least need granules, or 0:
// the head of the first list FhFit promises, else the head of need's own
// list when that block is large enough. The second look is what serves a
// request for the one block left, which sits below the boundary FhFit
// rounds up to. Below 32 granules the two lists are one.
FhFind(h: &FixedHeap, need: int): int {
    g := FhTake(h, FhFit(need))
    if g != 0 || need < 32 {
        return g
    }
    i := FhList(need)
    if (i >> 4) >= h.fls {
        return 0
    }
    b := h.base
    own := FhR(b, h.heads +% (i << 2))
    if own == 0 {
        return 0
    }
    n := FhR(b, (own << 3) +% 4) & FhSize
    if n < need {
        return 0
    }
    FhUnlink(h, own, n)
    return own
}

// FhCarve makes the block at g, which is on no list and spans have
// granules, a used block of need: a rest of 2 granules or more goes back
// as a free block (it has no free neighbour: the block after g is used, or
// the caller merged it into have), and a rest of 1 stays in the block.
FhCarve(h: &FixedHeap, g: int, have: int, need: int) {
    b := h.base
    rest := have -% need
    if rest < 2 {
        FhW(b, (g << 3) +% 4, have)
        FhW(b, (g +% have) << 3, have)
        return
    }
    FhW(b, (g << 3) +% 4, need)
    FhPut(h, g +% need, rest)
    FhW(b, (g +% need) << 3, need)
}

// FhPayload is the first byte after the header of the block at g.
FhPayload(h: &FixedHeap, g: int): *u8 {
    unsafe "the payload of the block at g, inside the buffer" {
        return raw.Offset(h.base, (g << 3) +% 8)
    }
}

// The fast path (alignment 1, 2, 4 or 8, and a size the buffer could
// hold): a payload starts one granule after a header, on an 8-byte
// boundary, so no padding is needed. The unsigned compare refuses a
// negative size with one branch. A zero-byte request takes a block of 2
// granules, so it gets an address of its own and is freed like any other.
// Every other alignment, and a size past max, goes to FhAllocWide, which
// refuses what is not valid.
FixedHeapAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    h := FixedHeapOf(ctx)
    if Trunc[uint](align -% 1) >= 8 || (align & (align -% 1)) != 0 || Trunc[uint](size) > Trunc[uint](h.max) {
        return FhAllocWide(h, size, align)
    }
    var need := (size +% 15) >> 3
    if need < 2 {
        need = 2
    }
    g := FhFind(h, need)
    if g == 0 {
        return raw.Null[u8]()
    }
    FhCarve(h, g, FhR(h.base, (g << 3) +% 4) & FhSize, need)
    return FhPayload(h, g)
}

// FhAllocWide refuses a negative size, one past max, and an alignment that
// is not a power of two from 16 to MaxAlign, and serves the rest. It takes
// a block with room for the request, the alignment and one granule more;
// the payload then moves up to the first aligned granule, and the granules
// skipped stay behind as a free block. A gap of one granule cannot be a
// block (H4), so the payload moves up one alignment further: the gap is 0,
// or 2 granules or more, and at most the alignment plus one.
FhAllocWide(h: &FixedHeap, size: int, align: int): *u8 {
    if Trunc[uint](size) > Trunc[uint](h.max) || align < 16 || align > MaxAlign || (align & (align -% 1)) != 0 {
        return raw.Null[u8]()
    }
    // Past the compare, size <= max (under 2^34) and align <= 2^30, and
    // every granule count below lies in the buffer, so the wrapping
    // operators are exact (and keep panic paths out of the entry this
    // inlines into).
    var need := (size +% 15) >> 3
    if need < 2 {
        need = 2
    }
    ag := align >> 3
    want := need +% ag +% 1
    if want > h.last -% h.first {
        return raw.Null[u8]()
    }
    found := FhFind(h, want)
    if found == 0 {
        return raw.Null[u8]()
    }
    b := h.base
    var g := found
    var have := FhR(b, (g << 3) +% 4) & FhSize
    // at is the payload's granule in the address space, gap how far the
    // next aligned one is.
    at := Trunc[int](raw.Addr(b) >> 3) +% g +% 1
    var gap := (0 -% at) & (ag -% 1)
    if gap == 1 {
        gap = gap +% ag
    }
    if gap != 0 {
        g = g +% gap
        have = have -% gap
        FhPut(h, found, gap)
    }
    FhCarve(h, g, have, need)
    return FhPayload(h, g)
}

// FhBlock is the granule of the used block whose payload starts at p, or 0
// when p cannot be one: it is not on an 8-byte boundary, it lies outside
// the blocks (the unsigned compare refuses a null or foreign p, whose
// granule wraps past last, and everything when the buffer holds no block),
// or it is the payload of a block that is free. A resize or a free of such
// a p changes nothing; so freeing a block twice in a row changes nothing
// the second time. An address on an 8-byte boundary inside a used block is
// not told from a block's start: passing one breaks AllocTable's contract,
// as it does for every allocator.
FhBlock(h: &FixedHeap, p: *u8): int {
    off := Trunc[int](raw.Addr(p) -% raw.Addr(h.base))
    g := (off >> 3) -% 1
    if (off & 7) != 0 || Trunc[uint](g -% h.first) >= Trunc[uint](h.last -% h.first) {
        return 0
    }
    if (FhR(h.base, (g << 3) +% 4) & FhFree) != 0 {
        return 0
    }
    return g
}

// A block shrinks in place, giving back a rest of 2 granules or more, and
// grows in place into the block after it when that one is free and large
// enough; anything else is refused with nothing changed. The size it has
// is read from its header, never from old. The free block after it is
// taken in whenever the size changes: to grow into, or so the rest given
// back and that block become one (H2).
FixedHeapResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    h := FixedHeapOf(ctx)
    g := FhBlock(h, p)
    if g == 0 || Trunc[uint](new) > Trunc[uint](h.max) {
        return false
    }
    var need := (new +% 15) >> 3
    if need < 2 {
        need = 2
    }
    b := h.base
    cur := FhR(b, (g << 3) +% 4)
    var have := cur
    if need != cur {
        nx := g +% cur
        ns := FhR(b, (nx << 3) +% 4)
        if (ns & FhFree) != 0 {
            if cur +% (ns & FhSize) < need {
                return false
            }
            FhUnlink(h, nx, ns & FhSize)
            have = cur +% (ns & FhSize)
        } else if need > cur {
            return false
        }
    }
    FhCarve(h, g, have, need)
    return true
}

// A freed block merges with the block after it and the block before it
// when they are free, and the result goes on its list: at most two
// unlinks and one insert. The size is read from the header, never from
// size. When the block merges into the one before it, its own header is
// left inside a free block; it is marked free there, so a second free of
// the same pointer still finds a free block and changes nothing.
FixedHeapFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    h := FixedHeapOf(ctx)
    g := FhBlock(h, p)
    if g == 0 {
        return
    }
    b := h.base
    var start := g
    var n := FhR(b, (g << 3) +% 4)
    nx := g +% n
    ns := FhR(b, (nx << 3) +% 4)
    if (ns & FhFree) != 0 {
        FhUnlink(h, nx, ns & FhSize)
        n = n +% (ns & FhSize)
    }
    pv := FhR(b, g << 3)
    if pv != 0 && (FhR(b, ((g -% pv) << 3) +% 4) & FhFree) != 0 {
        start = g -% pv
        FhUnlink(h, start, pv)
        n = n +% pv
        FhW(b, (g << 3) +% 4, FhFree)
    }
    FhPut(h, start, n)
}
