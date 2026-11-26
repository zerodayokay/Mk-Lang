// Why: a call fixes the order of every effect in it (callee, receiver,
// arguments, borrows, captured environments) and decides how each argument
// is passed; most of the language's evaluation-order and reborrow rules
// meet here.
//
// What: call, emitCall and emitCallAt (calls and their TermCall),
// borrowArg, isReborrow, isRecvReborrow and isRecvExclView (receivers and
// argument modes), environment (a capturing literal's environment),
// promote (variadic promotions), construct (struct, variant and error
// construction), builtin (universe calls), fnConst and constConversion.
//
// How: operands are formed in three passes. Pass 1 evaluates the values
// left to right, each read into a slot when a later operand could write it;
// pass 2 forms every borrow (&p, mut p, receiver borrows and reborrows)
// last; pass 3 builds each captured environment. A mut T or mut []T local
// passed to a mut parameter is reborrowed, not moved.

package lower

import (
	"mk/internal/ast"
	"mk/internal/check"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// errEdge is the error side a consuming construct gives a fallible call.
type errEdge struct {
	kind  mir.ErrorEdgeKind
	block mir.BlockID
	err   mir.LocalID
}

// call lowers a call expression that is not under try, else, match or Must.
func (bl *builder) call(x *ast.CallExpr) mir.Rvalue {
	c := bl.in.Call(x.ID())
	switch c.Kind {
	case CallStruct, CallVariant, CallError:
		return bl.construct(x, c)
	case CallBuiltin:
		return bl.builtin(x, c)
	case CallFunc, CallIface, CallValue:
		if c.Kind == CallFunc {
			// A call check folded to a constant (simd.Lanes[T]() of a
			// concrete lane type) is that
			// constant; it takes no argument, so nothing is skipped.
			if op, ok := bl.literal(x); ok && len(x.Args) == 0 {
				return use(op)
			}
		}
		dest := bl.emitCall(x, c, nil, mir.NoLocal)
		if !bl.open() {
			return use(mir.Imm(types.Never, 0))
		}
		return use(bl.valueOf(dest, true, x.Span()))
	}
	// some(x): check leaves it Poison; its type
	// is ?T of the operand.
	if id, ok := x.Fun.(*ast.Ident); ok && id.Name == source.SymSome && len(x.Args) == 1 {
		v := bl.operand(x.Args[0].Value)
		t := bl.in.Type(x.ID())
		if t == types.NoType || t == types.Poison {
			t = bl.tab.Optional(bl.b.OperandType(v))
		}
		return mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggSome, Ops: bl.b.AddOperands(v), Type: t}
	}
	bl.typeOf(x) // a Poison call (`some()` with no argument) is check's gap
	source.ICEf(x.Span(), "lower: call with no resolution")
	return mir.Rvalue{}
}

// pendingArg is one operand of a call in evaluation order.
type pendingArg struct {
	e      ast.Expr
	param  *types.Param // nil past the declared parameters (variadic) or for fn values
	recv   bool
	borrow bool // formed in pass 2
	// reborrow: a RecvPass receiver that is a whole mut T or mut []T local
	// (xs.Swap(0, 1) on xs: mut []T)
	reborrow bool
	// exclView: a RecvPass []T receiver of a mut self method (xs.Swap(0, 1)
	// on xs: []T), passed as the exclusive view RvSlice{Exclusive} of its
	// place, which borrow rejects as E0507
	exclView bool
	kind     mir.BorrowKind
	op       mir.Operand
	mode     mir.ArgMode
	done     bool
	// implicit: the () of a Print or Arena.Fmt written without its record
	// e is then the format argument, for spans only.
	implicit bool
}

// emitCall lowers the call's operands in evaluation order and emits the TermCall
// at the call's own span; it returns the destination place (the ok value of
// a fallible call).
func (bl *builder) emitCall(x *ast.CallExpr, c Call, edge *errEdge, spawn mir.LocalID) mir.Place {
	return bl.emitCallAt(x, c, edge, spawn, x.Span())
}

// emitCallAt is emitCall with the TermCall at site: a spawn's is its
// `s.go` expression, so the spawn's runtime site (SpawnAlloc's, a stack
// limit panic's) is the `s.go`, not the call inside it.
func (bl *builder) emitCallAt(x *ast.CallExpr, c Call, edge *errEdge, spawn mir.LocalID, site source.Span) mir.Place {
	// A scope statement's LocalScope destination is this call's own, never
	// a nested call's in the callee, receiver or arguments
	// (io.ScopeWith(N()) gives N a temporary).
	want := bl.callDest
	bl.callDest = mir.NoLocal
	tail := bl.tail
	bl.tail = false
	if tail && (c.Kind == CallIface || spawn != mir.NoLocal || edge != nil) {
		source.ICEf(x.Span(), "lower: become of a call through a bound, a spawn or an error edge (check's E0314)")
	}
	var callee mir.Callee
	var params []types.Param
	variadic := false
	switch c.Kind {
	case CallFunc:
		callee = mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: c.Item, Args: bl.b.AddGenericArgs(c.Args...)}}
	case CallIface:
		callee = mir.Callee{Kind: mir.CallInterface, Iface: c.Iface, Method: c.Method, SelfType: c.SelfType}
		// A method with its own generic parameters keeps their arguments in
		// Fn.Args (the last OwnGenerics of check's instantiation, after the
		// interface's): mono appends them to the impl's when it makes the
		// call direct.
		if c.Sig != nil && c.Sig.OwnGenerics > 0 {
			own := int(c.Sig.OwnGenerics)
			if len(c.Args) < own {
				source.ICEf(x.Span(), "lower: interface method call with %d generic arguments, method has %d of its own", len(c.Args), own)
			}
			callee.Fn.Args = bl.b.AddGenericArgs(c.Args[len(c.Args)-own:]...)
		}
	case CallValue:
		callee = mir.Callee{Kind: mir.CallValue, Value: bl.operand(x.Fun)}
	}
	if c.Sig != nil {
		params, variadic = c.Sig.Params, c.Sig.Variadic
		// The callee's own body raises the same ICE at its signature; a
		// call to it would otherwise fail mir.Validate untagged (fuzz input
		// f64a060f0ab47a8a: a parameter typed int.A).
		for i := range params {
			if params[i].Type == types.Poison {
				source.ICEf(x.Span(), "lower: a call to a callee with a parameter of Poison type and no diagnostic (check gap)")
			}
		}
		if c.Sig.Result == types.Poison {
			source.ICEf(x.Span(), "lower: a call to a callee whose result is Poison with no diagnostic (check gap)")
		}
	} else if c.Kind == CallValue && c.Recv == RecvNone {
		// A fn value's parameters are declared by its type: a mut T or
		// mut []T local passed to one is a reborrow too (not a move of
		// ys in f(ys)).
		if ft := bl.tab.Get(bl.b.OperandType(callee.Value)); ft.Kind == types.KFn && ft.Fn != nil {
			for _, fp := range ft.Fn.Params {
				params = append(params, types.Param{Type: fp.Type, Into: fp.Into})
			}
		}
	}
	var ps []pendingArg
	if c.Recv != RecvNone {
		fe, _ := methodField(x.Fun)
		pa := pendingArg{e: fe.X, recv: true}
		if len(params) > 0 {
			pa.param = &params[0]
		}
		switch c.Recv {
		case RecvRef:
			pa.borrow, pa.kind = true, mir.Shared
		case RecvMut:
			pa.borrow, pa.kind = true, mir.Exclusive
		case RecvPass:
			if spawn == mir.NoLocal && bl.isRecvReborrow(fe.X, c.Sig) {
				pa.borrow, pa.kind, pa.reborrow = true, mir.Exclusive, true
				if c.Sig.Receiver == types.ByShared && bl.tab.Kind(bl.typeOf(fe.X)) == types.KMutSlice {
					pa.kind = mir.Shared
				}
			} else if bl.isRecvExclView(fe.X, c.Sig) {
				pa.borrow, pa.kind, pa.exclView = true, mir.Exclusive, true
			}
		}
		ps = append(ps, pa)
		params = params[min(1, len(params)):]
	}
	for i, a := range x.Args {
		pa := pendingArg{e: a.Value}
		if i < len(params) {
			pa.param = &params[i]
		}
		// &p and mut p, mut xs[a..b] and &xs[a..b] included, are formed
		// in pass 2 (the whole place, its index expressions and bounds
		// checks, when the borrow is formed).
		if u, ok := unparen(a.Value).(*ast.UnaryExpr); ok && (u.Op == ast.TokAmp || u.Op == ast.TokMut) && spawn == mir.NoLocal {
			// &name of an extern var is a *T value, not a borrow: unary
			// lowers it (as a borrow it would be a &T assigned to the *T
			// argument).
			if bl.in.Use(u.X.ID()).Kind != UseConst && bl.tab.Kind(bl.typeOf(u)) != types.KRawPtr {
				pa.borrow, pa.kind = true, mir.Shared
				if u.Op == ast.TokMut {
					pa.kind = mir.Exclusive
				}
			}
		}
		if !pa.borrow && spawn == mir.NoLocal && bl.isReborrow(a.Value, pa.param) {
			pa.borrow = true
			pa.kind = mir.Exclusive
		}
		ps = append(ps, pa)
	}
	// The record a short Print or Arena.Fmt leaves out is
	// lowered as a written () would be, so both forms give one MIR.
	if len(x.Args) > 0 && len(x.Args) < len(params) && bl.in.ImplicitRecord(x.ID()) {
		ps = append(ps, pendingArg{e: x.Args[len(x.Args)-1].Value, param: &params[len(x.Args)], implicit: true})
	}
	// The callee value is read at its own position, before every operand
	// (h.f(Reset(mut h)) calls the f read first): a slot unless every operand
	// is inert for it.
	// A const fn parameter is never written and stays CallValue on its own
	// local, which mono makes direct.
	if callee.Kind == mir.CallValue && bl.readsLate(callee.Value) && !bl.constParamLocal(callee.Value.Place) && !bl.laterInert(ps, callee.Value.Place) {
		callee.Value = bl.slot(callee.Value, x.Fun.Span())
	}
	// Pass 1: every operand that is not a borrow form, left to right.
	for i := range ps {
		pa := &ps[i]
		if pa.borrow {
			continue
		}
		switch {
		case pa.implicit:
			pa.op = bl.emptyRecord(pa.e.Span())
		case pa.recv && c.Recv == RecvValue:
			pa.op = bl.valueOf(bl.place(pa.e), false, pa.e.Span())
		case pa.param != nil && pa.param.Const:
			if op, ok := bl.literal(pa.e); ok {
				pa.op = op
			} else if op, ok := bl.fnConst(pa.e); ok {
				pa.op = op
			} else {
				pa.op = bl.operand(pa.e)
			}
		default:
			pa.op = bl.operand(pa.e)
			if pa.param == nil && variadic && !pa.recv {
				pa.op = bl.promote(pa.op, pa.e.Span())
			}
		}
		// Every operand that runs after this one: the later operands of
		// pass 1, every borrow form of pass 2, and every capturing literal
		// whose environment is built in pass 3 (F(fn() { x += 1 }, x) reads
		// x before pass 3 forms mut x).
		if bl.readsLate(pa.op) && (!bl.laterInert(ps[i+1:], pa.op.Place) || !bl.laterInert(bl.lateForms(ps[:i]), pa.op.Place)) &&
			!(tail && bl.fixedParam(pa.op)) {
			pa.op = bl.slot(pa.op, pa.e.Span())
		}
		pa.mode = bl.argMode(pa.op, pa.e.Span())
	}
	// Pass 2: borrow forms, left to right.
	for i := range ps {
		pa := &ps[i]
		if !pa.borrow {
			continue
		}
		bl.borrowArg(pa, c, x.Span())
	}
	// Pass 3: the environment of each capturing literal written as a const
	// argument, in parameter order, after every operand of pass 1 and every
	// borrow form of pass 2; each is one more trailing argument. Nothing for
	// a call with no capturing literal.
	var envArgs []mir.Arg
	for i := range ps {
		pa := &ps[i]
		if pa.param == nil || !pa.param.Const || pa.implicit || pa.recv {
			continue
		}
		lit, ok := unparen(pa.e).(*ast.FnLit)
		if !ok {
			continue
		}
		env, ok := bl.in.Env(lit.ID())
		if !ok {
			continue
		}
		if spawn != mir.NoLocal {
			source.ICEf(lit.Span(), "lower: a capturing literal in an s.go call (E0609 is check's)")
		}
		envMode := mir.ArgCopy
		if bl.tab.Kind(env.Ref) == types.KMut {
			envMode = mir.ArgMove
		}
		envArgs = append(envArgs, mir.Arg{Operand: bl.environment(env, lit.Span()), Mode: envMode})
	}
	// Print and Arena.Fmt: the per-call thin body (format.go), after every
	// operand is formed, so the evaluation order is unchanged.
	// A format that is a forwarded `const` str parameter has no text
	// here; the call stays a plain call of Print or Arena.Fmt, and mono
	// judges and expands it per instance (internal/mono/format.go).
	// So does a record argument with a generic field.
	if tail {
		// become: nothing runs after the callee, whose result is
		// the function's; check refused every target that is not a Mk
		// function or a fn value of the caller's signature (E0312, E0314).
		if _, ok := bl.formatter(c); ok {
			source.ICEf(x.Span(), "lower: become of Print or Arena.Fmt (check's E0314)")
		}
		args := make([]mir.Arg, len(ps), len(ps)+len(envArgs))
		for i, pa := range ps {
			args[i] = mir.Arg{Operand: pa.op, Mode: pa.mode}
		}
		args = append(args, envArgs...)
		bl.term(mir.Terminator{Kind: mir.TermTailCall, Callee: callee, Args: bl.b.AddArgs(args...), Spawn: mir.NoLocal}, site)
		return mir.Place{}
	}
	var fmtFi fmtItems
	var fmtLines []fmtLine
	if fi, ok := bl.formatter(c); ok && bl.formatKnown(ps) && (len(c.Args) != 1 || !bl.hasParam(c.Args[0].Type)) {
		if spawn != mir.NoLocal {
			// The stack piece and argument arrays would not outlive the
			// spawner's statement (a spawn stores its arguments).
			source.ICEf(x.Span(), "lower: s.go of Print or Arena.Fmt is not synthesized")
		}
		fmtFi = fi
		callee, fmtLines = bl.formatCall(x, c, fi, ps)
	}
	args := make([]mir.Arg, len(ps), len(ps)+len(envArgs))
	for i, pa := range ps {
		args[i] = mir.Arg{Operand: pa.op, Mode: pa.mode}
	}
	args = append(args, envArgs...)
	union := bl.typeOf(x)
	ret := union
	if t := bl.tab.Get(ret); t.Kind == types.KErrorUnion && c.Errors != types.EmptyErrors {
		// A callee that cannot fail returns an E!T (a result type
		// parameter bound to one) whole, as a value.
		ret = t.Elem
	}
	if spawn != mir.NoLocal {
		ret = types.Void
	}
	// An E!T call under no try, else, match or Must, which check accepts as
	// an E!T value (var v: E!T = F(), an E!T argument): the ok value and the
	// error are joined into one E!T place (materialized), not an ICE
	// naming E0802.
	var mat *errEdge
	if c.Errors != types.EmptyErrors && spawn == mir.NoLocal && edge == nil && bl.tab.Kind(union) == types.KErrorUnion {
		mat = &errEdge{kind: mir.EdgeMatch, block: bl.newBlock(), err: bl.newTemp(bl.tab.ErrorSetType(c.Errors), x.Span())}
		edge = mat
	}
	dest := want
	if dest == mir.NoLocal || mat != nil {
		dest = bl.newTemp(ret, x.Span())
	}
	if fmtLines != nil {
		// An unpadded Print, straight-line (format.go): its calls in order,
		// each with this call's error edge, in place of the runner's call.
		bl.formatLines(fmtLines, fmtFi, args[0], bl.b.Place(dest), c, edge, site)
		if mat != nil {
			return bl.materialized(dest, mat, union, want, x.Span())
		}
		return bl.b.Place(dest)
	}
	if spawn == mir.NoLocal && len(args) == 2 && bl.isErrSetFormat(c) {
		// Format on an error-set value is the switch over its
		// members, each calling its provided Format directly.
		bl.errSetFormat(args[0].Operand, c.SelfType, args[1], bl.b.Place(dest), edge, site)
		if mat != nil {
			return bl.materialized(dest, mat, union, want, x.Span())
		}
		return bl.b.Place(dest)
	}
	target := bl.newBlock()
	tm := mir.Terminator{Kind: mir.TermCall, Callee: callee, Args: bl.b.AddArgs(args...), Dest: bl.b.Place(dest), Target: target, Spawn: spawn}
	if c.Errors != types.EmptyErrors && spawn == mir.NoLocal {
		if edge == nil {
			// The call's type is not E!T: check left it inconsistent.
			source.ICEf(x.Span(), "lower: a fallible call not under try, else, match or Must, E0802 (check gap)")
		}
		tm.HasErr = true
		tm.Err = mir.ErrorEdge{Kind: edge.kind, Block: edge.block, Error: edge.err}
	}
	bl.term(tm, site)
	bl.cur = target
	if mat != nil {
		return bl.materialized(dest, mat, union, want, x.Span())
	}
	if ret == types.Never {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, x.Span())
	} else if c.Kind == CallFunc && spawn == mir.NoLocal && bl.isCExtern(c.Item) {
		if d := bl.enumOf(ret); d != nil {
			bl.enumTagCheck(bl.b.Place(dest), d, x.Span())
		}
	}
	return bl.b.Place(dest)
}

// fixedParam reports whether op reads a whole parameter that is not var:
// no later operand can change it, so a tail call passes it as it is, and
// codegen forwards a memory-passed one's incoming pointer.
func (bl *builder) fixedParam(op mir.Operand) bool {
	if op.Kind != mir.OpCopy && op.Kind != mir.OpMove || op.Place.Proj.Len != 0 {
		return false
	}
	l := op.Place.Local
	return l >= 1 && uint32(l) <= bl.b.ParamCount && !bl.b.Locals[l].Var
}

// environment builds a capturing literal's environment at its call:
// t_i := &source_i, mut source_i, mut *source_i or mut source_i[..] for each
// capture in field order, e := record(t_1, .., t_n), r := &e (or mut e when
// any capture is mutable), all temporaries of the call's statement at the
// literal's span; it returns copy r (for &R) or move r (for mut R).
func (bl *builder) environment(env Env, at source.Span) mir.Operand {
	rt := bl.tab.Get(env.Ref)
	if rt.Kind != types.KRef && rt.Kind != types.KMut {
		source.ICEf(at, "lower: an environment of type %s", bl.tab.TypeString(env.Ref, bl.sig.Generics))
	}
	rec := rt.Elem
	fields := bl.tab.Get(rec).Fields
	if bl.tab.Kind(rec) != types.KRecord || len(fields) != len(env.Sources) {
		source.ICEf(at, "lower: an environment record of %d fields for %d captures", len(fields), len(env.Sources))
	}
	ops := make([]mir.Operand, len(env.Sources))
	for i, s := range env.Sources {
		var p mir.Place
		switch s.Kind {
		case UseLocal:
			p = bl.b.Place(bl.localOf(s.Binding, at))
		case UseParam:
			p = bl.b.Place(mir.LocalID(s.Param))
		case UseCapture:
			p = bl.capturePlace(s.Capture, s.Direct, at)
		default:
			source.ICEf(at, "lower: an environment source of kind %d", s.Kind)
		}
		ft := fields[i].Type
		mode := check.CapShared
		if i < len(env.Modes) {
			mode = env.Modes[i]
		}
		t := bl.newTemp(ft, at)
		switch mode {
		case check.CapMut:
			if bl.tab.Mut(bl.b.PlaceType(p)) != ft {
				source.ICEf(at, "lower: a mut environment field of type %s for a source of type %s", bl.tab.TypeString(ft, bl.sig.Generics), bl.tab.TypeString(bl.b.PlaceType(p), bl.sig.Generics))
			}
			bl.assign(bl.b.Place(t), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: p, Origin: mir.OriginPlain, Type: ft}, at)
		case check.CapReborrow:
			if bl.b.PlaceType(p) != ft {
				source.ICEf(at, "lower: a reborrowed environment field of type %s for a source of type %s", bl.tab.TypeString(ft, bl.sig.Generics), bl.tab.TypeString(bl.b.PlaceType(p), bl.sig.Generics))
			}
			if bl.tab.Kind(ft) == types.KMutSlice {
				bl.assign(bl.b.Place(t), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: p, Origin: mir.OriginPlain, Type: ft}, at)
			} else {
				bl.assign(bl.b.Place(t), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: bl.deref(p, at), Origin: mir.OriginPlain, Type: ft}, at)
			}
		default:
			if s.Kind == UseParam && bl.b.PlaceType(p) == ft {
				// A requires or ensures predicate holds the owner's parameter
				// or result as &P, and the capture is of P (check's
				// captureSource): borrow *p, a shared reborrow.
				p = bl.deref(p, at)
			}
			if bl.tab.Ref(bl.b.PlaceType(p)) != ft {
				source.ICEf(at, "lower: an environment field of type %s for a source of type %s", bl.tab.TypeString(ft, bl.sig.Generics), bl.tab.TypeString(bl.b.PlaceType(p), bl.sig.Generics))
			}
			bl.assign(bl.b.Place(t), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: p, Origin: mir.OriginPlain, Type: ft}, at)
		}
		ops[i] = bl.valueOfTemp(t, at)
	}
	e := bl.newTemp(rec, at)
	bl.assign(bl.b.Place(e), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggRecord, Type: rec, Ops: bl.b.AddOperands(ops...)}, at)
	r := bl.newTemp(env.Ref, at)
	if rt.Kind == types.KMut {
		bl.assign(bl.b.Place(r), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: bl.b.Place(e), Origin: mir.OriginPlain, Type: env.Ref}, at)
		return mir.Move(bl.b.Place(r))
	}
	bl.assign(bl.b.Place(r), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: bl.b.Place(e), Origin: mir.OriginPlain, Type: env.Ref}, at)
	return mir.Copy(bl.b.Place(r))
}

// emptyRecord is what operand gives for a written (): the empty record
// built into a temporary (recordLit with no fields), copied.
func (bl *builder) emptyRecord(at source.Span) mir.Operand {
	t := bl.tab.Record(nil)
	l := bl.newTemp(t, at)
	bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggRecord, Type: t, Ops: bl.b.AddOperands()}, at)
	return bl.valueOfTemp(l, at)
}

// laterInert reports whether every later operand of a call is inert for the
// read x: a borrow form (&p, mut p, an automatic receiver borrow, a
// reborrow) must also be disjoint from x.
func (bl *builder) laterInert(ps []pendingArg, x mir.Place) bool {
	for _, p := range ps {
		if p.borrow {
			e := unparen(p.e)
			if u, ok := e.(*ast.UnaryExpr); ok && (u.Op == ast.TokMut || u.Op == ast.TokAmp) {
				e = u.X
			}
			if !bl.inert(e) || !bl.disjoint(e, x, p.kind == mir.Exclusive) {
				return false
			}
			continue
		}
		// An implicit () counts as the written one, a RecordLit, which is
		// never inert: the short form's MIR equals the () form's.
		if p.implicit || !bl.inertFor(p.e, x) {
			return false
		}
	}
	return true
}

// lateForms are the pass-2 borrow forms and pass-3 capturing literals of ps.
func (bl *builder) lateForms(ps []pendingArg) []pendingArg {
	var out []pendingArg
	for _, p := range ps {
		if p.borrow {
			out = append(out, p)
			continue
		}
		if p.param != nil && p.param.Const && !p.implicit && !p.recv {
			if lit, ok := unparen(p.e).(*ast.FnLit); ok {
				if _, ok := bl.in.Env(lit.ID()); ok {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// isReborrow is the reborrow rule: a mut T or mut []T local, parameter or
// capture passed with no projection to a declared mut parameter with no into.
func (bl *builder) isReborrow(e ast.Expr, p *types.Param) bool {
	if p == nil || p.Into != types.NoParam {
		return false
	}
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	u := bl.in.Use(id.ID())
	if (u.Kind != UseLocal && u.Kind != UseParam && u.Kind != UseCapture) || bl.in.Adjust(id.ID()).Kind != AdjNone || bl.in.Convert(id.ID()).Kind != ConvNone {
		return false
	}
	pk := bl.tab.Kind(p.Type)
	return (pk == types.KMut || pk == types.KMutSlice) && bl.tab.Kind(bl.typeOf(id)) == pk
}

// isRecvReborrow is the receiver rule for RecvPass: a whole mut T or
// mut []T local or parameter passed as itself to a &self or mut self
// receiver (check passes a slice to a Slice[T] method with no Adjust) is a
// reborrow, not a move. A &self receiver of a mut []T takes a shared view
// instead, RvSlice{Shared} into a []T temporary (as a for loop's view
// does), so it is passed as a []T receiver is; a &self
// receiver of a whole mut T reborrows. A move would end the local. A self
// receiver takes the value by the argument rules. The receiver's `into`
// changes neither rule, as a dot call's automatic `mut *c` reborrows
// whatever the clause.
func (bl *builder) isRecvReborrow(e ast.Expr, sig *types.Signature) bool {
	if sig == nil || len(sig.Params) == 0 {
		return false
	}
	if sig.Receiver != types.ByMut && sig.Receiver != types.ByShared {
		return false
	}
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	u := bl.in.Use(id.ID())
	if bl.in.Adjust(id.ID()).Kind != AdjNone || bl.in.Convert(id.ID()).Kind != ConvNone {
		return false
	}
	k := bl.tab.Kind(bl.typeOf(id))
	switch u.Kind {
	case UseLocal, UseParam, UseCapture:
		return k == types.KMut || k == types.KMutSlice
	}
	return false
}

// isRecvExclView is the receiver rule for a mut self method on a shared
// slice: a RecvPass receiver of type []T (check passes a slice to a
// Slice[T] method with no Adjust) to a mut self receiver. It is the
// analogue of check's mut *x for a mut self on &T: lower forms the
// exclusive receiver, RvSlice{Exclusive} of the []T place into a mut []T
// temporary, and borrow rejects it as E0507 (such a method needs a
// mut []T, a var array, a List or a HeapList). A plain copy xs, a []T into
// a mut []T, would pass check and borrow and fail only in mono.
func (bl *builder) isRecvExclView(e ast.Expr, sig *types.Signature) bool {
	if sig == nil || sig.Receiver != types.ByMut || len(sig.Params) == 0 {
		return false
	}
	return bl.tab.Kind(bl.typeOf(e)) == types.KSlice
}

// borrowArg forms a pass-2 operand: &p, mut p, a receiver borrow, or a
// reborrow. An automatic receiver borrow's RvRef carries the span of the
// receiver's call, so E0505 on it points at the call.
func (bl *builder) borrowArg(pa *pendingArg, c Call, call source.Span) {
	at := pa.e.Span()
	if pa.exclView {
		p := bl.place(pa.e)
		mt := bl.tab.MutSlice(bl.tab.Get(bl.b.PlaceType(p)).Elem)
		l := bl.newTemp(mt, at)
		bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: p, Type: mt}, call)
		pa.op = bl.valueOfTemp(l, at)
		pa.mode = bl.argMode(pa.op, at)
		return
	}
	if pa.reborrow {
		p := bl.place(pa.e)
		if pa.kind == mir.Shared {
			// &self of a mut []T: a shared view, as a []T receiver is passed.
			st := bl.tab.Slice(bl.tab.Get(bl.b.PlaceType(p)).Elem)
			l := bl.newTemp(st, at)
			bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Shared, Place: p, Type: st}, call)
			pa.op = bl.valueOfTemp(l, at)
			pa.mode = bl.argMode(pa.op, at)
			return
		}
		if p.Proj.Len > 0 {
			pa.op, pa.mode = bl.reborrowPlace(p, call, at)
			return
		}
		pa.op = mir.Copy(p)
		pa.mode = mir.ArgReborrow
		return
	}
	if !pa.recv {
		if u, ok := unparen(pa.e).(*ast.UnaryExpr); ok && (u.Op == ast.TokAmp || u.Op == ast.TokMut) {
			if _, isSlice := bl.sliceBorrow(u); isSlice {
				// A slice borrow is its RvSlice value, not a reference to
				// it: unary lowers it through sliceExpr, bounds and all.
				pa.op = bl.operand(pa.e)
				pa.mode = bl.argMode(pa.op, at)
				return
			}
			var p mir.Place
			if s, ok := bl.mutSliceDeref(u); ok && bl.isPlaceExpr(s) {
				p = bl.mutPlace(s) // `mut *s` reborrows s
			} else if ok {
				p = bl.tempOf(s)
			} else if bl.isPlaceExpr(u.X) && pa.kind == mir.Exclusive {
				p = bl.mutPlace(u.X)
			} else if bl.isPlaceExpr(u.X) {
				p = bl.place(u.X)
			} else {
				p = bl.tempOf(u.X)
			}
			t := bl.typeOf(u)
			l := bl.newTemp(t, at)
			bl.assign(bl.b.Place(l), bl.ref(pa.kind, p, t), u.Span())
			pa.op = bl.valueOfTemp(l, at)
			pa.mode = bl.argMode(pa.op, at)
			return
		}
		// Reborrow of a local, parameter or capture.
		p := bl.place(pa.e)
		if p.Proj.Len > 0 {
			pa.op, pa.mode = bl.reborrowPlace(p, call, at)
			return
		}
		pa.op = mir.Copy(p)
		pa.mode = mir.ArgReborrow
		return
	}
	// Receiver: Adjust.Deref layers, then the borrow. A whole mut T local
	// with one layer reborrows; a whole &T local is copied.
	adj := bl.in.Adjust(pa.e.ID())
	if id, ok := unparen(pa.e).(*ast.Ident); ok && adj.Deref == 1 {
		// A captured &T is copied as a &T local is; a captured mut T has a
		// projection ((*env).x), so it falls through to emit an RvRef{Exclusive}
		// temporary below (ArgReborrow is for whole locals only).
		if u := bl.in.Use(id.ID()); u.Kind == UseLocal || u.Kind == UseParam || u.Kind == UseCapture && pa.kind == mir.Shared {
			base := bl.placeProper(id)
			k := bl.tab.Kind(bl.b.PlaceType(base))
			if (k == types.KRef || k == types.KMut) && bl.scopeOf(bl.tab.Get(bl.b.PlaceType(base)).Elem) {
				// A &Scope[E] to a Scope method: borrow *base's header
				// (DF-chat-01), below.
				k = types.KNone
			}
			if pa.kind == mir.Exclusive && k == types.KMut {
				pa.op, pa.mode = mir.Copy(base), mir.ArgReborrow
				return
			}
			if pa.kind == mir.Shared && k == types.KRef {
				pa.op, pa.mode = mir.Copy(base), mir.ArgCopy
				return
			}
		}
	}
	var p mir.Place
	if bl.isPlaceExpr(pa.e) && pa.kind == mir.Exclusive {
		// A mut self receiver is a write's place, so an
		// implementer index goes through ItemsMut, as for xs[i].f = v.
		p = bl.mutPlace(pa.e)
	} else if bl.isPlaceExpr(pa.e) {
		p = bl.place(pa.e)
	} else {
		p = bl.tempOf(pa.e)
		for n := adj.Deref; n > 0; n-- {
			p = bl.deref(p, at)
		}
	}
	p = bl.scopeHeader(p)
	vt := bl.b.PlaceType(p)
	t := bl.tab.Ref(vt)
	if pa.kind == mir.Exclusive {
		t = bl.tab.Mut(vt)
	}
	if pa.param != nil && bl.tab.Kind(pa.param.Type) != types.KSelf && bl.tab.Kind(pa.param.Type) != types.KParam {
		if pk := bl.tab.Kind(pa.param.Type); pk == types.KRef || pk == types.KMut {
			// keep the computed type; the parameter's own type may name Self
		}
	}
	l := bl.newTemp(t, at)
	bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvRef, Borrow: pa.kind, Place: p, Origin: mir.OriginPlain, Type: t}, call)
	pa.op = bl.valueOfTemp(l, at)
	pa.mode = bl.argMode(pa.op, at)
}

// reborrowPlace forms a temporary reborrow of a projected mut T or mut []T
// place (such as a capture (*env).x): MIR's ArgReborrow is for whole locals
// only, so a projected place reborrows through an RvRef or RvSlice temporary
// passed by ArgMove.
func (bl *builder) reborrowPlace(p mir.Place, call, at source.Span) (mir.Operand, mir.ArgMode) {
	t := bl.b.PlaceType(p)
	l := bl.newTemp(t, at)
	var rv mir.Rvalue
	switch bl.tab.Kind(t) {
	case types.KMutSlice:
		rv = mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: p, Type: t}
	case types.KMut:
		rv = mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: bl.deref(p, at), Origin: mir.OriginPlain, Type: t}
	default:
		source.ICEf(at, "lower: reborrowPlace on %s", bl.tab.TypeString(t, nil))
	}
	bl.assign(bl.b.Place(l), rv, call)
	op := bl.valueOfTemp(l, at)
	return op, bl.argMode(op, at)
}

// scopeHeader is a Scope[E] receiver's Scope: field 0 of Scope[E]'s layout,
// so a Scope method (Cancel) called on a Scope[E] borrows the header it is
// declared on (passing the whole Scope[E] for a &Scope is an ICE in
// mir.Validate). Any other place is itself.
func (bl *builder) scopeHeader(p mir.Place) mir.Place {
	t := bl.b.PlaceType(p)
	if !bl.scopeOf(t) {
		return p
	}
	return bl.extend(p, mir.Projection{Kind: mir.ProjField, A: 0, Type: bl.tab.Named(bl.tab.Get(t).Item, nil)})
}

// scopeOf reports whether t is a Scope[E].
func (bl *builder) scopeOf(t types.TypeID) bool {
	st := bl.tab.Get(t)
	return st.Kind == types.KNamed && bl.cfg.Known != nil && st.Item == bl.cfg.Known.Scope && len(st.Args) == 1
}

func (bl *builder) argMode(op mir.Operand, at source.Span) mir.ArgMode {
	if op.Kind == mir.OpMove {
		return mir.ArgMove
	}
	if op.Kind == mir.OpCopy && !bl.plain(bl.b.OperandType(op), at) {
		return mir.ArgMove
	}
	return mir.ArgCopy
}

// promote applies the C default promotions to a variadic argument.
func (bl *builder) promote(op mir.Operand, at source.Span) mir.Operand {
	t := bl.tab.Get(bl.b.OperandType(op))
	var to types.TypeID
	switch {
	case t.Kind == types.KFloat && t.Bits == 32:
		to = types.F64
	case t.Kind == types.KBool:
		to = types.I32
	case t.Kind == types.KInt && t.Bits < 32:
		to = types.U32
		if t.Signed {
			to = types.I32
		}
	default:
		return op
	}
	l := bl.newTemp(to, at)
	bl.assign(bl.b.Place(l), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastLossless, A: op, Type: to}, at)
	return mir.Copy(bl.b.Place(l))
}

// construct is struct, variant and error construction: written order,
// declaration-order operands.
func (bl *builder) construct(x *ast.CallExpr, c Call) mir.Rvalue {
	t := bl.typeOf(x)
	var n int
	agg := mir.AggStruct
	switch d := bl.cfg.Items.Get(c.Item).Decl.(type) {
	case *types.StructDecl:
		n = len(d.Fields)
	case *types.EnumDecl:
		agg = mir.AggEnum
		if int(c.Variant) < len(d.Variants) {
			n = len(d.Variants[c.Variant].Fields)
		}
	case *types.ErrorDecl:
		agg = mir.AggError
		n = len(d.Fields)
	}
	vals := make([]ast.Expr, len(x.Args))
	for i, a := range x.Args {
		vals[i] = a.Value
	}
	written := bl.operandsInOrder(vals)
	ops := make([]mir.Operand, n)
	for i := range written {
		k := uint32(i)
		if i < len(c.Fields) {
			k = c.Fields[i]
		}
		if int(k) >= n {
			source.ICEf(x.Span(), "lower: construction field %d of %d", k, n)
		}
		ops[k] = written[i]
	}
	return mir.Rvalue{Kind: mir.RvAggregate, Agg: agg, Item: c.Item, Variant: c.Variant, Type: t, Ops: bl.b.AddOperands(ops...)}
}

// recvExpr is the receiver of a builtin member call.
func recvExpr(x *ast.CallExpr) ast.Expr {
	if fe, ok := methodField(x.Fun); ok {
		return fe.X
	}
	return nil
}

// methodField is the member FieldExpr of a method call's callee: x.M, or
// x.M[A] with explicit generic arguments (an IndexExpr over the member).
func methodField(fun ast.Expr) (*ast.FieldExpr, bool) {
	switch f := fun.(type) {
	case *ast.FieldExpr:
		return f, true
	case *ast.IndexExpr:
		fe, ok := f.X.(*ast.FieldExpr)
		return fe, ok
	}
	return nil, false
}

// optPlace is the place of a ?T receiver, its value in a temporary.
func (bl *builder) optPlace(e ast.Expr) mir.Place {
	if bl.isPlaceExpr(e) {
		return bl.place(e)
	}
	p := bl.tempOf(e)
	for n := bl.in.Adjust(e.ID()).Deref; n > 0; n-- {
		p = bl.deref(p, e.Span())
	}
	return p
}

// optTaken is the whole temporary a consuming ?T form reads (else on a ?T,
// Must): t = operand(e), Move or Copy of a place, so the some payload is
// taken out of a LocalTemp root, never moved out of the user's local or
// parameter itself (that would be E0409); a value is its temporary already.
func (bl *builder) optTaken(e ast.Expr) mir.Place {
	if !bl.isPlaceExpr(e) {
		return bl.optPlace(e)
	}
	p := bl.place(e)
	l := bl.newTemp(bl.b.PlaceType(p), e.Span())
	bl.assign(bl.b.Place(l), use(bl.valueOf(p, true, e.Span())), e.Span())
	return bl.b.Place(l)
}

// some is t.Downcast(Some).Field(0).
func (bl *builder) somePayload(p mir.Place) mir.Place {
	ot := bl.b.PlaceType(p)
	return bl.extend(p, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelSome, Type: ot},
		mir.Projection{Kind: mir.ProjField, A: 0, Type: bl.tab.Get(ot).Elem})
}

// discr is d = RvDiscriminant(p) into a fresh temporary.
func (bl *builder) discr(p mir.Place, at source.Span) mir.Operand {
	d := bl.newTemp(types.U64, at)
	bl.assign(bl.b.Place(d), mir.Rvalue{Kind: mir.RvDiscriminant, Place: p, Type: types.U64}, at)
	return mir.Copy(bl.b.Place(d))
}

// panicBlock makes a block that panics with code (and message).
func (bl *builder) panicBlock(code types.PanicCode, msg *mir.Operand, at source.Span) mir.BlockID {
	save := bl.cur
	p := bl.newBlock()
	bl.cur = p
	t := mir.Terminator{Kind: mir.TermPanic, Panic: code}
	if msg != nil {
		t.HasMsg, t.Msg = true, *msg
	}
	bl.term(t, at)
	bl.cur = save
	return p
}

// fnConst is a named function with closed generic arguments as a constant
// operand: a fn value with one RelocFn, so mono keys the
// instance on it without tracing temporaries.
func (bl *builder) fnConst(e ast.Expr) (mir.Operand, bool) {
	// A direct call records its callee's Use on the call node itself, so a
	// call (`Lane(Two())`, `Apply(Pick(), 3)`) would read as the named
	// function: an int const parameter then took a fn constant's zero
	// bytes, and a fn one the callee itself. A call is
	// no constant (check's E0705); its value is an ordinary operand, which
	// mono's constArgOf refuses.
	if _, call := unparen(e).(*ast.CallExpr); call {
		return mir.Operand{}, false
	}
	u := bl.in.Use(unparen(e).ID())
	if u.Kind != UseFunc {
		return mir.Operand{}, false
	}
	for _, a := range u.Args {
		if !a.IsConst && bl.mentionsParam(a.Type) {
			return mir.Operand{}, false
		}
	}
	t := bl.typeOf(e)
	v := types.ConstValue{Type: t, Align: 8, Bytes: make([]byte, 8),
		Relocs: []types.Reloc{{Offset: 0, Kind: types.RelocFn, Fn: types.FnRef{Item: u.Item, Args: u.Args}}}}
	if err := v.Canonicalize(8); err != nil {
		source.ICEf(e.Span(), "lower: fn constant: %v", err)
	}
	return mir.Const(t, bl.cfg.Consts.Intern(v)), true
}

// mentionsParam reports whether t mentions a generic parameter or Self.
func (bl *builder) mentionsParam(t types.TypeID) bool {
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KParam, types.KSelf:
		return true
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KArray, types.KErrorUnion:
		return bl.mentionsParam(ty.Elem)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && bl.mentionsParam(a.Type) {
				return true
			}
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			if bl.mentionsParam(f.Type) {
				return true
			}
		}
	case types.KFn:
		return true // conservative: no fn type walk here
	}
	return false
}

// constConversion folds T(c) for an integer constant c into an integer T:
// check judged it by value (E0303), so it fits T, and a narrowing
// CastLossless would not be value-defined.
func (bl *builder) constConversion(a ast.Expr, to types.TypeID) (mir.Operand, bool) {
	tt := bl.tab.Get(to)
	if !tt.IsInteger() {
		return mir.Operand{}, false
	}
	l := bl.in.Literal(unparen(a).ID())
	if l.Kind != LitScalar || !bl.tab.Get(l.Type).IsInteger() {
		return mir.Operand{}, false
	}
	bits := uint8(64)
	if tt.Kind != types.KIntPtr && tt.Bits > 0 {
		bits = tt.Bits
	}
	v := l.Bits
	if bits < 64 {
		v &= 1<<bits - 1
	}
	return mir.Imm(to, v), true
}

// builtin lowers a universe call inline.
func (bl *builder) builtin(x *ast.CallExpr, c Call) mir.Rvalue {
	at := x.Span()
	arg := func(i int) ast.Expr {
		if i >= len(x.Args) {
			source.ICEf(at, "lower: builtin with %d arguments", len(x.Args))
		}
		return x.Args[i].Value
	}
	switch c.Builtin {
	case BAssert:
		cond := bl.operand(arg(0))
		ok := bl.newBlock()
		bl.boolSwitch(cond, ok, bl.panicBlock(types.PanicAssert, nil, at), at)
		bl.cur = ok
		return bl.voidValue(at)
	case BPanic:
		msg := bl.operand(arg(0))
		bl.term(mir.Terminator{Kind: mir.TermPanic, Panic: types.PanicExplicit, HasMsg: true, Msg: msg}, at)
		return use(mir.Imm(types.Never, 0))
	case BConvert, BCast, BTrunc:
		// A conversion check folded (a constant judged by value)
		// is its value: Lossless never narrows.
		if op, ok := bl.literal(x); ok && op.Kind == mir.OpImm {
			return use(op)
		}
		if c.Builtin == BConvert {
			if op, ok := bl.constConversion(arg(0), bl.typeOf(x)); ok {
				return use(op)
			}
		}
		v := bl.operand(arg(0))
		k := mir.CastLossless
		if c.Builtin == BCast && !c.Lossless {
			k = mir.CastChecked
		}
		if c.Builtin == BTrunc {
			k = mir.CastTrunc
		}
		return mir.Rvalue{Kind: mir.RvCast, Cast: k, A: v, Type: bl.typeOf(x)}
	case BSizeOf, BAlignOf:
		idx, ok := x.Fun.(*ast.IndexExpr)
		if !ok || len(idx.Args) != 1 {
			source.ICEf(at, "lower: SizeOf without a type argument")
		}
		t := bl.in.Type(idx.Args[0].ID())
		if t == types.NoType || t == types.Poison {
			if op, ok := bl.literal(x); ok {
				return use(op)
			}
			// An open type argument (SizeOf[T]() in a generic body): check
			// computes the type but records it nowhere.
			t = bl.typeOf(idx.Args[0])
		}
		k := mir.RvSizeOf
		if c.Builtin == BAlignOf {
			k = mir.RvAlignOf
		}
		return mir.Rvalue{Kind: k, Type: t}
	case BEvalArena:
		t := bl.typeOf(x)
		dest := bl.newTemp(t, at)
		target := bl.newBlock()
		bl.term(mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallBuiltin, Builtin: mir.BuiltinEvalArena},
			Dest: bl.b.Place(dest), Target: target, Spawn: mir.NoLocal}, at)
		bl.cur = target
		return use(bl.valueOfTemp(dest, at))
	case BLen:
		r := recvExpr(x)
		return mir.Rvalue{Kind: mir.RvLen, Place: bl.optPlace(r), Type: types.Int}
	case BOptIsSome, BOptIsNone:
		d := bl.discr(bl.optPlace(recvExpr(x)), at)
		v := uint64(0)
		if c.Builtin == BOptIsSome {
			v = 1
		}
		return mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: d, B: mir.Imm(types.U64, v), Type: types.Bool}
	case BOptMust:
		p := bl.optTaken(recvExpr(x))
		d := bl.discr(p, at)
		ok := bl.newBlock()
		bl.switchOn(d, []mir.SwitchCase{{Value: 1, Target: ok}}, bl.panicBlock(types.PanicMust, nil, at), at)
		bl.cur = ok
		return use(bl.valueOf(bl.somePayload(p), true, at))
	case BResMust:
		inner, ok := unparen(recvExpr(x)).(*ast.CallExpr)
		if !ok {
			source.ICEf(at, "lower: Must on a value that is not a call")
		}
		ic := bl.in.Call(inner.ID())
		if ic.Errors == types.EmptyErrors {
			// A callee that cannot fail returning an E!T value (a result
			// type parameter bound to one): its discriminant picks ok's
			// payload or the panic with the error, as elseUnion's does.
			p := bl.optTaken(inner)
			e := bl.newTemp(bl.tab.ErrorSetType(bl.tab.Get(bl.b.PlaceType(p)).Errors), at)
			ok, pb := bl.newBlock(), bl.newBlock()
			bl.unionSwitch(p, ok, pb, at)
			bl.cur = pb
			bl.assign(bl.b.Place(e), use(bl.valueOf(bl.errPayload(p), true, at)), at)
			bl.term(mir.Terminator{Kind: mir.TermPanic, Panic: types.PanicMust, HasMsg: true, Msg: bl.valueOfTemp(e, at)}, at)
			bl.cur = ok
			return use(bl.valueOf(bl.okPayload(p), true, at))
		}
		et := bl.tab.ErrorSetType(ic.Errors)
		e := bl.newTemp(et, at)
		save := bl.cur
		pb := bl.newBlock()
		bl.cur = pb
		msg := bl.valueOfTemp(e, at)
		bl.term(mir.Terminator{Kind: mir.TermPanic, Panic: types.PanicMust, HasMsg: true, Msg: msg}, at)
		bl.cur = save
		dest := bl.emitCall(inner, ic, &errEdge{kind: mir.EdgeMust, block: pb, err: e}, mir.NoLocal)
		return use(bl.valueOf(dest, true, at))
	}
	source.ICEf(at, "lower: builtin %d not handled", c.Builtin)
	return mir.Rvalue{}
}

// voidValue is the value of a void expression: the void constant, which a
// statement drops and a binding (`x := r else { }` on an E!void call)
// assigns. A record aggregate of void would fail mir.Validate's type check
// wherever it is assigned.
func (bl *builder) voidValue(at source.Span) mir.Rvalue {
	c := bl.cfg.Consts.Intern(types.ConstValue{Type: types.Void, Align: 1})
	return use(mir.Const(types.Void, c))
}

// constParamLocal reports whether p is a whole const value parameter.
func (bl *builder) constParamLocal(p mir.Place) bool {
	l := bl.b.Locals[p.Local]
	if p.Proj.Len != 0 || l.Kind != mir.LocalParam {
		return false
	}
	i := int(l.Param) - 1
	return i >= 0 && i < len(bl.sig.Params) && bl.sig.Params[i].Const
}

// materialized joins a materialized E!T call (emitCallAt): ok(dest) on the
// call's target, err(e) on its EdgeMatch block, into want when it has the
// E!T type, else a fresh temporary; the current block is the join.
func (bl *builder) materialized(dest mir.LocalID, mat *errEdge, union types.TypeID, want mir.LocalID, at source.Span) mir.Place {
	u := want
	if u == mir.NoLocal || bl.b.Locals[u].Type != union {
		u = bl.newTemp(union, at)
	}
	join := bl.newBlock()
	if bl.b.Locals[dest].Type == types.Never {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, at)
	}
	if bl.open() {
		var okOps mir.Range
		if bl.b.Locals[dest].Type != types.Void {
			okOps = bl.b.AddOperands(mir.Move(bl.b.Place(dest)))
		}
		bl.assign(bl.b.Place(u), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggOk, Ops: okOps, Type: union}, at)
		bl.gotoBlock(join, at)
	}
	bl.cur = mat.block
	bl.assign(bl.b.Place(u), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggErr, Ops: bl.b.AddOperands(mir.Move(bl.b.Place(mat.err))), Type: union}, at)
	bl.gotoBlock(join, at)
	bl.cur = join
	return bl.b.Place(u)
}
