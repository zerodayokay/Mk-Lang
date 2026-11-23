// vm, Zig, std: the task of README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. Standard Zig switch dispatch (no forced inline,
// no @branchHint, no @setRuntimeSafety). All arithmetic uses i64 without
// overflow.
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.
const std = @import("std");

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

const Frame = struct {
    ret_pc: usize,
    caller_base: usize,
    ret_reg: usize,
};

const HeapObj = struct {
    off: usize,
    len: usize,
};

const Program = struct {
    code: []const u32,
    consts: []const i64,
    funcs: []const usize,
};

const Vm = struct {
    alloc: std.mem.Allocator,
    regs: []i64,
    frames: []Frame,
    heap_data: std.ArrayList(i64),
    heap_objs: std.ArrayList(HeapObj),
};

fn encABC(op: u32, a: u32, b: u32, c: u32) u32 {
    return (op & 255) | ((a & 255) << 8) | ((b & 255) << 16) | ((c & 255) << 24);
}

fn encAU(op: u32, a: u32, u: u32) u32 {
    return (op & 255) | ((a & 255) << 8) | ((u & 65535) << 16);
}

fn encAS(op: u32, a: u32, s: i16) u32 {
    const u: u32 = @as(u16, @bitCast(s));
    return (op & 255) | ((a & 255) << 8) | (u << 16);
}

fn signExt16(insn: u32) i64 {
    const u: u16 = @truncate(insn >> 16);
    const s: i16 = @bitCast(u);
    return @as(i64, s);
}

const fib_code = [_]u32{
    encAS(OP_LOADI, 1, 1),
    encABC(OP_LE, 1, 0, 1),
    encAU(OP_JZ, 1, 4),
    encABC(OP_RET, 0, 0, 0),
    encABC(OP_MOV, 4, 0, 0),
    encAS(OP_ADDI, 4, -1),
    encABC(OP_CALL, 2, 4, 0),
    encABC(OP_MOV, 4, 0, 0),
    encAS(OP_ADDI, 4, -2),
    encABC(OP_CALL, 3, 4, 0),
    encABC(OP_ADD, 0, 2, 3),
    encABC(OP_RET, 0, 0, 0),
};
const fib_funcs = [_]usize{0};

const sieve_code = [_]u32{
    encABC(OP_MOV, 1, 0, 0),
    encAS(OP_ADDI, 1, 1),
    encABC(OP_ARR_ALLOC, 2, 1, 0),
    encAS(OP_LOADI, 5, 1),
    encAS(OP_LOADI, 3, 2),
    encABC(OP_MUL, 6, 3, 3),
    encABC(OP_LE, 6, 6, 0),
    encAU(OP_JZ, 6, 18),
    encABC(OP_ARR_GET, 6, 2, 3),
    encAU(OP_JNZ, 6, 16),
    encABC(OP_MUL, 4, 3, 3),
    encABC(OP_LE, 6, 4, 0),
    encAU(OP_JZ, 6, 16),
    encABC(OP_ARR_SET, 2, 4, 5),
    encABC(OP_ADD, 4, 4, 3),
    encAU(OP_JMP, 0, 11),
    encAS(OP_ADDI, 3, 1),
    encAU(OP_JMP, 0, 5),
    encAS(OP_LOADI, 7, 0),
    encAS(OP_LOADI, 8, 0),
    encAS(OP_LOADI, 3, 2),
    encABC(OP_LE, 6, 3, 0),
    encAU(OP_JZ, 6, 29),
    encABC(OP_ARR_GET, 6, 2, 3),
    encAU(OP_JNZ, 6, 27),
    encAS(OP_ADDI, 7, 1),
    encABC(OP_ADD, 8, 8, 3),
    encAS(OP_ADDI, 3, 1),
    encAU(OP_JMP, 0, 21),
    encAU(OP_LOADK, 9, 0),
    encABC(OP_MOD, 8, 8, 9),
    encABC(OP_MUL, 7, 7, 9),
    encABC(OP_ADD, 0, 7, 8),
    encABC(OP_HALT, 0, 0, 0),
};
const sieve_consts = [_]i64{1_000_000};

const arith_code = [_]u32{
    encAS(OP_LOADI, 2, 0),
    encAS(OP_LOADI, 3, 0),
    encAU(OP_LOADK, 4, 0),
    encAU(OP_LOADK, 5, 1),
    encAU(OP_LOADK, 6, 2),
    encAU(OP_LOADK, 7, 3),
    encAU(OP_LOADK, 8, 4),
    encABC(OP_LT, 9, 3, 0),
    encAU(OP_JZ, 9, 21),
    encABC(OP_MUL, 1, 1, 5),
    encABC(OP_ADD, 1, 1, 6),
    encABC(OP_BAND, 1, 1, 7),
    encABC(OP_DIV, 9, 1, 8),
    encABC(OP_MOD, 10, 1, 8),
    encABC(OP_SUB, 11, 1, 10),
    encABC(OP_BXOR, 11, 11, 9),
    encABC(OP_EQ, 9, 10, 8),
    encABC(OP_ADD, 2, 2, 11),
    encABC(OP_MOD, 2, 2, 4),
    encAS(OP_ADDI, 3, 1),
    encAU(OP_JMP, 0, 7),
    encABC(OP_HALT, 2, 0, 0),
};
const arith_consts = [_]i64{ 1_000_000_007, 1103515245, 12345, 0x7fffffff, 97 };

const sortdot_code = [_]u32{
    encABC(OP_ARR_ALLOC, 2, 0, 0),
    encABC(OP_ARR_ALLOC, 3, 0, 0),
    encAU(OP_LOADK, 15, 0),
    encAU(OP_LOADK, 12, 1),
    encAU(OP_LOADK, 13, 2),
    encAU(OP_LOADK, 14, 3),
    encAS(OP_LOADI, 11, 2),
    encAS(OP_LOADI, 4, 0),
    encABC(OP_LT, 9, 4, 0),
    encAU(OP_JZ, 9, 19),
    encABC(OP_MUL, 1, 1, 13),
    encAS(OP_ADDI, 1, 17),
    encABC(OP_MOD, 1, 1, 12),
    encABC(OP_ARR_SET, 2, 4, 1),
    encABC(OP_MOD, 10, 4, 14),
    encAS(OP_ADDI, 10, 1),
    encABC(OP_ARR_SET, 3, 4, 10),
    encAS(OP_ADDI, 4, 1),
    encAU(OP_JMP, 0, 8),
    encABC(OP_DIV, 6, 0, 11),
    encAU(OP_JZ, 6, 40),
    encABC(OP_MOV, 4, 6, 0),
    encABC(OP_LT, 9, 4, 0),
    encAU(OP_JZ, 9, 38),
    encABC(OP_ARR_GET, 7, 2, 4),
    encABC(OP_MOV, 5, 4, 0),
    encABC(OP_LE, 9, 6, 5),
    encAU(OP_JZ, 9, 35),
    encABC(OP_SUB, 10, 5, 6),
    encABC(OP_ARR_GET, 8, 2, 10),
    encABC(OP_LT, 9, 7, 8),
    encAU(OP_JZ, 9, 35),
    encABC(OP_ARR_SET, 2, 5, 8),
    encABC(OP_MOV, 5, 10, 0),
    encAU(OP_JMP, 0, 26),
    encABC(OP_ARR_SET, 2, 5, 7),
    encAS(OP_ADDI, 4, 1),
    encAU(OP_JMP, 0, 22),
    encABC(OP_DIV, 6, 6, 11),
    encAU(OP_JMP, 0, 20),
    encAS(OP_LOADI, 8, 0),
    encAS(OP_LOADI, 4, 0),
    encABC(OP_LT, 9, 4, 0),
    encAU(OP_JZ, 9, 51),
    encABC(OP_ARR_GET, 7, 2, 4),
    encABC(OP_ARR_GET, 10, 3, 4),
    encABC(OP_MUL, 7, 7, 10),
    encABC(OP_ADD, 8, 8, 7),
    encABC(OP_MOD, 8, 8, 15),
    encAS(OP_ADDI, 4, 1),
    encAU(OP_JMP, 0, 42),
    encABC(OP_HALT, 8, 0, 0),
};
const sortdot_consts = [_]i64{ 1_000_000_007, 100003, 48271, 97 };

fn vmExec(vm: *Vm, prog: Program, arg0: i64, arg1: i64) !i64 {
    vm.heap_data.clearRetainingCapacity();
    vm.heap_objs.clearRetainingCapacity();
    const regs = vm.regs;
    const frames = vm.frames;
    const code = prog.code;
    const consts = prog.consts;
    const funcs = prog.funcs;

    regs[0] = arg0;
    regs[1] = arg1;
    var pc: usize = 0;
    var base: usize = 0;
    var sp: usize = 0;

    while (true) {
        const insn = code[pc];
        pc += 1;
        const op = insn & 255;
        const a: usize = @intCast((insn >> 8) & 255);
        switch (op) {
            OP_HALT => return regs[base + a],
            OP_LOADI => regs[base + a] = signExt16(insn),
            OP_LOADK => regs[base + a] = consts[@intCast(insn >> 16)],
            OP_MOV => {
                const b: usize = @intCast((insn >> 16) & 255);
                regs[base + a] = regs[base + b];
            },
            OP_ADD => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = regs[base + b] + regs[base + c];
            },
            OP_SUB => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = regs[base + b] - regs[base + c];
            },
            OP_MUL => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = regs[base + b] * regs[base + c];
            },
            OP_DIV => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = @divTrunc(regs[base + b], regs[base + c]);
            },
            OP_MOD => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = @rem(regs[base + b], regs[base + c]);
            },
            OP_ADDI => regs[base + a] += signExt16(insn),
            OP_BAND => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = regs[base + b] & regs[base + c];
            },
            OP_BXOR => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = regs[base + b] ^ regs[base + c];
            },
            OP_EQ => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = if (regs[base + b] == regs[base + c]) 1 else 0;
            },
            OP_LT => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = if (regs[base + b] < regs[base + c]) 1 else 0;
            },
            OP_LE => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                regs[base + a] = if (regs[base + b] <= regs[base + c]) 1 else 0;
            },
            OP_JMP => pc = @intCast(insn >> 16),
            OP_JZ => {
                if (regs[base + a] == 0) {
                    pc = @intCast(insn >> 16);
                }
            },
            OP_JNZ => {
                if (regs[base + a] != 0) {
                    pc = @intCast(insn >> 16);
                }
            },
            OP_CALL => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                frames[sp] = .{ .ret_pc = pc, .caller_base = base, .ret_reg = a };
                sp += 1;
                base += b;
                pc = funcs[c];
            },
            OP_RET => {
                const val = regs[base + a];
                if (sp == 0) return val;
                sp -= 1;
                const f = frames[sp];
                base = f.caller_base;
                pc = f.ret_pc;
                regs[base + f.ret_reg] = val;
            },
            OP_ARR_ALLOC => {
                const b: usize = @intCast((insn >> 16) & 255);
                const n = regs[base + b];
                if (n < 0) return error.NegativeArraySize;
                const un: usize = @intCast(n);
                const off = vm.heap_data.items.len;
                try vm.heap_data.appendNTimes(vm.alloc, 0, un);
                const id = vm.heap_objs.items.len;
                try vm.heap_objs.append(vm.alloc, .{ .off = off, .len = un });
                regs[base + a] = @intCast(id);
            },
            OP_ARR_GET => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                const id: usize = @intCast(regs[base + b]);
                const idx = regs[base + c];
                const obj = vm.heap_objs.items[id];
                if (idx < 0 or @as(usize, @intCast(idx)) >= obj.len) return error.OutOfBounds;
                regs[base + a] = vm.heap_data.items[obj.off + @as(usize, @intCast(idx))];
            },
            OP_ARR_SET => {
                const b: usize = @intCast((insn >> 16) & 255);
                const c: usize = @intCast((insn >> 24) & 255);
                const id: usize = @intCast(regs[base + a]);
                const idx = regs[base + b];
                const obj = vm.heap_objs.items[id];
                if (idx < 0 or @as(usize, @intCast(idx)) >= obj.len) return error.OutOfBounds;
                vm.heap_data.items[obj.off + @as(usize, @intCast(idx))] = regs[base + c];
            },
            else => return error.IllegalOpcode,
        }
    }
}

pub fn main(init: std.process.Init) !void {
    const alloc = init.gpa;
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 6) {
        std.process.exit(2);
    }
    const fib_n = try std.fmt.parseInt(i64, args[1], 10);
    const sieve_n = try std.fmt.parseInt(i64, args[2], 10);
    const loop_n = try std.fmt.parseInt(i64, args[3], 10);
    const sort_n = try std.fmt.parseInt(i64, args[4], 10);
    const rounds = try std.fmt.parseInt(i64, args[5], 10);

    const p_fib = Program{ .code = &fib_code, .consts = &.{}, .funcs = &fib_funcs };
    const p_sieve = Program{ .code = &sieve_code, .consts = &sieve_consts, .funcs = &.{} };
    const p_arith = Program{ .code = &arith_code, .consts = &arith_consts, .funcs = &.{} };
    const p_sort = Program{ .code = &sortdot_code, .consts = &sortdot_consts, .funcs = &.{} };

    var vm = Vm{
        .alloc = alloc,
        .regs = try alloc.alloc(i64, 65536),
        .frames = try alloc.alloc(Frame, 4096),
        .heap_data = .empty,
        .heap_objs = .empty,
    };
    defer alloc.free(vm.regs);
    defer alloc.free(vm.frames);
    defer vm.heap_data.deinit(alloc);
    defer vm.heap_objs.deinit(alloc);

    var s_fib: i64 = 0;
    var s_sieve: i64 = 0;
    var s_arith: i64 = 0;
    var s_sort: i64 = 0;
    var r: i64 = 0;
    while (r < rounds) : (r += 1) {
        const seed = r + 1;
        s_fib = @rem(s_fib + try vmExec(&vm, p_fib, fib_n, 0), MOD_SUM);
        s_sieve = @rem(s_sieve + try vmExec(&vm, p_sieve, sieve_n, 0), MOD_SUM);
        s_arith = @rem(s_arith + try vmExec(&vm, p_arith, loop_n, seed), MOD_SUM);
        s_sort = @rem(s_sort + try vmExec(&vm, p_sort, sort_n, seed), MOD_SUM);
    }
    const total = @rem(s_fib + s_sieve + s_arith + s_sort, MOD_SUM);
    var out_buf: [256]u8 = undefined;
    var out = std.Io.File.stdout().writer(init.io, &out_buf);
    try out.interface.print("fib={d} sieve={d} arith={d} sortdot={d} total={d}\n", .{ s_fib, s_sieve, s_arith, s_sort, total });
    try out.interface.flush();
}
