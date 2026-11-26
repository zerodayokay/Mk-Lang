// Why: every way out of a scope (return, fail, try, break, continue) must run
// the scopes' cleanups and end their temporaries and locals, in order, on
// that edge; this file builds those exit chains.
//
// What: exitScopes and endScopeLocals (the exit chain), registerCleanup and
// runCleanup (defer and onerr), successExit and errorExit (return and fail),
// narrowBack, widen and jump (break and continue).
//
// How: each exit edge gets its own copy of every cleanup it runs, entered
// through a new block with one predecessor (an onerr that consumes a linear
// value is entered through an OnerrGuard). A defer or onerr statement itself
// emits nothing.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// exitKind is why an edge leaves scopes.
type exitKind uint8

const (
	exitFall exitKind = iota
	exitBreak
	exitReturn
	exitFail
	exitTry
)

// leavesElse reports whether leaving scopes[from:] leaves an else handler of
// an E!T call or value.
func (bl *builder) leavesElse(from int) bool {
	for i := from; i < len(bl.scopes); i++ {
		if bl.scopes[i].kind == scopeElseHandler {
			return true
		}
	}
	return false
}

// exitScopes emits the exit chain for scopes[from:], innermost first: each
// scope's cleanups latest first (onerr only on an error exit), its join, its
// temporary frames, its locals. It leaves the scope
// stack unchanged.
func (bl *builder) exitScopes(from int, kind exitKind, at source.Span) {
	runsOnerr := kind == exitFail || kind == exitTry || (kind == exitBreak && bl.leavesElse(from))
	for i := len(bl.scopes) - 1; i >= from; i-- {
		if !bl.open() {
			return
		}
		s := bl.scopes[i]
		// The cleanups of scope i read the locals as its own code does: an
		// edge out of an if T is X branch has left the branch's narrowing
		// once it reaches a scope outside it (after the write-backs).
		saveRemap := bl.remap
		bl.remap = s.remap
		for j := len(s.cleanups) - 1; j >= 0; j-- {
			d := s.cleanups[j]
			if !bl.defers[d].node.Onerr || runsOnerr {
				bl.runCleanup(d)
			}
		}
		bl.remap = saveRemap
		for _, n := range s.narrowed {
			if n.writeBack {
				t := bl.b.Locals[n.orig].Type
				bl.assign(bl.b.Place(n.orig), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: mir.Copy(bl.b.Place(n.narrowed)), Type: t}, at)
			}
		}
		if s.kind == scopeStmtScope && s.scopeVar != mir.NoLocal {
			bl.emit(mir.Statement{Kind: mir.StJoin, Local: s.scopeVar, Cancel: kind != exitFall, Span: bl.span(at)})
			if s.tryScope && kind == exitFall {
				bl.tryScopeResult(i, at)
			}
		}
		bl.endScopeLocals(i, at)
	}
}

// endScopeLocals is the last two steps of the exit chain for scopes[i]: its temporary
// frames, innermost first, then its locals in reverse declaration order.
func (bl *builder) endScopeLocals(i int, at source.Span) {
	s := bl.scopes[i]
	upper := len(bl.temps)
	if i+1 < len(bl.scopes) {
		upper = bl.scopes[i+1].tempDepth
	}
	for f := upper - 1; f >= s.tempDepth; f-- {
		ts := bl.temps[f].temps
		for k := len(ts) - 1; k >= 0; k-- {
			bl.scopeEnd(ts[k], at)
		}
	}
	for k := len(s.locals) - 1; k >= 0; k-- {
		bl.scopeEnd(s.locals[k], at)
	}
}

// registerCleanup is a defer or onerr statement: nothing runs here.
func (bl *builder) registerCleanup(d *ast.DeferStmt) {
	id := mir.DeferID(len(bl.b.Defers))
	bl.b.Defers = append(bl.b.Defers, mir.DeferInfo{Onerr: d.Onerr, Span: bl.span(d.Span())})
	rec := deferRec{node: d, consumes: mir.NoLocal}
	if d.Onerr {
		switch c := bl.in.OnerrConsumes(d.ID()); {
		case c.Binding != ast.NoNode:
			rec.consumes = bl.localOf(c.Binding, d.Span())
		case c.Param != types.NoParam:
			rec.consumes = mir.LocalID(c.Param)
		}
	}
	bl.defers = append(bl.defers, rec)
	s := &bl.scopes[len(bl.scopes)-1]
	s.cleanups = append(s.cleanups, id)
}

// runCleanup lowers a fresh copy of cleanup d on the current edge:
// a new entry block with one predecessor, blocks of the copy's origin, and
// a fresh body block after it; an onerr that consumes a linear value is
// entered through an OnerrGuard.
func (bl *builder) runCleanup(d mir.DeferID) {
	rec := bl.defers[d]
	origin := mir.OriginDefer
	if rec.node.Onerr {
		origin = mir.OriginOnerr
	}
	saveO, saveD := bl.origin, bl.deferID
	at := rec.node.Span()
	after := bl.newBlock()
	bl.origin, bl.deferID = origin, d
	entry := bl.newBlock()
	if rec.consumes != mir.NoLocal {
		bl.term(mir.Terminator{Kind: mir.TermOnerrGuard, Value: rec.consumes, Yes: entry, No: after}, at)
	} else {
		bl.gotoBlock(entry, at)
	}
	bl.cur = entry
	bl.openScope(scopeCleanup)
	if rec.node.Body != nil {
		bl.blockStmts(rec.node.Body)
		bl.closeScope(closeBrace(rec.node.Body))
	} else {
		bl.exprStmt(rec.node.X)
		bl.closeScope(rec.node.X.Span())
	}
	if bl.open() {
		bl.gotoBlock(after, at)
	}
	bl.origin, bl.deferID = saveO, saveD
	bl.cur = after
}

// exitTo runs the exit chain from the innermost scope out to scopes[depth].
func (bl *builder) exitTo(depth int, kind exitKind, at source.Span) {
	bl.exitScopes(depth, kind, at)
}

// successExit returns val (nil for void) as the success value: the ensures
// checks, _0, the exit chain, Return.
func (bl *builder) successExit(val *mir.Operand, at source.Span, exit mir.ExitKind) {
	if val != nil {
		v := bl.narrowBack(*val, bl.sig.Result, at)
		val = &v
	}
	bl.ensuresChecks(val, at)
	if !bl.open() {
		return
	}
	ret := bl.b.Place(mir.ReturnLocal)
	switch {
	case bl.sig.Errors != types.EmptyErrors:
		var ops mir.Range
		if val != nil {
			ops = bl.b.AddOperands(*val)
		}
		bl.assign(ret, mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggOk, Ops: ops, Type: bl.b.Locals[0].Type}, at)
	case val != nil:
		bl.assign(ret, use(*val), at)
	}
	bl.exitTo(0, exitReturn, at)
	if bl.open() {
		bl.term(mir.Terminator{Kind: mir.TermReturn, Exit: exit}, at)
	}
}

// narrowBack retypes v to want, the declared result type, inside an if T
// is X branch: check types `return &self.f` there under X (`&i32`) while
// _0 keeps T, and records no conversion on the expression (no ConvNarrow),
// so lower adds the no-op CastNarrow itself, only when substituting every
// enclosing branch's T by its X turns want into v's type. Any other
// mismatch is left for Validate's type check to report.
func (bl *builder) narrowBack(v mir.Operand, want types.TypeID, at source.Span) mir.Operand {
	got := bl.b.OperandType(v)
	if got == want || bl.narrowedType(want) != got {
		return v
	}
	l := bl.newTemp(want, at)
	bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: v, Type: want}, at)
	return bl.valueOfTemp(l, at)
}

// narrowedType is t with the T of every enclosing if T is X branch
// replaced by its X, outermost first: an outer X may name an inner
// branch's parameter (`if T is U { if U is u32 {`), an inner X is already
// read under the outer narrowing.
func (bl *builder) narrowedType(t types.TypeID) types.TypeID {
	for i := 0; i < len(bl.scopes); i++ {
		if sc := bl.scopes[i]; sc.kind == scopeTypeTestYes {
			t = bl.substParam(t, sc.ttParam, sc.ttType)
		}
	}
	return t
}

// errorExit returns the error e (an ErrorSet value of set es): widened to
// the function's set, _0 = err(e), the exit chain, Return.
func (bl *builder) errorExit(e mir.Operand, es types.ErrorSetID, at source.Span, exit mir.ExitKind, newError bool) {
	bl.mutSelfInvariant(at)
	if !bl.open() {
		return
	}
	e = bl.widen(e, es, bl.sig.Errors, at)
	bl.assign(bl.b.Place(mir.ReturnLocal), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggErr, Ops: bl.b.AddOperands(e), Type: bl.b.Locals[0].Type}, at)
	kind := exitFail
	if exit == mir.ExitTry {
		kind = exitTry
	}
	bl.exitTo(0, kind, at)
	bl.traceCall(exit == mir.ExitFail && newError, at) // the error-return trace (trace.go)
	if bl.open() {
		bl.term(mir.Terminator{Kind: mir.TermReturn, Exit: exit, NewError: newError}, at)
	}
}

// widen casts an error-set value of set from to set to when they differ.
func (bl *builder) widen(e mir.Operand, from, to types.ErrorSetID, at source.Span) mir.Operand {
	if from == to {
		return e
	}
	tt := bl.tab.ErrorSetType(to)
	w := bl.newTemp(tt, at)
	bl.assign(bl.b.Place(w), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastWidenErrors, A: e, Type: tt}, at)
	return mir.Move(bl.b.Place(w))
}

// inElseHandler reports whether the innermost enclosing handler scope is an
// else handler of an E!T call or value (a return there is ExitElse).
func (bl *builder) inElseHandler() bool {
	for i := len(bl.scopes) - 1; i >= 0; i-- {
		switch bl.scopes[i].kind {
		case scopeElseHandler:
			return true
		case scopeFn:
			return false
		}
	}
	return false
}

// jump is break or continue: the chain to the loop, the
// header temporaries, then a goto with ElseExit when the edge leaves an
// else handler.
func (bl *builder) jump(label *ast.Ident, isBreak bool, at source.Span) {
	li := len(bl.loops) - 1
	if label != nil {
		for li >= 0 && bl.loops[li].label != label.Name {
			li--
		}
	}
	if li < 0 {
		source.ICEf(at, "lower: break or continue outside a loop")
	}
	lc := &bl.loops[li]
	elseExit := bl.leavesElse(lc.depth)
	bl.exitScopes(lc.depth, exitBreak, at)
	if !bl.open() {
		return
	}
	for k := len(lc.hdr) - 1; k >= 0; k-- {
		bl.scopeEnd(lc.hdr[k], at)
	}
	target := lc.cont
	if isBreak {
		target = lc.exit
		lc.broken = true
	} else {
		lc.continued = true
	}
	bl.term(mir.Terminator{Kind: mir.TermGoto, Target: target, ElseExit: elseExit}, at)
}
