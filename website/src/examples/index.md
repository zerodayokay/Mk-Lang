---
description: Mk programs to read and copy - small programs on this page, short recipes in the cookbook, complete programs, and eighteen real packages in the repository.
---

# Examples

Mk examples come in four sizes:

| Size | Where | What you get |
|---|---|---|
| A few lines | This page | Small programs that each show one thing |
| A recipe | [Cookbook](./cookbook) | About fifty short programs for everyday tasks: files, maps, tasks, HTTP, processes, C |
| A program | [Complete programs](./programs) | A web service, a crash-safe key-value store, a kernel and an interpreter, explained |
| A package | [Dogfoods](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods) | Eighteen real tools of a few thousand lines each, with their own tests |

Every program on this site compiles with the current release. Those that print show their output underneath.

## FizzBuzz

```mk
// verdict: ok
Label(n: int): str {
    fizz := n % 3 == 0
    buzz := n % 5 == 0
    if fizz && buzz {
        return "FizzBuzz"
    } else if fizz {
        return "Fizz"
    } else if buzz {
        return "Buzz"
    }
    return ""
}

export Main(sys: &System): WriteError!void {
    for n in 1..16 {
        label := Label(n)
        if label.Len() > 0 {
            try sys.Print("{label}\n", (label))
        } else {
            try sys.Print("{n}\n", (n))
        }
    }
}
```

```text
1
2
Fizz
4
Buzz
Fizz
7
8
Fizz
Buzz
11
Fizz
13
14
FizzBuzz
```

`1..16` is half-open, so it counts from 1 to 15. `Label` returns a `str` literal, which lives as long as the program, so no arena is needed.

## Sum the numbers on the command line

```mk
// verdict: ok
export Main(sys: &System): WriteError!int {
    var total: i64 = 0
    for arg in sys.args[1..] {
        n := arg.ParseInt() else {
            try sys.PrintErr("sum: not a number: {arg}\n", (arg))
            return 2
        }
        total += n
    }
    try sys.Print("{total}\n", (total))
    return 0
}
```

```text
$ mk run sum.mk -- 10 20 12
42
$ mk run sum.mk -- 10 x
sum: not a number: x
$ echo $?
2
```

- `sys.args[1..]` skips the program's own path.
- `ParseInt` returns `ParseError!i64`. The `else` block handles the error where it happens and leaves `Main` with status 2.
- `total` is an `i64` because `ParseInt` returns one: Mk never converts between `int` and `i64` on its own.

## Count the most common words

[Your first program](/guide/first-program) builds `wordfreq` step by step: arguments, reading a file, a map of counts, sorting, and error handling.

## From the cookbook

A few recipes to start from:

| Task | Recipe |
|---|---|
| Read a file line by line | [Read a file line by line](./cookbook#read-a-file-line-by-line) |
| Parse command-line flags into a struct | [Parse command-line options](./cookbook#parse-command-line-options) |
| Read JSON into a typed struct | [Parse JSON into a struct](./cookbook#parse-json-into-a-struct) |
| Use a fresh arena for each request | [Use one arena per request](./cookbook#use-one-arena-per-request) |
| Serve HTTP with routes | [Serve HTTP with routes](./cookbook#serve-http-with-routes) |
| Run tasks with a deadline | [Run tasks with a deadline](./cookbook#run-tasks-with-a-deadline) |
| Run a program and capture its output | [Run a program and read its output](./cookbook#run-a-program-and-read-its-output) |
| Write a test and a property | [Write a test and a property](./cookbook#write-a-test-and-a-property) |

## The dogfood programs

The repository's [`Dogfoods`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods) directory holds eighteen complete packages, each with a command line, real input formats, error messages, exit codes and tests. Among them:

| Program | What it does |
|---|---|
| [`mgrep`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/mgrep) | Recursive parallel search, in the manner of ripgrep |
| [`lox`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/lox) | The Lox scripting language: compiler, bytecode VM, garbage-collected heap, REPL |
| [`kvd`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/kvd) | Key-value server with a write-ahead log, tested under simulated faults |
| [`chat`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/chat) | Multi-room chat server with a WebSocket front end |
| [`raft`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/raft) | A five-node Raft cluster over UDP with a linearizability checker |
| [`kernel`](https://github.com/zerodayokay/Mk-Lang/tree/main/Dogfoods/kernel) | A freestanding x86-64 kernel booted in QEMU |

[The Dogfoods README](https://github.com/zerodayokay/Mk-Lang/blob/main/Dogfoods/README.md) lists all eighteen, with what each shows about Mk and how to build and run it.
