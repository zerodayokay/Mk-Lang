// Why: the demo needs to check what was transferred and what was canceled.
// What: concurrent load workers, a slow response reader and the report.
// How: share one client, validate the response on every exchange and print
// counters after all tasks have joined.

import std.http
import std.atomic
import std.time
import std.url

struct Results {
    ok: atomic.AtomicInt
    canceled: atomic.AtomicInt
    bytes: atomic.AtomicInt
}

Exercise(h: &Heap, io: Io, c: &http.Client, base: str, w: &Work, payload: []u8, results: &Results) {
    start := if w.mode == Mode.Restart { 1 } else { 0 }
    round := if w.mode == Mode.Restart { "2" } else { "" }
    scope batch := io.Scope() {
        for i in start..w.streams {
            batch.go Transfer(h, io, c, base, w, payload, i < w.cancel, round, results)
        }
    }
}

Transfer(h: &Heap, io: Io, c: &http.Client, base: str, w: &Work, payload: []u8, cancel: bool, round: str, r: &Results) {
    var a := Arena.New(h) else { return }
    defer a.Free()
    path := if cancel { "cancel" } else { "echo" }
    address := a.Fmt("{base}/{path}?echo=yes%20please", (base, path)) else { return }
    dest := url.Parse(&a, address, none) else { return }
    var headers := List[http.Header].New(&a)
    headers.Push(http.Header(name: "x-demo", value: "relay")) else { return }
    req := http.ClientRequest(method: "POST", url: &dest, headers: headers.Items(), body: payload)
    var ex := c.Do(&a, io, &req) else { return }
    secure := if w.back == http.HttpVersion.Http3 { "yes" } else { "no" }
    if ex.Version() != w.front || ex.status != 200 ||
        !HasField(ex.headers, "x-relay-ingress", WireName(w.front)) ||
        !HasField(ex.headers, "x-origin-version", WireName(w.back)) ||
        !HasField(ex.headers, "x-origin-secure", secure) ||
        (round != "" && !HasField(ex.headers, "x-origin-round", round)) ||
        HasField(ex.headers, "x-origin-local", "origin") || !CookiesPreserved(ex.headers) {
        ex.Abandon()
        return
    }
    if cancel {
        ex.Abandon()
        _ = r.canceled.Add(1)
        return
    }
    n := ReadReply(io, mut ex, w.mode == Mode.Slow, payload.Len()) else {
        ex.Abandon()
        return
    }
    ex.Close(io) else {
        ex.Abandon()
        return
    }
    _ = r.bytes.Add(i64(n))
    _ = r.ok.Add(1)
}

// Separate Set-Cookie fields cannot be joined using the ordinary list
// syntax. Check their values and original order on the final exchange.
CookiesPreserved(headers: []http.Header): bool {
    expected: [2]str = ["first=1; Path=/", "second=2; Path=/"]
    var found := 0
    for h in headers {
        if SameName(h.name, "set-cookie") {
            if found == expected.Len() || h.value != expected[found] {
                return false
            }
            found += 1
        }
    }
    return found == expected.Len()
}

ReadReply(io: Io, ex: mut http.Exchange, slow: bool, want: int): ReadError|Canceled!int {
    var scratch: [512]u8 = [0; 512]
    var piece: [1024]u8 = [0; 1024]
    var reader := ex.Body(mut scratch[..])
    size := if slow { 257 } else { piece.Len() }
    var total := 0
    for {
        n := try reader.Read(mut piece[..size])
        if n == 0 {
            if total != want {
                fail ReadError
            }
            return total
        }
        if n > want - total || !CheckPayload(piece[..n], total) {
            fail ReadError
        }
        total += n
        if slow {
            try io.Sleep(time.Millis(1))
        }
    }
}

Settled(io: Io, relay: &Relay, origin: &Origin, want: int): bool {
    for _ in 0..1000 {
        if relay.budget.Read(io).active == 0 && origin.canceled.Load() == i64(want) {
            return true
        }
        io.Sleep(time.Millis(1)) else { return false }
    }
    return false
}

Report(sys: &System, w: &Work, relay: &Relay, origin: &Origin, r: &Results, ready: bool): RunFailed|WriteError!void {
    ok := r.ok.Load()
    canceled := r.canceled.Load()
    resets := origin.canceled.Load()
    usage := relay.budget.Read(sys.io)
    active := usage.active
    seen := origin.seen.Load()
    uploaded := origin.bytes.Load()
    downloaded := r.bytes.Load()
    bad := origin.bad.Load()
    try sys.Print("{front}->{back} requested={requested} completed={ok} canceled={canceled} resets={resets} active={active}\n", (
        front: WireName(w.front), back: WireName(w.back), requested: w.streams, ok, canceled, resets, active,
    ))
    try sys.Print("origin={seen} uploaded={uploaded} downloaded={downloaded} invalid={bad}\n", (seen, uploaded, downloaded, bad))
    if w.mode == Mode.Slow {
        try sys.Print("copy-buffer={copy} response-cap={cap} h2-window={window}\n", (copy: CopyBlock * 2, cap: MaxBody, window: Window))
        if w.streams == 1 {
            try sys.Print("retained-payload-peak={peak}\n", (peak: usage.peak_bytes))
        }
    }
    if !ready || ok + canceled != i64(w.streams) || canceled != i64(w.cancel) || resets != canceled ||
        active != 0 || relay.seen.Load() != i64(w.streams) ||
        seen != i64(w.streams) || uploaded != i64(w.streams * w.bytes) ||
        downloaded != ok * i64(w.bytes) || bad != 0 ||
        usage.bytes != 0 || usage.peak_bytes > MaxRetained {
        fail RunFailed
    }
}
