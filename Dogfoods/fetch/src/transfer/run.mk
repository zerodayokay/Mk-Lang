// Why: one transfer, with retries, resume, uploads and curl's exit codes.
// What: Run, RunIn, Attempt, Upload, Collect and the Failure set.
// How: every attempt runs under --max-time; a retryable failure waits
// --retry-delay and tries again, and every failure is reported with the
// exit code curl gives it.

import std.fs
import std.hash
import std.http
import std.net
import std.time
import std.url
import src.origin

// the buffer a streamed body is read through, and the chunk written per read
const BodyBuffer = 32768

error Truncated(got: i64)
error LocalFile(why: fs.Error)
error Refused(status: int)

/// Why one attempt failed.
type Failure = http.Error | url.Error | fs.Error | Truncated | Refused | LocalFile

/// Runs job against c: one attempt, then up to job.retry more after a
/// retryable failure, all under job.max_time when it is set. Writes the
/// response, the messages and the exit code into res; never fails, so it
/// can be a task of its own. Allocates one arena on h for the transfer and
/// res's texts on h.
export Run(c: &http.Client, h: &Heap, io: Io, job: &Job, addrs: &origin.Addrs, res: mut Outcome) {
    RunIn(c, h, h.Alloc(), io, job, addrs, res)
}

/// Run with res's texts on al, which may be a budget smaller than the
/// response: a text that does not fit fails the transfer with exit code 27,
/// as curl's out of memory. Not a task of its own, since al stays on the
/// caller's side.
export RunIn(c: &http.Client, h: &Heap, al: &Alloc, io: Io, job: &Job, addrs: &origin.Addrs, res: mut Outcome) {
    var a := Arena.New(h) else {
        Report(al, res, job, 27, "out of memory")
        return
    }
    defer a.Free()
    scope s := io.Scope() {
        if d := job.max_time {
            s.CancelAfter(d)
        }
        var tries := 0
        for {
            tries += 1
            Attempt(&a, c, io, job, al, res) else e {
                if s.Expired() {
                    text := a.Fmt("timed out after {tries} attempt(s)", (tries)) else { "timed out" }
                    Report(al, res, job, 28, text)
                    return
                }
                (code, text: described) := Describe(&a, e)
                // a refusal by --deny is reported as Refused, so say both
                text := if code == 7 && job.deny { "connection refused, or denied by --deny" } else { described }
                if tries > job.retry || !Retryable(code) {
                    Report(al, res, job, code, text)
                    return
                }
                // sleep no longer than the deadline leaves: past it the retry could not run
                wait := job.retry_delay
                if left := s.Remaining() {
                    if left.Nanos() <= wait.Nanos() {
                        Report(al, res, job, 28, a.Fmt("{text}; giving up after {tries} attempt(s), as the next would start past --max-time", (text, tries)) else { "giving up" })
                        return
                    }
                }
                Note(al, res, job, a.Fmt("attempt {tries} failed: {text}; retrying", (tries, text)) else { "retrying" })
                io.Sleep(wait) else {
                    Report(al, res, job, 28, "timed out while waiting to retry")
                    return
                }
                continue
            }
            return
        }
    }
}

// Retryable is whether a failure with this exit code may pass on a second
// try: a refused or reset connection, a closed or cut transfer, a 5xx.
Retryable(code: int): bool {
    return code == 7 || code == 18 || code == 52 || code == 56 || code == 22
}

// Attempt makes one request for job and writes its result into res.
Attempt(a: &Arena, c: &http.Client, io: Io, job: &Job, al: &Alloc, res: mut Outcome): Failure!void *job into a {
    var u := try url.Parse(a, job.link, none)
    if job.query.Len() > 0 {
        q := u.Query()
        joined := if q.Len() > 1 { try a.Fmt("{q}&{job.query}", (q, job)) } else { job.query }
        u = try u.Set(a, url.Part.Search, joined)
    }
    var headers := List[http.Header].New(a)
    for h in job.headers {
        try headers.Push(h)
    }
    if v := job.cookie {
        try headers.Push(http.Header(name: "Cookie", value: v))
    }
    if v := job.since {
        try headers.Push(http.Header(name: "If-Modified-Since", value: v))
    }
    var resume_from: i64 = 0
    if path := job.output {
        if job.resume {
            resume_from = try Existing(io, path)
            if resume_from > 0 {
                try headers.Push(http.Header(name: "Range", value: try a.Fmt("bytes={resume_from}-", (resume_from))))
            }
        }
    }
    method := if job.method.Len() > 0 { job.method } else if job.head { "HEAD" } else if job.body.Len() > 0 || job.data_file.IsSome() { "POST" } else { "GET" }
    req := http.ClientRequest(method: method, url: &u, headers: headers.Done()[..], body: job.body)
    if job.verbose {
        Verbose(a, al, res, &req, u.Path())
    }
    if path := job.output {
        try Save(a, c, io, job, &req, path, resume_from, al, res)
        return
    }
    if path := job.data_file {
        try Upload(a, c, io, job, &req, path, al, res)
        return
    }
    reply := try c.Fetch(a, io, &req)
    try Received(a, al, res, job, reply.status, reply.headers)
    if job.head {
        return
    }
    text := str.From(reply.body) else {
        n := reply.body.Len()
        Note(al, res, job, a.Fmt("binary body of {n} bytes not shown; save it with -o FILE", (n)) else { "binary body not shown" })
        return
    }
    try res.out.Push(al, text)
}

// Upload sends the file at path as the request's body through a Reader,
// with its size as Content-Length or, under --chunked, chunked, and keeps
// the response's body as Fetch would.
Upload(a: &Arena into a, c: &http.Client, io: Io, job: &Job, req: &http.ClientRequest, path: str, al: &Alloc, res: mut Outcome): Failure!void {
    var f := fs.Open(io, path) else e {
        fail LocalFile(why: e)
    }
    onerr f.Abandon()
    size := f.Size(io) else e {
        fail LocalFile(why: e)
    }
    len: ?i64 = if job.chunked { none } else { some(size) }
    var buf: [BodyBuffer]u8 = [0; BodyBuffer]
    var body := f.Reader(io, mut buf[..])
    var x := try c.DoStreaming(a, io, req, mut body, len)
    onerr x.Abandon()
    try f.Close(io)
    try Received(a, al, res, job, x.status, x.headers)
    data := try Collect(a, mut x)
    try x.Close(io)
    text := str.From(data) else {
        n := data.Len()
        Note(al, res, job, a.Fmt("binary body of {n} bytes not shown; save it with -o FILE", (n)) else { "binary body not shown" })
        return
    }
    try res.out.Push(al, text)
}

// Collect reads the rest of x's body into a, growing its buffer by
// doubling.
Collect(a: &Arena, x: mut http.Exchange): Failure![]u8 from a {
    var buf: [BodyBuffer]u8 = [0; BodyBuffer]
    var body := x.Body(mut buf[..])
    var data := try a.Slice[u8](BodyBuffer)
    var n := 0
    for {
        if n == data.Len() {
            bigger := try a.Slice[u8](data.Len() * 2)
            (mut bigger[..n]).CopyFrom(data[..n])
            data = bigger
        }
        got := body.Read(mut data[n..]) else {
            fail Truncated(got: i64(n))
        }
        if got == 0 {
            break
        }
        n += got
    }
    return data[..n]
}

// Existing is the size of the file at path, 0 when there is none.
Existing(io: Io, path: str): fs.Error!i64 {
    info := fs.Stat(io, path) else e {
        match e {
            fs.NotFound { return 0 }
            _ { fail e }
        }
    }
    return info.size
}

// Save streams the body of req's response into path through Client.Do,
// appending when the server answers a ranged request with 206.
Save(a: &Arena into a, c: &http.Client, io: Io, job: &Job, req: &http.ClientRequest, path: str, from: i64, al: &Alloc, res: mut Outcome): Failure!void {
    var x := try c.Do(a, io, req)
    onerr x.Abandon()
    try Received(a, al, res, job, x.status, x.headers)
    if x.status == 416 && from > 0 {
        try x.Close(io)
        try Summary(a, io, al, res, job, path, "already complete")
        return
    }
    if x.status >= 300 {
        try x.Close(io)
        return
    }
    appending := x.status == 206 && from > 0
    var opts := fs.Append
    if !appending {
        opts = fs.DefaultOptions
        opts.write = true
        opts.create = true
        opts.truncate = true
    }
    var f := try fs.OpenWith(io, path, opts)
    onerr f.Abandon()
    var got: i64 = 0
    var buf: [BodyBuffer]u8 = [0; BodyBuffer]
    var chunk: [BodyBuffer]u8 = [0; BodyBuffer]
    var body := x.Body(mut buf[..])
    for {
        n := body.Read(mut chunk[..]) else {
            try f.Close(io)
            fail Truncated(got: got)
        }
        if n == 0 {
            break
        }
        try f.WriteAll(io, chunk[..n])
        got += i64(n)
    }
    try f.Close(io)
    try x.Close(io)
    try Summary(a, io, al, res, job, path, if appending { "resumed" } else { "saved" })
}

// Summary reports a saved file by its size and CRC-32, read back from disk.
Summary(a: &Arena, io: Io, al: &Alloc, res: mut Outcome, job: &Job, path: str, how: str): fs.Error|OutOfMemory!void {
    if job.silent {
        return
    }
    var f := try fs.Open(io, path)
    onerr f.Abandon()
    var crc := hash.Crc32State.New()
    var size: i64 = 0
    var buf: [BodyBuffer]u8 = [0; BodyBuffer]
    for {
        n := try f.Read(io, mut buf[..])
        if n == 0 {
            break
        }
        crc.Bytes(buf[..n])
        size += i64(n)
    }
    try f.Close(io)
    sum := crc.Sum()
    try res.out.Push(al, try a.Fmt("{how} {path}: {size} bytes, crc32 {sum:08x}\n", (how, path, size, sum)))
}

// Received checks a response's status and keeps what the caller prints of
// its head: the head itself under --include, and every Set-Cookie for the
// jar.
Received(a: &Arena, al: &Alloc, res: mut Outcome, job: &Job, status: int, headers: []http.Header): Failure!void {
    for h in headers {
        if Lower(a, h.name) == "set-cookie" {
            try res.cookies.Push(al, try a.Fmt("{h.value}\n", (h)))
        }
    }
    if job.include || job.head {
        try res.out.Push(al, try a.Fmt("HTTP/1.1 {status}\n", (status)))
        for h in headers {
            try res.out.Push(al, try a.Fmt("{h.name}: {h.value}\n", (h)))
        }
        try res.out.Push(al, "\n")
    }
    if job.verbose {
        try res.err.Push(al, try a.Fmt("< {status}\n", (status)))
    }
    if job.fail_status && status >= 400 {
        fail Refused(status: status)
    }
}

// Verbose writes the request line and headers to res's error text.
Verbose(a: &Arena, al: &Alloc, res: mut Outcome, req: &http.ClientRequest, path: str) {
    line := a.Fmt("> {req.method} {path}\n", (req, path)) else {
        return
    }
    res.err.Push(al, line) else {}
    for h in req.headers {
        text := a.Fmt("> {h.name}: {h.value}\n", (h)) else {
            return
        }
        res.err.Push(al, text) else {}
    }
}

// Lower is s in ASCII lower case, s itself when it has no upper case.
Lower(a: &Arena, s: str): str from a, s {
    return s.ToLower(a) else { s }
}

// Report ends a transfer with code and message.
Report(al: &Alloc, res: mut Outcome, job: &Job, code: int, text: str) {
    res.code = code
    res.err.Push(al, "fetch: ") else {}
    res.err.Push(al, job.shown) else {}
    res.err.Push(al, ": ") else {}
    res.err.Push(al, text) else {}
    res.err.Push(al, "\n") else {}
}

// Note adds an informational line, unless --silent.
Note(al: &Alloc, res: mut Outcome, job: &Job, text: str) {
    if job.silent {
        return
    }
    res.err.Push(al, "fetch: ") else {}
    res.err.Push(al, job.shown) else {}
    res.err.Push(al, ": ") else {}
    res.err.Push(al, text) else {}
    res.err.Push(al, "\n") else {}
}

/// The exit code and message for a failed transfer, curl's codes where
/// curl has one: 3 a bad URL, 6 an unknown host, 7 a refused or denied
/// connection, 8 a reply that is not HTTP, 18 a body cut short, 22 a status of 400 or more under
/// --fail, 23 a file that cannot be written, 28 a timeout, 35 a failed
/// TLS handshake, 26 a body file that cannot be read, 47 too many redirects, 52 no reply, 56 a reset, 60 a
/// certificate that does not verify, 63 a body past --fetch-limit, 65 a
/// streamed body a redirect asks for again, 27 a response past
/// --max-memory.
Describe(a: &Arena, e: Failure): (code: int, text: str) from a {
    text := a.Fmt("{e}", (e)) else { "error" }
    return match e {
        url.BadUrl | url.BadHost | url.IdnaUnsupported | http.BadUrl { (code: 3, text: a.Fmt("bad URL: {e}", (e)) else { text }) }
        net.InvalidAddress { (code: 6, text: "could not resolve or parse the host") }
        net.Refused | net.Unreachable { (code: 7, text: "connection refused") }
        net.Unsupported { (code: 7, text: "connection not allowed") }
        Truncated(got) { (code: 18, text: a.Fmt("transfer closed after {got} body bytes", (got)) else { "transfer closed" }) }
        Refused(status) { (code: 22, text: a.Fmt("the server answered {status}", (status)) else { "error status" }) }
        http.Status(code) { (code: 22, text: a.Fmt("the server answered {code}", (code)) else { "error status" }) }
        time.TimedOut { (code: 28, text: "timed out") }
        net.SecureFailed(why) { Secure(why) }
        http.TooManyRedirects { (code: 47, text: "too many redirects") }
        http.BodyNotReplayable { (code: 65, text: "the redirect asks for the body again, and a streamed body cannot be sent twice") }
        LocalFile(why) { (code: 26, text: a.Fmt("cannot read the body file: {why}", (why)) else { "cannot read the body file" }) }
        Closed { (code: 52, text: "the server closed the connection without a reply") }
        net.Reset | ReadError { (code: 56, text: "the connection failed while receiving") }
        http.BadRequest { (code: 8, text: "the reply is not HTTP/1.1") }
        http.TooLarge { (code: 63, text: "the body is larger than --fetch-limit") }
        http.NoSecureTransport { (code: 1, text: "https is not available") }
        OutOfMemory { (code: 27, text: "out of memory: the response does not fit in --max-memory") }
        fs.NotFound | fs.Denied | fs.IsDirectory | fs.NotDirectory | fs.NoSpace | fs.InvalidPath | fs.Exists { (code: 23, text: a.Fmt("cannot write the output file: {e}", (e)) else { text }) }
        _ { (code: 1, text: text) }
    }
}

// Secure is the exit code and message of a failed TLS connection.
Secure(why: net.SecureFailure): (code: int, text: str) {
    return match why {
        .Handshake { (code: 35, text: "the TLS handshake failed") }
        .BadCertificate { (code: 60, text: "the server's certificate is malformed or misused") }
        .UnknownAuthority { (code: 60, text: "the server's certificate is not signed by a trusted root") }
        .Expired { (code: 60, text: "the server's certificate has expired") }
        .NameMismatch { (code: 60, text: "the server's certificate does not name the host") }
        .Protocol { (code: 35, text: "the TLS peer broke the protocol") }
        _ { (code: 35, text: "the TLS connection failed") }
    }
}
