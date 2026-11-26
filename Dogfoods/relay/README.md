# Relay

An offline reverse proxy and load generator. Pick HTTP/1.1, cleartext HTTP/2,
or TLS HTTP/3 independently on each side. The program opens two loopback
listeners and forwards uploads to an in-process echo origin. The load client
checks the actual HTTP version of each hop, the echoed bytes and the status.

```sh
cd Dogfoods/relay
mk run -- load h1 h2 16 32768 0
mk run -- load h2 h3 1 1024 0 ../fetch/cases/data/tls
mk run -- load h2 h2 8 8192 50
mk run -- slow h2 h1 1 32768 0
mk run -- goaway h1 h2 2 128 0
mk run -- sim-loss h3 h1 1 2048 0 ../fetch/cases/data/tls
```

After the mode and protocol pair, give the number of parallel requests
(1–16), the upload bytes per request (0–65,536) and the percentage to
cancel (0–100, rounded down to a whole request). For HTTP/3, give a directory
containing `root.pem`, `server.pem` and `server.key`. The examples use
the public loopback test identity from [fetch](../fetch).

To run separate processes, start the origin in one terminal and the
relay in another. Both bind to IPv4 loopback and stop on Ctrl+C.

```sh
mk run -- origin h2 8091
mk run -- serve h1 h2 8090 8091
curl http://127.0.0.1:8090/echo
```

The serve command can also use another loopback origin on its specified
port. The built-in standalone origin accepts GET and echoes the body of
POST requests to `/echo`.

The cancellation case uses HTTP/2 on both sides. It abandons selected
responses after their heads arrive, waits for the origin to observe the
failed writes and checks that every relay handler has returned. The
`goaway` mode serves one upload, stops and joins the HTTP/2 origin, and
starts another origin on the same listener. It checks that the relay's
existing client can reach the new round.
The `sim-loss` mode runs on a seeded `std.sim` network with 10% datagram
loss and requires an HTTP/3 hop.

The relay filters connection-specific fields on both hops and uses its
configured origin for authority. It forwards parsed path and query bytes
when the URL parser preserves them. The server API buffers uploads. On
downloads, the relay reads and flushes one block at a time using two
4 KiB buffers per handler. The response API copies each block into the
handler arena, so the relay caps that retained response data at 64 KiB.
A shared gate limits active forwards to 16 across all connections.
The slow reader consumes 257 bytes at a time with an 8 KiB HTTP/2 receive
window. Its output reports the configured limits and, for a single request,
the peak total of admitted request bodies and retained response copies.

Run the offline cases from the repository root with
`go run ./tools/dogfood run -only relay`. The cases compare transfer
counts and byte totals. A run fails if any expected exchange, origin
validation, or cancellation is missing.

## Section 18 lines

```text
System.Print[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
System.PrintErr[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
Arena.New(h: &Heap): OutOfMemory!Arena
Arena.Free(self) ends self
Reader.Read(mut self, buf: mut []u8): ReadError!int
Io.Scope(self): Scope
Io.Sleep(self, d: time.Duration): Canceled!void
Scope.Cancel(&self)
Scope.CancelAfter(&self, d: time.Duration)

// std.atomic
AtomicInt.New(n: i64): AtomicInt
AtomicInt.Add(&self, n: i64): i64
AtomicInt.Load(&self): i64
AtomicInt.Store(&self, n: i64)

// std.fs
fs.ReadText(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a

// std.net
net.Listen(io: Io, addr: str): net.Error!Listener
Listener.Port(&self): int
Listener.Close(self)

// std.proc
proc.WaitSignal(io: Io, s: Signal): Canceled!void

// std.sync
Mutex[T].New(x: T): Mutex[T]
Mutex[T].Lock(&self, io: Io): Canceled!Guard[T]
Semaphore.New(permits: int): Semaphore
Semaphore.Acquire(&self, io: Io, n: int): Canceled!void
Semaphore.Release(&self, n: int)

// std.http
Client.New(h: &Heap, l: ClientLimits): Client from h, l
Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Exchange from a, self
Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Reply from a
Client.Drive(&self, io: Io): Canceled!void
Client.Stop(&self)
Client.Close(self)
Exchange.Version(&self): HttpVersion
Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
Exchange.Close(self, io: Io): http.Error!void
Exchange.Abandon(self)
http.ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void): http.Error!void
Response.Status(mut self, code: int)
Response.Header(mut self, name: str, value: str): OutOfMemory!void
Response.Body(mut self, data: []u8): OutOfMemory!void
Response.Flush(mut self, io: Io, link: &Link): http.Error!void
Link.Version(&self): HttpVersion

// std.quic
quic.Connector(c: &Config): net.MuxDialer from c
quic.Acceptor(c: &Config): net.MuxServer from c

// std.crypto
crypto.ParsePrivateKey(der: []u8): crypto.Error!PrivateKey

// std.encoding
encoding.ParsePem(a: &Arena, text: str into a): encoding.Error|OutOfMemory!List[PemBlock] from a, text

// std.sim
Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim from a
Sim.Io(&self): Io
Sim.SetNetFaults(mut self, f: NetFaults)
Sim.Free(self)

// std.url
url.Parse(a: &Arena, input: str into a, base: ?&Url): url.Error|OutOfMemory!Url from a, input
Url.Path(&self): str from self
Url.Query(&self): str from self

// std.x509
x509.ParsePem(a: &Arena, pem: str): x509.Error!List[Certificate] from a
Pool.New(certs: []Certificate): Pool from certs

// std.tls
tls.Acceptor(c: &Config): net.Secure from c
```
