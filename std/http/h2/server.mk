// Why: the HTTP/2 server role runs one dedicated reader task (Serve) and one
// dedicated writer task (Writer) per connection, delivering whole requests to
// std.http worker tasks without holding connection locks across handlers or
// leaking buffers on stream completion or timeout.
//
// What: Request, Conn.Serve, Conn.Writer, Conn.Respond, Conn.Write, and
// Conn.Done.
//
// How: Serve reads frames in batches under keep-alive, header-block and
// progress-floor deadlines, delivers complete requests from rd.ready_reqs,
// and triggers Writer via wr_wake; Respond and Write encode responses and
// flow-controlled DATA frames (honouring MinDataUnit, DribbleWait and
// ProgressFloor) into wr.cur; Done recycles cached FieldBlock/body buffers
// and returns body admission budget in FIFO order.

import std.core
import std.http.fields
import std.net
import std.rt
import std.time

/// A request whose head is decoded and its body whole (END_STREAM seen),
/// moved to the server integration by value.
export linear struct Request {
    /// Stream handle for Respond, Write and Done.
    export stream: StreamRef
    /// Decoded request header block (reset and returned via Done).
    export head: fields.FieldBlock
    /// Whole request body bytes (cleared and returned via Done).
    export body: HeapList[u8]
}

impl Free for Request {
    Free(self, a: &Alloc) {
        Request(stream: _, head, body) := self
        head.Free(a)
        body.Free(a)
    }
}

CheckSlotStarved(st: &State, idx: int, now: time.Instant, lim: time.Duration): (starved: bool, pleft: i64) {
    slot := &st.streams.slots.Items()[idx]
    var starved := false
    var pleft: i64 = -1
    if slot.state == StOpen && slot.progress.active {
        if slot.progress.Starved(now, lim, ProgressFloor) {
            starved = true
        } else if since := slot.progress.since {
            rem := lim.Nanos() - now.Since(since).Nanos()
            pleft = if rem < 1_000_000 { 1_000_000 } else { rem }
        }
    }
    return (starved: starved, pleft: pleft)
}

// CheckServerTimeouts checks header-block and progress-floor deadlines on active
// streams and computes the next read deadline for Serve.
Conn.ServerReadDeadline(&self, nc: Io, preface_start: time.Instant, cont_start: mut ?time.Instant): Error!?time.Duration {
    a := self.heap.Alloc()
    core_ := self.Core()
    now := time.Now(nc)
    rg := core_.rd.Lock(nc).Must()
    preface_done := rg.Ref().rp.preface_done
    cont_open := rg.Ref().rp.cont_stream != 0
    rg.Unlock()
    if cont_open {
        if cont_start.IsNone() {
            *cont_start = some(now)
        }
    } else {
        *cont_start = none
    }
    var min_ns: i64 = -1
    if !preface_done {
        left := self.lim.write_limit.Nanos() - now.Since(preface_start).Nanos()
        if left <= 0 {
            fail time.TimedOut
        }
        min_ns = left
    }
    if cs := *cont_start {
        left := self.lim.write_limit.Nanos() - now.Since(cs).Nanos()
        if left <= 0 {
            fail Violation(code: EnhanceYourCalm, stream: 0)
        }
        if min_ns < 0 || left < min_ns {
            min_ns = left
        }
    }
    var wake_writer := false
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    if f := st.fault {
        sg.Unlock()
        fail f
    }
    active := st.streams.active
    for i in 0..st.streams.slots.Len() {
        (starved, pleft) := CheckSlotStarved(&*st, i, now, self.lim.write_limit)
        if starved {
            MarkStreamError(a, st, i, Cancel, now) else e {
                sg.Unlock()
                fail e
            }
            wake_writer = true
        } else if pleft > 0 && (min_ns < 0 || pleft < min_ns) {
            min_ns = pleft
        }
    }
    sg.Unlock()
    if wake_writer {
        core_.wr_wake.NotifyOne()
    }
    if preface_done && !cont_open && active == 0 && self.lim.idle_limit.Nanos() > 0 {
        idle_ns := self.lim.idle_limit.Nanos()
        if min_ns < 0 || idle_ns < min_ns {
            min_ns = idle_ns
        }
    }
    if min_ns < 0 {
        return none
    }
    return some(core.DurationOf(min_ns))
}

/// The server reader loop: reads frames until the connection ends, admitting
/// request bodies and passing each complete Request to deliver (which must
/// not wait). Errors: net.Error, Violation, OutOfMemory.
export Conn.Serve[C: Share](&self, io: Io, c: &C, deliver: fn(c: &C, conn: &Conn, r: Request): void): Error!void {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    core_ := self.Core()
    preface_start := time.Now(nc)
    var cont_start: ?time.Instant = none
    var accs := HeapList[ServerAcc].New(a)
    var ready := HeapList[int].New(a)
    defer {
        accs.Free(a)
        ready.Free(a)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        st.reader_done = true
        sg.Unlock()
        core_.wr_wake.NotifyOne()
    }
    for {
        dl := self.ServerReadDeadline(nc, preface_start, mut cont_start) else e {
            match e {
                Violation(code, stream: _) { self.FailConn(nc, e, some(code), false) }
                time.TimedOut { self.FailConn(nc, e, some(NoError), false) }
                _ { self.FailConn(nc, e, none, true) }
            }
            fail e
        }
        more := self.ReadAndDispatch(io, nc, dl, mut accs, mut ready) else e {
            match e {
                time.TimedOut {
                    is_idle := self.CheckAndQueueIdleGoaway(nc)
                    if is_idle {
                        core_.wr_wake.NotifyOne()
                        return
                    }
                    continue
                }
                Canceled {
                    var sg := core_.st.Lock(nc).Must()
                    st := mut *sg.Mut()
                    if st.fault.IsNone() {
                        st.owed.QueueGoaway(st.last_processed_id, NoError)
                    }
                    sg.Unlock()
                    core_.wr_wake.NotifyOne()
                    fail Canceled
                }
                _ {
                    fail e
                }
            }
        }
        for i in 0..ready.Len() {
            idx := ready.Items()[i]
            acc := mut accs.ItemsMut()[idx]
            if acc.ready {
                acc.ready = false
                rhead := core.Replace(mut acc.head, none).Must()
                rbody := match core.Replace(mut acc.body, none) {
                    some(b) { b }
                    none { HeapList[u8].New(a) }
                }
                deliver(c, self, Request(stream: StreamRef(index: idx, gen_: acc.gen_), head: rhead, body: rbody))
            }
        }
        ready.Clear()
        if !more {
            return
        }
    }
}

// CheckAndQueueIdleGoaway queues GOAWAY(NO_ERROR) when idle with no open streams.
Conn.CheckAndQueueIdleGoaway(&self, nc: Io): bool {
    core_ := self.Core()
    rg := core_.rd.Lock(nc).Must()
    p_done := rg.Ref().rp.preface_done
    c_open := rg.Ref().rp.cont_stream != 0
    rg.Unlock()
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    is_idle := p_done && !c_open && st.streams.active == 0
    if is_idle {
        st.owed.QueueGoaway(st.last_processed_id, NoError)
    }
    sg.Unlock()
    return is_idle
}

/// The server writer task loop: drains owed frames and queued DATA/HEADERS
/// until the reader finishes and all buffers are empty. Errors: net.Error,
/// time.TimedOut, OutOfMemory.
export Conn.Writer(&self, io: Io): Error!void {
    nc := io.NoCancel()
    core_ := self.Core()
    for {
        for {
            try core_.wl.Acquire(io, 1)
            wrote := self.WriteTurnLocked(io, false) else e {
                core_.wl.Release(1)
                fail e
            }
            core_.wl.Release(1)
            if !wrote {
                break
            }
        }
        var sg := core_.st.Lock(nc).Must()
        st := &*sg.Ref()
        var wg := core_.wr.Lock(nc).Must()
        empty := wg.Ref().cur.Len() == 0 && st.owed.Empty()
        wg.Unlock()
        done := empty && (st.reader_done || st.fault.IsSome())
        sg.Unlock()
        if done {
            return
        }
        core_.wr_wake.Wait(io) else {
            // Cancelled: flush any owed GOAWAY(NO_ERROR) best-effort before returning.
            if core_.wl.TryAcquire(1) {
                _ = self.WriteTurnLocked(nc, true) else {}
                core_.wl.Release(1)
            }
            fail Canceled
        }
    }
}

/// Writes a response head (names lower-cased, connection-specific fields
/// dropped) and, when end is true, END_STREAM. Waits if the write buffer is
/// full. Errors: BadField, TooLarge, net.StreamReset, net.Error, Canceled.
export Conn.Respond[F: fields.Field](&self, io: Io, s: StreamRef, status: int, list: []F, end: bool): Error!void {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    core_ := self.Core()
    for {
        wg0 := core_.wr.Lock(nc).Must()
        full := wg0.Ref().cur.Len() >= WriteFlushCap
        wg0.Unlock()
        if !full {
            break
        }
        sg0 := core_.st.Lock(nc).Must()
        f := sg0.Ref().fault
        sg0.Unlock()
        if err := f {
            fail err
        }
        try core_.wr_space_wake.Wait(io)
    }
    var wg := core_.wr.Lock(nc).Must()
    wr := mut *wg.Mut()
    sg_lim := core_.st.Lock(nc).Must()
    st_lim := &*sg_lim.Ref()
    if f := st_lim.fault {
        sg_lim.Unlock()
        wg.Unlock()
        fail f
    }
    max_list := st_lim.peer_settings.max_header_list
    mf := st_lim.peer_settings.max_frame
    sg_lim.Unlock()
    bound := ValidateResponseFields(a, wr, status, list, max_list) else e {
        wg.Unlock()
        fail e
    }
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    if f := st.fault {
        sg.Unlock()
        wg.Unlock()
        fail f
    }
    if s.index < 0 || s.index >= st.streams.slots.Len() {
        sg.Unlock()
        wg.Unlock()
        fail Closed
    }
    slot := mut st.streams.slots.ItemsMut()[s.index]
    if slot.gen_ != s.gen_ || slot.state == StIdle {
        sg.Unlock()
        wg.Unlock()
        fail Closed
    }
    if code := slot.reset {
        sg.Unlock()
        wg.Unlock()
        fail net.StreamReset(code: u64(code))
    }
    if slot.state == StClosed || slot.state == StHalfLocal {
        sg.Unlock()
        wg.Unlock()
        fail Closed
    }
    sid := slot.id
    if end {
        slot.state = if slot.state == StHalfRemote { StClosed } else { StHalfLocal }
    }
    sg.Unlock()
    blen := EncodeResponseBlock(a, wr, status, list, bound) else e {
        wg.Unlock()
        fail e
    }
    AppendHeadersFrames(a, mut wr.cur, sid, wr.hbuf.Items()[0..blen], mf, end) else e {
        wg.Unlock()
        fail e
    }
    wg.Unlock()
    core_.wr_wake.NotifyOne()
}

/// Writes response body bytes on s; waits for send window credit and write
/// buffer space under MinDataUnit, DribbleWait and ProgressFloor. Sends
/// END_STREAM when end is true. Errors: net.StreamReset, net.Error,
/// time.TimedOut, Canceled.
export Conn.Write(&self, io: Io, s: StreamRef, data: []u8, end: bool): Error!void {
    try self.SendDataStream(io, s, data, end)
}

// SendDataStream writes data on s in flow-controlled DATA frames (shared by
// server Conn.Write and client Conn.Send).
Conn.SendDataStream(&self, io: Io, s: StreamRef, data: []u8, end: bool): Error!void {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    core_ := self.Core()
    var off := 0
    for off < data.Len() || (end && off == data.Len()) {
        // Wait for write-buffer space when cur is full.
        for {
            wg0 := core_.wr.Lock(nc).Must()
            full := wg0.Ref().cur.Len() >= WriteFlushCap
            wg0.Unlock()
            if !full {
                break
            }
            if self.is_client {
                try self.WaitWriteTurnOrSpace(io, s.index)
            } else {
                sg0 := core_.st.Lock(nc).Must()
                f := sg0.Ref().fault
                sg0.Unlock()
                if err := f {
                    fail err
                }
                try core_.wr_space_wake.Wait(io)
            }
        }
        rem := data.Len() - off
        var chunk := 0
        var sid: u32 = 0
        var send_empty_end := false
        var wait_ns: i64 = -1
        var wg := core_.wr.Lock(nc).Must()
        wr := mut *wg.Mut()
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        if f := st.fault {
            sg.Unlock()
            wg.Unlock()
            fail f
        }
        if s.index < 0 || s.index >= st.streams.slots.Len() {
            sg.Unlock()
            wg.Unlock()
            fail Closed
        }
        slot := mut st.streams.slots.ItemsMut()[s.index]
        if slot.gen_ != s.gen_ || slot.state == StIdle {
            sg.Unlock()
            wg.Unlock()
            fail Closed
        }
        if self.is_client && slot.send_stopped {
            sg.Unlock()
            wg.Unlock()
            return
        }
        if slot.unprocessed {
            code := st.goaway_code
            sg.Unlock()
            wg.Unlock()
            fail net.MuxClosed(code: u64(code), app: true, peer: true)
        }
        if code := slot.reset {
            sg.Unlock()
            wg.Unlock()
            fail net.StreamReset(code: u64(code))
        }
        if slot.state == StClosed || slot.state == StHalfLocal {
            sg.Unlock()
            wg.Unlock()
            fail Closed
        }
        sid = slot.id
        now := time.Now(nc)
        if rem == 0 {
            send_empty_end = true
            slot.progress.Idle()
            slot.dribble_since = none
            slot.state = if slot.state == StHalfRemote { StClosed } else { StHalfLocal }
            if self.is_client && slot.state == StClosed && slot.refs == 0 && !slot.in_owed {
                st.streams.ReleaseSlot(s.index, a)
            }
        } else {
            sw := slot.send_win.Avail()
            cw := st.conn_send_win.Avail()
            min_u := if rem < MinDataUnit { rem } else { MinDataUnit }
            avail := if sw < cw { sw } else { cw }
            var allow := false
            if avail >= min_u {
                allow = true
            } else if avail > 0 {
                if ds := slot.dribble_since {
                    if now.Since(ds).Nanos() >= DribbleWait.Nanos() {
                        allow = true
                    }
                } else {
                    slot.dribble_since = some(now)
                }
            }
            if allow {
                mf := st.peer_settings.max_frame
                var k := avail
                if rem < k {
                    k = rem
                }
                if mf < k {
                    k = mf
                }
                slot.send_win.Take(k).Must()
                st.conn_send_win.Take(k).Must()
                slot.send_gate.SentData()
                st.conn_send_gate.SentData()
                slot.progress.Pending(now)
                slot.progress.Moved(k)
                slot.dribble_since = none
                slot.blocked_on = BlockNone
                chunk = k
                is_end := end && (off + k == data.Len())
                if is_end {
                    slot.progress.Idle()
                    slot.state = if slot.state == StHalfRemote { StClosed } else { StHalfLocal }
                }
                // Append DATA frame inside wr+st lock.
                d_off := wr.cur.Len()
                EnsureLen(a, mut wr.cur, d_off + 9 + k) else oe {
                    sg.Unlock()
                    wg.Unlock()
                    fail oe
                }
                _ = WriteData(mut wr.cur.ItemsMut()[d_off..d_off + 9 + k], sid, data[off..off + k], is_end).Must()
                if is_end && self.is_client && slot.state == StClosed && slot.refs == 0 && !slot.in_owed {
                    st.streams.ReleaseSlot(s.index, a)
                }
            } else {
                slot.progress.Pending(now)
                if slot.progress.Starved(now, self.lim.write_limit, ProgressFloor) {
                    slot.owed_reset = some(Cancel)
                    slot.state = StClosed
                    slot.progress.Idle()
                    st.reset_set.Add(sid)
                    if !self.is_client {
                        _ = st.reset_bucket.Take(now)
                    }
                    EnqueueOwedStream(st, s.index)
                    sg.Unlock()
                    wg.Unlock()
                    core_.wr_wake.NotifyOne()
                    if self.is_client {
                        self.FlushWrites(io) else {}
                    }
                    fail time.TimedOut
                }
                slot.blocked_on = if sw <= 0 { BlockStreamWin } else { BlockConnWin }
                // Compute wake time for DribbleWait / progress floor.
                if since := slot.progress.since {
                    p_rem := self.lim.write_limit.Nanos() - now.Since(since).Nanos()
                    wait_ns = if p_rem < 1_000_000 { 1_000_000 } else { p_rem }
                }
                if ds := slot.dribble_since {
                    d_rem := DribbleWait.Nanos() - now.Since(ds).Nanos()
                    d_wait := if d_rem < 1_000_000 { 1_000_000 } else { d_rem }
                    if wait_ns < 0 || d_wait < wait_ns {
                        wait_ns = d_wait
                    }
                }
            }
        }
        if send_empty_end {
            d_off := wr.cur.Len()
            EnsureLen(a, mut wr.cur, d_off + 9) else oe {
                sg.Unlock()
                wg.Unlock()
                fail oe
            }
            empty: [0]u8 = []
            _ = WriteData(mut wr.cur.ItemsMut()[d_off..d_off + 9], sid, empty[..], true).Must()
        }
        sg.Unlock()
        wg.Unlock()
        if send_empty_end {
            core_.wr_wake.NotifyOne()
            if self.is_client {
                try self.FlushWrites(io)
            }
            return
        }
        if chunk > 0 {
            off += chunk
            core_.wr_wake.NotifyOne()
            if self.is_client && (off == data.Len() && end) {
                try self.FlushWrites(io)
            }
            if off == data.Len() {
                return
            }
            continue
        }
        // Blocked on flow-control window: flush queued frames first and wait.
        if self.is_client {
            try self.FlushWrites(io)
            try self.WaitSendWindow(io, s.index, wait_ns)
        } else {
            core_.wr_wake.NotifyOne()
            if wait_ns > 0 {
                scope ws := io.Scope() {
                    ws.CancelAfter(core.DurationOf(wait_ns))
                    core_.wr_space_wake.Wait(io) else {
                        if !ws.Expired() {
                            fail Canceled
                        }
                    }
                }
            } else {
                try core_.wr_space_wake.Wait(io)
            }
        }
    }
}

/// Returns the request's buffers to the stream's slot and ends the handler's
/// hold on the stream (the stream stops counting against Streams, and its body
DoneSlotUpdate(
    st: mut State,
    idx: int,
    gen_: u32,
): (matched: bool, sid: u32, admit_reserve: int, admitted: bool, was_open: bool, release: bool) {
    if idx < 0 || idx >= st.streams.slots.Len() {
        return (matched: false, sid: 0, admit_reserve: 0, admitted: false, was_open: false, release: false)
    }
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.gen_ != gen_ || slot.state == StIdle {
        return (matched: false, sid: 0, admit_reserve: 0, admitted: false, was_open: false, release: false)
    }
    sid := slot.id
    reserve := slot.admit_reserve
    admitted := slot.admitted
    slot.admit_reserve = 0
    slot.admitted = false
    if slot.refs > 0 {
        slot.refs -= 1
    }
    was_open := slot.state != StClosed
    if was_open {
        slot.owed_reset = some(Cancel)
        slot.state = StClosed
        slot.progress.Idle()
    }
    release := slot.state == StClosed && slot.refs == 0 && !slot.in_owed
    return (matched: true, sid: sid, admit_reserve: reserve, admitted: admitted, was_open: was_open, release: release)
}

/// admission budget returns to the FIFO). O(admitted streams); allocates nothing.
export Conn.Done(&self, s: StreamRef, var head: fields.FieldBlock, var body: HeapList[u8]) {
    nc := core.IoMasked(rt.NativeIo())
    a := self.heap.Alloc()
    core_ := self.Core()
    head.Free(a)
    body.Free(a)
    var wake_writer := false
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    (matched, sid, admit_reserve: reserve, admitted, was_open, release) := DoneSlotUpdate(st, s.index, s.gen_)
    if matched {
        if reserve > 0 {
            if admitted {
                var admitted_ids: [16]u32 = [0; 16]
                n := st.admission.Return(reserve, mut admitted_ids[..])
                AdmitStreams(st, admitted_ids[0..n])
                if n > 0 {
                    wake_writer = true
                }
            } else {
                st.admission.Cancel(sid)
            }
        }
        if was_open {
            st.reset_set.Add(sid)
            EnqueueOwedStream(st, s.index)
            wake_writer = true
        }
        if release {
            st.streams.ReleaseSlot(s.index, a)
        }
    }
    sg.Unlock()
    if wake_writer {
        core_.wr_wake.NotifyOne()
    }
}

Conn.WaitWriteTurnOrSpace(&self, io: Io, idx: int): Error!void {
    try self.Core().wr_space_wake.Wait(io)
}

Conn.WaitSendWindow(&self, io: Io, idx: int, wait_ns: i64): Error!void {
    try self.Core().wr_space_wake.Wait(io)
}
