// Why: The web front end's routes and the state its handlers share.
//
// What: Front, Handle, and the routes for the room list, WebSockets and event
// streams.
//
// How: Handle routes by path; a route that starts a session claims a free
// bridge from the Front, so the number of web clients is bounded.

import std.atomic
import std.http
import std.url
import src.hub
import src.proto

/// What the web front end shares with every request: the bridges to the
/// server, the queue of new web clients, and the hub, which a handler only
/// reads.
export linear struct Front {
    heap: &Heap
    hb: &hub.Hub
    bridges: HeapList[Bridge]
    /// New web clients, for `Admit` to start a session for each.
    export arrivals: Chan[Arrival]
}

/// A front end with `n` bridges, so `n` web clients at once. Fails with
/// `OutOfMemory` when a bridge cannot be made. O(n); allocates two channels
/// per bridge.
export Front.New(h: &Heap, hb: &hub.Hub, n: int): OutOfMemory!Front from h, hb {
    a := h.Alloc()
    var bridges := HeapList[Bridge].New(a)
    onerr bridges.FreeEach(a, fn(b: Bridge) {
        b.Free(a)
    })
    for _ in 0..n {
        b := try Bridge.New(a)
        bridges.Push(a, b) else {
            b.Free(a)
            fail OutOfMemory
        }
    }
    arrivals := try Chan[Arrival].New(a, n)
    return Front(heap: h, hb: hb, bridges: bridges, arrivals: arrivals)
}

/// Frees the bridges, after every web client and session has ended.
export Front.Free(self) {
    Front(heap, bridges, arrivals, ..) := self
    a := heap.Alloc()
    bridges.FreeEach(a, fn(b: Bridge) {
        b.Free(a)
    })
    arrivals.Free(a)
}

/// The number of a free bridge, now taken for both sides, or `none` when
/// every bridge is in use. O(bridges).
Front.Claim(&self, io: Io): ?int {
    for i in 0..self.bridges.Len() {
        if self.bridges[i].Claim(io) {
            return some(i)
        }
    }
    return none
}

/// What a request asks for, read from its path: `/rooms`, `/ws`,
/// `/rooms/NAME/ws` or `/rooms/NAME/events`. The room is NAME with its `#`,
/// which a URL cannot carry in a path.
enum Route {
    List
    Socket(room: str)
    Events(room: str)
    Missing
}

/// The route of `path`, with its room name made in `a`.
RouteOf(a: &Arena, path: str): OutOfMemory!Route from a {
    if path == "/rooms" || path == "/rooms/" {
        return Route.List
    }
    if path == "/ws" {
        return Route.Socket(room: "")
    }
    if !path.StartsWith("/rooms/") {
        return Route.Missing
    }
    rest := path["/rooms/".Len()..]
    slash := rest.Find("/") else {
        return Route.Missing
    }
    room := try a.Fmt("#{name}", (name: rest[..slash]))
    if !proto.ValidRoom(room) {
        return Route.Missing
    }
    return match rest[slash + 1..] {
        "ws" { Route.Socket(room: room) }
        "events" { Route.Events(room: room) }
        _ { Route.Missing }
    }
}

/// The value of query parameter `name` in `query`, or `none`. O(query).
Param(a: &Arena, query: str, name: str): OutOfMemory!?str from a {
    pairs := try url.ParseQuery(a, query)
    for p in pairs {
        if p.name == name {
            return some(p.value)
        }
    }
    return none
}

/// Serves one request of the web front end: the room list, a WebSocket
/// session or a room's event stream. Fails only when the response cannot be
/// built or sent.
export Handle(c: &Front, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    if req.method != "GET" {
        res.Status(405)
        try res.Header("Allow", "GET")
        try res.Body(b"only GET is served\n")
        return
    }
    route := try RouteOf(a, req.path)
    nick := try Param(a, req.query, "nick")
    if n := nick {
        if !proto.ValidNick(n) {
            res.Status(400)
            try res.Body(b"nick must be 1 to 24 letters, digits, - or _\n")
            return
        }
    }
    match route {
        .List {
            try Rooms(c, a, io, res)
        }
        .Socket(room) {
            try Socket(c, io, req, res, link, room, nick)
        }
        .Events(room) {
            count := try Param(a, req.query, "count")
            limit := if text := count { text.ParseInt() else { -1 } } else { 0 }
            if limit < 0 || limit > 1000 {
                res.Status(400)
                try res.Body(b"count must be from 0 to 1000\n")
                return
            }
            try Events(c, a, io, res, link, room, nick, Cast[int](limit))
        }
        .Missing {
            res.Status(404)
            try res.Body(b"no such page\n")
        }
    }
}

/// Answers the room list: a line per room, `#name members=N topic=T`.
Rooms(c: &Front, a: &Arena, io: Io, res: mut http.Response): http.Error!void {
    table := c.hb.Table(io) else {
        fail Canceled
    }
    g := table.LockRead(io) else {
        fail Canceled
    }
    defer g.Unlock()
    rooms := g.Ref()
    try res.Header("Content-Type", "text/plain; charset=utf-8")
    if rooms.Len() == 0 {
        try res.Body(b"no rooms\n")
        return
    }
    var w := res.Writer()
    for i in 0..rooms.Len() {
        r := rooms.At(i)
        try Print(mut w, "{n} members={m} topic={t}\n", (n: r.name.Str(), m: r.count, t: r.Topic()))
    }
}

/// A web request lets go of its place in the count shutdown waits for.
Front.Leave(&self) {
    _ = self.hb.stats.web.FetchSub(1, atomic.AcqRel)
}

/// A web request takes a place in the count shutdown waits for.
Front.Enter(&self) {
    _ = self.hb.stats.web.FetchAdd(1, atomic.AcqRel)
}
