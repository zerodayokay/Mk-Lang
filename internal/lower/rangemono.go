// Why: rangeversion.go's versionBounds only hoists bounds checks out of
// counted `for c in lo..hi` loops (step +1 in a dedicated latch). Strided
// ascending loops (`for j <= n { flags[j] = 0; j += i }` in codegen/sieve),
// descending loops (`for r != 1 { count[r - 1] = r; r -= 1 }` in fannkuch,
// `for ve > vs && ... { ve -= 1 }` in httpparse), and two-pointer converging
// loops (`for i < j { swap(perm[i], perm[j]); i += 1; j -= 1 }` in fannkuch)
// keep bounds and overflow checks inside every iteration, blocking both
// check elimination and vectorisation.
//
// What: versionMonotone, a codegen-only rewrite called from versionLoops
// (rangeversion.go) under !opt.NoBounds, that versions innermost monotone
// while-loops on a non-trapping entry guard proving all checked indexes in
// [0, len) and induction steps free of overflow.
//
// How: finds innermost single-latch natural loops not handled by countedLoops
// whose header tests one of three monotone patterns on signed 64-bit locals:
//   - monoAsc:  c < hi or c <= hi, single step c = c + delta (invariant delta >= 0)
//   - monoDesc: c > lo, c >= lo, or (with delta == 1) c != lo, single step c = c - delta
//   - monoMeet: a < b or a <= b, single steps a = a + deltaA and b = b - deltaB
// When every checked index reads its induction variable strictly before its
// step (beforeStep), a wrapping guard before the header verifies the step
// bounds and [minIdx, maxIdx] subset of [0, len). On pass, a cloned fast loop
// replaces proved checks with `goto ok` and steps/offsets with AddNoWrap /
// SubNoWrap; on fail, the original loop runs unchanged with identical trap
// sites and ordering.

package lower

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

type monoKind uint8

const (
	monoAsc monoKind = iota + 1
	monoDesc
	monoMeet
)

type monoLoop struct {
	l          *vloop
	kind       monoKind
	cmp        mir.BinOp   // normalized: asc Lt/Le, desc Gt/Ge/Ne, meet Lt/Le
	vA, vB     mir.LocalID // asc/desc: vA is the IV; meet: vA increases, vB decreases
	bound      mir.Operand // asc: hi; desc: lo; meet: unused
	stepA      point       // definition of vA's step inside l
	stepB      point       // meet only: definition of vB's step inside l
	deltaA     mir.Operand // invariant step size for vA
	deltaB     mir.Operand // meet only: invariant step size for vB
	stepANoWrp bool        // true if stepA is already AddNoWrap / SubNoWrap
	stepBNoWrp bool        // true if stepB is already SubNoWrap
}

type monoCheck struct {
	blk, ok mir.BlockID
	iv      mir.LocalID // vA (or vB for monoMeet)
	base    *vexpr      // optional loop-invariant offset
	neg     bool        // true for iv - base
	sum     *point      // optional checked Add/Sub forming iv +/- base
	n       lenSrc
}

// versionMonotone versions up to limit qualifying monotone while-loops of b.
func versionMonotone(cfg *Config, b *mir.Body, mut versionMut, limit int) (*mir.Body, int) {
	if limit <= 0 {
		return b, 0
	}
	x := newVctx(cfg, b, mut)
	done := 0
	for _, ml := range x.monoLoops() {
		if done >= limit {
			break
		}
		if ml.l.ops > verMaxLoopOps {
			continue
		}
		var checks []monoCheck
		for _, m := range ml.l.members {
			if c, ok := x.monoCheckOf(ml, m); ok {
				checks = append(checks, c)
			}
		}
		if len(checks) == 0 {
			continue
		}
		x.emitMonoVersion(ml, checks)
		done++
	}
	if x.out == nil {
		return b, 0
	}
	return x.out, done
}

// monoLoops finds all innermost single-latch natural loops matching one of
// the three monotone patterns (skipping standard `for c in a..b` counted loops
// already handled by versionBounds).
func (x *vctx) monoLoops() []*monoLoop {
	g := x.g
	b := x.b
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
	var out []*monoLoop
	for _, h := range heads {
		in := make([]bool, nb)
		in[h] = true
		members := []mir.BlockID{h}
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
		if _, isCounted := latchCounter(&b.Blocks[l.latch]); isCounted && b.Blocks[l.latch].Term.Target == l.h {
			continue // already handled by versionBounds
		}
		if ml := x.matchMonoLoop(l); ml != nil {
			out = append(out, ml)
		}
	}
	return out
}

// matchMonoLoop validates the loop's CFG hygiene and header/step shape.
func (x *vctx) matchMonoLoop(l *vloop) *monoLoop {
	b, g := x.b, x.g
	for _, p := range g.pred[l.h] {
		if l.in[p] {
			continue
		}
		if l.pre != mir.NoBlock {
			return nil
		}
		l.pre = p
	}
	if l.pre == mir.NoBlock {
		return nil
	}
	if k := b.Blocks[l.pre].Term.Kind; k != mir.TermGoto && k != mir.TermSwitch {
		return nil
	}

	// Hygiene: ordinary blocks with goto/switch, no asm/join/holder loan.
	l.defs = make([]int32, len(b.Locals))
	for _, m := range l.members {
		blk := &b.Blocks[m]
		if blk.Origin != mir.OriginBody || blk.Term.Kind != mir.TermGoto && blk.Term.Kind != mir.TermSwitch {
			return nil
		}
		l.ops += len(blk.Stmts) + 1
		for j := range blk.Stmts {
			st := &blk.Stmts[j]
			switch st.Kind {
			case mir.StAsm, mir.StJoin:
				return nil
			case mir.StAssign:
				if st.Rvalue.Kind == mir.RvRef && st.Rvalue.Origin == mir.OriginLoopHolder {
					return nil
				}
				if st.Place.Proj.Len == 0 || !sliceLike(x.tab, b.Locals[st.Place.Local].Type) {
					l.defs[st.Place.Local]++
				}
			}
		}
	}

	// Header must be `t = cmp(A, B); switch copy t [0: exit, _: body]` (or
	// flipped `[0: body, _: exit]`), with body inside l and exit outside l.
	hb := &b.Blocks[l.h]
	if len(hb.Stmts) != 1 || hb.Term.Kind != mir.TermSwitch {
		return nil
	}
	hs := &hb.Stmts[0]
	hr := &hs.Rvalue
	if hs.Kind != mir.StAssign || hs.Place.Proj.Len != 0 || hr.Kind != mir.RvBinary || !hr.Bin.IsCompare() {
		return nil
	}
	ht := &hb.Term
	if ht.On.Kind != mir.OpCopy || !wholeOf(ht.On, hs.Place.Local) || ht.Cases.Len != 1 || ht.Default == mir.NoBlock {
		return nil
	}
	cs := b.Cases[ht.Cases.Start]
	if cs.Value != 0 {
		return nil
	}
	cmp := hr.Bin
	csExits := exitsLoop(b, l, cs.Target)
	defExits := exitsLoop(b, l, ht.Default)
	switch {
	case csExits && !defExits:
		l.exit, l.body = cs.Target, ht.Default
	case !csExits && defExits:
		l.body, l.exit = cs.Target, ht.Default
		cmp = invertCompare(cmp)
	default:
		return nil
	}

	ty := b.OperandType(hr.A)
	if in := intOf(x.tab, ty); !in.ok || !in.signed || in.bits != 64 {
		return nil
	}
	if b.OperandType(hr.B) != ty {
		return nil
	}
	l.ty = ty

	isLocal := func(o mir.Operand) (mir.LocalID, bool) {
		if o.Kind == mir.OpCopy && o.Place.Proj.Len == 0 && b.Locals[o.Place.Local].Type == ty {
			return o.Place.Local, true
		}
		return 0, false
	}
	isInvOp := func(o mir.Operand) bool {
		switch o.Kind {
		case mir.OpImm:
			return o.Type == ty
		case mir.OpCopy:
			return o.Place.Proj.Len == 0 && b.Locals[o.Place.Local].Type == ty && x.invariant(l, o.Place.Local)
		}
		return false
	}

	// Normalize `(lhs, op, rhs)` so any IV is on the left for asc/desc.
	tryAscDesc := func(lhs, rhs mir.Operand, op mir.BinOp) *monoLoop {
		v, ok := isLocal(lhs)
		if !ok || !isInvOp(rhs) {
			return nil
		}
		switch op {
		case mir.Lt, mir.Le:
			if pt, delta, nw, ok := x.findMonoStep(l, v, false); ok {
				return &monoLoop{l: l, kind: monoAsc, cmp: op, vA: v, bound: rhs, stepA: pt, deltaA: delta, stepANoWrp: nw}
			}
		case mir.Gt, mir.Ge, mir.Ne:
			if pt, delta, nw, ok := x.findMonoStep(l, v, true); ok {
				if op == mir.Ne && (delta.Kind != mir.OpImm || delta.Bits != 1) {
					return nil // Ne requires exact step -1 so it cannot skip past bound
				}
				return &monoLoop{l: l, kind: monoDesc, cmp: op, vA: v, bound: rhs, stepA: pt, deltaA: delta, stepANoWrp: nw}
			}
		}
		return nil
	}

	if ml := tryAscDesc(hr.A, hr.B, cmp); ml != nil {
		return ml
	}
	if ml := tryAscDesc(hr.B, hr.A, flipCompare(cmp)); ml != nil {
		return ml
	}

	// Converging two-pointer loop: `a < b` or `a <= b` (or flipped `b > a` / `b >= a`).
	a, aok := isLocal(hr.A)
	bv, bok := isLocal(hr.B)
	if !aok || !bok || a == bv {
		return nil
	}
	op := cmp
	if op == mir.Gt || op == mir.Ge {
		a, bv = bv, a
		op = flipCompare(op)
	}
	if op != mir.Lt && op != mir.Le {
		return nil
	}
	ptA, dA, nwA, okA := x.findMonoStep(l, a, false)
	ptB, dB, nwB, okB := x.findMonoStep(l, bv, true)
	if !okA || !okB {
		return nil
	}
	return &monoLoop{
		l: l, kind: monoMeet, cmp: op,
		vA: a, vB: bv,
		stepA: ptA, stepB: ptB,
		deltaA: dA, deltaB: dB,
		stepANoWrp: nwA, stepBNoWrp: nwB,
	}
}

// exitsLoop reports whether branching to dst unconditionally exits loop l
// without running any body statement: either dst is outside l, or dst is a
// short-circuit `&&` / `||` false-block (`c = 0; goto m`, `m: switch c [0: exit]`).
func exitsLoop(b *mir.Body, l *vloop, dst mir.BlockID) bool {
	if !l.in[dst] {
		return true
	}
	blk := &b.Blocks[dst]
	if blk.Term.Kind != mir.TermGoto {
		return false
	}
	var cond mir.LocalID
	var val uint64
	hasSet := false
	for i := range blk.Stmts {
		st := &blk.Stmts[i]
		if st.Kind == mir.StNop || st.Kind == mir.StScopeEnd {
			continue
		}
		if st.Kind != mir.StAssign || st.Place.Proj.Len != 0 || hasSet ||
			st.Rvalue.Kind != mir.RvUse || st.Rvalue.A.Kind != mir.OpImm || st.Rvalue.A.Type != types.Bool {
			return false
		}
		cond, val, hasSet = st.Place.Local, st.Rvalue.A.Bits&1, true
	}
	if !hasSet {
		return false
	}
	mb := &b.Blocks[blk.Term.Target]
	for i := range mb.Stmts {
		if mb.Stmts[i].Kind != mir.StNop && mb.Stmts[i].Kind != mir.StScopeEnd {
			return false
		}
	}
	mt := &mb.Term
	if mt.Kind != mir.TermSwitch || mt.On.Kind != mir.OpCopy || !wholeOf(mt.On, cond) {
		return false
	}
	for _, c := range b.Cases[mt.Cases.Start:mt.Cases.End()] {
		if c.Value&1 == val {
			return !l.in[c.Target]
		}
	}
	return mt.Default != mir.NoBlock && !l.in[mt.Default]
}

// findMonoStep finds the single definition of v inside l (not in the header)
// and verifies it is `v = v + delta` (dec == false) or `v = v - delta`
// (dec == true) with loop-invariant delta (OpImm or invariant local).
func (x *vctx) findMonoStep(l *vloop, v mir.LocalID, dec bool) (pt point, delta mir.Operand, noWrap bool, ok bool) {
	if l.defs[v] != 1 || x.addr[v] || x.b.Locals[v].Type != l.ty {
		return point{}, mir.Operand{}, false, false
	}
	for _, m := range l.members {
		if m == l.h {
			continue
		}
		blk := &x.b.Blocks[m]
		for j := range blk.Stmts {
			s := &blk.Stmts[j]
			if s.Kind != mir.StAssign || s.Place.Local != v || s.Place.Proj.Len != 0 {
				continue
			}
			r := &s.Rvalue
			if r.Kind != mir.RvBinary || r.Type != l.ty {
				return point{}, mir.Operand{}, false, false
			}
			if !dec {
				if r.Bin != mir.Add && r.Bin != mir.AddNoWrap {
					return point{}, mir.Operand{}, false, false
				}
				switch {
				case wholeOf(r.A, v):
					delta = r.B
				case wholeOf(r.B, v):
					delta = r.A
				default:
					return point{}, mir.Operand{}, false, false
				}
				noWrap = r.Bin == mir.AddNoWrap
			} else {
				if r.Bin != mir.Sub && r.Bin != mir.SubNoWrap || !wholeOf(r.A, v) {
					return point{}, mir.Operand{}, false, false
				}
				delta = r.B
				noWrap = r.Bin == mir.SubNoWrap
			}
			switch delta.Kind {
			case mir.OpImm:
				if delta.Type != l.ty || int64(delta.Bits) < 0 {
					return point{}, mir.Operand{}, false, false
				}
			case mir.OpCopy:
				if delta.Place.Proj.Len != 0 || x.b.Locals[delta.Place.Local].Type != l.ty || !x.invariant(l, delta.Place.Local) {
					return point{}, mir.Operand{}, false, false
				}
			default:
				return point{}, mir.Operand{}, false, false
			}
			return point{m, j}, delta, noWrap, true
		}
	}
	return point{}, mir.Operand{}, false, false
}

// monoCheckOf recognizes a bounds check ending blk in ml whose index is one
// of ml's monotone IVs (read strictly before its step) plus or minus an
// optional loop-invariant offset, against an invariant length.
func (x *vctx) monoCheckOf(ml *monoLoop, blk mir.BlockID) (monoCheck, bool) {
	l := ml.l
	b := x.b
	okBlk := boundsTarget(b, &b.Blocks[blk])
	if okBlk == mir.NoBlock {
		return monoCheck{}, false
	}
	at := point{blk, len(b.Blocks[blk].Stmts)}
	kd, ok := x.defOf(l, b.Blocks[blk].Term.On.Place.Local, at)
	if !ok {
		return monoCheck{}, false
	}
	kr := &b.Blocks[kd.blk].Stmts[kd.j].Rvalue
	if kr.Kind != mir.RvBinary || kr.Bin != mir.Lt || kr.A.Kind != mir.OpCopy || kr.A.Place.Proj.Len != 0 {
		return monoCheck{}, false
	}
	ud, ok := x.defOf(l, kr.A.Place.Local, kd)
	if !ok {
		return monoCheck{}, false
	}
	ur := &b.Blocks[ud.blk].Stmts[ud.j].Rvalue
	if ur.Kind != mir.RvCast || ur.Cast != mir.CastTrunc || ur.Type != types.Uint {
		return monoCheck{}, false
	}
	iv, base, neg, sum, ok := x.monoIndexOf(ml, ur.A, ud, 0)
	if !ok {
		return monoCheck{}, false
	}
	n, ok := x.lenOf(l, kr.B, kd)
	if !ok {
		return monoCheck{}, false
	}
	return monoCheck{blk: blk, ok: okBlk, iv: iv, base: base, neg: neg, sum: sum, n: n}, true
}

func (x *vctx) monoIndexOf(ml *monoLoop, o mir.Operand, use point, depth int) (iv mir.LocalID, base *vexpr, neg bool, sum *point, ok bool) {
	l := ml.l
	if depth > verMaxDepth || o.Kind != mir.OpCopy || o.Place.Proj.Len != 0 {
		return 0, nil, false, nil, false
	}
	v := o.Place.Local
	if x.b.Locals[v].Type != l.ty {
		return 0, nil, false, nil, false
	}
	if v == ml.vA {
		if x.beforeStep(l, ml.stepA, use) {
			return v, nil, false, nil, true
		}
		return 0, nil, false, nil, false
	}
	if ml.kind == monoMeet && v == ml.vB {
		if x.beforeStep(l, ml.stepB, use) {
			return v, nil, false, nil, true
		}
		return 0, nil, false, nil, false
	}
	d, ok := x.defOf(l, v, use)
	if !ok {
		return 0, nil, false, nil, false
	}
	r := &x.b.Blocks[d.blk].Stmts[d.j].Rvalue
	switch r.Kind {
	case mir.RvUse:
		return x.monoIndexOf(ml, r.A, d, depth+1)
	case mir.RvBinary:
		if r.Type != l.ty {
			return 0, nil, false, nil, false
		}
		switch r.Bin {
		case mir.Add, mir.AddNoWrap, mir.AddWrap:
			for _, sw := range [2]bool{false, true} {
				ia, ib := r.A, r.B
				if sw {
					ia, ib = r.B, r.A
				}
				if iv, b0, _, _, ok := x.monoIndexOf(ml, ia, d, depth+1); ok && b0 == nil {
					if e, ok := x.invExpr(l, ib, d, depth+1); ok {
						return iv, e, false, &d, true
					}
				}
			}
		case mir.Sub, mir.SubNoWrap, mir.SubWrap:
			if iv, b0, _, _, ok := x.monoIndexOf(ml, r.A, d, depth+1); ok && b0 == nil {
				if e, ok := x.invExpr(l, r.B, d, depth+1); ok {
					return iv, e, true, &d, true
				}
			}
		}
	}
	return 0, nil, false, nil, false
}

// emitMonoVersion emits the non-trapping entry guard and check-free fast copy
// of ml.
func (x *vctx) emitMonoVersion(ml *monoLoop, checks []monoCheck) {
	l := ml.l
	b := x.b
	span := b.Blocks[l.h].Term.Span
	gb := x.newBlock()
	x.retarget(l.pre, l.h, gb)
	e := &gen{x: x, blk: gb, span: span}
	ty := l.ty
	slow := l.h
	zero := mir.Imm(ty, 0)

	var ivLo, ivHi mir.Operand
	switch ml.kind {
	case monoAsc:
		c0 := mir.Copy(mir.LocalPlace(ml.vA))
		if ml.deltaA.Kind != mir.OpImm {
			e.test(e.bin(mir.Ge, ty, ml.deltaA, zero), false, slow)
		}
		hiInc := ml.bound
		if ml.cmp == mir.Lt {
			var ov mir.Operand
			hiInc, ov = e.addOvf(true, ty, ml.bound, mir.Imm(ty, 1))
			e.test(ov, true, slow)
		}
		if !ml.stepANoWrp {
			// Ensure c + delta never overflows or underflows for c in [c0, hiInc].
			e.test(e.bin(mir.Ge, ty, c0, zero), false, slow)
			_, ov := e.addOvf(false, ty, hiInc, ml.deltaA)
			e.test(ov, true, slow)
		}
		ivLo, ivHi = c0, hiInc
	case monoDesc:
		c0 := mir.Copy(mir.LocalPlace(ml.vA))
		if ml.deltaA.Kind != mir.OpImm {
			e.test(e.bin(mir.Ge, ty, ml.deltaA, zero), false, slow)
		}
		if ml.cmp == mir.Ne {
			// Guard against starting below bound (which would count down and wrap).
			e.test(e.bin(mir.Ge, ty, c0, ml.bound), false, slow)
		}
		loInc := ml.bound
		if ml.cmp == mir.Gt || ml.cmp == mir.Ne {
			var ov mir.Operand
			loInc, ov = e.addOvf(false, ty, ml.bound, mir.Imm(ty, 1))
			e.test(ov, true, slow)
		}
		if !ml.stepANoWrp {
			// Ensure c - delta never underflows for c >= loInc (and delta >= 0).
			_, ov := e.addOvf(true, ty, loInc, ml.deltaA)
			e.test(ov, true, slow)
		}
		ivLo, ivHi = loInc, c0
	case monoMeet:
		a0 := mir.Copy(mir.LocalPlace(ml.vA))
		b0 := mir.Copy(mir.LocalPlace(ml.vB))
		if ml.deltaA.Kind != mir.OpImm {
			e.test(e.bin(mir.Ge, ty, ml.deltaA, zero), false, slow)
		}
		if ml.deltaB.Kind != mir.OpImm {
			e.test(e.bin(mir.Ge, ty, ml.deltaB, zero), false, slow)
		}
		if !ml.stepANoWrp {
			e.test(e.bin(mir.Ge, ty, a0, zero), false, slow)
			_, ov := e.addOvf(false, ty, b0, ml.deltaA)
			e.test(ov, true, slow)
		}
		if !ml.stepBNoWrp {
			_, ov := e.addOvf(true, ty, a0, ml.deltaB)
			e.test(ov, true, slow)
		}
		ivLo, ivHi = a0, b0
	}

	for _, c := range checks {
		lo := ivLo
		up := ivHi
		if c.base != nil {
			bv := e.expr(c.base, ty)
			var ov1, ov2 mir.Operand
			lo, ov1 = e.addOvf(c.neg, ty, ivLo, bv)
			e.test(ov1, true, slow)
			up, ov2 = e.addOvf(c.neg, ty, ivHi, bv)
			e.test(ov2, true, slow)
		}
		e.test(e.bin(mir.Ge, ty, lo, zero), false, slow)
		var n mir.Operand
		switch c.n.kind {
		case 0:
			n = mir.Imm(types.Uint, c.n.imm)
		case 1:
			n = e.toUint(mir.Copy(mir.LocalPlace(c.n.v)))
		default:
			n = e.toUint(e.set(types.Int, mir.Rvalue{Kind: mir.RvLen, Place: mir.LocalPlace(c.n.v), Type: types.Int}))
		}
		e.test(e.bin(mir.Lt, types.Uint, e.toUint(up), n), false, slow)
	}

	m := x.copyLoop(l)
	x.out.Blocks[e.blk].Term = mir.Terminator{Kind: mir.TermGoto, Target: m[l.h], Span: span}

	for _, c := range checks {
		blk := &x.out.Blocks[m[c.blk]]
		ok := c.ok
		if m[ok] != mir.NoBlock {
			ok = m[ok]
		}
		blk.Term = mir.Terminator{Kind: mir.TermGoto, Target: ok, Span: blk.Term.Span}
		if s := c.sum; s != nil {
			r := &x.out.Blocks[m[s.blk]].Stmts[s.j].Rvalue
			switch r.Bin {
			case mir.Add:
				r.Bin = mir.AddNoWrap
			case mir.Sub:
				r.Bin = mir.SubNoWrap
			}
		}
	}
	if !ml.stepANoWrp {
		r := &x.out.Blocks[m[ml.stepA.blk]].Stmts[ml.stepA.j].Rvalue
		switch r.Bin {
		case mir.Add:
			r.Bin = mir.AddNoWrap
		case mir.Sub:
			r.Bin = mir.SubNoWrap
		}
	}
	if ml.kind == monoMeet && !ml.stepBNoWrp {
		r := &x.out.Blocks[m[ml.stepB.blk]].Stmts[ml.stepB.j].Rvalue
		if r.Bin == mir.Sub {
			r.Bin = mir.SubNoWrap
		}
	}
}
