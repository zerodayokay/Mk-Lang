// Why: statements fix the order in which a program's effects happen, and
// MIR must keep that order exactly: the right side of an assignment before
// its place, each place evaluated once, temporaries ended per statement.
//
// What: blockStmts, block and stmt (the statement dispatch: bindings,
// discard, break, continue, defer, onerr, unsafe, blocks), assignStmt and
// placeRuns (assignment and compound assignment), trapSpan, and destructure
// with bindSub and destructTake.
//
// How: each statement runs in a temporary frame of its own. When the place
// of an assignment can run code before the store, a right side that reads a
// place is first read into a slot. A destructure moves the value into one
// whole temporary and takes each field out of it.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// blockStmts lowers a block's statements in the current scope; statements
// after a diverging one are not lowered.
func (bl *builder) blockStmts(b *ast.Block) {
	for _, s := range b.Stmts {
		if !bl.open() {
			return
		}
		bl.stmt(s)
	}
}

// block lowers a block statement in a scope of its own.
func (bl *builder) block(b *ast.Block, k scopeKind) {
	bl.openScope(k)
	bl.blockStmts(b)
	bl.closeScope(closeBrace(b))
}

func (bl *builder) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.UnsafeStmt:
		bl.block(s.Body, scopePlain)
		return
	case *ast.DeferStmt:
		bl.registerCleanup(s)
		return
	case *ast.BadStmt:
		source.ICEf(s.Span(), "lower: a statement that did not parse")
	}
	bl.pushFrame(s.Span())
	switch s := s.(type) {
	case *ast.BindStmt:
		if s.Value == nil {
			bl.declare(s.Name)
			break
		}
		if bl.isValueConstruct(s.Value) && bl.in.Type(s.Value.ID()) != types.Never {
			// A binding is its own local: x: T, then x = v in each
			// arm, which is the statement form exactly.
			l := bl.declare(s.Name)
			bl.valueInto(s.Value, bl.b.Place(l))
			break
		}
		rv := bl.exprConv(s.Value)
		l := bl.declare(s.Name)
		bl.assign(bl.b.Place(l), rv, bl.trapSpan(rv, s.Span(), s.Value))
	case *ast.DestructureStmt:
		bl.destructure(s)
	case *ast.DiscardStmt:
		if x, ok := unparen(s.Value).(*ast.ElseExpr); ok {
			// A handler that falls through leaves no value on its edge
			// (the dropped form): the success
			// value stays in the call's destination, as for a statement.
			bl.exprStmtIn(x)
			break
		}
		bl.tempOf(s.Value)
	case *ast.AssignStmt:
		bl.assignStmt(s)
	case *ast.ExprStmt:
		bl.exprStmtIn(s.X)
	case *ast.ReturnStmt:
		bl.returnStmt(s)
	case *ast.FailStmt:
		bl.failStmt(s)
	case *ast.BecomeStmt:
		bl.becomeStmt(s)
	case *ast.BreakStmt:
		bl.jump(s.Label, true, s.Span())
	case *ast.ContinueStmt:
		bl.jump(s.Label, false, s.Span())
	case *ast.IfStmt:
		bl.ifStmt(s)
	case *ast.ForStmt:
		bl.forStmt(s)
	case *ast.MatchStmt:
		bl.matchStmt(s)
	case *ast.ScopeStmt:
		bl.scopeStmt(s)
	case *ast.AsmStmt:
		bl.asmStmt(s)
	default:
		source.ICEf(s.Span(), "lower: statement %T not handled", s)
	}
	bl.popFrame()
}

// exprStmt lowers an expression used as a statement in a frame of its own
// (cleanup bodies written as one expression).
func (bl *builder) exprStmt(e ast.Expr) {
	bl.pushFrame(e.Span())
	bl.exprStmtIn(e)
	bl.popFrame()
}

func (bl *builder) exprStmtIn(e ast.Expr) {
	rv := bl.exprConv(e)
	if !bl.open() || rv.Kind == mir.RvUse || rv.Type == types.Void || rv.Type == types.Never {
		return
	}
	l := bl.newTemp(bl.rvType(rv, e), e.Span())
	bl.assign(bl.b.Place(l), rv, e.Span())
}

// placeRuns reports whether evaluating the place e can run code before the
// store (an index expression, a bounds check, an ItemsMut call, a call whose
// result is dereferenced): then a right side that reads a place is read into
// a slot first (the right side before the place). A local, a field or
// deref path of one, runs nothing.
func (bl *builder) placeRuns(e ast.Expr) bool {
	switch x := unparen(e).(type) {
	case *ast.Ident:
		return false
	case *ast.FieldExpr:
		if bl.in.Use(x.ID()).Kind == UseField {
			return bl.placeRuns(x.X)
		}
	case *ast.UnaryExpr:
		if x.Op == ast.TokStar && bl.isPlaceExpr(x.X) {
			return bl.placeRuns(x.X)
		}
	case *ast.IndexExpr:
		// A bounds check writes nothing; an inert index (a literal, a
		// local) runs no code; an implementer's ItemsMut is a call.
		if bl.in.Use(x.ID()).Kind != UseNone || len(x.Args) != 1 || !bl.isPlaceExpr(x.X) {
			return true
		}
		ie, ok := x.Args[0].(ast.Expr)
		if !ok || !bl.inert(ie) {
			return true
		}
		t := bl.in.Type(x.X.ID())
		for k := bl.tab.Kind(t); k == types.KRef || k == types.KMut; k = bl.tab.Kind(t) {
			t = bl.tab.Get(t).Elem
		}
		switch bl.tab.Kind(t) {
		case types.KSlice, types.KMutSlice, types.KArray, types.KStr:
			return bl.placeRuns(x.X)
		}
	}
	return true
}

// simplePlace reports whether e is a local or a field path of one with no
// evaluation of its own (the rvalue is then stored directly).
func (bl *builder) simplePlace(e ast.Expr) bool {
	switch x := unparen(e).(type) {
	case *ast.Ident:
		u := bl.in.Use(x.ID())
		return (u.Kind == UseLocal || u.Kind == UseParam) && bl.in.Adjust(x.ID()).Deref == 0
	case *ast.FieldExpr:
		return bl.in.Use(x.ID()).Kind == UseField && bl.in.Adjust(x.X.ID()).Deref == 0 && bl.simplePlace(x.X)
	}
	return false
}

// assignStmt is p = e and p op= e (the right side first, then the
// place once).
func (bl *builder) assignStmt(s *ast.AssignStmt) {
	if s.Op == ast.TokAssign {
		if bl.simplePlace(s.Target) {
			rv := bl.exprConv(s.Value)
			p := bl.place(s.Target)
			bl.assign(p, rv, bl.trapSpan(rv, s.Span(), s.Value))
			return
		}
		v := bl.operand(s.Value)
		if bl.readsLate(v) && (v.Place.Proj.Len == 0 || bl.placeRuns(s.Target)) {
			v = bl.slot(v, s.Value.Span())
		}
		p := bl.mutPlace(s.Target)
		bl.assign(p, use(v), s.Span())
		return
	}
	if c := bl.in.Call(s.ID()); c.Kind == CallFunc {
		// v op= w on a vector is v = Vec:Op[T, N](v, w), the right side
		// first, then the place, read and written once.
		v := bl.operand(s.Value)
		if bl.readsLate(v) && bl.placeRuns(s.Target) {
			v = bl.slot(v, s.Value.Span())
		}
		p := bl.mutPlace(s.Target)
		dest := bl.directCall(c.Item, c.Args, []mir.Operand{mir.Copy(p), v}, bl.b.PlaceType(p), s.Span())
		bl.assign(p, use(mir.Copy(dest)), s.Span())
		return
	}
	op, ok := compoundOps[s.Op]
	if !ok {
		source.ICEf(s.Span(), "lower: assignment operator %v", s.Op)
	}
	v := bl.operand(s.Value)
	if bl.readsLate(v) && bl.placeRuns(s.Target) {
		// The place has an evaluation of its own (xs[I()] += n): n is read
		// before it, in evaluation order.
		v = bl.slot(v, s.Value.Span())
	}
	p := bl.mutPlace(s.Target)
	bl.assign(p, mir.Rvalue{Kind: mir.RvBinary, Bin: op, A: mir.Copy(p), B: v, Type: bl.b.PlaceType(p)}, s.Span())
}

// trapSpan is the span of the statement that stores rv, the value of e,
// directly into a binding or a place: the statement's span, except that an
// rvalue with an implicit check (+ - * / % << >> and negation,
// CastChecked, a str RvSlice) carries e's span, the checked expression's,
// because codegen reports a trap at its statement's span and every other
// site of such an rvalue (a temporary of operand or tempOf) already stores
// it at e's span. So `b := Cast[u8](n)` panics at the Cast, not at b
// (tests/programs/panics/cast.mk).
func (bl *builder) trapSpan(rv mir.Rvalue, stmt source.Span, e ast.Expr) source.Span {
	switch rv.Kind {
	case mir.RvBinary:
		switch rv.Bin {
		case mir.Add, mir.Sub, mir.Mul, mir.Div, mir.Rem, mir.Shl, mir.Shr:
			return e.Span()
		}
	case mir.RvUnary:
		if rv.Un == mir.Neg {
			return e.Span()
		}
	case mir.RvCast:
		if rv.Cast == mir.CastChecked {
			return e.Span()
		}
	case mir.RvSlice:
		if bl.tab.Kind(bl.b.PlaceType(rv.Place)) == types.KStr {
			return e.Span()
		}
	}
	return stmt
}

// mutPlace is place(e) for a write: an implementer index calls ItemsMut.
func (bl *builder) mutPlace(e ast.Expr) mir.Place {
	save := bl.placeMut
	bl.placeMut = true
	p := bl.place(e)
	bl.placeMut = save
	return p
}

// destructure is (a, b) := e and Point(x, y) := p: e into one whole
// temporary, then each named field moved or copied out of it.
//
// A by-value destructure consumes the value, and borrow sees that only as
// a take, a Move out of the temporary's fields. Each nested linear struct
// whose fields are all plain takes its own field. When the temporary is
// not plain and nothing was taken (every bound leaf plain or `_`, as
// in `HeapText(ptr, used: _, cap, owner) := self` or `File(h: _, pos: _,
// nosh: _) := self`), the destructure ends with one take of a `_` leaf (plain,
// or droppable like a `mut []T`) or else a plain binding's leaf into a
// statement temporary, so the temporary is consumed and its scope end is
// not E0402. The user's bindings keep their Copy: a Move would give a
// reference-typed binding H(t) instead of the field's own loans.
func (bl *builder) destructure(s *ast.DestructureStmt) {
	var t mir.Place
	if bl.isPlaceExpr(s.Value) {
		p := bl.place(s.Value)
		l := bl.newTemp(bl.b.PlaceType(p), s.Value.Span())
		bl.assign(bl.b.Place(l), use(bl.valueOf(p, true, s.Value.Span())), s.Value.Span())
		t = bl.b.Place(l)
	} else {
		t = bl.tempOf(s.Value)
	}
	dt := &destructTake{}
	bl.destructureInto(s.Pat, t, s.Var, dt)
	if dt.took || bl.plain(bl.b.PlaceType(t), s.Value.Span()) {
		return
	}
	if !dt.has {
		// A fieldless root or a linear root whose nested fields are all
		// plain and fieldless has no bound leaf to take.
		if bl.takeWhole(t, s.Span()) {
			return
		}
		source.ICEf(s.Span(), "lower: a destructure of a non-plain value with no plain leaf to take")
	}
	h := bl.newTemp(bl.b.PlaceType(dt.leaf), dt.at)
	bl.assign(bl.b.Place(h), use(mir.Move(dt.leaf)), dt.at)
}

// destructTake records whether a destructure took a field out of its
// temporary, and the plain leaf to take when it did not: the first `_`
// leaf, else the first plain binding.
type destructTake struct {
	took     bool
	has      bool
	wildcard bool
	leaf     mir.Place
	at       source.Span
}

// note records the leaf at fp: a take, or a plain candidate.
func (dt *destructTake) note(op mir.Operand, fp mir.Place, wildcard bool, at source.Span) {
	if op.Kind == mir.OpMove {
		dt.took = true
		return
	}
	if !dt.has || wildcard && !dt.wildcard {
		dt.has, dt.wildcard, dt.leaf, dt.at = true, wildcard, fp, at
	}
}

func (bl *builder) destructureInto(rp *ast.RecordPat, t mir.Place, isVar bool, dt *destructTake) {
	if rp.Elems {
		// [a, b] := arr: element i is a field step i on the array (MIR's
		// constant element step), not an index step, so borrow sees one
		// take of the temporary per element rather than a move out of
		// t[_]. check bound every element, so each is taken exactly once.
		for i, f := range rp.Fields {
			bl.bindSub(f, t, uint32(i), isVar, dt)
		}
		return
	}
	for i, f := range rp.Fields {
		k := bl.fieldIndex(bl.b.PlaceType(t), f, uint32(i))
		bl.bindSub(f, t, k, isVar, dt)
	}
}

// bindSub binds PatField f of the value at t, field k.
func (bl *builder) bindSub(f *ast.PatField, t mir.Place, k uint32, isVar bool, dt *destructTake) {
	if f.Pat == nil {
		ft := bl.fieldType(bl.b.PlaceType(t), k)
		if ft == types.NoType {
			ft = bl.in.Binding(f.Name.ID()).Type
		}
		fp := bl.field(t, k, ft)
		l := bl.declare(f.Name)
		op := bl.valueOf(fp, true, f.Span())
		dt.note(op, fp, false, f.Span())
		bl.assign(bl.b.Place(l), use(op), f.Span())
		return
	}
	ft := bl.fieldType(bl.b.PlaceType(t), k)
	if ft == types.NoType {
		ft = bl.in.Type(f.Pat.ID())
	}
	switch p := f.Pat.(type) {
	case *ast.WildcardPattern:
		// A `_` leaf that is not plain is droppable, since a linear one is
		// E0408: a `mut []T` or `mut T` field (`Encoder(s: _, hist: _) :=
		// self`). It is a candidate too,
		// so a destructure whose every leaf is `_` still takes one.
		if ft != types.NoType && ft != types.Poison {
			dt.note(mir.Operand{}, bl.field(t, k, ft), true, f.Span())
		}
	case *ast.NamePattern:
		id := bindingIdent(bl, p)
		if id == nil {
			return
		}
		if ft == types.NoType || ft == types.Poison {
			ft = bl.in.Binding(id.ID()).Type
		}
		fp := bl.field(t, k, ft)
		l := bl.declare(id)
		op := bl.valueOf(fp, true, f.Span())
		dt.note(op, fp, false, f.Span())
		bl.assign(bl.b.Place(l), use(op), f.Span())
	case *ast.PathPattern:
		// A nested struct destructure, (p: Point(x, y), n) := r
		// (irrefutable): its fields are bound from the same temporary,
		// t.k.j. A linear struct with plain fields needs its own take.
		if ft == types.NoType || ft == types.Poison {
			source.ICEf(p.Span(), "lower: a nested destructure of no type")
		}
		fp := bl.field(t, k, ft)
		for j, sf := range p.Fields {
			bl.bindSub(sf, fp, bl.fieldIndex(ft, sf, uint32(j)), isVar, dt)
		}
		dt.took = bl.takeWhole(fp, p.Span()) || dt.took
	default:
		source.ICEf(p.Span(), "lower: destructure sub-pattern %T", p)
	}
}

// fieldIndex is the field a PatField names: check's Use, else by name in
// the record, struct, variant or error declaration; else its position.
func (bl *builder) fieldIndex(t types.TypeID, f *ast.PatField, pos uint32) uint32 {
	if pf := bl.in.Pattern(f.ID()); pf.Kind != PInvalid {
		return pf.Field
	}
	var fields []types.FieldDecl
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KRecord:
		for k, rf := range ty.Fields {
			if rf.Name == f.Name.Name {
				return uint32(k)
			}
		}
	case types.KNamed:
		if d, ok := bl.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl); ok {
			fields = d.Fields
		}
	}
	for k, fd := range fields {
		if fd.Name == f.Name.Name {
			return uint32(k)
		}
	}
	return pos
}

// bindingIdent is the Ident whose binding a NamePattern declares.
func bindingIdent(bl *builder, p *ast.NamePattern) *ast.Ident {
	if bl.in.Binding(p.Name.ID()).OK {
		return p.Name
	}
	if bl.in.Binding(p.ID()).OK {
		return &ast.Ident{Base: ast.Base{NID: p.ID(), Sp: p.Span()}, Name: p.Name.Name}
	}
	return nil
}
