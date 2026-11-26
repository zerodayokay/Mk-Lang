# chat

A multi-room chat server over TCP and WebSocket, with bots in the same process. It shows a concurrent server in Mk: one task per connection, messages shared by reference count, bounded queues that drop slow clients, and a shutdown that cancels every wait.

## Running it

From the program's folder, `replay` runs a script of named clients against a server in the same process and prints the transcript:

```
cd Dogfoods/chat
mk run -- replay cases/data/basic.chat
```

```text
alice! connected
alice< MOTD Welcome to chat.
alice< OK welcome
alice> NICK alice
alice< OK nick alice
bob! connected
bob< MOTD Welcome to chat.
bob< OK welcome
bob> NICK bob
bob< OK nick bob
alice> JOIN #general
alice< OK join #general members=1 topic=
bob> JOIN #general
bob< OK join #general members=2 topic=
alice> MSG #general hello bob
alice< * bob joined #general
alice< OK msg 1
...
```

| Command | Does |
|---|---|
| `chat [options] serve` | Serve on `--addr` (127.0.0.1:7070) until Ctrl-C or `SHUTDOWN` |
| `chat [options] replay SCRIPT` | Run SCRIPT's clients against an in-process server and print the transcript |
| `chat [options] load` | Connect `--clients` clients at once and print what they saw |
| `chat drill` | Cancel a task at each kind of wait the server has and report how each ended |

`chat --help` lists the options. Errors print `chat: ...` on stderr and exit 1 (files, listener, memory, deadline) or 2 (usage, a bad script). `replay` and `load` give up after `--deadline` seconds (20 by default), print what they saw and exit 1, so a run that hangs still reports.

### The protocol

Clients send one command a line: `NICK`, `JOIN`, `PART`, `MSG`, `WHO`, `ROOMS`, `TOPIC`, `HISTORY`, `MOTD`, `STATS`, `WHOAMI`, `PING`, `OPER`, `KICK`, `SHUTDOWN`, `QUIT`. Every command is answered by a last line that starts `OK` or `ERR <code>`.

### The web front end

With `--web ADDR` the server also serves HTTP/1.1 with keep-alive. To serve HTTPS, add `--web-tls-chain CHAIN.pem` and `--web-tls-key KEY.pem`. The TLS listener offers HTTP/2 and HTTP/1.1. Server-sent events use HTTP/2 DATA frames when the client negotiates HTTP/2. WebSocket upgrades use HTTP/1.1.


| Request | Does |
|---|---|
| `GET /rooms` | Lists the rooms |
| `GET /ws` | Upgrades to a WebSocket (subprotocol `chat.v1`: one command or reply line per text message) |
| `GET /rooms/NAME/ws?nick=NICK` | The same, joining `#NAME` as `NICK` |
| `GET /rooms/NAME/events?count=N&nick=NICK` | Streams the room's traffic as server-sent events, `N` of them or until the client or the server goes |

A WebSocket may come from a client that sends no `Origin` or from an HTTP or HTTPS page on this machine (`127.0.0.1`, `localhost`, or `[::1]`, any port). Other origins receive status 403.

`--idle-ms N` drops a connection that has nothing read or queued for `N` ms (a scope deadline). `--record` logs every byte each TCP connection reads and writes. `--log LEVEL` sets what goes to stderr (`warn` by default; `info` adds each connection and its end).

### Scripts

A script is lines `name: COMMAND`; a client connects on its first line. Special lines:

| Line | Does |
|---|---|
| `name: !read` | Read one line |
| `name: !eof` | Read to the end of the stream, or to a read error, which it prints |
| `name: !close` | Hang up |
| `name: !hex 4d5347...` | Send the bytes the hex digits spell and a newline |
| `name: !ws PATH [ORIGIN]` | As a first line, with `--web`: a WebSocket client of the front end |
| `name: !events PATH` | As a first line, with `--web`: read an event stream. Cleartext reports HTTP/1.1 chunk sizes. HTTPS reports the negotiated version and decoded events. |
| `name: !get PATH` | Fetch a page and print its status and body |
| `name: !shutdown` | Close a TCP client's sending side and print what the server still sends |

For HTTPS replay against a private CA, give `--web-cacert ROOTS.pem`. The `web-h2` case reads one event before sending the next room message to verify that `Response.Flush` sends it immediately.

## Design

- The room table is a fixed, plain struct with a bit per member slot, behind one `sync.RwLock`, so it allocates nothing.
- A message to a room is one `sync.Shared` frame; each member's slot gets a reference-counted clone. Each slot has a bounded queue with credits, and a client out of credit is dropped by its own kicker task, even while a write to it is blocked.
- Each client has a rate limit, refilled on a timer, and a memory budget counted by `mem.Counting`.
- Server counters are single atomic words, so readers never wait.
- A web client's session is the TCP session, run over a bridge of two channels that the session sees as an ordinary `net.Transport`.
- Shutdown tells every client and waits for them to go. A session shuts its sending side first (`Conn.Shutdown`), then reads until the client closes, for at most 2 s, so the client reads the closing notice.

## Limits

- At most 1,024 clients (TCP and web together; one web client fewer at once than `--max-clients`, since the TCP accept loop holds a place while it waits) and 32 rooms.
- A line is at most 400 bytes.
- An HTTP handler cannot reach the hub's channels, so each web client talks to its session through a bridge; `http.ServeWith` cannot report the port it bound, so port 0 for `--web` is resolved first by binding and closing a listener.
- The session's read interrupt costs one task per wait.

## Tests

The cases in `cases/` replay scripts (TCP and web), run loads of up to a thousand clients on one and many carriers, drop a slow consumer, run the cancellation drill, and check usage and file errors; some run under `std.sim` with a seed. All network traffic is on loopback. Run them from the repository root with `go run ./tools/dogfood run -only chat`.

## Section 18 lines
The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```
Io.Scope(self): Scope
Io.ScopeOf[E](self): Scope[E]
Io.ScopeWith(self, l: Limits): Scope
Io.NoCancel(self): Io
Io.Sleep(self, d: time.Duration): Canceled!void
Io.Yield(self): Canceled!void
Scope.Cancel(&self)
Scope.CancelAfter(&self, d: time.Duration)
Scope.Expired(&self): bool
struct Limits {
enum StackSize {
// std.net
net.Listen(io: Io, addr: str): net.Error!Listener
net.Dial(io: Io, addr: str): net.Error!Stream
Listener.Accept(&self, io: Io): net.Error!Stream
Listener.Port(&self): int
Listener.Close(self)
Stream.Read(&self, io: Io, buf: mut []u8): net.Error!int
Stream.Write(&self, io: Io, data: []u8): net.Error!int
Stream.WriteAll(&self, io: Io, data: []u8): net.Error!void
Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
Stream.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Stream.Close(self)
Stream.Shutdown(&self, io: Io, how: Shutdown): net.Error!void
Stream.Conn(self): Conn
Conn.Read(&self, io: Io, buf: mut []u8): net.Error!int
Conn.WriteAll(&self, io: Io, data: []u8): net.Error!void
Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Conn.Shutdown(&self, io: Io, how: Shutdown): net.Error!void
Conn.PeerAddr(&self): ?Addr
Conn.Close(self, io: Io): net.Error!void
Conn.Abandon(self)
net.NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t
net.ReserveConn[T: Transport + Send + Share](h: &Heap): OutOfMemory!ConnSlot[T] from h
ConnSlot[T].Fill(self, t: T): Conn from self, t
ConnSlot[T].Free(self, a: &Alloc)
interface Transport {
// std.http
http.ServeWith[C: Share](sys: &System, addr: str, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void)): http.Error!void
Client.New(h: &Heap, l: ClientLimits): Client from h, l
Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Exchange from a, self
Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Reply from a
Client.Close(self)
Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
Exchange.Close(self, io: Io): http.Error!void
Exchange.Abandon(self)
Exchange.Version(&self): HttpVersion
Response.Flush(mut self, io: Io, link: &Link): http.Error!void
Link.Version(&self): HttpVersion
Link.Takeover(&self): ?Taken
Taken.Abandon(self)
struct ServerOptions {
linear struct Taken {
// std.proc
proc.Pipe(io: Io): proc.Error!(r: PipeReader, w: PipeWriter)
PipeReader.Read(&self, io: Io, buf: mut []u8): proc.Error!int
PipeReader.Close(self)
PipeWriter.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
PipeWriter.Close(self)
// std.crypto
crypto.ParsePrivateKey(der: []u8): crypto.Error!PrivateKey
// std.encoding
encoding.ParsePem(a: &Arena, text: str into a): encoding.Error|OutOfMemory!List[PemBlock] from a, text
// std.x509
x509.ParsePem(a: &Arena, pem: str): x509.Error!List[Certificate] from a
Pool.New(certs: []Certificate): Pool from certs
// std.tls
tls.Connector(c: &Config): net.Secure from c
tls.Acceptor(c: &Config): net.Secure from c
// std.websocket
websocket.Accept(h: &Heap, io: Io, req: &http.Request, t: http.Taken, protocols: []str, const allow: fn(origin: str): bool): websocket.Error!Conn from h, t
websocket.Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): websocket.Error!Conn from h
Conn.Send(&self, io: Io, kind: Kind, data: []u8): websocket.Error!void
Conn.Recv(&self, a: &Arena, io: Io): websocket.Error!Message from a
Conn.Close(self, io: Io, code: u16, reason: str): websocket.Error!void
Conn.Abandon(self)
// std.url
url.Parse(a: &Arena, input: str into a, base: ?&Url): url.Error|OutOfMemory!Url from a, input
url.ParseQuery(a: &Arena, s: str): OutOfMemory!List[(name: str, value: str)] from a
// std.log
log.Stderr(level: Level): Sink
Sink.Info[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Warn[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Error[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
// std.sync
*
// std.atomic
*
```
