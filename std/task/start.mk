// Why: an Mk executable has no C runtime, so something must take the
// process from its first instruction to Main: fault handlers, stack
// regions, heap, poller, arguments, streams, carriers and the main task.
//
// What: Start, the process entry; TaskInit (the TaskState page and the wait
// table), StartOn, MainEntry, the environment readers EnvInt and EnvIs, and
// Stream.
//
// How: the entry stub calls Start(sp): Windows mk_start with sp null, Linux
// _start with the initial stack pointer, macOS the support pack's main with
// the block of main's arguments on the main thread's stack. On Windows the main
// thread's own stack (8 MiB through the link's /stack) becomes carrier 0's
// system stack; on Linux, whose main-thread stack is whatever RLIMIT_STACK
// allows, and on macOS, Start maps an 8 MiB system stack and continues
// there. Main runs as the first task, on a Large slot; in a program with no
// task it runs on a stack of that size that Start maps on every target.
// Every failure before Main is rt.Fatal.

import std.core
import std.raw
import std.rt
import std.sys

// MainStackBytes is the stack Main runs on in a program with no task:
// a Large slot's size (rt.SlotBytes(rt.ClassLarge)), the slot Main gets
// with tasks.
const MainStackBytes: int = 16 << 20

// StartCommit is how much of carrier 0's Windows stack Start commits at its
// top (64 KiB). A fault there, as in an extern Main calls, needs the room
// below for the exception frame the kernel writes; with one page committed
// that frame passed the guard page on hosts with CET shadow stacks, whose
// frame is larger, and the process died before any handler ran.
const StartCommit: int = 64 << 10

const TaskStateBytes: int = 65536
const BucketBytes: int = 4096 * 64

// EnvInt reads a small decimal MK_* variable with GetEnvRaw, or -1.
EnvInt(sp: *u8, name: str): int {
    var buf: [32]u8 = [0; 32]
    n := sys.GetEnvRaw(sp, name, mut buf[..])
    if n <= 0 || n > 32 {
        return -1
    }
    var v := 0
    for i in 0..n {
        d := int(buf[i]) - 48
        if d < 0 || d > 9 || v > 100_000_000 {
            return -1
        }
        v = v * 10 + d
    }
    return v
}

// EnvIs reports whether variable name is exactly want.
EnvIs(sp: *u8, name: str, want: str): bool {
    var buf: [32]u8 = [0; 32]
    n := sys.GetEnvRaw(sp, name, mut buf[..])
    w := want.AsBytes()
    if n != w.Len() {
        return false
    }
    for i in 0..n {
        if buf[i] != w[i] {
            return false
        }
    }
    return true
}

// Stream fills one process stream.
Stream(i: int, cap: int): core.BufState {
    h := rt.NewHandle(sys.StdHandle(i), false)
    buf := rt.HeapAlloc(cap, 16)
    if h == 0 || raw.IsNull(buf) {
        rt.Fatal("cannot allocate the standard streams")
    }
    return core.BufState(lock: raw.Atomic[u32].New(0), h: h, buf: buf, len: 0, cap: cap, start: 0)
}

// Boot carries Start's values to the rest of the start on carrier 0's system
// stack: sp, the backend, and the stack's bounds when Start mapped it
// (Linux), else 0 and 0.
struct Boot {
    sp: *u8
    backend: u32
    lo: uint
    hi: uint
    // want is a library's carrier count option (lib.mk), 0 for the
    // default; an executable passes 0. lib marks a library's runtime.
    want: int
    lib: bool
}

// TaskInit maps the TaskState page and the wait table.
TaskInit(boot: *Boot) {
    p := sys.Reserve(TaskStateBytes, 65536)
    b := sys.Reserve(BucketBytes, 65536)
    if raw.IsNull(p) || raw.IsNull(b) || !sys.Commit(p, TaskStateBytes) || !sys.Commit(b, BucketBytes) {
        rt.Fatal("cannot reserve task stacks")
    }
    unsafe "fresh zero pages for the scheduler's state" {
        st := raw.PtrCast[u8, TaskState](p)
        (*st).buckets = raw.PtrCast[u8, Bucket](b)
        // No idle carrier armed yet; the heaps' tnext is CarriersInit's.
        // Edge: before the page is published (Sched.state's store below).
        (*st).armed_at.Store(MaxDeadline)
        sp := (*boot).sp
        (*st).backend = (*boot).backend
        seed := EnvInt(sp, "MK_TEST_SEED")
        if seed > 0 {
            (*st).jitter_seed = Trunc[u64](seed) *% 0x2545_F491_4F6C_DD1D | 1
        }
        (*st).spin = sys.CpuCount() > 1
        (*st).lib = (*boot).lib
        (*st).slot_cache = rt.UsableSlots(rt.ClassDefault) > 4096
        var want := CarrierWant(EnvInt(sp, "MK_CARRIERS"))
        if (*boot).want > 0 {
            want = CarrierWant((*boot).want)
        }
        if !CarriersInit(st, want) {
            rt.Fatal("cannot reserve task stacks")
        }
        // MK_TEST_POOL only outside release builds.
        test_pool := if rt.DevBuild() { EnvInt(sp, "MK_TEST_POOL") } else { -1 }
        if !PoolInit(st, test_pool) {
            rt.Fatal("cannot start the event loop")
        }
        c := raw.Offset((*st).carriers, 0)
        (lo, hi) := sys.ThreadStackBounds()
        (*c).sys_lo = lo
        (*c).sys_hi = hi
        if (*boot).hi != 0 {
            (*c).sys_lo = (*boot).lo
            (*c).sys_hi = (*boot).hi
        }
        rt.SetSysStack(0, (*c).sys_lo, (*c).sys_hi)
        w := sys.StackWords()
        (*c).sys_stack.tib = [w[0], w[1], w[2]]
        // Edge: every field above is written before the page's address is
        // published; every reader loads it (TS) after this store.
        Sched.state.Store(u64(raw.Addr(p)))
    }
    // A native task's cancellation point reads the scope flags'
    // count instead of calling NativeSleep (core.CancelPoint), and a native
    // task is known by its Io's table (HookMark). Registered from NativeIo,
    // the one site that makes a native Io, before the first task runs.
    core.NativeInit(NativeIo())
    // Io.NoCancel reads the task's depth through this hook.
    unsafe "DepthHooks is a global, alive for the whole process" {
        core.DepthInit(raw.Of(&DepthHooks))
    }
}

/// Start is the process entry: the whole start sequence. It never returns; every
/// failure before Main is rt.Fatal. Its cost is the start's: O(1) system
/// calls besides decoding the arguments and environment, O(n) in their bytes.
/// Maps the runtime's state pages and carrier records and allocates the
/// decoded arguments and environment, once, at start.
export Start(sp: *u8): never {
    // The function table and the extern filter go in before the hook, whose
    // installation publishes them to the handlers. A program with no task
    // has no carrier to ask about, so it links no filter.
    rt.GiveFuncTable()
    rt.LinkYieldHook() // Spin's hook: std.rt's, over the weak default
    if rt.UsesTasks() {
        sys.ExternFaultHook(InExtern)
        // The ownership test: a fault on a task slot or a carrier's
        // system stack is Mk's (Linux), and goes to the fault filters
        // whatever its pc (Windows).
        sys.FaultOwnerHook(OnMkStack)
    }
    sys.FaultHook(rt.OnFault)
    // A listener inherited from a spawn (MK_INHERIT) is no child's to have:
    // it is made close-on-exec (not inheritable) before anything can spawn,
    // whether or not this program ever asks for it. A program with no task
    // never spawns: spawn is one of its task fields.
    if rt.UsesTasks() {
        sys.InheritSeal(sp)
    }
    _ = sys.ThreadInit()
    if !rt.Init(sp) {
        rt.Fatal("cannot reserve task stacks")
    }
    // The build mode, before the test-only settings it gates
    // (MK_TEST_SLOTS and MK_TEST_POOL only outside release builds; a
    // release program's capacity never depends on them).
    rt.ProbeBuild()
    // The task stack regions only when a task can exist; without them
    // CurrentSlot is null everywhere (rt.SlotTop of an empty region).
    if rt.UsesTasks() {
        var test_slots := -1
        var prot_guards := false
        if rt.DevBuild() {
            test_slots = EnvInt(sp, "MK_TEST_SLOTS")
            // The PROT_NONE guard on a kernel that has light guards.
            prot_guards = EnvIs(sp, "MK_TEST_GUARDS", "prot")
        }
        if !rt.RegionsInit(test_slots, prot_guards) {
            rt.Fatal("cannot reserve task stacks")
        }
        ChainRegister()
    }
    rt.HeapInit()
    // A program that needs no task has no poller; Main then runs on carrier 0's system stack (StartOn).
    var backend: u32 = 0
    if rt.UsesTasks() {
        backend = sys.PollInit(EnvIs(sp, "MK_IO", "epoll"))
        if backend == 0 {
            rt.Fatal("cannot start the event loop")
        }
    }
    if !rt.DecodeArgs(sp) || !rt.DecodeEnv(sp) {
        rt.Fatal("cannot read the command line")
    }
    unsafe "the RtState page, before any task exists" {
        p := rt.Proc()
        (*p).stdout = Stream(1, 8192)
        (*p).stderr = Stream(2, 8192)
        (*p).stdin = Stream(0, 65536)
    }
    rt.SetNativeIo(NativeIo())
    var boot := Boot(sp: sp, backend: backend, lo: 0, hi: 0, want: 0, lib: false)
    if Target.os == Os.Linux || Target.os == Os.Macos || !rt.UsesTasks() {
        // An 8 MiB system stack for carrier 0, mapped as
        // sys.ThreadStart maps carrier k's (a guard page, then the stack;
        // here with CommitStack, so pages are supplied on touch and nothing
        // is charged up front). The guard is an OS page, at least 4 KiB
        // (16 KiB on arm64 macOS), so the stack starts on a page. On macOS
        // this also gives carrier 0 known bounds, which libSystem gives
        // for the threads it made only. The initial stack stays mapped, because
        // GetEnvRaw reads the environment from sp, and is never run on
        // again: Start's frame below is the last frame there.
        // With no task, Main runs on this stack instead of a Large
        // slot, so it is a Large slot's size on both targets, and how deep
        // Main may recurse does not depend on whether the program
        // has tasks. On Windows it is mapped as a fresh slot is (Prepare),
        // but with StartCommit committed at the top: then a WinGuardBytes
        // PAGE_GUARD window below it matching TaskGuarantee, and the TEB's
        // stack words in the context, so the kernel grows it on touch and
        // reports EXCEPTION_STACK_OVERFLOW at its bottom.
        size := if rt.UsesTasks() { SystemStackSize } else { MainStackBytes }
        ps := sys.PageSize()
        g := if ps > 4096 { ps } else { 4096 }
        base := sys.Reserve(size + g, g)
        if raw.IsNull(base) {
            rt.Fatal("cannot reserve task stacks")
        }
        boot.lo = raw.Addr(base) + Trunc[uint](g)
        boot.hi = boot.lo + Trunc[uint](size)
        var from: Context = [0; 8]
        var to: Context = [0; 8]
        unsafe "base is a fresh reservation of size + 4 KiB" {
            if Target.os == Os.Windows {
                top := raw.FromAddr[u8](boot.hi - Trunc[uint](StartCommit))
                if !sys.Commit(top, StartCommit) || !sys.GuardPages(raw.Offset(top, -rt.WinGuardBytes), rt.WinGuardBytes) {
                    rt.Fatal("cannot reserve task stacks")
                }
            } else if !sys.CommitStack(raw.Offset(base, g), size) {
                rt.Fatal("cannot reserve task stacks")
            }
        }
        unsafe "boot and from stay in Start's frame, which is never popped" {
            InitContext(raw.Of(&to), raw.FromAddr[u8](boot.hi), StartOn, raw.PtrCast[Boot, u8](raw.Of(&boot)))
            if Target.os == Os.Windows {
                // StackBase, StackLimit, DeallocationStack and
                // GuaranteedStackBytes, as Context1 sets a task's.
                to[1] = u64(boot.hi)
                to[2] = u64(boot.hi - Trunc[uint](StartCommit))
                to[3] = u64(raw.Addr(base))
                to[4] = TaskGuarantee
            }
            Switch(raw.Of(&from), raw.Of(&to))
        }
        rt.Fatal("scheduler: carrier 0 returned to the initial stack")
    }
    unsafe "boot is in this frame, which is never popped" {
        StartOn(raw.PtrCast[Boot, u8](raw.Of(&boot)))
    }
    rt.Fatal("scheduler: carrier 0 returned to the initial stack")
}

// StartOn is the rest of the start, on carrier 0's system stack; p is
// Start's Boot. It never returns.
StartOn(p: *u8) {
    if !rt.UsesTasks() {
        // A program with no task: no task, carrier, pool or
        // poller. Main runs here, on the MainStackBytes stack Start mapped,
        // so CurrentSlot is null for it and every path that tests it
        // takes its off-task branch (a blocking call inline); the heap
        // finds Main by this stack's bounds, recorded below before Main
        // runs, and gives it carrier 0's magazines (rt.MagOf). The guard
        // below the stack still makes an overflow a
        // panic (Linux: SetSysStack's guard; Windows: the kernel's overflow).
        unsafe "p is Start's Boot, in a frame that is never popped" {
            boot := raw.PtrCast[u8, Boot](p)
            (lo, hi) := sys.ThreadStackBounds()
            mapped := (*boot).hi != 0
            rt.SetSysStack(0, if mapped { (*boot).lo } else { lo }, if mapped { (*boot).hi } else { hi })
        }
        MainEntry(raw.Null[u8]())
        rt.Fatal("scheduler: Main returned past Finish")
    }
    unsafe "p is Start's Boot, in a frame that is never popped" {
        TaskInit(raw.PtrCast[u8, Boot](p))
    }
    if Target.os == Os.Macos {
        // Edge: single-threaded init before carrier 0 starts and before MainEntry runs.
        MainServiceEnabled.Store(1)
    }
    // Calls from C (lib.mk) run as tasks from here on.
    // Edge: after TaskInit published the TaskState page; a thunk's
    // FromCNeeded loads the state before it reads the page.
    Lib.state.Store(LibRunning)
    // Main in a Large slot, id 1.
    h := rt.TakeSlot(rt.ClassLarge)
    if raw.IsNull(h) || !rt.GrowTop(h, TopBytes + FrameReserve) {
        rt.Fatal("cannot reserve task stacks")
    }
    t := Lay(h, 1)
    unsafe "the fresh main task" {
        (*t).flags = MainFlag
        (*t).io = NativeIo()
        Context1(t, h, raw.Addr(t) & ~Trunc[uint](15), MainEntry)
        // Main goes straight to carrier 0's runnext: no other carrier
        // exists, and none is started for it (carriers start on demand).
        // Edge: before carrier 0's loop takes it; one thread, program order.
        (*t).state.Store(TaskRunnable)
        (*C0()).runnext.Store(AddrOf(t))
    }
    Run(C0())
}

// MainEntry runs on the main task's stack: sys.arena, the System,
// Main, then Finish.
MainEntry(p: *u8) {
    var arena := Arena.New(rt.ProcessHeap()) else {
        rt.Fatal("cannot allocate sys.arena")
    }
    system := core.NewSystem(rt.Args(), rt.NativeIo(), &arena, rt.ProcessHeap(), rt.Proc())
    r := rt.CallMain(&system)
    rt.Finish(r)
}
