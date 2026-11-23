// The notes service of doc/examples.md, sqlite half:
// the in-memory half
// tests/programs/notes.mk with its store replaced by std.db.sqlite over import c.sqlite3, on an
// in-memory database. The routes, the client and the transcript are notes.mk's, so notesdb.out
// equals notes.out. Ids come from `insert ... returning id`; a delete reports a miss through
// `delete ... returning id` stepping no row. Every connection task shares the one Db through
// &Db (std.db.sqlite orders every C call under its process lock). The amalgamation is not in
// the repository: README.md says how this package is built.
import std.db.sqlite
import std.http
import std.json
import std.net
import std.time

const TitleMax = 64
const BodyMax = 512

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

GetNote(c: &sqlite.Db, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400)
    }
    var stmt := c.Prepare(a, io, "select title, body from notes where id = ?") else {
        fail http.Status(code: 500)
    }
    defer stmt.Close()
    stmt.BindInt(1, id) else {
        fail http.Status(code: 500)
    }
    found := stmt.Step(io) else {
        fail http.Status(code: 500)
    }
    if !found {
        fail http.Status(code: 404)
    }
    title := try a.Dup(stmt.Str(0))
    body := try a.Dup(stmt.Str(1))
    note := Note(id: id, title: title, body: body)
    try res.Header("Content-Type", "application/json")
    var w := res.Writer()
    try note.FormatJson(mut w)
}

PostNote(c: &sqlite.Db, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
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
    if n.title.AsBytes().Len() > TitleMax || n.body.AsBytes().Len() > BodyMax {
        fail http.Status(code: 413)
    }
    var stmt := c.Prepare(a, io, "insert into notes(title, body) values(?, ?) returning id") else {
        fail http.Status(code: 500)
    }
    defer stmt.Close()
    stmt.BindText(1, n.title) else {
        fail http.Status(code: 500)
    }
    stmt.BindText(2, n.body) else {
        fail http.Status(code: 500)
    }
    row := stmt.Step(io) else {
        fail http.Status(code: 500)
    }
    if !row {
        fail http.Status(code: 500)
    }
    note := Note(id: stmt.Int(0), title: n.title, body: n.body)
    res.Status(201)
    try res.Header("Content-Type", "application/json")
    var w := res.Writer()
    try note.FormatJson(mut w)
}

DeleteNote(c: &sqlite.Db, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400)
    }
    var stmt := c.Prepare(a, io, "delete from notes where id = ? returning id") else {
        fail http.Status(code: 500)
    }
    defer stmt.Close()
    stmt.BindInt(1, id) else {
        fail http.Status(code: 500)
    }
    row := stmt.Step(io) else {
        fail http.Status(code: 500)
    }
    if !row {
        fail http.Status(code: 404)
    }
    res.Status(204)
}

Hello(c: &sqlite.Db, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"hello, world\n")
}

const Routes: []http.Route[sqlite.Db] = [
    http.Route[sqlite.Db](method: "GET", pattern: "/notes/{id}", handler: GetNote),
    http.Route[sqlite.Db](method: "POST", pattern: "/notes", handler: PostNote),
    http.Route[sqlite.Db](method: "DELETE", pattern: "/notes/{id}", handler: DeleteNote),
    http.Route[sqlite.Db](method: "GET", pattern: "/hello", handler: Hello),
]

Handle(c: &sqlite.Db, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
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

export Main(sys: &System): sqlite.Error|net.Error|http.Error|WriteError|Utf8Error|OutOfMemory!void {
    db := try sqlite.Open(sys.arena, sys.io, ":memory:")
    defer db.Close()
    try db.Exec(sys.io, "create table notes(id integer primary key, title text not null, body text not null)")
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
        http.Serve(sys, addr, 65536, &db, Handle) else {
        }
    }
    try out.Str(try str.From(buf[0..n]))
    try Print(mut out, "{result}\n", (result))
}
