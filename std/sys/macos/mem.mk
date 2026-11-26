// Why: the allocator and the task stacks need address space reserved,
// committed, discarded and guarded, and std's runtime asks for it in
// 4 KiB units while arm64 macOS pages are 16 KiB.
//
// What: Reserve, Commit, CommitStack, Decommit, Discard, Release, GuardPage,
// GuardPages, GuardStack and AddressLimited.
//
// How: mmap, mprotect and munmap, on whole OS pages (PageSize). A request
// that does not cover whole pages is rounded so that no live byte outside
// it is lost and no guard is lifted: Commit makes the covering pages
// read-write by protection alone (a fresh reservation reads zero, and
// Decommit leaves whole pages zero), CommitStack rounds its start up, so
// the guard page below a stack stays, and Decommit and Discard act on the
// whole pages inside the range only. Discard maps fresh read-write pages,
// since Darwin's MADV_DONTNEED does not promise zeros on the next touch.
// There is no lightweight guard (GuardStack is false), so a slot's guard is
// a PROT_NONE page.

import std.raw

const ProtNone: i32 = 0
const ProtRW: i32 = 3
const MapPrivate: i32 = 0x02
const MapFixed: i32 = 0x10
const MapNoReserve: i32 = 0x40
const MapAnon: i32 = 0x1000

// MapFailed is mmap's MAP_FAILED, (void *)-1.
const MapFailed: uint = 0xFFFF_FFFF_FFFF_FFFF

// MaxReserve bounds size + align in Reserve, as on Linux.
const MaxReserve: int = 1 << 62

// Page is the OS page, as a uint, for the rounding below.
Page(): uint {
    return Trunc[uint](PageSize())
}

PageDown(a: uint): uint {
    return a & ~(Page() - 1)
}

PageUp(a: uint): uint {
    return (a + Page() - 1) & ~(Page() - 1)
}

// Map is anonymous mmap; null on failure.
Map(p: *u8, size: int, prot: i32, flags: i32): *u8 {
    unsafe "p is a hint, or under MapFixed a range the caller owns that holds nothing live" {
        r := mmap(p, Trunc[uint](size), prot, flags | MapPrivate | MapAnon, -1, 0)
        return if raw.Addr(r) == MapFailed { raw.Null[u8]() } else { r }
    }
}

Unmap(p: *u8, size: int) {
    if size > 0 {
        unsafe "[p, p + size) is a mapping the caller owns and no longer uses" {
            _ = munmap(p, Trunc[uint](size))
        }
    }
}

// Protect sets the protection of the pages [lo, hi), both page-aligned.
Protect(lo: uint, hi: uint, prot: i32): bool {
    if hi <= lo {
        return true
    }
    unsafe "[lo, hi) lies in a reservation the caller owns" {
        return mprotect(raw.FromAddr[u8](lo), hi - lo, prot) == 0
    }
}

/// Reserve maps size + align bytes with no access and unmaps the misaligned
/// head and the tail, so the result is aligned to align. size is rounded up
/// to whole pages and align to at least a page; align must be a power of
/// two. null for a size or align that is not positive, an align that is
/// not a power of two, and a size so large that size + align would
/// overflow, as for any reservation the kernel refuses.
/// O(1) system calls; reserves address space and commits nothing.
export Reserve(size: int, align: int): *u8 {
    if size <= 0 || align <= 0 || (align & (align - 1)) != 0 {
        return raw.Null[u8]()
    }
    ps := PageSize()
    var al := align
    if al < ps {
        al = ps
    }
    if size > MaxReserve - al {
        return raw.Null[u8]()
    }
    sz := (size + ps - 1) / ps * ps
    p := Map(raw.Null[u8](), sz + al, ProtNone, MapNoReserve)
    if raw.IsNull(p) {
        return p
    }
    a := raw.Addr(p)
    aligned := (a + Trunc[uint](al) - 1) & ~(Trunc[uint](al) - 1)
    head := Trunc[int](aligned - a)
    unsafe "the head and tail are parts of the mapping just made" {
        Unmap(p, head)
        Unmap(raw.FromAddr[u8](aligned + Trunc[uint](sz)), al - head)
        return raw.FromAddr[u8](aligned)
    }
}

/// Commit makes [p, p + size) read-write: the pages that cover it, by
/// protection, so the contents of a page shared with a neighbour stay. A
/// page never written, or left by Decommit, reads zero.
/// O(1): one system call; commits size bytes, rounded to pages.
export Commit(p: *u8, size: int): bool {
    if size <= 0 {
        return true
    }
    a := raw.Addr(p)
    return Protect(PageDown(a), PageUp(a + Trunc[uint](size)), ProtRW)
}

/// CommitStack makes a stack's range read-write, from the first page
/// boundary at or above p (the guard page below p stays) to the page above
/// p + size.
/// O(1): one system call; commits size bytes, rounded to pages.
export CommitStack(p: *u8, size: int): bool {
    if size <= 0 {
        return true
    }
    a := raw.Addr(p)
    return Protect(PageUp(a), PageUp(a + Trunc[uint](size)), ProtRW)
}

/// Decommit drops the contents of the whole pages inside [p, p + size) and
/// makes them inaccessible, keeping the address space: fresh PROT_NONE
/// pages, which read zero once committed again.
/// O(1): one system call; gives back size bytes, rounded to pages.
export Decommit(p: *u8, size: int) {
    a := raw.Addr(p)
    lo := PageUp(a)
    hi := PageDown(a + Trunc[uint](size))
    if hi > lo {
        unsafe "[lo, hi) lies in a reservation the caller owns, and its contents are not needed" {
            _ = Map(raw.FromAddr[u8](lo), Trunc[int](hi - lo), ProtNone, MapFixed | MapNoReserve)
        }
    }
}

/// Discard drops the contents of the whole read-write pages inside
/// [p, p + size) and keeps them read-write: the next touch reads zero
/// (fresh pages over the range). A failure leaves the pages as they were.
/// O(1) system calls; gives back the contents of size bytes, rounded to
/// pages.
export Discard(p: *u8, size: int) {
    a := raw.Addr(p)
    lo := PageUp(a)
    hi := PageDown(a + Trunc[uint](size))
    if hi > lo {
        unsafe "[lo, hi) lies in a reservation the caller owns, and its contents are not needed" {
            _ = Map(raw.FromAddr[u8](lo), Trunc[int](hi - lo), ProtRW, MapFixed)
        }
    }
}

/// AddressLimited has std/sys/linux's contract; always false: macOS does
/// not enforce RLIMIT_AS.
/// O(1); allocates nothing.
export AddressLimited(): bool {
    return false
}

/// Release frees the whole reservation that Reserve returned at p.
/// O(1): one system call; gives back the reservation.
export Release(p: *u8, size: int) {
    a := raw.Addr(p)
    Unmap(p, Trunc[int](PageUp(a + Trunc[uint](size)) - a))
}

/// GuardPage makes the OS page at p inaccessible; p must be page-aligned
/// (false otherwise: rounding would cover bytes the caller did not give).
/// O(1): one system call; allocates nothing.
export GuardPage(p: *u8): bool {
    a := raw.Addr(p)
    if PageDown(a) != a {
        return false
    }
    return Protect(a, a + Page(), ProtNone)
}

/// GuardPages makes [p, p + size) inaccessible; p and size must be
/// page-aligned (false otherwise: rounding would cover bytes the caller did not give).
/// O(1): one system call; allocates nothing.
export GuardPages(p: *u8, size: int): bool {
    if size <= 0 {
        return false
    }
    a := raw.Addr(p)
    s := Trunc[uint](size)
    if PageDown(a) != a || PageDown(s) != s {
        return false
    }
    return Protect(a, a + s, ProtNone)
}

/// GuardStack is false: macOS has no lightweight guard, and the caller
/// falls back to GuardPage.
/// O(1); allocates nothing.
export GuardStack(p: *u8): bool {
    return false
}
