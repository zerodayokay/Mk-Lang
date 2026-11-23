// Why: every expression ends up as a MIR place, operand or rvalue, and the
// order in which its parts are read must match the language's left-to-right
// evaluation even when a later operand writes what an earlier one read.
//
// What: place, placeProper, tempOf and valueOf (places and their reads),
// operand, expr, nameUse, unary, binary, logical and recordLit
// (expressions), literal, and the evaluation-order helpers operandsInOrder,
// readsLate, slot, inert and inertFor.
//
// How: the slot rule: a place read is copied into a fresh temporary at its
// own position when a later operand of the same statement could write it
// (c.n + Bump(mut c) reads n before the call); an inert later operand needs
// no slot. A non-plain read through a value temporary's fields is a Copy
// into a fresh temporary.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// extend returns p with more steps (a fresh projection range).
func (bl *builder) extend(p mir.Place, steps ...mir.Projection) mir.Place {
	all := append(append([]mir.Projection(nil), bl.b.Proj(p)...), steps...)
	return bl.b.Place(p.Local, all...)
}

// deref is p.Deref with the pointee type.
func (bl *builder) deref(p mir.Place, at source.Span) mir.Place {
	t := bl.tab.Get(bl.b.PlaceType(p))
	switch t.Kind {
	case types.KRef, types.KMut, types.KRawPtr:
	default:
		source.ICEf(at, "lower: deref of %s", bl.tab.TypeString(bl.b.PlaceType(p), bl.sig.Generics))
	}
	return bl.extend(p, mir.Projection{Kind: mir.ProjDeref, Type: t.Elem})
}

func (bl *builder) field(p mir.Place, i uint32, t types.TypeID) mir.Place {
	return bl.extend(p, mir.Projection{Kind: mir.ProjField, A: i, Type: t})
}

// fieldType is the type of field i of the struct or record type t: the
// declared type with t's generic arguments substituted; NoType otherwise.
func (bl *builder) fieldType(t types.TypeID, i uint32) types.TypeID {
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KRecord:
		if int(i) < len(ty.Fields) {
			return ty.Fields[i].Type
		}
	case types.KNamed:
		if d, ok := bl.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl); ok && int(i) < len(d.Fields) {
			return bl.substArgs(d.Fields[i].Type, ty.Args)
		}
	}
	return types.NoType
}

// isPlaceExpr reports whether e denotes a place.
func (bl *builder) isPlaceExpr(e ast.Expr) bool {
	switch e := unparen(e).(type) {
	case *ast.Ident:
		switch bl.in.Use(e.ID()).Kind {
		case UseLocal, UseParam, UseSelfField, UseGlobal, UseCapture:
			return true
		}
	case *ast.FieldExpr:
		// A field, or a module-qualified var global (other.G is *(&G), as
		// placeProper builds it).
		switch bl.in.Use(e.ID()).Kind {
		case UseField, UseGlobal:
			return true
		}
	case *ast.UnaryExpr:
		return e.Op == ast.TokStar
	case *ast.IndexExpr:
		return bl.in.Use(e.ID()).Kind == UseNone && len(e.Args) == 1
	}
	return false
}

// place lowers e as a place, with check's Deref layers applied.
func (bl *builder) place(e ast.Expr) mir.Place {
	p := bl.placeProper(e)
	if u := bl.in.Use(e.ID()); u.Kind == UseSelfField {
		return p
	}
	for n := bl.in.Adjust(e.ID()).Deref; n > 0; n-- {
		p = bl.deref(p, e.Span())
	}
	return p
}

// placeProper is the place of e before check's adjustment.
func (bl *builder) placeProper(e ast.Expr) mir.Place {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return bl.place(x.X)
	case *ast.Ident:
		u := bl.in.Use(x.ID())
		switch u.Kind {
		case UseLocal:
			return bl.b.Place(bl.remapped(bl.localOf(u.Binding, x.Span())))
		case UseParam:
			return bl.b.Place(bl.remapped(mir.LocalID(u.Param)))
		case UseCapture:
			return bl.capturePlace(u.Capture, u.Direct, x.Span())
		case UseSelfField:
			sp := bl.b.Place(1)
			if u.Capture != 0 {
				sp = bl.capturePlace(u.Capture-1, false, x.Span()) // a literal of the invariant
			}
			self := bl.deref(sp, x.Span())
			return bl.field(self, u.Field, bl.typeOf(x))
		case UseGlobal:
			return bl.globalPlace(u.Item, bl.typeOf(x), x.Span())
		}
	case *ast.FieldExpr:
		if u := bl.in.Use(x.ID()); u.Kind == UseField {
			base := bl.place(x.X)
			ft := bl.in.Type(x.ID())
			if ft == types.NoType || ft == types.Poison {
				ft = bl.fieldType(bl.b.PlaceType(base), u.Field)
			}
			return bl.field(base, u.Field, ft)
		}
		if u := bl.in.Use(x.ID()); u.Kind == UseGlobal {
			return bl.globalPlace(u.Item, bl.typeOf(x), x.Span())
		}
	case *ast.UnaryExpr:
		if x.Op == ast.TokStar {
			var base mir.Place
			if bl.isPlaceExpr(x.X) {
				base = bl.place(x.X)
			} else {
				base = bl.tempOf(x.X)
			}
			return bl.deref(base, x.Span())
		}
	case *ast.IndexExpr:
		if bl.in.Use(x.ID()).Kind == UseNone && len(x.Args) == 1 {
			return bl.indexPlace(x)
		}
	}
	return bl.tempOf(e)
}

// globalPlace is *(&G).
func (bl *builder) globalPlace(item types.ItemID, t types.TypeID, at source.Span) mir.Place {
	r := bl.newTemp(bl.tab.Ref(t), at)
	bl.assign(bl.b.Place(r), mir.Rvalue{Kind: mir.RvGlobalRef, Item: item, Type: bl.tab.Ref(t)}, at)
	return bl.deref(bl.b.Place(r), at)
}

// tempOf computes e's value into a fresh temporary and returns its place.
func (bl *builder) tempOf(e ast.Expr) mir.Place {
	// A value is read, never a write target: an implementer index inside
	// it uses Items.
	saveMut := bl.placeMut
	bl.placeMut = false
	rv := bl.exprConv(e)
	bl.placeMut = saveMut
	t := bl.rvType(rv, e)
	if rv.Kind == mir.RvUse && (rv.A.Kind == mir.OpCopy || rv.A.Kind == mir.OpMove) && rv.A.Place.Proj.Len == 0 && bl.b.Locals[rv.A.Place.Local].Kind == mir.LocalTemp {
		return rv.A.Place
	}
	l := bl.newTemp(t, e.Span())
	bl.assign(bl.b.Place(l), rv, e.Span())
	return bl.b.Place(l)
}

// rvType is the type of the value rv computes for e. The fact rvalues'
// Type is the type measured or tested, not the result (mir.Rvalue doc):
// SizeOf, AlignOf and FieldCount give int, TypeIs bool.
func (bl *builder) rvType(rv mir.Rvalue, e ast.Expr) types.TypeID {
	switch rv.Kind {
	case mir.RvSizeOf, mir.RvAlignOf, mir.RvFieldCount:
		return types.Int
	case mir.RvTypeIs:
		return types.Bool
	}
	if rv.Kind == mir.RvUse {
		if t := bl.b.OperandType(rv.A); t != types.NoType {
			return t
		}
	} else if rv.Type != types.NoType {
		return rv.Type
	}
	return bl.typeOf(e)
}

// valueOf reads place p: Copy when its type is plain, else Move; a
// non-plain read through Field steps of a value temporary is a Copy into a
// fresh temporary, unless take is set (the consuming forms).
func (bl *builder) valueOf(p mir.Place, take bool, at source.Span) mir.Operand {
	t := bl.b.PlaceType(p)
	if bl.plain(t, at) {
		return mir.Copy(p)
	}
	if !take && p.Proj.Len > 0 && bl.b.Locals[p.Local].Kind == mir.LocalTemp {
		steps := bl.b.Proj(p)
		onlyFields := true
		for _, s := range steps {
			if s.Kind != mir.ProjField && s.Kind != mir.ProjDowncast {
				onlyFields = false
			}
		}
		if onlyFields {
			t2 := bl.newTemp(t, at)
			bl.assign(bl.b.Place(t2), use(mir.Copy(p)), at)
			return mir.Move(bl.b.Place(t2))
		}
	}
	return mir.Move(p)
}

// operand lowers e to an operand, with its implicit conversion applied.
func (bl *builder) operand(e ast.Expr) mir.Operand {
	rv := bl.exprConv(e)
	if rv.Kind == mir.RvUse {
		return rv.A
	}
	t := bl.rvType(rv, e)
	l := bl.newTemp(t, e.Span())
	bl.assign(bl.b.Place(l), rv, e.Span())
	return bl.valueOfTemp(l, e.Span())
}

func (bl *builder) valueOfTemp(l mir.LocalID, at source.Span) mir.Operand {
	if bl.plain(bl.b.Locals[l].Type, at) {
		return mir.Copy(bl.b.Place(l))
	}
	return mir.Move(bl.b.Place(l))
}

// exprConv is expr with Input.Convert applied: error widening or the
// narrowing retype.
func (bl *builder) exprConv(e ast.Expr) mir.Rvalue {
	rv := bl.expr(e)
	c := bl.in.Convert(e.ID())
	if c.Kind == ConvNone {
		return rv
	}
	var a mir.Operand
	if rv.Kind == mir.RvUse {
		a = rv.A
	} else {
		l := bl.newTemp(bl.typeOf(e), e.Span())
		bl.assign(bl.b.Place(l), rv, e.Span())
		a = bl.valueOfTemp(l, e.Span())
	}
	cast := mir.CastWidenErrors
	if c.Kind == ConvNarrow {
		cast = mir.CastNarrow
	}
	return mir.Rvalue{Kind: mir.RvCast, Cast: cast, A: a, Type: c.To}
}

// literal is a literal's operand: OpImm for scalars, OpConst
// otherwise.
func (bl *builder) literal(e ast.Expr) (mir.Operand, bool) {
	l := bl.in.Literal(e.ID())
	switch l.Kind {
	case LitScalar:
		t := bl.in.Type(e.ID())
		if t == types.NoType || t == types.Poison {
			t = l.Type
		}
		return mir.Imm(t, l.Bits), true
	case LitConst:
		return mir.Const(l.Type, l.Const), true
	}
	return mir.Operand{}, false
}

// expr lowers e to an rvalue computing its value (before Convert).
func (bl *builder) expr(e ast.Expr) mir.Rvalue {
	switch x := e.(type) {
	case *ast.IfStmt, *ast.MatchStmt:
		return bl.valueExpr(x)
	case *ast.ParenExpr:
		return bl.exprConv(x.X)
	case *ast.BasicLit:
		if op, ok := bl.literal(x); ok {
			return use(op)
		}
		source.ICEf(x.Span(), "lower: literal %q has no value", x.Raw)
	case *ast.Ident:
		return bl.name(x)
	case *ast.FieldExpr:
		switch u := bl.in.Use(x.ID()); u.Kind {
		case UseField:
			return use(bl.valueOf(bl.place(x), false, x.Span()))
		case UseNone:
			if op, ok := bl.literal(x); ok {
				return use(op)
			}
			bl.typeOf(x)
		default:
			return bl.nameUse(x, u)
		}
	case *ast.UnaryExpr:
		return bl.unary(x)
	case *ast.BinaryExpr:
		return bl.binary(x)
	case *ast.CallExpr:
		return bl.call(x)
	case *ast.IndexExpr:
		if u := bl.in.Use(x.ID()); u.Kind != UseNone {
			return bl.nameUse(x, u)
		}
		if len(x.Args) != 1 {
			// placeProper sends such an index back here through tempOf, so
			// without this guard the two recurse until the stack overflows.
			bl.typeOf(x)
			source.ICEf(x.Span(), "lower: index with %d arguments and no use", len(x.Args))
		}
		return use(bl.valueOf(bl.place(x), false, x.Span()))
	case *ast.SliceExpr:
		return bl.sliceExpr(x, mir.Shared)
	case *ast.RecordLit:
		return bl.recordLit(x)
	case *ast.ArrayLit:
		t := bl.typeOf(x)
		ops := bl.operandsInOrder(x.Elems)
		// An array literal of []T type (check gives one only
		// in a const initializer) is the static slice AggArray of Type t.
		return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggArray, Type: t, Ops: bl.b.AddOperands(ops...)}
	case *ast.RepeatLit:
		t := bl.typeOf(x)
		v := bl.operand(x.Value)
		return mir.Rvalue{Kind: mir.RvRepeat, A: v, Len: bl.tab.Get(t).Len, Type: t}
	case *ast.FnLit:
		if u := bl.in.Use(x.ID()); u.Kind == UseFunc {
			return bl.fnRef(u, bl.typeOf(x))
		}
		source.ICEf(x.Span(), "lower: fn literal without an item")
	case *ast.ElseExpr:
		return bl.elseExpr(x)
	case *ast.GoExpr:
		return bl.goExpr(x)
	case *ast.QuoteExpr:
		return bl.quoteExpr(x)
	}
	source.ICEf(e.Span(), "lower: expression %T not handled", e)
	return mir.Rvalue{}
}

// name lowers a bare name used as a value.
func (bl *builder) name(x *ast.Ident) mir.Rvalue {
	if x.Name == source.SymTrue || x.Name == source.SymFalse {
		v := uint64(0)
		if x.Name == source.SymTrue {
			v = 1
		}
		return use(mir.Imm(types.Bool, v))
	}
	return bl.nameUse(x, bl.in.Use(x.ID()))
}

func (bl *builder) nameUse(x ast.Expr, u Use) mir.Rvalue {
	switch u.Kind {
	case UseLocal, UseParam, UseSelfField, UseGlobal, UseCapture:
		return use(bl.valueOf(bl.place(x), false, x.Span()))
	case UseFunc:
		return bl.fnRef(u, bl.typeOf(x))
	case UseConst:
		return mir.Rvalue{Kind: mir.RvConstRef, Item: u.Item, Type: bl.typeOf(x)}
	case UseVariant:
		return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggEnum, Item: u.Item, Variant: u.Variant, Type: bl.typeOf(x)}
	case UseError:
		return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggError, Item: u.Item, Type: bl.typeOf(x)}
	case UseTarget:
		if bl.cfg.Known == nil || bl.cfg.Known.Target == types.NoItem {
			source.ICEf(x.Span(), "lower: Target with no Known.Target item (request L-8)")
		}
		return mir.Rvalue{Kind: mir.RvConstRef, Item: bl.cfg.Known.Target, Type: bl.typeOf(x)}
	case UseConstParam:
		t := bl.typeOf(x)
		return mir.Rvalue{Kind: mir.RvConstArg, Len: types.ConstArg{Kind: types.CAParam, Param: u.Generic, Type: t}, Type: t}
	case UseNoneLit:
		return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggNone, Type: bl.typeOf(x)}
	}
	if op, ok := bl.literal(x); ok {
		return use(op)
	}
	bl.typeOf(x) // a Poison name (a type name as a value: `A() { int }`) is check's gap
	source.ICEf(x.Span(), "lower: name with no use")
	return mir.Rvalue{}
}

func (bl *builder) fnRef(u Use, t types.TypeID) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvFnRef, Fn: mir.FnRef{Item: u.Item, Args: bl.b.AddGenericArgs(u.Args...)}, Type: t}
}

var unOps = map[ast.TokenKind]mir.UnOp{ast.TokMinus: mir.Neg, ast.TokBang: mir.Not, ast.TokTilde: mir.BitNot}

func (bl *builder) unary(x *ast.UnaryExpr) mir.Rvalue {
	switch x.Op {
	case ast.TokMinus, ast.TokBang, ast.TokTilde:
		if op, ok := bl.literal(x); ok {
			return use(op)
		}
		if c := bl.in.Call(x.ID()); c.Kind == CallFunc {
			return bl.operatorCall(x, c, []ast.Expr{x.X}) // a vector operator
		}
		a := bl.operand(x.X)
		return mir.Rvalue{Kind: mir.RvUnary, Un: unOps[x.Op], A: a, Type: bl.typeOf(x)}
	case ast.TokAmp, ast.TokMut:
		kind := mir.Shared
		if x.Op == ast.TokMut {
			kind = mir.Exclusive
		}
		if s, ok := bl.sliceBorrow(x); ok {
			return bl.sliceExpr(s, kind)
		}
		if u := bl.in.Use(x.X.ID()); u.Kind == UseConst && kind == mir.Shared {
			return mir.Rvalue{Kind: mir.RvGlobalRef, Item: u.Item, Type: bl.typeOf(x)}
		}
		if t, u := bl.typeOf(x), bl.in.Use(unparen(x.X).ID()); u.Kind == UseGlobal && bl.tab.Kind(t) == types.KRawPtr && kind == mir.Shared {
			// &name of an extern var is its address as *T: the
			// static reference, retyped as a raw pointer.
			el := bl.tab.Get(t).Elem
			r := bl.newTemp(bl.tab.Ref(el), x.Span())
			bl.assign(bl.b.Place(r), mir.Rvalue{Kind: mir.RvGlobalRef, Item: u.Item, Type: bl.tab.Ref(el)}, x.Span())
			return mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastLossless, A: mir.Copy(bl.b.Place(r)), Type: t}
		}
		var p mir.Place
		if s, ok := bl.mutSliceDeref(x); ok {
			// `mut *s` with s: mut []T reborrows s (mutSliceDeref).
			if bl.isPlaceExpr(s) {
				p = bl.mutPlace(s)
			} else {
				p = bl.tempOf(s)
			}
		} else if bl.isPlaceExpr(x.X) && kind == mir.Exclusive {
			p = bl.mutPlace(x.X)
		} else if bl.isPlaceExpr(x.X) {
			p = bl.place(x.X)
		} else {
			p = bl.tempOf(x.X)
		}
		return bl.ref(kind, p, bl.typeOf(x))
	case ast.TokStar:
		return use(bl.valueOf(bl.place(x), false, x.Span()))
	case ast.TokTry:
		return bl.tryExpr(x)
	}
	source.ICEf(x.Span(), "lower: unary %v", x.Op)
	return mir.Rvalue{}
}

// ref borrows p. A mut T of a mut []T place reborrows through the slice.
func (bl *builder) ref(kind mir.BorrowKind, p mir.Place, t types.TypeID) mir.Rvalue {
	pt := bl.tab.Get(bl.b.PlaceType(p))
	if tt := bl.tab.Get(t); tt.Kind == types.KMutSlice || tt.Kind == types.KSlice {
		if pt.Kind == types.KMutSlice || pt.Kind == types.KSlice || pt.Kind == types.KArray {
			return mir.Rvalue{Kind: mir.RvSlice, Borrow: kind, Place: p, Type: t}
		}
	}
	return mir.Rvalue{Kind: mir.RvRef, Borrow: kind, Place: p, Origin: mir.OriginPlain, Type: t}
}

var binOps = map[ast.TokenKind]mir.BinOp{
	ast.TokPlus: mir.Add, ast.TokMinus: mir.Sub, ast.TokStar: mir.Mul, ast.TokSlash: mir.Div, ast.TokPercent: mir.Rem,
	ast.TokPlusWrap: mir.AddWrap, ast.TokMinusWrap: mir.SubWrap, ast.TokStarWrap: mir.MulWrap,
	ast.TokShl: mir.Shl, ast.TokShr: mir.Shr, ast.TokAmp: mir.BitAnd, ast.TokPipe: mir.BitOr, ast.TokCaret: mir.BitXor,
	ast.TokEqEq: mir.Eq, ast.TokNotEq: mir.Ne, ast.TokLt: mir.Lt, ast.TokLtEq: mir.Le, ast.TokGt: mir.Gt, ast.TokGtEq: mir.Ge,
}

// compoundOps maps a compound assignment to its operator.
var compoundOps = map[ast.TokenKind]mir.BinOp{
	ast.TokPlusEq: mir.Add, ast.TokMinusEq: mir.Sub, ast.TokStarEq: mir.Mul, ast.TokSlashEq: mir.Div,
	ast.TokPercentEq: mir.Rem, ast.TokPlusWrapEq: mir.AddWrap, ast.TokMinusWrapEq: mir.SubWrap,
	ast.TokStarWrapEq: mir.MulWrap, ast.TokShlEq: mir.Shl, ast.TokShrEq: mir.Shr, ast.TokAmpEq: mir.BitAnd,
	ast.TokPipeEq: mir.BitOr, ast.TokCaretEq: mir.BitXor,
}

func (bl *builder) binary(x *ast.BinaryExpr) mir.Rvalue {
	if op, ok := bl.literal(x); ok {
		return use(op)
	}
	if x.Op == ast.TokAmpAmp || x.Op == ast.TokPipePipe {
		return bl.logical(x)
	}
	if c := bl.in.Call(x.ID()); c.Kind == CallFunc {
		return bl.operatorCall(x, c, []ast.Expr{x.X, x.Y}) // a vector operator
	}
	op, ok := binOps[x.Op]
	if !ok {
		source.ICEf(x.Span(), "lower: binary %v", x.Op)
	}
	ops := bl.operandsInOrder([]ast.Expr{x.X, x.Y})
	return mir.Rvalue{Kind: mir.RvBinary, Bin: op, A: ops[0], B: ops[1], Type: bl.typeOf(x)}
}

// operatorCall lowers an operator check recorded as
// a call of a std.simd intrinsic (`a +% b` on Vec[u8, 16] is
// `Vec:AddWrap[u8, 16](copy a, copy b)`) lowers to that direct call, the
// operands evaluated left to right and passed as copies (vectors are
// plain). No MIR kind is added for vectors.
func (bl *builder) operatorCall(x ast.Expr, c Call, operands []ast.Expr) mir.Rvalue {
	ops := bl.operandsInOrder(operands)
	dest := bl.directCall(c.Item, c.Args, ops, bl.typeOf(x), x.Span())
	return use(mir.Copy(dest))
}

// logical is && and || as control flow.
func (bl *builder) logical(x *ast.BinaryExpr) mir.Rvalue {
	r := bl.newTemp(types.Bool, x.Span())
	a := bl.operand(x.X)
	rhs, short, join := bl.newBlock(), bl.newBlock(), bl.newBlock()
	if x.Op == ast.TokAmpAmp {
		bl.boolSwitch(a, rhs, short, x.Span())
	} else {
		bl.boolSwitch(a, short, rhs, x.Span())
	}
	bl.cur = rhs
	b := bl.operand(x.Y)
	bl.assign(bl.b.Place(r), use(b), x.Span())
	bl.gotoBlock(join, x.Span())
	bl.cur = short
	v := uint64(0)
	if x.Op == ast.TokPipePipe {
		v = 1
	}
	bl.assign(bl.b.Place(r), use(mir.Imm(types.Bool, v)), x.Span())
	bl.gotoBlock(join, x.Span())
	bl.cur = join
	return use(mir.Copy(bl.b.Place(r)))
}

// recordLit evaluates fields in written order and builds the record with
// operands in canonical order.
func (bl *builder) recordLit(x *ast.RecordLit) mir.Rvalue {
	t := bl.typeOf(x)
	rt := bl.tab.Get(t)
	vals := make([]ast.Expr, len(x.Fields))
	for i, f := range x.Fields {
		if f.Value != nil {
			vals[i] = f.Value
		} else {
			vals[i] = f.Name
		}
	}
	written := bl.operandsInOrder(vals)
	ops := make([]mir.Operand, len(rt.Fields))
	for i, f := range x.Fields {
		for k, rf := range rt.Fields {
			if rf.Name == f.Name.Name {
				ops[k] = written[i]
			}
		}
	}
	return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggRecord, Type: t, Ops: bl.b.AddOperands(ops...)}
}

// operandsInOrder lowers es left to right: a whole local read
// directly when every later operand is inert, else into a slot at its
// position.
func (bl *builder) operandsInOrder(es []ast.Expr) []mir.Operand {
	ops := make([]mir.Operand, len(es))
	for i, e := range es {
		op := bl.operand(e)
		if bl.readsLate(op) && !bl.inertAfter(es[i+1:], op.Place) {
			op = bl.slot(op, e.Span())
		}
		ops[i] = op
	}
	return ops
}

// readsLate reports whether op reads a place that a later operand of the same
// statement or terminator could write before the read happens: a Copy or Move
// of a place rooted in a user local or parameter, a whole one or a field,
// element or deref path of one (c.n + Bump(mut c) and self.n + self.Bump() read
// n before the call), or of a temporary through a Deref or Index step. A whole
// temporary, or a field path of one, holds a value no later operand can reach.
func (bl *builder) readsLate(op mir.Operand) bool {
	if op.Kind != mir.OpCopy && op.Kind != mir.OpMove {
		return false
	}
	if bl.b.Locals[op.Place.Local].Kind != mir.LocalTemp {
		return true
	}
	for _, s := range bl.b.Proj(op.Place) {
		if s.Kind == mir.ProjDeref || s.Kind == mir.ProjIndex {
			return true
		}
	}
	return false
}

// slot reads op into a fresh temporary at its own position (the slot rule)
// and returns the temporary's operand.
func (bl *builder) slot(op mir.Operand, at source.Span) mir.Operand {
	l := bl.newTemp(bl.b.PlaceType(op.Place), at)
	bl.assign(bl.b.Place(l), use(op), at)
	return bl.valueOfTemp(l, at)
}

// inertAfter reports whether every expression is inert for the read x
// (see inertFor).
func (bl *builder) inertAfter(es []ast.Expr, x mir.Place) bool {
	for _, e := range es {
		if !bl.inertFor(e, x) {
			return false
		}
	}
	return true
}

// inert reports whether e runs no code and writes nothing: a literal, a
// payload-free variant or error, a const, a function value, a read of a local
// or parameter through any field, deref or element path whose index is itself
// inert (the bounds check writes nothing), or a borrow form of such a path. The
// Deref and Index steps read and never write, so a read of x before them equals
// a read after them.
func (bl *builder) inert(e ast.Expr) bool {
	switch x := unparen(e).(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		switch bl.in.Use(x.ID()).Kind {
		case UseLocal, UseParam, UseSelfField, UseFunc, UseConst, UseVariant, UseError, UseNoneLit, UseConstParam, UseCapture:
			return true
		}
		return x.Name == source.SymTrue || x.Name == source.SymFalse
	case *ast.FieldExpr:
		switch bl.in.Use(x.ID()).Kind {
		case UseField:
			return bl.inert(x.X)
		case UseVariant, UseError, UseConst, UseFunc:
			return true
		}
	case *ast.UnaryExpr:
		switch x.Op {
		case ast.TokAmp, ast.TokMut, ast.TokStar:
			return bl.inert(x.X)
		case ast.TokMinus:
			_, lit := x.X.(*ast.BasicLit)
			return lit
		}
	case *ast.CallExpr:
		// xs.Len() on a slice, array or str is RvLen: it reads xs only.
		if c := bl.in.Call(x.ID()); c.Kind == CallBuiltin && c.Builtin == BLen && len(x.Args) == 0 {
			if r := recvExpr(x); r != nil {
				return bl.inert(r)
			}
		}
	case *ast.IndexExpr:
		if bl.in.Use(x.ID()).Kind != UseNone || len(x.Args) != 1 {
			return false
		}
		ie, ok := x.Args[0].(ast.Expr)
		if !ok || !bl.inert(ie) || !bl.inert(x.X) {
			return false
		}
		// An implementer's element is read through Items, a call.
		t := bl.in.Type(x.X.ID())
		for k := bl.tab.Kind(t); k == types.KRef || k == types.KMut; k = bl.tab.Kind(t) {
			t = bl.tab.Get(t).Elem
		}
		switch bl.tab.Kind(t) {
		case types.KSlice, types.KMutSlice, types.KArray, types.KStr:
			return true
		}
	}
	return false
}

// inertFor is inert for an operand that reads x at the statement or
// terminator: a borrow form (&p, mut p) is inert only when it is disjoint
// from x (it borrows a place other than x), so F(x, &x) and
// F(x, mut x) read x into a slot first; every other inert form only reads.
func (bl *builder) inertFor(e ast.Expr, x mir.Place) bool {
	if u, ok := unparen(e).(*ast.UnaryExpr); ok && (u.Op == ast.TokMut || u.Op == ast.TokAmp) {
		return bl.inert(u.X) && bl.disjoint(u.X, x, u.Op == ast.TokMut)
	}
	return bl.inert(e)
}

// disjoint reports whether a borrow of the place expression p cannot
// overlap the MIR place x: p's root local is neither x's root nor a local
// an Index step of x reads (locals do not alias). An exclusive borrow also
// needs x to have no Deref step (x may be read through a reference to p's
// root); a shared borrow only reads.
func (bl *builder) disjoint(p ast.Expr, x mir.Place, exclusive bool) bool {
	root, ok := bl.rootLocal(p)
	if !ok || root == x.Local {
		return false
	}
	for _, s := range bl.b.Proj(x) {
		if (exclusive && s.Kind == mir.ProjDeref) || (s.Kind == mir.ProjIndex && mir.LocalID(s.A) == root) {
			return false
		}
	}
	return true
}

// mutSliceDeref reports whether u is `mut *s` with s: mut []T, and gives s.
// Check types it as the reborrow of the slice, mut []T again (expr_ops.go):
// it is the RvSlice of s itself, as `mut s[..]`, and never a deref of s,
// which lower cannot form (it would be E0000 "lower: deref of mut []i32").
func (bl *builder) mutSliceDeref(u *ast.UnaryExpr) (ast.Expr, bool) {
	if u.Op != ast.TokMut {
		return nil, false
	}
	d, ok := unparen(u.X).(*ast.UnaryExpr)
	if !ok || d.Op != ast.TokStar || bl.tab.Kind(bl.typeOf(d.X)) != types.KMutSlice {
		return nil, false
	}
	return d.X, true
}

// sliceBorrow reports whether u is mut xs[a..b] (or a & form check types
// as a slice): the slice expression is the borrow itself, an RvSlice of
// u's type. Check types &xs[a..b] as &[]T, a reference to
// the shared view's temporary, which is an ordinary & of a value.
func (bl *builder) sliceBorrow(u *ast.UnaryExpr) (*ast.SliceExpr, bool) {
	s, ok := unparen(u.X).(*ast.SliceExpr)
	if !ok {
		return nil, false
	}
	switch bl.tab.Kind(bl.typeOf(u)) {
	case types.KSlice, types.KMutSlice:
		return s, true
	}
	return nil, false
}

// rootLocal is the MIR local at the root of a place expression's path.
func (bl *builder) rootLocal(e ast.Expr) (mir.LocalID, bool) {
	switch x := unparen(e).(type) {
	case *ast.Ident:
		u := bl.in.Use(x.ID())
		switch u.Kind {
		case UseLocal:
			if i := int(u.Binding) - int(bl.nodeBase); i >= 0 && i < len(bl.locals) && bl.locals[i] != mir.NoLocal {
				return bl.remapped(bl.locals[i]), true
			}
		case UseParam:
			return bl.remapped(mir.LocalID(u.Param)), true
		case UseCapture:
			return bl.env, bl.env != mir.NoLocal
		case UseSelfField:
			if u.Capture != 0 {
				return bl.env, bl.env != mir.NoLocal
			}
			return 1, true
		}
	case *ast.FieldExpr:
		if bl.in.Use(x.ID()).Kind == UseField {
			return bl.rootLocal(x.X)
		}
	case *ast.UnaryExpr:
		if x.Op == ast.TokStar {
			return bl.rootLocal(x.X)
		}
	case *ast.IndexExpr:
		return bl.rootLocal(x.X)
	}
	return mir.NoLocal, false
}
