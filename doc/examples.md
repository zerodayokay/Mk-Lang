# Examples

Complete programs, each larger than a [cookbook](cookbook.md) recipe: a web service, a database, a kernel and an interpreter. Each one shows how memory, errors and concurrency fit together in a real program. Read the [overview](overview.md) first.

## A web service

A JSON service over SQLite. It shows routing, a typed error mapped to an HTTP status, and JSON written by a generator.

```mk
// verdict: parse
import std.db.sqlite
import std.http
import std.json

struct App {
    db: &sqlite.Db
}

struct Note {
    export id: i64
    export title: str
    export body: str
}

gen json.Codec(Note)

error NotFound(id: i64)

type LoadError = NotFound | sqlite.Error

// Load copies the row's text into a, so the note outlives the statement.
Load(db: &sqlite.Db, a: &Arena, io: Io, id: i64): LoadError!Note from a {
    var stmt := try db.Prepare(a, io, "select title, body from notes where id = ?")
    defer stmt.Close()
    try stmt.BindInt(1, id)
    found := try stmt.Step(io)
    if !found {
        fail NotFound(id: id)
    }
    title := try a.Dup(stmt.Str(0))
    body := try a.Dup(stmt.Str(1))
    return Note(id: id, title: title, body: body)
}

GetNote(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    id := req.ParamInt("id") else {
        fail http.Status(code: 400)
    }
    note := Load(c.db, a, io, id) else e {
        code := match e {
            NotFound { 404 }
            sqlite.Busy { 503 }
            _ { 500 }
        }
        fail http.Status(code: code)
    }
    try res.Header("Content-Type", "application/json")
    var w := res.Writer()
    try note.FormatJson(mut w)
}

Health(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"ok\n")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/notes/{id}", handler: GetNote),
    http.Route[App](method: "GET", pattern: "/health", handler: Health),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error|sqlite.Error!void {
    db := try sqlite.Open(sys.arena, sys.io, "notes.db")
    defer db.Close()
    try db.Exec(sys.io, "create table if not exists notes (id integer primary key, title text, body text)")
    app := App(db: &db)
    try http.Serve(sys, "127.0.0.1:8080", 1_000_000, &app, Handle)
}
```

The package needs [the manifest that `std.db.sqlite` describes](std/sqlite.md#the-package-manifest). With it, the program builds as written:

```text
$ mk run
$ curl -i localhost:8080/notes/1
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 47

{"id":1,"title":"Shopping","body":"milk, eggs"}
```

**What to notice.**

- `http.Serve` runs each connection in its own task, with an arena that is reset after every reply. A handler allocates in it and never frees.
- `Load` returns a `Note` that borrows only `a`, as `from a` says. It copies the row's text into the arena, so the note outlives the statement.
- `stmt` is linear: `defer stmt.Close()` closes it on every path, so no `try` can leak it.
- A handler fails with `http.Status` to answer with that status. `match e` turns each error of `Load` into one.
- `App` holds `&sqlite.Db`, which is `Share`, so every connection's task can use it.
- One process owns the database file ([files, locks and processes](std/sqlite.md#files-locks-and-processes)).

## A crash-safe key-value store

A store that appends each write to a log and keeps an index in memory. Its test crashes the store at a random point, a thousand times, and checks that no acknowledged write is lost.

```mk
// verdict: ok
import std.fs
import std.hash
import std.sim

// A record on disk: key, value, and a CRC-32 of the first 16 bytes.
const RecordSize: int = 20

type Error = fs.Error | OutOfMemory

linear struct Store {
    log: fs.File
    end: i64
    index: Map[u64, u64]
}

// Put writes the low n bytes of v at buf[at..], least significant first.
Put(buf: mut []u8, at: int, n: int, v: u64) {
    for i in 0..n {
        buf[at + i] = Trunc[u8](v >> Cast[u64](i * 8))
    }
}

// Get reads back what Put wrote.
Get(buf: []u8, at: int, n: int): u64 {
    var v: u64 = 0
    for i in 0..n {
        v |= u64(buf[at + i]) << Cast[u64](i * 8)
    }
    return v
}

// ReadRecord fills rec from pos, and returns false at the end of the log.
// ReadAt may return fewer bytes than asked, so it loops.
ReadRecord(log: &fs.File, io: Io, pos: i64, rec: mut []u8): fs.Error!bool {
    var got := 0
    for got < rec.Len() {
        n := try log.ReadAt(io, pos + i64(got), mut rec[got..])
        if n == 0 {
            return false
        }
        got += n
    }
    return true
}

// WriteRecord writes all of rec at pos.
WriteRecord(log: &fs.File, io: Io, pos: i64, rec: []u8): fs.Error!void {
    var put := 0
    for put < rec.Len() {
        put += try log.WriteAt(io, pos + i64(put), rec[put..])
    }
}

// Open replays the log into the index. A torn or corrupt record ends the
// replay: it was never acknowledged, so it is cut off.
Store.Open(a: &Arena, io: Io, path: str): Error!Store from a {
    var o := fs.ReadWrite
    o.create = true
    var log := try fs.OpenWith(io, path, o)
    onerr log.Abandon()
    var index := Map[u64, u64].New(a)
    var rec: [RecordSize]u8 = [0; RecordSize]
    var end: i64 = 0
    for {
        whole := try ReadRecord(&log, io, end, mut rec[..])
        if !whole || u64(hash.Crc32(rec[..16])) != Get(rec[..], 16, 4) {
            break
        }
        try index.Set(Get(rec[..], 0, 8), Get(rec[..], 8, 8))
        end += i64(RecordSize)
    }
    try log.Truncate(io, end)
    return Store(log: log, end: end, index: index)
}

// Put returns only once the record is on disk.
Store.Put(mut self, io: Io, key: u64, value: u64): Error!void {
    var rec: [RecordSize]u8 = [0; RecordSize]
    Put(mut rec[..], 0, 8, key)
    Put(mut rec[..], 8, 8, value)
    Put(mut rec[..], 16, 4, u64(hash.Crc32(rec[..16])))
    try WriteRecord(&self.log, io, self.end, rec[..])
    try self.log.Sync(io)
    self.end += i64(RecordSize)
    try self.index.Set(key, value)
}

Store.Get(&self, key: u64): ?u64 {
    if v := self.index.Get(&key) {
        return some(*v)
    }
    return none
}

// Close needs no error: every record is synced when Put returns.
Store.Close(self, io: Io) {
    Store(log, end: _, index: _) := self
    log.Close(io) else {
        log.Abandon()
    }
}

test "an acknowledged write survives any crash" (sys: &System) {
    for seed in 0..1000 {
        var world := try sim.Sim.New(sys.arena, Cast[u64](seed))
        defer world.Free()
        var a := try sys.arena.Sub()
        defer a.Free()
        var acked: u64 = 0
        var store := Store.Open(&a, world.Io(), "kv") else {
            continue
        }
        world.CrashAfterRandomOps()
        for k in 0..50 {
            store.Put(world.Io(), Cast[u64](k), Cast[u64](k * 10)) else {
                break
            }
            acked += 1
        }
        store.Close(world.Io())
        again := Store.Open(&a, world.Restart(), "kv").Must()
        for k in 0..Cast[int](acked) {
            Assert(again.Get(Cast[u64](k)).Must() == Cast[u64](k * 10))
        }
        again.Close(world.Io())
    }
}
```

`mk test` runs the test:

```text
$ mk test kv.mk
PASS main "an acknowledged write survives any crash"
1 passed, 0 failed
```

**What to notice.**

- `Store` holds a file, so it is `linear`: the compiler rejects a path that drops it without `Close`.
- `onerr log.Abandon()` in `Open` releases the file if the replay fails.
- `ReadAt` and `WriteAt` may move fewer bytes than asked, so `ReadRecord` and `WriteRecord` loop.
- A record is acknowledged only after `Sync`. Remove the `Sync` line and the test fails: a crash then loses writes that `Put` reported done.
- The test is deterministic. All I/O goes through `world.Io()`, a simulated disk and clock, and the crash point is drawn from the seed. A failing seed reruns exactly.

## A small kernel

A freestanding x86-64 kernel's core: a serial port driver, a page-frame allocator and the kernel's global state. It has no `Main`, no `Io`, no heap and no scheduler, and imports only `std.atomic` and the generator `std.dyn`, which need no operating system ([layers](std/README.md#1-layers)). Boot code, written in Mk or assembly, calls `KernelMain`.

```mk
// verdict: ok
// arch: x86_64
import std.atomic
import std.dyn

const Com1: u16 = 0x3F8
const FrameSize: uint = 4096
const FrameCount: int = 32768

// Out8 and In8 are the x86 port instructions.
Out8(port: u16, v: u8) {
    unsafe "out writes one byte to an I/O port and touches no memory" {
        asm("out dx, al", in("dx") port, in("al") v, options(nomem, nostack, preserves_flags))
    }
}

In8(port: u16): u8 {
    var v: u8 = 0
    unsafe "in reads one byte from an I/O port and touches no memory" {
        asm("in al, dx", out("al") v, in("dx") port, options(nomem, nostack, preserves_flags))
    }
    return v
}

interface Device {
    Write(mut self, data: []u8): int
}

gen dyn.Of(Device) // writes DeviceDyn, a Device whose type is chosen at run time

// A 16550 serial port.
struct Uart {
    port: u16
}

impl Device for Uart {
    Write(mut self, data: []u8): int {
        for b in data {
            for In8(self.port + 5) & 0x20 == 0 { // wait for an empty transmitter
            }
            Out8(self.port, b)
        }
        return data.Len()
    }
}

// Frames hands out 4 KiB physical pages from one region.
struct Frames {
    used: [FrameCount]bool
    base: uint
}

Frames.Take(mut self): ?uint {
    for i, slot in mut self.used {
        if !*slot {
            *slot = true
            return some(self.base + Cast[uint](i) * FrameSize)
        }
    }
    return none
}

Frames.Give(mut self, addr: uint) {
    i := Cast[int]((addr - self.base) / FrameSize)
    Assert(self.used[i]) // giving back a free page is a bug: panic
    self.used[i] = false
}

struct Kernel {
    console: Uart
    frames: Frames
}

// The kernel's state, behind a spin lock: a global must be Share.
var State: atomic.Spin[?Kernel] = atomic.Spin.New(none)

Log(d: mut DeviceDyn, msg: str) {
    _ = d.Write(msg.AsBytes())
}

// The boot code calls KernelMain with the first free physical address.
export extern KernelMain(free_base: uint) {
    var g := State.Lock()
    defer g.Unlock()
    g.Store(some(Kernel(console: Uart(port: Com1), frames: Frames(used: [false; FrameCount], base: free_base))))
    slot := g.Mut()
    if k := mut *slot {
        var console := DeviceDyn.Of(mut k.console)
        Log(mut console, "kernel: up\n")
        if page := k.frames.Take() {
            k.frames.Give(page)
            Log(mut console, "kernel: frames ok\n")
        }
    }
}
```

Check it with `mk check --target x86_64-freestanding`. A bootable image adds the boot code, and its `pkg.mk` names a link script and lists this module in `unsafe_modules`, since it writes `unsafe`.

**What to notice.**

- Every `unsafe` block says why it is sound.
- The global holds the concrete `Uart`. A global must be `Share` (E1002), and a `DeviceDyn` is not, so the kernel makes one for the length of a call.
- The dynamic type has a name, `DeviceDyn`, so a call that dispatches at run time shows in the code.
- `Assert` in `Give` panics on a double free. In a freestanding program a panic calls the program's `MkPanicHook`, or traps.
- `mk query stack KernelMain` prints the deepest call chain. It reports no static bound here because of the one call through `DeviceDyn`; a kernel that needs a bound calls `Uart` directly.

### Interrupt entry

An x86-64 interrupt handler written in Mk, entered through a `naked` stub. The stub saves the registers the handler may clobber, calls a plain Mk function with a pointer to the frame the CPU pushed, restores the registers and returns with `iretq`.

```mk
// verdict: ok
import std.atomic

struct Frame {
    export rip: u64
    export cs: u64
    export rflags: u64
    export rsp: u64
    export ss: u64
}

var Hits: atomic.AtomicInt = atomic.AtomicInt.New(0)
var LastRip: atomic.AtomicInt = atomic.AtomicInt.New(0)

OnBreakpoint(frame: *Frame) {
    unsafe "the stub passes the frame the CPU pushed, live for this call" {
        LastRip.Store(Trunc[i64]((*frame).rip))
    }
    _ = Hits.Add(1)
}

naked Int3Stub() {
    unsafe "an interrupt gate enters here; every caller-saved register is saved, and the stack is aligned at the call" {
        asm("""
            push rax
            push rcx
            push rdx
            push rsi
            push rdi
            push r8
            push r9
            push r10
            push r11
            lea rdi, [rsp + 72]
            cld
            call {h}
            pop r11
            pop r10
            pop r9
            pop r8
            pop rdi
            pop rsi
            pop rdx
            pop rcx
            pop rax
            iretq
            """, h: sym OnBreakpoint)
    }
}

StubAddress(): u64 {
    var addr: u64 = 0
    unsafe "lea computes the stub's address and touches nothing" {
        asm("lea {r}, [rip + {s}]", r: out(reg) addr, s: sym Int3Stub, options(pure, nomem, nostack, preserves_flags))
    }
    return addr
}
```

**What to notice.**

- The CPU aligns the stack to 16 bytes before it pushes its five words, so the stub's nine pushes leave it aligned for the call.
- In a kernel built without SSE (`general-regs-only`), the handler touches no SSE register, so the stub saves the general registers only. A kernel whose handlers use SSE adds `fxsave64`.
- `sym` names the handler in the assembly and keeps it linked. `StubAddress` gets the stub's address for the IDT the same way.
- Like the kernel above, the module is listed in `unsafe_modules`.

## An interpreter and a lexer

Two tools for interpreters and lexers: `become`, a guaranteed tail call, and range patterns in `match`.

A threaded interpreter jumps from one handler to the next with `become`. Each handler has the dispatcher's signature, so the call replaces the current frame, and a program of any length runs in one frame.

```mk
// verdict: ok
struct Vm {
    code: []u8
}

Step(vm: &Vm, pc: int, acc: i64): i64 {
    if pc >= vm.code.Len() {
        return acc
    }
    become Dispatch[vm.code[pc]](vm, pc + 1, acc)
}

Inc(vm: &Vm, pc: int, acc: i64): i64 {
    become Step(vm, pc, acc + 1)
}

Double(vm: &Vm, pc: int, acc: i64): i64 {
    become Step(vm, pc, acc *% 2)
}

Halt(vm: &Vm, pc: int, acc: i64): i64 {
    return acc
}

const Dispatch: [3]fn(&Vm, int, i64): i64 = [Inc, Double, Halt]

Run(vm: &Vm): i64 {
    return Step(vm, 0, 0)
}

export Main(sys: &System): WriteError!void {
    program: [5]u8 = [0, 0, 1, 0, 2] // add one, add one, double, add one, halt
    vm := Vm(code: program[..])
    try sys.Print("{n}\n", (n: Run(&vm)))
}
```

```text
$ mk run vm.mk
5
```

**What to notice.** `Dispatch` is a table of `fn` values indexed by opcode. `Halt` ends the run with a plain `return`.

A lexer classifies characters and bytes with range patterns.

```mk
// verdict: ok
Class(c: char): int {
    return match c {
        'a'..'{' | 'A'..'[' | '_' { 1 }
        '0'..':' { 2 }
        ' ' | '\t' | '\n' { 3 }
        _ { 0 }
    }
}

Width(lead: u8): int {
    return match lead {
        ..0x80 { 1 }
        0xC0..0xE0 { 2 }
        0xE0..0xF0 { 3 }
        0xF0..0xF8 { 4 }
        0x80..0xC0 | 0xF8.. { 0 }
    }
}
```

**What to notice.** Ranges are half-open, as every range in Mk: `'0'..':'` is the ten digits. An open end, `..0x80` or `0xF8..`, reaches the type's limit, so the byte match in `Width` covers all 256 values with no `_` arm.

### What `become` refuses

Each refusal has its own error code. The callee's signature must match the caller's: parameter and result types, error set and flows.

```mk
// verdict: reject E0312
Count(n: int): int {
    become Add(n, 1) // <- E0312
}

Add(a: int, b: int): int {
    return a + b
}
```

Nothing may be left to run after the callee: no pending `defer`, `onerr` or `scope` (E0406), and no linear value left unconsumed (E0402).

```mk
// verdict: reject E0406
Log(n: int) {
}

Down(n: int): int {
    defer Log(n)
    if n == 0 {
        return 0
    }
    become Down(n - 1) // <- E0406
}
```

```mk
// verdict: reject E0402
linear struct Token {
    id: int
}

Spend(t: Token): int {
    Token(id) := t
    return id
}

Hold(n: int): int {
    t := Token(id: n)
    if n == 0 {
        return Spend(t)
    }
    become Hold(n - 1) // <- E0402
}
```

No argument may borrow the caller's frame, which is gone when the callee runs.

```mk
// verdict: reject E0603
Sum(x: &int, n: int): int {
    if n == 0 {
        return *x
    }
    v := *x + n
    become Sum(&v, n - 1) // <- E0603
}
```

Every argument must travel in registers on every target, or be the caller's own parameter passed on in the same position. Win64 passes a struct of 24 bytes through memory the caller allocated, so `Fold` is refused.

```mk
// verdict: reject E0313
struct Acc {
    sum: int
    count: int
    last: int
}

Fold(a: Acc, n: int): Acc {
    if n == 0 {
        return a
    }
    become Fold(Acc(sum: a.sum + n, count: a.count + 1, last: n), n - 1) // <- E0313
}
```

The target must be a Mk function or a `fn` value (E0314). It cannot be a method called through an interface bound, a function whose `cpu` clause the caller lacks, an `extern` function or an intrinsic. A `naked` function makes no tail call.

```mk
// verdict: reject E0314
interface Shape {
    Area(&self): f64
}

Measure[T: Shape](s: &T): f64 {
    become s.Area() // <- E0314
}
```

```mk
// verdict: reject E0314
// arch: x86_64
Fast(x: int): int
    cpu "avx2"
{
    return x
}

Slow(x: int): int {
    unsafe "the caller tests Cpu().Has(\"avx2\") first" {
        become Fast(x) // <- E0314
    }
}
```

An `extern` function and an intrinsic are not Mk functions. A `naked` function has no frame to replace, and its body must be one `asm` statement (E1105).

```mk
// verdict: reject E0314
extern Getpid(): u32

Pid(): u32 {
    unsafe "the C library's call" {
        become Getpid() // <- E0314
    }
}
```

```mk
// verdict: reject E0314
import std.raw

Same(a: *u8, b: *u8, n: int): int {
    unsafe "a and b point at n bytes each" {
        become raw.Compare(a, b, n) // <- E0314
    }
}
```

```mk
// verdict: reject E1105 E0314
naked Entry(): never {
    become Spin() // <- E1105 E0314
}

Spin(): never {
    become Spin()
}
```

### What range patterns refuse

A range whose `lo` is not below its `hi` is empty (E0315). A match must cover every value of the type (E0306); for `char`, that is every Unicode scalar value, which leaves out the surrogates.

```mk
// verdict: reject E0315
Digit(c: char): int {
    return match c {
        '9'..'0' { 1 } // <- E0315
        _ { 0 }
    }
}
```

```mk
// verdict: reject E0306
Sign(x: i8): int {
    return match x { // <- E0306
        ..0 { -1 }
        1.. { 1 }
    }
}
```

Ranges may overlap: an arm takes only the values the arms before it did not, and an arm that gets none is not an error. Patterns take no guards, so a condition is an `if` inside the arm.

```mk
// verdict: ok
Size(n: u16): int {
    return match n {
        0..256 { 1 }
        200..300 { 2 }
        256.. {
            if n > 1000 {
                return 4
            }
            3
        }
    }
}
```
