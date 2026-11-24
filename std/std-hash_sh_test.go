// Why: the fold hash's two lanes, its short read and its weakness to chosen
// keys each had a bug or a claim worth pinning.
// What: two lanes keep short streams' hashes, the short read is exact, the
// Mk vectors are the model's, New degrades under chosen keys, NewKeyed not.
// How: Go model comparisons, and one built program at dev and release.

package std

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// oneLaneFold is the fold hash with one accumulator, as it was before it
// had two lanes.
func oneLaneFold(b []byte) uint64 {
	n := len(b)
	m := n / 8
	acc := foldF0
	j := 0
	for ; j+2 <= m; j += 2 {
		acc = fold(binary.LittleEndian.Uint64(b[8*j:])^acc, binary.LittleEndian.Uint64(b[8*j+8:])^foldF1)
	}
	var t uint64
	for k := 8 * m; k < n; k++ {
		t |= uint64(b[k]) << (8 * (k - 8*m))
	}
	a, c := t, uint64(0)
	if m%2 == 1 {
		a, c = binary.LittleEndian.Uint64(b[8*(m-1):]), t
	}
	return fold(fold(a^acc, c^foldF2^uint64(n)), foldF3)
}

// TestStdHash_ShortStreamsKeepOneLane: a stream of fewer than 32 bytes
// has at most one pair, so the second lane changes nothing (the integer
// and two-field keys, every str key under 31 bytes); from 32 bytes on the
// second lane is live, and a change in its pair changes the hash.
func TestStdHash_ShortStreamsKeepOneLane(t *testing.T) {
	b := make([]byte, 200)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	for n := 0; n < 32; n++ {
		if foldHash(b[:n]) != oneLaneFold(b[:n]) {
			t.Errorf("length %d: the two-lane hash differs from the one-lane hash", n)
		}
	}
	for n := 32; n <= 200; n++ {
		c := append([]byte{}, b[:n]...)
		c[16] ^= 1 // in pair 1, lane 1's first pair
		if foldHash(c) == foldHash(b[:n]) {
			t.Errorf("length %d: a flip in lane 1's pair leaves the hash unchanged", n)
		}
	}
}

// shortWord is core_hash.mk's ShortWord in Go: fewer than 8 bytes from a
// fixed number of loads.
func shortWord(b []byte) uint64 {
	n := len(b)
	le32 := func(i int) uint64 { return uint64(binary.LittleEndian.Uint32(b[i:])) }
	if n >= 4 {
		return le32(0) | le32(n-4)<<(8*(n-4))
	}
	if n == 0 {
		return 0
	}
	m := n / 2
	return uint64(b[0]) | uint64(b[m])<<(8*m) | uint64(b[n-1])<<(8*(n-1))
}

// TestStdHash_ShortWordIsExact: the overlapping reads equal the byte
// loop they replace for every length 0..7, with distinct bytes and with
// every byte 0xFF (an or of two reads must not set a bit twice wrongly).
func TestStdHash_ShortWordIsExact(t *testing.T) {
	for _, fill := range []func(i int) byte{func(i int) byte { return byte(0x21 + i) }, func(int) byte { return 0xFF }, func(i int) byte { return byte(1 << i) }} {
		for n := 0; n < 8; n++ {
			b := make([]byte, n)
			var want uint64
			for i := range b {
				b[i] = fill(i)
				want |= uint64(b[i]) << (8 * i)
			}
			if got := shortWord(b); got != want {
				t.Errorf("n %d: shortWord %#x, the byte loop %#x", n, got, want)
			}
		}
	}
}

// TestStdHash_VectorsMatchModel pins the distinct-byte vectors of the
// tests_map.mk block "FoldStr and NewHasher give the model's vectors on
// distinct bytes" to the model.
func TestStdHash_VectorsMatchModel(t *testing.T) {
	want := map[int]uint64{1: 18274468622797274669, 2: 13296630765199605486, 3: 17460296919843759486, 4: 755444409502779170,
		5: 344463838674294112, 6: 5316956422697354375, 7: 7435488838870673405, 12: 7233994000455831090,
		31: 11801513395352122371, 33: 8617620052980872440, 47: 7929219764555937175, 70: 17524606700546427409}
	b := make([]byte, 80)
	for i := range b {
		b[i] = byte(0x21 + i)
	}
	for n, w := range want {
		if got := foldHashStr(string(b[:n])); got != w {
			t.Errorf("foldHashStr of %d distinct bytes = %d, tests_map.mk says %d", n, got, w)
		}
	}
}

var hashKeyedCore = `import std.raw
import std.simd

// HkPt is a two-field key; its Hash absorbs both fields, 16 bytes.
struct HkPt {
    x: u64
    y: u64
}

impl Eq for HkPt {
    Eq(&self, other: &HkPt): bool {
        return self.x == other.x && self.y == other.y
    }
}

impl Hash for HkPt {
    Hash(&self, h: mut Hasher) {
        self.x.Hash(h)
        self.y.Hash(h)
    }
}

// HkEqCalls is the number of Eq calls TableFind makes to find k: its
// probe walk, counted; -1 when k is absent.
HkEqCalls(t: &Table[HkPt, int], k: &HkPt): int {
    hash := TableHash(t, k)
    mask := t.cap - 1
    h2 := H2(hash)
    var pos := H1(hash) & mask
    var stride := 0
    var calls := 0
    for {
        g := GroupAt16(t.ctrl, pos)
        var m := (g == simd.Vec[u8, 16].Splat(h2)).Bits()
        for m != 0 {
            i := (pos +% raw.Ctz64(m)) & mask
            calls += 1
            if SlotRef(KeyAt(t.slots, i), 0).Eq(k) {
                return calls
            }
            m = m & (m -% 1)
        }
        if (g >= simd.Vec[u8, 16].Splat(CtrlEmpty)).Bits() != 0 {
            return -1
        }
        stride +%= 16
        pos = (pos +% stride) & mask
    }
}

// HkFill inserts the n chosen keys (x, FoldF1): under the fold hash the
// pair folds to fold(x ^ F0, 0) = 0, so every key has the same hash. The
// result: the Eq calls of finding every key, the keys Get returns with the
// right value, and 1 when every key has key 0's hash.
HkFill(m: mut Map[HkPt, int], n: int): [3]int {
    for i in 0..n {
        k := HkPt(x: Trunc[u64](i) *% 0x9e3779b97f4a7c15, y: FoldF1)
        m.Set(k, i) else {
            return [-1, -1, -1]
        }
    }
    var eq := 0
    var found := 0
    var same := 1
    k0 := HkPt(x: 0, y: FoldF1)
    h0 := TableHash(&m.t, &k0)
    for i in 0..n {
        k := HkPt(x: Trunc[u64](i) *% 0x9e3779b97f4a7c15, y: FoldF1)
        eq += HkEqCalls(&m.t, &k)
        if v := m.Get(&k) {
            if *v == i {
                found += 1
            }
        }
        if TableHash(&m.t, &k) != h0 {
            same = 0
        }
    }
    return [eq, found, same]
}

export HkRun(a: &Arena, keyed: bool, n: int): [3]int {
    if keyed {
        var kb: [16]u8 = [0; 16]
        for i in 0..16 {
            kb[i] = Trunc[u8](i * 29 + 3)
        }
        var m := Map[HkPt, int].NewKeyed(a, HashKeyOf(kb[..]))
        return HkFill(mut m, n)
    }
    var m := Map[HkPt, int].New(a)
    return HkFill(mut m, n)
}
`

const hashKeyedModule = `import std.core

export Run(a: &Arena, keyed: bool, n: int): [3]int {
    return core.HkRun(a, keyed, n)
}
`

// TestStdHash_NewDegradesUnderChosenKeysNewKeyedDoesNot is the
// reference's "A fixed function resists nothing: whoever picks n keys can give
// them one probe sequence and make each operation O(n)", and its remedy,
// NewKeyed, in a built program at dev and release. The keys (x, F1) are
// solved in closed form against the fold hash (every pair folds to 0), so
// the test needs no search and follows the hash if it changes: the model
// check below fails first. Under New all 2,000 keys share one hash and
// finding them costs n(n+1)/2 Eq calls, every slot of the shared probe
// sequence once per key behind it; under NewKeyed (SipHash-1-3) the same
// keys cost about one Eq each. Both maps still return every key. Counting
// Eq calls instead of timing keeps the test exact on a loaded machine.
func TestStdHash_NewDegradesUnderChosenKeysNewKeyedDoesNot(t *testing.T) {
	t.Parallel()
	const n = 2000
	h0 := foldHashPair(0, foldF1)
	for i := uint64(0); i < 100; i++ {
		if foldHashPair(i*0x9e3779b97f4a7c15, foldF1) != h0 {
			t.Fatalf("model: the key (%d * golden, F1) does not share the hash of (0, F1); the chosen keys no longer collide", i)
		}
	}
	src := `import std.hkfix

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    a := hkfix.Run(sys.arena, false, 2000)
    b := hkfix.Run(sys.arena, true, 2000)
    try Print(mut out, "{x} {y} {z}\n", (x: a[0], y: a[1], z: a[2]))
    try Print(mut out, "{x} {y} {z}\n", (x: b[0], y: b[1], z: b[2]))
}
`
	extra := map[string]string{"core/tests_hashkeyed.mk": hashKeyedCore, "hkfix/hkfix.mk": hashKeyedModule}
	for _, flags := range [][]string{nil, {"--release"}} {
		got := collExec(t, collBuildWith(t, extra, src, flags...))
		var newEq, newFound, newSame, keyEq, keyFound, keySame int
		if _, err := fmt.Sscanf(strings.TrimSpace(got), "%d %d %d\n%d %d %d", &newEq, &newFound, &newSame, &keyEq, &keyFound, &keySame); err != nil {
			t.Fatalf("%v: output %q: %v", flags, got, err)
		}
		t.Logf("%v: New %d Eq calls for %d keys (one hash: %d); NewKeyed %d Eq calls (one hash: %d)", flags, newEq, n, newSame, keyEq, keySame)
		if newFound != n || keyFound != n {
			t.Errorf("%v: New found %d, NewKeyed %d of %d keys", flags, newFound, keyFound, n)
		}
		if newSame != 1 || newEq != n*(n+1)/2 {
			t.Errorf("%v: New: one hash %d, %d Eq calls; want 1 and n(n+1)/2 = %d (each operation O(n))", flags, newSame, newEq, n*(n+1)/2)
		}
		if keySame != 0 || keyEq > 2*n {
			t.Errorf("%v: NewKeyed: one hash %d, %d Eq calls; want 0 and at most 2n = %d", flags, keySame, keyEq, 2*n)
		}
	}
}
