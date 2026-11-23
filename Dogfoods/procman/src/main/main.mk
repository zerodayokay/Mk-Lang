// Why: procman is a process manager and task runner in the manner of
// foreman; this module holds Main, the command line and the wiring.
//
// What: Main, Dispatch, Usage, LoadProcfile and the error reports.
//
// How: the work is in the modules it imports. Exit codes: 0 done; 1 a
// process failed; 2 bad usage or a bad Procfile; 3 the run directory
// failed; 4 the status port did not answer; 125 to 127 for `run` as `env`
// gives them; 128 plus the signal's number when a signal stopped `start`.

import std.cli
import std.fs
import std.log
import std.proc
import src.demo
import src.procfile

export Main(sys: &System) {
    code := Dispatch(sys) else {
        // stdout or stderr is gone, so nothing can be reported
        1
    }
    proc.Exit(code)
}

Dispatch(sys: &System): WriteError!int {
    opts := Options.Parse(sys.arena, sys.args[1..]) else e {
        try ArgsError(sys, e)
        return 2
    }
    if opts.help {
        var out := sys.Stdout()
        try Usage(mut out)
        return 0
    }
    cfg := Configure(sys, &opts) else e {
        match e {
            BadLevel { try sys.PrintErr("procman: --log-level takes debug, info, warn or error\n") }
            BadWorkers { try sys.PrintErr("procman: --workers takes 1 to 64\n") }
            BadTailMemory { try sys.PrintErr("procman: --tail-memory takes 0 to 67108864 bytes\n") }
            BadStopAfter { try sys.PrintErr("procman: --stop-after takes a count of milliseconds\n") }
        }
        return 2
    }
    return match cfg.command {
        "start" { try Start(sys, &cfg) }
        "run" { try RunTask(sys, &cfg) }
        "check" { try Check(sys, &cfg) }
        "status" { try Status(sys, &cfg) }
        "ps" { try Ps(sys, &cfg) }
        "logs" { try ShowLogs(sys, &cfg) }
        "clean" { try Clean(sys, &cfg) }
        "demo" { try Demo(sys, &cfg) }
        "help" { try HelpCommand(sys) }
        _ {
            try sys.PrintErr("procman: unknown command `{c}`; try `procman help`\n", (c: cfg.command))
            2
        }
    }
}

HelpCommand(sys: &System): WriteError!int {
    var out := sys.Stdout()
    try Usage(mut out)
    return 0
}

Usage(w: mut Writer): WriteError!void {
    try w.Str(Synopsis)
    try Options.Usage(w)
}

// The generated parser reports where in `args` it stopped, by index, since
// an error holds no text.
ArgsError(sys: &System, e: cli.Error): WriteError!void {
    match e {
        cli.Unknown(arg) {
            try sys.PrintErr("procman: unknown option `{a}`\n", (a: sys.args[arg + 1]))
        }
        cli.Invalid(arg) {
            try sys.PrintErr("procman: `{a}` is not a valid value here\n", (a: sys.args[arg + 1]))
        }
        cli.Missing(field) {
            try sys.PrintErr("procman: option {field} is missing\n", (field))
        }
        OutOfMemory {
            try sys.PrintErr("procman: out of memory\n")
        }
        _ {
            Panic("cli.Error gained a member this match does not handle")
        }
    }
    try sys.PrintErr("run `procman --help` for the options and commands\n")
}

Demo(sys: &System, cfg: &Config): WriteError!int {
    if cfg.args.Len() == 1 && cfg.args[0] == "help" {
        for p in demo.Programs {
            try sys.Print("{p}\n", (p))
        }
        return 0
    }
    return try demo.Run(sys, cfg.args)
}

// Reads and parses the Procfile, reporting a failure the way a compiler
// does: file, line and column, the line, and a caret under the column.
LoadProcfile(sys: &System, cfg: &Config, logs: mut Logs): WriteError!?List[procfile.Entry] from sys {
    text := ReadProcfile(sys, cfg) else {
        return none
    }
    entries := procfile.Parse(sys.arena, text, fn(name: str, out: mut Text): OutOfMemory!bool {
        if name == "PROCMAN" {
            try out.Push(sys.args[0])
            return true
        }
        value := sys.Env(name) else {
            return false
        }
        try out.Push(value)
        return true
    }) else e {
        try ProcfileError(sys, cfg.procfile, text, e)
        return none
    }
    logs.Debug("read {path}: {n} entries", (path: cfg.procfile, n: entries.Len()))
    return some(entries)
}

ReadProcfile(sys: &System, cfg: &Config): ?str from sys {
    text := fs.ReadText(sys.arena, sys.io, cfg.procfile) else e {
        sys.PrintErr("procman: {path}: {e}\n", (path: cfg.procfile, e)) else {}
        return none
    }
    return some(text)
}

ProcfileError(sys: &System, path: str, text: str, e: procfile.Error): WriteError!void {
    (line, col, what) := match e {
        procfile.Syntax(line, col) { (line, col, what: "syntax error: write `name: command` or `name [options]: command`") }
        procfile.UnclosedQuote(line, col) { (line, col, what: "this quote is never closed") }
        procfile.UnknownOption(line, col) { (line, col, what: "unknown option: the options are restart, retries, backoff, limit and main") }
        procfile.BadValue(line, col) { (line, col, what: "bad value for this option") }
        procfile.Duplicate(line, first) {
            try sys.PrintErr("procman: {path}:{line}: this name is already declared on line {first}\n", (path, line, first))
            return
        }
        procfile.UnknownVariable(line, col) { (line, col, what: "unknown variable: set it in the environment, or write $$ for a dollar") }
        procfile.NoEntries {
            try sys.PrintErr("procman: {path}: no processes or tasks\n", (path))
            return
        }
        OutOfMemory {
            try sys.PrintErr("procman: {path}: out of memory\n", (path))
            return
        }
    }
    try sys.PrintErr("procman: {path}:{line}:{col}: {what}\n", (path, line, col, what))
    var n := 0
    for l in text.Lines() {
        n += 1
        if n == line {
            try sys.PrintErr("    {l}\n", (l))
            var caret := Text.New(sys.arena)
            for _ in 1..col {
                caret.Push(" ") else {}
            }
            try sys.PrintErr("    {c}^\n", (c: caret.Str()))
            return
        }
    }
}

ConsoleLogs(err: mut Writer, level: log.Level): Logs from err {
    return Logs(console: log.Logger.New(err, level), file: none)
}
