# app/vm: small register-based bytecode interpreter

An `app` row (tests/bench/README.md "Applications"). Every language implements the same 23-opcode register-based bytecode virtual machine (flat register file, call-frame stack, bounds-checked array heap, standard switch/match dispatch) and runs four deterministic bytecode programs parameterized by CLI arguments. The bytecode interpreter and its programs are the task itself, so every cell is kind `std` with zero external dependencies.

## Command line

```
vm <fib_n> <sieve_n> <loop_n> <sort_n> <rounds>
```

Every parameter comes from the command line so no compiler can constant-fold the bytecode execution. The program prints one line:

```
fib=<n> sieve=<n> arith=<n> sortdot=<n> total=<n>
```

where each component is the sum over all `<rounds>` rounds modulo `1_000_000_007`, and `total` is their sum modulo `1_000_000_007`.

## The bytecode ISA

Fixed-width 32-bit instructions (`u32`):
- `op = insn & 0xff` (bits 0..7)
- `a = (insn >> 8) & 0xff` (bits 8..15)
- `b = (insn >> 16) & 0xff` (bits 16..23)
- `c = (insn >> 24) & 0xff` (bits 24..31)
- `uimm = insn >> 16` (unsigned 16-bit target PC or constant-pool index)
- `simm = sign_extend_16(insn >> 16)` (signed 16-bit immediate)

23 opcodes:
- `0 HALT`: return `regs[base + a]`
- `1 LOADI`: `regs[base + a] = simm`
- `2 LOADK`: `regs[base + a] = consts[uimm]`
- `3 MOV`: `regs[base + a] = regs[base + b]`
- `4 ADD`, `5 SUB`, `6 MUL`, `7 DIV`, `8 MOD`: `regs[base + a] = regs[base + b] op regs[base + c]` (signed 64-bit integer arithmetic; `DIV` truncates toward zero, `MOD` is remainder)
- `9 ADDI`: `regs[base + a] += simm`
- `10 BAND`, `11 BXOR`: bitwise AND / XOR on 64-bit values
- `12 EQ`, `13 LT`, `14 LE`: `1` if true else `0`
- `15 JMP`: `pc = uimm`
- `16 JZ`, `17 JNZ`: conditional branch to `uimm` on `regs[base + a] == 0` / `!= 0`
- `18 CALL`: push `(ret_pc = pc, caller_base = base, ret_reg = a)`, `base += b`, `pc = funcs[c]`
- `19 RET`: pop frame (or halt with `regs[base + a]` if stack empty) and store return value into caller's `ret_reg`
- `20 ARR_ALLOC`: allocate zero-initialized `i64` array of length `regs[base + b]` on VM heap, store handle id in `regs[base + a]`
- `21 ARR_GET`: bounds-checked read `regs[base + a] = heap[regs[base + b]][regs[base + c]]`
- `22 ARR_SET`: bounds-checked write `heap[regs[base + a]][regs[base + b]] = regs[base + c]`

## The four bytecode programs

Each round `r` in `0..rounds` (with `seed = r + 1`) resets the VM array heap and executes:
1. **Recursive Fibonacci (`fib(fib_n)`)**: recursive register-window calls exercising `CALL`/`RET`, `LE`, `JZ`, `MOV`, `ADDI`, `ADD`.
2. **Iterative Prime Sieve (`sieve(sieve_n)`)**: allocates a VM heap array of `sieve_n + 1` elements, marks composites starting from `i * i`, counts primes and sums them, returning `count * 1_000_000 + (sum % 1_000_000)`.
3. **Loop Arithmetic & Checksum (`arith(loop_n, seed)`)**: tight register loop combining LCG updates (`MUL`, `ADD`, `BAND`), `DIV`, `MOD`, `SUB`, `BXOR`, `EQ` modulo `1_000_000_007`.
4. **Array Shell Sort + Weighted Dot Product (`sortdot(sort_n, seed)`)**: allocates two VM arrays `A` and `B` of length `sort_n`, fills `A` via `x = (x * 48271 + 17) % 100003` and `B[i] = (i % 97) + 1`, sorts `A` in place with Shell sort (`gap = n/2, gap/2, ...`), and returns `sum(A[i] * B[i]) % 1_000_000_007`.

## Cells

| Language | Kind | Program | Libraries |
|---|---|---|---|
| Mk | `std` | `mk/vm.mk`: `view struct` VM state with arena slices and `List`, `match` dispatch | std only |
| C | `std` | `vm.c`: flat heap arrays, standard C17 `switch` dispatch (no computed goto, no hints) | std only |
| Rust | `std` | `vm.rs`: `Vec` register/frame/heap buffers, `match` dispatch (no `unsafe`, no hints) | std only |
| Zig | `std` | `vm.zig`: slices and `std.ArrayList`, `switch` dispatch (no forced inline, no hints) | std only |
| Go | `std` | `testdata/vm.go`: slices, `switch` dispatch (no `unsafe`, no `//go:`) | std only |
| C# | `std` | `vm.cs`: flat arrays, `switch` dispatch (no `unsafe`, no `MethodImpl`, no `Unsafe.*`) | std only |

- **Overflow class:** `never`. Every intermediate stays strictly within signed 64-bit bounds.
- **Environment:** `MK_CARRIERS=1` (single-threaded); `publish`'s one-CPU affinity.
- **Bans:** no computed goto, no `unsafe`, no unchecked indexing, no inlining or branch hints (`appRowViolations`).
