// Why: a simulated run must fire scope deadlines, read the wall clock and
// write the process streams in an order the seed fixes, never one real time
// chooses, so that a seed replays the same run.
//
// What: SimDl and the World's armed deadlines, SimDeadline (SimTable's
// deadline entry), FireDeadlines, the MainAlone* helpers, SimWall, SimStdio
// and SimCall, SimTable's call entry for the OS operations a Sim answers.
//
// How: a deadline is a (record, clock) pair in the World's dls, fired at a
// choice (Decide's FireDeadlines) once the clock has passed it, and an event
// the clock jumps to when no task can run. Choices are fixed by the seed, so
// a replay fires each deadline at the same point. With main alone in the run
// (no task, so no choice), every clock move of main fires the deadlines it
// passes: a sleep, the disk latency, a now, a wall read, a yield, a dial's
// round trip, Sim.Advance and Sim.Run (MainAloneFire).

import std.core
import std.raw
import std.rt
import std.task

// SimDl is one armed scope deadline: the scope record's address and the
// clock at which it fires.
struct SimDl {
    rec: uint
    at: i64
}

// WallStart is 2026-01-01T00:00:00Z in nanoseconds since the Unix epoch,
// the simulated wall clock at ClockStart on every seed.
const WallStart: i64 = 1_767_225_600_000_000_000

// SimDeadline is SimTable's deadline entry: op 0 arms rec's deadline nanos
// from the simulated clock (nanos <= 0 fires it at once), op 1 disarms it
// (std.task's Join). A dead run arms nothing and answers Canceled.
SimDeadline(io: Io, rec: *u8, nanos: i64, op: u32): core.IoResult {
    var e := Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&e)
    a := raw.Addr(rec)
    if op == 1 {
        DropDeadline(w, a)
        Leave(&e)
        return Ok(0)
    }
    if nanos <= 0 {
        _ = task.DeadlineSet(rec, w.clock)
        DropDeadline(w, a)
        // Fired here, in the caller's turn: a deadline that has passed.
        task.DeadlineFireRec(rec)
        w.events += 1
        Leave(&e)
        return Ok(0)
    }
    at := if nanos > 0x7FFF_FFFF_FFFF_FFFE - w.clock { 0x7FFF_FFFF_FFFF_FFFE } else { w.clock + nanos }
    if task.DeadlineSet(rec, at) {
        // The earliest counts (DeadlineSet said this one is): a scope has
        // at most one entry.
        DropDeadline(w, a)
        if !w.dls.Push(SimDl(rec: a, at: at)) {
            Leave(&e)
            rt.Fatal("std.sim: cannot grow the deadline list")
        }
    }
    Leave(&e)
    return Ok(0)
}

// ClockAfter is the clock nanos > 0 from now, saturated below the
// largest i64 as SimDeadline's arming and the native timer's deadline are,
// so a sleep of any Duration cannot overflow the clock.
ClockAfter(w: &World, nanos: i64): i64 {
    if nanos > 0x7FFF_FFFF_FFFF_FFFE - w.clock {
        return 0x7FFF_FFFF_FFFF_FFFE
    }
    return w.clock + nanos
}

// DropDeadline removes rec's entry, if any.
DropDeadline(w: mut World, a: uint) {
    for i in 0..w.dls.Len() {
        if w.dls.Get(i).rec == a {
            _ = w.dls.RemoveAt(i)
            return
        }
    }
}

// FireDeadlines fires every deadline the clock has reached, earliest
// first, ties in arming order, as the native timer pops them,
// so of an ancestor and a descendant both due the earlier reads Expired;
// each fire sets its scope's flags, and CancelWaits, which runs next,
// makes the waits it cancelled runnable. Around the fires CutMark gives each
// weighed network waiter the instant of the fire that cancelled its own
// scope chain, which its take is weighed against. O(due
// deadlines x (armed deadlines + tasks)); allocates nothing.
FireDeadlines(w: mut World) {
    var first := true
    for {
        var j := -1
        for i in 0..w.dls.Len() {
            at := w.dls.Get(i).at
            if at <= w.clock && (j < 0 || at < w.dls.Get(j).at) {
                j = i
            }
        }
        if j < 0 {
            return
        }
        if first {
            CutMark(w, -2)
            first = false
        }
        d := w.dls.Get(j)
        _ = w.dls.RemoveAt(j)
        unsafe "an armed deadline's scope is live: its Join disarms it (op 1) under this slot's lock first" {
            task.DeadlineFireRec(raw.FromAddr[u8](d.rec))
        }
        w.events += 1
        CutMark(w, d.at)
    }
}

// MainAloneFire fires the deadlines the clock has passed when the caller
// moved it outside the baton with main alone in the run (no task, so no
// choice will fire them); with tasks, or for the holder, the next
// choice fires them (Decide), so a replay fires each at the same point.
MainAloneFire(w: mut World) {
    if !IsHolder(&*w) && !MainWaits(&*w) {
        FireDeadlines(w)
    }
}

// MainAloneCut is a deadlock's answer for main alone in the run (no task,
// so no choice fires a deadline): the clock jumps to the earliest armed
// deadline, which fires, so the scope it cut reads Expired, as a native
// wait cut by its deadline does; with none armed nothing changes. The
// wait's answer stays Canceled.
MainAloneCut(w: mut World) {
    if MainWaits(&*w) || IsHolder(&*w) {
        return
    }
    nd := NextDeadline(&*w)
    if nd < 0 {
        return
    }
    if nd > w.clock {
        w.clock = nd
    }
    FireDeadlines(w)
}

// MainAloneUntil is a wait of main alone in the run for an event due at
// at (an arrival): the clock goes to each armed deadline at or before at,
// in order, which fires there, as a sleep's do (table.mk's SimSleep); true
// when one cancelled io's scope, the clock then at that deadline, so the
// wait ends Canceled there, as natively; false with the deadlines before
// at fired and the clock not past them, the caller then moving it to at.
MainAloneUntil(io: Io, w: mut World, at: i64): bool {
    var nd := NextDeadline(&*w)
    for nd >= 0 && nd <= at {
        if nd > w.clock {
            w.clock = nd
        }
        FireDeadlines(w)
        if Halted(io) {
            return true
        }
        nd = NextDeadline(&*w)
    }
    return false
}

// NextDeadline is the earliest armed deadline, or -1.
NextDeadline(w: &World): i64 {
    var b: i64 = -1
    for i in 0..w.dls.Len() {
        at := w.dls.Get(i).at
        if b < 0 || at < b {
            b = at
        }
    }
    return b
}

// SimWall is SimCall's answer to core.IoOpWall: WallStart plus the simulated
// clock's progress since ClockStart. Like now, a read moves the clock by
// 1 us, so two reads differ, and fires the deadlines it passes for main
// alone (MainAloneFire).
SimWall(io: Io): core.IoResult {
    e := Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&e)
    w.clock += 1_000
    MainAloneFire(w)
    r := Ok(WallStart + (w.clock - ClockStart))
    Leave(&e)
    return r
}

// SimStdio is SimTable's stdio entry: the bytes go to the real
// process stream, written in the caller's turn, so the run's order is the
// streams' order; a dead run writes nothing (Canceled, which the sink
// reports as WriteError). The task keeps its turn throughout, so the
// schedule stays the seed's: the bytes are written on this
// thread (core.StdWriteHere), and a wait for the stream's lock, held by a
// thread outside the Sim (a native task's write, another Sim), parks the
// task natively with its hooks muted (core's StreamPut mutes a hooked
// task's hooks while it holds or waits for a stream's lock), so the Sim's
// leave hook never runs and no other task of the Sim is given the turn at
// a point real time chose; the holder still gets the carrier back.
SimStdio(io: Io, i: u32, data: []u8): core.IoResult {
    e := Enter(io)
    if !e.ok {
        return Dead()
    }
    Leave(&e)
    ok := core.StdWriteHere(rt.Proc(), i, rt.DirectIo(), rt.NativeIo(), data)
    if ok {
        return Ok(i64(data.Len()))
    }
    return core.IoResult(value: 0, err: core.SysErr.Other, os: 0)
}

// SimCall is SimTable's call entry: the simulated
// world answers the wall clock (core.IoOpWall, SimWall), a listen with
// options (core.IoOpListen, as SimListen), a stream's shutdown
// (core.IoOpShutdown, SimShutdown), the file
// operations of fscall.mk (lock, unlock, stat, a directory opened and an
// entry removed inside a held one), the processes, pipes and
// pseudo-terminals of proc.mk and the terminal of term.mk (
// its open, mode, size, wait and read); every other OS operation of core's
// IoOp constants is Unsupported, which every std caller reports as its
// own error. A Sim never runs a.job, which would reach the real OS. An op
// joins here when a simulated test of std.fs or std.net needs it.
SimCall(io: Io, a: *core.IoCallArgs): core.IoResult {
    unsafe "a is the caller's record, live until this returns" {
        if (*a).op == core.IoOpWall {
            return SimWall(io)
        }
        if (*a).op == core.IoOpListen {
            // rt.ListenWith: the simulated network has no accept queue to
            // size and no TIME_WAIT, so the options change nothing.
            return SimListen(io, raw.Str((*a).p, (*a).pn))
        }
        if (*a).op == core.IoOpShutdown {
            return SimShutdown(io, (*a).h, (*a).w.a)
        }
        if (*a).op == core.IoOpUdpConnect {
            if (*a).pn < SizeOf[core.IoAddr]() {
                if Halted(io) {
                    return Fail(core.SysErr.Canceled)
                }
                return Fail(core.SysErr.InvalidAddress)
            }
            to := raw.Read(raw.PtrCast[u8, core.IoAddr]((*a).p))
            return SimUdpConnect(io, (*a).h, to)
        }
        if (*a).op == core.IoOpTryRecvFrom {
            var empty: [0]u8 = [0; 0]
            var from := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
            buf := if (*a).outn > 0 { raw.SliceMut((*a).out, (*a).outn) } else { mut empty[..] }
            r := SimTryRecvFrom(io, (*a).h, buf, mut from)
            if r.err == core.SysErr.None && r.value >= 0 && (*a).qn >= SizeOf[core.IoAddr]() {
                raw.Write(raw.PtrCast[u8, core.IoAddr]((*a).q), from)
            }
            return r
        }
    }
    if r := FsCall(io, a) {
        return r
    }
    if r := ProcCall(io, a) {
        return r
    }
    if r := TermCall(io, a) {
        return r
    }
    return core.IoResult(value: 0, err: core.SysErr.Unsupported, os: 0)
}
