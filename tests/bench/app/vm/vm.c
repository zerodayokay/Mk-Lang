// vm, C, std: the task of README.md, a 23-opcode register-based bytecode
// virtual machine running four deterministic bytecode programs (recursive
// fibonacci, iterative prime sieve, arithmetic/checksum loop, and Shell sort
// plus weighted dot product) over a flat register file, call-frame stack and
// bounds-checked array heap. Standard C17 switch dispatch (no computed goto,
// no inline hints, no restrict, no attributes). All arithmetic uses int64_t
// and stays within signed 64-bit bounds without overflow.
// Usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

enum {
    OP_HALT = 0,
    OP_LOADI = 1,
    OP_LOADK = 2,
    OP_MOV = 3,
    OP_ADD = 4,
    OP_SUB = 5,
    OP_MUL = 6,
    OP_DIV = 7,
    OP_MOD = 8,
    OP_ADDI = 9,
    OP_BAND = 10,
    OP_BXOR = 11,
    OP_EQ = 12,
    OP_LT = 13,
    OP_LE = 14,
    OP_JMP = 15,
    OP_JZ = 16,
    OP_JNZ = 17,
    OP_CALL = 18,
    OP_RET = 19,
    OP_ARR_ALLOC = 20,
    OP_ARR_GET = 21,
    OP_ARR_SET = 22
};

#define MOD_SUM 1000000007LL

typedef struct {
    int ret_pc;
    int caller_base;
    int ret_reg;
} Frame;

typedef struct {
    size_t off;
    size_t len;
} HeapObj;

typedef struct {
    uint32_t *code;
    size_t code_len;
    size_t code_cap;
    int64_t *consts;
    size_t consts_len;
    size_t consts_cap;
    int *funcs;
    size_t funcs_len;
    size_t funcs_cap;
} Program;

typedef struct {
    int64_t *regs;
    Frame *frames;
    int64_t *heap_data;
    size_t heap_len;
    size_t heap_cap;
    HeapObj *heap_objs;
    size_t objs_len;
    size_t objs_cap;
} Vm;

static void *xrealloc(void *p, size_t n) {
    void *q = realloc(p, n);
    if (!q && n > 0) {
        fprintf(stderr, "vm: out of memory\n");
        exit(1);
    }
    return q;
}

static uint32_t enc_abc(uint32_t op, uint32_t a, uint32_t b, uint32_t c) {
    return (op & 255u) | ((a & 255u) << 8) | ((b & 255u) << 16) | ((c & 255u) << 24);
}

static uint32_t enc_au(uint32_t op, uint32_t a, uint32_t u) {
    return (op & 255u) | ((a & 255u) << 8) | ((u & 65535u) << 16);
}

static uint32_t enc_as(uint32_t op, uint32_t a, int s) {
    uint32_t u = (uint32_t)(uint16_t)(int16_t)s;
    return (op & 255u) | ((a & 255u) << 8) | (u << 16);
}

static int64_t sign_ext16(uint32_t insn) {
    int raw = (int)(insn >> 16);
    return raw >= 32768 ? (int64_t)(raw - 65536) : (int64_t)raw;
}

static void prog_init(Program *p) {
    memset(p, 0, sizeof(*p));
}

static void prog_free(Program *p) {
    free(p->code);
    free(p->consts);
    free(p->funcs);
}

static void prog_emit(Program *p, uint32_t insn) {
    if (p->code_len == p->code_cap) {
        p->code_cap = p->code_cap ? p->code_cap * 2 : 64;
        p->code = (uint32_t *)xrealloc(p->code, p->code_cap * sizeof(uint32_t));
    }
    p->code[p->code_len++] = insn;
}

static void prog_const(Program *p, int64_t v) {
    if (p->consts_len == p->consts_cap) {
        p->consts_cap = p->consts_cap ? p->consts_cap * 2 : 16;
        p->consts = (int64_t *)xrealloc(p->consts, p->consts_cap * sizeof(int64_t));
    }
    p->consts[p->consts_len++] = v;
}

static void prog_func(Program *p, int pc) {
    if (p->funcs_len == p->funcs_cap) {
        p->funcs_cap = p->funcs_cap ? p->funcs_cap * 2 : 8;
        p->funcs = (int *)xrealloc(p->funcs, p->funcs_cap * sizeof(int));
    }
    p->funcs[p->funcs_len++] = pc;
}

static Program build_fib(void) {
    Program p;
    prog_init(&p);
    prog_func(&p, 0);
    prog_emit(&p, enc_as(OP_LOADI, 1, 1));
    prog_emit(&p, enc_abc(OP_LE, 1, 0, 1));
    prog_emit(&p, enc_au(OP_JZ, 1, 4));
    prog_emit(&p, enc_abc(OP_RET, 0, 0, 0));
    prog_emit(&p, enc_abc(OP_MOV, 4, 0, 0));
    prog_emit(&p, enc_as(OP_ADDI, 4, -1));
    prog_emit(&p, enc_abc(OP_CALL, 2, 4, 0));
    prog_emit(&p, enc_abc(OP_MOV, 4, 0, 0));
    prog_emit(&p, enc_as(OP_ADDI, 4, -2));
    prog_emit(&p, enc_abc(OP_CALL, 3, 4, 0));
    prog_emit(&p, enc_abc(OP_ADD, 0, 2, 3));
    prog_emit(&p, enc_abc(OP_RET, 0, 0, 0));
    return p;
}

static Program build_sieve(void) {
    Program p;
    prog_init(&p);
    prog_const(&p, 1000000LL);
    prog_emit(&p, enc_abc(OP_MOV, 1, 0, 0));
    prog_emit(&p, enc_as(OP_ADDI, 1, 1));
    prog_emit(&p, enc_abc(OP_ARR_ALLOC, 2, 1, 0));
    prog_emit(&p, enc_as(OP_LOADI, 5, 1));
    prog_emit(&p, enc_as(OP_LOADI, 3, 2));
    prog_emit(&p, enc_abc(OP_MUL, 6, 3, 3));
    prog_emit(&p, enc_abc(OP_LE, 6, 6, 0));
    prog_emit(&p, enc_au(OP_JZ, 6, 18));
    prog_emit(&p, enc_abc(OP_ARR_GET, 6, 2, 3));
    prog_emit(&p, enc_au(OP_JNZ, 6, 16));
    prog_emit(&p, enc_abc(OP_MUL, 4, 3, 3));
    prog_emit(&p, enc_abc(OP_LE, 6, 4, 0));
    prog_emit(&p, enc_au(OP_JZ, 6, 16));
    prog_emit(&p, enc_abc(OP_ARR_SET, 2, 4, 5));
    prog_emit(&p, enc_abc(OP_ADD, 4, 4, 3));
    prog_emit(&p, enc_au(OP_JMP, 0, 11));
    prog_emit(&p, enc_as(OP_ADDI, 3, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 5));
    prog_emit(&p, enc_as(OP_LOADI, 7, 0));
    prog_emit(&p, enc_as(OP_LOADI, 8, 0));
    prog_emit(&p, enc_as(OP_LOADI, 3, 2));
    prog_emit(&p, enc_abc(OP_LE, 6, 3, 0));
    prog_emit(&p, enc_au(OP_JZ, 6, 29));
    prog_emit(&p, enc_abc(OP_ARR_GET, 6, 2, 3));
    prog_emit(&p, enc_au(OP_JNZ, 6, 27));
    prog_emit(&p, enc_as(OP_ADDI, 7, 1));
    prog_emit(&p, enc_abc(OP_ADD, 8, 8, 3));
    prog_emit(&p, enc_as(OP_ADDI, 3, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 21));
    prog_emit(&p, enc_au(OP_LOADK, 9, 0));
    prog_emit(&p, enc_abc(OP_MOD, 8, 8, 9));
    prog_emit(&p, enc_abc(OP_MUL, 7, 7, 9));
    prog_emit(&p, enc_abc(OP_ADD, 0, 7, 8));
    prog_emit(&p, enc_abc(OP_HALT, 0, 0, 0));
    return p;
}

static Program build_arith(void) {
    Program p;
    prog_init(&p);
    prog_const(&p, 1000000007LL);
    prog_const(&p, 1103515245LL);
    prog_const(&p, 12345LL);
    prog_const(&p, 0x7fffffffLL);
    prog_const(&p, 97LL);
    prog_emit(&p, enc_as(OP_LOADI, 2, 0));
    prog_emit(&p, enc_as(OP_LOADI, 3, 0));
    prog_emit(&p, enc_au(OP_LOADK, 4, 0));
    prog_emit(&p, enc_au(OP_LOADK, 5, 1));
    prog_emit(&p, enc_au(OP_LOADK, 6, 2));
    prog_emit(&p, enc_au(OP_LOADK, 7, 3));
    prog_emit(&p, enc_au(OP_LOADK, 8, 4));
    prog_emit(&p, enc_abc(OP_LT, 9, 3, 0));
    prog_emit(&p, enc_au(OP_JZ, 9, 21));
    prog_emit(&p, enc_abc(OP_MUL, 1, 1, 5));
    prog_emit(&p, enc_abc(OP_ADD, 1, 1, 6));
    prog_emit(&p, enc_abc(OP_BAND, 1, 1, 7));
    prog_emit(&p, enc_abc(OP_DIV, 9, 1, 8));
    prog_emit(&p, enc_abc(OP_MOD, 10, 1, 8));
    prog_emit(&p, enc_abc(OP_SUB, 11, 1, 10));
    prog_emit(&p, enc_abc(OP_BXOR, 11, 11, 9));
    prog_emit(&p, enc_abc(OP_EQ, 9, 10, 8));
    prog_emit(&p, enc_abc(OP_ADD, 2, 2, 11));
    prog_emit(&p, enc_abc(OP_MOD, 2, 2, 4));
    prog_emit(&p, enc_as(OP_ADDI, 3, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 7));
    prog_emit(&p, enc_abc(OP_HALT, 2, 0, 0));
    return p;
}

static Program build_sortdot(void) {
    Program p;
    prog_init(&p);
    prog_const(&p, 1000000007LL);
    prog_const(&p, 100003LL);
    prog_const(&p, 48271LL);
    prog_const(&p, 97LL);
    prog_emit(&p, enc_abc(OP_ARR_ALLOC, 2, 0, 0));
    prog_emit(&p, enc_abc(OP_ARR_ALLOC, 3, 0, 0));
    prog_emit(&p, enc_au(OP_LOADK, 15, 0));
    prog_emit(&p, enc_au(OP_LOADK, 12, 1));
    prog_emit(&p, enc_au(OP_LOADK, 13, 2));
    prog_emit(&p, enc_au(OP_LOADK, 14, 3));
    prog_emit(&p, enc_as(OP_LOADI, 11, 2));
    prog_emit(&p, enc_as(OP_LOADI, 4, 0));
    prog_emit(&p, enc_abc(OP_LT, 9, 4, 0));
    prog_emit(&p, enc_au(OP_JZ, 9, 19));
    prog_emit(&p, enc_abc(OP_MUL, 1, 1, 13));
    prog_emit(&p, enc_as(OP_ADDI, 1, 17));
    prog_emit(&p, enc_abc(OP_MOD, 1, 1, 12));
    prog_emit(&p, enc_abc(OP_ARR_SET, 2, 4, 1));
    prog_emit(&p, enc_abc(OP_MOD, 10, 4, 14));
    prog_emit(&p, enc_as(OP_ADDI, 10, 1));
    prog_emit(&p, enc_abc(OP_ARR_SET, 3, 4, 10));
    prog_emit(&p, enc_as(OP_ADDI, 4, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 8));
    prog_emit(&p, enc_abc(OP_DIV, 6, 0, 11));
    prog_emit(&p, enc_au(OP_JZ, 6, 40));
    prog_emit(&p, enc_abc(OP_MOV, 4, 6, 0));
    prog_emit(&p, enc_abc(OP_LT, 9, 4, 0));
    prog_emit(&p, enc_au(OP_JZ, 9, 38));
    prog_emit(&p, enc_abc(OP_ARR_GET, 7, 2, 4));
    prog_emit(&p, enc_abc(OP_MOV, 5, 4, 0));
    prog_emit(&p, enc_abc(OP_LE, 9, 6, 5));
    prog_emit(&p, enc_au(OP_JZ, 9, 35));
    prog_emit(&p, enc_abc(OP_SUB, 10, 5, 6));
    prog_emit(&p, enc_abc(OP_ARR_GET, 8, 2, 10));
    prog_emit(&p, enc_abc(OP_LT, 9, 7, 8));
    prog_emit(&p, enc_au(OP_JZ, 9, 35));
    prog_emit(&p, enc_abc(OP_ARR_SET, 2, 5, 8));
    prog_emit(&p, enc_abc(OP_MOV, 5, 10, 0));
    prog_emit(&p, enc_au(OP_JMP, 0, 26));
    prog_emit(&p, enc_abc(OP_ARR_SET, 2, 5, 7));
    prog_emit(&p, enc_as(OP_ADDI, 4, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 22));
    prog_emit(&p, enc_abc(OP_DIV, 6, 6, 11));
    prog_emit(&p, enc_au(OP_JMP, 0, 20));
    prog_emit(&p, enc_as(OP_LOADI, 8, 0));
    prog_emit(&p, enc_as(OP_LOADI, 4, 0));
    prog_emit(&p, enc_abc(OP_LT, 9, 4, 0));
    prog_emit(&p, enc_au(OP_JZ, 9, 51));
    prog_emit(&p, enc_abc(OP_ARR_GET, 7, 2, 4));
    prog_emit(&p, enc_abc(OP_ARR_GET, 10, 3, 4));
    prog_emit(&p, enc_abc(OP_MUL, 7, 7, 10));
    prog_emit(&p, enc_abc(OP_ADD, 8, 8, 7));
    prog_emit(&p, enc_abc(OP_MOD, 8, 8, 15));
    prog_emit(&p, enc_as(OP_ADDI, 4, 1));
    prog_emit(&p, enc_au(OP_JMP, 0, 42));
    prog_emit(&p, enc_abc(OP_HALT, 8, 0, 0));
    return p;
}

static int64_t vm_exec(Vm *vm, const Program *prog, int64_t arg0, int64_t arg1) {
    vm->heap_len = 0;
    vm->objs_len = 0;
    int64_t *regs = vm->regs;
    Frame *frames = vm->frames;
    const uint32_t *code = prog->code;
    const int64_t *consts = prog->consts;
    const int *funcs = prog->funcs;

    regs[0] = arg0;
    regs[1] = arg1;
    int pc = 0;
    int base = 0;
    int sp = 0;

    for (;;) {
        uint32_t insn = code[pc++];
        uint32_t op = insn & 255u;
        int a = (int)((insn >> 8) & 255u);
        switch (op) {
        case OP_HALT:
            return regs[base + a];
        case OP_LOADI:
            regs[base + a] = sign_ext16(insn);
            break;
        case OP_LOADK:
            regs[base + a] = consts[insn >> 16];
            break;
        case OP_MOV: {
            int b = (int)((insn >> 16) & 255u);
            regs[base + a] = regs[base + b];
            break;
        }
        case OP_ADD: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] + regs[base + c];
            break;
        }
        case OP_SUB: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] - regs[base + c];
            break;
        }
        case OP_MUL: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] * regs[base + c];
            break;
        }
        case OP_DIV: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] / regs[base + c];
            break;
        }
        case OP_MOD: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] % regs[base + c];
            break;
        }
        case OP_ADDI:
            regs[base + a] += sign_ext16(insn);
            break;
        case OP_BAND: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] & regs[base + c];
            break;
        }
        case OP_BXOR: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] ^ regs[base + c];
            break;
        }
        case OP_EQ: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] == regs[base + c] ? 1 : 0;
            break;
        }
        case OP_LT: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] < regs[base + c] ? 1 : 0;
            break;
        }
        case OP_LE: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            regs[base + a] = regs[base + b] <= regs[base + c] ? 1 : 0;
            break;
        }
        case OP_JMP:
            pc = (int)(insn >> 16);
            break;
        case OP_JZ:
            if (regs[base + a] == 0) {
                pc = (int)(insn >> 16);
            }
            break;
        case OP_JNZ:
            if (regs[base + a] != 0) {
                pc = (int)(insn >> 16);
            }
            break;
        case OP_CALL: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            frames[sp].ret_pc = pc;
            frames[sp].caller_base = base;
            frames[sp].ret_reg = a;
            sp++;
            base += b;
            pc = funcs[c];
            break;
        }
        case OP_RET: {
            int64_t val = regs[base + a];
            if (sp == 0) {
                return val;
            }
            sp--;
            Frame f = frames[sp];
            base = f.caller_base;
            pc = f.ret_pc;
            regs[base + f.ret_reg] = val;
            break;
        }
        case OP_ARR_ALLOC: {
            int b = (int)((insn >> 16) & 255u);
            int64_t n = regs[base + b];
            if (n < 0) {
                fprintf(stderr, "vm: negative array size\n");
                exit(1);
            }
            size_t un = (size_t)n;
            size_t off = vm->heap_len;
            size_t need = off + un;
            if (need > vm->heap_cap) {
                size_t cap = vm->heap_cap ? vm->heap_cap : 1024;
                while (cap < need) cap *= 2;
                vm->heap_data = (int64_t *)xrealloc(vm->heap_data, cap * sizeof(int64_t));
                vm->heap_cap = cap;
            }
            if (un > 0) {
                memset(vm->heap_data + off, 0, un * sizeof(int64_t));
            }
            vm->heap_len = need;
            if (vm->objs_len == vm->objs_cap) {
                vm->objs_cap = vm->objs_cap ? vm->objs_cap * 2 : 16;
                vm->heap_objs = (HeapObj *)xrealloc(vm->heap_objs, vm->objs_cap * sizeof(HeapObj));
            }
            size_t id = vm->objs_len++;
            vm->heap_objs[id].off = off;
            vm->heap_objs[id].len = un;
            regs[base + a] = (int64_t)id;
            break;
        }
        case OP_ARR_GET: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            int64_t id = regs[base + b];
            int64_t idx = regs[base + c];
            if (id < 0 || (size_t)id >= vm->objs_len) {
                fprintf(stderr, "vm: bad array handle\n");
                exit(1);
            }
            HeapObj obj = vm->heap_objs[id];
            if (idx < 0 || (size_t)idx >= obj.len) {
                fprintf(stderr, "vm: array index out of bounds\n");
                exit(1);
            }
            regs[base + a] = vm->heap_data[obj.off + (size_t)idx];
            break;
        }
        case OP_ARR_SET: {
            int b = (int)((insn >> 16) & 255u);
            int c = (int)((insn >> 24) & 255u);
            int64_t id = regs[base + a];
            int64_t idx = regs[base + b];
            if (id < 0 || (size_t)id >= vm->objs_len) {
                fprintf(stderr, "vm: bad array handle\n");
                exit(1);
            }
            HeapObj obj = vm->heap_objs[id];
            if (idx < 0 || (size_t)idx >= obj.len) {
                fprintf(stderr, "vm: array index out of bounds\n");
                exit(1);
            }
            vm->heap_data[obj.off + (size_t)idx] = regs[base + c];
            break;
        }
        default:
            fprintf(stderr, "vm: illegal opcode %u\n", op);
            exit(1);
        }
    }
}

int main(int argc, char **argv) {
    if (argc != 6) {
        fprintf(stderr, "usage: vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>\n");
        return 2;
    }
    int64_t fib_n = strtoll(argv[1], NULL, 10);
    int64_t sieve_n = strtoll(argv[2], NULL, 10);
    int64_t loop_n = strtoll(argv[3], NULL, 10);
    int64_t sort_n = strtoll(argv[4], NULL, 10);
    int64_t rounds = strtoll(argv[5], NULL, 10);

    Program p_fib = build_fib();
    Program p_sieve = build_sieve();
    Program p_arith = build_arith();
    Program p_sort = build_sortdot();

    Vm vm;
    memset(&vm, 0, sizeof(vm));
    vm.regs = (int64_t *)xrealloc(NULL, 65536 * sizeof(int64_t));
    vm.frames = (Frame *)xrealloc(NULL, 4096 * sizeof(Frame));

    int64_t s_fib = 0, s_sieve = 0, s_arith = 0, s_sort = 0;
    for (int64_t r = 0; r < rounds; r++) {
        int64_t seed = r + 1;
        s_fib = (s_fib + vm_exec(&vm, &p_fib, fib_n, 0)) % MOD_SUM;
        s_sieve = (s_sieve + vm_exec(&vm, &p_sieve, sieve_n, 0)) % MOD_SUM;
        s_arith = (s_arith + vm_exec(&vm, &p_arith, loop_n, seed)) % MOD_SUM;
        s_sort = (s_sort + vm_exec(&vm, &p_sort, sort_n, seed)) % MOD_SUM;
    }
    int64_t total = (s_fib + s_sieve + s_arith + s_sort) % MOD_SUM;
    printf("fib=%lld sieve=%lld arith=%lld sortdot=%lld total=%lld\n",
           (long long)s_fib, (long long)s_sieve, (long long)s_arith, (long long)s_sort, (long long)total);

    free(vm.regs);
    free(vm.frames);
    free(vm.heap_data);
    free(vm.heap_objs);
    prog_free(&p_fib);
    prog_free(&p_sieve);
    prog_free(&p_arith);
    prog_free(&p_sort);
    return 0;
}
