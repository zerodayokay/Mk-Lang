// Why: every written type (a parameter, a field, a cast, a bracket argument)
// must become a types.TypeID before anything can be checked against it.
//
// What: tctx, the context of one type expression, with typeOf (AST type
// nodes and bracket-argument covers to TypeIDs), named and genericArgs
// (generic arguments, const arguments in types), alias (alias expansion),
// errorUnion and errorSet, fnType (fn types with their flows),
// scopeFamily, negativeInstance and clauses.
//
// How: a recursive walk over the AST type node; tpos says whether void or
// never may stand in a position. A node under an earlier error is Poison,
// silently. Type arguments are read before const ones, so a const generic
// typed by a type parameter is judged at that type's argument.

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// tctx is where a type expression is read: the item whose generics a
// RefTypeParam indexes, what Self means there, and whose reads it records.
type tctx struct {
	m    *Module
	fx   *fileIndex
	item *types.Item
	// self is the type Self names: SelfType() in an interface, the impl's
	// For type in an impl, the type a Type.Name method belongs to.
	self types.TypeID
	// gens are the generics in scope (for messages and const parameters).
	gens []types.GenericParam
	rec  recorder
	// sink, when set, receives the diagnostics instead of the module (a
	// stage-6 unit, which runs in parallel with others); bound checks then
	// run at once, since facts are final in stage 6.
	sink *[]*diag.Diagnostic
	// unitEnv, set in a stage-6 unit, is the unit's current Env, so a type
	// expression's bounds are judged under the enclosing `if T is X`
	// narrowing (`var a: raw.Atomic[T]` in `if T is u32` is no E0701).
	unitEnv func() *Env
	// fmtMisuse, set in a stage-6 unit of the Print shape, reports the
	// E0705 of argsMisuse for a type argument that needs the record's Fields[Format].
	fmtMisuse func(at source.Span, t types.TypeID, b types.Bound) bool
	// constParam names the const parameter whose argument constArg reads,
	// for E0705's fact param ("" for an array length or repeat count).
	constParam string
	// poisoned, set in a unit, records a same-module const whose value a
	// use request at the span asked for and got poison with no diagnostic
	// (Body.PoisonReads and PoisonAt). Why is the const's own
	// failure, and accounting finds where it is reported.
	poisoned func(types.ItemID, source.Span)
	// argGens, set in a unit while finiteRequires judges one combination of
	// the caller's finite sets, binds the caller's const generics (by index
	// in its Generics), so a const value argument over them folds
	// (requiresArg: `F(k, t)` in `G[const k: int]`); nil elsewhere.
	argGens map[uint32]fval
}

// emit records diagnostics where this context reports.
func (x *tctx) emit(ds ...*diag.Diagnostic) {
	if x.sink != nil {
		*x.sink = append(*x.sink, ds...)
		return
	}
	x.m.Diags = append(x.m.Diags, ds...)
}

// tpos says which of void and never a position admits.
type tpos uint8

const (
	posPlain  tpos = iota
	posResult      // a function or fn type result: void and never
	posOk          // the T of E!T: void
	posPtr         // the pointee of *T: void
)

func (x *tctx) tab() *types.Table { return x.m.c.cfg.Types }

func (x *tctx) ref(n ast.Node) resolve.Ref {
	id := n.ID()
	if int(id) >= len(x.fx.info.Refs) {
		return resolve.Ref{}
	}
	return x.fx.info.Refs[id]
}

func (x *tctx) reading(n ast.Node) resolve.Reading {
	id := n.ID()
	if int(id) >= len(x.fx.info.Readings) {
		return resolve.ReadingNone
	}
	return x.fx.info.Readings[id]
}

func (x *tctx) errorf(code diag.Code, at source.Span, msg string) *diag.Diagnostic {
	d := diag.New(code, at, msg)
	x.emit(d)
	return d
}

func (x *tctx) str(t types.TypeID) string { return x.tab().TypeString(t, x.gens) }

// typeOf maps a type node, or a bracket argument read as a type, to its
// TypeID. A node under an earlier error is Poison, silently.
func (x *tctx) typeOf(n ast.Node, p tpos) types.TypeID {
	tab := x.tab()
	switch n := n.(type) {
	case nil:
		return types.Poison
	case *ast.ParenType:
		return x.typeOf(n.Type, p)
	case *ast.ParenExpr:
		return x.typeOf(n.X, p)
	case *ast.PathType:
		var args []ast.TypeOrExpr
		if n.Args != nil {
			args = n.Args.Args
		}
		return x.named(n, x.ref(n), args, p)
	case *ast.Ident, *ast.FieldExpr:
		return x.named(n, x.ref(n), nil, p)
	case *ast.IndexExpr:
		head := n.X
		for ie, ok := head.(*ast.IndexExpr); ok; ie, ok = head.(*ast.IndexExpr) {
			head = ie.X
		}
		switch head.(type) {
		case *ast.Ident, *ast.FieldExpr, *ast.ParenExpr, nil:
		default:
			// An index of a value that is no name (`0[0]`, `F()[0]`) read as
			// a type: resolve walked its head as a value and reported
			// nothing (exprRef), so it is E0301 here; never a silent Poison,
			// which lower turns into an ICE (FuzzCheck 77640a14944bb5b4's
			// relatives, `S.G[0[0], 8]`).
			x.errorf(diag.E0301, n.Span(), "an index of a value is not a type").Text("expected", "type").Text("found", "value")
			return types.Poison
		}
		return x.named(n, x.ref(n.X), n.Args, p)
	case *ast.RefType:
		return x.wrap(tab.Ref, x.typeOf(n.Elem, posPlain))
	case *ast.MutType:
		return x.wrap(tab.Mut, x.typeOf(n.Elem, posPlain))
	case *ast.MutSliceType:
		return x.wrap(tab.MutSlice, x.typeOf(n.Elem, posPlain))
	case *ast.SliceType:
		return x.wrap(tab.Slice, x.typeOf(n.Elem, posPlain))
	case *ast.OptionalType:
		return x.wrap(tab.Optional, x.typeOf(n.Elem, posPlain))
	case *ast.RawPtrType:
		return x.wrap(tab.RawPtr, x.typeOf(n.Elem, posPtr))
	case *ast.UnaryExpr:
		switch n.Op {
		case ast.TokAmp:
			return x.wrap(tab.Ref, x.typeOf(n.X, posPlain))
		case ast.TokMut:
			return x.wrap(tab.Mut, x.typeOf(n.X, posPlain))
		case ast.TokStar:
			return x.wrap(tab.RawPtr, x.typeOf(n.X, posPtr))
		}
	case *ast.ArrayType:
		elem := x.typeOf(n.Elem, posPlain)
		ln, ok := x.constArg(n.Len, types.Int)
		if !ok || elem == types.Poison {
			return types.Poison
		}
		if d := x.m.c.negativeLen(n.Len.Span(), ln); d != nil {
			x.emit(d)
			return types.Poison
		}
		return tab.Array(ln, elem)
	case *ast.ErrorType:
		return x.errorUnion(n, n.Errors, n.Ok)
	case *ast.BinaryExpr:
		switch n.Op {
		case ast.TokBang:
			return x.errorUnion(n, n.X, n.Y)
		case ast.TokPipe:
			return x.setType(n)
		}
	case *ast.UnionType:
		return x.setType(n)
	case *ast.RecordType:
		var fs []types.RecordField
		for _, f := range n.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			fs = appendField(fs, f.Name.Name, x.typeOf(f.Type, posPlain))
		}
		return x.record(fs)
	case *ast.RecordLit:
		if len(n.Fields) == 1 && n.Fields[0] != nil && n.Fields[0].Value == nil && n.Fields[0].Name != nil {
			return x.typeOf(n.Fields[0].Name, p) // (T) is T in a bracket
		}
		var fs []types.RecordField
		for _, f := range n.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			var ft types.TypeID
			if f.Value == nil {
				ft = x.typeOf(f.Name, posPlain)
			} else {
				ft = x.typeOf(f.Value, posPlain)
			}
			fs = appendField(fs, f.Name.Name, ft)
		}
		return x.record(fs)
	case *ast.FnType:
		return x.fnType(n)
	case *ast.BadType, *ast.BadExpr:
		return types.Poison
	}
	// Any other expression read as a type: resolve has reported it.
	return types.Poison
}

// wrap applies a one-element constructor, keeping Poison.
func (x *tctx) wrap(f func(types.TypeID) types.TypeID, e types.TypeID) types.TypeID {
	if e == types.Poison {
		return types.Poison
	}
	return f(e)
}

// appendField adds a record field; a repeated name was resolve's E0206 and
// the later field is dropped.
func appendField(fs []types.RecordField, name source.Symbol, t types.TypeID) []types.RecordField {
	for _, f := range fs {
		if f.Name == name {
			return fs
		}
	}
	return append(fs, types.RecordField{Name: name, Type: t})
}

func (x *tctx) record(fs []types.RecordField) types.TypeID {
	for _, f := range fs {
		if f.Type == types.Poison {
			return types.Poison
		}
	}
	return x.tab().Record(fs)
}

// named is a name in a type position, with its bracket arguments: a
// primitive, a type parameter, Self, a struct, enum, error or alias.
func (x *tctx) named(n ast.Node, r resolve.Ref, args []ast.TypeOrExpr, p tpos) types.TypeID {
	tab := x.tab()
	switch r.Kind {
	case resolve.RefPrim:
		t := types.TypeID(r.A)
		if len(args) > 0 {
			x.errorf(diag.E0304, n.Span(), "`"+x.str(t)+"` takes no type arguments").Text("reason", "type-arguments").
				Int("expected", 0).Int("found", int64(len(args)))
			return types.Poison
		}
		switch {
		case t == types.Never && p != posResult:
			x.errorf(diag.E0301, n.Span(), "`never` is only a result type").Text("expected", "a type other than never").Text("found", "never")
			return types.Poison
		case t == types.Void && p == posPlain:
			x.errorf(diag.E0301, n.Span(), "`void` is only a result type, the T of E!void or the pointee of *void").
				Text("expected", "a type other than void").Text("found", "void")
			return types.Poison
		}
		return t
	case resolve.RefTypeParam:
		if int(r.A) < len(x.gens) && x.gens[r.A].Const {
			x.errorf(diag.E0301, n.Span(), "a const parameter is a value, not a type").Text("expected", "type").Text("found", "const parameter")
			return types.Poison
		}
		return tab.Param(r.A)
	case resolve.RefSelfType:
		if x.self == types.NoType {
			return types.Poison
		}
		return x.self
	case resolve.RefItem:
		it := x.m.c.cfg.Items.Get(types.ItemID(r.A))
		switch it.Kind {
		case types.KindStruct, types.KindEnum:
			if it.ID == x.m.c.known.Scope && it.ID != types.NoItem && len(x.m.c.genericsOf(it)) == 0 {
				return x.scopeFamily(n, it, args)
			}
			ga, ok := x.genericArgs(n, it, args)
			if !ok {
				return types.Poison
			}
			x.rec.shape(it.Key)
			return tab.Named(it.ID, ga)
		case types.KindError:
			if len(args) > 0 {
				x.errorf(diag.E0304, n.Span(), "an error takes no type arguments").Text("reason", "type-arguments")
				return types.Poison
			}
			return tab.ErrorSetType(tab.ErrorSet([]types.ErrorMember{{Item: it.ID}}))
		case types.KindAlias:
			return x.alias(n, it, args, p)
		case types.KindInterface:
			name := x.m.c.cfg.Interner.Str(it.Name)
			x.errorf(diag.E0301, n.Span(), "an interface is a bound, not a type").Text("expected", "type").Text("found", "interface "+name).
				Alt("[T: " + name + "]").Alt("gen dyn.Of(" + name + ")")
			return types.Poison
		}
		d := x.errorf(diag.E0301, n.Span(), "`"+x.m.c.cfg.Interner.Str(it.Name)+"` is not a type").Text("expected", "type").Text("found", it.Kind.String())
		if pathLen(n) == 1 {
			x.m.c.hidesPreludeType(d, it)
		}
		return types.Poison
	case resolve.RefUniverse:
		x.errorf(diag.E0301, n.Span(), "`"+x.m.c.cfg.Interner.Str(source.Symbol(r.A))+"` is not a type").Text("expected", "type").Text("found", "universe name")
		return types.Poison
	case resolve.RefLocal, resolve.RefParam, resolve.RefResult, resolve.RefField, resolve.RefCapture:
		// A value in a type position: `a: (h)`, `F(): self.A`, `b: int.A`,
		// or a captured name inside a fn
		// literal (`d: origin`, FuzzCheck d12ab98b); never a silent Poison.
		found := "value"
		if r.Segs > 0 && int(r.Segs) < pathLen(n) {
			found = "member of a value"
		}
		x.errorf(diag.E0301, n.Span(), "`"+x.nodeText(n)+"` is a "+found+", not a type").Text("expected", "type").Text("found", found)
		return types.Poison
	case resolve.RefDeferred:
		// A member path in a type position (`int.A`, `Index.Make`): a type
		// has no type members, so no member is a type.
		x.errorf(diag.E0301, n.Span(), "`"+x.nodeText(n)+"` is a member path, not a type").Text("expected", "type").Text("found", "member")
		return types.Poison
	case resolve.RefModule:
		x.errorf(diag.E0301, n.Span(), "`"+x.nodeText(n)+"` is a module, not a type").Text("expected", "type").Text("found", "module")
		return types.Poison
	}
	return types.Poison
}

// pathLen is the number of segments of a name in a type position: an
// Ident is 1, a FieldExpr chain one more per member.
func pathLen(n ast.Node) int {
	switch n := n.(type) {
	case *ast.FieldExpr:
		return pathLen(n.X) + 1
	case *ast.PathType:
		if n.Path != nil {
			return len(n.Path.Names)
		}
	}
	return 1
}

// nodeText spells a type-position node for a message.
func (x *tctx) nodeText(n ast.Node) string {
	var b strings.Builder
	if pt, ok := n.(*ast.PathType); ok && pt.Path != nil {
		for i, id := range pt.Path.Names {
			if i > 0 {
				b.WriteByte('.')
			}
			if id != nil {
				b.WriteString(x.m.c.cfg.Interner.Str(id.Name))
			}
		}
		return b.String()
	}
	if e, ok := n.(ast.Expr); ok {
		x.m.c.exprText(&b, e)
		return b.String()
	}
	return "this name"
}

// scopeFamily reads `Scope` and `Scope[E]`: std declares the one non-generic
// item, and `Scope[E]` is that item with one argument, an error set, held as
// its ErrorSetType.
func (x *tctx) scopeFamily(n ast.Node, it *types.Item, args []ast.TypeOrExpr) types.TypeID {
	x.rec.shape(it.Key)
	switch len(args) {
	case 0:
		return x.tab().Named(it.ID, nil)
	case 1:
		es, ok := x.errorSet(args[0])
		if !ok {
			return types.Poison
		}
		return x.tab().Named(it.ID, []types.GenericArg{types.TypeArg(x.tab().ErrorSetType(es))})
	}
	x.errorf(diag.E0304, n.Span(), "`Scope` takes at most one argument, an error set").Text("reason", "type-arguments").
		Int("expected", 1).Int("found", int64(len(args)))
	return types.Poison
}

// genericArgs reads the bracket arguments of a generic type against its
// parameters: the count must match (E0304) and each slot's kind.
func (x *tctx) genericArgs(n ast.Node, it *types.Item, args []ast.TypeOrExpr) ([]types.GenericArg, bool) {
	ps := x.m.c.genericsOf(it)
	if len(args) != len(ps) {
		x.errorf(diag.E0304, n.Span(), "`"+x.m.c.cfg.Interner.Str(it.Name)+"` takes a different number of type arguments").
			Text("reason", "type-arguments").Int("expected", int64(len(ps))).Int("found", int64(len(args)))
		return nil, false
	}
	out := make([]types.GenericArg, len(args))
	ok := true
	// Every type argument is read before the const ones, so a const
	// generic typed by a type parameter (`P[T, const V: T]`, or V written
	// first) is judged at T's argument.
	for i, a := range args {
		if ps[i].Const {
			continue
		}
		t := x.typeOf(a, posPlain)
		ok = ok && t != types.Poison
		out[i] = types.TypeArg(t)
	}
	for i, a := range args {
		if !ps[i].Const {
			continue
		}
		x.constParam = x.m.c.cfg.Interner.Str(ps[i].Name)
		ca, good := x.constArg(a, x.m.c.subst(ps[i].Type, out, types.NoType))
		x.constParam = ""
		ok = ok && good
		out[i] = types.ConstGenericArg(ca)
	}
	if ok {
		x.m.queueBounds(x, n.Span(), it, out)
		if x.m.negativeInstance(x, n.Span(), it, out) {
			return out, false
		}
	}
	return out, ok
}

// negativeInstance is E0303 for an instance: a negative const argument
// reaches an array length of the declaration (`[N]T` in a field, a variant
// payload or an alias) by substitution, where no type expression carries
// it, so the instance is walked for one: E0303 at the type expression,
// which is then Poison. A declaration still being declared (a cycle
// through a reference) is walked once it is done, from the queue.
func (m *Module) negativeInstance(x *tctx, at source.Span, it *types.Item, args []types.GenericArg) bool {
	c := m.c
	value := ""
	for _, a := range args {
		if a.IsConst && value == "" && c.negativeLen(at, a.Const) != nil {
			value = c.cfg.Types.ConstString(a.Const.Value)
		}
	}
	if value == "" {
		return false
	}
	walk := func() (found, incomplete bool) {
		t := c.cfg.Types.Named(it.ID, args)
		if it.Kind == types.KindAlias {
			d, ok := c.declOfItem(it).(*types.AliasDecl)
			if !ok {
				return false, true
			}
			t = c.subst(d.Type, args, types.NoType)
		}
		lw := lenWalk{c: c, rec: x.rec, seen: map[types.TypeID]bool{}}
		return lw.negative(t), lw.incomplete
	}
	report := func() {
		x.emit(diag.New(diag.E0303, at, "an array length is at least 0: `"+x.str(c.cfg.Types.Named(it.ID, args))+"` has one below 0").
			Text("value", value).Text("type", "int"))
	}
	found, incomplete := walk()
	if found {
		report()
		return true
	}
	if incomplete && x.sink == nil {
		m.later(x.item, func() {
			if found, _ := walk(); found {
				report()
			}
		})
	}
	return false
}

// alias expands a transparent alias with its arguments.
func (x *tctx) alias(n ast.Node, it *types.Item, args []ast.TypeOrExpr, p tpos) types.TypeID {
	c := x.m.c
	d := c.aliasDecl(it, x.m)
	if d == nil {
		return types.Poison // a cycle, reported at the alias (E0907)
	}
	ga, ok := x.genericArgs(n, it, args)
	if !ok || d.Type == types.Poison {
		return types.Poison
	}
	t := c.subst(d.Type, ga, types.NoType)
	if t == types.Void && p == posPlain || t == types.Never && p != posResult {
		x.errorf(diag.E0301, n.Span(), "`"+x.str(t)+"` is only a result type").Text("expected", "type").Text("found", x.str(t))
		return types.Poison
	}
	return t
}

// errorUnion is E!T: E an error, an alias of a set, a union or an
// error-set parameter; T any type or void.
func (x *tctx) errorUnion(n ast.Node, es, ok ast.Node) types.TypeID {
	set, good := x.errorSet(es)
	t := x.typeOf(ok, posOk)
	if !good || t == types.Poison {
		return types.Poison
	}
	return x.tab().ErrorUnion(set, t)
}

// setType is a union of errors used as a type: an error value's type.
func (x *tctx) setType(n ast.Node) types.TypeID {
	set, ok := x.errorSet(n)
	if !ok {
		return types.Poison
	}
	return x.tab().ErrorSetType(set)
}

// errorSet reads the E of E!T or a union: the flattened members. A
// non-error part is E0301; an empty set cannot be written.
func (x *tctx) errorSet(n ast.Node) (types.ErrorSetID, bool) {
	var ms []types.ErrorMember
	if !x.errorMembers(n, &ms) {
		return types.EmptyErrors, false
	}
	return x.tab().ErrorSet(ms), true
}

func (x *tctx) errorMembers(n ast.Node, ms *[]types.ErrorMember) bool {
	tab := x.tab()
	switch n := n.(type) {
	case *ast.ParenType:
		return x.errorMembers(n.Type, ms)
	case *ast.ParenExpr:
		return x.errorMembers(n.X, ms)
	case *ast.UnionType:
		ok := true
		for _, t := range n.Types {
			ok = x.errorMembers(t, ms) && ok
		}
		return ok
	case *ast.BinaryExpr:
		if n.Op == ast.TokPipe {
			a := x.errorMembers(n.X, ms)
			return x.errorMembers(n.Y, ms) && a
		}
	case *ast.PathType, *ast.Ident, *ast.FieldExpr:
		r := x.ref(n)
		switch r.Kind {
		case resolve.RefPoison, resolve.RefNone:
			return false
		case resolve.RefTypeParam:
			*ms = append(*ms, types.ErrorMember{IsParam: true, Param: r.A})
			return true
		case resolve.RefItem:
			it := x.m.c.cfg.Items.Get(types.ItemID(r.A))
			switch it.Kind {
			case types.KindError:
				*ms = append(*ms, types.ErrorMember{Item: it.ID})
				return true
			case types.KindAlias:
				var args []ast.TypeOrExpr
				if pt, ok := n.(*ast.PathType); ok && pt.Args != nil {
					args = pt.Args.Args
				}
				t := x.alias(n, it, args, posPlain)
				if t == types.Poison {
					return false
				}
				if tab.Kind(t) == types.KErrorSet {
					*ms = append(*ms, tab.Members(tab.Get(t).Errors)...)
					return true
				}
			}
		}
	}
	t := x.typeOf(n, posOk)
	if t == types.Poison {
		return false
	}
	x.errorf(diag.E0301, n.Span(), "the left side of `!` must be an error or a union of errors").
		Text("expected", "error").Text("found", x.str(t))
	return false
}

// fnType is a fn type with its flows: the flows are computed
// by the rules of a signature, positionally.
func (x *tctx) fnType(n *ast.FnType) types.TypeID {
	tab := x.tab()
	sig := &types.Signature{Span: n.Span(), Generics: x.gens, Exported: x.item != nil && x.item.Exported}
	bad := false
	for _, fp := range n.Params {
		if fp == nil {
			bad = true
			continue
		}
		t := x.typeOf(fp.Type, posPlain)
		bad = bad || t == types.Poison
		pr := types.Param{Type: t, Span: fp.Span()}
		if fp.Name != nil {
			pr.Name = fp.Name.Name
		}
		if fp.Into != nil {
			if r := x.ref(fp.Into); r.Kind == resolve.RefParam {
				pr.Into = types.ParamIndex(r.A)
			}
		}
		sig.Params = append(sig.Params, pr)
	}
	res, errs, ok := x.result(n.Result)
	if !ok || bad {
		return types.Poison
	}
	sig.Result, sig.Errors = res, errs
	cl := x.clauses(n.Flows, sig)
	fl, ds := x.m.c.signatureFlows(flowFn{sig: sig, mod: x.m.ID, env: x.m.c.NewEnv(x.gens), at: n.Span()}, cl)
	x.emit(ds...)
	ps := make([]types.FnParam, len(sig.Params))
	for i, p := range sig.Params {
		ps[i] = types.FnParam{Type: p.Type, Into: p.Into}
	}
	return tab.Fn(types.FnType{Params: ps, Result: res, Errors: errs, Sources: fl.Sources, Fields: fl.Fields, Stores: fl.Stores, Ends: fl.Ends})
}

// result reads a result type: absent is void; E!T splits into its set and T.
func (x *tctx) result(n ast.Type) (types.TypeID, types.ErrorSetID, bool) {
	switch r := n.(type) {
	case nil:
		return types.Void, types.EmptyErrors, true
	case *ast.ErrorType:
		set, ok := x.errorSet(r.Errors)
		t := x.typeOf(r.Ok, posOk)
		return t, set, ok && t != types.Poison
	case *ast.ParenType:
		if _, isErr := r.Type.(*ast.ErrorType); isErr {
			return x.result(r.Type)
		}
	}
	t := x.typeOf(n, posResult)
	return t, types.EmptyErrors, t != types.Poison
}

// clauses translates a signature's or fn type's written flows into the
// input of signatureFlows. A name resolve marked (E0201) is absent.
func (x *tctx) clauses(flows []ast.Flow, sig *types.Signature) *flowClauses {
	cl := &flowClauses{}
	param := func(id *ast.Ident) types.ParamIndex {
		if id == nil {
			return types.NoParam
		}
		r := x.ref(id)
		if r.Kind != resolve.RefParam || r.A == 0 || int(r.A) > len(sig.Params) {
			return types.NoParam
		}
		return types.ParamIndex(r.A)
	}
	for _, f := range flows {
		switch f := f.(type) {
		case *ast.FromFlow:
			cl.hasFrom = true
			src := func(s *ast.Source) (fromClause, bool) {
				if s == nil || len(s.Names) == 0 {
					return fromClause{}, false
				}
				p := param(s.Names[0])
				if p == types.NoParam {
					return fromClause{}, false
				}
				fc := fromClause{param: p, at: s.Span()}
				for _, st := range s.Names[1:] {
					if st != nil {
						fc.steps = append(fc.steps, stepRef{name: st.Name, at: st.Span()})
					}
				}
				return fc, true
			}
			for _, s := range f.Sources {
				if fc, ok := src(s); ok {
					cl.from = append(cl.from, fc)
				}
			}
			// A per-field clause.
			for _, fs := range f.Fields {
				if fs == nil || fs.Name == nil {
					continue
				}
				fcl := fieldSrcClause{name: fs.Name.Name, at: fs.Name.Span()}
				for _, s := range fs.Sources {
					if fc, ok := src(s); ok {
						fcl.from = append(fcl.from, fc)
					}
				}
				cl.fields = append(cl.fields, fcl)
			}
		case *ast.StoreFlow:
			from, into := param(f.From), param(f.Into)
			if from == types.NoParam || into == types.NoParam {
				continue
			}
			cl.stores = append(cl.stores, storeClause{from: from, into: into, at: f.Span(), fromAt: f.From.Span()})
		case *ast.EndsFlow:
			if p := param(f.Name); p != types.NoParam {
				cl.ends = append(cl.ends, p)
				cl.endsAt = append(cl.endsAt, f.Span())
			}
		}
	}
	return cl
}
