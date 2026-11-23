// Why: runtime.AddFaultFilter must see a fault before Mk's own report and
// take it over, as on Linux; std.vm's code regions route their faults here
// too.
//
// What: one filter hook, called by SigFault (fault.mk) on the alternate
// signal stack before Mk's report, and a table of code regions whose faults
// go to the hook and are never reported as faults in Mk code.
//
// How: Linux's table and hook (std/sys/linux/filter.mk), lock-free, because
// the handler reads both while a thread may be adding to them: each entry is
// written before its count or its word is published, and a fault taken while
// one is changed never waits. The hook gets the thread state, not the
// ucontext: on macOS the registers sit behind uc_mcontext's pointer.

import std.raw

// FilterState holds the hook's fn bytes (set is 1 once written) and the
// code regions: CodeSlots pairs (lo, hi), each with a state word st whose
// low two bits are the slot's state (0 free, 1 claimed, 2 live, 3 being
// removed) and whose other bits count the slot's uses. Every change of a
// slot moves its st word forward, so a reader that sees the same live st
// word before and after it reads lo and hi read one region's pair
// (a seqlock with no waiting: a reader that sees a change skips the slot).
struct FilterState {
    hook: raw.Atomic[u64]
    set: raw.Atomic[u32]
    st: [64]u64
    lo: [64]u64
    hi: [64]u64
}

const CodeSlots: int = 64

var Filters: FilterState = FilterState(hook: raw.Atomic[u64].New(0), set: raw.Atomic[u32].New(0), st: [0; 64], lo: [0; 64], hi: [0; 64])

/// FaultFilterHook installs f, called on a fault as f(context, kind, addr,
/// pc): context the thread state the signal's uc_mcontext points to (its
/// registers at the offsets std.runtime's ContextReg gives), kind 2 (SIGSEGV or SIGBUS: an
/// access; std.runtime tells a guard page's fault, kind 1, by the slot
/// regions) or 3 (SIGILL: an illegal instruction); true returns from the handler, which resumes at the context f
/// may have edited. Called once (std.runtime's first AddFaultFilter).
/// O(1); allocates nothing.
export FaultFilterHook(f: fn(*u8, u32, uint, uint): bool) {
    unsafe "the hook word is an atomic's memory" {
        raw.Write(raw.PtrCast[raw.Atomic[u64], fn(*u8, u32, uint, uint): bool](raw.Of(&Filters.hook)), f)
    }
    // Edge: the Store publishes the hook's bytes, written above, to the
    // handler's Load.
    Filters.set.Store(1)
}

// FilterWord is word k of the code-region table's array a (0 lo, 1 hi, 2
// st), as an atomic.
FilterWord(a: int, k: int): &raw.Atomic[u64] {
    unsafe "Filters is static; a u64 has raw.Atomic[u64]'s layout, and every access to these words is atomic" {
        p := if a == 0 { raw.Of(&Filters.lo[k]) } else if a == 1 { raw.Of(&Filters.hi[k]) } else { raw.Of(&Filters.st[k]) }
        return &*raw.PtrCast[u64, raw.Atomic[u64]](p)
    }
}

/// CodeRegionAdd records [lo, hi) as a code region; false when CodeSlots
/// regions are recorded (std.vm's CodeRegion.New then fails). Lock-free:
/// a slot is claimed (st 0 to 1), written, then published (st 2).
/// O(CodeSlots); allocates nothing.
export CodeRegionAdd(lo: uint, hi: uint): bool {
    for k in 0..CodeSlots {
        // Edge: seq_cst; the Cas from a free st word (state 0) claims the
        // slot for this caller alone, and the Store of state 2 below
        // publishes lo and hi, written before it, to InCodeRegion's Loads.
        s := FilterWord(2, k).Load()
        if s & 3 == 0 && FilterWord(2, k).Cas(s, s + 1) {
            FilterWord(0, k).Store(u64(lo))
            FilterWord(1, k).Store(u64(hi))
            FilterWord(2, k).Store(s + 2)
            return true
        }
    }
    return false
}

/// CodeRegionRemove forgets the code region that starts at lo, recorded by
/// CodeRegionAdd and removed once (std.vm's CodeRegion.Release): its slot
/// goes from live to being removed (st 3), is cleared, and is freed with
/// its use count advanced. O(CodeSlots). Allocates nothing.
export CodeRegionRemove(lo: uint) {
    for k in 0..CodeSlots {
        // Edge: seq_cst; only the region's owner removes it, so a live st
        // word whose lo is this one stays live until the Cas below, and a
        // reader that read the slot before the Cas sees st change after.
        s := FilterWord(2, k).Load()
        if s & 3 == 2 && FilterWord(0, k).Load() == u64(lo) && FilterWord(2, k).Cas(s, s + 1) {
            FilterWord(0, k).Store(0)
            FilterWord(1, k).Store(0)
            FilterWord(2, k).Store(s + 2)
            return
        }
    }
}

/// InCodeRegion is whether pc lies in a recorded code region: a slot counts
/// when its st word is live and the same before and after lo and hi are
/// read; a slot changing meanwhile is skipped, since the region at pc, if
/// any, was recorded before the fault and is not being removed. Never
/// waits (the fault handlers call it). O(CodeSlots); allocates nothing.
export InCodeRegion(pc: uint): bool {
    for k in 0..CodeSlots {
        // Edge: seq_cst; the first Load of a live st word pairs with
        // CodeRegionAdd's Store of it, so lo and hi read after it are that
        // region's, unless st moved, which the second Load sees.
        s := FilterWord(2, k).Load()
        if s & 3 == 2 {
            l := FilterWord(0, k).Load()
            h := FilterWord(1, k).Load()
            if FilterWord(2, k).Load() == s && u64(pc) >= l && u64(pc) < h {
                return true
            }
        }
    }
    return false
}

// Filter runs the hook on the thread state at mc (the ucontext's
// uc_mcontext); true when it asked to resume, and libSystem's trampoline
// then resumes at the state the hook may have edited. False with no hook.
Filter(mc: *u8, kind: u32, addr: uint, pc: uint): bool {
    // Edge: as FaultFilterHook's Store.
    if Filters.set.Load() != 1 {
        return false
    }
    unsafe "mc is the faulting thread's state; the hook's bytes were written before set" {
        f := raw.Read(raw.PtrCast[raw.Atomic[u64], fn(*u8, u32, uint, uint): bool](raw.Of(&Filters.hook)))
        return f(mc, kind, addr, pc)
    }
}
