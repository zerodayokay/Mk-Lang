---
description: Declaring functions, several results as a record, var parameters, function values, fn literals and what they may capture, and guaranteed tail calls with become.
---

# Functions

A function is a name, parameters with their types, a result type after a colon, and a body. Every function returns with `return`; a body's last expression is not its value.

```mk
// verdict: ok
Add(a: int, b: int): int {
    return a + b
}

DivMod(a: int, b: int): (q: int, r: int) { // several results are one record
    return (q: a / b, r: a % b)
}

Steps(var n: int): int { // a by-value parameter is fixed unless marked var
    var steps := 0
    for n > 1 {
        n /= 2
        steps += 1
    }
    return steps
}

Apply(f: fn(int, int): int, x: int, y: int): int { // a function value
    return f(x, y)
}

Combined(): int {
    (q, r) := DivMod(17, 5) // destructure a record by its field names
    return Apply(Add, q, r) + Steps(8)
}
```

- There is no `fn` or `func` keyword in a declaration: a function is its signature.
- A function with no result type returns nothing (`void`).
- A by-value parameter is fixed inside the body unless it is marked `var`.
- Several results are returned as one record, which the caller takes apart by field name.

There is no overloading, no default argument, no variadic parameter and no named argument. By convention, parameters come in one order: the receiver, then an arena or allocator, then `io`, then the rest ([code style](./style)).

## What a signature tells the caller

A signature is a contract the compiler checks in both directions: the body must keep it, and each caller relies on it alone.

```mk
// verdict: ok
// takes an arena and text, may fail only with OutOfMemory, returns text in the arena
Upper(a: &Arena, text: str): OutOfMemory!str from a {
    return try text.ToUpper(a)
}
```

- **Parameters** say what the function can touch. With no `Io` it cannot do I/O; with no allocator or arena it cannot allocate.
- **The error set**, before `!`, lists every error it can fail with ([errors](./errors)).
- **`from` and `into`** say where a returned reference points and what a parameter is stored in ([memory](./memory#where-results-come-from)).

Because callers are checked against the signature alone, a change inside a function body never breaks its callers.

## Methods

A function named `Type.Name` lives in that type's namespace, and one whose first parameter is `self` is called with a dot. [Methods and interfaces](./methods-and-interfaces) covers them.

## Function values

A declared function's name, written without a call, is a `fn` value. Its type is written `fn(i32, i32): i32`:

```mk
// verdict: ok
Add(a: i32, b: i32): i32 {
    return a + b
}

Apply(f: fn(i32, i32): i32): i32 {
    return f(1, 2)
}

Three(): i32 {
    return Apply(Add)
}
```

A function value holds no captured state, and Mk functions used as values use the platform's C calling convention, so C can call them ([unsafe code and C](./unsafe-and-c)).

## Function literals

`fn(a: i32): bool { return a > 0 }` is a function literal. It is not a closure: a literal used as a value cannot capture the caller's locals (E0609).

```mk
// verdict: reject E0609
Over(limit: int): fn(&int): bool {
    return fn(x: &int): bool { return *x > limit } // <- E0609
}
```

The exception is a literal passed directly to a `const` parameter of type `fn`, such as `Sort`'s comparison. That literal is compiled into the call, so it may read the enclosing locals, and mutate `var` locals, for the duration of that call:

```mk
// verdict: ok
ByDistance(xs: mut []int, origin: int): int {
    var compares := 0
    xs.Sort(fn(a: &int, b: &int): bool {
        compares += 1
        return (*a - origin) * (*a - origin) < (*b - origin) * (*b - origin)
    })
    return compares
}
```

To keep a callback for later, store a `fn` value beside the state it needs, and pass that state in as a parameter. The cookbook shows the pattern in [keep a callback for later](/examples/cookbook#keep-a-callback-for-later).

## Tail calls

`become f(a)` calls `f` in place of the current function: the call reuses the frame, so the stack does not grow. Use it for a loop written as recursion, or a state machine of functions that call each other.

```mk
// verdict: ok
Gcd(a: u64, b: u64): u64 {
    if b == 0 {
        return a
    }
    become Gcd(b, a % b)
}

Count(n: int, acc: int): int {
    if n == 0 {
        return acc
    }
    become Count(n - 1, acc + n) // a million calls, one frame
}

export Main(sys: &System): WriteError!void {
    g := Gcd(1071, 462)
    s := Count(1000000, 0)
    try sys.Print("{g} {s}\n", (g, s))
}
```

```text
21 500000500000
```

[Tail calls](/reference/language/statements#tail-calls) lists what a `become` call must satisfy.

## Recursion and the stack

Ordinary recursion works as in C. Running out of stack is a panic with a report, not a silent crash ([panics](./errors#panics)), and a scope can set the stack size of the tasks it starts ([concurrency](./concurrency)).

## Next

[Structs, enums and records](./structs-and-enums).
