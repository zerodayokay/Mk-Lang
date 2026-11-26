// Why: a handler that produces its reply over time (a long listing, server
// events) must be able to send part of it before the rest exists.
//
// What: Response.Flush and its helpers (FlushTo, FlushHead, FlushPiece,
// WriteUnsent, SendRest), and Flushed, what the flushes sent so far.
//
// How: the first flush sends the head with Transfer-Encoding: chunked (RFC
// 9112 7.1) and the body so far as a chunk; each later flush sends what
// was added since as another chunk; Send then sends the rest and the last
// chunk. The exceptions follow.

// An HTTP/1.0 request cannot take chunked framing (RFC 9112 6.1), so its
// reply is sent with no length and ends with the connection (RFC 9112 6.3,
// item 8). A reply with no body (204, 304, or to HEAD) sends its head and
// nothing after. Response's fl field (response.mk) holds Flushed, and the
// first lines of Send hand over to SendRest once a flush has happened.

// The framing a flushed response chose.
const FlushChunked = 0
const FlushClose = 1
const FlushNoBody = 2

// Flushed is a response's flush state: whether its head was sent, the
// framing it chose, how many of the Body bytes now held were sent (dsent,
// reset by Body, which replaces them) and how many of the Writer's (tsent;
// the Writer only appends).
struct Flushed {
    head: bool
    // failed: a Flush failed, so what reached the client is unknown and
    // the server ends the connection after the handler.
    failed: bool
    mode: int
    dsent: int
    tsent: int
}

// NotFlushed is the state of a response nothing of which was sent.
NotFlushed(): Flushed {
    return Flushed(head: false, failed: false, mode: FlushChunked, dsent: 0, tsent: 0)
}

/// Sends what the response holds so far, before the handler returns: for
/// a reply produced over time, such as a long listing or server events.
/// O(bytes sent); allocates nothing.
///
/// On HTTP/2 and HTTP/3 this sends HEADERS followed by DATA on the request's
/// stream. On HTTP/1.1 the first Flush sends the head, with Transfer-Encoding: chunked in place
/// of the handler's framing fields, and the body so far as a chunk. Each
/// later Flush sends what was added since as another chunk: the bytes of a
/// Body set after the last Flush, whole, and what the Writer appended
/// since. When the handler returns, the server sends the rest and the last
/// chunk. To an HTTP/1.0 request the head carries no length and the body
/// ends with the connection; a 204, a 304 or a reply to HEAD sends its
/// head alone. Status and fields set after the first Flush are not sent,
/// and an error the handler returns after it cuts the body short.
///
/// Errors: a status that is not final (below 200) fails with Status(code)
/// and sends nothing. A write that fails, or a piece the client does not
/// take within the server's request_header, is WriteError (Canceled under
/// a cancelled scope). The server then sends nothing more on the
/// connection and closes it after the handler, whatever the handler
/// returns, and every later Flush fails with WriteError and sends
/// nothing. When link's connection was taken, Flush sends nothing and
/// does not fail.
export Response.Flush(mut self, io: Io, link: &Link): Error!void {
    if self.fl.failed {
        // A Flush failed: what reached the client is unknown, so nothing
        // more is written, and every later Flush fails as the first did.
        try CancelCheck(io)
        fail WriteError
    }
    try self.AdvertiseH3(link.h3_port)
    if target := link.h3 {
        scope sending := io.Scope() {
            sending.CancelAfter(link.pace)
            self.SendH3(io, target, false) else e {
                self.fl.failed = true
                try CancelCheck(io)
                fail e
            }
        }
        return
    }
    if target := link.h2 {
        self.FlushH2(io, target) else e {
            self.fl.failed = true
            try CancelCheck(io)
            fail e
        }
        return
    }
    cell := link.cell else {
        return
    }
    g := CellLock(cell, io)
    if conn := &*g.Ref() {
        var wb: [4096]u8 = [0; 4096]
        p := Paced(conn: conn, limit: link.pace)
        var w := (&PacedWrites).Writer(io, &p, mut wb[..])
        self.FlushTo(io, mut w) else e {
            self.fl.failed = true
            g.Unlock()
            try CancelCheck(io)
            fail e
        }
    }
    g.Unlock()
}

// FlushH2 sends what Flush sends over an HTTP/2 stream target: the head the
// first time, then any unsent body bytes since the last flush. Fails with
// Status(code) for non-final status or WriteError/net.Error on transport failure.
Response.FlushH2(mut self, io: Io, target: H2FlushTarget): Error!void {
    if !self.fl.head {
        code := self.code
        if code < 200 || code > 999 {
            fail Status(code: code)
        }
        bodyless := code == 204 || code == 304 || self.head
        hs := try BuildH2OutHeaders(self.arena, &*self, true, 0)
        target.conn.Respond(io, target.stream, code, hs, bodyless) else e {
            match e {
                Canceled { fail Canceled }
                _ { fail WriteError }
            }
        }
        self.fl.head = true
        self.fl.mode = if bodyless { FlushNoBody } else { FlushChunked }
    }
    if self.fl.mode == FlushNoBody {
        self.fl.dsent = self.data.buf.Len()
        self.fl.tsent = self.text.Str().AsBytes().Len()
        return
    }
    dn := self.data.buf.Len()
    if self.fl.dsent < dn {
        target.conn.Write(io, target.stream, self.data.buf[self.fl.dsent..dn], false) else e {
            match e {
                Canceled { fail Canceled }
                _ { fail WriteError }
            }
        }
        self.fl.dsent = dn
    }
    tb := self.text.Str().AsBytes()
    tn := tb.Len()
    if self.fl.tsent < tn {
        target.conn.Write(io, target.stream, tb[self.fl.tsent..tn], false) else e {
            match e {
                Canceled { fail Canceled }
                _ { fail WriteError }
            }
        }
        self.fl.tsent = tn
    }
}

// FlushTo sends to w what Flush sends: the head the first time, then the
// body bytes added since the last flush, and flushes w. A status that is
// not final (below 200, or above 999) fails with Status(code) and sends
// nothing. Fails with w's WriteError. O(bytes sent); allocates nothing.
Response.FlushTo(mut self, io: Io, w: mut Writer): Error!void {
    if !self.fl.head {
        code := self.code
        if code < 200 || code > 999 {
            fail Status(code: code)
        }
        bodyless := code == 204 || code == 304 || self.head
        self.fl.mode = if code == 204 || code == 304 { FlushNoBody } else if self.old { FlushClose } else { FlushChunked }
        if self.fl.mode == FlushClose {
            // The body ends with the connection (RFC 9112 6.3, item 8).
            self.close = true
        }
        try self.FlushHead(w)
        self.fl.head = true
        if bodyless {
            self.fl.mode = FlushNoBody
        }
    }
    try self.WriteUnsent(w, false)
    self.fl.dsent = self.data.buf.Len()
    self.fl.tsent = self.text.Str().Len()
    try w.Flush()
}

// FlushHead writes the status line and the fields, with the framing of
// fl.mode in place of the handler's Content-Length and Transfer-Encoding.
Response.FlushHead(&self, w: mut Writer): WriteError!void {
    code := self.code
    try Print(w, "HTTP/1.1 {code} {reason}\r\n", (code, reason: Reason(code)))
    for h in self.headers.Items() {
        if !IsToken(h.name) || EqFold(h.name, "content-length") || EqFold(h.name, "transfer-encoding") {
            continue
        }
        if EqFold(h.name, "connection") && self.close {
            continue
        }
        try w.Str(h.name)
        try w.Str(": ")
        try w.Str(h.value)
        try w.Str("\r\n")
    }
    if self.fl.mode == FlushChunked {
        try w.Str("Transfer-Encoding: chunked\r\n")
    }
    if self.close {
        try w.Str("Connection: close\r\n")
    }
    try w.Str("\r\n")
}

// WriteUnsent writes the body bytes not yet sent in fl.mode's framing
// (the unsent part of the Body bytes, then of the Writer's), and with last
// the last chunk. Nothing for a response with no body.
Response.WriteUnsent(&self, w: mut Writer, last: bool): WriteError!void {
    if self.fl.mode == FlushNoBody {
        return
    }
    // The Body bytes held now past those sent (all of them after a Body
    // that replaced the sent ones), then the Writer's
    // past those sent. Clamped, so neither slice can pass its end.
    tb := self.text.Str().AsBytes()
    dn := self.data.buf.Len()
    s1 := if self.fl.dsent < dn { self.fl.dsent } else { dn }
    s2 := if self.fl.tsent < tb.Len() { self.fl.tsent } else { tb.Len() }
    try self.FlushPiece(w, self.data.buf[s1..dn])
    try self.FlushPiece(w, tb[s2..tb.Len()])
    if last && self.fl.mode == FlushChunked {
        try w.Str("0\r\n\r\n")
    }
}

// FlushPiece writes b in fl.mode's framing.
Response.FlushPiece(&self, w: mut Writer, b: []u8): WriteError!void {
    if self.fl.mode == FlushChunked {
        try WriteChunks(w, b)
        return
    }
    try w.Bytes(b)
}

// SendRest is Send for a flushed response: the body bytes not yet sent and
// the last chunk, then the flush of w.
Response.SendRest(&self, w: mut Writer): Error!void {
    try self.WriteUnsent(w, true)
    try w.Flush()
}
