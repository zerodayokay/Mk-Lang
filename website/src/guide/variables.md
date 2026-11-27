---
description: Bindings with :=, var for values that change, declared-but-unassigned locals, constants computed at compile time, global variables, and the scope rules - no shadowing, every local used.
---

# Variables and scope

## Bindings

`x := e` declares the local `x` and gives it the value of `e`. A binding is fixed: it cannot be assigned again. `var x := e` declares one that can.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    name := "ada" // a binding: name cannot change
    var count := 0 // var: count may be assigned again
    count += 1
    small: i8 = -3 // with a type: the literal is an i8
    var total: i64 = 100
    total += i64(small)
    label: str // declared here, assigned below on every path
    if count > 0 {
        label = "some"
    } else {
        label = "none"
    }
    try sys.Print("{name} {count} {small} {total} {label}\n", (name, count, small, total, label))
}
```

```text
ada 1 -3 97 some
```

- The type comes from the value unless you write one: `small: i8 = -3`. A bare integer literal is an `int`, and a bare float literal an `f64`.
- `x: T` declares a local with no value. It must be assigned on every path before it is read (E0403). Without `var` it is assigned once.
- `var` also matters for borrowing: only a `var` local can be borrowed for writing with `mut x` ([memory](./memory#borrowing)).

Assigning to a binding without `var` is an error:

```mk
// verdict: reject E0404
Count(): int {
    n := 0
    n = 1 // <- E0404
    return n
}
```

A local read before it is assigned on every path:

```mk
// verdict: reject E0403
Pick(c: bool): int {
    x: int
    if c {
        x = 1
    }
    return x // <- E0403
}
```

## Assignment

`p = e` stores a value in a place: a `var` local, a field or element of one, or what a `mut` reference points at. The compound forms `+= -= *= /= %=`, the wrapping `+%= -%= *%=`, and the bit forms `<<= >>= &= |= ^=` combine an operator with the store. Assignment is a statement, not an expression: `a = b = 1` is E0101.

## Scope

A local lives from its declaration to the end of the block that declares it. Two rules keep names unambiguous:

- **No shadowing.** A local cannot reuse the name of another local or parameter of the same function, even in an inner block (E0206). It may reuse a name from outside the function, such as an imported module's.
- **Every local is used.** A local that is never read is an error (E0208). Discard a value with `_ = e`. In a pattern or loop header, `_` stands for a part you do not need: `for _ in 0..3`.

```mk
// verdict: reject E0206
Twice(n: int): int {
    x := n
    if n > 0 {
        x := n * 2 // <- E0206
        return x
    }
    return x
}
```

Rename the inner local, or assign to a `var`. Loop variables and the bindings of `if x := e` follow the same rules: they cannot be assigned, and they cannot shadow.

## Destructuring

A binding can take a record or a struct apart by its field names:

```mk
// verdict: ok
type Span = (lo: int, hi: int)

struct Pixel {
    x: int
    y: int
}

Width(s: Span): int {
    (lo, hi) := s // fields by name: lo and hi
    return hi - lo
}

Sum(p: Pixel): int {
    Pixel(x, y) := p // locals named after the struct's fields
    return x + y
}
```

The names must be the field names, and the pattern names every field or ends with `..`. [Destructuring](/reference/language/patterns#destructuring) has the rules, including renaming a field as you bind it.

## Constants

A `const` is a value the compiler computes. Its initializer may call ordinary functions, which run while the program compiles:

```mk
// verdict: ok
struct Vec2 {
    export x: int
    export y: int
}

const Width = 4
const Origin: Vec2 = Vec2(x: 0, y: 0)
const Primes: [4]int = [2, 3, 5, 7]
const Names: []str = ["ada", "grace"]
const Volume = Cube(Width) // a function call, run by the compiler

Cube(n: int): int {
    return n * n * n
}

export Main(sys: &System): WriteError!void {
    row: [Width]u8 = [0; Width] // a constant can size an array
    n := row.Len()
    p := Primes[2]
    x := Origin.x
    k := Names.Len()
    try sys.Print("{n} {p} {x} {k} {Volume}\n", (n, p, x, k, Volume))
}
```

```text
4 5 0 2 64
```

- The value is computed once and stored in the program; a constant costs nothing at run time.
- Its type must be plain: copyable, with no owned resources (E0408). A constant cannot be assigned.
- An initializer that panics, or constants that depend on each other in a cycle, are compile errors. [Compile-time code](./compile-time) shows more.

## Global variables

A module-level `var` is one instance shared by the whole program and every task. Its type must be safe to share between tasks (`Share`, E1002), so a global is usually an atomic or a `Mutex`:

```mk
// verdict: ok
import std.atomic

var Hits: atomic.Atomic[int] = atomic.Atomic[int].New(0)

Hit() {
    _ = Hits.FetchAdd(1, atomic.Relaxed)
}

export Main(sys: &System): WriteError!void {
    Hit()
    Hit()
    n := Hits.Load(atomic.Relaxed)
    try sys.Print("{n}\n", (n))
}
```

```text
2
```

A global is never assigned or borrowed `mut`; its own methods change it. A plain `var Count: int` that a function writes to does not compile:

```mk
// verdict: reject E0507
var Count: int = 0

Reset() {
    Count = 1 // <- E0507
}
```

Prefer passing state as parameters. Globals are for counters, caches and the like that the whole program shares ([globals in the memory model](/reference/memory#r16-globals)).

## Next

[Types and conversions](./types): the built-in types, and how to convert between them.
