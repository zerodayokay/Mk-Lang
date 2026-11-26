# std.sim

`std.sim` runs code written against `Io` in a simulated world. The world has a seeded scheduler, a simulated clock, an in-memory disk and network, one terminal, and the faults and crashes you inject. Every choice comes from one seed, so one seed gives one run. A failing seed replays exactly, as long as the program waits only on the `Sim`'s `Io`. The module also records a real run's `Io` calls and replays them with no OS behind them. Reach for it to test crash safety, timeouts, error paths and task interleavings that a real machine shows rarely.

Read the [library guide](README.md) first. The cookbook has a recipe to [test crash safety under simulation](../cookbook.md#test-crash-safety-under-simulation).

```mk
// verdict: parse
import std.sim
```

## Contents

- [The main pieces](#the-main-pieces)
- [A Sim and its memory](#a-sim-and-its-memory)
- [The network](#the-network)
- [Scheduling](#scheduling)
- [Deadlines and the clock](#deadlines-and-the-clock)
- [Faults](#faults)
- [Crashes and restarts](#crashes-and-restarts)
- [Recording and replay](#recording-and-replay)
- [Other modules under a Sim](#other-modules-under-a-sim)
- [Edge cases](#edge-cases): [waits the Sim cannot see](#waits-the-sim-cannot-see), [waits cut by a deadline](#waits-cut-by-a-deadline), [after the end](#after-the-end)
- [Examples](#examples): [one seed, one interleaving](#one-seed-one-interleaving), [a deadline on the simulated clock](#a-deadline-on-the-simulated-clock), [an echo over the simulated network](#an-echo-over-the-simulated-network), [what survives a power cut](#what-survives-a-power-cut), [record and replay](#record-and-replay)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Make a simulated world from a seed | `Sim.New(a, seed)`, then `Sim.Io()`; end it with `Sim.Free` |
| Memory for the program under test | `Sim.Arena()` |
| Move the clock, deliver what is in flight | `Sim.Advance(d)`, `Sim.Run()` |
| Fail the next operation of a kind | `Sim.FailNext(op, fault)` with `Op` and `Fault` |
| Crash at a seeded point, start the next run | `Sim.CrashAfterRandomOps()`, `Sim.Restart()` |
| Type into the simulated terminal | `Sim.TermInput(data, after)` |
| Record a real run's calls | `Recorder.New(inner)`, `Recorder.Io()`, `Recorder.Log(a)` |
| Replay a recording | `Replayer.New(log)`, `Replayer.Io()`, `Diverged`, `Done` |

The code under test takes an `Io`, so it runs unchanged: pass it `sys.io` in production and `s.Io()` in a test.

## A Sim and its memory

`Sim.New` takes an arena `a`. A new world has no files and no sockets; its clock starts at one second and its wall clock at 2026-01-01T00:00:00Z, on every seed.

- The `Sim` is **linear**: end it with `Free`. It is a view that holds `a`'s loan: its world's arena, which `Sim.Arena()` returns, is a sub-arena of `a`. So `a` is not freed, reset or moved while the `Sim` lives.
- A `Sim` also uses the process heap, for its world, its tables and its files' pages, outside any budget on `a`'s allocator. `New` fails with `OutOfMemory` when the heap is exhausted, or when 64 `Sim`s, `Recorder`s and `Replayer`s are already live.
- As a view it may hold any loan ([memory model, R9](../memory.md#r9-result-sources)). A function that takes a `Sim` beside another reference names its result's source with `from`. A result whose only reference parameter is a `Sim` holds the `Sim`'s loan. An exported type with a private `Sim` field is declared `view`.

## The network

The simulated network is one loopback host, and no real socket is opened.

- A port is bound by a listener or a datagram socket. `Dial` reaches a bound listener at once, and fails with `Refused` when nothing listens on the port.
- Each send is cut at a seeded point into one or two segments. Each segment arrives 20 to 500 µs later, in order. So a stream keeps its order, but a receive may come back short, as on TCP.
- Each datagram gets its own seeded delay, so datagrams may arrive out of order.
- A receive takes what has arrived. With nothing arrived, it moves the clock to the first arrival.
- `Sim.Run()` delivers everything in flight and moves the clock to the last arrival.

The example [an echo over the simulated network](#an-echo-over-the-simulated-network) shows a short receive.

## Scheduling

Tasks spawned under a `Sim`'s `Io` run one at a time, in an order drawn from the seed. Whenever the running task waits, the `Sim` picks from the seed which ready task runs next: a scheduling choice.

- The main task is not scheduled. The tasks it spawns start when main next waits. That wait is its `Join`, or a wait on the `Sim`'s `Io`: a channel or lock wait, a receive, an `Accept`, a `Sleep` or a `Yield`. So one seed gives one interleaving and one output, whether main spawns one root task or several tasks directly.
- A deadlock is reported at once, and the blocked waits answer `Canceled`.
- When a task's wake or send ends main's wait, main runs once that task waits, and then alone until its own next wait.
- The same holds when main's `Join`, or its wait in a full scope's `go`, ends because the tasks it waited for ended. This applies to a scope opened from an `Io` of the run: the `Sim`'s own, a `NoCancel` of it, or a `Recorder`'s over it.
- No deadlock is called before main's next wait, since main may still send what a blocked task waits for. No task of the run runs before then either. So a main that busy-polls after such a `Join`, with no wait in its loop, waits forever.
- A main `Sleep` ends when the simulated clock reaches its deadline, before the tasks then ready run. A main `Yield` is a `Sleep` of one microsecond. No deadlock is called while main sleeps.

## Deadlines and the clock

A deadline fires on the simulated clock, deterministically. Main's dial round trip, its clock reads and a file operation's disk latency move the clock with no scheduling choice. A deadline they pass fires at the next choice, with every other deadline then due, earliest first; a tie goes in arming order. `Sim.Advance(d)` and `Sim.Run()` move the clock from outside the run; when the run has no task, the deadlines they pass fire at once.

## Faults

`Sim.FailNext(op, f)` fails the next operation of kind `op` with the system error named by `f`, and the operation changes nothing. A later `FailNext` of the same kind replaces it.

| `Op` | Covers |
|---|---|
| `Read`, `Write` | A file's or a socket's read or write |
| `Sync` | A file's data sync |
| `Open` | Opening a file or a directory |
| `Accept`, `Connect` | A socket's accept or connect |

A read or write of a pipe, a `Pty`, a child's stream or the simulated terminal is neither a file's nor a socket's, and leaves the fault armed.

Each module reports the fault as its error set's member of that name, else as `Os(code: 0)`. `fs.Error` has `NoSpace` and `Denied`, and `net.Error` has `Reset` and `Refused`, so `FailNext(Op.Accept, Fault.Denied)` reaches `Accept`'s caller as `net.Os(code: 0)`.

This program arms one fault between three saves:

```mk
// verdict: ok
import std.fs
import std.sim

// Save writes a file and reports which step failed.
Save(io: Io, path: str): str {
    var f := fs.Create(io, path) else {
        return "create failed"
    }
    f.WriteAll(io, "data".AsBytes()) else e {
        f.Abandon()
        return match e {
            fs.NoSpace { "write failed: disk full" }
            _ { "write failed" }
        }
    }
    f.Close(io) else {
        f.Abandon() // a failed Close leaves f owned
        return "close failed"
    }
    return "saved"
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var s := try sim.Sim.New(sys.arena, 7)
    onerr s.Free()
    io := s.Io()
    try sys.Print("{r}\n", (r: Save(io, "a.txt")))
    s.FailNext(sim.Op.Write, sim.Fault.NoSpace) // the next write fails
    try sys.Print("{r}\n", (r: Save(io, "b.txt")))
    try sys.Print("{r}\n", (r: Save(io, "c.txt"))) // the fault was used up
    s.Free()
}
```

```text
$ mk run fault.mk
saved
write failed: disk full
saved
```

- One `FailNext` fails one operation. Arm it again before each operation you want to fail.

## Crashes and restarts

- `CrashAfterRandomOps` stops the run at a seeded point, log-uniform over its first 2^23 counted operations. Short runs crash often, and every point of a long run can be reached. From that point, every operation answers `Canceled`.
- `Restart` ends the run as a power cut does and returns the next run's `Io` over the same disk. Every file keeps only what was synced, an unlinked file is gone, every handle and connection of the run is dead, and armed faults and the crash point are cleared. The clock and the random stream go on.

## Recording and replay

A `Recorder`'s `Io` forwards every call to `inner` and logs its inputs' digest and its outputs. `Log` copies the log so far into an arena. A `Replayer`'s `Io` answers the same calls from such a log, copied at `New`, with no OS behind it.

- A call that differs from the log, or comes past its end, answers `Canceled` and sets `Diverged`. The exception is a `NoCancel` `Io`'s cancellation check, which sets `Diverged` and goes on; the same check of a freed `Recorder` or `Replayer` also goes on. `Done` is true once every record was used and none differed.
- Both are linear and keep their state on the process heap, since their `Io` reaches it from any task with no loan. Their `Io` returns `Canceled` once they are freed.
- A `Recorder` that runs out of memory stops recording, and its `Log` then fails with `OutOfMemory`, so a cut log is never mistaken for a whole one.
- The log holds the calls in the order they completed, with no task named. So a recording replays only when one task made its calls, or the tasks' calls never overlapped. A `Replayer` answers whichever task asks first, and a run whose tasks call in another order diverges.
- A handle is logged as its number in the recording, so one seed's recording is the same bytes in every process.
- Tasks spawned under a `Recorder`'s `Io` stay scheduled by the inner `Io` until they end, also after `Free`, so a `Sim` behind it goes on running them.

Deadlines replay too. A deadline armed through a `Recorder`'s `Io` is logged with the instant the scope then holds on `inner`'s clock. Its firing is logged before the first call that completes after it, or at a channel or lock wait. A `Replayer` has no clock. It gives `Remaining` that instant against the replayed clock, and fires the deadline at the same point between calls. So `Expired` and the cancellations replay as recorded.

- Only a read of `Expired` between two calls can see the firing later than the recorded run did.
- A `Cancel` between the firing and the next call diverges the replay.
- Two channel or lock waits with no call between them may replay a firing noted at the second at the first.

A recording logs and replays every basic call of the `Io`. These are file opens, reads, writes, syncs, renames and removals; making and listing directories; sockets, datagrams and name resolution; the clock, sleeps and random bytes. It also logs and replays these further operations:

- the wall clock;
- the file system's: `Stat` and `StatLink`, `SetModified`, `SetPermissions`, `Symlink`, `ReadLink`, `Cwd`, `Absolute`, `Copy`, a file's `Seek`, `Truncate` and data sync, and the directory opens and removals of `Walk` and `RemoveAll`;
- the metadata ones: the time zone, whether a standard stream is a terminal and its size, a socket's options and `Shutdown`, and `ListenWith`'s listen.

The other further operations are `Unsupported` under both, as under an `Io` with no OS:

- a mapping;
- a pipe, a child, a `Pty` or the terminal;
- a Unix-domain socket;
- the platform's certificate verdict;
- a file `Lock`;
- a socket's addresses.

## Other modules under a Sim

Each module's page says what it does under a `Sim`: [std.fs](fs.md), [std.net](net.md), [std.tls](tls.md), [std.time](time.md#under-a-simulated-io), [std.proc](proc.md#under-a-simulated-io), [std.term](term.md#under-a-simulated-io), [std.atomic](atomic.md), [std.runtime](runtime.md), [sqlite](sqlite.md).

## Edge cases

Most programs never meet these rules. They matter to a test that mixes native and simulated waits, cancels near a deadline, or keeps an `Io` after its run ends.

### Waits the Sim cannot see

A `Join` of a native scope, or of another `Sim`'s scope, counts as main's wait, as a native `Sleep` of main does.

- A deadlock may then be called while main waits there, at a point that depends on real time. Such a program need not keep one outcome per seed. Neither need a program whose native tasks use the `Sim`'s `Io`.
- A task of the run that waits in such a `Join` is away from the run, as in a native wait. Meanwhile the run goes on and its clock moves to the next sleeper or deadline. A deadline of the run may fire and cancel the waited tasks, native ones included.
- A scope opened through eight or more `Recorder`s stacked over the `Sim` counts as the run's own.

### Waits cut by a deadline

When a deadline fires at a scheduling choice, a wait made ready before that choice is weighed against the deadline:

- A task's `Accept`, or receive on a stream, weighs what it would take against the instant of the deadline that cancelled its scope chain. A connection queued, or a first byte that arrived, after that instant is not taken, and the wait answers `Canceled`, as it would natively. One that came at or before it is taken, and a receive reads only the bytes that had arrived by that instant.
- A wait whose scope was cancelled before that choice's first firing (a `Cancel`, a deadline of zero) is not weighed, and takes what is there.
- The same holds for a `Cancel` made after the clock passed the task's own deadline but before a choice fired it. The wait takes what is there, even a connection queued after that deadline. With nothing there, the scope does not read `Expired`, where natively the deadline came first.
- Any other wait made ready that way still takes what it waited for, even when it came after the deadline. Such waits are:
  - a stream's end or reset;
  - a datagram;
  - a sleep's end;
  - a read of a child, a pipe or the terminal.

### After the end

A `Sim`'s `Io` holds no loan, like any `Io`, so a copy may outlive its run. After `Free`, `Restart` or a crash, its operations return `Canceled`, its waits too.

- The exception is the cancellation check of a `NoCancel` `Io`, a sleep of 0 such as a free `Lock` makes. It goes on, unless a scope cancelled with `Cancel()` and opened from that `Io` covers it. A dead run is not a scope cancellation.
- A deadline on the `Io` of a restarted or freed run arms nothing, so a `CancelAfter` boundary there never fires. A crashed run keeps its clock, and its deadline cancels as `Cancel()` does.
- Handles the program still owns stay valid after `Free`, so their `Close` or `Abandon` still frees them.

## Examples

Each program below runs as written with `mk run`.

### One seed, one interleaving

```mk
// verdict: ok
import std.sim

Worker(io: Io, ch: &Chan[int], id: int) {
    for _ in 0..3 {
        ch.Send(io, id) else {
            return
        }
        // give the scheduler a choice of the next task
        io.Yield() else {
            return
        }
    }
}

// Order runs three workers under a Sim and returns the order in which
// their messages arrived.
Order(a: &Arena, seed: u64): OutOfMemory!Text from a {
    var s := try sim.Sim.New(a, seed)
    onerr s.Free()
    io := s.Io()
    h := s.Arena().Alloc()
    ch := try Chan[int].New(h, 9)
    scope sc := io.Scope() {
        sc.go Worker(io, &ch, 0)
        sc.go Worker(io, &ch, 1)
        sc.go Worker(io, &ch, 2)
    }
    names := ["a", "b", "c"]
    var order := Text.New(a)
    for _ in 0..9 {
        id := ch.Recv(io) else {
            break
        }
        try order.Push(names[id])
    }
    ch.Free(h)
    s.Free()
    return order
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    for seed in [1, 2, 1] {
        order := try Order(sys.arena, Cast[u64](seed))
        try sys.Print("seed {seed}: {order}\n", (seed, order: order.Str()))
    }
}
```

```text
$ mk run order.mk
seed 1: abcabcabc
seed 2: acbacbacb
seed 1: abcabcabc
```

- The workers start when main waits in the scope's end, and the seed picks which runs at each `Yield`.
- Seed 1 gives the same order both times. Loop over many seeds to try many interleavings.

### A deadline on the simulated clock

```mk
// verdict: ok
import std.sim
import std.time

export Main(sys: &System): OutOfMemory|WriteError!void {
    var s := try sim.Sim.New(sys.arena, 42)
    onerr s.Free()
    io := s.Io()
    start := time.Now(io)
    var expired := false
    scope sc := io.Scope() {
        sc.CancelAfter(time.Millis(10))
        io.Sleep(time.Seconds(60)) else {} // cut short by the deadline
        expired = sc.Expired()
    }
    waited := time.Now(io).Since(start)
    try sys.Print("expired: {expired}, waited {waited} of simulated time\n", (expired, waited))
    try sys.Print("wall clock: {t}\n", (t: time.Wall(io)))
    s.Free()
}
```

```text
$ mk run deadline.mk
expired: true, waited 10.001ms of simulated time
wall clock: 2026-01-01T00:00:00.010003Z
```

- The program returns at once: a minute of simulated sleep costs no real time.
- Each clock read by main moves the simulated clock a little, so the wait shows 10.001 ms and the wall clock a few microseconds past 10 ms.

### An echo over the simulated network

```mk
// verdict: ok
import std.net
import std.sim
import std.time

// Echo answers one connection with the bytes it receives, until the
// client shuts its side down.
Echo(l: &net.Listener, io: Io): net.Error!void {
    conn := try l.Accept(io)
    defer conn.Close()
    var buf: [64]u8 = [0; 64]
    for {
        n := try conn.Read(io, mut buf[..])
        if n == 0 {
            return
        }
        try conn.WriteAll(io, buf[..n])
    }
}

export Main(sys: &System): net.Error|Utf8Error|OutOfMemory|WriteError!void {
    var s := try sim.Sim.New(sys.arena, 1)
    defer s.Free()
    io := s.Io()
    start := time.Now(io)
    l := try net.Listen(io, "127.0.0.1:7000")
    defer l.Close()
    msg := "hello, simulated network"
    var reply: [64]u8 = [0; 64]
    var n := 0
    try scope sc := io.ScopeOf[net.Error]() {
        sc.go Echo(&l, io)
        c := try net.Dial(io, "127.0.0.1:7000")
        defer c.Close()
        try c.WriteAll(io, msg.AsBytes())
        try c.Shutdown(io, net.Shutdown.Write)
        for {
            got := try c.Read(io, mut reply[n..])
            if got == 0 {
                break // the echo is whole
            }
            try sys.Print("read {got} bytes\n", (got))
            n += got
        }
    }
    try sys.Print("{r}\n", (r: try str.From(reply[..n])))
    try sys.Print("took {d} of simulated time\n", (d: time.Now(io).Since(start)))
}
```

```text
$ mk run echo.mk
read 4 bytes
read 20 bytes
hello, simulated network
took 1.214342ms of simulated time
```

- The seed cut the send into two segments, so the first `Read` came back short. The loop reads until `Read` returns 0, as it must on a real network too.
- `Echo` takes an `Io`, so it runs unchanged over `sys.io`. Under the `Sim` no port is opened, and the round trip costs no real time.

### What survives a power cut

```mk
// verdict: ok
import std.fs
import std.sim

// Save writes text to path, and syncs it when sync is true.
Save(io: Io, path: str, text: str, sync: bool): fs.Error!void {
    var f := try fs.Create(io, path)
    onerr f.Abandon()
    try f.WriteAll(io, text.AsBytes())
    if sync {
        try f.Sync(io)
    }
    try f.Close(io)
}

// Show prints what the run behind io finds at path.
Show(sys: &System, io: Io, path: str): OutOfMemory|WriteError!void {
    text := fs.ReadText(sys.arena, io, path) else {
        try sys.Print("{path}: missing\n", (path))
        return
    }
    try sys.Print("{path}: {n} bytes\n", (path, n: text.Len()))
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    var s := try sim.Sim.New(sys.arena, 1)
    onerr s.Free()
    try Save(s.Io(), "synced.txt", "kept\n", true)
    try Save(s.Io(), "unsynced.txt", "lost\n", false)
    io := s.Restart() // a power cut: only synced data survives
    try Show(sys, io, "synced.txt")
    try Show(sys, io, "unsynced.txt")
    s.Free()
}
```

```text
$ mk run powercut.mk
synced.txt: 5 bytes
unsynced.txt: 0 bytes
```

- The unsynced file's name survived, but its bytes did not.
- Add `s.CrashAfterRandomOps()` before the writes, and loop over seeds, to crash at many points of the run; the [cookbook recipe](../cookbook.md#test-crash-safety-under-simulation) shows the loop.

### Record and replay

```mk
// verdict: ok
import std.fs
import std.sim

// Work is the code under test: it draws a random byte and reads a file.
Work(a: &Arena, io: Io): fs.Error|OutOfMemory!int {
    var b: [1]u8 = [0]
    try io.Random(mut b[..])
    text := try fs.ReadText(a, io, "input.txt")
    return text.Len() * 1000 + int(b[0])
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    try fs.WriteAll(sys.io, "input.txt", "hello\n".AsBytes())
    var r := try sim.Recorder.New(sys.io) // record a real run
    defer r.Free()
    n := try Work(sys.arena, r.Io())
    log := try r.Log(sys.arena)        // a copy of the log, in the arena
    try fs.Remove(sys.io, "input.txt") // the replay needs no file

    var p := try sim.Replayer.New(log)
    defer p.Free()
    m := try Work(sys.arena, p.Io()) // the same answers, from the log
    try sys.Print("same result: {same}, done: {done}, diverged: {div}\n", (same: n == m, done: p.Done(), div: p.Diverged()))
}
```

```text
$ mk run replay.mk
same result: true, done: true, diverged: false
```

- The replay gets the recorded random byte and file contents, though the file is gone.
- Save the log to a file to replay a failure seen once in production, in a test, as often as needed.

## API

<!-- api:begin -->
Stability: Provisional.

### `DefaultNetFaults`

```
export const DefaultNetFaults: NetFaults
```

The default fault profile every Sim starts with: no loss, no duplicates,
20 us to 500 us latency, unlimited rate and unbounded stream buffers
(stream_buffer 0), matching historical Sim replay bit for bit.

### `Fault`

```
export enum Fault
```

The faults FailNext injects, each the system error of the same name.
A module reports the fault as its error set's member of that name, else
as Os(code: 0): fs.Error has NoSpace and Denied, and net.Error has
Reset and Refused, so FailNext(Op.Accept, Fault.Denied) reaches
Accept's caller as net.Os(code: 0).

Variants:

- `NoSpace`: The device is full.
- `Denied`: Permission denied.
- `Reset`: The peer reset the connection.
- `Refused`: The peer refused the connection.

### `NetFaults`

```
export struct NetFaults {
    export drop: u32
    export duplicate: u32
    export latency_lo: time.Duration
    export latency_hi: time.Duration
    export rate: i64
    export stream_buffer: int
}
```

What the simulated network does: datagrams are dropped and duplicated at
parts per million (`0..1_000_000`), each drawn from the seed; latency is
uniform in `latency_lo..latency_hi` (`DefaultNetFaults` is 20 us to
500 us); `rate` is bytes per second through each receiving UDP port (0
for unlimited); `stream_buffer` bounds each stream end's unread bytes (0
for unbounded), so a send waits while the peer does not read. Start
from `DefaultNetFaults`.

### `Op`

```
export enum Op
```

The kinds of operation FailNext can fail. Read and Write cover both
files and sockets. Sync is a file's data sync, Open the open of a file
or a directory, and Accept and Connect are a socket's.

Variants:

- `Read`: A read of a file or a socket.
- `Write`: A write to a file or a socket.
- `Sync`: A file's data sync.
- `Open`: The open of a file or a directory.
- `Accept`: A socket's accept.
- `Connect`: A socket's connect.

### `Recorder`

```
export linear struct Recorder
```

An Io over another Io that forwards every call and logs its inputs'
digest and its outputs, so a Replayer can answer the same calls later.
It is linear: end it with Free.

### `Recorder.Free`

```
export Recorder.Free(self)
```

Ends the recording and frees the log. The Io answers Canceled from then
on, except a NoCancel copy's cancellation check, which goes on (see
Recorder.Io). Tasks spawned under the Io stay scheduled by the inner Io
until they end, so a Sim behind it goes on running them; the recorder
counts against the 64 live ones until the last of them ends. O(log's
blocks); allocates nothing.

### `Recorder.Io`

```
export Recorder.Io(&self): Io
```

The recording Io. Every call through it is forwarded to the inner Io
and logged with the inner Io's answer as it is, a NoCancel copy's
cancellation check (a sleep of 0) too. It holds no loan. After Free it
answers Canceled, except a NoCancel copy's check, which goes on unless
a scope cancelled with Cancel() and opened from that copy covers it.
O(1); allocates nothing.

### `Recorder.Log`

```
export Recorder.Log(&self, a: &Arena): OutOfMemory![]u8 from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A copy of the log so far, in a. It is a copy, not a view, since a
call through the Io, which holds no loan, may grow the log at any time.
Fails with OutOfMemory when a cannot hold it, and also when the
recording itself ran out of memory and stopped, so a cut log is never
taken for a whole one. O(n) in the log's length; allocates n bytes in
a.

### `Recorder.New`

```
export Recorder.New(inner: Io): OutOfMemory!Recorder
```

- Errors: OutOfMemory

A Recorder whose Io forwards every call to inner and logs it. Fails
with OutOfMemory when the process heap cannot hold the recorder, or 64
Sims, Recorders and Replayers are live. O(1); allocates the recorder on
the process heap until Free, and the log grows there with every call.
It keeps its state there, not in an arena, since its Io reaches it from
any task with no loan.

The log holds the calls in the order they completed, with no task
named. So a recording replays only when one task made its calls, or
the tasks' calls never overlapped: a Replayer hands the next record to
whichever task asks first. A handle is logged as its number in the
recording, so one seed's recording is the same bytes in every process.
A recorder that runs out of memory stops recording, and Log then
fails.

### `Replayer`

```
export linear struct Replayer
```

An Io that answers every call from a Recorder's log, with no OS behind
it. It is linear: end it with Free.

### `Replayer.Diverged`

```
export Replayer.Diverged(&self): bool
```

Whether some call differed from the log, or came after its end. O(1),
under the replayer's lock; allocates nothing.

### `Replayer.Done`

```
export Replayer.Done(&self): bool
```

Whether every record of the log was used, and none differed. O(1),
under the replayer's lock; allocates nothing.

### `Replayer.Free`

```
export Replayer.Free(self)
```

Frees the replayer and its copy of the log. The Io answers Canceled
from then on, except a NoCancel copy's cancellation check, which goes
on (see Replayer.Io). O(log's blocks); allocates nothing.

### `Replayer.Io`

```
export Replayer.Io(&self): Io
```

The replaying Io: every call through it is answered from the log. A
call the next record does not match, or one past the log's end,
diverges the replay and answers Canceled. A NoCancel copy's
cancellation check (a sleep of 0) diverges it and goes on, as after
Free, since a replay that cannot go on is not a scope cancellation; a
scope cancelled with Cancel() and opened from that copy still cancels
it. It holds no loan; after Free it answers Canceled. O(1); allocates
nothing.

### `Replayer.New`

```
export Replayer.New(log: []u8): OutOfMemory!Replayer
```

- Errors: OutOfMemory

A Replayer whose Io answers every call from log, which it copies.
Fails with OutOfMemory as Recorder.New does. O(n) in the log's length;
allocates the copy of log on the process heap until Free.

A replay is exact for one task's calls. The log names no task, so when
several tasks call at once the next record goes to whichever asks
first, and a call that is not the record's diverges.

### `Sim`

```
export linear mut view struct Sim
```

A simulated world behind an Io: a seeded scheduler, a simulated clock,
an in-memory disk and network, and one terminal. Every choice is drawn
from one seed, so one seed gives one run. A Sim is linear: end it with
Free. It is a view that borrows the arena it was made from, and it also
uses the process heap.

### `Sim.Advance`

```
export Sim.Advance(mut self, d: time.Duration)
```

Moves the simulated clock forward by d; a d of zero or less changes
nothing. When the run has no task, the scope deadlines the clock
passes fire at once; otherwise they fire at the next scheduling choice.
O(armed deadlines); allocates nothing.

### `Sim.Arena`

```
export Sim.Arena(&self): &Arena from self
```

- Flows: The result borrows from self.

The world's own arena, for the program under test; it lives until
Free. O(1); allocates nothing.

### `Sim.Blackhole`

```
export Sim.Blackhole(mut self, port: int, on: bool)
```

Drops all packets sent to or from `port` while `on` is true; calling
again with false restores delivery for new sends. In-flight packets
scheduled before Blackhole are untouched. O(1); allocates from the
process heap when enabling.

### `Sim.CrashAfterRandomOps`

```
export Sim.CrashAfterRandomOps(mut self)
```

Arms a crash at a seeded point: after k more counted operations, the
next one and every later one answer Canceled. k is drawn log-uniform
over the first 2^23, so short runs crash often and every point of a
long run can be reached. Restart then starts the next run with what was
synced. O(1); allocates nothing.

### `Sim.FailNext`

```
export Sim.FailNext(mut self, op: Op, f: Fault)
```

Makes the next operation of kind op fail with f, changing nothing; a
later FailNext of the same kind replaces it. Only a file's or a
socket's operation takes the fault: a read or write of the simulated
terminal, a pipe, a Pty or a child's stream leaves it armed. O(1);
allocates nothing.

### `Sim.Free`

```
export Sim.Free(self)
```

Frees the world and its arena. Every Io of this Sim answers Canceled
from then on (see Sim.Io). Handles the program still owns stay valid,
so their Close or Abandon still frees them. O(n) in the world's size;
allocates nothing.

### `Sim.Io`

```
export Sim.Io(&self): Io
```

The Io of the current run, to pass to the code under test. Like any Io
it holds no loan. Every copy answers Canceled once the run crashes,
ends with Restart, or the Sim is freed. A dead run is not a scope
cancellation, so the cancellation check of a NoCancel copy (a sleep of
0, as a free Lock or a Send with room makes) goes on; a scope cancelled
with Cancel() and opened from that copy still cancels it. O(1);
allocates nothing.

### `Sim.New`

```
export Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A simulated world whose every choice is drawn from seed, with no files
and no sockets. On every seed its clock starts at one second and its
wall clock at 2026-01-01T00:00:00Z. Fails with OutOfMemory when the
heap is exhausted or 64 Sims, Recorders and Replayers are already live.
O(1). It allocates the world, its tables and its files' pages on the
process heap, outside any budget on a's allocator; the world's arena is
a sub-arena of a. So the Sim borrows a: a is not reset or freed, and
its allocator not released, while the Sim lives.

### `Sim.Rebind`

```
export Sim.Rebind(mut self, port: int)
```

Reassigns the UDP socket bound at `port` to a fresh ephemeral port,
clearing any packet queues at the old port while keeping connected-peer
filtering and forwarding future replies to the new port. Does nothing if
`port` is not a bound UDP socket. O(n) in open sockets; allocates
nothing.

### `Sim.Restart`

```
export Sim.Restart(mut self): Io
```

Ends the run as a power cut does, and returns the next run's Io over
the same disk. Every file keeps only what was synced, an unlinked file
is gone, every handle and connection of the run is dead, and armed
faults and the crash point are cleared. The clock and the random stream
go on. Every Io of the earlier run answers Canceled, as after Free (see
Sim.Io). O(n) in the files, their unsynced pages and the connections;
allocates nothing, and frees what the run leaves.

### `Sim.Run`

```
export Sim.Run(mut self)
```

Delivers every message in flight and moves the clock to the last
arrival. When the run has no task, the scope deadlines the clock passes
fire at once; otherwise they fire at the next scheduling choice. O(n)
in the connections, datagrams and armed deadlines; allocates nothing.

### `Sim.SetNetFaults`

```
export Sim.SetNetFaults(mut self, f: NetFaults)
```

Replaces the active network fault profile for the current Sim (takes
effect on subsequent sends and stream writes). O(1); allocates nothing.

### `Sim.TermInput`

```
export Sim.TermInput(mut self, data: []u8, after: time.Duration): OutOfMemory!void
```

- Errors: OutOfMemory

Queues data as input of the Sim's one terminal, the one term.Open
opens under the Sim's Io. The bytes arrive together once after has
passed on the simulated clock (at once for a zero or negative after),
never before input queued earlier. Term.Read takes them in the target's
own form: the bytes on Linux; on Windows one key-down KEY_EVENT record
per UTF-16 unit of their text, each call's bytes decoded on their own
(an ill-formed or cut-short sequence is one U+FFFD per maximal
subpart). Input still queued when Restart ends the run is lost. Fails
with OutOfMemory when the heap is exhausted, with nothing queued. It
draws nothing from the random stream. O(len(data)); allocates the
queue's growth from the process heap.

<!-- api:end -->
