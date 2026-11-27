---
description: test blocks beside the code, mk test running each test in its own process, contracts with requires, ensures and invariant, property testing, fuzzing, benchmarks and deterministic simulation.
---

# Testing and contracts

## Tests

A `test` block is a named test, written beside the code it tests. `mk test` builds every test in the package and runs each in its own process, so a panic fails one test and not the run.

```mk
// verdict: ok
Clamp(x: int, lo: int, hi: int): int
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return hi
    }
    return x
}

test "clamp keeps a value inside the range" {
    Assert(Clamp(5, 0, 3) == 3)
    Assert(Clamp(-1, 0, 3) == 0)
}

test "a list on the heap" (sys: &System) {
    a := sys.heap.Alloc()
    var xs := HeapList[int].New(a)
    defer xs.Free(a)
    try xs.Push(a, 4)
    Assert(xs.Len() == 1)
}
```

```text
$ mk test
PASS src "clamp keeps a value inside the range"
PASS src "a list on the heap"
2 passed, 0 failed
```

- `Assert(cond)` panics when `cond` is false, which fails the test.
- A test may take `(sys: &System)` to get memory and I/O. It may use `try`: an error fails it.
- A test's heap reports leaks.

## Contracts

`requires`, `ensures` and a struct's `invariant` are contracts:

- `requires` states what the caller must pass; `ensures` states what the function returns, with `result` naming the return value.
- They are checked in development and test builds and under `--profile checked`, and left out of release builds.
- A false contract panics with the message `contract`.

Contracts document a function in a form the compiler checks, and they drive the property tester.

## Property testing

`mk test --prop` calls each function that has a contract with generated inputs, and reports any input that breaks it, shrunk to a single call. `Clamp` above is tested this way with no test written for it:

```text
$ mk test --prop
PASS src "prop Clamp"
    110 cases, 89 discarded by requires or invariant
1 passed, 0 failed (prop seed 30798615365709680)
```

Inputs that break `requires` are discarded, so the cases that run are the ones the function promises to handle. The seed reproduces a run. [Contracts and the property tester](/tools/#7-contracts-and-the-property-tester) has the details.

## Fuzzing and benchmarks

[`std.testing`](/reference/std/testing) registers fuzz targets and benchmarks from `test` blocks. `mk bench` runs the benchmarks and prints their statistics.

## Deterministic simulation

Every blocking operation goes through `Io`, so a test can run a program under the simulated `Io` of [`std.sim`](/reference/std/sim). It replays a whole program, its disk, network and clock included, from a seed, and can inject faults, such as a disk with no space left or a connection reset by its peer. A failure found once replays exactly. The cookbook shows [testing crash safety under simulation](/examples/cookbook#test-crash-safety-under-simulation).

## Next

[Compile-time code](./compile-time).
