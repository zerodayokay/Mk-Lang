// Why: a task needs a record (context, run state, waits, scopes) that costs
// no allocation at spawn, and the running task must be found without a
// thread-local.
//
// What: the run states TaskNew to TaskDead, WaitRec, TimerRec, Task, and
// TaskOf, HeaderOf, Current and MustCurrent.
//
// How: the record lies at the top of the task's own stack slot, 384 bytes
// below the slot header, so a spawn allocates no record; Current finds the
// slot from the stack pointer (rt.CurrentSlot).

import std.core
import std.raw
import std.rt
import std.sys

/// TaskNew: the task is made and not yet readied.
export const TaskNew: u32 = 0
/// TaskRunnable: the task is on a run queue.
export const TaskRunnable: u32 = 1
/// TaskRunning: a carrier runs the task.
export const TaskRunning: u32 = 2
/// TaskParked: the task waits for a Ready.
export const TaskParked: u32 = 3
/// TaskDead: the task's function returned; its slot is being freed.
export const TaskDead: u32 = 4

/// WaitRec is a task's address wait (wait.mk). state is a plain word: every
/// read and write of it is under the bucket lock of addr (WaitWord,
/// CancelWordWait, WakeWordOn), so it needs no atomic exchange (an xchg per
/// write was two per hand-off on the switch path).
export struct WaitRec {
    addr: *u32
    state: u32
    result: core.SysErr
}

/// TimerRec is a task's place in a carrier's timer heap (timer.mk):
/// its deadline under the heap's lock; its index, the heap's carrier in the
/// top 8 bits and the position below, written under that lock and read for
/// its heap bits without it; and its state word.
export struct TimerRec {
    deadline: i64
    index: raw.Atomic[u32]
    state: raw.Atomic[u32]
}

/// Task is a task's record: its context, run state, scopes, waits and hook
/// words. It lies just below the slot header at the top of the task's own
/// slot (TaskOf: 384 bytes below it), so a spawn
/// allocates no record.
export struct Task {
    ctx: Context
    state: raw.Atomic[u32]
    flags: u32
    // pin is the pinned carrier (runtime.Dedicated, runtime.Thread) this
    // task runs on, and only there; null for a task any shared carrier may
    // run. Set before the task is first readied (SpawnAlloc, PinRoot),
    // never changed after (ReadyPut reads it on every ready).
    pin: *Carrier
    home: *ScopeRec
    inner: *ScopeRec
    link: *Task
    carrier: *Carrier
    thunk: fn(*Io, *u8, *u8): bool
    io: Io
    block: *u8
    err: *u8
    wait: WaitRec
    timer: TimerRec
    op: *sys.Op
    job: *u8
    // The cancellable wait this task is in: its kind (WaitKind*),
    // the scope whose blocked list holds it, and the list links; all under
    // that scope's lock.
    wait_kind: u32
    // 1 once PollSubmit returned true for op, until Await clears op: a
    // cancel calls PollCancel only then, never during the submission
    // (CancelWait, Await).
    op_live: raw.Atomic[u32]
    blocked_in: *ScopeRec
    blocked_next: *Task
    blocked_prev: *Task
    // The wait bucket's back link (wait.mk): link is the next
    // task of the bucket's FIFO, wprev the previous, so a cancel unlinks
    // in O(1).
    wprev: *Task
    // A cancel walk that found this task's operation submitted (op_live)
    // pins it (1) under the scope lock and calls PollCancel after every
    // scope lock is released, through cancel_next; Await does
    // not return, so op's frame stays live, until the pin is 0 again.
    cancel_pin: raw.Atomic[u32]
    cancel_next: *Task
    // The hooks (hooks.mk): note is the hooked Io this task
    // spawned into since its last leaf (NoteBit in flags), and note2 a
    // second such Io, owed before note (Note2Bit); hold is the
    // scope whose Join or limit gate this task waits in (GateBit: the
    // gate), null otherwise. All written only by the task itself.
    note: Io
    note2: Io
    hold: *ScopeRec
}

const TaskOffset: int = 384
const MainFlag: u32 = 1

// TaskOf is the task record under a slot header.
TaskOf(h: *rt.SlotHeader): *Task {
    unsafe "h is a task slot's header; the record is just below it" {
        return raw.FromAddr[Task](raw.Addr(h) - Trunc[uint](TaskOffset))
    }
}

// HeaderOf is the slot header above a task record.
HeaderOf(t: *Task): *rt.SlotHeader {
    unsafe "t is a task record; its slot header is just above it" {
        return raw.FromAddr[rt.SlotHeader](raw.Addr(t) + Trunc[uint](TaskOffset))
    }
}

/// Current is the running task, or null off a task stack.
/// O(1): rt.CurrentSlot, two compares per region on the stack pointer;
/// allocates nothing.
export Current(): *Task {
    h := rt.CurrentSlot()
    return if raw.IsNull(h) { raw.Null[Task]() } else { TaskOf(h) }
}

// MustCurrent is Current for an operation that suspends: off a task stack
// that is panic 16.
MustCurrent(): *Task {
    t := Current()
    if raw.IsNull(t) {
        rt.Report(rt.PanicSuspendInCallback, raw.Null[rt.Site](), rt.PanicText(rt.PanicSuspendInCallback), "")
    }
    return t
}
