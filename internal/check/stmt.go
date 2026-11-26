// Why: every body is a list of statements; their typing and whether control
// can reach a point (a missing return, dead code after a diverging call) are
// checked in one walk.
//
// What: run (the unit walk: a function body, a test, an initializer, a fn
// literal, a contract or an invariant), fnBody (E0805), stmt and its cases
// (bindings and destructuring, assign, ifStmt, forStmt and inHead,
// returnStmt, jump, cleanupExit for defer and onerr, dropped values with
// E0209 and E0802), and foldCond.
//
// How: each statement reports whether it diverges, which gives
// Body.Diverges and E0805 for a result whose body can reach its end. After
// the body, the unit judges what it recorded along the way: const fn uses,
// capture uses and contract calls.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// loopCtx is one enclosing for loop: its node and whether a break targets
// it (a `for { }` with none diverges).
type loopCtx struct {
	node   ast.NodeID
	broken bool
}

// run checks the unit's root: a function's body, a test, a const or global
// initializer, a fn literal, a contract predicate or an invariant.
func (u *unit) run() {
	defer u.closeLiteralRecords() // before any literal unit reads them
	u.deferAt = -1
	switch n := u.b.Node.(type) {
	case *ast.FuncDecl:
		if n.Naked {
			u.nakedBody(n.Body) // E1105
		}
		u.fnBody(n.Body)
		u.judgeConstFnUses()
	case *ast.ExternDecl:
		if n.Naked {
			u.nakedBody(n.Body) // E1105
		}
		u.fnBody(n.Body)
	case *ast.TestItem:
		u.testParam(n)
		if n.Body != nil {
			u.block(n.Body)
		}
	case *ast.ConstDecl:
		if d, ok := u.it.Decl.(*types.ConstDecl); ok {
			u.initializer(n.Value, d.Type)
		}
	case *ast.GlobalDecl:
		if d, ok := u.it.Decl.(*types.GlobalDecl); ok && n.Value != nil {
			u.initializer(n.Value, d.Type)
		}
	case *ast.FnLit:
		u.fnBody(n.Body)
		u.judgeCaptureUses()
	case *ast.Contract:
		u.inContract = true
		u.expr(n.X, types.Bool)
		u.judgeContractConstFn()
	case *ast.InvariantMember:
		u.inContract = true
		u.expr(n.X, types.Bool)
	case *ast.ErrorDecl:
		u.errorFormatPlan(n) // a provided Format method
	}
}

// testParam types a test's parameter: `sys: &System`, any other
// type E0301 at the type with `expected: &System`; the local keeps the type
// written, so the body is checked against it.
func (u *unit) testParam(n *ast.TestItem) {
	if n.Param == nil || len(u.b.Locals) < 2 {
		return
	}
	sys := types.Poison
	if u.c.known.System != types.NoItem {
		sys = u.tab.Ref(u.tab.Named(u.c.known.System, nil))
	}
	t := sys
	if n.Param.Type != nil {
		t = u.x.typeOf(n.Param.Type, posPlain)
		u.setType(n.Param.Type, t)
		if t != types.Poison && sys != types.Poison && t != sys {
			u.err(diag.E0301, n.Param.Type.Span(), "a test takes `sys: &System`, found `"+u.str(t)+"`").
				Text("expected", "&System").Text("found", u.str(t))
		}
	}
	u.b.Locals[1].Type = t
}

// fnBody checks a function's, method's or fn literal's body, then E0805:
// a result other than void whose body can reach its end, at the
// closing brace, with the proven fix `return e` when the last statement
// is an expression of the result type.
func (u *unit) fnBody(b *ast.Block) {
	if b == nil {
		return
	}
	if u.block(b) || u.sig == nil || u.result == types.Void || u.result == types.Poison || sigHasPoison(u.sig) {
		return
	}
	sp := b.Span()
	end := source.Span{File: sp.File, Start: sp.End - 1, End: sp.End}
	if sp.End == sp.Start {
		end = sp
	}
	d := u.err(diag.E0805, end, "the body can reach its end without returning `"+u.str(u.result)+"`").Text("result", u.str(u.result))
	if n := len(b.Stmts); n > 0 {
		if es, ok := b.Stmts[n-1].(*ast.ExprStmt); ok && u.b.TypeOf(es.X.ID()) == u.result {
			at := es.X.Span()
			d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: source.Span{File: at.File, Start: at.Start, End: at.Start}, Text: "return "}},
				Why: "the last expression has the result type"})
		} else {
			u.lastValueFix(d, b.Stmts[n-1])
		}
	}
}

// initializer types a const's or global's value: against its written type,
// or on its own with literals defaulted.
func (u *unit) initializer(e ast.Expr, t types.TypeID) types.TypeID {
	if t == types.NoType {
		return u.expr(e, types.NoType)
	}
	u.expr(e, t)
	return t
}

// block checks a block's statements and reports whether it diverges (some
// statement diverges; the ones after it are dead and still checked).
func (u *unit) block(b *ast.Block) bool {
	if b == nil {
		return false
	}
	div := false
	for _, s := range b.Stmts {
		if s != nil && u.stmt(s) {
			div = true
		}
	}
	if div {
		u.b.diverge(b.ID())
	}
	return div
}

// stmt checks one statement and reports whether control never continues
// after it (recorded in Body.Diverges).
func (u *unit) stmt(s ast.Stmt) bool {
	div := u.stmtInner(s)
	if div {
		u.b.diverge(s.ID())
	}
	return div
}

func (u *unit) stmtInner(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BindStmt:
		t := types.NoType
		if s.Type != nil {
			n := len(u.b.PoisonAt)
			t = u.nt(u.x.typeOf(s.Type, posPlain))
			u.setType(s.Type, t)
			if len(u.b.PoisonAt) > n {
				// A poisoned const read in the declared type (`var a: [K]int`):
				// lower's E0000 on the binding is that read's.
				u.b.PoisonAt = append(u.b.PoisonAt, s.Span())
			}
		}
		if s.Value != nil {
			if vt := u.expr(s.Value, t); t == types.NoType {
				t = vt
				if u.tab.Kind(vt) == types.KErrorUnion {
					u.unhandled(s.Value, vt) // an E!T cannot be bound
					t = types.Poison
				} else if u.scopeEscapes(s.Value, vt) {
					t = types.Poison
				}
			}
		}
		if t == types.NoType {
			t = types.Poison
		}
		u.bindName(s.Name, t)
		return u.never(s.Value)
	case *ast.DestructureStmt:
		u.destructure(s)
		return u.never(s.Value)
	case *ast.ReturnStmt:
		u.returnStmt(s)
		return true
	case *ast.FailStmt:
		u.failStmt(s)
		return true
	case *ast.BecomeStmt:
		u.becomeStmt(s)
		return true
	case *ast.BreakStmt:
		u.jump(s.Span(), s.Label, true)
		return true
	case *ast.ContinueStmt:
		u.jump(s.Span(), s.Label, false)
		return true
	case *ast.ExprStmt:
		u.dropAt = s.X
		t := u.expr(s.X, types.NoType)
		u.dropAt = nil
		u.dropped(s, s.X, t, false)
		return t == types.Never
	case *ast.DiscardStmt:
		u.dropAt, u.discardAt = s.Value, s.Value
		t := u.expr(s.Value, types.NoType)
		u.dropAt, u.discardAt = nil, nil
		u.dropped(s, s.Value, t, true)
		return t == types.Never
	case *ast.AssignStmt:
		u.assign(s)
		return u.never(s.Value)
	case *ast.IfStmt:
		return u.ifStmt(s)
	case *ast.ForStmt:
		return u.forStmt(s)
	case *ast.MatchStmt:
		return u.matchStmt(s)
	case *ast.UnsafeStmt:
		u.unsafeDepth++
		div := u.block(s.Body)
		u.unsafeDepth--
		return div
	case *ast.AsmStmt:
		return u.asmStmt(s)
	case *ast.DeferStmt:
		saved := u.deferAt
		u.deferAt = len(u.loops)
		if s.Body != nil {
			u.block(s.Body)
		} else if s.X != nil {
			u.dropAt = s.X
			t := u.expr(s.X, types.NoType)
			u.dropAt = nil
			u.dropped(s, s.X, t, false)
		}
		u.deferAt = saved
		if s.Onerr {
			u.onerrValues(s)
		}
	case *ast.ScopeStmt:
		return u.scopeStmt(s)

	}
	return false
}

// never reports whether e has type never (a Panic or never call), which
// makes the enclosing statement diverge.
func (u *unit) never(e ast.Expr) bool {
	return e != nil && u.b.TypeOf(e.ID()) == types.Never
}

// inDefer reports whether the statement being checked is inside a defer or
// onerr body (fn literals are their own units).
func (u *unit) inDefer() bool { return u.deferAt >= 0 }

// cleanupExit is E0806: control leaving a defer or onerr body, at
// the token.
func (u *unit) cleanupExit(at source.Span, tok string) {
	at.End = min(at.End, at.Start+uint32(len(tok)))
	u.err(diag.E0806, at, "`"+tok+"` cannot leave a `defer` or `onerr` body").Text("token", tok)
}

// jump records a break's target and reports one that leaves a cleanup body.
func (u *unit) jump(at source.Span, label *ast.Ident, isBreak bool) {
	target := len(u.loops) - 1
	if label != nil {
		target = -1
		if r := u.ref(label); r.Kind == resolve.RefLabel {
			for i := len(u.loops) - 1; i >= 0; i-- {
				if u.loops[i].node == ast.NodeID(r.A) {
					target = i
					break
				}
			}
		}
	}
	tok := "continue"
	if isBreak {
		tok = "break"
	}
	if u.inDefer() && target < u.deferAt {
		u.cleanupExit(at, tok)
	}
	if isBreak && target >= 0 {
		u.loops[target].broken = true
	}
}

// returnStmt checks `return e` against the result; `return e` in a void
// or E!void function and a bare return elsewhere are E0301 with fact
// result; `return e` with `e: E!T` is E0802 (in expr).
func (u *unit) returnStmt(s *ast.ReturnStmt) {
	if u.inDefer() {
		u.cleanupExit(s.Span(), "return")
	}
	if u.sig == nil {
		if s.Value != nil {
			u.expr(s.Value, types.NoType)
		}
		return
	}
	res := u.nt(u.result)
	switch {
	case s.Value != nil && res == types.Void:
		t := u.expr(s.Value, types.NoType)
		if t != types.Poison && t != types.Never {
			u.err(diag.E0301, s.Value.Span(), "the function returns nothing").Text("expected", "void").Text("found", u.str(t)).Text("result", "void")
		}
	case s.Value == nil && res != types.Void && res != types.Poison:
		u.err(diag.E0301, s.Span(), "`return` needs a `"+u.str(res)+"` value").Text("expected", u.str(res)).Text("found", "void").Text("result", u.str(res))
	case s.Value != nil:
		saved := u.returnAt
		u.returnAt = s.Value
		t := u.expr(s.Value, res)
		u.returnAt = saved
		u.scopeEscapes(s.Value, t)
	}
}

// dropped checks an expression statement (or `_ = e`, discard) whose
// value is not void or never. E!T is E0802; a linear value is borrow's
// E0402; anything else E0209 with the proven fix `_ =` (a discard is
// legal).
func (u *unit) dropped(s ast.Stmt, e ast.Expr, t types.TypeID, discard bool) {
	if t == types.Void || t == types.Never || t == types.Poison || t == types.NoType {
		return
	}
	if u.tab.Kind(t) == types.KErrorUnion {
		u.unhandled(e, t)
		return
	}

	if discard {
		return
	}
	if _, ok := e.(*ast.ElseExpr); ok && t == types.Void {
		return
	}
	if !u.c.closed(t) || u.c.FactsIn(u.env, t).Kind == types.Linear {
		return // a linear value is borrow's E0402; a generic one may be
	}
	if u.isScope(t) {
		return // a dropped Scope is E1003 at the call, one diagnostic
	}
	sp := s.Span()
	u.err(diag.E0209, sp, "the value of type `"+u.str(t)+"` is dropped").Text("type", u.str(t)).
		Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: source.Span{File: sp.File, Start: sp.Start, End: sp.Start}, Text: "_ = "}},
			Why: "a discarded value is written `_ =`"})
}

// assign is `p = e` and `p op= e`: e against the place's type; the
// target must be a place (E0301 otherwise); a compound operator needs a
// numeric place (E0308). An extern var, also another module's (`m.Var`,
// no place either), is E0404 alone, which says how to write it.
func (u *unit) assign(s *ast.AssignStmt) {
	// A plain store into a C struct's function-pointer field reads
	// nothing out of C (cfnptr.go).
	if fe, ok := s.Target.(*ast.FieldExpr); ok && s.Op == ast.TokAssign {
		u.cFnStore = fe.ID()
	}
	t := u.operand(s.Target, types.NoType)
	u.cFnStore = 0
	if _, extern := u.externVarPlace(s.Target); t != types.Poison && !extern && !u.isPlace(s.Target) {
		u.err(diag.E0301, s.Target.Span(), "only a local, a field, an element or a dereference can be assigned").
			Text("reason", "not a place").Text("expected", "a place").Text("found", "an expression")
	}
	u.externVarWrite(s.Target, "write") // an extern var is written through a pointer
	if s.Op == ast.TokAssign {
		u.expr(s.Value, t)
		return
	}
	if v, ok := u.vecOf(t); ok {
		u.vecAssign(s, v) // a vector compound assignment follows its operator
		return
	}
	// A compound operator obeys its operator's rule: `+= -= *= /= %=` on
	// integers and floats, the wrapping, bitwise and shift forms on integers
	// only; a shift amount is any integer type.
	intOnly, shift := compoundKind(s.Op)
	if t != types.Poison && !(u.isNum(t) && (!intOnly || u.isInt(t))) {
		u.expr(s.Value, types.NoType)
		u.noOperator(s.Span(), s.Op, t)
		return
	}
	if shift {
		if y := u.operand(s.Value, types.NoType); t != types.Poison && y != types.Poison && !u.isInt(y) {
			u.noOperator(s.Span(), s.Op, y)
		}
		return
	}
	u.expr(s.Value, numericWant(u, t))
}

// compoundKind says whether a compound assignment's operator is defined on
// integers only, and whether it is a shift.
func compoundKind(op ast.TokenKind) (intOnly, shift bool) {
	switch op {
	case ast.TokPlusWrapEq, ast.TokMinusWrapEq, ast.TokStarWrapEq, ast.TokAmpEq, ast.TokPipeEq, ast.TokCaretEq:
		return true, false
	case ast.TokShlEq, ast.TokShrEq:
		return true, true
	}
	return false, false
}

// isPlace reports whether e, already typed, is a place: a local
// or a global (Use, not syntax: a const, a function, a universe name such
// as `true` and an item named through a module or type are not), a
// field, an element or a dereference (parentheses allowed).
func (u *unit) isPlace(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		switch u.b.use(e.ID()).Kind {
		case UseLocal, UseGlobal, UseField:
			return true
		}
		return false
	case *ast.FieldExpr:
		return u.b.use(e.ID()).Kind == UseField
	case *ast.IndexExpr:
		return u.b.use(e.ID()).Kind == UseNone // an element; `F[int]` is UseItem
	case *ast.ParenExpr:
		return u.isPlace(e.X)
	case *ast.UnaryExpr:
		return e.Op == ast.TokStar
	}
	return false
}

// ifStmt checks an if: a bool condition or a binding condition on ?T; it
// diverges when both branches do.
func (u *unit) ifStmt(s *ast.IfStmt) bool { return u.ifStmtV(s, nil) }

// ifStmtV checks an if as a statement (v nil) or as a value construct whose
// arms go through valueArm; an else if shares v.
func (u *unit) ifStmtV(s *ast.IfStmt, v *valueCtx) bool {
	if v != nil && s.Else == nil {
		u.valueIfWithoutElse(s, v) // E0311
	}
	switch c := s.Cond.(type) {
	case *ast.IsCond:
		return u.isBranch(s, c, v)

	case *ast.BindCond:
		u.bindCond(c)
	case ast.Expr:
		before := len(u.b.Diags)
		if u.expr(c, types.Bool) == types.Bool {
			u.foldCond(c)
		}
		u.maskCond(c, before) // a Mask condition is E0301
	}
	then := u.armBlock(s.Then, v)
	els := u.elseArm(s, v) // always checked: no short circuit
	return then && els
}

// elseArm checks an if's else part, statement or value.
func (u *unit) elseArm(s *ast.IfStmt, v *valueCtx) bool {
	switch e := s.Else.(type) {
	case *ast.IfStmt:
		if v == nil {
			return u.stmt(e)
		}
		div := u.ifStmtV(e, v)
		if div {
			u.b.diverge(e.ID())
		}
		return div
	case *ast.Block:
		return u.armBlock(e, v)
	}
	return false
}

// bindCond is `x := e`, `x := &e`, `x := mut e` in an if or for head: e must
// be ?T (E0307 otherwise, E!T included); x is T, &T or mut T.
func (u *unit) bindCond(c *ast.BindCond) {
	var mode ast.TokenKind
	v := c.Value
	if un, ok := v.(*ast.UnaryExpr); ok && (un.Op == ast.TokAmp || un.Op == ast.TokMut) {
		mode, v = un.Op, un.X
	}
	t := u.expr(v, types.NoType)
	if v != c.Value {
		u.setType(c.Value, types.Poison)
	}
	elem := types.Poison
	switch {
	case t == types.Poison:
	case u.tab.Kind(t) == types.KOptional:
		elem = u.tab.Get(t).Elem
		switch mode {
		case ast.TokAmp:
			elem = u.tab.Ref(elem)
		case ast.TokMut:
			elem = u.tab.Mut(elem)
		}
	default:
		u.err(diag.E0307, v.Span(), "a binding condition needs an optional, found `"+u.str(t)+"`").Text("found", u.str(t))
	}
	u.bindName(c.Name, elem)
}

// forStmt checks a for loop: ranges, slices and arrays, conditions,
// binding conditions; `for { }` with no break targeting it diverges.
func (u *unit) forStmt(s *ast.ForStmt) bool {
	forever := false
	switch h := s.Head.(type) {
	case nil:
		forever = true
	case *ast.InHead:
		if fs, ok := h.Source.(*ast.FieldsSource); ok {
			u.fieldsLoop(s, h, fs)
			return false
		}
		u.inHead(h)
	case *ast.BindCond:
		u.bindCond(h)
	case *ast.IsCond:
		return false
	case ast.Expr:
		before := len(u.b.Diags)
		u.expr(h, types.Bool)
		u.maskCond(h, before) // a Mask condition is E0301
	}
	u.loops = append(u.loops, loopCtx{node: s.ID()})
	u.block(s.Body)
	l := u.loops[len(u.loops)-1]
	u.loops = u.loops[:len(u.loops)-1]
	return forever && !l.broken
}

// inHead types `for i in a..b`, `for x in xs`, `for i, x in xs`.
func (u *unit) inHead(h *ast.InHead) {
	switch src := h.Source.(type) {
	case *ast.RangeSource:
		var t types.TypeID
		if isUntyped(src.Lo) && !isUntyped(src.Hi) {
			t = u.expr(src.Hi, types.NoType)
			u.expr(src.Lo, t)
		} else {
			t = u.expr(src.Lo, types.NoType)
			u.expr(src.Hi, t)
		}
		if t != types.Poison && !u.isInt(t) {
			u.err(diag.E0301, src.Lo.Span(), "a range is over an integer type, found `"+u.str(t)+"`").Text("expected", "an integer type").Text("found", u.str(t))
			t = types.Poison
		}
		u.bindName(h.Key, t)
		u.bindName(h.Value, types.Poison)
	case *ast.RefSource:
		xt := u.expr(src.X, types.NoType)
		if t, ok := u.iterHead(h, src, xt); ok {
			u.loopVar(h, t, h.Value != nil)
			return
		}
		elem := u.loopElem(src.X, xt)
		if elem != types.Poison {
			switch src.Mode {
			case ast.ByRef:
				elem = u.tab.Ref(elem)
			case ast.ByMutRef:
				elem = u.tab.Mut(elem)
			}
		}
		u.loopVar(h, elem, h.Value != nil)
	}
}

// loopElem is the element type a for loop reads from xs: []T, mut []T,
// [N]T (through references); a str or a Map is E0301 with its alternatives;
// a Slice[T] implementer is Poison until its items are looked up.
func (u *unit) loopElem(x ast.Expr, xt types.TypeID) types.TypeID {
	if xt == types.Poison {
		return types.Poison
	}
	v, n := u.deref(xt)
	vt := u.tab.Get(v)
	switch vt.Kind {
	case types.KSlice, types.KMutSlice, types.KArray:
		if n > 0 {
			*u.b.adjust(x.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n)}
		}
		return vt.Elem
	case types.KStr:
		u.err(diag.E0301, x.Span(), "a `str` is not a sequence of anything; choose one").Text("expected", "a slice or array").Text("found", "str").
			Alt(".Scalars()").Alt(".AsBytes()").Alt(".Lines()")
	case types.KNamed:
		if elem, ok := u.sliceElemOf(v); ok {
			if n > 0 {
				*u.b.adjust(x.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n)}
			}
			return elem
		}
		if it := u.c.cfg.Items.Get(vt.Item); u.c.cfg.Items.Module(it.Module).Std && u.c.cfg.Interner.Str(it.Name) == "Map" {
			u.err(diag.E0301, x.Span(), "a `Map` is iterated through `.Iter()`").Text("expected", "a slice or array").Text("found", u.str(v)).Alt(".Iter()")
			return types.Poison
		}
		u.notSequence(x, v)
	case types.KParam:
		// Iterating is not an operation a bound gives.
		u.needsBound(x.Span(), v, "a for loop over `"+u.str(v)+"`")
	case types.KPoison:
	default:
		u.notSequence(x, v)
	}
	return types.Poison
}

// notSequence is E0301 at a for loop's source that is not a slice, an
// array or a Slice[T] implementer.
func (u *unit) notSequence(x ast.Expr, t types.TypeID) {
	u.err(diag.E0301, x.Span(), "a for loop is over a slice, an array, a `Slice[T]` implementer or an iterator (`Next(mut self): ?T`), found `"+u.str(t)+"`").
		Text("expected", "a slice, an array, a `Slice[T]` implementer or an iterator (`Next(mut self): ?T`)").Text("found", u.str(t))
}

// bindName gives the local declared by name its type and records the
// binding node.
func (u *unit) bindName(name *ast.Ident, t types.TypeID) {
	if name == nil {
		return
	}
	id := u.declLocal[name.ID()]
	if id == 0 || int(id) >= len(u.b.Locals) {
		return
	}
	u.b.Locals[id].Type = t
	u.b.Locals[id].Span = name.Span()
	u.b.Bind[u.b.slot(name.ID())] = id
}

// foldCond: an if condition that is a constant with no call (bool
// literals, named consts with a value or a foldable initializer, operators
// over them) gets its value in Body.Consts, so lower emits only the taken
// branch. Both branches are still checked. A same-module const that does
// not fold leaves the condition unfolded.
func (u *unit) foldCond(c ast.Expr) {
	f := &folder{m: u.m, fx: u.fx, rec: u.x.rec, write: u.m.d != nil}
	if v, ok, _ := f.fold(c); ok && v.isBool {
		if id, ok := u.c.internFolded(v, types.Bool); ok {
			u.b.setConst(c.ID(), id)
		}
	}
}
