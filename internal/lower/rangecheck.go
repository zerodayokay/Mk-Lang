// Why: every checked add, cast and bounds check costs a compare and an early
// exit that blocks vectorisation; most of them provably never fail. The range
// analysis finds those and turns them into forms that cannot trap.
//
// What: rangeCheck and rangeRun (the pass Lower runs), rangePass with its
// setup, solve, join, run and refine (the dataflow), ProvedBounds,
// FoldProvedBounds and fieldsEdges, and the test-only mutants (rangeMut).
//
// How: a forward interval analysis over a lowered body (Cousot and Cousot,
// POPL 1977), then the rewrite of every check it proves; unproved checks stay
// exactly as lower emitted them. The transfer rules, numbered as the code and
// tests cite them: 1 constants, 2 bitwise operators and shifts, 3 casts,
// 4 checked operations that did not trap, 5 branch refinement, 6 lengths,
// 7 slice and array contents (rangexfer.go), 8 accumulators (rangeloop.go),
// 9 lengths relationally and 10 difference guards (rangezone.go), 11 call
// results (rangexfer.go callResult) and 12 short-circuit joins
// (rangesplit.go).
//
// Inside Lower the pass changes only an operator or a cast kind (Add, Sub
// and Mul to AddNoWrap, SubNoWrap and MulNoWrap, Checked to Trunc), never
// the CFG: borrow reads a bounds check by its shape (borrow/transfer.go
// boundsLen), so a folded check would change its diagnostics on rejected
// programs. Folding a proved bounds check is FoldProvedBounds, for a body
// borrow accepted.
//
// A generic body runs only as the instances mono makes of it, so each fact
// must hold on every instance: a type-parameter type is no integer here
// (intOf), an array length that is a const parameter is unknown (lenRange),
// RvConstArg and RvTypeIs are their types' ranges, a type test's two edges
// are both kept, and a generic fields loop, which mono unrolls once per
// field, is analysed with the edges fieldsEdges adds.
//
// Every supported target has 64-bit int and uint (types.Targets: all have
// PointerBytes 8), which intOf assumes.

package lower

import (
	"mk/internal/mir"
	"mk/internal/types"
)

// rangeMut is the mutation test's wrong fact (TestPC_MutationCaught): zero
// in every build; a test sets one to show some edge program catches it.
type rangeMut uint8

const (
	mutNone          rangeMut = iota
	mutLtOffByOne             // a < b refines a <= b-2 instead of a <= b-1
	mutMaskMinusOne           // x & m is [0, m-1]
	mutAccumIter              // rule 8 counts one iteration fewer
	mutContentStore           // the contents leave out the stores
	mutTruncNoFit             // a narrowing Trunc refines its source without the fit test
	mutShrLow                 // x >> k drops one from its upper bound
	mutLenShort               // a length is one less than it is
	mutSubLow                 // a checked a - b's least value is one more than it is
	mutMulHigh                // a checked a * b's greatest value is one less than it is
	mutZoneLeAsLt             // rule 10: a <= b gives a - b <= -1 in the zone
	mutFlagFree               // rule 10: a signed index cast to uint is its value even when negative
	mutLenEqShort             // rule 9: n := s.Len() is s's length minus one
	mutSliceLenLong           // rule 9: xs[a..b] has b - a + 1 elements
	mutSymNoKill              // rule 9: assigning a slice local keeps its length's facts
	mutVarNoKill              // rule 10: assigning an unknown value keeps a variable's facts
	mutCounterStep0           // rule 8: a counter step of 0 counts as an iteration
	mutCounterAnyDef          // rule 8: a counter may have a definition that is no step
	mutCounterWrap            // rule 8: a wrapping +% step counts as a step
	mutZoneOff                // no wrong fact: the zone off, the tests' baseline for what it proves
	mutWidenEvery             // no wrong fact: every edge into a widening point widens, not only retreating ones (the tests' baseline)
	mutMulTrapHalf            // rule 4: a checked * that did not trap is at most MAX / 2
	mutCountSetShort          // rule 11: Mask[T, N].CountSet() is at most N - 1
	mutCountSetGen            // rule 11: CountSet with N no value (a generic body) is at most 32
	mutBitsShort              // rule 11: raw.Popcount64, Ctz64 and Clz64 are at most 63
	mutStepHigh               // rule 5: t = a + c refines a to one below t's upper bound less c
	mutStepLow                // rule 5: t = a + c refines a to one above t's lower bound less c
	mutStepSelf               // rule 5: i = i + c refines i through its own old value
	mutStepWrap               // rule 5: a wrapping t = a +% c (a -% c) refines a
	mutStepStale              // rule 5: t = a + c refines a after a was assigned again
	mutStepSubSign            // rule 5: t = a - c refines a as if t = a + c
	mutSplitStmt              // rule 12: a join whose other statements assign is split as if they did not
	mutSplitFirst             // rule 12: a predecessor's kept state is its first, never updated
	mutSplitNot               // rule 12: a negation in the chain is read as a copy
	mutSplitFlip              // rule 12: each edge takes the predecessors' states for the other value
	mutStepCMinusA            // rule 5: t = c - a refines a as if t = a - c
)

// Limits of the pass: a body past them keeps every check.
const (
	rangeMaxCells  = 1 << 18 // blocks × tracked locals
	rangeMaxVisits = 64      // visits of one block before giving up
	rangeRounds    = 3       // dataflow runs for the slice contents
)

// rangePass is the analysis of one body.
type rangePass struct {
	cfg *Config
	b   *mir.Body
	tab *types.Table
	mut rangeMut

	n     int           // tracked locals
	idx   []int32       // local -> tracked index, or -1
	info  []intInfo     // per tracked local
	cidx  []int32       // local -> content index, or -1
	cinit []ival        // per content local: initial contents
	cval  []ival        // per content local: current contents
	cinfo []intInfo     // per content local: the element type
	locs  []mir.LocalID // per content local

	zs     *zoneSet // rules 9 and 10 (rangezone.go), nil when the body has none
	noZone bool     // the analysis without the zone (after it failed with it)

	// Rule 12 (rangesplit.go): block -> its short-circuit join, or -1 (nil
	// when the body has none); and the joins' scratch states.
	splitAt     []int32
	splits      []splitJoin
	sbuf, ebuf  []ival
	zbuf, ezbuf []int64

	vedges []vEdge // the edges mono adds to generic fields loops
	rpo    []mir.BlockID
	pos    []int32 // block -> RPO position, -1 when unreachable from entry
	soff   []int32 // block -> start of its successors in succ (and end at blk+1)
	succ   []mir.BlockID
	poff   []int32 // block -> start of its reached predecessors in pred
	pred   []mir.BlockID
	widen  []bool
	visits []uint8

	in      []ival // len(blocks) × n
	reached []bool
	nreach  []bool // narrowing's next reached set
	dirty   []bool // blocks whose in-state changed since they last ran
	next    []ival // narrowing's next states, allocated on first use
	cur     []ival // one state, the run's
	fbuf    []ival // one state, a switch's false edge

	// One block's scratch: the defining statement of each tracked local in
	// the block being run, and the versions its operands had then.
	def     []int32
	defA    []int32
	defB    []int32
	verA    []uint32
	verB    []uint32
	ver     []uint32
	blk     mir.BlockID // the block run is in
	failed  bool
	widened bool // a join widened some bound in this solve
}

// rangeCheck runs the analysis on b and rewrites what it proves; with fold
// it also turns each proved bounds check into a goto, and returns how many.
func rangeCheck(cfg *Config, b *mir.Body, fold bool) int {
	folds := rangeRun(cfg, b, fold)
	if !fold {
		return 0
	}
	return ProvedBounds{at: folds}.Fold(b)
}

// rangeRun runs the analysis on b and rewrites the arithmetic and casts it
// proves; it returns the bounds checks it proves, folded by the caller.
// With bounds false a body whose only candidates are bounds checks is not
// analysed.
func rangeRun(cfg *Config, b *mir.Body, bounds bool) [][2]mir.BlockID {
	if !hasRangeCandidate(cfg.Types, b, bounds) {
		return nil
	}
	folds, ok, zone := rangeRunOnce(cfg, b, false)
	if !ok && zone {
		// The zone's extra visits can reach the visit cap where the
		// intervals alone do not: the body then gets the intervals' proofs.
		folds, _, _ = rangeRunOnce(cfg, b, true)
	}
	return folds
}

// rangeRunOnce is rangeRun's work, without the zone when noZone; ok false
// when the analysis gave up (nothing rewritten), and zone true when it ran
// with a zone.
func rangeRunOnce(cfg *Config, b *mir.Body, noZone bool) ([][2]mir.BlockID, bool, bool) {
	p := &rangePass{cfg: cfg, b: b, tab: cfg.Types, mut: cfg.rangeMut, noZone: noZone || cfg.rangeMut == mutZoneOff}
	if !p.setup() {
		return nil, true, false
	}
	zone := p.zs != nil
	folds, ok := p.analyse()
	return folds, ok, zone
}

// analyse is the dataflow and the rewrite of a set-up pass.
func (p *rangePass) analyse() ([][2]mir.BlockID, bool) {
	// The contents (rule 7) are fixed during one dataflow run; a run whose
	// stores escape them grows them (widened to the element type after the
	// first round) and runs again, so the states the rewrite reads were
	// computed under contents that hold every store.
	for round := 0; ; round++ {
		if !p.solve() {
			return nil, false
		}
		if !p.updateContents(round) {
			break
		}
		if round > rangeRounds {
			return nil, false
		}
	}
	if p.failed {
		return nil, false
	}
	return p.rewrite(), true
}

// ProvedBounds is the bounds checks Lower's range analysis proved
// (LowerProved): each a block ending in `switch k [0: panic bounds, _: ok]`
// whose k is exactly {1}, and its ok target. It is what FoldProvedBounds
// would fold, kept so the driver folds them once borrow has accepted the
// body without running the analysis a second time.
type ProvedBounds struct {
	at [][2]mir.BlockID
}

// Len is the number of proved checks.
func (p ProvedBounds) Len() int { return len(p.at) }

// Fold turns each proved check of b into `goto ok` and returns how many.
// b must be the body the proof was made on, unchanged since, and borrow
// must have accepted it (FoldProvedBounds' rule). The compare statements
// stay, so eval's step count is unchanged, and the block count is
// unchanged.
func (p ProvedBounds) Fold(b *mir.Body) int {
	for _, f := range p.at {
		block := &b.Blocks[f[0]]
		block.Term = mir.Terminator{Kind: mir.TermGoto, Target: f[1], Span: block.Term.Span}
	}
	return len(p.at)
}

// FoldProvedBounds runs the range analysis on b, rewrites the checks it
// proves as Lower does, and turns every bounds check `switch k [0: panic
// bounds, _: ok]` whose k it proves true into `goto ok`; it returns the
// number of folded checks. The compare statements stay, so eval's step
// count is unchanged, and the panic block becomes unreachable.
// Call it only on a body borrow accepted:
// borrow exempts a bounds check's length read by the check's shape, so a
// fold before borrow would add E0504 on a line that already has an error.
// With cfg.NoRanges it does nothing: the pass is off everywhere.
// The driver uses LowerProved and ProvedBounds.Fold instead, which fold the
// same checks from Lower's own run (TestPM_ProvedBoundsMatchFold).
func FoldProvedBounds(cfg *Config, b *mir.Body) int {
	if cfg.NoRanges {
		return 0
	}
	return rangeCheck(cfg, b, true)
}

// hasRangeCandidate reports whether b has a check the pass could remove: a
// checked integer + - *, a checked integer cast, or with fold a bounds check.
func hasRangeCandidate(tab *types.Table, b *mir.Body, fold bool) bool {
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			if s.Kind != mir.StAssign {
				continue
			}
			switch r := &s.Rvalue; r.Kind {
			case mir.RvBinary:
				if (r.Bin == mir.Add || r.Bin == mir.Sub || r.Bin == mir.Mul) && intOf(tab, r.Type).ok {
					return true
				}
			case mir.RvCast:
				if r.Cast == mir.CastChecked && intOf(tab, r.Type).ok {
					return true
				}
			}
		}
		if fold && boundsTarget(b, blk) != mir.NoBlock {
			return true
		}
	}
	return false
}

// boundsTarget is the ok block of a bounds check `switch k [0: panic
// bounds, _: ok]` ending blk, or NoBlock.
func boundsTarget(b *mir.Body, blk *mir.Block) mir.BlockID {
	t := &blk.Term
	if t.Kind != mir.TermSwitch || t.Cases.Len != 1 || t.Default == mir.NoBlock || t.On.Kind != mir.OpCopy || t.On.Place.Proj.Len != 0 {
		return mir.NoBlock
	}
	c := b.Cases[t.Cases.Start]
	if c.Value != 0 {
		return mir.NoBlock
	}
	pb := &b.Blocks[c.Target]
	if len(pb.Stmts) != 0 || pb.Term.Kind != mir.TermPanic || pb.Term.Panic != types.PanicBounds {
		return mir.NoBlock
	}
	return t.Default
}

// setup picks the tracked and content locals and orders the CFG. Its
// arrays are few and flat: the pass runs on every body (BenchmarkLower*).
func (p *rangePass) setup() bool {
	b := p.b
	nl, nb := len(b.Locals), len(b.Blocks)
	addressed := p.addressedLocals()
	p.idx = make([]int32, nl)
	for l := range b.Locals {
		p.idx[l] = -1
		if !addressed[l] && intOf(p.tab, b.Locals[l].Type).ok {
			p.idx[l] = int32(p.n)
			p.n++
		}
	}
	if p.n == 0 || nb*p.n > rangeMaxCells {
		return false
	}
	if !p.fieldsEdges() {
		return false
	}
	p.info = make([]intInfo, 0, p.n)
	for l := range b.Locals {
		if p.idx[l] >= 0 {
			p.info = append(p.info, intOf(p.tab, b.Locals[l].Type))
		}
	}
	p.order()
	p.contents(addressed)
	p.in = make([]ival, nb*p.n+2*p.n)
	p.cur, p.fbuf = p.in[nb*p.n:nb*p.n+p.n], p.in[nb*p.n+p.n:]
	p.in = p.in[: nb*p.n : nb*p.n]
	flags := make([]bool, 3*nb)
	p.reached, p.nreach, p.dirty = flags[:nb], flags[nb:2*nb], flags[2*nb:]
	p.visits = make([]uint8, nb)
	s := make([]int32, 3*p.n)
	p.def, p.defA, p.defB = s[:p.n], s[p.n:2*p.n], s[2*p.n:]
	v := make([]uint32, 3*p.n)
	p.verA, p.verB, p.ver = v[:p.n], v[p.n:2*p.n], v[2*p.n:]
	if !p.noZone {
		p.zoneSetup()
	}
	p.splitSetup()
	return true
}

// addressedLocals marks every local a borrow, reslice or reborrow names:
// such a local may change through a reference, so it is not tracked.
func (p *rangePass) addressedLocals() []bool {
	b := p.b
	out := make([]bool, len(b.Locals))
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			r := &s.Rvalue
			if s.Kind == mir.StAssign && (r.Kind == mir.RvRef || r.Kind == mir.RvSlice) {
				out[r.Place.Local] = true
			}
			// An asm output writes its place: never tracked. A "_"
			// output has Out.Local NoLocal and writes nothing.
			if s.Kind == mir.StAsm {
				for _, op := range b.Asms[s.Asm].Operands {
					switch op.Kind {
					case mir.AsmOut, mir.AsmLateOut, mir.AsmInOut, mir.AsmInLateOut:
						if l := op.Out.Local; l != mir.NoLocal && int(l) < len(out) {
							out[l] = true
						}
					}
				}
			}
		}
		if t := &blk.Term; isCall(t) {
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				if a.Mode == mir.ArgReborrow && a.Operand.Kind <= mir.OpMove {
					out[a.Operand.Place.Local] = true
				}
			}
		}
	}
	return out
}

// vEdge is an edge of the analysed CFG that the body does not have yet.
type vEdge struct{ from, to mir.BlockID }

// fieldsEdges models what mono does to a generic fields loop: the
// body is lowered once, its blocks exactly [Header, Exit), the header an
// empty goto body, the latch (Header+2) goto Exit, and mono copies the
// range once per field of the instance's type, each copy's latch going to
// the next copy's header, and with no field sends the edges into the
// header to Exit (mono's unrollLoop).
// So the range runs any number of times, zero included, though the body
// says once. Two edges the analysis adds, never the body, make every
// instance's executions paths of the analysed CFG: latch -> header (one
// more field; the header becomes a widening point) and header -> Exit (no
// field, or the last: the header has no statement, so its out-state is the
// state on entry to the range). Rule 8 may take a fields loop as its loop L
// (its counter rule reads the analysed CFG, edges added included), and
// that is sound because the two added edges make every instance's
// execution a path of the analysed CFG: zero fields is the header -> Exit
// path, which no counter step lies on (pcGeneric's fields/rule8-acc and
// fields/rule8-step). A fields loop of another shape is not modelled:
// false, and the body keeps every check.
func (p *rangePass) fieldsEdges() bool {
	b := p.b
	for _, lp := range b.Loops {
		if lp.FieldsOf == types.NoType {
			continue
		}
		h, e := lp.Header, lp.Exit
		latch := h + 2
		if e == mir.NoBlock || int(e) >= len(b.Blocks) || latch >= e {
			return false
		}
		hb, lb := &b.Blocks[h], &b.Blocks[latch]
		if len(hb.Stmts) != 0 || hb.Term.Kind != mir.TermGoto || hb.Term.Target != h+1 ||
			lb.Term.Kind != mir.TermGoto || lb.Term.Target != e {
			return false
		}
		p.vedges = append(p.vedges, vEdge{latch, h}, vEdge{h, e})
	}
	return true
}

// order computes the successors (flat: block i's are succ[soff[i]:
// soff[i+1]]), the body's own and fieldsEdges', the reverse postorder from
// the entry, the predecessors among reached blocks (flat the same way), and
// the widening points (targets of a retreating edge).
func (p *rangePass) order() {
	b := p.b
	nb := len(b.Blocks)
	ints := make([]int32, 3*(nb+1))
	p.soff, p.poff, p.pos = ints[:nb+1], ints[nb+1:2*(nb+1)], ints[2*(nb+1):3*(nb+1)-1]
	p.succ = make([]mir.BlockID, 0, 2*nb+len(p.vedges))
	for i := 0; i < nb; i++ {
		p.soff[i] = int32(len(p.succ))
		p.succ = b.Successors(mir.BlockID(i), p.succ)
		for _, e := range p.vedges {
			if e.from == mir.BlockID(i) {
				p.succ = append(p.succ, e.to)
			}
		}
	}
	p.soff[nb] = int32(len(p.succ))
	flags := make([]bool, 2*nb)
	seen := flags[:nb]
	p.widen = flags[nb:]
	blocks := make([]mir.BlockID, 2*nb)
	post := blocks[:0:nb]
	type frame struct {
		b mir.BlockID
		i int32
	}
	stack := make([]frame, 1, 16)
	stack[0] = frame{mir.EntryBlock, p.soff[mir.EntryBlock]}
	seen[mir.EntryBlock] = true
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.i < p.soff[f.b+1] {
			s := p.succ[f.i]
			f.i++
			if !seen[s] {
				seen[s] = true
				stack = append(stack, frame{s, p.soff[s]})
			}
			continue
		}
		post = append(post, f.b)
		stack = stack[:len(stack)-1]
	}
	for i := range p.pos {
		p.pos[i] = -1
	}
	p.rpo = blocks[nb : nb+len(post)]
	for i, id := range post {
		p.rpo[len(post)-1-i] = id
	}
	for i, id := range p.rpo {
		p.pos[id] = int32(i)
	}
	for _, id := range p.rpo {
		for _, s := range p.succsOf(id) {
			p.poff[s+1]++
			if p.pos[s] <= p.pos[id] {
				p.widen[s] = true
			}
		}
	}
	for i := 1; i <= nb; i++ {
		p.poff[i] += p.poff[i-1]
	}
	p.pred = make([]mir.BlockID, p.poff[nb])
	fill := post[:0] // post is spent: reuse it as one cursor per block
	for i := 0; i < nb; i++ {
		fill = append(fill, mir.BlockID(p.poff[i]))
	}
	for _, id := range p.rpo {
		for _, s := range p.succsOf(id) {
			p.pred[fill[s]] = id
			fill[s]++
		}
	}
}

func (p *rangePass) succsOf(blk mir.BlockID) []mir.BlockID {
	return p.succ[p.soff[blk]:p.soff[blk+1]]
}

func (p *rangePass) predsOf(blk mir.BlockID) []mir.BlockID {
	return p.pred[p.poff[blk]:p.poff[blk+1]]
}

func (p *rangePass) state(blk mir.BlockID) []ival {
	return p.in[int(blk)*p.n : int(blk+1)*p.n]
}

// solve runs the dataflow to a post-fixpoint with widening, then narrows
// twice; false when a block was visited too often. Each sweep in reverse
// postorder runs only the blocks whose in-state changed since they last
// ran (run is a function of the in-state and the contents, which are fixed
// during a solve, so a block not dirty would only rejoin outs its
// successors already hold); the sweeps stop when one changes nothing, so
// every reached block's outs under its final in-state are joined into its
// successors: a post-fixpoint. A visit is a run, which is what widening
// and the visit cap count.
func (p *rangePass) solve() bool {
	for i := range p.reached {
		p.reached[i] = false
		p.dirty[i] = false
		p.visits[i] = 0
	}
	p.widened = false
	for i := range p.splits {
		clear(p.splits[i].have) // a solve keeps its own predecessor states
	}
	entry := p.state(mir.EntryBlock)
	for i := range entry {
		entry[i] = p.info[i].store(p.info[i].full())
	}
	p.reached[mir.EntryBlock] = true
	p.dirty[mir.EntryBlock] = true
	zs := p.zs
	if zs != nil {
		clear(zs.cnt)
		p.zEntry(zs.state(mir.EntryBlock))
	}
	cur := p.cur
	for changed := true; changed; {
		changed = false
		for _, blk := range p.rpo {
			if !p.dirty[blk] {
				continue
			}
			p.dirty[blk] = false
			p.visits[blk]++
			if p.visits[blk] > rangeMaxVisits {
				p.failed = true
				return false
			}
			copy(cur, p.state(blk))
			p.run(blk, cur, nil, func(s mir.BlockID, out []ival, zout []int64) {
				if p.join(s, out, zout, true) {
					changed = true
					p.dirty[s] = true
				}
			})
		}
	}
	// With no widening the sweeps computed the least fixpoint of the
	// joins, which narrowing would give back unchanged: skip it (most
	// bodies have no loop).
	if !p.widened {
		return true
	}
	// Narrowing (Jacobi): every in-state recomputed from its predecessors'
	// outs, which from a post-fixpoint stays a post-fixpoint.
	if p.next == nil {
		p.next = make([]ival, len(p.in))
	}
	next, nreach := p.next, p.nreach
	var znext []int64
	mm := 0
	if zs != nil {
		if zs.next == nil {
			zs.next = make([]int64, len(zs.in))
		}
		znext, mm = zs.next, zs.m*zs.m
	}
	for k := 0; k < 2; k++ {
		for i := range nreach {
			nreach[i] = false
		}
		copy(next[:p.n], p.state(mir.EntryBlock))
		if zs != nil {
			copy(znext[:mm], zs.state(mir.EntryBlock))
		}
		nreach[mir.EntryBlock] = true
		for _, blk := range p.rpo {
			if !p.reached[blk] {
				continue
			}
			copy(cur, p.state(blk))
			p.run(blk, cur, nil, func(s mir.BlockID, out []ival, zout []int64) {
				if p.splitAt != nil && p.splitAt[s] >= 0 {
					p.splitKeep(s, out, zout)
				}
				dst := next[int(s)*p.n : int(s+1)*p.n]
				var zdst []int64
				if zs != nil {
					zdst = znext[int(s)*mm : int(s+1)*mm]
				}
				if !nreach[s] {
					copy(dst, out)
					copy(zdst, zout)
					nreach[s] = true
					return
				}
				for i := range dst {
					dst[i] = joinI(dst[i], out[i])
				}
				if zs != nil {
					zs.join(zdst, zout, false)
				}
			})
		}
		p.in, next = next, p.in
		p.reached, nreach = nreach, p.reached
		if zs != nil {
			zs.in, znext = znext, zs.in
		}
	}
	p.next, p.nreach = next, nreach
	if zs != nil {
		zs.next = znext
	}
	return true
}

func joinI(a, b ival) ival {
	if a.empty() {
		return b
	}
	if b.empty() {
		return a
	}
	if b.lo < a.lo {
		a.lo = b.lo
	}
	if b.hi > a.hi {
		a.hi = b.hi
	}
	return a
}

// join merges out into s's in-state, widening at a widening point after its
// second visit; it reports a change. An interval widens only on a
// retreating edge, the out of the block run (p.blk) at or after s in
// reverse postorder: a bound that grows on a forward
// edge into a loop header is joined exactly, so an outer counter's copy
// that an inner loop never assigns keeps the bound the outer header
// proved, where widening it on the edge from the outer body lost it to the
// type's limit for good (narrowing cannot recover it: the inner back edge
// carries the widened value). Termination: every cycle of the CFG has a
// retreating edge, so a bound that grew without end would grow without end
// on some retreating edge, where it widens; the visit cap still guards
// every body (past it the body keeps every check). The zone widens as
// before, on every edge into a widening point.
func (p *rangePass) join(s mir.BlockID, out []ival, zout []int64, widen bool) bool {
	dst := p.state(s)
	zs := p.zs
	// Rule 12: a split join keeps each predecessor's state, and one that
	// changed runs the join again.
	kept := p.splitAt != nil && p.splitAt[s] >= 0 && p.splitKeep(s, out, zout)
	if !p.reached[s] {
		copy(dst, out)
		if zs != nil {
			copy(zs.state(s), zout)
		}
		p.reached[s] = true
		return true
	}
	changed := kept
	wz := widen && p.widen[s] && p.visits[s] >= 2
	w := wz && (p.pos[s] <= p.pos[p.blk] || p.mut == mutWidenEvery)
	for i := range dst {
		v := joinI(dst[i], out[i])
		if v == dst[i] {
			continue
		}
		if w {
			full := p.info[i].store(p.info[i].full())
			if v.lo < dst[i].lo {
				v.lo = full.lo
			}
			if v.hi > dst[i].hi {
				v.hi = full.hi
			}
			p.widened = true
		}
		dst[i] = v
		changed = true
	}
	if zs != nil && zs.join(zs.state(s), zout, wz) {
		changed = true
		if wz {
			// Each widened change drops bounds, so this terminates; the
			// cap keeps the zone from adding many visits: past it the
			// point's zone is top, which no join changes.
			p.widened = true
			if zs.cnt[s]++; zs.cnt[s] > zoneMaxWiden {
				zs.top(zs.state(s))
			}
		}
	}
	return changed
}

// hook, when not nil, sees every statement of a run before it is applied,
// with the state at that point (the rewrite and rule 8 use it).
type rangeHook func(j int, st []ival)

// run applies blk's statements to st, and to the zone from blk's zone
// in-state when the body has one, and hands each successor its states (the
// zone nil when there is none).
func (p *rangePass) run(blk mir.BlockID, st []ival, hook rangeHook, emit func(mir.BlockID, []ival, []int64)) {
	b := p.b
	block := &b.Blocks[blk]
	p.blk = blk
	for i := range p.def {
		p.def[i] = -1
	}
	zs := p.zs
	var z []int64
	if zs != nil {
		z = zs.cur
		copy(z, zs.state(blk))
	}
	for j := range block.Stmts {
		s := &block.Stmts[j]
		if hook != nil {
			hook(j, st)
		}
		if s.Kind != mir.StAssign {
			continue
		}
		if s.Place.Proj.Len != 0 {
			continue // a store into a place: contents are updateContents's
		}
		k := p.idx[s.Place.Local]
		// The zone's term for the value, read before the statement runs.
		var zt zterm
		zok, zdo := false, false
		if zs != nil && (k >= 0 && (zs.zv[k] >= 0 || s.Rvalue.Kind == mir.RvLen) || k < 0 && zs.sym[s.Place.Local] >= 0) {
			zdo = true
			zt, zok = p.zTermBefore(st, s, k)
		}
		if k < 0 {
			if zdo {
				p.zAssign(z, s, k, zt, zok, ival{})
			}
			continue
		}
		v := p.rvalue(st, &s.Rvalue, p.info[k])
		if zdo {
			p.zAssign(z, s, k, zt, zok, v)
		}
		p.ver[k]++
		st[k] = v
		p.def[k] = int32(j)
		p.defA[k], p.defB[k] = p.depOf(s.Rvalue.A), p.depOf(s.Rvalue.B)
		if s.Rvalue.Kind == mir.RvUse || s.Rvalue.Kind == mir.RvCast {
			p.defB[k] = -1
		}
		if a := p.defA[k]; a >= 0 {
			p.verA[k] = p.ver[a]
		}
		if c := p.defB[k]; c >= 0 {
			p.verB[k] = p.ver[c]
		}
	}
	if hook != nil {
		hook(len(block.Stmts), st) // the state at the terminator
	}
	t := &block.Term
	switch t.Kind {
	case mir.TermSwitch:
		if p.splitAt != nil && p.splitAt[blk] >= 0 && p.splitEmit(blk, emit) {
			return // rule 12: each edge from the predecessors that can take it
		}
		if t.On.Kind == mir.OpCopy && t.On.Place.Proj.Len == 0 && t.Cases.Len == 1 && t.Default != mir.NoBlock && p.idx[t.On.Place.Local] >= 0 {
			k := p.idx[t.On.Place.Local]
			c := b.Cases[t.Cases.Start]
			if p.info[k].bits == 1 && c.Value == 0 {
				// Rules 9 and 10: the compare as terms, read before rule 5
				// narrows the state.
				var op mir.BinOp
				var ta, tb zterm
				var small, zok bool
				var zf []int64
				if zs != nil {
					op, ta, tb, small, zok = p.zCompare(st, k)
					zf = zs.f
					copy(zf, z)
				}
				f := p.fbuf
				copy(f, st)
				if p.refine(f, k, ival{0, 0}, 0) && (!zok || p.zRefine(zf, op, ta, tb, small, false)) {
					emit(c.Target, f, zf)
				}
				if p.refine(st, k, ival{1, 1}, 0) && (!zok || p.zRefine(z, op, ta, tb, small, true)) {
					emit(t.Default, st, z)
				}
				return
			}
		}
	case mir.TermTailCall:
		return // an exit with no successor
	case mir.TermCall:
		var ze []int64 // the zone on the normal edge, when it knows more there
		if t.Dest.Proj.Len == 0 {
			if k := p.idx[t.Dest.Local]; k >= 0 {
				r := p.callResult(t, p.info[k]) // rule 11
				st[k] = p.info[k].store(r)
				if zs != nil && zs.zv[k] >= 0 {
					zs.forget(z, zs.zv[k])
					zs.bound(z, zs.zv[k], r)
				}
			} else if zs != nil && zs.sym[t.Dest.Local] >= 0 {
				ze = p.zCallLen(st, z, t)
			}
		}
		if t.HasErr && t.Err.Error != mir.NoLocal {
			if k := p.idx[t.Err.Error]; k >= 0 {
				st[k] = p.info[k].store(p.info[k].full())
				if zs != nil && zs.zv[k] >= 0 {
					zs.forget(z, zs.zv[k])
					if ze != nil {
						zs.forget(ze, zs.zv[k])
					}
				}
			}
		}
		if ze != nil && !(t.HasErr && t.Err.Block == t.Target) {
			for _, s := range p.succsOf(blk) {
				if s == t.Target {
					emit(s, st, ze)
				} else {
					emit(s, st, z)
				}
			}
			return
		}
	}
	for _, s := range p.succsOf(blk) {
		emit(s, st, z)
	}
}

// depOf is the tracked local a whole-local copy or move reads, or -1.
func (p *rangePass) depOf(op mir.Operand) int32 {
	if op.Kind > mir.OpMove || op.Place.Proj.Len != 0 {
		return -1
	}
	return p.idx[op.Place.Local]
}

// refine narrows tracked local k to r in st and follows k's defining
// statement back to the locals it was computed from (rule 5): false when
// the state became infeasible.
func (p *rangePass) refine(st []ival, k int32, r ival, depth int) bool {
	in := p.info[k]
	cur := in.load(st[k])
	want := in.load(r)
	nw := wi{wMax(cur.lo, want.lo), wMin(cur.hi, want.hi)}
	if nw.lo.cmp(nw.hi) > 0 {
		return false
	}
	st[k] = in.store(nw)
	if depth > 8 || p.def[k] < 0 {
		return true
	}
	r2 := &p.b.Blocks[p.blk].Stmts[p.def[k]].Rvalue
	// The operands still hold the values the definition read.
	if p.mut != mutStepStale || r2.Kind != mir.RvBinary || r2.Bin.IsCompare() {
		if a := p.defA[k]; a >= 0 && p.ver[a] != p.verA[k] {
			return true
		}
		if c := p.defB[k]; c >= 0 && p.ver[c] != p.verB[k] {
			return true
		}
	}
	switch r2.Kind {
	case mir.RvUse:
		if a := p.defA[k]; a >= 0 {
			return p.refineTo(st, a, nw, depth)
		}
	case mir.RvCast:
		a := p.defA[k]
		if a < 0 {
			return true
		}
		src := p.info[a]
		switch r2.Cast {
		case mir.CastLossless, mir.CastChecked:
			// The value went through unchanged.
			return p.refineTo(st, a, nw, depth)
		case mir.CastTrunc:
			// Trunc keeps the value when every source value fits the target;
			// and at equal width it is a bijection that maps each value in
			// both ranges to itself and every other value outside both (a
			// negative int becomes a uint of at least 2^63), so uint(i) < n
			// with n <= 2^63-1 means 0 <= i < n. A narrower Trunc refines
			// nothing otherwise: Trunc[u8](x) < 2 holds for x = 256.
			fitsBoth := src.fits(nw) && in.fits(nw)
			if in.fits(src.load(st[a])) || src.bits == in.bits && fitsBoth || p.mut == mutTruncNoFit && fitsBoth {
				return p.refineTo(st, a, nw, depth)
			}
		}
	case mir.RvBinary:
		if !r2.Bin.IsCompare() {
			return p.refineStep(st, k, r2, nw, depth)
		}
		if in.bits != 1 || nw.lo.cmp(nw.hi) != 0 {
			return true
		}
		truth := nw.lo.cmp(wInt(1)) == 0
		ai, aok := p.operand(st, r2.A)
		bi, bok := p.operand(st, r2.B)
		if !aok || !bok {
			return true
		}
		na, nb := refineCmp(r2.Bin, truth, ai.r, bi.r)
		if p.mut == mutLtOffByOne && truth && r2.Bin == mir.Lt {
			na.hi = wMin(ai.r.hi, bi.r.hi.sub(wInt(2)))
		}
		if a := p.defA[k]; a >= 0 && !p.refineTo(st, a, na, depth) {
			return false
		}
		if c := p.defB[k]; c >= 0 && !p.refineTo(st, c, nb, depth) {
			return false
		}
		if na.lo.cmp(na.hi) > 0 || nb.lo.cmp(nb.hi) > 0 {
			return false
		}
	}
	return true
}

// refineStep is rule 5 through k's defining statement r, t = a + c, c + a
// or a - c: when a compare narrows t to nw, a lies in nw - c (nw + c for
// a - c). Its conditions, each checked here or by refine before it:
//   - r is t's last definition in the block being run (p.def, reset per
//     block), so it ran on this execution of the block before the compare,
//     not in another block or an earlier iteration;
//   - a still holds the value r read: refine checked a's version
//     (mutStepStale skips that check for steps). A definition reading t
//     itself, such as `i = i + 16`, is refused: run records an operand's
//     version after it bumps the destination's, so the check passes there
//     although the value r read is gone (mutStepSelf);
//   - c is an immediate, read in t's type (a negative c and the type's
//     least value included: the arithmetic is 128-bit);
//   - the operator is checked (Add, Sub: a definition that did not trap
//     computed the exact sum) or proved (AddNoWrap, SubNoWrap: their
//     producer proved the exact sum lies in the type), never wrapping
//     (AddWrap, SubWrap keep it only modulo 2^w; mutStepWrap).
//
// Then t = a + c exactly, as integers, so a = t - c. This is what makes
// `for i + 16 <= xs.Len()` bound i by xs.Len() - 16.
func (p *rangePass) refineStep(st []ival, k int32, r *mir.Rvalue, nw wi, depth int) bool {
	var a int32
	var imm mir.Operand
	sub := false
	switch r.Bin {
	case mir.Add, mir.AddNoWrap:
	case mir.Sub, mir.SubNoWrap:
		sub = true
	case mir.AddWrap, mir.SubWrap:
		if p.mut != mutStepWrap {
			return true
		}
		sub = r.Bin == mir.SubWrap
	default:
		return true
	}
	switch {
	case r.B.Kind == mir.OpImm:
		a, imm = p.defA[k], r.B
	case r.A.Kind == mir.OpImm && (!sub || p.mut == mutStepCMinusA):
		a, imm = p.defB[k], r.A
	default:
		// Two-local exact step: t = a + b or t = a - b (checked or proved).
		// refine already verified both operands still hold the versions read
		// by r; invert in exact 128-bit arithmetic so narrowing t (e.g. from
		// a slice bounds check on base + off) narrows both operands.
		if p.mut == mutStepWrap && (r.Bin == mir.AddWrap || r.Bin == mir.SubWrap) {
			return true
		}
		da, db := p.defA[k], p.defB[k]
		if da < 0 || db < 0 || da == k || db == k {
			return true
		}
		ai := p.info[da].load(st[da])
		bi := p.info[db].load(st[db])
		var na, nb wi
		if sub {
			na = wi{nw.lo.add(bi.lo), nw.hi.add(bi.hi)}
			nb = wi{ai.lo.sub(nw.hi), ai.hi.sub(nw.lo)}
		} else {
			na = wi{nw.lo.sub(bi.hi), nw.hi.sub(bi.lo)}
			nb = wi{nw.lo.sub(ai.hi), nw.hi.sub(ai.lo)}
		}
		if na.lo.cmp(ai.lo) > 0 || na.hi.cmp(ai.hi) < 0 {
			if !p.refineTo(st, da, na, depth) {
				return false
			}
		}
		if nb.lo.cmp(bi.lo) > 0 || nb.hi.cmp(bi.hi) < 0 {
			if !p.refineTo(st, db, nb, depth) {
				return false
			}
		}
		return true
	}
	if a < 0 || a == k && p.mut != mutStepSelf {
		return true
	}
	c := p.info[k].immValue(imm.Bits)
	if sub && p.mut != mutStepSubSign {
		c = c.neg()
	}
	// Obligation: t = a + c exactly (c negated for a - c), so a lies in
	// nw - c.
	na := wi{nw.lo.sub(c), nw.hi.sub(c)}
	switch p.mut {
	case mutStepHigh:
		na.hi = na.hi.sub(wInt(1))
	case mutStepLow:
		na.lo = na.lo.add(wInt(1))
	}
	return p.refineTo(st, a, na, depth)
}

// refineTo is refine with a wide interval.
func (p *rangePass) refineTo(st []ival, k int32, r wi, depth int) bool {
	if r.lo.cmp(r.hi) > 0 {
		return false
	}
	in := p.info[k]
	r = in.clamp(r)
	if r.lo.cmp(r.hi) > 0 {
		return false
	}
	return p.refine(st, k, in.store(r), depth+1)
}
