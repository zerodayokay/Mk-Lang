// Why: server request admission, HPACK dynamic-table synchronization across
// 431 errors, inline 413/501/100 responses, trailers, 1-byte dribble timeouts,
// and response validation must work end-to-end without deadlock over bounded
// pipes.
//
// What: server integration tests in std.http.h2 over pipe.ConnPair.
//
// How: drives Conn.Server, Serve, Writer, Respond, Write, Done, and Close
// against a scripted client peer over 4 KiB..64 KiB bounded pipes.

import std.core
import std.http.fields
import std.net
import std.net.pipe
import std.rt

struct SrvCapture {
    count: int
    last_method: [16]u8
    last_method_len: int
    last_body_len: int
}

struct SrvHold {
    conn: &Conn
    reqs: int
}

TestHoldDeliver(st: &Mutex[SrvCapture], conn: &Conn, r: Request) {
    s := r.stream
    Request(stream: _, head, body) := r
    nc := core.IoMasked(rt.NativeIo())
    var g := st.Lock(nc).Must()
    g.Mut().count += 1
    for f in head.Iter() {
        if CoreBytesEq(f.name, ":method".AsBytes()) {
            n := if f.value.Len() < 16 { f.value.Len() } else { 16 }
            (mut g.Mut().last_method[0..n]).CopyFrom(f.value[0..n])
            g.Mut().last_method_len = n
        }
    }
    g.Mut().last_body_len = body.Len()
    g.Unlock()
    // Echo body and test connection-specific header filtering + lowercasing.
    var hs: [2]TestField = [
        TestField(name: "X-Custom", value: "val"),
        TestField(name: "Connection", value: "keep-alive"),
    ]
    conn.Respond(nc, s, 200, hs[..], body.Len() == 0) else {
        conn.Done(s, head, body)
        return
    }
    if body.Len() > 0 {
        conn.Write(nc, s, body.Items(), true) else {}
    }
    conn.Done(s, head, body)
}

TestServerCapTask(io: Io, conn: &Conn, st: &Mutex[SrvCapture]) {
    conn.Serve(io, st, TestHoldDeliver) else {}
}

TestEncodeHeaders(a: &Alloc, enc: mut fields.Encoder, pairs: [](name: str, val: str)): HeapList[u8] from a {
    var tmp := HeapList[u8].New(a)
    for _ in 0..4096 {
        tmp.Push(a, 0).Must()
    }
    var off := enc.Start(tmp.ItemsMut()).Must()
    for p in pairs {
        off += enc.Write(mut tmp.ItemsMut()[off..], p.name.AsBytes(), p.val.AsBytes(), fields.Mode.Index).Must()
    }
    var out := HeapList[u8].New(a)
    out.Extend(a, tmp.Items()[0..off]).Must()
    tmp.Free(a)
    return out
}

TestDecodeStatusAndBody(a: &Alloc, io: Io, peer: &net.Conn, dec: mut fields.Decoder, target_stream: u32): (status: int, body_len: int) {
    var status := 0
    var body_len := 0
    for {
        tf := TestReadFrame(a, io, peer).Must().Must()
        if tf.stream == target_stream && tf.kind == TypeHeaders {
            var blk := fields.FieldBlock.New(a)
            _ = dec.Decode(a, mut blk, tf.payload.Items()).Must()
            resp := fields.CheckResponse(&blk).Must()
            // Verify Connection header was stripped and X-Custom became lowercase.
            Assert(!TestHasHeader(&blk, "connection"))
            if resp.status >= 200 {
                Assert(TestHasHeader(&blk, "x-custom"))
                status = resp.status
            }
            blk.Free(a)
            if (tf.flags & 0x01) != 0 && status >= 200 {
                tf.Free(a)
                return (status: status, body_len: body_len)
            }
        } else if tf.stream == target_stream && tf.kind == TypeData {
            body_len += tf.payload.Len()
            if (tf.flags & 0x01) != 0 {
                tf.Free(a)
                return (status: status, body_len: body_len)
            }
        }
        tf.Free(a)
    }
}

test "H2 server handles POST + trailers and strips hop-by-hop headers over 4 KiB pipe" (sys: &System) {
    a := sys.heap.Alloc()
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 4096).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, TestDefaultLimits(), empty[..]).Must()
    cap := Mutex[SrvCapture].New(SrvCapture(count: 0, last_method: [0; 16], last_method_len: 0, last_body_len: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerCapTask(sys.io, srv_box.Ref(), &cap)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        var enc := fields.Encoder.New(a, fields.DefaultTableBytes, true).Must()
        defer enc.Free(a)
        var req_pairs: [5](name: str, val: str) = [
            (name: ":method", val: "POST"),
            (name: ":scheme", val: "https"),
            (name: ":authority", val: "example.com"),
            (name: ":path", val: "/post"),
            (name: "content-length", val: "5"),
        ]
        var hb := TestEncodeHeaders(a, mut enc, req_pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x04, 1, hb.Items()).Must()
        hb.Free(a)
        TestSendRawFrame(sys.io, &cli_c, TypeData, 0x00, 1, "hello".AsBytes()).Must()
        var tr_pairs: [1](name: str, val: str) = [(name: "x-trailer", val: "done")]
        var tb := TestEncodeHeaders(a, mut enc, tr_pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x05, 1, tb.Items()).Must()
        tb.Free(a)
        var dec := fields.Decoder.New(a, fields.DefaultTableBytes, 16384).Must()
        defer dec.Free(a)
        (status, body_len) := TestDecodeStatusAndBody(a, sys.io, &cli_c, mut dec, 1)
        Assert(status == 200 && body_len == 5)
        cli_c.Close(sys.io) else { cli_c.Abandon() }
    }
    var sc := srv_box.Take(a)
    sc.Close(sys.io) else { sc.Abandon() }
    res := cap.Into()
    Assert(res.count == 1 && res.last_body_len == 5)
}

test "H2 server returns inline 501 CONNECT, 431 (preserving HPACK), 413, and 100-continue" (sys: &System) {
    a := sys.heap.Alloc()
    var lim := TestDefaultLimits()
    lim.header_bytes = 1024
    lim.body_bytes = 512
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 16384).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, lim, empty[..]).Must()
    cap := Mutex[SrvCapture].New(SrvCapture(count: 0, last_method: [0; 16], last_method_len: 0, last_body_len: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerCapTask(sys.io, srv_box.Ref(), &cap)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        var enc := fields.Encoder.New(a, fields.DefaultTableBytes, true).Must()
        defer enc.Free(a)
        var dec := fields.Decoder.New(a, fields.DefaultTableBytes, 16384).Must()
        defer dec.Free(a)

        // 1. CONNECT -> 501 inline without hitting deliver
        var conn_pairs: [2](name: str, val: str) = [
            (name: ":method", val: "CONNECT"),
            (name: ":authority", val: "example.com:443"),
        ]
        var c_hb := TestEncodeHeaders(a, mut enc, conn_pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x05, 1, c_hb.Items()).Must()
        c_hb.Free(a)
        var got_501 := false
        for !got_501 {
            tf := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if tf.stream == 1 && tf.kind == TypeHeaders {
                var blk := fields.FieldBlock.New(a)
                _ = dec.Decode(a, mut blk, tf.payload.Items()).Must()
                Assert(fields.CheckResponse(&blk).Must().status == 501)
                blk.Free(a)
                got_501 = true
            }
            tf.Free(a)
        }

        // 2. Oversized header block (> 1024 decoded bytes) inserting dynamic entry x-keep: sync
        var big_val: [1100]u8 = [0x61; 1100]
        var big_pairs: [5](name: str, val: str) = [
            (name: ":method", val: "GET"),
            (name: ":scheme", val: "https"),
            (name: ":authority", val: "example.com"),
            (name: ":path", val: "/big"),
            (name: "x-keep", val: str.From(big_val[..]).Must()),
        ]
        var b_hb := TestEncodeHeaders(a, mut enc, big_pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x05, 3, b_hb.Items()).Must()
        b_hb.Free(a)
        var got_431 := false
        for !got_431 {
            tf := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if tf.stream == 3 && tf.kind == TypeHeaders {
                var blk := fields.FieldBlock.New(a)
                _ = dec.Decode(a, mut blk, tf.payload.Items()).Must()
                Assert(fields.CheckResponse(&blk).Must().status == 431)
                blk.Free(a)
                got_431 = true
            }
            tf.Free(a)
        }

        // 3. Subsequent small GET + Expect: 100-continue using dynamic-table friendly encoder
        var ok_pairs: [5](name: str, val: str) = [
            (name: ":method", val: "POST"),
            (name: ":scheme", val: "https"),
            (name: ":authority", val: "example.com"),
            (name: ":path", val: "/exp"),
            (name: "expect", val: "100-continue"),
        ]
        var o_hb := TestEncodeHeaders(a, mut enc, ok_pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x04, 5, o_hb.Items()).Must()
        o_hb.Free(a)
        var got_100 := false
        for !got_100 {
            tf := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if tf.stream == 5 && tf.kind == TypeHeaders {
                var blk := fields.FieldBlock.New(a)
                _ = dec.Decode(a, mut blk, tf.payload.Items()).Must()
                Assert(fields.CheckResponse(&blk).Must().status == 100)
                blk.Free(a)
                got_100 = true
            }
            tf.Free(a)
        }
        // Stream > 512 bytes on stream 5 -> inline 413 + RST_STREAM(NO_ERROR)
        var over: [600]u8 = [0x42; 600]
        TestSendRawFrame(sys.io, &cli_c, TypeData, 0x01, 5, over[..]).Must()
        var got_413 := false
        for !got_413 {
            tf := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if tf.stream == 5 && tf.kind == TypeHeaders {
                var blk := fields.FieldBlock.New(a)
                _ = dec.Decode(a, mut blk, tf.payload.Items()).Must()
                Assert(fields.CheckResponse(&blk).Must().status == 413)
                blk.Free(a)
                got_413 = true
            }
            tf.Free(a)
        }
        cli_c.Close(sys.io) else { cli_c.Abandon() }
    }
    srv_box.Take(a).Abandon()
    // Only valid non-inline-error requests reach deliver (none here since 1=501, 3=431, 5=413).
    Assert(cap.Into().count == 0)
}

test "H2 server admits 4 concurrent POSTs FIFO over bounded 4 KiB pipe without deadlock" (sys: &System) {
    a := sys.heap.Alloc()
    var lim := TestDefaultLimits()
    lim.body_bytes = 2048
    lim.window = 4096
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 4096).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, lim, empty[..]).Must()
    cap := Mutex[SrvCapture].New(SrvCapture(count: 0, last_method: [0; 16], last_method_len: 0, last_body_len: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(10_000_000_000))
        s.go TestServerCapTask(sys.io, srv_box.Ref(), &cap)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        var enc := fields.Encoder.New(a, fields.DefaultTableBytes, true).Must()
        defer enc.Free(a)
        var dec := fields.Decoder.New(a, fields.DefaultTableBytes, 16384).Must()
        defer dec.Free(a)
        // Open 4 streams each declaring 1200 bytes (total 4800 > body_bytes 2048).
        for i in 0..4 {
            sid := Trunc[u32](1 + i * 2)
            var pairs: [5](name: str, val: str) = [
                (name: ":method", val: "POST"),
                (name: ":scheme", val: "https"),
                (name: ":authority", val: "example.com"),
                (name: ":path", val: "/fifo"),
                (name: "content-length", val: "1200"),
            ]
            var hb := TestEncodeHeaders(a, mut enc, pairs[..])
            TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x04, sid, hb.Items()).Must()
            hb.Free(a)
        }
        var payload: [1200]u8 = [0x5A; 1200]
        // Stream 1 finishes first, releasing admission for queued streams 3, 5, 7.
        for i in 0..4 {
            sid := Trunc[u32](1 + i * 2)
            TestSendRawFrame(sys.io, &cli_c, TypeData, 0x01, sid, payload[..]).Must()
            (status: st_code, body_len: b_len) := TestDecodeStatusAndBody(a, sys.io, &cli_c, mut dec, sid)
            Assert(st_code == 200 && b_len == 1200)
        }
        cli_c.Close(sys.io) else { cli_c.Abandon() }
    }
    srv_box.Take(a).Abandon()
    Assert(cap.Into().count == 4)
}

test "H2 server resets 1-byte dribble stream on ProgressFloor timeout" (sys: &System) {
    a := sys.heap.Alloc()
    var lim := TestDefaultLimits()
    lim.write_limit = core.DurationOf(50_000_000)
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 16384).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, lim, empty[..]).Must()
    cap := Mutex[SrvCapture].New(SrvCapture(count: 0, last_method: [0; 16], last_method_len: 0, last_body_len: 0))
    scope s := sys.io.Scope() {
        s.CancelAfter(core.DurationOf(5_000_000_000))
        s.go TestServerCapTask(sys.io, srv_box.Ref(), &cap)
        s.go TestWriterTask(sys.io, srv_box.Ref())
        TestSendClientPreface(sys.io, &cli_c)
        var enc := fields.Encoder.New(a, fields.DefaultTableBytes, true).Must()
        defer enc.Free(a)
        var pairs: [4](name: str, val: str) = [
            (name: ":method", val: "POST"),
            (name: ":scheme", val: "https"),
            (name: ":authority", val: "example.com"),
            (name: ":path", val: "/dribble"),
        ]
        var hb := TestEncodeHeaders(a, mut enc, pairs[..])
        TestSendRawFrame(sys.io, &cli_c, TypeHeaders, 0x04, 1, hb.Items()).Must()
        hb.Free(a)
        var one: [1]u8 = [0x31]
        TestSendRawFrame(sys.io, &cli_c, TypeData, 0x00, 1, one[..]).Must()
        var got_rst := false
        for !got_rst {
            tf := TestReadFrame(a, sys.io, &cli_c).Must().Must()
            if tf.kind == TypeRstStream && tf.stream == 1 {
                got_rst = true
            }
            tf.Free(a)
        }
        Assert(got_rst)
        cli_c.Close(sys.io) else { cli_c.Abandon() }
    }
    srv_box.Take(a).Abandon()
}

test "H2 Respond rejects BadField without queuing frame" (sys: &System) {
    a := sys.heap.Alloc()
    (a: srv_c, b: cli_c) := pipe.ConnPair(sys.heap, 16384).Must()
    var empty: [0]u8 = []
    srv_box := Conn.Server(sys.heap, sys.io, srv_c, TestDefaultLimits(), empty[..]).Must()
    var bad: [1]TestField = [TestField(name: "x-bad\r\n", value: "v")]
    match srv_box.Ref().Respond(sys.io, StreamRef(index: 0, gen_: 1), 200, bad[..], true) {
        err(BadField) {}
        _ { Assert(false) }
    }
    // Stale StreamRef returns 0 from StreamId and Reset is a no-op.
    Assert(srv_box.Ref().StreamId(StreamRef(index: 99, gen_: 1)) == 0)
    srv_box.Ref().Reset(StreamRef(index: 99, gen_: 1), Cancel)
    cli_c.Abandon()
    srv_box.Take(a).Abandon()
}
