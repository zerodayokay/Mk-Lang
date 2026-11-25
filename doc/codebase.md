# Organising a codebase

This guide says how to lay out a program of many modules, so that two people put the same thing in the same place. Read it once you have written a few single-file programs. The [reference](reference.md) says what the language accepts, the [cookbook](cookbook.md) shows single programs, and [code style](style.md) says how each file is written.

## 1. Modules and directories

A package is a directory with a `pkg.mk` ([modules and build](reference.md#13-modules-and-build)). A module is a directory of `.mk` files under it, and its import path is that directory. So the directory tree is the module graph.

```text
notes/
  pkg.mk                    the manifest: bins, deps, C imports, unsafe modules, targets
  src/
    main/                   the bin root: Main and nothing else of weight
    notes/                  the domain: types, rules, its Error
    store/                  persistence behind a small API
    store/sqlite/           the one module that imports c.sqlite3, listed in unsafe_modules
    web/                    handlers and the route table
    os/windows/, os/linux/  one import name, os.native, a directory per target (pkg.Select)
  tests/                    whole-program tests with expected output, if any
```

| Rule | Why |
|---|---|
| One module per concept, named by what it holds (`store`, `web`), never by kind (`utils`, `types`, `helpers`) | An import line then says what a file depends on |
| The bin root holds `Main`, flag parsing and wiring. Domain modules never import it | A second bin or a test can reuse everything else |
| Imports point from the edges to the domain: `web` and `store` import `notes`, never the reverse. An import cycle is E0205 | The domain stays testable without I/O |
| `unsafe` code and C live in small leaf modules listed in `pkg.mk` (E1102), each with a safe API | Only those modules need reading for memory safety |
| A per-target difference is a module selected by `pkg.Select`, with the same API on every target (E0213), never an `if Target.os` spread through the code | `mk check --all-targets` compares the alternatives |
| `gen/` beside a module is a view of generated code. Do not commit it | The build cache holds the real output |

## 2. Errors

Each module exports one `Error`: a union of its own errors and the sets of the modules it passes errors on from, as `http.Error` holds `net.Error`. A function names the smallest set it can fail with, often one or two members. `try` checks that set against the caller's (E0801).

Error payloads are plain data with no references but static strings ([errors](reference.md#declaring-errors)). So an error carries a line number or an offset, or a fixed message such as a literal, never text that borrows from the input.

```mk
// verdict: ok
// a module's Error names its own errors and the sets it passes on
import std.fs

export error BadLine(line: int)
export type Error = BadLine | fs.Error | OutOfMemory

export Load(a: &Arena, io: Io, path: str): Error!List[str] from a {
    text := try fs.ReadText(a, io, path)
    var out := List[str].New(a)
    var n := 0
    for line in text.Lines() {
        n += 1
        if line.Len() == 0 {
            fail BadLine(line: n)
        }
        try out.Push(line)
    }
    return out
}
```

`Main`'s error set may name errors private to the bin. A library's exported signatures name only exported errors (E0412).

## 3. `Ctx` structs

A `Ctx` holds the capabilities a call tree needs and does not change: the request arena, the `Io`, the `&Heap`, a configuration view. Pass it as `&Ctx`. Pass state that changes beside it as an explicit `mut` parameter, so the signature still says what a function writes. A result built in the arena says `from c.arena`.

```mk
// verdict: ok
// a Ctx carries capabilities; state that changes is a separate mut parameter
struct Ctx {
    arena: &Arena
    io: Io
}

struct Stats {
    requests: int
}

Handle(c: &Ctx, stats: mut Stats, name: str): OutOfMemory!str from c.arena {
    stats.requests += 1
    return try c.arena.Fmt("hello {name}", (name))
}
```

| Belongs in a `Ctx` | Does not |
|---|---|
| `&Arena`, `Io`, `&Heap`, `&Alloc` | Counters, caches, connections: pass them `mut` |
| A `&Config` read by everyone | A `Writer` or `Reader`: they are unique, so they cannot be used through `&` |
| What one request's handlers share | Anything one handler alone uses: pass it to that handler |

A `Ctx` holding `&Arena` is not `Share`, because no `&Arena` crosses tasks ([concurrency](reference.md#12-concurrency)). Each task builds its own `Ctx` from the `&Heap` it is given.

State reached through a field of the `Ctx` is part of what the `Ctx` refers to. A store into it, here a value allocated with `c.a` put into the shared map, needs a clause on `c`, `*c into c`, and that clause ties every later use of the state to the `Ctx`. E0604 names the field the store lands in:

```mk
// verdict: reject E0604
struct Shared {
    m: Mutex[TextMap[HeapText]]
}

struct Ctx {
    st: &Shared
    a: &Alloc
    io: Io
}

Put(c: &Ctx, key: str, value: str): Canceled|OutOfMemory!void {
    var g := try c.st.m.Lock(c.io)
    defer g.Unlock()
    var v := HeapText.New(c.a)
    onerr v.Free(c.a)
    try v.Push(c.a, value)
    try g.Mut().Set(c.a, key, v) // <- E0604
}
```

Pass the state beside the `Ctx` and declare the store into it, `*c into st`. The caller can then take the state apart once the call is over:

```mk
// verdict: ok
struct Shared {
    m: Mutex[TextMap[HeapText]]
}

struct Ctx {
    a: &Alloc
    io: Io
}

Put(c: &Ctx, st: &Shared, key: str, value: str): Canceled|OutOfMemory!void *c into st {
    var g := try st.m.Lock(c.io)
    defer g.Unlock()
    var v := HeapText.New(c.a)
    onerr v.Free(c.a)
    try v.Push(c.a, value)
    try g.Mut().Set(c.a, key, v)
}

export Main(sys: &System) {
    a := sys.heap.Alloc()
    st := Shared(m: Mutex.New(TextMap[HeapText].New(a)))
    c := Ctx(a: a, io: sys.io)
    Put(&c, &st, "k", "v") else { }
    Shared(m) := st
    m.Into().Free(a)
}
```

## 4. Allocators in a library module

A library function never chooses its caller's memory. It takes the arena its result lives in as `a: &Arena`, after the receiver and before `io`, and its result says `from a`. A heap container takes `a: &Alloc` and is linear. Scratch memory comes from `a.Sub()` and is freed before return, so the caller's arena holds only the result.

```mk
// verdict: ok
// the result lives in the caller's arena; scratch lives in a sub-arena
export Words(a: &Arena, text: str into a): OutOfMemory!List[str] from a, text {
    var out := List[str].New(a)
    for w in text.Words() {
        try out.Push(w)
    }
    return out
}

export Longest(a: &Arena, text: str): OutOfMemory!int {
    var scratch := try a.Sub()
    defer scratch.Free()
    words := try Words(&scratch, text)
    var best := 0
    for w in words {
        if w.Len() > best {
            best = w.Len()
        }
    }
    return best
}
```

Only a bin decides lifetimes:

- `sys.arena` for data that lives as long as the program;
- one arena per request or per job, reset or freed when it ends;
- `sys.heap` for what outlives any one request or job.

A library that allocates in `sys.arena` itself makes a server grow without bound.

## 5. Generating at a concrete site

A generic body cannot run a generator on `T` ([compile time](reference.md#11-compile-time)). So the module that owns a type writes the `gen` line once, at the concrete type. Generic code bounds on the interface the generator implements: `derive.Eq` implements `Eq`, and `json.Codec` implements `json.Json`.

```mk
// verdict: ok
// gen at the concrete type; generic code bounds on what it implements
import std.json

struct User {
    export name: str
    export age: int
}

gen json.Codec(User)

Emit[T: json.Json](w: mut Writer, x: &T): WriteError!void {
    try x.FormatJson(w)
    try w.Str("\n")
}

EmitUser(w: mut Writer, u: &User): WriteError!void {
    try Emit(w, u)
}
```

A generated function that is not a method of an interface, such as `User.FromJson`, is called at concrete sites only.

## 6. Tests beside code

Tests live in the module they test, as `test` blocks in the file or in a `tests_<topic>.mk` file in the same directory. So they see the module's private items.

- `mk test` runs each block in a child process.
- A test that declares `(sys: &System)` gets a heap that reports leaks.
- `mk test --prop` checks a function's `requires` and `ensures` on generated inputs.
- A test over `std.sim` replays exactly from its seed.

Whole programs with expected output, if a package has them, go under `tests/`.

```mk
// verdict: ok
// a test beside the code it tests
Median(xs: mut []i32): ?i32 {
    if xs.Len() == 0 {
        return none
    }
    xs.Sort(fn(a: &i32, b: &i32): bool { return *a < *b })
    return some(xs[xs.Len() / 2])
}

test "median of three" {
    var xs: [3]i32 = [3, 1, 2]
    Assert(Median(mut xs[..]).Must() == 2)
}
```

## 7. Stability levels

Mk's own levels, for the language, the tools and the standard library, and what a release of Mk may change before and after 1.0, are in [stability](std/README.md#9-stability). A library of your own changes a stable item in these steps.

Before a stable item is removed or changed, it is deprecated for at least two minor releases. Its doc comment carries the line `/// Deprecated: <why>; use <what>.`, and `mk check` reports every use with a note naming the replacement. The change then comes with an `mk apidiff` entry, and with an `mk fix` rewrite when a source form changes.

There is no binary interface between Mk packages: a dependency is source, built by the same compiler. The JSON of `mk check --json` carries a schema version, and a version only gains fields.

## 8. Growing a library without breaking its users

A library grows by adding items, error members and enum variants. Three conventions let it do that without breaking the programs that use it.

### Options types

An options type holds the settings of one call: `fs.Options`, `net.ListenOptions`, `http.ClientLimits`, `http.ServerOptions`, `proc.Command`, `tls.Config`, `websocket.Options`, `csv.Options`. Each has a constant named `Default` plus the type's name, such as `fs.DefaultOptions`, and one unexported field. So a literal of the type cannot be written outside std. You copy the default and assign the fields you need. A field added later breaks no program, and `mk apidiff` reports it as an addition.

### Open sets

An `open` enum or error set may gain members in a later release. Outside the package that declares it, a `match` on it needs a `_` or binding arm, else E0306 `reason: open`.

```mk
// verdict: reject E0306
// fs.Kind is open, so a match outside std covers what a later release adds
import std.fs

Name(k: fs.Kind): str {
    match k { // <- E0306
        .File {
            return "file"
        }
        .Dir {
            return "dir"
        }
        .Link {
            return "link"
        }
        .Other {
            return "other"
        }
    }
}
```

```mk
// verdict: ok
// the _ arm covers members a later release adds; inside its own package, an open set matches exhaustively
import std.fs

open enum Shape {
    Circle
    Square
}

Sides(s: Shape): int {
    return match s {
        .Circle { 0 }
        .Square { 4 }
    }
}

Name(k: fs.Kind): str {
    return match k {
        .File { "file" }
        .Dir { "dir" }
        _ { "other" }
    }
}

Code(r: fs.Error!int): int {
    return match r {
        ok(n) { n }
        err(fs.NotFound) { 1 }
        err(_) { 2 }
    }
}
```

The rules:

- Write `open` before `enum`, or before `type` for a non-generic alias of an error set. On any other alias it is E0706.
- A set's package is every module whose path shares the declaring module's first segment: `std` for std, a dependency's name, or a top directory of this package. Inside it, a `match` is exhaustive as written, so it fails when a member is added and you see every place to update.
- Write `open` only in the package that declares every member of the set, else E0706 `reason: open-foreign`. The alias must also be in a module that declares at least one of its members, else E0706 `reason: open-away`.
- Error sets are structural. A set is open when it holds every member of an exported open alias. So `fs.Error | ParseError` is open too, since `fs.Error`'s growth reaches it.
- A library's own set that holds another package's error cannot be marked open, so a member it adds to that set is a breaking change.
- `mk fix` adds the missing arm, `_ { Panic("fs.Kind gained a member this match does not handle") }`. Every current member has its own arm before it, so the panic runs only for a member a later release adds.
- Adding a member to an open set is not breaking, and `mk apidiff` reports it as an addition. Marking an existing set `open` is breaking, since every exhaustive match outside its package then needs the arm.

In std, every module's `Error` union and each set it names is open, and so are the enums `proc.Signal`, `fs.Kind`, `term.Key`, `net.Shutdown`, `net.SecureFailure`, `crypto.Curve` and `crypto.HashKind`. The prelude's `Arch` and `Os` are open too, because they grow as targets are added. So a `match` on `Target.arch` or `Target.os` keeps a `_` arm. Every other std enum is closed.

### Deprecation

Rename or remove through a deprecation, as [stability levels](#7-stability-levels) describes, rather than in one step. Your users then get a release in which both forms compile and `mk check` names the replacement.
