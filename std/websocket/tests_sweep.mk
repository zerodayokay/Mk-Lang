// Why: every operation's behaviour under cancellation is part of its
// contract: Recv is repeatable, Send leaves the connection failed, Dial and
// Accept cancel cleanly (a prefix of their work, released).
// What: a cancel sweep of Conn.Recv, Send, Dial and Accept.
// How: under std.sim, each operation is interrupted at each await point in
// turn, counted so a sweep that cancels nowhere cannot pass.

import std.core
import std.http
import std.net
import std.sim
import std.time
import std.url

// Cut is a transport over inner (none: a sink whose reads never end) that
// numbers its Read and WriteAll calls in count. Call at waits in a 100 s
// Sleep, after a WriteAll's first half went through, for the cancel CutAt
// sends: the operation above is interrupted exactly at that await point.
linear view struct Cut {
    inner: ?net.Conn
    count: &Mutex[int]
    at: int
}

// Tick adds one call to count: the call's number, from 1.
Tick(count: &Mutex[int], io: Io): int {
    var g := count.Lock(io.NoCancel()) else {
        return 0
    }
    *g.Mut() += 1
    n := *g.Ref()
    g.Unlock()
    return n
}

// Seen is the calls count holds.
Seen(count: &Mutex[int], io: Io): int {
    g := count.Lock(io.NoCancel()) else {
        return 0
    }
    n := *g.Ref()
    g.Unlock()
    return n
}

impl net.Transport for Cut {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        if Tick(self.count, io) == self.at {
            try io.Sleep(time.Seconds(100))
        }
        if c := &self.inner {
            return try c.Read(io, buf)
        }
        try io.Sleep(time.Seconds(100))
        return 0
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        if Tick(self.count, io) == self.at {
            if c := &self.inner {
                try c.WriteAll(io, data[0..data.Len() / 2])
            }
            try io.Sleep(time.Seconds(100))
        }
        if c := &self.inner {
            try c.WriteAll(io, data)
        }
    }
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        if c := &self.inner {
            try c.Shutdown(io, how)
        }
    }
    PeerAddr(&self): ?net.Addr {
        return none
    }
    // The inner Conn is a stream's, whose Close only releases.
    Close(&self, io: Io): net.Error!void {
    }
    Abandon(self) {
        Cut(inner, count: _, at: _) := self
        match inner {
            some(c) {
                c.Abandon()
            }
            none {}
        }
    }
}

// SinkConn is a net.Conn over a Cut sink on h.
SinkConn(h: &Heap, count: &Mutex[int], at: int): OutOfMemory!net.Conn from h, count {
    slot := try net.ReserveConn[Cut](h)
    return slot.Fill(Cut(inner: none, count: count, at: at))
}

// CutOver is a net.Conn over a Cut over inner on h; on failure inner stays
// the caller's.
CutOver(h: &Heap, inner: net.Conn, count: &Mutex[int], at: int): OutOfMemory!net.Conn from h, inner, count {
    slot := try net.ReserveConn[Cut](h)
    return slot.Fill(Cut(inner: some(inner), count: count, at: at))
}

// CutAt cancels s once count reaches at, looking every 50 us of the Sim's
// time.
CutAt(s: &Scope, io: Io, count: &Mutex[int], at: int) {
    for {
        io.Sleep(core.DurationOf(50_000)) else {
            return
        }
        if Seen(count, io) >= at {
            s.Cancel()
            return
        }
    }
}

// SendAt sends data on a Conn over a Cut sink cut at call at, client side
// or not: CodeOf the Send's error (-1 when it returned), CodeOf the next
// Recv's error after a failure (-1 otherwise), and the calls the transport
// saw.
SendAt(sys: &System, io: Io, client: bool, data: []u8, at: int): Error!(code: int, later: int, calls: int) {
    count := Mutex[int].New(0)
    o := DefaultOptions
    c := try SinkConn(sys.heap, &count, at)
    ws := Wrap(sys.heap, c, client, &o, "".AsBytes()) else e {
        c.Abandon()
        fail e
    }
    var code := -1
    scope s := io.Scope() {
        s.go CutAt(&s, io, &count, at)
        ws.Send(io, Kind.Binary, data) else e {
            code = CodeOf(e)
        }
        s.Cancel()
    }
    calls := Seen(&count, io)
    later := if code == -1 { -1 } else { RecvCode(&ws, sys.arena, io) }
    ws.Abandon()
    return (code: code, later: later, calls: calls)
}

test "Send interrupted at each write it makes fails with Canceled and leaves the connection failed: 3 chunk writes for 40,000 bytes from a client, header and data for a server, one write for 100 bytes" (sys: &System) {
    var sm := try sim.Sim.New(sys.arena, 50)
    defer sm.Free()
    io := sm.Io()
    data := try Filled(sys.arena, 40000)
    for client in [true, false] {
        for size in [100, 40000] {
            // The writes one Send makes: the client masks 16 KiB a write; a
            // server writes a small frame whole, a large one as its header
            // and its data.
            want := if size == 100 { 1 } else if client { 3 } else { 2 }
            var cuts := 0
            for at in 1..10 {
                (code, later, calls) := try SendAt(sys, io, client, data[0..size], at)
                if code == -1 {
                    Assert(calls == want)
                    break
                }
                Assert(code == 3 && later == 3 && calls == at)
                cuts += 1
            }
            Assert(cuts == want)
        }
    }
}

test "a Send cancelled while it waits for the write lock sends nothing and leaves the connection usable" (sys: &System) {
    var sm := try sim.Sim.New(sys.arena, 51)
    defer sm.Free()
    io := sm.Io()
    o := DefaultOptions
    for client in [true, false] {
        count := Mutex[int].New(0)
        c := try SinkConn(sys.heap, &count, 1 << 30)
        ws := Wrap(sys.heap, c, client, &o, "".AsBytes()) else e {
            c.Abandon()
            fail e
        }
        onerr ws.Abandon()
        hv := ws.box.Ref()
        try hv.wl.Acquire(io, 1)
        var code := -1
        scope s := io.Scope() {
            s.go CancelAt(&s, io, 1_000_000)
            ws.Send(io, Kind.Text, "x".AsBytes()) else e {
                code = CodeOf(e)
            }
        }
        Assert(code == 3 && Seen(&count, io) == 0)
        hv.wl.Release(1)
        try ws.Send(io, Kind.Text, "x".AsBytes())
        try ws.Ping(io, "p".AsBytes())
        Assert(Seen(&count, io) == 2)
        ws.Abandon()
    }
}

// Masked is the frames a client sends for size bytes of Filled in parts
// frames, masked with key 1 2 3 4: their bytes in a.
Masked(a: &Arena, data: []u8, size: int, parts: int): OutOfMemory![]u8 from a {
    key: [4]u8 = [1, 2, 3, 4]
    step := (size + parts - 1) / parts
    buf := try a.Slice[u8](size + 14 * parts)
    var at := 0
    var n := 0
    for p in 0..parts {
        end := if p == parts - 1 { size } else { at + step }
        op: u8 = if p == 0 { OpBinary } else { OpContinue }
        var hd: [14]u8 = [0; 14]
        hn := PutHead(mut hd[..], op, end - at, true, key)
        if p != parts - 1 {
            hd[0] = hd[0] & 0x7F
        }
        (mut buf[n..n + hn]).CopyFrom(hd[0..hn])
        n += hn
        (mut buf[n..n + end - at]).CopyFrom(data[at..end])
        _ = Mask(mut buf[n..n + end - at], key, 0)
        n += end - at
        at = end
    }
    return buf[0..n]
}

// CutRecv1 is one Recv of c under a scope CutAt cancels at call at: the
// message's length, or -1 when it was Canceled.
CutRecv1(c: &Conn, a: &Arena, io: Io, count: &Mutex[int], at: int): int {
    var got := -1
    scope inner := io.Scope() {
        inner.go CutAt(&inner, io, count, at)
        m := c.Recv(a, io) else e {
            Assert(CodeOf(e) == 3)
            return -1
        }
        Assert(IsPattern(m.data))
        got = m.data.Len()
        inner.Cancel()
    }
    return got
}

// RecvAt has a raw peer feed b in six pieces 1 ms apart to a server over a
// Cut cut at read at, receives under a scope CutAt cancels, and receives
// again when that was interrupted: the length delivered, whether the first
// Recv was cut, and the reads the transport saw.
RecvAt(sys: &System, b: []u8, at: int): Error!(got: int, cut: bool, calls: int) {
    // One seed for every cut, so each run is the uninterrupted one up to
    // its cut.
    var sm := try sim.Sim.New(sys.arena, 55)
    defer sm.Free()
    io := sm.Io()
    o := DefaultOptions
    count := Mutex[int].New(0)
    (c, s) := try Loop(io)
    onerr c.Abandon()
    t := CutOver(sys.heap, s, &count, at) else e {
        s.Abandon()
        fail e
    }
    sv := Wrap(sys.heap, t, false, &o, "".AsBytes()) else e {
        t.Abandon()
        fail e
    }
    onerr sv.Abandon()
    var got := -1
    var cut := false
    scope outer := io.Scope() {
        outer.go Feed(&c, io, b, 6, 1_000_000)
        got = CutRecv1(&sv, sys.arena, io, &count, at)
        cut = got == -1
        if cut {
            m2 := try sv.Recv(sys.arena, io)
            Assert(IsPattern(m2.data))
            got = m2.data.Len()
        }
    }
    calls := Seen(&count, io)
    sv.Abandon()
    c.Abandon()
    return (got: got, cut: cut, calls: calls)
}

test "Recv is repeatable at each read it makes: cut at every one, one frame or three, short, past the direct-read size or past the first 64 KiB reservation, the next Recv delivers the message whole" (sys: &System) {
    // 80,000 bytes reach Whole's growth of its reservation and Keep after
    // it (one frame), and Gather's Extend across frames (three).
    data := try Filled(sys.arena, 80000)
    for parts in [1, 3] {
        for size in [5, 300, 20000, 80000] {
            b := try Masked(sys.arena, data, size, parts)
            // The reads of an uninterrupted Recv, then a cut at each.
            (got: full, cut: none_cut, calls: reads) := try RecvAt(sys, b, 1 << 30)
            Assert(full == size && !none_cut && reads >= 1)
            for at in 1..reads + 1 {
                (got, cut, calls: _) := try RecvAt(sys, b, at)
                Assert(cut && got == size)
            }
        }
    }
}

test "A second Recv waits for the first; a cancel of the waiter loses nothing, and each message reaches one Recv whole" (sys: &System) {
    var sm := try sim.Sim.New(sys.arena, 52)
    defer sm.Free()
    io := sm.Io()
    o := DefaultOptions
    (ws: sv, raw: peer) := try RawPair(sys.heap, io, false, &o)
    onerr sv.Abandon()
    onerr peer.Abandon()
    data := try Filled(sys.arena, 3000)
    one := try Masked(sys.arena, data, 3000, 3)
    first := Mutex[int].New(-1)
    var waiter := -1
    scope s := io.Scope() {
        // The first Recv holds the read half; the waiter is cancelled at
        // 2 ms, before the message starts at 3 ms.
        s.go FirstRecv(&sv, sys.heap, io, &first)
        scope w := io.Scope() {
            w.go CancelAt(&w, io, 2_000_000)
            io.Sleep(core.DurationOf(1_000_000)) else {}
            waiter = RecvCode(&sv, sys.arena, io)
        }
        io.Sleep(core.DurationOf(1_000_000)) else {}
        try peer.WriteAll(io, one)
    }
    Assert(waiter == 3 && Seen(&first, io) == 3000)
    // Two Recvs at once, two messages: each gets one, whole.
    a := Mutex[int].New(-1)
    b := Mutex[int].New(-1)
    scope s2 := io.Scope() {
        s2.go FirstRecv(&sv, sys.heap, io, &a)
        s2.go FirstRecv(&sv, sys.heap, io, &b)
        io.Sleep(core.DurationOf(1_000_000)) else {}
        try peer.WriteAll(io, one)
        try peer.WriteAll(io, one)
    }
    Assert(Seen(&a, io) == 3000 && Seen(&b, io) == 3000)
    sv.Abandon()
    peer.Abandon()
}

// FirstRecv receives one message on c into an arena of its own and stores
// its length in got when it is Pattern's (-2 when it is not; got keeps
// -1 on an error).
FirstRecv(c: &Conn, h: &Heap, io: Io, got: &Mutex[int]) {
    var a := Arena.New(h) else {
        return
    }
    m := c.Recv(&a, io) else {
        a.Free()
        return
    }
    n := if IsPattern(m.data) { m.data.Len() } else { -2 }
    a.Free()
    var g := got.Lock(io.NoCancel()) else {
        return
    }
    *g.Mut() = n
    g.Unlock()
}

// Trip is a transport over inner that fails its call at with Canceled by
// itself (a WriteAll after its first half went through), as the call of a
// cancelled Io returns: the transport a connector makes, which holds no
// reference to count with.
linear view struct Trip {
    inner: net.Conn
    calls: Mutex[int]
    at: int
}

impl net.Transport for Trip {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        if Tick(&self.calls, io) == self.at {
            fail Canceled
        }
        return try self.inner.Read(io, buf)
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        if Tick(&self.calls, io) == self.at {
            try self.inner.WriteAll(io, data[0..data.Len() / 2])
            fail Canceled
        }
        try self.inner.WriteAll(io, data)
    }
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        try self.inner.Shutdown(io, how)
    }
    PeerAddr(&self): ?net.Addr {
        return self.inner.PeerAddr()
    }
    Close(&self, io: Io): net.Error!void {
    }
    Abandon(self) {
        Trip(inner, calls, at: _) := self
        _ = calls.Into()
        inner.Abandon()
    }
}

// TripSec is a connector's state: its handshake is the sweep's first
// point, and the connection it gives is a Trip cut at at - 1.
struct TripSec {
    at: int
}

type TripType = fn(&TripSec, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn

// TripConnect fails with Canceled when at is 1, the connection staying
// the caller's, and otherwise wraps it in a Trip.
TripConnect(s: &TripSec, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    if s.at == 1 {
        fail Canceled
    }
    slot := try net.ReserveConn[Trip](h)
    return (conn: slot.Fill(Trip(inner: conn, calls: Mutex[int].New(0), at: s.at - 1)), chosen: -1)
}

// DialAt dials wss://127.0.0.1:7600/ through a TripSec cut at at against
// a RawServer: CodeOf Dial's error, -1 when Dial returned a Conn.
DialAt(sys: &System, io: Io, at: int): Error|url.Error!int {
    sec := TripSec(at: at)
    var o := DefaultOptions
    o.secure = some(net.Secure.Of[TripSec](core.ErasedCall.Of[TripSec, TripType](&sec, TripConnect)))
    u := try url.Parse(sys.arena, "wss://127.0.0.1:7600/chat", none)
    l := try net.Listen(io, Addr)
    defer l.Close()
    var code := -1
    scope srv := io.Scope() {
        srv.go RawServer(&l, io, "")
        c := Dial(sys.heap, sys.arena, io, &u, &o) else e {
            code = CodeOf(e)
            srv.Cancel()
            return code
        }
        c.Abandon()
    }
    return code
}

// DialEarly dials u under a scope cancelled before the call: CodeOf
// Dial's error, -1 when it returned a Conn.
DialEarly(sys: &System, io: Io, u: &url.Url, o: &Options): int {
    var code := -1
    scope s := io.Scope() {
        s.Cancel()
        c := Dial(sys.heap, sys.arena, io, u, o) else e {
            code = CodeOf(e)
            return code
        }
        c.Abandon()
    }
    return code
}

test "Dial interrupted at each await point fails with Canceled and releases what it made: under a cancelled scope, in the connector's handshake, the request's write and each read of the reply" (sys: &System) {
    var sm := try sim.Sim.New(sys.arena, 53)
    defer sm.Free()
    io := sm.Io()
    // At the first point, whichever it is: the scope is cancelled before
    // the call.
    o := DefaultOptions
    u := try url.Parse(sys.arena, "ws://127.0.0.1:7600/chat", none)
    Assert(DialEarly(sys, io, &u, &o) == 3)
    // Then at the connector, the request's write and the reply's one read
    // (the server writes its 101 before the client reads, so the Sim gives
    // it whole): three points, the counting heap finding no leak, and the
    // sweep ends where Dial works.
    var cuts := 0
    for at in 1..20 {
        code := try DialAt(sys, io, at)
        if code == -1 {
            break
        }
        Assert(code == 3)
        cuts += 1
    }
    Assert(cuts == 3)
}

// AcceptAt accepts req over a Taken of a Cut sink, under a scope cancelled
// before the call when early, else by CutAt at the first write: CodeOf
// Accept's error, or its Handshake status, and the Taken then abandoned by
// the caller, whose it still is; -1 when Accept returned.
AcceptAt(sys: &System, io: Io, req: &http.Request, early: bool): Error!int {
    count := Mutex[int].New(0)
    c := try SinkConn(sys.heap, &count, 1)
    t := http.Taken(conn: c, rest: "".AsBytes())
    var code := -1
    scope s := io.Scope() {
        if early {
            s.Cancel()
        } else {
            s.go CutAt(&s, io, &count, 1)
        }
        ws := Accept(sys.heap, io, req, t, DefaultOptions.protocols, AllowAll) else e {
            code = match e {
                Handshake(status) { status }
                _ { CodeOf(e) }
            }
            t.Abandon()
            return code
        }
        ws.Abandon()
    }
    return code
}

test "Accept interrupted in its one write, or under a scope cancelled before it, fails and leaves the Taken with its caller; a refusal so interrupted still reports its status" (sys: &System) {
    var sm := try sim.Sim.New(sys.arena, 54)
    defer sm.Free()
    io := sm.Io()
    hs := try Upgrade(sys.arena, "13")
    req := http.Request(method: "GET", path: "/chat", query: "", headers: hs, params: hs[0..0], body: "".AsBytes())
    old := try Upgrade(sys.arena, "8")
    r8 := http.Request(method: "GET", path: "/chat", query: "", headers: old, params: old[0..0], body: "".AsBytes())
    for early in [false, true] {
        Assert(try AcceptAt(sys, io, &req, early) == 3)
        Assert(try AcceptAt(sys, io, &r8, early) == 426)
    }
}
