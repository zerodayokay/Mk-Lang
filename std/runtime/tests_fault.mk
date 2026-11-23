// Why: fault filters run inside a hardware fault; every target's path must
// be tested for real.
// What: test blocks of runtime.AddFaultFilter, run by
// `mk test --std=std.runtime -run "Filter "`.
// How: each block runs in a child process of its own, so its filters see
// only its own faults. The blocks fault on protected pages, guard pages,
// code regions and illegal instructions, and check Pass, Resume and Mk's
// report. TestRt_FilterInGuarantee (std/std-rt_fault_test.go) checks a
// filter running inside a task's stack overflow.

import std.raw
import std.rt
import std.sys
import std.vm

// The page the tests below fault on: its address and its Region's base.
var FaultPage: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var FaultSeen: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var FaultKindSeen: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// Unprotect is a filter that, on an access to FaultPage, makes the page
// read-write again and resumes the thread at the same instruction, which
// then succeeds: a guard page a program manages.
Unprotect(c: &FaultContext): FaultAction {
    unsafe "the globals are this test process's; the filter runs on the faulting thread" {
        page := FaultPage.Load()
        ps := PageSizeOf()
        if c.Addr() < Trunc[uint](page) || c.Addr() >= Trunc[uint](page) + Cast[uint](ps) {
            return FaultAction.Pass
        }
        FaultSeen.Store(FaultSeen.Load() + 1)
        k: u64 = match c.Kind() {
            .Stack { 1 }
            .Access { 2 }
            .Illegal { 3 }
        }
        FaultKindSeen.Store(k)
        _ = sys.VmProtect(raw.FromAddr[u8](Trunc[uint](page)), ps, 2)
    }
    return FaultAction.Resume(ctx: *c)
}

test "Filter a read of a Protect(None) page faults, the filter sees it, and Resume retries the read" {
    ps := PageSizeOf()
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        r := vm.Reserve(2 * ps, ps) else {
            Assert(false)
            return
        }
        r.Commit(0, 2 * ps) else {
            Assert(false)
        }
        raw.Write(raw.FromAddr[u64](r.Base() + Trunc[uint](ps)), 77)
        r.Protect(ps, ps, vm.Prot.None) else {
            Assert(false)
        }
        FaultPage.Store(u64(r.Base()) + Cast[u64](ps))
        _ = AddFaultFilter(Unprotect)
        v := raw.Read(raw.FromAddr[u64](r.Base() + Trunc[uint](ps)))
        Assert(v == 77)
        Assert(FaultSeen.Load() == 1)
        Assert(FaultKindSeen.Load() == 2)
        r.Release()
    }
}

test "Filter a write to a guard page resumes after the filter commits it" {
    ps := PageSizeOf()
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        r := vm.Reserve(ps, ps) else {
            Assert(false)
            return
        }
        r.Commit(0, ps) else {
            Assert(false)
        }
        r.Protect(0, ps, vm.Prot.None) else {
            Assert(false)
        }
        FaultPage.Store(u64(r.Base()))
        _ = AddFaultFilter(Unprotect)
        p := raw.FromAddr[u64](r.Base() + 64)
        raw.Write(p, 5)
        raw.Write(p, raw.Read(p) + 1)
        Assert(raw.Read(p) == 6)
        Assert(FaultSeen.Load() == 1)
        r.Release()
    }
}

// PassAll is a filter that passes every fault on.
PassAll(c: &FaultContext): FaultAction {
    unsafe "the counter is this test process's" {
        FaultSeen.Store(FaultSeen.Load() + 1)
    }
    return FaultAction.Pass
}

test "Filter one that passes leaves Mk's report unchanged [exit 101: panic: invalid memory access at]" {
    ps := PageSizeOf()
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        _ = AddFaultFilter(PassAll)
        r := vm.Reserve(ps, ps) else {
            return
        }
        r.Commit(0, ps) else {}
        r.Protect(0, ps, vm.Prot.None) else {}
        FaultKindSeen.Store(ProtectedRead(r.Base()))
        r.Release()
    }
}

// The x86_64 code `mov rax, [0x10]` (8 bytes, an access fault at a code
// region's pc), then `mov eax, 42; ret`.
const LoadNull: []u8 = [0x48, 0x8B, 0x04, 0x25, 0x10, 0x00, 0x00, 0x00, 0xB8, 0x2A, 0x00, 0x00, 0x00, 0xC3]

// The aarch64 code that loads from 0x10 (the load at byte 4, an access
// fault at a code region's pc), then returns 42.
const LoadNullArm64: []u8 = [
    0x00, 0x02, 0x80, 0xD2, // movz x0, #0x10
    0x00, 0x00, 0x40, 0xF9, // ldr x0, [x0]
    0x40, 0x05, 0x80, 0x52, // movz w0, #42
    0xC0, 0x03, 0x5F, 0xD6  // ret
]

// LoadCode is the target's LoadNull.
LoadCode(): []u8 {
    return if Target.arch == Arch.Arm64 { LoadNullArm64 } else { LoadNull }
}

// LoadAt is the offset of LoadCode's faulting load, LoadLen its length.
LoadAt(): uint {
    return if Target.arch == Arch.Arm64 { 4 } else { 0 }
}

LoadLen(): uint {
    return if Target.arch == Arch.Arm64 { 4 } else { 8 }
}

// PutCode writes code at offset o of c through an Edit window and flushes it.
PutCode(c: mut vm.CodeRegion, o: int, code: []u8): bool {
    unsafe "vm's functions are unsafe only" {
        var w := c.Edit() else {
            return false
        }
        defer w.Done()
        var b := w.Bytes()
        for i in 0..code.Len() {
            b[o + i] = code[i]
        }
        w.Flush(o, code.Len())
        return true
    }
}

var CodeLo: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// SkipLoad is a filter that, on a fault at the load of the code region's
// LoadCode, resumes past it.
SkipLoad(c: &FaultContext): FaultAction {
    unsafe "the global is this test process's" {
        lo := Trunc[uint](CodeLo.Load())
        if c.Pc() != lo + LoadAt() {
            return FaultAction.Pass
        }
    }
    var d := *c
    d.SetPc(c.Pc() + LoadLen())
    return FaultAction.Resume(ctx: d)
}

// CallCode calls the code at addr, which returns a u32 in eax or w0.
CallCode(addr: uint): u32 {
    var r: u32 = 0
    if Target.arch == Arch.X64 {
        unsafe "addr is a code region's entry holding this file's code, which touches no Mk memory" {
            asm("sub rsp, 128\ncall {a}\nadd rsp, 128", a: in(reg) addr, out("eax") r, clobber_abi("C"))
        }
    }
    if Target.arch == Arch.Arm64 {
        unsafe "addr is a code region's entry holding this file's code, which touches no Mk memory" {
            // blr writes only x30, and AAPCS64 has no red zone below sp.
            asm("blr {a}", a: in(reg) addr, out("w0") r, clobber_abi("C"))
        }
    }
    return r
}

test "Filter a fault in a code region goes to the filters, and Resume at an edited pc continues there" {
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        var c := vm.CodeRegion.New(64) else {
            Assert(false)
            return
        }
        defer c.Release()
        Assert(PutCode(mut c, 0, LoadCode()))
        CodeLo.Store(u64(c.Entry(0)))
        _ = AddFaultFilter(SkipLoad)
        Assert(CallCode(c.Entry(0)) == 42)
    }
}

test "Filter FaultContext's setters edit the copy a filter returns" {
    var c := FaultContext(k: FaultKind.Access, at: 16, regs: [0; 32], ip: 100)
    c.SetPc(200)
    c.SetSp(4096)
    c.SetReg(0, 9)
    Assert(c.Pc() == 200 && c.Sp() == 4096 && c.Reg(0) == 9 && c.Reg(SpReg()) == 4096 && c.Addr() == 16)
}

PageSizeOf(): int {
    unsafe "vm.PageSize is unsafe only" {
        return vm.PageSize()
    }
}

// Code regions made and released by many
// tasks at once, each faulting once on a load a filter skips. On bd81ffa2 a
// slot could pair one adder's lo with another's hi, so a region's fault was
// not routed and the process died (0xC0000005).
var RaceBad: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// SkipAnyLoad resumes past a fault at LoadCode's load, in whichever code
// region it is: one whose pc holds the load's first bytes (x86_64's
// opcode 48 8b, aarch64's whole instruction).
SkipAnyLoad(c: &FaultContext): FaultAction {
    code := LoadCode()
    at := Trunc[int](LoadAt())
    n := if Target.arch == Arch.Arm64 { 4 } else { 2 }
    for i in 0..n {
        unsafe "pc is in a code region's executable view, which is readable" {
            if raw.Read(raw.FromAddr[u8](c.Pc() + Cast[uint](i))) != code[at + i] {
                return FaultAction.Pass
            }
        }
    }
    var d := *c
    d.SetPc(c.Pc() + LoadLen())
    return FaultAction.Resume(ctx: d)
}

RaceWorker(j: int, n: int) {
    size := (j % 4 + 1) * 16 * 4096
    for _ in 0..n {
        unsafe "vm's functions are unsafe only; the code is LoadCode's" {
            var c := vm.CodeRegion.New(size) else {
                _ = RaceBad.Add(1000000)
                return
            }
            off := c.Size() - 4096
            if !PutCode(mut c, off, LoadCode()) || CallCode(c.Entry(off)) != 42 {
                _ = RaceBad.Add(1)
            }
            c.Release()
        }
    }
}

test "Filter code regions made and released by 16 tasks at once each route their fault [env MK_CARRIERS=16]" (sys: &System) {
    unsafe "AddFaultFilter is unsafe only" {
        _ = AddFaultFilter(SkipAnyLoad)
    }
    scope s := sys.io.Scope() {
        for j in 0..16 {
            s.go RaceWorker(j, 1000)
        }
    }
    Assert(RaceBad.Load() == 0)
}

// A fault in a code region that
// no filter resumes is reported as such, exit 101, on every target. On
// bd81ffa2, Windows died with 0xC0000005 and no text, Linux exited 139.
CodeFault(filter: bool) {
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        if filter {
            _ = AddFaultFilter(PassAll)
        }
        var c := vm.CodeRegion.New(64) else {
            return
        }
        defer c.Release()
        _ = PutCode(mut c, 0, LoadCode())
        FaultKindSeen.Store(u64(CallCode(c.Entry(0))))
    }
}

test "Filter an unresumed fault in a code region is reported as one [exit 101: fault in a code region]" {
    CodeFault(false)
}

test "Filter a fault in a code region that every filter passes is reported as one [exit 101: fault in a code region]" {
    CodeFault(true)
}

// The x86_64 code `call` itself (e8 fb ff ff ff): a recursion with no end.
const CallSelf: []u8 = [0xE8, 0xFB, 0xFF, 0xFF, 0xFF]

// The aarch64 recursion with no end: each call pushes a frame record, as
// a function's prologue does, and calls its own start again.
const CallSelfArm64: []u8 = [
    0xFD, 0x7B, 0xBF, 0xA9, // 1: stp x29, x30, [sp, #-16]!
    0xFF, 0xFF, 0xFF, 0x97  // bl 1b
]

OverflowInRegion() {
    unsafe "vm's functions are unsafe only; the code is CallSelf's" {
        var c := vm.CodeRegion.New(4096) else {
            return
        }
        defer c.Release()
        _ = PutCode(mut c, 0, if Target.arch == Arch.Arm64 { CallSelfArm64 } else { CallSelf })
        FaultKindSeen.Store(u64(CallCode(c.Entry(0))))
    }
}

// A task's stack overflow whose pc is
// in a code region is a stack overflow, whatever its pc. On bd81ffa2 Linux
// reported a fault in a code region and exited 139.
test "Filter a task's stack overflow in a code region is reported as a stack overflow [exit 101: panic: stack limit in task]" (sys: &System) {
    unsafe "AddFaultFilter is unsafe only" {
        _ = AddFaultFilter(PassAll)
    }
    scope s := sys.io.Scope() {
        s.go OverflowInRegion()
    }
}

// A filter that faults (it reads
// FaultPage, a second Protect(None) page). Its own fault skips the filters
// and is reported as a fault in a fault filter, exit 101. On bd81ffa2
// Windows died with 0xC0000005 and no text; Linux re-entered the handler
// under SA_NODEFER (dev hung, release dumped core).
FaultingFilter(c: &FaultContext): FaultAction {
    unsafe "a read of FaultPage, which is Protect(None): it faults, the test's point" {
        if raw.Read(raw.FromAddr[u64](Trunc[uint](FaultPage.Load()))) == 5 {
            return FaultAction.Resume(ctx: *c)
        }
    }
    return FaultAction.Pass
}

test "Filter a fault inside a filter is reported as one, not filtered again [exit 101: fault in a fault filter]" {
    ps := PageSizeOf()
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        r := vm.Reserve(2 * ps, ps) else {
            return
        }
        r.Commit(0, 2 * ps) else {}
        r.Protect(0, 2 * ps, vm.Prot.None) else {}
        FaultPage.Store(u64(r.Base()) + Cast[u64](ps))
        _ = AddFaultFilter(FaultingFilter)
        FaultKindSeen.Store(ProtectedRead(r.Base()))
        r.Release()
    }
}

// ProtectedRead reads the word at addr with an atomic load, which the
// optimizer keeps. A plain raw.Read whose value went only into a global no
// code of a one-block release build reads was deleted as dead (the block
// exited 0: no fault); the same read whose value is tested faults (the
// fix round).
ProtectedRead(addr: uint): u64 {
    unsafe "addr is a committed page of the caller's region, 8-aligned" {
        return (*raw.FromAddr[raw.Atomic[u64]](addr)).Load()
    }
}

// CodeRegion.New refuses a
// region the fault path's table cannot hold, rather than make one whose
// fault would never reach the filters. HoldThen keeps n regions live down
// a recursion, then makes one more and runs LoadCode in it: 42 when the
// filter skipped its fault, 5 when New refused it with Unsupported. On
// bd81ffa2 the 65th region was made unrecorded and its fault killed the
// process (0xC0000005 on Windows).
HoldThen(n: int): int {
    unsafe "vm's functions are unsafe only; the code is LoadCode's" {
        if n == 0 {
            var c := vm.CodeRegion.New(4096) else {
                return 5
            }
            defer c.Release()
            if !PutCode(mut c, 0, LoadCode()) {
                return 4
            }
            return int(CallCode(c.Entry(0)))
        }
        h := vm.CodeRegion.New(4096) else {
            return 3
        }
        r := HoldThen(n - 1)
        h.Release()
        return r
    }
}

test "Filter a 64th live code region routes its fault, and a 65th is refused" {
    unsafe "AddFaultFilter is unsafe only" {
        _ = AddFaultFilter(SkipAnyLoad)
    }
    Assert(HoldThen(63) == 42)
    Assert(HoldThen(64) == 5)
    Assert(HoldThen(63) == 42)
}

// AddFaultFilter returns only once its filter can
// run, though another caller won the hook's installation. Eight tasks add
// a filter at once and each faults at once on its own Protect(None) page
// of one region, which the filter (any of the eight) makes readable again.
var RacePages: raw.Atomic[u64] = raw.Atomic[u64].New(0)

UnprotectRace(c: &FaultContext): FaultAction {
    unsafe "the region is this test's, 8 pages from RacePages" {
        lo := Trunc[uint](RacePages.Load())
        ps := Cast[uint](PageSizeOf())
        if c.Addr() < lo || c.Addr() >= lo + 8 * ps {
            return FaultAction.Pass
        }
        _ = sys.VmProtect(raw.FromAddr[u8](c.Addr() & ~(ps - 1)), Cast[int](ps), 2)
    }
    return FaultAction.Resume(ctx: *c)
}

AddThenFault(k: int, got: &raw.Atomic[u64]) {
    unsafe "AddFaultFilter is unsafe only; the page is the test's" {
        _ = AddFaultFilter(UnprotectRace)
        _ = got.Add(ProtectedRead(Trunc[uint](RacePages.Load()) + Cast[uint](k * PageSizeOf())) + 1)
    }
}

test "Filter eight tasks that add a filter at once each see their fault filtered" (sys: &System) {
    ps := PageSizeOf()
    got := raw.Atomic[u64].New(0)
    unsafe "vm's functions are unsafe only" {
        r := vm.Reserve(8 * ps, ps) else {
            Assert(false)
            return
        }
        r.Commit(0, 8 * ps) else {
            Assert(false)
        }
        r.Protect(0, 8 * ps, vm.Prot.None) else {
            Assert(false)
        }
        RacePages.Store(u64(r.Base()))
        scope s := sys.io.Scope() {
            for k in 0..8 {
                s.go AddThenFault(k, &got)
            }
        }
        r.Release()
    }
    Assert(got.Load() == 8)
}

// A fault's kind comes from std.rt's guard pages
// (rt.IsGuardPage, which OnFault's report uses too): a task slot's lowest
// page is Stack, any other address Access; std.sys's kinds 1 and 3 are
// Stack and Illegal. (A carrier's guard page is Linux's, linux.sh.) The
// guard is a page of at least 4 KiB: 16 KiB on arm64 macOS.
KindTask(ok: &raw.Atomic[u64]) {
    var mark: u64 = 0
    a := raw.Addr(raw.Of(&mark))
    base := rt.SlotBase(rt.SlotTop(a))
    ps := PageSizeOf()
    guard := Cast[uint](if ps > 4096 { ps } else { 4096 })
    if base != 0 && KindOf(2, base + 16) == FaultKind.Stack && KindOf(2, base + 2 * guard) == FaultKind.Access && KindOf(2, base + guard - 1) == FaultKind.Stack {
        ok.Store(1)
    }
}

test "Filter KindOf: a task slot's guard page is Stack, other addresses Access" (sys: &System) {
    ok := raw.Atomic[u64].New(0)
    scope s := sys.io.Scope() {
        s.go KindTask(&ok)
    }
    Assert(ok.Load() == 1)
    Assert(KindOf(1, 0) == FaultKind.Stack && KindOf(3, 0) == FaultKind.Illegal && KindOf(2, 16) == FaultKind.Access)
}

// FaultContext numbers the registers as the target encodes them, on the
// OS context of each target. RegCode saves the callee-saved registers,
// sets register n to 0x1000 + n (all but rsp), faults at RegLoadAt on
// `mov rax, [0x10]`, then restores them and returns eax. RegCheck checks
// every register, the pc and the stack pointer, and resumes past the load
// with rax = 2000 + a mask of the mismatches. RegCodeArm64 is the same on
// aarch64 for x0 to x30 but x18, the platform register, with the fault a
// load from x0 (0x1000) and `add x0, x0, x28` after it, so the return is
// 2000 only when RegCheck's SetReg of x28 to 0 took effect too.
const RegCode: []u8 = [0x53, 0x55, 0x56, 0x57, 0x41, 0x54, 0x41, 0x55, 0x41, 0x56, 0x41, 0x57, 0x48, 0xC7, 0xC0, 0x00,
    0x10, 0x00, 0x00, 0x48, 0xC7, 0xC1, 0x01, 0x10, 0x00, 0x00, 0x48, 0xC7, 0xC2, 0x02, 0x10, 0x00,
    0x00, 0x48, 0xC7, 0xC3, 0x03, 0x10, 0x00, 0x00, 0x48, 0xC7, 0xC5, 0x05, 0x10, 0x00, 0x00, 0x48,
    0xC7, 0xC6, 0x06, 0x10, 0x00, 0x00, 0x48, 0xC7, 0xC7, 0x07, 0x10, 0x00, 0x00, 0x49, 0xC7, 0xC0,
    0x08, 0x10, 0x00, 0x00, 0x49, 0xC7, 0xC1, 0x09, 0x10, 0x00, 0x00, 0x49, 0xC7, 0xC2, 0x0A, 0x10,
    0x00, 0x00, 0x49, 0xC7, 0xC3, 0x0B, 0x10, 0x00, 0x00, 0x49, 0xC7, 0xC4, 0x0C, 0x10, 0x00, 0x00,
    0x49, 0xC7, 0xC5, 0x0D, 0x10, 0x00, 0x00, 0x49, 0xC7, 0xC6, 0x0E, 0x10, 0x00, 0x00, 0x49, 0xC7,
    0xC7, 0x0F, 0x10, 0x00, 0x00, 0x48, 0x8B, 0x04, 0x25, 0x10, 0x00, 0x00, 0x00, 0x41, 0x5F, 0x41,
    0x5E, 0x41, 0x5D, 0x41, 0x5C, 0x5F, 0x5E, 0x5D, 0x5B, 0xC3]
const RegLoadAt: int = 117

const RegCodeArm64: []u8 = [
    0xFF, 0x83, 0x01, 0xD1, //   0: sub sp, sp, #96
    0xF3, 0x53, 0x00, 0xA9, //   4: stp x19, x20, [sp]
    0xF5, 0x5B, 0x01, 0xA9, //   8: stp x21, x22, [sp, #16]
    0xF7, 0x63, 0x02, 0xA9, //  12: stp x23, x24, [sp, #32]
    0xF9, 0x6B, 0x03, 0xA9, //  16: stp x25, x26, [sp, #48]
    0xFB, 0x73, 0x04, 0xA9, //  20: stp x27, x28, [sp, #64]
    0xFD, 0x7B, 0x05, 0xA9, //  24: stp x29, x30, [sp, #80]
    0x00, 0x00, 0x82, 0xD2, //  28: movz x0, #0x1000
    0x21, 0x00, 0x82, 0xD2, //  32: movz x1, #0x1001
    0x42, 0x00, 0x82, 0xD2, //  36: movz x2, #0x1002
    0x63, 0x00, 0x82, 0xD2, //  40: movz x3, #0x1003
    0x84, 0x00, 0x82, 0xD2, //  44: movz x4, #0x1004
    0xA5, 0x00, 0x82, 0xD2, //  48: movz x5, #0x1005
    0xC6, 0x00, 0x82, 0xD2, //  52: movz x6, #0x1006
    0xE7, 0x00, 0x82, 0xD2, //  56: movz x7, #0x1007
    0x08, 0x01, 0x82, 0xD2, //  60: movz x8, #0x1008
    0x29, 0x01, 0x82, 0xD2, //  64: movz x9, #0x1009
    0x4A, 0x01, 0x82, 0xD2, //  68: movz x10, #0x100a
    0x6B, 0x01, 0x82, 0xD2, //  72: movz x11, #0x100b
    0x8C, 0x01, 0x82, 0xD2, //  76: movz x12, #0x100c
    0xAD, 0x01, 0x82, 0xD2, //  80: movz x13, #0x100d
    0xCE, 0x01, 0x82, 0xD2, //  84: movz x14, #0x100e
    0xEF, 0x01, 0x82, 0xD2, //  88: movz x15, #0x100f
    0x10, 0x02, 0x82, 0xD2, //  92: movz x16, #0x1010
    0x31, 0x02, 0x82, 0xD2, //  96: movz x17, #0x1011
    0x73, 0x02, 0x82, 0xD2, // 100: movz x19, #0x1013
    0x94, 0x02, 0x82, 0xD2, // 104: movz x20, #0x1014
    0xB5, 0x02, 0x82, 0xD2, // 108: movz x21, #0x1015
    0xD6, 0x02, 0x82, 0xD2, // 112: movz x22, #0x1016
    0xF7, 0x02, 0x82, 0xD2, // 116: movz x23, #0x1017
    0x18, 0x03, 0x82, 0xD2, // 120: movz x24, #0x1018
    0x39, 0x03, 0x82, 0xD2, // 124: movz x25, #0x1019
    0x5A, 0x03, 0x82, 0xD2, // 128: movz x26, #0x101a
    0x7B, 0x03, 0x82, 0xD2, // 132: movz x27, #0x101b
    0x9C, 0x03, 0x82, 0xD2, // 136: movz x28, #0x101c
    0xBD, 0x03, 0x82, 0xD2, // 140: movz x29, #0x101d
    0xDE, 0x03, 0x82, 0xD2, // 144: movz x30, #0x101e
    0x00, 0x00, 0x40, 0xF9, // 148: ldr x0, [x0]
    0x00, 0x00, 0x1C, 0x8B, // 152: add x0, x0, x28
    0xF3, 0x53, 0x40, 0xA9, // 156: ldp x19, x20, [sp]
    0xF5, 0x5B, 0x41, 0xA9, // 160: ldp x21, x22, [sp, #16]
    0xF7, 0x63, 0x42, 0xA9, // 164: ldp x23, x24, [sp, #32]
    0xF9, 0x6B, 0x43, 0xA9, // 168: ldp x25, x26, [sp, #48]
    0xFB, 0x73, 0x44, 0xA9, // 172: ldp x27, x28, [sp, #64]
    0xFD, 0x7B, 0x45, 0xA9, // 176: ldp x29, x30, [sp, #80]
    0xFF, 0x83, 0x01, 0x91, // 180: add sp, sp, #96
    0xC0, 0x03, 0x5F, 0xD6  // 184: ret
]
const RegLoadAtArm64: int = 148

var RegLo: raw.Atomic[u64] = raw.Atomic[u64].New(0)

RegCheck(c: &FaultContext): FaultAction {
    var bad: u64 = 0
    arm := Target.arch == Arch.Arm64
    at := if arm { RegLoadAtArm64 } else { RegLoadAt }
    unsafe "the global is this test process's" {
        if c.Pc() != Trunc[uint](RegLo.Load()) + Cast[uint](at) {
            return FaultAction.Pass
        }
    }
    sp := SpReg()
    for n in 0..RegCount() {
        if n != sp && !(arm && n == 18) && c.Reg(n) != 0x1000 + Cast[u64](n) {
            bad |= 1 << Cast[u64](n)
        }
    }
    align: uint = if arm { 16 } else { 8 }
    if c.Sp() == 0 || c.Sp() % align != 0 || c.Reg(sp) != u64(c.Sp()) {
        bad |= 1 << Cast[u64](sp)
    }
    if c.Kind() != FaultKind.Access {
        bad |= 1 << Cast[u64](RegCount())
    }
    var d := *c
    d.SetReg(0, 2000 + bad)
    if arm {
        d.SetReg(28, 0)
    }
    d.SetPc(c.Pc() + LoadLen())
    return FaultAction.Resume(ctx: d)
}

test "Filter FaultContext reads every register in the target's encoding order, and SetReg and SetPc take effect" {
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        var c := vm.CodeRegion.New(4096) else {
            Assert(false)
            return
        }
        defer c.Release()
        Assert(PutCode(mut c, 0, if Target.arch == Arch.Arm64 { RegCodeArm64 } else { RegCode }))
        RegLo.Store(u64(c.Entry(0)))
        _ = AddFaultFilter(RegCheck)
        Assert(CallCode(c.Entry(0)) == 2000)
    }
}

// The stack queries agree with the guard page's address. On a task, a
// write to the byte just below StackBounds' lo less the guarantee (the
// guard page's top byte) faults as FaultKind.Stack at that address; a
// filter resumes past the store: x86_64's 3-byte `mov byte ptr [rax], 1`,
// aarch64's 4-byte `strb wzr, [x]`.
var GuardAt: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var GuardSeen: raw.Atomic[u64] = raw.Atomic[u64].New(0)

GuardFilter(c: &FaultContext): FaultAction {
    unsafe "the globals are this test process's" {
        if c.Addr() != Trunc[uint](GuardAt.Load()) {
            return FaultAction.Pass
        }
        GuardSeen.Store(if c.Kind() == FaultKind.Stack { 1 } else { 2 })
    }
    step: uint = if Target.arch == Arch.Arm64 { 4 } else { 3 }
    var d := *c
    d.SetPc(c.Pc() + step)
    return FaultAction.Resume(ctx: d)
}

GuardTask(ok: &raw.Atomic[u64]) {
    b := StackBounds()
    var g := b.lo - 1
    if Target.os == Os.Windows {
        g = b.lo - Trunc[uint](16 << 10) - 1
    }
    if !rt.IsGuardPage(g) || rt.IsGuardPage(g + 1) {
        return
    }
    GuardAt.Store(u64(g))
    if Target.arch == Arch.X64 {
        unsafe "g is the top byte of this task's guard page: the write faults and the filter skips it" {
            asm("mov byte ptr [rax], 1", in("rax") g)
        }
    }
    if Target.arch == Arch.Arm64 {
        unsafe "g is the top byte of this task's guard page: the write faults and the filter skips it" {
            asm("strb wzr, [{g}]", g: in(reg) g)
        }
    }
    ok.Store(GuardSeen.Load())
}

test "Filter StackBounds agrees with the guard page: a write just below it faults as Stack" (sys: &System) {
    unsafe "AddFaultFilter is unsafe only" {
        _ = AddFaultFilter(GuardFilter)
    }
    ok := raw.Atomic[u64].New(0)
    scope s := sys.io.Scope() {
        s.go GuardTask(&ok)
    }
    Assert(ok.Load() == 1)
}

// An illegal instruction, `ud2` (0f 0b), then
// `mov eax, 42; ret`. Linux takes SIGILL with SIGSEGV's routing, so it
// reaches the filters as FaultKind.Illegal on both targets.
const Ud2: []u8 = [0x0F, 0x0B, 0xB8, 0x2A, 0x00, 0x00, 0x00, 0xC3]

// aarch64's: the permanently undefined `udf #0`, then a return of 42.
const UdfArm64: []u8 = [
    0x00, 0x00, 0x00, 0x00, // udf #0
    0x40, 0x05, 0x80, 0x52, // movz w0, #42
    0xC0, 0x03, 0x5F, 0xD6  // ret
]

// SkipUd2 resumes past an Illegal fault at a code region's ud2 or udf (2
// or 4 bytes), and records the kind it saw.
SkipUd2(c: &FaultContext): FaultAction {
    unsafe "the global is this test process's" {
        if c.Pc() != Trunc[uint](CodeLo.Load()) {
            return FaultAction.Pass
        }
    }
    k: u64 = match c.Kind() {
        .Stack { 1 }
        .Access { 2 }
        .Illegal { 3 }
    }
    FaultKindSeen.Store(k)
    step: uint = if Target.arch == Arch.Arm64 { 4 } else { 2 }
    var d := *c
    d.SetPc(c.Pc() + step)
    return FaultAction.Resume(ctx: d)
}

// Ud2Region writes the target's Ud2 into a new code region and calls it.
Ud2Region(filter: bool): u32 {
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        var c := vm.CodeRegion.New(64) else {
            Assert(false)
            return 0
        }
        defer c.Release()
        Assert(PutCode(mut c, 0, if Target.arch == Arch.Arm64 { UdfArm64 } else { Ud2 }))
        CodeLo.Store(u64(c.Entry(0)))
        if filter {
            _ = AddFaultFilter(SkipUd2)
        }
        return CallCode(c.Entry(0))
    }
}

test "Filter an illegal instruction in a code region reaches the filters as Illegal, and Resume past it continues" {
    Assert(Ud2Region(true) == 42)
    Assert(FaultKindSeen.Load() == 3)
}

test "Filter an unresumed illegal instruction in a code region is reported as a fault in one [exit 101: fault in a code region]" {
    unsafe "AddFaultFilter is unsafe only" {
        _ = AddFaultFilter(PassAll)
    }
    _ = Ud2Region(false)
}

// Linux and macOS route only the faults Mk owns,
// as Windows' frame walk does (sys.FaultOwned): a pc in the image, a thread
// std.sys started, or a stack pointer on a task slot or a carrier's system
// stack. C code outside the image on a thread std did not make is not
// Mk's: the handler gives it the default action. Windows' answer is true
// for every fault its handler routes.
test "Filter Linux and macOS take a fault as Mk's by its pc, its thread or its stack, and C code on a foreign thread as not" {
    var x: u64 = 0
    unsafe "addresses only, never read" {
        slot := raw.Addr(raw.Of(&x))
        image := raw.Addr(raw.Of(&FaultSeen))
        if Target.os != Os.Linux && Target.os != Os.Macos {
            Assert(sys.FaultOwnedBy(4096, 8192, false))
            return
        }
        Assert(!sys.FaultOwnedBy(4096, 8192, false))
        Assert(sys.FaultOwnedBy(4096, 8192, true))
        Assert(sys.FaultOwnedBy(4096, slot, false))
        Assert(sys.FaultOwnedBy(image, 8192, false))
        Assert(sys.FaultOwned(image, slot))
    }
}

// The aarch64 code `movz w0, #0; ret`, whose imm16 is bits 5 to 20 of the
// first word.
const RetArm64: [8]u8 = [
    0x00, 0x00, 0x80, 0x52, // movz w0, #0
    0xC0, 0x03, 0x5F, 0xD6  // ret
]

// RetLen is the length of the code WriteRetAt writes.
RetLen(): int {
    return if Target.arch == Arch.Arm64 { 8 } else { 6 }
}

// WriteRetAt writes the target's code `return v` at offset o of w:
// x86_64's `mov eax, v; ret`, or RetArm64 with v in its immediate.
WriteRetAt(w: mut []u8, o: int, v: u8) {
    if Target.arch == Arch.Arm64 {
        for i in 0..8 {
            w[o + i] = RetArm64[i]
        }
        SetImmAt(w, o, v)
        return
    }
    w[o] = 0xB8
    w[o + 1] = v
    w[o + 2] = 0
    w[o + 3] = 0
    w[o + 4] = 0
    w[o + 5] = 0xC3
}

// SetImmAt writes v into the immediate of the code WriteRetAt wrote at
// offset o of w: x86_64's imm32 byte 1; aarch64's imm16, whose bits 5 to
// 12 (v's value) lie in bytes 0 and 1.
SetImmAt(w: mut []u8, o: int, v: u8) {
    if Target.arch == Arch.Arm64 {
        imm := u32(v) << 5
        w[o] = RetArm64[0] | Trunc[u8](imm)
        w[o + 1] = RetArm64[1] | Trunc[u8](imm >> 8)
        return
    }
    w[o + 1] = v
}

// SkipSealedWrite records an access fault on FaultPage and resumes past
// ProbeWriteByte's 3-byte x86_64 or 4-byte aarch64 store, working identically
// on dual-view (Windows, Linux), mprotect (x86_64 macOS) and MAP_JIT
// (arm64 macOS) code regions.
SkipSealedWrite(c: &FaultContext): FaultAction {
    unsafe "the globals are this test process's" {
        page := FaultPage.Load()
        ps := PageSizeOf()
        if c.Addr() < Trunc[uint](page) || c.Addr() >= Trunc[uint](page) + Cast[uint](ps) {
            return FaultAction.Pass
        }
        FaultSeen.Store(FaultSeen.Load() + 1)
        k: u64 = match c.Kind() {
            .Stack { 1 }
            .Access { 2 }
            .Illegal { 3 }
        }
        FaultKindSeen.Store(k)
    }
    step: uint = if Target.arch == Arch.Arm64 { 4 } else { 3 }
    var d := *c
    d.SetPc(c.Pc() + step)
    return FaultAction.Resume(ctx: d)
}

// ProbeWriteByte attempts a 1-byte store to addr using a fixed-length
// instruction that SkipSealedWrite skips when addr is sealed.
ProbeWriteByte(addr: uint) {
    if Target.arch == Arch.X64 {
        unsafe "addr is a sealed code-region page: the write faults and SkipSealedWrite skips it" {
            asm("mov byte ptr [rax], 1", in("rax") addr)
        }
    }
    if Target.arch == Arch.Arm64 {
        unsafe "addr is a sealed code-region page: the write faults and SkipSealedWrite skips it" {
            asm("strb wzr, [{a}]", a: in(reg) addr)
        }
    }
}

// Outside Edit a code region is sealed: a raw write to its writable address
// faults at that address (Access), while Entry's code runs throughout and a
// subsequent Edit writes cleanly without faulting.
test "Filter a write to a sealed code region faults outside Edit" {
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        var c := vm.CodeRegion.New(4096) else {
            Assert(false)
            return
        }
        defer c.Release()
        var w := c.Edit() else {
            Assert(false)
            return
        }
        var b := w.Bytes()
        waddr := raw.Addr(raw.DataMut(b))
        WriteRetAt(b, 0, 42)
        w.Flush(0, RetLen())
        w.Done()
        Assert(CallCode(c.Entry(0)) == 42)
        FaultPage.Store(u64(waddr))
        _ = AddFaultFilter(SkipSealedWrite)
        ProbeWriteByte(waddr)
        Assert(FaultSeen.Load() == 1)
        Assert(FaultKindSeen.Load() == 2)
        Assert(CallCode(c.Entry(0)) == 42)
        var w2 := c.Edit() else {
            Assert(false)
            return
        }
        SetImmAt(w2.Bytes(), 0, 9)
        w2.Flush(0, RetLen())
        w2.Done()
        Assert(CallCode(c.Entry(0)) == 9)
        Assert(FaultSeen.Load() == 1)
    }
}

// Commit always commits pages in the sealed state (non-writable until Edit).
test "Filter a page a reserved code region commits is sealed until Edit" {
    ps := PageSizeOf()
    unsafe "AddFaultFilter is unsafe only; vm's functions too" {
        var c := vm.CodeRegion.Reserve(4 * ps) else {
            Assert(false)
            return
        }
        defer c.Release()
        // Capture the writable base address before committing page 2.
        var w0 := c.Edit() else {
            Assert(false)
            return
        }
        wbase := raw.Addr(raw.DataMut(w0.Bytes()))
        w0.Done()
        c.Commit(2 * ps, ps) else {
            Assert(false)
        }
        page2 := wbase + Trunc[uint](2 * ps)
        Assert(raw.Read(raw.FromAddr[u8](page2)) == 0)
        FaultPage.Store(u64(page2))
        _ = AddFaultFilter(SkipSealedWrite)
        ProbeWriteByte(page2)
        Assert(FaultSeen.Load() == 1)
        Assert(FaultKindSeen.Load() == 2)
        var w := c.Edit() else {
            Assert(false)
            return
        }
        WriteRetAt(w.Bytes(), 2 * ps, 11)
        w.Flush(2 * ps, RetLen())
        w.Done()
        Assert(CallCode(c.Entry(2 * ps)) == 11)
        Assert(FaultSeen.Load() == 1)
    }
}

// A call into a page a reserved code region has not committed is a
// fault in a code region, as the fault path's table routes it.
test "Filter a call into an uncommitted page of a reserved code region is a fault in one [exit 101: fault in a code region]" {
    ps := PageSizeOf()
    unsafe "vm's functions are unsafe only" {
        var c := vm.CodeRegion.Reserve(2 * ps) else {
            return
        }
        defer c.Release()
        c.Commit(0, ps) else {}
        FaultKindSeen.Store(u64(CallCode(c.Entry(ps))))
    }
}
