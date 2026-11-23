// Why: a handler builds its reply in pieces (status, fields, body), and the
// server writes it in one pass with no allocation.
//
// What: Response with New, Status, Header, Body, Writer and Send.
//
// How: the response lives in the request's arena; Header and Body copy into
// it and Writer appends after the Body bytes. Send writes the status line,
// the fields, a Content-Length the handler did not set, the blank line and
// the body in one Writer pass (RFC 9112 sections 4, 6 and 7.1).

// BodyBytes holds the response body bytes copied into the response's arena.
// Holding only the arena reference and a byte slice (no carriers), replacing
// its slice keeps Response.Body free of `*self into self`.
struct BodyBytes {
    arena: &Arena
    buf: []u8
}

BodyBytes.Set(mut self, data: []u8): OutOfMemory!void {
    b := try self.arena.Slice[u8](data.Len())
    (mut b[..]).CopyFrom(data)
    self.buf = b[..]
}

BodyBytes.Clear(mut self) {
    self.buf = "".AsBytes()
}

/// A response under construction: a status, header fields and a body, all
/// in the request's arena. The server hands one to each handler and sends
/// it when the handler returns.
///
/// Header and Body copy what they are given into the arena, so a handler
/// stores no loan of its own into the response.
export view struct Response {
    arena: &Arena
    code: int
    headers: List[Header]
    data: BodyBytes
    text: Text
    head: bool
    close: bool
    // old: the request was HTTP/1.0, which cannot frame a chunked body
    // (RFC 9112 6.1), so Send sends a Content-Length instead.
    old: bool
    // fl: what Flush sent (flush.mk).
    fl: Flushed
}

// MaxChunk is the largest chunk Send writes under Transfer-Encoding: chunked.
const MaxChunk = 16384

/// A 200 response with no fields and an empty body, in a. O(1); allocates
/// nothing until a field or body bytes are added.
export Response.New(a: &Arena): Response {
    return Response(arena: a, code: 200, headers: List[Header].New(a), data: BodyBytes(arena: a, buf: "".AsBytes()), text: Text.New(a), head: false, close: false, old: false, fl: NotFlushed())
}

/// Sets the status code. O(1); allocates nothing.
///
/// The server answers a code outside 200 to 999 with 500 and ends the
/// connection, since an interim 1xx is never a final reply. Send called
/// directly panics on a code outside 100 to 999.
export Response.Status(mut self, code: int) {
    self.code = code
}

/// Adds a header field, copied into the response's arena. Fails with
/// OutOfMemory when the arena is full. O(n) in the field's length;
/// allocates name and value in the arena.
///
/// CR, LF and NUL in value become spaces, so a value cannot split the
/// response. A field whose name is not a token is not sent.
export Response.Header(mut self, name: str, value: str): OutOfMemory!void {
    n := try self.arena.Dup(name)
    v := try CleanValue(self.arena, value)
    try self.headers.Push(Header(name: n, value: v))
}

/// Sets the body to a copy of data, in the response's arena; what Writer
/// writes follows it. Fails with OutOfMemory when the arena is full. O(n);
/// allocates data's length in the arena.
export Response.Body(mut self, data: []u8): OutOfMemory!void {
    try self.data.Set(data)
    // New bytes: after a Flush the next one sends them whole.
    self.fl.dsent = 0
}

/// A Writer that appends text to the body, after the Body bytes. The bytes
/// written must be UTF-8, else the write fails with WriteError; use Body
/// for other bytes. O(1); allocates nothing until written to.
export Response.Writer(mut self): Writer from self {
    return self.text.Writer()
}

/// Writes the response to w, then flushes w. The server calls it; call it
/// yourself to answer a request read with ReadRequest. Fails with w's
/// WriteError. O(n) in the reply's bytes; allocates nothing.
///
/// Send writes the framing itself, so it always matches the body. A 1xx,
/// 204 or 304 carries no body and no Content-Length or Transfer-Encoding,
/// and a response to HEAD carries its length but not its bytes. With the
/// handler's `Transfer-Encoding: chunked`, the body goes out in chunks of
/// at most 16 KiB and the last chunk, except to an HTTP/1.0 request, which
/// gets no Transfer-Encoding. Otherwise Send writes the body's own
/// Content-Length in place of the handler's. A code outside 100 to 999
/// panics; the server answers 500 before that. After Flush, Send writes
/// only the body bytes added since, in the framing Flush chose, and the
/// last chunk.
export Response.Send(&self, io: Io, w: mut Writer): Error!void {
    if self.fl.head {
        try self.SendRest(w)
        return
    }
    code := self.code
    if code < 100 || code > 999 {
        Panic("http.Response: status code outside 100 to 999")
    }
    try Print(w, "HTTP/1.1 {code} {reason}\r\n", (code, reason: Reason(code)))
    bodyless := code < 200 || code == 204 || code == 304
    hs := self.headers.Items()
    // Send owns the framing (RFC 9112 6.3), so it always matches the bytes
    // written: a handler's Transfer-Encoding is kept only when it is exactly
    // chunked, which Send then does; its Content-Length only on a reply to
    // HEAD, which sends no bytes to contradict it; neither on 1xx, 204 or
    // 304 (RFC 9110 8.6, 15.3.5; RFC 9112 6.1), and no Transfer-Encoding
    // to an HTTP/1.0 request (RFC 9112 6.1). Every other framing field is
    // dropped and Send writes its own Content-Length.
    var chunked := false
    if !bodyless && !self.old {
        for i in 0..hs.Len() {
            if EqFold(hs[i].name, "transfer-encoding") && EqFold(hs[i].value, "chunked") {
                chunked = true
            }
        }
    }
    var te_done := false
    var user_len := false
    for i in 0..hs.Len() {
        h := hs[i]
        if !IsToken(h.name) {
            continue
        }
        if EqFold(h.name, "content-length") {
            if bodyless || chunked || !self.head || user_len || ParseLength(h.value.AsBytes()) < 0 {
                continue
            }
            user_len = true
        } else if EqFold(h.name, "transfer-encoding") {
            if !chunked || te_done || !EqFold(h.value, "chunked") {
                continue
            }
            te_done = true
        } else if EqFold(h.name, "connection") && self.close {
            continue
        }
        try w.Str(h.name)
        try w.Str(": ")
        try w.Str(h.value)
        try w.Str("\r\n")
    }
    tb := self.text.Str().AsBytes()
    if !bodyless && !chunked && !user_len {
        n := self.data.buf.Len() + tb.Len()
        try Print(w, "Content-Length: {n}\r\n", (n))
    }
    if self.close {
        try w.Str("Connection: close\r\n")
    }
    try w.Str("\r\n")
    if !bodyless && !self.head {
        if chunked {
            try WriteChunks(w, self.data.buf)
            try WriteChunks(w, tb)
            try w.Str("0\r\n\r\n")
        } else {
            try w.Bytes(self.data.buf)
            try w.Bytes(tb)
        }
    }
    try w.Flush()
}

// ChunkLine is the room before a chunk's bytes for its size line (at most
// 4 hex digits for MaxChunk, then CRLF, in 8 bytes); ChunkRoom holds the
// line, MaxChunk bytes and their CRLF.
const ChunkLine = 8
const ChunkRoom = 16394

// WriteChunks writes b as chunks of at most MaxChunk bytes, each a hex
// size, CRLF, the bytes, CRLF; nothing for an empty b (a zero size would
// end the body). Each chunk is put together in a buffer on the stack and
// handed to w in one Bytes call, so a chunk larger than w's buffer costs
// one write of w's port, not three (the size line, the bytes past the
// buffer, the CRLF), as StreamOut does. O(b.Len()), one copy of each byte;
// allocates nothing.
WriteChunks(w: mut Writer, b: []u8): WriteError!void {
    var buf: [ChunkRoom]u8 = [0; ChunkRoom]
    var off := 0
    for off < b.Len() {
        k := if b.Len() - off > MaxChunk { MaxChunk } else { b.Len() - off }
        var j := ChunkLine - 2
        var x := k
        for {
            j -= 1
            buf[j] = HexDigit(x & 15)
            x = x >> 4
            if x == 0 {
                break
            }
        }
        buf[ChunkLine - 2] = CR
        buf[ChunkLine - 1] = LF
        (mut buf[ChunkLine..ChunkLine + k]).CopyFrom(b[off..off + k])
        buf[ChunkLine + k] = CR
        buf[ChunkLine + k + 1] = LF
        try w.Bytes(buf[j..ChunkLine + k + 2])
        off += k
    }
}

HexDigit(d: int): u8 {
    if d < 10 {
        return Trunc[u8](0x30 + d)
    }
    return Trunc[u8](0x61 + d - 10)
}

// CleanValue copies v into a with CR, LF and NUL replaced by SP.
CleanValue(a: &Arena, v: str): OutOfMemory!str from a {
    b := v.AsBytes()
    var bad := false
    for x in b {
        if x == CR || x == LF || x == 0 {
            bad = true
            break
        }
    }
    if !bad {
        return try a.Dup(v)
    }
    c := try a.Slice[u8](b.Len())
    for i in 0..b.Len() {
        x := b[i]
        c[i] = if x == CR || x == LF || x == 0 { SP } else { x }
    }
    // Replacing ASCII bytes by an ASCII byte keeps v's UTF-8 well formed.
    return str.From(c[..]).Must()
}

// IsToken: s is a non-empty token (RFC 9110 5.6.2).
IsToken(s: str): bool {
    b := s.AsBytes()
    if b.Len() == 0 {
        return false
    }
    for i in 0..b.Len() {
        if !IsTchar(b[i]) {
            return false
        }
    }
    return true
}

// Reason is the reason phrase RFC 9110 section 15 gives code, or "" for a
// code it does not name (the phrase is optional, RFC 9112 4).
Reason(code: int): str {
    if code == 200 {
        return "OK"
    }
    if code == 201 {
        return "Created"
    }
    if code == 202 {
        return "Accepted"
    }
    if code == 204 {
        return "No Content"
    }
    if code == 206 {
        return "Partial Content"
    }
    if code == 100 {
        return "Continue"
    }
    if code == 101 {
        return "Switching Protocols"
    }
    if code == 301 {
        return "Moved Permanently"
    }
    if code == 302 {
        return "Found"
    }
    if code == 303 {
        return "See Other"
    }
    if code == 304 {
        return "Not Modified"
    }
    if code == 307 {
        return "Temporary Redirect"
    }
    if code == 308 {
        return "Permanent Redirect"
    }
    if code == 400 {
        return "Bad Request"
    }
    if code == 401 {
        return "Unauthorized"
    }
    if code == 403 {
        return "Forbidden"
    }
    if code == 404 {
        return "Not Found"
    }
    if code == 405 {
        return "Method Not Allowed"
    }
    if code == 408 {
        return "Request Timeout"
    }
    if code == 409 {
        return "Conflict"
    }
    if code == 411 {
        return "Length Required"
    }
    if code == 413 {
        return "Content Too Large"
    }
    if code == 414 {
        return "URI Too Long"
    }
    if code == 415 {
        return "Unsupported Media Type"
    }
    if code == 422 {
        return "Unprocessable Content"
    }
    if code == 429 {
        return "Too Many Requests"
    }
    if code == 431 {
        return "Request Header Fields Too Large"
    }
    if code == 500 {
        return "Internal Server Error"
    }
    if code == 501 {
        return "Not Implemented"
    }
    if code == 502 {
        return "Bad Gateway"
    }
    if code == 503 {
        return "Service Unavailable"
    }
    if code == 504 {
        return "Gateway Timeout"
    }
    if code == 505 {
        return "HTTP Version Not Supported"
    }
    return ""
}
