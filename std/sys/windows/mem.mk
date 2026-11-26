// Why: the heap and the task stacks need address space they can reserve
// first and back with memory later, page by page.
//
// What: Windows page calls: Reserve, Commit, CommitStack, Decommit,
// Discard, Release, GuardPage, GuardPages, GuardStack and AddressLimited.
//
// How: VirtualAlloc and VirtualFree. Reserving commits nothing; Commit is
// charged against the commit limit. A task slot's guard is a PAGE_GUARD
// page.

import std.raw

const MemCommit: u32 = 0x1000
const MemReserve: u32 = 0x2000
const MemDecommit: u32 = 0x4000
const MemRelease: u32 = 0x8000
const PageNoAccess: u32 = 0x01
const PageReadWrite: u32 = 0x04
const PageGuard: u32 = 0x100

/// Reserve returns size bytes of address space aligned to align, no access,
/// or null. Windows cannot release part of a reservation, so it reserves
/// size + align to find an aligned address, releases it, and reserves exactly
/// there; another thread may take the range in between, so it retries.
/// align must be a power of two (null otherwise, as on Linux: the aligned
/// address of another align would be wrong); a size the kernel cannot
/// reserve is null (size + align is computed in uint and cannot wrap: both
/// are below 2^63).
/// O(1) system calls (Windows retries when another thread takes the aligned
/// range); reserves address space and commits nothing.
export Reserve(size: int, align: int): *u8 {
    if size <= 0 || align <= 0 || (align & (align - 1)) != 0 {
        return raw.Null[u8]()
    }
    unsafe "kernel32; the probe reservation is released before reuse" {
        if align <= 65536 {
            return VirtualAlloc(raw.Null[u8](), Trunc[uint](size), MemReserve, PageNoAccess)
        }
        for _ in 0..8 {
            probe := VirtualAlloc(raw.Null[u8](), Trunc[uint](size) + Trunc[uint](align), MemReserve, PageNoAccess)
            if raw.IsNull(probe) {
                return probe
            }
            a := raw.Addr(probe)
            aligned := (a + Trunc[uint](align) - 1) & ~(Trunc[uint](align) - 1)
            _ = VirtualFree(probe, 0, MemRelease)
            p := VirtualAlloc(raw.FromAddr[u8](aligned), Trunc[uint](size), MemReserve, PageNoAccess)
            if !raw.IsNull(p) {
                return p
            }
        }
        return raw.Null[u8]()
    }
}

/// Commit makes [p, p+size) read-write; the pages read as zero until written.
/// O(1): one system call; commits size bytes.
export Commit(p: *u8, size: int): bool {
    unsafe "kernel32; [p, p + size) lies in a reservation the caller owns" {
        return !raw.IsNull(VirtualAlloc(p, Trunc[uint](size), MemCommit, PageReadWrite))
    }
}

/// CommitStack is Commit on Windows: a task slot's top pages.
/// O(1): one system call; commits size bytes.
export CommitStack(p: *u8, size: int): bool {
    return Commit(p, size)
}

/// Decommit drops the contents and the charge, keeping the address space.
/// O(1): one system call; gives back the commit of size bytes.
export Decommit(p: *u8, size: int) {
    unsafe "kernel32; [p, p + size) lies in a reservation the caller owns, and its contents are not needed" {
        _ = VirtualFree(p, Trunc[uint](size), MemDecommit)
    }
}

/// Discard drops the contents and the commit charge of [p, p + size): on
/// Windows that is Decommit (MEM_DECOMMIT), since no call drops a committed
/// page and keeps it readable as zero; the pages fault until committed
/// again (Commit, or a PAGE_GUARD page's growth path), which is why std/rt's
/// Windows trim re-arms the slot's guard page (TrimOne). Linux's Discard
/// keeps the pages read-write (MADV_DONTNEED).
/// O(1) system calls; gives back the contents (and on Windows the commit) of
/// size bytes.
export Discard(p: *u8, size: int) {
    Decommit(p, size)
}

/// AddressLimited has std/sys/linux's contract; always false: Windows
/// limits no process's address space below its size (a job object limits
/// committed memory, which Commit already meets).
/// O(1); allocates nothing.
export AddressLimited(): bool {
    return false
}

/// Release frees the whole reservation that Reserve returned at p.
/// O(1): one system call; gives back the reservation.
export Release(p: *u8, size: int) {
    unsafe "kernel32; p is a reservation the caller owns and no longer uses" {
        _ = VirtualFree(p, 0, MemRelease)
    }
}

/// GuardPage commits one page as PAGE_GUARD: the kernel's stack growth path,
/// or STATUS_GUARD_PAGE_VIOLATION once. False when Windows refuses.
/// O(1): one system call; commits the page.
export GuardPage(p: *u8): bool {
    return GuardPages(p, 4096)
}

/// GuardPages commits [p, p + size) as PAGE_GUARD: the kernel's stack
/// growth path and exception-frame room below StackLimit. False when
/// Windows refuses. O(1): one system call; commits size bytes.
export GuardPages(p: *u8, size: int): bool {
    unsafe "kernel32; [p, p + size) lies in a reservation the caller owns, holding nothing live" {
        return !raw.IsNull(VirtualAlloc(p, Trunc[uint](size), MemCommit, PageReadWrite | PageGuard))
    }
}

/// GuardStack is always false on Windows and touches nothing: Windows has
/// no lightweight guard (Linux's MADV_GUARD_INSTALL), and a task slot's
/// guard is GuardPage's PAGE_GUARD page.
/// O(1); allocates nothing.
export GuardStack(p: *u8): bool {
    return false
}
