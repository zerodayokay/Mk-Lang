// Why: Runs compiled Lox programs.
//
// What: Vm, its Options, the Runtime error, Vm.Run, the globals (Define,
// Forget, ListGlobals) and the call profile Vm.Profile.
//
// How: One value stack, the running frame in a local and the suspended callers
// in a deque, globals in a B-tree so the REPL lists them in name order
// (Crafting Interpreters, chapters 15 to 24). Collection runs only between
// instructions, when every live value is on the stack, in a global or in a
// constant's cache.

import std.coll
import std.mem
import src.code

/// A runtime error. Its message and stack trace are in `Vm.Message`.
export error Runtime

/// The settings of a VM.
export struct Options {
    /// The deepest call nesting; one more is "Stack overflow."
    export max_frames: int
    /// The most live objects after a collection, 0 for no limit.
    export max_objects: int
    /// Collect before every allocation, to find a value the roots miss.
    export stress: bool
    /// The seed of `random()`, above 0.
    export seed: u64
}

struct Frame {
    fun: int
    ip: int
    base: int
}

/// A Lox virtual machine. It keeps its globals and heap across `Run` calls, so a REPL
/// runs each entry in the same world.
export mut view struct Vm {
    host: Host
    natives: List[Entry]
    stack: List[code.Value]
    frames: coll.Deque[Frame]
    globals: coll.BTree[str, code.Value]
    report: Text
    seen: Set[mem.Handle]
    max_frames: int
    /// Each collection's counts, one line each, for `--gc-log`.
    export log: Text
    export logging: bool
    /// Calls per function while profiling: a function's index, or -1 - n for native n.
    calls: Map[int, int]
    export profiling: bool
}

/// A VM in `a` with every native defined as a global.
export Vm.New(a: &Arena, opts: Options): OutOfMemory!Vm from a {
    var host := Host.New(a, Heap.New(a, opts.max_objects, opts.stress))
    try ShareState(a, mut host, opts.seed)
    natives := try Install(a)
    var globals := coll.BTree[str, code.Value].New(a)
    for i, e in &natives {
        v := try host.heap.NewNative(i)
        try globals.Set(e.name, v)
    }
    return Vm(host: host, natives: natives, stack: List[code.Value].New(a), frames: coll.Deque[Frame].New(a), globals: globals, report: Text.New(a), seen: Set[mem.Handle].New(a), max_frames: opts.max_frames, log: Text.New(a), logging: false, calls: Map[int, int].New(a), profiling: false)
}

/// The message and stack trace of the last runtime error.
export Vm.Message(&self): str from self {
    return self.report.Str()
}

/// The heap's counters: collections run and objects freed.
export Vm.Heap(&self): &Heap from self {
    return &self.host.heap
}

/// How many numbers `random()` has drawn.
export Vm.RandomCalls(&self): int {
    r := self.host.Peek[Random]() else { return 0 }
    return r.calls
}

/// Defines global `name` before a script runs, as `--define` does, and reports whether it
/// replaced a value.
export Vm.Define(mut self, name: str into self, value: str): OutOfMemory!bool {
    // A value that parses as a number is one; anything else is a string.
    v := match value.ParseFloat() {
        ok(n) { code.Value.Num(n: n) }
        err(_) { try self.host.heap.NewString(value) }
    }
    old := try self.globals.Replace(name, v)
    return old.IsSome()
}

/// Removes global `name`, as the REPL's `:forget` does; false when there is none.
export Vm.Forget(mut self, name: str): bool {
    if !self.globals.Has(&name) {
        return false
    }
    _ = self.globals.Remove(&name)
    return true
}

/// Writes the globals whose names are in [lo, hi), or every global when `lo` is empty,
/// one `name = value` line each in name order.
export Vm.ListGlobals(mut self, w: mut Writer, lo: str, hi: str): WriteError|OutOfMemory!void {
    var it := if lo.Len() == 0 { self.globals.Iter() } else { self.globals.Range(&lo, &hi) }
    var shown := 0
    for (key, value) in it {
        try Print(w, "  {name} = ", (name: *key))
        try self.host.heap.Write(w, *value, mut self.seen)
        try w.Str("\n")
        shown += 1
    }
    try Print(w, "{shown} of {n} globals\n", (shown, n: self.globals.Len()))
}

/// Runs function `script` of `prog` to its end, writing what `print` prints to `out`.
/// A runtime error fails with `Runtime`, and the VM is ready for the next `Run`.
export Vm.Run(mut self, a: &Arena, prog: &code.Program, script: int, out: mut Writer): Runtime|OutOfMemory|WriteError!void *prog into self {
    for self.stack.Len() > 0 {
        _ = self.stack.Pop()
    }
    for self.frames.Len() > 0 {
        _ = self.frames.Pop()
    }
    fv := try self.FunctionValue(prog, script)
    try self.stack.Push(fv)
    var fr := Frame(fun: script, ip: prog.funs[script].code, base: 0)
    for {
        f := &prog.funs[fr.fun]
        op := prog.ops[fr.ip]
        fr.ip += 1
        match op {
            .Constant(index) {
                v := try self.Constant(prog, prog.Constant(f, index))
                try self.stack.Push(v)
                try self.MaybeCollect(a, prog, fr)
            }
            .Nil { try self.stack.Push(code.Value.Nil) }
            .True { try self.stack.Push(code.Value.Bool(b: true)) }
            .False { try self.stack.Push(code.Value.Bool(b: false)) }
            .Pop { _ = self.stack.Pop() }
            .GetLocal(slot) {
                v := self.stack[fr.base + slot]
                try self.stack.Push(v)
            }
            .SetLocal(slot) { self.stack[fr.base + slot] = self.Peek(0) }
            .GetGlobal(name) {
                text := Name(prog.Constant(f, name))
                v := self.globals.Get(&text) else {
                    try self.Fault(prog, fr, "Undefined variable '{text}'.", (text))
                    fail Runtime
                }
                try self.stack.Push(*v)
            }
            .DefineGlobal(name) {
                text := Name(prog.Constant(f, name))
                v := self.Pop()
                try self.globals.Set(text, v)
            }
            .SetGlobal(name) {
                text := Name(prog.Constant(f, name))
                v := self.Peek(0)
                slot := self.globals.GetMut(&text) else {
                    try self.Fault(prog, fr, "Undefined variable '{text}'.", (text))
                    fail Runtime
                }
                *slot = v
            }
            .Equal {
                rhs := self.Pop()
                lhs := self.Pop()
                try self.stack.Push(code.Value.Bool(b: self.Equal(lhs, rhs)))
            }
            .Greater | .Less | .Subtract | .Multiply | .Divide | .Modulo {
                rhs := self.Pop()
                lhs := self.Pop()
                (x, y) := Numbers(lhs, rhs) else {
                    try self.Fault(prog, fr, "Operands must be numbers.")
                    fail Runtime
                }
                r := match op {
                    .Greater { code.Value.Bool(b: x > y) }
                    .Less { code.Value.Bool(b: x < y) }
                    .Subtract { code.Value.Num(n: x - y) }
                    .Multiply { code.Value.Num(n: x * y) }
                    .Divide { code.Value.Num(n: x / y) }
                    _ { code.Value.Num(n: Remainder(x, y)) }
                }
                try self.stack.Push(r)
            }
            .Add {
                rhs := self.Pop()
                lhs := self.Pop()
                if p := Numbers(lhs, rhs) {
                    try self.stack.Push(code.Value.Num(n: p.x + p.y))
                } else if self.host.heap.Str(lhs).IsSome() && self.host.heap.Str(rhs).IsSome() {
                    // The operands stay on the stack while the result is made, so a
                    // collection that the allocation triggers later still sees them.
                    try self.stack.Push(lhs)
                    try self.stack.Push(rhs)
                    v := try self.host.heap.Concat(lhs, rhs)
                    _ = self.stack.Pop()
                    _ = self.stack.Pop()
                    try self.stack.Push(v)
                    try self.MaybeCollect(a, prog, fr)
                } else {
                    try self.Fault(prog, fr, "Operands must be two numbers or two strings.")
                    fail Runtime
                }
            }
            .Not {
                v := self.Pop()
                try self.stack.Push(code.Value.Bool(b: v.Falsey()))
            }
            .Negate {
                v := self.Pop()
                match v {
                    .Num(n) { try self.stack.Push(code.Value.Num(n: -n)) }
                    _ {
                        try self.Fault(prog, fr, "Operand must be a number.")
                        fail Runtime
                    }
                }
            }
            .Print {
                v := self.Pop()
                try self.host.heap.Write(out, v, mut self.seen)
                try out.Str("\n")
            }
            .Jump(to) { fr.ip = to }
            .JumpIfFalse(to) {
                if self.Peek(0).Falsey() {
                    fr.ip = to
                }
            }
            .Loop(to) { fr.ip = to }
            .Call(argc) { fr = try self.Call(a, prog, fr, argc) }
            .Array(count) {
                list := try self.host.heap.NewArray(self.stack.Items(), count)
                for _ in 0..count {
                    _ = self.stack.Pop()
                }
                try self.stack.Push(list)
                try self.MaybeCollect(a, prog, fr)
            }
            .GetIndex {
                index := self.Pop()
                target := self.Pop()
                v := try self.Index(prog, fr, target, index, none)
                try self.stack.Push(v)
            }
            .SetIndex {
                value := self.Pop()
                index := self.Pop()
                target := self.Pop()
                _ = try self.Index(prog, fr, target, index, some(value))
                try self.stack.Push(value)
            }
            .Return {
                result := self.Pop()
                for self.stack.Len() > fr.base {
                    _ = self.stack.Pop()
                }
                caller := self.frames.Pop() else { return }
                fr = caller
                try self.stack.Push(result)
            }
        }
    }
}

/// The text of a name constant; the compiler only names globals by string constants.
Name(c: &code.Constant): str {
    match c.key {
        .Str(text) { return text }
        _ { return "" }
    }
}

/// Both operands as numbers, if both are.
Numbers(a: code.Value, b: code.Value): ?(x: f64, y: f64) {
    match a {
        .Num(n: x) {
            match b {
                .Num(n: y) { return some((x: x, y: y)) }
                _ { return none }
            }
        }
        _ { return none }
    }
}

/// `x % y` with the sign of `x`, as C's fmod: x - y * trunc(x / y), exact for whole
/// operands below 2^53. A zero `y`, or an infinite `x`, gives NaN.
Remainder(x: f64, y: f64): f64 {
    if y == 0.0 || x - x != 0.0 {
        return 0.0 / 0.0
    }
    q := x / y
    if q - q != 0.0 || q >= 9007199254740992.0 || q <= -9007199254740992.0 {
        return x
    }
    return x - y * Cast[f64](Cast[i64](q))
}

Vm.Pop(mut self): code.Value {
    // The compiler balances every pop with a push, so the stack is never empty here.
    return self.stack.Pop().Must()
}

Vm.Peek(&self, depth: int): code.Value {
    return self.stack[self.stack.Len() - 1 - depth]
}

/// Lox equality: numbers by value (NaN equals nothing), strings by text, other objects by
/// identity.
Vm.Equal(&self, a: code.Value, b: code.Value): bool {
    if a.Eq(&b) {
        return match a {
            .Num(n) { n == n }
            _ { true }
        }
    }
    sa := self.host.heap.Str(a) else { return false }
    sb := self.host.heap.Str(b) else { return false }
    return sa == sb
}

/// The value of a constant. A string or function constant makes its object the first
/// time and keeps it in the constant's cell, which the collector treats as a root.
Vm.Constant(mut self, prog: &code.Program, c: &code.Constant): OutOfMemory!code.Value *prog into self {
    if v := c.object.Load() {
        return v
    }
    v := match c.key {
        .Num(bits) { return code.Value.Num(n: f64.FromBits(bits)) }
        .Str(text) { try self.host.heap.NewString(text) }
        .Fun(index) { try self.FunctionValue(prog, index) }
    }
    c.object.Store(some(v))
    return v
}

Vm.FunctionValue(mut self, prog: &code.Program, index: int): OutOfMemory!code.Value *prog into self {
    return try self.host.heap.NewFunction(index, prog.funs[index].name)
}

/// Calls the value `argc` slots below the top with the `argc` values above it, and
/// returns the frame to run next.
Vm.Call(mut self, a: &Arena, prog: &code.Program, fr: Frame, argc: int): Runtime|OutOfMemory|WriteError!Frame {
    callee := self.Peek(argc)
    o := self.host.heap.Get(callee) else {
        try self.Fault(prog, fr, "Can only call functions.")
        fail Runtime
    }
    match o {
        .Fun(index: at) {
            index := *at
            arity := prog.funs[index].arity
            if argc != arity {
                try self.Fault(prog, fr, "Expected {arity} arguments but got {argc}.", (arity, argc))
                fail Runtime
            }
            if self.frames.Len() + 1 >= self.max_frames {
                try self.Fault(prog, fr, "Stack overflow.")
                fail Runtime
            }
            try self.Count(index)
            try self.frames.Push(fr)
            return Frame(fun: index, ip: prog.funs[index].code, base: self.stack.Len() - argc - 1)
        }
        .Native(index: at) {
            index := *at
            arity := self.natives[index].fun.Arity()
            if argc != arity {
                try self.Fault(prog, fr, "Expected {arity} arguments but got {argc}.", (arity, argc))
                fail Runtime
            }
            try self.Count(-1 - index)
            n := self.stack.Len()
            args := self.stack.Items()[n - argc..n]
            r := self.natives[index].fun.Call(mut self.host, args) else e {
                name := self.natives[index].name
                try self.Fault(prog, fr, "{name}: {e}.", (name, e))
                fail Runtime
            }
            for _ in 0..argc + 1 {
                _ = self.stack.Pop()
            }
            try self.stack.Push(r)
            try self.MaybeCollect(a, prog, fr)
            return fr
        }
        _ {
            try self.Fault(prog, fr, "Can only call functions.")
            fail Runtime
        }
    }
}

/// Counts a call of `key` when profiling.
Vm.Count(mut self, key: int): OutOfMemory!void {
    if self.profiling {
        n := try self.calls.Ensure(key, 0)
        *n += 1
    }
}

/// One row of the profile: the function's key in `Vm.calls` and its calls.
struct Calls {
    key: int
    count: int
}

/// Most calls first, then in key order, so the profile is the same on every run.
ByCount(a: &Calls, b: &Calls): bool {
    if a.count != b.count {
        return a.count > b.count
    }
    return a.key < b.key
}

/// Writes the calls counted while profiling, most called first, one function a line.
export Vm.Profile(&self, a: &Arena, w: mut Writer, prog: &code.Program): WriteError|OutOfMemory!void {
    var rows := List[Calls].New(a)
    for (key, value) in self.calls.Iter() {
        try rows.Push(Calls(key: *key, count: *value))
    }
    rows.Sort(ByCount)
    try Print(w, "profile: {n} functions called\n", (n: self.calls.Len()))
    for r in rows {
        if r.key >= 0 {
            try Print(w, "{count:>9}  lox     {name}\n", (count: r.count, name: prog.funs[r.key].name))
        } else {
            try Print(w, "{count:>9}  native  {name}\n", (count: r.count, name: self.natives[-1 - r.key].name))
        }
    }
}

/// Reads, or with `value` writes, item `index` of list `target`.
Vm.Index(mut self, prog: &code.Program, fr: Frame, target: code.Value, index: code.Value, value: ?code.Value): Runtime|WriteError!code.Value {
    len := self.host.heap.ArrayLen(target) else {
        try self.Fault(prog, fr, "Only lists can be indexed.")
        fail Runtime
    }
    i := Whole(index) else {
        try self.Fault(prog, fr, "Index must be a whole number.")
        fail Runtime
    }
    if i < 0 || i >= len {
        try self.Fault(prog, fr, "Index {i} is out of range for a list of {len}.", (i, len))
        fail Runtime
    }
    if v := value {
        _ = self.host.heap.ArraySet(target, i, v)
        return v
    }
    // The index was checked against the length just above.
    return self.host.heap.ArrayGet(target, i).Must()
}

/// Collects when the heap asks for it, and fails when more objects than the limit
/// survive.
Vm.MaybeCollect(mut self, a: &Arena, prog: &code.Program, fr: Frame): Runtime|OutOfMemory|WriteError!void {
    if !self.host.heap.Due() {
        return
    }
    var scratch := try a.Sub()
    defer scratch.Free()
    var roots := List[code.Value].New(&scratch)
    for v in &self.stack {
        try roots.Push(*v)
    }
    for (key, value) in self.globals.Iter() {
        _ = key
        try roots.Push(*value)
    }
    for c in &prog.consts {
        if v := c.object.Load() {
            try roots.Push(v)
        }
    }
    done := try self.host.heap.Collect(&scratch, roots.Items())
    if self.logging {
        var w := self.log.Writer()
        try Print(mut w, "gc: {live} live, {freed} freed\n", (live: done.live, freed: done.freed))
    }
    limit := self.host.heap.limit
    if limit > 0 && done.live > limit {
        try self.Fault(prog, fr, "Out of memory: {live} objects live, the limit is {limit}.", (live: done.live, limit))
        fail Runtime
    }
}

/// Writes a runtime error's message and the stack trace, innermost call first, into the
/// report, as clox does.
Vm.Fault[A: Fields[Format] + Plain](mut self, prog: &code.Program, fr: Frame, const f: str, args: A): WriteError!void {
    self.report.Clear()
    var w := self.report.Writer()
    try Print(mut w, f, args)
    try w.Str("\n")
    try TraceLine(mut w, prog, fr)
    // Callers, innermost first; a deep trace keeps its two ends.
    n := self.frames.Len()
    var k := 1
    for k <= n {
        if n > 2 * TraceEnds && k == TraceEnds {
            try Print(mut w, "[... {skipped} calls not shown]\n", (skipped: n - 2 * TraceEnds + 1))
            k = n - TraceEnds + 1
            continue
        }
        // Every index below Len is present.
        try TraceLine(mut w, prog, *self.frames.Get(n - k).Must())
        k += 1
    }
}

/// How many calls a long trace shows at each end.
const TraceEnds: int = 8

TraceLine(w: mut Writer, prog: &code.Program, fr: Frame): WriteError!void {
    f := &prog.funs[fr.fun]
    // ip has moved past the instruction that failed.
    at := if fr.ip > 0 { fr.ip - 1 } else { 0 }
    line := prog.lines[at]
    if fr.base == 0 && f.name == "script" {
        try Print(w, "[line {line}] in script\n", (line))
    } else {
        try Print(w, "[line {line}] in {name}()\n", (line, name: f.name))
    }
}
