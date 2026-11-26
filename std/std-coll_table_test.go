// Why: std's Swiss table must probe, tombstone, grow and rehash exactly as
// specified, not merely find its keys.
// What: TestStdColl_TableMatchesGoModel, TestStdColl_HashStrMatchesSip13 and
// swissModel.
// How: std's Table and a Go model (Abseil's probing, EraseMetaOnly,
// DropDeletesWithoutResize) print control bytes and slots as matching digests.

package std

import (
	"fmt"
	"strings"
	"testing"
)

// swissModel is 4.8's table over int keys with values (16-byte groups).
type swissModel struct {
	ctrl              []byte // cap + 16 bytes, the first 16 mirrored at the end
	key, val          []int
	used, cap, growth int
	inPlace, resizes  int              // rule 6's two outcomes, counted for coverage
	hashFn            func(int) uint64 // nil: modelHash, the fixed key (New); else a NewKeyed key
}

const (
	mGroupWidth = 16
	mEmpty      = 0xFF
	mDeleted    = 0x80
)

// modelHash is New's hash of an int key: the fold hash.
func modelHash(k int) uint64 {
	return foldHashInt(k)
}

func (t *swissModel) set(i int, b byte) {
	t.ctrl[i] = b
	if i < mGroupWidth {
		t.ctrl[t.cap+i] = b
	}
}

func (t *swissModel) matchMask16(pos int, h2 byte) uint16 {
	var m uint16
	for k := 0; k < mGroupWidth; k++ {
		if t.ctrl[pos+k] == h2 {
			m |= 1 << k
		}
	}
	return m
}

func (t *swissModel) emptyMask16(pos int) uint16 {
	var m uint16
	for k := 0; k < mGroupWidth; k++ {
		if t.ctrl[pos+k] == mEmpty {
			m |= 1 << k
		}
	}
	return m
}

func (t *swissModel) freeMask16(pos int) uint16 {
	var m uint16
	for k := 0; k < mGroupWidth; k++ {
		if t.ctrl[pos+k] >= mDeleted {
			m |= 1 << k
		}
	}
	return m
}

func ctz16(m uint16) int {
	if m == 0 {
		return mGroupWidth
	}
	n := 0
	for m&1 == 0 {
		m >>= 1
		n++
	}
	return n
}

func clz16(m uint16) int {
	if m == 0 {
		return mGroupWidth
	}
	n := 0
	for m&(1<<(mGroupWidth-1)) == 0 {
		m <<= 1
		n++
	}
	return n
}

// lookup is TableLookup: the slot of k, else the first free slot on its path.
func (t *swissModel) lookup(hash uint64, k int) (at, free int) {
	if t.cap == 0 {
		return -1, -1
	}
	mask := t.cap - 1
	h2 := byte(hash >> 57)
	pos := int(hash>>7) & mask
	free = -1
	for stride := 0; ; {
		for m := t.matchMask16(pos, h2); m != 0; m &= m - 1 {
			i := (pos + ctz16(m)) & mask
			if t.key[i] == k && t.ctrl[i] < mDeleted {
				return i, -1
			}
		}
		if free < 0 {
			if f := t.freeMask16(pos); f != 0 {
				free = (pos + ctz16(f)) & mask
			}
		}
		if t.emptyMask16(pos) != 0 {
			return -1, free
		}
		stride += mGroupWidth
		pos = (pos + stride) & mask
	}
}

func (t *swissModel) firstFree(hash uint64) int {
	mask := t.cap - 1
	pos := int(hash>>7) & mask
	for stride := 0; ; {
		if m := t.freeMask16(pos); m != 0 {
			return (pos + ctz16(m)) & mask
		}
		stride += mGroupWidth
		pos = (pos + stride) & mask
	}
}

func (t *swissModel) claimAt(i int, hash uint64) int {
	if t.ctrl[i] == mEmpty {
		t.growth--
	}
	t.set(i, byte(hash>>57))
	t.used++
	return i
}

func (t *swissModel) place(hash uint64, slot int) int {
	if slot >= 0 && (t.growth > 0 || t.ctrl[slot] == mDeleted) {
		return t.claimAt(slot, hash)
	}
	t.reserve(hash)
	return t.claimAt(t.firstFree(hash), hash)
}

func (t *swissModel) reserve(hash uint64) {
	if t.cap == 0 {
		t.resize(8)
		return
	}
	i := t.firstFree(hash)
	if t.ctrl[i] == mDeleted || t.growth > 0 {
		return
	}
	if t.used <= t.cap*7/16 {
		t.inPlace++
		t.rehashInPlace()
		return
	}
	t.resizes++
	t.resize(t.cap * 2)
}

func (t *swissModel) resize(newcap int) {
	nt := &swissModel{ctrl: make([]byte, newcap+mGroupWidth), key: make([]int, newcap), val: make([]int, newcap),
		used: t.used, cap: newcap, growth: newcap*7/8 - t.used, inPlace: t.inPlace, resizes: t.resizes, hashFn: t.hashFn}
	for i := range nt.ctrl {
		nt.ctrl[i] = mEmpty
	}
	for i := 0; i < t.cap; i++ {
		if t.ctrl[i] < mDeleted {
			hash := t.hash(t.key[i])
			j := nt.firstFree(hash)
			nt.set(j, byte(hash>>57))
			nt.key[j], nt.val[j] = t.key[i], t.val[i]
		}
	}
	*t = *nt
}

func (t *swissModel) rehashInPlace() {
	cap, mask := t.cap, t.cap-1
	for i := 0; i < cap; i++ {
		if c := t.ctrl[i]; c == mDeleted {
			t.set(i, mEmpty)
		} else if c < mDeleted {
			t.set(i, mDeleted)
		}
	}
	for i := 0; i < cap; {
		if t.ctrl[i] != mDeleted {
			i++
			continue
		}
		hash := t.hash(t.key[i])
		p := int(hash>>7) & mask
		j := t.firstFree(hash)
		if ((i-p)&mask)/mGroupWidth == ((j-p)&mask)/mGroupWidth {
			t.set(i, byte(hash>>57))
			i++
			continue
		}
		if t.ctrl[j] == mEmpty {
			t.key[j], t.val[j] = t.key[i], t.val[i]
			t.set(j, byte(hash>>57))
			t.set(i, mEmpty)
			i++
		} else {
			t.key[i], t.key[j] = t.key[j], t.key[i]
			t.val[i], t.val[j] = t.val[j], t.val[i]
			t.set(j, byte(hash>>57))
		}
	}
	t.growth = cap*7/8 - t.used
}

func (t *swissModel) find(hash uint64, k int) int {
	at, _ := t.lookup(hash, k)
	return at
}

func (t *swissModel) erase(i int) {
	mask := t.cap - 1
	before := (i - mGroupWidth) & mask
	after := ctz16(t.emptyMask16(i))
	prior := clz16(t.emptyMask16(before))
	if after+prior < mGroupWidth {
		t.set(i, mEmpty)
		t.growth++
	} else {
		t.set(i, mDeleted)
	}
	t.used--
}

func (t *swissModel) digest() (uint64, uint64) {
	c, k := uint64(14695981039346656037), uint64(14695981039346656037)
	if t.cap > 0 {
		for _, b := range t.ctrl {
			c = (c ^ uint64(b)) * 1099511628211
		}
		for i := 0; i < t.cap; i++ {
			if t.ctrl[i] < mDeleted {
				k = (k ^ uint64(i)) * 1099511628211
				k = (k ^ uint64(t.key[i])) * 1099511628211
				k = (k ^ uint64(t.val[i])) * 1099511628211
			}
		}
	}
	return c, k
}

// modelRun is CollFixTableRun in Go.
func modelRun(keys []int, ops int, seed uint64, fill int) (string, *swissModel) {
	var out strings.Builder
	t := &swissModel{}
	s := seed
	var acc uint64
	nk := len(keys)
	gone := fill * 5 / 7
	for op := 0; op < ops; op++ {
		s = s*6364136223846793005 + 1442695040888963407
		r := int(s >> 33)
		k := keys[r%nk]
		what := (r / nk) % 8
		if fill > 0 {
			switch {
			case op < fill:
				k, what = keys[op], 0
			case op < fill+gone:
				k, what = keys[op-fill], 5
			default:
				k, what = keys[op-gone], 0
			}
		}
		hash := modelHash(k)
		switch {
		case what < 5:
			at, free := t.lookup(hash, k)
			if at < 0 {
				j := t.place(hash, free)
				t.key[j], t.val[j] = k, op
			} else {
				t.val[at] = op
			}
		case what < 7:
			if i := t.find(hash, k); i >= 0 {
				t.erase(i)
			}
		default:
			acc = acc*31 + uint64(t.find(hash, k)+1)
		}
		if op%61 == 0 || op == ops-1 {
			dc, ds := t.digest()
			fmt.Fprintf(&out, "%d %d %d %d %d %d %d\n", op, t.cap, t.used, t.growth, dc, ds, acc)
		}
	}
	return out.String(), t
}

// collidingKeys are n keys whose hashes share the low bits of H1 (bits 7 to
// 7 + bits), so in every table of up to 2^bits slots they start their probe
// at the same slot; with sameTag they also share H2, so every candidate in a
// group is a false match that costs an Eq.
func collidingKeys(n, bits int, sameTag bool) []int {
	var keys []int
	want := modelHash(0)
	mask := uint64(1)<<bits - 1
	for k := 0; len(keys) < n; k++ {
		h := modelHash(k)
		if (h>>7)&mask != (want>>7)&mask {
			continue
		}
		if sameTag && h>>57 != want>>57 {
			continue
		}
		keys = append(keys, k)
	}
	return keys
}

// scripted is the key list of a scripted run: fill keys that start their
// probe at one slot in tables up to 2^bits slots, so the fill is one long
// run and removing 5/7 of it leaves tombstones (EraseMetaOnly's deleted
// case), then fresh keys -1, -2, ... with ordinary hashes, which mostly land
// on empty slots outside the run: with growth at 0 and few keys live, that
// is the in-place rehash.
func scripted(fill, bits, ops int) []int {
	keys := collidingKeys(fill, bits, false)
	for i := 1; len(keys) < ops; i++ {
		keys = append(keys, -i)
	}
	return keys
}

// TestStdColl_TableMatchesGoModel runs std's table natively and in the Go
// model over three key sets: 300 ordinary keys (growth to 512 slots,
// tombstones, in-place rehashes), 120 keys that all start their probe at
// one slot in tables up to 1,024 slots (long chains through full groups,
// EraseMetaOnly's deleted case, rehash moves and swaps), and 40 keys that
// also share H2 (every match candidate is a false positive until the last).
// The control bytes, slot layout, used, growth and capacity agree after
// every 61st of 20,000 operations.
func TestStdColl_TableMatchesGoModel(t *testing.T) {
	t.Parallel()
	ordinary := make([]int, 300)
	for i := range ordinary {
		ordinary[i] = i*7919 - 1000
	}
	sets := []struct {
		name string
		keys []int
		ops  int
		fill int
	}{
		{"ordinary", ordinary, 20000, 0},
		{"same-h1", collidingKeys(120, 10, false), 20000, 0},
		{"same-h1-h2", collidingKeys(40, 10, true), 20000, 0},
		{"scripted-64", scripted(56, 6, 600), 600, 56},
		{"scripted-512", scripted(448, 9, 1900), 1900, 448},
		{"scripted-1024", scripted(896, 10, 3000), 3000, 896},
	}
	var src strings.Builder
	src.WriteString("import std.collfix\n\n")
	for i, s := range sets {
		fmt.Fprintf(&src, "const Keys%d: [%d]int = %s\n", i, len(s.keys), mkIntArray(s.keys))
	}
	src.WriteString("\nexport Main(sys: &System): WriteError!void {\n    var out := sys.Stdout()\n")
	var want strings.Builder
	inPlace := 0
	for i, s := range sets {
		seed := uint64(1000 + i)
		fmt.Fprintf(&src, "    try Print(mut out, \"set %d\\n\", ())\n", i)
		fmt.Fprintf(&src, "    try collfix.TableRun(mut out, sys.heap.Alloc(), Keys%d[..], %d, %d, %d)\n", i, s.ops, seed, s.fill)
		fmt.Fprintf(&want, "set %d\n", i)
		text, m := modelRun(s.keys, s.ops, seed, s.fill)
		want.WriteString(text)
		t.Logf("%s: %d keys, final capacity %d, %d in-place rehashes, %d resizes", s.name, len(s.keys), m.cap, m.inPlace, m.resizes)
		inPlace += m.inPlace
	}
	src.WriteString("}\n")
	if inPlace == 0 {
		t.Fatal("no key set reaches rule 6's in-place rehash; the test would not cover it")
	}
	got := collRun(t, src.String())
	diffLines(t, got, want.String())
}

// TestStdColl_HashStrMatchesSip13: HashOf on a str key (the one-shot SipStr)
// and the general Hasher path (NewHasher, str's Hash, HashFinish) both equal
// Go's SipHash-1-3 of the bytes and 0xFF, for every length from 0 to 40 and
// for multi-byte text.
func TestStdColl_HashStrMatchesSip13(t *testing.T) {
	t.Parallel()
	var cases []string
	for n := 0; n <= 40; n++ {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteByte(byte('a' + (i*7+n)%26))
		}
		cases = append(cases, b.String())
	}
	cases = append(cases, "é", "€uro", "ünïcødé ŧëxŧ, lönger than eight", "日本語のテキスト", "a\nb\tc")
	var src strings.Builder
	fmt.Fprintf(&src, "import std.collfix\n\nconst Cases: [%d]str = [", len(cases))
	for i, c := range cases {
		if i > 0 {
			src.WriteString(", ")
		}
		src.WriteString(mkString(c))
	}
	src.WriteString(`]

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    for c in Cases {
        a := collfix.HashStr(c)
        b := collfix.HasherStr(c)
        try Print(mut out, "{a} {b}\n", (a, b))
    }
}
`)
	var want strings.Builder
	for _, c := range cases {
		h := foldHashStr(c)
		fmt.Fprintf(&want, "%d %d\n", h, h)
	}
	diffLines(t, collRun(t, src.String()), want.String())
}

// hash is the key's hash under the table's key: modelHash for New's
// tables, hashFn for a NewKeyed one.
func (t *swissModel) hash(k int) uint64 {
	if t.hashFn != nil {
		return t.hashFn(k)
	}
	return modelHash(k)
}
