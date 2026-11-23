// Why: Map, Set and the heap maps sit on one Swiss table; probe layouts,
// tombstones, rehashing and the hash functions must all agree with models.
// What: test blocks for Map, Set, HeapMap, TextMap, Hasher, HashKeyed, the
// fold hash and the table internals (TableFind, TableLookup, TableResize).
// How: run by `mk test --std=std.core` with the counting heap (a leak fails
// with exit 102); TmChosen and TmClaim build exact probe layouts. The SipHash
// vectors come from the Go SipHash-1-3 of std/std-coll_keyed_test.go (sip13k)
// and, for the key (0, 0), from Rust's DefaultHasher; the fold hash vectors
// from the Go model foldHash of std/std-hash_model_test.go. The rows that
// panic are whole programs in std/std-coll_linear_test.go and
// std-coll_owner_test.go. Helper names start with Tm, because every file of
// std/core is one module.

import std.raw

// TmChosen is a hash whose probe starts at pos (H1, bits 7 and up) with the
// tag tag (H2, the top seven bits), for exact probe layouts.
TmChosen(pos: int, tag: int): u64 {
    return (Trunc[u64](pos) << 7) | (Trunc[u64](tag) << 57)
}

// TmClaim places key k (value k) at the slot an absent key with this hash
// takes; the table has room (TableReserve came first).
TmClaim(t: mut Table[int, int], hash: u64, k: int): int {
    i := TableClaim(t, hash)
    SlotWrite(KeyAt(t.slots, i), 0, k)
    SlotWrite(ValueAt(t.slots, i), 0, k)
    return i
}

TmNext(s: mut u64): int {
    *s = *s *% 6364136223846793005 +% 1442695040888963407
    return Trunc[int](*s >> 33)
}

// TmOrder is the keys of m in iteration order, folded (FNV-1a).
TmOrder(m: &Map[int, int]): u64 {
    var h: u64 = 14695981039346656037
    for e in m.Iter() {
        h = (h ^ Trunc[u64](*e.key)) *% 1099511628211
    }
    return h
}

test "Set inserts, and on a present key replaces the value" (sys: &System) {
    var m := Map[int, int].New(sys.arena)
    Assert(m.Len() == 0 && m.Get(&1).IsNone())
    try m.Set(1, 10)
    try m.Set(2, 20)
    try m.Set(1, 11)
    Assert(m.Len() == 2 && *m.Get(&1).Must() == 11 && *m.Get(&2).Must() == 20)
}

test "GetMut writes through and lookups never panic" (sys: &System) {
    var m := Map[str, int].New(sys.arena)
    Assert(m.GetMut(&"a").IsNone())
    try m.Set("a", 1)
    *m.GetMut(&"a").Must() += 41
    Assert(*m.Get(&"a").Must() == 42 && m.Get(&"").IsNone())
    try m.Set("", 7)
    Assert(*m.Get(&"").Must() == 7 && m.Has(&"") && !m.Has(&"b"))
}

test "Ensure inserts when absent and keeps a present value" (sys: &System) {
    var m := Map[int, int].New(sys.arena)
    v := try m.Ensure(5, 1)
    *v += 1
    w := try m.Ensure(5, 100)
    Assert(*w == 2 && m.Len() == 1)
}

test "Remove returns the value, none when absent" (sys: &System) {
    var m := Map[int, int].New(sys.arena)
    for i in 0..1_000 {
        try m.Set(i, i * 2)
    }
    for i in 0..1_000 {
        if i % 3 == 0 {
            Assert(m.Remove(&i).Must() == i * 2)
        }
    }
    Assert(m.Remove(&0).IsNone() && m.Remove(&5_000).IsNone())
    for i in 0..1_000 {
        Assert(m.Has(&i) == (i % 3 != 0))
    }
    Assert(m.Len() == 666)
}

// MapIter.Next's documented cost: removal only marks slots, so the table
// keeps its capacity, and the walk to the one live entry left reads up to
// capacity / 8 control groups.
test "removal keeps a map's capacity, so a sparse map's Next walks it" (sys: &System) {
    var m := Map[int, int].New(sys.arena)
    for i in 0..10_000 {
        try m.Set(i, i)
    }
    cap := m.t.cap
    Assert(cap >= 10_000)
    for i in 0..9_999 {
        Assert(m.Remove(&i).Must() == i)
    }
    Assert(m.t.cap == cap && m.Len() == 1)
    var it := m.Iter()
    e := it.Next().Must()
    Assert(*e.key == 9_999 && *e.value == 9_999)
    Assert(it.Next().IsNone())
}

test "iteration after removals visits every live entry once" (sys: &System) {
    var m := Map[int, int].New(sys.arena)
    var s: u64 = 9
    for _ in 0..20_000 {
        k := TmNext(mut s) % 700
        if TmNext(mut s) % 3 == 0 {
            _ = m.Remove(&k)
        } else {
            try m.Set(k, k + 1)
        }
    }
    var seen := Set[int].New(sys.arena)
    var n := 0
    for e in m.Iter() {
        Assert(*e.value == *e.key + 1)
        Assert(try seen.Add(*e.key))
        n += 1
    }
    Assert(n == m.Len())
    for k in 0..700 {
        Assert(m.Has(&k) == seen.Has(&k))
    }
}

test "Set adds, finds and removes members" (sys: &System) {
    var s := Set[str].New(sys.arena)
    Assert(try s.Add("x"))
    Assert(!(try s.Add("x")))
    Assert(try s.Add(""))
    Assert(s.Has(&"x") && s.Has(&"") && !s.Has(&"y"))
    Assert(s.Remove(&"x") && !s.Remove(&"x") && !s.Has(&"x"))
}

// TmFreeKey is key i of the free test: "the long key number i" when 3
// divides i, else "k" and i.
TmFreeKey(ar: &Arena, i: int): OutOfMemory!str from ar {
    if i % 3 == 0 {
        return try ar.Fmt("the long key number {i}", (i))
    }
    return try ar.Fmt("k{i}", (i))
}

test "HeapMap and TextMap free every byte" (sys: &System) {
    a := sys.heap.Alloc()
    var h := HeapMap[int, int].New(a)
    defer h.Free(a)
    var t := TextMap[int].New(a)
    defer t.Free(a)
    // Every third key is 16 bytes or more, so the map copies it; the
    // rest it keeps in their slots.
    for i in 0..3_000 {
        try h.Set(a, i, i)
        key := try TmFreeKey(sys.arena, i)
        v := try t.Ensure(a, key, i)
        *v += 1
    }
    for i in 0..3_000 {
        if i % 2 == 0 {
            Assert(h.Remove(&i).Must() == i)
            key := try TmFreeKey(sys.arena, i)
            Assert(t.Remove(a, key).Must() == i + 1)
        }
    }
    Assert(h.Len() == 1_500 && t.Len() == 1_500 && *t.Get("k2999").Must() == 3_000)
    Assert(*t.Get("the long key number 2997").Must() == 2_998)
}

test "NewHasher gives the fold hash" (sys: &System) {
    var b: [64]u8 = [0; 64]
    for i in 0..64 {
        b[i] = Trunc[u8](i)
    }
    ns: [7]int = [0, 1, 7, 8, 15, 16, 63]
    want: [7]u64 = [12733740897008279252, 10099875023774034710, 2310436164859541822, 2141061075543806254, 1690340452080105924, 14185856542041807614, 807221478985217149]
    for i in 0..7 {
        var h := NewHasher()
        h.Bytes(b[0..ns[i]])
        Assert(HashFinish(mut h) == want[i])
        // The same bytes in two pieces.
        var g := NewHasher()
        g.Bytes(b[0..ns[i] / 2])
        g.Bytes(b[ns[i] / 2..ns[i]])
        Assert(HashFinish(mut g) == want[i])
    }
}

test "a HashKey keys the hash: SipHash-1-3 under bytes 0x10 to 0x1f" (sys: &System) {
    var kb: [16]u8 = [0; 16]
    var b: [64]u8 = [0; 64]
    for i in 0..64 {
        b[i] = Trunc[u8](i)
        if i < 16 {
            kb[i] = Trunc[u8](i + 0x10)
        }
    }
    key := HashKeyOf(kb[..])
    Assert(key.k0 == 0x1716151413121110 && key.k1 == 0x1f1e1d1c1b1a1918)
    ns: [7]int = [0, 1, 7, 8, 15, 16, 63]
    want: [7]u64 = [4580954256181079065, 1627160963267044202, 14024929062309346122, 16677471005018694999, 11460469200492838601, 11320645357696366651, 16654075477394341694]
    for i in 0..7 {
        var h := NewHasherKeyed(key.k0, key.k1)
        h.Bytes(b[0..ns[i]])
        Assert(HashFinish(mut h) == want[i])
        // Every split into two pieces, and three: SipHash's Bytes with a
        // pending tail (ReadShort).
        for k in 0..ns[i] + 1 {
            var g := NewHasherKeyed(key.k0, key.k1)
            g.Bytes(b[0..k])
            g.Bytes(b[k..ns[i]])
            Assert(HashFinish(mut g) == want[i])
            j := (k + ns[i]) / 2
            var e := NewHasherKeyed(key.k0, key.k1)
            e.Bytes(b[0..k])
            e.Bytes(b[k..j])
            e.Bytes(b[j..ns[i]])
            Assert(HashFinish(mut e) == want[i])
        }
    }
    // Under SipHash's reference key too, HashKeyed is SipHash, not New's
    // fold hash.
    for i in 0..16 {
        kb[i] = Trunc[u8](i)
    }
    f := HashKeyOf(kb[..])
    for k in -100..100 {
        Assert(HashKeyed(f.k0, f.k1, &k) != HashOf(&k))
        Assert(HashKeyed(key.k0, key.k1, &k) != HashOf(&k))
    }
    s := "a str key"
    Assert(HashKeyed(f.k0, f.k1, &s) == SipStr(s) && HashOf(&s) == 5828005871986082735)
    m1 := -1
    Assert(HashOf(&m1) == 4553538096044278647)
}

// TmRustZero are Rust 1.99.1's DefaultHasher::new() values (SipHash-1-3
// under the key (0, 0)) of msg[0..n], msg[i] = (7 i + 3) mod 256, for the
// lengths of TmRustLens, as std/std-hashmod_zerokey_test.go's
// hmSip13RustZero records them.
const TmRustLens: [13]int = [0, 1, 7, 8, 9, 15, 16, 63, 64, 65, 255, 256, 300]
const TmRustZero: [13]u64 = [0xd1fba762150c532c, 0x486b06067755d7c9, 0x8a715eaaf55549aa, 0x36c186f0aa4cdbeb, 0x5176191e3d24cbff, 0x19ed3f1b38f7e4e2, 0xd1c94d62751d7b7b, 0x42ed5c087e0bfe5d, 0x65134a36595a21fb, 0xbbb33b0725b183dd, 0x621951539b5066e6, 0x9e352535fdf272ea, 0x37ba751da8b682e3]

test "HashKey.Of makes the key from chosen words: Of(0, 0) is Rust's DefaultHasher key and a zero-filled HashKey's" (sys: &System) {
    msg := try sys.arena.Slice[u8](300)
    for i in 0..300 {
        msg[i] = Trunc[u8](i * 7 + 3)
    }
    z := HashKey.Of(0, 0)
    zs := try sys.arena.Slice[HashKey](1)
    w := HashKeyWords(&z)
    Assert(w.k0 == 0 && w.k1 == 0 && zs[0].k0 == 0 && zs[0].k1 == 0)
    for i in 0..13 {
        var h := NewHasherKeyed(w.k0, w.k1)
        h.Bytes(msg[0..TmRustLens[i]])
        Assert(HashFinish(mut h) == TmRustZero[i])
    }
    // Of(k0, k1) is HashKeyOf of the same words' little-endian bytes, so a
    // table keyed by either orders the same, and not as New.
    var kb: [16]u8 = [0; 16]
    for i in 0..16 {
        kb[i] = Trunc[u8](i + 0x10)
    }
    a := HashKey.Of(0x1716151413121110, 0x1f1e1d1c1b1a1918)
    b := HashKeyOf(kb[..])
    Assert(a.k0 == b.k0 && a.k1 == b.k1)
    var m1 := Map[int, int].NewKeyed(sys.arena, a)
    var m2 := Map[int, int].NewKeyed(sys.arena, b)
    var m3 := Map[int, int].NewKeyed(sys.arena, z)
    var m4 := Map[int, int].New(sys.arena)
    for i in 0..500 {
        try m1.Set(i * 7, 0)
        try m2.Set(i * 7, 0)
        try m3.Set(i * 7, 0)
        try m4.Set(i * 7, 0)
    }
    Assert(TmOrder(&m1) == TmOrder(&m2) && TmOrder(&m1) != TmOrder(&m3) && TmOrder(&m3) != TmOrder(&m4) && TmOrder(&m1) != TmOrder(&m4))
    Assert(m1.t.keyed && m3.t.keyed && m3.t.k0 == 0 && m3.t.k1 == 0)
    k := 12345
    Assert(HashKeyed(w.k0, w.k1, &k) != HashOf(&k))
}

test "NewKeyed maps hold their keys through growth and removal" (sys: &System) {
    a := sys.heap.Alloc()
    var kb: [16]u8 = [0; 16]
    for i in 0..16 {
        kb[i] = Trunc[u8](i * 37 + 11)
    }
    key := HashKeyOf(kb[..])
    var m := Map[int, int].NewKeyed(sys.arena, key)
    var st := Set[int].NewKeyed(sys.arena, key)
    var h := HeapMap[int, int].NewKeyed(a, key)
    defer h.Free(a)
    var t := TextMap[int].NewKeyed(a, key)
    defer t.Free(a)
    for i in 0..4_000 {
        try m.Set(i, i)
        _ = try st.Add(i)
        try h.Set(a, i, i)
        k := try sys.arena.Fmt("{i}", (i))
        _ = try t.Ensure(a, k, i)
    }
    for i in 0..4_000 {
        if i % 4 != 0 {
            _ = m.Remove(&i)
            _ = st.Remove(&i)
            _ = h.Remove(&i)
            k := try sys.arena.Fmt("{i}", (i))
            _ = t.Remove(a, k)
        }
    }
    for i in 0..4_000 {
        k := try sys.arena.Fmt("{i}", (i))
        p := i % 4 == 0
        Assert(m.Has(&i) == p && st.Has(&i) == p && h.Get(&i).IsSome() == p && t.Get(k).IsSome() == p)
    }
    Assert(m.Len() == 1_000 && h.Len() == 1_000 && t.Len() == 1_000 && m.t.k0 == key.k0 && h.t.k1 == key.k1)
}

test "NewKeyed is SipHash under every key, so even the reference key does not order as New" (sys: &System) {
    var kb: [16]u8 = [0; 16]
    for i in 0..16 {
        kb[i] = Trunc[u8](i)
    }
    fixed := HashKeyOf(kb[..])
    kb[0] = 0xAA
    other := HashKeyOf(kb[..])
    var m1 := Map[int, int].New(sys.arena)
    var m2 := Map[int, int].NewKeyed(sys.arena, fixed)
    var m3 := Map[int, int].NewKeyed(sys.arena, other)
    for i in 0..500 {
        try m1.Set(i * 7, 0)
        try m2.Set(i * 7, 0)
        try m3.Set(i * 7, 0)
    }
    Assert(TmOrder(&m1) != TmOrder(&m2) && TmOrder(&m1) != TmOrder(&m3) && TmOrder(&m2) != TmOrder(&m3))
    Assert(!m1.t.keyed && m2.t.keyed && m3.t.keyed)
}

// TmViaFold is the fold hash through the Hash impl: NewHasher, Hash,
// HashFinish, the path HashOf's one-shots stand for.
TmViaFold[K: Hash](x: &K): u64 {
    var h := NewHasher()
    x.Hash(mut h)
    return HashFinish(mut h)
}

test "HashOf's one-shots equal the Hash impl path for every integer type, char, bool and str" (sys: &System) {
    ints: [6]int = [0, 1, -1, 1 << 40, -9223372036854775807 - 1, 9223372036854775807]
    for x in ints {
        Assert(HashOf(&x) == TmViaFold(&x))
        a := i64(x)
        Assert(HashOf(&a) == TmViaFold(&a) && HashOf(&a) == HashOf(&x))
        b := Trunc[u64](a)
        Assert(HashOf(&b) == TmViaFold(&b) && HashOf(&b) == HashOf(&x))
        c := Trunc[uint](a)
        Assert(HashOf(&c) == TmViaFold(&c) && HashOf(&c) == HashOf(&x))
    }
    i32s: [5]i32 = [0, 1, -1, -2147483647 - 1, 2147483647]
    for x in i32s {
        w := int(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    u32s: [4]u32 = [0, 1, 0x80000000, 0xFFFFFFFF]
    for x in u32s {
        w := u64(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    // The small integer types, char and bool take FoldU64K too.
    i16s: [4]i16 = [0, -1, -32768, 32767]
    for x in i16s {
        w := int(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    i8s: [4]i8 = [0, -1, -128, 127]
    for x in i8s {
        w := int(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    u16s: [3]u16 = [0, 0x8000, 0xFFFF]
    for x in u16s {
        w := u64(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    u8s: [3]u8 = [0, 0x80, 0xFF]
    for x in u8s {
        w := u64(x)
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    chars: [3]char = ['a', '~', '0']
    for x in chars {
        w := u64(u32(x))
        Assert(HashOf(&x) == TmViaFold(&x) && HashOf(&x) == HashOf(&w))
    }
    t := true
    f := false
    one: u64 = 1
    zero: u64 = 0
    Assert(HashOf(&t) == TmViaFold(&t) && HashOf(&t) == HashOf(&one))
    Assert(HashOf(&f) == TmViaFold(&f) && HashOf(&f) == HashOf(&zero))
    // A struct key takes the Hash impl path inside HashOf (made and
    // finished in place): equal to NewHasher's, and to the model's pair.
    p := TmPt(x: 3, y: -5)
    Assert(HashOf(&p) == TmViaFold(&p))
    // Every str length from 0 to 40: the 0xFF lands in each position of a
    // word, completes a word (lengths 7, 15, ...), and follows 0 to 2 pairs.
    var text := Text.New(sys.arena)
    for _ in 0..41 {
        s := text.Str()
        Assert(HashOf(&s) == TmViaFold(&s))
        try text.Push("k")
    }
}

// TmDistinct is 80 bytes, each a different ASCII character ('!' + i), so a
// str cut from it has a distinct byte at every position.
TmDistinct(): [80]u8 {
    var b: [80]u8 = [0; 80]
    for i in 0..80 {
        b[i] = Trunc[u8](0x21 + i)
    }
    return b
}

test "FoldStr equals the Hash impl path at every length and offset, with a distinct byte at each position" (sys: &System) {
    // Repeated bytes cannot see a misplaced tail byte: an overlapping load
    // one byte off reads the same value. Distinct bytes can, and every
    // offset k moves the str's start, so the loads run at every alignment.
    b := TmDistinct()
    for k in 0..8 {
        for n in 0..73 {
            s := try str.From(b[k..k + n])
            Assert(HashOf(&s) == TmViaFold(&s))
        }
    }
}

test "FoldStr and NewHasher give the model's vectors on distinct bytes" (sys: &System) {
    // From foldHashStr of std/std-hash_model_test.go over '!' + i
    // (TestStdHash_VectorsMatchModel pins them): 1 to 7 bytes take
    // ShortWord's loads, 31 to 70 both lanes.
    b := TmDistinct()
    ns: [12]int = [1, 2, 3, 4, 5, 6, 7, 12, 31, 33, 47, 70]
    want: [12]u64 = [18274468622797274669, 13296630765199605486, 17460296919843759486, 755444409502779170,
        344463838674294112, 5316956422697354375, 7435488838870673405, 7233994000455831090,
        11801513395352122371, 8617620052980872440, 7929219764555937175, 17524606700546427409]
    for i in 0..12 {
        s := try str.From(b[0..ns[i]])
        Assert(HashOf(&s) == want[i] && TmViaFold(&s) == want[i])
    }
}

test "KeyEq's StrEq equals str == at every length, with a difference at every position" (sys: &System) {
    // TableFind and TableLookup compare str keys eight bytes at a time;
    // a wrong overlap or a skipped word would miss a difference here.
    b := TmDistinct()
    var c := TmDistinct()
    for n in 0..41 {
        s := try str.From(b[0..n])
        same := try str.From(c[0..n])
        Assert(StrEq[str](s, same) && s == same && StrEq[str](s, s))
        if n > 0 {
            shorter := try str.From(b[0..n - 1])
            Assert(!StrEq[str](s, shorter) && !StrEq[str](shorter, s))
        }
        if n < 40 {
            // The same start, one byte longer: the length decides.
            longer := try str.From(b[0..n + 1])
            Assert(!StrEq[str](s, longer) && !StrEq[str](longer, s))
        }
        for p in 0..n {
            c[p] = c[p] ^ 0x01
            t := try str.From(c[0..n])
            Assert(!StrEq[str](s, t) && !StrEq[str](t, s) && s != t)
            c[p] = c[p] ^ 0x01
        }
    }
    // Through a table: equal content in another array finds the key. The
    // keys are views of b, so the table's arena is younger than b.
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    var m := Map[str, int].New(&ar)
    for n in 0..41 {
        try m.Set(try str.From(b[0..n]), n)
    }
    for n in 0..41 {
        k := try str.From(c[0..n])
        Assert(*m.Get(&k).Must() == n)
    }
}

test "Remove keeps a probe chain" (sys: &System) {
    a := sys.heap.Alloc()
    var t := EmptyTable[int, int]()
    Assert(TableResize(mut t, a, true, 32))
    for k in 0..16 {
        Assert(TmClaim(mut t, TmChosen(5, k + 1), k) == 5 + k)
    }
    kh := TmChosen(5, 100)
    Assert(TmClaim(mut t, kh, 99) == 21)
    TableErase(mut t, 6)
    Assert(CtrlAt(t.ctrl, 6) == CtrlDeleted)
    Assert(TableFind(&t, kh, &99) == 21)
    TableFreeArrays(&t, a)
}

test "Remove frees a never-full run" (sys: &System) {
    a := sys.heap.Alloc()
    var t := EmptyTable[int, int]()
    Assert(TableResize(mut t, a, true, 32))
    for k in 0..4 {
        Assert(TmClaim(mut t, TmChosen(5, k + 1), k) == 5 + k)
    }
    g := t.growth
    TableErase(mut t, 6)
    Assert(CtrlAt(t.ctrl, 6) == CtrlEmpty && t.growth == g + 1)
    Assert(TableFind(&t, TmChosen(5, 3), &2) == 7 && TableFind(&t, TmChosen(5, 4), &3) == 8)
    TableFreeArrays(&t, a)
}

test "Churn keeps capacity bounded" (sys: &System) {
    a := sys.heap.Alloc()
    var m := Map[int, int].New(sys.arena)
    var h := HeapMap[int, int].New(a)
    defer h.Free(a)
    for i in 0..1_000_000 {
        try m.Set(i, 0)
        _ = m.Remove(&i)
        try h.Set(a, i, 0)
        _ = h.Remove(&i)
    }
    Assert(m.t.cap == 8 && h.t.cap == 8 && m.Len() == 0)
    // A window of 50 live keys: capacity stays below 32 * 51 / 7.
    for i in 0..1_000_000 {
        try h.Set(a, i, i)
        if i >= 50 {
            j := i - 50
            Assert(h.Remove(&j).Must() == j)
        }
    }
    Assert(h.Len() == 50 && h.t.cap <= 128)
}

test "In-place rehash keeps every key" (sys: &System) {
    a := sys.heap.Alloc()
    var t := EmptyTable[int, int]()
    Assert(TableResize(mut t, a, true, 64))
    // An exact layout with chosen hashes, so the test does not depend on
    // the hash function (which may change): key k at slot k for k in
    // 0..56, which uses up growth.
    for k in 0..56 {
        Assert(TmClaim(mut t, TmChosen(k, 1), k) == k)
    }
    Assert(t.growth == 0)
    // Remove 55 down to 20: each sits in a run of 8 or more non-empty
    // bytes, so each becomes deleted and growth stays 0, with used 20 <=
    // 64 * 7 / 16 = 28.
    var r := 55
    for r >= 20 {
        i := TableFind(&t, TmChosen(r, 1), &r)
        Assert(i == r)
        TableErase(mut t, i)
        Assert(CtrlAt(t.ctrl, i) == CtrlDeleted)
        r -= 1
    }
    Assert(t.used == 20 && t.growth == 0 && TablePlaceKind(&t, -1) == PlaceRehash)
    // An insert whose first free slot is the empty slot 60 rehashes in
    // place: same capacity, the tombstones gone, every key moved to its
    // real hash's probe path (TableHash) and found there.
    Assert(TableReserve(mut t, a, true, TmChosen(60, 1)) && t.cap == 64 && t.growth == 56 - 20)
    for k in 0..20 {
        Assert(TableFind(&t, HashOf(&k), &k) >= 0)
    }
    for i in 0..64 {
        Assert(CtrlAt(t.ctrl, i) != CtrlDeleted)
    }
    TableFreeArrays(&t, a)
}

// HashKeyed's one-shot integer paths (SipU64K) against the Hash impl path
// they stand for: NewHasherKeyed, Hash, HashFinish.
TmViaImpl[K: Hash](k0: u64, k1: u64, x: &K): u64 {
    var h := NewHasherKeyed(k0, k1)
    x.Hash(mut h)
    return HashFinish(mut h)
}

test "the integer hash paths equal the Hash impls for every key type" (sys: &System) {
    keys: [3]u64 = [0x0706050403020100, 0x1716151413121110, 0xFFFFFFFFFFFFFFFF]
    kk: [3]u64 = [0x0f0e0d0c0b0a0908, 0x1f1e1d1c1b1a1918, 0]
    for j in 0..3 {
        k0 := keys[j]
        k1 := kk[j]
        ints: [6]int = [0, 1, -1, 1 << 40, -9223372036854775807 - 1, 9223372036854775807]
        for x in ints {
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x))
            a := i64(x)
            Assert(HashKeyed(k0, k1, &a) == TmViaImpl(k0, k1, &a))
            Assert(HashKeyed(k0, k1, &a) == HashKeyed(k0, k1, &x))
            b := Trunc[u64](a)
            Assert(HashKeyed(k0, k1, &b) == TmViaImpl(k0, k1, &b))
            Assert(HashKeyed(k0, k1, &b) == HashKeyed(k0, k1, &x))
            c := Trunc[uint](a)
            Assert(HashKeyed(k0, k1, &c) == TmViaImpl(k0, k1, &c))
        }
        i32s: [5]i32 = [0, 1, -1, -2147483647 - 1, 2147483647]
        for x in i32s {
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x))
            w := int(x)
            Assert(HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        u32s: [4]u32 = [0, 1, 0x80000000, 0xFFFFFFFF]
        for x in u32s {
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x))
            w := u64(x)
            Assert(HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        // The small integer types, char and bool.
        i16s: [3]i16 = [-1, -32768, 32767]
        for x in i16s {
            w := int(x)
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x) && HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        i8s: [3]i8 = [-1, -128, 127]
        for x in i8s {
            w := int(x)
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x) && HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        u16s: [2]u16 = [0x8000, 0xFFFF]
        for x in u16s {
            w := u64(x)
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x) && HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        u8s: [2]u8 = [0x80, 0xFF]
        for x in u8s {
            w := u64(x)
            Assert(HashKeyed(k0, k1, &x) == TmViaImpl(k0, k1, &x) && HashKeyed(k0, k1, &x) == HashKeyed(k0, k1, &w))
        }
        c := 'q'
        cw := u64(u32(c))
        Assert(HashKeyed(k0, k1, &c) == TmViaImpl(k0, k1, &c) && HashKeyed(k0, k1, &c) == HashKeyed(k0, k1, &cw))
        bt := true
        bf := false
        Assert(HashKeyed(k0, k1, &bt) == TmViaImpl(k0, k1, &bt) && HashKeyed(k0, k1, &bf) == TmViaImpl(k0, k1, &bf))
    }
    // The table path of each integer key type, keyed and not.
    var kb: [16]u8 = [0; 16]
    for i in 0..16 {
        kb[i] = Trunc[u8](i * 13 + 5)
    }
    key := HashKeyOf(kb[..])
    var m1 := Map[u32, int].NewKeyed(sys.arena, key)
    var m2 := Map[i32, int].New(sys.arena)
    var m3 := Map[u64, int].NewKeyed(sys.arena, key)
    var m4 := Map[i64, int].NewKeyed(sys.arena, key)
    var m5 := Map[uint, int].New(sys.arena)
    for i in 0..2_000 {
        try m1.Set(Trunc[u32](i * 7), i)
        try m2.Set(Trunc[i32](i - 1_000), i)
        try m3.Set(Trunc[u64](i) << 40, i)
        try m4.Set(Trunc[i64](i) - 1_000, i)
        try m5.Set(Trunc[uint](i * 3), i)
    }
    for i in 0..2_000 {
        a := Trunc[u32](i * 7)
        b := Trunc[i32](i - 1_000)
        c := Trunc[u64](i) << 40
        d := Trunc[i64](i) - 1_000
        e := Trunc[uint](i * 3)
        Assert(*m1.Get(&a).Must() == i && *m2.Get(&b).Must() == i && *m3.Get(&c).Must() == i)
        Assert(*m4.Get(&d).Must() == i && *m5.Get(&e).Must() == i)
    }
}

// A zero-size key: every TmZ equals every other and its Hash
// absorbs nothing, so a table holds at most one, and a slot of two TmZ has
// no bytes (TableResize leaves the slot array dangling).
struct TmZ {}

impl Eq for TmZ {
    Eq(&self, other: &TmZ): bool {
        return true
    }
}

impl Hash for TmZ {
    Hash(&self, h: mut Hasher) {
    }
}

// A two-field key through the generic Hash path, where the other table
// tests use an integer or a str key.
struct TmPt {
    x: int
    y: int
}

impl Eq for TmPt {
    Eq(&self, other: &TmPt): bool {
        return self.x == other.x && self.y == other.y
    }
}

impl Hash for TmPt {
    Hash(&self, h: mut Hasher) {
        self.x.Hash(h)
        self.y.Hash(h)
    }
}

test "a zero-size key: one entry at most, churn, iteration, and a slot of no bytes freed" (sys: &System) {
    a := sys.heap.Alloc()
    var m := Map[TmZ, int].New(sys.arena)
    var st := Set[TmZ].New(sys.arena)
    var h := HeapMap[TmZ, TmZ].New(a)
    defer h.Free(a)
    z := TmZ()
    for i in 0..2_000 {
        try m.Set(TmZ(), i)
        // The member is present at the start of round i unless round i - 1 removed it.
        first := try st.Add(TmZ())
        Assert(first == (i == 0 || i % 3 == 1) && !(try st.Add(TmZ())))
        try h.Set(a, TmZ(), TmZ())
        Assert(m.Len() == 1 && *m.Get(&z).Must() == i && st.t.Count() == 1 && h.Len() == 1)
        if i % 3 == 0 {
            Assert(m.Remove(&z).Must() == i && st.Remove(&z) && h.Remove(&z).IsSome())
            Assert(m.Len() == 0 && !st.Has(&z) && h.Get(&z).IsNone() && m.Remove(&z).IsNone())
        }
    }
    var n := 0
    for e in h.Iter() {
        Assert(e.key.Eq(&z))
        n += 1
    }
    for e in m.Iter() {
        Assert(*e.value == 1_999)
        n += 1
    }
    Assert(n == 2 && m.t.cap == 8 && h.t.cap == 8)
    var hk := HeapMap[TmZ, int].NewKeyed(a, HashKey(k0: 1, k1: 2))
    defer hk.Free(a)
    try hk.Set(a, TmZ(), 5)
    Assert(*hk.Get(&z).Must() == 5)
}

// TmPtRun fills m and h with 3,000 two-field keys, removes two in three, and
// checks lookups and iteration. (x, y) and (y, x) are distinct keys.
TmPtRun(m: mut Map[TmPt, int], h: mut HeapMap[TmPt, int], a: &Alloc): OutOfMemory!void {
    for i in 0..3_000 {
        try m.Set(TmPt(x: i / 50, y: i % 50), i)
        try h.Set(a, TmPt(x: i % 50, y: i / 50), i)
    }
    Assert(m.Len() == 3_000 && h.Len() == 3_000)
    for i in 0..3_000 {
        if i % 3 != 0 {
            mk := TmPt(x: i / 50, y: i % 50)
            hk := TmPt(x: i % 50, y: i / 50)
            Assert(m.Remove(&mk).Must() == i && h.Remove(&hk).Must() == i)
        }
    }
    for i in 0..3_000 {
        p := i % 3 == 0
        mk := TmPt(x: i / 50, y: i % 50)
        hk := TmPt(x: i % 50, y: i / 50)
        Assert(m.Has(&mk) == p && h.Get(&hk).IsSome() == p)
    }
    out := TmPt(x: 60, y: 0)
    Assert(m.Get(&out).IsNone() && h.Get(&out).IsNone())
    var n := 0
    var sum := 0
    for e in m.Iter() {
        Assert(e.key.x * 50 + e.key.y == *e.value && *e.value % 3 == 0)
        n += 1
        sum += *e.value
    }
    for e in h.Iter() {
        Assert(e.key.y * 50 + e.key.x == *e.value)
        n += 1
        sum += *e.value
    }
    // The multiples of 3 below 3,000, once in each map.
    Assert(n == 2_000 && sum == 2 * 1_498_500)
}

test "a two-field key through growth, removal and iteration, fixed and keyed" (sys: &System) {
    a := sys.heap.Alloc()
    var m := Map[TmPt, int].New(sys.arena)
    var h := HeapMap[TmPt, int].New(a)
    defer h.Free(a)
    try TmPtRun(mut m, mut h, a)
    var mk := Map[TmPt, int].NewKeyed(sys.arena, HashKey(k0: 0x1234, k1: 0x5678))
    var hk := HeapMap[TmPt, int].NewKeyed(a, HashKey(k0: 0x9abc, k1: 0xdef0))
    defer hk.Free(a)
    try TmPtRun(mut mk, mut hk, a)
}

// TmTextKeys is the differential script's 1,312 keys, 41 bytes apart in
// one array of the arena: key i has i % 41 bytes ('a' + (i / 41 + 7j) % 26
// at byte j), and in every eighth key the second half is NUL bytes, so
// keys that differ only by trailing NULs, like "a" and "a\0", both occur,
// at every length from 0 to 40 and on both sides of the 15-byte inline
// bound.
TmTextKeys(ar: &Arena): OutOfMemory!List[str] from ar {
    b := try ar.Slice[u8](1312 * 41)
    var keys := List[str].New(ar)
    for i in 0..1312 {
        n := i % 41
        for j in 0..n {
            b[i * 41 + j] = Trunc[u8](0x61 + (i / 41 + 7 * j) % 26)
            if i % 8 == 0 && j >= n / 2 {
                b[i * 41 + j] = 0
            }
        }
    }
    for i in 0..1312 {
        try keys.Push(str.From(b[i * 41..i * 41 + i % 41]).Must())
    }
    return keys
}

// TmTextRun drives one seeded script of Set, Replace, Ensure and Remove
// through t and through h, a HeapMap[str, int] over the same keys: each
// result must agree, then every lookup, then the iteration order, entry by
// entry. h's keys are views of keys; t keeps its own.
TmTextRun(t: mut TextMap[int], h: mut HeapMap[str, int], a: &Alloc, keys: &List[str], seed: u64): OutOfMemory!void *keys into h {
    var rng := seed
    for s in 0..12_000 {
        i := TmNext(mut rng) % keys.Len()
        k := keys[i]
        op := s % 6
        if op < 2 {
            try t.Set(a, k, s)
            try h.Set(a, k, s)
        } else if op == 2 {
            r := try t.Replace(a, k, s)
            q := try h.Replace(a, k, s)
            Assert(r.IsSome() == q.IsSome())
            if v := r {
                Assert(v == q.Must())
            }
        } else if op == 3 {
            v := try t.Ensure(a, k, s)
            w := try h.Ensure(a, k, s)
            Assert(*v == *w)
            *v += 1
            *w += 1
        } else {
            r := t.Remove(a, k)
            q := h.Remove(&k)
            Assert(r.IsSome() == q.IsSome())
            if v := r {
                Assert(v == q.Must())
            }
        }
    }
    Assert(t.Len() == h.Len())
    for i in 0..keys.Len() {
        k := keys[i]
        g := t.Get(k)
        Assert(g.IsSome() == h.Get(&k).IsSome())
        if v := g {
            Assert(*v == *h.Get(&k).Must())
        }
    }
    var ti := t.Iter()
    var hi := h.Iter()
    var n := 0
    for e := ti.Next() {
        f := hi.Next().Must()
        Assert(e.key == *f.key && *e.value == *f.value)
        n += 1
    }
    Assert(hi.Next().IsNone() && n == t.Len())
}

test "TextMap keeps HeapMap[str, V]'s results and order with inline and copied keys, fixed and keyed" (sys: &System) {
    a := sys.heap.Alloc()
    keys := try TmTextKeys(sys.arena)
    var t := TextMap[int].New(a)
    defer t.Free(a)
    var h := HeapMap[str, int].New(a)
    defer h.Free(a)
    try TmTextRun(mut t, mut h, a, &keys, 1)
    var tk := TextMap[int].NewKeyed(a, HashKey.Of(0x0123456789abcdef, 0xfedcba9876543210))
    defer tk.Free(a)
    var hk := HeapMap[str, int].NewKeyed(a, HashKey.Of(0x0123456789abcdef, 0xfedcba9876543210))
    defer hk.Free(a)
    try TmTextRun(mut tk, mut hk, a, &keys, 2)
}

test "TextKeyOf is inline up to 15 bytes, its text the key's, and keys that differ by a trailing NUL differ" (sys: &System) {
    b := TmDistinct()
    et := EmptyTable[TextKey, int]()
    ek := EmptyTableKeyed[TextKey, int](HashKey.Of(3, 4))
    for n in 0..41 {
        s := try str.From(b[0..n])
        k := TextKeyOf[int](s)
        Assert(TextKeyIsInline(&k) == (n <= 15) && TextKeyStr[int](&k) == s)
        Assert(TextProbe(&et, s).hash == HashOf(&s) && HashOf(&k) == HashOf(&s) && TextKeyFold[int](&k) == HashOf(&s))
        Assert(TextProbe(&ek, s).hash == HashKeyed(3, 4, &s) && TextKeyEq[int](&TextProbe(&ek, s).key, &k))
        Assert(HashKeyed(3, 4, &k) == HashKeyed(3, 4, &s) && TmViaFold(&k) == HashOf(&s))
        if n > 0 {
            p := TextKeyOf[int](try str.From(b[0..n - 1]))
            Assert(!TextKeyEq[int](&k, &p) && !TextKeyEq[int](&p, &k))
        }
        // The copied form a compile-time evaluation makes for every key
        // equals and hashes as the native form, and differs from another
        // key of either form.
        c := TextKey(p: raw.StrData(s), n: Trunc[u64](n))
        Assert(!TextKeyIsInline(&c) && TextKeyEq[int](&c, &k) && TextKeyEq[int](&k, &c) && TextKeyFold[int](&c) == HashOf(&s))
        if n > 0 {
            p := TextKeyOf[int](try str.From(b[1..n]))
            Assert(!TextKeyEq[int](&c, &p) && !TextKeyEq[int](&p, &c))
        }
    }
    var z: [3]u8 = [0x61, 0, 0]
    k1 := TextKeyOf[int](try str.From(z[0..1]))
    k2 := TextKeyOf[int](try str.From(z[0..2]))
    k3 := TextKeyOf[int](try str.From(z[0..3]))
    Assert(!TextKeyEq[int](&k1, &k2) && !TextKeyEq[int](&k2, &k3) && TextKeyEq[int](&k3, &k3))
    a := sys.heap.Alloc()
    var m := TextMap[int].New(a)
    defer m.Free(a)
    try m.Set(a, try str.From(z[0..1]), 1)
    try m.Set(a, try str.From(z[0..2]), 2)
    try m.Set(a, try str.From(z[0..3]), 3)
    Assert(m.Len() == 3 && *m.Get("a").Must() == 1 && *m.Get(try str.From(z[0..3])).Must() == 3)
}
