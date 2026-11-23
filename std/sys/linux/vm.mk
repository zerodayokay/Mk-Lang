// Why: std.vm (JIT code, guarded memory) and std.linux need protection
// changes, executable memory that is never writable and executable at once,
// and a raw system call.
//
// What: VmProtect, VmCode with VmCodeNew, VmCodeCommit, VmCodePerPage,
// VmCodeBeginWrite, VmCodeEndWrite, VmCodeProtect and VmCodeFree, VmFlush
// and RawSyscall.
//
// How: a code region is two views of one memfd (W^X: the writable mapping
// kept PROT_READ outside an active CodeWriter, the executable mapping
// PROT_READ|PROT_EXEC throughout, never one page both). Each call returns the
// kernel's errno (positive) or 0, so the caller can make vm.Error.Os(code).

import std.raw

const VmProtRead: uint = 1
const VmProtExec: uint = 4
const VmMapShared: uint = 0x01
// MFD_CLOEXEC (include/uapi/linux/memfd.h): the code region's fd is never
// inherited by a child.
const MfdCloexec: uint = 1
// MFD_EXEC (Linux 6.3, the same header): the memfd may be mapped
// executable even under sysctl vm.memfd_noexec=1 or 2.
// An older kernel refuses the unknown flag with EINVAL, and then the
// memfd is made without it, as before 6.3, when every memfd was executable.
const MfdExec: uint = 0x10
const VmEinval: int = -22

/// VmErrno is a raw result's errno, 0 when it is not an error.
/// O(1); allocates nothing.
VmErrno(r: int): i64 {
    return if IsErr(r) { i64(-r) } else { 0 }
}

/// VmProtect sets the protection of [p, p + size), page-aligned and inside
/// a reservation the caller owns: prot 0 none, 1 read, 2 read-write (never
/// executable: std.vm's Prot has no such value). 0, or the errno.
/// O(1): one system call; allocates nothing.
export VmProtect(p: *u8, size: int, prot: u32): i64 {
    pr: uint = if prot == 2 { ProtRW } else if prot == 1 { VmProtRead } else { ProtNone }
    unsafe "system call; [p, p + size) lies in a reservation the caller owns" {
        return VmErrno(Syscall(Nrs().mprotect, raw.Addr(p), Trunc[uint](size), pr, 0, 0, 0))
    }
}

/// VmCode is a code region's two views of one memfd: w the writable view
/// (read-only while sealed, read-write during Edit), x read-execute, each
/// size bytes; err the errno when it could not be made (then both are null).
export struct VmCode {
    export w: *u8
    export x: *u8
    export err: i64
}

// VmName is the memfd's name, as /proc/<pid>/maps shows it ("memfd:mk-code").
const VmName: str = "mk-code\0"

/// VmCodeNew makes a code region of size bytes (a whole number of pages),
/// sealed by default: memfd_create (MFD_EXEC where the kernel knows it),
/// ftruncate, then one MAP_SHARED mapping read-only and one read-execute of
/// the same pages (commit), or both with no access until VmCodeCommit (not
/// commit); the fd is closed, the mappings keep the memory. On any failure
/// what was made is undone and err holds the errno.
/// O(1): five system calls (six on a kernel before 6.3); commits nothing
/// until written.
export VmCodeNew(size: int, commit: bool): VmCode {
    wp: uint = if commit { VmProtRead } else { ProtNone }
    xp: uint = if commit { VmProtRead | VmProtExec } else { ProtNone }
    unsafe "system calls on an fd this function owns and closes; the mappings are new and the caller's" {
        var fd := Syscall(Nrs().memfd_create, raw.Addr(raw.StrData(VmName)), MfdCloexec | MfdExec, 0, 0, 0, 0)
        if fd == VmEinval {
            fd = Syscall(Nrs().memfd_create, raw.Addr(raw.StrData(VmName)), MfdCloexec, 0, 0, 0, 0)
        }
        if IsErr(fd) {
            return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: VmErrno(fd))
        }
        ufd := Trunc[uint](fd)
        t := Syscall(Nrs().ftruncate, ufd, Trunc[uint](size), 0, 0, 0, 0)
        if IsErr(t) {
            _ = Syscall(Nrs().close, ufd, 0, 0, 0, 0, 0)
            return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: VmErrno(t))
        }
        w := Syscall(Nrs().mmap, 0, Trunc[uint](size), wp, VmMapShared, ufd, 0)
        if IsErr(w) {
            _ = Syscall(Nrs().close, ufd, 0, 0, 0, 0, 0)
            return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: VmErrno(w))
        }
        x := Syscall(Nrs().mmap, 0, Trunc[uint](size), xp, VmMapShared, ufd, 0)
        _ = Syscall(Nrs().close, ufd, 0, 0, 0, 0, 0)
        if IsErr(x) {
            _ = Syscall(Nrs().munmap, Trunc[uint](w), Trunc[uint](size), 0, 0, 0, 0)
            return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: VmErrno(x))
        }
        return VmCode(w: raw.FromAddr[u8](Trunc[uint](w)), x: raw.FromAddr[u8](Trunc[uint](x)), err: 0)
    }
}

/// VmCodeCommit commits [offset, offset + size) of a region VmCodeNew made
/// with commit false, page-aligned and inside it, in the sealed state: the
/// writable view's pages become read-only, then the executable view's
/// read-execute; the memfd supplies pages on touch, so nothing is charged
/// before it. When the second mprotect fails the first is undone. 0, or the
/// errno.
/// O(1): two system calls; allocates nothing.
export VmCodeCommit(c: &VmCode, offset: int, size: int): i64 {
    o := Trunc[uint](offset)
    n := Trunc[uint](size)
    unsafe "system calls; both ranges lie in the caller's views" {
        e := Syscall(Nrs().mprotect, raw.Addr(c.w) + o, n, VmProtRead, 0, 0, 0)
        if IsErr(e) {
            return VmErrno(e)
        }
        x := Syscall(Nrs().mprotect, raw.Addr(c.x) + o, n, VmProtRead | VmProtExec, 0, 0, 0)
        if IsErr(x) {
            _ = Syscall(Nrs().mprotect, raw.Addr(c.w) + o, n, ProtNone, 0, 0, 0)
            return VmErrno(x)
        }
        return 0
    }
}

/// VmCodePerPage is true on Linux: entering and leaving a write window
/// updates the writable view's committed runs through VmCodeProtect.
/// O(1); allocates nothing.
export VmCodePerPage(): bool {
    return true
}

/// VmCodeBeginWrite is a no-op on Linux (VmCodeProtect unseals committed
/// pages of the writable view). O(1); allocates nothing.
export VmCodeBeginWrite(c: &VmCode): i64 {
    return 0
}

/// VmCodeEndWrite is a no-op on Linux (VmCodeProtect seals committed pages of
/// the writable view). O(1); allocates nothing.
export VmCodeEndWrite(c: &VmCode) {
}

/// VmCodeProtect sets the writable view's committed pages [offset, offset +
/// size) to PROT_READ|PROT_WRITE (writable) or PROT_READ (sealed); the
/// executable view is untouched. 0, or the errno.
/// O(1): one system call; allocates nothing.
export VmCodeProtect(c: &VmCode, offset: int, size: int, writable: bool): i64 {
    pr: uint = if writable { ProtRW } else { VmProtRead }
    unsafe "system call; [w + offset, w + offset + size) lies in the caller's writable view" {
        return VmErrno(Syscall(Nrs().mprotect, raw.Addr(c.w) + Trunc[uint](offset), Trunc[uint](size), pr, 0, 0, 0))
    }
}

/// VmCodeFree unmaps both views VmCodeNew made of size bytes.
/// O(1): two system calls; gives back the region. Allocates nothing.
export VmCodeFree(c: &VmCode, size: int) {
    unsafe "system calls; both views are the caller's and no longer used" {
        _ = Syscall(Nrs().munmap, raw.Addr(c.w), Trunc[uint](size), 0, 0, 0, 0)
        _ = Syscall(Nrs().munmap, raw.Addr(c.x), Trunc[uint](size), 0, 0, 0, 0)
    }
}

/// VmFlush makes code written through a region's writable view visible to
/// execution through its executable view at [x, x + n). On x86_64 the
/// instruction cache is coherent with stores (Intel SDM vol. 3, 11.6), so a
/// fence suffices. aarch64 cleans each data cache line to the point of
/// unification (dc cvau) and invalidates each instruction cache line (ic
/// ivau), at the line sizes CTR_EL0 states (Arm ARM, B2.4.4), then
/// synchronizes the pipeline (isb). Linux lets EL0 read CTR_EL0 and run
/// both maintenance instructions.
/// O(n / line); allocates nothing.
export VmFlush(x: *u8, n: int) {
    if Target.arch == Arch.Arm64 {
        if n <= 0 {
            return
        }
        lo := raw.Addr(x)
        hi := lo + Trunc[uint](n)
        unsafe "cache maintenance over [x, x + n), which the caller's region holds; it writes no memory" {
            asm("mrs {c}, ctr_el0\nmov {s}, #4\nubfx {t}, {c}, #16, #4\nlsl {t}, {s}, {t}\nsub {u}, {t}, #1\nbic {p}, {lo}, {u}\n1:\ndc cvau, {p}\nadd {p}, {p}, {t}\ncmp {p}, {hi}\nb.lo 1b\ndsb ish\nand {t}, {c}, #15\nlsl {t}, {s}, {t}\nsub {u}, {t}, #1\nbic {p}, {lo}, {u}\n2:\nic ivau, {p}\nadd {p}, {p}, {t}\ncmp {p}, {hi}\nb.lo 2b\ndsb ish\nisb",
                c: out(reg) _, s: out(reg) _, t: out(reg) _, u: out(reg) _, p: out(reg) _, lo: in(reg) lo, hi: in(reg) hi, options(nostack))
        }
    } else {
        raw.Fence()
    }
}

/// RawSyscall is std.linux's Syscall: the kernel's raw result, -4095 to -1
/// being -errno. It passes no Mk memory of its own: what each
/// argument means is the call's.
/// O(1): one system call. Allocates nothing.
export RawSyscall(nr: uint, a0: uint, a1: uint, a2: uint, a3: uint, a4: uint, a5: uint): int {
    unsafe "std.linux.Syscall is unsafe only (E1101): its caller justifies every argument" {
        return Syscall(nr, a0, a1, a2, a3, a4, a5)
    }
}
