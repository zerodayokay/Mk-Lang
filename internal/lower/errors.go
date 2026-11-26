// Why: Mk's error handling (try, else on calls, on E!T values and on
// optionals, fail, return, become) is control flow with several exit kinds,
// and lowering it right decides which cleanups run on each edge.
//
// What: tryExpr, tryUnion (try on a stored E!T value), elseExpr, elseUnion,
// okTemp and elseJoin for try and else; returnStmt, failStmt and
// becomeStmt; returnKind and isCall.
//
// How: a fallible call gets a success edge and an error edge; try widens the
// error and runs the exit chain with exit kind try, and else enters a handler
// scope whose return is ExitElse. A handler that yields a value assigns a
// temporary made before the call, and both edges join with it read once.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// fallibleCall is e as the fallible call a try, else, match or Must handles.
func (bl *builder) fallibleCall(e ast.Expr) (*ast.CallExpr, Call, bool) {
	x, ok := unparen(e).(*ast.CallExpr)
	if !ok {
		return nil, Call{}, false
	}
	c := bl.in.Call(x.ID())
	if c.Errors == types.EmptyErrors || (c.Kind != CallFunc && c.Kind != CallIface && c.Kind != CallValue) {
		return nil, Call{}, false
	}
	return x, c, true
}

// tryExpr is try F(args): the error edge widens, runs the exit chain
// with exit kind try, and returns ExitTry.
func (bl *builder) tryExpr(x *ast.UnaryExpr) mir.Rvalue {
	call, c, ok := bl.fallibleCall(x.X)
	if !ok {
		if bl.tab.Kind(bl.typeOf(x.X)) == types.KErrorUnion {
			return bl.tryUnion(x)
		}
		// a Poison operand (try Arena) is check's gap
		source.ICEf(x.Span(), "lower: try of something that is not a fallible call")
	}
	e := bl.newTemp(bl.tab.ErrorSetType(c.Errors), x.Span())
	tb := bl.newBlock()
	dest := bl.emitCall(call, c, &errEdge{kind: mir.EdgeTry, block: tb, err: e}, mir.NoLocal)
	succ := bl.cur
	bl.cur = tb
	bl.errorExit(bl.valueOfTemp(e, x.Span()), c.Errors, x.Span(), mir.ExitTry, false)
	bl.cur = succ
	if !bl.open() {
		return use(mir.Imm(types.Never, 0))
	}
	return use(bl.valueOf(dest, true, x.Span()))
}

// tryUnion is try e on an E!T value that is no call (a local, a parameter,
// `var r: E!T = F()` kept and tried later): the value is taken once into a
// temporary, as elseUnion takes it; an error leaves through the try exit
// chain with exit kind try (onerr runs, the trace gets the try's site), as
// a call's try edge takes the call's error, and ok continues with the
// payload.
func (bl *builder) tryUnion(x *ast.UnaryExpr) mir.Rvalue {
	at := x.Span()
	p := bl.optTaken(x.X)
	set := bl.tab.Get(bl.b.PlaceType(p)).Errors
	ok, tb := bl.newBlock(), bl.newBlock()
	bl.unionSwitch(p, ok, tb, at)
	bl.cur = tb
	e := bl.newTemp(bl.tab.ErrorSetType(set), at)
	bl.assign(bl.b.Place(e), use(bl.valueOf(bl.errPayload(p), true, at)), at)
	bl.errorExit(bl.valueOfTemp(e, at), set, at, mir.ExitTry, false)
	bl.cur = ok
	if t := bl.in.Type(x.ID()); t == types.Void || t == types.NoType {
		return bl.voidValue(at)
	}
	ok, value := bl.okTemp(p, ok, func() mir.Rvalue { return use(bl.valueOf(bl.okPayload(p), true, at)) }, at)
	bl.cur = ok
	return value()
}

// elseExpr is e else { H } and e else x { H }.
func (bl *builder) elseExpr(x *ast.ElseExpr) mir.Rvalue {
	// A handler that yields a value assigns r, a temporary of the
	// enclosing frame made before the call; the success edge assigns the
	// payload.
	r := mir.NoLocal
	if bl.in.IsValue(x.ID()) {
		r = bl.newTemp(bl.in.Type(x.ID()), x.Span())
		if bl.armDest == nil {
			bl.armDest = map[*ast.Block]mir.Place{}
		}
		bl.armDest[x.Handler] = bl.b.Place(r)
		defer delete(bl.armDest, x.Handler)
	}
	if call, c, ok := bl.fallibleCall(x.X); ok {
		et := bl.tab.ErrorSetType(c.Errors)
		var e mir.LocalID
		if x.Name != nil {
			e = bl.b.AddLocal(mir.Local{Type: et, Kind: mir.LocalUser, Name: x.Name.Name, Loop: mir.NoLoop, Span: bl.span(x.Name.Span())})
			bl.bindLocal(x.Name, e)
		} else {
			e = bl.newTemp(et, x.Span())
		}
		hb := bl.newBlock()
		dest := bl.emitCall(call, c, &errEdge{kind: mir.EdgeElse, block: hb, err: e}, mir.NoLocal)
		succ := bl.cur
		bl.cur = hb
		bl.openScope(scopeElseHandler)
		if x.Name != nil {
			s := &bl.scopes[len(bl.scopes)-1]
			s.locals = append(s.locals, e)
		}
		bl.armStmts(x.Handler)
		bl.closeScope(closeBrace(x.Handler))
		value := func() mir.Rvalue { return use(bl.valueOf(dest, true, x.Span())) }
		if r != mir.NoLocal {
			return bl.elseValueJoin(x, succ, r, value)
		}
		return bl.elseJoin(x, succ, value)
	}
	if bl.tab.Kind(bl.in.Type(x.X.ID())) == types.KErrorUnion {
		return bl.elseUnion(x, r)
	}
	// e a ?T value: none goes to the handler, which is not an error handler.
	p := bl.optTaken(x.X)
	d := bl.discr(p, x.Span())
	ok, hb := bl.newBlock(), bl.newBlock()
	bl.switchOn(d, []mir.SwitchCase{{Value: 1, Target: ok}}, hb, x.Span())
	bl.cur = hb
	bl.openScope(scopeOptHandler)
	bl.armStmts(x.Handler)
	bl.closeScope(closeBrace(x.Handler))
	value := func() mir.Rvalue { return use(bl.valueOf(bl.somePayload(p), true, x.Span())) }
	if r != mir.NoLocal {
		return bl.elseValueJoin(x, ok, r, value)
	}
	if bl.dropped == ast.Expr(x) {
		ok, value = bl.okTemp(p, ok, value, x.X.Span())
	}
	return bl.elseJoin(x, ok, value)
}

// elseUnion is e else { H } and e else x { H } on an E!T value that is no
// call (a parameter, a local): the value is taken once into a
// temporary, as optTaken takes a ?T (moved when it is not plain, as a call
// result is consumed), and its discriminant picks the edges as matchUnion's
// does: ok to the success edge, which gives the SelOk payload, anything else
// to the handler, an else handler of an error (scopeElseHandler) in
// which x is a user local holding the SelErr error, as the call path's
// error edge binds it. The ?T path would be wrong here: an ICE with x, a
// switch on the optional's some value (the handler ran for ok) without it.
func (bl *builder) elseUnion(x *ast.ElseExpr, r mir.LocalID) mir.Rvalue {
	at := x.Span()
	p := bl.optTaken(x.X)
	ok, hb := bl.newBlock(), bl.newBlock()
	bl.unionSwitch(p, ok, hb, at)
	bl.cur = hb
	bl.openScope(scopeElseHandler)
	if x.Name != nil {
		e := bl.b.AddLocal(mir.Local{Type: bl.tab.ErrorSetType(bl.tab.Get(bl.b.PlaceType(p)).Errors), Kind: mir.LocalUser, Name: x.Name.Name, Loop: mir.NoLoop, Span: bl.span(x.Name.Span())})
		bl.bindLocal(x.Name, e)
		s := &bl.scopes[len(bl.scopes)-1]
		s.locals = append(s.locals, e)
		bl.assign(bl.b.Place(e), use(bl.valueOf(bl.errPayload(p), true, x.Name.Span())), x.Name.Span())
	}
	bl.armStmts(x.Handler)
	bl.closeScope(closeBrace(x.Handler))
	value := func() mir.Rvalue { return use(bl.valueOf(bl.okPayload(p), true, at)) }
	if r != mir.NoLocal {
		return bl.elseValueJoin(x, ok, r, value)
	}
	ok, value = bl.okTemp(p, ok, value, x.X.Span())
	return bl.elseJoin(x, ok, value)
}

// okTemp is the success edge of the statement form, whose value may be
// dropped (`_ = r else { }`, `r else { return }` on an E!Res): a
// payload that is not plain moves out of the E!T (or ?T) temporary into a T
// temporary of its own, as a call's success edge leaves it in the call's T
// destination, and the value is read from there. A payload left unconsumed
// is then a T for borrow's E0402, with that type's release facts, not the
// E!T. A plain payload, and the value form (elseValueJoin assigns r on the
// success edge), keep the projection.
func (bl *builder) okTemp(p mir.Place, ok mir.BlockID, value func() mir.Rvalue, at source.Span) (mir.BlockID, func() mir.Rvalue) {
	t := bl.tab.Get(bl.b.PlaceType(p)).Elem
	if bl.plain(t, at) {
		return ok, value
	}
	end := bl.cur
	bl.cur = ok
	d := bl.newTemp(t, at)
	bl.assign(bl.b.Place(d), value(), at)
	ok, bl.cur = bl.cur, end
	return ok, func() mir.Rvalue { return use(bl.valueOf(bl.b.Place(d), true, at)) }
}

// elseJoin continues at the success block; a handler that falls through
// (the statement form) joins it.
func (bl *builder) elseJoin(x *ast.ElseExpr, succ mir.BlockID, value func() mir.Rvalue) mir.Rvalue {
	if !bl.open() {
		bl.cur = succ
		if !bl.open() {
			return use(mir.Imm(types.Never, 0))
		}
		if t := bl.in.Type(x.ID()); t == types.Void || t == types.NoType {
			return bl.voidValue(x.Span())
		}
		return value()
	}
	join := bl.newBlock()
	bl.gotoBlock(join, x.Span())
	if succ != mir.NoBlock {
		bl.cur = succ
		bl.gotoBlock(join, x.Span())
	}
	bl.cur = join
	return bl.voidValue(x.Span())
}

// returnKind is ExitElse inside an else handler of an E!T call or value, else
// ExitNormal.
func (bl *builder) returnKind() mir.ExitKind {
	if bl.inElseHandler() {
		return mir.ExitElse
	}
	return mir.ExitNormal
}

// returnStmt is return [e]: the value first, then ensures, _0, the
// exit chain, Return.
func (bl *builder) returnStmt(s *ast.ReturnStmt) {
	if s.Value == nil {
		bl.successExit(nil, s.Span(), bl.returnKind())
		return
	}
	v := bl.operand(s.Value)
	if !bl.open() {
		return
	}
	if bl.sig.Result == types.Void {
		bl.successExit(nil, s.Span(), bl.returnKind())
		return
	}
	bl.successExit(&v, s.Span(), bl.returnKind())
}

// isCall reports whether t calls a function: a TermCall or a TermTailCall,
// whose callee and arguments read the same; a tail call's Dest is
// _0 and it has no successor.
func isCall(t *mir.Terminator) bool {
	return t.Kind == mir.TermCall || t.Kind == mir.TermTailCall
}

// becomeStmt is become f(args): the call's operands in call order,
// then TermTailCall, with no exit chain: check left nothing to run (no
// defer, onerr or scope, no ensures; borrow refuses a live linear value).
func (bl *builder) becomeStmt(s *ast.BecomeStmt) {
	c := bl.in.Call(s.Call.ID())
	if c.Kind != CallFunc && c.Kind != CallValue {
		source.ICEf(s.Span(), "lower: become of call kind %d (check's E0314)", c.Kind)
	}
	bl.tail = true
	bl.emitCall(s.Call, c, nil, mir.NoLocal)
	bl.tail = false
}

// failStmt is fail e: NewError when e is constructed here.
func (bl *builder) failStmt(s *ast.FailStmt) {
	t := bl.tab.Get(bl.typeOf(s.Value))
	if t.Kind != types.KErrorSet {
		source.ICEf(s.Span(), "lower: fail of a %v", t.Kind)
	}
	v := bl.operand(s.Value)
	if !bl.open() {
		return
	}
	newErr := false
	switch x := unparen(s.Value).(type) {
	case *ast.CallExpr:
		newErr = bl.in.Call(x.ID()).Kind == CallError
	case *ast.Ident, *ast.FieldExpr:
		newErr = bl.in.Use(x.ID()).Kind == UseError
	}
	bl.errorExit(v, t.Errors, s.Span(), mir.ExitFail, newErr)
}
