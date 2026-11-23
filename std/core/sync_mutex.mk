// Why: tasks share mutable state through Mutex; the lock must be cheap when
// uncontended, park waiters instead of spinning, wake exactly the waiter it
// means, and bound how long any waiter is passed over.
//
// What: Mutex[T] (New, Lock, Into) and Guard[T] (Ref, Mut, Store, Unlock);
// the queue lock's slow paths (MutexLockSlow, MutexUnlockSlow and their
// helpers), which Chan's lock (sync_chan.mk) shares; the cancellation point
// CancelPoint; and the three-state lock word (LockWordSlow, ReleaseWord)
// that io_stream.mk's stream lock uses.
//
// How: a lock word then T inline. The word is a queue lock in one u64 with
// Go's starvation handoff; the details, invariants and memory ordering
// follow.

// Algorithm: a queue lock in one word, after WebKit's WordLock (Pizlo,
// "Locking in WebKit", 2016), with the fairness of Go's sync.Mutex
// (starvation mode, Go 1.9). The word is a u64: bit 0 locked (L), bit 1 the
// queue bit (K, held for a constant number of steps, never across a
// suspension), bit 2 woken (W, a woken waiter is on its way), bit 3 the
// first cell is starving (S, kept under K with the queue bits), and
// the other bits the address of the first cell of a FIFO of
// parked waiters (cells are 16-aligned). Each
// waiter is a MutexWaiter on its own stack (mem_chan.mk), parked with
// IoTable.wait on the cell's own state word, so a wake goes to exactly the
// task meant (the three-state word of Drepper's "Futexes Are Tricky"
// mutex 3 cannot tell its waiters apart, so it can neither hand the lock
// over nor stop a second wake).
//
//   Lock: a Cas setting L when the word read has L clear, whatever its
//   other bits; else, while the lock is held and nobody is queued,
//   spin on loads (parking_lot's rule: spinning is useless behind parked
//   waiters); then take K, append the cell (a waiter woken before goes to
//   the front, Go's LIFO requeue), release K and park.
//   Unlock: one Cas clearing L when K is clear and nobody is queued, or W
//   is set and S is clear (the cases in which an unlock under K only
//   clears L). Mutex.Lock and MutexUnlockWord make each first step inline,
//   so neither calls out while a woken waiter (W) is on its way. Else
//   take K: when the first cell is starving (it has waited 1 ms or more,
//   Go's starvationThresholdNs, and lost the lock once after a wake), pop it
//   and hand it the lock with L still set (state 2); when W is set, a woken
//   waiter will retry, so only clear L (Go's mutexWoken); else pop the first
//   cell, set W, set its state 1 (retry), clear L. The wake runs after K is
//   released. A free lock may be taken by any task (barging), which is the
//   throughput of Go's normal mode, and the handoff bounds the wait (below).
//
// Invariants of the queue lock (tests_sync.mk tests each):
//   Q1 Every field of a queued cell but state, and the word's queue bits,
//      change only under K; L does not clear while K is held, except by the
//      holder of the lock, who holds K at that moment.
//   Q2 A cell's state goes from 0 to 1 or 2 once per queueing, under K, by
//      the task that popped it, after the pop; nothing reads the cell after
//      that store (its owner may return at once). Its owner leaves the wait
//      only on a non-zero state, or, after a Canceled wait, by unlinking the
//      cell under K.
//   Q3 No lost wakeup: whenever the queue is not empty and L is clear, W is
//      set, and the woken task that W stands for will clear it and then take
//      the lock or queue again; a woken waiter that leaves on Canceled hands
//      that duty on (MutexWakeIfFree), and one that was handed the lock
//      unlocks it.
//   Q4 Bounded wait: a waiter that has waited 1 ms and lost the lock after a
//      wake goes to the front marked starving, and the next unlock hands it
//      the lock, so a waiter is passed over at most once after 1 ms, behind
//      the starving waiters queued in front of it.
//
// Memory ordering. raw.Atomic is seq_cst only; each site names the
// edge it needs, and acquire on lock and release on unlock would do for all.

import std.raw

/// A lock around a value of T. Lock waits for it and returns a Guard, the
/// only way to reach the value; Unlock on the Guard releases it. Waiters
/// queue in FIFO order, but a free lock goes to whichever task takes it
/// first. A waiter that has waited 1 ms and then loses the lock once more
/// goes to the front, and the next Unlock hands the lock to it directly.
/// Share when T is Send, so tasks share a &Mutex. There is no condition
/// variable: to wait for a condition, unlock, wait on a sync.Notify, and
/// lock again.
export struct Mutex[T] {
    word: raw.Atomic[u64]
    value: T
}

/// The held lock of a Mutex, giving access to its value. Linear: Unlock
/// ends it, so the lock cannot be forgotten.
export linear view struct Guard[T] {
    m: *Mutex[T]
    io: Io
    marker: [0]mut T
}

unsafe impl[T: Send] Send for Mutex[T] {}
unsafe impl[T: Send] Share for Mutex[T] {}

const MutexL: u64 = 1
const MutexK: u64 = 2
const MutexW: u64 = 4
// MutexS: the first queued cell is starving, set and cleared with the queue
// bits under K (MutexReleaseK), so an unlock can tell without reading a
// cell (MutexUnlockSlow). Cells are 16-aligned (mem_chan.mk).
const MutexS: u64 = 8
const MutexQ: u64 = 0xFFFF_FFFF_FFFF_FFF0

// MutexStarveNanos is Go's starvationThresholdNs.
const MutexStarveNanos: i64 = 1_000_000

// MutexStarveWakes is the wake count after which a waiter of a clockless
// lock (MutexLockSlow's clock false) starves: woken twice means it lost the
// lock at least once after a wake, Go's second condition.
const MutexStarveWakes = 2

// MutexSpins bounds the read-only spin before parking (Rust std's 100).
const MutexSpins = 100

/// An unlocked mutex holding x. O(1); no allocation, so it can initialize a
/// `var` global.
export Mutex[T].New(x: T): Mutex[T] {
    return Mutex[T](word: raw.Atomic[u64].New(0), value: x)
}

// MutexWordAt and MutexValueAt are the fields of the Mutex at m, for a
// Guard, which holds the mutex by raw pointer; nothing is
// dereferenced here.
MutexWordAt[T](m: *Mutex[T]): &raw.Atomic[u64] {
    unsafe "m is the live Mutex a Guard's loan keeps alive" {
        return &(*m).word
    }
}

MutexValueAt[T](m: *Mutex[T]): *T {
    unsafe "field address; nothing is dereferenced" {
        return raw.Of(&(*m).value)
    }
}

/// Waits for the lock and returns its Guard. Canceled when the scope is
/// canceled, and then the lock is not held. O(1) uncontended; allocates nothing
/// (a waiter's queue cell is on its own stack).
///
/// Lock checks for cancellation first: in a cancelled scope it returns
/// Canceled even when the lock is free, unless io's NoCancel mask covers
/// that scope (a scope opened from the masked Io is not covered). A
/// cancelled scope stays cancelled, so every later Lock in it returns
/// Canceled too, until the scope's block exits. A free lock is taken by
/// one compare-and-swap; otherwise Lock waits. A wait that is cancelled
/// returns Canceled without the lock.
export Mutex[T].Lock(&self, io: Io): Canceled!Guard[T] {
    // CancelPoint's fast test inline (CancelClear), so a Lock while no scope
    // is cancelled pays no call for its cancellation point; otherwise the
    // full check, which makes the same test first. The chain test
    // (ChainState) stays in the full check: inline here it made Lock too
    // large for LLVM to inline into a caller's loop (mutex-uncontended).
    if !CancelClear(io) {
        try CancelPoint(io)
    }
    w := &self.word
    // MutexLockSlow's first iteration, inline: the same Load, the same test
    // of L alone and the same Cas(s, s | L), so a free lock is taken
    // whatever W, K, S and the queue bits say (barging, as the slow path's
    // first iteration always did), and no state transition is added. Cas(0,
    // L) alone failed on every word with W set, and a woken waiter's W stays
    // set until its carrier runs it, which sent every Lock meanwhile through
    // the call. A failed Cas starts MutexLockSlow, whose loop loads again.
    // The Load orders nothing. Acquire edge: a successful Cas reads the word
    // the last unlock's read-modify-write left (every write of the word is
    // a read-modify-write, so this one is in its release sequence), so that
    // critical section happens before this.
    s := w.Load()
    if (s & MutexL) != 0 || !w.Cas(s, s | MutexL) {
        try MutexLockSlow(w, io, true)
    }
    return Guard[T](m: raw.Of(self), io: io, marker: [])
}

// CancelPoint returns Canceled when io's task is in a cancelled scope that
// io's mask does not cover: IoTable.sleep with 0
// nanoseconds checks and returns at once. Its cost is one
// indirect call and one load of the task's scope flag at any nesting depth,
// not a walk of the scope chain (TestSTDSYNC_LockCostIndependentOfDepth).
//
// The entry decides for a NoCancel Io too: a scope opened from the
// masked Io is past the mask and its own Cancel cancels the operations
// under it (task.IsCancelled, MaskedPast), which a test of bit 0 here
// cannot see. Only Canceled is a cancellation: any other failure of the
// check (InertIo's and rt.DirectTable's Other) is not, and the operation
// goes on, unlike Io.Sleep, which reads it as Canceled.
//
// The fast path: for an Io of the native table (NativeInit), masked or
// not, while no scope of the process has its cancel flag set (Native.live
// is 0), the answer is "not cancelled" with two loads and no call. The
// proof is CancelLiveAdd's comment. So a native cleanup Io pays a call
// only while some scope is cancelled; an Io of another table (a Sim's, a
// Recorder's) pays one at every check, and its entry answers: a dead Sim
// run's (crashed, restarted or freed) answers None to a masked check, since
// a dead run is not a scope cancellation, and Canceled to an unmasked one
// (SimSleep, TSleep).
CancelPoint(io: Io): Canceled!void {
    if CancelClear(io) {
        return
    }
    if IoSleep(io, 0).err == SysErr.Canceled {
        fail Canceled
    }
}

/// True when io is the native one and no live scope is cancelled, so a
/// cancellation point would return at once; false decides nothing, and
/// the caller makes the full check (CancelPoint), which makes this test
/// first again. Two loads and no call, small enough to inline before the
/// full check at every site that tests it (Mutex.Lock, Chan.Send and
/// Recv, std.sync's waits). O(1); allocates nothing.
export CancelClear(io: Io): bool {
    // Edge: both loads are the fast path's (CancelLiveAdd): a 0 in live is
    // read before, in the seq_cst order, the Add that precedes any flag
    // store still to come in a live scope, so "not cancelled" is the
    // answer the full check would give at this load.
    // live is read only for a native Io (IsNative, called first and by
    // name), so a run whose Io is not std.task's, such as the soundness
    // oracle's inert one (internal/eval's tgHostCall answers IsNative
    // false), reads no std global here.
    return IsNative(io) && Native.live.Load() == 0
}

/// True when no live scope is cancelled, so no task is, as of this read.
/// Internal to std, for std.task's own entries (task.Stop), whose Io is
/// always the native one. O(1).
export CancelQuiet(): bool {
    // Edge: as CancelPoint's load of live.
    return Native.live.Load() == 0
}

/// Adds n to the count of live scopes that are cancelled. Internal to std:
/// std.task adds 1 before it sets a scope's cancel flag (and takes it back
/// when the flag was already set), and subtracts 1 when a cancelled scope
/// ends, no task can be inside it and no cancel walk can reach it. While
/// the count is 0 no live scope is cancelled, so a cancellation check on the
/// native Io answers "not cancelled" with two loads and no call. A Sim's Io
/// does not use this count, since its cancellations have other causes.
/// O(1).
export CancelLiveAdd(n: i64) {
    // Edge: the counting edge above (seq_cst Add before the flag's store).
    // So while a task is inside a scope whose flag is 1 (the scope has not
    // ended), that scope's +1 is in live; and a load of live that reads 0
    // comes, in the total order S of seq_cst operations, before the +1 of
    // every flag store still to come in a live scope, so before that store
    // too. Hence CancelPoint's fast "not cancelled" is what the full check
    // would have read at the same point of S. Not correct for a table whose
    // cancellation has other causes (a Sim's ended run); only the
    // registered table takes the fast path.
    _ = Native.live.Add(n)
}

// MutexLockSlow is Lock's contended path; see the file comment. clock
// chooses the starvation rule (Q4): true, Go's, a waiter starves once it
// has waited 1 ms (MutexStarveNanos) and lost the lock after a wake, which
// costs one IoTable.now when it first queues and one per wake; false, a
// waiter starves once it is woken MutexStarveWakes times without getting
// the lock, with no clock read. Chan's lock (sync_chan.mk) passes false: its
// hold time is a few steps, so 1 ms is thousands of passes, and the clock
// reads cost 10% of a contended channel.
MutexLockSlow(w: &raw.Atomic[u64], io: Io, clock: bool): Canceled!void {
    var cell := MutexWaiter(next: raw.Null[MutexWaiter](), prev: raw.Null[MutexWaiter](),
        tail: raw.Null[MutexWaiter](), state: raw.Atomic[u32].New(0), io: io, starving: false)
    c := MWOf(mut cell)
    // The word keeps L, K, W and S in the low 4 bits of a cell's address
    // (MutexQ), so a cell that is not 16-aligned would corrupt the queue;
    // align(16) on MutexWaiter asks the frame for it, and this test turns
    // a misaligned cell into a panic instead of a corrupted queue (one test
    // on the parking path). Unproven as a detector: a copy of std without
    // align(16) did not trip it (its frames happened to be 16-aligned); the
    // layout fact itself is pinned by tests_sync.mk's block "MutexWaiter is
    // 16-aligned, leaving the word's four flag bits free".
    if (MWAddr(c) & 15) != 0 {
        raw.Fail(8, "Mutex: a waiter cell is not 16-aligned")
    }
    var spins := MutexSpins
    var start: i64 = 0
    // timed, not start == 0, says the clock was read: IoTable.now promises
    // no particular origin, and a clock that reads 0 (an inert or test table)
    // would otherwise never let this task queue.
    var timed := false
    var woken := false
    var wakes := 0
    for {
        // A load orders nothing: the Cas that acts on it does.
        s := w.Load()
        if (s & MutexL) == 0 {
            // Acquire edge, as Lock's Cas: it reads the unlock's write.
            if w.Cas(s, s | MutexL) {
                return
            }
            continue
        }
        if spins > 0 && (s & MutexQ) == 0 {
            spins -= 1
            continue
        }
        if (s & MutexK) != 0 {
            MutexSpinK(w, io)
            continue
        }
        if clock && !timed {
            // The clock is read before K is taken: nothing that enters the
            // runtime runs under K.
            start = MutexNow(io)
            timed = true
            continue
        }
        // Acquire edge for the queue: the Cas that sets K reads the write
        // that last cleared it, so the cells as the last K holder left them
        // are visible. L is set (it was in s) and stays set while K is held
        // (Q1).
        if !w.Cas(s, s | MutexK) {
            continue
        }
        h := MWAt(s & MutexQ)
        nh := if woken {
            MQPushFront(h, c)
        } else {
            MQAppend(h, c)
        }
        MutexReleaseK(w, nh, false)
        for {
            r := IoWait(io, AtomicWord(MWStateAt(c)), 0)
            // Acquire edge: a load that reads 1 or 2 reads the popper's
            // store, made under K after everything the popper's critical
            // section wrote (2: the lock's whole history so far).
            st := MWStateAt(c).Load()
            if st == 2 {
                return
            }
            if st == 1 {
                break
            }
            if r.err == SysErr.Canceled {
                MutexCancel(w, c, io)
                fail Canceled
            }
        }
        // Woken to retry: this task is the woken waiter W stands for (Q3).
        MutexClearW(w)
        MWStateAt(c).Store(0) // its own cell, off the queue: no other reader
        woken = true
        wakes += 1
        if clock {
            MWSetStarving(c, MutexNow(io) - start >= MutexStarveNanos)
        } else {
            MWSetStarving(c, wakes >= MutexStarveWakes)
        }
        spins = MutexSpins
    }
}

// MutexNow is the monotonic clock in nanoseconds (IoTable.now), read only
// on the parking path: once when a task first queues, once per wake.
MutexNow(io: Io): i64 {
    return IoNow(IoMasked(io)).value
}

// MutexSpinK waits for the queue bit, held by another task for a constant
// number of steps; after 64 loads it yields its carrier, so a holder whose
// thread the OS preempted can run.
MutexSpinK(w: &raw.Atomic[u64], io: Io) {
    var n := 64
    for {
        // A load orders nothing here: the Cas that takes K does.
        if (w.Load() & MutexK) == 0 {
            return
        }
        n -= 1
        if n == 0 {
            _ = IoYield(IoMasked(io))
            n = 64
        }
    }
}

// MutexTakeK takes the queue bit whatever L is, and returns the word with K
// set. Acquire edge: as MutexLockSlow's Cas that sets K.
MutexTakeK(w: &raw.Atomic[u64], io: Io): u64 {
    for {
        s := w.Load()
        if (s & MutexK) != 0 {
            MutexSpinK(w, io)
        } else if w.Cas(s, s | MutexK) {
            return s | MutexK
        }
    }
}

// MutexReleaseK releases the queue bit with head h, and clears L when
// unlock is true. S is set from h's starving flag, read here under K (a
// queued cell's fields change only under K, and a cell sets its own flag
// only while it is off the queue, Q1), so S always describes the head the
// word names. Release edge: the Cas that clears K publishes the queue
// edits made under it to the next task that sets K. W may change meanwhile
// (a woken waiter clears it), hence the loop.
MutexReleaseK(w: &raw.Atomic[u64], h: *MutexWaiter, unlock: bool) {
    clear := if unlock {
        MutexK | MutexQ | MutexS | MutexL
    } else {
        MutexK | MutexQ | MutexS
    }
    head := if !raw.IsNull(h) && MWStarving(h) {
        MWAddr(h) | MutexS
    } else {
        MWAddr(h)
    }
    for {
        s := w.Load()
        if w.Cas(s, (s & ~clear) | head) {
            return
        }
    }
}

// MutexClearW clears W; only the woken task W stands for calls it.
// Ordering: none needed beyond the Cas's own atomicity; the decisions that
// read W take K or L, whose edges order them.
MutexClearW(w: &raw.Atomic[u64]) {
    for {
        s := w.Load()
        if (s & MutexW) == 0 || w.Cas(s, s & ~MutexW) {
            return
        }
    }
}

// MutexHand sets a popped cell's state under K (Q2) and returns its wake,
// for after K is released, as Hand in sync_chan.mk. The cell must be off
// the queue already: once the store is visible its owner may return (a
// handed lock, or a free one it takes after a wake) and its frame, the
// cell, be gone, so nothing reads the cell after the store. Release edge: the store
// publishes everything before it to the owner's Load that reads it.
MutexHand(c: *MutexWaiter, state: u32): ChanWake {
    k := ChanWake(io: MWIo(c).NoCancel(), addr: AtomicWord(MWStateAt(c)))
    MWStateAt(c).Store(state)
    return k
}

// MutexCancel: c's wait returned Canceled with state 0 read. Under K the
// state is decisive (Q2): 0, still queued, so unlink it; 2, handed the lock,
// so unlock it; 1, woken, so pass the woken duty on (Q3).
MutexCancel(w: &raw.Atomic[u64], c: *MutexWaiter, io: Io) {
    s := MutexTakeK(w, io)
    h := MWAt(s & MutexQ)
    // Acquire edge: under K, a 1 or 2 here was stored under K before.
    st := MWStateAt(c).Load()
    if st == 0 {
        MutexReleaseK(w, MQUnlink(h, c), false)
        return
    }
    MutexReleaseK(w, h, false)
    if st == 2 {
        MutexUnlockWord(w, io)
        return
    }
    MutexClearW(w)
    MutexWakeIfFree(w, io)
}

// MutexWakeIfFree wakes the first waiter when the lock is free, somebody is
// queued and no waiter is woken: the duty of a woken waiter that leaves
// (Q3). A barger may take the lock meanwhile; then its unlock wakes.
MutexWakeIfFree(w: &raw.Atomic[u64], io: Io) {
    // A load orders nothing: the decision is taken again under K.
    s0 := w.Load()
    if (s0 & MutexL) != 0 || (s0 & MutexQ) == 0 || (s0 & MutexW) != 0 {
        return
    }
    s := MutexTakeK(w, io)
    h := MWAt(s & MutexQ)
    if raw.IsNull(h) || (s & (MutexL | MutexW)) != 0 {
        MutexReleaseK(w, h, false)
        return
    }
    MutexSetW(w)
    nh := MQPopFront(h) // before the store: the owner may leave once it sees it (Q2)
    k := MutexHand(h, 1)
    MutexReleaseK(w, nh, false)
    WakeCell(k)
}

// MutexSetW sets W under K, before the state store of the cell it stands
// for, so the woken task's MutexClearW comes after it (Q3). Ordering edge:
// the Cas is before the state store in program order, and the store is a
// release that the woken task's Load acquires, so its MutexClearW is later
// in the word's modification order than this set.
MutexSetW(w: &raw.Atomic[u64]) {
    for {
        s := w.Load()
        if w.Cas(s, s | MutexW) {
            return
        }
    }
}

// MutexUnlockWord releases a held Mutex word. Release edge: the read-modify-
// write that clears L (this Cas, or MutexReleaseK's) publishes the critical
// section to the next task whose Cas sets L; a handoff publishes it through
// the cell's state store instead. The fast path is kept apart from
// MutexUnlockSlow so that it stays small enough to inline.
//
// The fast path is MutexUnlockSlow's first iteration, inline: the same
// Load, the same test (K clear, and nobody queued or W set with S clear:
// the negation of its two breaks) and the same Cas(s, s & ~L), so no state
// transition is added. Cas(L, 0) alone failed whenever W was set, though
// the slow path then only cleared L with that one Cas. A failed test or
// Cas enters MutexUnlockSlow, whose loop loads again. The Load orders
// nothing; the Cas is the release edge above.
MutexUnlockWord(w: &raw.Atomic[u64], io: Io) {
    s := w.Load()
    if (s & MutexK) == 0 && ((s & MutexQ) == 0 || (s & (MutexW | MutexS)) == MutexW) && w.Cas(s, s & ~MutexL) {
        return
    }
    MutexUnlockSlow(w, io)
}

// MutexUnlockSlow: K is held, or somebody is queued with no waiter woken or
// the first cell starving, or a race changed the word under the fast path's
// Cas. Release edge: as MutexUnlockWord's.
//
// First, when K is clear and either nobody is queued, or a woken waiter is
// on its way (W) and the first cell is not starving (S clear), one Cas
// clears L without taking K: the two cases below in which an unlock under
// K only clears L. Q1 holds (K is clear in the word the Cas replaces), Q3
// holds (the queue is empty, or W is set), and Q4 needs no handover (S,
// which MutexReleaseK keeps true of the head this word names, is clear; a
// changed head changes the word, and the Cas fails). Taking K for such an
// unlock would cost two read-modify-writes of the contended line instead of
// one, and hold K meanwhile, for which other tasks spin.
MutexUnlockSlow(w: &raw.Atomic[u64], io: Io) {
    for {
        // A load orders nothing: the Cas that acts on it does.
        s0 := w.Load()
        if (s0 & MutexK) != 0 {
            break
        }
        if (s0 & MutexQ) != 0 && ((s0 & MutexW) == 0 || (s0 & MutexS) != 0) {
            break
        }
        // Release edge: as MutexUnlockWord's Cas, it clears L.
        if w.Cas(s0, s0 & ~MutexL) {
            return
        }
    }
    s := MutexTakeK(w, io)
    h := MWAt(s & MutexQ)
    if raw.IsNull(h) {
        MutexReleaseK(w, h, true)
        return
    }
    if MWStarving(h) {
        // Q4: hand over; L stays set, so no barger can take it meanwhile.
        nh := MQPopFront(h) // before the store: the owner may leave once it sees it (Q2)
        k := MutexHand(h, 2)
        MutexReleaseK(w, nh, false)
        WakeCell(k)
        return
    }
    if (s & MutexW) != 0 {
        // A woken waiter will retry: a second wake would only park again.
        MutexReleaseK(w, h, true)
        return
    }
    MutexSetW(w)
    nh := MQPopFront(h) // before the store: the owner may leave once it sees it (Q2)
    k := MutexHand(h, 1)
    MutexReleaseK(w, nh, true)
    WakeCell(k)
}

/// Ends the Mutex and returns its value. A Mutex passed by value is not
/// borrowed, so it is unlocked and nobody waits for it. O(1); allocates
/// nothing.
export Mutex[T].Into(self): T {
    Mutex(word: _, value) := self
    return value
}

/// The value, which the Guard keeps alive and exclusive while it lives.
/// O(1).
export Guard[T].Ref(&self): &T {
    return SlotRef(MutexValueAt(self.m), 0)
}

/// The value, writable, which the Guard keeps alive and exclusive while it
/// lives. O(1).
export Guard[T].Mut(mut self): mut T {
    return SlotMut(MutexValueAt(self.m), 0)
}

/// Replaces the value and drops the old one, so it compiles only when T is
/// not linear (E0701 at the call); Replace(g.Mut(), x) gives the old value
/// back instead. O(1); allocates nothing.
export Guard[T].Store(mut self, x: T into self) {
    if raw.IsLinear[T]() {
        raw.Fail(8, "Guard.Store would drop a linear value")
    }
    Discard(Replace(SlotMut(MutexValueAt(self.m), 0), x))
}

/// Releases the lock, through the Io that Lock was given. When the first
/// waiter has waited 1 ms and lost the lock once more, Unlock hands the lock
/// to it directly. O(1); allocates nothing.
export Guard[T].Unlock(self) {
    Guard(m, io, marker: _) := self
    MutexUnlockWord(MutexWordAt(m), io)
}

// The three-state lock word of io_stream.mk's stream lock: 0 unlocked,
// 1 locked, 2 locked with waiters; Drepper's mutex 3 in
// Rust std's futex form. Its holders keep it for a constant number of steps
// and never across a suspension; it needs no fairness beyond FIFO wakes.
//
// LockWordSlow is its contended path. On Canceled the word may stay 2,
// which costs the next unlock one wake that finds nobody; the wake this task
// forwards covers a runtime that reports Canceled after it took a wake meant
// for this word (the runtime may do either), so no other waiter is left parked
// behind an unlocked word. Its callers pass a NoCancel Io.
LockWordSlow(lock: &raw.Atomic[u32], io: Io): Canceled!void {
    var state := SpinWhileHeld(lock)
    // Acquire edge: a Cas(0, 1) reads the 0 the last Swap(0) wrote, so that
    // critical section happens before this one.
    if state == 0 && lock.Cas(0, 1) {
        return
    }
    for {
        // Acquire edge: a Swap(2) that returns 0 reads the last Swap(0).
        // A Swap(2) that returns 1 or 2 marks the word contended, so the
        // holder's Swap(0) sees 2 and wakes one waiter.
        if state != 2 && lock.Swap(2) == 0 {
            return
        }
        if IoWait(io, AtomicWord(lock), 2).err == SysErr.Canceled {
            IoWake(IoMasked(io), AtomicWord(lock), false)
            fail Canceled
        }
        state = SpinWhileHeld(lock)
    }
}

// SpinWhileHeld reads the word until it is not 1 (free, or contended) or
// the spin runs out, and returns the last value read. Loads only, so
// spinners do not take the cache line from the holder. A load orders
// nothing here: the Cas or Swap after it does.
//
// Liveness with a newcomer that wins by Cas(0, 1) while waiters are parked
// (the word is then 1): the waiter that the unlock's Swap(0) woke takes the
// lock only by Swap(2), never by Cas(0, 1), so if it does not get the lock
// it leaves the word at 2 before it parks again, and the next unlock wakes
// the next waiter.
SpinWhileHeld(lock: &raw.Atomic[u32]): u32 {
    var n := MutexSpins
    for {
        s := lock.Load()
        if s != 1 || n == 0 {
            return s
        }
        n -= 1
    }
}

// ReleaseWord releases a three-state lock word. Release edge: the Swap(0)
// publishes the critical section to the next Cas(0, 1) or Swap(2) that
// reads it. Old word 2: one waiter is woken (wake never fails).
ReleaseWord(lock: &raw.Atomic[u32], io: Io) {
    if lock.Swap(0) == 2 {
        IoWake(IoMasked(io), AtomicWord(lock), false)
    }
}
