// Why: The `lox` command: runs a script file, code given with --eval, or a REPL
// on standard input.
//
// What: Options, Main, and Session, which loads and runs one entry at a time.
//
// How: Exit codes are clox's (sysexits.h): 64 usage, 65 compile error, 70
// runtime error, 74 I/O error. One Session holds the program and the VM, so
// REPL entries share globals.

import std.cli
import std.fs
import src.code
import src.syntax
import src.vm

struct Options {
    /// Print this help and exit
    export help: bool
    /// List each compiled function's bytecode before it runs
    export disasm: bool
    /// Print heap and collection counts to stderr when done
    export stats: bool
    /// Count the calls of each function and print them to stderr when done
    export profile: bool
    /// Print one line to stderr for every collection
    export gc_log: bool
    /// Collect before every allocation (slow; finds values the collector misses)
    export gc_stress: bool
    /// The deepest call nesting (default 256)
    export max_frames: ?int
    /// The most live objects a collection may leave (default: no limit)
    export max_objects: ?int
    /// The seed of random(), above 0 (default 1)
    export seed: ?int
    /// Define a global before the script runs, as name=value
    export define: ?str
    /// Run this code instead of a file
    export eval: ?str
    /// The script to run; with none, and no --eval, read entries from standard input
    export files: []str
}

gen cli.Args(Options)

const Usage: int = 64
const DataError: int = 65
const Software: int = 70
const IoError: int = 74

/// What one run of some source ended with.
enum Outcome {
    Done
    CompileFailed
    RuntimeFailed
}

/// Standard output and standard error.
struct Term {
    out: Writer
    err: Writer
}

/// The program, the VM and the settings every entry shares, and the entry loaded to run
/// next.
struct Session {
    a: &Arena
    prog: code.Program
    vm: vm.Vm
    disasm: bool
    src: str
    line: int
}

export Main(sys: &System): WriteError!int {
    var t := Term(out: sys.Stdout(), err: sys.Stderr())
    args := sys.args[1..]
    opts := Options.Parse(sys.arena, args) else e {
        match e {
            cli.Unknown(arg) { try Print(mut t.err, "lox: unknown option '{o}'\n", (o: args[arg])) }
            cli.Invalid(arg) { try Print(mut t.err, "lox: invalid or missing value at '{v}'\n", (v: args[arg])) }
            cli.Missing(field: _) { try t.err.Str("lox: a required argument is missing\n") }
            OutOfMemory { try t.err.Str("lox: out of memory\n") }
            _ {
                Panic("cli.Error gained a member this match does not handle")
            }
        }
        try Options.Usage(mut t.err)
        return Usage
    }
    if opts.help {
        try t.out.Str("usage: lox [options] [script]\n\n")
        try Options.Usage(mut t.out)
        return 0
    }
    if opts.files.Len() > 1 {
        try Print(mut t.err, "lox: one script at a time, got {n}\n", (n: opts.files.Len()))
        return Usage
    }
    settings := Settings(&opts) else e {
        try Print(mut t.err, "lox: invalid option: {e}\n", (e))
        return Usage
    }
    // The VM and the program hold their own arena: they store values that hold it back
    // into it, which only a local arena can be declared to take.
    var arena := Arena.New(sys.heap) else {
        try t.err.Str("lox: out of memory\n")
        return Software
    }
    defer arena.Free()
    return try Interpret(sys, &arena, mut t, &opts, settings)
}

// Runs the session the options ask for, with the VM and the program in `a`.
// A function of its own, so what the session stores into itself reaches the
// arena `a` and stops there.
Interpret(sys: &System, a: &Arena, t: mut Term, opts: &Options, settings: vm.Options): WriteError!int *opts into a {
    var machine := vm.Vm.New(a, settings) else {
        try t.err.Str("lox: out of memory\n")
        return Software
    }
    machine.logging = opts.gc_log
    machine.profiling = opts.profile
    var s := Session(a: a, prog: code.Program.New(a), vm: machine, disasm: opts.disasm, src: "", line: 1)
    if d := opts.define {
        status := try Define(mut s, t, d)
        if status != 0 {
            return status
        }
    }
    var status := 0
    if src := opts.eval {
        s.Load(src, 1)
        status = Exit(try s.Run(t))
    } else if opts.files.Len() == 1 {
        path := opts.files[0]
        src := fs.ReadText(a, sys.io, path) else e {
            try Print(mut t.err, "lox: cannot read {path}: {e}\n", (path, e))
            return IoError
        }
        s.Load(src, 1)
        status = Exit(try s.Run(t))
    } else {
        var input := sys.Stdin()
        status = try Repl(mut s, t, mut input)
    }
    if opts.profile {
        s.vm.Profile(a, mut t.err, &s.prog) else e {
            match e {
                OutOfMemory {
                    try t.err.Str("lox: out of memory\n")
                    return Software
                }
                WriteError { fail WriteError }
            }
        }
    }
    if opts.stats {
        h := s.vm.Heap()
        try Print(mut t.err, "stats: {live} objects live, {runs} collections, {freed} freed, {draws} random draws\n", (live: h.Len(), runs: h.collections, freed: h.freed, draws: s.vm.RandomCalls()))
    }
    return status
}

error MaxFramesBelowOne(given: int)
error MaxObjectsBelowZero(given: int)
error SeedBelowOne(given: int)

type BadLimit = MaxFramesBelowOne | MaxObjectsBelowZero | SeedBelowOne

/// The VM settings the options ask for.
Settings(opts: &Options): BadLimit!vm.Options {
    frames := opts.max_frames else { 256 }
    if frames < 1 {
        fail MaxFramesBelowOne(given: frames)
    }
    objects := opts.max_objects else { 0 }
    if objects < 0 {
        fail MaxObjectsBelowZero(given: objects)
    }
    seed := opts.seed else { 1 }
    if seed < 1 {
        fail SeedBelowOne(given: seed)
    }
    return vm.Options(max_frames: frames, max_objects: objects, stress: opts.gc_stress, seed: Cast[u64](seed))
}

/// Applies `--define name=value`; returns the exit status, 0 when it worked.
Define(s: mut Session, t: mut Term, d: str into s): WriteError!int {
    eq := d.Find("=") else {
        try Print(mut t.err, "lox: --define takes name=value, got '{d}'\n", (d))
        return Usage
    }
    name := d[..eq]
    if name.Len() == 0 || syntax.IsKeyword(name) {
        try Print(mut t.err, "lox: --define needs a variable name, got '{name}'\n", (name))
        return Usage
    }
    _ = s.vm.Define(name, d[eq + 1..]) else {
        try t.err.Str("lox: out of memory\n")
        return Software
    }
    return 0
}

/// The exit status of a run.
Exit(o: Outcome): int {
    return match o {
        .Done { 0 }
        .CompileFailed { DataError }
        .RuntimeFailed { Software }
    }
}

// A workaround: Load and Run would be one call taking `src`; a call that both stores
// an argument and stores the session into itself cannot be made from Main.
/// Makes `src`, whose first line is numbered `line`, the entry the next `Run` runs.
Session.Load(mut self, src: str into self, line: int) {
    self.src = src
    self.line = line
}

/// Parses, compiles and runs the loaded entry, reporting every error on standard error as
/// clox does.
Session.Run(mut self, t: mut Term): WriteError!Outcome *self into self {
    parsed := syntax.Parse(self.a, self.src, self.line) else {
        try t.err.Str("lox: out of memory\n")
        return Outcome.RuntimeFailed
    }
    if parsed.errors.Len() > 0 {
        for e in parsed.errors {
            try ReportSyntax(mut t.err, &e)
        }
        return Outcome.CompileFailed
    }
    syntax.Parsed(ast, top, errors: syntax_errors) := parsed
    _ = syntax_errors
    first := self.prog.funs.Len()
    var c := code.Compiler.New(self.a, ast, Replace(mut self.prog, code.Program.New(self.a)))
    // The program goes back into the session whether or not the compile worked.
    script := match c.Script(top) {
        ok(i) { i }
        err(_) { -1 }
    }
    (prog, errors) := c.Finish()
    self.prog = prog
    // Nested functions are compiled after their enclosing one: report in line order.
    var found := errors
    found.SortStableInPlace(code.ByLine)
    if script < 0 {
        try t.err.Str("lox: out of memory\n")
        return Outcome.RuntimeFailed
    }
    if found.Len() > 0 {
        for e in found {
            try Print(mut t.err, "[line {line}] Error: {message}\n", (line: e.line, message: e.message))
        }
        return Outcome.CompileFailed
    }
    if self.disasm {
        for i in first..self.prog.funs.Len() {
            try self.prog.Disassemble(mut t.out, i)
        }
    }
    self.vm.Run(self.a, &self.prog, script, mut t.out) else e {
        try FlushLog(mut t.err, mut self.vm)
        match e {
            vm.Runtime { try t.err.Str(self.vm.Message()) }
            OutOfMemory { try t.err.Str("lox: out of memory\n") }
            WriteError { fail WriteError }
        }
        return Outcome.RuntimeFailed
    }
    try FlushLog(mut t.err, mut self.vm)
    return Outcome.Done
}

/// Writes and clears the collection log.
FlushLog(err: mut Writer, machine: mut vm.Vm): WriteError!void {
    try err.Str(machine.log.Str())
    machine.log.Clear()
}

/// `[line 3] Error at 'x': message`, as clox prints a syntax error.
ReportSyntax(err: mut Writer, e: &syntax.SyntaxError): WriteError!void {
    match e.at {
        .AtEnd { try Print(err, "[line {line}] Error at end: {m}\n", (line: e.line, m: e.message)) }
        .InToken { try Print(err, "[line {line}] Error: {m}\n", (line: e.line, m: e.message)) }
        .AtToken { try Print(err, "[line {line}] Error at '{lexeme}': {m}\n", (line: e.line, lexeme: e.lexeme, m: e.message)) }
    }
}
