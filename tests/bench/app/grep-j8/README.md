# app/grep-j8: app/grep's re variant, searched in parallel

An `app` row about concurrency (tests/bench/README.md "Applications"). It is `app/grep`'s `re` variant with mgrep's `-j 8`: the same tree, the same pattern, the same output. A concurrency row is a row of its own, since `concurrency` is set per row, and each twin opens with this row's header, so the row shares no source file with `app/grep`. Every twin below is checked against this file and `app/grep`'s before its number counts.

## The task

- **Command line:** `grep -c -j 8 --path-separator / -i 'n[aeiou]+dle|omega|s?igma' DIR`, mgrep's own options. tools/bench runs it with `{data}` as `DIR`.
- **Input:** `app/grep`'s tree, `tests/bench/data/grep-tree-100mb/` (gen kind `tree`, seed 5, bytes 100,000,000). The size is provisional: `verify -publish-size` on a quiet machine fixes it.
- **The work, every step:** `app/grep`'s README steps 1 to 8, for the `re` pattern, with this difference: the walk lists every file first, in mgrep's order, and then 8 threads of control (the language's model, below) search the files, each file by one of them. Each file's count is kept with its index, and the output is printed in walk order, whatever order the files finish in.
- **Output:** `app/grep`'s `re` output, byte for byte: 14,967 lines.

## The check

`"digest": true`: the SHA-256 of the standard output, CR LF read as LF, with the tree's path written back as `$DATA`. It equals `app/grep`'s `re` checksum.

## The cells

| Language | Kind | Model | Program | Libraries |
|---|---|---|---|---|
| Mk | `std` | tasks, M:N on 8 carriers (`MK_CARRIERS=8`) | mgrep (`Dogfoods/mgrep`, unchanged), `-j 8`: `src/work`'s feeder task hands file indexes to 8 search tasks through a `Chan`, a `sync.Semaphore` bounds the open files at 8, each task searches in its own arena and sends a heap-owned report back through a second `Chan`, and the main task prints the reports in file order, holding early ones in a `HeapMap` | std only |
| Rust | `eco` | OS threads | `rs/` (a crate): `app/grep`'s Rust program for `re`, with 8 `std::thread::scope` workers that take file indexes from an `AtomicUsize`; the results are sorted by index and printed | regex 1.12.3 and walkdir 2.5.0, from the same `cargo vendor` tree as `app/grep` (`crates/regex-1.12.3-walkdir-2.5.0`) |
| Go | `std` | goroutines, `GOMAXPROCS=8` | `testdata/grepj8.go`: `app/grep`'s Go program for `re`, with 8 worker goroutines that take file indexes from a channel and write each count into the file's slot; printed after the workers finish | std only |
| C# | `std` | OS threads | `grepj8.cs`: `app/grep`'s C# program for `re`, with 8 `System.Threading.Thread` workers claiming file indexes via `Interlocked.Increment` into the results array; printed in walk order after workers join | std only |

- **Skipped:** C and Zig, which have no regex in their std (`app/grep`'s C cell is `lit` only, and its Zig cell `lit` and `ci`).
- **Environment:** the concurrency setup: every program gets the affinity mask `0x5555`, one logical CPU on each of 8 physical cores (Windows numbers SMT siblings next to each other, tests/bench/README.md "Compile speed"; `0xff` would be 4 cores with both siblings of each), as the 8-thread rows and the compile tables use, and the thread count 8 (`GOMAXPROCS` and `MK_CARRIERS`); the twins take theirs from `-j 8`.
- **Columns:** `mk`, `rust`, `rust-ovf`, `go`. The `eco` cells are printed "eco (not classed)".
- **The defect line:** the Mk cell against the fastest `std` cell of the other languages (Go). The function a profile of the Mk cell names is the std function the line is about.
- **Bans:** as `app/grep`'s.
