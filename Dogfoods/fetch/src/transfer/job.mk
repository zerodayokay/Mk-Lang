// Why: a transfer is described once and then run, maybe in parallel.
// What: Job, one URL's transfer as the command line asked for it, and
// Outcome, what it produced.
// How: an Outcome is kept until every transfer has ended, so output from
// parallel tasks prints in command-line order.

import std.http
import std.time

/// One URL's transfer, as the command line asked for it.
export struct Job {
    export method: str
    // the URL with its placeholders expanded
    export link: str
    // the URL as messages show it, every loopback address written back as
    // its placeholder
    export shown: str
    // the query pairs to add, already form-encoded, "" for none
    export query: str
    export headers: []http.Header
    export body: []u8
    // a file whose bytes are the body, sent through a Reader as they are read
    export data_file: ?str
    // send data_file with chunked framing instead of its length
    export chunked: bool
    export output: ?str
    export resume: bool
    export include: bool
    export head: bool
    export versioned: bool
    export h3_only: bool
    export fetch_limit: int
    export fail_status: bool
    export verbose: bool
    export silent: bool
    // whether --deny filters the addresses connected to
    export deny: bool
    export max_time: ?time.Duration
    export retry: int
    export retry_delay: time.Duration
    // the Cookie header the jar has for this URL
    export cookie: ?str
    // the If-Modified-Since date
    export since: ?str
}

/// What one transfer produced, kept by the caller until every transfer has
/// ended, so output from parallel tasks prints in the order of the command
/// line.
export struct Outcome {
    export out: HeapText
    export err: HeapText
    // every Set-Cookie value of the final response, one a line
    export cookies: HeapText
    export code: int
}

/// An empty outcome whose texts live on a.
export NewOutcome(a: &Alloc): Outcome {
    return Outcome(out: HeapText.New(a), err: HeapText.New(a), cookies: HeapText.New(a), code: 0)
}

impl Free for Outcome {
    Free(self, a: &Alloc) {
        Outcome(out, err, cookies, code: _) := self
        out.Free(a)
        err.Free(a)
        cookies.Free(a)
    }
}
