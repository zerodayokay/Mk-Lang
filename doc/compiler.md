# The compiler

This guide is for people who work on the compiler. It describes how `mk` turns a package into an executable, how the Go packages under `internal/` divide the work, how incremental builds stay correct, and how to test a change. To use Mk, you need none of it: start with the [tour](overview.md). To build the repository and send a change, read [CONTRIBUTING.md](../CONTRIBUTING.md).

`mk` is one binary, written in Go with the standard library only: no cgo, no `unsafe`, no third-party modules. It emits textual LLVM IR, runs `clang` on that IR (never on C source) to produce objects, and runs `lld` to link them. The standard library, runtime included, is Mk source embedded in the binary.

## 1. Principles

| Principle | Consequence |
|---|---|
| One producer per semantic fact | Each fact has one package that computes it ([one producer per fact](#4-one-producer-per-fact)). Nothing else recomputes it |
| Fixed stages per module | No general query engine. [The stage rule](#3-the-stage-rule) keeps compile-time evaluation out of cycles |
| Hash at boundaries only | Items, MIR, const values and shapes are hashed. Individual queries are not |
| One-shot is the common case | `mk check` runs as a process. A cold process with a warm disk cache must be fast; there is no daemon |
| Diagnostics are data | The checker produces structured diagnostics. Text is one rendering, `--json` another |
| Output is deterministic | The same inputs give the same bytes ([determinism](#9-determinism)) |

## 2. Pipeline

Modules are compiled in import order. Modules whose imports are all done run in parallel, and within a module, files and functions run in parallel where the stage allows.

| # | Stage | Package | Output | Error codes |
|---|---|---|---|---|
| 1 | Load | `driver` | The module graph and source files (line endings normalized) | E0202, E0205 |
| 2 | Parse | `syntax` | A syntax tree and a token list that keeps comments and whitespace, per file | E01xx, E12xx |
| 3 | Resolve | `resolve` | What each name means, per module | E02xx |
| 4 | Generate | `gen` | The items `gen` lines emit, parsed and added to the module; resolve runs again over them | E0903 to E0906 |
| 5 | Signatures | `check` | Types, signatures with flows, shapes, the impl table | E03xx, E07xx, E0907, E0908 |
| 6 | Bodies | `check`, then `lower` | Typed bodies, then MIR per function, test and const initializer | E03xx, E07xx to E11xx |
| 7 | Ownership | `borrow` | Accept, or diagnostics on moves, linear values and loans | E04xx to E06xx |
| 7b | Interface | `iface` | The module's interface and every item's read set, written to the cache | |
| 8 | Instances | `mono` | Monomorphic MIR from the roots (`Main`, exports, tests), deduplicated by content | E0708 |
| 9 | Code generation | `codegen` | LLVM IR text per object pack | |
| 10 | Native | `toolchain` | Objects from `clang`, then the executable from `lld` | |

Some name errors need types, so a few E02xx codes come from stages 5 and 6. C imports (`import c.x`) are translated before stage 2 of the package's own modules, by running Clang ([C import](#7-generators-and-c-import)).

`mk check` runs stages 1 to 7b, then stage 8's instance-limit walk, which keeps no bodies. So `check` and `build` accept the same programs. It emits no code, which makes it the fastest loop. Only the earliest failing stage's diagnostics are reported, with the later-stage errors they name as their cause.

Before stages 3 to 7 of a module, the driver looks the module up in the cache. On a hit it loads the interface and skips them. On a miss it runs them, and reuses each function's cached result when that function's own key hits.

## 3. The stage rule

Stage 5 may run the evaluator only on bodies from strictly lower modules, which have already passed stage 7. An evaluation that would enter a body of the same module, or read a layout of the same module, stops with E0908 and a trace. The rule is enforced on the evaluation, not the expression, so it also covers entry through an imported generic or a `fn` value.

A same-module `const` that a type needs is evaluated at stage 5 from its initializer, provided it enters no same-module body. The module's other `const` and `var` initializers are evaluated after stage 7. Within a module, a const that reads itself is E0909, and a layout cycle is E0907.

`check` calls the evaluator only through an interface in `types`, which the driver implements with `eval`. That interface is the one place in the code where the stage order is visible.

## 4. One producer per fact

| Fact | Producer | Consumers |
|---|---|---|
| Syntax and trivia | Parser | Formatter, `mk fix`, resolver |
| What a name means | Resolver | Everything |
| Type of an expression | Checker, stage 6 | MIR builder, `mk query` |
| Size, alignment, ABI class, kind, `Send`, `Share` | Shape pass, stage 5 | Checker, loans, code generation, interfaces |
| Automatic result source | Shape pass, stored in the interface | Loans |
| Move, linear and initialization states | Move pass on MIR | Diagnostics |
| Loans and conflicts | Loan pass on MIR | Diagnostics, `mk query` |
| Const values | Evaluator | Signatures, code generation |
| Instances | Collector | Code generation |
| Fixes | Fix engine, on the error path only | `mk fix`. Fixes are never cached, because they may read a callee's body |

## 5. Intermediate representations

| IR | Purpose |
|---|---|
| Syntax tree | Typed tree plus a token list that keeps trivia, so the formatter and fixes can print it back |
| MIR | A typed control-flow graph. The input to moves, loans, the evaluator, instances and code generation. `defer` and `onerr` code is copied onto each exit edge |
| LLVM IR | Text, lowered from MIR, given to `clang` |

MIR is frozen per compiler version. Evaluation fuel is counted in MIR steps, and a test pins the step counts, so a compiler change cannot alter a fuel verdict silently.

### The evaluator

The evaluator (`eval`) interprets MIR for `const` values, generators and the REPL.

- **Memory** is a byte array per allocation in the target's layout; a pointer is an allocation and an offset. Raw access is allowed inside allocations the evaluation made. Reading another `const` gives a read-only copy.
- **Refused**: I/O, system calls, bodiless `extern` calls, `var` globals, and reading a pointer's bytes as an integer. Each is E0901.
- **Floats** give a canonical NaN from arithmetic. Math functions and float-to-text are the same Mk code in `std` that runs at run time; the host's libm and formatting are never used.
- **Limits**: fuel counts MIR statements and terminators, plus one step per 16 bytes moved, copied or allocated. Frames live on the heap, so host recursion cannot overflow; one evaluation's frames may hold 1 MiB, and past that it panics with `stack limit`.
- **Records**: each evaluation records the hash of every body it entered, every const and layout it read, every `Target` field it read, and every query with its answer, "none" included. So an added `impl` reruns an evaluation that saw its absence.

## 6. Incremental builds

### Interfaces

Each module's interface has three parts, each read whole:

| Part | Holds | Changes on |
|---|---|---|
| API | Exported names, signatures with flows, exported fields, const value hashes, the impl table | A public edit |
| Shape | One record per exported type: size, alignment, ABI class, kind, `Send`, `Share`. A generic type's fields are terms over its parameters, so an importer can compute `Wrap[Foo]` | A private edit that changes one of these |
| Body store | MIR of generics, inline candidates and anything evaluated, keyed by hash | A body edit |

Interfaces are shallow: a body edit in module `b` does not rewrite module `a`'s interface. Every function records the items it read, with the hash of the part it read, and its negative reads, such as "no item `X` in module `m`" or "no `impl I for T`".

### What an edit invalidates

| Edit | Rechecks | Recompiles (dev) | Why it stops there |
|---|---|---|---|
| Comment, whitespace | Nothing, unless a generator read that doc comment | Functions whose line or column moved | Hashes are over tokens. Line tables are in every profile, and a warm build must equal a cold one byte for byte |
| Body of a non-generic function | That function | That function | No inlining in dev |
| Body of a generic | That function | Its instances | Instances are keyed by their substituted MIR |
| Body that ran in a const evaluation | That evaluation | Dependents, only if the value changed | Values are compared |
| Body that ran in a generator | That generator | Items whose hash changed | Per-item hash |
| Private field, shape unchanged | The module, and importers whose loan check walked that field | The same | Importers read shapes, not fields |
| Private field, shape changed | Importers that use the type | Everything that touches the type | An ABI change recompiles every caller |
| Exported signature | Functions that read that item | The same | Per-item read sets |
| Exported item or `impl` added | Functions with a negative read of it | The same | Negative reads |
| `pkg.mk` | What read the changed field | The same | Read sets |
| Compiler version | Everything | Everything | The build ID is in every key |

Whether a struct has a private field at all is part of its API, because a construction must name every field. So adding the first private field, or removing the last, rechecks importers that construct or read the struct.

Release builds inline across functions, so a body edit there also recompiles the code that inlined it. Acceptance is the same in every profile.

### Instances

An instance is keyed by its MIR after substitution, plus the layouts, values and impl symbols it bakes in, the target and the profile. It belongs to no module. Its symbol is named by its generic and arguments, so a callee's body edit does not rename it for callers.

A `fn` literal passed as a `const` argument enters the key as the hash of its MIR, never as a call-site name. Identical comparators therefore share one instance, and a line added above them changes no key. E0708 bounds instantiation at a depth of 64 along one chain and 100,000 instances per build root.

`Print` and `Arena.Fmt` are declared in Mk, but the compiler writes their body per call: it is sized by the record's field count, which a generic cannot express. A `Print` with no width is straight-line: one `Writer.Str` call per literal and one direct call of each hole's formatter instance, in text order, each on the call's error edge, so no array is built and no call is indirect. A `Print` with a width, every `Arena.Fmt`, and a few other forms (a writer that is not a whole local, for one) fill a stack array of erased arguments and call one non-generic runner, as Rust's `fmt::Arguments` does. Either way each formatter instance is shared by every call that formats its type, and the bytes written are the same.

### The cache

The cache is a content-addressed store. It lives in `MK_CACHE` if set, else in one directory per user (`%LOCALAPPDATA%\mk\cache` on Windows, `$XDG_CACHE_HOME/mk` or `~/.cache/mk` elsewhere). `mk cache size` and `mk cache clean` manage it.

| Entry | Key |
|---|---|
| Module interface | Compiler build ID, the target facts read, the module's token hashes, its imports' interface hashes |
| Function check result | Compiler build ID, the function's token hash, the hashes in its read set |
| Const value, generator output | The bodies entered, the consts, layouts and target facts read, and the fuel |
| Object pack | Compiler build ID, target, profile, CPU, the PGO profile's hash in release, the sorted instance keys of the pack |
| C bindings | Clang's identity, the target, defines, sysroot and search paths name a list of up to four candidates, one per header content; a candidate holds while every header read has its hash and every path that would shadow one is absent: the same name earlier on the search path, or beside any file of the unit (a quoted include searches beside its includer first, and on the msvc target beside the whole include stack) |
| C object (shim, C source, assembly source) | Clang's identity, the command line and the source name the same kind of list, with the search path including the directories of the source's own `-I`, `-iquote`, `-isystem` and `-idirafter` flags. Any other flag but a plain one (`-D`, `-U`, `-std=`, `-W`, `-w`, `-pedantic`, `-O`, `-g`, `-m` but `-mllvm`, and `-f` without a value and naming no modules, profile or API notes) leaves the object uncached, and the link's key then takes the object's bytes |

Instances are grouped into object packs by owning module, split by size, so a body edit recompiles one pack. Entries are written to a temporary name and renamed, so concurrent `mk` processes and a crash never leave a torn entry. File size and modification time are a hint to skip rehashing, never part of a key. A module that read only the pointer size shares its interface across 64-bit targets.

Set `MK_CACHE_VERIFY=1` to recompute every cache hit and compare it with the stored entry. Use it when you change what a key covers.

`gen/` beside a module is a view of generated code for people to read. It is written only when it changes, one file per module per target, and never read as source.

## 7. Generators and C import

A generator run is an evaluation ([the evaluator](#the-evaluator)) plus a record of every declaration it reflected on. It reruns when anything it read changes. Its output is parsed and hashed per item, so unchanged items invalidate nothing.

C import is a native step, because a generator's fuel and determinism rules do not fit it. The driver runs Clang as a process with a scrubbed environment, `-nostdinc` and explicit `-isystem` and `--sysroot`, and reads its JSON syntax tree.

- Clang's record layout is taken as is. A layout Mk cannot express becomes an opaque type with accessors.
- Object-like macros are probed in one batch.
- `static inline` functions become shims in one generated C file.
- Items load lazily, because one system header can declare tens of thousands.
- All C imports of a package form one translation unit, so a C type has one identity in the package.

## 8. Back end

LLVM is the only back end, run as a process in every profile: `clang -O0` for dev builds, `-O3` with ThinLTO for release. The IR may use LLVM 21's features, so LLVM 21 or newer is required. There is no `opt` or `llc` step and no C source. The driver finds the tools from `--llvm`, then `MK_LLVM`, then a toolchain bundle's `llvm/bin` (the directory `../llvm/bin` from the `mk` executable's directory, used only when it holds both `clang` and the linker), then the platform's usual install location, then `PATH`. `mk version` prints what it found.

No C runtime is linked. The memory functions, stack probes, the task switch and the program entry are emitted as LLVM IR; the rest of the runtime is Mk in `std`. Line tables are emitted in every profile: DWARF on Linux and macOS, CodeView on Windows. A macOS executable is linked by `ld64.lld` against a libSystem stub the driver writes from `std/sys/macos/libSystem.def`, so no SDK is needed.

- **Inline assembly.** Each `asm` statement becomes one LLVM inline-asm call, Intel syntax unless `att`. `.s` files go through Clang.
- **Stack overflow.** Each task stack has a guard page below it, and frames larger than a page probe it, so an overflow always faults on the guard.
- **ABI lowering** is its own package, `abi`, tested against Clang's choices per target. A type's ABI class is stored in its shape. A function that only Mk calls directly (not `extern` or `export extern`, never used as a value or named by an `asm` `sym` operand, not spawned, not `naked`, with no `section` clause and on no `become` edge) has an internal lowering: on `x86_64-windows` outside `--debug`, a 9- to 16-byte aggregate of integers and pointers travels in two registers, as System V passes it, instead of by reference (`abi.LowerInternal`).
- **Tail-dispatch tables (`preserve_nonecc`).** In optimized builds outside `--debug` and `--profile size` on `x86_64` and `aarch64`, a module-private, non-section `const` array or slice of concrete Mk functions (non-extern, non-naked, non-variadic, with no `byval` parameter, and on non-Windows `x86_64` at most 10 general-purpose argument slots so `r11` stays free for inline-asm stack probes and `rax`/`r11` stay free for System V indirect tail calls) that never escapes and is dispatched via `become` inside its handler closure gets a private companion table (`<table_sym>$pnone`) and a `preserve_nonecc` body (`<sym>$pnone`) for every handler in the closed set. Each handler keeps its C ABI entry symbol as a thin wrapper that calls `<sym>$pnone` once (saving callee-saved registers at the outer boundary), while every `become` inside the loop — indirect through the table or direct to another handler in the set — dispatches with `musttail call preserve_nonecc`, keeping 8 to 12 arguments in registers with no stack spills. Eligibility and per-local table bindings are hashed into `PackFacts` so incremental builds stay byte-identical to cold builds.
- **Profile-guided optimization** is done in the front end, because LLVM's instrumentation needs a profile runtime written in C. `mk build --release --pgo-gen` counts function entries and branch edges, and the program writes a `.mkprof` file on exit. `mk profile merge` sums profiles. `--pgo=<file>`, or a `default.mkprof` in the package root, gives LLVM branch weights, entry counts and hot and cold attributes. A function is matched by its stable key and a checksum of its MIR block structure, so a stale entry is ignored, never misapplied.

## 9. Determinism

A cold build and a warm build produce byte-identical objects and executables, and so do builds with different thread counts.

| Source of variation | Rule |
|---|---|
| Map iteration order | Only stable IDs reach anything hashed or emitted |
| Parallel completion order | Results go in slots indexed by stable IDs. Diagnostics are sorted |
| File order | Byte order of names. Names that differ only in case are rejected |
| Paths | Package-relative. `-ffile-prefix-map` for C |
| Host | Clang, the SDK, `PATH` and the environment are in the key or scrubbed. `SOURCE_DATE_EPOCH` for C |
| Line endings | Normalized before hashing |
| CPU | No `native` CPU in a cache key. The default is the target's baseline |
| Memory, host stack | Never decide whether a program is accepted |
| Generators | Fields in declaration order; module-wide lists in byte order; hygienic names per run |

`--timings` prints per-stage time, and names any function that takes over 10 ms in the loan pass, on standard error. It stays out of `--json` and out of every cached result.

## 10. Packages

The compiler is a set of Go packages under `internal/`, with `cmd/mk` on top. Dependencies point down: a package imports only packages below it. The allowed edges are listed in `internal/archtest/dag.go`, and `TestPackageDAG` fails on any import that is not listed. To add an edge, change that table in the same change.

From the bottom up:

| Group | Packages | What they do |
|---|---|---|
| Contract | `source`, `diag`, `ast`, `types`, `mir` | Plain data shared by every stage: files, spans and hashing (`source`); diagnostics and their JSON (`diag`); the syntax tree (`ast`); types, signatures, shapes and the evaluator interface (`types`); MIR (`mir`). No logic beyond constructors, printing, hashing and validation |
| Front end | `syntax`, `resolve`, `fmtspec`, `check`, `lower` | Lexer and parser; name resolution; format strings of `Print`; type checking, generics and shapes; lowering typed bodies to MIR |
| Middle | `borrow`, `eval`, `gen`, `mono`, `iface` | Moves, linear values and loans; the MIR interpreter; generators; instance collection; interface encoding and hashes |
| Back end | `abi`, `llvm`, `pgo`, `codegen`, `toolchain` | C ABI classification; an LLVM IR text builder; profiles; MIR to LLVM IR; finding and running `clang` and `lld` |
| Services | `cache`, `cimport`, `fetch`, `git`, `release`, `format` | The content-addressed store; C import through Clang; fetching dependencies; git's protocol and tree hash; release archives and their signatures; `mk fmt` |
| Driver | `driver` | The session: module graph, stage scheduling, cache lookups, manifests, dependencies, the test runner. It may import everything below it |
| Front doors | `cmd/mk`, `corpus`, `lsp`, `repl`, `stdtest` | The CLI; the verdict-block corpus; the language server; the REPL; std's Go-side tests |

Two rules hold across the graph. The contract packages stay small and free of logic, because an edit there rebuilds every importer. And `check` reaches the evaluator only through the interface in `types` ([the stage rule](#3-the-stage-rule)).

The programs under `tools/` (the gate, the benchmark runner, the release tool and others) are outside the compiler's graph: nothing in the compiler imports them. `archtest` also checks that every `.go` file uses the standard library only, imports no `unsafe` or cgo, is gofmt-clean and has at most 1,200 lines.

## 11. Testing a change

[CONTRIBUTING.md](../CONTRIBUTING.md) has the commands, the gate and the rules for Linux and timing tests. Four checks matter most for a compiler change:

- **The documentation tests.** Every fenced `mk` block in `doc/` is a test, judged by its verdict line ([the rule](../CONTRIBUTING.md#documentation)). Run one document with `go run ./cmd/mk corpus --only "doc/memory.md:*"`. A change to what the language accepts changes a document and its blocks in the same change.
- **Program tests.** `tests/programs` holds whole programs with expected output, `tests/conformance` one unit per error code and memory rule, and `tests/soundness` the witnesses and mutant tables of the borrow checker's soundness tests. `internal/corpus` runs the last two.
- **Incremental builds.** Run a test twice, cold and warm, or set `MK_CACHE_VERIFY=1`. A warm build must equal a cold one byte for byte.
- **Determinism.** Build twice with different `-j` values in different directories, and compare the bytes.
