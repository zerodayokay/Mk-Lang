// Why: HTTP/2 HEADERS, PUSH_PROMISE and CONTINUATION blocks must all be
// HPACK-decoded in wire order to keep the dynamic table synchronized, even
// for refused, closed or post-GOAWAY streams, while separating connection-fatal
// compression errors from stream-scoped malformed-message errors.
//
// What: DispatchHeaderSection, DecodeInto, SendInlineStatus, and role-specific
// header/trailer handlers.
//
// How: decodes into rp.scratch_block (or DiscardSink for ignored streams),
// validates with fields.CheckRequest / CheckResponse / CheckTrailers, enforces
// RFC 9113 stream state and concurrency rules, and queues server body
// admission or ready requests without holding locks across I/O.

import std.core
import std.http.fields
import std.time

DecodeInto[S: fields.Sink](dec: mut fields.Decoder, a: &Alloc, sink: mut S, block: []u8): Error!fields.Outcome {
    return dec.Decode(a, sink, block) else e {
        match e {
            OutOfMemory { fail OutOfMemory }
            _ { fail Violation(code: CompressionError, stream: 0) }
        }
    }
}

DecodeDiscard(dec: mut fields.Decoder, a: &Alloc, block: []u8): Error!void {
    var sink := DiscardSink(unused: 0)
    _ = try DecodeInto(dec, a, mut sink, block)
}

AsciiFoldEq(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        var ca := a[i]
        var cb := b[i]
        if ca >= b'A' && ca <= b'Z' {
            ca += 32
        }
        if cb >= b'A' && cb <= b'Z' {
            cb += 32
        }
        if ca != cb {
            return false
        }
    }
    return true
}

HasExpect100(b: &fields.FieldBlock): bool {
    exp := "expect".AsBytes()
    cont := "100-continue".AsBytes()
    for f in b.Iter() {
        if CoreBytesEq(f.name, exp) && AsciiFoldEq(f.value, cont) {
            return true
        }
    }
    return false
}

// SendInlineStatus encodes a single-status response head (413, 431, 501) into
// wr.cur and wakes the writer without allocating a stream handler.
Conn.SendInlineStatus(&self, nc: Io, stream: u32, code: int, end_stream: bool): Error!void {
    a := self.heap.Alloc()
    core_ := self.Core()
    var wg := core_.wr.Lock(nc).Must()
    wr := mut *wg.Mut()
    sg := core_.st.Lock(nc).Must()
    mf := sg.Ref().peer_settings.max_frame
    sg.Unlock()
    blen := EncodeStatusBlock(a, wr, code) else e {
        wg.Unlock()
        fail e
    }
    AppendHeadersFrames(a, mut wr.cur, stream, wr.hbuf.Items()[0..blen], mf, end_stream) else e {
        wg.Unlock()
        fail e
    }
    wg.Unlock()
    core_.wr_wake.NotifyOne()
}

Conn.DispatchHeaderSection(&self, nc: Io, rp: mut ReadParser, kind: u8, stream: u32, promised: u32, block: []u8, end_stream: bool, now: time.Instant, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    if self.is_client {
        try self.DispatchClientHeaders(nc, rp, kind, stream, promised, block, end_stream, now)
        return
    }
    try self.DispatchServerHeaders(nc, rp, kind, stream, block, end_stream, now, accs, ready)
}

Conn.DispatchServerHeaders(&self, nc: Io, rp: mut ReadParser, kind: u8, stream: u32, block: []u8, end_stream: bool, now: time.Instant, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    a := self.heap.Alloc()
    if kind == TypePushPromise {
        fail Violation(code: ProtocolError, stream: 0)
    }
    if (stream & 1) == 0 {
        fail Violation(code: ProtocolError, stream: 0)
    }
    var mode := 0 // 0=new, 1=discard, 2=trailers, 3=stream_closed, 4=refused, 5=proto_conn_err
    var existing_idx := -1
    var sg0 := self.Core().st.Lock(nc).Must()
    st0 := mut *sg0.Mut()
    if st0.owed.goaway_sent && stream > st0.last_processed_id {
        mode = 1
    } else if stream <= st0.highest_peer_id {
        if st0.reset_set.Has(stream) {
            mode = 1
        } else {
            existing_idx = st0.streams.Find(stream)
            if existing_idx < 0 {
                mode = 5
            } else {
                slot := &st0.streams.slots.Items()[existing_idx]
                if slot.state == StHalfRemote || slot.state == StClosed {
                    mode = 3
                } else {
                    mode = 2
                }
            }
        }
    } else {
        st0.highest_peer_id = stream
        if st0.streams.open >= self.lim.streams {
            mode = 4
        }
    }
    sg0.Unlock()
    if mode == 5 {
        try DecodeDiscard(mut rp.dec, a, block)
        fail Violation(code: ProtocolError, stream: 0)
    }
    if mode == 1 {
        try DecodeDiscard(mut rp.dec, a, block)
        return
    }
    if mode == 3 {
        try DecodeDiscard(mut rp.dec, a, block)
        var sg := self.Core().st.Lock(nc).Must()
        st := mut *sg.Mut()
        MarkStreamError(a, st, existing_idx, StreamClosed, now) else e {
            sg.Unlock()
            fail e
        }
        self.WakeStream(existing_idx)
        sg.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    if mode == 4 {
        try DecodeDiscard(mut rp.dec, a, block)
        var sg := self.Core().st.Lock(nc).Must()
        st := mut *sg.Mut()
        ChargeReset(st, now) else ce {
            sg.Unlock()
            fail ce
        }
        st.owed.QueueReset(a, stream, RefusedStream) else oe {
            sg.Unlock()
            fail oe
        }
        sg.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    if mode == 2 {
        rp.scratch_block.Reset()
        out := try DecodeInto(mut rp.dec, a, mut rp.scratch_block, block)
        var ok := end_stream && out == fields.Outcome.Fits
        if ok {
            fields.CheckTrailers(&rp.scratch_block) else {
                ok = false
            }
        }
        rp.scratch_block.Reset()
        var sg := self.Core().st.Lock(nc).Must()
        st := mut *sg.Mut()
        var tr_gen: u32 = 0
        if ok {
            tslot := mut st.streams.slots.ItemsMut()[existing_idx]
            tr_gen = tslot.gen_
            if cl := tslot.content_length {
                if tslot.data_recvd != cl {
                    ok = false
                }
            }
            if ok {
                tslot.state = StHalfRemote
                tslot.progress.Idle()
                if tslot.id > st.last_processed_id {
                    st.last_processed_id = tslot.id
                }
                tslot.srv_dispatched = true
                tslot.refs = 1
            }
        }
        if !ok {
            MarkStreamError(a, st, existing_idx, ProtocolError, now) else e {
                sg.Unlock()
                fail e
            }
            sg.Unlock()
            self.WakeStream(existing_idx)
            if existing_idx < accs.Len() {
                ClearAcc(a, mut accs.ItemsMut()[existing_idx])
            }
            self.Core().wr_wake.NotifyOne()
            return
        }
        sg.Unlock()
        if existing_idx < accs.Len() {
            acc := mut accs.ItemsMut()[existing_idx]
            if acc.active && acc.gen_ == tr_gen {
                acc.active = false
                acc.ready = true
                try ready.Push(a, existing_idx)
            }
        }
        return
    }
    // New request stream.
    var head_blk := fields.FieldBlock.New(a)
    out := DecodeInto(mut rp.dec, a, mut head_blk, block) else e {
        head_blk.Free(a)
        fail e
    }
    if out == fields.Outcome.TooLarge {
        head_blk.Free(a)
        try self.SendInlineStatus(nc, stream, 431, true)
        if !end_stream {
            var sg := self.Core().st.Lock(nc).Must()
            st := mut *sg.Mut()
            st.reset_set.Add(stream)
            st.owed.QueueReset(a, stream, NoError) else oe {
                sg.Unlock()
                fail oe
            }
            sg.Unlock()
            self.Core().wr_wake.NotifyOne()
        }
        return
    }
    req_head := fields.CheckRequest(&head_blk) else {
        head_blk.Free(a)
        var sg := self.Core().st.Lock(nc).Must()
        st := mut *sg.Mut()
        ChargeReset(st, now) else ce {
            sg.Unlock()
            fail ce
        }
        st.owed.QueueReset(a, stream, ProtocolError) else oe {
            sg.Unlock()
            fail oe
        }
        sg.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    if req_head.connect {
        head_blk.Free(a)
        try self.SendInlineStatus(nc, stream, 501, true)
        if !end_stream {
            var sg := self.Core().st.Lock(nc).Must()
            st := mut *sg.Mut()
            st.reset_set.Add(stream)
            st.owed.QueueReset(a, stream, NoError) else oe {
                sg.Unlock()
                fail oe
            }
            sg.Unlock()
            self.Core().wr_wake.NotifyOne()
        }
        return
    }
    if cl := req_head.content_length {
        if cl > i64(self.lim.body_bytes) {
            head_blk.Free(a)
            try self.SendInlineStatus(nc, stream, 413, true)
            if !end_stream {
                var sg := self.Core().st.Lock(nc).Must()
                st := mut *sg.Mut()
                st.reset_set.Add(stream)
                st.owed.QueueReset(a, stream, NoError) else oe {
                    sg.Unlock()
                    fail oe
                }
                sg.Unlock()
                self.Core().wr_wake.NotifyOne()
            }
            return
        }
        if end_stream && req_head.method != "HEAD" && cl != 0 {
            head_blk.Free(a)
            var sg := self.Core().st.Lock(nc).Must()
            st := mut *sg.Mut()
            ChargeReset(st, now) else ce {
                sg.Unlock()
                fail ce
            }
            st.owed.QueueReset(a, stream, ProtocolError) else oe {
                sg.Unlock()
                fail oe
            }
            sg.Unlock()
            self.Core().wr_wake.NotifyOne()
            return
        }
    }
    expect_100 := !end_stream && HasExpect100(&head_blk)
    cl_val := req_head.content_length
    is_head := req_head.method == "HEAD"
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    init_recv := if st.our_settings_acked || self.lim.window >= InitialWindow { self.lim.window } else { InitialWindow }
    oidx := if st.streams.open >= self.lim.streams {
        none
    } else {
        st.streams.AllocSlot(a, stream, st.peer_settings.initial_window, init_recv, self.lim.window) else oe {
            sg.Unlock()
            head_blk.Free(a)
            fail oe
        }
    }
    idx := oidx else {
        sg.Unlock()
        head_blk.Free(a)
        var sg2 := self.Core().st.Lock(nc).Must()
        st2 := mut *sg2.Mut()
        ChargeReset(st2, now) else ce {
            sg2.Unlock()
            fail ce
        }
        st2.owed.QueueReset(a, stream, RefusedStream) else oe {
            sg2.Unlock()
            fail oe
        }
        sg2.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    (need_owed, gen_: sgen) := SetupServerSlotFields(a, st, idx, stream, cl_val, is_head, expect_100, end_stream, self.lim.window, self.lim.body_bytes, now) else oe {
        sg.Unlock()
        head_blk.Free(a)
        fail oe
    }
    if need_owed {
        EnqueueOwedStream(st, idx)
    }
    sg.Unlock()
    if need_owed {
        self.Core().wr_wake.NotifyOne()
    }
    EnsureServerAcc(a, accs, idx) else e {
        head_blk.Free(a)
        fail e
    }
    acc := mut accs.ItemsMut()[idx]
    acc.active = !end_stream
    acc.ready = end_stream
    acc.gen_ = sgen
    match core.Replace(mut acc.head, some(head_blk)) {
        some(old) { old.Free(a) }
        none {}
    }
    if end_stream {
        if b := mut acc.body {
            b.Clear()
        } else {
            match core.Replace(mut acc.body, some(HeapList[u8].New(a))) {
                some(old) { old.Free(a) }
                none {}
            }
        }
        try ready.Push(a, idx)
        return
    }
    match core.Replace(mut acc.body, some(HeapList[u8].New(a))) {
        some(old) { old.Free(a) }
        none {}
    }
}

SetupServerSlotFields(a: &Alloc into st, st: mut State, idx: int, stream: u32, cl_val: ?i64, is_head: bool, expect_100: bool, end_stream: bool, lim_win: int, body_bytes: int, now: time.Instant): OutOfMemory!(need_owed: bool, gen_: u32) {
    var need_owed := false
    var admitted := false
    reserve := if cl := cl_val { int(cl) } else { body_bytes }
    if !end_stream {
        admitted = try st.admission.Ask(a, stream, reserve)
    }
    slot := mut st.streams.slots.ItemsMut()[idx]
    slot.refs = if end_stream { 1 } else { 0 }
    slot.content_length = cl_val
    slot.head_method = is_head
    slot.expect_100 = expect_100
    if end_stream {
        slot.state = StHalfRemote
        slot.progress.Idle()
        if slot.id > st.last_processed_id {
            st.last_processed_id = slot.id
        }
        slot.srv_dispatched = true
    } else {
        slot.state = StOpen
        slot.admitted_win = lim_win
        slot.admit_reserve = reserve
        slot.progress.Pending(now)
        if admitted {
            slot.admitted = true
            if reserve > slot.admitted_win {
                delta := reserve - slot.admitted_win
                slot.recv_win.Grow(delta) else {}
                slot.owed_credit += delta
                slot.admitted_win = reserve
                need_owed = true
            }
            if expect_100 {
                slot.owed_100 = true
                need_owed = true
            }
        }
    }
    return (need_owed: need_owed, gen_: slot.gen_)
}

Conn.DispatchClientHeaders(&self, nc: Io, rp: mut ReadParser, kind: u8, stream: u32, promised: u32, block: []u8, end_stream: bool, now: time.Instant): Error!void {
    a := self.heap.Alloc()
    core_ := self.Core()
    if kind == TypePushPromise {
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        acked := st.our_settings_acked
        assoc_ok := !IsIdleStream(&*st, true, stream) && (st.reset_set.Has(stream) || st.streams.Find(stream) >= 0)
        prom_ok := (promised & 1) == 0 && promised > st.highest_peer_id
        valid := !acked && assoc_ok && prom_ok
        if valid {
            st.highest_peer_id = promised
            st.reserved_remote.Push(a, promised) else oe {
                sg.Unlock()
                fail oe
            }
            st.owed.QueueReset(a, promised, ProtocolError) else oe {
                sg.Unlock()
                fail oe
            }
        }
        sg.Unlock()
        try DecodeDiscard(mut rp.dec, a, block)
        if !valid {
            fail Violation(code: ProtocolError, stream: 0)
        }
        return
    }
    if (stream & 1) == 0 {
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        reserved := HasU32(&st.reserved_remote, stream)
        if reserved && end_stream {
            _ = RemoveU32(mut st.reserved_remote, stream)
        }
        sg.Unlock()
        try DecodeDiscard(mut rp.dec, a, block)
        if !reserved {
            fail Violation(code: ProtocolError, stream: 0)
        }
        return
    }
    var mode := 0 // 0=live, 1=discard, 2=proto_conn, 3=stream_closed
    var idx := -1
    var is_trailer := false
    sg0 := core_.st.Lock(nc).Must()
    st0 := &*sg0.Ref()
    if IsIdleStream(st0, true, stream) {
        mode = 2
    } else if st0.reset_set.Has(stream) {
        mode = 1
    } else {
        idx = st0.streams.Find(stream)
        if idx < 0 {
            mode = 2
        } else {
            slot := &st0.streams.slots.Items()[idx]
            if slot.state == StHalfRemote || slot.state == StClosed {
                mode = 3
            } else {
                is_trailer = slot.cli_head_ready
            }
        }
    }
    sg0.Unlock()
    if mode == 2 {
        try DecodeDiscard(mut rp.dec, a, block)
        fail Violation(code: ProtocolError, stream: 0)
    }
    if mode == 1 {
        try DecodeDiscard(mut rp.dec, a, block)
        return
    }
    if mode == 3 {
        try DecodeDiscard(mut rp.dec, a, block)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        MarkStreamError(a, st, idx, StreamClosed, now) else e {
            sg.Unlock()
            fail e
        }
        sg.Unlock()
        self.WakeStream(idx)
        return
    }
    if is_trailer {
        rp.scratch_block.Reset()
        out := try DecodeInto(mut rp.dec, a, mut rp.scratch_block, block)
        var ok := end_stream && out == fields.Outcome.Fits
        if ok {
            fields.CheckTrailers(&rp.scratch_block) else {
                ok = false
            }
        }
        rp.scratch_block.Reset()
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        if ok {
            slot := mut st.streams.slots.ItemsMut()[idx]
            skip_cl := slot.head_method || slot.cli_head_status == 204 || slot.cli_head_status == 304
            if cl := slot.content_length {
                if !skip_cl && slot.data_recvd != cl {
                    ok = false
                }
            }
            if ok {
                slot.cli_eof = true
                st.streams.EndRemote(idx)
                slot2 := &st.streams.slots.Items()[idx]
                if slot2.state == StClosed && slot2.refs == 0 && !slot2.in_owed {
                    st.streams.ReleaseSlot(idx, a)
                }
            }
        }
        if !ok {
            st.streams.slots.ItemsMut()[idx].cli_body_malformed = true
            MarkStreamError(a, st, idx, ProtocolError, now) else e {
                sg.Unlock()
                fail e
            }
        }
        sg.Unlock()
        self.WakeStream(idx)
        return
    }
    var blk := fields.FieldBlock.New(a)
    out := DecodeInto(mut rp.dec, a, mut blk, block) else e {
        blk.Free(a)
        fail e
    }
    if out == fields.Outcome.TooLarge {
        blk.Free(a)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        MarkClientHeadTooLarge(st, idx, stream)
        sg.Unlock()
        self.WakeStream(idx)
        return
    }
    resp := fields.CheckResponse(&blk) else {
        blk.Free(a)
        var sg := core_.st.Lock(nc).Must()
        st := mut *sg.Mut()
        st.streams.slots.ItemsMut()[idx].cli_head_malformed = true
        MarkStreamError(a, st, idx, ProtocolError, now) else e {
            sg.Unlock()
            fail e
        }
        sg.Unlock()
        self.WakeStream(idx)
        return
    }
    if resp.status >= 100 && resp.status < 200 {
        blk.Free(a)
        if end_stream {
            var sg := core_.st.Lock(nc).Must()
            st := mut *sg.Mut()
            st.streams.slots.ItemsMut()[idx].cli_head_malformed = true
            MarkStreamError(a, st, idx, ProtocolError, now) else e {
                sg.Unlock()
                fail e
            }
            sg.Unlock()
            self.WakeStream(idx)
        }
        return
    }
    blk_bytes := blk.Bytes()
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    if st.retained_head_bytes + blk_bytes > 4 * self.lim.header_bytes {
        MarkClientHeadTooLarge(st, idx, stream)
        sg.Unlock()
        blk.Free(a)
        self.WakeStream(idx)
        return
    }
    bad_cl := CheckClientEndCl(st, idx, end_stream, resp.status, resp.content_length)
    if bad_cl {
        MarkStreamError(a, st, idx, ProtocolError, now) else e {
            sg.Unlock()
            blk.Free(a)
            fail e
        }
        sg.Unlock()
        blk.Free(a)
        self.WakeStream(idx)
        return
    }
    StoreClientHead(a, st, idx, blk_bytes, resp.status, resp.content_length, end_stream, blk)
    sg.Unlock()
    self.WakeStream(idx)
}

MarkClientHeadTooLarge(st: mut State, idx: int, stream: u32) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    slot.cli_head_too_large = true
    slot.owed_reset = some(Cancel)
    slot.progress.Idle()
    st.streams.CloseSlot(idx)
    st.reset_set.Add(stream)
    EnqueueOwedStream(st, idx)
}

CheckClientEndCl(st: mut State, idx: int, end_stream: bool, status: int, cl: ?i64): bool {
    slot := mut st.streams.slots.ItemsMut()[idx]
    skip_cl := slot.head_method || status == 204 || status == 304
    if end_stream && !skip_cl {
        if c := cl {
            if c != 0 {
                slot.cli_head_malformed = true
                return true
            }
        }
    }
    return false
}

StoreClientHead(a: &Alloc into st, st: mut State, idx: int, blk_bytes: int, status: int, cl: ?i64, end_stream: bool, taken: fields.FieldBlock into st) {
    st.retained_head_bytes += blk_bytes
    slot := mut st.streams.slots.ItemsMut()[idx]
    match core.Replace(mut slot.cli_head, some(taken)) {
        some(warm) {
            var w := warm
            w.Free(a)
        }
        none {}
    }
    slot.cli_head_ready = true
    slot.cli_head_status = status
    slot.cli_head_cl = cl
    slot.content_length = cl
    if end_stream {
        slot.cli_eof = true
        st.streams.EndRemote(idx)
    }
}
