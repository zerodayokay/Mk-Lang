// Why: under an order the clamps of core_sort.mk never run, so the order
// oracle cannot test them; relations that are no order can.
// What: TestStdColl_BlockMergeIter, TestStdColl_BlockMergeModelTargeted and
// TestStdColl_BlockMergeMkNoOrder, over SortStable and SortStableInPlace.
// How: the Mk sorts must give the models' exact order and less count under
// seven non-order relations; targeted inputs reach each buffer path.

package std

import (
	"cmp"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// TestStdColl_BlockMergeIter: for every n from 2 blkRun to 2^15, n = 2^k -
// 64 to 2^k + 64 for k = 15 to 20, and 3000 sizes between, each level of
// BlkIter tiles 0..n in whole pairs whose runs are dstep or dstep + 1 long,
// the base runs are blkRun to 2 blkRun long, and the shorter run of a pair
// is at most n / 2, the minimum scratch SortStable takes.
func TestStdColl_BlockMergeIter(t *testing.T) {
	t.Parallel()
	var ns []int
	for n := 2 * blkRun; n <= 1<<15; n++ {
		ns = append(ns, n)
	}
	for k := 15; k <= 20; k++ {
		for d := -64; d <= 64; d++ {
			ns = append(ns, 1<<k+d)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 3000; i++ {
		ns = append(ns, 1<<15+r.IntN(1<<20-1<<15+2))
	}
	back := 0
	for _, n := range ns {
		it := newBlkIter(n)
		for !it.done() {
			a := it.dec
			if b := it.next(); b-a < blkRun || b-a > 2*blkRun {
				t.Fatalf("n=%d: base run %d..%d", n, a, b)
			}
		}
		for {
			it.begin()
			for !it.done() {
				a := it.dec
				m := it.next()
				if m >= n {
					t.Fatalf("n=%d dstep=%d: an odd number of runs", n, it.dstep)
				}
				b := it.next()
				for _, l := range []int{m - a, b - m} {
					if l < it.dstep || l > it.dstep+1 {
						t.Fatalf("n=%d: a run of %d with dstep %d", n, l, it.dstep)
					}
				}
				if min(m-a, b-m) > n/2 {
					t.Fatalf("n=%d: shorter run %d over n / 2", n, min(m-a, b-m))
				}
				if m-a > b-m {
					back++
				}
			}
			if it.dec != n {
				t.Fatalf("n=%d: level ends at %d", n, it.dec)
			}
			if !it.nextLevel() {
				break
			}
		}
	}
	if back == 0 {
		t.Fatal("no pair has the longer run first: BufMerge's backward arm is not reached")
	}
}

// blkLevels is the dstep of each merge level of n.
func blkLevels(n int) []int {
	it := newBlkIter(n)
	var ls []int
	for {
		ls = append(ls, it.dstep)
		if !it.nextLevel() {
			return ls
		}
	}
}

// blkTargeted: the runs of one level of n, chosen at random, each get d
// distinct keys, d around that level's bufsize and 2 bufsize, placed at the
// far end, the near end or spread; the runs' key ranges overlap, descend or
// ascend, and each run is shuffled, reversed or sorted.
func blkTargeted(r *rand.Rand, n int) []int {
	keys := make([]int, n)
	if n < 2*blkRun {
		for i := range keys {
			keys[i] = r.IntN(4)
		}
		return keys
	}
	ls := blkLevels(n)
	l := r.IntN(len(ls))
	size := ls[l]
	bs := size/blkSqrt(size) + 1
	it := newBlkIter(n)
	for i := 0; i < l; i++ {
		it.nextLevel()
	}
	it.begin()
	order := r.IntN(3)
	for ri := 0; !it.done(); ri++ {
		a := it.dec
		ln := it.next() - a
		cands := []int{1, 2, 3, bs - 2, bs - 1, bs, bs + 1, 2*bs - 2, 2*bs - 1, 2 * bs, 2*bs + 1, ln}
		d := min(max(cands[r.IntN(len(cands))], 1), ln)
		off := []int{r.IntN(3) * 5, (1000 - ri) * 4 * n, ri * 4 * n}[order]
		place := r.IntN(3)
		vals := keys[a : a+ln]
		for i := range vals {
			switch place {
			case 0:
				if i >= ln-(d-1) {
					vals[i] = 2 * (i - (ln - d) + 1)
				}
			case 1:
				vals[i] = 2 * min(i, d-1)
			default:
				vals[i] = 2 * r.IntN(d)
			}
			vals[i] += off
		}
		switch r.IntN(3) {
		case 0:
			r.Shuffle(ln, func(i, j int) { vals[i], vals[j] = vals[j], vals[i] })
		case 1:
			slices.Reverse(vals)
		}
	}
	return keys
}

// blkTargetedSize: a size to 600, a size q 2^j + r with q around a square
// (a level of dstep q), or 2^k - 2 to 2^k + 2, at most maxN.
func blkTargetedSize(r *rand.Rand, maxN int) int {
	switch r.IntN(3) {
	case 0:
		return r.IntN(601)
	case 1:
		for {
			m := 4 + r.IntN(300)
			j := r.IntN(8)
			if n := (m*m-1+r.IntN(3))<<j + r.IntN(1<<j); n <= maxN {
				return n
			}
		}
	default:
		k := 5 + r.IntN(bits.Len(uint(maxN))-5)
		return min(1<<k-2+r.IntN(5), maxN)
	}
}

// TestStdColl_BlockMergeModelTargeted: over 2000 fixed blkTargeted cases to
// 2^17, both models give slices.SortStableFunc's order within blkBound,
// with no clamp used.
func TestStdColl_BlockMergeModelTargeted(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(77, 702))
	for c := 0; c < 2000; c++ {
		n := blkTargetedSize(r, 1<<17)
		keys := blkTargeted(r, n)
		want := blkWant(keys)
		for _, mc := range blkModelRuns() {
			buf := mc.buf
			m := blkRunModel(keys, buf, mc.cache)
			if !slices.Equal(m.s, want) {
				t.Fatalf("case %d n=%d buf=%v cache=%d: order differs from slices.SortStableFunc", c, n, buf, mc.cache)
			}
			if m.clamps != 0 {
				t.Fatalf("case %d n=%d buf=%v: %d clamps under an order", c, n, buf, m.clamps)
			}
			if n >= 2 && (m.cmps > blkBound(n, 2) || m.moves > blkBound(n, 24)) {
				t.Fatalf("case %d n=%d buf=%v: %d less calls, %d moves, over the bound", c, n, buf, m.cmps, m.moves)
			}
		}
	}
}

// blkNoOrderRel is relation rel, as Rel in blockNoOrderHead: 0 the order,
// then seven that are none: a coin per ordered pair of ids, always true,
// less-or-equal, a partial order (keys divisible by 3 incomparable), a
// NaN-like order (keys divisible by 7), rock-paper-scissors on k mod 3 (no
// transitivity), and the order with key 1 also below key 0 (one cycle).
func blkNoOrderRel(rel int, a, b *blkKey) bool {
	switch rel {
	case 0:
		return a.k < b.k
	case 1:
		h := uint64(a.id)*0x9E3779B97F4A7C15 ^ uint64(b.id)*0xC2B2AE3D27D4EB4F ^ 5
		h = (h ^ (h >> 31)) * 0xBF58476D1CE4E5B9
		return h>>63 == 1
	case 2:
		return true
	case 3:
		return a.k <= b.k
	case 4:
		return a.k%3 != 0 && b.k%3 != 0 && a.k < b.k
	case 5:
		return a.k%7 != 0 && b.k%7 != 0 && a.k < b.k
	case 6:
		return ((b.k-a.k)%3+3)%3 == 1
	default:
		return (a.k == 1 && b.k == 0) || a.k < b.k
	}
}

const blkNoOrderRels = 8

const blockNoOrderHead = `struct P {
    k: int
    id: int
}

Isqrt(n: int): int {
    var r := 0
    for (r + 1) * (r + 1) <= n {
        r += 1
    }
    return r
}

Rnd(x: mut u64): int {
    *x = *x ^ (*x >> 12)
    *x = *x ^ (*x << 25)
    *x = *x ^ (*x >> 27)
    return Trunc[int]((*x *% 0x2545F4914F6CDD1D) >> 33)
}

// Keys is blkNoOrderKeys in Go.
Keys(pat: int, d: int, xs: mut []P) {
    n := xs.Len()
    var x: u64 = Trunc[u64](n) *% 0x9E3779B97F4A7C15 +% Trunc[u64](pat * 131 + d) +% 1
    q := Isqrt(d)
    for i in 0..n {
        var k := 0
        if pat == 0 {
            k = Rnd(mut x) % d
        } else if pat == 1 {
            k = (i / d) * d + (d - 1 - i % d)
        } else if pat == 2 {
            c := i % d
            k = if c + 2 * q + 2 > d { c } else { 0 }
        } else {
            k = (n - i) / d
        }
        xs[i] = P(k: k, id: i)
    }
}

Coin(x: int, y: int, seed: u64): bool {
    var h := (Trunc[u64](x) *% 0x9E3779B97F4A7C15) ^ (Trunc[u64](y) *% 0xC2B2AE3D27D4EB4F) ^ seed
    h = (h ^ (h >> 31)) *% 0xBF58476D1CE4E5B9
    return (h >> 63) == 1
}

Rel(rel: int, a: &P, b: &P): bool {
    if rel == 0 {
        return a.k < b.k
    } else if rel == 1 {
        return Coin(a.id, b.id, 5)
    } else if rel == 2 {
        return true
    } else if rel == 3 {
        return a.k <= b.k
    } else if rel == 4 {
        return a.k % 3 != 0 && b.k % 3 != 0 && a.k < b.k
    } else if rel == 5 {
        return a.k % 7 != 0 && b.k % 7 != 0 && a.k < b.k
    } else if rel == 6 {
        return ((b.k - a.k) % 3 + 3) % 3 == 1
    }
    return (a.k == 1 && b.k == 0) || a.k < b.k
}

Digest(xs: []P): u64 {
    var h: u64 = 14695981039346656037
    for i in 0..xs.Len() {
        h = (h ^ Trunc[u64](xs[i].id)) *% 1099511628211
    }
    return h
}

Run(out: mut Writer, a: &Alloc, rel: int, pat: int, n: int, d: int): OutOfMemory|WriteError!void {
    var l := HeapList[P].New(a)
    defer l.Free(a)
    var l2 := HeapList[P].New(a)
    defer l2.Free(a)
    for _ in 0..n {
        try l.Push(a, P(k: 0, id: 0))
        try l2.Push(a, P(k: 0, id: 0))
    }
    Keys(pat, d, l.ItemsMut())
    Keys(pat, d, l2.ItemsMut())
    c := Cell[int].New(0)
    try l.SortStable(a, fn(x: &P, y: &P): bool {
        c.Store(c.Load() + 1)
        return Rel(rel, x, y)
    })
    cmps := c.Load()
    h := Digest(l.Items())
    c.Store(0)
    l2.SortStableInPlace(fn(x: &P, y: &P): bool {
        c.Store(c.Load() + 1)
        return Rel(rel, x, y)
    })
    cmps2 := c.Load()
    h2 := Digest(l2.Items())
    try Print(out, "{rel} {pat} {n} {d} {h} {cmps} {h2} {cmps2}\n", (rel, pat, n, d, h, cmps, h2, cmps2))
}
`

func blkNoOrderRnd(x *uint64) int {
	*x ^= *x >> 12
	*x ^= *x << 25
	*x ^= *x >> 27
	return int((*x * 0x2545F4914F6CDD1D) >> 33)
}

// blkNoOrderKeys is Keys in blockNoOrderHead: random keys below d, blocks
// of d reversed, chunks of d mostly 0 with about 2 sqrt(d) distinct keys
// at their far end, and descending chunks of d.
func blkNoOrderKeys(pat, d, n int) []int {
	x := uint64(n)*0x9E3779B97F4A7C15 + uint64(pat*131+d) + 1
	q := blkSqrt(d)
	keys := make([]int, n)
	for i := range keys {
		switch pat {
		case 0:
			keys[i] = blkNoOrderRnd(&x) % d
		case 1:
			keys[i] = (i/d)*d + (d - 1 - i%d)
		case 2:
			if c := i % d; c+2*q+2 > d {
				keys[i] = c
			}
		default:
			keys[i] = (n - i) / d
		}
	}
	return keys
}

type blkNoOrderCase struct{ rel, pat, n, d int }

// The models blkNoOrderModel runs: SortStableInPlace (StableSort) and
// SortStable (DriftSort).
const (
	noOrderInPlace = iota
	noOrderStable
)

// blkNoOrderModel sorts c's keys with the model kind under c's relation.
func blkNoOrderModel(c blkNoOrderCase, kind int) *blkModel[blkKey] {
	m := &blkModel[blkKey]{s: blkPairs(blkNoOrderKeys(c.pat, c.d, c.n)), less: func(a, b *blkKey) bool { return blkNoOrderRel(c.rel, a, b) }, cache: blkCacheCap(16, 8)}
	switch kind {
	case noOrderStable:
		m.driftStable()
	default:
		m.stableSort()
	}
	return m
}

// blkNoOrderCases: every relation and pattern over sizes 0 to 80, 2^k - 1
// to 2^k + 1 for k = 7 to 14 and 60 blkTargetedSize sizes to 2^14, with d
// one of 1, 2, 3, sqrt(n) - 1, sqrt(n), 2 sqrt(n) + 1, n + 1 or a level's
// block, bufsize, 2 bufsize or run; above 300 elements a relation that is
// no order keeps a case whose model used no clamp one time in four.
func blkNoOrderCases() []blkNoOrderCase {
	r := rand.New(rand.NewPCG(7, 8))
	var sizes []int
	for n := 0; n <= 80; n++ {
		sizes = append(sizes, n)
	}
	for k := 7; k <= 14; k++ {
		sizes = append(sizes, 1<<k-1, 1<<k, 1<<k+1)
	}
	for i := 0; i < 60; i++ {
		sizes = append(sizes, blkTargetedSize(r, 1<<14))
	}
	var cs []blkNoOrderCase
	for _, n := range sizes {
		q := max(blkSqrt(n), 1)
		ds := []int{1, 2, 3, max(q-1, 1), q, 2*q + 1, n + 1}
		if n >= 2*blkRun {
			ls := blkLevels(n)
			size := ls[r.IntN(len(ls))]
			b := blkSqrt(size)
			ds = append(ds, b, size/b+1, 2*(size/b+1), size)
		}
		for rel := 0; rel < blkNoOrderRels; rel++ {
			for pat := 0; pat < 4; pat++ {
				c := blkNoOrderCase{rel, pat, n, max(ds[r.IntN(len(ds))], 1)}
				if rel != 0 && n > 300 && blkNoOrderModel(c, noOrderInPlace).clamps == 0 && r.IntN(4) != 0 {
					continue
				}
				cs = append(cs, c)
			}
		}
	}
	return cs
}

// blockNoOrderProgram is the Mk program for cases, scratch alternating
// between the heap and the arena, and the lines the models give.
func blockNoOrderProgram(cases []blkNoOrderCase) (src, want string, clamped int) {
	var b, w strings.Builder
	b.WriteString(blockNoOrderHead)
	fmt.Fprintf(&b, "\nconst Cases: [%d]int = [", 4*len(cases))
	for i, c := range cases {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d, %d, %d, %d", c.rel, c.pat, c.n, c.d)
	}
	b.WriteString("]\n\nexport Main(sys: &System): OutOfMemory|WriteError!void {\n    var out := sys.Stdout()\n    h := sys.heap.Alloc()\n    ar := sys.arena.Alloc()\n")
	fmt.Fprintf(&b, "    for i in 0..%d {\n        a := if i %% 2 == 0 { h } else { ar }\n        try Run(mut out, a, Cases[4 * i], Cases[4 * i + 1], Cases[4 * i + 2], Cases[4 * i + 3])\n    }\n}\n", len(cases))
	for _, c := range cases {
		fmt.Fprintf(&w, "%d %d %d %d", c.rel, c.pat, c.n, c.d)
		for _, kind := range []int{noOrderStable, noOrderInPlace} {
			m := blkNoOrderModel(c, kind)
			if m.clamps > 0 {
				clamped++
			}
			ids := make([]int, len(m.s))
			for i, x := range m.s {
				ids[i] = x.id
			}
			if c.rel == 0 {
				want := blkPairs(blkNoOrderKeys(c.pat, c.d, c.n))
				slices.SortStableFunc(want, func(a, b blkKey) int { return cmp.Compare(a.k, b.k) })
				if !slices.Equal(m.s, want) {
					panic(fmt.Sprintf("model differs from slices.SortStableFunc at %v", c))
				}
			}
			fmt.Fprintf(&w, " %d %d", idDigest(ids), m.cmps)
		}
		w.WriteByte('\n')
	}
	return b.String(), w.String(), clamped
}

// TestStdColl_BlockMergeMkNoOrder: the Mk SortStable and SortStableInPlace
// give the models' order (the digest of the ids) and less count exactly, under
// the order and the seven relations of blkNoOrderRel, in dev and release:
// the check that the clamps, BlkRotate's guard and every path a relation
// that is no order reaches are the models' too. The cases go in programs
// of at most 3,000,000 elements each.
func TestStdColl_BlockMergeMkNoOrder(t *testing.T) {
	t.Parallel()
	cases := blkNoOrderCases()
	var chunks [][]blkNoOrderCase
	var cur []blkNoOrderCase
	sum := 0
	for _, c := range cases {
		if sum+c.n > 3_000_000 && len(cur) > 0 {
			chunks, cur, sum = append(chunks, cur), nil, 0
		}
		cur = append(cur, c)
		sum += c.n
	}
	chunks = append(chunks, cur)
	total := 0
	for _, ch := range chunks {
		src, want, clamped := blockNoOrderProgram(ch)
		total += clamped
		for _, flags := range [][]string{nil, {"--release"}} {
			diffLines(t, collExec(t, collBuildWith(t, nil, src, flags...)), want)
		}
	}
	if total == 0 {
		t.Fatal("no model sort used a clamp: the test no longer covers them")
	}
	t.Logf("%d cases in %d programs, %d model sorts used a clamp", len(cases), len(chunks), total)
}
