// Why: the client read-token engine and Drive loop must handle idle exit,
// leader cancellation handoff among many concurrent followers, client write
// progress timeouts, and background PING/GOAWAY handling without deadlocks or
// lost wakes.
//
// What: lead.Driven and Conn.Drive tests in std.http.h2 over pipe.ConnPair.
//
// How: drives Conn.Client against Conn.Server and scripted net.Conn peers over
// bounded pipes, checking idle timer exit, 10-way concurrent cancel handoff,
// ProgressFloor reset under starved send windows, and Drive control frames.

import std.core
import std.http.fields
import std.net
import std.net.pipe
import std.rt
import std.sync.lead
import std.time

TestBeU32(b: []u8): u32 {
    return (u32(b[0]) << 24) | (u32(b[1]) << 16) | (u32(b[2]) << 8) | u32(b[3])
}

TestSendAck(io: Io, peer: &net.Conn) {
    var empty: [0]u8 = []
    TestSendRawFrame(io, peer, TypeSettings, FlagAck, 0, empty[..]).Must()
}

TestSendPeerSettings(io: Io, peer: &net.Conn, win: int, max_streams: int) {
    var s := InitialSettings
    s.initial_window = win
    s.max_streams = some(max_streams)
    s.enable_push = false
    var buf: [64]u8 = [0; 64]
    n := WriteSettings(mut buf[..], &s).Must()
    peer.WriteAll(io, buf[0..n]).Must()
}

TestSendFrameMust(io: Io, peer: &net.Conn, kind: u8, flags: u8, stream: u32, payload: []u8) {
    TestSendRawFrame(io, peer, kind, flags, stream, payload).Must()
}

TestLeadEchoDeliver(done: &Mutex[int], conn: &Conn, r: Request) {
    s := r.stream
    Request(stream: _, head, body) := r
    nc := core.IoMasked(rt.NativeIo())
    var hs: [1]TestField = [TestField(name: "x-ok", value: "1")]
    conn.Respond(nc, s, 200, hs[..], body.Len() == 0) else {
        conn.Done(s, head, body)
        return
    }
    if body.Len() > 0 {
        conn.Write(nc, s, body.Items(), true) else {}
    }
    conn.Done(s, head, body)
    var g := done.Lock(nc).Must()
    *g.Mut() += 1
    g.Unlock()
}

TestLeadServerLoop(io: Io, srv: &Conn, done: &Mutex[int]) {
    srv.Serve(io, done, TestLeadEchoDeliver) else {}
}

TestLeadReqTask(io: Io, h: &Heap, cli: &Conn, ok_cnt: &Mutex[int]) {
    nc := io.NoCancel()
    p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/lead")
    var hs: [0]TestField = []
    ms := cli.TryOpen(io, &p, hs[..], true) else {
        return
    }
    s := ms else {
        return
    }
    var a := Arena.New(h) else {
        cli.Release(s)
        return
    }
    defer a.Free()
    res := cli.Head(&a, io, s) else {
        cli.Release(s)
        return
    }
    Assert(res.status == 200)
    var buf: [64]u8 = [0; 64]
    n := cli.Read(io, s, mut buf[..]) else {
        cli.Release(s)
        return
    }
    Assert(n == 0)
    cli.Release(s)
    var g := ok_cnt.Lock(nc).Must()
    *g.Mut() += 1
    g.Unlock()
}

TestDriveTask(io: Io, cli: &Conn, idle: time.Duration, exited: &Mutex[int]) {
    nc := io.NoCancel()
    cli.Drive(io, idle) else {
        var g := exited.Lock(nc).Must()
        *g.Mut() = -1
        g.Unlock()
        return
    }
    var g := exited.Lock(nc).Must()
    *g.Mut() = 1
    g.Unlock()
}

TestHandshakePeer(a: &Alloc, io: Io, peer: &net.Conn, win: int, max_streams: int) {
    var pref: [24]u8 = [0; 24]
    ok := TestReadExact(io, peer, mut pref[..]).Must()
    Assert(ok)
    Assert(CoreBytesEq(pref[..], Preface[..]))
    // Expect client SETTINGS.
    f0 := TestReadFrame(a, io, peer).Must().Must()
    Assert(f0.kind == TypeSettings)
    f0.Free(a)
    // Send server SETTINGS + SETTINGS ACK.
    TestSendPeerSettings(io, peer, win, max_streams)
    TestSendAck(io, peer)
}

test "H2 client Drive exits after idle_limit when idle and stays running while idle<=0" (sys: &System) {
    io := sys.io
    h := sys.heap
    // Case 1: positive idle limit exits cleanly when no stream is active.
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 4096).Must()
    var done := Mutex[int].New(0)
    var exited := Mutex[int].New(0)

    var lim := TestDefaultLimits()
    lim.idle_limit = time.Seconds(5)
    var empty: [0]u8 = []
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    srv_box := Conn.Server(h, io, srv_conn, lim, empty[..]).Must()

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        s.go TestLeadServerLoop(io, srv_box.Ref(), &done)
        s.go TestWriterTask(io, srv_box.Ref())
        s.go TestDriveTask(io, cli_box.Ref(), time.Millis(25), &exited)
        var tries := 0
        for tries < 200 {
            var g := exited.Lock(io).Must()
            v := *g.Ref()
            g.Unlock()
            if v != 0 {
                break
            }
            io.Sleep(time.Millis(5)).Must()
            tries += 1
        }
        var g := exited.Lock(io).Must()
        Assert(*g.Ref() == 1)
        g.Unlock()
        s.Cancel()
    }
    Assert(cli_box.Ref().Draining())
    Assert(!cli_box.Ref().Usable())
    p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/new")
    var hs: [0]TestField = []
    Assert(cli_box.Ref().TryOpen(io, &p, hs[..], true).Must().IsNone())
    srv_box.Take(h.Alloc()).Abandon()
    cli_box.Take(h.Alloc()).Abandon()

    // Case 2: idle <= 0 keeps Drive running until canceled.
    (a: srv_conn2, b: cli_conn2) := pipe.ConnPair(h, 4096).Must()
    var exited2 := Mutex[int].New(0)
    cli_box2 := Conn.Client(h, io, cli_conn2, lim).Must()
    srv_box2 := Conn.Server(h, io, srv_conn2, lim, empty[..]).Must()
    scope s2 := io.Scope() {
        s2.CancelAfter(time.Seconds(5))
        s2.go TestLeadServerLoop(io, srv_box2.Ref(), &done)
        s2.go TestWriterTask(io, srv_box2.Ref())
        s2.go TestDriveTask(io, cli_box2.Ref(), time.Millis(0), &exited2)
        io.Sleep(time.Millis(25)).Must()
        var g0 := exited2.Lock(io).Must()
        Assert(*g0.Ref() == 0)
        g0.Unlock()
        s2.Cancel()
    }
    var g1 := exited2.Lock(io).Must()
    Assert(*g1.Ref() == -1)
    g1.Unlock()
    srv_box2.Take(h.Alloc()).Abandon()
    cli_box2.Take(h.Alloc()).Abandon()
}

test "H2 client retains the stream notification before a follower starts waiting" (sys: &System) {
    io := sys.io
    nc := io.NoCancel()
    (a: cli_conn, b: peer) := pipe.ConnPair(sys.heap, 4096).Must()
    defer peer.Abandon()
    cli_box := Conn.Client(sys.heap, io, cli_conn, TestDefaultLimits()).Must()
    defer cli_box.Take(sys.heap.Alloc()).Abandon()
    cli := cli_box.Ref()
    p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/")
    var hs: [0]TestField = []
    s := cli.TryOpen(io, &p, hs[..], true).Must().Must()
    defer cli.Release(s)
    party := ReaderParty(s.index)
    match cli.Begin(nc, PartyDrive) {
        lead.Step.Lead(deadline: _) {}
        _ { Assert(false) }
    }
    match cli.Begin(nc, party) {
        lead.Step.Wait(deadline: _) {}
        _ { Assert(false) }
    }

    cli.HandleRstStream(nc, 1, RefusedStream, time.Now(nc)).Must()
    scope w := io.Scope() {
        w.CancelAfter(time.Seconds(1))
        cli.Party(party).Wait(io).Must()
    }
    match cli.Begin(nc, party) {
        lead.Step.Done {}
        _ { Assert(false) }
    }
    Assert(cli.End(nc, party) == -1)
    Assert(cli.End(nc, PartyDrive) == -1)
}

test "H2 client leader handoff on cancel lets 10 waiting streams complete" (sys: &System) {
    io := sys.io
    h := sys.heap
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()
    var lim := TestDefaultLimits()
    lim.streams = 16
    var empty: [0]u8 = []
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    srv_box := Conn.Server(h, io, srv_conn, lim, empty[..]).Must()
    var srv_done := Mutex[int].New(0)
    var ok_cnt := Mutex[int].New(0)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        s.go TestLeadServerLoop(io, srv_box.Ref(), &srv_done)
        s.go TestWriterTask(io, srv_box.Ref())
        scope cs := io.Scope() {
            cs.go TestLeadReqTask(io, h, cli_box.Ref(), &ok_cnt)
            cs.Cancel()
        }
        scope rs := io.Scope() {
            for _ in 0..10 {
                rs.go TestLeadReqTask(io, h, cli_box.Ref(), &ok_cnt)
            }
        }
        s.Cancel()
    }
    var g := ok_cnt.Lock(io).Must()
    Assert(*g.Ref() == 10)
    g.Unlock()
    srv_box.Take(h.Alloc()).Abandon()
    cli_box.Take(h.Alloc()).Abandon()
}

TestPeerHandshakeThenDrain(io: Io, h: &Heap, peer: &net.Conn, win: int, max_streams: int, stop: &Mutex[bool]) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, win, max_streams)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    var buf: [512]u8 = [0; 512]
    var sent_sync := false
    for {
        var g := stop.Lock(io.NoCancel()).Must()
        s := *g.Ref()
        g.Unlock()
        if s {
            return
        }
        if !sent_sync {
            tf := TestReadFrame(a, io, peer) else { return }
            f := tf else { return }
            is_h := f.kind == TypeHeaders && f.stream == 1
            f.Free(a)
            if is_h {
                var p200: [1](name: str, val: str) = [(name: ":status", val: "200")]
                b200 := TestEncodeHeaders(a, mut enc, p200[..])
                TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders | FlagEndStream, 1, b200.Items())
                b200.Free(a)
                sent_sync = true
            }
        } else {
            scope rs := io.Scope() {
                rs.CancelAfter(time.Millis(10))
                _ = peer.Read(io, mut buf[..]) else { 0 }
            }
        }
    }
}

TestPeerReadUploadPastInitialWindow(io: Io, h: &Heap, peer: &net.Conn) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, InitialWindow, 1)
    var received := 0
    for received < InitialWindow {
        frame := TestReadFrame(a, io, peer).Must().Must()
        if frame.kind == TypeData {
            Assert(frame.stream == 1 && (frame.flags & FlagEndStream) == 0)
            received += frame.payload.Len()
        }
        frame.Free(a)
    }
    Assert(received == InitialWindow)

    credit: [4]u8 = [0, 0, 0, 1]
    TestSendFrameMust(io, peer, TypeWindowUpdate, 0, 0, credit[..])
    TestSendFrameMust(io, peer, TypeWindowUpdate, 0, 1, credit[..])
    for {
        frame := TestReadFrame(a, io, peer).Must().Must()
        is_data := frame.kind == TypeData
        if is_data {
            Assert(frame.stream == 1 && (frame.flags & FlagEndStream) != 0)
            Assert(CoreBytesEq(frame.payload.Items(), "x".AsBytes()))
        }
        frame.Free(a)
        if is_data {
            return
        }
    }
}

test "H2 client resumes a 64 KiB upload when one byte of window credit arrives" (sys: &System) {
    io := sys.io
    h := sys.heap
    (a: cli, b: peer) := pipe.ConnPair(h, 8192).Must()
    defer peer.Abandon()
    var lim := TestDefaultLimits()
    lim.window = 8192
    cli_box := Conn.Client(h, io, cli, lim).Must()
    defer cli_box.Take(h.Alloc()).Abandon()

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        s.go TestPeerReadUploadPastInitialWindow(io, h, &peer)
        p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/upload")
        hs: [1]TestField = [TestField(name: "content-length", value: "65536")]
        stream := cli_box.Ref().TryOpen(io, &p, hs[..], false).Must().Must()
        defer cli_box.Ref().Release(stream)
        payload: [65536]u8 = [b'x'; 65536]
        cli_box.Ref().Send(io, stream, payload[..], true).Must()
    }
}

test "H2 client ProgressFloor timer aborts starved Send without peer WINDOW_UPDATE" (sys: &System) {
    io := sys.io
    h := sys.heap
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()

    var lim := TestDefaultLimits()
    lim.write_limit = time.Millis(30)
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    var send_failed := false
    var stop := Mutex[bool].New(false)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        s.go TestPeerHandshakeThenDrain(io, h, &srv_conn, 16, 16, &stop)
        // Complete a 0-body GET on stream 1 so peer SETTINGS(initial_window=16) is definitely applied.
        p1 := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/sync")
        var hs: [0]TestField = []
        s1 := cli_box.Ref().TryOpen(io, &p1, hs[..], true).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        res1 := cli_box.Ref().Head(&ar, io, s1).Must()
        Assert(res1.status == 200)
        cli_box.Ref().Release(s1)

        p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/up")
        sr := cli_box.Ref().TryOpen(io, &p, hs[..], false).Must().Must()
        var big: [4096]u8 = [0x41; 4096]
        cli_box.Ref().Send(io, sr, big[..], true) else {
            send_failed = true
        }
        var g := stop.Lock(io).Must()
        *g.Mut() = true
        g.Unlock()
        cli_box.Ref().Release(sr)
    }
    Assert(send_failed)
    srv_conn.Abandon()
    cli_box.Take(h.Alloc()).Abandon()
}

test "H2 client Drive replies to PING and exits cleanly on GOAWAY" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()

    var lim := TestDefaultLimits()
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    var exited := Mutex[int].New(0)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(5))
        s.go TestDriveTask(io, cli_box.Ref(), time.Millis(0), &exited)
        TestHandshakePeer(a, io, &srv_conn, 65535, 16)

        // Send PING from peer while client Drive is idle.
        var ping_pay: [8]u8 = [1, 2, 3, 4, 5, 6, 7, 8]
        TestSendFrameMust(io, &srv_conn, TypePing, 0, 0, ping_pay[..])

        var found := false
        for _ in 0..8 {
            tf := TestReadFrame(a, io, &srv_conn).Must().Must()
            kind := tf.kind
            flags := tf.flags
            is_match := kind == TypePing && (flags & FlagAck) != 0 && CoreBytesEq(tf.payload.Items(), ping_pay[..])
            tf.Free(a)
            if kind == TypePing && (flags & FlagAck) != 0 {
                Assert(is_match)
                found = true
                break
            }
        }
        Assert(found)

        // Now send GOAWAY(last_stream: 1, code: NoError); Drive must exit with 1.
        var gw: [17]u8 = [0; 17]
        gn := WriteGoaway(mut gw[..], 1, NoError).Must()
        srv_conn.WriteAll(io, gw[0..gn]).Must()

        var tries := 0
        for tries < 100 {
            var ge := exited.Lock(io).Must()
            v := *ge.Ref()
            ge.Unlock()
            if v != 0 {
                break
            }
            io.Sleep(time.Millis(5)).Must()
            tries += 1
        }
        var ge := exited.Lock(io).Must()
        Assert(*ge.Ref() == 1)
        ge.Unlock()
        s.Cancel()
    }
    Assert(!cli_box.Ref().Usable())
    gl := cli_box.Ref().GoawayLast().Must()
    Assert(gl == u32(1))
    srv_conn.Abandon()
    cli_box.Take(h.Alloc()).Abandon()
}
