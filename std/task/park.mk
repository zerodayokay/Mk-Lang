// Why: a waker must not resume a task until its departing carrier saves its context.
// What: SwitchOut and RunAfter, Park and ParkUntil, Complete, and YieldNow.
// How: the task leaves an after-action for its carrier to perform after the switch.

import std.raw
import std.rt

// FinishHandoff releases the lock left by a direct task-to-task handoff
// (SwitchOut) once Switch has saved the departing task's context.
FinishHandoff(t: *Task) {
    unsafe "t is running on its carrier; a direct handoff leaves only AfterUnlock" {
        c := (*t).carrier
        if (*c).after.kind == AfterUnlock {
            w := (*c).after.word
            (*c).after.kind = AfterNone
            // Edge: release (order 2, as RunAfter) after Switch saved the
            // departing task's context; a waker acquiring this lock sees the
            // saved context before readying it.
            (*w).StoreOrd(0, 2)
        }
    }
}

// SwitchOut saves t and switches directly to c's runnext task on an
// unpinned AfterUnlock park (one Switch instead of task -> sys_ctx -> next),
// or resumes c's scheduler loop otherwise.
SwitchOut(t: *Task, kind: u32, word: *raw.Atomic[u32]) {
    Jitter(JitterPark)
    unsafe "t is the running task on carrier c" {
        c := (*t).carrier
        (*c).after = AfterSwitch(kind: kind, word: word, task: t)
        st := TS()
        if kind == AfterUnlock && raw.IsNull((*c).home) && (*st).jitter_seed == 0 && !(*c).spinning {
            tick := (*c).tick +% 1
            // Edge: hint reads; TimerTick falls through to FindRunnable for
            // timers, polling and fairness, and Swap(0) claims runnext.
            if tick % TimerTick != 0 && (*st).need_arm.LoadOrd(0) == 0 && (*c).runnext.LoadOrd(0) != 0 {
                // Edge: atomic claim of runnext, matching TakeNext.
                next_addr := (*c).runnext.Swap(0)
                if next_addr != 0 {
                    (*c).tick = tick
                    next := TaskAt(next_addr)
                    // Edge: atomic Swap enforces one-resume even under double-dequeue bugs.
                    if (*next).state.Swap(TaskRunning) != TaskRunnable {
                        rt.Fatal("scheduler: switched to a task that is not runnable")
                    }
                    (*HeaderOf(t)).carrier = rt.NoCarrier
                    (*c).current = next
                    (*next).carrier = c
                    (*HeaderOf(next)).carrier = Trunc[u16]((*c).index)
                    Switch(raw.Of(&(*t).ctx), raw.Of(&(*next).ctx))
                    FinishHandoff(t)
                    return
                }
            }
        }
        Switch(raw.Of(&(*t).ctx), raw.Of(&(*c).sys_ctx))
        FinishHandoff(t)
    }
}

// MarkParked records that the running task t is switching out. A plain
// store, not an atomic exchange (one xchg fewer per park on the switch
// path): no other thread reads or writes t.state from t's last resume
// until the switch's release, and every path that readies
// t comes after that release: RunAfter's unlock store,
// which the readier's lock acquire follows (Park, ParkKick); its
// handshake Cas, which the completer's Swap of the same word follows or
// which fails and readies t on this carrier (ParkUntil); or this carrier
// itself (YieldNow). Each is seq_cst, so the store happens before the
// readier's Swap in ReadyPut, which reads Parked (the one-resume invariant keeps
// its atomic check there and in Run).
MarkParked(t: *Task) {
    unsafe "the running task's own state word; see above for the order" {
        *raw.PtrCast[raw.Atomic[u32], u32](raw.Of(&(*t).state)) = TaskParked
    }
}

/// Park switches away; the scheduler releases unlock once the context is
/// saved, so a waker that takes the same lock never readies a half-saved
/// task. The lock is held across this one switch and the scheduler's
/// next few instructions only.
/// O(1) besides the time parked; allocates nothing.
export Park(unlock: *raw.Atomic[u32]) {
    t := MustCurrent()
    MarkParked(t)
    SwitchOut(t, AfterUnlock, unlock)
}

// ParkKick is Park, then OweArm once unlock is released (SleepFor's new
// earliest deadline): the kick's PollWake runs after the spinlock.
ParkKick(unlock: *raw.Atomic[u32]) {
    t := MustCurrent()
    MarkParked(t)
    SwitchOut(t, AfterUnlockKick, unlock)
}

/// ParkUntil switches away unless word reaches Done: the handshake.
/// O(1) besides the time parked; allocates nothing.
export ParkUntil(word: *raw.Atomic[u32]) {
    t := MustCurrent()
    MarkParked(t)
    SwitchOut(t, AfterHandshake, word)
}

/// Pending is a handshake word's value while its operation runs.
export const Pending: u32 = 1
/// Parked: the waiting task has switched out and waits for a Ready.
export const Parked: u32 = 2
/// Done: the operation completed; the waiter does not park, or is readied.
export const Done: u32 = 3

/// Complete sets word to Done; it readies t only if t had parked. From any
/// thread: a pool thread readies through the global queue.
/// O(1); allocates nothing beyond what Ready may start.
export Complete(word: *raw.Atomic[u32], t: *Task) {
    CompleteOn(Here(), word, t)
}

// CompleteOn is Complete on the calling thread's carrier c (or null).
CompleteOn(c: *Carrier, word: *raw.Atomic[u32], t: *Task) {
    if Completes(word) {
        ReadyOn(c, t)
    }
}

// CompleteSched is CompleteOn from c's scheduler loop (Reap, Idle).
CompleteSched(c: *Carrier, word: *raw.Atomic[u32], t: *Task) {
    if Completes(word) {
        ReadySched(c, t)
    }
}

// Completes marks the handshake word Done: true when the waiter had parked
// and the caller must ready it.
Completes(word: *raw.Atomic[u32]): bool {
    unsafe "word is a pending operation's handshake word" {
        // Edge: the operation's result was stored before this swap; the
        // waiter's RunAfter compare-and-swap (Pending to Parked) and this
        // swap are ordered in S, and exactly one of the two sees the other
        // and readies t.
        return (*word).Swap(Done) == Parked
    }
}

// YieldNow puts the running task at the global queue's tail, so
// every other runnable task, local and remote, may go first.
YieldNow() {
    t := MustCurrent()
    owed := HookLeave(t)
    MarkParked(t)
    SwitchOut(t, AfterYield, raw.Null[raw.Atomic[u32]]())
    HookEnter(t, owed)
}

// RunAfter finishes what the task asked for as it switched out, now
// that its context is saved.
RunAfter(c: *Carrier) {
    unsafe "the carrier's after-action, set by the task that switched out" {
        a := (*c).after
        (*c).after = AfterSwitch(kind: AfterNone, word: raw.Null[raw.Atomic[u32]](), task: raw.Null[Task]())
        if a.kind == AfterUnlock {
            // Edge: release (order 2, as rt.Unlock) after mk_switch saved
            // the context; a waker whose Cas takes this lock then sees the
            // saved context before it readies the task.
            (*a.word).StoreOrd(0, 2)
        } else if a.kind == AfterUnlockKick {
            // Edge: as AfterUnlock; OweArm's store is read by this
            // carrier only (timer.mk), so it needs no order after this one.
            (*a.word).StoreOrd(0, 2)
            OweArm()
        } else if a.kind == AfterHandshake {
            if !(*a.word).Cas(Pending, Parked) {
                ReadySched(c, a.task)
            }
        } else if a.kind == AfterYield {
            ReadyOn(raw.Null[Carrier](), a.task)
        } else if a.kind == AfterFree {
            h := HeaderOf(a.task)
            if Target.os == Os.Windows {
                // The slot keeps the StackLimit its pages really have.
                top := raw.Addr(h) + Trunc[uint](rt.HeaderBytes)
                (*h).committed_pages = Trunc[u16]((top - Trunc[uint]((*a.task).ctx[2])) / 4096)
            }
            class := (*h).class
            st := TS()
            // Edge: cache only when no waiter exists, TrimGate is unarmed, and
            // the region cap is large; recheck slot_waiters after caching to
            // close the Dekker window with NewSlot.
            if class == rt.ClassDefault && (*st).slot_cache && (*st).slot_waiters.Load() == 0 && rt.TrimGateGet() == 0 {
                (*h).state = 1
                (*h).next_free = (*c).slot_free
                (*c).slot_free = h
                (*c).slot_count += 1
                if (*st).slot_waiters.Load() != 0 || rt.TrimGateGet() != 0 {
                    FlushSlots(c, int((*c).slot_count))
                } else if (*c).slot_count >= 128 {
                    FlushSlots(c, 64)
                }
            } else {
                if class == rt.ClassDefault && (*c).slot_count > 0 {
                    FlushSlots(c, int((*c).slot_count))
                }
                rt.FreeSlot(h)
                SlotFreed(c, class)
            }
        }
    }
}
