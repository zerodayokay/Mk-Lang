// Why: every task that waits on I/O parks on the poller; on Linux that is
// io_uring or epoll, and both need the same shared state and the same rule
// for which thread blocks in the kernel.
//
// What: PollInit (backend selection and RaiseFdLimit), the state page
// PollPage, the spinlocks, the done list, PollWait, PollWake, PollTestHook
// and the dispatch to poll_uring.mk and poll_epoll.mk.
//
// How: io_uring unless MK_IO=epoll or the kernel refuses it, else epoll.
// One thread at a time blocks in the kernel; the others park on a semaphore,
// so one completion never wakes every idle carrier. The done list holds ops
// completed outside a kernel reap (an epoll cancel, a completion that did not
// fit a caller's out slice); PollWait hands them out first, and an op is on
// at most one list and leaves it under that list's lock, so none is lost and
// none is delivered twice.

import std.core
import std.raw

struct PollState {
    // The backend PollInit chose (2 io_uring, 3 epoll) and the wake
    // eventfd, as atomics so PollWake (a signal handler may call it) reads
    // them with no lock. Both are written once by PollInit before any other
    // poll call, and every later reader is ordered after it by the
    // sequentially consistent store and load.
    backend: raw.Atomic[u32]
    evfd: raw.Atomic[u64]
    page: raw.Atomic[u64]
}

var Poll: PollState = PollState(backend: raw.Atomic[u32].New(0), evfd: raw.Atomic[u64].New(0),
    page: raw.Atomic[u64].New(0))

// PollPage is the state both backends share, in one committed page.
struct PollPage {
    epfd: uint
    // token is 1 while one thread blocks in the kernel; sleepers
    // counts the threads parked on sem instead.
    token: raw.Atomic[u32]
    sleepers: raw.Atomic[i64]
    sem: Sem
    done_lock: raw.Atomic[u32]
    done_head: *Op
    done_tail: *Op
    // How many ops the done list holds: written under done_lock, read with
    // no lock by TakeDone's fast path and the parker (the list head itself
    // is a plain field, read only under the lock).
    done_n: raw.Atomic[u64]
    // epoll: the descriptor table (MaxFds records of DescBytes).
    descs: *u8
    // epoll_pwait2: 0 not tried, 1 present, 2 missing (ENOSYS).
    pwait2: raw.Atomic[u32]
    // io_uring (poll_uring.mk).
    ring_fd: uint
    sq_lock: raw.Atomic[u32]
    cq_lock: raw.Atomic[u32]
    sq_tail: u32
    sq_mask: u32
    sq_entries: u32
    cq_mask: u32
    sq_head_p: *u8
    sq_tail_p: *u8
    sq_array: *u8
    cq_head_p: *u8
    cq_tail_p: *u8
    cqes: *u8
    sqes: *u8
    generation: raw.Atomic[u32]
    wake_buf: u64
    // 1: a non-blocking reap took the wake's CQE while no thread held the
    // token; the next holder's wait returns at once (UringReap, UringWait).
    wake_pending: raw.Atomic[u32]
    // EdgeInject's controlled schedule (PollTestHook, std/rt-io_probe_test.go):
    // when on, the first EAGAIN of each epoll try runs test_cb (which makes
    // data arrive) and then reaps the edge it caused, before the op is
    // recorded: the interleaving in which another thread consumes the edge in
    // the window.
    test_on: raw.Atomic[u32]
    test_busy: raw.Atomic[u32]
    test_cb: fn(*u8)
    test_arg: *u8
    injected: raw.Atomic[u64]
    // PollTestPark's controlled schedule: when on, a PollWait that finds
    // the token taken runs park_cb(park_arg) before it counts itself a
    // sleeper (the window in which the token holder can return unseen).
    park_on: raw.Atomic[u32]
    park_cb: fn(*u8)
    park_arg: *u8
    parked: raw.Atomic[u64]
}

// PP is the state page (null before PollInit).
PP(): *PollPage {
    unsafe "the page PollInit made" {
        // Edge: PollInit stores the address after it filled the page, so a Load
        // that reads it sees the page.
        return raw.FromAddr[PollPage](Trunc[uint](Poll.page.Load()))
    }
}

// SpinLock takes a leaf lock: compare-and-swap 0 to 1, then up to 64 plain
// loads between tries, then a thread yield. Every lock here is held for a
// few instructions and never across a system call that blocks. The Cas
// that takes the lock is the acquire edge and SpinUnlock's release store
// of 0 the release edge, so everything written under the lock happens
// before the next holder's Cas succeeds.
SpinLock(w: *raw.Atomic[u32]) {
    unsafe "w is a lock word in the state page or a descriptor record" {
        for {
            if (*w).Load() == 0 && (*w).Cas(0, 1) {
                return
            }
            var spins := 0
            // Edge: none; these Loads only wait for the word to look free, and
            // the Cas above is the acquire.
            for (*w).Load() != 0 && spins < 64 {
                spins += 1
            }
            if spins >= 64 {
                ThreadYield()
            }
        }
    }
}

SpinTry(w: *raw.Atomic[u32]): bool {
    unsafe "w is a lock word" {
        // Edge: as SpinLock's: the Cas is the acquire, SpinUnlock's store of 0
        // the release.
        return (*w).Load() == 0 && (*w).Cas(0, 1)
    }
}

// SpinUnlock is a release store (order 2; a plain mov on x86_64,
// where a seq_cst store is an xchg), as rt.Unlock and core.SpinUnlock.
// Edge: release to the next SpinLock's or SpinTry's successful Cas(0, 1),
// which reads this 0. It orders no later load of this thread, so no
// protocol may use it as one half of a store-load (Dekker) pair. Audited
// for every caller:
// after each unlock in poll.mk, poll_epoll.mk and poll_uring.mk comes a
// return, a system call (PollWake's eventfd write, epoll_ctl, the retries),
// another lock's Cas, or a seq_cst pair on another word (UringReap's
// wake_pending Store then token Load); the done list's Dekker partner is
// done_n's Add, an RMW before the unlock, never the lock word. A failed
// SpinTry concludes only that another reaper holds the CQ lock.
SpinUnlock(w: *raw.Atomic[u32]) {
    unsafe "w is a lock word this thread holds" {
        // Edge: release; this holder's writes happen before the next
        // holder's acquiring Cas in SpinLock or SpinTry.
        (*w).StoreOrd(0, 2)
    }
}

// OsArea is the first byte of op.os. Linux layout: the wait or done list
// link at 0, the io_uring generation at 8, the stage (epoll: 1 connecting;
// io_uring remove: 1 the directory step) at 12, a sockaddr at 16 (32 bytes)
// with its length at 48, the io_uring user_data at 56 (an atomic word: a
// PollCancel on another thread reads it while a resubmission writes it),
// the resubmission handoff word at 64 and the CQE result it hands over at
// 68. Op is C layout: os starts at byte 136, so these
// words are naturally aligned.
OsArea(op: *Op): *u8 {
    unsafe "op is a live operation" {
        return raw.Data((*op).os[..])
    }
}

// io_uring's RECVMSG and SENDMSG keep their msghdr at 72 (56 bytes) and
// its iovec at 128 (16 bytes); the msghdr names the sockaddr at OsAddr.
const OsMsg: int = 72
const OsIov: int = 128
const MsgTrunc: uint = 0x20

// DgramDone is a finished datagram op's result from the kernel's count r:
// a receive decodes its source address from OsAddr into op.addr, and a
// count past len (MSG_TRUNC; trunc: the msghdr said so) is a datagram cut
// to len, reported as len with os 1.
DgramDone(op: *Op, r: int, trunc: bool) {
    unsafe "op is this thread's until it is handed out" {
        if (*op).kind != OpRecvFrom {
            (*op).res = Ok(i64(r))
            return
        }
        if !DecodeAddr(raw.Offset(OsArea(op), OsAddr), mut (*op).addr) {
            (*op).addr = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
        }
        if trunc || r > (*op).len {
            var n := r
            if n > (*op).len {
                n = (*op).len
            }
            (*op).res = OsResult(value: i64(n), err: core.SysErr.None, os: 1)
            return
        }
        (*op).res = Ok(i64(r))
    }
}

const OsNext: int = 0
const OsGen: int = 8
const OsStage: int = 12
const OsAddr: int = 16
const OsAddrN: int = 48
const OsUserData: int = 56
const OsResub: int = 64
const OsResubRes: int = 68

Next(op: *Op): *Op {
    unsafe "the link holds a live op's address or 0" {
        return raw.FromAddr[Op](Trunc[uint](Get64(OsArea(op), OsNext)))
    }
}

SetNext(op: *Op, n: *Op) {
    Put64(OsArea(op), OsNext, u64(raw.Addr(n)))
}

// PushDone queues a completed op (res already final) for the next PollWait.
PushDone(op: *Op) {
    pp := PP()
    SetNext(op, raw.Null[Op]())
    unsafe "the state page; the done list under its lock" {
        SpinLock(raw.Of(&(*pp).done_lock))
        if raw.IsNull((*pp).done_tail) {
            (*pp).done_head = op
        } else {
            SetNext((*pp).done_tail, op)
        }
        (*pp).done_tail = op
        // done_n: the Add comes after the list write and before the
        // unlock; a TakeDone whose Load sees it takes the lock, whose
        // acquire edge makes the list write visible. A Load that misses it
        // is ordered before the Add (sequentially consistent), so that
        // PollWait does not see the op; every pusher is followed by a
        // PollWait of its own or a PollWake (PushDone's callers), which
        // makes some PollWait take it.
        _ = (*pp).done_n.Add(1)
        SpinUnlock(raw.Of(&(*pp).done_lock))
    }
}

// TakeDone moves queued completions into out[k..] and returns the new k.
// The fast path is an atomic load of done_n, never a plain read of the
// list head, which other threads write under the lock.
TakeDone(out: mut []*Op, k: int): int {
    pp := PP()
    var n := k
    unsafe "the state page; the done list under its lock" {
        // Edge: the Load reads PushDone's Add, made under the lock after it
        // linked the op; a stale 0 leaves the op for the next take, which the
        // pusher's PollWake brings.
        if n >= out.Len() || (*pp).done_n.Load() == 0 {
            return n
        }
        SpinLock(raw.Of(&(*pp).done_lock))
        var took: u64 = 0
        for n < out.Len() && !raw.IsNull((*pp).done_head) {
            op := (*pp).done_head
            (*pp).done_head = Next(op)
            if raw.IsNull((*pp).done_head) {
                (*pp).done_tail = raw.Null[Op]()
            }
            out[n] = op
            n += 1
            took += 1
        }
        // Edge: none; under the lock, like PushDone's Add: done_n equals the
        // list's length whenever the lock is free.
        _ = (*pp).done_n.Add(0 -% took)
        SpinUnlock(raw.Of(&(*pp).done_lock))
    }
    return n
}

// Deliver puts a completed op in out[k] when it fits, else on the done
// list, and returns the new k.
Deliver(op: *Op, out: mut []*Op, k: int): int {
    if k < out.Len() {
        out[k] = op
        return k + 1
    }
    PushDone(op)
    return k
}

/// PollInit starts the poller and returns its backend: 2 io_uring, 3 epoll;
/// 0 on failure (fatal at start).
/// Once, at start: O(1) system calls; maps the backend's rings or tables, kept
/// until exit.
export PollInit(force_epoll: bool): u32 {
    RaiseFdLimit()
    p := Reserve(4096, 4096)
    if raw.IsNull(p) || !Commit(p, 4096) {
        return 0
    }
    unsafe "the fresh state page" {
        pp := raw.PtrCast[u8, PollPage](p)
        if !SemInit(mut (*pp).sem) {
            return 0
        }
        // PollInit runs once at startup, before any other poll call:
        // the page, backend and evfd stores are the publication edges every
        // later Load (sequentially consistent) is ordered after.
        Poll.page.Store(u64(raw.Addr(p)))
    }
    if !force_epoll && UringInit() {
        Poll.backend.Store(2)
        return 2
    }
    if !EpollInit() {
        return 0
    }
    // Edge: the backend is stored once, in PollInit, before any other thread
    // polls.
    Poll.backend.Store(3)
    return 3
}

// RaiseFdLimit sets the soft RLIMIT_NOFILE to the hard limit, once, at
// start (PollInit runs once at startup, before any descriptor is made). The
// poller watches any number of descriptors, and the soft limit alone (1,024
// by default; 10,240 on some WSL setups) would end a server at that many
// connections with EMFILE long before memory or the poller does; the echo
// test aims at 100,000 connections. Go's runtime does
// the same at start (os/rlimit.go, Go 1.19). A child gets the original
// soft limit back before its execve (ChildMain), as Go's children do
// (syscall's origRlimitNofile): a program that uses select() relies on a
// soft limit no larger than FD_SETSIZE. A failure (a seccomp filter, an
// old kernel without prlimit64) leaves the limit as it was.
RaiseFdLimit() {
    var lim: [2]u64 = [0; 2]
    unsafe "system call; lim is a struct rlimit64 { cur, max }" {
        p := raw.Addr(raw.DataMut(mut lim[..]))
        if Syscall(Nrs().prlimit64, 0, 7, 0, p, 0, 0) != 0 || lim[0] >= lim[1] {
            return
        }
        cur := lim[0]
        lim[0] = lim[1]
        if Syscall(Nrs().prlimit64, 0, 7, p, 0, 0, 0) == 0 {
            // Edge: stored once, here in PollInit, before any spawn;
            // ChildMain's Loads come after them.
            FdLimit.cur.Store(cur)
            FdLimit.max.Store(lim[1])
        }
    }
}

// FdLimitState is the RLIMIT_NOFILE pair RaiseFdLimit replaced, for a
// child to get back (ChildMain); max is 0 when nothing was raised. Both
// are stored once, in PollInit, before any spawn can run, and a spawn's
// loads are ordered after the stores (sequentially consistent).
struct FdLimitState {
    cur: raw.Atomic[u64]
    max: raw.Atomic[u64]
}

var FdLimit: FdLimitState = FdLimitState(cur: raw.Atomic[u64].New(0), max: raw.Atomic[u64].New(0))

// IsUring: the backend is io_uring.
IsUring(): bool {
    // Edge: PollInit's Store, made before any other thread runs.
    return Poll.backend.Load() == 2
}

/// PollSupports reports whether the poller runs ops of kind: io_uring every
/// kind; epoll sockets (and other non-blocking descriptors), datagrams and
/// child waits only, so files, opens, renames, removes and syncs go to the
/// blocking pool.
/// O(1); allocates nothing.
export PollSupports(kind: u32): bool {
    // The datagram kinds and the child wait: both backends.
    if kind == OpSendTo || kind == OpRecvFrom || kind == OpProcWait || kind == OpReady {
        return true
    }
    if kind < OpRead || kind > OpSync {
        return false
    }
    return if IsUring() { true } else { kind <= OpSend }
}

/// PollAttach readies h for the poller: io_uring needs no registration;
/// epoll registers h edge-triggered and refuses a regular file (false).
/// O(1) system calls; allocates nothing.
export PollAttach(h: uint): bool {
    return if IsUring() { true } else { EpollAdd(h) }
}

/// PollDetach takes h out of the poller before it is closed: epoll removes
/// it; io_uring holds no registration.
/// O(1) system calls; allocates nothing.
export PollDetach(h: uint) {
    if !IsUring() {
        EpollDel(h)
    }
}

/// PollSubmit starts op: true when it is pending (its completion comes from
/// PollWait), false when it finished now with op.res set (Other for a kind
/// the backend does not support).
/// O(1) system calls (io_uring: a send's one try, then the ring's enter;
/// epoll: one try, a connect's check and the descriptor's lock, repeated
/// while edges arrive meanwhile); allocates nothing (op is the caller's
/// until its completion).
export PollSubmit(op: *Op): bool {
    unsafe "op is the caller's operation; from here the one-owner rule holds" {
        raw.Fill(OsArea(op), 0, 160)
        if !PollSupports((*op).kind) {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
    }
    return if IsUring() { UringSubmit(op) } else { EpollSubmit(op) }
}

/// PollCancel asks for op's cancellation; its completion still arrives
/// through PollWait (Canceled unless it finished first).
/// O(1) system calls; allocates nothing.
export PollCancel(op: *Op) {
    if IsUring() {
        UringCancel(op)
    } else {
        EpollCancel(op)
    }
}

/// PollWait returns up to out.Len() completions: the done list first, then a
/// reap. A wait that would block takes the token and blocks in the kernel;
/// a thread that finds the token taken parks on the semaphore. A parker
/// returns only with completions or at its timeout: a post (the holder's
/// hand-off when it leaves the kernel, or a spare count) sends it back to
/// the token, and when it gets the token it blocks in the kernel itself
/// (a posted parker that reaped nothing and returned would leave the
/// other parkers asleep with the kernel wait free, so a later completion
/// would wake nobody).
///
/// The invariant (every PollWake and every
/// completion reach some caller while every idle carrier waits here): no
/// parker sleeps while the token is free and no post is on its way to some
/// parker. Three rules keep it:
///
/// 1. Dekker's pair, on every turn of the parker's loop: a parker counts
///    itself in sleepers, then tries the token, then sleeps; the holder
///    releases the token, then reads sleepers (all sequentially consistent).
///    In their total order either the parker's Cas comes after the holder's
///    Store(0), so it takes the token, or before, so its Add(1) precedes the
///    holder's Load, which sees it and posts. Without the Cas after the
///    Add, a holder could release the token and read sleepers between the
///    parker's failed first Cas and its Add(1) (PollTestPark's window,
///    ParkGap).
/// 2. A posted parker that finds the token taken sleeps again: the thread
///    that took it posts when it releases it (rule 1 again).
/// 3. A posted parker that leaves without the token (it found completions
///    on the done list) passes the post on when the token is free and
///    another parker sleeps (PassOn), so one post wakes a chain of parkers
///    that ends at one that takes the token or at no parker.
///
/// A spare count (a post whose parker took the token by its own Cas, or
/// timed out) is never a spurious return: the parker it wakes goes back to
/// the token by rule 2.
/// O(out.Len()) besides the wait; allocates nothing.
export PollWait(timeout_ns: i64, out: mut []*Op): int {
    if out.Len() == 0 {
        return 0
    }
    pp := PP()
    var k := TakeDone(out, 0)
    if k > 0 || timeout_ns == 0 {
        return Reap(out, k)
    }
    deadline := Deadline(timeout_ns)
    var posted := false
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // token: the Cas admits one thread into the kernel wait and the
        // Store(0) in HoldAndWait lets the next one in (mutual exclusion,
        // the release/acquire edge of a lock). sleepers: the Dekker pair
        // with token argued above.
        if (*pp).token.Cas(0, 1) {
            return HoldAndWait(timeout_ns, out)
        }
        TestPark()
        _ = (*pp).sleepers.Add(1)
        for {
            if (*pp).token.Cas(0, 1) {
                // Edge: none; this parker holds the token now and stops
                // counting itself (a stale count costs one spare post, rule 2).
                _ = (*pp).sleepers.Add(-1)
                return HoldAndWait(Left(deadline), out)
            }
            if !SemWait(&(*pp).sem, Left(deadline)) {
                break
            }
            posted = true
            // done_n: a completion queued outside a kernel reap (an epoll
            // cancel, an overflow) is handed out now; its pusher's
            // PollWake still reaches the token holder.
            // Edge: PushDone's Add, before its PollWake; a stale 0 is covered
            // by that wake.
            if (*pp).done_n.Load() != 0 {
                break
            }
        }
        // Edge: none; this parker's count leaves, and PassOn's Load must not
        // see it.
        _ = (*pp).sleepers.Add(-1)
    }
    k = Reap(out, TakeDone(out, 0))
    if posted {
        PassOn()
    }
    return k
}

// PassOn is rule 3 of PollWait: a parker that took a post and leaves
// without the token posts again when the token is free and a parker still
// sleeps. Its sleepers.Add(-1) came first, so it does not count itself;
// the loads are sequentially consistent, and a parker that counts itself
// after the sleepers Load tries the token after it (rule 1), while a
// holder that takes the token before the token Load posts on its release.
PassOn() {
    pp := PP()
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // Edge: rule 3's Dekker pair: this parker's sleepers.Add(-1) came
        // first, and a parker that counts itself after this Load tries the
        // token itself (rule 1).
        if (*pp).sleepers.Load() > 0 && (*pp).token.Load() == 0 {
            SemPost(&(*pp).sem, 1)
        }
    }
}

// HoldAndWait is the token holder's turn: block in the kernel, release the
// token, then post one parker if any counted itself: the posted parker
// takes the token and blocks in the kernel in its turn (rule 2), so no
// completion waits for a parked thread's timeout. One post per release:
// the other parkers stay asleep, covered by the new holder's release.
HoldAndWait(timeout_ns: i64, out: mut []*Op): int {
    pp := PP()
    k := BlockingWait(timeout_ns, out)
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // Edge: the Store(0) releases the token to the next Cas; the sleepers
        // Load after it is the releaser's half of the Dekker pair (rule 1).
        (*pp).token.Store(0)
        if (*pp).sleepers.Load() > 0 {
            SemPost(&(*pp).sem, 1)
        }
    }
    return k
}

// TestPark runs PollTestPark's callback when the hook is on.
TestPark() {
    pp := PP()
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // park_on's Load pairs with PollTestPark's Store(1), which comes
        // after park_cb and park_arg are written: the hook's publication
        // edge. parked is a counter.
        if (*pp).park_on.Load() == 1 {
            f := (*pp).park_cb
            f((*pp).park_arg)
            _ = (*pp).parked.Add(1)
        }
    }
}

// Reap collects completions without blocking into out[k..].
Reap(out: mut []*Op, k: int): int {
    return if IsUring() { UringReap(out, k, false) } else { EpollReap(0, out, k) }
}

// BlockingWait is the token holder's wait in the kernel.
BlockingWait(timeout_ns: i64, out: mut []*Op): int {
    return if IsUring() { UringWait(timeout_ns, out) } else { EpollReap(timeout_ns, out, 0) }
}

/// PollWake makes one blocked PollWait return: the eventfd write wakes the
/// thread in the kernel, or makes the next kernel wait return at once.
/// Async-signal-safe: one write. It posts no parker (such a post would
/// make a second carrier return with nothing, and the holder's hand-off a
/// third): PollWait's invariant says a parker sleeps only while some thread
/// holds the token or a post is on its way to a parker that will take it, so
/// the eventfd write always reaches a thread that waits in the kernel, now or
/// next. Only that thread drains the eventfd (EpollReap drains in blocking
/// waits only; UringReap leaves the wake's CQE to the token holder), so a
/// busy thread's non-blocking poll cannot swallow the wake.
/// Several wakes before the drain return one waiter (the eventfd counter is
/// read whole), which "one blocked PollWait" allows.
/// O(1): at most one system call; allocates nothing.
export PollWake() {
    // Edge: the init's Store of evfd. Before PollInit there is no eventfd (a
    // signal can arrive that early): this Load reads 0, the process's
    // standard input, which is never written here.
    ev := Poll.evfd.Load()
    if ev == 0 {
        return
    }
    var one: u64 = 1
    unsafe "system call; one is the eventfd counter increment" {
        _ = Syscall(Nrs().write, Trunc[uint](ev), raw.Addr(raw.OfMut(mut one)), 8, 0, 0, 0)
    }
}

/// PollTestHook installs (on) or removes EdgeInject's controlled schedule
/// on the epoll backend and returns how many times it ran so far: after the
/// first EAGAIN of a try, cb(arg) runs (a test makes data arrive there) and the
/// edge it causes is reaped at once, before the op is recorded. Without the
/// sequence check of poll_epoll.mk the op would then wait for an edge that
/// has already been consumed. Off, it costs one atomic load per EAGAIN. The
/// other backends have no edge to lose and never run cb. For tests only.
/// O(1); allocates nothing.
export PollTestHook(cb: fn(*u8), arg: *u8, on: bool): u64 {
    pp := PP()
    unsafe "the state page; set before any op that the hook can see" {
        // test_cb and test_arg are written before test_on's Store(1), and
        // InjectEdge reads them after its Load sees 1: the publication
        // edge of the hook (sequentially consistent store and load).
        if on {
            (*pp).test_cb = cb
            (*pp).test_arg = arg
            (*pp).test_on.Store(1)
        } else {
            (*pp).test_on.Store(0)
        }
        return (*pp).injected.Load()
    }
}

/// PollTestPark installs (on) or removes a controlled schedule for the
/// one-blocker rule and returns how many times it ran so far: a PollWait
/// that finds another thread blocking in the kernel runs cb(arg) before it
/// counts itself a sleeper, so a test can make the blocker return inside
/// that window (ParkGap). Off, it costs one atomic load per park. Windows
/// has no parked waiters (every caller waits on the port) and never runs
/// cb. For tests only.
/// O(1); allocates nothing.
export PollTestPark(cb: fn(*u8), arg: *u8, on: bool): u64 {
    pp := PP()
    unsafe "the state page; set before the waits the hook should see" {
        // park_cb and park_arg are written before park_on's Store(1), read
        // by TestPark after its Load sees 1 (the publication edge).
        if on {
            (*pp).park_cb = cb
            (*pp).park_arg = arg
            (*pp).park_on.Store(1)
        } else {
            (*pp).park_on.Store(0)
        }
        return (*pp).parked.Load()
    }
}

// Timespec writes ns as a struct timespec at p (16 bytes).
Timespec(p: *u8, ns: i64) {
    Put64(p, 0, Trunc[u64](ns / 1_000_000_000))
    Put64(p, 8, Trunc[u64](ns % 1_000_000_000))
}

/// Waits as PollWait does. Directed main-thread wakes are macOS-only.
/// O(1) besides the wait; allocates nothing.
export PollWaitMain(timeout_ns: i64, out: mut []*Op): int {
    return PollWait(timeout_ns, out)
}

/// Directed main-thread wakes are macOS-only. Returns false. O(1).
export PollTakeMainWake(): bool {
    return false
}

/// Directed main-thread wakes are macOS-only. O(1); does nothing.
export PollWakeMain() {}
