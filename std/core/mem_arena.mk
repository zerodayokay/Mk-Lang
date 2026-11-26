// Why: most Mk programs allocate from arenas: a bump allocation is a compare
// and an add, and everything in an arena is freed at once. The same code must
// also run inside compile-time evaluation, which has no addresses.
//
// What: Arena (New, NewIn, Put, Slice, SliceAligned, Dup, Fmt, Sub, Alloc,
// Reset, Free), HeapArena, std's one Send arena, and the arena of a
// compile-time evaluation (MakeEvalArena, EvalPageTable, Evaluating).
//
// How: chunks come from a source allocator (a Heap's for Arena.New, any
// &Alloc for Arena.NewIn) and double from FirstChunk up to MaxChunk. The
// header lives inside the first chunk, right after its Chunk record, so
// Arena.New is one allocation and moving the value never moves the state.
// No body reads or makes an address: positions are a pointer plus a byte
// count, so the evaluator runs the same code. Reset keeps the first chunk,
// the current one and the current aligned one, so an arena reset in a loop
// reaches a steady state with no heap traffic when each request is at most
// MaxChunk/2 bytes and one turn's requests fit in the current chunk.
// Requests aligned beyond ChunkAlign bump in an aligned chunk of their own,
// kept at the head of the list (BumpAligned). Alloc()'s free gives back the
// last block bumped, and a chunk of its own while it is the newest
// (FreeOwnChunk), so an arena on another's Alloc() (a frame) rewinds it
// last in first out.

import std.raw

/// A bump allocator: it takes memory in chunks from a source allocator (a
/// Heap, or any &Alloc), hands out pieces by moving a pointer, and gives
/// everything back at once with Reset or Free. Nothing is freed one by one.
/// Values in the arena borrow it, so the compiler rejects a use after Reset
/// or Free. Linear: it ends in Free.
export linear mut view struct Arena {
    hdr: *ArenaHdr
}

// src is the chunk source: the Alloc every chunk comes from and goes back
// to, and Sub's source. An arena on a Heap stores the Heap's Alloc, which
// is static memory; one from NewIn stores the caller's &Alloc, kept alive
// by the loan the arena holds (from a), and the arena's Subs hold its loan
// in turn. own.ctx is the header's own address, so Arena.Alloc() is
// stable. The current chunk's free bytes are base[used..size]; base is
// aligned to ChunkAlign. chunks is the newest chunk; next is the size of
// the next ordinary chunk.
struct ArenaHdr {
    src: *Alloc
    own: Alloc
    base: *u8
    used: int
    size: int
    chunks: *Chunk
    next: int
}

// A chunk's record, then its bytes. size is the whole allocation, record
// included. The first chunk holds the ArenaHdr after its record.
struct Chunk {
    prev: *Chunk
    size: int
}

// Arena asserts neither Send nor Share. An arena calls its chunk
// source by itself (Bump's slow paths, Sub, Reset, Free), and an arena
// whose source is an allocator confined to one task (Arena.NewIn over a
// mem.Counting or mem.Fixed) must never run in another; nothing in
// the type tells the two sources apart, so no arena crosses. A task takes &Heap and makes
// its own arena. HeapArena below is std's one Send arena: only a
// Heap feeds it.

// FirstChunk is the first chunk's size; chunks double up to MaxChunk.
const FirstChunk = 4096
const MaxChunk = 1048576

// ChunkAlign is the alignment of every ordinary chunk's bytes (Chunk is 16
// bytes, ArenaHdr 64, and the chunk itself comes at ChunkSrc), so an offset
// aligned to at most ChunkAlign within a chunk is aligned in memory. A type
// aligned beyond it (align(n), n up to MaxAlign) is served from an aligned
// chunk, allocated from the heap at an alignment at least the request's
// (BumpAligned), since
// the bump offset alone cannot align an address this file never reads
// (TestArenaMakesNoAddresses): an offset that is a multiple of align
// from a base aligned to at least align is aligned in memory.
const ChunkAlign = 8

// In an arena on a Heap (Arena.New, its Subs and HeapArena; never one made
// by NewIn over a caller's allocator, whose chunks are asked at ChunkAlign),
// every ordinary chunk, the first included, is asked of the source at
// LineAlign (ChunkSrc; LineBit in next marks such an arena), and its bytes
// start LineSkew mod LineAlign after it: Chunk's 16-byte record, or in the
// first chunk the record and the 64-byte header (HdrRoom, 80). So a bump
// offset off with off + LineSkew a multiple of LineAlign is on a cache line
// in memory, with no address read, and a Slice of LargeSlice bytes or more,
// of a type aligned to at most ChunkAlign, is bumped there (BumpLine): a
// buffer that size is one that vector loops scan, and at the chunk's 16 mod
// 64 every other 32-byte load of it would split a cache line, which the
// indexbyte and dot benchmarks show as a slowdown at L1 with AVX2. It costs
// at most LineAlign - 1 bytes of padding in a chunk, and an own chunk's
// record grows from 16 to LineAlign bytes; a chunk asked at LineAlign costs
// a mem.Fixed up to LineAlign more bytes after its first block (padding and
// the 8-byte slot it keeps to free blocks over 8-byte alignment), which a
// last-in first-out free gives back. Chunks stay ordinary, so Reset, Free,
// the chunk sizes and the order chunks go back in do not change. Below
// LargeSlice nothing changes, and SliceAligned keeps the alignment it is
// given (its doc bounds its padding by it). A compile-time evaluation keeps
// the type's alignment: what it makes becomes static data aligned to the
// type only (SliceAligned's rule), so a line would buy nothing there. An
// implementation property, not a public fact.
const LineBit = 1099511627776
const LineAlign = 64
const LineSkew = 16
const LargeSlice = 4096

// MaxAlign is the largest alignment an arena serves: align(n)'s largest n.
// A larger one is a failed allocation.
const MaxAlign = 65536

// An aligned chunk keeps its heap alignment's log2 in bits 56 to 60 of its
// size (ChunkShift), so Reset and Free return it with the alignment it was
// allocated with (the AllocTable contract) and Chunk keeps its 16 bytes; bit
// 61 (OwnBit) marks an aligned chunk of its own, one request's alone; bit
// 62 (CurBit) marks the current aligned chunk, whose record is an
// AlignedChunk. An ordinary chunk's top byte is 0: no heap serves 2^56
// bytes, and BumpAligned refuses a request past AlignedMax.
const ShiftAt = 56
const AlignedMax = 281474976710656
const OwnBit = 2305843009213693952
const CurBit = 4611686018427387904

// AlignedBase is the least alignment of a current aligned chunk: one chunk
// then serves every alignment up to a page (align(16) SIMD lanes, align(64)
// cache lines, align(4096) pages) by offset bumping alone, and the heap
// serves a 4 KiB alignment from its size classes (rt/heap.mk HeapAlloc); a
// larger request alignment gives the chunk its own. Every source keeps the
// AllocTable contract (an alignment it does not serve is null, so
// OutOfMemory), and only std makes an Alloc: its fields are private.
const AlignedBase = 4096

// The current aligned chunk's record: Chunk's two words, then the bump
// position, in bytes from the chunk's start. It is the head of the chunk
// list while it exists (LinkChunk keeps it there), so the header needs no
// field for it, and its alignment is its ChunkShift.
struct AlignedChunk {
    prev: *Chunk
    size: int
    used: int
}

// MaxBytes is the largest int: a byte count past it is OutOfMemory, never an
// overflow panic.
const MaxBytes = 9223372036854775807

// The table behind Arena.Alloc(): alloc bumps, resize grows or shrinks the
// last block in place only, free gives back the last block, or the newest
// chunk when the block has it to itself (FreeOwnChunk).
const ArenaTable = AllocTable(alloc: ArenaAllocEntry, resize: ArenaResizeEntry, free: ArenaFreeEntry)

// A request through Alloc() comes from any std container: a negative size,
// an alignment that is not a power of two, or one over MaxAlign is a failed
// allocation, null, as the AllocTable contract says, not a panic. One over
// ChunkAlign bumps in the current aligned chunk (Bump, BumpAligned).
ArenaAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    if size < 0 || align < 1 || align > MaxAlign || (align & (align - 1)) != 0 {
        return raw.Null[u8]()
    }
    unsafe "ctx is the address of a live arena header" {
        return Bump(raw.PtrCast[u8, ArenaHdr](ctx), size, align)
    }
}

// p's block is the last one bumped when it ends where the free bytes start.
// The same holds in the current aligned chunk, so a container of
// an align(n) element grows in place there too.
ArenaResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is the address of a live arena header; p a block of it" {
        h := raw.PtrCast[u8, ArenaHdr](ctx)
        if old < 0 || new < 0 || raw.IsNull(p) {
            return false
        }
        if old <= (*h).used && raw.Same(raw.Offset(p, old), raw.Offset((*h).base, (*h).used)) {
            start := (*h).used - old
            if new > (*h).size - start {
                return false
            }
            (*h).used = start + new
            return true
        }
        ac := AlignedLast(h, p, old)
        if raw.IsNull(ac) {
            return false
        }
        astart := (*ac).used - old
        if new > ((*ac).size & SizeMask()) - astart {
            return false
        }
        (*ac).used = astart + new
        return true
    }
}

// Freeing the last block bumped gives its bytes back (bumpalo's dealloc of
// the last allocation): a LIFO temporary costs nothing. A block with a
// chunk of its own goes back to the source with its chunk while that chunk
// is the newest (FreeOwnChunk). Any other block, or a size that does not
// match, stays until Reset or Free. The alignment padding before the block
// is not reclaimed.
ArenaFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is the address of a live arena header; p a block of it" {
        h := raw.PtrCast[u8, ArenaHdr](ctx)
        if size <= 0 || raw.IsNull(p) {
            return
        }
        if size <= (*h).used && raw.Same(raw.Offset(p, size), raw.Offset((*h).base, (*h).used)) {
            (*h).used = (*h).used - size
            return
        }
        ac := AlignedLast(h, p, size)
        if !raw.IsNull(ac) {
            (*ac).used = (*ac).used - size
            return
        }
        FreeOwnChunk(h, p, size, align)
    }
}

// FreeOwnChunk returns the newest chunk to the source when p's size bytes
// have it to themselves: a chunk of its own (BumpSlow, BumpAlignedSlow)
// freed before a later chunk was linked. That is how an arena on another
// arena's Alloc() gives a large request back: its chunk is the newest chunk
// of the arena under it, freed first. The newest chunk is the head, or
// right below the current aligned chunk (LinkChunk); finding it is two
// loads, and no list is walked. The block starts room bytes in, 16 or its
// alignment, and the chunk's size is room + size. An ordinary newest chunk
// is the first, the current one or one of its own (a chunk of the next
// size stops being current only when a newer one is linked above it, and
// the current one is never unlinked), and a block from its record to its
// end is alone in it; the first and the current are never returned. An aligned one must carry OwnBit: a former
// current aligned chunk has an own chunk's shift from AlignedBase up, and a
// block at its alignment can reach its end over smaller-aligned blocks.
FreeOwnChunk(h: *ArenaHdr, p: *u8, size: int, align: int) {
    var room := SizeOf[Chunk]()
    var shift := 0
    if align > ChunkAlign {
        if align > MaxAlign || (align & (align - 1)) != 0 {
            return
        }
        room = align
        shift = Log2(align)
    }
    if size > MaxBytes - room {
        return
    }
    unsafe "h is a live arena header; p a block of it" {
        head := (*h).chunks
        if raw.IsNull(head) {
            return
        }
        var above := raw.Null[Chunk]()
        var c := head
        if IsAlignedCur(head) {
            above = head
            c = (*head).prev
        }
        if raw.IsNull(c) || ChunkShift(c) != shift {
            return
        }
        if ((*c).size & SizeMask()) != room + size || !raw.Same(p, raw.Offset(raw.PtrCast[Chunk, u8](c), room)) {
            return
        }
        if shift != 0 {
            if ((*c).size & OwnBit) == 0 {
                return
            }
        } else if raw.IsNull((*c).prev) || IsCurrent(h, c) {
            return
        }
        if raw.IsNull(above) {
            (*h).chunks = (*c).prev
        } else {
            (*above).prev = (*c).prev
        }
        a := &*(*h).src
        if shift != 0 {
            FreeAligned(a, c)
        } else {
            FreeBytes(a, raw.PtrCast[Chunk, u8](c), (*c).size, ChunkSrc(h))
        }
    }
}

// AlignedLast is the current aligned chunk when p's n bytes are the last
// block bumped in it, else null.
AlignedLast(h: *ArenaHdr, p: *u8, n: int): *AlignedChunk {
    unsafe "h is a live arena header" {
        c := (*h).chunks
        if !IsAlignedCur(c) {
            return raw.Null[AlignedChunk]()
        }
        ac := raw.PtrCast[Chunk, AlignedChunk](c)
        if n > (*ac).used || !raw.Same(raw.Offset(p, n), raw.Offset(raw.PtrCast[Chunk, u8](c), (*ac).used)) {
            return raw.Null[AlignedChunk]()
        }
        return ac
    }
}

// AlignUp rounds n up to align, a power of two; n + align - 1 must fit int
// (the caller's guarantee; the wrapping operators are exact under it and
// keep the overflow branch out of Bump's fast path).
AlignUp(n: int, align: int): int {
    return (n +% (align -% 1)) & ~(align -% 1)
}

// IsLine reports whether h's arena puts large slices on a line (LineBit,
// set by Arena.New natively and kept by Sub); ChunkSrc is the alignment its
// chunks are asked and given back at. The bit sits far above any chunk
// size (MaxChunk), and only BumpSlow reads next, through NextOf.
IsLine(h: *ArenaHdr): bool {
    unsafe "h is a live arena header" {
        return ((*h).next & LineBit) != 0
    }
}

ChunkSrc(h: *ArenaHdr): int {
    if IsLine(h) {
        return LineAlign
    }
    return ChunkAlign
}

// Bump returns size bytes at align from the current chunk, starting a new
// chunk when they do not fit; null when the source is out of memory. The
// caller guarantees size >= 0 and align a power of two at most MaxAlign
// (Put, Slice and Dup by construction, ArenaAllocEntry by its checks); an
// align over ChunkAlign goes to BumpAligned (for Put and Slice the test
// folds away, since their align is a constant), so the fast path is one
// add, one mask and one compare. The
// three operations wrap, because none can overflow, and a checked one would
// add an overflow branch the fast path does not need (two in the release
// code of Dup). Why each is exact:
// - every current chunk's size is chunk bytes - room <= MaxBytes - 16
//   (BumpSlow refuses room + size past MaxBytes; UseChunk subtracts room),
//   and every writer keeps used <= size, so used + align - 1 <= used + 7
//   fits: AlignUp is exact;
// - 0 <= off <= size + 7, so size - off is in [-7, size]: exact, and
//   negative only when off passes size, where the compare fails;
// - the compare passed, so off + size <= the chunk's size: exact.
Bump(h: *ArenaHdr, size: int, align: int): *u8 {
    if align > ChunkAlign {
        return BumpAligned(h, size, align)
    }
    unsafe "h is a live arena header" {
        off := AlignUp((*h).used, align)
        if size <= (*h).size -% off {
            (*h).used = off +% size
            return raw.Offset((*h).base, off)
        }
        return BumpSlow(h, size, false)
    }
}

// BumpLine is Bump for a large Slice (LargeSlice): size bytes on a cache
// line, at the first offset off from used with off + LineSkew a multiple
// of LineAlign (a line arena's chunks come at it), else BumpSlow on a line; null when the
// source is out of memory, and then the arena is as it was, so Slice
// falls back to Bump at the type's alignment (a mem.Fixed too tight for
// the line's padding). The wrapping operations are exact for Bump's
// reasons: the current chunk is the first or one of the next size, so its
// size and used are at most MaxChunk and used + LineSkew + LineAlign - 1
// fits; 0 <= off - used < LineAlign, so size - off is exact and negative
// only where the compare fails; and off + size passed it.
BumpLine(h: *ArenaHdr, size: int): *u8 {
    unsafe "h is a live arena header" {
        off := AlignUp((*h).used +% LineSkew, LineAlign) -% LineSkew
        if size <= (*h).size -% off {
            (*h).used = off +% size
            return raw.Offset((*h).base, off)
        }
        return BumpSlow(h, size, true)
    }
}

// BumpSlow serves a request the current chunk cannot: one over half the
// next chunk size gets a chunk of its own, and the current chunk stays
// current; any other starts a chunk of the next size. Chunk
// bytes start ChunkAlign-aligned, so a new chunk serves any align Bump takes.
// For a line request (BumpLine) the own chunk's record is LineAlign bytes,
// so its bytes start on a line, and in a new chunk the request starts
// LineAlign - LineSkew bytes in; both fit, since size <= next - room.
//
// Every new chunk grows next, its own chunk included: next
// doubles, and keeps doubling until the request would have been an ordinary
// one (size <= next/2), up to MaxChunk. So a workload whose requests are
// all over next/2 moves them into ordinary chunks after one own chunk, the
// current chunk Reset keeps then holds them, and a Reset loop reaches a
// steady state with no heap traffic when each request is at most
// MaxChunk/2 and one turn's requests fit in the current chunk; requests
// over 512 KiB, and turns that need more than one MaxChunk chunk, take and
// return chunks on every turn. bumpalo sizes the next chunk
// by the last one and the request in the same way; next changes only after
// the heap gave the chunk, so a failure leaves the arena as it was.
BumpSlow(h: *ArenaHdr, size: int, line: bool): *u8 {
    unsafe "h is a live arena header" {
        var room := SizeOf[Chunk]()
        if line {
            room = LineAlign
        }
        next := (*h).next & ~LineBit
        flag := (*h).next & LineBit
        if size > next / 2 || size > next - room {
            if size > MaxBytes - room {
                return raw.Null[u8]()
            }
            c := NewChunk(h, room + size)
            if raw.IsNull(c) {
                return raw.Null[u8]()
            }
            var n := next
            if n < MaxChunk {
                n = n * 2
            }
            for n < MaxChunk && size > n / 2 {
                n = n * 2
            }
            n = n | flag
            (*h).next = n
            return raw.Offset(raw.PtrCast[Chunk, u8](c), room)
        }
        c := NewChunk(h, next)
        if raw.IsNull(c) {
            return raw.Null[u8]()
        }
        if next < MaxChunk {
            (*h).next = (next * 2) | flag
        }
        UseChunk(h, c)
        var off := 0
        if line {
            off = LineAlign - LineSkew
        }
        (*h).used = off + size
        return raw.Offset((*h).base, off)
    }
}

// BumpAligned serves size bytes at an align over ChunkAlign (every
// allocation of an align(n) type sits at n) from the current aligned
// chunk: the head of the list when its CurBit is set, allocated
// from the heap at 1 << ChunkShift >= align, so an offset that is a multiple
// of align is aligned in memory with no address read. The fast path is a
// load of the head, two compares, one add and one mask, as Bump's. Why the
// wrapping operations are exact: the current aligned chunk's bytes are at
// most MaxChunk (BumpAlignedSlow) and used stays within them, and align is
// at most MaxAlign, so AlignUp fits; size <= AlignedMax, so total - off is
// exact, and off + size passed the compare.
BumpAligned(h: *ArenaHdr, size: int, align: int): *u8 {
    if size > AlignedMax {
        return raw.Null[u8]()
    }
    unsafe "h is a live arena header" {
        c := (*h).chunks
        if IsAlignedCur(c) && align <= (1 << ChunkShift(c)) {
            ac := raw.PtrCast[Chunk, AlignedChunk](c)
            off := AlignUp((*ac).used, align)
            if size <= ((*ac).size & SizeMask()) -% off {
                (*ac).used = off +% size
                return raw.Offset(raw.PtrCast[Chunk, u8](c), off)
            }
        }
    }
    return BumpAlignedSlow(h, size, align)
}

// BumpAlignedSlow starts a current aligned chunk, or gives a large request
// a chunk of its own, by BumpSlow's rule: the next aligned chunk is twice
// the current one (FirstChunk when there is none), doubled again until the
// request is at most half of it, up to MaxChunk; a request still over half
// gets an own chunk (never current: no CurBit, linked below the head), and
// the current one stays current. So aligned chunk sizes only grow, Reset
// keeps the current (the largest), and a Reset loop whose aligned requests
// fit one chunk of at most MaxChunk makes no heap call after its first
// rounds (tests_arena.mk's "aligned requests bump in a chunk that Reset
// keeps"). A new current chunk is aligned to
// max(align, AlignedBase); the old current loses its CurBit and goes back
// to the heap at Reset or Free, with every own chunk (OwnBit marks one).
BumpAlignedSlow(h: *ArenaHdr, size: int, align: int): *u8 {
    unsafe "h is a live arena header" {
        // An aligned chunk hangs below the first chunk, so Reset and Free
        // never take it for the first; a header outside any chunk (a
        // hand-built fixture's) has none, and is refused.
        head := (*h).chunks
        if raw.IsNull(head) {
            return raw.Null[u8]()
        }
        a := &*(*h).src
        room := AlignUp(SizeOf[AlignedChunk](), align)
        need := room + size
        var n := FirstChunk
        if IsAlignedCur(head) {
            n = (*head).size & SizeMask()
            if n < MaxChunk {
                n = n * 2
            }
        }
        for n < MaxChunk && need > n / 2 {
            n = n * 2
        }
        if need > n / 2 {
            // An own chunk: its 16-byte record, its bytes align in.
            total := align + size
            p := AllocBytes(a, total, align)
            if raw.IsNull(p) {
                return raw.Null[u8]()
            }
            c := raw.PtrCast[u8, Chunk](p)
            raw.Write(c, Chunk(prev: raw.Null[Chunk](), size: total | (Log2(align) << ShiftAt) | OwnBit))
            LinkChunk(h, c)
            return raw.Offset(p, align)
        }
        var base := align
        if base < AlignedBase {
            base = AlignedBase
        }
        p := AllocBytes(a, n, base)
        if raw.IsNull(p) {
            return raw.Null[u8]()
        }
        if IsAlignedCur(head) {
            (*head).size = (*head).size & ~CurBit
        }
        raw.Write(raw.PtrCast[u8, AlignedChunk](p), AlignedChunk(prev: head, size: n | (Log2(base) << ShiftAt) | CurBit, used: need))
        (*h).chunks = raw.PtrCast[u8, Chunk](p)
        return raw.Offset(p, room)
    }
}

// Log2 of a power of two at most MaxAlign: its trailing zeros, one
// instruction.
Log2(n: int): int {
    return raw.Ctz64(Trunc[u64](n))
}

// SizeMask keeps a chunk size's bytes, without its shift, OwnBit and CurBit.
SizeMask(): int {
    return (1 << ShiftAt) - 1
}

// ChunkShift is an aligned chunk's alignment log2, 0 for an ordinary chunk.
ChunkShift(c: *Chunk): int {
    unsafe "c is a chunk of a live arena" {
        return ((*c).size >> ShiftAt) & 31
    }
}

// IsAlignedCur reports whether c (the list's head, or null) is the current
// aligned chunk.
IsAlignedCur(c: *Chunk): bool {
    if raw.IsNull(c) {
        return false
    }
    unsafe "c is a chunk of a live arena" {
        return ((*c).size & CurBit) != 0
    }
}

// FreeAligned returns an aligned chunk with the size and alignment it was
// allocated with.
FreeAligned(a: &Alloc, c: *Chunk) {
    unsafe "c is an aligned chunk of a live arena, unlinked by the caller's walk" {
        FreeBytes(a, raw.PtrCast[Chunk, u8](c), (*c).size & SizeMask(), 1 << ChunkShift(c))
    }
}

// LinkChunk links c into the list: at the head, or right below the current
// aligned chunk, which stays the head (an aligned chunk is never the first,
// so its prev is not null and the first chunk keeps prev null).
LinkChunk(h: *ArenaHdr, c: *Chunk) {
    unsafe "h is a live arena header; c a fresh chunk of it" {
        head := (*h).chunks
        if IsAlignedCur(head) {
            (*c).prev = (*head).prev
            (*head).prev = c
            return
        }
        (*c).prev = head
        (*h).chunks = c
    }
}

// NewChunk allocates a chunk of bytes bytes from the source and links it
// (LinkChunk); null when the source is out of memory.
NewChunk(h: *ArenaHdr, bytes: int): *Chunk {
    unsafe "h is a live arena header" {
        a := &*(*h).src
        p := AllocBytes(a, bytes, ChunkSrc(h))
        if raw.IsNull(p) {
            return raw.Null[Chunk]()
        }
        c := raw.PtrCast[u8, Chunk](p)
        raw.Write(c, Chunk(prev: raw.Null[Chunk](), size: bytes))
        LinkChunk(h, c)
        return c
    }
}

// HdrRoom is where the first chunk's free bytes start: after its record and
// the header.
HdrRoom(): int {
    return SizeOf[Chunk]() + AlignUp(SizeOf[ArenaHdr](), ChunkAlign)
}

// UseChunk makes c's bytes the current free bytes; the first chunk's start
// after the header it holds.
UseChunk(h: *ArenaHdr, c: *Chunk) {
    unsafe "h is a live arena header; c a chunk of it" {
        room := if raw.IsNull((*c).prev) { HdrRoom() } else { SizeOf[Chunk]() }
        (*h).base = raw.Offset(raw.PtrCast[Chunk, u8](c), room)
        (*h).used = 0
        (*h).size = (*c).size - room
    }
}

// IsCurrent reports whether c's bytes are the current free bytes.
IsCurrent(h: *ArenaHdr, c: *Chunk): bool {
    unsafe "h is a live arena header; c a chunk of it" {
        room := if raw.IsNull((*c).prev) { HdrRoom() } else { SizeOf[Chunk]() }
        return raw.Same(raw.Offset(raw.PtrCast[Chunk, u8](c), room), (*h).base)
    }
}

/// A new arena whose chunks come from the heap h. OutOfMemory when the heap
/// refuses the first chunk. O(1): one heap allocation, the first chunk.
export Arena.New(h: &Heap): OutOfMemory!Arena {
    return try NewArena(h.Alloc(), !Evaluating())
}

/// A new arena whose chunks come from a and go back to it. OutOfMemory when
/// a refuses the first chunk. O(1): one allocation from a, the first chunk.
///
/// Every chunk, the first included, is a request to a, and so are the
/// chunks of the arena's Subs, so a budget such as mem.Counting or
/// mem.Fixed bounds everything an API puts in the arena. A std API that
/// also uses the process heap says so (proc.Run's segments, a sim.Sim's
/// world). The arena holds a loan on a, and each Sub one on the arena, so
/// the allocator is not reset, freed or moved while either lives. An Arena
/// is not Send, so a source confined to one task stays on it.
///
/// Reset and Free return chunks to a, which decides what it reuses. A
/// mem.Fixed frees only its last block, so it gets bytes back only for
/// chunks freed in last-in first-out order: an arena that served a value
/// aligned over 8, or a Sub freed before its parent's later chunks, leaves
/// part of the Fixed used until Fixed.Reset.
///
/// Arena.NewIn(p.Alloc()) is a frame of the arena p: scratch memory for
/// one call, freed before p's later allocations. Its chunks are blocks of
/// p, at least FirstChunk (4 KiB) each, and Free gives them back newest
/// first, so p is back where it was. Nested frames whose chunks fit in p's
/// current chunk (p's chunks grow to 1 MiB) take nothing more from p's
/// source once warm; a request too large for p's chunks takes a chunk of
/// its own from the source and gives it back. p keeps a frame's bytes until
/// its Reset or Free when p started a new chunk while the frame lived, or
/// when the frame ends while a later block of p in the same chunk lives: a
/// value p gave there while the frame lived, or a newer frame's chunk. So a
/// call that takes a small value from p while its frame lives keeps 4 KiB
/// or more of p on every call, and frames nested deeper than one chunk of
/// p holds keep one more chunk on every round: Sub suits those.
export Arena.NewIn(a: &Alloc): OutOfMemory!Arena from a {
    return try NewArena(a, false)
}

// NewArena is NewIn, and with line a line arena (LineBit): Arena.New's
// natively, its Subs', HeapArena's; never NewIn's, whose source is the
// caller's and is asked exactly what it was at 438c5c70.
NewArena(a: &Alloc, line: bool): OutOfMemory!Arena from a {
    var src := ChunkAlign
    var flag := 0
    if line {
        src = LineAlign
        flag = LineBit
    }
    p := AllocBytes(a, FirstChunk, src)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    unsafe "p is fresh memory for the first chunk, its record and the header; a outlives the arena by the result's loan (from a)" {
        c := raw.PtrCast[u8, Chunk](p)
        raw.Write(c, Chunk(prev: raw.Null[Chunk](), size: FirstChunk))
        q := raw.Offset(p, SizeOf[Chunk]())
        hdr := raw.PtrCast[u8, ArenaHdr](q)
        room := HdrRoom()
        raw.Write(hdr, ArenaHdr(src: raw.Of(a), own: Alloc(ctx: q, table: &ArenaTable),
            base: raw.Offset(p, room), used: 0, size: FirstChunk - room, chunks: c, next: (FirstChunk * 2) | flag))
        return Arena(hdr: hdr)
    }
}

/// Moves x into the arena and returns a writable reference to it, in fresh
/// memory, disjoint from every other piece, on every call. OutOfMemory when
/// no chunk can be had, and then the caller still owns x. T may not be linear
/// (E0701 at the call, which names Box): the arena frees x unconsumed. O(1)
/// amortized; allocates SizeOf[T]() bytes.
export Arena.Put[T](&self, x: T into self): OutOfMemory!mut T from self {
    p := Bump(self.hdr, SizeOfT[T](), AlignOfT[T]())
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    q := BytesAs[T](p)
    SlotWrite(q, 0, x)   // fresh memory for one T
    return SlotMut(q, 0) // in the arena, which outlives the loan on self
}

/// A writable slice of n zero-filled elements in the arena. T must be
/// zero-valid, a type for which all-zero bytes are a valid value (E0701
/// otherwise), and n < 0 panics (code 8). OutOfMemory when no chunk can be had,
/// or when the byte count does not fit in an int (never an overflow panic).
/// O(n); allocates n elements.
export Arena.Slice[T](&self, n: int): OutOfMemory!mut []T {
    if n < 0 {
        raw.Fail(8, "Arena.Slice: negative count")
    }
    size := SizeOfT[T]()
    if size > 0 && n > MaxBytes / size {
        fail OutOfMemory
    }
    bytes := n * size
    var p := raw.Null[u8]()
    if bytes >= LargeSlice && AlignOfT[T]() <= ChunkAlign && IsLine(self.hdr) {
        p = BumpLine(self.hdr, bytes)
    }
    if raw.IsNull(p) {
        p = Bump(self.hdr, bytes, AlignOfT[T]())
    }
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    ZeroBytes(p, bytes) // T is zero-valid (check)
    return SlotViewMut(BytesAs[T](p), n)
}

/// As Slice, with the first element at a multiple of align bytes (or of
/// T's own alignment when that is larger): a buffer for vector loads, or
/// one on a cache line. align is a power of two from 1 to 65,536, else it
/// panics (code 8), as n < 0 does. OutOfMemory as Slice. O(n);
/// allocates n elements and at most align - 1 bytes of padding.
///
/// At compile time an align over T's own alignment panics (E0901), since
/// a slice made there becomes static data aligned to T only. For an
/// aligned static table, give the element type align(n) (n up to 65,536)
/// and use Slice: static data keeps an align(n) type's alignment on every
/// profile.
export Arena.SliceAligned[T](&self, n: int, align: int): OutOfMemory!mut []T {
    if n < 0 {
        raw.Fail(8, "Arena.SliceAligned: negative count")
    }
    if align < 1 || align > MaxAlign || (align & (align - 1)) != 0 {
        raw.Fail(8, "Arena.SliceAligned: alignment not a power of two from 1 to 65536")
    }
    if align > AlignOfT[T]() && Evaluating() {
        raw.Fail(8, "Arena.SliceAligned: an alignment over the element type's at compile time, where static data keeps only the type's; use an align(n) element type")
    }
    size := SizeOfT[T]()
    if size > 0 && n > MaxBytes / size {
        fail OutOfMemory
    }
    bytes := n * size
    var at := AlignOfT[T]()
    if align > at {
        at = align
    }
    p := Bump(self.hdr, bytes, at)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    ZeroBytes(p, bytes) // T is zero-valid (check)
    return SlotViewMut(BytesAs[T](p), n)
}

/// A copy of s in the arena. OutOfMemory when no chunk can be had.
/// O(s.Len()); allocates s.Len() bytes, none for "".
export Arena.Dup(&self, s: str): OutOfMemory!str from self {
    n := s.Len()
    if n == 0 {
        unsafe "an empty str needs no bytes; its data pointer stays nonnull" {
            return raw.Str(DanglingBytes(), 0)
        }
    }
    p := Bump(self.hdr, n, 1)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    unsafe "p is fresh memory for n bytes; s is valid UTF-8" {
        raw.Copy(p, raw.StrData(s), n)
        return raw.Str(p, n)
    }
}

/// Formats f with args into a new str in the arena, with the holes and
/// specs of Print. Any WriteError, a user Format's included, becomes
/// OutOfMemory. O(output length); allocates the text, growing as Text does.
export Arena.Fmt[A: Fields[Format] + Plain](&self, const f: str, args: A): OutOfMemory!str from self

/// A new, independent arena on the same chunk source, as Arena.New or
/// Arena.NewIn would make; it holds a loan on self, so it ends before self
/// does. Use it for scratch memory that is freed before the parent's.
/// OutOfMemory when the source refuses the first chunk. O(1): one
/// allocation from the source, the first chunk. Scratch for each of many
/// calls is cheaper as a frame, Arena.NewIn(self.Alloc()), whose chunks
/// come from self, while the frames live at once fit in one chunk of self
/// (see NewIn).
export Arena.Sub(&self): OutOfMemory!Arena {
    unsafe "hdr is this arena's live header, and its source outlives the result: the result holds self's loan, and self holds the source's" {
        return try NewArena(&*(*self.hdr).src, IsLine(self.hdr))
    }
}

/// The arena as an Alloc, the same one on every call, for containers that
/// take a &Alloc and should live in the arena. O(1). Its free gives back
/// the last block bumped, or a block that has a chunk of its own while
/// that chunk is the arena's newest; any other block stays until Reset or
/// Free.
export Arena.Alloc(&self): &Alloc {
    unsafe "hdr is this arena's live header, alive while self is" {
        return &(*self.hdr).own
    }
}

/// Ends every loan of the arena and makes its memory free for reuse: the
/// values it held are gone, and the compiler rejects a later use of them.
/// O(chunks); returns memory to the chunk source and allocates nothing.
///
/// It keeps the first chunk, the current chunk and the current aligned
/// chunk, and returns every other chunk to the source; the next chunk size
/// stays as it was. So an arena reset on every turn of a
/// loop settles into reusing the same memory when each request is at most
/// 512 KiB and one turn's requests fit in the current chunk (up to 1 MiB).
/// Otherwise the extra chunks are taken from the source and returned on
/// every turn.
export Arena.Reset(mut self) ends self {
    h := self.hdr
    unsafe "hdr is this arena's live header; nothing borrows the arena now" {
        a := &*(*h).src
        var first := raw.Null[Chunk]()
        var cur := raw.Null[Chunk]()
        top := (*h).chunks
        acur := if IsAlignedCur(top) { top } else { raw.Null[Chunk]() }
        var c := top
        for !raw.IsNull(c) {
            prev := (*c).prev
            if ChunkShift(c) != 0 {
                // Never first or the ordinary current, so tested first.
                if !raw.Same(c, acur) {
                    FreeAligned(a, c)
                }
            } else if raw.IsNull(prev) {
                first = c
            } else if raw.IsNull(cur) && IsCurrent(h, c) {
                cur = c
            } else {
                size := (*c).size
                FreeBytes(a, raw.PtrCast[Chunk, u8](c), size, ChunkSrc(h))
            }
            c = prev
        }
        if raw.IsNull(first) {
            // A header outside any chunk (a compile-time fixture's): only
            // the bump position can be reset (it has no aligned chunk).
            (*h).used = 0
            return
        }
        var keep := first
        if raw.IsNull(cur) {
            UseChunk(h, first)
        } else {
            (*cur).prev = first
            keep = cur
            UseChunk(h, cur)
        }
        if !raw.IsNull(acur) {
            ac := raw.PtrCast[Chunk, AlignedChunk](acur)
            (*ac).prev = keep
            (*ac).used = SizeOf[AlignedChunk]()
            keep = acur
        }
        (*h).chunks = keep
    }
}

/// Ends the arena and returns every chunk to the chunk source. O(chunks).
export Arena.Free(self) ends self {
    Arena(hdr) := self
    h := hdr
    unsafe "hdr is this arena's live header; nothing borrows the arena now" {
        a := &*(*h).src
        var first := raw.Null[Chunk]()
        var c := (*h).chunks
        for !raw.IsNull(c) {
            prev := (*c).prev
            if ChunkShift(c) != 0 {
                FreeAligned(a, c)
            } else if raw.IsNull(prev) {
                first = c
            } else {
                size := (*c).size
                FreeBytes(a, raw.PtrCast[Chunk, u8](c), size, ChunkSrc(h))
            }
            c = prev
        }
        if !raw.IsNull(first) {
            size := (*first).size
            FreeBytes(a, raw.PtrCast[Chunk, u8](first), size, ChunkSrc(h))
        }
    }
}

/// The bytes a's chunks take from the source, chunk headers included, for
/// std's tests of how much an operation keeps in an arena. Internal to std:
/// the chunk layout is not a public fact. O(chunks).
export ArenaHeld(a: &Arena): int {
    var n := 0
    unsafe "hdr is this arena's live header, alive while a is" {
        var c := (*a.hdr).chunks
        for !raw.IsNull(c) {
            n += (*c).size & SizeMask()
            c = (*c).prev
        }
    }
    return n
}

/// An arena on a Heap that may move to another task, for std's own tasks
/// (proc.Run's readers). Internal to std. Arena is not Send because its
/// source may be confined to one task; a HeapArena's source is always a
/// Heap, which is Share, so whichever task holds it may use it.
export linear mut view struct HeapArena {
    x: Arena
}

unsafe impl Send for HeapArena {}

/// A new arena on h, as Arena.New. O(1): one heap allocation.
export HeapArena.New(h: &Heap): OutOfMemory!HeapArena {
    x := try Arena.New(h)
    return HeapArena(x: x)
}

/// The arena itself, for the task that holds this value. O(1).
export HeapArena.Arena(&self): &Arena from self {
    return &self.x
}

/// Ends the arena and returns every chunk to its heap. O(chunks).
export HeapArena.Free(self) {
    HeapArena(x) := self
    x.Free()
}

// EvalArena: the arena of
// one compile-time evaluation, and the page source under it. The evaluator
// calls MakeEvalArena (Config.EvalArena) for EvalArena() and Gen.Arena(),
// once per evaluation, and replaces EvalPageAlloc and EvalPageFree by key
// (Config.PageAlloc, Config.PageFree) with a fresh zero-filled evaluation
// allocation and its release. So the arena is this file's ordinary Arena
// code over evaluation memory, and lives until the evaluation ends: nothing
// frees it, and its Heap record and Arena value sit in pages of their own.
// None of the three runs natively: EvalArena() is legal only where the
// evaluator runs (E0705), so the native bodies are a panic.

// The page source's allocate: size bytes, zero-filled, aligned to
// ChunkAlign; null when size is negative.
EvalPageAlloc(size: int): *u8 {
    raw.Fail(8, "EvalPageAlloc runs only at compile time")
    return raw.Null[u8]()
}

// The page source's free of a block EvalPageAlloc gave, with its size.
EvalPageFree(p: *u8, size: int) {
    raw.Fail(8, "EvalPageFree runs only at compile time")
}

// The Heap table under an evaluation's arena: alloc and free are the page
// source's, resize never grows in place (the arena does not call it).
const EvalPageTable = AllocTable(alloc: EvalPageAllocEntry, resize: EvalPageResizeEntry, free: EvalPageFreeEntry)

// Any power of two up to MaxAlign, like ArenaAllocEntry: evaluation
// memory has no addresses, so an alignment cannot be observed there and
// every block serves it; an aligned chunk's bytes start at an offset that is
// a multiple of its alignment, which is all the arena relies on.
EvalPageAllocEntry(ctx: *u8, size: int, align: int): *u8 {
    if size < 0 || align <= 0 || align > MaxAlign || (align & (align - 1)) != 0 {
        return raw.Null[u8]()
    }
    return EvalPageAlloc(size)
}

EvalPageResizeEntry(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    return false
}

EvalPageFreeEntry(ctx: *u8, p: *u8, size: int, align: int) {
    if !raw.IsNull(p) {
        EvalPageFree(p, size)
    }
}

// Config.EvalArena: a Heap over the page source, an Arena on it, both in
// pages of their own, and the arena's reference. An evaluation out of memory
// stops at its step limit before a page fails, so a null page is a panic.
MakeEvalArena(): &Arena {
    unsafe "fresh evaluation pages, written once and never freed: they live until the evaluation ends" {
        hp := raw.PtrCast[u8, Heap](EvalPageAlloc(SizeOf[Heap]()))
        if raw.IsNull(hp) {
            raw.Fail(8, "MakeEvalArena: no page")
        }
        raw.Write(hp, MakeHeap(raw.Null[u8](), raw.Of(&EvalPageTable)))
        ar := Arena.New(&*hp) else {
            raw.Fail(8, "MakeEvalArena: no page")
        }
        ap := raw.PtrCast[u8, Arena](EvalPageAlloc(SizeOf[Arena]()))
        if raw.IsNull(ap) {
            ar.Free()
            raw.Fail(8, "MakeEvalArena: no page")
        }
        raw.Write(ap, ar)
        return &*ap
    }
}

// Evaluating reports whether a compile-time evaluation runs the caller.
// The evaluator replaces it by key (Config.Evaluating) with true; natively it is false. Every arena an
// evaluation reaches, MakeEvalArena's, a Sub, an Arena.NewIn over any Alloc
// in evaluation memory, holds memory that becomes static data aligned to
// its type only, so SliceAligned asks this and not the arena.
// O(1).
Evaluating(): bool {
    return false
}
