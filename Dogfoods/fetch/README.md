# fetch

A command-line HTTP client in the manner of curl: HTTP and HTTPS transfers, redirects, cookies, compressed bodies, resumable downloads, uploads, retries and WebSocket, with curl's options and exit codes. It shows Mk's `std.http`, `std.tls`, `std.websocket`, `std.compress` and `std.x509`, and tests itself against a scripted server in the same process.

## Running it

From the program's folder:

```
cd Dogfoods/fetch
mk run -- --origin cases/data/site.origin 'http://{origin}/hello'
```

```text
hello, world
```

`--origin SCRIPT` starts a scripted origin server inside the same process on loopback, and `{origin}` in a URL names it. Other ways to run it:

| Command | Does |
|---|---|
| `fetch URL...` | Writes each body to standard output, or to a file with `-o` |
| `fetch -L`, `-i`, `-I`, `-f`, `-d`, `-H`, `-u`, `-X` | Follow redirects, show the head, HEAD only, fail on 4xx/5xx, send data, add a header, Basic auth, set the method |
| `fetch -o FILE -C --retry N` | Stream a body into a file and resume it with a `Range` request, retrying within `--max-time` |
| `fetch --compressed` | Accept gzip, deflate and zstd bodies |
| `fetch -b JAR -c JAR` | Send and keep cookies in a jar file |
| `fetch --data-file FILE [--chunked]` | Stream a request body from a file |
| `fetch --deny CLASSES` | Refuse loopback, private, link-local, ipv4 or ipv6 addresses |
| `fetch --cacert ROOTS.pem https://...` | Verify the server against a root file; `--handshake` checks the TLS connection alone |
| `fetch --parallel URL...` | Run every transfer at once on one client |
| `fetch --http2 URL...` | Offer HTTP/2 over TLS and accept HTTP/1.1 when the server selects it |
| `fetch --http2-prior-knowledge URL...` | Use HTTP/2 directly on cleartext URLs |
| `fetch --http2-window BYTES ...` | Set the per-stream HTTP/2 receive window |
| `fetch --http3`, `fetch --http3-only` | Try HTTP/3 first on HTTPS URLs, or require it |
| `fetch --drive URL...` | Service idle connections and use HTTP/3 when advertised by Alt-Svc |
| `fetch --ws-send TEXT ws://...` | Send WebSocket messages in order and print each reply |
| `fetch url [--base URL] [--set PART=VALUE] URL` | Parse a URL and print its parts |
| `fetch cert CHAIN.pem [--cacert ROOTS.pem] [--host NAME]` | Print and verify a certificate chain |
| `fetch --seed N [--fault NAME]` | Run the session under `std.sim`, with a fault injected |

Exit statuses follow curl's where curl has one (`fetch --help` lists them): 2 a bad command line, 3 a bad URL, 7 a refused or denied connection, 18 a body cut short, 22 an error status under `-f`, 28 a timeout, 35 a failed handshake, 47 too many redirects, 60 a certificate that does not verify, 63 a body past `--fetch-limit`, and others.

### The origin script

One route per `route METHOD PATH` line (`*` matches any method, and a `GET` route answers `HEAD`), then directives that shape its answer:

| Directive | Answer |
|---|---|
| `status N`, `header Name: value` | Status and headers; `{origin}` and the other placeholders are expanded |
| `body TEXT`, `file PATH`, `repeat N TEXT` | The body (`\n`, `\r`, `\t` escapes), a file relative to the script, a repeated text |
| `gzip`, `zlib`, `chunked` | Content coding and framing |
| `cut N`, `cut-unranged N`, `stall` | Close after N body bytes (always, or only without `Range`), or never answer |
| `echo` | A body that describes the request: method, target, headers, the body's size and CRC-32 |
| `ranges`, `modified DATE` | `bytes=N-` ranges (206 or 416); `Last-Modified` and 304 |
| `websocket` | The RFC 6455 handshake, then an echo of every message |

The origin listens on four loopback ports: `{origin}` and `{origin2}` over plain TCP, so a redirect can change origin, and, with `--tls-chain` and `--tls-key`, `{tls}` and `{tls2}` over TLS 1.3. No port reaches the output.

`--origin-h2` serves the cleartext ports with prior knowledge HTTP/2 support and the TLS ports with HTTP/2 ALPN. `--origin-h3` also binds UDP on both TLS ports. The multiplexed origins support ordinary HTTP routes, including delayed responses, ranges, and echo. Use the default origin for WebSocket or deliberately truncated HTTP/1.1 bodies.

## Design

- One `http.Client` serves every transfer; `--parallel` runs them in tasks, and each transfer's output is kept until all end, so it prints in command-line order.
- A large body streams into its file through `Client.Do`, and `--data-file` streams a request body through a `Reader` (`Client.DoStreaming`).
- `--max-memory BYTES` holds each transfer's output in a `mem.Fixed` of that size, so a response past it fails with a real `OutOfMemory` from the allocator (exit 27).
- The scripted origin and a WebSocket echo server written by hand to RFC 6455 run in the same process, so `websocket.Dial` is checked against an implementation that is not std's.
- `--seed N` runs the whole session on `std.sim`, with `--fault` arming a refused connect, a reset read or a denied accept.

## Limits

- Redirects are followed inside `http.Client`, so the jar sees only the final response's `Set-Cookie` headers; a cookie set by a 3xx is lost. Without `-L` the client follows no redirect and prints the 3xx reply as it came.
- The WebSocket mode sends the messages given on the command line and prints each reply; it is not interactive.
- `--max-memory` cannot be combined with `--parallel`, since an `&Alloc` stays in its task.
- A body that is not UTF-8 is not written to standard output; `-o FILE` saves it.
- `--deny` takes address classes, not single addresses: `ClientLimits.allow` is a plain `fn` with no state, so the classes live in one global `AtomicInt`.
- `--http3-only` accepts HTTPS URLs and disables TCP retry. Use `--http3` when following redirects with `-L` or checking the TCP TLS handshake with `--handshake`.

## Tests

The cases in `cases/` run every transfer against the scripted origin on loopback: redirects, cookies, decoders, resume and retry, uploads, TLS errors (expired, wrong name, unknown root), WebSocket, URL parsing and simulated network faults. Run them with `go run ./tools/dogfood run -only fetch` from the repository root.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
Scope.Remaining(&self): ?time.Duration
Scope.CancelAfter(&self, d: time.Duration)
Scope.Expired(&self): bool
// std.fs
fs.OpenWith(io: Io, p: str, o: Options): fs.Error!File
fs.Stat(io: Io, p: str): fs.Error!Info
fs.Open(io: Io, p: str): fs.Error!File
File.Read(mut self, io: Io, buf: mut []u8): fs.Error!int
File.WriteAll(mut self, io: Io, data: []u8): fs.Error!void
File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf
File.Size(&self, io: Io): fs.Error!i64
File.Close(self, io: Io): fs.Error!void
File.Abandon(self)
fs.ReadText(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a
fs.ReadBytes(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory![]u8 from a
fs.WriteAll(io: Io, p: str, data: []u8): fs.Error!void
// std.net
net.Listen(io: Io, addr: str): net.Error!Listener
net.Dial(io: Io, addr: str): net.Error!Stream
Listener.Accept(&self, io: Io): net.Error!Stream
Listener.Port(&self): int
Listener.Close(self)
Stream.Conn(self): Conn
Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Conn.Close(self, io: Io): net.Error!void
Conn.Abandon(self)
Secure.Connect(&self, h: &Heap into conn, io: Io, conn: Conn, host: str): net.Error!Conn from h, conn
enum SecureFailure {
// std.proc
proc.Exit(code: int): never
// std.atomic
AtomicInt.New(n: i64): AtomicInt
AtomicInt.Load(&self): i64
AtomicInt.Store(&self, n: i64)
// std.time
time.ParseHttpDate(s: str): ParseError!Time
time.FormatHttpDate(a: &Arena, t: Time): OutOfMemory!str from a
time.Wall(io: Io): Time
time.FromUnix(seconds: i64, nanos: i64): Time
Time.Unix(self): i64
Time.Add(self, d: Duration): Time
time.Seconds(n: i64): Duration
time.ParseDuration(s: str): ParseError!Duration
Duration.Nanos(self): i64
time.Hours(n: i64): Duration
// std.hash
hash.Crc32(data: []u8): u32
hash.Sha1(data: []u8): [20]u8
Crc32State.New(): Crc32State
Crc32State.Bytes(mut self, data: []u8)
Crc32State.Sum(self): u32
// std.http
error BodyNotReplayable
http.ReadRequest(a: &Arena into a, io: Io, r: mut Reader, limit: int): http.Error!Request from a
Request.Header(&self, name: str): ?str from self
Client.New(h: &Heap, l: ClientLimits): Client from h, l
Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Exchange from a, self
Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Reply from a
Client.DoStreaming(&self, a: &Arena, io: Io, req: &ClientRequest, body: mut Reader, len: ?i64): http.Error!Exchange from a, self
Client.Drive(&self, io: Io): Canceled!void
Client.Stop(&self)
Client.Close(self)
http.ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void)): http.Error!void
Response.Status(mut self, code: int)
Response.Header(mut self, name: str, value: str): OutOfMemory!void
Response.Body(mut self, data: []u8): OutOfMemory!void
Exchange.Version(&self): HttpVersion
Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
Exchange.Close(self, io: Io): http.Error!void
Exchange.Abandon(self)
struct ClientRequest {
linear struct Exchange {
struct ClientLimits {
struct Header {
struct Request {
struct Reply {
// std.quic
quic.Connector(c: &Config): net.MuxDialer from c
quic.Acceptor(c: &Config): net.MuxServer from c
// std.crypto
crypto.ParsePrivateKey(der: []u8): crypto.Error!PrivateKey
// std.compress
compress.Gzip(a: &Arena, data: []u8): OutOfMemory![]u8 from a
compress.Zlib(a: &Arena, data: []u8): OutOfMemory![]u8 from a
// std.sim
Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim
Sim.Io(&self): Io
Sim.FailNext(mut self, op: Op, f: Fault)
Sim.Free(self)
enum Op {
enum Fault {
// std.encoding
encoding.PercentEncode(a: &Arena, s: []u8, set: PercentSet): OutOfMemory!str from a
encoding.PercentDecode(a: &Arena, s: str): OutOfMemory![]u8 from a
encoding.Base64Encode(a: &Arena, data: []u8, alphabet: Alphabet): OutOfMemory!str from a
encoding.HexEncode(a: &Arena, data: []u8): OutOfMemory!str from a
encoding.ParsePem(a: &Arena, text: str into a): encoding.Error|OutOfMemory!List[PemBlock] from a, text
// std.url
*
// std.websocket
error Protocol(code: u16)
error TooLarge
error PeerClosed(code: u16)
error Handshake(status: int)
type Error = Protocol | TooLarge | PeerClosed | Handshake | net.Error | http.NoSecureTransport | OutOfMemory
websocket.Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): websocket.Error!Conn from h
Conn.Send(&self, io: Io, kind: Kind, data: []u8): websocket.Error!void
Conn.Recv(&self, a: &Arena, io: Io): websocket.Error!Message from a
Conn.Ping(&self, io: Io, data: []u8): websocket.Error!void
Conn.Close(self, io: Io, code: u16, reason: str): websocket.Error!void
Conn.Protocol(&self): ?str from self
Conn.Abandon(self)
enum Kind {
struct Message {
struct Options {
// std.x509
x509.ParsePem(a: &Arena, pem: str): x509.Error!List[Certificate] from a
x509.SystemRoots(a: &Arena, io: Io): x509.Error!Pool from a
x509.Verify(a: &Arena, io: Io, chain: []Certificate, roots: &Pool, now: time.Time, host: str): x509.Error!void
Pool.New(certs: []Certificate): Pool from certs
Certificate.Subject(&self, a: &Arena): OutOfMemory!str from a
Certificate.Issuer(&self, a: &Arena): OutOfMemory!str from a
Certificate.Names(&self, a: &Arena): OutOfMemory!List[str] from a, self *self into a
Certificate.NotBefore(&self): time.Time
Certificate.NotAfter(&self): time.Time
Certificate.Der(&self): []u8 from self
// std.tls
*
```
