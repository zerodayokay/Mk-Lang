// Why: the raw moves of SortStableInPlace that one range check guards must be driven
// to the edges of the slice, the cache and their runs on tests_sort.mk's inputs.
// What: TestStdColl_BlockMergeModelEdges, with tsKeyAt, tsLengths, tlCoin and
// tsTypes (Go copies of tests_sort.mk's and tests_list.mk's helpers).
// How: runs the Go model (blkModel.stableSort) over those inputs and records
// which edges each guarded move reaches; the model asserts every check holds.

package std

import (
	"testing"
)

// tsKeyAt is tests_sort.mk's TsKeyAt, with TlNext's generator.
func tsKeyAt(pat, i, n int, s *uint64, kmax int) int {
	switch pat {
	case 0:
		return i
	case 1:
		return n - i
	case 2:
		return 7
	case 5:
		return i % 7
	case 6:
		return min(i, n-i)
	}
	*s = *s*6364136223846793005 + 1442695040888963407
	r := int(*s >> 33)
	if pat == 3 {
		return r % 4
	}
	return r % kmax
}

// tsLengths is tests_sort.mk's TsLengths(big = true).
func tsLengths() []int {
	var ns []int
	for n := 0; n <= 70; n++ {
		ns = append(ns, n)
	}
	return append(ns, 96, 127, 128, 129, 200, 255, 256, 257, 1000, 1023, 1024, 1025, 5000)
}

// tlCoin is tests_list.mk's TlCoin.
func tlCoin(x, y int, seed uint64) bool {
	h := uint64(x)*0x9E3779B97F4A7C15 ^ uint64(y)*0xC2B2AE3D27D4EB4F ^ seed
	h = (h ^ (h >> 31)) * 0xBF58476D1CE4E5B9
	return h>>63 == 1
}

// tsTypes are tests_sort.mk's element types of 2 bytes and more as the
// model sees them: the cache each gets (BlkCacheCap), the longest slice
// TsAll sorts of it, and the key range its random pattern draws from.
var tsTypes = []struct {
	name       string
	cache, max int
	kmax       int
}{
	{"u16", 2048, 200, 256},
	{"u32", 1024, 5000, 65536},
	{"u64", 512, 5000, 0x7FFFFFFF},
	{"TsB9", 455, 5000, 65536},
	{"TsP16", 256, 5000, 0x7FFFFFFF},
	{"TsP40", 102, 5000, 0x7FFFFFFF},
	{"TsA16", 0, 5000, 0x7FFFFFFF},
	{"TsP4096", 1, 200, 0x7FFFFFFF},
	{"TsBig", 0, 200, 0x7FFFFFFF},
}

// TestStdColl_BlockMergeModelEdges runs the Go model of SortStable
// (blkModel.stableSort) over the inputs std/core/tests_sort.mk sorts for
// its elements of 2 bytes and more (every length of TsLengths, TsKeyAt's
// seven patterns under the order, and always true, always false and
// TsCoin's coin on pattern 4), each type with the stack cache it gets
// (tsTypes), and checks that they drive the raw moves one range check
// guards to the edges of the slice, the cache and their runs: SymSwapRange
// and BlkMergeInternal (a run of one, a run at the first slot and one ending
// at the last, a merge whose A or B run is one element and whose B run ends
// at the last slot), and the cache's BlkCacheIn, BlkCacheOut,
// BlkMergeToCache, BlkMergeFromCache and BlkMergeExternal (both ends of the
// slice, a full cache, runs of one). The model asserts every range check's
// conditions at every call, and that each element has exactly one live home
// at every call of less, so the sweep ending also shows the checks never fail
// on these inputs.
func TestStdColl_BlockMergeModelEdges(t *testing.T) {
	t.Parallel()
	edges := map[string]int{}
	rels := []func(a, b *blkKey) bool{
		func(a, b *blkKey) bool { return a.k < b.k },
		func(a, b *blkKey) bool { return true },
		func(a, b *blkKey) bool { return false },
		func(a, b *blkKey) bool { return tlCoin(a.id, b.id, 0x2545F4914F6CDD1D) },
	}
	for _, ty := range tsTypes {
		for _, n := range tsLengths() {
			if n > ty.max {
				continue
			}
			for ri, rel := range rels {
				for pat := 0; pat < 7; pat++ {
					if ri > 0 && pat != 4 {
						continue // TsAll runs the non-orders on pattern 4 only
					}
					seed := uint64(n)*1000003 + uint64(pat)*7919 + 1
					keys := make([]int, n)
					for i := range keys {
						keys[i] = tsKeyAt(pat, i, n, &seed, ty.kmax)
					}
					m := &blkModel[blkKey]{s: blkPairs(keys), less: rel, edges: edges, cache: ty.cache}
					m.stableSort()
				}
			}
		}
	}
	t.Logf("edges reached: %v", edges)
	// No call has b = 0 (SymRotate's second run starts after its first; a
	// block swap's second run is a later block or the second buffer, never
	// first), and no internal merge starts at 0 (while a level merges, the
	// front of the first run holds an internal buffer), so the first slot is
	// reached by a = 0 alone; BlkMergeInternal runs only without a cache
	// that holds an A block (TsA16, TsP4096, TsBig). The cache's moves and
	// merges reach both ends of the slice and runs of one.
	for _, e := range []string{
		"swap n=1", "swap a=0", "swap a+n=len", "swap b+n=len",
		"merge al=1", "merge bl=1", "merge be=len",
		"cachein n=1", "cachein from=0", "cachein from+n=len", "cachein at+n=cap",
		"cacheout to=0", "cacheout to+n=len",
		"tocache a=0", "tocache b=len", "fromcache to=0", "fromcache to+b=len",
		"external al=1", "external bl=1", "external be=len", "external al=cap",
	} {
		if edges[e] == 0 {
			t.Errorf("no input of tests_sort.mk reaches %s", e)
		}
	}
}
