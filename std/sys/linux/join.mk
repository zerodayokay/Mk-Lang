// Why: runtime.Thread and Mk libraries need threads that can be joined,
// which the runtime's own detached threads are not.
//
// What: ThreadStartJoin and ThreadJoin, behind sys.ThreadJoin.
//
// How: the thread is cloned with its ThreadRec's ctid set to 1, which the
// kernel clears to 0 and wakes on futex when the thread exits
// (CLONE_CHILD_CLEARTID, in CloneThread); the join waits for 0, then unmaps
// the thread's stack, guard page and alternate signal stack. System-libc
// mode uses pthread_create and pthread_join on the same explicit mappings.

import std.raw

const FutexWait: uint = 0
// The wait is shared (no FUTEX_PRIVATE_FLAG): the kernel wakes a cleared
// child TID with a shared FUTEX_WAKE.

/// ThreadStartJoin is ThreadStart for a thread ThreadJoin will join: value
/// is the address of its join record (the handle ThreadJoin takes), not the
/// tid. Its stack stays mapped until the join. O(1) system calls, as
/// ThreadStart. Maps the thread's stack; allocates nothing on the heap.
export ThreadStartJoin(entry: fn(*u8), arg: *u8, stack: int): OsResult {
    if stack <= 0 || stack > MaxThreadStack {
        return Fail(22)
    }
    if LibcBuild() {
        return PthreadStart(entry, arg, stack, true)
    }
    total := (stack + 4095) / 4096 * 4096 + 4096
    base := Reserve(total, 4096)
    if raw.IsNull(base) {
        return FailWith(MapError(12))
    }
    unsafe "the mapping is this thread's stack until ThreadJoin unmaps it" {
        if !Commit(raw.Offset(base, 4096), total - 4096) {
            Release(base, total)
            return FailWith(MapError(12))
        }
        _ = StackTable()
        top := raw.Offset(base, total - 64)
        rec := raw.PtrCast[u8, ThreadRec](top)
        raw.Write(rec, ThreadRec(entry: entry, arg: arg, tid: 0, ctid: 1, lo: raw.Addr(base) + 4096,
            hi: raw.Addr(base) + Trunc[uint](total), alt: 0))
        r := Clone(CloneThread, top, raw.Of(&(*rec).tid), raw.Of(&(*rec).ctid), 0, ThreadMain, top)
        if IsErr(r) {
            Release(base, total)
            return Fail(i64(-r))
        }
        return Ok(Cast[i64](raw.Addr(top)))
    }
}

/// ThreadJoin waits until the thread h (ThreadStartJoin's value) has ended,
/// then unmaps its stack and its 64 KiB alternate signal stack (ThreadInit's,
/// recorded by ThreadMain). Blocks the calling OS thread: a task joins through
/// the blocking pool (runtime.Joined.Join). O(1) system calls besides the
/// wait. Allocates nothing.
export ThreadJoin(h: i64) {
    if LibcBuild() {
        PthreadJoin(h)
        return
    }
    unsafe "h is the ThreadRec ThreadStartJoin wrote at its stack's top; ctid is the word the kernel clears at the thread's exit" {
        rec := raw.FromAddr[ThreadRec](Trunc[uint](h))
        w := raw.Of(&(*rec).ctid)
        for {
            // Edge: the kernel's clear of ctid at the thread's exit comes
            // after the thread's last write, so a Load of 0 sees them all.
            v := (*raw.PtrCast[u32, raw.Atomic[u32]](w)).Load()
            if v == 0 {
                break
            }
            _ = Syscall(Nrs().futex, raw.Addr(w), FutexWait, uint(v), 0, 0, 0)
        }
        alt := (*rec).alt
        if alt != 0 {
            Release(raw.FromAddr[u8](alt), AltStackBytes)
        }
        lo := (*rec).lo - 4096
        hi := (*rec).hi
        Release(raw.FromAddr[u8](lo), Trunc[int](hi - lo))
    }
}
