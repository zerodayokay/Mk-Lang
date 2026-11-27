---
description: Fixed-size arrays, slices that view them, growable lists and hash maps in arenas, and the other containers in std.coll and on the heap.
---

# Arrays, lists and maps

## Arrays and slices

`[N]T` is an array: `N` values stored inline and copied as a whole. `[]T` is a slice: a view of consecutive values that live elsewhere. `mut []T` is a slice you may write through. Indexing and slicing are bounds-checked in every build.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    primes := [2, 3, 5, 7, 11] // an array literal: [5]int
    var zeros: [4]int = [0; 4] // [value; N] repeats a value
    zeros[1] = 9
    head := primes[..2]        // a slice of the first two: []int
    tail := primes[2..]        // from index 2 to the end
    mid := primes[1..4]
    try sys.Print("{a} {b} {c} {d}\n", (a: head.Len(), b: tail[0], c: mid.Len(), d: zeros[1])) // prints 2 5 3 9
}
```

```text
2 5 3 9
```

- Ranges are half-open: `1..4` is indexes 1, 2 and 3. Either end may be left out.
- An array literal is an array, so pass `xs[..]` where a slice is expected.
- An index or slice out of bounds panics, in every build.

## Lists

`List[T]` is a growable list. Like most of the library's containers it lives in an arena, and is freed with it. `Push` can run out of memory, so it takes a `try`.

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    var xs := List[int].New(sys.arena)
    for i in 1..6 {
        try xs.Push(i * i)
    }
    xs.Reverse()
    xs.Sort(fn(a: &int, b: &int): bool { return *a < *b })
    last := xs.Pop().Must()
    try sys.Print("{n} items, last was {last}, first is {first}\n", (n: xs.Len(), last, first: xs[0]))
    // prints 4 items, last was 25, first is 1
}
```

```text
4 items, last was 25, first is 1
```

- `Pop` returns an optional, `none` on an empty list. `Must()` takes the value or panics.
- `Sort` takes a function that says whether `a` goes before `b`. A `fn` literal written in the call may read the caller's locals ([functions](./functions#function-literals)).
- Slices, arrays and lists share one set of operations, among them `Len`, `Get`, `Sort`, `SortStable`, `Search`, `Reverse`, `Swap`, `Has`, `Index`, `Chunks` and `SplitAt`.

`mk query members List` prints the methods of any type, with their signatures.

## Maps

`Map[K, V]` is a hash map in an arena. Keys need the `Hash` and `Eq` interfaces; numbers, `str` and other built-in types have them. `Get` returns an optional reference, and `Ensure` returns the value for a key, inserting a default first when the key is missing.

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    var counts := Map[str, int].New(sys.arena)
    for word in "the cat and the hat and the bat".Words() {
        n := try counts.Ensure(word, 0)
        *n += 1
    }
    try counts.Set("dog", 0)
    key := "the"
    if n := counts.Get(&key) {
        try sys.Print("{key}: {n}\n", (key, n: *n)) // prints the: 3
    }
    cat := "cat"
    try sys.Print("{k} keys, has cat: {c}\n", (k: counts.Len(), c: counts.Has(&cat))) // prints 6 keys, has cat: true
}
```

```text
the: 3
6 keys, has cat: true
```

`for (key, value) in counts.Iter() { ... }` visits every entry, in no fixed order. The record's fields are named `key` and `value`, and destructuring binds by name, so those are the names to use. To make your own struct a key, generate `Hash` and `Eq` for it with `gen derive.Hash(T)` and `gen derive.Eq(T)` ([generators](./compile-time#generators)).

## Other containers

| Container | Where | What it is |
|---|---|---|
| `Set[T]` | Prelude | A hash set in an arena |
| `Text` | Prelude | Growable UTF-8 text ([building text](./strings#building-text)) |
| `Deque`, `PriorityQueue`, `BTree`, `Bitset` | [`std.coll`](/reference/std/coll) | More containers, in an arena or on the heap |
| `HeapList`, `HeapMap`, `TextMap`, `HeapText`, `Box` | Prelude | Containers on the heap, for data that outlives any arena ([the heap](./memory#the-heap)) |
| `Chan[T]` | Prelude | A channel between tasks ([concurrency](./concurrency)) |

Arena containers need no release of their own: they go when their arena is freed. Heap containers are linear, so the compiler makes you `Free` each one exactly once. [Memory and ownership](./memory) explains the difference, and [slices and lists](/reference/std/core#slices-and-lists) lists every method.

## Iterating

A `for` loop visits each element:

- `for x in xs` copies each element, so it needs elements that can be copied (E0409).
- `for x in &xs` gives a shared reference to each one.
- `for x in mut xs` gives an exclusive reference, so you can change them in place.
- `for i, x in xs` adds the index.

There are no `.map`, `.filter` or `.collect` chains: write the loop. [Control flow](./control-flow#for) has every form of `for`.

## Next

[Control flow](./control-flow): `if`, `for`, `match` and `defer`.
