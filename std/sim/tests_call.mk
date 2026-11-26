// Why: a simulation must never reach the OS, so every call-entry op a Sim
// or a tape does not model has to answer Unsupported without running its job.
// What: SIM-Call and SIM-Tape-Wall over every IoOp constant and the first
// number past them; SIM-Call-Fs, SIM-Call-Open and the blocks on a Sim's
// listen, shutdown, cancelled operations and File.Truncate.
// How: CallAll asks each op through a Sim, a Recorder's Tape and a Replayer
// (skipping what each models: the wall clock, fscall.mk's file operations,
// rt.ListenWith's listen, rt.SockShutdown, the ops TapeOp replays), with
// CallMark as the job that records it ran.

import std.core
import std.raw
import std.rt
import std.task
import std.time

// CallRan is set by CallMark if any entry ran a job.
var CallRan: raw.Atomic[u32] = raw.Atomic[u32].New(0)

// CallMark is a job that records that it ran.
CallMark(a: *core.IoCallArgs) {
    // Edge: none needed: CallAll reads it on the task that made every
    // call, after each returned (a job runs inline or is joined first).
    CallRan.Store(1)
}

// ProcOp: op is one a Sim answers from its processes, pipes and
// pseudo-terminals (proc.mk; tests_proc.mk) or its terminal
// (TermOp).
ProcOp(op: u32): bool {
    return op == core.IoOpPipe || op == core.IoOpSpawn || op == core.IoOpSignal || op == core.IoOpDetach ||
        op == core.IoOpGroupEnd || op == core.IoOpPtyOpen || op == core.IoOpPtyResize || op == core.IoOpPtyClose ||
        TermOp(op)
}

// TermOp: op is one a Sim answers from its terminal (term.mk;
// tests_term.mk). IoOpTermSize stays asked: of a standard stream (w.a 0)
// it is Unsupported.
TermOp(op: u32): bool {
    return op == core.IoOpTermOpen || op == core.IoOpTermMode || op == core.IoOpReady || op == core.IoOpTermRead
}

// FsOp: op is one a Sim answers from its file system (fscall.mk).
FsOp(op: u32): bool {
    return op == core.IoOpLock || op == core.IoOpUnlock || op == core.IoOpStat || op == core.IoOpDirOpenAt || op == core.IoOpRemoveAt ||
        op == core.IoOpTruncate
}

// CallAll asks io's call entry for each op from 1 to IoOpCount; true when
// every one answered Unsupported and no job ran, but for a Sim's wall clock
// (IoOpWall), which it answers from the simulated clock (SimWall). A
// tape forwards and logs its wall clock (checked by SIM-Tape-Wall) and the
// ops TapeOp names (the SIM-Tape-Call blocks of tests_tape.mk), so with tape those are not asked here: a
// Recorder over the native Io would run their jobs.
CallAll(io: Io, sim: bool, tape: bool): bool {
    for op in 1..int(core.IoOpCount) + 1 {
        if tape && (Trunc[u32](op) == core.IoOpWall || TapeOp(Trunc[u32](op))) {
            continue
        }
        if sim && ProcOp(Trunc[u32](op)) {
            // Answered by the Sim's processes (proc.mk, the SD8-Sim-
            // blocks of tests_proc.mk); not asked, since a zeroed record
            // names no request.
            continue
        }
        var a := core.IoCallArgs(op: Trunc[u32](op), route: core.IoRouteInline, h: 0, os: 0, p: raw.Null[u8](), pn: 0,
            q: raw.Null[u8](), qn: 0, out: raw.Null[u8](), outn: 0, w: core.IoWords(a: 0, b: 0, c: 0, d: 0),
            res: core.IoResult(value: 0, err: core.SysErr.None, os: 0), job: CallMark, stop: raw.Null[u32]())
        var r := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
        unsafe "a is in this frame until IoCall returns" {
            r = core.IoCall(io, raw.OfMut(mut a))
        }
        if sim && (FsOp(Trunc[u32](op)) || Trunc[u32](op) == core.IoOpListen || Trunc[u32](op) == core.IoOpShutdown ||
            Trunc[u32](op) == core.IoOpUdpConnect || Trunc[u32](op) == core.IoOpTryRecvFrom) {
            // Answered by the Sim's file system (fscall.mk, SIM-Call-Fs),
            // or its network's listen (rt.ListenWith, SD8-Listen-Sim),
            // shutdown (rt.SockShutdown), UdpConnect and TryRecvFrom.
            continue
        }
        want := if sim && Trunc[u32](op) == core.IoOpWall { core.SysErr.None } else { core.SysErr.Unsupported }
        if r.err != want {
            return false
        }
    }
    // Edge: as in CallMark.
    return CallRan.Load() == 0
}

// CallTask runs CallAll on a task of a Sim and stores 1 in out when it held.
CallTask(io: Io, out: &raw.Atomic[u32]) {
    if CallAll(io, true, false) {
        out.Store(1)
    }
}

test "SIM-Call every call-entry op answers Unsupported under a Sim, a Recorder's Tape and a Replayer, and runs no job (the wall clock and the ops each answers aside)" (sys: &System) {
    var s := Sim.New(sys.arena, 1) else {
        Assert(false)
        return
    }
    sio := s.Io()
    out := raw.Atomic[u32].New(0)
    scope r := sio.Scope() {
        r.go CallTask(sio, &out)
    }
    s.Free()
    Assert(out.Load() == 1)
    var rec := Recorder.New(sys.io) else {
        Assert(false)
        return
    }
    Assert(CallAll(rec.Io(), false, true))
    log := rec.Log(sys.arena) else {
        rec.Free()
        Assert(false)
        return
    }
    rec.Free()
    var rp := Replayer.New(log) else {
        Assert(false)
        return
    }
    Assert(CallAll(rp.Io(), false, true))
    rp.Free()
}

test "SIM-Tape-Wall a Recorder forwards the wall clock and logs it; its Replayer answers the same instant" (sys: &System) {
    var rec := Recorder.New(sys.io) else {
        Assert(false)
        return
    }
    w := time.Wall(rec.Io())
    // The inner Io's clock: past 2026-01-01, not the Unix epoch.
    Assert(w.Unix() > 1_767_225_600)
    log := rec.Log(sys.arena) else {
        rec.Free()
        Assert(false)
        return
    }
    rec.Free()
    var rp := Replayer.New(log) else {
        Assert(false)
        return
    }
    Assert(time.Wall(rp.Io()).UnixNanos() == w.UnixNanos())
    Assert(!rp.Diverged())
    rp.Free()
}

// FsB is s's bytes, the form the call functions take a path in.
FsB(s: str): []u8 from s {
    return s.AsBytes()
}

// FsHolder takes h's exclusive lock, holds it 3 ms of the Sim's clock and
// drops it; 1 in out when each step answered as it should.
FsHolder(io: Io, h: uint, out: &raw.Atomic[u32]) {
    if rt.FileLock(io, h, true, true).value != 1 {
        return
    }
    io.Sleep(time.Millis(3)) else {
        return
    }
    if rt.FileUnlock(io, h).err == core.SysErr.None {
        out.Store(1)
    }
}

// FsRun is the SIM-Call-Fs checks on a task of a Sim; true when each held.
FsRun(io: Io): bool {
    var ok := true
    Assert(core.IoMkdir(io, "d").err == core.SysErr.None)
    Assert(core.IoMkdir(io, "d/sub").err == core.SysErr.None)
    o := core.IoOpen(io, "d/x", 1)
    Assert(o.err == core.SysErr.None)
    h1 := Trunc[uint](o.value)
    Assert(core.IoWrite(io, h1, "abc".AsBytes(), -1).value == 3)
    h2 := Trunc[uint](core.IoOpen(io, "d/x", 0).value)
    // A Sim's handle is a foreign record, never a native socket.
    ok = ok && !rt.IsNativeSocket(h1)
    // Locks: per handle, as Linux's OFD locks; a conversion; a close drops one.
    ok = ok && rt.FileLock(io, h1, true, true).value == 1
    ok = ok && rt.FileLock(io, h2, false, true).value == 0
    ok = ok && rt.FileLock(io, h2, true, true).value == 0
    ok = ok && rt.FileUnlock(io, h1).err == core.SysErr.None
    ok = ok && rt.FileLock(io, h2, false, true).value == 1
    ok = ok && rt.FileLock(io, h1, false, true).value == 1
    ok = ok && rt.FileLock(io, h1, true, true).value == 0
    ok = ok && rt.FileUnlock(io, h2).err == core.SysErr.None
    ok = ok && rt.FileLock(io, h1, true, true).value == 1
    ok = ok && rt.FileUnlock(io, h1).err == core.SysErr.None
    ok = ok && rt.FileUnlock(io, h1).err == core.SysErr.None
    // A waiting Lock gets the lock when its holder drops it at 3 ms, and a
    // deadline ends a waiting Lock with Canceled, the lock not taken.
    held := raw.Atomic[u32].New(0)
    t0 := time.Now(io)
    scope s := io.Scope() {
        s.go FsHolder(io, h1, &held)
        io.Sleep(time.Millis(1)) else {}
        scope d := io.Scope() {
            d.CancelAfter(time.Millis(1))
            ok = ok && rt.FileLock(io, h2, true, false).err == core.SysErr.Canceled
        }
        ok = ok && rt.FileLock(io, h2, true, false).value == 1
        ok = ok && time.Now(io).Since(t0).Nanos() >= 3_000_000
    }
    ok = ok && held.Load() == 1
    // A waiting Lock in a cancelled scope is Canceled before it starts,
    // even on a lock no other handle holds, and takes nothing; TryLock is
    // no cancellation point (RT8-Call-LockCancelFree, as natively).
    ok = ok && rt.FileUnlock(io, h2).err == core.SysErr.None
    scope c := io.Scope() {
        c.Cancel()
        ok = ok && rt.FileLock(io, h2, true, false).err == core.SysErr.Canceled
        ok = ok && rt.FileLock(io, h1, true, true).value == 1
        ok = ok && rt.FileUnlock(io, h1).err == core.SysErr.None
    }
    ok = ok && rt.FileLock(io, h2, true, false).value == 1
    ok = ok && rt.FileLock(io, h1, false, true).value == 0
    Assert(core.IoClose(io, h2).err == core.SysErr.None)
    ok = ok && rt.FileLock(io, h1, true, true).value == 1
    Assert(core.IoClose(io, h1).err == core.SysErr.None)
    // Stat by a path's bytes.
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    ok = ok && rt.StatEx(io, FsB("d/x"), false, mut w).err == core.SysErr.None
    ok = ok && w.a == i64(core.IoKindFile) && w.b == 3 && w.d == 0o644 && w.c > 0
    ok = ok && rt.StatEx(io, FsB("d"), true, mut w).err == core.SysErr.None && w.a == i64(core.IoKindDir)
    ok = ok && rt.StatEx(io, FsB("d/none"), false, mut w).err == core.SysErr.NotFound
    var bad: [2]u8 = [b'd', 0xFF]
    ok = ok && rt.StatEx(io, bad[..], false, mut w).err == core.SysErr.Unsupported
    // A directory opened inside a held one, and entries removed inside it.
    dr := rt.DirOpenAt(io, 0, FsB("d"))
    ok = ok && dr.err == core.SysErr.None
    dh := Trunc[uint](dr.value)
    var name: [64]u8 = [0; 64]
    var st := core.IoStat(kind: 0, size: 0, modified: 0)
    var seen := 0
    for {
        r := core.IoReadDir(io, dh, mut name[..], mut st)
        if r.err != core.SysErr.None || r.value == 0 {
            break
        }
        seen += 1
    }
    ok = ok && seen == 2
    cr := rt.DirOpenAt(io, dh, FsB("sub"))
    ok = ok && cr.err == core.SysErr.None
    ok = ok && rt.DirOpenAt(io, dh, FsB("x")).err == core.SysErr.NotDirectory
    ok = ok && rt.DirOpenAt(io, dh, FsB("sub/y")).err == core.SysErr.InvalidPath
    ok = ok && rt.DirOpenAt(io, dh, FsB("..")).err == core.SysErr.InvalidPath
    ok = ok && rt.DirOpenAt(io, dh, FsB("none")).err == core.SysErr.NotFound
    ok = ok && rt.RemoveAt(io, dh, FsB("x"), true).err == core.SysErr.NotDirectory
    ok = ok && rt.RemoveAt(io, dh, FsB("sub"), false).err == core.SysErr.IsDirectory
    ok = ok && rt.RemoveAt(io, dh, FsB("x"), false).err == core.SysErr.None
    ok = ok && rt.RemoveAt(io, dh, FsB("x"), false).err == core.SysErr.NotFound
    ok = ok && rt.RemoveNotEmpty(rt.RemoveAt(io, 0, FsB("d"), true))
    ok = ok && core.IoClose(io, Trunc[uint](cr.value)).err == core.SysErr.None
    ok = ok && rt.RemoveAt(io, dh, FsB("sub"), true).err == core.SysErr.None
    ok = ok && core.IoClose(io, dh).err == core.SysErr.None
    ok = ok && rt.RemoveAt(io, 0, FsB("d"), true).err == core.SysErr.None
    ok = ok && core.IoStatPath(io, "d", mut st).err == core.SysErr.NotFound
    return ok
}

// FsTask runs FsRun and stores 1 in out when it held.
FsTask(io: Io, out: &raw.Atomic[u32]) {
    if FsRun(io) {
        out.Store(1)
    }
}

test "SIM-Call-Fs a Sim answers the call entry's lock, unlock, stat, directory-open-at and remove-at as Linux does" (sys: &System) {
    for seed in 0..3 {
        var s := Sim.New(sys.arena, Cast[u64](seed)) else {
            Assert(false)
            return
        }
        io := s.Io()
        out := raw.Atomic[u32].New(0)
        scope r := io.Scope() {
            r.go FsTask(io, &out)
        }
        s.Free()
        Assert(out.Load() == 1)
    }
}

// FoRun checks the Sim's open entry in the options form, as fs.OpenWith
// passes it; true when each answer held.
FoRun(io: Io): bool {
    var ok := true
    base := core.IoOpenOptions
    rw := base | core.IoOpenRead | core.IoOpenWrite
    // No access asked: EINVAL. Missing without create: NotFound.
    ok = ok && core.IoOpen(io, "o", base).os == 22
    ok = ok && core.IoOpen(io, "o", rw).err == core.SysErr.NotFound
    c := core.IoOpen(io, "o", rw | core.IoOpenCreate | core.IoOpenExclusive)
    ok = ok && c.err == core.SysErr.None
    h := Trunc[uint](c.value)
    ok = ok && core.IoWrite(io, h, "hello".AsBytes(), -1).value == 5
    ok = ok && core.IoClose(io, h).err == core.SysErr.None
    ok = ok && core.IoOpen(io, "o", rw | core.IoOpenCreate | core.IoOpenExclusive).err == core.SysErr.Exists
    // Append: each write at the end, whatever position it names.
    a := core.IoOpen(io, "o", base | core.IoOpenAppend)
    ok = ok && a.err == core.SysErr.None
    ah := Trunc[uint](a.value)
    ok = ok && core.IoWrite(io, ah, "!".AsBytes(), 0).value == 1
    ok = ok && core.IoWrite(io, ah, "?".AsBytes(), -1).value == 1
    ok = ok && core.IoClose(io, ah).err == core.SysErr.None
    r := core.IoOpen(io, "o", base | core.IoOpenRead)
    rh := Trunc[uint](r.value)
    var b: [16]u8 = [0; 16]
    ok = ok && core.IoRead(io, rh, mut b[..], 0).value == 7 && b[0] == b'h' && b[5] == b'!' && b[6] == b'?'
    // A read-only handle does not write.
    ok = ok && core.IoWrite(io, rh, "x".AsBytes(), -1).err == core.SysErr.Denied
    ok = ok && core.IoClose(io, rh).err == core.SysErr.None
    // Write without truncate keeps the rest; truncate empties the file.
    wv := core.IoOpen(io, "o", base | core.IoOpenWrite)
    wh := Trunc[uint](wv.value)
    ok = ok && core.IoWrite(io, wh, "J".AsBytes(), -1).value == 1
    ok = ok && core.IoClose(io, wh).err == core.SysErr.None
    t := core.IoOpen(io, "o", rw | core.IoOpenTruncate)
    th := Trunc[uint](t.value)
    ok = ok && core.IoRead(io, th, mut b[..], 0).value == 0
    ok = ok && core.IoClose(io, th).err == core.SysErr.None
    // The legacy mode 2 writes the file as it is.
    ok = ok && core.IoOpen(io, "none", 2).err == core.SysErr.NotFound
    return ok
}

// FoTask runs FoRun and stores 1 in out when it held.
FoTask(io: Io, out: &raw.Atomic[u32]) {
    if FoRun(io) {
        out.Store(1)
    }
}

test "SIM-Call-Open a Sim opens in the options form fs.OpenWith passes: create, exclusive, append, truncate, read-only, as Linux does" (sys: &System) {
    var s := Sim.New(sys.arena, 2) else {
        Assert(false)
        return
    }
    io := s.Io()
    out := raw.Atomic[u32].New(0)
    scope r := io.Scope() {
        r.go FoTask(io, &out)
    }
    s.Free()
    Assert(out.Load() == 1)
}

// ListenSimTask runs rt.ListenWith on a task of a Sim and stores 1 in out
// when it was the Sim's listen: a listener on the port asked for whatever
// the options, the port held against a second listen, and released by
// its close; and an address the Sim cannot read is InvalidAddress.
ListenSimTask(io: Io, out: &raw.Atomic[u32]) {
    l := rt.ListenWith(io, "127.0.0.1:7400", 4, false)
    if l.err != core.SysErr.None || l.value == 0 || l.os != 7400 {
        return
    }
    if rt.ListenWith(io, "127.0.0.1:7400", 0, true).err != core.SysErr.AddrInUse {
        return
    }
    if rt.CloseRuntimeHandle(Trunc[uint](l.value)).err != core.SysErr.None {
        return
    }
    m := rt.ListenWith(io, "127.0.0.1:7400", 0, true)
    if m.err != core.SysErr.None || rt.CloseRuntimeHandle(Trunc[uint](m.value)).err != core.SysErr.None {
        return
    }
    if rt.ListenWith(io, "nowhere", 0, true).err != core.SysErr.InvalidAddress {
        return
    }
    out.Store(1)
}

test "SD8-Listen-Sim under a Sim rt.ListenWith is the Sim's listen, whatever the backlog and reuse choice" (sys: &System) {
    var s := Sim.New(sys.arena, 3) else {
        Assert(false)
        return
    }
    io := s.Io()
    out := raw.Atomic[u32].New(0)
    scope r := io.Scope() {
        r.go ListenSimTask(io, &out)
    }
    s.Free()
    Assert(out.Load() == 1)
}

// CancelEntriesTask runs each path and network entry of a Sim's table in a
// cancelled scope and stores 1 in out when every one answered Canceled,
// as the native entries' Stop does, and the same open after the scope
// answered NotFound.
CancelEntriesTask(io: Io, out: &raw.Atomic[u32]) {
    var ok := true
    var st := core.IoStat(kind: 0, size: 0, modified: 0)
    scope s := io.Scope() {
        s.Cancel()
        c := core.SysErr.Canceled
        ok = ok && core.IoOpen(io, "missing", 0).err == c
        ok = ok && core.IoRename(io, "missing", "other").err == c
        ok = ok && core.IoRemove(io, "missing").err == c
        ok = ok && core.IoMkdir(io, "d").err == c
        ok = ok && core.IoStatPath(io, "missing", mut st).err == c
        ok = ok && core.IoOpenDir(io, "missing").err == c
        ok = ok && core.IoListen(io, "127.0.0.1:7500").err == c
        ok = ok && core.IoDial(io, "127.0.0.1:7500").err == c
    }
    ok = ok && core.IoOpen(io, "missing", 0).err == core.SysErr.NotFound
    if ok {
        out.Store(1)
    }
}

test "under a Sim a cancelled scope's open, rename, remove, mkdir, stat, opendir, listen and dial are Canceled before they start, as natively" (sys: &System) {
    var s := Sim.New(sys.arena, 4) else {
        Assert(false)
        return
    }
    io := s.Io()
    out := raw.Atomic[u32].New(0)
    scope r := io.Scope() {
        r.go CancelEntriesTask(io, &out)
    }
    s.Free()
    Assert(out.Load() == 1)
}

// ShutdownRead reads h until n bytes or the end of the stream (a Sim's
// receive may come back short, as TCP's does): the bytes read, or -1 on an
// error.
ShutdownRead(io: Io, h: uint, buf: mut []u8, n: int): int {
    var got := 0
    for got < n {
        r := core.IoRecv(io, h, mut buf[got..n])
        if r.err != core.SysErr.None {
            return -1
        }
        if r.value == 0 {
            break
        }
        got += Trunc[int](r.value)
    }
    return got
}

// ShutdownSimTask runs a stream's shutdowns through rt.SockShutdown under
// a Sim and stores in out 1 when each answered as natively, else the
// number of the first step that did not.
ShutdownSimTask(io: Io, out: &raw.Atomic[u32]) {
    l := core.IoListen(io, "127.0.0.1:7410")
    d := core.IoDial(io, "127.0.0.1:7410")
    a := core.IoAccept(io, Trunc[uint](l.value))
    if l.err != core.SysErr.None || d.err != core.SysErr.None || a.err != core.SysErr.None {
        out.Store(2)
        return
    }
    lh := Trunc[uint](l.value)
    dh := Trunc[uint](d.value)
    ah := Trunc[uint](a.value)
    var buf: [8]u8 = [0; 8]
    var step: u32 = 1
    // Write shut: the peer reads what was sent, then the end of the
    // stream.
    if core.IoSend(io, dh, "abc".AsBytes()).value != 3 {
        step = 3
    } else if rt.SockShutdown(io, dh, 1).err != core.SysErr.None {
        step = 4
    } else if ShutdownRead(io, ah, mut buf[..], 8) != 3 || buf[0] != 0x61 || buf[2] != 0x63 {
        step = 5
    } else if core.IoSend(io, ah, "xyz".AsBytes()).value != 3 {
        // The other direction still works.
        step = 6
    } else if ShutdownRead(io, dh, mut buf[..], 3) != 3 || buf[0] != 0x78 {
        step = 7
    } else if core.IoSend(io, dh, "q".AsBytes()).err != core.SysErr.Reset {
        // A send on the shut side is Reset; a second shutdown succeeds.
        step = 8
    } else if rt.SockShutdown(io, dh, 1).err != core.SysErr.None {
        step = 9
    } else if rt.SockShutdown(io, ah, 0).err != core.SysErr.None {
        // Read shut: this end's receives answer 0.
        step = 10
    } else if core.IoRecv(io, ah, mut buf[..]).value != 0 {
        step = 11
    } else if rt.SockShutdown(io, ah, 3).err != core.SysErr.Other {
        // An unknown how is Other; a listener is no stream.
        step = 12
    } else if rt.SockShutdown(io, lh, 2).err != core.SysErr.Closed {
        step = 13
    }
    _ = core.IoClose(io, dh)
    _ = core.IoClose(io, ah)
    _ = core.IoClose(io, lh)
    out.Store(step)
}

test "under a Sim a stream's shutdown ends the peer's reads after the bytes sent, keeps the other direction, refuses later sends and answers receives 0 after a read shutdown" (sys: &System) {
    var s := Sim.New(sys.arena, 5) else {
        Assert(false)
        return
    }
    io := s.Io()
    out := raw.Atomic[u32].New(0)
    scope r := io.Scope() {
        r.go ShutdownSimTask(io, &out)
    }
    s.Free()
    Assert(out.Load() == 1)
}

// ShutdownParked receives on h, which nothing is sent to, and stores 1 in
// out when the receive ends with the end of the stream.
ShutdownParked(io: Io, h: uint, out: &raw.Atomic[u32]) {
    var buf: [4]u8 = [0; 4]
    r := core.IoRecv(io, h, mut buf[..])
    if r.err == core.SysErr.None && r.value == 0 {
        out.Store(1)
    }
}

// ShutdownWakeTask parks a receiver on one end, then shuts the other
// end's sending side from another task; out gets 1 when the receiver woke
// with the end of the stream.
ShutdownWakeTask(io: Io, out: &raw.Atomic[u32]) {
    l := core.IoListen(io, "127.0.0.1:7411")
    d := core.IoDial(io, "127.0.0.1:7411")
    a := core.IoAccept(io, Trunc[uint](l.value))
    if l.err != core.SysErr.None || d.err != core.SysErr.None || a.err != core.SysErr.None {
        return
    }
    got := raw.Atomic[u32].New(0)
    scope s := io.Scope() {
        s.go ShutdownParked(io, Trunc[uint](a.value), &got)
        io.Sleep(core.DurationOf(1_000_000)) else {}
        _ = rt.SockShutdown(io, Trunc[uint](d.value), 1)
    }
    _ = core.IoClose(io, Trunc[uint](d.value))
    _ = core.IoClose(io, Trunc[uint](a.value))
    _ = core.IoClose(io, Trunc[uint](l.value))
    out.Store(got.Load())
}

test "under a Sim a receiver already waiting when the peer shuts its sending side wakes with the end of the stream" (sys: &System) {
    for seed in 1..5 {
        var s := Sim.New(sys.arena, Trunc[u64](seed)) else {
            Assert(false)
            return
        }
        io := s.Io()
        out := raw.Atomic[u32].New(0)
        scope r := io.Scope() {
            r.go ShutdownWakeTask(io, &out)
        }
        s.Free()
        Assert(out.Load() == 1)
    }
}

// MainStatUntilCut stats p on io until a stat is Canceled, at most limit
// times: the number of stats that succeeded, or -1 when none was cut.
MainStatUntilCut(io: Io, p: str, limit: int): int {
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    for k in 0..limit {
        r := rt.StatEx(io, p.AsBytes(), false, mut w)
        if r.err == core.SysErr.Canceled {
            return k
        }
    }
    return -1
}

test "under a Sim with main alone, a scope's cancel or deadline cuts stat, readdir, a waiting Lock and a Sleep at the deadline's clock" (sys: &System) {
    var s := Sim.New(sys.arena, 9) else {
        Assert(false)
        return
    }
    io := s.Io()
    Assert(core.IoMkdir(io, "d").err == core.SysErr.None)
    f := core.IoOpen(io, "d/f", 1)
    Assert(f.err == core.SysErr.None)
    // A cancelled scope: the stat and the directory fill are Canceled
    // before they start.
    dh := core.IoOpenDir(io, "d")
    Assert(dh.err == core.SysErr.None)
    scope c := io.Scope() {
        c.Cancel()
        Assert(MainStatUntilCut(io, "d/f", 1) == 0)
        var name: [64]u8 = [0; 64]
        var st := core.IoStat(kind: 0, size: 0, modified: 0)
        Assert(core.IoReadDir(io, Trunc[uint](dh.value), mut name[..], mut st).err == core.SysErr.Canceled)
    }
    rt.Abandon(Trunc[uint](dh.value))
    // A 1 ms deadline: the stats run on the simulated disk's clock until
    // one starts after it.
    t0 := time.Now(io)
    scope d := io.Scope() {
        d.CancelAfter(time.Millis(1))
        n := MainStatUntilCut(io, "d/f", 100_000)
        Assert(n > 0 && d.Expired())
    }
    took := time.Now(io).Since(t0).Nanos()
    Assert(took >= 1_000_000 && took < 2_000_000)
    // A waiting Lock against another handle's: cut at its 1 ms deadline.
    g := core.IoOpen(io, "d/f", 0)
    Assert(g.err == core.SysErr.None)
    Assert(rt.FileLock(io, Trunc[uint](f.value), true, true).value == 1)
    t1 := time.Now(io)
    var lr := core.IoResult(value: 0, err: core.SysErr.None, os: 0)
    scope d := io.Scope() {
        d.CancelAfter(time.Millis(1))
        lr = rt.FileLock(io, Trunc[uint](g.value), true, false)
    }
    lt := time.Now(io).Since(t1).Nanos()
    Assert(lr.err == core.SysErr.Canceled && lt >= 1_000_000 && lt < 2_000_000)
    // A Sleep: ended at the deadline's clock.
    t2 := time.Now(io)
    var cut := false
    scope d := io.Scope() {
        d.CancelAfter(time.Millis(3))
        io.Sleep(time.Seconds(10)) else {
            cut = true
        }
    }
    st := time.Now(io).Since(t2).Nanos()
    Assert(cut && st >= 3_000_000 && st < 3_100_000)
    rt.Abandon(Trunc[uint](g.value))
    rt.Abandon(Trunc[uint](f.value))
    s.Free()
}

test "under a Sim, File.Truncate shortens and lengthens a file (zeros past the old end), is durable only after a sync, and is Denied on a read-only handle" (sys: &System) {
    var s := Sim.New(sys.arena, 13) else {
        Assert(false)
        return
    }
    io := s.Io()
    o := core.IoOpen(io, "f.bin", 1)
    Assert(o.err == core.SysErr.None)
    h := Trunc[uint](o.value)
    var big: [6000]u8 = [7; 6000]
    Assert(core.IoWrite(io, h, big[..], 0).value == 6000)
    Assert(core.IoSync(io, h).err == core.SysErr.None)
    // Shorter: 4,100 bytes, the tail of the second page gone.
    Assert(rt.FileTruncate(io, h, 4100).err == core.SysErr.None)
    Assert(core.IoSize(io, h).value == 4100)
    // Longer again: zeros past 4,100, not the old sevens.
    Assert(rt.FileTruncate(io, h, 5000).err == core.SysErr.None)
    var buf: [8]u8 = [0; 8]
    Assert(core.IoRead(io, h, mut buf[..], 4096).value == 8)
    Assert(buf[0] == 7 && buf[3] == 7 && buf[4] == 0 && buf[7] == 0)
    Assert(rt.FileTruncate(io, h, 5).err == core.SysErr.None && core.IoSize(io, h).value == 5)
    Assert(rt.FileTruncate(io, h, -1).err == core.SysErr.Other)
    // Not synced: a restart keeps the synced 6,000 bytes.
    rt.Abandon(h)
    io2 := s.Restart()
    r := core.IoOpen(io2, "f.bin", 0)
    Assert(r.err == core.SysErr.None)
    rh := Trunc[uint](r.value)
    Assert(core.IoSize(io2, rh).value == 6000)
    Assert(rt.FileTruncate(io2, rh, 1).err == core.SysErr.Denied)
    rt.Abandon(rh)
    s.Free()
}

RunUdpCallOps(io: Io, a_port: u16, b_port: u16): int {
    var ip: [16]u8 = [0; 16]
    ip[0] = 127
    ip[3] = 1
    la := core.IoAddr(ip: ip, port: a_port, v6: false, zone: 0)
    lb := core.IoAddr(ip: ip, port: b_port, v6: false, zone: 0)
    ra := core.IoUdp(io, la)
    if ra.err != core.SysErr.None {
        return -1
    }
    ha := Trunc[uint](ra.value)
    pa := core.IoAddr(ip: ip, port: Trunc[u16](ra.os), v6: false, zone: 0)
    rb := core.IoUdp(io, lb)
    if rb.err != core.SysErr.None {
        rt.Abandon(ha)
        return -2
    }
    hb := Trunc[uint](rb.value)
    pb := core.IoAddr(ip: ip, port: Trunc[u16](rb.os), v6: false, zone: 0)

    rc := task.NativeUdpConnect(io, ha, pb)
    if rc.err != core.SysErr.None {
        rt.Abandon(ha)
        rt.Abandon(hb)
        return -3
    }
    var rbuf: [16]u8 = [0; 16]
    var from := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    // Empty TryRecvFrom -> value -1, err None.
    rt0 := task.NativeTryRecvFrom(io, ha, mut rbuf[..], mut from)
    if rt0.err != core.SysErr.None || rt0.value != -1 {
        rt.Abandon(ha)
        rt.Abandon(hb)
        return -4
    }
    var pkt: [3]u8 = [11, 22, 33]
    _ = core.IoSendTo(io, hb, pkt[..], pa)
    var got := -1
    for _ in 0..200 {
        rt1 := task.NativeTryRecvFrom(io, ha, mut rbuf[..], mut from)
        if rt1.err == core.SysErr.None && rt1.value == 3 {
            got = int(rbuf[0]) + int(rbuf[1]) + int(rbuf[2])
            break
        }
        io.Sleep(core.DurationOf(100_000)) else {
            break
        }
    }
    rt.Abandon(ha)
    rt.Abandon(hb)
    return got
}

test "SIM-Call-UdpOps ops 42 (UdpConnect) and 43 (TryRecvFrom) work under a Sim and replay via Recorder and Replayer" (sys: &System) {
    // Under Sim: UdpConnect filters and TryRecvFrom drains once queued.
    var s := try Sim.New(sys.arena, 29)
    sio := s.Io()
    Assert(RunUdpCallOps(sio, 0, 0) == 66)
    s.Free()

    // Under Recorder + Replayer over native Io: identical replay.
    var rec := try Recorder.New(sys.io)
    n_rec := RunUdpCallOps(rec.Io(), 0, 0)
    Assert(n_rec == 66)
    log := rec.Log(sys.arena) else {
        rec.Free()
        Assert(false)
        return
    }
    rec.Free()
    var rp := try Replayer.New(log)
    n_rep := RunUdpCallOps(rp.Io(), 0, 0)
    Assert(n_rep == 66 && rp.Done() && !rp.Diverged())
    rp.Free()
}
