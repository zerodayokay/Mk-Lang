// Why: every command reads files or standard input and reports errors the
// same way.
// What: Ctx, Streams, ForEach, Report, the exit statuses and the input errors.
// How: ForEach opens each named input in turn; the worst status wins.

import std.cli
import std.fs

/// A line whose bytes are not UTF-8, counted from 1 in its input.
export error BadUtf8(line: int)

/// A line `Reader.Line` could not return, counted from 1: longer than its
/// buffer of `limit` bytes, or not UTF-8. A `limit` of 0 is standard input's
/// own buffer, whose size no document gives.
export error Unreadable(line: int, limit: int)

/// An input larger than the limit a whole-input command reads.
export error TooLarge(limit: int)

/// What reading an input can fail with. `WriteError` is the output's.
export type Error = BadUtf8 | Unreadable | TooLarge | ReadError | WriteError | fs.Error | OutOfMemory

/// The exit status of a run that did what was asked.
export const Ok: int = 0
/// Nothing matched, or something was not in the asked form.
export const NoMatch: int = 1
/// A bad command line.
export const Usage: int = 2
/// An input could not be opened or read, or was not UTF-8.
export const Failed: int = 3
/// Memory ran out, or a memory limit was reached.
export const NoMemory: int = 4

/// The capabilities every subcommand shares: the program's arena, the heap
/// for what outlives one line, and the I/O handle.
export struct Ctx {
    export arena: &Arena
    export heap: &Heap
    export io: Io
}

/// Standard output and standard error.
export struct Streams {
    export out: Writer
    export err: Writer
}

/// The default size of a read buffer, in bytes.
export const BufferSize: int = 16384

/// Reports `name: e` on `s.err` with the program's prefix, after flushing
/// `s.out` so the two streams keep their order on a terminal. A failed
/// write to standard error has nowhere else to go, so it is dropped.
export Report[E: Format](s: mut Streams, name: str, e: &E) {
    s.out.Flush() else {}
    Print(mut s.err, "textkit: {name}: {e}\n", (name, e)) else {}
}

/// Reports `name:line: e`, as `Report` does.
export ReportAt[E: Format](s: mut Streams, name: str, line: int, e: &E) {
    s.out.Flush() else {}
    Print(mut s.err, "textkit: {name}:{line}: {e}\n", (name, line, e)) else {}
}

/// The exit status for a failed input: `NoMemory` for `OutOfMemory`, else `Failed`.
export StatusOf(e: &Error): int {
    return match *e {
        OutOfMemory { NoMemory }
        _ { Failed }
    }
}

/// The worse of two exit statuses: the higher one.
export Worse(a: int, b: int): int {
    return if b > a { b } else { a }
}

// Found before Parse, which has no short options and would refuse `-h`
// (a `help` field makes only `--help` a flag).
/// Whether `args` asks for help: `--help` or `-h` before any `--`.
export WantsHelp(args: []str): bool {
    for arg in args {
        if arg == "--" {
            return false
        }
        if arg == "--help" || arg == "-h" {
            return true
        }
    }
    return false
}

/// Prints the help of subcommand `name`: its synopsis line, then the
/// options `usage` writes. Returns the exit status.
export Help(s: mut Streams, synopsis: str, usage: fn(mut Writer): WriteError!void): int {
    Print(mut s.out, "usage: textkit {synopsis}\n", (synopsis)) else { return Failed }
    usage(mut s.out) else { return Failed }
    return Ok
}

/// Reports a command line `cli` refused, naming the argument, then the
/// options `usage` writes, on standard error. Returns `Usage`, or
/// `NoMemory` when parsing ran out of memory.
export BadArgs(s: mut Streams, command: str, args: []str, e: &cli.Error, usage: fn(mut Writer): WriteError!void): int {
    s.out.Flush() else {}
    match *e {
        cli.Unknown(arg) {
            Print(mut s.err, "textkit {command}: unknown option {a}\n", (command, a: Arg(args, arg))) else {}
        }
        cli.Missing(field) {
            Print(mut s.err, "textkit {command}: missing argument {field}\n", (command, field)) else {}
        }
        cli.Invalid(arg) {
            Print(mut s.err, "textkit {command}: invalid argument {a}\n", (command, a: Arg(args, arg))) else {}
        }
        OutOfMemory {
            Print(mut s.err, "textkit {command}: out of memory\n", (command)) else {}
            return NoMemory
        }
        _ {
            Panic("cli.Error gained a member this match does not handle")
        }
    }
    usage(mut s.err) else {}
    return Usage
}

/// `args[i]`, or `"(end)"` when `i` is past the end.
Arg(args: []str, i: int): str {
    return if i >= 0 && i < args.Len() { args[i] } else { "(end)" }
}

/// Runs `step` with `state` over every input named in `paths`, in order: `-` is
/// standard input, and no path at all means standard input alone. A file is
/// read through a buffer of `size` bytes, allocated once in `c.arena`. An
/// input that fails is reported on `s.err` and the run goes on with the next.
/// Returns the worst exit status: `Ok`, `Failed` or `NoMemory`.
/// `step` may keep what it reads in `state`'s own memory.
export ForEach[S](c: &Ctx, s: mut Streams, stdin: mut Reader, paths: []str, size: int, state: mut S, const step: fn(c: &Ctx, state: mut S, s: mut Streams, r: mut Reader, name: str): Error!void): int {
    var status := Ok
    buf := c.arena.Slice[u8](if size < 1 { 1 } else { size }) else {
        Report(s, "buffer", &OutOfMemory)
        return NoMemory
    }
    if paths.Len() == 0 {
        step(c, state, s, stdin, "-") else e {
            Report(s, "-", &e)
            return StatusOf(&e)
        }
        return Ok
    }
    for path in paths {
        if path == "-" {
            step(c, state, s, stdin, "-") else e {
                Report(s, "-", &e)
                status = Worse(status, StatusOf(&e))
            }
            continue
        }
        var f := fs.Open(c.io, path) else e {
            Report(s, path, &e)
            status = Worse(status, Failed)
            continue
        }
        var r := f.Reader(c.io, buf)
        step(c, state, s, mut r, path) else e {
            f.Abandon()
            Report(s, path, &e)
            status = Worse(status, StatusOf(&e))
            continue
        }
        f.Close(c.io) else e {
            f.Abandon()
            Report(s, path, &e)
            status = Worse(status, Failed)
        }
    }
    return status
}

/// The value of a size option `flag`, or `fallback` when it was not given,
/// or `none` after reporting a value below `least` on `s.err`.
export Size(s: mut Streams, command: str, flag: str, value: ?int, fallback: int, least: int): ?int {
    n := value else { fallback }
    if n < least {
        Print(mut s.err, "textkit {command}: {flag} must be at least {least}, not {n}\n", (command, flag, least, n)) else {}
        return none
    }
    return some(n)
}
