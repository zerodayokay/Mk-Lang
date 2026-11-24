// Why: an Mk library linked into a C host has no Main; its runtime must
// start and stop on the host's terms, and a function the host calls must
// still be able to wait, sleep and do I/O.
//
// What: the library runtime (MkLibStart, MkLibStop, StartLibrary,
// StopLibrary), the export thunk's test FromCNeeded, RunFromC and its
// semaphore pool (TakeSem, GiveSem), and CurrentIo.
//
// How: a library's runtime starts on the first call from C, or earlier through
// its <name>_mk_start (MkLibStart): the process steps of Start except the
// arguments, then carrier 0 on a thread of its own (the host keeps its
// threads), whose loop the other carriers join on demand as in an
// executable. A call from C off a task (codegen's export thunk, exports.go:
// FromCNeeded, then RunFromC) runs the function as a new task of the
// runtime while the calling thread waits on a semaphore, so the function
// may wait, sleep and do I/O; on a task, or on a carrier's system stack
// (inside a task's own extern call, which does not run the callback as a
// task), the thunk calls it directly. <name>_mk_stop (MkLibStop) ends every
// carrier's loop and every pool thread, joins the carriers and removes the
// fault handler; a restart in the same process is not supported, as
// unloading is not, so a call after the stop is panic 16.

import std.core
import std.raw
import std.rt
import std.sys

// The library runtime's states (LibWords.state).
const LibNone: u32 = 0
const LibStarting: u32 = 1
const LibRunning: u32 = 2
const LibStopping: u32 = 3
const LibStopped: u32 = 4

// LibWords: the state; the calls from C in flight (RunFromC); the options
// the runtime started with; carrier 0's thread (ThreadStartJoin's value);
// the semaphore pool's lock, records and free stack.
struct LibWords {
    state: raw.Atomic[u32]
    calls: raw.Atomic[u32]
    carriers: raw.Atomic[u32]
    stack: raw.Atomic[u64]
    c0: raw.Atomic[u64]
    semlock: raw.Atomic[u32]
    sems: raw.Atomic[u64]
    nfree: raw.Atomic[u32]
    nmade: raw.Atomic[u32]
}

var Lib: LibWords = LibWords(state: raw.Atomic[u32].New(0), calls: raw.Atomic[u32].New(0), carriers: raw.Atomic[u32].New(0),
    stack: raw.Atomic[u64].New(0), c0: raw.Atomic[u64].New(0), semlock: raw.Atomic[u32].New(0), sems: raw.Atomic[u64].New(0),
    nfree: raw.Atomic[u32].New(0), nmade: raw.Atomic[u32].New(0))

// SemPool is the semaphores RunFromC's callers wait on, made once each and
// reused (a Windows semaphore is a handle): free holds the indices of the
// free ones.
const SemPool: int = 256

struct SemRecs {
    sem: [256]sys.Sem
    free: [256]u32
}

/// MkLibStart is <name>_mk_start's body (the support pack's entry of a
/// library calls it with the options' fields): it starts the runtime with
/// carriers carriers (0: the executable's default, MK_CARRIERS or the
/// processor count) and stack_reserve bytes of system stack per carrier
/// (0: 8 MiB; at least MinStackReserve, at most 1 GiB), and answers 0; 1
/// when it was started already with other options; 2 after a stop (a
/// restart is cut). Safe from any thread: one caller starts it, the others
/// wait for it, and a caller that finds a stop in progress waits for its
/// end (a refused stop leaves the runtime running). O(1) system calls
/// besides the start's mappings.
export extern MkLibStart(carriers: u32, stack_reserve: u64): i32 {
    for {
        // Edge: the state's stores (here, MkLibStop, Start) publish every
        // start or stop step before them.
        s := Lib.state.Load()
        if s == LibRunning {
            // Edge: stored before the state's Store(LibRunning), read after
            // its Load above.
            if (carriers == 0 && stack_reserve == 0) || (carriers == Lib.carriers.Load() && stack_reserve == Lib.stack.Load()) {
                return 0
            }
            return 1
        }
        if s == LibStopped {
            return 2
        }
        // Edge: the compare-and-swap admits one starter; the others yield
        // until its Store(LibRunning), after every start step.
        if s == LibNone && Lib.state.Cas(LibNone, LibStarting) {
            // Edge: before the state's Store(LibRunning), which publishes them.
            Lib.carriers.Store(carriers)
            Lib.stack.Store(stack_reserve)
            StartLibrary(carriers, stack_reserve)
            Lib.state.Store(LibRunning)
            return 0
        }
        sys.ThreadYield()
    }
}

/// MkLibStop is <name>_mk_stop's body: legal when no call into the library
/// is running. It ends every carrier's loop and joins its thread, ends the
/// pool's threads, and removes the fault handler; 0, or 1 when a call is
/// running (nothing stops). A stop before any start does nothing and
/// answers 0, and the runtime may still start; a second stop answers 0.
/// After a stop a call is panic 16 and a restart is cut; a call that
/// arrives during a stop waits for its end, so a refused stop panics
/// nothing. O(carriers) system calls; frees nothing of the
/// heap or the slot regions, which stay reserved until the process ends.
export extern MkLibStop(): i32 {
    // Edge: as MkLibStart's loads of the state.
    s := Lib.state.Load()
    if s == LibNone || s == LibStopped {
        return 0
    }
    // Edge: the compare-and-swap orders the stop after every start step.
    if !Lib.state.Cas(LibRunning, LibStopping) {
        return 1
    }
    // Edge: a thunk raises calls before it reads the state (FromCNeeded),
    // so a call either sees LibStopping (panic 16) or is counted here.
    if Lib.calls.Load() != 0 {
        // Edge: the revert releases the calls waiting in FromCNeeded.
        Lib.state.Store(LibRunning)
        return 1
    }
    StopLibrary()
    // Edge: after every stop step; a waiting call then panics.
    Lib.state.Store(LibStopped)
    return 0
}

// MinStackReserve is the least carrier system stack a library's
// stack_reserve gives: a smaller one is raised to it, since a carrier runs
// the scheduler, user externs' C code and, on an overflow, its 32 KiB
// guarantee there (Linux maps exactly what it is given).
const MinStackReserve: u64 = 256 * 1024

// StartLibrary is Start for a library: Start's steps through the process
// streams and the native Io, without the arguments, on the calling (host)
// thread, then carrier 0's thread, which runs TaskInit and the scheduler loop; it returns once the
// TaskState page is published.
StartLibrary(carriers: u32, stack_reserve: u64) {
    rt.GiveFuncTable()
    rt.LinkYieldHook()
    // The environment (a library skips argv only): a start stack with no
    // arguments on Linux, read from procfs; Windows reads the process's
    // block and needs none (sys.LibraryEnv).
    env := sys.LibraryEnv()
    if Target.os == Os.Linux && raw.IsNull(env) {
        rt.Fatal("cannot read the environment")
    }
    if !rt.Init(env) {
        rt.Fatal("cannot reserve task stacks")
    }
    (lo, hi) := sys.LibraryImage()
    rt.SetImage(lo, hi)
    sys.ExternFaultHook(InExtern)
    sys.FaultOwnerHook(OnMkStack)
    // Windows: tells a library's own stack overflows from the host's.
    sys.LibraryOwnerHook(OnMkStack)
    sys.FaultHook(rt.OnFault)
    rt.ProbeBuild()
    if !rt.RegionsInit(-1, false) {
        rt.Fatal("cannot reserve task stacks")
    }
    ChainRegister()
    rt.HeapInit()
    backend := sys.PollInit(EnvIs(env, "MK_IO", "epoll"))
    if backend == 0 {
        rt.Fatal("cannot start the event loop")
    }
    if !rt.DecodeEnv(env) {
        rt.Fatal("cannot read the environment")
    }
    unsafe "the RtState page, before any task exists" {
        p := rt.Proc()
        (*p).stdout = Stream(1, 8192)
        (*p).stderr = Stream(2, 8192)
        (*p).stdin = Stream(0, 65536)
    }
    rt.SetNativeIo(NativeIo())
    var stack := SystemStackSize
    if stack_reserve > 0 && stack_reserve <= 1 << 30 {
        stack = Trunc[int](if stack_reserve < MinStackReserve { MinStackReserve } else { stack_reserve })
    }
    // Edge: before carrier 0's thread starts, which orders it for every
    // carrier thread (CarrierStackBytes).
    LibStack.bytes.Store(Trunc[u64](stack))
    // boot.sp is the environment's start stack: TaskInit reads MK_CARRIERS
    // and the test settings from it, as an executable's from its own.
    var boot := Boot(sp: env, backend: backend, lo: 0, hi: 0, want: int(carriers), lib: true)
    unsafe "boot lives in this frame until carrier 0 published the TaskState page, after its last read" {
        r := sys.ThreadStartJoin(LibCarrier0, raw.PtrCast[Boot, u8](raw.Of(&boot)), stack)
        if r.err != core.SysErr.None {
            rt.Fatal("cannot start the scheduler's thread")
        }
        // Edge: read only by the stop, after the state's Store(LibRunning).
        Lib.c0.Store(Trunc[u64](r.value))
    }
    // Edge: TaskInit's Store of the page, after its last read of boot.
    for Sched.state.Load() == 0 {
        sys.ThreadYield()
    }
}

// LibStackWords holds a library's carrier system stack size
// (MkLibStart's stack_reserve), 0 in an executable.
struct LibStackWords {
    bytes: raw.Atomic[u64]
}

var LibStack: LibStackWords = LibStackWords(bytes: raw.Atomic[u64].New(0))

// CarrierStackBytes is a carrier thread's system stack: a library's
// stack_reserve when it gave one, else SystemStackSize.
CarrierStackBytes(): int {
    // Edge: StartLibrary's store, before any carrier thread started.
    n := LibStack.bytes.Load()
    return if n == 0 { SystemStackSize } else { Trunc[int](n) }
}

// LibCarrier0 is a library's carrier 0 thread: the TaskState page (whose
// carrier 0 stack is this thread's, and whose lib flag is set before the
// page is published), then the loop, until the stop.
LibCarrier0(p: *u8) {
    unsafe "p is StartLibrary's Boot, alive until the page is published" {
        TaskInit(raw.PtrCast[u8, Boot](p))
    }
    Run(C0())
    CarrierEnd(C0())
}

// StopLibrary ends the loops and joins the threads (MkLibStop).
StopLibrary() {
    st := TS()
    unsafe "the TaskState page" {
        // Edge: before the wakes; an idle carrier reads it after PollWait
        // returns (FindRunnable), a pool thread after SemWait (PoolMain).
        (*st).stopping.Store(1)
        // Every started carrier ends: post wakes until all have counted
        // their end. A carrier still busy goes idle first (no call runs).
        // ncarriers may read want + 1 while a start past want gives its
        // claim back (StartCarrier), a carrier that never runs: clamped to
        // want, as KeptHandles does.
        // Edge: no call runs, so no carrier starts now; the loads read
        // each carrier's claim, and nexited each carrier's end.
        var n := (*st).ncarriers.Load()
        if n > (*st).want.Load() {
            n = (*st).want.Load()
        }
        for (*st).nexited.Load() < n {
            sys.PollWake()
            sys.ThreadYield()
        }
        for i in 1..int(n) {
            h := (*st).cthr[i]
            if h != 0 {
                sys.ThreadJoin(Trunc[i64](h))
            }
        }
        // Edge: StartLibrary's store, before the state's Store(LibRunning).
        sys.ThreadJoin(Trunc[i64](Lib.c0.Load()))
        // The pool's threads: one post each, then their ends.
        rt.Lock(&(*st).plock)
        m := (*st).pthreads
        rt.Unlock(&(*st).plock)
        sys.SemPost(&(*st).psem, m)
        // Edge: each pool thread's Add after its last step (PoolMain).
        for (*st).pexited.Load() < m {
            sys.ThreadYield()
        }
    }
    sys.FaultUnhook()
}

/// FromCNeeded is the export thunk's test (codegen exports.go): true
/// when the calling thread is off a task and off every carrier's system
/// stack, so the call runs as a task (RunFromC); false on a task, or inside
/// a task's own extern call (a carrier's system stack, where a callback
/// does not run as a task, so that call is direct and panic 16 stays for a
/// suspension in it). In a
/// library it starts the runtime on the first call (MkLibStart's defaults)
/// and counts the call, which RunFromC uncounts. A call that finds a stop
/// in progress waits for its end: a refused stop (a call runs) reverts the
/// state and the call goes on, and a call after a stop is panic 16.
/// O(1) besides the first start and a stop's wait: two
/// compares on the stack pointer.
export FromCNeeded(): bool {
    if !raw.IsNull(rt.CurrentSlot()) {
        return false
    }
    // Edge: as MkLibStop's: counted before the state is read, and kept
    // while a stop is in progress, so that stop either read this count
    // (and reverts) or read none and ends in LibStopped.
    _ = Lib.calls.Add(1)
    // Edge: as MkLibStart's loads of the state.
    var s := Lib.state.Load()
    for s == LibStopping {
        sys.ThreadYield()
        // Edge: MkLibStop's revert or its Store(LibStopped).
        s = Lib.state.Load()
    }
    if s != LibRunning && (s == LibStopped || MkLibStart(0, 0) == 2) {
        // Edge: the count given back; the process ends in the report.
        _ = Lib.calls.Add(0xFFFF_FFFF)
        rt.Reporting(raw.Null[rt.Site](), rt.PanicText(rt.PanicSuspendInCallback), ": a call after the library's stop", "")
    }
    if rt.SysStackOf(rt.StackPointer()) >= 0 {
        // Edge: uncounted at once; this call is direct.
        _ = Lib.calls.Add(0xFFFF_FFFF)
        return false
    }
    return true
}

// FromCReq is one call from C: the thunk's entry and frame, and the
// semaphore record its caller waits on.
struct FromCReq {
    entry: fn(*u8)
    frame: *u8
    sem: int
}

/// RunFromC runs entry(frame) as a new task of the runtime, on a Default
/// slot with the native Io, and waits on the calling thread until it
/// returns; FromCNeeded counted the call, which this uncounts.
/// The task's stack class is Default; a call waits (yielding the thread)
/// while every slot is taken. O(1) besides the call itself: a task, a
/// ready from a foreign thread (the global queue and a carrier's wake)
/// and a semaphore post and wait.
export RunFromC(entry: fn(*u8), frame: *u8) {
    var req := FromCReq(entry: entry, frame: frame, sem: TakeSem())
    need := TopBytes + FrameReserve
    var h := rt.TakeSlot(rt.ClassDefault)
    for raw.IsNull(h) || !rt.GrowTop(h, need) {
        if !raw.IsNull(h) {
            rt.FreeSlot(h)
        }
        sys.ThreadYield()
        h = rt.TakeSlot(rt.ClassDefault)
    }
    t := Lay(h, rt.NextTaskId())
    unsafe "the fresh task, readied once; req lives in this frame until the task posted" {
        (*t).io = NativeIo()
        (*t).block = raw.PtrCast[FromCReq, u8](raw.Of(&req))
        Context1(t, h, raw.Addr(t) & ~Trunc[uint](15), FromCEntry)
        ReadyOn(raw.Null[Carrier](), t)
        for !sys.SemWait(SemAt(req.sem), -1) {
        }
    }
    GiveSem(req.sem)
    // Edge: after the task's post; MkLibStop reads 0 only when no call runs.
    _ = Lib.calls.Add(0xFFFF_FFFF)
}

// FromCEntry runs a call from C on its task: the thunk's entry, the
// caller's post, the task's end.
FromCEntry(p: *u8) {
    unsafe "p is this task's record; its block is RunFromC's request, alive until the post" {
        t := raw.PtrCast[u8, Task](p)
        FinishHandoff(t)
        req := raw.PtrCast[u8, FromCReq]((*t).block)
        f := (*req).entry
        f((*req).frame)
        sys.SemPost(SemAt((*req).sem), 1)
        SwitchOut(t, AfterFree, raw.Null[raw.Atomic[u32]]())
    }
    for {
        sys.ThreadYield()
    }
}

// Recs is the semaphore pool's records, made on first use.
Recs(): *SemRecs {
    // Edge: the records are written before the Cas publishes them.
    var a := Lib.sems.Load()
    if a == 0 {
        p := rt.HeapAlloc(SizeOf[SemRecs](), 64)
        if raw.IsNull(p) {
            rt.Fatal("cannot allocate the library's semaphores")
        }
        unsafe "p is a fresh block of SizeOf[SemRecs]() bytes" {
            r := raw.PtrCast[u8, SemRecs](p)
            for i in 0..SemPool {
                (*r).sem[i] = sys.NewSem()
            }
        }
        // Edge: the Cas publishes the records written above; a loser
        // reads the winner's address, written before its Cas.
        if Lib.sems.Cas(0, u64(raw.Addr(p))) {
            a = u64(raw.Addr(p))
        } else {
            a = Lib.sems.Load() // another thread's; this block is not freed (once)
        }
    }
    unsafe "the published records" {
        return raw.FromAddr[SemRecs](Trunc[uint](a))
    }
}

// TakeSem takes a free semaphore of the pool, made on its first use;
// with every one taken it yields until one is given back.
TakeSem(): int {
    r := Recs()
    for {
        rt.Lock(&Lib.semlock)
        unsafe "the pool's records, under its lock" {
            // Edge: none needed; nfree, nmade and the records change only
            // under semlock, whose Lock and Unlock order them.
            n := Lib.nfree.Load()
            if n > 0 {
                i := int((*r).free[int(n) - 1])
                Lib.nfree.Store(n - 1)
                rt.Unlock(&Lib.semlock)
                return i
            }
            // Edge: under semlock, as nfree's.
            m := Lib.nmade.Load()
            if int(m) < SemPool {
                Lib.nmade.Store(m + 1)
                rt.Unlock(&Lib.semlock)
                if !sys.SemInit(mut (*r).sem[int(m)]) {
                    rt.Fatal("cannot make a semaphore")
                }
                return int(m)
            }
        }
        rt.Unlock(&Lib.semlock)
        sys.ThreadYield()
    }
}

// GiveSem gives semaphore i back to the pool.
GiveSem(i: int) {
    r := Recs()
    rt.Lock(&Lib.semlock)
    unsafe "the pool's records, under its lock" {
        // Edge: under semlock, as TakeSem's.
        n := Lib.nfree.Load()
        (*r).free[int(n)] = Trunc[u32](i)
        Lib.nfree.Store(n + 1)
    }
    rt.Unlock(&Lib.semlock)
}

// SemAt is semaphore i of the pool.
SemAt(i: int): &sys.Sem {
    unsafe "the published records, which live until the process ends" {
        return &(*Recs()).sem[i]
    }
}

/// CurrentIo is the running task's Io, none off a task: runtime.TaskIo's
/// body, so a function called from C, which runs as a task, finds
/// the Io to wait, sleep and do I/O with. O(1); allocates nothing.
export CurrentIo(): ?Io {
    t := Current()
    if raw.IsNull(t) {
        return none
    }
    unsafe "the running task's own record" {
        return some((*t).io)
    }
}
