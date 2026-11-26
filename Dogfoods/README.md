# Dogfoods

Eighteen complete programs written in Mk: command-line tools, servers, a scripting language, a ray tracer and a kernel. Each is a real package with a command line, real input formats, error messages and exit codes, and its own tests. Read them to see how Mk code looks at the scale of a few thousand lines, and how the standard library fits together.

## The programs

| Program | What it does | What it shows in Mk |
|---|---|---|
| [`bigcalc`](bigcalc) | Arbitrary-precision RPN calculator, plus certificate, key, signature and encryption tools | `std.big`, `std.crypto`, `std.x509`; an arena per input line that is kept only when the whole line succeeds |
| [`chat`](chat) | Multi-room chat server with a line protocol and a WebSocket front end | Tasks and channels, `std.http` and `std.websocket`, reference-counted broadcast frames, slow-client back-pressure, cancellation |
| [`cron`](cron) | Scheduler daemon with time zones | `std.time` zones and DST, process groups and timeouts in `std.proc`, an advisory lock, a simulated clock |
| [`fetch`](fetch) | HTTP/1.1, HTTP/2, HTTP/3 and WebSocket client | `std.http`, `std.quic`, `std.tls`, `std.websocket`, parallel transfers, Alt-Svc and fallback |
| [`relay`](relay) | Loopback reverse proxy and load generator | HTTP/1.1, HTTP/2, HTTP/3, bounded forwarding, cancellation and simulated UDP loss |
| [`fm`](fm) | Two-pane terminal file manager and pager | `std.term` raw mode, mouse and bracketed paste, a shell on a pseudo-terminal, grapheme-aware wrapping |
| [`imgproc`](imgproc) | Image pipeline over PNM files | SIMD kernels at 128, 256 and 512 bits chosen at run time, parallel tiles, byte-identical output on every path |
| [`kernel`](kernel) | Freestanding x86-64 kernel booted in QEMU | `x86_64-freestanding`, PVH boot without an assembly file, naked interrupt stubs, `std.mmio`, a frame allocator |
| [`kvd`](kvd) | Key-value server and client with a write-ahead log | Crash-safe files, memory-mapped snapshots, `std.sim` fault injection and replay, a Unix-socket admin channel |
| [`libprobe`](libprobe) | Library loader and export lister, in the manner of `ldd` and `dumpbin /exports` | `std.dl`, a PE export-table reader, per-target modules, `links` in `pkg.mk` |
| [`lox`](lox) | The Lox scripting language: compiler, bytecode VM, garbage-collected heap, REPL | Arena ASTs linked by index, a mark-and-sweep heap, native functions through `gen dyn.Of` |
| [`mgrep`](mgrep) | Recursive parallel search, in the manner of ripgrep | `gen regex.Matcher`, vector byte search, worker tasks with output kept in file order |
| [`notes`](notes) | HTTP JSON notes service over SQLite | `gen json.Codec`, `http.Route` tables, C sources of its own behind one `unsafe` module |
| [`orderbook`](orderbook) | Limit order book matching engine | B-tree price ladders of FIFO queues, integer prices, CSV files saved by write, sync and rename |
| [`pack`](pack) | Archiver with signing and encryption | Ed25519 signatures, X25519 sealing in fixed-size chunks, streaming zstd, gzip and zlib |
| [`procman`](procman) | Process manager and task runner for Procfiles | One task per process, restart back-off, `proc.Supervise`, a status port |
| [`raft`](raft) | A five-node Raft cluster over UDP with a linearizability checker | `std.sim` partitions, loss and corruption rolled from a seed; recordings of failing seeds |
| [`textkit`](textkit) | Unicode text tools: wc, grep, sort, norm, fold, words, width, convert | `std.text` segmentation and normalization, WHATWG encodings, streaming UTF-8 checks |
| [`tracer`](tracer) | Path tracer and function plotter | A deterministic per-pixel sampler, so the image is the same on any number of carriers; a uniform grid for meshes |

Each folder's `README.md` shows how to run the program, its main design points and its limits.

## Building and running one

Each program is an ordinary Mk package. From its folder, `mk run` builds it and runs it; arguments for the program follow `--`:

```sh
cd Dogfoods/bigcalc
mk run -- calc "2 127 ^ 1 -"
```

```text
170141183460469231731687303715884105727
```

`mk build` writes the executables to the current directory (or to `-o`), and `mk test` runs the `test` blocks in its sources. The kernel builds for `x86_64-freestanding` only and runs in QEMU; see [kernel/README.md](kernel/README.md).

## Layout

```text
Dogfoods/
  README.md        this file
  COVERAGE.md      which library lines each program uses and which tests execute them (generated)
  EXEMPT.md        library lines no program can exercise, and where they are tested instead
  <name>/
    pkg.mk         the package manifest
    README.md      what the program does, how to run it, its design and limits
    src/           its modules
    cases/         its tests
```

Each README ends with a `## Section 18 lines` block: the lines of the library surface in `doc/reference.md` that the program uses. `tools/dogfood` reads that block.

## Tests

The tests are cases: each runs the program once and compares what it does with the expected result. From the repository root:

```sh
go run ./tools/dogfood run                    # every program, every target and profile
go run ./tools/dogfood run -only bigcalc      # one program (or a comma-separated list)
go run ./tools/dogfood list                   # each program, its size and the library lines it lists
go run ./tools/dogfood cover                  # writes COVERAGE.md
```

`run` also takes `-target` (`x86_64-windows`, `x86_64-linux`, `x86_64-freestanding`) and `-profile` (`dev`, `release`). The Linux cases run through WSL on a Windows host, and the kernel's cases boot it in QEMU (without QEMU, the kernel's ELF file is only checked).

## Cases

A case is a set of files `cases/<case>.<kind>`, a case name being letters, digits, `-` and `_`. Fixtures go in subdirectories of `cases/`.

| Kind | Holds |
|---|---|
| `.args` | The program's arguments, one per line, spaces kept |
| `.in` | Standard input (none: empty) |
| `.env` | `NAME=value` lines added to the environment |
| `.out`, `.err` | Expected standard output and error, compared byte for byte (none: empty) |
| `.code` | Expected exit status (none: 0) |
| `.bin` | Which of the package's bins to run (none: the first) |
| `.targets` | Targets and profiles the case runs on, separated by spaces (none: all) |
| `.sim` | `seed N` or `seeds A..B` lines: the program runs once per seed with `--seed N`; `mode test` runs `mk test --sim N` instead |
| `.pty` | A terminal script: the program runs on a pseudo-terminal |

A file `<case>.<target>.<profile>.<kind>`, `<case>.<target>.<kind>` or `<case>.<profile>.<kind>` overrides `<case>.<kind>` where outputs differ between targets or profiles; the most specific one wins.

Each case runs in an empty working directory of its own. In `.args` and `.env`, `$CASES` stands for the `cases/` directory, `$WORK` for the working directory, and `{tmp}` for a fresh scratch directory on the target's own file system (for Unix sockets, locks and permission bits). The harness writes those paths back as `$CASES`, `$WORK` and `{tmp}` in the output before comparing, and `$SEED` in an expected file is the run's seed. A case has 30 seconds, and one program's cases have 3 minutes together per target and profile.

### Terminal cases

A case with a `.pty` file runs its program on a pseudo-terminal through `ptyrun` (`tools/dogfood/ptyrun`). The case is judged by its exit code and by a transcript the program writes to `$WORK/transcript`, compared with `.out`; what the program draws on the terminal is not compared. The script has one step a line (blank lines and `#` lines are skipped):

| Step | Does |
|---|---|
| `size C R` | The terminal's size when it opens (default 80 by 24) |
| `keys TEXT` | Types TEXT; escapes `\e`, `\r`, `\n`, `\t`, `\\`, `\xHH` |
| `key NAME...` | Types named keys: `enter`, `tab`, `esc`, arrows, `home`, `end`, `pgup`, `pgdn`, `f1` to `f12`, `ctrl-a` to `ctrl-z`, and so on |
| `paste TEXT` | Types TEXT inside bracketed-paste markers |
| `resize C R` | Resizes the terminal |
| `expect N` | Waits until the transcript holds N complete lines |
| `sleep MS` | Waits MS milliseconds, for a timing the program defines |

### The kernel's cases

A kernel case boots the ELF in `qemu-system-x86_64` with COM1 connected to the harness over TCP. `.in` is typed into the serial port a line at a time, the serial output is compared with `.out`, and `.code` is QEMU's exit status, `(code << 1) | 1` for the code the kernel writes to port `0xf4`.
