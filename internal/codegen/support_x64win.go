// Why: Windows needs a stack probe (__chkstk) that no C runtime provides
// here, and the task switch must also swap the thread's stack bounds in
// the NT_TIB, as fibers do, so the system sees each task's own stack.
//
// What: win64Chkstk (__chkstk), win64ChkstkAligned (mk_chkstk_65536, for
// frames aligned over a page), win64Switch (mk_switch with the NT_TIB
// words), win64TaskStart (mk_task_start), win64TrapCall (mk_trap_call, the
// resumed trapped call) and win64CallSystem (mk_call_system), as module
// assembly.
//
// How: AT&T syntax for LLVM's integrated assembler, each routine with its
// SEH unwind codes, so an unwinder can walk through it.

package codegen

// win64Chkstk touches each page from the caller's stack pointer down by the
// size in rax, so the guard page is hit in order and never jumped; every
// register but the flags is preserved.
const win64Chkstk = win64ChkstkPushes + win64ChkstkLoop

// win64ChkstkPushes is the prologue of both probes, with its unwind codes:
// rcx and rax pushed, so an unwinder finds the
// caller's return address at [rsp + 16], as the kind-1 row says.
const win64ChkstkPushes = `
pushq %rcx
.seh_pushreg %rcx
pushq %rax
.seh_pushreg %rax
.seh_endprologue
`

// win64ChkstkAligned is mk_chkstk_65536, for a function whose
// frame LLVM realigns over a page. Its Win64 prologue calls the probe with
// the frame's size in rax, subtracts rax from rsp (R, the caller's rsp,
// less rax) and only then rounds rsp down to the alignment n, by up to
// n - 16 bytes no probe touched, so the first call from the frame (or a
// store low in it) could land past the guard page: an access violation
// with no handler. This probe is __chkstk from R down to (R - rax) &
// -65536, the lowest rounding of R - rax for any n up to 65536, so it
// touches those pages too, in order, under any inlining merge of
// alignments. It probes (R - rax) mod 65536 bytes more than the frame's
// size: at most 65536 - n more than the rounding to n needs, none at n =
// 65536 (a flat rax + 65520 would probe up to 65520 more). It keeps rax
// for the prologue's subtraction and pushes
// nothing more, so the caller's return address stays at [rsp + 16] for
// its kind-1 row, as __chkstk's.
const win64ChkstkAligned = win64ChkstkPushes + `
leaq 24(%rsp), %rcx
subq %rax, %rcx
andq $0xffff, %rcx
addq %rcx, %rax
` + win64ChkstkLoop

// win64ChkstkLoop is the probe loop of __chkstk and mk_chkstk_65536,
// after the two pushes: it touches every page from the caller's stack
// pointer down by rax, then restores rax and rcx.
const win64ChkstkLoop = `cmpq $0x1000, %rax
leaq 24(%rsp), %rcx
jb 1f
2:
subq $0x1000, %rcx
orq $0, (%rcx)
subq $0x1000, %rax
cmpq $0x1000, %rax
ja 2b
1:
subq %rax, %rcx
orq $0, (%rcx)
popq %rax
popq %rcx
retq
`

// win64Switch is mk_switch(from: rcx, to: rdx): the eight
// callee-saved GPRs, xmm6 to xmm15, MXCSR and the x87 control word on the
// stack (240 bytes with the return address); NT_TIB StackBase, StackLimit,
// DeallocationStack and GuaranteedStackBytes saved into from[1..4] and
// installed from to[1..4], as fibers do.
const win64Switch = `
pushq %rbp
.seh_pushreg %rbp
pushq %rbx
.seh_pushreg %rbx
pushq %rdi
.seh_pushreg %rdi
pushq %rsi
.seh_pushreg %rsi
pushq %r12
.seh_pushreg %r12
pushq %r13
.seh_pushreg %r13
pushq %r14
.seh_pushreg %r14
pushq %r15
.seh_pushreg %r15
subq $168, %rsp
.seh_stackalloc 168
movups %xmm6, 0(%rsp)
.seh_savexmm %xmm6, 0
movups %xmm7, 16(%rsp)
.seh_savexmm %xmm7, 16
movups %xmm8, 32(%rsp)
.seh_savexmm %xmm8, 32
movups %xmm9, 48(%rsp)
.seh_savexmm %xmm9, 48
movups %xmm10, 64(%rsp)
.seh_savexmm %xmm10, 64
movups %xmm11, 80(%rsp)
.seh_savexmm %xmm11, 80
movups %xmm12, 96(%rsp)
.seh_savexmm %xmm12, 96
movups %xmm13, 112(%rsp)
.seh_savexmm %xmm13, 112
movups %xmm14, 128(%rsp)
.seh_savexmm %xmm14, 128
movups %xmm15, 144(%rsp)
.seh_savexmm %xmm15, 144
.seh_endprologue
stmxcsr 160(%rsp)
fnstcw 164(%rsp)
movq %gs:0x08, %rax
movq %rax, 8(%rcx)
movq %gs:0x10, %rax
movq %rax, 16(%rcx)
movq %gs:0x1478, %rax
movq %rax, 24(%rcx)
movl %gs:0x1748, %eax
movq %rax, 32(%rcx)
movq %rsp, (%rcx)
movq (%rdx), %rsp
movq 8(%rdx), %rax
movq %rax, %gs:0x08
movq 16(%rdx), %rax
movq %rax, %gs:0x10
movq 24(%rdx), %rax
movq %rax, %gs:0x1478
movq 32(%rdx), %rax
movl %eax, %gs:0x1748
movups 0(%rsp), %xmm6
movups 16(%rsp), %xmm7
movups 32(%rsp), %xmm8
movups 48(%rsp), %xmm9
movups 64(%rsp), %xmm10
movups 80(%rsp), %xmm11
movups 96(%rsp), %xmm12
movups 112(%rsp), %xmm13
movups 128(%rsp), %xmm14
movups 144(%rsp), %xmm15
ldmxcsr 160(%rsp)
fldcw 164(%rsp)
addq $168, %rsp
popq %r15
popq %r14
popq %r13
popq %r12
popq %rsi
popq %rdi
popq %rbx
popq %rbp
retq
`

// win64TaskStart is where a fresh context's first switch returns: the entry
// is in r12 and its argument in r13 (InitContext's frame); it reserves the
// 32-byte shadow space and calls entry(arg). A task's entry never returns.
const win64TaskStart = `
movq %r13, %rcx
subq $32, %rsp
.seh_stackalloc 32
.seh_endprologue
callq *%r12
ud2
`

// win64TrapCall is mk_trap_call (trapform.go): the vectored handler
// resumes a trapped call here (std/sys/windows/fault.mk TrapResume) with
// rsp the faulting stack pointer, r11 the trap's address plus one and r10
// the callee, which takes no argument and never returns. It writes a
// machine frame below rsp, 16-aligned (the return address r11 at 0, the
// faulting rsp at 24; the other slots are not read), which
// UWOP_PUSH_MACHFRAME restores, so RtlVirtualUnwind steps from the
// callee's frame through this one to the trapping function's, whatever
// its alignment, as from a call at the trap; then the shadow space and the
// call. Nothing unwinds from before the .seh_pushframe: no instruction
// there faults, unless the stack itself is exhausted, which the call at
// the trap would have met too.
const win64TrapCall = `
movq %rsp, %rax
andq $-16, %rsp
subq $48, %rsp
movq %rax, 24(%rsp)
movq %r11, (%rsp)
.seh_pushframe
subq $32, %rsp
.seh_stackalloc 32
.seh_endprologue
callq *%r10
ud2
`

// win64CallSystem is mk_call_system(fn: rcx, args: rdx, ss: r8): run
// fn(args) on the carrier's system stack ss.sp, with the thread's NT_TIB
// StackBase, StackLimit and DeallocationStack set to ss.tib[0..2] for the
// call and restored after, so the kernel grows the system stack through its
// guard page and exception dispatch sees a stack inside the recorded one.
// The StackLimit the call leaves is stored back in ss.tib[1]: the kernel
// lowers it as the call commits pages, and the next call must start from
// that limit, since a probe trusts every page below it to be unused (MSVC's
// __chkstk stores a zero in each).
const win64CallSystem = `
pushq %rbx
.seh_pushreg %rbx
pushq %rsi
.seh_pushreg %rsi
pushq %rdi
.seh_pushreg %rdi
pushq %r12
.seh_pushreg %r12
pushq %r13
.seh_pushreg %r13
movq %rsp, %rbx
.seh_setframe %rbx, 0
.seh_endprologue
movq %r8, %r13
movq %gs:0x08, %rsi
movq %gs:0x10, %rdi
movq %gs:0x1478, %r12
movq 8(%r8), %rax
movq %rax, %gs:0x08
movq 16(%r8), %rax
movq %rax, %gs:0x10
movq 24(%r8), %rax
movq %rax, %gs:0x1478
movq (%r8), %rsp
subq $32, %rsp
movq %rcx, %rax
movq %rdx, %rcx
callq *%rax
movq %gs:0x10, %rax
movq %rax, 16(%r13)
movq %rbx, %rsp
movq %rsi, %gs:0x08
movq %rdi, %gs:0x10
movq %r12, %gs:0x1478
popq %r13
popq %r12
popq %rdi
popq %rsi
popq %rbx
retq
`
