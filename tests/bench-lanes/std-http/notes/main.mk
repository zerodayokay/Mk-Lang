// The notes service of doc/examples.md, with an in-memory store in
// place of sqlite: GET, POST and DELETE on /notes, JSON
// through gen json.Codec, one arena per request (http.Serve), the store
// behind a Mutex every connection task shares. It is also the server of
// std.http's load test. Run: notes <addr>.
import std.http
import std.json

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

export Main(sys: &System): http.Error!void {
    empty := Slot(used: false, id: 0, tn: 0, title: [0; 64], bn: 0, body: [0; 512])
    ctx := Ctx(store: Mutex[Store].New(Store(next: 1, slots: [empty; 256])))
    try http.Serve(sys, sys.args[1], 65536, &ctx, Handle)
}
