// Why: concurrent leader handoff and cancellation under arbitrary task
// interleavings must never strand a waiting party or lose the read token.
//
// What: PCT-scheduled cancellation sweep over FakeConn and Wait[D: Driven].
//
// How: across 64 seeds under std.sim (the PCT scheduler), spawns cancellable
// parties, surviving parties, a timer deadline, and a driver party while a
// peer sends frames at seeded delays; asserts every survivor finishes with
// its exact payload and Token ends empty and unheld.

import std.net
import std.sim
import std.time

PctCancelTask(fc: &FakeConn, io: Io, p: int, out: &Mutex[int]) {
    Wait(fc, io, p) else {
        var g := out.Lock(io.NoCancel()).Must()
        *g.Mut() = -1
        g.Unlock()
        return
    }
    var g := fc.st.Lock(io.NoCancel()).Must()
    v := g.Ref().val[p]
    g.Unlock()
    var og := out.Lock(io.NoCancel()).Must()
    *og.Mut() = v
    og.Unlock()
}

PctPeerSender(io: Io, srv: &net.Conn, seed: int) {
    // Send frames for parties 0..4 with seeded delays so cancellations hit
    // both queued waiters and an active leader.
    for i in 0..5 {
        if ((seed + i) % 2) == 0 {
            io.Sleep(time.Millis(Cast[i64](1 + ((seed + i) % 3)))) else {
                return
            }
        } else {
            io.Yield() else {
                return
            }
        }
        var msg: [2]u8 = [Trunc[u8](i), Trunc[u8](30 + i * 10 + (seed % 50))]
        srv.WriteAll(io, msg[..]) else {
            return
        }
    }
}

PctCutAt(s: &Scope, io: Io, delay_ms: i64) {
    if delay_ms > 0 {
        io.Sleep(time.Millis(delay_ms)) else {
            return
        }
    }
    s.Cancel()
}

RunPctSeed(h: &Heap, a: &Arena, seed: int): net.Error!void {
    var simw := try sim.Sim.New(a, Cast[u64](seed + 1))
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(a, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(h.Alloc(), cli)
    defer fc.Free(io, h.Alloc())

    // Arm a timer 2 ms ahead on half the seeds to exercise T5 under PCT.
    if (seed % 2) == 0 {
        var clk := try sim.Sim.New(a, 1)
        defer clk.Free()
        clk.Advance(time.Millis(2))
        dl := time.Now(clk.Io())
        var tg := fc.st.Lock(io).Must()
        tg.Mut().next_timer = some(dl)
        tg.Unlock()
    }

    var out0 := Mutex[int].New(-2)
    var out1 := Mutex[int].New(-2)
    var out2 := Mutex[int].New(-2)
    var out3 := Mutex[int].New(-2)
    var out4 := Mutex[int].New(-2)
    var out7 := Mutex[int].New(-2)

    scope all := io.Scope() {
        all.go PctPeerSender(io, &srv, seed)
        // Optional driver party (T6) on every 3rd seed.
        scope drv := io.Scope() {
            if (seed % 3) == 0 {
                drv.go PctCancelTask(&fc, io, 7, &out7)
            }
            scope surv := io.Scope() {
                surv.go PctCancelTask(&fc, io, 3, &out3)
                surv.go PctCancelTask(&fc, io, 4, &out4)
                scope cut := io.Scope() {
                    cut.go PctCancelTask(&fc, io, 0, &out0)
                    cut.go PctCancelTask(&fc, io, 1, &out1)
                    cut.go PctCancelTask(&fc, io, 2, &out2)
                    cut.go PctCutAt(&cut, io, Cast[i64](seed % 6))
                }
            }
            drv.Cancel()
        }
    }

    // Survivors 3 and 4 MUST have received their exact payloads.
    want3 := 30 + 3 * 10 + (seed % 50)
    want4 := 30 + 4 * 10 + (seed % 50)
    var g3 := out3.Lock(io).Must()
    Assert(*g3.Ref() == want3)
    g3.Unlock()
    var g4 := out4.Lock(io).Must()
    Assert(*g4.Ref() == want4)
    g4.Unlock()

    // Token must be completely free with zero waiters left behind.
    var fg := fc.st.Lock(io).Must()
    Assert(!fg.Ref().tok.Reading())
    Assert(fg.Ref().tok.Waiting() == 0)
    fg.Unlock()
}

RunOnePctSeed(h: &Heap, seed: int): net.Error!void {
    var ar := try Arena.New(h)
    defer ar.Free()
    try RunPctSeed(h, &ar, seed)
}

test "PCT cancel-safety across 64 seeds" (sys: &System) {
    for seed in 0..64 {
        try RunOnePctSeed(sys.heap, seed)
    }
}
