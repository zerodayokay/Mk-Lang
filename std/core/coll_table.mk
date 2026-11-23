// Why: Map, Set, HeapMap and TextMap differ only in where their memory comes
// from and who owns the keys; one hash table under all four keeps the
// probing, growth and iteration order identical and tested once.
//
// What: Table[K, V] and Slot, the probe and mask helpers (EmptyMask,
// FreeMask, MatchMask, H1, H2), hashing (HashOf, TableHash), and the
// operations TableFind, TableLookup, TablePlace, TableReserve, TableClaim,
// TableErase, TableResize, TableRehashInPlace and TableNext.
//
// How: a Swiss table. ctrl has cap + 8 bytes: 0xFF empty, 0x80 deleted,
// 0x00..0x7F full holding h2, the hash's top 7 bits; the first 8 are repeated
// at the end, so a group read at any pos never wraps. growth counts the empty
// slots that may still be filled before a rehash. Operations take the full
// 64-bit hash, so the container computes it once per call and a test can
// build exact probe layouts. A table hashes with the fold hash (New) or with
// SipHash-1-3 under its own key (NewKeyed); keyed says which. The raw memory
// is mem_table.mk's; this file needs no unsafe block.

import std.raw
import std.simd

struct Slot[K, V] {
    key: K
    value: V
}

struct Table[K, V] {
    ctrl: *u8
    slots: *Slot[K, V]
    used: int
    cap: int
    growth: int
    k0: u64
    k1: u64
    keyed: bool
}

const CtrlEmpty: u8 = 0xFF
const CtrlDeleted: u8 = 0x80
const GroupLsb: u64 = 0x0101010101010101
const GroupMsb: u64 = 0x8080808080808080

// EmptyTable is a table of capacity 0 whose every hash is the fold hash
// (New's tables); it allocates nothing.
EmptyTable[K, V](): Table[K, V] {
    return Table[K, V](ctrl: raw.Null[u8](), slots: raw.Null[Slot[K, V]](), used: 0, cap: 0, growth: 0, k0: 0, k1: 0, keyed: false)
}

// EmptyTableKeyed is a table of capacity 0 whose every hash is SipHash-1-3
// under key (NewKeyed's tables), whatever the key; it allocates
// nothing.
EmptyTableKeyed[K, V](key: HashKey): Table[K, V] {
    return Table[K, V](ctrl: raw.Null[u8](), slots: raw.Null[Slot[K, V]](), used: 0, cap: 0, growth: 0, k0: key.k0, k1: key.k1, keyed: true)
}

Table[K, V].Count(&self): int {
    return self.used
}

// The exact group masks, one bit 7 per byte. A byte is empty when bits 7
// and 6 are both set, which only 0xFF has (the bit shifted out of a byte is
// masked off); empty or deleted when bit 7 is set. MatchMask may report a
// false h2 match, which costs one Eq; EmptyMask never errs.
EmptyMask(g: u64): u64 {
    return g & (g << 1) & GroupMsb
}

FreeMask(g: u64): u64 {
    return g & GroupMsb
}

MatchMask(g: u64, h2: u8): u64 {
    x := g ^ (u64(h2) * GroupLsb)
    return (x -% GroupLsb) & ~x & GroupMsb
}

// H1 picks the probe start, H2 the control byte of a full slot.
H1(hash: u64): int {
    return Trunc[int](hash >> 7)
}

H2(hash: u64): u8 {
    return Trunc[u8](hash >> 57)
}

// HashOf is k's fold hash, the hash of every table New makes:
// NewHasher, k.Hash, HashFinish. A str key takes FoldStr, a TextMap's
// TextKey FoldStr of its text (TextKeyFold in coll_heap.mk), and every
// integer key, char and bool FoldU64K, the same values in straight line
// (the value their Hash impl in core_impls.mk gives: U64 of the value,
// sign-extended for signed types, a char's scalar, a bool's 0 or 1), as
// HashKeyed does for SipHash. Any other key (a struct, an enum, a derived
// Hash) runs its Hash impl on a fold Hasher made and finished here, in the
// caller's pack, so the only calls left are the impl's own.
HashOf[K: Hash](k: &K): u64 {
    if K is str {
        return FoldStr[K](*k)
    }
    if K is TextKey {
        return TextKeyFold[K](k)
    }
    if K is int {
        return FoldU64K[K](Trunc[u64](i64(*k)))
    }
    if K is i64 {
        return FoldU64K[K](Trunc[u64](*k))
    }
    if K is i32 {
        return FoldU64K[K](Trunc[u64](i64(*k)))
    }
    if K is i16 {
        return FoldU64K[K](Trunc[u64](i64(*k)))
    }
    if K is i8 {
        return FoldU64K[K](Trunc[u64](i64(*k)))
    }
    if K is u64 {
        return FoldU64K[K](*k)
    }
    if K is uint {
        return FoldU64K[K](u64(*k))
    }
    if K is u32 {
        return FoldU64K[K](u64(*k))
    }
    if K is u16 {
        return FoldU64K[K](u64(*k))
    }
    if K is u8 {
        return FoldU64K[K](u64(*k))
    }
    if K is char {
        return FoldU64K[K](u64(u32(*k)))
    }
    if K is bool {
        if *k {
            return FoldU64K[K](1)
        }
        return FoldU64K[K](0)
    }
    var h := Hasher(v0: FoldF0, v1: 0, v2: FoldF4, v3: 0, tail: 0, ntail: 0, total: 0, sip: false, half: false)
    k.Hash(mut h)
    return FoldFinishK[K](&h)
}

// TableHash is k's hash in t: the fold hash for New's tables, SipHash-1-3
// under the table's HashKey for NewKeyed's. Every hash of a table
// is this one. keyed never changes after the table is made, so the branch
// is predicted.
TableHash[K: Hash, V](t: &Table[K, V], k: &K): u64 {
    if t.keyed {
        return HashKeyed(t.k0, t.k1, k)
    }
    return HashOf(k)
}

// HashKeyed is k's SipHash-1-3 hash under (k0, k1): NewHasherKeyed, k.Hash,
// HashFinish. A str key takes SipStrKeyed, the same value with the state in
// registers and one align 1 load per word (str's Hash impl in
// core_impls.mk is fixed: the bytes, then 0xFF). Every integer key, char
// and bool takes SipU64K, the value its Hash impl gives (U64 of the value,
// sign-extended for signed types): with the key a table field rather than
// a constant, the Hash impl path leaves two calls per lookup in release,
// and SipU64K is inlined.
HashKeyed[K: Hash](k0: u64, k1: u64, k: &K): u64 {
    if K is str {
        return SipStrKeyed(k0, k1, *k)
    }
    if K is TextKey {
        return SipStrKeyed(k0, k1, TextKeyStr[K](k))
    }
    if K is int {
        return SipU64K[K](k0, k1, Trunc[u64](i64(*k)))
    }
    if K is i64 {
        return SipU64K[K](k0, k1, Trunc[u64](*k))
    }
    if K is i32 {
        return SipU64K[K](k0, k1, Trunc[u64](i64(*k)))
    }
    if K is u64 {
        return SipU64K[K](k0, k1, *k)
    }
    if K is uint {
        return SipU64K[K](k0, k1, u64(*k))
    }
    if K is u32 {
        return SipU64K[K](k0, k1, u64(*k))
    }
    if K is i16 {
        return SipU64K[K](k0, k1, Trunc[u64](i64(*k)))
    }
    if K is i8 {
        return SipU64K[K](k0, k1, Trunc[u64](i64(*k)))
    }
    if K is u16 {
        return SipU64K[K](k0, k1, u64(*k))
    }
    if K is u8 {
        return SipU64K[K](k0, k1, u64(*k))
    }
    if K is char {
        return SipU64K[K](k0, k1, u64(u32(*k)))
    }
    if K is bool {
        if *k {
            return SipU64K[K](k0, k1, 1)
        }
        return SipU64K[K](k0, k1, 0)
    }
    var h := NewHasherKeyed(k0, k1)
    k.Hash(mut h)
    return HashFinish(mut h)
}

// SipU64K is the SipHash-1-3 hash under (k0, k1) of the 8 bytes of x: the
// compression of x, then of the last word (the length 8 on top), then
// d = 3 rounds, NewHasherKeyed, U64(x) and HashFinish in straight line.
// Generic over the key type only, so it is instantiated, and inlined, in
// the caller's pack, as a non-generic std function is not.
SipU64K[K](k0: u64, k1: u64, x: u64): u64 {
    last: u64 = 8 << 56
    var v0 := 0x736f6d6570736575 ^ k0
    var v1 := 0x646f72616e646f6d ^ k1
    var v2 := 0x6c7967656e657261 ^ k0
    var v3 := 0x7465646279746573 ^ k1 ^ x
    for r in 0..5 {
        if r == 1 {
            v0 = v0 ^ x
            v3 = v3 ^ last
        } else if r == 2 {
            v0 = v0 ^ last
            v2 = v2 ^ 0xFF
        }
        v0 = v0 +% v1
        v1 = raw.RotL64(v1, 13)
        v1 = v1 ^ v0
        v0 = raw.RotL64(v0, 32)
        v2 = v2 +% v3
        v3 = raw.RotL64(v3, 16)
        v3 = v3 ^ v2
        v0 = v0 +% v3
        v3 = raw.RotL64(v3, 21)
        v3 = v3 ^ v0
        v2 = v2 +% v1
        v1 = raw.RotL64(v1, 17)
        v1 = v1 ^ v2
        v2 = raw.RotL64(v2, 32)
    }
    return v0 ^ v1 ^ v2 ^ v3
}

// KeyEq is a.Eq(b), the comparison of every probe. A str key compares
// eight bytes at a time (StrEq), the value str's Eq impl gives, because
// str's == lowers to the support pack's memcmp, which reads one byte per
// step; a TextKey compares its two words first (TextKeyEq).
KeyEq[K: Eq](a: &K, b: &K): bool {
    if K is str {
        return StrEq[K](*a, *b)
    }
    if K is TextKey {
        return TextKeyEq[K](a, b)
    }
    return a.Eq(b)
}

// StrEq is a == b: the lengths, then the words of a and b, the last one
// overlapping the one before when the length is not a multiple of 8; under
// 8 bytes the fixed loads of FoldStr's short read, compared in place. Over
// 16 bytes, where the loop would run, two views of the same bytes are equal
// with no read (as Zig's mem.eql): a lookup by the very view stored, as
// when keys are views of one buffer, skips the compare. The
// index arithmetic wraps because 0 <= i < n holds, so it never does.
// Generic over the key type only, so it is instantiated, and can be
// inlined, in the caller's pack (FoldU64K).
StrEq[K](a: str, b: str): bool {
    n := a.Len()
    if n != b.Len() {
        return false
    }
    if n >= 8 {
        if n > 16 && raw.Same(raw.StrData(a), raw.StrData(b)) {
            return true
        }
        var i := 0
        for n -% i > 8 {
            if LoadU64(a, i) != LoadU64(b, i) {
                return false
            }
            i = i +% 8
        }
        return LoadU64(a, n -% 8) == LoadU64(b, n -% 8)
    }
    x := a.AsBytes()
    y := b.AsBytes()
    if n >= 4 {
        j := n -% 4
        xl := u32(x[0]) | (u32(x[1]) << 8) | (u32(x[2]) << 16) | (u32(x[3]) << 24)
        yl := u32(y[0]) | (u32(y[1]) << 8) | (u32(y[2]) << 16) | (u32(y[3]) << 24)
        xh := u32(x[j]) | (u32(x[j +% 1]) << 8) | (u32(x[j +% 2]) << 16) | (u32(x[j +% 3]) << 24)
        yh := u32(y[j]) | (u32(y[j +% 1]) << 8) | (u32(y[j +% 2]) << 16) | (u32(y[j +% 3]) << 24)
        return xl == yl && xh == yh
    }
    if n == 0 {
        return true
    }
    m := n >> 1
    e := n -% 1
    return x[0] == y[0] && x[m] == y[m] && x[e] == y[e]
}

// TableFind is the slot of the key equal to k, whose hash is hash, or -1.
// 16-byte groups at pos, pos + 16, pos + 48, ... modulo cap (triangular);
// an empty byte in a group ends the probe.
TableFind[K: Hash + Eq, V](t: &Table[K, V], hash: u64, k: &K): int {
    if t.cap == 0 {
        return -1
    }
    if Target.os != Os.Freestanding {
        return TableFindSimd(t, hash, k)
    }
    mask := t.cap -% 1
    h2 := H2(hash)
    var pos := H1(hash) & mask
    var stride := 0
    for {
        lo := GroupAt(t.ctrl, pos)
        var m := MatchMask(lo, h2)
        for m != 0 {
            i := (pos +% (raw.Ctz64(m) >> 3)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return i
            }
            m = m & (m -% 1)
        }
        hi := GroupAt(t.ctrl, pos +% 8)
        m = MatchMask(hi, h2)
        for m != 0 {
            i := (pos +% 8 +% (raw.Ctz64(m) >> 3)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return i
            }
            m = m & (m -% 1)
        }
        if (EmptyMask(lo) | EmptyMask(hi)) != 0 {
            return -1
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

TableFindSimd[K: Hash + Eq, V](t: &Table[K, V], hash: u64, k: &K): int {
    mask := t.cap -% 1
    vh2 := simd.Vec[u8, 16].Splat(H2(hash))
    vemp := simd.Vec[u8, 16].Splat(CtrlEmpty)
    var pos := H1(hash) & mask
    var stride := 0
    for {
        g := GroupAt16(t.ctrl, pos)
        var m := (g == vh2).Bits()
        for m != 0 {
            i := (pos +% raw.Ctz64(m)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return i
            }
            m = m & (m -% 1)
        }
        if (g == vemp).Bits() != 0 {
            return -1
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

// FirstFree is the first empty or deleted slot on hash's probe path; the
// table has capacity and, by the growth rule, at least one empty slot.
FirstFree[K, V](t: &Table[K, V], hash: u64): int {
    if Target.os != Os.Freestanding {
        return FirstFreeSimd(t, hash)
    }
    mask := t.cap -% 1
    var pos := H1(hash) & mask
    var stride := 0
    for {
        m := FreeMask(GroupAt(t.ctrl, pos))
        if m != 0 {
            return (pos +% (raw.Ctz64(m) >> 3)) & mask
        }
        m2 := FreeMask(GroupAt(t.ctrl, pos +% 8))
        if m2 != 0 {
            return (pos +% 8 +% (raw.Ctz64(m2) >> 3)) & mask
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

FirstFreeSimd[K, V](t: &Table[K, V], hash: u64): int {
    mask := t.cap -% 1
    vdel := simd.Vec[u8, 16].Splat(CtrlDeleted)
    var pos := H1(hash) & mask
    var stride := 0
    for {
        m := (GroupAt16(t.ctrl, pos) >= vdel).Bits()
        if m != 0 {
            return (pos +% raw.Ctz64(m)) & mask
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

// TableLookup is TableFind for an insert: at is the slot of the key equal
// to k, or -1; when the key is absent, free is the first empty or deleted
// slot on its probe path, or -1 at capacity 0.
TableLookup[K: Hash + Eq, V](t: &Table[K, V], hash: u64, k: &K): (at: int, free: int) {
    if t.cap == 0 {
        return (at: -1, free: -1)
    }
    if Target.os != Os.Freestanding {
        return TableLookupSimd(t, hash, k)
    }
    mask := t.cap -% 1
    h2 := H2(hash)
    var pos := H1(hash) & mask
    var stride := 0
    var free := -1
    for {
        lo := GroupAt(t.ctrl, pos)
        var m := MatchMask(lo, h2)
        for m != 0 {
            i := (pos +% (raw.Ctz64(m) >> 3)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return (at: i, free: -1)
            }
            m = m & (m -% 1)
        }
        hi := GroupAt(t.ctrl, pos +% 8)
        m = MatchMask(hi, h2)
        for m != 0 {
            i := (pos +% 8 +% (raw.Ctz64(m) >> 3)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return (at: i, free: -1)
            }
            m = m & (m -% 1)
        }
        if free < 0 {
            f := FreeMask(lo)
            if f != 0 {
                free = (pos +% (raw.Ctz64(f) >> 3)) & mask
            } else {
                f2 := FreeMask(hi)
                if f2 != 0 {
                    free = (pos +% 8 +% (raw.Ctz64(f2) >> 3)) & mask
                }
            }
        }
        if (EmptyMask(lo) | EmptyMask(hi)) != 0 {
            return (at: -1, free: free)
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

TableLookupSimd[K: Hash + Eq, V](t: &Table[K, V], hash: u64, k: &K): (at: int, free: int) {
    mask := t.cap -% 1
    vh2 := simd.Vec[u8, 16].Splat(H2(hash))
    vemp := simd.Vec[u8, 16].Splat(CtrlEmpty)
    vdel := simd.Vec[u8, 16].Splat(CtrlDeleted)
    var pos := H1(hash) & mask
    var stride := 0
    var free := -1
    for {
        g := GroupAt16(t.ctrl, pos)
        var m := (g == vh2).Bits()
        for m != 0 {
            i := (pos +% raw.Ctz64(m)) & mask
            if KeyEq(SlotRef(KeyAt(t.slots, i), 0), k) {
                return (at: i, free: -1)
            }
            m = m & (m -% 1)
        }
        if free < 0 {
            f := (g >= vdel).Bits()
            if f != 0 {
                free = (pos +% raw.Ctz64(f)) & mask
            }
        }
        if (g == vemp).Bits() != 0 {
            return (at: -1, free: free)
        }
        stride = stride +% 16
        pos = (pos +% stride) & mask
    }
}

// TablePlace claims a slot for an absent key with this hash and returns
// it, or -1 when a is out of memory, and then the table is unchanged. free
// is TableLookup's: TablePlaceKind decides whether that slot is claimed at
// once, or the table rehashes in place or grows first, and only growth
// allocates. Every allocation happens before the caller moves the key and
// value in, so on failure the caller still owns them.
TablePlace[K: Hash + Eq, V](t: mut Table[K, V], a: &Alloc, free: bool, hash: u64, slot: int): int {
    kind := TablePlaceKind(&*t, slot)
    if kind == PlaceClaim {
        return TableClaimAt(t, slot, hash)
    }
    if kind == PlaceRehash {
        TableRehashInPlace(t)
    } else if !TableResize(t, a, free, IntMax(8, t.cap * 2)) {
        return -1
    }
    return TableClaim(t, hash)
}

// What TablePlace does with TableLookup's free slot: claim it at once,
// rehash in place first (no allocation), or grow first (the one case that
// allocates). HeapMap and TextMap make the owner check exactly when
// the kind is PlaceGrow, so the decision is made here once.
const PlaceClaim: int = 0
const PlaceRehash: int = 1
const PlaceGrow: int = 2

// TablePlaceKind decides an insert at slot, the first free slot
// on the key's probe path (TableLookup gives one whenever cap > 0): a
// deleted slot, or an empty one while growth is left, is claimed; else at
// most cap·7/16 used slots rehash in place and more double the capacity
// (8 from capacity 0).
TablePlaceKind[K, V](t: &Table[K, V], slot: int): int {
    if t.cap == 0 {
        return PlaceGrow
    }
    if slot >= 0 && (t.growth > 0 || CtrlAt(t.ctrl, slot) == CtrlDeleted) {
        return PlaceClaim
    }
    if t.used <= t.cap * 7 / 16 {
        return PlaceRehash
    }
    return PlaceGrow
}

// TableClaimAt marks slot i, empty or deleted, full with hash's h2: a
// deleted slot is reused with growth unchanged; an empty one uses up one of
// growth.
TableClaimAt[K, V](t: mut Table[K, V], i: int, hash: u64): int {
    if CtrlAt(t.ctrl, i) == CtrlEmpty {
        t.growth -= 1
    }
    CtrlPut(t.ctrl, t.cap, i, H2(hash))
    t.used += 1
    return i
}

// TableReserve makes the insert of an absent key with this hash possible
// without a rehash, allocating from a when it must; false when a is out of
// memory, and then the table is unchanged. free says whether old arrays go
// back to a (heap tables) or are abandoned to it (arena tables). Every
// allocation happens here, before the caller moves the key and value,
// so on failure the caller still owns them.
TableReserve[K: Hash + Eq, V](t: mut Table[K, V], a: &Alloc, free: bool, hash: u64): bool {
    slot := if t.cap > 0 { FirstFree(&*t, hash) } else { -1 }
    // TablePlaceKind is the one place this is decided: mostly
    // tombstones rehash in place with no allocation; else double.
    kind := TablePlaceKind(&*t, slot)
    if kind == PlaceClaim {
        return true
    }
    if kind == PlaceRehash {
        TableRehashInPlace(t)
        return true
    }
    return TableResize(t, a, free, IntMax(8, t.cap * 2))
}

// TableClaim marks the slot an absent key with this hash goes to as full
// and returns it; TableReserve came first. A deleted slot is reused with
// growth unchanged; an empty one uses up one of growth.
TableClaim[K, V](t: mut Table[K, V], hash: u64): int {
    return TableClaimAt(t, FirstFree(&*t, hash), hash)
}

// TableErase marks slot i, whose key and value the caller has moved out,
// as no longer full (Abseil's EraseMetaOnly): empty when the run of
// non-empty bytes through i is shorter than 16, so no probe can have passed
// through it, which gives one of growth back; else deleted.
TableErase[K, V](t: mut Table[K, V], i: int) {
    mask := t.cap -% 1
    before := (i -% 16) & mask
    empty := if Target.os != Os.Freestanding {
        TableEraseShortSimd(t.ctrl, i, before)
    } else {
        lo_a := EmptyMask(GroupAt(t.ctrl, i))
        after := if lo_a != 0 { raw.Ctz64(lo_a) >> 3 } else { 8 +% (raw.Ctz64(EmptyMask(GroupAt(t.ctrl, i +% 8))) >> 3) }
        hi_b := EmptyMask(GroupAt(t.ctrl, before +% 8))
        prior := if hi_b != 0 { raw.Clz64(hi_b) >> 3 } else { 8 +% (raw.Clz64(EmptyMask(GroupAt(t.ctrl, before))) >> 3) }
        after +% prior < 16
    }
    if empty {
        CtrlPut(t.ctrl, t.cap, i, CtrlEmpty)
        t.growth += 1
    } else {
        CtrlPut(t.ctrl, t.cap, i, CtrlDeleted)
    }
    t.used -= 1
}

TableEraseShortSimd(ctrl: *u8, i: int, before: int): bool {
    vemp := simd.Vec[u8, 16].Splat(CtrlEmpty)
    after := raw.Ctz64((GroupAt16(ctrl, i) == vemp).Bits())
    prior := raw.Clz64((GroupAt16(ctrl, before) == vemp).Bits() << 48)
    return after +% prior < 16
}

// TableResize moves every key to new arrays of newcap slots: the
// old arrays are freed to a when free is set, else abandoned to the arena.
TableResize[K: Hash + Eq, V](t: mut Table[K, V], a: &Alloc, free: bool, newcap: int): bool {
    size := SizeOfT[Slot[K, V]]()
    align := AlignOfT[Slot[K, V]]()
    // A slot array whose byte count would overflow int is out of memory, as
    // in GrowSlots, never a wrapped size.
    if newcap > GrowLimit - 16 || (size > 0 && newcap > GrowLimit / size) {
        return false
    }
    ctrl := NewCtrl(a, newcap)
    if raw.IsNull(ctrl) {
        return false
    }
    var slots := Dangling[Slot[K, V]]()
    if size > 0 {
        p := AllocBytes(a, newcap * size, align)
        if raw.IsNull(p) {
            FreeBytes(a, ctrl, newcap + 16, 16)
            return false
        }
        slots = BytesAs[Slot[K, V]](p)
    }
    var nt := Table[K, V](ctrl: ctrl, slots: slots, used: t.used, cap: newcap, growth: newcap * 7 / 8 - t.used, k0: t.k0, k1: t.k1, keyed: t.keyed)
    for i in 0..t.cap {
        if CtrlAt(t.ctrl, i) < CtrlDeleted {
            hash := TableHash(&*t, SlotRef(KeyAt(t.slots, i), 0))
            j := FirstFree(&nt, hash)
            CtrlPut(nt.ctrl, newcap, j, H2(hash))
            SlotCopy(SlotAt(nt.slots, j), SlotAt(t.slots, i), 1) // a move: the old slot is abandoned
        }
    }
    if free {
        TableFreeArrays(&*t, a)
    }
    *t = nt
    return true
}

// TableRehashInPlace rehashes at the same capacity with no
// allocation (Abseil's DropDeletesWithoutResize): deleted bytes become
// empty and full ones deleted, "to place"; each slot to place, in index
// order, goes to the first empty or to-place slot j on its probe path: it
// stays when i and j are in the same probe group, moves when j is empty,
// and swaps with j when j is still to place, after which i is processed
// again.
TableRehashInPlace[K: Hash + Eq, V](t: mut Table[K, V]) {
    cap := t.cap
    mask := cap - 1
    for i in 0..cap {
        c := CtrlAt(t.ctrl, i)
        if c == CtrlDeleted {
            CtrlPut(t.ctrl, cap, i, CtrlEmpty)
        } else if c < CtrlDeleted {
            CtrlPut(t.ctrl, cap, i, CtrlDeleted)
        }
    }
    var i := 0
    for i < cap {
        if CtrlAt(t.ctrl, i) != CtrlDeleted {
            i += 1
            continue
        }
        hash := TableHash(&*t, SlotRef(KeyAt(t.slots, i), 0))
        p := H1(hash) & mask
        j := FirstFree(&*t, hash)
        if ((i - p) & mask) >> 4 == ((j - p) & mask) >> 4 {
            CtrlPut(t.ctrl, cap, i, H2(hash))
            i += 1
            continue
        }
        if CtrlAt(t.ctrl, j) == CtrlEmpty {
            SlotCopy(SlotAt(t.slots, j), SlotAt(t.slots, i), 1)
            CtrlPut(t.ctrl, cap, j, H2(hash))
            CtrlPut(t.ctrl, cap, i, CtrlEmpty)
            i += 1
        } else {
            SlotSwap(t.slots, i, j)
            CtrlPut(t.ctrl, cap, j, H2(hash))
        }
    }
    t.growth = cap * 7 / 8 - t.used
}

// TableNext is the first full slot at or after i, or -1 (index order).
TableNext[K, V](t: &Table[K, V], i: int): int {
    var j := i
    for t.cap - j >= 8 {
        full := ~GroupAt(t.ctrl, j) & GroupMsb
        if full != 0 {
            return j +% (raw.Ctz64(full) >> 3)
        }
        j = j +% 8
    }
    for j < t.cap {
        if CtrlAt(t.ctrl, j) < CtrlDeleted {
            return j
        }
        j = j +% 1
    }
    return -1
}

// TableFreeArrays returns a heap table's arrays to a; its keys and values
// are the caller's.
TableFreeArrays[K, V](t: &Table[K, V], a: &Alloc) {
    if t.cap == 0 {
        return
    }
    FreeBytes(a, t.ctrl, t.cap + 16, 16)
    FreeSlots(a, t.slots, t.cap)
}
