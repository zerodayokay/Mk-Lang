// Why: every task runs on a stack slot of its own, and taking one must be
// cheap and must not fragment the address space, so slots come from large
// regions reserved once.
//
// What: Region and the slot classes (ClassDefault, ClassLarge,
// ClassForBytes), RegionsInit, TakeSlot, FreeSlot, GrowTop, the guard kind
// (GuardKind), and the trim of idle slots (TrimSlots, TrimKeep) with its
// test hooks.
//
// How: two reserved slot regions (Default 1 MiB slots, Large 16 MiB slots,
// 1 TiB each, halved on failure down to 1,024 slots), further classes
// reserved lazily; a slot is prepared on first use (committed top and a
// guard page) and returns to its class's free stack when its task dies.

import std.raw
import std.sys

/// ClassDefault is the slot class of a task with the default 1 MiB stack.
export const ClassDefault: u8 = 0
/// ClassLarge is the slot class of a task with a 16 MiB stack.
export const ClassLarge: u8 = 1
/// SlotClasses is how many slot classes there are: Default, Large, and
/// StackSize.Bytes's power-of-two sizes from 64 KiB (class 2) to 512 MiB
/// (class 15), class c holding slots of 2^(c + 14) bytes; 1 MiB and 16 MiB
/// are Default's and Large's.
export const SlotClasses: int = 16
// BytesMinShift and BytesMaxShift bound StackSize.Bytes: 64 KiB to 512 MiB.
const BytesMinShift: int = 16
const BytesMaxShift: int = 29
// LazyReserve is the address space a Bytes class reserves on first use
// (never fewer than 1,024 slots), against 1 TiB for Default and Large.
const LazyReserve: int = 1 << 38
// LazyLock admits one reservation of a Bytes class at a time.
var LazyLock: raw.Atomic[u32] = raw.Atomic[u32].New(0)

/// ClassShift is log2 of the slot size of class. O(1); allocates nothing.
export ClassShift(class: u8): int {
    if class == ClassDefault {
        return 20
    }
    if class == ClassLarge {
        return 24
    }
    return int(class) + 14
}

/// ClassForBytes is the slot class of StackSize.Bytes(n): n rounded up to
/// a power of two, at least 64 KiB; Default's or Large's for 1 MiB and 16
/// MiB. Its region is reserved here the first time (EnsureRegion). 255 when
/// n is above 512 MiB or the OS refuses the reservation. O(1) after the
/// first use of a class; allocates nothing from the Heap.
export ClassForBytes(n: int): u8 {
    if n > 1 << BytesMaxShift {
        return 255
    }
    var shift := BytesMinShift
    for shift < BytesMaxShift && 1 << shift < n {
        shift += 1
    }
    if shift == 20 {
        return ClassDefault
    }
    if shift == 24 {
        return ClassLarge
    }
    class := Trunc[u8](shift - 14)
    if !EnsureRegion(class) {
        return 255
    }
    return class
}

// EnsureRegion reserves class's region when it has none: under LazyLock,
// at most LazyReserve bytes of slots and never fewer than 1,024 (halving
// on refusal, as RegionsInit does). False when the OS refuses.
EnsureRegion(class: u8): bool {
    // Edge: RegionReserve stores base last; a nonzero base is a whole region.
    if Regions.r[int(class)].base.Load() != 0 {
        return true
    }
    Lock(&LazyLock)
    // Edge: as above, under the lock.
    var ok := Regions.r[int(class)].base.Load() != 0
    if !ok {
        shift := ClassShift(class)
        var slots := LazyReserve >> shift
        if slots < 1024 {
            slots = 1024
        }
        ok = RegionReserve(RegionOf(class), Trunc[u32](shift), slots, int(class))
        if ok {
            // Edge: the region's words (RegionReserve) and its next before
            // the head names it, so a SlotTop that reads the head reads
            // them; only this lock's holder stores either.
            Regions.r[int(class)].next.Store(u64(Regions.lazy.Load()))
            Regions.lazy.Store(u32(class))
            LazyLookups()
        }
    }
    Unlock(&LazyLock)
    return ok
}
/// GuardBytes is the guard page at every slot's base: 4 KiB, or the OS page
/// where that is larger (16 KiB on arm64 macOS, where no smaller range can
/// be protected); Windows also keeps the 16 KiB guarantee above it
/// (std/task/spawn.mk's TaskGuarantee). Every slot is at least 64 KiB, so a
/// slot always has room above its guard. O(1); allocates nothing.
export GuardBytes(): int {
    ps := sys.PageSize()
    return if ps > 4096 { ps } else { 4096 }
}
// FreshCommit is the top a fresh Windows slot commits: one page, which
// holds the 512-byte top, the usual argument block and the initial frame; a
// task that needs more grows through the PAGE_GUARD page below it as any
// Windows thread stack does (one guard fault per page, on the slot's first
// use only: a reused slot keeps its pages). One page rather than 8 KiB
// keeps 4 KiB less committed per idle task (TestRtSched_IdleWindows bounds
// an idle task's commit).
const FreshCommit: int = 4096

/// The guard kind of Linux slots: GuardUntried until the first
/// slot is prepared, GuardLight while madvise(MADV_GUARD_INSTALL) works (the
/// whole slot read-write, the lowest page a guard in the page tables: no
/// VMA per slot), GuardProt for good once the kernel refused it or
/// MK_TEST_GUARDS=prot asked for it (the lowest page PROT_NONE: two VMAs per
/// slot, so idle tasks are bounded by (vm.max_map_count - maps) / 2).
/// macOS has no light guard: its slots are GuardProt from the start.
/// Windows slots always use PAGE_GUARD and never read it.
export const GuardUntried: u32 = 0
/// GuardLight: the lowest page of a slot is a madvise guard.
export const GuardLight: u32 = 1
/// GuardProt: the lowest page of a slot is PROT_NONE.
export const GuardProt: u32 = 2

struct GuardWords {
    mode: raw.Atomic[u32]
}

var Guards: GuardWords = GuardWords(mode: raw.Atomic[u32].New(0))

/// GuardKind is the guard kind slots are prepared with now (GuardUntried
/// before the first Linux slot, and always on Windows; GuardProt on macOS).
/// O(1); allocates nothing.
export GuardKind(): u32 {
    // Edge: none needed; every slot handed out was prepared under the mode
    // its own Prepare read, and the mode only moves to GuardProt.
    return Guards.mode.Load()
}

/// One region's bookkeeping, in the RtState page (rt.mk).
export struct Region {
    /// The reservation's lowest address; 0 when the region has none.
    export base: uint
    /// The bytes of one slot, a power of two.
    export slot_bytes: int
    /// log2 of slot_bytes.
    export shift: u32
    /// How many slots the reservation holds.
    export slots: int
    /// How many slots may be handed out: slots, or fewer under
    /// MK_TEST_SLOTS.
    export usable: int
    lock: raw.Atomic[u32]
    next: int
    free: *SlotHeader
    free_count: int
    // over is 1 while free_count > TrimKeep, written under lock only when
    // free_count crosses it, so an idle carrier can test it without the
    // lock (TrimSlots).
    over: raw.Atomic[u32]
    // cold is the stack of trimmed free slots (TrimSlots): their pages
    // below the top one are given back; TakeSlot pops free first, then
    // cold, then a never-used index.
    cold: *SlotHeader
    cold_count: int
    // Indices whose Prepare failed, to be tried again before next (so an
    // OS refusal does not lose the index); under lock.
    retry: [16]int
    nretry: int
}

// RegionReserve reserves one region: slots of 2^shift bytes, halving the
// count on failure down to 1,024.
RegionReserve(r: *Region, shift: u32, var slots: int, index: int): bool {
    slot := 1 << Trunc[int](shift)
    for slots >= 1024 {
        p := sys.Reserve(slots * slot, slot)
        if !raw.IsNull(p) {
            unsafe "r is this region's record in the RtState page" {
                (*r).base = raw.Addr(p)
                (*r).slot_bytes = slot
                (*r).shift = shift
                (*r).slots = slots
                (*r).usable = slots
                // Edge: shift and size before base: SlotTop reads base first and treats
                // 0 as no region, so a reader that sees base sees the rest; all before any
                // task exists.
                Regions.r[index].shift.Store(u64(shift))
                Regions.r[index].size.Store(Trunc[u64](slots * slot))
                Regions.r[index].base.Store(u64(raw.Addr(p)))
            }
            return true
        }
        slots = slots / 2
    }
    return false
}

/// RegionsInit reserves both regions (during Start); test_slots, when 16 or
/// more, caps the Default region's usable slots (MK_TEST_SLOTS), and
/// prot_guards forces the PROT_NONE guard (MK_TEST_GUARDS=prot). The
/// caller passes -1 and false in a release build.
/// False when the OS refuses a reservation even at 1,024 slots. O(1): per
/// region at most 11 reservation calls of address space (the count halves on
/// each refusal), nothing committed; allocates nothing from the Heap.
export RegionsInit(test_slots: int, prot_guards: bool): bool {
    if (prot_guards && Target.os == Os.Linux) || Target.os == Os.Macos {
        // Edge: none needed; stored before any task exists.
        Guards.mode.Store(GuardProt)
    }
    st := R()
    unsafe "st is the RtState page" {
        if !RegionReserve(raw.Of(&(*st).regions[0]), 20, 1_048_576, 0) {
            return false
        }
        if !RegionReserve(raw.Of(&(*st).regions[1]), 24, 65_536, 1) {
            return false
        }
        if test_slots >= 16 && test_slots < (*st).regions[0].usable {
            (*st).regions[0].usable = test_slots
        }
    }
    return true
}

/// UsableSlots is the number of slots of class a spawn may use: the
/// reservation's count, or MK_TEST_SLOTS's cap in a dev or test build.
/// O(1); allocates nothing.
export UsableSlots(class: u8): int {
    unsafe "the region records are written once in Start" {
        return (*RegionOf(class)).usable
    }
}

// RegionOf is the record of a slot class.
RegionOf(class: u8): *Region {
    st := R()
    unsafe "st is the RtState page" {
        return raw.Of(&(*st).regions[int(class)])
    }
}

// Prepare makes a never-used slot usable. Windows: the top
// FreshCommit bytes, then a PAGE_GUARD page below them. Linux: the
// whole slot read-write, then a light guard on its lowest page; if the
// kernel refuses it, that page is made PROT_NONE and every later slot is
// prepared the older way (GuardProt): read-write above the PROT_NONE page,
// which the reservation left so. retried is a slot whose earlier Prepare
// failed (TakeSlot's retry list): its lowest page may have been left
// read-write, so the older way makes it PROT_NONE again, and a slot is
// handed out only with a guard of either kind. false when the OS
// refuses; the slot is then left as reserved as it can be made.
Prepare(top: uint, slot: int, retried: bool): bool {
    unsafe "top is the top of a reserved, never-used slot" {
        base := raw.FromAddr[u8](top - Trunc[uint](slot))
        if Target.os == Os.Windows {
            c := raw.FromAddr[u8](top - Trunc[uint](FreshCommit))
            if !sys.Commit(c, FreshCommit) {
                return false
            }
            if !sys.GuardPage(raw.Offset(c, -4096)) {
                return false
            }
            h := raw.FromAddr[SlotHeader](top - Trunc[uint](HeaderBytes))
            (*h).committed_pages = Trunc[u16](FreshCommit / 4096)
            return true
        }
        // Edge: none needed beyond the value: a stale GuardLight only makes
        // this slot try the light guard once more, which the kernel refuses
        // again (its answer is the same for every slot, sys.GuardCheck).
        if Guards.mode.Load() != GuardProt {
            if !sys.CommitStack(base, slot) {
                return false
            }
            if sys.GuardStack(base) {
                // Edge: none needed; a hint for GuardKind (Untried to Light).
                _ = Guards.mode.Cas(GuardUntried, GuardLight)
                return true
            }
            // Refused (a kernel before 6.13): the older form for good.
            // Edge: none needed; every Prepare after this load takes the
            // older form, and one that read the old value is refused too.
            Guards.mode.Store(GuardProt)
            if sys.GuardPage(base) {
                return true
            }
            // No guard can be made: back to reserved (PROT_NONE), and the
            // index goes on the retry list.
            sys.Decommit(base, slot)
            return false
        }
        if retried && !sys.GuardPage(base) {
            return false
        }
        return sys.CommitStack(raw.Offset(base, GuardBytes()), slot - GuardBytes())
    }
}

/// GrowTop commits the top need bytes of a Windows slot when they are not
/// committed yet, moving the guard page below them. Linux slots are
/// committed whole.
/// False when the OS refuses the commit. O(1): at most one commit and one guard
/// move; commits need bytes of the slot.
export GrowTop(h: *SlotHeader, need: int): bool {
    if Target.os != Os.Windows {
        return true
    }
    unsafe "h is the header of a prepared slot" {
        have := int((*h).committed_pages) * 4096
        if need <= have {
            return true
        }
        want := (need + 4095) / 4096 * 4096
        top := raw.Addr(h) + Trunc[uint](HeaderBytes)
        lo := raw.FromAddr[u8](top - Trunc[uint](want))
        if !sys.Commit(lo, want - have) || !sys.GuardPage(raw.Offset(lo, -4096)) {
            return false
        }
        (*h).committed_pages = Trunc[u16](want / 4096)
        return true
    }
}

/// TakeSlot pops a free slot of class, else prepares the next never-used one;
/// null when the region is full or the OS refuses (the caller waits).
/// O(1) under the region lock; a never-used slot costs its preparation's system
/// calls (its top pages and the guard).
export TakeSlot(class: u8): *SlotHeader {
    r := RegionOf(class)
    unsafe "r is a region record; slots are inside its reservation" {
        Lock(&(*r).lock)
        h := (*r).free
        if !raw.IsNull(h) {
            (*r).free = (*h).next_free
            (*r).free_count -= 1
            if (*r).free_count == TrimKeep {
                // Edge: none needed; a hint for TrimSlots, which rechecks
                // under the lock.
                (*r).over.Store(0)
            }
            Unlock(&(*r).lock)
            (*h).next_free = raw.Null[SlotHeader]()
            return h
        }
        c := (*r).cold
        if !raw.IsNull(c) {
            (*r).cold = (*c).next_free
            (*r).cold_count -= 1
            Unlock(&(*r).lock)
            (*c).next_free = raw.Null[SlotHeader]()
            if (*c).unready == 1 {
                // A Linux slot whose read-write step failed in TrimOne:
                // try it again; refused, it goes back on cold (no
                // capacity is lost) and the caller waits as for a full
                // region, woken by the next slot freed.
                if !ReadyCold(c, (*r).slot_bytes) {
                    Lock(&(*r).lock)
                    (*c).next_free = (*r).cold
                    (*r).cold = c
                    (*r).cold_count += 1
                    Unlock(&(*r).lock)
                    return raw.Null[SlotHeader]()
                }
                (*c).unready = 0
            }
            return c
        }
        var i := 0
        var retried := false
        if (*r).nretry > 0 {
            (*r).nretry -= 1
            i = (*r).retry[(*r).nretry]
            retried = true
        } else {
            if (*r).next >= (*r).usable {
                Unlock(&(*r).lock)
                return raw.Null[SlotHeader]()
            }
            i = (*r).next
            (*r).next += 1
        }
        Unlock(&(*r).lock)
        top := (*r).base + Trunc[uint]((i + 1) * (*r).slot_bytes)
        if !Prepare(top, (*r).slot_bytes, retried) {
            // Give the index back: the newest one by lowering next, else
            // on the retry list. With the list full (16 refusals waiting at
            // once) the index stays unused for good; the region still has
            // every other one.
            Lock(&(*r).lock)
            if (*r).next == i + 1 {
                (*r).next = i
            } else if (*r).nretry < 16 {
                (*r).retry[(*r).nretry] = i
                (*r).nretry += 1
            }
            Unlock(&(*r).lock)
            return raw.Null[SlotHeader]()
        }
        p := raw.FromAddr[SlotHeader](top - Trunc[uint](HeaderBytes))
        (*p).class = class
        return p
    }
}

/// FreeSlot returns a dead task's slot, prepared and with its pages still
/// committed.
/// O(1) under the region lock; allocates nothing, frees nothing.
export FreeSlot(h: *SlotHeader) {
    unsafe "h is the header of a slot whose task has ended" {
        (*h).state = 1
        (*h).next_free = raw.Null[SlotHeader]()
        FreeSlotBatch((*h).class, h, h, 1)
    }
}

/// TakeSlotBatch pops up to max already-prepared free slots of class in one
/// lock acquisition, returning the linked list head (or null) and writing the
/// count to got. Never prepares fresh slots or touches cold.
export TakeSlotBatch(class: u8, max: int, got: mut int): *SlotHeader {
    if max <= 0 {
        *got = 0
        return raw.Null[SlotHeader]()
    }
    r := RegionOf(class)
    unsafe "r is a region record; slots are inside its reservation" {
        Lock(&(*r).lock)
        head := (*r).free
        if raw.IsNull(head) {
            Unlock(&(*r).lock)
            *got = 0
            return head
        }
        was_over := (*r).free_count > TrimKeep
        var tail := head
        var n := 1
        for n < max && !raw.IsNull((*tail).next_free) {
            tail = (*tail).next_free
            n += 1
        }
        (*r).free = (*tail).next_free
        (*tail).next_free = raw.Null[SlotHeader]()
        (*r).free_count -= n
        if was_over && (*r).free_count <= TrimKeep {
            // Edge: none needed; a hint for TrimSlots, which rechecks under the lock.
            (*r).over.StoreOrd(0, 0)
        }
        Unlock(&(*r).lock)
        *got = n
        return head
    }
}

/// FreeSlotBatch pushes n dead slots linked head..tail back onto class's free
/// stack in one lock acquisition.
export FreeSlotBatch(class: u8, head: *SlotHeader, tail: *SlotHeader, n: int) {
    r := RegionOf(class)
    unsafe "r is a region record; head..tail are n dead slots of class" {
        Lock(&(*r).lock)
        was_under := (*r).free_count <= TrimKeep
        (*tail).next_free = (*r).free
        (*r).free = head
        (*r).free_count += n
        if was_under && (*r).free_count > TrimKeep {
            // Edge: none needed; a hint for TrimSlots, which rechecks under the lock.
            (*r).over.StoreOrd(1, 0)
        }
        Unlock(&(*r).lock)
    }
}

/// TrimKeep is how many free slots of a class keep their pages;
/// TrimBatch bounds the slots one TrimSlots call trims, so an idle carrier
/// returns to its poller soon.
export const TrimKeep: int = 256
const TrimBatch: int = 32

/// TrimSlots trims up to TrimBatch free slots of class beyond TrimKeep
/// and returns how many it trimmed. Called by an idle carrier
/// (std.task Idle), never on a spawn or a task end. The slots are taken off
/// the free stack under the lock, trimmed outside it (system calls), and
/// pushed on the cold stack under the lock again. While they are off both
/// stacks a spawn may find no slot and wait: the caller must then do a task
/// end's wake (std.task SlotFreed) once per trimmed slot, which ends that
/// wait as a freed slot would (spawn.mk's NewSlot argument). Which free
/// slots are trimmed: the newest beyond the kept ones (the top of the LIFO
/// stack, O(1) per slot); the oldest would need a walk of 256
/// under the lock or a second link; after a spike every free slot is as
/// cold as another, so the kept set's age does not matter. O(TrimBatch)
/// plus the system calls.
export TrimSlots(class: u8): int {
    r := RegionOf(class)
    unsafe "r is a region record; the slots taken are free, owned by nobody" {
        // Edge: none needed; a hint (above), and a missed trim waits for the
        // next idle pass.
        if (*r).over.Load() == 0 {
            return 0
        }
        var batch: [32]*SlotHeader = [raw.Null[SlotHeader](); 32]
        var n := 0
        Lock(&(*r).lock)
        for n < TrimBatch && (*r).free_count > TrimKeep {
            h := (*r).free
            (*r).free = (*h).next_free
            (*r).free_count -= 1
            batch[n] = h
            n += 1
        }
        if (*r).free_count <= TrimKeep {
            // Edge: none needed; the same hint, stored under the lock.
            (*r).over.Store(0)
        }
        Unlock(&(*r).lock)
        if n > 0 {
            TrimGateHold()
        }
        var ok: [32]bool = [false; 32]
        for i in 0..n {
            ok[i] = TrimOne(batch[i], (*r).slot_bytes)
        }
        Lock(&(*r).lock)
        for i in 0..n {
            h := batch[i]
            // A slot whose Linux read-write step failed goes on cold too,
            // marked unready: TakeSlot makes it read-write before use, so
            // no refusal loses the slot.
            (*h).unready = if ok[i] { 0 } else { 1 }
            (*h).next_free = (*r).cold
            (*r).cold = h
            (*r).cold_count += 1
        }
        Unlock(&(*r).lock)
        return n
    }
}

// TrimOne gives back the pages of a free slot below its top page (which
// holds the header and stays). Linux: the range above the base guard page
// is discarded (sys.Discard, madvise(MADV_DONTNEED)), so its pages
// are dropped and read zero on the next touch, with the mapping and its
// protection kept; the guard page of either kind is not in the range.
// Windows: everything below the page under the top FreshCommit bytes is
// decommitted, and that page, still committed (the slot had grown past
// it), becomes the PAGE_GUARD page again, so the slot is as Prepare leaves
// a fresh one, with committed_pages to match (the next task's StackLimit,
// spawn.mk); a slot that never grew is left.
// Neither target can refuse (one madvise, no VMA split, so nothing can
// fail near vm.max_map_count). False only from the test hook TrimRefuse: the slot
// is then cold and unready, and TakeSlot readies it (ReadyCold) before a
// task runs there, the path RT14-Trim-Refused checks.
TrimOne(h: *SlotHeader, slot: int): bool {
    unsafe "h is the header of a free slot that nobody else holds now" {
        top := raw.Addr(h) + Trunc[uint](HeaderBytes)
        base := top - Trunc[uint](slot)
        if Target.os == Os.Windows {
            if int((*h).committed_pages) * 4096 <= FreshCommit {
                return true
            }
            g := top - Trunc[uint](FreshCommit) - 4096
            sys.Decommit(raw.FromAddr[u8](base), Trunc[int](g - base))
            // A committed page changes protection: no commit charge, so the
            // call does not fail for want of memory. If it did, the page
            // stays read-write and counted, and a task that grows past it
            // faults as on a full slot.
            guarded := sys.GuardPage(raw.FromAddr[u8](g))
            (*h).committed_pages = Trunc[u16](if guarded { FreshCommit / 4096 } else { FreshCommit / 4096 + 1 })
            return true
        }
        sys.Discard(TrimLo(h, slot), slot - GuardBytes() - 4096)
        return !TrimHookRefuses()
    }
}

// TrimLo is the lowest byte TrimOne gives back on Linux: above the base
// guard page (of either kind), which stays.
TrimLo(h: *SlotHeader, slot: int): *u8 {
    unsafe "h is the header of a slot of slot bytes; the address is inside it" {
        top := raw.Addr(h) + Trunc[uint](HeaderBytes)
        return raw.FromAddr[u8](top - Trunc[uint](slot) + Trunc[uint](GuardBytes()))
    }
}

// ReadyCold readies an unready cold slot: one TrimOne refused (only the
// test hook TrimRefuse does). It makes the trimmed range
// read-write (already so after a Discard, so the call changes nothing on
// a real slot), and refuses while the hook is armed, as a kernel's ENOMEM
// would. Linux only: a Windows trim never refuses.
ReadyCold(h: *SlotHeader, slot: int): bool {
    return if TrimHookRefuses() { false } else { sys.CommitStack(TrimLo(h, slot), slot - GuardBytes() - 4096) }
}

// TrimHookRefuses counts the test hook down and is true while it is armed.
TrimHookRefuses(): bool {
    // Edge: none needed; a test's own count, set before it trims. A Cas,
    // so two carriers trimming at once never count it below 0.
    for {
        v := TrimSlotFail.Load()
        if v == 0 {
            break
        }
        if TrimSlotFail.Cas(v, v - 1) {
            return true
        }
    }
    return false
}

// TrimSlotFail is a test hook (RT14-Trim-Refused, through TrimRefuse):
// while above 0, each TrimOne and ReadyCold refuses and counts it down.
// Off it costs one load per trimmed slot, never a spawn's.
var TrimSlotFail: raw.Atomic[u32] = raw.Atomic[u32].New(0)

/// TrimRefuse makes the next n TrimOne and ReadyCold calls refuse (tests
/// only).
/// O(1); allocates nothing.
export TrimRefuse(n: u32) {
    // Edge: none needed; read by the trims the test then causes.
    TrimSlotFail.Store(n)
}

/// SlotCounts is the free and the cold slot counts of class, and the pages
/// the cold ones keep committed by their headers' count (a Windows count;
/// Linux slots read 0 there), for the trim tests. O(cold) under the lock;
/// allocates nothing.
export SlotCounts(class: u8): (free: int, cold: int, cold_pages: int) {
    r := RegionOf(class)
    unsafe "r is a region record" {
        Lock(&(*r).lock)
        f := (*r).free_count
        k := (*r).cold_count
        var pages := 0
        var c := (*r).cold
        for !raw.IsNull(c) {
            pages += int((*c).committed_pages)
            c = (*c).next_free
        }
        Unlock(&(*r).lock)
        return (free: f, cold: k, cold_pages: pages)
    }
}

/// SlotBytes is the slot size of a class.
/// O(1); allocates nothing.
export SlotBytes(class: u8): int {
    return 1 << ClassShift(class)
}

/// LazyClasses is whether any StackSize.Bytes class has a region, so the
/// idle trim looks past Default and Large. O(1); allocates nothing.
export LazyClasses(): bool {
    // Edge: EnsureRegion's Store, after the region's words.
    return Regions.lazy.Load() != 0
}

// TrimGate is a controlled-schedule hook for tests (RT14-Trim-RaceGate),
// std.task IdleGate's protocol: 0 off, 1 armed, 2 a TrimSlots call holds
// its batch off both stacks (between its two lock sections), 3 released.
// Off it costs a trim that took slots one load, never a spawn's.
var TrimGate: raw.Atomic[u32] = raw.Atomic[u32].New(0)

/// TrimGateSet and TrimGateGet arm, release and read TrimGate (tests only).
/// O(1); allocates nothing.
export TrimGateSet(v: u32) {
    // Edge: seq_cst; a release (3) is read by the holder's next Load.
    TrimGate.Store(v)
}

/// TrimGateGet reads TrimGate.
/// O(1); allocates nothing.
export TrimGateGet(): u32 {
    // seq_cst: reading 2 means a batch is off both stacks (edge: the
    // holder's Unlock, then its Cas to 2, then this load).
    return TrimGate.Load()
}

// TrimGateHold holds the first trim that reaches it while TrimGate is
// armed until a test releases it, or for 2 s at most (then it opens the
// gate itself, so a test that never releases it delays one trim once).
TrimGateHold() {
    // Edge: none needed beyond seq_cst (TrimGateGet's comment).
    if TrimGate.Load() == 1 && TrimGate.Cas(1, 2) {
        t0 := sys.Now()
        for TrimGate.Load() == 2 && sys.Now() - t0 < 2_000_000_000 {
            sys.ThreadYield()
        }
        _ = TrimGate.Cas(2, 0)
    }
}

/// SlotsMade is how many slots of class have been prepared and handed out
/// at least once (the indices below next that are not on the retry list),
/// for the trim tests: once no task runs, each is free or cold. O(1) under
/// the lock; allocates nothing.
export SlotsMade(class: u8): int {
    r := RegionOf(class)
    unsafe "r is a region record" {
        Lock(&(*r).lock)
        n := (*r).next - (*r).nretry
        Unlock(&(*r).lock)
        return n
    }
}
