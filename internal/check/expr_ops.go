// Why: operators, conversions and references are where untyped literals
// meet typed values; each must adapt a literal to the other side, judge a
// constant by its value, and refuse a lossy or meaningless conversion.
//
// What: unary and binary (with vector operators and Target folds),
// sameOperands and adaptTo, derefType, conversion and convert (`T(x)`,
// `Cast[T](x)`, `Trunc[T](x)`: E0303, E0302), constFits, floatConstFitsF32
// and constFolder,
// unhandledOperand (E0802), noOperator (E0308, E0703).
//
// How: a lossy conversion is E0303 unless the operand is a constant whose
// value fits; a named const with no value yet is folded or requested from
// the evaluator, never assumed to fit. int and uint are 64-bit.

package check

import (
	"math"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

var opText = map[ast.TokenKind]string{
	ast.TokPlus: "+", ast.TokMinus: "-", ast.TokStar: "*", ast.TokSlash: "/", ast.TokPercent: "%",
	ast.TokPlusWrap: "+%", ast.TokMinusWrap: "-%", ast.TokStarWrap: "*%", ast.TokAmp: "&", ast.TokPipe: "|",
	ast.TokCaret: "^", ast.TokShl: "<<", ast.TokShr: ">>", ast.TokTilde: "~", ast.TokBang: "!",
	ast.TokAmpAmp: "&&", ast.TokPipePipe: "||", ast.TokEqEq: "==", ast.TokNotEq: "!=",
	ast.TokLt: "<", ast.TokLtEq: "<=", ast.TokGt: ">", ast.TokGtEq: ">=", ast.TokMut: "mut",
	ast.TokPlusEq: "+=", ast.TokMinusEq: "-=", ast.TokStarEq: "*=", ast.TokSlashEq: "/=", ast.TokPercentEq: "%=",
	ast.TokPlusWrapEq: "+%=", ast.TokMinusWrapEq: "-%=", ast.TokStarWrapEq: "*%=", ast.TokShlEq: "<<=", ast.TokShrEq: ">>=",
	ast.TokAmpEq: "&=", ast.TokPipeEq: "|=", ast.TokCaretEq: "^=",
}

// noOperator is E0308, or E0703 on a type parameter.
func (u *unit) noOperator(at source.Span, op ast.TokenKind, t types.TypeID) types.TypeID {
	if t == types.Poison {
		return types.Poison
	}
	if u.tab.Kind(t) == types.KParam {
		u.needsBound(at, t, "`"+opText[op]+"`")
		return types.Poison
	}
	u.err(diag.E0308, at, "`"+opText[op]+"` is not defined on `"+u.str(t)+"`").Text("operator", opText[op]).Text("type", u.str(t))
	return types.Poison
}

func (u *unit) isInt(t types.TypeID) bool   { return u.tab.Get(t).IsInteger() }
func (u *unit) isFloat(t types.TypeID) bool { return u.tab.Kind(t) == types.KFloat }
func (u *unit) isNum(t types.TypeID) bool   { return u.isInt(t) || u.isFloat(t) }

// unary checks a prefix operator.
func (u *unit) unary(e *ast.UnaryExpr, want types.TypeID) types.TypeID {
	tab := u.tab
	switch e.Op {
	case ast.TokMinus:
		if lit, ok := e.X.(*ast.BasicLit); ok && (lit.Kind == ast.TokInt || lit.Kind == ast.TokFloat) {
			t := u.literal(lit, true, want)
			u.setType(lit, t)
			// The folded value belongs to the UnaryExpr, which lower reads
			// as one constant; the literal keeps none (no double negation).
			if u.b.Consts != nil {
				if k := u.b.Consts[u.b.slot(lit.ID())]; k != types.NoConst {
					u.b.setConst(e.ID(), k)
					u.b.Consts[u.b.slot(lit.ID())] = types.NoConst
				}
			}
			return t
		}
		t := u.unhandledOperand(e.X, u.expr(e.X, numericWant(u, want)))
		if t == types.Poison || (u.isInt(t) && tab.Get(t).Signed) || u.isFloat(t) {
			return t
		}
		if v, ok := u.vecOf(t); ok {
			return u.vecUnary(e, v)
		}
		return u.noOperator(e.Span(), e.Op, t)
	case ast.TokBang:
		t := u.unhandledOperand(e.X, u.operand(e.X, types.NoType))
		if t == types.Bool {
			u.foldNot(e) // !(Target.arch == Arch.X64)
		}
		if t == types.Bool || t == types.Poison {
			return t
		}
		if v, ok := u.vecOf(t); ok {
			return u.vecUnary(e, v)
		}
		return u.noOperator(e.Span(), e.Op, t)
	case ast.TokTilde:
		t := u.unhandledOperand(e.X, u.expr(e.X, numericWant(u, want)))
		if t == types.Poison || u.isInt(t) {
			return t
		}
		if v, ok := u.vecOf(t); ok {
			return u.vecUnary(e, v)
		}
		return u.noOperator(e.Span(), e.Op, t)
	case ast.TokAmp:
		inner := types.NoType
		if want != types.NoType && want != types.Poison && tab.Kind(want) == types.KRef {
			inner = tab.Get(want).Elem
		}
		t := u.operand(e.X, inner)
		if t == types.Poison {
			return t
		}
		if _, ok := u.externVarPlace(e.X); ok {
			return tab.RawPtr(t) // the one place & gives a raw pointer
		}
		u.packedBorrow(e, "&")
		return tab.Ref(t)
	case ast.TokMut:
		if s, ok := e.X.(*ast.SliceExpr); ok {
			t := u.expr(s, types.NoType)
			if t == types.Poison {
				return t
			}
			if k := tab.Kind(t); k == types.KSlice {
				return tab.MutSlice(tab.Get(t).Elem)
			}
			return tab.Mut(t)
		}
		if d, ok := e.X.(*ast.UnaryExpr); ok && d.Op == ast.TokStar {
			// `mut *s` with s: mut []T reborrows the slice: the result is
			// mut []T again.
			if st := u.operand(d.X, types.NoType); tab.Kind(st) == types.KMutSlice {
				u.setType(d, types.Poison)
				return st
			} else if st == types.Poison {
				u.setType(d, types.Poison)
				return st
			} else {
				u.setType(d, u.derefType(d, st))
				if dt := u.b.TypeOf(d.ID()); dt != types.Poison {
					return tab.Mut(dt)
				}
				return types.Poison
			}
		}
		inner := types.NoType
		if want != types.NoType && want != types.Poison && tab.Kind(want) == types.KMut {
			inner = tab.Get(want).Elem
		}
		t := u.operand(e.X, inner)
		if t == types.Poison {
			return t
		}
		u.externVarWrite(e.X, "borrow")
		u.packedBorrow(e, "mut")
		return tab.Mut(t)
	case ast.TokStar:
		t := u.operand(e.X, types.NoType)
		if t == types.Poison {
			return t
		}
		return u.derefType(e, t)
	case ast.TokTry:
		return u.tryExpr(e, want)
	}
	return types.Poison
}

// derefType is `*e` with e: t: T of &T, mut T and *T; *void is
// E0301; anything else E0308.
func (u *unit) derefType(e *ast.UnaryExpr, t types.TypeID) types.TypeID {
	switch ty := u.tab.Get(t); ty.Kind {
	case types.KRef, types.KMut:
		return ty.Elem
	case types.KRawPtr:
		if ty.Elem == types.Void {
			u.err(diag.E0301, e.Span(), "`*void` cannot be dereferenced: cast it to a typed pointer first").
				Text("expected", "*T").Text("found", u.str(t))
			return types.Poison
		}
		u.rawDeref(e, t)
		return ty.Elem
	}
	u.err(diag.E0308, e.Span(), "`*` needs a reference or a raw pointer, found `"+u.str(t)+"`").Text("operator", "*").Text("type", u.str(t))
	return types.Poison
}

// operand checks a sub-expression whose expectation is advisory only: an
// untyped literal defaults, nothing is reported against want.
func (u *unit) operand(e ast.Expr, want types.TypeID) types.TypeID {
	t := u.exprInner(e, want)
	if t == types.NoType {
		t = types.Poison
	}
	u.setType(e, t)
	return t
}

// unhandledOperand handles an operator's operand x of type t:
// an `E!T` value is consumed only by `try`, `else`, `match` or `.Must()`,
// so as an operand it is E0802 at x with the `try` fix (unless the
// expected type already drew E0802 or E0301 there), and Poison from then
// on: no E0308 on the operator and no E0301 at the other operand adapted
// to `E!T`. One diagnostic per unhandled value, so `F() == F()` has two.
func (u *unit) unhandledOperand(x ast.Expr, t types.TypeID) types.TypeID {
	if t == types.Poison || t == types.NoType || u.tab.Kind(t) != types.KErrorUnion {
		return t
	}
	u.storedFallible(x)
	return types.Poison
}

// numericWant passes an expected type down to an arithmetic operand only
// when it is numeric (or a parameter, so a literal reports E0703).
func numericWant(u *unit, want types.TypeID) types.TypeID {
	if want == types.NoType || want == types.Poison {
		return types.NoType
	}
	if u.isNum(want) || u.tab.Kind(want) == types.KParam {
		return want
	}
	return types.NoType
}

// binary checks an infix operator: both operands of one type after
// adaptation, then the operator's domain.
func (u *unit) binary(e *ast.BinaryExpr, want types.TypeID) types.TypeID {
	switch e.Op {
	case ast.TokAmpAmp, ast.TokPipePipe:
		x := u.unhandledOperand(e.X, u.operand(e.X, types.Bool))
		y := u.unhandledOperand(e.Y, u.operand(e.Y, types.Bool))
		if mx, my := u.maskOperand(e.X, x), u.maskOperand(e.Y, y); mx || my {
			return types.Poison // E0301, not E0308
		}
		if x != types.Bool && x != types.Poison {
			return u.noOperator(e.Span(), e.Op, x)
		}
		if y != types.Bool && y != types.Poison {
			return u.noOperator(e.Span(), e.Op, y)
		}
		if x == types.Bool && y == types.Bool {
			u.foldLogic(e) // an arch test of constants
		}
		return types.Bool
	case ast.TokShl, ast.TokShr:
		x := u.unhandledOperand(e.X, u.expr(e.X, numericWant(u, want)))
		y := u.unhandledOperand(e.Y, u.operand(e.Y, types.NoType))
		if v, ok := u.vecOf(x); ok {
			return u.vecShift(e, v, y)
		}
		if x == types.Poison || y == types.Poison {
			return x
		}
		if !u.isInt(x) {
			return u.noOperator(e.Span(), e.Op, x)
		}
		if !u.isInt(y) {
			return u.noOperator(e.Span(), e.Op, y)
		}
		return x
	}
	cmp := false
	switch e.Op {
	case ast.TokEqEq, ast.TokNotEq, ast.TokLt, ast.TokLtEq, ast.TokGt, ast.TokGtEq:
		cmp = true
	}
	opWant := types.NoType
	if !cmp {
		opWant = numericWant(u, want)
	}
	t := u.sameOperands(e, opWant)
	if t == types.Poison {
		if cmp {
			return types.Bool
		}
		return t
	}
	if v, ok := u.vecOf(t); ok {
		return u.vecBinary(e, v)
	}
	tab := u.tab
	ty := tab.Get(t)
	switch e.Op {
	case ast.TokPlus, ast.TokMinus, ast.TokStar, ast.TokSlash, ast.TokPercent:
		if u.isNum(t) {
			return t
		}
	case ast.TokPlusWrap, ast.TokMinusWrap, ast.TokStarWrap, ast.TokAmp, ast.TokPipe, ast.TokCaret:
		if u.isInt(t) {
			return t
		}
	case ast.TokEqEq, ast.TokNotEq:
		if u.isNum(t) || t == types.Bool || t == types.Char || t == types.Str || u.plainEnum(ty) {
			u.foldTargetCompare(e) // Target.arch == Arch.X64
			return types.Bool
		}
		if isNoneName(u, e.X) || isNoneName(u, e.Y) {
			u.err(diag.E0308, e.Span(), "compare an optional with `none` through `.IsNone()`").
				Text("operator", opText[e.Op]).Text("type", u.str(t)).Alt(".IsNone()")
			return types.Bool
		}
		u.noOperator(e.Span(), e.Op, t)
		return types.Bool
	case ast.TokLt, ast.TokLtEq, ast.TokGt, ast.TokGtEq:
		if u.isNum(t) || t == types.Char || t == types.Str {
			return types.Bool
		}
		u.noOperator(e.Span(), e.Op, t)
		return types.Bool
	}
	return u.noOperator(e.Span(), e.Op, t)
}

// sameOperands checks both operands of an arithmetic or comparison operator
// into one type: an untyped literal adapts to the other side; two literals
// give int or f64; otherwise the types must be equal (E0301 at the right
// operand).
func (u *unit) sameOperands(e *ast.BinaryExpr, want types.TypeID) types.TypeID {
	xu, yu := isUntyped(e.X), isUntyped(e.Y)
	var x, y types.TypeID
	switch {
	case xu && !yu:
		y = u.unhandledOperand(e.Y, u.operand(e.Y, want))
		x = u.operand(e.X, adaptTo(u, y))
	case xu && yu && want == types.NoType:
		x = u.operand(e.X, defaultType(e.X))
		if defaultType(e.Y) == types.F64 && x == types.Int {
			x = u.operand(e.X, types.F64)
		}
		y = u.operand(e.Y, adaptTo(u, x))
	default:
		x = u.unhandledOperand(e.X, u.operand(e.X, want))
		y = u.unhandledOperand(e.Y, u.operand(e.Y, adaptTo(u, x)))
	}
	if x == types.Poison || y == types.Poison {
		return types.Poison
	}
	if x != y {
		u.mismatch(e.Y.Span(), x, y)
		return types.Poison
	}
	return x
}

// adaptTo is the expected type a literal operand takes from the other
// operand's type.
func adaptTo(u *unit, t types.TypeID) types.TypeID {
	if t == types.Poison {
		return types.NoType
	}
	return t
}

func isNoneName(u *unit, e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	r := u.ref(id)
	return r.Kind == resolve.RefUniverse && source.Symbol(r.A) == source.SymNone
}

// plainEnum reports a payload-free enum (== and != apply to it).
func (u *unit) plainEnum(ty *types.Type) bool {
	if ty.Kind != types.KNamed {
		return false
	}
	d, ok := u.c.cfg.Items.Get(ty.Item).Decl.(*types.EnumDecl)
	if !ok {
		return false
	}
	for _, v := range d.Variants {
		if len(v.Fields) > 0 {
			return false
		}
	}
	return true
}

// call checks a call: conversions `T(x)` to a primitive, `Cast[T](x)` and
// `Trunc[T](x)` here, every other form in callOther.
func (u *unit) call(e *ast.CallExpr, want types.TypeID) types.TypeID {
	var to types.TypeID
	builtin := BuiltinNone
	switch f := e.Fun.(type) {
	case *ast.Ident:
		if r := u.ref(f); r.Kind == resolve.RefPrim {
			to = types.TypeID(r.A)
			builtin = BuiltinConvert
		}
	case *ast.IndexExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if r := u.ref(id); r.Kind == resolve.RefUniverse && len(f.Args) == 1 {
				switch source.Symbol(r.A) {
				case source.SymCast:
					builtin = BuiltinCast
				case source.SymTrunc:
					builtin = BuiltinTrunc
				}
				if builtin != BuiltinNone {
					to = u.x.typeOf(f.Args[0], posPlain)
				}
			}
		}
	}
	if builtin == BuiltinNone {
		return u.callOther(e, want)
	}
	return u.conversion(e, to, builtin)
}

// conversion checks `T(x)`, `Cast[T](x)` or `Trunc[T](x)` with T already
// read: one value, then the judgment of the builtin. `T(x)` with T
// an alias that is not of a struct, or an enum, comes here from callName
// (an alias is transparent, so `Byte(x)` is `u8(x)`), and
// an integer to an enum is E0303 (`lossless`).
func (u *unit) conversion(e *ast.CallExpr, to types.TypeID, builtin Builtin) types.TypeID {
	*u.b.use(e.ID()) = Use{Kind: UseBuiltin, Index: uint32(builtin)}
	if len(e.Args) != 1 || e.Args[0] == nil || e.Args[0].Name != nil {
		for _, a := range e.Args {
			if a != nil {
				u.operand(a.Value, types.NoType)
			}
		}
		u.err(diag.E0304, e.Span(), "a conversion takes one value").Text("signature", u.str(to)+"(x)").Int("expected", 1).Int("found", int64(len(e.Args)))
		return to
	}
	arg := e.Args[0].Value
	argWant := types.NoType
	if builtin == BuiltinTrunc && isUntyped(arg) {
		argWant = types.Int // a literal argument of Trunc is int
	}
	from := u.operand(arg, argWant)
	if to == types.Poison || from == types.Poison {
		return to
	}
	// A conversion of or to a type parameter is no operation a bound gives
	// (E0703, not E0302).
	for _, p := range [2]types.TypeID{from, to} {
		if u.tab.Kind(p) == types.KParam {
			op := "`" + u.str(to) + "(x)`"
			switch builtin {
			case BuiltinCast:
				op = "`Cast[" + u.str(to) + "](x)`"
			case BuiltinTrunc:
				op = "`Trunc[" + u.str(to) + "](x)`"
			}
			u.needsBound(e.Span(), p, "the conversion "+op)
			return types.Poison
		}
	}
	switch builtin {
	case BuiltinCast:
		if !u.castable(from) || !u.castable(to) {
			u.noConversion(e.Span(), from, to)
		}
	case BuiltinTrunc:
		if !u.isInt(from) || !u.isInt(to) {
			u.noConversion(e.Span(), from, to)
		}
	default:
		u.convert(e, arg, from, to)
	}
	return to
}

// castable is Cast's domain: integers, floats, char, enums with a declared
// representation.
func (u *unit) castable(t types.TypeID) bool {
	return u.isNum(t) || t == types.Char || u.reprEnum(t) != types.NoType
}

// reprEnum is the declared representation of an enum, NoType otherwise.
func (u *unit) reprEnum(t types.TypeID) types.TypeID {
	ty := u.tab.Get(t)
	if ty.Kind != types.KNamed {
		return types.NoType
	}
	if d, ok := u.c.cfg.Items.Get(ty.Item).Decl.(*types.EnumDecl); ok {
		return d.Tag
	}
	return types.NoType
}

func (u *unit) noConversion(at source.Span, from, to types.TypeID) {
	u.err(diag.E0302, at, "no conversion from `"+u.str(from)+"` to `"+u.str(to)+"`").Text("from", u.str(from)).Text("to", u.str(to))
}

// lossy is E0303 for `T(x)`, naming only the conversions that take the
// pair: Cast for any castable pair, Trunc for two integers. The fix's
// reason says what Cast does for the pair: from an integer to a float it
// rounds and never panics.
func (u *unit) lossy(at source.Span, from, to types.TypeID) {
	cast := u.castable(from) && u.castable(to)
	trunc := u.isInt(from) && u.isInt(to)
	use, why := "", ""
	switch {
	case cast && trunc:
		use, why = ": use `Cast` or `Trunc`", "Cast panics on a value that does not fit; Trunc keeps the low bits"
	case cast && u.isInt(from) && u.isFloat(to):
		use, why = ": use `Cast`", "Cast rounds to the nearest value"
	case cast:
		use, why = ": use `Cast`", "Cast panics on a value that does not fit"
	case trunc:
		use, why = ": use `Trunc`", "Trunc keeps the low bits"
	}
	d := u.err(diag.E0303, at, "`"+u.str(to)+"("+u.str(from)+")` can lose information"+use).
		Text("from", u.str(from)).Text("to", u.str(to))
	if why != "" {
		d.Fix(diag.Fix{Class: diag.Compiles, Why: why})
	}
}

// intInfo is an integer type's width (int and uint are 64-bit) and
// signedness.
func (u *unit) intInfo(t types.TypeID) (uint, bool) {
	ty := u.tab.Get(t)
	if ty.Kind == types.KIntPtr {
		return 64, ty.Signed
	}
	return uint(ty.Bits), ty.Signed
}

// convert judges `T(x)`: lossless conversions pass, a lossy one is
// E0303 unless x is a literal whose value fits, no conversion is
// E0302.
func (u *unit) convert(e *ast.CallExpr, arg ast.Expr, from, to types.TypeID) {
	if from == to {
		return
	}
	ok, known := u.lossless(from, to)
	if !known {
		u.noConversion(e.Span(), from, to)
		return
	}
	// An enum target is never judged by value: a constant whose value is not
	// known yet would be accepted (constFits), and no integer
	// converts to an enum.
	if ok || (u.reprEnum(to) == types.NoType && u.constFits(arg, from, to)) {
		return
	}
	u.lossy(e.Span(), from, to)
}

// lossless reports whether every value of from fits to; known is false when
// the pair has no conversion at all (E0302).
func (u *unit) lossless(from, to types.TypeID) (ok, known bool) {
	if r := u.reprEnum(from); r != types.NoType {
		if r == to {
			return true, true
		}
		return u.lossless(r, to)
	}
	switch {
	case u.isInt(from) && u.isInt(to):
		fb, fs := u.intInfo(from)
		tb, ts := u.intInfo(to)
		switch {
		case fs == ts:
			return tb >= fb, true
		case !fs && ts:
			return tb > fb, true
		}
		return false, true
	case u.isInt(from) && u.isFloat(to):
		fb, _ := u.intInfo(from)
		if to == types.F32 {
			return fb <= 16, true
		}
		return fb <= 32, true
	case u.isFloat(from) && u.isFloat(to):
		return to == types.F64, true
	case u.isFloat(from) && u.isInt(to):
		return false, true
	case from == types.U8 && to == types.Char:
		return true, true
	case u.isInt(from) && to == types.Char:
		return false, true
	case from == types.Char && u.isInt(to):
		b, _ := u.intInfo(to)
		return b >= 32, true
	case u.isInt(from) && u.reprEnum(to) != types.NoType:
		return false, true
	}
	return false, false
}

// constShaped reports whether e is integer operators over integer literals
// and named consts only (the "operators over constants" forms
// check folds), reading names only, so the early-set scan can use it too.
func (m *Module) constShaped(fx *fileIndex, e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return m.constShaped(fx, e.X)
	case *ast.BasicLit:
		return e.Kind == ast.TokInt
	case *ast.Ident, *ast.FieldExpr:
		id := e.ID()
		if int(id) >= len(fx.info.Refs) {
			return false
		}
		r := fx.info.Refs[id]
		return r.Kind == resolve.RefItem && m.c.cfg.Items.Get(types.ItemID(r.A)).Kind == types.KindConst
	case *ast.UnaryExpr:
		return (e.Op == ast.TokMinus || e.Op == ast.TokTilde) && m.constShaped(fx, e.X)
	case *ast.BinaryExpr:
		return m.constShaped(fx, e.X) && m.constShaped(fx, e.Y)
	}
	return false
}

// constFolder folds a constant operand of a conversion: a named const with
// no value yet goes through the request path of a const argument
// (constValueArg: folded, else EvalConst at StageSignatures, or E0908 for a
// const that needs a same-module layout), so the value is judged, never
// assumed to fit.
func (u *unit) constFolder() *folder {
	f := &folder{m: u.m, fx: u.fx, rec: u.x.rec}
	f.constUse = func(it *types.Item, at source.Span) (types.ConstID, bool) {
		a, ok := u.m.constValueArg(u.x, it, at)
		return a.Value, ok && a.Kind == types.CAValue
	}
	return f
}

// constFits judges a constant argument by its value: an integer
// literal (or a negated one), a named const or integer operators over them
// whose value fits the target type.
func (u *unit) constFits(arg ast.Expr, from, to types.TypeID) bool {
	for {
		p, ok := arg.(*ast.ParenExpr)
		if !ok {
			break
		}
		arg = p.X
	}
	neg := false
	if un, ok := arg.(*ast.UnaryExpr); ok && un.Op == ast.TokMinus {
		neg, arg = true, un.X
		for {
			p, ok := arg.(*ast.ParenExpr)
			if !ok {
				break
			}
			arg = p.X
		}
	}
	var v uint64
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind == ast.TokFloat {
			return to == types.F32 && floatLitFitsF32(a.Raw)
		}
		if a.Kind != ast.TokInt {
			return false
		}
		x, ok := parseIntLit(a.Raw)
		if !ok {
			return false
		}
		v = x
	case *ast.Ident, *ast.FieldExpr:
		// A named const is judged by its value (`u64(PageCount)`), and so
		// is another module's, named qualified (`i8(m.C)`), a C import's
		// macro or enum constant among them. A same-module const has no
		// value before its evaluation; it is accepted here rather than
		// reported falsely.
		r := u.ref(a)
		if r.Kind != resolve.RefItem {
			return false
		}
		it := u.c.cfg.Items.Get(types.ItemID(r.A))
		d, ok := it.Decl.(*types.ConstDecl)
		// A const of an enum type is judged by type, as its tag is
		// (`i8(W.Hi)`): the fold gives an enum no integer value.
		if !ok || u.reprEnum(from) != types.NoType {
			return false
		}
		if u.isFloat(from) {
			return to == types.F32 && u.floatConstFitsF32(it, a.Span())
		}
		var x int64
		// wide is an unsigned value past the int64 range (a u64 or uint
		// above MaxInt64): its magnitude is x's bits, never a negative x.
		wide := false
		if d.HasValue {
			if x, ok = u.c.constInt(d.Value); !ok {
				return false
			}
			wide = x < 0 && !u.tab.Get(u.c.cfg.Consts.Get(d.Value).Type).Signed
		} else {
			// A same-module const whose value the driver has not written
			// yet: folded here when its initializer folds (fold.go), else
			// requested at StageSignatures like any early const. Only a
			// failed request is accepted: E0908 is then
			// reported at the use, any other failure is the const's own.
			f := u.constFolder()
			fv, good, _ := f.fold(a)
			switch {
			case !good || fv.isBool:
				return true
			case fv.n.IsInt64():
				x = fv.n.Int64()
			case fv.n.IsUint64():
				x, wide = int64(fv.n.Uint64()), true
			default:
				return true
			}
		}
		switch {
		case wide:
			v = uint64(x)
		case x < 0:
			neg, v = !neg, uint64(-x)
		default:
			v = uint64(x)
		}
	case *ast.BinaryExpr, *ast.UnaryExpr:
		// Operators over constants are a constant: judged
		// by the value check folds; one naming a const whose value is not
		// known yet is accepted rather than reported falsely. An operator
		// over float consts is not judged: the fold has no float value.
		if u.isFloat(from) || !u.m.constShaped(u.fx, arg) {
			return false
		}
		f := u.constFolder()
		fv, good, _ := f.fold(arg)
		if !good {
			return true
		}
		if fv.isBool || !fv.n.IsInt64() {
			return false
		}
		x := fv.n.Int64()
		if x < 0 {
			neg, v = !neg, uint64(-x)
		} else {
			v = uint64(x)
		}
	default:
		return false
	}
	switch {
	case u.isInt(to):
		b, s := u.intInfo(to)
		return intFits(v, neg, b, s)
	case to == types.F32:
		return v <= 1<<24
	case to == types.F64:
		return v <= 1<<53
	case to == types.Char:
		return !neg && (v < 0xD800 || (v > 0xDFFF && v <= 0x10FFFF))
	}
	return false
}

// floatConstFitsF32 judges `f32(K)` of a named float const K by its value,
// requested as a const argument's is (constValueArg): it fits when the
// value is exactly an f32. A const whose value cannot be had is accepted,
// as an integer one is: its own evaluation, or E0908 at the use, says why.
func (u *unit) floatConstFitsF32(it *types.Item, at source.Span) bool {
	a, ok := u.m.constValueArg(u.x, it, at)
	if !ok || a.Kind != types.CAValue {
		return true
	}
	v := u.c.cfg.Consts.Get(a.Value)
	if v.Type != types.F64 || len(v.Bytes) != 8 {
		return false
	}
	f := math.Float64frombits(types.LoadUint(v.Bytes, 8))
	return f != f || float64(float32(f)) == f
}
