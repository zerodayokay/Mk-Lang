// Why: after a spike, the free task slots beyond rt.TrimKeep and the empty
// heap segments beyond 8 must give their pages back and still be usable.
// What: the RT14-Trim-* blocks (Slots, Heap, Race, RaceGate, Refused) over
// TrimSpare in sched.mk.
// How: Linux reads the resident set (/proc/self/statm, IdleMem) and the
// mapping count (/proc/self/maps), which macOS has no file for; every target
// reads the runtime's own counts (rt.SlotCounts, rt.HeapEmptySegments).

import std.core
import std.raw
import std.rt

const TrimTasks: int = 1_000
const TrimFrame: int = 65_536

// TrimTouch fills a 64 KiB frame (an OS call writes every byte, so no
// optimization drops the frame), then parks on gate until it is not 0.
TrimTouch(io: Io, gate: &raw.Atomic[u32], arrived: &raw.Atomic[u32], sum: &raw.Atomic[u32]) {
    var buf: [65536]u8 = [0; 65536]
    _ = NativeRandom(io, mut buf[..])
    _ = arrived.Add(1)
    for gate.Load() == 0 {
        unsafe "gate outlives every task of the scope" {
            _ = WaitWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(gate)), 0, false)
        }
    }
    _ = sum.Add(u32(buf[TrimFrame / 2]) + 1)
}

// TrimSpike runs TrimTasks tasks of TrimTouch at once and returns the
// resident bytes while all of them are parked (-1 off Linux).
TrimSpike(sys: &System): int {
    gate := raw.Atomic[u32].New(0)
    arrived := raw.Atomic[u32].New(0)
    sum := raw.Atomic[u32].New(0)
    var peak := -1
    scope s := sys.io.Scope() {
        for _ in 0..TrimTasks {
            s.go TrimTouch(sys.io, &gate, &arrived, &sum)
        }
        for int(arrived.Load()) < TrimTasks {
            sys.io.Sleep(core.DurationOf(1_000_000)) else {
                return peak
            }
        }
        peak = IdleMem()
        gate.Store(1)
        unsafe "gate outlives every task of the scope" {
            WakeWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(&gate)), true)
        }
    }
    // Every task added at least 1.
    Assert(int(sum.Load()) >= TrimTasks)
    return peak
}

// TrimSettled waits (sleeping, so carriers go idle and trim) until the
// Default class holds at most TrimKeep free slots and every spike slot is
// free or cold again; false after 4 s.
TrimSettled(sys: &System): bool {
    for _ in 0..400 {
        (free, cold, cold_pages) := rt.SlotCounts(rt.ClassDefault)
        _ = cold_pages
        if free <= rt.TrimKeep && free + cold >= TrimTasks {
            return true
        }
        sys.io.Sleep(core.DurationOf(10_000_000)) else {
            return false
        }
    }
    return false
}

test "RT14-Trim-Slots after 1,000 tasks of 64 KiB, idle carriers give back the slots beyond 256, which are used again" (sys: &System) {
    m0 := IdleMem()
    maps0 := IdleScan("/proc/self/maps", 1)
    m1 := TrimSpike(sys)
    maps1 := IdleScan("/proc/self/maps", 1)
    Assert(TrimSettled(sys))
    m2 := IdleMem()
    maps2 := IdleScan("/proc/self/maps", 1)
    (free, cold, cold_pages) := rt.SlotCounts(rt.ClassDefault)
    Assert(cold >= TrimTasks - rt.TrimKeep)
    if Target.os == Os.Windows {
        // Every cold slot commits its top page alone (and the guard pages
        // below it, which the count leaves out), as a fresh one does.
        Assert(cold_pages == cold)
    } else if Target.os == Os.Linux {
        // 1,000 frames of 64 KiB were resident at the peak; after the trim
        // only the kept slots' are, at most 256 x 68 KiB (17.4 MB) of the
        // 65.5 MB.
        Assert(m1 - m0 > TrimTasks * TrimFrame / 2)
        Assert(m2 - m0 < (m1 - m0) / 2)
        // The re-mapped range merges with its slot's mapping again: no new
        // mapping per trimmed slot (Linux caps a process's mappings).
        Assert(maps2 <= maps1 + 16)
        FNote("trim resident bytes at the peak, above the start", i64(m1 - m0))
        FNote("trim resident bytes after the trim, above the start", i64(m2 - m0))
        FNote("trim maps at start, peak, after", i64(maps0 * 1_000_000 + maps1 * 1_000 + maps2))
    }
    FNote("trim free and cold slots", i64(free * 10_000 + cold))
    // The cold slots run tasks again: on Windows each grows from one page
    // through its guard page, as a fresh slot does.
    _ = TrimSpike(sys)
    Assert(TrimSettled(sys))
}

test "RT14-Trim-Heap after 60 blocks of 1 MiB are freed, idle carriers decommit the empty segments beyond 8, which are used again" (sys: &System) {
    var blocks: [60]*u8 = [raw.Null[u8](); 60]
    m0 := IdleMem()
    for round in 0..2 {
        for i in 0..60 {
            p := rt.HeapAlloc(1 << 20, 16)
            Assert(!raw.IsNull(p))
            unsafe "a fresh block of 1 MiB" {
                raw.Fill(p, Trunc[u8](i + round + 1), 1 << 20)
            }
            blocks[i] = p
        }
        m1 := IdleMem()
        for i in 0..60 {
            unsafe "the block was written above" {
                Assert(raw.Read(raw.Offset(blocks[i], (1 << 20) - 1)) == Trunc[u8](i + round + 1))
            }
            rt.HeapFree(blocks[i], 1 << 20, 16)
        }
        // Three spans of 16 pages fit in a segment's 63, so the blocks
        // filled 20 segments, now empty (some may hold other blocks).
        e0 := rt.HeapEmptySegments()
        Assert(e0 >= 12)
        var settled := false
        for _ in 0..400 {
            if rt.HeapEmptySegments() <= 8 {
                settled = true
                break
            }
            sys.io.Sleep(core.DurationOf(10_000_000)) else {
                return
            }
        }
        Assert(settled)
        e1 := rt.HeapEmptySegments()
        m2 := IdleMem()
        if Target.os == Os.Linux {
            FNote("trim heap resident KiB at the peak and after the trim, above the start", i64((m1 - m0) / 1024 * 1_000_000 + (m2 - m0) / 1024))
            FNote("trim heap empty segments before and after", i64(e0 * 1_000 + e1))
            Assert(m1 - m0 > 50 << 20)
            // Each trimmed segment held three 1 MiB blocks, all written.
            Assert(m1 - m2 >= (e0 - e1) * (3 << 20) * 3 / 4)
        }
    }
}

// RaceSlots is the Default slots the race blocks allow (their [env
// MK_TEST_SLOTS=296]): rt.TrimKeep + 40, so a burst that uses every slot
// leaves 40 to trim and a spawn beyond them must wait for a slot.
const RaceSlots: int = 296

// RacePark counts itself arrived and parks until gate is not 0.
RacePark(gate: &raw.Atomic[u32], arrived: &raw.Atomic[u32]) {
    _ = arrived.Add(1)
    for gate.Load() == 0 {
        unsafe "gate outlives every task of the scope" {
            _ = WaitWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(gate)), 0, false)
        }
    }
}

// RaceHold runs n tasks at once (each in its own slot: all park until all
// have arrived), then lets them end. With arm, TrimGate is armed just
// before they end, so the first trim of their slots holds its batch.
RaceHold(sys: &System, n: int, arm: bool) {
    gate := raw.Atomic[u32].New(0)
    arrived := raw.Atomic[u32].New(0)
    scope s := sys.io.Scope() {
        for _ in 0..n {
            s.go RacePark(&gate, &arrived)
        }
        for int(arrived.Load()) < n {
            sys.io.Sleep(core.DurationOf(1_000_000)) else {
                return
            }
        }
        if arm {
            rt.TrimGateSet(1)
        }
        gate.Store(1)
        unsafe "gate outlives every task of the scope" {
            WakeWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(&gate)), true)
        }
    }
}

// RaceSettled waits (sleeping, so carriers go idle and trim) until the
// Default class has at most TrimKeep free slots and every slot ever handed
// out (rt.SlotsMade) free or cold, none lost; false after 4 s. least is the
// fewest slots the block made: MK_TEST_SLOTS caps a dev build at it, and a
// release build (no cap) may make more.
RaceSettled(sys: &System, least: int): bool {
    for _ in 0..400 {
        (free, cold, cold_pages) := rt.SlotCounts(rt.ClassDefault)
        _ = cold_pages
        made := rt.SlotsMade(rt.ClassDefault)
        if rt.DevBuild() {
            Assert(made == least)
        }
        if free <= rt.TrimKeep && made >= least && free + cold == made {
            return true
        }
        sys.io.Sleep(core.DurationOf(10_000_000)) else {
            return false
        }
    }
    return false
}

// RaceShort is a short task: some sleep a little, so bursts overlap.
RaceShort(io: Io, k: int, done: &raw.Atomic[u32]) {
    if k % 3 == 0 {
        io.Sleep(core.DurationOf(i64(k % 5) * 100_000)) else {
            return
        }
    }
    _ = done.Add(1)
}

test "RT14-Trim-Race bursts beyond the slots race the idle trims at every carrier count, every spawn runs and no slot is lost [env MK_TEST_SLOTS=296]" (sys: &System) {
    // Every index used once, so the slot total is known.
    RaceHold(sys, RaceSlots, false)
    done := raw.Atomic[u32].New(0)
    var total := 0
    for round in 0..40 {
        // A burst of 40 more tasks than slots: spawns wait for slots that
        // tasks end and trims take and put back, while idle carriers trim
        // the free slots beyond 256 between bursts.
        scope s := sys.io.Scope() {
            for k in 0..RaceSlots + 40 {
                s.go RaceShort(sys.io, k + round, &done)
            }
        }
        total += RaceSlots + 40
        // A pause of 0 to 3 ms, so carriers go idle (and trim) before the
        // next burst starts, at varying points of the trims.
        if round % 4 != 0 {
            sys.io.Sleep(core.DurationOf(i64(round % 4) * 1_000_000)) else {
                return
            }
        }
    }
    Assert(int(done.Load()) == total)
    // Every slot ever handed out is free or cold again (none lost).
    Assert(RaceSettled(sys, RaceSlots))
}

// RaceRelease waits until Main's spawns stop advancing (it waits for a
// slot), at most 1 s, then releases TrimGate; blocked reports that a spawn
// was seen waiting while the batch was held. It then parks on gate with
// the others, so its own end (a task end's wake) cannot be what wakes the
// waiting spawn: only the trim's SlotFreed can.
RaceRelease(io: Io, arrived: &raw.Atomic[u32], want: int, blocked: &raw.Atomic[u32], gate: &raw.Atomic[u32]) {
    var last := -1
    for _ in 0..1000 {
        a := int(arrived.Load())
        if a == last && a < want {
            blocked.Store(1)
            break
        }
        last = a
        io.Sleep(core.DurationOf(1_000_000)) else {
            break
        }
    }
    rt.TrimGateSet(3)
    for gate.Load() == 0 {
        unsafe "gate outlives every task of the scope" {
            _ = WaitWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(gate)), 0, false)
        }
    }
}

test "RT14-Trim-RaceGate a spawn that finds no slot while a trim holds its batch off both stacks is woken when the batch goes cold [env MK_TEST_SLOTS=296]" (sys: &System) {
    var want: u32 = 1
    unsafe "the TaskState page Start mapped, which lives until exit" {
        want = (*TS()).want.Load()
    }
    // One carrier would hold itself at the gate: no race to make there.
    RaceHold(sys, RaceSlots, want >= 2)
    if want < 2 {
        Assert(RaceSettled(sys, RaceSlots))
        return
    }
    var held := false
    for _ in 0..2000 {
        g := rt.TrimGateGet()
        if g == 2 {
            held = true
            break
        }
        sys.io.Sleep(core.DurationOf(1_000_000)) else {
            return
        }
    }
    Assert(held)
    // The batch is off both stacks: every other slot is free or cold. The
    // releaser and RaceSlots - 1 parked tasks need every slot, so a spawn
    // waits for the held batch; only the trim's SlotFreed per slot wakes it
    // (no task ends while all park).
    gate := raw.Atomic[u32].New(0)
    arrived := raw.Atomic[u32].New(0)
    blocked := raw.Atomic[u32].New(0)
    scope s := sys.io.Scope() {
        s.go RaceRelease(sys.io, &arrived, RaceSlots - 1, &blocked, &gate)
        for _ in 0..RaceSlots - 1 {
            s.go RacePark(&gate, &arrived)
        }
        for int(arrived.Load()) < RaceSlots - 1 {
            sys.io.Sleep(core.DurationOf(1_000_000)) else {
                return
            }
        }
        gate.Store(1)
        unsafe "gate outlives every task of the scope" {
            WakeWord(raw.PtrCast[raw.Atomic[u32], u32](raw.Of(&gate)), true)
        }
    }
    FNote("trim race gate: a spawn waited while the batch was held (1 yes)", i64(blocked.Load()))
    rt.TrimGateSet(0)
    Assert(RaceSettled(sys, RaceSlots))
}

test "RT14-Trim-Refused a trimmed slot whose read-write step the kernel refuses stays usable: it is made ready before its next task [env MK_TEST_SLOTS=296]" (sys: &System) {
    // Linux only refuses there (Windows' trim has no step that can fail
    // for want of memory); the block still runs the bursts on Windows.
    if Target.os == Os.Linux {
        // Every trim of the 40 slots beyond TrimKeep is refused.
        rt.TrimRefuse(40)
    }
    RaceHold(sys, RaceSlots, false)
    Assert(RaceSettled(sys, RaceSlots))
    // Every slot runs a task again, the refused ones included: with a
    // bounded retry list the slots beyond it were lost and this burst
    // waited for good.
    RaceHold(sys, RaceSlots, false)
    rt.TrimRefuse(0)
    Assert(RaceSettled(sys, RaceSlots))
}
