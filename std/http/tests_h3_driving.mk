// Why: an HTTP/3 client needs a readiness barrier between starting Drive
// and dispatching its first request.
// What: readiness before and after Stop, and cancellation while waiting.
// How: start Drive in a scope and wait on the public client method.

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

test "an HTTP client leaves the H3 pool off the stack until it needs it" (sys: &System) {
    c := Client.New(sys.heap, DefaultClientLimits)
    defer c.Close()
    Assert(SizeOf[Client]() < 16384)
    Assert(c.H3State().IsNone())
}
