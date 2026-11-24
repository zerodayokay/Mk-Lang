# std.runtime

`std.runtime` describes the running program and gives a few controls over the runtime. It tells you how many processors and carrier threads there are and how much stack is left. It runs a blocking C call without holding a carrier, gives wakes that any thread may send, and starts OS threads of their own. It also writes crash dumps and holds the fault filters and the panic hook. Most programs never import it. Reach for it when you embed an interpreter or a JIT, call C libraries that block or need one thread, or handle crashes.

Read the [library guide](README.md) first. Tasks and scopes are in [the reference](../reference.md#12-concurrency).

```mk
// verdict: parse
import std.runtime
```

## Contents

- [The main pieces](#the-main-pieces)
- [Carriers, tasks and stacks](#carriers-tasks-and-stacks)
- [Blocking foreign calls](#blocking-foreign-calls)
- [Wakers](#wakers)
- [Threads of their own](#threads-of-their-own)
- [Calls from C](#calls-from-c)
- [Fault filters](#fault-filters)
- [Panics](#panics)
- [A foreign Io](#a-foreign-io)
- [Examples](#examples): [runtime facts](#runtime-facts), [run a blocking call](#run-a-blocking-call), [a thread of its own](#a-thread-of-its-own), [resume from a fault](#resume-from-a-fault), [write a crash dump](#write-a-crash-dump)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Count processors and carrier threads | `Processors`, `Carriers` |
| Check the stack before recursing deeper | `StackBounds`, `StackRemaining` |
| Read the CPU's cycle counter | `Cycles` |
| Hint branch prediction and cold paths | `Likely`, `Unlikely`, `Cold` |
| Run a C call that blocks its thread | `Blocking` |
| Wake a task from any thread | `Waker`: `New`, `Wait`, `Wake` |
| Run code on one OS thread | `Dedicated`, or `Thread` and `Joined.Join` |
| Get the current task's `Io` from C-called code | `TaskIo` |
| See hardware faults first | `AddFaultFilter`, `FaultContext`, `FaultAction`, `FaultKind` (unsafe) |
| Write a crash dump | `WriteDump` |
| Run code at a panic | `export extern MkPanicHook(info: &runtime.PanicInfo)` |
| Make an `Io` over your own operations | `ForeignIo`, `IoTable`, `InertTable` (unsafe) |

Everything here is safe except `AddFaultFilter`, `ForeignIo` and `ForeignContext`, which are callable only in `unsafe` (E1101).

## Carriers, tasks and stacks

Mk code runs as tasks on a pool of carrier threads. `Carriers` is the number of carriers: the `MK_CARRIERS` environment variable when set, a library's start option, or else `Processors`, the processor count the OS reports for this process.

`MK_CARRIERS` is from 1 to 256, and a larger value counts as 256. Unset or below 1, there is one carrier per logical processor. A carrier runs one task until that task waits, and never preempts it. So with one carrier, a task that spins waiting for another never lets it run.

Tasks move between carriers, so there is no thread-local storage: the runtime finds the current task from the stack pointer. Pass a program's context to the code that needs it, as `sys` is passed.

`StackBounds` is the running task's usable stack, with its guard page left out, or a carrier's system stack when called on one. `StackRemaining` is the distance from the stack pointer to the low bound. It costs a handful of instructions, so an interpreter can check it on every call and fail with its own error well before the guard page. `Cycles` reads the CPU's cycle counter, which never decreases on one thread; it cannot run at compile time.

## Blocking foreign calls

A carrier that sits in a blocking C call cannot run other tasks. `Blocking(io, state, f)` runs `f(state)` on the runtime's blocking pool instead, off the carriers, and returns its result.

- A cancel that reaches the job while it waits in the pool's queue takes it out unrun. `Blocking` then fails with `Canceled` and the caller still owns `state`, as a failed `Chan.Send` leaves the value with the sender.
- Once `f` has started, it runs to its end and its result is returned, cancelled or not. A blocking foreign call cannot be stopped from outside, so this is the one wait in Mk that a deadline cannot cut.
- Off a task, or with an `Io` that is not the runtime's own (a `Sim`'s), `f` runs on the calling thread.
- The pool is shared with std's own blocking work, such as file system calls: 64 threads, plus threads for long waits, at most 1,024 in all.

## Wakers

A `Waker` is a wake that one task waits for and any thread may give, a foreign thread included. It is one word, so it can live in a `var` global or in memory a C library keeps a pointer to. A C callback calls `Wake`, and a task blocked in `Wait` resumes.

- A `Wake` before the `Wait` is kept, and the next `Wait` returns at once.
- `Wait` may return early, as a wait on an atomic word does, so check the condition you wait for and wait again.
- `Wait` fails with `Canceled` when its scope is cancelled. Off a task, `Wait` is panic 16.

## Threads of their own

Some C libraries must always be called from the thread that set them up.

- `Dedicated(io)` returns an `Io` whose tasks, and their tasks, run on one carrier thread of their own. That carrier never takes work from other carriers and is never robbed of its own, and it lives until the process exits. A `NoCancel` copy of a dedicated `Io` is dedicated too.
- `Thread(io, f)` starts a new carrier and runs `f` on it with a full `Io` pinned to it, outside `io`'s scopes: cancelling the caller's scope does not reach `f`. The result, `Joined`, is linear: `Joined.Join(io)` waits for the carrier to end. `Join` can be cancelled, as any wait on an `Io` can, so it never blocks a carrier past a deadline; on `Canceled` the `Joined` is still yours, to join again.
- With a `Sim`'s or another non-native `Io`, `Dedicated` returns the `Io` as it is, and `Thread` runs `f` on the calling task before it returns.
- Both panic with code 8 when all 256 carrier records are in use or the OS refuses a thread. So does a spawn through the `Io` of a `Thread` that has ended.

## Calls from C

C may call an `export extern` function off a task: from a foreign thread, or from a carrier thread outside a task. The function then runs as a new task of its runtime while the calling thread waits. So it may wait, sleep and do I/O, in an executable as in a library; `TaskIo` gives it the task's `Io`. Called on a carrier's system stack, inside a task's own `extern` call, it is a direct call, and a suspension in it is panic 16.

A library ([packages](../packages.md#15-libraries)) has its own runtime, and only C ABI values cross its boundary.

- The runtime starts on the library's first call, or earlier through `<name>_mk_start(options)`.
- It stops through `<name>_mk_stop()`. A call made during the stop waits for its end. After the stop a call is panic 16 ([packages](../packages.md#15-libraries)).
- The library's heap and stack regions stay reserved until the process ends.

## Fault filters

`AddFaultFilter(f)` lets a JIT, a garbage collector or a sandbox see a hardware fault before Mk reports it, and sometimes resume.

- Filters run in the order added, before Mk's own report. Each gets a `FaultContext`: the `FaultKind`, the address, `Pc`, `Sp` and the general registers. It returns `FaultAction.Pass`, or `FaultAction.Resume` with a context it edited, which the handler installs.
- A filter runs inside the fault. It must not take a lock, allocate or suspend. On Windows a filter runs on the faulting stack: during a task's stack overflow it has 8 KiB, out of the 16 KiB the runtime keeps for the report. On Linux and macOS filters run on the carrier's 64 KiB alternate signal stack. A fault inside a filter ends the process with `panic: fault in a fault filter`, status 101.
- There can be at most 16 filters, and they stay for the life of the process; one more panics with code 8. `Reg(n)` numbers the registers as the target encodes them, `RegCount()` of them: x86_64's 16 (0 rax, 1 rcx, ..., 15 r15) or aarch64's x0 to x30 and sp (31).

Which faults reach the filters depends on the target:

| Target | Faults the filters see |
|---|---|
| Windows | A stack overflow (in a library, one in its own code or on a stack Mk made); an access or illegal-instruction fault in Mk code; any fault whose `pc` is in a code region. Never a fault that C code handles itself (`__try`). |
| Linux, macOS | A `SIGSEGV`, `SIGBUS` or `SIGILL` that Mk owns: its `pc` in the executable's image, on the main thread or a thread std started, or on a task's or a carrier's stack; any fault in a code region. A fault in C code outside the image on a foreign thread, and a signal another process or thread sends, get the signal's default action, raised again at once. |

- `FaultKind.Stack` is an access to a guard page the runtime knows, a task's or a carrier's, and is reported as a stack overflow wherever its `pc` is.
- A fault in a [`std.vm`](vm.md) code region goes to the filters, and is never reported as a fault in Mk code. One that no filter resumes ends the process with `panic: fault in a code region`, status 101.
- C code that installs its own signal handler replaces std's.

## Panics

A [panic](../reference.md#panics) prints a report and ends the process with status 101. `WriteDump(path)` writes a crash dump of the running process: a minidump on Windows, which a debugger opens. On every other target it fails with `Unsupported`.

A package may define a panic hook. It is called once per panic, before the report, and never for a nested panic. When it returns, the report is written and the process ends.

```mk
// verdict: ok
import std.runtime

// MkPanicHook writes a crash dump before Mk's report.
export extern MkPanicHook(info: &runtime.PanicInfo) {
    runtime.WriteDump("crash.dmp") else {}
}

export Main(sys: &System) {
    xs := [1, 2, 3]
    i := sys.args.Len() + 5
    _ = xs[i] // panics with code 2
}
```

```text
$ mk run hook.mk
panic: bounds at hook.mk:12:9
backtrace:
  Main (hook.mk)
```

On Windows the hook wrote crash.dmp before the report. `PanicInfo` holds only C ABI values (`code`, `message`, `file`, `line`, `column`), so a hook may be written in C. `PanicInfo.code` names the cause; the numbering only grows.

| Code | Cause |
|---|---|
| 1 | Integer overflow |
| 2 | Index or slice bounds |
| 3 | Division by zero |
| 4 | Shift amount |
| 5 | `Cast` |
| 6 | `Must` |
| 7 | `Assert` |
| 8 | `Panic`, and a library's own checks |
| 9 | Stack limit |
| 10 | A `str` slice off a scalar boundary |
| 11 | `FreeEmpty` of a non-empty container |
| 12 | The wrong allocator |
| 13 | An enum tag |
| 14 | A contract |
| 15 | Unreachable code |
| 16 | A suspending `Io` operation where none may suspend: in a function called from C after its library's stop, inside a task's own `extern` call, or `Atomic.Wait` off a task |

## A foreign Io

An `Io` is a table of operations. `ForeignIo(ctx, table)` makes a linear `Foreign` whose `Io` runs a program's own table, as `mem.Custom` makes an `Alloc` over a program's own functions ([mem](mem.md)). Use it to host Mk code inside another event loop.

- Each entry receives that `Io`, and finds `ctx` through `ForeignContext(io)`.
- `IoResult`, `SysErr`, `IoStat`, `IoAddr`, `IoChild`, `IoCallArgs` and `IoWords` are the records the entries take and return. `InertTable` is a table whose every entry answers `Other` and does nothing: copy it with `var t := runtime.InertTable` and set the entries your host provides.
- A wake with no `Io` (`Atomic.Wake`, `Waker.Wake`) calls each live `Foreign`'s wake entry from the waking thread, so a wake entry must not suspend.
- `Foreign.Release` waits for the entries in flight, on a task by yielding it to the scheduler, so a parked entry delays it. Afterwards every copy of the `Io` answers `Canceled`. An entry must not release its own `Foreign`: it would wait for itself for ever.
- At most 256 `Foreign`s are live at once; one more panics with code 8.

## Examples

Each program runs as written with `mk run`, except the fault filter, which needs `unsafe`: put it in a package whose manifest lists its module in `unsafe_modules` ([packages](../packages.md#2-the-manifest)).

### Runtime facts

```mk
// verdict: ok
import std.runtime

// Depth recurses until less than 64 KiB of stack is left, and returns how
// deep it got: an interpreter's guard against running off its stack.
Depth(n: int): int {
    if runtime.StackRemaining() < 64 << 10 {
        return n // a real interpreter fails with its own error here
    }
    return Depth(n + 1)
}

export Main(sys: &System): WriteError!void {
    procs := runtime.Processors()
    carriers := runtime.Carriers()
    try sys.Print("processors >= 1: {p}, carriers >= 1: {c}\n", (p: procs >= 1, c: carriers >= 1))
    (lo, hi) := runtime.StackBounds()
    try sys.Print("stack known: {known}\n", (known: hi > lo))
    try sys.Print("deep enough: {deep}\n", (deep: Depth(0) > 100))
    t0 := runtime.Cycles()
    t1 := runtime.Cycles()
    try sys.Print("cycles never go back: {ok}\n", (ok: t1 >= t0))
}
```

```text
$ mk run facts.mk
processors >= 1: true, carriers >= 1: true
stack known: true
deep enough: true
cycles never go back: true
```

- The counts and bounds vary by machine, so the program prints comparisons. Print `procs` or `hi - lo` to see your own.

### Run a blocking call

```mk
// verdict: ok
import std.runtime

// Lookup stands in for a C library call that blocks its thread, such as a
// synchronous DNS lookup. Blocking runs it on the blocking pool.
Lookup(id: int): int {
    var x := id
    for _ in 0..1000 {
        x = (x * 31 + 7) % 1000003
    }
    return x
}

// Ask runs a lookup in a task, and notes whether it was cancelled.
Ask(io: Io, canceled: mut bool) {
    _ = runtime.Blocking(io, 2, Lookup) else {
        *canceled = true // the scope was cancelled first, so Lookup never ran
        return
    }
}

export Main(sys: &System): Canceled|WriteError!void {
    v := try runtime.Blocking(sys.io, 1, Lookup)
    try sys.Print("lookup 1: {v}\n", (v))

    var canceled := false
    scope s := sys.io.Scope() {
        s.Cancel()
        s.go Ask(sys.io, mut canceled)
    }
    try sys.Print("lookup 2 cancelled: {canceled}\n", (canceled))
}
```

```text
$ mk run blocking.mk
lookup 1: 309041
lookup 2 cancelled: true
```

- `f` is a plain function of the state: pass everything it needs in `state`, and get everything back in its result.

### A thread of its own

```mk
// verdict: ok
import std.atomic
import std.runtime

// Squares holds what Worker computed; a global, since f takes only an Io.
var Squares: atomic.Atomic[i64] = atomic.Atomic[i64].New(0)

// Worker runs on a carrier thread of its own, as code bound to one OS
// thread needs. Tasks it spawns run on that thread too.
Worker(io: Io) {
    scope s := io.Scope() {
        for i in 1..5 {
            s.go Square(i)
        }
    }
}

Square(i: int) {
    _ = Squares.FetchAdd(i64(i * i), atomic.SeqCst)
}

// JoinAll joins j; a Join cut short by a cancel is retried on a NoCancel Io,
// since j is still the caller's after Canceled.
JoinAll(j: runtime.Joined, io: Io) {
    j.Join(io) else {
        JoinAll(j, io.NoCancel())
    }
}

export Main(sys: &System): Canceled|WriteError!void {
    JoinAll(runtime.Thread(sys.io, Worker), sys.io)
    try sys.Print("sum of squares 1..4: {n}\n", (n: Squares.Load(atomic.SeqCst)))

    w := runtime.Waker.New()
    w.Wake()           // a Wake before the Wait is kept
    try w.Wait(sys.io) // so this returns at once, and consumes it
    try sys.Print("kept wake consumed\n")
}
```

```text
$ mk run thread.mk
sum of squares 1..4: 30
kept wake consumed
```

- `Joined` is linear, so the compiler makes you join every `Thread`, also on the path where `Join` was cancelled.

### Resume from a fault

```mk
// verdict: ok
import std.raw
import std.runtime
import std.vm

// mov eax, [0]; ret: generated code that reads address 0, as a JIT's
// implicit null check does. The load is 7 bytes long.
const NullLoad: [8]u8 = [0x8B, 0x04, 0x25, 0x00, 0x00, 0x00, 0x00, 0xC3]

// NullCheck turns a read of address 0 into a result of 0xFFFF: it sets
// eax and resumes after the load. Every other fault goes on to the next
// filter and then to Mk's report. It takes no lock, allocates nothing
// and never suspends, as a filter must.
NullCheck(ctx: &runtime.FaultContext): runtime.FaultAction {
    if ctx.Kind() != runtime.FaultKind.Access || ctx.Addr() != 0 {
        return runtime.FaultAction.Pass
    }
    var c := *ctx
    c.SetReg(0, 0xFFFF)  // rax, register 0
    c.SetPc(c.Pc() + 7) // skip the load
    return runtime.FaultAction.Resume(ctx: c)
}

export Main(sys: &System): vm.Error|WriteError!void {
    unsafe "NullLoad is a whole function, flushed before the call; NullCheck resumes only its load" {
        _ = runtime.AddFaultFilter(NullCheck)
        var code := try vm.CodeRegion.New(4096)
        onerr code.Release()
        var w := try code.Edit()
        (mut w.Bytes()[0..8]).CopyFrom(NullLoad[..])
        w.Flush(0, 8)
        w.Done()
        f := raw.FnFromAddr[fn(): u32](code.Entry(0))
        try sys.Print("f() = {v:x}\n", (v: f()))
        code.Release()
    }
}
```

```text
$ mk run .
f() = ffff
```

- The fault is in a code region, so it reaches the filters on Windows, Linux and macOS alike. Registers are numbered as x86_64 encodes them: 0 is `rax`, 4 is `rsp`.
- See [std.vm](vm.md) for code regions.

### Write a crash dump

```mk
// verdict: ok
import std.fs
import std.runtime

export Main(sys: &System): fs.Error|WriteError!void {
    match runtime.WriteDump("app.dmp") {
        ok(_) {
            info := try fs.Stat(sys.io, "app.dmp")
            try sys.Print("wrote app.dmp: {big}\n", (big: info.size > 0))
            try fs.Remove(sys.io, "app.dmp")
        }
        err(e) {
            match e {
                runtime.Unsupported { try sys.Print("no dumps on this target\n") }
                runtime.OsError(code) { try sys.Print("the OS refused: {code}\n", (code)) }
            }
        }
    }
}
```

```text
$ mk run dump.mk
wrote app.dmp: true
```

- That is the output on Windows. On Linux the program prints `no dumps on this target`.
- The dump holds every thread's stack and registers and the list of loaded modules. `WriteDump` runs on the calling stack and suspends the other threads while it reads them.

## API

<!-- api:begin -->
Stability: Stable.

### `AddFaultFilter`

```
export AddFaultFilter(f: fn(&FaultContext): FaultAction): FilterId
```

Adds f after the filters already added. Filters run on every later
fault that reaches them, in the order added, before Mk's own report,
until one returns Resume. A filter runs inside a fault: it must not
take a lock, allocate or suspend. A fault inside a filter ends the
process with `panic: fault in a fault filter`, status 101.

Which faults reach the filters differs by target. On Windows: a stack
overflow, an access or illegal-instruction fault in Mk code, and any
fault whose pc is in a std.vm code region; never a fault that C code
handles itself (`__try`). On Linux: a SIGSEGV, SIGBUS or SIGILL that Mk
owns, which is one whose pc is in the executable's image, whose thread
is the main thread or one std started, or whose stack is a task's or a
carrier's; and any fault whose pc is in a std.vm code region. So on
Linux a fault in a shared library's C code called on an Mk thread
reaches the filters and Mk's report, where Windows routes only a fault
whose pc is in the image.

macOS routes as Linux does.

On Linux, a fault in C code outside the image on a thread std did not
make gets the signal's default action, raised again at once, as on
Windows it gets the system's. So does a SIGSEGV, SIGBUS or SIGILL that
another process or thread sends (kill, tgkill): it is not a fault and
is never reported as Mk's. C code that installs a signal handler of its
own replaces std's.

Callable only in unsafe (E1101). Panics with code 8 past 16 filters.
O(1) besides waiting for an earlier addition to publish; allocates
nothing.

### `Blocking`

```
export Blocking[T: Send](io: Io, state: T, f: fn(T): T): Canceled!T
```

- Flows: The result borrows from state and f.
- Errors: Canceled

Runs f(state) on the blocking pool, off the carriers, and returns its
result. Use it for a foreign call that blocks a thread, such as a C
library's wait or a raw system call, so that no carrier is held.

Fails with Canceled when io's scope is cancelled, or becomes cancelled
while the job waits in the pool's queue: the job is taken out unrun and
the caller still owns state, as a failed Chan.Send leaves the value
with the sender. On success state is consumed. Once f has started it
runs to its end and its result is returned, cancelled or not: a foreign
blocking call cannot be stopped from outside, so this is the one wait
a deadline cannot cut.

Off a task, or on an Io that is not the runtime's own (a Sim's), f runs
on the calling thread. The pool is shared with std's own blocking work:
64 threads plus the wait jobs, 1,024 threads in all. O(1) besides f:
one queue append and one park; allocates nothing on the heap.

### `Carriers`

```
export Carriers(): int
```

The number of carrier threads in use: MK_CARRIERS, a library's start
option, or the processor count; 1 in a program with no task. O(1);
allocates nothing.

### `Cold`

```
export Cold()
```

Hints that the current execution path is cold. Marks the enclosing basic
block path cold for branch prediction and outgoing calls; no-op at compile
time. O(1); allocates nothing.

### `Cycles`

```
export Cycles(): u64
```

Reads the CPU's cycle counter: `rdtsc` on x86_64, `CNTVCT_EL0` on
aarch64. It never decreases on one thread. Its rate is the target's: on
x86_64 the time-stamp counter's, on aarch64 the generic timer's fixed
frequency (`CNTFRQ_EL0`), not the processor's clock. It cannot run at
compile time.

### `Dedicated`

```
export Dedicated(io: Io): Io
```

An Io whose tasks run on one carrier thread of their own, which never
steals work and is never stolen from. Use it for code that needs one OS
thread, such as a library bound to the thread that started it. Every
task spawned from a scope opened from this Io runs there, and so do
their tasks. The carrier and its thread live until the process exits.
The carrier fires its tasks' sleeps; the shared carriers collect their
I/O completions. The Io's own operations run as io's do, on the
calling task.

A NoCancel copy of it is dedicated too, and io's own cancellation mask
is kept. A Sim's or another non-native Io is returned as it is, since a
Sim runs one task at a time already. A wrapping Io over a dedicated one,
such as std.sim's Recorder, is not dedicated: its scopes spawn on the
shared carriers. On Windows the carrier's thread handle stays open
until exit, so a handle-leak check around the call counts it.

Panics (code 8) when all 256 carrier records are in use, or when the
OS refuses the thread. Past the free records above the carrier count,
each call lowers by one the count of shared carriers not yet started.
O(1) system calls; reserves an 8 MiB system stack and commits the
carrier's record.

### `Error`

```
export type Error = Unsupported | OsError
```

Every way a std.runtime operation fails.

### `FaultAction`

```
export enum FaultAction
```

A filter's answer: Resume at the context it edited, which the handler
installs, or Pass to the next filter and then to Mk's own report.

Variants:

- `Resume(ctx: FaultContext)`: Resume at ctx, the context the filter edited.
- `Pass`: Pass the fault to the next filter, and then to Mk's own report.

### `FaultContext`

```
export struct FaultContext
```

A fault's context: its kind and address, and the faulting thread's
general registers and program counter. A filter may edit them with the
setters and return the context with Resume. Reg(n) and SetReg(n)
number the registers as the target encodes them (RegCount of them): on
x86_64 0 rax, 1 rcx, 2 rdx, 3 rbx, 4 rsp, 5 rbp, 6 rsi, 7 rdi, 8 to 15
r8 to r15; on aarch64 0 to 30 x0 to x30, 31 sp.

### `FaultContext.Addr`

```
export FaultContext.Addr(&self): uint
```

The faulting address; for Stack, the stack pointer. O(1); allocates
nothing.

### `FaultContext.Kind`

```
export FaultContext.Kind(&self): FaultKind
```

What faulted. O(1); allocates nothing.

### `FaultContext.Pc`

```
export FaultContext.Pc(&self): uint
```

The program counter of the faulting instruction. O(1); allocates
nothing.

### `FaultContext.Reg`

```
export FaultContext.Reg(&self, n: int): u64
```

General register n, 0 to RegCount() - 1, in the order FaultContext
lists. Another n panics with the bounds code (2). O(1); allocates
nothing.

### `FaultContext.SetPc`

```
export FaultContext.SetPc(mut self, pc: uint)
```

Sets the program counter the thread resumes at. O(1); allocates nothing.

### `FaultContext.SetReg`

```
export FaultContext.SetReg(mut self, n: int, v: u64)
```

Sets general register n, 0 to RegCount() - 1. Another n panics with the
bounds code (2). O(1); allocates nothing.

### `FaultContext.SetSp`

```
export FaultContext.SetSp(mut self, sp: uint)
```

Sets the stack pointer: Reg(4) on x86_64, Reg(31) on aarch64. O(1);
allocates nothing.

### `FaultContext.Sp`

```
export FaultContext.Sp(&self): uint
```

The stack pointer: Reg(4) on x86_64, Reg(31) on aarch64. O(1);
allocates nothing.

### `FaultKind`

```
export enum FaultKind
```

What faulted. Stack is an access to a guard page below a task's or a
carrier's stack: a stack overflow, reported as one wherever its pc is.
Access is any other read, write or execution the page does not allow.
Illegal is an illegal or privileged instruction (Windows'
EXCEPTION_ILLEGAL_INSTRUCTION and EXCEPTION_PRIV_INSTRUCTION, Linux's
SIGILL).

Variants:

- `Stack`: An access to a guard page below a task's or a carrier's stack: a stack
  overflow.
- `Access`: Any other read, write or execution the page does not allow.
- `Illegal`: An illegal or privileged instruction.

### `FilterId`

```
export struct FilterId
```

The registration of one filter. A filter stays for the life of the
process; there is no way to remove one.

### `Foreign`

```
export linear struct Foreign
```

A registered foreign operation table. It is linear, so it is always
released with Release, after which every copy of its Io answers
Canceled.

### `Foreign.Io`

```
export Foreign.Io(&self): Io
```

The Io of this Foreign. Copies may outlive it, and answer Canceled
after Release. O(1); allocates nothing.

### `Foreign.Release`

```
export Foreign.Release(self)
```

Ends the Foreign: waits for the entries in flight to return, and frees
its slot. No entry of the table runs after this, and every copy of the
Io answers Canceled. An entry that is parked, such as a sleep or a wait
the table hands to the native Io, delays Release until it returns.

On a task, Release waits by yielding the task through the scheduler,
with backoff, so an entry parked on the same carrier keeps running; off
a task, on a foreign thread, it yields the thread. An entry must not
release its own Foreign: it would wait for itself for ever, and this is
not detected. O(1) besides the wait; allocates nothing.

### `ForeignContext`

```
export ForeignContext(io: Io): *u8
```

The ctx that ForeignIo was given for io's Foreign, inside one of its
entries or anywhere its Io is live; null for an Io that is no live
Foreign's. Inside an entry it is the ctx even while another thread's
Release of the Foreign waits for that entry. Outside an entry, a
concurrent Release may make the answer stale as soon as it is returned.
Callable only in unsafe (E1101). O(1); allocates nothing.

### `ForeignIo`

```
export ForeignIo(ctx: *u8, table: &IoTable): Foreign
```

Makes an Io that runs a program's own operation table, as mem.Custom
makes an Alloc over a program's own functions. Each entry receives that
Io and finds ctx through ForeignContext. table and what ctx points at
must live until Release.

A wake with no Io (Atomic.Wake, Waker.Wake) calls every live Foreign's
wake entry with that Foreign's Io, from whatever thread called Wake, a
foreign one included, so a wake entry must not suspend. A wake entry
that itself calls Atomic.Wake or Waker.Wake, to reach the native wake,
is not called again from inside that call.

Callable only in unsafe (E1101). Panics with code 8 when 256 Foreigns
are live. O(256) at worst to find a slot; allocates nothing.

### `InertTable`

```
export const InertTable: IoTable
```

A table whose every entry does nothing and answers Other (os 0), to
copy and fill in: `var t := runtime.InertTable`, then set the entries a
host provides.

### `IoAddr`

```
export struct IoAddr
```

A network address as the udp, sendto, recvfrom and resolve entries take it:
ip, port, v6 and the IPv6 zone.

Fields:

- `ip: [16]u8`: The address in network order; an IPv4 address in ip[0..4], the rest 0.
- `port: u16`: The port, in host order.
- `v6: bool`: Whether ip holds an IPv6 address.
- `zone: u32`: The IPv6 scope id, 0 for none.

### `IoCallArgs`

```
export struct IoCallArgs
```

One call of the call entry: the operation, its route, its handles, bytes and
words, and its result.

Fields:

- `op: u32`: The IoOp constant that names the operation.
- `route: u32`: How the call runs: an IoRoute constant.
- `h: uint`: The runtime handle the call is on, 0 for none.
- `os: uint`: The OS handle behind h, which the native entry fills.
- `p: *u8`: The first input bytes: a pointer, with pn bytes.
- `pn: int`: The length of p in bytes.
- `q: *u8`: The second input bytes: a pointer, with qn bytes.
- `qn: int`: The length of q in bytes.
- `out: *u8`: The output bytes: a pointer, with room for outn bytes.
- `outn: int`: The length of out in bytes.
- `w: IoWords`: The scalar words in and out.
- `res: IoResult`: The call's result.
- `job: fn(*IoCallArgs)`: The native body that runs the operation, reading and writing this
  record.
- `stop: *u32`: On the blocking pool, the job's cancel word: 0 while it runs, 1 once a
  cancel or a deadline asked it to end. Null elsewhere.

### `IoChild`

```
export struct IoChild
```

The in-out record of the spawn entry: the child's stdio and inherited handle
in, its pipe ends and pid out.

Fields:

- `stdio: [3]u32`: In: how each of stdin, stdout and stderr is set up, IoStdInherit,
  IoStdNull or IoStdPipe.
- `inherit: uint`: In: a runtime socket handle the child inherits, 0 for none.
- `pipes: [3]uint`: Out: the runtime handle of the parent's end of each piped stream
  (stdin's is the write end), 0 where the stream is not a pipe.
- `pid: i64`: Out: the child's OS process id.

### `IoResult`

```
export struct IoResult
```

What an entry returns: value, or err with the OS's own code in os.

Fields:

- `value: i64`: The entry's result when err is None, such as a byte count or a runtime
  handle.
- `err: SysErr`: None on success, else the failure kind.
- `os: i64`: The OS's own error code when err is Other. On success some entries put
  a second result here, such as listen's bound port.

### `IoStat`

```
export struct IoStat
```

The records some table entries fill or take: a stat (IoStat), a
network address (IoAddr), a child's stdio (IoChild), and a call entry's
arguments (IoCallArgs) and words (IoWords).

Fields:

- `kind: u32`: IoKindFile, IoKindDir, IoKindLink or IoKindOther.
- `size: i64`: The size in bytes.
- `modified: i64`: The time of the last modification, in nanoseconds since the Unix epoch.

### `IoTable`

```
export struct IoTable
```

The operation table behind an Io: one fn per operation, each taking
the Io first. ForeignIo makes an Io over a table of a program's own.
IoTable and ForeignIo are provisional: they may change in a minor
release.

Fields:

- `open: fn(Io, str, u32): IoResult`: open(path, mode) opens a file; mode is 0, 1, 2 or the options form
  (IoOpenOptions). value is the runtime handle.
- `read: fn(Io, uint, mut []u8, i64): IoResult`: read(h, buf, pos) reads at most buf.Len() bytes at offset pos, or at the
  handle's position when pos is -1. value is the count; 0 is the end.
- `write: fn(Io, uint, []u8, i64): IoResult`: write(h, data, pos) writes data at offset pos, or at the handle's
  position when pos is -1. value is the count written.
- `size: fn(Io, uint): IoResult`: size(h) answers the file's size in bytes.
- `sync: fn(Io, uint): IoResult`: sync(h) flushes the file's data to its device.
- `close: fn(Io, uint): IoResult`: close(h) releases the handle. It is not a cancellation point.
- `rename: fn(Io, str, str): IoResult`: rename(src, dst) renames a file or directory.
- `remove: fn(Io, str): IoResult`: remove(path) removes the entry at path.
- `listen: fn(Io, str): IoResult`: listen(addr) binds a listener to "host:port". value is the runtime
  handle and os the bound port, so port 0 answers which port the OS chose.
- `accept: fn(Io, uint): IoResult`: accept(h) waits for one connection on the listener h. value is the new
  connection's runtime handle.
- `dial: fn(Io, str): IoResult`: dial(addr) connects to "host:port". value is the connection's runtime
  handle.
- `recv: fn(Io, uint, mut []u8): IoResult`: recv(h, buf) reads at most buf.Len() bytes from a connection. value is
  the count; 0 is the end of the stream.
- `send: fn(Io, uint, []u8): IoResult`: send(h, data) writes some of data to a connection. value is the count,
  which may be short.
- `now: fn(Io): IoResult`: now() answers the monotonic clock in nanoseconds, from an arbitrary
  start.
- `sleep: fn(Io, i64): IoResult`: sleep(nanos) suspends the caller for nanos nanoseconds on this Io's
  clock.
- `random: fn(Io, mut []u8): IoResult`: random(buf) fills buf with random bytes from the OS.
- `yield: fn(Io): IoResult`: yield() lets other tasks run before the caller continues.
- `signal: fn(Io, u32): IoResult`: signal(s) waits until the signal s arrives.
- `wait: fn(Io, *u32, u32): IoResult`: wait(addr, expected) suspends the caller while the word at addr holds
  expected, until a wake on it. It may return early.
- `wake: fn(Io, *u32, bool)`: wake(addr, all) wakes one waiter on the word at addr, or every one when
  all is true. It must not suspend.
- `mkdir: fn(Io, str): IoResult`: mkdir(path) makes a directory.
- `stat: fn(Io, str, mut IoStat): IoResult`: stat(path, st) fills st with the facts of the file at path.
- `opendir: fn(Io, str): IoResult`: opendir(path) opens a directory for reading. value is the runtime
  handle.
- `readdir: fn(Io, uint, mut []u8, mut IoStat): IoResult`: readdir(h, name, st) reads the next entry of the directory h: its name
  into name and its facts into st.
- `udp: fn(Io, IoAddr): IoResult`: udp(addr) makes a UDP socket bound to addr. value is the runtime handle
  and os the bound port.
- `sendto: fn(Io, uint, []u8, IoAddr): IoResult`: sendto(h, data, to) sends data as one datagram to to.
- `recvfrom: fn(Io, uint, mut []u8, mut IoAddr): IoResult`: recvfrom(h, buf, from) receives one datagram into buf and its sender's
  address into from. value is the count.
- `resolve: fn(Io, str, mut []IoAddr): IoResult`: resolve(host, out) writes the addresses of host into out. value is the
  count; a name with no answer is NotFound.
- `spawn: fn(Io, []str, mut IoChild): IoResult`: spawn(args, child) starts a program with the arguments args, set up and
  answered through child. value is the child's runtime handle.
- `procwait: fn(Io, uint): IoResult`: procwait(h) waits for the child h to end. value is its exit code.
- `prockill: fn(Io, uint): IoResult`: prockill(h) ends the child h at once.
- `inherited: fn(Io): IoResult`: inherited() answers the listening socket this process inherited from its
  parent, once; a second call is NotFound.
- `enter: fn(Io, u64)`: enter(id) runs on a task's own stack before the task runs and after
  each suspension point; id is the task's id.
- `leave: fn(Io, u64)`: leave(id) runs before each suspension point of a task and once when its
  function returns; id is the task's id.
- `deadline: fn(Io, *u8, i64, u32): IoResult`: deadline(rec, nanos, op) arms or disarms the deadline of a scope record.
- `stdio: fn(Io, u32, []u8): IoResult`: stdio(i, data) writes data whole to process stream i, 0 stdout or 1
  stderr.
- `call: fn(Io, *IoCallArgs): IoResult`: call(args) runs one OS operation described by args.

### `IoWords`

```
export struct IoWords
```

The four scalar words of a call-entry call, in and out.

Fields:

- `a: i64`: The first word; its meaning is the IoOp's.
- `b: i64`: The second word; its meaning is the IoOp's.
- `c: i64`: The third word; its meaning is the IoOp's.
- `d: i64`: The fourth word; its meaning is the IoOp's.

### `Joined`

```
export linear struct Joined
```

A running Thread. It is linear, so it is always joined with Join.

### `Joined.Join`

```
export Joined.Join(self, io: Io): Canceled!void
```

- Errors: Canceled

Waits until the Thread's carrier has ended, then joins its thread and
frees its record. The OS join runs on the blocking pool, so no carrier
is blocked.

Fails with Canceled when io's scope is or becomes cancelled, or its
deadline passes, while it waits. The Joined then stays the caller's, as
a failed Chan.Send leaves the value with the sender, to Join again, for
instance from a NoCancel Io. Off a task it is panic 16. O(1) besides
the wait; allocates nothing.

### `Likely`

```
export Likely(cond: bool): bool
```

Hints that `cond` is usually true and returns `cond` unchanged. Lowers
inline to `llvm.expect.i1`; no-op at compile time. O(1); allocates nothing.

### `OsError`

```
export error OsError(code: i64)
```

The OS refused, with its code (GetLastError on Windows).

### `PanicInfo`

```
export struct PanicInfo
```

What a panic hook receives: the panic code (see the panic code table
in the std.runtime docs), the message, and the source position of the
panicking code. Only C ABI values cross, so a hook may be written in C.

Fields:

- `code: u32`: The panic code.
- `message: str`: The panic's message.
- `file: str`: The source file of the panicking code.
- `line: int`: The line of the panicking code.
- `column: int`: The column of the panicking code.

### `Processors`

```
export Processors(): int
```

The number of processors the OS reports this process may run on. O(1)
after the first call's system call; allocates nothing.

### `RegCount`

```
export RegCount(): int
```

RegCount is how many general registers a FaultContext holds: 16 on
x86_64, 32 on aarch64 (x0 to x30 and sp). O(1); allocates nothing.

### `StackBounds`

```
export StackBounds(): (lo: uint, hi: uint)
```

The running task's usable stack, [lo, hi). lo is above the guard page
and, on Windows, above the reserve the overflow report keeps; the
task's frames grow down from hi. On a stack that is no task's, such as
a carrier's system stack or the main thread with no task, it is that
thread's stack with its guard area left out. Where the OS gives no
bounds (Linux's main thread), they are the ones start-up recorded for
that stack. It is (0, 0) when no bounds are known: Linux's main thread
before start-up records them, and a foreign thread. O(1) on a task: a
few compares; off a task, one system call and at most 256 loads;
allocates nothing.

### `StackRemaining`

```
export StackRemaining(): uint
```

The bytes left on the running stack above its usable low bound: the
stack pointer less StackBounds' lo, or 0 when the stack is at or past
it or has no known bounds. It is a handful of instructions on a task,
so an interpreter may call it on every call and fail with its own error
before the guard page. O(1); allocates nothing.

### `SysErr`

```
export enum SysErr
```

An entry's failure kind (None on success).

Variants:

- `None`
- `NotFound`
- `Exists`
- `Denied`
- `IsDirectory`
- `NotDirectory`
- `NoSpace`
- `InvalidPath`
- `Canceled`
- `Closed`
- `Refused`
- `Reset`
- `AddrInUse`
- `Unreachable`
- `InvalidAddress`
- `Other`
- `Unsupported`

### `TaskIo`

```
export TaskIo(): ?Io
```

The running task's Io, or none off a task. A function called from C
runs as a new task of its runtime, so TaskIo gives it the native Io,
with which it may wait, sleep and do I/O. O(1); allocates nothing.

### `Thread`

```
export Thread(io: Io, f: fn(Io)): Joined
```

Runs f on a new carrier of its own, whose thread runs nothing else. f
gets a full Io pinned to that carrier, as Dedicated's is, on a task
with a 16 MiB stack, as Main has. The carrier ends once f and every task
spawned on it have ended. f runs outside io's scopes: a cancel of the
caller's scope does not reach it, only Join.

With a Sim's or another non-native Io, f runs on the calling task, with
io, before Thread returns, as Blocking's f does; so it does with a
wrapping Io over a dedicated one, such as std.sim's Recorder.

Panics (code 8) when all 256 carrier records are in use or the OS
refuses the thread. A spawn through f's Io after the carrier ended
panics (code 8). O(1) system calls; takes a 16 MiB task slot, waiting
for one as a spawn does, and reserves an 8 MiB system stack.

### `Unlikely`

```
export Unlikely(cond: bool): bool
```

Hints that `cond` is usually false and returns `cond` unchanged. Lowers
inline to `llvm.expect.i1`; no-op at compile time. O(1); allocates nothing.

### `Unsupported`

```
export error Unsupported
```

This target cannot write a dump: only Windows can.

### `Waker`

```
export struct Waker
```

A wake that a task waits for and any thread may give, a foreign one
included. It is one word, so it may live in a `var` global or in memory
a C library holds a pointer to. A Wake before the Wait is kept: the
next Wait returns at once and consumes it.

### `Waker.New`

```
export Waker.New(): Waker
```

A Waker with no wake pending. O(1); allocates nothing.

### `Waker.Wait`

```
export Waker.Wait(&self, io: Io): Canceled!void
```

- Errors: Canceled

Waits until a Wake, then consumes it; returns at once when one is
pending. It may return early, as a wait on an atomic word does, so the
caller rechecks what it waits for. Fails with Canceled when io's scope
is or becomes cancelled. Off a task it is panic 16. O(1) besides the
wait; allocates nothing.

### `Waker.Wake`

```
export Waker.Wake(&self)
```

Gives the wake: wakes the task waiting on it, or keeps the wake for the
next Wait. It may be called from any thread, a foreign one included,
and needs no Io. O(1); allocates nothing.

### `WriteDump`

```
export WriteDump(path: str): Error!void
```

- Errors: Error (OsError(code: i64), Unsupported)

Writes a crash dump of this process to path, which is created or
truncated. On Windows the dump is a minidump that a debugger opens:
every thread's stack and registers, and the list of loaded modules.
It runs on the calling stack and needs a few pages of it; the other
threads are suspended while they are read.

Fails with Unsupported on every target but Windows, and on Windows
when dbghelp.dll cannot be loaded; with OsError and the OS's code when
the file cannot be written. O(the process's threads and modules);
allocates nothing of its own.

<!-- api:end -->
