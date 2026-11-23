// vm, Mk, std: the task of ../README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. All arithmetic stays within i64 without overflow.
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.

const OpHalt: u32 = 0
const OpLoadI: u32 = 1
const OpLoadK: u32 = 2
const OpMov: u32 = 3
const OpAdd: u32 = 4
const OpSub: u32 = 5
const OpMul: u32 = 6
const OpDiv: u32 = 7
const OpMod: u32 = 8
const OpAddI: u32 = 9
const OpBand: u32 = 10
const OpBxor: u32 = 11
const OpEq: u32 = 12
const OpLt: u32 = 13
const OpLe: u32 = 14
const OpJmp: u32 = 15
const OpJz: u32 = 16
const OpJnz: u32 = 17
const OpCall: u32 = 18
const OpRet: u32 = 19
const OpArrAlloc: u32 = 20
const OpArrGet: u32 = 21
const OpArrSet: u32 = 22

const ModSum: i64 = 1_000_000_007

error VmError

struct Frame {
    ret_pc: int
    caller_base: int
    ret_reg: int
}

struct HeapObj {
    off: int
    len: int
}

view struct Program {
    code: List[u32]
    consts: List[i64]
    funcs: List[int]
}

view struct Vm {
    regs: mut []i64
    frames: mut []Frame
    heap_data: List[i64]
    heap_objs: List[HeapObj]
}

EncABC(op: u32, a: u32, b: u32, c: u32): u32 {
    return (op & 255) | ((a & 255) << 8) | ((b & 255) << 16) | ((c & 255) << 24)
}

EncAU(op: u32, a: u32, u: u32): u32 {
    return (op & 255) | ((a & 255) << 8) | ((u & 65535) << 16)
}

EncAS(op: u32, a: u32, s: int): u32 {
    u := Cast[u32](s & 65535)
    return (op & 255) | ((a & 255) << 8) | (u << 16)
}

SignExt16(insn: u32): i64 {
    raw := int(insn >> 16)
    if raw >= 32768 {
        return i64(raw - 65536)
    }
    return i64(raw)
}

BuildFib(a: &Arena): OutOfMemory!Program from a {
    var code := List[u32].New(a)
    var consts := List[i64].New(a)
    var funcs := List[int].New(a)
    try funcs.Push(0)
    try code.Push(EncAS(OpLoadI, 1, 1))
    try code.Push(EncABC(OpLe, 1, 0, 1))
    try code.Push(EncAU(OpJz, 1, 4))
    try code.Push(EncABC(OpRet, 0, 0, 0))
    try code.Push(EncABC(OpMov, 4, 0, 0))
    try code.Push(EncAS(OpAddI, 4, -1))
    try code.Push(EncABC(OpCall, 2, 4, 0))
    try code.Push(EncABC(OpMov, 4, 0, 0))
    try code.Push(EncAS(OpAddI, 4, -2))
    try code.Push(EncABC(OpCall, 3, 4, 0))
    try code.Push(EncABC(OpAdd, 0, 2, 3))
    try code.Push(EncABC(OpRet, 0, 0, 0))
    return Program(code: code, consts: consts, funcs: funcs)
}

BuildSieve(a: &Arena): OutOfMemory!Program from a {
    var code := List[u32].New(a)
    var consts := List[i64].New(a)
    var funcs := List[int].New(a)
    try consts.Push(1_000_000)
    try code.Push(EncABC(OpMov, 1, 0, 0))
    try code.Push(EncAS(OpAddI, 1, 1))
    try code.Push(EncABC(OpArrAlloc, 2, 1, 0))
    try code.Push(EncAS(OpLoadI, 5, 1))
    try code.Push(EncAS(OpLoadI, 3, 2))
    try code.Push(EncABC(OpMul, 6, 3, 3))
    try code.Push(EncABC(OpLe, 6, 6, 0))
    try code.Push(EncAU(OpJz, 6, 18))
    try code.Push(EncABC(OpArrGet, 6, 2, 3))
    try code.Push(EncAU(OpJnz, 6, 16))
    try code.Push(EncABC(OpMul, 4, 3, 3))
    try code.Push(EncABC(OpLe, 6, 4, 0))
    try code.Push(EncAU(OpJz, 6, 16))
    try code.Push(EncABC(OpArrSet, 2, 4, 5))
    try code.Push(EncABC(OpAdd, 4, 4, 3))
    try code.Push(EncAU(OpJmp, 0, 11))
    try code.Push(EncAS(OpAddI, 3, 1))
    try code.Push(EncAU(OpJmp, 0, 5))
    try code.Push(EncAS(OpLoadI, 7, 0))
    try code.Push(EncAS(OpLoadI, 8, 0))
    try code.Push(EncAS(OpLoadI, 3, 2))
    try code.Push(EncABC(OpLe, 6, 3, 0))
    try code.Push(EncAU(OpJz, 6, 29))
    try code.Push(EncABC(OpArrGet, 6, 2, 3))
    try code.Push(EncAU(OpJnz, 6, 27))
    try code.Push(EncAS(OpAddI, 7, 1))
    try code.Push(EncABC(OpAdd, 8, 8, 3))
    try code.Push(EncAS(OpAddI, 3, 1))
    try code.Push(EncAU(OpJmp, 0, 21))
    try code.Push(EncAU(OpLoadK, 9, 0))
    try code.Push(EncABC(OpMod, 8, 8, 9))
    try code.Push(EncABC(OpMul, 7, 7, 9))
    try code.Push(EncABC(OpAdd, 0, 7, 8))
    try code.Push(EncABC(OpHalt, 0, 0, 0))
    return Program(code: code, consts: consts, funcs: funcs)
}

BuildArith(a: &Arena): OutOfMemory!Program from a {
    var code := List[u32].New(a)
    var consts := List[i64].New(a)
    var funcs := List[int].New(a)
    try consts.Push(1_000_000_007)
    try consts.Push(1103515245)
    try consts.Push(12345)
    try consts.Push(0x7fffffff)
    try consts.Push(97)
    try code.Push(EncAS(OpLoadI, 2, 0))
    try code.Push(EncAS(OpLoadI, 3, 0))
    try code.Push(EncAU(OpLoadK, 4, 0))
    try code.Push(EncAU(OpLoadK, 5, 1))
    try code.Push(EncAU(OpLoadK, 6, 2))
    try code.Push(EncAU(OpLoadK, 7, 3))
    try code.Push(EncAU(OpLoadK, 8, 4))
    try code.Push(EncABC(OpLt, 9, 3, 0))
    try code.Push(EncAU(OpJz, 9, 21))
    try code.Push(EncABC(OpMul, 1, 1, 5))
    try code.Push(EncABC(OpAdd, 1, 1, 6))
    try code.Push(EncABC(OpBand, 1, 1, 7))
    try code.Push(EncABC(OpDiv, 9, 1, 8))
    try code.Push(EncABC(OpMod, 10, 1, 8))
    try code.Push(EncABC(OpSub, 11, 1, 10))
    try code.Push(EncABC(OpBxor, 11, 11, 9))
    try code.Push(EncABC(OpEq, 9, 10, 8))
    try code.Push(EncABC(OpAdd, 2, 2, 11))
    try code.Push(EncABC(OpMod, 2, 2, 4))
    try code.Push(EncAS(OpAddI, 3, 1))
    try code.Push(EncAU(OpJmp, 0, 7))
    try code.Push(EncABC(OpHalt, 2, 0, 0))
    return Program(code: code, consts: consts, funcs: funcs)
}

BuildSortDot(a: &Arena): OutOfMemory!Program from a {
    var code := List[u32].New(a)
    var consts := List[i64].New(a)
    var funcs := List[int].New(a)
    try consts.Push(1_000_000_007)
    try consts.Push(100003)
    try consts.Push(48271)
    try consts.Push(97)
    try code.Push(EncABC(OpArrAlloc, 2, 0, 0))
    try code.Push(EncABC(OpArrAlloc, 3, 0, 0))
    try code.Push(EncAU(OpLoadK, 15, 0))
    try code.Push(EncAU(OpLoadK, 12, 1))
    try code.Push(EncAU(OpLoadK, 13, 2))
    try code.Push(EncAU(OpLoadK, 14, 3))
    try code.Push(EncAS(OpLoadI, 11, 2))
    try code.Push(EncAS(OpLoadI, 4, 0))
    try code.Push(EncABC(OpLt, 9, 4, 0))
    try code.Push(EncAU(OpJz, 9, 19))
    try code.Push(EncABC(OpMul, 1, 1, 13))
    try code.Push(EncAS(OpAddI, 1, 17))
    try code.Push(EncABC(OpMod, 1, 1, 12))
    try code.Push(EncABC(OpArrSet, 2, 4, 1))
    try code.Push(EncABC(OpMod, 10, 4, 14))
    try code.Push(EncAS(OpAddI, 10, 1))
    try code.Push(EncABC(OpArrSet, 3, 4, 10))
    try code.Push(EncAS(OpAddI, 4, 1))
    try code.Push(EncAU(OpJmp, 0, 8))
    try code.Push(EncABC(OpDiv, 6, 0, 11))
    try code.Push(EncAU(OpJz, 6, 40))
    try code.Push(EncABC(OpMov, 4, 6, 0))
    try code.Push(EncABC(OpLt, 9, 4, 0))
    try code.Push(EncAU(OpJz, 9, 38))
    try code.Push(EncABC(OpArrGet, 7, 2, 4))
    try code.Push(EncABC(OpMov, 5, 4, 0))
    try code.Push(EncABC(OpLe, 9, 6, 5))
    try code.Push(EncAU(OpJz, 9, 35))
    try code.Push(EncABC(OpSub, 10, 5, 6))
    try code.Push(EncABC(OpArrGet, 8, 2, 10))
    try code.Push(EncABC(OpLt, 9, 7, 8))
    try code.Push(EncAU(OpJz, 9, 35))
    try code.Push(EncABC(OpArrSet, 2, 5, 8))
    try code.Push(EncABC(OpMov, 5, 10, 0))
    try code.Push(EncAU(OpJmp, 0, 26))
    try code.Push(EncABC(OpArrSet, 2, 5, 7))
    try code.Push(EncAS(OpAddI, 4, 1))
    try code.Push(EncAU(OpJmp, 0, 22))
    try code.Push(EncABC(OpDiv, 6, 6, 11))
    try code.Push(EncAU(OpJmp, 0, 20))
    try code.Push(EncAS(OpLoadI, 8, 0))
    try code.Push(EncAS(OpLoadI, 4, 0))
    try code.Push(EncABC(OpLt, 9, 4, 0))
    try code.Push(EncAU(OpJz, 9, 51))
    try code.Push(EncABC(OpArrGet, 7, 2, 4))
    try code.Push(EncABC(OpArrGet, 10, 3, 4))
    try code.Push(EncABC(OpMul, 7, 7, 10))
    try code.Push(EncABC(OpAdd, 8, 8, 7))
    try code.Push(EncABC(OpMod, 8, 8, 15))
    try code.Push(EncAS(OpAddI, 4, 1))
    try code.Push(EncAU(OpJmp, 0, 42))
    try code.Push(EncABC(OpHalt, 8, 0, 0))
    return Program(code: code, consts: consts, funcs: funcs)
}

Exec(vm: mut Vm, prog: &Program, arg0: i64, arg1: i64): OutOfMemory|VmError!i64 {
    vm.heap_data.Clear()
    vm.heap_objs.Clear()
    vm.regs[0] = arg0
    vm.regs[1] = arg1
    code := prog.code.Items()
    consts := prog.consts.Items()
    funcs := prog.funcs.Items()
    var pc := 0
    var base := 0
    var sp := 0
    for {
        insn := code[pc]
        pc += 1
        op := insn & 255
        a := int((insn >> 8) & 255)
        match op {
            0 { // HALT
                return vm.regs[base + a]
            }
            1 { // LOADI
                vm.regs[base + a] = SignExt16(insn)
            }
            2 { // LOADK
                vm.regs[base + a] = consts[int(insn >> 16)]
            }
            3 { // MOV
                b := int((insn >> 16) & 255)
                vm.regs[base + a] = vm.regs[base + b]
            }
            4 { // ADD
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] + vm.regs[base + c]
            }
            5 { // SUB
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] - vm.regs[base + c]
            }
            6 { // MUL
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] * vm.regs[base + c]
            }
            7 { // DIV
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] / vm.regs[base + c]
            }
            8 { // MOD
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] % vm.regs[base + c]
            }
            9 { // ADDI
                vm.regs[base + a] = vm.regs[base + a] + SignExt16(insn)
            }
            10 { // BAND
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] & vm.regs[base + c]
            }
            11 { // BXOR
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = vm.regs[base + b] ^ vm.regs[base + c]
            }
            12 { // EQ
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = if vm.regs[base + b] == vm.regs[base + c] { 1 } else { 0 }
            }
            13 { // LT
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = if vm.regs[base + b] < vm.regs[base + c] { 1 } else { 0 }
            }
            14 { // LE
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.regs[base + a] = if vm.regs[base + b] <= vm.regs[base + c] { 1 } else { 0 }
            }
            15 { // JMP
                pc = int(insn >> 16)
            }
            16 { // JZ
                if vm.regs[base + a] == 0 {
                    pc = int(insn >> 16)
                }
            }
            17 { // JNZ
                if vm.regs[base + a] != 0 {
                    pc = int(insn >> 16)
                }
            }
            18 { // CALL
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                vm.frames[sp] = Frame(ret_pc: pc, caller_base: base, ret_reg: a)
                sp += 1
                base += b
                pc = funcs[c]
            }
            19 { // RET
                val := vm.regs[base + a]
                if sp == 0 {
                    return val
                }
                sp -= 1
                f := vm.frames[sp]
                base = f.caller_base
                pc = f.ret_pc
                vm.regs[base + f.ret_reg] = val
            }
            20 { // ARR_ALLOC
                b := int((insn >> 16) & 255)
                n := Cast[int](vm.regs[base + b])
                if n < 0 {
                    fail VmError
                }
                off := vm.heap_data.Len()
                for _ in 0..n {
                    try vm.heap_data.Push(0)
                }
                id := vm.heap_objs.Len()
                try vm.heap_objs.Push(HeapObj(off: off, len: n))
                vm.regs[base + a] = i64(id)
            }
            21 { // ARR_GET
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                id := Cast[int](vm.regs[base + b])
                idx := Cast[int](vm.regs[base + c])
                obj := vm.heap_objs[id]
                if idx < 0 || idx >= obj.len {
                    fail VmError
                }
                vm.regs[base + a] = vm.heap_data[obj.off + idx]
            }
            22 { // ARR_SET
                b := int((insn >> 16) & 255)
                c := int((insn >> 24) & 255)
                id := Cast[int](vm.regs[base + a])
                idx := Cast[int](vm.regs[base + b])
                obj := vm.heap_objs[id]
                if idx < 0 || idx >= obj.len {
                    fail VmError
                }
                vm.heap_data[obj.off + idx] = vm.regs[base + c]
            }
            _ {
                fail VmError
            }
        }
    }
}

export Main(sys: &System): OutOfMemory|ParseError|WriteError|VmError!void {
    if sys.args.Len() != 6 {
        var e := sys.Stderr()
        try Print(mut e, "usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>\n")
        fail ParseError
    }
    fib_n := try sys.args[1].ParseInt()
    sieve_n := try sys.args[2].ParseInt()
    loop_n := try sys.args[3].ParseInt()
    sort_n := try sys.args[4].ParseInt()
    rounds := Cast[int](try sys.args[5].ParseInt())

    var a := try Arena.New(sys.heap)
    defer a.Free()

    p_fib := try BuildFib(&a)
    p_sieve := try BuildSieve(&a)
    p_arith := try BuildArith(&a)
    p_sort := try BuildSortDot(&a)

    regs := try a.Slice[i64](65536)
    frames := try a.Slice[Frame](4096)
    var vm := Vm(
        regs: regs,
        frames: frames,
        heap_data: List[i64].New(&a),
        heap_objs: List[HeapObj].New(&a),
    )

    var s_fib: i64 = 0
    var s_sieve: i64 = 0
    var s_arith: i64 = 0
    var s_sort: i64 = 0
    for r in 0..rounds {
        seed := i64(r + 1)
        s_fib = (s_fib + try Exec(mut vm, &p_fib, fib_n, 0)) % ModSum
        s_sieve = (s_sieve + try Exec(mut vm, &p_sieve, sieve_n, 0)) % ModSum
        s_arith = (s_arith + try Exec(mut vm, &p_arith, loop_n, seed)) % ModSum
        s_sort = (s_sort + try Exec(mut vm, &p_sort, sort_n, seed)) % ModSum
    }
    total := (s_fib + s_sieve + s_arith + s_sort) % ModSum
    var out := sys.Stdout()
    try Print(mut out, "fib={s_fib} sieve={s_sieve} arith={s_arith} sortdot={s_sort} total={total}\n", (s_fib, s_sieve, s_arith, s_sort, total))
}
