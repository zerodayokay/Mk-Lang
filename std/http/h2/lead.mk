// Why: multiplexed client connections without dedicated reader tasks share one
// read token across concurrent callers and Conn.Drive; the token protocol
// guarantees bounded waits, clean leader handoff on cancellation, and timer
// enforcement for idle shutdown, DribbleWait, and ProgressFloor.
//
// What: lead.Driven impl for Conn, Conn.Drive, WaitWriteTurnOrSpace, and
// WaitSendWindow.
//
// How: party 0 serves Conn.Drive, odd parties (1 + 2*idx) wait on stream reads
// (Head/Read), and even parties (2 + 2*idx) wait on send window credit; every
// Begin/End runs under the state lock with cancellation masked while Lead and
// Notify waits run under the caller's Io scope.

import std.core
import std.sync
import std.sync.lead
import std.time

const PartyDrive: int = 0

ReaderParty(idx: int): int {
    return 1 + 2 * idx
}

WriterParty(idx: int): int {
    return 2 + 2 * idx
}

ClientReaderReady(st: &State, idx: int): bool {
    if st.fault.IsSome() {
        return true
    }
    if idx < 0 || idx >= st.streams.slots.Len() {
        return true
    }
    slot := &st.streams.slots.Items()[idx]
    if slot.state == StIdle || slot.unprocessed || slot.reset.IsSome() {
        return true
    }
    if slot.cli_head.IsSome() || slot.cli_head_too_large || slot.cli_head_malformed {
        return true
    }
    if slot.cli_head_ready && (slot.cli_pages.Read() > 0 || slot.cli_eof || slot.cli_body_malformed) {
        return true
    }
    return false
}

ClientWriterReady(st: &State, idx: int, now: time.Instant, write_lim: time.Duration): bool {
    if st.fault.IsSome() {
        return true
    }
    if idx < 0 || idx >= st.streams.slots.Len() {
        return true
    }
    slot := &st.streams.slots.Items()[idx]
    if slot.state == StIdle || slot.state == StClosed || slot.state == StHalfLocal {
        return true
    }
    if slot.send_stopped || slot.unprocessed || slot.reset.IsSome() {
        return true
    }
    if slot.progress.active && slot.progress.Starved(now, write_lim, ProgressFloor) {
        return true
    }
    sw := slot.send_win.Avail()
    cw := st.conn_send_win.Avail()
    avail := if sw < cw { sw } else { cw }
    if avail >= MinDataUnit {
        return true
    }
    if avail > 0 {
        if ds := slot.dribble_since {
            if now.Since(ds).Nanos() >= DribbleWait.Nanos() {
                return true
            }
        }
    }
    return false
}

MinClientTimeout(st: &State, now: time.Instant, write_lim: time.Duration): ?time.Duration {
    var best: ?i64 = none
    if st.streams.active == 0 && st.drive_idle_limit.Nanos() > 0 {
        if since := st.drive_idle_since {
            rem := st.drive_idle_limit.Nanos() - now.Since(since).Nanos()
            best = some(if rem < 0 { 0 } else { rem })
        }
    }
    for i in 0..st.streams.slots.Len() {
        slot := &st.streams.slots.Items()[i]
        if slot.state == StOpen || slot.state == StHalfRemote {
            if slot.progress.active {
                if since := slot.progress.since {
                    raw := write_lim.Nanos() - now.Since(since).Nanos()
                    rem := if raw < 0 { 0 } else { raw }
                    if b := best {
                        if rem < b {
                            best = some(rem)
                        }
                    } else {
                        best = some(rem)
                    }
                }
            }
            if ds := slot.dribble_since {
                raw := DribbleWait.Nanos() - now.Since(ds).Nanos()
                rem := if raw < 0 { 0 } else { raw }
                if b := best {
                    if rem < b {
                        best = some(rem)
                    }
                } else {
                    best = some(rem)
                }
            }
        }
    }
    if best.IsNone() && st.streams.active > 0 && write_lim.Nanos() > 0 {
        best = some(write_lim.Nanos())
    }
    if b := best {
        return some(core.DurationOf(b))
    }
    return none
}

CheckClientTimers(st: mut State, now: time.Instant, write_lim: time.Duration): bool {
    var wake_writer := false
    for i in 0..st.streams.slots.Len() {
        var starved := false
        slot := &st.streams.slots.Items()[i]
        if (slot.state == StOpen || slot.state == StHalfRemote) && slot.progress.active {
            starved = slot.progress.Starved(now, write_lim, ProgressFloor)
        }
        if starved {
            s := mut st.streams.slots.ItemsMut()[i]
            s.owed_reset = some(Cancel)
            s.reset = some(Cancel)
            s.progress.Idle()
            sid := s.id
            st.reset_set.Add(sid)
            st.streams.CloseSlot(i)
            EnqueueOwedStream(st, i)
            wake_writer = true
        }
    }
    if st.streams.active == 0 {
        if st.drive_idle_since.IsNone() {
            st.drive_idle_since = some(now)
        }
        if st.drive_idle_limit.Nanos() > 0 {
            if since := st.drive_idle_since {
                if now.Since(since).Nanos() >= st.drive_idle_limit.Nanos() {
                    st.drive_stop = true
                }
            }
        }
    } else {
        st.drive_idle_since = none
    }
    return wake_writer
}

impl lead.Driven for Conn {
    Begin(&self, nc: Io, p: int): lead.Step {
        core_ := self.Core()
        now := time.Now(nc)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        if p == PartyDrive {
            if st.fault.IsSome() || st.drive_stop || (st.goaway_recvd && st.streams.active == 0) {
                sg.Unlock()
                return lead.Step.Done
            }
            if st.streams.active == 0 && st.drive_idle_limit.Nanos() > 0 {
                if since := st.drive_idle_since {
                    if now.Since(since).Nanos() >= st.drive_idle_limit.Nanos() {
                        st.drive_stop = true
                        sg.Unlock()
                        return lead.Step.Done
                    }
                }
            }
        } else if (p & 1) == 1 {
            idx := (p - 1) / 2
            if ClientReaderReady(&*st, idx) {
                sg.Unlock()
                return lead.Step.Done
            }
        } else {
            idx := (p - 2) / 2
            if ClientWriterReady(&*st, idx, now, self.lim.write_limit) {
                sg.Unlock()
                return lead.Step.Done
            }
        }
        got := st.token.Enter(p)
        sg.Unlock()
        if got {
            return lead.Step.Lead(deadline: none)
        }
        return lead.Step.Wait(deadline: none)
    }

    Lead(&self, io: Io, nc: Io, deadline: ?time.Instant): Canceled!void {
        _ = deadline
        a := self.heap.Alloc()
        self.FlushWrites(io) else e {
            match e {
                Canceled { fail Canceled }
                _ {
                    self.Timers(nc)
                    return
                }
            }
        }
        core_ := self.Core()
        var sg := core_.st.Lock(nc).Must()
        now := time.Now(nc)
        if sg.Ref().fault.IsSome() || sg.Ref().drive_stop {
            sg.Unlock()
            self.WakeAll()
            return
        }
        var dur := MinClientTimeout(sg.Ref(), now, self.lim.write_limit)
        var run_due := false
        if d := dur {
            if d.Nanos() <= 0 {
                run_due = true
            }
        }
        if run_due {
            wake_writer := CheckClientTimers(sg.Mut(), now, self.lim.write_limit)
            stop_now := sg.Ref().fault.IsSome() || sg.Ref().drive_stop
            dur = MinClientTimeout(sg.Ref(), now, self.lim.write_limit)
            sg.Unlock()
            if wake_writer {
                self.FlushWrites(nc) else {}
            }
            self.WakeAll()
            if stop_now {
                return
            }
        } else {
            sg.Unlock()
        }
        if d := dur {
            if d.Nanos() < 1_000_000 {
                dur = some(core.DurationOf(1_000_000))
            }
        }
        var accs := HeapList[ServerAcc].New(a)
        var ready := HeapList[int].New(a)
        more := self.ReadAndDispatch(io, nc, dur, mut accs, mut ready) else e {
            accs.Free(a)
            ready.Free(a)
            match e {
                Canceled { fail Canceled }
                _ {
                    self.Timers(nc)
                    return
                }
            }
        }
        accs.Free(a)
        ready.Free(a)
        if !more {
            self.FailConn(nc, Closed, none, false)
        }
        self.Timers(nc)
    }

    Timers(&self, nc: Io) {
        core_ := self.Core()
        now := time.Now(nc)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        wake_writer := CheckClientTimers(st, now, self.lim.write_limit)
        sg.Unlock()
        if wake_writer {
            self.FlushWrites(nc) else {}
        }
        self.WakeAll()
    }

    End(&self, nc: Io, p: int): int {
        core_ := self.Core()
        var sg := core_.st.Lock(nc).Must()
        wake := sg.Mut().token.Leave(p)
        sg.Unlock()
        return wake
    }

    Party(&self, p: int): &sync.Notify {
        return &self.Core().parties.Items()[p]
    }
}

Conn.WaitWriteTurnOrSpace(&self, io: Io, idx: int): Error!void {
    _ = idx
    nc := io.NoCancel()
    core_ := self.Core()
    try core_.wl.Acquire(io, 1)
    for {
        wg := core_.wr.Lock(nc).Must()
        need := wg.Ref().cur.Len() >= WriteFlushCap
        wg.Unlock()
        if !need {
            break
        }
        wrote := self.WriteTurnLocked(io, false) else e {
            core_.wl.Release(1)
            fail e
        }
        if !wrote {
            break
        }
    }
    // Drain remaining cur/owed before releasing wl so no appender strands bytes.
    for {
        var empty := false
        wg := core_.wr.Lock(nc).Must()
        sg := core_.st.Lock(nc).Must()
        if wg.Ref().cur.Len() == 0 && sg.Ref().owed.Empty() {
            core_.wl.Release(1)
            empty = true
        }
        sg.Unlock()
        wg.Unlock()
        if empty {
            return
        }
        _ = self.WriteTurnLocked(io, false) else e {
            core_.wl.Release(1)
            fail e
        }
    }
}

Conn.WaitSendWindow(&self, io: Io, idx: int, wait_ns: i64): Error!void {
    _ = wait_ns
    try lead.Wait(self, io, WriterParty(idx))
}

/// Runs the connection's background driver party until the connection fails,
/// finishes draining after GOAWAY with zero active streams, or sits idle past
/// idle (unless idle <= 0), or io is cancelled. Errors: Canceled.
export Conn.Drive(&self, io: Io, idle: time.Duration): Canceled!void {
    nc := io.NoCancel()
    core_ := self.Core()
    now := time.Now(nc)
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    st.drive_idle_limit = idle
    if st.streams.active == 0 && st.drive_idle_since.IsNone() {
        st.drive_idle_since = some(now)
    }
    sg.Unlock()
    self.FlushWrites(io) else {}
    try lead.Wait(self, io, PartyDrive)
}
