// Why: a call is the busiest expression form: its target may be a function
// item, a method, an interface method of a bound, a fn value, a
// construction, a builtin or a universe name, each with its own arguments,
// generics, receiver adjustment and diagnostics.
//
// What: callee, callName, callMember, callStatic, callMethod, callValue,
// callIfaceMethod, sliceMethod, builtinCall, universeCall and evalArena
// (the forms), applyCall, explicitArgs and judgePending, argShape (E0304,
// E0305), receiver (the receiver's Adjust), IfaceCallLowers.
//
// How: calls of functions and methods end in applyCall: explicit
// arguments, the receiver's owner arguments first, the arguments left to
// right, the expected result, then bounds (E0711, E0701). A call through a
// fn value checks against its fn type; a construction goes to construct.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// callee is a resolved call target with the generic arguments already known.
type callee struct {
	kind  UseKind // UseItem, UseMethod, UseIfaceMethod
	item  types.ItemID
	index uint32 // interface method index
	sig   *types.Signature
	// recv is the receiver of a dot call, recvPassed the type self receives
	// after the receiver adjustment; NoType for a namespace-form call.
	recv       ast.Expr
	recvPassed types.TypeID
	self       types.TypeID       // KSelf, for an interface method
	pre        []types.GenericArg // owner (or interface) arguments, from slot 0
	explicit   *ast.IndexExpr     // the callee's own arguments in brackets
	// preChecked: pre came from a type expression, whose bounds were checked
	// there, so finish does not report them again.
	preChecked bool
	name       ast.Node       // where E0711 points
	fe         *ast.FieldExpr // the member node, which gets the same Use (not in namespace form)
	// ns: a `T.Name(..)` call in namespace form. Its
	// member node keeps no Use, which is how the Body tells it from a dot
	// call `x.Name(..)`: there is no receiver, so no receiver Adjust, and
	// the first argument is an ordinary argument for the first parameter,
	// self included.
	ns bool
}

// walkArgs types every argument on its own: a call whose target is unknown
// (an earlier error) or not yet handled. The expectation is Poison, so
// nothing in them reports what only the unknown target could decide
// (`none`, `[]`).
func (u *unit) walkArgs(e *ast.CallExpr) {
	for _, a := range e.Args {
		if a != nil && a.Value != nil {
			u.expr(a.Value, types.Poison)
		}
	}
}

// callOther types a call that is not a conversion, Cast or Trunc.
func (u *unit) callOther(e *ast.CallExpr, want types.TypeID) types.TypeID {
	switch f := e.Fun.(type) {
	case *ast.Ident:
		return u.callName(e, f, u.ref(f), nil, want)
	case *ast.FieldExpr:
		switch r := u.ref(f); r.Kind {
		case resolve.RefItem:
			return u.callName(e, f, r, nil, want)
		case resolve.RefPoison:
			u.walkArgs(e)
			return types.Poison
		}
		return u.callMember(e, f, nil, want)
	case *ast.IndexExpr:
		switch u.x.reading(f) {
		case resolve.ReadGeneric:
			switch x := f.X.(type) {
			case *ast.Ident:
				return u.callName(e, x, u.ref(x), f, want)
			case *ast.FieldExpr:
				if r := u.ref(x); r.Kind == resolve.RefItem {
					return u.callName(e, x, r, f, want)
				}
				return u.callMember(e, x, f, want)
			}
		case resolve.ReadUndecided:
			if x, ok := f.X.(*ast.FieldExpr); ok {
				return u.callMember(e, x, f, want)
			}
		}
	}
	return u.callValue(e, want)
}

// callName is a call whose callee is a name: a function item, a type
// (construction), a universe builtin, or a value of fn type.
func (u *unit) callName(e *ast.CallExpr, name ast.Expr, r resolve.Ref, idx *ast.IndexExpr, want types.TypeID) types.TypeID {
	c := u.c
	switch r.Kind {
	case resolve.RefItem:
		it := c.cfg.Items.Get(types.ItemID(r.A))
		switch d := c.declOfItem(it).(type) {
		case *types.FuncDecl:
			u.x.rec.signature(it.Key)
			u.unsafeCall(e, it.ID, d)
			if idx != nil {
				u.setType(idx, types.Poison)
			}
			return u.applyCall(e, &callee{kind: UseItem, item: it.ID, sig: &d.Sig, explicit: idx, name: name}, want)
		case *types.StructDecl, *types.ErrorDecl:
			var h head
			if idx != nil {
				h, _ = u.typeHead(idx)
			} else {
				h, _ = u.typeHead(name)
			}
			return u.construct(e, h, -1, name, want)
		case *types.AliasDecl:
			var n ast.Expr = name
			if idx != nil {
				n = idx
			}
			h, ok := u.typeHead(n)
			if ok && h.it != nil && h.it.Kind == types.KindStruct {
				return u.construct(e, h, -1, name, want)
			}
			if !ok {
				h.t = types.Poison
			}
			// An alias of anything but a struct is transparent:
			// `Byte(x)` with `type Byte = u8` is the conversion `u8(x)`,
			// and so is one of an enum, an error set or a record,
			// judged as a conversion (never a silent Poison).
			return u.conversion(e, h.t, BuiltinConvert)
		case *types.EnumDecl:
			// An enum is constructed through a variant; `E(x)` is a
			// conversion to it (integer to enum is E0303).
			var n ast.Expr = name
			if idx != nil {
				n = idx
			}
			return u.conversion(e, u.x.typeOf(n, posPlain), BuiltinConvert)
		}
	case resolve.RefUniverse:
		if source.Symbol(r.A) == source.SymSome && idx == nil {
			return u.someCall(e, want)
		}
		return u.universeCall(e, source.Symbol(r.A), idx)
	case resolve.RefPoison, resolve.RefNone:
		u.walkArgs(e)
		return types.Poison
	}
	return u.callValue(e, want)
}

// universeCall types Assert(bool), Panic(str), SizeOf[T]() and AlignOf[T]().
// Every other universe name called is Poison here, and never a
// silent one (lower's invariant; the `A(){some}` family found by
// fuzzing): a value name or a bound called is E0301, a builtin without its
// one type argument E0711 or E0304, `fields` outside a loop head E0301.
// `ok` and `err` are resolve's E0201 (pattern heads only).
func (u *unit) universeCall(e *ast.CallExpr, s source.Symbol, idx *ast.IndexExpr) types.TypeID {
	var b Builtin
	var params []types.TypeID
	res := types.Void
	name := u.c.cfg.Interner.Str(s)
	switch s {
	case source.SymAssert, source.SymPanic:
		if idx != nil {
			return u.noTypeArgs(e, idx, name)
		}
		b, params = BuiltinAssert, []types.TypeID{types.Bool}
		if s == source.SymPanic {
			b, params, res = BuiltinPanic, []types.TypeID{types.Str}, types.Never
		}
	case source.SymSome:
		// `some(x)` itself is someCall; here it carries brackets.
		return u.noTypeArgs(e, idx, name)
	case source.SymSizeOf, source.SymAlignOf:
		b, res = BuiltinSizeOf, types.Int
		if s == source.SymAlignOf {
			b = BuiltinAlignOf
		}
		if idx == nil || len(idx.Args) != 1 {
			u.oneTypeArg(e, idx, name, name+"[T]()")
			return types.Poison
		}
		u.setType(idx, types.Poison)
		// A closed type's size or alignment is a constant: its value
		// goes to Body.Consts. A type that mentions a parameter has none, and
		// the type argument's node carries the type instead, which lower reads
		// for RvSizeOf and RvAlignOf in a generic body. A closed one keeps
		// NoType there, so lower takes the constant.
		t := u.x.typeOf(idx.Args[0], posPlain)
		if t != types.Poison && !u.c.closed(t) {
			u.setType(idx.Args[0], t)
		}
		if t != types.Poison && u.c.closed(t) {
			sh := u.c.Layout(t).Shape
			v := int64(sh.Size)
			if b == BuiltinAlignOf {
				v = int64(sh.Align)
			}
			u.b.setConst(e.ID(), u.c.intConst(v))
		}
	case source.SymEvalArena:
		return u.evalArena(e, idx)
	case source.SymCast, source.SymTrunc:
		// `Cast[T](x)` and `Trunc[T](x)` are call's; here the one type
		// argument is missing or there are several.
		u.oneTypeArg(e, idx, name, name+"[T](x)")
		return types.Poison
	case source.SymTrue, source.SymFalse, source.SymNone, source.SymTarget:
		u.walkArgs(e)
		if idx != nil {
			u.setType(idx, types.Poison)
		}
		u.err(diag.E0301, e.Fun.Span(), "`"+name+"` is not a function").Text("expected", "a function").Text("found", name)
		return types.Poison
	case source.SymPlain, source.SymSend, source.SymShare, source.SymFieldsBound:
		u.walkArgs(e)
		if idx != nil {
			u.setType(idx, types.Poison)
		}
		u.err(diag.E0301, e.Fun.Span(), "`"+name+"` is a built-in bound, not a function").Text("expected", "a function").Text("found", "bound")
		return types.Poison
	case source.SymFields:
		u.walkArgs(e)
		if idx != nil {
			u.setType(idx, types.Poison)
		}
		return u.fieldsOutsideLoop(e.Fun, name)
	default:
		// ok and err: resolve reported E0201 (pattern heads only).
		u.walkArgs(e)
		return types.Poison
	}
	*u.b.use(e.ID()) = Use{Kind: UseBuiltin, Index: uint32(b)}
	u.plainArgs(e, params, name+"(..)")
	return res
}

// noTypeArgs is a universe builtin that takes no type arguments written
// with brackets (`some[i32](1)`, `Assert[T](c)`): E0304 type-arguments, as
// EvalArena's.
func (u *unit) noTypeArgs(e *ast.CallExpr, idx *ast.IndexExpr, name string) types.TypeID {
	u.walkArgs(e)
	if idx == nil {
		return types.Poison
	}
	u.setType(idx, types.Poison)
	u.err(diag.E0304, idx.Span(), "`"+name+"` takes no type arguments").Text("reason", "type-arguments").
		Int("expected", 0).Int("found", int64(len(idx.Args)))
	return types.Poison
}

// oneTypeArg reports a builtin that takes exactly one type argument
// (SizeOf, AlignOf, Cast, Trunc) called without it, E0711 as a generic
// function whose T nothing fixes, or with another count, E0304
// type-arguments as explicitArgs.
func (u *unit) oneTypeArg(e *ast.CallExpr, idx *ast.IndexExpr, name, form string) {
	u.walkArgs(e)
	if idx == nil {
		u.err(diag.E0711, e.Fun.Span(), "`"+name+"` needs its type argument: write `"+form+"`").Text("param", "T")
		return
	}
	u.setType(idx, types.Poison)
	u.err(diag.E0304, idx.Span(), "wrong number of type arguments").Text("reason", "type-arguments").
		Text("signature", form).Int("expected", 1).Int("found", int64(len(idx.Args)))
}

// fieldsOutsideLoop is the loop word `fields` anywhere but the source of a
// fields loop: E0301 reason fields-of, as the loop's
// own misuse.
func (u *unit) fieldsOutsideLoop(at ast.Expr, name string) types.TypeID {
	u.err(diag.E0301, at.Span(), "`"+name+"` is the source of a fields loop only: write `for name, value in fields(&x) { }`").
		Text("reason", "fields-of")
	return types.Poison
}

// evalArena types `EvalArena()`: a builtin use whose result is a &Arena to
// the prelude's Arena. It is legal only in code evaluated at
// compile time: a const or global initializer, a requires predicate
// over const parameters only, or a gen-only function; elsewhere E0705.
// The call takes no argument (E0304) and no type argument.
func (u *unit) evalArena(e *ast.CallExpr, idx *ast.IndexExpr) types.TypeID {
	if idx != nil {
		u.setType(idx, types.Poison)
		u.err(diag.E0304, idx.Span(), "`EvalArena` takes no type arguments").
			Text("signature", "EvalArena(): &Arena").Int("expected", 0).Int("found", int64(len(idx.Args)))
		u.walkArgs(e)
		return types.Poison
	}
	*u.b.use(e.ID()) = Use{Kind: UseBuiltin, Index: uint32(BuiltinEvalArena)}
	u.plainArgs(e, nil, "EvalArena(): &Arena")
	if !u.compileTimeUnit() {
		u.err(diag.E0705, e.Span(), "`EvalArena()` gives an arena for one compile-time evaluation; it is legal only in a const or global initializer, a compile-time `requires` or a generator").
			Text("param", "EvalArena")
	}
	if u.c.known.Arena == types.NoItem {
		return types.Poison
	}
	// A &Arena, as eval's builtin and Gen.Arena give it.
	return u.tab.Ref(u.tab.Named(u.c.known.Arena, nil))
}

// compileTimeUnit reports whether the unit runs only at compile time
// (a const or global initializer, a requires predicate whose every
// parameter is a const parameter of its function, or a gen-only
// function).
func (u *unit) compileTimeUnit() bool {
	switch n := u.b.Node.(type) {
	case *ast.ConstDecl, *ast.GlobalDecl:
		return true
	case *ast.Contract:
		if n.Kind != ast.Requires || u.sig == nil {
			return false
		}
		owner := u.c.cfg.Items.Get(u.it.Owner)
		fd, ok := u.c.declOfItem(owner).(*types.FuncDecl)
		if !ok {
			return false
		}
		for _, pp := range u.sig.Params {
			i := fd.Sig.ParamByName(pp.Name)
			if i == types.NoParam || !fd.Sig.Param(i).Const {
				return false
			}
		}
		return true
	}
	return u.genOnlyUnit()
}

// plainArgs checks the arguments of a non-generic parameter list.
func (u *unit) plainArgs(e *ast.CallExpr, params []types.TypeID, sig string) {
	u.argShape(e, len(params), false, sig, nil, nil)
	for i, a := range e.Args {
		if a == nil || a.Value == nil {
			continue
		}
		w := types.NoType
		if i < len(params) {
			w = params[i]
		}
		u.expr(a.Value, w)
	}
}

// argShape reports named arguments (E0305 `named-argument`, proven fix
// when the names are the parameters in order) and a wrong count
// (E0304 with the signature) on a call of a function or fn value.
//
// The signature text for a message is sig, or, when full is set, full's
// spelling, built only when a diagnostic needs it (most calls draw none).
func (u *unit) argShape(e *ast.CallExpr, n int, variadic bool, sig string, full *types.Signature, names []source.Symbol) {
	sigText := func() string {
		if full != nil {
			return u.c.sigString(full)
		}
		return sig
	}
	var named []*ast.Arg
	for _, a := range e.Args {
		if a != nil && a.Name != nil {
			named = append(named, a)
		}
	}
	if len(named) > 0 {
		d := u.err(diag.E0305, named[0].Span(), "a function takes its arguments by position, not by name").
			Text("reason", "named-argument").Text("signature", sigText())
		inOrder := len(names) == len(e.Args)
		var edits []diag.Edit
		for i, a := range e.Args {
			if a == nil || a.Name == nil {
				continue
			}
			if !inOrder || a.Name.Name != names[i] || a.Value == nil {
				inOrder = false
				break
			}
			edits = append(edits, diag.Edit{Span: between(a.Name.Span(), a.Value.Span()), Text: ""})
		}
		if inOrder && len(edits) > 0 {
			d.Fix(diag.Fix{Class: diag.Proven, Edits: edits, Why: "the names are the parameters in order"})
		} else {
			d.Alt("remove the names and pass the arguments in parameter order")
		}
	}
	if len(e.Args) != n && !(variadic && len(e.Args) >= n) {
		u.err(diag.E0304, e.Span(), "wrong number of arguments").Text("signature", sigText()).
			Int("expected", int64(n)).Int("found", int64(len(e.Args)))
	}
}

// applyCall checks a call of a function or method against its signature
// (explicit arguments, the receiver, the arguments left to right,
// the expected result, E0711, bounds, the Use and Body.Generic).
func (u *unit) applyCall(e *ast.CallExpr, cs *callee, want types.TypeID) types.TypeID {
	sig := cs.sig
	in := u.newInfer(sig.Generics, cs.self)
	in.preset(0, cs.pre)
	if cs.preChecked {
		in.checked = append([]bool(nil), in.bound...)
	}
	own := int(sig.OwnGenerics)
	params := sig.Params
	skip := 0
	// The receiver binds the owner's arguments before the method's own
	// brackets are read, so a const generic typed by the owner's T is
	// judged at T's argument.
	if cs.recv != nil && len(params) > 0 {
		skip = 1
		in.unify(in.at(params[0].Type), cs.recvPassed)
	}
	if cs.recv != nil && u.goCalls != nil && u.goCalls[e.ID()] {
		if u.goRecv == nil {
			u.goRecv = map[ast.NodeID]types.TypeID{}
		}
		u.goRecv[e.ID()] = cs.recvPassed
	}
	rest := params[skip:]
	if cs.explicit != nil {
		u.explicitArgs(in, cs.explicit, sig.Generics[len(sig.Generics)-own:], len(sig.Generics)-own)
	}
	names := make([]source.Symbol, len(rest))
	for i, p := range rest {
		names[i] = p.Name
	}
	fmtCall := u.isFormatter(cs.item)
	slot := u.judgedSlot(sig, rest)
	shaped := fmtCall || slot >= 0 || u.formatShaped(sig, rest)
	// Print(w, f) and a.Fmt(f), one argument short of the
	// formatter's parameters, are the call with () as its record,
	// and so may any callee of the same signature shape.
	short := shaped && len(rest) >= 2 && len(e.Args) == len(rest)-1 && len(sig.Generics) > 0
	if short && !u.recordOpen(in, rest[len(rest)-1].Type) {
		// The record's type is fixed already (an explicit argument
		// or the owner's), and not (): the call keeps its arity, E0304.
		short = false
	}
	count := len(rest)
	if short {
		count--
	}
	u.argShape(e, count, sig.Variadic, "", sig, names[:count])
	for i, a := range e.Args {
		if a == nil || a.Value == nil {
			continue
		}
		switch {
		case i < len(rest):
			if rest[i].Const && (u.goCalls == nil || !u.goCalls[e.ID()]) {
				// A literal here may capture, and a const fn parameter
				// may be passed on, only when the parameter is of fn
				// type: the callee may use a const F as any value.
				if u.tab.Kind(rest[i].Type) == types.KFn {
					u.markConstLit(&u.constLit, a.Value)
				} else {
					u.markConstLit(&u.constNotFn, a.Value)
				}
			}
			in.argOrLater(a.Value, rest[i].Type)
		case sig.Variadic:
			u.variadicArg(a.Value)
		default:
			u.expr(a.Value, types.NoType)
		}
	}
	if short {
		u.shortFormat(e, e.Args[count-1], in, rest[count].Type)
	}
	fi := len(rest) - 2
	switch {
	case fmtCall:
	case slot >= 0 && fi >= 0:
		// The format argument is judged by formatCheck (E0705 there), the
		// other const arguments here.
		ps := append([]types.Param(nil), rest...)
		ps[fi].Const = false
		u.constArgs(e, ps)
	default:
		u.constArgs(e, rest)
	}
	if slot >= 0 && len(sig.Generics) > 0 {
		in.fmtSlot = slot + 1
		u.printForward(e, in, rest, slot, short)
	} else {
		u.shapeFElsewhere(e, rest, -1)
	}
	ret := u.tab.ReturnType(sig.Result, sig.Errors)
	if want != types.NoType && want != types.Poison {
		if r := in.at(ret); in.open(r) {
			in.unify(r, want)
		}
	}
	in.lateArgs()
	u.judgePending(in)
	nameAt := e.Fun.Span()
	if cs.name != nil {
		nameAt = cs.name.Span()
	}
	inst, ok := in.finish(nameAt, e.Span())
	if ok && len(sig.Generics) > 0 {
		var at ast.Node = e.Fun
		if cs.name != nil {
			at = cs.name
		}
		// An open head whose T comes from a value of the owner type (the
		// namespace form `raw.Atomic.Load(&a)`) was judged where that type
		// was formed: one E0701, not two.
		owner := cs.recv == nil && !cs.preChecked && !u.c.takesOwner(sig, cs.item)
		u.implicitBounds(cs.item, at.Span(), inst, owner)
		if inst != 0 && int(inst) < len(u.b.Generic) {
			u.simdCallRules(at, e, cs.item, u.b.Generic[inst]) // lane classes, Shuffle's indices
			u.lanesCall(e, cs.item, u.b.Generic[inst])         // simd.Lanes[T]() is a constant
			u.mmioLane(at, cs.item, u.b.Generic[inst])
			u.offsetOfCall(e, cs.item, u.b.Generic[inst])
		}
	}
	u.cpuNameArg(e, cs.item) // CpuFeatures.Of and Has
	u.genUse(e, sig, "call")
	if u.inContract && u.takesIo(sig) {
		u.contractForm(e.Span(), "a call taking `Io`")
	}
	use := Use{Kind: cs.kind, Item: cs.item, Index: cs.index, Inst: inst}
	*u.b.use(e.ID()) = use
	if cs.fe != nil {
		if !cs.ns {
			*u.b.use(cs.fe.ID()) = use
		}
		u.setType(cs.fe, types.Poison)
	}
	if shaped && fi >= 0 && len(e.Args) == len(rest) && e.Args[fi] != nil && e.Args[fi+1] != nil && len(sig.Generics) > 0 && !in.fmtFailed && !in.fmtSkip {
		a := types.Poison
		if ok && inst != 0 {
			if g := u.b.Generic[inst]; len(g) > 0 {
				if slot >= 0 && slot < len(g) {
					a = g[slot].Type
				} else {
					a = g[len(g)-1].Type
				}
			}
		}
		if fmtCall || slot >= 0 {
			u.formatCheck(e.Args[fi].Value, e.Args[fi+1].Value, a, fmtCall)
		}
	} else if (fmtCall || slot >= 0) && in.fmtFailed && !in.fmtSkip && fi >= 0 && fi < len(e.Args) && e.Args[fi] != nil && e.Args[fi].Value != nil {
		// The record failed its bound (E0701), so its text is not matched;
		// a format that is no constant is still E0705.
		if v := e.Args[fi].Value; !u.forwardedFormat(v) {
			u.formatText(v)
		}
	}
	if ok && len(sig.Contracts) > 0 {
		u.constRequires(e, sig, rest, inst)
	}
	if !ok {
		return types.Poison
	}
	return in.final(ret)
}

// explicitArgs reads a callee's own generic arguments written in brackets
// (`Parse[i32](s)`) into slots from..: the count must match (E0304
// `type-arguments`); bounds are checked with the inferred ones.
func (u *unit) explicitArgs(in *infer, idx *ast.IndexExpr, ps []types.GenericParam, from int) {
	if len(idx.Args) != len(ps) {
		u.err(diag.E0304, idx.Span(), "wrong number of type arguments").Text("reason", "type-arguments").
			Int("expected", int64(len(ps))).Int("found", int64(len(idx.Args)))
		return
	}
	// The type arguments first, then the const ones at their generic's
	// type with the bindings so far, the receiver's included:
	// `GR[K, Pick]` with `GR[const V: T, T]`, `b.M[K]()` with
	// `Box[T].M[const V: T]`. A type still open is judged later
	// (judgePending).
	u.explicitTypes(in, idx, ps, from)
	for i, a := range idx.Args {
		if !ps[i].Const {
			continue
		}
		u.x.constParam = u.c.cfg.Interner.Str(ps[i].Name)
		w := in.at(ps[i].Type)
		ca, ok := u.x.constArg(a, w)
		u.x.constParam = ""
		if ok {
			in.set(from+i, types.ConstGenericArg(ca))
			if u.c.hasHole(w) {
				in.pending = append(in.pending, pendingConst{at: a.Span(), slot: from + i, p: ps[i].Type})
			}
		}
	}
}

// pendingConst is a const argument written in brackets whose generic's
// type still held an inference variable when it was read (the owner's T
// of a namespace-form call, bound by a value argument): judgePending
// judges it once the call's arguments are read.
type pendingConst struct {
	at   source.Span
	slot int
	p    types.TypeID
}

// judgePending judges each pending const argument at its generic's type
// with the bindings so far (judgeConstArg). One whose type is still open
// is left: finish reports E0711 for the slot that keeps it open. A
// mismatch makes the call Poison (badExplicit), as a bad type argument
// does.
func (u *unit) judgePending(in *infer) {
	for _, pc := range in.pending {
		w := in.at(pc.p)
		if u.c.hasHole(w) || pc.slot >= len(in.binds) || !in.binds[pc.slot].IsConst {
			continue
		}
		a, ok := u.x.judgeConstArg(pc.at, in.binds[pc.slot].Const, w)
		if !ok {
			in.badExplicit = true
			continue
		}
		in.binds[pc.slot] = types.ConstGenericArg(a)
	}
	in.pending = nil
}

// explicitTypes is explicitArgs' first pass: the arguments of its type
// parameters.
func (u *unit) explicitTypes(in *infer, idx *ast.IndexExpr, ps []types.GenericParam, from int) {
	for i, a := range idx.Args {
		if ps[i].Const {
			continue
		}
		// posArg: a written argument may be void wherever an inferred one may,
		// so `raw.Null[void]()` and `raw.PtrCast[T, void](p)` build the *void
		// that `raw.Addr(p)` already takes by inference.
		if t := u.x.typeOf(a, posArg); t != types.Poison {
			in.set(from+i, types.TypeArg(t))
		} else {
			if ax, ok := a.(ast.Expr); ok && ax != nil && !typeShaped(a) && u.x.reading(a) != resolve.ReadType {
				// A value where a type parameter's argument goes, in a bracket
				// resolve left to check (`Vec[i8, 16].WidenLo[0, 8]`,
				// `v.M[0]()`): resolve walked it as a value and
				// reported nothing, so it is E0301 here, as a value name in
				// a type position is (typeOf).
				pn := u.c.cfg.Interner.Str(ps[i].Name)
				u.err(diag.E0301, a.Span(), "`"+pn+"` takes a type, not a value").Text("expected", "type").Text("found", "value")
				u.setType(ax, types.Poison)
			}
			in.set(from+i, types.TypeArg(types.Poison))
			in.badExplicit = true
		}
	}
}

// typeShaped is resolve's test of the same name: whether a bracket
// argument has a type reading at all.
func typeShaped(a ast.TypeOrExpr) bool {
	switch a := a.(type) {
	case ast.Type, *ast.Ident, *ast.FieldExpr, *ast.IndexExpr, *ast.ParenExpr, *ast.RecordLit:
		return true
	case *ast.UnaryExpr:
		return a.Op == ast.TokAmp || a.Op == ast.TokMut || a.Op == ast.TokStar
	case *ast.BinaryExpr:
		return a.Op == ast.TokPipe || a.Op == ast.TokBang
	}
	return false
}

// callValue is a call through a value of fn type (UseFnValueCall): a
// local, a parameter, a field, any expression. Arity and types come from
// the fn type; calling anything else is E0301.
func (u *unit) callValue(e *ast.CallExpr, want types.TypeID) types.TypeID {
	ft := u.expr(e.Fun, types.NoType)
	t := u.callFnType(e, ft)
	// A const fn parameter is a direct call after instantiation:
	// UseItem with the parameter's index.
	if id, ok := e.Fun.(*ast.Ident); ok && u.sig != nil && u.tab.Kind(ft) == types.KFn {
		if r := u.ref(id); r.Kind == resolve.RefParam && types.ItemID(r.B) == u.it.ID && r.A >= 1 && int(r.A) <= len(u.sig.Params) && u.sig.Params[r.A-1].Const {
			*u.b.use(e.ID()) = Use{Kind: UseItem, Index: r.A}
		}
	}
	return t
}

func (u *unit) callFnType(e *ast.CallExpr, ft types.TypeID) types.TypeID {
	if ft == types.Poison {
		u.walkArgs(e)
		return types.Poison
	}
	ty := u.tab.Get(ft)
	if ty.Kind != types.KFn {
		u.walkArgs(e)
		u.err(diag.E0301, e.Fun.Span(), "`"+u.str(ft)+"` is not a function").Text("expected", "a function").Text("found", u.str(ft))
		return types.Poison
	}
	ps := make([]types.TypeID, len(ty.Fn.Params))
	for i, p := range ty.Fn.Params {
		ps[i] = p.Type
	}
	*u.b.use(e.ID()) = Use{Kind: UseFnValueCall}
	u.plainArgs(e, ps, u.str(ft))
	return u.tab.ReturnType(ty.Fn.Result, ty.Fn.Errors)
}

// callMember is a call `X.Name(..)` (or `X.Name[A](..)`): a namespace-form
// call or a construction when X names a type, a method call otherwise.
func (u *unit) callMember(e *ast.CallExpr, fe *ast.FieldExpr, idx *ast.IndexExpr, want types.TypeID) types.TypeID {
	if idx != nil {
		u.setType(idx, types.Poison)
	}
	if fe.Name == nil {
		u.walkArgs(e)
		return types.Poison
	}
	if h, ok := u.typeHead(fe.X); ok {
		u.setType(fe.X, types.Poison)
		return u.callStatic(e, fe, h, idx, want)
	}
	rt := u.expr(fe.X, types.NoType)
	return u.callMethod(e, fe, rt, idx, want)
}

// callStatic is `T.Name(..)` with T a type: a variant's construction, a
// Type.Name function, or a method in namespace form with its receiver
// written as the first argument.
func (u *unit) callStatic(e *ast.CallExpr, fe *ast.FieldExpr, h head, idx *ast.IndexExpr, want types.TypeID) types.TypeID {
	if h.t == types.Poison {
		u.walkArgs(e)
		return types.Poison
	}
	mb, ok := u.memberOf(h.t, fe.Name)
	if !ok {
		u.walkArgs(e)
		return types.Poison
	}
	switch mb.Kind {
	case MemberVariant:
		return u.construct(e, h, int(mb.Field), fe, want)
	case MemberFunc, MemberImplMethod:
		d, ok := u.c.declOfItem(u.c.cfg.Items.Get(mb.Item)).(*types.FuncDecl)
		if !ok {
			break
		}
		// E1101: `str.FromC(p)` and a bodiless extern member are as unsafe
		// in namespace form as by name.
		u.unsafeCall(e, mb.Item, d)
		cs := &callee{kind: UseItem, item: mb.Item, sig: &d.Sig, explicit: idx, name: fe.Name, fe: fe, ns: true}
		if !h.open && mb.Kind == MemberFunc {
			if ty := u.tab.Get(h.t); ty.Kind == types.KNamed {
				cs.pre, cs.preChecked = ty.Args, true
			}
		}
		return u.applyCall(e, cs, want)
	case MemberField:
		u.typeFieldMiss(fe, h.t)
	}
	u.walkArgs(e)
	return types.Poison
}

// callMethod is `x.M(..)` on a value: builtin members, interface
// methods of a type parameter's bounds, Type.Name methods and impl methods
// with the receiver adjusted per its mode, and fn-typed fields.
func (u *unit) callMethod(e *ast.CallExpr, fe *ast.FieldExpr, rt types.TypeID, idx *ast.IndexExpr, want types.TypeID) types.TypeID {
	if rt == types.Poison {
		u.walkArgs(e)
		return types.Poison
	}
	v, n := u.deref(rt)
	vt := u.tab.Get(v)
	nm := u.c.cfg.Interner.Str(fe.Name.Name)
	switch vt.Kind {
	case types.KPoison:
		u.walkArgs(e)
		return types.Poison
	case types.KRawPtr:
		// Member lookup never auto-derefs through `*T`; only a field
		// access is a dereference. A
		// method call on a raw pointer is E0203, never a silent Poison
		// (lower's invariant).
		u.walkArgs(e)
		u.rawMember(fe, rt)
		return types.Poison
	case types.KParam:
		return u.callIfaceMethod(e, fe, v, n, idx, want)
	case types.KRecord:
		for i, f := range vt.Fields {
			if f.Name == fe.Name.Name {
				*u.b.use(fe.ID()) = Use{Kind: UseField, Index: uint32(i)}
				if n > 0 {
					*u.b.adjust(fe.X.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n + u.predDeref(fe.X))}
				}
				u.setType(fe, f.Type)
				return u.callFnType(e, f.Type)
			}
		}
	case types.KNamed:
		if t, ok := u.ifaceSelfMethod(e, fe, v, n, idx, want); ok {
			return t
		}
	case types.KErrorSet:
		if t, ok := u.errorSetMethod(e, fe, v, n, idx, want); ok {
			return t
		}
	}
	if (vt.Kind == types.KOptional || vt.Kind == types.KErrorUnion) && resolve.Fold(nm) == resolve.Fold("Or") {
		u.walkArgs(e)
		u.orRemoved(e, fe)
		return types.Poison
	}
	if syn, ok := resolve.MemberSynonym(nm); ok && syn.OnFallible && vt.Kind == types.KErrorUnion {
		// `.unwrap()` on an `E!T` asks for `Must`, not for the error to be
		// handled: E1221, not E0802.
		u.walkArgs(e)
		u.synonymMiss(v, fe.Name, nm, syn)
		return types.Poison
	}
	if vt.Kind == types.KErrorUnion && nm != "Must" {
		// try applies to the outermost call: in `try a.B().C()` a fallible
		// B() is E0802.
		u.walkArgs(e)
		u.unhandled(fe.X, v)
		return types.Poison
	}
	if b, ok := u.builtinMember(v, fe.Name); ok {
		return u.builtinCall(e, fe, b, v, n, want)
	}
	if cs, ok := u.sliceMethod(fe, v, n, idx); ok {
		return u.applyCall(e, cs, want)
	}
	nd := len(u.b.Diags)
	mb, ok := u.valueMemberOf(v, fe, true)
	if !ok {
		if vt.Kind == types.KErrorSet && nm == "Format" && len(u.b.Diags) > nd {
			u.c.errFmtFacts(u.b.Diags[len(u.b.Diags)-1], v)
		}
		u.walkArgs(e)
		return types.Poison
	}
	switch mb.Kind {
	case MemberField:
		if n > 0 {
			*u.b.adjust(fe.X.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n + u.predDeref(fe.X))}
		}
		ft := u.fieldUse(fe, v, mb)
		u.setType(fe, ft)
		return u.callFnType(e, ft)
	case MemberFunc, MemberImplMethod:
		it := u.c.cfg.Items.Get(mb.Item)
		d, ok := u.c.declOfItem(it).(*types.FuncDecl)
		if !ok || d.Sig.Receiver == types.NoReceiver {
			// A Type.Name function without a receiver is called in
			// namespace form; through a value it is not a method.
			u.walkArgs(e)
			u.err(diag.E0203, fe.Name.Span(), "`"+nm+"` has no receiver; call it as `"+u.str(v)+"."+nm+"(..)`").
				Text("type", u.str(v)).Fact("members", diag.TextList(nm))
			return types.Poison
		}
		if vt.Kind == types.KNamed && u.env != nil && !IfaceCallLowers(len(u.env.gen)) {
			// Another of an interface's own functions called on a value of
			// that interface in a function with no generics (`Shape.Four`
			// calling `self.Two()`): as for its methods (ifaceSelfMethod),
			// no instance knows the impl, which would be an internal
			// error in mono.
			if oi := u.c.cfg.Items.Get(vt.Item); oi.Kind == types.KindInterface {
				in := u.c.cfg.Interner
				u.err(diag.E0702, fe.Name.Span(), "`"+nm+"` on a value of interface `"+in.Str(oi.Name)+"` in a non-generic function has no impl of `"+in.Str(oi.Name)+"` to call").
					Text("interface", in.Str(oi.Name)).Text("type", u.str(v)).
					Alt("make it a generic function over `T: " + in.Str(oi.Name) + "`")
			}
		}
		u.unsafeCall(e, mb.Item, d) // E1101, as for a call by name
		cs := &callee{kind: UseMethod, item: mb.Item, sig: &d.Sig, recv: fe.X, explicit: idx, name: fe.Name, fe: fe}
		cs.recvPassed = u.receiver(fe.X, d.Sig.Receiver, v, n)
		return u.applyCall(e, cs, want)
	case MemberVariant:
		// `e.A(..)` through a value: a variant is constructed through
		// its type, never a silent Poison (lower's invariant).
		u.walkArgs(e)
		u.notFieldValue(fe, v, mb)
		return types.Poison
	}
	u.walkArgs(e)
	return types.Poison
}

// ifaceSelfMethod types a call of one of interface I's own methods on a
// value of type I[A], which only the self of a `Type.Name` function owned
// by I has (`Slice[T].Sort`'s body calling `self.ItemsMut()`): the method
// as a bound's would be (UseIfaceMethod, Self := I[A], I's parameters :=
// A). False when v is not an interface or I declares no such method.
func (u *unit) ifaceSelfMethod(e *ast.CallExpr, fe *ast.FieldExpr, v types.TypeID, n int, idx *ast.IndexExpr, want types.TypeID) (types.TypeID, bool) {
	vt := u.tab.Get(v)
	it := u.c.cfg.Items.Get(vt.Item)
	if it.Kind != types.KindInterface {
		return types.NoType, false
	}
	d, ok := u.c.declOfItem(it).(*types.InterfaceDecl)
	if !ok {
		return types.NoType, false
	}
	for k := range d.Methods {
		s := &d.Methods[k]
		if s.Name != fe.Name.Name {
			continue
		}
		u.x.rec.signature(it.Key)
		if u.env != nil {
			if !IfaceCallLowers(len(u.env.gen)) {
				// A non-generic `I.Name` (I has no type parameters) has no
				// instance in which self's impl is known, so the call
				// would be an interface call in an instance body: an
				// internal error in lower.
				in := u.c.cfg.Interner
				u.err(diag.E0702, fe.Name.Span(), "`"+in.Str(fe.Name.Name)+"` on a value of interface `"+in.Str(it.Name)+
					"` in a non-generic function has no impl of `"+in.Str(it.Name)+"` to call").
					Text("interface", in.Str(it.Name)).Text("type", u.str(v)).
					Alt("make it a generic function over `T: " + in.Str(it.Name) + "`")
			}
		}
		cs := &callee{kind: UseIfaceMethod, item: it.ID, index: uint32(k), sig: s, self: v, pre: vt.Args, explicit: idx, name: fe.Name, fe: fe}
		if s.Receiver != types.NoReceiver {
			cs.recv = fe.X
			cs.recvPassed = u.receiver(fe.X, s.Receiver, v, n)
		}
		return u.applyCall(e, cs, want), true
	}
	return types.NoType, false
}

// IfaceCallLowers reports whether a body with genericCount generic
// parameters (its combined list) may keep an interface call: mir's M2
// (validate.go, GenericCount == 0) refuses one in an instance body, where
// no impl is chosen. check refuses such a call on an interface-typed self
// where it is made (ifaceSelfMethod, E0702). lower does not assert it at
// the call: an error set's e.Format(w) is lowered as an interface call and
// then rewritten into a switch of direct calls.
func IfaceCallLowers(genericCount int) bool { return genericCount > 0 }

// sliceMethod gives slices Slice's methods: a `[]T`, `mut []T`, `[N]T` or
// `Slice[T]`
// implementer that has no member of this name gets the `Type.Name`
// methods std declares on `Slice[T]`, with `T` the element. The call is
// UseMethod of the Slice method; the receiver keeps its own type (a slice
// is passed as it is, anything else is borrowed per the receiver mode), and
// lower reaches the elements through Items or ItemsMut. False when the
// value has a member of that name or Slice declares none.
func (u *unit) sliceMethod(fe *ast.FieldExpr, v types.TypeID, n int, idx *ast.IndexExpr) (*callee, bool) {
	c := u.c
	if c.known.Slice == types.NoItem {
		return nil, false
	}
	vt := u.tab.Get(v)
	var elem types.TypeID
	switch vt.Kind {
	case types.KSlice, types.KMutSlice, types.KArray:
		elem = vt.Elem
	case types.KNamed:
		if vt.Item == c.known.Slice {
			return nil, false
		}
		if cands, _ := u.m.lookup(v, fe.Name.Name); len(cands) > 0 {
			return nil, false
		}
		t, ok := u.sliceElemOf(v)
		if !ok {
			return nil, false
		}
		elem = t
	default:
		return nil, false
	}
	cands, folded := u.m.lookup(u.tab.Named(c.known.Slice, []types.GenericArg{types.TypeArg(elem)}), fe.Name.Name)
	if folded || len(cands) != 1 || cands[0].Kind != MemberFunc {
		return nil, false
	}
	it := c.cfg.Items.Get(cands[0].Item)
	d, ok := c.declOfItem(it).(*types.FuncDecl)
	if !ok || d.Sig.Receiver == types.NoReceiver || len(d.Sig.Generics) == 0 {
		return nil, false
	}
	u.x.rec.signature(it.Key)
	u.privateMethod(fe.Name.Span(), cands[0])
	cs := &callee{kind: UseMethod, item: it.ID, sig: &d.Sig, recv: fe.X, explicit: idx, name: fe.Name, fe: fe,
		pre: []types.GenericArg{types.TypeArg(elem)}, preChecked: true}
	if b, ok := c.sliceImplicit(it); ok && !c.satisfies(u.env, elem, b, u.x.rec) {
		u.b.Diags = append(u.b.Diags, c.boundFailure(u.env, fe.Name.Span(), d.Sig.Generics[0].Name, b, elem, u.x.gens))
	}
	if (vt.Kind == types.KSlice || vt.Kind == types.KMutSlice) && n == 0 {
		cs.recvPassed = v
		if d.Sig.Receiver == types.ByMut {
			u.constMutation(fe.X, "borrowed mut")
		}
	} else {
		cs.recvPassed = u.receiver(fe.X, d.Sig.Receiver, v, n)
	}
	return cs, true
}

// receiver records the receiver's Adjust and returns the type self
// receives: &self forms &x (or reborrows a reference), mut self forms mut x
// (or reborrows; on &T it forms mut *x, which borrow rejects as E0507),
// self moves a place or copies through a reference (AdjCopyThrough; E0409
// for a non-plain T is borrow's). Deref is the layers removed first.
func (u *unit) receiver(x ast.Expr, mode types.Receiver, v types.TypeID, n int) types.TypeID {
	adj := Adjust{Deref: uint8(n + u.predDeref(x))}
	var passed types.TypeID
	switch mode {
	case types.ByShared:
		adj.Kind, passed = AdjRefShared, u.tab.Ref(v)
	case types.ByMut:
		u.constMutation(x, "borrowed mut")
		adj.Kind, passed = AdjRefMut, u.tab.Mut(v)
	default:
		passed = v
		if n > 0 {
			adj.Kind = AdjCopyThrough
		}
	}
	if adj.Kind != AdjNone || adj.Deref > 0 {
		*u.b.adjust(x.ID()) = adj
	}
	return passed
}

// callIfaceMethod is `x.M(..)` with x's type a type parameter: M must be a
// method of an interface among its bounds, else E0703.
func (u *unit) callIfaceMethod(e *ast.CallExpr, fe *ast.FieldExpr, v types.TypeID, n int, idx *ast.IndexExpr, want types.TypeID) types.TypeID {
	pi := u.tab.Get(v).Index
	if int(pi) < len(u.env.gen) {
		for _, b := range u.env.gen[pi].Bounds {
			if b.Kind != types.BoundInterface || b.Item == types.NoItem {
				continue
			}
			it := u.c.cfg.Items.Get(b.Item)
			d, ok := u.c.declOfItem(it).(*types.InterfaceDecl)
			if !ok {
				continue
			}
			for k := range d.Methods {
				s := &d.Methods[k]
				if s.Name != fe.Name.Name {
					continue
				}
				u.x.rec.signature(it.Key)
				cs := &callee{kind: UseIfaceMethod, item: b.Item, index: uint32(k), sig: s, self: v, pre: b.Args, explicit: idx, name: fe.Name, fe: fe}
				if s.Receiver != types.NoReceiver {
					cs.recv = fe.X
					cs.recvPassed = u.receiver(fe.X, s.Receiver, v, n)
				}
				return u.applyCall(e, cs, want)
			}
		}
	}
	u.walkArgs(e)
	u.needsBound(fe.Name.Span(), v, "method `"+u.c.cfg.Interner.Str(fe.Name.Name)+"`")
	return types.Poison
}

// builtinCall types a builtin member call: ?T.Must(), ?T.Or(v),
// ?T.IsSome(), ?T.IsNone(), E!T.Must(), Len() on slices, arrays and str.
// The receiver is read through its references (Adjust.Deref).
func (u *unit) builtinCall(e *ast.CallExpr, fe *ast.FieldExpr, b Builtin, v types.TypeID, n int, want types.TypeID) types.TypeID {
	if n > 0 {
		*u.b.adjust(fe.X.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n + u.predDeref(fe.X))}
	}
	use := Use{Kind: UseBuiltin, Index: uint32(b)}
	*u.b.use(e.ID()) = use
	*u.b.use(fe.ID()) = use
	u.setType(fe, types.Poison)
	vt := u.tab.Get(v)
	nm := u.c.cfg.Interner.Str(fe.Name.Name)
	switch b {
	case BuiltinOptMust, BuiltinErrMust:
		u.plainArgs(e, nil, nm+"()")
		return vt.Elem
	case BuiltinLen:
		u.plainArgs(e, nil, nm+"(): int")
		return types.Int
	}
	u.plainArgs(e, nil, nm+"(): bool")
	return types.Bool
}
