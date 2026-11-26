# std.coll

`std.coll` adds four containers to the prelude's `List`, `Map` and `Set`.

- `Deque`, a double-ended queue.
- `PriorityQueue`, which always gives its least element.
- `BTree`, a map that keeps its keys in order.
- `Bitset`, a fixed set of small integers.

Reach for it when a `List` or a `Map` does the job badly. That is when you push at the front, want "the next smallest", iterate in sorted order or over a range of keys, or keep one bit per flag.

Read the [library guide](README.md) first.

```mk
// verdict: parse
import std.coll
```

## Contents

- [The main pieces](#the-main-pieces)
- [Arena and heap forms](#arena-and-heap-forms)
- [Order](#order)
- [Freeing a heap container](#freeing-a-heap-container)
- [Linear elements](#linear-elements)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [a queue with two ends](#a-queue-with-two-ends), [the next job by due date](#the-next-job-by-due-date), [sorted keys and ranges](#sorted-keys-and-ranges), [a sieve in a bitset](#a-sieve-in-a-bitset), [a heap tree that owns its values](#a-heap-tree-that-owns-its-values)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Push and pop at both ends, index from the front | `Deque`: `Push`, `Pop`, `PushFront`, `PopFront`, `Get`, `GetMut` |
| Take the least element first | `PriorityQueue`: `Push`, `Pop`, `Peek` |
| A map in key order, with ranges | `BTree`: `Set`, `Replace`, `Get`, `GetMut`, `Has`, `Remove`, `Iter`, `Range` |
| One bit per small integer | `Bitset`: `Set`, `Remove`, `Has`, `Count` |
| The same, outliving any arena | `HeapDeque`, `HeapBTree` |
| Give a heap container back | `Free`, `FreeEmpty`, `FreeEach` |

Every container has `New` and `Len`. `Deque`, `PriorityQueue`, `BTree` and `Bitset` take an `&Arena`; `HeapDeque` and `HeapBTree` take an `&Alloc`.

## Arena and heap forms

`Deque`, `PriorityQueue`, `BTree` and `Bitset` live in an arena, as `List` does. They need no release: the arena frees them with everything else it holds. Each is a view that borrows its arena, so the arena is not reset or freed while the container is in use.

`HeapDeque` and `HeapBTree` are the heap forms, as `HeapList` is the heap form of `List`. They follow the library's [heap-container rules](README.md#3-memory-allocators-and-arenas). A call that grows the container or gives its storage back panics (`wrong allocator`, code 12) when the `a` it is given is not the allocator given to `New`. A push with room does not check `a`. Both are `Send` and `Share` when their elements are.

`Deque.GetMut` and `BTree.GetMut` borrow one element for writing, as `Slice.GetMut` does, and return `none` rather than panic for an index or key that is not there. `HeapDeque` has no `GetMut`.

## Order

- `PriorityQueue[T]` needs `T: Ord`. `Pop` and `Peek` give the least element by `Ord.Less`. Equal elements come out in no stated order.
- `BTree[K, V]` needs `K: Ord` and iterates in ascending key order. `Range(lo, hi)` runs from `lo` up to, not including, `hi`, and is empty unless `lo < hi`.
- In a `Deque`, `Get(0)` is the front and `Get(Len() - 1)` the back; `PushFront` adds before `Get(0)`. A `Deque` has no iterator: index it, `for i in 0..q.Len() { x := q.Get(i).Must() }`.
- A `Bitset` holds `bits` bits, numbered from 0, all clear at `New`. `Len` is that number and `Count` the number of set bits. An index outside `0..Len()` panics (`bounds`, code 2).

## Freeing a heap container

| Call | Use it when | What it does |
|---|---|---|
| `Free(a)` | The elements are not linear, or are `Free` | Frees every element with its own `Free`, then the storage. It never panics, except for the wrong allocator |
| `FreeEach(a, release)` | The elements are linear and not `Free`, such as files | Hands every element to `release`, a function literal that may capture locals, then frees the storage |
| `FreeEmpty(a)` | The container is already empty | Frees the storage; panics (`free non-empty`, code 11) if an element is left |

Calls that overwrite an entry of a `HeapBTree` free what they displace with the `a` they take, as `HeapMap` does:

- A `Set` that finds its key frees the old value, and the key it was given.
- A `Replace` frees the key it was given, and returns the old value.

## Linear elements

No call drops a linear value without saying so. Where a call would drop a key or a value and cannot free it, or would store it where its arena frees it, the element type must not be linear. A call that breaks this is E0701 at the call:

- `HeapDeque.Free` and `HeapBTree.Free` need elements that are not linear or are `Free`. The diagnostic names `FreeEach` and `FreeEmpty`.
- `HeapBTree.Set` needs the same of `K` and `V`, and `HeapBTree.Replace` of `K`. Use `Replace` to get the old value back instead.
- An arena container frees what it holds with its arena, unconsumed, so it holds no linear value: `Deque.Push`, `Deque.PushFront`, `PriorityQueue.Push` and the arena `BTree`'s `Set` and `Replace` refuse a linear element, key or value. The diagnostic names `HeapDeque` or `HeapBTree`, which hold one.
- `Remove` by key, on either tree, drops the stored key, so `K` may not be linear.

```mk
// verdict: reject E0701
// Free cannot close files, so a deque of them is given back with FreeEach
import std.coll
import std.fs

Leak(a: &Alloc, files: coll.HeapDeque[fs.File]) {
    files.Free(a) // <- E0701
}
```

```mk
// verdict: ok
import std.coll
import std.fs

CloseAll(a: &Alloc, files: coll.HeapDeque[fs.File]) {
    files.FreeEach(a, fn(f: fs.File) { f.Abandon() })
}
```

The [memory guide](../memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path) has the rule for linear values.

## Memory and cost

| Operation | Cost | Allocation |
|---|---|---|
| `Deque` and `HeapDeque`: push, pop, `Get` | O(1); a push amortized | A full deque doubles its buffer |
| `PriorityQueue`: `Push`, `Pop` | O(log n) compares | `Push`: a full queue grows its array; `Pop`: none |
| `PriorityQueue.Peek` | O(1) | None |
| `BTree` and `HeapBTree`: `Get`, `Set`, `Remove` | O(log n) | At most one node per level, on `Set` |
| `Iter`, `Range` | O(log n) to start, O(1) amortized a step | None |
| `Bitset`: `Set`, `Remove`, `Has` | O(1) | None |
| `Bitset.Count` | O(Len() / 64) | None |

`New` allocates nothing for any container but `Bitset`, which allocates its words at once. A growing arena container leaves its old buffer to the arena, as `List` does. A node that `Remove` empties is kept and reused by a later `Set`, so a tree that keeps changing does not grow its arena without bound.

A push or `Set` grows the container before it takes the element, so one that fails with `OutOfMemory` leaves the element with you and changes nothing.

## Examples

Each program below runs as written with `mk run`.

### A queue with two ends

```mk
// verdict: ok
import std.coll

export Main(sys: &System): OutOfMemory|WriteError!void {
    var q := coll.Deque[int].New(sys.arena)
    try q.Push(2) // back
    try q.Push(3)
    try q.PushFront(1) // front
    try sys.Print("len {n}, first {f}\n", (n: q.Len(), f: *q.Get(0).Must()))
    if x := q.GetMut(2) {
        *x = 30 // change the back value in place
    }
    for x := q.PopFront() {
        try sys.Print("{x}\n", (x))
    }
}
```

```text
$ mk run deque.mk
len 3, first 1
1
2
30
```

- `Push` and `Pop` work at the back, as on `List`; `PushFront` and `PopFront` at the front.
- `for x := q.PopFront()` runs until `PopFront` returns `none`.

### The next job by due date

```mk
// verdict: ok
import std.coll

struct Job {
    due: int
    name: str
}

impl Eq for Job {
    Eq(&self, other: &Job): bool {
        return self.due == other.due
    }
}

impl Ord for Job {
    Less(&self, other: &Job): bool {
        return self.due < other.due // the earliest due date comes out first
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var q := coll.PriorityQueue[Job].New(sys.arena)
    try q.Push(Job(due: 30, name: "deploy"))
    try q.Push(Job(due: 10, name: "build"))
    try q.Push(Job(due: 20, name: "test"))
    if next := q.Peek() {
        try sys.Print("next: {name}\n", (name: next.name))
    }
    for job := q.Pop() {
        try sys.Print("{due} {name}\n", (due: job.due, name: job.name))
    }
}
```

```text
$ mk run jobs.mk
next: build
10 build
20 test
30 deploy
```

- The queue's order is the element's `Ord`. For the largest first, reverse the comparison in `Less`.
- `Peek` leaves the element in place; `Pop` removes it.

### Sorted keys and ranges

```mk
// verdict: ok
import std.coll

export Main(sys: &System): OutOfMemory|WriteError!void {
    var scores := coll.BTree[str, int].New(sys.arena)
    for p in [(name: "mia", n: 7), (name: "ben", n: 4), (name: "zoe", n: 9), (name: "ada", n: 5)] {
        try scores.Set(p.name, p.n)
    }
    try scores.Set("ben", 12) // a present key gets the new value
    var all := scores.Iter()
    for e := all.Next() { // in key order
        try sys.Print("{k} = {v}\n", (k: *e.key, v: *e.value))
    }
    var mid := scores.Range(&"b", &"n") // from "b" up to, not including, "n"
    for e := mid.Next() {
        try sys.Print("in b..n: {k}\n", (k: *e.key))
    }
    _ = scores.Remove(&"zoe")
    try sys.Print("{n} left, has zoe: {z}\n", (n: scores.Len(), z: scores.Has(&"zoe")))
}
```

```text
$ mk run scores.mk
ada = 5
ben = 12
mia = 7
zoe = 9
in b..n: ben
in b..n: mia
3 left, has zoe: false
```

- `Next` returns each entry as `(key: &K, value: &V)`, borrowed from the tree.
- `Remove` returns the removed value, or `none` when the key is absent.

### A sieve in a bitset

```mk
// verdict: ok
import std.coll

// Primes below n by the sieve of Eratosthenes: bit i set means i is composite.
export Main(sys: &System): OutOfMemory|WriteError!void {
    n := 50
    var composite := try coll.Bitset.New(sys.arena, n) // all clear
    var sep := ""
    for i in 2..n {
        if composite.Has(i) {
            continue
        }
        try sys.Print("{sep}{i}", (sep, i))
        sep = " "
        var j := i * i
        for j < n {
            composite.Set(j)
            j += i
        }
    }
    try sys.Print("\n{c} of {n} bits set\n", (c: composite.Count(), n: composite.Len()))
}
```

```text
$ mk run sieve.mk
2 3 5 7 11 13 17 19 23 29 31 37 41 43 47
33 of 50 bits set
```

- The size is fixed at `New`. `Set(50)` here would panic (`bounds`, code 2).

### A heap tree that owns its values

```mk
// verdict: ok
import std.coll

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    var names := coll.HeapBTree[int, HeapText].New(a)
    defer names.Free(a) // frees every HeapText, then the tree's nodes
    users := [(id: 30, name: "kim"), (id: 10, name: "ada"), (id: 20, name: "ben")]
    for u in users {
        var t := HeapText.New(a)
        onerr t.Free(a)
        try t.Push(a, u.name)
        try names.Set(a, u.id, t) // the tree owns t from here
    }
    var it := names.Iter()
    for e := it.Next() {
        try sys.Print("{k}: {v}\n", (k: *e.key, v: e.value.Str()))
    }
}
```

```text
$ mk run names.mk
10: ada
20: ben
30: kim
```

- `HeapText` is `Free`, so `names.Free(a)` frees each value before the nodes.
- A `Set` that found its key would free the old `HeapText` with `a`.

## API

<!-- api:begin -->
Stability: Stable.

### `BTree`

```
export view struct BTree[K: Ord, V]
```

An ordered map in an arena: Get, Set and Remove in O(log n), and
iteration in key order. K needs Ord. Its nodes live in the arena and
are freed with it; a node emptied by Remove is reused by a later Set,
so a tree that keeps changing does not grow the arena without bound.
The arena frees the entries unconsumed, so the tree holds no linear key
or value: every call that stores one is E0701. A HeapBTree holds them.

### `BTree.Get`

```
export BTree[K, V].Get(&self, k: &K): ?&V
```

- Flows: The result borrows from self.

The value of k; none when k is absent. O(log n).

### `BTree.GetMut`

```
export BTree[K, V].GetMut(mut self, k: &K): ?mut V
```

- Flows: The result borrows from self.

The value of k, to update in place. O(log n), one descent.

### `BTree.Has`

```
export BTree[K, V].Has(&self, k: &K): bool
```

Whether k is present. O(log n).

### `BTree.Iter`

```
export BTree[K, V].Iter(&self): BTreeIter[K, V]
```

- Flows: The result borrows from self.

Every entry in ascending key order. O(log n) to start, O(1) amortized
a step.

### `BTree.Len`

```
export BTree[K, V].Len(&self): int
```

The number of entries. O(1).

### `BTree.New`

```
export BTree[K, V].New(a: &Arena): BTree[K, V]
```

- Flows: The result borrows from a.

An empty BTree in a. O(1); allocates nothing until the first Set.

### `BTree.Range`

```
export BTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self
```

- Flows: The result borrows from self.

The entries with lo <= key < hi in ascending order: from lo up to, not
including, hi. Empty unless lo < hi. O(log n) to start, then O(1)
amortized a step.

### `BTree.Remove`

```
export BTree[K, V].Remove(mut self, k: &K): ?V
```

- Flows: The result borrows what self refers to; self is free after the call.

The value of k, removed; none when k is absent. The stored key is
dropped, so K may not be linear (E0701 at the call). O(log n); it
allocates nothing, and a node it empties is kept for the next split.

### `BTree.Replace`

```
export BTree[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
```

- Flows: The result borrows from v. The result borrows what self refers to; self is free after the call.
- Errors: OutOfMemory

Stores v under k and returns the old value, or none when k was absent.
A present key keeps the stored key and drops the given k, so K may not
be linear (E0701 at the call); nor may V, since the arena would free the
stored v unconsumed (E0701, naming HeapBTree). Fails with OutOfMemory as
Set does, and then k and v are not consumed. O(log n) nodes and
O(B log n) compares; allocates at most one node per level, and nothing
for a present key.

### `BTree.Set`

```
export BTree[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
```

- Errors: OutOfMemory

Stores v under k. A present key keeps the stored key and gets v; the
given key and the old value are dropped, so neither K nor V may be
linear (E0701 at the call, which names HeapBTree). Fails with
OutOfMemory when a node split needs a node the arena cannot give; the
nodes are allocated before anything moves, so k and v are then not
consumed. O(log n) nodes and O(B log n) compares, where B, from 6 to
16, depends on the sizes of K and V; allocates at most one node per
level.

### `BTreeIter`

```
export view struct BTreeIter[K, V]
```

The entries of a BTree or HeapBTree in ascending key order, made by
Iter and Range.

### `BTreeIter.Next`

```
export BTreeIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.tree
```

- Flows: The result borrows what self.tree refers to; self is free after the call.

The next entry, or none after the last. O(1) amortized.

### `Bitset`

```
export view struct Bitset
```

A set of small integers: a fixed number of bits, numbered from 0 to
Len() - 1 and all clear at New, in 64-bit words in an arena. Set,
Remove and Has are O(1); Count is O(Len() / 64).

### `Bitset.Count`

```
export Bitset.Count(&self): int
```

The number of set bits, counted a word at a time. O(Len() / 64);
allocates nothing.

### `Bitset.Has`

```
export Bitset.Has(&self, i: int): bool
```

Whether bit i is set; panics (code 2) unless 0 <= i < Len(). O(1).

### `Bitset.Len`

```
export Bitset.Len(&self): int
```

The number of bits, set or not, fixed at New. O(1).

### `Bitset.New`

```
export Bitset.New(a: &Arena, bits: int): OutOfMemory!Bitset
```

- Flows: The result borrows from a.
- Errors: OutOfMemory

A Bitset of bits bits, all clear. Fails with OutOfMemory when a cannot
give the ceil(bits / 64) words; panics (code 2) when bits < 0.
O(bits / 64); allocates the words, zero-filled, from a.

### `Bitset.Remove`

```
export Bitset.Remove(mut self, i: int)
```

Clears bit i; panics (code 2) unless 0 <= i < Len(). O(1).

### `Bitset.Set`

```
export Bitset.Set(mut self, i: int)
```

Sets bit i; panics (code 2) unless 0 <= i < Len(). O(1).

### `Deque`

```
export view struct Deque[T]
```

A double-ended queue in an arena: push and pop at either end, and
index from the front, each O(1) (a push amortized). Its storage lives
in the arena and is freed with it, its elements unconsumed, so it holds
no linear value: Push and PushFront of one are E0701. A HeapDeque holds
one.

### `Deque.Get`

```
export Deque[T].Get(&self, i: int): ?&T
```

- Flows: The result borrows from self.

The i-th value from the front; none unless 0 <= i < Len(). O(1).

### `Deque.GetMut`

```
export Deque[T].GetMut(mut self, i: int): ?mut T
```

- Flows: The result borrows from self.

The i-th value from the front, writable; none unless 0 <= i < Len().
It never panics, as Slice.GetMut. O(1).

### `Deque.Len`

```
export Deque[T].Len(&self): int
```

The number of values. O(1).

### `Deque.New`

```
export Deque[T].New(a: &Arena): Deque[T]
```

- Flows: The result borrows from a.

An empty Deque in a. O(1); allocates nothing until the first push.

### `Deque.Pop`

```
export Deque[T].Pop(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

The back value, removed; none when empty. O(1).

### `Deque.PopFront`

```
export Deque[T].PopFront(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

The front value, removed; none when empty. O(1).

### `Deque.Push`

```
export Deque[T].Push(mut self, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Appends x at the back. Fails with OutOfMemory when the deque is full
and the arena cannot double its capacity; the deque grows before it
takes x, so x is consumed only on success. Amortized O(1); a growth
allocates twice the capacity from the arena and leaves the old buffer
to it. T may not be linear (E0701 at the call, which names HeapDeque).

### `Deque.PushFront`

```
export Deque[T].PushFront(mut self, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Prepends x at the front, as Push; T may not be linear. Amortized O(1).

### `HeapBTree`

```
export linear view struct HeapBTree[K: Ord, V]
```

An ordered map on an allocator, with the operations of BTree. It is
linear: give it back with Free, FreeEmpty or FreeEach. It is Send and
Share when K and V are.

### `HeapBTree.FreeEach`

```
export HeapBTree[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))
```

Hands every key and value to release, in key order, then frees every
node. Use it for keys or values that are linear and not Free. Panics
(code 12) when a is not the allocator given to New. release may
capture locals and must consume its arguments. O(n) calls of release.

### `HeapBTree.FreeEmpty`

```
export HeapBTree[K, V].FreeEmpty(self, a: &Alloc)
```

Frees the nodes of an empty tree. Panics (code 11) when it holds an
entry, and (code 12) when a is not the allocator given to New.
O(spare nodes).

### `HeapBTree.Get`

```
export HeapBTree[K, V].Get(&self, k: &K): ?&V
```

- Flows: The result borrows from self.

The value of k; none when k is absent. O(log n).

### `HeapBTree.GetMut`

```
export HeapBTree[K, V].GetMut(mut self, k: &K): ?mut V
```

- Flows: The result borrows from self.

The value of k, to update in place. O(log n), one descent.

### `HeapBTree.Has`

```
export HeapBTree[K, V].Has(&self, k: &K): bool
```

Whether k is present. O(log n).

### `HeapBTree.Iter`

```
export HeapBTree[K, V].Iter(&self): BTreeIter[K, V]
```

- Flows: The result borrows from self.

Every entry in ascending key order, as BTree.Iter.

### `HeapBTree.Len`

```
export HeapBTree[K, V].Len(&self): int
```

The number of entries. O(1).

### `HeapBTree.New`

```
export HeapBTree[K, V].New(a: &Alloc): HeapBTree[K, V]
```

- Flows: The result borrows from a.

An empty HeapBTree on a. O(1); allocates nothing until the first Set.

### `HeapBTree.Range`

```
export HeapBTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self
```

- Flows: The result borrows from self.

The entries with lo <= key < hi in ascending order, as BTree.Range.

### `HeapBTree.Remove`

```
export HeapBTree[K, V].Remove(mut self, k: &K): ?V
```

- Flows: The result borrows what self refers to; self is free after the call.

The value of k, removed, as BTree.Remove: K may not be linear (E0701
at the call). It takes no allocator, so a node it empties is kept for
the next split or for Free. O(log n).

### `HeapBTree.Replace`

```
export HeapBTree[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V
```

- Flows: The result borrows from v. The result borrows what self refers to; self is free after the call.
- Errors: OutOfMemory

Stores v under k and returns the old value, or none when k was absent,
as BTree.Replace, except that for a present key the given key is freed
with a. So K must be not linear or Free (E0701 at the call); V may be
anything. Panics (code 12) when a is not the allocator given to New;
it checks only when it allocates a node. Fails with OutOfMemory as Set
does, and then k and v are not consumed. O(log n); allocates at most
one node per level.

### `HeapBTree.Set`

```
export HeapBTree[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void
```

- Errors: OutOfMemory

Stores v under k, as BTree.Set, except that for a present key the old
value and the given key are freed with a, as HeapMap.Set does. So each
of K and V must be not linear or Free (E0701 at the call, which names
Replace). Panics (code 12) when a is not the allocator given to New;
it checks only when it allocates a node. O(log n); allocates at most
one node per level.

### `HeapDeque`

```
export linear view struct HeapDeque[T]
```

A double-ended queue on an allocator, with the operations of Deque
except GetMut.
It is linear: give it back with Free, FreeEmpty or FreeEach. It is
Send and Share when T is.

### `HeapDeque.FreeEach`

```
export HeapDeque[T].FreeEach(self, a: &Alloc, const release: fn(T))
```

Hands every element to release, front to back, then frees the buffer.
Use it for elements that are linear and not Free. Panics (code 12) when
a is not the allocator given to New. release may capture locals and
must consume its argument. O(n) calls of release.

### `HeapDeque.FreeEmpty`

```
export HeapDeque[T].FreeEmpty(self, a: &Alloc)
```

Frees the buffer of an empty deque. Panics (code 11) when it holds an
element, and (code 12) when a is not the allocator given to New. O(1).

### `HeapDeque.Get`

```
export HeapDeque[T].Get(&self, i: int): ?&T
```

- Flows: The result borrows from self.

The i-th value from the front; none unless 0 <= i < Len(). O(1).

### `HeapDeque.Len`

```
export HeapDeque[T].Len(&self): int
```

The number of values. O(1).

### `HeapDeque.New`

```
export HeapDeque[T].New(a: &Alloc): HeapDeque[T]
```

- Flows: The result borrows from a.

An empty HeapDeque on a. O(1); allocates nothing until the first push.

### `HeapDeque.Pop`

```
export HeapDeque[T].Pop(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

The back value, removed; none when empty. O(1).

### `HeapDeque.PopFront`

```
export HeapDeque[T].PopFront(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

The front value, removed; none when empty. O(1).

### `HeapDeque.Push`

```
export HeapDeque[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Appends x at the back, as Deque.Push, growing with a. A growth frees
the old buffer, and panics (code 12) when a is not the allocator given
to New; a push with room does not look at a. Amortized O(1).

### `HeapDeque.PushFront`

```
export HeapDeque[T].PushFront(mut self, a: &Alloc, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Prepends x at the front, as Push. Amortized O(1).

### `PriorityQueue`

```
export view struct PriorityQueue[T: Ord]
```

A priority queue in an arena: Pop and Peek give the least element by
T's Ord. Push and Pop are O(log n), Peek O(1). Equal elements come out
in no stated order. Its storage is freed with the arena, its elements
unconsumed, so it holds no linear value: Push of one is E0701.

### `PriorityQueue.Len`

```
export PriorityQueue[T].Len(&self): int
```

The number of elements. O(1).

### `PriorityQueue.New`

```
export PriorityQueue[T].New(a: &Arena): PriorityQueue[T]
```

- Flows: The result borrows from a.

An empty queue in a. O(1); allocates nothing until the first Push.

### `PriorityQueue.Peek`

```
export PriorityQueue[T].Peek(&self): ?&T
```

- Flows: The result borrows from self.

The least element, left in place; none when empty. O(1).

### `PriorityQueue.Pop`

```
export PriorityQueue[T].Pop(mut self): ?T
```

- Flows: The result borrows what self refers to; self is free after the call.

The least element, removed; none when empty. O(log n) compares and
moves; allocates nothing.

### `PriorityQueue.Push`

```
export PriorityQueue[T].Push(mut self, x: T into self): OutOfMemory!void
```

- Errors: OutOfMemory

Adds x. Fails with OutOfMemory when the queue is full and the arena
cannot grow it; the queue grows before it takes x, so x is consumed
only on success. O(log n) compares, amortized; a growth allocates from
the arena and leaves the old array to it. T may not be linear (E0701 at
the call).

<!-- api:end -->
