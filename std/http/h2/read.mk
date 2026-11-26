// Why: HTTP/2 frames arrive over a byte stream and must be reassembled,
// bounded, HPACK-decoded in strict wire order, validated against connection
// and stream state machines, and dispatched in batches without per-frame
// allocation or blocking on socket writes.
//
// What: ReadBuf, ReadParser, ReadState, ReadAndDispatch, DispatchBuffered,
// DispatchFrame, and frame handlers.
//
// How: ReadState separates ReadBuf (the 8 KiB..16,393-byte receive buffer)
// from ReadParser (HPACK decoder, CONTINUATION assembly and ready_reqs) so
// parsed frames borrowing ReadBuf never conflict with mutating ReadParser.

import std.core
import std.http.fields
import std.task
import std.time

const ReadBufInit: int = 8192

linear view struct ReadBuf {
    buf: HeapList[u8]
    start: int
    end: int
    magic_off: int
}

ReadBuf.New(a: &Alloc, is_client: bool): OutOfMemory!ReadBuf {
    var buf := HeapList[u8].New(a)
    onerr buf.Free(a)
    try EnsureLen(a, mut buf, ReadBufInit)
    return ReadBuf(buf: buf, start: 0, end: 0, magic_off: if is_client { 24 } else { 0 })
}

ReadBuf.Free(self, a: &Alloc) {
    ReadBuf(buf, ..) := self
    buf.Free(a)
}

ReadBuf.Compact(mut self) {
    if self.start == 0 {
        return
    }
    rem := self.end - self.start
    if rem > 0 {
        items := self.buf.ItemsMut()
        for i in 0..rem {
            items[i] = items[self.start + i]
        }
    }
    self.start = 0
    self.end = rem
}

ReadBuf.NeedMore(&self, is_client: bool): Violation!bool {
    if !is_client && self.magic_off < 24 {
        avail := self.end - self.start
        check := if avail < 24 - self.magic_off { avail } else { 24 - self.magic_off }
        for i in 0..check {
            if self.buf.Items()[self.start + i] != Preface[self.magic_off + i] {
                fail Violation(code: ProtocolError, stream: 0)
            }
        }
        if avail <= 24 - self.magic_off {
            return true
        }
    }
    off := if !is_client && self.magic_off < 24 { self.start + (24 - self.magic_off) } else { self.start }
    h := ParseHeader(self.buf.Items()[off..self.end]) else {
        return true
    }
    if h.length > InitialSettings.max_frame {
        fail Violation(code: FrameSizeError, stream: 0)
    }
    return (self.end - off) < 9 + h.length
}

linear view struct ServerAcc {
    active: bool
    ready: bool
    gen_: u32
    head: ?fields.FieldBlock
    body: ?HeapList[u8]
}

impl Free for ServerAcc {
    Free(self, a: &Alloc) {
        ServerAcc(active: _, ready: _, gen_: _, head, body) := self
        match head {
            some(h) { h.Free(a) }
            none {}
        }
        match body {
            some(b) { b.Free(a) }
            none {}
        }
    }
}

EnsureServerAcc(a: &Alloc into accs, accs: mut HeapList[ServerAcc], idx: int): OutOfMemory!void {
    for accs.Len() <= idx {
        var acc := ServerAcc(active: false, ready: false, gen_: 0, head: none, body: none)
        accs.Push(a, acc) else e {
            acc.Free(a)
            fail e
        }
    }
}

linear view struct ReadParser {
    preface_done: bool
    cont_stream: u32
    cont_count: int
    cont_end_stream: bool
    cont_kind: u8
    cont_promised: u32
    dec: fields.Decoder
    scratch_block: fields.FieldBlock
}

ReadParser.New(a: &Alloc, header_bytes: int): OutOfMemory!ReadParser {
    var dec := try fields.Decoder.New(a, fields.DefaultTableBytes, header_bytes)
    onerr dec.Free(a)
    return ReadParser(
        preface_done: false,
        cont_stream: 0,
        cont_count: 0,
        cont_end_stream: false,
        cont_kind: 0,
        cont_promised: 0,
        dec: dec,
        scratch_block: fields.FieldBlock.New(a),
    )
}

ReadParser.Free(self, a: &Alloc) {
    ReadParser(preface_done: _, cont_stream: _, cont_count: _, cont_end_stream: _, cont_kind: _, cont_promised: _, dec, scratch_block) := self
    dec.Free(a)
    var sb := scratch_block
    sb.Free(a)
}

linear view struct ReadState {
    rbuf: ReadBuf
    hblock: HeapList[u8]
    rp: ReadParser
}

ReadState.New(a: &Alloc, header_bytes: int, is_client: bool): OutOfMemory!ReadState {
    var rb := try ReadBuf.New(a, is_client)
    onerr rb.Free(a)
    rp := try ReadParser.New(a, header_bytes)
    return ReadState(rbuf: rb, hblock: HeapList[u8].New(a), rp: rp)
}

ReadState.Free(self, a: &Alloc) {
    ReadState(rbuf, hblock, rp) := self
    rbuf.Free(a)
    hblock.Free(a)
    rp.Free(a)
}

ReadState.Seed(mut self, a: &Alloc into self, pre: []u8): OutOfMemory!void {
    if pre.Len() > self.rbuf.buf.Len() {
        try EnsureLen(a, mut self.rbuf.buf, pre.Len())
    }
    (mut self.rbuf.buf.ItemsMut()[0..pre.Len()]).CopyFrom(pre)
    self.rbuf.start = 0
    self.rbuf.end = pre.Len()
}

ReadState.ShrinkIdle(mut self, a: &Alloc into self) {
    if self.rbuf.start == self.rbuf.end {
        self.rbuf.start = 0
        self.rbuf.end = 0
        if self.rbuf.buf.Len() > ReadBufInit {
            var nb := HeapList[u8].New(a)
            var ok := true
            EnsureLen(a, mut nb, ReadBufInit) else {
                ok = false
            }
            if ok {
                old := core.Replace(mut self.rbuf.buf, nb)
                old.Free(a)
            } else {
                nb.Free(a)
            }
        }
        if self.hblock.Len() == 0 {
            old_hb := core.Replace(mut self.hblock, HeapList[u8].New(a))
            old_hb.Free(a)
        }
    }
}

IsIdleStream(st: &State, is_client: bool, stream: u32): bool {
    if stream == 0 {
        return false
    }
    odd := (stream & 1) == 1
    if is_client {
        return if odd { stream >= st.next_stream_id } else { stream > st.highest_peer_id }
    }
    return if odd { stream > st.highest_peer_id } else { true }
}

HasU32(list: &HeapList[u32], v: u32): bool {
    for x in list.Items() {
        if x == v {
            return true
        }
    }
    return false
}

RemoveU32(list: mut HeapList[u32], v: u32): bool {
    for i, x in list.Items() {
        if x == v {
            _ = list.Remove(i)
            return true
        }
    }
    return false
}

ChargeReset(st: mut State, now: time.Instant): Violation!void {
    if !st.reset_bucket.Take(now) {
        fail Violation(code: EnhanceYourCalm, stream: 0)
    }
}

ChargeControl(st: mut State, now: time.Instant): Violation!void {
    if !st.control_bucket.Take(now) {
        fail Violation(code: EnhanceYourCalm, stream: 0)
    }
}

MarkStreamError(a: &Alloc, st: mut State, idx: int, code: u32, now: time.Instant): Violation|OutOfMemory!void {
    try ChargeReset(st, now)
    var admitted_ids: [16]u32 = [0; 16]
    admitted_n := MarkSlotReset(st, idx, code, mut admitted_ids[..])
    if admitted_n > 0 {
        AdmitStreams(st, admitted_ids[0..admitted_n])
    }
    EnqueueOwedStream(st, idx)
}

MarkSlotReset(st: mut State, idx: int, code: u32, admitted_ids: mut []u32): int {
    slot := mut st.streams.slots.ItemsMut()[idx]
    sid := slot.id
    slot.owed_reset = some(code)
    slot.reset = some(code)
    slot.progress.Idle()
    reserve := slot.admit_reserve
    was_admitted := slot.admitted
    slot.admit_reserve = 0
    slot.admitted = false
    st.reset_set.Add(sid)
    st.streams.CloseSlot(idx)
    if reserve > 0 {
        if was_admitted {
            return st.admission.Return(reserve, admitted_ids)
        }
        st.admission.Cancel(sid)
    }
    return 0
}

// ReadAndDispatch reads up to one socket batch (if needed) under deadline and
// dispatches all complete frames in rd.rbuf. Returns false on clean EOF.
Conn.ReadAndDispatch(&self, io: Io, nc: Io, deadline: ?time.Duration, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!bool {
    a := self.heap.Alloc()
    core_ := self.Core()
    var rg := core_.rd.Lock(nc).Must()
    rd := mut *rg.Mut()
    need := rd.rbuf.NeedMore(self.is_client) else e {
        rg.Unlock()
        self.FailConn(nc, e, some(e.code), false)
        fail e
    }
    if need {
        rd.rbuf.Compact()
        var target := ReadBufInit
        off := if !self.is_client && rd.rbuf.magic_off < 24 { 24 - rd.rbuf.magic_off } else { 0 }
        if rd.rbuf.end - rd.rbuf.start >= off + 9 {
            if h := ParseHeader(rd.rbuf.buf.Items()[rd.rbuf.start + off..rd.rbuf.end]) {
                if off + 9 + h.length > target {
                    target = off + 9 + h.length
                }
            }
        }
        if target > rd.rbuf.buf.Len() {
            EnsureLen(a, mut rd.rbuf.buf, target) else e {
                rg.Unlock()
                self.FailConn(nc, e, none, true)
                fail e
            }
        }
        var n := 0
        var rerr: ?Error = none
        var expired := false
        cap := rd.rbuf.buf.Len()
        if dl := deadline {
            scope s := io.Scope() {
                s.CancelAfter(dl)
                n = self.NetConn().Read(io, mut rd.rbuf.buf.ItemsMut()[rd.rbuf.end..cap]) else e {
                    rerr = some(e)
                    canceled := match e {
                        Canceled { true }
                        _ { false }
                    }
                    expired = task.LimitHit(&s, canceled)
                    0
                }
            }
        } else {
            n = self.NetConn().Read(io, mut rd.rbuf.buf.ItemsMut()[rd.rbuf.end..cap]) else e {
                rerr = some(e)
                0
            }
        }
        if e := rerr {
            rg.Unlock()
            if expired {
                fail time.TimedOut
            }
            match e {
                Canceled { fail Canceled }
                _ {
                    self.FailConn(nc, e, none, true)
                    fail e
                }
            }
        }
        if n == 0 {
            partial := rd.rbuf.end > rd.rbuf.start || !rd.rp.preface_done || rd.rp.cont_stream != 0
            rg.Unlock()
            if partial {
                self.FailConn(nc, Closed, none, true)
                fail Closed
            }
            return false
        }
        rd.rbuf.end += n
    }
    self.DispatchBuffered(nc, rd, accs, ready) else e {
        rg.Unlock()
        match e {
            Violation(code, stream: _) { self.FailConn(nc, e, some(code), false) }
            _ { self.FailConn(nc, e, none, true) }
        }
        fail e
    }
    sg := self.Core().st.Lock(nc).Must()
    idle := sg.Ref().streams.active == 0
    sg.Unlock()
    if idle {
        rd.ShrinkIdle(a)
    }
    rg.Unlock()
    if self.is_client {
        self.FlushWrites(io) else {}
    }
    return true
}

Conn.DispatchBuffered(&self, nc: Io, rd: mut ReadState, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    for rd.rbuf.start < rd.rbuf.end {
        if !self.is_client && rd.rbuf.magic_off < 24 {
            avail := rd.rbuf.end - rd.rbuf.start
            need := 24 - rd.rbuf.magic_off
            take := if avail < need { avail } else { need }
            for i in 0..take {
                if rd.rbuf.buf.Items()[rd.rbuf.start + i] != Preface[rd.rbuf.magic_off + i] {
                    fail Violation(code: ProtocolError, stream: 0)
                }
            }
            rd.rbuf.start += take
            rd.rbuf.magic_off += take
            if rd.rbuf.magic_off < 24 {
                break
            }
            continue
        }
        h := ParseHeader(rd.rbuf.buf.Items()[rd.rbuf.start..rd.rbuf.end]) else {
            break
        }
        if h.length > InitialSettings.max_frame {
            fail Violation(code: FrameSizeError, stream: 0)
        }
        total := 9 + h.length
        if rd.rbuf.end - rd.rbuf.start < total {
            break
        }
        var handled_bad := false
        f := Parse(&h, rd.rbuf.buf.Items()[rd.rbuf.start..rd.rbuf.start + total], InitialSettings.max_frame) else ve {
            try self.HandleParseViolation(nc, &rd.rp, ve)
            handled_bad = true
            Frame.Unknown(kind: 0, stream: 0)
        }
        if handled_bad {
            rd.rbuf.start += total
            continue
        }
        try self.DispatchFrame(nc, mut rd.hblock, mut rd.rp, f, accs, ready)
        rd.rbuf.start += total
    }
}

Conn.HandleParseViolation(&self, nc: Io, rp: &ReadParser, ve: Violation): Error!void {
    a := self.heap.Alloc()
    if rp.cont_stream != 0 {
        fail Violation(code: ProtocolError, stream: 0)
    }
    if ve.stream == 0 {
        fail ve
    }
    now := time.Now(nc)
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    if IsIdleStream(&*st, self.is_client, ve.stream) {
        sg.Unlock()
        fail Violation(code: ve.code, stream: 0)
    }
    ChargeReset(st, now) else ce {
        sg.Unlock()
        fail ce
    }
    idx := st.streams.Find(ve.stream)
    if idx >= 0 {
        MarkStreamError(a, st, idx, ve.code, now) else me {
            sg.Unlock()
            fail me
        }
        self.WakeStream(idx)
    } else if !st.reset_set.Has(ve.stream) {
        st.owed.QueueReset(a, ve.stream, ve.code) else oe {
            sg.Unlock()
            fail oe
        }
    }
    sg.Unlock()
    self.Core().wr_wake.NotifyOne()
}

Conn.DispatchFrame(&self, nc: Io, hblock: mut HeapList[u8], rp: mut ReadParser, f: Frame, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    a := self.heap.Alloc()
    now := time.Now(nc)
    if !rp.preface_done {
        match f {
            .Settings(ack, entries: _) {
                if ack {
                    fail Violation(code: ProtocolError, stream: 0)
                }
                rp.preface_done = true
            }
            _ {
                fail Violation(code: ProtocolError, stream: 0)
            }
        }
    }
    if rp.cont_stream != 0 {
        match f {
            .Continuation(stream, block, end_headers) {
                if stream != rp.cont_stream {
                    fail Violation(code: ProtocolError, stream: 0)
                }
                rp.cont_count += if block.Len() == 0 && !end_headers { 2 } else { 1 }
                if rp.cont_count > MaxContinuations {
                    fail Violation(code: EnhanceYourCalm, stream: 0)
                }
                if hblock.Len() + block.Len() > self.lim.header_bytes {
                    fail Violation(code: EnhanceYourCalm, stream: 0)
                }
                try hblock.Extend(a, block)
                if end_headers {
                    try self.FinishHeaderBlock(nc, hblock, rp, now, accs, ready)
                }
                return
            }
            _ {
                fail Violation(code: ProtocolError, stream: 0)
            }
        }
    }
    match f {
        .Continuation(stream: _, block: _, end_headers: _) {
            fail Violation(code: ProtocolError, stream: 0)
        }
        .Settings(ack, entries) {
            try self.HandleSettings(nc, rp, ack, entries, now)
        }
        .Ping(ack, data) {
            if !ack {
                var sg := self.Core().st.Lock(nc).Must()
                st := mut *sg.Mut()
                ChargeControl(st, now) else ce {
                    sg.Unlock()
                    fail ce
                }
                st.owed.QueuePing(data) else pe {
                    sg.Unlock()
                    fail pe
                }
                sg.Unlock()
                self.Core().wr_wake.NotifyOne()
            }
        }
        .Goaway(last, code, debug: _) {
            self.HandleGoaway(nc, last, code)
        }
        .WindowUpdate(stream, increment) {
            try self.HandleWindowUpdate(nc, stream, increment, now)
        }
        .Priority(stream) {
            var sg := self.Core().st.Lock(nc).Must()
            st := mut *sg.Mut()
            ChargeControl(st, now) else ce {
                sg.Unlock()
                fail ce
            }
            _ = stream
            sg.Unlock()
        }
        .RstStream(stream, code) {
            try self.HandleRstStream(nc, stream, code, now)
        }
        .Data(stream, data, padded, end) {
            try self.HandleData(nc, stream, data, padded, end, now, accs, ready)
        }
        .Headers(stream, block, padded: _, end_headers, end_stream) {
            if block.Len() > self.lim.header_bytes {
                fail Violation(code: EnhanceYourCalm, stream: 0)
            }
            if end_headers {
                try self.DispatchHeaderSection(nc, rp, TypeHeaders, stream, 0, block, end_stream, now, accs, ready)
                return
            }
            hblock.Clear()
            try hblock.Extend(a, block)
            rp.cont_stream = stream
            rp.cont_count = 0
            rp.cont_end_stream = end_stream
            rp.cont_kind = TypeHeaders
            rp.cont_promised = 0
        }
        .PushPromise(stream, promised, block, end_headers) {
            if block.Len() > self.lim.header_bytes {
                fail Violation(code: EnhanceYourCalm, stream: 0)
            }
            if end_headers {
                try self.DispatchHeaderSection(nc, rp, TypePushPromise, stream, promised, block, false, now, accs, ready)
                return
            }
            hblock.Clear()
            try hblock.Extend(a, block)
            rp.cont_stream = stream
            rp.cont_count = 0
            rp.cont_end_stream = false
            rp.cont_kind = TypePushPromise
            rp.cont_promised = promised
        }
        .Unknown(kind: _, stream: _) {
        }
    }
}

Conn.FinishHeaderBlock(&self, nc: Io, hblock: mut HeapList[u8], rp: mut ReadParser, now: time.Instant, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    stream := rp.cont_stream
    end_stream := rp.cont_end_stream
    kind := rp.cont_kind
    promised := rp.cont_promised
    rp.cont_stream = 0
    rp.cont_count = 0
    self.DispatchHeaderSection(nc, rp, kind, stream, promised, hblock.Items(), end_stream, now, accs, ready) else e {
        hblock.Clear()
        fail e
    }
    hblock.Clear()
}

Conn.HandleSettings(&self, nc: Io, rp: mut ReadParser, ack: bool, entries: []u8, now: time.Instant): Error!void {
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    if ack {
        if !st.our_settings_acked {
            st.our_settings_acked = true
            if st.our_initial_window < InitialWindow {
                delta := st.our_initial_window - InitialWindow
                for i in 0..st.streams.slots.Len() {
                    slot := mut st.streams.slots.ItemsMut()[i]
                    if slot.state == StOpen || slot.state == StHalfLocal {
                        slot.recv_win.Adjust(delta) else {}
                    }
                }
            }
        }
        if st.pending_table_bytes != st.acked_table_bytes {
            st.acked_table_bytes = st.pending_table_bytes
            rp.dec.SetMaxTableSize(st.acked_table_bytes)
        }
        sg.Unlock()
        return
    }
    ChargeControl(st, now) else ce {
        sg.Unlock()
        fail ce
    }
    old_init := st.peer_settings.initial_window
    st.peer_settings.Apply(entries, self.is_client) else ae {
        sg.Unlock()
        fail ae
    }
    new_init := st.peer_settings.initial_window
    if new_init != old_init {
        delta := new_init - old_init
        for i in 0..st.streams.slots.Len() {
            slot := mut st.streams.slots.ItemsMut()[i]
            if slot.state == StOpen || slot.state == StHalfRemote {
                slot.send_win.Adjust(delta) else fe {
                    sg.Unlock()
                    fail fe
                }
            }
        }
        self.WakeBlockedWriters(&*st, BlockWindow)
    }
    st.owed.QueueSettingsAck(st.peer_settings.table_bytes) else qe {
        sg.Unlock()
        fail qe
    }
    sg.Unlock()
    self.Core().wr_wake.NotifyOne()
}

Conn.HandleGoaway(&self, nc: Io, last: u32, code: u32) {
    a := self.heap.Alloc()
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    st.goaway_recvd = true
    if last < st.goaway_last {
        st.goaway_last = last
    }
    st.goaway_code = code
    if self.is_client {
        for i in 0..st.streams.slots.Len() {
            var close_it := false
            var recycle := false
            slot := mut st.streams.slots.ItemsMut()[i]
            if slot.state != StIdle && slot.state != StClosed && Unprocessed(slot.id, st.goaway_last) {
                slot.unprocessed = true
                slot.progress.Idle()
                close_it = true
                recycle = slot.refs == 0 && !slot.in_owed
            }
            if close_it {
                st.streams.CloseSlot(i)
                self.WakeStream(i)
            }
            if recycle {
                st.streams.ReleaseSlot(i, a)
            }
        }
        self.Party(PartyDrive).NotifyOne()
    }
    sg.Unlock()
}

CheckSlotWindowUpdate(st: mut State, idx: int, increment: u32, now: time.Instant): Violation!int {
    slot := mut st.streams.slots.ItemsMut()[idx]
    if slot.state == StClosed || slot.state == StHalfLocal {
        return 0
    }
    if slot.send_gate.Charge(int(increment)) {
        if !st.control_bucket.Take(now) {
            fail Violation(code: EnhanceYourCalm, stream: 0)
        }
    }
    slot.send_win.Grow(int(increment)) else {
        return 2
    }
    return 1
}

Conn.HandleWindowUpdate(&self, nc: Io, stream: u32, increment: u32, now: time.Instant): Error!void {
    a := self.heap.Alloc()
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    if stream == 0 {
        if increment == 0 {
            sg.Unlock()
            fail Violation(code: ProtocolError, stream: 0)
        }
        if st.conn_send_gate.Charge(int(increment)) {
            ChargeControl(st, now) else ce {
                sg.Unlock()
                fail ce
            }
        }
        st.conn_send_win.Grow(int(increment)) else fe {
            sg.Unlock()
            fail fe
        }
        self.WakeBlockedWriters(&*st, BlockWindow)
        sg.Unlock()
        return
    }
    if IsIdleStream(&*st, self.is_client, stream) {
        sg.Unlock()
        fail Violation(code: ProtocolError, stream: 0)
    }
    if self.is_client && HasU32(&st.reserved_remote, stream) {
        sg.Unlock()
        fail Violation(code: ProtocolError, stream: 0)
    }
    if increment == 0 {
        if st.reset_set.Has(stream) {
            sg.Unlock()
            return
        }
        widx := st.streams.Find(stream)
        if widx >= 0 {
            MarkStreamError(a, st, widx, ProtocolError, now) else me {
                sg.Unlock()
                fail me
            }
            self.WakeStream(widx)
        } else {
            ChargeReset(st, now) else ce {
                sg.Unlock()
                fail ce
            }
            st.owed.QueueReset(a, stream, ProtocolError) else oe {
                sg.Unlock()
                fail oe
            }
        }
        sg.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    if st.reset_set.Has(stream) {
        sg.Unlock()
        return
    }
    idx := st.streams.Find(stream)
    if idx < 0 {
        sg.Unlock()
        return
    }
    res := CheckSlotWindowUpdate(st, idx, increment, now) else ce {
        sg.Unlock()
        fail ce
    }
    if res == 2 {
        MarkStreamError(a, st, idx, FlowControlError, now) else me {
            sg.Unlock()
            fail me
        }
        self.WakeStream(idx)
        sg.Unlock()
        self.Core().wr_wake.NotifyOne()
        return
    }
    if res == 1 && st.streams.slots.Items()[idx].blocked_on == BlockWindow {
        self.WriterNotify(idx).NotifyOne()
    }
    sg.Unlock()
}

Conn.HandleRstStream(&self, nc: Io, stream: u32, code: u32, now: time.Instant): Error!void {
    a := self.heap.Alloc()
    var sg := self.Core().st.Lock(nc).Must()
    st := mut *sg.Mut()
    if IsIdleStream(&*st, self.is_client, stream) {
        sg.Unlock()
        fail Violation(code: ProtocolError, stream: 0)
    }
    _ = RemoveU32(mut st.reserved_remote, stream)
    ChargeReset(st, now) else ce {
        sg.Unlock()
        fail ce
    }
    idx := st.streams.Find(stream)
    if idx >= 0 {
        (recycle, admitted_n, admitted_ids) := ApplyRstStream(st, idx, code, self.is_client)
        if admitted_n > 0 {
            AdmitStreams(st, admitted_ids[0..admitted_n])
        }
        if recycle {
            st.streams.ReleaseSlot(idx, a)
        }
        self.WakeStream(idx)
    }
    sg.Unlock()
    self.Core().wr_wake.NotifyOne()
}

ApplyRstStream(st: mut State, idx: int, code: u32, is_client: bool): (recycle: bool, admitted_n: int, admitted_ids: [16]u32) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    sid := slot.id
    if is_client && code == NoError && slot.cli_head_ready && slot.cli_eof {
        slot.send_stopped = true
        slot.progress.Idle()
    } else {
        slot.reset = some(code)
        slot.progress.Idle()
    }
    st.streams.CloseSlot(idx)
    var unlink_owed := false
    slot2 := mut st.streams.slots.ItemsMut()[idx]
    var admitted_ids: [16]u32 = [0; 16]
    var admitted_n := 0
    if slot2.admit_reserve > 0 {
        if slot2.admitted {
            admitted_n = st.admission.Return(slot2.admit_reserve, mut admitted_ids[..])
        } else {
            st.admission.Cancel(sid)
        }
        slot2.admit_reserve = 0
        slot2.admitted = false
    }
    if !is_client && slot2.in_owed && slot2.owed_reset.IsNone() {
        slot2.owed_100 = false
        slot2.owed_credit = 0
        unlink_owed = true
    }
    if unlink_owed {
        UnlinkOwedStream(st, idx)
    }
    slot3 := &st.streams.slots.Items()[idx]
    return (recycle: slot3.refs == 0 && !slot3.in_owed, admitted_n: admitted_n, admitted_ids: admitted_ids)
}

CheckDataSlotInner(st: mut State, idx: int, data_len: int, wire_len: int, end: bool, is_client: bool, body_limit: int): (rc: int, gen_: u32) {
    // Returns rc: 0=ok, 2=err_stream_closed, 3=err_flow, 4=err_proto, 5=too_large_413, 6=append_cli, 7=append_cli_and_recycle.
    slot := mut st.streams.slots.ItemsMut()[idx]
    sgen := slot.gen_
    if slot.state != StOpen && slot.state != StHalfLocal {
        return (rc: 2, gen_: sgen)
    }
    slot.recv_win.Take(wire_len) else {
        return (rc: 3, gen_: sgen)
    }
    if is_client {
        slot.data_recvd += i64(data_len)
        if cl := slot.content_length {
            skip_cl := slot.head_method || slot.cli_head_status == 204 || slot.cli_head_status == 304
            if !skip_cl && (slot.data_recvd > cl || (end && slot.data_recvd != cl)) {
                slot.cli_body_malformed = true
                return (rc: 4, gen_: sgen)
            }
        }
        var eof_recycle := false
        if end {
            slot.cli_eof = true
            st.streams.EndRemote(idx)
            slot2 := &st.streams.slots.Items()[idx]
            eof_recycle = slot2.state == StClosed && slot2.refs == 0 && !slot2.in_owed
        }
        return (rc: if eof_recycle { 7 } else { 6 }, gen_: sgen)
    }
    slot.data_recvd += i64(data_len)
    slot.progress.Moved(data_len)
    if cl := slot.content_length {
        if slot.data_recvd > cl || (end && slot.data_recvd != cl) {
            return (rc: 4, gen_: sgen)
        }
    }
    if slot.data_recvd > i64(body_limit) {
        return (rc: 5, gen_: sgen)
    }
    if end {
        slot.state = StHalfRemote
        slot.progress.Idle()
        if slot.id > st.last_processed_id {
            st.last_processed_id = slot.id
        }
        slot.srv_dispatched = true
        slot.refs = 1
    }
    return (rc: 0, gen_: sgen)
}

CheckDataSlot(heap: &Heap, st: mut State, idx: int, data: []u8, wire_len: int, end: bool, is_client: bool, body_limit: int): OutOfMemory!(rc: int, gen_: u32) {
    // Returns: 0=ok, 1=client_eof_recycled, 2=err_stream_closed, 3=err_flow, 4=err_proto, 5=too_large_413.
    (rc, gen_: sgen) := CheckDataSlotInner(st, idx, data.Len(), wire_len, end, is_client, body_limit)
    if rc == 6 || rc == 7 {
        try st.streams.AppendClientData(heap, idx, data)
        if rc == 7 {
            return (rc: 1, gen_: sgen)
        }
        return (rc: 0, gen_: sgen)
    }
    return (rc: rc, gen_: sgen)
}

ReleaseAdmissionOn413(st: mut State, idx: int, stream: u32, end: bool): (admitted_n: int, admitted_ids: [16]u32) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    slot.progress.Idle()
    var admitted_ids: [16]u32 = [0; 16]
    var admitted_n := 0
    if slot.admit_reserve > 0 {
        if slot.admitted {
            admitted_n = st.admission.Return(slot.admit_reserve, mut admitted_ids[..])
        } else {
            st.admission.Cancel(stream)
        }
        slot.admit_reserve = 0
        slot.admitted = false
    }
    if !end {
        slot.owed_reset = some(NoError)
        st.reset_set.Add(stream)
    }
    st.streams.CloseSlot(idx)
    return (admitted_n: admitted_n, admitted_ids: admitted_ids)
}

Conn.HandleData(&self, nc: Io, stream: u32, data: []u8, padded: int, end: bool, now: time.Instant, accs: mut HeapList[ServerAcc], ready: mut HeapList[int]): Error!void {
    a := self.heap.Alloc()
    core_ := self.Core()
    wire_len := data.Len() + padded
    var wake_writer := false
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    st.conn_recv_win.Take(wire_len) else fe {
        sg.Unlock()
        fail fe
    }
    st.conn_recv_credit.Buffered(wire_len)
    inc := st.conn_recv_credit.Owed()
    if inc > 0 {
        st.owed.AddConnCredit(inc)
        st.conn_recv_win.Grow(inc) else {}
        wake_writer = true
    }
    if IsIdleStream(&*st, self.is_client, stream) {
        sg.Unlock()
        fail Violation(code: ProtocolError, stream: 0)
    }
    if self.is_client && HasU32(&st.reserved_remote, stream) {
        sg.Unlock()
        fail Violation(code: ProtocolError, stream: 0)
    }
    if data.Len() == 0 && !end {
        ChargeControl(st, now) else ce {
            sg.Unlock()
            fail ce
        }
    }
    if !self.is_client && st.owed.goaway_sent && stream > st.last_processed_id {
        sg.Unlock()
        if wake_writer {
            core_.wr_wake.NotifyOne()
        }
        return
    }
    if st.reset_set.Has(stream) {
        sg.Unlock()
        if wake_writer {
            core_.wr_wake.NotifyOne()
        }
        return
    }
    idx := st.streams.Find(stream)
    if idx < 0 {
        ChargeReset(st, now) else ce {
            sg.Unlock()
            fail ce
        }
        st.owed.QueueReset(a, stream, StreamClosed) else oe {
            sg.Unlock()
            fail oe
        }
        sg.Unlock()
        core_.wr_wake.NotifyOne()
        return
    }
    (rc, gen_: sgen) := CheckDataSlot(self.heap, st, idx, data, wire_len, end, self.is_client, self.lim.body_bytes) else oe {
        sg.Unlock()
        fail oe
    }
    if rc == 1 {
        st.streams.ReleaseSlot(idx, a)
        sg.Unlock()
        self.WakeStream(idx)
        return
    }
    if rc == 2 || rc == 3 || rc == 4 {
        ecode := if rc == 2 { StreamClosed } else if rc == 3 { FlowControlError } else { ProtocolError }
        MarkStreamError(a, st, idx, ecode, now) else me {
            sg.Unlock()
            fail me
        }
        sg.Unlock()
        if !self.is_client && idx < accs.Len() {
            ClearAcc(a, mut accs.ItemsMut()[idx])
        }
        core_.wr_wake.NotifyOne()
        self.WakeStream(idx)
        return
    }
    if rc == 5 {
        (admitted_n, admitted_ids) := ReleaseAdmissionOn413(st, idx, stream, end)
        if admitted_n > 0 {
            AdmitStreams(st, admitted_ids[0..admitted_n])
        }
        if !end {
            EnqueueOwedStream(st, idx)
        }
        sg.Unlock()
        if !self.is_client && idx < accs.Len() {
            ClearAcc(a, mut accs.ItemsMut()[idx])
        }
        self.SendInlineStatus(nc, stream, 413, true) else {}
        return
    }
    if self.is_client && padded > 0 {
        slot := mut st.streams.slots.ItemsMut()[idx]
        slot.recv_credit.Buffered(padded)
        sinc := slot.recv_credit.Owed()
        if sinc > 0 && slot.state != StClosed && slot.state != StHalfRemote {
            slot.owed_credit += sinc
            slot.recv_win.Grow(sinc) else {}
            EnqueueOwedStream(st, idx)
            wake_writer = true
        }
    }
    sg.Unlock()
    if wake_writer {
        core_.wr_wake.NotifyOne()
    }
    if self.is_client {
        self.Party(ReaderParty(idx)).NotifyOne()
        return
    }
    if idx < accs.Len() {
        acc := mut accs.ItemsMut()[idx]
        if acc.active && acc.gen_ == sgen {
            if data.Len() > 0 {
                if b := mut acc.body {
                    try b.Extend(a, data)
                } else {
                    var nb := HeapList[u8].New(a)
                    onerr nb.Free(a)
                    try nb.Extend(a, data)
                    match core.Replace(mut acc.body, some(nb)) {
                        some(old) { old.Free(a) }
                        none {}
                    }
                }
            }
            if end {
                acc.active = false
                acc.ready = true
                try ready.Push(a, idx)
            }
        }
    }
}

ClearAcc(a: &Alloc, acc: mut ServerAcc) {
    acc.active = false
    acc.ready = false
    if h := mut acc.head {
        h.Reset()
    }
    if b := mut acc.body {
        b.Clear()
    }
    _ = a
}
