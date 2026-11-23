// The notes service of doc/examples.md, in-memory half, run on both targets, from
// tests/bench-lanes/std-http/notes
// (GET, POST and DELETE on /notes, JSON through gen json.Codec, one arena per request, the store
// behind a Mutex every connection task shares), driven by a client task in the same program
// through http.Fetch. The client takes a free port from a listener on 127.0.0.1:0, starts
// http.Serve there in a task, creates two notes, reads, deletes and misses one, sends the
// error cases (bad JSON, a bad id, an unknown path, a wrong method, a title too long), then
// cancels the scope, so Serve returns. It prints one line per request: method, path, status
// and body. The sqlite half (std.db.sqlite) is notesdb/, a package directory run by hand.
import std.http
import std.json
import std.net
import std.time

const Cap = 256
const TitleMax = 64
const BodyMax = 512

struct Slot {
    used: bool
    id: i64
    tn: int
    title: [64]u8
    bn: int
    body: [512]u8
}

struct Store {
    next: i64
    slots: [256]Slot
}

struct Ctx {
    store: Mutex[Store]
}

struct Note {
    export id: i64
    export title: str
    export body: str
}

gen json.Codec(Note)

struct NewNote {
    export title: str
    export body: str
}

gen json.Codec(NewNote)

GetNote(c: &Ctx, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400)
    }
    var g := try c.store.Lock(io)
    defer g.Unlock()
    s := g.Ref()
    for k in 0..Cap {
        if s.slots[k].used && s.slots[k].id == id {
            // A slot holds the bytes of a whole str (PostNote), so they are UTF-8.
            title := try a.Dup(str.From(s.slots[k].title[0..s.slots[k].tn]).Must())
            body := try a.Dup(str.From(s.slots[k].body[0..s.slots[k].bn]).Must())
            note := Note(id: id, title: title, body: body)
            try res.Header("Content-Type", "application/json")
            var w := res.Writer()
            try note.FormatJson(mut w)
            return
        }
    }
    fail http.Status(code: 404)
}

PostNote(c: &Ctx, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    text := str.From(req.body) else {
        fail http.Status(code: 400)
    }
    // json.Parse stores text into its arena, and the handler's
    // type lets no loan of req into a: parse in a scratch arena; FromJson
    // copies what it keeps into a.
    var scratch := try a.Sub()
    defer scratch.Free()
    v := json.Parse(&scratch, text) else {
        fail http.Status(code: 400)
    }
    n := NewNote.FromJson(a, &v) else {
        fail http.Status(code: 400)
    }
    tb := n.title.AsBytes()
    bb := n.body.AsBytes()
    if tb.Len() > TitleMax || bb.Len() > BodyMax {
        fail http.Status(code: 413)
    }
    var g := try c.store.Lock(io)
    defer g.Unlock()
    var s := g.Mut()
    for k in 0..Cap {
        if !s.slots[k].used {
            id := s.next
            s.next += 1
            s.slots[k].used = true
            s.slots[k].id = id
            s.slots[k].tn = tb.Len()
            for x in 0..tb.Len() {
                s.slots[k].title[x] = tb[x]
            }
            s.slots[k].bn = bb.Len()
            for x in 0..bb.Len() {
                s.slots[k].body[x] = bb[x]
            }
            note := Note(id: id, title: n.title, body: n.body)
            res.Status(201)
            try res.Header("Content-Type", "application/json")
            var w := res.Writer()
            try note.FormatJson(mut w)
            return
        }
    }
    fail http.Status(code: 507)
}

DeleteNote(c: &Ctx, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400)
    }
    var g := try c.store.Lock(io)
    defer g.Unlock()
    var s := g.Mut()
    for k in 0..Cap {
        if s.slots[k].used && s.slots[k].id == id {
            s.slots[k].used = false
            res.Status(204)
            return
        }
    }
    fail http.Status(code: 404)
}

Hello(c: &Ctx, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"hello, world\n")
}

const Routes: []http.Route[Ctx] = [
    http.Route[Ctx](method: "GET", pattern: "/notes/{id}", handler: GetNote),
    http.Route[Ctx](method: "POST", pattern: "/notes", handler: PostNote),
    http.Route[Ctx](method: "DELETE", pattern: "/notes/{id}", handler: DeleteNote),
    http.Route[Ctx](method: "GET", pattern: "/hello", handler: Hello),
]

Handle(c: &Ctx, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

const NoHeaders: []http.Header = []

// ClientTask runs Client in its own arena and copies its transcript into out, since a Writer
// is not Send.
ClientTask(s: &Scope, io: Io, heap: &Heap, base: str, out: mut []u8, n: mut int, result: mut str) {
    defer s.Cancel()
    var arena := Arena.New(heap) else {
        *result = "out of memory"
        return
    }
    defer arena.Free()
    var t := Text.New(&arena)
    var w := t.Writer()
    Client(io, &arena, base, mut w) else {
        *result = "client failed"
    }
    w.Flush() else {
        *result = "flush failed"
    }
    b := t.Str().AsBytes()
    for i in 0..b.Len() {
        if i < out.Len() {
            out[i] = b[i]
            *n = i + 1
        }
    }
}

// Call sends one request, retrying a refused dial while the server task starts, and prints
// "<method> <path> <status> <body>".
Call(w: mut Writer, a: &Arena, io: Io, base: str, method: str, path: str, body: str): http.Error|WriteError!void {
    url := try a.Fmt("{base}{path}", (base, path))
    // Fetch stores a's own loan into a: a scratch arena keeps that
    // out of Call's signature.
    var scratch := try a.Sub()
    defer scratch.Free()
    var tries := 0
    for {
        r := http.Fetch(&scratch, io, method, url, NoHeaders, body.AsBytes(), 65536) else e {
            tries += 1
            if tries < 200 {
                try io.Sleep(time.Millis(10))
                continue
            }
            fail e
        }
        text := str.From(r.body) else { "(a body that is not UTF-8)" }
        status := r.status
        try Print(w, "{method} {path} {status} {text}\n", (method, path, status, text))
        return
    }
}

Client(io: Io, a: &Arena, base: str, w: mut Writer): http.Error|WriteError!void {
    try Call(w, a, io, base, "POST", "/notes", "{\"title\":\"first\",\"body\":\"h\u{e9}llo \\\"world\\\"\\n\"}")
    try Call(w, a, io, base, "POST", "/notes", "{\"title\":\"second\",\"body\":\"\"}")
    try Call(w, a, io, base, "GET", "/notes/1", "")
    try Call(w, a, io, base, "GET", "/notes/2", "")
    try Call(w, a, io, base, "DELETE", "/notes/1", "")
    try Call(w, a, io, base, "GET", "/notes/1", "")
    try Call(w, a, io, base, "DELETE", "/notes/1", "")
    try Call(w, a, io, base, "POST", "/notes", "{\"title\":")
    try Call(w, a, io, base, "GET", "/notes/x", "")
    try Call(w, a, io, base, "GET", "/nothing", "")
    try Call(w, a, io, base, "PUT", "/notes/2", "")
    try Call(w, a, io, base, "POST", "/notes", "{\"title\":\"0123456789012345678901234567890123456789012345678901234567890123456789\",\"body\":\"\"}")
    try Call(w, a, io, base, "GET", "/hello", "")
}

export Main(sys: &System): net.Error|http.Error|WriteError|Utf8Error|OutOfMemory!void {
    empty := Slot(used: false, id: 0, tn: 0, title: [0; 64], bn: 0, body: [0; 512])
    ctx := Ctx(store: Mutex[Store].New(Store(next: 1, slots: [empty; 256])))
    probe := try net.Listen(sys.io, "127.0.0.1:0")
    p := probe.Port()
    probe.Close()
    addr := try sys.arena.Fmt("127.0.0.1:{p}", (p))
    base := try sys.arena.Fmt("http://127.0.0.1:{p}", (p))
    var out := sys.Stdout()
    var result := "ok"
    buf := try sys.arena.Slice[u8](1 << 16)
    var n := 0
    scope s := sys.io.Scope() {
        s.go ClientTask(&s, sys.io, sys.heap, base, mut buf[..], mut n, mut result)
        http.Serve(sys, addr, 65536, &ctx, Handle) else {
        }
    }
    try out.Str(try str.From(buf[0..n]))
    try Print(mut out, "{result}\n", (result))
}
