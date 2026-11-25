// Why: some programs (a JIT, a garbage collector, a sandbox) need to see a
// hardware fault first and sometimes resume from it.
//
// What: runtime.AddFaultFilter, FaultContext, FaultAction (Pass, Resume)
// and FaultKind, and RunFilters, which the fault handlers call.
//
// How: filters run in the order added, before Mk's own report. On Windows
// in the vectored handler on the faulting stack (a stack overflow's inside
// the task's 16 KiB guarantee area); on Linux and macOS in the SIGSEGV,
// SIGBUS and SIGILL handler on the carrier's alternate signal stack. The
// registers are read from and written back to the OS context at the
// offsets of ContextReg and ContextPc, per OS and architecture. A filter runs
// inside a fault: it must not take a lock the faulting code may hold,
// allocate, or suspend, and a fault inside a filter is reported, not
// filtered (RunFilters). The list is lock-free: append only, each filter
// written before the count that publishes it.

import std.raw
import std.rt
import std.sys

/// What faulted. Stack is an access to a guard page below a task's or a
/// carrier's stack: a stack overflow, reported as one wherever its pc is.
/// Access is any other read, write or execution the page does not allow.
/// Illegal is an illegal or privileged instruction (Windows'
/// EXCEPTION_ILLEGAL_INSTRUCTION and EXCEPTION_PRIV_INSTRUCTION, Linux's
/// SIGILL).
export enum FaultKind {
    /// An access to a guard page below a task's or a carrier's stack: a stack
    /// overflow.
    Stack
    /// Any other read, write or execution the page does not allow.
    Access
    /// An illegal or privileged instruction.
    Illegal
}

/// A fault's context: its kind and address, and the faulting thread's
/// general registers and program counter. A filter may edit them with the
/// setters and return the context with Resume. Reg(n) and SetReg(n)
/// number the registers as the target encodes them (RegCount of them): on
/// x86_64 0 rax, 1 rcx, 2 rdx, 3 rbx, 4 rsp, 5 rbp, 6 rsi, 7 rdi, 8 to 15
/// r8 to r15; on aarch64 0 to 30 x0 to x30, 31 sp.
export struct FaultContext {
    k: FaultKind
    at: uint
    regs: [32]u64
    ip: uint
}

/// RegCount is how many general registers a FaultContext holds: 16 on
/// x86_64, 32 on aarch64 (x0 to x30 and sp). O(1); allocates nothing.
export RegCount(): int {
    return if Target.arch == Arch.Arm64 { 32 } else { 16 }
}

// SpReg is the stack pointer's register number.
SpReg(): int {
    return if Target.arch == Arch.Arm64 { 31 } else { 4 }
}

/// A filter's answer: Resume at the context it edited, which the handler
/// installs, or Pass to the next filter and after the last on as with no
/// filter: to Mk's own report, or, for a fault in C code on Windows, to
/// the C code's own handler or the system (AddFaultFilter).
export enum FaultAction {
    /// Resume at ctx, the context the filter edited.
    Resume(ctx: FaultContext)
    /// Pass the fault to the next filter, and after the last on as with no
    /// filter.
    Pass
}

/// The registration of one filter. A filter stays for the life of the
/// process; there is no way to remove one.
export struct FilterId {
    n: int
}

/// What faulted. O(1); allocates nothing.
export FaultContext.Kind(&self): FaultKind {
    return self.k
}

/// The faulting address; for Stack, the stack pointer. O(1); allocates
/// nothing.
export FaultContext.Addr(&self): uint {
    return self.at
}

/// The program counter of the faulting instruction. O(1); allocates
/// nothing.
export FaultContext.Pc(&self): uint {
    return self.ip
}

/// The stack pointer: Reg(4) on x86_64, Reg(31) on aarch64. O(1);
/// allocates nothing.
export FaultContext.Sp(&self): uint {
    return Trunc[uint](self.regs[SpReg()])
}

/// General register n, 0 to RegCount() - 1, in the order FaultContext
/// lists. Another n panics with the bounds code (2). O(1); allocates
/// nothing.
export FaultContext.Reg(&self, n: int): u64 {
    return self.regs[..RegCount()][n]
}

/// Sets the program counter the thread resumes at. O(1); allocates nothing.
export FaultContext.SetPc(mut self, pc: uint) {
    self.ip = pc
}

/// Sets the stack pointer: Reg(4) on x86_64, Reg(31) on aarch64. O(1);
/// allocates nothing.
export FaultContext.SetSp(mut self, sp: uint) {
    self.regs[SpReg()] = u64(sp)
}

/// Sets general register n, 0 to RegCount() - 1. Another n panics with the
/// bounds code (2). O(1); allocates nothing.
export FaultContext.SetReg(mut self, n: int, v: u64) {
    if n < 0 || n >= RegCount() {
        raw.Fail(2, "FaultContext.SetReg: register out of range")
    }
    self.regs[n] = v
}

// The filter list: FilterMax slots, the first ready of them written.
const FilterMax: int = 16

struct FilterList {
    claimed: u64
    ready: u64
    hooked: u64
    f: [16]fn(&FaultContext): FaultAction
}

NoFilter(c: &FaultContext): FaultAction {
    return FaultAction.Pass
}

var FilterSet: FilterList = FilterList(claimed: 0, ready: 0, hooked: 0, f: [NoFilter; 16])

// FWord is one of the list's counters, as an atomic.
FWord(p: *u64): &raw.Atomic[u64] {
    unsafe "FilterSet is static; a u64 has raw.Atomic[u64]'s layout, and every access to the counters is atomic" {
        return &*raw.PtrCast[u64, raw.Atomic[u64]](p)
    }
}

/// Adds f after the filters already added. Filters run on every later
/// fault that reaches them, in the order added, before Mk's own report,
/// until one returns Resume. A filter runs inside a fault: it must not
/// take a lock, allocate or suspend. A fault inside a filter ends the
/// process with `panic: fault in a fault filter`, status 101.
///
/// Which faults reach the filters differs by target. On Windows: a stack
/// overflow, an access or illegal-instruction fault in Mk code, any fault
/// whose pc is in a std.vm code region, and any other access or
/// illegal-instruction fault whose thread is one std started (a carrier, a
/// blocking-pool thread) or whose stack is a task's or a carrier's (in a
/// program with no task, every one), whatever its pc: C code's, a system
/// DLL's. The filters see such a fault before the C code's own handler
/// (`__try`), and one they all pass goes on to it, or to the system, as
/// it would without filters. On Linux: a
/// SIGSEGV, SIGBUS or SIGILL that Mk owns, which is one whose pc is in
/// the executable's image, whose thread is the main thread or one std
/// started, or whose stack is a task's or a carrier's; and any fault whose
/// pc is in a std.vm code region. One every filter passes is Mk's report.
///
/// macOS routes as Linux does.
///
/// On Linux, a fault in C code outside the image on a thread std did not
/// make gets the signal's default action, raised again at once, as on
/// Windows it gets the system's (in a program with no task, after the
/// filters). So does a SIGSEGV, SIGBUS or SIGILL that
/// another process or thread sends (kill, tgkill): it is not a fault and
/// is never reported as Mk's. C code that installs a signal handler of its
/// own replaces std's.
///
/// Callable only in unsafe (E1101). Panics with code 8 past 16 filters.
/// O(1) besides waiting for an earlier addition to publish; allocates
/// nothing.
export AddFaultFilter(f: fn(&FaultContext): FaultAction): FilterId {
    // Edge: the claim needs no edge; the slot is this caller's alone.
    i := FWord(raw.Of(&FilterSet.claimed)).Add(1)
    if i >= u64(FilterMax) {
        raw.Fail(8, "runtime.AddFaultFilter: 16 filters are added")
    }
    unsafe "slot i is this caller's alone (the claim), and no handler calls it before ready passes it" {
        raw.Write(raw.Offset(raw.PtrCast[[16]fn(&FaultContext): FaultAction, fn(&FaultContext): FaultAction](raw.Of(&FilterSet.f)), Trunc[int](i)), f)
    }
    // Edge: release after the slot's write; an earlier claimant publishes
    // first, so ready only grows over written slots.
    for FWord(raw.Of(&FilterSet.ready)).LoadOrd(1) != i {
        sys.ThreadYield()
    }
    FWord(raw.Of(&FilterSet.ready)).StoreOrd(i + 1, 2)
    // Edge: seq_cst; the Cas admits one installer (hooked 0 to 1), whose
    // Store of 2 comes after FaultFilterHook installed the hook; every
    // other caller waits for 2, so no AddFaultFilter returns before its
    // filter can run (three states). The hook runs
    // only filters below ready, so installing it after the first publish
    // is enough.
    h := FWord(raw.Of(&FilterSet.hooked))
    if h.Load() != 2 {
        if h.Cas(0, 1) {
            sys.FaultFilterHook(RunFilters)
            h.Store(2)
        } else {
            for h.Load() != 2 {
                sys.ThreadYield()
            }
        }
    }
    return FilterId(n: Trunc[int](i))
}

// ContextReg is the offset of encoding register n in the OS context std.sys
// hands the hook, and ContextPc that of the program counter:
// - Windows' CONTEXT: Rax at 0x78 to R15 at 0xF0 in encoding order, Rip at
//   0xF8.
// - Linux x86_64's ucontext_t: uc_mcontext.gregs at 40, in the kernel's REG_
//   order (R8..R15 0 to 7, RDI 8, RSI 9, RBP 10, RBX 11, RDX 12, RAX 13,
//   RCX 14, RSP 15, RIP 16).
// - Linux aarch64's ucontext_t: uc_mcontext (struct sigcontext) at 176,
//   after the 128-byte signal mask, 16-aligned: fault_address, then
//   regs[31] (x0 to x30) at 184, sp at 432, pc at 440.
// - macOS: the thread state uc_mcontext points to (std.sys passes it, not
//   the ucontext), after the 16-byte exception state: on x86_64 rax, rbx,
//   rcx, rdx, rdi, rsi, rbp, rsp, r8 to r15, then rip (MacGreg maps the
//   encoding order); on arm64 x0 to x28, fp (x29) and lr (x30) in order
//   from 16, sp at 264, pc at 272.
const LinuxGreg: [16]int = [13, 14, 12, 11, 15, 10, 9, 8, 0, 1, 2, 3, 4, 5, 6, 7]
const MacGreg: [16]int = [0, 2, 3, 1, 7, 6, 5, 4, 8, 9, 10, 11, 12, 13, 14, 15]

ContextReg(n: int): int {
    if Target.arch == Arch.Arm64 {
        if Target.os == Os.Macos {
            return if n == 31 { 264 } else { 16 + n * 8 }
        }
        return if n == 31 { 432 } else { 184 + n * 8 }
    }
    if Target.os == Os.Macos {
        return 16 + MacGreg[n] * 8
    }
    return if Target.os == Os.Windows { 0x78 + n * 8 } else { 40 + LinuxGreg[n] * 8 }
}

ContextPc(): int {
    if Target.arch == Arch.Arm64 {
        return if Target.os == Os.Macos { 272 } else { 440 }
    }
    if Target.os == Os.Macos {
        return 16 + 16 * 8
    }
    return if Target.os == Os.Windows { 0xF8 } else { 40 + 16 * 8 }
}

// KindOf is a fault's kind from the handler's code: 1 Stack, 3 Illegal,
// else Access, except that an access to a guard page std.rt knows (a task
// slot's lowest page, or on Linux the page below a carrier's system stack;
// rt.IsGuardPage, which std.rt's own report uses) is Stack: std.sys does
// not know those regions.
KindOf(kind: u32, addr: uint): FaultKind {
    if kind == 1 {
        return FaultKind.Stack
    }
    if kind == 3 {
        return FaultKind.Illegal
    }
    return if rt.IsGuardPage(addr) { FaultKind.Stack } else { FaultKind.Access }
}

// RunFilters is the hook std.sys calls on a fault: it copies the OS
// context's registers into a FaultContext, runs the filters in order, and
// on a Resume writes the edited registers back and answers true. The
// faulting thread is marked while its filters run (MarkFilters): a fault
// inside a filter reaches this hook again on the same thread, which skips
// the filters and reports "fault in a fault filter", exit 101, so a filter
// that faults never runs again on its own fault (on Linux, SA_NODEFER would
// otherwise re-enter the handler until the alternate stack overflows).
RunFilters(os: *u8, kind: u32, addr: uint, pc: uint): bool {
    mark := MarkEnter(MarkFilters, sys.ThreadId())
    if mark < 0 {
        rt.Reporting(raw.Null[rt.Site](), "fault in a fault filter", "", "")
    }
    r := RunFiltersIn(os, kind, addr, pc)
    MarkLeave(mark)
    return r
}

// RunFiltersIn is RunFilters inside the thread's mark.
RunFiltersIn(os: *u8, kind: u32, addr: uint, pc: uint): bool {
    var c := FaultContext(k: KindOf(kind, addr), at: addr, regs: [0; 32], ip: pc)
    unsafe "os is the faulting thread's OS context, live while the handler runs" {
        for n in 0..RegCount() {
            c.regs[n] = raw.Read(raw.PtrCast[u8, u64](raw.Offset(os, ContextReg(n))))
        }
    }
    // Edge: acquire, pairing with AddFaultFilter's release of ready.
    k := FWord(raw.Of(&FilterSet.ready)).LoadOrd(1)
    for i in 0..Trunc[int](k) {
        f := FilterSet.f[i]
        match f(&c) {
            .Resume(ctx) {
                unsafe "os is the faulting thread's OS context; the handler installs it when it resumes" {
                    for n in 0..RegCount() {
                        raw.Write(raw.PtrCast[u8, u64](raw.Offset(os, ContextReg(n))), ctx.regs[n])
                    }
                    raw.Write(raw.PtrCast[u8, u64](raw.Offset(os, ContextPc())), u64(ctx.ip))
                }
                return true
            }
            .Pass {}
        }
    }
    return false
}
