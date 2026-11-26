// Why: most OS operations (file locks, readiness waits, one-off system
// calls) do not need their own Io table entry; one generic entry runs any
// of them, and a program links an operation's code only when it uses it.
//
// What: NativeCall, the native table's call entry, and CallJob, the pool
// job that runs a std.rt job (std/rt/call.mk).
//
// How: the caller's core.IoCallArgs record names the job and its route:
// inline, checked, on the pool (whole or cancellable), a lock wait or a
// readiness wait on the poller. The entry names no operation itself, so a
// program links an operation's code, and its imports, only when it names
// the std.rt function that passes its job.

import std.core
import std.raw
import std.rt
import std.sys

// CallJob runs a pool call: op.buf is the caller's core.IoCallArgs, whose
// job writes its res; the pool reads op.res.
CallJob(op: *sys.Op) {
    unsafe "op.buf is the caller's IoCallArgs, live until the job completes" {
        a := raw.PtrCast[u8, core.IoCallArgs]((*op).buf)
        // The job's cancel word: CancelJob stores 1 in op.cancel when it
        // marks the job, so a long job polls it (IoCallArgs.stop).
        (*a).stop = raw.PtrCast[raw.Atomic[u32], u32](raw.Of(&(*op).cancel))
        job := (*a).job
        job(a)
        r := (*a).res
        (*op).res = sys.OsResult(value: r.value, err: r.err, os: r.os)
    }
}

/// NativeCall is the native table's call entry (core.IoCall): the pool
/// routes check the scope first and run the job on
/// the pool; IoRouteWhole's job is cut by a cancel only while it is queued
/// and is not interrupted once it runs; the
/// inline route runs the job here and is no cancellation point, and the
/// checked route runs it here after the scope check;
/// IoRouteReady runs no job and waits on the poller for a.h's input. a.h, when
/// not 0, is a live runtime handle, whose OS handle the job finds in a.os.
/// O(1) besides the job; allocates nothing.
NativeCall(io: Io, a: *core.IoCallArgs): core.IoResult {
    unsafe "a is the caller's record, live until this returns; a.h is a live runtime handle" {
        if (*a).h != 0 {
            (*a).os = (*rt.Rec((*a).h)).os
        }
        route := (*a).route
        if route == core.IoRouteInline {
            job := (*a).job
            job(a)
            return (*a).res
        }
        if Stop(io) {
            return ErrOf(core.SysErr.Canceled)
        }
        if route == core.IoRouteChecked {
            job := (*a).job
            job(a)
            return (*a).res
        }
        if route == core.IoRouteLock {
            // A lock wait on the poller (sys.OpLock): it holds no
            // thread, and a cancel ends it through PollCancel.
            var l := sys.NewOp(sys.OpLock, (*a).os)
            l.len = if (*a).w.a == 2 { 1 } else { 0 }
            return OnPoller(io, mut l)
        }
        if route == core.IoRouteReady {
            if (*a).op == core.IoOpObjectWait {
                if Target.os != Os.Windows {
                    return ErrOf(core.SysErr.Unsupported)
                }
                var w := sys.NewOp(sys.OpReady, (*a).os)
                w.pos = (*a).w.a
                r := OnPoller(io, mut w)
                sys.PollRelease(raw.OfMut(mut w))
                if w.len != 0 {
                    (*a).w.b = 1
                }
                return r
            }
            // A readiness wait on the poller (sys.OpReady): it holds no
            // thread, and a cancel ends it through PollCancel.
            var w := sys.NewOp(sys.OpReady, (*a).os)
            r := OnPoller(io, mut w)
            sys.PollRelease(raw.OfMut(mut w))
            return r
        }
        var op := sys.NewOp(0, (*a).os)
        op.buf = raw.PtrCast[core.IoCallArgs, u8](a)
        // IoRouteWhole: a cancel or a deadline ends the job only while it
        // waits for a pool thread (OnPoolWhole), never once it runs.
        return if route == core.IoRouteWhole { OnPoolWhole(io, CallJob, mut op) } else { OnPool(io, CallJob, mut op) }
    }
}
