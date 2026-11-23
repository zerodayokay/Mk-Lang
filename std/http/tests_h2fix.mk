// Why: regressions in the server's time limits and Flush once found.
// What: keep-alive and request-header limits kept apart, the lingering
// close's total bound, a Flush after a failed one, one port write per chunk.
// How: a simulated Io with a raw peer, so the bytes and the times are
// exactly the test's. Run by mk test --std=std.http.

import std.core
import std.net
import std.raw
import std.time

// HxReq is a request with no body that ServeWith answers 200 (SwEcho).
const HxReq = "GET / HTTP/1.1\r\nHost: x\r\n\r\n"

// HxReplyStatus reads one reply with no body from c, up to its blank line:
// its status, 0 when the connection ends first or a read fails.
HxReplyStatus(io: Io, c: &net.Stream): int {
    var b: [512]u8 = [0; 512]
    var n := 0
    for CtHeadEnd(b[0..n]) < 0 {
        if n == b.Len() {
            return 0
        }
        k := c.Read(io, mut b[n..b.Len()]) else {
            return 0
        }
        if k == 0 {
            return 0
        }
        n += k
    }
    if n < 12 {
        return 0
    }
    return int(b[9] - 48) * 100 + int(b[10] - 48) * 10 + int(b[11] - 48)
}

// HxServe serves one connection on port with keep_alive 10 s and
// request_header 1 s, while client runs; client's result.
HxServe(io: Io, h: &Heap, port: str, client: fn(Io, str): bool): bool {
    l := net.Listen(io, port) else {
        return false
    }
    var o := DefaultServerOptions
    o.keep_alive = time.Seconds(10)
    o.request_header = time.Seconds(1)
    var ok := false
    scope top := io.Scope() {
        top.go SwServeOne(io, &l, h, &o)
        ok = client(io, port)
        top.Cancel()
    }
    l.Close()
    return ok
}

// HxKeepClient: a second request 2 s after the first reply, inside
// keep_alive (10 s) but past request_header (1 s), is answered.
HxKeepClient(io: Io, port: str): bool {
    c := net.Dial(io, port) else {
        return false
    }
    var ok := CtSend(io, &c, HxReq) && HxReplyStatus(io, &c) == 200
    ok = ok && CtNap(io, 2000) && CtSend(io, &c, HxReq) && HxReplyStatus(io, &c) == 200
    c.Close()
    return ok
}

HxKeep(io: Io, h: &Heap): bool {
    return HxServe(io, h, "127.0.0.1:7500", HxKeepClient)
}

test "a kept-alive connection waits keep_alive for the next request, not request_header" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), HxKeep))
    }
}

// HxSplitClient: after 2 s idle (inside keep_alive), a head in two writes
// 5 ms apart is answered: request_header runs from its first byte, not
// from the start of the wait.
HxSplitClient(io: Io, port: str): bool {
    c := net.Dial(io, port) else {
        return false
    }
    ok := CtNap(io, 2000) && CtSend(io, &c, "GET / HTTP/1.1\r\n") && CtNap(io, 5) && CtSend(io, &c, "Host: x\r\n\r\n") && HxReplyStatus(io, &c) == 200
    c.Close()
    return ok
}

HxSplit(io: Io, h: &Heap): bool {
    return HxServe(io, h, "127.0.0.1:7501", HxSplitClient)
}

test "request_header runs from a request's first byte, so a head split across reads after an idle wait is answered" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), HxSplit))
    }
}

// HxStallClient: after 2 s idle, a head whose rest never comes is closed
// once request_header (1 s) passes from its first byte: no reply, and the
// end comes between 1 s and 2 s after that byte.
HxStallClient(io: Io, port: str): bool {
    c := net.Dial(io, port) else {
        return false
    }
    var ok := CtNap(io, 2000) && CtSend(io, &c, "GET / HTTP/1.1\r\n")
    t0 := time.Now(io)
    ok = ok && HxReplyStatus(io, &c) == 0
    took := time.Now(io).Since(t0).Nanos()
    c.Close()
    return ok && took >= 900_000_000 && took < 2_000_000_000
}

HxStall(io: Io, h: &Heap): bool {
    return HxServe(io, h, "127.0.0.1:7502", HxStallClient)
}

test "a head left unfinished after its first byte is still closed once request_header passes" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), HxStall))
    }
}

// HxLingerClient: a head over header_bytes gets 431; the client then
// sends a byte every 400 ms, each inside the 500 ms idle limit. The
// server's lingering close ends at LingerTime (30 s) all the same: the
// client's write fails between 30 s and 32 s after the reply.
HxLingerClient(io: Io, port: str): bool {
    c := net.Dial(io, port) else {
        return false
    }
    var ok := SwSendHead(io, &c, 2000, false) && SwStatus(io, &c) == 431
    t0 := time.Now(io)
    var took: i64 = 0
    for ok {
        if !CtNap(io, 400) {
            ok = false
            break
        }
        took = time.Now(io).Since(t0).Nanos()
        if took > 60_000_000_000 {
            break
        }
        if !CtSend(io, &c, "x") {
            break
        }
    }
    c.Close()
    return ok && took >= 30_000_000_000 && took < 32_000_000_000
}

HxLinger(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7503") else {
        return false
    }
    var o := DefaultServerOptions
    o.header_bytes = 1024
    var ok := false
    scope top := io.Scope() {
        top.go SwServeOne(io, &l, h, &o)
        ok = HxLingerClient(io, "127.0.0.1:7503")
        top.Cancel()
    }
    l.Close()
    return ok
}

test "the lingering close after a 431 ends at LingerTime though the client sends a byte before each idle limit" (sys: &System) {
    Assert(CtSim(sys, 1, HxLinger))
}

// HxFlushTwice: the first Flush runs in a scope already cancelled and
// fails; a second Flush in a live scope must fail too and send nothing.
HxFlushTwice(c: &SwApp, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): Error!void {
    try res.Body("abc".AsBytes())
    scope s := io.Scope() {
        s.Cancel()
        res.Flush(io, link) else {}
    }
    // Bytes added after the failed Flush: the second Flush must not send
    // them as a chunk on a stream whose head never went out.
    FlAdd(res, "de")
    try res.Flush(io, link)
}

// HxFlushServeOne serves one connection with HxFlushTwice.
HxFlushServeOne(io: Io, l: &net.Listener, h: &Heap) {
    st := l.Accept(io) else {
        return
    }
    app := SwApp(n: 0)
    ServeOwned(st.Conn(), h, io, &DefaultServerOptions, &app, HxFlushTwice)
}

// HxFlushCase: the peer gets no byte at all, so no status line after the
// failed Flush.
HxFlushCase(io: Io, h: &Heap): bool {
    l := net.Listen(io, "127.0.0.1:7504") else {
        return false
    }
    var got := -1
    scope top := io.Scope() {
        top.go HxFlushServeOne(io, &l, h)
        got = HxDrain(io, "127.0.0.1:7504")
    }
    l.Close()
    return got == 0
}

// HxDrain sends one request to port and counts the bytes that come back
// before the connection ends; -1 when the dial fails.
HxDrain(io: Io, port: str): int {
    c := net.Dial(io, port) else {
        return -1
    }
    var n := 0
    if CtSend(io, &c, HxReq) {
        var b: [256]u8 = [0; 256]
        for {
            k := c.Read(io, mut b[..]) else {
                break
            }
            if k == 0 {
                break
            }
            n += k
        }
    }
    c.Close()
    return n
}

test "a Flush after a failed Flush fails and sends nothing, so no second status line reaches the client" (sys: &System) {
    for seed in 1..4 {
        Assert(CtSim(sys, Cast[u64](seed), HxFlushCase))
    }
}

// HxCount counts the calls a Writer makes to its port.
struct HxCount {
    calls: raw.Atomic[u32]
}

// HxCountWrite counts one port write.
HxCountWrite(c: &HxCount, io: Io, b: []u8): WriteError!void {
    // Edge: none needed; one task writes and reads the count.
    _ = c.calls.Add(1)
}

// HxCountFlush: nothing to flush.
HxCountFlush(c: &HxCount, io: Io): WriteError!void {
}

const HxCounts = core.WritePort[HxCount].New(HxCountWrite, HxCountFlush)

test "a chunked body costs one port write per 16 KiB chunk, not three, through a 4 KiB Writer" (sys: &System) {
    a := sys.arena
    var body := a.Slice[u8](65536).Must()
    var res := Response.New(a)
    res.Body(body[..]).Must()
    c := HxCount(calls: raw.Atomic[u32].New(0))
    var wb: [4096]u8 = [0; 4096]
    var w := (&HxCounts).Writer(sys.io, &c, mut wb[..])
    res.FlushTo(sys.io, mut w).Must()
    res.SendRest(mut w).Must()
    // The head and the first chunk's start (one buffer handed over), the
    // rest of the first chunk, three more chunks, then the last chunk: 6.
    // Three writes a chunk made 9.
    Assert(c.calls.Load() <= 6)
}
