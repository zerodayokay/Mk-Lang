# The `mk` command

`mk` is the compiler, build tool, test runner, formatter and language server, in one executable. This guide describes each command with its common flags and an example, then the diagnostic format, fixes and the query API that editors and scripts use. [The packages guide](packages.md) covers manifests and dependencies.

## 1. Overview

```sh
mk new hello        # a package with pkg.mk and src/main.mk
cd hello
mk run              # build and run it
mk check            # check without building
mk test             # run the test blocks
mk fmt              # format the files in place
```

Every command takes its flags and at most one path, in any order. The path is one of:

- a package directory;
- a `.mk` file;
- for `build`, `check` and `test`, `./...`, which means every member of a workspace.

With no path, a command works on the current directory. `mk help` lists the commands, and `mk <command> -h` lists one command's flags.

| Exit status | Meaning |
|---|---|
| 0 | Success |
| 1 | The program has errors, a test failed, or `mk fmt --check` found unformatted files |
| 2 | A usage error, or a file could not be read or written |
| 3 | An internal compiler error (E0000) |

`mk run` exits with the program's own status.

## 2. Commands

### Flags the compiling commands share

`build`, `run`, `check`, `test`, `fix`, `doc`, `bench` and the package commands take these flags:

| Flag | Does |
|---|---|
| `--json` | Print diagnostics as the JSON document of [section 3](#3-diagnostics) |
| `--max-diagnostics n` | Print at most `n` diagnostics of the failing phase, plus every internal error and every error one of them follows from ([section 3](#3-diagnostics)). 0 means no cap. The default is no cap for JSON, 50 for text |
| `--target name` | Build for this target, such as `x86_64-linux` or `x86_64-windows`. The default is the host |
| `--offline` | Never fetch. Dependencies come from the cache or `vendor/`. `MK_OFFLINE=1` does the same |
| `--locked` | Refuse a dependency version that `pkg.sum` has no line for, and never write `pkg.sum` |
| `--no-cache` | Read nothing from the cache and write nothing to it |
| `--llvm dir` | The directory that holds clang and lld ([section 11](#11-llvm-and-toolchains)) |
| `-j n` | The number of parallel workers. The default is the number of CPUs |
| `--timings` | Print the time each step of the build took, on stderr |

### `mk build`

`mk build` compiles the package's bins into executables or libraries. They go to the current directory, or to `-o`. A successful build prints nothing.

```sh
mk build                      # dev profile: fast to build, contracts checked
mk build --release -o out/app # optimized, written to out/app
mk build --target x86_64-linux hello.mk
```

| Flag | Does |
|---|---|
| `--release` | Use the release profile: optimized by LLVM, contracts off. The default is the dev profile |
| `--profile size` | A release build optimized for size (`-Oz`) |
| `--profile checked` | Optimized as `--release`, with contracts on and `build.Profile` set to `Checked` |
| `--symbols` | With `--release`, keep symbols and debug information (PDB, DWARF) for profilers |
| `--debug` | Full debug information, variables and types included, in either profile |
| `--bin name` | Build only this bin |
| `-o path` | The output file (one bin) or directory (several bins). A path that ends in `/` or names an existing directory is a directory: each bin goes to `<path>/<bin>` (`.exe` on Windows) |
| `--cpu name` | The target CPU. The default is the target's baseline |
| `--frame-pointers on\|off` | Keep a frame pointer in every function. The default is `MK_FRAME_POINTERS`, else the target's: off on x86_64-windows, on elsewhere |
| `--pgo file` | With `--release`, optimize with this profile, or `off` for none. The default is `default.mkprof` in the package root, when present |
| `--pgo-gen` | With `--release`, build an instrumented program that writes a profile ([section 10](#10-debugging-coverage-fuzzing-and-profiling)) |
| `--object file` | Link this object file (COFF on Windows, ELF on Linux) into every executable. Repeatable |

### `mk run`

`mk run` builds the program, then runs it. Arguments after `--` go to the program. It takes the same flags as `mk build`, and exits with the program's status.

```sh
mk run
mk run hello.mk
mk run --release -- input.txt --verbose
```

### `mk check`

`mk check` reports every error a build would report, except a failure of clang or the linker (E0001), without generating code. It is faster than `mk build`. It runs clang only to translate C imports. With `--json`, the diagnostics go to stdout; otherwise to stderr.

```sh
mk check
mk check --all-targets
```

`--all-targets` also translates C imports and checks `pkg.Select` modules across every declared target, reporting any failing target or exported API mismatch (E0213).

### `mk test`

`mk test` builds the package's `test` blocks and runs each one in its own process. It prints one line per test and a total. A test passes when it returns, or, after a call of [`testing.ExpectPanic(want)`](std/testing.md#expected-panics), when it panics with a report that contains `want`.

```sh
mk test
mk test --run parse --timeout 10s
```

```text
PASS src "words"
1 passed, 0 failed
```

| Flag | Does |
|---|---|
| `--run text` | Run only the tests whose names contain `text` |
| `--timeout d` | The time limit per test process (under `--fuzz`, until the test reaches its loop). The default is `60s`; `0` turns it off |
| `--release` | Build the tests in the release profile |
| `--prop` | Run the property tester over the contracts instead of the tests ([section 7](#7-contracts-and-the-property-tester)) |
| `--sim seed` | Give every test this seed as `MK_SIM_SEED`, for a test that builds a simulated world from it, and seed the property tester ([section 9](#9-simulation-record-and-replay)) |
| `--cover`, `--cover-out file` | Print line coverage per file, and write it as lcov |
| `--fuzz name`, `--fuzztime d` | Run the fuzz target of the named test ([section 10](#10-debugging-coverage-fuzzing-and-profiling)) |
| `--std module` | Run the standard library's own tests for this module and the modules under it (`std` for all). Takes no path |
| `--show-output` | Also print each passing test's output under its `PASS` line, indented as a failing test's is |

On Windows, the standard library's Unix socket fixtures run in private working directories. A short relative socket name fits the address field even if the temporary directory has a long absolute path.

A failing test's output is printed under its `FAIL` line, and a passing test's only with `--show-output`, which goes with every other flag. `--prop` prints each contract's output whatever its result. With `--json`, every test's object carries its output in `output`, with or without the flag. `mk bench` prints its statistics, not the output, and does not take the flag.

### `mk fmt`

`mk fmt` formats the `.mk` files under the path in place, and prints the name of each file it changed. There is one format and no options.

```sh
mk fmt              # format the package
mk fmt --check      # list unformatted files, change nothing
```

`--check` (or `-l`) lists the files that are not formatted, writes nothing, and exits 1 if there are any.

### `mk fix`

`mk fix` applies every proven fix, rechecks, and repeats until none is left. It then prints the chain of rounds. [Section 4](#4-fix-classes) says which fixes are proven.

```sh
mk fix --dry-run    # show what would change
mk fix
```

| Flag | Does |
|---|---|
| `--dry-run` | Compute the chain and write nothing |
| `--canonical` | Then apply the rewrites of [the style guide](style.md), each kept only if the package still checks clean |
| `--std module` | Rewrite the standard library's own files of this module and those under it, in the tree `MK_STD` names. Takes no path |

### `mk explain`

`mk explain` prints what an error code means: its name, phase, trigger and fix class, with a rejected and an accepted example where one exists. The code is not case-sensitive.

```text
$ mk explain E0208
E0208 unused (types)
trigger: An unused local or import
fix: Proven: remove
...
```

`--json` prints one object with the keys `code`, `name`, `phase`, `trigger`, `fix_class`, `reject` and `accept`. [The error codes](errors.md) list every code.

### `mk doc`

`mk doc` prints the package's public surface, with its `///` comments, as Markdown on stdout.

```sh
mk doc -o API.md
mk doc --html -o api.html
mk doc --check
mk doc std.fs           # the same as mk doc --std std.fs
```

A path argument `std` or `std.<module>` that names no file or directory is a standard-library module, as with `--std`, with `--check` too. A name that ends in `.mk` or holds a path separator stays a path. A module the standard library does not have is an error that names it.

| Flag | Does |
|---|---|
| `--html` | Write HTML instead of Markdown |
| `--embed` | Write Markdown for a page that has its own title: no title, no module heading, items at `###` |
| `-o file` | Write to a file |
| `--check` | Instead, compile the fenced `mk` blocks of the `///` comments, each judged by its verdict line, and exit 1 when one fails |
| `--std module` | Document the standard library's public modules under this path (`std` for all); with `--check`, check their blocks |

Each item shows its signature, then a `Flows` line for what its result borrows, an `Errors` line for the errors it can return, its `///` text, and its fields or variants. For this module:

```mk
// verdict: ok
/// A word and how often it occurs.
export struct Entry {
    /// The word.
    export word: str
    /// How many times it occurs.
    export count: int
}

/// The text is empty.
export error Empty

/// First returns the first word of text.
export First(text: str): Empty!str from text {
    for w in text.Words() {
        return w
    }
    fail Empty
}
```

`mk doc --embed` prints:

````text
### `Empty`

```
export error Empty
```

The text is empty.

### `Entry`

```
export struct Entry
```

A word and how often it occurs.

Fields:

- `word: str`: The word.
- `count: int`: How many times it occurs.

### `First`

```
export First(text: str): Empty!str from text
```

- Flows: The result borrows from text.
- Errors: Empty

First returns the first word of text.
````

For a standard library module, `mk doc --std` starts with the module's stability level (`Stability: Stable.`).

### `mk query`

`mk query` answers a question about the checked package: a type's members, a function's signature, where a name is imported from. [Section 5](#5-query-api) lists the queries.

```text
$ mk query sig Words
export Words(s: str): int
module src
/// Words counts the words of s.
$ mk query import Arena
Arena is in the prelude: no import
```

### `mk lsp`

`mk lsp` is the language server. It talks the Language Server Protocol over standard input and output ([section 12](#12-editor-mk-lsp), and [Editors](editors.md) for setup).

| Flag | Does |
|---|---|
| `--std dir` | The standard library directory. The default is `MK_STD`, else the copy built into mk |
| `--cache dir` | The cache directory. The default is `MK_CACHE`, else the per-user cache |
| `--log file` | Append one line per message, with its time, to this file |
| `--check-capabilities` | Print the server's capabilities as JSON and exit |

### `mk repl`

`mk repl` reads Mk expressions and declarations and evaluates them at compile time, with the package's modules in scope ([section 8](#8-the-repl)).

| Flag | Does |
|---|---|
| `--root dir` | The package directory, or a directory of `.mk` files. The default is the current directory if it holds `pkg.mk`, else the standard library alone |
| `--fuel steps` | The step limit of each evaluation. The default is 4,000,000 |
| `--prompt on\|off\|auto` | Print prompts. `auto` prints them when stdin is a terminal |

### `mk bench`

`mk bench` runs the benchmarks: `test` blocks that call `testing.Bench` or `testing.BenchSys` ([section 10](#10-debugging-coverage-fuzzing-and-profiling)). A name selects the benchmarks whose names contain it.

```sh
mk bench
mk bench parse --baseline before.json
```

| Flag | Does |
|---|---|
| `--dev` | Build in the dev profile instead of release |
| `--limit d` | The time limit per benchmark. The default is `20s` |
| `--timeout d` | The time limit per benchmark process. The default is `2m` |
| `--out file` | Save the run as JSON. This needs `MK_TIMING=1` in the environment, so that saved numbers come from a run you meant to measure |
| `--baseline file` | Compare with a saved run |

### `mk apidiff`

`mk apidiff A B` compares the public facts of two versions of a package, given as two package directories, and says which changes break callers ([section 6](#6-api-diff)).

```sh
mk apidiff ../mylib-1.0 .
```

`--std` compares two standard library trees. `--json` prints the result as JSON.

### `mk cimport`

`mk cimport` translates the package's C imports and prints one block per import: the generated Mk declarations, the share of the headers' declarations it bound, and the declarations it skipped, with the reason for each.

```sh
mk cimport
```

### `mk cache`

`mk cache` reports and empties the cache. [The packages guide](packages.md#8-the-cache) says where the cache is.

| Command | Does |
|---|---|
| `mk cache size [path]` | Report the size of the cache a build of `path` uses, fetched packages and build products apart |
| `mk cache clean [path]` | Empty that cache's build products. Fetched packages stay |
| `mk cache prune --packages <dir>...` | Remove the fetched packages that no `pkg.sum` under the named directories refers to |

### `mk profile merge`

`mk profile merge` sums the profiles that instrumented runs wrote into one profile for profile-guided optimization ([section 10](#10-debugging-coverage-fuzzing-and-profiling)). The result does not depend on the order of the inputs.

```sh
mk profile merge -o default.mkprof run-*.mkprof
```

### Package commands

`mk new`, `mk install`, `mk remove`, `mk update`, `mk tree`, `mk why` and `mk vendor` create a package and manage its dependencies. [The packages guide](packages.md#16-package-commands) describes them.

```sh
mk new notes
mk install github.com/someone/toml@0.9
mk tree
```

### `mk version`, `mk upgrade` and `mk toolchain`

`mk version` prints mk's version and build, the Go version it was built with, and the LLVM tools it uses, with the rule that chose them ([section 11](#11-llvm-and-toolchains)).

`mk upgrade` installs a signed release and makes it current. A release build of mk, the one the [install scripts](../README.md#install) put in place, knows where releases are; an mk built from source needs `MK_RELEASES`. `mk toolchain` lists, selects and removes installed releases. [Section 11](#11-llvm-and-toolchains) describes both.

### `mk corpus`

`mk corpus` compiles every fenced `mk` block of the documentation in `doc/` and checks it against its verdict line. It is for working on Mk itself, from a checkout of the repository; [CONTRIBUTING](../CONTRIBUTING.md) describes it.

```sh
mk corpus -j 2 --only "doc/tools.md:*"
```

| Flag | Does |
|---|---|
| `--only pattern` | Judge only the blocks whose `document:line` matches the pattern |
| `--all` | Also judge the files of `tests/conformance` |
| `--build` | Also build the blocks whose verdict is `ok`, and run those that have a `Main` |
| `--strict` | Count a pending block as failed |
| `--target name` | Judge every block for this target; the default is the host. A block that holds for one host only is skipped |
| `--root dir` | The repository root. The default is found from the current directory |

## 3. Diagnostics

Every diagnostic is structured data, and the text form is one way to print it. [The error codes](errors.md#reading-a-diagnostic) show how to read the text form. With `--json`, a compiling command writes one JSON document instead:

```json
{
  "version": 1,
  "phase": "flow",
  "diagnostics": [
    {
      "code": "E0501",
      "name": "exclusive-conflict",
      "phase": "flow",
      "root": 1,
      "at": {"file": "src/lex.mk", "line": 41, "col": 5, "end_line": 41, "end_col": 10},
      "message": "pager is borrowed by a, which is used on line 43",
      "facts": {
        "loan": {"place": "pager", "kind": "exclusive", "at": "src/lex.mk:40:1"},
        "holder": "a",
        "kept_live_by": "src/lex.mk:43:1",
        "signature": "Pager.Next(mut self): ?str"
      },
      "fixes": [
        {"class": "proven",
         "edits": [{"file": "src/lex.mk", "line": 12, "col": 32, "end_line": 12, "end_col": 32, "text": " from self.text"}],
         "why": "Pager is a view, so its result stays borrowed. This private function returns only what self.text holds"}
      ],
      "alternatives": ["Use a before calling Next again"],
      "explain": "mk explain E0501"
    }
  ],
  "omitted": 0
}
```

The document holds the schema version, the phase that failed, its diagnostics, and how many diagnostics `--max-diagnostics` left out. Each diagnostic has exactly these keys, in this order:

| Key | Holds |
|---|---|
| `code`, `name` | The error code and its name, as in [the error codes](errors.md) |
| `phase` | The phase that reported it |
| `root` | The position, counted from 1, of the diagnostic that causes this one. A diagnostic that is its own cause holds its own position |
| `at` | The location: a file, with a start and an end line and column. Columns count bytes from 1 |
| `message` | One line of text |
| `facts` | What the compiler knew: a type and its members, a loan and its holder, the signature in question |
| `fixes` | Each fix with its class, its edits and why it is right. An edit whose start equals its end inserts its `text` |
| `alternatives` | Options for you to choose from, as text |
| `explain` | The command that explains the code |

The phases, in order, are `parse`, `imports`, `names`, `types` and `flow`. After them comes `native`, for E0001, a failure of clang or the linker; it is reached only when every earlier phase is clean. A report that holds only internal errors (E0000) has the phase `internal`.

`version` is 1. A key may be added within a version, and a reader ignores keys it does not know. Removing or renaming a key, or changing what one means, takes the next version.

How errors are reported:

- Every error of the earliest failing phase is reported at once, and nothing from later phases except an error one of them follows from, which is reported as its `root` with its own phase. A failed `gen` line's error is reported this way beside the unknown names its missing output leaves.
- Errors are grouped by cause. `root` links each consequence to its cause, so fixing the cause clears the group.
- A borrow conflict names the loan, its holder and the use that keeps it live.
- E0203 gives the type and the members the module can name there, and E0201 the module path and import line, so you need no second lookup.
- A habit from another language, such as `v.push(x);`, gives the line rewritten with every rewrite its phase can make. `mk fix` applies the rewrites of later phases in later rounds.
- An error in generated code cites the generated line, the generator and the `gen` line that ran it.

## 4. Fix classes

| Class | Meaning | Applied by `mk fix` |
|---|---|---|
| `proven` | Keeps the program's behaviour, checked by recompiling | Yes |
| `compiles` | Checked to compile, but changes what the program does | No, offered |
| `alternatives` | Named options, not checked | No |

`mk fix` repeats until no proven fix is left, and reports the whole chain. A fix that adds a clause to a function can move an error to that function's callers; the next round fixes that, in the same run. Each fix is checked by recompiling, so nothing unchecked is applied.

A proven `from` or `into` clause (E0601, E0602, E0604) is recompiled before it is offered, by `mk check`, by each round of `mk fix`, and by `mk lsp` when code actions are asked for. It stays proven when the report gets better. When its error only moves to the callers, their own clauses are applied with it and the package is rechecked again, step by step. The clause stays proven only if that chain ends better than it started. A clause whose chain ends elsewhere is offered as an alternative, and the note names the error that is left, for example `try: write ... (not proven: recompiled, the package then reports E0505 at main.mk:16)`. That happens when a caller is left with an E0505, where the new loan meets a value that outlives it, or with an E0503. A report allows 16 rechecks. A clause not recompiled within them is an alternative too ("not proven: not recompiled"). `mk fix` writes only the clauses that are still proven, so it never writes one that `mk check` would offer as an alternative.

When the package checks clean, a last round drops the empty `()` argument of each `Print`-style call whose text has no hole. It keeps the edit only if the package still checks clean.

| Fix | Class | Why |
|---|---|---|
| Recase, unique import, `var`, `view`, removing an unused name, habit syntax | `proven` where the code's row in [the error codes](errors.md) says so | Local and mechanical |
| `from` from a body | `proven` for a private function, `compiles` for an exported one | A new clause on a public function can move errors to callers in other packages |
| `into` | `proven` for a private function, `compiles` for an exported one | It adds loans at callers. A private function's callers are rechecked in the same run, and a clause whose chain of callers ends in another error is an alternative |
| `defer x.Close()` | `proven` only when one void release method exists, its arguments are unique in scope, and the value does not escape | Otherwise it could be wrong |
| Match arms | `compiles` | Stubs compile, but do nothing useful |
| The `_` arm of an open set (E0306 `reason: open`) | `proven` | Every member known today already has an arm, so the `_` arm runs only for a member a later release adds. It panics, naming the set |
| `try` | `compiles` | Handling an error and passing it on are different programs |
| Closure to struct plus `fn` | `alternatives` | It rewrites the callee |
| Iterator chain to loop | `compiles`, when the output type is stated | The chain states no arena or error set |

## 5. Query API

`mk query` answers questions about the checked package, so that a tool or a person never has to guess a name. Each answer is short, at most about 200 tokens, except the listings marked "never capped". Every query takes `--json` and `--root path`. In a package that does not parse, `members` and `find` answer from the standard library alone, print the package's diagnostics on stderr and end with a line saying so.

| Query | Answers |
|---|---|
| `members T` | Fields and methods with their signatures. A name that is both a type and a function, `Eq`, answers the type's and names the function after them |
| `find word` | Names matching a word or a synonym: `len`, `size`, `append`, `HashMap`, `unwrap`. The compiler's members of `?T` and `E!T`, such as `?T.Must`, are listed as `builtin` |
| `sig Name` | The full signature, with flows, error set and contracts |
| `import Name` | The import line for an exported name |
| `modules` | The module map |
| `kind T` | Plain, unique or linear, and why |
| `shape T` | Size, alignment, ABI class, kind, carrier, opaque, `Send`, `Share` |
| `why-live F line` | Which holder keeps a loan live, and the path to it |
| `flows F` | Automatic and declared sources |
| `errors F` | The error set, with reasons |
| `callers F` | Direct callers |
| `allocs F` | A path to an allocator, or none |
| `stack F` | The stack bound in calls, or the recursion that prevents one. A recursion whose depth parameter falls around every cycle counts at its bound ([section 5.2](#52-depth-bounded-recursion)) |
| `panics F` | Every panic site reachable from `F` through direct calls, with its code and the call path to it |
| `uses` | Every standard library item the package references, with its sites. Never capped |
| `perimeter` | Per package of the build: its local names, source and version, and its unsafe code, C and assembly ([the packages guide](packages.md#11-grants-for-unsafe-code)). Never capped |
| `panics --member name` | Every panic site of a workspace member's modules ([section 5.1](#51-member-wide-panic-sites)). Never capped |
| `sig --member name` | Every function, method and other item of a workspace member's modules with what `sig Name` answers for it, generated ones included ([section 5.3](#53-member-wide-signatures)). Never capped |

### 5.1 Member-wide panic sites

`mk query panics --member <name> [--json] [--root path]` lists every place where one workspace member's code can panic. Name the member by its manifest's `name` or by its entry in the root's `members`. It works from the root or from any member. A package outside a workspace answers for its own name.

The listing is complete: never capped and never paged. `panics F` follows only direct calls from `F`, so it misses code reached through a `const` table of `fn` values or a struct of `fn` hooks. This listing reads every function of the member's modules, reached or not: functions, methods, `fn` literals, contract predicates and test blocks. Sites in the standard library and in dependencies are not listed.

Each site has a kind:

| `kind` | `explicit` | Where |
|---|---|---|
| `panic` | true | `Panic(msg)` |
| `assert` | true | `Assert(c)` |
| `unwrap` | true | `.Must()` on `none` or an error |
| `contract` | true | A `requires`, `ensures` or `invariant` clause, checked in dev and test builds |
| `bounds` | false | An index or slice bound |
| `overflow` | false | Integer `+ - *` and negation. Never the wrapping forms, or a `+`, `-` or `*` that the compiler proved in range |
| `divide` | false | Integer `/` and `%`, unless the divisor is a constant other than 0 and -1 |
| `shift` | false | `<<` and `>>` |
| `cast` | false | `Cast[T](x)` and every checked cast, also one that cannot fail (an integer to `f64`, a widening `Vec.Cast`) |
| `str-boundary` | false | A `str` slice that does not fall on a character boundary |
| `enum-tag` | false | An enum tag read from C: at an `export extern` entry, or returned by a C `extern` |
| `unreachable` | false | Not listed. The trap after an exhaustive `match` or a call that never returns fires only on a compiler bug |

Rules for what counts as a site:

- Some standard library functions are compiled in place at the call, and their checks are sites at the call: `math.DivWide` (`divide`, `overflow`), and `simd.Vec`'s `FromSlice`, `FromSliceOr`, `CopyTo`, `CopyToMasked` and `Gather` (`bounds`), `DivTrunc` (`divide`), `Shl`, `Shr`, `ShlEach` and `ShrEach` (`shift`), and `Cast` (`cast`).
- A panic inside any other standard library function belongs to the library and is not listed. Nor is std.simd's panic for a restated clause that only a generic instance breaks.
- `EvalArena()` called at run time is a `panic` site at the call. A compile-time `requires` that calls it is also checked at run time on entry in dev builds.
- A generic function's sites are listed once, from its generic body, with `generic` true. Each stands for the same site in every instance. No operator that can trap applies to a type parameter's values (E0703), so no instance has a site of its own.
- The listing follows the dev profile. A `contract` site does not exist in a release build. A bounds or overflow check that the optimizer later removes is still listed. So the list may hold sites that a build drops, but it misses none of the member's own code.
- Entries may repeat exactly, one per operation, when several operations start at the same position.
- The listing covers the modules of one target, the host's or `--target`'s. A member with per-target modules is listed once per target.

The `--json` document, version 1, is one object and a newline (shown wrapped, with one site):

```text
{"version":1,"query":"panics","member":"eng","functions":19,"complete":true,"missing":[],
 "sites":[{"file":"src/ops.mk","line":4,"col":5,"kind":"assert","code":7,"explicit":true,
           "function":"OpAdd","module":"src","generic":false,"test":false}, ...]}
```

- `version` is 1. A field added keeps the version, and a field removed or changed in meaning raises it.
- `query` is `"panics"`, and `member` is the member's package name.
- `functions` counts the function bodies read.
- `complete` is false when a function that should have a body has none, because it failed to check. `missing` names those functions, so a site is never dropped silently.
- `sites` are sorted by file, line, column, kind and function. Every field of a site is always present:
  - `file` is relative to the member's directory, with `/`.
  - `line` and `col` count from 1, with `col` in bytes.
  - `kind` comes from the table above, and `code` is the runtime's fixed panic code for it.
  - `function` is named as the other queries name it, for example `Vm.Step`, `Apply (fn literal)` or `src#test("ops")`.
  - `module` is the module path.
  - `generic` and `test` are true for a site in a generic function or inside a test block.

Without `--json`, the listing is a header line, then one line per site: `kind  file:line:col  in function`.

| Exit status | When |
|---|---|
| 0 | The listing was printed |
| 1 | The member has errors, printed on stderr. The listing is still printed when the member checks as far as types |
| 2 | An unknown member, which the message names among the members; or `--member` given with a query other than `panics` and `sig`, or with a name |

### 5.2 Depth-bounded recursion

`stack F` gives a bound in calls: the deepest chain of direct calls from `F`. A recursion has no such bound by itself, so `stack` names it and stops, unless a depth parameter bounds it. Divide and conquer (a Karatsuba product, a recursive division, a parser's nesting) then states its bound like this:

```mk
// verdict: ok
// Sum adds xs by halves. depth falls by one per level, and at 0 the loop
// finishes, so the recursion is never deeper than depth.
Sum(xs: []i64, depth: u8): i64 {
    if xs.Len() <= 8 || depth == 0 {
        var s: i64 = 0
        for x in xs {
            s += x
        }
        return s
    }
    h := xs.Len() / 2
    return Sum(xs[..h], depth - 1) + Sum(xs[h..], depth - 1)
}

export Total(xs: []i64): i64 {
    return Sum(xs, 64)
}
```

`mk query stack Total` answers `bounded` with a deepest chain of 257 frames: `Total`, then `Sum` at most 256 deep. The rule, for each recursion (a set of functions that call each other directly):

- Each function in it has a parameter of type `u8` or `u16`, its depth, the same type in all of them, that the body never assigns or lends as `mut`. Its position and name may differ from function to function.
- Every call inside the recursion passes the caller's depth, either as it is or less a positive constant: `depth`, `depth - 1`, or a local bound once to one of these. `-` traps below zero, so the depth never wraps; `-%` does not count.
- The calls that pass the depth unchanged form no cycle, so every cycle lowers it.

Such a recursion of n functions counts as at most (max + 1) n frames, max being 255 for `u8` and 65,535 for `u16`, whatever depth its callers pass, and `stack` lists each one as `depth-bounded recursion`. A recursion that breaks the rule is reported as before, with the cycle that prevents a bound. A walk that stops at 20,000 functions gives no bound either. Frames are counted, not bytes: a frame's size is known only after code generation.

### 5.3 Member-wide signatures

`mk query sig --member <name> [--json] [--root path]` lists every function and method of one workspace member's modules, each with its signature as `sig Name` spells it, then the member's other items, each with its head. Every entry carries its doc lines as `sig Name` answers them, so one process answers what a `sig Name` per item would. The member is named, found and checked as for [section 5.1](#51-member-wide-panic-sites), and the exit status is the same. `sig Name` still takes one name.

The listing is complete: never capped and never paged. Its `functions` hold functions, methods, impl methods and `extern` functions, and the functions a `gen` line writes, which no declaration in the source shows. They also hold an error's built-in `Format` ([the reference](reference.md#how-errors-print)), located at the error's name. Its `items` hold structs, enums, errors, interfaces, `type` aliases, consts and globals, the types a `gen` line writes included. Contract predicates, invariants, `fn` literals and test blocks are not listed. Items of the standard library and of dependencies are not listed.

The `--json` document, version 1, is one object and a newline (shown wrapped, with one function and one item):

```text
{"version":1,"query":"sig","member":"eng","complete":true,"missing":[],
 "functions":[{"name":"Collect","module":"src","file":"src/heap.mk","line":11,"col":8,
               "sig":"export Collect(h: mut Heap)","resolved":"export Collect(h: mut Heap)",
               "contracts":[],"exported":true,
               "impl":"","generated":false,"gen":"","kind":"func","subject":"Collect",
               "doc":["/// Takes the heap mutably."],"truncated":0}, ...],
 "items":[{"name":"Heap","module":"src","file":"src/heap.mk","line":5,"col":15,
           "sig":"export struct Heap  (module src)","resolved":"export struct Heap  (module src)",
           "contracts":[],"exported":true,
           "impl":"","generated":false,"gen":"","kind":"struct","subject":"Heap",
           "doc":["/// A heap."],"truncated":0}, ...]}
```

- `version` is 1. A field added keeps the version, and a field removed or changed in meaning raises it.
- `query` is `"sig"`, and `member` is the member's package name.
- `complete` is false when a function has no signature, because it failed to check. `missing` names those functions.
- `functions` are sorted by file, line, column and name. Every field of an entry is always present:
  - `name` is the name `sig` takes: `Collect`, or `Heap.Grow` for a method. An impl method, which `sig` does not answer, is named after its type too, `Square.Area`, also for an impl for a generic type (`Box.Area` for `impl[T] Shape for Box[T]`), and `impl` names its interface, `Shape`; `impl` is `""` for any other function.
  - `module` is the module path.
  - `file`, `line` and `col` locate the function's name, as diagnostics print it. A generated function's `file` is its module's gen view, `src/gen/<target>/gen.mk`, which a query does not write.
  - `sig` is the signature as the first line of `sig Name` spells it, `export` included. An impl method's is spelled the same way after its `name`, and so is an error's built-in `Format`, which `sig NotFound.Format` writes without the type: `NotFound.Format(&self, w: mut Writer): WriteError!void`. `contracts` are its `requires` and `ensures` clauses, as the source writes them. A type is spelled by its name alone, as `sig Name` spells it: `mut Heap` for a parameter of a dependency's `heap.Heap` too, so `module` and the imports tell two types of one name apart. An alias is spelled as the declaration writes it, by its name and arguments: `Load(i: u64): Value` for `type Value = u64`, also under `&`, `mut`, `[]`, `?`, an array, `E!T` and a generic type's arguments. A result slot that holds an error union as a value, including one in a nested `fn` type, spells the union followed by `(cannot fail)`.
  - `resolved` is `sig` with every alias spelled as its type, `Load(i: u64): u64`, and equals `sig` when the signature names no alias.
  - `exported` is true for an exported function. An impl method is never `exported`: its type and its interface decide who can call it.
  - `generated` is true for a function a `gen` line wrote, and for the built-in `Format` of an error one wrote, and `gen` is that line, as `file:line:col`. It is `""` for any other function.
  - `kind` is `func` for every function, and for an item its kind as `find` names it: `struct`, `enum`, `error`, `interface`, `alias`, `const` or `global`.
  - `subject` is a spelling that `sig`, asked in the member, resolves to this entry. `sig` takes a spelling for the first item that has it, in the build's order of the modules, which is not the listing's, so a `name` two modules declare answers for one of them only, and `subject` says which. It is `name` when no item before this one in that order takes it, else the module's last segment and `name` (`more.Point`), else the module's path and `name` (`src.x.more.Point`), and `""` when items before it take all three. It is also `""` for an impl method and an error's built-in `Format`, which `sig` does not answer as listed.
  - `doc` is the entry's `///` lines, and `truncated` the number of lines cut, exactly as `sig <subject>`'s answer holds them: the answer is cut at its 200-token cap, and `doc` holds the doc lines that survive the cut. `doc` is `[]` for an entry with no doc lines. An impl method, or an entry whose spellings are all taken, gets its own `///` lines by the same rule. An error's built-in `Format`, which no source declares, has `doc` `[]`, as `sig NotFound.Format` has no doc line: the lines above the error's name are the error's.
- `items` are sorted as `functions` are, and an item's entry has the same fields. Its `sig` is the first line of `sig <subject>`, the item's head with its module, `export struct Heap  (module src)`; its `resolved` equals its `sig`, its `contracts` are `[]` and its `impl` `""`. `generated` is true for an item a `gen` line wrote, and `gen` is that line, as for a function.

For an entry whose `subject` is not `""`, `sig <subject>` asked in the member (from its directory, or with `--root` at it) answers, line for line: `sig`; for a function, each contract indented by four spaces, then `module` and the module's path; then `doc`, with `truncated` as its count of lines cut. When a function's contracts alone pass the cap, the answer stops among them: it holds only the lines before the cut, `doc` is `[]`, and `truncated` counts the contract and module lines cut too. The listing is built from the same lines as the answer, and names the member's modules by their own paths wherever it is asked. From a package that depends on the member, `sig` looks a spelling up in that package first, and spells the member's module paths with the name that package gives the member: where `eng` names the member `hp` as `heap`, `sig Heap` from `eng` answers `(module heap.src)` for the entry's `(module src)`.

Without `--json`, the listing is a header line, then one line per function, `sig  file:line:col`, followed by `(gen file:line:col)` for a generated one, and each contract on a line of its own. When the member has other items, a line `N other item(s)` follows, then one line per item, `sig  file:line:col`, with `(gen file:line:col)` as for a function. Doc lines are in the JSON only.

The listing covers the modules of one target, the host's or `--target`'s.

## 6. API diff

`mk apidiff A B` compares the exported signatures, flows, error sets, const values and layouts of two versions of a package, so that no public fact changes unnoticed.

| Change | Severity |
|---|---|
| Signature, flow clause, error set | Breaking |
| Kind, carrier, `Send`, `Share` | Breaking |
| Size or ABI class | Recompile |
| Exported const value | Reported |
| A field added to a `std.pkg` struct | Compatible, since a manifest may leave it out |
| `open` written on an existing enum or error set | Breaking: a `match` outside the package with no `_` arm stops compiling. This holds even when an exported open alias of the older version already opened the set |
| `open` taken off an enum or error set | Reported |
| A member added to an open enum or error set, or to a set that names it and grows with it | Compatible: a set that holds every member of an open alias is open, so every set that holds the added members was already open |
| An added item | Compatible, with one exception. An open error-set alias whose members all exist in the older version is Breaking, because error sets are structural: the alias opens every set that holds those members, a dependent's own spelling included. It stays Compatible if an exported open alias of the older version has only members among them |

`mk apidiff --std A B` compares two standard library trees. A public, non-generic alias of a type declared in a module that only the standard library can import is compared as that type, under the alias's name and module. So std.core's `TargetInfo`, `Arch` and `Os` stand for `std.target`'s, and std.time's `Duration` for std.core's. A change to the type's facts is a change to the alias. Pointing the alias at another type with the same facts is no change.

## 7. Contracts and the property tester

`requires`, `ensures` and `invariant` are part of a function's public signature. The reference says [when contracts are checked](reference.md#when-contracts-are-checked).

`mk test --prop` runs the property tester instead of the test blocks. It calls each function that has a contract at compile time, with inputs generated from its parameter types: integers at and around their limits first, then random values.

- An input that breaks `requires`, or a struct's `invariant`, is discarded.
- A broken `ensures` or `invariant`, or a panic, is a finding. A finding is shrunk to a minimal call and saved in the cache, and the next run tries it first. Its report then says `from the corpus`.
- A function whose inputs the tester cannot generate is reported as skipped, with the reason, never as passed.
- A generic `T` is tried with a small set of types.
- The run prints its seed; `--sim SEED` repeats it.

Contracts are checked in the callee, so editing a contract does not rebuild the callers.

## 8. The REPL

`mk repl` reads entries and evaluates them at compile time, with the package's modules (or the standard library alone) in scope. Each evaluation is limited by `--fuel`. Compile-time evaluation has no `Io`, no raw memory access outside what the evaluation allocated, and no `asm` ([E0901](errors.md)).

## 9. Simulation, record and replay

`Io` is a value, so a program that takes an `Io` can run against a simulated one. [std.sim](std/sim.md) simulates a seeded world, and records and replays a real run's `Io` calls.

`mk test --sim SEED` puts the seed in each test's environment as `MK_SIM_SEED` and prints it in the report, and `--prop` draws its inputs from it. It does not simulate the test's `System`: `sys.io` is still the real one. A test that wants the simulated world reads the seed (`sys.Env("MK_SIM_SEED")`), builds a `sim.Sim` from it and runs its code on the `Sim`'s `Io`. Code that waits only on that `Io` cannot read the clock or the outside world, so the seed decides the whole run. The limit is `unsafe` code and C: a C library that reads the clock escapes simulation.

## 10. Debugging, coverage, fuzzing and profiling

**Backtraces.** A panic report adds one line per stack frame after the panic site, with the function's name and file. On Windows the system unwinder walks the stack; on Linux, the frame-pointer chain. With frame pointers off on Linux, the report says so. A frame outside Mk code prints its address. [`runtime.WriteDump`](std/runtime.md#write-a-crash-dump) writes a crash dump, for a panic hook that wants one.

**Frame pointers.** `--frame-pointers=on|off`, or `MK_FRAME_POINTERS`, keeps a frame record in every Mk function that calls another. The default, in dev and release builds, is the build target's: off on x86_64-windows, where the system unwinder walks frames through `.pdata` and needs no frame pointer, and on for every other target. A cross-build takes its target's default. Changing it rebuilds the cached code.

**Coverage.** `mk test --cover` counts each basic block and prints line coverage per file. The counts are written at exit, and when the runner stops a test on a timeout. `--cover-out file` also writes lcov. The counters are readable from inside the process. The report lists the package's own files, not std's.

**Fuzzing.** `testing.Fuzz(f)` in a `test` block registers a fuzz target (`testing.FuzzSys(sys, f)` one that takes the test's `System`, to run an external oracle or write an artifact through `sys.io`, or to allocate from `sys.heap`). Plain `mk test` runs it over its saved inputs; `mk test --fuzz <test name> [--fuzztime d]` searches for new failing inputs. With `--show-output`, a passing plain run shows each `fuzz input` marker on its own line, followed by what the target printed for that input (std.testing writes out `sys`'s buffered streams before each marker and after the last call), and a passing `--fuzz` run only the loop's summary, `fuzz: N calls in T, 0 failing runs, 0 inputs saved`: the target's output over thousands of calls is not kept, and a failing input's `.report` holds the last 200 lines of it.

**Benchmarks.** `testing.Bench(f)` in a `test` block registers a benchmark (`testing.BenchSys(sys, f)` one that allocates from the heap), and `mk bench [name]` runs it. `--limit` bounds the time each benchmark takes, and `--baseline` compares with an earlier run.

See [std.testing](std/testing.md) for fuzz targets and benchmarks.

**Build profiles.** `--profile size` optimizes for size. `--release --symbols` keeps symbols and debug information for profilers. `--profile checked` optimizes as `--release` does, with contracts on.

**Profile-guided optimization.**

1. Build an instrumented program: `mk build --release --pgo-gen`.
2. Run it on typical input. At exit it writes `<name>-<pid>.mkprof` to `MK_PROFILE_DIR`, or else beside the executable.
3. Merge the profiles into the package root: `mk profile merge -o default.mkprof *.mkprof`.
4. `mk build --release` uses `default.mkprof`. `--pgo file` names another profile, and `--pgo off` uses none.

Dev and test builds use no profile.

## 11. LLVM and toolchains

mk compiles to LLVM IR and runs clang and lld on it. It needs LLVM 21 or newer. It looks for the tools in this order, and uses the first directory that holds clang:

1. `--llvm dir`. It is an error if the directory holds no clang.
2. `MK_LLVM`.
3. `../llvm/bin` beside mk's own `bin/` directory, in a release archive.
4. The host's default locations: `C:\Program Files\LLVM\bin` on Windows; `/usr/lib/llvm-21/bin`, `/usr/local/bin` and `/usr/bin` on Linux; Homebrew's `/opt/homebrew/opt/llvm/bin` and `/usr/local/opt/llvm/bin`, then `/usr/local/bin`, on macOS.
5. The directories on `PATH` that hold clang.

Within the last two rules, a directory that also holds the linker wins over an earlier one that holds clang alone. The other tools come from the chosen directory only. `mk version` shows which directory was chosen and by which rule. Changing LLVM rebuilds everything.

Where only `lld` is present, mk runs it as `lld -flavor link`, `lld -flavor gnu` or `lld -flavor darwin`, so one LLVM installation links Windows, Linux, macOS and freestanding executables.

**Installed toolchains.** A release is one archive per host on the [releases page](https://github.com/zerodayokay/Mk-Lang/releases): `mk-<version>-x86_64-windows.zip`, `mk-<version>-x86_64-linux.tar.gz`, `mk-<version>-aarch64-linux.tar.gz` and `mk-<version>-aarch64-macos.tar.gz` (Apple silicon), with `SHA256SUMS` over the archives and `SHA256SUMS.sig`, its Ed25519 signature by the release key. An archive holds `bin/mk`, `llvm/bin/clang` and `llvm/bin/lld` from LLVM's official release with clang's headers, `doc/`, the licences and `RELEASE.txt` (mk's commit and the SHA-256 of each LLVM file). The macOS archive needs macOS 14 (Sonoma) or later, the oldest the official LLVM build runs on, is tested on macOS 15, and loads only the system's libraries. There is no archive for Intel Macs yet: LLVM publishes no Intel macOS binaries, and Homebrew's link against its own shared libraries. On an Intel Mac, install.sh and `mk upgrade` install nothing and say to build mk from source with Homebrew's LLVM 21 (`brew install llvm@21 lld@21`, as the README's [Building from source](../README.md#building-from-source) shows). The [install scripts](../README.md#install) make the first install: they check the signature and the hash, unpack the archive into `<root>/toolchains/<version>` and point `<root>/current` at it. The root is `%LOCALAPPDATA%\mk` on Windows and `~/.local/share/mk` on Linux and macOS. On macOS, install.sh needs nothing beyond the system (it checks the signature with Perl when the system's LibreSSL cannot check Ed25519), and adds the `PATH` line to `~/.zprofile` as well as `~/.profile`. mk never downloads a toolchain by itself; these commands manage the installed ones:

| Command | Does |
|---|---|
| `mk upgrade [version]` | Download a signed release (the newest by default), check its signature against the keys built into the running mk and its hash, unpack it beside the installed versions and make it current. A wrong signature, a hash mismatch or an interrupted unpack leaves everything as it was. Only a release build of mk knows where releases are. An mk built from source needs `MK_RELEASES`, and otherwise changes nothing |
| `mk toolchain list` | List the installed versions and the current one |
| `mk toolchain use <version>` | Make an installed version current |
| `mk toolchain remove <version>` | Remove an installed version |

`MK_HOME` names the install root, and `MK_RELEASES` the address releases are downloaded from.

## 12. Editor: `mk lsp`

`mk lsp` is a Language Server Protocol 3.17 server over standard input and output. It gives the same answers as `mk check` and `mk query`. [Editors](editors.md) shows how to set it up.

The workspace is the package whose `pkg.mk` holds the open file. A file of the standard library's own tree is checked as part of it. Open buffers replace their files' text, so an unsaved edit is checked as you type, and only the code it affects is rechecked.

| Request | Answer |
|---|---|
| `initialize`, `shutdown`, `exit` | Capabilities for the rows below. Positions are UTF-8 when the client offers it, else UTF-16, converted from Mk's byte columns |
| `didOpen`, `didChange`, `didSave`, `didClose` | Incremental text sync |
| `publishDiagnostics` and pull diagnostics | [Section 3](#3-diagnostics)'s diagnostics: the earliest failing phase, each with its code, message and span, and the facts' locations as related information |
| `codeAction` | Each fix of [section 4](#4-fix-classes), labelled with its class. A `proven` fix is safe to apply without review. `source.organizeImports` sorts the imports. `mk fix`'s repeat-until-done is a command, not an action |
| `workspace/executeCommand` | `mk.explain`, which answers as `mk explain` does |
| `hover` | For a name: the signature with its flows and error set, and its kind, as `mk query sig` and `kind`; for a type, also its `shape` and `members`; for a local, parameter or field, its type. For a keyword, operator, primitive type or universe name: the reference's rule |
| `definition`, `typeDefinition`, `implementation`, `references` | The declaration of a name, its type's declaration, the `impl`s of an interface, and its uses in the package |
| `documentSymbol`, `workspace/symbol` | The file's items with their methods under them; the package's items |
| `formatting`, `rangeFormatting` | Through `mk fmt`. A file that does not parse is left as it is |
| `completion`, `completionItem/resolve` | After `.`, the members of the receiver's type, as `mk query members`; after `import `, the module map |
| `rename`, `prepareRename` | Renames a name across the package. Renaming an exported name also edits the open packages that depend on this one by path, and is refused while such a package is not open. A fetched dependency is never edited |
| `signatureHelp`, `inlayHint`, `documentHighlight`, `foldingRange`, `selectionRange`, call hierarchy, semantic tokens (full and delta) | As their names say |
| `didChangeWatchedFiles` | Rechecks the files changed on disk |

Answers come from the last successful check. Where the check has no answer, the server falls back to reading the tokens around the cursor.
