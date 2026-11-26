// Why: SortStableInPlace's stack cache adds moves, compares and merges that the Go
// model of the block merge must follow statement for statement.
// What: the model's cache half: moveSC, moveCS, moveSS, clt, ltSC, cacheIn,
// cacheOut, mergeToCache, mergeFromCache, mergeExternal and cacheLevel2/4.
// How: each mirrors its Blk* function of core_sort_stable.mk; the rest of the model and
// its tests are in std-coll_blockmerge_model_test.go (split for the size rule).

package std

import "fmt"

// moveSC moves s[i] into cache slot j, moveCS cache slot j into s[i], and
// moveSS s[i] into s[d]; each is a move (1).
func (m *blkModel[T]) moveSC(i, j int) {
	if !m.live[i] || m.clive[j] {
		panic(fmt.Sprintf("a move from s[%d] to cache slot %d moves a hole or onto a value", i, j))
	}
	m.cbuf[j] = m.s[i]
	m.live[i], m.clive[j] = false, true
	m.moves++
}

func (m *blkModel[T]) moveCS(j, i int) {
	if !m.clive[j] || m.live[i] {
		panic(fmt.Sprintf("a move from cache slot %d to s[%d] moves a hole or onto a value", j, i))
	}
	m.s[i] = m.cbuf[j]
	m.live[i], m.clive[j] = true, false
	m.moves++
}

func (m *blkModel[T]) moveSS(i, d int) {
	if !m.live[i] || m.live[d] {
		panic(fmt.Sprintf("a move from s[%d] to s[%d] moves a hole or onto a value", i, d))
	}
	m.s[d] = m.s[i]
	m.live[i], m.live[d] = false, true
	m.moves++
}

// clt is less of cache slots i and j (BlkCacheLess), ltSC of s[i] and
// cache slot j.
func (m *blkModel[T]) clt(i, j int) bool {
	if i < 0 || i >= m.cache || j < 0 || j >= m.cache {
		panic(fmt.Sprintf("BlkCacheLess's check fails: %d %d of %d", i, j, m.cache))
	}
	m.cmps++
	m.seeC(i)
	m.seeC(j)
	return m.less(&m.cbuf[i], &m.cbuf[j])
}

func (m *blkModel[T]) ltSC(i, j int) bool {
	m.cmps++
	m.see(i)
	m.seeC(j)
	return m.less(&m.s[i], &m.cbuf[j])
}

// cacheIn is BlkCacheIn, cacheOut BlkCacheOut; each panics where the Mk
// body's range check fails.
func (m *blkModel[T]) cacheIn(from, n, at int) {
	if n <= 0 {
		return
	}
	if from < 0 || from > len(m.s)-n || at < 0 || at > m.cache-n {
		panic(fmt.Sprintf("BlkCacheIn's check fails: %d %d %d of %d, cache %d", from, n, at, len(m.s), m.cache))
	}
	if m.edges != nil {
		m.edge("cachein n=1", n == 1)
		m.edge("cachein from=0", from == 0)
		m.edge("cachein from+n=len", from+n == len(m.s))
		m.edge("cachein at+n=cap", at+n == m.cache)
	}
	for i := 0; i < n; i++ {
		m.moveSC(from+i, at+i)
	}
}

func (m *blkModel[T]) cacheOut(at, n, to int) {
	if n <= 0 {
		return
	}
	if at < 0 || at > m.cache-n || to < 0 || to > len(m.s)-n {
		panic(fmt.Sprintf("BlkCacheOut's check fails: %d %d %d of %d, cache %d", at, n, to, len(m.s), m.cache))
	}
	if m.edges != nil {
		m.edge("cacheout to=0", to == 0)
		m.edge("cacheout to+n=len", to+n == len(m.s))
	}
	for i := 0; i < n; i++ {
		m.moveCS(at+i, to+i)
	}
}

// mergeToCache is BlkMergeToCache: s[a..mid] and s[mid..b] merged into the
// cache slots at..at + b - a (BlkMergeRaw).
func (m *blkModel[T]) mergeToCache(a, mid, b, at int) {
	if a < 0 || a >= mid || mid >= b || b > len(m.s) || at < 0 || at > m.cache-(b-a) {
		panic(fmt.Sprintf("BlkMergeToCache's check fails: %d %d %d %d of %d, cache %d", a, mid, b, at, len(m.s), m.cache))
	}
	if m.edges != nil {
		m.edge("tocache a=0", a == 0)
		m.edge("tocache b=len", b == len(m.s))
	}
	ai, bi, d := a, mid, at
	for {
		if !m.lt(bi, ai) {
			m.moveSC(ai, d)
			ai++
			d++
			if ai == mid {
				for ; bi < b; bi++ {
					m.moveSC(bi, d)
					d++
				}
				return
			}
		} else {
			m.moveSC(bi, d)
			bi++
			d++
			if bi == b {
				for ; ai < mid; ai++ {
					m.moveSC(ai, d)
					d++
				}
				return
			}
		}
	}
}

// mergeFromCache is BlkMergeFromCache: cache slots [0..mid] and [mid..b]
// merged into s[to..to + b].
func (m *blkModel[T]) mergeFromCache(mid, b, to int) {
	if mid <= 0 || mid >= b || b > m.cache || to < 0 || to > len(m.s)-b {
		panic(fmt.Sprintf("BlkMergeFromCache's check fails: %d %d %d of %d, cache %d", mid, b, to, len(m.s), m.cache))
	}
	if m.edges != nil {
		m.edge("fromcache to=0", to == 0)
		m.edge("fromcache to+b=len", to+b == len(m.s))
	}
	ai, bi, d := 0, mid, to
	for {
		if !m.clt(bi, ai) {
			m.moveCS(ai, d)
			ai++
			d++
			if ai == mid {
				for ; bi < b; bi++ {
					m.moveCS(bi, d)
					d++
				}
				return
			}
		} else {
			m.moveCS(bi, d)
			bi++
			d++
			if bi == b {
				for ; ai < mid; ai++ {
					m.moveCS(ai, d)
					d++
				}
				return
			}
		}
	}
}

// mergeExternal is BlkMergeExternal: A's ae - as values in the cache's
// first slots, s[as..ae] holes, merged with s[bs..be] into s[as..be].
func (m *blkModel[T]) mergeExternal(as, ae, bs, be int) {
	al, bl := ae-as, be-bs
	if al <= 0 {
		return
	}
	if as < 0 || ae != bs || bl < 0 || be > len(m.s) || al > m.cache {
		panic(fmt.Sprintf("BlkMergeExternal's check fails: %d %d %d %d of %d, cache %d", as, ae, bs, be, len(m.s), m.cache))
	}
	if m.edges != nil {
		m.edge("external al=1", al == 1)
		m.edge("external bl=0", bl == 0)
		m.edge("external bl=1", bl == 1)
		m.edge("external be=len", be == len(m.s))
		m.edge("external al=cap", al == m.cache)
	}
	ac, bc := 0, 0
	if bl > 0 {
		for {
			d := as + ac + bc
			if !m.ltSC(bs+bc, ac) {
				m.moveCS(ac, d)
				ac++
				if ac >= al {
					break
				}
			} else {
				m.moveSS(bs+bc, d)
				bc++
				if bc >= bl {
					break
				}
			}
		}
	}
	for ; ac < al; ac++ {
		m.moveCS(ac, as+ac+bc)
	}
}

// cacheLevel2 is BlkCacheLevel2.
func (m *blkModel[T]) cacheLevel2(it *blkIter) {
	it.begin()
	for !it.done() {
		as := it.dec
		ae := it.next()
		be := it.next()
		if m.lt(be-1, as) {
			m.rotate(as, ae, be)
		} else if m.lt(ae, ae-1) {
			m.cacheIn(as, ae-as, 0)
			m.mergeExternal(as, ae, ae, be)
		}
	}
}

// cacheLevel4 is BlkCacheLevel4.
func (m *blkModel[T]) cacheLevel4(it *blkIter) {
	it.begin()
	for !it.done() {
		a1 := it.dec
		a1e := it.next()
		b1e := it.next()
		a2e := it.next()
		b2e := it.next()
		if m.lt(b1e-1, a1) {
			m.cacheIn(a1, a1e-a1, b1e-a1e)
			m.cacheIn(a1e, b1e-a1e, 0)
		} else if m.lt(a1e, a1e-1) {
			m.mergeToCache(a1, a1e, b1e, 0)
		} else {
			if !m.lt(a2e, a2e-1) && !m.lt(b1e, b1e-1) {
				continue
			}
			m.cacheIn(a1, b1e-a1, 0)
		}
		l1 := b1e - a1
		if m.lt(b2e-1, b1e) {
			m.cacheIn(b1e, a2e-b1e, l1+(b2e-a2e))
			m.cacheIn(a2e, b2e-a2e, l1)
		} else if m.lt(a2e, a2e-1) {
			m.mergeToCache(b1e, a2e, b2e, l1)
		} else {
			m.cacheIn(b1e, b2e-b1e, l1)
		}
		l := b2e - a1
		if m.clt(l-1, 0) {
			m.cacheOut(l1, l-l1, a1)
			m.cacheOut(0, l1, a1+(l-l1))
		} else if m.clt(l1, l1-1) {
			m.mergeFromCache(l1, l, a1)
		} else {
			m.cacheOut(0, l, a1)
		}
	}
}
