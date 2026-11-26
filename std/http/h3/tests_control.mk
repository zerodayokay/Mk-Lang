// Why: every HTTP/3 control-stream, SETTINGS, GOAWAY, CANCEL_PUSH,
// MAX_PUSH_ID and excessive-load rule of RFC 9114 5.2, 6.2.1, 7.2 and 10.5
// must be pinned by positive and negative tests over bounded Mux streams.
//
// What: test blocks covering H3-6, H3-7, H3-35, H3-47, H3-48, H3-58..H3-61,
// H3-64, H3-65, H3-67..H3-69, H3-71..H3-74, H3-76, H3-79, H3-80, and H3-96.
//
// How: opens raw unidirectional streams on std.net.pipe's MuxPair alongside
// real Session.Client / Session.Server instances and verifies exact Violation
// codes, Settings values, and GOAWAY state.

import hf = std.http.fields
import std.net.pipe

// Helper: assert Session.Poll fails with connection Violation(want_code, -1).
AssertPollFatal(s: &Session, io: Io, want_code: u64) {
    var got: u64 = 0
    s.Poll(io) else e {
        match e {
            Violation(code, stream) {
                Assert(stream == -1)
                got = code
            }
            _ { Assert(false) }
        }
    }
    Assert(got == want_code)
}

// Helper: minimal Header impl for Exchange.Open / Exchange.Respond tests.
struct Kv {
    n: str
    v: str
}

impl hf.Field for Kv {
    Name(&self): []u8 { return self.n.AsBytes() }
    Value(&self): []u8 { return self.v.AsBytes() }
    Sensitive(&self): bool { return false }
}

test "RFC 9114 6.2.1: our control stream is opened once and starts with SETTINGS" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    cs := try Session.Client(sys.heap, sys.io, &cm, 2048)
    defer cs.Free(sys.heap.Alloc())

    // Exactly one uni stream was opened by client, starting with 0x00 and SETTINGS.
    u := (try sm.Accept(sys.io, true, false)).Must()
    defer sm.Release(u)
    if extra := try sm.Accept(sys.io, true, false) {
        sm.Release(extra)
        Assert(false)
    }

    var raw: [64]u8 = [0; 64]
    n := (try sm.Read(sys.io, &u, mut raw[..], false)).Must()
    Assert(n >= 2)
    stype := ReadVarint(raw[0..n], 0).Must()
    Assert(stype.v == StreamControl)
    fh := ParseFrameHeader(raw[stype.n..n]).Must()
    Assert(fh.kind == FrameSettings)
    var st := InitialSettings
    try st.Apply(sys.heap.Alloc(), raw[stype.n + fh.header_len..stype.n + fh.header_len + Cast[int](fh.length)])
    Assert(st.max_field_section_size.IsSome() && st.max_field_section_size.Must() == 2048)
}

test "RFC 9114 7.2.4.2: SETTINGS is sent before any peer data is received" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    // Server created alone, before client sends a single byte.
    ss := try Session.Server(sys.heap, sys.io, &sm, 1024)
    defer ss.Free(sys.heap.Alloc())

    u := (try cm.Accept(sys.io, true, false)).Must()
    defer cm.Release(u)
    var raw: [64]u8 = [0; 64]
    n := (try cm.Read(sys.io, &u, mut raw[..], false)).Must()
    Assert(n > 0 && raw[0] == 0x00)
    fh := ParseFrameHeader(raw[1..n]).Must()
    Assert(fh.kind == FrameSettings)
}

test "RFC 9114 4.2.2: SETTINGS carries MAX_FIELD_SECTION_SIZE and the peer's is honoured on send" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 96)
    defer ss.Free(sys.heap.Alloc())

    try cs.Ref().Poll(sys.io)
    Assert(cs.Ref().PeerSettings().max_field_section_size.Must() == 96)

    // Small request fits in 96 bytes? ":method GET" (43) + ":scheme https" (44) + ":path /" (38) > 96,
    // so even a basic request exceeds 96 -> fails locally with SendTooLarge before opening stream!
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "ex", path: "/")
    var empty: [0]Kv = []
    var too_large := false
    match Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true) {
        ok(ex) {
            ex.Release(cs.Ref())
            Assert(false)
        }
        err(e) {
            match e {
                SendTooLarge { too_large = true }
                _ { Assert(false) }
            }
        }
    }
    Assert(too_large)
}

test "RFC 9114 7.2.4.2: requests sent before the peer's SETTINGS use default settings" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()

    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    // Server has NOT been created/sent SETTINGS yet; client default max_field_section_size is unlimited (none).
    Assert(cs.Ref().PeerSettings().max_field_section_size.IsNone())
    p := hf.Pseudo(method: "GET", scheme: "https", authority: "example.com", path: "/early")
    var empty: [0]Kv = []
    ex := try Exchange.Open(cs.Ref(), sys.io, &p, empty[..], true)
    ex.Release(cs.Ref())
}

test "RFC 9114 7.2.6: a GOAWAY id that is not a client bidi stream id is a connection H3_ID_ERROR" (sys: &System) {
    for bad_id in [u64(1), u64(2), u64(3), u64(5), u64(6)] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        ctrl := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(ctrl)
        var buf: [32]u8 = [0; 32]
        var o := WriteVarint(mut buf[..], StreamControl)
        o += EncodeSettings(mut buf[o..], none, 0, 0, false)
        o += WriteSingleIdFrame(mut buf[o..], FrameGoaway, bad_id)
        try sm.Write(sys.io, &ctrl, buf[0..o], false)

        AssertPollFatal(cs.Ref(), sys.io, H3IdError)
    }
}

test "RFC 9114 5.2: a GOAWAY with a larger id than the previous one is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    try ss.Ref().SendGoaway(sys.io, 8)
    try cs.Ref().Poll(sys.io)
    Assert(cs.Ref().Goaway().Must() == 8)

    // Lowering GOAWAY id to 4 succeeds.
    try ss.Ref().SendGoaway(sys.io, 4)
    try cs.Ref().Poll(sys.io)
    Assert(cs.Ref().Goaway().Must() == 4)

    // Server SendGoaway itself refuses raising id from 4 to 8.
    var send_err: u64 = 0
    ss.Ref().SendGoaway(sys.io, 8) else e {
        match e {
            Violation(code, stream: _) { send_err = code }
            _ { Assert(false) }
        }
    }
    Assert(send_err == H3IdError)

    // Raw larger GOAWAY on another connection triggers H3_ID_ERROR on client Poll.
    (client: cm2, server: sm2) := try pipe.MuxPair(sys.heap, 4096)
    defer cm2.Abandon()
    defer sm2.Abandon()
    cs2 := try Session.Client(sys.heap, sys.io, &cm2, 4096)
    defer cs2.Free(sys.heap.Alloc())
    ctrl2 := (try sm2.Open(sys.io, true, true)).Must()
    defer sm2.Release(ctrl2)
    var buf: [32]u8 = [0; 32]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += WriteSingleIdFrame(mut buf[o..], FrameGoaway, 4)
    o += WriteSingleIdFrame(mut buf[o..], FrameGoaway, 8)
    try sm2.Write(sys.io, &ctrl2, buf[0..o], false)
    AssertPollFatal(cs2.Ref(), sys.io, H3IdError)
}

test "RFC 9114 6.2.1: a control stream whose first frame is not SETTINGS is a connection H3_MISSING_SETTINGS" (sys: &System) {
    for first_kind in [FrameGoaway, u64(0x21)] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        ctrl := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(ctrl)
        var buf: [16]u8 = [0; 16]
        var o := WriteVarint(mut buf[..], StreamControl)
        o += WriteSingleIdFrame(mut buf[o..], first_kind, 0)
        try sm.Write(sys.io, &ctrl, buf[0..o], false)

        AssertPollFatal(cs.Ref(), sys.io, H3MissingSettings)
    }
}

test "RFC 9114 6.2.1: a second control stream is a connection H3_STREAM_CREATION_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    c1 := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(c1)
    var b1: [16]u8 = [0; 16]
    var o1 := WriteVarint(mut b1[..], StreamControl)
    o1 += EncodeSettings(mut b1[o1..], none, 0, 0, false)
    try sm.Write(sys.io, &c1, b1[0..o1], false)
    try cs.Ref().Poll(sys.io)

    c2 := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(c2)
    var b2: [8]u8 = [0; 8]
    o2 := WriteVarint(mut b2[..], StreamControl)
    try sm.Write(sys.io, &c2, b2[0..o2], false)
    AssertPollFatal(cs.Ref(), sys.io, H3StreamCreationError)
}

test "RFC 9114 6.2.1: we never close our control stream or stop the peer's" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    try cs.Ref().Poll(sys.io)
    try ss.Ref().Poll(sys.io)

    // Writing an extra GREASE frame to our peer control streams must succeed (neither closed nor stopped).
    try ss.Ref().SendGoaway(sys.io, 0)
    try cs.Ref().Poll(sys.io)
    Assert(cs.Ref().Goaway().Must() == 0)
}

test "RFC 9114 6.2.1: FIN or RESET_STREAM on the peer's control stream is a connection H3_CLOSED_CRITICAL_STREAM" (sys: &System) {
    // Clean FIN after SETTINGS.
    (client: cm1, server: sm1) := try pipe.MuxPair(sys.heap, 4096)
    defer cm1.Abandon()
    defer sm1.Abandon()
    cs1 := try Session.Client(sys.heap, sys.io, &cm1, 4096)
    defer cs1.Free(sys.heap.Alloc())
    c1 := (try sm1.Open(sys.io, true, true)).Must()
    defer sm1.Release(c1)
    var b1: [16]u8 = [0; 16]
    var o1 := WriteVarint(mut b1[..], StreamControl)
    o1 += EncodeSettings(mut b1[o1..], none, 0, 0, false)
    try sm1.Write(sys.io, &c1, b1[0..o1], true)
    AssertPollFatal(cs1.Ref(), sys.io, H3ClosedCriticalStream)

    // RESET_STREAM on peer's control stream.
    (client: cm2, server: sm2) := try pipe.MuxPair(sys.heap, 4096)
    defer cm2.Abandon()
    defer sm2.Abandon()
    cs2 := try Session.Client(sys.heap, sys.io, &cm2, 4096)
    defer cs2.Free(sys.heap.Alloc())
    c2 := (try sm2.Open(sys.io, true, true)).Must()
    defer sm2.Release(c2)
    var b2: [16]u8 = [0; 16]
    var o2 := WriteVarint(mut b2[..], StreamControl)
    o2 += EncodeSettings(mut b2[o2..], none, 0, 0, false)
    try sm2.Write(sys.io, &c2, b2[0..o2], false)
    try cs2.Ref().Poll(sys.io)
    sm2.Reset(&c2, H3NoError)
    AssertPollFatal(cs2.Ref(), sys.io, H3ClosedCriticalStream)
}

test "RFC 9114 7.2: DATA, HEADERS or PUSH_PROMISE on the control stream is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    for bad_frame in [FrameData, FrameHeaders, FramePushPromise] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        ctrl := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(ctrl)
        var buf: [16]u8 = [0; 16]
        var o := WriteVarint(mut buf[..], StreamControl)
        o += EncodeSettings(mut buf[o..], none, 0, 0, false)
        o += WriteFrameHeader(mut buf[o..], bad_frame, 0)
        try sm.Write(sys.io, &ctrl, buf[0..o], false)
        AssertPollFatal(cs.Ref(), sys.io, H3FrameUnexpected)
    }
}

test "RFC 9114 7.2.3: CANCEL_PUSH on the control stream of a client that sent no MAX_PUSH_ID is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    ctrl := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(ctrl)
    var buf: [16]u8 = [0; 16]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += WriteSingleIdFrame(mut buf[o..], FrameCancelPush, 0)
    try sm.Write(sys.io, &ctrl, buf[0..o], false)
    AssertPollFatal(cs.Ref(), sys.io, H3IdError)
}

test "RFC 9114 7.2.3: CANCEL_PUSH on the control stream at our server is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    ctrl := (try cm.Open(sys.io, true, true)).Must()
    defer cm.Release(ctrl)
    var buf: [16]u8 = [0; 16]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += WriteSingleIdFrame(mut buf[o..], FrameCancelPush, 0)
    try cm.Write(sys.io, &ctrl, buf[0..o], false)
    AssertPollFatal(ss.Ref(), sys.io, H3IdError)
}

test "RFC 9114 7.2.4: a second SETTINGS is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    ctrl := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(ctrl)
    var buf: [24]u8 = [0; 24]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    try sm.Write(sys.io, &ctrl, buf[0..o], false)
    AssertPollFatal(cs.Ref(), sys.io, H3FrameUnexpected)
}

test "RFC 9114 7.2.4: a duplicate setting identifier is a connection H3_SETTINGS_ERROR" (sys: &System) {
    // Duplicate known setting (0x06) and duplicate GREASE setting (0x21).
    var p1: [4]u8 = [0x06, 10, 0x06, 20]
    var p2: [4]u8 = [0x21, 1, 0x21, 2]
    for p in [p1[..], p2[..]] {
        var st := InitialSettings
        var code: u64 = 0
        st.Apply(sys.heap.Alloc(), p) else e {
            match e {
                Violation(code: c, stream: _) { code = c }
                _ { Assert(false) }
            }
        }
        Assert(code == H3SettingsError)
    }
}

test "RFC 9114 7.2.4: unknown and reserved setting identifiers are ignored" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    ctrl := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(ctrl)
    var buf: [32]u8 = [0; 32]
    var o := WriteVarint(mut buf[..], StreamControl)
    // SETTINGS: GREASE 0x21=7, unknown 0x99=42, MAX_FIELD_SECTION_SIZE 0x06=512, GREASE 0x40=99.
    var payload: [16]u8 = [0; 16]
    var po := WriteVarint(mut payload[..], 0x21)
    po += WriteVarint(mut payload[po..], 7)
    po += WriteVarint(mut payload[po..], 0x99)
    po += WriteVarint(mut payload[po..], 42)
    po += WriteVarint(mut payload[po..], SettingMaxFieldSectionSize)
    po += WriteVarint(mut payload[po..], 512)
    po += WriteVarint(mut payload[po..], 0x40)
    po += WriteVarint(mut payload[po..], 99)
    o += WriteFrameHeader(mut buf[o..], FrameSettings, Cast[u64](po))
    (mut buf[o..o + po]).CopyFrom(payload[0..po])
    o += po
    try sm.Write(sys.io, &ctrl, buf[0..o], false)

    try cs.Ref().Poll(sys.io)
    Assert(cs.Ref().PeerSettings().max_field_section_size.Must() == 512)
}

test "RFC 9114 7.2.4.1: an HTTP/2 setting identifier is a connection H3_SETTINGS_ERROR" (sys: &System) {
    for bad_id in [u64(0x02), u64(0x03), u64(0x04), u64(0x05)] {
        (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
        defer cm.Abandon()
        defer sm.Abandon()
        cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
        defer cs.Free(sys.heap.Alloc())

        ctrl := (try sm.Open(sys.io, true, true)).Must()
        defer sm.Release(ctrl)
        var buf: [16]u8 = [0; 16]
        var o := WriteVarint(mut buf[..], StreamControl)
        o += WriteFrameHeader(mut buf[o..], FrameSettings, 2)
        buf[o] = Trunc[u8](bad_id)
        buf[o + 1] = 0
        o += 2
        try sm.Write(sys.io, &ctrl, buf[0..o], false)
        AssertPollFatal(cs.Ref(), sys.io, H3SettingsError)
    }
}

test "RFC 9114 7.2.7: MAX_PUSH_ID at a client is a connection H3_FRAME_UNEXPECTED" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    ctrl := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(ctrl)
    var buf: [16]u8 = [0; 16]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += WriteSingleIdFrame(mut buf[o..], FrameMaxPushId, 1)
    try sm.Write(sys.io, &ctrl, buf[0..o], false)
    AssertPollFatal(cs.Ref(), sys.io, H3FrameUnexpected)
}

test "RFC 9114 7.2.7: a smaller MAX_PUSH_ID than before is a connection H3_ID_ERROR" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    ss := try Session.Server(sys.heap, sys.io, &sm, 4096)
    defer ss.Free(sys.heap.Alloc())

    ctrl := (try cm.Open(sys.io, true, true)).Must()
    defer cm.Release(ctrl)
    var buf: [24]u8 = [0; 24]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += EncodeSettings(mut buf[o..], none, 0, 0, false)
    o += WriteSingleIdFrame(mut buf[o..], FrameMaxPushId, 10)
    o += WriteSingleIdFrame(mut buf[o..], FrameMaxPushId, 12)
    try cm.Write(sys.io, &ctrl, buf[0..o], false)
    try ss.Ref().Poll(sys.io)

    var b2: [8]u8 = [0; 8]
    n2 := WriteSingleIdFrame(mut b2[..], FrameMaxPushId, 8)
    try cm.Write(sys.io, &ctrl, b2[0..n2], false)
    AssertPollFatal(ss.Ref(), sys.io, H3IdError)
}

test "RFC 9114 10.5: a control frame over its size bound is a connection H3_EXCESSIVE_LOAD" (sys: &System) {
    (client: cm, server: sm) := try pipe.MuxPair(sys.heap, 4096)
    defer cm.Abandon()
    defer sm.Abandon()
    cs := try Session.Client(sys.heap, sys.io, &cm, 4096)
    defer cs.Free(sys.heap.Alloc())

    ctrl := (try sm.Open(sys.io, true, true)).Must()
    defer sm.Release(ctrl)
    var buf: [16]u8 = [0; 16]
    var o := WriteVarint(mut buf[..], StreamControl)
    o += WriteFrameHeader(mut buf[o..], FrameSettings, Cast[u64](MaxControlFrameBytes + 1))
    try sm.Write(sys.io, &ctrl, buf[0..o], false)
    AssertPollFatal(cs.Ref(), sys.io, H3ExcessiveLoad)
}
