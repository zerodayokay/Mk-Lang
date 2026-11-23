// Why: The API's routes and their handlers.
//
// What: Routes, the route table, Handle, and one handler per endpoint: health,
// version, stats, list, tag, create, get, put, patch and delete.
//
// How: http.Dispatch matches the table; it answers 405 with Allow when only the
// method differs and 404 when nothing matches. Handlers validate with
// notes.Validate and reply through app.mk.

import std.http
import std.log
import src.notes
import src.store
import src.store.cext

/// The longest page `GET /notes` returns.
export const MaxPage: int = 100

/// The API. `Dispatch` answers 405 with `Allow` when only the method differs,
/// and 404 when nothing matches.
export const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/health", handler: Health),
    http.Route[App](method: "GET", pattern: "/version", handler: Version),
    http.Route[App](method: "GET", pattern: "/stats", handler: StatsOf),
    http.Route[App](method: "GET", pattern: "/notes", handler: ListNotes),
    http.Route[App](method: "POST", pattern: "/notes", handler: CreateNote),
    http.Route[App](method: "GET", pattern: "/notes/{id}", handler: GetNote),
    http.Route[App](method: "GET", pattern: "/tags/{tag}", handler: TagNotes),
    http.Route[App](method: "PUT", pattern: "/notes/{id}", handler: PutNote),
    http.Route[App](method: "PATCH", pattern: "/notes/{id}", handler: PatchNote),
    http.Route[App](method: "DELETE", pattern: "/notes/{id}", handler: DeleteNote),
]

/// The server's handler: the route table, and a log line for a request whose
/// handler failed before it replied.
export Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    http.Dispatch(a, io, Routes, c, req, res) else e {
        // Dispatch reports its own 404 and 405 as Status, which Serve sends.
        match e {
            http.Status(code) {
                line := try a.Fmt("{m} {p} {code} 0", (m: req.method, p: req.path, code))
                c.Note(a, io, log.Level.Warn, line)
            }
            _ {
                line := try a.Fmt("{m} {p} failed: {e}", (m: req.method, p: req.path, e))
                c.Note(a, io, log.Level.Error, line)
            }
        }
        fail e
    }
}

Health(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Content-Type", "application/json")
    var w := res.Writer()
    try w.Str("{\"status\":\"ok\"}")
    c.Note(a, io, log.Level.Info, try a.Fmt("{m} {p} 200 15", (m: req.method, p: req.path)))
}

// The first page of notes with one tag: GET /notes?tag= under a path of its own.
TagNotes(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    tag := req.Param("tag") else {
        fail http.Status(code: 404)
    }
    q := store.Query(text: "", tag: tag, limit: MaxPage, offset: 0)
    page := c.store.List(a, io, &q) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    try Reply(c, a, io, req, res, 200, try Encode(a, &page))
}

Version(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    name := cext.Register() else { "" }
    body := try a.Fmt("{{\"sqlite\":\"{v}\",\"normalizer\":\"{name}\"}}", (v: cext.SqliteVersion(), name))
    try Reply(c, a, io, req, res, 200, body)
}

StatsOf(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    s := c.store.Stats(a, io) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    try Reply(c, a, io, req, res, 200, try Encode(a, &s))
}

ListNotes(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    q := ReadQuery(a, req.query) else e {
        match e {
            BadQuery(index) {
                try Problem(c, a, io, req, res, 400, "a query parameter is malformed", some(index))
            }
            OutOfMemory {
                fail OutOfMemory
            }
        }
        return
    }
    if q.limit < 1 || q.limit > MaxPage || q.offset < 0 {
        try Problem(c, a, io, req, res, 400, "limit is 1 to 100 and offset at least 0", none)
        return
    }
    page := c.store.List(a, io, &q) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    try Reply(c, a, io, req, res, 200, try Encode(a, &page))
}

CreateNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var scratch := try a.Sub()
    defer scratch.Free()
    v := try ParseBody(c, a, &scratch, io, req, res) else { return }
    n := notes.NoteIn.FromJson(a, &v) else {
        try Problem(c, a, io, req, res, 422, "a note needs a string title and body, and tags as strings", none)
        return
    }
    if !try Valid(c, a, io, req, res, &n) {
        return
    }
    note := c.store.Create(a, io, &n) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    location := try a.Fmt("/notes/{id}", (id: note.id))
    try res.Header("Location", location)
    try Reply(c, a, io, req, res, 201, try Encode(a, &note))
}

GetNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(c, a, io, req, res) else { return }
    found := c.store.Get(a, io, id) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    note := found else {
        try Problem(c, a, io, req, res, 404, "no such note", none)
        return
    }
    try Reply(c, a, io, req, res, 200, try Encode(a, &note))
}

PutNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(c, a, io, req, res) else { return }
    var scratch := try a.Sub()
    defer scratch.Free()
    v := try ParseBody(c, a, &scratch, io, req, res) else { return }
    n := notes.NoteIn.FromJson(a, &v) else {
        try Problem(c, a, io, req, res, 422, "a note needs a string title and body, and tags as strings", none)
        return
    }
    try Save(c, a, io, req, res, id, &n)
}

PatchNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(c, a, io, req, res) else { return }
    var scratch := try a.Sub()
    defer scratch.Free()
    v := try ParseBody(c, a, &scratch, io, req, res) else { return }
    found := c.store.Get(a, io, id) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    cur := found else {
        try Problem(c, a, io, req, res, 404, "no such note", none)
        return
    }
    n := notes.ApplyPatch(a, &v, &cur) else e {
        match e {
            notes.NotObject {
                try Problem(c, a, io, req, res, 422, "a patch is a JSON object", none)
            }
            notes.UnknownField(index) {
                try Problem(c, a, io, req, res, 422, "a patch member is not title, body or tags", some(index))
            }
            notes.WrongType(index) {
                try Problem(c, a, io, req, res, 422, "a patch member has the wrong type", some(index))
            }
            OutOfMemory {
                fail OutOfMemory
            }
        }
        return
    }
    try Save(c, a, io, req, res, id, &n)
}

DeleteNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(c, a, io, req, res) else { return }
    gone := c.store.Delete(a, io, id) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    if !gone {
        try Problem(c, a, io, req, res, 404, "no such note", none)
        return
    }
    try Reply(c, a, io, req, res, 200, "{\"deleted\":true}")
}

// Validates n and stores it over the note with id, replying with the result.
Save(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, id: i64, n: &notes.NoteIn): http.Error!void {
    if !try Valid(c, a, io, req, res, n) {
        return
    }
    found := c.store.Update(a, io, id, n) else e {
        try StoreFailed(c, a, io, req, res, e)
        return
    }
    note := found else {
        try Problem(c, a, io, req, res, 404, "no such note", none)
        return
    }
    try Reply(c, a, io, req, res, 200, try Encode(a, &note))
}

// Whether n passes notes.Validate; a 422 problem has been sent when it does not.
Valid(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, n: &notes.NoteIn): http.Error!bool {
    notes.Validate(n) else e {
        try Problem(c, a, io, req, res, 422, notes.Explain(e), notes.Where(e))
        return false
    }
    return true
}

// The route's id, or none after a 400 problem.
IdOf(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!?i64 {
    id := req.ParamInt("id") else {
        try Problem(c, a, io, req, res, 400, "a note id is a number", none)
        return none
    }
    return some(id)
}

// The list query: q, tag, limit (20 when absent) and offset (0).
ReadQuery(a: &Arena, query: str): BadQuery|OutOfMemory!store.Query from a, query {
    text := try QueryParam(a, query, "q") else { "" }
    tag := try QueryParam(a, query, "tag") else { "" }
    limit := try Number(a, query, "limit", 20, 2)
    offset := try Number(a, query, "offset", 0, 3)
    return store.Query(text: text, tag: tag, limit: limit, offset: offset)
}

// A numeric query parameter, or fallback when it is absent.
Number(a: &Arena, query: str, name: str, fallback: int, index: int): BadQuery|OutOfMemory!int {
    text := try QueryParam(a, query, name) else { return fallback }
    n := text.ParseInt() else { fail BadQuery(index: index) }
    return Cast[int](n)
}
