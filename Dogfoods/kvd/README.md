# kvd

A key-value server with a write-ahead log, its client, offline tools for the log and the snapshot, and a simulation mode that crashes the server at every log write and checks what it recovers. It shows Mk's tasks and scopes, its sync primitives, file locking and memory mapping, and `std.sim`.

## Running it

From the program's folder:

```
cd Dogfoods/kvd
mk run -- wal dump cases/data/dirty.wal
```

```text
cases/data/dirty.wal: generation 3, not shut down cleanly
      24 SET    alpha one
      43 SET    beta two
      61 APPEND alpha +more
      82 DEL    beta
      97 SET    gamma three
end: clean
```

The other commands:

| Command | Does |
|---|---|
| `kvd serve [--addr IP:PORT] [--data DIR]` | Runs a server until `SHUTDOWN`, Ctrl-C or SIGTERM |
| `kvd client [--addr HOST:PORT] [COMMAND...]` | Sends one command, or one per line of standard input |
| `kvd admin --admin PATH COMMAND...` | Sends one command over the admin channel, a Unix-domain socket |
| `kvd demo [--clients N] [--hold]` | A server and N scripted clients in one process |
| `kvd bench [--clients N] [--requests N]` | Loads an in-process server and reports the replies and latencies |
| `kvd sim [--seeds A..B]` | Crashes, faults and recovers the server under `std.sim`, once per seed |
| `kvd record LOG` / `kvd replay LOG` | Records a client session, then replays it with no server |
| `kvd wal dump\|check\|compact FILE` | Prints, verifies or rewrites a log |
| `kvd snap dump\|get FILE [KEY]` | Prints a snapshot, or looks one key up in it |

The protocol is one line per request: `PING`, `GET k`, `SET k value...`, `DEL k`, `APPEND k value...`, `INCR k`, `GETSET k value...`, `KEYS [prefix]`, `DBSIZE`, `HASH k`, `WAIT k` (the value once `k` has one), `STATS`, `NAME n`, `CLIENTS`, `COMPACT`, `QUIT`, `SHUTDOWN`. With `--admin`, `COMPACT` and `SHUTDOWN` are served only on the admin channel. A reply starts with `+` (status), `-` (error), `:` (integer), `$` (value), `_` (none) or `*n` (n value lines follow).

A data directory holds `kvd.wal` (the log), `kvd.snap` (the snapshot the log's generation starts from) and `kvd.lock` (the instance lock). Exit codes: 0 ok, 1 failure, 2 usage, 3 corrupt log or snapshot, 4 out of memory, 5 simulation check failed.

## Design

- Every change is written to the log as a CRC-checked record and synced by a policy (`--sync always|never|N`) before the store sees it, under the log's mutex and then the store's `RwLock`, so the log's order is the store's.
- Compaction writes the store as a sorted snapshot with an offset index to a new file, renames it into place, and cuts the log back to its header. Recovery maps the snapshot read-only (`fs.Map`, the one `unsafe` module), replays the log after it, and cuts off a torn last record.
- Each connection is a task. Idle clients and slow reads are cut off by scope deadlines, and the server shuts down cleanly on SIGINT, SIGTERM or `SHUTDOWN` with clients still connected. Only the caller's task writes the log, through a `log.Sink`; other tasks report events through a channel.
- The store is a `TextMap[HeapText]` keyed with SipHash from `HashKey.New`, so keys chosen by clients cannot collide on purpose.
- `kvd sim` runs the server and its clients on a `sim.Sim`: each seed picks a log write to crash at, a torn write, a disk or network fault and a client that leaves mid-command, restarts the machine, recovers twice and checks that every recovered state is what the clients were told.

## Limits

- A value holds no line break, and a key no space or control byte; a line is at most `--max-key` + `--max-value` + 64 bytes.
- Recovery reads the log into memory; the log is at most what the compaction threshold lets grow.
- The admin channel serves one connection at a time, and drops one idle for ten seconds unless `--idle-ms` says otherwise, since on Windows a Unix stream may never see its peer leave. `kvd admin` waits at most `--timeout-ms` (5,000) for a reply.
- `--sync never` and `--sync N` trade the durability of the last changes for fewer syncs: a machine crash can lose what was answered but not synced. `kvd sim` syncs every change.
- Under `std.sim` the snapshot is read into an arena (no mapping there), the log is written over rather than truncated, and deadlines do not fire, so `kvd sim` sets none.
- `kvd record` records the client's task only: a recording names no task, so it replays one task's calls.
- If the store runs out of memory after a change is logged, the client gets `-ERR OutOfMemory`, and recovery still applies the change.

## Tests

The cases in `cases/` cover every command, shutdown with clients connected, the admin channel, timeouts, logs that are torn or corrupt, damaged snapshots, `OutOfMemory` under `--max-memory`, and 1,000 simulated crashes. Run them with `go run ./tools/dogfood run -only kvd` from the repository root; `mk test` runs the test blocks.

## Section 18 lines

The standard-library lines this program uses, as `doc/reference.md` lists them; `go run ./tools/dogfood cover` reads this block.

```mk
HeapMap[K, V].New(a: &Alloc): HeapMap[K, V]
HeapMap[K, V].NewKeyed(a: &Alloc, key: HashKey): HeapMap[K, V]
HeapMap[K, V].Get(&self, k: &K): ?&V
HeapMap[K, V].GetMut(mut self, k: &K): ?mut V
HeapMap[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void
HeapMap[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V
HeapMap[K, V].Ensure(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!mut V from self
HeapMap[K, V].Remove(mut self, k: &K): ?V
HeapMap[K, V].Len(&self): int
HeapMap[K, V].Iter(&self): MapIter[K, V]
HeapMap[K, V].Free(self, a: &Alloc)
HeapMap[K, V].FreeEmpty(self, a: &Alloc)
HeapMap[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))
MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map
TextMap[V].New(a: &Alloc): TextMap[V]
TextMap[V].NewKeyed(a: &Alloc, key: HashKey): TextMap[V]
TextMap[V].Get(&self, k: str): ?&V
TextMap[V].GetMut(mut self, k: str): ?mut V
TextMap[V].Set(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!void
TextMap[V].Replace(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!?V
TextMap[V].Ensure(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!mut V from self
TextMap[V].Remove(mut self, a: &Alloc, k: str): ?V
TextMap[V].Len(&self): int
TextMap[V].Iter(&self): TextMapIter[V]
TextMapIter[V].Next(mut self): ?(key: str, value: &V) from self.map
TextMap[V].Free(self, a: &Alloc)
TextMap[V].FreeEmpty(self, a: &Alloc)
TextMap[V].FreeEach(self, a: &Alloc, const release: fn(V))
HeapText.New(a: &Alloc): HeapText
HeapText.Push(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Set(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Clear(mut self)
HeapText.Len(&self): int
HeapText.Str(&self): str
HeapText.Free(self, a: &Alloc)
Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x
Box[T].Ref(&self): &T
Box[T].Mut(mut self): mut T
Box[T].Take(self, a: &Alloc): T
Box[T].Free(self, a: &Alloc)
Replace[T](dst: mut T, x: T into dst): T
Arena.Alloc(&self): &Alloc
interface Free {
HashKey.New(io: Io): Canceled!HashKey
HashKey.Of(k0: u64, k1: u64): HashKey
Scope.CancelAfter(&self, d: time.Duration)
Scope.Expired(&self): bool
// std.fs. File and Error below are fs.File and fs.Error
error Os(code: i64)
error Unsupported
type Error = NotFound | Exists | Denied | IsDirectory | NotDirectory | NoSpace | InvalidPath | Utf8Error | Os | Canceled | Unsupported
fs.OpenWith(io: Io, p: str, o: Options): fs.Error!File
File.Seek(mut self, io: Io, pos: i64, from: Whence): fs.Error!i64
File.Size(&self, io: Io): fs.Error!i64
File.Truncate(mut self, io: Io, size: i64): fs.Error!void
File.Lock(&self, io: Io, mode: LockMode): fs.Error!void
File.TryLock(&self, io: Io, mode: LockMode): fs.Error!bool
File.Unlock(&self, io: Io): fs.Error!void
fs.Map(io: Io, f: &File, offset: i64, len: int): fs.Error!Mapping
Mapping.Bytes(&self): []u8 from self
Mapping.Unmap(self)
File.ReadAt(&self, io: Io, pos: i64, buf: mut []u8): fs.Error!int
struct Options {
enum Whence {
enum LockMode {
fs.Rename(io: Io, src: str, dst: str): fs.Error!void
fs.Remove(io: Io, p: str): fs.Error!void
fs.WriteAll(io: Io, p: str, data: []u8): fs.Error!void
fs.Create(io: Io, p: str): fs.Error!File
File.Write(mut self, io: Io, data: []u8): fs.Error!int
File.WriteAt(&self, io: Io, pos: i64, data: []u8): fs.Error!int
File.WriteAll(mut self, io: Io, data: []u8): fs.Error!void
File.Sync(mut self, io: Io): fs.Error!void
File.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf
File.Close(self, io: Io): fs.Error!void
File.Abandon(self)
fs.MakeDir(io: Io, p: str): fs.Error!void
// std.net. Listener, Stream and Error below are net.Listener, net.Stream and net.Error
error Os(code: i64)
error Unsupported
type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | SecureFailed | StreamReset | MuxClosed | OutOfMemory
Stream.SetNoDelay(&self, on: bool): net.Error!void
Stream.SetKeepAlive(&self, idle: ?time.Duration): net.Error!void
net.ListenWith(io: Io, addr: str, o: ListenOptions): net.Error!Listener
net.ListenUnix(io: Io, path: str): net.Error!Listener
net.DialUnix(io: Io, path: str): net.Error!Stream
struct ListenOptions {
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
net.ParseAddr(s: str): net.Error!Addr
net.Resolve(a: &Arena, io: Io, host: str): net.Error|OutOfMemory!List[Addr] from a
Addr.Format(&self, w: mut Writer): WriteError!void
struct Addr {
// std.proc. Signal below is proc.Signal
proc.WaitSignal(io: Io, s: Signal): Canceled!void
enum Signal {
// std.sync. Shared below is sync.Shared
Shared[T].New(h: &Heap, x: T): OutOfMemory!Shared[T] from h, x
Shared[T].Clone(&self): Shared[T] from self.ptr
Shared[T].Ref(&self): &T
Shared[T].Free(self, h: &Heap)
Mutex[T].New(x: T): Mutex[T]
Mutex[T].Lock(&self, io: Io): Canceled!Guard[T]
Mutex[T].Into(self): T
Guard[T].Ref(&self): &T
Guard[T].Mut(mut self): mut T
Guard[T].Store(mut self, x: T into self)
Guard[T].Unlock(self)
RwLock[T].New(x: T): RwLock[T]
RwLock[T].Lock(&self, io: Io): Canceled!Guard[T]
RwLock[T].LockRead(&self, io: Io): Canceled!ReadGuard[T]
RwLock[T].Into(self): T
ReadGuard[T].Ref(&self): &T
ReadGuard[T].Unlock(self)
Notify.New(): Notify
Notify.NotifyOne(&self)
Notify.NotifyAll(&self)
Notify.Wait(&self, io: Io): Canceled!void
// std.atomic
AtomicInt.New(n: i64): AtomicInt
AtomicInt.Add(&self, n: i64): i64
AtomicInt.Load(&self): i64
AtomicInt.Store(&self, n: i64)
// std.time. Duration and Instant below are time.Duration and time.Instant
time.Seconds(n: i64): Duration
time.Millis(n: i64): Duration
time.Micros(n: i64): Duration
Duration.Nanos(self): i64
time.Now(io: Io): Instant
Instant.Since(self, earlier: Instant): Duration
time.Wall(io: Io): Time
time.FromUnix(seconds: i64, nanos: i64): Time
Time.UnixNanos(self): i64
Time.Format(&self, w: mut Writer): WriteError!void
error TimedOut
// std.hash
hash.Xx(data: []u8): u64
hash.Xx64(data: []u8, seed: u64): u64
hash.Crc32(data: []u8): u32
hash.Sip13(data: []u8, key: HashKey): u64
Crc32State.New(): Crc32State
Crc32State.Bytes(mut self, data: []u8)
Crc32State.Sum(self): u32
Xx64State.New(seed: u64): Xx64State
Xx64State.Bytes(mut self, data: []u8)
Xx64State.Sum(self): u64
// std.log
Logger.New(w: mut Writer, level: Level): Logger from w
Logger.NewTimed(io: Io, w: mut Writer, level: Level): Logger from w
Logger.Log[A: Fields[Format] + Plain](mut self, level: Level, const f: str, args: A): WriteError!void
Logger.Info[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
Logger.Warn[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
Logger.Error[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
log.Stderr(level: Level): Sink
log.Stdout(level: Level): Sink
struct Sink {
Sink.Log[A: Fields[Format] + Plain](&self, io: Io, level: Level, const f: str, args: A): WriteError!void
Sink.Debug[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Info[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Warn[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Error[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
enum Level {
// std.sim
Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim
Sim.Io(&self): Io
Sim.Arena(&self): &Arena from self
Sim.Advance(mut self, d: time.Duration)
Sim.Run(mut self)
Sim.CrashAfterRandomOps(mut self)
Sim.Restart(mut self): Io
Sim.FailNext(mut self, op: Op, f: Fault)
Sim.Free(self)
Recorder.New(inner: Io): OutOfMemory!Recorder
Recorder.Io(&self): Io
Recorder.Log(&self, a: &Arena): OutOfMemory![]u8 from a
Recorder.Free(self)
Replayer.New(log: []u8): OutOfMemory!Replayer
Replayer.Io(&self): Io
Replayer.Diverged(&self): bool
Replayer.Done(&self): bool
Replayer.Free(self)
enum Op {
enum Fault {
```
