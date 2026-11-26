// Why: ServeWith, ServeOn and ServeChan must serve HTTP/2 connections (over
// TLS ALPN "h2" or cleartext h2c) through the exact same DirectFn and RelayFn
// handlers, Response, and Link interfaces as HTTP/1.
//
// What: H2ResField, BuildH2OutHeaders, BuildH2ServerRequest, and ServeH2.
//
// How: ServeH2 runs h2.Conn.Writer and on-demand workers (up to h2_streams,
// each keeping one resettable Arena and exiting when idle with zero open
// streams) inside one scope alongside h2.Conn.ServeStep.

import std.core
import std.http.fields
import std.http.h2
import std.net

struct H2ResField {
    name: str
    value: str
}

impl fields.Field for H2ResField {
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

struct H2WorkerState {
    total: int
    busy: int
    pending: int
    max: int
}

struct H2WorkerCtx[C, T] {
    conn: &h2.Conn
    heap: &Heap
    o: &ServerOptions
    c: &C
    plain: ?DirectFn[C]
    relay: ?RelayFn[C, T]
    jobs: &Chan[h2.Request]
    pool: &Mutex[H2WorkerState]
    secure: bool
}

FormatIntIntoArena(a: &Arena, n: int): OutOfMemory!str from a {
    if n == 0 {
        return try a.Dup("0")
    }
    var buf: [32]u8 = [0; 32]
    var i := 32
    var v := n
    var neg := false
    if v < 0 {
        neg = true
        v = -v
    }
    for v > 0 {
        i -= 1
        buf[i] = Trunc[u8](0x30 + v % 10)
        v /= 10
    }
    if neg {
        i -= 1
        buf[i] = b'-'
    }
    s := str.From(buf[i..32]) else {
        return try a.Dup("0")
    }
    return try a.Dup(s)
}

IsH2SkippedResponseHeader(name: str): bool {
    return EqFold(name, "transfer-encoding") || EqFold(name, "connection") || EqFold(name, "keep-alive") || EqFold(name, "upgrade") || EqFold(name, "proxy-connection") || EqFold(name, "te")
}

BuildH2OutHeaders(a: &Arena, res: &Response, is_flush: bool, total_len: int): OutOfMemory![]H2ResField from a {
    var out := List[H2ResField].New(a)
    code := res.code
    bodyless := code < 200 || code == 204 || code == 304
    var user_len := false
    for h in res.headers.Items() {
        if !IsToken(h.name) {
            continue
        }
        if EqFold(h.name, "content-length") {
            if bodyless || is_flush || !res.head || user_len || ParseLength(h.value.AsBytes()) < 0 {
                continue
            }
            user_len = true
        } else if IsH2SkippedResponseHeader(h.name) {
            continue
        }
        nc := try a.Dup(h.name)
        vc := try a.Dup(h.value)
        try out.Push(H2ResField(name: nc, value: vc))
    }
    if !bodyless && !is_flush && !user_len {
        cl_str := try FormatIntIntoArena(a, total_len)
        try out.Push(H2ResField(name: "content-length", value: cl_str))
    }
    return out.Done()[..]
}

CopyJoinedCookie(a: &Arena, hb: &fields.FieldBlock): OutOfMemory!?str from a {
    cookie := "cookie".AsBytes()
    var count := 0
    var need := 0
    for f in hb.Iter() {
        if f.name.Len() == cookie.Len() && core.BytesEqual(f.name, 0, cookie) {
            if count > 0 {
                need += 2
            }
            need += f.value.Len()
            count += 1
        }
    }
    if count <= 1 {
        return none
    }
    buf := try a.Slice[u8](need)
    _ = fields.JoinCookies(hb, buf).Must()
    s := str.From(buf[..]) else {
        return none
    }
    return some(s)
}

BuildH2ServerRequest(a: &Arena, hb: &fields.FieldBlock, body: &HeapList[u8]): Error!Request from a {
    var method := ""
    var scheme := ""
    var authority := ""
    var target := ""
    var saw_host := false
    var headers := List[Header].New(a)
    joined_cookie := try CopyJoinedCookie(a, hb)
    var emitted_cookie := false
    for f in hb.Iter() {
        fn_ := str.From(f.name) else { fail BadRequest(offset: 0) }
        fv := str.From(f.value) else { fail BadRequest(offset: 0) }
        if fn_ == ":method" {
            method = try a.Dup(fv)
        } else if fn_ == ":scheme" {
            scheme = try a.Dup(fv)
        } else if fn_ == ":authority" {
            authority = try a.Dup(fv)
        } else if fn_ == ":path" {
            target = try a.Dup(fv)
        } else if fn_.Len() > 0 && fn_.AsBytes()[0] == b':' {
            fail BadRequest(offset: 0)
        } else if fn_ == "cookie" {
            if jc := joined_cookie {
                if !emitted_cookie {
                    try headers.Push(Header(name: "cookie", value: jc))
                    emitted_cookie = true
                }
            } else {
                nc := try a.Dup(fn_)
                vc := try a.Dup(fv)
                try headers.Push(Header(name: nc, value: vc))
            }
        } else {
            if fn_ == "host" {
                if authority.Len() > 0 {
                    continue
                }
                saw_host = true
            }
            nc := try a.Dup(fn_)
            vc := try a.Dup(fv)
            try headers.Push(Header(name: nc, value: vc))
        }
    }
    if scheme != "http" && scheme != "https" {
        fail BadRequest(offset: 0)
    }
    if authority.Len() > 0 || !saw_host {
        if authority.Len() > 0 {
            try headers.Push(Header(name: "host", value: authority))
        }
    }
    rt_target := try ParseTarget(target, method, 0, target.Len(), 0)
    bcopy := try a.Slice[u8](body.Len())
    if body.Len() > 0 {
        (mut bcopy[..]).CopyFrom(body.Items())
    }
    empty_params := List[Header].New(a).Done()[..]
    return Request(method: method, path: rt_target.path, query: rt_target.query, headers: headers.Done()[..], params: empty_params, body: bcopy[..])
}

SendH2Response(a: &Arena, io: Io, conn: &h2.Conn, s: h2.StreamRef, res: mut Response, is_head: bool, failed: bool) {
    if res.fl.failed || (res.fl.head && failed) {
        conn.Reset(s, h2.Cancel)
        return
    }
    if res.fl.head {
        if res.fl.mode == FlushNoBody {
            return
        }
        dn := res.data.buf.Len()
        tb := res.text.Str().AsBytes()
        tn := tb.Len()
        drem := if res.fl.dsent < dn { res.data.buf[res.fl.dsent..dn] } else { "".AsBytes() }
        trem := if res.fl.tsent < tn { tb[res.fl.tsent..tn] } else { "".AsBytes() }
        if drem.Len() > 0 {
            conn.Write(io, s, drem, trem.Len() == 0) else { return }
        }
        if trem.Len() > 0 {
            conn.Write(io, s, trem, true) else {}
        } else if drem.Len() == 0 {
            empty: [0]u8 = []
            conn.Write(io, s, empty[..], true) else {}
        }
        return
    }
    res.head = is_head
    if failed || res.code < 200 || res.code > 999 {
        res.code = 500
        res.headers.Clear()
        res.data.Clear()
        res.text.Clear()
    }
    bodyless := res.code < 200 || res.code == 204 || res.code == 304
    tb := res.text.Str().AsBytes()
    total := res.data.buf.Len() + tb.Len()
    send_body := !bodyless && !is_head && total > 0
    hs := BuildH2OutHeaders(a, &*res, false, total) else {
        var empty_hs: [0]H2ResField = []
        conn.Respond(io, s, 500, empty_hs[..], true) else {}
        return
    }
    conn.Respond(io, s, res.code, hs, !send_body) else e {
        match e {
            h2.BadField {
                var empty_hs: [0]H2ResField = []
                conn.Respond(io, s, 500, empty_hs[..], true) else {}
            }
            _ {}
        }
        return
    }
    if send_body {
        if res.data.buf.Len() > 0 {
            conn.Write(io, s, res.data.buf, tb.Len() == 0) else { return }
        }
        if tb.Len() > 0 {
            conn.Write(io, s, tb, true) else {}
        }
    }
}

HandleH2Request[C: Share, T: Send](wctx: &H2WorkerCtx[C, T], rh: &Heap into ch, ch: ?&Chan[T], a: &Arena, io: Io, job: h2.Request) {
    s := job.stream
    h2.Request(stream: _, head, body) := job
    req := BuildH2ServerRequest(a, &head, &body) else {
        var empty_hs: [0]H2ResField = []
        wctx.conn.Respond(io, s, 400, empty_hs[..], true) else {}
        wctx.conn.Done(s, head, body)
        return
    }
    is_head := req.method == "HEAD"
    var res := Response.New(a)
    res.head = is_head
    res.version = HttpVersion.Http2
    res.secure = wctx.secure
    link := Link(version: HttpVersion.Http2, cell: none, h3: none, h3_port: wctx.o.h3_port, h2: some(H2FlushTarget(conn: wctx.conn, stream: s)), io: io, rest: "".AsBytes(), pace: wctx.o.request_header)
    var failed := false
    CallHandler(wctx.c, rh, ch, wctx.plain, wctx.relay, a, io, &req, mut res, &link) else err {
        match err {
            Status(code) {
                if res.fl.head {
                    failed = true
                } else {
                    res.code = code
                    res.data.Clear()
                    res.text.Clear()
                }
            }
            _ {
                failed = true
            }
        }
    }
    res.AdvertiseH3(wctx.o.h3_port) else { failed = true }
    SendH2Response(a, io, wctx.conn, s, mut res, is_head, failed)
    wctx.conn.Done(s, head, body)
}

H2Worker[C: Share, T: Send](io: Io, wctx: &H2WorkerCtx[C, T], rh: &Heap into ch, ch: ?&Chan[T]) {
    var arena := Arena.New(wctx.heap) else {
        nc := io.NoCancel()
        var g := wctx.pool.Lock(nc).Must()
        g.Mut().total -= 1
        g.Unlock()
        return
    }
    defer arena.Free()
    nc := io.NoCancel()
    for {
        var exit_now := false
        var pg := wctx.pool.Lock(nc).Must()
        st := pg.Mut()
        if st.pending == 0 && !wctx.jobs.Ready() && wctx.conn.OpenStreams() == 0 {
            st.total -= 1
            exit_now = true
        }
        pg.Unlock()
        if exit_now {
            return
        }
        rjob := wctx.jobs.Recv(io) else {
            var pg2 := wctx.pool.Lock(nc).Must()
            pg2.Mut().total -= 1
            pg2.Unlock()
            return
        }
        var pg3 := wctx.pool.Lock(nc).Must()
        st3 := pg3.Mut()
        st3.pending -= 1
        st3.busy += 1
        pg3.Unlock()
        HandleH2Request(wctx, rh, ch, &arena, io, rjob)
        arena.Reset()
        var pg4 := wctx.pool.Lock(nc).Must()
        pg4.Mut().busy -= 1
        pg4.Unlock()
    }
}

struct H2ServeCtx {
    conn: &h2.Conn
    jobs: &Chan[h2.Request]
    spawn_ch: &Chan[int]
}

OnH2Request(sctx: &H2ServeCtx, conn: &h2.Conn, r: h2.Request into sctx) {
    unsent := sctx.jobs.TrySend(r).Must()
    if drop := unsent {
        h2.Request(stream: ds, head: dh, body: db) := drop
        conn.Reset(ds, h2.RefusedStream)
        conn.Done(ds, dh, db)
        return
    }
    _ = sctx.spawn_ch.TrySend(1) else {}
}

H2ReaderTask(io: Io, sctx: &H2ServeCtx) {
    sctx.conn.ServeInto(io, sctx, OnH2Request) else {}
    sctx.jobs.Close()
    sctx.spawn_ch.Close()
}

H2WriterTask(io: Io, conn: &h2.Conn) {
    conn.Writer(io) else {}
}

ServeH2[C: Share, T: Send](conn: net.Conn, heap: &Heap into conn, io: Io, o: &ServerOptions, c: &C, rh: &Heap into ch, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T], pre: []u8, secure: bool) {
    streams := if o.h2_streams < 1 { 1 } else { o.h2_streams }
    lim := h2.Limits(
        streams: streams,
        window: 65535,
        header_bytes: o.header_bytes,
        body_bytes: o.body_bytes,
        write_limit: o.request_header,
        close_limit: core.DurationOf(5_000_000_000),
        idle_limit: o.keep_alive,
    )
    h2c_box := h2.Conn.Server(heap, io, conn, lim, pre) else {
        conn.Abandon()
        return
    }
    a := heap.Alloc()
    var jobs := Chan[h2.Request].New(a, streams * 2) else {
        h2c_box.Take(a).Abandon()
        return
    }
    var spawn_ch := Chan[int].New(a, streams * 4) else {
        jobs.FreeEmpty(a)
        h2c_box.Take(a).Abandon()
        return
    }
    pool := Mutex[H2WorkerState].New(H2WorkerState(total: 0, busy: 0, pending: 0, max: streams))
    wctx := H2WorkerCtx[C, T](conn: h2c_box.Ref(), heap: heap, o: o, c: c, plain: plain, relay: relay, jobs: &jobs, pool: &pool, secure: secure)
    sctx := H2ServeCtx(conn: h2c_box.Ref(), jobs: &jobs, spawn_ch: &spawn_ch)
    scope hs := io.Scope() {
        hs.go H2WriterTask(io, h2c_box.Ref())
        hs.go H2ReaderTask(io, &sctx)
        nc := io.NoCancel()
        for {
            _ = spawn_ch.Recv(io) else {
                break
            }
            var spawn := false
            var pg := pool.Lock(nc).Must()
            st := pg.Mut()
            st.pending += 1
            if st.total - st.busy < st.pending && st.total < st.max {
                st.total += 1
                spawn = true
            }
            pg.Unlock()
            if spawn {
                hs.go H2Worker(io, &wctx, rh, ch)
            }
        }
    }
    for {
        left := jobs.Recv(io.NoCancel()) else {
            break
        }
        h2.Request(stream: ls, head: lh, body: lb) := left
        h2c_box.Ref().Done(ls, lh, lb)
    }
    spawn_ch.Free(a)
    jobs.FreeEmpty(a)
    h2c := h2c_box.Take(a)
    h2c.Close(io.NoCancel()) else {
        h2c.Abandon()
    }
}
