// Why: a call to a bodiless extern outside std (user C code) may need far
// more stack than a task slot has, so it runs on the carrier's own system
// stack instead.
//
// What: SystemStack and SystemStackDone, which bracket such a call, and the
// fault hooks OnMkStack and InExtern that tell the fault handlers whose
// stack a faulting stack pointer lies on.
//
// How: the scheduler is suspended inside Switch while its task runs, so the
// area below its saved frame is free; SystemStack hands out a pointer 256
// bytes below it and sets the task's in_c flag, which InExtern reads. On
// Windows the call also runs under the system stack's NT_TIB words, and the
// scheduler's saved context is their one record: SystemStack copies them
// from it, and SystemStackDone stores back the StackLimit the call left.

import std.raw
import std.rt

/// SystemStack is the current carrier's record with sp 256 bytes below the
/// scheduler's saved frame, 16-aligned; null off a task stack (call
/// directly).
/// O(1); allocates nothing: the stack is the carrier's, mapped when the carrier
/// started.
export SystemStack(): *SysStack {
    t := Current()
    if raw.IsNull(t) {
        return raw.Null[SysStack]()
    }
    unsafe "the running task's carrier" {
        c := (*t).carrier
        sp := (Trunc[uint]((*c).sys_ctx[0]) - 256) & ~Trunc[uint](15)
        (*c).sys_stack.sp = raw.FromAddr[u8](sp)
        if Target.os == Os.Windows {
            // The words Switch saved when the scheduler left its stack, with
            // the StackLimit of any call since (SystemStackDone). The words
            // read when the carrier started are stale once the stack has
            // grown: a StackLimit above the stack's real one makes a probe
            // that stores, as MSVC's __chkstk does, write over live frames.
            (*c).sys_stack.tib = [(*c).sys_ctx[1], (*c).sys_ctx[2], (*c).sys_ctx[3]]
        }
        h := HeaderOf(t)
        (*h).in_c = 1
        // A plain add on the carrier's own record (the task runs on c's
        // thread): no atomic on the C-call path.
        (*c).extern_calls += 1
        return raw.Of(&(*c).sys_stack)
    }
}

/// SystemStackDone clears in_c after the call. On Windows it also records
/// the StackLimit the call left in the scheduler's saved context, so the
/// scheduler and the carrier's next call run under the system stack's real
/// limit when the call committed more of it.
/// O(1); allocates nothing.
export SystemStackDone() {
    h := rt.CurrentSlot()
    if !raw.IsNull(h) {
        unsafe "the running task's header, and its carrier's record, which only this thread writes" {
            (*h).in_c = 0
            if Target.os == Os.Windows {
                c := (*TaskOf(h)).carrier
                (*c).sys_ctx[2] = (*c).sys_stack.tib[1]
            }
        }
    }
}

// OnMkStack is the fault-ownership hook (sys.FaultOwnerHook; on Windows,
// which faults outside Mk's code still go to the fault filters): whether
// sp lies on a task slot or a recorded carrier system stack, a pinned
// carrier's included. It runs in the fault handler: two region compares
// and rt.SysStackOf's 256 loads, no lock, no allocation.
OnMkStack(sp: uint): bool {
    return rt.SlotTop(sp) != 0 || rt.SysStackOf(sp) >= 0
}

// InExtern is the in_c test, which std.sys's Windows vectored handler
// asks through ExternFaultHook: when sp, a faulting thread's stack pointer,
// lies on a carrier's system stack while that carrier's current task runs
// a user extern (in_c, set by SystemStack), the stack pointer SystemStack
// gave mk_call_system for the call, else 0. Such a fault may be the C
// code's own, for its own handlers, and the handler's frame walk stops at
// mk_call_system's frame. It runs in the fault handler: no lock, no
// allocation, and a null TaskState (before TaskInit, or a program with no
// task) answers 0.
InExtern(sp: uint): uint {
    i := rt.SysStackOf(sp)
    st := TS()
    // i names the faulting thread's own carrier, whose current, whose
    // task's in_c and whose sys_stack only that thread writes, so those
    // reads are in program order on one thread.
    if i < 0 || raw.IsNull(st) {
        return 0
    }
    unsafe "the TaskState page; carrier i's record, committed; its current task's header" {
        // Any recorded i (below 256) is a committed record: a shared
        // carrier's (below ncarriers, StartCarrier's Add before its thread
        // exists) or a pinned one's (at or past want, PinClaim's commit
        // before its thread exists). rt.SysStackOf answers only entries a
        // carrier's own thread stored on its new stack (CarrierStack), so a
        // pinned carrier is taken as a shared one is.
        if i >= MaxCarriers {
            return 0
        }
        c := CarrierAt(i)
        t := (*c).current
        if raw.IsNull(t) || (*HeaderOf(t)).in_c != 1 {
            return 0
        }
        return raw.Addr((*c).sys_stack.sp)
    }
}
