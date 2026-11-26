# std.dl

`std.dl` loads a shared library while the program runs and looks up the addresses of its exported functions. Use it for plugins, optional system libraries, or a library whose name is known only at run time. When the library is known when you build, link it in the manifest with `pkg.Link` instead ([packages](../packages.md#2-the-manifest)); the loader then opens it when the program starts. The module is [provisional](../codebase.md#7-stability-levels).

```mk
// verdict: parse
import std.dl
```

## Contents

- [The main pieces](#the-main-pieces)
- [Where Open looks](#where-open-looks)
- [Libraries and symbols](#libraries-and-symbols)
- [Present](#present)
- [Errors](#errors)
- [Linux](#linux)
- [Examples](#examples): [open and look up](#open-and-look-up), [keep a library loaded](#keep-a-library-loaded), [call a function](#call-a-function)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Load a library | `Open(io, name, search)`, which returns a `Library` |
| Find an exported function or variable | `Library.Symbol(name)` |
| Unload it | `Library.Close` |
| Keep it loaded until the process exits | `Library.Detach` |
| Ask whether an `extern` function can be called | `Present(io, f)` |

Loading and looking up are safe. Calling through an address that `Symbol` returns is `unsafe`, in a module listed in the manifest's `unsafe_modules`.

## Where Open looks

`Open` searches only where its `Search` says. It never looks in the current directory or in `PATH`, so a file planted there cannot be loaded by mistake.

| `Search` | Windows | Linux |
|---|---|---|
| `System` | A bare name, in System32 | A soname, in the dynamic loader's order, which honours `LD_LIBRARY_PATH` |
| `Application` | A bare name, in the executable's directory, then System32, for the library and its own dependencies | A name resolved against the directory of `/proc/self/exe` |
| `Path` | An absolute path. The library's own dependencies are looked up in its directory, then System32 | An absolute path |

A name of the wrong form for its `Search`, such as a name with a directory for `System` or a relative name for `Path`, is `BadName`. So is an empty name, one holding a NUL, and one over 1,024 UTF-16 units.

Windows looks for the DLLs a program links at start-up only among the KnownDLLs, in the executable's directory and in System32. A bin whose package graph links a DLL is built with the load configuration flag `DependentLoadFlags` 0xA00, so the current directory and `PATH` are never searched. Ship a third-party DLL beside the executable, or open it with `dl.Open` and `Search.Path`. A DLL linked with `kind: "lazy"` is loaded on the first call of one of its functions, with the same search (`Search.Application`'s flags); see [Present](#present).

## Libraries and symbols

A `Library` is linear: end it with `Close`, or with `Detach` to keep the library loaded for the life of the process. It holds one word, so it is `Send` and `Share` and may be used from any task. It owns an OS handle, so it does not implement `Free`: release a container of libraries with `FreeEach`.

- `Symbol(name)` gives the address of an exported function or variable, or `none`. It is also `none` for an empty name, one over 255 bytes and one holding a NUL.
- An address from `Symbol` is valid until `Close`. After `Detach` it stays valid.
- `Close` releases the library; the OS unloads it, running its detach code, when that was its last reference. Opening a library twice gives two references. `Close` only releases, so it never fails.
- `Open` and `Close` run the library's initializers or finalizers on the calling carrier and block it until they return. They cannot be cancelled.

To call a function, turn its address into a `fn` value of the right C signature with `raw.FnFromAddr` inside `unsafe`. The signature is yours to get right: nothing checks it. The call runs on the calling task's stack, not on the system stack that a call of an `extern` gets ([the C stack](../reference.md#functions-c-can-call-export-extern)), so call a library that uses much stack from a task spawned with `StackSize.Large`.

## Present

A package that links a DLL with `kind: "lazy"` ([linking libraries](../reference.md#linking-libraries)) calls its functions as `extern`s, but the DLL is not in the import table: the program starts on a machine that lacks it, and the first call loads it. `Present(io, f)` asks before calling:

- `f` names a bodiless `extern` function directly. Anything else, such as a local holding one, a function with a body or a call, is E0705 `reason: present-argument`. Naming `f` here needs no `unsafe`: `Present` never calls it.
- For a lazily bound `f` it loads the DLL if it is not loaded, as the first call would, and finds `f` without calling it. The initializer runs on the carrier's system stack and blocks the carrier, as `Open` does; so `Present`, like `Open`, takes `io`.
- For any other `extern` it is `true` when the program is built: the loader bound the function before `Main`.
- A failed load is not remembered: a later `Present` or call tries again.
- Calling a lazily bound function that is not present panics with `unresolved function`, naming the function and the DLL.

The program below names a newer Windows function. A lazily bound function of an optional DLL is asked the same way, and there the answer comes at run time:

```mk
// verdict: ok
import std.dl

// SetThreadDescription and GetCurrentThread are kernel32's.
extern SetThreadDescription(thread: uint, name: *u16): i32
extern GetCurrentThread(): uint

export Main(sys: &System): WriteError!void {
    var named := false
    if Target.os == Os.Windows {
        if dl.Present(sys.io, SetThreadDescription) {
            named = true // call it here, inside unsafe
        }
    }
    try sys.Print("can name threads: {named}\n", (named))
}
```

```text
$ mk run present.mk
can name threads: true
```

A package that binds Direct3D 12 lazily, so the program also runs where d3d12.dll is absent, writes `d3d12.def` with the names it uses (`D3D12CreateDevice`, one per line) and links it:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "viewer",
    bins: [pkg.Bin(name: "viewer", root: "src", subsystem: "console")],
    unsafe_modules: ["src"],
    links: [pkg.Link(name: "d3d12.dll", kind: "lazy", path: "d3d12.def", target: "x86_64-windows")],
)
```

Then `dl.Present(sys.io, D3D12CreateDevice)` picks the Direct3D path or the software one.

## Errors

`dl.Error` is the set of ways `Open` fails:

| Member | Meaning | Windows code |
|---|---|---|
| `NotFound` | No library of that name where the `Search` looks | 126 |
| `Invalid` | The file is not a library this program can load: another architecture, or not an executable image | 193 |
| `InitFailed` | The library's initializer failed | 1114 |
| `BadName` | The name has the wrong form for its `Search` | |
| `Unsupported` | This program cannot load libraries at run time | |
| `Os(code)` | Any other Windows error, with its code | |

On Linux, every failed load under `System` is `NotFound`. Under `Application` and `Path`, a failed load is `NotFound` when the file is absent and `Invalid` when it exists. `InitFailed` and `Os` occur only on Windows. `dl.Error` is open, so a `match` on it outside std keeps a `_` arm.

## Linux

`Open` fails with `Unsupported` unless the bin is built in [system-libc mode](libc.md#system-libc-mode-on-linux). A program that needs only `std.dl` turns the mode on by linking the C library itself, once per Linux target it builds for:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "plugins",
    bins: [pkg.Bin(name: "plugins", root: "src", subsystem: "console")],
    links: [
        pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "x86_64-linux"),
        pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "aarch64-linux"),
    ],
)
```

On Linux, `Open` calls `dlopen` with `RTLD_NOW | RTLD_LOCAL`: every symbol is bound at load, and none is visible to libraries opened later. What else system-libc mode changes is described in [std.libc](libc.md#system-libc-mode-on-linux).

## Examples

### Open and look up

```mk
// verdict: ok
import std.dl

// Try opens name, looks up two symbols, and says what happened.
Try(sys: &System, name: str, search: dl.Search): WriteError!void {
    match dl.Open(sys.io, name, search) {
        ok(lib) {
            found := lib.Symbol("MulDiv").IsSome()
            absent := lib.Symbol("NoSuchFunction").IsNone()
            lib.Close(sys.io) // never fails
            try sys.Print("{name}: open, MulDiv found: {found}, NoSuchFunction absent: {absent}\n", (name, found, absent))
        }
        err(e) { try sys.Print("{name}: {e}\n", (name, e)) }
    }
}

export Main(sys: &System): WriteError!void {
    try Try(sys, "kernel32.dll", dl.Search.System) // a bare name, looked up in System32
    try Try(sys, "no-such-library.dll", dl.Search.System)
    try Try(sys, "sub/kernel32.dll", dl.Search.System) // System takes no directory
    try Try(sys, "kernel32.dll", dl.Search.Path)       // Path needs an absolute path
}
```

```text
$ mk run open.mk
kernel32.dll: open, MulDiv found: true, NoSuchFunction absent: true
no-such-library.dll: NotFound
sub/kernel32.dll: BadName
kernel32.dll: BadName
```

- That is the output on Windows. A static Linux build prints `Unsupported` on every line.
- `lib.Close` comes before the `try`, because a failed print would otherwise leave the linear `Library` open, which the compiler rejects.

### Keep a library loaded

```mk
// verdict: ok
import std.dl

// Lookup loads name and keeps it loaded for the rest of the process,
// so the address it returns never goes dead: there is nothing to close.
Lookup(io: Io, name: str, symbol: str): dl.Error!?*u8 {
    lib := try dl.Open(io, name, dl.Search.System)
    p := lib.Symbol(symbol)
    lib.Detach() // ends the handle; the library stays loaded
    return p
}

export Main(sys: &System): dl.Error|WriteError!void {
    tick := try Lookup(sys.io, "kernel32.dll", "GetTickCount64")
    try sys.Print("GetTickCount64 found: {found}\n", (found: tick.IsSome()))
    missing := try Lookup(sys.io, "kernel32.dll", "NoSuchFunction")
    try sys.Print("NoSuchFunction found: {found}\n", (found: missing.IsSome()))
}
```

```text
$ mk run keep.mk
GetTickCount64 found: true
NoSuchFunction found: false
```

- After `Detach` the address stays valid until the process exits, so it can be kept in a global or handed to C.
- That is the output on Windows; a static Linux build fails with `Unsupported`.

### Call a function

This program uses `unsafe`, so it is the root module of a package whose `pkg.mk` lists it in `unsafe_modules`, run with `mk run .`.

```mk
// verdict: ok
import std.dl
import std.raw

// MulDiv is kernel32's int MulDiv(int a, int b, int c): a * b / c,
// rounded, with a 64-bit intermediate.
type MulDiv = fn(i32, i32, i32): i32

export Main(sys: &System): dl.Error|WriteError!void {
    lib := try dl.Open(sys.io, "kernel32.dll", dl.Search.System)
    p := lib.Symbol("MulDiv") else {
        lib.Close(sys.io)
        return
    }
    var r: i32 = 0
    unsafe "MulDiv has this C signature, and lib stays open until after the call" {
        f := raw.FnFromAddr[MulDiv](raw.Addr(p))
        r = f(1_000_000, 3_000, 7)
    }
    lib.Close(sys.io) // p is dead from here on
    try sys.Print("MulDiv(1000000, 3000, 7) = {r}\n", (r))
}
```

```text
$ mk run .
MulDiv(1000000, 3000, 7) = 428571429
```

- The cookbook's [Load a library at run time](../cookbook.md#load-a-library-at-run-time) does the same with `user32.dll`.

## API

<!-- api:begin -->
Stability: Provisional.

### `BadName`

```
export error BadName
```

The name has the wrong form for its Search: empty, holding a NUL, over
1,024 UTF-16 units, a separator or a colon in a bare name, or a
relative Path.

### `Error`

```
export open type Error = NotFound | Invalid | InitFailed | BadName | Unsupported | Os
```

Every way Open fails. The set is open, so a match on it outside std
keeps a `_` arm.

### `InitFailed`

```
export error InitFailed
```

The library's initializer failed (Windows error 1114). Linux does not
report it apart from the failures above.

### `Invalid`

```
export error Invalid
```

The file is not a library this program can load: another architecture,
or not an executable image. On Windows this is error 193. On Linux it
is a failed load of a file that exists, under Application and Path.

### `Library`

```
export linear struct Library
```

An open library. It is linear: end it with Close, or with Detach to
keep the library loaded for the life of the process. An address from
Symbol is dead after Close. It holds one word, so it is Send and Share.

### `Library.Close`

```
export Library.Close(self, io: Io)
```

Releases the library. The OS unloads it, running its detach code, when
this was its last reference. It never fails: a release the OS refuses
leaves the library loaded. It blocks the carrier and cannot be
cancelled. O(1) plus the loader's work; allocates nothing.

### `Library.Detach`

```
export Library.Detach(self)
```

Ends the handle and keeps the library loaded for the life of the
process, so addresses from Symbol stay valid. O(1); allocates nothing.

### `Library.Symbol`

```
export Library.Symbol(&self, name: str): ?*u8
```

The address of the exported function or variable name, or none. It is
also none for a name that is empty, over 255 bytes or holds a NUL. The
address is valid until Close; calling it is the caller's unsafe.
O(name) plus the lookup; allocates nothing.

### `NotFound`

```
export error NotFound
```

No library of that name where the Search looks. On Windows this is
error 126. On Linux it is every failed load under System, and a file
that is absent under Application and Path.

### `Open`

```
export Open(io: Io, name: str, search: Search): Error!Library
```

- Errors: Error (BadName, InitFailed, Invalid, NotFound, Os(code: i64), Unsupported)

Loads the library name where search says and returns it open. It runs
the library's initializers on the calling carrier and blocks it, and it
cannot be cancelled. Opening a library already loaded adds a reference,
and each Library releases its own. On Linux it calls dlopen with
RTLD_NOW | RTLD_LOCAL.

Fails with BadName for a name of the wrong form for search, NotFound,
Invalid or InitFailed as their docs say, Os for any other Windows
error, and Unsupported on Linux unless the bin is in system-libc mode.
O(name) plus the loader's work; allocates nothing.

### `Os`

```
export error Os(code: i64)
```

Any other failure, with the OS's own code (Windows only).

### `Present`

```
export Present[F](io: Io, f: F): bool
```

Present reports whether f, a bodiless extern function named directly
(`dl.Present(io, GetThing)`), can be called. For a function of a lazily
bound library (pkg.Link kind "lazy") it loads the library if it is not
loaded yet, running its initializers on the carrier's system stack and
blocking the carrier as Open does, and finds the function, without
calling it; a later call of f then costs one indirect call. For any
other extern it is true when the program is built, since the loader
resolved the function before Main. Naming f here needs no unsafe:
Present never calls it. A failed load is not remembered: asking again
loads again. Calling a lazily bound function that is not present panics
(code 17, unresolved function), so a program that may run without the
library asks first. Any other argument is E0705 reason
present-argument. O(1) plus the loader's work on the first ask;
allocates nothing.

### `Search`

```
export enum Search
```

Where Open looks for a library. It never looks in the current
directory or PATH.

Variants:

- `System`: On Windows a bare name, looked up in System32 only. On Linux a
  soname, looked up in ld.so's order, which honours LD_LIBRARY_PATH.
- `Application`: On Windows a bare name, looked up in the executable's directory, then
  System32, for the library and for its own dependencies. On Linux a
  name resolved against the directory of /proc/self/exe.
- `Path`: An absolute path, on both targets.

### `Unsupported`

```
export error Unsupported
```

This program cannot load libraries at run time: a Linux bin outside
system-libc mode, or a target with no loader.

<!-- api:end -->
