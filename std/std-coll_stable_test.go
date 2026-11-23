// Why: SortStable, SortStableInPlace and Search are called from user code, so
// their order and cost must hold through check's Slice routing too.
// What: TestStdColl_StableSortMatchesGo over core_sort.mk, mem_drift.mk and
// coll_sort.mk.
// How: less counts against the Go models (driftRunModel, blkRunModel), order
// against slices.SortStableFunc, Search against sort.Search and its loop's
// call count (searchModel); also at compile time.

package std

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

const collStableHead = `view struct P {
    k: int
    id: int
    c: &Cell[int]
}

ByKey(a: &P, b: &P): bool {
    a.c.Store(a.c.Load() + 1)
    return a.k < b.k
}

// Search's predicate, with a key of another type than the element (K = int).
KeyLess(a: &P, q: &int): bool {
    a.c.Store(a.c.Load() + 1)
    return a.k < *q
}

// Digest of the ids in order, and whether the keys are sorted.
IdDigest(xs: mut []P): (h: u64, sorted: bool) {
    var h: u64 = 14695981039346656037
    var sorted := true
    for i in 0..xs.Len() {
        h = (h ^ Trunc[u64](xs[i].id)) *% 1099511628211
        if i > 0 && xs[i].k < xs[i - 1].k {
            sorted = false
        }
    }
    return (h: h, sorted: sorted)
}

// Sorts (key, id) pairs stably by key with SortStable, counting less calls,
// then searches for each query; prints the sort's count, the id digest,
// whether the keys are in order, and per query the index, then the
// search's total count. The same pairs sorted by SortStable with a literal
// that captures the counter (on a List,
// so the Slice routing through ItemsMut is used too) must give the same
// count and digest, else the line says so.
Run(out: mut Writer, a: &Arena, keys: []int, queries: []int): OutOfMemory|WriteError!void {
    // The counter lives in a, as the lists that hold a reference to it do.
    c := try a.Put(Cell[int].New(0))
    var l := List[P].New(a)
    var l2 := List[P].New(a)
    var l3 := List[P].New(a)
    for i in 0..keys.Len() {
        try l.Push(P(k: keys[i], id: i, c: &*c))
        try l2.Push(P(k: keys[i], id: i, c: &*c))
        try l3.Push(P(k: keys[i], id: i, c: &*c))
    }
    l3.SortStableInPlace(ByKey)
    cmpsip := c.Load()
    d3 := IdDigest(l3.Done())
    c.Store(0)
    try l2.SortStable(a.Alloc(), fn(x: &P, y: &P): bool {
        c.Store(c.Load() + 1)
        return x.k < y.k
    })
    cmpsw := c.Load()
    d2 := IdDigest(l2.Done())
    c.Store(0)
    xs := l.Done()
    try xs.SortStable(a.Alloc(), ByKey)
    cmps := c.Load()
    d := IdDigest(xs)
    h := d.h
    sorted := d.sorted
    if cmpsw != cmps || d2.h != h || d2.sorted != sorted {
        try Print(out, "SortStable by a literal differs: {cmpsw} {x} ", (cmpsw, x: d2.h))
    }
    if d3.h != h || d3.sorted != sorted {
        try Print(out, "SortStableInPlace differs: {x} ", (x: d3.h))
    }
    try Print(out, "{cmps} {cmpsip} {h} {sorted}", (cmps, cmpsip, h, sorted))
    c.Store(0)
    for q in queries {
        i := xs.Search(&q, KeyLess)
        try Print(out, " {i}", (i))
    }
    sc := c.Load()
    try Print(out, " / {sc}\n", (sc))
}

struct Q {
    k: int
    id: int
}

QLess(a: &Q, b: &Q): bool {
    return a.k < b.k
}

QKey(a: &Q, k: &int): bool {
    return a.k < *k
}

// The same sort and search, with no counter or allocator, so it runs at compile time.
Digest(): u64 {
    var xs: [300]Q = [Q(k: 0, id: 0); 300]
    var s: u64 = 5
    for i in 0..300 {
        s = s *% 6364136223846793005 +% 1442695040888963407
        xs[i] = Q(k: Trunc[int](s >> 33) % 23, id: i)
    }
    xs.SortStableInPlace(QLess)
    var h: u64 = 14695981039346656037
    for i in 0..300 {
        h = (h ^ Trunc[u64](xs[i].id)) *% 1099511628211
    }
    for k in 0..25 {
        h = (h ^ Trunc[u64](xs.Search(&k, QKey))) *% 1099511628211
    }
    return h
}

const Compiled = Digest()

// Digest's sort by SortStable, scratch from a, so it runs at compile
// time over EvalArena's allocator; 0 if a fails.
DigestIn(a: &Arena): u64 {
    var xs: [300]Q = [Q(k: 0, id: 0); 300]
    var s: u64 = 5
    for i in 0..300 {
        s = s *% 6364136223846793005 +% 1442695040888963407
        xs[i] = Q(k: Trunc[int](s >> 33) % 23, id: i)
    }
    xs.SortStable(a.Alloc(), QLess) else {
        return 0
    }
    var h: u64 = 14695981039346656037
    for i in 0..300 {
        h = (h ^ Trunc[u64](xs[i].id)) *% 1099511628211
    }
    for k in 0..25 {
        h = (h ^ Trunc[u64](xs.Search(&k, QKey))) *% 1099511628211
    }
    return h
}

const CompiledIn = DigestIn(EvalArena())
`

// goStable sorts (key, id) pairs by key with the Go models of SortStable
// and SortStableInPlace, for their less calls, and with slices.SortStableFunc,
// for the order, which both models must give; then for each query
// Search's index over the sorted keys by searchModel (which must agree with
// sort.Search), counting less calls.
func goStable(t *testing.T, keys, queries []int) string {
	xs := blkWant(keys)
	// P is 24 bytes (two ints and a reference), which sets the scratch and cache.
	m, mip := driftRunModel(keys, false, 24), blkRunModel(keys, false, blkCacheCap(24, 8))
	ids := make([]int, len(xs))
	for i, x := range xs {
		ids[i] = x.id
		if m.s[i] != x || mip.s[i] != x {
			t.Fatalf("the models and slices.SortStableFunc differ at %d", i)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %d %d true", m.cmps, mip.cmps, idDigest(ids))
	sc := 0
	for _, q := range queries {
		i := searchModel(len(xs), func(h int) bool { sc++; return xs[h].k < q })
		if w := sort.Search(len(xs), func(h int) bool { return !(xs[h].k < q) }); i != w {
			t.Fatalf("searchModel gives %d for %d, sort.Search %d", i, q, w)
		}
		fmt.Fprintf(&b, " %d", i)
	}
	fmt.Fprintf(&b, " / %d\n", sc)
	return b.String()
}

// searchModel is Slice[T].Search's loop (core_sort.mk) over n elements,
// where less(h) is less of element h and the key: the answer lies in
// i..i + n, each step asks of element i + ceil(n/2) - 1 and halves n
// whatever less answers, so it makes bits.Len(n) calls, and on a
// partitioned slice it returns sort.Search's index.
func searchModel(n int, less func(h int) bool) int {
	i := 0
	for n > 0 {
		h := n - n>>1
		if less(i + h - 1) {
			i += h
		}
		n >>= 1
	}
	return i
}

type byKeyCount struct {
	less func(i, j int) bool
	swap func(i, j int)
	n    int
}

func (b byKeyCount) Len() int           { return b.n }
func (b byKeyCount) Less(i, j int) bool { return b.less(i, j) }
func (b byKeyCount) Swap(i, j int)      { b.swap(i, j) }

// stableQueries: the first 20 keys, each key - 1 and + 1, and keys below
// the least and above the greatest.
func stableQueries(keys []int) []int {
	if len(keys) == 0 {
		return []int{0, -1}
	}
	lo, hi := slices.Min(keys), slices.Max(keys)
	qs := []int{lo - 1, hi + 1, lo, hi}
	for i := 0; i < len(keys) && i < 20; i++ {
		qs = append(qs, keys[i], keys[i]-1, keys[i]+1)
	}
	return qs
}

// TestStdColl_StableSortMatchesGo: Slice[T].SortStable (by a named comparator
// and by a capturing literal) and Slice[T].SortStableInPlace, called from user
// code. Over every pattern of sortCases (and every length 0 to 70) the number
// of less calls of SortStable and of SortStableInPlace equals their Go models'
// (driftRunModel, blkRunModel) on the same input, and the
// order of the ids equals slices.SortStableFunc's (a stable order is unique,
// so this is also the stability check). SortStable on a List with a capturing
// literal gives the same count and order. Search, with an int key over P
// elements (K differs from T), gives sort.Search's index with searchModel's
// number of less calls for keys present, absent, below the first and above
// the last.
// The same sorts and search run at compile time (a const with
// SortStableInPlace, and SortStable over EvalArena's allocator) give the
// native result.
func TestStdColl_StableSortMatchesGo(t *testing.T) {
	t.Parallel()
	var src, calls, want strings.Builder
	k := 0
	add := func(keys []int) {
		qs := stableQueries(keys)
		fmt.Fprintf(&src, "const K%d: [%d]int = %s\nconst Q%d: [%d]int = %s\n", k, len(keys), mkIntArray(keys), k, len(qs), mkIntArray(qs))
		fmt.Fprintf(&calls, "    try Run(mut out, sys.arena, K%d[..], Q%d[..])\n", k, k)
		want.WriteString(goStable(t, keys, qs))
		k++
	}
	var s uint64 = 17
	for n := 0; n <= 70; n++ {
		keys := make([]int, n)
		for i := range keys {
			s = s*6364136223846793005 + 1442695040888963407
			keys[i] = int(s>>33) % (n/3 + 1)
		}
		add(keys)
	}
	for _, n := range []int{1000, 20000} {
		for _, c := range sortCases(n) {
			add(c.keys)
		}
	}
	// The compile-time digest, from the Go model of Digest.
	type q struct{ k, id int }
	qs := make([]q, 300)
	s = 5
	for i := range qs {
		s = s*6364136223846793005 + 1442695040888963407
		qs[i] = q{int(s>>33) % 23, i}
	}
	slices.SortStableFunc(qs, func(a, b q) int { return cmp.Compare(a.k, b.k) })
	h := uint64(14695981039346656037)
	for _, x := range qs {
		h = (h ^ uint64(x.id)) * 1099511628211
	}
	for key := 0; key < 25; key++ {
		i := sort.Search(len(qs), func(j int) bool { return qs[j].k >= key })
		h = (h ^ uint64(i)) * 1099511628211
	}
	fmt.Fprintf(&want, "compiled %d native %d\n", h, h)
	fmt.Fprintf(&want, "compiled in %d native in %d\n", h, h)
	src.WriteString(collStableHead)
	src.WriteString("\nexport Main(sys: &System): OutOfMemory|WriteError!void {\n    var out := sys.Stdout()\n")
	src.WriteString(calls.String())
	src.WriteString("    c := Compiled\n    n := Digest()\n    try Print(mut out, \"compiled {c} native {n}\\n\", (c, n))\n")
	src.WriteString("    ci := CompiledIn\n    ni := DigestIn(sys.arena)\n    try Print(mut out, \"compiled in {ci} native in {ni}\\n\", (ci, ni))\n}\n")
	var extra map[string]string
	for _, flags := range [][]string{nil, {"--release"}} {
		diffLines(t, collExec(t, collBuildWith(t, extra, src.String(), flags...)), want.String())
	}
}
