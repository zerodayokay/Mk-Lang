// Why: the Mk stable sorts are checked against models that count every less
// call and move, so the models must be right themselves.
// What: the Go model of StableSort (core_sort_stable.mk), SortStableInPlace's body
// sort, TestStdColl_BlockMergeModel* and TestStdColl_BlockMergeCounts, which
// also run the model of the sort through scratch (bufSort,
// std-coll_drift_model_test.go).
// How: the models follow the Mk bodies statement for statement; here they run
// alone over many more inputs, and under relations that are no order.

package std

import (
	"cmp"
	"fmt"
	"math/bits"
	"os"
	"slices"
	"sort"
	"strconv"
	"testing"
)

// blkRun is core_sort_stable.mk's BlkRun: below 2 * blkRun elements both sorts are
// one insertion sort, above it the base runs are blkRun to 2 * blkRun
// long.
const blkRun = 16

// blkModel is a slice under sort, with less and the counts. moves counts
// element moves: a swap is 3 (as SwapAt's read, write, write), a buffer
// move 1.
type blkModel[T any] struct {
	s     []T
	less  func(a, b *T) bool
	cmps  int
	moves int
	// clamps counts the clamps that changed a search's result: none under
	// a strict weak order, where each is a no-op.
	clamps int
	// edges, when not nil, counts the calls of swapRange, mergeInternal
	// and the cache's moves that reach an edge of the slice, the cache or
	// their runs (TestStdColl_BlockMergeModelEdges).
	edges map[string]int
	// cache is StableSort's stack cache in slots (BlkCacheCap: 4,096 /
	// the element's bytes, 0 for none), cbuf its values; live and clive
	// mark the slots of s and of the cache that hold a value. A move
	// panics unless its source holds one and its destination none, and
	// less unless both arguments hold one, so the model checks that each
	// element has exactly one live home at every call of less.
	cache int
	cbuf  []T
	live  []bool
	clive []bool
	// size is the element's bytes, which set the drift model's scratch
	// (std-coll_drift_model_test.go; 16 when 0), sbuf the scratch and
	// slive which of its slots hold a value.
	size  int
	sbuf  []T
	slive []bool
}

// blkCacheCap is core_sort_stable.mk's BlkCacheCap for an element of size bytes
// aligned to align.
func blkCacheCap(size, align int) int {
	if align > 8 || size == 0 || size > 4096 {
		return 0
	}
	return 4096 / size
}

// see panics when s[i] is a hole (while the model tracks them).
func (m *blkModel[T]) see(i int) {
	if m.live != nil && !m.live[i] {
		panic(fmt.Sprintf("less reads s[%d], a hole", i))
	}
}

// seeC panics when cache slot j holds no value.
func (m *blkModel[T]) seeC(j int) {
	if !m.clive[j] {
		panic(fmt.Sprintf("less reads cache slot %d, which holds no value", j))
	}
}

// clamp is min(max(i, lo), hi), counted when it changes i.
func (m *blkModel[T]) clamp(i, lo, hi int) int {
	if j := min(max(i, lo), hi); j != i {
		m.clamps++
		return j
	}
	return i
}

func (m *blkModel[T]) lt(i, j int) bool {
	m.cmps++
	m.see(i)
	m.see(j)
	return m.less(&m.s[i], &m.s[j])
}

// swap is SwapAt; a hole swapped with a value moves with it.
func (m *blkModel[T]) swap(i, j int) {
	if i == j {
		return
	}
	m.moves += 3
	m.s[i], m.s[j] = m.s[j], m.s[i]
	if m.live != nil {
		m.live[i], m.live[j] = m.live[j], m.live[i]
	}
}

// insertion is PdqInsertion: an element less than the one before it is
// held out (a move), each greater one before it shifts up a slot (a move),
// and it fills the hole (a move); the calls of less and the order are a
// swap per step's.
func (m *blkModel[T]) insertion(a, b int) {
	for i := a + 1; i < b; i++ {
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

// swapRange is SymSwapRange, which checks the whole run once and then swaps
// by raw moves: the model panics where that check would fail (where swap's
// i == j return would not, a == b), so a model test that ends also shows the
// check never fails on its inputs.
func (m *blkModel[T]) swapRange(a, b, n int) {
	if n > 0 && (a < 0 || b < 0 || a > len(m.s)-n || b > len(m.s)-n) {
		panic(fmt.Sprintf("SymSwapRange's check fails: %d %d %d of %d", a, b, n, len(m.s)))
	}
	if m.edges != nil && n > 0 {
		m.edge("swap n=1", n == 1)
		m.edge("swap a=0", a == 0)
		m.edge("swap b=0", b == 0)
		m.edge("swap a+n=len", a+n == len(m.s))
		m.edge("swap b+n=len", b+n == len(m.s))
	}
	for i := 0; i < n; i++ {
		m.swap(a+i, b+i)
	}
}

// edge counts name in m.edges when at holds.
func (m *blkModel[T]) edge(name string, at bool) {
	if at {
		m.edges[name]++
	}
}

// rotate is BlkRotate: SymRotate when both blocks are non-empty.
func (m *blkModel[T]) rotate(a, mid, b int) {
	if a > mid || mid > b || a < 0 || b > len(m.s) {
		panic(fmt.Sprintf("rotate %d %d %d of %d", a, mid, b, len(m.s)))
	}
	if a == mid || mid == b {
		return
	}
	i, j := mid-a, b-mid
	for i != j {
		if i > j {
			m.swapRange(mid-i, mid, j)
			i -= j
		} else {
			m.swapRange(mid-i, mid+j-i, i)
			j -= i
		}
	}
	m.swapRange(mid-i, mid, i)
}

// blkIter is BlkIter: WikiSort's iterator over the runs of one level, in
// integer steps of size/den with the remainder spread by a numerator.
type blkIter struct {
	size, den, dec, num, dstep, nstep int
}

func newBlkIter(n int) blkIter {
	p := 1 << (bits.Len(uint(n)) - 1)
	den := p / blkRun
	return blkIter{size: n, den: den, dstep: n / den, nstep: n % den}
}

func (it *blkIter) begin()     { it.dec, it.num = 0, 0 }
func (it *blkIter) done() bool { return it.dec >= it.size }
func (it *blkIter) next() int {
	it.dec += it.dstep
	it.num += it.nstep
	if it.num >= it.den {
		it.num -= it.den
		it.dec++
	}
	return it.dec
}
func (it *blkIter) nextLevel() bool {
	it.dstep += it.dstep
	it.nstep += it.nstep
	if it.nstep >= it.den {
		it.nstep -= it.den
		it.dstep++
	}
	return it.dstep < it.size
}

// blkSqrt is BlkSqrt, floor(sqrt(n)).
func blkSqrt(n int) int {
	x, r, b := n, 0, 1<<62
	for b > x {
		b /= 4
	}
	for b != 0 {
		if x >= r+b {
			x -= r + b
			r = r/2 + b
		} else {
			r /= 2
		}
		b /= 4
	}
	return r
}

// The searches are BlkBinaryFirst, BlkBinaryLast and the four Blk*ward
// searches: v is the index of the value searched for, a..b the range; each
// result lies in a..b whatever less answers.
func (m *blkModel[T]) binaryFirst(v, a, b int) int {
	if a >= b {
		return b
	}
	cur, size := a, b-a
	for size > 0 {
		off := size % 2
		size /= 2
		if m.lt(cur+size, v) {
			cur += size + off
		}
	}
	return cur
}

func (m *blkModel[T]) binaryLast(v, a, b int) int {
	if a >= b {
		return b
	}
	cur, size := a, b-a
	for size > 0 {
		off := size % 2
		size /= 2
		if !m.lt(v, cur+size) {
			cur += size + off
		}
	}
	return cur
}

func blkSkip(n, unique int) int {
	if s := n / unique; s > 1 {
		return s
	}
	return 1
}

func (m *blkModel[T]) firstForward(v, a, b, unique int) int {
	if b-a == 0 {
		return a
	}
	skip := blkSkip(b-a, unique)
	index := a + skip
	for m.lt(index-1, v) {
		if index >= b-skip {
			return m.binaryFirst(v, index, b)
		}
		index += skip
	}
	return m.binaryFirst(v, index-skip, index)
}

func (m *blkModel[T]) firstBackward(v, a, b, unique int) int {
	if b-a == 0 {
		return a
	}
	skip := blkSkip(b-a, unique)
	index := b - skip
	for index > a && !m.lt(index-1, v) {
		if index < a+skip {
			return m.binaryFirst(v, a, index)
		}
		index -= skip
	}
	return m.binaryFirst(v, index, index+skip)
}

func (m *blkModel[T]) lastForward(v, a, b, unique int) int {
	if b-a == 0 {
		return a
	}
	skip := blkSkip(b-a, unique)
	index := a + skip
	for !m.lt(v, index-1) {
		if index >= b-skip {
			return m.binaryLast(v, index, b)
		}
		index += skip
	}
	return m.binaryLast(v, index-skip, index)
}

func (m *blkModel[T]) lastBackward(v, a, b, unique int) int {
	if b-a == 0 {
		return a
	}
	skip := blkSkip(b-a, unique)
	index := b - skip
	for index > a && m.lt(v, index-1) {
		if index < a+skip {
			return m.binaryLast(v, a, index)
		}
		index -= skip
	}
	return m.binaryLast(v, index, index+skip)
}

// mergeInternal is BlkMergeInternal: A's values are in the buffer at buf,
// A's slots hold the buffer's; every move is a swap, so the buffer ends
// holding its own values in another order. Like swapRange, the model
// panics where the Mk body's one range check would fail.
func (m *blkModel[T]) mergeInternal(as, ae, bs, be, buf int) {
	ac, bc, ins := 0, 0, 0
	al, bl := ae-as, be-bs
	if bl > 0 && al > 0 {
		if as < 0 || ae > bs || be > len(m.s) || buf < 0 || buf > len(m.s)-al {
			panic(fmt.Sprintf("BlkMergeInternal's check fails: %d %d %d %d %d of %d", as, ae, bs, be, buf, len(m.s)))
		}
		if m.edges != nil {
			m.edge("merge al=1", al == 1)
			m.edge("merge bl=1", bl == 1)
			m.edge("merge as=0", as == 0)
			m.edge("merge be=len", be == len(m.s))
			m.edge("merge buf=0", buf == 0)
			m.edge("merge buf+al=len", buf+al == len(m.s))
		}
		for {
			if !m.lt(bs+bc, buf+ac) {
				m.swap(as+ins, buf+ac)
				ac++
				ins++
				if ac >= al {
					break
				}
			} else {
				m.swap(as+ins, bs+bc)
				bc++
				ins++
				if bc >= bl {
					break
				}
			}
		}
	}
	m.swapRange(buf+ac, as+ins, al-ac)
}

// mergeInPlace is BlkMergeInPlace: binary search and rotation, used only
// when a level found no second buffer.
func (m *blkModel[T]) mergeInPlace(as, ae, bs, be int) {
	if ae-as == 0 || be-bs == 0 {
		return
	}
	for {
		mid := m.binaryFirst(as, bs, be)
		amount := mid - ae
		m.rotate(as, ae, mid)
		if be == mid {
			break
		}
		bs = mid
		as += amount
		ae = bs
		na := m.binaryLast(as, as, ae)
		if na <= as {
			m.clamps++
			na = as + 1
		}
		as = na
		if ae-as == 0 {
			break
		}
	}
}

type blkPull struct{ from, to, count, a, b int }

// stableSort is StableSort.
func (m *blkModel[T]) stableSort() {
	n := len(m.s)
	if n < 2*blkRun {
		m.insertion(0, n)
		return
	}
	m.live = make([]bool, n)
	for i := range m.live {
		m.live[i] = true
	}
	m.cbuf = make([]T, m.cache)
	m.clive = make([]bool, m.cache)
	it := newBlkIter(n)
	for !it.done() {
		a := it.dec
		b := it.next()
		m.insertion(a, b)
	}
	for {
		m.level(&it)
		if !it.nextLevel() {
			break
		}
	}
	for i, l := range m.live {
		if !l {
			panic(fmt.Sprintf("StableSort ends with a hole at %d", i))
		}
	}
	for j, l := range m.clive {
		if l {
			panic(fmt.Sprintf("StableSort ends with a value in cache slot %d", j))
		}
	}
	m.live = nil
}

// level is BlkLevel: one level of WikiSort's merges, with the cache.
func (m *blkModel[T]) level(it *blkIter) {
	size := it.dstep
	if size < m.cache {
		if (size+1)*4 <= m.cache && size*4 <= len(m.s) {
			m.cacheLevel4(it)
			it.nextLevel()
		} else {
			m.cacheLevel2(it)
		}
		return
	}
	block := blkSqrt(size)
	bufsize := size/block + 1
	var pull [2]blkPull
	pi := 0
	b1s, b1e, b2s, b2e := 0, 0, 0, 0
	find := bufsize + bufsize
	separately := false
	if block <= m.cache {
		find = bufsize
	} else if find > size {
		find = bufsize
		separately = true
	}
	oneBuffer := block <= m.cache
	it.begin()
	for !it.done() {
		as := it.dec
		ae := it.next()
		bs := ae
		be := it.next()
		last := as
		count := 1
		index := 0
		for count < find {
			index = m.lastForward(last, last+1, ae, find-count)
			if index == ae {
				break
			}
			last = index
			count++
		}
		index = last
		if count >= bufsize {
			pull[pi] = blkPull{from: index, to: as, count: count, a: as, b: be}
			pi = 1
			if count == bufsize+bufsize {
				b1s, b1e, b2s, b2e = as, as+bufsize, as+bufsize, as+count
				break
			} else if find == bufsize+bufsize {
				b1s, b1e = as, as+count
				find = bufsize
			} else if oneBuffer {
				b1s, b1e = as, as+count
				break
			} else if separately {
				b1s, b1e = as, as+count
				separately = false
			} else {
				b2s, b2e = as, as+count
				break
			}
		} else if pi == 0 && count > b1e-b1s {
			b1s, b1e = as, as+count
			pull[pi] = blkPull{from: index, to: as, count: count, a: as, b: be}
		}
		last = be - 1
		count = 1
		for count < find {
			index = m.firstBackward(last, bs, last, find-count)
			if index == bs {
				break
			}
			last = index - 1
			count++
		}
		index = last
		if count >= bufsize {
			pull[pi] = blkPull{from: index, to: be, count: count, a: as, b: be}
			pi = 1
			if count == bufsize+bufsize {
				b1s, b1e, b2s, b2e = be-count, be-bufsize, be-bufsize, be
				break
			} else if find == bufsize+bufsize {
				b1s, b1e = be-count, be
				find = bufsize
			} else if oneBuffer {
				b1s, b1e = be-count, be
				break
			} else if separately {
				b1s, b1e = be-count, be
				separately = false
			} else {
				if pull[0].a == as {
					pull[0].b -= pull[1].count
				}
				b2s, b2e = be-count, be
				break
			}
		} else if pi == 0 && count > b1e-b1s {
			b1s, b1e = be-count, be
			pull[pi] = blkPull{from: index, to: be, count: count, a: as, b: be}
		}
	}
	for p := 0; p < 2; p++ {
		length := pull[p].count
		if pull[p].to < pull[p].from {
			index := pull[p].from
			for count := 1; count < length; count++ {
				i := m.firstBackward(index-1, pull[p].to, pull[p].from-(count-1), length-count)
				lo, hi := pull[p].to+length-1-count, pull[p].from-count
				i = m.clamp(i, lo, hi)
				m.rotate(i+1, pull[p].from+1-count, pull[p].from+1)
				pull[p].from = i + count
				index = i
			}
		} else if pull[p].to > pull[p].from {
			index := pull[p].from + 1
			for count := 1; count < length; count++ {
				i := m.lastForward(index, index, pull[p].to, length-count)
				lo, hi := index+1, pull[p].to-(length-1-count)
				i = m.clamp(i, lo, hi)
				m.rotate(pull[p].from, pull[p].from+count, i-1)
				pull[p].from = i - 1 - count
				index = i
			}
		}
	}
	bufsize = b1e - b1s
	block = size/bufsize + 1
	it.begin()
	for !it.done() {
		as := it.dec
		ae := it.next()
		bs := ae
		be := it.next()
		start := as
		skip := false
		for p := 0; p < 2 && !skip; p++ {
			if start != pull[p].a {
				continue
			}
			if pull[p].from > pull[p].to {
				as += pull[p].count
				skip = ae-as == 0
			} else if pull[p].from < pull[p].to {
				be -= pull[p].count
				skip = be-bs == 0
			}
		}
		if skip {
			continue
		}
		if m.lt(be-1, as) {
			m.rotate(as, ae, be)
		} else if m.lt(ae, ae-1) {
			bas, bae := as, ae
			fae := as + (bae-bas)%block
			ia := b1s
			for idx := fae; idx < bae; idx += block {
				m.swap(ia, idx)
				ia++
			}
			las, lae := as, fae
			lbs, lbe := 0, 0
			bbs, bbe := bs, bs+min(block, be-bs)
			bas += fae - as
			ia = b1s
			if lae-las <= m.cache {
				m.cacheIn(las, lae-las, 0)
			} else if b2e-b2s > 0 {
				m.swapRange(las, b2s, lae-las)
			}
			if bae-bas > 0 {
				for {
					if (lbe-lbs > 0 && !m.lt(lbe-1, ia)) || bbe-bbs == 0 {
						split := m.binaryFirst(ia, lbs, lbe)
						rem := lbe - split
						mina := bas
						for fa := mina + block; fa < bae; fa += block {
							if m.lt(fa, mina) {
								mina = fa
							}
						}
						m.swapRange(bas, mina, block)
						m.swap(bas, ia)
						ia++
						if lae-las <= m.cache {
							m.mergeExternal(las, lae, lae, split)
						} else if b2e-b2s > 0 {
							m.mergeInternal(las, lae, lae, split, b2s)
						} else {
							m.mergeInPlace(las, lae, lae, split)
						}
						if b2e-b2s > 0 || block <= m.cache {
							if block <= m.cache {
								m.cacheIn(bas, block, 0)
							} else {
								m.swapRange(bas, b2s, block)
							}
							m.swapRange(split, bas+block-rem, rem)
						} else {
							m.rotate(split, bas, bas+block)
						}
						las, lae = bas-rem, bas-rem+block
						lbs, lbe = lae, lae+rem
						bas += block
						if bae-bas == 0 {
							break
						}
					} else if bbe-bbs < block {
						m.rotate(bas, bbs, bbe)
						lbs, lbe = bas, bas+(bbe-bbs)
						bas += bbe - bbs
						bae += bbe - bbs
						bbe = bbs
					} else {
						m.swapRange(bas, bbs, block)
						lbs, lbe = bas, bas+block
						bas += block
						bae += block
						bbs += block
						if bbe > be-block {
							bbe = be
						} else {
							bbe += block
						}
					}
				}
			}
			if lae-las <= m.cache {
				m.mergeExternal(las, lae, lae, be)
			} else if b2e-b2s > 0 {
				m.mergeInternal(las, lae, lae, be, b2s)
			} else {
				m.mergeInPlace(las, lae, lae, be)
			}
		}
	}
	m.insertion(b2s, b2e)
	for p := 0; p < 2; p++ {
		unique := pull[p].count * 2
		if pull[p].from > pull[p].to {
			bs, be := pull[p].a, pull[p].a+pull[p].count
			for be-bs > 0 {
				index := m.firstForward(bs, be, pull[p].b, unique)
				amount := index - be
				m.rotate(bs, be, index)
				bs += amount + 1
				be += amount
				unique -= 2
			}
		} else if pull[p].from < pull[p].to {
			bs, be := pull[p].b-pull[p].count, pull[p].b
			for be-bs > 0 {
				index := m.lastBackward(be-1, pull[p].a, bs, unique)
				amount := bs - index
				m.rotate(index, bs, be)
				bs -= amount
				be -= amount + 1
				unique -= 2
			}
		}
	}
}

// blkKey is the 16-byte pair every test sorts, as the Mk tests' P.
type blkKey struct{ k, id int }

func blkPairs(keys []int) []blkKey {
	xs := make([]blkKey, len(keys))
	for i, k := range keys {
		xs[i] = blkKey{k, i}
	}
	return xs
}

// blkRunModel sorts keys' pairs by key with StableSort's model, or with
// DriftSort's half-slice model (16-byte elements) when buf is set, and returns
// the model with its counts.
func blkRunModel(keys []int, buf bool, cache int) *blkModel[blkKey] {
	m := &blkModel[blkKey]{s: blkPairs(keys), less: func(a, b *blkKey) bool { return a.k < b.k }, cache: cache}
	if buf {
		m.bufSort()
	} else {
		m.stableSort()
	}
	return m
}

// blkPattern is the keys of pattern pat at length n, as BlkPattern in the
// Mk program generates them (blkPatternNames): seeded by n and pat, so each
// case is fixed.
func blkPattern(pat, n int) []int {
	s := uint64(n)*1000003 + uint64(pat)*7919 + 1
	rnd := func() int {
		s = s*6364136223846793005 + 1442695040888963407
		return int(s >> 33)
	}
	xs := make([]int, n)
	for i := range xs {
		switch pat {
		case 0:
			xs[i] = rnd() % 1000000000
		case 1:
			xs[i] = i
		case 2:
			xs[i] = n - i
		case 3:
			xs[i] = 7
		case 4:
			xs[i] = min(i, n-i)
		case 5:
			xs[i] = i % 100
		case 6:
			xs[i] = rnd() % 2
		case 7:
			xs[i] = rnd() % 3
		case 8:
			xs[i] = rnd() % 17
		case 9:
			xs[i] = rnd() % max(1, blkSqrt(n)-1)
		case 10:
			xs[i] = rnd() % (2*blkSqrt(n) + 1)
		case 11:
			xs[i] = i / 37
		case 12:
			xs[i] = (n - i) / 37
		case 13:
			// sorted, then every 64th element replaced by a random key
			if i%64 == 63 {
				xs[i] = rnd() % (n + 1)
			} else {
				xs[i] = i
			}
		}
	}
	return xs
}

var blkPatternNames = []string{"random", "sorted", "reversed", "equal", "organ-pipe", "sawtooth", "distinct2", "distinct3", "distinct17", "sqrt-1", "2sqrt+1", "runs37", "desc37", "every64"}

// blkSizes is every n from 0 to upto, then 2^k - 1, 2^k and 2^k + 1 and the
// squares m^2 - 1, m^2, m^2 + 1 (m a power of two or one less), up to max.
func blkSizes(upto, maxN int) []int {
	var ns []int
	for n := 0; n <= upto; n++ {
		ns = append(ns, n)
	}
	seen := map[int]bool{}
	for _, n := range ns {
		seen[n] = true
	}
	add := func(n int) {
		if n > upto && n <= maxN && !seen[n] {
			seen[n] = true
			ns = append(ns, n)
		}
	}
	for k := 1; 1<<k <= maxN+1; k++ {
		add(1<<k - 1)
		add(1 << k)
		add(1<<k + 1)
		for _, r := range []int{1<<(k/2) - 1, 1 << (k / 2)} {
			add(r*r - 1)
			add(r * r)
			add(r*r + 1)
		}
	}
	slices.Sort(ns)
	return ns
}

// blkWant sorts keys' pairs with slices.SortStableFunc, the oracle.
func blkWant(keys []int) []blkKey {
	xs := blkPairs(keys)
	slices.SortStableFunc(xs, func(a, b blkKey) int { return cmp.Compare(a.k, b.k) })
	return xs
}

// blkBound is the bound both models' counts are held to: c n log2 n plus a
// constant per element, with log2 n = bits.Len(n).
func blkBound(n, c int) int {
	return c*n*bits.Len(uint(n)) + 64*n + 64
}

// TestStdColl_BlockMergeModelMatchesGo: both models, over every pattern and
// every size of blkSizes(600, 1<<16), give slices.SortStableFunc's order,
// and their less calls and moves stay within blkBound: O(n log n), where a
// SymMerge body is O(n log^2 n) in moves. StableSort's model
// runs with each cache of blkModelCaches.
func TestStdColl_BlockMergeModelMatchesGo(t *testing.T) {
	t.Parallel()
	for _, n := range blkSizes(600, 1<<16) {
		for pat := range blkPatternNames {
			keys := blkPattern(pat, n)
			want := blkWant(keys)
			for _, mc := range blkModelRuns() {
				buf, cache := mc.buf, mc.cache
				m := blkRunModel(keys, buf, cache)
				if !slices.Equal(m.s, want) {
					t.Fatalf("buf=%v cache=%d %s n=%d: order differs from slices.SortStableFunc", buf, cache, blkPatternNames[pat], n)
				}
				if m.cmps > blkBound(n, 2) || m.moves > blkBound(n, 24) {
					t.Fatalf("buf=%v cache=%d %s n=%d: %d less calls, %d moves, over the bound", buf, cache, blkPatternNames[pat], n, m.cmps, m.moves)
				}
				if m.clamps != 0 {
					t.Fatalf("buf=%v cache=%d %s n=%d: %d clamps changed a result under an order", buf, cache, blkPatternNames[pat], n, m.clamps)
				}
			}
		}
	}
}

// blkModelCaches are the caches StableSort's model runs with in the model
// tests: none (an element over 4,096 bytes or aligned over 8, or of zero
// size), a tiny one (3: only the first, uneven A block fits), and those of
// 40-, 16- and 8-byte elements (102, 256, 512).
var blkModelCaches = []int{0, 3, 102, 256, 512}

// blkModelRun is one run of a model test: DriftSort's half-slice model (buf),
// or StableSort's with a cache.
type blkModelRun struct {
	buf   bool
	cache int
}

// blkModelRuns is DriftSort's half-slice model, then StableSort's with each
// cache of blkModelCaches.
func blkModelRuns() []blkModelRun {
	runs := []blkModelRun{{buf: true}}
	for _, c := range blkModelCaches {
		runs = append(runs, blkModelRun{cache: c})
	}
	return runs
}

// blkNoOrderMax is the largest size TestStdColl_BlockMergeModelNoOrder
// runs; MK_BLK_NOORDER_MAX raises it for a one-off run.
func blkNoOrderMax() int {
	if v, err := strconv.Atoi(os.Getenv("MK_BLK_NOORDER_MAX")); err == nil && v > 0 {
		return v
	}
	return 1 << 13
}

// TestStdColl_BlockMergeModelNoOrder: under relations that are no strict
// weak order, over every pattern and every size of blkSizes(300,
// blkNoOrderMax()), both models end, stay in range (their rotate and Go's
// bounds checks panic otherwise) and leave a permutation; always false
// keeps the input order. The relations: a fixed coin per ordered pair of
// ids (tests_list.mk's TlChaos), always true, always false, which ignore
// the keys; and three that are an order but for ties or a few keys, the
// ones programs write by mistake: less-or-equal (Zig's std.sort.block
// warns it breaks that sort), a partial order whose keys divisible by 3 are
// incomparable with every key, and a float-like order whose keys divisible
// by 7 are NaN, less than nothing and with nothing less than them.
func TestStdColl_BlockMergeModelNoOrder(t *testing.T) {
	t.Parallel()
	coin := func(x, y int, seed uint64) bool {
		h := uint64(x)*0x9E3779B97F4A7C15 ^ uint64(y)*0xC2B2AE3D27D4EB4F ^ seed
		h = (h ^ (h >> 31)) * 0xBF58476D1CE4E5B9
		return h>>63 == 1
	}
	rels := []struct {
		name string
		less func(seed uint64) func(a, b *blkKey) bool
	}{
		{"coin", func(seed uint64) func(a, b *blkKey) bool {
			return func(a, b *blkKey) bool { return coin(a.id, b.id, seed) }
		}},
		{"true", func(uint64) func(a, b *blkKey) bool { return func(a, b *blkKey) bool { return true } }},
		{"false", func(uint64) func(a, b *blkKey) bool { return func(a, b *blkKey) bool { return false } }},
		{"<=", func(uint64) func(a, b *blkKey) bool { return func(a, b *blkKey) bool { return a.k <= b.k } }},
		{"partial", func(uint64) func(a, b *blkKey) bool {
			return func(a, b *blkKey) bool { return a.k%3 != 0 && b.k%3 != 0 && a.k < b.k }
		}},
		{"nan", func(uint64) func(a, b *blkKey) bool {
			return func(a, b *blkKey) bool { return a.k%7 != 0 && b.k%7 != 0 && a.k < b.k }
		}},
	}
	clamps := make([]int, len(rels))
	defer func() {
		t.Logf("clamps that changed a result, by relation: %v", clamps)
		if clamps[0] == 0 {
			t.Errorf("the coin relation reached no clamp: the test no longer covers them")
		}
	}()
	for _, n := range blkSizes(300, blkNoOrderMax()) {
		for pat := range blkPatternNames {
			keys := blkPattern(pat, n)
			for ri, rel := range rels {
				for _, mc := range blkModelRuns() {
					buf := mc.buf
					m := &blkModel[blkKey]{s: blkPairs(keys), less: rel.less(uint64(pat)), cache: mc.cache}
					if buf {
						m.bufSort()
					} else {
						m.stableSort()
					}
					clamps[ri] += m.clamps
					ids := make([]int, n)
					for i, x := range m.s {
						ids[i] = x.id
					}
					slices.Sort(ids)
					for i, id := range ids {
						if id != i {
							t.Fatalf("%s %s n=%d buf=%v: not a permutation", rel.name, blkPatternNames[pat], n, buf)
						}
					}
					if rel.name == "false" {
						for i, x := range m.s {
							if x.id != i {
								t.Fatalf("always false %s n=%d buf=%v: order changed at %d", blkPatternNames[pat], n, buf, i)
							}
						}
					}
				}
			}
		}
	}
}

// TestStdColl_BlockMergeCounts logs the less calls and moves, per n log2 n,
// of Go's sort.Stable (Go's stable_func algorithm) and of both models:
// every pattern at 2^10, 2^14 and 2^20, then the inputs of the two
// sortstable bench rows at 10^6 (tests/bench-lanes/std-defects8:
// splitmix64 keys % 1000; tests/bench/idiomatic: splitmix64 keys,
// reversed, a sawtooth of period 1000, 16 distinct keys, there at 10^7).
// Nothing is timed.
func TestStdColl_BlockMergeCounts(t *testing.T) {
	t.Parallel()
	row := func(name string, keys []int) {
		n := len(keys)
		xs := blkPairs(keys)
		gc, gm := 0, 0
		sort.Stable(byKeyCount{func(i, j int) bool { gc++; return xs[i].k < xs[j].k }, func(i, j int) { gm += 3; xs[i], xs[j] = xs[j], xs[i] }, n})
		w := blkRunModel(keys, false, blkCacheCap(16, 8))
		b := blkRunModel(keys, true, 0)
		nl := float64(n * bits.Len(uint(n)))
		t.Logf("n=%d %-12s per n log2 n: sort.Stable less %.2f moves %.2f | StableSort less %.2f moves %.2f | DriftSort less %.2f moves %.2f",
			n, name, float64(gc)/nl, float64(gm)/nl, float64(w.cmps)/nl, float64(w.moves)/nl, float64(b.cmps)/nl, float64(b.moves)/nl)
	}
	for _, n := range []int{1 << 10, 1 << 14, 1 << 20} {
		for pat, name := range blkPatternNames {
			row(name, blkPattern(pat, n))
		}
	}
	mix := func(z uint64) uint64 {
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		return z ^ (z >> 31)
	}
	const n = 1000000
	bench := func(f func(i int, st uint64) int) []int {
		keys := make([]int, n)
		st := uint64(1)
		for i := range keys {
			st += 0x9e3779b97f4a7c15
			keys[i] = f(i, st)
		}
		return keys
	}
	row("bench8%1000", bench(func(_ int, st uint64) int { return int(mix(st) % 1000) }))
	row("idiom-rand", bench(func(_ int, st uint64) int { return int(mix(st) >> 1) }))
	row("idiom-rev", bench(func(i int, _ uint64) int { return n - i }))
	row("idiom-saw", bench(func(i int, _ uint64) int { return i % 1000 }))
	row("idiom-16", bench(func(_ int, st uint64) int { return int(mix(st) % 16) }))
}
