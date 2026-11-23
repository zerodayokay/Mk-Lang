// Why: a broadcast server hands frames from a handler to another task.
// What: ServeChan under a Sim and over the machine's loopback.
// How: the handler sends a sync.Shared frame from the server's heap into
// the channel, a task drains it; peers are SwHeadClient's.

import std.net
import std.raw
import std.sync

// BcHub is the served context: the value each frame carries.
struct BcHub {
    v: int
}

// BcPut is a chat server's Hub.Put: a frame of c's value from h, sent into
// ch; false when it could not be made or sent (then it is freed).
BcPut(c: &BcHub, h: &Heap into ch, ch: &Chan[sync.Shared[int]], io: Io): bool {
    m := sync.Shared[int].New(h, c.v) else {
        return false
    }
    ch.Send(io, m) else {
        m.Free(h)
        return false
    }
    return true
}

// BcHandle is a chat server's handler: one frame per request, 204 when it was
// sent, 503 when it was not.
BcHandle(c: &BcHub, h: &Heap into ch, ch: &Chan[sync.Shared[int]], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    if BcPut(c, h, ch, io) {
        res.Status(204)
    } else {
        res.Status(503)
    }
}

// BcServe runs ServeChan over l in a task of the caller: out becomes 1
// when it returned with no error (at the scope's cancel), 2 when it failed.
BcServe(io: Io, h: &Heap into ch, l: &net.Listener, o: &ServerOptions, c: &BcHub, ch: &Chan[sync.Shared[int]], out: &raw.Atomic[u32]) {
    ServeChan(h, io, l, o, c, ch, BcHandle) else {
        out.Store(2)
        return
    }
    out.Store(1)
}

// BcDrain receives frames from ch until it is closed and empty, adds
// their values into sum, and frees each with h.
BcDrain(io: Io, h: &Heap, ch: &Chan[sync.Shared[int]], sum: &raw.Atomic[u32]) {
    for {
        m := ch.Recv(io) else {
            return
        }
        _ = sum.Add(Cast[u32](*m.Ref()))
        m.Free(h)
    }
}

// BcBroadcast listens on port 0 and serves k clients in turn with
// ServeChan while a task drains the channel; then it cancels the server,
// closes the channel and frees it. True when every client got 204, the
// drain saw k frames of the hub's 3, and ServeChan returned cleanly at the
// cancel.
BcBroadcast(io: Io, h: &Heap): bool {
    k := 3
    l := net.Listen(io, "127.0.0.1:0") else {
        return false
    }
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())
    ch := Chan[sync.Shared[int]].New(h.Alloc(), 4) else {
        l.Close()
        return false
    }
    hub := BcHub(v: 3)
    o := DefaultServerOptions
    out := raw.Atomic[u32].New(0)
    sum := raw.Atomic[u32].New(0)
    var ok := 0
    scope drain := io.Scope() {
        drain.go BcDrain(io, h, &ch, &sum)
        scope top := io.Scope() {
            top.go BcServe(io, h, &l, &o, &hub, &ch, &out)
            for _ in 0..k {
                if SwHeadClient(io, addr, 10) == 204 {
                    ok += 1
                }
            }
            top.Cancel()
        }
        ch.Close()
    }
    l.Close()
    ch.FreeEach(h.Alloc(), fn(x: sync.Shared[int]) {
        x.Free(h)
    })
    // Edge: each scope's join orders its task's stores before these loads.
    return ok == k && sum.Load() == Cast[u32](3 * k) && out.Load() == 1
}

test "ServeChan's handler sends a frame from the server's heap into the channel it is handed, which another task drains" (sys: &System) {
    Assert(CtSim(sys, 12, BcBroadcast))
}

test "ServeChan serves the broadcast shape over the machine's loopback, with sys.io and sys.heap" (sys: &System) {
    Assert(BcBroadcast(sys.io, sys.heap))
}
