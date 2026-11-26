// Why: the scheduler runs tasks on OS threads (carriers and the blocking
// pool) and parks idle threads, which on Windows needs its own calls.
//
// What: ThreadStart, ThreadInit, ThreadYield, the semaphores SemInit,
// SemWait and SemPost, ThreadInterrupt, and ThreadMade with the table of
// the threads ThreadStart made.
//
// How: CreateThread with a stack reservation, a stack guarantee set per
// thread for the overflow report, and kernel semaphores whose timed waits
// wait again until a deadline by Now() has passed, since a millisecond
// wait can end early. Tasks never block a thread here. Each thread
// ThreadStart made holds its id in a table while its entry runs, for the
// fault handler's test of whose thread faulted (ThreadMade).

import std.raw

// GuaranteeBytes is the stack the overflow exception may use.
const GuaranteeBytes: u32 = 32768
// StackReservation is STACK_SIZE_PARAM_IS_A_RESERVATION.
const StackReservation: u32 = 0x10000
// WaitTimeout is WAIT_TIMEOUT.
const WaitTimeout: u32 = 0x102
// MaxI64 is i64's largest value.
const MaxI64: i64 = 0x7FFF_FFFF_FFFF_FFFF
// MaxThreadStack is the largest stack ThreadStart takes (1 TiB, both
// targets): far above any stack std asks for, and far below the sizes at
// which stack + the guard page would overflow.
const MaxThreadStack: int = 1 << 40

/// ThreadStart runs entry(arg) on a new thread with a stack reservation of
/// stack bytes. value is the thread HANDLE, kept open. stack is 1 to
/// MaxThreadStack bytes (CreateThread rounds it up to whole pages, as the
/// Linux variant does); anything else is refused with
/// ERROR_INVALID_PARAMETER (Other) before anything is made, as on Linux (0
/// would have been the image's default reservation here and a fault on
/// Linux; StackArg).
/// O(1) system calls; reserves the thread's stack, kept for its life, and
/// commits one page for the start record, which the new thread frees.
export ThreadStart(entry: fn(*u8), arg: *u8, stack: int): OsResult {
    if stack <= 0 || stack > MaxThreadStack {
        return Fail(87)
    }
    unsafe "kernel32; the start record lives until the thread has read it" {
        rec := raw.PtrCast[u8, ThreadRec](VirtualAlloc(raw.Null[u8](), 4096, MemCommit | MemReserve, PageReadWrite))
        if raw.IsNull(rec) {
            return LastError()
        }
        raw.Write(rec, ThreadRec(entry: entry, arg: arg))
        var tid: u32 = 0
        h := CreateThread(raw.Null[u8](), Trunc[uint](stack), MkThreadStart, raw.PtrCast[ThreadRec, u8](rec), StackReservation,
            raw.OfMut(mut tid))
        if h == 0 {
            r := LastError()
            _ = VirtualFree(raw.PtrCast[ThreadRec, u8](rec), 0, MemRelease)
            return r
        }
        return Ok(Trunc[i64](h))
    }
}

struct ThreadRec {
    entry: fn(*u8)
    arg: *u8
}

// MkThreadStart is the LPTHREAD_START_ROUTINE of every thread std starts.
// The thread's id is in the table (MadeEnter) while its entry runs.
MkThreadStart(p: *u8): u32 {
    unsafe "p is the ThreadRec ThreadStart wrote" {
        rec := raw.Read(raw.PtrCast[u8, ThreadRec](p))
        _ = VirtualFree(p, 0, MemRelease)
        _ = ThreadInit()
        k := MadeEnter()
        f := rec.entry
        f(rec.arg)
        MadeLeave(k)
    }
    return 0
}

// MadeSlots is how many live threads ThreadStart made the table holds:
// more than std runs at once (256 carriers, the blocking pool's 1,024
// threads and a few of its own).
const MadeSlots: int = 2048

// MadeTable holds the ids of the live threads ThreadStart made, one word
// each, 0 for a free word (no thread's id is 0). Only a thread itself
// stores its id, and it clears the word before it ends: std ends no thread
// it made but by its entry's return.
struct MadeTable {
    ids: [2048]u32
}

var Made: MadeTable = MadeTable(ids: [0; 2048])

// MadeWord is word k of the table, as an atomic.
MadeWord(k: int): &raw.Atomic[u32] {
    unsafe "Made is static; a u32 has raw.Atomic[u32]'s layout, and every access to these words is atomic" {
        return &*raw.PtrCast[u32, raw.Atomic[u32]](raw.Of(&Made.ids[k]))
    }
}

// MadeEnter stores the calling thread's id in a free word of the table and
// gives its index, or -1 when every word is held (ThreadMade then answers
// false for the thread). O(MadeSlots); allocates nothing.
MadeEnter(): int {
    me := Trunc[u32](ThreadId())
    for k in 0..MadeSlots {
        // Edge: seq_cst; the Cas from 0 gives the word to this thread.
        if MadeWord(k).Load() == 0 && MadeWord(k).Cas(0, me) {
            return k
        }
    }
    return -1
}

// MadeLeave frees the word MadeEnter gave (nothing for -1).
MadeLeave(k: int) {
    if k >= 0 {
        // Edge: seq_cst; the word is free for any thread after this store.
        MadeWord(k).Store(0)
    }
}

// ThreadMade is whether the calling thread is one ThreadStart made whose
// entry is running: a carrier, a blocking-pool thread or another of std's
// own. A foreign thread (the host's, a system DLL's or a C library's own)
// answers false, and so does one started while the table was full. The
// fault handler's stack test (MkStack). A thread's id is unique while it
// runs, and only it stores its own, so a word holding it is this thread's
// own write, read in program order. Loads and GetCurrentThreadId, which
// leaves the thread's last error as it was; no lock, no allocation.
// O(MadeSlots).
ThreadMade(): bool {
    me := Trunc[u32](ThreadId())
    for k in 0..MadeSlots {
        // Edge: as above; another thread's word never holds this id.
        if MadeWord(k).Load() == me {
            return true
        }
    }
    return false
}

/// ThreadInit sets the stack guarantee the overflow report runs in, as a
/// thread starts; false when Windows refuses it.
/// The vectored handler is process-wide and installed by FaultHook.
/// O(1) system calls; allocates nothing.
export ThreadInit(): bool {
    unsafe "kernel32; g is a u32 in this frame" {
        var g: u32 = GuaranteeBytes
        return SetThreadStackGuarantee(raw.OfMut(mut g)) != 0
    }
}

/// ThreadYield gives the CPU to another ready thread, if there is one.
/// O(1): one system call; allocates nothing.
export ThreadYield() {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        _ = SwitchToThread()
    }
}

/// SemInit makes s ready to use, before it is shared; false when the OS
/// has no semaphore to give.
/// O(1): one system call; creates one kernel semaphore object.
export SemInit(s: mut Sem): bool {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        h := CreateSemaphoreW(raw.Null[u8](), 0, 0x7FFF_FFFF, raw.Null[u16]())
        if h == 0 {
            return false
        }
        s.h = h
        return true
    }
}

/// SemWait takes one count, waiting up to timeout_ns nanoseconds; false when
/// the time passed first. A negative timeout waits forever, and so does a
/// timeout whose end passes i64's largest value (Linux's Deadline). A
/// finite wait returns false only once its deadline by Now() has passed, as
/// the Linux variant's does: one WaitForSingleObject waits at most
/// 0xFFFF_FFFE ms (Millis), short of a timeout of 49.7 days or more, and its
/// milliseconds run on the interrupt clock, which can end a wait before
/// Now() reaches its deadline (PollWait's early end); so a call that times
/// out early waits again for the rest (SemLong). WAIT_FAILED (a handle that
/// is not a semaphore) is false at once.
/// O(1) system calls besides the wait; allocates nothing.
export SemWait(s: &Sem, timeout_ns: i64): bool {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        if timeout_ns < 0 {
            return WaitForSingleObject(s.h, 0xFFFF_FFFF) == 0
        }
        now := Now()
        if timeout_ns > MaxI64 - now {
            return WaitForSingleObject(s.h, 0xFFFF_FFFF) == 0
        }
        deadline := now + timeout_ns
        var left := timeout_ns
        for {
            var ms := Millis(left)
            if ms > 2 && TestPoint(8) {
                ms = 2
            }
            r := WaitForSingleObject(s.h, ms)
            if r == 0 {
                return true
            }
            if r != WaitTimeout {
                return false
            }
            left = deadline - Now()
            if left <= 0 {
                return false
            }
        }
    }
}

/// SemPost adds n counts, saturating at the maximum SemInit gave (2^31 - 1),
/// as the Linux variant does. ReleaseSemaphore takes an i32 count and adds
/// all or nothing, so n is first capped at 2^31 - 1 (a larger n would pass
/// as negative and post nothing), and a post past the maximum is halved
/// until it fits (at most 31 failed calls, only at saturation; SemBig).
/// O(1) system calls; allocates nothing.
export SemPost(s: &Sem, n: u32) {
    var c := n
    if c > 0x7FFF_FFFF {
        c = 0x7FFF_FFFF
    }
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        for c > 0 {
            if ReleaseSemaphore(s.h, Trunc[i32](c), raw.Null[i32]()) != 0 {
                return
            }
            c = c / 2
        }
    }
}

// Millis rounds a timeout up to whole milliseconds; negative is INFINITE.
Millis(ns: i64): u32 {
    if ns < 0 {
        return 0xFFFF_FFFF
    }
    var ms := ns / 1_000_000
    if ns % 1_000_000 != 0 {
        ms += 1
    }
    return if ms >= 0xFFFF_FFFE { 0xFFFF_FFFE } else { Trunc[u32](ms) }
}

/// ThreadInterrupt ends a blocking synchronous call of thread (the HANDLE
/// ThreadStart returned, never a pseudo-handle) with CancelSynchronousIo:
/// the call fails with ERROR_OPERATION_ABORTED, Canceled.
/// A thread with no such call in progress (ERROR_NOT_FOUND: the call has
/// not started to block, or has returned) is interrupted by nothing; the
/// caller re-sends (the early window). false only when the handle is
/// not a thread this process can cancel.
/// O(1): one system call; allocates nothing.
export ThreadInterrupt(thread: uint): bool {
    unsafe "a kernel32 call that passes no memory, only values and handles" {
        return if CancelSynchronousIo(thread) != 0 { true } else { GetLastError() == 1168 }
    }
}
