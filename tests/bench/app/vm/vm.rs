// vm, Rust, std: the task of README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. Pure safe Rust (no unsafe, no hints, no
// get_unchecked). All arithmetic uses i64 without overflow.
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.
use std::env;
use std::process;

const OP_HALT: u32 = 0;
const OP_LOADI: u32 = 1;
const OP_LOADK: u32 = 2;
const OP_MOV: u32 = 3;
const OP_ADD: u32 = 4;
const OP_SUB: u32 = 5;
const OP_MUL: u32 = 6;
const OP_DIV: u32 = 7;
const OP_MOD: u32 = 8;
const OP_ADDI: u32 = 9;
const OP_BAND: u32 = 10;
const OP_BXOR: u32 = 11;
const OP_EQ: u32 = 12;
const OP_LT: u32 = 13;
const OP_LE: u32 = 14;
const OP_JMP: u32 = 15;
const OP_JZ: u32 = 16;
const OP_JNZ: u32 = 17;
const OP_CALL: u32 = 18;
const OP_RET: u32 = 19;
const OP_ARR_ALLOC: u32 = 20;
const OP_ARR_GET: u32 = 21;
const OP_ARR_SET: u32 = 22;

const MOD_SUM: i64 = 1_000_000_007;

#[derive(Clone, Copy, Default)]
struct Frame {
    ret_pc: usize,
    caller_base: usize,
    ret_reg: usize,
}

#[derive(Clone, Copy)]
struct HeapObj {
    off: usize,
    len: usize,
}

struct Program {
    code: Vec<u32>,
    consts: Vec<i64>,
    funcs: Vec<usize>,
}

struct Vm {
    regs: Vec<i64>,
    frames: Vec<Frame>,
    heap_data: Vec<i64>,
    heap_objs: Vec<HeapObj>,
}

fn enc_abc(op: u32, a: u32, b: u32, c: u32) -> u32 {
    (op & 255) | ((a & 255) << 8) | ((b & 255) << 16) | ((c & 255) << 24)
}

fn enc_au(op: u32, a: u32, u: u32) -> u32 {
    (op & 255) | ((a & 255) << 8) | ((u & 65535) << 16)
}

fn enc_as(op: u32, a: u32, s: i32) -> u32 {
    let u = (s as i16 as u16) as u32;
    (op & 255) | ((a & 255) << 8) | (u << 16)
}

fn sign_ext16(insn: u32) -> i64 {
    ((insn >> 16) as u16 as i16) as i64
}

fn build_fib() -> Program {
    Program {
        code: vec![
            enc_as(OP_LOADI, 1, 1),
            enc_abc(OP_LE, 1, 0, 1),
            enc_au(OP_JZ, 1, 4),
            enc_abc(OP_RET, 0, 0, 0),
            enc_abc(OP_MOV, 4, 0, 0),
            enc_as(OP_ADDI, 4, -1),
            enc_abc(OP_CALL, 2, 4, 0),
            enc_abc(OP_MOV, 4, 0, 0),
            enc_as(OP_ADDI, 4, -2),
            enc_abc(OP_CALL, 3, 4, 0),
            enc_abc(OP_ADD, 0, 2, 3),
            enc_abc(OP_RET, 0, 0, 0),
        ],
        consts: vec![],
        funcs: vec![0],
    }
}

fn build_sieve() -> Program {
    Program {
        code: vec![
            enc_abc(OP_MOV, 1, 0, 0),
            enc_as(OP_ADDI, 1, 1),
            enc_abc(OP_ARR_ALLOC, 2, 1, 0),
            enc_as(OP_LOADI, 5, 1),
            enc_as(OP_LOADI, 3, 2),
            enc_abc(OP_MUL, 6, 3, 3),
            enc_abc(OP_LE, 6, 6, 0),
            enc_au(OP_JZ, 6, 18),
            enc_abc(OP_ARR_GET, 6, 2, 3),
            enc_au(OP_JNZ, 6, 16),
            enc_abc(OP_MUL, 4, 3, 3),
            enc_abc(OP_LE, 6, 4, 0),
            enc_au(OP_JZ, 6, 16),
            enc_abc(OP_ARR_SET, 2, 4, 5),
            enc_abc(OP_ADD, 4, 4, 3),
            enc_au(OP_JMP, 0, 11),
            enc_as(OP_ADDI, 3, 1),
            enc_au(OP_JMP, 0, 5),
            enc_as(OP_LOADI, 7, 0),
            enc_as(OP_LOADI, 8, 0),
            enc_as(OP_LOADI, 3, 2),
            enc_abc(OP_LE, 6, 3, 0),
            enc_au(OP_JZ, 6, 29),
            enc_abc(OP_ARR_GET, 6, 2, 3),
            enc_au(OP_JNZ, 6, 27),
            enc_as(OP_ADDI, 7, 1),
            enc_abc(OP_ADD, 8, 8, 3),
            enc_as(OP_ADDI, 3, 1),
            enc_au(OP_JMP, 0, 21),
            enc_au(OP_LOADK, 9, 0),
            enc_abc(OP_MOD, 8, 8, 9),
            enc_abc(OP_MUL, 7, 7, 9),
            enc_abc(OP_ADD, 0, 7, 8),
            enc_abc(OP_HALT, 0, 0, 0),
        ],
        consts: vec![1_000_000],
        funcs: vec![],
    }
}

fn build_arith() -> Program {
    Program {
        code: vec![
            enc_as(OP_LOADI, 2, 0),
            enc_as(OP_LOADI, 3, 0),
            enc_au(OP_LOADK, 4, 0),
            enc_au(OP_LOADK, 5, 1),
            enc_au(OP_LOADK, 6, 2),
            enc_au(OP_LOADK, 7, 3),
            enc_au(OP_LOADK, 8, 4),
            enc_abc(OP_LT, 9, 3, 0),
            enc_au(OP_JZ, 9, 21),
            enc_abc(OP_MUL, 1, 1, 5),
            enc_abc(OP_ADD, 1, 1, 6),
            enc_abc(OP_BAND, 1, 1, 7),
            enc_abc(OP_DIV, 9, 1, 8),
            enc_abc(OP_MOD, 10, 1, 8),
            enc_abc(OP_SUB, 11, 1, 10),
            enc_abc(OP_BXOR, 11, 11, 9),
            enc_abc(OP_EQ, 9, 10, 8),
            enc_abc(OP_ADD, 2, 2, 11),
            enc_abc(OP_MOD, 2, 2, 4),
            enc_as(OP_ADDI, 3, 1),
            enc_au(OP_JMP, 0, 7),
            enc_abc(OP_HALT, 2, 0, 0),
        ],
        consts: vec![1_000_000_007, 1103515245, 12345, 0x7fffffff, 97],
        funcs: vec![],
    }
}

fn build_sortdot() -> Program {
    Program {
        code: vec![
            enc_abc(OP_ARR_ALLOC, 2, 0, 0),
            enc_abc(OP_ARR_ALLOC, 3, 0, 0),
            enc_au(OP_LOADK, 15, 0),
            enc_au(OP_LOADK, 12, 1),
            enc_au(OP_LOADK, 13, 2),
            enc_au(OP_LOADK, 14, 3),
            enc_as(OP_LOADI, 11, 2),
            enc_as(OP_LOADI, 4, 0),
            enc_abc(OP_LT, 9, 4, 0),
            enc_au(OP_JZ, 9, 19),
            enc_abc(OP_MUL, 1, 1, 13),
            enc_as(OP_ADDI, 1, 17),
            enc_abc(OP_MOD, 1, 1, 12),
            enc_abc(OP_ARR_SET, 2, 4, 1),
            enc_abc(OP_MOD, 10, 4, 14),
            enc_as(OP_ADDI, 10, 1),
            enc_abc(OP_ARR_SET, 3, 4, 10),
            enc_as(OP_ADDI, 4, 1),
            enc_au(OP_JMP, 0, 8),
            enc_abc(OP_DIV, 6, 0, 11),
            enc_au(OP_JZ, 6, 40),
            enc_abc(OP_MOV, 4, 6, 0),
            enc_abc(OP_LT, 9, 4, 0),
            enc_au(OP_JZ, 9, 38),
            enc_abc(OP_ARR_GET, 7, 2, 4),
            enc_abc(OP_MOV, 5, 4, 0),
            enc_abc(OP_LE, 9, 6, 5),
            enc_au(OP_JZ, 9, 35),
            enc_abc(OP_SUB, 10, 5, 6),
            enc_abc(OP_ARR_GET, 8, 2, 10),
            enc_abc(OP_LT, 9, 7, 8),
            enc_au(OP_JZ, 9, 35),
            enc_abc(OP_ARR_SET, 2, 5, 8),
            enc_abc(OP_MOV, 5, 10, 0),
            enc_au(OP_JMP, 0, 26),
            enc_abc(OP_ARR_SET, 2, 5, 7),
            enc_as(OP_ADDI, 4, 1),
            enc_au(OP_JMP, 0, 22),
            enc_abc(OP_DIV, 6, 6, 11),
            enc_au(OP_JMP, 0, 20),
            enc_as(OP_LOADI, 8, 0),
            enc_as(OP_LOADI, 4, 0),
            enc_abc(OP_LT, 9, 4, 0),
            enc_au(OP_JZ, 9, 51),
            enc_abc(OP_ARR_GET, 7, 2, 4),
            enc_abc(OP_ARR_GET, 10, 3, 4),
            enc_abc(OP_MUL, 7, 7, 10),
            enc_abc(OP_ADD, 8, 8, 7),
            enc_abc(OP_MOD, 8, 8, 15),
            enc_as(OP_ADDI, 4, 1),
            enc_au(OP_JMP, 0, 42),
            enc_abc(OP_HALT, 8, 0, 0),
        ],
        consts: vec![1_000_000_007, 100003, 48271, 97],
        funcs: vec![],
    }
}

fn vm_exec(vm: &mut Vm, prog: &Program, arg0: i64, arg1: i64) -> i64 {
    vm.heap_data.clear();
    vm.heap_objs.clear();
    let code = &prog.code;
    let consts = &prog.consts;
    let funcs = &prog.funcs;
    let regs = &mut vm.regs;
    let frames = &mut vm.frames;

    regs[0] = arg0;
    regs[1] = arg1;
    let mut pc: usize = 0;
    let mut base: usize = 0;
    let mut sp: usize = 0;

    loop {
        let insn = code[pc];
        pc += 1;
        let op = insn & 255;
        let a = ((insn >> 8) & 255) as usize;
        match op {
            OP_HALT => return regs[base + a],
            OP_LOADI => regs[base + a] = sign_ext16(insn),
            OP_LOADK => regs[base + a] = consts[(insn >> 16) as usize],
            OP_MOV => {
                let b = ((insn >> 16) & 255) as usize;
                regs[base + a] = regs[base + b];
            }
            OP_ADD => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] + regs[base + c];
            }
            OP_SUB => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] - regs[base + c];
            }
            OP_MUL => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] * regs[base + c];
            }
            OP_DIV => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] / regs[base + c];
            }
            OP_MOD => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] % regs[base + c];
            }
            OP_ADDI => regs[base + a] += sign_ext16(insn),
            OP_BAND => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] & regs[base + c];
            }
            OP_BXOR => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = regs[base + b] ^ regs[base + c];
            }
            OP_EQ => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = if regs[base + b] == regs[base + c] { 1 } else { 0 };
            }
            OP_LT => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = if regs[base + b] < regs[base + c] { 1 } else { 0 };
            }
            OP_LE => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                regs[base + a] = if regs[base + b] <= regs[base + c] { 1 } else { 0 };
            }
            OP_JMP => pc = (insn >> 16) as usize,
            OP_JZ => {
                if regs[base + a] == 0 {
                    pc = (insn >> 16) as usize;
                }
            }
            OP_JNZ => {
                if regs[base + a] != 0 {
                    pc = (insn >> 16) as usize;
                }
            }
            OP_CALL => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                frames[sp] = Frame { ret_pc: pc, caller_base: base, ret_reg: a };
                sp += 1;
                base += b;
                pc = funcs[c];
            }
            OP_RET => {
                let val = regs[base + a];
                if sp == 0 {
                    return val;
                }
                sp -= 1;
                let f = frames[sp];
                base = f.caller_base;
                pc = f.ret_pc;
                regs[base + f.ret_reg] = val;
            }
            OP_ARR_ALLOC => {
                let b = ((insn >> 16) & 255) as usize;
                let n = regs[base + b];
                if n < 0 {
                    eprintln!("vm: negative array size");
                    process::exit(1);
                }
                let un = n as usize;
                let off = vm.heap_data.len();
                vm.heap_data.resize(off + un, 0);
                let id = vm.heap_objs.len();
                vm.heap_objs.push(HeapObj { off, len: un });
                regs[base + a] = id as i64;
            }
            OP_ARR_GET => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                let id = regs[base + b] as usize;
                let idx = regs[base + c];
                let obj = vm.heap_objs[id];
                if idx < 0 || (idx as usize) >= obj.len {
                    eprintln!("vm: array index out of bounds");
                    process::exit(1);
                }
                regs[base + a] = vm.heap_data[obj.off + idx as usize];
            }
            OP_ARR_SET => {
                let b = ((insn >> 16) & 255) as usize;
                let c = ((insn >> 24) & 255) as usize;
                let id = regs[base + a] as usize;
                let idx = regs[base + b];
                let obj = vm.heap_objs[id];
                if idx < 0 || (idx as usize) >= obj.len {
                    eprintln!("vm: array index out of bounds");
                    process::exit(1);
                }
                vm.heap_data[obj.off + idx as usize] = regs[base + c];
            }
            _ => {
                eprintln!("vm: illegal opcode {}", op);
                process::exit(1);
            }
        }
    }
}

fn main() {
    let args: Vec<String> = env::args().collect();
    if args.len() != 6 {
        eprintln!("usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>");
        process::exit(2);
    }
    let fib_n: i64 = args[1].parse().expect("fib_n");
    let sieve_n: i64 = args[2].parse().expect("sieve_n");
    let loop_n: i64 = args[3].parse().expect("loop_n");
    let sort_n: i64 = args[4].parse().expect("sort_n");
    let rounds: i64 = args[5].parse().expect("rounds");

    let p_fib = build_fib();
    let p_sieve = build_sieve();
    let p_arith = build_arith();
    let p_sort = build_sortdot();

    let mut vm = Vm {
        regs: vec![0; 65536],
        frames: vec![Frame::default(); 4096],
        heap_data: Vec::new(),
        heap_objs: Vec::new(),
    };

    let mut s_fib: i64 = 0;
    let mut s_sieve: i64 = 0;
    let mut s_arith: i64 = 0;
    let mut s_sort: i64 = 0;
    for r in 0..rounds {
        let seed = r + 1;
        s_fib = (s_fib + vm_exec(&mut vm, &p_fib, fib_n, 0)) % MOD_SUM;
        s_sieve = (s_sieve + vm_exec(&mut vm, &p_sieve, sieve_n, 0)) % MOD_SUM;
        s_arith = (s_arith + vm_exec(&mut vm, &p_arith, loop_n, seed)) % MOD_SUM;
        s_sort = (s_sort + vm_exec(&mut vm, &p_sort, sort_n, seed)) % MOD_SUM;
    }
    let total = (s_fib + s_sieve + s_arith + s_sort) % MOD_SUM;
    println!("fib={} sieve={} arith={} sortdot={} total={}", s_fib, s_sieve, s_arith, s_sort, total);
}
