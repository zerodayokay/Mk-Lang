// Why: SortStable sorts through scratch memory by a port of Rust's driftsort,
// whose many size-dependent paths the Mk tests check against a model that
// must follow the Mk body exactly.
// What: the Go model of mem_drift.mk's DriftStable, DriftIn and DriftSort
// (driftStable, bufSort, drift and its helpers, on blkModel) and
// TestStdColl_DriftModelMatchesGo.
// How: statement for statement, counting less calls and moves, with a
// liveness map over s and the scratch: a move panics unless its source holds
// a value and its destination none, and less unless both arguments hold
// one, so the model checks that each element has exactly one live home at
// every call of less. Each function panics where the Mk body's range check
// would fail.

package std

import (
	"fmt"
	"math/bits"
	"slices"
	"testing"
)

// The constants of mem_drift.mk.
const (
	driftInsertMax = 20
	driftSmall     = 32
	driftSqrtRun   = 64
	driftFullBytes = 8000000
	driftStackB    = 4096
	driftMedRecLen = 64 // core_sort.mk's PdqMedRecLen
	driftSmallScr  = 48 // DriftSmallScratch
)

// driftScratchLen is DriftScratchLen for n elements of size bytes.
func driftScratchLen(n, size int) int {
	return max(n-n/2, min(n, driftFullBytes/size), driftSmallScr)
}

// driftRun is a run: its length and whether it is sorted.
type driftRun struct {
	n      int
	sorted bool
}

// elemSize is the element size the model's scratch is sized for: size, or
// 16 (blkKey) when it is unset.
func (m *blkModel[T]) elemSize() int {
	if m.size > 0 {
		return m.size
	}
	return 16
}

// driftStable is SortStable's body (DriftStable): StableSort up to
// driftInsertMax, else DriftSort through DriftScratchLen slots, on the
// stack or from its allocator alike.
func (m *blkModel[T]) driftStable() {
	n := len(m.s)
	if n <= driftInsertMax {
		m.stableSort()
		return
	}
	m.driftWith(n, driftScratchLen(n, m.elemSize()))
}

// bufSort models SortStable's half-slice fallback: StableSort up to
// driftInsertMax, else DriftSort through n / 2 scratch slots.
func (m *blkModel[T]) bufSort() {
	n := len(m.s)
	if n <= driftInsertMax {
		m.stableSort()
		return
	}
	m.driftWith(n, n/2)
}

// driftWith is DriftIn: DriftSort through cap slots, with the
// liveness map, which must end with every slot of s a value and the
// scratch empty.
func (m *blkModel[T]) driftWith(n, cap int) {
	m.live = make([]bool, n)
	for i := range m.live {
		m.live[i] = true
	}
	m.sbuf = make([]T, cap)
	m.slive = make([]bool, cap)
	m.drift(0, n, n <= 2*driftSmall)
	for i, l := range m.live {
		if !l {
			panic(fmt.Sprintf("the sort ends with a hole at %d", i))
		}
	}
	for j, l := range m.slive {
		if l {
			panic(fmt.Sprintf("the sort ends with a value in scratch slot %d", j))
		}
	}
	m.live, m.sbuf, m.slive = nil, nil, nil
}

// toB moves s[i] to scratch slot j, toS scratch slot j to s[i].
func (m *blkModel[T]) toB(i, j int) {
	if !m.live[i] || m.slive[j] {
		panic(fmt.Sprintf("a move from s[%d] to scratch slot %d moves a hole or onto a value", i, j))
	}
	m.sbuf[j] = m.s[i]
	m.live[i], m.slive[j] = false, true
	m.moves++
}

func (m *blkModel[T]) toS(j, i int) {
	if !m.slive[j] || m.live[i] {
		panic(fmt.Sprintf("a move from scratch slot %d to s[%d] moves a hole or onto a value", j, i))
	}
	m.s[i] = m.sbuf[j]
	m.live[i], m.slive[j] = true, false
	m.moves++
}

// inS moves s[j] to the hole s[i].
func (m *blkModel[T]) inS(j, i int) {
	if i == j {
		return
	}
	if !m.live[j] || m.live[i] {
		panic(fmt.Sprintf("a move from s[%d] to s[%d] moves a hole or onto a value", j, i))
	}
	m.s[i] = m.s[j]
	m.live[i], m.live[j] = true, false
	m.moves++
}

// seeB panics when scratch slot j holds no value.
func (m *blkModel[T]) seeB(j int) {
	if !m.slive[j] {
		panic(fmt.Sprintf("less reads scratch slot %d, which holds no value", j))
	}
}

// ltSB is less of s[i] and scratch slot j, ltBS of slot j and s[i], ltBB
// of two scratch slots.
func (m *blkModel[T]) ltSB(i, j int) bool {
	m.cmps++
	m.see(i)
	m.seeB(j)
	return m.less(&m.s[i], &m.sbuf[j])
}

func (m *blkModel[T]) ltBS(j, i int) bool {
	m.cmps++
	m.see(i)
	m.seeB(j)
	return m.less(&m.sbuf[j], &m.s[i])
}

func (m *blkModel[T]) ltBB(i, j int) bool {
	m.cmps++
	m.seeB(i)
	m.seeB(j)
	return m.less(&m.sbuf[i], &m.sbuf[j])
}

// driftDepth is DriftDepth.
func driftDepth(l, mid, r int, scale uint64) int {
	return bits.LeadingZeros64(scale*uint64(l+mid) ^ scale*uint64(mid+r))
}

// driftLimit is DriftLimit.
func driftLimit(n int) int {
	return 2 * (bits.Len(uint(n|1)) - 1)
}

// drift is DriftSort on s[lo..hi].
func (m *blkModel[T]) drift(lo, hi int, eager bool) {
	n := hi - lo
	if n < 2 {
		return
	}
	if len(m.sbuf) < n/2 {
		panic(fmt.Sprintf("DriftSort's check fails: cap %d for %d", len(m.sbuf), n))
	}
	un := uint64(n)
	scale := ((uint64(1) << 62) + un - 1) / un
	good := blkSqrt(n)
	if n <= driftSqrtRun*driftSqrtRun {
		good = min(n/2, driftSqrtRun)
	}
	var runs, depths [66]int
	var srt [66]bool
	top := 0
	scan := 0
	prev := driftRun{0, true}
	for {
		next := driftRun{0, true}
		depth := 0
		if scan < n {
			next = m.createRun(lo, hi, lo+scan, good, eager)
			depth = driftDepth(scan-prev.n, scan, scan+next.n, scale)
		}
		for top > 1 && depths[top-1] >= depth {
			left := driftRun{runs[top-1], srt[top-1]}
			l := left.n + prev.n
			prev = m.logicalMerge(lo+scan-l, lo+scan, left, prev)
			top--
		}
		runs[top], srt[top], depths[top] = prev.n, prev.sorted, depth
		top++
		if scan >= n {
			break
		}
		scan += next.n
		prev = next
	}
	if !prev.sorted {
		m.quick(lo, hi, driftLimit(n), -1)
	}
}

// createRun is DriftCreateRun at s[at..hi].
func (m *blkModel[T]) createRun(lo, hi, at, good int, eager bool) driftRun {
	l := hi - at
	if l >= good && l >= 2 {
		k := 2
		if m.lt(at+1, at) {
			for k < l && m.lt(at+k, at+k-1) {
				k++
			}
			if k >= good {
				for i, j := at, at+k-1; i < j; i, j = i+1, j-1 {
					m.swap(i, j)
				}
				return driftRun{k, true}
			}
		} else {
			for k < l && !m.lt(at+k, at+k-1) {
				k++
			}
			if k >= good {
				return driftRun{k, true}
			}
		}
	}
	if eager {
		k := min(l, driftSmall)
		m.smallSort(at, at+k)
		return driftRun{k, true}
	}
	return driftRun{min(l, good), false}
}

// logicalMerge is DriftLogicalMerge of the runs left and right in s[a..b].
func (m *blkModel[T]) logicalMerge(a, b int, left, right driftRun) driftRun {
	l := b - a
	if l > len(m.sbuf) || left.sorted || right.sorted {
		mid := a + left.n
		if !left.sorted {
			m.quick(a, mid, driftLimit(mid-a), -1)
		}
		if !right.sorted {
			m.quick(mid, b, driftLimit(b-mid), -1)
		}
		m.merge(a, mid, b)
		return driftRun{l, true}
	}
	return driftRun{l, false}
}

// quick is DriftQuick.
func (m *blkModel[T]) quick(a, b, limit, anc int) {
	for {
		l := b - a
		if l <= driftSmall {
			m.smallSort(a, b)
			return
		}
		if limit == 0 {
			m.drift(a, b, true)
			return
		}
		limit--
		pivot := m.pivot(a, b)
		eq := anc >= 0 && !m.lt(anc, pivot)
		nl, at, pat := 0, anc, 0
		if !eq {
			nl, at, pat = m.partition(a, b, pivot, false, at)
			eq = nl == 0
		}
		if eq {
			k, _, _ := m.partition(a, b, pivot, true, at)
			a += k
			anc = -1
			continue
		}
		m.quick(a+nl, b, limit, pat)
		b = a + nl
		if at >= a && at < b {
			anc = at
		} else {
			anc = -1
		}
	}
}

// med3 is PdqMed3, med3Rec PdqMed3Rec.
func (m *blkModel[T]) med3(a, b, c int) int {
	x := m.lt(a, b)
	y := m.lt(a, c)
	if x == y {
		if m.lt(b, c) != x {
			return c
		}
		return b
	}
	return a
}

func (m *blkModel[T]) med3Rec(a, b, c, n int) int {
	if n >= 8 {
		n8 := n / 8
		h := n8 / 2
		a = m.med3Rec(a+h, a+n8*3+h, a+n8*6+h, n8)
		b = m.med3Rec(b+h, b+n8*3+h, b+n8*6+h, n8)
		c = m.med3Rec(c+h, c+n8*3+h, c+n8*6+h, n8)
	}
	return m.med3(a, b, c)
}

// pivot is DriftPivot.
func (m *blkModel[T]) pivot(a, b int) int {
	l := b - a
	if l < driftMedRecLen {
		n8 := l / 8
		return m.med3(a, a+n8*4, a+n8*7)
	}
	n8 := l / 8
	h := n8 / 2
	return m.med3Rec(a+h, a+n8*3+h, a+n8*6+h, n8)
}

// partition is DriftPartition: it returns how many went left, where the
// element at t went (t unchanged when outside a..b) and where the pivot
// went.
func (m *blkModel[T]) partition(a, b, pivot int, left bool, t int) (int, int, int) {
	l := b - a
	if a < 0 || b > len(m.s) || l > len(m.sbuf) || pivot < a || pivot >= b {
		panic(fmt.Sprintf("DriftPartition's check fails: %d..%d pivot %d cap %d", a, b, pivot, len(m.sbuf)))
	}
	nl, r := 0, l
	pslot, tslot := 0, -1
	for i := a; i < b; i++ {
		r--
		if i == pivot {
			if left {
				pslot = nl
				nl++
			} else {
				pslot = r + nl
			}
			continue
		}
		var x bool
		if left {
			x = !m.ltS(pivot, i)
		} else {
			x = m.ltS(i, pivot)
		}
		d := nl
		if !x {
			d = r + nl
		}
		m.toB(i, d)
		if i == t {
			tslot = d
		}
		if x {
			nl++
		}
	}
	m.toB(pivot, pslot)
	for j := 0; j < nl; j++ {
		m.toS(j, a+j)
	}
	for j := 0; j < l-nl; j++ {
		m.toS(l-1-j, a+nl+j)
	}
	if t == pivot {
		tslot = pslot
	}
	at := t
	pos := func(d int) int {
		if d < nl {
			return a + d
		}
		return b - 1 - d + nl
	}
	if tslot >= 0 {
		at = pos(tslot)
	}
	return nl, at, pos(pslot)
}

// ltS is lt with the liveness of both arguments checked.
func (m *blkModel[T]) ltS(i, j int) bool {
	return m.lt(i, j)
}

// merge is DriftMerge of s[a..mid] and s[mid..b].
func (m *blkModel[T]) merge(a, mid, b int) {
	ll, rl := mid-a, b-mid
	if ll <= 0 || rl <= 0 {
		return
	}
	if a < 0 || b > len(m.s) || min(ll, rl) > len(m.sbuf) {
		panic(fmt.Sprintf("DriftMerge's check fails: %d..%d..%d cap %d", a, mid, b, len(m.sbuf)))
	}
	if ll <= rl {
		for j := 0; j < ll; j++ {
			m.toB(a+j, j)
		}
		l, r, d := 0, mid, a
		for l < ll && r < b {
			if !m.ltSB(r, l) {
				m.toS(l, d)
				l++
			} else {
				m.inS(r, d)
				r++
			}
			d++
		}
		for ; l < ll; l, d = l+1, d+1 {
			m.toS(l, d)
		}
		return
	}
	for j := 0; j < rl; j++ {
		m.toB(mid+j, j)
	}
	l, r, d := mid, rl, b
	for {
		d--
		if m.ltBS(r-1, l-1) {
			m.inS(l-1, d)
			l--
		} else {
			m.toS(r-1, d)
			r--
		}
		if l == a || r == 0 {
			break
		}
	}
	for j := 0; j < r; j++ {
		m.toS(j, l+j)
	}
}

// insertFrom is PdqInsertFrom on s[a..a + n], whose first k are sorted.
func (m *blkModel[T]) insertFrom(a, k, n int) {
	for i := a + k; i < a+n; i++ {
		if !m.lt(i, i-1) {
			continue
		}
		held := m.s[i]
		j := i
		for {
			m.s[j] = m.s[j-1]
			m.moves++
			j--
			if j == a {
				break
			}
			m.cmps++
			m.see(j - 1)
			if !m.less(&held, &m.s[j-1]) {
				break
			}
		}
		m.s[j] = held
		m.moves += 2
	}
}

// place is a slot of s (inS) or of the scratch.
type place struct {
	inS bool
	i   int
}

func (m *blkModel[T]) val(p place) *T {
	if p.inS {
		m.see(p.i)
		return &m.s[p.i]
	}
	m.seeB(p.i)
	return &m.sbuf[p.i]
}

// ltP is less of the values at two places; moveP moves a value from one
// place to another, which holds none.
func (m *blkModel[T]) ltP(x, y place) bool {
	m.cmps++
	return m.less(m.val(x), m.val(y))
}

func (m *blkModel[T]) moveP(from, to place) {
	if from == to {
		return
	}
	v := *m.val(from)
	if to.inS {
		if m.live[to.i] {
			panic(fmt.Sprintf("a move onto s[%d], which holds a value", to.i))
		}
		m.s[to.i], m.live[to.i] = v, true
	} else {
		if m.slive[to.i] {
			panic(fmt.Sprintf("a move onto scratch slot %d, which holds a value", to.i))
		}
		m.sbuf[to.i], m.slive[to.i] = v, true
	}
	if from.inS {
		m.live[from.i] = false
	} else {
		m.slive[from.i] = false
	}
	m.moves++
}

func sp(i int) place { return place{true, i} }
func bp(i int) place { return place{false, i} }

// smallSort is DriftSmallSort of s[a..b].
func (m *blkModel[T]) smallSort(a, b int) {
	l := b - a
	if l < 8 {
		m.insertion(a, b)
		return
	}
	if a < 0 || b > len(m.s) || l > driftSmall || len(m.sbuf) < 8 {
		panic(fmt.Sprintf("DriftSmallSort's check fails: %d..%d cap %d", a, b, len(m.sbuf)))
	}
	if len(m.sbuf) < l+16 {
		m.smallNarrow(a, b)
		return
	}
	h := l / 2
	k := 4
	if l >= 16 {
		k = 8
		m.sort8(sp(a), bp(0), bp(l))
		m.sort8(sp(a+h), bp(h), bp(l+8))
	} else {
		m.sort4(sp(a), bp(0))
		m.sort4(sp(a+h), bp(h))
	}
	m.extend(a, 0, k, h)
	m.extend(a+h, h, k, l-h)
	if !m.bidi(bp(0), l, sp(a)) {
		m.mergeUp(bp(0), h, bp(h), l-h, sp(a))
	}
}

// add is the place k slots after p.
func add(p place, k int) place { return place{p.inS, p.i + k} }

// extend is DriftExtend from s[src..] to scratch slots dst.. .
func (m *blkModel[T]) extend(src, dst, k, n int) {
	for i := k; i < n; i++ {
		m.moveP(sp(src+i), bp(dst+i))
		// PdqInsertFrom(dst, i, i + 1): one insertion step in the scratch.
		if !m.ltP(bp(dst+i), bp(dst+i-1)) {
			continue
		}
		held := m.sbuf[dst+i]
		j := i
		for {
			m.sbuf[dst+j] = m.sbuf[dst+j-1]
			m.moves++
			j--
			if j == 0 {
				break
			}
			m.cmps++
			m.seeB(dst + j - 1)
			if !m.less(&held, &m.sbuf[dst+j-1]) {
				break
			}
		}
		m.sbuf[dst+j] = held
		m.moves += 2
	}
}

// smallNarrow is DriftSmallNarrow of s[a..b].
func (m *blkModel[T]) smallNarrow(a, b int) {
	l := b - a
	h := l / 2
	k := 4
	if l >= 16 {
		k = 8
		m.sort8(sp(a), sp(a), bp(0))
		m.sort8(sp(a+h), sp(a+h), bp(0))
	} else {
		m.sort4(sp(a), bp(0))
		for j := 0; j < 4; j++ {
			m.moveP(bp(j), sp(a+j))
		}
		m.sort4(sp(a+h), bp(0))
		for j := 0; j < 4; j++ {
			m.moveP(bp(j), sp(a+h+j))
		}
	}
	m.insertFrom(a, k, h)
	m.insertFrom(a+h, k, l-h)
	m.merge(a, a+h, b)
}

// sort4 is DriftSort4 of the 4 values at v into the 4 places at dst.
func (m *blkModel[T]) sort4(v, dst place) {
	at := func(k int) place { return add(v, k) }
	c1 := m.ltP(at(1), at(0))
	c2 := m.ltP(at(3), at(2))
	ia, ib, ic, id := 0, 1, 2, 3
	if c1 {
		ia, ib = 1, 0
	}
	if c2 {
		ic, id = 3, 2
	}
	c3 := m.ltP(at(ic), at(ia))
	c4 := m.ltP(at(id), at(ib))
	imin, imax := ia, id
	if c3 {
		imin = ic
	}
	if c4 {
		imax = ib
	}
	icb, ibc := ib, ic
	if c4 {
		icb = ic
	}
	if c3 {
		ibc = ib
	}
	ul, ur := icb, ibc
	if c3 {
		ul = ia
	}
	if c4 {
		ur = id
	}
	c5 := m.ltP(at(ur), at(ul))
	ilo, ihi := ul, ur
	if c5 {
		ilo, ihi = ur, ul
	}
	for j, i := range []int{imin, ilo, ihi, imax} {
		m.moveP(at(i), add(dst, j))
	}
}

// sort8 is DriftSort8: the 8 values at v to the 8 places at dst through
// tmp.
func (m *blkModel[T]) sort8(v, dst, tmp place) {
	m.sort4(v, tmp)
	m.sort4(add(v, 4), add(tmp, 4))
	if !m.bidi(tmp, 8, dst) {
		m.mergeUp(tmp, 4, add(tmp, 4), 4, dst)
	}
}

// bidi is DriftBidi of the runs at src[0..n / 2] and src[n / 2..n] into
// dst; it moves nothing and returns false when the ends miss.
func (m *blkModel[T]) bidi(src place, n int, dst place) bool {
	h := n / 2
	at := make([]int, n)
	l, r, lb, rb, df, db := 0, h, h-1, n-1, 0, n-1
	for range h {
		if !m.ltP(add(src, r), add(src, l)) {
			at[df] = l
			l++
		} else {
			at[df] = r
			r++
		}
		df++
		if m.ltP(add(src, rb), add(src, lb)) {
			at[db] = lb
			lb--
		} else {
			at[db] = rb
			rb--
		}
		db--
	}
	if n%2 != 0 {
		if l <= lb {
			at[df] = l
			l++
		} else {
			at[df] = r
			r++
		}
	}
	if l != lb+1 || r != rb+1 {
		return false
	}
	for d := 0; d < n; d++ {
		m.moveP(add(src, at[d]), add(dst, d))
	}
	return true
}

// mergeUp is DriftMergeUp of the runs at x[0..nx] and y[0..ny] into dst.
func (m *blkModel[T]) mergeUp(x place, nx int, y place, ny int, dst place) {
	l, r, d := 0, 0, 0
	for l < nx && r < ny {
		if !m.ltP(add(y, r), add(x, l)) {
			m.moveP(add(x, l), add(dst, d))
			l++
		} else {
			m.moveP(add(y, r), add(dst, d))
			r++
		}
		d++
	}
	for ; l < nx; l, d = l+1, d+1 {
		m.moveP(add(x, l), add(dst, d))
	}
	for ; r < ny; r, d = r+1, d+1 {
		m.moveP(add(y, r), add(dst, d))
	}
}

// driftRunModel sorts keys' pairs by key with SortStable's full-scratch model
// (in == false) or its half-slice fallback model (in == true), for elements
// of size bytes.
func driftRunModel(keys []int, in bool, size int) *blkModel[blkKey] {
	m := &blkModel[blkKey]{s: blkPairs(keys), less: func(a, b *blkKey) bool { return a.k < b.k }, size: size}
	if in {
		m.bufSort()
	} else {
		m.driftStable()
	}
	return m
}

// TestStdColl_DriftModelMatchesGo: both bodies' model, over every pattern
// and every size of blkSizes(600, 1<<16) and at element sizes whose scratch
// is the whole slice or half of it, give slices.SortStableFunc's order,
// within blkBound's calls and moves.
func TestStdColl_DriftModelMatchesGo(t *testing.T) {
	t.Parallel()
	for _, n := range blkSizes(600, 1<<16) {
		for pat := range blkPatternNames {
			keys := blkPattern(pat, n)
			want := blkWant(keys)
			// 16 bytes: the whole slice; 4,000: half of it past 2,000.
			for _, size := range []int{16, 4000} {
				for _, in := range []bool{false, true} {
					m := driftRunModel(keys, in, size)
					if !slices.Equal(m.s, want) {
						t.Fatalf("in=%v size=%d %s n=%d: order differs from slices.SortStableFunc", in, size, blkPatternNames[pat], n)
					}
					if m.cmps > blkBound(n, 2) || m.moves > blkBound(n, 24) {
						t.Fatalf("in=%v size=%d %s n=%d: %d less calls, %d moves, over the bound", in, size, blkPatternNames[pat], n, m.cmps, m.moves)
					}
				}
			}
		}
	}
}
