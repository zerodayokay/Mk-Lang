---
description: Structured concurrency in Mk - Io as the capability to block, tasks in scopes, results through mut arguments, channels, mutexes, cancellation and deadlines, with data races rejected at compile time.
---

# Concurrency

Mk has no `async` and no `await`, and any function can call any other. A function can block or suspend only through an `Io` value it was given, so a function without one cannot do I/O. `Main` gets one in `sys.io`.

## Tasks in scopes

Tasks are structured: they start inside a `scope` block, and the block does not exit until every task in it has finished. `s.go F(args)` starts a task that runs `F`. Its arguments are evaluated at once, in the parent. They may borrow the parent's locals, because the scope outlives the task.

```mk
// verdict: ok
Sum(xs: []int, out: mut int) {
    for x in xs {
        *out += x
    }
}

export Main(sys: &System): WriteError!void {
    data := [1, 2, 3, 4, 5, 6, 7, 8]
    var left := 0
    var right := 0
    scope s := sys.io.Scope() {
        s.go Sum(data[..4], mut left) // each task gets its own exclusive loan
        s.go Sum(data[4..], mut right)
    }                                 // both tasks have finished here
    try sys.Print("{left} + {right} = {t}\n", (left, right, t: left + right)) // prints 10 + 26 = 36
}
```

```text
10 + 26 = 36
```

- A task returns `void`. Results come back through `mut` arguments, as here, or through a channel.
- Arguments must be `Send`: safe to hand to another task.
- There are no data races: two tasks cannot hold exclusive loans on one place, and the compiler rejects a program that tries.

```mk
// verdict: reject E0501
Bump(n: mut int) {
    *n += 1
}

export Main(sys: &System) {
    var count := 0
    scope s := sys.io.Scope() {
        s.go Bump(mut count)
        s.go Bump(mut count) // <- E0501
    }
}
```

## Channels

A `Chan[T]` passes values between tasks. It is shared, so it comes from a heap allocator, and you free it after the tasks that use it are done.

```mk
// verdict: ok
Produce(io: Io, ch: &Chan[int], n: int) {
    for i in 0..n {
        ch.Send(io, i * i) else {
            return
        }
    }
    ch.Close()
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    ch := try Chan[int].New(a, 4) // room for four values
    defer ch.Free(a)
    var total := 0
    scope s := sys.io.Scope() {
        s.go Produce(sys.io, &ch, 10)
        for {
            x := ch.Recv(sys.io) else { // fails with Closed once the producer is done
                break
            }
            total += x
        }
    }
    try sys.Print("{total}\n", (total)) // prints 285
}
```

```text
285
```

`Select` over several channels is in [`std.sync`](/reference/std/sync).

## Shared state

Shared state goes behind a `Mutex[T]`. `Lock` returns a guard, which is linear, so you cannot forget to unlock it:

```mk
// verdict: ok
Add(io: Io, total: &Mutex[int], k: int) {
    var g := total.Lock(io) else {
        return
    }
    *g.Mut() += k
    g.Unlock()
}

export Main(sys: &System): WriteError!void {
    total := Mutex.New(0)
    scope s := sys.io.Scope() {
        for k in 1..101 {
            s.go Add(sys.io, &total, k)
        }
    }
    try sys.Print("{n}\n", (n: total.Into())) // prints 5050
}
```

```text
5050
```

`std.sync` has `RwLock`, `Semaphore`, `Once` and more, and `std.atomic` has atomic integers and values.

## Errors, cancellation and limits

A scope can do more than wait:

| Need | Use |
|---|---|
| Collect the errors tasks fail with | `io.ScopeOf[E]()` |
| Cancel every task in the scope | `s.Cancel()` |
| Give the scope a deadline | `s.CancelAfter(d)` |
| Limit the number of tasks and their stack size | `io.ScopeWith(...)` |

A cancelled operation fails with `Canceled`, an ordinary error in its set, so a signature shows what can be cancelled. Cancellation is level-triggered: once a scope is cancelled, every cancellable operation in it keeps returning `Canceled`. The rules are in [concurrency](/reference/language/concurrency).

## Testing concurrent code

Because every blocking operation goes through `Io`, a test can swap in the simulated `Io` of [`std.sim`](/reference/std/sim), which replays a whole concurrent program, its disk, network and clock included, from a seed. A failure found once can be replayed exactly ([testing](./testing)).

## Recipes

The cookbook shows how to:

- [serve each connection in its own task](/examples/cookbook#serve-each-connection-in-its-own-task),
- [time out a read, an idle connection and a retry](/examples/cookbook#time-out-a-read-an-idle-connection-and-a-retry),
- [handle errors inside tasks](/examples/cookbook#handle-errors-inside-tasks),
- [shut down on a signal](/examples/cookbook#shut-down-on-a-signal),
- [process a slice in parallel](/examples/cookbook#process-a-slice-in-parallel).

## Next

[Modules and packages](./modules).
