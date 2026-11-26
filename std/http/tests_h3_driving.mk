// Why: an HTTP/3 client needs a readiness barrier between starting Drive
// and dispatching its first request.
// What: readiness before and after Stop, and cancellation while waiting.
// How: start Drive in a scope and wait on the public client method.

import std.core
import std.time

TestWaitDrivingTask(c: &Client, io: Io) {
    c.Drive(io).Must()
}

test "WaitDriving observes a started Drive and returns false after Stop" (sys: &System) {
    c := Client.New(sys.heap, DefaultClientLimits)
    defer c.Close()
    io := sys.io
    scope s := io.Scope() {
        s.go TestWaitDrivingTask(&c, io)
        Assert(c.WaitDriving(io).Must())
        c.Stop()
    }
    Assert(!c.WaitDriving(io).Must())
}

test "WaitDriving is canceled when no Drive starts in its scope" (sys: &System) {
    c := Client.New(sys.heap, DefaultClientLimits)
    defer c.Close()
    io := sys.io
    scope s := io.Scope() {
        s.Cancel()
        match c.WaitDriving(io) {
            err(Canceled) {}
            _ { Assert(false) }
        }
    }
}

test "HTTP/3 Stop wakes every slot waiter even when it parks after the wake" (sys: &System) {
    c := Client.New(sys.heap, DefaultClientLimits)
    defer c.Close()
    state := try c.EnsureH3State()
    io := sys.io
    var before: u32 = 0
    scope driving := io.Scope() {
        driving.go TestWaitDrivingTask(&c, io)
        Assert(try c.WaitDriving(io))
        before = state.epoch.Load()
        c.Stop()
    }
    scope waiting := io.Scope() {
        waiting.CancelAfter(time.Seconds(1))
        Assert(!core.SyncWait(io, &state.epoch, before))
        Assert(!core.SyncWait(io, &state.epoch, before))
    }
}

test "an HTTP client leaves the H3 pool off the stack until it needs it" (sys: &System) {
    c := Client.New(sys.heap, DefaultClientLimits)
    defer c.Close()
    Assert(SizeOf[Client]() < 16384)
    Assert(c.H3State().IsNone())
}
