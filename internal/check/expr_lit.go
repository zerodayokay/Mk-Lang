// Why: values that are not calls or operators (member values, records,
// arrays, repeats, fn literals, indexing, slicing, generic fn values) each
// have their own typing rule, and none may leave a Poison without a
// diagnostic.
//
// What: fieldExpr and staticValue, genericFnValue, genericValue and
// memberGenericValue (E0711), recordLit, arrayLit and repeatLit, fnLit and
// expectedFlows, index, slice, elemOf and sliceElemOf.
//
// How: each form is typed against the expected type where one exists, but
// a generic fn value takes its arguments only from what is written. An
// undecided bracket is decided by resolve's rule: after a type it is
// generic arguments, after anything else an index.

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// fieldExpr types `X.Name` in value position: an item of a module, a
// variant or Type.Name function of a type, or a field of a value.
func (u *unit) fieldExpr(fe *ast.FieldExpr, want types.TypeID) types.TypeID {
	switch u.ref(fe).Kind {
	case resolve.RefItem:
		return u.name(fe, want)
	case resolve.RefPoison:
		return types.Poison
	}
	if fe.Name == nil {
		return types.Poison
	}
	if h, ok := u.typeHead(fe.X); ok {
		u.setType(fe.X, types.Poison)
		return u.staticValue(fe, h, want)
	}
	return u.field(fe)
}

// staticValue is `T.Name` as a value: a payload-free variant (UseVariant;
// a generic enum's arguments come from the expected type), or a
// non-generic Type.Name function as a fn value.
func (u *unit) staticValue(fe *ast.FieldExpr, h head, want types.TypeID) types.TypeID {
	if h.t == types.Poison {
		return types.Poison
	}
	mb, ok := u.memberOf(h.t, fe.Name)
	if !ok {
		return types.Poison
	}
	switch mb.Kind {
	case MemberVariant:
		d, ok := u.c.declOfItem(h.it).(*types.EnumDecl)
		if !ok || int(mb.Field) >= len(d.Variants) {
			return types.Poison
		}
		v := d.Variants[mb.Field]
		if len(v.Fields) > 0 {
			var names []string
			for _, f := range v.Fields {
				names = append(names, u.c.cfg.Interner.Str(f.Name))
			}
			u.err(diag.E0305, fe.Span(), "`"+u.c.cfg.Interner.Str(v.Name)+"` carries fields; construct it with them").Fact("missing", diag.TextList(names...))
			return types.Poison
		}
		in := u.newInfer(d.Generics, types.NoType)
		if ty := u.tab.Get(h.t); !h.open && ty.Kind == types.KNamed {
			in.preset(0, ty.Args)
			in.checked = append([]bool(nil), in.bound...)
		}
		self := u.tab.Named(h.it.ID, identityArgs(u.tab, d.Generics))
		if want != types.NoType && want != types.Poison {
			in.unify(in.at(self), want)
		}
		inst, ok := in.finish(fe.Name.Span(), fe.Span())
		*u.b.use(fe.ID()) = Use{Kind: UseVariant, Item: h.it.ID, Index: mb.Field, Inst: inst}
		if !ok {
			return types.Poison
		}
		return in.final(self)
	case MemberFunc, MemberImplMethod:
		it := u.c.cfg.Items.Get(mb.Item)
		d, ok := u.c.declOfItem(it).(*types.FuncDecl)
		if !ok || sigHasPoison(&d.Sig) {
			return types.Poison
		}
		*u.b.use(fe.ID()) = Use{Kind: UseItem, Item: it.ID}
		u.genUse(fe, &d.Sig, "fn value")
		u.cpuUse(fe, &d.Sig, true) // cpu-value
		u.rawNeedsSSE(fe, it.ID)   // E1106
		u.nakedValue(fe, d)        // naked-value
		u.unsafeValue(fe, it.ID)
		if u.constParamFnValue(fe, &d.Sig) {
			return types.Poison
		}
		if len(d.Sig.Generics) > 0 {
			// The head's written arguments are the owner's, as a call's
			// (callStatic): `P[int].Id` is the instance over int.
			var pre []types.GenericArg
			if ty := u.tab.Get(h.t); !h.open && mb.Kind == MemberFunc && ty.Kind == types.KNamed {
				pre = ty.Args
			}
			return u.genericFnValue(fe, it, &d.Sig, pre, want)
		}
		u.valueRequires(fe, &d.Sig, 0) // a clause over consts, as at a call
		return u.fnTypeOf(&d.Sig)
	case MemberField:
		u.typeFieldMiss(fe, h.t)
	}
	return types.Poison
}

// notValue is E0301 for a name in value position that names a type, an
// interface or another non-value item: Poison, never silently
// (lower's invariant; FuzzLower's `scope b := Scope {}`).
func (u *unit) notValue(e ast.Expr, kind string) types.TypeID {
	var b strings.Builder
	if ix, ok := e.(*ast.IndexExpr); ok {
		u.c.exprText(&b, ix.X) // `Slice[int]` names `Slice`
	} else {
		u.c.exprText(&b, e)
	}
	name := b.String()
	u.err(diag.E0301, e.Span(), "`"+name+"` is a "+kind+", not a value").Text("expected", "value").Text("found", kind)
	return types.Poison
}

// bareError is an error item written as a value: a payload-free error
// (UseError), of the set of that one error; one with fields must be
// constructed (E0305).
func (u *unit) bareError(e ast.Expr, it *types.Item, d *types.ErrorDecl) types.TypeID {
	if len(d.Fields) > 0 {
		var names []string
		for _, f := range d.Fields {
			names = append(names, u.c.cfg.Interner.Str(f.Name))
		}
		u.err(diag.E0305, e.Span(), "`"+u.c.cfg.Interner.Str(it.Name)+"` carries fields; construct it with them").Fact("missing", diag.TextList(names...))
		return types.Poison
	}
	*u.b.use(e.ID()) = Use{Kind: UseError, Item: it.ID}
	return u.tab.ErrorSetType(u.tab.ErrorSet([]types.ErrorMember{{Item: it.ID}}))
}

// genericFnValue is a generic function used as a fn value: its arguments
// come from pre (a closed head's written arguments, from slot 0, whose
// bounds its type expression checked), never from the expected type:
// an argument not written is E0711. The
// instance meets the implicit bounds as a call's does.
func (u *unit) genericFnValue(e ast.Expr, it *types.Item, s *types.Signature, pre []types.GenericArg, want types.TypeID) types.TypeID {
	in := u.newInfer(s.Generics, types.NoType)
	if len(pre) > 0 {
		in.preset(0, pre)
		in.checked = append([]bool(nil), in.bound...)
	}
	ft := u.fnTypeOf(s)
	if want == types.Poison {
		return types.Poison
	}
	if i := in.firstUnbound(); i >= 0 {
		u.valueNotInferred(e, s, pre, i, ft, want)
		in.poisonUnbound()
	}
	inst, ok := in.finish(e.Span(), e.Span())
	*u.b.use(e.ID()) = Use{Kind: UseItem, Item: it.ID, Inst: inst}
	if !ok {
		return types.Poison
	}
	u.implicitBounds(it.ID, e.Span(), inst, len(pre) == 0)
	u.simdValueRules(e, it.ID, inst) // the SIMD rules for a value
	u.valueRequires(e, s, inst)      // the callee's requires, as at a call
	return u.instFlows(in.final(ft))
}

// valueNotInferred is the E0711 for a generic
// function named as a value with generic slot first not written: e is the
// Ident or the Type.Name FieldExpr. It is anchored at the name the missing
// arguments belong after: the owner's for an owner generic (`Holder` in
// `Holder.Get`), else the function's. When the expected fn type would fix
// every argument, the fix inserting them is proven.
func (u *unit) valueNotInferred(e ast.Expr, s *types.Signature, pre []types.GenericArg, first int, ft, want types.TypeID) {
	owners := len(s.Generics) - int(s.OwnGenerics)
	name, at := e, e.Span()
	if fe, ok := e.(*ast.FieldExpr); ok {
		name, at = fe.Name, fe.Name.Span()
		if first < owners {
			at = fe.X.Span()
		}
	}
	shown := u.c.cfg.Interner.Str(s.Name)
	var b strings.Builder
	if fe, ok := e.(*ast.FieldExpr); ok && first < owners {
		u.c.exprText(&b, fe.X)
		shown = b.String()
	} else {
		u.c.exprText(&b, name)
		shown = b.String()
	}
	pn := u.c.cfg.Interner.Str(s.Generics[first].Name)
	d := u.err(diag.E0711, at, "`"+shown+"` is generic: a value names its type arguments, `"+shown+"["+pn+"]`; they are never inferred for a value").
		Text("param", pn)
	// The proven fix: the expected type fixes every missing argument of one
	// bracket list (the owner's or the function's own).
	from, to := owners, len(s.Generics)
	if first < owners {
		from, to = 0, owners
	}
	if want == types.NoType || u.tab.Kind(want) != types.KFn {
		d.Alt("write the type arguments in brackets")
		return
	}
	sc := u.newInfer(s.Generics, types.NoType)
	sc.preset(0, pre)
	sc.unify(sc.at(ft), want)
	var args []string
	for i := range s.Generics {
		if !sc.bound[i] || s.Generics[i].Const || sc.binds[i].Type == types.Poison || u.c.hasHole(sc.binds[i].Type) {
			d.Alt("write the type arguments in brackets")
			return
		}
		if i >= from && i < to {
			args = append(args, u.str(sc.binds[i].Type))
		}
	}
	d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: source.Span{File: at.File, Start: at.End, End: at.End}, Text: "[" + strings.Join(args, ", ") + "]"}},
		Why: "the expected type fixes the arguments"})
}

// instFlows is a generic function instance's fn type with the flows its
// type arguments make vacuous dropped: a result source on a result or a
// parameter that is no carrier once substituted has no effect, as a
// written `from x` on one has none in a signature or
// a `fn` type (signatureFlows). Without it `Id[T](x: T): T` as a value of
// `fn(int): int` would keep `from #1`, which no `fn` type over int can
// declare, so it would be E0608.
func (u *unit) instFlows(t types.TypeID) types.TypeID {
	ty := u.tab.Get(t)
	if ty.Kind != types.KFn || len(ty.Fn.Sources) == 0 {
		return t
	}
	ft := *ty.Fn
	resCarrier := u.c.isCarrier(ft.Result)
	kept := func(s types.Source) bool {
		return resCarrier && int(s.Param) >= 1 && int(s.Param) <= len(ft.Params) && u.c.isCarrier(ft.Params[s.Param-1].Type)
	}
	var keep []types.Source
	for _, s := range ft.Sources {
		if kept(s) {
			keep = append(keep, s)
		}
	}
	if len(keep) == len(ft.Sources) {
		return t
	}
	ft.Sources = keep
	// A per-field clause loses the same sources field by field. A
	// field left with none keeps its entry and holds nothing, as
	// perFieldFlows keeps a named field whose sources are no carriers: the
	// instance and the same signature written out are one fn type, where
	// dropping the entry would read the field as the union.
	var fields []types.FieldSources
	for _, f := range ft.Fields {
		var fk []types.Source
		for _, s := range f.Sources {
			if kept(s) {
				fk = append(fk, s)
			}
		}
		fields = append(fields, types.FieldSources{Field: f.Field, Sources: fk})
	}
	ft.Fields = fields
	return u.tab.Fn(ft)
}

// recordLit types `(a: x, b)`: a bare name is `b: b`; under
// an expected record the fields take their types by name.
func (u *unit) recordLit(e *ast.RecordLit, want types.TypeID) types.TypeID {
	var exp []types.RecordField
	if want != types.NoType && want != types.Poison && u.tab.Kind(want) == types.KRecord {
		exp = u.tab.Get(want).Fields
	}
	var fs []types.RecordField
	poison := false
	for _, f := range e.Fields {
		if f == nil || f.Name == nil {
			poison = true
			continue
		}
		w := types.NoType
		for _, x := range exp {
			if x.Name == f.Name.Name {
				w = x.Type
			}
		}
		var t types.TypeID
		if f.Value != nil {
			t = u.expr(f.Value, w)
		} else {
			u.punAt = f.Name
			t = u.expr(f.Name, w)
			u.punAt = nil
		}
		if f.Value != nil {
			u.scopeEscapes(f.Value, t)
		} else {
			u.scopeEscapes(f.Name, t)
		}
		if t == types.Poison {
			poison = true
		}
		fs = appendField(fs, f.Name.Name, t)
	}
	if poison {
		return types.Poison
	}
	return u.tab.Record(fs)
}

// arrayLit types `[e1, e2]`: `[n]T` with T from the expected array
// or the first element; `[]` needs an expected `[0]T` (E0711). In a
// const initializer, an expected `[]T` makes the literal a `[]T` over
// static data and `[]` the empty one (a static slice); everywhere else an
// expected `[]T` stays E0301 (or E0711 for `[]`), the mismatch expr
// reports.
func (u *unit) arrayLit(e *ast.ArrayLit, want types.TypeID) types.TypeID {
	elem := types.NoType
	static := false
	if want != types.NoType && want != types.Poison {
		switch u.tab.Kind(want) {
		case types.KArray:
			elem = u.tab.Get(want).Elem
		case types.KSlice:
			if u.constInit() {
				elem, static = u.tab.Get(want).Elem, true
			}
		}
	}
	if len(e.Elems) == 0 {
		if elem == types.NoType {
			if want != types.Poison {
				u.err(diag.E0711, e.Span(), "`[]` needs an expected array type").Text("param", "T")
			}
			return types.Poison
		}
		if static {
			return want
		}
		return u.tab.Array(u.c.intArg(0), elem)
	}
	if static {
		for _, x := range e.Elems {
			u.expr(x, elem)
		}
		return want
	}
	for _, x := range e.Elems {
		t := u.expr(x, elem)
		if elem == types.NoType {
			elem = t
		}
	}
	if elem == types.Poison {
		return types.Poison
	}
	return u.tab.Array(u.c.intArg(int64(len(e.Elems))), elem)
}

// constInit reports whether u checks the initializer of a const, where an
// array literal against `[]T` is a static slice. A fn literal in
// the initializer is a unit of its own, so the bodies an evaluation runs
// never see the rule; a var global's initializer is not a const's.
func (u *unit) constInit() bool { return constValueNode(u) != nil }

// repeatLit types `[e; n]`: n a const int of at least 0, e
// Plain (E0408 otherwise). A count whose value check cannot give is
// Poison here with no diagnostic of the count's: a named const's failed
// evaluation is reported at the const, and the body lists it in
// PoisonReads. Before initEarly (a declaration's type in step 4) a
// same-module early const cannot be evaluated; typeConsts types such a
// const again (untypeProvisional).
func (u *unit) repeatLit(e *ast.RepeatLit, want types.TypeID) types.TypeID {
	elem := types.NoType
	if want != types.NoType && want != types.Poison && u.tab.Kind(want) == types.KArray {
		elem = u.tab.Get(want).Elem
	}
	t := u.expr(e.Value, elem)
	u.setType(e.Count, types.Int)
	n, ok := u.x.constArg(e.Count, types.Int)
	if t == types.Poison || !ok {
		return types.Poison
	}
	if d := u.c.negativeLen(e.Count.Span(), n); d != nil {
		u.b.Diags = append(u.b.Diags, d)
		return types.Poison
	}
	if !u.c.satisfies(u.env, t, types.Bound{Kind: types.BoundPlain}, u.x.rec) {
		u.err(diag.E0408, e.Value.Span(), "`[e; n]` copies e, so `"+u.str(t)+"` must be Plain").Text("reason", "repeat").Text("type", u.str(t))
	}
	return u.tab.Array(n, t)
}

// fnLit is a fn literal's fn type; its body is its own unit.
// Written where a fn type is expected, the literal takes that type's flows
// as its own: borrow checks its body against them,
// and its fn type carries them.
func (u *unit) fnLit(e *ast.FnLit, want types.TypeID) types.TypeID {
	r := u.ref(e)
	if r.Kind != resolve.RefItem {
		return types.Poison
	}
	it := u.c.cfg.Items.Get(types.ItemID(r.A))
	d, ok := it.Decl.(*types.FuncDecl)
	if !ok || sigHasPoison(&d.Sig) {
		return types.Poison
	}
	*u.b.use(e.ID()) = Use{Kind: UseItem, Item: it.ID}
	u.expectedFlows(&d.Sig, want)
	if ii := u.m.byItem[it.ID]; ii != nil && len(ii.Captures) > 0 {
		u.literalCaptures(e, it, ii)
	}
	if d.Sig.Flows.Ambiguous {
		u.literalE0601(it.ID)
	}
	return u.fnTypeOf(&d.Sig)
}

// literalE0601 reports the literal's held E0601 (runFlows) in the enclosing
// unit: no expected fn type gave the literal its sources. A literal writes
// no clause, so the `from` alternatives a function gets are replaced by
// the two ways a literal can say it.
func (u *unit) literalE0601(lit types.ItemID) {
	for _, d := range u.m.litE0601[lit] {
		c := *d
		c.Alternatives = []string{"pass it where a fn type that says `from` is expected", "make it a named function with `from`"}
		u.b.Diags = append(u.b.Diags, &c)
	}
}

// expectedFlows gives a literal's signature s the flows of the expected fn
// type want, when want is a fn type of as many parameters and s
// has none of its own (the grammar gives a literal no clause): its stores,
// ends and intos, and its result sources, which settle a literal
// whose own sources would be ambiguous (E0601). Only the enclosing body's
// unit reaches its literal, so the write races nothing.
func (u *unit) expectedFlows(s *types.Signature, want types.TypeID) {
	if want == types.NoType || u.tab.Kind(want) != types.KFn || s.Flows.Declared {
		return
	}
	ft := u.tab.Get(want).Fn
	if len(ft.Params) != len(s.Params) {
		return
	}
	if len(ft.Sources) > 0 && sameParamTypes(ft, s) {
		s.Flows.Sources = append([]types.Source(nil), ft.Sources...)
		// A per-field clause counts among the type's result sources too,
		// so the body is checked field by field against it and the
		// literal's fn type is the target's.
		s.Flows.Fields = types.CloneFieldSources(ft.Fields)
		s.Flows.Ambiguous = false
	}
	if len(ft.Stores) == 0 && len(ft.Ends) == 0 && !anyInto(ft.Params) {
		return
	}
	for i := range s.Params {
		if s.Params[i].Into == types.NoParam {
			s.Params[i].Into = ft.Params[i].Into
		}
	}
	s.Flows.Stores = append([]types.Store(nil), ft.Stores...)
	s.Flows.Ends = append([]types.ParamIndex(nil), ft.Ends...)
}

// sameParamTypes reports whether a literal's signature s has the result and
// parameter types of fn type ft, so ft's sources name the same places.
func sameParamTypes(ft *types.FnType, s *types.Signature) bool {
	if ft.Result != s.Result {
		return false
	}
	for i, p := range ft.Params {
		if p.Type != s.Params[i].Type {
			return false
		}
	}
	return true
}

// anyInto reports whether some parameter of a fn type flows `into` another.
func anyInto(ps []types.FnParam) bool {
	for _, p := range ps {
		if p.Into != types.NoParam {
			return true
		}
	}
	return false
}

// elemOf is the element type xs[i] reads: of []T, mut []T and
// [N]T, through any & and mut layers (Adjust.Deref on xs).
func (u *unit) elemOf(x ast.Expr, xt types.TypeID, at source.Span, op string) (types.TypeID, types.TypeKind, bool) {
	v, n := u.deref(xt)
	if n > 0 {
		*u.b.adjust(x.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n)}
	}
	vt := u.tab.Get(v)
	switch vt.Kind {
	case types.KSlice, types.KMutSlice, types.KArray:
		return vt.Elem, vt.Kind, true
	case types.KStr:
		return types.U8, vt.Kind, true
	case types.KNamed:
		if elem, ok := u.sliceElemOf(v); ok {
			return elem, types.KSlice, true
		}
	case types.KPoison:
		return types.Poison, vt.Kind, false
	case types.KRawPtr:
		// Indexing does not dereference a raw pointer: E0308, with the
		// dereference written out, never a silent Poison (lower's
		// invariant; found beside FuzzNoSilentPoison's `(c.Load)`).
		var b strings.Builder
		u.c.exprText(&b, x)
		u.err(diag.E0308, at, "the raw pointer `"+u.str(v)+"` has no "+op+" operator; dereference it first").Text("operator", op).Text("type", u.str(v)).
			Alt("write `(*" + b.String() + ")[..]` inside an `unsafe \"reason\" { }` block")
		return types.Poison, vt.Kind, false
	case types.KParam:
		u.needsBound(at, v, op)
		return types.Poison, vt.Kind, false
	}
	d := u.err(diag.E0308, at, "`"+u.str(v)+"` cannot be "+op+"ed").Text("operator", op).Text("type", u.str(v))
	if id, ok := x.(*ast.Ident); ok {
		if r := u.ref(id); r.Kind == resolve.RefItem {
			u.c.hidesPreludeType(d, u.c.cfg.Items.Get(types.ItemID(r.A)))
		}
	}
	return types.Poison, vt.Kind, false
}

// intIndex checks an index or bound: any integer type, E0301
// otherwise; it has no expected type.
func (u *unit) intIndex(e ast.Expr) {
	if e == nil {
		return
	}
	t := u.expr(e, types.NoType)
	if t != types.Poison && !u.isInt(t) {
		u.err(diag.E0301, e.Span(), "an index is an integer, found `"+u.str(t)+"`").Text("expected", "an integer type").Text("found", u.str(t))
	}
}

// index types `xs[i]`; an IndexExpr read as generic arguments in
// value position (a generic function named with its arguments) is the fn
// value of that instance. An undecided bracket (resolve's ReadUndecided:
// its head is a member, `self.items[i]`, `sys.args[1]`) is decided here by
// resolve's own rule: a head that names a type
// takes generic arguments, any other head is a value and is indexed.
func (u *unit) index(e *ast.IndexExpr, want types.TypeID) types.TypeID {
	switch u.x.reading(e) {
	case resolve.ReadIndex:
	case resolve.ReadUndecided:
		if _, isType := u.typeHead(e.X); isType {
			return u.genericValue(e, want)
		}
		if t, ok := u.memberGenericValue(e); ok {
			return t
		}
	default:
		return u.genericValue(e, want)
	}
	if len(e.Args) != 1 {
		// `xs[0, 5]`: an index takes one value;
		// never a silent Poison.
		xt := u.expr(e.X, types.NoType)
		for _, a := range e.Args {
			if ax, ok := a.(ast.Expr); ok && ax != nil {
				u.expr(ax, types.NoType)
			}
		}
		if xt != types.Poison {
			u.err(diag.E0304, e.Span(), "an index takes one value").Text("signature", "x[i]").
				Int("expected", 1).Int("found", int64(len(e.Args))).Alt("x[a..b]")
		}
		return types.Poison
	}
	xt := u.expr(e.X, types.NoType)
	idx, _ := e.Args[0].(ast.Expr)
	if idx == nil {
		// A type in an index (`xs[[]int]`, the fuzz input
		// `F[T](x:T){0[[]x](0)}`): E0301, as `xs[int]` is, never a
		// silent Poison, which lower turns into an ICE.
		if xt != types.Poison && e.Args[0] != nil {
			u.err(diag.E0301, e.Args[0].Span(), "an index is a value, not a type").Text("expected", "value").Text("found", "type")
		}
		return types.Poison
	}
	if xt == types.Poison {
		u.expr(idx, types.NoType)
		return types.Poison
	}
	if v, _ := u.deref(xt); u.tab.Kind(v) == types.KStr {
		u.expr(idx, types.NoType)
		u.err(diag.E0308, e.Span(), "a `str` is sliced, not indexed").Text("operator", "index").Text("type", "str").Alt("s.Byte(i)")
		return types.Poison
	}
	elem, _, ok := u.elemOf(e.X, xt, e.Span(), "index")
	u.intIndex(idx)
	if !ok {
		return types.Poison
	}
	return elem
}

// genericValue is `F[A]` in value position: a generic function's fn value
// with its arguments written. A type or an interface with arguments is no
// value (E0301, as the bare name is): `Slice[int].Fill(..)` and
// `P[int]` never become a silent Poison, which lower turns into an ICE.
// An interface is no namespace head (typeHead), so its
// functions are reached through a value's method call only.
func (u *unit) genericValue(e *ast.IndexExpr, want types.TypeID) types.TypeID {
	if u.x.reading(e) != resolve.ReadGeneric {
		// index sends an undecided bracket here when its head names a type;
		// resolve decides a module-path head itself (`lib.P[int]` and
		// `lib.Get[int]` are ReadGeneric, TestE0301_ForeignHeadNotValue),
		// so this return is not known to be reached. Not decided here.
		return types.Poison
	}
	r := u.ref(e.X)
	if r.Kind != resolve.RefItem {
		// resolve reads a bracket as generic arguments only after a head
		// that resolved to an item; a head it
		// could not resolve reported there.
		return types.Poison
	}
	it := u.c.cfg.Items.Get(types.ItemID(r.A))
	switch it.Kind {
	case types.KindStruct, types.KindEnum, types.KindAlias, types.KindInterface:
		u.setType(e.X, types.Poison)
		return u.notValue(e, it.Kind.String())
	}
	d, ok := u.c.declOfItem(it).(*types.FuncDecl)
	if !ok || sigHasPoison(&d.Sig) {
		return types.Poison // a value item whose declaration failed reported there
	}
	u.setType(e.X, types.Poison)
	s := &d.Sig
	in := u.newInfer(s.Generics, types.NoType)
	own := int(s.OwnGenerics)
	u.explicitArgs(in, e, s.Generics[len(s.Generics)-own:], len(s.Generics)-own)
	u.judgePending(in) // nothing binds a value's generics later
	ft := u.fnTypeOf(s)
	// Never from the expected type. A wrong count was E0304 in
	// explicitArgs, so the slots it left are poison, not a second E0711.
	if len(e.Args) != own {
		in.poisonUnbound()
	}
	inst, ok := in.finish(e.X.Span(), e.Span())
	*u.b.use(e.ID()) = Use{Kind: UseItem, Item: it.ID, Inst: inst}
	u.genUse(e, s, "fn value")
	u.cpuUse(e, s, true) // cpu-value
	u.nakedValue(e, d)   // naked-value
	u.unsafeValue(e, it.ID)
	if !ok || u.constParamFnValue(e, s) {
		return types.Poison
	}
	u.implicitBounds(it.ID, e.Span(), inst, true)
	u.simdValueRules(e, it.ID, inst) // the SIMD rules for a value
	u.valueRequires(e, s, inst)      // the callee's requires, as at a call
	return u.instFlows(in.final(ft))
}

// memberGenericValue is `Type[A].M[B]` in value position:
// a bracket after a Type.Name member that is a function with generics of
// its own gives those, as `F[A]` gives a free function's. ok is false when
// e is no such form, so it is an index; the head's arguments are the
// owner's, as a call's (callStatic), and an owner argument not
// written is E0711.
func (u *unit) memberGenericValue(e *ast.IndexExpr) (types.TypeID, bool) {
	fe, ok := e.X.(*ast.FieldExpr)
	if !ok {
		return types.NoType, false
	}
	h, ok := u.typeHead(fe.X)
	if !ok || h.t == types.Poison {
		return types.NoType, false
	}
	mb, ok := u.memberOf(h.t, fe.Name)
	if !ok {
		u.setType(fe.X, types.Poison)
		u.setType(fe, types.Poison)
		for _, a := range e.Args {
			if ax, ok := a.(ast.Expr); ok && ax != nil {
				u.setType(ax, types.Poison)
			}
		}
		return types.Poison, true
	}
	if mb.Kind != MemberFunc && mb.Kind != MemberImplMethod {
		return types.NoType, false
	}
	it := u.c.cfg.Items.Get(mb.Item)
	d, ok := u.c.declOfItem(it).(*types.FuncDecl)
	if !ok || d.Sig.OwnGenerics == 0 {
		return types.NoType, false
	}
	u.setType(fe.X, types.Poison)
	u.setType(fe, types.Poison)
	s := &d.Sig
	if sigHasPoison(s) {
		return types.Poison, true
	}
	in := u.newInfer(s.Generics, types.NoType)
	var pre []types.GenericArg
	if ty := u.tab.Get(h.t); !h.open && mb.Kind == MemberFunc && ty.Kind == types.KNamed {
		pre = ty.Args
		in.preset(0, pre)
		in.checked = append([]bool(nil), in.bound...)
	}
	own := int(s.OwnGenerics)
	u.explicitArgs(in, e, s.Generics[len(s.Generics)-own:], len(s.Generics)-own)
	u.judgePending(in) // nothing binds a value's generics later
	ft := u.fnTypeOf(s)
	if len(e.Args) != own {
		in.poisonUnbound()
	} else if i := in.firstUnbound(); i >= 0 {
		u.valueNotInferred(fe, s, pre, i, ft, types.NoType)
		in.poisonUnbound()
	}
	inst, ok := in.finish(fe.Name.Span(), e.Span())
	*u.b.use(e.ID()) = Use{Kind: UseItem, Item: it.ID, Inst: inst}
	u.genUse(e, s, "fn value")
	u.cpuUse(e, s, true) // cpu-value
	u.nakedValue(e, d)   // naked-value
	u.unsafeValue(e, it.ID)
	if !ok || u.constParamFnValue(e, s) {
		return types.Poison, true
	}
	u.implicitBounds(it.ID, e.Span(), inst, len(pre) == 0)
	u.simdValueRules(e, it.ID, inst) // the SIMD rules for a value
	u.valueRequires(e, s, inst)      // the callee's requires, as at a call
	return u.instFlows(in.final(ft)), true
}

// slice types `xs[a..b]`: `[]T` of a slice or array, `str` of a
// str; `mut xs[a..b]` is the unary mut's.
func (u *unit) slice(e *ast.SliceExpr) types.TypeID {
	xt := u.expr(e.X, types.NoType)
	u.intIndex(e.Lo)
	u.intIndex(e.Hi)
	if xt == types.Poison {
		return types.Poison
	}
	elem, kind, ok := u.elemOf(e.X, xt, e.Span(), "slice")
	if !ok {
		return types.Poison
	}
	if kind == types.KStr {
		return types.Str
	}
	return u.tab.Slice(elem)
}

// sliceElemOf is T when v's constructor implements Slice[T]:
// the impl is in the constructor's module or in Slice's, and its
// target is matched against v. It records ReadImpl(Slice, v) when it finds
// an impl and ReadNoImpl(Slice, v) when it finds none,
// so adding a Slice impl rechecks a unit that could not index or iterate v
// (the driver's implHash prefix).
func (u *unit) sliceElemOf(v types.TypeID) (types.TypeID, bool) {
	c := u.c
	if c.known.Slice == types.NoItem {
		return types.NoType, false
	}
	ck, ok := c.ctorOf(v)
	if !ok {
		return types.NoType, false
	}
	sliceKey := c.cfg.Items.Get(c.known.Slice).Key
	for _, mid := range []types.ModuleID{c.ctorModule(ck), c.cfg.Items.Get(c.known.Slice).Module} {
		om := c.module(mid)
		if om == nil || !om.done.Load() && om != u.m {
			continue
		}
		impl, ok := om.implTab[implKey{iface: c.known.Slice, ctor: ck}]
		if !ok {
			continue
		}
		u.x.rec.impl(sliceKey, u.str(v))
		d, ok := c.cfg.Items.Get(impl).Decl.(*types.ImplDecl)
		if !ok || len(d.Bound.Args) != 1 || d.Bound.Args[0].IsConst {
			return types.NoType, false
		}
		in := u.newInfer(d.Generics, types.NoType)
		if !in.unify(in.at(d.For), v) {
			return types.NoType, false
		}
		return in.final(d.Bound.Args[0].Type), true
	}
	u.x.rec.noImpl(sliceKey, u.str(v))
	return types.NoType, false
}
