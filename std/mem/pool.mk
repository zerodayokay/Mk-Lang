// Why: graphs, entity tables and collectors need values that refer to each
// other by name rather than by reference, with stale names detected
// instead of reaching freed or reused slots.
//
// What: Pool[T], its Handle and PoolIter: New, Add, Get, GetMut, GetPair,
// Free, Len, Iter and Sweep (a mark-and-sweep collector's pass).
//
// How: the slot map of Rust's slotmap crate (SlotMap: a version per slot, a
// free list of vacant slots), in an arena. The rest of this header gives the
// layout, how another pool's handles are told apart, the invariants and the
// costs.
//
// Layout. slots is a buffer of cap Slot[T] in the pool's arena, n of them
// in use: a generation and the value, none while the slot is vacant. Every
// slot of the buffer is written (base, none) when the buffer grows, so a
// new slot needs nothing before x goes in. It doubles when full, in place
// or by moving the n slots bitwise to a new block and leaving the old one
// to the arena, as List does; growth never touches x, so Add keeps its
// argument on failure. vacant is a List of the vacant slots'
// indices, used last in first out. A Handle is 8 bytes: the slot
// index and the slot's 32-bit generation.
//
// Another pool's handles. A Handle has no room for a pool identity, so
// each pool starts its generations at its own base, drawn once per New
// from a process-wide counter and spread by Fibonacci hashing (Knuth,
// TAOCP 6.4): the bases of consecutive pools are about 2^32 / phi apart.
// Another pool's handle matches only where both slots have the same index
// and the same generation: for two consecutive pools that takes one of the
// two slots to be reused about 2^31 times, and among N pools the nearest
// bases are about 2^32 / N apart; short of that it gets none and false. The
// guarantee that a foreign handle gets none, which a 16-byte handle with a
// pool identity gave, is probabilistic at 8 bytes.
//
// Invariants (checked by std's Go tests against a model of random Add,
// Free, Get, GetMut, Len, Iter, Sweep and GetPair sequences with stale and
// foreign handles):
// - P1: a handle h gets its value from Get, GetMut and GetPair, and Free(h)
//   is true, exactly when h was returned by this pool's Add and not freed
//   or swept since (another pool's handle: above); every other handle gets
//   none and false, never a panic.
// - P2: a slot's generation only moves forward from base, by one per free
//   (wrapping); a slot whose next generation would be base again (2^32 - 1
//   reuses) is retired, never put back on vacant, so no stale handle can
//   match again.
// - P3: Len is the number of occupied slots, and Iter visits exactly them,
//   in slot order.
//
// Costs: Add, Get, GetMut, GetPair, Free and Len are O(1); a whole Iter
// pass is O(n), n the slots ever used; Sweep is n calls of keep. Add
// allocates only when slots or vacant grow (amortized O(1), List's
// doubling in the arena, which keeps what it outgrows until the arena is
// freed); Free and Sweep allocate only when vacant grows, and a failure
// there retires the slot instead. Free and Sweep drop values, so T is not
// linear (E0701 at the call). No std peer exists in C, Rust,
// Zig or Go's standard libraries (Rust's is the slotmap crate, outside
// std), so there is no tools/bench ratio row.

import std.core
import std.raw

// The next pool's identity. Ordering: only the Add's atomicity matters
// (each New gets a distinct old value); no other memory is published
// through it, so the sequential consistency raw.Atomic gives is more than
// it needs and creates no edge anything relies on.
struct PoolIdWord {
    next: raw.Atomic[u64]
}

var PoolIds: PoolIdWord = PoolIdWord(next: raw.Atomic[u64].New(1))

/// The name of a value in a Pool: 8 bytes, plain, Eq and Hash, with no
/// public fields. It holds a slot index and that slot's 32-bit generation.
/// To keep two kinds of handle apart, wrap each in a one-field struct.
export struct Handle {
    idx: u32
    ver: u32
}

impl Eq for Handle {
    Eq(&self, other: &Self): bool {
        return self.idx == other.idx && self.ver == other.ver
    }
}

// The slot index alone: the generation starts at the pool's base, which
// depends on how many pools the process made before (and in what order its
// tasks made them), so hashing it would make a handle-keyed map's order
// differ between runs. Consistent with Eq, since equal handles have equal
// indices; the cost is that the handles of one slot's generations collide,
// which only a map keeping stale handles of a reused slot sees.
impl Hash for Handle {
    Hash(&self, h: mut Hasher) {
        h.U64(u64(self.idx))
    }
}

struct Slot[T] {
    ver: u32
    val: ?T
}

/// Values of T in slots of an arena, each named by a Handle that goes
/// stale when its value is freed. A freed slot is reused with a new
/// generation, so a stale handle gets none, never another value.
///
/// A handle carries no pool identity, so each pool starts its generations
/// at its own base. Another pool's handle is refused unless its slot and
/// generation match a live value here: between two pools that takes about
/// 2^31 reuses of one slot, and among the N pools a process made the
/// nearest bases are about 2^32 / N apart. A false match is never
/// memory-unsafe, since every value of a pool is a T.
export view struct Pool[T] {
    slots: *Slot[T]
    n: int
    cap: int
    vacant: List[u32]
    live: int
    base: u32
    alloc: &Alloc
}

// PoolSlot is slot i of p as a mutable reference; the callers keep i < n.
PoolSlot[T](p: *Slot[T], i: int): mut Slot[T] {
    unsafe "slot i < n of the live buffer, initialized (zero fill or a write), reached under the pool's mut loan" {
        return mut *raw.Offset(p, i)
    }
}

// PoolSlotRef is PoolSlot under the pool's shared loan.
PoolSlotRef[T](p: *Slot[T], i: int): &Slot[T] {
    unsafe "slot i < n of the live buffer, initialized, reached under the pool's shared loan" {
        return &*raw.Offset(p, i)
    }
}

// PoolSet stores x in place. The value place held is forgotten: the
// callers pass a vacant place (none, which owns nothing) or, from Free, a
// value of a T that is not linear, which Forget drops (as
// std.core's Discard; Free checks raw.IsLinear first). A plain assignment
// cannot say this in a body generic over T (E0402).
PoolSet[T](place: mut ?T, x: ?T) {
    p := raw.OfMut(place)
    unsafe "p is place, live and initialized; its value is none or a non-linear T the caller gives up" {
        raw.Forget(raw.Read(p))
        raw.Write(p, x)
    }
}

// PoolGrow doubles the buffer (8 slots at first), in place when the arena
// can resize its last block, else into a fresh block that the cap slots
// (every one written) move to bitwise, the old one left to the arena
// (List's rule). The new slots are written (base, none). False when the
// arena is out of memory or the pool is at SlotMax, and nothing changed
// then.
PoolGrow[T](p: mut Pool[T]): bool {
    var c := if p.cap > 0 { p.cap * 2 } else { 8 }
    if c > SlotMax {
        c = SlotMax
    }
    size := SizeOf[Slot[T]]()
    align := AlignOf[Slot[T]]()
    if c <= p.cap || c > 0x7FFFFFFFFFFFFFFF / size {
        return false
    }
    al := p.alloc
    var d := p.slots
    unsafe "the buffer is p's own block of cap slots, all written, or a fresh block of c; the cap slots move bitwise and the old copies are never read again" {
        if p.cap == 0 || !core.ResizeBytes(al, raw.PtrCast[Slot[T], u8](p.slots), p.cap * size, c * size, align) {
            q := core.AllocBytes(al, c * size, align)
            if raw.IsNull(q) {
                return false
            }
            d = raw.PtrCast[u8, Slot[T]](q)
            raw.Copy(d, p.slots, p.cap)
        }
        for i in p.cap..c {
            raw.Write(raw.Offset(d, i), Slot[T](ver: p.base, val: none))
        }
    }
    p.slots = d
    p.cap = c
    return true
}

// SlotMax bounds the index: it fits a u32.
const SlotMax: int = 0xFFFFFFFF

// PoolBase is the first generation of the pool numbered id: id times
// 2^64 / phi, its top 32 bits (Fibonacci hashing, Knuth TAOCP 6.4).
PoolBase(id: u64): u32 {
    return Trunc[u32]((id *% 0x9E3779B97F4A7C15) >> 32)
}

/// An empty pool in a. O(1); allocates nothing until the first Add.
export Pool[T].New(a: &Arena): Pool[T] {
    return Pool[T](slots: raw.Null[Slot[T]](), n: 0, cap: 0, vacant: List[u32].New(a), live: 0, base: PoolBase(PoolIds.next.Add(1)), alloc: a.Alloc())
}

/// Stores x and returns its handle: in the slot freed last when there is
/// one, else in a new slot at the end. Fails with OutOfMemory when the
/// arena cannot grow the pool or the pool holds 2^32 - 1 slots; then
/// nothing changed and the caller keeps x. Amortized O(1); a growth
/// doubles the buffer in the arena, which keeps the old one.
export Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle {
    if i := self.vacant.Pop() {
        s := PoolSlot(self.slots, int(i))
        PoolSet(mut s.val, some(x)) // a vacant slot holds none
        self.live += 1
        return Handle(idx: i, ver: s.ver)
    }
    if self.n == self.cap && !PoolGrow(mut *self) {
        fail OutOfMemory
    }
    i := self.n
    self.n += 1
    s := PoolSlot(self.slots, i) // written (0, none) by PoolGrow
    PoolSet(mut s.val, some(x))
    self.live += 1
    return Handle(idx: Trunc[u32](i), ver: s.ver)
}

// PoolMatch is true when h names one of p's slots and carries that slot's
// generation. The slot may still be vacant: a retired one (P2) keeps its
// last generation, so the callers also check the value is there (P1).
PoolMatch[T](p: &Pool[T], h: Handle): bool {
    if int(h.idx) >= p.n {
        return false
    }
    return PoolSlotRef(p.slots, int(h.idx)).ver == h.ver
}

/// The value h names; none for a stale, foreign or out-of-range handle,
/// never a panic. O(1).
export Pool[T].Get(&self, h: Handle): ?&T {
    if !PoolMatch(self, h) {
        return none
    }
    s := PoolSlotRef(self.slots, int(h.idx))
    if v := &s.val {
        return some(v)
    }
    return none
}

/// The value h names, to update in place; none as Get. O(1).
export Pool[T].GetMut(mut self, h: Handle): ?mut T {
    if !PoolMatch(&*self, h) {
        return none
    }
    s := PoolSlot(self.slots, int(h.idx))
    if v := mut s.val {
        return some(v)
    }
    return none
}

/// Drops h's value, makes every handle to it stale and returns true;
/// false, with nothing changed, for a handle that names no live value of
/// this pool. T may not be linear (E0701 at the call; in a generic
/// function, bound T by Plain). The slot is reused by a later Add, unless
/// its generation is spent after 2^32 - 1 reuses: then it is retired, so
/// a stale handle never matches again. When the arena cannot record the
/// free slot, the slot is retired too, so Free never fails. O(1).
export Pool[T].Free(mut self, h: Handle): bool {
    if !PoolMatch(&*self, h) {
        return false
    }
    if PoolSlotRef(self.slots, int(h.idx)).val.IsNone() {
        return false
    }
    if raw.IsLinear[T]() {
        raw.Fail(8, "Pool.Free would drop a linear value")
    }
    PoolVacate(mut *self, int(h.idx))
    return true
}

// PoolVacate drops the value of the occupied slot i, of a T that is not
// linear, bumps the slot's generation and puts the slot back on vacant; a
// slot whose generation is spent, or that vacant has no room for, is
// retired.
PoolVacate[T](p: mut Pool[T], i: int) {
    s := PoolSlot(p.slots, i)
    PoolSet(mut s.val, none)
    p.live -= 1
    if s.ver +% 1 == p.base {
        return // spent: retired at its last generation (P2)
    }
    s.ver = s.ver +% 1
    p.vacant.Push(Trunc[u32](i)) else {
    }
}

/// The number of live values. O(1).
export Pool[T].Len(&self): int {
    return self.live
}

/// The live values of a pool with their handles, in slot order; made by
/// Pool.Iter.
export view struct PoolIter[T] {
    pool: &Pool[T]
    i: int
}

/// Iterates the live values in slot order. O(1); a whole pass is O(n), n
/// the slots ever used. It allocates nothing.
export Pool[T].Iter(&self): PoolIter[T] from self {
    return PoolIter[T](pool: self, i: 0)
}

/// The next live value and its handle, or none after the last.
export PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool {
    for self.i < self.pool.n {
        i := self.i
        self.i = i + 1
        s := PoolSlotRef(self.pool.slots, i)
        if v := &s.val {
            return some((handle: Handle(idx: Trunc[u32](i), ver: s.ver), value: v))
        }
    }
    return none
}

/// Drops every live value whose keep returns false, as Free does, so its
/// handles go stale, and returns how many it dropped: the sweep of a
/// mark-and-sweep collector. keep may capture locals. T may not be linear
/// (E0701 at the call). n calls of keep, n the slots ever used.
export Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int {
    var dropped := 0
    for i in 0..self.n {
        s := PoolSlotRef(self.slots, i)
        var drop := false
        if v := &s.val {
            drop = !keep(Handle(idx: Trunc[u32](i), ver: s.ver), v)
        }
        if !drop {
            continue
        }
        if raw.IsLinear[T]() {
            raw.Fail(8, "Pool.Sweep would drop a linear value")
        }
        PoolVacate(mut *self, i)
        dropped += 1
    }
    return dropped
}

/// The values of h and k, both writable; none when h and k name the same
/// slot or either names no live value of this pool, never a panic. O(1).
export Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self {
    if h.idx == k.idx || !PoolMatch(&*self, h) || !PoolMatch(&*self, k) {
        return none
    }
    unsafe "h.idx != k.idx and both are below n: two distinct initialized slots of the buffer under the pool's mut loan, so the two references never alias; both carry self's loan" {
        a := mut *raw.Offset(self.slots, int(h.idx))
        b := mut *raw.Offset(self.slots, int(k.idx))
        if x := mut a.val {
            if y := mut b.val {
                return some((a: x, b: y))
            }
        }
    }
    return none
}
