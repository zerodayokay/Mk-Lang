---
description: Mk is a small compiled language for memory-safe systems programming without a garbage collector. What it is for, what it checks, and how this guide is organised.
---

# What is Mk?

Mk is a small compiled language for memory-safe systems programming without a garbage collector. It is meant for software that has to be fast and correct: command-line tools, servers, language runtimes and kernels.

The `mk` compiler builds native executables through LLVM, for Windows, Linux and macOS on x86-64 and AArch64, and for freestanding x86-64 with no operating system ([targets](/reference/language/modules-and-build#targets)).

## What the compiler checks

- **Memory safety without a garbage collector.** The compiler checks ownership and borrowing, so a use after free, a double free or a data race does not compile. It needs no lifetime annotations to do this: a signature says where a result borrows from with `from`, and the compiler infers the rest ([memory and ownership](./memory)).
- **Resources released exactly once.** A file, a lock guard or a heap container is *linear*: the compiler makes you release it on every path, and only once.
- **Errors in the type.** A function that can fail says which errors in its result type, `E!T`, and the caller must handle them or pass them on with `try` ([optionals and errors](./errors)).
- **Arithmetic and bounds in every build.** Integer overflow, an index out of bounds and division by zero panic in release builds too. Wrapping arithmetic is a separate operator, `+%` ([operators](./operators)).

## How Mk code reads

Mk is designed to be read by people and written by programs as well as people. Four principles shape it:

| Principle | What it means in practice |
|---|---|
| **Small** | The whole language, borrowing rules included, is meant to fit in your head. A feature gets in only if it pays for itself. |
| **Explicit** | Allocation, errors, I/O and borrowing show in signatures. A function with no `Io` parameter cannot do I/O; one with no allocator cannot allocate. |
| **One way to write each thing** | No macros, no inheritance, no implicit conversions, no overloading, no hidden control flow. `for` is the only loop. |
| **Errors that lead to the fix** | Every diagnostic has a code. `mk explain E0404` explains one, and `mk fix` applies the fixes the compiler can prove. |

A complete program shows most of this at once:

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    try sys.Print("hello, world\n")
}
```

```text
hello, world
```

- A program starts at `export Main`. It receives `sys: &System`, which holds the program's arguments, standard streams, a heap, an arena and the `Io` handle. There are no globals for I/O or memory.
- Printing can fail, so `Print` returns `WriteError!void`: an error or nothing. `try` passes an error up to the caller, and an error that leaves `Main` is printed and exits with status 1.

## What Mk does not have

Knowing what is absent saves time when you come from another language. Mk has no garbage collector, no `null` (use `?T`), no exceptions (errors are values), no inheritance, no macros, no operator overloading, no function overloading, no default or named arguments, no closures that capture by reference outside a call, no `async`/`await`, no implicit numeric conversions and no semicolons. The compiler recognises the common forms of other languages and names the Mk form in its error; [habits from other languages](/reference/language/habits-from-other-languages) has the full table.

## Status

Mk is at version 0.6. The language and the standard library may still change between 0.x releases; each standard-library module is marked Stable, Experimental or Provisional, and [stability](/reference/std/#9-stability) says what each level promises. The [changelog](/development/changelog) lists every user-visible change.

## How this guide is organised

1. **Introduction**: [install mk](./installation), run a program in the [quick start](./quick-start), then build [your first program](./first-program) step by step.
2. **Fundamentals**: [syntax](./syntax), [variables](./variables), [types](./types), [operators](./operators), [strings](./strings) and [collections](./collections).
3. **Language features**: [control flow](./control-flow), [functions](./functions), [structs and enums](./structs-and-enums), [methods and interfaces](./methods-and-interfaces), [generics](./generics), [errors](./errors), [memory](./memory), [concurrency](./concurrency), [modules](./modules), [testing](./testing), [compile-time code](./compile-time) and [unsafe code and C](./unsafe-and-c).

The guide teaches; the [language reference](/reference/) states every rule, and the [memory model](/reference/memory) states every borrowing rule with an accepted and a rejected program. Every Mk example on this site is compiled against the real compiler, and an example labelled **Does not compile** shows the exact error codes the compiler reports.
