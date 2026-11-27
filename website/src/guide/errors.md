---
description: Optionals instead of null, errors as values in the result type E!T, fail, try, else and match, cleanup with onerr, how errors leave Main, and panics for bugs.
---

# Optionals and errors

Mk has no null and no exceptions. A value that may be missing is an optional, `?T`. A function that can fail says so in its result type, `E!T`, and the caller handles the error where it can see it. A bug, such as an index out of bounds, is a panic.

## Optionals

`?T` holds `some(x)` or `none`. A reference `&T` always points at something; `?&T` is the nullable one, still the size of a pointer.

```mk
// verdict: ok
Find(xs: []int, x: int): ?int {
    for i, y in xs {
        if y == x {
            return some(i)
        }
    }
    return none
}

Lookups(xs: []int): int {
    if i := Find(xs, 7) { // runs when the value is some, with i bound to its payload
        return i
    }
    j := Find(xs, 3) else { // runs the block on none; its last line is the value
        -1
    }
    return j + Find(xs, 0).Must() // Must panics on none
}
```

| Form | Does |
|---|---|
| `if x := o { }` | Runs the block when `o` is `some`, with `x` bound |
| `x := o else { default }` | Takes the value, or the block's last line on `none` |
| `for x := o { }` | Loops while `o` gives `some(x)` |
| `match o { some(x) { } none { } }` | Handles both cases |
| `o.Must()` | Takes the value, or panics on `none` |
| `o.IsNone()` | Tests for `none` |

Optionals have no `==`: test one with these forms.

## Declaring errors

You declare errors with `error` and combine them into sets with `|`. An error may carry plain data.

```mk
// verdict: ok
error NotFound(id: i64)
error Denied

type LoadError = NotFound | Denied

Load(id: i64): LoadError!str {
    if id < 0 {
        fail Denied            // return an error
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return "data"
}
```

- `fail E` returns an error from the function.
- A signature always names its error set; Mk never infers one.
- `type LoadError = NotFound | Denied` names a set so several functions can share it. The library's errors, such as `OutOfMemory`, `WriteError` and `fs.Error`, combine with yours the same way.

## Handling errors

The caller has four ways to handle a result, shown here against `Load` above:

```mk
// verdict: ok
error NotFound(id: i64)
error Denied

type LoadError = NotFound | Denied

Load(id: i64): LoadError!str {
    if id < 0 {
        fail Denied
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return "data"
}

Length(id: i64): LoadError!int {
    s := try Load(id)          // on error, return it; Load's set must fit in ours (E0801)
    return s.Len()
}

Quiet(id: i64): int {
    s := Load(id) else e {     // handle the error here; e is bound to it
        _ = e
        ""
    }
    return s.Len()
}

Describe(id: i64): str {
    return match Load(id) {    // or handle both cases with match
        ok(s) { s }
        err(e) {
            match e {
                NotFound { "missing" }
                Denied { "denied" }
            }
        }
    }
}

export Main(sys: &System): LoadError|WriteError!void {
    try sys.Print("{a} {b} {c}\n", (a: Quiet(-1), b: Describe(0), c: try Length(5)))
    // prints 0 missing 4
    _ = try Length(0) // leaves Main with the error: prints error: main.NotFound(id: 0), exits 1
}
```

```text
0 missing 4
error: main.NotFound(id: 0)
    at main.mk:12:9
    at main.mk:18:10
    at main.mk:45:9
```

| Form | When to use it |
|---|---|
| `x := try F()` | Pass the error up. The callee's error set must fit inside the caller's (E0801) |
| `x := F() else e { ... }` | Handle it here: recover with a value, or leave with `return`, `break` or `continue` |
| `match F() { ok(v) { } err(e) { } }` | Handle both cases, and match on which error it was |
| `F().Must()` | Take the value, or panic. Only where an error would be a bug |

### Handling both cases with match

`match` on a result names the value with `ok(v)` and the error with `err(e)`. Matching on `e` names each error in the set, and the compiler checks that every one is covered. Leave out a case and the program does not compile.

### Results must be used

A result must be used (E0209). `_ = F()` discards a value, but never an error: a call that can fail must have its error handled or passed on (E0802).

```mk
// verdict: reject E0802
error Bad

Save(): Bad!void {
    fail Bad
}

Forget() {
    Save() // <- E0802
}
```

## Cleanup on the error path

`onerr s` runs `s` when its block exits with an error. `defer s` runs at every exit. They share one stack: at an exit, the pending statements run in reverse order of declaration.

```mk
// verdict: ok
error Bad

Step(sys: &System, fails: bool): Bad!void {
    defer sys.Print("defer 1\n") else {}
    onerr sys.Print("onerr 2\n") else {}
    defer sys.Print("defer 3\n") else {}
    if fails {
        fail Bad
    }
    sys.Print("body done\n") else {}
}

export Main(sys: &System): WriteError!void {
    Step(sys, false) else {}
    try sys.Print("--\n")
    Step(sys, true) else e {
        try sys.Print("caller saw {e}\n", (e))
    }
}
```

```text
body done
defer 3
defer 1
--
defer 3
onerr 2
defer 1
caller saw Bad
```

The typical use is a resource that is released on failure and handed to the caller on success, such as a file being written: [write a file and report a close error](/examples/cookbook#write-a-file-and-report-a-close-error) shows it. A `return` is never an error exit, even in a function that can fail.

## Errors from `Main`

When an error leaves `Main`, it is printed to standard error and the process exits with status 1. A development build records each `try` the error passed through, and prints that path under the error, as in the output above. [Errors from `Main`](/reference/language/errors#errors-from-main) gives the exit status of each result type.

## Panics

A panic is not an error. It reports a bug: a state the program should never reach. It prints a report to standard error and ends the process with status 101. Nothing unwinds: no `defer` or `onerr` runs, and no caller can catch it.

```mk
// verdict: ok
export Main(sys: &System): int {
    xs := [1, 2, 3]
    i := sys.args.Len() + 5
    return xs[i]
}
```

```text
panic: bounds at main.mk:5:12
backtrace:
  Main (main.mk)
```

Overflow, an index out of bounds, division by zero, a failed `Assert` or `Must`, a failed contract and stack exhaustion all panic, and `Panic("message")` panics on purpose. [Panics](/reference/language/errors#panics) lists every message. `mk query panics` lists every place in your code that can panic.

## Designing error sets

- Name the condition, not the operation: `NotFound`, `Denied`, `TooLong`, not `LoadFailed`.
- Put in the payload what the caller needs to act or to report, such as the id that was missing.
- Give each function the smallest set it can fail with, so a caller can handle every case.
- Handle an error where something useful can be done about it: retry, use a default, report it. Elsewhere, pass it on with `try`.
- Use `Must` and `Assert` for conditions only a bug can break, and errors for everything the outside world can cause.

## Next

[Memory and ownership](./memory).
