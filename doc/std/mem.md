# std.mem

`std.mem` adds allocators and a pool to the prelude's `Arena` and `Heap`.

- `Fixed` serves memory from a buffer you give it, and frees its last block only.
- `FixedHeap` serves memory from a buffer you give it, and frees and reuses any block.
- `Fallback` serves memory from a buffer first, and from another allocator once a request does not fit.
- `Counting` counts what passes through another allocator.
- `Limit` refuses what would take another allocator past a number of bytes.
- `Failing` fails the requests a test picks, to reach code that handles `OutOfMemory`.
- `Null` refuses everything.
- `Custom` is an allocator over your own functions.
- `Pool` stores values under generational handles.
- `OffsetOf`, `Prefetch` and `Zeroed` help with layout.

Reach for it to put a hard budget on memory, to keep small containers off the heap, or to check in a test that code frees what it allocates, allocates nothing, or survives an allocation that fails. Its pool lets values name each other without references.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.mem
```

## Contents

- [The main pieces](#the-main-pieces)
- [Allocators](#allocators)
- [Choosing an allocator](#choosing-an-allocator)
- [A budget for an arena](#a-budget-for-an-arena)
- [Pools and handles](#pools-and-handles)
- [Layout](#layout)
- [Examples](#examples): [a list in a fixed buffer](#a-list-in-a-fixed-buffer), [a heap in a buffer](#a-heap-in-a-buffer), [small on the stack, large on the heap](#small-on-the-stack-large-on-the-heap), [an arena on a budget](#an-arena-on-a-budget), [a limit on the heap](#a-limit-on-the-heap), [count allocations](#count-allocations), [fail every allocation once](#fail-every-allocation-once), [prove a path allocates nothing](#prove-a-path-allocates-nothing), [nodes that name each other](#nodes-that-name-each-other)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Allocate from a buffer you own, a stack array included | `Fixed.New(buf)`, then `Fixed.Alloc()`; `Fixed.Reset` |
| Allocate and free in any order inside a buffer you own | `FixedHeap.New(buf)`, then `FixedHeap.Alloc()`; `FixedHeap.Reset` |
| Use a buffer while requests fit it, then another allocator | `Fallback.New(inner, buf)`, then `Fallback.Alloc()`; `Fallback.Reset` |
| Count bytes and allocations, find leaks | `Counting.New(inner)`, then `Counting.Alloc()`; `Bytes`, `Live`, `Peak` |
| Refuse what would pass a number of live bytes | `Limit.New(inner, max)`, then `Limit.Alloc()`; `Bytes`, `Left` |
| Fail one allocation, or every one from some point on | `Failing.New(inner, at)` or `Failing.NewFrom(inner, at)`, then `Failing.Alloc()`; `Requests`, `Failed` |
| Check that code allocates nothing | `Null()` |
| An allocator over your own functions | `Custom(ctx, table)` with an `AllocTable` (unsafe) |
| Store values under handles, reuse freed slots | `Pool[T]`: `Add`, `Get`, `GetMut`, `GetPair`, `Free`, `Iter`, `Sweep` |
| A field's byte offset | `OffsetOf[T]("field")` |
| A prefetch hint | `Prefetch(p, write)` |
| A value of zero bytes, such as a C struct of many fields | `Zeroed[T]()` |

Each allocator gives an `&Alloc` (`Custom` an `Alloc`, which you pass as `&`), which a heap container (`HeapList`, `HeapMap`, `Box`, `coll.HeapDeque`, ...) or `Arena.NewIn` takes. `Counting`, `Limit`, `Failing` and `Fallback` take an `&Alloc` themselves, so they stack in any order.

## Allocators

- **`Fixed`** is a bump allocator over a caller's buffer, and never touches the system. An allocation is O(1). Only the last block can be freed, and freeing it gives back the alignment padding before it too. So frees in any last-in, first-out order return the buffer to where it was. The last block also grows in place, so a lone growing list costs no copies. Each block starts on at least an 8-byte boundary, so a block under 8 bytes takes 8, and up to 7 bytes at the start of the buffer go unused. `Reset` frees everything at once; it ends the `Fixed`, so nothing allocated from it can be used after it.
- **`FixedHeap`** is a general-purpose allocator over a caller's buffer, and never touches the system either. Any block can be freed, in any order, and its bytes are used again. An allocation, a resize and a free are each O(1), whatever the buffer holds: a request takes a block from the first list of free blocks that is sure to fit it, found with two bit scans (two-level segregated fit). A freed block merges with the free blocks beside it at once, and a block grows in place into a free block after it.
  - Each block costs 8 bytes besides its own, and its size rounds up to 8 bytes, so a block of 1 to 8 bytes takes 16. A block also keeps 8 bytes that follow it when they are too few to be a block of their own. Each block starts on an 8-byte boundary; alignments up to 2^30 are served.
  - The bookkeeping lives at the start of the buffer: 4 + 68 bytes for the blocks under 128 bytes, and as much again for each doubling of the largest block's size above that, rounded up to 8. That is 416 bytes of a 4 KiB buffer and 960 bytes of a 1 MiB one. Up to 72 bytes at the end of the buffer go unused where more bookkeeping would cost more than they are worth. A longer buffer never has a smaller largest block.
  - A buffer of under 96 bytes from its first 8-byte boundary refuses every request, and a buffer over 16 GiB is used up to 16 GiB.
  - At an alignment of 8 or less, a request of up to 248 bytes is served whenever a free block of its size exists. A larger one is sure to be served when a free block at least one sixteenth larger exists.
  - A request aligned over 8 bytes needs a free block larger than it by the alignment and 8 bytes.
  - A free or a resize of a null pointer, of a pointer outside the buffer or of one off an 8-byte boundary changes nothing, and so does a second free of a block right after the first.
  - `Reset` frees everything at once, as `Fixed.Reset` does.
- **`Fallback`** serves a request from a caller's buffer when it fits there, and from another allocator when it does not. A container that stays small never reaches that allocator, and one that outgrows the buffer still works. The buffer is a `Fixed`: only its last block is freed or grown in place. A block that cannot grow in place is allocated again by its container, as over any allocator: in the buffer if the new size fits there, in the other allocator if not. The old block's bytes come back when it was the buffer's last block, and at `Reset` otherwise. Each block goes back to where it came from, told apart by its address. A zero-byte request goes to the other allocator, as a request for one byte. `Reset` makes the buffer free again.
- **`Counting`** passes every request to another allocator and counts the bytes and allocations live through it, and the most bytes it has seen live. It fails exactly when the inner allocator does. `Live()` of zero at the end of a test means nothing leaked.
- **`Limit`** passes requests to another allocator while the bytes live through it stay within a limit, and refuses the request that would pass it, without asking the inner allocator. It counts the sizes callers asked for, as `Counting` does. Freed bytes are available again at once, whatever the order of the frees, and a resize is charged the difference. It needs no memory of its own.
- **`Failing`** passes requests to another allocator and fails the ones a test picks. It numbers, from zero, the requests that can fail: each allocation and each resize to more bytes. `Failing.New(inner, at)` fails request `at` and no other; `Failing.NewFrom(inner, at)` fails it and every later one, as an exhausted allocator does. A negative `at` fails none. A failed request does not reach the inner allocator. `Requests()` is how many were numbered, and `Failed()` how many failed.
- **`Null()`** fails every request with `OutOfMemory`. Code that runs to the end with it allocated nothing on that run.
- **`Custom(ctx, table)`** makes an `Alloc` over a program's own functions, for an embedder that hands Mk its own heap. Each request is one call of the table's `alloc`, `resize` or `free` entry with `ctx`. `Custom`, and the `Alloc.Bytes` and `FreeBytes` calls that reach the table, are callable only inside `unsafe` (E1101), since the entries are trusted to keep the table's rules. `AllocTable` and `Custom` are Provisional.

The `Alloc()` method of each returns an `&Alloc` that borrows the value, so a container made from it keeps that value in place while it lives. `Counting.New`, `Limit.New`, `Failing.New`, `Failing.NewFrom` and `Fallback.New` borrow `inner`, and `Fixed.New`, `FixedHeap.New` and `Fallback.New` borrow `buf` for writing. So the compiler refuses a read of the buffer while a block is live in it (E0504), and a `Reset` while a container made from the allocator lives (E0501).

No `&Alloc` crosses to another task (E1002), and none of these types is `Share`, so each value serves one task at a time. `Null()` holds no state: any task may call it.

`Counting`, `Limit` and `Failing` also run at compile time, over the allocator of `EvalArena()`. `Fixed`, `FixedHeap` and `Fallback` read their buffer's address, so a compile-time evaluation refuses them (E0901).

## Choosing an allocator

| You have | Use |
|---|---|
| Data that dies together, at the end of a scope or a request | An `Arena` |
| Long-lived data freed piece by piece, on a hosted target | The `Heap` |
| A buffer, and frees in last-in, first-out order or none at all | `Fixed` |
| A buffer, and frees in any order: a kernel's heap, a subsystem on a fixed share of memory, a target with no operating system | `FixedHeap` |
| Containers that are small nearly always, and large now and then | `Fallback` over a stack array, then the `Heap` |
| A budget in bytes, and no wish to set the bytes aside | `Limit` over the `Heap` |
| A test of what a function does when memory runs out | `Failing`, one run per request number |
| A test that a function frees everything | `Counting`, and `Live()` at the end |

A `Fixed` and a `Limit` both bound memory, but not the same amount. A `Fixed` bounds what was ever handed out since the last `Reset`, since it frees only its last block. A `Limit` and a `FixedHeap` bound what is live at one time.

## A budget for an arena

`Arena.NewIn(a)` makes an arena whose chunks, the first included, and its `Sub`s' chunks come from the allocator `a`, and go back to it at `Reset` and `Free`. So a `Limit`, a `Fixed` or a `FixedHeap` under it bounds everything an API that takes `&Arena` puts in the arena, and a `Counting` under it measures it. The example [an arena on a budget](#an-arena-on-a-budget) shows one.

- The arena borrows `a`, and each `Sub` borrows the arena. So the allocator behind `a` is not reset, freed or moved while either lives.
- An `Arena` is not `Send`, so it never calls `a` from another task.
- A `Fixed` gets back only what is freed in last-in, first-out order. An arena that served a value aligned over 8 bytes, or a `Sub` freed before its parent's later chunks, may leave part of the buffer used until `Fixed.Reset`. A `FixedHeap` and a `Limit` get every chunk back, in any order.
- A std API that also uses the process heap says so in its documentation, as [`proc.Run`](proc.md) and a [`sim.Sim`](sim.md) do.

## Pools and handles

A `Pool[T]` keeps its values in slots in its arena, each slot with a generation, and reuses a freed slot. `Add` returns a `Handle`: 8 bytes, plain, `Eq` and `Hash`, with no public fields, naming the slot and its 32-bit generation. Values can store handles to each other where references would tie them to one borrow. Wrap a `Handle` in a one-field struct to keep two kinds of handle apart.

- A handle whose value was freed gets `none` from `Get` and `GetMut` and `false` from `Free`: never a panic, never another value. A slot whose generation is spent, after 2^32 - 1 reuses, is retired and never reused, so a stale handle never matches again.
- A handle from another pool is refused the same way unless its slot and generation match a live value there. A handle has no room for its pool's identity, so each pool starts its generations at its own base. Between two pools a false match takes about 2^31 reuses of one slot; among the N pools a process has made, the nearest bases are about 2^32 / N apart. A false match is never memory-unsafe, since every value of a pool is a `T`.
- `Free` drops the value, as `Sweep` does, so each needs a `T` that is not linear (E0701 at the call). Inside a generic function, the bounds must say `T` is not linear, as `T: Plain` does.
- `Iter` visits the live values in slot order. `Sweep` frees every value whose `keep` returns false and returns how many it freed: the sweep of a mark-and-sweep collector.
- `GetPair` gives two values for writing, and returns `none` when the two handles are equal or stale, never a panic.

`Add`, `Get`, `GetMut`, `GetPair`, `Free` and `Len` are O(1); a whole `Iter` pass is O(n), and `Sweep` is n calls of `keep`, n the slots ever used. `Add` allocates only when the pool grows, doubling its buffer in the arena, which keeps the old one until the arena is freed. A failed `Add` changes nothing and leaves the value with the caller. `Free` and `Sweep` may allocate in the arena to record a freed slot; when that fails the slot is retired, so they never fail.

```mk
// verdict: reject E0701
// Sweep drops what it does not keep, so its values are not linear
import std.fs
import std.mem

Purge(p: mut mem.Pool[fs.File]): int {
    return p.Sweep(fn(h: mem.Handle, f: &fs.File): bool { return false }) // <- E0701
}

PurgeAll[T](p: mut mem.Pool[T]): int {
    return p.Sweep(fn(h: mem.Handle, x: &T): bool { return false }) // <- E0701
}
```

## Layout

`OffsetOf[T]("field")` is the byte offset of a field in `T`'s layout, as a compile-time value. A field `T` lacks is E0901. `Prefetch(p, write)` tells the CPU that `p`'s memory will soon be read, or written; it changes nothing a program can observe. Both are safe, O(1) and allocate nothing.

```mk
// verdict: ok
import std.mem

struct Header {
    tag: u8
    size: u32
    next: u64
}

const NextAt: int = mem.OffsetOf[Header]("next") // a compile-time value

export Main(sys: &System): WriteError!void {
    try sys.Print("size at {s}, next at {n}, {total} bytes\n", (s: mem.OffsetOf[Header]("size"), n: NextAt, total: SizeOf[Header]()))
}
```

```text
$ mk run layout.mk
size at 4, next at 8, 16 bytes
```

`Zeroed[T]()` is a `T` whose bytes are all zero: every integer and float 0, every `bool` false, every raw pointer null, every optional `none`. It fills a record of many fields without naming each one, as C's `= {0}` does, and is the way to start a C import's struct ([C imports](../reference.md#c-imports)). `T` must be [zero-valid](../reference.md#zero-valid-types), or the call is E0701, so it is safe: a `str`, slice, reference, `fn` value or struct with an `invariant` cannot be zero. `T` is the result, so it is not an error union (E0701): zero the value type instead. O(size of `T`); allocates nothing; also at compile time.

```mk
// verdict: ok
import std.mem

struct Window {
    x: i32
    y: i32
    width: i32
    height: i32
    title: [64]u8
    parent: ?u32
    scale: f32
    visible: bool
}

export Main(sys: &System): WriteError!void {
    var w := mem.Zeroed[Window]()
    w.width = 640
    try sys.Print("{w} {h} {v}\n", (w: w.width, h: w.height, v: w.visible))
}
```

```text
$ mk run zeroed.mk
640 0 false
```

## Examples

Each program below runs as written with `mk run`.

### A list in a fixed buffer

```mk
// verdict: ok
import std.mem

export Main(sys: &System): OutOfMemory|WriteError!void {
    var buf: [300]u8 = [0; 300]
    f := mem.Fixed.New(mut buf[..]) // every byte comes from buf
    var xs := HeapList[int].New(f.Alloc())
    onerr xs.Free(f.Alloc())
    for i in 0..100 {
        xs.Push(f.Alloc(), i) else {
            try sys.Print("full after {n} values\n", (n: xs.Len()))
            break
        }
    }
    xs.Free(f.Alloc())
}
```

```text
$ mk run fixed.mk
full after 32 values
```

- The list doubles its capacity as it grows, in place since it is the last block. 32 values take 256 bytes; 64 would take 512, more than the buffer holds.
- A failed `Push` leaves the list as it was, so it is still freed at the end.

### A heap in a buffer

```mk
// verdict: ok
import std.mem

export Main(sys: &System): OutOfMemory|WriteError!void {
    var buf: [4096]u8 = [0; 4096]
    h := mem.FixedHeap.New(mut buf[..]) // every byte comes from buf
    a := h.Alloc()
    var total := 0
    for round in 0..1_000 {
        var m := HeapMap[int, int].New(a)
        onerr m.Free(a)
        for i in 0..50 {
            try m.Set(a, i, i * round)
        }
        total += m.Len()
        m.Free(a) // the next round uses these bytes again
    }
    try sys.Print("{total} entries through 4 KiB\n", (total))
}
```

```text
$ mk run heap.mk
50000 entries through 4 KiB
```

- A map that grows frees its old table, which is not the last block. A `Fixed` keeps those bytes until `Reset` and runs out after a few rounds; a `FixedHeap` uses them again.
- Past the buffer a `Set` fails with `OutOfMemory`, as it does over a `Fixed`.

### Small on the stack, large on the heap

```mk
// verdict: ok
import std.mem

// Squares sums the squares below n, through a list on a.
Squares(a: &Alloc, n: int): OutOfMemory!int {
    var xs := HeapList[int].New(a)
    onerr xs.Free(a)
    for i in 0..n {
        try xs.Push(a, i * i)
    }
    var sum := 0
    for x in xs.Items() {
        sum += x
    }
    xs.Free(a)
    return sum
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    c := mem.Counting.New(sys.heap.Alloc()) // counts what reaches the heap
    var buf: [2048]u8 = [0; 2048]
    b := mem.Fallback.New(c.Alloc(), mut buf[..])
    small := try Squares(b.Alloc(), 100)
    try sys.Print("{small}, with {n} bytes from the heap\n", (small, n: c.Peak()))
    large := try Squares(b.Alloc(), 10_000)
    try sys.Print("{large}, reached the heap: {spilled}\n", (large, spilled: c.Peak() > 0))
}
```

```text
$ mk run fallback.mk
328350, with 0 bytes from the heap
333283335000, reached the heap: true
```

- `Squares` takes an ordinary `&Alloc` and knows nothing of the buffer. A hundred values grow in place inside it; ten thousand start there and move to the heap when they no longer fit.
- The list is the buffer's last block, so its bytes there are freed when it moves out, and the buffer is whole again for the next call.

### An arena on a budget

```mk
// verdict: ok
import std.mem

// Distinct counts the different words of text; its map lives in a.
Distinct(a: &Arena, text: str into a): OutOfMemory!int {
    var seen := Map[str, bool].New(a)
    for w in text.Words() {
        try seen.Set(w, true)
    }
    return seen.Len()
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var buf: [4096]u8 = [0; 4096]
    f := mem.Fixed.New(mut buf[..])
    var scratch := try Arena.NewIn(f.Alloc()) // every chunk of the arena comes from buf
    onerr scratch.Free()
    n := try Distinct(&scratch, "the cat saw the dog and the bird")
    try sys.Print("{n} distinct words\n", (n))
    scratch.Free()
}
```

```text
$ mk run budget.mk
6 distinct words
```

- `Distinct` takes an ordinary `&Arena` and knows nothing of the budget. Past 4,096 bytes it fails with `OutOfMemory` instead of reaching the heap.

### A limit on the heap

```mk
// verdict: ok
import std.mem

// Collect pushes n values into a list on a and returns how many it held.
Collect(a: &Alloc, n: int): OutOfMemory!int {
    var xs := HeapList[int].New(a)
    onerr xs.Free(a)
    for i in 0..n {
        try xs.Push(a, i)
    }
    held := xs.Len()
    xs.Free(a)
    return held
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    l := mem.Limit.New(sys.heap.Alloc(), 64 * 1024) // at most 64 KiB live at once
    n := try Collect(l.Alloc(), 1_000)
    try sys.Print("{n} values fit, {left} bytes left\n", (n, left: l.Left()))
    _ = Collect(l.Alloc(), 100_000) else {
        try sys.Print("100,000 values pass the limit\n")
    }
    try sys.Print("{b} bytes live at the end\n", (b: l.Bytes()))
}
```

```text
$ mk run limit.mk
1000 values fit, 65536 bytes left
100,000 values pass the limit
0 bytes live at the end
```

- Nothing is set aside: the 64 KiB are a ceiling on what the heap is asked for, not a buffer.
- What the first call freed is available to the second. A growing list holds its old buffer and its new one for a moment, and both count.

### Count allocations

```mk
// verdict: ok
import std.mem

export Main(sys: &System): OutOfMemory|WriteError!void {
    c := mem.Counting.New(sys.heap.Alloc()) // counts what passes through it
    a := c.Alloc()
    x := try Box[i64].New(a, 1)
    onerr x.Free(a)
    y := try Box[i64].New(a, 2)
    onerr y.Free(a)
    try sys.Print("live {l}, bytes {b}\n", (l: c.Live(), b: c.Bytes()))
    x.Free(a)
    y.Free(a)
    try sys.Print("live {l}, bytes {b}, peak {p}\n", (l: c.Live(), b: c.Bytes(), p: c.Peak()))
}
```

```text
$ mk run count.mk
live 2, bytes 16
live 0, bytes 0, peak 16
```

- `Bytes` counts the sizes callers asked for, not what the inner allocator rounded them up to.
- A negative `Live` means something freed through the `Counting` a block it did not allocate.

### Fail every allocation once

```mk
// verdict: ok
import std.mem

// Build fills a map and a list on a. A failure half-way must free both.
Build(a: &Alloc): OutOfMemory!int {
    var names := TextMap[int].New(a)
    onerr names.Free(a)
    var ids := HeapList[int].New(a)
    onerr ids.Free(a)
    words := ["ant", "bee", "cat", "dog", "eel", "fox"]
    for i in 0..6 {
        _ = try names.Ensure(a, words[i], i)
        try ids.Push(a, i)
    }
    n := names.Len() + ids.Len()
    names.Free(a)
    ids.Free(a)
    return n
}

export Main(sys: &System): WriteError!void {
    c := mem.Counting.New(sys.heap.Alloc())
    var at := 0
    var failed := 0
    var leaked := 0
    for at < 1_000 {
        f := mem.Failing.New(c.Alloc(), at) // fails request number at, and no other
        _ = Build(f.Alloc()) else {
            failed += 1
        }
        leaked += c.Live()
        if f.Failed() == 0 {
            break // the run made only at requests: each has now failed once
        }
        at += 1
    }
    try sys.Print("{at} requests, {failed} runs failed, {leaked} blocks leaked\n", (at, failed, leaked))
}
```

```text
$ mk run failing.mk
11 requests, 10 runs failed, 0 blocks leaked
```

- Each run fails one request more than the last, so every `try` of `Build` that allocates takes its error path once. The `Counting` beneath shows that each path freed what it had.
- One run did not fail though its request did: a list that cannot grow in place allocates a new buffer instead, so a refused resize is not an error.
- `Failing.NewFrom` fails every request from `at` on. Use it to check the paths that run after memory is gone, which must not allocate.

### Prove a path allocates nothing

```mk
// verdict: ok
import std.mem

// Sum adds xs up and allocates nothing.
Sum(a: &Alloc, xs: []int): OutOfMemory!int {
    var total := 0
    for x in xs {
        total += x
    }
    return total
}

// Evens collects the even values of xs in a list, so it allocates.
Evens(a: &Alloc, xs: []int): OutOfMemory!int {
    var even := HeapList[int].New(a)
    onerr even.Free(a)
    for x in xs {
        if x % 2 == 0 {
            try even.Push(a, x)
        }
    }
    n := even.Len()
    even.Free(a)
    return n
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    xs := [3, 1, 2]
    n := try Sum(mem.Null(), xs[..]) // it succeeded, so Sum allocated nothing
    try sys.Print("sum {n}\n", (n))
    _ = Evens(mem.Null(), xs[..]) else {
        try sys.Print("Evens allocates\n")
    }
}
```

```text
$ mk run null.mk
sum 6
Evens allocates
```

- `Null` proves something about one run: a path that allocates only on some inputs passes on the others.

### Nodes that name each other

```mk
// verdict: ok
import std.mem

struct Node {
    name: str
    next: ?mem.Handle // a handle, not a reference, so nodes can name each other
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var p := mem.Pool[Node].New(sys.arena)
    b := try p.Add(Node(name: "b", next: none))
    a := try p.Add(Node(name: "a", next: some(b)))
    if n := p.Get(a) {
        if nb := n.next {
            try sys.Print("a -> {name}\n", (name: p.Get(nb).Must().name))
        }
    }
    _ = p.Free(b)
    try sys.Print("b after Free: {gone}, Free again: {again}\n", (gone: p.Get(b).IsNone(), again: p.Free(b)))
    c := try p.Add(Node(name: "c", next: none)) // reuses b's slot
    try sys.Print("old b names c: {same}, live: {n}\n", (same: p.Get(b).IsSome(), n: p.Len()))
    dropped := p.Sweep(fn(h: mem.Handle, n: &Node): bool { return n.name != "c" })
    try sys.Print("swept {d}, live: {n}\n", (d: dropped, n: p.Len()))
    _ = c
}
```

```text
$ mk run pool.mk
a -> b
b after Free: true, Free again: false
old b names c: false, live: 2
swept 1, live: 1
```

- `Add` puts a value in the slot freed last, so `c` takes `b`'s slot with a new generation, and the old handle `b` still gets `none`.
- `Sweep` keeps the values for which `keep` returns true and frees the rest.

## API

<!-- api:begin -->
Stability: Stable.

### `AllocTable`

```
export struct AllocTable
```

A table of a program's own allocator functions, which Custom calls:
alloc(ctx, n, align) returns a block or null, resize(ctx, p, old, new,
align) resizes in place or returns false, and free(ctx, p, n, align)
frees. Build one with `mem.AllocTable(alloc: ..., resize: ..., free:
...)`. Provisional, as Custom is.

Fields:

- `alloc: fn(*u8, int, int): *u8`: alloc(ctx, size, align) returns a block, or null on failure.
- `resize: fn(*u8, *u8, int, int, int): bool`: resize(ctx, p, old, new, align) grows or shrinks the block in place, or
  returns false.
- `free: fn(*u8, *u8, int, int)`: free(ctx, p, size, align) gives the block back.

### `Counting`

```
export mut view struct Counting
```

An allocator that passes every request to another and counts the bytes
and allocations live through it, and the peak.

### `Counting.Alloc`

```
export Counting.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that counts and passes each request on to inner; it
fails exactly when inner does. O(1) per request on top of inner's.

### `Counting.Bytes`

```
export Counting.Bytes(&self): int
```

The bytes allocated through self and not freed: the sizes callers
asked for, not what inner rounded them up to. O(1).

### `Counting.Live`

```
export Counting.Live(&self): int
```

The allocations made through self and not freed. Zero at the end of a
test means nothing leaked; a negative count means something freed
through self a block that self did not allocate. O(1).

### `Counting.New`

```
export Counting.New(inner: &Alloc): Counting
```

- Flows: The result borrows from inner.

A Counting over inner, with every count zero. The result borrows inner.
O(1); allocates nothing.

### `Counting.Peak`

```
export Counting.Peak(&self): int
```

The most Bytes() has been since New. O(1).

### `Custom`

```
export Custom(ctx: *u8, table: &AllocTable): Alloc from table
```

- Flows: The result borrows from table.

An Alloc whose every request is one call of an entry of table, with
ctx. table must outlive the Alloc and every block it hands out.
Callable only inside unsafe (E1101), since the entries are trusted to
keep the table's rules. O(1); allocates nothing.

### `Failing`

```
export mut view struct Failing
```

An allocator that passes requests to another and fails the ones a test
picks by number, to drive code down its OutOfMemory paths.

### `Failing.Alloc`

```
export Failing.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that fails the picked requests with OutOfMemory, without
asking inner, and passes every other one on. A free, and a resize to
the same or fewer bytes, is never failed and never counted. O(1) per
request on top of inner's.

### `Failing.Failed`

```
export Failing.Failed(&self): int
```

The requests self failed. Zero after a run means the run made no
request with the number picked, so a loop over request numbers stops
there. O(1).

### `Failing.New`

```
export Failing.New(inner: &Alloc, at: int): Failing
```

- Flows: The result borrows from inner.

A Failing over inner that fails exactly one request: the one numbered
at, counting from zero the allocations and the resizes to more bytes
made through it. A negative at fails none. The result borrows inner.
O(1); allocates nothing.

### `Failing.NewFrom`

```
export Failing.NewFrom(inner: &Alloc, at: int): Failing
```

- Flows: The result borrows from inner.

A Failing over inner that fails the request numbered at and every one
after it, as an exhausted allocator does. A negative at fails none. The
result borrows inner. O(1); allocates nothing.

### `Failing.Requests`

```
export Failing.Requests(&self): int
```

The requests made through self that could have failed: the allocations
and the resizes to more bytes, the failed ones included. O(1).

### `Fallback`

```
export mut view struct Fallback
```

An allocator that serves a request from a caller's buffer when it fits
there, and from another allocator when it does not. A container that
stays small then never reaches that allocator, and one that outgrows
the buffer still works.

The buffer is a Fixed: only its last block is freed or grown in place.
A block that cannot grow in place is allocated again by its container,
in the buffer if it fits there and in the other allocator if not.

### `Fallback.Alloc`

```
export Fallback.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that tries the buffer first and inner second; it fails
with OutOfMemory only when inner does. A block goes back to where it
came from, whichever that was. A zero-byte request goes to inner, as
a request for one byte. O(1) per request on top of inner's.

### `Fallback.New`

```
export Fallback.New(inner: &Alloc, buf: mut []u8): Fallback from inner, buf
```

- Flows: The result borrows from inner and buf.

A Fallback that serves from buf first, then from inner. inner stays
borrowed, and buf borrowed (mut), for as long as the Fallback and
anything allocated from it lives. As for Fixed.New, up to 7 bytes in
front of buf's first 8-byte boundary are never handed out. O(1);
allocates nothing.

### `Fallback.Reset`

```
export Fallback.Reset(mut self) ends self
```

- Flows: Ends every borrow of self.

Makes the whole buffer free again. It ends self, so nothing allocated
from self can be used after it; what came from the other allocator was
freed there by then. O(1).

### `Fixed`

```
export mut view struct Fixed
```

A bump allocator over a caller's buffer, which never touches the
system: an allocation is O(1), the last block grows and frees in place,
and Reset frees everything at once.

Only the last block can be freed, and freeing it gives back the
alignment padding before it too, so frees in any last-in, first-out
order return the buffer to where it was. Each block starts on at least
an 8-byte boundary, so a block under 8 bytes takes 8. Alignments up to
2^30 are served; a larger one fails.

### `Fixed.Alloc`

```
export Fixed.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that hands out self's buffer; a request it cannot fit
fails with OutOfMemory. A container made from it borrows self. O(1).

### `Fixed.New`

```
export Fixed.New(buf: mut []u8): Fixed
```

- Flows: The result borrows from buf.

A Fixed over buf, which stays borrowed (mut) for as long as the Fixed
and anything allocated from it lives. Up to 7 bytes in front of buf's
first 8-byte boundary are never handed out; a buf with no such boundary
(fewer than 8 bytes, or an empty one at an odd address) refuses every
request. O(1); allocates nothing.

### `Fixed.Reset`

```
export Fixed.Reset(mut self) ends self
```

- Flows: Ends every borrow of self.

Makes the whole buffer free again. It ends self, so nothing allocated
from self can be used after it. O(1).

### `FixedHeap`

```
export mut view struct FixedHeap
```

A general-purpose allocator over a caller's buffer, which never touches
the system: any block can be freed, in any order, and its bytes are
used again. An allocation, a resize and a free are each O(1), whatever
the buffer holds.

A freed block merges with the free blocks beside it at once, and a
block grows in place into a free block after it. Each block costs 8
bytes besides its own, rounded up to 8, and starts on an 8-byte
boundary; alignments up to 2^30 are served. The bookkeeping lives at
the start of the buffer: 416 bytes of a 4 KiB buffer, 960 of a 1 MiB
one. A buffer over 16 GiB is used up to 16 GiB.

### `FixedHeap.Alloc`

```
export FixedHeap.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that hands out self's buffer; a request it cannot fit
fails with OutOfMemory. A container made from it borrows self. O(1).

### `FixedHeap.New`

```
export FixedHeap.New(buf: mut []u8): FixedHeap
```

- Flows: The result borrows from buf.

A FixedHeap over buf, which stays borrowed (mut) for as long as the
FixedHeap and anything allocated from it lives. Up to 7 bytes in front
of buf's first 8-byte boundary are never handed out, and the
bookkeeping takes the start of what follows; a buf too small for it
and one block (under 96 bytes from that boundary) refuses every
request. O(1) besides clearing the bookkeeping; allocates nothing.

### `FixedHeap.Reset`

```
export FixedHeap.Reset(mut self) ends self
```

- Flows: Ends every borrow of self.

Makes the whole buffer free again. It ends self, so nothing allocated
from self can be used after it. O(1) besides clearing the bookkeeping.

### `Handle`

```
export struct Handle
```

The name of a value in a Pool: 8 bytes, plain, Eq and Hash, with no
public fields. It holds a slot index and that slot's 32-bit generation.
To keep two kinds of handle apart, wrap each in a one-field struct.

### `Limit`

```
export mut view struct Limit
```

An allocator that passes requests to another while the bytes live
through it stay within a limit, and refuses the request that would
pass it.

### `Limit.Alloc`

```
export Limit.Alloc(&self): &Alloc from self
```

- Flows: The result borrows from self.

The allocator that passes each request on to inner unless it would
take Bytes() past the limit: then it fails with OutOfMemory and inner
is not asked. It also fails whenever inner does. O(1) per request on
top of inner's.

### `Limit.Bytes`

```
export Limit.Bytes(&self): int
```

The bytes allocated through self and not freed: the sizes callers
asked for, not what inner rounded them up to. O(1).

### `Limit.Left`

```
export Limit.Left(&self): int
```

The bytes a request may still take: the limit less Bytes(). O(1).

### `Limit.New`

```
export Limit.New(inner: &Alloc, max: int): Limit
```

- Flows: The result borrows from inner.

A Limit of max bytes over inner, with nothing counted yet; a max below
zero is zero, which refuses every request of one byte or more. The
result borrows inner. O(1); allocates nothing.

### `Null`

```
export Null(): &Alloc
```

An allocator on which every request fails with OutOfMemory. Code that
runs to the end with it allocated nothing on that run. O(1).

### `OffsetOf`

```
export OffsetOf[T](field: str): int
```

The byte offset of the field named field in T's layout, as a
compile-time value; a field T lacks is E0901. It allocates nothing.

### `Pool`

```
export view struct Pool[T]
```

Values of T in slots of an arena, each named by a Handle that goes
stale when its value is freed. A freed slot is reused with a new
generation, so a stale handle gets none, never another value.

A handle carries no pool identity, so each pool starts its generations
at its own base. Another pool's handle is refused unless its slot and
generation match a live value here: between two pools that takes about
2^31 reuses of one slot, and among the N pools a process made the
nearest bases are about 2^32 / N apart. A false match is never
memory-unsafe, since every value of a pool is a T.

### `Pool.Add`

```
export Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle
```

- Errors: OutOfMemory

Stores x and returns its handle: in the slot freed last when there is
one, else in a new slot at the end. Fails with OutOfMemory when the
arena cannot grow the pool or the pool holds 2^32 - 1 slots; then
nothing changed and the caller keeps x. Amortized O(1); a growth
doubles the buffer in the arena, which keeps the old one.

### `Pool.Free`

```
export Pool[T].Free(mut self, h: Handle): bool
```

Drops h's value, makes every handle to it stale and returns true;
false, with nothing changed, for a handle that names no live value of
this pool. T may not be linear (E0701 at the call; in a generic
function, bound T by Plain). The slot is reused by a later Add, unless
its generation is spent after 2^32 - 1 reuses: then it is retired, so
a stale handle never matches again. When the arena cannot record the
free slot, the slot is retired too, so Free never fails. O(1).

### `Pool.Get`

```
export Pool[T].Get(&self, h: Handle): ?&T
```

- Flows: The result borrows from self.

The value h names; none for a stale, foreign or out-of-range handle,
never a panic. O(1).

### `Pool.GetMut`

```
export Pool[T].GetMut(mut self, h: Handle): ?mut T
```

- Flows: The result borrows from self.

The value h names, to update in place; none as Get. O(1).

### `Pool.GetPair`

```
export Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self
```

- Flows: The result borrows from self.

The values of h and k, both writable; none when h and k name the same
slot or either names no live value of this pool, never a panic. O(1).

### `Pool.Iter`

```
export Pool[T].Iter(&self): PoolIter[T] from self
```

- Flows: The result borrows from self.

Iterates the live values in slot order. O(1); a whole pass is O(n), n
the slots ever used. It allocates nothing.

### `Pool.Len`

```
export Pool[T].Len(&self): int
```

The number of live values. O(1).

### `Pool.New`

```
export Pool[T].New(a: &Arena): Pool[T]
```

- Flows: The result borrows from a.

An empty pool in a. O(1); allocates nothing until the first Add.

### `Pool.Sweep`

```
export Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int
```

Drops every live value whose keep returns false, as Free does, so its
handles go stale, and returns how many it dropped: the sweep of a
mark-and-sweep collector. keep may capture locals. T may not be linear
(E0701 at the call). n calls of keep, n the slots ever used.

### `PoolIter`

```
export view struct PoolIter[T]
```

The live values of a pool with their handles, in slot order; made by
Pool.Iter.

### `PoolIter.Next`

```
export PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool
```

- Flows: The result borrows what self.pool refers to; self is free after the call.

The next live value and its handle, or none after the last.

### `Prefetch`

```
export Prefetch[T](p: &T, write: bool)
```

A hint that p's memory will soon be read, or written when write is
true; it changes nothing a program can observe. O(1); allocates
nothing.

### `Zeroed`

```
export Zeroed[T](): T
```

A T whose bytes are all zero: every integer and float 0, every bool
false, every raw pointer null, every optional none. T must be zero-valid
and not an error union (E0701 otherwise): a C import's struct of any
size, or an array or struct of such parts. O(size of T); allocates
nothing.

<!-- api:end -->
