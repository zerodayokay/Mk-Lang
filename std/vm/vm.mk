// Why: a JIT, a sandbox's cage or an arena of its own needs address space
// the program manages itself: reserved, committed and protected by page.
//
// What: Reserve, and Region with Base, Size, Commit, Decommit, Protect and
// Release; Prot; CodeRegion (New, Reserve, Size, Entry, Commit, Edit,
// EditRange, Release) and CodeWriter (Bytes, Flush, Done); PageSize; and the
// Error set.
//
// How: every function and method is callable only in `unsafe` (E1101),
// as fs.Map is: a Region hands out raw
// addresses whose pages may be inaccessible, and a CodeRegion runs what it
// is given. W^X on every page: Prot has no executable value, and a
// CodeRegion is always sealed outside an active linear CodeWriter from Edit.
// On Windows and Linux its writable and executable views are two mappings of
// one memory (the writable view read-only outside Edit, the executable view
// read-execute throughout); on arm64 macOS it is one MAP_JIT mapping whose
// write-vs-execute mode switches per thread via pthread_jit_write_protect_np
// when the host supports it, else a private anonymous mapping toggled between
// read-write and read-execute via mprotect (as on x86_64 macOS). Built on
// std.sys's pages (Reserve, Commit, Decommit, Release, PageSize, VmProtect,
// VmCodeNew, VmCodeCommit, VmCodePerPage, VmCodeBeginWrite, VmCodeEndWrite,
// VmCodeProtect, VmCodeFree, VmFlush). There is no large-page support.

import std.raw
import std.sys

/// An offset, length or size is not a whole number of pages, or is
/// outside the region.
export error Misaligned
/// This target cannot do it, or 64 code regions are live.
export error Unsupported
/// The OS refused, with its code (GetLastError on Windows, errno on Linux
/// and macOS).
export error Os(code: i64)

/// Every way a std.vm operation fails.
export type Error = OutOfMemory | Misaligned | Unsupported | Os

/// The OS's page size in bytes. Every offset and length a Region takes is
/// a multiple of it. O(1); allocates nothing.
export PageSize(): int {
    return sys.PageSize()
}

/// Reserved address space: size bytes from Base, with no access until
/// Commit. It is linear: end it with Release, which gives back the whole
/// range.
export linear struct Region {
    at: uint
    n: int
    // bits: one bit per page, set while the page is committed, so Protect
    // refuses an uncommitted page on every target and Commit
    // skips committed ones. Its own reservation of bitn bytes, a whole
    // number of pages: 1 MiB for a 32 GiB region. Committed whole at
    // Reserve: on Windows charged against the commit limit; on Linux an
    // mprotect of a MAP_NORESERVE mapping, so no charge, and pages appear
    // as they are touched.
    bits: uint
    bitn: int
}

// Pages is whether n is a non-negative multiple of the page size.
Pages(n: int): bool {
    return n >= 0 && n % sys.PageSize() == 0
}

/// Reserves size bytes of address space, a whole number of pages, at an
/// address that is a multiple of align. align is a power of two; an align
/// below a page acts as a page. No page is committed: on Linux the
/// reservation is MAP_NORESERVE, so a large reservation (32 GiB) costs no
/// commit charge.
///
/// The Region tracks committed pages in a bitmap, a second reservation of
/// one bit per page rounded up to a page (one page for up to 128 MiB of
/// 4 KiB pages, 1 MiB for 32 GiB), committed at once: charged on Windows,
/// not on Linux.
///
/// Fails with Misaligned for a size or align of the wrong form, and
/// OutOfMemory when the OS has no such range. O(1) system calls (three);
/// allocates nothing on the heap.
export Reserve(size: int, align: int): Error!Region {
    if size <= 0 || !Pages(size) || align <= 0 || (align & (align - 1)) != 0 {
        fail Misaligned
    }
    var al := align
    if al < sys.PageSize() {
        al = sys.PageSize()
    }
    p := sys.Reserve(size, al)
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    bitn := BitBytes(size)
    b := NewBits(bitn)
    if raw.IsNull(b) {
        sys.Release(p, size)
        fail OutOfMemory
    }
    return Region(at: raw.Addr(p), n: size, bits: raw.Addr(b), bitn: bitn)
}

// BitBytes is the size of the commit bitmap of a region of size bytes: one
// bit per page, rounded up to whole pages.
BitBytes(size: int): int {
    ps := sys.PageSize()
    words := (size / ps + 63) / 64
    return (words * 8 + ps - 1) / ps * ps
}

// NewBits reserves and commits a commit bitmap of bitn bytes (BitBytes),
// every bit clear; null when the OS refuses, with nothing left behind.
NewBits(bitn: int): *u8 {
    b := sys.Reserve(bitn, sys.PageSize())
    if raw.IsNull(b) {
        return b
    }
    if !sys.CommitStack(b, bitn) {
        sys.Release(b, bitn)
        return raw.Null[u8]()
    }
    return b
}

// BitWord is word w of the commit bitmap at bits (pages 64 * w to
// 64 * w + 63), a Region's or a reserved CodeRegion's, as an atomic.
BitWord(bits: uint, w: int): &raw.Atomic[u64] {
    unsafe "word w lies in the bitmap at bits, committed whole when its region was made and live until the region is released; every access to it is atomic" {
        return &*raw.FromAddr[raw.Atomic[u64]](bits + Trunc[uint](w * 8))
    }
}

// RangeMask is the bits of word w that pages [first, end) cover (end >
// first, and the range meets the word).
RangeMask(w: int, first: int, end: int): u64 {
    var lo := first - w * 64
    if lo < 0 {
        lo = 0
    }
    var hi := end - w * 64
    if hi > 64 {
        hi = 64
    }
    top: u64 = if hi == 64 { 0xFFFF_FFFF_FFFF_FFFF } else { (1 << Trunc[u64](hi)) - 1 }
    return top & ~((1 << Trunc[u64](lo)) - 1)
}

// MarkPages sets (on) or clears the commit bits of pages [first, first + k),
// a word at a time: one atomic read-modify-write per 64 pages, so a large
// range costs few atomic operations.
MarkPages(bits: uint, first: int, k: int, on: bool) {
    end := first + k
    var w := first / 64
    for w * 64 < end {
        m := RangeMask(w, first, end)
        // Edge: relaxed; the bitmap is only a record of which pages this
        // Region's own calls committed, read by its later calls; a caller
        // that runs them on several threads at once orders them itself.
        if on {
            _ = BitWord(bits, w).OrOrd(m, 0)
        } else {
            _ = BitWord(bits, w).AndOrd(~m, 0)
        }
        w += 1
    }
}

// AllCommitted is whether every page of [first, first + k) is committed, a
// word at a time.
AllCommitted(bits: uint, first: int, k: int): bool {
    return NextPage(bits, first, first + k, false) == first + k
}

// NextPage is the first page of [from, end) whose commit bit is set (on)
// or clear (not on), or end when there is none: one load per 64 pages.
NextPage(bits: uint, from: int, end: int, on: bool): int {
    var p := from
    for p < end {
        w := p / 64
        // Edge: relaxed, as MarkPages.
        var word := BitWord(bits, w).LoadOrd(0)
        if !on {
            word = ~word
        }
        word = word & RangeMask(w, p, end)
        if word != 0 {
            var i := 0
            for word & 1 == 0 {
                word = word >> 1
                i += 1
            }
            return w * 64 + i
        }
        p = (w + 1) * 64
    }
    return end
}

// BadRange panics with the bounds code (2): a range that is not whole pages
// inside the region is a programming error, as a slice bound is.
BadRange(op: str): never {
    raw.Fail(2, op)
}

// Span is [offset, offset + len) of r checked against its pages: the base
// pointer, or null when it is not page-aligned or not inside r.
Span(r: &Region, offset: int, len: int): *u8 {
    if !Pages(offset) || !Pages(len) || len == 0 || offset > r.n - len {
        return raw.Null[u8]()
    }
    unsafe "an address inside the region r reserved, checked above; only made, not read" {
        return raw.FromAddr[u8](r.at + Trunc[uint](offset))
    }
}

/// The first address of the region, which a program reads and writes
/// through once it has committed pages. O(1); allocates nothing.
export Region.Base(&self): uint {
    return self.at
}

/// The region's size in bytes, a whole number of pages. O(1); allocates
/// nothing.
export Region.Size(&self): int {
    return self.n
}

/// Commits [offset, offset + len). Its uncommitted pages become read-write
/// and read as zero, charged against the OS's commit limit. Pages already
/// committed keep their contents and protection, on every target.
/// Concurrent calls on overlapping ranges are the caller's to exclude.
///
/// Fails with OutOfMemory when the OS refuses the charge, and the runs of
/// pages committed before it stay committed; with Misaligned for a range
/// that is not whole pages inside the region. O(len / (64 pages)) bitmap
/// words, plus one system call per run of uncommitted pages; allocates
/// nothing.
export Region.Commit(&self, offset: int, len: int): Error!void {
    p := Span(self, offset, len)
    if raw.IsNull(p) {
        fail Misaligned
    }
    ps := sys.PageSize()
    end := (offset + len) / ps
    var at := NextPage(self.bits, offset / ps, end, false)
    for at < end {
        stop := NextPage(self.bits, at, end, true)
        unsafe "pages [at, stop) lie inside the region, checked by Span above" {
            if !sys.Commit(raw.FromAddr[u8](self.at + Trunc[uint](at * ps)), (stop - at) * ps) {
                fail OutOfMemory
            }
        }
        MarkPages(self.bits, at, stop - at, true)
        at = NextPage(self.bits, stop, end, false)
    }
}

/// Drops the contents and the commit charge of [offset, offset + len). The
/// address space stays reserved, with no access. A range that is not whole
/// pages inside the region panics with the bounds code (2). One system
/// call, plus one bitmap read-modify-write per 64 pages; allocates
/// nothing. O(len / (64 pages)).
export Region.Decommit(&self, offset: int, len: int) {
    p := Span(self, offset, len)
    if raw.IsNull(p) {
        BadRange("Region.Decommit: not whole pages inside the region")
    }
    sys.Decommit(p, len)
    ps := sys.PageSize()
    MarkPages(self.bits, offset / ps, len / ps, false)
}

/// Sets the protection of the committed pages [offset, offset + len) to
/// None (any access faults), Read or ReadWrite; never executable.
///
/// Fails with Os(487) when a page in the range is not committed, on every
/// target: 487 is Windows' ERROR_INVALID_ADDRESS, and std gives the same
/// code on Linux. Fails with Misaligned for a range that is not whole
/// pages inside the region. One bitmap load per 64 pages and one system
/// call; allocates nothing. O(len / (64 pages)).
export Region.Protect(&self, offset: int, len: int, p: Prot): Error!void {
    at := Span(self, offset, len)
    if raw.IsNull(at) {
        fail Misaligned
    }
    ps := sys.PageSize()
    if !AllCommitted(self.bits, offset / ps, len / ps) {
        fail Os(code: 487)
    }
    code: u32 = match p {
        .None { 0 }
        .Read { 1 }
        .ReadWrite { 2 }
    }
    e := sys.VmProtect(at, len, code)
    if e != 0 {
        fail Os(code: e)
    }
}

/// Gives back the whole reservation, committed pages included, and the
/// commit bitmap's. O(1): two system calls; allocates nothing.
export Region.Release(self) {
    Region(at, n, bits, bitn) := self
    unsafe "at and bits are the reservations Reserve made, of n and bitn bytes, released once: Release takes the linear region" {
        sys.Release(raw.FromAddr[u8](at), n)
        sys.Release(raw.FromAddr[u8](bits), bitn)
    }
}

/// A page protection for Region.Protect. There is no executable value: no
/// page is both writable and executable (W^X), and code runs only from a
/// CodeRegion's executable view.
export enum Prot {
    /// No access.
    None
    /// Read only.
    Read
    /// Read and write.
    ReadWrite
}

/// Memory for generated code, enforcing W^X on every target: no page is
/// writable and executable at once, and the region stays sealed (not
/// writable) outside an active CodeWriter from Edit. To emit or patch code,
/// call Edit to open a linear CodeWriter, write through CodeWriter.Bytes,
/// flush the instruction cache with CodeWriter.Flush, close the window with
/// CodeWriter.Done, and run at Entry.
///
/// On Windows it is a pagefile-backed section mapped twice (a writable view
/// kept read-only outside Edit, and a read-execute view untouched
/// throughout); on Linux a memfd mapped twice the same way; on arm64 macOS a
/// MAP_JIT mapping whose write-vs-execute mode switches per thread through
/// pthread_jit_write_protect_np when the host supports it, falling back to a
/// private anonymous mapping toggled between read-write during Edit and
/// read-execute outside it via mprotect (as on x86_64 macOS) when it does not.
///
/// A region from New is committed whole; one from Reserve has no page
/// committed until Commit takes it. Linear: end it with Release.
export linear struct CodeRegion {
    c: sys.VmCode
    n: int
    // bits: the commit bitmap of a region from Reserve, one bit per page,
    // set while the page is committed (as a Region's), of bitn bytes; 0
    // for a region from New, every page of which is committed.
    bits: uint
    bitn: int
}

/// An open exclusive write window into a CodeRegion, returned by
/// CodeRegion.Edit. It borrows the CodeRegion mutably while live, so no
/// concurrent Edit, Commit, Entry or Release can touch the region until Done
/// closes the window. Write code through Bytes, flush the instruction cache
/// with Flush, and end the window with Done.
///
/// Linear: every path must close it with Done (use `defer w.Done()`). Do not
/// hold a CodeWriter across an Io wait or task suspension: on arm64 macOS
/// JIT write protection belongs to the calling OS thread, so resuming on
/// another carrier would leave the original carrier write-enabled.
export linear view struct CodeWriter {
    r: *CodeRegion
    off: int
    len: int
    ranged: bool
}

/// A code region of size bytes, rounded up to whole pages, committed whole
/// and sealed (not writable until Edit).
///
/// Fails with Misaligned for a size that is not positive, Os when the OS
/// refuses (Os(38), ENOSYS, on x86_64-nutshell), and Unsupported while 64
/// code regions are live: a fault in a 65th could not reach
/// runtime.AddFaultFilter's filters. O(1) system calls, and a scan of the
/// 64 region slots; allocates nothing on the heap.
export CodeRegion.New(size: int): Error!CodeRegion {
    n := try CodeSize(size)
    c := sys.VmCodeNew(n, true)
    if c.err != 0 {
        fail Os(code: c.err)
    }
    if !CodeAdd(&c, n) {
        fail Unsupported
    }
    return CodeRegion(c: c, n: n, bits: 0, bitn: 0)
}

/// A code region of size bytes, rounded up to whole pages, reserved with
/// no page committed. A page can be written (inside Edit) and run only once
/// Commit takes it. A write to a page not committed faults, and so does a
/// read or call into one (a call into one faults as a fault in a code
/// region, which goes to runtime.AddFaultFilter's filters). On Windows it is
/// a section made SEC_RESERVE; on Linux a memfd with both views mapped with
/// no access; on macOS a mapping with PROT_NONE (MAP_JIT on arm64).
///
/// Fails as New does, and with OutOfMemory when its commit bitmap (one bit
/// per page rounded up to a page, as a Region's) cannot be made. O(1)
/// system calls, and a scan of the 64 region slots; allocates nothing on
/// the heap.
export CodeRegion.Reserve(size: int): Error!CodeRegion {
    n := try CodeSize(size)
    bitn := BitBytes(n)
    b := NewBits(bitn)
    if raw.IsNull(b) {
        fail OutOfMemory
    }
    c := sys.VmCodeNew(n, false)
    if c.err != 0 {
        sys.Release(b, bitn)
        fail Os(code: c.err)
    }
    if !CodeAdd(&c, n) {
        sys.Release(b, bitn)
        fail Unsupported
    }
    return CodeRegion(c: c, n: n, bits: raw.Addr(b), bitn: bitn)
}

// CodeSize is size rounded up to whole pages: Misaligned for a size that is
// not positive, OutOfMemory past 2^62.
CodeSize(size: int): Error!int {
    if size <= 0 {
        fail Misaligned
    }
    if size > (1 << 62) {
        fail OutOfMemory
    }
    ps := sys.PageSize()
    return (size + ps - 1) / ps * ps
}

// CodeAdd records c's executable view in the fault path's table of code
// regions, so a fault in it goes to runtime.AddFaultFilter's filters and is
// never reported as a fault in Mk code; when the table is full it
// gives both views back and is false.
CodeAdd(c: &sys.VmCode, n: int): bool {
    if !sys.CodeRegionAdd(raw.Addr(c.x), raw.Addr(c.x) + Trunc[uint](n)) {
        sys.VmCodeFree(c, n)
        return false
    }
    return true
}

/// The region's size in bytes, a whole number of pages. O(1); allocates
/// nothing.
export CodeRegion.Size(&self): int {
    return self.n
}

/// The executable address of the byte at offset, to pass to
/// raw.FnFromAddr, which makes the fn value. An offset outside
/// [0, Size()) panics with the bounds code (2). O(1); allocates nothing.
export CodeRegion.Entry(&self, offset: int): uint {
    if offset < 0 || offset >= self.n {
        BadRange("CodeRegion.Entry: offset outside the region")
    }
    return raw.Addr(self.c.x) + Trunc[uint](offset)
}

/// Commits [offset, offset + len) of a region from Reserve. Its pages not
/// yet committed read as zero and become sealed (non-writable until Edit)
/// and executable at Entry. Pages already committed keep their contents and
/// protection. On a region from New, whose pages are all committed, it only
/// checks the range. Taking mut self prevents Commit from racing with Edit
/// or Entry.
///
/// Fails with Misaligned for a range that is not whole pages inside the
/// region, and Os when the OS refuses; the runs committed before it stay
/// committed. O(len / (64 pages)) bitmap words, plus one system call per
/// run of uncommitted pages (two on Linux); allocates nothing.
export CodeRegion.Commit(mut self, offset: int, len: int): Error!void {
    if !Pages(offset) || !Pages(len) || len == 0 || offset > self.n - len {
        fail Misaligned
    }
    if self.bits == 0 {
        return
    }
    ps := sys.PageSize()
    end := (offset + len) / ps
    var at := NextPage(self.bits, offset / ps, end, false)
    for at < end {
        stop := NextPage(self.bits, at, end, true)
        e := sys.VmCodeCommit(&self.c, at * ps, (stop - at) * ps)
        if e != 0 {
            fail Os(code: e)
        }
        MarkPages(self.bits, at, stop - at, true)
        at = NextPage(self.bits, stop, end, false)
    }
}

/// Opens an exclusive write window into the region, returning a linear
/// CodeWriter borrowing self. On Windows and Linux every committed page of
/// the writable view becomes read-write while the executable view stays
/// read-execute; on arm64 macOS with per-thread JIT write protection the
/// calling thread switches to JIT write mode (pthread_jit_write_protect_np(0))
/// while other threads remain in execute mode; on x86_64 macOS and arm64 macOS
/// fallback every committed page switches from read-execute to read-write.
///
/// Fails with Os when the OS refuses; any pages already made writable before
/// the refusal are sealed again as far as the OS allows and the region stays
/// sealed. O(1) on arm64 macOS with MAP_JIT; on Windows, Linux, x86_64 macOS
/// and arm64 macOS fallback O(Size / (64 pages)) bitmap words plus one system
/// call per run of committed pages (one in all for a region from New);
/// allocates nothing.
export CodeRegion.Edit(mut self): Error!CodeWriter from self {
    e := sys.VmCodeBeginWrite(&self.c)
    if e != 0 {
        fail Os(code: e)
    }
    if sys.VmCodePerPage(&self.c) {
        CodeProtect(&*self, true) else err {
            CodeSealAll(&*self)
            sys.VmCodeEndWrite(&self.c)
            fail err
        }
    }
    return CodeWriter(r: raw.OfMut(self), off: 0, len: self.n, ranged: false)
}

/// Opens an exclusive write window covering only `[offset, offset + len)`,
/// returning a linear CodeWriter borrowing self. Both offset and len must be
/// whole multiples of PageSize() within `[0, Size()]`, and every page in
/// `[offset, offset + len)` must already be committed. `len == 0` at a valid
/// page-aligned offset is a clean no-op that touches no page protections.
///
/// Unlike Edit, only the pages in `[offset, offset + len)` become writable on
/// targets with per-page mappings, and Done seals only that range;
/// `CodeWriter.Bytes()` returns a `len`-byte slice whose index 0 maps to
/// `offset` in the region, and `CodeWriter.Flush(rel_off, rel_len)` flushes
/// relative to `offset`. On arm64 macOS with per-thread MAP_JIT write
/// protection, JIT write mode is still per calling thread.
///
/// Fails with Misaligned when offset or len is negative, not a multiple of
/// PageSize(), or outside the region; fails with Os(487) when any page in
/// `[offset, offset + len)` of a reserved region is uncommitted; fails with Os
/// when the OS refuses protection. O(1) system calls; allocates nothing.
export CodeRegion.EditRange(mut self, offset: int, len: int): Error!CodeWriter from self {
    if offset < 0 || len < 0 || !Pages(offset) || !Pages(len) || offset > self.n || len > self.n - offset {
        fail Misaligned
    }
    if len == 0 {
        return CodeWriter(r: raw.OfMut(self), off: offset, len: 0, ranged: true)
    }
    if self.bits != 0 {
        ps := sys.PageSize()
        if !AllCommitted(self.bits, offset / ps, len / ps) {
            fail Os(code: 487)
        }
    }
    e := sys.VmCodeBeginWrite(&self.c)
    if e != 0 {
        fail Os(code: e)
    }
    if sys.VmCodePerPage(&self.c) {
        ep := sys.VmCodeProtect(&self.c, offset, len, true)
        if ep != 0 {
            sys.VmCodeEndWrite(&self.c)
            fail Os(code: ep)
        }
    }
    return CodeWriter(r: raw.OfMut(self), off: offset, len: len, ranged: true)
}

// CodeProtect sets the committed pages of r to writable (true) or sealed
// (false): the whole region for New, else each run of committed pages, since
// uncommitted pages stay inaccessible (and Windows refuses a protection
// change that spans one, 487). Stops at the first OS refusal.
CodeProtect(r: &CodeRegion, writable: bool): Error!void {
    if r.bits == 0 {
        e := sys.VmCodeProtect(&r.c, 0, r.n, writable)
        if e != 0 {
            fail Os(code: e)
        }
        return
    }
    ps := sys.PageSize()
    end := r.n / ps
    var at := NextPage(r.bits, 0, end, true)
    for at < end {
        stop := NextPage(r.bits, at, end, false)
        e := sys.VmCodeProtect(&r.c, at * ps, (stop - at) * ps, writable)
        if e != 0 {
            fail Os(code: e)
        }
        at = NextPage(r.bits, stop, end, true)
    }
}

// CodeSealAll seals every committed run of r, continuing past any OS refusal
// so remaining committed runs are still sealed.
CodeSealAll(r: &CodeRegion) {
    if r.bits == 0 {
        _ = sys.VmCodeProtect(&r.c, 0, r.n, false)
        return
    }
    ps := sys.PageSize()
    end := r.n / ps
    var at := NextPage(r.bits, 0, end, true)
    for at < end {
        stop := NextPage(r.bits, at, end, false)
        _ = sys.VmCodeProtect(&r.c, at * ps, (stop - at) * ps, false)
        at = NextPage(r.bits, stop, end, true)
    }
}

/// The region's writable bytes while this CodeWriter lives. What they hold
/// is seen through the executable view after Flush and Done. A write to a
/// page that a region from Reserve has not committed faults. O(1); allocates
/// nothing.
export CodeWriter.Bytes(mut self): mut []u8 from self {
    unsafe "r is the live CodeRegion this CodeWriter borrowed exclusively until Done; w + off spans len writable bytes" {
        if self.len == 0 {
            return raw.SliceMut((*self.r).c.w, 0)
        }
        return raw.SliceMut(raw.FromAddr[u8](raw.Addr((*self.r).c.w) + Trunc[uint](self.off)), self.len)
    }
}

/// Makes the bytes written at [offset, offset + len) visible to execution:
/// FlushInstructionCache on Windows, sys_icache_invalidate on arm64 macOS,
/// data-cache clean and instruction-cache invalidate on arm64 Linux, and a
/// fence on x86_64 Linux and macOS, whose caches are coherent. Call it after
/// writing code and before Done. For an EditRange writer, offset is relative
/// to the edited range. A range outside the writer window is clipped to it;
/// on arm64 only committed pages may be flushed. O(len / cache line) on arm64,
/// O(1) on x86_64; allocates nothing.
export CodeWriter.Flush(&self, offset: int, len: int) {
    unsafe "r is the live CodeRegion this CodeWriter borrowed exclusively until Done; the address lies inside its executable view [off, off + len), clipped below" {
        wn := self.len
        if wn == 0 {
            return
        }
        var o := offset
        var n := len
        if o < 0 {
            o = 0
        }
        if o > wn {
            o = wn
        }
        if n < 0 || n > wn - o {
            n = wn - o
        }
        if n == 0 {
            return
        }
        sys.VmFlush(raw.FromAddr[u8](raw.Addr((*self.r).c.x) + Trunc[uint](self.off + o)), n)
    }
}

/// Closes the write window and seals the region again: every committed page
/// of the writable view (or only `[off, off + len)` for a writer opened by
/// EditRange with `len > 0`) becomes read-only on Windows and Linux; on
/// x86_64 macOS and arm64 macOS fallback the affected committed pages return
/// to read-execute; on arm64 macOS with MAP_JIT the calling thread's JIT
/// write protection is re-enabled (pthread_jit_write_protect_np(1) when no
/// nested window remains).
///
/// Infallible so `defer w.Done()` works on every path; if the OS refuses a
/// page protection change, Done still attempts every remaining committed
/// run. O(1) on arm64 macOS with MAP_JIT; on Windows, Linux, x86_64 macOS
/// and arm64 macOS fallback O(Size / (64 pages)) bitmap words for Edit or
/// O(1) system call for EditRange; allocates nothing.
export CodeWriter.Done(self) {
    CodeWriter(r, off, len, ranged) := self
    if ranged && len == 0 {
        return
    }
    unsafe "r is the live CodeRegion this linear CodeWriter borrowed exclusively until Done" {
        if sys.VmCodePerPage(&(*r).c) {
            if !ranged {
                CodeSealAll(&*r)
            } else {
                _ = sys.VmCodeProtect(&(*r).c, off, len, false)
            }
        }
        sys.VmCodeEndWrite(&(*r).c)
    }
}

/// Unmaps the region's view(s), and gives back a reserved region's commit
/// bitmap. O(1) system calls; allocates nothing.
export CodeRegion.Release(self) {
    CodeRegion(c, n, bits, bitn) := self
    sys.CodeRegionRemove(raw.Addr(c.x))
    sys.VmCodeFree(&c, n)
    if bits != 0 {
        unsafe "bits is the bitmap Reserve made, of bitn bytes, released once: Release takes the linear region" {
            sys.Release(raw.FromAddr[u8](bits), bitn)
        }
    }
}
