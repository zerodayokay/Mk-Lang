// Why: QUIC flow control prevents memory exhaustion across streams and
// connections, refreshes MAX_DATA on buffering and MAX_STREAM_DATA on read or
// admission, accounts for RESET_STREAM final sizes, and checks final-size rules.
//
// What: unit tests for QT-50, QT-54..QT-58, QT-60, QT-64, QT-67, QT-68,
// QT-70, and QT-95.
//
// How: configures tight local and peer limits on Streams and inspects emitted
// MAX_DATA / MAX_STREAM_DATA / DATA_BLOCKED / STREAM_DATA_BLOCKED frames and
// error codes.

TFlDummy(): SentFrame {
    return SentFrame(kind: SentKind.Control(index: 0))
}

TFlCode(e: Error): u64 {
    return match e {
        TransportError(code, frame: _) { code }
        _ { 9999 }
    }
}

test "RFC 9000 4.1 and 4.6: MAX_DATA, MAX_STREAM_DATA and MAX_STREAMS that do not raise a limit are ignored" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(true)
    lim.peer_max_data = 1000
    lim.peer_max_stream_data_bidi_remote = 500
    lim.peer_max_streams_bidi = 4
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    id := (try cli.Open(h, false)).Must()

    cli.OnMaxData(800)
    cli.OnMaxData(1000)
    Assert(cli.conn_flow.tx_max_data == 1000)

    try cli.OnMaxStreamData(h, id, 400)
    try cli.OnMaxStreamData(h, id, 500)
    Assert(cli.map.Get(&id).Must().send.tx_max_stream_data == 500)

    try cli.OnMaxStreams(true, 2)
    try cli.OnMaxStreams(true, 4)
    Assert(cli.limits.peer_max_streams_bidi == 4)
}

test "RFC 9000 4.1: the sender never exceeds stream or connection credit" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(true)
    lim.peer_max_data = 100
    lim.peer_max_stream_data_bidi_remote = 40
    lim.peer_max_streams_bidi = 4
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    s0 := (try cli.Open(h, false)).Must()
    s1 := (try cli.Open(h, false)).Must()
    var big: [100]u8 = [b'a'; 100]
    _ = try cli.Write(h, s0, big[..], false)
    _ = try cli.Write(h, s1, big[..], false)

    var out: [512]u8 = [0; 512]
    var fs: [8]SentFrame = [TFlDummy(); 8]
    r := cli.Next(h, mut out[..], 512, mut fs[..])
    // s0 is capped by stream limit 40; s1 gets 40 (total 80 <= conn 100).
    Assert(cli.conn_flow.tx_sent == 80)
    Assert(cli.map.Get(&s0).Must().send.send_offset == 40)
    Assert(cli.map.Get(&s1).Must().send.send_offset == 40)
    _ = r

    // Open s2 with stream limit 40: remaining conn credit is 20 -> sends at most 20!
    s2 := (try cli.Open(h, false)).Must()
    _ = try cli.Write(h, s2, big[..], false)
    _ = cli.Next(h, mut out[..], 512, mut fs[..])
    Assert(cli.conn_flow.tx_sent == 100)
    Assert(cli.map.Get(&s2).Must().send.send_offset == 20)
}

test "RFC 9000 4.1: data past stream or connection credit is FLOW_CONTROL_ERROR" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(false)
    lim.max_data = 30
    lim.max_stream_data_bidi_remote = 20
    lim.max_streams_bidi = 4
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    var buf21: [21]u8 = [b'x'; 21]
    var e1: u64 = 0
    srv.OnStream(h, 0, 0, buf21[..], false) else e { e1 = TFlCode(e) }
    Assert(e1 == ErrFlowControl)

    var buf18: [18]u8 = [b'y'; 18]
    try srv.OnStream(h, 0, 0, buf18[..], false)
    try srv.OnStream(h, 4, 0, buf18[0..10], false)
    // Total rx_total is 28; 5 more bytes on stream 4 (offset 10..15 <= stream 20) exceeds conn 30!
    var e2: u64 = 0
    srv.OnStream(h, 4, 10, buf18[0..5], false) else e { e2 = TFlCode(e) }
    Assert(e2 == ErrFlowControl)
}

test "RFC 9000 4.2: MAX_STREAM_DATA and MAX_DATA go out at half the window with no BLOCKED frame received" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(true)
    lim.max_data = 200
    lim.max_stream_data_bidi_local = 100
    lim.peer_max_streams_bidi = 4
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    id := (try cli.Open(h, false)).Must()
    var data: [60]u8 = [b'k'; 60]
    try cli.OnStream(h, id, 0, data[..], false)

    // Connection credit returns on buffering (60 < half of 200 = 100 -> not yet).
    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [TFlDummy(); 4]
    Assert(cli.Next(h, mut out[..], 64, mut fs[..]).frames == 0)

    // Read 50 bytes (half of stream window 100) -> MAX_STREAM_DATA(id, 150) owed immediately!
    var rbuf: [50]u8 = [0; 50]
    Assert((try cli.Read(h, id, mut rbuf[..])).Must() == 50)
    r := cli.Next(h, mut out[..], 64, mut fs[..])
    Assert(r.frames == 1)
    ft := T23ReadVarint(out[0..r.n], 0).Must()
    sid := T23ReadVarint(out[0..r.n], ft.next).Must()
    lim_v := T23ReadVarint(out[0..r.n], sid.next).Must()
    Assert(ft.v == 0x11 && sid.v == Cast[u64](id) && lim_v.v == 150)

    // Buffer 40 more bytes (total 100 = half of conn window 200) -> MAX_DATA(300) owed on buffering!
    try cli.OnStream(h, id, 60, data[0..40], false)
    r2 := cli.Next(h, mut out[..], 64, mut fs[..])
    Assert(r2.frames == 1)
    ft2 := T23ReadVarint(out[0..r2.n], 0).Must()
    mv := T23ReadVarint(out[0..r2.n], ft2.next).Must()
    Assert(ft2.v == 0x10 && mv.v == 300)

    // Server body admission credit through Credit raises limit immediately.
    var srv_lim := StreamLimits.Default(false)
    srv_lim.max_stream_data_bidi_remote = 16384
    var srv := try Streams.New(h, false, &srv_lim)
    defer srv.Free(h)
    try srv.OnStream(h, 0, 0, data[0..10], false)
    srv.Credit(0, 65536)
    r3 := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r3.frames == 1)
    ft3 := T23ReadVarint(out[0..r3.n], 0).Must()
    sid3 := T23ReadVarint(out[0..r3.n], ft3.next).Must()
    mv3 := T23ReadVarint(out[0..r3.n], sid3.next).Must()
    Assert(ft3.v == 0x11 && mv3.v == 65536)
}

test "RFC 9000 4.5: a RESET_STREAM's final size is charged to connection flow control" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(false)
    lim.max_data = 100
    lim.max_stream_data_bidi_remote = 80
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnStream(h, 0, 0, "abc".AsBytes(), false)
    Assert(srv.conn_flow.rx_total == 3)
    try srv.OnReset(h, 0, 1, 60)
    // Connection consumed jumps to 60 (>= half of 100), so MAX_DATA(160) goes out!
    Assert(srv.conn_flow.rx_total == 60)
    var out: [64]u8 = [0; 64]
    var fs: [4]SentFrame = [TFlDummy(); 4]
    r := srv.Next(h, mut out[..], 64, mut fs[..])
    Assert(r.frames == 1)
    ft := T23ReadVarint(out[0..r.n], 0).Must()
    mv := T23ReadVarint(out[0..r.n], ft.next).Must()
    Assert(ft.v == 0x10 && mv.v == 160)
}

test "RFC 9000 4.5: a changed final size, data past it, or a final size below received data is FINAL_SIZE_ERROR" (sys: &System) {
    h := sys.heap.Alloc()
    lim := StreamLimits.Default(false)
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnStream(h, 0, 0, "hello".AsBytes(), true)
    var e1: u64 = 0
    srv.OnStream(h, 0, 0, "hello!".AsBytes(), true) else e { e1 = TFlCode(e) }
    Assert(e1 == ErrFinalSize)
    var e2: u64 = 0
    srv.OnStream(h, 0, 5, "x".AsBytes(), false) else e { e2 = TFlCode(e) }
    Assert(e2 == ErrFinalSize)
    var e3: u64 = 0
    srv.OnReset(h, 0, 0, 4) else e { e3 = TFlCode(e) }
    Assert(e3 == ErrFinalSize)
    var e4: u64 = 0
    srv.OnReset(h, 0, 0, 6) else e { e4 = TFlCode(e) }
    Assert(e4 == ErrFinalSize)

    // Final size below already-received data on new stream 4.
    try srv.OnStream(h, 4, 0, "0123456789".AsBytes(), false)
    var e5: u64 = 0
    srv.OnStream(h, 4, 0, "0123".AsBytes(), true) else e { e5 = TFlCode(e) }
    Assert(e5 == ErrFinalSize)
    var e6: u64 = 0
    srv.OnReset(h, 4, 0, 9) else e { e6 = TFlCode(e) }
    Assert(e6 == ErrFinalSize)
}

test "RFC 9000 3.5: data after STOP_SENDING is counted toward flow control and discarded" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(false)
    lim.max_data = 100
    lim.max_stream_data_bidi_remote = 100
    var srv := try Streams.New(h, false, &lim)
    defer srv.Free(h)
    try srv.OnStream(h, 0, 0, "abc".AsBytes(), false)
    srv.Stop(0, 9)
    var big: [60]u8 = [b'z'; 60]
    try srv.OnStream(h, 0, 3, big[..], false)
    Assert(srv.conn_flow.rx_total == 63)
    var rbuf: [16]u8 = [0; 16]
    Assert((try srv.Read(h, 0, mut rbuf[..])).IsNone())
}

test "RFC 9000 4.1, 13.3, 19.13: BLOCKED frames, stale BLOCKED drop, no MAX_STREAM_DATA in SizeKnown, and current-limit resend" (sys: &System) {
    h := sys.heap.Alloc()
    var lim := StreamLimits.Default(true)
    lim.max_data = 200
    lim.max_stream_data_bidi_local = 100
    lim.peer_max_data = 200
    lim.peer_max_stream_data_bidi_remote = 5
    lim.peer_max_streams_bidi = 4
    var cli := try Streams.New(h, true, &lim)
    defer cli.Free(h)
    id := (try cli.Open(h, false)).Must()
    _ = try cli.Write(h, id, "0123456789".AsBytes(), false)

    var out: [128]u8 = [0; 128]
    var fs: [8]SentFrame = [TFlDummy(); 8]
    // First Next sends 5 bytes.
    r1 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r1.frames == 1)
    cli.OnAcked(h, &fs[0])
    // Second Next finds stream blocked at 5 -> emits STREAM_DATA_BLOCKED(id, 5).
    r2 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r2.frames == 1)
    ft := T23ReadVarint(out[0..r2.n], 0).Must()
    sid := T23ReadVarint(out[0..r2.n], ft.next).Must()
    blim := T23ReadVarint(out[0..r2.n], sid.next).Must()
    Assert(ft.v == 0x15 && blim.v == 5)
    // QT-68: peer sending BLOCKED for limit already raised drops cleanly.
    cli.OnDataBlocked(10)
    try cli.OnStreamDataBlocked(h, id, 10)
    cli.OnStreamsBlocked(true, 1)

    // QT-70: no MAX_STREAM_DATA once final size is known (SizeKnown)!
    var d60: [60]u8 = [b'a'; 60]
    try cli.OnStream(h, id, 60, d60[0..10], true) // out-of-order FIN -> SizeKnown!
    try cli.OnStream(h, id, 0, d60[..], false)    // complete -> DataRecvd
    var rbuf: [60]u8 = [0; 60]
    _ = try cli.Read(h, id, mut rbuf[..])
    // Should NOT emit MAX_STREAM_DATA because final size is known (and 70 < half of 200 conn window).
    r3 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r3.frames == 0)

    // QT-95: lost MAX_DATA resends the CURRENT limit, and losing an older one is ignored!
    var d100: [100]u8 = [b'b'; 100]
    id2 := (try cli.Open(h, false)).Must()
    lim.max_stream_data_bidi_local = 1000
    try cli.OnStream(h, id2, 0, d60[..], false) // total 130 >= half of 200 -> MAX_DATA(330)
    r4 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r4.frames == 1)
    f_old := fs[0]
    // Buffer another 100 -> total 230 -> MAX_DATA(430) owed and sent!
    id3 := (try cli.Open(h, false)).Must()
    try cli.OnStream(h, id3, 0, d100[..], false)
    r5 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r5.frames == 1)
    f_new := fs[0]
    // Losing f_old is ignored because f_new superseded it!
    cli.OnLost(h, &f_old)
    Assert(cli.Next(h, mut out[..], 128, mut fs[..]).frames == 0)
    // Losing f_new resends current limit (430).
    cli.OnLost(h, &f_new)
    r6 := cli.Next(h, mut out[..], 128, mut fs[..])
    Assert(r6.frames == 1)
    ft6 := T23ReadVarint(out[0..r6.n], 0).Must()
    mv6 := T23ReadVarint(out[0..r6.n], ft6.next).Must()
    Assert(ft6.v == 0x10 && mv6.v == 430)
}
