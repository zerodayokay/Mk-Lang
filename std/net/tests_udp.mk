// Why: UDP, address parsing and name resolution have many edge cases (zones,
// mapped addresses, cut datagrams, cancelled lookups) that must match Go.
// What: test blocks for Bind, Datagram's Send, Recv, Port and Close, Addr,
// ParseAddr, Format and Resolve, including cancellation and leak cycles.
// How: run by std/std-net_sockblocks_test.go through mk test (dev and release,
// 1 and 8 carriers, both targets) and by std/std-io_blocks_test.go's harness.
// One block asks a DNS server for names under .invalid and cancels each
// lookup while it waits (GetAddrInfoExCancel on Windows, the stub resolver's
// exchange on Linux), so a cancel that did not end a lookup shows as a slow
// block.

import std.core
import std.raw
import std.sys

// TuCode is 1 Canceled, 2 InvalidAddress, 3 AddrInUse, 4 Os, 5 other.
TuCode(e: Error): int {
    return match e {
        Canceled { 1 }
        InvalidAddress { 2 }
        AddrInUse { 3 }
        Os(code: _) { 4 }
        _ { 5 }
    }
}

// TuBind binds addr or panics.
TuBind(io: Io, addr: str): Datagram {
    d := Bind(io, addr) else {
        Panic("net.Bind failed")
    }
    return d
}

// TuLo is the loopback Addr of the family, port p.
TuLo(v6: bool, p: int): Addr {
    var ip: [16]u8 = [0; 16]
    if v6 {
        ip[15] = 1
    } else {
        ip[0] = 127
        ip[3] = 1
    }
    return Addr(ip: ip, port: Trunc[u16](p), v6: v6)
}

// TuSame: equal addresses.
TuSame(a: &Addr, b: &Addr): bool {
    if a.port != b.port || a.v6 != b.v6 {
        return false
    }
    for k in 0..16 {
        if a.ip[k] != b.ip[k] {
            return false
        }
    }
    return true
}

test "Udp echo on IPv4 and IPv6 loopback: Port, peer, a cut datagram, an empty one, an empty buffer" (sys: &System) {
    io := sys.io
    for k in 0..2 {
        v6 := k == 1
        var spec := "127.0.0.1:0"
        if v6 {
            spec = "[::1]:0"
        }
        a := TuBind(io, spec)
        b := TuBind(io, spec)
        pa := a.Port()
        pb := b.Port()
        Assert(pa > 0 && pa < 65536 && pb > 0 && pb < 65536 && pa != pb)
        var msg: [5]u8 = [104, 101, 108, 108, 111]
        a.Send(io, msg[..], TuLo(v6, pb)) else {
            Assert(false)
        }
        var buf: [64]u8 = [0; 64]
        (n: n, peer: peer) := b.Recv(io, mut buf[..]) else {
            Panic("Recv failed")
        }
        Assert(n == 5 && buf[0] == 104 && buf[4] == 111)
        want := TuLo(v6, pa)
        Assert(TuSame(&peer, &want))
        // The reply goes to the peer Recv reported.
        b.Send(io, buf[0..n], peer) else {
            Assert(false)
        }
        (n: n2, peer: p2) := a.Recv(io, mut buf[..]) else {
            Panic("Recv failed")
        }
        wb := TuLo(v6, pb)
        Assert(n2 == 5 && TuSame(&p2, &wb))
        // A datagram longer than buf is cut to it; the rest is lost, so the
        // next Recv gets the next datagram.
        var big: [1000]u8 = [7; 1000]
        big[9] = 9
        a.Send(io, big[..], TuLo(v6, pb)) else {
            Assert(false)
        }
        (n: n3, peer: _) := b.Recv(io, mut buf[0..10]) else {
            Panic("Recv failed")
        }
        Assert(n3 == 10 && buf[9] == 9)
        // An empty datagram is a datagram of 0 bytes.
        a.Send(io, big[0..0], TuLo(v6, pb)) else {
            Assert(false)
        }
        (n: n4, peer: p4) := b.Recv(io, mut buf[..]) else {
            Panic("Recv failed")
        }
        Assert(n4 == 0 && TuSame(&p4, &want))
        // An empty buf discards one datagram and reports its peer.
        a.Send(io, msg[..], TuLo(v6, pb)) else {
            Assert(false)
        }
        a.Send(io, msg[0..2], TuLo(v6, pb)) else {
            Assert(false)
        }
        (n: n5, peer: p5) := b.Recv(io, mut buf[0..0]) else {
            Panic("Recv failed")
        }
        Assert(n5 == 0 && TuSame(&p5, &want))
        (n: n6, peer: _) := b.Recv(io, mut buf[..]) else {
            Panic("Recv failed")
        }
        Assert(n6 == 2)
        a.Close()
        b.Close()
    }
}

test "Udp: the largest IPv4 datagram, 65,507 bytes, goes whole (Windows); a larger one is Os and the socket still works" (sys: &System) {
    io := sys.io
    a := TuBind(io, "127.0.0.1:0")
    b := TuBind(io, "127.0.0.1:0")
    big := sys.arena.Slice[u8](70_000) else {
        Panic("arena")
    }
    for i in 0..big.Len() {
        big[i] = Trunc[u8](i % 251)
    }
    back := sys.arena.Slice[u8](70_000) else {
        Panic("arena")
    }
    // The round trip of the largest datagram runs on Windows only: WSL 2's
    // loopback drops every IPv4 datagram over 1,472 bytes (measured with
    // Python's socket module in WSL), so a Recv there
    // would wait forever. The refusal of 65,508 bytes below is local and
    // runs on both.
    if Target.os == core.Os.Windows {
        a.Send(io, big[0..65_507], TuLo(false, b.Port())) else {
            Assert(false)
        }
        (n: n, peer: _) := b.Recv(io, mut back[..]) else {
            Panic("Recv failed")
        }
        Assert(n == 65_507 && back[0] == 0 && back[65_506] == Trunc[u8](65_506 % 251))
    }
    var got := 0
    a.Send(io, big[0..65_508], TuLo(false, b.Port())) else e {
        got = TuCode(e)
    }
    Assert(got == 4)
    var msg: [3]u8 = [1, 2, 3]
    a.Send(io, msg[..], TuLo(false, b.Port())) else {
        Assert(false)
    }
    (n: n2, peer: _) := b.Recv(io, mut back[..]) else {
        Panic("Recv failed")
    }
    Assert(n2 == 3 && back[2] == 3)
    a.Close()
    b.Close()
}

test "Udp: a [::] socket serves an IPv4 and an IPv6 peer; the IPv4 peer is unmapped and answered" (sys: &System) {
    io := sys.io
    srv := TuBind(io, "[::]:0")
    c4 := TuBind(io, "127.0.0.1:0")
    c6 := TuBind(io, "[::1]:0")
    ps := srv.Port()
    var buf: [16]u8 = [0; 16]
    var msg: [2]u8 = [4, 6]
    c4.Send(io, msg[0..1], TuLo(false, ps)) else {
        Assert(false)
    }
    (n: n, peer: p) := srv.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    w4 := TuLo(false, c4.Port())
    Assert(n == 1 && buf[0] == 4 && TuSame(&p, &w4))
    srv.Send(io, buf[0..1], p) else {
        Assert(false)
    }
    (n: n2, peer: q) := c4.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    wq := TuLo(false, ps)
    Assert(n2 == 1 && TuSame(&q, &wq))
    c6.Send(io, msg[1..2], TuLo(true, ps)) else {
        Assert(false)
    }
    (n: n3, peer: p6) := srv.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    w6 := TuLo(true, c6.Port())
    Assert(n3 == 1 && buf[0] == 6 && TuSame(&p6, &w6))
    // An IPv4-mapped destination from an IPv4 socket goes as IPv4.
    m := ParseAddr(sys.arena.Fmt("[::ffff:127.0.0.1]:{ps}", (ps)).Must()) else {
        Panic("ParseAddr failed")
    }
    c4.Send(io, msg[0..1], m) else {
        Assert(false)
    }
    (n: n4, peer: p4) := srv.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    Assert(n4 == 1 && TuSame(&p4, &w4))
    // Any other IPv6 destination from an IPv4 socket is InvalidAddress.
    var got := 0
    c4.Send(io, msg[..], TuLo(true, ps)) else e {
        got = TuCode(e)
    }
    Assert(got == 2)
    srv.Close()
    c4.Close()
    c6.Close()
}

test "Udp Bind takes the address forms: :port, localhost, a zone, AddrInUse and malformed input" (sys: &System) {
    io := sys.io
    a := TuBind(io, ":0")
    Assert(a.Port() > 0)
    b := TuBind(io, "LocalHost:0")
    var msg: [1]u8 = [1]
    b.Send(io, msg[..], TuLo(false, a.Port())) else {
        Assert(false)
    }
    var buf: [4]u8 = [0; 4]
    (n: n, peer: p) := a.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    wb := TuLo(false, b.Port())
    Assert(n == 1 && TuSame(&p, &wb))
    z := TuBind(io, "[::1%0]:0")
    z.Close()
    var got := 0
    // The exact address of a bound socket is AddrInUse on both targets. (A
    // specific address over a wildcard one binds on Windows, which sets no
    // SO_EXCLUSIVEADDRUSE, and is AddrInUse on Linux: not judged here.)
    x := Bind(io, sys.arena.Fmt("127.0.0.1:{p}", (p: b.Port())).Must()) else e {
        got = TuCode(e)
        a.Close()
        b.Close()
        Assert(got == 3)
        // Malformed: every one is InvalidAddress with no call.
        bad := ["", "0", "127.0.0.1", "127.0.0.1:", "127.0.0.1:65536", "1.2.3:0", "::1:0", "[::1:0", "[::1]0",
            "[1.2.3.4]:0", "[::1%]:0", "[::1%x]:0", "[::1%99999999999]:0", "host.example:0", "127.0.0.01:0", " :0",
            // Listen's port is one to five digits, so six are
            // refused whatever their value.
            "127.0.0.1:000000", "[::1]:0000080", "127.0.0.1:000080"]
        for s in bad {
            var g := 0
            y := Bind(io, s) else e2 {
                g = TuCode(e2)
                Assert(g == 2)
                continue
            }
            y.Close()
            Assert(false)
        }
        return
    }
    x.Close()
    a.Close()
    b.Close()
    Assert(false)
}

// TuRecvCancel waits in a Recv nobody answers; got: 1 Canceled, 2 other.
// started is stored just before the Recv, with no suspension point
// between. Edge: this seq_cst store happens before TuAwaitStarted's
// load that reads 1, so the main task knows the receiver has passed the
// line before Recv; on one carrier, where a task runs until it suspends,
// the main task runs again only once the receiver has suspended, and its
// first suspension point is the Recv's wait in the poller.
TuRecvCancel(d: Datagram, io: Io, got: &raw.Atomic[u32], started: &raw.Atomic[u32]) {
    var buf: [16]u8 = [0; 16]
    started.Store(1)
    _ = d.Recv(io, mut buf[..]) else e {
        got.Store(Trunc[u32](TuCode(e)))
        d.Close()
        return
    }
    got.Store(9)
    d.Close()
}

// TuAwaitStarted yields until flag reads non-zero (TuRecvCancel's edge).
TuAwaitStarted(io: Io, flag: &raw.Atomic[u32]) {
    for flag.Load() == 0 {
        io.NoCancel().Yield() else {
        }
    }
}

// TuCancelRound runs one Recv cancelled in the way kind says: 0 at once
// (at one carrier before the receiver first runs, so its entry check sees
// the cancel), 1 once the receiver has started (at one carrier it is then
// waiting in the poller, so the cancel walk ends a pending operation:
// PollCancel, CancelIoEx or ASYNC_CANCEL, and the wait's Dekker pair), 2
// once it has started and 1 ms later (the same at 8 carriers, by time).
// The verdict: 1 Canceled.
TuCancelRound(io: Io, kind: int): u32 {
    d := TuBind(io, "127.0.0.1:0")
    g := raw.Atomic[u32].New(0)
    started := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go TuRecvCancel(d, io, &g, &started)
        if kind >= 1 {
            TuAwaitStarted(io, &started)
        }
        if kind == 2 {
            io.NoCancel().Sleep(core.DurationOf(1_000_000)) else {
            }
        }
        s.Cancel()
    }
    return g.Load()
}

test "Udp: a Recv ends with Canceled when cancelled at entry, while waiting in the poller (controlled at one carrier) and late" (sys: &System) {
    io := sys.io
    for round in 0..60 {
        Assert(TuCancelRound(io, round % 3) == 1)
    }
    // The other sockets still work after the cancels.
    a := TuBind(io, "127.0.0.1:0")
    b := TuBind(io, "127.0.0.1:0")
    var msg: [3]u8 = [1, 2, 3]
    a.Send(io, msg[..], TuLo(false, b.Port())) else {
        Assert(false)
    }
    var buf: [8]u8 = [0; 8]
    (n: n, peer: _) := b.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    Assert(n == 3 && buf[2] == 3)
    a.Close()
    b.Close()
}

// TuRecvCount is Recv's count, or -1 on an error.
TuRecvCount(d: &Datagram, io: Io, buf: mut []u8): int {
    (n: n, peer: _) := d.Recv(io, buf) else {
        return -1
    }
    return n
}

// TuBindCode binds addr and closes it: 0, or TuCode of the error.
TuBindCode(io: Io, addr: str): int {
    x := Bind(io, addr) else e {
        return TuCode(e)
    }
    x.Close()
    return 0
}

test "in a cancelled scope Bind, Send, Recv and Resolve are Canceled, and succeed under NoCancel" (sys: &System) {
    io := sys.io
    d := TuBind(io, "127.0.0.1:0")
    var msg: [1]u8 = [5]
    var buf: [4]u8 = [0; 4]
    var codes: [6]int = [0; 6]
    scope s := io.Scope() {
        s.Cancel()
        codes[0] = TuBindCode(io, "127.0.0.1:0")
        // A bad address in a cancelled scope is Canceled too (the
        // check comes before the work).
        codes[1] = TuBindCode(io, "nonsense")
    }
    scope s3 := io.Scope() {
        s3.Cancel()
        d.Send(io, msg[..], TuLo(false, d.Port())) else e {
            codes[2] = TuCode(e)
        }
        _ = d.Recv(io, mut buf[..]) else e {
            codes[3] = TuCode(e)
        }
        _ = Resolve(sys.arena, io, "127.0.0.1") else e {
            match e {
                Canceled {
                    codes[4] = 1
                }
                _ {
                    codes[4] = 5
                }
            }
        }
        // Shielded: the send and the receive work.
        d.Send(io.NoCancel(), msg[..], TuLo(false, d.Port())) else {
            codes[5] = 9
        }
        codes[5] = TuRecvCount(&d, io.NoCancel(), mut buf[..]) - 1
    }
    for k in 0..5 {
        Assert(codes[k] == 1)
    }
    Assert(codes[5] == 0)
    d.Close()
}

// TuServe echoes every datagram on d until its scope is cancelled, then
// closes it (its end is the cancel, not a count, so a lost or
// extra datagram cannot keep it waiting). echoed counts the echoes sent,
// bad any failure other than the closing cancel.
TuServe(d: Datagram, io: Io, echoed: &raw.Atomic[u32], bad: &raw.Atomic[u32]) {
    var buf: [64]u8 = [0; 64]
    for {
        (n: k, peer: p) := d.Recv(io, mut buf[..]) else e {
            if TuCode(e) != 1 {
                _ = bad.Add(1)
            }
            d.Close()
            return
        }
        d.Send(io, buf[0..k], p) else e {
            if TuCode(e) == 1 {
                d.Close()
                return
            }
            _ = bad.Add(1)
            continue
        }
        _ = echoed.Add(1)
    }
}

// TuWatch cancels t when prog has not moved for 2 s; a cancel of t ends it.
// No edge is needed for prog: it is a liveness hint that publishes no data
// (seq_cst, as every raw.Atomic operation), so any value it reads is safe.
TuWatch(t: &Scope, io: Io, prog: &raw.Atomic[u32]) {
    var last := prog.Load()
    for {
        io.Sleep(core.DurationOf(2_000_000_000)) else {
            return
        }
        now := prog.Load()
        if now == last {
            t.Cancel()
            return
        }
        last = now
    }
}

// TuClient makes rounds round trips to port ps from a socket of its own;
// each datagram carries the id and the round, and the echo must carry the
// same, from ps (at most one datagram per client in flight). Every failure
// is counted in bad and none can hang it: a failed Send skips its Recv, and
// a watchdog cancels the rounds when 2 s pass with no round done, which
// ends the waiting Recv with Canceled, counted once.
TuClient(io: Io, id: int, ps: int, rounds: int, bad: &raw.Atomic[u32]) {
    d := Bind(io, "127.0.0.1:0") else {
        _ = bad.Add(1)
        return
    }
    prog := raw.Atomic[u32].New(0)
    scope t := io.Scope() {
        t.go TuWatch(&t, io, &prog)
        TuClientRounds(&d, io, id, ps, rounds, &prog, bad)
        t.Cancel()
    }
    d.Close()
}

// TuClientRounds is TuClient's loop; a Canceled (the watchdog) ends it.
TuClientRounds(d: &Datagram, io: Io, id: int, ps: int, rounds: int, prog: &raw.Atomic[u32], bad: &raw.Atomic[u32]) {
    var out: [3]u8 = [0; 3]
    var back: [16]u8 = [0; 16]
    to := TuLo(false, ps)
    for i in 0..rounds {
        _ = prog.Add(1)
        out[0] = Trunc[u8](id)
        out[1] = Trunc[u8](i)
        out[2] = Trunc[u8](i >> 8)
        d.Send(io, out[..], to) else e {
            _ = bad.Add(1)
            if TuCode(e) == 1 {
                return
            }
            continue
        }
        (n: n, peer: p) := d.Recv(io, mut back[..]) else e {
            _ = bad.Add(1)
            if TuCode(e) == 1 {
                return
            }
            continue
        }
        if n != 3 || back[0] != out[0] || back[1] != out[1] || back[2] != out[2] || !TuSame(&p, &to) {
            _ = bad.Add(1)
        }
    }
}

test "Udp: 8 client tasks, 10,000 round trips through one server socket, every failure counted and none a hang" (sys: &System) {
    io := sys.io
    srv := TuBind(io, "127.0.0.1:0")
    ps := srv.Port()
    bad := raw.Atomic[u32].New(0)
    echoed := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go TuServe(srv, io, &echoed, &bad)
        scope c := io.Scope() {
            for id in 0..8 {
                c.go TuClient(io, id, ps, 1_250, &bad)
            }
        }
        s.Cancel()
    }
    // Edge: the scope's join orders every task's adds before these loads.
    Assert(bad.Load() == 0)
    Assert(echoed.Load() == 10_000)
}

// TuParse is ParseAddr's verdict on s: 1 parsed, 2 InvalidAddress, 5 other.
TuParse(s: str): int {
    _ = ParseAddr(s) else e {
        return 1 + TuCode(e) - 1
    }
    return 1
}

// TuText is a's Format output.
TuText(ar: &Arena, a: &Addr): str from ar {
    return ar.Fmt("{a}", (a: *a)).Must()
}

test "ParseAddr accepts netip's forms and refuses the rest; Format is RFC 5952; they round-trip" (sys: &System) {
    ar := sys.arena
    // Accepted, and Format gives the canonical text on the right.
    good := [
        (s: "1.2.3.4:80", f: "1.2.3.4:80"),
        (s: "0.0.0.0:0", f: "0.0.0.0:0"),
        (s: "255.255.255.255:65535", f: "255.255.255.255:65535"),
        (s: "1.2.3.4:00080", f: "1.2.3.4:80"),
        (s: "[::]:1", f: "[::]:1"),
        (s: "[::1]:53", f: "[::1]:53"),
        (s: "[2001:DB8::1]:443", f: "[2001:db8::1]:443"),
        (s: "[2001:db8:0:0:1:0:0:1]:1", f: "[2001:db8::1:0:0:1]:1"),
        (s: "[2001:0db8:0000:0000:0000:ff00:0042:8329]:9", f: "[2001:db8::ff00:42:8329]:9"),
        (s: "[1:0:0:2:0:0:0:3]:1", f: "[1:0:0:2::3]:1"),
        (s: "[1:0:2:0:3:0:4:0]:1", f: "[1:0:2:0:3:0:4:0]:1"),
        (s: "[1::]:1", f: "[1::]:1"),
        (s: "[1:2:3:4:5:6:7::]:1", f: "[1:2:3:4:5:6:7:0]:1"),
        (s: "[::ffff:1.2.3.4]:7", f: "[::ffff:1.2.3.4]:7"),
        (s: "[::FFFF:102:304]:7", f: "[::ffff:1.2.3.4]:7"),
        (s: "[::1.2.3.4]:7", f: "[::102:304]:7"),
        (s: "[1:2:3:4:5:6:1.2.3.4]:7", f: "[1:2:3:4:5:6:102:304]:7"),
        (s: "[fe80::1:2]:7", f: "[fe80::1:2]:7"),
    ]
    for g in good {
        a := ParseAddr(g.s) else {
            Panic("a good address was refused")
        }
        Assert(TuText(ar, &a) == g.f)
        // Format's text parses back to the same Addr.
        b := ParseAddr(TuText(ar, &a)) else {
            Panic("Format's text was refused")
        }
        Assert(TuSame(&a, &b))
    }
    bad := [
        "", ":", ":80", "1.2.3.4", "1.2.3.4:", "1.2.3.4:65536", "1.2.3.4:-1", "1.2.3.4:+1", "1.2.3.4:8a",
        "1.2.3:80", "1.2.3.4.5:80", "01.2.3.4:80", "1.2.3.256:80", "1..3.4:80", ".1.2.3:80", "1.2.3.:80",
        "localhost:80", "a.b.c.d:80", "::1:80", "[::1]", "[::1:80", "::1]:80", "[::1]80", "[1.2.3.4]:80",
        "[]:80", "[:]:80", "[:::]:80", "[1:::2]:80", "[1::2::3]:80", "[12345::]:80", "[1:2:3:4:5:6:7:8:9]:80",
        "[1:2:3:4:5:6:7:8::]:80", "[1:2:3:4:5:6:7]:80", "[1:2:3:4:5:6:7:]:80", "[:1:2:3:4:5:6:7]:80",
        "[::1%eth0]:80", "[fe80::1%1]:80", "[::1.2.3]:80", "[::1.2.3.4.5]:80", "[1.2.3.4::]:80",
        "[1:2:3:4:5:6:7:1.2.3.4]:80", "[::g]:80", " 1.2.3.4:80", "1.2.3.4:80 ", "1.2.3.4 :80",
    ]
    for s in bad {
        Assert(TuParse(s) == 2)
    }
}

test "Resolve: numeric addresses answer themselves, localhost is both loopbacks, an empty name is InvalidAddress" (sys: &System) {
    io := sys.io
    l := Resolve(sys.arena, io, "192.0.2.7") else {
        Panic("Resolve failed")
    }
    Assert(l.Len() == 1)
    a := l.Done()
    Assert(!a[0].v6 && a[0].ip[0] == 192 && a[0].ip[3] == 7 && a[0].port == 0)
    l6 := Resolve(sys.arena, io, "::1") else {
        Panic("Resolve failed")
    }
    b := l6.Done()
    Assert(b.Len() == 1 && b[0].v6 && b[0].ip[15] == 1)
    lo := Resolve(sys.arena, io, "LocalHost") else {
        Panic("Resolve failed")
    }
    c := lo.Done()
    Assert(c.Len() == 2 && !c[0].v6 && c[0].ip[0] == 127 && c[1].v6 && c[1].ip[15] == 1)
    // An IPv4-mapped answer comes back as IPv4.
    m := Resolve(sys.arena, io, "::ffff:10.1.2.3") else {
        Panic("Resolve failed")
    }
    d := m.Done()
    Assert(d.Len() == 1 && !d[0].v6 && d[0].ip[0] == 10 && d[0].ip[3] == 3)
    var got := 0
    _ = Resolve(sys.arena, io, "") else e {
        match e {
            InvalidAddress {
                got = 2
            }
            _ {
                got = 5
            }
        }
    }
    Assert(got == 2)
    // A zoned answer is refused, as ParseAddr refuses a zone.
    for z in ["fe80::1%5", "::1%1", "fe80::1%4294967295"] {
        var gz := 0
        _ = Resolve(sys.arena, io, z) else e {
            gz = TuResCode(e)
        }
        Assert(gz == 2)
    }
    ll := Resolve(sys.arena, io, "fe80::1") else {
        Panic("Resolve failed")
    }
    Assert(ll.Len() == 1)
}

// TuResCode is TuCode for Resolve's errors, 6 OutOfMemory.
TuResCode(e: Error | OutOfMemory): int {
    return match e {
        Canceled { 1 }
        InvalidAddress { 2 }
        AddrInUse { 3 }
        Os(code: _) { 4 }
        OutOfMemory { 6 }
        _ { 5 }
    }
}

// TuHandles is the process's open OS handles or descriptors, or -1 when
// unknown (std.sys's HandleCount; the blocks' parameter is named sys).
TuHandles(): int {
    return sys.HandleCount()
}

// TuCycle binds, sends one datagram to itself, receives it and closes;
// then a Bind on its own bound address (AddrInUse), a malformed Bind, an
// IPv6 Send from the IPv4 socket and a too-large Send, all failing. bad
// counts a step that did not go as stated.
TuCycle(io: Io, big: []u8, bad: mut int) {
    d := Bind(io, "127.0.0.1:0") else {
        *bad += 1
        return
    }
    p := d.Port()
    var msg: [4]u8 = [1, 2, 3, 4]
    d.Send(io, msg[..], TuLo(false, p)) else {
        *bad += 1
    }
    var buf: [8]u8 = [0; 8]
    if TuRecvCount(&d, io, mut buf[..]) != 4 {
        *bad += 1
    }
    if TuBindCode(io, "127.0.0.1:0") != 0 || TuBindCode(io, "127.0.0.1:x") != 2 {
        *bad += 1
    }
    var spec: [32]u8 = [0; 32]
    pre := "127.0.0.1:".AsBytes()
    for k in 0..pre.Len() {
        spec[k] = pre[k]
    }
    n := PutPort(mut spec, pre.Len(), p)
    if TuBindCode(io, str.From(spec[0..n]).Must()) != 3 {
        *bad += 1
    }
    var got := 0
    d.Send(io, msg[..], TuLo(true, p)) else e {
        got = TuCode(e)
    }
    d.Send(io, big, TuLo(false, p)) else e {
        got = got * 10 + TuCode(e)
    }
    if got != 24 {
        *bad += 1
    }
    d.Close()
}

// PutPort writes p in decimal at b[n..]; the new length.
PutPort(b: mut [32]u8, n: int, p: int): int {
    var d: [5]u8 = [0; 5]
    var k := 0
    var x := p
    for {
        d[k] = Trunc[u8](48 + x % 10)
        k += 1
        x = x / 10
        if x == 0 {
            break
        }
    }
    var m := n
    for k > 0 {
        k -= 1
        b[m] = d[k]
        m += 1
    }
    return m
}

test "Udp: 500 cycles of Bind, Send, Recv and Close with failing Binds and Sends, and 50 cancelled Recvs, leave the handle count unchanged" (sys: &System) {
    io := sys.io
    big := sys.arena.Slice[u8](70_000) else {
        Panic("arena")
    }
    var bad := 0
    // One round of each first: the first socket of a process loads
    // Winsock and the first task starts the carriers, each of which opens
    // handles of its own once (5 on Windows, measured: the count did not
    // change with 0 to 1,000 cycles before this warm-up).
    // Three cancel rounds, one of each kind, since the first kind can end
    // before the receiver task runs (so before 8 carriers have started).
    TuCycle(io, big[..], mut bad)
    TuCancelRounds(io, 3, mut bad)
    // Then every carrier, and timer slots on all of them: std.task starts a
    // carrier whenever a task is readied while every started one is busy,
    // and a carrier started inside the measured loop moves the count by more
    // than 2. The default carrier count is the CPU count, far more than the
    // 8 the warm-up above reaches.
    TuStartCarriers(io)
    // Then one whole unmeasured pass of what the measured pass does: after
    // the warm-up alone the measured pass still gained 1 to 6 handles, and
    // after a second pass 0 or 2 (measured on a Windows desktop, 2026-11-09,
    // 24 runs at the default, 2 and 8 carriers). A leak gains as
    // much in every pass, so the measured pass still sees it. Both passes
    // are short because mk test gives a block 60 s and a socket's Bind and
    // Close cost about 12 ms on WSL's Linux, uring and epoll alike, against
    // 0.1 ms on Windows (measured 2026-11-09, release build at 1 carrier).
    for _ in 0..250 {
        TuCycle(io, big[..], mut bad)
    }
    TuCancelRounds(io, 10, mut bad)
    h0 := TuHandles()
    for _ in 0..500 {
        TuCycle(io, big[..], mut bad)
    }
    TuCancelRounds(io, 50, mut bad)
    h1 := TuHandles()
    Assert(bad == 0)
    // A leak on any path of a cycle would be 500 or more, and on one kind
    // of cancelled Recv 16 or more. What may still appear is bounded by the
    // slack: std.sys's high-resolution timer slots, two handles each, made
    // when more carriers wait with a timeout at once than ever before.
    d := h1 - h0
    if h0 >= 0 && (d > 8 || d < -8) {
        var o := sys.Stdout()
        _ = Print(mut o, "handles moved by {d} from {h0}, at most 8\n", (d, h0)) else {
        }
        _ = o.Flush() else {
        }
    }
    Assert(h0 < 0 || (d <= 8 && d >= -8))
}

// TuBusy keeps its carrier for about 5 ms without suspending, so the next
// spawn of TuStartCarriers finds every started carrier busy.
TuBusy(io: Io) {
    t0 := core.IoNow(io).value
    for core.IoNow(io).value - t0 < 5_000_000 {
    }
}

// TuNap sleeps 1 ms twenty times.
TuNap(io: Io) {
    for _ in 0..20 {
        io.Sleep(core.DurationOf(1_000_000)) else {
            return
        }
    }
}

// TuStartCarriers starts every carrier the runtime may start (it starts one
// when a task is readied while none is idle or spinning, up to MK_CARRIERS
// or the CPU count): twice, two busy tasks per CPU. Then 16 tasks nap at
// once, so the carriers make the timer slots the loop may use.
TuStartCarriers(io: Io) {
    var n := 2 * sys.CpuCount()
    if n > 512 {
        n = 512
    }
    for _ in 0..2 {
        scope s := io.Scope() {
            for _ in 0..n {
                s.go TuBusy(io)
            }
        }
    }
    scope z := io.Scope() {
        for _ in 0..16 {
            z.go TuNap(io)
        }
    }
}

// TuCancelRounds runs n Recvs cancelled at entry, while waiting and late
// (TuCancelRound's kinds in turn); bad counts one that did not end with
// Canceled.
TuCancelRounds(io: Io, n: int, bad: mut int) {
    for round in 0..n {
        if TuCancelRound(io, round % 3) != 1 {
            *bad += 1
        }
    }
}

// TuPeer is a v6 IoAddr with ip[0..2] = hi, ip[15] = 1 and zone z.
TuPeer(hi0: u8, hi1: u8, z: u32): core.IoAddr {
    var ip: [16]u8 = [0; 16]
    ip[0] = hi0
    ip[1] = hi1
    ip[15] = 1
    return core.IoAddr(ip: ip, port: 9, v6: true, zone: z)
}

test "Recv names a link-local peer only in the socket's own zone, and Send addresses a link-local peer in that zone" (sys: &System) {
    // fe80::/10 is fe80:: to febf:ffff::; fec0:: (old site-local) and
    // fe00:: are outside it.
    for c in [(a: 0xFE, b: 0x80, ll: true), (a: 0xFE, b: 0xBF, ll: true), (a: 0xFE, b: 0xC0, ll: false),
        (a: 0xFE, b: 0x7F, ll: false), (a: 0x20, b: 0x01, ll: false), (a: 0xFF, b: 0x02, ll: false)] {
        q := TuPeer(Trunc[u8](c.a), Trunc[u8](c.b), 0)
        Assert(IsLinkLocal(&q.ip) == c.ll)
    }
    // A link-local peer in another zone than the socket's is refused (its
    // reply in zone 0 would go to the wrong link); in the socket's zone, or
    // any peer that is not link-local, is named.
    p5 := TuPeer(0xFE, 0x80, 5)
    Assert(!PeerZoneOk(&p5, 0) && !PeerZoneOk(&p5, 4) && PeerZoneOk(&p5, 5))
    pb := TuPeer(0xFE, 0xBF, 7)
    Assert(!PeerZoneOk(&pb, 0) && PeerZoneOk(&pb, 7))
    g := TuPeer(0x20, 0x01, 3)
    Assert(PeerZoneOk(&g, 0) && PeerZoneOk(&g, 5))
    v4 := core.IoAddr(ip: [0; 16], port: 9, v6: false, zone: 2)
    Assert(PeerZoneOk(&v4, 0))
    // Send's destination: a link-local to in the socket's zone, any other
    // in zone 0; the family mapping both ways; an unmapped IPv6 to from an
    // IPv4 socket refused.
    var dst := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 9)
    ll := Addr(ip: TuPeer(0xFE, 0x80, 0).ip, port: 7, v6: true)
    Assert(SendDst(true, 5, &ll, mut dst) && dst.zone == 5 && dst.v6 && dst.port == 7 && dst.ip[0] == 0xFE)
    Assert(SendDst(true, 0, &ll, mut dst) && dst.zone == 0)
    gl := Addr(ip: TuPeer(0x20, 0x01, 0).ip, port: 7, v6: true)
    Assert(SendDst(true, 5, &gl, mut dst) && dst.zone == 0 && dst.ip[0] == 0x20)
    l4 := TuLo(false, 8)
    Assert(SendDst(true, 5, &l4, mut dst) && dst.v6 && dst.zone == 0 && dst.ip[10] == 0xFF && dst.ip[11] == 0xFF &&
        dst.ip[12] == 127 && dst.ip[15] == 1 && dst.port == 8)
    Assert(SendDst(false, 0, &l4, mut dst) && !dst.v6 && dst.ip[0] == 127 && dst.ip[3] == 1 && dst.ip[10] == 0)
    var mip: [16]u8 = [0; 16]
    mip[10] = 0xFF
    mip[11] = 0xFF
    mip[12] = 10
    mip[15] = 3
    mapped := Addr(ip: mip, port: 6, v6: true)
    Assert(SendDst(false, 0, &mapped, mut dst) && !dst.v6 && dst.ip[0] == 10 && dst.ip[3] == 3 && dst.ip[10] == 0)
    Assert(!SendDst(false, 0, &gl, mut dst) && !SendDst(false, 0, &ll, mut dst))
    // A loopback peer has zone 0 on both targets, so the IPv6 echo of
    // The echo block passes this check; a socket bound with zone 0 keeps it.
    io := sys.io
    z := TuBind(io, "[::1%0]:0")
    Assert(z.zone == 0 && z.v6)
    a := TuBind(io, "[::1]:0")
    var msg: [2]u8 = [8, 9]
    a.Send(io, msg[..], TuLo(true, z.Port())) else {
        Assert(false)
    }
    var buf: [4]u8 = [0; 4]
    (n: n, peer: p) := z.Recv(io, mut buf[..]) else {
        Panic("Recv failed")
    }
    w := TuLo(true, a.Port())
    Assert(n == 2 && TuSame(&p, &w))
    z.Close()
    a.Close()
}

// TuListenVerdict and TuBindVerdict are 2 for InvalidAddress, 0 for any other
// outcome (success included, the socket closed at once).
TuListenVerdict(io: Io, s: str): int {
    l := Listen(io, s) else e {
        if TuCode(e) == 2 {
            return 2
        }
        return 0
    }
    l.Close()
    return 0
}

TuBindVerdict(io: Io, s: str): int {
    if TuBindCode(io, s) == 2 {
        return 2
    }
    return 0
}

test "Bind takes exactly Listen's forms: the same InvalidAddress verdict on every string of a table" (sys: &System) {
    io := sys.io
    // Port 0 or no port, and only loopback or wildcard hosts, so a string
    // Listen and Bind both accept binds on both targets.
    forms := [
        "127.0.0.1:0", ":0", "localhost:0", "LOCALHOST:0", "LocalHost:00000", "[::1]:0", "[::]:0", "[::1%0]:0",
        "[0:0:0:0:0:0:0:1]:0", "[0::1]:0", "127.0.0.1:00000", "[::1]:00", "0.0.0.0:0",
        "127.0.0.1:000000", "[::1]:0000080", "127.0.0.1:000080", "127.0.0.1:0000000000", "127.0.0.1:65536",
        "127.0.0.1:99999", "127.0.0.1:", "127.0.0.1", "", ":", "0", ":00000", ":000000", "[::1]", "[::1]:",
        "[::1]0", "[::1:0", "::1:0", "1.2.3:0", "127.0.0.01:0", "127.0.0.256:0", " :0", "127.0.0.1: 0",
        "127.0.0.1:+0", "127.0.0.1:-0", "127.0.0.1:0 ", "[::1%]:0", "[::1%x]:0", "[::1%4294967296]:0",
        "[::1%99999999999]:0", "[::1%1%2]:0", "[1.2.3.4]:0", "[]:0", "[:]:0", "[:::]:0", "[1::2::3]:0",
        "[12345::]:0", "[1:2:3:4:5:6:7:8:9]:0", "[1:2:3:4:5:6:7]:0", "[1:2:3:4:5:6:7:]:0", "[:1:2:3:4:5:6:7]:0",
        "[::1.2.3]:0", "[::1.2.3.4.5]:0", "[1.2.3.4::]:0", "[1:2:3:4:5:6:7:1.2.3.4]:0", "[::g]:0",
        "host.example:0", "localhost.:0", "localhos:0", "localhostx:0", "[localhost]:0", "127.0.0.1:0:0",
        "[::1]:0:0", "[[::1]]:0", "[::1]]:0", "[::1]:0]", "127.0.0.1.:0", "1.2.3.4.5:0",
    ]
    for s in forms {
        Assert(TuListenVerdict(io, s) == TuBindVerdict(io, s))
    }
    // The table holds both verdicts, so the agreement is not vacuous.
    Assert(TuBindVerdict(io, "127.0.0.1:00000") == 0 && TuBindVerdict(io, "127.0.0.1:000000") == 2)
}

// TuGot is one Recv's outcome: code 0 with n and the peer, else TuCode.
struct TuGot {
    n: int
    peer: Addr
    code: int
}

// TuTimer cancels s after ns nanoseconds, unless s is cancelled first.
TuTimer(s: &Scope, io: Io, ns: i64) {
    io.Sleep(core.DurationOf(ns)) else {
        return
    }
    s.Cancel()
}

// TuRecvInto is d.Recv(io, buf) into g.
TuRecvInto(d: &Datagram, io: Io, buf: mut []u8, g: mut TuGot) {
    (n: n, peer: p) := d.Recv(io, buf) else e {
        g.code = TuCode(e)
        return
    }
    g.n = n
    g.peer = p
    g.code = 0
}

// TuRecvWithin is d.Recv bounded by ns nanoseconds: a Recv the bound ends
// has code 1, so a lost datagram is a verdict, never a hang.
TuRecvWithin(d: &Datagram, io: Io, buf: mut []u8, ns: i64): TuGot {
    var g := TuGot(n: 0, peer: TuLo(false, 0), code: 5)
    scope t := io.Scope() {
        t.go TuTimer(&t, io, ns)
        TuRecvInto(d, io, buf, mut g)
        t.Cancel()
    }
    return g
}

// TuFlight binds a sender and sends one 2-byte datagram tagged k to port
// p, cancelling s just before the send (mode 1) or just after it (mode 0),
// so the datagram arrives while the cancel walk runs. The send is
// shielded, since s may already be cancelled. from is the sender's port, 0
// when a step failed. Edge: the store of from is ordered before the main
// task's load by the scope's join.
TuFlight(s: &Scope, io: Io, p: int, k: int, mode: int, from: &raw.Atomic[u32]) {
    q := io.NoCancel()
    tx := Bind(q, "127.0.0.1:0") else {
        s.Cancel()
        return
    }
    var msg: [2]u8 = [Trunc[u8](k), Trunc[u8](k >> 8)]
    if mode == 1 {
        s.Cancel()
    }
    tx.Send(q, msg[..], TuLo(false, p)) else {
        s.Cancel()
        tx.Close()
        return
    }
    from.Store(Trunc[u32](tx.Port()))
    if mode == 0 {
        s.Cancel()
    }
    tx.Close()
}

test "Udp: a Recv cancelled with its datagram in flight either returns it or leaves it queued, and the same socket keeps working" (sys: &System) {
    io := sys.io
    d := TuBind(io, "127.0.0.1:0")
    p := d.Port()
    var buf: [8]u8 = [0; 8]
    var canceled := 0
    for k in 0..1_000 {
        from := raw.Atomic[u32].New(0)
        var g := TuGot(n: 0, peer: TuLo(false, 0), code: 5)
        scope s := io.Scope() {
            s.go TuFlight(&s, io, p, k, k % 2, &from)
            // The body's own Recv waits under s, so the sender's cancel
            // ends it unless the datagram's completion won.
            TuRecvInto(&d, io, mut buf[..], mut g)
        }
        if g.code == 1 {
            // Canceled: the datagram was not consumed, so the same socket
            // returns it (2 s bound: a lost datagram is a failure here).
            canceled += 1
            g = TuRecvWithin(&d, io, mut buf[..], 2_000_000_000)
        }
        // This round's datagram, from this round's sender: never an earlier
        // round's, never none. The first wrong round fails the block, so a
        // runtime that loses datagrams fails in 2 s, not at the timeout.
        want := TuLo(false, Trunc[int](from.Load()))
        Assert(g.code == 0 && g.n == 2 && buf[0] == Trunc[u8](k) && buf[1] == Trunc[u8](k >> 8) && TuSame(&g.peer, &want))
    }
    // Both outcomes occur (measured on Windows: the Recv cancelled before
    // the send is Canceled, the one sent to first returns the datagram).
    Assert(canceled > 0)
    // Nothing is left over: a stray copy of any round's datagram would be
    // read here instead of the bound ending it.
    e := TuRecvWithin(&d, io, mut buf[..], 20_000_000)
    Assert(e.code == 1)
    d.Close()
}

// TuCanary fills a 16 KiB frame array with a pattern, waits 50 ms and
// reports whether every byte is still the pattern. Called at the depth
// Resolve was, it covers the frames Resolve and the runtime's resolve
// entry used, so a lookup that wrote its answers after Resolve returned
// would change it. io.Random of one byte first passes the array's address
// to an indirect call, so the compiler must read the bytes again after the
// wait.
TuCanary(io: Io): bool {
    var c: [16384]u8 = [0; 16384]
    io.NoCancel().Random(mut c[0..1]) else {
    }
    for i in 0..c.Len() {
        c[i] = 0xA5
    }
    io.NoCancel().Sleep(core.DurationOf(50_000_000)) else {
    }
    for x in c {
        if x != 0xA5 {
            return false
        }
    }
    return true
}

// TuResolveOnce resolves host in the caller's scope and checks the frame
// after: TuResCode of the error (0 on success), or 9 when the canary
// changed.
TuResolveOnce(ar: &Arena, io: Io, host: str): int {
    var code := 0
    _ = Resolve(ar, io, host) else e {
        code = TuResCode(e)
    }
    if !TuCanary(io) {
        return 9
    }
    return code
}

// TuCancelAfter cancels s once started reads 1 and ns more nanoseconds
// pass (0: at once).
TuCancelAfter(s: &Scope, io: Io, ns: i64, started: &raw.Atomic[u32]) {
    TuAwaitStarted(io, started)
    if ns > 0 {
        io.NoCancel().Sleep(core.DurationOf(ns)) else {
        }
    }
    s.Cancel()
}

// TuNow is the monotonic clock in nanoseconds (std.sys; the blocks'
// parameter is named sys).
TuNow(): i64 {
    return sys.Now()
}

test "Resolve: a lookup waiting on the network ends at a cancel, and nothing writes its frame after it returns" (sys: &System) {
    io := sys.io
    t0 := TuNow()
    seed := t0 & 0xFFFF_FFFF
    var codes: [10]int = [0; 10]
    for k in 0..32 {
        // A fresh name each round and each run, so no cache answers it.
        host := sys.arena.Fmt("mk-net2-{k}-{seed}.invalid", (k, seed)).Must()
        started := raw.Atomic[u32].New(0)
        var code := 0
        scope s := io.Scope() {
            s.go TuCancelAfter(&s, io, i64(k % 8) * 1_000_000, &started)
            started.Store(1)
            code = TuResolveOnce(sys.arena, io, host)
        }
        codes[code] += 1
    }
    // Canceled, or the network's answer first (InvalidAddress for NXDOMAIN,
    // Os, Unreachable or Refused when no resolver answers: the machine's
    // network, not std.net, decides those); never a changed frame, never
    // success for a name under .invalid (RFC 6761 6.4), never OutOfMemory.
    Assert(codes[0] == 0 && codes[6] == 0 && codes[9] == 0)
    // 32 lookups that a cancel ends after at most 7 ms, each with a 50 ms
    // canary: about 2 s. Without the cancel, one lookup alone can wait out
    // the system's timeout (about 12 s on Windows), so 20 s is the line.
    Assert(TuNow() - t0 < 20_000_000_000)
}
