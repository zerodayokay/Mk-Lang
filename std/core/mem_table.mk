// Why: the Swiss table in coll_table.mk reads control bytes in 16-byte groups
// and reaches slot fields through raw pointers; this file holds that raw
// memory access so the table algorithm needs no unsafe block.
//
// What: CtrlAt, CtrlPut, GroupAt, KeyAt, ValueAt, NewCtrl and Discard; and
// TextKeyInlineOf, TextKeyWord0 and TextKeyInlineStr, the raw access of a
// TextMap's inline keys (coll_heap.mk).
//
// How: the control array has cap + 16 bytes, the first 16 mirrored at the end
// (for cap == 8, slots 0..7 mirrored at 8..15 and 16..23 stay CtrlEmpty), so a
// 16-byte group read at any pos never wraps.

import std.raw
import std.simd

// CtrlAt is ctrl[i].
CtrlAt(ctrl: *u8, i: int): u8 {
    unsafe "the caller keeps i below cap + 16, the length of ctrl" {
        return raw.Read(raw.Offset(ctrl, i))
    }
}

// CtrlPut writes ctrl[i] = b and, when i < 16, its mirror ctrl[cap + i], so
// a 16-byte group read never wraps and the mirror is always exact.
CtrlPut(ctrl: *u8, cap: int, i: int, b: u8) {
    unsafe "the caller keeps i below cap; ctrl holds cap + 16 bytes" {
        raw.Write(raw.Offset(ctrl, i), b)
        if i < TableGroupWidth {
            raw.Write(raw.Offset(ctrl, cap +% i), b)
        }
    }
}

// GroupAt is the 8 control bytes at pos (freestanding scalar fallback).
GroupAt(ctrl: *u8, pos: int): u64 {
    unsafe "pos < cap + 8, and ctrl holds cap + 16 bytes" {
        return raw.ReadUnaligned(raw.PtrCast[u8, u64](raw.Offset(ctrl, pos)))
    }
}

// GroupAt16 is the 16 control bytes at pos as a vector (one unaligned 128-bit load).
GroupAt16(ctrl: *u8, pos: int): simd.Vec[u8, 16] {
    unsafe "pos < cap, and ctrl holds cap + 16 bytes" {
        return simd.Vec[u8, 16].FromSlice(raw.Slice(raw.Offset(ctrl, pos), 16), 0)
    }
}

// KeyAt and ValueAt are the fields of slot i.
KeyAt[K, V](slots: *Slot[K, V], i: int): *K {
    unsafe "field addresses; nothing is dereferenced" {
        return raw.Of(&(*raw.Offset(slots, i)).key)
    }
}

ValueAt[K, V](slots: *Slot[K, V], i: int): *V {
    unsafe "field addresses; nothing is dereferenced" {
        return raw.Of(&(*raw.Offset(slots, i)).value)
    }
}

// NewCtrl allocates cap + 16 control bytes, every one empty; null when a is
// out of memory.
NewCtrl(a: &Alloc, cap: int): *u8 {
    p := AllocBytes(a, cap + TableGroupWidth, TableGroupWidth)
    if raw.IsNull(p) {
        return p
    }
    unsafe "p is fresh memory for cap + 16 bytes" {
        raw.Fill(p, CtrlEmpty, cap + TableGroupWidth)
    }
    return p
}

// Discard drops a plain value, or a linear one the caller has decided to
// drop after its check.
Discard[T](x: T) {
    unsafe "the caller checked raw.IsLinear" {
        raw.Forget(x)
    }
}

// TextKeyInlineOf is the inline TextKey whose first word is w0 and whose n
// is w1: w0 is written over p's bytes, so p holds no pointer. Natively
// only (TextKeyOf). Generic over the map's value type only, as TextKeyOf,
// so the three are inlined in the caller's pack.
TextKeyInlineOf[V](w0: u64, w1: u64): TextKey {
    var k := TextKey(p: raw.Null[u8](), n: w1)
    unsafe "an inline key: its first 8 bytes are w0's, never read as a pointer" {
        raw.Write(raw.PtrCast[TextKey, u64](raw.OfMut(mut k)), w0)
    }
    return k
}

// TextKeyWord0 is the first word of the inline key at k, its bytes 0 to 7.
TextKeyWord0[K](k: &TextKey): u64 {
    unsafe "an inline key: its first 8 bytes are a word, not a pointer" {
        return raw.Read(raw.PtrCast[TextKey, u64](raw.Of(k)))
    }
}

// TextKeyInlineStr is the inline key at k as a view of its n bytes, the
// first bytes of *k, valid while *k is.
TextKeyInlineStr[K](k: &TextKey, n: int): str {
    unsafe "an inline key: its n bytes, valid UTF-8 copied from a str, are the first bytes of *k" {
        return StrOf(raw.PtrCast[TextKey, u8](raw.Of(k)), n)
    }
}
