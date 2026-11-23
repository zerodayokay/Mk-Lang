// Why: sets of small integers (visited nodes, flags) need one bit per member
// and constant-time tests, without a heap allocation per set.
//
// What: Bitset, a fixed number of bits in 64-bit words borrowed from an
// arena, with New, Set, Remove, Has, Count and Len.
//
// How: bit i lives in word i >> 6 at bit i & 63, and the bits past Len() in
// the last word stay zero, so Count can add whole words. The words are
// reached through mem_slots.mk's helpers, so the one unsafe block here takes
// the arena slice's data pointer. The layout follows Rust's fixedbitset and
// Zig's std.DynamicBitSet.

import std.raw

/// A set of small integers: a fixed number of bits, numbered from 0 to
/// Len() - 1 and all clear at New, in 64-bit words in an arena. Set,
/// Remove and Has are O(1); Count is O(Len() / 64).
export view struct Bitset {
    ptr: *u64
    bits: int
    alloc: &Alloc
}

// BitWords is the number of words that hold bits bits: ceil(bits / 64).
BitWords(bits: int): int {
    if bits & 63 != 0 {
        return (bits >> 6) + 1
    }
    return bits >> 6
}

/// A Bitset of bits bits, all clear. Fails with OutOfMemory when a cannot
/// give the ceil(bits / 64) words; panics (code 2) when bits < 0.
/// O(bits / 64); allocates the words, zero-filled, from a.
export Bitset.New(a: &Arena, bits: int): OutOfMemory!Bitset {
    if bits < 0 {
        raw.Fail(2, "Bitset.New: negative length")
    }
    s := try a.Slice[u64](BitWords(bits))
    var p := raw.Null[u64]()
    unsafe "the slice's data pointer; the arena keeps the words alive while the Bitset holds its loan" {
        p = raw.DataMut(s)
    }
    return Bitset(ptr: p, bits: bits, alloc: a.Alloc())
}

// BitWord is the index of the word holding bit i, after the bounds check
// (code 2); op names the caller in the panic. The index is below
// BitWords(b.bits), so it is a word New allocated.
BitWord(b: &Bitset, i: int, op: str): int {
    if i < 0 || i >= b.bits {
        raw.Fail(2, op)
    }
    return i >> 6
}

/// Sets bit i; panics (code 2) unless 0 <= i < Len(). O(1).
export Bitset.Set(mut self, i: int) {
    w := SlotMut(self.ptr, BitWord(&*self, i, "Bitset.Set: index out of range"))
    *w = *w | (u64(1) << (i & 63))
}

/// Clears bit i; panics (code 2) unless 0 <= i < Len(). O(1).
export Bitset.Remove(mut self, i: int) {
    w := SlotMut(self.ptr, BitWord(&*self, i, "Bitset.Remove: index out of range"))
    *w = *w & ~(u64(1) << (i & 63))
}

/// Whether bit i is set; panics (code 2) unless 0 <= i < Len(). O(1).
export Bitset.Has(&self, i: int): bool {
    w := SlotRef(self.ptr, BitWord(self, i, "Bitset.Has: index out of range"))
    return (*w >> (i & 63)) & 1 != 0
}

/// The number of set bits, counted a word at a time. O(Len() / 64);
/// allocates nothing.
export Bitset.Count(&self): int {
    var c := 0
    for k in 0..BitWords(self.bits) {
        c += raw.Popcount64(*SlotRef(self.ptr, k))
    }
    return c
}

/// The number of bits, set or not, fixed at New. O(1).
export Bitset.Len(&self): int {
    return self.bits
}
