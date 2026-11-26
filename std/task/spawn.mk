// Why: spawning a task must be cheap enough for one task per connection or
// per small job: no heap allocation, and a full slot class must make the
// spawner wait rather than fail.
//
// What: SpawnAlloc and Spawn (the two halves of `s.go f(...)`), TaskEntry,
// the slot allocation NewSlot and SlotFreed with their wait words SlotGen
// and SlotWaiters, Lay, Context1, and the constants FrameReserve, TopBytes
// and TaskGuarantee.
//
// How: a task's header, record, error buffer and argument block live at the
// top of its own slot, so a spawn allocates nothing from the heap. A spawn
// that finds no free slot of its class parks on the class's generation
// word; each task end bumps it and wakes one waiter.

import std.core
import std.raw
import std.rt
import std.sys

// FrameReserve is the part of the child's stack the parent writes below the
// argument block: InitContext's initial frame (mk_task_start's return
// address and register words, under 128 bytes), with margin. The child's own
// frames grow through the guard page as on any Windows thread stack, since
// mk_switch installs the slot's NT_TIB words, so nothing more is
// committed for them in advance. Reserving 16 KiB for the initial frame and
// the thunk's first frames would commit
// 20 KiB plus the guard for every fresh Default slot, two more system calls
// each (TestRtSched_IdleWindows bounds an idle task's commit). Linux slots
// are read-write whole, so this is only the Windows commit.
const FrameReserve: int = 1024
const TopBytes: int = 512

// TaskGuarantee is a task's GuaranteedStackBytes word (Windows): the
// stack the overflow exception may use. The kernel keeps it, plus one page,
// as PAGE_GUARD pages below the lowest page a task has touched, so a task
// whose stack grew once commits it in full: it was most of the 47.6 KB of
// commit per held connection of echo.mk (31.2 KB with 16 KiB; both measured
// on one development machine). The overflow report and the exit after it
// need between 8 and 12 KiB on this path: with 8 KiB stack_task.mk prints
// its report and then dies with an access violation, to a file and to a
// console; with 12 KiB and 16 KiB stack_main, stack_task, stack_frame and
// spawn_huge give their .err text and status, dev and release. So a task
// keeps 16 KiB, which the report fits in; carrier threads keep
// 32 KiB (sys.ThreadInit).
const TaskGuarantee: u64 = 16384

Round16(n: int): int {
    return (n + 15) / 16 * 16
}

// SlotGen is the generation a spawn of class waits on when no slot of
// that class is free: a task end of that class bumps it.
SlotGen(class: u8): *raw.Atomic[u32] {
    unsafe "the TaskState page Start mapped, which lives until exit" {
        return if class == rt.ClassLarge { raw.Of(&(*TS()).slot_gen_large) } else { raw.Of(&(*TS()).slot_gen) }
    }
}

// SlotWaiters counts the spawns of class waiting on SlotGen(class).
SlotWaiters(class: u8): *raw.Atomic[u32] {
    unsafe "the TaskState page Start mapped, which lives until exit" {
        return if class == rt.ClassLarge { raw.Of(&(*TS()).slot_waiters_large) } else { raw.Of(&(*TS()).slot_waiters) }
    }
}

// NewSlot takes a slot of class, committing need bytes of its top; it
// waits (not cancellably) until a task of that class ends when none can be
// had.
//
// No lost wake (all seq_cst): the waiter loads g, fails TakeSlot,
// raises the waiter count, and WaitWord parks only if the generation is
// still g, compared under the bucket lock. A task end frees its slot, bumps
// the generation, then reads the count (SlotFreed). If its bump comes
// before the waiter's compare, the waiter does not park; else the count
// was raised before the compare, so the end reads it as nonzero and wakes
// one waiter, whose bucket entry it finds (the compare and the enqueue are
// one section under the bucket lock, which WakeWord takes). One wake per
// freed slot is enough: a woken spawn that loses the slot to another spawn
// waits again, and the slot it lost is in use, not free. The classes have
// their own words, so a freed Large slot never takes the wake of a Default
// waiter. Tests: RT-Slot-WakeOne, RT-F20-SlotExhaustWaits.
NewSlot(class: u8, need: int): *rt.SlotHeader {
    if class == rt.ClassDefault {
        cur := Current()
        if !raw.IsNull(cur) {
            unsafe "cur is the running task; its carrier's slot_free is owner-only" {
                c := (*cur).carrier
                // Restrict per-carrier slot cache to shared carriers (pinned carriers
                // never enter Idle/TrimSpare and would leak cached slots).
                if !raw.IsNull(c) && raw.IsNull((*c).home) {
                    var h := (*c).slot_free
                    if !raw.IsNull(h) {
                        (*c).slot_free = (*h).next_free
                        (*c).slot_count -= 1
                        (*h).next_free = raw.Null[rt.SlotHeader]()
                        if Target.os != Os.Windows || need <= int((*h).committed_pages) * 4096 || rt.GrowTop(h, need) {
                            return h
                        }
                        rt.FreeSlot(h)
                        SlotFreed(c, class)
                        if (*c).slot_count > 0 {
                            FlushSlots(c, int((*c).slot_count))
                        }
                        // Edge: refill only when the shared demand and trim
                        // checks leave the Default slot cache eligible.
                    } else if (*TS()).slot_cache && (*TS()).slot_waiters.Load() == 0 && rt.TrimGateGet() == 0 {
                        var got := 0
                        h = rt.TakeSlotBatch(class, 64, mut got)
                        if !raw.IsNull(h) {
                            (*c).slot_free = (*h).next_free
                            (*c).slot_count = Trunc[u32](got - 1)
                            (*h).next_free = raw.Null[rt.SlotHeader]()
                            if Target.os != Os.Windows || need <= int((*h).committed_pages) * 4096 || rt.GrowTop(h, need) {
                                return h
                            }
                            rt.FreeSlot(h)
                            SlotFreed(c, class)
                            if (*c).slot_count > 0 {
                                FlushSlots(c, int((*c).slot_count))
                            }
                        }
                    }
                }
            }
        }
    }
    word := SlotGen(class)
    waiters := SlotWaiters(class)
    unsafe "the TaskState page Start mapped, which lives until exit; the slot words are atomics" {
        for {
            // Edge: g is read before TakeSlot, so a slot freed after this
            // load moves the generation past g and the wait below returns.
            g := (*word).Load()
            h := rt.TakeSlot(class)
            if !raw.IsNull(h) {
                if rt.GrowTop(h, need) {
                    return h
                }
                rt.FreeSlot(h)
            }
            // Edge: the count is raised before re-trying TakeSlot and before
            // WaitWord's compare of the generation, closing the Dekker window
            // with carriers caching freed slots when slot_waiters == 0.
            _ = (*waiters).Add(1)
            h2 := rt.TakeSlot(class)
            if !raw.IsNull(h2) {
                if rt.GrowTop(h2, need) {
                    _ = (*waiters).Add(0xFFFF_FFFF)
                    return h2
                }
                rt.FreeSlot(h2)
            }
            _ = WaitWord(raw.PtrCast[raw.Atomic[u32], u32](word), g, false)
            // Edge: withdraw this attempt's demand after the generation wait.
            _ = (*waiters).Add(0xFFFF_FFFF)
            // Edge: none; a test counter.
            _ = (*TS()).slot_waits.Add(1)
        }
    }
}

// SlotFreed is the task end's half of NewSlot's protocol, after FreeSlot:
// bump the class's generation and wake one waiting spawn, taking the bucket
// lock only when one waits.
SlotFreed(c: *Carrier, class: u8) {
    SlotFreedBatch(c, class, 1)
}

// SlotFreedBatch wakes waiting spawners after returning n slots to class: one
// waiter when n == 1, or all waiters when a batch (n > 1) is returned so no
// waiter stays parked while returned slots sit free in the region.
SlotFreedBatch(c: *Carrier, class: u8, n: int) {
    word := SlotGen(class)
    unsafe "the TaskState page Start mapped, which lives until exit; the slot words are atomics" {
        // Edge: the bump follows FreeSlot/FreeSlotBatch (so a waiter that sees
        // it finds the slot) and precedes the count load (NewSlot's argument).
        _ = (*word).Add(1)
        if (*SlotWaiters(class)).Load() != 0 {
            WakeWordOn(c, raw.PtrCast[raw.Atomic[u32], u32](word), n > 1)
        }
    }
}

// LayFast writes the header and only the live control fields of a spawned Task,
// skipping ctx (written by Context1), home/inner/io/block/thunk (written by
// SpawnAlloc/Spawn), wait/timer (written before parking) and note/note2 (gated
// by flags == 0).
LayFast(h: *rt.SlotHeader, id: u64): *Task {
    t := TaskOf(h)
    unsafe "h's slot is reserved for this task" {
        (*h).state = 2
        (*h).in_c = 0
        (*h).id = id
        (*h).trace_n = 0
        (*h).trace_dropped = 0
        (*h).carrier = rt.NoCarrier
        // Edge: relaxed before Spawn readies it; ReadyPut's Swap publishes TaskNew.
        (*t).state.StoreOrd(TaskNew, 0)
        (*t).flags = 0
        (*t).pin = raw.Null[Carrier]()
        (*t).link = raw.Null[Task]()
        (*t).err = raw.Null[u8]()
        (*t).op = raw.Null[sys.Op]()
        (*t).job = raw.Null[u8]()
        (*t).wait_kind = WaitKindNone
        // Edge: relaxed before any Await or cancel walk can see t.
        (*t).op_live.StoreOrd(0, 0)
        (*t).blocked_in = raw.Null[ScopeRec]()
        // Edge: relaxed before any Await or cancel walk can see t.
        (*t).cancel_pin.StoreOrd(0, 0)
        (*t).hold = raw.Null[ScopeRec]()
        return t
    }
}

// Lay writes a fresh header and task record at the top of h's slot.
Lay(h: *rt.SlotHeader, id: u64): *Task {
    t := TaskOf(h)
    unsafe "h's slot is reserved for this task" {
        (*h).state = 2
        (*h).in_c = 0
        (*h).id = id
        (*h).trace_n = 0
        (*h).trace_dropped = 0
        (*h).carrier = rt.NoCarrier
        raw.Fill(raw.PtrCast[Task, u8](t), 0, SizeOf[Task]())
        // Edge: relaxed before Spawn readies it; ReadyPut's Swap publishes TaskNew.
        (*t).state.StoreOrd(TaskNew, 0)
        return t
    }
}

/// SpawnAlloc reserves a child of s and returns its argument block, size
/// bytes at align in the top half of its slot.
/// Panics with a stack-limit panic when the block does not fit in half the
/// slot. Waits while a limited scope has its limit of live tasks, and while
/// every slot of the scope's stack class is taken. O(1) besides those waits;
/// takes one task slot and commits its top pages, nothing from the heap.
export SpawnAlloc(s: &Scope, size: int, align: int, site: &rt.Site): *u8 {
    r := RecOf(s)
    unsafe "a live scope record" {
        need := TopBytes + Round16(int((*r).err_size)) + Round16(size) + (align - 1)
        if need > rt.SlotBytes((*r).class) / 2 {
            rt.PanicAt(rt.PanicStackLimit, site)
        }
        if (*r).limit > 0 {
            for {
                // Edge: the limit gate: the compare-and-swap below admits at
                // most limit tasks, and a task end's decrement then WakeWord (limited
                // scopes always wake) follows WaitWord's compare under the bucket lock.
                n := (*r).live.Load()
                if n < (*r).limit && (*r).live.Cas(n, n + 1) {
                    break
                }
                // The gate's scope for the spawner's leave hook (HookHold).
                p := Current()
                HoldSet(p, r, true)
                _ = WaitWord(LiveWord(r), n, false)
                HoldClear(p)
            }
        } else {
            // Edge: raised before the task exists, by the body or a task of s, so
            // Join's load cannot read 0 while it lives.
            _ = (*r).live.Add(1)
        }
        h := NewSlot((*r).class, need + FrameReserve)
        t := LayFast(h, rt.NextTaskId())
        (*t).home = r
        (*t).inner = r
        (*t).io = (*r).io
        // A scope opened from a pinned Io spawns on its carrier (carrier.mk's
        // pinned half); the bits are 0 on every other native Io, so a
        // shared spawn pays one test here. A Sim's Io has its slot there:
        // PinSpawn answers null for it. Called through the word the first
        // pinned claim wrote (TaskState.pin_spawn), so the optimizer keeps
        // it out of this function; before that claim only a Sim's Io has
        // the bits, and it gets the null a zero word would call.
        if (core.IoFlags((*r).io) & PinIoBits) != 0 {
            // Edge: the first claim's Store, before any pinned Io existed;
            // a Sim's Io may read 0 here, and gets the null PinSpawn
            // would answer.
            var w := (*TS()).pin_spawn.Load()
            if w != 0 {
                f := raw.Read(raw.PtrCast[u64, fn(Io): *Carrier](raw.Of(&w)))
                (*t).pin = f((*r).io)
            }
        }
        HookMark(t)
        // The birth fact: before Spawn readies t.
        HookBorn(t)
        var at := raw.Addr(t)
        if (*r).err_size > 0 {
            at -= Trunc[uint](Round16(int((*r).err_size)))
            (*t).err = raw.FromAddr[u8](at)
            raw.Write(raw.FromAddr[u32](at), 0)
        }
        at = (at - Trunc[uint](Round16(size))) & ~(Trunc[uint](align) - 1)
        (*t).block = raw.FromAddr[u8](at)
        return (*t).block
    }
}

/// Spawn makes the child whose block SpawnAlloc returned runnable. "A task
/// always starts": nothing here checks cancellation.
/// O(1); allocates nothing beyond what Ready may start.
export Spawn(s: &Scope, thunk: fn(*Io, *u8, *u8): bool, block: *u8) {
    h := rt.SlotOf(block)
    t := TaskOf(h)
    unsafe "the child reserved by SpawnAlloc" {
        (*t).thunk = thunk
        Start1(t, h, raw.Addr(block) & ~Trunc[uint](15), TaskEntry)
    }
}

// Start1 makes t's first context (Context1) and readies it on the calling
// task's carrier: ReadyOn's body written out (main's Ready inlined there),
// so Spawn keeps main's inlining of ReadyPut, Jitter and WakeOne. A
// pinned carrier runs only pinned tasks, so the calling task's pin tells a
// pinned carrier (OnPinned's test) with no null test of the carrier.
Start1(t: *Task, h: *rt.SlotHeader, top: uint, entry: fn(*u8)) {
    Context1(t, h, top, entry)
    cur := Current()
    var c := raw.Null[Carrier]()
    unsafe "t's pin is written by SpawnAlloc, before this first ready; the calling task's pin and carrier do not change while it runs" {
        if !raw.IsNull(cur) {
            c = (*cur).carrier
        }
        if !raw.IsNull((*t).pin) {
            _ = PinReadyOf(c, t)
            return
        }
        if !raw.IsNull(cur) && !raw.IsNull((*cur).pin) {
            ReadyOn(raw.Null[Carrier](), t)
            return
        }
    }
    ReadyPutNew(c, t)
    Jitter(JitterReady)
    WakeOne()
}

// Context1 makes t's first context below top: InitContext, and on Windows
// the NT_TIB words of its slot (Context's layout).
Context1(t: *Task, h: *rt.SlotHeader, top: uint, entry: fn(*u8)) {
    unsafe "t is a fresh task in slot h" {
        InitContext(raw.Of(&(*t).ctx), raw.FromAddr[u8](top), entry, raw.PtrCast[Task, u8](t))
        if Target.os == Os.Windows {
            stop := raw.Addr(h) + Trunc[uint](rt.HeaderBytes)
            (*t).ctx[1] = u64(stop)
            (*t).ctx[2] = u64(stop - Trunc[uint](int((*h).committed_pages) * 4096))
            (*t).ctx[3] = u64(stop - (Trunc[uint](1) << Trunc[uint](rt.ClassShift((*h).class))))
            (*t).ctx[4] = TaskGuarantee
        }
    }
}

// TaskEntry runs on the task's own stack: the thunk with &t.io,
// the first error, End, then the slot goes back.
TaskEntry(p: *u8) {
    unsafe "p is this task's record" {
        t := raw.PtrCast[u8, Task](p)
        FinishHandoff(t)
        f := (*t).thunk
        HookFirst(t)
        failed := f(raw.Of(&(*t).io), (*t).block, (*t).err)
        // The last leave (LastBit), after any note the
        // task owes, before the task's end is counted, so a hook never runs
        // after Join returned; HookBit stays set to the end.
        HookLast(t)
        r := (*t).home
        if failed && (*r).err_size > 0 {
            Fail(t)
        }
        // End: after this decrement the task reads nothing of its scope.
        limited := (*r).limit > 0
        word := LiveWord(r)
        // Edge: the task end: Fail's compare-and-swap and payload copy, and
        // every access of this task to its scope, precede this decrement in S;
        // Join's load that reads the final 0 follows it. Nothing of r is read
        // after it (the word was taken above).
        old := (*r).live.Add(0xFFFF_FFFF)
        if old == 1 || limited {
            WakeWord(word, true)
        }
        // A stress point after End: a task preempted here has released its
        // joiner, so any access to r placed after the decrement (the
        // mutation Fail after End) races the joiner's return
        // (test "RT-R17-FailBeforeEnd").
        Jitter(JitterEnd)
        SwitchOut(t, AfterFree, raw.Null[raw.Atomic[u32]]())
    }
    for {
        sys.ThreadYield()
    }
}
