// Why: spawning copies a task's arguments into a fresh slot and may have to
// wait for one; ids, argument blocks and slot layout must be right.
// What: task ids (RT-Ids-First, rt.NextTaskId), the argument block
// (RT-F7-ThunkIoByPointer, RT-F6-SpawnBigBlock), waiting for a slot
// (RT-F20-SlotExhaustWaits), RT-Layout-SlotTop, RT-F15-RandomLarge and RT-Pin-*.
// How: tasks report back through mut arguments and atomics; the slot cap
// comes from MK_TEST_SLOTS in dev builds.

import std.core
import std.raw
import std.rt
import std.sys

// IdOf is the running task's id.
IdOf(): u64 {
    unsafe "MustCurrent is the running task, whose slot header lives while it runs" {
        return (*HeaderOf(MustCurrent())).id
    }
}

// IdInto stores the running task's id in out.
IdInto(io: Io, out: mut u64) {
    *out = IdOf()
}

test "RT-Ids-First Main is task 1 and the first spawned tasks are 2 and 3" (sys: &System) {
    // The probe calls the block from Main before any spawn of the process.
    Assert(IdOf() == 1)
    var a: u64 = 0
    var b: u64 = 0
    scope s := sys.io.Scope() {
        s.go IdInto(sys.io, mut a)
    }
    scope s := sys.io.Scope() {
        s.go IdInto(sys.io, mut b)
    }
    Assert(a == 2)
    Assert(b == 3)
    Assert(IdOf() == 1)
}

// F7Pair is a 24-byte argument (passed by memory on SysV and Win64 alike).
struct F7Pair {
    x: i64
    y: i64
    z: i64
}

// F7Args takes nine arguments of several classes, more than either ABI's
// argument registers, and writes what it saw.
F7Args(io: Io, a: int, b: int, p: F7Pair, f: f64, t: str, c: u8, d: i32, out: mut i64) {
    var sum: i64 = Trunc[i64](a) * 1_000 + Trunc[i64](b)
    sum += p.x + p.y * 10 + p.z * 100
    if f == 2.5 {
        sum += 7
    }
    if t == "seven" {
        sum += 70
    }
    sum += i64(c) + i64(d)
    *out = sum
}

// F7Set takes a mut argument: the task writes the parent's local.
F7Set(io: Io, x: mut int) {
    *x = 42
}

// F7Flags stores IoFlags of the io it was given, and whether a 1 ns sleep
// through it returns Canceled.
F7Flags(io: Io, flags: mut uint, canceled: mut bool) {
    *flags = core.IoFlags(io)
    io.Sleep(core.DurationOf(1)) else {
        *canceled = true
        return
    }
}

test "RT-F7-ThunkIoByPointer every argument reaches the task" (sys: &System) {
    var x := 0
    scope s := sys.io.Scope() {
        s.go F7Set(sys.io, mut x)
    }
    Assert(x == 42)
    var got: i64 = 0
    scope s := sys.io.Scope() {
        s.go F7Args(sys.io, 3, 4, F7Pair(x: 1, y: 2, z: 3), 2.5, "seven", 200, -9, mut got)
    }
    Assert(got == 3_004 + 321 + 7 + 70 + 200 - 9)
    // An Io reaches the task as the parent passed it (the thunk does not read
    // its own &t.io): a NoCancel Io stays NoCancel (bit 0 set). A
    // scope opened from it is a new boundary, so its own
    // Cancel cancels the task's sleep through the NoCancel Io too; a plain
    // Io sees its scope's cancel as ever.
    var f1: uint = 0
    var c1 := false
    var f2: uint = 0
    var c2 := false
    nc := sys.io.NoCancel()
    scope s := nc.Scope() {
        s.Cancel()
        s.go F7Flags(nc, mut f1, mut c1)
    }
    scope s := sys.io.Scope() {
        s.Cancel()
        s.go F7Flags(sys.io, mut f2, mut c2)
    }
    Assert((f1 & 1) == 1 && c1)
    Assert((f2 & 1) == 0 && c2)
}

// F6Check counts the bytes of big that differ from the pattern.
F6Check16(io: Io, big: [16384]u8, bad: mut int) {
    for i in 0..16384 {
        if big[i] != Trunc[u8](i * 7 + 3) {
            *bad += 1
        }
    }
}

F6Check200(io: Io, big: [204800]u8, bad: mut int) {
    for i in 0..204800 {
        if big[i] != Trunc[u8](i * 13 + 1) {
            *bad += 1
        }
    }
}

test "RT-F6-SpawnBigBlock 16 KiB and 200 KiB argument blocks arrive whole" (sys: &System) {
    // A fresh Windows slot commits 8 KiB; SpawnAlloc grows the committed top
    // past the block before the parent writes it. Two rounds:
    // fresh slots, then reused ones.
    for _ in 0..2 {
        var big: [16384]u8 = [0; 16384]
        for i in 0..16384 {
            big[i] = Trunc[u8](i * 7 + 3)
        }
        var bad := 0
        scope s := sys.io.Scope() {
            s.go F6Check16(sys.io, big, mut bad)
        }
        Assert(bad == 0)
    }
    var huge: [204800]u8 = [0; 204800]
    for i in 0..204800 {
        huge[i] = Trunc[u8](i * 13 + 1)
    }
    for _ in 0..2 {
        var bad := 0
        var bad2 := 0
        scope s := sys.io.Scope() {
            s.go F6Check200(sys.io, huge, mut bad)
            s.go F6Check200(sys.io, huge, mut bad2)
        }
        Assert(bad == 0 && bad2 == 0)
    }
}

// F20Nap counts itself live, sleeps 1 ms, and records the highest live
// count it saw.
F20Nap(io: Io, live: &raw.Atomic[u32], high: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    n := live.Add(1) + 1
    for {
        h := high.Load()
        if n <= h || high.Cas(h, n) {
            break
        }
    }
    io.Sleep(core.DurationOf(1_000_000)) else {
        _ = live.Add(0xFFFF_FFFF)
        return
    }
    _ = live.Add(0xFFFF_FFFF)
    _ = done.Add(1)
}

test "RT-F20-SlotExhaustWaits 5,000 tasks in 1,024 slots all finish [env MK_TEST_SLOTS=1024]" (sys: &System) {
    // With MK_TEST_SLOTS=1024 the Default region has 1,024 usable slots
    // in a dev build; s.go waits for a slot instead of failing.
    // A release build ignores the setting, and the
    // block still passes there (more slots); the high mark is checked only
    // when the cap is in force.
    live := raw.Atomic[u32].New(0)
    high := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope s := sys.io.Scope() {
        for _ in 0..5_000 {
            s.go F20Nap(sys.io, &live, &high, &done)
        }
    }
    Assert(done.Load() == 5_000)
    Assert(live.Load() == 0)
    capped := rt.UsableSlots(rt.ClassDefault) == 1024
    Assert(capped == rt.DevBuild())
    if capped {
        Assert(high.Load() <= 1_024)
        Assert(high.Load() >= 2)
    }
}

test "RT-Layout-SlotTop the header, the task record and the scope record fit their bytes" {
    // SlotHeader is 128 bytes at the top, Task in the 384 below it; a
    // ScopeRec is the 128-byte head of std.core's Scope.
    Assert(SizeOf[rt.SlotHeader]() <= rt.HeaderBytes)
    Assert(SizeOf[Task]() <= TaskOffset)
    Assert(rt.HeaderBytes + TaskOffset == TopBytes)
    Assert(int(TaskGuarantee) + 4096 == rt.WinGuardBytes)
    Assert(SizeOf[ScopeRec]() <= ErrOffset)
    Assert(SizeOf[ScopeRec]() <= SizeOf[Scope]())
}

// F15Zero counts the aligned 4 KiB blocks of b that are all zero.
F15Zero(b: []u8): int {
    var zero := 0
    var i := 0
    for i + 4096 <= b.Len() {
        var any: u8 = 0
        for j in i..i + 4096 {
            any |= b[j]
        }
        if any == 0 {
            zero += 1
        }
        i += 4096
    }
    return zero
}

test "RT-F15-RandomLarge 64 MiB of OS randomness in one call" (sys: &System) {
    // Larger than one getrandom call returns (32 MiB) and one
    // ProcessPrng-sized chunk: std.sys loops, and no block stays zero. A
    // 4 KiB block of real randomness is all zero with probability 2^-32768.
    n := 64 << 20
    p := rt.HeapAlloc(n, 4096)
    Assert(!raw.IsNull(p))
    unsafe "p is a fresh block of n bytes" {
        raw.Fill(p, 0, n)
        b := raw.SliceMut(p, n)
        sys.io.Random(b) else {
            Assert(false)
        }
        Assert(F15Zero(raw.Slice(p, n)) == 0)
        sys.io.Random(mut b[..0]) else {
            Assert(false)
        }
    }
    rt.HeapFree(p, n, 4096)
}

// JoinRoot is a Thread's f that returns at once.
JoinRoot(io: Io) {
}

// A joined Thread's carrier stack is unmapped by the join, so its entry in
// rt's system-stack table goes with it (rt.ClearSysStack, PinJoin): no
// later mapping at those addresses is taken for that stack.
test "RT-Pin-JoinClears a joined Thread's system stack leaves rt's table" {
    i := PinThread(JoinRoot)
    w := PinDone(i)
    unsafe "w is the done word of record i, live until PinJoin below" {
        for (*raw.FromAddr[raw.Atomic[u32]](w)).Load() == 0 {
            _ = WaitWord(raw.FromAddr[u32](w), 0, false)
        }
        lo := (*CarrierAt(i)).sys_lo
        Assert(lo != 0 && rt.SysStackOf(lo + 64) == i)
        PinJoin(i)
        Assert(rt.SysStackOf(lo + 64) < 0)
    }
}

// JoinSeen is what JoinLook saw: rt.SysStackOf's answer for JoinLo, plus 1,
// when the thread's join began.
var JoinLo: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var JoinSeen: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// JoinLook is PinJoinJob after a look at rt's table.
JoinLook(op: *sys.Op) {
    r := rt.SysStackOf(Trunc[uint](JoinLo.Load()))
    JoinSeen.Store(if r < 0 { 0 } else { Trunc[u64](r) + 1 })
    PinJoinJob(op)
}

// The join unmaps the stack, so the entry must be gone before it starts:
// a mapping at those addresses made while the join runs (another thread's)
// is never taken for the joined carrier's stack.
test "RT-Pin-JoinClearsFirst a Thread's system-stack entry is gone before its thread's join starts" {
    i := PinThread(JoinRoot)
    w := PinDone(i)
    unsafe "w is the done word of record i, live until PinJoinWith below" {
        for (*raw.FromAddr[raw.Atomic[u32]](w)).Load() == 0 {
            _ = WaitWord(raw.FromAddr[u32](w), 0, false)
        }
        lo := (*CarrierAt(i)).sys_lo
        Assert(lo != 0 && rt.SysStackOf(lo + 64) == i)
        JoinLo.Store(u64(lo + 64))
        JoinSeen.Store(99)
        PinJoinWith(i, JoinLook)
        // On the pool (this is a task): seen -1, so 0.
        Assert(JoinSeen.Load() == 0)
        Assert(rt.SysStackOf(lo + 64) < 0)
    }
}
