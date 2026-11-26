# std.testing

`std.testing` turns a `test` block into a fuzz target or a benchmark. `testing.Fuzz(f)` calls `f` with inputs: saved files under plain `mk test`, mutated ones under `mk test --fuzz`, and `testing.FuzzSys(sys, f)` does the same for an `f` that takes the test's `System`. `testing.Bench(f)` lets `mk bench` time `f`, and `testing.BenchSys(sys, f)` does the same for an `f` that takes the test's `System`. None needs syntax of its own: a test block calls the function, and the `mk` command decides what happens. For ordinary tests, `Assert` and the `test` block are enough; for checking a function's contract with generated arguments, use `mk test --prop` ([contracts and the property tester](../tools.md#7-contracts-and-the-property-tester)).

Read the [`mk test`](../tools.md#mk-test) and [`mk bench`](../tools.md#mk-bench) commands first, and [fuzzing and benchmarks](../tools.md#10-debugging-coverage-fuzzing-and-profiling) for the flags.

```mk
// verdict: parse
import std.testing
```

## Contents

- [The main pieces](#the-main-pieces)
- [Fuzz targets](#fuzz-targets)
- [The mutation loop](#the-mutation-loop)
- [Benchmarks](#benchmarks)
- [Memory and cost](#memory-and-cost)
- [Examples](#examples): [run a fuzz target over saved inputs](#run-a-fuzz-target-over-saved-inputs), [a saved input that fails](#a-saved-input-that-fails), [find a failing input](#find-a-failing-input), [a fuzz target with an external oracle](#a-fuzz-target-with-an-external-oracle), [a benchmark](#a-benchmark), [a benchmark that allocates](#a-benchmark-that-allocates)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Declare a fuzz target | `testing.Fuzz(f)` in a `test` block, `f: fn([]u8)` |
| Declare a fuzz target that needs the `System` (an external oracle, an artifact, the heap) | `testing.FuzzSys(sys, f)` in a `test` block that takes `(sys: &System)`, `f: fn(sys: &System, data: []u8)` |
| Run it over saved inputs | `mk test` |
| Search for failing inputs | `mk test --fuzz <test name> [--fuzztime d]` |
| Declare a benchmark | `testing.Bench(f)` in a `test` block, `f: fn(n: int)` |
| Declare a benchmark that allocates from the heap | `testing.BenchSys(sys, f)` in a `test` block that takes `(sys: &System)`, `f: fn(sys: &System, n: int)` |
| Time the benchmarks | `mk bench [name]` |
| Line coverage of the package | `mk test --cover` ([coverage](../tools.md#10-debugging-coverage-fuzzing-and-profiling)) |

## Fuzz targets

A fuzz target is a function `f: fn([]u8)` that fails by panicking: a failed `Assert`, an overflow, an index out of bounds. One `test` block calls `testing.Fuzz(f)` once, and the test's name names its inputs.

- Plain `mk test` calls `f` once per file in `testdata/fuzz/<test name>/` under the package root, except the `.report` files, in file-name order, with the file's bytes. Before each call it writes `fuzz input <file name>` to the test's output, so a failure names its input.
- With no such directory, or an empty one, `f` is not called and the test passes.
- Outside `mk test`, for example in `mk run`, `Fuzz` calls nothing.
- Give fuzz tests names that are plain file names. Any other name gets a directory of its own, `<name>~<8 hex digits>`, with each character that a Windows file name cannot hold written `_`. That happens to:
  - a name with one of the characters `< > : " / \ | ? *`, or a control character, or one that ends in a space or a dot;
  - a Windows device name such as `CON`, which also gets `_` in front (`_CON~...`);
  - a name that another test of the package shares, ignoring case.

  The hex digits come from the SHA-256 of the module path and the test name: in module `main`, the test `a:b` reads `testdata/fuzz/a_b~977664b1/`. When the plain directory `a_b/` holds files, `mk test` prints a note naming the one it reads.

A function value captures nothing, so `f: fn([]u8)` cannot reach a `System`: it cannot run an external oracle (a reference implementation or a disassembler, through `std.proc`), write a finding's artifact, or allocate from `sys.heap`. A target that needs one is a function `f: fn(sys: &System, data: []u8)`. Its test block takes `(sys: &System)` and calls `testing.FuzzSys(sys, f)`, which calls `f(sys, data)` wherever `Fuzz` calls `f(data)`, in plain `mk test` and in the mutation loop alike.

- The fuzzer's own work, reading the saved inputs and writing the state the driver saves a failing input from, never goes through `sys`: it uses the native `Io` and the process heap, under `mk test --sim` too.
- `f`'s `sys` is the test block's own, under `mk test --sim SEED` too: an oracle it runs is a real process, deterministic only as far as that process is. An `f` that wants a seeded, simulated world builds one with [`std.sim`](sim.md).
- The hang limit counts the whole call of `f`, an oracle's time included.

Commit the `testdata/fuzz/` directories: they are the regression tests the mutation loop leaves behind.

## The mutation loop

`mk test --fuzz <test name>` runs the fuzz target of the test with exactly that name. It starts from the saved inputs (or one empty input), changes them, and calls `f` with each new input until the fuzz time ends: `--fuzztime`, 10 seconds by default.

- The changes are bit flips, byte sets, inserts, deletes, pieces of other saved inputs, and tokens from a dictionary. The dictionary holds the string, byte and integer literals of the test block's file.
- The loop is guided by coverage: an input that reaches a block of the package no earlier input reached is kept, and later inputs are made from it. The loop reads the counters of `mk test --cover`.
- An input is at most 4,096 bytes.
- A run fails when `f` panics, leaks, or exits with any status but 0, and hangs when an input does not return for 10 seconds. Each new failure is saved as `testdata/fuzz/<test name>/<16 hex digits>`, named after the input's SHA-256. A `.report` file beside it gives the failure's signature, the exit status, the seed, the call number and the input. The input is quoted when it is UTF-8 text of at most 256 bytes, else its first 256 bytes are in hex. Then come the last 200 lines of the program's output. A failure with the same signature as a saved one is not saved again.
- After a failure the loop starts over with the next seed. It stops after 20 failing runs.

A saved input stays in the directory, so plain `mk test` calls `f` with it from then on: the failure is a test until it is fixed.

## Benchmarks

A benchmark is a function `f: fn(n: int)` that does its work `n` times. One `test` block calls `testing.Bench(f)`.

- `mk bench` first doubles `n` from 1 until one call takes at least 20 ms (or `n` reaches 2^30). It then times `f(n)` once per repetition on the monotonic clock and repeats until the result is stable, 600 repetitions have run, or the time limit passes. [`mk bench`](../tools.md#mk-bench) describes the statistics and `--baseline`.
- Plain `mk test` calls `f(1)` once, and so does any other run. So `mk test` checks that every benchmark works.
- `mk bench` finds benchmarks by the text `testing.Bench(` or `testing.BenchSys(` in a test block. Import `std.testing` under its own name, and call `Bench` or `BenchSys` directly in the block.

A function value captures nothing, so `f: fn(n: int)` cannot reach a `System`, and without one it has no heap: a hosted program's heap is `sys.heap`, and such an `f` allocates only from a buffer of its own (`std.mem`'s `Fixed`). A benchmark that allocates from the heap is a function `f: fn(sys: &System, n: int)`. Its test block takes `(sys: &System)` and calls `testing.BenchSys(sys, f)`, which calls `f(sys, n)` wherever `Bench` calls `f(n)`. `f` builds its fixture from `sys.heap` in each call, and the time includes it; built once before `f`'s loop of `n`, its cost is spread over the `n` iterations.

## Memory and cost

`Fuzz` and `FuzzSys` read their inputs into memory from the process heap and free them before they return. In plain `mk test` each costs one call of `f` per saved input. `Bench` and `BenchSys` allocate nothing themselves. In any other run `Fuzz` and `FuzzSys` cost one check, `Bench` one call of `f(1)` and `BenchSys` one call of `f(sys, 1)`.

## Examples

Each example is a package directory: the file shown (`port.mk`, `digits.mk`, `header.mk`, `upper.mk`, `sum.mk`, `list.mk`, in order) and, for the fuzz targets, the files under `testdata/fuzz/`.

### Run a fuzz target over saved inputs

```mk
// verdict: ok
import std.testing

// ParsePort reads a decimal port number, 1 to 65535.
ParsePort(b: []u8): ?int {
    if b.Len() == 0 || b.Len() > 5 {
        return none
    }
    var n := 0
    for c in b {
        if c < b'0' || c > b'9' {
            return none
        }
        n = n * 10 + int(c - b'0')
    }
    if n == 0 || n > 65535 {
        return none
    }
    return some(n)
}

// Check is the fuzz target: it fails by panicking.
Check(b: []u8) {
    if n := ParsePort(b) {
        Assert(n >= 1 && n <= 65535)
    }
}

test "parse_port" {
    testing.Fuzz(Check) // calls Check on each file of testdata/fuzz/parse_port/
}
```

With three files in `testdata/fuzz/parse_port/`: `empty` (no bytes), `http-alt` (`8080`) and `too-big` (`65536`):

```text
$ mk test
PASS main "parse_port"
1 passed, 0 failed
```

### A saved input that fails

```mk
// verdict: ok
import std.testing

// Digits reads a decimal number. It has a bug: no limit on the length.
Digits(b: []u8): ?int {
    var n := 0
    for c in b {
        if c < b'0' || c > b'9' {
            return none
        }
        n = n * 10 + int(c - b'0') // overflows past 19 digits: a panic
    }
    return some(n)
}

Check(b: []u8) {
    _ = Digits(b)
}

test "digits" {
    testing.Fuzz(Check)
}
```

With `testdata/fuzz/digits/small` (`42`) and `testdata/fuzz/digits/twenty-nines` (twenty `9`s):

```text
$ mk test
FAIL main "digits": panic (exit 101)
    fuzz input small
    fuzz input twenty-nines
    panic: overflow at digits.mk:11:13
    backtrace:
      Digits (digits.mk)
      Check (digits.mk)
```

The rest of the backtrace and the summary line `0 passed, 1 failed` are cut here. The last `fuzz input` line names the input that failed.

### Find a failing input

```mk
// verdict: ok
import std.testing

// Payload returns the bytes after a three-byte header: "MK" and a length.
// It has a bug: it trusts the length.
Payload(b: []u8): ?[]u8 {
    if b.Len() < 3 || b[0] != b'M' || b[1] != b'K' {
        return none
    }
    n := int(b[2])
    return some(b[3..3 + n]) // out of bounds when the length lies
}

Check(b: []u8) {
    _ = Payload(b)
}

test "payload" {
    testing.Fuzz(Check)
}
```

With no saved inputs, plain `mk test` passes. The mutation loop finds the bug: `b'M'` and `b'K'` are in its dictionary, since they are literals of the file.

```text
$ mk test --fuzz payload --fuzztime 5s
FAIL main "payload": fuzz: 20 failing runs, 1 input saved to testdata/fuzz/payload/
    seed 1: panic after 18 calls: saved testdata/fuzz/payload/1b51a40d66121bdc
    seed 2: panic after 1 call: the failure of testdata/fuzz/payload/1b51a40d66121bdc (panic: bounds at header.mk:11:17), not saved again
    seed 3: panic after 151 calls: the failure of testdata/fuzz/payload/1b51a40d66121bdc (panic: bounds at header.mk:11:17), not saved again
```

Seeds 4 to 20 each print a line like those of seeds 2 and 3, and a last line counts the calls; those lines are cut here. The saved input is the six bytes `4d4bb2df8196`: `MK`, then a length of 178 that the input does not have. Every later plain `mk test` replays it:

```text
$ mk test
FAIL main "payload": panic (exit 101)
    fuzz input 1b51a40d66121bdc
    panic: bounds at header.mk:11:17
```

The backtrace and the summary line that follow are cut here.

### A fuzz target with an external oracle

```mk
// verdict: ok
import std.proc
import std.testing

// Upper is the function under test: ASCII letters to upper case.
Upper(b: []u8, out: mut []u8) {
    for i in 0..b.Len() {
        c := b[i]
        out[i] = if c >= b'a' && c <= b'z' { c - 32 } else { c }
    }
}

// Check compares Upper with tr, the oracle, run through sys.io.
Check(sys: &System, b: []u8) {
    var a := Arena.New(sys.heap).Must()
    defer a.Free()
    out := a.Slice[u8](b.Len()).Must()
    Upper(b, out)
    args := ["tr", "a-z", "A-Z"]
    r := proc.Run(&a, sys.io, args[..], b, 1 << 16) else {
        return // no tr on this machine: nothing to compare with
    }
    Assert(r.stdout.Len() == out.Len())
    for i in 0..out.Len() {
        Assert(r.stdout[i] == out[i])
    }
}

test "upper" (sys: &System) {
    testing.FuzzSys(sys, Check) // calls Check(sys, b) on each saved input
}
```

`Check` allocates its output from `sys.heap` and runs the oracle through `sys.io`, neither of which a `fn([]u8)` can reach. With no saved inputs, plain `mk test` passes; `mk test --fuzz upper` runs `tr` on every input it makes.

### A benchmark

```mk
// verdict: ok
import std.testing

// SumDigits adds the decimal digits of x.
SumDigits(x: int): int {
    var s := 0
    var v := x
    for v > 0 {
        s += v % 10
        v /= 10
    }
    return s
}

Run(n: int) {
    for i in 0..n {
        Assert(SumDigits(i * 9) % 9 == 0) // a multiple of 9 has a digit sum divisible by 9
    }
}

test "sum_digits" {
    testing.Bench(Run) // mk bench times Run(n); mk test calls Run(1)
}
```

```text
$ mk test
PASS main "sum_digits"
1 passed, 0 failed
```

`mk bench` prints the median time per call, its confidence interval and the number of repetitions. These differ on every machine, so they are not shown here.

### A benchmark that allocates

```mk
// verdict: ok
import std.testing

// Sum adds 0 through n-1 from a list it builds in an arena.
Sum(sys: &System, n: int): OutOfMemory!int {
    var a := try Arena.New(sys.heap)
    defer a.Free()
    var xs := List[int].New(&a)
    for i in 0..n {
        try xs.Push(i)
    }
    var s := 0
    for x in xs {
        s += x
    }
    return s
}

Run(sys: &System, n: int) {
    Assert(Sum(sys, n).Must() == n * (n - 1) / 2)
}

test "sum_list" (sys: &System) {
    testing.BenchSys(sys, Run) // mk bench times Run(sys, n); mk test calls Run(sys, 1)
}
```

```text
$ mk test
PASS main "sum_list"
1 passed, 0 failed
```

## API

<!-- api:begin -->
Stability: Stable.

### `Bench`

```
export Bench(f: fn(n: int))
```

Bench makes f the benchmark of the test block that calls it. f(n) does
its work n times. Under `mk bench` it doubles n from 1 until one call
takes at least 20 ms (n stops at 2^30), then times f(n) once per
repetition on the monotonic clock and reports each time on standard
error, from which `mk bench` computes its statistics. Anywhere else,
plain `mk test` included, it calls f(1) once, so a test run checks that
the benchmark works. `mk bench` finds a benchmark by the text
`testing.Bench(` or `testing.BenchSys(` in its test block, so import
std.testing under its own name. A benchmark that allocates takes a
System: see BenchSys.

Bench itself allocates nothing; O(the repetitions' calls of f).

### `BenchSys`

```
export BenchSys(sys: &System, f: fn(sys: &System, n: int))
```

BenchSys is Bench for a benchmark that needs the System, to allocate
from sys.heap or to reach sys.io: the test block passes its own sys,
and BenchSys calls f(sys, n) wherever Bench would call f(n), so
`mk bench` times f(sys, n) and any other run calls f(sys, 1) once. f
builds its fixture in each call, and the time includes it; built once
before f's loop of n, its cost is spread over the n iterations.

BenchSys itself allocates nothing; O(the repetitions' calls of f).

### `Fuzz`

```
export Fuzz(f: fn([]u8))
```

Fuzz makes f the fuzz target of the test block that calls it, and runs
it. Plain `mk test` calls f once per file of testdata/fuzz/<test name>/
except the .report files, in name order, with the file's bytes, and
names each file in the test's output before the call.
`mk test --fuzz <test name>` instead calls f on inputs it mutates,
guided by the blocks they reach, until the fuzz time ends or 20 runs
have failed. It saves an input that panics, leaks, hangs or exits
non-zero into that directory, one input per distinct failure. f fails
by panicking. Under plain `mk test` with no corpus files, and in any run
outside `mk test`, f is not called. A target that needs the System, to
run an external oracle or allocate from the heap, takes one: see
FuzzSys.

It allocates its inputs from the process heap and frees them before it
returns; O(the corpus) in plain mode.

### `FuzzSys`

```
export FuzzSys(sys: &System, f: fn(sys: &System, data: []u8))
```

FuzzSys is Fuzz for a target that needs the System, to reach sys.io (to
run an external oracle through std.proc, or to write a finding's
artifact) or to allocate from sys.heap: the test block passes its own
sys, and FuzzSys calls f(sys, data) wherever Fuzz would call f(data).
The fuzzer's own work, reading the corpus and writing the state file
the driver saves a failing input from, runs on the native Io and the
process heap, never on sys, so it is the same under `mk test --sim`.
f's sys is the test block's own, under `--sim` too: an oracle it runs
is a real process, deterministic only as far as that process is, and
an f that wants a simulated world builds one with std.sim. The hang
limit counts f's whole call, an oracle's time included.

It allocates its inputs from the process heap and frees them before it
returns; O(the corpus) in plain mode.

<!-- api:end -->
