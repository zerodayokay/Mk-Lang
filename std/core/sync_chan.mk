// Why: tasks hand values to each other through channels; a send must move
// the value exactly once, wake exactly one task, and leave the value with the
// caller when it fails or is cancelled.
//
// What: Chan[T] with New, Send, TrySend, Recv, Close, Free, FreeEmpty and
// FreeEach; the channel lock (ChanLock, ChanUnlock); parking (ParkCell); the
// support of sync.Select (ChanWatch, ChanUnwatch, ChanPass, SelFireOne,
// SelFireAll, SelParkWait); and the cancellation check SyncCancel with
// SyncWait and SyncWake. The raw field access is mem_chan.mk's.
//
// How: Go's channel (runtime/chan.go, hchan): one lock; a ring of cap slots;
// two FIFO queues of parked waiters, receivers (rq) and senders (sq), each
// waiter a cell on its own task's stack; direct handoff between a sender and
// a parked receiver, or a receiver and a parked sender, so a value is copied
// once and exactly one task is woken per handoff. Capacity 0 is a
// rendezvous: Send succeeds only once a receiver holds the value. Everything
// but the cell's state word is read and written under the lock.

// Invariants (tests_sync.mk and std.sync's tests test each):
//   C1 rq is non-empty only when used == 0, and sq only when used == cap.
//   C2 closed implies rq and sq are empty, and closed never goes back.
//   C3 A queued cell has state 0, its task is parked on it or about to be,
//      and it is alive: its task leaves Send or Recv only after it reads a
//      non-zero state, or after it unlinks the cell under the lock.
//   C4 A cell's state goes from 0 to 1 (handed over) or 2 (closed) once,
//      under the lock, by the task that took it off its queue; after that
//      store the waker reads nothing of the cell (its address is only the
//      key of the wake that follows).
//   C5 Consume on success: a Send that fails, Closed or
//      Canceled, has not moved x; one that succeeds moved x once, into the
//      ring, into a receiver's cell, or (when it parked) out of its frame by
//      the receiver that set its state to 1.
//
// Memory ordering (raw.Atomic is seq_cst only). The lock word is Mutex's
// queue lock (sync_mutex.mk), with its edges. A cell's state: the waker's Store(1) or Store(2) is a
// release that publishes the value it wrote into a receiver's cell, or its
// read of a sender's x; the parked task's Load that reads it is the acquire
// that makes the value, or the end of the read, visible before it moves on.

import std.raw

/// A channel that carries values of T between tasks, in FIFO order, with a
/// buffer of the capacity given to New, in memory from an allocator. Share
/// a &Chan with the tasks that send and receive; Close tells receivers no
/// more values come, and Free gives the memory back. Chan[T] and &Chan[T]
/// are Selectable for every T, so sync.Select can wait on any channel.
/// Chan is Send and Share when T is Send. It is not linear, so the compiler
/// does not catch a missing Free: a channel on the heap that is never freed
/// leaks; one on an arena's Alloc goes with the arena.
export view struct Chan[T] {
    st: *ChanState[T]
    marker: [0]T
}

unsafe impl[T: Send] Send for Chan[T] {}
unsafe impl[T: Send] Share for Chan[T] {}

// The channel's lock is the Mutex's queue lock (sync_mutex.mk: L, K, W and
// a FIFO of waiter cells on the waiters' stacks in one u64 word), driven
// with a NoCancel Io: a task that waits for the lock is not waiting for the
// channel, and the lock is held only for a constant number of steps (Close:
// one per parked task), never across a suspension. Not the three-state
// word (LockWordSlow, Drepper's mutex 3): under it, once one task has
// parked, every contender parks at once (spinning stops at 2) and every
// unlock wakes one, a convoy at 8 carriers; the queue lock spins while
// nobody is queued, keeps one woken waiter at a time (W) and hands over to
// a starving one, counted in wakes, with no clock read (MutexLockSlow's
// clock false).
//
// ChanLock: a contended path first leaves its Io in flg and tab, so a
// Close, which has no Io, can park and wake with one. Ordering edge: the
// flg and tab Stores are before this task's Cas that sets K and queues its
// cell (program order, all seq_cst), so a Close that reads the queue or W
// in the word and then loads tab and flg reads these stores or later ones.
// flg is stored first and loaded second, so a reader that sees this tab
// sees this flg or a later one. Two tasks under different tables (a Sim
// run and the native runtime on one channel) can still leave a pair from
// two Ios; ChanLockNoIo then retries instead of trusting the wait.
// Acquire edge of Cas(0, L): as Mutex.Lock's.
//
// A word that already holds this Io's value is not stored again: the
// words share the lock's cache line, and every contended acquisition
// storing both (two xchg) made the waiters write the line the holder works
// on. A skipped store
// leaves the value an earlier Store wrote, equal to this Io's, so a Close
// that reads the pair after this task queued still reads this Io's words or
// later ones.
ChanLock[T](st: *ChanState[T], io: Io) {
    lock := ChanLockAt(st)
    if !lock.Cas(0, MutexL) {
        f := u64(IoFlags(io))
        // Edge: none for the Loads (a compare with this task's own value);
        // a Store, when made, is before the Cas that queues this task, as
        // above.
        if ChanFlgAt(st).Load() != f {
            ChanFlgAt(st).Store(f)
        }
        tb := TabOf(io)
        if ChanTabAt(st).Load() != tb {
            ChanTabAt(st).Store(tb)
        }
        MutexLockSlow(lock, IoMasked(io), false) else {}
    }
}

// ChanTabIo is the Io the last contended ChanLock stored. Ordering edge:
// tab is loaded first and flg second, the reverse of ChanLock's stores, so
// a reader that sees a tab sees that store's flg or a later one.
ChanTabIo[T](st: *ChanState[T]): Io {
    t := ChanTabAt(st).Load()
    return TabIo(t, ChanFlgAt(st).Load())
}

// ChanLockNoIo is ChanLock for Close: it spins on loads while no task has
// ever taken the slow path (tab is 0; the holder never suspends, so the
// wait is the holder's few steps), and takes the slow path with the stored
// Io otherwise. A slow path that fails (the stored Io's run has ended, or
// its table refuses this task: a Sim's deadlock rule answers Canceled even
// on a NoCancel Io) did not take the lock, so it spins and tries again,
// never returning as if it held the lock. Acquire
// edge: Cas(0, L) as ChanLock's. The Loads order nothing: any non-zero tab
// is the address of a global table (TabOf), and the Cas decides.
ChanLockNoIo[T](st: *ChanState[T]) {
    lock := ChanLockAt(st)
    for {
        if lock.Cas(0, MutexL) {
            return
        }
        if ChanTabAt(st).Load() != 0 {
            var ok := true
            MutexLockSlow(lock, ChanTabIo(st), false) else {
                ok = false
            }
            if ok {
                return
            }
        }
        var n := MutexSpins
        for n > 0 && lock.Load() != 0 {
            n -= 1
        }
    }
}

// Release edge: as Guard.Unlock's (MutexUnlockWord).
ChanUnlock[T](st: *ChanState[T], io: Io) {
    MutexUnlockWord(ChanLockAt(st), IoMasked(io))
}

// ChanUnlockNoIo: a word other than L alone means a task queued (or was
// popped and is woken, W), and every such task stored tab before its Cas
// that queued it (the edge ChanLock names), so tab is set when the fast
// Cas fails. Release edge: as MutexUnlockWord's.
ChanUnlockNoIo[T](st: *ChanState[T]) {
    lock := ChanLockAt(st)
    if !lock.Cas(MutexL, 0) {
        MutexUnlockSlow(lock, ChanTabIo(st))
    }
}

// QPush appends w to q; QPop takes the first cell or null; QRemove unlinks
// a queued w. O(1) each (doubly linked). The lock is held.
QPush[T](q: mut WaitQ[T], w: *ChanWaiter[T]) {
    WLink(w, q.tail, raw.Null[ChanWaiter[T]]())
    if raw.IsNull(q.tail) {
        q.head = w
    } else {
        WSetNext(q.tail, w)
    }
    q.tail = w
}

QPop[T](q: mut WaitQ[T]): *ChanWaiter[T] {
    w := q.head
    if !raw.IsNull(w) {
        QRemove(q, w)
    }
    return w
}

QRemove[T](q: mut WaitQ[T], w: *ChanWaiter[T]) {
    p := WPrev(w)
    n := WNext(w)
    if raw.IsNull(p) {
        q.head = n
    } else {
        WSetNext(p, n)
    }
    if raw.IsNull(n) {
        q.tail = p
    } else {
        WSetPrev(n, p)
    }
}

// Hand ends a handoff to a cell already off its queue, under the lock: it
// reads the wake's Io and the state word's address first, then stores the
// state (C4), and returns the wake for the caller to run after ChanUnlock,
// as Go's chansend and chanrecv call goready after unlock (the wake, a
// bucket lock, a run-queue push and at times a poller syscall, is
// then outside the channel's critical section). The store stays under the
// lock: a cancelled owner that takes the lock and still reads 0 unlinks its
// cell, so a popped cell with state 0 must never be seen by it.
//
// Release edge: the Store(state) publishes what this task wrote into the
// cell (WFill) or the end of its read of the sender's x (TakeElem) to the
// owner's Load in ParkCell that reads it. After the store the owner may
// leave and its frame be reused; the wake uses the address only as a key
// (WakeWordOn never dereferences it), so a later waiter at that address
// gets a spurious return, which every wait loop allows.
Hand[T](w: *ChanWaiter[T], state: u32): ChanWake {
    k := ChanWake(io: WIo(w).NoCancel(), addr: AtomicWord(WStateAt(w)))
    // Release (2): publishes WFill/TakeElem to ParkCell's Acquire LoadOrd(1);
    // ChanUnlock's Cas and WakeWord's bucket Lock Cas provide the full barrier
    // before the bucket scan.
    WStateAt(w).StoreOrd(state, 2)
    return k
}

// ChanWake is a handoff's wake, taken by Hand before the state store.
struct ChanWake {
    io: Io
    addr: *u32
}

WakeCell(k: ChanWake) {
    IoWake(k.io, k.addr, false)
}

// Release is Hand and its wake at once, for Close, which wakes under the
// lock: it pops an unbounded number of cells and allocates nothing, and a
// cell's memory may be gone once its state is stored, so it cannot keep a
// list of wakes to run after the unlock. Close runs once per channel.
Release[T](w: *ChanWaiter[T], state: u32) {
    WakeCell(Hand(w, state))
}

/// A channel with room for capacity buffered values (0: every Send waits for a
/// Recv), in memory from a; capacity < 0 panics with code 8. OutOfMemory leaves
/// nothing allocated. O(1); allocates the channel's state and, above 0, a
/// buffer of capacity slots.
export Chan[T].New(a: &Alloc, capacity: int): OutOfMemory!Chan[T] {
    if capacity < 0 {
        raw.Fail(8, "Chan.New: negative capacity")
    }
    st := ChanAlloc[T](a, capacity)
    if raw.IsNull(st) {
        fail OutOfMemory
    }
    return Chan[T](st: st, marker: [])
}

/// Sends x, waiting while there is no receiver and no room. Closed when the
/// channel is closed and Canceled when the scope is; either way the caller
/// still owns x. O(1) besides the wait; allocates nothing.
///
/// A waiting receiver takes x directly; otherwise x goes into the buffer
/// when it has room; otherwise the task waits until a receiver takes x, the
/// channel is closed (Closed) or the scope is cancelled (Canceled). Send
/// checks for cancellation first: in a cancelled scope it returns Canceled
/// even when a receiver or room is there. Once a receiver has taken x, Send
/// succeeds, even if the scope is cancelled meanwhile. A Send that makes a
/// value ready wakes one sync.Select watching the channel.
export Chan[T].Send(&self, io: Io, x: T into self): Canceled|Closed!void {
    // CancelPoint's fast test inline (CancelClear), as Mutex.Lock's.
    if !CancelClear(io) {
        try CancelPoint(io)
    }
    st := self.st
    ChanLock(st, io)
    var m := ChanMetaGet(st)
    if m.closed {
        ChanUnlock(st, io)
        fail Closed
    }
    r := QPop(mut m.rq)
    if !raw.IsNull(r) {
        ChanMetaPut(st, m)
        WFill(r, x)
        k := Hand(r, 1)
        ChanUnlock(st, io)
        WakeCell(k)
        return
    }
    if m.used < m.cap {
        // The tail slot, (head + used) % cap, computed without forming
        // head + used, which overflows int when cap is above MaxInt / 2 (a
        // zero-size T needs no ring, so any capacity is allocatable).
        // head < cap and used < cap, so both forms are
        // in range.
        at := if m.used < m.cap - m.head {
            m.head + m.used
        } else {
            m.used - (m.cap - m.head)
        }
        m.used += 1
        ChanMetaPut(st, m)
        RingPut(m.buf, at, x)
        if ChanWatched(st) {
            SelFireOne(st) // a value is buffered: one watching Select may now Recv
        }
        ChanUnlock(st, io)
        return
    }
    var cell := ChanWaiter[T](next: raw.Null[ChanWaiter[T]](), prev: raw.Null[ChanWaiter[T]](),
        state: raw.Atomic[u32].New(0), io: io, elem: ElemOf(&x), value: none)
    w := WaiterOf(mut cell)
    QPush(mut m.sq, w)
    ChanMetaPut(st, m)
    if ChanWatched(st) {
        SelFireOne(st) // a sender is parked: one watching Select may now Recv
    }
    ChanUnlock(st, io)
    state := ParkCell(st, w, io, false)
    ChanWaiter(next: _, prev: _, state: _, io: _, elem: _, value) := cell
    Discard(value) // a sender's cell never holds a value
    if state == ParkCanceled {
        fail Canceled
    }
    if state == 2 {
        fail Closed
    }
    ForgetSent(x) // a receiver moved x out bitwise (TakeElem) before it set state 1
}

/// Sends x without waiting: none when it sent (a waiting receiver took x,
/// or the buffer had room), or some(x), x given back, when the channel is
/// full, so a full channel never drops x. Closed when the channel is
/// closed, and then the caller still owns x. O(1) besides the lock wait;
/// allocates nothing.
///
/// At capacity 0 it succeeds only when a task is waiting in Recv; a task
/// waiting in sync.Select does not count, so TrySend gives x back. It takes
/// no Io and does not check for cancellation. It never waits for room, but
/// it may wait briefly for the channel's lock, which is held for a constant
/// number of steps.
export Chan[T].TrySend(&self, x: T into self): Closed!?T {
    st := self.st
    ChanLockNoIo(st)
    var m := ChanMetaGet(st)
    if m.closed {
        ChanUnlockNoIo(st)
        fail Closed
    }
    r := QPop(mut m.rq)
    if !raw.IsNull(r) {
        ChanMetaPut(st, m)
        WFill(r, x)
        k := Hand(r, 1)
        ChanUnlockNoIo(st)
        WakeCell(k)
        return none
    }
    if m.used < m.cap {
        // The tail slot, as Send computes it.
        at := if m.used < m.cap - m.head {
            m.head + m.used
        } else {
            m.used - (m.cap - m.head)
        }
        m.used += 1
        ChanMetaPut(st, m)
        RingPut(m.buf, at, x)
        if ChanWatched(st) {
            SelFireOne(st) // a value is buffered: one watching Select may now Recv
        }
        ChanUnlockNoIo(st)
        return none
    }
    ChanUnlockNoIo(st)
    return some(x)
}

/// The oldest value sent, waiting while there is none. Closed when the channel
/// is closed and empty, Canceled when the scope is canceled. O(1) besides the
/// wait; allocates nothing.
///
/// Values buffered before Close are still delivered, then Recv fails with
/// Closed. Recv checks for cancellation first: in a cancelled scope it
/// returns Canceled even with a value buffered, so a cleanup that drains
/// the channel passes io.NoCancel(). Once a sender has handed it a value,
/// Recv returns it, even if the scope is cancelled meanwhile.
export Chan[T].Recv(&self, io: Io): Canceled|Closed!T {
    // CancelPoint's fast test inline (CancelClear), as Mutex.Lock's.
    if !CancelClear(io) {
        try CancelPoint(io)
    }
    st := self.st
    ChanLock(st, io)
    var m := ChanMetaGet(st)
    s := QPop(mut m.sq)
    if !raw.IsNull(s) {
        if m.cap == 0 {
            ChanMetaPut(st, m)
            x := TakeElem(WElem(s))
            k := Hand(s, 1)
            ChanUnlock(st, io)
            WakeCell(k)
            return x
        }
        at := m.head // the ring is full (C1): its tail slot is the head's
        x := RingTake(m.buf, at)
        RingPut(m.buf, at, TakeElem(WElem(s)))
        m.head += 1
        if m.head == m.cap {
            m.head = 0
        }
        ChanMetaPut(st, m)
        k := Hand(s, 1)
        ChanUnlock(st, io)
        WakeCell(k)
        return x
    }
    if m.used > 0 {
        at := m.head
        m.head += 1
        if m.head == m.cap {
            m.head = 0
        }
        m.used -= 1
        ChanMetaPut(st, m)
        x := RingTake(m.buf, at)
        ChanUnlock(st, io)
        return x
    }
    if m.closed {
        ChanUnlock(st, io)
        fail Closed
    }
    var cell := ChanWaiter[T](next: raw.Null[ChanWaiter[T]](), prev: raw.Null[ChanWaiter[T]](),
        state: raw.Atomic[u32].New(0), io: io, elem: raw.Null[T](), value: none)
    w := WaiterOf(mut cell)
    QPush(mut m.rq, w)
    ChanMetaPut(st, m)
    ChanUnlock(st, io)
    state := ParkCell(st, w, io, true)
    ChanWaiter(next: _, prev: _, state: _, io: _, elem: _, value) := cell
    x := value else {
        // No sender filled the cell: a sender fills it before it stores 1,
        // so state 1 never reaches here.
        if state == ParkCanceled {
            fail Canceled
        }
        fail Closed
    }
    return x
}

// ParkCanceled is ParkCell's result when the wait was cancelled and the
// cell unlinked. ParkCell returns no error so that its caller destructures
// its cell on every path, and a cell over a linear T is always consumed.
const ParkCanceled: u32 = 3

// ParkCell waits on the queued cell w (unlocked) until its state is not 0
// and returns it, 1 or 2, or ParkCanceled. A non-zero state is final (C4),
// so it is read without the lock. On a Canceled wait the lock is taken and
// the state read again: still 0 means no waker took the cell, so it is
// unlinked from its queue and ParkCanceled returned; 1 or 2 means a waker won the race, and that
// result stands (level-triggered: the next cancellable call returns
// Canceled). A spurious return waits again. Acquire edge: a Load
// that reads 1 or 2 reads Hand's store, the release that publishes the
// value in the cell or the end of the read of x.
ParkCell[T](st: *ChanState[T], w: *ChanWaiter[T], io: Io, recv: bool): u32 {
    s := WStateAt(w)
    st0 := s.LoadOrd(1)
    if st0 != 0 {
        return st0
    }
    for {
        r := IoWait(io, AtomicWord(s), 0)
        state := s.LoadOrd(1)
        if state != 0 {
            return state
        }
        if r.err == SysErr.Canceled {
            ChanLock(st, io)
            state2 := s.LoadOrd(1)
            if state2 != 0 {
                ChanUnlock(st, io)
                return state2
            }
            var m := ChanMetaGet(st)
            if recv {
                QRemove(mut m.rq, w)
            } else {
                QRemove(mut m.sq, w)
            }
            ChanMetaPut(st, m)
            ChanUnlock(st, io)
            return ParkCanceled
        }
    }
}

/// Closes the channel: later Sends fail with Closed, and Recv drains what is
/// buffered, then fails with Closed. Every task waiting in Send or Recv
/// wakes with Closed, and every sync.Select watching the channel wakes.
/// Closing twice is allowed; Close cannot be cancelled and takes no Io.
/// O(waiting tasks); allocates nothing.
export Chan[T].Close(&self) {
    st := self.st
    ChanLockNoIo(st)
    var m := ChanMetaGet(st)
    if m.closed {
        ChanUnlockNoIo(st)
        return
    }
    m.closed = true
    for {
        r := QPop(mut m.rq)
        if raw.IsNull(r) {
            break
        }
        Release(r, 2)
    }
    for {
        s := QPop(mut m.sq)
        if raw.IsNull(s) {
            break
        }
        Release(s, 2)
    }
    ChanMetaPut(st, m)
    if ChanWatched(st) {
        SelFireAll(st) // closed: watching Selects may now Recv (Closed)
    }
    ChanUnlockNoIo(st)
}

/// Releases the channel: every buffered value is released with a, oldest
/// first, then the buffer and the channel's state go back to a. a must be
/// the allocator the channel was made with (code 12 otherwise). The call
/// compiles only when T is not linear or is Free (E0701). No task can be
/// waiting, since a waiting task holds a loan on the Chan. O(1) when T has
/// nothing to release, else O(buffered).
impl[T] Free for Chan[T] {
    Free(self, a: &Alloc) {
        Chan(st, marker) := self
        Discard(marker)
        m := ChanMetaGet(st)
        OwnerCheck(a, m.owner, "Chan.Free: wrong allocator")
        if NeedsRelease[T]() {
            var at := m.head
            for _ in 0..m.used {
                ReleaseValue(RingTake(m.buf, at), a)
                at += 1
                if at == m.cap {
                    at = 0
                }
            }
        }
        ChanFreeState(a, st, m)
    }
}

/// As Free, but panics (code 11) when a value is still buffered: with
/// FreeEach, the way to release a channel of a T that is linear and not
/// Free. O(1).
export Chan[T].FreeEmpty(self, a: &Alloc) {
    Chan(st, marker) := self
    Discard(marker)
    m := ChanMetaGet(st)
    OwnerCheck(a, m.owner, "Chan.FreeEmpty: wrong allocator")
    if m.used > 0 {
        raw.Fail(11, "Chan.FreeEmpty of a channel holding values")
    }
    ChanFreeState(a, st, m)
}

/// As Free, but hands every buffered value to release, oldest first.
/// O(buffered) calls of release.
export Chan[T].FreeEach(self, a: &Alloc, const release: fn(T)) {
    Chan(st, marker) := self
    Discard(marker)
    m := ChanMetaGet(st)
    OwnerCheck(a, m.owner, "Chan.FreeEach: wrong allocator")
    var at := m.head
    for _ in 0..m.used {
        release(RingTake(m.buf, at))
        at += 1
        if at == m.cap {
            at = 0
        }
    }
    ChanFreeState(a, st, m)
}

// ---- sync.Select and sync.RwLock support ----
//
// Select (std/sync/select.mk) watches each channel of its record with one
// cell of its SelPark (mem_chan.mk), as Go's selectgo queues one sudog per
// case. Watch, under the channel's lock: true when Recv would not
// wait, else the cell is linked into the channel's xq. A Send that buffers
// a value or parks a sender fires one Select (SelFireOne): it pops
// cells from the head of xq under its lock until one's park Cas-es fired
// from 0 to that cell's index + 1 (a park another channel fired already
// is popped and skipped: that Select rescans anyway), and wakes that one
// through the park's own Io, under the lock. Close fires every cell
// (SelFireAll): a closed channel is ready for every Select at once. So a
// channel event wakes one Select watching that channel, as Go's
// selectgo dequeues one sudog, and a channel nobody watches pays one null
// test of xq.head under the lock it already holds.
//
// Passing the wake on: a Select takes no value (it returns
// an index), so the Select a Send fired may return another field,
// or Canceled, or its caller may never Recv; a Select left parked would
// then miss the value. So a Select that a channel fired, on leaving with
// any result, calls ChanPass on that channel: under its lock, if it is
// still ready and still watched, it fires one more. While a channel is
// ready and a cell of it is linked unfired, some Select it fired is
// running and will pass the wake on, or will find it not ready under the
// lock (then the readiness ended, and the next ready-making operation
// fires again). A Select that a fire woke, finding nothing ready, parks
// again without passing: what it found ended the readiness.
//
// No lost wakeup: the readiness test and the link are one critical section
// of the channel's lock, so an operation that makes the channel ready
// either comes before it (Watch returns true) or after it, and then finds
// the cell. fired is a futex word: a Select waits on it while it reads 0,
// and the fire's Store(1) comes before its wake, so a wait that read 0
// before the Store is woken by the wake that follows it.
//
// Lifetime (C4's discipline): a waker reads the cell and its park, stores
// fired and wakes, all under the lock, so the Select, which unlinks each
// cell under that channel's lock before it leaves, is still there; the
// wake uses fired's address only as a key, so a wake that reaches a later
// waiter at that address is a spurious return, which every wait allows. A Select
// unlinks every cell still linked, under each channel's lock, before it
// rescans, waits again or returns, on every path.

// SelQPush links c, cell at of park p, at the tail of q; SelQRemove unlinks
// a linked c (queued false after). The channel's lock is held. O(1) each.
SelQPush(q: mut SelQ, c: *SelCell, p: *SelPark, at: int) {
    SelCellPut(c, SelCell(next: raw.Null[SelCell](), prev: q.tail, park: p, queued: true, at: Trunc[u32](at)))
    if raw.IsNull(q.tail) {
        q.head = c
    } else {
        var t := SelCellGet(q.tail)
        t.next = c
        SelCellPut(q.tail, t)
    }
    q.tail = c
}

SelQRemove(q: mut SelQ, c: *SelCell) {
    var x := SelCellGet(c)
    if raw.IsNull(x.prev) {
        q.head = x.next
    } else {
        var pr := SelCellGet(x.prev)
        pr.next = x.next
        SelCellPut(x.prev, pr)
    }
    if raw.IsNull(x.next) {
        q.tail = x.prev
    } else {
        var nx := SelCellGet(x.next)
        nx.prev = x.prev
        SelCellPut(x.next, nx)
    }
    x.next = raw.Null[SelCell]()
    x.prev = raw.Null[SelCell]()
    x.queued = false
    SelCellPut(c, x)
}

// SelFire pops cell c from q and fires its park unless another channel
// fired it this round: fired.Cas(0, at + 1), then the wake through the
// park's Io, under the lock. True when this call fired it. Release edge:
// the Cas publishes nothing the Select reads without a lock (it rescans
// under each channel's lock, and reads the index after unlinking every
// cell under each lock); it is the change of the futex word that the
// Select's wait compares, made before the wake. A failed Cas means a
// Cas of another channel won this round, and that one's wake follows it.
SelFire(q: mut SelQ, c: *SelCell): bool {
    x := SelCellGet(c)
    SelQRemove(q, c)
    f := SelParkFired(x.park)
    if !f.Cas(0, x.at + 1) {
        return false
    }
    WakeCell(ChanWake(io: SelParkIo(x.park).NoCancel(), addr: AtomicWord(f)))
    return true
}

// SelFireOne fires the first Select in the channel's xq that no other
// channel fired this round, popping the ones another did. Under the
// lock (it allocates nothing, so it keeps no wake for after the unlock).
// The callers test ChanWatched first, so a channel nobody watches pays one
// load and a null test inline. O(1) but for the skipped cells, each popped
// once per round of its Select.
SelFireOne[T](st: *ChanState[T]) {
    var q := ChanXqGet(st)
    for !raw.IsNull(q.head) {
        if SelFire(mut q, q.head) {
            break
        }
    }
    ChanXqPut(st, q)
}

// SelFireAll pops and fires every cell of the channel's xq (Close: every
// Select watching it may now Recv, and gets Closed), under the lock as
// Close's Release wakes its waiters.
SelFireAll[T](st: *ChanState[T]) {
    var q := ChanXqGet(st)
    for !raw.IsNull(q.head) {
        _ = SelFire(mut q, q.head)
    }
    ChanXqPut(st, q)
}

/// A channel's Selectable.Pass, for sync.Select. A Select that this
/// channel woke calls it as it leaves; when Recv would still not wait and
/// another Select watches the channel, it wakes that one, so a value the
/// leaving Select did not take is not left with Selects parked on it.
export ChanPass[T](c: &Chan[T], p: *SelPark) {
    st := c.st
    io := SelParkIo(p)
    ChanLock(st, io)
    m := ChanMetaGet(st)
    if (m.used > 0 || !raw.IsNull(m.sq.head) || m.closed) && ChanWatched(st) {
        SelFireOne(st)
    }
    ChanUnlock(st, io)
}

/// A channel's Selectable.Ready, for sync.Select: true when Recv would not
/// wait (a value is buffered, a sender is waiting, or the channel is
/// closed). It takes the channel's lock without an Io, as Close does, for
/// a constant number of steps. O(1).
export ChanReady[T](c: &Chan[T]): bool {
    st := c.st
    ChanLockNoIo(st)
    m := ChanMetaGet(st)
    ready := m.used > 0 || !raw.IsNull(m.sq.head) || m.closed
    ChanUnlockNoIo(st)
    return ready
}

/// Whether any sync.Select still watches the channel, read under its lock
/// as ChanReady reads. std.sync's tests use it to check that a Select that
/// returned leaves no watch behind.
export ChanHasWatcher[T](c: &Chan[T]): bool {
    st := c.st
    ChanLockNoIo(st)
    w := ChanWatched(st)
    ChanUnlockNoIo(st)
    return w
}

/// A channel's Selectable.Watch, for sync.Select: true when Recv would not
/// wait; otherwise cell i of p (i below SelCells) starts watching the
/// channel and it returns false. The lock is taken with the Select's Io,
/// without cancellation.
export ChanWatch[T](c: &Chan[T], p: *SelPark, i: int): bool {
    st := c.st
    io := SelParkIo(p)
    ChanLock(st, io)
    m := ChanMetaGet(st)
    if m.used > 0 || !raw.IsNull(m.sq.head) || m.closed {
        ChanUnlock(st, io)
        return true
    }
    var q := ChanXqGet(st)
    SelQPush(mut q, SelCellAt(p, i), p, i)
    ChanXqPut(st, q)
    ChanUnlock(st, io)
    return false
}

/// A channel's Selectable.Unwatch, for sync.Select: cell i of p stops
/// watching the channel. After it returns, no task touches that cell
/// again.
export ChanUnwatch[T](c: &Chan[T], p: *SelPark, i: int) {
    st := c.st
    io := SelParkIo(p)
    w := SelCellAt(p, i)
    ChanLock(st, io)
    if SelCellGet(w).queued {
        var q := ChanXqGet(st)
        SelQRemove(mut q, w)
        ChanXqPut(st, q)
    }
    ChanUnlock(st, io)
}

/// Waits until a watched channel wakes p; true when the wait was cancelled
/// first. A spurious wake-up waits again. For sync.Select.
export SelParkWait(p: *SelPark): bool {
    f := SelParkFired(p)
    io := SelParkIo(p)
    for {
        // Edge: acquire: the Load that reads non-zero reads a fire's Store;
        // what the Select reads next, it reads under the channels' locks.
        if f.Load() != 0 {
            return false
        }
        r := IoWait(io, AtomicWord(f), 0)
        if f.Load() != 0 {
            return false
        }
        if r.err == SysErr.Canceled {
            return true
        }
    }
}

/// Clears p for the next round of a sync.Select. The caller has unwatched
/// every cell first, so no wake from an earlier round can reach the next.
/// The Store is sequentially consistent: an acquire and release edge.
export SelParkReset(p: *SelPark) {
    SelParkFired(p).Store(0)
}

/// The cancellation check that Mutex.Lock makes, shared by std.sync,
/// std.net, std.http, std.fs, std.proc and std.term for calls that make no
/// runtime call first. Canceled only when io's task is in a cancelled
/// scope that io's NoCancel mask does not cover; a scope opened from a
/// masked Io is not covered. Any other failure of the check is not a
/// cancellation, and a NoCancel Io of a Sim run that crashed, restarted or
/// was freed, or of a freed Recorder, passes. O(1): for a native Io two
/// loads while no scope is cancelled, else one indirect call; allocates
/// nothing.
export SyncCancel(io: Io): Canceled!void {
    try CancelPoint(io)
}

/// Waits while w holds expected, through the Io's wait entry (IoTable.wait);
/// true when the wait was cancelled. A spurious wake-up returns false, so
/// the caller re-reads w.
export SyncWait(io: Io, w: &raw.Atomic[u32], expected: u32): bool {
    return IoWait(io, AtomicWord(w), expected).err == SysErr.Canceled
}

/// Wakes one task waiting on w, or every one when all is true.
export SyncWake(io: Io, w: &raw.Atomic[u32], all: bool) {
    IoWake(IoMasked(io), AtomicWord(w), all)
}

/// The value of a Mutex without taking its lock, for a caller that keeps
/// writers out by other means: RwLock's readers hold a count that keeps
/// the writer, who holds the Mutex while writing, waiting, so no Guard is
/// alive while this reference is.
export MutexPeek[T](m: &Mutex[T]): &T from m {
    return SlotRef(MutexValueAt(raw.Of(m)), 0)
}
