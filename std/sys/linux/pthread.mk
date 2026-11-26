// Why: in system-libc mode glibc keeps per-thread state (errno, malloc's
// per-thread cache, the stack canary at %fs:0x28) in a thread control block,
// which a bare clone would share with its parent; such threads must come
// from glibc.
//
// What: PthreadStart and PthreadMain, the thread path of system-libc mode,
// and PthreadSlack.
//
// How: every thread is made by glibc's pthread_create on a stack this file
// maps. glibc places its control block and the static TLS at the
// top of the given stack, so the mapping holds PthreadSlack bytes more. The
// thread records its stack in the stack table as ThreadStart's clone path
// does: exactly stack bytes, ending at the page boundary above its first
// frame, with a guard page right below. Joinable threads keep their join
// record on a separate page below the guard.

import std.core
import std.raw

// PthreadRec is the new thread's record, in PthreadStart's frame: the
// parent waits on tid (a futex word) for ThreadStart's value, and the
// thread reads the other fields before it publishes tid.
struct PthreadRec {
    entry: fn(*u8)
    arg: *u8
    floor: uint
    stack: int
    tid: raw.Atomic[u32]
    alt: uint
}

struct PthreadJoinRec {
    id: u64
    join: uint
    size: int
    alt: uint
}

const PthreadDetached: uint = 1

// PthreadSlack is the room above the recorded stack for glibc's thread
// control block, its static TLS and the thread's first frames (glibc
// 2.39 takes a few KiB).
const PthreadSlack: int = 65536

// PthreadStackMin is glibc's PTHREAD_STACK_MIN, the smallest stack
// pthread_attr_setstack takes: 16 KiB on x86_64, 128 KiB on aarch64.
PthreadStackMin(): int {
    return if Target.arch == Arch.Arm64 { 131072 } else { 16384 }
}

// PthreadJoinAddr resolves the join entry from the executable's own libc.
// Closing this reference leaves libc and the address live until exit.
PthreadJoinAddr(): uint {
    lib := DlOpen("libc.so.6", 0)
    if lib.err != core.SysErr.None {
        return 0
    }
    h := Trunc[uint](lib.value)
    defer DlClose(h)
    p := DlSymbol(h, "pthread_join") else { return 0 }
    return raw.Addr(p)
}

// PthreadStart is the system-libc branch (stack already checked).
// The detached handle is the thread ID. The joinable handle is its record.
PthreadStart(entry: fn(*u8), arg: *u8, stack: int, joinable: bool): OsResult {
    join := if joinable { PthreadJoinAddr() } else { 0 }
    if joinable && join == 0 {
        return Fail(38)
    }
    size := (stack + 4095) / 4096 * 4096
    want := size + PthreadSlack
    usable := if want < PthreadStackMin() { PthreadStackMin() } else { want }
    prefix := if joinable { 8192 } else { 4096 }
    total := prefix + usable
    base := Reserve(total, 4096)
    if raw.IsNull(base) {
        return FailWith(MapError(12))
    }
    unsafe "the mapping is the new thread's stack for its whole life; the table's slots are the libc functions the entry bound" {
        stack_base := raw.Offset(base, prefix)
        if (joinable && !Commit(base, 4096)) || !Commit(stack_base, usable) {
            Release(base, total)
            return FailWith(MapError(12))
        }
        _ = StackTable()
        var rec := PthreadRec(entry: entry, arg: arg, floor: raw.Addr(stack_base), stack: size, tid: raw.Atomic[u32].New(0), alt: 0)
        var attr: [8]u64 = [0; 8]
        a := raw.PtrCast[u64, u8](raw.DataMut(mut attr[..]))
        var w := LibcFn(LcAttrInit)
        init := raw.Read(raw.PtrCast[u64, fn(*u8): i32](raw.OfMut(mut w)))
        if init(a) != 0 {
            Release(base, total)
            return Fail(12)
        }
        w = LibcFn(LcAttrSetStack)
        set_stack := raw.Read(raw.PtrCast[u64, fn(*u8, *u8, uint): i32](raw.OfMut(mut w)))
        w = LibcFn(LcAttrSetDetach)
        set_detach := raw.Read(raw.PtrCast[u64, fn(*u8, i32): i32](raw.OfMut(mut w)))
        w = LibcFn(LcAttrDestroy)
        destroy := raw.Read(raw.PtrCast[u64, fn(*u8): i32](raw.OfMut(mut w)))
        var rc := set_stack(a, stack_base, Trunc[uint](usable))
        if rc == 0 && !joinable {
            rc = set_detach(a, i32(PthreadDetached))
        }
        var id: u64 = 0
        if rc == 0 {
            w = LibcFn(LcPthreadCreate)
            create := raw.Read(raw.PtrCast[u64, fn(*u64, *u8, fn(*u8): *u8, *u8): i32](raw.OfMut(mut w)))
            rc = create(raw.OfMut(mut id), a, PthreadMain, raw.PtrCast[PthreadRec, u8](raw.OfMut(mut rec)))
        }
        _ = destroy(a)
        if rc != 0 {
            Release(base, total)
            return Fail(i64(rc))
        }
        // The thread publishes its tid before it runs entry; rec stays in
        // this frame until then.
        tw := raw.Of(&rec.tid)
        for {
            t := (*tw).Load()
            if t != 0 {
                if joinable {
                    raw.Write(raw.PtrCast[u8, PthreadJoinRec](base), PthreadJoinRec(id: id, join: join, size: total, alt: rec.alt))
                    return Ok(Trunc[i64](raw.Addr(base)))
                }
                return Ok(i64(t))
            }
            _ = Syscall(Nrs().futex, raw.Addr(tw), FutexWaitPrivate, 0, 0, 0, 0)
        }
    }
}

PthreadJoin(h: i64) {
    unsafe "h points to the mapped join record of a joinable pthread that has not been joined" {
        base := raw.FromAddr[u8](Trunc[uint](h))
        rec := raw.Read(raw.PtrCast[u8, PthreadJoinRec](base))
        join := raw.FnFromAddr[fn(u64, **u8): i32](rec.join)
        if join(rec.id, raw.Null[*u8]()) == 0 {
            if rec.alt != 0 {
                Release(raw.FromAddr[u8](rec.alt), AltStackBytes)
            }
            Release(base, rec.size)
        }
    }
}

// PthreadMain is every system-libc thread's start routine. Its stack is
// [lo, hi): hi the page boundary above its own frame (the scheduler's frames
// under it are inside, as on the clone path; above it is glibc's block),
// lo = hi - stack, with the page below lo
// made the guard (the mapping's own guard page when the slack is used up
// exactly). Then the alternate signal stack, the stack table's entry, the
// tid for the parent, and entry(arg). Returning ends the thread through
// glibc. A joined thread's mappings are released by its joiner.
PthreadMain(p: *u8): *u8 {
    var top: u64 = 0
    hi := (raw.Addr(raw.Of(&top)) + 4095) & ~Trunc[uint](4095)
    unsafe "p is the PthreadRec in PthreadStart's frame, which waits for tid" {
        rec := raw.PtrCast[u8, PthreadRec](p)
        f := (*rec).entry
        arg := (*rec).arg
        floor := (*rec).floor
        var lo := hi - Trunc[uint]((*rec).stack)
        if lo < floor {
            lo = floor
        }
        if lo > floor {
            _ = GuardPage(raw.FromAddr[u8](lo - 4096))
        }
        (*rec).alt = AltStackInit()
        e := StackClaim(lo, hi)
        tw := raw.Of(&(*rec).tid)
        (*tw).Store(Trunc[u32](ThreadId()))
        _ = Syscall(Nrs().futex, raw.Addr(tw), FutexWakePrivate, 1, 0, 0, 0)
        f(arg)
        StackFree(e)
    }
    return raw.Null[u8]()
}
