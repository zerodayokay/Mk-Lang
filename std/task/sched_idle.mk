// Why: idle carriers must publish themselves idle, recheck every queue
// per the scheduler's wake protocol, trim spare slots and wait in the
// poller without missing a wake-up.
// What: Idle, FlushSlots, TrimSpare and the controlled-schedule test gates
// IdleGate and ReapGate (split from sched.mk).
// How: follows the seq_cst publication and AnyWork recheck protocol of
// sched.mk, running on each carrier's system stack.

import std.raw
import std.rt
import std.sys

// Idle is the last step. c publishes itself idle, stops spinning, rechecks
// every queue (the header's protocol), and only then waits in the poller:
// with the earliest timer's deadline when no other idle carrier waits for
// one as early (ArmIdle), else until a wake, a completion or a signal.
// Every idle carrier may be inside PollWait at once (std.sys's contract).
// It returns spinning, to look for work.
Idle(c: *Carrier) {
    main := Target.os == Os.Macos && raw.Addr(c) == raw.Addr(C0())
    if main {
        RunMainService(c)
    }
    st := TS()
    TrimSpare(c)
    unsafe "the TaskState page; c is the calling thread's carrier" {
        (*c).linger_from = 0
        (*c).pool_wait = false
        // Edge: the idler's publication: before the unspin and AnyWork in S (the
        // wake protocol in the header).
        _ = (*st).nidle.Add(1)
        Jitter(JitterIdle)
        if (*c).spinning {
            (*c).spinning = false
            _ = (*st).nspinning.Add(0xFFFF_FFFF)
        }
        if AnyWork() {
            // Edge: none needed; leaving idle (it then spins and searches).
            _ = (*st).nidle.Add(0xFFFF_FFFF)
            Spin(c)
            return
        }
    }
    (timeout, armed) := ArmIdle()
    // The pool's early window: while a cancelled pool job may still be
    // in its call, an idle carrier wakes within ReinterruptNs to interrupt
    // it again.
    var wait := timeout
    if PoolListed() && (wait < 0 || wait > ReinterruptNs) {
        wait = ReinterruptNs
    }
    Jitter(JitterIdle)
    IdleGateHold()
    var done: [64]*sys.Op = [raw.Null[sys.Op](); 64]
    n := if main {
        WaitMainService(c, wait, mut done[..])
    } else {
        sys.PollWait(wait, mut done[..])
    }
    SignalPoll(c)
    if PoolListed() {
        PoolReinterrupt()
    }
    unsafe "the TaskState page Start mapped, which lives until exit" {
        if timeout < 0 && !(main && MainServiceDue(c) != 0) {
            // Edge: none; a test counter (test "RT-MN-TimerNoSpuriousWake").
            _ = (*st).idle_unarmed.Add(1)
        }
        _ = (*st).nidle.Add(0xFFFF_FFFF)
        // Edge: cleared before this carrier looks for work, so a waker whose
        // compare-and-swap of waking fails knows this search comes after its
        // push (WakeOne).
        (*st).waking.Store(0)
    }
    Spin(c)
    for i in 0..n {
        unsafe "a completed operation of a parked task" {
            op := done[i]
            // Edge: as in Reap, a count for PollNow only.
            _ = (*st).npending.Add(0xFFFF_FFFF)
            CompleteSched(c, raw.Of(&(*op).state), raw.PtrCast[u8, Task]((*op).waiter))
        }
    }
    // The timer this carrier woke for, on whichever carrier's heap it is
    // (ArmIdle took the least over all), is fired here, before DisarmIdle,
    // so DisarmIdle's hand-off sees only the timers still to come and does
    // not wake a second carrier for one this carrier is handling.
    _ = FireAll(c)
    DisarmIdle(armed)
}

// TrimSpare is the idle trim, on an idle carrier before it publishes
// itself idle: free task slots beyond rt.TrimKeep per class give their
// pages back (at most 32 per class per pass), then one empty heap segment
// beyond 8. Each trimmed slot was off both free stacks for a moment, so a
// spawn may have failed to find it and waited on the class's generation:
// SlotFreed per slot is the task end's wake that ends that wait (NewSlot's
// argument in spawn.mk). A task it readies is on c's queues before the
// AnyWork recheck below, so c does not go idle past it. With nothing over
// the bounds it costs three atomic loads.
// FlushSlots returns up to want cached ClassDefault slots from c's local
// free list to the region in one batch lock and bumps SlotFreed once.
FlushSlots(c: *Carrier, want: int) {
    unsafe "c is the calling thread's carrier; its slot_free list is owner-only" {
        head := (*c).slot_free
        if raw.IsNull(head) || want <= 0 {
            return
        }
        var tail := head
        var n := 1
        for n < want && !raw.IsNull((*tail).next_free) {
            tail = (*tail).next_free
            n += 1
        }
        (*c).slot_free = (*tail).next_free
        (*tail).next_free = raw.Null[rt.SlotHeader]()
        (*c).slot_count -= Trunc[u32](n)
        rt.FreeSlotBatch(rt.ClassDefault, head, tail, n)
        SlotFreedBatch(c, rt.ClassDefault, n)
    }
}

TrimSpare(c: *Carrier) {
    FlushSlots(c, 128)
    n := rt.TrimSlots(rt.ClassDefault)
    for _ in 0..n {
        SlotFreed(c, rt.ClassDefault)
    }
    m := rt.TrimSlots(rt.ClassLarge)
    for _ in 0..m {
        SlotFreed(c, rt.ClassLarge)
    }
    if rt.LazyClasses() {
        // StackSize.Bytes's classes, once any has a region.
        for k in 2..rt.SlotClasses {
            cl := Trunc[u8](k)
            b := rt.TrimSlots(cl)
            for _ in 0..b {
                SlotFreed(c, cl)
            }
        }
    }
    _ = rt.TrimHeap()
}

// IdleGate is a controlled-schedule hook for tests ("RT-Reap-Repost"): 0 off,
// 1 armed, 2 a carrier holds at the gate (published idle, queues rechecked,
// not yet in PollWait), 3 released. Off, it costs Idle one atomic load.
var IdleGate: raw.Atomic[u32] = raw.Atomic[u32].New(0)

// IdleGateHold holds the first carrier that reaches it while the gate is
// armed until a test releases it, or for 2 s at most (then it opens the
// gate itself), so a gate a test armed and never released delays one
// carrier once instead of holding it for good.
IdleGateHold() {
    // seq_cst: the holder's nidle Add comes before its Cas to 2, so a test
    // that sees 2 knows a carrier is counted idle (edge: gate -> test).
    if IdleGate.Load() == 1 && IdleGate.Cas(1, 2) {
        GateWait(raw.Of(&IdleGate))
    }
}

// GateWait waits while the gate word is 2 (held), at most GateMax; then a
// still-held gate is opened (2 to 0).
GateWait(g: *raw.Atomic[u32]) {
    t0 := sys.Now()
    unsafe "a static gate word" {
        // Edge: the test's release store (3 or 0) ends the hold.
        for (*g).Load() == 2 && sys.Now() - t0 < GateMax {
            sys.ThreadYield()
        }
        _ = (*g).Cas(2, 0)
    }
}

const GateMax: i64 = 2_000_000_000

// ReapGate is the controlled-schedule hook of test "RT-MN-ReapClear", the same protocol
// as IdleGate, for one carrier only (ReapGateWho, its index + 1): a Reap
// there that found waking 1 and no carrier idle holds just
// before it clears waking. Off, it costs that rare branch one atomic load.
var ReapGate: raw.Atomic[u32] = raw.Atomic[u32].New(0)
var ReapGateWho: raw.Atomic[u32] = raw.Atomic[u32].New(0)

ReapGateHold(c: *Carrier) {
    unsafe "c is the calling thread's carrier" {
        // Edge: as IdleGateHold's: the holder's nidle load comes before its
        // Cas to 2, so a test that sees 2 knows the load is done.
        if ReapGate.Load() == 1 && ReapGateWho.Load() == (*c).index + 1 && ReapGate.Cas(1, 2) {
            GateWait(raw.Of(&ReapGate))
        }
    }
}
