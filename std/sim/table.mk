// Why: std reaches the OS only through an IoTable, so a Sim plugs in as a
// third table beside the native and direct ones; this file is where every
// Io call meets the simulated world.
//
// What: SimTable and its entries (SimOpen, SimRead, SimWrite, SimSync,
// SimListen, SimAccept, SimDial, SimRecv, SimSend, SimNow, SimSleep,
// SimRandom, SimYield, SimWait, SimWake, SimUdp, SimRecvFrom and the rest),
// and the disk latency bounds DiskLo and DiskHi.
//
// How: each entry decodes its Io (sim.mk's Enter), runs the gate (crash
// point, armed fault), does its work on the world under the slot's lock
// and returns. Every disk entry costs simulated time; the waiting rules
// follow.
//
// Called by a task that holds the scheduler's baton (sched.mk), an operation that would wait parks the
// task and hands the baton on. Called by any other task (main), a wait
// (a futex wait, a receive, an accept, a sleep, a yield) parks it
// natively while the run has tasks that may end it (MainWait); with none,
// it moves the clock to the event that ends the wait, or, when no event
// can, answers Canceled (a deadlock in a one-task run). Every disk entry
// costs a seeded 1 to 64 us of simulated time, so time.Now sees I/O take
// time.

import std.core
import std.raw
import std.task

const SimTable = core.IoTable(open: SimOpen, read: SimRead, write: SimWrite, size: SimSize,
    sync: SimSync, close: SimClose, rename: SimRename, remove: SimRemove, listen: SimListen,
    accept: SimAccept, dial: SimDial, recv: SimRecv, send: SimSend, now: SimNow,
    sleep: SimSleep, random: SimRandom, yield: SimYield, signal: SimSignal, wait: SimWait,
    wake: SimWake, mkdir: SimMkdir, stat: SimStat, opendir: SimOpenDir, readdir: SimReadDir,
    udp: SimUdp, sendto: SimSendTo, recvfrom: SimRecvFrom, resolve: SimResolve, spawn: SimSpawn,
    procwait: SimProcWait, prockill: SimProcKill, inherited: SimInherited,
    enter: SimEnter, leave: SimLeave, deadline: SimDeadline, stdio: SimStdio,
    call: SimCall)

const DiskLo: i64 = 1_000
const DiskHi: i64 = 64_000

Dead(): core.IoResult {
    return Fail(core.SysErr.Canceled)
}

// Begin is Enter plus the gate for fault kind fk; when its result's err is
// not None, the slot is already released and the result is the answer.
Begin(io: Io, fk: int, e: mut Entry): core.IoResult {
    return BeginH(io, fk, 0, e)
}

// BeginH is Begin for an entry on handle h: an injected Reset on a
// connection breaks it both ways, as a real reset does. A read or write of
// the simulated terminal, or of a pipe's or a Pty's end (their
// errors are Closed, never Reset), is no file's or socket's, so it takes
// no fault FailNext armed: the fault waits for the next file or socket
// operation. It still counts as a Sim operation, so no crash point moves.
BeginH(io: Io, fk: int, h: uint, e: mut Entry): core.IoResult {
    *e = Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&*e)
    var k := fk
    if k >= 0 && h != 0 && w.fault[k] != 0 && (FindHand(&*w, h, HTerm) >= 0 || IsPipeHand(&*w, h)) {
        k = FNone
    }
    g := w.Gate(k)
    if g.err == core.SysErr.Reset {
        c := FindHand(&*w, h, HConn)
        if c >= 0 {
            ResetConn(w, w.handles.Get(c).obj)
        }
    }
    if g.err != core.SysErr.None {
        Leave(&*e)
        e.ok = false
        return g
    }
    if w.handoff {
        w.handoff = false
        return Handoff(io, e)
    }
    return g
}

// Handoff parks the holder at a PCT change point, still runnable, so the
// task now of highest priority runs first and this operation runs after
// it (the baton is handed at an Io call to the task the
// stream chooses). Every entry looks its handle up after BeginH, so what
// ran meanwhile (a close, a Restart) is seen. A run that died meanwhile
// answers Canceled, with e released.
Handoff(io: Io, e: mut Entry): core.IoResult {
    w := WorldOf(&*e)
    i := FindT(&*w, w.holder)
    if i < 0 || !IsHolder(&*w) {
        return Ok(0)
    }
    w.tasks.Mut(i).state = TReady
    w.events += 1
    if !Block(io, e) {
        e.ok = false
        return Dead()
    }
    if WorldOf(&*e).dead {
        Leave(&*e)
        e.ok = false
        return Dead()
    }
    return Ok(0)
}

// DiskWait charges one disk operation's seeded latency. The
// baton's holder sleeps it as an event, so other tasks run meanwhile and
// every timer fires on time: this is also where two tasks' file
// operations interleave. Any other caller moves the clock, as a
// one-task run does. False when the run died meanwhile: e is released and
// the operation must not run.
DiskWait(io: Io, e: mut Entry): bool {
    w := WorldOf(&*e)
    lat := w.Latency(DiskLo, DiskHi)
    if !IsHolder(&*w) {
        // With main alone, the deadlines the clock passed fire here, and
        // the next operation in a scope one cut sees it.
        w.clock += lat
        MainAloneFire(w)
        return true
    }
    if BlockAs(io, e, BSleep, 0, w.clock + lat) != core.SysErr.None || WorldOf(&*e).dead {
        if e.ok {
            Leave(&*e)
            e.ok = false
        }
        return false
    }
    return true
}

// NotYet in a receive's os: data is in flight and arrives at the result's
// value (the clock); only a holder's receive answers it, and it then
// sleeps until that arrival.
const NotYet: i64 = 1

IsNotYet(r: core.IoResult): bool {
    return r.err == core.SysErr.Canceled && r.os == NotYet
}

// Await parks the holder until r's arrival, or until the network changes
// (a send, a close, a reset); false when the run died or deadlocked. A
// weighed wait (a stream receive's) gets its cut as NetBlock's does.
Await(io: Io, e: mut Entry, r: core.IoResult, weigh: bool, cut: mut i64): bool {
    var until: i64 = -1
    if IsNotYet(r) {
        until = r.value
    }
    return NetBlock(io, e, weigh, until, cut) == core.SysErr.None
}

// NetBlock parks the holder on the network as BlockAs does (BNet, until).
// A weighed wait (weigh: an accept's, a stream receive's) sets cut to the
// instant of the deadline fire that cancelled the holder's scope chain
// while it waited, at the choice that fired it (FireDeadlines' CutMark),
// else -1: the clock may pass that deadline with no choice (main's dial
// round trip, its clock reads, a file operation's disk latency), and an
// event before the choice may have readied the holder, so its retry
// weighs what it would take against cut (ConnCut, DataCut).
// O(tasks) besides the park; allocates nothing.
NetBlock(io: Io, e: mut Entry, weigh: bool, until: i64, cut: mut i64): core.SysErr {
    *cut = -1
    if !weigh {
        return BlockAs(io, e, BNet, 0, until)
    }
    w := WorldOf(&*e)
    i := FindT(&*w, w.holder)
    if i >= 0 {
        w.tasks.Mut(i).weigh = true
        w.tasks.Mut(i).cut = -1
    }
    st := BlockAs(io, e, BNet, 0, until)
    if e.ok {
        w2 := WorldOf(&*e)
        j := FindT(&*w2, w2.holder)
        if j >= 0 {
            if w2.tasks.Get(j).cut >= 0 {
                *cut = w2.tasks.Get(j).cut
            }
            w2.tasks.Mut(j).weigh = false
            w2.tasks.Mut(j).cut = -1
        }
    }
    return st
}

NoEntry(): Entry {
    return Entry(slot: 0, w: raw.Null[World](), ok: false)
}

SimOpen(io: Io, p: str, mode: u32): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FOpen, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskOpen(w, p, mode)
    Leave(&e)
    return r
}

SimRead(io: Io, h: uint, buf: mut []u8, pos: i64): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts, whether or not it would wait
    // (a write, or a receive that finds bytes, is Canceled natively in a
    // cancelled scope, so it is here too).
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := BeginH(io, FRead, h, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if FindHand(&*WorldOf(&e), h, HTerm) >= 0 {
        // The terminal's input is read by rt.TermWait and rt.TermRead
        // (term.mk), as std.term reads it.
        Leave(&e)
        return Fail(core.SysErr.Unsupported)
    }
    if FindHand(&*WorldOf(&e), h, HConn) >= 0 {
        pipe := IsPipeHand(&*WorldOf(&e), h)
        r := RecvLoop(io, mut e, h, buf)
        if pipe && r.err == core.SysErr.None && r.value > 0 {
            PipeTook(io)
        }
        return r
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    r := DiskRead(WorldOf(&e), h, buf, pos)
    Leave(&e)
    return r
}

// RecvLoop receives on connection h for the entry e holds; the holder of
// the baton, and a waiter outside the scheduler (main) while the run has
// tasks, wait for the network instead of a deadlock's Canceled. It
// releases e.
RecvLoop(io: Io, e: mut Entry, h: uint, buf: mut []u8): core.IoResult {
    if !IsHolder(&*WorldOf(&*e)) && !MainWaits(&*WorldOf(&*e)) {
        // Main alone, its bytes still in flight: NetRecv moves the clock to
        // their arrival, so a deadline before it fires first and cuts the
        // receive there, as a sleep's does.
        at := InFlight(&*WorldOf(&*e), h)
        if at > 0 && MainAloneUntil(io, WorldOf(&*e), at) {
            Leave(&*e)
            return Fail(core.SysErr.Canceled)
        }
    }
    var r := NetRecv(WorldOf(&*e), h, buf, IsHolder(&*WorldOf(&*e)))
    // A stream's receive is weighed; a pipe's is not.
    weigh := !IsPipeHand(&*WorldOf(&*e), h)
    for r.err == core.SysErr.Canceled && IsHolder(&*WorldOf(&*e)) {
        var cut: i64 = -1
        if !Await(io, e, r, weigh, mut cut) {
            if e.ok {
                Leave(&*e)
            }
            return Dead()
        }
        if cut >= 0 && DataCut(&*WorldOf(&*e), h, cut) {
            // A deadline that cancelled the wait came before the bytes:
            // Canceled with nothing taken, as natively.
            Leave(&*e)
            return Fail(core.SysErr.Canceled)
        }
        // A weighed take reads only the bytes that arrived by the cut:
        // natively the read woke for them, before the deadline, and bytes
        // that came after it stay for the next read.
        lim := if cut >= 0 { ArrivedBy(&*WorldOf(&*e), h, cut) } else { 0 }
        if lim > 0 && lim < buf.Len() {
            r = NetRecv(WorldOf(&*e), h, mut buf[0..lim], true)
        } else {
            r = NetRecv(WorldOf(&*e), h, buf, true)
        }
    }
    for r.err == core.SysErr.Canceled && !IsHolder(&*WorldOf(&*e)) && MainWaits(&*WorldOf(&*e)) {
        st := MainWait(io, e, -1)
        if !e.ok {
            return Dead()
        }
        if Halted(io) {
            // Woken in a cancelled scope (the cancel itself, or an event
            // after it): Canceled with nothing taken, as a native receive
            // the cancel walk ends, so the read can be made again.
            Leave(&*e)
            return Fail(core.SysErr.Canceled)
        }
        r = NetRecv(WorldOf(&*e), h, buf, false)
        if st != core.SysErr.None {
            // Deadlocked or cancelled: this answer stands.
            break
        }
    }
    if r.err == core.SysErr.Canceled {
        MainAloneCut(WorldOf(&*e))
    }
    Leave(&*e)
    return r
}

SimWrite(io: Io, h: uint, data: []u8, pos: i64): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts, whether or not it would wait
    // (a write, or a receive that finds bytes, is Canceled natively in a
    // cancelled scope, so it is here too).
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := BeginH(io, FWrite, h, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    var r := Fail(core.SysErr.Closed)
    if FindHand(&*WorldOf(&e), h, HTerm) >= 0 {
        // What a program writes to the simulated terminal is discarded,
        // all of it at once.
        Leave(&e)
        return Ok(i64(data.Len()))
    }
    if IsPipeHand(&*WorldOf(&e), h) {
        return PipeSendLoop(io, mut e, h, data)
    }
    if FindHand(&*WorldOf(&e), h, HConn) >= 0 {
        w := WorldOf(&e)
        r = NetSend(w, h, data)
        WakeNet(w)
        Settle(io, mut e)
    } else {
        if !DiskWait(io, mut e) {
            return Dead()
        }
        r = DiskWrite(WorldOf(&e), h, data, pos)
    }
    Leave(&e)
    return r
}

SimSize(io: Io, h: uint): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    w := WorldOf(&e)
    var r := Fail(core.SysErr.Closed)
    if FindHand(&*w, h, HFile) >= 0 {
        r = DiskSize(w, h)
    } else if HandKind(&*w, h) != 0 {
        // A socket or a directory stream has no size, as a pipe has none.
        r = Ok(0)
    }
    Leave(&e)
    return r
}

SimSync(io: Io, h: uint): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FSync, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskSync(w, h)
    Leave(&e)
    return r
}

SimClose(io: Io, h: uint): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    w := WorldOf(&e)
    r := if !DiskClose(w, h) && !NetClose(w, h) && !KidClose(w, h) && !TermClose(w, h) { Fail(core.SysErr.Closed) } else { Ok(0) }
    WakeNet(w)
    Settle(io, mut e)
    Leave(&e)
    return r
}

// SimShutdown is rt.SockShutdown under a Sim (SimCall): inline, no
// cancellation point, as natively (no Halted check); a peer waiting to
// receive is woken to read the end of the stream.
SimShutdown(io: Io, h: uint, how: i64): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    w := WorldOf(&e)
    r := NetShutdown(w, h, how)
    WakeNet(w)
    Settle(io, mut e)
    Leave(&e)
    return r
}

SimRename(io: Io, src: str, dst: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskRename(w, src, dst)
    Leave(&e)
    return r
}

SimRemove(io: Io, p: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskRemove(w, p)
    Leave(&e)
    return r
}

SimListen(io: Io, addr: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetListen(WorldOf(&e), addr)
    Leave(&e)
    return r
}

SimAccept(io: Io, h: uint): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FAccept, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    var r := NetAccept(WorldOf(&e), h)
    for r.err == core.SysErr.Canceled && IsHolder(&*WorldOf(&e)) {
        var cut: i64 = -1
        if NetBlock(io, mut e, true, -1, mut cut) != core.SysErr.None {
            if e.ok {
                Leave(&e)
            }
            return Dead()
        }
        if cut >= 0 && ConnCut(&*WorldOf(&e), h, cut) {
            // A deadline that cancelled the wait came before the
            // connection: Canceled with nothing taken, as natively; the
            // next accept takes it.
            Leave(&e)
            return Fail(core.SysErr.Canceled)
        }
        r = NetAccept(WorldOf(&e), h)
    }
    // A waiter outside the baton (main) waits for a dial as RecvLoop
    // waits for data, while a task of the run may still dial.
    for r.err == core.SysErr.Canceled && !IsHolder(&*WorldOf(&e)) && MainWaits(&*WorldOf(&e)) {
        st := MainWait(io, mut e, -1)
        if !e.ok {
            return Dead()
        }
        if Halted(io) {
            // As RecvLoop's: no connection is taken from the queue.
            Leave(&e)
            return Fail(core.SysErr.Canceled)
        }
        r = NetAccept(WorldOf(&e), h)
        if st != core.SysErr.None {
            break
        }
    }
    if r.err == core.SysErr.Canceled {
        MainAloneCut(WorldOf(&e))
    }
    Leave(&e)
    return r
}

SimDial(io: Io, addr: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FConnect, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    var rtt: i64 = 0
    r := NetDial(WorldOf(&e), addr, mut rtt)
    WakeNet(WorldOf(&e))
    // The connection exists at once (the server may accept it); the dial
    // returns a round trip later. The holder sleeps that round trip as an
    // event; a dead run still returns the handle, whose close is
    // then safe.
    if IsHolder(&*WorldOf(&e)) {
        if BlockAs(io, mut e, BSleep, 0, WorldOf(&e).clock + rtt) != core.SysErr.None {
            if e.ok {
                Leave(&e)
            }
            return r
        }
    } else {
        // Main alone: the deadlines the round trip passes fire, so the
        // scope reads Expired and the next operation in it is Canceled;
        // the dial itself, like the holder's, returns its connection.
        WorldOf(&e).clock += rtt
        MainAloneFire(WorldOf(&e))
        Settle(io, mut e)
    }
    Leave(&e)
    return r
}

SimRecv(io: Io, h: uint, buf: mut []u8): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts, whether or not it would wait
    // (a write, or a receive that finds bytes, is Canceled natively in a
    // cancelled scope, so it is here too).
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := BeginH(io, FRead, h, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    has_buf := WorldOf(&e).net_faults.stream_buffer > 0
    r := RecvLoop(io, mut e, h, buf)
    if has_buf && r.err == core.SysErr.None && r.value > 0 {
        PipeTook(io)
    }
    return r
}

SimSend(io: Io, h: uint, data: []u8): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts, whether or not it would wait
    // (a write, or a receive that finds bytes, is Canceled natively in a
    // cancelled scope, so it is here too).
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := BeginH(io, FWrite, h, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if WorldOf(&e).net_faults.stream_buffer > 0 && FindHand(&*WorldOf(&e), h, HConn) >= 0 {
        return PipeSendLoop(io, mut e, h, data)
    }
    w := WorldOf(&e)
    var r := NetSend(w, h, data)
    WakeNet(w)
    Settle(io, mut e)
    Leave(&e)
    return r
}

// now is not counted toward a crash and never fails. Each call moves the
// clock 1 us and, for main alone, fires the deadlines it passes, so a loop
// that polls the clock or Expired for a deadline ends.
SimNow(io: Io): core.IoResult {
    e := Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&e)
    w.clock += 1_000
    MainAloneFire(w)
    r := Ok(w.clock)
    Leave(&e)
    return r
}

// sleep moves the clock; a crashed run's sleep is Canceled, so a sleep
// loop in it ends. A waiter outside the baton (main) sleeps as a task
// does while the run has tasks: they run meanwhile, and it wakes when the
// clock reaches its deadline (MainDue), or when the run's last task ends,
// the clock then jumping to the deadline.
//
// A NoCancel mask masks scope cancellation, and a dead run (crashed,
// restarted or freed) is not one. So the cancellation check of a masked
// Io, a sleep of 0, answers None in a dead run: a NoCancel Lock of a free
// mutex in cleanup goes on. It is still Canceled under a cancelled scope
// opened from the masked Io (Halted), and every real wait of a dead
// run, a masked one included, stays Canceled.
SimSleep(io: Io, nanos: i64): core.IoResult {
    masked_check := nanos == 0 && (core.IoFlags(io) & 1) != 0
    var e := Enter(io)
    if !e.ok {
        if masked_check && !Halted(io) {
            return Ok(0)
        }
        return Dead()
    }
    w := WorldOf(&e)
    var r := Ok(0)
    if Halted(io) {
        // a sleep, a CancelPoint's sleep of 0 included, in a
        // cancelled scope answers Canceled first, as natively.
        r = Dead()
    } else if w.dead {
        if !masked_check {
            r = Dead()
        }
    } else if nanos > 0 && IsHolder(&*w) {
        // Others run while the holder sleeps; the clock jumps to its wake
        // when nothing else can run.
        if BlockAs(io, mut e, BSleep, 0, ClockAfter(&*w, nanos)) != core.SysErr.None {
            if e.ok {
                Leave(&e)
            }
            return Dead()
        }
    } else if nanos > 0 && MainWaits(&*w) {
        due := ClockAfter(&*w, nanos)
        if MainWait(io, mut e, due) != core.SysErr.None {
            r = Dead()
        }
        if !e.ok {
            return Dead()
        }
        w2 := WorldOf(&e)
        if r.err == core.SysErr.None && w2.clock < due {
            w2.clock = due
        }
    } else if nanos > 0 {
        // Main alone (no task of the run): the clock goes to the wake, or
        // to the first deadline on the way, which fires there; a deadline
        // that cancels this sleep's scope ends it with Canceled at that
        // clock, as natively.
        due := ClockAfter(&*w, nanos)
        var nd := NextDeadline(&*w)
        for nd >= 0 && nd <= due && r.err == core.SysErr.None {
            if nd > w.clock {
                w.clock = nd
            }
            FireDeadlines(w)
            if Halted(io) {
                r = Dead()
            }
            nd = NextDeadline(&*w)
        }
        if r.err == core.SysErr.None {
            w.clock = due
        }
    }
    Leave(&e)
    return r
}

// random draws from the world's stream: one seed, one sequence of bytes. A
// cancelled scope ends it first, drawing nothing, as the native entry's
// Stop does (Io.Random is Canceled there).
SimRandom(io: Io, buf: mut []u8): core.IoResult {
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    e := Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&e)
    var r := Ok(0)
    if w.dead {
        r = Dead()
    } else {
        var k := 0
        for k < buf.Len() {
            var x := w.Next()
            var j := 0
            for j < 8 && k < buf.Len() {
                buf[k] = Trunc[u8](x)
                x = x >> 8
                j += 1
                k += 1
            }
        }
    }
    Leave(&e)
    return r
}

// A yield by a waiter outside the baton (main) while the run has tasks is
// a sleep of 1 us: the run goes on until the clock passes it, or until
// every task waits.
SimYield(io: Io): core.IoResult {
    var e := Enter(io)
    if !e.ok {
        return Dead()
    }
    w := WorldOf(&e)
    if !w.dead && !Halted(io) && !IsHolder(&*w) && MainWaits(&*w) {
        Leave(&e)
        return SimSleep(io, 1_000)
    }
    var r := Ok(0)
    if !w.dead {
        // A yield costs 1 us, as a now does, so a yield loop that waits for
        // a sleeper lets the clock reach its deadline; for main alone the
        // deadlines it passes fire, so a yield loop in a scope with a
        // deadline ends Canceled there.
        w.clock += 1_000
        MainAloneFire(w)
    }
    if w.dead || Halted(io) {
        // a yield in a cancelled scope answers Canceled, as natively.
        r = Dead()
    } else if IsHolder(&*w) {
        // The yielder drops below every task, so a yield loop hands the
        // baton round; it stays runnable.
        _ = ChangePoint(w)
        i := FindT(&*w, w.holder)
        if i >= 0 {
            Demote(w, i)
            w.tasks.Mut(i).state = TReady
            if !Block(io, mut e) {
                return Dead()
            }
        }
    }
    Leave(&e)
    return r
}

// No signal reaches a simulation, so a signal wait never ends on its own:
// it waits as a blocked operation that only its scope's cancel or
// deadline ends (Canceled, the scope Expired at a deadline), and a run
// with nothing else to do deadlocks (Canceled), as before. It is no Sim
// operation (no crash point or fault), as a sleep is none.
SimSignal(io: Io, s: u32): core.IoResult {
    if Halted(io) {
        return Dead()
    }
    var e := Enter(io)
    if !e.ok {
        return Dead()
    }
    for {
        w := WorldOf(&e)
        if w.dead {
            Leave(&e)
            return Dead()
        }
        if IsHolder(&*w) {
            if BlockAs(io, mut e, BNet, 0, -1) != core.SysErr.None {
                if e.ok {
                    Leave(&e)
                }
                return Dead()
            }
            continue
        }
        if MainWaits(&*w) {
            // Asked again from the top when it returns: the run's last task
            // may have ended meanwhile, leaving main alone.
            st := MainWait(io, mut e, -1)
            if !e.ok {
                return Dead()
            }
            if Halted(io) || st != core.SysErr.None {
                MainAloneCut(WorldOf(&e))
                Leave(&e)
                return Dead()
            }
            continue
        }
        // Main alone: the earliest deadline fires, else a deadlock.
        MainAloneCut(w)
        Leave(&e)
        return Dead()
    }
}

// The futex wait: a word that already differs answers at once (the
// kernel's rule). The baton's holder parks until a wake of the word; a
// task outside the scheduler (main) waits natively on MainWords[slot]
// while the run has tasks, which wake it through SimWake (a spurious
// return is allowed: the caller rechecks its word); with none, nothing
// can change the word, so the wait is a deadlock (Canceled), as it is
// when the run deadlocks or its last task ends while main waits. A waiter
// woken by a wake owes the run a note (task.HookOwe): no choice is made
// until its next leaf.
SimWait(io: Io, addr: *u32, expected: u32): core.IoResult {
    var e := Enter(io)
    if !e.ok {
        return Dead()
    }
    // The futex compare, a seq_cst load as the native wait's: the edge is
    // the waker's store before its SimWake, which a later compare sees.
    var cur: u32 = 0
    unsafe "addr is the word the caller waits on, live for the call (IoTable.wait)" {
        cur = (*raw.PtrCast[u32, raw.Atomic[u32]](addr)).Load()
    }
    if cur != expected {
        Leave(&e)
        return Ok(0)
    }
    w := WorldOf(&e)
    if IsHolder(&*w) {
        err := BlockAs(io, mut e, BWord, raw.Addr(addr), -1)
        if e.ok {
            Leave(&e)
        }
        if err != core.SysErr.None {
            return Dead()
        }
        return Ok(0)
    }
    if w.tasks.Len() == 0 {
        Leave(&e)
        return Dead()
    }
    err := MainWait(io, mut e, -1)
    if !e.ok {
        return Dead()
    }
    unsafe "addr is the word the caller waits on, live for the call (IoTable.wait)" {
        cur = (*raw.PtrCast[u32, raw.Atomic[u32]](addr)).Load()
    }
    Leave(&e)
    // The word first: a wake that came before a deadlock still counts.
    if cur != expected {
        return Ok(0)
    }
    return core.IoResult(value: 0, err: err, os: 0)
}

// MainWait parks a waiter outside the scheduler (main) until a wake, a
// network event or a deadlock of the run bumps MainWords[slot], or, with
// a due of 0 or more (a sleep), until the clock reaches due (MainDue) or
// the run's last task ends; e is held on entry and again on return unless
// the Sim died (e not ok). Canceled when the run deadlocked meanwhile (an
// event wait only) or the waiter's scope was cancelled, else None. It
// returns only with the baton free, and the waiter then owes the run a
// note (Owe), so no choice is made before its next leaf: it takes its
// answer and runs alone until then.
MainWait(io: Io, e: mut Entry, due: i64): core.SysErr {
    w := WorldOf(&*e)
    // Counted, so a wake or a deadlock bumps MainWords[slot]. No lost wake:
    // mc is loaded under the slot's lock and every bump is made under it
    // (sched.mk, Bump's argument). The wait's own leaf sends main's note
    // first, so its tasks may start: that bump, too, follows mc.
    dl := w.dlk
    w.mwait += 1
    a := raw.Addr(task.Current())
    if a != 0 {
        // Out of memory: a wake does not hold choices for this waiter
        // (runs may then differ, as for AddPend).
        _ = w.mains.Push(MainRec(a: a, due: due, io: io))
    }
    slot := e.slot
    var err := core.SysErr.None
    for {
        mc := MainAtomic(slot).Load()
        Leave(&*e)
        err = task.WaitWord(MainWord(slot), mc, task.Cancellable(io))
        *e = Enter(io)
        if !e.ok {
            return core.SysErr.Canceled
        }
        w2 := WorldOf(&*e)
        if err != core.SysErr.None || w2.dlk != dl || !MainWaits(&*w2) {
            break
        }
        // A return with the word unchanged is spurious, and a sleeper that
        // another waiter's wake bumped before its deadline sleeps on.
        if MainAtomic(slot).Load() != mc && (due < 0 || w2.clock >= due) {
            break
        }
    }
    // Its turn: the baton is free. A waiter woken while a task holds it
    // (a wake made by the holder, a cancel) waits, pending so that no
    // choice is made meanwhile, until the holder's next leaf (Decide bumps
    // MainWords[slot] for it).
    var w3 := WorldOf(&*e)
    for a != 0 && w3.holder != 0 && MainWaits(&*w3) {
        AddPend(w3, a)
        w3.mturn = true
        mc := MainAtomic(slot).Load()
        Leave(&*e)
        _ = task.WaitWord(MainWord(slot), mc, false)
        *e = Enter(io)
        if !e.ok {
            return core.SysErr.Canceled
        }
        w3 = WorldOf(&*e)
    }
    w3.mwait -= 1
    if w3.dlk != dl && due < 0 {
        err = core.SysErr.Canceled
    }
    if a == 0 {
        return err
    }
    DropMain(w3, a)
    Leave(&*e)
    Owe(io, a)
    *e = Enter(io)
    return err
}

// MainWaits: a waiter outside the scheduler may wait on the run, since a
// task of it may still answer (a wake, a send).
MainWaits(w: &World): bool {
    return w.hooked && w.tasks.Len() > 0
}

// Owe: the native waiter at a runs again and may still wake or spawn a
// task of the run, so no choice is made, and no deadlock called, until
// its next leaf sends the note it now owes (sched.mk, Births and ends).
// The wake that woke it made it pending already (SimWake); a waiter that
// owes no note (none could be set) is not waited for.
Owe(io: Io, a: uint) {
    owed := task.HookOwe(io)
    var e := Enter(io)
    if !e.ok {
        return
    }
    w := WorldOf(&e)
    if owed && w.hooked && w.tasks.Len() > 0 {
        AddPend(w, a)
    } else {
        DropPend(w, a)
    }
    Leave(&e)
}

// DropMain forgets one native waiter at a.
DropMain(w: mut World, a: uint) {
    for i in 0..w.mains.Len() {
        if w.mains.Get(i).a == a {
            _ = w.mains.RemoveAt(i)
            return
        }
    }
}

// The wake: the tasks blocked on addr become runnable in the order they
// blocked (one, or all), and a native waiter (main) is woken.
SimWake(io: Io, addr: *u32, all: bool) {
    var e := Enter(io)
    if !e.ok {
        return
    }
    w := WorldOf(&e)
    hooked := w.hooked
    // Under the slot's lock, so a native waiter's mc load precedes the
    // bump or its compare sees it (MainWait); before the choice, so the
    // waiters are pending for it.
    if w.mwait > 0 {
        WakeMains(w)
    }
    if hooked {
        WakeAddr(w, raw.Addr(addr), all)
        Settle(io, mut e)
    }
    Leave(&e)
    if hooked {
        task.WakeWord(addr, all)
    }
}

SimMkdir(io: Io, p: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskMkdir(w, p)
    Leave(&e)
    return r
}

SimStat(io: Io, p: str, st: mut core.IoStat): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskStat(w, p, st)
    Leave(&e)
    return r
}

SimOpenDir(io: Io, p: str): core.IoResult {
    // A cancelled scope ends the operation before it starts, as the
    // native entry's Stop does.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FOpen, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !DiskWait(io, mut e) {
        return Dead()
    }
    w := WorldOf(&e)
    r := DiskOpenDir(w, p)
    Leave(&e)
    return r
}

SimReadDir(io: Io, h: uint, name: mut []u8, st: mut core.IoStat): core.IoResult {
    // The directory fill is a pool operation natively, checked before it
    // starts.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FRead, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := DiskReadDir(WorldOf(&e), h, name, st)
    Leave(&e)
    return r
}

SimUdp(io: Io, addr: core.IoAddr): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetUdp(WorldOf(&e), addr)
    Leave(&e)
    return r
}

SimSendTo(io: Io, h: uint, data: []u8, to: core.IoAddr): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FWrite, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetSendTo(WorldOf(&e), h, data, to)
    WakeNet(WorldOf(&e))
    Settle(io, mut e)
    Leave(&e)
    return r
}

SimRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    // As the native entries' Stop does, a cancelled scope
    // ends the operation before it starts.
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FRead, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    if !IsHolder(&*WorldOf(&e)) && !MainWaits(&*WorldOf(&e)) {
        // Main alone, a datagram in flight: NetRecvFrom moves the clock to
        // its arrival, so a deadline before it fires first and cuts the
        // receive there, as RecvLoop's does.
        at := DgramInFlight(&*WorldOf(&e), h)
        if at > 0 && MainAloneUntil(io, WorldOf(&e), at) {
            Leave(&e)
            return Fail(core.SysErr.Canceled)
        }
    }
    var r := NetRecvFrom(WorldOf(&e), h, buf, from, IsHolder(&*WorldOf(&e)))
    for r.err == core.SysErr.Canceled && IsHolder(&*WorldOf(&e)) {
        // A datagram's wait is not weighed.
        var cut: i64 = -1
        if !Await(io, mut e, r, false, mut cut) {
            if e.ok {
                Leave(&e)
            }
            return Dead()
        }
        r = NetRecvFrom(WorldOf(&e), h, buf, from, true)
    }
    // A waiter outside the baton (main) waits for a datagram as RecvLoop
    // waits for data, while a task of the run may still send one.
    for r.err == core.SysErr.Canceled && !IsHolder(&*WorldOf(&e)) && MainWaits(&*WorldOf(&e)) {
        st := MainWait(io, mut e, -1)
        if !e.ok {
            return Dead()
        }
        if Halted(io) {
            // As RecvLoop's: no datagram is taken.
            Leave(&e)
            return Fail(core.SysErr.Canceled)
        }
        r = NetRecvFrom(WorldOf(&e), h, buf, from, false)
        if st != core.SysErr.None {
            break
        }
    }
    if r.err == core.SysErr.Canceled {
        // Main alone with nothing queued: the earliest deadline fires, as
        // RecvLoop's and SimAccept's.
        MainAloneCut(WorldOf(&e))
    }
    Leave(&e)
    return r
}

SimUdpConnect(io: Io, h: uint, to: core.IoAddr): core.IoResult {
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FConnect, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetUdpConnect(WorldOf(&e), h, to)
    Leave(&e)
    return r
}

SimTryRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut core.IoAddr): core.IoResult {
    if Halted(io) {
        return Fail(core.SysErr.Canceled)
    }
    var e := NoEntry()
    g := Begin(io, FRead, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetTryRecvFrom(WorldOf(&e), h, buf, from)
    Leave(&e)
    return r
}

SimResolve(io: Io, host: str, out: mut []core.IoAddr): core.IoResult {
    var e := NoEntry()
    g := Begin(io, FNone, mut e)
    if g.err != core.SysErr.None {
        return g
    }
    r := NetResolve(WorldOf(&e), host, out)
    Leave(&e)
    return r
}

// The table's spawn entry: a simulation's children are made by the
// call entry's spawn (proc.mk), whose request this form cannot
// carry.
SimSpawn(io: Io, args: []str, child: mut core.IoChild): core.IoResult {
    return Fail(core.SysErr.Denied)
}

// SimProcWait waits for a simulated child (proc.mk's ProcWait).
SimProcWait(io: Io, h: uint): core.IoResult {
    return ProcWait(io, h)
}

// SimProcKill kills a simulated child, as a signal 9 (proc.mk).
SimProcKill(io: Io, h: uint): core.IoResult {
    return CallSignal(io, h, 9)
}

SimInherited(io: Io): core.IoResult {
    return Fail(core.SysErr.NotFound)
}
