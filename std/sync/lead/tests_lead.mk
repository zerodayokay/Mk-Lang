// Why: Token and Wait[D: Driven] implement the read-token protocol once for
// multiplexed connections, so every invariant (FIFO, O(1) leave)
// must be pinned directly and under std.sim's PCT scheduler.
//
// What: unit tests for Token and protocol tests over a toy Driven
// fake connection running on std.sim.
//
// How: a FakeConn wraps a Token inside a Mutex[FakeState], per-party Notify
// slots, and a simulated net.Conn under std.sim; tests drive concurrent tasks
// with deadlines and cancellations and check every delivery and token handoff.

import std.net
import std.sim
import std.sync
import std.time

test "Token New, Enter, Leave FIFO, Reading, Waiting, Grow and Free" (sys: &System) {
    a := sys.heap.Alloc()
    var tok := try Token.New(a, 3)
    defer tok.Free(a)
    Assert(!tok.Reading())
    Assert(tok.Waiting() == 0)

    // Party 0 acquires free token.
    Assert(tok.Enter(0))
    Assert(tok.Reading())
    Assert(tok.Waiting() == 0)

    // Parties 1 and 2 queue in FIFO order.
    Assert(!tok.Enter(1))
    Assert(!tok.Enter(2))
    Assert(tok.Reading())
    Assert(tok.Waiting() == 2)

    // Re-entering while already queued preserves position.
    Assert(!tok.Enter(1))
    Assert(tok.Waiting() == 2)

    // Leader 0 leaves -> wakes party 1 (oldest queued).
    Assert(tok.Leave(0) == 1)
    Assert(!tok.Reading())
    Assert(tok.Waiting() == 1)

    // Grow adds party 3 without disturbing party 2.
    try tok.Grow(a, 5)
    Assert(tok.Enter(1))
    Assert(tok.Reading())
    Assert(!tok.Enter(3))
    Assert(!tok.Enter(4))
    Assert(tok.Waiting() == 3)

    // Cancel queued party 3 out of the middle.
    Assert(tok.Leave(3) == -1)
    Assert(tok.Waiting() == 2)

    // Leader 1 leaves -> wakes party 2, then 2 takes and leaves -> wakes 4.
    Assert(tok.Leave(1) == 2)
    Assert(tok.Enter(2))
    Assert(tok.Leave(2) == 4)
    Assert(tok.Enter(4))
    Assert(tok.Leave(4) == -1)
    Assert(!tok.Reading())
    Assert(tok.Waiting() == 0)
}

test "Token Leave while Idle passes free token to waiting party" (sys: &System) {
    a := sys.heap.Alloc()
    var tok := try Token.New(a, 3)
    defer tok.Free(a)

    Assert(tok.Enter(0))
    Assert(!tok.Enter(1))
    Assert(!tok.Enter(2))
    // 0 releases and pops 1; token is free while 2 still waits.
    Assert(tok.Leave(0) == 1)
    Assert(!tok.Reading())
    Assert(tok.Waiting() == 1)

    // Suppose 1 got its condition satisfied before calling Enter and exits via Leave(1):
    // rule T2 requires passing the free token on to 2.
    Assert(tok.Leave(1) == 2)
    Assert(tok.Waiting() == 0)
    Assert(tok.Leave(0) == -1)
}

test "Token Enter when token became free while party still queued" (sys: &System) {
    a := sys.heap.Alloc()
    var tok := try Token.New(a, 3)
    defer tok.Free(a)

    Assert(tok.Enter(0))
    Assert(!tok.Enter(1))
    Assert(!tok.Enter(2))
    // 0 leaves, waking 1; token is free and 2 is still queued.
    Assert(tok.Leave(0) == 1)
    Assert(!tok.Reading())
    Assert(tok.Waiting() == 1)
    // 2's timer expired and re-runs Begin -> Enter(2) before 1 enters.
    Assert(tok.Enter(2))
    Assert(tok.Reading())
    Assert(tok.Waiting() == 0)
    Assert(tok.Leave(2) == -1)
    Assert(!tok.Reading())
}

const MaxParties: int = 8

struct FakeState {
    tok: Token
    cond: [8]bool
    gen_: [8]u32
    val: [8]int
    fault: bool
    next_timer: ?time.Instant
    timer_runs: int
    reads: int
    bytes_read: int
    spurious: bool
    rd_buf: [64]u8
    rd_len: int
}

struct FakeConn {
    conn: net.Conn
    st: Mutex[FakeState]
    parties: [8]sync.Notify
}

FakeConn.New(a: &Alloc, conn: net.Conn): OutOfMemory!FakeConn from a, conn {
    tok := try Token.New(a, MaxParties)
    st := FakeState(
        tok: tok,
        cond: [false; 8],
        gen_: [0; 8],
        val: [0; 8],
        fault: false,
        next_timer: none,
        timer_runs: 0,
        reads: 0,
        bytes_read: 0,
        spurious: false,
        rd_buf: [0; 64],
        rd_len: 0,
    )
    return FakeConn(
        conn: conn,
        st: Mutex[FakeState].New(st),
        parties: [
            sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
            sync.Notify.New(), sync.Notify.New(), sync.Notify.New(), sync.Notify.New(),
        ],
    )
}

FakeConn.Free(self, io: Io, a: &Alloc) {
    FakeConn(conn, st, parties: _) := self
    _ = io
    conn.Abandon()
    FakeState(tok, ..) := st.Into()
    tok.Free(a)
}

impl Driven for FakeConn {
    Begin(&self, nc: Io, p: int): Step {
        var g := self.st.Lock(nc).Must()
        if g.Ref().fault || g.Ref().cond[p] {
            g.Unlock()
            return Step.Done
        }
        is_lead := g.Mut().tok.Enter(p)
        dl := g.Ref().next_timer
        g.Unlock()
        if is_lead {
            return Step.Lead(deadline: dl)
        }
        return Step.Wait(deadline: dl)
    }

    Lead(&self, io: Io, nc: Io, deadline: ?time.Instant): Canceled!void {
        var buf: [32]u8 = [0; 32]
        var got := 0
        var eof := false
        var cancel_err := false
        scope rs := io.Scope() {
            match deadline {
                some(t) {
                    rs.CancelAfter(t.Since(time.Now(io)))
                }
                none {
                }
            }
            // One socket read per Lead (rule T3: Canceled consumes nothing).
            var timed_out := false
            n := self.conn.Read(io, mut buf[..]) else err {
                if rs.Expired() {
                    timed_out = true
                } else {
                    match err {
                        Canceled {
                            cancel_err = true
                        }
                        _ {
                            eof = true
                        }
                    }
                }
                0
            }
            if !cancel_err && !eof && !timed_out && n == 0 {
                eof = true
            }
            got = n
        }
        if cancel_err {
            fail Canceled
        }
        // Dispatch under state lock with nc (L1, L2), after rs has closed.
        var to_notify: [8]bool = [false; 8]
        var g := self.st.Lock(nc).Must()
        g.Mut().reads += 1
        g.Mut().bytes_read += got
        if got > 0 {
            cur := g.Ref().rd_len
            for k in 0..got {
                g.Mut().rd_buf[cur + k] = buf[k]
            }
            total := cur + got
            var off := 0
            for off + 2 <= total {
                target := int(g.Ref().rd_buf[off])
                v := int(g.Ref().rd_buf[off + 1])
                if target >= 0 && target < MaxParties {
                    g.Mut().val[target] = v
                    g.Mut().cond[target] = true
                    g.Mut().gen_[target] += 1
                    to_notify[target] = true
                }
                off += 2
            }
            rem := total - off
            for k in 0..rem {
                g.Mut().rd_buf[k] = g.Ref().rd_buf[off + k]
            }
            g.Mut().rd_len = rem
        }
        if eof {
            g.Mut().fault = true
            for i in 0..MaxParties {
                to_notify[i] = true
            }
        }
        match g.Ref().next_timer {
            some(t) {
                if t.Since(time.Now(nc)).Nanos() <= 0 {
                    g.Mut().timer_runs += 1
                    g.Mut().next_timer = none
                }
            }
            none {
            }
        }
        g.Unlock()
        for i in 0..MaxParties {
            if to_notify[i] {
                self.parties[i].NotifyOne()
            }
        }
    }

    Timers(&self, nc: Io) {
        var g := self.st.Lock(nc).Must()
        match g.Ref().next_timer {
            some(t) {
                if t.Since(time.Now(nc)).Nanos() <= 0 {
                    g.Mut().timer_runs += 1
                    g.Mut().next_timer = none
                }
            }
            none {
            }
        }
        g.Unlock()
    }

    End(&self, nc: Io, p: int): int {
        var g := self.st.Lock(nc).Must()
        wake := g.Mut().tok.Leave(p)
        g.Unlock()
        return wake
    }

    Party(&self, p: int): &sync.Notify {
        return &self.parties[p]
    }
}

WaitTask(fc: &FakeConn, io: Io, p: int, out: &Mutex[int]) {
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

SimConnPair(a: &Arena, io: Io): net.Error!(cli: net.Conn, srv: net.Conn) {
    var ln := try net.Listen(io, "127.0.0.1:0")
    defer ln.Close()
    addr := try a.Fmt("127.0.0.1:{p}", (p: ln.Port()))
    cli_st := try net.Dial(io, addr)
    onerr cli_st.Close()
    srv_st := try ln.Accept(io)
    return (cli: cli_st.Conn(), srv: srv_st.Conn())
}

test "T1 no lost wake-up: delivery before Wait keeps permit and completes" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 101)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    // Deliver notification for party 1 before Wait starts: Notify stores 1 permit.
    var g := fc.st.Lock(io).Must()
    g.Mut().cond[1] = true
    g.Mut().val[1] = 42
    g.Unlock()
    fc.Party(1).NotifyOne()

    try Wait(&fc, io, 1)
    var g2 := fc.st.Lock(io).Must()
    Assert(g2.Ref().val[1] == 42)
    Assert(g2.Ref().reads == 0)
    g2.Unlock()
}

test "T2 and L1 no lost token: cancelled leader and cancelled waiter pass token" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 202)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    var out0 := Mutex[int].New(0)
    var out1 := Mutex[int].New(0)
    var out2 := Mutex[int].New(0)

    scope all := io.Scope() {
        // Party 0 (leader) and party 1 (waiter) share a cancellable inner scope;
        // party 2 waits in the outer scope.
        scope cut := io.Scope() {
            cut.go WaitTask(&fc, io, 0, &out0)
            cut.go WaitTask(&fc, io, 1, &out1)
            all.go WaitTask(&fc, io, 2, &out2)
            // Let 0 enter Lead and 1, 2 queue behind 0.
            try io.Sleep(time.Millis(5))
            cut.Cancel()
        }
        // Now 0 and 1 exited with Canceled; 2 must have received the token and read.
        try io.Sleep(time.Millis(5))
        var msg: [2]u8 = [2, 77]
        try srv.WriteAll(io, msg[..])
    }
    var g0 := out0.Lock(io).Must()
    Assert(*g0.Ref() == -1)
    g0.Unlock()
    var g1 := out1.Lock(io).Must()
    Assert(*g1.Ref() == -1)
    g1.Unlock()
    var g2 := out2.Lock(io).Must()
    Assert(*g2.Ref() == 77)
    g2.Unlock()
}

test "T3 cancelled leader loses nothing: unconsumed read completes for successor" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 303)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    var out0 := Mutex[int].New(0)
    scope cut := io.Scope() {
        cut.go WaitTask(&fc, io, 0, &out0)
        try io.Sleep(time.Millis(2))
        cut.Cancel()
    }
    // Leader 0 cancelled before any bytes arrived; nothing lost from stream.
    var g0 := out0.Lock(io).Must()
    Assert(*g0.Ref() == -1)
    g0.Unlock()

    var msg: [2]u8 = [3, 99]
    try srv.WriteAll(io, msg[..])
    try Wait(&fc, io, 3)
    var g3 := fc.st.Lock(io).Must()
    Assert(g3.Ref().val[3] == 99)
    g3.Unlock()
}

test "T4 spurious wakes are harmless: stale permit re-checks Begin" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 404)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    // Leave a stale permit on party 4's Notify before condition is set.
    fc.Party(4).NotifyOne()

    var out4 := Mutex[int].New(0)
    scope s := io.Scope() {
        s.go WaitTask(&fc, io, 4, &out4)
        try io.Sleep(time.Millis(5))
        // Spurious wake did not falsely complete party 4; now send real message.
        var g := out4.Lock(io).Must()
        Assert(*g.Ref() == 0)
        g.Unlock()
        var msg: [2]u8 = [4, 55]
        try srv.WriteAll(io, msg[..])
    }
    var g4 := out4.Lock(io).Must()
    Assert(*g4.Ref() == 55)
    g4.Unlock()
}

test "T5 timers need no token: waiting party runs Timers on deadline without holding token" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 505)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    // Arm a timer 3 ms in the future (relative to simw's current clock);
    // party 0 leads (with a long read), party 1 waits with deadline.
    var clk := try sim.Sim.New(sys.arena, 1)
    defer clk.Free()
    clk.Advance(time.Now(io).Since(time.Now(clk.Io())))
    clk.Advance(time.Millis(3))
    dl := time.Now(clk.Io())
    var g := fc.st.Lock(io).Must()
    g.Mut().next_timer = some(dl)
    g.Unlock()

    var out0 := Mutex[int].New(0)
    var out1 := Mutex[int].New(0)
    scope s := io.Scope() {
        s.go WaitTask(&fc, io, 0, &out0)
        try io.Sleep(time.Millis(1))
        s.go WaitTask(&fc, io, 1, &out1)
        // Advance past the 3 ms timer while 0 is blocked in Lead; 1's Wait deadline runs Timers.
        try io.Sleep(time.Millis(5))
        var tg := fc.st.Lock(io).Must()
        Assert(tg.Ref().timer_runs >= 1)
        tg.Unlock()
        var m0: [2]u8 = [0, 11]
        var m1: [2]u8 = [1, 22]
        try srv.WriteAll(io, m0[..])
        try srv.WriteAll(io, m1[..])
    }
    var g0 := out0.Lock(io).Must()
    Assert(*g0.Ref() == 11)
    g0.Unlock()
    var g1 := out1.Lock(io).Must()
    Assert(*g1.Ref() == 22)
    g1.Unlock()
}

test "T6 party with no stream: driver party drives reads until scope cancelled" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 606)
    defer simw.Free()
    io := simw.Io()
    (cli, srv) := try SimConnPair(sys.arena, io)
    defer srv.Abandon()
    onerr cli.Abandon()
    var fc := try FakeConn.New(sys.heap.Alloc(), cli)
    defer fc.Free(io, sys.heap.Alloc())

    // Party 7 is the driver (its cond[7] is never set to true): it drives reads for party 5.
    var out7 := Mutex[int].New(0)
    scope drv := io.Scope() {
        drv.go WaitTask(&fc, io, 7, &out7)
        try io.Sleep(time.Millis(2))
        var msg: [2]u8 = [5, 88]
        try srv.WriteAll(io, msg[..])
        try Wait(&fc, io, 5)
        drv.Cancel()
    }
    var g7 := out7.Lock(io).Must()
    Assert(*g7.Ref() == -1)
    g7.Unlock()
    var g5 := fc.st.Lock(io).Must()
    Assert(g5.Ref().val[5] == 88)
    g5.Unlock()
}
