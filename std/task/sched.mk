// Why: Mk's tasks are M:N: many tasks over a few carrier threads. Something
// must decide what each carrier runs next, hand tasks between carriers and
// put idle carriers to sleep without ever losing a wake-up.
//
// What: TaskState (the scheduler's process state), the scheduler loop Run
// and FindRunnable; Ready, WakeOne, Spin, Found, Linger, Idle, AnyWork and
// StealRounds; Reap, TrimSpare and the test gates IdleGate and ReapGate.
// Park, ParkUntil, Complete and YieldNow are in park.mk.
//
// How: the loop runs on each carrier's system stack. No preemption: a task
// runs until it suspends. The rest of this comment states who may touch
// what and the wake protocol that makes a lost wake-up impossible.
//
// Who may touch what. A carrier's deque bottom, its owner fields and its
// after-record belong to its own thread; ReadyOn(c, t) with c not null is
// called only on c's thread (by a task running on c, or by c's scheduler).
// Every other thread readies through the global queue (ReadyOn(null, t)).
//
// The wake protocol (no lost wake-up). Work is made visible first (a push
// on a deque, runnext or the global queue), then WakeOne reads nspinning and
// nidle. An idle carrier first raises nidle, then gives up spinning, then
// rechecks every queue (AnyWork), then blocks in PollWait. All of these are
// seq_cst, so in the total order S either the waker's read of nidle comes
// after the idler's increment (and the waker wakes someone), or the idler's
// recheck comes after the waker's push (and it sees the work); and a waker
// that saw nspinning > 0 relies on that spinner, whose decrement (Idle, or
// the last spinner's in Found) comes after the read in S and is followed by
// the same recheck (AnyWork), and in Found by a WakeOne when it sees work.
// One exception, by design: a carrier's own scheduler loop that readies a
// task into its empty runnext (ReadySched) wakes nobody, because that
// carrier takes it next. No lost wake: tests "RT-MN-PingPong",
// "RT-MN-WakeStorm" and "RT-MN-ForeignReady" in tests_mn.mk, "RT-Spread";
// no spurious wake: test "RT-File-SameCarrier" in tests_file.mk.
//
// A spinner may linger (Linger): after a fruitless search that saw another
// carrier's runnext occupied, it stays spinning and searches again, for at
// most LingerMax, so a busy pair's readies find nspinning > 0 and post no
// PollWake. That is the spinner case above, not
// an exception: the lingering carrier keeps nspinning raised only while it
// searches, and ends in Found or Idle like every spinner. Tests
// "RT-Linger-Ends" (it goes idle again) and "RT-NextWaits" (a task stuck
// in a runnext is still taken).

import std.raw
import std.rt
import std.sys

// TaskState is the scheduler's process state, in a page Start maps (the
// same reason as rt's RtState: no loan covers it).
struct TaskState {
    buckets: *Bucket
    // The timer heaps are the carriers' (Carrier.tlock, timer.mk).
    // armed_at is the earliest deadline some idle carrier will wake for
    // (Idle, ArmIdle), MaxDeadline when none: lowered only by ArmIdle's
    // compare-and-swap, raised back only by DisarmIdle's.
    armed_at: raw.Atomic[i64]
    // need_arm is 1 while a timer may be earlier than armed_at (OweArm).
    need_arm: raw.Atomic[u32]
    // Slot waits (spawn.mk NewSlot), on their own cache line apart from
    // WakeOne's nspinning/nidle and the global queue.
    pad_slot: [8]u64
    slot_gen: raw.Atomic[u32]
    slot_gen_large: raw.Atomic[u32]
    slot_waiters: raw.Atomic[u32]
    slot_waiters_large: raw.Atomic[u32]
    slot_cache: bool
    // Test counters (one atomic add each, on paths that just made a
    // system call or are about to wait): Idle returns from a PollWait
    // with no timeout, and NewSlot's slot waits.
    idle_unarmed: raw.Atomic[u64]
    slot_waits: raw.Atomic[u64]
    // WakeOne's PollWakes (one add beside the system call; "RT-Bench-PingPong"
    // prints it).
    wakes_posted: raw.Atomic[u64]
    // Linger's false returns: a spinner leaving for Idle (one add on a
    // path about to publish itself idle; "RT-Linger-Ends").
    linger_ends: raw.Atomic[u64]
    backend: u32
    // Carriers (carrier.mk), padded from slot waits and the global queue.
    pad_wake: [8]u64
    carriers: *Carrier
    want: raw.Atomic[u32]
    ncarriers: raw.Atomic[u32]
    nidle: raw.Atomic[u32]
    nspinning: raw.Atomic[u32]
    waking: raw.Atomic[u32]
    // npending counts poller operations submitted and not yet reaped.
    npending: raw.Atomic[u32]
    // The global queue (runq.mk), under the leaf lock glock, on its own line.
    pad_g: [8]u64
    glock: raw.Atomic[u32]
    ghead: *Task
    gtail: *Task
    glen: raw.Atomic[u32]
    // MK_TEST_SEED (jitter.mk); 0 turns the jitter points off.
    jitter_seed: u64
    jitter: raw.Atomic[u64]
    // The blocking pool (pool.mk): the job queue and the counts under
    // plock; psem counts posted jobs; pmax is MaxPoolThreads or
    // MK_TEST_POOL; pwaits counts the wait jobs (PoolJob.wait) queued or
    // in their call, which pmax does not bound.
    plock: raw.Atomic[u32]
    phead: *PoolJob
    ptail: *PoolJob
    pqueued: u32
    pwaiting: u32
    pthreads: u32
    pmax: u32
    pwaits: u32
    psem: sys.Sem
    // The jobs a cancel found in their call, a two-way list
    // through PoolJob.cnext and cprev under plock, and their count, the
    // lock-free gate of the re-interrupt (PoolReinterrupt); pint_at is the
    // clock of the last interrupt round. Each pool thread has a slot (bit
    // i % 64 of pslots[i / 64] taken, under plock; PoolSlots of them);
    // pthr[i] is its thread identity (the
    // HANDLE or tid ThreadStart returned), stored under plock by its starter
    // after the start, 0 until then.
    pcancel: *PoolJob
    pcancel_n: raw.Atomic[u32]
    pint_at: raw.Atomic[u64]
    // pqueued_n mirrors pqueued (stored under plock with each change), so a
    // spinning pool thread reads it without the lock (PoolSpin).
    pqueued_n: raw.Atomic[u32]
    // spin: this process may run on more than one CPU (sys.CpuCount() > 1,
    // set by Start before any task). The pool spin and the pool linger wait
    // for another CPU's thread, so on one CPU they only delay it.
    spin: bool
    // The pinned carriers (carrier.mk's pinned half): pinlock orders their
    // record claims against StartCarrier's (a leaf lock, cold paths only);
    // pins is their PinRec array, null until the first claim; bit i of
    // pinused is set while record i is a pinned carrier's.
    pinlock: raw.Atomic[u32]
    pins: *PinRec
    pinused: [4]u64
    // pin_ready holds PinReady's fn bytes and pin_spawn PinSpawn's, both
    // stored by the first claim (PinClaim) under pinlock, before any
    // pinned task or Io exists: the ready paths and SpawnAlloc call them through
    // these words, so the optimizer keeps the pinned half out of the
    // shared path (main's registers and inlining there). Atomics: a
    // shared spawn may load pin_spawn while a first claim stores it.
    pin_ready: raw.Atomic[u64]
    pin_spawn: raw.Atomic[u64]
    pslots: [16]u64
    pthr: [1024]u64
    // A library's runtime (lib.mk):
    // lib is set before any carrier but 0 starts, and then every carrier
    // thread is started joinable, its handle in cthr; stopping is set by
    // StopLibrary, after which an idle carrier ends its loop and a pool
    // thread its wait, each counted in nexited or pexited.
    lib: bool
    stopping: raw.Atomic[u32]
    nexited: raw.Atomic[u32]
    pexited: raw.Atomic[u32]
    cthr: [256]u64
}

struct TaskWords {
    state: raw.Atomic[u64]
}

var Sched: TaskWords = TaskWords(state: raw.Atomic[u64].New(0))

// TS is the TaskState page.
TS(): *TaskState {
    unsafe "the state word holds the page Start mapped" {
        // Edge: TaskInit stores the page address after the page's fields.
        return raw.FromAddr[TaskState](Trunc[uint](Sched.state.Load()))
    }
}

// Here is the calling thread's carrier when it runs a task (the task's
// carrier), else null: scheduler code passes its carrier explicitly, and a
// foreign thread has none.
Here(): *Carrier {
    t := Current()
    if raw.IsNull(t) {
        return raw.Null[Carrier]()
    }
    unsafe "the running task's carrier does not change while it runs" {
        return (*t).carrier
    }
}

/// Ready makes t runnable from any thread: on the calling task's
/// carrier, else on the global queue.
/// O(1): a full deque moves half its tasks to the global queue (Overflow),
/// and at most one system call wakes an idle carrier (PollWake); allocates
/// nothing, though it starts a carrier thread (an 8 MiB system stack) when
/// every started carrier is busy and fewer than CarrierCount exist.
export Ready(t: *Task) {
    ReadyOn(Here(), t)
}

// ReadyOn makes t runnable on c (its runnext slot, the previous one moving
// to the deque) or, with c null, on the global queue; then WakeOne. c is the
// calling thread's own carrier or null. A pinned task (Task.pin) takes the
// pinned half's cold call (PinReadyOf) and wakes no shared carrier; any
// other takes main's path (ReadyPut, then Jitter and WakeOne), with c
// taken as null on a pinned carrier (Shared).
ReadyOn(c: *Carrier, t: *Task) {
    unsafe "t's pin is written before its first ready and never again" {
        if !raw.IsNull((*t).pin) {
            _ = PinReadyOf(c, t)
            return
        }
    }
    _ = ReadyPut(Shared(c), t)
    Jitter(JitterReady)
    WakeOne()
}

// ReadySched is ReadyOn for c's own scheduler loop (on c's system stack:
// a completion it reaped, a timer it fired, a handshake it lost), which
// takes its runnext next. A task that lands in an empty runnext is work
// only c will want, and waking a carrier for it costs a PollWake and a
// spurious search (and, on iocp, moves the next completion to the woken
// carrier, the most recent waiter of the LIFO port). So a carrier is woken
// only when the swap displaced a task onto the deque, which others may
// steal (Tokio's LIFO-slot rule). No lost wake: the displaced task is
// pushed before WakeOne, as in ReadyOn, and the task in runnext is c's own
// next schedule; when the fairness step runs another task first, NextWaits
// wakes a carrier for it.
ReadySched(c: *Carrier, t: *Task) {
    unsafe "t's pin is written before its first ready and never again" {
        if !raw.IsNull((*t).pin) {
            _ = PinReadyOf(c, t)
            return
        }
    }
    if ReadyPut(Shared(c), t) {
        Jitter(JitterReady)
        WakeOne()
    }
}

// Shared is c for an unpinned task's ready: c itself on a shared carrier
// or null, and null on a pinned carrier (OnPinned), so the task goes to the
// global queue, as off any carrier: a pinned carrier never holds a shared
// task.
Shared(c: *Carrier): *Carrier {
    if OnPinned(c) {
        return raw.Null[Carrier]()
    }
    return c
}

// OnPinned is whether c is a pinned carrier (its home set). Plain read:
// home is written before c's thread started, and read here on c's own
// thread.
OnPinned(c: *Carrier): bool {
    unsafe "c is this thread's carrier or null" {
        return !raw.IsNull(c) && !raw.IsNull((*c).home)
    }
}

// PutPinned is PinReady's answer (PinReadyOf): the task is a pinned
// carrier's work (on c, the pinned carrier itself, or in another's inbox,
// whose carrier PinPut woke), so no shared carrier is woken for it.
const PutPinned: u32 = 2

// ReadyPut is main's ReadyPut, unchanged, for an unpinned task and a shared
// carrier or null (Shared): true when the task is visible to other
// carriers' searches as stealable work beyond c's next (it went to the
// global queue, or displaced a task from c's runnext onto the deque).
ReadyPut(c: *Carrier, t: *Task): bool {
    unsafe "t's context is saved (it is new or parked); c is this thread's carrier" {
        // Invariant (one resume): a task is readied once per park. The
        // swap is the state's only writer here, so a second Ready of the
        // same park sees Runnable and stops the process (no double wake).
        // Edge: the ready's claim: the context was saved before the park's
        // release (RunAfter), which this readier followed (its lock or the
        // handshake); a carrier that later reads Runnable resumes that context.
        prev := (*t).state.Swap(TaskRunnable)
        if prev != TaskParked && prev != TaskNew {
            rt.Fatal("scheduler: a task was readied twice")
        }
        if raw.IsNull(c) {
            GlobalPush(t)
            return true
        }
        // Edge: the swap publishes t to thieves (StealNext) and takes the
        // previous task out in one step.
        old := (*c).runnext.Swap(AddrOf(t))
        if old != 0 {
            PushLocal(c, TaskAt(old))
            return true
        }
    }
    return false
}

// ReadyPutNew is ReadyPut for a newly spawned task t that no other thread has
// seen yet, avoiding an atomic Swap on t.state before runnext/GlobalPush publishes it.
ReadyPutNew(c: *Carrier, t: *Task) {
    unsafe "t is a fresh unshared task; c is this thread's carrier" {
        // Edge: relaxed store published by runnext.Swap or GlobalPush below.
        (*t).state.StoreOrd(TaskRunnable, 0)
        if raw.IsNull(c) {
            GlobalPush(t)
            return
        }
        // Edge: the swap publishes t to thieves (StealNext) and takes the previous task.
        old := (*c).runnext.Swap(AddrOf(t))
        if old != 0 {
            PushLocal(c, TaskAt(old))
        }
    }
}

// ReadyClaim is the ready's state change, Runnable from Parked or New.
ReadyClaim(t: *Task) {
    unsafe "t's context is saved (it is new or parked)" {
        // Invariant (one resume): a task is readied once per park. The
        // swap is the state's only writer here, so a second Ready of the
        // same park sees Runnable and stops the process (no double wake).
        // Edge: the ready's claim: the context was saved before the park's
        // release (RunAfter), which this readier followed (its lock or the
        // handshake); a carrier that later reads Runnable resumes that context.
        prev := (*t).state.Swap(TaskRunnable)
        if prev != TaskParked && prev != TaskNew {
            rt.Fatal("scheduler: a task was readied twice")
        }
    }
}

// PinReadyOf readies pinned t through the word the first claim stored
// (TaskState.pin_ready: PinReady, carrier.mk), so the pinned half stays a
// call out of ReadyOn, ReadySched, Start1 and their callers. A task has a pin only
// after that claim, whose store and the carrier's thread start come before
// the pin's store.
PinReadyOf(c: *Carrier, t: *Task): u32 {
    unsafe "the TaskState page; w holds PinReady's fn bytes, stored by the first claim" {
        // Edge: the claim's Store (under pinlock, before any pinned task)
        // came before t's pin was written, which this thread read.
        var w := (*TS()).pin_ready.Load()
        f := raw.Read(raw.PtrCast[u64, fn(*Carrier, *Task): u32](raw.Of(&w)))
        return f(c, t)
    }
}

// WakeOne gets a carrier looking for the work just made visible (after
// Ready): nothing while some carrier spins (it will see the work, header);
// else a PollWake for an idle carrier, at most one in flight; else, with
// no carrier idle and fewer than CarrierCount started, a new carrier.
WakeOne() {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // Edge: the wake protocol's reads (header): after the push, nspinning
        // then nidle, all seq_cst against the idler's Add and recheck.
        if (*st).nspinning.Load() != 0 {
            return
        }
        if (*st).nidle.Load() != 0 {
            // Edge: waking is cleared by every carrier that returns from
            // PollWait (Idle) before it looks for work, so a failed
            // compare-and-swap here means a carrier will look after this
            // push; a successful one posts a wake that some PollWait, now or
            // next, returns for.
            if (*st).waking.Cas(0, 1) {
                // Edge: none; a test counter.
                _ = (*st).wakes_posted.Add(1)
                sys.PollWake()
            }
            return
        }
        // Edge: the Cas takes the spinning count for the new carrier (it starts
        // spinning), so two wakers do not both start one for one push.
        if (*st).ncarriers.Load() < (*st).want.Load() && (*st).nspinning.Cas(0, 1) {
            StartCarrier()
        }
    }
}

// Found ends a carrier's search: a spinning carrier stops spinning, and the
// last spinner wakes another carrier to look for more work (Go's
// resetspinning), so readied tasks keep reaching idle carriers.
Found(c: *Carrier) {
    unsafe "c is the calling thread's carrier" {
        (*c).linger_from = 0
        (*c).pool_wait = false
        st := TS()
        // The owed timer's hand-off (OweArm, test "RT-MN-TimerOwner"): this carrier is about to
        // run a task, so a timer owed an armed idle carrier gets one now.
        // Edge: need_arm is stored before this carrier's search ends
        // (RunAfter, DisarmIdle), so this load sees it; an idler raises
        // nidle before its ArmIdle clears need_arm and scans, so either
        // this nidle load sees the idler or the idler's ArmIdle sees the
        // timer.
        if (*st).need_arm.Load() != 0 && (*st).nidle.Load() != 0 && (*st).waking.Cas(0, 1) {
            sys.PollWake()
        }
        if (*c).spinning {
            (*c).spinning = false
            // The last spinner hands off only when work is still visible
            // (Go's resetspinning, narrowed): a waker that saw nspinning >
            // 0 after its push left that work to the spinners. Its push
            // and its read of nspinning come before this decrement in S
            // (or it read 0 and woke someone itself), so AnyWork, after the
            // decrement, sees the work and WakeOne runs; work pushed after
            // the decrement finds nspinning 0 and its own WakeOne wakes. A
            // carrier that found the only task (a completion it reaped)
            // wakes nobody (no spurious wake).
            // Edge: the last spinner's decrement precedes AnyWork in S (comment above).
            if (*TS()).nspinning.Add(0xFFFF_FFFF) == 1 && AnyWork() {
                WakeOne()
            }
        }
    }
}

// Spin makes c a spinning carrier.
Spin(c: *Carrier) {
    unsafe "c is the calling thread's carrier" {
        if !(*c).spinning {
            (*c).spinning = true
            // Edge: raised before the search; a waker that reads it relies on this
            // spinner's later decrement and recheck (Found, Idle).
            _ = (*TS()).nspinning.Add(1)
        }
    }
}

// TimerTick: every so many schedules the loop fires expired timers, reaps
// completions with a zero timeout and takes one task from the global queue
// or, failing that, the oldest task of its own deque, before it takes the
// next local task. The owner takes its deque LIFO, so
// without the oldest-task step a task at the top of a carrier's deque could
// wait for good behind a pair that keeps readying each other ("RT-MN-Fair").
const TimerTick: u32 = 61

// Reap readies the waiters of the operations PollWait completed within
// timeout (0: do not block, -1: forever) on c and returns how many there
// were. For c's scheduler loop, which runs what it readied (ReadySched): a call
// from a running task must not reap a completion it will not run, or that
// task waits in runnext with no carrier woken (the task call of test "RT-Reap-Repost"
// reaps only a wake: no operation is pending then).
Reap(c: *Carrier, timeout: i64, done: mut []*sys.Op): int {
    n := sys.PollWait(timeout, done)
    // A signal's PollWake may be the wake this poll consumed.
    SignalPoll(c)
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // A zero-timeout poll from a busy carrier never keeps a wake from
        // the carrier WakeOne posted it for, on any backend (std.sys's
        // contract, PollWait): Linux leaves it to the thread in the kernel
        // or the next blocking wait, and IOCP posts a wake packet its
        // Dequeue(0) took again, once per call (WakeKeep). So Reap re-posts
        // nothing while a carrier is idle (an IOCP re-post here would cost
        // one PostQueuedCompletionStatus per busy reap while waking was 1,
        // and one spurious idle return). Test "RT-Reap-Repost" drives the case with
        // the IdleGate schedule (an idle carrier held before PollWait while
        // this poll takes its wake): it checks std.sys's re-post. With no
        // carrier idle there is nobody to wake, and the next WakeOne may
        // post again (below).
        //
        // With no carrier idle, waking is cleared, and then nidle is read
        // again: a carrier that went idle between the first read and the
        // clear may have been passed over by a waker whose
        // compare-and-swap failed on the stale 1 (WakeOne), so it is woken
        // here. Edge (all seq_cst): that waker's push and its failed Cas
        // come before this Store(0) in S (the Cas read 1), and the idler's
        // nidle Add comes before its AnyWork; either the re-read below sees
        // the idler (and wakes it), or the idler's Add, hence its AnyWork,
        // comes after the re-read and so after the push, and it sees the
        // work. At most one extra wake per clear. Test "RT-MN-ReapClear".
        // Edge: these two loads are the first reads of the argument above.
        if (*st).waking.Load() == 1 && (*st).nidle.Load() == 0 {
            ReapGateHold(c)
            // Edge: the clear, then the re-read of nidle (comment above).
            (*st).waking.Store(0)
            if (*st).nidle.Load() != 0 && (*st).waking.Cas(0, 1) {
                sys.PollWake()
            }
        }
    }
    for i in 0..n {
        unsafe "a completed operation of a parked task" {
            op := done[i]
            // Edge: none needed beyond the count: npending only tells
            // PollNow whether a zero-timeout poll can find anything; the
            // completion itself is published by the handshake (Completes).
            _ = (*TS()).npending.Add(0xFFFF_FFFF)
            CompleteSched(c, raw.Of(&(*op).state), raw.PtrCast[u8, Task]((*op).waiter))
        }
    }
    return n
}

// PollNow reaps completions without blocking when any operation is pending.
PollNow(c: *Carrier): int {
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // Edge: none needed; an early out (Await raises npending before its
        // submission, so a completion to reap is counted).
        if (*TS()).npending.Load() == 0 {
            return 0
        }
    }
    var done: [64]*sys.Op = [raw.Null[sys.Op](); 64]
    return Reap(c, 0, mut done[..])
}

// GlobalBatch is how many tasks the global-queue step takes: len / carriers
// + 1, at most 128.
GlobalBatch(): u32 {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // Edge: none; a batch size, taken under glock later.
        var n := (*st).glen.Load() / (*st).ncarriers.Load() + 1
        if n > 128 {
            n = 128
        }
        return n
    }
}

// CanSteal limits the spinning carriers to half of the busy ones (Go's
// rule), so idle carriers do not burn every CPU looking for work.
CanSteal(c: *Carrier): bool {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        if (*c).spinning {
            return true
        }
        // Edge: none; a throttle (Go's rule), not part of the wake protocol.
        busy := (*st).ncarriers.Load() -% (*st).nidle.Load()
        return 2 * (*st).nspinning.Load() < busy
    }
}

// NextRand steps c's xorshift64 (Marsaglia 2003, 13/7/17).
NextRand(c: *Carrier): u64 {
    unsafe "c is the calling thread's carrier" {
        var x := (*c).rng
        x ^= x << 13
        x ^= x >> 7
        x ^= x << 17
        (*c).rng = x
        return x
    }
}

// StealNextWait is how long a thief waits between reading the other
// carriers' runnext slots and taking one (Go's runqsteal waits usleep(3)
// before it takes a running P's runnext). A carrier that is switching takes
// its own runnext within a few hundred nanoseconds, so a task still in the
// slot after the wait is one its carrier is not getting to (it runs a task
// that does not suspend), and a task taken sooner would move a ping-pong
// pair to another carrier on every steal. The wait is a clock spin, not
// ThreadYield, which returns at once while CPUs are idle.
const StealNextWait: i64 = 3_000

// StealRounds is the stealing step: four rounds over the other started carriers
// from a random start, taking half a victim's deque; then their runnext
// slots, each read once, and after one StealNextWait the first that still
// holds the task read. The thief waits once per search, not once per
// victim, so a search costs at most StealNextWait more at any carrier count.
StealRounds(c: *Carrier): *Task {
    n := int(ScanCarriers())
    if n <= 1 {
        return raw.Null[Task]()
    }
    unsafe "carrier records of started carriers" {
        me := int((*c).index)
        for _ in 0..4 {
            start := Trunc[int](NextRand(c) % Trunc[u64](n))
            for k in 0..n {
                i := (start + k) % n
                if i == me {
                    continue
                }
                t := StealHalf(c, CarrierAt(i))
                if !raw.IsNull(t) {
                    return t
                }
            }
        }
        // seen has a word per carrier; n is at most MaxCarriers
        // (ScanCarriers clamps it to want, which CarrierWant clamps), so
        // every index below is in bounds.
        var seen: [MaxCarriers]u64 = [0; MaxCarriers]
        var any := false
        for i in 0..n {
            if i != me {
                // Edge: none needed; StealNext's compare-and-swap transfers.
                seen[i] = (*CarrierAt(i)).runnext.Load()
                if seen[i] != 0 {
                    any = true
                }
            }
        }
        if !any {
            return raw.Null[Task]()
        }
        t0 := sys.Now()
        // Linger: this search saw a runnext occupied.
        (*c).next_seen_at = t0
        for sys.Now() - t0 < StealNextWait {
        }
        start := Trunc[int](NextRand(c) % Trunc[u64](n))
        for k in 0..n {
            i := (start + k) % n
            if i == me {
                continue
            }
            t := StealNext(CarrierAt(i), seen[i])
            if !raw.IsNull(t) {
                return t
            }
        }
    }
    return raw.Null[Task]()
}

// The spinner's linger. A spinning carrier whose search failed but saw
// another carrier's runnext occupied within LingerNs keeps spinning and
// searches again (after LingerPause) instead of going idle, for at most
// LingerMax from its first fruitless search. While it spins, nspinning is
// not 0, so a task that readies another into its carrier's runnext costs
// one load in WakeOne instead of a PollWake to an idle carrier (a system
// call on the readier's path) that then searches, finds nothing it may
// take, and goes idle for the next ready to wake it again. Counted on
// pingpong.mk at 8 carriers, 10,000,000 round trips (one development machine),
// without the linger and with it: PollWakes 505,644 and
// 15,864, runnext steals that moved the pair 83,230 and 16,635; switch 71
// and 54 ns in that counting build, 68 and 51 ns (medians of 7) without the
// counters. A sampled StealNextWait that drops a slot as soon as it changes
// cuts the steals further (to 4,089) but makes spawn.mk at 8 carriers 213
// ns against 199 with this plain wait, so the plain wait is the one used.
// The wake protocol holds: a lingering carrier is a spinner, which every
// search ends through Found or Idle (the header). LingerMax bounds
// the CPU a spinner spends beside a busy pair: after it, the carrier goes
// idle, and the next WakeOne wakes it (test "RT-Linger-Ends").
const LingerNs: i64 = 20_000
const LingerMax: i64 = 200_000
const LingerPause: i64 = 1_000

// The pool linger. A carrier whose task just parked in Blocking (pool_wait,
// set by BlockingIn) keeps spinning after a fruitless search for at most
// PoolLingerNs from its first one, so
// a quick pool job's ready (GlobalPush, then WakeOne from the pool thread)
// finds nspinning > 0 and posts no PollWake, and this carrier takes the
// task from the global queue on its next search. Without it each pool call
// sleeps and wakes a carrier (3.9 voluntary context switches per stat on
// WSL 2, one development machine). The protocol is the linger's: the carrier is a
// spinner and ends in Found or Idle. A job longer than the bound costs
// PoolLingerNs of one carrier's CPU once, then the path without it
// (the PoolLinger-Ends block checks that it stops spinning).
const PoolLingerNs: i64 = 50_000

// Linger is true when c, spinning after a fruitless search, searches
// again (above); it waits LingerPause first, so the search does not read
// the other carriers' lines in a tight loop.
Linger(c: *Carrier): bool {
    unsafe "c is the calling thread's carrier" {
        now := sys.Now()
        if (*c).linger_from == 0 {
            (*c).linger_from = now
        }
        if now - (*c).next_seen_at < LingerNs && now - (*c).linger_from < LingerMax ||
            (*c).pool_wait && now - (*c).linger_from < PoolLingerNs {
            // The pause ends early when the global queue has a task (a
            // pool job's ready lands there): the next search takes it.
            // Edge: none; a hint, the pop reads the queue under glock.
            for (*TS()).glen.Load() == 0 && sys.Now() - now < LingerPause {
            }
            return true
        }
        (*c).linger_from = 0
        (*c).pool_wait = false
        // Edge: none; a test counter.
        _ = (*TS()).linger_ends.Add(1)
        return false
    }
}

StartedCarriers(): u32 {
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // Edge: a carrier is counted before its thread starts (StartCarrier).
        return (*TS()).ncarriers.Load()
    }
}

// ScanCarriers is how many carrier records a scan of the started carriers
// reads (AnyWork, StealRounds, and the timer scans Earliest, FireOthers and
// FireRotate): the started ones, at most want. StartCarrier raises
// ncarriers before it compares with want, so a load may briefly read
// want + 1 (one StartCarrier runs at a time, under WakeOne's nspinning
// count). Record want is never initialized and may lie past the commit
// (CarriersInit commits whole 64 KiB units), and at want 256 it is past
// StealRounds' seen array.
ScanCarriers(): u32 {
    n := StartedCarriers()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // Edge: none needed; want only falls (StartCarrier, PinClaim), and
        // no shared carrier runs at or past it: a record there is either
        // never started or a pinned carrier's (carrier.mk's pinned half),
        // whose deque and heap only its own loop uses and no scan may read.
        // n is read before w, and every pinned index is at least want.
        w := (*TS()).want.Load()
        return if n > w { w } else { n }
    }
}

// AnyWork is the idle recheck: the global queue, then every
// started carrier's runnext and deque.
AnyWork(): bool {
    st := TS()
    unsafe "the TaskState page and the carrier records" {
        // Edge: the idle recheck: these loads follow the idler's
        // nidle Add in S, so a push whose waker read nidle 0 is seen here.
        if (*st).glen.Load() != 0 {
            return true
        }
        n := int(ScanCarriers())
        for i in 0..n {
            v := CarrierAt(i)
            if (*v).runnext.Load() != 0 || Size(v) != 0 {
                return true
            }
        }
    }
    return false
}

// FindRunnable is the search order: the fairness step, runnext and the local
// deque, the global queue, timers, a non-blocking poll, stealing, then the
// idle wait. It returns a runnable task that c now owns.
FindRunnable(c: *Carrier): *Task {
    for {
        var due := false
        unsafe "c's own fields, on its system stack" {
            (*c).tick += 1
            if (*c).tick >= TimerTick {
                (*c).tick = 0
                due = true
            }
        }
        if due {
            unsafe "c is the calling thread's carrier; flush cached slots if any spawner waits" {
                // Edge: observed Default slot demand or an armed trim gate
                // sends this carrier's cached slots back to the shared region.
                if (*c).slot_count > 0 && ((*TS()).slot_waiters.Load() != 0 || rt.TrimGateGet() != 0) {
                    FlushSlots(c, int((*c).slot_count))
                }
            }
            _ = FireTimersOn(c)
            // One other carrier's heap, in turn, so a carrier that
            // runs a task that never suspends has its timers fired by a
            // busy one.
            FireRotate(c)
            // A busy process (no carrier in PollWait, npending 0) still
            // sees a delivery within TimerTick schedules: a few loads.
            SignalPoll(c)
            // A cancelled pool job still in its call is interrupted
            // again. The gate is here, not only inside PoolReinterrupt: with
            // the bare call in this block a switch costs 58 ns instead of 29
            // ("RT-Bench-PingPong", 1 carrier, one development machine; the call is
            // cold, so this is a code-layout effect, not the call's own
            // cost).
            if PoolListed() {
                PoolReinterrupt()
            }
            _ = PollNow(c)
            g := GlobalPop(c, 1)
            if !raw.IsNull(g) {
                NextWaits(c)
                Found(c)
                return g
            }
            o := StealOne(c)
            if !raw.IsNull(o) {
                NextWaits(c)
                Found(c)
                return o
            }
        }
        var t := TakeNext(c)
        if raw.IsNull(t) {
            t = Take(c)
        }
        if raw.IsNull(t) {
            t = GlobalPop(c, GlobalBatch())
        }
        if !raw.IsNull(t) {
            Found(c)
            return t
        }
        if FireTimersOn(c) {
            continue
        }
        if PollNow(c) > 0 {
            continue
        }
        if CanSteal(c) {
            Spin(c)
            // Before it steals tasks, a carrier with nothing to run
            // fires the other heaps' due timers (Go's stealWork).
            if FireOthers(c) {
                continue
            }
            s := StealRounds(c)
            if !raw.IsNull(s) {
                Found(c)
                return s
            }
            if Linger(c) {
                continue
            }
        }
        Idle(c)
        // A library's stop (lib.mk): an idle carrier woken after it ends
        // its loop. Only after Idle, so a busy carrier pays nothing.
        unsafe "the TaskState page" {
            // Edge: StopLibrary stores stopping before the wakes it posts.
            if (*TS()).stopping.Load() != 0 {
                return raw.Null[Task]()
            }
        }
    }
}

// NextWaits: c runs another task before its runnext (the fairness step),
// so a task that ReadySched left there without a wake may wait behind a
// task that does not suspend for long; wake a carrier that can steal it
// (StealRounds' last round). Without a task in runnext, one load.
NextWaits(c: *Carrier) {
    unsafe "c is the calling thread's carrier" {
        // Edge: none needed; a task left there was published by ReadyPut's Swap.
        if (*c).runnext.Load() != 0 {
            WakeOne()
        }
    }
}

// Idle is the last step. c publishes itself idle, stops spinning, rechecks
// every queue (the header's protocol), and only then waits in the poller:
// with the earliest timer's deadline when no other idle carrier waits for
// one as early (ArmIdle), else until a wake, a completion or a signal.
// Every idle carrier may be inside PollWait at once (std.sys's contract).
// It returns spinning, to look for work.
Idle(c: *Carrier) {
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
    n := sys.PollWait(wait, mut done[..])
    SignalPoll(c)
    if PoolListed() {
        PoolReinterrupt()
    }
    unsafe "the TaskState page Start mapped, which lives until exit" {
        if timeout < 0 {
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

// Run is the scheduler loop on carrier c's system stack. It never
// returns: the process ends in Finish, Exit, a panic or a fault.
Run(c: *Carrier) {
    for {
        t := FindRunnable(c)
        if raw.IsNull(t) {
            return // a library's stop (FindRunnable)
        }
        unsafe "t is runnable, its context saved, and now owned by c" {
            h := HeaderOf(t)
            (*c).current = t
            (*t).carrier = c
            // Invariant (one resume): only a runnable task is resumed;
            // a task resumed twice (two carriers holding it) stops here.
            // Edge: atomic Swap enforces one-resume across carriers.
            if (*t).state.Swap(TaskRunning) != TaskRunnable {
                rt.Fatal("scheduler: switched to a task that is not runnable")
            }
            (*h).carrier = Trunc[u16]((*c).index)
            Switch(raw.Of(&(*c).sys_ctx), raw.Of(&(*t).ctx))
            cur := (*c).current
            (*HeaderOf(cur)).carrier = rt.NoCarrier
            (*c).current = raw.Null[Task]()
        }
        RunAfter(c)
    }
}
