// Why: the HTTP/2 client engine must drive 100 concurrent streams,
// 32 MiB bidirectional transfers over bounded pipes, 50 mid-body cancel
// cycles without slot/token corruption, 1xx skipping, NO_ERROR early-response
// resets, PUSH_PROMISE timing rules, GOAWAY unprocessed semantics, and the
// retained-head byte cap.
//
// What: client integration tests in std.http.h2.
//
// How: pairs Conn.Client with both Conn.Server and scripted net.Conn peers
// over std.net.pipe.

import std.core
import std.http.fields
import std.net
import std.net.pipe
import std.rt

TestCliEchoDeliver(cnt: &Mutex[int], conn: &Conn, r: Request) {
    s := r.stream
    Request(stream: _, head, body) := r
    nc := core.IoMasked(rt.NativeIo())
    var hs: [1]TestField = [TestField(name: "x-srv", value: "ok")]
    conn.Respond(nc, s, 200, hs[..], body.Len() == 0) else {
        conn.Done(s, head, body)
        return
    }
    if body.Len() > 0 {
        conn.Write(nc, s, body.Items(), true) else {}
    }
    conn.Done(s, head, body)
    var g := cnt.Lock(nc).Must()
    *g.Mut() += 1
    g.Unlock()
}

TestCliServerTask(io: Io, srv: &Conn, cnt: &Mutex[int]) {
    srv.Serve(io, cnt, TestCliEchoDeliver) else {}
}

TestCliOpenAndFinish(io: Io, h: &Heap, cli: &Conn, ok: &Mutex[int]) {
    nc := io.NoCancel()
    p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/fan")
    var hs: [1]TestField = [TestField(name: "x-id", value: "1")]
    ms := cli.TryOpen(io, &p, hs[..], false) else { return }
    s := ms else { return }
    var pay: [3]u8 = [b'a', b'b', b'c']
    cli.Send(io, s, pay[..], true) else {
        cli.Release(s)
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
    var buf: [16]u8 = [0; 16]
    var got := 0
    for {
        n := cli.Read(io, s, mut buf[got..]) else {
            cli.Release(s)
            return
        }
        if n == 0 {
            break
        }
        got += n
    }
    Assert(got == 3)
    Assert(CoreBytesEq(buf[0..3], pay[..]))
    cli.Release(s)
    var g := ok.Lock(nc).Must()
    *g.Mut() += 1
    g.Unlock()
}

test "H2 client 100 concurrent streams and fan-out-then-iterate over 4 KiB pipe" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 4096).Must()

    var lim := TestDefaultLimits()
    lim.streams = 100
    var empty: [0]u8 = []
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    srv_box := Conn.Server(h, io, srv_conn, lim, empty[..]).Must()
    var srv_cnt := Mutex[int].New(0)
    var ok_cnt := Mutex[int].New(0)
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(10_000_000_000))
        s.go TestCliServerTask(io, srv_box.Ref(), &srv_cnt)
        s.go TestWriterTask(io, srv_box.Ref())

        // Fan-out-then-iterate: open 100 streams first, verify 101st hits cap (none), then drain all 100.
        var refs := HeapList[StreamRef].New(a)
        defer refs.Free(a)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/fan100")
        var hs: [0]TestField = []
        for _ in 0..100 {
            sr := cli.TryOpen(io, &p, hs[..], true).Must().Must()
            refs.Push(a, sr).Must()
        }
        extra := cli.TryOpen(io, &p, hs[..], true).Must()
        Assert(extra.IsNone())

        var ar := Arena.New(h).Must()
        defer ar.Free()
        var buf: [16]u8 = [0; 16]
        for i in 0..refs.Len() {
            sr := refs.Items()[i]
            res := cli.Head(&ar, io, sr).Must()
            Assert(res.status == 200)
            n := cli.Read(io, sr, mut buf[..]).Must()
            Assert(n == 0)
            cli.Release(sr)
            ar.Reset()
        }

        // Now launch 100 concurrent stream tasks simultaneously.
        scope batch := io.Scope() {
            for _ in 0..100 {
                batch.go TestCliOpenAndFinish(io, h, cli, &ok_cnt)
            }
        }
        var g := ok_cnt.Lock(io).Must()
        Assert(*g.Ref() == 100)
        g.Unlock()
        s.Cancel()
    }
    srv_box.Take(a).Abandon()
    cli_box.Take(a).Abandon()
}

TestSendBulkTask(io: Io, cli: &Conn, s: StreamRef, total: int) {
    var chunk: [16384]u8 = [0x5A; 16384]
    var sent := 0
    for sent < total {
        step := if total - sent < 16384 { total - sent } else { 16384 }
        end := sent + step == total
        cli.Send(io, s, chunk[0..step], end).Must()
        sent += step
    }
}

TestPeerBulkBidi(io: Io, h: &Heap, peer: &net.Conn, total: int, max_win: i64) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, 65535, 16)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    var hpairs: [1](name: str, val: str) = [(name: ":status", val: "200")]
    hblk := TestEncodeHeaders(a, mut enc, hpairs[..])
    TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 1, hblk.Items())
    hblk.Free(a)

    var peer_recv_win := 65535
    var peer_send_conn_win := 65535
    var peer_send_strm_win := 65535
    var peer_recvd := 0
    var peer_sent := 0
    var down_chunk: [8192]u8 = [0x33; 8192]

    for peer_recvd < total || peer_sent < total {
        for peer_sent < total && peer_send_conn_win >= 8192 && peer_send_strm_win >= 8192 {
            rem := total - peer_sent
            n := if rem < 8192 { rem } else { 8192 }
            fl := if peer_sent + n == total { FlagEndStream } else { 0 }
            TestSendFrameMust(io, peer, TypeData, fl, 1, down_chunk[0..n])
            peer_sent += n
            peer_send_conn_win -= n
            peer_send_strm_win -= n
            Assert(peer_send_conn_win >= 0 && peer_send_strm_win >= 0)
        }
        tf := TestReadFrame(a, io, peer).Must().Must()
        if tf.kind == TypeData && tf.stream == 1 {
            dlen := tf.payload.Len()
            peer_recvd += dlen
            peer_recv_win -= dlen
            Assert(peer_recv_win >= 0)
            Assert(i64(dlen) <= max_win)
            if peer_recv_win < 32768 && peer_recvd < total {
                var wu: [16]u8 = [0; 16]
                w0 := WriteWindowUpdate(mut wu[..], 0, 32768).Must()
                peer.WriteAll(io, wu[0..w0]).Must()
                w1 := WriteWindowUpdate(mut wu[..], 1, 32768).Must()
                peer.WriteAll(io, wu[0..w1]).Must()
                peer_recv_win += 32768
            }
        } else if tf.kind == TypeWindowUpdate {
            inc := int(TestBeU32(tf.payload.Items()) & 0x7FFF_FFFF)
            if tf.stream == 0 {
                peer_send_conn_win += inc
            } else if tf.stream == 1 {
                peer_send_strm_win += inc
            }
        }
        tf.Free(a)
    }
}

test "H2 client 32 MiB bidirectional transfer over bounded pipe within windows" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 65536).Must()

    var lim := TestDefaultLimits()
    lim.window = 65535
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    cli := cli_box.Ref()
    total := 32 * 1024 * 1024

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(30_000_000_000))
        s.go TestPeerBulkBidi(io, h, &srv_conn, total, 65535)
        p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/bulk")
        var hs: [0]TestField = []
        sr := cli.TryOpen(io, &p, hs[..], false).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        scope send_s := io.Scope() {
            send_s.go TestSendBulkTask(io, cli, sr, total)
            res := cli.Head(&ar, io, sr).Must()
            Assert(res.status == 200)

            var rbuf: [16384]u8 = [0; 16384]
            var rtotal := 0
            for {
                n := cli.Read(io, sr, mut rbuf[..]).Must()
                if n == 0 {
                    break
                }
                rtotal += n
            }
            Assert(rtotal == total)
        }
        cli.Release(sr)
    }
    srv_conn.Abandon()
    cli_box.Take(a).Abandon()
}

test "H2 client 50 cancel-mid-body cycles keep slots and token healthy" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()
    var lim := TestDefaultLimits()
    lim.streams = 4
    var empty: [0]u8 = []
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    srv_box := Conn.Server(h, io, srv_conn, lim, empty[..]).Must()
    var cnt := Mutex[int].New(0)
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(10_000_000_000))
        s.go TestCliServerTask(io, srv_box.Ref(), &cnt)
        s.go TestWriterTask(io, srv_box.Ref())
        p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/mid")
        var hs: [0]TestField = []
        var chunk: [64]u8 = [0x42; 64]
        for _ in 0..50 {
            sr := cli.TryOpen(io, &p, hs[..], false).Must().Must()
            cli.Send(io, sr, chunk[..], false).Must()
            cli.Release(sr)
        }
        // After 50 cancellations with streams=4, all 4 slots must still be usable.
        var ar := Arena.New(h).Must()
        defer ar.Free()
        var buf: [16]u8 = [0; 16]
        for _ in 0..4 {
            sr := cli.TryOpen(io, &p, hs[..], true).Must().Must()
            res := cli.Head(&ar, io, sr).Must()
            Assert(res.status == 200)
            n := cli.Read(io, sr, mut buf[..]).Must()
            Assert(n == 0)
            cli.Release(sr)
            ar.Reset()
        }
        s.Cancel()
    }
    srv_box.Take(a).Abandon()
    cli_box.Take(a).Abandon()
}

TestPeer1xxThen200(io: Io, h: &Heap, peer: &net.Conn) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, 65535, 16)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    for {
        rf := TestReadFrame(a, io, peer) else { break }
        tf := rf else { break }
        sid := tf.stream
        is_hdr := tf.kind == TypeHeaders
        tf.Free(a)
        if is_hdr && sid == 1 {
            var p100: [1](name: str, val: str) = [(name: ":status", val: "100")]
            b100 := TestEncodeHeaders(a, mut enc, p100[..])
            TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 1, b100.Items())
            b100.Free(a)

            var p103: [2](name: str, val: str) = [(name: ":status", val: "103"), (name: "link", val: "</a.css>; rel=preload")]
            b103 := TestEncodeHeaders(a, mut enc, p103[..])
            TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 1, b103.Items())
            b103.Free(a)

            var p200: [2](name: str, val: str) = [(name: ":status", val: "200"), (name: "x-final", val: "yes")]
            b200 := TestEncodeHeaders(a, mut enc, p200[..])
            TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 1, b200.Items())
            b200.Free(a)

            TestSendFrameMust(io, peer, TypeData, FlagEndStream, 1, "hello".AsBytes())
        } else if is_hdr && sid == 3 {
            var p100: [1](name: str, val: str) = [(name: ":status", val: "100")]
            b100 := TestEncodeHeaders(a, mut enc, p100[..])
            TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 3, b100.Items())
            b100.Free(a)
            break
        }
    }
    peer.Shutdown(io, net.Shutdown.Both) else {}
}

test "H2 client skips 100 and 103 interim heads and fails cleanly on EOF after 1xx" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()
    cli_box := Conn.Client(h, io, cli_conn, TestDefaultLimits()).Must()
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestPeer1xxThen200(io, h, &srv_conn)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/1xx")
        var hs: [0]TestField = []
        s1 := cli.TryOpen(io, &p, hs[..], true).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        res := cli.Head(&ar, io, s1).Must()
        Assert(res.status == 200)
        var rbuf: [16]u8 = [0; 16]
        n := cli.Read(io, s1, mut rbuf[..]).Must()
        Assert(n == 5)
        Assert(CoreBytesEq(rbuf[0..5], "hello".AsBytes()))
        Assert(cli.Read(io, s1, mut rbuf[..]).Must() == 0)
        cli.Release(s1)

        s3 := cli.TryOpen(io, &p, hs[..], true).Must().Must()
        var got_eof_err := false
        var empty_fv: [0]fields.FieldView = []
        _ = cli.Head(&ar, io, s3) else e {
            got_eof_err = true
            _ = e
            (status: 0, list: empty_fv[..], content_length: none)
        }
        Assert(got_eof_err)
        cli.Release(s3)
    }
    srv_conn.Abandon()
    cli_box.Take(a).Abandon()
}

TestPeerEarlyNoError(io: Io, h: &Heap, peer: &net.Conn) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, 65535, 16)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    for {
        tf := TestReadFrame(a, io, peer).Must().Must()
        sid := tf.stream
        is_hdr := tf.kind == TypeHeaders
        tf.Free(a)
        if is_hdr && sid == 1 {
            var p200: [1](name: str, val: str) = [(name: ":status", val: "200")]
            b200 := TestEncodeHeaders(a, mut enc, p200[..])
            TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders, 1, b200.Items())
            b200.Free(a)
            TestSendFrameMust(io, peer, TypeData, FlagEndStream, 1, "done".AsBytes())
            var rst: [16]u8 = [0; 16]
            rn := WriteRstStream(mut rst[..], 1, NoError).Must()
            peer.WriteAll(io, rst[0..rn]).Must()
            return
        }
    }
}

test "H2 client keeps response readable after RST_STREAM(NO_ERROR) post-completion" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()
    cli_box := Conn.Client(h, io, cli_conn, TestDefaultLimits()).Must()
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestPeerEarlyNoError(io, h, &srv_conn)
        p := fields.Pseudo(method: "POST", scheme: "http", authority: "example.com", path: "/early")
        var hs: [0]TestField = []
        s1 := cli.TryOpen(io, &p, hs[..], false).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        res := cli.Head(&ar, io, s1).Must()
        Assert(res.status == 200)
        var rbuf: [16]u8 = [0; 16]
        Assert(cli.Read(io, s1, mut rbuf[..]).Must() == 4)
        Assert(CoreBytesEq(rbuf[0..4], "done".AsBytes()))
        Assert(cli.Read(io, s1, mut rbuf[..]).Must() == 0)
        cli.Release(s1)
    }
    srv_conn.Abandon()
    cli_box.Take(a).Abandon()
}

TestPeerPushPromise(io: Io, h: &Heap, peer: &net.Conn, send_ack_first: bool, saw_expected: &Mutex[bool]) {
    a := h.Alloc()
    var pref: [24]u8 = [0; 24]
    ok := TestReadExact(io, peer, mut pref[..]).Must()
    Assert(ok)
    f0 := TestReadFrame(a, io, peer).Must().Must()
    f0.Free(a)
    TestSendPeerSettings(io, peer, 65535, 16)
    if send_ack_first {
        TestSendAck(io, peer)
    }

    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    for {
        tf := TestReadFrame(a, io, peer).Must().Must()
        sid := tf.stream
        is_hdr := tf.kind == TypeHeaders
        tf.Free(a)
        if is_hdr && sid == 1 {
            // Send PUSH_PROMISE for stream 2 referencing stream 1.
            var pp_req: [4](name: str, val: str) = [
                (name: ":method", val: "GET"),
                (name: ":scheme", val: "http"),
                (name: ":authority", val: "example.com"),
                (name: ":path", val: "/pushed"),
            ]
            pblk := TestEncodeHeaders(a, mut enc, pp_req[..])
            var ppay := HeapList[u8].New(a)
            for _ in 0..4 {
                ppay.Push(a, 0).Must()
            }
            PutBeU32(mut ppay.ItemsMut()[0..4], 2)
            ppay.Extend(a, pblk.Items()).Must()
            pblk.Free(a)
            TestSendFrameMust(io, peer, TypePushPromise, FlagEndHeaders, 1, ppay.Items())
            ppay.Free(a)

            if !send_ack_first {
                // Before ACK: client must keep conn alive and RST stream 2 (or 200 succeeds).
                var p200: [1](name: str, val: str) = [(name: ":status", val: "200")]
                b200 := TestEncodeHeaders(a, mut enc, p200[..])
                TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders | FlagEndStream, 1, b200.Items())
                b200.Free(a)
                for _ in 0..8 {
                    rf := TestReadFrame(a, io, peer) else { break }
                    rf2 := rf else { break }
                    is_rst2 := rf2.kind == TypeRstStream && rf2.stream == 2
                    rf2.Free(a)
                    if is_rst2 {
                        var g := saw_expected.Lock(io).Must()
                        *g.Mut() = true
                        g.Unlock()
                        return
                    }
                }
            } else {
                // After ACK: client must GOAWAY(PROTOCOL_ERROR).
                for _ in 0..8 {
                    rf := TestReadFrame(a, io, peer) else { break }
                    rf2 := rf else { break }
                    var is_goaway_proto := false
                    if rf2.kind == TypeGoaway && rf2.payload.Len() >= 8 {
                        code := TestBeU32(rf2.payload.Items()[4..8])
                        is_goaway_proto = code == ProtocolError
                    }
                    rf2.Free(a)
                    if is_goaway_proto {
                        var g := saw_expected.Lock(io).Must()
                        *g.Mut() = true
                        g.Unlock()
                        return
                    }
                }
            }
            return
        }
    }
}

test "H2 client handles PUSH_PROMISE before vs after SETTINGS ACK" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()

    // Case 1: before ACK -> resets promised stream 2 and stream 1 still completes 200.
    (a: srv_conn1, b: cli_conn1) := pipe.ConnPair(h, 8192).Must()
    cli1_box := Conn.Client(h, io, cli_conn1, TestDefaultLimits()).Must()
    cli1 := cli1_box.Ref()
    var saw1 := Mutex[bool].New(false)
    scope s1 := io.Scope() {
        s1.CancelAfter(core.DurationOf(5_000_000_000))
        s1.go TestPeerPushPromise(io, h, &srv_conn1, false, &saw1)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/pp1")
        var hs: [0]TestField = []
        sr := cli1.TryOpen(io, &p, hs[..], true).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        res := cli1.Head(&ar, io, sr).Must()
        Assert(res.status == 200)
        cli1.Release(sr)
    }
    var g1 := saw1.Lock(io).Must()
    Assert(*g1.Ref())
    g1.Unlock()
    srv_conn1.Abandon()
    cli1_box.Take(a).Abandon()

    // Case 2: after ACK -> connection PROTOCOL_ERROR GOAWAY.
    (a: srv_conn2, b: cli_conn2) := pipe.ConnPair(h, 8192).Must()
    cli2_box := Conn.Client(h, io, cli_conn2, TestDefaultLimits()).Must()
    cli2 := cli2_box.Ref()
    var saw2 := Mutex[bool].New(false)
    scope s2 := io.Scope() {
        s2.CancelAfter(core.DurationOf(5_000_000_000))
        s2.go TestPeerPushPromise(io, h, &srv_conn2, true, &saw2)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/pp2")
        var hs: [0]TestField = []
        sr := cli2.TryOpen(io, &p, hs[..], true).Must().Must()
        var ar := Arena.New(h).Must()
        defer ar.Free()
        var head_failed := false
        var empty_fv: [0]fields.FieldView = []
        _ = cli2.Head(&ar, io, sr) else e {
            head_failed = true
            _ = e
            (status: 0, list: empty_fv[..], content_length: none)
        }
        Assert(head_failed)
        cli2.Release(sr)
    }
    var g2 := saw2.Lock(io).Must()
    Assert(*g2.Ref())
    g2.Unlock()
    srv_conn2.Abandon()
    cli2_box.Take(a).Abandon()
}

TestPeerGoawayUnprocessed(io: Io, h: &Heap, peer: &net.Conn) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, 65535, 16)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    var saw_1 := false
    var saw_3 := false
    for !saw_1 || !saw_3 {
        tf := TestReadFrame(a, io, peer).Must().Must()
        if tf.kind == TypeHeaders {
            if tf.stream == 1 { saw_1 = true }
            if tf.stream == 3 { saw_3 = true }
        }
        tf.Free(a)
    }
    // Send GOAWAY(last_stream: 1, code: EnhanceYourCalm) then finish stream 1.
    var gw: [17]u8 = [0; 17]
    gn := WriteGoaway(mut gw[..], 1, EnhanceYourCalm).Must()
    peer.WriteAll(io, gw[0..gn]).Must()

    var p200: [1](name: str, val: str) = [(name: ":status", val: "200")]
    b200 := TestEncodeHeaders(a, mut enc, p200[..])
    TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders | FlagEndStream, 1, b200.Items())
    b200.Free(a)
}

test "H2 client GOAWAY marks Unprocessed streams MuxClosed while processed finishes" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 8192).Must()
    cli_box := Conn.Client(h, io, cli_conn, TestDefaultLimits()).Must()
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestPeerGoawayUnprocessed(io, h, &srv_conn)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/gw")
        var hs: [0]TestField = []
        s1 := cli.TryOpen(io, &p, hs[..], true).Must().Must()
        s3 := cli.TryOpen(io, &p, hs[..], true).Must().Must()

        var ar := Arena.New(h).Must()
        defer ar.Free()

        var s3_unproc := false
        var empty_fv: [0]fields.FieldView = []
        _ = cli.Head(&ar, io, s3) else e {
            match e {
                net.MuxClosed(code, app: _, peer: _) {
                    Assert(code == u64(EnhanceYourCalm))
                    s3_unproc = true
                }
                _ {}
            }
            (status: 0, list: empty_fv[..], content_length: none)
        }
        Assert(s3_unproc)

        res1 := cli.Head(&ar, io, s1).Must()
        Assert(res1.status == 200)
        Assert(!cli.Usable())
        Assert(cli.GoawayLast().Must() == u32(1))
        Assert(Unprocessed(cli.StreamId(s3), u32(1)))
        Assert(!Unprocessed(cli.StreamId(s1), u32(1)))
        Assert(cli.TryOpen(io, &p, hs[..], true).Must().IsNone())

        cli.Release(s1)
        cli.Release(s3)
    }
    srv_conn.Abandon()
    cli_box.Take(a).Abandon()
}

TestPeerRetainedHeadCap(io: Io, h: &Heap, peer: &net.Conn) {
    a := h.Alloc()
    TestHandshakePeer(a, io, peer, 65535, 16)
    var enc := fields.Encoder.New(a, 4096, true).Must()
    defer enc.Free(a)
    var sids := HeapList[u32].New(a)
    defer sids.Free(a)
    for sids.Len() < 8 {
        tf := TestReadFrame(a, io, peer).Must().Must()
        if tf.kind == TypeHeaders {
            sids.Push(a, tf.stream).Must()
        }
        tf.Free(a)
    }
    // Send ~900 bytes of headers per stream across 8 streams (> 4 * 1024 cap) before client calls Head.
    var pairs: [7](name: str, val: str) = [
        (name: ":status", val: "200"),
        (name: "x-big-a", val: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
        (name: "x-big-b", val: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
        (name: "x-big-c", val: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"),
        (name: "x-big-d", val: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"),
        (name: "x-big-e", val: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"),
        (name: "x-big-f", val: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"),
    ]
    for i in 0..sids.Len() {
        blk := TestEncodeHeaders(a, mut enc, pairs[..])
        TestSendFrameMust(io, peer, TypeHeaders, FlagEndHeaders | FlagEndStream, sids.Items()[i], blk.Items())
        blk.Free(a)
    }
}

test "H2 client retained head cap enforces 4 * header_bytes budget" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 16384).Must()
    var lim := TestDefaultLimits()
    lim.header_bytes = 1024
    cli_box := Conn.Client(h, io, cli_conn, lim).Must()
    cli := cli_box.Ref()

    scope s := io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestPeerRetainedHeadCap(io, h, &srv_conn)
        p := fields.Pseudo(method: "GET", scheme: "http", authority: "example.com", path: "/cap")
        var hs: [0]TestField = []
        var refs: [8]StreamRef = [StreamRef(index: 0, gen_: 0); 8]
        for i in 0..8 {
            refs[i] = cli.TryOpen(io, &p, hs[..], true).Must().Must()
        }
        var ar := Arena.New(h).Must()
        defer ar.Free()
        var saw_too_large := false
        var empty_fv: [0]fields.FieldView = []
        for i in 0..8 {
            _ = cli.Head(&ar, io, refs[i]) else e {
                match e {
                    TooLarge { saw_too_large = true }
                    _ {}
                }
                (status: 0, list: empty_fv[..], content_length: none)
            }
            ar.Reset()
            cli.Release(refs[i])
        }
        Assert(saw_too_large)
    }
    srv_conn.Abandon()
    cli_box.Take(a).Abandon()
}
