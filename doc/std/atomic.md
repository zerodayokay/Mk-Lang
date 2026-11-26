# std.atomic

`std.atomic` has atomic words that tasks on any carrier read and change without a lock. `AtomicInt` and `AtomicBool` are for counters and flags, and `Atomic[T]`, with an explicit memory order, for lock-free code. `Wait` and `Wake` sleep until a word changes, and `Spin` is a lock that never suspends. Reach for it when a counter, a flag or a single word is all that tasks share. For a value larger than a word, use the prelude's `Mutex` or `Chan`, or [`std.sync`](sync.md).

Read the [standard library guide](README.md) first, and the [memory model's rule on tasks](../memory.md#r17-tasks) for what may cross between them.

```mk
// verdict: parse
import std.atomic
```

## Contents

- [The main pieces](#the-main-pieces)
- [Simple atomics and ordered atomics](#simple-atomics-and-ordered-atomics)
- [Memory orders](#memory-orders)
- [Read-modify-write](#read-modify-write)
- [Waiting for a word to change](#waiting-for-a-word-to-change)
- [Spin](#spin)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [count across tasks](#count-across-tasks), [publish data to waiting tasks](#publish-data-to-waiting-tasks), [a compare-exchange loop](#a-compare-exchange-loop), [a spin lock](#a-spin-lock), [pass an order on](#pass-an-order-on)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| A counter shared by tasks | `AtomicInt`: `New`, `Add`, `Load`, `Store` |
| A flag shared by tasks | `AtomicBool`: `New`, `Load`, `Store`, `Swap` |
| A word of any integer width, or a raw pointer, with a chosen order | `Atomic[T]`: `New`, `Load`, `Store`, `Swap` |
| Update a word from its old value | `Atomic[T].CompareExchange`, `CompareExchangeWeak` |
| Add or combine bits, wrapping | `Atomic[T].FetchAdd`, `FetchSub`, `FetchAnd`, `FetchOr`, `FetchXor` |
| Name a memory order | `Relaxed`, `Acquire`, `Release`, `AcqRel`, `SeqCst`, `Unordered` |
| Order memory without touching a word | `Fence` |
| Sleep until a word changes | `Atomic[T].Wait`, `Atomic[T].Wake` |
| A lock for a few steps, with no `Io` | `Spin[T]`: `New`, `Lock`, `Into`; `SpinGuard[T]`: `Ref`, `Mut`, `Store`, `Unlock` |
| Tell the CPU a loop is spinning | `Pause` |

Only `Wait` takes an `Io`. Everything else works without one, so a kernel or a freestanding program can use it.

## Simple atomics and ordered atomics

`AtomicInt` holds an `i64` and `AtomicBool` a `bool`. Every operation on them is sequentially consistent (`SeqCst`), so you never choose an order. A task that reads a value another task stored also sees every write that task made before the store. `AtomicInt.Add` returns the new value and panics on overflow.

`Atomic[T]` is one atomic word of `T`, where `T` is an integer of 1, 2, 4 or 8 bytes, `int`, `uint` or a raw pointer. Any other `T` is E0701. Each operation is one atomic instruction, and takes its memory order as a `const` argument, so the order is fixed where you write it and costs nothing at run time.

All three types are unique, `Send` and `Share`, so tasks share them through a `&`. An `Atomic[T]` is `Send` and `Share` whatever its `T`: reading through a pointer it holds is for `unsafe` code to justify. Their `New`, and `Spin`'s, allocates nothing, so a `var` global may be initialized with one.

## Memory orders

An order says how an atomic operation orders the other memory accesses around it. These are C++20's memory orders, plus `Unordered`.

| Order | Meaning |
|---|---|
| `Relaxed` | Atomic, and the word's writes fall in one order every task agrees on; no ordering edge. |
| `Acquire` | A load or read-modify-write that sees a `Release` write also sees every write before it. |
| `Release` | A store or read-modify-write that makes every write before it visible to an `Acquire` reader of its value. |
| `AcqRel` | Both, for a read-modify-write or a fence. |
| `SeqCst` | Both, and the `SeqCst` operations of a program fall in one total order. |
| `Unordered` | No tearing and no ordering, for a load or a store only. |

`Unordered` promises less than `Relaxed`: a load sees some value a store wrote, never a torn one, but the word's writes need not fall in one order that every task agrees on. It compiles to the same instruction as `Relaxed`. The [memory model's `unsafe` rules](../memory.md#r19-unsafe) say what `unsafe` code may assume of it.

`Order` has no other values: its field is private, so no module but `std.atomic` can make one. The pattern to publish data is: write it, then store a flag with `Release`. A reader that loads the flag with `Acquire` and sees the value then sees the data.

An order that does not apply to an operation is E0706 at the call:

- `Fence(Relaxed)` orders nothing, and `Fence` refuses `Unordered`.
- A `Load` cannot be `Release` or `AcqRel`, and a `Store` cannot be `Acquire` or `AcqRel`.
- A read-modify-write cannot be `Unordered`.
- The `failure` order of a compare-exchange is a load's, so it cannot be `Release` or `AcqRel`. It may be stronger than `success`.

```mk
// verdict: reject E0706
// an order that does not apply is rejected where it is written
import std.atomic

Publish() {
    atomic.Fence(atomic.Release)
    atomic.Fence(atomic.Relaxed) // <- E0706
}
```

`Fence(o)` orders memory without touching a word: an `Acquire` fence after a `Relaxed` load makes that load an `Acquire`, and a `Release` fence before a `Relaxed` store makes that store a `Release`.

The rules are the methods `o.CanLoad()` (a load, and a `failure` order), `o.CanStore()`, `o.CanFence()` and `o.CanModify()` (every read-modify-write), which each operation states as a `requires` clause. A function that passes a `const` order on states the same clause, as for any [clause of a generic](../reference.md#10-generics-and-interfaces): see [Pass an order on](#pass-an-order-on).

## Read-modify-write

- `Swap` stores a value and returns the old one, in one step.
- `CompareExchange(old, new, success, failure)` stores `new` if the word holds `old`. It returns `ok` and the value the word held, and `ok` is true exactly when that value was `old`. A retry loop needs no second load.
- `CompareExchangeWeak` may fail with the value equal to `old`, for a loop that retries anyway. On x86-64 it is the same instruction as `CompareExchange`.
- `FetchAdd` to `FetchXor` take an integer `T` (E0701 for a pointer), wrap, and return the old value. `AtomicInt.Add` is the checked one.
- `AtomicBool.Swap(true)` on a false flag returns `false` to exactly one of any number of tasks: a one-shot flag.

## Waiting for a word to change

`Wait(io, expected)` parks the task while the word holds `expected`, until a `Wake`. It may return early for no reason, so read the word again in a loop. The check of the word and the park are one step as far as `Wake` is concerned, so a `Wake` after the word changed is never lost.

- `Wake(all)` wakes one waiter, or all of them. It needs no `Io` and may be called from any thread, a foreign one included. It also wakes tasks waiting under a simulated `Io` from `std.sim`, or under a foreign `Io` from `std.runtime`. It takes a sequentially consistent fence after waking native waiters and before it wakes waiters on other `Io`s.
- Both take a `T` of 4 bytes (`requires SizeOf[T]() == 4`, else E0706), since the OS compares the word's bits.
- `Wait` fails with `Canceled` when its scope is cancelled. Called off a task, it panics with code 16.

std's own locks wait this way. Prefer `Mutex` or `std.sync` unless you are building a primitive of your own.

## Spin

`Spin[T]` is a lock around a value of `T` that never suspends a task and needs no `Io`. Use it in a kernel, in a section that runs with interrupts off, or around a critical section of a few steps. `Lock` spins until the lock is free and returns a `SpinGuard`, which borrows the `Spin` and is linear: every path ends it with `Unlock`. The next holder sees every write made under the lock. `Spin[T]` is `Send` and `Share` when `T` is `Send`, as `Mutex[T]` is.

- `Lock` never fails and is not fair: a waiter may be passed over.
- A waiter reads the lock word until it is free, then tries to take it. After each failed take it pauses, twice as long each time, up to 1,024 pause hints. From its 64th look at the word, it yields the OS thread before each look instead, through the yield hook `MkThreadYield`.
- `MkThreadYield` needs no import. A hosted program gets the OS's yield. A freestanding program only pauses, unless it exports its own `MkThreadYield`.
- A carrier runs one task until that task waits, and `Lock` never waits. With one carrier (`MK_CARRIERS=1`), a task that spins on a lock another task holds never lets that task run, so hold a `Spin` only across code that does not yield.
- `SpinGuard.Store` drops the old value, so for a linear `T` it is E0701 at the call; `Replace(g.Mut(), x)` gives the old value back instead. `Spin.Into` takes the value out of a `Spin` that no guard borrows.

`Pause` is the spin-wait hint (`pause` on x86_64): it saves power and lets a sibling hardware thread run, and changes nothing a program can observe. Call it in a loop of your own that spins on a word.

## Memory and cost

Nothing in `std.atomic` allocates. Every operation is O(1), except that `Wait` and `Spin.Lock` also wait for as long as they must. `AtomicInt.Add`, `FetchAdd` and `FetchSub` are wait-free: one instruction however many tasks add at once. On x86-64 a `FetchAnd`, `FetchOr` or `FetchXor` whose result is used is a compare-exchange loop and not wait-free.

## Examples

Each program with a `Main` below runs as written with `mk run`. In the last example, `mk check` accepts the first block and rejects the second with E0706.

### Count across tasks

```mk
// verdict: ok
import std.atomic

Work(hits: &atomic.AtomicInt, first: &atomic.AtomicBool, winners: &atomic.AtomicInt) {
    if !first.Swap(true) { // exactly one task gets false back
        _ = winners.Add(1)
    }
    for _ in 0..1000 {
        _ = hits.Add(1) // returns the new value; panics on overflow
    }
}

export Main(sys: &System): WriteError!void {
    hits := atomic.AtomicInt.New(0)
    first := atomic.AtomicBool.New(false)
    winners := atomic.AtomicInt.New(0)
    scope s := sys.io.Scope() {
        for _ in 0..8 {
            s.go Work(&hits, &first, &winners)
        }
    } // the scope waits for its tasks
    try sys.Print("{h} hits, {w} first\n", (h: hits.Load(), w: winners.Load()))
}
```

```text
$ mk run counter.mk
8000 hits, 1 first
```

- The tasks borrow the atomics from `Main`; the `scope` block waits for every task before those loans end.

### Publish data to waiting tasks

```mk
// verdict: ok
import std.atomic

// Reader waits until ready is 1, then reads the data the writer published.
Reader(io: Io, ready: &atomic.Atomic[u32], data: &atomic.Atomic[i64], sum: &atomic.Atomic[i64]) {
    for ready.Load(atomic.Acquire) == 0 { // Wait may return early: look again
        ready.Wait(io, 0) else {
            return // the scope was cancelled
        }
    }
    _ = sum.FetchAdd(data.Load(atomic.Relaxed), atomic.Relaxed)
}

export Main(sys: &System): WriteError!void {
    ready := atomic.Atomic[u32].New(0) // Wait and Wake take a 4-byte word
    data := atomic.Atomic[i64].New(0)
    sum := atomic.Atomic[i64].New(0)
    scope s := sys.io.Scope() {
        for _ in 0..4 {
            s.go Reader(sys.io, &ready, &data, &sum)
        }
        data.Store(42, atomic.Relaxed)
        ready.Store(1, atomic.Release) // publishes the store of 42
        ready.Wake(true)               // wakes every reader
    }
    try sys.Print("sum {n}\n", (n: sum.Load(atomic.SeqCst)))
}
```

```text
$ mk run publish.mk
sum 168
```

- The `Release` store of `ready` and each reader's `Acquire` load that sees it make the `Relaxed` store of 42 visible to the reader.
- A reader that starts after the store finds `ready` already 1 and never waits.

### A compare-exchange loop

```mk
// verdict: ok
import std.atomic

// RaiseTo sets m to the larger of m and x with a compare-exchange loop.
RaiseTo(m: &atomic.Atomic[i64], x: i64) {
    var cur := m.Load(atomic.Relaxed)
    for cur < x {
        r := m.CompareExchangeWeak(cur, x, atomic.AcqRel, atomic.Relaxed)
        if r.ok {
            return
        }
        cur = r.value // the value another task wrote; no second load
    }
}

export Main(sys: &System): WriteError!void {
    m := atomic.Atomic[i64].New(0)
    scope s := sys.io.Scope() {
        for x in [17, 3, 99, 42, 64] {
            s.go RaiseTo(&m, i64(x))
        }
    }
    flags := atomic.Atomic[u8].New(0b0001)
    old := flags.FetchOr(0b0100, atomic.Relaxed) // returns the old value
    try sys.Print("max {m}, flags {o} -> {f}\n", (m: m.Load(atomic.Acquire), o: old, f: flags.Load(atomic.Relaxed)))
}
```

```text
$ mk run max.mk
max 99, flags 1 -> 5
```

- The weak form fits here: a spurious failure only runs the loop once more.

### A spin lock

```mk
// verdict: ok
import std.atomic

struct Stats {
    count: int
    total: int
}

Record(io: Io, s: &atomic.Spin[Stats], x: int) {
    for _ in 0..100 {
        var g := s.Lock() // spins; never suspends the task
        p := g.Mut()
        p.count += 1
        p.total += x
        g.Unlock() // the guard is linear: every path unlocks
        io.Yield() else {}
    }
}

export Main(sys: &System): WriteError!void {
    s := atomic.Spin[Stats].New(Stats(count: 0, total: 0))
    scope sc := sys.io.Scope() {
        for x in 1..5 {
            sc.go Record(sys.io, &s, x)
        }
    }
    st := s.Into() // no guard is left, so the value comes out
    try sys.Print("{c} records, total {t}\n", (c: st.count, t: st.total))
}
```

```text
$ mk run spin.mk
400 records, total 1000
```

- Nothing between `Lock` and `Unlock` yields, so the program also runs with `MK_CARRIERS=1`. In a task that may wait while it holds the lock, use `Mutex` instead.

### Pass an order on

A function that takes an order as a `const` parameter states the clause of the operation it passes the order to. The compiler then checks the clause at each call of the wrapper.

```mk
// verdict: ok
import std.atomic

Peek(a: &atomic.Atomic[i64], const o: atomic.Order): i64
    requires o.CanLoad() {
    return a.Load(o)
}

Both(a: &atomic.Atomic[i64]): i64 {
    return Peek(a, atomic.Acquire) + Peek(a, atomic.SeqCst)
}
```

Without the clause, the order would reach the operation unchecked, so the compiler rejects the call inside the wrapper:

```mk
// verdict: reject E0706
import std.atomic

Poke(a: &atomic.Atomic[i64], const o: atomic.Order) {
    a.Store(1, o) // <- E0706
}
```

## API

<!-- api:begin -->
Stability: Stable.

### `AcqRel`

```
export const AcqRel: Order
```

Acquire and Release at once, for a read-modify-write or a fence.

### `Acquire`

```
export const Acquire: Order
```

For a load or read-modify-write: when it reads the value of a Release
write, it sees every write made before that write.

### `Atomic`

```
export struct Atomic[T: Plain]
```

One atomic word of T, with the memory order of each operation given at
the call. T is an integer of 1, 2, 4 or 8 bytes, int, uint or a raw
pointer (E0701 otherwise). Each operation is one atomic instruction;
none allocates or takes an Io.

An Atomic is unique, Send and Share whatever its T: reading through a
pointer it holds is for unsafe code to justify. A `var` global may hold
one, and an array of them starts zeroed with `mem.Zeroed`, as `[e; n]`
copies e and an Atomic is not Plain.

### `Atomic.CompareExchange`

```
export Atomic[T].CompareExchange(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
    requires success.CanModify()
    requires failure.CanLoad() && failure.CanModify()
```

- Flows: The result borrows from old and new. The result borrows what self refers to; self is free after the call.

Sets the word to new if it holds old. value is what the word held either
way, so a retry loop needs no second load. ok is true when the word held
old: then the read-modify-write has order success. Otherwise nothing is
written, and the read is a load of order failure, which may be stronger
than success (as C++20 allows) but cannot release. Strong: ok is false
only when value != old. O(1); it allocates nothing.

### `Atomic.CompareExchangeWeak`

```
export Atomic[T].CompareExchangeWeak(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
    requires success.CanModify()
    requires failure.CanLoad() && failure.CanModify()
```

- Flows: The result borrows from old and new. The result borrows what self refers to; self is free after the call.

CompareExchange that may fail spuriously, with ok false and value ==
old, for a loop that retries anyway. On CPUs whose compare-exchange is
a load-linked and store-conditional pair it saves an inner loop; on
x86-64 it is the same instruction as CompareExchange. O(1); it
allocates nothing.

### `Atomic.FetchAdd`

```
export Atomic[T].FetchAdd(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Adds x, wrapping, and returns the old value. T is an integer (E0701 for
a pointer). Wait-free. Any order but Unordered. O(1); it allocates
nothing.

### `Atomic.FetchAnd`

```
export Atomic[T].FetchAnd(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Sets the word to its bitwise and with x and returns the old value. T is
an integer. Any order but Unordered. O(1); it allocates nothing. On
x86-64, when the result is used, it is a compare-exchange loop and not
wait-free.

### `Atomic.FetchOr`

```
export Atomic[T].FetchOr(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Sets the word to its bitwise or with x and returns the old value. T is
an integer. Any order but Unordered. O(1); it allocates nothing. On
x86-64, when the result is used, it is a compare-exchange loop and not
wait-free.

### `Atomic.FetchSub`

```
export Atomic[T].FetchSub(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Subtracts x, wrapping, and returns the old value. T is an integer.
Wait-free. Any order but Unordered. O(1); it allocates nothing.

### `Atomic.FetchXor`

```
export Atomic[T].FetchXor(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Sets the word to its bitwise xor with x and returns the old value. T is
an integer. Any order but Unordered. O(1); it allocates nothing. On
x86-64, when the result is used, it is a compare-exchange loop and not
wait-free.

### `Atomic.Load`

```
export Atomic[T].Load(&self, const o: Order): T
    requires o.CanLoad()
```

- Flows: The result borrows what self refers to; self is free after the call.

Returns the value of the word. o is Relaxed, Acquire, SeqCst or
Unordered (E0706 otherwise). O(1); it allocates nothing.

### `Atomic.New`

```
export Atomic[T].New(x: T): Atomic[T]
```

- Flows: The result borrows from x.

Returns an Atomic holding x. O(1); it allocates nothing, so a `var`
global may be initialized with it.

### `Atomic.Store`

```
export Atomic[T].Store(&self, x: T, const o: Order)
    requires o.CanStore()
```

Sets the word to x. o is Relaxed, Release, SeqCst or Unordered (E0706
otherwise). O(1); it allocates nothing.

### `Atomic.Swap`

```
export Atomic[T].Swap(&self, x: T, const o: Order): T
    requires o.CanModify()
```

- Flows: The result borrows from x. The result borrows what self refers to; self is free after the call.

Sets the word to x and returns what it held, in one read-modify-write.
Any order but Unordered. O(1); it allocates nothing.

### `Atomic.Wait`

```
export Atomic[T].Wait(&self, io: Io, expected: T): Canceled!void
    requires SizeOf[T]() == 4
```

- Errors: Canceled

Parks the task while the word holds expected, until a Wake. Wait may
also return early for no reason, so read the word again in a loop. It
fails with Canceled when io's scope is or becomes cancelled. Called off
a task, it panics with code 16. The check of the word and the park are
one step as far as Wake is concerned: a Wake after the word changed is
not lost. T is 4 bytes (E0706 otherwise), since the word is compared by
its bits. O(1) besides the wait; it allocates nothing.

### `Atomic.Wake`

```
export Atomic[T].Wake(&self, all: bool)
    requires SizeOf[T]() == 4
```

Wakes one task waiting on the word, or every one when all is true. It
may be called from any thread, a foreign one included, and needs no Io.
It also wakes tasks waiting under a simulated Io or a foreign Io: it
calls each live foreign Io's wake entry from the waking thread. It takes
a sequentially consistent fence after waking native waiters and before
it wakes waiters on other Io. T is 4 bytes (E0706 otherwise). O(1)
besides the wake; it allocates nothing.

### `AtomicBool`

```
export struct AtomicBool
```

A bool that tasks share and change atomically, for a flag. Every
operation on it is sequentially consistent (SeqCst).

### `AtomicBool.Load`

```
export AtomicBool.Load(&self): bool
```

Returns the value. Its ordering is AtomicInt.Load's, an acquire: after
it, the task sees every write made before the Store or Swap it read.
O(1); it allocates nothing.

### `AtomicBool.New`

```
export AtomicBool.New(b: bool): AtomicBool
```

Returns an AtomicBool holding b. O(1); it allocates nothing, so a `var`
global may be initialized with it.

### `AtomicBool.Store`

```
export AtomicBool.Store(&self, b: bool)
```

Sets the value to b. Its ordering is AtomicInt.Store's, a release: a
task whose Load or Swap reads b also sees every write this task made
before. O(1); it allocates nothing.

### `AtomicBool.Swap`

```
export AtomicBool.Swap(&self, b: bool): bool
```

Sets the value to b and returns the old value, in one atomic step. When
any number of tasks call Swap(true) on a false flag, exactly one gets
false back: a one-shot flag. SeqCst, an acquire and a release: it sees
what the write it replaces published, and publishes this task's earlier
writes to the next reader. O(1); it allocates nothing.

### `AtomicInt`

```
export struct AtomicInt
```

An i64 that tasks share and change atomically. Every operation on it is
sequentially consistent (SeqCst), so it needs no memory order.

### `AtomicInt.Add`

```
export AtomicInt.Add(&self, n: i64): i64
```

Adds n and returns the new value. Wait-free: one atomic
read-modify-write, however many tasks add at once. O(1); it allocates
nothing.

A sum that overflows i64 panics with code 1, which ends the process.
Before the panic, Add subtracts n back, so the wrapped value is in the
word only for that moment.

Ordering: SeqCst. Add reads the latest value of the word, and after it
the task sees every write that tasks made before their earlier Store or
Add of the word. A Load or Add that reads its result, or a later value,
sees every write this task made before the Add.

### `AtomicInt.Load`

```
export AtomicInt.Load(&self): i64
```

Returns the value. SeqCst: after the Load, the task sees every write
made before the Store or Add whose value it read. O(1); it allocates
nothing.

### `AtomicInt.New`

```
export AtomicInt.New(n: i64): AtomicInt
```

Returns an AtomicInt holding n. O(1); it allocates nothing, so a `var`
global may be initialized with it.

### `AtomicInt.Store`

```
export AtomicInt.Store(&self, n: i64)
```

Sets the value to n. SeqCst: a task whose Load or Add reads n also sees
every write this task made before the Store. O(1); it allocates nothing.

### `Fence`

```
export Fence(const o: Order)
    requires o.CanFence()
```

Orders memory without touching a word. o is Acquire, Release, AcqRel or
SeqCst; Relaxed and Unordered order nothing, so they are E0706. An
Acquire fence after a Relaxed load makes that load an Acquire; a Release
fence before a Relaxed store makes that store a Release, as C++20's
fences do. O(1); it allocates nothing.

### `Order`

```
export struct Order
```

A memory order: how an atomic operation orders the memory accesses
around it. Pass one of the consts below as an operation's const
argument; no other value can be made. One byte.

### `Order.CanFence`

```
export Order.CanFence(self): bool
```

Reports whether a fence may have this order: every order but Relaxed,
which orders nothing, and Unordered, which is for a load or store only.
Fence requires it. O(1); it allocates nothing.

### `Order.CanLoad`

```
export Order.CanLoad(self): bool
```

Reports whether a load may have this order: Relaxed, Acquire, SeqCst or
Unordered, since a load cannot release. Load and the failure order of a
compare-exchange require it. Evaluated at compile time at each call.
O(1); it allocates nothing.

### `Order.CanModify`

```
export Order.CanModify(self): bool
```

Reports whether a read-modify-write may have this order: every order
but Unordered, which is for a load or store only. Swap, the Fetch
operations and both orders of a compare-exchange require it. O(1); it
allocates nothing.

### `Order.CanStore`

```
export Order.CanStore(self): bool
```

Reports whether a store may have this order: Relaxed, Release, SeqCst or
Unordered, since a store cannot acquire. Store requires it. O(1); it
allocates nothing.

### `Pause`

```
export Pause()
```

Tells the CPU that the thread is spinning on a word, which saves power
and lets a sibling hardware thread run (`pause` on x86_64). It changes
nothing a program can observe. O(1); it allocates nothing.

### `Relaxed`

```
export const Relaxed: Order
```

The operation is atomic, and the word's writes fall in one order that
every task agrees on, but it orders no other memory: other memory may be
seen in any order around it.

### `Release`

```
export const Release: Order
```

For a store or read-modify-write: an Acquire that reads its value sees
every write this task made before it.

### `SeqCst`

```
export const SeqCst: Order
```

Acquire, Release or both, as the operation allows, and every SeqCst
operation of the program falls in one order that all tasks agree on.

### `Spin`

```
export view struct Spin[T]
```

A lock around a value of T that spins and never suspends a task, so it
needs no Io. Lock returns a SpinGuard; Into takes the value out. Spin is
Send and Share when T is Send.

### `Spin.Into`

```
export Spin[T].Into(self): T
```

- Flows: The result borrows from self.

Consumes the Spin and returns its value. A Spin passed by value has no
guard borrowing it, so it is unlocked. O(1); it allocates nothing.

### `Spin.Lock`

```
export Spin[T].Lock(&self): SpinGuard[T]
```

- Flows: The result borrows from self.

Spins until the lock is free, takes it, and returns the guard. It never
fails and never suspends the task, and it is not fair: a waiter may be
passed over. It allocates nothing. The writes made under the lock by
its last holder are visible once Lock returns.

A waiter reads the lock word until it is free, then tries to take it,
and pauses after each failed take, twice as long each time. From the
64th look on, Lock yields the OS thread before each look instead,
through the yield hook MkThreadYield, which needs no import: a hosted
program gets the OS's yield, and a freestanding one only pauses unless
it exports its own.
It never yields the task, so with one carrier a task spinning on a lock
that another task holds never lets that task run.

### `Spin.New`

```
export Spin[T].New(x: T): Spin[T]
```

- Flows: The result borrows from x.

Returns a Spin holding x, unlocked. O(1); it allocates nothing, so a
`var` global may be initialized with it.

### `SpinGuard`

```
export linear view struct SpinGuard[T]
```

Proof that a Spin is held, returned by Spin.Lock. It borrows the Spin,
which stays alive and locked until Unlock. The guard is linear: every
path must end it with Unlock.

### `SpinGuard.Mut`

```
export SpinGuard[T].Mut(mut self): mut T
```

- Flows: The result borrows from self.

Returns the guarded value, for writing. O(1); it allocates nothing.

### `SpinGuard.Ref`

```
export SpinGuard[T].Ref(&self): &T
```

- Flows: The result borrows from self.

Returns the guarded value, for reading. O(1); it allocates nothing.

### `SpinGuard.Store`

```
export SpinGuard[T].Store(mut self, x: T into self)
```

Replaces the guarded value with x and drops the old one. For a linear
T the call is E0701: use Replace(g.Mut(), x) to get the old value back.
O(1); it allocates nothing.

### `SpinGuard.Unlock`

```
export SpinGuard[T].Unlock(self)
```

Releases the lock with a Release store and ends the guard. The next
holder sees every write made under the lock. The store is not SeqCst, so
it orders no later load of this task. O(1); it allocates nothing.

### `Unordered`

```
export const Unordered: Order
```

For a load or store only: a load sees some value a store wrote, never a
torn one, and nothing is ordered by it. Fence and every
read-modify-write refuse it (E0706).

<!-- api:end -->
