// Why: The compiler and the VM share one bytecode format.
//
// What: Op, the instructions of the stack VM; Function, a compiled function;
// and Program, every function compiled so far, with Program.Disassemble.
//
// How: Jumps hold absolute instruction indices (Crafting Interpreters, chapters
// 14 and 24). All functions' code, lines and constants sit in program-wide
// lists made once, and a function names its run by index, so the REPL can keep
// adding functions.

import std.derive

/// One instruction. Operands name a constant, a stack slot, a count or a target.
export enum Op {
    Constant(index: int)
    Nil
    True
    False
    Pop
    GetLocal(slot: int)
    SetLocal(slot: int)
    GetGlobal(name: int)
    DefineGlobal(name: int)
    SetGlobal(name: int)
    Equal
    Greater
    Less
    Add
    Subtract
    Multiply
    Divide
    Modulo
    Not
    Negate
    Print
    Jump(to: int)
    JumpIfFalse(to: int)
    Loop(to: int)
    Call(argc: int)
    Array(count: int)
    GetIndex
    SetIndex
    Return
}

gen derive.Eq(Op)
gen derive.Format(Op)

/// A compiled function: a run of the program's code and a run of its constants.
export struct Function {
    export name: str
    export arity: int
    /// The first instruction in `Program.ops`, and how many there are.
    export code: int
    export len: int
    /// The first constant in `Program.consts`, and how many there are.
    export consts: int
    export count: int
}

// A workaround: one list per function would be the natural layout, but a list made
// after construction and stored in an arena struct cannot be reached from Main.
/// Every function compiled so far, with their code, lines and constants in program-wide
/// lists made once. A REPL adds a script function per entry, and objects name functions
/// by index, so nothing is ever removed.
export struct Program {
    export funs: List[Function]
    export ops: List[Op]
    export lines: List[int]
    export consts: List[Constant]
}

/// An empty program in `a`.
export Program.New(a: &Arena): Program {
    return Program(funs: List[Function].New(a), ops: List[Op].New(a), lines: List[int].New(a), consts: List[Constant].New(a))
}

/// Constant `i` of function `f`.
export Program.Constant(&self, f: &Function, i: int): &Constant from self {
    return &self.consts[f.consts + i]
}

/// Writes a listing of function `index`: its constants in key order, then its code.
export Program.Disassemble(&self, w: mut Writer, index: int): WriteError!void {
    f := self.funs[index]
    try Print(w, "== {name} ({arity} params) ==\n", (name: f.name, arity: f.arity))
    // The constants are listed in key order, the order the compiler searches them in.
    var order: [256]int = [0; 256]
    n := if f.count < 256 { f.count } else { 256 }
    for i in 0..n {
        order[i] = i
    }
    consts := self.consts.Items()[f.consts..f.consts + f.count]
    (mut order[..n]).SortStableInPlace(fn(x: &int, y: &int): bool { return KeyLess(&consts[*x].key, &consts[*y].key) })
    for i in order[..n] {
        if bits := NumBits(&consts[i].key) {
            try Print(w, "  const {i:>3} Num({n})\n", (i, n: f64.FromBits(bits)))
        } else {
            try Print(w, "  const {i:>3} {key}\n", (i, key: consts[i].key))
        }
    }
    var last := -1
    for i in f.code..f.code + f.len {
        line := self.lines[i]
        op := self.ops[i]
        if line == last {
            try Print(w, "{i:04}    | {op}\n", (i, op))
        } else {
            try Print(w, "{i:04} {line:>4} {op}\n", (i, line, op))
        }
        last = line
    }
}
