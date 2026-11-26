// Why: a handler of ServeWith that upgrades its connection must take it
// from the server, which then never touches it again.
//
// What: Link.Takeover.
//
// How: the connection is taken out of the link's cell (CellTake), with the
// bytes the server read after the request, as Go's http.Hijacker does; the
// server then sends nothing on it.

/// Takes the connection the handler's response would be sent on, with the
/// bytes the server read after the request, for websocket.Accept or a
/// protocol of your own; none when it was already taken or the request came
/// over HTTP/2 or HTTP/3. O(1); allocates nothing.
///
/// After it, the server writes nothing on the connection, not even the
/// handler's response, and stops serving the connection when the handler
/// returns. The Taken holds the link's loans, so it lives inside the
/// handler.
export Link.Takeover(&self): ?Taken {
    cell := self.cell else {
        return none
    }
    c := CellTake(cell, self.io) else {
        return none
    }
    return some(Taken(conn: c, rest: self.rest))
}
