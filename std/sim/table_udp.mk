// Why: simulated datagrams and name resolution use the network world's
// clock, fault gates, and packet queues.
// What: the datagram and resolver entries in SimTable.
// How: starts each call with the matching fault gate and releases its
// world entry, parking for receive only while the packet queue is empty.

import std.core

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
