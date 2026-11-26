// Why: Supervise must restart workers with its backoff, end them all on a
// cancel, and leave no server behind when the parent fails.
// What: test blocks over SuperviseOn and a raw runtime listener (PtListener),
// the last over the exported Supervise and net.Listen.
// How: workers are this test executable again (MK_INHERIT set) and the parent
// drives them as a client; every child ends itself after 60 s.

import std.core
import std.net

// PtListener is a raw runtime listening socket, the L of SuperviseOn.
struct PtListener {
    h: uint
}

error PtStop

// PtSupName is the name of what SuperviseOn failed with.
PtSupName(e: PtStop | Error): str {
    return match e {
        PtStop { "PtStop" }
        NotFound { "NotFound" }
        Denied { "Denied" }
        TooLarge { "TooLarge" }
        Os(code: _) { "Os" }
        Canceled { "Canceled" }
        OutOfMemory { "OutOfMemory" }
        Unsupported { "Unsupported" }
    }
}

// PtAddr writes "127.0.0.1:<port>" into b.
PtAddr(b: mut [32]u8, port: i64): str from b {
    pre := "127.0.0.1:".AsBytes()
    (mut b[0..pre.Len()]).CopyFrom(pre)
    var d: [8]u8 = [0; 8]
    var n := 0
    var x := Trunc[int](port)
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

// PtDialWait is core.IoDial, waiting out a machine that has no local port to
// give: while every dynamic port is held (other processes' loopback churn
// leaves TIME_WAIT sockets, and they can outnumber Windows' 16,384 dynamic
// ports) a Windows dial is AddrInUse (WSAEADDRINUSE, 10048) whatever the
// remote side is, so it waits 250 ms and dials again, at most 40 times. Every
// other answer is returned at once.
PtDialWait(io: Io, addr: str): core.IoResult {
    for _ in 0..40 {
        d := core.IoDial(io, addr)
        if d.err != core.SysErr.AddrInUse {
            return d
        }
        io.NoCancel().Sleep(core.DurationOf(250_000_000)) else {
        }
    }
    return core.IoDial(io, addr)
}

// PrDial is net.Dial with PtDialWait's wait.
PrDial(io: Io, addr: str): net.Error!net.Stream {
    for _ in 0..40 {
        c := net.Dial(io, addr) else e {
            match e {
                net.AddrInUse {
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
    return try net.Dial(io, addr)
}

// PtSendAll sends every byte of data on s.
PtSendAll(io: Io, s: uint, data: []u8): bool {
    var off := 0
    for off < data.Len() {
        w := core.IoSend(io, s, data[off..])
        if w.err != core.SysErr.None || w.value <= 0 || Trunc[int](w.value) > data.Len() - off {
            return false
        }
        off += Trunc[int](w.value)
    }
    return true
}

// PtDial dials port: the stream, 0 on a failure, counted in bad.
PtDial(io: Io, port: i64, bad: mut PtBad): uint {
    var ab: [32]u8 = [0; 32]
    d := PtDialWait(io, PtAddr(mut ab, port))
    if d.err == core.SysErr.Canceled {
        bad.canceled += 1
        return 0
    }
    if d.err != core.SysErr.None {
        bad.dial += 1
        bad.os = d.os
        return 0
    }
    return Trunc[uint](d.value)
}

// PtQuery sends cmd on s and reads the 8-byte answer: the identity in its
// first seven bytes (never 0: the worker sets bit 0 of byte 6), 0 on a
// failure, counted in bad.
PtQuery(io: Io, s: uint, cmd: u8, bad: mut PtBad): i64 {
    var q: [1]u8 = [cmd]
    var got: [8]u8 = [0; 8]
    var n := 0
    if PtSendAll(io, s, q[..]) {
        for n < 8 {
            r := core.IoRecv(io, s, mut got[n..])
            if r.err != core.SysErr.None || r.value <= 0 {
                if r.err == core.SysErr.Canceled {
                    bad.canceled += 1
                    return 0
                }
                bad.os = r.os
                break
            }
            n += Trunc[int](r.value)
        }
    }
    if n != 8 {
        bad.short += 1
        return 0
    }
    var v: i64 = 0
    for i in 0..7 {
        v = v * 256 + i64(got[i])
    }
    return v
}

// PtAsk dials port, asks cmd and closes: the identity, or 0 (counted).
// After the answer it reads to the worker's close before its own, so the
// worker closes first and the TIME_WAIT lands on the worker's end (the
// listener's port), not on this client's ephemeral port: closing first
// here left one client TIME_WAIT per ask, about 10,000 for one run of
// these blocks, and four runs at once used up Windows' 16,384 dynamic
// ports.
PtAsk(io: Io, port: i64, cmd: u8, bad: mut PtBad): i64 {
    s := PtDial(io, port, mut *bad)
    if s == 0 {
        return 0
    }
    v := PtQuery(io, s, cmd, mut *bad)
    if v != 0 {
        var sink: [16]u8 = [0; 16]
        for {
            r := core.IoRecv(io, s, mut sink[..])
            if r.err != core.SysErr.None || r.value <= 0 {
                break
            }
        }
    }
    _ = core.IoClose(io, s)
    return v
}

// What went wrong for a client: cancelled calls, failed dials, short or
// missing answers, and the last OS code.
struct PtBad {
    canceled: int
    dial: int
    short: int
    os: i64
}

// PtServe answers each connection of l with the worker's identity id, one
// connection at a time: 'h' gets the answer and is held until the client
// closes it, so a worker holding one serves nobody else (three holds reach
// three workers, whatever order the OS hands accepts out in: Windows and
// Linux both favour the latest waiter); 'p' gets the answer and then a
// panic, which ends this process (101). A failed accept ends it with 3.
PtServe(io: Io, l: uint, id: [8]u8) {
    for {
        a := core.IoAccept(io, l)
        if a.err != core.SysErr.None {
            Exit(3)
        }
        s := Trunc[uint](a.value)
        var q: [1]u8 = [0]
        r := core.IoRecv(io, s, mut q[..])
        if r.err == core.SysErr.None && r.value == 1 {
            _ = PtSendAll(io, s, id[..])
            if q[0] == 112 {
                Panic("raw worker panic on request")
            }
            if q[0] == 104 {
                var sink: [16]u8 = [0; 16]
                for {
                    w := core.IoRecv(io, s, mut sink[..])
                    if w.err != core.SysErr.None || w.value <= 0 {
                        break
                    }
                }
            }
        }
        _ = core.IoClose(io, s)
    }
}

// PtWorker is a serving worker: a random identity (bit 0 set, so never
// 0), served until the process is killed or panics; after 60 s it ends
// itself with 4.
PtWorker(sys: &System, l: &PtListener): PtStop!void {
    var id: [8]u8 = [0; 8]
    sys.io.Random(mut id[..]) else {
        fail PtStop
    }
    id[6] = id[6] | 1
    scope s := sys.io.Scope() {
        s.go PtServe(sys.io, l.h, id)
        sys.io.Sleep(core.DurationOf(60_000_000_000)) else {
            Exit(5)
        }
        Exit(4)
    }
}

// PtOnce is a worker that serves one connection and returns, so its
// process exits 0 and the parent restarts it (the backoff block).
PtOnce(sys: &System, l: &PtListener): PtStop!void {
    a := core.IoAccept(sys.io, l.h)
    if a.err != core.SysErr.None {
        fail PtStop
    }
    s := Trunc[uint](a.value)
    var q: [1]u8 = [0]
    _ = core.IoRecv(sys.io, s, mut q[..])
    var id: [8]u8 = [0, 0, 0, 0, 0, 0, 1, 0]
    _ = PtSendAll(sys.io, s, id[..])
    _ = core.IoClose(sys.io, s)
}

// PtListen is the block's listener: the inherited one in a child, else a
// new one on a loopback port the OS picks; port is 0 in a child.
PtListen(io: Io, port: mut i64): PtListener {
    i := core.IoInherited(io)
    if i.err == core.SysErr.None {
        return PtListener(h: Trunc[uint](i.value))
    }
    Assert(i.err == core.SysErr.NotFound)
    r := core.IoListen(io, "127.0.0.1:0")
    Assert(r.err == core.SysErr.None && r.value != 0 && r.os > 0)
    *port = r.os
    return PtListener(h: Trunc[uint](r.value))
}

// PtGone closes the parent's listener and dials its port: Refused shows
// that no child holds the socket any more, so the whole tree has ended.
PtGone(io: Io, l: PtListener, port: i64) {
    _ = core.IoClose(io, l.h)
    var ab: [32]u8 = [0; 32]
    d := PtDialWait(io, PtAddr(mut ab, port))
    Assert(d.err == core.SysErr.Refused)
}

// PtHas: x is one of the first n ids.
PtHas(ids: &[8]i64, n: int, x: i64): bool {
    for i in 0..n {
        if ids[i] == x {
            return true
        }
    }
    return false
}

// PtHold3 holds one connection with each of the three workers: their
// identities in ids (0 where an answer failed); the streams in ss, for
// PtRelease.
PtHold3(io: Io, port: i64, ids: mut [8]i64, ss: mut [3]uint, bad: mut PtBad) {
    for i in 0..3 {
        ss[i] = PtDial(io, port, mut *bad)
        ids[i] = 0
        if ss[i] != 0 {
            ids[i] = PtQuery(io, ss[i], 104, mut *bad)
        }
    }
}

PtRelease(io: Io, ss: &[3]uint) {
    for i in 0..3 {
        if ss[i] != 0 {
            _ = core.IoClose(io, ss[i])
        }
    }
}

// PtDistinct3: the first three ids are non-zero and distinct.
PtDistinct3(ids: &[8]i64): bool {
    return ids[0] != 0 && ids[1] != 0 && ids[2] != 0 && ids[0] != ids[1] && ids[0] != ids[2] && ids[1] != ids[2]
}

// The client's findings in the panic block.
struct PtSeen {
    before: [8]i64
    after: [8]i64
    panicked: i64
    asks: int
    stale: int
    bad: PtBad
}

// PtClient drives three workers: three holds give the three identities;
// one worker is made to panic; 200 asks follow while it restarts, every
// one to be answered and none by the dead worker; three holds again give
// the two survivors and one new identity, the restarted worker. Then it
// cancels s.
PtClient(s: &Scope, io: Io, port: i64, r: mut PtSeen) {
    var ss: [3]uint = [0; 3]
    PtHold3(io, port, mut r.before, mut ss, mut r.bad)
    PtRelease(io, &ss)
    r.panicked = PtAsk(io, port, 112, mut r.bad)
    for _ in 0..200 {
        v := PtAsk(io, port, 105, mut r.bad)
        r.asks += 1
        if v == r.panicked {
            r.stale += 1
        }
    }
    PtHold3(io, port, mut r.after, mut ss, mut r.bad)
    PtRelease(io, &ss)
    s.Cancel()
}

// PtNew is how many of after's three ids are not in before's three.
PtNew(before: &[8]i64, after: &[8]i64): int {
    var n := 0
    for i in 0..3 {
        if !PtHas(before, 3, after[i]) {
            n += 1
        }
    }
    return n
}

test "Supervise: a worker's panic restarts that worker while the others keep serving; cancel ends the tree" (sys: &System) {
    var port: i64 = 0
    l := PtListen(sys.io, mut port)
    if SupervisedChild(sys) {
        SuperviseOn[PtStop, PtListener](sys, 3, l.h, &l, PtWorker) else {
            Exit(6)
        }
        Exit(7)
    }
    var r := PtSeen(before: [0; 8], after: [0; 8], panicked: 0, asks: 0, stale: 0, bad: PtBad(canceled: 0, dial: 0, short: 0, os: 0))
    var got := ""
    t0 := Now(sys.io)
    scope s := sys.io.Scope() {
        s.go PtClient(&s, sys.io, port, mut r)
        SuperviseOn[PtStop, PtListener](sys, 3, l.h, &l, PtWorker) else e {
            got = PtSupName(e)
        }
    }
    var out := sys.Stdout()
    nw := PtNew(&r.before, &r.after)
    bd := r.bad.dial
    bs := r.bad.short
    bc := r.bad.canceled
    bo := r.bad.os
    st := r.stale
    _ = Print(mut out, "new {nw} dial {bd} short {bs} canceled {bc} os {bo} stale {st}\n", (nw, bd, bs, bc, bo, st)) else {
    }
    _ = out.Flush() else {
    }
    Assert(got == "Canceled")
    // Three workers before; one of them panicked; every ask answered, none
    // by the dead one; three workers after, one of them new.
    Assert(PtDistinct3(&r.before) && PtHas(&r.before, 3, r.panicked))
    Assert(r.bad.dial == 0 && r.bad.short == 0 && r.bad.canceled == 0 && r.stale == 0 && r.asks == 200)
    Assert(PtDistinct3(&r.after) && !PtHas(&r.after, 3, r.panicked) && nw == 1)
    Assert(Now(sys.io) - t0 < 60_000_000_000)
    PtGone(sys.io, l, port)
}

// PtTimer cancels s after d nanoseconds.
PtTimer(s: &Scope, io: Io, d: i64) {
    _ = io.Sleep(core.DurationOf(d)) else {
    }
    s.Cancel()
}

// PtCount asks port until canceled or, during startup, the first reply.
PtCount(io: Io, port: i64, n: mut int, first: bool) {
    for {
        var bad := PtBad(canceled: 0, dial: 0, short: 0, os: 0)
        v := PtAsk(io, port, 105, mut bad)
        if bad.canceled > 0 {
            return
        }
        if v > 0 {
            *n += 1
            if first {
                return
            }
        }
        // 1 ms between asks: the block needs one served ask, and an
        // unpaced loop made thousands of connections a round.
        io.Sleep(core.DurationOf(1_000_000)) else {
            return
        }
    }
}

// PtServing waits for a real reply under a startup deadline, then starts
// the separate serving window. The parent checks for zero replies after
// cancellation and cleanup so a watchdog expiry cannot pass as success.
PtServing(s: &Scope, io: Io, port: i64, d: i64, n: mut int) {
    scope startup := io.Scope() {
        startup.CancelAfter(core.DurationOf(20_000_000_000))
        PtCount(io, port, mut *n, true)
    }
    if *n == 0 {
        s.Cancel()
        return
    }
    s.CancelAfter(core.DurationOf(d))
    PtCount(io, port, mut *n, false)
}

// The worker serves one ask and exits. The block reads Slot's trace
// (supervise.mk's SlotTrace) through six lives and the decision after the
// sixth, then cancels: PtBackoffCheck says what it proves. Counting answers
// in a fixed 3.5 s would fail when four lives took more than 3.5 s
// (reproduced by planting latency; no such failure was recorded).
test "Supervise: a worker that keeps exiting is restarted three times at once, then with a doubling delay" (sys: &System) {
    var port: i64 = 0
    l := PtListen(sys.io, mut port)
    if SupervisedChild(sys) {
        SuperviseOn[PtStop, PtListener](sys, 1, l.h, &l, PtOnce) else {
            Exit(6)
        }
        // The worker served one connection: this child ends, 0.
        return
    }
    var n := 0
    var bad := PtBad(canceled: 0, dial: 0, short: 0, os: 0)
    var got := ""
    // Edge: stored before SuperviseOn starts the slot's task, so the slot
    // logs from its first start (SlotTraced).
    SlotTrace.armed.Store(1)
    t0 := Now(sys.io)
    scope s := sys.io.Scope() {
        s.go PtTimer(&s, sys.io, PtLivesLimit)
        s.go PtLives(&s, sys.io, port, 6, PtLivesEvents, mut n, mut bad)
        SuperviseOn[PtStop, PtListener](sys, 1, l.h, &l, PtOnce) else e {
            got = PtSupName(e)
        }
    }
    took := Now(sys.io) - t0
    // Edge: after the scope's join, which orders the slot's last store
    // before these loads.
    SlotTrace.armed.Store(0)
    var g := SlotTrace.log.Lock(sys.io.NoCancel()) else {
        Panic("the trace's lock failed")
    }
    // Edge: n is stored under the lock held here.
    m := Trunc[int](SlotTrace.n.Load())
    k := g.Ref().kind
    at := g.Ref().at
    arg := g.Ref().arg
    g.Unlock()
    rep := PtTraceText(sys.arena, &k, &at, &arg, m)
    var out := sys.Stdout()
    bd := bad.dial
    bs := bad.short
    _ = Print(mut out, "asks {n} dial {bd} short {bs} took {took} trace{rep}\n", (n, bd, bs, took, rep)) else {
    }
    _ = out.Flush() else {
    }
    if got != "Canceled" {
        Panic(sys.arena.Fmt("SuperviseOn ended with {got}; trace{rep}", (got, rep)).Must())
    }
    // Six lives, each serving one ask on the inherited listener, and no
    // short answer. A failed dial is reported, not judged: it reaches no
    // life (PtDialWait's AddrInUse after its 40 waits, on a machine whose
    // dynamic ports TIME_WAIT holds), and PtLives asks again.
    if n != 6 || bad.short != 0 {
        Panic(sys.arena.Fmt("asks {n} short {bs}, want 6 0 (dial {bd}); trace{rep}", (n, bs, bd, rep)).Must())
    }
    why := PtBackoffCheck(sys.arena, &k, &at, &arg, m)
    if why != "" {
        Panic(sys.arena.Fmt("{why}; trace{rep}", (why, rep)).Must())
    }
    Assert(took < 60_000_000_000)
    PtGone(sys.io, l, port)
}

// The backoff block's bounds: a watchdog well inside the harnesses' 60 s
// per block, so a defect is reported with its trace and not as a block
// with no exit; and the events of six lives: the first start and exit,
// then a decision, a start and an exit per restart, then the decision
// after the sixth exit.
const PtLivesLimit: i64 = 40_000_000_000
const PtLivesEvents: i64 = 18

// PtLives asks port until want asks were answered, then waits until the
// slot's trace holds events events and cancels s. A cancel (the
// watchdog's) ends it early. Asks keep 1 ms apart, as PtCount's.
PtLives(s: &Scope, io: Io, port: i64, want: int, events: i64, n: mut int, bad: mut PtBad) {
    for *n < want {
        v := PtAsk(io, port, 105, mut *bad)
        if bad.canceled > 0 {
            return
        }
        if v > 0 {
            *n += 1
        }
        io.Sleep(core.DurationOf(1_000_000)) else {
            return
        }
    }
    // Edge: the slot's store of n follows its stores of the event's
    // fields; this load only decides when to cancel.
    for SlotTrace.n.Load() < events {
        io.Sleep(core.DurationOf(1_000_000)) else {
            return
        }
    }
    s.Cancel()
}

// PtTraceText is the trace as " kind@us:arg" per event, us the
// microseconds since the first event, for the block's report.
PtTraceText(a: &Arena, k: &[SlotTraceMax]i64, at: &[SlotTraceMax]i64, arg: &[SlotTraceMax]i64, m: int): str from a {
    var t := a.Fmt(" ({m} events)", (m)).Must()
    for i in 0..m {
        kind := k[i]
        us := (at[i] - at[0]) / 1000
        x := arg[i]
        t = a.Fmt("{t} {kind}@{us}:{x}", (t, kind, us, x)).Must()
    }
    return t
}

// PtBackoffCheck is "" when the trace is that of six lives restarted three
// times at once and then with a doubling delay, else what differs. It
// reads only what Slot decided and the clock readings Slot took around
// each decision, so the pace of the workers and of the machine does not
// move it:
//   - each event is the one that order asks for (no start or exit missing,
//     no failed spawn), and every life exited with 0, having served;
//   - the delays Backoff.Next gave are 0, 0, 0, then BackoffFirst
//     doubling: 1 s, 2 s and 4 s;
//   - each decision is taken within BackoffFirst / 2 of the exit before
//     it. Between the two readings Slot only closes the handle (a call
//     that does not suspend) and loops, so only a slot that waited about
//     a backoff after an exit can reach it;
//   - a delayed restart starts at least its delay after the decision
//     (Io.Sleep's "at least d"), so the delay was slept;
//   - an immediate restart starts within BackoffFirst / 2 of its decision.
//     Nothing between the two readings suspends the slot (no sleep, no
//     wait), so only a restart that waited about a backoff can reach it.
// The limit, the trade the block makes for stability under load: nothing is judged
// from a start note to the exit note after it, which holds the spawn call,
// the life, the child's exit and IoProcWait's return. So a Slot that
// stalls after its wait returns, before it reads the exit (a 1.5 s stall
// there), passes, as a slow spawn does; no bound
// there could tell it from a slow wait under load. Nor is a delayed
// restart's start bounded above (a Slot that oversleeps its backoff
// passes).
PtBackoffCheck(a: &Arena, k: &[SlotTraceMax]i64, at: &[SlotTraceMax]i64, arg: &[SlotTraceMax]i64, m: int): str from a {
    if m < 18 {
        return a.Fmt("{m} events, want at least 18", (m)).Must()
    }
    if k[0] != SlotStart || k[1] != SlotExited || arg[1] != 0 {
        return "life 1 is not a start and an exit with 0"
    }
    var want: [6]i64 = [0, 0, 0, 1, 2, 4]
    for j in 0..6 {
        i := 2 + 3 * j
        if k[i] != SlotDecided || arg[i] != want[j] * BackoffFirst {
            r := j + 1
            return a.Fmt("event {i} is not restart {r}'s decision with its delay", (i, r)).Must()
        }
        // Event i - 1 is the exit before this decision: index 1 when j is
        // 0, else checked as SlotExited in the iteration before.
        late := at[i] - at[i - 1]
        if late >= BackoffFirst / 2 {
            r := j + 1
            return a.Fmt("restart {r} was decided {late} ns after the exit before it", (r, late)).Must()
        }
        if j == 5 {
            break
        }
        if k[i + 1] != SlotStart || k[i + 2] != SlotExited || arg[i + 2] != 0 {
            l := j + 2
            return a.Fmt("events {i} on are not life {l}'s start and exit with 0", (i, l)).Must()
        }
        gap := at[i + 1] - at[i]
        if arg[i] == 0 && gap >= BackoffFirst / 2 {
            r := j + 1
            return a.Fmt("restart {r} is at once but started {gap} ns after its decision", (r, gap)).Must()
        }
        if arg[i] > 0 && gap < arg[i] {
            r := j + 1
            return a.Fmt("restart {r} started {gap} ns after its decision, before its delay", (r, gap)).Must()
        }
    }
    return ""
}

test "Supervise: a cancel at 0, 1, 20 and 200 ms ends every worker before SuperviseOn returns" (sys: &System) {
    var port: i64 = 0
    l := PtListen(sys.io, mut port)
    if SupervisedChild(sys) {
        SuperviseOn[PtStop, PtListener](sys, 4, l.h, &l, PtWorker) else {
            Exit(6)
        }
        Exit(7)
    }
    var last := l
    var lport := port
    for k, d in [0, 1_000_000, 20_000_000, 200_000_000] {
        if k > 0 {
            last = PtListen(sys.io, mut lport)
        }
        var got := ""
        t0 := Now(sys.io)
        scope s := sys.io.Scope() {
            s.go PtTimer(&s, sys.io, i64(d))
            SuperviseOn[PtStop, PtListener](sys, 4, last.h, &last, PtWorker) else e {
                got = PtSupName(e)
            }
        }
        Assert(got == "Canceled" && Now(sys.io) - t0 < 20_000_000_000)
        PtGone(sys.io, last, lport)
    }
}

// A failed first start is SuperviseOn's error: a directory handle is no
// listener, so the runtime's spawn refuses to pass it on (Other, Os(0)
// here) before any child starts; every slot fails at once and the scope
// joins. No child ever runs this block.
test "Supervise: a failed first start of a worker is SuperviseOn's error, and nothing keeps running" (sys: &System) {
    d := core.IoOpenDir(sys.io, ".")
    Assert(d.err == core.SysErr.None && d.value != 0)
    h := Trunc[uint](d.value)
    l := PtListener(h: h)
    var got := ""
    t0 := Now(sys.io)
    SuperviseOn[PtStop, PtListener](sys, 3, h, &l, PtWorker) else e {
        got = PtSupName(e)
    }
    Assert(got == "Os" && Now(sys.io) - t0 < 10_000_000_000)
    _ = core.IoClose(sys.io, h)
}

// PrDec writes pre and then x in decimal into b.
PrDec(b: mut [32]u8, pre: str, x: int): str from b {
    p := pre.AsBytes()
    for i in 0..p.Len() {
        b[i] = p[i]
    }
    var d: [20]u8 = [0; 20]
    var n := 0
    var v := x
    for {
        d[n] = Trunc[u8](48 + v % 10)
        n += 1
        v = v / 10
        if v == 0 {
            break
        }
    }
    for i in 0..n {
        b[p.Len() + i] = d[n - 1 - i]
    }
    return str.From(b[0..p.Len() + n]).Must()
}

// PrWorker is the cookbook's worker: it answers each connection with its
// listener's port; a connection that sends 'p' first makes it panic after
// the answer. Every worker ends itself after 60 s (Accept fails once the
// parent is gone and closed nothing, so the bound is the parent's watchdog).
PrWorker(sys: &System, l: &net.Listener): net.Error!void {
    var b: [32]u8 = [0; 32]
    me := PrDec(mut b, "", l.Port())
    for {
        conn := try l.Accept(sys.io)
        defer conn.Close()
        var q: [1]u8 = [0]
        n := try conn.Read(sys.io, mut q[..])
        try conn.WriteAll(sys.io, me.AsBytes())
        if n == 1 && q[0] == 112 {
            Panic("worker panic on request")
        }
    }
}

// PrAsk dials port, sends cmd and reads the answer to its end: 1 when it
// is the port, 0 otherwise.
PrAsk(io: Io, port: int, cmd: u8): int {
    var ab: [32]u8 = [0; 32]
    c := PrDial(io, PrDec(mut ab, "127.0.0.1:", port)) else {
        return 0
    }
    var q: [1]u8 = [cmd]
    c.WriteAll(io, q[..]) else {
        c.Close()
        return 0
    }
    var got: [32]u8 = [0; 32]
    var n := 0
    for n < 32 {
        k := c.Read(io, mut got[n..]) else {
            break
        }
        if k == 0 {
            break
        }
        n += k
    }
    c.Close()
    var wb: [32]u8 = [0; 32]
    want := PrDec(mut wb, "", port).AsBytes()
    if n != want.Len() {
        return 0
    }
    for i in 0..n {
        if got[i] != want[i] {
            return 0
        }
    }
    return 1
}

// PrClient: 20 asks, one that makes a worker panic, 100 asks while it
// restarts; every answer counted; then cancel.
PrClient(s: &Scope, io: Io, port: int, ok: mut int) {
    for _ in 0..20 {
        *ok += PrAsk(io, port, 105)
    }
    *ok += PrAsk(io, port, 112)
    for _ in 0..100 {
        *ok += PrAsk(io, port, 105)
    }
    s.Cancel()
}

// PrRefused: a dial of port is refused, so no process holds the listener.
PrRefused(io: Io, port: int): bool {
    var ab: [32]u8 = [0; 32]
    c := PrDial(io, PrDec(mut ab, "127.0.0.1:", port)) else e {
        return match e {
            net.Refused { true }
            _ { false }
        }
    }
    c.Close()
    return false
}

// The cookbook recipe "Keep serving when a worker panics" over the
// exported Supervise: in a worker, net.Listen returns the inherited
// listener, whose Port is the parent's; every answer is right while one
// worker panics and is restarted; a cancel ends the tree.
test "Supervise: the exported function over net.Listen restarts a worker that panics, answers every ask, and a cancel ends the tree" (sys: &System) {
    l := net.Listen(sys.io, "127.0.0.1:0") else {
        Exit(5)
    }
    if SupervisedChild(sys) {
        Supervise[net.Error](sys, 4, &l, PrWorker) else {
            Exit(6)
        }
        Exit(7)
    }
    port := l.Port()
    var ok := 0
    var got := "none"
    t0 := Now(sys.io)
    scope s := sys.io.Scope() {
        s.go PrClient(&s, sys.io, port, mut ok)
        Supervise[net.Error](sys, 4, &l, PrWorker) else e {
            match e {
                Canceled {
                    got = "Canceled"
                }
                _ {
                    got = "other"
                }
            }
        }
    }
    l.Close()
    var after := "served"
    if PrRefused(sys.io, port) {
        after = "refused"
    }
    var out := sys.Stdout()
    _ = Print(mut out, "ok {ok} supervise {got} after {after}\n", (ok, got, after)) else {
    }
    _ = out.Flush() else {
    }
    Assert(ok == 121 && got == "Canceled" && after == "refused")
    Assert(Now(sys.io) - t0 < 60_000_000_000)
}

// Backoff.Next and Ran over synthetic times: the backoff rule (three
// restarts in RestartPeriod, then 1 s doubling to 30 s) exactly, with no
// clock. Each crash comes 1 ms after its restart.
test "Supervise: three restarts at once, then 1 s doubling to 30 s; a slow restart is at once; a 60 s run resets" (sys: &System) {
    ms := i64(1_000_000)
    sec := i64(1_000_000_000)
    var b := Backoff(at: [0; 3], n: 0, delay: 0)
    var t: i64 = 0
    // At the 30 s cap the restart three back is 60 s old every third
    // time, so that restart is at once: at most RestartBurst restarts in
    // any RestartPeriod, OTP's intensity rule, never a 30 s stall that
    // the rule does not ask for.
    var want: [14]i64 = [0, 0, 0, 1, 2, 4, 8, 16, 30, 30, 0, 30, 30, 0]
    for i in 0..14 {
        d := b.Next(t)
        Assert(d == want[i] * sec)
        t = t + d + ms
    }
    // A restart more than RestartPeriod after the third-last: at once,
    // and the delay is kept (no clean run happened).
    t = t + 61 * sec
    Assert(b.Next(t) == 0 && b.delay == 30 * sec)
    // A run of 60 s resets the delay: the next burst is three at once,
    // then 1 s again.
    var c := Backoff(at: [0; 3], n: 0, delay: 0)
    t = 0
    for _ in 0..6 {
        t = t + c.Next(t) + ms
    }
    Assert(c.delay == 4 * sec)
    c.Ran(60 * sec)
    t = t + 60 * sec
    var after: [4]i64 = [0, 0, 0, 1]
    for i in 0..4 {
        d := c.Next(t)
        Assert(d == after[i] * sec)
        t = t + d + ms
    }
    // A run just short of the period keeps it: the next backoff doubles.
    var e := Backoff(at: [0; 3], n: 0, delay: 0)
    t = 0
    for _ in 0..6 {
        t = t + e.Next(t) + ms
    }
    e.Ran(60 * sec - 1)
    t = t + 60 * sec
    var kept: [4]i64 = [0, 0, 0, 8]
    for i in 0..4 {
        d := e.Next(t)
        Assert(d == kept[i] * sec)
        t = t + d + ms
    }
}

// Only a cancel ends a slot whose wait failed; any other failure is that
// worker's crash, restarted after the backoff. No Io table
// can make a native procwait fail on demand, so the rule is the function
// Slot calls, checked for every error.
test "Supervise: only Canceled ends a slot whose wait failed" (sys: &System) {
    Assert(WaitEnds(core.SysErr.Canceled))
    var others: [14]core.SysErr = [core.SysErr.NotFound, core.SysErr.Exists, core.SysErr.Denied, core.SysErr.IsDirectory, core.SysErr.NotDirectory, core.SysErr.NoSpace, core.SysErr.InvalidPath, core.SysErr.Closed, core.SysErr.Refused, core.SysErr.Reset, core.SysErr.AddrInUse, core.SysErr.Unreachable, core.SysErr.InvalidAddress, core.SysErr.Other]
    for x in others {
        Assert(!WaitEnds(x))
    }
}

// 64 workers: the first and last rounds cancel 1.5 s after a reply, and
// the middle two cancel immediately or after 300 ms. Every cancel ends
// all workers (Refused after the listener closes) without a handle leak:
// a waiting slot holds no pool thread (procwait is a poller operation),
// which a thread handle per worker would show on Windows.
test "Supervise: 64 workers serve; a cancel at 1.5 s, 0, 300 ms and 1.5 s ends all of them with no handle left" (sys: &System) {
    var port: i64 = 0
    l := PtListen(sys.io, mut port)
    if SupervisedChild(sys) {
        SuperviseOn[PtStop, PtListener](sys, 64, l.h, &l, PtWorker) else {
            Exit(6)
        }
        Exit(7)
    }
    // Round 0 serves for 1.5 s and warms the runtime up (64 spawns at once
    // start pool threads, each a handle, and their number moves with
    // load); the three later rounds, up to 192 worker starts, must leave the
    // count within 32 of where it was after round 0. 16 was exceeded once
    // (19, TestProc_RunBlocks dev at 8 carriers beside three more runs of these
    // blocks); a handle per worker would move it by 64 or more. PtHandles
    // leaves out the handles the runtime makes on demand and keeps
    // (task.KeptHandles): they caused this block's intermittent failures
    // (handles moved by 33 to 50). In 20 release runs of this block alone
    // on Windows (measured 2026-11-17 with a std that printed each count),
    // the window made 0 to 20 timer slots (two
    // handles each) and started 0 to 12 carriers; the raw count moved by 6 to
    // 56, past 32 in 4 runs, and by 2 to 16 without them. That rest is the
    // OS's thread pool behind the registered child waits (its threads and
    // their events), which no counter here reads. The pool's own threads
    // stay counted: none starts in the window (64 after round 0 in every run).
    var before := 0
    var last := l
    var lport := port
    for k, d in [1_500_000_000, 0, 300_000_000, 1_500_000_000] {
        if k == 1 {
            before = PtHandles()
        }
        if k > 0 {
            last = PtListen(sys.io, mut lport)
        }
        var got := ""
        var served := 0
        t0 := Now(sys.io)
        serving := k == 0 || k == 3
        scope s := sys.io.Scope() {
            if serving {
                s.go PtServing(&s, sys.io, lport, i64(d), mut served)
            } else {
                s.go PtTimer(&s, sys.io, i64(d))
                s.go PtCount(sys.io, lport, mut served, false)
            }
            SuperviseOn[PtStop, PtListener](sys, 64, last.h, &last, PtWorker) else e {
                got = PtSupName(e)
            }
        }
        Assert(got == "Canceled" && Now(sys.io) - t0 < 30_000_000_000)
        PtGone(sys.io, last, lport)
        if serving && served == 0 {
            Panic("Supervise: no worker replied before the startup deadline")
        }
    }
    PtNoLeak(sys, before, 32)
}

test "IsWorker is false in a process that Supervise did not start" (sys: &System) {
    Assert(IsWorker(sys) == SupervisedChild(sys))
    Assert(!IsWorker(sys))
}

// PortAnswer serves l, answering every connection with the one byte c.
PortAnswer(sys: &System, l: &net.Listener, c: u8): net.Error!void {
    for {
        conn := try l.Accept(sys.io)
        defer conn.Close()
        var q: [1]u8 = [c]
        try conn.WriteAll(sys.io, q[..])
    }
}

// PortGood answers "1": the worker's admin listener has the port it asked
// for, and its listener of the parent's address is the inherited one.
PortGood(sys: &System, l: &net.Listener): net.Error!void {
    try PortAnswer(sys, l, 49)
}

// PortBad answers "0" from the listener that took the inherited socket in
// place of the admin port.
PortBad(sys: &System, l: &net.Listener): net.Error!void {
    try PortAnswer(sys, l, 48)
}

// PortAsk reads the worker's one-byte answer on port, then cancels the
// scope; 0 when no answer came.
PortAsk(s: &Scope, io: Io, port: int, got: mut int) {
    var ab: [32]u8 = [0; 32]
    c := PrDial(io, PrDec(mut ab, "127.0.0.1:", port)) else {
        s.Cancel()
        return
    }
    var q: [1]u8 = [0]
    n := c.Read(io, mut q[..]) else {
        0
    }
    c.Close()
    if n == 1 {
        *got = int(q[0])
    }
    s.Cancel()
}

test "Supervise: in a worker, a Listen of another port binds it anew, and the Listen of the parent's address gets the inherited listener" (sys: &System) {
    if SupervisedChild(sys) {
        // A port nobody holds: bound and closed through the runtime, so the
        // inherited listener is not asked for it.
        r := core.IoListen(sys.io, "127.0.0.1:0")
        if r.err != core.SysErr.None {
            Exit(5)
        }
        q := Trunc[int](r.os)
        _ = core.IoClose(sys.io, Trunc[uint](r.value))
        var ab: [32]u8 = [0; 32]
        admin := net.Listen(sys.io, PrDec(mut ab, "127.0.0.1:", q)) else {
            Exit(5)
        }
        l := net.Listen(sys.io, "127.0.0.1:0") else {
            Exit(5)
        }
        if admin.Port() == q {
            Supervise[net.Error](sys, 1, &l, PortGood) else {
                Exit(6)
            }
        } else {
            Supervise[net.Error](sys, 1, &admin, PortBad) else {
                Exit(6)
            }
        }
        Exit(7)
    }
    l := net.Listen(sys.io, "127.0.0.1:0") else {
        Exit(5)
    }
    port := l.Port()
    var got := 0
    scope s := sys.io.Scope() {
        s.go PortAsk(&s, sys.io, port, mut got)
        Supervise[net.Error](sys, 1, &l, PortGood) else {
        }
    }
    l.Close()
    Assert(got == 49)
}
