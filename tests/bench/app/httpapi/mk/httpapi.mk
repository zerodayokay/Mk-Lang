// httpapi, Mk: the task of ../README.md, a JSON API over HTTP/1.1 on
// std.http.Serve, one task per connection and one arena per request
// (MK_CARRIERS is the row's thread count, set by tools/bench). Items live
// in a HeapMap from id to their heap text, behind a sync.RwLock; the next
// id is kept with them. Bodies are read with json.Parse and the codec's
// FromJson, and written with the codec's FormatJson; a body of 256 bytes
// or more goes out gzip-compressed (compress.Gzip) when the request
// accepts gzip. Written from the documents only (doc/reference.md's
// module map and library surface, and Dogfoods/notes as user code), as an outside user
// would. Usage: httpapi <port>. Serves 127.0.0.1:<port> until killed.
import std.compress
import std.http
import std.json
import std.sync

/// Bodies at least this long are compressed when the client accepts gzip.
const GzipMin: int = 256

/// The longest name, and the longest tag, in bytes.
const MaxName: int = 64
const MaxTag: int = 32

/// The largest quantity.
const MaxQty: i64 = 1_000_000_000

/// The longest page `GET /items` returns, and its length by default.
const MaxLimit: i64 = 100
const DefaultLimit: i64 = 20

/// An item as a client sends it in a POST or PUT body.
export struct ItemIn {
    export name: str
    export tag: str
    export qty: i64
}

gen json.Codec(ItemIn)

/// An item as the API returns it.
export struct Item {
    export id: i64
    export name: str
    export tag: str
    export qty: i64
}

gen json.Codec(Item)

/// One page of a tag's items, by id, and how many items the tag has.
export struct ItemList {
    export items: []Item
    export total: int
}

gen json.Codec(ItemList)

/// The body of every error response.
export struct Problem {
    export message: str
}

gen json.Codec(Problem)

/// A DELETE's answer.
export struct Deleted {
    export deleted: bool
}

gen json.Codec(Deleted)

/// One item as it is stored: its text on the heap.
struct Stored {
    name: HeapText
    tag: HeapText
    qty: i64
}

impl Free for Stored {
    Free(self, a: &Alloc) {
        Stored(name, tag, ..) := self
        name.Free(a)
        tag.Free(a)
    }
}

/// The items, and the id the next one gets.
linear struct Items {
    map: HeapMap[i64, Stored]
    next: i64
}

/// What every connection's task shares.
struct App {
    items: sync.RwLock[Items]
    heap: &Heap
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "POST", pattern: "/items", handler: Create),
    http.Route[App](method: "GET", pattern: "/items", handler: ListItems),
    http.Route[App](method: "GET", pattern: "/items/{id}", handler: GetItem),
    http.Route[App](method: "PUT", pattern: "/items/{id}", handler: PutItem),
    http.Route[App](method: "DELETE", pattern: "/items/{id}", handler: DeleteItem),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

/// `x` as compact JSON text in `a`.
Encode[T: json.Json](a: &Arena, x: &T): OutOfMemory!str from a {
    var t := Text.New(a)
    var w := t.Writer()
    x.FormatJson(mut w) else { fail OutOfMemory }
    return t.Done()
}

/// Whether the request's `Accept-Encoding` names gzip.
AcceptsGzip(req: &http.Request): bool {
    accept := req.Header("Accept-Encoding") else { return false }
    for part in accept.Split(",") {
        name := part.Trim()
        semi := name.Find(";") else { name.Len() }
        if name[..semi].Trim() == "gzip" {
            return true
        }
    }
    return false
}

/// Sends `body`, a JSON text, with `status`: gzip-compressed when it is at
/// least GzipMin bytes and the request accepts gzip.
Reply(a: &Arena, req: &http.Request, res: mut http.Response, status: int, body: str): http.Error!void {
    res.Status(status)
    try res.Header("Content-Type", "application/json")
    if body.Len() >= GzipMin && AcceptsGzip(req) {
        packed := try compress.Gzip(a, body.AsBytes())
        try res.Header("Content-Encoding", "gzip")
        try res.Body(packed)
    } else {
        try res.Body(body.AsBytes())
    }
}

/// Sends a problem: `{"message":...}` with `status`.
Refuse(a: &Arena, req: &http.Request, res: mut http.Response, status: int, message: str): http.Error!void {
    p := Problem(message: message)
    try Reply(a, req, res, status, try Encode(a, &p))
}

/// Whether a tag is 1 to MaxTag bytes of `a` to `z`, `0` to `9` and `-`.
GoodTag(tag: str): bool {
    if tag.Len() == 0 || tag.Len() > MaxTag {
        return false
    }
    for c in tag.AsBytes() {
        if !((c >= b'a' && c <= b'z') || (c >= b'0' && c <= b'9') || c == b'-') {
            return false
        }
    }
    return true
}

/// The request's item, its text copied into `a`, or none after a 400 or
/// 422 problem. The parsed body holds views of the request, so it lives in
/// `scratch`, the handler's own arena.
ReadItem(a: &Arena, scratch: &Arena, req: &http.Request, res: mut http.Response): http.Error!?ItemIn from a *req into scratch {
    text := str.From(req.body) else {
        try Refuse(a, req, res, 400, "the body is not UTF-8")
        return none
    }
    v := json.Parse(scratch, text) else {
        try Refuse(a, req, res, 400, "the body is not JSON")
        return none
    }
    n := ItemIn.FromJson(a, &v) else {
        try Refuse(a, req, res, 422, "an item is an object with a string name and tag and an integer qty")
        return none
    }
    if n.name.Len() == 0 || n.name.Len() > MaxName || !GoodTag(n.tag) || n.qty < 0 || n.qty > MaxQty {
        try Refuse(a, req, res, 422, "an item needs a name of 1 to 64 bytes, a tag of 1 to 32 bytes of a-z, 0-9 and -, and a qty of 0 to 1000000000")
        return none
    }
    return some(n)
}

/// Copies an item's text onto the heap.
NewStored(h: &Alloc, n: &ItemIn): OutOfMemory!Stored from h {
    var name := HeapText.New(h)
    onerr name.Free(h)
    try name.Push(h, n.name)
    var tag := HeapText.New(h)
    onerr tag.Free(h)
    try tag.Push(h, n.tag)
    return Stored(name: name, tag: tag, qty: n.qty)
}

/// The route's id, or none after a 400 problem.
IdOf(a: &Arena, req: &http.Request, res: mut http.Response): http.Error!?i64 {
    id := req.ParamInt("id") else {
        try Refuse(a, req, res, 400, "an item id is a number")
        return none
    }
    return some(id)
}

/// Stores `n` under the next id and returns the id.
Insert(c: &App, io: Io, n: &ItemIn): Canceled|OutOfMemory!i64 {
    h := c.heap.Alloc()
    var g := try c.items.Lock(io)
    defer g.Unlock()
    s := g.Mut()
    id := s.next
    stored := try NewStored(h, n)
    onerr stored.Free(h)
    try s.map.Set(h, id, stored)
    s.next += 1
    return id
}

/// `POST /items`: stores the item under the next id; 201 with the item and
/// its Location.
Create(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var scratch := try a.Sub()
    defer scratch.Free()
    n := try ReadItem(a, &scratch, req, res) else { return }
    id := try Insert(c, io, &n)
    item := Item(id: id, name: n.name, tag: n.tag, qty: n.qty)
    try res.Header("Location", try a.Fmt("/items/{id}", (id)))
    try Reply(a, req, res, 201, try Encode(a, &item))
}

/// The item `id` as JSON text in `a`, or none when there is no such item.
Lookup(c: &App, a: &Arena, io: Io, id: i64): Canceled|OutOfMemory!?str from a {
    g := try c.items.LockRead(io)
    defer g.Unlock()
    st := g.Ref().map.Get(&id) else { return none }
    item := Item(id: id, name: st.name.Str(), tag: st.tag.Str(), qty: st.qty)
    return some(try Encode(a, &item))
}

/// `GET /items/{id}`: the item, or 404.
GetItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(a, req, res) else { return }
    body := try Lookup(c, a, io, id) else {
        try Refuse(a, req, res, 404, "no such item")
        return
    }
    try Reply(a, req, res, 200, body)
}

/// Replaces the fields of item `id` with `n`'s; false when there is no
/// such item.
Update(c: &App, io: Io, id: i64, n: &ItemIn): Canceled|OutOfMemory!bool {
    h := c.heap.Alloc()
    var g := try c.items.Lock(io)
    defer g.Unlock()
    st := g.Mut().map.GetMut(&id) else { return false }
    try st.name.Set(h, n.name)
    try st.tag.Set(h, n.tag)
    st.qty = n.qty
    return true
}

/// `PUT /items/{id}`: replaces the item's fields; 200 with the item, or 404.
PutItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(a, req, res) else { return }
    var scratch := try a.Sub()
    defer scratch.Free()
    n := try ReadItem(a, &scratch, req, res) else { return }
    if !try Update(c, io, id, &n) {
        try Refuse(a, req, res, 404, "no such item")
        return
    }
    item := Item(id: id, name: n.name, tag: n.tag, qty: n.qty)
    try Reply(a, req, res, 200, try Encode(a, &item))
}

/// Removes item `id` and frees it; false when there is no such item.
Remove(c: &App, io: Io, id: i64): Canceled!bool {
    var g := try c.items.Lock(io)
    defer g.Unlock()
    gone := g.Mut().map.Remove(&id) else { return false }
    gone.Free(c.heap.Alloc())
    return true
}

/// `DELETE /items/{id}`: removes the item; 200, or 404.
DeleteItem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := try IdOf(a, req, res) else { return }
    if !try Remove(c, io, id) {
        try Refuse(a, req, res, 404, "no such item")
        return
    }
    d := Deleted(deleted: true)
    try Reply(a, req, res, 200, try Encode(a, &d))
}

/// The raw value of the first `name=value` pair of a URL query, or none.
/// The API's tags and limits need no escapes, so a value is not decoded.
QueryParam(query: str, name: str): ?str from query {
    for pair in query.Split("&") {
        eq := pair.Find("=") else { pair.Len() }
        if pair[..eq] == name {
            return some(if eq < pair.Len() { pair[eq + 1..] } else { "" })
        }
    }
    return none
}

/// The page of `tag`'s items, by id, at most `limit`, as JSON text in `a`.
Page(c: &App, a: &Arena, io: Io, tag: str, limit: i64): Canceled|OutOfMemory!str from a {
    g := try c.items.LockRead(io)
    defer g.Unlock()
    // the stored text is the lock's, so each matching item's text is
    // copied into an arena of the page's own; only the JSON text reaches `a`
    var copies := try a.Sub()
    defer copies.Free()
    var found := List[Item].New(&copies)
    for (key: id, value: st) in g.Ref().map.Iter() {
        if st.tag.Str() == tag {
            try found.Push(Item(id: *id, name: try copies.Dup(st.name.Str()), tag: try copies.Dup(st.tag.Str()), qty: st.qty))
        }
    }
    found.Sort(fn(x: &Item, y: &Item): bool { return x.id < y.id })
    total := found.Len()
    all := found.Done()
    n := if i64(total) > limit { Cast[int](limit) } else { total }
    page := ItemList(items: all[..n], total: total)
    return try Encode(a, &page)
}

/// `GET /items?tag=T&limit=N`: the tag's items by id, at most N of them
/// (20 when absent, 1 to 100), and how many the tag has.
ListItems(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    tag := QueryParam(req.query, "tag") else { "" }
    if !GoodTag(tag) {
        try Refuse(a, req, res, 400, "tag is 1 to 32 bytes of a-z, 0-9 and -")
        return
    }
    var limit := DefaultLimit
    if text := QueryParam(req.query, "limit") {
        limit = text.ParseInt() else { 0 }
    }
    if limit < 1 || limit > MaxLimit {
        try Refuse(a, req, res, 400, "limit is 1 to 100")
        return
    }
    try Reply(a, req, res, 200, try Page(c, a, io, tag, limit))
}

export Main(sys: &System): http.Error!void {
    if sys.args.Len() != 2 {
        var e := sys.Stderr()
        Print(mut e, "usage: httpapi <port>\n") else {}
        fail http.Status(code: 2)
    }
    addr := try sys.arena.Fmt("127.0.0.1:{port}", (port: sys.args[1]))
    h := sys.heap.Alloc()
    app := App(items: sync.RwLock.New(Items(map: HeapMap[i64, Stored].New(h), next: 1)), heap: sys.heap)
    http.Serve(sys, addr, 65536, &app, Handle) else e {
        App(items, heap: _) := app
        Items(map, next: _) := items.Into()
        map.Free(h)
        fail e
    }
    App(items, heap: _) := app
    Items(map, next: _) := items.Into()
    map.Free(h)
}
