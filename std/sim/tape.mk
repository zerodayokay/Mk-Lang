// Why: a Recorder and a Replayer (record.mk) are Ios, so each needs an
// IoTable; one table serves both, since every entry records or replays
// the same call.
//
// What: TapeTable and its entries (TOpen, TRead, ..., TDeadline, TStdio,
// TCall, TWall, TSleep, TWait, TWake), with TapeOp, the call ops the tape
// logs, and TakeCheck, the replay of a masked cancellation check.
//
// How: each entry digests its inputs, then either answers from the log (a
// Replayer) or calls the inner Io and logs the answer (a Recorder). The
// pattern is the same for every entry; only the inputs digested and the
// outputs logged differ.

import std.core
import std.raw
import std.rt
import std.task

const TapeTable = core.IoTable(open: TOpen, read: TRead, write: TWrite, size: TSize,
    sync: TSync, close: TClose, rename: TRename, remove: TRemove, listen: TListen,
    accept: TAccept, dial: TDial, recv: TRecv, send: TSend, now: TNow,
    sleep: TSleep, random: TRandom, yield: TYield, signal: TSignal, wait: TWait,
    wake: TWake, mkdir: TMkdir, stat: TStat, opendir: TOpenDir, readdir: TReadDir,
    udp: TUdp, sendto: TSendTo, recvfrom: TRecvFrom, resolve: TResolve, spawn: TSpawn,
    procwait: TProcWait, prockill: TProcKill, inherited: TInherited,
    enter: TEnter, leave: TLeave, deadline: TDeadline, stdio: TStdio,
    call: TCall)

// The hooks are not logged: they are the scheduler's, not the
// program's calls. A Recorder forwards them, so tasks spawned under a
// Recorder over a Sim are scheduled by the Sim, also after the Recorder is
// freed, until each has ended (LeaveMode counts them); a Replayer has no
// scheduler behind it.
TEnter(io: Io, id: u64) {
    m := HookMode(io)
    if m.ok && !m.replay {
        core.IoEnter(m.inner, id)
    }
}

TLeave(io: Io, id: u64) {
    m := LeaveMode(io, id)
    if m.ok && !m.replay {
        core.IoLeave(m.inner, id)
    }
}

// TStdio is the tape's stdio entry. Not logged, as the hooks are
// not: the bytes are the program's report, not an input a replay must
// answer. A Recorder writes through its inner Io (the process's stream,
// or a Sim's in its order); a Replayer has no scheduler behind it, so it
// writes to the process's stream without suspending (core.StdWriteHere,
// as SimStdio). A freed tape writes nothing (Canceled).
TStdio(io: Io, i: u32, data: []u8): core.IoResult {
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if !m.replay {
        return core.IoStdio(m.inner, i, data)
    }
    if core.StdWriteHere(rt.Proc(), i, rt.DirectIo(), rt.NativeIo(), data) {
        return Ok(i64(data.Len()))
    }
    return Fail(core.SysErr.Other)
}

// TDeadline is the tape's deadline entry. A Recorder arms and
// disarms on its inner Io's clock (the native timer heap, a Sim's), so a
// recorded run's deadlines fire as in the run it records, and logs each arm (ODeadline: the deadline the scope then
// holds, and whether it fired at once) and each fire (OFire, by the
// scope's arm number, before the first record after it: LogFires). A
// Replayer has no clock behind it: it records the logged deadline in the
// scope (task.DeadlineSet), so Remaining reads it against the replayed
// clock, and fires the scope's deadline where the log holds its fire
// (ReplayFires), so Expired and the cancellation replay. The tape lists
// each scope armed through it until that scope's Join disarms it, and a
// freed Recorder keeps its slot while the list is not empty (TapeGone),
// so the disarm always reaches the inner Io and no armed record outlives
// its scope. O(armed scopes of the tape) besides the inner entry and a
// replayed fire's walk; allocates the list's growth on std's heap.
TDeadline(io: Io, rec: *u8, nanos: i64, op: u32): core.IoResult {
    a := raw.Addr(rec)
    if op == 1 {
        TapeDrop(io, a, true)
        return Ok(0)
    }
    e := EnterTape(io)
    if !e.ok {
        return Dead()
    }
    t := TapeOf(&e)
    i := FindDl(&*t, a)
    num := if i >= 0 { t.dls.Get(i).num } else { t.arms + 1 }
    d := FnvU64(FnvI(FnvSeed, nanos), num)
    if t.replay {
        ReplayFires(t, false)
        var nil := NoOut()
        var r := TakeLocked(t, ODeadline, 0, d, mut nil[..], HNone, Bound(max: -1, hdr: 0, unit: 0))
        if !t.diverged && (r.value < -1 || r.value > TapeMaxInstant || r.os < 0 || r.os > 1) {
            // An instant no clock gives (Remaining would overflow on it).
            t.diverged = true
            r = Fail(core.SysErr.Canceled)
        }
        if t.diverged {
            UnlockSlot(e.slot)
            return r
        }
        if i < 0 {
            t.arms = num
        }
        if r.value >= 0 {
            fire := r.os == 1
            if i < 0 && !t.dls.Push(DlRec(rec: a, num: num, at: r.value, fired: fire, arming: false)) {
                UnlockSlot(e.slot)
                rt.Fatal("std.sim: cannot grow a replayer's deadline list")
            }
            if i >= 0 {
                x := t.dls.Get(i)
                t.dls.Set(i, DlRec(rec: a, num: num, at: r.value, fired: x.fired || fire, arming: false))
            }
            _ = task.DeadlineSet(rec, r.value)
            if fire {
                // As natively for a deadline that has passed: fired in the
                // arm, under the lock as ReplayFires' fires; it set the
                // flag in the recording, so a loss diverges.
                task.DeadlineFireRec(rec)
                if !task.DeadlineExpired(rec) {
                    t.diverged = true
                }
            }
        }
        UnlockSlot(e.slot)
        return core.IoResult(value: 0, err: r.err, os: 0)
    }
    if i < 0 {
        t.arms = num
        if !t.dls.Push(DlRec(rec: a, num: num, at: -1, fired: false, arming: true)) {
            UnlockSlot(e.slot)
            rt.Fatal("std.sim: cannot grow a recorder's deadline list")
        }
    } else {
        x := t.dls.Get(i)
        t.dls.Set(i, DlRec(rec: a, num: num, at: x.at, fired: x.fired, arming: true))
    }
    inner := core.IoMaskAs(t.inner, io)
    UnlockSlot(e.slot)
    r := core.IoDeadline(inner, rec, nanos, 0)
    at := task.DeadlineAt(rec)
    var value: i64 = -1
    if v := at {
        value = v
    }
    expired := task.DeadlineExpired(rec)
    e2 := EnterTape(io)
    if e2.ok {
        t2 := TapeOf(&e2)
        // The arm's own fire goes in the arm record (os 1), not in an
        // OFire before it: the replay lists the scope at the arm.
        // A fire inside the arm was skipped by LogFires (arming), so it
        // is this record's.
        var now := false
        j := FindDl(&*t2, a)
        if j >= 0 {
            x := t2.dls.Get(j)
            now = expired && !x.fired
            t2.dls.Set(j, DlRec(rec: a, num: num, at: value, fired: x.fired || now, arming: false))
        }
        LogFires(t2, false)
        var empty: [0]u8 = [0; 0]
        Append(t2, ODeadline, FnvU64(d, 0), value, r.err, if now { 1 } else { 0 }, empty[..], empty[..])
        UnlockSlot(e2.slot)
    }
    if at.IsNone() {
        // The inner Io records no deadline (no clock): no Join disarms it.
        TapeDrop(io, a, false)
    }
    return r
}

// TapeDrop takes the scope record at a off the tape's list and, when
// disarm, disarms it on a Recorder's inner Io; a freed Recorder whose list
// and tasks are now gone gives up its slot and frees its block here (after
// the disarm, which uses a copy of the inner Io). A Replayer's scope has
// nothing armed: a fire it replays ran under the lock taken here.
TapeDrop(io: Io, a: uint, disarm: bool) {
    e := EnterSlot(io)
    if !e.ok {
        return
    }
    t := TapeOf(&e)
    var had := false
    i := FindDl(&*t, a)
    if i >= 0 {
        _ = t.dls.RemoveAt(i)
        had = true
    }
    inner := t.inner
    replay := t.replay
    gone := had && TapeGone(&*t)
    if gone {
        SetSlot(e.slot, 0, 0)
    }
    UnlockSlot(e.slot)
    if had && disarm && !replay {
        unsafe "a is the live scope record whose Join is disarming it" {
            _ = core.IoDeadline(inner, raw.FromAddr[u8](a), 0, 1)
        }
    }
    if gone {
        FreeTape(e.t)
    }
}

// TapeOp: the call op op is one the tape logs and replays: the
// file system's and the metadata ops, whose outputs are a result, the
// four words and the out bytes, and whose handles are runtime handles
// (IoOpDirOpenAt's and IoOpListen's new ones are logged as other new
// handles are). The rest answer Unsupported (core.InertCall): Map and
// Unmap (an address in this process), Pipe, Spawn, Signal, Detach and
// GroupEnd (a child, and OS handles std.rt wraps after the call), the Pty
// ops, TermOpen, TermMode, Ready and TermRead (a terminal, and OS handles
// std.rt wraps after the call), UnixListen and UnixDial (an OS socket
// std.rt wraps after the call), CertVerify (a trust verdict, which a log
// anyone can edit must not supply: std.x509's platform verification then
// fails Unsupported), and Lock, Unlock, LocalAddr and PeerAddr, whose
// callers make them only for an OS handle (fs.File's release before a
// Windows lock, net's NewStream), which a recording has and its replay
// does not.
TapeOp(op: u32): bool {
    return op == core.IoOpSeek || op == core.IoOpTruncate || op == core.IoOpStat || op == core.IoOpSetModified ||
        op == core.IoOpSetMode || op == core.IoOpSymlink || op == core.IoOpReadLink || op == core.IoOpCwd ||
        op == core.IoOpAbsolute || op == core.IoOpCopy || op == core.IoOpSockOpt || op == core.IoOpShutdown ||
        op == core.IoOpIsTerminal || op == core.IoOpTermSize || op == core.IoOpTimeZone ||
        op == core.IoOpDataSync || op == core.IoOpDirOpenAt || op == core.IoOpRemoveAt || op == core.IoOpListen ||
        op == core.IoOpUdpConnect || op == core.IoOpTryRecvFrom
}

// CountOp: op's value is the count of out bytes it wrote.
CountOp(op: u32): bool {
    return op == core.IoOpReadLink || op == core.IoOpCwd || op == core.IoOpAbsolute || op == core.IoOpTimeZone
}

// PutWords and GetWords are a call record's 32 bytes of words.
PutWords(out: mut []u8, w: core.IoWords) {
    PutAt(out, 0, Trunc[u64](w.a))
    PutAt(out, 8, Trunc[u64](w.b))
    PutAt(out, 16, Trunc[u64](w.c))
    PutAt(out, 24, Trunc[u64](w.d))
}

GetWords(b: []u8): core.IoWords {
    return core.IoWords(a: Trunc[i64](GetU64(b, 0)), b: Trunc[i64](GetU64(b, 8)), c: Trunc[i64](GetU64(b, 16)),
        d: Trunc[i64](GetU64(b, 24)))
}

// TCall is the tape's call entry. The wall
// clock (core.IoOpWall) is an input as now is: a Recorder forwards it and
// logs the answer, a Replayer answers it from the log.
// The ops TapeOp names are logged with their inputs' digest and replayed
// with their result, words and out bytes (OCall + op); a Recorder
// forwards each to its inner Io, IoOpDirOpenAt through rt.DirOpenAt, so
// a native inner Io's directory handle is wrapped before it is logged.
// Every other op answers Unsupported, as an Io with no OS does
// (core.InertCall). O(the op's bytes) besides the inner call; allocates
// the log's growth.
TCall(io: Io, a: *core.IoCallArgs): core.IoResult {
    var op: u32 = 0
    unsafe "a is the caller's record, live until this returns" {
        op = (*a).op
    }
    if op == core.IoOpWall {
        return TWall(io, a)
    }
    if op == core.IoOpUdpConnect {
        return TUdpConnect(io, a)
    }
    if op == core.IoOpTryRecvFrom {
        return TTryRecvFrom(io, a)
    }
    if !TapeOp(op) {
        return core.InertCall(io, a)
    }
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    var empty: [0]u8 = [0; 0]
    var h: uint = 0
    var w := core.IoWords(a: 0, b: 0, c: 0, d: 0)
    var d := FnvU64(FnvSeed, u64(op))
    var outn := 0
    unsafe "a is the caller's record, live until this returns; p, q and out are its slices" {
        h = (*a).h
        w = (*a).w
        outn = (*a).outn
        d = FnvU64(d, u64((*a).route))
        d = FnvU64(Digest(d, raw.Slice((*a).p, (*a).pn)), Cast[u64]((*a).pn))
        d = FnvU64(Digest(d, raw.Slice((*a).q, (*a).qn)), Cast[u64]((*a).qn))
    }
    d = FnvI(FnvI(FnvI(FnvI(FnvI(d, i64(outn)), w.a), w.b), w.c), w.d)
    tag := CallTag(op)
    hfx := if op == core.IoOpDirOpenAt || op == core.IoOpListen { HNew } else { HNone }
    var wb: [32]u8 = [0; 32]
    if m.replay {
        bd := if CountOp(op) { Bound(max: i64(outn), hdr: WordsSize, unit: 1) } else { Bound(max: -1, hdr: 0, unit: 0) }
        var n := 0
        var r := Fail(core.SysErr.Canceled)
        unsafe "out and outn are the caller's slice, live until this returns" {
            out := if outn > 0 { raw.SliceMut((*a).out, outn) } else { mut empty[..] }
            r = TakeV2(io, tag, h, d, mut wb[..], out, hfx, bd, mut n)
        }
        unsafe "a is the caller's record, live until this returns" {
            if n >= WordsSize {
                (*a).w = GetWords(wb[..])
            }
            (*a).res = r
        }
        return r
    }
    var r := Fail(core.SysErr.Other)
    if op == core.IoOpDirOpenAt {
        unsafe "p and pn are the caller's slice, live until this returns" {
            r = rt.DirOpenAt(m.inner, h, raw.Slice((*a).p, (*a).pn))
        }
    } else {
        r = core.IoCall(m.inner, a)
    }
    var k := 0
    if CountOp(op) && r.err == core.SysErr.None && r.value > 0 && r.value <= i64(outn) {
        k = Cast[int](r.value)
    }
    unsafe "a is the caller's record, live until this returns; out holds outn bytes" {
        PutWords(mut wb[..], (*a).w)
        (*a).res = r
        out := if k > 0 { raw.Slice((*a).out, k) } else { empty[..] }
        Post2(io, tag, h, d, r, wb[..], out)
    }
    return r
}

// TWall is TCall's wall clock.
TWall(io: Io, a: *core.IoCallArgs): core.IoResult {
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        r := Take(io, OWall, 0, FnvSeed, mut nil[..], HNone)
        unsafe "a is the caller's record, live until this returns" {
            (*a).res = r
        }
        return r
    }
    r := core.IoCall(m.inner, a)
    Post(io, OWall, 0, FnvSeed, r, nil[..])
    return r
}

// NoOut is the empty payload buffer of a call with no outputs.
NoOut(): [0]u8 {
    return [0; 0]
}

TOpen(io: Io, p: str, mode: u32): core.IoResult {
    d := FnvU64(Digest(FnvSeed, p.AsBytes()), u64(mode))
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, OOpen, 0, d, mut nil[..], HNew)
    }
    r := core.IoOpen(m.inner, p, mode)
    Post(io, OOpen, 0, d, r, nil[..])
    return r
}

TRead(io: Io, h: uint, buf: mut []u8, pos: i64): core.IoResult {
    d := FnvI(FnvI(FnvSeed, pos), i64(buf.Len()))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return TakeV(io, ORead, h, d, buf, HNone, Bound(max: i64(buf.Len()), hdr: 0, unit: 1))
    }
    r := core.IoRead(m.inner, h, buf, pos)
    Post(io, ORead, h, d, r, Filled(buf[..], r))
    return r
}

TWrite(io: Io, h: uint, data: []u8, pos: i64): core.IoResult {
    d := FnvI(Digest(FnvSeed, data), pos)
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return TakeV(io, OWrite, h, d, mut nil[..], HNone, Bound(max: i64(data.Len()), hdr: 0, unit: 0))
    }
    r := core.IoWrite(m.inner, h, data, pos)
    Post(io, OWrite, h, d, r, nil[..])
    return r
}

// THandle is the pattern of an entry whose one input is its handle.
THandle(io: Io, op: u8, h: uint, hfx: int): core.IoResult {
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, op, h, FnvSeed, mut nil[..], hfx)
    }
    var r := Fail(core.SysErr.Other)
    if op == OSize {
        r = core.IoSize(m.inner, h)
    } else if op == OSync {
        r = core.IoSync(m.inner, h)
    } else if op == OClose {
        r = core.IoClose(m.inner, h)
    } else if op == OAccept {
        r = core.IoAccept(m.inner, h)
    } else if op == OProcWait {
        r = core.IoProcWait(m.inner, h)
    } else if op == OProcKill {
        r = core.IoProcKill(m.inner, h)
    }
    Post(io, op, h, FnvSeed, r, nil[..])
    return r
}

TSize(io: Io, h: uint): core.IoResult {
    return THandle(io, OSize, h, HNone)
}

TSync(io: Io, h: uint): core.IoResult {
    return THandle(io, OSync, h, HNone)
}

TClose(io: Io, h: uint): core.IoResult {
    return THandle(io, OClose, h, HClose)
}

TAccept(io: Io, h: uint): core.IoResult {
    return THandle(io, OAccept, h, HNew)
}

TProcWait(io: Io, h: uint): core.IoResult {
    return THandle(io, OProcWait, h, HNone)
}

TProcKill(io: Io, h: uint): core.IoResult {
    return THandle(io, OProcKill, h, HNone)
}

TRename(io: Io, src: str, dst: str): core.IoResult {
    d := Digest(FnvU64(Digest(FnvSeed, src.AsBytes()), 0), dst.AsBytes())
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, ORename, 0, d, mut nil[..], HNone)
    }
    r := core.IoRename(m.inner, src, dst)
    Post(io, ORename, 0, d, r, nil[..])
    return r
}

// TPath is the pattern of an entry whose one input is a path or address.
TPath(io: Io, op: u8, p: str, hfx: int): core.IoResult {
    d := Digest(FnvSeed, p.AsBytes())
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, op, 0, d, mut nil[..], hfx)
    }
    var r := Fail(core.SysErr.Other)
    if op == ORemove {
        r = core.IoRemove(m.inner, p)
    } else if op == OListen {
        r = core.IoListen(m.inner, p)
    } else if op == ODial {
        r = core.IoDial(m.inner, p)
    } else if op == OMkdir {
        r = core.IoMkdir(m.inner, p)
    } else if op == OOpenDir {
        r = core.IoOpenDir(m.inner, p)
    }
    Post(io, op, 0, d, r, nil[..])
    return r
}

TRemove(io: Io, p: str): core.IoResult {
    return TPath(io, ORemove, p, HNone)
}

TListen(io: Io, addr: str): core.IoResult {
    return TPath(io, OListen, addr, HNew)
}

TDial(io: Io, addr: str): core.IoResult {
    return TPath(io, ODial, addr, HNew)
}

TMkdir(io: Io, p: str): core.IoResult {
    return TPath(io, OMkdir, p, HNone)
}

TOpenDir(io: Io, p: str): core.IoResult {
    return TPath(io, OOpenDir, p, HNew)
}

TRecv(io: Io, h: uint, buf: mut []u8): core.IoResult {
    d := FnvI(FnvSeed, i64(buf.Len()))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return TakeV(io, ORecv, h, d, buf, HNone, Bound(max: i64(buf.Len()), hdr: 0, unit: 1))
    }
    r := core.IoRecv(m.inner, h, buf)
    Post(io, ORecv, h, d, r, Filled(buf[..], r))
    return r
}

TSend(io: Io, h: uint, data: []u8): core.IoResult {
    d := Digest(FnvSeed, data)
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return TakeV(io, OSend, h, d, mut nil[..], HNone, Bound(max: i64(data.Len()), hdr: 0, unit: 0))
    }
    r := core.IoSend(m.inner, h, data)
    Post(io, OSend, h, d, r, nil[..])
    return r
}

TNow(io: Io): core.IoResult {
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        // A reading outside 0..TapeMaxInstant is a log no clock wrote
        // (Remaining would overflow on it): it diverges.
        return TakeV(io, ONow, 0, FnvSeed, mut nil[..], HNone, Bound(max: TapeMaxInstant, hdr: 0, unit: 0))
    }
    r := core.IoNow(m.inner)
    Post(io, ONow, 0, FnvSeed, r, nil[..])
    return r
}

// random and yield reach the inner Io through Io's own methods (core's
// IoRandom and IoYield are not exported), so their result is None or
// Canceled, as those methods map it.
CanceledOr(failed: bool): core.IoResult {
    if failed {
        return Fail(core.SysErr.Canceled)
    }
    return Ok(0)
}

// sleep reaches the inner Io through core.IoSleep and logs its result as it
// is, so only Canceled is a cancellation through a Recorder too:
// an inner Io with no OS answers Other, and core's check goes on. A freed
// Recorder's or Replayer's masked check, a sleep of 0, answers None, as a
// dead Sim run's does (SimSleep), unless a cancelled scope opened from the
// masked Io covers it (Halted). So does a Replayer's masked check
// that the log does not hold (TakeCheck).
TSleep(io: Io, nanos: i64): core.IoResult {
    d := FnvI(FnvSeed, nanos)
    m := ModeOf(io)
    var nil := NoOut()
    masked_check := nanos == 0 && (core.IoFlags(io) & 1) != 0
    if !m.ok {
        if masked_check && !Halted(io) {
            return Ok(0)
        }
        return Dead()
    }
    if m.replay {
        if masked_check {
            return TakeCheck(io, d)
        }
        return Take(io, OSleep, 0, d, mut nil[..], HNone)
    }
    r := core.IoSleep(m.inner, nanos)
    Post(io, OSleep, 0, d, r, nil[..])
    return r
}

// TakeCheck replays a masked check: the next record, when it is
// this check's, answers as recorded. A Replayer that has diverged, has run
// out of log or meets another op there cannot replay the call: it is
// marked diverged, as any unmatched call marks it, and the check answers
// None, since a replay that cannot go on is a dead run, not a scope
// cancellation, unless a cancelled scope opened from the masked Io covers
// it (Halted). So a NoCancel lock in cleanup takes a free mutex
// under a diverged Replayer, as under a dead Sim run.
TakeCheck(io: Io, d: u64): core.IoResult {
    e := EnterTape(io)
    if !e.ok {
        if Halted(io) {
            return Dead()
        }
        return Ok(0)
    }
    t := TapeOf(&e)
    var nil := NoOut()
    ReplayFires(t, false)
    r := TakeLocked(t, OSleep, 0, d, mut nil[..], HNone, Bound(max: -1, hdr: 0, unit: 0))
    diverged := t.diverged
    UnlockSlot(e.slot)
    if diverged {
        if Halted(io) {
            return Dead()
        }
        return Ok(0)
    }
    return r
}

TRandom(io: Io, buf: mut []u8): core.IoResult {
    d := FnvI(FnvSeed, i64(buf.Len()))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, ORandom, 0, d, buf, HNone)
    }
    var failed := false
    m.inner.Random(buf) else {
        failed = true
    }
    r := CanceledOr(failed)
    var nil := NoOut()
    if failed {
        Post(io, ORandom, 0, d, r, nil[..])
    } else {
        Post(io, ORandom, 0, d, r, buf[..])
    }
    return r
}

TYield(io: Io): core.IoResult {
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, OYield, 0, FnvSeed, mut nil[..], HNone)
    }
    var failed := false
    m.inner.Yield() else {
        failed = true
    }
    r := CanceledOr(failed)
    Post(io, OYield, 0, FnvSeed, r, nil[..])
    return r
}

TSignal(io: Io, s: u32): core.IoResult {
    d := FnvU64(FnvSeed, u64(s))
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, OSignal, 0, d, mut nil[..], HNone)
    }
    r := core.IoSignal(m.inner, s)
    Post(io, OSignal, 0, d, r, nil[..])
    return r
}

// The futex pair is not logged: it is the program's own synchronization
// (Mutex, Chan), not a call to the world outside. A Recorder forwards the
// wait and the wake to the inner Io, so a waiter sleeps as it would there:
// on the native table, natively; under a Sim, as a task the Sim sees
// blocked (its deadlock rule and its seed's order hold), or, for main, on
// the Sim's main word. A Replayer has no inner Io, so it parks the caller
// on the native table's wait (task.WaitWord), as natively. Either way the
// waiter sleeps until its waker's wake: a yield-and-return wait spun its
// carrier, and under a Recorder over a Sim it never let the waker run
// (main is outside the Sim's baton, so a spin is not a wait the Sim sees).
TWait(io: Io, addr: *u32, expected: u32): core.IoResult {
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if !m.replay {
        // A fire before the wait, and one that ended it Canceled, is
        // logged here, so a replay fires it before this wait and
        // the wait ends as it did: the wait itself is not logged.
        NoteFires(io)
        // m.inner carries the caller's NoCancel bit (ModeOf).
        if core.SyncWait(m.inner, WordAt(addr), expected) {
            NoteFires(io)
            return Fail(core.SysErr.Canceled)
        }
        return Ok(0)
    }
    TakeFires(io)
    if !rt.UsesTasks() {
        // No task exists, so only another OS thread can change the
        // word: a spurious return, after which the caller rereads it.
        return Ok(0)
    }
    // As the native table's wait: cancellable unless io is NoCancel.
    e := task.WaitWord(addr, expected, task.Cancellable(io))
    return if e == core.SysErr.None { Ok(0) } else { Fail(e) }
}

// NoteFires logs a Recorder's fires not logged yet (LogFires), for a wait
// that writes no record of its own, marked as a wait's (os 1). O(listed
// scopes squared); allocates the log's growth.
NoteFires(io: Io) {
    e := EnterTape(io)
    if !e.ok {
        return
    }
    t := TapeOf(&e)
    if !t.replay && t.dls.Len() > 0 {
        LogFires(t, true)
    }
    UnlockSlot(e.slot)
}

// TakeFires replays the fires a Replayer's log holds next (ReplayFires),
// for a wait that takes no record of its own: only those a wait logged
// (os 1), so a fire the recording logged before a later call's record
// waits for that call. One limit stays: two waits with no record between
// them, the first ended as recorded and a fire noted at the second, replay
// the fire at the first (waits are not logged). O(fires times listed
// scopes plus each fire's walk); allocates nothing.
TakeFires(io: Io) {
    e := EnterTape(io)
    if !e.ok {
        return
    }
    t := TapeOf(&e)
    if t.replay {
        ReplayFires(t, true)
    }
    UnlockSlot(e.slot)
}

// TWake wakes where TWait parked: through the inner Io for a Recorder (a
// Sim's wake also wakes the native buckets once it has tasks), also for a
// freed one whose tasks still run (HookMode), and on the native table for
// a Replayer. A wake with no waiter is a no-op, and a spurious one is
// allowed.
TWake(io: Io, addr: *u32, all: bool) {
    m := HookMode(io)
    if m.ok && !m.replay {
        core.SyncWake(m.inner, WordAt(addr), all)
        return
    }
    if rt.UsesTasks() {
        task.WakeWord(addr, all)
    }
}

// WordAt is the futex word a waiter or waker passed, as the atomic core's
// wait and wake take.
WordAt(addr: *u32): &raw.Atomic[u32] {
    unsafe "addr is the live word a Mutex or Chan passed to its table's wait or wake (IoTable.wait, wake), and a u32 has raw.Atomic[u32]'s layout" {
        return &*raw.PtrCast[u32, raw.Atomic[u32]](addr)
    }
}

TStat(io: Io, p: str, st: mut core.IoStat): core.IoResult {
    d := Digest(FnvSeed, p.AsBytes())
    m := ModeOf(io)
    var out: [20]u8 = [0; 20]
    if !m.ok {
        return Dead()
    }
    if m.replay {
        r := Take(io, OStat, 0, d, mut out[..], HNone)
        if r.err == core.SysErr.None {
            *st = GetStat(out[..], 0)
        }
        return r
    }
    r := core.IoStatPath(m.inner, p, st)
    if r.err == core.SysErr.None {
        PutStat(mut out[..], 0, *st)
        Post(io, OStat, 0, d, r, out[..])
    } else {
        Post(io, OStat, 0, d, r, out[0..0])
    }
    return r
}

TReadDir(io: Io, h: uint, name: mut []u8, st: mut core.IoStat): core.IoResult {
    d := FnvI(FnvSeed, i64(name.Len()))
    m := ModeOf(io)
    var out: [20]u8 = [0; 20]
    if !m.ok {
        return Dead()
    }
    if m.replay {
        // The payload is the stat record, then the name.
        var buf: [4116]u8 = [0; 4116]
        r := TakeV(io, OReadDir, h, d, mut buf[..], HNone, Bound(max: i64(name.Len()), hdr: StatSize, unit: 1))
        if r.err == core.SysErr.None && r.value > 0 {
            *st = GetStat(buf[..], 0)
            n := Cast[int](r.value)
            unsafe "TakeV bounds n by name.Len() and the payload by StatSize + n <= buf.Len()" {
                raw.Copy(raw.DataMut(mut name[0..n]), raw.Data(buf[StatSize..StatSize + n]), n)
            }
        }
        return r
    }
    r := core.IoReadDir(m.inner, h, name, st)
    if r.err == core.SysErr.None && r.value > 0 && r.value <= 4096 && Cast[int](r.value) <= name.Len() {
        var sb: [20]u8 = [0; 20]
        PutStat(mut sb[..], 0, *st)
        Post2(io, OReadDir, h, d, r, sb[0..StatSize], name[0..Cast[int](r.value)])
    } else {
        Post(io, OReadDir, h, d, r, out[0..0])
    }
    return r
}

TUdp(io: Io, addr: core.IoAddr): core.IoResult {
    d := FnvAddr(FnvSeed, addr)
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, OUdp, 0, d, mut nil[..], HNew)
    }
    r := core.IoUdp(m.inner, addr)
    Post(io, OUdp, 0, d, r, nil[..])
    return r
}

TSendTo(io: Io, h: uint, data: []u8, to: core.IoAddr): core.IoResult {
    d := FnvAddr(Digest(FnvSeed, data), to)
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return TakeV(io, OSendTo, h, d, mut nil[..], HNone, Bound(max: i64(data.Len()), hdr: 0, unit: 0))
    }
    r := core.IoSendTo(m.inner, h, data, to)
    Post(io, OSendTo, h, d, r, nil[..])
    return r
}

TUdpConnect(io: Io, a: *core.IoCallArgs): core.IoResult {
    var h: uint = 0
    var to := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    var has_addr := false
    unsafe "a is the caller's record, live until this returns" {
        h = (*a).h
        if (*a).pn >= SizeOf[core.IoAddr]() {
            to = raw.Read(raw.PtrCast[u8, core.IoAddr]((*a).p))
            has_addr = true
        }
    }
    d := if has_addr { FnvAddr(FnvSeed, to) } else { FnvSeed }
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        r := Take(io, OUdpConnect, h, d, mut nil[..], HNone)
        unsafe "a is the caller's record, live until this returns" {
            (*a).res = r
        }
        return r
    }
    r := core.IoCall(m.inner, a)
    Post(io, OUdpConnect, h, d, r, nil[..])
    return r
}

TTryRecvFrom(io: Io, a: *core.IoCallArgs): core.IoResult {
    var h: uint = 0
    var outn := 0
    unsafe "a is the caller's record, live until this returns" {
        h = (*a).h
        outn = (*a).outn
    }
    d := FnvI(FnvSeed, i64(outn))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if m.replay {
        var tmp: [65560]u8 = [0; 65560]
        var empty: [0]u8 = [0; 0]
        var got := 0
        r := TakeV2(io, OTryRecvFrom, h, d, mut tmp[..], mut empty[..], HNone, Bound(max: -1, hdr: 0, unit: 0), mut got)
        if r.err == core.SysErr.None && r.value >= 0 {
            if r.value > i64(outn) || i64(AddrSize) + r.value > i64(got) {
                e := EnterTape(io)
                if e.ok {
                    TapeOf(&e).diverged = true
                    UnlockSlot(e.slot)
                }
                return Fail(core.SysErr.Canceled)
            }
            from := GetAddr(tmp[..], 0)
            n := Cast[int](r.value)
            unsafe "TakeV2 and the check above bound n <= outn and AddrSize + n <= got" {
                if (*a).qn >= SizeOf[core.IoAddr]() {
                    raw.Write(raw.PtrCast[u8, core.IoAddr]((*a).q), from)
                }
                if n > 0 {
                    raw.Copy((*a).out, raw.Data(tmp[AddrSize..AddrSize + n]), n)
                }
                (*a).res = r
            }
        } else {
            unsafe "a is the caller's record, live until this returns" {
                (*a).res = r
            }
        }
        return r
    }
    r := core.IoCall(m.inner, a)
    var ab: [24]u8 = [0; 24]
    if r.err == core.SysErr.None && r.value >= 0 && r.value <= 65535 && r.value <= i64(outn) {
        unsafe "a is the caller's record, out holds outn bytes, q holds IoAddr" {
            var from := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
            if (*a).qn >= SizeOf[core.IoAddr]() {
                from = raw.Read(raw.PtrCast[u8, core.IoAddr]((*a).q))
            }
            PutAddr(mut ab[..], 0, from)
            Post2(io, OTryRecvFrom, h, d, r, ab[0..AddrSize], raw.Slice((*a).out, Cast[int](r.value)))
        }
    } else {
        Post(io, OTryRecvFrom, h, d, r, ab[0..0])
    }
    return r
}

// The payload is the sender's address, then the datagram's bytes.
TRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    d := FnvI(FnvSeed, i64(buf.Len()))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    if m.replay {
        var tmp: [65560]u8 = [0; 65560]
        r := TakeV(io, ORecvFrom, h, d, mut tmp[..], HNone, Bound(max: i64(buf.Len()), hdr: AddrSize, unit: 1))
        if r.err == core.SysErr.None {
            *from = GetAddr(tmp[..], 0)
            n := Cast[int](r.value)
            if n > 0 {
                unsafe "TakeV bounds n by buf.Len() and the payload by AddrSize + n <= tmp.Len()" {
                    raw.Copy(raw.DataMut(mut buf[0..n]), raw.Data(tmp[AddrSize..AddrSize + n]), n)
                }
            }
        }
        return r
    }
    r := core.IoRecvFrom(m.inner, h, buf, from)
    // The address and the bytes go into the record as two parts: no 64 KiB
    // temporary on the recording path.
    var ab: [24]u8 = [0; 24]
    if r.err == core.SysErr.None && r.value >= 0 && r.value <= 65535 && Cast[int](r.value) <= buf.Len() {
        PutAddr(mut ab[..], 0, *from)
        Post2(io, ORecvFrom, h, d, r, ab[0..AddrSize], buf[0..Cast[int](r.value)])
    } else {
        Post(io, ORecvFrom, h, d, r, ab[0..0])
    }
    return r
}

TResolve(io: Io, host: str, out: mut []core.IoAddr): core.IoResult {
    d := FnvI(Digest(FnvSeed, host.AsBytes()), i64(out.Len()))
    m := ModeOf(io)
    if !m.ok {
        return Dead()
    }
    var tmp: [736]u8 = [0; 736]
    if m.replay {
        r := TakeV(io, OResolve, 0, d, mut tmp[..], HNone, Bound(max: i64(out.Len()), hdr: 0, unit: AddrSize))
        if r.err == core.SysErr.None {
            n := Cast[int](r.value)
            for k in 0..n {
                out[k] = GetAddr(tmp[..], k * AddrSize)
            }
        }
        return r
    }
    r := core.IoResolve(m.inner, host, out)
    if r.err == core.SysErr.None && r.value >= 0 && r.value <= 32 && Cast[int](r.value) <= out.Len() {
        n := Cast[int](r.value)
        for k in 0..n {
            PutAddr(mut tmp[..], k * AddrSize, out[k])
        }
        Post(io, OResolve, 0, d, r, tmp[0..n * AddrSize])
    } else {
        Post(io, OResolve, 0, d, r, tmp[0..0])
    }
    return r
}

// Spawning is recorded as its result only; a replay refuses it, since its
// pipes would need an OS (Denied, as a Sim's spawn).
TSpawn(io: Io, args: []str, child: mut core.IoChild): core.IoResult {
    var d := FnvSeed
    for a in args {
        d = FnvU64(Digest(d, a.AsBytes()), 0)
    }
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Fail(core.SysErr.Denied)
    }
    r := core.IoSpawn(m.inner, args, child)
    Post(io, OSpawn, 0, d, r, nil[..])
    return r
}

TInherited(io: Io): core.IoResult {
    m := ModeOf(io)
    var nil := NoOut()
    if !m.ok {
        return Dead()
    }
    if m.replay {
        return Take(io, OInherited, 0, FnvSeed, mut nil[..], HNew)
    }
    r := core.IoInherited(m.inner)
    Post(io, OInherited, 0, FnvSeed, r, nil[..])
    return r
}
