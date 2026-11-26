// Why: Windows waitable kernel handles (D3D12 fence events, DXGI frame-latency
// waitables) must suspend the calling task on the poller without holding a
// carrier or pool thread, support a finite timeout, and report a signal that
// arrived during cancellation so semaphore credits are never lost.
//
// What: ObjectWaitResult and ObjectWait.
//
// How: routes Core's IoOpObjectWait through IoRouteReady on the Windows
// poller (a one-shot registered wait on the handle), which sets op.len when
// ProcFired consumed a signal after ProcCancel won.

import std.core

/// The outcome of ObjectWait: err is None on signal or timeout (timed_out
/// distinguishes them), Canceled when the scope cancelled, or an OS error
/// (Unsupported outside Windows or under a Sim).
export struct ObjectWaitResult {
    /// None on signal or timeout, Canceled when cancelled, else the error.
    export err: core.SysErr
    /// True when timeout_ns elapsed before the object signalled.
    export timed_out: bool
    /// True when Canceled was returned after the wait already consumed one
    /// signal on the handle (the caller holds the credit).
    export fired_after_cancel: bool
}

ObjWaitJob(a: *core.IoCallArgs) {
}

/// ObjectWait suspends the calling task on the Windows poller until the
/// kernel waitable handle h signals, timeout_ns nanoseconds elapse (negative:
/// forever), or io's scope cancels. Unsupported outside Windows or on a
/// non-native Io. O(1) system calls; allocates nothing.
export ObjectWait(io: Io, h: uint, timeout_ns: i64): ObjectWaitResult {
    if Target.os != Os.Windows {
        return ObjectWaitResult(err: core.SysErr.Unsupported, timed_out: false, fired_after_cancel: false)
    }
    var a := CallOf(core.IoOpObjectWait, core.IoRouteReady, 0, ObjWaitJob)
    a.os = h
    a.w.a = timeout_ns
    r := Run(io, mut a)
    if r.err == core.SysErr.Canceled {
        return ObjectWaitResult(err: core.SysErr.Canceled, timed_out: false, fired_after_cancel: a.w.b != 0)
    }
    if r.err != core.SysErr.None {
        return ObjectWaitResult(err: r.err, timed_out: false, fired_after_cancel: false)
    }
    return ObjectWaitResult(err: core.SysErr.None, timed_out: r.value != 0, fired_after_cancel: false)
}
