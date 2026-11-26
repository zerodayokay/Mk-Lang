// Why: rt.ObjectWait must report Unsupported on non-native and non-Windows
// Io tables, Other on a null handle, and Canceled without a spurious
// fired-after-cancel when its scope was already cancelled.
//
// What: portable tests of ObjectWait over DirectIo and NativeIo.
//
// How: calls ObjectWait under DirectIo(), on handle 0, and inside a
// pre-cancelled scope. (Windows kernel-handle races live in
// std/rt-objwait_windows_test.go.)

import std.core

test "RT-ObjectWait DirectIo returns Unsupported" {
    r := ObjectWait(DirectIo(), 0, -1)
    Assert(r.err == core.SysErr.Unsupported && !r.timed_out && !r.fired_after_cancel)
}

test "RT-ObjectWait invalid handle and pre-cancelled scope" (sys: &System) {
    if Target.os != Os.Windows {
        r := ObjectWait(sys.io, 0, 0)
        Assert(r.err == core.SysErr.Unsupported && !r.timed_out && !r.fired_after_cancel)
        return
    }
    bad := ObjectWait(sys.io, 0, 0)
    Assert(bad.err != core.SysErr.None && bad.err != core.SysErr.Canceled && !bad.timed_out && !bad.fired_after_cancel)
    scope s := sys.io.Scope() {
        s.Cancel()
        c := ObjectWait(sys.io, 0, -1)
        Assert(c.err == core.SysErr.Canceled && !c.timed_out && !c.fired_after_cancel)
    }
}
