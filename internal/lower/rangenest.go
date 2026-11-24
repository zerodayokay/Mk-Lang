// Why: versionBounds (rangeversion.go) only versions innermost counted loops.
// In 2D counted nests (`for k in 0..n { a[i*n+k]; for j in 0..n { b[k*n+j] ... } }`),
// that leaves the guard, any outer bounds check, and any checked row multiply
// (`k * n`) executing on every iteration of the outer loop k.
//
// What: versionNestedBounds, a codegen-only rewrite called from versionLoops
// before versionBounds under !opt.NoBounds. It versions depth-2 counted-loop
// nests (an outer counted loop enclosing one or more innermost counted loops)
// on a single non-trapping entry guard before the outer loop.
//
// How: for each qualifying depth-2 nest O with innermost counted loops I_m
// whose entry start and bound are O-invariant, every checked bounds index in O
// or any I_m is resolved to an arithmetic expression tree over O-invariants,
// O.c, and I_m.c. Before O's header, a wrapping guard verifies non-empty loop
// ranges and propagates exact [min, max] intervals bottom-up through every
// Add, Sub, and Mul (using `uint(a_lo|b_lo|a_hi|b_hi) <= 0x7fffffff` to prove
// non-negative overflow-free products in one compare) and proves
// `0 <= min <= max < len`. On pass, a cloned fast copy of the full nest
// replaces proved checks with `goto ok` and proved checked ops with AddNoWrap,
// SubNoWrap, or MulNoWrap; on fail, the original nest runs unchanged with
// identical trap sites and ordering.

package lower

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

type nestExprKind uint8

const (
	neImm nestExprKind = iota
	neLocal
	neOuterIV
	neInnerIV
	neBin
)

type nestExpr struct {
	kind  nestExprKind
	imm   uint64
	v     mir.LocalID
	inner *nestInner
	op    mir.BinOp
	pt    point
	a, b  *nestExpr
	inv   bool
}

type nestInner struct {
	l        *vloop
	initExpr *nestExpr
	hiExpr   *nestExpr
	j0, jmax mir.Operand // filled during guard emission
}

type nestCheck struct {
	blk, ok mir.BlockID
	inner   *nestInner // nil for checks directly in O
	idx     *nestExpr
	n       lenSrc
}

type nestCand struct {
	o          *vloop
	inners     []*nestInner
	owner      map[mir.BlockID]*nestInner
	inAnyInner []bool
}

// versionNestedBounds versions qualifying depth-2 counted-loop nests of b.
// It returns the updated body, how many nests were versioned, and the set of
// original inner-loop headers covered by versioned nests (so versionBounds
// skips cold fallback inner loops).
func versionNestedBounds(cfg *Config, b *mir.Body, mut versionMut) (*mir.Body, int, map[mir.BlockID]bool) {
	x := newVctx(cfg, b, mut)
	cands := x.nestedCountedLoops()
	if len(cands) == 0 {
		return b, 0, nil
	}
	inAny := make([]bool, len(b.Blocks))
	for _, c := range cands {
		for _, m := range c.o.members {
			inAny[m] = true
		}
	}
	var skip map[mir.BlockID]bool
	done := 0
	for _, c := range cands {
		if done == verMaxLoops {
			break
		}
		if inAny[c.o.pre] {
			continue
		}
		var checks []nestCheck
		for _, m := range c.o.members {
			if ck, ok := x.nestCheckOf(c, m); ok {
				checks = append(checks, ck)
			}
		}
		if len(checks) == 0 {
			continue
		}
		x.emitNestVersion(c, checks)
		done++
		if skip == nil {
			skip = map[mir.BlockID]bool{}
		}
		for _, in := range c.inners {
			skip[in.l.h] = true
		}
	}
	if x.out == nil {
		return b, 0, nil
	}
	return x.out, done, skip
}

// nestedCountedLoops finds all qualifying depth-2 counted-loop nests in x.b.
func (x *vctx) nestedCountedLoops() []*nestCand {
	g, b := x.g, x.b
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
	if len(heads) < 2 {
		return nil
	}
	slices.Sort(heads)
	isHead := make([]bool, nb)
	for _, h := range heads {
		isHead[h] = true
	}

	type rawLoop struct {
		h        mir.BlockID
		in       []bool
		members  []mir.BlockID
		subHeads []mir.BlockID
	}
	raws := make(map[mir.BlockID]*rawLoop, len(heads))
	innerByHead := map[mir.BlockID]*vloop{}

	for _, h := range heads {
		in := make([]bool, nb)
		in[h] = true
		members := []mir.BlockID{h}
		stack := slices.Clone(latches[h])
		var subs []mir.BlockID
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if in[u] {
				continue
			}
			in[u] = true
			members = append(members, u)
			if isHead[u] {
				subs = append(subs, u)
			}
			stack = append(stack, g.pred[u]...)
		}
		slices.Sort(members)
		slices.Sort(subs)
		raws[h] = &rawLoop{h: h, in: in, members: members, subHeads: subs}
		if len(subs) == 0 && len(latches[h]) == 1 {
			l := &vloop{h: h, latch: latches[h][0], in: in, members: members, pre: mir.NoBlock}
			if l.shape(g, x.tab) {
				innerByHead[h] = l
			}
		}
	}

	var out []*nestCand
	for _, h := range heads {
		rl := raws[h]
		if len(rl.subHeads) == 0 || len(latches[h]) != 1 {
			continue
		}
		// Every sub-header must be an innermost counted loop contained in h.
		allInner := true
		for _, sh := range rl.subHeads {
			il := innerByHead[sh]
			if il == nil {
				allInner = false
				break
			}
			for _, m := range il.members {
				if !rl.in[m] {
					allInner = false
					break
				}
			}
			if !allInner {
				break
			}
		}
		if !allInner {
			continue
		}
		o := &vloop{h: h, latch: latches[h][0], in: rl.in, members: rl.members, pre: mir.NoBlock}
		if !o.shape(g, x.tab) || o.small || o.ops > verMaxLoopOps || !x.hiInvariant(o) || x.addr[o.c] {
			continue
		}

		cand := &nestCand{o: o, owner: map[mir.BlockID]*nestInner{}, inAnyInner: make([]bool, nb)}
		for _, sh := range rl.subHeads {
			for _, m := range innerByHead[sh].members {
				cand.inAnyInner[m] = true
			}
		}
		for _, sh := range rl.subHeads {
			il := innerByHead[sh]
			if il.ty != o.ty || x.addr[il.c] || !o.in[il.pre] || il.in[il.pre] {
				continue
			}
			initExpr, ok := x.innerInitExpr(cand, il)
			if !ok {
				continue
			}
			hiExpr, ok := x.nestExprOf(cand, nil, il.hi, point{il.h, 0}, true, 0)
			if !ok {
				continue
			}
			ni := &nestInner{l: il, initExpr: initExpr, hiExpr: hiExpr}
			cand.inners = append(cand.inners, ni)
			for _, m := range il.members {
				cand.owner[m] = ni
			}
		}
		if len(cand.inners) == 0 {
			continue
		}
		out = append(out, cand)
	}
	return out
}

// innerInitExpr resolves the single non-latch initialization of il.c inside o
// to an O-invariant nestExpr. Because every sub-header of o is an innermost
// loop, o \ union(inners) is acyclic; an init statement outside il dominating
// il.pre (with o.defs[il.c] == 2) defines il.c on every entry to il.
func (x *vctx) innerInitExpr(c *nestCand, il *vloop) (*nestExpr, bool) {
	o := c.o
	if o.defs[il.c] != 2 {
		return nil, false
	}
	b := x.b
	var found bool
	var initPt point
	for _, m := range o.members {
		if il.in[m] {
			continue
		}
		blk := &b.Blocks[m]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			if s.Kind != mir.StAssign || s.Place.Local != il.c {
				continue
			}
			if s.Place.Proj.Len != 0 || found {
				return nil, false
			}
			found = true
			initPt = point{m, j}
		}
	}
	if !found || initPt.blk == o.h || c.inAnyInner[initPt.blk] {
		return nil, false
	}
	if initPt.blk != il.pre && !x.g.dominates(initPt.blk, il.pre) {
		return nil, false
	}
	r := &b.Blocks[initPt.blk].Stmts[initPt.j].Rvalue
	switch r.Kind {
	case mir.RvUse:
		return x.nestExprOf(c, nil, r.A, initPt, true, 0)
	case mir.RvBinary:
		return x.nestBinExpr(c, nil, r, initPt, true, 0)
	}
	return nil, false
}

// nestExprOf resolves operand op read at use inside outer loop c.o to a
// nestExpr tree. If invOnly is true, neither c.o.c nor inner.l.c is permitted.
func (x *vctx) nestExprOf(c *nestCand, inner *nestInner, op mir.Operand, use point, invOnly bool, depth int) (*nestExpr, bool) {
	ol := c.o
	if depth > verMaxDepth {
		return nil, false
	}
	switch op.Kind {
	case mir.OpImm:
		if op.Type != ol.ty {
			return nil, false
		}
		return &nestExpr{kind: neImm, imm: op.Bits, inv: true}, true
	case mir.OpCopy:
	default:
		return nil, false
	}
	if op.Place.Proj.Len != 0 || x.b.Locals[op.Place.Local].Type != ol.ty {
		return nil, false
	}
	v := op.Place.Local
	if v == ol.c {
		if invOnly || use.blk == ol.latch {
			return nil, false
		}
		return &nestExpr{kind: neOuterIV, v: v, inv: false}, true
	}
	if inner != nil && v == inner.l.c {
		if invOnly || use.blk == inner.l.latch {
			return nil, false
		}
		return &nestExpr{kind: neInnerIV, v: v, inner: inner, inv: false}, true
	}
	if x.invariant(ol, v) || x.mut == vmutBaseAnyDef && !x.addr[v] && v != ol.c && (inner == nil || v != inner.l.c) && x.b.Locals[v].Kind == mir.LocalUser {
		return &nestExpr{kind: neLocal, v: v, inv: true}, true
	}
	d, ok := x.defOf(ol, v, use)
	if !ok {
		return nil, false
	}
	if c.inAnyInner[d.blk] && (inner == nil || c.owner[d.blk] != inner) {
		return nil, false
	}
	r := &x.b.Blocks[d.blk].Stmts[d.j].Rvalue
	switch r.Kind {
	case mir.RvUse:
		return x.nestExprOf(c, inner, r.A, d, invOnly, depth+1)
	case mir.RvBinary:
		return x.nestBinExpr(c, inner, r, d, invOnly, depth+1)
	}
	return nil, false
}

func (x *vctx) nestBinExpr(c *nestCand, inner *nestInner, r *mir.Rvalue, d point, invOnly bool, depth int) (*nestExpr, bool) {
	if r.Type != c.o.ty {
		return nil, false
	}
	var wrap bool
	switch r.Bin {
	case mir.Add, mir.AddNoWrap, mir.Sub, mir.SubNoWrap, mir.Mul, mir.MulNoWrap:
	case mir.AddWrap, mir.SubWrap, mir.MulWrap:
		wrap = true
	default:
		return nil, false
	}
	ea, ok := x.nestExprOf(c, inner, r.A, d, invOnly, depth)
	if !ok {
		return nil, false
	}
	eb, ok := x.nestExprOf(c, inner, r.B, d, invOnly, depth)
	if !ok {
		return nil, false
	}
	inv := ea.inv && eb.inv
	if wrap && !inv {
		// Wrapping arithmetic is non-monotone over intervals; only permit it on
		// O-invariant point values.
		return nil, false
	}
	return &nestExpr{kind: neBin, op: r.Bin, pt: d, a: ea, b: eb, inv: inv}, true
}

// nestCheckOf recognizes a bounds check ending blk in c.o whose index is a
// nestExpr and whose length is O-invariant.
func (x *vctx) nestCheckOf(c *nestCand, blk mir.BlockID) (nestCheck, bool) {
	b := x.b
	okBlk := boundsTarget(b, &b.Blocks[blk])
	if okBlk == mir.NoBlock {
		return nestCheck{}, false
	}
	inner := c.owner[blk]
	if inner == nil && c.inAnyInner[blk] {
		return nestCheck{}, false
	}
	at := point{blk, len(b.Blocks[blk].Stmts)}
	kd, ok := x.defOf(c.o, b.Blocks[blk].Term.On.Place.Local, at)
	if !ok {
		return nestCheck{}, false
	}
	kr := &b.Blocks[kd.blk].Stmts[kd.j].Rvalue
	if kr.Kind != mir.RvBinary || kr.Bin != mir.Lt || kr.A.Kind != mir.OpCopy || kr.A.Place.Proj.Len != 0 {
		return nestCheck{}, false
	}
	ud, ok := x.defOf(c.o, kr.A.Place.Local, kd)
	if !ok {
		return nestCheck{}, false
	}
	ur := &b.Blocks[ud.blk].Stmts[ud.j].Rvalue
	if ur.Kind != mir.RvCast || ur.Cast != mir.CastTrunc || ur.Type != types.Uint {
		return nestCheck{}, false
	}
	idx, ok := x.nestExprOf(c, inner, ur.A, ud, false, 0)
	if !ok {
		return nestCheck{}, false
	}
	n, ok := x.lenOf(c.o, kr.B, kd)
	if !ok {
		return nestCheck{}, false
	}
	return nestCheck{blk: blk, ok: okBlk, inner: inner, idx: idx, n: n}, true
}

// emitNestVersion emits the non-trapping entry guard before c.o.h and clones
// the entire depth-2 nest into a check-free fast copy.
func (x *vctx) emitNestVersion(c *nestCand, checks []nestCheck) {
	o := c.o
	b := x.b
	span := b.Blocks[o.h].Term.Span
	gb := x.newBlock()
	x.retarget(o.pre, o.h, gb)
	e := &gen{x: x, blk: gb, span: span}
	ty := o.ty
	slow := o.h

	// Outer loop non-empty: k0 < hiK -> kmax = hiK - 1 (exact since hiK > k0 >= MIN).
	k0 := mir.Copy(mir.LocalPlace(o.c))
	e.test(e.bin(mir.Lt, ty, k0, o.hi), false, slow)
	kmax := e.bin(mir.SubWrap, ty, o.hi, mir.Imm(ty, 1))

	// Initialize [j0, jmax] for each inner loop referenced by at least one check.
	usedInner := map[*nestInner]bool{}
	for _, ck := range checks {
		if ck.inner != nil {
			usedInner[ck.inner] = true
		}
	}
	for _, in := range c.inners {
		if !usedInner[in] {
			continue
		}
		j0, _ := x.emitNestInterval(e, in.initExpr, ty, k0, kmax, slow)
		hiJ, _ := x.emitNestInterval(e, in.hiExpr, ty, k0, kmax, slow)
		e.test(e.bin(mir.Lt, ty, j0, hiJ), false, slow)
		in.j0 = j0
		in.jmax = e.bin(mir.SubWrap, ty, hiJ, mir.Imm(ty, 1))
	}

	// Prove every check's index interval lies in [0, len) with no intermediate
	// overflow.
	for _, ck := range checks {
		lo, hi := x.emitNestInterval(e, ck.idx, ty, k0, kmax, slow)
		if x.mut != vmutNoLow {
			e.test(e.bin(mir.Ge, ty, lo, mir.Imm(ty, 0)), false, slow)
		}
		var n mir.Operand
		switch ck.n.kind {
		case 0:
			n = mir.Imm(types.Uint, ck.n.imm)
		case 1:
			n = e.toUint(mir.Copy(mir.LocalPlace(ck.n.v)))
		default:
			n = e.toUint(e.set(types.Int, mir.Rvalue{Kind: mir.RvLen, Place: mir.LocalPlace(ck.n.v), Type: types.Int}))
		}
		op := mir.Lt
		if x.mut == vmutHiLe {
			op = mir.Le
		}
		e.test(e.bin(op, types.Uint, e.toUint(hi), n), false, slow)
	}

	m := x.copyLoop(o)
	x.out.Blocks[e.blk].Term = mir.Terminator{Kind: mir.TermGoto, Target: m[o.h], Span: span}

	// Fast copy: bypass proved checks and promote proved checked arithmetic.
	for _, ck := range checks {
		blk := &x.out.Blocks[m[ck.blk]]
		ok := ck.ok
		if m[ok] != mir.NoBlock {
			ok = m[ok]
		}
		blk.Term = mir.Terminator{Kind: mir.TermGoto, Target: ok, Span: blk.Term.Span}
		x.promoteNestExpr(m, ck.idx)
	}
	for _, in := range c.inners {
		if !usedInner[in] {
			continue
		}
		x.promoteNestExpr(m, in.initExpr)
		x.promoteNestExpr(m, in.hiExpr)
	}
}

// emitNestInterval emits non-trapping guard code computing exact bounds
// [lo, hi] (with lo <= hi) for ne over all iterations of O and its used inner
// loops, branching to slow if any checked arithmetic could overflow.
func (x *vctx) emitNestInterval(e *gen, ne *nestExpr, ty types.TypeID, k0, kmax mir.Operand, slow mir.BlockID) (lo, hi mir.Operand) {
	switch ne.kind {
	case neImm:
		op := mir.Imm(ty, ne.imm)
		return op, op
	case neLocal:
		op := mir.Copy(mir.LocalPlace(ne.v))
		return op, op
	case neOuterIV:
		return k0, kmax
	case neInnerIV:
		return ne.inner.j0, ne.inner.jmax
	}
	alo, ahi := x.emitNestInterval(e, ne.a, ty, k0, kmax, slow)
	blo, bhi := x.emitNestInterval(e, ne.b, ty, k0, kmax, slow)
	if ne.inv {
		switch ne.op {
		case mir.AddWrap:
			r := e.bin(mir.AddWrap, ty, alo, blo)
			return r, r
		case mir.SubWrap:
			r := e.bin(mir.SubWrap, ty, alo, blo)
			return r, r
		case mir.MulWrap:
			r := e.bin(mir.MulWrap, ty, alo, blo)
			return r, r
		case mir.Add, mir.AddNoWrap:
			r, ov := e.addOvf(false, ty, alo, blo)
			if x.mut != vmutNoOvf {
				e.test(ov, true, slow)
			}
			return r, r
		case mir.Sub, mir.SubNoWrap:
			r, ov := e.addOvf(true, ty, alo, blo)
			if x.mut != vmutNoOvf {
				e.test(ov, true, slow)
			}
			return r, r
		case mir.Mul, mir.MulNoWrap:
			if x.mut != vmutNoOvf {
				or := e.bin(mir.BitOr, ty, alo, blo)
				e.test(e.bin(mir.Le, types.Uint, e.toUint(or), mir.Imm(types.Uint, 0x7fffffff)), false, slow)
			}
			r := e.bin(mir.MulWrap, ty, alo, blo)
			return r, r
		}
	}
	switch ne.op {
	case mir.Add, mir.AddNoWrap:
		lo, ov1 := e.addOvf(false, ty, alo, blo)
		hi, ov2 := e.addOvf(false, ty, ahi, bhi)
		if x.mut != vmutNoOvf {
			e.test(ov1, true, slow)
			e.test(ov2, true, slow)
		}
		return lo, hi
	case mir.Sub, mir.SubNoWrap:
		lo, ov1 := e.addOvf(true, ty, alo, bhi)
		hi, ov2 := e.addOvf(true, ty, ahi, blo)
		if x.mut != vmutNoOvf {
			e.test(ov1, true, slow)
			e.test(ov2, true, slow)
		}
		return lo, hi
	case mir.Mul, mir.MulNoWrap:
		// With alo <= ahi and blo <= bhi, uint(alo|blo|ahi|bhi) <= 0x7fffffff
		// proves 0 <= alo <= ahi <= 2^31-1 and 0 <= blo <= bhi <= 2^31-1, so
		// 0 <= alo*blo <= a*b <= ahi*bhi < 2^62 < MAX (no overflow).
		if x.mut != vmutNoOvf {
			or1 := e.bin(mir.BitOr, ty, alo, blo)
			or2 := e.bin(mir.BitOr, ty, ahi, bhi)
			orAll := e.bin(mir.BitOr, ty, or1, or2)
			e.test(e.bin(mir.Le, types.Uint, e.toUint(orAll), mir.Imm(types.Uint, 0x7fffffff)), false, slow)
		}
		lo = e.bin(mir.MulWrap, ty, alo, blo)
		hi = e.bin(mir.MulWrap, ty, ahi, bhi)
		return lo, hi
	}
	panic("unreachable nestExpr op")
}

// promoteNestExpr rewrites every checked Add/Sub/Mul in ne to its NoWrap form
// in the cloned fast copy m.
func (x *vctx) promoteNestExpr(m []mir.BlockID, ne *nestExpr) {
	if ne.kind != neBin {
		return
	}
	x.promoteNestExpr(m, ne.a)
	x.promoteNestExpr(m, ne.b)
	dst := m[ne.pt.blk]
	if dst == mir.NoBlock {
		return
	}
	r := &x.out.Blocks[dst].Stmts[ne.pt.j].Rvalue
	switch r.Bin {
	case mir.Add:
		r.Bin = mir.AddNoWrap
	case mir.Sub:
		r.Bin = mir.SubNoWrap
	case mir.Mul:
		r.Bin = mir.MulNoWrap
	}
}
