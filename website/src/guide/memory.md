---
description: How Mk is memory safe without a garbage collector - arenas and the heap, plain, unique and linear values, borrowing and loans, from and into in signatures, and views.
---

# Memory and ownership

Mk has no garbage collector and no hidden allocation. Memory comes from an allocator you pass in. The compiler checks that no reference outlives what it points at, and that nothing is released twice or forgotten. Most code needs no annotations for this.

This page is the practical introduction. [The memory model](/reference/memory) states every rule, each with an accepted program, a rejected one and the fix.

## Arenas

Most allocation goes into an arena. An arena hands out memory quickly and frees all of it at once. `sys.arena` lives for the whole program. For work with a shorter life, such as one request, make your own arena and free it when the work ends.

```mk
// verdict: ok
Shout(a: &Arena, words: []str): OutOfMemory!List[str] from a {
    var out := List[str].New(a)
    for w in words {
        try out.Push(try w.ToUpper(a))
    }
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free() // every List, Map, Text and string made in it goes too
    words := ["arena", "memory"]
    loud := try Shout(&arena, words[..])
    for w in loud {
        try sys.Print("{w}\n", (w)) // prints ARENA, then MEMORY
    }
}
```

```text
ARENA
MEMORY
```

- `List`, `Map`, `Set` and `Text` live in the arena they were made with, and need no release of their own.
- `arena.Reset()` empties an arena for reuse, and `a.Sub()` makes a child arena for scratch work.
- Nothing made in an arena can be used after the arena is freed or reset. The compiler rejects it:

```mk
// verdict: reject E0503
export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap)
    names := List[str].New(&arena) // names borrows the arena
    arena.Free() // <- E0503
    try sys.Print("{n}\n", (n: names.Len()))
}
```

```text
freed.mk:4:5: E0503 move-while-borrowed: arena conflicts with a live shared loan held by names
    |     arena.Free() // <- E0503
    |     ^^^^^^^^^^^^
    loan: arena (shared) at freed.mk:3:28
    holder: names
    signature: Free(self) ends self
    explain: mk explain E0503
1 error
```

## The heap

Data that must outlive any one arena lives on the heap. `sys.heap.Alloc()` gives an allocator. The heap containers `HeapList`, `HeapMap`, `TextMap`, `HeapText` and `Box` take it at each call that allocates, and the compiler makes you `Free` each one exactly once:

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    var xs := HeapList[int].New(a)
    defer xs.Free(a) // released on every path out of Main
    try xs.Push(a, 4)
    try xs.Push(a, 7)
    try sys.Print("{n}\n", (n: xs.Len()))
}
```

```text
2
```

[Return heap-owned data that outlives an arena](/examples/cookbook#return-heap-owned-data-that-outlives-an-arena) builds a `HeapList` and hands it to the caller. Other allocators, such as fixed buffers, pools and limits, are in [`std.mem`](/reference/std/mem), and [allocators and arenas](/reference/std/#3-memory-allocators-and-arenas) explains how they fit together.

## Plain, unique and linear values

Every type has a kind, which says what happens when a value is used:

| Kind | On use | Examples |
|---|---|---|
| **Plain** | Copied | Numbers, `bool`, `str`, `&T`, `[]T`, structs of plain fields |
| **Unique** | Moved: the old name can no longer be used | `mut T`, `List[T]`, `Map[K, V]` |
| **Linear** | Moved, and must be consumed exactly once on every path | Files, lock guards, `HeapList` and the other heap containers |

A struct takes the strictest kind among its fields, so a struct holding a file is linear without saying so. Declare a type `linear` when it holds no linear field but must still be released, such as a handle:

```mk
// verdict: ok
linear struct Ticket {
    id: int
}

Ticket.Redeem(self): int { // takes the ticket by value, which consumes it
    Ticket(id) := self
    return id
}

Redeemed(): int {
    t := Ticket(id: 7)
    return t.Redeem()
}
```

Using a value after it moved is an error, and so is a path where a linear value is never consumed:

```mk
// verdict: reject E0401 E0402
linear struct Ticket {
    id: int
}

Ticket.Redeem(self): int {
    Ticket(id) := self
    return id
}

Twice(): int {
    t := Ticket(id: 7)
    n := t.Redeem()
    return n + t.Redeem() // <- E0401
}

Maybe(c: bool): int {
    t := Ticket(id: 7) // <- E0402
    if c {
        return t.Redeem()
    }
    return 0
}
```

`defer t.Free(a)` and `onerr t.Free(a)` are the usual ways to consume a linear value on every path.

## Borrowing

`&x` borrows `x` to read it, and `mut x` borrows it to write. A borrow is called a *loan*.

- `mut x` needs `x` to be `var`.
- A loan lasts until the last use of anything that holds it, not until the end of a block.
- While a shared loan (`&x`) is live, `x` cannot be written or moved.
- While an exclusive loan (`mut x`) is live, `x` cannot be used at all.

```mk
// verdict: ok
Bump(n: mut int) {
    *n += 1
}

Sum(xs: &[3]int): int {
    return xs[0] + xs[1] + xs[2]
}

Borrows(): int {
    var xs: [3]int = [1, 2, 3]
    total := Sum(&xs)  // a shared loan, ended when Sum returns
    Bump(mut xs[0])    // an exclusive loan of one element
    first := &xs[0]
    n := *first        // first's last use: its loan ends here
    xs[1] = 10         // so this write is allowed
    return total + n + xs[1]
}
```

```mk
// verdict: reject E0502
Conflict(): int {
    var xs: [3]int = [1, 2, 3]
    first := &xs[0]
    xs[1] = 10 // <- E0502
    return *first
}
```

All the elements of one array or slice count as one place, so writing `xs[1]` conflicts with a loan on `xs[0]`. Fields are separate places. To change elements one by one, iterate with `for x in mut xs`, or split the slice with `SplitAt`. [The conflict table](/reference/memory#r5-the-conflict-table) has the rules.

## Where results come from

When a function returns something that holds a loan, the caller must know what the loan is on. Usually the types tell: when only one parameter could have supplied the result, that is the answer. When several could, the signature names them with `from`. When a function stores a parameter inside another, it says so with `into`.

```mk
// verdict: ok
Longest(a: str, b: str): str from a, b {
    if a.Len() > b.Len() {
        return a
    }
    return b
}

struct Names {
    all: List[str]
}

Names.Add(mut self, name: str into self): OutOfMemory!void { // name is stored in self
    try self.all.Push(name)
}
```

Without `from a, b`, `Longest` does not compile: the compiler cannot tell which argument the result borrows.

```mk
// verdict: reject E0601
Longest(a: str, b: str): str { // <- E0601
    if a.Len() > b.Len() {
        return a
    }
    return b
}
```

The compiler checks each body against its signature, and checks each caller against the signature alone. So a change inside a function body never breaks its callers. There are no lifetime parameters: `from` and `into`, with `ends` for a few advanced cases, are what a signature says about borrowing. [Result sources](/reference/memory#r9-result-sources) has the rules.

## Views

A struct whose fields borrow, such as a `str` or a slice, holds those loans. A window that keeps `xs: []int` can hand out slices that point into the numbers: they need the numbers to stay alive, not the window.

```mk
// verdict: ok
struct Window {
    xs: []int
    pos: int
}

Window.Next(mut self): ?[]int {
    if self.pos + 2 > self.xs.Len() {
        return none
    }
    start := self.pos
    self.pos += 1
    return some(self.xs[start..start + 2]) // a view of xs, not of the window
}

Rises(xs: []int): int { // Rises([1, 3, 2, 5, 8][..]) is 3
    var n := 0
    for w in Window(xs: xs, pos: 0) {
        if w[1] > w[0] {
            n += 1
        }
    }
    return n
}
```

A type that keeps its memory behind a private pointer, as `List` and `Map` do, is declared `view struct`, `plain view struct` or `mut view struct`, so its users can see how it borrows ([`linear` and `view` structs](/reference/language/declarations#linear-and-view-structs)).

## What the compiler guarantees

A program without `unsafe` that compiles is meant to be free of three things: reads or writes of memory whose lifetime has ended, two conflicting accesses while a reference is usable, and data races. Separately, the compiler requires every linear value to be consumed exactly once on every path, and run-time checks cover what types cannot: bounds, overflow and division by zero.

The rules are tested, not formally proven: a program the compiler accepts that breaks these guarantees is a bug in the rules. `unsafe` code keeps them only if its author meets the duties the memory model lists. [What is guaranteed](/reference/memory#9-what-is-guaranteed) is the exact statement.

## Going further

The memory model covers the rest, each rule with accepted and rejected examples: per-field loans, reborrowing, temporaries, `Cell` for shared mutation, `ends` for arenas holding cycles, and [programs Rust accepts and Mk rejects](/reference/memory#7-programs-rust-accepts-and-mk-rejects-with-mechanical-rewrites), with mechanical rewrites.

## Next

[Concurrency](./concurrency).
