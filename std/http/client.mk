// Why: a program needs one HTTP/1.1 client that every task shares, which
// reuses kept-alive connections and refuses addresses the caller forbids,
// with no window between that check and the dial.
//
// What: Client with New, Close, Do and Fetch, and what they share: Send and
// Client.Exchange (one request on a Line), RequestHead and PushTarget (the
// request's bytes), the pool key Origin, and Connect, Open and Dial.
//
// How: kept-alive connections live in a fixed table of slots (pool.mk),
// keyed by origin; Dial checks each resolved address with
// ClientLimits.allow and dials the one allowed, so a name cannot resolve
// one way for the check and another for the dial. Details below.

// A pool key is the origin (scheme, host and port, RFC 6454): a request
// claims an idle connection of its origin, and Exchange.Close leaves it
// there for the next one. https goes through ClientLimits.secure, and with
// ClientLimits.proxy every connection is the proxy's (proxy.mk). A pooled
// connection the server closed meanwhile is retried once on a new one when
// nothing of the reply was read, as Go's Transport retries.

import std.compress
import std.net
import std.raw
import std.rt
import std.sync
import std.task
import std.time
import std.url

// MaxOrigin is the room of a pool key: "https://" and a host of up to 255
// bytes with ":" and five port digits.
const MaxOrigin = 280

// Origin is a pool key, "scheme://host:port" in place, n its length. A key
// that does not fit has n = -1, and its connections are never pooled.
struct Origin {
    b: [280]u8
    n: int
}

/// An HTTP/1.1 client with a pool of kept-alive connections, and HTTP/2
/// when its limits turn h2 or h2c on, with a second pool of multiplexed
/// connections. One client serves every task of a program: it is linear,
/// Send and Share, and its pools are behind locks of their own. End it with
/// Close.
///
/// It keeps h and its limits, with what they borrow, such as the
/// tls.Config behind secure, so it cannot outlive them. Its connections,
/// secure ones included, live on the process heap, so a pooled connection
/// outlives every request's arena.
export linear mut view struct Client {
    heap: &Heap
    limits: ClientLimits
    slots: Pair[Pair[Pair[Pair[Slot]]]]
    mux: Pair[Pair[Pair[Pair[MuxSlot]]]]
    mux_dial: Mutex[int]
    drive_wake: sync.Notify
    drive_stop: raw.Atomic[u32]
    drive_active: raw.Atomic[u32]
    drive_ready: sync.Notify
    h3_pool: Mutex[[16]H3Slot]
    h3_wake: sync.Notify
    alt_cache: Mutex[[64]AltOrigin]
}

/// A client with the limits l and an empty pool. O(16): the pool has 16 slots;
/// allocates nothing: the pool's table is inside the client.
export Client.New(h: &Heap, l: ClientLimits): Client from h, l {
    return Client(heap: h, limits: l, slots: NewSlots(), mux: NewMuxSlots(), mux_dial: Mutex[int].New(0), drive_wake: sync.Notify.New(), drive_stop: raw.Atomic[u32].New(0), drive_active: raw.Atomic[u32].New(0), drive_ready: sync.Notify.New(), h3_pool: Mutex[[16]H3Slot].New(NewH3Slots()), h3_wake: sync.Notify.New(), alt_cache: Mutex[[64]AltOrigin].New([NoAltOrigin; 64]))
}

/// Releases the client: every idle connection is closed without speaking
/// to the peer, and the pool's memory goes back to the heap. An open
/// Exchange borrows the client, so none can be open here. Never blocks or
/// fails. O(idle connections); allocates nothing.
export Client.Close(self) {
    Client(heap, limits: _, slots, mux, mux_dial: _, drive_wake: _, drive_stop: _, drive_active: _, drive_ready: _, h3_pool, h3_wake: _, alt_cache) := self
    FreeSlots(slots)
    FreeMuxSlots(heap.Alloc(), mux)
    _ = alt_cache.Into()
    h3_slots := h3_pool.Into()
    for sl in &h3_slots[..] {
        DropH3(heap.Alloc(), sl.conn)
    }
}

/// Sends req and reads the reply's head, following redirects, and returns
/// an Exchange that holds the connection and reads the body. End the
/// Exchange with Close or Abandon. It uses an idle pooled connection to
/// the same origin when there is one. O(bytes sent and read); allocates
/// the head, the fields, the connection's read buffer and the body's
/// Readers in a, which holds the connection until the Exchange ends.
///
/// Redirects: a 301, 302, 303, 307 or 308 whose Location is an http or
/// https URL is followed, up to the limits' redirects (TooManyRedirects
/// past them; with 0 none is followed and the 3xx reply is returned as it
/// is). The redirect's body is read and dropped, up to 64 KiB, else its
/// connection is closed. A 303, and a 301 or 302 after a POST, is followed
/// with GET and no body. When a redirect changes the origin, Authorization,
/// Proxy-Authorization and Cookie are not sent to the new one; the client
/// never sends URL userinfo. A Location that is not an http or https URL
/// ends the following, and that reply is returned.
///
/// Each connection: allow is asked about each resolved address, and the
/// client dials the allowed ones in turn until one connects (Refused when
/// allow refuses every one, else the last dial's error). https
/// needs secure (NoSecureTransport). The connect limit bounds resolving,
/// dialling and the handshake. With the limits' proxy, every connection is
/// the proxy's: allow is asked about its addresses, and the proxy resolves
/// the origin's name. An http request goes to it in absolute form, and an
/// https one through a CONNECT tunnel, whose reply head the connect limit
/// and header_bytes bound. A CONNECT status other than 2xx is
/// ProxyRefused(code), and a proxy that is not an http URL with a host and
/// no userinfo is BadUrl.
///
/// The reply's head is bounded by header_bytes (TooLarge) and its arrival
/// by response_header (time.TimedOut). Its framing is checked as strictly
/// as a request's: BadRequest at the offset in the reply. The fields sent
/// are the request's, with Host, Connection, Content-Length,
/// Transfer-Encoding and, when the client has decoders, Accept-Encoding
/// written by the client itself. A pooled connection that the server
/// closed while it was idle is retried once on a new connection, when
/// nothing of the reply was read and the method is idempotent. A cancel
/// while it waits is Canceled, or time.TimedOut once response_header has
/// passed; either way its connection is closed.
export Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Exchange from a, self {
    var u := req.url
    var method := req.method
    var body := req.body
    var strip := false
    var hop := 0
    var st := NoStream()
    for {
        ex := try self.Send(a, io, method, u, req.headers, body, strip, mut st)
        if !IsRedirect(ex.status) || self.limits.redirects <= 0 {
            // A redirects limit of 0 follows none: the 3xx reply is the
            // caller's as it is.
            return ex
        }
        loc := Lookup(ex.headers, "location") else {
            return ex
        }
        // A copy in a: url.Parse stores its input into a, and loc
        // is a view of the reply, which holds self.
        dloc := a.Dup(loc) else {
            return ex
        }
        next := url.Parse(a, dloc, some(u)) else {
            return ex
        }
        if next.Scheme() != "http" && next.Scheme() != "https" {
            return ex
        }
        if hop >= self.limits.redirects {
            ex.Abandon()
            fail TooManyRedirects
        }
        hop += 1
        code := ex.status
        Drain(ex, io)
        if code == 303 || (code == 301 || code == 302) && method == "POST" {
            if method != "HEAD" {
                method = "GET"
            }
            body = "".AsBytes()
        }
        if !SameOrigin(u, &next) {
            strip = true
        }
        u = &*try a.Put(next)
    }
}

/// Sends req as Do does, reads the whole body into a, and closes the
/// exchange: for a reply that fits in memory. O(bytes sent and read);
/// allocates the reply's fields and body in a, the body in buffers that
/// double from 16 KiB, up to about four times its size, and Do's working
/// memory in an arena of its own, freed before it returns.
///
/// A decoded body over the limits' body_bytes is TooLarge, and so is a
/// reply not decoded whose Content-Length passes it, before a byte of its
/// body is read. A failed body read is ReadError, or Canceled under a
/// cancelled scope. Otherwise it fails as Do does.
export Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): Error!Reply from a {
    var sub := try a.Sub()
    defer sub.Free()
    var ex := try self.Do(&sub, io, req)
    onerr ex.Abandon()
    if ExchangeOver(&ex, io, self.limits.body_bytes) {
        fail TooLarge
    }
    status := ex.status
    hs := try CopyHeaders(a, ex.headers)
    body := try ReadAllBody(a, io, mut ex, self.limits.body_bytes)
    try ex.Close(io)
    return Reply(status: status, headers: hs, body: body)
}

// CopyHeaders copies hs, names and values, into a.
CopyHeaders(a: &Arena, hs: []Header): OutOfMemory![]Header from a {
    var l := List[Header].New(a)
    for h in hs {
        try l.Push(Header(name: try a.Dup(h.name), value: try a.Dup(h.value)))
    }
    return l.Done()[..]
}

// ReadAllBody reads ex's body to its end into a buffer in a that doubles
// as bytes arrive (GrowFor), so memory follows the bytes that came, never
// the limit; one byte past limit is TooLarge.
ReadAllBody(a: &Arena, io: Io, ex: mut Exchange, limit: i64): Error![]u8 from a {
    lim := if limit < 0 { 0 } else { int(limit) }
    var none_buf: [0]u8 = []
    var r := ex.Body(mut none_buf[..])
    var b := try a.Slice[u8](0)
    var n := 0
    for {
        if n == b.Len() {
            if n == lim {
                var one: [1]u8 = [0; 1]
                k := r.Read(mut one[..]) else {
                    try CancelCheck(io)
                    fail ReadError
                }
                if k > 0 {
                    fail TooLarge
                }
                return b[0..n]
            }
            b = try GrowFor(a, b[0..n], lim - n)
        }
        k := r.Read(mut b[n..b.Len()]) else {
            try CancelCheck(io)
            fail ReadError
        }
        if k == 0 {
            return b[0..n]
        }
        n += k
    }
}

// Drain reads a redirect's body to its end, at most 64 KiB, and closes the
// exchange, which pools the connection when the whole body was read; a
// failed close releases the connection, and the next request goes on.
Drain(var ex: Exchange, io: Io) {
    DrainBody(mut ex)
    ex.Close(io) else {
        ex.Abandon()
    }
}

// DrainBody reads at most 64 KiB of ex's body, until its end or a failure.
DrainBody(ex: mut Exchange) {
    var buf: [4096]u8 = [0; 4096]
    var none_buf: [0]u8 = []
    var r := ex.Body(mut none_buf[..])
    var total := 0
    for total < 65536 {
        n := r.Read(mut buf[..]) else {
            return
        }
        if n == 0 {
            return
        }
        total += n
    }
}

// IsRedirect: a status the client follows (RFC 9110 15.4).
IsRedirect(code: int): bool {
    return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}

// SameOrigin: a and b have the same scheme, host and port (RFC 6454).
SameOrigin(a: &url.Url, b: &url.Url): bool {
    return a.Scheme() == b.Scheme() && a.Hostname() == b.Hostname() && PortOf(a) == PortOf(b)
}

// PortOf is u's port, 0 when it is its scheme's default.
PortOf(u: &url.Url): int {
    p := u.Port() else {
        return 0
    }
    return int(p)
}

// Send makes one request to u and reads its reply's head: from a pooled
// connection when one is idle (retried once on a new connection, never
// another pooled one, when the server closed it before any byte of the
// reply), else from a new one. st is the body from a Reader, when
// DoStreaming sends one; a retry needs that no byte was taken from it.
Client.Send(&self, a: &Arena, io: Io, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, st: mut Stream): Error!Exchange from a, self {
    scheme := u.Scheme()
    https := scheme == "https"
    if !https && scheme != "http" {
        fail BadUrl
    }
    if !IsToken(method) {
        fail BadUrl
    }
    host := u.Hostname()
    if host.Len() == 0 {
        fail BadUrl
    }
    var proxied := false
    if p := self.limits.proxy {
        try ProxyCheck(p)
        proxied = true
    }
    port := if p := u.Port() { int(p) } else if https { 443 } else { 80 }
    key := OriginOf(scheme, host, port)
    use_h2 := (https && self.limits.h2) || (!https && self.limits.h2c && !proxied)
    if https && !proxied && self.H3Running() && self.limits.h3.IsSome() {
        if h3 := try self.SendH3(a, io, method, u, headers, body, strip, st, key, host, port) {
            self.RememberAltSvc(io, host, key, &h3)
            return h3
        }
    }
    if https && self.limits.secure.IsNone() {
        fail NoSecureTransport
    }
    ex := if use_h2 {
        try self.SendH2(a, io, method, u, headers, body, strip, st, key, host, port, https)
    } else {
        try self.SendH1(a, io, method, u, headers, body, strip, st, key, host, port, https, -1)
    }
    if https && !proxied {
        self.RememberAltSvc(io, host, key, &ex)
    }
    return ex
}

// SendH1 is Send over HTTP/1.1. pre is a slot ClaimIdle took for key's
// origin, used for the first attempt, or -1 to claim one here.
Client.SendH1(&self, a: &Arena, io: Io, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, st: mut Stream, key: Origin, host: str, port: int, https: bool, pre: int): Error!Exchange from a, self {
    proxied := self.limits.proxy.IsSome()
    head := RequestHead(a, method, u, headers, body, strip, proxied, self.limits.decoders, st.r.IsSome(), st.len) else e {
        if pre >= 0 {
            SlotAt(&self.slots, pre).busy.Store(0)
        }
        fail e
    }
    lm := NewLineIn(a) else e {
        if pre >= 0 {
            SlotAt(&self.slots, pre).busy.Store(0)
        }
        fail e
    }
    var attempt := 0
    var first := pre
    for {
        cl := if first >= 0 { Claim(slot: first, reused: true) } else { self.Claim(io, key, attempt == 0) }
        first = -1
        if cl.slot >= 0 {
            sl := SlotAt(&self.slots, cl.slot)
            if !cl.reused {
                c := Connect(rt.ProcessHeap(), &self.limits, a, io, host, port, https) else e {
                    sl.busy.Store(0)
                    fail e
                }
                var sg := sl.st.Lock(io.NoCancel()) else {
                    Panic("http.Client: a NoCancel lock was cancelled")
                }
                DropConn(Replace(mut sg.Mut().conn, some(c)))
                sg.Mut().key = key
                sg.Unlock()
            }
            var pg := lm.Lock(io.NoCancel()) else {
                Panic("http.Client: a NoCancel lock was cancelled")
            }
            pg.Mut().slot.Store(raw.Of(sl)) // a raw link: mem_slot.mk says why
            ResetLine(pg.Mut())
            pg.Unlock()
        } else {
            c := try Connect(rt.ProcessHeap(), &self.limits, a, io, host, port, https)
            var pg := lm.Lock(io.NoCancel()) else {
                Panic("http.Client: a NoCancel lock was cancelled")
            }
            DropConn(Replace(mut pg.Mut().conn, some(c)))
            ResetLine(pg.Mut())
            pg.Unlock()
        }
        got := self.Exchange(a, io, lm, key, method, head, body, st) else e {
            stale := cl.reused && Retryable(e) && Idempotent(method) && LineUntouched(lm, io) && !st.took
            AbandonLine(lm, io)
            if stale && attempt == 0 {
                attempt = 1
                continue
            }
            fail e
        }
        return got
    }
}

// AttachH1ToSlot installs an ALPN "http/1.1" fallback net.Conn into a claimed
// HTTP/1.1 pool slot and links lm to it.
Client.AttachH1ToSlot(&self, io: Io, lm: &Mutex[Line], slot_idx: int, key: Origin, c: net.Conn into self) {
    sl := SlotAt(&self.slots, slot_idx)
    var sg := sl.st.Lock(io.NoCancel()) else {
        Panic("http.Client: a NoCancel lock was cancelled")
    }
    DropConn(Replace(mut sg.Mut().conn, some(c)))
    sg.Mut().key = key
    sg.Unlock()
    var pg := lm.Lock(io.NoCancel()) else {
        Panic("http.Client: a NoCancel lock was cancelled")
    }
    pg.Mut().slot.Store(raw.Of(sl))
    ResetLine(pg.Mut())
    pg.Unlock()
}

// AttachH1ToLine installs an unpooled ALPN "http/1.1" fallback net.Conn into lm.
AttachH1ToLine(io: Io, lm: &Mutex[Line], c: net.Conn into lm) {
    var pg := lm.Lock(io.NoCancel()) else {
        Panic("http.Client: a NoCancel lock was cancelled")
    }
    DropConn(Replace(mut pg.Mut().conn, some(c)))
    ResetLine(pg.Mut())
    pg.Unlock()
}

// Exchange writes the request on the Line's connection and reads the
// reply's head into an Exchange.
Client.Exchange(&self, a: &Arena into lm, io: Io, lm: &Mutex[Line] into a, key: Origin, method: str, head: str, body: []u8, st: mut Stream): Error!Exchange from a, self {
    try LineWrite(lm, io, head, body, st)
    var g := try lm.Lock(io)
    l := g.Mut()
    lim := self.limits.header_bytes
    var consumed := 0
    var interim := 0
    var expired := false
    var err: ?Error = none
    var hd: ?Head = none
    scope s := io.Scope() {
        s.CancelAfter(self.limits.response_header)
        for {
            raw := ReadReplyHead(a, io, l, lim - consumed, consumed) else e {
                err = some(e)
                canceled := match e {
                    Canceled { true }
                    _ { false }
                }
                expired = task.LimitHit(&s, canceled)
                break
            }
            h := ParseHead(a, raw.text, true, consumed + raw.skipped) else e {
                err = some(e)
                break
            }
            if h.status >= 100 && h.status < 200 && h.status != 101 {
                interim += 1
                if interim > MaxInterim {
                    err = some(BadRequest(offset: consumed + raw.skipped))
                    break
                }
                consumed += raw.skipped + raw.text.Len()
                continue
            }
            hd = some(h)
            break
        }
    }
    if e := err {
        l.failed = true
        g.Unlock()
        if expired {
            fail time.TimedOut
        }
        fail e
    }
    h := hd else {
        g.Unlock()
        fail Canceled
    }
    l.reuse = !h.close
    l.done = false
    l.failed = false
    l.meta = lim
    if method == "HEAD" || h.status < 200 || h.status == 204 || h.status == 304 {
        l.mode = BodyNone
        l.done = true
        if h.status == 101 {
            l.reuse = false
        }
    } else if h.chunked {
        l.mode = BodyChunked
        l.ck = Chunk(state: CkSize, size: 0, digits: 0, off: 0, meta: 0)
    } else if h.length >= 0 {
        l.mode = BodyLength
        l.left = h.length
        l.done = h.length == 0
    } else {
        l.mode = BodyClose
        l.reuse = false
    }
    dec := if l.done { none } else { Decoder(self.limits.decoders, h.headers) }
    l.coded = dec.IsSome()
    g.Unlock()
    hs := if dec.IsSome() { try WithoutFraming(a, h.headers) } else { h.headers }
    ver := if h.minor == 0 { HttpVersion.Http10 } else { HttpVersion.Http11 }
    rd := try BodyReader(a, io, lm, dec)
    return Exchange(status: h.status, headers: hs, client: self, carrier: Carrier.H1(m: lm), version: ver, reader: rd, io: io, key: key)
}

// NewLineIn puts an empty Line under its Mutex in a.
NewLineIn(a: &Arena): OutOfMemory!&Mutex[Line] from a {
    m := Mutex[Line].New(NewLine())
    p := a.Put(m) else {
        DropLine(m.Into())
        fail OutOfMemory
    }
    return &*p
}

// DropLine releases a Line's connection, if it holds one.
DropLine(l: Line) {
    Line(slot: _, conn, ..) := l
    match conn {
        some(c) {
            c.Abandon()
        }
        none {}
    }
}

// BodyReader is the body's Reader over the Line, in a: the raw Reader, and
// dec's decoding Reader over it when the reply is encoded.
BodyReader(a: &Arena into lm, io: Io, lm: &Mutex[Line] into a, dec: ?compress.Content): OutOfMemory!mut Reader from a {
    empty := try a.Slice[u8](0)
    raw := try a.Put((&LineReads).Reader(io, lm, empty))
    if d := dec {
        dbuf := try a.Slice[u8](4096)
        dr := try d.wrap(a, raw, dbuf)
        return try a.Put(dr)
    }
    return raw
}

// Decoder is the client's decoder named by the reply's Content-Encoding,
// none when it names none, names one the client lacks, or lists more than
// one coding (each is then left to the caller, undecoded).
Decoder(ds: []compress.Content, hs: []Header): ?compress.Content from ds {
    ce := Lookup(hs, "content-encoding") else {
        return none
    }
    for d in ds {
        if EqFold(ce, d.name) {
            return some(d)
        }
    }
    return none
}

// WithoutFraming is hs without Content-Encoding and Content-Length, which
// describe the encoded body the caller no longer sees.
WithoutFraming(a: &Arena, hs: []Header): OutOfMemory![]Header from a, hs *hs into a {
    var l := List[Header].New(a)
    for h in hs {
        if !EqFold(h.name, "content-encoding") && !EqFold(h.name, "content-length") {
            try l.Push(h)
        }
    }
    return l.Done()[..]
}

// RequestHead is the request line and the fields, ending with the blank
// line: the target from u's path and query (in absolute form, after
// "http://", Host's text, when proxied and u is http, RFC 9112 3.2.2),
// Host from u, the caller's fields but those the client writes and, when
// strip, the credentials (and Proxy-Authorization when proxied and u is
// https: in the tunnel it would reach the origin), then Accept-Encoding
// and the framing: Content-Length (body's, or slen for a streamed body),
// or Transfer-Encoding: chunked for a streamed body of no stated length.
RequestHead(a: &Arena, method: str, u: &url.Url, headers: []Header, body: []u8, strip: bool, proxied: bool, decoders: []compress.Content, streamed: bool, slen: ?i64): OutOfMemory!str from a {
    tunnel := proxied && u.Scheme() == "https"
    var t := Text.New(a)
    try t.Push(method)
    try t.Push(" ")
    if proxied && !tunnel {
        try t.Push("http://")
        try t.Push(u.Hostname())
        if p := u.Port() {
            try t.Push(try a.Fmt(":{p}", (p)))
        }
    }
    try PushTarget(a, mut t, u.Path(), false)
    q := u.Query()
    if q.Len() > 0 {
        try t.Push("?")
        try PushTarget(a, mut t, q, true)
    }
    try t.Push(" HTTP/1.1\r\nHost: ")
    try t.Push(u.Hostname())
    if p := u.Port() {
        try t.Push(try a.Fmt(":{p}", (p)))
    }
    try t.Push("\r\n")
    for h in headers {
        if !IsToken(h.name) || ClientOwned(h.name) || strip && Credential(h.name) || tunnel && EqFold(h.name, "proxy-authorization") || decoders.Len() > 0 && EqFold(h.name, "accept-encoding") {
            continue
        }
        try t.Push(h.name)
        try t.Push(": ")
        try t.Push(try CleanValue(a, h.value))
        try t.Push("\r\n")
    }
    if decoders.Len() > 0 {
        try t.Push("Accept-Encoding: ")
        for i in 0..decoders.Len() {
            if i > 0 {
                try t.Push(", ")
            }
            try t.Push(decoders[i].name)
        }
        try t.Push("\r\n")
    }
    if streamed {
        if n := slen {
            try t.Push(try a.Fmt("Content-Length: {n}\r\n", (n)))
        } else {
            try t.Push("Transfer-Encoding: chunked\r\n")
        }
    } else if body.Len() > 0 || method == "POST" || method == "PUT" || method == "PATCH" {
        try t.Push(try a.Fmt("Content-Length: {n}\r\n", (n: body.Len())))
    }
    try t.Push("\r\n")
    return t.Done()
}

// PushTarget pushes s, a path (or a query, when query) of a parsed URL, as
// the request target's bytes: each byte RFC 3986 allows there as it is
// (IsPathByte, and "?" in a query; a "%" only before two hex digits), any
// other as "%XX" (RFC 3986 2.1). The URL Standard leaves bytes in a path
// or query that RFC 3986 does not allow ("|", "[", "]", "{", "}", "^",
// "`", a backslash), which ParseTarget, and so ServeWith, refuses;
// Go's client escapes them too. O(s.Len()); allocates only when a byte is
// escaped, three times s in a.
PushTarget(a: &Arena, t: mut Text, s: str, query: bool): OutOfMemory!void {
    b := s.AsBytes()
    var clean := true
    for i in 0..b.Len() {
        if !TargetByte(b, i, query) {
            clean = false
            break
        }
    }
    if clean {
        try t.Push(s)
        return
    }
    var out := try a.Slice[u8](3 * b.Len())
    var n := 0
    for i in 0..b.Len() {
        c := b[i]
        if TargetByte(b, i, query) {
            out[n] = c
            n += 1
        } else {
            out[n] = 0x25
            out[n + 1] = UpperHex(Cast[int](c >> 4))
            out[n + 2] = UpperHex(Cast[int](c & 15))
            n += 3
        }
    }
    // Every byte written is ASCII, so the text is UTF-8.
    text := rt.StrFrom(out[0..n]) else {
        fail OutOfMemory
    }
    try t.Push(text)
}

// TargetByte: b[i] may stand as it is in a request target's path (or
// query, when query): IsPathByte, "?" in a query, and a "%" only when two
// hex digits follow (CheckEscape's rule).
TargetByte(b: []u8, i: int, query: bool): bool {
    c := b[i]
    if c == 0x25 {
        return i + 2 < b.Len() && HexVal(b[i + 1]) >= 0 && HexVal(b[i + 2]) >= 0
    }
    return IsPathByte(c) || query && c == 0x3F
}

// UpperHex is the upper-case hex digit of d, 0 to 15 (RFC 3986 2.1).
UpperHex(d: int): u8 {
    if d < 10 {
        return Trunc[u8](0x30 + d)
    }
    return Trunc[u8](0x41 + d - 10)
}

// ClientOwned: a field the client writes itself (RFC 9112 6.1, 9.6).
ClientOwned(name: str): bool {
    return EqFold(name, "host") || EqFold(name, "connection") || EqFold(name, "content-length") || EqFold(name, "transfer-encoding") || EqFold(name, "keep-alive") || EqFold(name, "upgrade") || EqFold(name, "te")
}

// Credential: a field not sent to another origin after a redirect.
Credential(name: str): bool {
    return EqFold(name, "authorization") || EqFold(name, "proxy-authorization") || EqFold(name, "cookie")
}

// OriginOf is the pool key of scheme://host:port, n = -1 when it does not
// fit MaxOrigin.
OriginOf(scheme: str, host: str, port: int): Origin {
    var o := Origin(b: [0; 280], n: -1)
    var digits: [5]u8 = [0; 5]
    var d := 5
    var x := port
    for {
        d -= 1
        digits[d] = Trunc[u8](0x30 + x % 10)
        x = x / 10
        if x == 0 || d == 0 {
            break
        }
    }
    need := scheme.Len() + 3 + host.Len() + 1 + (5 - d)
    if need > MaxOrigin {
        return o
    }
    var n := 0
    for part in [scheme.AsBytes(), "://".AsBytes(), host.AsBytes(), ":".AsBytes(), digits[d..5]] {
        (mut o.b[n..n + part.Len()]).CopyFrom(part)
        n += part.Len()
    }
    o.n = n
    return o
}

// OriginEq: two keys that fit name the same origin.
OriginEq(x: &Origin, y: &Origin): bool {
    if x.n < 0 || x.n != y.n {
        return false
    }
    for i in 0..x.n {
        if x.b[i] != y.b[i] {
            return false
        }
    }
    return true
}

// Connect makes a connection to host:port within the connect limit
// (time.TimedOut past it): Open's resolve, dial and handshake.
Connect(h: &Heap, lim: &ClientLimits, a: &Arena, io: Io, host: str, port: int, https: bool): Error!net.Conn from h {
    bare := if host.Len() > 2 && host.Byte(0) == 0x5B { host[1..host.Len() - 1] } else { host }
    var out: ?net.Conn = none
    var err: ?Error = none
    var expired := false
    scope s := io.Scope() {
        s.CancelAfter(lim.connect)
        DropConn(Replace(mut out, Open(h, lim, a, io, bare, port, https) else e {
            err = some(e)
            canceled := match e {
                Canceled { true }
                _ { false }
            }
            expired = task.LimitHit(&s, canceled)
            none
        }))
    }
    match out {
        some(c) {
            return c
        }
        none {}
    }
    if expired {
        fail time.TimedOut
    }
    if e := err {
        fail e
    }
    fail Canceled
}

// Open dials host:port, or the proxy and through it (Reach), and, for
// https, hands the connection to the secure connector with host as the
// name to check; a failed handshake releases the connection.
Open(h: &Heap, lim: &ClientLimits, a: &Arena, io: Io, host: str, port: int, https: bool): Error!?net.Conn from h {
    c := try Reach(lim, a, io, host, port, https)
    if !https {
        return some(c)
    }
    sec := lim.secure else {
        c.Abandon()
        fail NoSecureTransport
    }
    r := sec.Connect(h, io, c, host) else e {
        c.Abandon()
        fail e
    }
    return some(r)
}

// Dial resolves host and dials the first address allow lets through:
// Refused when allow refuses every address, else the last dial's error.
Dial(lim: &ClientLimits, a: &Arena, io: Io, host: str, port: int): Error!net.Conn from io {
    addrs := try net.Resolve(a, io, host)
    var last: Error = net.Refused
    for x in addrs.Items() {
        var ad := x
        ad.port = Trunc[u16](port)
        if f := lim.allow {
            if !f(&ad) {
                continue
            }
        }
        st := net.Dial(io, try a.Fmt("{ad}", (ad))) else e {
            match e {
                Canceled {
                    fail e
                }
                _ {
                    last = e
                    continue
                }
            }
        }
        return st.Conn()
    }
    fail last
}

// Retryable: an error of a pooled connection the server may have closed
// while it was idle, not a cancel, a limit or a malformed reply.
Retryable(e: Error): bool {
    match e {
        Closed {
            return true
        }
        net.Reset {
            return true
        }
        WriteError {
            return true
        }
        _ {
            return false
        }
    }
}

// Idempotent: a method a client may send again when no reply came (RFC
// 9110 9.2.2), as Go's Transport retries only these.
Idempotent(method: str): bool {
    return method == "GET" || method == "HEAD" || method == "OPTIONS" || method == "TRACE" || method == "PUT" || method == "DELETE"
}
