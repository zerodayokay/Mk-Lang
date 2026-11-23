// Why: std.vm (JIT code, guarded memory) needs protection changes and
// executable memory that is never writable and executable at once (W^X), and
// std.linux's raw system call must answer on every target that names it.
//
// What: VmProtect, VmCode with VmCodeNew, VmCodeCommit, VmCodePerPage,
// VmCodeBeginWrite, VmCodeEndWrite, VmCodeProtect and VmCodeFree, VmFlush
// and RawSyscall.
//
// How: VmProtect is mprotect with Darwin's protections. Code regions use one
// mapping (w == x): on arm64 macOS an anonymous MAP_JIT mapping whose
// writability switches per thread through pthread_jit_write_protect_np (1
// execute mode by default, 0 write mode inside an active CodeWriter, with a
// per-thread nesting table so overlapping edits on one thread do not drop
// write mode early); on x86_64 macOS a private anonymous mapping whose
// committed pages toggle between PROT_READ|PROT_EXEC and PROT_READ|PROT_WRITE
// via mprotect (never RWX). macOS has no stable system-call interface
// (libSystem is the only one), so RawSyscall refuses. Each call returns the
// errno (positive) or 0, so the caller can make vm.Error.Os(code).

import std.raw

const VmProtRead: i32 = 1
const VmProtExec: i32 = 4
const VmProtRX: i32 = 5
const VmProtRWX: i32 = 7
// MAP_JIT (<sys/mman.h>): anonymous executable mapping on arm64 macOS whose
// write-vs-execute mode switches per thread via pthread_jit_write_protect_np.
const VmMapJit: i32 = 0x0800
// VmEnosys is Darwin's ENOSYS.
const VmEnosys: i64 = 78

// JitWrites tracks how many nested CodeWriter windows each OS thread holds on
// arm64 macOS, so an inner Done or a CodeRegion.New on the same thread does
// not re-enable write protection while an outer CodeWriter is still live. At
// most 64 code regions can be live at once (filter.mk's CodeSlots).
const JitSlots: int = 64

struct JitState {
    tid: [64]u64
    depth: [64]u32
}

var JitWrites: JitState = JitState(tid: [0; 64], depth: [0; 64])

JitTidWord(k: int): &raw.Atomic[u64] {
    unsafe "JitWrites is static; a u64 has raw.Atomic[u64]'s layout, and every access to tid is atomic" {
        return &*raw.PtrCast[u64, raw.Atomic[u64]](raw.Of(&JitWrites.tid[k]))
    }
}

JitDepthWord(k: int): &raw.Atomic[u32] {
    unsafe "JitWrites is static; a u32 has raw.Atomic[u32]'s layout, and slot k's depth is accessed only by its owner thread" {
        return &*raw.PtrCast[u32, raw.Atomic[u32]](raw.Of(&JitWrites.depth[k]))
    }
}

// JitEnsureExec puts the calling thread into execute mode unless it already
// holds an open CodeWriter window.
JitEnsureExec() {
    unsafe "pthread_self and pthread_jit_write_protect_np are libSystem calls passing no memory" {
        me := pthread_self()
        for k in 0..JitSlots {
            if JitTidWord(k).Load() == me {
                return
            }
        }
        pthread_jit_write_protect_np(1)
    }
}

// JitWriteEnter opens a write window on the calling thread, switching to write
// mode (0) on the 0 -> 1 nesting transition.
JitWriteEnter() {
    unsafe "pthread_self and pthread_jit_write_protect_np are libSystem calls passing no memory" {
        me := pthread_self()
        for k in 0..JitSlots {
            if JitTidWord(k).Load() == me {
                d := JitDepthWord(k)
                // Edge: relaxed; slot k belongs to this thread alone while tid[k] == me.
                d.StoreOrd(d.LoadOrd(0) + 1, 0)
                return
            }
        }
        for k in 0..JitSlots {
            if JitTidWord(k).Load() == 0 && JitTidWord(k).Cas(0, me) {
                JitDepthWord(k).StoreOrd(1, 0)
                pthread_jit_write_protect_np(0)
                return
            }
        }
        pthread_jit_write_protect_np(0)
    }
}

// JitWriteExit closes a write window on the calling thread, restoring execute
// mode (1) on the 1 -> 0 nesting transition.
JitWriteExit() {
    unsafe "pthread_self and pthread_jit_write_protect_np are libSystem calls passing no memory" {
        me := pthread_self()
        for k in 0..JitSlots {
            if JitTidWord(k).Load() == me {
                d := JitDepthWord(k)
                n := d.LoadOrd(0)
                if n > 1 {
                    d.StoreOrd(n - 1, 0)
                    return
                }
                d.StoreOrd(0, 0)
                // Edge: seq_cst; clears the slot after depth is zeroed so the
                // next thread claiming slot k never races with depth.
                JitTidWord(k).Store(0)
                pthread_jit_write_protect_np(1)
                return
            }
        }
        pthread_jit_write_protect_np(1)
    }
}

/// VmProtect sets the protection of [p, p + size), page-aligned and inside
/// a reservation the caller owns: prot 0 none, 1 read, 2 read-write (never
/// executable: std.vm's Prot has no such value). 0, or the errno.
/// O(1): one system call; allocates nothing.
export VmProtect(p: *u8, size: int, prot: u32): i64 {
    pr: i32 = if prot == 2 { ProtRW } else if prot == 1 { VmProtRead } else { ProtNone }
    unsafe "mprotect of [p, p + size), which lies in a reservation the caller owns" {
        return if mprotect(p, Trunc[uint](size), pr) == 0 { 0 } else { Errno() }
    }
}

/// VmCode is a code region's mapping: on macOS w and x name the same mapping
/// of size bytes, sealed by default; err the errno when it could not be made
/// (then both are null).
export struct VmCode {
    export w: *u8
    export x: *u8
    export err: i64
}

/// VmCodeNew makes a code region of size bytes (a whole number of pages),
/// sealed by default: on arm64 macOS an anonymous MAP_JIT mapping (RWX if
/// commit, PROT_NONE otherwise, with pthread_jit_write_protect_np(1) active);
/// on x86_64 macOS a private anonymous mapping (RX if commit, PROT_NONE
/// otherwise). On failure both views are null and err holds the errno.
/// O(1) system calls; allocates nothing.
export VmCodeNew(size: int, commit: bool): VmCode {
    if Target.arch == Arch.Arm64 {
        unsafe "libSystem: pthread_jit_write_protect_supported_np and mmap of a new anonymous MAP_JIT range" {
            if pthread_jit_write_protect_supported_np() == 0 {
                return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: VmEnosys)
            }
            prot: i32 = if commit { VmProtRWX } else { ProtNone }
            p := mmap(raw.Null[u8](), Trunc[uint](size), prot, MapPrivate | MapAnon | VmMapJit, -1, 0)
            if raw.Addr(p) == MapFailed {
                return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: Errno())
            }
            JitEnsureExec()
            return VmCode(w: p, x: p, err: 0)
        }
    }
    prot: i32 = if commit { VmProtRX } else { ProtNone }
    unsafe "libSystem: mmap of a new anonymous code range" {
        p := mmap(raw.Null[u8](), Trunc[uint](size), prot, MapPrivate | MapAnon, -1, 0)
        if raw.Addr(p) == MapFailed {
            return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: Errno())
        }
        return VmCode(w: p, x: p, err: 0)
    }
}

/// VmCodeCommit commits [offset, offset + size) of a region VmCodeNew made
/// with commit false, page-aligned and inside it, in the sealed state: on
/// arm64 macOS PROT_READ|PROT_WRITE|PROT_EXEC under MAP_JIT (executable while
/// write-protected); on x86_64 macOS PROT_READ|PROT_EXEC. 0, or the errno.
/// O(1): one system call; allocates nothing.
export VmCodeCommit(c: &VmCode, offset: int, size: int): i64 {
    prot: i32 = if Target.arch == Arch.Arm64 { VmProtRWX } else { VmProtRX }
    unsafe "mprotect of [x + offset, x + offset + size), which lies in the caller's code region" {
        p := raw.FromAddr[u8](raw.Addr(c.x) + Trunc[uint](offset))
        return if mprotect(p, Trunc[uint](size), prot) == 0 { 0 } else { Errno() }
    }
}

/// VmCodePerPage is whether entering and leaving a write window updates page
/// protections over each committed run: false on arm64 macOS (handled once
/// per thread by VmCodeBeginWrite and VmCodeEndWrite), true on x86_64 macOS.
/// O(1); allocates nothing.
export VmCodePerPage(): bool {
    return Target.arch != Arch.Arm64
}

/// VmCodeBeginWrite opens a write window on c: on arm64 macOS switches the
/// calling thread out of JIT write-protection (0); on x86_64 macOS no-op
/// (VmCodeProtect toggles the committed pages). 0, or the errno.
/// O(1); allocates nothing.
export VmCodeBeginWrite(c: &VmCode): i64 {
    if Target.arch == Arch.Arm64 {
        JitWriteEnter()
    }
    return 0
}

/// VmCodeEndWrite closes a write window on c: on arm64 macOS restores the
/// calling thread's JIT write-protection (1) when no nested window remains on
/// this thread; on x86_64 macOS no-op. O(1); allocates nothing.
export VmCodeEndWrite(c: &VmCode) {
    if Target.arch == Arch.Arm64 {
        JitWriteExit()
    }
}

/// VmCodeProtect switches the committed pages [offset, offset + size) of c
/// between writable (PROT_READ|PROT_WRITE) and sealed (PROT_READ|PROT_EXEC)
/// on x86_64 macOS; no-op on arm64 macOS. 0, or the errno.
/// O(1): one system call on x86_64; allocates nothing.
export VmCodeProtect(c: &VmCode, offset: int, size: int, writable: bool): i64 {
    if Target.arch == Arch.Arm64 {
        return 0
    }
    prot: i32 = if writable { ProtRW } else { VmProtRX }
    unsafe "mprotect of [w + offset, w + offset + size), which lies in the caller's code region" {
        p := raw.FromAddr[u8](raw.Addr(c.w) + Trunc[uint](offset))
        return if mprotect(p, Trunc[uint](size), prot) == 0 { 0 } else { Errno() }
    }
}

/// VmCodeFree unmaps the code region VmCodeNew made of size bytes.
/// O(1): one system call; allocates nothing.
export VmCodeFree(c: &VmCode, size: int) {
    // Both w and x name the single mapping VmCodeNew made, so unmap it once.
    Unmap(c.x, size)
}

/// VmFlush makes code written at [x, x + n) visible to execution there. On
/// arm64 it is libSystem's sys_icache_invalidate (<libkern/OSCacheControl.h>),
/// which cleans the data cache and invalidates the instruction cache over
/// the range; on x86_64 the instruction cache is coherent with stores
/// (Intel SDM vol. 3, 11.6), so a fence suffices.
/// O(n / line); allocates nothing.
export VmFlush(x: *u8, n: int) {
    if Target.arch == Arch.Arm64 {
        if n <= 0 {
            return
        }
        unsafe "cache maintenance over [x, x + n), which the caller's region holds; it writes no memory" {
            sys_icache_invalidate(x, Trunc[uint](n))
        }
    } else {
        raw.Fence()
    }
}

/// RawSyscall makes no system call on macOS, whose system-call numbers are
/// private to libSystem and change between releases: it returns -78
/// (-ENOSYS) for every nr. std.linux, its only caller, builds for Linux
/// targets alone. O(1); allocates nothing.
export RawSyscall(nr: uint, a0: uint, a1: uint, a2: uint, a3: uint, a4: uint, a5: uint): int {
    return -78
}
