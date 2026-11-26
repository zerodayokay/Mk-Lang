// Why: a map made with NewKeyed must hash with SipHash-1-3 under its own key,
// so keys chosen against the fixed key cannot force collisions.
// What: TestStdColl_KeyedHashMatchesSip13, _KeyedTablesMatchModel and
// _KeyedResistsChosenKeys, over Map, Set, HeapMap and TextMap.
// How: hashes against Go's SipHash-1-3; seeded operations in lockstep against
// swissModel under the same key; probe groups counted for colliding keys.

package std

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"testing"
)

const collKeyedCore = `// Fixture of the keyed-map tests: std-internal calls, exported.

export CollKeyedKey(b: []u8): HashKey {
    return HashKeyOf(b)
}

export CollKeyedHashInt(key: HashKey, k: int): u64 {
    return HashKeyed(key.k0, key.k1, &k)
}

export CollKeyedHashStr(key: HashKey, s: str): u64 {
    return HashKeyed(key.k0, key.k1, &s)
}

export CollKeyedHasherBytes(key: HashKey, b: []u8): u64 {
    var h := NewHasherKeyed(key.k0, key.k1)
    h.Bytes(b)
    return HashFinish(mut h)
}

export CollKeyedFixedInt(k: int): u64 {
    return HashOf(&k)
}

// The probe groups TableFind reads to find each stored key, summed.
CollKeyedProbes[K: Hash + Eq, V](t: &Table[K, V]): int {
    if t.cap == 0 {
        return 0
    }
    mask := t.cap - 1
    var sum := 0
    for i in 0..t.cap {
        if CtrlAt(t.ctrl, i) < CtrlDeleted {
            hash := TableHash(t, SlotRef(KeyAt(t.slots, i), 0))
            var pos := H1(hash) & mask
            var stride := 0
            var n := 1
            for ((i - pos) & mask) >= TableGroupWidth {
                stride += TableGroupWidth
                pos = (pos + stride) & mask
                n += 1
            }
            sum += n
        }
    }
    return sum
}

export CollKeyedMapProbes[K: Hash + Eq, V](m: &Map[K, V]): int {
    return CollKeyedProbes(&m.t)
}

export CollKeyedHeapProbes[K: Hash + Eq, V](m: &HeapMap[K, V]): int {
    return CollKeyedProbes(&m.t)
}
`

const collKeyedModule = `import std.core

export Key(b: []u8): core.HashKey {
    return core.CollKeyedKey(b)
}

export HashInt(key: core.HashKey, k: int): u64 {
    return core.CollKeyedHashInt(key, k)
}

export HashStr(key: core.HashKey, s: str): u64 {
    return core.CollKeyedHashStr(key, s)
}

export HasherBytes(key: core.HashKey, b: []u8): u64 {
    return core.CollKeyedHasherBytes(key, b)
}

export FixedInt(k: int): u64 {
    return core.CollKeyedFixedInt(k)
}

export MapProbes[K: Hash + Eq, V](m: &Map[K, V]): int {
    return core.CollKeyedMapProbes(m)
}

export HeapProbes[K: Hash + Eq, V](m: &HeapMap[K, V]): int {
    return core.CollKeyedHeapProbes(m)
}
`

func collKeyedExtra() map[string]string {
	return map[string]string{"core/tests_collkeyed.mk": collKeyedCore, "collkeyed/collkeyed.mk": collKeyedModule}
}

// sip13k is SipHash-1-3 under the key (k0, k1).
func sip13k(k0, k1 uint64, b []byte) uint64 {
	v0 := 0x736f6d6570736575 ^ k0
	v1 := 0x646f72616e646f6d ^ k1
	v2 := 0x6c7967656e657261 ^ k0
	v3 := 0x7465646279746573 ^ k1
	round := func() {
		v0 += v1
		v1 = bits.RotateLeft64(v1, 13)
		v1 ^= v0
		v0 = bits.RotateLeft64(v0, 32)
		v2 += v3
		v3 = bits.RotateLeft64(v3, 16)
		v3 ^= v2
		v0 += v3
		v3 = bits.RotateLeft64(v3, 21)
		v3 ^= v0
		v2 += v1
		v1 = bits.RotateLeft64(v1, 17)
		v1 ^= v2
		v2 = bits.RotateLeft64(v2, 32)
	}
	n := len(b)
	i := 0
	for ; i+8 <= n; i += 8 {
		m := binary.LittleEndian.Uint64(b[i:])
		v3 ^= m
		round()
		v0 ^= m
	}
	last := uint64(n&0xff) << 56
	for j := 0; i+j < n; j++ {
		last |= uint64(b[i+j]) << (8 * j)
	}
	v3 ^= last
	round()
	v0 ^= last
	v2 ^= 0xff
	round()
	round()
	round()
	return v0 ^ v1 ^ v2 ^ v3
}

// keyedKey is a test key: its 16 bytes and the two words they give.
type keyedKey struct {
	name   string
	b      [16]byte
	k0, k1 uint64
}

func newKeyedKey(name string, f func(i int) byte) keyedKey {
	k := keyedKey{name: name}
	for i := range k.b {
		k.b[i] = f(i)
	}
	k.k0 = binary.LittleEndian.Uint64(k.b[:8])
	k.k1 = binary.LittleEndian.Uint64(k.b[8:])
	return k
}

// keyedKeys: the fixed key's bytes 00..0f (so k0 = 0x0706050403020100,
// k1 = 0x0f0e0d0c0b0a0908), and two others.
func keyedKeys() []keyedKey {
	return []keyedKey{
		newKeyedKey("fixed", func(i int) byte { return byte(i) }),
		newKeyedKey("k1", func(i int) byte { return byte(i*37 + 11) }),
		newKeyedKey("ff", func(int) byte { return 0xFF }),
	}
}

func (k keyedKey) mkBytes() string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range k.b {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d", x)
	}
	b.WriteByte(']')
	return b.String()
}

func (k keyedKey) intHash(x int) uint64 {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(int64(x)))
	return sip13k(k.k0, k.k1, b[:])
}

func (k keyedKey) strHash(s string) uint64 {
	return sip13k(k.k0, k.k1, append([]byte(s), 0xFF))
}

// TestStdColl_KeyedHashMatchesSip13: HashKeyed of int and str keys and a
// keyed Hasher over bytes equal Go's SipHash-1-3 under the same key, and
// HashKeyOf reads k0 and k1 as SipHash's little-endian key words. A HashKey is
// made in these tests from chosen bytes through the fixture's HashKeyOf;
// HashKey.New(io) reads them from Io.Random (io_io.mk).
func TestStdColl_KeyedHashMatchesSip13(t *testing.T) {
	t.Parallel()
	keys := keyedKeys()
	if keys[0].k0 != 0x0706050403020100 || keys[0].k1 != 0x0f0e0d0c0b0a0908 {
		t.Fatal("the fixed key's bytes do not give the fixed key")
	}
	ints := []int{0, 1, -1, 42, 1 << 40, -9223372036854775808, 9223372036854775807}
	strs := []string{"", "a", "abcdefg", "abcdefgh", "key-0000000000000001", "é€　"}
	var src, want strings.Builder
	src.WriteString("import std.collkeyed\n\n")
	fmt.Fprintf(&src, "const Ints: [%d]int = %s\n", len(ints), mkIntArray(ints))
	fmt.Fprintf(&src, "const Strs: [%d]str = [", len(strs))
	for i, s := range strs {
		if i > 0 {
			src.WriteString(", ")
		}
		src.WriteString(mkString(s))
	}
	src.WriteString("]\n")
	var raw [40]byte
	for i := range raw {
		raw[i] = byte(i*7 + 1)
	}
	var calls strings.Builder
	for ki, k := range keys {
		fmt.Fprintf(&src, "\nCheck%d(out: mut Writer, b: []u8): WriteError!void {\n    kb: [16]u8 = %s\n    key := collkeyed.Key(kb[..])\n", ki, k.mkBytes())
		src.WriteString("    for x in Ints {\n        h := collkeyed.HashInt(key, x)\n        f := collkeyed.FixedInt(x)\n        try Print(out, \"i {h} {f}\\n\", (h, f))\n    }\n")
		src.WriteString("    for s in Strs {\n        h := collkeyed.HashStr(key, s)\n        try Print(out, \"s {h}\\n\", (h))\n    }\n")
		src.WriteString("    for n in 0..41 {\n        h := collkeyed.HasherBytes(key, b[0..n])\n        try Print(out, \"b {h}\\n\", (h))\n    }\n}\n")
		fmt.Fprintf(&calls, "    try Check%d(mut out, b[..])\n", ki)
		for _, x := range ints {
			fmt.Fprintf(&want, "i %d %d\n", k.intHash(x), foldHashInt(x))
		}
		for _, s := range strs {
			fmt.Fprintf(&want, "s %d\n", k.strHash(s))
		}
		for n := 0; n <= 40; n++ {
			fmt.Fprintf(&want, "b %d\n", sip13k(k.k0, k.k1, raw[:n]))
		}
	}
	src.WriteString("\nexport Main(sys: &System): WriteError!void {\n    var out := sys.Stdout()\n    var b: [40]u8 = [0; 40]\n    for i in 0..40 {\n        b[i] = Trunc[u8](i * 7 + 1)\n    }\n")
	src.WriteString(calls.String())
	src.WriteString("}\n")
	if keys[0].strHash("abc") != sip13([]byte("abc\xff")) {
		t.Fatal("sip13k under the fixed key differs from sip13")
	}
	diffLines(t, collExec(t, collBuildWith(t, collKeyedExtra(), src.String())), want.String())
}

// keyedRunProgram runs the seeded operations of TestStdColl_KeyedTables
// MatchModel over four containers in lockstep.
const keyedRunProgram = `import std.collkeyed

struct Rng {
    s: u64
}

Rng.Next(mut self, n: int): int {
    self.s = self.s *% 6364136223846793005 +% 1442695040888963407
    return Trunc[int](self.s >> 33) % n
}

Fnv(h: u64, x: u64): u64 {
    return (h ^ x) *% 1099511628211
}

FnvStr(s: str): u64 {
    var h: u64 = 14695981039346656037
    b := s.AsBytes()
    for i in 0..b.Len() {
        h = (h ^ u64(b[i])) *% 1099511628211
    }
    return h
}

Run(out: mut Writer, m: mut Map[int, int], hm: mut HeapMap[int, int], s: mut Set[int], tm: mut TextMap[int], a: &Alloc, keys: []int, strs: []str, ops: int, seed: u64): OutOfMemory|WriteError!void {
    var rng := Rng(s: seed)
    var acc: u64 = 0
    for op in 0..ops {
        idx := rng.Next(keys.Len())
        what := rng.Next(8)
        k := keys[idx]
        key := strs[idx]
        if what < 5 {
            try m.Set(k, op)
            try hm.Set(a, k, op)
            _ = try s.Add(k)
            v := try tm.Ensure(a, key, op)
            *v = op
        } else if what < 7 {
            _ = m.Remove(&k)
            _ = hm.Remove(&k)
            _ = s.Remove(&k)
            _ = tm.Remove(a, key)
        } else {
            var g1 := 0
            if v := m.Get(&k) {
                g1 = *v + 1
            }
            var g2 := 0
            if v := hm.Get(&k) {
                g2 = *v + 1
            }
            var g3 := 0
            if s.Has(&k) {
                g3 = 1
            }
            var g4 := 0
            if v := tm.Get(key) {
                g4 = *v + 1
            }
            acc = acc *% 31 +% Trunc[u64](g1)
            acc = acc *% 31 +% Trunc[u64](g2)
            acc = acc *% 31 +% Trunc[u64](g3)
            acc = acc *% 31 +% Trunc[u64](g4)
        }
        if op < 16 || op % 97 == 0 || op == ops - 1 {
            var o1: u64 = 14695981039346656037
            var i1 := m.Iter()
            for e := i1.Next() {
                o1 = Fnv(Fnv(o1, Trunc[u64](*e.key)), Trunc[u64](*e.value))
            }
            var o2: u64 = 14695981039346656037
            var i2 := hm.Iter()
            for e := i2.Next() {
                o2 = Fnv(Fnv(o2, Trunc[u64](*e.key)), Trunc[u64](*e.value))
            }
            var o4: u64 = 14695981039346656037
            var i4 := tm.Iter()
            for e := i4.Next() {
                o4 = Fnv(Fnv(o4, FnvStr(e.key)), Trunc[u64](*e.value))
            }
            n1 := m.Len()
            n2 := hm.Len()
            n4 := tm.Len()
            p1 := collkeyed.MapProbes(&*m)
            p2 := collkeyed.HeapProbes(&*hm)
            try Print(out, "{op} {n1} {o1} {n2} {o2} {n4} {o4} {acc} {p1} {p2}\n", (op, n1, o1, n2, o2, n4, o4, acc, p1, p2))
        }
    }
}
`

// keyedModelRun is the Go side of Run: one model for the int-keyed
// containers (Map, HeapMap and Set share a layout, which depends only on
// the hashes and the operations) and one for TextMap's str keys.
func keyedModelRun(k *keyedKey, keys []int, strs []string, ops int, seed uint64) string {
	im := &swissModel{}
	sm := &swissModel{}
	if k != nil {
		kk := *k
		im.hashFn = kk.intHash
		sm.hashFn = func(i int) uint64 { return kk.strHash(strs[i]) }
	} else {
		sm.hashFn = func(i int) uint64 { return foldHashStr(strs[i]) }
	}
	s := seed
	next := func(n int) int {
		s = s*6364136223846793005 + 1442695040888963407
		return int(s>>33) % n
	}
	fnv := func(h, x uint64) uint64 { return (h ^ x) * 1099511628211 }
	fnvStr := func(x string) uint64 {
		h := uint64(14695981039346656037)
		for i := 0; i < len(x); i++ {
			h = fnv(h, uint64(x[i]))
		}
		return h
	}
	var out strings.Builder
	var acc uint64
	for op := 0; op < ops; op++ {
		idx := next(len(keys))
		what := next(8)
		k := keys[idx]
		ih, sh := im.hash(k), sm.hash(idx)
		switch {
		case what < 5:
			if at, free := im.lookup(ih, k); at < 0 {
				j := im.place(ih, free)
				im.key[j], im.val[j] = k, op
			} else {
				im.val[at] = op
			}
			if at, free := sm.lookup(sh, idx); at < 0 {
				j := sm.place(sh, free)
				sm.key[j], sm.val[j] = idx, op
			} else {
				sm.val[at] = op
			}
		case what < 7:
			if i := im.find(ih, k); i >= 0 {
				im.erase(i)
			}
			if i := sm.find(sh, idx); i >= 0 {
				sm.erase(i)
			}
		default:
			g1, g3, g4 := 0, 0, 0
			if i := im.find(ih, k); i >= 0 {
				g1, g3 = im.val[i]+1, 1
			}
			if i := sm.find(sh, idx); i >= 0 {
				g4 = sm.val[i] + 1
			}
			acc = acc*31 + uint64(g1)
			acc = acc*31 + uint64(g1)
			acc = acc*31 + uint64(g3)
			acc = acc*31 + uint64(g4)
		}
		if op < 16 || op%97 == 0 || op == ops-1 {
			o1 := uint64(14695981039346656037)
			for i := 0; i < im.cap; i++ {
				if im.ctrl[i] < mDeleted {
					o1 = fnv(fnv(o1, uint64(im.key[i])), uint64(im.val[i]))
				}
			}
			o4 := uint64(14695981039346656037)
			for i := 0; i < sm.cap; i++ {
				if sm.ctrl[i] < mDeleted {
					o4 = fnv(fnv(o4, fnvStr(strs[sm.key[i]])), uint64(sm.val[i]))
				}
			}
			probes := modelProbes(im)
			fmt.Fprintf(&out, "%d %d %d %d %d %d %d %d %d %d\n", op, im.used, o1, im.used, o1, sm.used, o4, acc, probes, probes)
		}
	}
	return out.String()
}

// TestStdColl_KeyedTablesMatchModel: the four containers, made with New,
// with NewKeyed of the fixed key's bytes, and with NewKeyed of two other keys,
// run the same seeded operations in lockstep (growth, removals, tombstones);
// after every 97th operation each container's length and iteration order, and
// the values lookups return, equal the Go model of the Swiss table
// (std-coll_table_test.go's swissModel) under that key. So every hash of a
// keyed table, the ones of a resize and an in-place rehash included, uses the
// table's key, and another key gives another order.
func TestStdColl_KeyedTablesMatchModel(t *testing.T) {
	t.Parallel()
	keys := make([]int, 150)
	strs := make([]string, len(keys))
	for i := range keys {
		keys[i] = i*7919 - 500
		strs[i] = "key-" + strconv.Itoa(keys[i])
	}
	const ops, seed = 6000, 77
	var src, want strings.Builder
	src.WriteString(keyedRunProgram)
	fmt.Fprintf(&src, "\nconst Keys: [%d]int = %s\nconst Strs: [%d]str = [", len(keys), mkIntArray(keys), len(strs))
	for i, s := range strs {
		if i > 0 {
			src.WriteString(", ")
		}
		src.WriteString(mkString(s))
	}
	src.WriteString("]\n")
	var calls strings.Builder
	nb := 0
	block := func(name, mk string) {
		fmt.Fprintf(&src, "\nCfg%d(out: mut Writer, sys: &System, a: &Alloc): OutOfMemory|WriteError!void {\n    try Print(out, \"%s\\n\", ())\n%s", nb, name, mk)
		fmt.Fprintf(&src, "    try Run(out, mut m, mut hm, mut s, mut tm, a, Keys[..], Strs[..], %d, %d)\n}\n", ops, seed)
		fmt.Fprintf(&calls, "    try Cfg%d(mut out, sys, a)\n", nb)
		nb++
	}
	block("new", "    var m := Map[int, int].New(sys.arena)\n    var hm := HeapMap[int, int].New(a)\n    defer hm.Free(a)\n    var s := Set[int].New(sys.arena)\n    var tm := TextMap[int].New(a)\n    defer tm.Free(a)\n")
	want.WriteString("new\n" + keyedModelRun(nil, keys, strs, ops, seed))
	outs := map[string]string{}
	for _, k := range keyedKeys() {
		block("keyed "+k.name, fmt.Sprintf("    kb: [16]u8 = %s\n    key := collkeyed.Key(kb[..])\n    var m := Map[int, int].NewKeyed(sys.arena, key)\n    var hm := HeapMap[int, int].NewKeyed(a, key)\n    defer hm.Free(a)\n    var s := Set[int].NewKeyed(sys.arena, key)\n    var tm := TextMap[int].NewKeyed(a, key)\n    defer tm.Free(a)\n", k.mkBytes()))
		k := k
		text := keyedModelRun(&k, keys, strs, ops, seed)
		outs[k.name] = text
		want.WriteString("keyed " + k.name + "\n" + text)
	}
	src.WriteString("\nexport Main(sys: &System): OutOfMemory|WriteError!void {\n    var out := sys.Stdout()\n    a := sys.heap.Alloc()\n")
	src.WriteString(calls.String())
	src.WriteString("}\n")
	// New hashes with the fold hash, so NewKeyed of SipHash's
	// reference key no longer orders as New does.
	if outs["fixed"] == keyedModelRun(nil, keys, strs, ops, seed) {
		t.Fatal("model: NewKeyed of SipHash's reference key orders as New: the keyed tables do not use SipHash")
	}
	if outs["k1"] == outs["fixed"] || outs["ff"] == outs["fixed"] || outs["k1"] == outs["ff"] {
		t.Fatal("model: two keys give the same orders; the test would not see a table that ignores its key")
	}
	for _, flags := range [][]string{nil, {"--release"}} {
		diffLines(t, collExec(t, collBuildWith(t, collKeyedExtra(), src.String(), flags...)), want.String())
	}
}

// modelProbes is CollKeyedProbes on the model.
func modelProbes(m *swissModel) int {
	mask := m.cap - 1
	sum := 0
	for i := 0; i < m.cap; i++ {
		if m.ctrl[i] < mDeleted {
			pos := int(m.hash(m.key[i])>>7) & mask
			n := 1
			for stride := 0; (i-pos)&mask >= mGroupWidth; n++ {
				stride += mGroupWidth
				pos = (pos + stride) & mask
			}
			sum += n
		}
	}
	return sum
}

// TestStdColl_KeyedResistsChosenKeys: 1,500 int keys chosen so that under
// the fixed key they share the low 11 bits of H1 (one probe start in every
// table up to 2,048 slots, the capacity they end in). The probe groups a
// lookup of each stored key reads, summed, equal the model's, are quadratic
// for Map.New and HeapMap.New (at least n²/64) and linear for the same keys in
// a NewKeyed map (at most 2n).
func TestStdColl_KeyedResistsChosenKeys(t *testing.T) {
	t.Parallel()
	const n = 1500
	keys := collidingKeys(n, 11, false)
	k1 := keyedKeys()[1]
	probes := func(hf func(int) uint64) (int, int) {
		m := &swissModel{hashFn: hf}
		for _, k := range keys {
			h := m.hash(k)
			_, free := m.lookup(h, k)
			j := m.place(h, free)
			m.key[j], m.val[j] = k, 0
		}
		return modelProbes(m), m.cap
	}
	fixed, fcap := probes(nil)
	keyed, kcap := probes(k1.intHash)
	t.Logf("%d chosen keys: fixed key %d probe groups (capacity %d), keyed %d (capacity %d)", n, fixed, fcap, keyed, kcap)
	if fixed < n*n/64 || keyed > 2*n || fcap != 2048 {
		t.Fatalf("model: fixed %d, keyed %d, capacity %d: the keys do not collide as chosen", fixed, keyed, fcap)
	}
	src := fmt.Sprintf(`import std.collkeyed

const Keys: [%d]int = %s

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    a := sys.heap.Alloc()
    kb: [16]u8 = %s
    key := collkeyed.Key(kb[..])
    var m1 := Map[int, int].New(sys.arena)
    var m2 := Map[int, int].NewKeyed(sys.arena, key)
    var h1 := HeapMap[int, int].New(a)
    defer h1.Free(a)
    var h2 := HeapMap[int, int].NewKeyed(a, key)
    defer h2.Free(a)
    for k in Keys {
        try m1.Set(k, 0)
        try m2.Set(k, 0)
        try h1.Set(a, k, 0)
        try h2.Set(a, k, 0)
    }
    p1 := collkeyed.MapProbes(&m1)
    p2 := collkeyed.MapProbes(&m2)
    p3 := collkeyed.HeapProbes(&h1)
    p4 := collkeyed.HeapProbes(&h2)
    n := m1.Len() + m2.Len() + h1.Len() + h2.Len()
    try Print(mut out, "{p1} {p2} {p3} {p4} {n}\n", (p1, p2, p3, p4, n))
}
`, len(keys), mkIntArray(keys), k1.mkBytes())
	want := fmt.Sprintf("%d %d %d %d %d\n", fixed, keyed, fixed, keyed, 4*n)
	diffLines(t, collExec(t, collBuildWith(t, collKeyedExtra(), src, "--release")), want)
}
