// Why: a stream's addresses, options, half-close and cancel-safety are
// promises the OS keeps differently on Windows and Linux; each needs a check.
// What: test blocks for LocalAddr and PeerAddr, SetNoDelay, SetKeepAlive,
// Shutdown, ListenWith, a Conn over a stream used by two tasks, a cancelled
// Conn.Read that loses nothing, a cancelled WriteAll and Write (the prefix
// rule), an unnamed peer, and Check's error mapping.
// How: every block listens on loopback port 0; run by mk test --std=std.net
// and std/std-net_connblocks_test.go. Keep-alive timers are read from Linux's
// /proc/net/tcp (TkTimer).

import std.core
import std.fs
import std.rt
import std.time

// SameAddr: a and b are the same address and port.
SameAddr(a: Addr, b: Addr): bool {
    if a.port != b.port || a.v6 != b.v6 {
        return false
    }
    for i in 0..16 {
        if a.ip[i] != b.ip[i] {
            return false
        }
    }
    return true
}

// IsLoopback4: a is 127.0.0.1.
IsLoopback4(a: Addr): bool {
    return !a.v6 && a.ip[0] == 127 && a.ip[1] == 0 && a.ip[2] == 0 && a.ip[3] == 1
}

// TnPair is a connected loopback pair: the dialled end and the accepted
// one.
TnPair(io: Io, l: &Listener): Error!(c: Stream, s: Stream) {
    var b: [32]u8 = [0; 32]
    c := try TnDial(io, TnAddr(mut b, l.Port()))
    s := l.Accept(io) else e {
        c.Close()
        fail e
    }
    return (c: c, s: s)
}

test "A stream knows both ends' addresses, and its Conn keeps the peer's" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    Assert(IsLoopback4(c.PeerAddr()) && int(c.PeerAddr().port) == l.Port())
    Assert(IsLoopback4(c.LocalAddr()) && c.LocalAddr().port != 0)
    Assert(SameAddr(s.PeerAddr(), c.LocalAddr()))
    Assert(SameAddr(s.LocalAddr(), c.PeerAddr()))
    peer := c.PeerAddr()
    conn := c.Conn()
    defer conn.Abandon()
    p := conn.PeerAddr() else {
        Panic("a stream's Conn lost its peer")
    }
    Assert(SameAddr(p, peer))
}

test "An IPv6 loopback stream reports [::1] at both ends" (sys: &System) {
    l := Listen(sys.io, "[::1]:0") else {
        // A host with no IPv6 loopback has nothing to test here.
        return
    }
    defer l.Close()
    var b: [64]u8 = [0; 64]
    var w := core.Capped.New(mut b[..])
    var ww := w.Writer()
    try Print(mut ww, "[::1]:{p}", (p: l.Port()))
    c := try Dial(sys.io, str.From(w.Bytes()).Must())
    defer c.Close()
    s := try l.Accept(sys.io)
    defer s.Close()
    Assert(c.PeerAddr().v6 && c.PeerAddr().ip[15] == 1 && int(c.PeerAddr().port) == l.Port())
    Assert(SameAddr(s.PeerAddr(), c.LocalAddr()))
}

test "NoDelay and KeepAlive set and clear on a TCP stream" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer c.Close()
    defer s.Close()
    try c.SetNoDelay(true)
    try c.SetNoDelay(false)
    try c.SetKeepAlive(some(time.Seconds(30)))
    // Rounded up to one second; zero leaves the OS's idle time.
    try c.SetKeepAlive(some(core.DurationOf(1)))
    try c.SetKeepAlive(some(core.DurationOf(0)))
    try c.SetKeepAlive(none)
    Assert(KeepAliveSeconds(1) == 1 && KeepAliveSeconds(1_000_000_000) == 1 && KeepAliveSeconds(1_000_000_001) == 2)
    Assert(KeepAliveSeconds(-5) == 0 && KeepAliveSeconds(0) == 0)
}

test "Shutdown(Write) is the peer's end of stream, and the other direction still works" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer c.Close()
    defer s.Close()
    try c.WriteAll(sys.io, "abc".AsBytes())
    try c.Shutdown(sys.io, Shutdown.Write)
    var buf: [8]u8 = [0; 8]
    var n := 0
    for {
        k := try s.Read(sys.io, mut buf[n..])
        if k == 0 {
            break
        }
        n += k
    }
    Assert(n == 3 && buf[0] == 0x61 && buf[2] == 0x63)
    try s.WriteAll(sys.io, "xyz".AsBytes())
    try s.Shutdown(sys.io, Shutdown.Both)
    n = 0
    for {
        k := try c.Read(sys.io, mut buf[n..])
        if k == 0 {
            break
        }
        n += k
    }
    Assert(n == 3 && buf[0] == 0x78)
}

test "Conn.Shutdown(Write) of a stream's Conn ends the peer's reads" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    conn := c.Conn()
    onerr conn.Abandon()
    try conn.WriteAll(sys.io, "hi".AsBytes())
    try conn.Shutdown(sys.io, Shutdown.Write)
    var buf: [8]u8 = [0; 8]
    Assert(try s.Read(sys.io, mut buf[..]) == 2)
    Assert(try s.Read(sys.io, mut buf[..]) == 0)
    // A stream's Close only releases: a cancelled scope does not stop it.
    scope sc := sys.io.Scope() {
        sc.Cancel()
        try conn.Close(sys.io)
    }
}

test "Shutdown in a cancelled scope is Canceled and changes nothing" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer c.Close()
    defer s.Close()
    var got := ""
    scope sc := sys.io.Scope() {
        sc.Cancel()
        c.Shutdown(sys.io, Shutdown.Write) else e {
            got = ErrorName(e)
        }
    }
    Assert(got == "Canceled")
    try c.WriteAll(sys.io, "z".AsBytes())
    var buf: [4]u8 = [0; 4]
    Assert(try s.Read(sys.io, mut buf[..]) == 1)
}

test "ListenWith: DefaultListenOptions listens as Listen does, a no_delay listener serves its streams, and a backlog of 4 holds 4 connections before any Accept" (sys: &System) {
    var o := DefaultListenOptions
    o.no_delay = true
    l := try ListenWith(sys.io, "127.0.0.1:0", o)
    defer l.Close()
    Assert(l.Port() != 0)
    (c, s) := try TnPair(sys.io, &l)
    defer c.Close()
    defer s.Close()
    try s.WriteAll(sys.io, "ok".AsBytes())
    var buf: [4]u8 = [0; 4]
    Assert(try c.Read(sys.io, mut buf[..]) == 2)
    d := try ListenWith(sys.io, "127.0.0.1:0", DefaultListenOptions)
    d.Close()
    // Only the lower bound: what a full queue does differs by OS.
    var small := DefaultListenOptions
    small.backlog = 4
    b := try ListenWith(sys.io, "127.0.0.1:0", small)
    defer b.Close()
    var ab: [32]u8 = [0; 32]
    at := TnAddr(mut ab, b.Port())
    d1 := try TnDial(sys.io, at)
    defer d1.Close()
    d2 := try TnDial(sys.io, at)
    defer d2.Close()
    d3 := try TnDial(sys.io, at)
    defer d3.Close()
    d4 := try TnDial(sys.io, at)
    defer d4.Close()
    for _ in 0..4 {
        a := try b.Accept(sys.io)
        defer a.Close()
        try a.WriteAll(sys.io, "k".AsBytes())
    }
    Assert(try d1.Read(sys.io, mut buf[..]) == 1 && try d4.Read(sys.io, mut buf[..]) == 1)
}

test "ListenWith with reuse_addr false refuses a port still in TIME_WAIT on Linux, where the default rebinds it, Windows rebinds it, and a bad address fails as Listen's does" (sys: &System) {
    var o := DefaultListenOptions
    o.reuse_addr = false
    // The first listener sets SO_REUSEADDR, and so its accepted socket and
    // that socket's TIME_WAIT entry: Linux rebinds over a TIME_WAIT entry
    // only when both sockets set it (measured: a first listener without it
    // made the default's rebind below AddrInUse too).
    l := try ListenWith(sys.io, "127.0.0.1:0", DefaultListenOptions)
    var ab: [32]u8 = [0; 32]
    at := TnAddr(mut ab, l.Port())
    (c, s) := TnPair(sys.io, &l) else e {
        l.Close()
        fail e
    }
    // The accepted end closes first, so its port is held in TIME_WAIT (or
    // FIN_WAIT_2 until the client's FIN lands): one entry, under the
    // machine's socket rule.
    s.Close()
    var buf: [1]u8 = [0; 1]
    n := c.Read(sys.io, mut buf[..]) else {
        c.Close()
        l.Close()
        Panic("the client's read after the server's close failed")
    }
    Assert(n == 0)
    c.Close()
    l.Close()
    again := ListenWith(sys.io, at, o) else e {
        Assert(ErrorName(e) == "AddrInUse")
        if Target.os == core.Os.Linux {
            r := try ListenWith(sys.io, at, DefaultListenOptions)
            r.Close()
        }
        bad := ListenWith(sys.io, "nonsense", o) else e2 {
            Assert(ErrorName(e2) == "InvalidAddress")
            return
        }
        bad.Close()
        Panic("ListenWith took a bad address")
    }
    again.Close()
    if Target.os == core.Os.Linux {
        Panic("ListenWith with reuse_addr false rebound a port in TIME_WAIT")
    }
    // Windows binds over TIME_WAIT (rt.ListenWith's doc, SD8-Listen-Reuse).
    bad := ListenWith(sys.io, "nonsense", o) else e2 {
        Assert(ErrorName(e2) == "InvalidAddress")
        return
    }
    bad.Close()
    Panic("ListenWith took a bad address")
}

// ConnReader reads n bytes from c into out (its sum and count), one Read
// at a time.
ConnReader(io: Io, c: &Conn, n: int, sum: mut int, got: mut int) {
    var buf: [700]u8 = [0; 700]
    for *got < n {
        k := c.Read(io, mut buf[..]) else {
            return
        }
        if k == 0 {
            return
        }
        for i in 0..k {
            *sum += int(buf[i])
        }
        *got += k
    }
}

// ConnWriter writes n bytes (byte i is i % 251) to c in pieces of 1 to
// 997 bytes.
ConnWriter(io: Io, c: &Conn, n: int, failed: mut bool) {
    var buf: [997]u8 = [0; 997]
    var off := 0
    var piece := 1
    for off < n {
        var k := piece
        if k > n - off {
            k = n - off
        }
        for i in 0..k {
            buf[i] = Trunc[u8]((off + i) % 251)
        }
        c.WriteAll(io, buf[0..k]) else {
            *failed = true
            return
        }
        off += k
        piece = piece * 7 % 997 + 1
    }
}

test "One task reads a stream's Conn while another writes it, through an echo" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    var b: [32]u8 = [0; 32]
    n := 300_000
    var echoed := 0
    var sum := 0
    var got := 0
    var failed := false
    scope s := sys.io.Scope() {
        s.go TnEcho(&l, sys.io, mut echoed)
        st := try TnDial(sys.io, TnAddr(mut b, l.Port()))
        conn := st.Conn()
        onerr conn.Abandon()
        scope inner := sys.io.Scope() {
            inner.go ConnReader(sys.io, &conn, n, mut sum, mut got)
            inner.go ConnWriter(sys.io, &conn, n, mut failed)
        }
        // The Conn's Close ends the echo's reads.
        try conn.Close(sys.io)
    }
    var want := 0
    for i in 0..n {
        want += i % 251
    }
    Assert(!failed && got == n && sum == want && echoed == n)
}

// CancelLater cancels s after nap ns.
CancelLater(s: &Scope, io: Io, nap: i64) {
    io.Sleep(core.DurationOf(nap)) else {
        return
    }
    s.Cancel()
}

test "a Conn.Read cancelled while it waits returns Canceled, and the next Read gets the bytes sent after" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    conn := c.Conn()
    onerr conn.Abandon()
    var buf: [16]u8 = [0; 16]
    var got := ""
    scope sc := sys.io.Scope() {
        sc.go CancelLater(&sc, sys.io, 5_000_000)
        _ = conn.Read(sys.io, mut buf[..]) else e {
            got = ErrorName(e)
        }
    }
    Assert(got == "Canceled")
    try s.WriteAll(sys.io, "after".AsBytes())
    var n := 0
    for n < 5 {
        n += try conn.Read(sys.io, mut buf[n..])
    }
    Assert(n == 5 && buf[0] == 0x61 && buf[4] == 0x72)
    try conn.Close(sys.io)
}

test "a TCP stream whose peer the OS cannot name reports the zero Addr, and its Conn's PeerAddr is none" (sys: &System) {
    // getpeername of a listening socket fails (ENOTCONN, WSAENOTCONN) as
    // it does for an accepted socket whose client already reset: the
    // failure path of NewStream, made deterministic.
    l := TnListen(sys.io)
    Listener(h, ..) := l
    st := NewStream(sys.io, h)
    Assert(st.kind == StreamTcp && SameAddr(st.PeerAddr(), ZeroAddr))
    conn := st.Conn()
    defer conn.Abandon()
    match conn.PeerAddr() {
        some(_) {
            Panic("a Conn reported a peer the OS could not name")
        }
        none {}
    }
}

test "Check maps SysErr.Unsupported to Unsupported, which 1.1 mapped to Os, and an unnamed SysErr to Os with the OS's code" (sys: &System) {
    var name := ""
    _ = Check(core.IoResult(value: 0, err: core.SysErr.Unsupported, os: 0)) else e {
        name = ErrorName(e)
        0
    }
    Assert(name == "Unsupported")
    var code: i64 = -1
    _ = Check(core.IoResult(value: 0, err: core.SysErr.Other, os: 1234)) else e {
        code = match e {
            Os(code: k) { k }
            _ { -2 }
        }
        0
    }
    Assert(code == 1234)
    Assert(try Check(core.IoResult(value: 7, err: core.SysErr.None, os: 0)) == 7)
}

// TkHex is the hex number b spells, -1 when b is empty, longer than 15
// digits or holds another byte.
TkHex(b: []u8): i64 {
    if b.Len() == 0 || b.Len() > 15 {
        return -1
    }
    var v: i64 = 0
    for i in 0..b.Len() {
        c := b[i]
        var d: i64 = -1
        if c >= 0x30 && c <= 0x39 {
            d = i64(c - 0x30)
        } else if c >= 0x41 && c <= 0x46 {
            d = i64(c - 0x41) + 10
        } else if c >= 0x61 && c <= 0x66 {
            d = i64(c - 0x61) + 10
        }
        if d < 0 {
            return -1
        }
        v = v * 16 + d
    }
    return v
}

// TkField is the field of line at index want, fields split by spaces;
// empty when the line has fewer.
TkField(line: []u8, want: int): []u8 from line {
    var i := 0
    var k := 0
    for i < line.Len() {
        for i < line.Len() && line[i] == 0x20 {
            i += 1
        }
        var j := i
        for j < line.Len() && line[j] != 0x20 {
            j += 1
        }
        if k == want && j > i {
            return line[i..j]
        }
        k += 1
        i = j
    }
    return line[0..0]
}

// TkAfter is the hex number after the first colon of f (a port in
// "0100007F:1F90", the ticks in "02:00000BB8"), and TkBefore the one
// before it; -1 when f has no colon.
TkAfter(f: []u8): i64 {
    for i in 0..f.Len() {
        if f[i] == 0x3A {
            return TkHex(f[i + 1..])
        }
    }
    return -1
}

TkBefore(f: []u8): i64 {
    for i in 0..f.Len() {
        if f[i] == 0x3A {
            return TkHex(f[0..i])
        }
    }
    return -1
}

// TkTimer finds the IPv4 connection from local port lp to remote port rp
// in Linux's /proc/net/tcp: its timer kind (the tr field; 2 is the
// keep-alive timer, 0 none) and the ticks until it fires (USER_HZ, 100 a
// second). False when no line matches.
TkTimer(sys: &System, lp: int, rp: int, tr: mut i64, when: mut i64): fs.Error|OutOfMemory!bool {
    text := try fs.ReadBytes(sys.arena, sys.io, "/proc/net/tcp")
    var start := 0
    for start < text.Len() {
        var end := start
        for end < text.Len() && text[end] != 0x0A {
            end += 1
        }
        line := text[start..end]
        start = end + 1
        if TkAfter(TkField(line, 1)) == i64(lp) && TkAfter(TkField(line, 2)) == i64(rp) {
            t := TkField(line, 5)
            *tr = TkBefore(t)
            *when = TkAfter(t)
            return true
        }
    }
    return false
}

test "on Linux: SetKeepAlive arms the keep-alive timer within the idle time set, some(0) keeps the idle time already set, and none stops it (/proc/net/tcp)" (sys: &System) {
    if Target.os != core.Os.Linux {
        // Windows has no table of a socket's timers to read, and std has
        // no option getter.
        return
    }
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer c.Close()
    defer s.Close()
    lp := int(c.LocalAddr().port)
    rp := l.Port()
    var tr: i64 = 0
    var when: i64 = 0
    // Off: no timer on an idle established socket.
    Assert(try TkTimer(sys, lp, rp, mut tr, mut when))
    Assert(tr == 0)
    try c.SetKeepAlive(some(time.Seconds(30)))
    Assert(try TkTimer(sys, lp, rp, mut tr, mut when))
    Assert(tr == 2 && when > 2_500 && when <= 3_000)
    // some(0) sets no idle time: the 30 s stays, not the OS's 7,200 s.
    try c.SetKeepAlive(some(core.DurationOf(0)))
    Assert(try TkTimer(sys, lp, rp, mut tr, mut when))
    Assert(tr == 2 && when > 2_500 && when <= 3_000)
    try c.SetKeepAlive(some(time.Seconds(100)))
    Assert(try TkTimer(sys, lp, rp, mut tr, mut when))
    Assert(tr == 2 && when > 9_500 && when <= 10_000)
    try c.SetKeepAlive(none)
    Assert(try TkTimer(sys, lp, rp, mut tr, mut when))
    Assert(tr == 0)
}

// TnSmallSend is the send buffer the cut-write blocks ask for: 0, which
// Windows keeps and Linux raises to its minimum, and on macOS, whose
// setsockopt refuses a size below 1, 16 KiB: small beside the peer's
// receive buffer, which then decides when a send waits, and large enough
// that WriteCut's 4,096 Writes, each taking up to this much, outrun that
// buffer (with 1 byte a Write would take a byte per acknowledgment).
TnSmallSend(): i64 {
    return if Target.os == core.Os.Macos { 16_384 } else { 0 }
}

// MidByte is byte i of the cancelled WriteAll's payload: never 0xFF, the byte the caller
// overwrites its buffer with once WriteAll returned.
MidByte(i: int): u8 {
    return Trunc[u8](i % 251)
}

test "a Conn.WriteAll cancelled mid-send fails Canceled having sent a prefix the peer reads intact, and leaves the caller's buffer alone" (sys: &System) {
    // More than loopback's send and receive buffers hold together, so the
    // send still waits on a peer that does not read when the scope's
    // deadline lands.
    n := 32 << 20
    big := sys.arena.Slice[u8](n) else {
        Panic("arena")
    }
    for i in 0..n {
        big[i] = MidByte(i)
    }
    rb := sys.arena.Slice[u8](65_536) else {
        Panic("arena")
    }
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    // No send buffer. Observed on Windows: with the default one, this
    // 32 MiB WriteAll returned success in 3 ms and the peer later read it
    // all, so no cancel could land mid-send; with 0 it waits and the
    // deadline cancels it. (That Windows then sends from the caller's
    // memory, bounded by the peer's window, is from knowledge, not
    // observed.) Linux keeps its minimum. Darwin refuses a size below 1
    // (EINVAL), so there it is TnSmallSend's.
    r := rt.SockOption(rt.NativeIo(), c.h, core.IoSockSendBuffer, TnSmallSend(), 0)
    conn := c.Conn()
    onerr conn.Abandon()
    _ = try Check(r)
    var why := ""
    scope sc := sys.io.Scope() {
        sc.CancelAfter(core.DurationOf(50_000_000))
        conn.WriteAll(sys.io, big[..]) else e {
            why = ErrorName(e)
        }
    }
    Assert(why == "Canceled")
    // The call returned, so the buffer is the caller's again: no send in
    // flight may still read it. Every byte the peer reads below must be
    // the payload's, none 0xFF.
    for i in 0..n {
        big[i] = 0xFF
    }
    conn.Abandon()
    var k := 0
    var bad := 0
    // 1: end of stream; 2: Reset (an OS may reset a connection whose send
    // was cancelled); 3: another failure.
    var end := 0
    for end == 0 {
        m := s.Read(sys.io, mut rb[..]) else e {
            end = match e {
                Reset { 2 }
                _ { 3 }
            }
            0
        }
        if end == 0 && m == 0 {
            end = 1
        }
        for j in 0..m {
            if rb[j] != MidByte(k + j) {
                bad += 1
            }
        }
        k += m
    }
    Assert(bad == 0 && k < n && end != 3)
    // A prefix took effect: an orderly end of stream comes after some of it.
    Assert(end == 2 || k > 0)
}

// WriteCut checks Stream.Write's cancel-safety, prefix: it writes
// 64 KiB chunks of a stream whose byte i is MidByte(i) through c, each
// Write under its own 100 ms deadline, while the peer s reads nothing,
// until a Write returns Canceled. Then it closes c and reads s to its
// end. Every counted byte must arrive and the cancelled Write may add
// some or all of its chunk (all of it was seen delivered on Windows
// AF_UNIX), so the peer reads from counted to counted + 64 KiB
// bytes, each the stream's own; a Reset may cut it short (on Windows, with
// the send buffer set to 0, a cancelled TCP send reset the connection and
// the peer's first read was Reset; with the default buffer the cut chunk
// was delivered whole and the stream ended normally, in 6 runs of 6). The
// line it prints is shown when the block fails.
WriteCut(sys: &System, c: Stream, s: &Stream) {
    n := 65_536
    chunk := sys.arena.Slice[u8](n) else {
        Panic("arena")
    }
    rb := sys.arena.Slice[u8](65_536) else {
        Panic("arena")
    }
    var sent := 0
    var writes := 0
    var cut := false
    // 4,096 Writes are 256 MiB, more than loopback holds for a peer that
    // does not read.
    for writes < 4_096 && !cut {
        for j in 0..n {
            chunk[j] = MidByte(sent + j)
        }
        // The count sent; -1 for Canceled, -3 for another failure.
        var got := -2
        scope d := sys.io.Scope() {
            d.CancelAfter(core.DurationOf(100_000_000))
            got = c.Write(sys.io, chunk[..]) else e {
                match e {
                    Canceled { -1 }
                    _ { -3 }
                }
            }
        }
        Assert(got == -1 || got > 0 && got <= n)
        writes += 1
        if got == -1 {
            cut = true
        } else {
            sent += got
        }
    }
    // The call returned, so no send in flight may still read the chunk.
    for j in 0..n {
        chunk[j] = 0xFF
    }
    c.Close()
    var k := 0
    var bad := 0
    // 1: end of stream; 2: Reset; 3: another failure.
    var end := 0
    for end == 0 {
        m := s.Read(sys.io, mut rb[..]) else e {
            end = match e {
                Reset { 2 }
                _ { 3 }
            }
            0
        }
        if end == 0 && m == 0 {
            end = 1
        }
        for j in 0..m {
            if rb[j] != MidByte(k + j) {
                bad += 1
            }
        }
        k += m
    }
    var o := sys.Stdout()
    _ = Print(mut o, "writes {w}, counted {c}, the peer read {k} (the cut Write's share {x}), end {e}, bad {b}\n",
        (w: writes, c: sent, k: k, x: k - sent, e: end, b: bad)) else {
    }
    _ = o.Flush() else {
    }
    Assert(cut && bad == 0 && end != 3 && k <= sent + n)
    Assert(end == 2 || k >= sent)
}

test "a Stream.Write cut by its deadline is prefix: the peer reads the counted bytes and some or all of the cut chunk, or a Reset ends it" (sys: &System) {
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    // No send buffer, as the WriteAll block above: with Windows' default
    // one a Write returns once the data is taken, wherever the peer is.
    r := rt.SockOption(rt.NativeIo(), c.h, core.IoSockSendBuffer, TnSmallSend(), 0)
    onerr c.Close()
    _ = try Check(r)
    WriteCut(sys, c, &s)
}

test "a Stream.Write cut by its deadline with the default send buffer is prefix: the peer reads the counted bytes and some or all of the cut chunk" (sys: &System) {
    // With no send buffer (the block above) Windows resets the connection
    // and the peer reads nothing, so there only the Canceled is checked.
    // With the default buffer the cut came within 4 or 5 Writes and its
    // chunk was delivered whole (6 runs of 6), so this block checks the bytes on Windows TCP too.
    l := TnListen(sys.io)
    defer l.Close()
    (c, s) := try TnPair(sys.io, &l)
    defer s.Close()
    WriteCut(sys, c, &s)
}

test "an accepted stream's peer is the address the accept op returned, so a client that reset before the stream was made is still named" (sys: &System) {
    // Accept's three steps run apart here: the runtime's accept, the
    // client's reset, then NewStream. On Linux getpeername of the accepted
    // socket fails with ENOTCONN once the reset has landed (inet_getname:
    // the socket is TCP_CLOSE), so a peer read that way would be none
    // there; the accept op's address is not. Windows' getpeername still
    // names the reset peer (observed), so there the block holds either
    // way, and a block of task/tests_net.mk shows that no getpeername is
    // made.
    l := TnListen(sys.io)
    defer l.Close()
    var b: [32]u8 = [0; 32]
    c := try TnDial(sys.io, TnAddr(mut b, l.Port()))
    local := c.LocalAddr()
    a := Check(core.IoAccept(sys.io, l.h)) else e {
        c.Close()
        fail e
    }
    h := Trunc[uint](a)
    Assert(rt.SockOption(sys.io, c.h, core.IoSockLinger, 1, 0).err == core.SysErr.None)
    c.Close()
    // The reset has landed when a receive on the accepted socket ends.
    var buf: [8]u8 = [0; 8]
    for {
        r := core.IoRecv(sys.io, h, mut buf[..])
        if r.err != core.SysErr.None || r.value == 0 {
            break
        }
    }
    st := NewStream(sys.io, h)
    Assert(st.kind == StreamTcp && SameAddr(st.PeerAddr(), local))
    conn := st.Conn()
    defer conn.Abandon()
    p := conn.PeerAddr() else {
        Panic("an accepted stream's Conn lost the peer the accept named")
    }
    Assert(SameAddr(p, local))
}
