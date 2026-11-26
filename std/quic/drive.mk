// Why: QConn implements the read-token protocol (lead.Driven) so whichever
// task is waiting drives socket/inbox reads, dispatches frames, runs timers
// (including the T5 PTO/2 deadline cap), and flushes outgoing packets without
// a background reader task.
//
// What: impl lead.Driven for QConn, RunTimers, DrainIncomingNonBlocking, and
// HandClosingToEndpoint.
//
// How: Begin checks PartyReady and enters Token; Lead receives or waits on the
// inbox with deadline capped at PTO/2 while ack-eliciting packets are in
// flight, drains up to 64 queued datagrams, runs due timers, and calls
// FlushSend; End leaves Token and wakes the next queued waiter.

import std.net
import std.sync
import std.sync.lead
import std.time

// Runs all due connection timers under Engine lock at now.
RunTimersLocked(qc: &QConn, e: mut Engine, now: time.Instant) {
    e.keys.ExpirePrev(now)
    if dl := e.idle_deadline {
        if now.Since(dl).Nanos() >= 0 {
            // QT-298: idle expiry closes silently and sends nothing.
            e.close.OnPeerClose(0, false, now, e.rec.Pto())
            if e.hs_net_err.IsNone() {
                e.hs_net_err = some(Closed)
            }
            e.pn_exhausted = true
            e.RecomputeTimer(now)
            qc.WakeSatisfied(e)
            return
        }
    }
    if e.close.IsClosed() && e.close.Expired(now) {
        e.abandoned = true
        e.drive_stop = true
        e.next_timer = none
        qc.WakeSatisfied(e)
        return
    }
    _ = e.path.OnTimeout(now)
    if rt := e.rec.Timer(now) {
        if rt.Nanos() <= 0 {
            ApplyRecoveryEvent(e, now, false) else {
                e.FailTransport(ErrInternal, 0, now)
            }
        }
    }
    e.RecomputeTimer(now)
    qc.WakeSatisfied(e)
}

ApplyRecoveryEvent(e: mut Engine, now: time.Instant, unblocked: bool): OutOfMemory!void {
    a := e.h.Alloc()
    space := e.rec.GetLossTimeAndSpace().space
    n := e.rec.spaces[SpaceIndex(space)].frm_len
    try GrowFrameScratch(a, mut e.lost_frames, n)
    var lost := Replace(mut e.lost_frames, HeapList[SentFrame].New(a))
    defer Replace(mut e.lost_frames, lost).Free(a)
    res := if unblocked { e.rec.OnDatagramReceived(now, lost.ItemsMut()) } else { e.rec.OnTimeout(now, lost.ItemsMut()) }
    if res.lost > 0 {
        HandleLostFrames(e, lost.Items()[0..res.lost])
    }
    if res.probes > 0 {
        e.probe_owed_space = some(e.rec.PtoSpace())
        e.probe_owed_count = res.probes
        if !unblocked {
            e.path.OnPto()
        }
    }
}

// Processes any datagrams already queued in the socket or endpoint inbox without waiting.
QConn.DrainIncomingNonBlocking(&self, nc: Io) {
    var rx_buf: [PoolSlotBytes]u8 = [0; PoolSlotBytes]
    var processed := 0
    if sock := &self.sock {
        for processed < 64 {
            got := sock.TryRecv(nc, mut rx_buf[..]) else err_rx {
                self.HandleRecvError(nc, &err_rx)
                break
            }
            item := got else { break }
            processed += 1
            now := time.Now(nc)
            var g := self.eng.Lock(nc).Must()
            e := mut *g.Mut()
            ProcessDatagram(e, nc, mut rx_buf[0..item.n], item.peer, now)
            self.WakeSatisfied(e)
            g.Unlock()
        }
    } else if ep := &self.ep {
        for processed < 64 {
            slot := ep.Ref().PopInbox(self.ep_conn_idx, self.ep_conn_gen) else { break }
            info := ep.Ref().ReadAndFreeSlot(slot, mut rx_buf[..])
            processed += 1
            now := time.Now(nc)
            var g := self.eng.Lock(nc).Must()
            e := mut *g.Mut()
            ProcessDatagram(e, nc, mut rx_buf[0..info.len], info.from, now)
            self.WakeSatisfied(e)
            g.Unlock()
        }
    }
}

// Handles a socket receive error on the client UDP socket (ICMP Refused fails
// dial only during handshake, ignored after, Section 3.6.7).
QConn.HandleRecvError(&self, nc: Io, err_rx: &net.Error) {
    is_icmp := match *err_rx {
        net.Refused | net.Reset | net.Unreachable { true }
        _ { false }
    }
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if is_icmp && e.hs_state >= HsComplete {
        g.Unlock()
        return
    }
    if e.hs_net_err.IsNone() {
        e.hs_net_err = some(*err_rx)
    }
    e.pn_exhausted = true
    self.WakeSatisfied(e)
    g.Unlock()
}

QConn.BuildEndpointCloseState(&self, nc: Io, pkt: mut []u8): ?(len: int, until: time.Instant, val_addr: net.Addr, has_val: bool) {
    var g := self.eng.Lock(nc).Must()
    e := mut *g.Mut()
    if e.close.phase != ClosePhase.Closing {
        g.Unlock()
        return none
    }
    e.close_first_pending = true
    var pkt_len := 0
    if built := BuildCloseDatagram(e, pkt, time.Now(nc)) {
        pkt_len = built.len
    }
    until := if u := e.close.until { u } else { time.Now(nc) }
    val_addr := e.path.ValidatedAddr()
    has_val := e.addr_validated
    g.Unlock()
    if pkt_len <= 0 {
        return none
    }
    return some((len: pkt_len, until: until, val_addr: val_addr, has_val: has_val))
}

// Hands a server connection's closing packet and state to Endpoint so
// Endpoint.Run can rate-limit close replies for 3*PTO after QConn frees (3.7.4).
QConn.HandClosingToEndpoint(&self, nc: Io) {
    ep := match &self.ep {
        some(b) { b }
        none { return }
    }
    var pkt: [128]u8 = [0; 128]
    st := self.BuildEndpointCloseState(nc, mut pkt[..]) else { return }
    ep.Ref().SetClosingState(self.ep_conn_idx, self.ep_conn_gen, pkt[0..st.len], st.until, st.val_addr, st.has_val)
}

QConn.RunTimersWithLock(&self, nc: Io, now: time.Instant) {
    var g := self.eng.Lock(nc).Must()
    RunTimersLocked(self, mut *g.Mut(), now)
    g.Unlock()
}

impl lead.Driven for QConn {
    Begin(&self, nc: Io, p: int): lead.Step {
        var g := self.eng.Lock(nc).Must()
        e := mut *g.Mut()
        if e.PartyReady(p) {
            g.Unlock()
            return lead.Step.Done
        }
        if e.tok.Enter(p) {
            var dl := e.next_timer
            // Rule T5 (Section 3.6.2): cap leader's deadline at PTO/2 while
            // ack-eliciting packets are in flight.
            if e.rec.AnyAckElicitingInFlight() {
                var half := e.rec.Pto().Nanos() / 2
                if half < 1_000_000 {
                    half = 1_000_000
                }
                cap := InstantAdd(time.Now(nc), time.Nanos(half))
                dl = EarliestInstant(dl, some(cap))
            }
            g.Unlock()
            return lead.Step.Lead(deadline: dl)
        }
        dl := e.next_timer
        g.Unlock()
        return lead.Step.Wait(deadline: dl)
    }

    Lead(&self, io: Io, nc: Io, deadline: ?time.Instant): Canceled!void {
        var rx_buf: [PoolSlotBytes]u8 = [0; PoolSlotBytes]
        var got_one := false
        var got_len := 0
        var got_from := QuicZeroAddr

        if sock := &self.sock {
            if first := sock.TryRecv(nc, mut rx_buf[..]) else err_try {
                self.HandleRecvError(nc, &err_try)
                none
            } {
                got_one = true
                got_len = first.n
                got_from = first.peer
            } else {
                match deadline {
                    some(dl) {
                        rem := dl.Since(time.Now(nc))
                        if rem.Nanos() > 0 {
                            var cancelled := false
                            scope s := io.Scope() {
                                s.CancelAfter(rem)
                                r := sock.Recv(io, mut rx_buf[..]) else err_rx {
                                    if !s.Expired() {
                                        match err_rx {
                                            Canceled { cancelled = true }
                                            _ { self.HandleRecvError(nc, &err_rx) }
                                        }
                                    }
                                    (n: 0, peer: QuicZeroAddr)
                                }
                                if r.n > 0 {
                                    got_one = true
                                    got_len = r.n
                                    got_from = r.peer
                                }
                            }
                            if cancelled {
                                fail Canceled
                            }
                        }
                    }
                    none {
                        r := sock.Recv(io, mut rx_buf[..]) else err_rx {
                            match err_rx {
                                Canceled { fail Canceled }
                                _ { self.HandleRecvError(nc, &err_rx) }
                            }
                            (n: 0, peer: QuicZeroAddr)
                        }
                        if r.n > 0 {
                            got_one = true
                            got_len = r.n
                            got_from = r.peer
                        }
                    }
                }
            }
        } else if ep := &self.ep {
            if slot := ep.Ref().PopInbox(self.ep_conn_idx, self.ep_conn_gen) {
                info := ep.Ref().ReadAndFreeSlot(slot, mut rx_buf[..])
                got_one = true
                got_len = info.len
                got_from = info.from
            } else {
                notify := ep.Ref().InboxNotify(self.ep_conn_idx)
                match deadline {
                    some(dl) {
                        rem := dl.Since(time.Now(nc))
                        if rem.Nanos() > 0 {
                            var cancelled := false
                            scope s := io.Scope() {
                                s.CancelAfter(rem)
                                notify.Wait(io) else {
                                    if !s.Expired() {
                                        cancelled = true
                                    }
                                }
                            }
                            if cancelled {
                                fail Canceled
                            }
                        }
                    }
                    none {
                        notify.Wait(io) else {
                            fail Canceled
                        }
                    }
                }
                if slot := ep.Ref().PopInbox(self.ep_conn_idx, self.ep_conn_gen) {
                    info := ep.Ref().ReadAndFreeSlot(slot, mut rx_buf[..])
                    got_one = true
                    got_len = info.len
                    got_from = info.from
                }
            }
        }

        if got_one {
            now := time.Now(nc)
            var g := self.eng.Lock(nc).Must()
            e := mut *g.Mut()
            ProcessDatagram(e, nc, mut rx_buf[0..got_len], got_from, now)
            self.WakeSatisfied(e)
            g.Unlock()
            self.DrainIncomingNonBlocking(nc)
        }
        self.Timers(nc)
        if !got_one {
            var stuck_dl := false
            var eg := self.eng.Lock(nc).Must()
            re := eg.Mut()
            if nt := re.next_timer {
                if nt.Since(time.Now(nc)).Nanos() <= 0 {
                    // Only sleep 1ms if no sendable stream work can progress without waiting on clock.
                    can_prog := re.streams.HasPending() && re.rec.CanSend(MaxSendDatagram, time.Now(nc))
                    if !can_prog {
                        stuck_dl = true
                    }
                }
            }
            eg.Unlock()
            if stuck_dl {
                _ = io.Sleep(time.Millis(1)) else {
                    fail Canceled
                }
            }
        }
    }

    Timers(&self, nc: Io) {
        now := time.Now(nc)
        self.RunTimersWithLock(nc, now)
        self.FlushSend(nc) else {}
    }

    End(&self, nc: Io, p: int): int {
        var g := self.eng.Lock(nc).Must()
        e := mut *g.Mut()
        wake := e.tok.Leave(p)
        var extra_all := false
        if wake >= 0 {
            if e.bucket_counts[wake % NotifyBuckets] > 1 {
                extra_all = true
            }
        }
        g.Unlock()
        if extra_all {
            self.parties[wake % NotifyBuckets].NotifyAll()
        }
        return wake
    }

    Party(&self, p: int): &sync.Notify {
        return &self.parties[p % NotifyBuckets]
    }
}
