// Why: HTTP/2 frames must be written without per-frame allocation, in strict
// wire order (HEADERS + CONTINUATIONs contiguous, stream ids monotonic, owed
// PING/SETTINGS ACKs ahead of queued DATA), and never interrupted mid-TLS-record
// by a caller's cancellation (H2).
//
// What: WriteState, WriteTurn, FlushWrites, EncodeStatusHeaders,
// EncodeRequestHeaders, and SendDataStream.
//
// How: WriteState keeps double buffers cur and out plus the HPACK Encoder;
// Turn swaps cur/out under wr and writes out under NoCancel bounded by
// write_limit or close_limit; FlushWrites releases wl while holding wr and
// st after verifying both cur and owed are empty so no bytes can be stranded.

import std.core
import std.http.fields
import std.net
import std.task
import std.time

const WriteFlushCap: int = 16384
const ShrinkCapThreshold: int = 65536

linear view struct WriteState {
    cur: HeapList[u8]
    enc: fields.Encoder
    hbuf: HeapList[u8]
    lower: HeapList[u8]
}

WriteState.New(a: &Alloc): OutOfMemory!WriteState {
    var enc := try fields.Encoder.New(a, fields.DefaultTableBytes, true)
    onerr enc.Free(a)
    return WriteState(
        cur: HeapList[u8].New(a),
        enc: enc,
        hbuf: HeapList[u8].New(a),
        lower: HeapList[u8].New(a),
    )
}

WriteState.Free(self, a: &Alloc) {
    WriteState(cur, enc, hbuf, lower) := self
    cur.Free(a)
    enc.Free(a)
    hbuf.Free(a)
    lower.Free(a)
}

EnsureLen(a: &Alloc, buf: mut HeapList[u8], need: int): OutOfMemory!void {
    for buf.Len() < need {
        try buf.Push(a, 0)
    }
}

AppendFrameHead(a: &Alloc, dst: mut HeapList[u8], length: int, kind: u8, flags: u8, stream: u32): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 9)
    PutFrameHead(mut dst.ItemsMut()[off..off + 9], length, kind, flags, stream)
}

AppendHeadersFrames(a: &Alloc, dst: mut HeapList[u8], stream: u32, block: []u8, max_frame: int, end_stream: bool): OutOfMemory!void {
    mf := if max_frame < 16384 { 16384 } else { max_frame }
    if block.Len() <= mf {
        var flags: u8 = FlagEndHeaders
        if end_stream {
            flags = flags | FlagEndStream
        }
        try AppendFrameHead(a, dst, block.Len(), TypeHeaders, flags, stream)
        try dst.Extend(a, block)
        return
    }
    var flags: u8 = if end_stream { FlagEndStream } else { u8(0) }
    try AppendFrameHead(a, dst, mf, TypeHeaders, flags, stream)
    try dst.Extend(a, block[0..mf])
    var off := mf
    for off < block.Len() {
        rem := block.Len() - off
        k := if rem > mf { mf } else { rem }
        last := off + k == block.Len()
        cflags := if last { FlagEndHeaders } else { u8(0) }
        try AppendFrameHead(a, dst, k, TypeContinuation, cflags, stream)
        try dst.Extend(a, block[off..off + k])
        off += k
    }
}

AppendFixedRst(a: &Alloc, dst: mut HeapList[u8], stream: u32, code: u32): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 13)
    _ = WriteRstStream(mut dst.ItemsMut()[off..off + 13], stream, code).Must()
}

AppendFixedWindowUpdate(a: &Alloc, dst: mut HeapList[u8], stream: u32, inc: u32): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 13)
    _ = WriteWindowUpdate(mut dst.ItemsMut()[off..off + 13], stream, inc).Must()
}

AppendFixedPing(a: &Alloc, dst: mut HeapList[u8], data: &[8]u8, ack: bool): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 17)
    _ = WritePing(mut dst.ItemsMut()[off..off + 17], data, ack).Must()
}

AppendFixedSettingsAck(a: &Alloc, dst: mut HeapList[u8]): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 9)
    _ = WriteSettingsAck(mut dst.ItemsMut()[off..off + 9]).Must()
}

AppendFixedGoaway(a: &Alloc, dst: mut HeapList[u8], last: u32, code: u32): OutOfMemory!void {
    off := dst.Len()
    try EnsureLen(a, dst, off + 17)
    _ = WriteGoaway(mut dst.ItemsMut()[off..off + 17], last, code).Must()
}

IsSensitiveName(lower_name: []u8): bool {
    return CoreBytesEq(lower_name, "authorization".AsBytes()) || CoreBytesEq(lower_name, "cookie".AsBytes())
}

CoreBytesEq(a: []u8, b: []u8): bool {
    return a.Len() == b.Len() && core.BytesEqual(a, 0, b)
}

// EncodeStatusBlock writes `:status <code>` into wr.hbuf using wr.enc.
EncodeStatusBlock(a: &Alloc, wr: mut WriteState, code: int): OutOfMemory!int {
    status_bytes: [3]u8 = [
        Trunc[u8](0x30 + (code / 100) % 10),
        Trunc[u8](0x30 + (code / 10) % 10),
        Trunc[u8](0x30 + code % 10),
    ]
    need := wr.enc.StartBound() + wr.enc.FieldBound(":status".AsBytes(), status_bytes[..])
    try EnsureLen(a, mut wr.hbuf, need)
    var o := wr.enc.Start(wr.hbuf.ItemsMut()).Must()
    o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":status".AsBytes(), status_bytes[..], fields.Mode.Index).Must()
    return o
}

// ValidateAndBoundResponse validates response fields, lowers names, checks
// decoded size <= max_list, and returns the exact HPACK upper bound.
ValidateResponseFields[F: fields.Field](a: &Alloc, wr: mut WriteState, status: int, list: []F, max_list: ?int): Error!int {
    if status < 100 || status > 999 || status == 101 {
        fail BadField
    }
    var list_size := fields.EntryOverhead + 7 + 3
    var bound := wr.enc.StartBound() + wr.enc.FieldBound(":status".AsBytes(), "200".AsBytes())
    for f in &list {
        n := f.Name()
        v := f.Value()
        if !fields.Sendable(n) {
            continue
        }
        try EnsureLen(a, mut wr.lower, n.Len())
        if !fields.LowerName(n, v, mut wr.lower.ItemsMut()[0..n.Len()]) {
            fail BadField
        }
        if n.Len() > 0 && n[0] == b':' {
            fail BadField
        }
        entry := fields.EntryOverhead + n.Len() + v.Len()
        if m := max_list {
            if entry > m - list_size {
                fail TooLarge
            }
        }
        list_size += entry
        bound += wr.enc.FieldBound(n, v)
    }
    return bound
}

// ValidateRequestFields validates request pseudo-headers and fields before
// allocating a stream id or mutating HPACK state.
ValidateRequestFields[F: fields.Field](a: &Alloc, wr: mut WriteState, pseudo: &fields.Pseudo, list: []F, max_list: ?int): Error!int {
    if pseudo.method.Len() == 0 || pseudo.scheme.Len() == 0 || pseudo.path.Len() == 0 {
        fail BadField
    }
    var tmp_name: [1]u8 = [0]
    for x in [pseudo.method.AsBytes(), pseudo.scheme.AsBytes(), pseudo.authority.AsBytes(), pseudo.path.AsBytes()] {
        if !fields.LowerName("a".AsBytes(), x, mut tmp_name[..]) {
            fail BadField
        }
    }
    var list_size := 0
    list_size += fields.EntryOverhead + 7 + pseudo.method.Len()
    list_size += fields.EntryOverhead + 7 + pseudo.scheme.Len()
    list_size += fields.EntryOverhead + 5 + pseudo.path.Len()
    if pseudo.authority.Len() > 0 {
        list_size += fields.EntryOverhead + 10 + pseudo.authority.Len()
    }
    if m := max_list {
        if list_size > m {
            fail TooLarge
        }
    }
    var bound := wr.enc.StartBound()
    bound += wr.enc.FieldBound(":method".AsBytes(), pseudo.method.AsBytes())
    bound += wr.enc.FieldBound(":scheme".AsBytes(), pseudo.scheme.AsBytes())
    bound += wr.enc.FieldBound(":path".AsBytes(), pseudo.path.AsBytes())
    if pseudo.authority.Len() > 0 {
        bound += wr.enc.FieldBound(":authority".AsBytes(), pseudo.authority.AsBytes())
    }
    for f in &list {
        n := f.Name()
        v := f.Value()
        if !fields.Sendable(n) {
            continue
        }
        try EnsureLen(a, mut wr.lower, n.Len())
        if !fields.LowerName(n, v, mut wr.lower.ItemsMut()[0..n.Len()]) {
            fail BadField
        }
        if n.Len() > 0 && n[0] == b':' {
            fail BadField
        }
        entry := fields.EntryOverhead + n.Len() + v.Len()
        if m := max_list {
            if entry > m - list_size {
                fail TooLarge
            }
        }
        list_size += entry
        bound += wr.enc.FieldBound(n, v)
    }
    return bound
}

// EncodeResponseBlock writes validated response headers into wr.hbuf.
EncodeResponseBlock[F: fields.Field](a: &Alloc, wr: mut WriteState, status: int, list: []F, bound: int): OutOfMemory!int {
    try EnsureLen(a, mut wr.hbuf, bound)
    status_bytes: [3]u8 = [
        Trunc[u8](0x30 + (status / 100) % 10),
        Trunc[u8](0x30 + (status / 10) % 10),
        Trunc[u8](0x30 + status % 10),
    ]
    var o := wr.enc.Start(wr.hbuf.ItemsMut()).Must()
    o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":status".AsBytes(), status_bytes[..], fields.Mode.Index).Must()
    for f in &list {
        n := f.Name()
        v := f.Value()
        if !fields.Sendable(n) {
            continue
        }
        _ = fields.LowerName(n, v, mut wr.lower.ItemsMut()[0..n.Len()])
        ln := wr.lower.Items()[0..n.Len()]
        mode := if f.Sensitive() || IsSensitiveName(ln) { fields.Mode.Never } else { fields.Mode.Index }
        o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ln, v, mode).Must()
    }
    return o
}

// EncodeRequestBlock writes validated request headers into wr.hbuf.
EncodeRequestBlock[F: fields.Field](a: &Alloc, wr: mut WriteState, pseudo: &fields.Pseudo, list: []F, bound: int): OutOfMemory!int {
    try EnsureLen(a, mut wr.hbuf, bound)
    var o := wr.enc.Start(wr.hbuf.ItemsMut()).Must()
    o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":method".AsBytes(), pseudo.method.AsBytes(), fields.Mode.Index).Must()
    o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":scheme".AsBytes(), pseudo.scheme.AsBytes(), fields.Mode.Index).Must()
    o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":path".AsBytes(), pseudo.path.AsBytes(), fields.Mode.Index).Must()
    if pseudo.authority.Len() > 0 {
        o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ":authority".AsBytes(), pseudo.authority.AsBytes(), fields.Mode.Index).Must()
    }
    for f in &list {
        n := f.Name()
        v := f.Value()
        if !fields.Sendable(n) {
            continue
        }
        _ = fields.LowerName(n, v, mut wr.lower.ItemsMut()[0..n.Len()])
        ln := wr.lower.Items()[0..n.Len()]
        mode := if f.Sensitive() || IsSensitiveName(ln) { fields.Mode.Never } else { fields.Mode.Index }
        o += wr.enc.Write(mut wr.hbuf.ItemsMut()[o..], ln, v, mode).Must()
    }
    return o
}

// EmitOwed writes all owed frames ahead of wr.cur into wout and clears wr.cur.
EmitOwed(a: &Alloc into st, wr: mut WriteState, st: mut State, wout: mut HeapList[u8]): OutOfMemory!void {
    wout.Clear()
    for {
        p := st.owed.PopPing() else {
            break
        }
        try AppendFixedPing(a, wout, &p, true)
    }
    for st.owed.conn_credit > 0 {
        grant := if st.owed.conn_credit > MaxWindow { MaxWindow } else { st.owed.conn_credit }
        st.owed.conn_credit -= grant
        try AppendFixedWindowUpdate(a, wout, 0, Trunc[u32](grant))
    }
    mf := st.peer_settings.max_frame
    var deferred_rst := HeapList[(sid: u32, code: u32)].New(a)
    onerr deferred_rst.Free(a)
    for st.owed.stream_head >= 0 {
        idx := st.owed.stream_head
        (sid, do_100, credit, rst, recycle) := PopOwedStreamItem(st, idx)
        if do_100 && rst.IsNone() && sid != 0 {
            blen := try EncodeStatusBlock(a, wr, 100)
            try AppendHeadersFrames(a, wout, sid, wr.hbuf.Items()[0..blen], mf, false)
        }
        if credit > 0 && sid != 0 {
            var rem := credit
            for rem > 0 {
                g := if rem > MaxWindow { MaxWindow } else { rem }
                rem -= g
                try AppendFixedWindowUpdate(a, wout, sid, Trunc[u32](g))
            }
        }
        if code := rst {
            if sid != 0 {
                try deferred_rst.Push(a, (sid: sid, code: code))
            }
        }
        if recycle {
            st.streams.ReleaseSlot(idx, a)
        }
    }
    if wr.cur.Len() > 0 {
        try wout.Extend(a, wr.cur.Items())
        wr.cur.Clear()
    }
    // The SETTINGS ACK goes after every frame already built: an ACK tells
    // the peer the new settings are in force (RFC 9113 6.5.3), and frames in
    // wr.cur were built under the old ones (a client's HEADERS opened under
    // the old MAX_CONCURRENT_STREAMS, blocks encoded at the old table size).
    for {
        sz := st.owed.PopSettingsAck() else {
            break
        }
        try AppendFixedSettingsAck(a, wout)
        wr.enc.SetMaxTableSize(sz)
    }
    for dr in deferred_rst.Items() {
        try AppendFixedRst(a, wout, dr.sid, dr.code)
    }
    deferred_rst.Free(a)
    for r in st.owed.resets.Items() {
        try AppendFixedRst(a, wout, r.stream, r.code)
    }
    st.owed.resets.Clear()
    if g := st.owed.goaway {
        try AppendFixedGoaway(a, wout, g.last, g.code)
        st.owed.goaway = none
        st.owed.goaway_sent = true
    }
}

PopOwedStreamItem(st: mut State, idx: int): (sid: u32, do_100: bool, credit: int, rst: ?u32, recycle: bool) {
    slot := mut st.streams.slots.ItemsMut()[idx]
    nx := slot.owed_next
    slot.owed_next = -1
    slot.owed_prev = -1
    slot.in_owed = false
    sid := slot.id
    do_100 := slot.owed_100
    slot.owed_100 = false
    var credit := slot.owed_credit
    slot.owed_credit = 0
    rst := slot.owed_reset
    slot.owed_reset = none
    recycle := slot.state == StClosed && slot.refs == 0
    if rst.IsSome() || slot.state == StClosed || slot.state == StHalfRemote {
        credit = 0
    }
    st.owed.stream_head = nx
    if nx >= 0 {
        st.streams.slots.ItemsMut()[nx].owed_prev = -1
    } else {
        st.owed.stream_tail = -1
    }
    return (sid: sid, do_100: do_100, credit: credit, rst: rst, recycle: recycle)
}

// WriteTurnLocked drains st.owed and wr.cur into wout while caller holds wl,
// then performs one bounded socket WriteAll without holding wr or st.
Conn.WriteTurnLocked(&self, io: Io, close_mode: bool): Error!bool {
    nc := io.NoCancel()
    a := self.heap.Alloc()
    core_ := self.Core()
    var limit := self.lim.write_limit
    var should_shutdown := false
    var wog := core_.wout.Lock(nc).Must()
    wout := mut *wog.Mut()
    var wg := core_.wr.Lock(nc).Must()
    wr := mut *wg.Mut()
    var sg := core_.st.Lock(nc).Must()
    st := mut *sg.Mut()
    EmitOwed(a, wr, st, wout) else e {
        sg.Unlock()
        wg.Unlock()
        wout.Clear()
        wog.Unlock()
        self.FailConn(nc, e, none, true)
        fail e
    }
    if wout.Len() == 0 {
        should_shutdown = st.fault.IsSome()
        sg.Unlock()
        wg.Unlock()
        wog.Unlock()
        if should_shutdown {
            self.NetConn().Shutdown(nc, net.Shutdown.Both) else {}
        }
        return false
    }
    if close_mode || st.fault.IsSome() || st.owed.goaway_sent {
        limit = self.lim.close_limit
    }
    should_shutdown = st.fault.IsSome()
    sg.Unlock()
    wg.Unlock()
    var write_err: ?Error = none
    var expired := false
    wio := if close_mode { io } else { nc }
    scope s := wio.Scope() {
        s.CancelAfter(limit)
        self.NetConn().WriteAll(wio, wout.Items()) else e {
            write_err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
        }
    }
    wout.Clear()
    wog.Unlock()
    if e := write_err {
        var err: Error = e
        if expired {
            err = time.TimedOut
        }
        self.FailConn(nc, err, none, true)
        fail err
    }
    if should_shutdown {
        self.NetConn().Shutdown(nc, net.Shutdown.Both) else {}
    }
    core_.wr_space_wake.NotifyAll()
    self.WakeAll()
    return true
}

// FlushWrites acquires wl if free and drains wr.cur and st.owed until both
// are empty while holding wr and st, then releases wl inside that lock so no
// concurrent appender can strand bytes.
Conn.FlushWrites(&self, io: Io): Error!void {
    core_ := self.Core()
    if !core_.wl.TryAcquire(1) {
        return
    }
    nc := io.NoCancel()
    for {
        var done := false
        var wg := core_.wr.Lock(nc).Must()
        var sg := core_.st.Lock(nc).Must()
        if wg.Ref().cur.Len() == 0 && sg.Ref().owed.Empty() {
            core_.wl.Release(1)
            done = true
        }
        sg.Unlock()
        wg.Unlock()
        if done {
            core_.wr_space_wake.NotifyAll()
            self.WakeAll()
            return
        }
        _ = self.WriteTurnLocked(io, false) else e {
            core_.wl.Release(1)
            core_.wr_space_wake.NotifyAll()
            self.WakeAll()
            fail e
        }
    }
}
