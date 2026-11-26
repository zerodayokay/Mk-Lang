// Why: Mk has no C runtime, so the process needs its own general-purpose
// allocator, fast for the small blocks most programs churn through and
// safe to call from any thread.
//
// What: the process heap behind ProcessHeap: HeapAlloc, HeapResize,
// HeapUsable and HeapFree over segments, pages and size classes (Classes),
// the per-carrier magazines (Mag, MagInit), and TrimHeap, which returns the
// pages of empty segments.
//
// How: it follows mimalloc: 4 MiB segments of 64 KiB pages, 32 size classes
// carved lazily from pages, large spans of whole pages, huge blocks in their
// own reservation. A shared cache under one spinlock serves every thread,
// and a per-carrier magazine of each small class fronts it for code running
// on a task, and for Main in a program with no task, which runs on carrier
// 0's system stack; there is no lock-free list for frees from another
// thread. The heap never panics: out of memory is null.

import std.raw
import std.sys

const SegBytes: int = 4 << 20
const PageBytes: int = 65536
const SmallMax: int = 8192
const LargeMax: int = 1 << 20
const SegMagic: u64 = 0x314B_4D47_4553_4B4D
const KindSmall: u32 = 1
const KindHuge: u32 = 2
const SpanHead: u16 = 0xFFFF
const SpanTail: u16 = 0xFFFE
// HeaderCommit is the committed part of a segment's header page.
const HeaderCommit: int = 8192

/// The 32 small size classes: 16 to 128 by 16,
/// then four per doubling.
export const Classes: [32]int = [16, 32, 48, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512, 640,
    768, 896, 1024, 1280, 1536, 1792, 2048, 2560, 3072, 3584, 4096, 5120, 6144, 7168, 8192]

// One 64 KiB page's metadata. class is 1 + the class index, SpanHead or
// SpanTail for a large span, 0 free.
struct PageMeta {
    class: u16
    span: u16
    block: u32
    capacity: u32
    used: u32
    free: *u8
    bump: u32
    committed: u32
    next: *PageMeta
    prev: *PageMeta
    in_list: u32
    index: u32
    seg: *SegHeader
}

// At the base of every segment (small and large pages), and of every huge
// reservation, whose block starts at base + max(64 KiB, align).
struct SegHeader {
    magic: u64
    base: uint
    kind: u32
    // idle is 1 while the segment is counted in HeapState.empty: every
    // page free and some page committed (TrimHeap's candidates).
    idle: u32
    // A small segment's free pages; for a huge reservation, the bytes it
    // reserves past its first commit (HeapState.spare's share).
    free_pages: u64
    bytes: int
    cap: int
    next: *SegHeader
    // The list of idle segments (HeapState.idle_head), while idle == 1:
    // O(1) to add, remove and trim.
    idle_prev: *SegHeader
    idle_next: *SegHeader
    pages: [64]PageMeta
}

/// The heap's shared cache, in the RtState page; mags is the per-carrier
/// magazine array (MagInit), null until then.
export struct HeapState {
    lock: raw.Atomic[u32]
    segs: *SegHeader
    lists: [32]*PageMeta
    mags: *Mag
    // empty counts the small segments with every page free and some page
    // still committed (idle == 1); over mirrors empty > TrimKeepSegs for
    // TrimHeap's test without the lock, written under it on each crossing.
    empty: int
    over: raw.Atomic[u32]
    // The idle segments, newest at the head; TrimHeap trims from the tail,
    // the one empty longest. Under lock.
    idle_head: *SegHeader
    idle_tail: *SegHeader
    // spare is the address space huge blocks may still reserve past what
    // they commit (HugeAlloc takes, HeapFree gives back): HugeSpareBudget
    // from HeapInit, or 0 when the address space is limited
    // (sys.AddressLimited) and before HeapInit.
    spare: raw.Atomic[int]
}

// A carrier's magazine of one small class: a stack of free blocks that only
// code running on that carrier touches, so most small allocations and frees
// take no lock (magazines, Bonwick and Adams, USENIX 2001; the per-CPU
// caches of tcmalloc). A task cannot change carrier inside HeapAlloc or
// HeapFree (neither suspends), and the carrier index comes from the running
// task's slot header, which the scheduler sets before the switch in and
// clears after the switch out; scheduler code and foreign threads have no
// slot and use the shared cache. In a program with no task there is no
// slot: code on carrier 0's system stack, which is Main's, takes carrier
// 0's magazine, and every other thread the shared cache (MagOf). An empty
// magazine refills cap / 2 blocks
// under one lock; a full one gives half back under one lock, so the shared
// lock is taken at most once per cap / 2 operations. A block enters the
// magazine of its page's class (its own metadata), never of a request's
// size, so a block resized in place to a smaller size goes back to its
// class. Bounded: cap is 16 blocks, fewer for classes over 1 KiB (16 KiB
// per class at most), so a carrier holds at most about 200 KiB.
struct Mag {
    n: u32
    cap: u32
    items: [16]*u8
}

const MagCarriers: int = 256

/// MagInit maps the magazines of every possible carrier (256 x 32 x 136
/// bytes, about 1.1 MiB, committed zero; pages are touched only by the
/// carriers that run). False when the OS refuses: the heap then uses the
/// shared cache alone.
/// O(1): one reservation; allocates nothing from the Heap.
export MagInit(): bool {
    bytes := MagCarriers * 32 * SizeOf[Mag]()
    p := sys.Reserve(bytes, 65536)
    if raw.IsNull(p) {
        return false
    }
    if !sys.Commit(p, bytes) {
        sys.Release(p, bytes)
        return false
    }
    unsafe "the RtState page, before any task exists" {
        (*HS()).mags = raw.PtrCast[u8, Mag](p)
    }
    return true
}

// MagOf is the calling code's carrier's magazine of class ci, or null when
// it has none or before MagInit. With tasks, the carrier is the running
// task's, from its slot header; off a task stack there is none.
//
// Without tasks (mono folds UsesTasks), no slot exists, and Main runs
// on carrier 0's system stack, [sys_lo[0], sys_hi[0]), which StartOn
// records on Main's thread before Main runs (std/task/start.mk) and
// nothing records or clears again: ClearSysStack and the other
// SetSysStack calls belong to carriers and pinned threads, which only a
// program with tasks has. Code whose stack pointer lies there takes
// magazine 0, and the magazine stays one carrier's, touched by one
// execution at a time:
// - Only Main's thread runs on that stack. Any other thread (one C code
//   starts, the system's console-control handler thread on Windows, a
//   thread a C library makes) has its own stack and takes the shared
//   cache, as a foreign thread does with tasks; a block may move between
//   the two either way, as between carriers.
// - Before StartOn records the bounds, sys_lo[0] is 0 and every call takes
//   the shared cache (Start's streams, arguments and environment, on the
//   initial stack); the exit (Finish, flush, exit hooks) runs on Main's
//   stack, as Main.
// - Code that interrupts Main on its own stack never allocates: Linux's
//   signal handlers run on the 64 KiB alternate stack (ThreadInit), and
//   allocate nothing even where it could not be installed (SigCount,
//   SigFatal, SigNop and the terminal hooks make system calls only);
//   Windows' vectored handler and std.runtime's fault filters run on the
//   faulting stack and must not allocate (OnFault never returns and
//   allocates nothing). So no allocation nests inside a magazine operation
//   here, as none does on a task's carrier.
// - A user export callable from C makes UsesTasks true (its thunk runs the
//   call as a task), so no host thread enters such a program through an
//   export. A Mk function C calls back on a thread of its own runs on that
//   thread's stack: the first case.
MagOf(hs: *HeapState, ci: int): *Mag {
    unsafe "the RtState page and the running task's slot header" {
        if raw.IsNull((*hs).mags) {
            return raw.Null[Mag]()
        }
        var carrier := 0
        if UsesTasks() {
            h := CurrentSlot()
            if raw.IsNull(h) || (*h).carrier == NoCarrier {
                return raw.Null[Mag]()
            }
            carrier = int((*h).carrier)
        } else {
            sp := StackPointer()
            st := R()
            // Edge: StartOn's SetSysStack stored carrier 0's entry on Main's
            // thread, in program order, before Main ran on that stack, so
            // Main reads it; any other thread's stack pointer lies outside
            // it whether it reads the entry, 0 or half of it (hi is stored
            // first, and a hi of 0 matches no stack pointer).
            lo := Trunc[uint]((*st).sys_lo[0].Load())
            if lo == 0 || sp < lo || sp >= Trunc[uint]((*st).sys_hi[0].Load()) {
                return raw.Null[Mag]()
            }
        }
        m := raw.Offset((*hs).mags, carrier * 32 + ci)
        if (*m).cap == 0 {
            var c := 16384 / Classes[ci]
            if c > 16 {
                c = 16
            }
            if c < 2 {
                c = 2
            }
            (*m).cap = Trunc[u32](c)
        }
        return m
    }
}

// MagAlloc pops m, or refills it from the shared cache under one lock. A
// refill keeps SmallAlloc's order: p is its first block, and the rest are
// reversed in place, so the pops hand them out as SmallAlloc gave them
// (address order on a fresh page), not last first: a program that builds
// a structure and walks it walks memory forward (tests_heap.mk checks the
// order; the deepfree benchmark with one task is such a program).
MagAlloc(hs: *HeapState, m: *Mag, ci: int): *u8 {
    unsafe "m is this carrier's magazine" {
        if (*m).n > 0 {
            (*m).n -= 1
            return (*m).items[int((*m).n)]
        }
        Lock(&(*hs).lock)
        p := SmallAlloc(hs, ci)
        if !raw.IsNull(p) {
            for (*m).n < (*m).cap / 2 {
                q := SmallAlloc(hs, ci)
                if raw.IsNull(q) {
                    break
                }
                (*m).items[int((*m).n)] = q
                (*m).n += 1
            }
        }
        Unlock(&(*hs).lock)
        var i := 0
        var j := int((*m).n) - 1
        for i < j {
            t := (*m).items[i]
            (*m).items[i] = (*m).items[j]
            (*m).items[j] = t
            i += 1
            j -= 1
        }
        return p
    }
}

// MagFree pushes p on m, or gives the newer half of a full m back with p
// under one lock: items[cap / 2..n) in the order they were freed, then p,
// so the pages see the frees in their order (tests_heap.mk checks it). n
// drops to cap / 2 before the first FreeSmall, so m never counts a block
// it has given back.
MagFree(hs: *HeapState, m: *Mag, p: *u8) {
    unsafe "m is this carrier's magazine; p is a small block" {
        if (*m).n < (*m).cap {
            (*m).items[int((*m).n)] = p
            (*m).n += 1
            return
        }
        h := (*m).cap / 2
        e := (*m).n
        (*m).n = h
        Lock(&(*hs).lock)
        for j in int(h)..int(e) {
            FreeSmall(hs, (*m).items[j])
        }
        FreeSmall(hs, p)
        Unlock(&(*hs).lock)
    }
}

HS(): *HeapState {
    unsafe "Init ran, so R() is the page it mapped, which lives until exit" {
        return raw.Of(&(*R()).hs)
    }
}

// Real reports whether p is a block, not the dangling pointer of a
// zero-size request (below 4 MiB).
Real(p: *u8): bool {
    return raw.Addr(p) >= Trunc[uint](SegBytes)
}

// SmallClassOf is the smallest class holding n (1 <= n <= SmallMax), with
// no loop. O(1): the classes are 16 to 128 by 16, then four per doubling,
// so the index is computed from n's top bits (the bin computation of
// mimalloc's mi_bin, or jemalloc's size-class table, without a table):
// (n + 15) / 16 - 1 up to 128; above, with v = n - 1 and b its top bit (7
// to 12), 8 + 4 (b - 7) + the two bits of v below b. RT-Heap-SmallClassOf
// checks it against ClassIndex and the scan for every n.
SmallClassOf(n: int): int {
    if n <= 128 {
        return (n + 15) / 16 - 1
    }
    v := Trunc[u64](n - 1)
    b := 63 - raw.Clz64(v)
    return 8 + 4 * (b - 7) + Trunc[int]((v >> Trunc[u64](b - 2)) & 3)
}

// ClassIndex is the smallest class holding n whose size is a multiple of
// align (1 <= n <= SmallMax, align a power of two <= 4096): SmallClassOf's
// class, kept in 0 to 31 for any n, then a step. Every class is a
// multiple of 16, so align <= 16 needs no step; a larger align steps up to
// the first class that is its multiple (a few steps; n >= align, so the
// step ends by the next power of two). RT-Heap-ClassIndex checks the
// result against the linear scan for every n and align.
ClassIndex(n: int, align: int): int {
    var i := SmallClassOf(n)
    if i < 0 {
        i = 0
    }
    if i > 31 {
        return 31
    }
    // A power of two of 16 or less divides every class, so the step below
    // would not move: skip it, and its division by a value only known at
    // run time. Any other align (0 included, which the division traps on)
    // still takes the step.
    if align >= 1 && align <= 16 && align & (align - 1) == 0 {
        return i
    }
    for i < 31 && Classes[i] % align != 0 {
        i += 1
    }
    return i
}

// PageAddr is the first byte of page i of segment s.
PageAddr(s: *SegHeader, i: int): *u8 {
    unsafe "page i lies inside segment s" {
        return raw.FromAddr[u8](raw.Addr(s) + Trunc[uint](i * PageBytes))
    }
}

// NewSegment reserves a segment, commits its header and links it.
NewSegment(hs: *HeapState): *SegHeader {
    p := sys.Reserve(SegBytes, SegBytes)
    if raw.IsNull(p) {
        return raw.Null[SegHeader]()
    }
    if !sys.Commit(p, HeaderCommit) {
        sys.Release(p, SegBytes)
        return raw.Null[SegHeader]()
    }
    unsafe "p is the fresh segment, its header page committed and zero" {
        s := raw.PtrCast[u8, SegHeader](p)
        (*s).magic = SegMagic
        (*s).base = raw.Addr(p)
        (*s).kind = KindSmall
        (*s).free_pages = AllFree
        (*s).bytes = SegBytes
        for i in 0..64 {
            (*s).pages[i].index = Trunc[u32](i)
            (*s).pages[i].seg = s
        }
        (*s).next = (*hs).segs
        (*hs).segs = s
        return s
    }
}

// CommitPages commits pages [i, i+k) of s that are not committed yet.
CommitPages(s: *SegHeader, i: int, k: int): bool {
    unsafe "pages of segment s" {
        for j in i..i + k {
            if (*s).pages[j].committed == 0 {
                if !sys.Commit(PageAddr(s, j), PageBytes) {
                    return false
                }
                (*s).pages[j].committed = 1
            }
        }
    }
    return true
}

// FindRun finds k free pages in a row in some segment (a new one if
// needed); it returns the segment and the first index, or a null segment.
// First fit over the segment list (RunIn per segment): O(segments x k)
// word operations (k <= 16); the header page (bit 0) is never free. Worst case: every segment
// fragmented below k pages, all walked (mimalloc keeps span queues by size
// to avoid the walk; the change to make if a profile shows the walk).
FindRun(hs: *HeapState, k: int): (s: *SegHeader, i: int) {
    unsafe "the segment list of the cache, under its lock" {
        var s := (*hs).segs
        for !raw.IsNull(s) {
            if (*s).kind == KindSmall {
                i := RunIn((*s).free_pages, k)
                if i >= 0 {
                    return (s: s, i: i)
                }
            }
            s = (*s).next
        }
        n := NewSegment(hs)
        return (s: n, i: 1)
    }
}

// RunIn is the lowest i with bits i to i + k - 1 of f all set, or -1:
// f AND-ed with itself shifted right by 1 to k - 1 has bit i set exactly
// then (a shift brings in zeros, so no run passes bit 63), and Ctz64 finds
// the lowest. O(k). RT-Heap-RunIn checks it against the bit-by-bit scan.
RunIn(f: u64, k: int): int {
    var run := f
    for j in 1..k {
        if run == 0 {
            return -1
        }
        run = run & (f >> Trunc[u64](j))
    }
    return if run == 0 { -1 } else { raw.Ctz64(run) }
}

// TakePages marks pages [i, i+k) of s used and commits them.
TakePages(s: *SegHeader, i: int, k: int): bool {
    if !CommitPages(s, i, k) {
        return false
    }
    unsafe "pages of segment s, under the cache lock" {
        for j in i..i + k {
            (*s).free_pages = (*s).free_pages & ~(Trunc[u64](1) << Trunc[u64](j))
        }
        if (*s).idle == 1 {
            IdleUnlink(s)
        }
    }
    return true
}

// IdleLink adds s to the head of the idle list and counts it (idle 0 to
// 1); IdleUnlink removes it and uncounts it (1 to 0). The cache lock is
// held. O(1) each.
IdleLink(s: *SegHeader) {
    hs := HS()
    unsafe "the shared cache and segment s, under its lock" {
        (*s).idle = 1
        (*s).idle_prev = raw.Null[SegHeader]()
        (*s).idle_next = (*hs).idle_head
        if raw.IsNull((*hs).idle_head) {
            (*hs).idle_tail = s
        } else {
            (*(*hs).idle_head).idle_prev = s
        }
        (*hs).idle_head = s
        EmptyAdd(1)
    }
}

IdleUnlink(s: *SegHeader) {
    hs := HS()
    unsafe "the shared cache and segment s, under its lock" {
        p := (*s).idle_prev
        n := (*s).idle_next
        if raw.IsNull(p) {
            (*hs).idle_head = n
        } else {
            (*p).idle_next = n
        }
        if raw.IsNull(n) {
            (*hs).idle_tail = p
        } else {
            (*n).idle_prev = p
        }
        (*s).idle_prev = raw.Null[SegHeader]()
        (*s).idle_next = raw.Null[SegHeader]()
        (*s).idle = 0
        EmptyAdd(-1)
    }
}

// AllFree is a small segment's free_pages with every page but the header
// free.
const AllFree: u64 = 0xFFFF_FFFF_FFFF_FFFE

// TrimKeepSegs is how many empty segments keep their pages.
const TrimKeepSegs: int = 8

// EmptyAdd changes the count of committed empty segments by d; the cache
// lock is held.
EmptyAdd(d: int) {
    hs := HS()
    unsafe "the shared cache, under its lock" {
        (*hs).empty += d
        if d > 0 && (*hs).empty == TrimKeepSegs + 1 {
            // Edge: none needed; a hint for TrimHeap, which rechecks under
            // the lock.
            (*hs).over.Store(1)
        } else if d < 0 && (*hs).empty == TrimKeepSegs {
            (*hs).over.Store(0)
        }
    }
}

/// TrimHeap decommits the pages of up to TrimHeapBatch empty segments
/// beyond the TrimKeepSegs empty ones that keep theirs, the
/// ones empty longest first, and says whether it did any. Called by an idle
/// carrier (std.task Idle), never on an allocation or a free. Each segment
/// costs O(1) list steps and one system call over its pages above the
/// header, under the cache lock (the pages are free, so no block is there
/// and nobody reads them; a later TakePages commits each page again through
/// its committed flag, so the Decommit cannot move outside the lock without
/// a TakePages committing a page it then drops). The lock is let go between
/// segments, so an allocation waits for one system call at most.
export TrimHeap(): bool {
    hs := HS()
    var did := false
    unsafe "the shared cache, under its lock" {
        for _ in 0..TrimHeapBatch {
            // Edge: none needed; a hint, and a missed trim waits for the
            // next idle pass.
            if (*hs).over.Load() == 0 {
                return did
            }
            Lock(&(*hs).lock)
            s := (*hs).idle_tail
            if (*hs).empty <= TrimKeepSegs || raw.IsNull(s) {
                Unlock(&(*hs).lock)
                return did
            }
            IdleUnlink(s)
            sys.Decommit(PageAddr(s, 1), SegBytes - PageBytes)
            for j in 1..64 {
                (*s).pages[j].committed = 0
            }
            Unlock(&(*hs).lock)
            did = true
        }
    }
    return did
}

// TrimHeapBatch bounds the segments one TrimHeap call trims (16 MiB), so
// an idle carrier returns to its poller soon.
const TrimHeapBatch: int = 4

/// HeapEmptySegments is the count of empty segments that keep committed
/// pages, for the trim tests.
/// O(1): one read of the kept count under the cache lock; allocates nothing.
export HeapEmptySegments(): int {
    hs := HS()
    unsafe "the shared cache, under its lock" {
        Lock(&(*hs).lock)
        n := (*hs).empty
        Unlock(&(*hs).lock)
        return n
    }
}

// FreePages returns pages [i, i+k) of s to the segment (still committed).
FreePages(s: *SegHeader, i: int, k: int) {
    unsafe "pages of segment s, under the cache lock" {
        for j in i..i + k {
            (*s).pages[j].class = 0
            (*s).pages[j].span = 0
            (*s).pages[j].used = 0
            (*s).pages[j].bump = 0
            (*s).pages[j].free = raw.Null[u8]()
            (*s).free_pages = (*s).free_pages | (Trunc[u64](1) << Trunc[u64](j))
        }
        if (*s).free_pages == AllFree && (*s).idle == 0 {
            IdleLink(s)
        }
    }
}

// ListPush and ListRemove keep a class's list of pages with free blocks.
ListPush(hs: *HeapState, ci: int, m: *PageMeta) {
    unsafe "a page of the cache, under its lock" {
        h := (*hs).lists[ci]
        (*m).prev = raw.Null[PageMeta]()
        (*m).next = h
        if !raw.IsNull(h) {
            (*h).prev = m
        }
        (*hs).lists[ci] = m
        (*m).in_list = 1
    }
}

ListRemove(hs: *HeapState, ci: int, m: *PageMeta) {
    unsafe "a page of the cache, under its lock" {
        if raw.IsNull((*m).prev) {
            (*hs).lists[ci] = (*m).next
        } else {
            (*(*m).prev).next = (*m).next
        }
        if !raw.IsNull((*m).next) {
            (*(*m).next).prev = (*m).prev
        }
        (*m).next = raw.Null[PageMeta]()
        (*m).prev = raw.Null[PageMeta]()
        (*m).in_list = 0
    }
}

// SmallAlloc pops a block of class ci (the small path, one cache).
SmallAlloc(hs: *HeapState, ci: int): *u8 {
    unsafe "pages of the cache, under its lock" {
        var m := (*hs).lists[ci]
        for !raw.IsNull(m) {
            if !raw.IsNull((*m).free) {
                p := (*m).free
                (*m).free = raw.Read(raw.PtrCast[u8, *u8](p))
                (*m).used += 1
                return p
            }
            if (*m).bump < (*m).capacity {
                p := raw.Offset(PageAddr((*m).seg, int((*m).index)), int((*m).bump) * int((*m).block))
                (*m).bump += 1
                (*m).used += 1
                return p
            }
            next := (*m).next
            ListRemove(hs, ci, m)
            m = next
        }
        (s, i) := FindRun(hs, 1)
        if raw.IsNull(s) || !TakePages(s, i, 1) {
            return raw.Null[u8]()
        }
        n := raw.Of(&(*s).pages[i])
        (*n).class = Trunc[u16](ci + 1)
        (*n).block = Trunc[u32](Classes[ci])
        (*n).capacity = Trunc[u32](PageBytes / Classes[ci])
        (*n).bump = 1
        (*n).used = 1
        (*n).free = raw.Null[u8]()
        ListPush(hs, ci, n)
        return PageAddr(s, i)
    }
}

// LargeAlloc takes a span of whole pages for 8 KiB < n <= 1 MiB.
LargeAlloc(hs: *HeapState, n: int): *u8 {
    k := (n + PageBytes - 1) / PageBytes
    (s, i) := FindRun(hs, k)
    if raw.IsNull(s) || !TakePages(s, i, k) {
        return raw.Null[u8]()
    }
    unsafe "the span's pages, under the cache lock" {
        (*s).pages[i].class = SpanHead
        (*s).pages[i].span = Trunc[u16](k)
        for j in i + 1..i + k {
            (*s).pages[j].class = SpanTail
            (*s).pages[j].block = Trunc[u32](i)
        }
    }
    return PageAddr(s, i)
}

// HugeAlloc gives a request over 1 MiB, or aligned beyond 4 KiB, its own
// reservation. The request's pages are committed whole, so the commit
// limit fails here; the reservation spans HugeSpare times them (at most
// HugeSpareMax more, and only while HugeSpareBudget lasts), or just them
// when the address space refuses the larger one, so HeapResize can grow
// the block in place by committing more of it. A growing list then
// neither copies its elements nor touches fresh pages for them at each
// doubling, as it would with a new block.
HugeAlloc(size: int, align: int): *u8 {
    var lead := PageBytes
    if align > lead {
        lead = align
    }
    var res_align := SegBytes
    if align > res_align {
        res_align = align
    }
    if size > (1 << 46) {
        return raw.Null[u8]()
    }
    total := (lead + size + PageBytes - 1) / PageBytes * PageBytes
    // The spare part is total * 15 below HugeSpareMax, which the compare
    // keeps from wrapping, else HugeSpareMax; total is under 2^63 - 2^34
    // (lead is a power of two in int, size at most 2^46), so the sum does
    // not wrap either. Wrapping operators keep their checks out of every
    // program's heap.
    var extra := HugeSpareMax
    if total < HugeSpareMax / (HugeSpare - 1) {
        extra = total *% (HugeSpare - 1)
    }
    // Edge: the atomic debit tests the shared spare budget's old balance.
    // An insufficient balance refuses the reservation and restores the debit.
    hs := HS()
    unsafe "the RtState page" {
        if (*hs).spare.Add(0 -% extra) < extra {
            // Edge: refund the failed debit without losing other updates.
            _ = (*hs).spare.Add(extra)
            extra = 0
        }
    }
    var base := sys.Reserve(total +% extra, res_align)
    if raw.IsNull(base) && extra != 0 {
        unsafe "the RtState page" {
            // Edge: refund the spare debit before retrying without spare.
            _ = (*hs).spare.Add(extra)
        }
        extra = 0
        base = sys.Reserve(total, res_align)
    }
    if raw.IsNull(base) {
        return base
    }
    if !sys.Commit(base, total) {
        sys.Release(base, total +% extra)
        unsafe "the RtState page" {
            // Edge: return the spare charge after releasing the reservation.
            _ = (*hs).spare.Add(extra)
        }
        return raw.Null[u8]()
    }
    unsafe "base is the fresh reservation, its first total bytes committed" {
        p := raw.Offset(base, lead)
        HugeHeader(base, base, total +% extra, total - lead, extra)
        HugeHeader(raw.Offset(p, -PageBytes), base, total +% extra, total - lead, extra)
        return p
    }
}

// HugeSpare is how many times a huge block's first pages its reservation
// spans, HugeSpareMax the most address space one block reserves past them
// (16 GiB), and HugeSpareBudget the most all live huge blocks reserve past
// what they first commit (64 GiB, an eighth of a 39-bit AArch64 address
// space). A list that doubles from a block's first size grows in place four
// times before it needs a new block. Address space is not memory: the part
// not committed costs no commit charge and no page. It is virtual size,
// though: a request aligned over 4 KiB is huge too, so 100 bytes at an
// alignment of 8 KiB reserve 2 MiB. Where the address space is limited
// (Linux RLIMIT_AS, ulimit -v), HeapInit leaves the budget at 0 and a huge
// block reserves only its pages, as other reservations need that space. On
// Linux a growing huge block is two mappings (the committed pages and the
// rest), one more than before against vm.max_map_count.
const HugeSpare: int = 16
const HugeSpareMax: int = 1 << 34
const HugeSpareBudget: int = 1 << 36

// HugeGrow commits the pages of p's reservation that a size of new needs
// past its capacity, and says whether it could: false when new does not
// fit the reservation, or the OS refuses the commit (the commit limit), and
// then nothing changed. Both headers of the reservation (the one at base,
// and the one below p, which HeaderOf reads at an alignment of 4 MiB or
// more) get the new capacity. Only the block's owner resizes or frees it,
// so the headers need no lock.
HugeGrow(h: *SegHeader, p: *u8, new: int): bool {
    unsafe "h is the header of p's huge reservation; p is live" {
        // The offsets lie inside the reservation (bytes, under 2^63):
        // lead < bytes, new <= bytes - lead once past the compare, and the
        // page rounding stays under bytes, so the wrapping operators are
        // exact.
        base := (*h).base
        lead := Trunc[int](raw.Addr(p) -% base)
        if new > (*h).bytes -% lead {
            return false
        }
        have := lead +% (*h).cap
        want := (lead +% new +% (PageBytes - 1)) & -PageBytes
        if !sys.Commit(raw.FromAddr[u8](base +% Trunc[uint](have)), want -% have) {
            return false
        }
        cap := want -% lead
        (*raw.PtrCast[u8, SegHeader](raw.Offset(p, -PageBytes))).cap = cap
        (*raw.FromAddr[SegHeader](base)).cap = cap
        return true
    }
}

// HugeHeader writes a huge reservation's header at at: bytes is the whole
// reservation (what Release takes), cap the committed bytes from the block's
// start.
HugeHeader(at: *u8, base: *u8, total: int, cap: int, extra: int) {
    unsafe "at is committed memory of the reservation" {
        h := raw.PtrCast[u8, SegHeader](at)
        (*h).magic = SegMagic
        (*h).base = raw.Addr(base)
        (*h).kind = KindHuge
        (*h).bytes = total
        (*h).cap = cap
        (*h).free_pages = Trunc[u64](extra)
    }
}

// HeaderOf finds the header for a real block p: just below p when align is
// at least a segment (the huge block is then segment-aligned itself), else
// at p's 4 MiB-aligned base.
HeaderOf(p: *u8, align: int): *SegHeader {
    unsafe "p is a block this heap returned" {
        if align >= SegBytes {
            return raw.PtrCast[u8, SegHeader](raw.Offset(p, -PageBytes))
        }
        return raw.FromAddr[SegHeader](raw.Addr(p) & ~(Trunc[uint](SegBytes) - 1))
    }
}

/// HeapAlloc returns a block of at least size bytes aligned to align (a power
/// of two), or null when memory is exhausted; size <= 0 gives the dangling
/// pointer align when align < 4 MiB.
/// O(1) for a small block (8 KiB or less) from the carrier's magazine or the
/// shared cache; a large block (to 1 MiB) scans the segments' page maps for a
/// run of free pages, O(segments x pages); a huge block (over 1 MiB, or aligned
/// past 4 KiB) is its own reservation, a few system calls. A new segment is one
/// 4 MiB reservation.
export HeapAlloc(var size: int, align: int): *u8 {
    if size <= 0 {
        if align < SegBytes {
            unsafe "a dangling, aligned, never dereferenced pointer" {
                return raw.FromAddr[u8](Trunc[uint](align))
            }
        }
        size = 1
    }
    if align > 4096 || size > LargeMax {
        return HugeAlloc(size, align)
    }
    var n := size
    if align > n {
        n = align
    }
    hs := HS()
    unsafe "the cache, under its lock; m is this carrier's magazine" {
        if n <= SmallMax {
            // The fast path: ClassIndex is SmallClassOf's computation with
            // no loop at a power-of-two align of 16 or less
            // (RT-Heap-SmallClassOf), then the magazine's pop inline; an
            // empty magazine goes straight to MagAlloc's refill, so the
            // class and the magazine are found once. One call site of
            // MagOf and MagAlloc each keeps the code small (the release
            // hello world is at its 64 KiB budget).
            ci := ClassIndex(n, align)
            m := MagOf(hs, ci)
            if !raw.IsNull(m) {
                if (*m).n > 0 {
                    (*m).n -= 1
                    return (*m).items[int((*m).n)]
                }
                return MagAlloc(hs, m, ci)
            }
        }
        Lock(&(*hs).lock)
        p := if n <= SmallMax { SmallAlloc(hs, ClassIndex(n, align)) } else { LargeAlloc(hs, n) }
        Unlock(&(*hs).lock)
        return p
    }
}

/// HeapResize says whether the block p can hold new bytes in place (the
/// AllocTable contract): true when new fits its class, its span's pages or
/// its reservation's capacity, false otherwise. A huge block whose
/// capacity new passes grows in place when its reservation has room: the
/// pages it needs are committed, and true; false, with nothing changed,
/// when the reservation is too small or the OS refuses the commit. A
/// dangling p cannot grow.
/// O(1) for every kind: it compares with the block's metadata, and only a
/// huge block's growth makes a system call, one commit; it moves and
/// allocates nothing.
export HeapResize(p: *u8, old: int, new: int, align: int): bool {
    if !Real(p) {
        return new <= 0
    }
    h := HeaderOf(p, align)
    unsafe "h is the header of p's segment or reservation" {
        if (*h).kind == KindHuge {
            return new <= (*h).cap || HugeGrow(h, p, new)
        }
        i := Trunc[int]((raw.Addr(p) - (*h).base) >> 16)
        m := raw.Of(&(*h).pages[i])
        return if (*m).class == SpanHead { new <= int((*m).span) * PageBytes } else { new <= int((*m).block) }
    }
}

/// HeapUsable is the size of the block HeapAlloc returned at p for this
/// align (db.sqlite asks it): its class size for a small block, the
/// span's whole pages for a large one, the committed capacity for a huge
/// one; 0 for the dangling pointer of a zero-size request. It is the bound
/// HeapResize grows to in place without a commit, read from the same
/// metadata, so every size from the request up to it is a valid old size
/// for HeapResize and HeapFree; a huge block's growth past it raises it.
/// HeapFree reads only p and align, never size: a caller that frees with
/// any size, or with HeapUsable's, frees the same block (this is
/// what lets std.libc serve malloc_usable_size and free(p) with no header).
/// Like HeapFree, it needs p live and the align of its allocation.
/// O(1); allocates nothing.
export HeapUsable(p: *u8, align: int): int {
    if !Real(p) {
        return 0
    }
    h := HeaderOf(p, align)
    unsafe "h is the header of p's segment or reservation, p live" {
        if (*h).kind == KindHuge {
            return (*h).cap
        }
        i := Trunc[int]((raw.Addr(p) - (*h).base) >> 16)
        m := raw.Of(&(*h).pages[i])
        return if (*m).class == SpanHead { int((*m).span) * PageBytes } else { int((*m).block) }
    }
}

/// HeapFree returns the block p to the heap. A dangling p returns at once,
/// reading nothing; the path is found from the block's own metadata, never
/// from size, so a block resized in place to 0 is still freed.
/// O(1) for a small block, O(pages) for a large span; a huge block's
/// reservation is released, one system call.
export HeapFree(p: *u8, size: int, align: int) {
    if !Real(p) {
        return
    }
    h := HeaderOf(p, align)
    unsafe "h is the header of p's segment or reservation" {
        if (*h).kind == KindHuge {
            extra := Trunc[int]((*h).free_pages)
            sys.Release(raw.FromAddr[u8]((*h).base), (*h).bytes)
            // Edge: other allocators can claim this spare after the release.
            _ = (*HS()).spare.Add(extra)
            return
        }
        hs := HS()
        i := Trunc[int]((raw.Addr(p) - (*h).base) >> 16)
        m := raw.Of(&(*h).pages[i])
        // p is live and owned by the caller, so its page keeps its class
        // (a page is reassigned only once none of its blocks is in use),
        // and the class was written under the lock before p was handed
        // out: reading it here needs no lock.
        if (*m).class == SpanHead {
            Lock(&(*hs).lock)
            FreePages(h, i, int((*m).span))
            Unlock(&(*hs).lock)
            return
        }
        mg := MagOf(hs, int((*m).class) - 1)
        if !raw.IsNull(mg) {
            // The push inline; MagFree only to give half back.
            if (*mg).n < (*mg).cap {
                (*mg).items[int((*mg).n)] = p
                (*mg).n += 1
                return
            }
            MagFree(hs, mg, p)
            return
        }
        Lock(&(*hs).lock)
        FreeSmall(hs, p)
        Unlock(&(*hs).lock)
    }
}

// FreeSmall returns a small block to its page; the shared lock is held.
FreeSmall(hs: *HeapState, p: *u8) {
    h := HeaderOf(p, 16)
    unsafe "p is a small block of h's segment; the lock is held" {
        i := Trunc[int]((raw.Addr(p) - (*h).base) >> 16)
        m := raw.Of(&(*h).pages[i])
        ci := int((*m).class) - 1
        raw.Write(raw.PtrCast[u8, *u8](p), (*m).free)
        (*m).free = p
        (*m).used -= 1
        if (*m).in_list == 0 {
            ListPush(hs, ci, m)
        }
        // An empty page goes back to its segment unless it is the only page
        // on its class's list, which stays so the next allocation of the
        // class needs no new page.
        if (*m).used == 0 && !(raw.Same((*hs).lists[ci], m) && raw.IsNull((*m).next)) {
            ListRemove(hs, ci, m)
            FreePages(h, i, 1)
        }
    }
}

/// ProcessHeap is the one Heap, whose table is HeapTable (or CountingTable
/// in a test build after TrackLeaks).
/// O(1); allocates nothing.
export ProcessHeap(): &Heap {
    unsafe "the RtState page lives for the whole process" {
        return &(*R()).heap
    }
}

/// HeapSegments counts the segments of the shared cache, for the tests that
/// check that the heap does not grow across rounds (RT-HeapRemoteFree).
/// O(s) for the s segments, under the cache lock; allocates nothing.
export HeapSegments(): int {
    hs := HS()
    var n := 0
    unsafe "the shared cache, under its lock" {
        Lock(&(*hs).lock)
        var s := (*hs).segs
        for !raw.IsNull(s) {
            n += 1
            s = (*s).next
        }
        Unlock(&(*hs).lock)
    }
    return n
}
