// Why: TCP over loopback must connect, echo, refuse and cancel exactly as
// documented, including a cancel racing data in flight.
// What: test blocks for Listen, Dial, Accept, Read, Write, WriteAll, Bind,
// and the cancellation of Read and Accept at every point of their wait.
// How: run by mk test --std=std.net and std/std-io_blocks_test.go. Every
// block listens on 127.0.0.1 port 0 and panics with "net.Listen failed" when
// it cannot; TnAwaitPending uses std.task's OpsInPoller probe, not wall time.

import std.core
import std.raw
import std.task

// TnListen listens on a loopback port the OS picks.
TnListen(io: Io): Listener {
    l := Listen(io, "127.0.0.1:0") else {
        Panic("net.Listen failed")
    }
    return l
}

// TnAddr writes "127.0.0.1:<port>" into b and returns it.
TnAddr(b: mut [32]u8, port: int): str from b {
    pre := "127.0.0.1:".AsBytes()
    (mut b[0..pre.Len()]).CopyFrom(pre)
    var d: [8]u8 = [0; 8]
    var n := 0
    var x := port
    for {
        d[n] = Trunc[u8](48 + x % 10)
        n += 1
        x = x / 10
        if x == 0 {
            break
        }
    }
    for i in 0..n {
        b[pre.Len() + i] = d[n - 1 - i]
    }
    return str.From(b[0..pre.Len() + n]).Must()
}

// TnDial is Dial, waiting out a machine that has no local port to give.
// A Windows dial binds a wildcard port before ConnectEx, and while every
// dynamic port is held (other processes' loopback churn leaves TIME_WAIT
// sockets, and they can outnumber Windows' 16,384 dynamic ports) the dial is
// AddrInUse (WSAEADDRINUSE, 10048) whatever the remote side is. So these
// blocks wait 250 ms and dial again, at most 40 times; every other answer,
// the one a block judges, is returned at once.
TnDial(io: Io, addr: str): Error!Stream {
    for _ in 0..40 {
        c := Dial(io, addr) else e {
            match e {
                AddrInUse {
                    io.NoCancel().Sleep(core.DurationOf(250_000_000)) else {
                    }
                    continue
                }
                _ {
                    fail e
                }
            }
        }
        return c
    }
    return try Dial(io, addr)
}

// TnEcho accepts one connection and echoes it until end of stream.
TnEcho(l: &Listener, io: Io, done: mut int) {
    c := l.Accept(io) else {
        *done = -1
        return
    }
    var buf: [1000]u8 = [0; 1000]
    for {
        n := c.Read(io, mut buf[..]) else {
            *done = -2
            c.Close()
            return
        }
        if n == 0 {
            break
        }
        c.WriteAll(io, buf[0..n]) else {
            *done = -3
            c.Close()
            return
        }
        *done += n
    }
    c.Close()
}

// TnAccept waits for a connection that never comes; got is 1 when the wait
// ended with Canceled.
TnAccept(l: &Listener, io: Io, got: mut int) {
    c := l.Accept(io) else e {
        match e {
            Canceled {
                *got = 1
            }
            _ {
                *got = 2
            }
        }
        return
    }
    c.Close()
    *got = 3
}

test "Listen on port 0 reports the bound port" (sys: &System) {
    l := TnListen(sys.io)
    p := l.Port()
    Assert(p > 0 && p < 65536)
    // A second listener on the same port is AddrInUse.
    var ab: [32]u8 = [0; 32]
    var got := 0
    m := Listen(sys.io, TnAddr(mut ab, p)) else e {
        match e {
            AddrInUse {
                got = 1
            }
            _ {
                got = 2
            }
        }
        l.Close()
        Assert(got == 1)
        return
    }
    m.Close()
    l.Close()
    Assert(false)
}

test "A stream echoes 1,000 lines through Writer and Reader" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var ab: [32]u8 = [0; 32]
    addr := TnAddr(mut ab, l.Port())
    var echoed := 0
    var lines := 0
    scope s := io.Scope() {
        s.go TnEcho(&l, io, mut echoed)
        c := TnDial(io, addr) else {
            Panic("net.Dial of the listener failed")
        }
        var wb: [16]u8 = [0; 16]
        var w := c.Writer(io, mut wb[..])
        var rb: [64]u8 = [0; 64]
        var r := c.Reader(io, mut rb[..])
        for i in 0..1000 {
            w.Str("a line of text\n") else {
                Assert(false)
            }
            if i % 100 == 99 {
                w.Flush() else {
                    Assert(false)
                }
                // Read back the 100 lines just sent.
                for _ in 0..100 {
                    line := r.Line(sys.arena) else {
                        Assert(false)
                        break
                    }
                    if l2 := line {
                        Assert(l2 == "a line of text")
                        lines += 1
                    }
                }
            }
        }
        c.Close()
    }
    l.Close()
    Assert(lines == 1000)
    Assert(echoed == 15_000)
}

test "A dial to a closed port is Refused" (sys: &System) {
    // A dial that connects means another process took the ephemeral port
    // between the Close and the Dial (other processes on the machine bind
    // 127.0.0.1:0); that round is retried with a fresh port, at most 8
    // times. Any error but Refused panics with the error's own text. A
    // machine with no local port to dial from answers AddrInUse, which
    // TnDial waits out.
    for _ in 0..8 {
        l := TnListen(sys.io)
        p := l.Port()
        l.Close()
        var ab: [32]u8 = [0; 32]
        c := TnDial(sys.io, TnAddr(mut ab, p)) else e {
            match e {
                Refused {
                    return
                }
                _ {
                    Panic(sys.arena.Fmt("dial of a closed port: {e}", (e)).Must())
                }
            }
        }
        c.Close()
    }
    Assert(false)
}

test "Cancelling the scope ends a blocked Accept with Canceled" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var got := 0
    scope s := io.Scope() {
        s.go TnAccept(&l, io, mut got)
        io.NoCancel().Sleep(core.DurationOf(20_000_000)) else {
        }
        s.Cancel()
    }
    l.Close()
    Assert(got == 1)
}

// TnDialRead dials port and reads from a peer that sends nothing until
// the scope is cancelled; then it reads again under NoCancel. got is 1 when
// the first Read ended with Canceled and the second one returned the single
// byte "x", which the peer writes after the cancel, so the cancelled Read
// took nothing. started is stored just before the first Read, after the
// dial's wait is over (TnReadCancel's edge), so a pending operation counted
// after TuAwaitStarted is the Read's, not the dial's. Otherwise got is 2
// when the first Read failed with another error, 3 when it returned bytes
// (a lost cancel), 4 when the dial failed, 5 when the second Read failed,
// and 6 when the second Read returned other than "x".
TnDialRead(port: int, io: Io, got: mut int, started: &raw.Atomic[u32]) {
    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, port)) else {
        *got = 4
        started.Store(1)
        return
    }
    var buf: [16]u8 = [0; 16]
    started.Store(1)
    // A byte count, or minus TnCanc's code.
    first := c.Read(io, mut buf[..]) else e {
        -TnCanc(e)
    }
    if first != -1 {
        *got = if first < 0 { 2 } else { 3 }
        c.Close()
        return
    }
    n := c.Read(io.NoCancel(), mut buf[..]) else {
        *got = 5
        c.Close()
        return
    }
    *got = if n == 1 && buf[0] == 0x78 { 1 } else { 6 }
    c.Close()
}

test "Cancelling the scope ends a blocked Read with Canceled, and the stream keeps its next bytes" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var got := 0
    started := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go TnDialRead(l.Port(), io, mut got, &started)
        p := l.Accept(io) else {
            l.Close()
            Assert(false)
            return
        }
        // The reader's dial is under the cancellable io, so its connect is
        // a blocked operation of s too until the reader resumes from it:
        // wait for started first, then for the Read to be pending in the
        // poller, so the cancel walk issues PollCancel for the Read before
        // Cancel returns and before the byte below is written.
        TuAwaitStarted(io, &started)
        if !TnAwaitPending(io, &s) {
            Panic("the reader's Read was not pending in the poller within 10 s (or its dial failed)")
        }
        io.NoCancel().Sleep(core.DurationOf(20_000_000)) else {
        }
        s.Cancel()
        // This body is in the cancelled scope too, so its write is shielded.
        p.WriteAll(io.NoCancel(), "x".AsBytes()) else {
            Assert(false)
        }
        p.Close()
    }
    l.Close()
    if got != 1 {
        Panic(sys.arena.Fmt("the reader's got {got}, want 1", (got)).Must())
    }
}

// TnReadCancel dials port, then reads from a peer that sends nothing until
// the scope is cancelled, then reads again under NoCancel; got: 1 when the
// first Read ended with Canceled and the second returned the single byte
// "x" the peer writes after the cancel (so the cancelled Read took no
// byte), 2 another failure, 3 the first Read returned bytes, 4 a failed
// dial, 5 a failed second Read. The dial is under NoCancel, so a cancel at
// once still reaches the Read. started is stored just before the first
// Read, with no suspension point between. Edge: this seq_cst store happens
// before TuAwaitStarted's load that reads 1; on one carrier, where a task
// runs until it suspends, the main task runs again only once this task has
// suspended, and its first suspension point after the store is the Read's
// wait in the poller (TuRecvCancel's argument). done is stored once the
// first Read returned, whatever it returned. Edge: this seq_cst store
// happens before TnAwaitDone's load that reads 1.
TnReadCancel(port: int, io: Io, got: mut int, started: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    var ab: [32]u8 = [0; 32]
    c := TnDial(io.NoCancel(), TnAddr(mut ab, port)) else {
        *got = 4
        done.Store(1)
        return
    }
    var buf: [16]u8 = [0; 16]
    started.Store(1)
    first := c.Read(io, mut buf[..]) else e {
        -TnCanc(e)
    }
    done.Store(1)
    if first != -1 {
        *got = if first < 0 { 2 } else { 3 }
        c.Close()
        return
    }
    n := c.Read(io.NoCancel(), mut buf[..]) else {
        *got = 5
        c.Close()
        return
    }
    *got = if n == 1 && buf[0] == 0x78 { 1 } else { 5 }
    c.Close()
}

// TnAcceptCancel waits in an Accept that no dial answers, storing started
// just before it as TnReadCancel does; got: 1 Canceled, 2 another failure,
// 3 a connection.
TnAcceptCancel(l: &Listener, io: Io, got: mut int, started: &raw.Atomic[u32]) {
    started.Store(1)
    c := l.Accept(io) else e {
        *got = TnCanc(e)
        return
    }
    c.Close()
    *got = 3
}

// TnAwaitPending yields until a task blocked in s has its poller operation
// submitted (std.task's OpsInPoller probe) and is true, or is false once
// 10 s have passed; a caller whose premise is the pending operation
// panics on false. A cancel of s from then on finds the operation live,
// so the cancel walk has issued PollCancel for it before Cancel returns.
// OpsInPoller counts any blocked operation of s, so a caller that also
// has a cancellable dial in s waits for the task's started flag first.
// Without the probe, "pending" would be a guess from wall time, which a
// loaded machine can outlast.
TnAwaitPending(io: Io, s: &Scope): bool {
    t0 := TuNow()
    for task.OpsInPoller(s) == 0 {
        if TuNow() - t0 >= 10_000_000_000 {
            return false
        }
        io.NoCancel().Yield() else {
        }
    }
    return true
}

// TnAwaitDone yields until done reads non-zero (TnReadCancel's edge), or
// until 10 s have passed, so a lost cancel shows as the reader's got
// instead of a hang.
TnAwaitDone(io: Io, done: &raw.Atomic[u32]) {
    t0 := TuNow()
    for done.Load() == 0 && TuNow() - t0 < 10_000_000_000 {
        io.NoCancel().Yield() else {
        }
    }
}

// TnReadRound and TnAcceptRound cancel one Read or one Accept in the way
// kind says, as TuCancelRound does for a Recv: 0 at once (at one carrier
// before the task first runs, so the call's entry check sees the cancel),
// 1 once the task has started (at one carrier it is then waiting in the
// poller, so the cancel walk ends a pending operation; at more carriers the
// cancel may land anywhere from the call's entry to its wait), 2 once its
// operation is pending in the poller (TnAwaitPending) and 1 ms later. The
// verdict is the task's got: 1.
//
// A Read's cancel races the peer's byte unless the cancel walk itself
// cancels the pending operation: a cancel that lands after the Read
// registered its wait but before the poller held the operation leaves the
// cancel to the reader (the Dekker pair in std.task's AwaitIn), and a byte
// that arrives first is the Read's result (an operation the OS completes
// with data returns that data). So at kinds 0
// and 1 the peer writes only once the first Read has returned, and Canceled
// is the only right end wherever the cancel landed; at kind 2 the operation
// is pending when the cancel starts, and the peer writes as soon as Cancel
// returns, so the cancelled Read must also have taken no byte. An Accept
// that no dial answers has no such race.
TnReadRound(io: Io, kind: int): int {
    l := TnListen(io)
    var got := 0
    started := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go TnReadCancel(l.Port(), io, mut got, &started, &done)
        if kind == 0 {
            s.Cancel()
        }
        p := l.Accept(io.NoCancel()) else {
            Panic("Accept of the reader's dial failed")
        }
        if kind >= 1 {
            TuAwaitStarted(io, &started)
            if kind == 2 {
                if !TnAwaitPending(io, &s) {
                    Panic("TnReadRound kind 2: the Read was not pending in the poller within 10 s")
                }
                io.NoCancel().Sleep(core.DurationOf(1_000_000)) else {
                }
            }
            s.Cancel()
        }
        if kind <= 1 {
            TnAwaitDone(io, &done)
        }
        // This body is in the cancelled scope too, so its write is shielded.
        p.WriteAll(io.NoCancel(), "x".AsBytes()) else {
            Panic("WriteAll to the reader failed")
        }
        p.Close()
    }
    l.Close()
    return got
}

TnAcceptRound(io: Io, kind: int): int {
    l := TnListen(io)
    var got := 0
    started := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go TnAcceptCancel(&l, io, mut got, &started)
        if kind >= 1 {
            TuAwaitStarted(io, &started)
        }
        if kind == 2 {
            if !TnAwaitPending(io, &s) {
                Panic("TnAcceptRound kind 2: the Accept was not pending in the poller within 10 s")
            }
            io.NoCancel().Sleep(core.DurationOf(1_000_000)) else {
            }
        }
        s.Cancel()
    }
    l.Close()
    return got
}

test "A Read and an Accept end with Canceled when cancelled at entry, while waiting in the poller (controlled at one carrier) and late" (sys: &System) {
    for round in 0..60 {
        k := round % 3
        r := TnReadRound(sys.io, k)
        if r != 1 {
            Panic(sys.arena.Fmt("TnReadRound round {round} kind {k}: got {r}, want 1", (round, k, r)).Must())
        }
        a := TnAcceptRound(sys.io, k)
        if a != 1 {
            Panic(sys.arena.Fmt("TnAcceptRound round {round} kind {k}: got {a}, want 1", (round, k, a)).Must())
        }
    }
}

// TnFlightByte is byte i of an in-flight payload.
TnFlightByte(i: int): u8 {
    return Trunc[u8]((i * 7 + 1) & 255)
}

// TnFlightRead dials port and reads an n-byte payload that the peer writes
// while main cancels the scope: the first Read under io, which returns
// bytes or Canceled, then Reads under NoCancel to the end of the stream.
// got: 1 when the bytes read are the payload exactly, none lost and none
// read twice; 2 another count or byte; 4 a failed dial; 5 a failed later
// Read. started is stored just before the first Read (TnReadCancel's edge).
TnFlightRead(port: int, io: Io, n: int, got: mut int, started: &raw.Atomic[u32]) {
    var ab: [32]u8 = [0; 32]
    c := TnDial(io.NoCancel(), TnAddr(mut ab, port)) else {
        *got = 4
        return
    }
    var buf: [128]u8 = [0; 128]
    started.Store(1)
    // Canceled takes no byte; any other failure shows in the reads below.
    var k := c.Read(io, mut buf[0..64]) else {
        0
    }
    for k < buf.Len() {
        m := c.Read(io.NoCancel(), mut buf[k..]) else {
            *got = 5
            c.Close()
            return
        }
        if m == 0 {
            break
        }
        k += m
    }
    c.Close()
    var same := k == n
    for i in 0..k {
        if same && buf[i] != TnFlightByte(i) {
            same = false
        }
    }
    *got = if same { 1 } else { 2 }
}

// TnFlightRound writes n payload bytes to the reader with no delay around
// the scope's cancel, before it when early, else after it: the cancel
// lands while the bytes are in flight to the Read, or just before or after
// them. The verdict is the reader's got: 1.
TnFlightRound(io: Io, n: int, early: bool): int {
    l := TnListen(io)
    var got := 0
    started := raw.Atomic[u32].New(0)
    var pay: [64]u8 = [0; 64]
    for i in 0..n {
        pay[i] = TnFlightByte(i)
    }
    scope s := io.Scope() {
        s.go TnFlightRead(l.Port(), io, n, mut got, &started)
        p := l.Accept(io.NoCancel()) else {
            Panic("Accept of the reader's dial failed")
        }
        TuAwaitStarted(io, &started)
        if early {
            s.Cancel()
        }
        // This body is in the cancelled scope, so its write is shielded.
        p.WriteAll(io.NoCancel(), pay[0..n]) else {
            Panic("WriteAll to the reader failed")
        }
        if !early {
            s.Cancel()
        }
        p.Close()
    }
    l.Close()
    return got
}

test "A Read cancelled while its bytes arrive returns them or Canceled, and the stream loses none and repeats none" (sys: &System) {
    for round in 0..200 {
        n := if round % 2 == 0 { 1 } else { 37 }
        Assert(TnFlightRound(sys.io, n, round % 4 < 2) == 1)
    }
}

test "IPv6 loopback listens on port 0, dials and echoes" (sys: &System) {
    io := sys.io
    l := Listen(io, "[::1]:0") else {
        Panic("net.Listen failed")
    }
    p := l.Port()
    Assert(p > 0 && p < 65536)
    addr := sys.arena.Fmt("[::1]:{p}", (p)).Must()
    var echoed := 0
    scope s := io.Scope() {
        s.go TnEcho(&l, io, mut echoed)
        c := TnDial(io, addr) else {
            Panic("net.Dial of the listener failed")
        }
        c.WriteAll(io, "over v6".AsBytes()) else {
            Assert(false)
        }
        var back: [7]u8 = [0; 7]
        var got := 0
        for got < 7 {
            n := c.Read(io, mut back[got..]) else {
                break
            }
            if n == 0 {
                break
            }
            got += n
        }
        Assert(got == 7 && back[0] == 0x6F && back[6] == 0x36)
        c.Close()
    }
    l.Close()
    Assert(echoed == 7)
}

// TnCanc is 1 for Canceled, 2 for any other error.
TnCanc(e: Error): int {
    return match e {
        Canceled { 1 }
        _ { 2 }
    }
}

// TnEmpty makes call k of c with nothing to move: 0 Read, 1 Write, 2
// WriteAll. 0 on success, else TnCanc.
TnEmpty(c: &Stream, io: Io, k: int): int {
    var empty: [0]u8 = []
    if k == 0 {
        _ = c.Read(io, mut empty[..]) else e {
            return TnCanc(e)
        }
    } else if k == 1 {
        _ = c.Write(io, empty[..]) else e {
            return TnCanc(e)
        }
    } else {
        c.WriteAll(io, empty[..]) else e {
            return TnCanc(e)
        }
    }
    return 0
}

test "a Stream call with nothing to move is Canceled in a cancelled scope" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, l.Port())) else {
        l.Close()
        Assert(false)
        return
    }
    var got: [3]int = [9; 3]
    var shielded: [3]int = [9; 3]
    scope s := io.Scope() {
        s.Cancel()
        for k in 0..3 {
            got[k] = TnEmpty(&c, io, k)
            shielded[k] = TnEmpty(&c, io.NoCancel(), k)
        }
    }
    for k in 0..3 {
        Assert(got[k] == 1)
        Assert(shielded[k] == 0)
        Assert(TnEmpty(&c, io, k) == 0)
    }
    c.Close()
    l.Close()
}

// TnBindBad is 0 when Bind of an address that does not parse is
// InvalidAddress, else TnCanc of its error (3 when it succeeds).
TnBindBad(io: Io): int {
    d := Bind(io, "no address") else e {
        return match e {
            InvalidAddress { 0 }
            _ { TnCanc(e) }
        }
    }
    d.Close()
    return 3
}

// An Io over core.InertIo: its sleep entry fails with Other, so its
// Sleep(0), the check every call with Canceled in its set makes first, fails
// with something other than Canceled. That is not a cancellation:
// the call goes on, here to its result with nothing to move, and Bind to
// InvalidAddress. Its NoCancel form goes on the same way.
test "a cancellation check whose Sleep(0) fails with another error is not a cancellation, masked or not" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, l.Port())) else {
        l.Close()
        Assert(false)
        return
    }
    inert := core.MakeIo(raw.Of(&core.InertIo), 0)
    var plain: [3]int = [9; 3]
    var masked: [3]int = [9; 3]
    var inside: [3]int = [9; 3]
    for k in 0..3 {
        plain[k] = TnEmpty(&c, inert, k)
        masked[k] = TnEmpty(&c, inert.NoCancel(), k)
    }
    bad := TnBindBad(inert)
    bad_masked := TnBindBad(inert.NoCancel())
    scope s := io.Scope() {
        s.Cancel()
        for k in 0..3 {
            inside[k] = TnEmpty(&c, inert, k)
        }
    }
    for k in 0..3 {
        Assert(plain[k] == 0)
        Assert(masked[k] == 0)
        Assert(inside[k] == 0)
    }
    Assert(bad == 0)
    Assert(bad_masked == 0)
    c.Close()
    l.Close()
}

// The scope tree at net's cancellation check: a scope opened from a
// NoCancel Io belongs to that Io, so its own Cancel cancels the calls made
// under it with the masked Io; the cancelled scope around the NoCancel call
// does not.
test "a scope opened from a NoCancel Io cancels net's check under it, and the scope around the mask does not" (sys: &System) {
    io := sys.io
    l := TnListen(io)
    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, l.Port())) else {
        l.Close()
        Assert(false)
        return
    }
    var outer: [3]int = [9; 3]
    var own: [3]int = [9; 3]
    scope s := io.Scope() {
        s.Cancel()
        m := io.NoCancel()
        for k in 0..3 {
            outer[k] = TnEmpty(&c, m, k)
        }
        scope b := m.Scope() {
            b.Cancel()
            for k in 0..3 {
                own[k] = TnEmpty(&c, m, k)
            }
        }
    }
    for k in 0..3 {
        Assert(outer[k] == 0)
        Assert(own[k] == 1)
    }
    c.Close()
    l.Close()
}

test "Listener.Addr reports bound IPv4 and IPv6 loopback addresses and Stream.SetBuffers sets sizes" (sys: &System) {
    io := sys.io
    l4 := TnListen(io)
    defer l4.Close()
    a4 := l4.Addr()
    Assert(!a4.v6 && int(a4.port) == l4.Port())
    Assert(a4.ip[0] == 127 && a4.ip[3] == 1)

    l6 := Listen(io, "[::1]:0") else {
        Assert(false)
        return
    }
    defer l6.Close()
    a6 := l6.Addr()
    Assert(a6.v6 && int(a6.port) == l6.Port() && a6.ip[15] == 1)

    var ab: [32]u8 = [0; 32]
    c := TnDial(io, TnAddr(mut ab, l4.Port())) else {
        Assert(false)
        return
    }
    defer c.Close()
    s := l4.Accept(io) else {
        Assert(false)
        return
    }
    defer s.Close()

    try c.SetBuffers(32768, 32768)
    try c.SetBuffers(0, 16384)
    try s.SetBuffers(16384, 0)
    var bad := false
    c.SetBuffers(-1, 1024) else err {
        match err {
            Os(code: _) { bad = true }
            _ {}
        }
    }
    Assert(bad)

    var msg: [4]u8 = [1, 2, 3, 4]
    try c.WriteAll(io, msg[..])
    var rbuf: [8]u8 = [0; 8]
    n := try s.Read(io, mut rbuf[..])
    Assert(n == 4 && rbuf[0] == 1 && rbuf[3] == 4)
}
