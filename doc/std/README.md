# The standard library

The standard library is Mk code under `std/`, shipped inside the `mk` binary. It includes the runtime and the system layer: there is no C runtime and no libc. This guide covers what every module shares: its layer, the naming of its calls, how it allocates, does I/O, fails and is cancelled, and how stable it is, with what a release of Mk may change. The [module index](#8-module-index) links each module's page. Read the [overview](../overview.md) first.

Each module's page says what the module is for, what it costs and how it fails, with examples, and ends with the module's API, generated from the doc comments in `std/`. `mk doc --std std.<module>` prints the same API, and [reference section 18](../reference.md#18-library-surface) lists every signature in one place. The prelude, the names every module sees with no import, is [std.core](core.md).

## 1. Layers

The library has four layers. A module imports only from its own layer and the layers below it.

| Layer | Modules | Needs |
|---|---|---|
| 0, core | `core` (the prelude types, interfaces, slices, `str`, `Cell`, `Format`), `math`, `simd`, `atomic`, `mmio`, `hash`, `build` | Nothing: no allocator, no OS |
| 1, memory | `mem` (allocators), `coll` (containers), `text` (`Text`, Unicode, formatting) | Layer 0, and a source of memory pages |
| 2, system | `fs`, `net`, `time`, `proc`, `term`, `sync`, `log`, `dl`, `runtime`, `vm`, `linux`; the private `rt`, `sys` and `task` | Layer 1, and an OS |
| 3, batteries | `http`, `json`, `crypto`, `compress`, `regex`, `cli`, `sim`, `db.sqlite`, `big`, `encoding`, `url`, `websocket`, `csv`, `x509`, `tls`, `quic`, `testing`, `libc` | Layer 2 |

The generator modules `derive` and `dyn` run at compile time and belong to no layer: the code they emit uses layer 0 alone.

A **hosted** program, the usual kind, gets everything. Its entry, its panic report, stack-overflow handling and system calls are Mk code in `rt` and `sys`, and no C library is linked. The exception is a Linux program that links a shared library: it also loads the system libc.

A **freestanding** program, such as a kernel, links no `rt`, `sys` or `task`. The reference's [freestanding](../reference.md#freestanding) section lists what it may import, and [std.free](free.md) how it reports a panic and gets its heap.

## 2. Naming

One word means one thing across the whole library, so you can predict a name you have not seen.

| Word | Always means |
|---|---|
| `New` | Creates a value. Allocates only if it takes an allocator |
| `Free` | Ends memory. A heap-backed value takes the allocator it was made from. An `Arena` remembers its source, so `Arena.Free(self)` takes none. A container's `Free` frees its elements first |
| `FreeEmpty`, `FreeEach` | End a container whose elements are linear and not `Free`: panic unless it is empty, or hand each element to a callback |
| `Close` | Ends a handle: a file, a socket, a listener |
| `Abandon` | Ends a handle on an error path. Never fails, and loses unflushed data |
| `Get` | Looks up by key or index. Returns `?T` and never panics |
| `Set` | Stores at a key or index |
| `Replace` | Stores and returns what was there: a map's value at a key, or any place with `Replace(dst, x)` |
| `Swap` | Exchanges two places |
| `Load`, `Store` | Read or write the one value a holder keeps: `Cell`, `Guard`, `AtomicInt` |
| `Ref`, `Mut` | Borrow the one value a holder keeps, shared or exclusive: `Box`, `Guard`, `Shared` |
| `Push`, `Pop` | Add and remove at the end |
| `Has` | Membership. Returns `bool` |
| `Remove` | Deletes by key or index |
| `GetPair` | Two elements, both `mut`. `none` when they are the same one or either is missing |
| `SplitAt`, `Chunks`, `ChunksMut` | Pieces of a slice: two exclusive halves at an index, or every `n` elements, shared or exclusive |
| `Sweep` | Drops every element a predicate does not keep, and returns how many |
| `Len` | Count of elements. For `str`, bytes |
| `Is` prefix | A predicate |
| `To` prefix | A conversion that allocates. Takes an arena |
| `As` prefix | A conversion that does not allocate. Returns a view |
| `Parse` prefix | Text to a value. Returns an error set |
| `Read`, `Write` | One operation, which may be partial. Returns a count |
| `ReadAll`, `WriteAll` | Loops until done |
| `Next` | One step of iteration. Returns `?T` |
| `Iter` | Makes a value with a `Next` |
| `Must` | Unwraps or panics |

There is no `Or` method for a default: write `x := opt else { v }`.

**Parameter order is always receiver, allocator or arena, `io`, then the rest.** You never need to look up the order of a call. Naming for your own code is in the [style guide](../style.md).

### One way per concept

| Concept | The one way | Not provided |
|---|---|---|
| A growing list | `List[T]` | A linked list in the core |
| A list that outlives an arena | `HeapList[T]`, linear | |
| A map | `Map[K, V]` | An ordered map in the core. `coll.BTree` is a separate structure |
| Building text | `Text` | String concatenation |
| Formatting | `Print` to a `Writer`; `arena.Fmt` for a `str` | |
| Reading a whole file | `fs.ReadText`, `fs.ReadBytes` | |
| Sorting | `xs.Sort(less)`; `try xs.SortStable(a, less)` or `xs.SortStableInPlace(less)` to keep equal elements in order; `xs.Search(&key, less)` to find a place in a sorted slice | Sort by key |
| A counter shared across tasks | `atomic` | |
| State shared across tasks | `Mutex[T]` | Reader-writer locks in the core. `sync.RwLock` is separate |
| Communication between tasks | `Chan[T]` | |
| Time | `time.Instant`, `time.Duration` | Floating-point seconds |
| Randomness | `io.Random(buf)` for bytes from the OS; `crypto.Random` for a fast stream of secure bytes; `math.Rng` for a sequence that repeats from a seed | A global generator |
| Logging | A `log.Logger` passed as a value | A global logger |

## 3. Memory: allocators and arenas

Nothing in Mk allocates behind your back. A function can allocate only if one of its parameters can reach an allocator. `Main` receives two: `sys.arena`, which lives as long as the program, and `sys.heap`.

**Arenas are the default.** `List`, `Map`, `Set` and `Text` hold a `&Arena`. They need no release: the arena frees everything at once when it ends. A short program never thinks about memory, and a server makes one arena per request.

```mk
// verdict: ok
// Squares builds a list in the caller's arena: no release needed.
Squares(a: &Arena, n: int): OutOfMemory!List[int] from a {
    var out := List[int].New(a)
    for i in 0..n {
        try out.Push(i * i)
    }
    return out
}

// Keep builds a list on the heap, which outlives any arena.
Keep(a: &Alloc, n: int): OutOfMemory!HeapList[int] {
    var out := HeapList[int].New(a)
    onerr out.Free(a) // a failed Push frees what was built
    for i in 0..n {
        try out.Push(a, i)
    }
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free() // frees the list and everything else in the arena
    xs := try Squares(&arena, 5)
    a := sys.heap.Alloc()
    ys := try Keep(a, 3)
    defer ys.Free(a) // linear: the compiler rejects a path without it
    try sys.Print("{x} {y}\n", (x: xs[4], y: ys.Len()))
}
```

```text
$ mk run lists.mk
16 3
```

| Type | Backed by | Kind | Push | Release |
|---|---|---|---|---|
| `List[T]`, `Map[K, V]`, `Set[T]`, `Text` | An arena | Unique | `try xs.Push(x)` | None. The arena ends it |
| `HeapList[T]`, `HeapMap[K, V]`, `TextMap[V]`, `HeapText` | Any `&Alloc` | Linear | `try xs.Push(a, x)` | `xs.Free(a)` |
| `Box[T]` | Any `&Alloc` | Linear | | `b.Free(a)` |

A heap container takes the allocator at every call that may allocate, and is linear, so the compiler checks that it is freed. It records its allocator's identity and panics if you pass a different one.

| Allocator | Use | Shared by tasks | Free one object | Release |
|---|---|---|---|---|
| `Arena` | Everything scoped: requests, queries, parse trees, short programs | No | No | `Free`, `Reset` |
| `arena.Sub()` | Scratch space inside a function | No | No | `Free`, `Reset` |
| `Arena.NewIn(a)` | An arena whose memory comes from another allocator, so a `Limit`, a `Fixed` or a `FixedHeap` bounds it and a `Counting` measures it | No | No | `Free`, `Reset` |
| `Heap` | Long-lived data | Yes, as `&Heap` | Yes | Process end |
| `mem.Fixed` | Over a caller's buffer, a stack array included | No | The last one only | The buffer's scope, `Reset` |
| `mem.FixedHeap` | A general-purpose allocator over a caller's buffer: a kernel's heap, a subsystem on a fixed share of memory | No | Yes, and its bytes are used again | The buffer's scope, `Reset` |
| `mem.Fallback` | Over a caller's buffer while requests fit, then another allocator: small containers stay off the heap | No | As `Fixed` in the buffer, as wrapped outside it | The buffer's scope, `Reset` |
| `mem.Pool[T]` | Same-size objects, reused often | No | Yes, `Free(h)` | Its arena |
| `mem.Counting` | Wraps another allocator, counts bytes and live objects, reports leaks in tests | No | As wrapped | |
| `mem.Limit` | Wraps another allocator, refuses what would take the live bytes past a limit | No | As wrapped | |
| `mem.Failing` | Wraps another allocator, fails the requests a test picks, to reach the code that handles `OutOfMemory` | No | As wrapped | |
| `mem.Null` | Always fails. Proves at run time that a path allocates nothing | No | No | |

An `&Alloc` never crosses to another task (E1002). A task that allocates on the heap takes the `&Heap` and calls `Alloc()` itself.

A `Pool`'s slots carry a generation. A stale handle gets `none` from `Get` and `false` from `Free`, never a panic.

Growing an arena list abandons the old buffer in the arena, so the worst-case waste is about the final size. A server that puts per-request data in `sys.arena` grows without bound; give each request its own arena. The [codebase guide](../codebase.md#4-allocators-in-a-library-module) shows how a library takes its caller's arena.

## 4. `Io` and the system handle

`Main` receives a `&System`:

```mk
// verdict: parse
struct System {
    export args: []str   // args[0] is the program's path, as it was started
    export io: Io        // the door to the OS
    export arena: &Arena // lives as long as the program
    export heap: &Heap   // for what outlives any one arena
}
```

`Io` is a value, not a function kind. A function can do I/O, sleep or wait only through an `Io` it holds, or a value that holds one, such as a `Reader` or `Writer`. So a signature shows whether a function can block. Any function can call any other; there is no `async`.

```mk
// verdict: ok
import std.fs

Count(text: str): int {
    var n := 0
    for line in text.Lines() {
        if line.Len() > 0 {
            n += 1
        }
    }
    return n
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    // the file's text lives in sys.arena; sys.io reads it
    text := try fs.ReadText(sys.arena, sys.io, "notes.txt")
    n := Count(text) // Count takes no Io, so it cannot touch the OS
    try sys.Print("{n} lines\n", (n))
}
```

Because `Io` is a parameter, a test can pass a different one. `std.sim` provides a simulated `Io` with its own clock, files and network, and a run replays exactly from its seed. `Io` is also how tasks start: `io.Scope()` opens a [scope](../reference.md#12-concurrency).

`sys.Print` and `sys.PrintErr` write formatted text to standard output and standard error. Both are buffered ([standard streams](core.md#system-and-the-standard-streams)).

## 5. Errors

Each module exports one error set named `Error`, such as `fs.Error` or `net.Error`. A function's signature names the precise set it can fail with, which is often smaller.

```mk
// verdict: ok
import std.fs

// Size returns the length of a file, or none when it does not exist.
Size(a: &Arena, io: Io, path: str): fs.Error|OutOfMemory!?int {
    text := fs.ReadText(a, io, path) else e {
        match e {
            fs.NotFound {
                return none
            }
            _ {
                fail e // every other error goes to the caller
            }
        }
    }
    return some(text.Len())
}

export Main(sys: &System): fs.Error|OutOfMemory|WriteError!void {
    if n := try Size(sys.arena, sys.io, "notes.txt") {
        try sys.Print("{n} bytes\n", (n))
    } else {
        try sys.Print("no notes yet\n")
    }
}
```

The prelude holds the errors many modules share ([std.core](core.md#optionals-and-errors)).

A std error set may gain members in a later release, so a `match` on one outside std keeps a `_` arm ([open sets](../codebase.md#open-sets)).

A call that consumes a value, such as `Push`, `Insert`, `Set`, `Send` or `Box.New`, consumes it only on success. On `OutOfMemory`, `Closed` or `Canceled` you still own it.

A panic is not an error: it prints a report and ends the process ([reference section 9](../reference.md#9-errors)).

### Untrusted input

Every entry point that reads bytes from a peer or a file never panics on its input, and holds memory and stack bounded by its limits: `url.Parse`, the HTTP parsers, WebSocket frames, `Unzlib`, the zstd decoder, `Encoding.Decode`, base64 and hex decoding, the DER and PEM readers, the TZif reader, `ParseRfc3339`, `ParseHttpDate`, `ParseDuration`, `Term.Read`'s decoder, `csv.Records`, `big.Parse`, `x509.Parse`, `x509.ParsePem`, the TLS parsers, the QUIC wire parsers (`std.quic`) and key parsing.

## 6. Cancellation

Cancellation belongs to a scope. `s.Cancel()`, or the deadline of `s.CancelAfter(d)`, cancels the scope's body, every task in it and every scope nested inside it.

```mk
// verdict: ok
import std.time

Tick(io: Io, n: mut int) {
    for {
        io.Sleep(time.Millis(10)) else {
            return // Canceled: the scope's deadline has passed
        }
        *n += 1
    }
}

Count(io: Io): int {
    var n := 0
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(100))
        s.go Tick(io, mut n)
    }
    return n
}
```

An operation that can be cancelled has `Canceled` in its error set, so its signature shows it. The rules of a cancelled scope are in the reference: [cancellation](../reference.md#cancellation), [deadlines](../reference.md#deadlines) and [masking](../reference.md#masking-cancellation-nocancel). What std adds:

- A `Close` that only releases (a file, a socket, a pipe end, a child's handle, a mapping, a terminal) and every `Abandon` are not cancellable.
- A time limit in a module's options, such as `http.ClientLimits` or `tls.Config`, fails with `time.TimedOut` when it passes, never a bare `Canceled`, also when the operation is cancelled from outside once its limit has passed.
- What an interrupted operation leaves behind is its class, defined in [cancel-safety](core.md#cancel-safety). Each module's page gives the class of its calls.

## 7. Handles

A handle owns an OS resource or a peer's protocol state: `fs.File`, `fs.Mapping`, `fs.Walker`, `proc.Child`, `proc.PipeReader`, `proc.PipeWriter`, `proc.Pty`, `term.Term`, `net.Stream`, `net.Conn`, `http.Client`, `http.Exchange`, `http.Taken`, `tls.Conn`, `websocket.Conn` and `dl.Library`. A handle is linear, so the compiler checks that you end it.

- A handle keeps what its constructor's `from` clause names, and nothing else. `net.Conn`, `http.Client`, `tls.Conn` and `websocket.Conn` keep the `&Heap` given at creation, `fs.Walker` keeps its `&Heap` (an open directory and a name per level), and `http.Exchange` holds its arena's loan. The others hold only handles.
- A handle does not implement `Free`. Its consuming calls take no allocator and release everything it owns. Release a container of handles with `FreeEach`.
- A type whose `Close` can fail has an infallible `Abandon(self)` for error paths: `onerr f.Abandon()`.
- No consuming call has a run-time precondition: `Child.Wait(self)` and an encoder's `Finish(self)` are their own proof.

## 8. Module index

Each name links the module's page. Run `mk doc --std std.<module>` for one module's API, or `mk doc --std std` for the whole library. A module is Stable unless the table says otherwise ([stability](#9-stability)).

| Module | What it is for |
|---|---|
| [`std.core`](core.md) | The prelude, needed by every module and imported by none: memory, containers, text, formatting, `Io`, scopes, channels and the shared errors |
| [`std.atomic`](atomic.md) | Atomic integers and values, memory orders, fences, a spin lock |
| [`std.big`](big.md) | Arbitrary-precision integers |
| [`std.build`](build.md) | Compile time only: a file's bytes (`build.File`) and the build profile |
| [`std.cli`](cli.md) | Command-line arguments parsed into a struct: `gen cli.Args(T)` |
| [`std.coll`](coll.md) | More containers: `Deque`, `PriorityQueue`, `BTree`, `Bitset`, and their heap forms |
| [`std.compress`](compress.md) | Deflate, gzip and zlib, both ways; zstd decoding |
| [`std.crypto`](crypto.md) | HMAC, HKDF, ChaCha20-Poly1305, AES-GCM, X25519, Ed25519, ECDSA, RSA verification, Argon2id, a secure random stream, constant-time comparison |
| [`std.csv`](csv.md) | Reading and writing CSV records |
| [`std.db.sqlite`](sqlite.md) | SQLite through its C sources, behind a safe API. Needs the package to declare the C import and sources |
| [`std.derive`](derive.md) | Generators for `Eq`, `Hash` and `Format` |
| [`std.dl`](dl.md) | Loading shared libraries at run time. Provisional |
| [`std.dyn`](dyn.md) | `gen dyn.Of(I)`: a boxed value that dispatches through interface `I` at run time |
| [`std.encoding`](encoding.md) | Base64, hex, percent-encoding, PEM, DER, and the text encodings of the WHATWG Encoding Standard |
| [`std.encoding.cjk`](encoding.md#stdencodingcjk) | The Chinese, Japanese and Korean multi-byte encodings, linked only when named |
| [`std.free`](free.md) | The freestanding target's panic information and heap |
| [`std.fs`](fs.md) | Files, directories, metadata, walking a tree, memory-mapped files |
| [`std.hash`](hash.md) | XXH3, XXH64, CRC32, SipHash, SHA-1, SHA-2, BLAKE3, with streaming states |
| [`std.http`](http.md) | An HTTP/1.1 server and client, request routing |
| [`std.json`](json.md) | Parsing into a `Value`, and `gen json.Codec(T)` for typed reading and writing |
| [`std.libc`](libc.md) | C functions, written in Mk, that a package's own C sources link against. No Mk names |
| [`std.linux`](linux.md) | The raw system call, on x86_64-linux only. Unsafe only |
| [`std.log`](log.md) | A leveled logger, passed as a value |
| [`std.math`](math.md) | Math functions, checked and wrapping integer helpers, bit operations, a seeded random generator, float-to-digits |
| [`std.mem`](mem.md) | The `Fixed`, `FixedHeap`, `Fallback`, `Counting`, `Limit`, `Failing` and `Null` allocators, and `Pool` |
| [`std.mmio`](mmio.md) | Volatile access to device registers. Unsafe only, Provisional |
| [`std.net`](net.md) | TCP, UDP and Unix sockets, name resolution, addresses |
| [`std.proc`](proc.md) | Child processes with captured output, signals, pseudo-terminals, `Supervise` for worker processes that share a listening socket |
| `std.quic` | QUIC (RFC 9000, RFC 9001), the transport of HTTP/3. Experimental |
| [`std.raw`](raw.md) | Unsafe intrinsics. Only from modules listed in `unsafe_modules`; Provisional outside std |
| [`std.regex`](regex.md) | Linear-time regular expressions, and `gen regex.Matcher` to compile one at build time |
| [`std.runtime`](runtime.md) | The scheduler and the process: processor counts, task stacks, dedicated threads, the blocking pool, wakers, fault filters |
| [`std.sim`](sim.md) | The simulated `Io` for deterministic tests, with injected faults, recording and replay. Named `sim` because `test` is a keyword. Provisional |
| [`std.simd`](simd.md) | Vector types, CPU feature dispatch, and fast byte routines. Provisional |
| [`std.sync`](sync.md) | `RwLock`, `Select` over channels, `Shared`, `Semaphore`, `Notify`, `Barrier`, `Once` |
| [`std.term`](term.md) | Terminal control: key and mouse events, styles and colours, cursor movement, the alternate screen |
| [`std.testing`](testing.md) | Fuzz targets and benchmarks registered from `test` blocks |
| [`std.text`](text.md) | Unicode: graphemes, normalization, case folding, display width, word, sentence and line breaks, bidirectional text, character properties |
| [`std.time`](time.md) | Instants, durations, wall-clock time, calendar dates, time zones |
| [`std.time.tzdata`](time.md#stdtimetzdata) | The time-zone database, embedded, linked only when named |
| [`std.tls`](tls.md) | TLS client and server. Experimental |
| [`std.url`](url.md) | Parsing and building URLs, query strings, internationalized domain names |
| [`std.vm`](vm.md) | Reserving, committing and protecting memory pages; executable code regions. Unsafe only |
| [`std.websocket`](websocket.md) | WebSocket client and server |
| [`std.x509`](x509.md) | Certificate parsing and verification. Experimental |

The modules `std.core`, `std.rt`, `std.sys`, `std.task`, `std.target`, `std.simd.kernel`, `std.http.fields`, `std.http.h2`, `std.http.h3`, `std.sync.lead` and `std.net.pipe` are the library's own internals. A program cannot import them (E0202); what they export that you need is in the prelude or in `std.runtime`. `std.pkg` holds the types a `pkg.mk` manifest is written in, reached through the manifest's `pkg` name ([packages](../packages.md)).

### Reading an API section

Each page ends with its module's API, generated from the doc comments by `mk doc --std std.<module>`. Under each function, **Flows** says what the result borrows, and **Errors** lists every error the function's set can hold. The text below them says which errors the function returns and when, what it costs and what it allocates.

## 9. Stability

Every public item of the language, the tools and std has a stability level, which says how it may change from one release to the next. `mk query` and `mk doc` show an item's level, and `mk check` notes every package that imports an Experimental module, directly or through a dependency.

| Level | May change | Items |
|---|---|---|
| Stable | As the [release rules](#releases-before-10) below say | Language: the language of [reference](../reference.md) sections 1 to 16, the [memory model](../memory.md), the error codes, every field of `pkg.mk`. Library: everything not listed below. Tools: the `mk` commands and their flags |
| Provisional | In any minor release, with no deprecation first | Language: `asm` and `naked`; the `cpu` clause; the `mk lsp` surface; the kernel forms (`align(n)`, `section`, `extern var`, `general-regs-only`, the freestanding target, `MkEntry` and `MkPanicHook`). Library: `std.simd`, `std.sim`, `std.mmio`, `std.free`, `std.dl`, `std.raw` outside std, `proc.Pty`, `Command.terminal`, `net.Transport`, `net.NewConn`, `net.ConnSlot`, `net.ReserveConn`, `encoding.Codec`, `Encoding.Of`, `runtime.ForeignIo` with `IoTable`, `mem.Custom` with `AllocTable` |
| Experimental | In any minor release, and may be withdrawn | Library: `std.tls`, `std.x509`, `std.quic`. Target: `x86_64-nutshell` |

### Releases before 1.0

Mk is at 0.6.0. Until 1.0:

- A patch release (0.1.1) fixes bugs. It adds, removes and changes no public item.
- A minor release (0.2.0) may change or remove a Stable item. Each such change has an entry in [CHANGELOG.md](../../CHANGELOG.md) under that release and shows in `mk apidiff`. When a source form changes, an `mk fix` rewrite comes with it wherever the rewrite is certain. When the old and new forms can live side by side, the old one is deprecated first.
- Provisional and Experimental items may change in any minor release, with a CHANGELOG entry and no deprecation. An Experimental module may be withdrawn.
- The JSON of `mk check --json` carries a schema version, and a version only gains fields.

From 1.0, a minor release only adds to the Stable items: it never removes or changes a public fact. A Stable item that must change is deprecated for at least two minor releases and changes in the next major release, in [the steps any library follows](../codebase.md#7-stability-levels).

### A Stable module over an Experimental one

A level covers a module's own items. No Stable module imports an Experimental one: `std.http` and `std.websocket` speak TLS only through a `net.Secure` that the program makes with `tls.Connector` or `tls.Acceptor`. A program that uses `https`, `wss` or a TLS server therefore imports `std.tls` itself, and `mk check` notes it. The `secure` settings and `NoSecureTransport` are Stable. What happens over TLS, `https` and `wss` included, is Experimental as long as `std.tls` is.
