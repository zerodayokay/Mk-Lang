// Why: a bounds check in a hot loop blocks vectorisation even when one test
// before the loop could show every index in range.
//
// What: VersionLoops and VersionOptions — accumulator TRE (rectre.go), 2D nest
// versioning (rangenest.go), counted-loop versioning (versionBounds), monotone
// while-loop versioning (rangemono.go), and reduction versioning (rangereduce.go).
//
// How: runs after mono in release builds (internal/driver/loopversion.go),
// returning a new body so eval/borrow/cache stay untouched. Non-trapping entry
// guards prove all indices in [0, len); fast copies drop proved checks, while
// fallback runs the original loop with identical trap sites and order.

package lower

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// VersionOptions selects the versioning rewrites. The zero value runs both;
// the switches are for the on/off comparisons (soundness bar) and to run
// the bounds rewrite without the reduction rewrite.
type VersionOptions struct {
	// NoBounds turns the bounds versioning off.
	NoBounds bool
	// NoReduce turns the reduction versioning (rangereduce.go) off.
	NoReduce bool
	// mut is the mutation test's wrong guard; zero outside it.
	mut versionMut
	// chunk is the reduction's pass-1 chunk in iterations, for tests that cross
	// chunk boundaries in small loops; zero is redChunk.
	chunk uint64
}

// versionMut is a wrong guard or a wrong eligibility rule for
// TestPC_MutationCaught: each makes some edge program index out of bounds,
// or overflow without a trap, in the fast copy.
type versionMut uint8

const (
	vmutNone         versionMut = iota
	vmutHiLe                    // bounds: the last index may equal the length
	vmutNoLow                   // bounds: the first index may be negative
	vmutNoOvf                   // bounds: the guard's sums may wrap
	vmutLenAnyDef               // bounds: a slice assigned in the loop keeps its length
	vmutBaseAnyDef              // bounds: a base assigned in the loop is invariant
	vmutIVAfter                 // bounds: a stepped counter read after its step has its value before it
	vmutTripShort               // reduce: the term count is half the iterations
	vmutNoInit                  // reduce: the accumulator's initial value is no term
	vmutLimitHigh               // reduce: the precheck admits terms up to 4B
	vmutNoRestore               // reduce: the fallback reruns from pass 1's accumulator
	vmutNoChunk                 // reduce: pass 1 is one chunk, the whole loop
	vmutChunkPastHi             // reduce: a chunk's end is not capped at the bound
	vmutRestoreFirst            // reduce: the fallback restores s0, the loop's first s, with the chunk's counter
	vmutAnyLive                 // reduce: restartable admits a written local live at the header
	vmutReadsAny                // reduce: the accumulator may be read besides its add
)

// Limits: functions of the MIR alone. A loop past them, or a body
// past verMaxBlocks, is not versioned.
const (
	verMaxLoopOps = 256     // statements plus terminators of one loop (bounds)
	verMaxLoops   = 16      // loops versioned per body, per rewrite
	verMaxBlocks  = 1 << 14 // blocks of a body either rewrite reads
	verMaxDepth   = 6       // definitions followed back from one operand
)

// VersionLoops returns b with accumulator tail-recursion elimination
// (rectre.go), bounds versions (!opt.NoBounds), and reduction versions
// (!opt.NoReduce), leaving b unmodified. b must be an instance body.
func VersionLoops(cfg *Config, b *mir.Body, opt VersionOptions) *mir.Body {
	out, _, _ := versionLoops(cfg, b, opt)
	return out
}

// versionLoops is VersionLoops, and how many loops each rewrite versioned.
func versionLoops(cfg *Config, b *mir.Body, opt VersionOptions) (out *mir.Body, bounds, reduce int) {
	if cfg.NoRanges || b == nil || b.GenericCount != 0 || len(b.Blocks) > verMaxBlocks {
		return b, 0, 0
	}
	out = elimAccTailRec(cfg, b, opt)
	if len(out.Loops) == 0 && !hasLatch(out) || len(out.Blocks) > verMaxBlocks {
		return out, 0, 0
	}
	if !opt.NoBounds {
		var skip map[mir.BlockID]bool
		if hasLatch(out) {
			out, bounds, skip = versionNestedBounds(cfg, out, opt.mut)
		}
		if bounds < verMaxLoops && hasLatch(out) && len(out.Blocks) <= verMaxBlocks {
			var sb int
			out, sb = versionBounds(cfg, out, opt.mut, skip, verMaxLoops-bounds)
			bounds += sb
		}
		if bounds < verMaxLoops && len(out.Blocks) <= verMaxBlocks {
			var mb int
			out, mb = versionMonotone(cfg, out, opt.mut, verMaxLoops-bounds)
			bounds += mb
		}
	}
	if !opt.NoReduce && hasLatch(out) && len(out.Blocks) <= verMaxBlocks {
		chunk := opt.chunk
		if chunk == 0 {
			chunk = redChunk
		}
		out, reduce = versionReduce(cfg, out, opt.mut, chunk)
	}
	return out, bounds, reduce
}

// hasLatch is the cheap filter: some block is a counted loop's latch,
// `c = AddNoWrap(copy c, 1); goto h` (lower's `for c in a..b`).
func hasLatch(b *mir.Body) bool {
	for i := range b.Blocks {
		if _, ok := latchCounter(&b.Blocks[i]); ok {
			return true
		}
	}
	return false
}

// latchCounter is c when blk is exactly `c = AddNoWrap(copy c, 1); goto _`.
func latchCounter(blk *mir.Block) (mir.LocalID, bool) {
	if len(blk.Stmts) != 1 || blk.Term.Kind != mir.TermGoto {
		return 0, false
	}
	s := &blk.Stmts[0]
	r := &s.Rvalue
	if s.Kind != mir.StAssign || s.Place.Proj.Len != 0 || r.Kind != mir.RvBinary || r.Bin != mir.AddNoWrap ||
		!wholeOf(r.A, s.Place.Local) || r.B.Kind != mir.OpImm || r.B.Bits != 1 {
		return 0, false
	}
	return s.Place.Local, true
}

// cloneForVersion is a private copy of b: every slice the rewrites append
// to or write is its own. Statement slices stay shared until a block is
// changed (vctx.own).
func cloneForVersion(b *mir.Body) *mir.Body {
	c := *b
	c.Locals = slices.Clone(b.Locals)
	c.Blocks = slices.Clone(b.Blocks)
	c.Cases = slices.Clone(b.Cases)
	return &c
}

// vcfg is the CFG facts of one body: successors, predecessors among
// reachable blocks, reverse postorder and immediate dominators (Cooper,
// Harvey and Kennedy, "A simple, fast dominance algorithm", 2001).
type vcfg struct {
	b    *mir.Body
	succ [][]mir.BlockID
	pred [][]mir.BlockID
	rpo  []mir.BlockID
	pos  []int32 // RPO position, -1 when unreachable
	idom []int32
}

func newVcfg(b *mir.Body) *vcfg {
	nb := len(b.Blocks)
	g := &vcfg{b: b, succ: make([][]mir.BlockID, nb), pred: make([][]mir.BlockID, nb), pos: make([]int32, nb), idom: make([]int32, nb)}
	for i := range nb {
		g.succ[i] = b.Successors(mir.BlockID(i), nil)
		g.pos[i] = -1
		g.idom[i] = -1
	}
	// Iterative DFS postorder from the entry.
	seen := make([]bool, nb)
	type frame struct {
		b mir.BlockID
		i int
	}
	var post []mir.BlockID
	stack := []frame{{mir.EntryBlock, 0}}
	seen[mir.EntryBlock] = true
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.i < len(g.succ[f.b]) {
			s := g.succ[f.b][f.i]
			f.i++
			if !seen[s] {
				seen[s] = true
				stack = append(stack, frame{s, 0})
			}
			continue
		}
		post = append(post, f.b)
		stack = stack[:len(stack)-1]
	}
	for i := len(post) - 1; i >= 0; i-- {
		g.pos[post[i]] = int32(len(g.rpo))
		g.rpo = append(g.rpo, post[i])
	}
	for _, u := range g.rpo {
		for _, s := range g.succ[u] {
			g.pred[s] = append(g.pred[s], u)
		}
	}
	g.idom[mir.EntryBlock] = int32(mir.EntryBlock)
	inter := func(x, y int32) int32 {
		for x != y {
			for g.pos[x] > g.pos[y] {
				x = g.idom[x]
			}
			for g.pos[y] > g.pos[x] {
				y = g.idom[y]
			}
		}
		return x
	}
	for changed := true; changed; {
		changed = false
		for _, u := range g.rpo[1:] {
			nd := int32(-1)
			for _, p := range g.pred[u] {
				if g.idom[p] < 0 {
					continue
				}
				if nd < 0 {
					nd = int32(p)
				} else {
					nd = inter(nd, int32(p))
				}
			}
			if nd >= 0 && g.idom[u] != nd {
				g.idom[u] = nd
				changed = true
			}
		}
	}
	return g
}

// dominates reports whether a dominates c (both reachable; a block
// dominates itself).
func (g *vcfg) dominates(a, c mir.BlockID) bool {
	if g.pos[a] < 0 || g.pos[c] < 0 {
		return false
	}
	for x := int32(c); ; x = g.idom[x] {
		if x == int32(a) {
			return true
		}
		if x == int32(mir.EntryBlock) {
			return false
		}
	}
}

// vloop is one innermost counted loop: header h is exactly
// `t = Lt(copy c, hi); switch copy t [0: exit, _: body]`, its one latch is
// `c = AddNoWrap(copy c, 1); goto h`, and pre is its one predecessor
// outside, whose terminator is a goto or a switch.
type vloop struct {
	h, latch, pre, exit, body mir.BlockID
	in                        []bool // by block
	members                   []mir.BlockID
	c, t                      mir.LocalID
	hi                        mir.Operand
	ty                        types.TypeID // the counter's type, a signed 64-bit integer
	defs                      []int32      // by local: definitions inside the loop
	ops                       int
	small                     bool // smallTrip: a known count of at most verSmallTrip
}

// countedLoops finds the innermost counted loops of g's body, by header.
func countedLoops(g *vcfg, tab *types.Table) []*vloop {
	b := g.b
	nb := len(b.Blocks)
	latches := map[mir.BlockID][]mir.BlockID{}
	var heads []mir.BlockID
	for _, u := range g.rpo {
		for _, h := range g.succ[u] {
			if g.dominates(h, u) {
				if _, ok := latches[h]; !ok {
					heads = append(heads, h)
				}
				latches[h] = append(latches[h], u)
			}
		}
	}
	slices.Sort(heads)
	isHead := make([]bool, nb)
	for _, h := range heads {
		isHead[h] = true
	}
	var out []*vloop
	for _, h := range heads {
		in := make([]bool, nb)
		in[h] = true
		var members []mir.BlockID
		members = append(members, h)
		stack := slices.Clone(latches[h])
		inner := false
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if in[u] {
				continue
			}
			in[u] = true
			members = append(members, u)
			if isHead[u] {
				inner = true
			}
			stack = append(stack, g.pred[u]...)
		}
		if inner || len(latches[h]) != 1 {
			continue
		}
		slices.Sort(members)
		l := &vloop{h: h, latch: latches[h][0], in: in, members: members, pre: mir.NoBlock}
		if l.shape(g, tab) {
			out = append(out, l)
		}
	}
	return out
}

// shape checks the counted-loop form and fills the loop's fields.
func (l *vloop) shape(g *vcfg, tab *types.Table) bool {
	b := g.b
	c, ok := latchCounter(&b.Blocks[l.latch])
	if !ok || b.Blocks[l.latch].Term.Target != l.h {
		return false
	}
	hb := &b.Blocks[l.h]
	if len(hb.Stmts) != 1 || hb.Term.Kind != mir.TermSwitch {
		return false
	}
	s := &hb.Stmts[0]
	r := &s.Rvalue
	if s.Kind != mir.StAssign || s.Place.Proj.Len != 0 || r.Kind != mir.RvBinary || r.Bin != mir.Lt || !wholeOf(r.A, c) {
		return false
	}
	t := &hb.Term
	if t.On.Kind != mir.OpCopy || !wholeOf(t.On, s.Place.Local) || t.Cases.Len != 1 || t.Default == mir.NoBlock {
		return false
	}
	cs := b.Cases[t.Cases.Start]
	if cs.Value != 0 || l.in[cs.Target] || !l.in[t.Default] {
		return false
	}
	ty := b.Locals[c].Type
	if in := intOf(tab, ty); !in.ok || !in.signed || in.bits != 64 {
		return false
	}
	switch r.B.Kind {
	case mir.OpImm:
		if r.B.Type != ty {
			return false
		}
	case mir.OpCopy:
		if r.B.Place.Proj.Len != 0 || b.Locals[r.B.Place.Local].Type != ty {
			return false
		}
	default:
		return false
	}
	l.c, l.t, l.hi, l.ty, l.exit, l.body = c, s.Place.Local, r.B, ty, cs.Target, t.Default
	for _, p := range g.pred[l.h] {
		if l.in[p] {
			continue
		}
		if l.pre != mir.NoBlock {
			return false
		}
		l.pre = p
	}
	if l.pre == mir.NoBlock {
		return false
	}
	if k := b.Blocks[l.pre].Term.Kind; k != mir.TermGoto && k != mir.TermSwitch {
		return false
	}
	l.small = smallTrip(b, l)
	// Every block is ordinary code with a goto or switch, and no statement
	// is asm or a join: nothing in the loop calls, writes a local behind a
	// reference, or waits.
	l.defs = make([]int32, len(b.Locals))
	for _, m := range l.members {
		blk := &b.Blocks[m]
		if blk.Origin != mir.OriginBody || blk.Term.Kind != mir.TermGoto && blk.Term.Kind != mir.TermSwitch {
			return false
		}
		l.ops += len(blk.Stmts) + 1
		for j := range blk.Stmts {
			st := &blk.Stmts[j]
			switch st.Kind {
			case mir.StAsm, mir.StJoin:
				return false
			case mir.StAssign:
				if st.Rvalue.Kind == mir.RvRef && st.Rvalue.Origin == mir.OriginLoopHolder {
					return false // a holder loan is made once (a MIR verifier rule)
				}
				if st.Place.Proj.Len == 0 || !sliceLike(tab, b.Locals[st.Place.Local].Type) {
					// A write through a slice's index is an element's,
					// never the slice value's; any other write changes
					// the local.
					l.defs[st.Place.Local]++
				}
			}
		}
	}
	if l.defs[c] != 1 {
		return false // the latch's step is the counter's only definition
	}
	// The bound is the same on every iteration (no definition in the loop,
	// no reference: shape runs before vctx's addressed sets, so the caller
	// checks the reference half, hiInvariant).
	if l.hi.Kind == mir.OpCopy && l.defs[l.hi.Place.Local] != 0 {
		return false
	}
	return true
}

// hiInvariant reports whether l's bound holds one value through the loop.
func (x *vctx) hiInvariant(l *vloop) bool {
	return l.hi.Kind == mir.OpImm || x.invariant(l, l.hi.Place.Local)
}

// verSmallTrip is the most iterations of a loop whose bounds are both
// immediates that is still not versioned: LLVM unrolls such a loop whole,
// a copy buys nothing, and its size lands in every program that links
// the code (std.rt's fault and report paths: tests/programs/hello.mk's
// release size budget, TestHelloWorldWithinBudget).
const verSmallTrip = 16

// smallTrip reports whether l runs a known number of iterations, at most
// verSmallTrip: hi an immediate, and the counter's last definition in the
// predecessor an immediate (lower's `c = lo` before `goto header`).
func smallTrip(b *mir.Body, l *vloop) bool {
	if l.pre == mir.NoBlock {
		return false
	}
	st := b.Blocks[l.pre].Stmts
	// imm is o's value on entry to the header when the predecessor's last
	// definition of it is an immediate (lower's `n = hi; c = lo; goto`).
	imm := func(o mir.Operand) (int64, bool) {
		if o.Kind == mir.OpImm {
			return int64(o.Bits), true
		}
		if o.Kind != mir.OpCopy || o.Place.Proj.Len != 0 {
			return 0, false
		}
		for j := len(st) - 1; j >= 0; j-- {
			s := &st[j]
			if s.Kind != mir.StAssign || s.Place.Local != o.Place.Local {
				continue
			}
			if s.Place.Proj.Len != 0 || s.Rvalue.Kind != mir.RvUse || s.Rvalue.A.Kind != mir.OpImm {
				return 0, false
			}
			return int64(s.Rvalue.A.Bits), true
		}
		return 0, false
	}
	hi, ok := imm(l.hi)
	if !ok {
		return false
	}
	lo, ok := imm(mir.Copy(mir.LocalPlace(l.c)))
	return ok && hi-lo <= verSmallTrip
}

// sliceLike reports whether t is a slice or str type, whose value a write
// through an index never changes.
func sliceLike(tab *types.Table, t types.TypeID) bool {
	switch tab.Get(t).Kind {
	case types.KSlice, types.KMutSlice, types.KStr:
		return true
	}
	return false
}

// vctx is one rewrite of one body: its CFG facts, the locals that may
// change behind a reference (addr) and the slice locals whose value may
// (hdr), and the private copy being built.
type vctx struct {
	cfg  *Config
	tab  *types.Table
	b    *mir.Body // the body read
	g    *vcfg
	addr []bool
	hdr  []bool
	mut  versionMut
	// chunk is the reduction's pass-1 chunk (versionReduce).
	chunk uint64
	out   *mir.Body // the copy written, nil until the first rewrite
	own   []bool    // blocks of out whose statement slice is out's own
}

func newVctx(cfg *Config, b *mir.Body, mut versionMut) *vctx {
	x := &vctx{cfg: cfg, tab: cfg.Types, b: b, g: newVcfg(b), mut: mut}
	x.addr, x.hdr = addressedForVersion(b)
	return x
}

// addressedForVersion marks the locals a borrow, reslice, reborrow or asm
// output names (their value may change while a loop runs: the range pass's
// addressedLocals), and the slice locals whose value itself may: borrowed
// whole, or an asm output (rangezone's symOK; an ArgReborrow passes the
// callee its own copy of the slice value).
func addressedForVersion(b *mir.Body) (addr, hdr []bool) {
	addr, hdr = make([]bool, len(b.Locals)), make([]bool, len(b.Locals))
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			r := &s.Rvalue
			if s.Kind == mir.StAssign && (r.Kind == mir.RvRef || r.Kind == mir.RvSlice) {
				addr[r.Place.Local] = true
				if r.Kind == mir.RvRef && r.Place.Proj.Len == 0 {
					hdr[r.Place.Local] = true
				}
			}
			if s.Kind == mir.StAsm {
				for _, op := range b.Asms[s.Asm].Operands {
					if l := op.Out.Local; l != mir.NoLocal && int(l) < len(addr) {
						addr[l], hdr[l] = true, true
					}
				}
			}
		}
		if t := &blk.Term; isCall(t) {
			for _, a := range b.Args[t.Args.Start:t.Args.End()] {
				if a.Mode == mir.ArgReborrow && a.Operand.Kind <= mir.OpMove {
					addr[a.Operand.Place.Local] = true
				}
			}
		}
	}
	return addr, hdr
}

// point is a statement of a block, or its terminator (j == len(Stmts)).
type point struct {
	blk mir.BlockID
	j   int
}

// defOf is the one definition of local v inside loop l, and whether it
// dominates use (an earlier statement of use's block, or a strictly
// dominating block). Every execution that reaches use in some iteration
// then ran the definition earlier in the same iteration: the definition is
// not the header (its only statement defines the header's bool), so it is
// on every path from the header to use, and a path from the loop's entry
// reaches the header first.
func (x *vctx) defOf(l *vloop, v mir.LocalID, use point) (point, bool) {
	if l.defs[v] != 1 || x.addr[v] {
		return point{}, false
	}
	for _, m := range l.members {
		blk := &x.b.Blocks[m]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			if s.Kind != mir.StAssign || s.Place.Local != v || s.Place.Proj.Len != 0 {
				continue
			}
			d := point{m, j}
			if m == use.blk {
				return d, j < use.j
			}
			return d, m != l.h && x.g.dominates(m, use.blk)
		}
	}
	return point{}, false
}

// invariant reports whether local v holds one value through every
// iteration of l: no definition inside it, and no reference to it.
func (x *vctx) invariant(l *vloop, v mir.LocalID) bool {
	return l.defs[v] == 0 && !x.addr[v]
}

// vexpr is a loop-invariant integer value the guard recomputes: an
// immediate, a local read at the guard, or a + - * of two vexprs, each in
// the wrapping form.
type vexpr struct {
	kind uint8 // 0 immediate, 1 local, 2 binary
	imm  uint64
	v    mir.LocalID
	op   mir.BinOp // AddWrap, SubWrap or MulWrap
	a, b *vexpr
}

// invExpr resolves operand o, read at use inside l, to the value it has
// there as a vexpr. Obligation (the guard's base): o's local is invariant,
// or its one definition in l dominates use and is a + - * (checked,
// wrapping or proved) of invariant values. The guard recomputes it with
// the wrapping operators; the fast copy keeps the definition as it is, so
// on an execution that reaches use the definition ran in this iteration
// and did not trap, and a checked or proved + - * that did not trap equals
// its wrapping form.
func (x *vctx) invExpr(l *vloop, o mir.Operand, use point, depth int) (*vexpr, bool) {
	if depth > verMaxDepth {
		return nil, false
	}
	switch o.Kind {
	case mir.OpImm:
		if o.Type != l.ty {
			return nil, false
		}
		return &vexpr{kind: 0, imm: o.Bits}, true
	case mir.OpCopy:
	default:
		return nil, false
	}
	if o.Place.Proj.Len != 0 || x.b.Locals[o.Place.Local].Type != l.ty {
		return nil, false
	}
	v := o.Place.Local
	if x.invariant(l, v) || x.mut == vmutBaseAnyDef && !x.addr[v] && v != l.c && x.b.Locals[v].Kind == mir.LocalUser {
		return &vexpr{kind: 1, v: v}, true
	}
	d, ok := x.defOf(l, v, use)
	if !ok {
		return nil, false
	}
	r := &x.b.Blocks[d.blk].Stmts[d.j].Rvalue
	switch r.Kind {
	case mir.RvUse:
		return x.invExpr(l, r.A, d, depth+1)
	case mir.RvBinary:
		var op mir.BinOp
		switch r.Bin {
		case mir.Add, mir.AddWrap, mir.AddNoWrap:
			op = mir.AddWrap
		case mir.Sub, mir.SubWrap, mir.SubNoWrap:
			op = mir.SubWrap
		case mir.Mul, mir.MulWrap, mir.MulNoWrap:
			op = mir.MulWrap
		default:
			return nil, false
		}
		if r.Type != l.ty {
			return nil, false
		}
		a, ok := x.invExpr(l, r.A, d, depth+1)
		if !ok {
			return nil, false
		}
		bb, ok := x.invExpr(l, r.B, d, depth+1)
		if !ok {
			return nil, false
		}
		return &vexpr{kind: 2, op: op, a: a, b: bb}, true
	}
	return nil, false
}

// ivar is a counter of loop l: l's own (step nil), or a local stepped by
// one exactly once per completed iteration (step its definition).
type ivar struct {
	v    mir.LocalID
	step *point
}

// stepVar reports whether v is a stepped counter of l: its one definition
// in l is `v = Add(copy v, 1)` or AddNoWrap, in a block that dominates the
// latch, so every iteration that reaches the latch runs it exactly once
// (l is innermost: no block runs twice in an iteration).
func (x *vctx) stepVar(l *vloop, v mir.LocalID) (point, bool) {
	if v == l.c || l.defs[v] != 1 || x.addr[v] || x.b.Locals[v].Type != l.ty {
		return point{}, false
	}
	d, ok := x.defOf(l, v, point{l.latch, 0})
	if !ok {
		return point{}, false
	}
	s := &x.b.Blocks[d.blk].Stmts[d.j]
	r := &s.Rvalue
	if r.Kind != mir.RvBinary || r.Bin != mir.Add && r.Bin != mir.AddNoWrap || !wholeOf(r.A, v) || r.B.Kind != mir.OpImm || r.B.Bits != 1 {
		return point{}, false
	}
	return d, true
}

// beforeStep reports whether a read at use comes before v's step d in the
// same iteration: an earlier statement of d's block, or a block that d's
// block does not reach without passing the header.
func (x *vctx) beforeStep(l *vloop, d, use point) bool {
	if use.blk == d.blk {
		return use.j <= d.j
	}
	seen := make([]bool, len(x.b.Blocks))
	stack := []mir.BlockID{d.blk}
	for len(stack) > 0 {
		u := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, s := range x.g.succ[u] {
			if !l.in[s] || s == l.h || seen[s] {
				continue
			}
			if s == use.blk {
				return false
			}
			seen[s] = true
			stack = append(stack, s)
		}
	}
	return true
}

// affine is an index: iv's value at the start of the iteration (c for the
// counter, p0 + k for a stepped p after k iterations), plus or minus base
// (nil: plus nothing). sum is the statement that adds the base, rewritten
// to its proved form in the fast copy.
type affine struct {
	iv   ivar
	base *vexpr
	neg  bool
	sum  *point
}

// affineOf resolves operand o, read at use inside l, to an affine index.
// Obligation: o's value at use is iv's value in this iteration (the
// counter, read anywhere but the latch, whose only statement is its step;
// a stepped counter read before its step) plus or minus base's (invExpr's
// obligation), through definitions that dominate use.
func (x *vctx) affineOf(l *vloop, o mir.Operand, use point, depth int) (affine, bool) {
	if depth > verMaxDepth || o.Kind != mir.OpCopy || o.Place.Proj.Len != 0 {
		return affine{}, false
	}
	v := o.Place.Local
	if x.b.Locals[v].Type != l.ty {
		return affine{}, false
	}
	if v == l.c {
		return affine{iv: ivar{v: v}}, use.blk != l.latch
	}
	if d, ok := x.stepVar(l, v); ok {
		if x.beforeStep(l, d, use) || x.mut == vmutIVAfter {
			return affine{iv: ivar{v: v, step: &d}}, true
		}
		return affine{}, false
	}
	d, ok := x.defOf(l, v, use)
	if !ok {
		return affine{}, false
	}
	r := &x.b.Blocks[d.blk].Stmts[d.j].Rvalue
	switch r.Kind {
	case mir.RvUse:
		return x.affineOf(l, r.A, d, depth+1)
	case mir.RvBinary:
		if r.Type != l.ty {
			return affine{}, false
		}
		switch r.Bin {
		case mir.Add, mir.AddNoWrap, mir.AddWrap:
			for _, sw := range [2]bool{false, true} {
				ia, ib := r.A, r.B
				if sw {
					ia, ib = r.B, r.A
				}
				if a, ok := x.affineOf(l, ia, d, depth+1); ok && a.base == nil {
					if e, ok := x.invExpr(l, ib, d, depth+1); ok {
						a.base, a.sum = e, &d
						return a, true
					}
				}
			}
		case mir.Sub, mir.SubNoWrap, mir.SubWrap:
			if a, ok := x.affineOf(l, r.A, d, depth+1); ok && a.base == nil {
				if e, ok := x.invExpr(l, r.B, d, depth+1); ok {
					a.base, a.neg, a.sum = e, true, &d
					return a, true
				}
			}
		}
	}
	return affine{}, false
}

// lenSrc is a check's length: an immediate, an invariant int local, or the
// length of a slice or str local whose value the loop never changes.
type lenSrc struct {
	kind uint8 // 0 immediate, 1 int local, 2 Len of a slice local
	imm  uint64
	v    mir.LocalID
}

// vcheck is one bounds check the guard can prove: its block (ending in
// `switch copy k [0: panic bounds, _: ok]`), ok, the index and the length.
type vcheck struct {
	blk, ok mir.BlockID
	idx     affine
	n       lenSrc
}

// checkOf recognizes lower's bounds check (checks.go boundsCheck) ending
// blk in l: `u = Trunc[uint](copy i); n = Len(s); nu = Trunc[uint](copy n);
// k = Lt(copy u, copy nu); switch copy k [0: panic bounds, _: ok]`, each
// definition dominating its use, with i an affine index and n's length the
// same on every iteration.
func (x *vctx) checkOf(l *vloop, blk mir.BlockID) (vcheck, bool) {
	b := x.b
	ok := boundsTarget(b, &b.Blocks[blk])
	if ok == mir.NoBlock {
		return vcheck{}, false
	}
	at := point{blk, len(b.Blocks[blk].Stmts)}
	kd, okk := x.defOf(l, b.Blocks[blk].Term.On.Place.Local, at)
	if !okk {
		return vcheck{}, false
	}
	kr := &b.Blocks[kd.blk].Stmts[kd.j].Rvalue
	if kr.Kind != mir.RvBinary || kr.Bin != mir.Lt || kr.A.Kind != mir.OpCopy || kr.A.Place.Proj.Len != 0 {
		return vcheck{}, false
	}
	ud, oku := x.defOf(l, kr.A.Place.Local, kd)
	if !oku {
		return vcheck{}, false
	}
	ur := &b.Blocks[ud.blk].Stmts[ud.j].Rvalue
	if ur.Kind != mir.RvCast || ur.Cast != mir.CastTrunc || ur.Type != types.Uint {
		return vcheck{}, false
	}
	idx, oki := x.affineOf(l, ur.A, ud, 0)
	if !oki {
		return vcheck{}, false
	}
	n, okn := x.lenOf(l, kr.B, kd)
	if !okn {
		return vcheck{}, false
	}
	return vcheck{blk: blk, ok: ok, idx: idx, n: n}, true
}

// lenOf resolves a check's uint length operand nu read at use.
func (x *vctx) lenOf(l *vloop, o mir.Operand, use point) (lenSrc, bool) {
	b := x.b
	if o.Kind != mir.OpCopy || o.Place.Proj.Len != 0 {
		return lenSrc{}, false
	}
	nd, ok := x.defOf(l, o.Place.Local, use)
	if !ok {
		return lenSrc{}, false
	}
	nr := &b.Blocks[nd.blk].Stmts[nd.j].Rvalue
	if nr.Kind != mir.RvCast || nr.Cast != mir.CastTrunc || nr.Type != types.Uint {
		return lenSrc{}, false
	}
	a := nr.A
	switch a.Kind {
	case mir.OpImm:
		if a.Type == types.Int && int64(a.Bits) >= 0 {
			return lenSrc{kind: 0, imm: a.Bits}, true
		}
		return lenSrc{}, false
	case mir.OpCopy:
	default:
		return lenSrc{}, false
	}
	if a.Place.Proj.Len != 0 || b.Locals[a.Place.Local].Type != types.Int {
		return lenSrc{}, false
	}
	v := a.Place.Local
	if x.invariant(l, v) {
		return lenSrc{kind: 1, v: v}, true
	}
	ld, ok := x.defOf(l, v, nd)
	if !ok {
		return lenSrc{}, false
	}
	lr := &b.Blocks[ld.blk].Stmts[ld.j].Rvalue
	if lr.Kind != mir.RvLen || lr.Place.Proj.Len != 0 {
		return lenSrc{}, false
	}
	s := lr.Place.Local
	t := x.tab.Get(b.Locals[s].Type)
	switch t.Kind {
	case types.KArray:
		// Obligation: an array's length is its type's, whatever is
		// written to it.
		if t.Len.Kind == types.CAValue {
			v := x.cfg.Consts.Get(t.Len.Value)
			if n := types.LoadUint(v.Bytes, len(v.Bytes)); n <= 1<<62 {
				return lenSrc{kind: 0, imm: n}, true
			}
		}
	case types.KSlice, types.KMutSlice, types.KStr:
		// Obligation: no statement of the loop assigns s whole (a write
		// through an index is an element's), and nothing borrows it
		// whole, so its length is the guard's.
		if l.defs[s] == 0 && !x.hdr[s] || x.mut == vmutLenAnyDef && !x.hdr[s] {
			return lenSrc{kind: 2, v: s}, true
		}
	}
	return lenSrc{}, false
}

// ensure makes x.out the private copy.
func (x *vctx) ensure() *mir.Body {
	if x.out == nil {
		x.out = cloneForVersion(x.b)
		x.own = make([]bool, len(x.out.Blocks))
	}
	return x.out
}

// newBlock appends an ordinary block to out.
func (x *vctx) newBlock() mir.BlockID {
	out := x.ensure()
	out.Blocks = append(out.Blocks, mir.Block{Origin: mir.OriginBody, Term: mir.Terminator{Kind: mir.TermUnreachable}})
	x.own = append(x.own, true)
	return mir.BlockID(len(out.Blocks) - 1)
}

// stmts is block id's statements in out, made out's own first.
func (x *vctx) stmts(id mir.BlockID) *[]mir.Statement {
	out := x.ensure()
	if !x.own[id] {
		out.Blocks[id].Stmts = slices.Clone(out.Blocks[id].Stmts)
		x.own[id] = true
	}
	return &out.Blocks[id].Stmts
}

// temp appends a compiler temporary of type t.
func (x *vctx) temp(t types.TypeID, span mir.SpanID) mir.LocalID {
	return x.ensure().AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop, Span: span})
}

// gen emits guard code into one block at a time.
type gen struct {
	x    *vctx
	blk  mir.BlockID
	span mir.SpanID
}

// set emits `v = r` and returns copy v, v a new temporary of type t.
func (e *gen) set(t types.TypeID, r mir.Rvalue) mir.Operand {
	v := e.x.temp(t, e.span)
	st := e.x.stmts(e.blk)
	*st = append(*st, mir.Statement{Kind: mir.StAssign, Place: mir.LocalPlace(v), Rvalue: r, Span: e.span})
	return mir.Copy(mir.LocalPlace(v))
}

func (e *gen) bin(op mir.BinOp, t types.TypeID, a, b mir.Operand) mir.Operand {
	rt := t
	if op.IsCompare() {
		rt = types.Bool
	}
	return e.set(rt, mir.Rvalue{Kind: mir.RvBinary, Bin: op, A: a, B: b, Type: rt})
}

func (e *gen) toUint(a mir.Operand) mir.Operand {
	return e.set(types.Uint, mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastTrunc, A: a, Type: types.Uint})
}

// expr emits a vexpr's wrapping recomputation.
func (e *gen) expr(v *vexpr, t types.TypeID) mir.Operand {
	switch v.kind {
	case 0:
		return mir.Imm(t, v.imm)
	case 1:
		return mir.Copy(mir.LocalPlace(v.v))
	}
	return e.bin(v.op, t, e.expr(v.a, t), e.expr(v.b, t))
}

// addOvf emits r = a +% b (sub: a -% b) and the bool "the exact result does
// not fit": for +, a and b have one sign and r the other, ((a ^ r) & (b ^
// r)) < 0; for -, a and b differ in sign and r differs from a, ((a ^ b) &
// (a ^ r)) < 0 (Hacker's Delight 2-13).
func (e *gen) addOvf(sub bool, t types.TypeID, a, b mir.Operand) (r, ovf mir.Operand) {
	if sub {
		r = e.bin(mir.SubWrap, t, a, b)
		ovf = e.bin(mir.Lt, t, e.bin(mir.BitAnd, t, e.bin(mir.BitXor, t, a, b), e.bin(mir.BitXor, t, a, r)), mir.Imm(t, 0))
	} else {
		r = e.bin(mir.AddWrap, t, a, b)
		ovf = e.bin(mir.Lt, t, e.bin(mir.BitAnd, t, e.bin(mir.BitXor, t, a, r), e.bin(mir.BitXor, t, b, r)), mir.Imm(t, 0))
	}
	return r, ovf
}

// test ends the current block with `switch cond [0: no, _: next]` (or the
// other way round when bad: cond true goes to no) and continues in next.
func (e *gen) test(cond mir.Operand, bad bool, no mir.BlockID) {
	next := e.x.newBlock()
	out := e.x.out
	yes0, yes1 := no, next
	if bad {
		yes0, yes1 = next, no
	}
	cs := mir.Range{Start: uint32(len(out.Cases)), Len: 1}
	out.Cases = append(out.Cases, mir.SwitchCase{Value: 0, Target: yes0})
	out.Blocks[e.blk].Term = mir.Terminator{Kind: mir.TermSwitch, On: cond, Cases: cs, Default: yes1, Span: e.span}
	e.blk = next
}

// copyLoop appends a copy of l's blocks to out and returns the map from an
// original block to its copy (NoBlock outside l). Edges inside l go to the
// copies; every other edge (the exit, a panic block) is the original's.
func (x *vctx) copyLoop(l *vloop) []mir.BlockID {
	out := x.ensure()
	m := make([]mir.BlockID, len(x.b.Blocks))
	for i := range m {
		m[i] = mir.NoBlock
	}
	for _, id := range l.members {
		m[id] = x.newBlock()
	}
	for _, id := range l.members {
		src := &x.b.Blocks[id]
		dst := &out.Blocks[m[id]]
		dst.Stmts = slices.Clone(src.Stmts)
		dst.Term = src.Term
		t := &dst.Term
		switch t.Kind {
		case mir.TermGoto:
			if m[t.Target] != mir.NoBlock {
				t.Target = m[t.Target]
			}
		case mir.TermSwitch:
			cs := mir.Range{Start: uint32(len(out.Cases)), Len: t.Cases.Len}
			for _, c := range x.b.Cases[t.Cases.Start:t.Cases.End()] {
				if m[c.Target] != mir.NoBlock {
					c.Target = m[c.Target]
				}
				out.Cases = append(out.Cases, c)
			}
			t.Cases = cs
			if t.Default != mir.NoBlock && m[t.Default] != mir.NoBlock {
				t.Default = m[t.Default]
			}
		}
	}
	return m
}

// retarget sends every edge from block from to old to nw instead.
func (x *vctx) retarget(from, old, nw mir.BlockID) {
	out := x.ensure()
	t := &out.Blocks[from].Term
	switch t.Kind {
	case mir.TermGoto:
		if t.Target == old {
			t.Target = nw
		}
	case mir.TermSwitch:
		cs := mir.Range{Start: uint32(len(out.Cases)), Len: t.Cases.Len}
		for _, c := range out.Cases[t.Cases.Start:t.Cases.End()] {
			if c.Target == old {
				c.Target = nw
			}
			out.Cases = append(out.Cases, c)
		}
		t.Cases = cs
		if t.Default == old {
			t.Default = nw
		}
	}
}

// versionBounds versions up to limit qualifying innermost loops of b.
func versionBounds(cfg *Config, b *mir.Body, mut versionMut, skip map[mir.BlockID]bool, limit int) (*mir.Body, int) {
	if limit <= 0 {
		return b, 0
	}
	x := newVctx(cfg, b, mut)
	done := 0
	for _, l := range countedLoops(x.g, x.tab) {
		if done >= limit {
			break
		}
		if skip[l.h] || l.ops > verMaxLoopOps || l.small || !x.hiInvariant(l) || x.addr[l.c] {
			continue
		}
		var checks []vcheck
		for _, m := range l.members {
			if c, ok := x.checkOf(l, m); ok {
				checks = append(checks, c)
			}
		}
		if len(checks) == 0 {
			continue
		}
		x.versionLoop(l, checks)
		done++
	}
	if x.out == nil {
		return b, 0
	}
	return x.out, done
}

// versionLoop builds the non-trapping entry guard and check-free fast copy of l.
// With c0 on entry and T = (hi - 1) - c0, each check proves lo = S ± B exact
// with lo >= 0 and up = lo + T exact with uint(up) < uint(len); stepped
// counters also prove p0 + T < max. Zero-trip loops run no check either way.
func (x *vctx) versionLoop(l *vloop, checks []vcheck) {
	b := x.b
	span := b.Blocks[l.h].Term.Span
	gb := x.newBlock()
	x.retarget(l.pre, l.h, gb)
	e := &gen{x: x, blk: gb, span: span}
	ty := l.ty
	slow := l.h
	maxI := mir.Imm(ty, 1<<63-1)
	c0 := mir.Copy(mir.LocalPlace(l.c))
	hm1, o1 := e.addOvf(true, ty, l.hi, mir.Imm(ty, 1))
	tt, o2 := e.addOvf(true, ty, hm1, c0)
	if x.mut != vmutNoOvf {
		e.test(o1, true, slow)
		e.test(o2, true, slow)
	}
	starts := map[mir.LocalID]mir.Operand{l.c: c0}
	steps := map[mir.LocalID]bool{}
	for _, c := range checks {
		iv := c.idx.iv
		s, ok := starts[iv.v]
		if !ok {
			s = mir.Copy(mir.LocalPlace(iv.v))
			starts[iv.v] = s
		}
		if iv.step != nil && !steps[iv.v] {
			steps[iv.v] = true
			// p0 + T < max: every step p0 + k + 1, k <= T, fits.
			up, ov := e.addOvf(false, ty, s, tt)
			if x.mut != vmutNoOvf {
				e.test(ov, true, slow)
			}
			e.test(e.bin(mir.Lt, ty, up, maxI), false, slow)
		}
		lo := s
		if c.idx.base != nil {
			var ov mir.Operand
			lo, ov = e.addOvf(c.idx.neg, ty, s, e.expr(c.idx.base, ty))
			if x.mut != vmutNoOvf {
				e.test(ov, true, slow)
			}
		}
		if x.mut != vmutNoLow {
			e.test(e.bin(mir.Ge, ty, lo, mir.Imm(ty, 0)), false, slow)
		}
		up, ov := e.addOvf(false, ty, lo, tt)
		if x.mut != vmutNoOvf {
			e.test(ov, true, slow)
		}
		var n mir.Operand
		switch c.n.kind {
		case 0:
			n = mir.Imm(types.Uint, c.n.imm)
		case 1:
			n = e.toUint(mir.Copy(mir.LocalPlace(c.n.v)))
		default:
			n = e.toUint(e.set(types.Int, mir.Rvalue{Kind: mir.RvLen, Place: mir.LocalPlace(c.n.v), Type: types.Int}))
		}
		op := mir.Lt
		if x.mut == vmutHiLe {
			op = mir.Le
		}
		e.test(e.bin(op, types.Uint, e.toUint(up), n), false, slow)
	}
	m := x.copyLoop(l)
	x.out.Blocks[e.blk].Term = mir.Terminator{Kind: mir.TermGoto, Target: m[l.h], Span: span}
	// The fast copy: each proved check is `goto ok`, its compare kept, and
	// its sum and its counter's step are the proved forms.
	for _, c := range checks {
		blk := &x.out.Blocks[m[c.blk]]
		ok := c.ok
		if m[ok] != mir.NoBlock {
			ok = m[ok]
		}
		blk.Term = mir.Terminator{Kind: mir.TermGoto, Target: ok, Span: blk.Term.Span}
		if s := c.idx.sum; s != nil {
			r := &x.out.Blocks[m[s.blk]].Stmts[s.j].Rvalue
			switch r.Bin {
			case mir.Add:
				r.Bin = mir.AddNoWrap
			case mir.Sub:
				r.Bin = mir.SubNoWrap
			}
		}
		if st := c.idx.iv.step; st != nil {
			r := &x.out.Blocks[m[st.blk]].Stmts[st.j].Rvalue
			if r.Bin == mir.Add {
				r.Bin = mir.AddNoWrap
			}
		}
	}
}
