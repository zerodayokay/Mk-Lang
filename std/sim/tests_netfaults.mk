// Why: simulated network faults, backpressure, blackholes, rebinds and
// DefaultNetFaults determinism must hold under std.sim.
// What: test blocks for DefaultNetFaults replay/determinism, NetFaults
// statistics (drop, duplicate, latency_lo/hi, rate), Sim.Blackhole,
// NetFaults.stream_buffer backpressure, and Sim.Rebind.
// How: run by mk test --std=std.sim and TestSTDSIM_TestBlocks.

import std.core
import std.net
import std.raw
import std.time

IoFmtLo(b: mut [32]u8, port: int): str from b {
    pre := "127.0.0.1:".AsBytes()
    (mut b[0..pre.Len()]).CopyFrom(pre)
    var d: [8]u8 = [0; 8]
    var n := 0
    var x := port
    for {
        d[n] = Trunc[u8](48 + x % 10)
        n += 1
        x /= 10
        if x == 0 {
            break
        }
    }
    for i in 0..n {
        b[pre.Len() + i] = d[n - 1 - i]
    }
    return str.From(b[0..pre.Len() + n]).Must()
}

NetWorkload(io: Io): net.Error!i64 {
    var acc: i64 = 0
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    var ab: [32]u8 = [0; 32]
    addr := IoFmtLo(mut ab, l.Port())
    c := try net.Dial(io, addr)
    defer c.Close()
    s := try l.Accept(io)
    defer s.Close()
    var msg: [12]u8 = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]
    try c.WriteAll(io, msg[..])
    try c.Shutdown(io, net.Shutdown.Write)
    var buf: [16]u8 = [0; 16]
    for {
        n := try s.Read(io, mut buf[..])
        if n == 0 {
            break
        }
        for i in 0..n {
            acc += i64(buf[i])
        }
    }

    srv := try net.Bind(io, "127.0.0.1:0")
    defer srv.Close()
    cli := try net.Bind(io, "127.0.0.1:0")
    onerr cli.Close()
    sa := net.ParseAddr(IoFmtLo(mut ab, srv.Port())).Must()
    try cli.Connect(io, sa)
    sd := cli.Share()
    defer sd.Close()
    var p: [4]u8 = [20, 21, 22, 23]
    var none_a: ?net.Addr = none
    try sd.Send(io, p[..], none_a)
    (n: dn, peer: _) := try srv.Recv(io, mut buf[..])
    for i in 0..dn {
        acc += i64(buf[i])
    }
    return acc
}

test "SIM-NetFaults DefaultNetFaults determinism and bit-for-bit tape replay" (sys: &System) {
    // Run the same workload twice with DefaultNetFaults on the same seed:
    // logs are byte-for-byte identical, and Replayer replays Done cleanly.
    var s1 := try Sim.New(sys.arena, 12345)
    defer s1.Free()
    s1.SetNetFaults(DefaultNetFaults)
    var rec1 := try Recorder.New(s1.Io())
    defer rec1.Free()
    v1 := try NetWorkload(rec1.Io())
    log1 := try rec1.Log(sys.arena)

    var s2 := try Sim.New(sys.arena, 12345)
    defer s2.Free()
    var rec2 := try Recorder.New(s2.Io())
    defer rec2.Free()
    v2 := try NetWorkload(rec2.Io())
    log2 := try rec2.Log(sys.arena)

    Assert(v1 == 164 && v2 == 164)
    Assert(log1.Len() == log2.Len())
    for i in 0..log1.Len() {
        Assert(log1[i] == log2[i])
    }

    var rp := try Replayer.New(log1)
    defer rp.Free()
    v3 := try NetWorkload(rp.Io())
    Assert(v3 == 164 && rp.Done() && !rp.Diverged())
}

DropTrial(a: &Arena, seed: u64): net.Error!bool {
    var s := try Sim.New(a, seed)
    defer s.Free()
    s.SetNetFaults(NetFaults(
        drop: 500_000,
        duplicate: 0,
        latency_lo: time.Millis(2),
        latency_hi: time.Millis(5),
        rate: 0,
        stream_buffer: 0,
        sealed: 0,
    ))
    io := s.Io()
    srv := try net.Bind(io, "127.0.0.1:0")
    onerr srv.Close()
    ssrv := srv.Share()
    defer ssrv.Close()
    cli := try net.Bind(io, "127.0.0.1:0")
    defer cli.Close()
    var ab: [32]u8 = [0; 32]
    sa := net.ParseAddr(IoFmtLo(mut ab, ssrv.Port())).Must()
    var pkt: [4]u8 = [1, 2, 3, 4]
    t0 := time.Now(io)
    try cli.Send(io, pkt[..], sa)
    s.Run()
    var rbuf: [8]u8 = [0; 8]
    if got := try ssrv.TryRecv(io, mut rbuf[..]) {
        (n: n, peer: _) := got
        Assert(n == 4)
        dt := time.Now(io).Since(t0).Nanos()
        Assert(dt >= 2_000_000 && dt <= 5_000_000)
        return true
    }
    return false
}

DupCheck(a: &Arena): net.Error!void {
    var sd := try Sim.New(a, 99)
    defer sd.Free()
    sd.SetNetFaults(NetFaults(
        drop: 0,
        duplicate: 500_000,
        latency_lo: time.Micros(100),
        latency_hi: time.Micros(200),
        rate: 0,
        stream_buffer: 0,
        sealed: 0,
    ))
    dio := sd.Io()
    dsrv := try net.Bind(dio, "127.0.0.1:0")
    onerr dsrv.Close()
    dss := dsrv.Share()
    defer dss.Close()
    dcli := try net.Bind(dio, "127.0.0.1:0")
    defer dcli.Close()
    var dab: [32]u8 = [0; 32]
    dsa := net.ParseAddr(IoFmtLo(mut dab, dss.Port())).Must()
    var dpkt: [2]u8 = [9, 8]
    for _ in 0..30 {
        try dcli.Send(dio, dpkt[..], dsa)
    }
    sd.Run()
    var dup_count := 0
    var drbuf: [8]u8 = [0; 8]
    for {
        if got := try dss.TryRecv(dio, mut drbuf[..]) {
            _ = got
            dup_count += 1
        } else {
            break
        }
    }
    Assert(dup_count > 30)
}

RateCheck(a: &Arena): net.Error!void {
    var sr := try Sim.New(a, 7)
    defer sr.Free()
    sr.SetNetFaults(NetFaults(
        drop: 0,
        duplicate: 0,
        latency_lo: core.DurationOf(1000),
        latency_hi: core.DurationOf(1000),
        rate: 1000,
        stream_buffer: 0,
        sealed: 0,
    ))
    rio := sr.Io()
    rsrv := try net.Bind(rio, "127.0.0.1:0")
    defer rsrv.Close()
    rcli := try net.Bind(rio, "127.0.0.1:0")
    defer rcli.Close()
    var rab: [32]u8 = [0; 32]
    rsa := net.ParseAddr(IoFmtLo(mut rab, rsrv.Port())).Must()
    var big: [512]u8 = [7; 512]
    rt0 := time.Now(rio)
    try rcli.Send(rio, big[..], rsa)
    var rbig: [512]u8 = [0; 512]
    (n: rn, peer: _) := try rsrv.Recv(rio, mut rbig[..])
    Assert(rn == 512)
    Assert(time.Now(rio).Since(rt0).Nanos() >= 500_000_000)
}

test "SIM-NetFaults drop, duplicate, latency bounds and rate shaping over seeds" (sys: &System) {
    var arrived := 0
    for seed in 1..41 {
        if try DropTrial(sys.arena, Cast[u64](seed)) {
            arrived += 1
        }
    }
    Assert(arrived > 5 && arrived < 35)
    try DupCheck(sys.arena)
    try RateCheck(sys.arena)
}

test "SIM-NetFaults Blackhole drops while enabled and delivers once cleared" (sys: &System) {
    var s := try Sim.New(sys.arena, 55)
    defer s.Free()
    io := s.Io()
    srv := try net.Bind(io, "127.0.0.1:0")
    onerr srv.Close()
    ss := srv.Share()
    defer ss.Close()
    cli := try net.Bind(io, "127.0.0.1:0")
    defer cli.Close()
    var ab: [32]u8 = [0; 32]
    sa := net.ParseAddr(IoFmtLo(mut ab, ss.Port())).Must()

    s.Blackhole(ss.Port(), true)
    var pkt: [3]u8 = [1, 2, 3]
    for _ in 0..5 {
        try cli.Send(io, pkt[..], sa)
    }
    s.Run()
    var rbuf: [8]u8 = [0; 8]
    Assert((try ss.TryRecv(io, mut rbuf[..])).IsNone())

    s.Blackhole(ss.Port(), false)
    var good: [3]u8 = [4, 5, 6]
    try cli.Send(io, good[..], sa)
    s.Run()
    got := try ss.TryRecv(io, mut rbuf[..])
    Assert(got.IsSome())
    (n: n, peer: _) := got.Must()
    Assert(n == 3 && rbuf[0] == 4 && rbuf[2] == 6)
}

StreamBufferWriter(c: &net.Conn, io: Io, done: &raw.Atomic[u32]) {
    var payload: [256]u8 = [0; 256]
    for i in 0..256 {
        payload[i] = Trunc[u8](i)
    }
    c.WriteAll(io, payload[..]) else {
        return
    }
    done.Store(1)
}

test "SIM-NetFaults stream_buffer backpressure blocks writer until reader drains" (sys: &System) {
    var s := try Sim.New(sys.arena, 33)
    defer s.Free()
    s.SetNetFaults(NetFaults(
        drop: 0,
        duplicate: 0,
        latency_lo: core.DurationOf(1000),
        latency_hi: core.DurationOf(1000),
        rate: 0,
        stream_buffer: 64,
        sealed: 0,
    ))
    io := s.Io()
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    var ab: [32]u8 = [0; 32]
    c := try net.Dial(io, IoFmtLo(mut ab, l.Port()))
    conn := c.Conn()
    defer conn.Abandon()
    srv := try l.Accept(io)
    defer srv.Close()

    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.go StreamBufferWriter(&conn, io, &done)
        try io.Sleep(time.Millis(10))
        // Writer cannot complete 256 bytes into a 64-byte stream buffer yet.
        Assert(done.Load() == 0)

        var rbuf: [64]u8 = [0; 64]
        var total := 0
        for total < 256 {
            n := try srv.Read(io, mut rbuf[..])
            if n == 0 {
                break
            }
            for i in 0..n {
                Assert(rbuf[i] == Trunc[u8](total + i))
            }
            total += n
        }
        Assert(total == 256)
    }
    Assert(done.Load() == 1)
}

test "SIM-NetFaults Rebind moves UDP port and delivers replies to new port" (sys: &System) {
    var s := try Sim.New(sys.arena, 77)
    defer s.Free()
    io := s.Io()
    a := try net.Bind(io, "127.0.0.1:0")
    defer a.Close()
    b := try net.Bind(io, "127.0.0.1:0")
    defer b.Close()

    old_port := a.Port()
    var ab: [32]u8 = [0; 32]
    ba := net.ParseAddr(IoFmtLo(mut ab, b.Port())).Must()

    s.Rebind(old_port)
    var msg: [4]u8 = [31, 32, 33, 34]
    try a.Send(io, msg[..], ba)
    var buf: [16]u8 = [0; 16]
    (n: n1, peer: new_peer) := try b.Recv(io, mut buf[..])
    Assert(n1 == 4 && buf[0] == 31 && int(new_peer.port) != old_port)

    var rep: [2]u8 = [77, 88]
    try b.Send(io, rep[..], new_peer)
    (n: n2, peer: rpeer) := try a.Recv(io, mut buf[..])
    Assert(n2 == 2 && buf[0] == 77 && buf[1] == 88 && rpeer.port == ba.port)
}
