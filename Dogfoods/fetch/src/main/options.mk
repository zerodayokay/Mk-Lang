// Why: fetch takes curl's options, so its users need not learn new ones.
// What: Options, NewOptions, ParseOptions and Set, the option errors, and
// the known decoders and deny classes.
// How: short and long options, with values either attached or as the next
// argument; errors name the argument's index.

import std.http
import std.time

/// Everything the command line asks of one run.
struct Options {
    urls: List[str]
    method: str
    headers: List[http.Header]
    data: ?str
    data_file: ?str
    chunked: bool
    queries: List[str]
    user: ?str
    output: ?str
    resume: bool
    include: bool
    head: bool
    fail_status: bool
    verbose: bool
    silent: bool
    parallel: bool
    handshake: bool
    tls12: bool
    h2: bool
    h2c: bool
    h2_window: ?int
    h3: bool
    h3_only: bool
    drive: bool
    origin_h2: bool
    origin_h3: bool
    alpn: List[str]
    follow: bool
    max_redirs: int
    max_time: ?time.Duration
    retry: int
    retry_delay: time.Duration
    fetch_limit: i64
    // the bytes one transfer's output may hold, 0 for no limit
    max_memory: int
    decoders: List[str]
    deny: List[str]
    cookies: ?str
    jar: ?str
    cacert: ?str
    time_cond: ?str
    origin: ?str
    tls_chain: ?str
    tls_key: ?str
    seed: ?u64
    faults: List[str]
    // the WebSocket steps in order, each a kind (b't' text, b'b' binary,
    // b'p' ping) and its text
    ws_kinds: List[u8]
    ws_texts: List[str]
    ws_max: int
    ws_protocols: List[str]
    help: bool
}

error Usage(arg: int)
error MissingValue(arg: int)
error BadValue(arg: int)
error NoUrl

/// Why a command line cannot be read; each payload is the index of the
/// argument at fault.
type OptionError = Usage | MissingValue | BadValue | NoUrl | OutOfMemory

// the options that take a value, long and short spellings
const ValueOptions: []str = ["-o", "--output", "-H", "--header", "-X", "--request", "-d", "--data", "--data-file", "-u", "--user", "--query", "--max-redirs", "--max-time", "--retry", "--retry-delay", "--fetch-limit", "--max-memory", "--decode", "--deny", "-b", "--cookies", "-c", "--cookie-jar", "--cacert", "--alpn", "--http2-window", "--time-cond", "--origin", "--tls-chain", "--tls-key", "--seed", "--fault", "--ws-send", "--ws-binary", "--ws-ping", "--ws-max", "--ws-protocol"]

NewOptions(a: &Arena): Options {
    return Options(
        urls: List[str].New(a),
        method: "",
        headers: List[http.Header].New(a),
        data: none,
        data_file: none,
        chunked: false,
        queries: List[str].New(a),
        user: none,
        output: none,
        resume: false,
        include: false,
        head: false,
        fail_status: false,
        verbose: false,
        silent: false,
        parallel: false,
        handshake: false,
        tls12: false,
        h2: false,
        h2c: false,
        h2_window: none,
        h3: false,
        h3_only: false,
        drive: false,
        origin_h2: false,
        origin_h3: false,
        alpn: List[str].New(a),
        follow: false,
        max_redirs: 10,
        max_time: none,
        retry: 0,
        retry_delay: time.Seconds(1),
        fetch_limit: 67108864,
        max_memory: 0,
        decoders: List[str].New(a),
        deny: List[str].New(a),
        cookies: none,
        jar: none,
        cacert: none,
        time_cond: none,
        origin: none,
        tls_chain: none,
        tls_key: none,
        seed: none,
        faults: List[str].New(a),
        ws_kinds: List[u8].New(a),
        ws_texts: List[str].New(a),
        ws_max: 0,
        ws_protocols: List[str].New(a),
        help: false,
    )
}

/// Reads the client's command line, args being everything after the
/// program's name. An option's value is the next argument or follows `=`.
/// Fails at the first argument it cannot read. O(n) in the arguments;
/// allocates the lists in a.
ParseOptions(a: &Arena, args: []str): OptionError!Options from a, args *args into a {
    var o := NewOptions(a)
    var i := 0
    for i < args.Len() {
        arg := args[i]
        i += 1
        if !arg.StartsWith("-") || arg == "-" {
            try o.urls.Push(arg)
            continue
        }
        if arg == "--" {
            for i < args.Len() {
                try o.urls.Push(args[i])
                i += 1
            }
            break
        }
        // --name=value carries its value; otherwise a value option takes the next argument
        var name := arg
        var value := ""
        if eq := arg.Find("=") {
            if arg.StartsWith("--") {
                name = arg[..eq]
                value = arg[eq + 1..]
            }
        }
        at := i - 1
        if ValueOptions.Has(&name) && value.Len() == 0 && name == arg {
            if i == args.Len() {
                fail MissingValue(arg: at)
            }
            value = args[i]
            i += 1
        }
        try Set(mut o, name, value, at)
    }
    if o.urls.Len() == 0 && !o.help {
        fail NoUrl
    }
    return o
}

// Set records one option in o.
Set(o: mut Options, name: str, value: str into o, at: int): OptionError!void {
    match name {
        "-o" | "--output" { o.output = some(value) }
        "-C" | "--continue" { o.resume = true }
        "-L" | "--location" { o.follow = true }
        "-H" | "--header" {
            colon := value.Find(":") else { fail BadValue(arg: at) }
            try o.headers.Push(http.Header(name: value[..colon].Trim(), value: value[colon + 1..].Trim()))
        }
        "-X" | "--request" { o.method = value }
        "-I" | "--head" { o.head = true }
        "-i" | "--include" { o.include = true }
        "-d" | "--data" { o.data = some(value) }
        "--data-file" { o.data_file = some(value) }
        "--chunked" { o.chunked = true }
        "-u" | "--user" { o.user = some(value) }
        "--query" { try o.queries.Push(value) }
        "-f" | "--fail" { o.fail_status = true }
        "-v" | "--verbose" { o.verbose = true }
        "-s" | "--silent" { o.silent = true }
        "--parallel" { o.parallel = true }
        "--handshake" { o.handshake = true }
        "--tlsv1.2" { o.tls12 = true }
        "--http2" { o.h2 = true }
        "--http2-prior-knowledge" { o.h2c = true }
        "--http2-window" {
            n := try Count(value, at)
            if n == 0 {
                fail BadValue(arg: at)
            }
            o.h2_window = some(n)
        }
        "--http3" { o.h3 = true }
        "--http3-only" { o.h3_only = true }
        "--drive" { o.drive = true }
        "--origin-h2" { o.origin_h2 = true }
        "--origin-h3" { o.origin_h3 = true }
        "--alpn" {
            for p in value.Split(",") {
                try o.alpn.Push(p)
            }
        }
        "--max-redirs" { o.max_redirs = try Count(value, at) }
        "--max-time" { o.max_time = some(try Seconds(value, at)) }
        "--retry" { o.retry = try Count(value, at) }
        "--retry-delay" { o.retry_delay = try Seconds(value, at) }
        "--fetch-limit" { o.fetch_limit = i64(try Count(value, at)) }
        "--max-memory" { o.max_memory = try Count(value, at) }
        "--compressed" {
            for d in ["gzip", "deflate", "zstd"] {
                try o.decoders.Push(d)
            }
        }
        "--decode" {
            for d in value.Split(",") {
                if !KnownDecoders.Has(&d) {
                    fail BadValue(arg: at)
                }
                try o.decoders.Push(d)
            }
        }
        "--deny" {
            for d in value.Split(",") {
                if !DenyClasses.Has(&d) {
                    fail BadValue(arg: at)
                }
                try o.deny.Push(d)
            }
        }
        "-b" | "--cookies" { o.cookies = some(value) }
        "-c" | "--cookie-jar" { o.jar = some(value) }
        "--cacert" { o.cacert = some(value) }
        "--time-cond" { o.time_cond = some(value) }
        "--origin" { o.origin = some(value) }
        "--tls-chain" { o.tls_chain = some(value) }
        "--tls-key" { o.tls_key = some(value) }
        "--seed" { o.seed = some(Cast[u64](try Count(value, at))) }
        "--fault" {
            if !Faults.Has(&value) {
                fail BadValue(arg: at)
            }
            try o.faults.Push(value)
        }
        "--ws-send" | "--ws-binary" | "--ws-ping" {
            kind := if name == "--ws-send" { b't' } else if name == "--ws-binary" { b'b' } else { b'p' }
            try o.ws_kinds.Push(kind)
            try o.ws_texts.Push(value)
        }
        "--ws-max" { o.ws_max = try Count(value, at) }
        "--ws-protocol" {
            for p in value.Split(",") {
                try o.ws_protocols.Push(p)
            }
        }
        "-h" | "--help" { o.help = true }
        _ { fail Usage(arg: at) }
    }
}

/// The content codings --decode accepts.
const KnownDecoders: []str = ["gzip", "deflate", "zstd"]

/// The address classes --deny accepts.
const DenyClasses: []str = ["loopback", "private", "link-local", "ipv4", "ipv6", "all"]

/// The faults --fault injects under --seed.
const Faults: []str = ["connect-refused", "read-reset", "accept-denied"]

// Count reads a non-negative decimal count.
Count(value: str, at: int): BadValue!int {
    n := value.ParseInt() else { fail BadValue(arg: at) }
    if n < 0 || n > 1000000000 {
        fail BadValue(arg: at)
    }
    return int(n)
}

// Seconds reads a duration: whole seconds, as curl's options take them,
// or a duration with units such as "250ms" or "1.5s".
Seconds(value: str, at: int): BadValue!time.Duration {
    d := match value.ParseInt() {
        ok(n) {
            if n < 0 || n > 86400 {
                fail BadValue(arg: at)
            }
            time.Seconds(n)
        }
        err(_) { time.ParseDuration(value) else { fail BadValue(arg: at) } }
    }
    if d.Nanos() < 0 || d.Nanos() > time.Hours(24).Nanos() {
        fail BadValue(arg: at)
    }
    return d
}
