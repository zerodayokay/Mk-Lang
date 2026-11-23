// Why: Lox source becomes bytecode in a single pass over the syntax tree.
//
// What: The bytecode compiler: walks the arena AST and emits stack-machine code
// (Crafting Interpreters, chapters 17 to 24).
//
// How: Locals live in stack slots; names that are not local are globals. A
// function body sees its own locals and the globals only: closures over
// enclosing locals are not supported, and naming one is a compile error. Each
// function's code is contiguous: a nested `fun` takes its function index at
// once and is queued, then compiled after the function around it ends.

import src.syntax

/// A compile error that the parser could not see: scoping and limits.
export struct CompileError {
    export line: int
    export message: str
}

/// Orders compile errors by line.
export ByLine(a: &CompileError, b: &CompileError): bool {
    return a.line < b.line
}

/// The most local slots a function has, as in clox.
const MaxLocals: int = 256

struct Local {
    name: str
    depth: int
    ready: bool
}

/// A function declared and not yet compiled: its declaration, its index in the program,
/// and the names of the locals around it, a span of `Compiler.outer`.
struct Pending {
    decl: int
    index: int
    outer: syntax.Span
}

/// Compiles one parsed tree into a program. Every list is made in `New`, and the state of
/// the function being compiled is reset between functions.
export view struct Compiler {
    ast: syntax.Ast
    prog: Program
    found: List[CompileError]
    locals: List[Local]
    sorted: List[int]
    pending: List[Pending]
    outer: List[str]
    index: int
    depth: int
    in_script: bool
    enclosing: syntax.Span
}

/// A compiler of `ast` that appends to `prog`, allocating in `a`. `Finish` gives the
/// program back.
export Compiler.New(a: &Arena, ast: syntax.Ast, prog: Program): Compiler from a, ast, prog {
    return Compiler(ast: ast, prog: prog, found: List[CompileError].New(a), locals: List[Local].New(a), sorted: List[int].New(a), pending: List[Pending].New(a), outer: List[str].New(a), index: 0, depth: 0, in_script: true, enclosing: syntax.Span(start: 0, len: 0))
}

/// The program with the functions compiled so far, and the errors found.
export Compiler.Finish(self): (prog: Program, errors: List[CompileError]) {
    Compiler(prog, found, ast, locals, sorted, pending, outer, ..) := self
    _ = ast
    _ = locals
    _ = sorted
    _ = pending
    _ = outer
    return (prog: prog, errors: found)
}

/// Compiles the top-level statements `top` into a new script function, then every function
/// declared in it, and returns the script's index in the program. Scoping errors are in
/// `Finish`'s result; only memory can fail. O(n) in the tree.
export Compiler.Script(mut self, top: syntax.Span): OutOfMemory!int {
    script := try self.Reserve("script", 0)
    try self.Begin(script, true, syntax.Span(start: 0, len: 0))
    for i in 0..top.len {
        try self.Statement(self.ast.Ref(top, i))
    }
    n := self.ast.stmts.Len()
    line := if n > 0 { self.ast.stmts[n - 1].line } else { 1 }
    try self.EmitReturn(line)
    self.End()
    // Declared functions compile first to last, so the code and a --disasm
    // listing follow the source; one compiled later may add more.
    for self.pending.Len() > 0 {
        try self.Function(self.pending.Remove(0))
    }
    return script
}

/// Takes the next function index for a function named `name`.
Compiler.Reserve(mut self, name: str into self, arity: int): OutOfMemory!int {
    try self.prog.funs.Push(Function(name: name, arity: arity, code: 0, len: 0, consts: 0, count: 0))
    return self.prog.funs.Len() - 1
}

/// Starts compiling function `index`, whose enclosing locals are the span `outer`.
Compiler.Begin(mut self, index: int, script: bool, outer: syntax.Span): OutOfMemory!void {
    self.prog.funs[index].code = self.prog.ops.Len()
    self.prog.funs[index].consts = self.prog.consts.Len()
    self.index = index
    self.depth = 0
    self.in_script = script
    self.enclosing = outer
    self.locals.Clear()
    self.sorted.Clear()
    // Slot 0 holds the function being called, as in clox.
    try self.locals.Push(Local(name: "", depth: 0, ready: true))
}

/// Ends the current function: its code and constants are the runs since `Begin`.
Compiler.End(mut self) {
    i := self.index
    self.prog.funs[i].len = self.prog.ops.Len() - self.prog.funs[i].code
    self.prog.funs[i].count = self.prog.consts.Len() - self.prog.funs[i].consts
}

Compiler.Error(mut self, line: int, message: str into self): OutOfMemory!void {
    try self.found.Push(CompileError(line: line, message: message))
}

/// Appends `op` and returns its index in the program.
Compiler.Emit(mut self, line: int, op: Op): OutOfMemory!int {
    try self.prog.ops.Push(op)
    try self.prog.lines.Push(line)
    return self.prog.ops.Len() - 1
}

/// Rewrites the jump at `at` to land on the next instruction.
Compiler.Patch(mut self, at: int) {
    to := self.prog.ops.Len()
    jump := match self.prog.ops[at] {
        .Jump { Op.Jump(to: to) }
        .JumpIfFalse { Op.JumpIfFalse(to: to) }
        _ { self.prog.ops[at] }
    }
    self.prog.ops[at] = jump
}

/// The slot of the constant `key` in the current function, adding it if there is none
/// equal. `sorted` holds the function's constant slots in key order, so the lookup is a
/// binary search and an insertion.
Compiler.Constant(mut self, key: ConstKey into self): OutOfMemory!int {
    base := self.prog.funs[self.index].consts
    consts := self.prog.consts.Items()[base..]
    pos := self.sorted.Search(&key, fn(i: &int, k: &ConstKey): bool { return KeyLess(&consts[*i].key, k) })
    if pos < self.sorted.Len() {
        found := self.sorted[pos]
        if consts[found].key.Eq(&key) {
            return found
        }
    }
    slot := self.prog.consts.Len() - base
    try self.prog.consts.Push(Constant(key: key, object: Cell.New(none)))
    try self.sorted.Insert(pos, slot)
    return slot
}

Compiler.EmitReturn(mut self, line: int): OutOfMemory!void {
    _ = try self.Emit(line, Op.Nil)
    _ = try self.Emit(line, Op.Return)
}

Compiler.EndScope(mut self, line: int): OutOfMemory!void {
    self.depth -= 1
    for {
        n := self.locals.Len()
        if n <= 1 || self.locals[n - 1].depth <= self.depth {
            return
        }
        _ = self.locals.Pop()
        _ = try self.Emit(line, Op.Pop)
    }
}

/// Reports whether a declaration here is a global: at the top level of a script.
Compiler.AtGlobalScope(&self): bool {
    return self.in_script && self.depth == 0
}

/// Declares `name` in the current scope; at the top level of a script it is a global.
Compiler.Declare(mut self, line: int, name: str into self): OutOfMemory!void {
    if self.AtGlobalScope() {
        return
    }
    var i := self.locals.Len() - 1
    var twice := false
    for i > 0 && self.locals[i].depth == self.depth {
        twice = twice || self.locals[i].name == name
        i -= 1
    }
    if twice {
        try self.Error(line, "Already a variable with this name in this scope.")
    }
    if self.locals.Len() == MaxLocals {
        try self.Error(line, "Too many local variables in function.")
        return
    }
    try self.locals.Push(Local(name: name, depth: self.depth, ready: false))
}

/// Makes the variable just declared readable, or defines the global.
Compiler.Define(mut self, line: int, name: str into self): OutOfMemory!void {
    if self.AtGlobalScope() {
        k := try self.Constant(ConstKey.Str(text: name))
        _ = try self.Emit(line, Op.DefineGlobal(name: k))
        return
    }
    last := self.locals.Len() - 1
    self.locals[last].ready = true
}

/// The slot of local `name` in the current function, if it is one. A name that is a
/// local of an enclosing function is an error, since there are no closures.
Compiler.Resolve(mut self, line: int, name: str): OutOfMemory!?int {
    var i := self.locals.Len() - 1
    for i > 0 {
        if self.locals[i].name == name {
            if !self.locals[i].ready {
                try self.Error(line, "Can't read local variable in its own initializer.")
            }
            return some(i)
        }
        i -= 1
    }
    var captured := false
    for j in 0..self.enclosing.len {
        captured = captured || self.outer[self.enclosing.start + j] == name
    }
    if captured {
        try self.Error(line, "Can't capture a local of an enclosing function (no closures).")
    }
    return none
}

/// Queues function declaration `decl` and returns its index. The names of every local
/// visible here, and of those around this function, are kept for its capture check.
Compiler.Declared(mut self, decl: int): OutOfMemory!int {
    d := self.ast.funs[decl]
    index := try self.Reserve(d.name, d.params.len)
    start := self.outer.Len()
    for j in 0..self.enclosing.len {
        name := self.outer[self.enclosing.start + j]
        try self.outer.Push(name)
    }
    for i in 1..self.locals.Len() {
        name := self.locals[i].name
        try self.outer.Push(name)
    }
    span := syntax.Span(start: start, len: self.outer.Len() - start)
    try self.pending.Push(Pending(decl: decl, index: index, outer: span))
    return index
}

/// Compiles a queued function.
Compiler.Function(mut self, p: Pending): OutOfMemory!void {
    d := self.ast.funs[p.decl]
    try self.Begin(p.index, false, p.outer)
    self.depth = 1
    for i in 0..d.params.len {
        name := self.ast.Name(d.params, i)
        try self.Declare(d.line, name)
        try self.Define(d.line, name)
    }
    for i in 0..d.body.len {
        try self.Statement(self.ast.Ref(d.body, i))
    }
    try self.EmitReturn(d.line)
    self.End()
}

Compiler.Statement(mut self, at: int): OutOfMemory!void {
    node := self.ast.stmts[at]
    line := node.line
    match node.stmt {
        .Expression(expr) {
            try self.Expression(expr)
            _ = try self.Emit(line, Op.Pop)
        }
        .Print(expr) {
            try self.Expression(expr)
            _ = try self.Emit(line, Op.Print)
        }
        .Var(name, init) {
            try self.Declare(line, name)
            if e := init {
                try self.Expression(e)
            } else {
                _ = try self.Emit(line, Op.Nil)
            }
            try self.Define(line, name)
        }
        .Block(body) {
            self.depth += 1
            for i in 0..body.len {
                try self.Statement(self.ast.Ref(body, i))
            }
            try self.EndScope(line)
        }
        .If(cond, then, otherwise) {
            try self.Expression(cond)
            skip := try self.Emit(line, Op.JumpIfFalse(to: 0))
            _ = try self.Emit(line, Op.Pop)
            try self.Statement(then)
            done := try self.Emit(line, Op.Jump(to: 0))
            self.Patch(skip)
            _ = try self.Emit(line, Op.Pop)
            if e := otherwise {
                try self.Statement(e)
            }
            self.Patch(done)
        }
        .While(cond, body) {
            start := self.prog.ops.Len()
            try self.Expression(cond)
            exit := try self.Emit(line, Op.JumpIfFalse(to: 0))
            _ = try self.Emit(line, Op.Pop)
            try self.Statement(body)
            _ = try self.Emit(line, Op.Loop(to: start))
            self.Patch(exit)
            _ = try self.Emit(line, Op.Pop)
        }
        .Function(decl) {
            name := self.ast.funs[decl].name
            try self.Declare(line, name)
            if !self.AtGlobalScope() {
                last := self.locals.Len() - 1
                self.locals[last].ready = true
            }
            fun := try self.Declared(decl)
            k := try self.Constant(ConstKey.Fun(index: fun))
            _ = try self.Emit(line, Op.Constant(index: k))
            try self.Define(line, name)
        }
        .Return(value) {
            if self.in_script {
                try self.Error(line, "Can't return from top-level code.")
            }
            if e := value {
                try self.Expression(e)
                _ = try self.Emit(line, Op.Return)
            } else {
                try self.EmitReturn(line)
            }
        }
    }
}

Compiler.Variable(mut self, line: int, name: str into self, set: bool): OutOfMemory!void {
    if slot := try self.Resolve(line, name) {
        _ = try self.Emit(line, if set { Op.SetLocal(slot: slot) } else { Op.GetLocal(slot: slot) })
        return
    }
    k := try self.Constant(ConstKey.Str(text: name))
    _ = try self.Emit(line, if set { Op.SetGlobal(name: k) } else { Op.GetGlobal(name: k) })
}

Compiler.Expression(mut self, at: int): OutOfMemory!void {
    node := self.ast.exprs[at]
    line := node.line
    match node.expr {
        .Number(value) {
            k := try self.Constant(ConstKey.Num(bits: value.Bits()))
            _ = try self.Emit(line, Op.Constant(index: k))
        }
        .Str(text) {
            k := try self.Constant(ConstKey.Str(text: text))
            _ = try self.Emit(line, Op.Constant(index: k))
        }
        .True { _ = try self.Emit(line, Op.True) }
        .False { _ = try self.Emit(line, Op.False) }
        .Nil { _ = try self.Emit(line, Op.Nil) }
        .Variable(name) { try self.Variable(line, name, false) }
        .Assign(name, value) {
            try self.Expression(value)
            try self.Variable(line, name, true)
        }
        .Unary(op, right) {
            try self.Expression(right)
            _ = try self.Emit(line, if op == syntax.Kind.Bang { Op.Not } else { Op.Negate })
        }
        .Binary(op, left, right) {
            try self.Expression(left)
            try self.Expression(right)
            try self.Binary(line, op)
        }
        .Logical(op, left, right) {
            try self.Expression(left)
            if op == syntax.Kind.And {
                skip := try self.Emit(line, Op.JumpIfFalse(to: 0))
                _ = try self.Emit(line, Op.Pop)
                try self.Expression(right)
                self.Patch(skip)
            } else {
                other := try self.Emit(line, Op.JumpIfFalse(to: 0))
                done := try self.Emit(line, Op.Jump(to: 0))
                self.Patch(other)
                _ = try self.Emit(line, Op.Pop)
                try self.Expression(right)
                self.Patch(done)
            }
        }
        .Call(callee, args) {
            try self.Expression(callee)
            for i in 0..args.len {
                try self.Expression(self.ast.Ref(args, i))
            }
            _ = try self.Emit(line, Op.Call(argc: args.len))
        }
        .Array(items) {
            for i in 0..items.len {
                try self.Expression(self.ast.Ref(items, i))
            }
            _ = try self.Emit(line, Op.Array(count: items.len))
        }
        .Index(target, index) {
            try self.Expression(target)
            try self.Expression(index)
            _ = try self.Emit(line, Op.GetIndex)
        }
        .SetIndex(target, index, value) {
            try self.Expression(target)
            try self.Expression(index)
            try self.Expression(value)
            _ = try self.Emit(line, Op.SetIndex)
        }
    }
}

/// `a != b` is `!(a == b)`, `a >= b` is `!(a < b)` and `a <= b` is `!(a > b)`, as in clox.
Compiler.Binary(mut self, line: int, op: syntax.Kind): OutOfMemory!void {
    first := match op {
        .Plus { Op.Add }
        .Minus { Op.Subtract }
        .Star { Op.Multiply }
        .Slash { Op.Divide }
        .Percent { Op.Modulo }
        .EqualEqual | .BangEqual { Op.Equal }
        .Greater | .LessEqual { Op.Greater }
        _ { Op.Less }
    }
    _ = try self.Emit(line, first)
    match op {
        .BangEqual | .GreaterEqual | .LessEqual { _ = try self.Emit(line, Op.Not) }
        _ {}
    }
}
