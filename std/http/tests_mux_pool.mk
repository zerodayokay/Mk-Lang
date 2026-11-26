// Why: MuxSlot's lock-free 64-bit word state machine (Free/Dialing/Live/Dead,
// driven bit, 21-bit refcount, 40-bit generation) must never double-free Box[h2.Conn],
// leak references under contention, or open duplicate connections on cold start.
//
// What: unit and concurrency tests for MuxSlot, ClaimMuxOrDial cold-start
// coalescing, h2_streams overflow across slots, and GOAWAY eviction.
//
// How: tests MuxSlot transitions directly over pipe.ConnPair H2 connections and
// end-to-end against ServeH2 / ServeOn over 127.0.0.1:0.

import std.http.h2
import std.net
import std.net.pipe
import std.raw
import std.time
import std.url

test "MuxSlot word bitfield round-trip and Dead 1->0 exclusive teardown" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 4096).Must()
    defer srv_conn.Abandon()

    var s := NewMuxSlot()
    Assert(MuxStateOf(s.word.Load()) == MuxFree)
    Assert(MuxGenOf(s.word.Load()) == u64(0))

    key := OriginOf("http", "127.0.0.1", 8080)
    g0 := s.TryStartDial(key, time.Now(io)).Must()
    Assert(g0 == u64(0))
    Assert(MuxStateOf(s.word.Load()) == MuxDialing)
    Assert(s.TryStartDial(key, time.Now(io)).IsNone())

    s.AbortDial(g0)
    Assert(MuxStateOf(s.word.Load()) == MuxFree)
    Assert(MuxGenOf(s.word.Load()) == u64(1))

    g1 := s.TryStartDial(key, time.Now(io)).Must()
    Assert(g1 == u64(1))
    dlim := DefaultClientLimits
    box := h2.Conn.Client(h, io, cli_conn, ClientH2Limits(&dlim)).Must()
    s.FinishDial(a, g1, box, time.Now(io)).Must()
    Assert(MuxStateOf(s.word.Load()) == MuxLive)
    Assert(MuxRefsOf(s.word.Load()) == u64(1))

    Assert(s.ClaimRef().Must() == g1)
    Assert(s.ClaimDrive().Must() == g1)
    Assert(s.ClaimDrive().IsNone())
    Assert(MuxRefsOf(s.word.Load()) == u64(3))

    Assert(!s.MarkDead(g0))
    Assert(s.MarkDead(g1))
    Assert(MuxStateOf(s.word.Load()) == MuxDead)
    Assert(MuxRefsOf(s.word.Load()) == u64(3))
    Assert(MuxDrivenOf(s.word.Load()))
    Assert(s.ClaimRef().IsNone())
    Assert(s.ClaimDrive().IsNone())

    // Release two refs: slot stays Dead, no Box yet.
    r1 := s.ReleaseRef(a, false)
    Assert(r1.IsNone())
    DropOptH2Box(r1, a)
    r2 := s.ReleaseRef(a, true)
    Assert(r2.IsNone())
    DropOptH2Box(r2, a)
    Assert(MuxRefsOf(s.word.Load()) == u64(1))
    Assert(!MuxDrivenOf(s.word.Load()))

    taken := s.ReleaseRef(a, false).Must()
    taken.Take(a).Abandon()
    Assert(MuxStateOf(s.word.Load()) == MuxFree)
    Assert(MuxRefsOf(s.word.Load()) == u64(0))
    Assert(MuxGenOf(s.word.Load()) == u64(2))

    FreeMuxSlot(a, s)
}

MuxWorkerClaim(io: Io, h: &Heap, s: &MuxSlot, gen_: u64, tears: &raw.Atomic[int]) {
    _ = io
    a := h.Alloc()
    for _ in 0..64 {
        if cg := s.ClaimRef() {
            Assert(cg == gen_)
            if box := s.ReleaseRef(a, false) {
                box.Take(a).Abandon()
                _ = tears.Add(1)
            }
        }
        if MuxStateOf(s.word.Load()) != MuxLive {
            break
        }
    }
}

test "MuxSlot 16-task Claim/Release contention with concurrent MarkDead tears down once" (sys: &System) {
    io := sys.io
    h := sys.heap
    a := h.Alloc()
    (a: srv_conn, b: cli_conn) := pipe.ConnPair(h, 4096).Must()
    defer srv_conn.Abandon()

    var s := NewMuxSlot()
    key := OriginOf("http", "127.0.0.1", 8080)
    g := s.TryStartDial(key, time.Now(io)).Must()
    dlim := DefaultClientLimits
    box := h2.Conn.Client(h, io, cli_conn, ClientH2Limits(&dlim)).Must()
    s.FinishDial(a, g, box, time.Now(io)).Must()

    var tears := raw.Atomic[int].New(0)
    scope sc := io.Scope() {
        for _ in 0..16 {
            sc.go MuxWorkerClaim(io, h, &s, g, &tears)
        }
        io.Sleep(time.Millis(1)).Must()
        _ = s.MarkDead(g)
        if b2 := s.ReleaseRef(a, false) {
            b2.Take(a).Abandon()
            _ = tears.Add(1)
        }
    }
    Assert(tears.Load() == 1)
    Assert(MuxStateOf(s.word.Load()) == MuxFree)
    Assert(MuxGenOf(s.word.Load()) == g + u64(1))
    FreeMuxSlot(a, s)
}

MuxGateHandler(cnt: &raw.Atomic[int], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    _ = a
    _ = req
    _ = link
    _ = cnt.Add(1)
    io.Sleep(time.Millis(40)) else {}
    res.code = 200
    try res.Body("ok".AsBytes())
}

MuxFetchOne(io: Io, h: &Heap, cli: &Client, url_str: str, ok: &raw.Atomic[int]) {
    var ar := Arena.New(h).Must()
    defer ar.Free()
    pu := url.Parse(&ar, url_str, none).Must()
    hs := List[Header].New(&ar).Done()
    req := ClientRequest(method: "GET", url: &pu, headers: hs[..], body: "".AsBytes())
    r := cli.Fetch(&ar, io, &req) else {
        return
    }
    if r.status == 200 && CtSame(r.body, "ok") {
        _ = ok.Add(1)
    }
}

MuxAcceptLoop(io: Io, h: &Heap, l: &net.Listener, o: &ServerOptions, accepted: &raw.Atomic[int], cnt: &raw.Atomic[int]) {
    var empty: [0]u8 = []
    scope cs := io.Scope() {
        for {
            c := l.Accept(io) else { break }
            _ = accepted.Add(1)
            cs.go ServeH2[raw.Atomic[int], int](c.Conn(), h, io, o, cnt, h, none, some(MuxGateHandler), none, empty[..], false)
        }
    }
}

test "H2 client cold-start coalesces 16 concurrent first requests onto 1 connection" (sys: &System) {
    io := sys.io
    h := sys.heap
    var so := DefaultServerOptions
    so.h2c = true
    so.h2_streams = 32
    var l := net.Listen(io, "127.0.0.1:0").Must()
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var accepted := raw.Atomic[int].New(0)
    var cnt := raw.Atomic[int].New(0)
    var clim := DefaultClientLimits
    clim.h2c = true
    clim.h2_streams = 32
    var cli := Client.New(h, clim)
    defer cli.Close()

    var uar := Arena.New(h).Must()
    defer uar.Free()
    url_str := uar.Fmt("http://{addr}/coalesce", (addr)).Must()
    var ok := raw.Atomic[int].New(0)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        s.go MuxAcceptLoop(io, h, &l, &so, &accepted, &cnt)
        scope reqs := io.Scope() {
            for _ in 0..16 {
                reqs.go MuxFetchOne(io, h, &cli, url_str, &ok)
            }
        }
        s.Cancel()
    }
    Assert(ok.Load() == 16)
    Assert(cnt.Load() == 16)
    Assert(accepted.Load() == 1)
}

test "H2 client overflows h2_streams=2 across multiple MuxSlots" (sys: &System) {
    io := sys.io
    h := sys.heap
    var so := DefaultServerOptions
    so.h2c = true
    so.h2_streams = 2
    var l := net.Listen(io, "127.0.0.1:0").Must()
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var accepted := raw.Atomic[int].New(0)
    var cnt := raw.Atomic[int].New(0)
    var clim := DefaultClientLimits
    clim.h2c = true
    clim.h2_streams = 10
    var cli := Client.New(h, clim)
    defer cli.Close()

    var uar := Arena.New(h).Must()
    defer uar.Free()
    url_str := uar.Fmt("http://{addr}/fanout", (addr)).Must()
    var ok := raw.Atomic[int].New(0)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        s.go MuxAcceptLoop(io, h, &l, &so, &accepted, &cnt)
        // Warm up 1 request so client applies server SETTINGS(MAX_CONCURRENT_STREAMS=2).
        var warm_ok := raw.Atomic[int].New(0)
        MuxFetchOne(io, h, &cli, url_str, &warm_ok)
        Assert(warm_ok.Load() == 1)
        scope reqs := io.Scope() {
            for _ in 0..6 {
                reqs.go MuxFetchOne(io, h, &cli, url_str, &ok)
            }
        }
        s.Cancel()
    }
    Assert(ok.Load() == 6)
    Assert(cnt.Load() == 7)
    Assert(accepted.Load() >= 2)
}

MuxGoawayAcceptOne(io: Io, h: &Heap, l: &net.Listener, o: &ServerOptions, accepted: &raw.Atomic[int], cnt: &raw.Atomic[int]) {
    var empty: [0]u8 = []
    // Connection 1 is served in a scope cancelled after its first request,
    // which ends it with GOAWAY(NO_ERROR); connection 2 is served until the
    // test's scope ends.
    scope s1 := io.Scope() {
        c1 := l.Accept(io) else { return }
        _ = accepted.Add(1)
        s1.go ServeH2[raw.Atomic[int], int](c1.Conn(), h, io, o, cnt, h, none, some(MuxGateHandler), none, empty[..], false)
        for cnt.Load() < 1 {
            io.Sleep(time.Millis(2)) else { break }
        }
        s1.Cancel()
    }
    scope s2 := io.Scope() {
        c2 := l.Accept(io) else { return }
        _ = accepted.Add(1)
        s2.go ServeH2[raw.Atomic[int], int](c2.Conn(), h, io, o, cnt, h, none, some(MuxGateHandler), none, empty[..], false)
    }
}

test "H2 client retires GOAWAY slot and dials fresh MuxSlot for next request" (sys: &System) {
    io := sys.io
    h := sys.heap
    var so := DefaultServerOptions
    so.h2c = true
    var l := net.Listen(io, "127.0.0.1:0").Must()
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var accepted := raw.Atomic[int].New(0)
    var cnt := raw.Atomic[int].New(0)
    var clim := DefaultClientLimits
    clim.h2c = true
    var cli := Client.New(h, clim)
    defer cli.Close()

    var uar := Arena.New(h).Must()
    defer uar.Free()
    url_str := uar.Fmt("http://{addr}/goaway", (addr)).Must()

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        s.go MuxGoawayAcceptOne(io, h, &l, &so, &accepted, &cnt)
        var ar1 := Arena.New(h).Must()
        defer ar1.Free()
        pu1 := url.Parse(&ar1, url_str, none).Must()
        hs1 := List[Header].New(&ar1).Done()
        req1 := ClientRequest(method: "GET", url: &pu1, headers: hs1[..], body: "".AsBytes())
        r1 := cli.Fetch(&ar1, io, &req1).Must()
        Assert(r1.status == 200)
        // Let connection 1 end with its GOAWAY.
        io.Sleep(time.Millis(30)).Must()
        var ar2 := Arena.New(h).Must()
        defer ar2.Free()
        pu2 := url.Parse(&ar2, url_str, none).Must()
        hs2 := List[Header].New(&ar2).Done()
        req2 := ClientRequest(method: "GET", url: &pu2, headers: hs2[..], body: "".AsBytes())
        r2 := cli.Fetch(&ar2, io, &req2).Must()
        Assert(r2.status == 200)
        s.Cancel()
    }
    Assert(accepted.Load() == 2)
    Assert(cnt.Load() == 2)
}

MuxDriveTask(c: &Client, io: Io) {
    c.Drive(io) else {}
}

// MuxWaitDriven waits up to 2 s for slot i's driven bit to be want.
MuxWaitDriven(io: Io, c: &Client, i: int, want: bool): bool {
    for _ in 0..1000 {
        if MuxDrivenOf(MuxSlotAt(&c.mux, i).word.Load()) == want {
            return true
        }
        io.Sleep(time.Millis(2)) else {
            return false
        }
    }
    return false
}

test "Client.Drive drives a pooled h2c connection, returns after Stop, and the connection stays pooled" (sys: &System) {
    io := sys.io
    h := sys.heap
    var so := DefaultServerOptions
    so.h2c = true
    var l := net.Listen(io, "127.0.0.1:0").Must()
    defer l.Close()
    var ab: [64]u8 = [0; 64]
    addr := ThAddr(mut ab, "127.0.0.1:", l.Port())

    var accepted := raw.Atomic[int].New(0)
    var cnt := raw.Atomic[int].New(0)
    var clim := DefaultClientLimits
    clim.h2c = true
    var cli := Client.New(h, clim)
    defer cli.Close()

    var uar := Arena.New(h).Must()
    defer uar.Free()
    url_str := uar.Fmt("http://{addr}/drive", (addr)).Must()
    var ok := raw.Atomic[int].New(0)

    scope s := io.Scope() {
        s.CancelAfter(time.Seconds(10))
        s.go MuxAcceptLoop(io, h, &l, &so, &accepted, &cnt)
        scope d := io.Scope() {
            d.go MuxDriveTask(&cli, io)
            MuxFetchOne(io, h, &cli, url_str, &ok)
            Assert(MuxWaitDriven(io, &cli, 0, true))
            cli.Stop()
        }
        // Drive returned: its task left the slot Live and dropped the bit.
        w := MuxSlotAt(&cli.mux, 0).word.Load()
        Assert(MuxStateOf(w) == MuxLive)
        Assert(!MuxDrivenOf(w))
        MuxFetchOne(io, h, &cli, url_str, &ok)
        s.Cancel()
    }
    Assert(ok.Load() == 2)
    Assert(accepted.Load() == 1)
}
