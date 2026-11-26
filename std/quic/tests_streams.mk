// Why: QUIC stream multiplexing must enforce stream-ID bit layout, sequential
// opening, state machines for both halves, reassembly with the 32-gap cap,
// retransmission fidelity, and role/direction frame legality.
//
// What: unit tests for QT-44..QT-49, QT-51..QT-53, QT-59, QT-61..QT-63,
// QT-65..QT-67, QT-69, QT-71, QT-74..QT-78, and QT-82.
//
// How: drives Streams directly and decodes frames emitted by Next using
// T23ReadVarint to verify both state transitions and wire output.

T3DummyFrame(): SentFrame {
    return SentFrame(kind: SentKind.Control(index: 0))
}

T3Eq(a: []u8, b: []u8): bool {
    if a.Len() != b.Len() {
        return false
    }
    for i in 0..a.Len() {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

T3PeerLimits(): StreamLimits {
    var lim := StreamLimits.Default(true)
    lim.peer_max_data = 1_000_000
    lim.peer_max_stream_data_bidi_local = 65_536
    lim.peer_max_stream_data_bidi_remote = 65_536
    lim.peer_max_stream_data_uni = 65_536
    lim.peer_max_streams_bidi = 10
    lim.peer_max_streams_uni = 10
    return lim
}

T3Code(e: Error): u64 {
    return match e {
        TransportError(code, frame: _) { code }
        _ { 9999 }
    }
}

test "RFC 9000 2.1: stream ids carry initiator and direction bits and rise by 4 per type" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)

    Assert((try cli.Open(h, false)).Must() == 0)
    Assert((try cli.Open(h, false)).Must() == 4)
    Assert((try cli.Open(h, true)).Must() == 2)
    Assert((try cli.Open(h, true)).Must() == 6)

    Assert((try srv.Open(h, false)).Must() == 1)
    Assert((try srv.Open(h, false)).Must() == 5)
    Assert((try srv.Open(h, true)).Must() == 3)
    Assert((try srv.Open(h, true)).Must() == 7)
}

test "RFC 9000 3.2: a peer stream id opens all lower ids of its type" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    // Client opens bidi stream 8 -> opens 0, 4, 8 in order.
    try srv.OnStream(h, 8, 0, "hi".AsBytes(), true)
    Assert(srv.Accept(false).Must() == 0)
    Assert(srv.Accept(false).Must() == 4)
    Assert(srv.Accept(false).Must() == 8)
    Assert(srv.Accept(false).IsNone())
}

test "RFC 9000 3.2: MAX_STREAM_DATA or STOP_SENDING opens a peer bidirectional stream" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnMaxStreamData(h, 4, 4096)
    Assert(srv.Accept(false).Must() == 0)
    Assert(srv.Accept(false).Must() == 4)
    try srv.OnStop(h, 8, 77)
    Assert(srv.Accept(false).Must() == 8)
}

test "RFC 9000 4.6: a peer stream id past the advertised limit is STREAM_LIMIT_ERROR" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := T3PeerLimits()
    lim.max_streams_bidi = 2
    lim.max_streams_uni = 1
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    // Peer bidi limit is 2 (ids 0, 4 allowed; id 8 exceeds limit).
    try srv.OnStream(h, 4, 0, "ok".AsBytes(), false)
    var err1: u64 = 0
    srv.OnStream(h, 8, 0, "no".AsBytes(), false) else e { err1 = T3Code(e) }
    Assert(err1 == ErrStreamLimit)
    // Peer uni limit is 1 (id 2 allowed; id 6 exceeds limit).
    var err2: u64 = 0
    srv.OnStream(h, 6, 0, "no".AsBytes(), false) else e { err2 = T3Code(e) }
    Assert(err2 == ErrStreamLimit)
}

test "RFC 9000 19.11: opening past the peer's stream limit waits and sends STREAMS_BLOCKED" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := T3PeerLimits()
    lim.peer_max_streams_bidi = 1
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    Assert((try cli.Open(h, false)).Must() == 0)
    Assert((try cli.Open(h, false)).IsNone())

    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [T3DummyFrame(); 4]
    r := cli.Next(h, mut out[..], 64, mut fs[..])
    Assert(r.frames == 1)
    ft := T23ReadVarint(out[0..r.n], 0).Must()
    lim_v := T23ReadVarint(out[0..r.n], ft.next).Must()
    Assert(ft.v == 0x16 && lim_v.v == 1)
}

test "RFC 9000 4.6: MAX_STREAMS credit returns only when a stream's record is freed" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := T3PeerLimits()
    lim.max_streams_bidi = 1
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    // Client opens bidi stream 0 with FIN.
    try srv.OnStream(h, 0, 0, "req".AsBytes(), true)
    _ = srv.Accept(false).Must()
    var buf: [16]u8 = [0; 16]
    Assert((try srv.Read(h, 0, mut buf[..])).Must() == 3)
    Assert((try srv.Read(h, 0, mut buf[..])).Must() == 0)
    // Send half writes FIN and is ACKed -> both halves terminal, but NOT released yet!
    _ = try srv.Write(h, 0, "resp".AsBytes(), true)
    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [T3DummyFrame(); 4]
    r1 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r1.frames == 1)
    srv.OnAcked(h, &fs[0])
    // Still not released -> no MAX_STREAMS yet, and stream 4 still refused.
    r2 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r2.frames == 0)
    var err_lim: u64 = 0
    srv.OnStream(h, 4, 0, "early".AsBytes(), true) else e { err_lim = T3Code(e) }
    Assert(err_lim == ErrStreamLimit)

    // Release frees record and triggers MAX_STREAMS(bidi, 2).
    srv.Release(h, 0)
    r3 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r3.frames == 1)
    ft := T23ReadVarint(out[0..r3.n], 0).Must()
    mv := T23ReadVarint(out[0..r3.n], ft.next).Must()
    Assert(ft.v == 0x12 && mv.v == 2)
    try srv.OnStream(h, 4, 0, "now".AsBytes(), true)
}

test "RFC 9000 2.2: out-of-order and repeated stream data reassemble in order" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnStream(h, 2, 6, "world".AsBytes(), true)
    var buf: [32]u8 = [0; 32]
    Assert((try srv.Read(h, 2, mut buf[..])).IsNone())
    // Overlapping + repeated data at offset 0.
    try srv.OnStream(h, 2, 0, "hello ".AsBytes(), false)
    try srv.OnStream(h, 2, 2, "llo".AsBytes(), false)
    n := (try srv.Read(h, 2, mut buf[..])).Must()
    Assert(n == 11 && T3Eq(buf[0..n], "hello world".AsBytes()))
    Assert((try srv.Read(h, 2, mut buf[..])).Must() == 0)

    // 32-gap cap (QT-52, QT-82): 33rd gap fails with GapLimit and leaves state untouched.
    try srv.OnStream(h, 6, 0, "a".AsBytes(), false)
    for g in 0..32 {
        try srv.OnStream(h, 6, Cast[u64](2 + g * 2), "x".AsBytes(), false)
    }
    var hit_gap := false
    srv.OnStream(h, 6, 100, "z".AsBytes(), false) else e {
        match e {
            GapLimit { hit_gap = true }
            _ { Assert(false) }
        }
    }
    Assert(hit_gap)
    // Filling gap 1 still delivers cleanly.
    try srv.OnStream(h, 6, 1, "b".AsBytes(), false)
    Assert((try srv.Read(h, 6, mut buf[..])).Must() == 3)
    Assert(T3Eq(buf[0..3], "abx".AsBytes()))
}

test "RFC 9000 2.2 and 4.5: retransmission resends identical bytes and nothing is sent past final size" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    id := (try cli.Open(h, false)).Must()
    _ = try cli.Write(h, id, "abcdef".AsBytes(), true)
    // QT-59: writing after FIN fails.
    var post_fin_err := false
    _ = cli.Write(h, id, "g".AsBytes(), false) else {
        post_fin_err = true
        0
    }
    Assert(post_fin_err)

    var out1: [64]u8 = [0; 64]
    var out2: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [T3DummyFrame(); 4]
    r1 := cli.Next(h, mut out1[..], 64, mut fs[..])
    Assert(r1.frames == 1)
    cli.OnLost(h, &fs[0])
    r2 := cli.Next(h, mut out2[..], 64, mut fs[..])
    Assert(r2.frames == 1 && r2.n == r1.n && T3Eq(out1[0..r1.n], out2[0..r2.n]))
}

test "RFC 9000 3.3, 3.5, 4.4, 13.3: RESET_STREAM, STOP_SENDING, terminal silence, and suppressed reset in DataRecvd" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    id := (try cli.Open(h, false)).Must()
    _ = try cli.Write(h, id, "hello".AsBytes(), false)
    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [T3DummyFrame(); 4]
    r1 := cli.Next(h, mut out[..], 64, mut fs[..])
    Assert(r1.frames == 1)
    // Queue another write then lose packet 0, then STOP_SENDING arrives -> resets with code 42 and drops queued/lost data!
    _ = try cli.Write(h, id, "world".AsBytes(), false)
    cli.OnLost(h, &fs[0])
    try cli.OnStop(h, id, 42)
    r2 := cli.Next(h, mut out[..], 64, mut fs[..])
    Assert(r2.frames == 1)
    // Verify RESET_STREAM (0x04) with code 42 and final_size 5 (sent bytes before reset).
    ft := T23ReadVarint(out[0..r2.n], 0).Must()
    sid := T23ReadVarint(out[0..r2.n], ft.next).Must()
    rc := T23ReadVarint(out[0..r2.n], sid.next).Must()
    rfs := T23ReadVarint(out[0..r2.n], rc.next).Must()
    Assert(ft.v == 0x04 && sid.v == 0 && rc.v == 42 && rfs.v == 5)
    // QT-71: lost RESET_STREAM resends identical code and final size.
    var out_re: [64]u8 = [0; 64]
    cli.OnLost(h, &fs[0])
    r3 := cli.Next(h, mut out_re[..], 64, mut fs[..])
    Assert(r3.n == r2.n && T3Eq(out[0..r2.n], out_re[0..r3.n]))
    cli.OnAcked(h, &fs[0])

    // QT-61: receive half still works and keeps flow control after send reset!
    try cli.OnStream(h, id, 0, "reply".AsBytes(), true)
    // QT-69: RESET_STREAM in DataRecvd is suppressed and buffered data is still delivered!
    try cli.OnReset(h, id, 99, 5)
    var buf: [16]u8 = [0; 16]
    Assert((try cli.Read(h, id, mut buf[..])).Must() == 5)
    Assert(T3Eq(buf[0..5], "reply".AsBytes()))
    Assert((try cli.Read(h, id, mut buf[..])).Must() == 0)

    // QT-63: late frames after stream closed and freed are ignored and do not reopen it.
    cli.Release(h, id)
    try cli.OnStream(h, id, 0, "late".AsBytes(), false)
    try cli.OnReset(h, id, 1, 5)
    try cli.OnMaxStreamData(h, id, 999_999)
    try cli.OnStop(h, id, 1)
    Assert(cli.Accept(false).IsNone())
}

test "RFC 9000 3.5: aborting a read sends STOP_SENDING until the stream is done" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnStream(h, 0, 0, "abc".AsBytes(), false)
    srv.Stop(0, 55)
    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [T3DummyFrame(); 4]
    r1 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r1.frames == 1)
    ft := T23ReadVarint(out[0..r1.n], 0).Must()
    Assert(ft.v == 0x05)
    // Lost in Recv -> resent.
    srv.OnLost(h, &fs[0])
    r2 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r2.frames == 1)
    // Peer completes stream with RESET_STREAM -> lost STOP_SENDING is no longer resent.
    try srv.OnReset(h, 0, 0, 3)
    srv.OnLost(h, &fs[0])
    r3 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r3.frames == 0)
}

test "RFC 9000 19.4, 19.5, 19.8, 19.10, 19.13: stream-state legality checks" (sys: &System) {
    h := sys.heap.Alloc()
    lim := T3PeerLimits()
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)

    // QT-74: STREAM for uncreated local bidi (0) or send-only local uni (2).
    var e1: u64 = 0
    cli.OnStream(h, 0, 0, "x".AsBytes(), false) else e { e1 = T3Code(e) }
    Assert(e1 == ErrStreamState)
    var e2: u64 = 0
    cli.OnStream(h, 2, 0, "x".AsBytes(), false) else e { e2 = T3Code(e) }
    Assert(e2 == ErrStreamState)

    // QT-75 & QT-76: RESET_STREAM or STREAM_DATA_BLOCKED for send-only local uni (2).
    _ = try cli.Open(h, true) // creates id 2
    var e3: u64 = 0
    cli.OnReset(h, 2, 0, 0) else e { e3 = T3Code(e) }
    Assert(e3 == ErrStreamState)
    var e4: u64 = 0
    cli.OnStreamDataBlocked(h, 2, 0) else e { e4 = T3Code(e) }
    Assert(e4 == ErrStreamState)

    // QT-77 & QT-78: STOP_SENDING or MAX_STREAM_DATA for uncreated local bidi (0) or recv-only peer uni (3).
    var e5: u64 = 0
    cli.OnStop(h, 0, 0) else e { e5 = T3Code(e) }
    Assert(e5 == ErrStreamState)
    var e6: u64 = 0
    cli.OnMaxStreamData(h, 0, 100) else e { e6 = T3Code(e) }
    Assert(e6 == ErrStreamState)
    var e7: u64 = 0
    cli.OnStop(h, 3, 0) else e { e7 = T3Code(e) }
    Assert(e7 == ErrStreamState)
    var e8: u64 = 0
    cli.OnMaxStreamData(h, 3, 100) else e { e8 = T3Code(e) }
    Assert(e8 == ErrStreamState)
}
