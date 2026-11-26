// Why: the public HTTP client reports protocol and transport failures.
// What: request dispatch and streaming response bodies over a QUIC slot.
// How: only pre-request failures and explicit rejections can change the
// transport. An arena cell owns the HTTP/3 exchange until Close or Abandon.

import std.compress
import std.core
import std.http.fields
import std.http.h3
import std.net
import std.task
import std.time
import std.url

MapH3Error(e: h3.Error): Error {
    match e {
        h3.Violation(code, stream: _) { return Protocol(code: code) }
        h3.TooLarge { return TooLarge }
        h3.SendTooLarge { return TooLarge }
        Canceled { return Canceled }
        Closed { return Closed }
        OutOfMemory { return OutOfMemory }
        time.TimedOut { return time.TimedOut }
        net.AddrInUse { return net.AddrInUse }
        net.InvalidAddress { return net.InvalidAddress }
        net.MuxClosed(code, app, peer) { return net.MuxClosed(code: code, app: app, peer: peer) }
        net.Os(code) { return net.Os(code: code) }
        net.Refused { return net.Refused }
        net.Reset { return net.Reset }
        net.SecureFailed(why) { return net.SecureFailed(why: why) }
        net.StreamReset(code) { return net.StreamReset(code: code) }
        net.Unreachable { return net.Unreachable }
        net.Unsupported { return net.Unsupported }
        _ { return Closed }
    }
}

struct H3Body {
    stream: ?h3.Exchange
    index: int
    eof: bool
    failed: bool
    content_length: ?i64
    coded: bool
}

H3BodyFill(m: &Mutex[H3Body], io: Io, dst: mut []u8): ReadError!int {
    if dst.Len() == 0 {
        return 0
    }
    var g := m.Lock(io.NoCancel()).Must()
    defer g.Unlock()
    if g.Ref().failed {
        fail ReadError
    }
    if g.Ref().eof {
        return 0
    }
    if ex := &g.Ref().stream {
        n := ex.Read(io, dst) else {
            g.Mut().failed = true
            fail ReadError
        }
        g.Mut().eof = n == 0
        return n
    }
    fail ReadError
}

const H3Reads = core.ReadPort[Mutex[H3Body]].New(H3BodyFill)

SettleH3(m: &Mutex[H3Body], io: Io): ReadError!void {
    var buf: [512]u8 = [0; 512]
    var total := 0
    for total < SettleMax {
        n := try H3BodyFill(m, io, mut buf[..])
        if n == 0 {
            return
        }
        total += n
    }
}

H3BodyOver(m: &Mutex[H3Body], io: Io, limit: i64): bool {
    g := m.Lock(io.NoCancel()).Must()
    defer g.Unlock()
    if !g.Ref().coded {
        if len := g.Ref().content_length {
            return len > limit
        }
    }
    return false
}

Client.ReleaseH3(&self, m: &Mutex[H3Body], io: Io, abandon: bool): bool {
    nc := io.NoCancel()
    var g := m.Lock(nc).Must()
    failed := g.Ref().failed
    reset := abandon || failed || !g.Ref().eof
    idx := g.Ref().index
    stream := Replace(mut g.Mut().stream, none)
    g.Unlock()
    if ex := stream {
        pg := self.h3_pool.Lock(nc).Must()
        conn := H3ConnRef((*pg.Ref())[idx].conn)
        pg.Unlock()
        if reset {
            ex.Cancel()
        }
        ex.Release(conn.session.Ref())
        p := self.H3PutBack(nc, idx, false, false)
        if abandon {
            DropH3(self.heap.Alloc(), p)
        } else {
            CloseH3(self.heap.Alloc(), p, nc, self.limits.close, h3.H3NoError)
        }
    }
    return failed
}

H3ReadPort(a: &Arena into m, io: Io, m: &Mutex[H3Body] into a, dec: ?compress.Content): OutOfMemory!mut Reader from a {
    b := try a.Slice[u8](0)
    r := try a.Put((&H3Reads).Reader(io, m, b))
    if coding := dec {
        buf := try a.Slice[u8](4096)
        decoded := try coding.wrap(a, r, buf)
        return try a.Put(decoded)
    }
    return r
}

// H3Upload streams the request and preserves a response when its peer
// stops the upload (RFC 9114 4.1).
H3Upload(ex: &h3.Exchange, io: Io, body: []u8, st: mut Stream): Error!void {
    if r := mut st.r {
        var buf: [16384]u8 = [0; 16384]
        var sent: i64 = 0
        for {
            var want := buf.Len()
            if n := st.len {
                left := n - sent
                if left == 0 {
                    ex.Write(io, buf[0..0], true) else e { fail MapH3Error(e) }
                    return
                }
                if left < i64(want) {
                    want = int(left)
                }
            }
            n := (*r).Read(mut buf[0..want]) else {
                try CancelCheck(io)
                fail ReadError
            }
            if n == 0 && st.len.IsSome() {
                fail ReadError
            }
            if n > 0 {
                st.took = true
            }
            if st.len.IsSome() {
                sent += i64(n)
            }
            ex.Write(io, buf[0..n], n == 0) else e { fail MapH3Error(e) }
            if n == 0 {
                return
            }
        }
    }
    if body.Len() > 0 {
        ex.Write(io, body, true) else e { fail MapH3Error(e) }
    }
}

Client.H3Head(&self, a: &Arena, io: Io, ex: &h3.Exchange, method: str, body: []u8, st: mut Stream): Error!(status: int, headers: []Header, length: ?i64, coding: ?compress.Content) from a, self {
    H3Upload(ex, io, body, st) else e {
        match e {
            net.StreamReset {}
            _ { fail e }
        }
    }
    var status := 0
    var len: ?i64 = none
    var headers := List[Header].New(a)
    var err: ?Error = none
    empty_h3_fields: [0]fields.FieldView = []
    scope reading := io.Scope() {
        reading.CancelAfter(self.limits.response_header)
        head := ex.Head(a, io) else e {
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            err = some(if task.LimitHit(&reading, canceled) { time.TimedOut } else { MapH3Error(e) })
            (status: 0, headers: empty_h3_fields[..], content_length: none)
        }
        status = head.status
        len = head.content_length
        for f in head.headers {
            headers.Push(Header(name: f.name, value: f.value)) else e {
                err = some(e)
                break
            }
        }
    }
    if e := err {
        fail e
    }
    list := headers.Done()[..]
    empty_len := if l := len { l == 0 } else { false }
    dec := if method == "HEAD" || status == 204 || status == 304 || empty_len { none } else { Decoder(self.limits.decoders, list) }
    final := if dec.IsSome() { try WithoutFraming(a, list) } else { list }
    return (status: status, headers: final, length: len, coding: dec)
}

LowerH3Name(a: &Arena, name: str, value: str): Error!str from a {
    b := try a.Slice[u8](name.Len())
    if !fields.LowerName(name.AsBytes(), value.AsBytes(), mut b[..]) {
        fail BadRequest(offset: 0)
    }
    return str.From(b[..]) else { fail BadRequest(offset: 0) }
}

BuildH3Request(a: &Arena, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, decoders: []compress.Content, st: &Stream): Error!(pseudo: fields.Pseudo, list: []H2ReqField) from a {
    rq := try BuildH2RequestFields(a, method, u, headers, body, strip, decoders, st.r.IsSome(), st.len)
    var list := List[H2ReqField].New(a)
    for f in rq.list {
        if fields.Sendable(f.name.AsBytes()) {
            name := try LowerH3Name(a, f.name, f.value)
            try list.Push(H2ReqField(name: name, value: f.value))
        }
    }
    var pseudo := rq.pseudo
    host := u.Hostname()
    if host.Has(":") {
        pseudo.authority = if p := u.Port() { try a.Fmt("[{host}]:{p}", (host, p)) } else { try a.Fmt("[{host}]", (host)) }
    }
    return (pseudo: pseudo, list: list.Done()[..])
}

Client.SendH3(&self, a: &Arena, io: Io, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, st: mut Stream, key: Origin, host: str, port: int): Error!?Exchange from a, self {
    for attempt in 0..2 {
        slot := (try self.ClaimH3(io, key, host, port)) else { return none }
        conn := H3ConnRef(slot.conn)
        sess := conn.session.Ref()
        var transferred := false
        var retire := false
        defer {
            if !transferred {
                p := self.H3PutBack(io, slot.index, false, retire)
                CloseH3(self.heap.Alloc(), p, io.NoCancel(), self.limits.close, h3.H3NoError)
            }
        }
        rq := try BuildH3Request(a, method, u, headers, body, strip, self.limits.decoders, &*st)
        stream := h3.Exchange.Open(sess, io, &rq.pseudo, rq.list, body.Len() == 0 && st.r.IsNone()) else e {
            match e {
                net.StreamReset(code) {
                    if code == h3.H3RequestRejected && attempt == 0 {
                        retire = true
                        continue
                    }
                }
                h3.Violation(code: _, stream: id) { retire = id < 0 }
                Closed { retire = true }
                net.MuxClosed { retire = true }
                _ {}
            }
            fail MapH3Error(e)
        }
        onerr {
            stream.Cancel()
            stream.Release(sess)
        }
        head := self.H3Head(a, io, &stream, method, body, st) else e {
            var retry := false
            match e {
                net.StreamReset(code) {
                    retry = code == h3.H3RequestRejected
                }
                _ {}
            }
            if goaway := sess.Goaway() {
                retry = retry || h3.Unprocessed(stream.Id(), goaway)
            }
            if retry && attempt == 0 && !st.took {
                retire = true
                stream.Cancel()
                stream.Release(sess)
                continue
            }
            fail e
        }
        state := Mutex[H3Body].New(H3Body(stream: none, index: slot.index, eof: false, failed: false, content_length: head.length, coded: head.coding.IsSome()))
        m := a.Put(state) else {
            H3Body(stream: old, index: _, eof: _, failed: _, content_length: _, coded: _) := state.Into()
            if orphan := old {
                orphan.Release(sess)
            }
            fail OutOfMemory
        }
        rd := try H3ReadPort(a, io, &*m, head.coding)
        var g := m.Lock(io.NoCancel()).Must()
        old := Replace(mut g.Mut().stream, some(stream))
        g.Unlock()
        if orphan := old {
            orphan.Release(sess)
        }
        transferred = true
        return some(Exchange(status: head.status, headers: head.headers, client: self, carrier: Carrier.H3(m: &*m), version: HttpVersion.Http3, reader: rd, io: io, key: key))
    }
    return none
}
