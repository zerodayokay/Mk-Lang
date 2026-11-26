// Why: carriers and pool threads are made by std itself, without libc, and
// the fault handler needs each thread's stack bounds and an alternate
// signal stack to report a stack overflow.
//
// What: ThreadStart (ThreadMain), ThreadInit, ThreadYield, StackWords (none
// on Linux), ThreadStackBounds and the stack table (StackTable, StackClaim,
// StackFree).
//
// How: threads by clone on a mapped stack with a guard page, joined through
// the cleared TID. Each thread records its bounds in a shared table of
// 32-byte entries, since Linux has no NT_TIB. In system-libc mode
// ThreadStart hands the same mapping to pthread_create instead (pthread.mk).

import std.raw

// The clone flags: a thread in this process, joined
// through the cleared TID.
const CloneThread: uint = 0x100 | 0x200 | 0x400 | 0x800 | 0x10000 | 0x40000 | 0x100000 | 0x200000
const AltStackBytes: int = 65536
// MaxThreadStack is the largest stack ThreadStart takes (1 TiB, both
// targets): far above any stack std asks for, and far below the sizes at
// which the page rounding and the guard page would overflow.
const MaxThreadStack: int = 1 << 40

// ThreadRec sits at the top of a new thread's stack until the thread reads
// it (40 bytes of the 64 ThreadStart keeps below the top). lo and hi are the
// stack ThreadStart mapped, for ThreadStackBounds.
struct ThreadRec {
    entry: fn(*u8)
    arg: *u8
    tid: u32
    ctid: u32
    lo: uint
    hi: uint
    // alt: the address of the thread's alternate signal stack (ThreadMain
    // records it), which ThreadJoin unmaps; 0 when there is none.
    alt: uint
}

// The stack table (ThreadStackBounds; Linux has no TLS and no NT_TIB): one
// mapping of StackSlots entries of 32 bytes, made by the first ThreadStart
// (StackTable): the owner's tid at 0 (atomic, 0 free), lo at 8, hi at 16. A
// thread claims an entry in ThreadMain before its entry function runs and
// frees it when the function returns, so a reused tid never finds a stale
// entry. Only the owner reads lo and hi; other threads read only tid words.
struct StackState {
    page: raw.Atomic[u64]
}

var Stacks: StackState = StackState(page: raw.Atomic[u64].New(0))

const StackSlots: int = 512
const StackTableBytes: int = 16384

/// ThreadStart maps stack bytes, rounded up to whole pages, plus a guard
/// page and clones onto it. value is the tid. stack is 1 to MaxThreadStack
/// bytes; anything else is refused with EINVAL (Other) before anything is
/// mapped, as on Windows. Used as given, the size would overflow stack +
/// 4096 (a panic) near int's largest value, 0 would put the ThreadRec in
/// the guard page, and a size that is not a multiple of 16 would misalign
/// the new thread's stack (StackArg).
/// O(1) system calls; maps the thread's stack and its guard page, kept for
/// the thread's life; the first call also maps the stack table
/// (StackSlots entries), kept until exit.
export ThreadStart(entry: fn(*u8), arg: *u8, stack: int): OsResult {
    if stack <= 0 || stack > MaxThreadStack {
        return Fail(22)
    }
    if LibcBuild() {
        // System-libc mode: glibc's pthread_create on a mapping
        // of its own (pthread.mk), so the thread has its own control block.
        // A constant of the build: a static bin holds no
        // PthreadStart, and a libc-mode bin no clone path.
        return PthreadStart(entry, arg, stack, false)
    }
    total := (stack + 4095) / 4096 * 4096 + 4096
    base := Reserve(total, 4096)
    if raw.IsNull(base) {
        return FailWith(MapError(12))
    }
    unsafe "the mapping is this thread's stack for its whole life" {
        if !Commit(raw.Offset(base, 4096), total - 4096) {
            Release(base, total)
            return FailWith(MapError(12))
        }
        // The table exists before the thread does (a null table only
        // means ThreadStackBounds answers (0, 0) on that thread).
        _ = StackTable()
        top := raw.Offset(base, total - 64)
        rec := raw.PtrCast[u8, ThreadRec](top)
        raw.Write(rec, ThreadRec(entry: entry, arg: arg, tid: 0, ctid: 0, lo: raw.Addr(base) + 4096,
            hi: raw.Addr(base) + Trunc[uint](total), alt: 0))
        r := Clone(CloneThread, top, raw.Of(&(*rec).tid), raw.Of(&(*rec).ctid), 0, ThreadMain, top)
        if IsErr(r) {
            Release(base, total)
            return Fail(i64(-r))
        }
        return Ok(i64(r))
    }
}

// ThreadMain runs on the new thread's stack, below its ThreadRec.
ThreadMain(p: *u8) {
    unsafe "p is the ThreadRec ThreadStart wrote" {
        rec := raw.PtrCast[u8, ThreadRec](p)
        // The alternate stack's address goes in the record, read by
        // ThreadJoin once the kernel cleared ctid at this thread's exit.
        (*rec).alt = AltStackInit()
        e := StackClaim((*rec).lo, (*rec).hi)
        f := (*rec).entry
        f((*rec).arg)
        StackFree(e)
    }
}

// StackTable is the stack table's first byte, mapped on first use; null
// when it cannot be mapped. Two first callers may both map one: the Cas
// from 0 publishes exactly one (its zeros were written by the kernel before
// the Cas, and every reader loads the address after it: sequentially
// consistent), and the loser unmaps its own.
StackTable(): *u8 {
    // Edge: the winner's Cas publishes the filled page; a Load that reads it
    // sees the page.
    var v := Stacks.page.Load()
    if v == 0 {
        p := Reserve(StackTableBytes, 4096)
        if raw.IsNull(p) {
            return p
        }
        if !Commit(p, StackTableBytes) {
            Release(p, StackTableBytes)
            return raw.Null[u8]()
        }
        // Edge: the Cas from 0 publishes this page, filled above, to every
        // later Load.
        if Stacks.page.Cas(0, u64(raw.Addr(p))) {
            v = u64(raw.Addr(p))
        } else {
            Release(p, StackTableBytes)
            v = Stacks.page.Load()
        }
    }
    unsafe "the table mapping, never released once published" {
        return raw.FromAddr[u8](Trunc[uint](v))
    }
}

// StackTid is entry i's owner word.
StackTid(t: *u8, i: int): *raw.Atomic[u32] {
    unsafe "entry i of the table, 32-byte aligned" {
        return raw.PtrCast[u8, raw.Atomic[u32]](raw.Offset(t, i * 32))
    }
}

// StackClaim records [lo, hi) as the calling thread's stack and returns the
// entry, or -1 when the table is missing or full (ThreadStackBounds then
// answers (0, 0)). The Cas from 0 makes the entry this thread's alone; lo
// and hi are written after it and read only by this thread, so they need
// no edge.
StackClaim(lo: uint, hi: uint): int {
    pg := Stacks.page.Load()
    if pg == 0 {
        return -1
    }
    unsafe "the stack table page, published before any other thread starts, lives until exit" {
        t := raw.FromAddr[u8](Trunc[uint](pg))
        tid := Trunc[u32](ThreadId())
        for i in 0..StackSlots {
            if (*StackTid(t, i)).Load() == 0 && (*StackTid(t, i)).Cas(0, tid) {
                Put64(t, i * 32 + 8, u64(lo))
                Put64(t, i * 32 + 16, u64(hi))
                return i
            }
        }
    }
    return -1
}

// StackFree gives entry e back (the Store to 0 is ordered before any later
// claimant's Cas that takes it: sequentially consistent; that claimant
// writes lo and hi only after its Cas).
StackFree(e: int) {
    if e < 0 {
        return
    }
    unsafe "the stack table page, published before any other thread starts, lives until exit" {
        // Edge: the Cas that published the page, before any other thread
        // started.
        t := raw.FromAddr[u8](Trunc[uint](Stacks.page.Load()))
        (*StackTid(t, e)).Store(0)
    }
}

/// ThreadInit gives the thread a 64 KiB alternate signal stack, where the
/// fault handler runs.
/// false when the stack cannot be mapped or installed; it is then unmapped.
/// O(1) system calls; maps the 64 KiB alternate signal stack, kept for the
/// thread's life.
export ThreadInit(): bool {
    return AltStackInit() != 0
}

// AltStackInit is ThreadInit, giving the alternate stack's address, or 0
// when it could not be mapped or installed (then it is unmapped).
AltStackInit(): uint {
    p := Reserve(AltStackBytes, 4096)
    if raw.IsNull(p) {
        return 0
    }
    var ss: [3]u64 = [0; 3]
    ss[0] = u64(raw.Addr(p))
    ss[2] = u64(AltStackBytes)
    unsafe "system call; ss is a stack_t" {
        if Commit(p, AltStackBytes) && !IsErr(Syscall(Nrs().sigaltstack, raw.Addr(raw.Data(ss[..])), 0, 0, 0, 0, 0)) {
            return raw.Addr(p)
        }
    }
    Release(p, AltStackBytes)
    return 0
}

/// ThreadYield gives the CPU to another ready thread, if there is one.
/// O(1): one system call; allocates nothing.
export ThreadYield() {
    unsafe "a system call that passes no memory, only values and handles" {
        _ = Syscall(Nrs().sched_yield, 0, 0, 0, 0, 0, 0)
    }
}

/// StackWords is the thread's NT_TIB stack words (SysStack.tib); Linux
/// has no NT_TIB, so all four are 0.
/// O(1); allocates nothing.
export StackWords(): [4]u64 {
    return [0; 4]
}

// ThreadMade is whether the calling thread is Mk's: the main thread
// (Faults.main_tid, carrier 0's) or one ThreadStart made (its tid is in
// the stack table): the fault handler's ownership test (FaultOwned). A
// foreign thread answers false, and so does a thread made while the table
// was full. Async-signal-safe: one gettid and loads.
ThreadMade(): bool {
    tid := Trunc[u32](ThreadId())
    // Edge: stored at rt.Init on this thread when it is the main one.
    if tid == Faults.main_tid.Load() {
        return true
    }
    // Edge: as ThreadStackBounds' Load.
    pg := Stacks.page.Load()
    if pg == 0 {
        return false
    }
    unsafe "the published table; a matching owner word is this thread's own write" {
        t := raw.FromAddr[u8](Trunc[uint](pg))
        for i in 0..StackSlots {
            if (*StackTid(t, i)).Load() == tid {
                return true
            }
        }
    }
    return false
}

/// ThreadStackBounds is the calling thread's stack [lo, hi) when ThreadStart
/// made the thread: the mapping above its guard page (sys_lo, sys_hi).
/// (0, 0) on the main thread, on a foreign thread, and when the stack table
/// was full (more than 512 live threads) or could not be mapped.
/// O(StackSlots): one scan of the 512-entry table; allocates nothing.
export ThreadStackBounds(): (lo: uint, hi: uint) {
    // Edge: as StackTable's Load: the publishing Cas came after the page was
    // filled.
    pg := Stacks.page.Load()
    if pg == 0 {
        return (lo: 0, hi: 0)
    }
    unsafe "the published table; entry i's lo and hi are this thread's own writes" {
        t := raw.FromAddr[u8](Trunc[uint](pg))
        tid := Trunc[u32](ThreadId())
        for i in 0..StackSlots {
            if (*StackTid(t, i)).Load() == tid {
                return (lo: Trunc[uint](Get64(t, i * 32 + 8)), hi: Trunc[uint](Get64(t, i * 32 + 16)))
            }
        }
    }
    return (lo: 0, hi: 0)
}
