---
description: if and if-as-a-value, for as the only loop (ranges, slices, iterators, conditions, labels), exhaustive match with patterns, and defer and onerr for cleanup.
---

# Control flow

Mk has four control-flow constructs: `if`, `for`, `match` and `defer`. Conditions take no parentheses, braces are always required, and a condition must be a `bool`.

## if

```mk
// verdict: ok
Sign(n: int): int {
    if n < 0 {
        return -1
    } else if n == 0 {
        return 0
    }
    return 1
}

Abs(n: int): int {
    return if n < 0 { -n } else { n } // an if with an else is a value
}
```

An `if` with an `else` is an expression: each branch's last line is its value. `if x := e { }` runs when an optional is `some`, with `x` bound to its value ([optionals](./errors#optionals)).

## for

`for` is the only loop. It counts over a range, walks a slice or an iterator, or repeats while a condition holds.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    xs := [3, 1, 4, 1, 5]
    var sum := 0
    for i in 0..xs.Len() {  // a half-open range: 0, 1, 2, 3, 4
        sum += xs[i]
    }
    for x in xs {           // each element, copied
        sum += x
    }
    for i, x in xs {        // with its index
        if i == 0 {
            sum += x
        }
    }
    var n := 27
    var steps := 0
    for n != 1 {            // a while loop
        n = if n % 2 == 0 { n / 2 } else { 3 * n + 1 }
        steps += 1
    }
    var found := 0
    outer: for a in 1..10 { // a label for break and continue
        for b in 1..10 {
            if a * b == 42 {
                found = a
                break outer
            }
        }
    }
    try sys.Print("{sum} {steps} {found}\n", (sum, steps, found)) // prints 31 111 6
}
```

```text
31 111 6
```

| Form | Loops |
|---|---|
| `for i in 0..n { }` | Over a half-open range |
| `for x in xs { }` | Over each element, copied |
| `for x in &xs { }` | Over a shared reference to each element |
| `for x in mut xs { }` | Over an exclusive reference to each element, to change it |
| `for i, x in xs { }` | With the index |
| `for cond { }` | While `cond` is true |
| `for { }` | Until `break` or `return` |
| `for x := e { }` | While `e` gives `some(x)` |

`break` and `continue` work as in C, and a label (`outer:`) lets them leave an outer loop.

### Iterators

An iterator is an ordinary struct with a `Next` method that returns an optional. `for` calls it until it returns `none`:

```mk
// verdict: ok
struct Countdown {
    left: int
}

Countdown.Next(mut self): ?int {
    if self.left == 0 {
        return none
    }
    self.left -= 1
    return some(self.left)
}

Doubled(xs: mut []int) {
    for x in mut xs {
        *x *= 2
    }
}

Total(): int {
    var sum := 0
    for n in Countdown(left: 4) { // 3, 2, 1, 0
        sum += n
    }
    return sum
}
```

There are no `.map`, `.filter` or `.collect` chains: write the loop.

## match

`match` runs the first arm whose pattern fits, and it must cover every case (E0306). Used as a value, a `match` gives the last line of the arm that ran.

```mk
// verdict: ok
Classify(n: int): str {
    return match n {
        0 { "zero" }
        1..10 { "small" }    // a half-open range
        10.. { "large" }     // an open end runs to the type's limit
        _ { "negative" }
    }
}

Method(name: str): int {
    match name {
        "GET" | "HEAD" {
            return 1
        }
        "POST" {
            return 2
        }
        other {              // a name binds the whole value
            return other.Len()
        }
    }
}
```

Patterns include:

| Pattern | Matches |
|---|---|
| `0`, `"GET"`, `'a'` | A literal |
| `1..10`, `'a'..'z'` | An integer or `char` range |
| `A \| B` | Either pattern |
| `_` | Anything |
| `name` | Anything, binding it to `name` |
| `.Circle(r)` | An enum variant, binding its fields ([enums](./structs-and-enums#enums)) |
| `some(x)`, `none` | An optional |
| `ok(v)`, `err(e)` | A result ([errors](./errors#handling-both-cases-with-match)) |

A missing case does not compile:

```mk
// verdict: reject E0306
Name(n: int): str {
    return match n { // <- E0306
        0 { "zero" }
        1 { "one" }
    }
}
```

[Patterns](/reference/language/patterns) has every form, and [exhaustiveness](/reference/language/patterns#exhaustiveness) how the compiler decides a `match` is complete.

## defer

`defer` runs a statement when its block exits, by any path. Deferred statements run in reverse order. A `defer` inside a loop body runs at the end of each iteration.

```mk
// verdict: ok
export Main(sys: &System) {
    sys.Print("one\n") else {}
    defer sys.Print("three\n") else {}
    defer sys.Print("two\n") else {}
    // prints one, two, three
}
```

```text
one
two
three
```

A deferred statement cannot `try`, `fail` or `return`, so `else {}` here ignores a print error. The usual use is releasing something: `defer arena.Free()` right after making the arena.

`onerr` is the same as `defer`, but runs only when the block exits with an error. Use it to release something on the failure paths and hand it over on success ([cleanup on the error path](./errors#cleanup-on-the-error-path)).

## return

Every function returns with `return`; a body's last expression is not its value. A function returning `void` may fall off its end. `return` followed by a newline returns nothing, so the value goes on the same line.

## Next

[Functions](./functions).
