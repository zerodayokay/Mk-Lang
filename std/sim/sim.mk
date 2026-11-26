// Why: code written against Io should be testable for crashes, faults and
// task interleavings with every run reproducible from one number, the
// seed, as FoundationDB's simulator and TigerBeetle's VOPR do.
//
// What: Sim, a third Io beside the native table (std.task) and the direct
// one (std.rt), with New, Io, Arena, Advance, Run, CrashAfterRandomOps,
// Restart, FailNext and Free; the Op and Fault enums; the global registry
// of Sims and tapes (SlotWords) and the World a Sim owns.
//
// How: every source of nondeterminism (random stream, clock, disk, network,
// crash point, faults) sits behind one seeded generator. An Io names a
// registry slot and a token, never Sim memory, so a stale Io is detected.
//
// Safety after Free. An Io holds no loan, so a copy of a Sim's Io
// may outlive the Sim. Its table is the static SimTable, never Sim memory,
// and its flags name a slot of the global registry Sims and a token:
// flags = slot << 48 | depth << 32 | token << 1 | nocancel. Every entry locks the slot and
// compares the slot's token with the Io's before it touches the world; Free
// and Restart change the token under the lock, so an Io of a freed Sim or
// of an earlier run answers Canceled and never reads freed memory. Tokens
// come from one global counter and are never reused (31 bits; bits 32 to
// 47 of the flags are the NoCancel mask depth).

import std.core
import std.raw
import std.rt
import std.time

/// The kinds of operation FailNext can fail. Read and Write cover both
/// files and sockets. Sync is a file's data sync, Open the open of a file
/// or a directory, and Accept and Connect are a socket's.
export enum Op {
    /// A read of a file or a socket.
    Read
    /// A write to a file or a socket.
    Write
    /// A file's data sync.
    Sync
    /// The open of a file or a directory.
    Open
    /// A socket's accept.
    Accept
    /// A socket's connect.
    Connect
}

/// The faults FailNext injects, each the system error of the same name.
/// A module reports the fault as its error set's member of that name, else
/// as Os(code: 0): fs.Error has NoSpace and Denied, and net.Error has
/// Reset and Refused, so FailNext(Op.Accept, Fault.Denied) reaches
/// Accept's caller as net.Os(code: 0).
export enum Fault {
    /// The device is full.
    NoSpace
    /// Permission denied.
    Denied
    /// The peer reset the connection.
    Reset
    /// The peer refused the connection.
    Refused
}

/// What the simulated network does: datagrams are dropped and duplicated
/// at parts per million, each drawn from the seed; latency is uniform in
/// lo..hi; rate is bytes per second through each receiving port (0 for
/// unlimited); stream_buffer bounds each stream end's unread bytes (0 for
/// unbounded), so a send waits while the peer does not read. Start from
/// DefaultNetFaults.
export struct NetFaults {
    /// Datagram drop probability in parts per million (0..1_000_000).
    export drop: u32
    /// Datagram duplication probability in parts per million (0..1_000_000).
    export duplicate: u32
    /// Minimum simulated delivery latency.
    export latency_lo: time.Duration
    /// Maximum simulated delivery latency.
    export latency_hi: time.Duration
    /// Bandwidth cap in bytes per second per receiving UDP port (0 unlimited).
    export rate: i64
    /// Maximum unread bytes per stream end (0 unbounded).
    export stream_buffer: int
    sealed: u8
}

/// Today's model: no loss, no duplication, 20 to 500 us, unlimited,
/// unbounded.
export const DefaultNetFaults: NetFaults = NetFaults(
    drop: 0,
    duplicate: 0,
    latency_lo: core.DurationOf(LatLo),
    latency_hi: core.DurationOf(LatHi),
    rate: 0,
    stream_buffer: 0,
    sealed: 0,
)

/// A simulated world behind an Io: a seeded scheduler, a simulated clock,
/// an in-memory disk and network, and one terminal. Every choice is drawn
/// from one seed, so one seed gives one run. A Sim is linear: end it with
/// Free. It is a view that borrows the arena it was made from, and it also
/// uses the process heap.
export linear mut view struct Sim {
    slot: int
    w: *World
}

// ---------------------------------------------------------------------------
// The registry.

const SlotCount: int = 64
// 31 bits, bits 1 to 31 of the flags; bits 32 to 47 hold the
// NoCancel mask depth (core.MaskDepthShift).
const TokenMask: u64 = 0x7FFF_FFFF

// The words of one slot. token and world change only under the lock; the
// lock word and Registry.next are used through raw.Atomic (an atomic is
// not Plain, so the 64 slots are one repeated plain record and each atomic
// use casts the word's address, which has the atomic's layout). tape:
// world is a Recorder's or Replayer's Tape, not a World (see RunKey).
struct SlotWords {
    lock: u32
    token: u64
    world: u64
    tape: bool
}

struct Registry {
    next: u64
    s: [64]SlotWords
}

var Sims: Registry = Registry(next: 1, s: [SlotWords(lock: 0, token: 0, world: 0, tape: false); 64])

// LockWord is slot i's lock word as an atomic.
LockWord(i: int): &raw.Atomic[u32] {
    unsafe "a u32 in static memory has raw.Atomic[u32]'s layout, and Sims is never freed" {
        return &*raw.PtrCast[u32, raw.Atomic[u32]](raw.Of(&Sims.s[i].lock))
    }
}

// LockSlot takes slot i's word through rt.Lock: a compare and
// swap, then test-and-test-and-set with exponential backoff up to 64
// loads, then a thread yield, so a holder preempted on a shared machine
// costs its waiters a yield each, not a spin for a whole quantum. A Sim's
// entries never wait while they hold it (no entry blocks for real), so a
// holder releases it within one entry's work. The Cas and the Store are
// seq_cst (raw.Atomic), which orders every access to the slot and its
// world between them.
LockSlot(i: int) {
    rt.Lock(LockWord(i))
}

UnlockSlot(i: int) {
    rt.Unlock(LockWord(i))
}

// SlotToken, SlotWorld and SetSlot read and write slot i's words; the
// caller holds the lock, or owns the Sim (only its own mut methods change
// its token).
SlotToken(i: int): u64 {
    unsafe "Sims is static; the token changes only under the slot's lock" {
        return Sims.s[i].token
    }
}

SlotWorld(i: int): u64 {
    unsafe "Sims is static; the caller holds the slot's lock" {
        return Sims.s[i].world
    }
}

SetSlot(i: int, t: u64, w: u64) {
    unsafe "Sims is static; the caller holds the slot's lock" {
        raw.Write(raw.Of(&Sims.s[i].token), t)
        raw.Write(raw.Of(&Sims.s[i].world), w)
    }
}

// SlotTape and SetSlotTape read and write whether slot i holds a tape;
// the caller holds the lock.
SlotTape(i: int): bool {
    unsafe "Sims is static; the caller holds the slot's lock" {
        return Sims.s[i].tape
    }
}

SetSlotTape(i: int, tape: bool) {
    unsafe "Sims is static; the caller holds the slot's lock" {
        raw.Write(raw.Of(&Sims.s[i].tape), tape)
    }
}

// NewToken is a token no earlier run of any Sim had: 1 to 2^31 - 1. The
// Add needs no edge, only atomicity: each caller gets a distinct value.
NewToken(): u64 {
    var t: u64 = 0
    unsafe "a u64 in static memory has raw.Atomic[u64]'s layout" {
        t = ((*raw.PtrCast[u64, raw.Atomic[u64]](raw.Of(&Sims.next))).Add(1) +% 1) & TokenMask
    }
    if t == 0 {
        raw.Fail(8, "std.sim: token space exhausted")
    }
    return t
}

// ClaimSlot takes a free registry slot for w, with token t; -1 when all 64
// are in use.
ClaimSlot(w: *World, t: u64): int {
    for i in 0..SlotCount {
        LockSlot(i)
        if SlotWorld(i) == 0 {
            SetSlot(i, t, u64(raw.Addr(w)))
            SetSlotTape(i, false)
            UnlockSlot(i)
            return i
        }
        UnlockSlot(i)
    }
    return -1
}

// IoFor is the Io of slot i's run with token t.
IoFor(i: int, t: u64): Io {
    return core.MakeIo(raw.Of(&SimTable), (Cast[uint](i) << 48) | (uint(t) << 1))
}

// ---------------------------------------------------------------------------
// The world.

// The clock starts at one second, so a program that treats an Instant of 0
// as unset still works; it is in nanoseconds and never goes backwards.
const ClockStart: i64 = 1_000_000_000

// The crash point's span: half the draws are uniform over the
// first CrashSpan counted operations (short runs crash often), the other
// half uniform over CrashSpan << j operations with j uniform in
// 1..CrashDoublings, so every point of a run of up to 8 million
// operations can be crashed into, the density falling off as 1 / k (a
// log-uniform span: no run length is favoured, FoundationDB's random kill
// points at any time of the run).
const CrashSpan: u64 = 128
const CrashDoublings: u64 = 16

linear struct World {
    rng: [4]u64
    clock: i64
    epoch: u32
    ops: u64
    crash: i64
    dead: bool
    fault: [6]u32
    next_port: int
    seq: u64
    nodes: Vec[Node]
    handles: Vec[Hand]
    ends: Vec[End]
    ports: Vec[Port]
    dgrams: Vec[Dgram]
    slot: int
    token: u64
    closing: uint
    // The scheduler (sched.mk): the run's tasks, the baton's holder (a
    // task id, 0 for none) and its task record's address, whether a task
    // was ever born or entered, the next demoted priority, a change
    // counter, the spawners outside the baton that owe a note,
    // birth ranks, the priority seed, PCT's change points and the order of
    // blocking.
    tasks: Vec[TRec]
    holder: u64
    run: uint
    hooked: bool
    low: i64
    events: u64
    pend: Vec[uint]
    ranks: u64
    sseed: u64
    cps: [2]u64
    cpend: u64
    steps: u64
    handoff: bool
    bseq: u64
    // Native waiters outside the scheduler (main's waits, parked on
    // MainWords[slot]): their count, their records (task address and
    // deadline), the deadlocks so far, and whether one waits for the
    // baton to be free (Decide then bumps MainWords[slot]).
    mwait: u32
    mains: Vec[MainRec]
    dlk: u64
    mturn: bool
    // The native tasks (main) waiting in a Join or a limit gate of
    // a scope that tasks of the run belong to (JoinRec, sched.mk).
    joins: Vec[JoinRec]
    // The armed scope deadlines of the run, on its clock (SimDeadline).
    dls: Vec[SimDl]
    // The run's simulated children and pseudo-terminals (proc.mk).
    kids: Vec[Kid]
    ptys: Vec[SimPty]
    // The Sim's one terminal (term.mk): its input queue, an End no
    // handle sends on, and the mode in force (0 as opened, core.IoTermRaw
    // or core.IoTermNoEcho).
    term: End
    tmode: u8
    net_faults: NetFaults
    blackholes: Vec[int]
    user: Arena
}

// Mix is splitmix64's step (Steele, Lea and Flood 2014), the seeding
// xoshiro256**'s authors prescribe.
Mix(x: u64): u64 {
    var z := x
    z = (z ^ (z >> 30)) *% 0xBF58476D1CE4E5B9
    z = (z ^ (z >> 27)) *% 0x94D049BB133111EB
    return z ^ (z >> 31)
}

const Gold: u64 = 0x9E3779B97F4A7C15

// SeedRng fills the xoshiro256** state from seed through splitmix64; the
// state is never all zero (splitmix64 is a bijection of distinct inputs).
SeedRng(seed: u64): [4]u64 {
    return [Mix(seed +% Gold), Mix(seed +% Gold *% 2), Mix(seed +% Gold *% 3), Mix(seed +% Gold *% 4)]
}

// Next is xoshiro256** (Blackman and Vigna 2018): 256 bits of state,
// period 2^256 - 1, passes BigCrush.
World.Next(mut self): u64 {
    s := mut self.rng
    result := raw.RotL64(s[1] *% 5, 7) *% 9
    t := s[1] << 17
    s[2] ^= s[0]
    s[3] ^= s[1]
    s[1] ^= s[2]
    s[0] ^= s[3]
    s[2] ^= t
    s[3] = raw.RotL64(s[3], 45)
    return result
}

// Below is uniform in 0..n (n > 0), by Lemire's multiply-shift with
// rejection (Lemire 2019, "Fast Random Integer Generation in an Interval").
World.Below(mut self, n: u64): u64 {
    var m := raw.Mul128(self.Next(), n)
    if m.lo < n {
        thresh := (0 -% n) % n
        for m.lo < thresh {
            m = raw.Mul128(self.Next(), n)
        }
    }
    return m.hi
}

// Latency is a seeded delay in lo..hi nanoseconds.
World.Latency(mut self, lo: i64, hi: i64): i64 {
    return lo + Cast[i64](self.Below(Cast[u64](hi - lo)))
}

World.NextSeq(mut self): u64 {
    self.seq += 1
    return self.seq
}

// ---------------------------------------------------------------------------
// Results.

Ok(v: i64): core.IoResult {
    return core.IoResult(value: v, err: core.SysErr.None, os: 0)
}

Fail(e: core.SysErr): core.IoResult {
    return core.IoResult(value: 0, err: e, os: 0)
}

FailOs(code: i64): core.IoResult {
    return core.IoResult(value: 0, err: core.SysErr.Other, os: code)
}

// FaultErr is the SysErr a Fault stands for.
FaultErr(f: Fault): core.SysErr {
    return match f {
        .NoSpace { core.SysErr.NoSpace }
        .Denied { core.SysErr.Denied }
        .Reset { core.SysErr.Reset }
        .Refused { core.SysErr.Refused }
    }
}

OpIndex(op: Op): int {
    return match op {
        .Read { 0 }
        .Write { 1 }
        .Sync { 2 }
        .Open { 3 }
        .Accept { 4 }
        .Connect { 5 }
    }
}

// Fault kinds by OpIndex, for the entries; -1 is an entry no fault names.
const FRead: int = 0
const FWrite: int = 1
const FSync: int = 2
const FOpen: int = 3
const FAccept: int = 4
const FConnect: int = 5
const FNone: int = -1

const ErrCodes: [4]core.SysErr = [core.SysErr.NoSpace, core.SysErr.Denied, core.SysErr.Reset, core.SysErr.Refused]

// Gate runs before every counted entry: a crashed run answers Canceled;
// a crash point reached ends the run here; an armed fault for kind fk is
// taken and answered. err None means the entry goes ahead.
World.Gate(mut self, fk: int): core.IoResult {
    if self.dead {
        return Fail(core.SysErr.Canceled)
    }
    if self.crash == 0 {
        self.dead = true
        self.crash = -1
        return Fail(core.SysErr.Canceled)
    }
    if self.crash > 0 {
        self.crash -= 1
    }
    self.ops += 1
    // a change point inside an operation hands the baton on
    // before the operation runs (BeginH parks the holder).
    if ChangePoint(self) {
        self.handoff = true
    }
    if fk >= 0 && self.fault[fk] != 0 {
        e := ErrCodes[int(self.fault[fk]) - 1]
        self.fault[fk] = 0
        return Fail(e)
    }
    return Ok(0)
}

// ---------------------------------------------------------------------------
// Entering from an Io.

// Entry is an entry's hold on a world: the slot is locked exactly when ok.
struct Entry {
    slot: int
    w: *World
    ok: bool
}

// Enter decodes io, locks its slot and checks its token.
Enter(io: Io): Entry {
    f := core.IoFlags(io)
    i := Cast[int](f >> 48)
    t := u64((f >> 1) & uint(TokenMask))
    if i >= SlotCount || t == 0 {
        return Entry(slot: 0, w: raw.Null[World](), ok: false)
    }
    LockSlot(i)
    if SlotToken(i) != t {
        UnlockSlot(i)
        return Entry(slot: 0, w: raw.Null[World](), ok: false)
    }
    var w := raw.Null[World]()
    unsafe "the slot's world word holds the address of its live World while the token matches" {
        w = raw.FromAddr[World](uint(SlotWorld(i)))
    }
    return Entry(slot: i, w: w, ok: true)
}

Leave(e: &Entry) {
    if e.ok {
        UnlockSlot(e.slot)
    }
}

// WorldOf is the world an ok entry holds, for the entry's own call.
WorldOf(e: &Entry): mut World {
    unsafe "an ok entry holds its slot's lock, and the slot's world lives until Free takes that lock" {
        return mut *e.w
    }
}

// ---------------------------------------------------------------------------
// The API.

/// A simulated world whose every choice is drawn from seed, with no files
/// and no sockets. On every seed its clock starts at one second and its
/// wall clock at 2026-01-01T00:00:00Z. Fails with OutOfMemory when the
/// heap is exhausted or 64 Sims, Recorders and Replayers are already live.
/// O(1). It allocates the world, its tables and its files' pages on the
/// process heap, outside any budget on a's allocator; the world's arena is
/// a sub-arena of a. So the Sim borrows a: a is not reset or freed, and
/// its allocator not released, while the Sim lives.
export Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim from a {
    var user := try a.Sub()
    p := rt.HeapAlloc(SizeOf[World](), AlignOf[World]())
    if raw.IsNull(p) {
        user.Free()
        fail OutOfMemory
    }
    var w := raw.Null[World]()
    unsafe "p is a fresh block of World's size" {
        w = raw.PtrCast[u8, World](p)
        raw.Write(w, World(rng: SeedRng(seed), clock: ClockStart, epoch: 1, ops: 0, crash: -1, dead: false,
            fault: [0; 6], next_port: 49152, seq: 0, nodes: Vec[Node].Empty(), handles: Vec[Hand].Empty(),
            ends: Vec[End].Empty(), ports: Vec[Port].Empty(), dgrams: Vec[Dgram].Empty(), slot: 0, token: 0,
            closing: 0, tasks: Vec[TRec].Empty(), holder: 0, run: 0, hooked: false, low: -1, events: 0,
            pend: Vec[uint].Empty(), ranks: 0, sseed: 0, cps: [0; 2], cpend: 0, steps: 0, handoff: false, bseq: 0, mwait: 0, mains: Vec[MainRec].Empty(), dlk: 0, mturn: false,
            joins: Vec[JoinRec].Empty(), dls: Vec[SimDl].Empty(), kids: Vec[Kid].Empty(), ptys: Vec[SimPty].Empty(), term: FreshEnd(0), tmode: 0,
            net_faults: DefaultNetFaults, blackholes: Vec[int].Empty(), user: user))
        SchedInit(mut *w, seed)
    }
    t := NewToken()
    slot := ClaimSlot(w, t)
    if slot < 0 {
        unsafe "w was written above and is not registered" {
            u := raw.Read(raw.OfMut(mut (*w).user))
            u.Free()
            rt.HeapFree(p, SizeOf[World](), AlignOf[World]())
        }
        fail OutOfMemory
    }
    unsafe "no Io of this Sim exists yet, so nothing else reads the world" {
        (*w).slot = slot
        (*w).token = t
    }
    // A third Io's note reaches this Sim through the fan.
    FanSet()
    // A wake with no Io reaches this Sim's parked tasks.
    ListenWake()
    return Sim(slot: slot, w: w)
}

/// The Io of the current run, to pass to the code under test. Like any Io
/// it holds no loan. Every copy answers Canceled once the run crashes,
/// ends with Restart, or the Sim is freed. A dead run is not a scope
/// cancellation, so the cancellation check of a NoCancel copy (a sleep of
/// 0, as a free Lock or a Send with room makes) goes on; a scope cancelled
/// with Cancel() and opened from that copy still cancels it. O(1);
/// allocates nothing.
export Sim.Io(&self): Io {
    return IoFor(self.slot, SlotToken(self.slot))
}

/// The world's own arena, for the program under test; it lives until
/// Free. O(1); allocates nothing.
export Sim.Arena(&self): &Arena from self {
    unsafe "the world lives until Free(self), and a shared loan of self keeps Free away" {
        return &(*self.w).user
    }
}

/// Moves the simulated clock forward by d; a d of zero or less changes
/// nothing. When the run has no task, the scope deadlines the clock
/// passes fire at once; otherwise they fire at the next scheduling choice.
/// O(armed deadlines); allocates nothing.
export Sim.Advance(mut self, d: time.Duration) {
    n := d.Nanos()
    LockSlot(self.slot)
    w := self.World()
    if n > 0 {
        w.clock = ClockAfter(&*w, n)
        MainAloneFire(w)
    }
    UnlockSlot(self.slot)
}

/// Delivers every message in flight and moves the clock to the last
/// arrival. When the run has no task, the scope deadlines the clock passes
/// fire at once; otherwise they fire at the next scheduling choice. O(n)
/// in the connections, datagrams and armed deadlines; allocates nothing.
export Sim.Run(mut self) {
    LockSlot(self.slot)
    w := self.World()
    last := LastArrival(&*w)
    if last > w.clock {
        w.clock = last
        MainAloneFire(w)
    }
    UnlockSlot(self.slot)
}

/// Arms a crash at a seeded point: after k more counted operations, the
/// next one and every later one answer Canceled. k is drawn log-uniform
/// over the first 2^23, so short runs crash often and every point of a
/// long run can be reached. Restart then starts the next run with what was
/// synced. O(1); allocates nothing.
export Sim.CrashAfterRandomOps(mut self) {
    LockSlot(self.slot)
    w := self.World()
    span := if w.Below(2) == 1 { CrashSpan << (1 + w.Below(CrashDoublings)) } else { CrashSpan }
    w.crash = Cast[i64](w.Below(span))
    UnlockSlot(self.slot)
}

/// Ends the run as a power cut does, and returns the next run's Io over
/// the same disk. Every file keeps only what was synced, an unlinked file
/// is gone, every handle and connection of the run is dead, and armed
/// faults and the crash point are cleared. The clock and the random stream
/// go on. Every Io of the earlier run answers Canceled, as after Free (see
/// Sim.Io). O(n) in the files, their unsynced pages and the connections;
/// allocates nothing, and frees what the run leaves.
export Sim.Restart(mut self): Io {
    LockSlot(self.slot)
    w := self.World()
    RevertDisk(w)
    DropRun(w)
    w.dead = false
    w.crash = -1
    w.fault = [0; 6]
    w.epoch += 1
    t := NewToken()
    w.token = t
    SetSlot(self.slot, t, SlotWorld(self.slot))
    // The run's tasks find the token changed and run unscheduled.
    SchedReset(w)
    Bump(self.slot, w.ranks)
    BumpMain(self.slot)
    UnlockSlot(self.slot)
    return IoFor(self.slot, t)
}

/// Makes the next operation of kind op fail with f, changing nothing; a
/// later FailNext of the same kind replaces it. Only a file's or a
/// socket's operation takes the fault: a read or write of the simulated
/// terminal, a pipe, a Pty or a child's stream leaves it armed. O(1);
/// allocates nothing.
export Sim.FailNext(mut self, op: Op, f: Fault) {
    LockSlot(self.slot)
    w := self.World()
    var code: u32 = 1
    for k in 0..4 {
        if ErrCodes[k] == FaultErr(f) {
            code = Trunc[u32](k + 1)
        }
    }
    w.fault[OpIndex(op)] = code
    UnlockSlot(self.slot)
}

/// Applies `f` to every datagram and stream send from now on. O(1); allocates
/// nothing.
export Sim.SetNetFaults(mut self, f: NetFaults) {
    LockSlot(self.slot)
    w := self.World()
    w.net_faults = f
    UnlockSlot(self.slot)
}

/// Drops every datagram sent to `port` while `on` is true. O(blackholed
/// ports); grows the blackhole list on the process heap if needed.
export Sim.Blackhole(mut self, port: int, on: bool) {
    LockSlot(self.slot)
    w := self.World()
    SetBlackhole(w, port, on)
    UnlockSlot(self.slot)
}

/// Moves the port a UDP socket sends from, as a NAT rebinding does: the peer
/// sees datagrams from a new address and replies reach it there. No-op when
/// `port` is not bound. O(ports); allocates nothing.
export Sim.Rebind(mut self, port: int) {
    LockSlot(self.slot)
    w := self.World()
    NetRebind(w, port)
    UnlockSlot(self.slot)
}

/// Frees the world and its arena. Every Io of this Sim answers Canceled
/// from then on (see Sim.Io). Handles the program still owns stay valid,
/// so their Close or Abandon still frees them. O(n) in the world's size;
/// allocates nothing.
export Sim.Free(self) {
    Sim(slot, w) := self
    LockSlot(slot)
    SetSlot(slot, 0, 0)
    var used: u64 = 0
    unsafe "this Sim owns w, alive until the frees below" {
        used = (*w).ranks
    }
    // Parked tasks wake, find the slot empty and run unscheduled; native
    // waiters answer Canceled.
    Bump(slot, used)
    BumpMain(slot)
    UnlockSlot(slot)
    unsafe "the slot no longer names w, so no entry reaches it; this Sim owned it" {
        FreeWorld(mut *w)
        u := raw.Read(raw.OfMut(mut (*w).user))
        u.Free()
        rt.HeapFree(raw.PtrCast[World, u8](w), SizeOf[World](), AlignOf[World]())
    }
    // A wake with no Io skips the Sims once none is live (wake.mk).
    UnlistenWake()
}

// World is the world of a Sim whose slot the caller has locked.
Sim.World(&self): mut World {
    unsafe "the caller holds the slot's lock, and the Sim owns its world" {
        return mut *self.w
    }
}
