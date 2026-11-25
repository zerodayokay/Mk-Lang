# Why Mk looks like this

Mk is a memory-safe systems language with no garbage collector, no hidden control flow and no hidden allocation. It is deliberately small and explicit. Each thing has one way to be written, and Mk leaves out abstractions whose main purpose is convenience. The aim is code that a person can review quickly and an AI agent can write correctly, in a language small enough to fit whole in an agent's context.

Most answers below come back to that aim: a feature that saves typing but hides what the code does, or adds a second way to say the same thing, stays out. This page answers the questions people ask most about what Mk includes, what it leaves out, and why.

## Where Mk's ideas come from

| Language | Mk takes | Mk leaves |
|---|---|---|
| C | Declared-order struct layout. The C ABI as the contract between languages. A small mental model of a call | Undefined behaviour. Textual `#include`. Null by default. Unchecked arrays |
| Go | Lightweight tasks and the word `go`. `defer`. One formatter with no options. A directory as a module. An import graph without cycles. A build cache keyed by content. Minimal version selection. No semicolons | Garbage collection. Moving stacks. Implicit interface satisfaction. `nil`. The open `error` interface. Detached goroutines. Export by letter case |
| Rust | Shared XOR exclusive borrowing. Enums with payloads and exhaustive `match`. Generics checked at the definition. One `impl` per type and interface. Generics compiled once per type argument. `unsafe` as a marked boundary. `Send` and `Sync` | Lifetime parameters. Variance. Implicit `Drop`. `Pin`. Procedural macros. `dyn` as a language type. Closures as values. Iterator adapters. `async`. Cargo features. Unwinding |
| Zig | Compile-time code in the same language. Explicit allocators and unmanaged containers. `defer` and `errdefer` (Mk's `onerr`). Prefix `try`. Error return traces. `+%` wrapping operators. The spellings `E!T` and `?T`. `Io` passed as a parameter. `Reader` and `Writer` with a buffer. `test` blocks | `anytype` and duck-typed generics. Lazy analysis of unused code. Inferred error sets. Undefined behaviour in fast builds. Type reification. Errors without payloads. A program as the build file |
| Odin | Iterators as plain procedures. Lambdas that capture nothing. Compile-time branches that are type-checked | The implicit `context` allocator |

Mk also takes structured concurrency and level-triggered cancellation from Trio, linear values with no implicit destructor from Austral, the separation of borrowing from copying a dependence from Swift, deterministic simulation testing from FoundationDB and TigerBeetle, and per-request memory regions from Postgres's memory contexts.

## Memory

### Why no garbage collector?

Mk is memory-safe without one. Ownership, borrowing and linear values give the guarantees, and the compiler checks them, so nothing scans memory at run time. Mk's own runtime, scheduler and allocators are written in Mk, and a kernel can be too.

### Why no lifetime parameters?

Mk tracks loans on values instead of naming lifetimes in types. Each value holds a set of loans, and the compiler follows them through the function. A signature says where a result comes from only when more than one parameter could supply it, with `from p`, and says where a parameter is stored with `into p`.

Programs often store references, but a value that holds references from several unrelated sources is rare. So one set of loans per value costs little precision, and no type ever carries a lifetime. The body is checked against the signature, so nothing in a body leaks to callers. The [memory model](memory.md) has the rules.

### Why no destructors?

A destructor is a call the reader cannot see, at the end of a scope. It brings drop order, drop checking and the question of what runs during a panic. Mk makes resources **linear** instead: a linear value must be consumed exactly once on every path, and the compiler proves it. Release is a visible call, often written with `defer` or `onerr`.

No safety argument relies on a release running. Anything that must happen, such as joining tasks, is a language block (`scope`), not a guard value.

### Why are arenas the default?

Most data in a request, a query or a short program dies together. `List`, `Map` and `Text` live in an arena and need no release, so most code never thinks about freeing memory. Heap containers (`HeapList`, `HeapMap`, `Box`) are linear and checked. Making every container linear would put a release call in most functions, to check memory that is safe to leak. Postgres's memory contexts are the model.

### Why pass allocators explicitly?

So a signature shows whether a function can allocate. A function can allocate only if a parameter can reach an allocator: an `&Arena`, an `&Alloc`, an `&Heap`, or a value holding one. Odin's implicit `context` allocator and Rust's global allocator are state a signature does not show. `Main` receives the program's arena and heap in `sys`; there is no global allocator.

### Why no null, and no zero values?

A reference is never null. A value that may be absent is `?T`, and the compiler makes you handle `none`. A local must be assigned before it is read, so there is no zero value to forget about. Uninitialized memory exists only in `unsafe` code.

## Errors

### Why no exceptions?

An exception is control flow that no line of the caller shows. In Mk a function that can fail says so in its return type, `E!T`, and the caller handles the error with `try`, `else` or `match` where it can see it. `try` is a prefix, so error paths stay at the left margin.

### Why must every signature list its error set?

If error sets were inferred from bodies, as in Zig, or open, as Go's `error` is, then editing a body could change what callers must handle. In Mk the set is part of the signature, so adding an error is a visible API change that `mk apidiff` reports. A library that wants room to grow declares its set [`open`](codebase.md#open-sets).

### Why can't errors carry references?

Error payloads are plain data. An error then allocates nothing, holds no loans, and can always travel up the stack past the data it describes. It carries an offset or a line number, not the text.

### Why does a panic end the process?

Mk has no unwinding. Unwinding brings landing pads, unwind safety, poisoned locks, panics during cleanup and trouble at the C boundary, and the result is hidden control flow. Abandoning only the failing task would leave locks held and `unsafe` state torn.

The cost is that one failing request ends every task in its process, where Go's `net/http` would recover per connection. `std.proc.Supervise` limits that to one worker process, and `mk query panics` lists every panic site a function can reach. Binaries are also smaller without unwind tables.

### Why does integer overflow trap in every build?

So a program means the same in a debug build and a release build. Rust traps in debug and wraps in release, so tests and production differ. Zig's fast mode makes overflow undefined, which breaks memory safety. Mk traps everywhere and offers `+%`, `-%` and `*%` where you want wrapping.

## Functions and types

### Why no closures?

A closure that outlives its call needs a decision about where its captures live: the stack, the heap or inline. Rust answers with `Fn`, `FnMut`, `FnOnce`, `move` and capture analysis. That is five concepts, and more for the reader to track. Odin has only non-capturing lambdas, and Zig's maintainers recommend a context struct, for the same reason.

Mk has `fn` literals. A literal passed as a `const` argument may read and mutate the caller's locals (`var` locals, or reborrowed `mut` references and slices), because it runs only during the call and its borrows end with the statement:

```mk
// verdict: ok
SortBy(xs: mut []i32, descending: bool) {
    xs.Sort(fn(a: &i32, b: &i32): bool {
        return if descending { *a > *b } else { *a < *b } // reads the caller's local
    })
}
```

A callback that is stored or returned is a struct with a method. That is the real cost: middleware and functional options become structs.

### Why no iterator adapters?

`for x in e` works over integers, slices, arrays and any value with a `Next(mut self): ?T` method. There is no `map`, `filter` or `collect`. A chain is often quicker to write; a loop is often easier to check, and it shows the allocation that `collect` hides. Zig and Odin treat iterators the same way, as plain functions.

### Why no dynamic dispatch in the language?

A language `dyn` type needs object-safety rules, a vtable layout and lifetime defaults. Mk generates the same thing as readable source instead: `gen dyn.Of(Writer)` emits a type named `WriterDyn`, so dispatch is visible in the name. Most dispatch in other languages has another form in Mk: error sets replace `dyn Error`, and `Io`, `Alloc`, `Reader` and `Writer` are concrete structs with a table of functions.

### Why no inheritance or embedding?

Both delegate calls in ways the call site does not show. Mk has structs, enums and interfaces with explicit `impl`. Interfaces are bounds on generics, checked at the definition, and dispatch is static.

### Why must `impl` be explicit?

So no name that happens to match makes a type conform to an interface. A type has at most one `impl` of an interface, in the module of the type or of the interface.

### Why no overloading, default arguments or implicit conversions?

Each saves typing and adds nothing the compiler can check. A name means one function. Conversions are spelled: `T(x)` converts only when no value is lost, `Cast[T](x)` traps if the value does not fit, and `Trunc[T](x)` keeps the low bits.

### Why no operator overloading?

An operator that may call any function is hidden dispatch. In Mk, `a + b` on two integers is integer addition and nothing else.

### Why no associated types, blanket impls or specialization?

An interface takes type parameters instead of associated types. One `impl` per type constructor keeps coherence simple. Where specialization would give a fast path, write `if T is u8 { ... }`: both branches are checked at the definition.

### Why no tuples?

Mk has named records: `(quot: i64, rem: i64)`. Destructuring matches by name, so swapping two fields is a compile error rather than a silent bug. The same form carries `Print`'s arguments. Only an array, whose elements are all of one type and have no names, is taken apart by position, with its own brackets: `[a, b] := xs`.

### Why no shadowing?

It saves typing and checks nothing. Declaring a name that hides another local is E0206.

```mk
// verdict: reject E0206
Clamp(x: int): int {
    y := x
    if y > 10 {
        y := 10 // <- E0206
        return y
    }
    return y
}
```

### Why is a length a signed `int`?

So `len - 1` on an empty slice gives -1, which you can see, rather than wrapping to a huge number. It also removes most casts between index and length types.

### Why is `str` always UTF-8, with no `+`?

A `str` is a view that is always valid UTF-8, so text functions never re-check it. Indexing is by byte offset. There is no `+` on strings because nothing allocates implicitly: build text with `Text` or `arena.Fmt`. A `Path` holds the bytes the OS gives, which may not be UTF-8.

## Compile time

### Why no macros?

Mk has three tools for metaprogramming, all in the same language. A `const` parameter bakes an argument into the function, as `Sort`'s comparator is. Const evaluation runs any function with no `Io` at compile time, for tables and parsed formats. `gen` runs a function that emits source, and the output is checked as if you had written it. Token macros and procedural macros are left out because their output is neither readable source nor checked at the definition.

### Why not types as values, as in Zig?

Duck-typed generics fail when they are instantiated, often deep inside a library, far from the mistake. Mk checks a generic once, at its definition, against its bounds. So a generic body can never fail for one particular type argument. A call checks only the bounds and `requires` clauses, and reports any failure at the call.

### Why no `const fn`?

A function with no `Io` parameter cannot do I/O, so any such function can run at compile time when its arguments are constants. No marker is needed.

### Why can't compile-time code do I/O?

Unrestricted I/O at compile time, as in Jai, cannot be deterministic or cached. Compile-time code in Mk cannot do I/O, and each evaluation has its own step limit, so the order in which the compiler evaluates things cannot change whether a program compiles.

### Why no conditional compilation attributes or features?

An `if` on a constant, such as `Target.os`, does the job, and both branches are checked. A per-target module, selected in the manifest, covers larger differences. Feature flags multiply the configurations a package must build in.

## Concurrency

### Why no `async` and `await`?

Any Mk function can call any other. Tasks are stackful, so a task can suspend anywhere it holds an `Io`. Stackless frames inferred by the compiler would let an edit to one body change the frame size of its callers.

### Isn't `Io` a function colour?

No. `Io` is a value, not a kind of function. Any function can call any other, and an `Io` can be stored, wrapped and replaced like any value. It carries the capability to block, so a signature shows which functions can. A test passes a simulated `Io` to run the program deterministically.

### Why can't a task outlive its scope?

Every task has a parent `scope`, and the block cannot exit until its tasks finish. So a task may borrow from its parent's stack, and errors and cancellation have somewhere to go. `scope` is a language block rather than a guard value, so it does not depend on a release running.

### Why is cancellation an error?

Cancellation is level-triggered, as in Trio: once a scope is cancelled, every cancellable operation in it keeps returning `Canceled`. Because it is a typed error in the operation's set, a signature shows what can be cancelled, and code cannot swallow it by accident the way a caught exception can be.

### Why don't task stacks grow?

Moving a stack needs a precise map of every pointer on it, and segmented stacks are slow when a call sits on a segment boundary. Mk gives each task a fixed slot of address space, touched on demand, with a guard page below it. A scope can ask for a larger stack size.

### Why no `select` statement?

It is rare. `sync.Select` provides typed selection over channels as a library.

## Tooling

### Why no warnings?

A reader or a tool cannot tell how much a warning matters. So in Mk a problem is an error or it is nothing.

### Why no formatter options?

So all Mk code has one layout. `mk fmt` has no settings, as `gofmt` has none.

### Why is `pkg.mk` not a program?

A manifest is a `const` value: it declares bins, dependencies, C imports and targets, and performs no actions. Mk leaves out Zig's build file, which is a general program.

## Adding to Mk

A language feature gets in only when three unrelated kinds of program need it and no library can provide it. It must be explainable in one paragraph, readable where it is used, and checkable by the compiler. Overloading, default arguments, implicit conversions, inheritance, higher-kinded types, token macros, warnings and formatter options all fail the same way: they save typing and add nothing the compiler can check.
