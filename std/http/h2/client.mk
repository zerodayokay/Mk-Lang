// Why: HTTP/2 client exchanges share one multiplexed connection across concurrent
// tasks using zero-allocation StreamRef handles, atomic stream-id/HPACK open,
// flow-controlled request body writes, 1xx response skipping, and non-blocking
// stream cleanup via Release.
//
// What: Conn.TryOpen, Conn.Send, Conn.Head, Conn.Read, and Conn.Release.
//
// How: TryOpen encodes request HEADERS inside wr+st and flushes inline; Send
// delegates to SendDataStream under lead.Wait window waiting; Head and Read
// block on the stream's reader party via lead.Wait; Release never blocks.

import std.core
import std.http.fields
import std.net
import std.rt
import std.sync.lead
import std.time

/// Attempts to open a client stream and queue its request HEADERS (id and HPACK
/// block atomically under wr+st). Returns none without waiting when the peer
/// stream limit, stream-id space, or received GOAWAY prevents opening. Errors:
/// BadField, TooLarge, net.Error, Canceled, OutOfMemory.
export Conn.TryOpen[F: fields.Field](&self, io: Io, pseudo: &fields.Pseudo, list: []F, end_stream: bool): Error!?StreamRef {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    core_ := self.Core()
    var wg := core_.wr.Lock(nc).Must()
    wr := mut *wg.Mut()
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    if f := st.fault {
        sg.Unlock()
        wg.Unlock()
        fail f
    }
    cap := EffectiveMaxStreams(&st.peer_settings, self.lim.streams)
    if st.goaway_recvd || st.next_stream_id > MaxStreamId || st.streams.open >= cap {
        sg.Unlock()
        wg.Unlock()
        return none
    }
    max_list := st.peer_settings.max_header_list
    mf := st.peer_settings.max_frame
    bound := ValidateRequestFields(a, wr, pseudo, list, max_list) else e {
        sg.Unlock()
        wg.Unlock()
        fail e
    }
    blen := EncodeRequestBlock(a, wr, pseudo, list, bound) else oe {
        sg.Unlock()
        wg.Unlock()
        fail oe
    }
    // Pre-reserve wr.cur worst-case frame headers + block before advancing stream id.
    frame_cnt := if blen == 0 { 1 } else { (blen + mf - 1) / mf }
    EnsureLen(a, mut wr.cur, wr.cur.Len() + blen + frame_cnt * 9) else oe {
        sg.Unlock()
        wg.Unlock()
        fail oe
    }
    sid := st.next_stream_id
    init_recv := if st.our_settings_acked || self.lim.window >= InitialWindow { self.lim.window } else { InitialWindow }
    oidx := st.streams.AllocSlot(a, sid, st.peer_settings.initial_window, init_recv, self.lim.window) else oe {
        sg.Unlock()
        wg.Unlock()
        fail oe
    }
    idx := oidx else {
        sg.Unlock()
        wg.Unlock()
        return none
    }
    st.next_stream_id += 2
    st.drive_idle_since = none
    sgen := InitClientSlot(st, idx, pseudo.method == "HEAD", end_stream)
    // Trim wr.cur back to exact pre-append length and emit HEADERS frames.
    start_len := wr.cur.Len() - (blen + frame_cnt * 9)
    for wr.cur.Len() > start_len {
        _ = wr.cur.Pop()
    }
    AppendHeadersFrames(a, mut wr.cur, sid, wr.hbuf.Items()[0..blen], mf, end_stream) else oe {
        sg.Unlock()
        wg.Unlock()
        fail oe
    }
    sg.Unlock()
    wg.Unlock()
    sref := StreamRef(index: idx, gen_: sgen)
    self.FlushWrites(io) else e {
        self.Release(sref)
        fail e
    }
    return some(sref)
}

InitClientSlot(st: mut State, idx: int, is_head: bool, end_stream: bool): u32 {
    slot := mut st.streams.slots.ItemsMut()[idx]
    slot.refs = 1
    slot.head_method = is_head
    slot.state = if end_stream { StHalfLocal } else { StOpen }
    return slot.gen_
}

/// Writes request body bytes on s, waiting for stream and connection send window
/// credit under MinDataUnit, DribbleWait and ProgressFloor. Sends END_STREAM
/// when end is true. Errors: net.StreamReset, net.MuxClosed, net.Error,
/// time.TimedOut, Canceled.
export Conn.Send(&self, io: Io, s: StreamRef, data: []u8, end: bool): Error!void {
    try self.SendDataStream(io, s, data, end)
}

/// Waits for the final response head of s (skipping 1xx interim responses) and
/// copies its FieldViews into a. Errors: TooLarge, BadField, net.StreamReset,
/// net.MuxClosed, net.Error, time.TimedOut, Canceled.
export Conn.Head(&self, a: &Arena, io: Io, s: StreamRef): Error!(status: int, list: []fields.FieldView, content_length: ?i64) from a {
    nc := io.NoCancel()
    h_alloc := self.heap.Alloc()
    core_ := self.Core()
    for {
        self.FlushWrites(io) else {}
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        if s.index < 0 || s.index >= st.streams.slots.Len() {
            f := st.fault
            sg.Unlock()
            if err := f {
                fail err
            }
            fail Closed
        }
        goaway_code := st.goaway_code
        conn_fault := st.fault
        slot := mut st.streams.slots.ItemsMut()[s.index]
        stale := slot.gen_ != s.gen_ || slot.state == StIdle
        found_blk := if !stale && slot.cli_head.IsSome() {
            core.Replace(mut slot.cli_head, none)
        } else {
            none
        }
        status := if !stale { slot.cli_head_status } else { 0 }
        cl := if !stale { slot.cli_head_cl } else { none }
        too_large := !stale && found_blk.IsNone() && slot.cli_head_too_large
        malformed := !stale && found_blk.IsNone() && !too_large && slot.cli_head_malformed
        unproc := !stale && found_blk.IsNone() && !too_large && !malformed && slot.unprocessed
        rst_code := if !stale && found_blk.IsNone() && !too_large && !malformed && !unproc {
            slot.reset
        } else {
            none
        }
        if b := found_blk {
            var blk := b
            bbytes := blk.Bytes()
            if st.retained_head_bytes >= bbytes {
                st.retained_head_bytes -= bbytes
            } else {
                st.retained_head_bytes = 0
            }
            sg.Unlock()
            cnt := blk.Len()
            var out := List[fields.FieldView].New(a)
            for i in 0..cnt {
                if txt := blk.Text(i) {
                    name_copy := a.Dup(txt.name) else oe {
                        blk.Free(h_alloc)
                        fail oe
                    }
                    val_copy := a.Dup(txt.value) else oe {
                        blk.Free(h_alloc)
                        fail oe
                    }
                    out.Push(fields.FieldView(name: name_copy, value: val_copy)) else oe {
                        blk.Free(h_alloc)
                        fail oe
                    }
                } else {
                    blk.Free(h_alloc)
                    fail BadField
                }
            }
            blk.Free(h_alloc)
            done_out := out.Done()
            return (status: status, list: done_out[..], content_length: cl)
        }
        sg.Unlock()
        if too_large {
            fail TooLarge
        }
        if malformed {
            fail BadField
        }
        if unproc {
            fail net.MuxClosed(code: u64(goaway_code), app: true, peer: true)
        }
        if rc := rst_code {
            fail net.StreamReset(code: u64(rc))
        }
        if err := conn_fault {
            fail err
        }
        if stale {
            fail Closed
        }
        try lead.Wait(self, io, ReaderParty(s.index))
    }
}

/// Reads response body bytes of s into buf: returns byte count, or 0 at
/// END_STREAM. Updates stream receive credit and emits WINDOW_UPDATEs when
/// owed. Errors: BadField, net.StreamReset, net.MuxClosed, net.Error, Canceled.
export Conn.Read(&self, io: Io, s: StreamRef, buf: mut []u8): Error!int {
    if buf.Len() == 0 {
        return 0
    }
    nc := io.NoCancel()
    h_alloc := self.heap.Alloc()
    core_ := self.Core()
    for {
        self.FlushWrites(io) else {}
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        if s.index < 0 || s.index >= st.streams.slots.Len() {
            f := st.fault
            sg.Unlock()
            if err := f {
                fail err
            }
            fail Closed
        }
        var stale := false
        var bad_body := false
        var eof := false
        var unproc := false
        var rst_code: ?u32 = none
        goaway_code := st.goaway_code
        conn_fault := st.fault
        var copied := 0
        var wake_writer := false
        if st.streams.slots.Items()[s.index].gen_ != s.gen_ || st.streams.slots.Items()[s.index].state == StIdle {
            stale = true
        }
        if !stale {
            copied = st.streams.ReadClientData(h_alloc, s.index, buf)
            if copied > 0 {
                slot := mut st.streams.slots.ItemsMut()[s.index]
                slot.recv_credit.Consumed(copied)
                inc := slot.recv_credit.Owed()
                if inc > 0 && slot.state != StClosed && slot.state != StHalfRemote {
                    slot.owed_credit += inc
                    slot.recv_win.Grow(inc) else {}
                    wake_writer = true
                }
            } else {
                slot := &st.streams.slots.Items()[s.index]
                bad_body = slot.cli_body_malformed
                eof = slot.cli_eof
                unproc = slot.unprocessed
                rst_code = slot.reset
            }
        }
        if wake_writer {
            EnqueueOwedStream(st, s.index)
        }
        sg.Unlock()
        if copied > 0 {
            if wake_writer {
                self.FlushWrites(io) else {}
            }
            return copied
        }
        if bad_body {
            fail BadField
        }
        if eof {
            return 0
        }
        if unproc {
            fail net.MuxClosed(code: u64(goaway_code), app: true, peer: true)
        }
        if rc := rst_code {
            fail net.StreamReset(code: u64(rc))
        }
        if err := conn_fault {
            fail err
        }
        if stale {
            fail Closed
        }
        try lead.Wait(self, io, ReaderParty(s.index))
    }
}

/// Ends the caller's hold on client stream s: queues RST_STREAM(CANCEL) if the
/// stream has not finished both sides, updates idle tracking, and releases the
/// slot when idle. Never waits. O(1); allocates nothing.
export Conn.Release(&self, s: StreamRef) {
    nc := core.IoMasked(rt.NativeIo())
    h_alloc := self.heap.Alloc()
    core_ := self.Core()
    now := time.Now(nc)
    var wake := false
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    if s.index >= 0 && s.index < st.streams.slots.Len() {
        (matched, sid, was_open, release, dropped_bytes) := ReleaseClientSlot(st, s.index, s.gen_)
        if matched {
            if dropped_bytes > 0 {
                if st.retained_head_bytes >= dropped_bytes {
                    st.retained_head_bytes -= dropped_bytes
                } else {
                    st.retained_head_bytes = 0
                }
            }
            if was_open {
                st.reset_set.Add(sid)
                EnqueueOwedStream(st, s.index)
                wake = true
            }
            if !was_open && release {
                st.streams.ReleaseSlot(s.index, h_alloc)
            }
            if st.streams.active == 0 && st.drive_idle_since.IsNone() {
                st.drive_idle_since = some(now)
            }
        }
    }
    sg.Unlock()
    if wake {
        core_.wr_wake.NotifyOne()
    }
    self.WakeAll()
}

ReleaseClientSlot(st: mut State, idx: int, gen_: u32): (matched: bool, sid: u32, was_open: bool, release: bool, dropped_bytes: int) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.gen_ != gen_ || slot.state == StIdle {
        return (matched: false, sid: 0, was_open: false, release: false, dropped_bytes: 0)
    }
    sid := slot.id
    var dropped_bytes := 0
    if h := &slot.cli_head {
        dropped_bytes = h.Bytes()
    }
    if slot.refs > 0 {
        slot.refs -= 1
    }
    was_open := slot.state != StClosed
    if was_open {
        slot.owed_reset = some(Cancel)
        slot.progress.Idle()
        st.streams.CloseSlot(idx)
    }
    slot2 := &st.streams.slots.Items()[idx]
    release := slot2.state == StClosed && slot2.refs == 0 && !slot2.in_owed
    return (matched: true, sid: sid, was_open: was_open, release: release, dropped_bytes: dropped_bytes)
}
