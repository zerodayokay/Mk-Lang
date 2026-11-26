// Why: io_uring lets one ring carry every socket, file and terminal wait of
// the process with few system calls; it is the default Linux poller.
//
// What: the io_uring backend: UringInit, UringSubmit (with TrySend),
// UringCancel, Resubmit, UringFinish (with SharedStep), UringReap and
// UringWait.
//
// How: one ring for the process, its SQ ring, CQ ring and SQE array mapped
// with mmap (one mapping for the two rings). A send is first tried once on
// the calling carrier (TrySend, MSG_DONTWAIT); every other op, and a send that
// would block, goes through the ring. Submission fills an SQE under the SQ
// lock, publishes the tail with a sequentially consistent store and enters
// the kernel; completions are reaped under the CQ lock by whoever polls. The
// wake is an eventfd with a read always pending on the ring. Nothing that
// runs under the CQ lock makes a system call that can block: the reap,
// UringFinish and its steps only read results and decide the next SQE,
// which the caller submits after the lock is released.
//
// user_data is the op's address (below 2^48 on both architectures) with a
// 16-bit submission generation above it, kept in op.os. ASYNC_CANCEL names
// that exact value and carries user_data 0, which the reaper ignores, so a
// cancel processed late cannot hit a newer op at the same stack address.
// The generation is defence in depth; the order alone already makes a hit
// impossible: a PollCancel runs while its op is alive (the task
// is still in Await: std.task unregisters the waiter under the lock its
// cancel runs under), so no other op can be at that address before the
// cancel SQE is published, and the SQ is consumed in tail order, so every
// SQE of a later op at that address reaches the kernel after the cancel
// was processed (ASYNC_CANCEL runs at submission, not on io-wq). A wrap of
// the 16 bits (65,536 submissions between UringCancel's load and its push)
// could only make the cancel name an older submission of the same, still
// live op, which Resubmit's cancel check covers (ResubCancel).
//
// Ownership: an op's SQE is consumed by the kernel, and from then until its
// CQE is reaped the kernel and this file own the op and its buffers; the
// reaper hands the op out exactly once (UringFinish), or submits its next
// step (the directory step of a remove, an EINTR without a cancel) and keeps
// it.
//
// A resubmission must not lose a cancel (Resubmit): a PollCancel that ran
// between the CQE and the new SQE named the old user_data and found
// nothing, so the resubmitter checks op.cancel after its push and cancels
// the new submission itself. Reading op after the push is safe only while
// nobody can hand the op out, so the resubmitter holds the handoff word at
// 1 across its push and check; a reaper that meets the new CQE meanwhile
// leaves the result in the op and moves the word to 2, and the resubmitter,
// whose release (1 to 0) then fails, finishes the op itself. Exactly one
// thread hands the op out, and the op is live whenever either reads it.

import std.core
import std.raw

const SetupCqSize: u32 = 0x08
const SetupClamp: u32 = 0x10
const FeatSingleMmap: u32 = 0x001
const FeatNoDrop: u32 = 0x002
const FeatSubmitStable: u32 = 0x004
const FeatRwCurPos: u32 = 0x008
const FeatExtArg: u32 = 0x100
const EnterGetEvents: uint = 0x01
const EnterExtArg: uint = 0x08
const OffSqRing: uint = 0
const OffSqes: uint = 0x1000_0000
const MapShared: uint = 0x01
const MapPopulate: uint = 0x8000

const OpcFsync: u8 = 3
const OpcPollAdd: u8 = 6
const OpcSendMsg: u8 = 9
const OpcRecvMsg: u8 = 10
const OpcAccept: u8 = 13
const OpcAsyncCancel: u8 = 14
const OpcConnect: u8 = 16
const OpcOpenAt: u8 = 18
const OpcRead: u8 = 22
const OpcWrite: u8 = 23
const OpcSend: u8 = 26
const OpcRecv: u8 = 27
const OpcRenameAt: u8 = 35
const OpcUnlinkAt: u8 = 36

// WakeTag is the user_data of the eventfd read (no op has address 1).
const WakeTag: u64 = 1
const AddrMask: u64 = 0xFFFF_FFFF_FFFF
const AcceptCloExec: u32 = 0x80000
// POLLHUP and POLLRDHUP in a POLL_ADD's result mask (SharedStep).
const PollHupMask: u32 = 0x10
const PollRdHupMask: u32 = 0x2000
// IORING_ACCEPT_DONTWAIT (Linux 6.10), in an ACCEPT SQE's ioprio: the
// accept never waits, whatever the file's O_NONBLOCK.
const AcceptDontWait: u16 = 2
// OsPollMask keeps a shared listener's POLL_ADD result for its ACCEPT step:
// the RECVMSG msghdr's bytes, which an accept never uses.
const OsPollMask: int = 72

// DontWaitState says whether this kernel takes AcceptDontWait: 0 not known
// yet, 1 it does, 2 it refuses it (EINVAL, kernels before 6.10). Racing
// reapers store the same answer.
struct DontWaitState {
    v: raw.Atomic[u32]
}

var DontWait: DontWaitState = DontWaitState(v: raw.Atomic[u32].New(0))
// MSG_DONTWAIT: TrySend's one non-blocking send on a blocking socket.
const MsgDontWait: uint = 0x40

// MapFd is mmap of a file at offset; null on failure.
MapFd(size: int, fd: uint, off: uint): *u8 {
    unsafe "a system call that passes no memory, only values and handles" {
        r := Syscall(Nrs().mmap, 0, Trunc[uint](size), ProtRW, MapShared | MapPopulate, fd, off)
        return if IsErr(r) { raw.Null[u8]() } else { raw.FromAddr[u8](Trunc[uint](r)) }
    }
}

// UringInit sets up the ring, or returns false with nothing left open, so
// selection falls back to epoll: ENOSYS, EPERM (io_uring_disabled,
// seccomp), EACCES, or a kernel without single mmap, no-drop, stable
// submission, the current-position read and the extended wait argument
// (all of them are in 5.11 and later).
UringInit(): bool {
    pp := PP()
    var params: [15]u64 = [0; 15]
    unsafe "system call; params is a struct io_uring_params" {
        pr := raw.PtrCast[u64, u8](raw.DataMut(mut params[..]))
        Put32(pr, 4, 4096)
        Put32(pr, 8, SetupCqSize | SetupClamp)
        fd := Syscall(Nrs().io_uring_setup, 256, raw.Addr(pr), 0, 0, 0, 0)
        if IsErr(fd) {
            return false
        }
        f := Trunc[uint](fd)
        need := FeatSingleMmap | FeatNoDrop | FeatSubmitStable | FeatRwCurPos | FeatExtArg
        if (Get32(pr, 20) & need) != need {
            _ = Syscall(Nrs().close, f, 0, 0, 0, 0, 0)
            return false
        }
        sq_entries := Get32(pr, 0)
        cq_entries := Get32(pr, 4)
        var size := int(Get32(pr, 64)) + int(sq_entries) * 4
        cq_size := int(Get32(pr, 100)) + int(cq_entries) * 16
        if cq_size > size {
            size = cq_size
        }
        ring := MapFd(size, f, OffSqRing)
        if raw.IsNull(ring) {
            _ = Syscall(Nrs().close, f, 0, 0, 0, 0, 0)
            return false
        }
        sqes := MapFd(int(sq_entries) * 64, f, OffSqes)
        if raw.IsNull(sqes) {
            Unmap(ring, size)
            _ = Syscall(Nrs().close, f, 0, 0, 0, 0, 0)
            return false
        }
        // A blocking eventfd: io_uring honours O_NONBLOCK and would fail
        // the pending read with EAGAIN instead of waiting.
        ev := Syscall(Nrs().eventfd2, 0, EpollCloExec, 0, 0, 0, 0)
        if IsErr(ev) {
            Unmap(sqes, int(sq_entries) * 64)
            Unmap(ring, size)
            _ = Syscall(Nrs().close, f, 0, 0, 0, 0, 0)
            return false
        }
        (*pp).ring_fd = f
        (*pp).sq_entries = sq_entries
        (*pp).sq_mask = Get32(raw.Offset(ring, int(Get32(pr, 48))), 0)
        (*pp).cq_mask = Get32(raw.Offset(ring, int(Get32(pr, 88))), 0)
        (*pp).sq_head_p = raw.Offset(ring, int(Get32(pr, 40)))
        (*pp).sq_tail_p = raw.Offset(ring, int(Get32(pr, 44)))
        (*pp).sq_array = raw.Offset(ring, int(Get32(pr, 64)))
        (*pp).cq_head_p = raw.Offset(ring, int(Get32(pr, 80)))
        (*pp).cq_tail_p = raw.Offset(ring, int(Get32(pr, 84)))
        (*pp).cqes = raw.Offset(ring, int(Get32(pr, 100)))
        (*pp).sqes = sqes
        (*pp).sq_tail = Get32((*pp).sq_tail_p, 0)
        // Published before PollInit returns (see PollInit's edge).
        Poll.evfd.Store(Trunc[u64](ev))
    }
    ArmWake()
    return true
}

// RingLoad and RingStore read and write a kernel-shared ring word
// atomically (sequentially consistent: at least the acquire and release the
// io_uring protocol asks for on the head and tail words).
RingLoad(p: *u8): u32 {
    unsafe "p is a word of the mapped ring" {
        return (*raw.PtrCast[u8, raw.Atomic[u32]](p)).Load()
    }
}

RingStore(p: *u8, v: u32) {
    unsafe "p is a word of the mapped ring" {
        (*raw.PtrCast[u8, raw.Atomic[u32]](p)).Store(v)
    }
}

// Enter is io_uring_enter, with EINTR retried.
Enter(to_submit: u32, min_complete: u32, flags: uint, arg: uint, argsz: uint): int {
    unsafe "system call; arg points to argsz bytes the caller keeps alive for the call" {
        for {
            r := Syscall(Nrs().io_uring_enter, (*PP()).ring_fd, uint(to_submit), uint(min_complete), flags, arg,
                argsz)
            if r != -4 {
                return r
            }
        }
    }
}

// Unsubmitted is how many published SQEs the kernel has not consumed.
Unsubmitted(): u32 {
    unsafe "PollInit ran, so PP() and the ring words it mapped live until exit" {
        return RingLoad((*PP()).sq_tail_p) -% RingLoad((*PP()).sq_head_p)
    }
}

// Push copies sqe into the ring and submits it. When the ring is full (the
// kernel refused earlier SQEs with EBUSY while its completion backlog was
// full), it drains the CQ, submits what is queued and yields until a slot
// frees. An SQE the kernel does not consume now (EBUSY, EAGAIN) stays
// published and goes with the next io_uring_enter, which always submits
// what is queued. Never called with the CQ lock held (UringReap submits
// after releasing it), so the drain cannot deadlock.
Push(sqe: *u8) {
    pp := PP()
    unsafe "the state page and the mapped rings; the SQ under its lock" {
        SpinLock(raw.Of(&(*pp).sq_lock))
        for (*pp).sq_tail -% RingLoad((*pp).sq_head_p) >= (*pp).sq_entries {
            SpinUnlock(raw.Of(&(*pp).sq_lock))
            // Drain the CQ into the done list, so a kernel holding a
            // completion backlog can flush it and take the queued SQEs;
            // the completions reach their waiters through the next
            // PollWait (woken here).
            //
            // With an empty out every completion goes to the done list, so
            // the reap returns 0 and the PollWake below is what hands them
            // out; it also replaces the wake's CQE, which this blocking
            // reap may take from the token holder. The nesting
            // Push, UringReap, Resubmit, Push is bounded by the CQEs that
            // need a next step while the SQ stays full: each level reaps at
            // least one of them, or it resubmits nothing and goes no deeper
            // (about 1.5 KiB of stack a level). The reap's ArmWake, when it
            // takes the wake's CQE, adds one level at most: the CQE is
            // consumed at the first.
            var nothing: [1]*Op = [raw.Null[Op]()]
            _ = UringReap(mut nothing[..0], 0, true)
            _ = Enter(Unsubmitted(), 0, 0, 0, 0)
            PollWake()
            ThreadYield()
            SpinLock(raw.Of(&(*pp).sq_lock))
        }
        t := (*pp).sq_tail
        idx := t & (*pp).sq_mask
        raw.Copy(raw.Offset((*pp).sqes, int(idx) * 64), sqe, 64)
        Put32((*pp).sq_array, int(idx) * 4, idx)
        (*pp).sq_tail = t +% 1
        // The SQE's bytes and its array slot are written before the tail
        // that publishes them (sequentially consistent store; the kernel
        // reads the tail with acquire semantics).
        RingStore((*pp).sq_tail_p, t +% 1)
        SpinUnlock(raw.Of(&(*pp).sq_lock))
    }
    // Test point 9: the SQE stays published and unconsumed, as after an
    // EBUSY or EAGAIN (PushAll).
    if TestOn(9) {
        return
    }
    // Every published SQE the kernel has not consumed, not 1 (PushAll): an
    // SQE left by an EBUSY or EAGAIN would otherwise wait behind
    // each later push, which submits one from the head, until a token
    // holder's UringWait, and while every carrier polls without blocking
    // nobody holds the token. 0 means another pusher's enter took this
    // SQE: no system call.
    n := Unsubmitted()
    if n > 0 {
        _ = Enter(n, 0, 0, 0, 0)
    }
}

// ArmWake queues the eventfd read that PollWake's write completes.
ArmWake() {
    var sqe: [8]u64 = [0; 8]
    pp := PP()
    unsafe "sqe is a local SQE; the read buffer is in the state page" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut sqe[..]))
        raw.Write(q, OpcRead)
        // Edge: the init's Store of evfd, made before this read is armed.
        Put32(q, 4, Trunc[u32](Poll.evfd.Load()))
        Put64(q, 8, 0xFFFF_FFFF_FFFF_FFFF)
        Put64(q, 16, u64(raw.Addr(raw.Of(&(*pp).wake_buf))))
        Put32(q, 24, 8)
        Put64(q, 32, WakeTag)
        Push(q)
    }
}

// Prep fills sqe for op and returns false (op.res set) when the op cannot
// be expressed (an address of no family, a path that does not fit its
// scratch buffer or holds a NUL).
Prep(op: *Op, q: *u8): bool {
    unsafe "op is the caller's operation; its os area and scratch live until the completion" {
        o := OsArea(op)
        k := (*op).kind
        fd := Trunc[u32]((*op).h)
        if k == OpRead || k == OpWrite {
            opc := if k == OpRead { OpcRead } else { OpcWrite }
            raw.Write(q, opc)
            Put32(q, 4, fd)
            Put64(q, 8, Trunc[u64]((*op).pos))
            Put64(q, 16, u64(raw.Addr((*op).buf)))
            Put32(q, 24, Trunc[u32](Clip((*op).len)))
        } else if k == OpRecv || k == OpSend {
            opc := if k == OpRecv { OpcRecv } else { OpcSend }
            raw.Write(q, opc)
            Put32(q, 4, fd)
            Put64(q, 16, u64(raw.Addr((*op).buf)))
            Put32(q, 24, Trunc[u32](Clip((*op).len)))
            if k == OpSend {
                Put32(q, 28, Trunc[u32](MsgNoSignal))
            }
        } else if k == OpAccept && (*op).pos == SharedPort && Get32(o, OsStage) == 0 {
            // A listener other processes accept on too (inherited, handed to
            // a child, or passed) first waits with a one-shot POLL_ADD for
            // POLLIN, then accepts (SharedStep). The kernel arms an ACCEPT's
            // wait as an exclusive waiter, and a killed process's armed
            // ACCEPT stays on the listener's queue until its ring's exit work
            // runs (asynchronously since Linux 6.10): a connection that
            // arrives meanwhile wakes only that dead entry, and the live
            // sibling hears of it only when the next connection comes. A
            // POLL_ADD waits non-exclusively, so every holder is woken.
            raw.Write(q, OpcPollAdd)
            Put32(q, 4, fd)
            Put32(q, 28, 1)
        } else if k == OpAccept {
            raw.Write(q, OpcAccept)
            // A shared listener's accept step 1 never waits (AcceptDontWait);
            // step 2, on a kernel that refuses the flag, is a plain ACCEPT
            // (SharedStep).
            if (*op).pos == SharedPort && Get32(o, OsStage) == 1 {
                Put16(q, 2, AcceptDontWait)
            }
            Put32(q, 4, fd)
            Put32(o, OsAddrN, 32)
            Put64(q, 8, u64(raw.Addr(raw.Offset(o, OsAddrN))))
            Put64(q, 16, u64(raw.Addr(raw.Offset(o, OsAddr))))
            Put32(q, 28, AcceptCloExec)
        } else if k == OpConnect {
            n := EncodeAddr(&(*op).addr, raw.Offset(o, OsAddr))
            if n == 0 {
                (*op).res = FailWith(core.SysErr.InvalidAddress)
                return false
            }
            raw.Write(q, OpcConnect)
            Put32(q, 4, fd)
            Put64(q, 8, Trunc[u64](n))
            Put64(q, 16, u64(raw.Addr(raw.Offset(o, OsAddr))))
        } else if k == OpSync {
            raw.Write(q, OpcFsync)
            Put32(q, 4, fd)
        } else if k == OpProcWait || k == OpReady {
            // A one-shot POLL_ADD for POLLIN on the pidfd (it completes
            // when the child ends; UringFinish reaps it), or on the
            // descriptor OpReady waits for.
            raw.Write(q, OpcPollAdd)
            Put32(q, 4, fd)
            Put32(q, 28, 1)
        } else if k == OpRecvFrom || k == OpSendTo {
            // RECVMSG and SENDMSG over a msghdr, an iovec and the sockaddr,
            // all in the os area, which lives until the CQE.
            var namelen := 32
            var opc := OpcRecvMsg
            var flags := MsgTrunc
            if k == OpSendTo {
                namelen = EncodeAddr(&(*op).addr, raw.Offset(o, OsAddr))
                if namelen == 0 {
                    (*op).res = FailWith(core.SysErr.InvalidAddress)
                    return false
                }
                opc = OpcSendMsg
                flags = MsgNoSignal
            }
            m := raw.Offset(o, OsMsg)
            v := raw.Offset(o, OsIov)
            Put64(v, 0, u64(raw.Addr((*op).buf)))
            Put64(v, 8, u64(Clip((*op).len)))
            Put64(m, 0, u64(raw.Addr(raw.Offset(o, OsAddr))))
            Put32(m, 8, Trunc[u32](namelen))
            Put64(m, 16, u64(raw.Addr(v)))
            Put64(m, 24, 1)
            raw.Write(q, opc)
            Put32(q, 4, fd)
            Put64(q, 16, u64(raw.Addr(m)))
            Put32(q, 24, 1)
            Put32(q, 28, Trunc[u32](flags))
        } else {
            return PrepPath(op, q)
        }
        return true
    }
}

// PrepPath fills the SQE of an open, rename or remove; the paths are copied
// NUL-terminated into the op's scratch buffer (buf, len), which lives in the
// task's frame until the completion.
PrepPath(op: *Op, q: *u8): bool {
    unsafe "op's scratch buffer holds len bytes" {
        k := (*op).kind
        scratch := raw.SliceMut((*op).buf, (*op).len)
        if raw.IsNull((*op).buf) || !CPath((*op).path, scratch) {
            (*op).res = FailWith(core.SysErr.InvalidPath)
            return false
        }
        p1 := raw.Addr((*op).buf)
        Put32(q, 4, Trunc[u32](AtFdcwd))
        Put64(q, 16, u64(p1))
        if k == OpOpen {
            raw.Write(q, OpcOpenAt)
            // The pool's flags for the same mode (file.mk), so both routes
            // open alike (mode 2 read-write, the options form).
            mode := Trunc[u32]((*op).pos)
            Put32(q, 24, Trunc[u32](OpenPerm(mode)))
            Put32(q, 28, Trunc[u32](OpenFlags(mode)))
        } else if k == OpRename {
            n1 := (*op).path.Len() + 1
            rest := raw.SliceMut(raw.Offset((*op).buf, n1), (*op).len - n1)
            if !CPath((*op).path2, rest) {
                (*op).res = FailWith(core.SysErr.InvalidPath)
                return false
            }
            raw.Write(q, OpcRenameAt)
            Put32(q, 24, Trunc[u32](AtFdcwd))
            Put64(q, 8, u64(raw.Addr(raw.Offset((*op).buf, n1))))
        } else {
            raw.Write(q, OpcUnlinkAt)
            if Get32(OsArea(op), OsStage) == 1 {
                Put32(q, 28, Trunc[u32](AtRemoveDir))
            }
        }
        return true
    }
}

// SubmitPrepared stamps the generation and user_data and pushes the SQE.
SubmitPrepared(op: *Op, q: *u8) {
    pp := PP()
    unsafe "PollInit ran, so PP() is the page it made, which lives until exit; op is the submitter's until this push, and q is the SQE slot it reserved" {
        // Edge: none; a counter: distinct values per submission are all it
        // needs, and the user_data it builds is published with the SQE by
        // the tail store.
        g := ((*pp).generation.Add(1) +% 1) & 0xFFFF
        o := OsArea(op)
        Put32(o, OsGen, g)
        ud := u64(raw.Addr(op)) | (u64(g) << 48)
        // Atomic because a PollCancel on another thread may read it now;
        // the SQE's own copy is published by Push's tail store.
        (*UserData(op)).Store(ud)
        Put64(q, 32, ud)
        // Test point 4 records the first user_data stamped while it is on
        // (a Cas from 0: one value, never overwritten).
        if TestOn(4) {
            // Edge: none; a test record that the test reads after its own
            // synchronisation.
            _ = Hooks.saved.Cas(0, ud)
        }
    }
    Push(q)
}

// TrySend is a send's first step on io_uring: one sendto with MSG_DONTWAIT
// on the calling carrier, as epoll's Try does first. True when that
// finished the op (res set: the bytes sent, or the error mapped as
// UringFinish maps it); false on EAGAIN (the send buffer is full), and the
// op goes to the ring, which waits for room. Measured by the Mk authors on
// one machine (WSL kernel 6.6, echo.mk at 10,000 connections, one 64-byte echo
// on every connection at once): with every send through the ring,
// 149 / 158 ms at 1 / 8 carriers (the ring did not scale past one
// carrier; epoll took 140 / 47 ms); with this step 119 / 65 ms. Receives
// stay on the ring: trying a receive first as well gave 168 / 61 ms,
// because a server's receive usually finds no data yet and the try is a
// wasted system call. Order is unchanged: a task's sends follow one
// another (each awaits its op), and two tasks' sends on one stream have no
// order on either backend.
TrySend(op: *Op): bool {
    unsafe "op is the caller's operation, not yet submitted" {
        if (*op).kind != OpSend {
            return false
        }
        fd := (*op).h
        for {
            r := Syscall(Nrs().sendto, fd, raw.Addr((*op).buf), Clip((*op).len), MsgNoSignal | MsgDontWait, 0, 0)
            if r == Eintr4 {
                continue
            }
            if r == Eagain {
                return false
            }
            (*op).res = if IsErr(r) { IoFail(fd, OpSend, i64(-r)) } else { Ok(i64(r)) }
            return true
        }
    }
}

// UringSubmit starts op: true when it completes through the ring, false
// (res set) when TrySend finished it or it cannot be expressed.
UringSubmit(op: *Op): bool {
    if TrySend(op) {
        return false
    }
    var sqe: [8]u64 = [0; 8]
    unsafe "sqe is a local SQE" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut sqe[..]))
        if !Prep(op, q) {
            return false
        }
        SubmitPrepared(op, q)
    }
    return true
}

// UringCancel submits ASYNC_CANCEL for op's exact user_data.
UringCancel(op: *Op) {
    var sqe: [8]u64 = [0; 8]
    unsafe "sqe is a local SQE" {
        q := raw.PtrCast[u64, u8](raw.DataMut(mut sqe[..]))
        raw.Write(q, OpcAsyncCancel)
        Put32(q, 4, 0xFFFF_FFFF)
        var ud := (*UserData(op)).Load()
        // Test point 4: a cancel that names an older submission's
        // user_data, as one processed late would (the generation test).
        // Edge: none; test point 4's record, as above.
        if TestOn(4) && Hooks.saved.Load() != 0 {
            ud = Hooks.saved.Load()
        }
        Put64(q, 16, ud)
        Push(q)
    }
}

// UserData is op's current user_data word. Its Store in SubmitPrepared and
// its Load in UringCancel are sequentially consistent, so a cancel reads
// either the old or the new submission's value, never a torn one; which
// one it reads is settled by Resubmit's check of op.cancel.
UserData(op: *Op): *raw.Atomic[u64] {
    unsafe "the user_data word of op's os area, 8-byte aligned (OsArea)" {
        return raw.PtrCast[u8, raw.Atomic[u64]](raw.Offset(OsArea(op), OsUserData))
    }
}

// ResubWord is op's handoff word: 0 none, 1 a resubmitter is between its
// push and its cancel check, 2 a reaper met the CQE in that window and left
// the result at OsResubRes.
ResubWord(op: *Op): *raw.Atomic[u32] {
    unsafe "the handoff word of op's os area, 4-byte aligned (OsArea)" {
        return raw.PtrCast[u8, raw.Atomic[u32]](raw.Offset(OsArea(op), OsResub))
    }
}

// ResubClaim is a reaper's check for every CQE of an op: true when a
// resubmitter still holds op in its window and will finish it (the result
// is left in the op), false when the reaper finishes it as usual.
ResubClaim(op: *Op, res: i32): bool {
    w := ResubWord(op)
    unsafe "op's CQE was just reaped, so op is live: its task waits for it" {
        // Edge: 0, no resubmitter holds op. The word is 1 from before the
        // submission that produced this completion until its submitter
        // releases it, and this Load is after the completion, so 0 here
        // means the release came first (SC order) and op is this reaper's.
        if (*w).Load() == 0 {
            return false
        }
        Put32(OsArea(op), OsResubRes, Trunc[u32](i64(res)))
        // The result is written before the Cas, and the resubmitter reads
        // it after its own Cas(1, 0) fails on this 2: the handoff's
        // release/acquire edge (both sequentially consistent).
        return (*w).Cas(1, 2)
    }
}

// Resubmit submits op's next step after its CQE (the directory step of a
// remove, an EINTR or ECONNABORTED with no cancel, an OpReady's EINVAL on
// a terminal with no cancel) and returns the new k:
// op goes back to the kernel, or is delivered into out (or the done list)
// when it finished. Never called with the CQ lock held.
Resubmit(op: *Op, out: mut []*Op, k: int): int {
    w := ResubWord(op)
    unsafe "op is this thread's: its last CQE was reaped and not handed out" {
        for {
            // Taken before the push: from here no reaper hands op out.
            // Edge: the Store(1) is ordered before the push, so a reaper that
            // meets the new CQE sees 1 and leaves op to this thread.
            (*w).Store(1)
            if !UringSubmit(op) {
                (*w).Store(0)
                return Deliver(op, out, k)
            }
            // Test point 2: the window in which a reaper on another thread
            // can meet the new CQE and a PollCancel can name the new or the
            // old user_data (ResubRace).
            _ = TestPoint(2)
            // Edge: a cancel stored before this load may have named the old
            // user_data; cancel the new submission (sequentially
            // consistent: a cancel stored after this load is followed by
            // its PollCancel, whose Load of user_data is after the Store in
            // SubmitPrepared, so it names this submission).
            if (*op).cancel.Load() == 1 {
                UringCancel(op)
            }
            // Edge: the Cas(1, 0) hands op to the reapers; it fails on a
            // reaper's Cas(1, 2), after which the result that reaper wrote
            // before its Cas is read here.
            if (*w).Cas(1, 0) {
                return k
            }
            // A reaper met the new CQE while this thread held the word.
            res := Trunc[i32](i64(Get32(OsArea(op), OsResubRes)))
            // Edge: none; op stays this thread's (the reaper left it), and 0
            // marks no step in flight.
            (*w).Store(0)
            if UringFinish(op, res) {
                return Deliver(op, out, k)
            }
        }
    }
}

// SharedStep moves a shared listener's accept (op.pos SharedPort) on after
// the CQE res; OsStage says which step res answers. 0: the POLL_ADD fired,
// so the next step is an ACCEPT, with AcceptDontWait (step 1) unless this
// kernel refuses it (step 2). 1 or 2: the ACCEPT's answer. Neither step
// waits in the kernel once the POLL_ADD fired, except step 2 after a lost
// race, which then waits as any ACCEPT (on kernels before 6.10, where a
// dead holder's wait is removed with its ring), and nothing here makes a
// system call. The answer is 0 to go on with UringFinish's own handling of
// res, 1 when op.res is set (the op is done), 2 to submit op again (its
// next step, or the POLL_ADD again after a sibling took the connection).
// EAGAIN with POLLHUP or POLLRDHUP in the POLL_ADD's mask is EINVAL, as a
// blocking accept answers (a Unix listener a holder shut down polls
// readable for good).
SharedStep(op: *Op, res: i32): u32 {
    unsafe "op's CQE was just reaped: the op is this thread's until it is handed out" {
        o := OsArea(op)
        st := Get32(o, OsStage)
        if st == 0 {
            if res < 0 {
                return 0
            }
            Put32(o, OsPollMask, Trunc[u32](i64(res)))
            // Edge: none; a cache of the kernel's answer (DontWaitState).
            Put32(o, OsStage, if DontWait.v.Load() == 2 { 2 } else { 1 })
            return 2
        }
        e := -i64(res)
        if res >= 0 || e == 11 {
            // The kernel took step 1's flag; step 2 after an EINVAL of step 1
            // (DontWait still 0) shows the flag was what it refused.
            // Edge: none; these operations share only the cached kernel answer.
            if st == 1 {
                DontWait.v.Store(1)
            } else if DontWait.v.Load() == 0 {
                DontWait.v.Store(2)
            }
        }
        if res >= 0 {
            return 0
        }
        if e == 11 {
            if (Get32(o, OsPollMask) & (PollHupMask | PollRdHupMask)) != 0 {
                (*op).res = IoFail((*op).h, OpAccept, 22)
                return 1
            }
            // Edge: as the resubmit's: a cancel stored after this Load is
            // followed by its PollCancel.
            if (*op).cancel.Load() == 1 {
                (*op).res = FailWith(core.SysErr.Canceled)
                return 1
            }
            Put32(o, OsStage, 0)
            return 2
        }
        // Edge: none; the cached answer only selects the next kernel operation.
        if e == 22 && st == 1 && DontWait.v.Load() == 0 {
            // A kernel before 6.10 refuses the flag with EINVAL; so does a
            // listener that is not listening. Step 2 tells them apart.
            Put32(o, OsStage, 2)
            return 2
        }
        return 0
    }
}
// UringFinish turns op's CQE result into op.res. false: the op goes on to
// its next step (the directory step of a remove; an EINTR or ECONNABORTED
// without a cancel; an OpReady's EINVAL on a terminal without a cancel; a
// shared listener's next step, SharedStep) and the caller submits it again once it has released the CQ lock (a submission
// may have to wait for the kernel to flush its completion backlog, which
// needs the CQ head this reap is about to return).
UringFinish(op: *Op, res: i32): bool {
    unsafe "op's CQE was just reaped: the op is this thread's until it is handed out" {
        k := (*op).kind
        if k == OpAccept && (*op).pos == SharedPort {
            step := SharedStep(op, res)
            if step != 0 {
                return step == 1
            }
        }
        if res >= 0 {
            if k == OpAccept {
                if !DecodeAddr(raw.Offset(OsArea(op), OsAddr), mut (*op).addr) {
                    (*op).addr = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
                }
            }
            if k == OpRecvFrom {
                DgramDone(op, int(res), (Get32(raw.Offset(OsArea(op), OsMsg), 48) & Trunc[u32](MsgTrunc)) != 0)
                return true
            }
            if k == OpReady {
                (*op).res = Ok(0)
                return true
            }
            if k == OpProcWait {
                // The pidfd is readable only once the child has ended, so
                // the reap finds it (Other if it ever did not).
                if !ProcTry(op) {
                    (*op).res = FailWith(core.SysErr.Other)
                }
                return true
            }
            (*op).res = Ok(i64(res))
            return true
        }
        e := -i64(res)
        if k == OpRemove && e == 21 && Get32(OsArea(op), OsStage) == 0 {
            Put32(OsArea(op), OsStage, 1)
            return false
        }
        if e == 4 || (k == OpAccept && e == 103) {
            // Edge: as the resubmit's: a cancel stored after this Load is
            // followed by its PollCancel.
            if (*op).cancel.Load() == 1 {
                (*op).res = FailWith(core.SysErr.Canceled)
                return true
            }
            return false
        }
        if k == OpReady && e == 22 && IsTerminal((*op).h) {
            // A terminal whose mode was set during the wait.
            // TCSETS wakes the tty's two wait queues with no event mask
            // (n_tty_set_termios); the one-shot POLL_ADD then finds no input
            // and the kernel arms it again, which fails with EINVAL on its
            // second queue: the entry it kept from the first arm is unlinked
            // but still there (read in Linux v6.6 io_uring/poll.c:
            // io_poll_check_events' IOU_POLL_REISSUE, __io_queue_proc's "fail
            // a third attempt"). So it is a wake with nothing to read, and
            // the op is submitted again as a fresh POLL_ADD, which arms on a
            // terminal as this op's first did: the wait goes on, and input
            // that came meanwhile completes it at once. The kernel bug is
            // fixed upstream in f2dbe87968a3 ("io_uring/poll: drop stale
            // double poll entry on rearm"; first in mainline 7.4-rc1; 7.3.y
            // from 7.3.2 (2c92f10b4a7c); 6.6.y from 6.6.161 (07aa50be));
            // affected are kernels with 6e5aedb9324a (6.2 on, inferred, and
            // 6.1.y from c06015ebc436 until 71c6346aab54) without it. On a
            // terminal whose line discipline polls (n_tty, every terminal a
            // shell gives), each such completion needs a wake of the tty's
            // queues, so this does not spin; on one whose discipline has no
            // poll but answers TCGETS (N_PPP) every arm fails so, and this
            // resubmits without end until a cancel (not reached by any std
            // path; the epoll backend waits for ever there instead). A handle
            // that is not a terminal keeps its EINVAL. epoll never answers
            // this (ReadyTry's ppoll reports readiness, not the wake). One
            // TCGETS, only on this rare path, under the CQ lock; it can block
            // while a line-discipline change or a hangup holds the tty's
            // ldisc_sem (tty_ldisc_ref_wait), and the carriers in a blocking
            // reap spin meanwhile. Edge: as the EINTR case above: a cancel
            // stored after this Load is followed by its PollCancel.
            if (*op).cancel.Load() == 1 {
                (*op).res = FailWith(core.SysErr.Canceled)
                return true
            }
            return false
        }
        // Else by kind and handle, as epoll's Try maps it.
        (*op).res = if k == OpConnect { ConnectFail(e) } else { IoFail((*op).h, k, e) }
        return true
    }
}

// UringReap drains the CQ: completions go to out[k..] and, past out's
// end, to the done list, so the CQ never stays full behind a small out (a
// full CQ makes the kernel hold a backlog and refuse submissions). A
// thread that finds the CQ lock taken leaves the reaping to its holder,
// unless block is set (the token holder after its wait, or a submitter
// whose SQ is full), which waits for the lock. Ops that go on to a next
// step are submitted after the lock is released, at most 64 per call (the
// rest of the CQ stays for the next reap).
UringReap(out: mut []*Op, k: int, block: bool): int {
    pp := PP()
    var n := TakeDone(out, k)
    var again: [64]*Op = [raw.Null[Op](); 64]
    var na := 0
    var rearm := false
    var stray := false
    unsafe "the state page and the mapped rings; the CQ under its lock" {
        lock := raw.Of(&(*pp).cq_lock)
        if block {
            SpinLock(lock)
        } else if !SpinTry(lock) {
            return n
        }
        var head := RingLoad((*pp).cq_head_p)
        tail := RingLoad((*pp).cq_tail_p)
        for head != tail && na < 64 {
            c := raw.Offset((*pp).cqes, int(head & (*pp).cq_mask) * 16)
            ud := Get64(c, 0)
            // The wake's CQE is the token holder's: a
            // non-blocking reap stops before it while a thread holds the
            // token, so a busy thread cannot swallow a PollWake meant for
            // the thread in the kernel (kernels 5.11 to 5.18 wake a waiter
            // only while tail - head reaches min_complete, so a reaped wake
            // would leave it asleep). The CQEs behind it wait one moment
            // for the holder, whose wait the left CQE ends at once. With
            // the token free nobody waits in the kernel: the reap takes the
            // CQE (the CQ must not stall behind it) and keeps the wake for
            // the next holder in wake_pending (below).
            if ud == WakeTag && !block {
                // Edge: the holder's token Cas(0, 1); with no holder the wake
                // waits in wake_pending (PollWake's Dekker pair, below).
                if (*pp).token.Load() == 1 {
                    break
                }
                stray = true
            }
            var res := Trunc[i32](i64(Get32(c, 8)))
            head = head +% 1
            if ud == 0 {
                continue
            }
            if ud == WakeTag {
                rearm = true
                continue
            }
            op := raw.FromAddr[Op](Trunc[uint](ud & AddrMask))
            // Test point 5: one op CQE reports EINTR (ResubCancel). A Cas
            // from 0: exactly one CQE is changed.
            // Edge: none; a test record, one Cas from 0.
            if TestOn(5) && Hooks.faked.Cas(0, 1) {
                res = -4
            }
            if ResubClaim(op, res) {
                continue
            }
            if UringFinish(op, res) {
                n = Deliver(op, out, n)
            } else {
                again[na] = op
                na += 1
            }
        }
        // Every CQE before head is consumed (its fields copied out) before
        // this store returns the slots to the kernel, which reads the head
        // with acquire semantics.
        RingStore((*pp).cq_head_p, head)
        SpinUnlock(lock)
    }
    for i in 0..na {
        n = Resubmit(again[i], out, n)
    }
    if rearm {
        ArmWake()
    }
    if stray {
        // A wake taken with the token free makes the next kernel wait
        // return at once, as epoll's undrained eventfd does. The Dekker
        // pair with UringWait: this Store(1), then the token Load; the
        // holder's token Cas, then its wake_pending Swap (all sequentially
        // consistent). A holder whose Swap missed the 1 took the token
        // before this Load, which then sees it and wakes it again.
        unsafe "PollInit ran, so PP() is the page it made, which lives until exit; its words are atomics" {
            // Edge: the Store(1) is before the token Load, and a holder's
            // Swap(0) after its token Cas, so either this Load sees the holder
            // or the holder's Swap sees the 1 (Dekker pair).
            (*pp).wake_pending.Store(1)
            if (*pp).token.Load() == 1 {
                PollWake()
            }
        }
    }
    return n
}

// UringWait is the token holder's wait: submit what is queued and wait for
// one completion up to timeout_ns (negative: forever), then reap.
UringWait(timeout_ns: i64, out: mut []*Op): int {
    var arg: [3]u64 = [0; 3]
    var ts: [2]u64 = [0; 2]
    unsafe "system call; arg is a struct io_uring_getevents_arg" {
        // A wake a non-blocking reap took while nobody held the token
        // (UringReap's stray wake): this wait returns at once.
        // Edge: the Swap takes the pending wake that PollWake stored before its
        // token Load.
        t := if (*PP()).wake_pending.Swap(0) == 1 { 0 } else { timeout_ns }
        if t >= 0 {
            Timespec(raw.PtrCast[u64, u8](raw.DataMut(mut ts[..])), t)
            arg[2] = u64(raw.Addr(raw.Data(ts[..])))
            _ = Enter(Unsubmitted(), 1, EnterGetEvents | EnterExtArg, raw.Addr(raw.Data(arg[..])), 24)
        } else {
            _ = Enter(Unsubmitted(), 1, EnterGetEvents, 0, 0)
        }
    }
    return UringReap(out, 0, true)
}
