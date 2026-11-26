// Why: tasks run on carriers, the OS threads of the M:N scheduler; their
// records, start-up and stacks are one concern, and so are the pinned
// carriers behind runtime.Dedicated and runtime.Thread.
//
// What: Carrier and AfterSwitch (what the scheduler does once a task has
// switched out), CarriersInit, CarrierWant, StartCarrier, CarrierMain and
// CarrierStack; the pinned carriers (PinRec, PinClaim, PinReady, PinRun,
// PinDedicated, PinThread, PinJoin and their helpers).
//
// How: carrier 0 is the main thread; carrier k starts on demand on its own
// thread with an 8 MiB system stack, when a task is readied while every
// started carrier is busy and fewer than CarrierCount exist. The records
// are one reservation (TaskState.carriers), committed for CarrierCount
// carriers at start, never moved and never freed.

import std.core
import std.raw
import std.rt
import std.sys

// What the scheduler does once a task has switched out.
struct AfterSwitch {
    kind: u32
    word: *raw.Atomic[u32]
    task: *Task
}

const AfterNone: u32 = 0
const AfterUnlock: u32 = 1
const AfterHandshake: u32 = 2
const AfterYield: u32 = 3
const AfterFree: u32 = 4
// AfterUnlockKick is AfterUnlock, then OweArm (a sleeper's new earliest
// deadline, SleepFor).
const AfterUnlockKick: u32 = 5

/// The carrier's system stack for an extern call.
export struct SysStack {
    /// The top to switch to, 16-byte aligned, below the scheduler's frame.
    export sp: *u8
    /// Windows' NT_TIB StackBase, StackLimit and DeallocationStack of the
    /// system stack; zero on Linux.
    export tib: [3]u64
}

/// MaxCarriers is the most carriers a process runs; their records are
/// reserved at start.
export const MaxCarriers: int = 256
// SystemStackSize is every carrier's system stack.
const SystemStackSize: int = 8 << 20

/// One carrier. The fields above pad0 are the owner's (its thread alone
/// reads and writes them, except index and the system-stack words, written
/// before the thread starts). runnext, top, bottom and the ring are shared
/// with thieves and live on their own cache lines.
export struct Carrier {
    index: u32
    spinning: bool
    sys_ctx: Context
    sys_stack: SysStack
    sys_lo: uint
    sys_hi: uint
    current: *Task
    after: AfterSwitch
    // tick counts schedules; every TimerTick-th one looks at the global
    // queue, the oldest local task, the timers and the poller first.
    tick: u32
    // rng is the xorshift64 state of the steal order.
    rng: u64
    // extern_calls counts the extern calls tasks made on this carrier's
    // system stack (SystemStack); owner-only, read by tests after a join
    // (test "RT-F21-SystemStackEveryCarrier": which carriers ran them).
    extern_calls: u64
    // The spinner's linger (sched.mk Linger), owner-only: when this
    // carrier's last search saw another carrier's runnext occupied, and
    // when its current run of fruitless searches began (0: none).
    next_seen_at: i64
    linger_from: i64
    // pool_wait: this carrier's task parked in Blocking since its last
    // Found (the pool linger, sched.mk PoolLingerNs); owner-only.
    pool_wait: bool
    // tturn is the other carrier whose heap the last TimerTick step looked
    // at (timer.mk FireRotate); owner-only.
    tturn: u32
    // home is this carrier itself on a pinned carrier (runtime.Dedicated,
    // runtime.Thread; carrier.mk's pinned half), null on a shared one:
    // a ready keeps a task on this carrier only when the task's pin
    // equals it (ReadyOn's Shared and OnPinned, PinReady). Written before the carrier's thread starts; read by its
    // own thread only (one word of the former pad, so the record's size,
    // and with it every committed unit, is unchanged).
    home: *Carrier
    // Per-carrier cache of free ClassDefault slots (owner-only; flushed on Idle).
    slot_free: *rt.SlotHeader
    slot_count: u32
    slot_pad: u32
    // The main-thread service deadline, read and written only by carrier 0.
    main_service_due: i64
    pad0: [4]u64
    runnext: raw.Atomic[u64]
    pad1: [7]u64
    top: raw.Atomic[i64]
    pad2: [7]u64
    bottom: raw.Atomic[i64]
    pad3: [7]u64
    ring: [256]raw.Atomic[u64]
    // The carrier's timer heap (timer.mk), on lines of its own: the
    // owner arms and fires it, any carrier may disarm an entry or fire its
    // due timers (a steal), all under tlock. tnext is the earliest deadline
    // (MaxDeadline when the heap is empty, CarriersInit's value), stored at
    // the end of every tlock section that changes the heap, so a scheduler
    // skips the lock while no timer is due and an idle carrier arms for
    // the least tnext of all (ArmIdle).
    pad4: [8]u64
    tlock: raw.Atomic[u32]
    timers: *uint
    ntimers: int
    cap_timers: int
    tnext: raw.Atomic[u64]
    pad5: [8]u64
}

// CarrierAt is carrier i's record.
CarrierAt(i: int): *Carrier {
    unsafe "the carrier array holds CarrierCount records" {
        return raw.Offset((*TS()).carriers, i)
    }
}

// C0 is carrier 0, the main thread's.
C0(): *Carrier {
    return CarrierAt(0)
}

// CarrierBytes is the bytes of n carrier records, rounded up to 64 KiB.
CarrierBytes(n: int): int {
    return (n * SizeOf[Carrier]() + 65535) / 65536 * 65536
}

// CarriersInit reserves the records and commits want of them.
// Only records below want are ever read: ncarriers may briefly read
// want + 1 (StartCarrier), so every scan of the started carriers counts
// ScanCarriers.
CarriersInit(st: *TaskState, want: int): bool {
    p := sys.Reserve(CarrierBytes(MaxCarriers), 65536)
    if raw.IsNull(p) || !sys.Commit(p, CarrierBytes(want)) {
        return false
    }
    unsafe "the TaskState page, before any task or carrier thread exists" {
        (*st).carriers = raw.PtrCast[u8, Carrier](p)
        // Edge: before any carrier thread starts; ThreadStart orders these stores
        // before the new thread's first load.
        (*st).want.Store(Trunc[u32](want))
        (*st).ncarriers.Store(1)
        for i in 0..want {
            c := raw.Offset((*st).carriers, i)
            (*c).index = Trunc[u32](i)
            // Any odd seed works for xorshift64; mixing in the test seed
            // makes MK_TEST_SEED change the steal order too.
            (*c).rng = (Trunc[u64](i) + 1) *% 0x9E37_79B9_7F4A_7C15 | 1
            (*c).rng ^= (*st).jitter_seed
            if (*c).rng == 0 {
                (*c).rng = 1
            }
            // An empty heap (PublishNext's value), so a scan of the started
            // carriers' tnext (Earliest, FireOthers) reads no deadline from
            // a carrier counted before its thread starts.
            // Edge: before any carrier thread starts, as want's store.
            (*c).tnext.Store(Trunc[u64](MaxDeadline))
        }
    }
    return true
}

// CarrierWant is MK_CARRIERS (1 to 256) when set, else sys.CpuCount(),
// clamped to MaxCarriers.
CarrierWant(env: int): int {
    var n := env
    if n < 1 {
        n = sys.CpuCount()
    }
    if n < 1 {
        n = 1
    }
    if n > MaxCarriers {
        n = MaxCarriers
    }
    return n
}

/// KeptHandles is how many OS handles the runtime made on demand and keeps
/// until the process exits, besides the blocking pool's threads: on Windows
/// each started carrier's thread handle (carrier 0 is the main thread, which
/// holds none) and std.sys's timer slots (sys.TimerHandles); 0 on Linux,
/// where neither holds a descriptor. A leak test subtracts its change from
/// the process's handle count, since when a carrier starts or a timer slot
/// is made inside a measured window is timing, not a leak. The pool's
/// threads stay counted, so a pool thread per waiting call still shows. A
/// carrier is counted just before its thread starts, so a start in flight
/// reads one high. A runtime.Dedicated carrier's thread handle (Windows),
/// also kept until exit, is not counted: the program makes it by its own
/// call, so a leak test that calls Dedicated inside its window counts it
/// (a Thread's handle is closed by its Join). For leak tests.
/// O(1); allocates nothing.
export KeptHandles(): int {
    var n := sys.TimerHandles()
    if PollBackend() == 1 {
        st := TS()
        unsafe "the TaskState page Start mapped, which lives until exit" {
            // Edge: none; a count for leak tests. ncarriers may read want + 1
            // while a start past want gives its claim back, so it is
            // clamped to want.
            var c := (*st).ncarriers.Load()
            if c > (*st).want.Load() {
                c = (*st).want.Load()
            }
            if c > 0 {
                n += int(c) - 1
            }
        }
    }
    return n
}

// StartCarrier starts the next carrier, which begins spinning: the caller
// holds one count of nspinning for it (WakeOne's compare-and-swap) and
// hands it over. When every carrier is started already, or the OS refuses
// the thread, the count is given back and want is lowered so no later wake
// tries again.
StartCarrier() {
    st := TS()
    unsafe "the TaskState page Start mapped, which lives until exit" {
        // The claim is under pinlock, as a pinned carrier's (PinClaim),
        // which may lower want only while ncarriers is below it: so no
        // shared carrier is ever started at an index a pinned one holds.
        rt.Lock(&(*st).pinlock)
        // Edge: the Add claims index i; a carrier is counted (and visible to
        // AnyWork and StealRounds through ncarriers) before its thread exists, and
        // its record was committed at start, so readers see a zero deque.
        // Scans clamp to want (ScanCarriers): record want may be uncommitted.
        i := (*st).ncarriers.Add(1)
        if i >= (*st).want.Load() {
            // Edge: given back, as below; until then ncarriers reads want + 1.
            _ = (*st).ncarriers.Add(0xFFFF_FFFF)
            rt.Unlock(&(*st).pinlock)
            _ = (*st).nspinning.Add(0xFFFF_FFFF)
            return
        }
        rt.Unlock(&(*st).pinlock)
        c := CarrierAt(int(i))
        (*c).spinning = true
        // A library's carriers are joined at its stop (lib.mk).
        var start := sys.ThreadStart
        if (*st).lib {
            start = sys.ThreadStartJoin
        }
        r := start(CarrierMain, raw.PtrCast[Carrier, u8](c), CarrierStackBytes())
        if r.err == core.SysErr.None && (*st).lib {
            (*st).cthr[int(i)] = Trunc[u64](r.value)
        }
        if r.err != core.SysErr.None {
            // Edge: want is lowered before the counts are given back, so a
            // waker that reads the lowered counts also reads the new want.
            // Under pinlock and only downward: a PinClaim between this
            // carrier's claim and here may have lowered want below i
            // already, and a plain store would raise it back over a pinned
            // record.
            rt.Lock(&(*st).pinlock)
            if i < (*st).want.Load() {
                (*st).want.Store(i)
            }
            rt.Unlock(&(*st).pinlock)
            // Edge: the counts given back after want's store (above).
            _ = (*st).ncarriers.Add(0xFFFF_FFFF)
            _ = (*st).nspinning.Add(0xFFFF_FFFF)
        }
    }
}

// CarrierMain runs on carrier k's new thread, whose stack is its system
// stack (sys.ThreadInit already ran: the stack guarantee or the alternate
// signal stack).
CarrierMain(p: *u8) {
    unsafe "p is this carrier's record, initialized before the thread started" {
        c := raw.PtrCast[u8, Carrier](p)
        CarrierStack(c)
        Run(c)
        CarrierEnd(c)
    }
}

// CarrierEnd is a carrier thread's end after a library's stop (lib.mk):
// its rt entry goes before the join unmaps its stack, then the count the
// stop waits on.
CarrierEnd(c: *Carrier) {
    unsafe "c is this thread's carrier record" {
        rt.ClearSysStack(int((*c).index))
        // Edge: StopLibrary reads the count, then joins the thread.
        _ = (*TS()).nexited.Add(1)
    }
}

// CarrierStack records the calling thread's stack as c's system stack (its
// bounds, rt.SetSysStack's entry, Windows' NT_TIB words), on a carrier's
// new thread before its loop runs (CarrierMain, PinMain).
CarrierStack(c: *Carrier) {
    unsafe "c is this thread's carrier record, initialized before the thread started" {
        (lo, hi) := sys.ThreadStackBounds()
        (*c).sys_lo = lo
        (*c).sys_hi = hi
        if hi == 0 && Target.os == Os.Linux {
            // A fallback only: Linux's ThreadStackBounds answers
            // [base + 4 KiB, base + 4 KiB + stack) for a thread
            // sys.ThreadStart made, and (0, 0) only when 512
            // such threads are alive at once (its table is full: 256
            // carriers and 64 pool threads stay below that, foreign test
            // threads could not). Then: sys.ThreadStart maps SystemStackSize
            // bytes (a whole number of pages) above one guard page, with the
            // mapping's top page-aligned and its ThreadRec 64 bytes below
            // the top; this frame is a few hundred bytes below that, so the
            // top is sp rounded up to a page, the range std.sys would give.
            // Deviation: this leans on std.sys's layout; the test "RT-SysStack-Bounds"
            // checks the result either way.
            top := (rt.StackPointer() + 4095) & ~Trunc[uint](4095)
            (*c).sys_lo = top - Trunc[uint](SystemStackSize)
            (*c).sys_hi = top
        }
        rt.SetSysStack(int((*c).index), (*c).sys_lo, (*c).sys_hi)
        w := sys.StackWords()
        (*c).sys_stack.tib = [w[0], w[1], w[2]]
    }
}

// The pinned half: runtime.Dedicated and
// runtime.Thread run tasks on a carrier of their own, which never steals
// and is never stolen from.
//
// - Records. A pinned carrier is a Carrier record at an index at or past
//   want, taken from 255 down (PinClaim), so every scan of the shared
//   carriers (ScanCarriers: AnyWork, StealRounds, Earliest, FireOthers,
//   FireRotate) passes it by, and its index fits the timer word's 8 bits,
//   rt's 256 SetSysStack entries and its 256 magazines. When no index past
//   want is free and no shared carrier has been started at want - 1, want
//   is lowered by one (under pinlock, which StartCarrier's claim also
//   takes), so the shared count falls by the records pinned there; when
//   every record is in use (MK_CARRIERS=256 with all started), the claim
//   fails and Dedicated or Thread panics (code 8). Its PinRec (inbox,
//   sleep, live count, done word) is in a separate array (pins).
// - Ready. A task's pin (Task.pin) is set before its first ready and never
//   changes. ReadyOn, ReadySched and Start1 test it first: an unpinned
//   task takes main's path (ReadyPut), with a pinned readying carrier (its
//   home set) taken as none, so the task goes to the global queue; a
//   pinned one takes a cold
//   call through TaskState.pin_ready (PinReady), which keeps it on the
//   readying carrier only when that is its own, else puts it in its
//   carrier's inbox (PinPut). Every ready passes there: wakes,
//   completions a shared carrier reaps, timers, yields, spawns, slot waits.
//   A full deque of a pinned carrier overflows to its own inbox (Overflow).
// - Loop. PinRun takes runnext, its deque and its inbox, fires its own
//   heap, and never steals, polls or takes the global queue; it is not in
//   ncarriers, nidle, nspinning or waking, and never calls PollWait, so the
//   shared wake protocol (sched.mk's header) neither counts nor wakes it.
//   Its completions are reaped by the shared carriers (one poller for the
//   process), which ready its tasks into its inbox.
// - Sleep (no lost wake, all seq_cst). PinIdle stores sleeping 1, then
//   reads the inbox count; PinPut adds to the count under ilock, then
//   reads sleeping and, if it moves it from 1 to 0, posts the semaphore.
//   In S either the sleeper's read comes after the add (it does not
//   sleep) or the putter's read comes after the store (it posts, and the
//   SemWait returns or does not block). A post the sleeper did not need
//   stays as a count and costs one spurious loop. Only the carrier's own
//   thread changes its runnext, deque, heap and live count, so the inbox
//   is the only work that arrives from outside.
// - Timers. A pinned task's sleep goes on its carrier's heap (SleepForIn),
//   which only that carrier fires, sleeping with the heap's earliest
//   deadline as its timeout; nothing else inserts there (a scope's
//   deadline armed on a pinned carrier goes to carrier 0's heap,
//   HeapHere), so its timeout is never stale. A cancel may remove a timer
//   from another thread: the carrier then wakes early once.
// - Spawns. A task spawned from a pinned Io (PinIoOf) is pinned to that
//   carrier (SpawnAlloc, PinSpawn), and counted in its live count.
// - The Io. Io is {table, flags}. A pinned Io keeps the native
//   table and puts its carrier's index (1 to 255) in bits 48 to 55 and the
//   record's 8-bit generation in bits 56 to 63, the bits a Sim's or a
//   Tape's Io holds its slot in: every mask (NoCancel, IoMasked,
//   IoMaskAs) keeps them, so a masked copy of a pinned Io is pinned too,
//   and std.fs, std.time and std.x509 take them for a simulated Io's only
//   on a non-native table. PinIoOf and PinOfIo are the two functions that
//   know the layout. A stale Io of an ended Thread is refused at a spawn (panic 8) unless its
//   record was reused 256 times since (the generation's bound).
// - End. A Thread's carrier counts its root and every task pinned to it
//   (live); when the count reaches 0 it closes (Cas to PinClosed, which
//   refuses later spawns), stores done and wakes it (core.IoWakeWord, so a
//   Sim's Join is woken too), and its thread returns. Join waits on done,
//   then joins the thread on the blocking pool and frees the record. A
//   Dedicated carrier never ends.

/// PinRec is a pinned carrier's state beside its Carrier record (PinOf).
export struct PinRec {
    // The inbox: tasks readied for the carrier by other threads (PinPut),
    // a FIFO through Task.link under ilock; n counts them (stored under
    // ilock, read without it).
    ilock: raw.Atomic[u32]
    n: raw.Atomic[u32]
    head: *Task
    tail: *Task
    // sleeping is 1 from just before the carrier's last look at the inbox
    // until it is back from SemWait (PinIdle).
    sleeping: raw.Atomic[u32]
    sem_ok: u32
    sem: sys.Sem
    // live counts the carrier's tasks that have not ended, PinClosed once
    // a Thread's carrier closed (PinFind).
    live: raw.Atomic[u64]
    // done is 1 once a Thread's carrier loop ended (PinMain): Join's word.
    done: raw.Atomic[u32]
    // kind is PinKindDedicated or PinKindThread while the record is a
    // pinned carrier's, PinKindFree after; PinSpawn reads it from any
    // thread, so it is an atomic.
    kind: raw.Atomic[u32]
    era: raw.Atomic[u32]
    // thread is sys.ThreadStartJoin's value for a Thread's carrier.
    thread: i64
}

const PinKindFree: u32 = 0
const PinKindDedicated: u32 = 1
const PinKindThread: u32 = 2
const PinClosed: u64 = 0x8000_0000_0000_0000
// The pinned Io's bits on the native table (a simulated Io's slot bits
// on its own): the carrier's index in bits 48 to 55, the record's
// generation in bits 56 to 63.
const PinIoShift: uint = 48
const PinIoBits: uint = 0xFFFF_0000_0000_0000

// PinOf is carrier c's PinRec.
PinOf(c: *Carrier): *PinRec {
    unsafe "the pins array, made by the first claim, which made c a pinned carrier" {
        return raw.Offset((*TS()).pins, int((*c).index))
    }
}

// PinClaim takes a record for a pinned carrier of kind (PinKindDedicated or
// PinKindThread) and makes it ready for its thread: null when no record is
// left or the memory or semaphore cannot be had.
PinClaim(kind: u32): *Carrier {
    st := TS()
    unsafe "the TaskState page; carrier records past want, committed here" {
        rt.Lock(&(*st).pinlock)
        if raw.IsNull((*st).pins) {
            bytes := (MaxCarriers * SizeOf[PinRec]() + 65535) / 65536 * 65536
            p := sys.Reserve(bytes, 65536)
            if raw.IsNull(p) || !sys.Commit(p, bytes) {
                rt.Unlock(&(*st).pinlock)
                return raw.Null[Carrier]()
            }
            (*st).pins = raw.PtrCast[u8, PinRec](p)
            var wr: u64 = 0
            var ws: u64 = 0
            raw.Write(raw.PtrCast[u64, fn(*Carrier, *Task): u32](raw.OfMut(mut wr)), PinReady)
            raw.Write(raw.PtrCast[u64, fn(Io): *Carrier](raw.OfMut(mut ws)), PinSpawn)
            // Edge: before any pinned task or Io exists (this claim's
            // carrier is not started yet), so a load that sees a pin or a
            // pinned Io's bits reads them; a shared spawn through a Sim's
            // Io may read 0 or the word, and PinSpawn answers null for it.
            (*st).pin_ready.Store(wr)
            (*st).pin_spawn.Store(ws)
        }
        // Edge: want and ncarriers change only under pinlock (here and
        // StartCarrier's claim), except StartCarrier's lowering of want
        // on a refused thread, which only lowers it further.
        w := int((*st).want.Load())
        var i := -1
        var k := MaxCarriers - 1
        for k >= w && k >= 1 {
            if ((*st).pinused[k / 64] >> Trunc[u64](k % 64)) & 1 == 0 {
                i = k
                break
            }
            k -= 1
        }
        // Edge: under pinlock, which StartCarrier's claim holds too, so
        // ncarriers below w means record w - 1 was never claimed, and the
        // lowered want is read by every later claim (none starts w - 1).
        if i < 0 && w > 1 && int((*st).ncarriers.Load()) < w {
            // No free record past want: take want - 1, never started.
            (*st).want.Store(Trunc[u32](w - 1))
            i = w - 1
        }
        if i < 0 {
            rt.Unlock(&(*st).pinlock)
            return raw.Null[Carrier]()
        }
        (*st).pinused[i / 64] |= u64(1) << Trunc[u64](i % 64)
        rt.Unlock(&(*st).pinlock)
        // Records past the start's commit: their pages, without touching
        // the contents of a record already committed beside them (Linux's
        // Commit maps fresh pages, so its CommitStack, which only
        // changes the protection, is used there).
        size := SizeOf[Carrier]()
        lo := (i * size) / 4096 * 4096
        hi := ((i + 1) * size + 4095) / 4096 * 4096
        base := raw.PtrCast[Carrier, u8]((*st).carriers)
        ok := if Target.os == Os.Linux { sys.CommitStack(raw.Offset(base, lo), hi - lo) } else { sys.Commit(raw.Offset(base, lo), hi - lo) }
        if !ok {
            rt.Lock(&(*st).pinlock)
            (*st).pinused[i / 64] &= ~(u64(1) << Trunc[u64](i % 64))
            rt.Unlock(&(*st).pinlock)
            return raw.Null[Carrier]()
        }
        c := CarrierAt(i)
        (*c).index = Trunc[u32](i)
        d := raw.Offset((*st).pins, i)
        if (*d).sem_ok == 0 && !sys.SemInit(mut (*d).sem) {
            PinRelease(c)
            return raw.Null[Carrier]()
        }
        (*d).sem_ok = 1
        (*c).index = Trunc[u32](i)
        (*c).home = c
        (*c).spinning = false
        (*c).current = raw.Null[Task]()
        (*c).tick = 0
        (*c).rng = (Trunc[u64](i) + 1) *% 0x9E37_79B9_7F4A_7C15 | 1
        // Edge: before the thread starts and before any Io naming the
        // record exists, as the stores below.
        (*d).kind.Store(kind)
        (*d).head = raw.Null[Task]()
        (*d).tail = raw.Null[Task]()
        // Edge: before the thread starts (sys.ThreadStart orders these
        // stores before the new thread's first load) and before any Io
        // naming the record exists.
        (*d).n.Store(0)
        (*d).sleeping.Store(0)
        (*d).live.Store(0)
        (*d).done.Store(0)
        if (*c).ntimers == 0 {
            (*c).tnext.Store(Trunc[u64](MaxDeadline))
        }
        return c
    }
}

// PinRelease gives record c back (a claim that failed, or a Thread's
// carrier after Join joined its thread): its generation moves on, so a
// stale Io of it is refused (PinSpawn).
PinRelease(c: *Carrier) {
    st := TS()
    unsafe "the TaskState page; c is a pinned record no thread runs" {
        d := PinOf(c)
        // Edge: before the era's Add and the record's free bit, as the
        // era; a stale spawn that reads Free refuses.
        (*d).kind.Store(PinKindFree)
        // Edge: before the record is free (pinlock below): a spawn through
        // a stale Io that reads the new generation refuses.
        _ = (*d).era.Add(1)
        (*c).home = raw.Null[Carrier]()
        i := int((*c).index)
        rt.Lock(&(*st).pinlock)
        (*st).pinused[i / 64] &= ~(u64(1) << Trunc[u64](i % 64))
        rt.Unlock(&(*st).pinlock)
    }
}

// PinIoOf is io (native, masked or not) pinned to carrier c: a scope opened
// from it spawns its tasks on c (the header's Io).
PinIoOf(c: *Carrier, io: Io): Io {
    unsafe "c is a pinned record" {
        // Edge: the era PinRelease moved on before the record was free;
        // a claim that took it reads the new one.
        g := uint((*PinOf(c)).era.Load() & 0xFF)
        f := (core.IoFlags(io) & ~PinIoBits) | (uint((*c).index) << PinIoShift) | (g << (PinIoShift + 8))
        // io's own table word, the one NativeInit registered (IsNative
        // compares addresses, and each `&NativeTable` of a constant may
        // name a copy of its own), with the flags word replaced: an Io is
        // two words, its table's address, then its flags (core_iodecl.mk).
        var x := io
        raw.Write(raw.Offset(raw.PtrCast[Io, uint](raw.OfMut(mut x)), 1), f)
        return x
    }
}

// PinOfIo is the pinned carrier an Io names, and the generation it names:
// null for an Io that names none (not native, or no index), masked or not.
PinOfIo(io: Io): (c: *Carrier, era: u32) {
    f := core.IoFlags(io)
    i := Trunc[int]((f >> PinIoShift) & 0xFF)
    if i == 0 || !core.IsNative(io) {
        return (c: raw.Null[Carrier](), era: 0)
    }
    return (c: CarrierAt(i), era: Trunc[u32]((f >> (PinIoShift + 8)) & 0xFF))
}

// PinSpawn is the pin of a task spawned from io (SpawnAlloc, which calls it
// only when io's pin bits are not 0): null when io names no pinned
// carrier, else that carrier, its live count raised for the new task.
// A spawn through the Io of a Thread that has ended panics (code 8).
PinSpawn(io: Io): *Carrier {
    (c, era) := PinOfIo(io)
    if raw.IsNull(c) {
        return c
    }
    unsafe "c is a pinned record (the Io's index was one when it was made)" {
        d := PinOf(c)
        for {
            // Edge: the Cas raises live only while the carrier has not
            // closed (PinFind's Cas from 0 to PinClosed), so a closed
            // carrier never gets a task.
            n := (*d).live.Load()
            if n == PinClosed || (*d).kind.Load() == PinKindFree || ((*d).era.Load() & 0xFF) != era {
                raw.Fail(8, "a task spawned through the Io of a runtime.Thread that has ended")
            }
            if (*d).live.Cas(n, n + 1) {
                return c
            }
        }
    }
}

// PinReady is ReadyPut for a pinned task t (through TaskState.pin_ready,
// PinReadyOf): on its own carrier c it goes to c's runnext, the previous
// one to c's deque (only c runs them, so no carrier is woken); readied
// anywhere else, to its carrier's inbox (PinPut). Always PutPinned.
PinReady(c: *Carrier, t: *Task): u32 {
    ReadyClaim(t)
    unsafe "t is runnable and on no list; c is this thread's carrier or null" {
        p := (*t).pin
        // Plain reads: the pin before the task's first ready; home before
        // c's thread started, on c's thread.
        if !raw.IsNull(c) && raw.Addr(p) == raw.Addr((*c).home) {
            // Edge: as ReadyPut's swap; on a pinned carrier only its
            // own thread takes runnext.
            old := (*c).runnext.Swap(AddrOf(t))
            if old != 0 {
                PushLocal(c, TaskAt(old))
            }
            return PutPinned
        }
        PinPut(p, t)
    }
    return PutPinned
}

// PinPut queues t, pinned to p and just made runnable, on p's inbox from
// any thread (PinReady), and wakes p if it
// sleeps (the header's sleep).
PinPut(p: *Carrier, t: *Task) {
    PinSplice(p, t, t, 1)
    d := PinOf(p)
    unsafe "p's PinRec" {
        // Edge: the putter's half of the sleep pair (header): the count's
        // Add (in PinSplice) precedes this load in S.
        if (*d).sleeping.Load() == 1 && (*d).sleeping.Cas(1, 0) {
            sys.SemPost(&(*d).sem, 1)
        }
    }
}

// PinSplice appends the list head..tail of n tasks to c's inbox (PinPut,
// and Overflow on c's own thread).
PinSplice(c: *Carrier, head: *Task, tail: *Task, n: u32) {
    d := PinOf(c)
    unsafe "c's PinRec; the tasks are runnable and on no other list" {
        (*tail).link = raw.Null[Task]()
        rt.Lock(&(*d).ilock)
        if raw.IsNull((*d).tail) {
            (*d).head = head
        } else {
            (*(*d).tail).link = head
        }
        (*d).tail = tail
        // Edge: raised after the tasks are linked, under ilock; PinTake
        // reads it, then takes ilock; PinIdle reads it after its store of
        // sleeping (the header's sleep).
        _ = (*d).n.Add(n)
        rt.Unlock(&(*d).ilock)
    }
}

// PinTake takes c's whole inbox, on c's thread: the first task is returned,
// the rest pushed on c's deque in order.
PinTake(c: *Carrier, d: *PinRec): *Task {
    unsafe "c's own PinRec, on c's thread" {
        // Edge: a nonzero count was raised after its tasks were linked
        // under ilock (PinSplice); the take below holds ilock.
        if (*d).n.Load() == 0 {
            return raw.Null[Task]()
        }
        rt.Lock(&(*d).ilock)
        first := (*d).head
        (*d).head = raw.Null[Task]()
        (*d).tail = raw.Null[Task]()
        // Edge: under ilock, as PinSplice's Add.
        (*d).n.Store(0)
        rt.Unlock(&(*d).ilock)
        if raw.IsNull(first) {
            return first
        }
        var x := (*first).link
        (*first).link = raw.Null[Task]()
        for !raw.IsNull(x) {
            next := (*x).link
            (*x).link = raw.Null[Task]()
            PushLocal(c, x)
            x = next
        }
        return first
    }
}

// PinFind is a pinned carrier's FindRunnable: every TimerTick schedules
// its timers, its inbox and the oldest task of its deque first (as the
// shared fairness step), then runnext, the deque, the inbox and its
// timers, else its sleep. Null when a Thread's carrier has no task left
// and has closed.
PinFind(c: *Carrier, d: *PinRec): *Task {
    for {
        var due := false
        unsafe "c's own fields, on its system stack" {
            (*c).tick += 1
            if (*c).tick >= TimerTick {
                (*c).tick = 0
                due = true
            }
        }
        if due {
            _ = FireTimersOn(c)
            g := PinTake(c, d)
            if !raw.IsNull(g) {
                return g
            }
            o := StealOne(c)
            if !raw.IsNull(o) {
                return o
            }
        }
        var t := TakeNext(c)
        if raw.IsNull(t) {
            t = Take(c)
        }
        if raw.IsNull(t) {
            t = PinTake(c, d)
        }
        if !raw.IsNull(t) {
            return t
        }
        if FireTimersOn(c) {
            continue
        }
        unsafe "c's own PinRec" {
            // Edge: the close (header): only this thread lowers live, and a
            // spawn's Cas from a nonzero count, or from 0 before this Cas,
            // keeps the carrier open.
            if (*d).kind.Load() == PinKindThread && (*d).live.Load() == 0 && (*d).live.Cas(0, PinClosed) {
                return raw.Null[Task]()
            }
        }
        PinIdle(c, d)
    }
}

// PinIdle sleeps until a PinPut, or until c's earliest timer (the header's
// sleep); it returns at once when a timer is due or work arrived.
PinIdle(c: *Carrier, d: *PinRec) {
    var wait: i64 = -1
    due := NextOf(c)
    if due != MaxDeadline {
        wait = due - sys.Now()
        if wait <= 0 {
            return
        }
    }
    unsafe "c's own PinRec" {
        // Edge: the sleeper's half of the sleep pair (header): this store
        // precedes the count's load in S.
        (*d).sleeping.Store(1)
        if (*d).n.Load() != 0 {
            (*d).sleeping.Store(0)
            return
        }
        _ = sys.SemWait(&(*d).sem, wait)
        // Edge: none of its own; a putter that still reads 1 posts once
        // more, which the next sleep consumes.
        (*d).sleeping.Store(0)
    }
}

// PinAfter is RunAfter for a pinned carrier: a sleeper's new earliest
// deadline owes the shared carriers nothing (its own loop arms for it),
// and a task's end lowers the live count.
PinAfter(c: *Carrier, d: *PinRec) {
    unsafe "the carrier's after-action, set by the task that switched out" {
        a := (*c).after
        (*c).after = AfterSwitch(kind: AfterNone, word: raw.Null[raw.Atomic[u32]](), task: raw.Null[Task]())
        if a.kind == AfterUnlock || a.kind == AfterUnlockKick {
            // Edge: release, as RunAfter's AfterUnlock.
            (*a.word).StoreOrd(0, 2)
        } else if a.kind == AfterHandshake {
            if !(*a.word).Cas(Pending, Parked) {
                ReadySched(c, a.task)
            }
        } else if a.kind == AfterYield {
            ReadyOn(raw.Null[Carrier](), a.task)
        } else if a.kind == AfterFree {
            h := HeaderOf(a.task)
            if Target.os == Os.Windows {
                top := raw.Addr(h) + Trunc[uint](rt.HeaderBytes)
                (*h).committed_pages = Trunc[u16]((top - Trunc[uint]((*a.task).ctx[2])) / 4096)
            }
            class := (*h).class
            rt.FreeSlot(h)
            SlotFreed(c, class)
            // Edge: only this thread lowers the count; PinFind's close
            // reads it on this thread.
            _ = (*d).live.Add(0xFFFF_FFFF_FFFF_FFFF)
        }
    }
}

// PinRun is a pinned carrier's loop on its system stack (Run's body with
// PinFind and PinAfter); it returns once a Thread's carrier has closed.
PinRun(c: *Carrier) {
    d := PinOf(c)
    for {
        t := PinFind(c, d)
        if raw.IsNull(t) {
            return
        }
        unsafe "t is runnable, its context saved, and now owned by c" {
            h := HeaderOf(t)
            (*c).current = t
            (*t).carrier = c
            // Invariant: one resume per park, as in Run.
            // Edge: as Run's.
            if (*t).state.Swap(TaskRunning) != TaskRunnable {
                rt.Fatal("scheduler: switched to a task that is not runnable")
            }
            (*h).carrier = Trunc[u16]((*c).index)
            Switch(raw.Of(&(*c).sys_ctx), raw.Of(&(*t).ctx))
            (*h).carrier = rt.NoCarrier
            (*c).current = raw.Null[Task]()
        }
        PinAfter(c, d)
    }
}

// PinMain is a pinned carrier's thread: its loop, and for a Thread's
// carrier the done word once the loop has ended.
PinMain(p: *u8) {
    unsafe "p is this carrier's record, claimed and made ready before the thread started" {
        c := raw.PtrCast[u8, Carrier](p)
        CarrierStack(c)
        PinRun(c)
        d := PinOf(c)
        // Edge: seq_cst; Join's wait compares the word with 0 under the
        // bucket lock (or the Sim's), and the wake follows this store.
        (*d).done.Store(1)
        core.IoWakeWord(raw.Addr(raw.Of(&(*d).done)), true)
    }
}

/// PinDedicated is runtime.Dedicated: io pinned to a new carrier of its
/// own, whose thread lives until the process exits. A Sim's or another
/// non-native Io is returned as it is; io's mask, if any, is kept. Panics
/// (code 8) when no carrier record is left, or when the OS refuses the
/// thread.
/// O(1) system calls besides the record's first commit; reserves an 8 MiB
/// system stack for the thread.
export PinDedicated(io: Io): Io {
    if !core.IsNative(io) {
        return io
    }
    c := PinClaim(PinKindDedicated)
    if raw.IsNull(c) {
        raw.Fail(8, "runtime.Dedicated: no carrier record is left")
    }
    var ok := false
    unsafe "c is the claimed record, made ready for its thread" {
        ok = sys.ThreadStart(PinMain, raw.PtrCast[Carrier, u8](c), SystemStackSize).err == core.SysErr.None
    }
    if !ok {
        PinRelease(c)
        raw.Fail(8, "runtime.Dedicated: the OS refused the carrier's thread")
    }
    return PinIoOf(c, io)
}

// PinRootEntry runs a Thread's root task: f, from its block, with the
// task's pinned Io, then the task's end.
PinRootEntry(p: *u8) {
    unsafe "p is this task's record; its block holds f" {
        t := raw.PtrCast[u8, Task](p)
        f := raw.Read(raw.PtrCast[u8, fn(Io)]((*t).block))
        f((*t).io)
        SwitchOut(t, AfterFree, raw.Null[raw.Atomic[u32]]())
    }
    for {
        sys.ThreadYield()
    }
}

/// PinThread is runtime.Thread's start: a new pinned carrier whose root
/// task runs f with a native Io pinned to it, on a Large slot as Main
/// is. The answer is the carrier's index for PinDone and PinJoin.
/// Panics (code 8) when no carrier record is left or the OS refuses the
/// thread. O(1) system calls besides the record's first commit; takes one
/// Large slot, waiting for one on a task as a spawn does, and reserves an
/// 8 MiB system stack.
export PinThread(f: fn(Io)): int {
    c := PinClaim(PinKindThread)
    if raw.IsNull(c) {
        raw.Fail(8, "runtime.Thread: no carrier record is left")
    }
    need := TopBytes + FrameReserve
    var h := raw.Null[rt.SlotHeader]()
    if raw.IsNull(Current()) {
        h = rt.TakeSlot(rt.ClassLarge)
        if !raw.IsNull(h) && !rt.GrowTop(h, need) {
            rt.FreeSlot(h)
            h = raw.Null[rt.SlotHeader]()
        }
        if raw.IsNull(h) {
            PinRelease(c)
            raw.Fail(8, "runtime.Thread: no task slot is free")
        }
    } else {
        h = NewSlot(rt.ClassLarge, need)
    }
    t := Lay(h, rt.NextTaskId())
    d := PinOf(c)
    unsafe "the fresh root task, and c's record, before its thread starts" {
        (*t).pin = c
        (*t).io = PinIoOf(c, NativeIo())
        at := (raw.Addr(t) - 16) & ~Trunc[uint](15)
        raw.Write(raw.FromAddr[fn(Io)](at), f)
        (*t).block = raw.FromAddr[u8](at)
        Context1(t, h, at, PinRootEntry)
        // Edge: before the thread starts, which orders these stores before
        // its first load; the root is the carrier's first task.
        // An Add, not a Store: a stale Io whose 8-bit era wrapped to this
        // record's (PinSpawn) may have raised live since PinClaim's
        // Store(0), and its task must stay counted, or the carrier could
        // close under it.
        _ = (*d).live.Add(1)
        (*t).state.Store(TaskRunnable)
        (*c).runnext.Store(AddrOf(t))
        r := sys.ThreadStartJoin(PinMain, raw.PtrCast[Carrier, u8](c), SystemStackSize)
        if r.err != core.SysErr.None {
            // Edge: none; no thread ever read the record.
            (*c).runnext.Store(0)
            rt.FreeSlot(h)
            SlotFreed(Here(), rt.ClassLarge)
            PinRelease(c)
            raw.Fail(8, "runtime.Thread: the OS refused the carrier's thread")
        }
        (*d).thread = r.value
        return int((*c).index)
    }
}

/// PinDone is the address of Thread carrier i's done word: 1 once its
/// loop has ended (runtime.Joined.Join waits on it). O(1).
export PinDone(i: int): uint {
    unsafe "i is a Thread's record, not yet joined" {
        return raw.Addr(raw.Of(&(*PinOf(CarrierAt(i))).done))
    }
}

// PinJoinJob joins the thread whose handle op.pos holds, on a pool thread.
PinJoinJob(op: *sys.Op) {
    unsafe "op is PinJoin's, live until this job returns" {
        sys.ThreadJoin((*op).pos)
    }
}

/// PinJoin joins Thread carrier i's thread, whose done word is 1, and frees
/// its record. On a task the join runs on the blocking pool, not
/// cancellable (the thread is past its loop, so it is a short wait), so no
/// carrier waits on it; off a task, on the calling thread. O(1) besides
/// the join; allocates nothing.
export PinJoin(i: int) {
    PinJoinWith(i, PinJoinJob)
}

// PinJoinWith is PinJoin with the job that joins the thread (PinJoinJob;
// a test passes one that looks at rt's table first).
PinJoinWith(i: int, job: fn(*sys.Op)) {
    c := CarrierAt(i)
    d := PinOf(c)
    // The thread is past its loop (only done's wake is left), and the join
    // unmaps its system stack: its rt entry goes first, so no mapping made
    // at those addresses after the unmap (by any thread, while the join
    // runs on the pool) is taken for it by SysStackOf or InSysGuard.
    // A fault inside that wake (core.IoWakeWord, which may call a Foreign
    // wake entry) after this clear loses only its system-stack
    // classification (no in_c answer on Windows, Access rather than Stack
    // for a Linux guard hit); it stays Mk's.
    rt.ClearSysStack(i)
    unsafe "i is a Thread's record whose loop has ended" {
        var op := sys.NewOp(0, 0)
        op.pos = (*d).thread
        if raw.IsNull(Current()) {
            job(raw.OfMut(mut op))
        } else {
            _ = BlockingWhole(job, raw.OfMut(mut op), false)
        }
    }
    PinRelease(c)
}
