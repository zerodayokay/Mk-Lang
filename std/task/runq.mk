// Why: the scheduler needs per-carrier run queues that the owning carrier
// uses without locks and idle carriers can steal from, plus one shared
// queue for overflow and fairness.
//
// What: each carrier's bounded work-stealing deque (PushLocal, Take,
// StealOne, StealHalf, Overflow), its runnext slot (TakeNext, StealNext),
// and the global queue, the injector (GlobalPush, GlobalSplice, GlobalPop).
//
// How: the deque is the Chase-Lev work-stealing deque on a fixed ring of 256
// entries (D. Chase and Y. Lev, "Dynamic Circular Work-Stealing Deque", SPAA
// 2005), in the form N. M. Le, A. Pop, A. Cohen and F. Zappa Nardelli prove
// correct for weak memory ("Correct and Efficient Work-Stealing for Weak
// Memory Models", PPoPP 2013, figure 1), without the growth step: a full
// push sends half the ring and the new task to the global queue.
// The owner pushes and takes at the bottom (LIFO); thieves take at the top
// (FIFO), one task per compare-and-swap; "steal half" is that many
// single steals (StealHalf).
//
// Memory ordering. raw.Atomic offers only sequentially consistent
// operations, and every load and store of
// top, bottom, the ring slots and runnext below is one. The paper's
// relaxed, acquire and release accesses are each strengthened to seq_cst,
// and its two seq_cst fences are subsumed: Take's store of bottom followed
// by its load of top, and Steal's load of top followed by its load of
// bottom, are seq_cst accesses in program order, so they appear in that
// order in the single total order S of all seq_cst operations, which is the
// store-load ordering the fences exist to give. A strengthening of every
// access keeps every execution the proof allows and removes none of its
// guarantees, so the paper's theorem (each pushed task is taken exactly
// once, by Take or by one successful Steal) holds here. The comments at
// each access name the edge it must create.
//
// ABA. top and bottom are 64-bit counters that only the operations below
// move: top only increases (by a successful compare-and-swap, +1), so a
// compare-and-swap of top from t can succeed only while top still equals t,
// and t is never seen again once passed; 2^63 tasks would have to be stolen
// for it to wrap. The ring index is the counter & 255.

import std.raw
import std.rt

/// RingSize is the capacity of a carrier's deque.
export const RingSize: i64 = 256
const RingMask: i64 = 255

// TaskAt and AddrOf convert between ring words and task records.
TaskAt(x: u64): *Task {
    unsafe "a ring word, runnext or a queue link holds a task record's address, or 0" {
        return raw.FromAddr[Task](Trunc[uint](x))
    }
}

AddrOf(t: *Task): u64 {
    return u64(raw.Addr(t))
}

// RingSlot is slot i & 255 of c's ring.
RingSlot(c: *Carrier, i: i64): *raw.Atomic[u64] {
    unsafe "c is a carrier record; the index is masked into the ring" {
        return raw.Of(&(*c).ring[Trunc[int](i & RingMask)])
    }
}

// PushLocal adds t at the bottom of c's deque. Only c's own thread calls it
// (Chase-Lev's owner), from a task on c or from c's scheduler loop.
PushLocal(c: *Carrier, t: *Task) {
    unsafe "c is the calling thread's own carrier" {
        // Edge: relaxed owner-only load of bottom.
        b := (*c).bottom.LoadOrd(0)
        // Edge: this load of top reads the latest successful steal, so the
        // fullness test below never lets a push overwrite a slot a thief
        // may still read (a stale smaller top would only make it stricter).
        t0 := (*c).top.Load()
        if b - t0 >= RingSize {
            Overflow(c, t)
            return
        }
        // Edge: relaxed slot write published by the seq_cst store of bottom
        // below, so a thief whose load of bottom sees b + 1 reads this slot.
        (*RingSlot(c, b)).StoreOrd(AddrOf(t), 0)
        Jitter(JitterPush)
        (*c).bottom.Store(b + 1)
    }
}

// Take removes the oldest task at the top of c's deque (Go's runqget), or
// returns null. Only c's own thread calls it; empty costs zero RMWs.
Take(c: *Carrier): *Task {
    unsafe "c is the calling thread's own carrier" {
        // Edge: relaxed owner-only load of bottom, fixed by this thread.
        b := (*c).bottom.LoadOrd(0)
        for {
            // Edge: reads top; if t0 == b the deque is empty without an RMW.
            t0 := (*c).top.Load()
            if t0 >= b {
                return raw.Null[Task]()
            }
            // Edge: owner's own slot in [t0, b), live while top == t0.
            x := (*RingSlot(c, t0)).LoadOrd(0)
            Jitter(JitterTake)
            // Edge: Cas on top orders this take against thieves (StealOne, StealHalf).
            if (*c).top.Cas(t0, t0 + 1) {
                return TaskAt(x)
            }
        }
    }
}

// StealOne takes the task at the top of v's deque from any thread, or
// returns null when v's deque is empty or another taker won the race (the
// paper's ABORT; the caller moves on).
StealOne(v: *Carrier): *Task {
    unsafe "v is a carrier record; its deque is read only through atomics" {
        // Edge: top is loaded before bottom in S (the paper's seq_cst fence
        // between them), so t0 < b means slot t0 held a pushed task that had
        // not been taken when bottom was read.
        t0 := (*v).top.Load()
        Jitter(JitterSteal)
        b := (*v).bottom.Load()
        if t0 >= b {
            return raw.Null[Task]()
        }
        // The slot is read before the compare-and-swap; it can be
        // overwritten only by a push at index t0 + 256, which needs top >
        // t0 first, and then this compare-and-swap fails and x is dropped.
        x := (*RingSlot(v, t0)).Load()
        // Edge: the steal's transfer: only the compare-and-swap that moves
        // top from t0 gets slot t0's task (the owner's Take races on the
        // same word).
        return if !(*v).top.Cas(t0, t0 + 1) { raw.Null[Task]() } else { TaskAt(x) }
    }
}

// Size is bottom - top as last seen: a hint, exact only to the owner.
Size(c: *Carrier): i64 {
    unsafe "c is a carrier record" {
        // Edge: none; a hint (AnyWork reads it after raising nidle, so a push
        // before that Add is seen: the wake protocol in sched.mk).
        n := (*c).bottom.Load() - (*c).top.Load()
        return if n < 0 { 0 } else { n }
    }
}

// StealHalf moves up to half (rounded up, at most 128) of v's deque to c's in
// one Cas on v.top (Go's runqgrab). c is the calling carrier with an empty
// deque (StealRounds), so stolen entries stage directly in c's unpublished
// ring slots above c.bottom without a stack buffer.
StealHalf(c: *Carrier, v: *Carrier): *Task {
    unsafe "c is the calling thread's carrier; v's deque is read through atomics" {
        for {
            // Edge: top loaded before bottom; bottom only increases, so slots
            // in [t0, b) were written before bottom reached b and cannot be
            // overwritten while top == t0.
            t0 := (*v).top.Load()
            b := (*v).bottom.Load()
            n := b - t0
            if n <= 0 {
                return raw.Null[Task]()
            }
            // Edge: owner-only load of c's bottom and top; StealRounds calls
            // StealHalf with c empty, falling back to StealOne otherwise.
            cb := (*c).bottom.LoadOrd(0)
            if cb != (*c).top.Load() {
                return StealOne(v)
            }
            var want := n - n / 2
            if want > 128 {
                want = 128
            }
            // Edge: slot t0 published by v's bottom.Store.
            first := (*RingSlot(v, t0)).LoadOrd(0)
            for j in 1..Trunc[int](want) {
                // Edge: c's deque is empty (bottom == cb), so thieves cannot read
                // these slots until bottom.Store below; v's slots are live while top == t0.
                (*RingSlot(c, cb + Trunc[i64](j - 1))).StoreOrd((*RingSlot(v, t0 + Trunc[i64](j))).LoadOrd(0), 0)
            }
            Jitter(JitterSteal)
            // Edge: one Cas claims all want tasks against v's Take and other thieves.
            if (*v).top.Cas(t0, t0 + want) {
                if want > 1 {
                    // Edge: publishes all stolen slots to thieves in one store.
                    (*c).bottom.Store(cb + want - 1)
                }
                return TaskAt(first)
            }
        }
    }
}

// Overflow takes a push to c's full ring: half of it (taken from the top in
// one batch Cas, safe because only the owner writes the ring or moves bottom)
// and t go to the global queue in one locked splice.
Overflow(c: *Carrier, t: *Task) {
    unsafe "c is the calling thread's own carrier; the tasks are runnable" {
        (*t).link = raw.Null[Task]()
        var head := t
        var tail := t
        var n: u32 = 1
        // Edge: owner-only load of bottom, fixed throughout Overflow.
        b := (*c).bottom.LoadOrd(0)
        for {
            // Edge: reads latest top; only c writes c's ring, so slots
            // [t0, t0 + want) stay untouched even after top advances here.
            t0 := (*c).top.Load()
            avail := b - t0
            if avail <= 0 {
                break
            }
            want := if avail > RingSize / 2 { RingSize / 2 } else { avail }
            Jitter(JitterSteal)
            // Edge: one Cas claims [t0, t0 + want) against concurrent thieves.
            if (*c).top.Cas(t0, t0 + want) {
                for j in 0..Trunc[int](want) {
                    // Edge: c is the sole writer of c's ring, still here.
                    x := TaskAt((*RingSlot(c, t0 + Trunc[i64](j))).LoadOrd(0))
                    (*x).link = raw.Null[Task]()
                    (*tail).link = x
                    tail = x
                    n += 1
                }
                break
            }
        }
        // A pinned carrier's tasks are its own: they go to its inbox, which
        // only it takes from (PinSplice, carrier.mk), never to the global
        // queue.
        if raw.IsNull((*c).home) {
            GlobalSplice(head, tail, n)
        } else {
            PinSplice(c, head, tail, n)
        }
    }
}

// TakeNext empties c's runnext slot; any thread may race it, and the
// swap gives the task to exactly one.
TakeNext(c: *Carrier): *Task {
    unsafe "c is a carrier record" {
        // Edge: none needed; an early out before the Swap, which transfers.
        if (*c).runnext.Load() == 0 {
            return raw.Null[Task]()
        }
        // Edge: the swap is the transfer of the task; a thief's
        // compare-and-swap (StealNext) and this swap are ordered in S, and
        // only the first sees the address.
        return TaskAt((*c).runnext.Swap(0))
    }
}

// StealNext takes v's runnext from another carrier when it still holds x,
// the value the thief read before it waited (StealRounds). A task's address
// in runnext is the task itself, so a compare-and-swap that succeeds after
// the slot was emptied and refilled with the same task still transfers
// exactly the task the slot holds (the value is the identity; no ABA
// hazard): that task is runnable and in the slot, wherever it was between.
StealNext(v: *Carrier, x: u64): *Task {
    unsafe "v is a carrier record" {
        // Edge: the compare-and-swap and the owner's swap (TakeNext,
        // ReadyPut) are ordered in S; only the first to see x gets the task.
        return if x == 0 || !(*v).runnext.Cas(x, 0) { raw.Null[Task]() } else { TaskAt(x) }
    }
}

// The global queue: a FIFO of tasks linked through Task.link under the
// leaf lock glock (last in the lock order), with its length in glen so the
// scheduler can look without the lock.

GlobalPush(t: *Task) {
    unsafe "t is runnable and on no other list" {
        (*t).link = raw.Null[Task]()
    }
    GlobalSplice(t, t, 1)
}

// GlobalSplice appends the list head..tail of n tasks.
GlobalSplice(head: *Task, tail: *Task, n: u32) {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit; the global queue under glock" {
        rt.Lock(&(*st).glock)
        if raw.IsNull((*st).gtail) {
            (*st).ghead = head
        } else {
            (*(*st).gtail).link = head
        }
        (*st).gtail = tail
        // Edge: glen is raised after the tasks are linked and before the
        // lock is released; a carrier that reads glen > 0 then takes glock,
        // which orders it after this splice. An idle carrier's recheck
        // reads glen after raising nidle (sched_idle.mk, Idle).
        _ = (*st).glen.Add(n)
        rt.Unlock(&(*st).glock)
    }
}

// GlobalPop takes up to max tasks from the global queue: the first is
// returned, the rest pushed on c's deque.
GlobalPop(c: *Carrier, max: u32): *Task {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit; the global queue under glock" {
        // Edge: a nonzero glen was raised after the tasks were linked under
        // glock (GlobalSplice); this pop takes glock before it reads the list.
        if (*st).glen.Load() == 0 {
            return raw.Null[Task]()
        }
        rt.Lock(&(*st).glock)
        first := (*st).ghead
        if raw.IsNull(first) {
            rt.Unlock(&(*st).glock)
            return first
        }
        var n: u32 = 1
        var last := first
        for n < max && !raw.IsNull((*last).link) {
            last = (*last).link
            n += 1
        }
        (*st).ghead = (*last).link
        if raw.IsNull((*st).ghead) {
            (*st).gtail = raw.Null[Task]()
        }
        (*last).link = raw.Null[Task]()
        // Edge: under glock, as GlobalSplice's Add.
        _ = (*st).glen.Add(0 -% n)
        rt.Unlock(&(*st).glock)
        var x := (*first).link
        (*first).link = raw.Null[Task]()
        if !raw.IsNull(x) {
            // Edge: owner-only load of bottom; acquire top for room check.
            var b := (*c).bottom.LoadOrd(0)
            var t0 := (*c).top.Load()
            var b0 := b
            for !raw.IsNull(x) {
                next := (*x).link
                (*x).link = raw.Null[Task]()
                if b - t0 >= RingSize {
                    if b != b0 {
                        // Edge: publish batch before Overflow reads bottom.
                        (*c).bottom.Store(b)
                        b0 = b
                    }
                    Overflow(c, x)
                    b = (*c).bottom.LoadOrd(0)
                    t0 = (*c).top.Load()
                    b0 = b
                } else {
                    // Edge: relaxed slot write published by the bottom store below.
                    (*RingSlot(c, b)).StoreOrd(AddrOf(x), 0)
                    b += 1
                }
                x = next
            }
            if b != b0 {
                // Edge: publishes popped batch to thieves in one seq_cst store.
                (*c).bottom.Store(b)
            }
        }
        return first
    }
}
