// Why: the tagged mode runs Stacked Borrows on the evaluator's flat memory
// and needs per-byte-range state that plain evaluation never pays for.
//
// What: the tagged state of a run: tagState, tagAlloc (per-allocation
// segments), tseg (a byte range with its borrow stack, init bits and
// liveness), item, tlocal, taginfo (the tag table), protItem (call
// protectors), tframe and argUndo, with split, merge and the init helpers.
//
// How: an allocation's bytes are a sorted list of segments, split on demand
// and merged once there are more than 64. Tags are made at borrows, slices
// and function entry only; copies keep them. The pointer-word tag shadow
// itself lives in alloc.ptag (memory.go), so the functions that set, copy and
// clear pointer bits keep it in the same place.

package eval

import (
	"slices"
	"sort"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// noVal is the value register's "no pointer in flight" state (tagState.val).
const noVal = ^uint32(0)

// item is one entry of a segment's borrow stack: a tag, whether it grants
// writes (Unique), and the call id that protects it (0 for none). Item 0 of
// every stack is the owner, tag 0.
type item struct {
	tag    uint32
	unique bool
	prot   uint32
}

// tseg is one segment of an allocation: the bytes [lo, hi), their stack, and
// liveness below the allocation. local is the frame local the bytes
// belong to, -1 for padding and for every other allocation kind.
type tseg struct {
	lo, hi   uint32
	stack    []item
	interior bool
	ended    bool
	gen      uint32
	local    int32
}

// tlocal is one local of a frame allocation: its bytes, whether a linear
// value is owned there, and the paths already taken or narrowed.
type tlocal struct {
	lo, hi uint32
	owned  bool
	linear bool
	user   bool
	taken  [][]mir.Projection
}

// tagAlloc is the tagged state of one allocation number. Numbers are never
// reused within an evaluation, so a dead allocation keeps its record and the
// span that ended it.
type tagAlloc struct {
	made   bool
	frame  bool
	dead   bool
	died   source.Span
	segs   []tseg
	init   []uint64 // frame allocations: one bit per written byte
	locals []tlocal
	// slot is, per local of a frame, the local whose value it last took by
	// a whole move (t = move x into a temporary t), plus one; 0 for none.
	// It finds the source of an argument slot (as borrow's slot restore does).
	slot []mir.LocalID
	// fnItem and body are the frame's function and MIR (Where strings).
	fnItem types.ItemID
	body   *mir.Body
	size   uint32
	// resets are the ranges of own memory an arena's Reset ended, in order
	// (tagarena.go): VDeadScope's Removed there.
	resets []resetRec
}

// taginfo is one tag's record: its allocation and activation, its
// permission, and the sites that made and first removed it.
type taginfo struct {
	alloc   uint32
	gen     uint32
	unique  bool
	made    source.Span
	removed source.Span
}

// protItem is one item a call's entry retag protected.
type protItem struct {
	alloc, lo, hi, tag uint32
}

// tframe is the tagged state of one frame: its call id and the items its
// entry retag protected.
type tframe struct {
	call uint32
	prot []protItem
	// undo is the caller's state of each place a fallible call's argument
	// moved out of, restored on the call's error edge (tgErrorEdge).
	undo []argUndo
}

// argUndo is one moved argument's place in the caller's frame before the
// move: its bytes' init bits and its local's ownership.
type argUndo struct {
	local  mir.LocalID
	owned  bool
	lo, hi uint32
	init   []bool
}

// tagState is one tagged run's state (m.tg; nil in plain mode).
type tagState struct {
	opt TagOptions
	// allocs is indexed by allocation number; a record never moves, so a
	// pointer to one stays valid while the table grows.
	allocs []*tagAlloc
	tags   []taginfo
	// val is the tag of the pointer value in flight between the load that
	// read it and the store that writes it (the tag is read with the word
	// and written with it, never recomputed); noVal when none.
	val    uint32
	calls  uint32
	frames []tframe
	// callItems names each call id's function (VProtected's Holder).
	callItems []types.ItemID
	viol      []Violation
	// facts are the types VFacts was reported for (once per type).
	facts map[types.TypeID]bool
	// rng is the run's Seed stream (Cond, the schedule; taghost.go).
	rng uint64
	// frng is FailAlloc's stream, and armed is set once the entry started.
	frng  uint64
	armed bool
	// startList is the schedule taken, one yield point per spawned task.
	startList []uint32
	// task is the host's tasks and scopes (taghost.go).
	task tasking
	// race is the interior race check's state (tagrace.go).
	race raceState
	// resetItem is std's Arena.Reset, whose call ends what the arena held
	// (tagarena.go); NoItem when the program has no std arena.
	resetItem types.ItemID
}

func newTagState(opt *TagOptions) *tagState {
	tg := &tagState{val: noVal, tags: make([]taginfo, 1, 64), facts: map[types.TypeID]bool{}}
	if opt != nil {
		tg.opt = *opt
	}
	if tg.opt.Max <= 0 {
		tg.opt.Max = 1
	}
	tg.rng = tg.opt.Seed
	tg.frng = tg.opt.Seed ^ 0x4661696c416c6c63 // "FailAllc"
	return tg
}

// rec returns allocation a's record, growing the table.
func (tg *tagState) rec(a uint32) *tagAlloc {
	for int(a) >= len(tg.allocs) {
		tg.allocs = append(tg.allocs, nil)
	}
	ta := tg.allocs[a]
	if ta == nil {
		ta = &tagAlloc{}
		tg.allocs[a] = ta
	}
	return ta
}

// known is allocation a's record when one exists, nil otherwise.
func (tg *tagState) known(a uint32) *tagAlloc {
	if int(a) < len(tg.allocs) {
		return tg.allocs[a]
	}
	return nil
}

// newTag makes a fresh tag over allocation a in activation gen.
func (tg *tagState) newTag(a, gen uint32, unique bool, at source.Span) uint32 {
	if uint64(len(tg.tags)) >= uint64(noVal) {
		panic(&stop{err: &types.EvalError{Kind: types.EvalRefused}, tagged: tagSkipped, skip: "tags exhausted"})
	}
	t := uint32(len(tg.tags))
	tg.tags = append(tg.tags, taginfo{alloc: a, gen: gen, unique: unique, made: at})
	return t
}

// ownerOnly is a fresh stack holding the owner alone; shared marks a frozen
// owner (a const copy).
func ownerOnly(shared bool) []item { return []item{{tag: 0, unique: !shared}} }

// find returns the index of the segment holding byte o (o < size).
func (ta *tagAlloc) find(o uint32) int {
	return sort.Search(len(ta.segs), func(i int) bool { return ta.segs[i].hi > o })
}

// split makes segment boundaries at lo and hi (lo < hi <= size) and returns
// the index range [i, j) of the segments covering [lo, hi).
func (ta *tagAlloc) split(lo, hi uint32) (int, int) {
	i := ta.find(lo)
	if s := ta.segs[i]; s.lo < lo {
		left := s
		left.hi = lo
		left.stack = slices.Clone(s.stack)
		ta.segs[i].lo = lo
		ta.segs = slices.Insert(ta.segs, i, left)
		i++
	}
	j := ta.find(hi - 1)
	if s := ta.segs[j]; s.hi > hi {
		right := s
		right.lo = hi
		right.stack = slices.Clone(s.stack)
		ta.segs[j].hi = hi
		ta.segs = slices.Insert(ta.segs, j+1, right)
	}
	return i, j + 1
}

// merge joins adjacent segments with equal state once an allocation holds
// more than 64, so the range map stays short.
func (ta *tagAlloc) merge() {
	if len(ta.segs) <= 64 {
		return
	}
	out := ta.segs[:1]
	for _, s := range ta.segs[1:] {
		p := &out[len(out)-1]
		if p.interior == s.interior && p.ended == s.ended && p.gen == s.gen && p.local == s.local && slices.Equal(p.stack, s.stack) {
			p.hi = s.hi
			continue
		}
		out = append(out, s)
	}
	clear(ta.segs[len(out):])
	ta.segs = out
}

// initRange reports whether every bit (all) or any bit (!all) of [lo, hi)
// of the init set is set.
func (ta *tagAlloc) initRange(lo, hi uint32, all bool) bool {
	if all {
		for o := lo; o < hi; o++ {
			if !bitAt(ta.init, o) {
				return false
			}
		}
		return true
	}
	return anyBit(ta.init, lo, hi)
}

func (ta *tagAlloc) setInit(lo, hi uint32) {
	for o := lo; o < hi; o++ {
		setBit(ta.init, o)
	}
}

// localAt is the index of the frame local holding byte o, -1 for none.
func (ta *tagAlloc) localAt(o uint32) int {
	i := ta.find(o)
	if i < len(ta.segs) {
		return int(ta.segs[i].local)
	}
	return -1
}

// newOwn gives allocation a, of size n, the tagged state of memory the
// evaluation owns: one live segment, the owner alone. shared marks
// a frozen const copy.
func (tg *tagState) newOwn(a, n uint32, shared bool) *tagAlloc {
	ta := tg.rec(a)
	*ta = tagAlloc{made: true, size: n}
	if n > 0 {
		ta.segs = []tseg{{lo: 0, hi: n, stack: ownerOnly(shared), local: -1}}
	}
	return ta
}
