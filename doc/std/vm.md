# std.vm

`std.vm` reserves, commits and protects address space by the page, and makes memory for generated code. It is for programs that manage memory below the level of an allocator: a JIT, a garbage collector's heap, a sandbox's cage, an arena that grows in place. Everything else should use the [allocators](README.md#3-memory-allocators-and-arenas).

Every function and method of `std.vm` is callable only in `unsafe` (E1101). A `Region` hands out raw addresses whose pages may be inaccessible, and a `CodeRegion` runs whatever bytes it is given. A module that uses it needs permission in its manifest's `unsafe_modules` ([packages](../packages.md#2-the-manifest)).

```mk
// verdict: parse
import std.vm
```

## Contents

- [The main pieces](#the-main-pieces)
- [Regions](#regions)
- [Code regions](#code-regions)
- [Errors](#errors)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [reserve and commit](#reserve-and-commit), [generate and run code](#generate-and-run-code), [grow a code space](#grow-a-code-space)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Find the page size | `PageSize` |
| Reserve address space | `Reserve`, which returns a `Region` |
| Make pages usable, or drop them | `Region.Commit`, `Region.Decommit` |
| Make pages read-only or inaccessible | `Region.Protect` with a `Prot` |
| Give the space back | `Region.Release` |
| Memory for generated code | `CodeRegion.New` (committed) or `CodeRegion.Reserve` (committed by the page) |
| Write code, then run it | `CodeRegion.Edit`, `CodeRegion.EditRange` -> `CodeWriter` (`Bytes`, `Flush`, `Done`), then `CodeRegion.Entry` |

## Regions

A `Region` is reserved address space: a range of addresses that nothing else in the process will use, with no memory behind it yet. Reserving costs no memory, so a program can reserve gigabytes and commit only what it touches.

- `Commit(offset, len)` makes pages read-write. A newly committed page reads as zero and counts against the OS's commit limit. Pages already committed keep their contents and protection.
- `Decommit(offset, len)` drops the pages' contents and their commit charge. The addresses stay reserved, and an access faults until the next `Commit`.
- `Protect(offset, len, p)` sets committed pages to `None`, `Read` or `ReadWrite`. A page that is not committed is `Os(487)` on every target.
- `Prot` has no executable value: no page of a `Region` can run code.
- Offsets and lengths are whole pages inside the region, else `Misaligned`; for `Decommit`, which cannot fail, it is a panic with code 2.
- A `Region` is linear: end it with `Release`, which gives back the whole range.

Two tasks that commit overlapping ranges at once must exclude each other themselves.

## Code regions

A `CodeRegion` is memory for code that the program generates, enforcing W^X on every target: no page is writable and executable at once, and the region stays sealed (not writable) outside an active `CodeWriter` returned by `Edit`.

1. Open an exclusive write window with `var w := try c.Edit()` (or `try c.EditRange(offset, len)` for a single page-aligned range).
2. Write the bytes into `w.Bytes()`.
3. Call `w.Flush(offset, len)`, the instruction-cache flush.
4. Close the window with `w.Done()` (or `defer w.Done()`), re-sealing the region.
5. Turn `c.Entry(offset)`, the executable address, into a `fn` value with `raw.FnFromAddr[F](...)`, and call it.

- `CodeWriter` is linear and borrows `CodeRegion` mutably (`mut self`), so the compiler prevents forgetting `Done` (E0402), prevents holding `w.Bytes()` past `Done` (E0503), and prevents calling `Entry`, `Commit`, `Edit` or `Release` while a write window is open.
- Do not hold a `CodeWriter` across an `Io` wait or task suspension: on arm64 macOS JIT write protection belongs to the calling OS thread, so resuming on another carrier would leave the original carrier write-enabled.
- `CodeRegion.New(size)` commits every page in the sealed state. `CodeRegion.Reserve(size)` commits none, and `Commit(offset, len)` takes pages in the sealed state as code grows. A write to a page not committed faults, and so does a read or call into one (a call faults as a fault in a code region).
- A fault in a code region goes to the fault filters of [std.runtime](runtime.md#fault-filters), never to Mk's report of a fault in Mk code. The runtime tracks code regions for this in a table of 64, so `New` and `Reserve` fail with `Unsupported` while 64 are live.
- `Entry` panics with code 2 for an offset outside `[0, Size())`.
- On Windows a code region is a pagefile-backed section mapped twice (the writable view read-only outside `Edit`, the executable view read-execute throughout); on Linux a memfd mapped twice the same way; on arm64 macOS a `MAP_JIT` mapping whose write-vs-execute mode switches per thread through `pthread_jit_write_protect_np` when the host supports it (other threads stay in execute mode while one thread edits), falling back to a private anonymous mapping toggled between read-write during `Edit` and read-execute outside it via `mprotect` (as on x86_64 macOS) when per-thread JIT write protection is unavailable or `MAP_JIT` is refused by policy.

## Errors

`vm.Error` has four members:

| Member | Meaning |
|---|---|
| `Misaligned` | An offset, length or size is not a whole number of pages, or is outside the region |
| `OutOfMemory` | The OS has no such range, or refuses the commit charge |
| `Unsupported` | This target cannot do it, or 64 code regions are live |
| `Os(code)` | Any other refusal, with the OS's code (`GetLastError` on Windows, `errno` on Linux and macOS) |

A `Commit` that fails part way leaves the pages it committed before the failure committed.

## Memory and cost

`std.vm` never allocates from a Mk allocator. A `Region` keeps one bit per page in a bitmap of its own, a second reservation rounded up to a page. That is one page for up to 128 MiB of 4 KiB pages, 1 MiB for 32 GiB. On Linux the reservation is made with `MAP_NORESERVE`, so even a 32 GiB reservation costs no commit charge, and the bitmap is not charged either. On Windows the bitmap is charged.

`Reserve` and `Release` are a few system calls. `Commit`, `Decommit` and `Protect` cost one system call per run of pages they change, plus a scan of the bitmap at 64 pages per word. `CodeRegion.Edit` and `CodeWriter.Done` are O(1) on arm64 macOS when `MAP_JIT` per-thread write protection is active; on Windows, Linux, x86_64 macOS and arm64 macOS fallback they cost one system call per run of committed pages (one in all for `CodeRegion.New`) plus the bitmap scan.

## Examples

These programs use `unsafe`, so they run as the root module of a package whose `pkg.mk` lists it, for example:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "demo",
    bins: [pkg.Bin(name: "demo", root: "src", subsystem: "console")],
    unsafe_modules: ["src"],
)
```

with the program in `src/main.mk`, run with `mk run .`.

### Reserve and commit

```mk
// verdict: ok
import std.raw
import std.vm

export Main(sys: &System): vm.Error|WriteError!void {
    unsafe "every address used below is inside r and committed read-write" {
        page := vm.PageSize()
        // Reserve 1 GiB of address space. No page is committed yet, so this
        // costs no memory.
        var r := try vm.Reserve(1 << 30, page)
        onerr r.Release()
        try r.Commit(0, page) // the first page: read-write, all zeros
        p := raw.FromAddr[u64](r.Base())
        try sys.Print("fresh page reads {v}\n", (v: raw.Read(p)))
        raw.Write(p, 1234)
        try sys.Print("after a write: {v}\n", (v: raw.Read(p)))
        r.Decommit(0, page) // contents and charge dropped, no access
        try r.Commit(0, page)
        try sys.Print("recommitted page reads {v}\n", (v: raw.Read(p)))
        match r.Protect(page, page, vm.Prot.Read) { // the second page is not committed
            ok(_) {}
            err(e) { try sys.Print("Protect of an uncommitted page: {e}\n", (e)) }
        }
        r.Release()
    }
}
```

```text
$ mk run .
fresh page reads 0
after a write: 1234
recommitted page reads 0
Protect of an uncommitted page: Os(code: 487)
```

- `raw.FromAddr` turns an address into a pointer; reads and writes through it are the `unsafe` block's to justify.
- `onerr r.Release()` gives the space back if a later line fails, which the compiler requires of a linear `Region`.

### Generate and run code

```mk
// verdict: ok
import std.raw
import std.vm

// x86-64 machine code for a function that returns n: mov eax, n; ret.
WriteReturn(w: mut []u8, at: int, n: u8) {
    code: [6]u8 = [0xB8, n, 0, 0, 0, 0xC3]
    (mut w[at..at + 6]).CopyFrom(code[..])
}

// EmitReturn opens an exclusive write window, writes and flushes a function,
// and re-seals the region on exit via defer w.Done().
EmitReturn(c: mut vm.CodeRegion, at: int, n: u8): vm.Error!void {
    unsafe "at..at + 6 lies inside the committed code region" {
        var w := try c.Edit()
        defer w.Done()
        WriteReturn(w.Bytes(), at, n)
        w.Flush(at, 6)
    }
}

export Main(sys: &System): vm.Error|WriteError!void {
    unsafe "the region holds whole functions at offsets 0 and 64, flushed before each call" {
        var c := try vm.CodeRegion.New(4096) // committed whole, sealed
        onerr c.Release()
        try EmitReturn(mut c, 0, 42)
        f := raw.FnFromAddr[fn(): u32](c.Entry(0)) // run through the executable view
        try sys.Print("f() = {v}\n", (v: f()))

        try EmitReturn(mut c, 64, 7) // open a second write window for a second function
        g := raw.FnFromAddr[fn(): u32](c.Entry(64))
        try sys.Print("g() = {v}\n", (v: g()))
        c.Release()
    }
}
```

```text
$ mk run .
f() = 42
g() = 7
```

- The bytes are x86-64 machine code.
- A `fn` value made by `raw.FnFromAddr` is dead after `Release`.
- [Resume from a fault](runtime.md#resume-from-a-fault) uses a code region with a fault filter.

### Grow a code space

```mk
// verdict: ok
import std.raw
import std.vm

// x86-64 machine code for a function that returns n: mov eax, n; ret.
WriteReturn(w: mut []u8, at: int, n: u8) {
    code: [6]u8 = [0xB8, n, 0, 0, 0, 0xC3]
    (mut w[at..at + 6]).CopyFrom(code[..])
}

export Main(sys: &System): vm.Error|WriteError!void {
    unsafe "each function is written into a committed page and flushed before its call" {
        page := vm.PageSize()
        // Room for 1,024 pages of code, none committed: a JIT's code space.
        var c := try vm.CodeRegion.Reserve(1024 * page)
        onerr c.Release()
        try sys.Print("reserved {n} pages\n", (n: c.Size() / page))
        var next := 0 // the first page not yet used
        for n in [3, 5, 8] {
            try c.Commit(next, page) // one more page, sealed, as the code grows
            var w := try c.Edit()
            WriteReturn(w.Bytes(), next, Trunc[u8](n))
            w.Flush(next, 6)
            w.Done()
            f := raw.FnFromAddr[fn(): u32](c.Entry(next))
            try sys.Print("page {p}: f() = {v}\n", (p: next / page, v: f()))
            next += page
        }
        c.Release()
    }
}
```

```text
$ mk run .
reserved 1024 pages
page 0: f() = 3
page 1: f() = 5
page 2: f() = 8
```

- `CodeRegion.Reserve` commits nothing, so reserving 1,024 pages costs no memory. Each `Commit` adds pages that read as zero.
- A call into a page that is not committed faults, as a fault in a code region.

## API

<!-- api:begin -->
Stability: Stable.

### `CodeRegion`

```
export linear struct CodeRegion
```

Memory for generated code, enforcing W^X on every target: no page is
writable and executable at once, and the region stays sealed (not
writable) outside an active CodeWriter from Edit. To emit or patch code,
call Edit to open a linear CodeWriter, write through CodeWriter.Bytes,
flush the instruction cache with CodeWriter.Flush, close the window with
CodeWriter.Done, and run at Entry.

On Windows it is a pagefile-backed section mapped twice (a writable view
kept read-only outside Edit, and a read-execute view untouched
throughout); on Linux a memfd mapped twice the same way; on arm64 macOS a
MAP_JIT mapping whose write-vs-execute mode switches per thread through
pthread_jit_write_protect_np when the host supports it, falling back to a
private anonymous mapping toggled between read-write during Edit and
read-execute outside it via mprotect (as on x86_64 macOS) when it does not.

A region from New is committed whole; one from Reserve has no page
committed until Commit takes it. Linear: end it with Release.

### `CodeRegion.Commit`

```
export CodeRegion.Commit(mut self, offset: int, len: int): Error!void
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Commits [offset, offset + len) of a region from Reserve. Its pages not
yet committed read as zero and become sealed (non-writable until Edit)
and executable at Entry. Pages already committed keep their contents and
protection. On a region from New, whose pages are all committed, it only
checks the range. Taking mut self prevents Commit from racing with Edit
or Entry.

Fails with Misaligned for a range that is not whole pages inside the
region, and Os when the OS refuses; the runs committed before it stay
committed. O(len / (64 pages)) bitmap words, plus one system call per
run of uncommitted pages (two on Linux); allocates nothing.

### `CodeRegion.Edit`

```
export CodeRegion.Edit(mut self): Error!CodeWriter from self
```

- Flows: The result borrows from self.
- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Opens an exclusive write window into the region, returning a linear
CodeWriter borrowing self. On Windows and Linux every committed page of
the writable view becomes read-write while the executable view stays
read-execute; on arm64 macOS with per-thread JIT write protection the
calling thread switches to JIT write mode (pthread_jit_write_protect_np(0))
while other threads remain in execute mode; on x86_64 macOS and arm64 macOS
fallback every committed page switches from read-execute to read-write.

Fails with Os when the OS refuses; any pages already made writable before
the refusal are sealed again as far as the OS allows and the region stays
sealed. O(1) on arm64 macOS with MAP_JIT; on Windows, Linux, x86_64 macOS
and arm64 macOS fallback O(Size / (64 pages)) bitmap words plus one system
call per run of committed pages (one in all for a region from New);
allocates nothing.

### `CodeRegion.EditRange`

```
export CodeRegion.EditRange(mut self, offset: int, len: int): Error!CodeWriter from self
```

- Flows: The result borrows from self.
- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Opens an exclusive write window covering only `[offset, offset + len)`,
returning a linear CodeWriter borrowing self. Both offset and len must be
whole multiples of PageSize() within `[0, Size()]`, and every page in
`[offset, offset + len)` must already be committed. `len == 0` at a valid
page-aligned offset is a clean no-op that touches no page protections.

Unlike Edit, only the pages in `[offset, offset + len)` become writable on
targets with per-page mappings, and Done seals only that range;
`CodeWriter.Bytes()` returns a `len`-byte slice whose index 0 maps to
`offset` in the region, and `CodeWriter.Flush(rel_off, rel_len)` flushes
relative to `offset`. On arm64 macOS with per-thread MAP_JIT write
protection, JIT write mode is still per calling thread.

Fails with Misaligned when offset or len is negative, not a multiple of
PageSize(), or outside the region; fails with Os(487) when any page in
`[offset, offset + len)` of a reserved region is uncommitted; fails with Os
when the OS refuses protection. O(1) system calls; allocates nothing.

### `CodeRegion.Entry`

```
export CodeRegion.Entry(&self, offset: int): uint
```

The executable address of the byte at offset, to pass to
raw.FnFromAddr, which makes the fn value. An offset outside
[0, Size()) panics with the bounds code (2). O(1); allocates nothing.

### `CodeRegion.New`

```
export CodeRegion.New(size: int): Error!CodeRegion
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

A code region of size bytes, rounded up to whole pages, committed whole
and sealed (not writable until Edit).

Fails with Misaligned for a size that is not positive, Os when the OS
refuses (Os(38), ENOSYS, on x86_64-nutshell), and Unsupported while 64
code regions are live: a fault in a 65th could not reach
runtime.AddFaultFilter's filters. O(1) system calls, and a scan of the
64 region slots; allocates nothing on the heap.

### `CodeRegion.Release`

```
export CodeRegion.Release(self)
```

Unmaps the region's view(s), and gives back a reserved region's commit
bitmap. O(1) system calls; allocates nothing.

### `CodeRegion.Reserve`

```
export CodeRegion.Reserve(size: int): Error!CodeRegion
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

A code region of size bytes, rounded up to whole pages, reserved with
no page committed. A page can be written (inside Edit) and run only once
Commit takes it. A write to a page not committed faults, and so does a
read or call into one (a call into one faults as a fault in a code
region, which goes to runtime.AddFaultFilter's filters). On Windows it is
a section made SEC_RESERVE; on Linux a memfd with both views mapped with
no access; on macOS a mapping with PROT_NONE (MAP_JIT on arm64).

Fails as New does, and with OutOfMemory when its commit bitmap (one bit
per page rounded up to a page, as a Region's) cannot be made. O(1)
system calls, and a scan of the 64 region slots; allocates nothing on
the heap.

### `CodeRegion.Size`

```
export CodeRegion.Size(&self): int
```

The region's size in bytes, a whole number of pages. O(1); allocates
nothing.

### `CodeWriter`

```
export linear view struct CodeWriter
```

An open exclusive write window into a CodeRegion, returned by
CodeRegion.Edit. It borrows the CodeRegion mutably while live, so no
concurrent Edit, Commit, Entry or Release can touch the region until Done
closes the window. Write code through Bytes, flush the instruction cache
with Flush, and end the window with Done.

Linear: every path must close it with Done (use `defer w.Done()`). Do not
hold a CodeWriter across an Io wait or task suspension: on arm64 macOS
JIT write protection belongs to the calling OS thread, so resuming on
another carrier would leave the original carrier write-enabled.

### `CodeWriter.Bytes`

```
export CodeWriter.Bytes(mut self): mut []u8 from self
```

- Flows: The result borrows from self.

The region's writable bytes while this CodeWriter lives. What they hold
is seen through the executable view after Flush and Done. A write to a
page that a region from Reserve has not committed faults. O(1); allocates
nothing.

### `CodeWriter.Done`

```
export CodeWriter.Done(self)
```

Closes the write window and seals the region again: every committed page
of the writable view (or only `[off, off + len)` for a writer opened by
EditRange with `len > 0`) becomes read-only on Windows and Linux; on
x86_64 macOS and arm64 macOS fallback the affected committed pages return
to read-execute; on arm64 macOS with MAP_JIT the calling thread's JIT
write protection is re-enabled (pthread_jit_write_protect_np(1) when no
nested window remains).

Infallible so `defer w.Done()` works on every path; if the OS refuses a
page protection change, Done still attempts every remaining committed
run. O(1) on arm64 macOS with MAP_JIT; on Windows, Linux, x86_64 macOS
and arm64 macOS fallback O(Size / (64 pages)) bitmap words for Edit or
O(1) system call for EditRange; allocates nothing.

### `CodeWriter.Flush`

```
export CodeWriter.Flush(&self, offset: int, len: int)
```

Makes the bytes written at [offset, offset + len) visible to execution:
FlushInstructionCache on Windows, sys_icache_invalidate on arm64 macOS,
data-cache clean and instruction-cache invalidate on arm64 Linux, and a
fence on x86_64 Linux and macOS, whose caches are coherent. Call it after
writing code and before Done. For an EditRange writer, offset is relative
to the edited range. A range outside the writer window is clipped to it;
on arm64 only committed pages may be flushed. O(len / cache line) on arm64,
O(1) on x86_64; allocates nothing.

### `Error`

```
export type Error = OutOfMemory | Misaligned | Unsupported | Os
```

Every way a std.vm operation fails.

### `Misaligned`

```
export error Misaligned
```

An offset, length or size is not a whole number of pages, or is
outside the region.

### `Os`

```
export error Os(code: i64)
```

The OS refused, with its code (GetLastError on Windows, errno on Linux
and macOS).

### `PageSize`

```
export PageSize(): int
```

The OS's page size in bytes. Every offset and length a Region takes is
a multiple of it. O(1); allocates nothing.

### `Prot`

```
export enum Prot
```

A page protection for Region.Protect. There is no executable value: no
page is both writable and executable (W^X), and code runs only from a
CodeRegion's executable view.

Variants:

- `None`: No access.
- `Read`: Read only.
- `ReadWrite`: Read and write.

### `Region`

```
export linear struct Region
```

Reserved address space: size bytes from Base, with no access until
Commit. It is linear: end it with Release, which gives back the whole
range.

### `Region.Base`

```
export Region.Base(&self): uint
```

The first address of the region, which a program reads and writes
through once it has committed pages. O(1); allocates nothing.

### `Region.Commit`

```
export Region.Commit(&self, offset: int, len: int): Error!void
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Commits [offset, offset + len). Its uncommitted pages become read-write
and read as zero, charged against the OS's commit limit. Pages already
committed keep their contents and protection, on every target.
Concurrent calls on overlapping ranges are the caller's to exclude.

Fails with OutOfMemory when the OS refuses the charge, and the runs of
pages committed before it stay committed; with Misaligned for a range
that is not whole pages inside the region. O(len / (64 pages)) bitmap
words, plus one system call per run of uncommitted pages; allocates
nothing.

### `Region.Decommit`

```
export Region.Decommit(&self, offset: int, len: int)
```

Drops the contents and the commit charge of [offset, offset + len). The
address space stays reserved, with no access. A range that is not whole
pages inside the region panics with the bounds code (2). One system
call, plus one bitmap read-modify-write per 64 pages; allocates
nothing. O(len / (64 pages)).

### `Region.Protect`

```
export Region.Protect(&self, offset: int, len: int, p: Prot): Error!void
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Sets the protection of the committed pages [offset, offset + len) to
None (any access faults), Read or ReadWrite; never executable.

Fails with Os(487) when a page in the range is not committed, on every
target: 487 is Windows' ERROR_INVALID_ADDRESS, and std gives the same
code on Linux. Fails with Misaligned for a range that is not whole
pages inside the region. One bitmap load per 64 pages and one system
call; allocates nothing. O(len / (64 pages)).

### `Region.Release`

```
export Region.Release(self)
```

Gives back the whole reservation, committed pages included, and the
commit bitmap's. O(1): two system calls; allocates nothing.

### `Region.Size`

```
export Region.Size(&self): int
```

The region's size in bytes, a whole number of pages. O(1); allocates
nothing.

### `Reserve`

```
export Reserve(size: int, align: int): Error!Region
```

- Errors: Error (OutOfMemory, Misaligned, Os(code: i64), Unsupported)

Reserves size bytes of address space, a whole number of pages, at an
address that is a multiple of align. align is a power of two; an align
below a page acts as a page. No page is committed: on Linux the
reservation is MAP_NORESERVE, so a large reservation (32 GiB) costs no
commit charge.

The Region tracks committed pages in a bitmap, a second reservation of
one bit per page rounded up to a page (one page for up to 128 MiB of
4 KiB pages, 1 MiB for 32 GiB), committed at once: charged on Windows,
not on Linux.

Fails with Misaligned for a size or align of the wrong form, and
OutOfMemory when the OS has no such range. O(1) system calls (three);
allocates nothing on the heap.

### `Unsupported`

```
export error Unsupported
```

This target cannot do it, or 64 code regions are live.

<!-- api:end -->
