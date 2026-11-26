// Why: every task that waits on I/O parks on the poller; on macOS that is
// kqueue, and it needs one rule for which thread blocks in the kernel and
// one place where completions made outside a kernel reap wait.
//
// What: PollInit (RaiseFdLimit, the kqueue, the descriptor table), the
// state page PollPage, the spinlocks, the done list, PollSupports,
// PollAttach, PollDetach, PollSubmit, PollCancel, PollWait, PollWake,
// PollTestHook, PollTestPark, the reap KqReap and the byte-offset helpers
// Get16 to Put64. The per-operation tries are in poll_ops.mk, the child
// waits in poll_proc.mk.
//
// How: one thread at a time blocks in kevent; the others park on a
// semaphore, so one completion never wakes every idle carrier. Descriptors
// are registered once for EVFILT_READ and EVFILT_WRITE with EV_CLEAR (edge
// semantics). An operation is tried at once; only EAGAIN records it on its
// descriptor's wait list, and the handler (on whichever thread reaps the
// edge) retries it, so a completion is final. No edge is lost: each
// direction counts its edges (seq), and a submitter records its op only if
// seq is unchanged after the EAGAIN, under the descriptor's lock. The
// records are a table indexed by fd (MaxFds records of 64 bytes), mapped
// once; untouched pages cost nothing. PollWake triggers an EVFILT_USER
// event. A child wait is an EVFILT_PROC NOTE_EXIT registration on the pid,
// its op on a list keyed by pid (never in the event's udata), reaped with
// waitpid once the event comes. The done list holds ops completed outside a
// kernel reap (a cancel, a completion that did not fit a caller's out
// slice); PollWait hands them out first, and an op is on at most one list
// and leaves it under that list's lock, so none is lost and none is
// delivered twice.

import std.core
import std.raw
import std.sys.lc

struct PollState {
    // The backend PollInit chose (3, kqueue) and the kqueue descriptor plus
    // one (0 before PollInit), as atomics so PollWake (a signal handler may
    // call it) reads them with no lock. Both are written once by PollInit
    // before any other poll call, and every later reader is ordered after
    // it by the sequentially consistent store and load.
    backend: raw.Atomic[u32]
    kq: raw.Atomic[u64]
    page: raw.Atomic[u64]
}

var PollShared: PollState = PollState(backend: raw.Atomic[u32].New(0), kq: raw.Atomic[u64].New(0),
    page: raw.Atomic[u64].New(0))

// PollPage is the poller's state, in one committed page.
struct PollPage {
    kq: i32
    // token is 1 while one thread blocks in the kernel; sleepers
    // counts the threads parked on sem instead.
    token: raw.Atomic[u32]
    sleepers: raw.Atomic[i64]
    sem: Sem
    main_sem: Sem
    main_waiter: raw.Atomic[u32]
    main_pending: raw.Atomic[u32]
    done_lock: raw.Atomic[u32]
    done_head: *Op
    done_tail: *Op
    // How many ops the done list holds: written under done_lock, read with
    // no lock by TakeDone's fast path and the parker (the list head itself
    // is a plain field, read only under the lock).
    done_n: raw.Atomic[u64]
    // The descriptor table (MaxFds records of DescBytes).
    descs: *u8
    // The child waits: a list linked through OsNext, under proc_lock.
    proc_lock: raw.Atomic[u32]
    proc_head: *Op
    // EdgeInject's controlled schedule (PollTestHook): when on, the first
    // EAGAIN of each try runs test_cb (which makes data arrive) and then
    // reaps the edge it caused, before the op is recorded: the
    // interleaving in which another thread consumes the edge in the window.
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

// kqueue's filters (int16, held here as their u16 bits), flags and notes,
// from Darwin's <sys/event.h>.
const FiltRead: u16 = 0xFFFF
const FiltWrite: u16 = 0xFFFE
const FiltProc: u16 = 0xFFFB
const FiltUser: u16 = 0xFFF6
// FiltTimer is EVFILT_TIMER (-7); its data is the period in milliseconds.
const FiltTimer: u16 = 0xFFF9
// TickMs is the period at which an unwatched terminal's pending OpReady is
// tried again (UnwatchedArm).
const TickMs: u64 = 10
const EvAdd: u16 = 0x1
const EvDelete: u16 = 0x2
const EvOneShot: u16 = 0x10
const EvClear: u16 = 0x20
const EvReceipt: u16 = 0x40
const EvEof: u16 = 0x8000
const NoteTrigger: u32 = 0x0100_0000
const NoteExitStatus: u32 = 0x0400_0000
const NoteExit: u32 = 0x8000_0000
// WakeIdent is the EVFILT_USER event's ident (its own namespace: kqueue
// keys an event by ident and filter).
const WakeIdent: u64 = 0
// struct kevent is 32 bytes on arm64 and x86_64: ident at 0, filter at 8,
// flags at 10, fflags at 12, data at 16, udata at 24.
const KevBytes: int = 32
// EventWords is KqReap's event buffer in u64 words: 64 events.
const EventWords: int = 256
const EvMax: i32 = 64
// MaxWaitSec caps one kevent timeout: Darwin answers EINVAL for a longer
// one, so a longer wait returns early with nothing, which PollWait allows.
const MaxWaitSec: i64 = 1_000_000

// Darwin errno values (<sys/errno.h>) and the other constants the poller
// passes.
const Esrch: i64 = 3
const Eintr: i64 = 4
const Echild: i64 = 10
const Emfile: i64 = 24
const Eagain: i64 = 35
const Einprogress: i64 = 36
const Einval: i64 = 22
const Econnaborted: i64 = 53
const Enotconn: i64 = 57
const FSetFd: i32 = 2
const FGetFl: i32 = 3
const FSetFl: i32 = 4
const FdCloExec: i32 = 1
const ONonBlock: i32 = 0x4
const SolSocket: i32 = 0xFFFF
const SoError: i32 = 0x1007
const SoNoSigPipe: i32 = 0x1022
const MsgTrunc: i32 = 0x10
const RlimitNofile: i32 = 8
const PPid: i32 = 1
const WNoHang: i32 = 0x1
const WExited: i32 = 0x4
const WNoWait: i32 = 0x20
const PollIn: u16 = 0x1
const PollNval: u16 = 0x20

// MaxFds bounds the descriptors the poller serves (a larger fd is refused
// by PollAttach, so it goes to the pool, which serves it correctly).
const MaxFds: int = 1_048_576
const DescBytes: int = 64
// A record: lock at 0, read seq at 4, write seq at 8, the registered
// filters at 12 (1 read, 2 write, 4 an unwatched character device, 8 its
// tick timer armed), then the read list (head 16, tail 24) and the write
// list (head 32, tail 40).
const DLock: int = 0
const DSeqRead: int = 4
const DSeqWrite: int = 8
const DAttach: int = 12
const DHeadRead: int = 16
const DHeadWrite: int = 32

Get16(p: *u8, off: int): u16 {
    unsafe "the caller's structure holds off + 2 bytes" {
        return raw.ReadUnaligned(raw.PtrCast[u8, u16](raw.Offset(p, off)))
    }
}

Get32(p: *u8, off: int): u32 {
    unsafe "the caller's structure holds off + 4 bytes" {
        return raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.Offset(p, off)))
    }
}

Get64(p: *u8, off: int): u64 {
    unsafe "the caller's structure holds off + 8 bytes" {
        return raw.ReadUnaligned(raw.PtrCast[u8, u64](raw.Offset(p, off)))
    }
}

Put16(p: *u8, off: int, v: u16) {
    unsafe "the caller's structure holds off + 2 bytes" {
        raw.Write(raw.PtrCast[u8, u16](raw.Offset(p, off)), v)
    }
}

Put32(p: *u8, off: int, v: u32) {
    unsafe "the caller's structure holds off + 4 bytes" {
        raw.Write(raw.PtrCast[u8, u32](raw.Offset(p, off)), v)
    }
}

Put64(p: *u8, off: int, v: u64) {
    unsafe "the caller's structure holds off + 8 bytes" {
        raw.Write(raw.PtrCast[u8, u64](raw.Offset(p, off)), v)
    }
}

// PP is the state page (null before PollInit).
PP(): *PollPage {
    unsafe "the page PollInit made" {
        // Edge: PollInit stores the address after it filled the page, so a Load
        // that reads it sees the page.
        return raw.FromAddr[PollPage](Trunc[uint](PollShared.page.Load()))
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

// SpinUnlock is a release store (order 2).
// Edge: release to the next SpinLock's successful Cas(0, 1), which reads
// this 0. It orders no later load of this thread, so no protocol may use it
// as one half of a store-load (Dekker) pair: after each unlock here comes a
// return, a system call, another lock's Cas, or a sequentially consistent
// pair on another word; the done list's Dekker partner is done_n's Add, an
// RMW before the unlock, never the lock word.
SpinUnlock(w: *raw.Atomic[u32]) {
    unsafe "w is a lock word this thread holds" {
        // Edge: release; this holder's writes happen before the next
        // holder's acquiring Cas in SpinLock.
        (*w).StoreOrd(0, 2)
    }
}

// OsArea is the first byte of op.os. macOS layout: the wait, done or child
// list link at 0, the stage (1 connecting) at 12, a sockaddr at 16 (32
// bytes) with its length at 48, a datagram receive's struct msghdr at 72
// (48 bytes), its struct iovec at 128 (16 bytes) and the byte an empty
// receive takes its datagram into at 144. Op is C layout: os
// starts at byte 136, so these words are naturally aligned.
OsArea(op: *Op): *u8 {
    unsafe "op is a live operation" {
        return raw.Data((*op).os[..])
    }
}

const OsNext: int = 0
const OsStage: int = 12
const OsAddr: int = 16
const OsAddrN: int = 48
const OsMsg: int = 72
const OsIov: int = 128
const OsScratch: int = 144

Next(op: *Op): *Op {
    unsafe "the link holds a live op's address or 0" {
        return raw.FromAddr[Op](Trunc[uint](Get64(OsArea(op), OsNext)))
    }
}

SetNext(op: *Op, n: *Op) {
    Put64(OsArea(op), OsNext, u64(raw.Addr(n)))
}

// DgramDone is a finished datagram op's result from the count r: a receive
// decodes its source address from OsAddr into op.addr, and a datagram the
// kernel cut to len (trunc: msghdr's MSG_TRUNC) is reported as len with
// os 1.
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

/// PollInit starts the poller and returns its backend: 3, kqueue (the
/// number Linux's epoll has: a readiness poller whose files go to the
/// blocking pool); 0 on failure (fatal at start). force_epoll is ignored:
/// kqueue is the only backend.
/// Once, at start: O(1) system calls; maps the descriptor table, kept until
/// exit.
export PollInit(force_epoll: bool): u32 {
    RaiseFdLimit()
    p := Reserve(4096, 4096)
    if raw.IsNull(p) || !Commit(p, 4096) {
        return 0
    }
    unsafe "the fresh state page" {
        pp := raw.PtrCast[u8, PollPage](p)
        if !SemInit(mut (*pp).sem) || !SemInit(mut (*pp).main_sem) {
            return 0
        }
        // PollInit runs once at startup, before any other poll call: the
        // page, backend and kq stores are the publication edges every later
        // Load (sequentially consistent) is ordered after.
        PollShared.page.Store(u64(raw.Addr(p)))
    }
    if !KqInit() {
        return 0
    }
    // Edge: the backend is stored once, in PollInit, before any other thread
    // polls.
    PollShared.backend.Store(3)
    return 3
}

// RaiseFdLimit sets the soft RLIMIT_NOFILE to the hard limit, capped at
// kern.maxfilesperproc (setrlimit refuses a soft limit above it, and the
// hard limit is often RLIM_INFINITY), once, at start, before any
// descriptor is made. The poller watches any number of descriptors, and
// the default soft limit (256) would end a server at that many connections
// with EMFILE. Go's runtime does the same at start (syscall's
// rlimit_darwin.go). FdLimit keeps the original pair for a spawn that
// gives a child the original soft limit back (a program that uses select()
// relies on a soft limit no larger than FD_SETSIZE); a spawn that does not
// passes the raised one on. A failure leaves the limit as it was.
RaiseFdLimit() {
    var lim: [2]u64 = [0; 2]
    var name: [32]u8 = [0; 32]
    if !CPath("kern.maxfilesperproc", mut name[..]) {
        return
    }
    unsafe "lim is a struct rlimit { cur, max }; max is a u32 sysctl value and n its size" {
        p := raw.PtrCast[u64, u8](raw.DataMut(mut lim[..]))
        if getrlimit(RlimitNofile, p) != 0 {
            return
        }
        var target := lim[1]
        var max: [1]u32 = [0]
        var n: [1]uint = [4]
        if sysctlbyname(raw.Data(name[..]), raw.PtrCast[u32, u8](raw.DataMut(mut max[..])), raw.DataMut(mut n[..]),
            raw.Null[u8](), 0) == 0 && n[0] == 4 && max[0] != 0 && target > u64(max[0]) {
            target = u64(max[0])
        }
        if lim[0] >= target {
            return
        }
        cur := lim[0]
        lim[0] = target
        if setrlimit(RlimitNofile, p) == 0 {
            // Edge: stored once, here in PollInit, before any spawn; a
            // child's Loads come after them.
            FdLimit.cur.Store(cur)
            FdLimit.max.Store(lim[1])
        }
    }
}

// FdLimitState is the RLIMIT_NOFILE pair RaiseFdLimit replaced, for a
// child to get back; max is 0 when nothing was raised. Both
// are stored once, in PollInit, before any spawn can run, and a spawn's
// loads are ordered after the stores (sequentially consistent).
struct FdLimitState {
    cur: raw.Atomic[u64]
    max: raw.Atomic[u64]
}

var FdLimit: FdLimitState = FdLimitState(cur: raw.Atomic[u64].New(0), max: raw.Atomic[u64].New(0))

// SetEv writes the i-th struct kevent at p.
SetEv(p: *u8, i: int, ident: u64, filter: u16, flags: u16, fflags: u32) {
    unsafe "p holds at least i + 1 struct kevent records" {
        e := raw.Offset(p, i * KevBytes)
        Put64(e, 0, ident)
        Put16(e, 8, filter)
        Put16(e, 10, flags)
        Put32(e, 12, fflags)
        Put64(e, 16, 0)
        Put64(e, 24, 0)
    }
}

// UnwatchedArm arms, or with arm false disarms, the tick of the unwatched
// character device fd: a periodic EVFILT_TIMER of TickMs whose ident is
// fd. kqueue serves no /dev/tty on macOS (libuv's select thread exists for
// the same reason), so a pending OpReady on one is tried again at each
// tick (KqReap) instead of at an edge. Called under fd's record lock, with
// the armed bit (8) read and set there, so a tick is never disarmed while
// an op waits for it. O(1): at most one system call; allocates nothing.
UnwatchedArm(d: *u8, fd: uint, arm: bool) {
    bits := Get32(d, DAttach)
    armed := (bits & 8) != 0
    if arm == armed {
        return
    }
    var ev: [4]u64 = [0; 4]
    unsafe "ev holds one struct kevent" {
        p := raw.PtrCast[u64, u8](raw.DataMut(mut ev[..]))
        SetEv(p, 0, u64(fd), FiltTimer, if arm { EvAdd } else { EvDelete }, 0)
        Put64(p, 16, TickMs)
        _ = kevent(Kq(), p, 1, raw.Null[u8](), 0, raw.Null[i64]())
    }
    Put32(d, DAttach, if arm { bits | 8 } else { bits & ~u32(8) })
}

// KqApply applies the n changes at ch, each with EV_RECEIPT, and receives
// their receipts into rc (n records): the receipt count, or -1 when kevent
// itself failed. EINTR is repeated (every change here is idempotent or its
// repeat's ENOENT is ignored).
KqApply(kq: i32, ch: *u8, n: int, rc: *u8): int {
    for {
        var r: i32 = 0
        unsafe "ch and rc hold n struct kevent records each" {
            r = kevent(kq, ch, Trunc[i32](n), rc, Trunc[i32](n), raw.Null[i64]())
        }
        if r >= 0 {
            return int(r)
        }
        if Errno() != Eintr {
            return -1
        }
    }
}

// KqChange applies one change with no receipt: 0, or the errno.
KqChange(kq: i32, ident: u64, filter: u16, flags: u16, fflags: u32): i64 {
    var ev: [4]u64 = [0; 4]
    unsafe "ev holds one struct kevent" {
        p := raw.PtrCast[u64, u8](raw.DataMut(mut ev[..]))
        SetEv(p, 0, ident, filter, flags, fflags)
        for {
            r := kevent(kq, p, 1, raw.Null[u8](), 0, raw.Null[i64]())
            if r >= 0 {
                return 0
            }
            e := Errno()
            if e != Eintr {
                return e
            }
        }
    }
}

// KqInit makes the kqueue (close-on-exec), the descriptor table and the
// wake event (EVFILT_USER with EV_CLEAR).
KqInit(): bool {
    pp := PP()
    unsafe "libSystem calls; the state page" {
        kq := kqueue()
        if kq < 0 {
            return false
        }
        _ = fcntl(kq, FSetFd, FdCloExec)
        d := Map(raw.Null[u8](), MaxFds * DescBytes, ProtRW, 0)
        if raw.IsNull(d) {
            _ = lc.CloseFd(kq)
            return false
        }
        if KqChange(kq, WakeIdent, FiltUser, EvAdd | EvClear, 0) != 0 {
            Unmap(d, MaxFds * DescBytes)
            _ = lc.CloseFd(kq)
            return false
        }
        (*pp).kq = kq
        (*pp).descs = d
        // Published before PollInit returns (see PollInit's edge).
        PollShared.kq.Store(u64(Trunc[u32](kq)) + 1)
    }
    return true
}

// Kq is the kqueue descriptor.
Kq(): i32 {
    unsafe "PollInit ran, so PP() is the page it made" {
        return (*PP()).kq
    }
}

// Desc is fd's record.
Desc(fd: uint): *u8 {
    unsafe "fd < MaxFds, checked by every caller" {
        return raw.Offset((*PP()).descs, Trunc[int](fd) * DescBytes)
    }
}

DescLock(d: *u8): *raw.Atomic[u32] {
    unsafe "d is a live descriptor record, and DLock is its lock word's offset" {
        return raw.PtrCast[u8, raw.Atomic[u32]](raw.Offset(d, DLock))
    }
}

// ClearDesc empties d's wait lists and sets its registered filters.
ClearDesc(d: *u8, bits: u32) {
    SpinLock(DescLock(d))
    for off in [DHeadRead, DHeadRead + 8, DHeadWrite, DHeadWrite + 8] {
        Put64(d, off, 0)
    }
    Put32(d, DAttach, bits)
    SpinUnlock(DescLock(d))
}

// KqAdd registers fd for EVFILT_READ and EVFILT_WRITE with EV_CLEAR and
// clears its record. A regular file or a directory (kqueue reports a
// regular file always ready, so it stays on the pool), an fd beyond the
// table, and an fd whose read filter kqueue refuses are refused, except a
// character device (a terminal kqueue does not serve), which is attached
// unwatched (filter bit 4): its ops are tried once and, when they would
// wait, end Unsupported. A write filter kqueue refuses leaves the fd's
// write-direction ops refused at submission (Other). No op ever waits for
// an edge that cannot come.
KqAdd(fd: uint): bool {
    if fd >= Trunc[uint](MaxFds) {
        return false
    }
    var st: [18]u64 = [0; 18]
    if StatFd(fd, mut st) != 0 {
        return false
    }
    t := StatMode(&st) & 0o170000
    if t == 0o100000 || t == 0o040000 {
        return false
    }
    d := Desc(fd)
    ClearDesc(d, 0)
    var ch: [8]u64 = [0; 8]
    var rc: [8]u64 = [0; 8]
    var bits: u32 = 0
    unsafe "ch and rc hold two struct kevent records each" {
        cp := raw.PtrCast[u64, u8](raw.DataMut(mut ch[..]))
        rp := raw.PtrCast[u64, u8](raw.DataMut(mut rc[..]))
        SetEv(cp, 0, u64(fd), FiltRead, EvAdd | EvClear | EvReceipt, 0)
        SetEv(cp, 1, u64(fd), FiltWrite, EvAdd | EvClear | EvReceipt, 0)
        n := KqApply(Kq(), cp, 2, rp)
        if n < 0 {
            return false
        }
        for i in 0..n {
            e := raw.Offset(rp, i * KevBytes)
            // A receipt's data is 0 on success, else the change's errno.
            if Get64(e, 16) == 0 {
                f := Get16(e, 8)
                if f == FiltRead {
                    bits = bits | 1
                } else if f == FiltWrite {
                    bits = bits | 2
                }
            }
        }
    }
    if (bits & 1) == 0 {
        if t == 0o020000 {
            ClearDesc(d, bits | 4)
            return true
        }
        if bits != 0 {
            _ = KqChange(Kq(), u64(fd), FiltWrite, EvDelete, 0)
        }
        return false
    }
    ClearDesc(d, bits)
    return true
}

// KqDel removes fd's filters and clears its record (no op may be pending
// on it: the contract's no-close-while-pending rule). An fd that is not
// registered costs one kevent whose ENOENT is ignored.
KqDel(fd: uint) {
    if fd >= Trunc[uint](MaxFds) {
        return
    }
    d := Desc(fd)
    SpinLock(DescLock(d))
    bits := Get32(d, DAttach)
    // An unwatched terminal's tick stops with it.
    UnwatchedArm(d, fd, false)
    SpinUnlock(DescLock(d))
    if bits != 0 {
        var ch: [8]u64 = [0; 8]
        var rc: [8]u64 = [0; 8]
        unsafe "ch and rc hold two struct kevent records each" {
            cp := raw.PtrCast[u64, u8](raw.DataMut(mut ch[..]))
            SetEv(cp, 0, u64(fd), FiltRead, EvDelete | EvReceipt, 0)
            SetEv(cp, 1, u64(fd), FiltWrite, EvDelete | EvReceipt, 0)
            _ = KqApply(Kq(), cp, 2, raw.PtrCast[u64, u8](raw.DataMut(mut rc[..])))
        }
    }
    ClearDesc(d, 0)
}

/// PollSupports reports whether the poller runs ops of kind: sockets,
/// pipes and other descriptors kqueue watches (read, write, accept,
/// connect, recv, send), the datagram kinds, child waits and readiness
/// waits; files, opens, renames, removes, syncs and locks go to the
/// blocking pool.
/// O(1); allocates nothing.
export PollSupports(kind: u32): bool {
    if kind == OpSendTo || kind == OpRecvFrom || kind == OpProcWait || kind == OpReady {
        return true
    }
    return kind >= OpRead && kind <= OpSend
}

/// PollAttach readies h for the poller: registers it for kqueue's read and
/// write filters with EV_CLEAR. A regular file, a directory, a descriptor
/// past the table and one kqueue refuses are refused (false), except a
/// character device kqueue refuses (a terminal), which is attached
/// unwatched: an op on it is tried once and ends Unsupported when it would
/// wait. It does not make h non-blocking: the caller does (SocketOpen's
/// sockets are).
/// O(1) system calls; allocates nothing.
export PollAttach(h: uint): bool {
    return KqAdd(h)
}

/// PollDetach takes h out of the poller before it is closed or lent:
/// removes both filters and clears its record.
/// O(1) system calls; allocates nothing.
export PollDetach(h: uint) {
    KqDel(h)
}

/// PollSubmit starts op: true when it is pending (its completion comes from
/// PollWait), false when it finished now with op.res set (Other for a kind
/// the poller does not support, or a descriptor not attached for the
/// direction the op waits in).
/// O(1) system calls (one try, a connect's check and the descriptor's lock,
/// repeated while edges arrive meanwhile; a child wait's registration and
/// peek); allocates nothing (op is the caller's until its completion).
export PollSubmit(op: *Op): bool {
    unsafe "op is the caller's operation; from here the one-owner rule holds" {
        raw.Fill(OsArea(op), 0, 160)
        if !PollSupports((*op).kind) {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
        if (*op).kind == OpProcWait {
            return ProcSubmit(op)
        }
    }
    return KqSubmit(op)
}

/// PollCancel asks for op's cancellation; its completion still arrives
/// through PollWait (Canceled unless it finished first).
/// O(1) system calls; allocates nothing.
export PollCancel(op: *Op) {
    unsafe "op is pending (the one-owner rule): its record lives" {
        if (*op).kind == OpProcWait {
            ProcCancel(op)
        } else {
            KqCancel(op)
        }
    }
}

/// PollWait returns up to out.Len() completions: the done list first, then a
/// reap. A wait that would block takes the token and blocks in kevent; a
/// thread that finds the token taken parks on the semaphore. A parker
/// returns only with completions or at its timeout: a post (the holder's
/// hand-off when it leaves the kernel, or a spare count) sends it back to
/// the token, and when it gets the token it blocks in the kernel itself
/// (a posted parker that reaped nothing and returned would leave the
/// other parkers asleep with the kernel wait free, so a later completion
/// would wake nobody).
///
/// The invariant (every PollWake and every completion reach some caller
/// while every idle carrier waits here): no parker sleeps while the token
/// is free and no post is on its way to some parker. Three rules keep it:
///
/// 1. Dekker's pair, on every turn of the parker's loop: a parker counts
///    itself in sleepers, then tries the token, then sleeps; the holder
///    releases the token, then reads sleepers (all sequentially consistent).
///    In their total order either the parker's Cas comes after the holder's
///    Store(0), so it takes the token, or before, so its Add(1) precedes the
///    holder's Load, which sees it and posts.
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
    return PollWaitOn(timeout_ns, out, false)
}

/// Waits as PollWait does, with an additional directed wake for carrier zero.
/// O(1) besides the wait; allocates nothing.
export PollWaitMain(timeout_ns: i64, out: mut []*Op): int {
    return PollWaitOn(timeout_ns, out, true)
}

/// Consumes a directed wake before running main-thread work. Only the
/// initial thread may consume it. O(1); allocates nothing.
export PollTakeMainWake(): bool {
    unsafe "the poll page was published at program startup" {
        return (*PP()).main_pending.Swap(0) != 0
    }
}

/// Records main-thread work and wakes its scheduler if it is idle.
/// O(1) system calls; allocates nothing.
export PollWakeMain() {
    pp := PP()
    unsafe "the poll page was published at program startup" {
        // Publish before waking either place the main thread can sleep:
        // kevent with the token, or its own semaphore without it.
        (*pp).main_pending.Store(1)
        WakeMainParker(pp)
        PollWake()
    }
}

WakeMainParker(pp: *PollPage) {
    unsafe "the live poll page; only the main thread parks on its semaphore" {
        if (*pp).main_waiter.Load() != 0 && (*pp).main_waiter.Cas(1, 0) {
            SemPost(&(*pp).main_sem, 1)
        }
    }
}

PollWaitOn(timeout_ns: i64, out: mut []*Op, main: bool): int {
    if out.Len() == 0 {
        return 0
    }
    pp := PP()
    var k := TakeDone(out, 0)
    if k > 0 || timeout_ns == 0 || (main && MainWakePending(pp)) {
        return KqReap(0, out, k)
    }
    deadline := Deadline(timeout_ns)
    var posted := false
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // token: the Cas admits one thread into the kernel wait and the
        // Store(0) in HoldAndWait lets the next one in (mutual exclusion,
        // the release/acquire edge of a lock). sleepers: the Dekker pair
        // with token argued above.
        if (*pp).token.Cas(0, 1) {
            return HoldAndWait(if main && MainWakePending(pp) { 0 } else { timeout_ns }, out)
        }
        if main {
            return WaitMainParker(pp, deadline, out)
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
            // done_n: a completion queued outside a kernel reap (a cancel,
            // an overflow) is handed out now; its pusher's PollWake still
            // reaches the token holder.
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
    k = KqReap(0, out, TakeDone(out, 0))
    if posted {
        PassOn()
    }
    return k
}

MainWakePending(pp: *PollPage): bool {
    unsafe "the poll page was published at program startup" {
        return (*pp).main_pending.Load() != 0
    }
}

WaitMainParker(pp: *PollPage, deadline: i64, out: mut []*Op): int {
    unsafe "only the main thread announces itself on the poll page's main semaphore" {
        for {
            // If a signal missed the announcement, its pending bit is
            // visible here. If the token release missed it, the Cas can take
            // the token without waiting.
            (*pp).main_waiter.Store(1)
            if MainWakePending(pp) {
                (*pp).main_waiter.Store(0)
                break
            }
            if (*pp).token.Cas(0, 1) {
                (*pp).main_waiter.Store(0)
                return HoldAndWait(if MainWakePending(pp) { 0 } else { Left(deadline) }, out)
            }
            if !SemWait(&(*pp).main_sem, Left(deadline)) {
                (*pp).main_waiter.Store(0)
                break
            }
        }
    }
    return KqReap(0, out, TakeDone(out, 0))
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

// HoldAndWait is the token holder's turn: block in kevent, release the
// token, then post one parker if any counted itself: the posted parker
// takes the token and blocks in the kernel in its turn (rule 2), so no
// completion waits for a parked thread's timeout. One post per release:
// the other parkers stay asleep, covered by the new holder's release.
HoldAndWait(timeout_ns: i64, out: mut []*Op): int {
    pp := PP()
    k := KqReap(timeout_ns, out, 0)
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
        // Edge: the Store(0) releases the token to the next Cas; the sleepers
        // Load after it is the releaser's half of the Dekker pair (rule 1).
        (*pp).token.Store(0)
        WakeMainParker(pp)
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

/// PollWake makes one blocked PollWait return: it triggers the EVFILT_USER
/// event, which wakes the thread in kevent, or makes the next kernel wait
/// return at once. Async-signal-safe: one kevent, with errno kept for the
/// interrupted code. It posts no parker (such a post would make a second
/// carrier return with nothing): PollWait's invariant says a parker sleeps
/// only while some thread holds the token or a post is on its way to a
/// parker that will take it, so the trigger always reaches a thread that
/// waits in the kernel, now or next. The event is EV_CLEAR, so whichever
/// reap retrieves it consumes it; a non-blocking reap that does triggers it
/// again (KqReap), so a busy thread's poll cannot swallow the wake meant
/// for the blocked one. Several wakes before a retrieval return one waiter,
/// which "one blocked PollWait" allows.
/// O(1): at most one system call (repeated on EINTR); allocates nothing.
export PollWake() {
    // Edge: the init's Store of kq. Before PollInit there is no kqueue (a
    // signal can arrive that early): this Load reads 0, and nothing is done.
    k := PollShared.kq.Load()
    if k == 0 {
        return
    }
    unsafe "__error is this thread's errno word, restored after the call" {
        ep := __error()
        saved := *ep
        _ = KqChange(Trunc[i32](k - 1), WakeIdent, FiltUser, 0, NoteTrigger)
        raw.Write(ep, saved)
    }
}

/// PollTestHook installs (on) or removes EdgeInject's controlled schedule
/// and returns how many times it ran so far: after the first EAGAIN of a
/// try, cb(arg) runs (a test makes data arrive there) and the edge it
/// causes is reaped at once, before the op is recorded. Without the
/// sequence check of KqSubmit the op would then wait for an edge that has
/// already been consumed. Off, it costs one atomic load per EAGAIN. For
/// tests only.
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
/// that window (ParkGap). Off, it costs one atomic load per park. For tests
/// only.
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

// KqReap waits up to timeout_ns (0: not at all; negative: forever; capped
// at MaxWaitSec) for events, handles them, and returns the completions in
// out[k..]. An error (EINTR included) reaps no event. A read event with
// EV_EOF also runs the write side: a writer then sees EPIPE instead of
// waiting for a write edge kqueue may not send.
KqReap(timeout_ns: i64, out: mut []*Op, k: int): int {
    var events: [EventWords]u64 = [0; EventWords]
    var got := 0
    unsafe "events holds EvMax struct kevent records; ts is a struct timespec" {
        evp := raw.PtrCast[u64, u8](raw.DataMut(mut events[..]))
        var ts: [2]i64 = [0, 0]
        var tsp := raw.Null[i64]()
        if timeout_ns >= 0 {
            sec := timeout_ns / 1_000_000_000
            if sec >= MaxWaitSec {
                ts[0] = MaxWaitSec
            } else {
                ts[0] = sec
                ts[1] = timeout_ns % 1_000_000_000
            }
            tsp = raw.DataMut(mut ts[..])
        }
        r := kevent(Kq(), raw.Null[u8](), 0, evp, EvMax, tsp)
        if r > 0 {
            got = int(r)
        }
    }
    var n := TakeDone(out, k)
    for i in 0..got {
        unsafe "the i-th struct kevent the kernel wrote" {
            e := raw.Offset(raw.PtrCast[u64, u8](raw.Data(events[..])), i * KevBytes)
            ident := Get64(e, 0)
            filter := Get16(e, 8)
            flags := Get16(e, 10)
            if filter == FiltUser {
                // The wake: a blocking wait (the token holder's) consumes
                // it; a non-blocking reap triggers it again, so a PollWake
                // meant for the thread blocked in kevent still wakes it (Go's
                // netpoll relays it the same way).
                if timeout_ns == 0 {
                    _ = KqChange(Kq(), WakeIdent, FiltUser, 0, NoteTrigger)
                }
                continue
            }
            if filter == FiltProc {
                have := (Get32(e, 12) & NoteExit) != 0
                n = ProcEvent(ident, have, Cast[i64](Get64(e, 16)), out, n)
                continue
            }
            if ident >= u64(MaxFds) {
                continue
            }
            d := Desc(Trunc[uint](ident))
            if filter == FiltTimer {
                // An unwatched terminal's tick: its read list is tried as
                // at an edge, and the tick stops once nothing waits.
                n = Ready(d, DSeqRead, DHeadRead, out, n)
                SpinLock(DescLock(d))
                if Get64(d, DHeadRead) == 0 {
                    UnwatchedArm(d, Trunc[uint](ident), false)
                }
                SpinUnlock(DescLock(d))
                continue
            }
            if filter == FiltRead {
                n = Ready(d, DSeqRead, DHeadRead, out, n)
                if (flags & EvEof) != 0 {
                    n = Ready(d, DSeqWrite, DHeadWrite, out, n)
                }
            } else if filter == FiltWrite {
                n = Ready(d, DSeqWrite, DHeadWrite, out, n)
            }
        }
    }
    return n
}
