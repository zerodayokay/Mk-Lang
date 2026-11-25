// Why: a hardware fault in Mk code (a stack overflow, a bad access, an
// illegal instruction) must become Mk's own fault report, while C code and
// system DLLs that handle their own exceptions with __try keep working.
//
// What: the Windows fault path: FaultHook (the vectored exception handler
// MkVectoredHandler and the filter MkUnhandledFilter), ExternFaultHook,
// FuncRowHook, ImageBounds, FatalWrite, FaultOwner's frame walk, and
// TrapResume, which resumes a trapped call at mk_trap_call.
//
// How: a fault becomes a FaultInfo handed to the hook std.rt installs
// (rt.OnFault). FaultOwner unwinds the faulting thread's frames by their
// unwind entries and matches them against std.rt's function table, so a
// fault is Mk's only when no exception handler lies between it and Mk code.

import std.raw

struct FaultState {
    // hook holds the bytes of the fn(&FaultInfo): never value; set is 1
    // once it is written (a write through a raw pointer into an atomic is
    // defined).
    hook: raw.Atomic[u64]
    set: raw.Atomic[u32]
    image_lo: raw.Atomic[u64]
    image_hi: raw.Atomic[u64]
    // in_extern holds the bytes of the fn(uint): uint ExternFaultHook
    // installed; extern_set is 1 once it is written.
    in_extern: raw.Atomic[u64]
    extern_set: raw.Atomic[u32]
    // rows and nrows are std.rt's function table as FuncRowHook gave it:
    // its address, then its row count, 0 until it is given.
    rows: raw.Atomic[u64]
    nrows: raw.Atomic[u64]
}

var Faults: FaultState = FaultState(hook: raw.Atomic[u64].New(0), set: raw.Atomic[u32].New(0),
    image_lo: raw.Atomic[u64].New(0), image_hi: raw.Atomic[u64].New(0), in_extern: raw.Atomic[u64].New(0),
    extern_set: raw.Atomic[u32].New(0), rows: raw.Atomic[u64].New(0), nrows: raw.Atomic[u64].New(0))

const ExceptionStackOverflow: u32 = 0xC000_00FD
const ExceptionAccessViolation: u32 = 0xC000_0005
const ExceptionInPageError: u32 = 0xC000_0006
const ExceptionIllegalInstruction: u32 = 0xC000_001D
const ExceptionPrivInstruction: u32 = 0xC000_0096
const ContinueSearch: i32 = 0
// CONTEXT offsets, and its size; it is 16-aligned.
const ContextRsp: int = 0x98
const ContextRip: int = 0xF8
const ContextBytes: int = 1232
// RtlVirtualUnwind's handler type for an exception handler (__except).
const UnwFlagEHandler: u32 = 1
// The most frames FaultOwner walks before it gives the fault up; a frame
// with no unwind entry moves the walk up one word.
const WalkFrames: int = 512
// A row of std.rt's function table (rt.FuncRow):
// 24 bytes, the function's entry address first, its kind at RowKind; kind
// RowMem is a memory function C code calls too (memcpy, memset, ...).
const RowBytes: int = 24
const RowKind: int = 16
const RowMem: u32 = 3
// Kind RowChkstk is a stack probe (__chkstk, mk_chkstk_65536), which C
// code calls too.
const RowChkstk: u32 = 1
// The offsets in the x64 CONTEXT record of the scratch registers TrapResume
// sets.
const ContextR10: int = 0xC8
const ContextR11: int = 0xD0
// The immediate of a trapped call's ud1 (internal/codegen/trapform.go).
const TrapCall: u8 = 0x7F

/// FaultHook installs h, the vectored handler (first in the chain) and the
/// unhandled-exception filter, once per process, at startup.
/// O(1) system calls (the handler's installation); allocates nothing.
export FaultHook(h: fn(&FaultInfo): never) {
    unsafe "the hook word is an atomic's memory; kernel32" {
        raw.Write(raw.PtrCast[raw.Atomic[u64], fn(&FaultInfo): never](raw.Of(&Faults.hook)), h)
        // The hook is written before the handler is installed: the
        // AddVectoredExceptionHandler call that publishes the handler is
        // ordered after it (a system call, and the Swap is sequentially
        // consistent), so a fault on any later thread reads h. The Swap
        // admits one installer; image_lo and image_hi are stored before the
        // handler exists, the same edge.
        if Faults.set.Swap(1) == 0 {
            if LibFaults.on.Load() != 0 {
                // A library (lib.mk): its own bounds, its handler kept for
                // the stop, and the host's unhandled-exception filter left.
                LibFaults.veh.Store(u64(raw.Addr(AddVectoredExceptionHandler(1, MkVectoredHandler))))
                return
            }
            (lo, hi) := ImageBounds(raw.Null[u8]())
            // Edge: both bounds before the handler that reads them exists.
            Faults.image_lo.Store(u64(lo))
            Faults.image_hi.Store(u64(hi))
            _ = AddVectoredExceptionHandler(1, MkVectoredHandler)
            _ = SetUnhandledExceptionFilter(MkUnhandledFilter)
        }
    }
}

/// ExternFaultHook installs f, the in-extern test: f(sp), for a fault whose
/// stack pointer is sp, is the stack pointer mk_call_system switched to
/// when the fault happened while a task ran a user extern on its carrier's
/// system stack, else 0. The vectored handler stops its frame walk there
/// (FaultOwner) and gives the C code a true StackLimit (CoverStackLimit).
/// Called once, before FaultHook.
/// O(1); allocates nothing.
export ExternFaultHook(f: fn(uint): uint) {
    unsafe "the filter word is an atomic's memory" {
        raw.Write(raw.PtrCast[raw.Atomic[u64], fn(uint): uint](raw.Of(&Faults.in_extern)), f)
    }
    // Edge: the filter's bytes are written before this sequentially
    // consistent Store, and ExternTop reads them only after a Load of 1.
    Faults.extern_set.Store(1)
}

/// FuncRowHook gives the vectored handler std.rt's function table: n rows
/// of rt.FuncRow at rows (24 bytes each, the function's entry address
/// first), one for each of Mk's functions, adapters and support routines,
/// so it can tell Mk's code from C's (FaultOwner). The rows live for the
/// process. Called once, by std.task's Start before FaultHook, so no fault
/// the handler sees comes before it. O(1); allocates nothing.
export FuncRowHook(rows: *u8, n: int) {
    // Edge: the address, then the count (below), which FaultOwner loads first.
    Faults.rows.Store(u64(raw.Addr(rows)))
    // Edge: the address is stored before the count, and FaultOwner loads
    // the count first, so a count it reads comes with its address.
    Faults.nrows.Store(Trunc[u64](n))
}

// TrapEntry is the intrinsic address of mk_trap_call, the support pack's
// routine a trapped call resumes at (internal/codegen/trapform.go), or
// null when the build traps no call. O(1); allocates nothing.
TrapEntry(): *u8

// TrapResume resumes a trapped call (internal/codegen/trapform.go): when
// the build has mk_trap_call and the illegal instruction at the context's
// pc is ud1l 0x7f(%eax), %eax (67 0F B9 40 7F) right after a prefetchnta
// (0F 18 05) whose rel32 names the callee, the twelve bytes inside the
// image and the callee the entry of one of Mk's functions (a kind-0 row of
// std.rt's table), it sets the context to mk_trap_call (rip) with r10 the
// callee and r11 the trap's address plus one (the return address
// mk_trap_call's machine frame holds), and returns true; the handler then
// resumes the thread there, so the callee runs as the call ran it, on the
// faulting stack, outside exception dispatch. Codegen emits that pair for
// nothing else, so any other illegal instruction, or one before FaultHook
// stored the image bounds, gives false and goes on as before. It has no
// check of its own (wrapping arithmetic and raw reads only), so it cannot
// trap inside the handler; it reads only inside the image and the rows.
// In a build with no trapped call TrapEntry is null and it folds to false.
// O(rows); allocates nothing.
TrapResume(p: *u8): bool {
    // First, so a build with no trapped call folds this to false.
    entry := raw.Addr(TrapEntry())
    if entry == 0 {
        return false
    }
    // Edge: FaultHook stored the image bounds before it installed this
    // handler.
    lo := Trunc[uint](Faults.image_lo.Load())
    hi := Trunc[uint](Faults.image_hi.Load())
    if hi -% lo < 12 {
        return false
    }
    unsafe "p is the EXCEPTION_POINTERS the system passed; every byte read lies inside the image" {
        ctx := raw.Read(raw.PtrCast[u8, *u8](raw.Offset(p, 8)))
        pc := raw.Read(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextRip)))
        // pc - 7 to pc + 4 inside the image, in one compare (a pc below
        // lo + 7 wraps to a large distance).
        if pc -% 7 -% lo >= hi -% lo -% 11 {
            return false
        }
        if raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.FromAddr[u8](pc))) != 0x40B9_0F67 ||
            raw.Read(raw.FromAddr[u8](pc +% 4)) != TrapCall ||
            raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.FromAddr[u8](pc -% 7))) & 0xFF_FFFF != 0x05_180F {
            return false
        }
        // The prefetch's rel32, relative to its end (the trap's address),
        // sign-extended by wrapping arithmetic.
        rel := uint(raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.FromAddr[u8](pc -% 4))))
        callee := if rel >= 0x8000_0000 { pc -% (0x1_0000_0000 -% rel) } else { pc +% rel }
        // The callee must be a Mk function: the entry of a kind-0 row of
        // std.rt's table (FuncRowHook's), so no other bytes can send the
        // thread anywhere else.
        // Edge: FuncRowHook's Store of the count, after the address.
        n := Trunc[int](Faults.nrows.Load())
        rows := raw.FromAddr[u8](Trunc[uint](Faults.rows.Load()))
        var found := false
        for k in 0..n {
            row := raw.Offset(rows, k *% RowBytes)
            if raw.Read(raw.PtrCast[u8, uint](row)) == callee && raw.Read(raw.PtrCast[u8, u32](raw.Offset(row, RowKind))) == 0 {
                found = true
            }
        }
        if !found {
            return false
        }
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextR10)), callee)
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextR11)), pc +% 1)
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextRip)), entry)
        return true
    }
}

// ExternTop is the installed in-extern test of sp, 0 before one is.
ExternTop(sp: uint): uint {
    // Edge: ExternFaultHook's Store of 1, after the filter's bytes.
    if Faults.extern_set.Load() != 1 {
        return 0
    }
    unsafe "the filter word holds a fn value ExternFaultHook wrote" {
        f := raw.Read(raw.PtrCast[raw.Atomic[u64], fn(uint): uint](raw.Of(&Faults.in_extern)))
        return f(sp)
    }
}

/// ImageBounds is the executable image's address range [lo, hi):
/// __ImageBase to __ImageBase + SizeOfImage, from the PE headers of the
/// module GetModuleHandleW(null) names. sp is unused here; the
/// Linux variant reads the auxiliary vector from it.
/// O(1): two reads of the PE headers and one system call; allocates nothing.
export ImageBounds(sp: *u8): (lo: uint, hi: uint) {
    unsafe "kernel32; the image's own PE headers" {
        base := GetModuleHandleW(raw.Null[u16]())
        if base == 0 {
            return (lo: 0, hi: 0)
        }
        b := raw.FromAddr[u8](base)
        nt := u32(raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(b, 0x3C))))
        size := raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(b, int(nt) + 0x50)))
        return (lo: base, hi: base + uint(size))
    }
}

// InImage reports whether pc lies in the executable image, not a system DLL.
InImage(pc: uint): bool {
    a := u64(pc)
    // Edge: FaultHook stored both bounds before AddVectoredExceptionHandler
    // published this handler.
    return a >= Faults.image_lo.Load() && a < Faults.image_hi.Load()
}

// Report hands f to the hook; it never returns.
Report(f: FaultInfo): never {
    // Edge: FaultHook's Swap to 1 came after the hook's bytes were written, so
    // a Load of 1 reads them.
    if Faults.set.Load() == 1 {
        unsafe "the hook word holds a fn value FaultHook wrote" {
            h := raw.Read(raw.PtrCast[raw.Atomic[u64], fn(&FaultInfo): never](raw.Of(&Faults.hook)))
            h(&f)
        }
    }
    FatalWrite("panic: fault\n".AsBytes())
    FatalExit(101)
}

// Words reads the exception code, pc, sp and the access address of the
// EXCEPTION_POINTERS at p.
Words(p: *u8): (code: u32, pc: uint, sp: uint, addr: uint) {
    unsafe "p is the EXCEPTION_POINTERS the system passed" {
        rec := raw.Read(raw.PtrCast[u8, *u8](p))
        ctx := raw.Read(raw.PtrCast[u8, *u8](raw.Offset(p, 8)))
        code := raw.Read(raw.PtrCast[u8, u32](rec))
        pc := raw.Read(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextRip)))
        sp := raw.Read(raw.PtrCast[u8, uint](raw.Offset(ctx, ContextRsp)))
        // NumberParameters at 24; the access address is
        // ExceptionInformation[1], at 40, when there are two.
        addr: uint = if raw.Read(raw.PtrCast[u8, u32](raw.Offset(rec, 24))) >= 2 {
            raw.Read(raw.PtrCast[u8, uint](raw.Offset(rec, 40)))
        } else {
            0
        }
        return (code: code, pc: pc, sp: sp, addr: addr)
    }
}

// MkVectoredHandler takes a stack overflow always (in a library, only one that is Mk's:
// LibPassOverflow), and an access fault in
// the image when it is Mk's (FaultOwner): in Mk's own code, or in
// code with no exception handler between it and the Mk frame that called
// it. Everything else goes on to frame handlers, so C code and system DLLs
// that handle their own exceptions with __try keep working, and
// STATUS_GUARD_PAGE_VIOLATION stays the kernel's growth path; a fault
// nobody handles reaches MkUnhandledFilter when the frame walk can reach
// it. Any exception while a task runs a user extern on its carrier's
// system stack (the in-extern test, ExternTop) first gets a true StackLimit
// (CoverStackLimit), for the walk and for the C code's own handlers. A
// fault whose pc is in a std.vm code region goes to the filters, and one
// none resumes is reported as kind 4. It runs on the faulting
// stack: for an overflow, inside the guarantee (a task's 16 KiB; a
// carrier's 32 KiB).
MkVectoredHandler(p: *u8): i32 {
    (code, pc, sp, addr) := Words(p)
    // A trapped call resumes into its callee before any filter or
    // FaultOwner sees it, as the call never reached them (TrapResume).
    if code == ExceptionIllegalInstruction && TrapResume(p) {
        return ExceptionContinueExecution
    }
    if code == ExceptionStackOverflow {
        // A library passes on an overflow that is not Mk's (lib.mk).
        if LibPassOverflow(pc, sp, ExternTop(sp)) {
            return ContinueSearch
        }
        // runtime.AddFaultFilter's filters run first (filter.mk), here in
        // the task's guarantee area.
        if Filter(p, 1, sp, pc) {
            return ExceptionContinueExecution
        }
        // An overflow in the image is reported at the pc FaultOwner gives,
        // as an access fault is: inside a memory function or a stack probe
        // Mk code called (rows with no site), at that Mk caller; in C code
        // with no handler, at the Mk frame that called it; in Mk code, at
        // its own pc. A walk that gives the fault up (0) keeps the
        // faulting pc.
        var at := pc
        if InImage(pc) {
            o := FaultOwner(p, ExternTop(sp))
            if o != 0 {
                at = o
            }
        }
        Report(FaultInfo(kind: 1, addr: sp, pc: at, sp: sp))
    }
    top := ExternTop(sp)
    if top != 0 {
        CoverStackLimit()
    }
    access := code == ExceptionAccessViolation || code == ExceptionInPageError
    if access || code == ExceptionIllegalInstruction || code == ExceptionPrivInstruction {
        kind: u32 = if access { 2 } else { 3 }
        // A fault in a std.vm code region goes to the filters and is never
        // reported as a fault in Mk code; one no filter resumes is
        // reported as a fault in a code region, exit 101, as on Linux
        // (std.rt's OnFault, kind 4).
        if InCodeRegion(pc) {
            if Filter(p, kind, addr, pc) {
                return ExceptionContinueExecution
            }
            Report(FaultInfo(kind: 4, addr: addr, pc: pc, sp: sp))
        }
        if InImage(pc) {
            at := FaultOwner(p, top)
            if at != 0 {
                if Filter(p, kind, addr, pc) {
                    return ExceptionContinueExecution
                }
                Report(FaultInfo(kind: kind, addr: addr, pc: at, sp: sp))
            }
        }
    }
    return ContinueSearch
}

// FaultOwner walks the faulting thread's frames from the context of the
// EXCEPTION_POINTERS at p, by their unwind entries (RtlLookupFunctionEntry,
// RtlVirtualUnwind on a copy of the context), to decide whether a fault is
// Mk's: the pc to report it at, or 0 when it is not. Mk code has no
// exception handler, so a fault is Mk's when no frame below the first of
// Mk's code has one: a frame whose function starts at a row of std.rt's
// table (Mk's functions, adapters and support routines; a leaf with no
// entry is known when it faults at its first instruction), or the frame
// of mk_call_system, whose stack pointer is top - 32 after its callee
// returns (top, ExternTop's, is 0 off a user extern). So a fault in Mk's
// own code, a C callback's included, is reported at its own pc;
// one in C code with no handler at the Mk frame that called it, or at the
// faulting pc when that was a leaf an Mk function called (an Mk leaf, which
// has no entry, gets its own site). A memory function's row (RowMem) or
// a stack probe's (RowChkstk) at frame 0 does not decide (the probes have
// unwind entries of their own): C code calls
// them too, so the walk unwinds that frame by its entry, or as a leaf,
// and its caller decides. The other module-assembly routines (mk_switch,
// mk_call_system, mk_task_start) have entries too and decide at frame 0
// as any row does: only Mk code calls them. A
// fault in the support pack's memset is Mk's when Mk code called it,
// reported at that Mk caller (its site), and a C caller's own __except
// handles it with the callee-saved registers the entry restores. Every
// other row, thunks and adapters included, decides at once. 0 when a
// frame on the way has an exception handler (C code that may handle its
// own fault with __try), at a pc of 0 (it has no entry and is no row), or
// past WalkFrames frames. A
// frame with no entry is unwound as the system unwinds a leaf: its return
// address is at sp. Clang gives a C function an entry only when it has a
// handler or an unwind table, so a walk through C frames without one moves
// up a word at a time, as the system's own dispatch does. An Mk function
// split into chained unwind entries is known by its first part only; a
// fault in another part is reported at its caller. Before FuncRowHook no
// row matches. The walk reads only inside the thread's stack as the TEB
// records it (StackLimit to StackBase, the limit made true first by
// CoverStackLimit during an extern), and gives the fault up (0) at a frame
// whose stack pointer leaves it, so a wrong unwind never faults here.
// Allocates nothing; it runs in the handler.
FaultOwner(p: *u8, top: uint): uint {
    var buf: [156]u64 = [0; 156]
    // Edge: FuncRowHook's Store of the count, after the address.
    n := Trunc[int](Faults.nrows.Load())
    unsafe "p is the EXCEPTION_POINTERS the system passed; c is buf's 16-aligned copy of its CONTEXT; the rows live for the process" {
        rows := raw.FromAddr[u8](Trunc[uint](Faults.rows.Load()))
        c := raw.FromAddr[u8]((raw.Addr(raw.DataMut(mut buf[..])) +% 15) & ~Trunc[uint](15))
        raw.Copy(c, raw.Read(raw.PtrCast[u8, *u8](raw.Offset(p, 8))), ContextBytes)
        rip := raw.PtrCast[u8, uint](raw.Offset(c, ContextRip))
        rsp := raw.PtrCast[u8, uint](raw.Offset(c, ContextRsp))
        pc := raw.Read(rip)
        teb := Teb()
        lo := raw.Read(raw.PtrCast[u8, uint](raw.Offset(teb, 0x10)))
        room := raw.Read(raw.PtrCast[u8, uint](raw.Offset(teb, 0x08))) -% lo -% 8
        // near: frame 0 was a leaf (not a memory function passed on), so
        // its own pc is reported at frame 1.
        var near := false
        for i in 0..WalkFrames {
            at := raw.Read(rip)
            s := raw.Read(rsp)
            // With top 0 this is never true: no stack pointer is 2^64 - 32.
            if s == top -% 32 {
                return at
            }
            var base: uint = 0
            e := RtlLookupFunctionEntry(at, raw.OfMut(mut base), raw.Null[u8]())
            start := if raw.IsNull(e) { at } else { base +% uint(raw.Read(raw.PtrCast[u8, u32](e))) }
            var pass := false
            for k in 0..n {
                row := raw.Offset(rows, k *% RowBytes)
                if raw.Read(raw.PtrCast[u8, uint](row)) == start {
                    kind := raw.Read(raw.PtrCast[u8, u32](raw.Offset(row, RowKind)))
                    if i == 0 && (kind == RowMem || kind == RowChkstk) {
                        pass = true
                        break
                    }
                    return if near { pc } else { at }
                }
            }
            // The unwind reads the stack from s: only inside the thread's
            // stack (one compare: s below lo wraps to a large distance).
            if s -% lo >= room {
                return 0
            }
            if raw.IsNull(e) {
                near = i == 0 && !pass
                raw.Write(rip, raw.Read(raw.FromAddr[uint](raw.Read(rsp))))
                raw.Write(rsp, raw.Read(rsp) +% 8)
            } else {
                near = false
                var data: u64 = 0
                var frame: uint = 0
                h := RtlVirtualUnwind(UnwFlagEHandler, base, at, e, c, raw.PtrCast[u64, u8](raw.OfMut(mut data)),
                    raw.OfMut(mut frame), raw.Null[u8]())
                if !raw.IsNull(h) {
                    return 0
                }
            }
        }
        return 0
    }
}

// CoverStackLimit lowers the thread's NT_TIB StackLimit (gs:0x10) to the
// page this handler runs on, when it is above it. During a user extern the
// words are the system stack's as std.task installed them (SystemStack),
// which follow the stack as it grows; this guards a StackLimit that was
// left above the extern's frames all the same. Exception dispatch and
// RtlUnwindEx refuse frames below StackLimit (STATUS_BAD_STACK), so the C
// code's own __except would never run. Every page from here up is
// committed, since this frame is on it, so the lower limit is true.
// mk_call_system restores the task's words after the call.
CoverStackLimit() {
    var mark: u64 = 0
    unsafe "the TEB of the running thread; mark is this frame's" {
        here := raw.Addr(raw.Of(&mark)) & ~Trunc[uint](4095)
        limit := raw.PtrCast[u8, uint](raw.Offset(Teb(), 0x10))
        if raw.Read(limit) > here {
            raw.Write(limit, here)
        }
    }
}

// MkUnhandledFilter reports an exception nobody handled as kind 2 with no
// site.
MkUnhandledFilter(p: *u8): i32 {
    w := Words(p)
    kind: u32 = if w.code == ExceptionStackOverflow { 1 } else { 2 }
    Report(FaultInfo(kind: kind, addr: w.addr, pc: 0, sp: w.sp))
}

/// FatalWrite writes data to standard error, synchronously and unbuffered.
/// A console gets UTF-16 through WriteConsoleW, as every console write does,
/// so a report with non-ASCII text reads the same as the "error:"
/// line. Each call is complete UTF-8 (the report's pieces are whole
/// strings), so a fresh carry per call loses nothing; no state is shared
/// with the standard error stream's record.
/// Errors are ignored. O(len(data)); allocates nothing.
export FatalWrite(data: []u8) {
    h := StdHandle(2)
    if IsConsole(h) {
        var carry := NewCarry()
        _ = WriteConsole(h, data, mut carry)
        return
    }
    _ = WriteSync(h, data, -1)
}

/// FaultOwnerHook is Linux's ownership hook (std/sys/linux/fault.mk); here
/// the vectored handler's frame walk decides (FaultOwner), so it does
/// nothing. O(1); allocates nothing.
export FaultOwnerHook(f: fn(uint): bool) {
}

/// FaultOwned is Linux's ownership test; here every fault the vectored
/// handler routes is already Mk's, so it answers true. O(1).
export FaultOwned(pc: uint, sp: uint): bool {
    return true
}

/// FaultOwnedBy is FaultOwned with the thread's answer given: true here.
/// O(1).
export FaultOwnedBy(pc: uint, sp: uint, made: bool): bool {
    return true
}
