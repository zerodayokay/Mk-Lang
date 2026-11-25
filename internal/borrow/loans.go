// Why: borrows make loans and assignments overwrite what a place held; both
// change the loan sets every later access is judged against.
//
// What: borrow (RvRef and the RvSlice forms that act as one), e0507Mut,
// pathHeld, loopSkip, destination and destinationFV (the destination rows of
// `p = rv`), scopeEnd, e0404 and e0508.
//
// How: a borrow is classified by the derefs on its place: through a shared
// reference it makes no new loan, through an exclusive one a new loan with
// no target; a raw deref holds nothing. A destination writes a whole local,
// a field or through a deref; a scope end is a shallow access, an E0402 for
// a linear local that may still hold a value, then the purge.

package borrow

import (
	"strings"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// borrow is RvRef (or the RvSlice rows that act as one) of path p.
func (c *checker) borrow(st *state, ctx *stmtCtx, s *mir.Statement, p pathID, excl bool) setID {
	pi := c.paths.get(p)
	root := pi.root
	rv := &s.Rvalue
	col := colRef
	if excl {
		col = colMut
	}
	c.useCheck(st, ctx, root, false)
	// Any raw deref holds nothing; no access past the raw step.
	for i, sp := range pi.steps {
		if sp.kind == stDeref && sp.mode == dmRaw {
			c.access(st, ctx, c.paths.prefix(p, i), colRead, nil)
			return emptySet
		}
	}
	skip := c.loopSkip(rv)
	if r, ok := c.endsForm[siteKey{ctx.b, uint32(ctx.i)}]; ok && c.report && !ctx.term {
		skip = c.exemption(st, ctx, r)
	}
	k := pi.derefs
	if k == 0 {
		l, ok := c.siteLoans[siteKey{ctx.b, uint32(ctx.i)}]
		if !ok {
			l = noLoan
		}
		c.access(st, ctx, p, col, skip)
		ri := &c.roots[root]
		if excl {
			if c.report && (ri.kind == rkUser && !ri.isVar || ri.kind == rkParam && ri.byValueParam && !ri.isVar) {
				c.e0508(ctx, root)
			}
			c.changeSite(st, ctx, root)
		} else if c.w.holdsMutexNonPlain(pi.typ()) {
			c.changeSite(st, ctx, root)
		}
		if l == noLoan {
			return emptySet
		}
		return c.sets.of(uint32(l))
	}
	shared := false
	last := dmShared
	for _, sp := range pi.steps {
		if sp.kind == stDeref {
			if sp.mode == dmShared {
				shared = true
			}
			last = sp.mode
		}
	}
	if excl && shared {
		c.e0507Mut(ctx, p)
		return emptySet
	}
	if last == dmShared {
		// No new loan: as reading the reference.
		c.access(st, ctx, p, colRead, skip)
		h := c.fieldOrHeld(st, root, p)
		if k == 1 {
			return h
		}
		return c.tag(c.behind(st, h), p)
	}
	// The last reference crossed is exclusive: a new loan with no target,
	// holding everything on the path.
	c.access(st, ctx, p, col, skip)
	out := c.pathHeldTagged(st, root, k, p)
	if l, ok := c.siteLoans[siteKey{ctx.b, uint32(ctx.i)}]; ok && l != noLoan {
		out = c.sets.add(out, uint32(l))
	}
	return out
}

// e0507Mut reports E0507 for a mut borrow of path p, which is reached
// through a shared step, at the statement or call. Only the
// report pass emits.
func (c *checker) e0507Mut(ctx *stmtCtx, p pathID) {
	if c.captureBackstop(ctx, p, "mut-borrow") {
		return // captureBackstop reported it
	}
	if c.report {
		d := c.errAt(diag.E0507, ctx.span, "%s is reached through a shared reference and cannot be borrowed mut", c.placeString(p)).
			Text("place", c.placeString(p))
		c.subSliceAlt(d, p)
	}
}

// subSliceAlt names the exclusive view when the shared place is a temporary
// holding a sub-slice, `b[..2].CopyFrom(x)`: `xs[a..b]` is the
// shared view and `(mut xs[a..b])` the exclusive one, which a mut method
// takes; `mut` binds below the call, so the parentheses are needed.
func (c *checker) subSliceAlt(d *diag.Diagnostic, p pathID) {
	pi := c.paths.get(p)
	if d == nil || c.roots[pi.root].kind != rkTemp || c.env.File == nil {
		return
	}
	sp := c.body.Span(c.body.Locals[c.roots[pi.root].local].Span)
	f := c.env.File(sp.File)
	if f == nil || sp.End > f.Len() || sp.End <= sp.Start {
		return
	}
	text := f.Slice(sp)
	if strings.HasSuffix(text, "]") && strings.Contains(text, "[") && strings.Contains(text, "..") && !strings.HasPrefix(text, "mut ") {
		d.Alt("borrow the sub-slice exclusively: `(mut " + text + ")`, since `" + text + "` is the shared view")
	}
}

// pathHeld is H(root) ∪ B(H(root)) ∪ … with one more B per deref after the
// first, reading split field f on a split local when path p names field f and
// tagging layer 1 behind a deref when p steps through a field before a deref.
func (c *checker) fieldOrHeld(st *state, root rootID, p pathID) setID {
	if int(root) < len(c.roots) && c.roots[root].nfields > 0 && st.get(root).fv != 0 {
		pi := c.paths.get(p)
		if k, f, _ := c.keyOf(pi); k == keyField {
			if fs := c.sets.fieldSets(st.get(root).fv); int(f) < len(fs) {
				return fs[f]
			}
		}
	}
	return c.held(st, root)
}

func (c *checker) pathHeldTagged(st *state, root rootID, derefs int, p pathID) setID {
	cur := c.fieldOrHeld(st, root, p)
	acc := cur
	for i := 1; i < derefs; i++ {
		cur = c.behind(st, cur)
		if i == 1 {
			cur = c.tag(cur, p)
			if int(root) < len(c.roots) {
				if ri := &c.roots[root]; ri.kind == rkParam && ri.param != types.NoParam {
					if outer := c.params[ri.param].outer; outer != noLoan && c.sets.has(acc, uint32(outer)) && c.hasIsolatedFieldTarget(ri.param, cur) {
						acc = c.sets.minus(acc, c.sets.of(uint32(outer)))
						acc = c.sets.add(acc, uint32(c.outerKeep(ri.param)))
					}
				}
			}
		}
		acc = c.sets.unite(acc, cur)
	}
	return acc
}

// loopSkip is the skip list of a for loop's element borrow: its
// own loop's holder loan and its own element loan.
func (c *checker) loopSkip(rv *mir.Rvalue) func(loanID) bool {
	if rv.Origin != mir.OriginLoopElement || rv.Loop == mir.NoLoop {
		return nil
	}
	loop := rv.Loop
	return func(l loanID) bool {
		lo := &c.loans[l]
		return lo.loop == loop && (lo.holder || lo.element)
	}
}

// destination is the destination table for `p = rv` with S the rvalue's
// set.
func (c *checker) destination(st *state, ctx *stmtCtx, dst mir.Place, S setID) {
	c.destinationFV(st, ctx, dst, S, 0)
}

// destinationFV is destination with the value's per-field sets fv (0 when
// whole): a whole assignment to a root that keeps field sets takes them
// (fields.go); any other destination reads S alone. A str field of an
// error's payload takes static loans only (staticPayload).
func (c *checker) destinationFV(st *state, ctx *stmtCtx, dst mir.Place, S setID, fv fvID) {
	if c.report && c.errorStrField(dst) {
		c.staticPayload(ctx, S)
	}
	p := c.paths.ofPlace(c.body, c.tab, dst)
	pi := c.paths.get(p)
	root := pi.root
	ri := &c.roots[root]
	if len(pi.steps) == 0 {
		e := st.get(root)
		in := initOf(e)
		if c.report && in != initU {
			if ri.linear && in&initI != 0 {
				d := c.errAt(diag.E0402, ctx.span, "%s is overwritten while it may still hold a value that must be consumed", c.rootName(root)).
					Text("place", c.rootName(root))
				c.releaseFacts(d, root, ri.typ)
			}
			if ri.kind == rkUser && !ri.isVar || ri.kind == rkParam && !ri.isVar {
				c.e0404(ctx, root)
			}
		}
		c.access(st, ctx, p, colWrite, nil)
		if in != initU {
			c.changeSite(st, ctx, root)
		} else {
			c.noteBinding(root, ctx.span)
		}
		e = st.get(root)
		e.init = initI
		e.moved = emptySet
		e.fv = 0 // a whole assignment replaces the value and its field sets
		c.put(st, root, e)
		c.setSplit(st, root, S, fv)
		c.kill(st, p)
		if root == 0 {
			c.boundary(st, ctx, S)
		}
		return
	}
	if pi.derefs == 0 {
		c.useCheck(st, ctx, root, false)
		if c.report {
			if ri.kind == rkUser && !ri.isVar || ri.kind == rkParam && !ri.isVar {
				c.e0404(ctx, root)
			}
			if c.w.linear(pi.typ()) {
				d := c.errAt(diag.E0402, ctx.span, "assigning to %s drops the value it holds, which must be consumed", c.placeString(p)).
					Text("place", c.placeString(p))
				c.releaseFacts(d, noRoot, pi.typ())
			}
		}
		c.access(st, ctx, p, colWrite, nil)
		c.changeSite(st, ctx, root)
		if !c.fieldWrite(st, p, S) {
			// A whole root, or a place that names no field: the root is
			// whole from here, and S joins its one set.
			c.collapse(st, root)
			c.addHeld(st, root, S)
		}
		c.kill(st, p)
		return
	}
	// Through a deref: the direct write rule.
	for _, sp := range pi.steps {
		if sp.kind == stDeref && sp.mode == dmShared {
			if c.captureBackstop(ctx, p, "write") {
				return // captureBackstop reported it
			}
			if c.report {
				c.errAt(diag.E0507, ctx.span, "%s is reached through a shared reference and cannot be written", c.placeString(p)).
					Text("place", c.placeString(p))
			}
			return
		}
	}
	c.useCheck(st, ctx, root, false)
	if c.report && c.w.linear(pi.typ()) {
		d := c.errAt(diag.E0402, ctx.span, "assigning to %s drops the value it holds, which must be consumed", c.placeString(p)).
			Text("place", c.placeString(p))
		c.releaseFacts(d, noRoot, pi.typ())
	}
	c.access(st, ctx, p, colWrite, nil)
	c.directWrite(st, ctx, p, S)
	c.kill(st, p)
}

// scopeEnd is StScopeEnd x: a shallow ScopeEnd access (E0505, or
// E0506 for a temporary or loop holder, at the loan's creation site); E0402
// if x is linear and may still hold a value; then the purge.
func (c *checker) scopeEnd(st *state, ctx *stmtCtx, x mir.LocalID) {
	r := rootID(x)
	p := c.paths.ofPlace(c.body, c.tab, mir.LocalPlace(x))
	c.access(st, ctx, p, colEnd, nil)
	if c.report && c.roots[r].linear && initOf(st.get(r))&initI != 0 {
		d := c.errAt(diag.E0402, c.body.Locals[x].Span, "%s is not consumed before its scope ends", c.rootName(r)).
			Text("place", c.rootName(r)).Loc("path", c.body.Span(ctx.span))
		c.releaseFacts(d, r, c.roots[r].typ)
	}
	c.purge(st, r)
}

func (c *checker) e0404(ctx *stmtCtx, r rootID) {
	d := c.errAt(diag.E0404, ctx.span, "%s is not var and is already assigned", c.rootName(r)).
		Loc("declared_at", c.body.Span(c.body.Locals[c.roots[r].local].Span))
	c.addVarFix(d, r)
}

func (c *checker) e0508(ctx *stmtCtx, r rootID) {
	d := c.errAt(diag.E0508, ctx.span, "%s is not var and cannot be borrowed mut", c.rootName(r)).
		Loc("declared_at", c.body.Span(c.body.Locals[c.roots[r].local].Span))
	c.addVarFix(d, r)
}
