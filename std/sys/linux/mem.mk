// Why: the allocator and the task stacks need address space reserved,
// committed, discarded and guarded page by page, with Linux's overcommit
// rules in mind.
//
// What: Reserve, Commit, CommitStack, Decommit, Discard, Release, GuardPage,
// GuardPages, GuardStack and AddressLimited.
//
// How: mmap, mprotect, munmap and madvise. Reserve and Decommit use
// MAP_NORESERVE; Commit does not, so it is charged against the commit limit,
// while CommitStack only changes protection over the NORESERVE slot region.
// GuardStack uses MADV_GUARD_INSTALL (Linux 6.13) so a guard page splits no
// VMA, and the caller falls back to GuardPage (PROT_NONE) when it is refused.

import std.raw

const ProtNone: uint = 0
const ProtRW: uint = 3
const MapPrivate: uint = 0x02
const MapFixed: uint = 0x10
const MapAnonymous: uint = 0x20
const MapNoReserve: uint = 0x4000

// Map is mmap with no file; null on failure.
Map(p: *u8, size: int, prot: uint, flags: uint): *u8 {
    unsafe "system call; p is a hint, or under MapFixed a range the caller reserved that holds nothing live" {
        r := Syscall(Nrs().mmap, raw.Addr(p), Trunc[uint](size), prot, flags, 0xFFFF_FFFF_FFFF_FFFF, 0)
        return if IsErr(r) { raw.Null[u8]() } else { raw.FromAddr[u8](Trunc[uint](r)) }
    }
}

Unmap(p: *u8, size: int) {
    if size > 0 {
        unsafe "system call; [p, p + size) is a mapping the caller owns and no longer uses" {
            _ = Syscall(Nrs().munmap, raw.Addr(p), Trunc[uint](size), 0, 0, 0, 0)
        }
    }
}

/// Reserve maps size + align bytes with no access and unmaps the misaligned
/// head and the tail, so the result is aligned to align. size is rounded up
/// to whole pages and align to at least a page (munmap takes whole pages, so
/// a smaller align would unmap the page holding the result, and an odd size
/// would leave the tail mapped); align must be a power of two. null for a size or
/// align that is not positive, an align that is not a power of two, and a
/// size so large that size + align would overflow (ReserveLimits), as for
/// any reservation the kernel refuses.
/// O(1) system calls (Windows retries when another thread takes the aligned
/// range); reserves address space and commits nothing.
export Reserve(size: int, align: int): *u8 {
    if size <= 0 || align <= 0 || (align & (align - 1)) != 0 {
        return raw.Null[u8]()
    }
    var al := align
    if al < 4096 {
        al = 4096
    }
    if size > MaxReserve - al {
        return raw.Null[u8]()
    }
    sz := (size + 4095) / 4096 * 4096
    p := Map(raw.Null[u8](), sz + al, ProtNone, MapPrivate | MapAnonymous | MapNoReserve)
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

// MaxReserve bounds size + align in Reserve (2^62: above every user address
// space, so the kernel refuses anything near it anyway, and far from int's
// largest value, so the page rounding cannot overflow).
const MaxReserve: int = 1 << 62

/// Commit maps fresh read-write pages over [p, p+size), charged. A refused
/// commit (the commit limit) maps the range back as reserved; only if that
/// map is refused too does the range stay unmapped, a hole in the
/// caller's reservation.
/// O(1): one system call, two when refused; commits size bytes.
export Commit(p: *u8, size: int): bool {
    q := Map(p, size, ProtRW, MapPrivate | MapAnonymous | MapFixed)
    if !raw.IsNull(q) {
        return true
    }
    // Some kernels (v6.6's mmap_region, for one) unmap the old range before
    // the overcommit check, so a refused mmap can leave a hole in the
    // caller's reservation, where an unrelated mapping could land and a
    // later Release would unmap it. The range is mapped back with no access
    // and no charge. A window remains: a mapping another thread makes
    // between the two calls may land in the hole, and this map then
    // replaces it. Closing it needs the kernel to check before it unmaps.
    _ = Map(p, size, ProtNone, MapPrivate | MapAnonymous | MapFixed | MapNoReserve)
    return false
}

// RlimitAs is RLIMIT_AS (include/uapi/asm-generic/resource.h).
const RlimitAs: uint = 9

/// AddressLimited reports whether the process's address space is limited
/// (a finite soft RLIMIT_AS, as `ulimit -v` sets), so reservations past
/// what is committed take from a budget other mappings need. False when
/// the limit cannot be read.
/// O(1): one system call; allocates nothing.
export AddressLimited(): bool {
    var lim: [2]u64 = [0; 2]
    unsafe "system call; lim is a struct rlimit64 { cur, max }" {
        if Syscall(Nrs().prlimit64, 0, RlimitAs, 0, raw.Addr(raw.DataMut(mut lim[..])), 0, 0) != 0 {
            return false
        }
    }
    return lim[0] != 0xFFFF_FFFF_FFFF_FFFF
}

/// CommitStack makes a task slot's range read-write: the kernel supplies
/// pages on touch.
/// O(1): one system call; commits size bytes.
export CommitStack(p: *u8, size: int): bool {
    unsafe "system call; [p, p + size) lies in a reservation the caller owns" {
        return !IsErr(Syscall(Nrs().mprotect, raw.Addr(p), Trunc[uint](size), ProtRW, 0, 0, 0))
    }
}

/// Decommit drops the contents and the charge, keeping the address space.
/// O(1): one system call; gives back the commit of size bytes.
export Decommit(p: *u8, size: int) {
    _ = Map(p, size, ProtNone, MapPrivate | MapAnonymous | MapFixed | MapNoReserve)
}

// MADV_DONTNEED (include/uapi/asm-generic/mman-common.h).
const MadvDontNeed: uint = 4

/// Discard drops the contents and the charge of the read-write pages
/// [p, p + size) of a private anonymous mapping and keeps the mapping and
/// its protection: the next touch reads a zero page (madvise(MADV_DONTNEED)).
/// One call that splits no VMA, so unlike
/// Decommit (a map-again then CommitStack) it cannot fail near
/// vm.max_map_count. Guard markers of GuardStack in the range stay
/// (zapping keeps them, Linux 6.13's MADV_GUARD_INSTALL). The kernel does
/// not return EINTR for this advice today; it would be repeated, as
/// GuardStack does, since a repeat drops nothing more. The result is
/// otherwise ignored: on a range the caller owns the only refusals are
/// EINVAL for a locked or hugetlb mapping, which std never makes, and then
/// the pages simply stay.
/// O(1) system calls; gives back the contents (and on Windows the commit) of
/// size bytes.
export Discard(p: *u8, size: int) {
    unsafe "system call; [p, p + size) lies in a reservation the caller owns, and its contents are not needed" {
        for {
            r := Syscall(Nrs().madvise, raw.Addr(p), Trunc[uint](size), MadvDontNeed, 0, 0, 0)
            if r != -4 {
                return
            }
        }
    }
}

/// Release frees the whole reservation that Reserve returned at p.
/// O(1): one system call; gives back the reservation.
export Release(p: *u8, size: int) {
    Unmap(p, size)
}

/// GuardPage makes one page inaccessible.
/// O(1): one system call; allocates nothing.
export GuardPage(p: *u8): bool {
    unsafe "system call; p is a page of a reservation the caller owns, holding nothing live" {
        return !IsErr(Syscall(Nrs().mprotect, raw.Addr(p), 4096, ProtNone, 0, 0, 0))
    }
}

/// GuardPages makes [p, p + size) inaccessible.
/// O(1): one system call; allocates nothing.
export GuardPages(p: *u8, size: int): bool {
    unsafe "system call; [p, p + size) lies in a reservation the caller owns, holding nothing live" {
        return !IsErr(Syscall(Nrs().mprotect, raw.Addr(p), Trunc[uint](size), ProtNone, 0, 0, 0))
    }
}

// MADV_GUARD_INSTALL (include/uapi/asm-generic/mman-common.h, Linux 6.13).
const MadvGuardInstall: uint = 102

/// GuardStack makes the page at p, inside a read-write task slot, a
/// lightweight guard: madvise(MADV_GUARD_INSTALL) marks it in
/// the page tables, so a touch faults (SIGSEGV at the page's address, as a
/// PROT_NONE page does) and the slot's mapping is not split: no new VMA.
/// false when the kernel refuses: EINVAL before Linux 6.13 (the advice is
/// unknown) or on a mapping the advice does not apply to (locked, hugetlb,
/// a PFN map); the caller then falls back to GuardPage. EINTR (the kernel
/// gives up its internal zap-and-retry loop when a signal is pending, which
/// ThreadInterrupt's SIGURG can be) is repeated: it leaves no partial guard,
/// and the call installs one page.
/// O(1) system calls; allocates nothing.
export GuardStack(p: *u8): bool {
    unsafe "system call; p is the unused lowest page of a stack slot the caller owns" {
        for {
            r := Syscall(Nrs().madvise, raw.Addr(p), 4096, MadvGuardInstall, 0, 0, 0)
            if r != -4 {
                return !IsErr(r)
            }
        }
    }
}
