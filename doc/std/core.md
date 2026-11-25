# std.core: the prelude

The prelude is the set of names every Mk file sees without an import. It holds strings and text, slices and lists, maps and sets, the shared errors, arenas and allocators, formatting, readers and writers, `Io` and `System`, scopes, channels and locks. The module `std.core` declares them. You cannot import it (E0202); you write its names bare, as in `List[int]`, `Print` or `Chan[str]`.

Read the [library guide](README.md) first. The language rules are in the [reference](../reference.md), and borrowing is in the [memory model](../memory.md).

## Contents

- [The main pieces](#the-main-pieces)
- [Arenas and allocators](#arenas-and-allocators)
- [Strings and text](#strings-and-text)
- [Slices and lists](#slices-and-lists)
- [Maps and sets](#maps-and-sets)
- [Releasing what a container holds](#releasing-what-a-container-holds)
- [Optionals and errors](#optionals-and-errors)
- [Formatting and printing](#formatting-and-printing)
- [Reading and writing](#reading-and-writing)
- [Io, scopes and cancellation](#io-scopes-and-cancellation)
- [Channels and locks](#channels-and-locks)
- [Targets and generators](#targets-and-generators)
- [Examples](#examples): [parse settings](#parse-settings), [sort and search a list](#sort-and-search-a-list), [group words in a map](#group-words-in-a-map), [heap containers](#heap-containers), [format a table](#format-a-table), [read and write bytes](#read-and-write-bytes), [tasks and a channel](#tasks-and-a-channel), [an arena per turn](#an-arena-per-turn)
- [API](#api)

## The main pieces

| Area | Names |
|---|---|
| Strings and text | `str` and its methods, `Text`, `HeapText`, `Path`; the iterators `Lines`, `Split`, `Words`, `Scalars` |
| Sequences | `Slice[T]` (the methods of `[]T`, `[N]T`, `List`, `HeapList`), `List[T]`, `HeapList[T]`, `Chunks`, `ChunksMut`, `Swap`, `Replace` |
| Maps and sets | `Map[K, V]`, `Set[T]`, `HeapMap[K, V]`, `TextMap[V]`, `MapIter`, `TextMapIter`, `HashKey`, `Hasher` |
| Interfaces | `Eq`, `Ord`, `Hash`, `Format`, `Free` |
| Errors | `OutOfMemory`, `Canceled`, `Closed`, `ReadError`, `WriteError`, `ParseError`, `Utf8Error` |
| Memory | `Arena`, `Heap`, `Alloc`, `Box[T]`, `Cell[T]` |
| Output and input | `Print`, `Writer`, `Reader`, `System` |
| Tasks | `Io`, `Scope`, `Limits`, `StackSize`, `Chan[T]`, `Mutex[T]`, `Guard[T]` |
| Target | `TargetInfo`, `Arch`, `Os` |
| Generators | `Gen`, `Type`, `TypeKind`, `Code`, `Block`, `Field`, `Variant`, `Method`, `Param`, `Receiver`, `Flow`, `FlowKind` |

The rest of the library builds on these:

- [`std.coll`](coll.md) adds deques, priority queues, B-trees and bitsets.
- [`std.text`](text.md) adds the Unicode algorithms.
- [`std.mem`](mem.md) adds allocators with budgets and a pool.
- [`std.sync`](sync.md) adds more locks and `Select`.
- [`std.atomic`](atomic.md) adds atomics.
- [`std.time`](time.md) adds the `time.Duration` that `Io.Sleep` and `Scope.CancelAfter` take.

Two rules hold for every method on this page:

- A method with a by-value `self` of a plain type, such as `str.Len`, is callable on a reference to it: `r.Len()` for `r: &str`.
- A call that consumes its argument, such as `Push` or `Send`, leaves it with you when it fails ([errors](README.md#5-errors)).

## Arenas and allocators

The [library guide](README.md#3-memory-allocators-and-arenas) explains when to use an arena and when the heap.

### Arenas

An `Arena` hands out memory that is freed all at once. It is a linear bump allocator. What it hands out borrows it, so a `Reset` while a value from it is live is E0501. It ends with `Free()`, which takes no allocator. An `Arena` is not `Send` (E1001).

| Call | What it does |
|---|---|
| `Arena.New(h)` | An arena over the heap |
| `Arena.NewIn(a)` | An arena whose chunks come from the allocator `a` and go back to it, so a budget bounds it ([a budget for an arena](mem.md#a-budget-for-an-arena)) |
| `a.Sub()` | A child arena for scratch memory, freed before its parent |
| `Arena.NewIn(a.Alloc())` | A frame: a child arena whose chunks come from `a` itself and go back to it when it ends, last in first out ([a frame per call](#a-frame-per-call)) |
| `a.Put(x)` | Moves `x` into the arena and returns it `mut`; on `OutOfMemory` the caller keeps `x` |
| `a.Slice[T](n)` | `n` zeroed elements of a zero-valid `T` (E0701 otherwise); a negative `n` panics (code 8) |
| `a.SliceAligned[T](n, align)` | The same, with the first element at a multiple of `align` bytes, or of `T`'s alignment when that is larger |
| `a.Dup(s)` | A copy of `s`; `""` allocates nothing |
| `a.Fmt(f, args)` | A formatted `str`, with `Print`'s holes and specs |
| `a.Alloc()` | The arena as an `&Alloc`, so heap containers can live in it |
| `a.Reset()`, `a.Free()` | End every value at once; `Reset` keeps memory for the next turn |

- `SliceAligned`'s `align` is a power of two from 1 to 65,536, else it panics (code 8). A slice made at compile time becomes static data aligned to `T`'s alignment only, so there an `align` over `T`'s is E0901. A static table that must be aligned has an `align(n)` element type, `n` up to 65,536, which static data keeps on every profile.
- `Reset` keeps the first chunk, the current one and the current aligned one, and the next chunk size stays as it was. So a loop that resets its arena each turn settles. It takes nothing more from the heap when each request is at most 512 KiB and one turn fits in the current chunk (up to 1 MiB). Otherwise chunks go and come every turn.
- A frame, `Arena.NewIn(a.Alloc())`, rewinds `a` when it ends: its chunks are blocks of `a`, freed newest first, and `a.Alloc()` gives back the last block it bumped and a chunk of its own while it is `a`'s newest. Frames nest as calls do, and each holds a loan on `a`, so `a` is not reset or freed while one lives. Once warm they take nothing more from the heap while the frames live at once fit in `a`'s current chunk (each takes at least 4 KiB of it, and `a`'s chunks grow to 1 MiB); a request too large for `a`'s chunks takes a chunk of its own from the heap and gives it back. A frame is not given back when `a` moved to a new chunk under it, or when it ends before a newer frame: those bytes stay in `a` until its `Reset` or `Free`. So frames nested deeper than one chunk of `a` holds, about 250 of them, keep one more chunk of `a` on every round; there a `Sub` per call, which takes its first chunk from the heap, stays bounded.

### The heap and Alloc

The `Heap` is the process allocator, `sys.heap` in `Main`, in static memory that is never released. An `Alloc` is any allocator a heap container can take: `Heap.Alloc()` gives the heap's, the same `&Alloc` on every call, and [`std.mem`](mem.md) makes others.

- An `Alloc`'s identity is its address: a block goes back only to the `Alloc` it came from, and passing another one is a panic (code 12; [panics](runtime.md#panics)).
- An `Alloc` is neither `Send` nor `Share`, so no `&Alloc` goes to another task (E1002).
- `Alloc.Bytes` and `FreeBytes` hand out raw blocks and are callable only in `unsafe` ([allocators](mem.md#allocators)).

### Cell

A `Cell[T: Plain]` holds a plain value that you read with `Load` and replace with `Store` through a shared reference, without a lock. Both are O(1). A `Cell` is not `Share`, so a `&Cell` stays in one task: use `Mutex`, `Chan` or [`std.atomic`](atomic.md) across tasks.

## Strings and text

A `str` is a view of UTF-8 bytes: it borrows the bytes and never owns them. `Text` is a growable string in an arena, and `HeapText` its linear heap form. `Len` counts bytes, not characters.

| Task | Use |
|---|---|
| Measure and index | `Len`, `Byte(i)`, `AsBytes`, `s[i..j]` (byte offsets) |
| Search | `Find`, `Has`, `StartsWith`, `EndsWith` |
| Cut | `Trim`, `Lines`, `Split`, `SplitOnce`, `Words`, `Scalars` |
| Build | `Text.New(a)`, then `Push`, `PushChar`, `PushReplaced`; `Replace`, `ToLower`, `ToUpper`, `Arena.Fmt` |
| Parse | `ParseInt`, `ParseFloat`, `ParseFloatPrefix` |
| Bytes from outside | `str.From`, `str.FromC`, `Path` |

- `Len`, `AsBytes` and `Byte` are O(1). `Byte` out of range panics (code 2), and a slice `s[i..j]` off a scalar boundary panics (code 10).
- `Find` returns the byte offset of the first match, or `none`. `Find` and `Has` are O(n + m) in the worst case, and `""` matches at 0.
- `Trim` strips Unicode White_Space at both ends, and `Words` splits on it.
- `Lines` strips `\n` and `\r\n`. A final newline ends the last line without starting an empty one, and a lone `\r` is kept.
- `Split` keeps empty pieces: `"a,,b"` gives three, and `""` gives one. An empty separator panics (code 8).
- `"name=a=b".SplitOnce("=")` gives `some((before: "name", after: "a=b"))`. A missing separator gives `none`. An empty separator panics (code 8), as with `Split`. Both fields borrow the input, so a temporary separator can go out of scope immediately.
- `Lines`, `Split`, `Words` and `Scalars` are iterators that borrow the string. Each is O(1) to make and allocates nothing.

**Replacing.** `s.Replace(a, old, new)` returns a copy of `s` in `a` with every match of `old` replaced by `new`. `Text.PushReplaced(s, old, new)` appends the same bytes to a text.

- The leftmost match is replaced first, and matches never overlap. The search goes on after each replaced match and never looks inside `new`: replacing `"aa"` by `"b"` in `"aaa"` gives `"ba"`.
- An empty `old` matches at every scalar boundary: `"ab"` with `""` replaced by `"-"` is `"-a-b-"`.
- A valid `old` matches only whole scalars, so the result is valid UTF-8.
- A text with no match is still copied, since the result lives in `a`. On `OutOfMemory` a `Text` is unchanged.

**Case.** `ToLower` and `ToUpper` copy the string into an arena with Unicode's simple one-to-one case mapping, the same in every locale. So `"ÀÉ İ"` lowers to `"àé i"`, and `"Straße"` uppers to `"STRAßE"`: the simple mapping gives `ß` no uppercase. The result can be longer in bytes than the input: `Ⱥ` takes 2 bytes and its lowercase 3. For caseless matching use [`text.FoldCase`](text.md#normalization-and-case-folding).

**Parsing numbers.**

- `ParseInt` accepts an optional `+` or `-`, then ASCII digits, and nothing else. The value must fit an `i64`. Anything else is `ParseError`.
- `ParseFloat` accepts an optional sign, then `digits`, `digits.`, `digits.digits` or `.digits`, then an optional `e` or `E` exponent with an optional sign and at least one digit. It also accepts `inf` or `infinity` with an optional sign, or `nan` with no sign, in any case. Nothing else is accepted: no whitespace, `_` or hex form. The value is correctly rounded. A total underflow is a zero with the text's sign, and an overflow is `ParseError`.
- `ParseFloatPrefix` parses the longest prefix in `ParseFloat`'s grammar and returns the value and the bytes it used. With no prefix it gives a NaN and `used` 0. An overflow and a written `inf` both give an infinity, told apart by `s[0..used]`.

| Text | `used` | Value |
|---|---|---|
| `1.5e` | 3 | 1.5 |
| `5.` | 2 | 5 |
| `.`, `+` | 0 | NaN |
| `1_000` | 1 | 1 |
| `infinit` | 3 | inf |
| `-nan` | 0 | NaN |
| `1e400x` | 5 | inf |

**Bytes from outside.** `str.From(bytes)` checks that the bytes are UTF-8 and fails with `Utf8Error` if not: an overlong form, a surrogate, a value above U+10FFFF or a cut-off sequence. It is O(n). `str.FromC(p)` reads up to a NUL, is callable only in `unsafe`, and panics (code 8) when the bytes before the NUL are not UTF-8.

`f64.Bits` and `f64.FromBits`, and `f32`'s, reinterpret a float's bits exactly, NaN payloads and `-0` included, on every target and at compile time.

### Text and HeapText

A `Text` is a growable string in an arena that is always valid UTF-8. `Push`, `PushChar` and `PushReplaced` append, and leave the text unchanged on `OutOfMemory`.

- `Str()` is the text so far, borrowed from the `Text`. `Done()` ends the text and returns a `str` that borrows the arena instead, so a function returns `t.Done()` as a result `from a`. Returning `t.Str()` there returns a borrow of a local (E0603).
- `Drain(n)` removes the first `n` bytes. An `n` that is not on a scalar boundary panics (code 10).
- `Writer()` is a `Writer` that appends to the text. It has no buffer, so each write lands at once and `Flush` does nothing. It works at compile time.

`HeapText` is a `Text` on an allocator, linear, ended with `Free(a)`. `Push` is amortized O(`s.Len()`) and leaves the text unchanged on `OutOfMemory`. `HeapText` implements no `Eq` or `Hash`, so it is no `HeapMap` key: a text key belongs in a `TextMap`.

### Paths

A `Path` holds a file name's bytes as the OS gives them, which need not be UTF-8. `Str()` is the text when the bytes are UTF-8 and `none` otherwise (O(n)), and `AsBytes()` gives the bytes. On Windows a `Path` holds the name's WTF-8, so a name with an unpaired surrogate round-trips. [`std.fs`](fs.md#paths) lists names as `Path`s and opens them.

In a format hole a `Path` prints its bytes, with one U+FFFD for each maximal part that is not UTF-8, Unicode's recommended substitution. So a surrogate's WTF-8, `ED A0 80`, prints three.

| Path | `Base()` | `Parent()` |
|---|---|---|
| `""` | `.` | `none` |
| `/` | `/` | `none` |
| `a` | `a` | `none` |
| `a/b/` | `b` | `a` |

On Windows a drive and `\` count too:

| Path | `Base()` | `Parent()` |
|---|---|---|
| `C:` | `.` | `none` |
| `C:\` | `\` | `none` |
| `C:a` | `a` | `C:` |

On Linux a drive letter and `\` are ordinary bytes: `C:`, `C:\` and `C:a` are each one relative element, their own `Base()`, with `Parent()` `none`.

`Path.New(a, bytes)` copies the bytes into the arena; on `OutOfMemory` the arena is as it was. `p.Join(a, part)` appends `part` in one block of `a`:

- A part with a root and no drive (`/y`, and `\y` on Windows) replaces the path but keeps its drive: `C:\x` joined with `\y` is `C:\y`.
- A part with a drive (`D:\y`, `D:y`, Windows only) replaces the whole path.
- Otherwise the result is the path, the native separator, then the part. No separator is added when the path is empty, already ends with one, or is a drive alone (`C:`). An empty part gives a copy of the path.
- Drives are the only prefixes a `Path` knows: a UNC `\\server\share` prefix reads as a root of separators.

## Slices and lists

`Slice[T]` holds the methods of every sequence. Each `&self` method works on `[]T`, `[N]T`, `List` and `HeapList`. A `mut self` method needs a `mut []T`, a `var` array, a `List` or a `HeapList`, else E0507, or E0508 when the receiver is a local that is not `var`.

| Task | Use |
|---|---|
| Read and write elements | `Len`, `Get`, `GetMut`, `Swap`, `Reverse`, `Fill`, `CopyFrom` |
| Look for a value | `Has`, `Index` |
| Sort and search | `Sort`, `SortStable`, `SortStableInPlace`, `Search` |
| Cut into views | `SplitAt`, `GetPair`, `Chunks`, `ChunksMut` |
| Grow | `List[T].New(a)`, `HeapList[T].New(a)`, then `Push`, `Insert`, `Pop`, `Remove`, `Clear`; `List.Truncate`, `List.Resize` |
| Exchange places | `Swap(a, b)`, `Replace(dst, x)` |

- `Len` is O(1). `Get` and `GetMut` return `none` out of range and never panic; indexing `xs[i]` out of range panics (code 2).
- `Swap(i, j)` panics (code 2) out of range. `Reverse` is O(Len()).
- `Fill` and `CopyFrom` need a plain `T` (E0701 at the call). `CopyFrom` needs equal lengths, else it panics (code 2) before it stores anything, and works on overlapping views.
- `Has` and `Index` need `T: Eq` (E0701 at the call) and are O(Len()).

Calling a `mut` method on `xs[i]` borrows all of `xs` mutably, not one element, so `xs` must be a place you may change:

```mk
// verdict: ok
// a mut method on an indexed element borrows the sequence mut
struct Counter {
    n: int
}

Counter.Bump(mut self) {
    self.n += 1
}

BumpAt(cs: mut []Counter, i: int) {
    cs[i].Bump()
}

BumpFirst(a: &Arena): OutOfMemory!int {
    var cs := List[Counter].New(a)
    try cs.Push(Counter(n: 0))
    cs[0].Bump()
    return cs[0].n
}
```

```mk
// verdict: reject E0508
// the element borrows its array mut, so the array must be a var
struct Counter {
    n: int
}

Counter.Bump(mut self) {
    self.n += 1
}

First(): int {
    cs := [Counter(n: 0), Counter(n: 1)]
    cs[0].Bump() // <- E0508
    return cs[0].n
}
```

### Sorting and searching

`Sort`, `SortStableInPlace` and `Search` allocate nothing. `SortStable` takes scratch memory from the stack or the allocator it is given, and frees it before it returns.

- `Sort(less)` is unstable, O(n log n) in the worst case, and gives the same result on every run.
- `SortStable(a, less)` keeps equal elements in their order, and makes O(n log n) calls of `less` and moves. Past 20 elements it merges through scratch of `Len() / 2` elements or more: the whole slice while that is at most 8,000,000 bytes (at least 48 elements). Scratch that fits in 4,096 bytes, for a `T` aligned to at most 8, is on the stack, and other scratch comes from `a` (falling back to `Len() / 2` elements from `a` if `a` refuses the larger block) and is freed before it returns. Through scratch, input that is already sorted or strictly descending costs O(n). Below 21 elements, for a zero-size `T`, or when the scratch fits on the stack, it allocates nothing from `a`. `OutOfMemory` comes before any element moves, so the slice is unchanged. It is Rust's driftsort.
- `SortStableInPlace(less)` keeps equal elements in their order with no allocator or heap allocation, using an in-place block merge sort with a fixed stack cache of at most 4,096 bytes for elements aligned to at most 8. O(n log n) calls of `less` and moves on every input. Works at compile time and in freestanding programs.
- `Search(&key, less)` takes a slice where `less(element, key)` holds for a prefix and not after it. It returns that prefix's length: the first index whose element is not less than the key, or `Len()` when there is none. That is where the key goes to keep the slice sorted. It is O(log n). On any other slice it returns some index from 0 to `Len()`.
- A `less` that needs context is a function literal that reads it, so there are no `With` forms ([Sort with a comparison that reads locals](../cookbook.md#sort-with-a-comparison-that-reads-locals)).
- Sorting a slice that holds a float NaN leaves a permutation of its elements in an unspecified order, with no panic.

### Views into a slice

- `SplitAt(i)` gives the two halves, both `mut`, and panics (code 2) unless `0 <= i <= Len()`.
- `Chunks(n)` and `ChunksMut(n)` give pieces of `n` elements, the last one shorter, and panic (code 8) unless `n` is above 0.
- `GetPair(i, j)` gives two elements `mut`, and returns `none` when the two indices are equal or out of range, never a panic.

`SplitAt`, `ChunksMut` and `GetPair` hand out disjoint exclusive views that the loan rules cannot prove disjoint. Each holds the loan its receiver held, and the library answers for the disjointness ([views](../memory.md#r18-view-and-ends)). So each piece of `ChunksMut` can go to its own task: `for c in xs.ChunksMut(n) { s.go Work(c) }` ([Process a slice in parallel](../cookbook.md#process-a-slice-in-parallel)).

### List and HeapList

A `List[T]` is a growable array in an arena; `HeapList[T]` is its linear heap form, which takes the allocator at every call that may allocate.

- `New` allocates nothing until the first `Push`. A growth takes the capacity to the largest of 4, twice the old capacity, and what is needed. In an arena it grows in place when the buffer is the arena's last block, and otherwise leaves the old buffer to the arena.
- `Push` is amortized O(1). `Pop` is O(1). `Insert(i, x)` and `Remove(i)` are O(Len() - i) and panic (code 2) out of range; `Insert` checks before it grows.
- `List.Truncate(n)` keeps the first `n` elements; an `n` at or above `Len()` changes nothing. It panics (code 2) for a negative `n`. `HeapList` has no `Truncate`.
- `List.Resize(n, x)` makes the length `n`: it keeps the first `n` elements and appends copies of `x`, growing at most once. `T` must be plain (E0701 otherwise). It panics (code 2) for a negative `n`. It is how a body generic in a plain `T` fills a list with one value: there a second `Push(x)` of the same `x` is E0604, since it would store `x` through the copies already in the list, and a plain `T` may hold a `&Cell` that `Push`'s `x into self` lets it write.
- `Clear` empties the list and keeps its capacity.
- `List.Done()` ends the list and returns a `mut []T` that lives as long as the arena.
- `HeapList.Extend(a, xs)` copies a plain `T` (E0701 otherwise), grows at most once, and leaves the list unchanged on `OutOfMemory`.
- A list prints as `[a, b, c]`.

```mk
// verdict: ok
// Resets l to n copies of val, for any plain T: one Resize stores val once.
Refill[T: Plain](l: mut List[T], n: int, val: T into l): OutOfMemory!void {
    l.Clear()
    try l.Resize(n, val)
}

export Main(sys: &System): OutOfMemory!void {
    var words := List[u64].New(sys.arena)
    try Refill(mut words, 4, 0)
    var names := List[str].New(sys.arena)
    try Refill(mut names, 2, "none")
}
```

### Swap and Replace

`Swap(a, b)` exchanges two places. `Replace(dst, x)` puts `x` in `dst` and returns what was there, which is how a linear value leaves a place. Both work for any `T`, linear included, allocate nothing and cannot fail.

### Iterators

A `for` loop takes an iterator by value, never through `&` or `mut` (E0301; [iterators](../reference.md#iterators)). An iterator over a container borrows it while it lives. A linear iterator holds an OS handle, as [`fs.Walker`](fs.md#walk-a-directory) does. Its `Next` takes `io` or an arena or can fail, so the loop is `for e := try it.Next(a, io) { }`, and its own `Close` releases it.

## Maps and sets

`Map[K, V]` and `Set[T]` live in an arena. `HeapMap[K, V]` is the linear heap form of `Map`, and `TextMap[V]` a heap map keyed by text that keeps a new key of up to 15 bytes in its table and copies a longer one into its allocator. Keys need `Hash + Eq`.

| Task | Use |
|---|---|
| Make | `New(a)`, or `NewKeyed(a, key)` for keys from untrusted input |
| Store | `Set(k, v)`, `Replace(k, v)`, `Ensure(k, v)`; `Set.Add(x)` |
| Look up | `Get`, `GetMut`, `Has`, `Len` |
| Delete | `Remove` |
| Walk | `for (key, value) in m.Iter()`: `key` is `&K` and `value` is `&V` |

- `Get`, `GetMut`, `Has`, `Set`, `Replace`, `Ensure` and `Remove` are expected O(1). `Get` returns `?&V`.
- `Set` on a present key replaces the value. It keeps the stored key and drops the given key and the old value.
- `Replace` stores the value and returns the old one, so `V` may be linear.
- `Ensure(k, v)` returns the value for `k` as `mut V`, inserting `v` first if the key is absent.
- `Set.Add(x)` returns `false` when `x` was already a member.
- `Remove` drops the stored key. In an arena map its memory stays in the arena, and a grown table leaves the old one there.
- On a `HeapMap` or `TextMap`, `Set`, `Ensure` and `Replace` take the allocator and free what they displace with it ([releasing](#releasing-what-a-container-holds)). `TextMap.Remove(a, k)` frees its copy of the key, if it made one. A `TextMap`'s operations are O(1 + `k.Len()`).
- `Iter` is O(1) and a full walk O(capacity). A table never shrinks, so one `Next` on a sparse table may read up to capacity / 8 groups.

**Which types compare and hash.** A reference `&T` has `Eq`, `Ord` and `Hash` when `T` has them, and they look at the value it points to, never the address. So `Map[&K, V]` works when `K: Hash + Eq`. The primitive types implement:

| Interface | Implemented by |
|---|---|
| `Format`, `Eq` | the integers, `f32`, `f64`, `bool`, `char`, `str` |
| `Ord` | the integers, `f32`, `f64`, `char`, `str` |
| `Hash` | the integers, `bool`, `char`, `str` |

On floats `Eq` and `Ord` are IEEE `==` and `<`, so generic code and operator code agree. A NaN equals nothing, itself included, and is not ordered. The floats have no `Hash`, so no float is a `Map` or `Set` key. [`derive`](derive.md) implements `Eq`, `Hash` and `Format` for your own types.

**Iteration order and hashing.** `Map`, `Set`, `HeapMap` and `TextMap` iterate in an unspecified order. The order depends only on the key type, the sequence of insertions and removals, and the map's hash key if it has one. Sort the keys when order matters.

A map made with `New` hashes with one fixed fast function and no key. Its order is the same in every run of the program and at compile time, but the function may change between releases. A fixed function resists nothing: whoever picks n keys can give them one probe sequence and make each operation O(n). For keys from untrusted input, such as network or file data, make the map or set with `NewKeyed` and a `HashKey`:

- `HashKey.New(io)` reads 16 bytes with `Io.Random`. A keyed map hashes with SipHash-1-3 under that key, and its order varies from process to process unless `io` is simulated. Compile-time evaluation runs no `Io` operation, so it never makes one.
- `HashKey.Of(k0, k1)` makes the key from chosen words, for output that must repeat, such as a file format, a test vector or a checksum. A key whose words are known protects nothing.
- A zero-filled `HashKey` is the key (0, 0). Under it, `hash.Sip13` gives the values of Rust's `DefaultHasher::new()`, which makes them test vectors.

`HashKey` is plain and has no public fields. A `Hash` implementation feeds a `Hasher` with `Bytes` (O(n)) and `U64` (O(1)); the hash depends only on the byte stream, however the calls split it.

## Releasing what a container holds

An arena container (`List`, `Map`, `Set`, `Text`) needs no release: the arena frees it. A heap container (`HeapList`, `HeapMap`, `TextMap`, `HeapText`) and a `Box` are linear, so the compiler rejects a path that drops one. Each remembers the allocator it was made with, and a call that allocates or frees with another one panics (code 12).

**`Free`.** `HeapList`, `HeapMap`, `TextMap`, `HeapText`, `Box` and `Chan` implement `Free` when each element type is not linear or is itself `Free`; so do [`coll`](coll.md#freeing-a-heap-container)'s `HeapDeque` and `HeapBTree`. Their `Free(a)` frees every element, then the storage, so it never panics. It is O(1) when the elements have nothing to release, since nothing is walked; otherwise it walks the elements and calls their own `Free`.

```mk
// verdict: ok
// a map of heap strings is overwritten and freed in one call each
Store(a: &Alloc into m, m: mut TextMap[HeapText], key: str, value: str): OutOfMemory!void {
    var v := HeapText.New(a)
    onerr v.Free(a)
    try v.Push(a, value)
    try m.Set(a, key, v)
}

Release(a: &Alloc, m: TextMap[HeapText]) {
    m.Free(a)
}
```

When a call on a heap container overwrites an entry, it frees what it displaces with the allocator `a` it takes:

- `Set` on a present key frees the old value and the key you passed.
- `Ensure` on a present key frees the key and the value you passed.
- `Replace` frees the key you passed, and returns the old value to you.

**Linear elements.** Where a key or element type is linear and not `Free`, `Free`, `Set` and `Ensure` are E0701 at the call, and so is `Replace` for such a key. The diagnostic names the alternatives:

- `Replace` returns the old value for the caller to consume.
- `FreeEach(a, release)` hands each element to `release`, a function literal that may capture, then frees the storage.
- `FreeEmpty(a)` frees an empty container and panics (code 11) on one that is not empty.
- A `Box` gives its value back with `Take(a)`.

```mk
// verdict: reject E0701
// a container of files is not freed by Free
import std.fs

Leak(a: &Alloc, files: HeapList[fs.File]) {
    files.Free(a) // <- E0701
}
```

A call with no allocator cannot free what it drops, so the dropped type must not be linear. These are E0701 at the call when it is:

- `Remove` by key on a map, a `Set` or a [`coll.BTree`](coll.md), which drops the stored key.
- `Store` on a `Guard` or an [`atomic.SpinGuard`](atomic.md#spin). `Replace(g.Mut(), x)` gives the old value back instead.
- [`mem.Pool`](mem.md#pools-and-handles)'s `Free` and `Sweep`.
- An arena `Map`'s `Set`, `Ensure` and `Replace`, an arena `BTree`'s `Set` and `Replace`, and `Set.Add`, for the key or element they would drop.

```mk
// verdict: reject E0701
// a call that would drop a linear key, with no `a` or no Free to release it, is refused
linear struct Lease {
    id: u64
}

impl Eq for Lease {
    Eq(&self, other: &Lease): bool {
        return self.id == other.id
    }
}

impl Hash for Lease {
    Hash(&self, h: mut Hasher) {
        h.U64(self.id)
    }
}

Prune(a: &Alloc, m: mut HeapMap[Lease, int], k: &Lease): OutOfMemory!void {
    _ = m.Remove(k)                          // <- E0701
    old := try m.Replace(a, Lease(id: 1), 2) // <- E0701
    _ = old
    v := try m.Ensure(a, Lease(id: 2), 3) // <- E0701
    *v += 1
}
```

So a linear value is never dropped behind your back: either the call refuses to compile, or one of these four checks at run time and panics:

- `FreeEmpty` of a container that is not empty (code 11).
- `Clear` of a `List` (code 8) or a `HeapList` (code 11) that still holds linear elements. An emptied list clears.
- A `List.Truncate` that would drop a linear element (code 8).
- The last [`sync.Shared`](sync.md#shared)'s `Free` of a linear value. A handle that is not the last frees.

**Handles.** A value that owns an OS handle or a peer's protocol state, such as an `fs.Walker`, a `proc.Child` or a `net.Conn`, does not implement `Free`. It has its own `Close`. Release a container of such values with `FreeEach`.

- A handle keeps what its constructor's `from` clause names, and nothing else. Each module's page says what its handles keep.
- Its consuming calls take no allocator and release everything it owns.
- No consuming call has a run-time precondition: `Child.Wait(self)` and an encoder's `Finish(self)` can always be called.

**`Box[T]`** holds one value on an allocator. It is linear: `Take(a)` frees the box and returns the value, and `Free(a)` releases both, which compiles only for a `T` that is not linear or is `Free` (E0701). `Ref` and `Mut` borrow the value. A zero-size `T` allocates nothing. A `Box` is `Send` when `T` is, and `Share` when `T` is.

**Bounds a signature cannot spell.** A call that misses one is E0701 at the call:

| Function | Needs |
|---|---|
| `Has`, `Index` | `T: Eq` |
| `Fill`, `CopyFrom`, `HeapList.Extend`, `List.Resize` | `T: Plain` |
| `Arena.Slice[T]`, `Arena.SliceAligned[T]` | A zero-valid `T`: its all-zero bytes are a value of it. Both fill their elements with zeros |
| `Free`, `Set`, `Ensure`, `Replace`, `Remove` and others | The rules above |

A zero-valid type is always plain. These types are zero-valid:

- the integers, the floats, `bool`, `char`, `void`, raw pointers and every `?T`;
- an array, record, struct or `E!T` whose parts are all zero-valid.

These are not:

- a struct with an `invariant`;
- an enum with no variant of discriminant 0, or whose variant 0 has a part that is not zero-valid;
- a reference, a `fn` value, `str`, a slice, an error-set value or a type parameter.

## Optionals and errors

`?T` has `Must()`, `IsSome()` and `IsNone()`, and `E!T` has `Must()`. `Must` returns the value or panics. An `else` block gives a default, on `?T` and `E!T` alike: `i := text.Find(":") else { text.Len() }`. There is no `Or` method. The rules for `try`, `else`, `match` and `onerr` are in [reference section 9](../reference.md#9-errors).

```mk
// verdict: ok
// IsSome, IsNone and a default from an else block
Port(given: ?int): int {
    if given.IsNone() {
        return 8080
    }
    return given else { 0 }
}

Name(text: str): str {
    at := text.Find("=") else { text.Len() }
    return text[..at]
}
```

The prelude declares the errors many modules share. None carries a payload.

| Error | Meaning |
|---|---|
| `OutOfMemory` | An allocator could not satisfy a request |
| `Canceled` | The operation's scope was cancelled, or its deadline passed |
| `Closed` | The channel or handle was closed |
| `ReadError` | A `Reader`'s source failed, or its bytes were not what the call needs |
| `WriteError` | A `Writer`'s destination failed |
| `ParseError` | Text was not in the expected form |
| `Utf8Error` | Bytes were not valid UTF-8 |

A module's own error set adds these where its calls can return them, as `fs.Error` adds `Canceled` and `Utf8Error`.

## Formatting and printing

`Print(w, f, args)` writes the format string `f` to a `Writer`, filling each hole from the record `args`. The format is checked at compile time. `Print` is O(output) and allocates nothing. The record must be plain, so a `List` or another value that is not plain goes in by reference, `(names: &names)` (E0701 otherwise).

| Task | Use |
|---|---|
| Print to standard output or standard error | `sys.Print(f, args)`, `sys.PrintErr(f, args)` |
| Print to any `Writer` | `Print(w, f, args)` |
| Build a `str` | `arena.Fmt(f, args)` |
| Print your own type | `impl Format for T`, or `derive.Format` |
| Choose width, alignment, sign, digits, radix | A spec after the name: `{price:>8.2}`, `{flags:08b}` |

**What has `Format`.** The numbers, `str`, `bool` and `char` implement `Format`, and so does a reference to any of them. `?T`, `[]T`, `List[T]` and `HeapList[T]` implement it when `T` does:

- An optional prints `some(v)` or `none`.
- A slice or list prints `[a, b, c]`, or `[]` when empty. Each element prints through its own `Format`, so a `str` inside prints as written, without quotes.
- An error whose payload fields all have `Format` has it, and so does an error set whose members all do. It writes its name, then, if it has payload fields, each `name: value` in declaration order, joined by `, ` in parentheses: `NotFound(id: 3)`. An error-set value writes its member, and a module's error prints unqualified.
- Your own type gets `Format` from an `impl` ([Implement Format for your own type](../cookbook.md#implement-format-for-your-own-type)) or from `derive.Format`.

```mk
// verdict: reject E0706
// an error whose payload has no Format has none
struct P {
    n: int
}

error Bad(p: P)

ShowBad(sys: &System, e: Bad) {
    sys.Print("{e}\n", (e)) else {} // <- E0706
}
```

### Holes

A format string holds holes, `{name}` or `{name:spec}`. Write `{{` and `}}` for literal braces.

- Each hole names a field of the argument record, and each field of the record is used by some hole. A hole names a field or a field path, never an expression, so the format stays a constant.
- A field path, `{r.name}`, starts at a field of the record and goes through exported fields of structs and records, at any depth and through shared references. So a row prints from `(r)`.
- A path ends at a field whose type implements `Format`. A top-level field that only paths use counts as used and needs no `Format` of its own, so a struct with no `Format` prints by its fields.

Each of these is E0706 at the format argument:

- a bare hole whose field has no `Format`;
- a path through an unexported field, or through a type with no fields;
- a path that ends at a type with no `Format`;
- a hole that names no field, or a field that no hole uses.

```mk
// verdict: reject E0706
// a path names exported fields only
struct Secret {
    export id: int
    key: int
}

impl Format for Secret {
    Format(&self, w: mut Writer): WriteError!void {
        try Print(w, "{id}", (id: self.id))
    }
}

Leak(w: mut Writer, s: Secret): WriteError!void {
    try Print(w, "{s.key}\n", (s)) // <- E0706
}
```

```mk
// verdict: reject E0706
// a bare hole still needs Format of its type
struct Part {
    export name: str
}

Show(sys: &System, p: Part): WriteError!void {
    try sys.Print("{p}\n", (p)) // <- E0706
}
```

### Specs

A spec is `[[fill]align][+][0][width][.N][type]`. Each part is optional, and they come in that order.

| Part | Meaning |
|---|---|
| `width` | From 1 to 255. Pads the value to that many bytes, not characters. Works on any field, a user `Format` included. A value at or past its width is written whole |
| `fill` | One printable ASCII byte other than `{` and `}`, written only before an `align`. A space by default |
| `align` | `<` left, `>` right, `^` both sides, the odd byte on the right. With no `align`, numbers go right and everything else left |
| `+` | Gives a non-negative number a sign too. Integer and float fields only |
| `0` | Writes zeros after the sign up to the width. Integer and float fields only, and takes no `align` |
| `.N` | `N` from 0 to 99 digits after the point. Float fields only |
| `type` | `x`, `X`, `b` or `o`: the bits at the declared width in lowercase hex, uppercase hex, binary or octal, with no prefix. Integer fields only, and takes no `+`. `{n:x}` of an `i8` holding -1 is `ff` |

An `align` or a `0` needs a width. There is no `?`, `#` or `e` spec: `derive.Format` is the structural form. Any other spec is E0706 at the format argument.

```mk
// verdict: reject E0706
// + and 0 are for numbers, .N for floats, a radix for integers, and a width is 1 to 255; a brace is no fill
Specs(w: mut Writer, name: str, n: int, x: f64): WriteError!void {
    try Print(w, "{name:+}\n", (name))   // <- E0706
    try Print(w, "{name:05}\n", (name))  // <- E0706
    try Print(w, "{n:.2}\n", (n))        // <- E0706
    try Print(w, "{x:x}\n", (x))         // <- E0706
    try Print(w, "{n:+x}\n", (n))        // <- E0706
    try Print(w, "{n:256}\n", (n))       // <- E0706
    try Print(w, "{n:<05}\n", (n))       // <- E0706
    try Print(w, "{n:?}\n", (n))         // <- E0706
    try Print(w, "{name:{>5}\n", (name)) // <- E0706
}
```

**Padding.**

- To pad a hole, `Print` first formats its value into a counting `Writer` to measure it, then formats it again. So a value formats twice, and your own `Format` must write the same bytes both times.
- A hole with `0` pads as it writes and is not measured.
- A program that gives no hole a width links no padding code.

**Floats.**

- With no spec, a float prints the fewest digits that read back to the same value.
- A decimal exponent from -4 to 20 prints in fixed notation. Any other prints as `d.ddde+XX` or `d.ddde-XX`, with at least two exponent digits: `1e+21`, `1e-05`.
- `nan`, `inf`, `-inf` and `-0` print as written, and `nan` never has a sign.
- `{x:.N}` rounds the exact value to the nearest, ties to even.
- Zero padding skips `nan` and `inf`: `{x:+08.2}` prints `     nan`, `    +inf` and `-0000.00` for nan, inf and -0.

### Leaving out the record

A string with no holes takes no record: `Print(mut out, "done\n")` means `Print(mut out, "done\n", ())`, and `arena.Fmt("none")` means `arena.Fmt("none", ())`. Both forms are accepted, and `mk fmt` writes the short form after a bare `Print`.

```mk
// verdict: reject E0706
// a string with holes still needs its record
Count(w: mut Writer, n: int): WriteError!void {
    try Print(w, "{n} items\n") // <- E0706
}
```

**Forwarders.** A function has `Print`'s shape when its last two parameters are a `const` `str` and a type parameter bounded by `Fields[Format]`. `System.Print`, `log.Logger.Info` and your own forwarders have it.

- A call of such a function may leave the record out too: `sys.Print("done\n")`, `lg.Info("starting")`. Only the signature decides this, never the body.
- `mk fix` writes the short form for such a call once the package checks clean. It does so when the call's text is one string literal with no hole and the call holds no comment.

```mk
// verdict: ok
// sys.Print, and any callee shaped like Print, take no record when the string has no holes
import std.log

Note[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void {
    try w.Str("note: ")
    try Print(w, f, args)
}

Run(sys: &System): WriteError!void {
    try sys.Print("starting\n")
    try sys.PrintErr("{n} warnings\n", (n: 0))
    var out := sys.Stdout()
    try Note(mut out, "ready\n")
    var lg := log.Logger.New(mut out, log.Level.Info)
    try lg.Info("listening")
}
```

- The record can be left out only while its type is open or `()`. A type argument or the receiver's type may fix it to another record, as `Print[(x: int)](mut out, "hi\n")` does. Then the call is one argument short (E0304).
- A forwarder's format is checked at each call of the forwarder, against that call's text. Take `Info[A](const f: str, args: A)`, which calls `Print(w, f, args)`. A hole that names no field, a field with no hole, or a bad spec is E0706 at the call of `Info` that wrote the text. Inside `Info`, `Print(w, f)` takes no record when that text has no hole.

**The record inside a forwarder.** Take a record whose type is a type parameter of the function itself. Its `Fields[Format]` is checked hole by hole against each call's text, not field by field. Each hole's value must implement `Format`: the top-level field, or a path's last field. The record's other bounds, such as `Plain`, hold as written.

Inside the body, the record may only be passed on, with `f` unchanged, as the record of another call of the same shape. Any other use that needs `Fields[Format]` is E0705 at the definition:

- looping over the record with `fields`;
- passing the record, or its type, to a generic function or type bounded by `Fields[Format]`;
- passing `f` with another record or with none, or the record with another format;
- passing `f` to any other `const` parameter.

Uses that need only `Plain`, and reading `f` as a value, are fine.

```mk
// verdict: reject E0705
// a body of the Print shape only passes its record on, with its format
Dump[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void {
    for name, value in fields(&args) { // <- E0705
        try w.Str(name)
        try value.Format(w)
    }
}
```

Two other shapes hold the bound as written, so neither can carry a struct with no `Format`:

- A record type parameter that belongs to the owner, as in `L[A].P(&self, const f: str, args: A)`. The bound is checked where `L[...]` is formed and at the call, and the text is checked per instance.
- A forwarder of another shape, such as `G(sys: &System, const f: str, r: Row)` calling `sys.Print(f, (r))`.

Whether a call is accepted depends on the callee's signature and the text alone, never on a body.

## Reading and writing

A `Reader` and a `Writer` are views over a byte source or sink, with an optional buffer that the caller passes in. They borrow their source or destination while they live. A function that takes `w: mut Writer` writes to any of them.

| Task | Use |
|---|---|
| Get one | `sys.Stdin()`, `sys.Stdout()`, `sys.Stderr()`, `Text.Writer()`, `Reader.Of(bytes)`, [`File.Reader` and `File.Writer`](fs.md#files-and-other-handles), the network streams |
| Write | `Writer.Str`, `Writer.Bytes`, `Print(w, f, args)`, then `Writer.Flush` |
| Read | `Reader.Read`, `Reader.ReadAll`, `Reader.Until`, `Reader.Line` |
| The program's arguments and environment | `sys.args`, `sys.Env(name)` |

**Writing.** `Str` and `Bytes` write, and so does `Print(w, ...)`; `Flush` sends what a buffer holds. Flush a buffered `Writer` before you drop it, or the buffered bytes are lost. A failed write drops its bytes, and the next write starts clean.

**Reading.**

- `Read(buf)` makes one read and may return fewer bytes than asked; it returns 0 at end of input. `ReadAll(buf)` reads until `buf` is full or the input ends.
- `Until(delim, buf)` copies bytes up to and including the first `delim`, or until `buf` is full, and returns 0 at end of input. It never takes bytes past `delim` from the reader.
- `Line(a)` returns the next line, copied into `a`, or `none` at end of input. It strips `\n` and `\r\n`, returns a last line that has no newline, and keeps a lone `\r` at its end. It fails with `ReadError` on a line longer than the buffer, on bytes that are not UTF-8, or when the source fails. A reader with no inline buffer (`Stdin()`, `Reader.Of`) takes lines up to 64 KiB.
- `Reader.Of(b)` reads the bytes of `b` from the first. It holds `b`'s loan, never writes `b` and takes no `Io`, so it runs at compile time.

**Interruption.** A `Reader` or `Writer` reports an interruption by a cancel or a deadline as `ReadError` or `WriteError`. Whether it was a timeout is read from the scope with `Expired()` ([deadlines](#deadlines)). See [cancel-safety](#cancel-safety).

[`text.Capped` and `text.Sink`](text.md#writers-with-no-allocator) make a `Writer` with no arena and no `Io`.

### System and the standard streams

`Main` receives a `&System`: `args` (`args[0]` is the program's path, as it was started), `io`, `arena` (lives as long as the program) and `heap` ([the system handle](README.md#4-io-and-the-system-handle)).

- `Stdout()` and `Stderr()` each have one 8 KiB buffer, shared by every `Writer` taken from them, so taking a `Writer` for each call costs nothing. Both are flushed when `Main` returns or `proc.Exit` runs, stdout first, and before a panic report. `Flush` forces it earlier.
- If the last flush of stdout fails, the process writes `error: WriteError on stdout` to stderr, when stderr's own flush worked. If either flush fails, an exit status of 0 becomes 1.
- Writes to stdout and stderr, flushes included, are not cancellation points. A loop that only prints checks for a cancel with `io.Yield()` or `io.Sleep(time.Millis(0))`.
- `sys.Print(f, args)` is `Print` over a `Writer` from `sys.Stdout()`, and `sys.PrintErr` the same over `sys.Stderr()`. A call costs one `Print`, and fails with `WriteError` or a user `Format`'s error.
- `Stdin()` readers share the process's one 64 KiB input buffer. A read that must wait flushes stdout and stderr first, so a prompt shows. Readers on several tasks get whole pieces, since each read holds the stream's lock.
- `Env(name)` is the variable as it was when the process started; of two equal names the first wins. On Windows names compare without ASCII case.

## Io, scopes and cancellation

An `Io` is the handle through which a function reaches the OS: files, sockets, clocks, sleep, randomness and tasks. It is a plain value: copy it and share it freely. It holds no loan and names no task. The same program runs on the native runtime or under [`std.sim`](sim.md)'s simulated OS, and a function that takes an `Io` never runs at compile time. Why an `Io` is a parameter is in [the library guide](README.md#4-io-and-the-system-handle).

| Call | What it does |
|---|---|
| `io.Sleep(d)` | Suspends the task for at least `d`; `Canceled` if its scope is cancelled first. A zero or negative `d` returns at once and is still a cancellation point |
| `io.Yield()` | Lets other tasks run first; `Canceled` in a cancelled scope |
| `io.Random(buf)` | Fills `buf` from the OS's random source; on `Canceled`, `buf` holds anything |
| `io.Scope()`, `io.ScopeOf[E]()`, `io.ScopeWith(l)` | Opens a task scope |
| `io.NoCancel()` | An `Io` that ignores the cancels of the enclosing scopes |

### Scopes

A program's tasks run in scopes. The reference teaches them: [scopes](../reference.md#scopes), [tasks that fail](../reference.md#tasks-that-fail-scopeofe), [cancellation](../reference.md#cancellation), [deadlines](../reference.md#deadlines) and [limits and stacks](../reference.md#limits-and-stacks). This section adds what the prelude's types promise beyond that.

- A scope's record lives in the caller's frame: opening one allocates nothing.
- `io.ScopeWith(l)` with `l.tasks` below 1 panics (code 8). So does a `StackSize` above 512 MiB, or a stack reservation the OS refuses.
- `StackSize` is not open, so a `match` on it outside std covers `Bytes`.

### Deadlines

A deadline is a cancel armed on a timer, and a timeout on an operation is a scope around it:

```mk
// verdict: ok
// sleep for at most 20 ms, then tell a timeout from a cancel
import std.time

Nap(io: Io): bool {
    var timed_out := false
    scope s := io.Scope() {
        s.CancelAfter(time.Millis(20))
        io.Sleep(time.Seconds(5)) else {} // Canceled when the deadline passes
        timed_out = s.Expired()
    }
    return timed_out
}
```

`s.CancelAfter(d)` arms a timer the scope owns. When it fires, the scope is cancelled exactly as by `s.Cancel()`. The reference gives the rules every deadline follows ([deadlines](../reference.md#deadlines)). Beyond them:

- `Expired()` stays true until the block exits. A scope cancelled first disarms its timer.
- `Remaining()` is never below zero. Deadlines above a `NoCancel` boundary do not count.
- No `Scope` function takes a `time.Instant`, and an `Io` holds no deadline: every `Io` passes through the current task and its scopes.
- To observe a deadline after a join, nest: the deadline on an outer scope, the tasks in an inner one, and `Expired()` read after the inner block. Under `ScopeOf[E]`, a task given `&s` maps its `Canceled` to `time.TimedOut` when `s.Expired()` ([Run tasks with a deadline](../cookbook.md#run-tasks-with-a-deadline)).

### Masking

`io.NoCancel()` returns an `Io` that ignores the `Cancel`, early exit and deadlines of the scopes that enclose the call. Use it for work that must finish, such as cleanup: a sleep on the masked `Io` completes in a cancelled scope.

- A scope you open from the masked `Io` is a new boundary. Its own `Cancel` and deadline cancel the operations under it, and its `Expired()` says which one it was.
- So only such a boundary, or a scope nested under one, cancels an operation on the masked `Io`. The boundary is opened after the mask, from this `Io` or one masked later. A scope opened from an unmasked `Io` below the mask never cancels it.
- A scope opened inside a scope of the masked `Io` belongs to that scope, whichever `Io` opened it. The scopes around the `NoCancel` call do not cancel the operations under it.
- An operation on the unmasked `Io` that is under no scope of the masked one is still cancelled by the outer scopes.
- To keep an outer deadline in force, open its scope outside the masked one, and pass the masked `Io` only to the operations that must finish.

The check before an operation follows the same tree. Take a cancelled scope opened from the masked `Io`. Under it, a `Mutex.Lock` of a free mutex, a `Send` with room and a `Recv` with a value buffered all return `Canceled` when made with that `Io`.

### Cancel-safety

Every operation a cancel or a deadline can interrupt falls in one of three classes, whether or not `Canceled` is in its error set. The operations of other modules fall in the same classes.

| Class | After an interruption | In the prelude |
|---|---|---|
| Repeatable | Nothing was consumed; call again | `Chan.Recv`, `Chan.Send` (the value stays with the sender), `Mutex.Lock`, `Io.Sleep`, `Reader.Read` and `Reader.Line` on a buffered reader |
| Prefix | An unknown prefix took effect; close the stream | `Reader.Until`, `Reader.ReadAll`, every `Writer` method over a stream |
| Failed | Every later call but `Abandon` fails, and a call blocked in another task returns the same failure at once | None. Connections and streams of [`std.net`](net.md), [`std.http`](http.md), [`std.tls`](tls.md), [`std.websocket`](websocket.md) and [`std.compress`](compress.md) can end here |

## Channels and locks

The prelude has `Chan` and `Mutex`. [`std.sync`](sync.md) adds read-write locks, semaphores, notifications, barriers, one-time initialization, `Select` over channels and `Shared` values, and [`std.atomic`](atomic.md) atomic words. How tasks map to threads is on [`std.runtime`](runtime.md#carriers-tasks-and-stacks)'s page.

| Task | Use |
|---|---|
| Start tasks and wait for them | `scope s := io.Scope() { s.go F(...) }` ([scopes](#scopes)) |
| Pass values between tasks | `Chan[T].New(a, capacity)`, then `Send`, `Recv`, `TrySend`, `Close`, `Free` |
| Share state between tasks | `Mutex[T].New(x)`, then `Lock`, and the `Guard`'s `Ref`, `Mut`, `Store`, `Unlock` |
| A value one task reads and replaces | `Cell[T]` ([cell](#cell)) |

### Chan

A `Chan[T]` carries values between tasks in FIFO order. `Chan[T].New(a, capacity)` makes one with a buffer of `capacity` values from the allocator `a`. A capacity of 0 is a rendezvous, and a negative capacity panics (code 8). Share a `&Chan` with the tasks that use it: a `Chan` is `Send` and `Share` when `T` is `Send`.

- `Send(io, x)` waits for room. `Recv(io)` waits for a value. Both check for a cancel first. So in a cancelled scope a `Send` with room and a `Recv` with a value buffered return `Canceled` and change nothing. One already done when the cancel lands succeeds. On `Closed` or `Canceled` the caller keeps `x`. A cleanup that drains a channel passes `io.NoCancel()`.
- `Recv` drains what is buffered after `Close`, then returns `Closed`.
- `Close` is idempotent and wakes every waiter, and every `sync.Select` watching the channel.
- `TrySend(x)` sends without waiting for room. It gives `none` when it sent, or `x` back when the channel is full, so a full channel never drops it. On `Closed` the caller keeps `x`. It takes no `Io` and is no cancellation point. At capacity 0 it sends only to a task waiting in `Recv`; a task waiting in [`sync.Select`](sync.md#select) does not count.
- End a channel with `Free(a)`, `FreeEmpty(a)` (code 11 when values remain) or `FreeEach(a, release)`. A `Chan` is not linear, so a heap channel that is never freed leaks; one on an arena's `Alloc` goes with the arena.

### Mutex and Guard

A `Mutex[T]` is a lock around a value of `T`. `Mutex.New(x)` allocates nothing, so it can initialize a `var` global. A `Mutex` is `Share` when `T` is `Send`, so tasks share a `&Mutex`.

- `Lock(io)` waits for the lock and returns a `Guard[T]`, the only way to reach the value. It checks for a cancel first, and after `Canceled` the lock is not held.
- A `Guard` is linear: `Ref` and `Mut` borrow the value, and `Unlock` releases the lock. `Store(x)` replaces the value and needs a `T` that is not linear (E0701); otherwise write `Replace(g.Mut(), x)`.
- Waiters queue in FIFO order, but a free lock goes to whichever task takes it first. A waiter that has waited 1 ms and then loses the lock once more goes to the front, and the next `Unlock` hands the lock to it directly.
- `Into()` ends the mutex and returns the value.

There is no condition variable: a task that waits for a condition under a `Mutex` unlocks, waits on a `sync.Notify`, and locks again ([Wait for a condition](sync.md#wait-for-a-condition)). For shared counters and flags, see [Share state between tasks](../cookbook.md#share-state-between-tasks).

## Targets and generators

| Task | Use |
|---|---|
| Ask what the program is built for | `Target.arch`, `Target.os`, `Target.pointer_bytes`, `Target.vector_bits` |
| Read a type's shape in a generator | `Type.Fields`, `Type.Variants`, `Type.Methods`, `Type.Kind` |
| Emit code from a generator | `quote { }` to make a `Code`, `Gen.Emit`, `Gen.Fresh`, `Gen.Ident` |
| Report a generator error | `Gen.Fail`, `Gen.FailAt`, `Gen.FailOn` |
| Read a data file in a generator | `Gen.Input(path)` |

`Target` is a compile-time `TargetInfo` ([branching on the target](../reference.md#branching-on-the-target)). The generator types are prelude names. Their methods are in [the generator API](../reference.md#the-generator-api), and `Gen.Input`'s path rules are in [data files](build.md#data-files).

## Examples

Each program below runs as written with `mk run`.

### Parse settings

```mk
// verdict: ok
// A settings file, one "key = value" per line; # starts a comment.
const Input: str = "# server\nname = demo box\nport = 8080\r\nratio = 0.75\n\nmode = fast\n"

WordCount(s: str): int {
    var n := 0
    for _ in s.Words() { // splits on any run of white space
        n += 1
    }
    return n
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError!void {
    var keys := Text.New(sys.arena)
    for line in Input.Lines() { // \n and \r\n both end a line
        entry := line.Trim()
        if entry.Len() == 0 || entry.StartsWith("#") {
            continue
        }
        eq := entry.Find("=") else { continue } // none when there is no "="
        key := entry[..eq].Trim()
        value := entry[eq + 1..].Trim()
        if key == "port" {
            port := try value.ParseInt()
            try sys.Print("port {p} (next {n})\n", (p: port, n: port + 1))
        } else if key == "ratio" {
            r := try value.ParseFloat()
            try sys.Print("ratio {r} = {pct:.1}%\n", (r, pct: r * 100.0))
        } else {
            shout := try value.ToUpper(sys.arena)
            try sys.Print("{key}: {shout}\n", (key, shout))
        }
        if keys.Len() > 0 {
            try keys.Push(", ")
        }
        try keys.Push(key)
    }
    joined := try keys.Str().Replace(sys.arena, ", ", " | ")
    try sys.Print("keys: {joined}\n", (joined))
    var n := 0
    for _ in "a,,b".Split(",") { // empty pieces are kept
        n += 1
    }
    try sys.Print("pieces: {n}, words: {w}\n", (n, w: WordCount("  one two\tthree ")))
}
```

```text
$ mk run settings.mk
name: DEMO BOX
port 8080 (next 8081)
ratio 0.75 = 75.0%
mode: FAST
keys: name | port | ratio | mode
pieces: 3, words: 3
```

- `entry[..eq]` slices by bytes. `Find` returns a byte offset, so the slice falls on a scalar boundary.
- `Trim`, `Lines`, `Split` and `Words` return views of `Input`; only `ToUpper`, `Replace` and the `Text` copy into the arena.

### Sort and search a list

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    var xs := List[int].New(sys.arena) // allocates nothing until the first Push
    for x in [42, 7, 19, 3, 25] {
        try xs.Push(x)
    }
    xs.Sort(fn(a: &int, b: &int): bool { return *a < *b })
    try sys.Print("sorted {xs}\n", (xs: &xs))

    at := xs.Search(&20, fn(e: &int, k: &int): bool { return *e < *k })
    try xs.Insert(at, 20) // the place Search found keeps the list sorted
    try sys.Print("insert 20 at {at}: {xs}\n", (at, xs: &xs))
    try sys.Print("has 7: {h}, index of 25: {i}\n", (h: xs.Has(&7), i: xs.Index(&25)))

    var words := ["pear", "fig", "apple", "kiwi", "plum"]
    try words.SortStable(sys.heap.Alloc(), fn(a: &str, b: &str): bool { return a.Len() < b.Len() })
    try sys.Print("by length {w}\n", (w: words[..])) // equal lengths keep their order

    for piece in xs.Chunks(4) { // the last piece is shorter
        try sys.Print("chunk {piece}\n", (piece))
    }
    xs.Reverse()
    last := xs.Pop() else { 0 }
    try sys.Print("reversed, popped {last}: {xs}, get 9: {g}\n", (last, xs: &xs, g: xs.Get(9)))
}
```

```text
$ mk run lists.mk
sorted [3, 7, 19, 25, 42]
insert 20 at 3: [3, 7, 19, 20, 25, 42]
has 7: true, index of 25: some(4)
by length [fig, pear, kiwi, plum, apple]
chunk [3, 7, 19, 20]
chunk [25, 42]
reversed, popped 3: [42, 25, 20, 19, 7], get 9: none
```

- `words` is a `var` array, so its `mut self` methods such as `SortStable` work on it directly.

### Group words in a map

```mk
// verdict: ok
const Words: str = "the cat saw the dog and the dog saw a cat"

export Main(sys: &System): OutOfMemory|WriteError!void {
    var seen := Set[str].New(sys.arena)
    var by_len := Map[int, List[str]].New(sys.arena)
    for w in Words.Words() {
        if !try seen.Add(w) { // false: already a member
            continue
        }
        group := try by_len.Ensure(w.Len(), List[str].New(sys.arena))
        try group.Push(w)
    }
    // A map's order is unspecified: collect the keys and sort them.
    var lens := List[int].New(sys.arena)
    for e in by_len.Iter() {
        try lens.Push(*e.key)
    }
    lens.Sort(fn(a: &int, b: &int): bool { return *a < *b })
    for n in lens[..] {
        group := by_len.Get(&n) else { continue }
        try sys.Print("{n}: {group}\n", (n, group))
    }
    try sys.Print("{u} lengths, has dog: {d}, has fox: {f}\n", (u: by_len.Len(), d: seen.Has(&"dog"), f: seen.Has(&"fox")))
    if old := try by_len.Replace(1, List[str].New(sys.arena)) { // the old value comes back
        try sys.Print("replaced {old}\n", (old: &old))
    }
}
```

```text
$ mk run group.mk
1: [a]
3: [the, cat, saw, dog, and]
2 lengths, has dog: true, has fox: false
replaced [a]
```

- `Ensure` returns the stored value `mut`, so `group.Push` appends inside the map.
- For the most common words of a text, see [Count words and print the ten most common](../cookbook.md#count-words-and-print-the-ten-most-common).

### Heap containers

```mk
// verdict: ok
// Notes builds a map that outlives every arena: its memory comes from a.
Notes(a: &Alloc): OutOfMemory!TextMap[HeapText] {
    var m := TextMap[HeapText].New(a)
    onerr m.Free(a) // a failed call below frees what was built
    for pair in [(k: "todo", v: "buy milk"), (k: "idea", v: "a tool"), (k: "todo", v: "call bo")] {
        var v := HeapText.New(a)
        onerr v.Free(a)
        try v.Push(a, pair.v)
        try m.Set(a, pair.k, v) // frees the old value of a present key
    }
    return m
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    var notes := try Notes(a)
    defer notes.Free(a) // linear: the compiler rejects a path that forgets it
    if t := notes.Get("todo") {
        try sys.Print("todo: {t}, {n} keys\n", (t: t.Str(), n: notes.Len()))
    }
    var ids := HeapList[int].New(a)
    onerr ids.Free(a)
    try ids.Extend(a, [3, 1, 2][..])
    ids.Sort(fn(x: &int, y: &int): bool { return *x < *y })
    try sys.Print("ids {ids}\n", (ids: &ids))
    ids.Free(a)
    b := try Box[int].New(a, 41)
    var n := b.Take(a) // gives the value back and frees the box
    n += 1
    try sys.Print("boxed {n}\n", (n))
}
```

```text
$ mk run heap.mk
todo: call bo, 2 keys
ids [1, 2, 3]
boxed 42
```

- Every call that may allocate takes `a`, and the container checks that it is the allocator it was made with.
- `Free` of a `TextMap[HeapText]` frees each `HeapText` and each key copy (keys of 16 bytes or more), then the table.

### Format a table

```mk
// verdict: ok
struct Item {
    export name: str
    export qty: int
    export price: f64
}

error NotFound(id: int)

export Main(sys: &System): OutOfMemory|WriteError!void {
    items := [Item(name: "bolts", qty: 120, price: 0.25), Item(name: "gears", qty: 8, price: 12.5)]
    try sys.Print("{a:<8}|{b:>5}|{c:>8}\n", (a: "name", b: "qty", c: "price"))
    for it in items {
        // a hole names a field path; Item itself has no Format
        try sys.Print("{it.name:.<8}|{it.qty:5}|{it.price:8.2}\n", (it))
    }
    flags := 0x2A
    try sys.Print("{flags:08b} {flags:x} {flags:#>4X} {flags:o} {m:^7}|\n", (flags, m: "mid"))
    try sys.Print("{a} {b} {c} {d} {e:.0} {f:.0}\n", (a: 0.1 + 0.2, b: 1e21, c: 0.00001, d: 2.5e-3, e: 0.5, f: 1.5))
    nan := 0.0 / 0.0
    inf := 1.0 / 0.0
    try sys.Print("[{a:+08.2}] [{b:+08.2}] [{c:+08.2}] {inf} {n}\n", (a: nan, b: inf, c: -0.0, inf, n: -inf))
    port: ?int = some(8080)
    missing: ?int = none
    var names := List[str].New(sys.arena)
    try names.Push("ann")
    try names.Push("bo")
    try sys.Print("{port} {missing} {names} {e} {{literal}}\n", (port, missing, names: &names, e: NotFound(id: 3)))
    s := try sys.arena.Fmt("{n} items", (n: names.Len())) // a str in the arena
    try sys.Print("{s}\n", (s))
    try sys.Print("done\n") // no holes, no record
}
```

```text
$ mk run table.mk
name    |  qty|   price
bolts...|  120|    0.25
gears...|    8|   12.50
00101010 2a ##2A 52   mid  |
0.30000000000000004 1e+21 1e-05 0.0025 0 2
[     nan] [    +inf] [-0000.00] inf -inf
some(8080) none [ann, bo] NotFound(id: 3) {literal}
2 items
done
```

- `{e:.0}` and `{f:.0}` round 0.5 and 1.5 to even: 0 and 2.
- For a table with a `Format` of your own type, see [Print a table](../cookbook.md#print-a-table).

### Read and write bytes

```mk
// verdict: ok
// Report writes a small report to any Writer: a file, a socket, stdout or a Text.
Report(w: mut Writer, title: str, n: int): WriteError!void {
    try Print(w, "== {title} ==\n", (title))
    try Print(w, "{n} records\n", (n))
}

export Main(sys: &System): ReadError|OutOfMemory|WriteError!void {
    data := "id,name\r\n1,ann\n2,bo".AsBytes()
    var r := Reader.Of(data) // reads a byte slice; no Io, no buffer
    var lines := 0
    for line := try r.Line(sys.arena) { // none at the end; \n and \r\n are stripped
        lines += 1
        try sys.Print("line {lines}: {line}\n", (lines, line))
    }

    var parts := Reader.Of("a;bb;ccc".AsBytes())
    var buf: [8]u8 = [0; 8]
    for {
        n := try parts.Until(0x3B, mut buf[..]) // up to and including ';', never past it
        if n == 0 {
            break
        }
        try sys.Print("piece of {n} bytes\n", (n))
    }

    var t := Text.New(sys.arena)
    var tw := t.Writer() // a Writer into the Text, with no buffer of its own
    try Report(mut tw, "import", lines - 1)
    try sys.Print("{n} bytes built\n", (n: t.Len()))

    var out := sys.Stdout() // shares the process's one stdout buffer
    try Report(mut out, "stdout", 2)
    try out.Flush() // also flushed when Main returns
}
```

```text
$ mk run bytes.mk
line 1: id,name
line 2: 1,ann
line 3: 2,bo
piece of 2 bytes
piece of 3 bytes
piece of 3 bytes
23 bytes built
== stdout ==
2 records
```

- The last line, `2,bo`, has no newline and is still a line.
- For a file, the same `Line` loop runs over `File.Reader`: see [Read a file line by line](../cookbook.md#read-a-file-line-by-line).

### Tasks and a channel

```mk
// verdict: ok
Produce(io: Io, ch: &Chan[int], n: int): Canceled|Closed!void {
    for i in 1..n + 1 {
        try ch.Send(io, i * i) // waits while the channel is full
    }
    ch.Close() // receivers drain what is buffered, then get Closed
}

Consume(io: Io, ch: &Chan[int], total: &Mutex[int]): Canceled!void {
    for {
        x := ch.Recv(io) else e {
            match e {
                Closed { return } // nothing more will come
                Canceled { fail Canceled }
            }
        }
        var g := try total.Lock(io)
        *g.Mut() += x
        g.Unlock()
    }
}

export Main(sys: &System): Canceled|Closed|OutOfMemory|WriteError!void {
    io := sys.io
    a := sys.heap.Alloc()
    ch := try Chan[int].New(a, 4) // room for 4 values; 0 would be a rendezvous
    total := Mutex[int].New(0)
    try scope s := io.ScopeOf[Canceled|Closed]() { // the first error cancels the rest
        s.go Produce(io, &ch, 10)
        s.go Consume(io, &ch, &total)
        s.go Consume(io, &ch, &total)
    }
    ch.Free(a)
    try sys.Print("sum of squares: {sum}\n", (sum: total.Into()))
}
```

```text
$ mk run tasks.mk
sum of squares: 385
```

- The scope block ends when all three tasks have ended, so `ch.Free` and `total.Into` come after it.
- A task cannot print through `sys`, which is not `Share`: it reports through values the parent owns, here the `Mutex`.

### An arena per turn

```mk
// verdict: ok
// Label builds a str in the caller's arena; the result borrows it.
Label(a: &Arena, id: int, name: str): OutOfMemory!str from a {
    return try a.Fmt("#{id} {name}", (id, name))
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var scratch := try Arena.New(sys.heap) // one arena, reset on each turn of the loop
    defer scratch.Free()
    var longest := 0
    for id in 1..4 {
        label := try Label(&scratch, id, "request")
        squares := try scratch.Slice[i64](id * 100) // zeroed
        squares[0] = 7
        if label.Len() > longest {
            longest = label.Len()
        }
        try sys.Print("{label}: {n} slots\n", (label, n: squares.Len()))
        scratch.Reset() // ends every loan: label and squares are gone
    }
    kept := try sys.arena.Dup("kept for the whole run") // sys.arena lives as long as the program
    p := try sys.arena.Put(41)
    *p += 1
    try sys.Print("{kept}, {p}, longest label {longest}\n", (kept, p: *p, longest))
}
```

```text
$ mk run turns.mk
#1 request: 100 slots
#2 request: 200 slots
#3 request: 300 slots
kept for the whole run, 42, longest label 10
```

- `Reset` keeps the chunks this loop uses, so the loop settles and stops taking memory from the heap.
- A server does the same with one arena per request: see [Use one arena per request](../cookbook.md#use-one-arena-per-request).

### A frame per call

Calls that nest, each needing scratch memory for its own duration, share one arena through frames: each call takes `Arena.NewIn(scratch.Alloc())` and frees it when it returns. A `Sub` would take its first chunk from the heap on every call; a frame takes its chunks from the shared arena and gives them back, last in first out.

```mk
// verdict: ok
// Digits writes n in base 10 into a frame of the shared scratch arena, and
// Sum calls itself through it: each call's frame ends before its caller's.
Digits(scratch: &Arena, n: int): OutOfMemory!int {
    var frame := try Arena.NewIn(scratch.Alloc())
    defer frame.Free()
    s := try frame.Fmt("{n}", (n,))
    return s.Len()
}

Sum(scratch: &Arena, depth: int): OutOfMemory!int {
    var frame := try Arena.NewIn(scratch.Alloc())
    defer frame.Free()
    xs := try frame.Slice[int](depth + 1)
    for i in 0..xs.Len() {
        xs[i] = i
    }
    var total := try Digits(&frame, depth * 1000)
    if depth > 0 {
        total += try Sum(scratch, depth - 1)
    }
    for x in xs {
        total += x
    }
    return total
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var scratch := try Arena.New(sys.heap)
    defer scratch.Free()
    for _ in 0..3 {
        n := try Sum(&scratch, 3)
        try sys.Print("{n}\n", (n,))
    }
}
```

```text
$ mk run frames.mk
23
23
23
```

- Every call's frame is freed before its caller's, so the shared arena is back where it was after each `Sum`, and the loop settles with no heap call.
- A frame is an `Arena`: a function that takes `&Arena` takes `&frame`.

## API

Generated from the module's doc comments by `mk doc --std std.core` ([reading the API](README.md#reading-an-api-section)).

<!-- api:begin -->
Stability: Stable.

### `Alloc`

```
export struct Alloc
```

An allocator: a context pointer and a table of entries. Containers take
a &Alloc and record its address, which is the allocator's identity: a
block goes back only to the Alloc it came from. Alloc is unique and
neither Send nor Share, whatever its fields.

### `Alloc.Bytes`

```
export Alloc.Bytes(&self, n: int, align: int): *u8
```

A raw block of n bytes aligned to align (a power of two), or null when
the allocator fails: one call of the table's alloc entry. The block is
uninitialized; give it back with FreeBytes and the same n and align.
Callable only in an unsafe block (E1101). O(1) besides the entry.

### `Alloc.FreeBytes`

```
export Alloc.FreeBytes(&self, p: *u8, n: int, align: int)
```

Gives back a block that Bytes returned, with the n and align it was
asked for: one call of the table's free entry. Callable only in an
unsafe block (E1101). O(1) besides the entry; it allocates nothing
itself.

### `Arch`

```
export open enum Arch
```

The target's architecture: X64, Arm64 or Riscv64. The enum is open, so a
match on it needs a `_` arm.

Variants:

- `X64`
- `Arm64`
- `Riscv64`

### `Arena`

```
export linear mut view struct Arena
```

A bump allocator: it takes memory in chunks from a source allocator (a
Heap, or any &Alloc), hands out pieces by moving a pointer, and gives
everything back at once with Reset or Free. Nothing is freed one by one.
Values in the arena borrow it, so the compiler rejects a use after Reset
or Free. Linear: it ends in Free.

### `Arena.Alloc`

```
export Arena.Alloc(&self): &Alloc
```

- Flows: The result borrows from self.

The arena as an Alloc, the same one on every call, for containers that
take a &Alloc and should live in the arena. O(1). Its free gives back
the last block bumped, or a block that has a chunk of its own while
that chunk is the arena's newest; any other block stays until Reset or
Free.

### `Arena.Dup`

```
export Arena.Dup(&self, s: str): OutOfMemory!str from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

A copy of s in the arena. OutOfMemory when no chunk can be had.
O(s.Len()); allocates s.Len() bytes, none for "".

### `Arena.Fmt`

```
export Arena.Fmt[A: Fields[Format] + Plain](&self, const f: str, args: A): OutOfMemory!str from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

Formats f with args into a new str in the arena, with the holes and
specs of Print. Any WriteError, a user Format's included, becomes
OutOfMemory. O(output length); allocates the text, growing as Text does.

### `Arena.Free`

```
export Arena.Free(self) ends self
```

- Flows: Ends every borrow of self.

Ends the arena and returns every chunk to the chunk source. O(chunks).

### `Arena.New`

```
export Arena.New(h: &Heap): OutOfMemory!Arena
```

- Flows: The result borrows from h.
- Errors: OutOfMemory

A new arena whose chunks come from the heap h. OutOfMemory when the heap
refuses the first chunk. O(1): one heap allocation, the first chunk.

### `Arena.NewIn`

```
export Arena.NewIn(a: &Alloc): OutOfMemory!Arena from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A new arena whose chunks come from a and go back to it. OutOfMemory when
a refuses the first chunk. O(1): one allocation from a, the first chunk.

Every chunk, the first included, is a request to a, and so are the
chunks of the arena's Subs, so a budget such as mem.Counting or
mem.Fixed bounds everything an API puts in the arena. A std API that
also uses the process heap says so (proc.Run's segments, a sim.Sim's
world). The arena holds a loan on a, and each Sub one on the arena, so
the allocator is not reset, freed or moved while either lives. An Arena
is not Send, so a source confined to one task stays on it.

Reset and Free return chunks to a, which decides what it reuses. A
mem.Fixed frees only its last block, so it gets bytes back only for
chunks freed in last-in first-out order: an arena that served a value
aligned over 8, or a Sub freed before its parent's later chunks, leaves
part of the Fixed used until Fixed.Reset.

Arena.NewIn(p.Alloc()) is a frame of the arena p: scratch memory for
one call, freed before p's later allocations. Its chunks are blocks of
p, at least FirstChunk (4 KiB) each, and Free gives them back newest
first, so p is back where it was. Nested frames whose chunks fit in p's
current chunk (p's chunks grow to 1 MiB) take nothing more from p's
source once warm; a request too large for p's chunks takes a chunk of
its own from the source and gives it back. p keeps a frame's bytes until
its Reset or Free when p started a new chunk while the frame lived, or
when the frame ends before a newer one, so frames nested deeper than
one chunk of p holds keep one more chunk on every round: Sub suits that
depth.

### `Arena.Put`

```
export Arena.Put[T](&self, x: T into self): OutOfMemory!mut T from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

Moves x into the arena and returns a writable reference to it, in fresh
memory, disjoint from every other piece, on every call. OutOfMemory when
no chunk can be had, and then the caller still owns x. O(1) amortized;
allocates SizeOf[T]() bytes.

### `Arena.Reset`

```
export Arena.Reset(mut self) ends self
```

- Flows: Ends every borrow of self.

Ends every loan of the arena and makes its memory free for reuse: the
values it held are gone, and the compiler rejects a later use of them.
O(chunks); returns memory to the chunk source and allocates nothing.

It keeps the first chunk, the current chunk and the current aligned
chunk, and returns every other chunk to the source; the next chunk size
stays as it was. So an arena reset on every turn of a
loop settles into reusing the same memory when each request is at most
512 KiB and one turn's requests fit in the current chunk (up to 1 MiB).
Otherwise the extra chunks are taken from the source and returned on
every turn.

### `Arena.Slice`

```
export Arena.Slice[T](&self, n: int): OutOfMemory!mut []T
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

A writable slice of n zero-filled elements in the arena. T must be
zero-valid, a type for which all-zero bytes are a valid value (E0701
otherwise), and n < 0 panics (code 8). OutOfMemory when no chunk can be had,
or when the byte count does not fit in an int (never an overflow panic).
O(n); allocates n elements.

### `Arena.SliceAligned`

```
export Arena.SliceAligned[T](&self, n: int, align: int): OutOfMemory!mut []T
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

As Slice, with the first element at a multiple of align bytes (or of
T's own alignment when that is larger): a buffer for vector loads, or
one on a cache line. align is a power of two from 1 to 65,536, else it
panics (code 8), as n < 0 does. OutOfMemory as Slice. O(n);
allocates n elements and at most align - 1 bytes of padding.

At compile time an align over T's own alignment panics (E0901), since
a slice made there becomes static data aligned to T only. For an
aligned static table, give the element type align(n) (n up to 65,536)
and use Slice: static data keeps an align(n) type's alignment on every
profile.

### `Arena.Sub`

```
export Arena.Sub(&self): OutOfMemory!Arena
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

A new, independent arena on the same chunk source, as Arena.New or
Arena.NewIn would make; it holds a loan on self, so it ends before self
does. Use it for scratch memory that is freed before the parent's.
OutOfMemory when the source refuses the first chunk. O(1): one
allocation from the source, the first chunk. Scratch for each of many
calls is cheaper as a frame, Arena.NewIn(self.Alloc()), whose chunks
come from self, while the frames live at once fit in one chunk of self
(see NewIn).

### `Block`

```
export struct Block
```

A list of statements built one Add at a time; it splices into a quote as
its tokens.

### `Block.Add`

```
export Block.Add(mut self, c: Code)
```

Appends c, then a newline, to the block. Compile time only.

### `Box`

```
export linear view struct Box[T]
```

One value of T on an allocator, owned by the Box. The Box records its
allocator, and Take and Free panic (code 12) when given a different one.
Linear: it ends in Free or Take. Send when T is Send, Share when T is
Share.

### `Box.Mut`

```
export Box[T].Mut(mut self): mut T
```

- Flows: The result borrows from self.

The boxed value, writable. O(1).

### `Box.New`

```
export Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x
```

- Flows: The result borrows from a and x.
- Errors: OutOfMemory

A Box holding x, on the allocator a. x moves in only on success: OutOfMemory
when a cannot supply the bytes of a T, and then the caller still owns x.
O(1) plus one allocation from a; a zero-size T needs no memory.

### `Box.Ref`

```
export Box[T].Ref(&self): &T
```

- Flows: The result borrows from self.

The boxed value. O(1).

### `Box.Take`

```
export Box[T].Take(self, a: &Alloc): T
```

- Flows: The result borrows from self.

Ends the Box and gives its value back: the memory goes back to a, which
must be the Box's allocator (code 12 otherwise). Works for a Box of any
T. O(1) plus one free to a.

### `Canceled`

```
export error Canceled
```

The operation's scope was canceled, before it started or while it waited.

### `Cell`

```
export struct Cell[T: Plain]
```

A plain value that can be read and replaced through a shared reference,
with Load and Store, without a lock or an atomic. A Cell is not Share, so
a &Cell[T] cannot cross to another task: use Mutex, Chan or std.atomic
there.

### `Cell.Load`

```
export Cell[T].Load(&self): T
```

- Flows: The result borrows what self refers to; self is free after the call.

The value in the cell. O(1).

### `Cell.New`

```
export Cell[T].New(x: T): Cell[T]
```

- Flows: The result borrows from x.

A cell holding x. O(1); allocates nothing.

### `Cell.Store`

```
export Cell[T].Store(&self, x: T into self)
```

Puts x in the cell; the old value, which is plain, is overwritten. O(1).

### `Chan`

```
export view struct Chan[T]
```

A channel that carries values of T between tasks, in FIFO order, with a
buffer of the capacity given to New, in memory from an allocator. Share
a &Chan with the tasks that send and receive; Close tells receivers no
more values come, and Free gives the memory back. Chan[T] and &Chan[T]
are Selectable for every T, so sync.Select can wait on any channel.
Chan is Send and Share when T is Send. It is not linear, so the compiler
does not catch a missing Free: a channel on the heap that is never freed
leaks; one on an arena's Alloc goes with the arena.

### `Chan.Close`

```
export Chan[T].Close(&self)
```

Closes the channel: later Sends fail with Closed, and Recv drains what is
buffered, then fails with Closed. Every task waiting in Send or Recv
wakes with Closed, and every sync.Select watching the channel wakes.
Closing twice is allowed; Close cannot be cancelled and takes no Io.
O(waiting tasks); allocates nothing.

### `Chan.FreeEach`

```
export Chan[T].FreeEach(self, a: &Alloc, const release: fn(T))
```

As Free, but hands every buffered value to release, oldest first.
O(buffered) calls of release.

### `Chan.FreeEmpty`

```
export Chan[T].FreeEmpty(self, a: &Alloc)
```

As Free, but panics (code 11) when a value is still buffered: with
FreeEach, the way to release a channel of a T that is linear and not
Free. O(1).

### `Chan.New`

```
export Chan[T].New(a: &Alloc, capacity: int): OutOfMemory!Chan[T]
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A channel with room for capacity buffered values (0: every Send waits for a
Recv), in memory from a; capacity < 0 panics with code 8. OutOfMemory leaves
nothing allocated. O(1); allocates the channel's state and, above 0, a
buffer of capacity slots.

### `Chan.Recv`

```
export Chan[T].Recv(&self, io: Io): Canceled|Closed!T
```

- Flows: The result borrows what self refers to; self is free after the call.
- Errors: Canceled, Closed

The oldest value sent, waiting while there is none. Closed when the channel
is closed and empty, Canceled when the scope is canceled. O(1) besides the
wait; allocates nothing.

Values buffered before Close are still delivered, then Recv fails with
Closed. Recv checks for cancellation first: in a cancelled scope it
returns Canceled even with a value buffered, so a cleanup that drains
the channel passes io.NoCancel(). Once a sender has handed it a value,
Recv returns it, even if the scope is cancelled meanwhile.

### `Chan.Send`

```
export Chan[T].Send(&self, io: Io, x: T into self): Canceled|Closed!void
```

- Errors: Canceled, Closed

Sends x, waiting while there is no receiver and no room. Closed when the
channel is closed and Canceled when the scope is; either way the caller
still owns x. O(1) besides the wait; allocates nothing.

A waiting receiver takes x directly; otherwise x goes into the buffer
when it has room; otherwise the task waits until a receiver takes x, the
channel is closed (Closed) or the scope is cancelled (Canceled). Send
checks for cancellation first: in a cancelled scope it returns Canceled
even when a receiver or room is there. Once a receiver has taken x, Send
succeeds, even if the scope is cancelled meanwhile. A Send that makes a
value ready wakes one sync.Select watching the channel.

### `Chan.TrySend`

```
export Chan[T].TrySend(&self, x: T into self): Closed!?T
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.
- Errors: Closed

Sends x without waiting: none when it sent (a waiting receiver took x,
or the buffer had room), or some(x), x given back, when the channel is
full, so a full channel never drops x. Closed when the channel is
closed, and then the caller still owns x. O(1) besides the lock wait;
allocates nothing.

At capacity 0 it succeeds only when a task is waiting in Recv; a task
waiting in sync.Select does not count, so TrySend gives x back. It takes
no Io and does not check for cancellation. It never waits for room, but
it may wait briefly for the channel's lock, which is held for a constant
number of steps.

### `Chunks`

```
export view struct Chunks[T]
```

An iterator over the pieces of a shared slice, n elements each and the
last one shorter; made by Slice.Chunks.

### `Chunks.Next`

```
export Chunks[T].Next(mut self): ?[]T from self.xs
```

- Flows: The result borrows what self.xs refers to; self is free after the call.

The next piece, or none after the last. O(1); allocates nothing.

### `ChunksMut`

```
export view struct ChunksMut[T]
```

An iterator over the writable pieces of a mut slice, n elements each and
the last one shorter, each handed out once; made by Slice.ChunksMut. The
pieces borrow the slice, not the iterator, so several may be live at
once, as `for c in xs.ChunksMut(n) { s.go Work(c) }` needs.

### `ChunksMut.Next`

```
export ChunksMut[T].Next(mut self): ?mut []T from self.ptr
```

- Flows: The result borrows what self.ptr refers to; self is free after the call.

The next mut piece, or none after the last. O(1); allocates nothing.

### `Closed`

```
export error Closed
```

The channel or stream was closed.

### `Code`

```
export struct Code
```

A run of tokens. A quote { } expression has type Code, and $(...) splices
a value into it. A float splices as the shortest decimal that reads back
to its bits, and a fn value as its function's qualified name; an operand
of a type that cannot splice is E0301 at the splice.

### `Code.Then`

```
export Code.Then(self, c: Code): Code
```

A new Code of self's tokens followed by c's; both stay unchanged. Compile
time only.

### `Eq`

```
export interface Eq {
    Eq(&self, other: &Self): bool
}
```

Equality of two values of one type, as Has, Index and map keys need it.
&T is Eq when T is, and compares the referents, not the addresses. On
floats it is IEEE ==, as the operator is, so a NaN equals nothing,
itself included.

### `Field`

```
export struct Field
```

A field of a struct, error, record or enum variant.

### `Field.Doc`

```
export Field.Doc(&self): str
```

The field's doc comment: the /// lines directly above it, empty when there
are none. Compile time only.

### `Field.Exported`

```
export Field.Exported(&self): bool
```

Whether the field is exported. Compile time only.

### `Field.Name`

```
export Field.Name(&self): str
```

The field's name. Compile time only.

### `Field.Type`

```
export Field.Type(&self): Type
```

The field's type; Opaque when it is not resolved yet. Compile time only.

### `Flow`

```
export struct Flow
```

One flow clause of a method's signature (Method.Flows).

### `Flow.Field`

```
export Flow.Field(&self): str
```

The result field of a per-field `from` clause, else "". O(1). Compile time
only.

### `Flow.Kind`

```
export Flow.Kind(&self): FlowKind
```

The clause's kind. O(1). Compile time only.

### `Flow.Param`

```
export Flow.Param(&self): str
```

What the clause flows into or ends: `result` for From, the receiving
parameter for Into (`x: T into q` gives `q`) and Store (`*q into p` gives
`p`), the parameter whose loans end for Ends; the receiver is `self`.
O(1). Compile time only.

### `Flow.Path`

```
export Flow.Path(&self): str
```

The source as written: for From the path after `from` (`a`, `self.buf`),
for Into the parameter that flows (`x`), for Store the stored parameter
(`q`), "" for Ends. O(1). Compile time only.

### `FlowKind`

```
export enum FlowKind
```

What a flow clause states: `from` (the result holds a loan on the
parameter), `into` (a parameter flows into another), `ends`, or a store
`*q into p`.

Variants:

- `From`: `from`: the result holds a loan on the parameter.
- `Into`: `into`: a parameter flows into another.
- `Ends`: `ends`: the call ends every loan on the named parameter.
- `Store`: `*q into p`: what q points at is written into p.

### `Format`

```
export interface Format {
    Format(&self, w: mut Writer): WriteError!void
}
```

Writes a value's text to a Writer, for a {hole} in Print, Arena.Fmt,
System.Print and every function shaped like Print.
The numbers, str, bool, char and references to them implement it, and so
do ?T, []T, List[T] and HeapList[T] when T does: an optional prints
some(v) or none, a sequence [a, b, c] or [], each element through its own
Format, so a str inside prints without quotes. An error whose payload
fields all have Format has it too: it prints its name, then its fields as
name: value in declaration order, joined by ", " in parentheses, as
NotFound(id: 3); a module's error prints unqualified. An error set has
Format when all its members do, and prints the member it holds.

### `Free`

```
export interface Free {
    Free(self, a: &Alloc)
}
```

The interface of a value that owns memory from an allocator and gives it
back to a. The prelude's HeapList, HeapMap, TextMap, HeapText, Box and Chan
implement it, as do std.coll's HeapDeque and HeapBTree and std.net's
ConnSlot. A container's Free is deep: it releases every element with a
before its storage. Such a Free compiles only when each element type is not
linear or is Free (E0701 at the call, which names FreeEmpty and FreeEach as
the alternatives).

### `Gen`

```
export struct Gen
```

One run of a generator: the receiver of every call that emits code or
reports an error.

### `Gen.Arena`

```
export Gen.Arena(&self): &Arena
```

The run's arena, which lasts for the run, for building text and lists with
std. What it allocates counts against the run's limits like any other
allocation. Compile time only.

### `Gen.Block`

```
export Gen.Block(&self): Block
```

A fresh empty builder of statements. It holds no loan on the Gen, so Emit
may follow while it lives. Compile time only.

### `Gen.Emit`

```
export Gen.Emit(mut self, c: Code)
```

Appends the items of c to this run's output; c must parse as top-level
items, with no gen or import (E0905). Compile time only.

### `Gen.Fail`

```
export Gen.Fail(mut self, msg: str)
```

Records E0903 at the gen line with msg. The run continues, and its output is
discarded when it ends. Compile time only.

### `Gen.FailAt`

```
export Gen.FailAt(mut self, f: Field, msg: str)
```

As Fail, but reports E0903 at field f's declaration. Compile time only.

### `Gen.FailOn`

```
export Gen.FailOn(mut self, t: Type, msg: str)
```

As Fail, but reports E0903 at type t's declaration. Compile time only.

### `Gen.Fresh`

```
export Gen.Fresh(mut self, base: str): Code
```

An identifier unique in the module, made from base, a line number and a
counter of the run. A base that is not an identifier is E0901.
Compile time only.

### `Gen.Ident`

```
export Gen.Ident(&self, name: str): Code
```

name as one identifier token. A name that is not an identifier, or is a
keyword, is E0901. Compile time only.

### `Gen.Input`

```
export Gen.Input(&self, path: str): []u8
```

- Flows: The result borrows from path.

The bytes of the regular file at path. path is relative to the package
root of the gen line's module, written with `/`, without `..`, and
reached through no link; the file is at most 64 MiB. Any other path is
E0910. The gen line runs again when the file changes. O(n) in the file's
bytes. Compile time only.

### `Gen.Str`

```
export Gen.Str(&self, s: str): Code
```

s as one str literal token, escaped as Mk source writes literals.
Compile time only.

### `Guard`

```
export linear view struct Guard[T]
```

The held lock of a Mutex, giving access to its value. Linear: Unlock
ends it, so the lock cannot be forgotten.

### `Guard.Mut`

```
export Guard[T].Mut(mut self): mut T
```

- Flows: The result borrows from self.

The value, writable, which the Guard keeps alive and exclusive while it
lives. O(1).

### `Guard.Ref`

```
export Guard[T].Ref(&self): &T
```

- Flows: The result borrows from self.

The value, which the Guard keeps alive and exclusive while it lives.
O(1).

### `Guard.Store`

```
export Guard[T].Store(mut self, x: T into self)
```

Replaces the value and drops the old one, so it compiles only when T is
not linear (E0701 at the call); Replace(g.Mut(), x) gives the old value
back instead. O(1); allocates nothing.

### `Guard.Unlock`

```
export Guard[T].Unlock(self)
```

Releases the lock, through the Io that Lock was given. When the first
waiter has waited 1 ms and lost the lock once more, Unlock hands the lock
to it directly. O(1); allocates nothing.

### `Hash`

```
export interface Hash {
    Hash(&self, h: mut Hasher)
}
```

Feeds a value into a Hasher; values that are Eq must hash the same. &T is
Hash when T is, and hashes the referent. The floats have no Hash, so no
float is a Map or Set key.

### `HashKey`

```
export struct HashKey
```

A SipHash-1-3 key for the maps and sets made with NewKeyed, which is how
to hash keys from untrusted input such as network or file data. Plain,
with no public fields. HashKey.New(io) makes a random one from 16 bytes
of Io.Random, so a keyed map's iteration order varies from process to
process unless io is simulated; compile-time evaluation runs no Io
operation, so it never makes one. HashKey.Of(k0, k1) makes one from
chosen words. A zero-filled HashKey is the key (0, 0), the key Rust's
DefaultHasher uses.

### `HashKey.New`

```
export HashKey.New(io: Io): Canceled!HashKey
```

- Errors: Canceled

Makes a random hash key for NewKeyed tables: 16 bytes of Io.Random,
read as SipHash's two key words. A map keyed with it hashes with
SipHash-1-3, so its order varies from process to process unless io is
simulated. Fails with Canceled as Io.Random does, and then makes no key,
so a cancelled caller never gets an all-zero key. Compile-time
evaluation runs no Io operation, so it never makes a key. O(1): one
Io.Random call of 16 bytes; it allocates nothing.

### `HashKey.Of`

```
export HashKey.Of(k0: u64, k1: u64): HashKey
```

The key whose SipHash words are k0 and k1, for output that must repeat: a
file format, a test vector, a checksum. HashKey.Of(0, 0) is the same key
as a zero-filled HashKey. A key whose words are known protects nothing;
for keys from untrusted input use HashKey.New(io). O(1); allocates
nothing and cannot fail.

### `Hasher`

```
export struct Hasher
```

The state a Hash impl feeds bytes into, with Bytes and U64. The hash
depends only on the byte stream, however Bytes and U64 calls split it.
A Hasher is one of two kinds:
- a fold hasher: one fixed fast function with no key,
  which the maps and sets made with New use;
- SipHash-1-3 under a HashKey, which the maps and sets made with
  NewKeyed use.

### `Hasher.Bytes`

```
export Hasher.Bytes(mut self, b: []u8)
```

Feeds the bytes of b into the hash: first completing the pending tail,
then whole 8-byte little-endian words, then keeping the rest as the new
tail. O(n); allocates nothing, and reads 8 bytes per step.

### `Hasher.U64`

```
export Hasher.U64(mut self, x: u64)
```

Feeds x into the hash as 8 little-endian bytes. O(1); allocates nothing.

### `Heap`

```
export struct Heap
```

The process heap, in static memory and never released. Main receives it
as sys.heap; Heap.Alloc gives it to the containers that take a &Alloc.

### `Heap.Alloc`

```
export Heap.Alloc(&self): &Alloc
```

The heap as an Alloc, the same one on every call, for HeapList, HeapMap,
Box, Chan and the other containers that take a &Alloc. O(1); allocates
nothing.

The result holds no loan on the Heap: the heap is static memory that is
never released, so the &Alloc is valid for the whole program.

### `HeapList`

```
export linear view struct HeapList[T]
```

A growable array whose buffer comes from an allocator, so it outlives any
arena. It remembers the allocator it was made with, and a grow or free with
another one panics (code 12). Linear: end it with Free, FreeEmpty or
FreeEach.

### `HeapList.Clear`

```
export HeapList[T].Clear(mut self)
```

Empties the list and keeps its buffer. A list that still holds linear
elements panics (code 11) rather than drop them. O(Len()); allocates
nothing.

### `HeapList.Extend`

```
export HeapList[T].Extend(mut self, a: &Alloc, xs: []T): OutOfMemory!void *xs into self
```

- Flows: Stores the loans xs holds in self.
- Errors: OutOfMemory

Appends a copy of each element of xs, in order: one growth to fit them
all, then one Slice.CopyFrom of xs into the new slots, one memmove. The
elements are copied, not moved, so T must be plain: a call with any other
T is E0701. The list takes the loans the elements hold, not a loan on xs,
so xs may be a view of a buffer the caller keeps using.
OutOfMemory when the buffer cannot grow, and then the list is unchanged.
O(xs.Len()), plus a copy of Len() elements when it grows; a growth checks
that a is the list's allocator, allocates from a and frees the old buffer.
An Extend that does not grow, or of an empty xs, does not look at a.

### `HeapList.FreeEach`

```
export HeapList[T].FreeEach(self, a: &Alloc, const release: fn(T))
```

Hands every element to release, in index order, then frees the buffer
with the list's own allocator a. release may capture and must consume its
argument. O(Len()) calls of release.

### `HeapList.FreeEmpty`

```
export HeapList[T].FreeEmpty(self, a: &Alloc)
```

Frees the buffer of an empty list, with the list's own allocator a. Panics
(code 11) when the list still holds an element. It ends a list whose
element type is linear and not Free, after every element is taken out.
O(1).

### `HeapList.Insert`

```
export HeapList[T].Insert(mut self, a: &Alloc, i: int, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Inserts x before element i (i == Len() appends). Panics (code 2) unless
0 <= i <= Len(), before any growth. OutOfMemory when the buffer cannot
grow, and then the caller still owns x. O(Len() - i); growth allocates
from a.

### `HeapList.Len`

```
export HeapList[T].Len(&self): int
```

The number of elements. O(1).

### `HeapList.New`

```
export HeapList[T].New(a: &Alloc): HeapList[T]
```

- Flows: The result borrows from a.

An empty list that will allocate from a. O(1); allocates nothing until the
first Push.

### `HeapList.Pop`

```
export HeapList[T].Pop(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes and returns the last element, or none when the list is empty. O(1);
the buffer is kept.

### `HeapList.Push`

```
export HeapList[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Appends x. OutOfMemory when the buffer cannot grow, and then the caller
still owns x. Amortized O(1); a growth checks that a is the list's
allocator, allocates the new buffer from a and frees the old one. A Push
that does not grow does not look at a.

### `HeapList.Remove`

```
export HeapList[T].Remove(mut self, i: int): T
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes and returns element i, keeping the order of the rest. Panics
(code 2) unless 0 <= i < Len(). O(Len() - i); allocates nothing.

### `HeapMap`

```
export linear view struct HeapMap[K: Hash + Eq, V]
```

A hash map whose table comes from an allocator, a Swiss table. It hashes
and orders its entries as Map does. It remembers its allocator, and an
allocation or free with another one panics (code 12). Linear: end it with
Free, FreeEmpty or FreeEach.

### `HeapMap.Ensure`

```
export HeapMap[K, V].Ensure(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!mut V from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

The value for k, writable, inserting v first when k is absent. When k is
present the given k and v are released with a; a call is E0701 when K or
V is linear and not Free. OutOfMemory as Set. Expected O(1); growth
allocates from a.

### `HeapMap.FreeEach`

```
export HeapMap[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))
```

Hands every key and value to release, in the map's iteration order, then
frees the table with the map's own allocator a. O(capacity / 8) group
reads plus Len() calls of release.

### `HeapMap.FreeEmpty`

```
export HeapMap[K, V].FreeEmpty(self, a: &Alloc)
```

Frees the table of an empty map, with the map's own allocator a. Panics
(code 11) when the map still holds an entry. It ends a map whose contents
are linear and not Free, after every entry is removed. O(1).

### `HeapMap.Get`

```
export HeapMap[K, V].Get(&self, k: &K): ?&V
```

- Flows: The result borrows from self.

The value stored for k, or none. Expected O(1); allocates nothing.

### `HeapMap.GetMut`

```
export HeapMap[K, V].GetMut(mut self, k: &K): ?mut V
```

- Flows: The result borrows from self.

The value stored for k, writable in place, or none. Expected O(1); allocates
nothing.

### `HeapMap.Iter`

```
export HeapMap[K, V].Iter(&self): MapIter[K, V]
```

- Flows: The result borrows from self.

An iterator over the entries, in the map's unspecified order. O(1); a full
walk is O(capacity).

### `HeapMap.Len`

```
export HeapMap[K, V].Len(&self): int
```

The number of entries. O(1).

### `HeapMap.New`

```
export HeapMap[K, V].New(a: &Alloc): HeapMap[K, V]
```

- Flows: The result borrows from a.

An empty map that will allocate from a. It hashes with a fixed function,
so its order is the same from run to run. The function may change between
releases, and it is not safe for keys from untrusted input: use NewKeyed.
O(1); allocates nothing until the first insert.

### `HeapMap.NewKeyed`

```
export HeapMap[K, V].NewKeyed(a: &Alloc, key: HashKey): HeapMap[K, V]
```

- Flows: The result borrows from a.

As New, but hashing with SipHash-1-3 under key, for keys from
untrusted input. O(1); allocates nothing.

### `HeapMap.Remove`

```
export HeapMap[K, V].Remove(mut self, k: &K): ?V
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes k and returns its value, or none when k is absent. The stored key
is dropped, so a call with a linear K is E0701. Expected O(1); frees
nothing.

### `HeapMap.Replace`

```
export HeapMap[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V
```

- Flows: The result borrows from v. The result borrows what self refers to; self is free after the call.
- Errors: OutOfMemory

Sets k's value to v and returns the old value, or none when k was absent.
When k is present the given k is released with a; a call is E0701 when K
is linear and not Free. OutOfMemory as Set. Expected O(1); growth
allocates from a.

### `HeapMap.Set`

```
export HeapMap[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void
```

- Errors: OutOfMemory

Sets k's value to v. When k is present the stored key stays, and the old
value and the given k are released with a; a call is E0701 when K or V is
linear and not Free. OutOfMemory when the table cannot grow for a new key,
and then the caller still owns k and v. Expected O(1); growth is amortized
O(1) and allocates from a.

### `HeapText`

```
export linear view struct HeapText
```

A growable text buffer whose memory comes from an allocator. It remembers
its allocator, and a grow or free with another one panics (code 12).
Linear: end it with Free. It implements no Eq or Hash, so it is no
HeapMap key: a text key belongs in a TextMap.

### `HeapText.Clear`

```
export HeapText.Clear(mut self)
```

Empties the text and keeps its buffer for reuse. O(1).

### `HeapText.Len`

```
export HeapText.Len(&self): int
```

The text's length in bytes. O(1).

### `HeapText.New`

```
export HeapText.New(a: &Alloc): HeapText
```

- Flows: The result borrows from a.

An empty text that will allocate from a. O(1); allocates nothing until the
first Push or Set.

### `HeapText.Push`

```
export HeapText.Push(mut self, a: &Alloc, s: str): OutOfMemory!void
```

- Errors: OutOfMemory

Appends s. OutOfMemory when the buffer cannot grow, and then the text is
unchanged. Amortized O(s.Len()); a growth checks that a is the text's
allocator (code 12 otherwise), allocates from a and frees the old buffer.

### `HeapText.Set`

```
export HeapText.Set(mut self, a: &Alloc, s: str): OutOfMemory!void
```

- Errors: OutOfMemory

Replaces the text with s. OutOfMemory when the buffer cannot grow, and
then the text is unchanged. O(s.Len()); when s fits the capacity it is
copied in place and nothing is allocated. Otherwise the buffer grows from
a, which must be the text's allocator (code 12 otherwise), and the old
bytes are not copied.

### `HeapText.Str`

```
export HeapText.Str(&self): str
```

- Flows: The result borrows from self.

The text as a str, borrowed from the HeapText. O(1); allocates nothing.

### `Io`

```
export struct Io
```

The handle through which a program reaches the OS: every operation that
may wait or touch the outside world (files, sockets, clocks, sleeping,
randomness, task scopes) takes an Io. Main gets one in System.io. The same
program runs on the native runtime or under a simulator (std.sim),
depending on the Io it is given. A function that takes an Io never runs
at compile time.

An Io is a plain value that holds no loans and may be copied and shared
between tasks freely. It names no task: the runtime finds the current task
itself.

### `Io.NoCancel`

```
export Io.NoCancel(self): Io
```

Returns the same Io with the cancellation in force masked: Cancel, an
early exit and the deadlines of the scopes enclosing the call never
reach an operation on it. Use it for cleanup that must finish, such as
a final write or a close.

A scope opened from the masked Io, here or later, is a new boundary. Its
own Cancel and deadline reach the operations in it and in the scopes
under it, and Expired tells why.

An operation on the masked Io is cancelled only by such a boundary
(opened from this Io or from one masked later), or by a scope under one.
A scope opened below the mask from an unmasked Io never cancels it.

A scope opened inside a scope of the masked Io, from either Io, belongs
to that scope, so the scopes around the NoCancel call do not reach it.
An operation on the unmasked Io under no scope of the masked one is
still cancelled by them.

To keep an outer deadline in force, open that scope outside, and give
the masked Io only to the operations that must finish.

On an Io already masked, the result masks every scope either call would
mask. O(1): one call that reads the running task's scope depth; it
allocates nothing.

### `Io.Random`

```
export Io.Random(self, buf: mut []u8): Canceled!void
```

- Errors: Canceled

Fills buf with bytes from the OS random source. Fails with Canceled when
the scope is cancelled, or on an Io with no OS behind it; buf may then
hold anything and must not be used. O(buf.Len()); it allocates nothing.

### `Io.Scope`

```
export Io.Scope(self): Scope
```

Opens a task scope (see Scope). Its record lives in the caller's frame.
O(1); it allocates nothing.

### `Io.ScopeOf`

```
export Io.ScopeOf[E](self): Scope[E]
```

Opens a task scope whose tasks may fail with E. O(1); it allocates
nothing, as Scope.

### `Io.ScopeWith`

```
export Io.ScopeWith(self, l: Limits): Scope
```

Opens a task scope under the limits l. O(1); it allocates nothing, as
Scope.

### `Io.Sleep`

```
export Io.Sleep(self, d: Duration): Canceled!void
```

- Errors: Canceled

Suspends the task for at least d. Fails with Canceled when the scope is
cancelled first. O(1) here: one runtime call; it allocates nothing.

A zero or negative d returns at once and is still a cancellation point.
A sleep ended by a cancel can be repeated: it consumed nothing.

### `Io.Yield`

```
export Io.Yield(self): Canceled!void
```

- Errors: Canceled

Lets other tasks run before this one continues. Fails with Canceled
when the scope is cancelled. O(1) here: one runtime call; it allocates
nothing.

### `Limits`

```
export struct Limits
```

The limits of a scope opened with Io.ScopeWith: at most tasks tasks at
once, each with a stack of size stack. s.go waits while the scope is full.
tasks must be at least 1; a smaller value panics the scope (code 8).

Fields:

- `tasks: int`: The most tasks the scope runs at once; at least 1.
- `stack: StackSize`: The stack size of each task of the scope.

### `Lines`

```
export view struct Lines
```

An iterator over the lines of a text, made by str.Lines.

### `Lines.Next`

```
export Lines.Next(mut self): ?str from self.text
```

- Flows: The result borrows what self.text refers to; self is free after the call.

The next line, or none after the last. O(line length); allocates nothing.

Up to the next \n, with one \r before it stripped; a final newline ends
the last line without starting an empty one; a lone \r is kept.

### `List`

```
export view struct List[T]
```

A growable array in an arena. A growth makes the capacity the largest of
4, twice the old capacity and what is needed. It grows in place when the
buffer is the arena's last block; otherwise it takes a new buffer and
leaves the old one to the arena.

### `List.Clear`

```
export List[T].Clear(mut self)
```

Empties the list and keeps its capacity. A list that still holds linear
elements panics (code 8) rather than drop them. O(1).

### `List.Done`

```
export List[T].Done(self): mut []T
```

- Flows: The result borrows from self.

Ends the list and returns its elements as a slice in the list's arena,
valid as long as the arena. O(1); allocates nothing.

### `List.Insert`

```
export List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Inserts x before element i (i == Len() appends). Panics (code 2) unless
0 <= i <= Len(), before any growth. OutOfMemory when the buffer cannot
grow, and then the caller still owns x. O(Len() - i); growth allocates from
the arena.

### `List.Len`

```
export List[T].Len(&self): int
```

The number of elements. O(1).

### `List.New`

```
export List[T].New(a: &Arena): List[T]
```

- Flows: The result borrows from a.

An empty list in a. O(1); allocates nothing until the first Push. While
the capacity is 0, its slices (from Items or Done) still have a non-null
data pointer.

### `List.Pop`

```
export List[T].Pop(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes and returns the last element, or none when the list is empty. O(1);
allocates nothing.

### `List.Push`

```
export List[T].Push(mut self, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Appends x. OutOfMemory when the arena cannot grow the buffer, and then the
caller still owns x. Amortized O(1); growth allocates from the arena.

### `List.Remove`

```
export List[T].Remove(mut self, i: int): T
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes and returns element i, keeping the order of the rest. Panics
(code 2) unless 0 <= i < Len(). O(Len() - i); allocates nothing.

### `List.Resize`

```
export List[T].Resize(mut self, n: int, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Makes the length n: keeps the first n elements and appends copies of x
up to n, growing at most once. The elements are copies, so T must be
plain: a call with any other T is E0701. The call stores x once, so a
body generic in T can fill a list with one value; there a second Push of
the same value is E0604, since it would store it through the copies
already in the list.
Panics (code 2) when n < 0. OutOfMemory when the buffer cannot grow, and
then the list is unchanged. O(n - Len()) for n above Len(), else O(1),
plus a copy of Len() elements when the buffer grows; growth allocates
from the arena.

### `List.Truncate`

```
export List[T].Truncate(mut self, n: int)
```

Keeps the first n elements and drops the rest, keeping the capacity; n at
or above Len() changes nothing. Panics (code 2) when n < 0, and (code 8)
when it would drop a linear element, as Clear does. O(1); allocates
nothing.

### `Map`

```
export view struct Map[K: Hash + Eq, V]
```

A hash map in an arena, a Swiss table. When the table grows, the old one
is left to the arena. Iteration order is unspecified; it depends only on
the key type, the insertions and removals, and the HashKey if there is
one.

### `Map.Ensure`

```
export Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

The value for k, writable, inserting v first when k is absent. When k is
present the given k and v are dropped, so a call with a linear K or V is
E0701. OutOfMemory as Set. Expected O(1); growth is amortized O(1) and
allocates from the arena.

### `Map.Get`

```
export Map[K, V].Get(&self, k: &K): ?&V
```

- Flows: The result borrows from self.

The value stored for k, or none. Expected O(1); allocates nothing.

### `Map.GetMut`

```
export Map[K, V].GetMut(mut self, k: &K): ?mut V
```

- Flows: The result borrows from self.

The value stored for k, writable in place, or none. Expected O(1); allocates
nothing.

### `Map.Has`

```
export Map[K, V].Has(&self, k: &K): bool
```

Whether k is present. Expected O(1); allocates nothing.

### `Map.Iter`

```
export Map[K, V].Iter(&self): MapIter[K, V]
```

- Flows: The result borrows from self.

An iterator over the entries, in the map's unspecified order. O(1); a full
walk is O(capacity).

### `Map.Len`

```
export Map[K, V].Len(&self): int
```

The number of entries. O(1).

### `Map.New`

```
export Map[K, V].New(a: &Arena): Map[K, V]
```

- Flows: The result borrows from a.

An empty map in a. It hashes with a fixed function, so its order is the
same in every run and at compile time. The function, and so the order,
may change between releases. Whoever picks the keys can make them collide
and each operation O(n), so make a map of keys from untrusted input with
NewKeyed. O(1); allocates nothing until the first insert.

### `Map.NewKeyed`

```
export Map[K, V].NewKeyed(a: &Arena, key: HashKey): Map[K, V]
```

- Flows: The result borrows from a.

As New, but hashing with SipHash-1-3 under key. While key is secret, an
attacker cannot choose keys that collide. O(1); allocates nothing.

### `Map.Remove`

```
export Map[K, V].Remove(mut self, k: &K): ?V
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes k and returns its value, or none when k is absent. The stored key
is dropped, so a call with a linear K is E0701. Expected O(1); the arena
keeps the memory.

### `Map.Replace`

```
export Map[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
```

- Flows: The result borrows from v. The result borrows what self refers to; self is free after the call.
- Errors: OutOfMemory

Sets k's value to v and returns the old one, or none when k was absent.
When k is present the stored key stays and the given k is dropped, so a
call with a linear K is E0701. V may be linear, since the old value comes
back. OutOfMemory as Set. Expected O(1); growth is amortized O(1) and
allocates from the arena.

### `Map.Set`

```
export Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
```

- Errors: OutOfMemory

Sets k's value to v. When k is present the stored key stays, and the
given k and the old value are dropped, so a call with a linear K or V is
E0701. OutOfMemory when the table cannot grow for a new key, and then the
caller still owns k and v. Expected O(1); growth is amortized O(1) and
allocates from the arena.

### `MapIter`

```
export view struct MapIter[K, V]
```

An iterator over a Map's or HeapMap's entries, in the map's iteration
order. The map stays borrowed while the iterator lives.

### `MapIter.Next`

```
export MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map
```

- Flows: The result borrows what self.map refers to; self is free after the call.

The next entry, or none after the last; allocates nothing. A full walk is
O(capacity): one 8-byte group read per 8 slots. A table never shrinks,
since removal only marks slots, so one call on a map that removals left
sparse may read up to capacity / 8 groups.

### `Method`

```
export struct Method
```

A method of an interface, an inherent function of a named struct or enum
(Type.Methods), or a function named as a gen argument (Receiver Static).

### `Method.Flows`

```
export Method.Flows(&self): []Flow
```

The flow clauses the signature declares, one per source of a `from`, in
one fixed order: by kind (From, Into, Ends, Store), then field, then
parameter, then path. Inferred sources are not clauses. O(f) in the
clauses. Compile time only.

### `Method.Generic`

```
export Method.Generic(&self): bool
```

Whether the method has type parameters of its own. Compile time only.

### `Method.HasFlows`

```
export Method.HasFlows(&self): bool
```

Whether the signature declares from, into, *q into p or ends. O(f) in the
clauses. Compile time only.

### `Method.Name`

```
export Method.Name(&self): str
```

The method's name. Compile time only.

### `Method.Params`

```
export Method.Params(&self): []Param
```

The parameters after the receiver, in order. On a generic method it is
E0901, so test Generic first. Compile time only.

### `Method.Receiver`

```
export Method.Receiver(&self): Receiver
```

How the method takes its receiver: Static, Value, Ref or Mut. Compile time
only.

### `Method.Result`

```
export Method.Result(&self): Type
```

The result type with its error set: a Fallible for E!T, the Scalar void
when there is no result. On a generic method it is E0901. Compile time
only.

### `Method.Signature`

```
export Method.Signature(&self): Code
```

The signature printed as (receiver, name: type, ...): result, with Self kept
and the flows left out. Compile time only.

### `Mutex`

```
export struct Mutex[T]
```

A lock around a value of T. Lock waits for it and returns a Guard, the
only way to reach the value; Unlock on the Guard releases it. Waiters
queue in FIFO order, but a free lock goes to whichever task takes it
first. A waiter that has waited 1 ms and then loses the lock once more
goes to the front, and the next Unlock hands the lock to it directly.
Share when T is Send, so tasks share a &Mutex. There is no condition
variable: to wait for a condition, unlock, wait on a sync.Notify, and
lock again.

### `Mutex.Into`

```
export Mutex[T].Into(self): T
```

- Flows: The result borrows from self.

Ends the Mutex and returns its value. A Mutex passed by value is not
borrowed, so it is unlocked and nobody waits for it. O(1); allocates
nothing.

### `Mutex.Lock`

```
export Mutex[T].Lock(&self, io: Io): Canceled!Guard[T]
```

- Flows: The result borrows from self.
- Errors: Canceled

Waits for the lock and returns its Guard. Canceled when the scope is
canceled, and then the lock is not held. O(1) uncontended; allocates nothing
(a waiter's queue cell is on its own stack).

Lock checks for cancellation first: in a cancelled scope it returns
Canceled even when the lock is free, unless io's NoCancel mask covers
that scope (a scope opened from the masked Io is not covered). A
cancelled scope stays cancelled, so every later Lock in it returns
Canceled too, until the scope's block exits. A free lock is taken by
one compare-and-swap; otherwise Lock waits. A wait that is cancelled
returns Canceled without the lock.

### `Mutex.New`

```
export Mutex[T].New(x: T): Mutex[T]
```

- Flows: The result borrows from x.

An unlocked mutex holding x. O(1); no allocation, so it can initialize a
`var` global.

### `Ord`

```
export interface Ord {
    Less(&self, other: &Self): bool
}
```

A strict weak order: Less is true when self comes before other. &T is Ord
when T is, and orders the referents. On floats it is IEEE <, so a NaN is
not ordered; sorting a slice that holds one leaves its elements in some
order, with no panic.

### `Os`

```
export open enum Os
```

The target's operating system: Linux, Windows, Macos, Freestanding or
Nutshell. The enum is open, so a match on it needs a `_` arm.

Variants:

- `Linux`
- `Windows`
- `Macos`
- `Freestanding`
- `Nutshell`

### `OutOfMemory`

```
export error OutOfMemory
```

An allocator or arena could not supply the memory a call needed.

### `Param`

```
export struct Param
```

A parameter of an interface method, after the receiver.

### `Param.Const`

```
export Param.Const(&self): bool
```

Whether the parameter is const. Compile time only.

### `Param.Name`

```
export Param.Name(&self): str
```

The parameter's name. Compile time only.

### `Param.Type`

```
export Param.Type(&self): Type
```

The parameter's type. Compile time only.

### `ParseError`

```
export error ParseError
```

The text is not a number of the requested type, or the number is out of its
range.

### `Path`

```
export view struct Path
```

A file system path: a view over bytes as the OS gives them, which need
not be UTF-8. The bytes live in an arena or in a literal's static data,
and the Path borrows them. On Windows a Path holds the name's WTF-8, so a
name with an unpaired surrogate round-trips.

In a format hole a Path prints valid UTF-8 as written, and otherwise one
U+FFFD for each maximal subpart that is not UTF-8 (Unicode's recommended
substitution), so an unpaired surrogate's bytes ED A0 80 print as three.
Printing allocates nothing.

### `Path.AsBytes`

```
export Path.AsBytes(&self): []u8 from self.bytes
```

- Flows: The result borrows what self.bytes refers to; self is free after the call.

The path's bytes, as the OS gave them. O(1); allocates nothing.

### `Path.Base`

```
export Path.Base(&self): Path from self.bytes
```

- Flows: The result borrows what self.bytes refers to; self is free after the call.

The last element of the path. O(n); allocates nothing.

Trailing separators are dropped first; the path is not otherwise
cleaned. "." for an empty path or a drive alone ("C:"), and the root's
last separator for a root ("/", "C:\").

### `Path.Join`

```
export Path.Join(&self, a: &Arena, part: str): OutOfMemory!Path from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

The path with part appended, in a. One block of exactly the result's
length from a; O(n).

- A part with a root and no drive (`/y`, and `\y` on Windows) replaces
  the path but keeps its drive: `C:\x` joined with `\y` is `C:\y`.
- A part with a drive (`D:\y`, `D:y`, Windows only) replaces the whole
  path.
- Otherwise the result is the path, the native separator, then part. No
  separator is added when the path is empty, already ends with one, or
  is a drive alone ("C:").
- An empty part gives a copy of the path.

Drives are the only prefixes Path knows: a UNC `\\server\share` prefix
is read as a root of separators.

### `Path.New`

```
export Path.New(a: &Arena, bytes: []u8): OutOfMemory!Path from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A Path holding a copy of bytes in a, as one block of exactly bytes.Len()
bytes, aligned 1. OutOfMemory leaves a as it was. O(n).

### `Path.Parent`

```
export Path.Parent(&self): ?Path from self.bytes
```

- Flows: The result borrows what self.bytes refers to; self is free after the call.

The parent of the path, or none when it has none. O(n); allocates nothing.

The path without its last element and the separators before it. none
for an empty path, a root, and a single relative element such as "a"
("C:a" has the parent "C:"). Trailing separators are dropped first, so
the parent of "a/b/" is "a".

### `Path.Str`

```
export Path.Str(&self): ?str from self.bytes
```

- Flows: The result borrows what self.bytes refers to; self is free after the call.

The path as a str, or none unless its bytes are well-formed UTF-8, as
str.From checks them. O(n); allocates nothing.

### `Print`

```
export Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void
```

- Errors: WriteError

Writes f to w with each {hole} replaced by its field of args, formatted by
its Format or by the hole's spec. WriteError comes from the Writer or from
a user's Format. O(output length) plus the Format calls; allocates
nothing. An intrinsic: the compiler writes each piece in turn (through
FmtPadRun's tables when a hole has a width), stopping at the first error.

f is checked against args at compile time: every hole names a field or
a field path of args, every field is used, and every spec is valid, else
E0706. {{ and }} write literal braces. A string with no holes takes no
record: Print(w, "done").

### `ReadError`

```
export error ReadError
```

A read failed, or a line did not fit its buffer.

### `Reader`

```
export view struct Reader
```

A source of bytes: an optional inline buffer, and a table that refills
it. System.Stdin, File.Reader, Reader.Of and the network streams hand
them out; a Reader borrows what it reads from. Read, ReadAll, Until and
Line read from one.

A read interrupted by a cancel or a deadline fails with ReadError;
whether it was a timeout is read from the scope (Scope.Expired).

### `Reader.Line`

```
export Reader.Line(mut self, a: &Arena): ReadError|OutOfMemory!?str from a
```

- Flows: The result borrows from a.
- Errors: ReadError, OutOfMemory

The next line copied into a, without its \n or \r\n, or none at the end of
input. ReadError when the bytes are not UTF-8, the line is longer than the
buffer, or the source fails; OutOfMemory when a cannot hold the copy. O(line
length); allocates the line in a.

The last line is returned even when it has no \n; a lone \r at its end
is kept. A line that fills the buffer exactly is still a line. A source
that reports a negative count or more bytes than it was given, or that
calls back into this Reader and moves its position during a fill, is a
ReadError, never a silent end of input or a read past the buffer. A
reader with no buffer of its own (Stdin(), Reader.Of) reads one line per
call; a line over 64 KiB is ReadError. On a buffered reader, a Line
interrupted by a cancel consumes nothing: call it again.

### `Reader.Of`

```
export Reader.Of(b: []u8): Reader from b
```

- Flows: The result borrows from b.

Returns a Reader over the bytes of b, from the first. Read copies them
out with no table call and returns 0 once every byte is read, Until
scans b in place, and Line reads as on a reader with no buffer (one
table call per line, lines up to 64 KiB, as a Stdin() reader's). It
never writes b and holds b's loan. It takes no Io, so it runs at compile
time. O(1); allocates nothing.

### `Reader.Read`

```
export Reader.Read(mut self, buf: mut []u8): ReadError!int
```

- Errors: ReadError

Reads at most buf.Len() bytes into buf and returns how many, 0 at end of
input. It may return fewer than asked; ReadAll loops until buf is full.
Fails with the table's ReadError. O(count) plus at most one table read;
allocates nothing.

Buffered bytes are served first. Otherwise it makes one table read:
straight into buf, with no copy, when buf is at least as large as the
inline buffer, else into the inline buffer to serve from. A table that
reports a negative count or more bytes than it was given breaks its
contract: ReadError, never a silent end of input.

On a buffered reader Read is repeatable: a call interrupted by a cancel
consumed nothing, and calling it again is safe.

### `Reader.ReadAll`

```
export Reader.ReadAll(mut self, buf: mut []u8): ReadError!int
```

- Errors: ReadError

Reads until buf is full or the input ends, and returns the count read:
less than buf.Len() only at end of input. Fails with the table's
ReadError; the bytes read before the failure are in buf but not
counted, so the call is prefix under a cancel. O(buf.Len()) plus a table
read per Read call; allocates nothing.

### `Reader.Until`

```
export Reader.Until(mut self, delim: u8, buf: mut []u8): ReadError!int
```

- Errors: ReadError

Reads into buf up to and including the first delim and returns the count.
Fails with the table's ReadError. O(count); allocates nothing.

It stops after delim or when buf is full, refilling the inline buffer as
needed, and returns 0 at end of input. At end of input after some bytes
it returns them, and the next call returns 0. It never takes a byte past
delim from the source: a buffered reader keeps what follows delim in its
buffer, and a reader with no inline buffer asks its table for one byte
at a time (for \n, the table's line entry, which stops after the
newline). A Reader.Of is scanned in place, as a buffered reader is.
Each buffered byte is searched once. A table that reports a negative
count or more bytes than it was given is ReadError. An interrupted call
is prefix: the bytes it took are lost.

### `Receiver`

```
export enum Receiver
```

How a method takes its receiver: none, self, &self or mut self.

Variants:

- `Static`: No receiver: a function called on the type, or a function named as a gen
  argument.
- `Value`: self: the method takes the receiver by value.
- `Ref`: &self: the method borrows the receiver.
- `Mut`: mut self: the method borrows the receiver mutably.

### `Replace`

```
export Replace[T](dst: mut T, x: T into dst): T
```

- Flows: The result borrows from x. The result borrows what dst refers to; dst is free after the call.

Puts x in dst and returns the value that was there, for any T, linear
included: the way to take a linear value out of a place.
O(SizeOf[T]()); allocates nothing and cannot fail.

### `Scalars`

```
export view struct Scalars
```

An iterator over the Unicode scalars of a text, made by str.Scalars.

### `Scalars.Next`

```
export Scalars.Next(mut self): ?char
```

The next scalar, or none at the end of the text. O(1); allocates nothing.

### `Scope`

```
export mut view struct Scope
```

A task scope, opened with Io.Scope, Io.ScopeOf or Io.ScopeWith: the
tasks spawned in it end before it does, and Cancel and CancelAfter reach
every operation under it. Its 128-byte record lives in the scope's local
on the caller's frame, so opening one allocates nothing. Scope[E] also
holds the scope's error.

### `Scope.Cancel`

```
export Scope.Cancel(&self)
```

Cancels the scope: each cancellable operation in it, and in the scopes
nested in it, fails with Canceled (a Reader or Writer reports ReadError
or WriteError). O(scopes nested in it plus the tasks blocked in them):
one walk, taking each scope's lock.

### `Scope.CancelAfter`

```
export Scope.CancelAfter(&self, d: Duration)
```

Cancels the scope once d has passed, as Cancel does: every cancellable
operation under it, in the body, its tasks and its nested scopes, fails
with Canceled. Only the earliest deadline counts, and a zero or negative
d cancels at once. The scope owns the timer, and its exit disarms it at
once. Under Scope[E] a deadline adds no error. The deadline runs on the
clock of the scope's Io. O(log n) in the timers armed; it allocates
nothing past the growth of the runtime's timer heap.

A timeout on one operation is a scope around it: no operation takes a
timeout argument, no Scope function takes a time.Instant, and an Io
holds no deadline. An operation is cancelled through the scopes of the
task that runs it, whichever Io it was given. For a deadline at an
instant t, call s.CancelAfter(t.Since(time.Now(io))).

### `Scope.Expired`

```
export Scope.Expired(&self): bool
```

Reports whether this scope's own deadline fired while it was not yet
cancelled: true tells a timeout from a Cancel. Once true it stays true
until the scope's block exits. A scope cancelled first, by Cancel or
through an ancestor, disarms its timer and never expires. O(1).

To see a deadline after a join, nest two scopes: the deadline on the
outer one, the tasks in the inner one, and Expired read after the inner
block. Under ScopeOf[E], a task given &s maps its Canceled to
time.TimedOut when s.Expired().

### `Scope.Remaining`

```
export Scope.Remaining(&self): ?Duration
```

Returns the least time left over the deadlines of this scope and its
ancestors, at least zero, or none when none of them has a deadline.
Deadlines above a NoCancel boundary do not count. O(depth of the scope
chain).

### `Set`

```
export view struct Set[T: Hash + Eq]
```

A hash set in an arena: a Map with no values.

### `Set.Add`

```
export Set[T].Add(mut self, x: T into self): OutOfMemory!bool
```

- Errors: OutOfMemory

Adds x and returns true when it was not a member. When it was, the stored
member stays and x is dropped, so a call with a linear T is E0701.
OutOfMemory when the table cannot grow, and then the caller still owns x.
Expected O(1); growth is amortized O(1) and allocates from the arena.

### `Set.Has`

```
export Set[T].Has(&self, x: &T): bool
```

Whether x is a member. Expected O(1); allocates nothing.

### `Set.New`

```
export Set[T].New(a: &Arena): Set[T]
```

- Flows: The result borrows from a.

An empty set in a. It hashes with a fixed function, so its order is the
same in every run and at compile time. The function may change between
releases, and it is not safe for keys from untrusted input: use NewKeyed.
O(1); allocates nothing until the first Add.

### `Set.NewKeyed`

```
export Set[T].NewKeyed(a: &Arena, key: HashKey): Set[T]
```

- Flows: The result borrows from a.

As New, but hashing with SipHash-1-3 under key, for members from
untrusted input. O(1); allocates nothing.

### `Set.Remove`

```
export Set[T].Remove(mut self, x: &T): bool
```

Removes x and returns whether it was a member. The stored member is
dropped, so a call with a linear T is E0701. Expected O(1); the arena
keeps the memory.

### `Slice`

```
export interface Slice[T] {
    Items(&self): []T
    ItemsMut(mut self): mut []T
}
```

The interface of indexable types: []T, [N]T, List and HeapList. Every
&self method of Slice[T] works on each of them. A mut self method needs a
mut []T, a var array, a List or a HeapList: other receivers are E0507,
and a local that is not var is E0508.

### `Slice.Chunks`

```
export Slice[T].Chunks(&self, n: int): Chunks[T] from self
```

- Flows: The result borrows from self.

The elements in pieces of n, the last one shorter; an empty slice
gives none. Panics (code 8) unless n > 0. O(1); each Next is O(1).

### `Slice.ChunksMut`

```
export Slice[T].ChunksMut(mut self, n: int): ChunksMut[T] from self
```

- Flows: The result borrows from self.

The elements in mut pieces of n, the last one shorter, each handed out
once. Panics (code 8) unless n > 0. O(1); each Next is O(1).

### `Slice.CopyFrom`

```
export Slice[T].CopyFrom(mut self, src: []T) *src into self
```

- Flows: Stores the loans src holds in self.

Copies src's elements over self's. O(Len()); allocates nothing.

T must be plain: a call with any other T is E0701. The lengths must be
equal, else it panics (code 2) before any store. The copy is one block
move of the bytes, correct even when the two views overlap.

### `Slice.Fill`

```
export Slice[T].Fill(mut self, x: T into self)
```

Sets every element to x. O(Len()); allocates nothing.

Each element gets a copy of x, so T must be plain: a call with any other
T is E0701.

### `Slice.Get`

```
export Slice[T].Get(&self, i: int): ?&T
```

- Flows: The result borrows from self.

some(&self[i]) when 0 <= i < Len(), else none; never panics. O(1);
allocates nothing.

### `Slice.GetMut`

```
export Slice[T].GetMut(mut self, i: int): ?mut T
```

- Flows: The result borrows from self.

Element i, writable, when 0 <= i < Len(), else none; never panics. O(1).

### `Slice.GetPair`

```
export Slice[T].GetPair(mut self, i: int, j: int): ?(a: mut T, b: mut T) from self
```

- Flows: The result borrows from self.

Both elements i and j, mut; none when i == j or either is out of
range, never a panic. O(1). As for SplitAt, the library answers for the
two being apart, and each holds the receiver's loan.

### `Slice.Has`

```
export Slice[T].Has(&self, x: &T): bool
```

Whether some element equals x; T needs Eq (E0701 at the call). O(Len());
allocates nothing.

### `Slice.Index`

```
export Slice[T].Index(&self, x: &T): ?int
```

The index of the first element equal to x, or none; T needs Eq (E0701 at
the call). O(Len()) calls of Eq; allocates nothing. A u8 slice is
searched a word at a time, with vectors past the first 32 bytes, except
on x86_64-freestanding.

### `Slice.Len`

```
export Slice[T].Len(&self): int
```

The number of elements. O(1).

### `Slice.Reverse`

```
export Slice[T].Reverse(mut self)
```

Reverses the elements in place. O(Len()); allocates nothing.

### `Slice.Search`

```
export Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int
```

Binary search: on a slice where less(element, key) holds for a prefix
and not after it, the prefix's length. That is the first index whose
element is not less than key, or Len() when there is none: the place
where key goes to keep the slice sorted. On any other slice it returns
some index from 0 to Len(). O(log n), at most ceil(log2(n + 1)) calls of
less; allocates nothing and never panics.

### `Slice.Sort`

```
export Slice[T].Sort(mut self, const less: fn(&T, &T): bool)
```

Sorts the elements by less, in place; equal elements may change order.
O(n log n) in the worst case, and the same result on every run; it
allocates nothing. A less that needs context is a function literal that
reads it, so no sort or search takes a separate context argument. The
algorithm is a
pattern-defeating quicksort.

### `Slice.SortStable`

```
export Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void
```

- Errors: OutOfMemory

Sorts the elements by less, keeping equal elements in their order.
O(n log n) calls of less and moves on every input, and O(n) on input
that is already sorted or strictly descending. Past 20 elements it
merges through scratch memory of Len() / 2 elements or more (the whole
slice while that is at most 8,000,000 bytes, at least 48 elements): on
the stack when that fits in 4,096 bytes and T is aligned to at most 8,
and otherwise from a, falling back to Len() / 2 elements from a before
failing, and freed before it returns. Below 21 elements, for a
zero-size T, or when the scratch fits on the stack, it allocates
nothing from a. Fails with OutOfMemory when a cannot give even
Len() / 2 elements of scratch, before any element moves, so the slice
is unchanged. The algorithm is Rust's driftsort. For a stable sort
that takes no allocator, use SortStableInPlace.

### `Slice.SortStableInPlace`

```
export Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)
```

Sorts the elements by less, keeping equal elements in their order,
strictly in place with no allocator or heap allocation. O(n log n)
calls of less and O(n log n) moves on every input, with a fixed
stack cache of at most 4,096 bytes for elements aligned to at most 8.
Works at compile time and in freestanding programs. The algorithm is
a block merge sort. For faster sorting through scratch memory from
an allocator, use SortStable.

### `Slice.SplitAt`

```
export Slice[T].SplitAt(mut self, i: int): (left: mut []T, right: mut []T) from self
```

- Flows: The result borrows from self.

The first i elements and the rest, both mut. Panics (code 2) unless
0 <= i <= Len(). O(1). The compiler cannot prove two views of one slice
apart; the library answers for it, and each half holds the loan the
receiver held.

### `Slice.Swap`

```
export Slice[T].Swap(mut self, i: int, j: int)
```

Exchanges elements i and j; i == j changes nothing. Panics (code 2)
unless both are in 0..Len(). O(1).

### `Split`

```
export view struct Split
```

An iterator over the pieces of a text between occurrences of a
separator, made by str.Split.

### `Split.Next`

```
export Split.Next(mut self): ?str from self.text
```

- Flows: The result borrows what self.text refers to; self is free after the call.

The next piece before the separator, or none after the last. Amortized
O(piece length + separator length); allocates nothing.

Empty pieces are kept, and "" yields one empty piece. A one-byte
separator such as "," is found with a byte scan, a longer one as
str.Find finds it.

### `StackSize`

```
export enum StackSize
```

The size of a task's stack. Default is 1 MiB of address space; Large is
what Main gets (16 MiB); Bytes(n) is n bytes rounded up to a power of two
from 64 KiB to 512 MiB. A larger n, or a reservation the OS refuses,
panics the scope (code 8). Stack memory is touched only on demand. Each
Bytes size has a region of its own, reserved the first time a scope asks
for it. StackSize is not open, so a match on it outside std must cover
Bytes.

Variants:

- `Default`: 1 MiB of address space.
- `Large`: 16 MiB, the size Main gets.
- `Bytes(n: int)`: n bytes, rounded up to a power of two from 64 KiB to 512 MiB.

### `Swap`

```
export Swap[T](a: mut T, b: mut T) *a into b, *b into a
```

- Flows: Stores the loans a holds in b. Stores the loans b holds in a.

Exchanges the values of a and b, for any T, linear included.
O(SizeOf[T]()); allocates nothing and cannot fail.

### `System`

```
export struct System
```

What Main is given: the command-line arguments (args[0] is the
program's path, as it was started), the Io, the process arena and the heap. It also gives
the standard streams (Stdout, Stderr, Stdin, Print) and the environment
(Env).

Fields:

- `args: []str`: The command-line arguments; args[0] is the program's path, as it was started.
- `io: Io`: The Io that Main runs on.
- `arena: &Arena`: The process arena.
- `heap: &Heap`: The process heap.

### `System.Env`

```
export System.Env(&self, name: str): ?str from self
```

- Flows: The result borrows from self.

Returns the value of the environment variable name, or none when it is
not set. The environment is the one read when the process started; when
a name appears twice, the first wins. O(total bytes of the names); it
allocates nothing.

On Windows, names compare without regard to ASCII case, so Path and
PATH are one variable; a non-ASCII letter must match exactly. Windows
itself also folds non-ASCII letters, so such a name can be found by the
OS and not here.

### `System.Print`

```
export System.Print[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
```

- Errors: WriteError

Formats args by f and writes the text to standard output, as Print on
Stdout() does. It writes to the one process buffer, so a call costs no
more than one Print. Fails with WriteError when the stream fails, or
with the error of a user Format. O(output length) plus the Format calls;
it allocates nothing.

f is a const argument: the compiler checks each call's holes against
args at that call, and a mismatch is E0706. With no hole, args may be
left out: sys.Print("done\n").

### `System.PrintErr`

```
export System.PrintErr[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
```

- Errors: WriteError

Formats args by f and writes the text to standard error, as Print on
Stderr() does. Fails with WriteError when the stream fails, or with the
error of a user Format. O(output length) plus the Format calls; it
allocates nothing. With no hole, args may be left out, as for Print.

### `System.Stderr`

```
export System.Stderr(&self): Writer
```

- Flows: The result borrows from self.

Returns a Writer to standard error, over its shared process buffer. It
is flushed as standard output is, when Main returns or proc.Exit runs;
if that last flush fails, the process exits with status 1 where it would
have exited 0. A write to it is not a cancellation point: it goes on
under a cancelled scope. To stop writing, check the scope's cancellation
with another Io call. O(1); it allocates nothing.

### `System.Stdin`

```
export System.Stdin(&self): Reader
```

- Flows: The result borrows from self.

Returns a Reader from standard input, over the shared 64 KiB process
buffer. A read that must wait for input flushes standard output and
standard error first, so a prompt shows before the program waits. A
line read from it with Reader.Line may be up to 64 KiB. O(1); it
allocates nothing.

### `System.Stdout`

```
export System.Stdout(&self): Writer
```

- Flows: The result borrows from self.

Returns a Writer to standard output. Every Writer from Stdout shares
the one 8 KiB process buffer, so taking one per call costs nothing.
The buffer is flushed when Main returns or proc.Exit runs, and Flush
forces it out earlier. If that last flush fails, the process writes
`error: WriteError on stdout` to standard error, when that still works,
and exits with status 1 where it would have exited 0. A write to it is
not a cancellation point: it goes on under a cancelled scope. To stop
writing, check the scope's cancellation with another Io call. O(1); it
allocates nothing.

### `TargetInfo`

```
export struct TargetInfo
```

The build's target, as the compile-time constant Target: arch (Arch), os
(Os), pointer_bytes and vector_bits (128 on x86_64 and aarch64). Each is
fixed by the target, never by --cpu.

Fields:

- `arch: Arch`
- `os: Os`
- `pointer_bytes: int`
- `vector_bits: int`

### `Text`

```
export view struct Text
```

A growable text buffer in an arena, always valid UTF-8: build a str
piece by piece with Push, PushChar or a Writer, then take it with Str
or Done. It grows as List does. HeapText is its linear form on the heap.

### `Text.Clear`

```
export Text.Clear(mut self)
```

Empties the text and keeps its buffer. O(1).

### `Text.Done`

```
export Text.Done(self): str
```

- Flows: The result borrows from self.

Ends the text and returns its bytes as a str in the arena, as List.Done
does. O(1); allocates nothing. The result borrows the arena, not the
Text, so a function can `return t.Done()` as a result `from a` without
a copy, where `return t.Str()` would return a borrow of a local (E0603).

### `Text.Drain`

```
export Text.Drain(mut self, n: int)
```

Removes the first n bytes and shifts the rest down; n must be from 0 to
the length and on a scalar boundary, else a str-boundary panic (code 10).
O(Len() - n) for the shift; allocates nothing, and the
capacity is kept.

### `Text.Len`

```
export Text.Len(&self): int
```

The length of the text so far in bytes, as Str().Len(). O(1).

### `Text.New`

```
export Text.New(a: &Arena): Text
```

- Flows: The result borrows from a.

An empty text in a. O(1); allocates nothing until the first Push.

### `Text.Push`

```
export Text.Push(mut self, s: str): OutOfMemory!void
```

- Errors: OutOfMemory

Appends s. OutOfMemory when the arena cannot grow the buffer, and then the
text is unchanged. Amortized O(s.Len()); growth allocates from the arena.

### `Text.PushChar`

```
export Text.PushChar(mut self, c: char): OutOfMemory!void
```

- Errors: OutOfMemory

Appends c's UTF-8 encoding. OutOfMemory as Push. Amortized O(1).

### `Text.PushReplaced`

```
export Text.PushReplaced(mut self, s: str, old: str, new: str): OutOfMemory!void
```

- Errors: OutOfMemory

Appends s with every occurrence of old replaced by new. Matches are found
from the left and never overlap; the search resumes after each replaced
match, and new is never searched: "aaa" with "aa" replaced by "b" gives
"ba". An empty old matches at every scalar boundary, so new goes before each
scalar and after the last: "ab" with "" replaced by "-" gives "-a-b-". A
valid old matches only whole scalars, so the text stays valid UTF-8.
OutOfMemory when the arena cannot grow the buffer, and then the text is
unchanged. O(s.Len() + old.Len()) search plus the copy; when new is not
longer than old, room for s.Len() more bytes is made once, before the first
copy, so appends stay amortized O(1) a byte.

### `Text.Str`

```
export Text.Str(&self): str
```

- Flows: The result borrows from self.

The text so far, valid UTF-8 always, in the arena. O(1); allocates
nothing.

### `Text.Writer`

```
export Text.Writer(mut self): Writer
```

- Flows: The result borrows from self.

A Writer that appends to self, for code that writes to any Writer.
Each write must be valid UTF-8 (WriteError otherwise), and OutOfMemory
becomes WriteError. O(1), allocating nothing; each write is O(its
bytes) and grows self. It has no buffer of its own: each write lands in
the text at once, and Flush does nothing. It needs no Io, so formatting
into a Text also runs at compile time.

### `TextMap`

```
export linear view struct TextMap[V]
```

A hash map from text to V whose table and key copies come from an
allocator. A new key of up to 15 bytes is kept inside the table, with no
allocation of its own; Set copies a longer new key into memory from a,
and Remove and Free give that memory back. It remembers its allocator,
and an allocation or free with another one panics (code 12). Linear: end
it with Free, FreeEmpty or FreeEach.

### `TextMap.Ensure`

```
export TextMap[V].Ensure(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!mut V from self
```

- Flows: The result borrows from self.
- Errors: OutOfMemory

The value for k, writable, inserting v first when k is absent. When k is
present the given v is released with a; a call is E0701 when V is linear
and not Free. OutOfMemory as Set. Expected O(1 + k.Len()); a new key is
copied, or kept in the table, as Set does.

### `TextMap.FreeEach`

```
export TextMap[V].FreeEach(self, a: &Alloc, const release: fn(V))
```

Hands every value to release, in the map's iteration order, freeing each
key copy, then frees the table; a must be the map's allocator (code 12
otherwise). O(capacity / 8) group reads plus Len() calls of release.

### `TextMap.FreeEmpty`

```
export TextMap[V].FreeEmpty(self, a: &Alloc)
```

Frees the table of an empty map; a must be the map's allocator (code 12
otherwise). Panics (code 11) when the map still holds an entry. It ends a
map whose V is
linear and not Free, after every entry is removed. O(1).

### `TextMap.Get`

```
export TextMap[V].Get(&self, k: str): ?&V
```

- Flows: The result borrows from self.

The value stored for k, or none. Expected O(1 + k.Len()); allocates nothing.

### `TextMap.GetMut`

```
export TextMap[V].GetMut(mut self, k: str): ?mut V
```

- Flows: The result borrows from self.

The value stored for k, writable in place, or none. Expected O(1 + k.Len());
allocates nothing.

### `TextMap.Iter`

```
export TextMap[V].Iter(&self): TextMapIter[V]
```

- Flows: The result borrows from self.

An iterator over the entries, in the map's unspecified order. O(1); a full
walk is O(capacity).

### `TextMap.Len`

```
export TextMap[V].Len(&self): int
```

The number of entries. O(1).

### `TextMap.New`

```
export TextMap[V].New(a: &Alloc): TextMap[V]
```

- Flows: The result borrows from a.

An empty map that will allocate its table and key copies from a. It
hashes with a fixed function, so its order is the same from run to run.
The function may change between releases, and it is not safe for keys
from untrusted input: use NewKeyed. O(1); allocates nothing until the
first insert.

### `TextMap.NewKeyed`

```
export TextMap[V].NewKeyed(a: &Alloc, key: HashKey): TextMap[V]
```

- Flows: The result borrows from a.

As New, but hashing with SipHash-1-3 under key, for keys from
untrusted input. O(1); allocates nothing.

### `TextMap.Remove`

```
export TextMap[V].Remove(mut self, a: &Alloc, k: str): ?V
```

- Flows: The result borrows what self refers to; self is free after the call.

Removes k and returns its value, or none when k is absent. The map's copy
of the key, if it made one, goes back to a, which must be the map's
allocator (code 12 otherwise). The empty key has no copy, so removing it
does not look at a. Expected O(1 + k.Len()).

### `TextMap.Replace`

```
export TextMap[V].Replace(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!?V
```

- Flows: The result borrows from v. The result borrows what self refers to; self is free after the call.
- Errors: OutOfMemory

Sets k's value to v and returns the old value, or none when k was absent.
OutOfMemory as Set. Expected O(1 + k.Len()); a new key is copied, or kept
in the table, as Set does.

### `TextMap.Set`

```
export TextMap[V].Set(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!void
```

- Errors: OutOfMemory

Sets k's value to v. When k is present its old value is released with a;
a call is E0701 when V is linear and not Free. OutOfMemory when copying a
new key or growing the table fails, and then the caller still owns v.
Expected O(1 + k.Len()); a new key of 16 bytes or more is copied into
k.Len() bytes from a, which the map owns, and a shorter one is kept in
the table.

### `TextMapIter`

```
export view struct TextMapIter[V]
```

An iterator over a TextMap's entries, in the map's iteration order. The
map stays borrowed while the iterator lives.

### `TextMapIter.Next`

```
export TextMapIter[V].Next(mut self): ?(key: str, value: &V) from self.map
```

- Flows: The result borrows what self.map refers to; self is free after the call.

The next entry, or none after the last; allocates nothing. A full walk is
O(capacity): one 8-byte group read per 8 slots. A table never shrinks,
since removal only marks slots, so one call on a map that removals left
sparse may read up to capacity / 8 groups.

### `Type`

```
export struct Type
```

A type as a generator sees it, with names resolved as in the module of
the gen line.

### `Type.Args`

```
export Type.Args(&self): []Type
```

- Flows: The result borrows from self.

The generic arguments of a named type, in order; const arguments are left
out. Compile time only.

### `Type.Elem`

```
export Type.Elem(&self): Type
```

The element type of a reference, slice, array, optional or raw pointer, or
the ok type of a fallible; any other kind is E0901. Compile time only.

### `Type.Exported`

```
export Type.Exported(&self): bool
```

Whether the named type is exported; true for an unnamed type. Compile time
only.

### `Type.Fields`

```
export Type.Fields(&self): []Field
```

The fields of a struct or error in declaration order, or of a record in
canonical order, with an instance's arguments substituted. Only exported
fields for a type of another module. Opaque is E0906, any other kind E0901.
Compile time only.

### `Type.Kind`

```
export Type.Kind(&self): TypeKind
```

The type's kind; a name not resolved yet is Opaque. Compile time only.

### `Type.Methods`

```
export Type.Methods(&self): []Method
```

An interface's methods in declaration order, or the inherent functions of a
named struct or enum in declaration order (only exported ones for a type of
another module), each a signature read; any other kind is E0901. O(m) in
the methods returned. Compile time only.

### `Type.Name`

```
export Type.Name(&self): Code
```

The type spelled as a resolved reference, generic arguments included
(fs.File, List[User]), every name pinned to its declaration. Compile time
only.

### `Type.Same`

```
export Type.Same(&self, other: Type): bool
```

Type identity. Two Opaque types are the same when they name the same not yet
generated item; a resolved and an Opaque type never are. Compile time only.

### `Type.Text`

```
export Type.Text(&self): str
```

The bare declared name of a named type (User), else the type's source
spelling ([]int). Compile time only.

### `Type.Variants`

```
export Type.Variants(&self): []Variant
```

An enum's variants in declaration order, payload types substituted as for
Fields; not an enum is E0901. Compile time only.

### `TypeKind`

```
export enum TypeKind
```

What kind of type a Type is. Opaque is a name not resolved yet, such as an
item that a later gen line makes.

Variants:

- `Struct`: A named struct.
- `Enum`: A named enum.
- `Error`: An error type.
- `Interface`: An interface.
- `Record`: A record type, such as (x: int, y: int).
- `Scalar`: A built-in scalar type, such as int or bool.
- `Ref`: A reference, &T or mut T.
- `Slice`: A slice, []T or mut []T.
- `Array`: A fixed-length array, [n]T.
- `Optional`: An optional, ?T.
- `Fallible`: A fallible type, E!T.
- `Fn`: A function type.
- `RawPtr`: A raw pointer, *T.
- `Opaque`: A name not resolved yet, such as an item a later gen line makes.

### `Utf8Error`

```
export error Utf8Error
```

The bytes are not well-formed UTF-8.

### `Variant`

```
export struct Variant
```

A variant of an enum.

### `Variant.Fields`

```
export Variant.Fields(&self): []Field
```

The variant's payload fields, in order. Compile time only.

### `Variant.Name`

```
export Variant.Name(&self): str
```

The variant's name. Compile time only.

### `Words`

```
export view struct Words
```

An iterator over the whitespace-separated words of a text, made by
str.Words.

### `Words.Next`

```
export Words.Next(mut self): ?str from self.text
```

- Flows: The result borrows what self.text refers to; self is free after the call.

The next word, or none after the last. O(bytes skipped and taken); allocates
nothing.

Skips White_Space scalars, then takes the longest run of other scalars,
so a word is never empty. ASCII text is read 8 bytes at a time; other
bytes one scalar at a time.

### `WriteError`

```
export error WriteError
```

A write or flush failed.

### `Writer`

```
export view struct Writer
```

A destination for bytes: an optional inline buffer, and a table that
takes what the buffer cannot hold. Print and the Format methods write
to one. System.Stdout, File.Writer, Text.Writer and the network streams
hand them out; a Writer borrows what it writes to.

A write interrupted by a cancel or a deadline fails with WriteError;
whether it was a timeout is read from the scope (Scope.Expired). Over a
stream, every Writer method is prefix under a cancel: an unknown part
of the bytes went out, so close the stream.

### `Writer.Bytes`

```
export Writer.Bytes(mut self, b: []u8): WriteError!void
```

- Errors: WriteError

Writes b. Fails with the table's WriteError. O(b.Len()); allocates
nothing.

When b fits in the free part of the inline buffer, it is copied there:
one compare and one copy, no table call. When nothing is buffered and b
is larger than the buffer, b goes to the table whole, uncopied; every
write of a writer with no buffer (a process stream, a Text) takes this
path, one table call. Otherwise the buffer is filled up and handed to
the table, then the rest is buffered or, when it still does not fit,
handed over whole: at most two table calls. An empty b copies nothing
and makes no call.

A failed table write drops the bytes it was given, and the buffer is
empty afterwards, so a later write starts clean instead of failing again
on the same bytes.

### `Writer.Flush`

```
export Writer.Flush(mut self): WriteError!void
```

- Errors: WriteError

Writes out what is buffered, then calls the table's flush. Fails with
the table's WriteError; the buffered bytes are dropped then too. Flush a
Writer with a buffer before dropping it: a dropped Writer's buffered
bytes are lost. O(buffered bytes) plus one table write and the flush;
allocates nothing.

### `Writer.Str`

```
export Writer.Str(mut self, s: str): WriteError!void
```

- Errors: WriteError

Writes the bytes of s, as Bytes does. Fails with the table's WriteError.
O(s.Len()); allocates nothing.

### `f32.Bits`

```
export f32.Bits(self): u32
```

The IEEE 754 binary32 encoding of self, bit for bit, NaN payloads and the
sign of zero included, on every target and at compile time. O(1).

### `f32.FromBits`

```
export f32.FromBits(b: u32): f32
```

The f32 whose binary32 encoding is b, bit for bit: every u32 is one, a
signalling NaN's payload included, on every target and at compile time.
O(1).

### `f64.Bits`

```
export f64.Bits(self): u64
```

The IEEE 754 binary64 encoding of self, bit for bit, NaN payloads and the
sign of zero included, on every target and at compile time. O(1).

### `f64.FromBits`

```
export f64.FromBits(b: u64): f64
```

The f64 whose binary64 encoding is b, bit for bit: every u64 is one, a
signalling NaN's payload included, on every target and at compile time.
O(1).

### `str.AsBytes`

```
export str.AsBytes(self): []u8
```

- Flows: The result borrows from self.

The text's bytes, sharing its memory. O(1); allocates nothing.

### `str.Byte`

```
export str.Byte(self, i: int): u8
```

The byte at offset i; panics (code 2) outside 0..Len(). O(1).

### `str.EndsWith`

```
export str.EndsWith(self, suffix: str): bool
```

Whether the text ends with suffix; "" always does. O(suffix.Len());
allocates nothing. Above 16 bytes it compares with vectors, except on
x86_64-freestanding.

### `str.Find`

```
export str.Find(self, sub: str): ?int
```

The byte offset of the first match of sub, or none; "" matches at 0. O(n +
m) in the worst case; allocates nothing.

### `str.From`

```
export str.From(bytes: []u8): Utf8Error!str
```

- Flows: The result borrows from bytes.
- Errors: Utf8Error

The bytes as a str that shares their memory. Fails with Utf8Error unless
they are well-formed UTF-8: no overlong form, no surrogate, no value
above 10FFFF and no truncated sequence (Unicode section 3.9). O(n), 8
bytes or more per step on ASCII; allocates nothing. From 256 bytes it
validates with std.simd's vector validator, except on
x86_64-freestanding.

### `str.FromC`

```
export str.FromC(p: *u8): str
```

The NUL-terminated string at p, as a str sharing its memory. O(n); allocates
nothing.

A call needs an unsafe context: p points at a NUL-terminated string the
caller vouches for. Bytes before the NUL that are not UTF-8 panic (code
8). The check is str.From's, vectorized from 256 bytes.

### `str.Has`

```
export str.Has(self, sub: str): bool
```

Whether sub occurs in the text; "" always does. O(n + m) in the worst
case; allocates nothing.

### `str.Len`

```
export str.Len(self): int
```

The length in bytes. O(1).

### `str.Lines`

```
export str.Lines(self): Lines
```

- Flows: The result borrows from self.

An iterator over the lines, each without its \n or \r\n; a final newline
ends the last line without starting an empty one. O(1); allocates nothing.

### `str.ParseFloat`

```
export str.ParseFloat(self): ParseError!f64
```

- Errors: ParseError

Parses the text as an f64, correctly rounded. ParseError when the text is
not a number or the value overflows. O(n) for up to 19 significant digits,
more for the multiprecision fallback; allocates nothing.

The text is an optional + or -, then digits, digits., digits.digits or
.digits, then an optional exponent (e or E, an optional sign, digits).
It may instead be inf, infinity or nan in any case, the first two with
an optional sign. Nothing else parses: no whitespace, no _ and no hex. A
value too small for an f64 is a zero with the text's sign; a value too
large is ParseError.

### `str.ParseFloatPrefix`

```
export str.ParseFloatPrefix(self): (value: f64, used: int)
```

Parses the longest prefix of the text that is a float by
str.ParseFloat's grammar, and returns its value and how many bytes it
used. The value is ParseFloat's, correctly rounded, except that a value
too large is an infinity with the text's sign where ParseFloat fails; a
value too small is a zero with the text's sign. When no prefix is a
float, used is 0 and the value is NaN. A written inf and a value too
large both give an infinity; the prefix's text, self[0..used], tells
them apart. No whitespace is skipped. O(n) for up to 19 significant
digits, more for the multiprecision fallback; allocates nothing.

The prefix stops before the first byte that cannot extend it: "1.5e" and
"1.5e+" use 3 ("1.5"), "5." uses 2, "." and "+" use 0, "1_000" uses 1,
"infinit" uses 3 ("inf"), and "-nan" uses 0 (nan takes no sign).

### `str.ParseInt`

```
export str.ParseInt(self): ParseError!i64
```

- Errors: ParseError

Parses the text as a decimal i64. ParseError when the text is not one or the
value does not fit. O(n); allocates nothing.

The text is an optional + or -, then one or more ASCII decimal digits,
and nothing else: no whitespace, no _ and no prefix such as 0x. The value
must fit in an i64.

### `str.Replace`

```
export str.Replace(self, a: &Arena, old: str, new: str): OutOfMemory!str from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A copy of the text in a with every occurrence of old replaced by new,
matched as Text.PushReplaced matches; a text with no match is
still copied, since the result lives in a. OutOfMemory when the arena
cannot grow. O(n + old.Len()) search plus the copy; one allocation, of
max(4, n) bytes, when new is not longer than old.

### `str.Scalars`

```
export str.Scalars(self): Scalars
```

- Flows: The result borrows from self.

An iterator over the Unicode scalars. O(1); allocates nothing.

### `str.Split`

```
export str.Split(self, sep: str): Split from self, sep
```

- Flows: The result borrows from self and sep.

An iterator over the pieces between occurrences of sep, empty pieces kept.
An empty sep panics here (code 8). O(1); allocates nothing.

### `str.SplitOnce`

```
export str.SplitOnce(self, sep: str): ?(before: str, after: str) from self
```

- Flows: The result borrows from self.

The text before and after the first occurrence of sep, or none if absent.
An empty sep panics (code 8), as Split's does. Both views borrow self.
O(n + m) in the worst case for n bytes of text and m bytes of separator.
Allocates nothing.

### `str.StartsWith`

```
export str.StartsWith(self, prefix: str): bool
```

Whether the text starts with prefix; "" always does, and returns before any
compare. O(prefix.Len()); allocates nothing. Above 16 bytes it compares
with vectors, except on x86_64-freestanding.

### `str.ToLower`

```
export str.ToLower(self, a: &Arena): OutOfMemory!str from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A copy of the text in a with each scalar lowercased by Unicode's simple
one-to-one mapping, the same for every locale: "ÀÉ İ" gives "àé i". A
scalar maps to one scalar, whose encoding may be longer. For caseless
matching use std.text's FoldCase. OutOfMemory when the arena cannot grow.
O(n log k) for k case ranges, ASCII O(1) per byte; allocates about n
bytes, once in the common case.

### `str.ToUpper`

```
export str.ToUpper(self, a: &Arena): OutOfMemory!str from a
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A copy of the text in a with each scalar uppercased by Unicode's simple
one-to-one mapping, as ToLower: "Straße" gives "STRAßE", since Unicode's
simple mapping gives ß no uppercase. OutOfMemory when the arena cannot grow.
O(n log k) for k case ranges, ASCII O(1) per byte; allocates about n bytes,
as ToLower.

### `str.Trim`

```
export str.Trim(self): str
```

- Flows: The result borrows from self.

The text without its leading and trailing White_Space scalars, as a
sub-view. O(n); allocates nothing.

### `str.Words`

```
export str.Words(self): Words
```

- Flows: The result borrows from self.

An iterator over the words, split on White_Space. O(1); allocates nothing.

<!-- api:end -->
