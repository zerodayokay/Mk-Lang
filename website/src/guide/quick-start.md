---
description: Run a one-file Mk program, build an optimized executable, make a package with mk new, and meet mk check, mk test, mk fmt and mk explain.
---

# Quick start

This page assumes `mk` is [installed](./installation) and `mk version` works.

## Hello world

Save this as `hello.mk`:

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    try sys.Print("hello, world\n")
}
```

```text
$ mk run hello.mk
hello, world
```

`mk run` compiles the file to a native executable and runs it. To keep the executable, build it instead:

```sh
mk build hello.mk              # the dev profile: fast to build, contracts checked
mk build --release hello.mk    # the release profile: optimized by LLVM, contracts off
```

A successful build prints nothing and writes the executable to the current directory. A dev build also records each `try` an error passes through and prints that trace if the error leaves `Main`. Both profiles check arithmetic and bounds ([`mk build`](/tools/#mk-build) lists the other profiles and flags).

## Arguments and the exit status

Arguments after `--` go to the program. `sys.args[0]` is the program's own path, so `sys.args` has one element when you pass none. When `Main` returns `int`, that is the exit status:

```mk
// verdict: ok
export Main(sys: &System): int {
    if sys.args.Len() < 2 {
        sys.PrintErr("usage: greet NAME\n") else {} // ignore a failed write
        return 2 // the exit status
    }
    name := sys.args[1]
    sys.Print("hello, {name}\n", (name)) else {}
    return 0
}
```

```text
$ mk run greet.mk
usage: greet NAME
$ echo $?
2
$ mk run greet.mk -- Ada
hello, Ada
```

`Main` may return `void`, `int`, `E!void` or `E!int`. An error that leaves `Main` is printed and the process exits with status 1 ([errors from `Main`](/reference/language/errors#errors-from-main)).

## A package

A program of more than one file is a package: a directory with a `pkg.mk` manifest. `mk new` makes one:

```sh
mk new hello
cd hello
mk run
```

```text
hello from hello
```

It writes two files:

```text
hello/
  pkg.mk          the manifest
  src/
    main.mk       export Main
```

The manifest is itself Mk, a constant the compiler evaluates:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "hello",
    mk: "0.6",
    bins: [pkg.Bin(name: "hello", root: "src", subsystem: "console")],
)
```

Inside a package, the commands take no file name: `mk run`, `mk build` and `mk test` work on the whole package. [Modules and packages](./modules) covers imports, and [packages](/tools/packages) covers dependencies.

## The everyday commands

| Command | What it does |
|---|---|
| `mk run [file]` | Build, then run. Arguments after `--` go to the program |
| `mk build [--release] [file]` | Build the executables |
| `mk check [file]` | Check without building. Prints nothing when the code is correct |
| `mk test` | Build every `test` block and run each in its own process |
| `mk fmt` | Format the `.mk` files in place. There is one format and no options |
| `mk fix` | Apply the fixes the compiler can prove, such as a missing `var` |
| `mk explain E0404` | Explain an error code, with a rejected and an accepted example |
| `mk doc` | Print a package's public API as Markdown or HTML |
| `mk query members List` | Ask the checked program a question, here the methods of `List` |
| `mk lsp` | The language server, for [editors](/tools/editors) |

[The mk command](/tools/) documents every command and flag.

## When the compiler says no

Every diagnostic has a position, a code, a name, the offending line and, where one exists, a fix. This program forgets that `total` must be `var` to change:

```mk
// verdict: reject E0404
export Main(sys: &System): WriteError!void {
    total := 0
    for i in 1..4 {
        total += i // <- E0404
    }
    try sys.Print("{total}\n", (total))
}
```

```text
$ mk run count.mk
count.mk:4:9: E0404 assign-immutable: total is not var and is already assigned
    |         total += i // <- E0404
    |         ^^^^^^^^^^
    declared_at: count.mk:2:5
    fix (proven): declare it var so it may be assigned again or borrowed mut
    explain: mk explain E0404
1 error
```

The fix is marked *proven*, so `mk fix count.mk` applies it: `var total := 0`. `mk explain E0404` prints the rule with a rejected and an accepted example, and the [error codes](/reference/errors) page lists every code. [Reading a diagnostic](/reference/errors#reading-a-diagnostic) explains each part of the report.

## Next

Build a real program, step by step, in [your first program](./first-program).
