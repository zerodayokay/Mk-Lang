// Why: Every handler replies, logs and reports failures the same way.
//
// What: App, Reply, Problem, Failure, StoreFailed, Encode and the threshold
// GzipMin.
//
// How: Reply gzips a body of at least GzipMin bytes when the request accepts
// gzip, and logs the request at Warn for a 4xx and Error for a 5xx.

import std.compress
import std.db.sqlite
import std.http
import std.json
import std.log
import src.notes
import src.store

/// Responses at least this long are compressed when the client accepts gzip.
export const GzipMin: int = 256

/// What every request's handler shares: the store, the heap for the request
/// log, and the log itself. Every connection's task reads it through `&App`.
export struct App {
    export store: store.Store
    export heap: &Heap
    export log: AccessLog
}

/// Releases the app's log, as `AccessLog.Close` does.
export App.Close(self, a: &Arena, io: Io): (lines: str, closed: bool) from a {
    App(store, heap, log) := self
    _ = store
    return log.Close(a, io, heap.Alloc())
}

/// Logs one line through the app's log at `level`. A line that cannot be
/// written is dropped: a request never fails because its log line did.
export App.Note(&self, a: &Arena, io: Io, level: log.Level, line: str) {
    self.log.Write(a, io, self.heap.Alloc(), level, line) else {}
}

/// Sends `body`, a JSON text, with `status`: gzip-compressed when the request
/// accepts gzip and the body is at least `GzipMin` bytes. Logs the request
/// with its status, at `Warn` for a 4xx and `Error` for a 5xx.
export Reply(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, status: int, body: str): http.Error!void {
    res.Status(status)
    try res.Header("Content-Type", "application/json")
    var sent := body.Len()
    if body.Len() >= GzipMin && AcceptsGzip(req) {
        packed := try compress.Gzip(a, body.AsBytes())
        try res.Header("Content-Encoding", "gzip")
        try res.Header("Vary", "Accept-Encoding")
        try res.Body(packed)
        sent = packed.Len()
    } else {
        try res.Body(body.AsBytes())
    }
    level := if status >= 500 { log.Level.Error } else if status >= 400 { log.Level.Warn } else { log.Level.Info }
    line := try a.Fmt("{m} {p} {status} {sent}", (m: req.method, p: req.path, status, sent))
    c.Note(a, io, level, line)
}

/// Sends a problem body: `{"message": ..., "offset": ...}`.
export Problem(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, status: int, message: str, offset: ?int): http.Error!void {
    p := notes.Problem(message: message, offset: offset)
    try Reply(c, a, io, req, res, status, try Encode(a, &p))
}

/// The status and message a store failure is reported with: 409 for a
/// conflict, 503 for a locked database, else 500.
export Failure(e: store.Error): (status: int, message: str) {
    return match e {
        store.Conflict { (status: 409, message: "another note has this title") }
        sqlite.Busy { (status: 503, message: "the database is locked; try again") }
        sqlite.Sqlite | sqlite.Misuse | OutOfMemory | Canceled { (status: 500, message: "the database failed") }
        _ {
            Panic("sqlite.Error gained a member this match does not handle")
        }
    }
}

/// Reports a store failure with `Failure`'s status, and a `Retry-After`
/// header on 503.
export StoreFailed(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, e: store.Error): http.Error!void {
    f := Failure(e)
    if f.status == 503 {
        try res.Header("Retry-After", "1")
    }
    line := try a.Fmt("store: {e}", (e))
    c.Note(a, io, log.Level.Debug, line)
    try Problem(c, a, io, req, res, f.status, f.message, none)
}

/// `x` as compact JSON text in `a`.
export Encode[T: json.Json](a: &Arena, x: &T): OutOfMemory!str from a {
    var t := Text.New(a)
    var w := t.Writer()
    x.FormatJson(mut w) else { fail OutOfMemory }
    return t.Done()
}

/// Whether the request's `Accept-Encoding` names gzip.
export AcceptsGzip(req: &http.Request): bool {
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

/// Reads the request body as JSON into `scratch`: a 400 problem with the
/// byte offset for text that is not UTF-8 or not JSON, and none returned
/// after it is sent. The value holds views of the body, so it lives in an
/// arena of the handler's own, and the handler copies what it keeps into `a`.
export ParseBody(c: &App, a: &Arena, scratch: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!?json.Value from scratch, req *req into scratch {
    text := str.From(req.body) else {
        try Problem(c, a, io, req, res, 400, "the body is not UTF-8", none)
        return none
    }
    v := json.Parse(scratch, text) else e {
        match e {
            json.Syntax(offset) {
                try Problem(c, a, io, req, res, 400, "the body is not JSON", some(offset))
            }
            json.Depth(offset) {
                try Problem(c, a, io, req, res, 400, "the body nests too deep", some(offset))
            }
            json.Mismatch(offset) {
                try Problem(c, a, io, req, res, 422, "the body has the wrong shape", some(offset))
            }
            OutOfMemory {
                fail OutOfMemory
            }
            _ {
                Panic("json.Error gained a member this match does not handle")
            }
        }
        return none
    }
    line := try a.Fmt("body {v}", (v: &v))
    c.Note(a, io, log.Level.Debug, line)
    return some(v)
}
