// Why: QConn implements net.MuxEngine across concurrent reader/writer tasks
// and a background Drive task without holding eng across blocking waits or I/O.
//
// What: QConn.MuxOpen, MuxAccept, MuxRead, MuxWrite, MuxCredit, MuxWait,
// MuxReset, MuxStop, MuxRelease, MuxLimits, MuxUsable, MuxPeerAddr, MuxClose,
// QConn.Drive, and small lock-scoped helper methods.
//
// How: each blocking operation polls under eng.Lock(nc), allocates a PartyWant
// slot on miss, releases eng before FlushSend or lead.Wait, and frees the
// party slot on success, error, or cancellation.

import std.net
import std.sync.lead
import std.time

// Result of one non-blocking write attempt inside MuxWrite.
enum WriteAttempt {
    Done
    Continue
    Wait
}

// Opens a stream (MuxEngine.Open).
QConn.MuxOpen(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
    try self.EnsureHandshake(io)
    nc := io.NoCancel()
    var party := -1
    for {
        var g := self.eng.Lock(nc).Must()
        e := mut *g.Mut()
        if e.IsDead() {
            if party >= 0 {
                e.FreeParty(party)
            }
            err := e.ClosedError()
            g.Unlock()
            fail err
        }
        res := e.streams.Open(e.h.Alloc(), uni) else {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            fail OutOfMemory
        }
        if id := res {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            return some((id: id, gen_: 0))
        }
        if !wait {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            // Flush STREAMS_BLOCKED if queued.
            self.FlushSend(nc) else {}
            return none
        }
        if party < 0 {
            party = e.AllocParty(PartyWant.Open(uni: uni)) else {
                g.Unlock()
                fail OutOfMemory
            }
        }
        g.Unlock()
        // Flush STREAMS_BLOCKED before waiting.
        self.FlushSend(nc) else {}
        lead.Wait(self, io, party) else {
            self.ReleaseParty(nc, party)
            fail Canceled
        }
    }
}

// Accepts a peer-initiated stream (MuxEngine.Accept).
QConn.MuxAccept(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
    try self.EnsureHandshake(io)
    nc := io.NoCancel()
    var party := -1
    for {
        var g := self.eng.Lock(nc).Must()
        e := mut *g.Mut()
        if res := e.streams.Accept(uni) {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            return some((id: res, gen_: 0))
        }
        if e.IsDead() {
            if party >= 0 {
                e.FreeParty(party)
            }
            err := e.ClosedError()
            g.Unlock()
            fail err
        }
        if !wait {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            return none
        }
        if party < 0 {
            party = e.AllocParty(PartyWant.Accept(uni: uni)) else {
                g.Unlock()
                fail OutOfMemory
            }
        }
        g.Unlock()
        lead.Wait(self, io, party) else {
            self.ReleaseParty(nc, party)
            fail Canceled
        }
    }
}

// Reads bytes from stream id (MuxEngine.Read).
QConn.MuxRead(&self, io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): net.Error!?int {
    _ = gen_
    nc := io.NoCancel()
    var party := -1
    for {
        var g := self.eng.Lock(nc).Must()
        e := mut *g.Mut()
        if e.streams.IsReadable(id) {
            res := e.streams.Read(e.h.Alloc(), id, buf) else err {
                if party >= 0 {
                    e.FreeParty(party)
                }
                ne := MapQuicError(&err, true)
                g.Unlock()
                fail ne
            }
            if party >= 0 {
                e.FreeParty(party)
            }
            need_flush := e.streams.HasPending()
            g.Unlock()
            if need_flush {
                self.FlushSend(nc) else {}
            }
            return res
        }
        if e.IsDead() {
            if party >= 0 {
                e.FreeParty(party)
            }
            err := e.ClosedError()
            g.Unlock()
            fail err
        }
        if !wait {
            if party >= 0 {
                e.FreeParty(party)
            }
            g.Unlock()
            return none
        }
        if party < 0 {
            party = e.AllocParty(PartyWant.Read(id: id)) else {
                g.Unlock()
                fail OutOfMemory
            }
        }
        g.Unlock()
        lead.Wait(self, io, party) else {
            self.ReleaseParty(nc, party)
            fail Canceled
        }
    }
}

// Performs one locked write or party allocation step for MuxWrite.
QConn.TryWriteStep(&self, nc: Io, id: i64, data: []u8, off: int, fin: bool, party: int): net.Error!(step: WriteAttempt, new_off: int, party: int) {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    var p := party
    if e.IsDead() {
        if p >= 0 {
            e.FreeParty(p)
        }
        err := e.ClosedError()
        g.Unlock()
        fail err
    }
    if e.streams.CanWriteNow(id, MaxStreamSendBuf) {
        rem := data.Len() - off
        take := if rem > int(MaxStreamSendBuf) { int(MaxStreamSendBuf) } else { rem }
        is_fin := fin && (off + take == data.Len())
        _ = e.streams.Write(e.h.Alloc(), id, data[off..off + take], is_fin) else err {
            if p >= 0 {
                e.FreeParty(p)
            }
            ne := MapQuicError(&err, true)
            g.Unlock()
            fail ne
        }
        next_off := off + take
        if next_off == data.Len() && (!fin || is_fin) {
            if p >= 0 {
                e.FreeParty(p)
            }
            g.Unlock()
            return (step: WriteAttempt.Done, new_off: next_off, party: -1)
        }
        g.Unlock()
        return (step: WriteAttempt.Continue, new_off: next_off, party: p)
    }
    if p < 0 {
        np := e.AllocParty(PartyWant.Write(id: id)) else {
            g.Unlock()
            fail OutOfMemory
        }
        p = np
    }
    g.Unlock()
    return (step: WriteAttempt.Wait, new_off: off, party: p)
}

// Writes data and optional FIN on stream id (MuxEngine.Write).
QConn.MuxWrite(&self, io: Io, id: i64, gen_: u32, data: []u8, fin: bool): net.Error!void {
    _ = gen_
    nc := io.NoCancel()
    var off := 0
    var party := -1
    for off < data.Len() || (off == data.Len() && fin) {
        res := self.TryWriteStep(nc, id, data, off, fin, party) else err {
            fail err
        }
        off = res.new_off
        party = res.party
        try self.FlushSend(io)
        match res.step {
            .Done { return }
            .Continue {}
            .Wait {
                lead.Wait(self, io, party) else {
                    self.ReleaseParty(nc, party)
                    fail Canceled
                }
            }
        }
    }
}

// Raises receive credit for stream id (MuxEngine.Credit). Never waits.
QConn.MuxCredit(&self, id: i64, gen_: u32, total: i64) {
    _ = gen_
    if total <= 0 {
        return
    }
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.streams.Credit(id, Cast[u64](total))
    need_flush := e.streams.HasPending()
    g.Unlock()
    if need_flush {
        self.FlushSend(nc) else {}
    }
}

// Allocates a PartyWant.Wait slot with the monitored stream IDs.
QConn.AllocWaitParty(&self, nc: Io, ids: []i64, incoming: bool): net.Error!int {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    party := e.AllocParty(PartyWant.Wait(incoming: incoming)) else {
        g.Unlock()
        fail OutOfMemory
    }
    items := e.parties.ItemsMut()
    for i in 0..ids.Len() {
        items[party].wait_ids.Push(e.h.Alloc(), ids[i]) else {
            e.FreeParty(party)
            g.Unlock()
            fail OutOfMemory
        }
    }
    g.Unlock()
    return party
}

// Checks whether a Wait party has become ready or the connection is dead.
QConn.PollWaitParty(&self, nc: Io, party: int): net.Error!?net.MuxReady {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.PartyReady(party) {
        if r := e.parties[party].ready {
            e.FreeParty(party)
            g.Unlock()
            return some(r)
        }
        if e.IsDead() {
            e.FreeParty(party)
            err := e.ClosedError()
            g.Unlock()
            fail err
        }
    }
    g.Unlock()
    return none
}

// Waits until one of ids is readable or incoming stream arrives (MuxEngine.Wait).
QConn.MuxWait(&self, io: Io, ids: []i64, incoming: bool): net.Error!net.MuxReady {
    try self.EnsureHandshake(io)
    nc := io.NoCancel()
    party := try self.AllocWaitParty(nc, ids, incoming)
    for {
        if r := try self.PollWaitParty(nc, party) {
            return r
        }
        lead.Wait(self, io, party) else {
            self.ReleaseParty(nc, party)
            fail Canceled
        }
    }
}

// Resets sending on stream id (MuxEngine.Reset). Never waits.
QConn.MuxReset(&self, id: i64, gen_: u32, code: u64) {
    _ = gen_
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.streams.Reset(id, code)
    self.WakeSatisfied(e)
    g.Unlock()
    self.FlushSend(nc) else {}
}

// Aborts receiving on stream id (MuxEngine.Stop). Never waits.
QConn.MuxStop(&self, id: i64, gen_: u32, code: u64) {
    _ = gen_
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.streams.Stop(id, code)
    self.WakeSatisfied(e)
    g.Unlock()
    self.FlushSend(nc) else {}
}

// Releases stream handle id (MuxEngine.Release). Never waits.
QConn.MuxRelease(&self, id: i64, gen_: u32) {
    _ = gen_
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    e.streams.Release(e.h.Alloc(), id)
    need_flush := e.streams.HasPending()
    g.Unlock()
    if need_flush {
        self.FlushSend(nc) else {}
    }
}

// Returns peer stream and idle limits (MuxEngine.Limits).
QConn.MuxLimits(&self): net.MuxLimits {
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    (bidi, uni) := e.streams.OpenLimits()
    idle := time.Nanos(e.idle_effective_ns)
    g.Unlock()
    return net.MuxLimits(bidi: bidi, uni: uni, idle: idle)
}

// Processes queued packets/timers without blocking and reports usability (MuxEngine.Usable).
QConn.MuxUsable(&self, io: Io): bool {
    nc := io.NoCancel()
    // A blocked socket read owns the datagram's read mutex. Only the
    // read-token holder can poll without waiting behind that reader.
    var rx := self.eng.Lock(nc).Must()
    var party := -1
    if !rx.Ref().tok.Reading() && rx.Ref().tok.Waiting() == 0 && !rx.Ref().IsDead() {
        party = rx.Mut().AllocParty(PartyWant.Drive) else { -1 }
        if party >= 0 {
            _ = rx.Mut().tok.Enter(party)
        }
    }
    rx.Unlock()
    if party >= 0 {
        self.DrainIncomingNonBlocking(nc)
        next := self.End(nc, party)
        if next >= 0 {
            self.Party(next).NotifyOne()
        }
        self.ReleaseParty(nc, party)
    }
    self.Timers(nc)
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    var ok := !e.IsDead() && !e.drive_stop
    if ok {
        if dl := e.idle_deadline {
            if time.Now(nc).Since(dl).Nanos() >= 0 {
                ok = false
            }
        }
    }
    g.Unlock()
    return ok
}

// Returns the current peer address (MuxEngine.PeerAddr).
QConn.MuxPeerAddr(&self): ?net.Addr {
    nc := self.nc
    var g := self.eng.Lock(nc).Must()
    addr := g.Mut().path.Destination()
    g.Unlock()
    return some(addr)
}

// Closes the connection with application error code (MuxEngine.Close).
QConn.MuxClose(&self, io: Io, code: u64): net.Error!void {
    nc := io.NoCancel()
    now := time.Now(nc)
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if !e.close.IsClosed() && !e.pn_exhausted {
        e.close.Close(code, true, 0, now, e.rec.Pto())
        e.close_first_pending = true
        e.RecomputeTimer(now)
    }
    self.WakeSatisfied(e)
    g.Unlock()
    self.FlushSend(io) else err {
        fail err
    }
    // Hand server closing state to Endpoint so it rate-limits replies for 3*PTO (3.7.4).
    self.HandClosingToEndpoint(nc)
}

// Releases a previously allocated party slot under eng lock.
QConn.ReleaseParty(&self, nc: Io, party: int) {
    if party < 0 {
        return
    }
    var g := self.eng.Lock(nc).Must()
    g.Mut().FreeParty(party)
    g.Unlock()
}

// Allocates the background Drive party unless the connection has already stopped.
QConn.EnterDriveParty(&self, nc: Io): ?int {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.IsDead() || e.drive_stop {
        g.Unlock()
        return none
    }
    p := e.AllocParty(PartyWant.Drive) else {
        g.Unlock()
        return none
    }
    g.Unlock()
    return some(p)
}

// Checks whether the background Drive loop should exit.
QConn.ShouldStopDrive(&self, nc: Io): bool {
    var g := self.eng.Lock(nc).Must()
    e := &*g.Mut()
    stop := e.IsDead() || e.drive_stop
    g.Unlock()
    return stop
}

// Runs as a background driver task until the connection ends or io is cancelled.
QConn.Drive(&self, io: Io): Canceled!void {
    nc := io.NoCancel()
    party := self.EnterDriveParty(nc) else { return }
    for {
        if self.ShouldStopDrive(nc) {
            break
        }
        lead.Wait(self, io, party) else {
            self.ReleaseParty(nc, party)
            fail Canceled
        }
    }
    self.ReleaseParty(nc, party)
}
