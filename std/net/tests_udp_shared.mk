// Why: a datagram can be shared across tasks and connected to a default peer.
// What: tests for the connected and shared datagram operations.
// How: uses independent loopback sockets and checks the concurrent send and receive paths.

import std.core
import std.raw

TuShareRecvTask(d: &SharedDatagram, io: Io, count: int, out_sum: &raw.Atomic[i64]) {
    var buf: [64]u8 = [0; 64]
    var sum: i64 = 0
    for _ in 0..count {
        (n, peer: _) := d.Recv(io, mut buf[..]) else {
            return
        }
        if n > 0 {
            sum += i64(buf[0])
        }
    }
    out_sum.Store(sum)
}

TuShareSendTask(d: &SharedDatagram, io: Io, to: Addr, count: int) {
    for i in 0..count {
        var b: [4]u8 = [Trunc[u8](i + 1), 0xAB, 0, 1]
        d.Send(io, b[..], some(to)) else {
            return
        }
    }
}

test "Datagram.LocalAddr, Connect, SetBuffers and connected SharedDatagram.Send(none)" (sys: &System) {
    io := sys.io
    a := TuBind(io, "127.0.0.1:0")
    onerr a.Close()
    la := a.LocalAddr()
    Assert(!la.v6 && int(la.port) == a.Port() && la.ip[0] == 127 && la.ip[3] == 1)

    b := TuBind(io, "127.0.0.1:0")
    defer b.Close()
    c := TuBind(io, "127.0.0.1:0")
    defer c.Close()

    try a.SetBuffers(32768, 32768)
    try a.SetBuffers(0, 16384)
    var bad_buf := false
    a.SetBuffers(-1, 1024) else err {
        match err {
            Os(code: _) { bad_buf = true }
            _ {}
        }
    }
    Assert(bad_buf)

    // Before Connect on u, SharedDatagram.Send(none) fails with InvalidAddress.
    u := TuBind(io, "127.0.0.1:0")
    onerr u.Close()
    su := u.Share()
    defer su.Close()
    var p0: [2]u8 = [7, 8]
    var uc := 0
    var none_addr: ?Addr = none
    su.Send(io, p0[..], none_addr) else e {
        uc = TuCode(e)
    }
    Assert(uc == 2)

    try a.Connect(io, b.LocalAddr())
    var decoy: [2]u8 = [99, 99]
    var reply: [2]u8 = [42, 43]
    try c.Send(io, decoy[..], a.LocalAddr())
    try b.Send(io, reply[..], a.LocalAddr())
    var rbuf: [16]u8 = [0; 16]
    (n: an, peer: apeer) := try a.Recv(io, mut rbuf[..])
    ba := b.LocalAddr()
    Assert(an == 2 && rbuf[0] == 42 && TuSame(&apeer, &ba))

    // Connect + Share: Send(none) reaches b.
    sa := a.Share()
    defer sa.Close()
    try sa.Send(io, p0[..], none_addr)
    (n: bn, peer: bpeer) := try b.Recv(io, mut rbuf[..])
    Assert(bn == 2 && rbuf[0] == 7 && rbuf[1] == 8 && TuSame(&bpeer, &la))
}

test "Datagram.Share: SharedDatagram TryRecv, SendBatch and concurrent Send + Recv in two tasks" (sys: &System) {
    io := sys.io
    srv := TuBind(io, "127.0.0.1:0")
    defer srv.Close()

    d := TuBind(io, "127.0.0.1:0")
    port := d.Port()
    la := d.LocalAddr()
    sd := d.Share()
    defer sd.Close()
    sla := sd.LocalAddr()
    Assert(sd.Port() == port && TuSame(&sla, &la))

    // TryRecv on empty queue returns none without blocking.
    var buf: [8]u8 = [0; 8]
    Assert((try sd.TryRecv(io, mut buf[..])).IsNone())

    // Queue empty (0-byte) datagram and oversized (truncated) datagram.
    var empty: [0]u8 = []
    try srv.Send(io, empty[..], la)
    var got0 := false
    for _ in 0..500 {
        if r := try sd.TryRecv(io, mut buf[..]) {
            (n: n0, peer: p0) := r
            sa := srv.LocalAddr()
            Assert(n0 == 0 && TuSame(&p0, &sa))
            got0 = true
            break
        }
        try io.Sleep(core.DurationOf(100_000))
    }
    Assert(got0)

    var big: [16]u8 = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]
    try srv.Send(io, big[..], la)
    var got_big := false
    for _ in 0..500 {
        if r := try sd.TryRecv(io, mut buf[..]) {
            (n: n1, peer: _) := r
            Assert(n1 == 8 && buf[0] == 1 && buf[7] == 8)
            got_big = true
            break
        }
        try io.Sleep(core.DurationOf(100_000))
    }
    Assert(got_big)

    // SendBatch splits into 3 datagrams of lengths 4, 4, 2.
    var batch: [10]u8 = [10, 11, 12, 13, 20, 21, 22, 23, 30, 31]
    try sd.SendBatch(io, batch[..], 4, some(srv.LocalAddr()))
    var sbuf: [16]u8 = [0; 16]
    (n: b0, peer: _) := try srv.Recv(io, mut sbuf[..])
    Assert(b0 == 4 && sbuf[0] == 10)
    (n: b1, peer: _) := try srv.Recv(io, mut sbuf[..])
    Assert(b1 == 4 && sbuf[0] == 20)
    (n: b2, peer: _) := try srv.Recv(io, mut sbuf[..])
    Assert(b2 == 2 && sbuf[0] == 30 && sbuf[1] == 31)

    // Concurrent Send and Recv on the same SharedDatagram in two tasks.
    count := 16
    var rx_sum := raw.Atomic[i64].New(0)
    scope sc := io.Scope() {
        sc.go TuShareRecvTask(&sd, io, count, &rx_sum)
        sc.go TuShareSendTask(&sd, io, srv.LocalAddr(), count)
        var ebuf: [16]u8 = [0; 16]
        for _ in 0..count {
            (n: en, peer: peer) := try srv.Recv(io, mut ebuf[..])
            try srv.Send(io, ebuf[0..en], peer)
        }
    }
    Assert(rx_sum.Load() == i64(count * (count + 1) / 2))
}
