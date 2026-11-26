// Why: the HTTP server can serve QUIC and TCP on the same address and port.
// What: the HTTP/3 listener and per-stream handler.
// How: a connection owns a session; each accepted stream uses the shared
// HTTP request and response conversion and runs in the connection's scope.

import std.core
import std.http.fields
import std.http.h3
import std.net
import std.sync

RunH3Listener(l: &net.MuxListener, io: Io) {
    l.Run(io) else {}
}

AcceptH3[C: Share, T: Send](l: &net.MuxListener, h: &Heap, rh: &Heap into ch, io: Io, o: &ServerOptions, sem: &sync.Semaphore, c: &C, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T]) {
    scope connections := io.Scope() {
        defer connections.Cancel()
        for {
            m := l.Accept(io) else { return }
            sem.Acquire(io, 1) else {
                m.Abandon()
                return
            }
            connections.go ServeH3(m, h, rh, io, o, sem, c, ch, plain, relay)
        }
    }
}

ServeH3[C: Share, T: Send](m: net.Mux, h: &Heap, rh: &Heap into ch, io: Io, o: &ServerOptions, sem: &sync.Semaphore, c: &C, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T]) {
    defer sem.Release(1)
    sb := h3.Session.ServerWithQpack(h, io, &m, o.header_bytes, 4096, 16) else {
        m.Abandon()
        return
    }
    sess := sb.Ref()
    active := sync.Semaphore.New(if o.h2_streams > 0 { o.h2_streams } else { 1 })
    budget := sync.Semaphore.New(if o.body_bytes > 0 { o.body_bytes } else { 1 })
    var last: i64 = 0
    var close_code := h3.H3NoError
    scope requests := io.Scope() {
        defer requests.Cancel()
        for {
            active.Acquire(io, 1) else { break }
            ex := h3.Exchange.Accept(sess, io) else e {
                active.Release(1)
                match e {
                    h3.Violation(code, stream: _) { close_code = code }
                    _ {}
                }
                break
            }
            last = ex.Id() + 4
            requests.go ServeH3Request(sess, ex, h, rh, io, o, &active, &budget, c, ch, plain, relay)
        }
    }
    nc := io.NoCancel()
    scope closing := nc.Scope() {
        closing.CancelAfter(o.request_header)
        if close_code == h3.H3NoError {
            sess.SendGoaway(nc, last) else {}
        }
    }
    sb.Take(h.Alloc()).Close(h.Alloc())
    scope closing := nc.Scope() {
        closing.CancelAfter(o.request_header)
        m.Close(nc, close_code) else { m.Abandon() }
    }
}

EmptyH3Response(ex: &h3.Exchange, io: Io, code: int) {
    headers: [0]H2ResField = []
    ex.Respond(io, code, headers[..], true) else { ex.Cancel() }
}

ReadH3RequestBody(ex: &h3.Exchange, a: &Alloc, io: Io, o: &ServerOptions, budget: &sync.Semaphore, reserved: mut int, body: mut HeapList[u8]): Error!void {
    var buf: [16384]u8 = [0; 16384]
    for {
        var n := 0
        var err: ?h3.Error = none
        scope read := io.Scope() {
            read.CancelAfter(o.request_header)
            n = ex.Read(io, mut buf[..]) else e {
                err = some(e)
                0
            }
        }
        if e := err {
            fail MapH3Error(e)
        }
        if n == 0 {
            return
        }
        if n > o.body_bytes - body.Len() {
            fail TooLarge
        }
        if *reserved == 0 {
            // Unknown-length bodies wait for admission only once DATA
            // starts, so bodyless requests take no body permit.
            try budget.Acquire(io, o.body_bytes)
            *reserved = o.body_bytes
        }
        try body.Extend(a, buf[0..n])
    }
}

ServeH3Request[C: Share, T: Send](sess: &h3.Session, ex: h3.Exchange, h: &Heap, rh: &Heap into ch, io: Io, o: &ServerOptions, sem: &sync.Semaphore, budget: &sync.Semaphore, c: &C, ch: ?&Chan[T], plain: ?DirectFn[C], relay: ?RelayFn[C, T]) {
    defer sem.Release(1)
    defer ex.Release(sess)
    a := h.Alloc()
    var head := fields.FieldBlock.New(a)
    defer head.Free(a)
    var head_ok := false
    scope read := io.Scope() {
        read.CancelAfter(o.request_header)
        ex.RequestHead(io, mut head) else e {
            match e {
                h3.TooLarge {
                    ex.StopReading(h3.H3NoError)
                    EmptyH3Response(&ex, io, 431)
                }
                _ { ex.Cancel() }
            }
            return
        }
        head_ok = true
    }
    if !head_ok {
        return
    }
    var expect := false
    var size := 0
    for f in head.Iter() {
        name := str.From(f.name) else { continue }
        if name == ":method" && core.BytesEqual(f.value, 0, "CONNECT".AsBytes()) && f.value.Len() == 7 {
            ex.StopReading(h3.H3NoError)
            EmptyH3Response(&ex, io, 501)
            return
        }
        if name == "expect" {
            value := str.From(f.value) else { "" }
            expect = expect || EqFold(value, "100-continue")
        }
        if name == "content-length" {
            size = ParseLength(f.value)
            if size > o.body_bytes {
                ex.StopReading(h3.H3NoError)
                EmptyH3Response(&ex, io, 413)
                return
            }
        }
    }
    var reserved := 0
    defer {
        if reserved > 0 {
            budget.Release(reserved)
        }
    }
    if size > 0 {
        budget.Acquire(io, size) else {
            ex.Cancel()
            return
        }
        reserved = size
    }
    if expect {
        EmptyH3Response(&ex, io, 100)
    }
    var body := HeapList[u8].New(a)
    defer body.Free(a)
    ReadH3RequestBody(&ex, a, io, o, budget, mut reserved, mut body) else e {
        match e {
            TooLarge {
                ex.StopReading(h3.H3NoError)
                EmptyH3Response(&ex, io, 413)
            }
            _ { ex.Cancel() }
        }
        return
    }
    arena := Arena.New(h) else {
        EmptyH3Response(&ex, io, 500)
        return
    }
    defer arena.Free()
    req := BuildH2ServerRequest(&arena, &head, &body) else {
        EmptyH3Response(&ex, io, 400)
        return
    }
    var res := Response.New(&arena)
    res.head = req.method == "HEAD"
    res.version = HttpVersion.Http3
    res.secure = true
    link := Link(version: HttpVersion.Http3, cell: none, h2: none, h3: some(&ex), io: io, rest: "".AsBytes(), pace: o.request_header, h3_port: 0)
    var failed := false
    CallHandler(c, rh, ch, plain, relay, &arena, io, &req, mut res, &link) else e {
        match e {
            Status(code) {
                if !res.fl.head {
                    res.code = code
                    res.data.Clear()
                    res.text.Clear()
                } else {
                    failed = true
                }
            }
            _ { failed = true }
        }
    }
    scope send := io.Scope() {
        send.CancelAfter(o.request_header)
        if failed && res.fl.head || res.fl.failed {
            ex.Cancel()
        } else {
            if failed || res.code < 200 || res.code > 999 {
                res.Clear(500)
            }
            res.head = req.method == "HEAD"
            res.SendH3(io, &ex, true) else { ex.Cancel() }
        }
    }
}

// SendH3 writes the head if needed and then the unsent body, ending at FIN.
Response.SendH3(mut self, io: Io, ex: &h3.Exchange, last: bool): Error!void {
    if !self.fl.head {
        if self.code < 200 || self.code > 999 {
            fail Status(code: self.code)
        }
        no_body := self.head || self.code == 204 || self.code == 304
        size := self.data.buf.Len() + self.text.Str().Len()
        hs := try BuildH2OutHeaders(self.arena, &*self, !last, size)
        var normalized := List[H2ResField].New(self.arena)
        for f in &hs {
            name := LowerH3Name(self.arena, f.name, f.value) else {
                EmptyH3Response(ex, io, 500)
                self.fl.head = true
                self.fl.mode = FlushNoBody
                return
            }
            try normalized.Push(H2ResField(name: name, value: f.value))
        }
        end := no_body || last && size == 0
        ex.Respond(io, self.code, normalized.Done()[..], end) else e {
            match e {
                h3.SendTooLarge {
                    EmptyH3Response(ex, io, 500)
                    self.fl.head = true
                    self.fl.mode = FlushNoBody
                    return
                }
                _ { fail MapH3Error(e) }
            }
        }
        self.fl.head = true
        self.fl.mode = if end { FlushNoBody } else { FlushChunked }
    }
    if self.fl.mode == FlushNoBody {
        return
    }
    dn := self.data.buf.Len()
    tb := self.text.Str().AsBytes()
    if self.fl.dsent < dn {
        ex.Write(io, self.data.buf[self.fl.dsent..dn], false) else e { fail MapH3Error(e) }
        self.fl.dsent = dn
    }
    if self.fl.tsent < tb.Len() || last {
        ex.Write(io, tb[self.fl.tsent..], last) else e { fail MapH3Error(e) }
        self.fl.tsent = tb.Len()
    }
}
