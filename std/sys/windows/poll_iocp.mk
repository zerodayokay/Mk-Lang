// Why: the scheduler parks tasks on I/O, and on Windows the one scalable
// way to wait for files and sockets together is an I/O completion port.
//
// What: the Windows poller: PollInit, PollSupports, PollAttach,
// PollDetach, PollSubmit, PollCancel, PollRelease, PollWait and PollWake,
// with the AcceptEx reissue (ReissueAccept) and the shared-port listener.
//
// How: one I/O completion port for the process. Files opened for overlapped I/O
// and sockets are attached with skip-on-success (sockets only when every
// provider allows it, ws2_32.mk), so a synchronous success posts nothing
// and PollSubmit returns false for it. Every carrier may wait in PollWait.
//
// Op.os layout (offsets in the area): the OVERLAPPED at 0 (32 bytes), the
// op's own address at 32 (a completion's OVERLAPPED* finds its op through
// it, with no field-offset constant), the accept socket at 40, the WSARecv
// flags at 48, the synchronous byte count at 52, the resubmission handoff
// word at 56 and the byte count it hands over at 60, and from 64 the
// AcceptEx address buffer (2 x 44 bytes), the ConnectEx target, or a
// datagram's sockaddr (32 bytes) with its length at 96. Op is C
// layout: os starts at byte 136, so the OVERLAPPED
// and these words are naturally aligned.
//
// A reissued AcceptEx (a peer reset before the accept) must not lose a
// cancel: a CancelIoEx that ran between the failed packet and the reissue
// found nothing. So the reissuer checks op.cancel after AcceptEx and cancels
// it itself, holding the handoff word at 1 so that no other thread hands op
// out meanwhile (the packet can come on any thread); a PollWait that
// dequeues the packet in that window moves the word to 2 and leaves op to
// the reissuer, which finishes it (ReissueAccept).
//
// Ownership: a PollSubmit that returns true hands op, its OVERLAPPED and
// buf to the OS until the completion packet; PollWait is the only place that
// takes the op back, once, for that packet. An op that fails at submission
// posts no packet (skip-on-success or not), and its result is set here.

import std.core
import std.raw

struct PollState {
    port: raw.Atomic[u64]
}

var Poll: PollState = PollState(port: raw.Atomic[u64].New(0))

// WakeKey marks PollWake's completion packet, which has no OVERLAPPED.
const WakeKey: uint = 0x4D4B
// SkipModes is FILE_SKIP_COMPLETION_PORT_ON_SUCCESS |
// FILE_SKIP_SET_EVENT_ON_HANDLE.
const SkipModes: u8 = 3

// Offsets in Op.os.
const OsBack: int = 32
const OsSock: int = 40
const OsFlags: int = 48
const OsCount: int = 52
const OsResub: int = 56
const OsResubBytes: int = 60
const OsAddrs: int = 64
// AcceptEx wants each address slot 16 bytes larger than the largest sockaddr.
const AcceptAddrBytes: u32 = 44
// AcceptUnixBytes is an AF_UNIX accept's slot: sockaddr_un (110) + 16, and
// AcceptEx fails a smaller one with WSAEFAULT (measured on this host: 110
// fails, 126 succeeds). AcceptUnixArea is the area NativeAccept lends for
// it: two slots (unix.mk).
const AcceptUnixBytes: u32 = 128

// AcceptSlot is op's AcceptEx address slot size by the listener's family.
AcceptSlot(op: *Op): u32 {
    unsafe "op is the caller's pending operation" {
        // The Unix slot only with the lent area: without one AcceptEx fails with WSAEFAULT, never writes past op.os.
        return if (*op).addr.family == Unix && (*op).len >= AcceptUnixArea { AcceptUnixBytes } else { AcceptAddrBytes }
    }
}

// AcceptArea is op's AcceptEx address area: op.os's for TCP, and for a
// Unix listener the area of AcceptUnixArea bytes the caller lent in op.buf
// (it lives until the op completes: Await returns only after that).
AcceptArea(op: *Op, o: *u8): *u8 {
    unsafe "op is the caller's pending operation" {
        return if (*op).addr.family == Unix && (*op).len >= AcceptUnixArea { (*op).buf } else { raw.Offset(o, OsAddrs) }
    }
}

const ErrorHandleEof: u32 = 38
const ErrorBrokenPipe: u32 = 109
const ErrorNetnameDeleted: u32 = 64
const ErrorOperationAborted: u32 = 995
const WsaeConnReset: u32 = 10054

Port(): uint {
    // Edge: PollInit's Store of the port, before any other thread runs.
    return Trunc[uint](Poll.port.Load())
}

// OsArea is the first byte of op.os.
OsArea(op: *Op): *u8 {
    unsafe "op is a live operation" {
        return raw.Data((*op).os[..])
    }
}

/// PollInit starts the poller and returns its backend: 1 iocp, 0 on failure
/// (fatal at start). force_epoll is Linux's.
/// Once, at start: O(1) system calls; creates the completion port and maps
/// the timer's page, kept until exit.
export PollInit(force_epoll: bool): u32 {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        p := CreateIoCompletionPort(InvalidHandle, 0, 0, 0)
        if p == 0 {
            return 0
        }
        // Published once, before any other poll call (Start step 4, on the
        // only thread): the store and every later Load are sequentially
        // consistent, the publication edge for the port handle.
        Poll.port.Store(u64(p))
    }
    HrInit()
    return 1
}

/// PollSupports reports whether the port runs ops of kind: files on
/// overlapped handles (their reads, writes and lock waits) and every socket
/// operation; opens, renames, removes and syncs go to the blocking pool.
/// O(1); allocates nothing.
export PollSupports(kind: u32): bool {
    return kind == OpRead || kind == OpWrite || kind == OpAccept || kind == OpConnect || kind == OpRecv ||
        kind == OpSend || kind == OpSendTo || kind == OpRecvFrom || kind == OpProcWait || kind == OpResolve || kind == OpReady ||
        kind == OpLock
}

/// PollAttach associates an overlapped file handle with the port and sets
/// skip-on-success. false: the handle cannot use the port; the caller closes
/// it and reopens it for the pool (a handle once associated stays so).
/// O(1) system calls; allocates nothing.
export PollAttach(h: uint): bool {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        if CreateIoCompletionPort(h, Port(), 0, 0) == 0 {
            return false
        }
        return SetFileCompletionNotificationModes(h, SkipModes) != 0
    }
}

/// PollDetach does nothing on Windows: a handle leaves the port when it is
/// closed.
/// O(1): no call; allocates nothing.
export PollDetach(h: uint) {
}

/// PollSubmit starts op. true: pending, its completion comes from PollWait;
/// false: finished now, op.res set, no packet will follow.
/// O(1) system calls (an accept also makes its socket, a connect binds
/// first, a child wait reads the exit code and registers its wait, a lookup
/// maps a page for its name); allocates nothing beyond a lookup's page,
/// given back when it ends (op is the caller's until its completion).
export PollSubmit(op: *Op): bool {
    o := OsArea(op)
    unsafe "op is the caller's operation; from here the one-owner rule holds" {
        raw.Fill(o, 0, 160)
        Put64(o, OsBack, u64(raw.Addr(op)))
        k := (*op).kind
        if k == OpRead || k == OpWrite {
            return SubmitFile(op, o)
        }
        if k == OpRecv || k == OpSend {
            return SubmitStream(op, o)
        }
        if k == OpAccept {
            return SubmitAccept(op, o)
        }
        if k == OpConnect {
            return SubmitConnect(op, o)
        }
        if k == OpRecvFrom || k == OpSendTo {
            return SubmitDgram(op, o)
        }
        if k == OpProcWait || k == OpReady {
            return SubmitProcWait(op, o)
        }
        if k == OpResolve {
            return SubmitResolve(op, o)
        }
        if k == OpLock {
            return SubmitLock(op, o)
        }
        (*op).res = FailWith(core.SysErr.Other)
        return false
    }
}

// SubmitLock is OpLock: LockFileEx over every byte (LockLen(0)), shared
// or exclusive (op.len 1), at offset 0 of the zeroed OVERLAPPED, without
// fail-immediately, so a held lock leaves it pending on the port until
// the holder releases it or a cancel ends it. A lock taken at once is
// skipped on the port (skip-on-success), as a file read's is.
SubmitLock(op: *Op, o: *u8): bool {
    l := LockLen(0)
    unsafe "op is the caller's pending operation; o its OVERLAPPED" {
        flags: u32 = if (*op).len == 1 { LockExclusive } else { 0 }
        if LockFileEx((*op).h, flags, 0, l.low, l.high, o) != 0 {
            (*op).res = Ok(1)
            return false
        }
        e := GetLastError()
        if e == WsaIoPending {
            return true
        }
        (*op).res = Fail(e)
        return false
    }
}

// SubmitFile is ReadFile or WriteFile at op.pos (a pipe or other stream
// handle ignores the offset). End of input is a result of 0. An overlapped
// disk file has no stream position, so pos < 0 there is refused with
// ERROR_INVALID_PARAMETER (Other, os 87) instead of reading at offset 0
// (an offset of 0 would make repeated stream reads return the first bytes
// forever); the caller keeps the position (std.task's HandleRec.pos).
SubmitFile(op: *Op, o: *u8): bool {
    unsafe "op is the caller's pending operation; buf holds len bytes" {
        var pos := (*op).pos
        if pos < 0 {
            if GetFileType((*op).h) == 1 {
                (*op).res = Fail(87)
                return false
            }
            pos = 0
        }
        Put64(o, 16, Trunc[u64](pos))
        var n := (*op).len
        if n > MaxIo {
            n = MaxIo
        }
        if n < 0 {
            n = 0
        }
        var ok: i32 = 0
        if (*op).kind == OpRead {
            ok = ReadFile((*op).h, (*op).buf, Trunc[u32](n), raw.Null[u32](), o)
        } else {
            ok = WriteFile((*op).h, (*op).buf, Trunc[u32](n), raw.Null[u32](), o)
        }
        if ok != 0 {
            (*op).res = Ok(i64(Trunc[u32](Get64(o, 8))))
            return false
        }
        e := GetLastError()
        if e == WsaIoPending {
            return true
        }
        if (*op).kind == OpRead && (e == ErrorHandleEof || e == ErrorBrokenPipe) {
            (*op).res = Ok(0)
            return false
        }
        if (*op).kind == OpRead {
            (*op).res = ReadFail((*op).h, e)
        } else {
            (*op).res = Fail(e)
        }
        return false
    }
}

// SubmitStream is WSARecv or WSASend of one buffer. The WSABUF may live on
// this stack: Winsock captures it before the call returns.
SubmitStream(op: *Op, o: *u8): bool {
    unsafe "op is the caller's pending operation; buf holds len bytes" {
        if !WinsockReady() {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
        var n := (*op).len
        if n > MaxIo {
            n = MaxIo
        }
        if n < 0 {
            n = 0
        }
        var wb: [2]u64 = [Trunc[u64](n), u64(raw.Addr((*op).buf))]
        bufs := raw.PtrCast[u64, u8](raw.Data(wb[..]))
        count := raw.PtrCast[u8, u32](raw.Offset(o, OsCount))
        var r: i32 = 0
        if (*op).kind == OpRecv {
            f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, u32, *u32, *u32, *u8, *u8): i32](WsFn(WsRecv)))
            r = f((*op).h, bufs, 1, count, raw.PtrCast[u8, u32](raw.Offset(o, OsFlags)), o, raw.Null[u8]())
        } else {
            f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, u32, *u32, u32, *u8, *u8): i32](WsFn(WsSend)))
            r = f((*op).h, bufs, 1, count, 0, o, raw.Null[u8]())
        }
        if r == 0 {
            if SkipSockets() {
                (*op).res = Ok(i64(Get32(o, OsCount)))
                return false
            }
            return true
        }
        e := WsaError()
        if e == WsaIoPending {
            return true
        }
        (*op).res = Fail(e)
        return false
    }
}

// The datagram kinds keep the sockaddr at OsAddrs (32 bytes) and, for a
// receive, its length at OsDgramLen; both live until the packet (WSARecvFrom
// writes them at completion).
const OsDgramLen: int = 96
const WsaeMsgSize: u32 = 10040

// SubmitDgram is WSARecvFrom or WSASendTo. A datagram longer than
// the buffer completes with WSAEMSGSIZE and the buffer's length, which
// DgramDone reports as a cut datagram (os 1).
SubmitDgram(op: *Op, o: *u8): bool {
    unsafe "op is the caller's pending operation; buf holds len bytes" {
        if !WinsockReady() {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
        var n := (*op).len
        if n > MaxIo {
            n = MaxIo
        }
        if n < 0 {
            n = 0
        }
        var wb: [2]u64 = [Trunc[u64](n), u64(raw.Addr((*op).buf))]
        bufs := raw.PtrCast[u64, u8](raw.Data(wb[..]))
        count := raw.PtrCast[u8, u32](raw.Offset(o, OsCount))
        sa := raw.Offset(o, OsAddrs)
        var r: i32 = 0
        if (*op).kind == OpRecvFrom {
            Put32(o, OsDgramLen, 32)
            f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, u32, *u32, *u32, *u8, *i32, *u8, *u8): i32](WsFn(WsRecvFrom)))
            r = f((*op).h, bufs, 1, count, raw.PtrCast[u8, u32](raw.Offset(o, OsFlags)), sa,
                raw.PtrCast[u8, i32](raw.Offset(o, OsDgramLen)), o, raw.Null[u8]())
        } else {
            alen := EncodeAddr(&(*op).addr, sa)
            if alen == 0 {
                (*op).res = FailWith(core.SysErr.InvalidAddress)
                return false
            }
            f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, u32, *u32, u32, *u8, i32, *u8, *u8): i32](WsFn(WsSendTo)))
            r = f((*op).h, bufs, 1, count, 0, sa, Trunc[i32](alen), o, raw.Null[u8]())
        }
        if r == 0 {
            if SkipSockets() {
                DgramDone(op, o, Get32(o, OsCount), false)
                return false
            }
            return true
        }
        e := WsaError()
        if e == WsaIoPending {
            return true
        }
        // An immediate WSAEMSGSIZE is a cut datagram whatever the skip
        // mode: an immediate error means no packet follows (Go's execIO,
        // .NET's ProcessIOCPResult, libuv), so returning it as an error in
        // the no-skip branch would break the cut-datagram rule. On Windows
        // 11 a too-small receive never takes this branch:
        // WSARecvFrom answers WSA_IO_PENDING and the packet carries
        // STATUS_BUFFER_OVERFLOW, with skip-on-success on and off, the
        // datagram queued before or after the call (measured with a Go
        // probe); Finish turns that packet into the cut datagram.
        if e == WsaeMsgSize && (*op).kind == OpRecvFrom {
            DgramDone(op, o, Trunc[u32](n), true)
            return false
        }
        (*op).res = Fail(e)
        return false
    }
}

// DgramDone sets a finished datagram op's result: a receive decodes its
// source from OsAddrs into op.addr, and a cut datagram is its bytes with os 1.
DgramDone(op: *Op, o: *u8, bytes: u32, cut: bool) {
    unsafe "op is this thread's until it is handed out" {
        if (*op).kind == OpRecvFrom {
            if !DecodeAddr(raw.Offset(o, OsAddrs), mut (*op).addr) {
                (*op).addr = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
            }
            if cut {
                (*op).res = OsResult(value: i64(bytes), err: core.SysErr.None, os: 1)
                return
            }
        }
        (*op).res = Ok(i64(bytes))
    }
}

// SubmitAccept is AcceptEx on a new socket of the listener's family. A peer
// that reset before the accept is not the listener's failure: the socket is
// closed and the accept issued again (as Go's runtime does).
SubmitAccept(op: *Op, o: *u8): bool {
    unsafe "op is the caller's pending operation" {
        if !WinsockReady() {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
        if (*op).addr.family == 0 {
            var a := SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
            r := SocketName((*op).h, mut a)
            if r.err != core.SysErr.None {
                (*op).res = r
                return false
            }
            (*op).addr.family = a.family
        }
        af := OsFamily((*op).addr.family)
        if af == 0 {
            (*op).res = FailWith(core.SysErr.InvalidAddress)
            return false
        }
        accept := raw.Read(raw.PtrCast[u64, fn(uint, uint, *u8, u32, u32, u32, *u32, *u8): i32](WsFn(WsAcceptEx)))
        shared := (*op).pos == SharedPort
        for {
            s := NewSocket(af)
            if s == InvalidSocket {
                (*op).res = WsaFail()
                return false
            }
            Put64(o, OsSock, u64(s))
            if shared {
                ev := CreateEventW(raw.Null[u8](), 1, 0, raw.Null[u16]())
                if ev == 0 {
                    r := LastError()
                    _ = CloseSocketRaw(s)
                    (*op).res = r
                    return false
                }
                Put64(o, 24, u64(ev) | 1)
            }
            if accept((*op).h, s, AcceptArea(op, o), 0, AcceptSlot(op), AcceptSlot(op),
                raw.PtrCast[u8, u32](raw.Offset(o, OsCount)), o) != 0 {
                // A shared accept that finished at once queued nothing
                // (the event's low bit), whatever the skip mode.
                if shared {
                    ShareEventClose(o)
                    AcceptDone(op, o)
                    return false
                }
                if SkipSockets() {
                    AcceptDone(op, o)
                    return false
                }
                return true
            }
            e := WsaError()
            if e == WsaIoPending {
                return if shared { ShareArm(op, o) } else { true }
            }
            if shared {
                ShareEventClose(o)
            }
            _ = CloseSocketRaw(s)
            if e != WsaeConnReset && e != ErrorNetnameDeleted {
                (*op).res = Fail(e)
                return false
            }
            raw.Fill(o, 0, 32)
        }
    }
}

// AcceptDone finishes a successful AcceptEx: the accept context (so
// getsockname, shutdown and the rest work on the new socket), the port
// association, and the peer's address. On a failure the new socket is
// closed and op.res is the error.
AcceptDone(op: *Op, o: *u8) {
    unsafe "op's accept completed; the address buffer holds both addresses" {
        s := Trunc[uint](Get64(o, OsSock))
        var l: u64 = u64((*op).h)
        if SetSockOpt(s, SolSocket, SoUpdateAcceptContext, raw.PtrCast[u64, u8](raw.OfMut(mut l)), 8) != 0 {
            r := WsaFail()
            _ = CloseSocketRaw(s)
            (*op).res = r
            return
        }
        if !AttachSocket(s) {
            r := LastError()
            _ = CloseSocketRaw(s)
            (*op).res = r
            return
        }
        var local := raw.Null[u8]()
        var remote := raw.Null[u8]()
        var llen: i32 = 0
        var rlen: i32 = 0
        addrs := raw.Read(raw.PtrCast[u64, fn(*u8, u32, u32, u32, **u8, *i32, **u8, *i32)](WsFn(WsAcceptAddrs)))
        addrs(AcceptArea(op, o), 0, AcceptSlot(op), AcceptSlot(op), raw.OfMut(mut local), raw.OfMut(mut llen),
            raw.OfMut(mut remote), raw.OfMut(mut rlen))
        if raw.IsNull(remote) || !DecodeAddr(remote, mut (*op).addr) {
            (*op).addr = SockAddr(family: 0, port: 0, addr: [0; 16], scope_id: 0)
        }
        (*op).res = Ok(Trunc[i64](s))
    }
}

// SubmitConnect is ConnectEx, which needs a bound socket: h is bound to the
// wildcard address of its family first (WSAEINVAL: already bound).
SubmitConnect(op: *Op, o: *u8): bool {
    unsafe "op is the caller's pending operation" {
        if !WinsockReady() {
            (*op).res = FailWith(core.SysErr.Other)
            return false
        }
        af := OsFamily((*op).addr.family)
        if af == 0 {
            (*op).res = FailWith(core.SysErr.InvalidAddress)
            return false
        }
        var w: [4]u64 = [0; 4]
        wp := raw.PtrCast[u64, u8](raw.Data(w[..]))
        Put16(wp, 0, af)
        if BindRaw((*op).h, wp, Trunc[i32](OsAddrLen((*op).addr.family))) != 0 {
            e := WsaError()
            if e != WsaeInval {
                (*op).res = Fail(e)
                return false
            }
        }
        n := EncodeAddr(&(*op).addr, raw.Offset(o, OsAddrs))
        connect := raw.Read(raw.PtrCast[u64, fn(uint, *u8, i32, *u8, u32, *u32, *u8): i32](WsFn(WsConnectEx)))
        if connect((*op).h, raw.Offset(o, OsAddrs), Trunc[i32](n), raw.Null[u8](), 0,
            raw.PtrCast[u8, u32](raw.Offset(o, OsCount)), o) != 0 {
            if SkipSockets() {
                ConnectDone(op)
                return false
            }
            return true
        }
        e := WsaError()
        if e == WsaIoPending {
            return true
        }
        (*op).res = Fail(e)
        return false
    }
}

// ConnectDone sets the connect context, so shutdown and getsockname work.
ConnectDone(op: *Op) {
    unsafe "op's connect completed" {
        if SetSockOpt((*op).h, SolSocket, SoUpdateConnectContext, raw.Null[u8](), 0) != 0 {
            (*op).res = WsaFail()
            return
        }
        (*op).res = Ok(0)
    }
}

/// PollCancel asks the OS to cancel op; its packet still arrives, with 995
/// (Canceled) unless the operation finished first. A call after the packet
/// was dequeued but before the task resumed finds nothing (ERROR_NOT_FOUND).
/// A child wait has no I/O to cancel: the cancel takes the state word from
/// armed and posts the Canceled packet itself (ProcCancel).
/// O(1) system calls; allocates nothing.
export PollCancel(op: *Op) {
    unsafe "op is pending or not yet resumed: its OVERLAPPED lives (one-owner rule)" {
        if (*op).kind == OpProcWait || (*op).kind == OpReady {
            ProcCancel(OsArea(op))
            return
        }
        if (*op).kind == OpResolve {
            ResolveCancel(OsArea(op))
            return
        }
        _ = CancelIoEx((*op).h, OsArea(op))
    }
}

// A child wait (OpProcWait) or object wait (OpReady) keeps the registered wait
// at OsWait and its state word at OsProcState: 0 idle (before the submission,
// or after a failed one), 1 armed, 2 the object signalled / process ended
// (ProcFired posted the packet), 3 cancelled (ProcCancel posted it), 4 fired
// after cancel (ProcFired saw ProcCancelled after the signal was consumed),
// 5 timed out (ProcFired with timed != 0 posted the packet). The one packet
// comes from whichever of the two Cas(1, ..) wins, so exactly one completion
// arrives, and a cancel before the submission or after the completion finds
// no 1 and does nothing. The callback reads only the state word and the port;
// the registered wait lives until the task's PollRelease, whose blocking
// UnregisterWaitEx waits out a callback still running, so the os area (in
// the task's frame) outlives every callback. This is the child wait of
// libuv's uv_process (RegisterWaitForSingleObject, WT_EXECUTEINWAITTHREAD,
// WT_EXECUTEONLYONCE) and of tokio's Windows child: a waiting child holds
// no thread of this process (the system's wait thread serves 63 waits).
const OsWait: int = 40
const OsProcState: int = 64
const ProcArmed: u32 = 1
const ProcEnded: u32 = 2
const ProcCancelled: u32 = 3
const ProcCancelledFired: u32 = 4
const ProcTimedOut: u32 = 5
const WtExecuteInWaitThread: u32 = 4
const WtExecuteOnlyOnce: u32 = 8
const Infinite: u32 = 0xFFFF_FFFF
const WaitFailed: u32 = 0xFFFF_FFFF

ProcState(o: *u8): *raw.Atomic[u32] {
    unsafe "the state word of op's os area, 4-byte aligned" {
        return raw.PtrCast[u8, raw.Atomic[u32]](raw.Offset(o, OsProcState))
    }
}

// SubmitProcWait: a process that has ended or an object already signalled
// finishes at once; else a one-shot registered wait posts op's packet when
// it fires or times out. OpReady uses op.pos as timeout_ns (negative: forever).
SubmitProcWait(op: *Op, o: *u8): bool {
    unsafe "kernel32; op is the caller's pending operation" {
        h := (*op).h
        wf := WaitForSingleObject(h, 0)
        if wf == 0 {
            (*op).res = if (*op).kind == OpReady { Ok(0) } else { ProcExit(h) }
            return false
        }
        if (*op).kind == OpReady && (*op).pos == 0 {
            if wf == WaitTimeout {
                (*op).res = Ok(1)
                return false
            }
            if wf == WaitFailed {
                (*op).res = LastError()
                return false
            }
        }
        ms: u32 = if (*op).kind == OpReady && (*op).pos >= 0 { Millis((*op).pos) } else { Infinite }
        // Armed before the wait exists, so its callback finds 1. Edge: the
        // Store is sequentially consistent with the callback's and the
        // cancel's Cas (every atomic here is SC).
        (*ProcState(o)).Store(ProcArmed)
        if RegisterWaitForSingleObject(raw.PtrCast[u8, uint](raw.Offset(o, OsWait)), h, ProcFired, o, ms,
            WtExecuteInWaitThread | WtExecuteOnlyOnce) == 0 {
            r := LastError()
            // A cancel that ran meanwhile already posted the packet: that
            // is op's completion, so op is pending after all.
            if !(*ProcState(o)).Cas(ProcArmed, 0) {
                return true
            }
            (*op).res = r
            return false
        }
        return true
    }
}

// ProcFired is the registered wait's callback, on the system's wait thread
// (a foreign thread: atomics and one system call only). Edge: its Cas from
// armed is the one-completion rule's; when a signal arrived after ProcCancel
// won (timed == 0), Cas(ProcCancelled, ProcCancelledFired) records it for
// PollRelease after UnregisterWaitEx waits out this callback.
ProcFired(o: *u8, timed: u8) {
    unsafe "o is the os area of a pending op, alive until PollRelease" {
        if timed != 0 {
            if (*ProcState(o)).Cas(ProcArmed, ProcTimedOut) {
                _ = PostQueuedCompletionStatus(Port(), 0, 0, o)
            }
            return
        }
        if (*ProcState(o)).Cas(ProcArmed, ProcEnded) {
            _ = PostQueuedCompletionStatus(Port(), 0, 0, o)
            return
        }
        _ = (*ProcState(o)).Cas(ProcCancelled, ProcCancelledFired)
    }
}

// ProcCancel posts the Canceled packet of a pending child wait (above).
ProcCancel(o: *u8) {
    unsafe "o is the os area of a pending op" {
        if (*ProcState(o)).Cas(ProcArmed, ProcCancelled) {
            // The status (STATUS_CANCELLED, hrtimer.mk's) is written before
            // the post; the dequeuer reads it after (the port's edge).
            Put64(o, 0, u64(StatusCancelled))
            _ = PostQueuedCompletionStatus(Port(), 0, 0, o)
        }
    }
}

// ProcExit is the exit code of the ended process h (a u32, as Windows
// keeps it).
ProcExit(h: uint): OsResult {
    var code: u32 = 0
    unsafe "kernel32; code is a u32 in this frame" {
        if GetExitCodeProcess(h, raw.OfMut(mut code)) == 0 {
            return LastError()
        }
    }
    return Ok(i64(code))
}

/// PollRelease releases what a submission of op keeps past its completion,
/// once its Await is over (the task calls it after a child wait and after an
/// accept on a shared-port listener, whatever the answer): the registered
/// wait (at OsWait for a child wait, at OsShareWait for a shared accept) and
/// a shared accept's event. The blocking form of UnregisterWaitEx
/// (INVALID_HANDLE_VALUE) returns after a callback still running has
/// returned, and no callback starts after it, so op's frame may go. A wait
/// that never registered (a finish at once, a cancel before the submission)
/// is 0. Any other op holds nothing.
/// O(1) system calls (UnregisterWaitEx blocks while a callback still runs);
/// allocates nothing.
export PollRelease(op: *Op) {
    o := OsArea(op)
    unsafe "kernel32; op's Await is over, so op is this thread's" {
        var at := OsWait
        if (*op).kind == OpAccept {
            if (*op).pos != SharedPort {
                return
            }
            at = OsShareWait
        } else if (*op).kind != OpProcWait && (*op).kind != OpReady {
            return
        }
        w := Trunc[uint](Get64(o, at))
        if w != 0 {
            _ = UnregisterWaitEx(w, InvalidHandle)
            Put64(o, at, 0)
        }
        if (*op).kind == OpReady && (*ProcState(o)).Load() == ProcCancelledFired {
            (*op).len = 1
        }
        if (*op).kind == OpAccept {
            ShareEventClose(o)
        }
    }
}

/// A shared-port listener (Supervise's child): a socket inherited
/// from another process stays associated with that process's completion
/// port (the association belongs to the file object, and a second
/// CreateIoCompletionPort fails), so an AcceptEx on it would post its packet
/// to the parent's port. Its accepts (op.pos SharedPort) are issued with an
/// event whose low bit is set, which queues no packet anywhere, and a
/// one-shot registered wait on the event posts the op's OVERLAPPED to this
/// process's port (libuv's UV_HANDLE_EMULATE_IOCP). The accepted socket is a
/// new file object and is attached here as usual. The event is at OVERLAPPED
/// offset 24 (hEvent, low bit set), the registered wait at OsShareWait (after
/// the two address slots).
export const SharedPort: i64 = 1
const OsShareWait: int = 152

// ShareEventClose closes a shared accept's event, if it has one.
ShareEventClose(o: *u8) {
    ev := Get64(o, 24) & 0xFFFF_FFFF_FFFF_FFFE
    if ev != 0 {
        unsafe "kernel32; the event is this op's" {
            _ = CloseHandle(Trunc[uint](ev))
        }
        Put64(o, 24, 0)
    }
}

// ShareFired is a shared accept's callback on the system's wait thread (a
// foreign thread: one system call). The kernel wrote the OVERLAPPED's
// status before it set the event; the post hands op to a dequeuer here.
ShareFired(o: *u8, timed: u8) {
    unsafe "kernel32; o is the os area of a pending accept, alive until PollRelease" {
        _ = PostQueuedCompletionStatus(Port(), 0, 0, o)
    }
}

// ShareArm registers the wait of a pending shared accept. When it cannot,
// the accept is cancelled and waited for here (CancelIoEx, then the event),
// so nothing is left pending, and op fails at once.
ShareArm(op: *Op, o: *u8): bool {
    unsafe "kernel32; op's accept is pending" {
        ev := Trunc[uint](Get64(o, 24) & 0xFFFF_FFFF_FFFF_FFFE)
        if RegisterWaitForSingleObject(raw.PtrCast[u8, uint](raw.Offset(o, OsShareWait)), ev, ShareFired, o, Infinite,
            WtExecuteInWaitThread | WtExecuteOnlyOnce) != 0 {
            return true
        }
        r := LastError()
        _ = CancelIoEx((*op).h, o)
        _ = WaitForSingleObject(ev, Infinite)
        ShareEventClose(o)
        _ = CloseSocketRaw(Trunc[uint](Get64(o, OsSock)))
        (*op).res = r
        return false
    }
}

// IsSocketKind: the kinds whose errors come through Winsock.
IsSocketKind(k: u32): bool {
    return k == OpAccept || k == OpConnect || k == OpRecv || k == OpSend || k == OpSendTo || k == OpRecvFrom
}

// Finish turns op's packet (status: the OVERLAPPED's NTSTATUS) into op.res.
// false only when an accept was issued again and is still pending.
Finish(op: *Op, o: *u8, status: u64, bytes: u32): bool {
    unsafe "op's packet was just dequeued: the op is this thread's until it is returned" {
        k := (*op).kind
        if k == OpResolve {
            ResolveFinish(op, o)
            return true
        }
        if k == OpProcWait || k == OpReady {
            if status != 0 {
                (*op).res = FailWith(core.SysErr.Canceled)
            } else if k == OpReady && (*ProcState(o)).Load() == ProcTimedOut {
                (*op).res = Ok(1)
            } else {
                (*op).res = if k == OpReady { Ok(0) } else { ProcExit((*op).h) }
            }
            return true
        }
        var e: u32 = 0
        var n := bytes
        if status != 0 {
            var flags: u32 = 0
            if IsSocketKind(k) {
                f := raw.Read(raw.PtrCast[u64, fn(uint, *u8, *u32, i32, *u32): i32](WsFn(WsOverlapped)))
                if f((*op).h, o, raw.OfMut(mut n), 0, raw.OfMut(mut flags)) == 0 {
                    e = WsaError()
                }
            } else if GetOverlappedResult((*op).h, o, raw.OfMut(mut n), 0) == 0 {
                e = GetLastError()
            }
        }
        // Test point 5: one accepted connection reports a reset before
        // the accept, so the op goes through ReissueAccept with no cancel
        // (ResubCancel). A Cas from 0: exactly one packet is changed.
        // Edge: none; a test record, one Cas from 0.
        if e == 0 && k == OpAccept && TestOn(5) && Hooks.faked.Cas(0, 1) {
            e = WsaeConnReset
        }
        if e == 0 {
            if k == OpAccept {
                AcceptDone(op, o)
            } else if k == OpConnect {
                ConnectDone(op)
            } else if k == OpRecvFrom || k == OpSendTo {
                DgramDone(op, o, n, false)
            } else if k == OpLock {
                // Granted: a lock moves no bytes.
                (*op).res = Ok(1)
            } else {
                (*op).res = Ok(i64(n))
            }
            return true
        }
        // Data wins: a read or a write that a
        // cancel ended with a nonzero count answers the count. This is the
        // contract's rule; no driver measured here reports one. A named
        // pipe write cancelled after its reader took part of it completes
        // with 995 and a count of 0 (measured), so std.task keeps
        // pipe writes to what cannot end in part (sys.PipeRoom). A
        // datagram, an accept and a connect move nothing in part.
        if e == ErrorOperationAborted && n > 0 && (k == OpRead || k == OpWrite || k == OpRecv || k == OpSend) {
            (*op).res = Ok(i64(n))
            return true
        }
        if k == OpAccept {
            _ = CloseSocketRaw(Trunc[uint](Get64(o, OsSock)))
            if e == WsaeConnReset || e == ErrorNetnameDeleted {
                // The listener did not fail: a cancelled accept ends here
                // as Canceled (not Reset), any other is issued again. A
                // shared accept's registered wait has fired, so it goes back
                // to the task with Reset, which releases it and asks again
                // (std.task NativeAccept).
                // Edge: a canceller stores cancel before its own cancel call,
                // so a 0 here is followed by a cancel that finds the op.
                if (*op).cancel.Load() == 1 {
                    (*op).res = FailWith(core.SysErr.Canceled)
                    return true
                }
                if (*op).pos == SharedPort {
                    (*op).res = Fail(e)
                    return true
                }
                return ReissueAccept(op, o)
            }
        }
        if k == OpRecvFrom && e == WsaeMsgSize {
            DgramDone(op, o, n, true)
            return true
        }
        if k == OpRead && (e == ErrorHandleEof || e == ErrorBrokenPipe) {
            (*op).res = Ok(0)
            return true
        }
        if k == OpRead {
            (*op).res = ReadFail((*op).h, e)
        } else {
            (*op).res = Fail(e)
        }
        return true
    }
}

// ResubWord is op's handoff word: 0 none, 1 a reissuer is between its
// AcceptEx and its cancel check, 2 a PollWait dequeued the packet in that
// window and left the byte count at OsResubBytes (the status stays in the
// OVERLAPPED).
ResubWord(o: *u8): *raw.Atomic[u32] {
    unsafe "the handoff word of op's os area, 4-byte aligned" {
        return raw.PtrCast[u8, raw.Atomic[u32]](raw.Offset(o, OsResub))
    }
}

// ResubClaim is PollWait's check for every packet of an op: true when a
// reissuer still holds op in its window and finishes it, false when
// PollWait finishes it as usual.
ResubClaim(o: *u8, bytes: u32): bool {
    w := ResubWord(o)
    unsafe "the packet was just dequeued, so op is live: its task waits for it" {
        // Edge: 0, no resubmitter holds op. The word is 1 from before the
        // submission that produced this completion until its submitter
        // releases it, and this Load is after the completion, so 0 here
        // means the release came first (SC order) and op is this reaper's.
        if (*w).Load() == 0 {
            return false
        }
        Put32(o, OsResubBytes, bytes)
        // The count is written before the Cas; the reissuer reads it after
        // its own Cas(1, 0) fails on this 2 (the handoff's release/acquire
        // edge, sequentially consistent).
        return (*w).Cas(1, 2)
    }
}

// ReissueAccept issues op's AcceptEx again after a reset before the accept.
// true: op finished (res set, the caller hands it out); false: pending, its
// packet comes from PollWait.
ReissueAccept(op: *Op, o: *u8): bool {
    w := ResubWord(o)
    unsafe "op is this thread's: its packet was dequeued and not handed out" {
        // Taken before AcceptEx: from here no PollWait hands op out.
        // Edge: the Store(1) is ordered before AcceptEx, so a PollWait that
        // dequeues its packet sees 1.
        (*w).Store(1)
        // Test point 2: the window between the failed packet and the
        // reissue, in which a PollCancel's CancelIoEx finds nothing to
        // cancel (ResubCancel).
        _ = TestPoint(2)
        raw.Fill(o, 0, 32)
        if !SubmitAccept(op, o) {
            // Edge: none; no accept is in flight, so op stays this thread's.
            (*w).Store(0)
            return true
        }
        // Test point 10: the window after the AcceptEx, in which another
        // thread's PollWait may dequeue the new packet and leave op here
        // (ReissueRace).
        _ = TestPoint(10)
        // Edge: a cancel stored before this load may have run CancelIoEx
        // before the reissue; cancel it now (a cancel stored after this load
        // runs its CancelIoEx after the AcceptEx, which it finds).
        if (*op).cancel.Load() == 1 {
            _ = CancelIoEx((*op).h, o)
        }
        // Edge: the Cas(1, 0) hands op to PollWait; it fails when a PollWait
        // dequeued the packet meanwhile, which wrote the bytes read below
        // before it changed the word.
        if (*w).Cas(1, 0) {
            return false
        }
        // The packet was dequeued while this thread held the word; Finish
        // may issue the accept again, closing its own window inside.
        bytes := Get32(o, OsResubBytes)
        // Edge: none; op stays this thread's, and 0 marks no reissue in flight.
        (*w).Store(0)
        return Finish(op, o, Get64(o, 0), bytes)
    }
}

/// PollWait blocks up to timeout_ns (negative: forever) and returns the
/// completions it reaped into out, at most out.Len() (it dequeues no more
/// packets than that, so none is lost); the wake packet and a timer's packet
/// are not completions. A finite timeout arms a high-resolution timer
/// (hrtimer.mk), so the wait ends within the timer's resolution instead of
/// the system tick; the millisecond timeout stays as its bound.
/// O(min(out.Len(), 64)) packets besides the wait: a finite wait arms and
/// releases its timer, a failed completion reads its error with one more
/// system call, and an accept reset before it was accepted is issued again
/// (ReissueAccept); allocates nothing.
export PollWait(timeout_ns: i64, out: mut []*Op): int {
    var max := out.Len()
    if max > 64 {
        max = 64
    }
    if max < 1 {
        return 0
    }
    if timeout_ns < 0 {
        return Dequeue(timeout_ns, out, max).k
    }
    if timeout_ns == 0 {
        // A non-blocking call never keeps a wake (the Linux backends' rule
        // too). The mechanism, as Windows Internals describes completion
        // ports and consistent with WakeLate (not measured beyond it): a
        // packet posted while a thread waits on the port is given to that
        // thread, so a wake packet is queued, and can be dequeued here, when
        // no thread was waiting yet (an idle carrier between its last check
        // and its wait, or every waiter held by the port's concurrency
        // limit). That carrier's wait would then sleep through the wake
        // (WakeKeep and WakeLate check it). So a wake taken here is posted
        // again, once per call: several wakes may make one waiter return
        // (PollWake's rule). Cost: one post per non-blocking call while a
        // wake is queued and nobody waits, which the scheduler's
        // one-wake-in-flight rule bounds.
        r := Dequeue(0, out, max)
        if r.woke {
            PollWake()
        }
        return r.k
    }
    // A finite wait that ends with no completion and no wake before its
    // deadline (by Now) waits again for the rest: the timer and the
    // millisecond timeout run on the interrupt clock, and a 30 ms wait
    // measured 29.224 ms by QueryPerformanceCounter on a Windows 11
    // development machine (the EpollMs probe). A stray timer packet of another slot does not end
    // a wait early either. A deadline past i64's largest value is no
    // deadline (HrArm gives such a wait no timer and Millis caps it).
    start := Now()
    deadline: i64 = if timeout_ns <= 0x7FFF_FFFF_FFFF_FFFF - start { start + timeout_ns } else { -1 }
    var left := timeout_ns
    // Test point 7: one stray timer packet, as another slot's late timer
    // would leave (StrayTimer).
    if TestPoint(7) {
        unsafe "a kernel32 call that passes no memory, only values and handles" {
            _ = PostQueuedCompletionStatus(Port(), 0, HrKey, raw.Null[u8]())
        }
    }
    for {
        a := HrArm(left)
        var wait := left
        if a.fired {
            // The timer expired before its packet was associated: reap what
            // is already there, then judge the deadline as below (the timer
            // runs on the interrupt clock too).
            wait = 0
        }
        r := Dequeue(wait, out, max)
        HrRelease(a.slot)
        if r.k > 0 || r.woke || deadline < 0 {
            return r.k
        }
        now := Now()
        if now >= deadline {
            return 0
        }
        left = deadline - now
    }
}

// Dequeue is one GetQueuedCompletionStatusEx of at most max packets, each
// finished and put in out; woke says a PollWake packet was among them.
Dequeue(timeout_ns: i64, out: mut []*Op, max: int): (k: int, woke: bool) {
    var entries: [256]u64 = [0; 256]
    var got: u32 = 0
    unsafe "kernel32; entries holds 64 OVERLAPPED_ENTRY records" {
        if GetQueuedCompletionStatusEx(Port(), raw.PtrCast[u64, u8](raw.DataMut(mut entries[..])), Trunc[u32](max),
            raw.OfMut(mut got), Millis(timeout_ns), 0) == 0 {
            return (k: 0, woke: false)
        }
        var k := 0
        var woke := false
        for i in 0..int(got) {
            ov := entries[i * 4 + 1]
            if ov == 0 {
                // A wake (WakeKey) or a timer's packet (HrKey).
                if Trunc[uint](entries[i * 4]) == WakeKey {
                    woke = true
                }
                continue
            }
            o := raw.FromAddr[u8](Trunc[uint](ov))
            op := raw.FromAddr[Op](Trunc[uint](Get64(o, OsBack)))
            bytes := Trunc[u32](entries[i * 4 + 3])
            if ResubClaim(o, bytes) {
                continue
            }
            if Finish(op, o, Get64(o, 0), bytes) {
                out[k] = op
                k += 1
            }
        }
        return (k: k, woke: woke)
    }
}

/// PollTestHook is the epoll backend's lost-edge test hook; a completion port
/// has no edge to lose, so it never runs cb.
/// Tests only. O(1); allocates nothing.
export PollTestHook(cb: fn(*u8), arg: *u8, on: bool): u64 {
    return 0
}

/// PollTestPark is the Linux one-blocker rule's test hook; every Windows
/// caller waits on the port itself, so no thread parks and cb never runs.
/// Tests only. O(1); allocates nothing.
export PollTestPark(cb: fn(*u8), arg: *u8, on: bool): u64 {
    return 0
}

/// PollWake makes one blocked PollWait return; from any thread.
/// O(1): at most one system call; allocates nothing.
export PollWake() {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        _ = PostQueuedCompletionStatus(Port(), 0, WakeKey, raw.Null[u8]())
    }
}
