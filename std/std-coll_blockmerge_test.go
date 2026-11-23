// Why: the stable sorts have many size-dependent paths, and each must stay
// stable and do exactly what the Go models do.
// What: TestStdColl_BlockMergeMatchesGo over Slice[T].SortStable
// (mem_drift.mk, coll_sort.mk) and Slice[T].SortStableInPlace
// (core_sort.mk's StableSort).
// How: a Mk program sorts every blkPattern around the block sizes; digests
// must match slices.SortStableFunc and less counts the models'.

package std

import (
	"fmt"
	"strings"
	"testing"
)

const blockMergeHead = `struct P {
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

// Keys fills xs with pattern pat's keys, as blkPattern in Go.
Keys(pat: int, xs: mut []P) {
    n := xs.Len()
    var s: u64 = Trunc[u64](n) *% 1000003 +% Trunc[u64](pat) *% 7919 +% 1
    q := Isqrt(n)
    few := if q - 1 > 1 { q - 1 } else { 1 }
    for i in 0..n {
        var k := 0
        if pat == 1 {
            k = i
        } else if pat == 2 {
            k = n - i
        } else if pat == 3 {
            k = 7
        } else if pat == 4 {
            k = if i < n - i { i } else { n - i }
        } else if pat == 5 {
            k = i % 100
        } else if pat == 11 {
            k = i / 37
        } else if pat == 12 {
            k = (n - i) / 37
        } else if pat == 13 && i % 64 != 63 {
            k = i
        } else {
            s = s *% 6364136223846793005 +% 1442695040888963407
            r := Trunc[int](s >> 33)
            if pat == 0 {
                k = r % 1000000000
            } else if pat == 6 {
                k = r % 2
            } else if pat == 7 {
                k = r % 3
            } else if pat == 8 {
                k = r % 17
            } else if pat == 9 {
                k = r % few
            } else if pat == 10 {
                k = r % (2 * q + 1)
            } else {
                k = r % (n + 1)
            }
        }
        xs[i] = P(k: k, id: i)
    }
}

Digest(xs: []P): u64 {
    var h: u64 = 14695981039346656037
    for i in 0..xs.Len() {
        h = (h ^ Trunc[u64](xs[i].id)) *% 1099511628211
    }
    return h
}

// Run sorts case (pat, n) with SortStable and with SortStableInPlace, each
// counting its calls of less, and prints the digests and counts.
Run(out: mut Writer, a: &Alloc, pat: int, n: int): OutOfMemory|WriteError!void {
    var l := HeapList[P].New(a)
    defer l.Free(a)
    var l2 := HeapList[P].New(a)
    defer l2.Free(a)
    for _ in 0..n {
        try l.Push(a, P(k: 0, id: 0))
        try l2.Push(a, P(k: 0, id: 0))
    }
    Keys(pat, l.ItemsMut())
    Keys(pat, l2.ItemsMut())
    c := Cell[int].New(0)
    try l.SortStable(a, fn(x: &P, y: &P): bool {
        c.Store(c.Load() + 1)
        return x.k < y.k
    })
    cmps := c.Load()
    h := Digest(l.Items())
    c.Store(0)
    l2.SortStableInPlace(fn(x: &P, y: &P): bool {
        c.Store(c.Load() + 1)
        return x.k < y.k
    })
    cmps2 := c.Load()
    h2 := Digest(l2.Items())
    try Print(out, "{pat} {n} {h} {cmps} {h2} {cmps2}\n", (pat, n, h, cmps, h2, cmps2))
}
`

// blkCase is one (pattern, size) case.
type blkCase struct{ pat, n int }

// blockMergeCases: every pattern at every size of blkSizes(150, 1<<16);
// above 2^16, 2^k - 1, 2^k and 2^k + 1 to 2^20 under the patterns that
// reach the most paths (random, reversed, all equal, 3 distinct, sqrt(n) - 1
// and 2 sqrt(n) + 1 distinct, every 64th random).
func blockMergeCases() (small, large []blkCase) {
	for _, n := range blkSizes(150, 1<<16) {
		for pat := range blkPatternNames {
			small = append(small, blkCase{pat, n})
		}
	}
	for k := 17; k <= 20; k++ {
		for _, n := range []int{1<<k - 1, 1 << k, 1<<k + 1} {
			for _, pat := range []int{0, 2, 3, 7, 9, 10, 13} {
				large = append(large, blkCase{pat, n})
			}
		}
	}
	return small, large
}

// blockMergeProgram is the Mk program for cases, and the lines it must
// print: the oracle's digest and the models' counts.
func blockMergeProgram(cases []blkCase) (src, want string) {
	var b, w strings.Builder
	b.WriteString(blockMergeHead)
	fmt.Fprintf(&b, "\nconst Cases: [%d]int = [", 2*len(cases))
	for i, c := range cases {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d, %d", c.pat, c.n)
	}
	b.WriteString("]\n\nexport Main(sys: &System): OutOfMemory|WriteError!void {\n    var out := sys.Stdout()\n    a := sys.heap.Alloc()\n")
	fmt.Fprintf(&b, "    for i in 0..%d {\n        try Run(mut out, a, Cases[2 * i], Cases[2 * i + 1])\n    }\n}\n", len(cases))
	for _, c := range cases {
		keys := blkPattern(c.pat, c.n)
		want := blkWant(keys)
		ids := make([]int, len(want))
		for i, x := range want {
			ids[i] = x.id
		}
		h := idDigest(ids)
		fmt.Fprintf(&w, "%d %d %d %d %d %d\n", c.pat, c.n, h, driftRunModel(keys, false, 16).cmps, h, blkRunModel(keys, false, blkCacheCap(16, 8)).cmps)
	}
	return b.String(), w.String()
}

// TestStdColl_BlockMergeMatchesGo: a Mk program generates each case of
// blkPattern (random, sorted, reversed, all equal, organ pipe, sawtooth, 2, 3
// and 17 distinct keys, sqrt(n) - 1 and 2 sqrt(n) + 1 distinct keys, runs of
// 37 equal keys ascending and descending, sorted with every 64th key random)
// at every size around the sorts' block boundaries from 0 to 2^20 (blkSizes:
// each n to 150, 2^k - 1, 2^k, 2^k + 1 and the squares around 2^k, which set
// the block and buffer sizes), sorts it with both stable sorts, and prints the digest
// of the ids in order and the calls of less. The digests must be
// slices.SortStableFunc's (the stability and permutation oracle: a stable
// order is unique) and the counts the Go models' exactly
// (std-coll_blockmerge_model_test.go), the check that the Mk bodies are the
// algorithms the models state. Dev and release builds both run, dev to 2^18 +
// 1.
func TestStdColl_BlockMergeMatchesGo(t *testing.T) {
	t.Parallel()
	small, large := blockMergeCases()
	// A dev build runs the large part to 2^18 + 1 only, so the run stays
	// well inside collTimeout under a loaded gate (alone, the whole large
	// part took 48 s of its 120 in dev); release runs it to 2^20 + 1.
	var largeDev []blkCase
	for _, c := range large {
		if c.n <= 1<<18+1 {
			largeDev = append(largeDev, c)
		}
	}
	t.Logf("%d small cases (n <= 2^16), %d large (2^17 - 1 to 2^20 + 1), %d of them in dev", len(small), len(large), len(largeDev))
	for _, part := range []struct {
		name  string
		cases []blkCase
		flags []string
	}{
		{"small/dev", small, nil},
		{"small/release", small, []string{"--release"}},
		{"large/dev", largeDev, nil},
		{"large/release", large, []string{"--release"}},
	} {
		src, want := blockMergeProgram(part.cases)
		t.Run(part.name, func(t *testing.T) {
			t.Parallel()
			diffLines(t, collExec(t, collBuildWith(t, nil, src, part.flags...)), want)
		})
	}
}
