// Why: GUI work must stay on the process's initial macOS thread even when
// an independent OS thread submits a request.
// What: checks main-thread service dispatch, its system stack, and its timer.
// How: the service polls once on a directed wake from a detached thread,
// then ticks once more and asks to stop periodic polling.

import std.core
import std.raw
import std.rt
import std.sys
import std.time

var MainServiceCalls: raw.Atomic[u32] = raw.Atomic[u32].New(0)
var MainServiceOffStack: raw.Atomic[u32] = raw.Atomic[u32].New(0)
var MainServiceTestArm: raw.Atomic[u32] = raw.Atomic[u32].New(0)

TestMainService(): bool {
    var marker: int = 0
    if rt.SysStackOf(raw.Addr(raw.Of(&marker))) != 0 || !raw.IsNull(Current()) {
        MainServiceOffStack.Store(1)
    }
    _ = MainServiceCalls.Add(1)
    return MainServiceTestArm.Load() == 1
}

TestWakeMainService(arg: *u8) {
    _ = arg
    MainServiceTestArm.Store(1)
    WakeMainThreadService()
}

test "RT-MainThreadService dispatches on carrier zero and polls while active" (system: &System) {
    if Target.os != Os.Macos {
        return
    }
    Assert(MainThreadService(TestMainService))
    for _ in 0..200 {
        if MainServiceCalls.Load() > 0 {
            break
        }
        try system.io.Sleep(time.Millis(5))
    }
    Assert(MainServiceCalls.Load() == 1)
    started := sys.ThreadStart(TestWakeMainService, raw.Null[u8](), 1 << 20)
    Assert(started.err == core.SysErr.None)
    for _ in 0..200 {
        if MainServiceCalls.Load() >= 3 {
            break
        }
        try system.io.Sleep(time.Millis(5))
    }
    MainServiceTestArm.Store(0)
    Assert(MainServiceCalls.Load() >= 3)
    Assert(MainServiceOffStack.Load() == 0)
}
