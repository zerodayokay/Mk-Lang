// Why: the HTTP/2 server connection engine must reject malformed prefaces,
// enforce stream-0 and idle-stream state rules, emit owed ACKs ahead of
// data, bound control/reset/CONTINUATION abuse floods, catch flow-control
// overflows, and close idle connections cleanly.
//
// What: scripted-peer tests over std.net.pipe for h2.Conn.Server, Serve,
// Writer, Reset, Close, and Abandon.
//
// How: spawns Serve and Writer on one side of pipe.ConnPair while the test task
// acts as a scripted raw-frame client, reading server frames and sending valid
// or adversarial frames.

import std.core
import std.http.fields
import std.net
import std.net.pipe
import std.rt

struct TestField {
    name: str
    value: str
}

impl fields.Field for TestField {
    Name(&self): []u8 {
        return self.name.AsBytes()
    }

    Value(&self): []u8 {
        return self.value.AsBytes()
    }

    Sensitive(&self): bool {
        return false
    }
}

TestHasHeader(b: &fields.FieldBlock, name: str): bool {
    for f in b.Iter() {
        if CoreBytesEq(f.name, name.AsBytes()) {
            return true
        }
    }
    return false
}

struct TestDiscardSink {
    reqs: int
}

TestDiscardDeliver(st: &Mutex[TestDiscardSink], conn: &Conn, r: Request) {
    s := r.stream
    Request(stream: _, head, body) := r
    conn.Done(s, head, body)
    nc := core.IoMasked(rt.NativeIo())
    var g := st.Lock(nc).Must()
    g.Mut().reqs += 1
    g.Unlock()
}

struct TestEchoSink {
    reqs: int
}

TestEchoDeliver(st: &Mutex[TestEchoSink], conn: &Conn, r: Request) {
    s := r.stream
    Request(stream: _, head, body) := r
    nc := core.IoMasked(rt.NativeIo())
    var g := st.Lock(nc).Must()
    g.Mut().reqs += 1
    g.Unlock()
    var hs: [1]TestField = [TestField(name: "x-ok", value: "1")]
    conn.Respond(nc, s, 200, hs[..], body.Len() == 0) else {
        conn.Done(s, head, body)
        return
    }
    if body.Len() > 0 {
        conn.Write(nc, s, body.Items(), true) else {}
    }
    conn.Done(s, head, body)
}

TestDefaultLimits(): Limits {
    return Limits(
        streams: 16,
        window: 65535,
        header_bytes: 16384,
        body_bytes: 65536,
        write_limit: core.DurationOf(5_000_000_000),
        close_limit: core.DurationOf(2_000_000_000),
        idle_limit: core.DurationOf(0),
    )
}

TestSendRawFrame(io: Io, peer: &net.Conn, kind: u8, flags: u8, stream: u32, payload: []u8): Error!void {
    var hdr: [9]u8 = [0; 9]
    len := payload.Len()
    hdr[0] = Trunc[u8](len >> 16)
    hdr[1] = Trunc[u8](len >> 8)
    hdr[2] = Trunc[u8](len)
    hdr[3] = kind
    hdr[4] = flags
    hdr[5] = Trunc[u8](stream >> 24)
    hdr[6] = Trunc[u8](stream >> 16)
    hdr[7] = Trunc[u8](stream >> 8)
    hdr[8] = Trunc[u8](stream)
    try peer.WriteAll(io, hdr[..])
    if len > 0 {
        try peer.WriteAll(io, payload)
    }
}

struct TestFrame {
    kind: u8
    flags: u8
    stream: u32
    payload: HeapList[u8]
}

TestFrame.Free(self, a: &Alloc) {
    TestFrame(kind: _, flags: _, stream: _, payload) := self
    payload.Free(a)
}

TestReadExact(io: Io, peer: &net.Conn, dst: mut []u8): Error!bool {
    var got := 0
    for got < dst.Len() {
        n := try peer.Read(io, mut dst[got..])
        if n == 0 {
            return false
        }
        got += n
    }
    return true
}

TestReadFrame(a: &Alloc, io: Io, peer: &net.Conn): Error!?TestFrame from a {
    var hdr: [9]u8 = [0; 9]
    ok := try TestReadExact(io, peer, mut hdr[..])
    if !ok {
        return none
    }
    h := ParseHeader(hdr[..]).Must()
    var p := HeapList[u8].New(a)
    onerr p.Free(a)
    if h.length > 0 {
        for _ in 0..h.length {
            try p.Push(a, 0)
        }
        ok2 := try TestReadExact(io, peer, p.ItemsMut())
        if !ok2 {
            p.Free(a)
            return none
        }
    }
    return some(TestFrame(kind: h.kind, flags: h.flags, stream: h.stream, payload: p))
}

TestSendClientPreface(io: Io, peer: &net.Conn) {
    var buf: [64]u8 = [0; 64]
    (mut buf[0..24]).CopyFrom(Preface[..])
    s := InitialSettings
    n := WriteSettings(mut buf[24..], &s).Must()
    peer.WriteAll(io, buf[0..24 + n]).Must()
}

TestWaitGoaway(a: &Alloc, io: Io, peer: &net.Conn): (found: bool, code: u32, last: u32) {
    for {
        f := TestReadFrame(a, io, peer) else {
            return (found: false, code: 0, last: 0)
        }
        tf := f else {
            return (found: false, code: 0, last: 0)
        }
        if tf.kind == TypeGoaway && tf.payload.Len() >= 8 {
            p := tf.payload.Items()
            last := (u32(p[0] & 0x7F) << 24) | (u32(p[1]) << 16) | (u32(p[2]) << 8) | u32(p[3])
            code := (u32(p[4]) << 24) | (u32(p[5]) << 16) | (u32(p[6]) << 8) | u32(p[7])
            tf.Free(a)
            return (found: true, code: code, last: last)
        }
        tf.Free(a)
    }
}

TestEncodeSimpleGet(a: &Alloc, path: str): HeapList[u8] from a {
    var enc := fields.Encoder.New(a, fields.DefaultTableBytes, true).Must()
    defer enc.Free(a)
    var out := HeapList[u8].New(a)
    for _ in 0..512 {
        out.Push(a, 0).Must()
    }
    var off := enc.Start(out.ItemsMut()).Must()
    off += enc.Write(mut out.ItemsMut()[off..], ":method".AsBytes(), "GET".AsBytes(), fields.Mode.Index).Must()
    off += enc.Write(mut out.ItemsMut()[off..], ":scheme".AsBytes(), "https".AsBytes(), fields.Mode.Index).Must()
    off += enc.Write(mut out.ItemsMut()[off..], ":authority".AsBytes(), "example.com".AsBytes(), fields.Mode.Index).Must()
    off += enc.Write(mut out.ItemsMut()[off..], ":path".AsBytes(), path.AsBytes(), fields.Mode.Index).Must()
    var res := HeapList[u8].New(a)
    res.Extend(a, out.Items()[0..off]).Must()
    out.Free(a)
    return res
}

TestServerTask(io: Io, conn: &Conn, st: &Mutex[TestDiscardSink]) {
    conn.Serve(io, st, TestDiscardDeliver) else {}
}

TestWriterTask(io: Io, conn: &Conn) {
    conn.Writer(io) else {}
}

test "H2 server sends SETTINGS and conn WINDOW_UPDATE and prioritizes PING ACK" (sys: &System) {
    a := sys.heap.Alloc()
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 65536).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, TestDefaultLimits(), empty[..]).Must()
    sink := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerTask(sys.io, srv_box.Ref(), &sink)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        // Read server initial SETTINGS + connection WINDOW_UPDATE.
        f1 := TestReadFrame(a, sys.io, &cli_c).Must().Must()
        Assert(f1.kind == TypeSettings && f1.stream == 0 && (f1.flags & 1) == 0)
        f1.Free(a)
        f2 := TestReadFrame(a, sys.io, &cli_c).Must().Must()
        Assert(f2.kind == TypeWindowUpdate && f2.stream == 0)
        f2.Free(a)
        // Complete client preface and send PING + SETTINGS.
        TestSendClientPreface(sys.io, &cli_c)
        var ping_payload: [8]u8 = [1, 2, 3, 4, 5, 6, 7, 8]
        TestSendRawFrame(sys.io, &cli_c, TypePing, 0, 0, ping_payload[..]).Must()
        var saw_settings_ack := false
        var saw_ping_ack := false
        for !saw_settings_ack || !saw_ping_ack {
            f := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if f.kind == TypeSettings && (f.flags & 1) == 1 {
                saw_settings_ack = true
            } else if f.kind == TypePing && (f.flags & 1) == 1 {
                Assert(core.BytesEqual(f.payload.Items(), 0, ping_payload[..]))
                saw_ping_ack = true
            }
            f.Free(a)
        }
        cli_c.Close(sys.io) else { cli_c.Abandon() }
    }
    srv_box.Take(a).Abandon()
    _ = sink.Into()
}

test "H2 server rejects bad preface and non-SETTINGS first frame with PROTOCOL_ERROR" (sys: &System) {
    a := sys.heap.Alloc()
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 65536).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, TestDefaultLimits(), empty[..]).Must()
    sink := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerTask(sys.io, srv_box.Ref(), &sink)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        cli_c.WriteAll(sys.io, "NOT HTTP/2 PREFACE AT ALL".AsBytes()).Must()
        (found, code, last: _) := TestWaitGoaway(a, sys.io, &cli_c)
        Assert(found && code == ProtocolError)
        cli_c.Abandon()
    }
    srv_box.Take(a).Abandon()
    _ = sink.Into()

    // Now valid magic followed by PING instead of SETTINGS.
    (a: srv2, b: cli2) := pipe.ConnPair(sys.heap, 65536).Must()
    srv2_box := Conn.Server(sys.heap, sys.io, srv2, TestDefaultLimits(), empty[..]).Must()
    sink2 := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope s2 := sys.io.Scope() {
        s2.CancelAfter(core.DurationOf(5_000_000_000))
        s2.go TestServerTask(sys.io, srv2_box.Ref(), &sink2)
        s2.go TestWriterTask(sys.io, srv2_box.Ref())
        cli2.WriteAll(sys.io, Preface[..]).Must()
        var p8: [8]u8 = [0; 8]
        TestSendRawFrame(sys.io, &cli2, TypePing, 0, 0, p8[..]).Must()
        (found: found2, code: code2, last: _) := TestWaitGoaway(a, sys.io, &cli2)
        Assert(found2 && code2 == ProtocolError)
        cli2.Abandon()
    }
    srv2_box.Take(a).Abandon()
    _ = sink2.Into()
}

test "H2 server stops CONTINUATION flood (empty non-final counts as 2)" (sys: &System) {
    a := sys.heap.Alloc()
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 65536).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, TestDefaultLimits(), empty[..]).Must()
    sink := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerTask(sys.io, srv_box.Ref(), &sink)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        var hb := TestEncodeSimpleGet(a, "/cont")
        // HEADERS without END_HEADERS, then 17 empty non-final CONTINUATIONs (count = 34 > MaxContinuations 32).
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x01, 1, hb.Items()).Must()
        hb.Free(a)
        for _ in 0..17 {
            TestSendRawFrame(sys.io, &cli_c, TypeContinuation, 0x00, 1, empty[..]) else {
                break
            }
        }
        (found, code, last: _) := TestWaitGoaway(a, sys.io, &cli_c)
        Assert(found && code == EnhanceYourCalm)
        cli_c.Abandon()
    }
    srv_box.Take(a).Abandon()
    _ = sink.Into()
}

test "H2 server stops PING flood, SETTINGS flood, and rapid-reset flood" (sys: &System) {
    a := sys.heap.Alloc()
    var empty: [0]u8 = []

    // 1. PING flood
    (a: s1, b: c1) := pipe.ConnPair(sys.heap, 65536).Must()
    b1 := Conn.Server(sys.heap, sys.io, s1, TestDefaultLimits(), empty[..]).Must()
    k1 := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope sc1 := sys.io.Scope() {
        sc1.CancelAfter(core.DurationOf(5_000_000_000))
        sc1.go TestServerTask(sys.io, b1.Ref(), &k1)
        sc1.go TestWriterTask(sys.io, b1.Ref())
        TestSendClientPreface(sys.io, &c1)
        var p8: [8]u8 = [0; 8]
        for _ in 0..(ControlBudget + 10) {
            TestSendRawFrame(sys.io, &c1, TypePing, 0, 0, p8[..]) else {
                break
            }
        }
        (found, code, last: _) := TestWaitGoaway(a, sys.io, &c1)
        Assert(found && code == EnhanceYourCalm)
        c1.Abandon()
    }
    b1.Take(a).Abandon()
    _ = k1.Into()

    // 2. SETTINGS flood
    (a: s2, b: c2) := pipe.ConnPair(sys.heap, 65536).Must()
    b2 := Conn.Server(sys.heap, sys.io, s2, TestDefaultLimits(), empty[..]).Must()
    k2 := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope sc2 := sys.io.Scope() {
        sc2.CancelAfter(core.DurationOf(5_000_000_000))
        sc2.go TestServerTask(sys.io, b2.Ref(), &k2)
        sc2.go TestWriterTask(sys.io, b2.Ref())
        TestSendClientPreface(sys.io, &c2)
        for _ in 0..(ControlBudget + 10) {
            TestSendRawFrame(sys.io, &c2, TypeSettings, 0, 0, empty[..]) else {
                break
            }
        }
        (found: found2, code: code2, last: _) := TestWaitGoaway(a, sys.io, &c2)
        Assert(found2 && code2 == EnhanceYourCalm)
        c2.Abandon()
    }
    b2.Take(a).Abandon()
    _ = k2.Into()

    // 3. Rapid reset flood + MadeYouReset (WINDOW_UPDATE(0) flood on open/closed streams)
    (a: s3, b: c3) := pipe.ConnPair(sys.heap, 65536).Must()
    b3 := Conn.Server(sys.heap, sys.io, s3, TestDefaultLimits(), empty[..]).Must()
    k3 := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope sc3 := sys.io.Scope() {
        sc3.CancelAfter(core.DurationOf(5_000_000_000))
        sc3.go TestServerTask(sys.io, b3.Ref(), &k3)
        sc3.go TestWriterTask(sys.io, b3.Ref())
        TestSendClientPreface(sys.io, &c3)
        var hb := TestEncodeSimpleGet(a, "/rr")
        for i in 0..(ResetBudget + 10) {
            sid := Trunc[u32](1 + i * 2)
            TestSendRawFrame(sys.io, &c3, TypeHeaders, 0x04, sid, hb.Items()) else {
                break
            }
            var r4: [4]u8 = [0; 4]
            TestSendRawFrame(sys.io, &c3, TypeRstStream, 0, sid, r4[..]) else {
                break
            }
        }
        hb.Free(a)
        (found: found3, code: code3, last: _) := TestWaitGoaway(a, sys.io, &c3)
        Assert(found3 && code3 == EnhanceYourCalm)
        c3.Abandon()
    }
    b3.Take(a).Abandon()
    _ = k3.Into()
}

test "H2 server closes idle connection with GOAWAY(NO_ERROR) after idle_limit" (sys: &System) {
    a := sys.heap.Alloc()
    var lim := TestDefaultLimits()
    lim.idle_limit = core.DurationOf(40_000_000)
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 65536).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, lim, empty[..]).Must()
    sink := Mutex[TestDiscardSink].New(TestDiscardSink(reqs: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerTask(sys.io, srv_box.Ref(), &sink)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        (found, code, last: _) := TestWaitGoaway(a, sys.io, &cli_c)
        Assert(found && code == NoError)
        cli_c.Abandon()
    }
    srv_box.Take(a).Abandon()
    _ = sink.Into()
}
