// Why: std.net.pipe provides the bounded in-memory Conn and Mux fakes that
// HTTP/2 and HTTP/3 tests rely on for backpressure, flow control, resets and
// multi-stream waits.
//
// What: test blocks for ConnPair (bounded writes, shutdown, cancel) and
// MuxPair (Mux.Of, Open/Accept, Credit, Wait, Reset/Stop, Release/Limits,
// Close/Abandon).
//
// How: runs tasks concurrently over ConnPair and MuxPair under the real
// runtime and std.sim; the test heap checks zero leaked allocations.

import std.net
import std.sim
import std.time

WriteAllTask(io: Io, c: &net.Conn, data: []u8, err_out: &Mutex[bool]) {
    c.WriteAll(io, data) else {
        var g := err_out.Lock(io.NoCancel()).Must()
        *g.Mut() = true
        g.Unlock()
        return
    }
}

test "Pipe ConnPair bounded write waits until reader consumes" (sys: &System) {
    (a, b) := try ConnPair(sys.heap, 8)
    defer a.Abandon()
    defer b.Abandon()

    var msg: [20]u8 = [0; 20]
    for i in 0..20 {
        msg[i] = Trunc[u8](i + 1)
    }
    var write_err := Mutex[bool].New(false)
    var got: [20]u8 = [0; 20]
    var total := 0

    scope s := sys.io.Scope() {
        s.go WriteAllTask(sys.io, &a, msg[..], &write_err)
        // Writer fills 8 bytes and parks until reader drains them.
        try sys.io.Sleep(time.Millis(5))
        for total < 20 {
            n := try b.Read(sys.io, mut got[total..])
            Assert(n > 0)
            total += n
        }
    }
    var g := write_err.Lock(sys.io).Must()
    Assert(!*g.Ref())
    g.Unlock()
    for i in 0..20 {
        Assert(got[i] == Trunc[u8](i + 1))
    }
}

ParkReadPipe(io: Io, c: &net.Conn, res: &Mutex[int]) {
    var buf: [16]u8 = [0; 16]
    n := c.Read(io, mut buf[..]) else {
        var g := res.Lock(io.NoCancel()).Must()
        *g.Mut() = -1
        g.Unlock()
        return
    }
    var g := res.Lock(io.NoCancel()).Must()
    *g.Mut() = n
    g.Unlock()
}

test "Pipe ConnPair Shutdown wakes parked Read and cancelled Read consumes nothing" (sys: &System) {
    (a, b) := try Pair(sys.heap)
    defer a.Abandon()
    defer b.Abandon()

    // Cancelled Read consumes nothing.
    var buf: [8]u8 = [0; 8]
    scope cut := sys.io.Scope() {
        cut.CancelAfter(time.Millis(2))
        var err_c := false
        _ = b.Read(sys.io, mut buf[..]) else {
            err_c = true
            0
        }
        Assert(err_c)
    }
    var p: [3]u8 = [10, 20, 30]
    try a.WriteAll(sys.io, p[..])
    Assert((try b.Read(sys.io, mut buf[..])) == 3)
    Assert(buf[0] == 10 && buf[2] == 30)

    // Shutdown(Read) wakes parked Read with 0 (EOF).
    var res := Mutex[int].New(-2)
    scope s := sys.io.Scope() {
        s.go ParkReadPipe(sys.io, &b, &res)
        try sys.io.Sleep(time.Millis(5))
        try b.Shutdown(sys.io, net.Shutdown.Read)
    }
    var g := res.Lock(sys.io).Must()
    Assert(*g.Ref() == 0)
    g.Unlock()
}

MuxWriteTask(io: Io, m: &net.Mux, s: &net.MuxStream, data: []u8, fin: bool) {
    m.Write(io, s, data, fin) else {
    }
}

test "Pipe MuxPair Open, Accept, Read, Write, Credit and Wait" (sys: &System) {
    (client: cli, server: srv) := try MuxPair(sys.heap, 8)
    defer cli.Abandon()
    defer srv.Abandon()

    Assert(cli.Usable(sys.io))
    Assert(srv.Usable(sys.io))

    // Client opens a bidi stream; server sees Incoming(uni: false) via Wait.
    cs := (try cli.Open(sys.io, false, true)) else {
        Assert(false)
        return
    }
    defer cli.Release(cs)
    Assert(cs.Id() == 0)

    var no_ids: [0]i64 = []
    ready := try srv.Wait(sys.io, no_ids[..], true)
    match ready {
        net.MuxReady.Incoming(uni) {
            Assert(!uni)
        }
        _ {
            Assert(false)
        }
    }
    ss := (try srv.Accept(sys.io, false, false)) else {
        Assert(false)
        return
    }
    defer srv.Release(ss)
    Assert(ss.Id() == 0)

    // Non-blocking Read returns none before data arrives.
    var rbuf: [16]u8 = [0; 16]
    if n0 := try srv.Read(sys.io, &ss, mut rbuf[..], false) {
        _ = n0
        Assert(false)
    }

    // Client writes 16 bytes (cap is 8); server reads 8, then calls Credit(16)
    // because peer-opened bidi streams only get credit via Credit.
    var msg: [16]u8 = [0; 16]
    for i in 0..16 {
        msg[i] = Trunc[u8](i + 5)
    }
    scope wsc := sys.io.Scope() {
        wsc.go MuxWriteTask(sys.io, &cli, &cs, msg[..], true)
        var ids: [1]i64 = [ss.Id()]
        wr := try srv.Wait(sys.io, ids[..], false)
        match wr {
            net.MuxReady.Readable(index) {
                Assert(index == 0)
            }
            _ {
                Assert(false)
            }
        }
        n1 := (try srv.Read(sys.io, &ss, mut rbuf[..], true)) else { -1 }
        Assert(n1 == 8)
        // Wait briefly to confirm client is blocked on credit, then grant Credit(16).
        try sys.io.Sleep(time.Millis(3))
        if n0 := try srv.Read(sys.io, &ss, mut rbuf[8..], false) {
            _ = n0
            Assert(false)
        }
        srv.Credit(&ss, 16)
        n2 := (try srv.Read(sys.io, &ss, mut rbuf[8..], true)) else { -1 }
        Assert(n2 == 8)
        n3 := (try srv.Read(sys.io, &ss, mut rbuf[..], true)) else { -1 }
        Assert(n3 == 0)
    }
    for i in 0..16 {
        Assert(rbuf[i] == Trunc[u8](i + 5))
    }
}

test "Pipe MuxPairWith stream limits, Release, Reset, Stop and Close" (sys: &System) {
    (client: cli, server: srv) := try MuxPairWith(sys.heap, 1, 1, 64)
    defer cli.Abandon()
    defer srv.Abandon()

    Assert(cli.Limits().bidi == 1)
    s0 := (try cli.Open(sys.io, false, false)) else {
        Assert(false)
        fail Closed
    }
    defer cli.Release(s0)
    Assert(cli.Limits().bidi == 0)
    // Second bidi open with wait=false returns none.
    if s_extra := try cli.Open(sys.io, false, false) {
        cli.Release(s_extra)
        Assert(false)
    }

    // Server accepts and releases s0 -> client regains 1 stream credit.
    as0 := (try srv.Accept(sys.io, false, true)) else {
        Assert(false)
        fail Closed
    }
    srv.Release(as0)
    Assert(cli.Limits().bidi == 1)
    s1 := (try cli.Open(sys.io, false, false)) else {
        Assert(false)
        fail Closed
    }
    defer cli.Release(s1)
    as1 := (try srv.Accept(sys.io, false, true)) else {
        Assert(false)
        fail Closed
    }
    defer srv.Release(as1)

    // Reset on s0 gives StreamReset(code: 42) on peer Read.
    cli.Reset(&s0, 42)
    // Stop on as1 gives StreamReset(code: 77) on peer Write.
    srv.Stop(&as1, 77)
    var werr: u64 = 0
    var d: [4]u8 = [1, 2, 3, 4]
    cli.Write(sys.io, &s1, d[..], false) else err {
        match err {
            net.StreamReset(code) { werr = code }
            _ {}
        }
    }
    Assert(werr == 77)
}

test "Pipe MuxPair under std.sim" (sys: &System) {
    var simw := try sim.Sim.New(sys.arena, 17)
    defer simw.Free()
    io := simw.Io()

    (client: cli, server: srv) := try MuxPair(sys.heap, 16)
    defer srv.Abandon()
    onerr cli.Abandon()

    u := (try cli.Open(io, true, false)) else {
        Assert(false)
        fail Closed
    }
    onerr cli.Release(u)
    su := (try srv.Accept(io, true, true)) else {
        Assert(false)
        fail Closed
    }
    onerr srv.Release(su)
    var payload: [4]u8 = [9, 8, 7, 6]
    try cli.Write(io, &u, payload[..], true)
    var buf: [8]u8 = [0; 8]
    n := (try srv.Read(io, &su, mut buf[..], true)) else {
        Assert(false)
        fail Closed
    }
    Assert(n == 4 && buf[0] == 9 && buf[3] == 6)
    cli.Release(u)
    srv.Release(su)
    try cli.Close(io, 0)
}
