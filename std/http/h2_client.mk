// Why: with h2 or h2c on, Client.Do, Fetch and DoStreaming carry each
// request on a stream of a pooled HTTP/2 connection (ALPN "h2" with an
// http/1.1 fallback, or h2c by prior knowledge), under the same limits,
// redirects and body Reader as on HTTP/1.1.
//
// What: H2Stream, the state an Exchange holds for its stream; the body
// Reader over it (H2Reads); dialling with ALPN (ConnectH2OrH1); the request's
// fields (BuildH2RequestFields); the retry rule (H2Retries); Client.SendH2;
// and the Exchange's Close and Abandon (CloseH2Exchange, AbandonH2Exchange).
//
// How: SendH2 claims a slot of the mux pool, or dials one (mux_pool.mk); an
// idle connection whose server chose http/1.1 is used before a new dial.
// It opens a stream with h2.Conn.TryOpen, sends the body, reads the head
// with h2.Conn.Head and builds an Exchange over an H2Stream in the caller's
// arena, which links to the slot by a raw pointer and the slot's generation
// (mem_mux.mk). When every slot is busy, the request gets a connection of
// its own that its Exchange closes.

import std.compress
import std.core
import std.http.fields
import std.http.h2
import std.net
import std.raw
import std.rt
import std.task
import std.time
import std.url

struct H2ReqField {
    name: str
    value: str
}

impl fields.Field for H2ReqField {
    Name(&self): []u8 {
        return self.name.AsBytes()
    }

    Value(&self): []u8 {
        return self.value.AsBytes()
    }

    Sensitive(&self): bool {
        return Credential(self.name)
    }
}

struct H2Stream {
    slot: raw.Atomic[*MuxSlot]
    slot_gen: u64
    direct: raw.Atomic[*Box[h2.Conn]]
    sref: ?h2.StreamRef
    eof: bool
    failed: bool
    coded: bool
    content_length: ?i64
}

NewH2StreamIn(a: &Arena): OutOfMemory!&Mutex[H2Stream] from a {
    m := Mutex[H2Stream].New(H2Stream(
        slot: raw.Atomic[*MuxSlot].New(raw.Null[MuxSlot]()),
        slot_gen: 0,
        direct: raw.Atomic[*Box[h2.Conn]].New(raw.Null[Box[h2.Conn]]()),
        sref: none,
        eof: false,
        failed: false,
        coded: false,
        content_length: none,
    ))
    p := a.Put(m) else {
        _ = m.Into()
        fail OutOfMemory
    }
    return &*p
}

MapH2Error(e: h2.Error): Error {
    match e {
        h2.Violation(code, stream: _) { return Protocol(code: u64(code)) }
        h2.BadField { return BadRequest(offset: 0) }
        h2.TooLarge { return TooLarge }
        OutOfMemory { return OutOfMemory }
        Canceled { return Canceled }
        Closed { return Closed }
        time.TimedOut { return time.TimedOut }
        net.StreamReset(code) { return net.StreamReset(code: code) }
        net.MuxClosed(code, app, peer) { return net.MuxClosed(code: code, app: app, peer: peer) }
        net.Reset { return net.Reset }
        net.Refused { return net.Refused }
        net.AddrInUse { return net.AddrInUse }
        net.InvalidAddress { return net.InvalidAddress }
        net.Unreachable { return net.Unreachable }
        net.Unsupported { return net.Unsupported }
        net.SecureFailed(why) { return net.SecureFailed(why: why) }
        net.Os(code) { return net.Os(code: code) }
        _ { return BadRequest(offset: 0) }
    }
}

ShouldMarkH2ConnDead(e: h2.Error): bool {
    match e {
        net.StreamReset { return false }
        Canceled { return false }
        time.TimedOut { return false }
        OutOfMemory { return false }
        _ { return true }
    }
}

H2StreamConn(hs: &H2Stream): ?&h2.Conn {
    dp := hs.direct.Load()
    if !raw.IsNull(dp) {
        return some(MuxConnOf(dp))
    }
    sp := hs.slot.Load()
    if !raw.IsNull(sp) {
        cp := MuxSlotOf(sp).conn.Load()
        if !raw.IsNull(cp) {
            return some(MuxConnOf(cp))
        }
    }
    return none
}

H2StreamOver(m: &Mutex[H2Stream], io: Io, limit: i64): bool {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    hs := g.Ref()
    var over := false
    if !hs.coded {
        if cl := hs.content_length {
            over = cl > limit
        }
    }
    g.Unlock()
    return over
}

H2StreamFailed(m: &Mutex[H2Stream], io: Io): bool {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    f := g.Ref().failed
    g.Unlock()
    return f
}

H2StreamRead(m: &Mutex[H2Stream], io: Io, dst: mut []u8): ReadError!int {
    if dst.Len() == 0 {
        return 0
    }
    var g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    hs := mut *g.Mut()
    if hs.failed {
        g.Unlock()
        fail ReadError
    }
    if hs.eof {
        g.Unlock()
        return 0
    }
    sr := hs.sref else {
        hs.failed = true
        g.Unlock()
        fail ReadError
    }
    conn := H2StreamConn(&*hs) else {
        hs.failed = true
        g.Unlock()
        fail ReadError
    }
    n := conn.Read(io, sr, dst) else e {
        hs.failed = true
        sp := hs.slot.Load()
        sgen := hs.slot_gen
        g.Unlock()
        if !raw.IsNull(sp) && ShouldMarkH2ConnDead(e) {
            _ = MuxSlotOf(sp).MarkDead(sgen)
        }
        fail ReadError
    }
    if n == 0 {
        hs.eof = true
    }
    g.Unlock()
    return n
}

const H2Reads = core.ReadPort[Mutex[H2Stream]].New(H2StreamRead)

SettleH2(m: &Mutex[H2Stream], io: Io): ReadError!void {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    done := g.Ref().eof || g.Ref().failed
    g.Unlock()
    if done {
        return
    }
    var buf: [512]u8 = [0; 512]
    var total := 0
    for total < SettleMax {
        k := try H2StreamRead(m, io, mut buf[..])
        if k == 0 {
            return
        }
        total += k
    }
}

H2BodyReader(a: &Arena into hs, io: Io, hs: &Mutex[H2Stream] into a, dec: ?compress.Content): OutOfMemory!mut Reader from a {
    empty := try a.Slice[u8](0)
    raw_r := try a.Put((&H2Reads).Reader(io, hs, empty))
    if d := dec {
        dbuf := try a.Slice[u8](4096)
        dr := try d.wrap(a, raw_r, dbuf)
        return try a.Put(dr)
    }
    return raw_r
}

CloseH2Exchange(client: &Client, m: &Mutex[H2Stream], close_io: Io, ex_io: Io): Error!void {
    nc := ex_io.NoCancel()
    var g := m.Lock(nc) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    hs := mut *g.Mut()
    failed := hs.failed
    need_reset := !hs.eof || failed
    sr := core.Replace(mut hs.sref, none)
    sp := hs.slot.Swap(raw.Null[MuxSlot]())
    sgen := hs.slot_gen
    dp := hs.direct.Swap(raw.Null[Box[h2.Conn]]())
    g.Unlock()

    if sref := sr {
        if !raw.IsNull(dp) {
            conn := MuxConnOf(dp)
            if need_reset {
                conn.Reset(sref, h2.Cancel)
            }
            conn.Release(sref)
        } else if !raw.IsNull(sp) {
            cp := MuxSlotOf(sp).conn.Load()
            if !raw.IsNull(cp) {
                conn := MuxConnOf(cp)
                if need_reset {
                    conn.Reset(sref, h2.Cancel)
                }
                conn.Release(sref)
            }
        }
    }
    var close_err: ?Error = none
    a := client.heap.Alloc()
    if !raw.IsNull(dp) {
        box := TakeMuxBox(a, dp)
        c := box.Take(a)
        scope s := close_io.Scope() {
            s.CancelAfter(client.limits.close)
            c.Close(close_io) else e {
                c.Abandon()
                close_err = some(MapH2Error(e))
            }
        }
    } else if !raw.IsNull(sp) {
        sl := MuxSlotOf(sp)
        sl.Touch(sgen, time.Now(nc))
        if box := sl.ReleaseRef(a, false) {
            c := box.Take(a)
            scope s := close_io.Scope() {
                s.CancelAfter(client.limits.close)
                c.Close(close_io) else e {
                    c.Abandon()
                    close_err = some(MapH2Error(e))
                }
            }
        }
    }
    if e := close_err {
        fail e
    }
    if failed {
        fail ReadError
    }
}

AbandonH2Exchange(client: &Client, m: &Mutex[H2Stream], ex_io: Io) {
    nc := ex_io.NoCancel()
    var g := m.Lock(nc) else {
        Panic("http.Exchange: a NoCancel lock was cancelled")
    }
    hs := mut *g.Mut()
    sr := core.Replace(mut hs.sref, none)
    sp := hs.slot.Swap(raw.Null[MuxSlot]())
    dp := hs.direct.Swap(raw.Null[Box[h2.Conn]]())
    g.Unlock()

    if sref := sr {
        if !raw.IsNull(dp) {
            conn := MuxConnOf(dp)
            conn.Reset(sref, h2.Cancel)
            conn.Release(sref)
        } else if !raw.IsNull(sp) {
            cp := MuxSlotOf(sp).conn.Load()
            if !raw.IsNull(cp) {
                conn := MuxConnOf(cp)
                conn.Reset(sref, h2.Cancel)
                conn.Release(sref)
            }
        }
    }
    a := client.heap.Alloc()
    if !raw.IsNull(dp) {
        TakeMuxBox(a, dp).Take(a).Abandon()
    } else if !raw.IsNull(sp) {
        if box := MuxSlotOf(sp).ReleaseRef(a, false) {
            box.Take(a).Abandon()
        }
    }
}

ClientH2Limits(lim: &ClientLimits): h2.Limits {
    streams := if lim.h2_streams < 1 { 1 } else { lim.h2_streams }
    win := if lim.h2_window < 0 { 0 } else { lim.h2_window }
    wlim := if lim.response_header.Nanos() > 0 { lim.response_header } else { time.Seconds(30) }
    clim := if lim.close.Nanos() > 0 { lim.close } else { time.Seconds(5) }
    return h2.Limits(
        streams: streams,
        window: win,
        header_bytes: lim.header_bytes,
        body_bytes: int(lim.body_bytes),
        write_limit: wlim,
        close_limit: clim,
        idle_limit: lim.idle,
    )
}

DropOptH2Box(r: ?Box[h2.Conn], a: &Alloc) {
    if box := r {
        box.Take(a).Abandon()
    }
}

OpenAlpnH2OrH1(h: &Heap, lim: &ClientLimits, a: &Arena, io: Io, host: str, port: int, https: bool, h1_out: mut ?net.Conn): Error!?Box[h2.Conn] from h {
    c := try Reach(lim, a, io, host, port, https)
    if !https {
        box := h2.Conn.Client(h, io, c, ClientH2Limits(lim)) else e {
            c.Abandon()
            fail MapH2Error(e)
        }
        return some(box)
    }
    sec := lim.secure else {
        c.Abandon()
        fail NoSecureTransport
    }
    var offer: [2]str = ["h2", "http/1.1"]
    (conn: sc, chosen) := sec.Negotiate(rt.ProcessHeap(), io, c, host, offer[..]) else e {
        c.Abandon()
        fail e
    }
    if chosen == 0 {
        box := h2.Conn.Client(h, io, sc, ClientH2Limits(lim)) else e {
            sc.Abandon()
            fail MapH2Error(e)
        }
        return some(box)
    }
    DropConn(Replace(mut *h1_out, some(sc)))
    return none
}

ConnectH2OrH1(h: &Heap, lim: &ClientLimits, a: &Arena, io: Io, host: str, port: int, https: bool, h1_out: mut ?net.Conn): Error!?Box[h2.Conn] from h {
    bare := if host.Len() > 2 && host.Byte(0) == 0x5B { host[1..host.Len() - 1] } else { host }
    var err: ?Error = none
    var expired := false
    var h2_out: ?Box[h2.Conn] = none
    scope s := io.Scope() {
        s.CancelAfter(lim.connect)
        DropOptH2Box(Replace(mut h2_out, OpenAlpnH2OrH1(h, lim, a, io, bare, port, https, h1_out) else e {
            err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
            none
        }), h.Alloc())
    }
    if box := h2_out {
        return some(box)
    }
    if h1_out.IsSome() {
        return none
    }
    if expired {
        fail time.TimedOut
    }
    if e := err {
        fail e
    }
    fail Canceled
}

BuildH2RequestFields(a: &Arena, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, decoders: []compress.Content, streamed: bool, slen: ?i64): Error!(pseudo: fields.Pseudo, list: []H2ReqField) from a {
    var auth_t := Text.New(a)
    try auth_t.Push(u.Hostname())
    if p := u.Port() {
        try auth_t.Push(try a.Fmt(":{p}", (p)))
    }
    auth := auth_t.Done()

    var path_t := Text.New(a)
    up := u.Path()
    if up.Len() == 0 {
        try path_t.Push("/")
    } else {
        try PushTarget(a, mut path_t, up, false)
    }
    uq := u.Query()
    if uq.Len() > 0 {
        try path_t.Push("?")
        try PushTarget(a, mut path_t, uq, true)
    }
    path_str := path_t.Done()

    var out := List[H2ReqField].New(a)
    for h in headers {
        if ClientOwned(h.name) || EqFold(h.name, "proxy-connection") || strip && Credential(h.name) || decoders.Len() > 0 && EqFold(h.name, "accept-encoding") {
            continue
        }
        nc := try a.Dup(h.name)
        vc := try a.Dup(h.value)
        try out.Push(H2ReqField(name: nc, value: vc))
    }
    if decoders.Len() > 0 {
        var ae := Text.New(a)
        for i in 0..decoders.Len() {
            if i > 0 {
                try ae.Push(", ")
            }
            try ae.Push(decoders[i].name)
        }
        try out.Push(H2ReqField(name: "accept-encoding", value: ae.Done()))
    }
    if streamed {
        if n := slen {
            try out.Push(H2ReqField(name: "content-length", value: try a.Fmt("{n}", (n))))
        }
    } else if body.Len() > 0 || method == "POST" || method == "PUT" || method == "PATCH" {
        try out.Push(H2ReqField(name: "content-length", value: try a.Fmt("{n}", (n: body.Len()))))
    }
    pseudo := fields.Pseudo(method: try a.Dup(method), scheme: try a.Dup(u.Scheme()), authority: auth, path: path_str)
    return (pseudo: pseudo, list: out.Done()[..])
}

// MaxUnprocessed bounds the retries of a request the server did not
// process: REFUSED_STREAM, or a stream past a GOAWAY's last stream id (RFC
// 9113 6.8, 8.7). Such a request is retried whatever its method, because the
// server guarantees it did nothing; a fresh connection opens streams before
// it reads the server's SETTINGS, so a low stream limit may refuse a few.
const MaxUnprocessed = 8

// H2Retries counts the retries of one request.
struct H2Retries {
    unprocessed: int
    replayed: int
}

// H2Retries.Again reports whether the request that failed with e goes again
// on another stream, and counts it: a request the server did not process,
// up to MaxUnprocessed times; an idempotent one whose connection failed,
// once, as on HTTP/1.1. A request whose streamed body was read from (took)
// never goes again.
H2Retries.Again(mut self, e: &Error, method: str, took: bool): bool {
    if took {
        return false
    }
    var unprocessed := false
    var replay := false
    match *e {
        net.MuxClosed { unprocessed = true }
        net.StreamReset(code) {
            unprocessed = code == u64(h2.RefusedStream)
            replay = Idempotent(method) && code != u64(h2.Cancel)
        }
        Closed { replay = Idempotent(method) }
        net.Reset { replay = Idempotent(method) }
        WriteError { replay = Idempotent(method) }
        _ {}
    }
    if unprocessed && self.unprocessed < MaxUnprocessed {
        self.unprocessed += 1
        return true
    }
    if replay && self.replayed < 1 {
        self.replayed += 1
        return true
    }
    return false
}

TryH2Head(a: &Arena, io: Io, conn: &h2.Conn, sref: h2.StreamRef): h2.Error!?(status: int, list: []fields.FieldView, content_length: ?i64) from a {
    return some(try conn.Head(a, io, sref))
}

// SendH2Body sends the request body on sref: body whole, or st's Reader
// in pieces of at most StreamPiece bytes, ending the stream.
SendH2Body(io: Io, conn: &h2.Conn, sref: h2.StreamRef, body: []u8, st: mut Stream): Error!void {
    match mut st.r {
        some(r) {
            var buf: [16384]u8 = [0; 16384]
            var sent: i64 = 0
            if expect := st.len {
                for sent < expect {
                    rem := expect - sent
                    want := if rem < i64(StreamPiece) { int(rem) } else { StreamPiece }
                    k := (*r).Read(mut buf[0..want]) else {
                        try CancelCheck(io)
                        fail ReadError
                    }
                    if k == 0 {
                        fail ReadError
                    }
                    st.took = true
                    sent += i64(k)
                    conn.Send(io, sref, buf[0..k], sent == expect) else e {
                        fail MapH2Error(e)
                    }
                }
                if expect == 0 {
                    empty: [0]u8 = []
                    conn.Send(io, sref, empty[..], true) else e {
                        fail MapH2Error(e)
                    }
                }
            } else {
                for {
                    k := (*r).Read(mut buf[..]) else {
                        try CancelCheck(io)
                        fail ReadError
                    }
                    if k == 0 {
                        empty: [0]u8 = []
                        conn.Send(io, sref, empty[..], true) else e {
                            fail MapH2Error(e)
                        }
                        break
                    }
                    st.took = true
                    conn.Send(io, sref, buf[0..k], false) else e {
                        fail MapH2Error(e)
                    }
                }
            }
        }
        none {
            if body.Len() > 0 {
                conn.Send(io, sref, body, true) else e {
                    fail MapH2Error(e)
                }
            }
        }
    }
}

// FinishH2Exchange sends the body and reads the reply's head into an
// Exchange. A server may answer before it has read the whole body and reset
// the stream (RFC 9113 8.1 allows RST_STREAM(NO_ERROR) after a complete
// response), so a reset while sending still reads the head: a complete reply
// is delivered, and otherwise the head read fails with the reset.
Client.FinishH2Exchange(&self, a: &Arena, io: Io, hm: &Mutex[H2Stream] into a, conn: &h2.Conn, sref: h2.StreamRef, method: str, body: []u8, st: mut Stream, key: Origin): Error!Exchange from a, self {
    SendH2Body(io, conn, sref, body, st) else e {
        match e {
            net.StreamReset {}
            _ { fail e }
        }
    }
    var expired := false
    var err: ?Error = none
    var status := 0
    var cl: ?i64 = none
    var raw_hs := List[Header].New(a)
    scope hs := io.Scope() {
        hs.CancelAfter(self.limits.response_header)
        if res := TryH2Head(a, io, conn, sref) else e {
            err = some(MapH2Error(e))
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&hs, canceled)
            none
        } {
            status = res.status
            cl = res.content_length
            for f in res.list {
                raw_hs.Push(Header(name: f.name, value: f.value)) else oe {
                    err = some(oe)
                    break
                }
            }
        }
    }
    if e := err {
        if expired {
            fail time.TimedOut
        }
        fail e
    }
    raw_list := raw_hs.Done()[..]
    // A reply with no body gets no decoder, as on HTTP/1.1. END_STREAM alone
    // does not say the body is empty: DATA may sit buffered behind it, and
    // Conn.Read reports the end only after the buffered bytes.
    empty_len := if n := cl { n == 0 } else { false }
    no_body := method == "HEAD" || status == 204 || status == 304 || empty_len
    dec := if no_body { none } else { Decoder(self.limits.decoders, raw_list) }
    var hmg := hm.Lock(io.NoCancel()).Must()
    hmg.Mut().coded = dec.IsSome()
    hmg.Mut().content_length = cl
    hmg.Unlock()

    final_hs := if dec.IsSome() { try WithoutFraming(a, raw_list) } else { raw_list }
    rd := try H2BodyReader(a, io, hm, dec)
    return Exchange(status: status, headers: final_hs, client: self, carrier: Carrier.H2(m: hm), version: HttpVersion.Http2, reader: rd, io: io, key: key)
}

Client.SendH2(&self, a: &Arena, io: Io, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, st: mut Stream, key: Origin, host: str, port: int, https: bool): Error!Exchange from a, self {
    reqf := try BuildH2RequestFields(a, method, u, headers, body, strip, self.limits.decoders, st.r.IsSome(), st.len)
    start := time.Now(io.NoCancel())
    var retries := H2Retries(unprocessed: 0, replayed: 0)
    var tried_mask: u32 = 0
    var fresh_refusals := 0
    for {
        claim := try self.ClaimMuxOrDial(io, key, start, tried_mask)
        // No live HTTP/2 connection has room: an idle connection whose server
        // chose http/1.1 comes before a new dial.
        idle := match claim {
            MuxClaimKind.MuxGot { -1 }
            _ { self.ClaimIdle(io, key) }
        }
        if idle >= 0 {
            match claim {
                MuxClaimKind.MuxDial(idx, gen_) { MuxSlotAt(&self.mux, idx).AbortDial(gen_) }
                _ {}
            }
            return try self.SendH1(a, io, method, u, headers, body, strip, st, key, host, port, https, idle)
        }
        var slot_idx := -1
        var slot_gen: u64 = 0
        var direct_ptr := raw.Null[Box[h2.Conn]]()
        var fresh := false
        var h1_fallback: ?net.Conn = none
        match claim {
            MuxClaimKind.MuxGot(idx, gen_) {
                slot_idx = idx
                slot_gen = gen_
            }
            MuxClaimKind.MuxDial(idx, gen_) {
                sl := MuxSlotAt(&self.mux, idx)
                obox := ConnectH2OrH1(self.heap, &self.limits, a, io, host, port, https, mut h1_fallback) else e {
                    DropConn(h1_fallback)
                    sl.AbortDial(gen_)
                    fail e
                }
                if box := obox {
                    DropConn(Replace(mut h1_fallback, none))
                    sl.FinishDial(self.heap.Alloc(), gen_, box, time.Now(io.NoCancel())) else oe {
                        sl.AbortDial(gen_)
                        box.Take(self.heap.Alloc()).Abandon()
                        DropConn(h1_fallback)
                        fail oe
                    }
                    self.drive_wake.NotifyOne()
                    slot_idx = idx
                    slot_gen = gen_
                    fresh = true
                } else {
                    sl.AbortDial(gen_)
                    if h1_fallback.IsNone() {
                        DropConn(h1_fallback)
                        fail Canceled
                    }
                }
            }
            MuxClaimKind.MuxFull {
                obox := ConnectH2OrH1(self.heap, &self.limits, a, io, host, port, https, mut h1_fallback) else e {
                    DropConn(h1_fallback)
                    fail e
                }
                if box := obox {
                    DropConn(Replace(mut h1_fallback, none))
                    dp := AllocMuxBox(self.heap.Alloc(), box) else oe {
                        box.Take(self.heap.Alloc()).Abandon()
                        DropConn(h1_fallback)
                        fail oe
                    }
                    direct_ptr = dp
                    fresh = true
                } else if h1_fallback.IsNone() {
                    DropConn(h1_fallback)
                    fail Canceled
                }
            }
        }
        if h1_conn := h1_fallback {
            head := RequestHead(a, method, u, headers, body, strip, self.limits.proxy.IsSome(), self.limits.decoders, st.r.IsSome(), st.len) else oe {
                h1_conn.Abandon()
                fail oe
            }
            lm := NewLineIn(a) else oe {
                h1_conn.Abandon()
                fail oe
            }
            cl := self.Claim(io, key, false)
            if cl.slot >= 0 {
                self.AttachH1ToSlot(io, lm, cl.slot, key, h1_conn)
            } else {
                AttachH1ToLine(io, lm, h1_conn)
            }
            ex := self.Exchange(a, io, lm, key, method, head, body, st) else e {
                AbandonLine(lm, io)
                fail e
            }
            return ex
        }

        conn := if slot_idx >= 0 {
            MuxConnOf(MuxSlotAt(&self.mux, slot_idx).conn.Load())
        } else {
            MuxConnOf(direct_ptr)
        }
        has_stream := st.r.IsSome()
        end_headers := !has_stream && body.Len() == 0
        osref := conn.TryOpen(io, &reqf.pseudo, reqf.list, end_headers) else e {
            mapped := MapH2Error(e)
            if slot_idx >= 0 {
                sl := MuxSlotAt(&self.mux, slot_idx)
                if ShouldMarkH2ConnDead(e) || conn.Draining() {
                    _ = sl.MarkDead(slot_gen)
                }
                if box := sl.ReleaseRef(self.heap.Alloc(), false) {
                    CloseMuxBox(self.heap.Alloc(), box, io, self.limits.close)
                }
            } else {
                CloseMuxBox(self.heap.Alloc(), TakeMuxBox(self.heap.Alloc(), direct_ptr), io, self.limits.close)
            }
            if retries.Again(&mapped, method, st.took) {
                tried_mask = 0
                continue
            }
            fail mapped
        }
        sref := osref else {
            // No stream. A pooled connection at its limit is skipped for the
            // rest of this round, so each round ends in a dial; a connection
            // dialled for this request that grants none is counted, so a
            // server that grants no stream fails the request instead of
            // being dialled for ever.
            if slot_idx >= 0 {
                sl := MuxSlotAt(&self.mux, slot_idx)
                if conn.Draining() {
                    _ = sl.MarkDead(slot_gen)
                }
                if box := sl.ReleaseRef(self.heap.Alloc(), false) {
                    CloseMuxBox(self.heap.Alloc(), box, io, self.limits.close)
                }
                tried_mask |= Trunc[u32](1) << Trunc[u32](slot_idx)
            } else {
                CloseMuxBox(self.heap.Alloc(), TakeMuxBox(self.heap.Alloc(), direct_ptr), io, self.limits.close)
                tried_mask = u32(0xffff)
            }
            if fresh {
                fresh_refusals += 1
                if fresh_refusals > 2 {
                    fail net.StreamReset(code: u64(h2.RefusedStream))
                }
            }
            continue
        }

        hm := NewH2StreamIn(a) else oe {
            conn.Reset(sref, h2.Cancel)
            conn.Release(sref)
            if slot_idx >= 0 {
                if box := MuxSlotAt(&self.mux, slot_idx).ReleaseRef(self.heap.Alloc(), false) {
                    CloseMuxBox(self.heap.Alloc(), box, io, self.limits.close)
                }
            } else {
                CloseMuxBox(self.heap.Alloc(), TakeMuxBox(self.heap.Alloc(), direct_ptr), io, self.limits.close)
            }
            fail oe
        }
        var hmg := hm.Lock(io.NoCancel()).Must()
        if slot_idx >= 0 {
            hmg.Mut().slot.Store(raw.Of(MuxSlotAt(&self.mux, slot_idx)))
            hmg.Mut().slot_gen = slot_gen
        } else {
            hmg.Mut().direct.Store(direct_ptr)
        }
        hmg.Mut().sref = some(sref)
        hmg.Unlock()

        ex := self.FinishH2Exchange(a, io, hm, conn, sref, method, body, st, key) else e {
            // A failed or draining connection leaves the pool before the
            // retry, so the retry cannot claim it again.
            if slot_idx >= 0 && conn.Draining() {
                _ = MuxSlotAt(&self.mux, slot_idx).MarkDead(slot_gen)
            }
            AbandonH2Exchange(self, hm, io)
            if retries.Again(&e, method, st.took) {
                tried_mask = 0
                continue
            }
            fail e
        }
        return ex
    }
}
