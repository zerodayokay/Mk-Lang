// Why: Mutex, Chan, scopes and counters all need a task to sleep until a
// word changes, and a wait queue in each of them would cost memory and code.
//
// What: WaitWord and WakeWord (park while a 32-bit word holds a value, wake
// one or all waiters on it), CancelWordWait, and the bucket table behind them.
//
// How: tasks wait on 32-bit words in one table of 4,096 buckets, each a lock
// and a doubly linked FIFO of tasks, found by a Fibonacci hash of the
// address, as Linux futexes and Go's semaphore table do. Spurious returns
// are allowed.

import std.core
import std.raw
import std.rt

// 64 bytes: a lock and a FIFO of waiting tasks.
struct Bucket {
    lock: raw.Atomic[u32]
    pad: u32
    head: *Task
    tail: *Task
    rest: [5]u64
}

const BucketCount: int = 4096
/// WaitNone: the task waits on no address.
export const WaitNone: u32 = 0
/// WaitQueued: the task is in its address's bucket.
export const WaitQueued: u32 = 1
/// WaitWoken: a wake took the task off its bucket.
export const WaitWoken: u32 = 2
/// WaitCancelled: a cancel took the task off its bucket.
export const WaitCancelled: u32 = 3

// BucketOf is the bucket of address a: Fibonacci hashing (Knuth, TAOCP
// vol. 3, 6.4, multiplicative hashing by 2^64 / phi) of the word index
// a >> 2, keeping the top 12 bits of the product. Every address bit reaches
// the index, so words at one frame offset in many task slots (addresses
// 2^20 or 2^24 apart, an arithmetic progression) spread over the table
// (the three-distance theorem) instead of sharing a few buckets, as a
// shift-and-xor hash, ((a >> 2) ^ (a >> 14)) & 4095, would: it puts such
// words of 4,096 Default slots in 64 buckets and of Large slots in 4, so
// WakeWord would scan a bucket of every waiter. Test RT-Wait-Spread checks
// the spread. One multiply, no allocation.
BucketIndex(a: uint): int {
    return Trunc[int](((u64(a) >> 2) *% 0x9E37_79B9_7F4A_7C15) >> 52)
}

BucketOf(addr: *u32): *Bucket {
    i := BucketIndex(raw.Addr(addr))
    unsafe "the bucket table Start mapped" {
        return raw.Offset((*TS()).buckets, i)
    }
}

// AtomicAt reads the word at addr atomically.
AtomicAt(addr: *u32): u32 {
    unsafe "addr is a live 32-bit word of a Mutex, Chan, scope or counter" {
        return (*raw.PtrCast[u32, raw.Atomic[u32]](addr)).Load()
    }
}

/// WaitWord parks the running task while *addr == expected. None when
/// woken (or spuriously), Canceled when cancellable and the task's scope
/// chain is or becomes cancelled while it waits. Off a task stack it is
/// panic 16. Lock order: the inner scope's lock, then the bucket's.
/// O(1) besides the wait: one bucket lock, and the scope lock when cancellable;
/// allocates nothing (the wait record is in the Task).
export WaitWord(addr: *u32, expected: u32, cancellable: bool): core.SysErr {
    t := MustCurrent()
    owed := HookLeave(t)
    r := WaitWordIn(t, addr, expected, cancellable)
    HookEnter(t, owed)
    return r
}

// WaitWordIn is WaitWord between the task's hooks (hooks.mk).
WaitWordIn(t: *Task, addr: *u32, expected: u32, cancellable: bool): core.SysErr {
    if cancellable && Cancelled(t) {
        return core.SysErr.Canceled
    }
    if AtomicAt(addr) != expected {
        return core.SysErr.None
    }
    var sc := raw.Null[ScopeRec]()
    if cancellable {
        locked := LockScope(t)
        if locked.cancelled {
            return core.SysErr.Canceled
        }
        sc = locked.sc
    }
    b := BucketOf(addr)
    unsafe "the bucket and the running task" {
        rt.Lock(&(*b).lock)
        if AtomicAt(addr) != expected {
            rt.Unlock(&(*b).lock)
            if !raw.IsNull(sc) {
                rt.Unlock(&(*sc).lock)
            }
            return core.SysErr.None
        }
        (*t).wait.addr = addr
        // Plain: under the bucket lock, which every reader of the state
        // holds (WakeWordOn, CancelWordWait).
        (*t).wait.state = WaitQueued
        (*t).wait.result = core.SysErr.None
        (*t).link = raw.Null[Task]()
        (*t).wprev = (*b).tail
        if raw.IsNull((*b).tail) {
            (*b).head = t
        } else {
            (*(*b).tail).link = t
        }
        (*b).tail = t
        if !raw.IsNull(sc) {
            LinkBlocked(sc, t, WaitKindWord)
            rt.Unlock(&(*sc).lock)
        }
        Park(raw.Of(&(*b).lock))
        if !raw.IsNull(sc) {
            Unblock(t)
        }
        return (*t).wait.result
    }
}

// CancelWordWait takes t out of its bucket when it is still queued there
// (WaitQueued to WaitCancelled, result Canceled) and reports whether it did;
// a task already woken is left to its waker. The caller holds t's scope
// lock (CancelWait), and then this bucket lock (the lock order).
//
// O(1): the bucket list is doubly linked (link forward, wprev
// back), as Linux's futex hash chains and Go's sudog lists are. The singly
// linked list made a cancel scan from the head; a scope's blocked list is
// LIFO and the bucket FIFO, so cancelling N waiters of one word cost N^2/2
// steps under the bucket and scope spinlocks. state == WaitQueued, read
// under the bucket lock, is the proof that t is on this bucket's list:
// only an append under this lock sets it, and only a removal under this
// lock clears it.
CancelWordWait(t: *Task): bool {
    unsafe "t is in a word wait, registered on a blocked list whose lock is held" {
        b := BucketOf((*t).wait.addr)
        rt.Lock(&(*b).lock)
        // Edge: under the bucket lock, as every write of the state.
        if (*t).wait.state != WaitQueued {
            rt.Unlock(&(*b).lock)
            return false
        }
        BucketUnlink(b, t)
        (*t).wait.result = core.SysErr.Canceled
        // Edge: under the bucket lock; WaitWord reads result after its
        // Park returns, which the Ready after this section follows.
        (*t).wait.state = WaitCancelled
        rt.Unlock(&(*b).lock)
        return true
    }
}

// BucketUnlink removes t from b's list in O(1); b's lock is held and t is
// on the list.
BucketUnlink(b: *Bucket, t: *Task) {
    unsafe "b's list under its lock; t is on it" {
        prev := (*t).wprev
        next := (*t).link
        if raw.IsNull(prev) {
            (*b).head = next
        } else {
            (*prev).link = next
        }
        if raw.IsNull(next) {
            (*b).tail = prev
        } else {
            (*next).wprev = prev
        }
        (*t).link = raw.Null[Task]()
        (*t).wprev = raw.Null[Task]()
    }
}

/// WakeWord wakes the first (or every) task waiting on addr; from any
/// thread, never parking. A woken task goes to the calling task's carrier,
/// else to the global queue.
/// O(w) for the w tasks queued in addr's bucket (one of 4,096); allocates
/// nothing, though a ready may start a carrier thread (Ready).
export WakeWord(addr: *u32, all: bool) {
    WakeWordOn(Here(), addr, all)
}

// WakeWordOn is WakeWord readying on c, the calling thread's carrier (or
// null). It reads only the bucket, never *addr: a task that has ended may
// wake an address its scope no longer holds, which is a
// spurious wake for any waiter there.
WakeWordOn(c: *Carrier, addr: *u32, all: bool) {
    // Edge: a wake before the task system started has no waiter to find
    // (WaitWord runs only on a task), and the bucket table does not exist
    // yet: a Main that never made a task frees a Sim.
    // The state word is stored once, by Start, before any task runs.
    if Sched.state.Load() == 0 {
        return
    }
    b := BucketOf(addr)
    var woken := raw.Null[Task]()
    unsafe "the bucket's list, under its lock" {
        rt.Lock(&(*b).lock)
        var t := (*b).head
        for !raw.IsNull(t) {
            next := (*t).link
            if raw.Same((*t).wait.addr, addr) {
                BucketUnlink(b, t)
                // Edge: under the bucket lock; the woken task reads its
                // result only after the Ready below.
                (*t).wait.state = WaitWoken
                (*t).link = woken
                woken = t
                if !all {
                    break
                }
            }
            t = next
        }
        rt.Unlock(&(*b).lock)
        for !raw.IsNull(woken) {
            w := woken
            woken = (*w).link
            ReadyOn(c, w)
        }
    }
}
