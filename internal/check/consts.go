// Why: an untyped const's type must be known before any reader sees it,
// and a compile-time requires clause must be judged at every call and fn
// value it can be judged at, inside generics too, without ever giving a
// false diagnostic.
//
// What: typeConsts, typeConst and typeCycle (untyped consts, E0909);
// constRequires, valueRequires and requiresAt (E0706 at a concrete call or
// value); restatedRequires and its helpers (a call inside a generic);
// foldedRequires; genericSubs and spellVariant (spelling arguments).
//
// How: with closed arguments a clause is evaluated or folded. Inside a
// generic it is spelled with the call's arguments and must match one of the
// caller's own requires, or is judged over the caller's finite const
// generics; what can be neither is E0706, unless the entry check judges it.

package check

import (
	"math/big"
	"strconv"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// constValueNode is the initializer of a const unit.
func constValueNode(u *unit) ast.Expr {
	if n, ok := u.b.Node.(*ast.ConstDecl); ok {
		return n.Value
	}
	return nil
}

// constTyping is the state of an untyped const's stage-5 typing.
type constTyping uint8

const (
	ctNone constTyping = iota
	ctBusy
	ctDone
)

// typeConsts types every untyped const of the module in item order, then
// runs its Plain check. The initializer's diagnostics are its unit's
// in stage 6 and are dropped here. It runs after initEarly, so a repeat
// count or const argument naming an early const checks and Prepares that
// const on demand and evaluates it.
func (m *Module) typeConsts() {
	for _, ii := range m.info.Items {
		it := m.c.cfg.Items.Get(ii.Item)
		if it.Kind != types.KindConst {
			continue
		}
		m.guard(it, func() {
			d, ok := it.Decl.(*types.ConstDecl)
			if !ok || d.Type != types.NoType {
				return
			}
			if t := m.typeConst(it); t != types.NoType && t != types.Poison {
				m.constPlain(it, t)
			}
		})
	}
}

// untypeProvisional forgets the typing of every untyped const typed on
// demand before the early set existed (a declaration's type in step 4 asks
// declConstArg for it) whose typing gave no type: a repeat count or a const
// argument naming an early const could not be evaluated then and was
// Poison with no diagnostic. typeConsts, which runs once early consts can
// be checked and evaluated on demand, types them again. The
// declaration types already built keep what they had.
func (m *Module) untypeProvisional() {
	for id, st := range m.d.consts {
		if st != ctDone {
			continue
		}
		if d, ok := m.c.cfg.Items.Get(id).Decl.(*types.ConstDecl); ok && d.Type == types.NoType {
			m.d.consts[id] = ctNone
		}
	}
}

// typeConst types one untyped const's initializer, on demand from another
// const's (a const met while its own typing is in progress is a cycle,
// E0909's, and stays NoType here).
func (m *Module) typeConst(it *types.Item) types.TypeID {
	d, ok := it.Decl.(*types.ConstDecl)
	if !ok {
		return types.NoType
	}
	if d.Type != types.NoType || m.d == nil {
		return d.Type
	}
	if m.d.consts == nil {
		m.d.consts = map[types.ItemID]constTyping{}
	}
	switch m.d.consts[it.ID] {
	case ctBusy:
		m.typeCycle(it.ID)
		return d.Type
	case ctDone:
		return d.Type
	}
	m.d.consts[it.ID] = ctBusy
	m.d.typing = append(m.d.typing, it.ID)
	defer func() {
		m.d.consts[it.ID] = ctDone
		m.d.typing = m.d.typing[:len(m.d.typing)-1]
	}()
	u := m.newUnit(it)
	t := u.initializer(constValueNode(u), types.NoType)
	if t == types.Poison {
		// Poison never reaches a Decl. A reader of the value treats
		// NoType as Poison (Body.PoisonReads).
		t = types.NoType
	}
	d.Type = t
	return t
}

// constRequires is the compile-time requires at a concrete call: each
// requires clause of the callee whose predicate names only const parameters
// is evaluated (EvalContract, StageSignatures, at the call) with the
// call's folded const arguments; false is E0706 with fact clause. An
// argument check cannot fold (a forwarded const parameter in a generic
// caller, a call) leaves the clause unchecked here; one
// whose argument is the caller's own const parameter is restated by the
// caller (forwardedRequires).
func (u *unit) constRequires(e *ast.CallExpr, sig *types.Signature, rest []types.Param, inst uint32) {
	u.requiresAt(e, e, sig, rest, inst)
}

// valueRequires is constRequires for a function named as a fn value (`F`,
// `F[4]`, `T[A].M`, `T[A].M[B]`): the compile-time clauses are judged
// at the value e with its generic arguments (instance inst, 0 for none), as
// at a call, and inside a generic the clause over the caller's const
// generics is restated as at a call (restatedRequires). Three kinds of
// clause stay unchecked at a value:
//   - one over value parameters, which a fn value does not supply (a const
//     value parameter is E0705 there, constParamFnValue);
//   - a clause whose evaluation the evaluator refuses for a run-time
//     operation (evaluateContract), as at a call: the entry check judges it
//     in dev and test builds;
//   - an imported clause whose evaluation fails otherwise, E0908 under the
//     stage rule (`SizeOf[P]()` of a struct of this module), a panic or
//     fuel: at a call that is the call's diagnostic, at a value nothing is
//     reported.
//
// Only a clause that folds or evaluates to false is E0706 at a value.
func (u *unit) valueRequires(e ast.Node, sig *types.Signature, inst uint32) {
	if len(sig.Contracts) == 0 {
		return
	}
	u.naming = e
	u.requiresAt(e, nil, sig, nil, inst)
	u.naming = nil
}

// requiresAt is constRequires and valueRequires: at is the node reported,
// call the call (nil for a fn value) and rest its parameters. With closed
// arguments every clause is evaluated or folded, never classified by its
// text: false is E0706, true passes, and a refused run-time
// operation (a var read) leaves it to run time. A clause over a parameter
// that is not const has no argument to evaluate with and stays unchecked.
func (u *unit) requiresAt(at ast.Node, call *ast.CallExpr, sig *types.Signature, rest []types.Param, inst uint32) {
	var gargs []types.GenericArg
	if inst != 0 {
		gargs = u.closeSizeOf(sig, u.b.Generic[inst])
		for _, a := range gargs {
			if !a.IsConst && !u.c.closed(a.Type) || a.IsConst && (a.Const.Kind != types.CAValue || !u.c.closedConst(a.Const)) {
				u.restatedRequires(at, call, rest, sig, gargs) // a generic caller
				return
			}
		}
	}
	gsub, _, unspelled := u.genericSubs(sig, gargs)
	rejected := false // one unjudgeable E0706 per call
	for _, ct := range sig.Contracts {
		if ct.Kind != types.Requires {
			continue
		}
		pred := u.c.cfg.Items.Get(ct.Body)
		// The caller restates it. A clause that also reads an
		// argument genericSubs cannot spell (an enum value) is not, since it
		// would be asked for in the callee's own generic name: beside a
		// forwarded const it cannot be judged here either
		// (unspelledForwarded).
		if call != nil && u.readsForwarded(ct, unspelled) && u.unspelledForwarded(at, call, rest, sig, gargs, ct, pred, gsub, &rejected) {
			continue
		}
		if call != nil && !u.readsForwarded(ct, unspelled) && u.forwardedRequires(call, rest, sig, gargs, ct, pred, gsub) {
			continue
		}
		if pred.Module == u.m.ID {
			// A same-module predicate is a stage-6 unit with no MIR yet, and
			// is not prepared early: check folds it itself when it is
			// operators over the call's const arguments, consts and literals
			// (checked at each call); any other one is unchecked here.
			u.foldedRequires(at, call, sig, rest, gargs, pred)
			continue
		}
		u.m.c.declOfItem(pred)
		pd, ok := pred.Decl.(*types.FuncDecl)
		if !ok {
			continue
		}
		// A refused run-time operation gives no d: run time judges it.
		holds, known, d, _ := u.evalClause(at, call, rest, ct, pd, gargs)
		if d != nil {
			if call != nil { // at a value an evaluation failure is unchecked (valueRequires)
				u.b.Diags = append(u.b.Diags, d)
			}
			continue
		}
		if known && !holds {
			u.requiresFalse(at, call, u.c.clauseText(pred))
		}
	}
}

// closeSizeOf is gargs with each `SizeOf[T]()` const argument of a closed T
// replaced by its value, so a clause over that const generic is evaluated
// or folded as over a literal (`F[SizeOf[E]()]` with `requires N > 0`, E
// empty). The recorded instance keeps its CASizeOf. gargs itself is
// returned when nothing changes, and an argument whose size the generic's
// type cannot hold stays as it is.
func (u *unit) closeSizeOf(sig *types.Signature, gargs []types.GenericArg) []types.GenericArg {
	var out []types.GenericArg
	for i, a := range gargs {
		if !a.IsConst || a.Const.Kind != types.CASizeOf || i >= len(sig.Generics) || !sig.Generics[i].Const {
			continue
		}
		t := a.Const.Type
		if t == types.NoType || t == types.Poison || !u.c.closed(t) || u.c.sizeOnCycle(t) {
			continue
		}
		u.c.shapeReads(u.env, t, u.x.rec)
		gt := sig.Generics[i].Type
		id, ok := u.c.internFolded(fval{n: new(big.Int).SetUint64(u.c.Shape(t).Size), t: gt}, gt)
		if !ok {
			continue
		}
		if out == nil {
			out = append([]types.GenericArg(nil), gargs...)
		}
		out[i] = types.GenericArg{IsConst: true, Const: types.ConstArg{Kind: types.CAValue, Value: id, Type: gt}}
	}
	if out == nil {
		return gargs
	}
	return out
}

// requiresFalse is E0706 at at for the false compile-time clause clause,
// fact clause; call is nil for a fn value, which names no callee and passes
// no argument. A callee loaded from the cache may have no clause text.
func (u *unit) requiresFalse(at ast.Node, call *ast.CallExpr, clause string) {
	switch {
	case call == nil && clause != "":
		u.err(diag.E0706, at.Span(), "the function named here requires `"+clause+"`, which is false").Text("clause", clause)
	case call == nil:
		u.err(diag.E0706, at.Span(), "a `requires` clause of the function named here is false")
	case clause != "":
		u.err(diag.E0706, at.Span(), "the callee requires `"+clause+"`, which is false for these arguments").Text("clause", clause)
	default:
		u.err(diag.E0706, at.Span(), "a `requires` clause of the callee is false for these arguments")
	}
}

// evalClause evaluates the imported compile-time requires ct (predicate
// declaration pd) at call e with the closed generic arguments gargs: each
// const parameter the predicate names is bound to the call's folded
// argument. known is false when an argument does not fold or the evaluator
// gives no bool; d is the evaluator's diagnostic. refused is set, with no
// diagnostic, when the evaluator refused a run-time operation: the clause
// is then left to run time (evaluateContract). at is the node
// reported: the call, or with e nil a vector operator or a fn value,
// where a clause with a value parameter is unknown, since neither
// supplies one.
func (u *unit) evalClause(at ast.Node, e *ast.CallExpr, rest []types.Param, ct types.Contract, pd *types.FuncDecl, gargs []types.GenericArg) (holds, known bool, d *diag.Diagnostic, refused bool) {
	var args []types.ConstID
	for _, pp := range pd.Sig.Params {
		if e == nil {
			return false, false, nil, false
		}
		v, good := u.requiresArg(e, rest, pp.Name)
		if !good {
			return false, false, nil, false
		}
		args = append(args, v)
	}
	req := u.c.evalRequest(u.m.ID, types.EvalContract, ct.Body, types.StageSignatures, at.Span())
	req.Args = args
	req.GenericArgs = gargs
	v, d, refused := u.c.evaluateContract(req, u.x.rec)
	if d != nil || v == types.NoConst {
		return false, false, d, refused
	}
	fv, ok := u.c.fvalOf(v)
	if !ok || !fv.isBool {
		return false, false, nil, false
	}
	return fv.b, true, nil, false
}

// foldedRequires checks a same-module compile-time requires at a concrete
// call without the evaluator: the
// predicate's expression is folded (fold.go) with each const
// parameter and const generic parameter it names bound to the call's
// folded argument (gargs are the call's closed generic arguments). False
// is E0706 with fact clause, as constRequires reports an evaluated one. A
// predicate that names a parameter whose argument does not fold, or that
// does not fold itself (a call), is left unchecked: a false accept, never a
// false diagnostic. A fold that panics (an overflow, a division by zero, a
// shift past the width; not on the side of && or || its left side decides)
// is E0901 at at, as an imported clause's evaluation is at a call:
// the instance would panic at its entry check in dev and run its body
// unchecked in release. at is the node reported, e the call (nil for a fn
// value, whose clause over a value parameter does not fold). judged is
// false when the clause was left unchecked (it did not fold).
func (u *unit) foldedRequires(at ast.Node, e *ast.CallExpr, sig *types.Signature, rest []types.Param, gargs []types.GenericArg, pred *types.Item) (judged bool) {
	holds, known, panicked := u.foldedClause(at, e, sig, rest, gargs, pred)
	if panicked != types.PanicNone {
		msg := "compile-time evaluation panicked: " + panicked.String()
		if txt := u.c.clauseText(pred); txt != "" {
			msg += " in `requires " + txt + "`"
		}
		u.err(diag.E0901, at.Span(), msg).Text("panic", panicked.String()).Fact("trace", diag.ListFact())
		return true
	}
	if !known {
		return false
	}
	if !holds {
		u.requiresFalse(at, e, u.c.clauseText(pred))
	}
	return true
}

// foldedClause is foldedRequires' fold: known is false when the predicate
// does not fold (it is then unchecked); panicked is the fold's first panic,
// PanicNone for none. at and e are as evalClause's.
func (u *unit) foldedClause(at ast.Node, e *ast.CallExpr, sig *types.Signature, rest []types.Param, gargs []types.GenericArg, pred *types.Item) (holds, known bool, panicked types.PanicCode) {
	n, fx := u.m.nodeOf(pred.ID)
	ct, ok := n.(*ast.Contract)
	if !ok || ct.X == nil {
		return false, false, types.PanicNone
	}
	// enums: `requires D == Mode.B` over an enum const generic folds.
	f := &folder{m: u.m, fx: fx, rec: u.x.rec, params: map[uint32]fval{}, gens: map[uint32]fval{}, enums: true}
	// A const parameter bound to a struct const (an atomic.Order) is no
	// fval; a call of an imported method on it folds through the evaluator
	// (methodCall), any other use of it does not fold.
	opaque := map[uint32]types.ConstID{}
	// A type generic bound to a closed type is read only by SizeOf and
	// AlignOf, which fold to its layout: `requires SizeOf[T]() > 0`.
	tys := map[uint32]types.TypeID{}
	f.call = func(ce *ast.CallExpr) (fval, bool, bool) {
		if v, ok := u.layoutCall(f, ce, tys); ok {
			return v, true, false
		}
		return u.methodCall(f, at, ce, opaque, pred.Owner)
	}
	good := true
	bind := func(r resolve.Ref) bool {
		switch {
		case r.Kind == resolve.RefParam && types.ItemID(r.B) == pred.Owner:
			if _, seen := f.params[r.A]; seen {
				return true
			}
			if r.A == 0 || int(r.A) > len(sig.Params) || e == nil {
				return false
			}
			if _, seen := opaque[r.A]; seen {
				return true
			}
			id, ok := u.requiresArg(e, rest, sig.Params[r.A-1].Name)
			if !ok {
				return false
			}
			v, ok := u.c.fvalOf(id)
			if !ok {
				opaque[r.A] = id
				if ev, isEnum := u.c.enumFval(id); isEnum {
					f.params[r.A] = ev // `requires m == Mode.B`
				}
				return true
			}
			f.params[r.A] = v
			return true
		case r.Kind == resolve.RefTypeParam && (types.ItemID(r.B) == pred.Owner || types.ItemID(r.B) == pred.ID):
			if _, seen := f.gens[r.A]; seen {
				return true
			}
			if _, seen := tys[r.A]; seen {
				return true
			}
			if int(r.A) >= len(gargs) || int(r.A) >= len(sig.Generics) {
				return false
			}
			if !sig.Generics[r.A].Const {
				a := gargs[r.A]
				if a.IsConst || a.Type == types.NoType || a.Type == types.Poison || !u.c.closed(a.Type) {
					return false
				}
				tys[r.A] = a.Type
				return true
			}
			a := gargs[r.A]
			if !a.IsConst || a.Const.Kind != types.CAValue {
				return false
			}
			v, ok := u.c.fvalOf(a.Const.Value)
			if !ok {
				v, ok = u.c.enumFval(a.Const.Value)
			}
			f.gens[r.A] = v
			return ok
		}
		return true
	}
	ast.Inspect(ct.X, func(c ast.Node) bool {
		if c == nil || !good {
			return false
		}
		if id := c.ID(); int(id) < len(fx.info.Refs) {
			good = bind(fx.info.Refs[id])
		}
		return good
	})
	if !good {
		return false, false, types.PanicNone
	}
	v, ok, p := f.fold(ct.X)
	if p {
		return false, false, f.panicCode
	}
	if !ok || !v.isBool {
		return false, false, types.PanicNone
	}
	return v.b, true, types.PanicNone
}

// layoutCall folds `SizeOf[T]()` and `AlignOf[T]()` in a same-module
// predicate (foldedClause) with T a type generic of the predicate's
// function bound in tys to a closed type: its layout's size or alignment,
// an int. ok is false for any other call.
func (u *unit) layoutCall(f *folder, ce *ast.CallExpr, tys map[uint32]types.TypeID) (fval, bool) {
	ix, ok := ce.Fun.(*ast.IndexExpr)
	if !ok || len(ce.Args) != 0 || len(ix.Args) != 1 || ix.Args[0] == nil {
		return fval{}, false
	}
	r := f.ref(ix.X)
	if r.Kind != resolve.RefUniverse || source.Symbol(r.A) != source.SymSizeOf && source.Symbol(r.A) != source.SymAlignOf {
		return fval{}, false
	}
	a := f.ref(ix.Args[0])
	if a.Kind != resolve.RefTypeParam {
		return fval{}, false
	}
	t, ok := tys[a.A]
	if !ok || u.c.sizeOnCycle(t) {
		return fval{}, false
	}
	u.c.shapeReads(u.env, t, u.x.rec)
	sh := u.c.Layout(t).Shape
	n := sh.Size
	if source.Symbol(r.A) == source.SymAlignOf {
		n = uint64(sh.Align)
	}
	return fval{n: new(big.Int).SetUint64(n), t: types.Int}, true
}

// restatedRequires is the rule for a call inside a generic: each
// compile-time requires of the callee whose predicate names
// only const generic parameters is spelled with the callee's const
// parameters replaced by the call's arguments; a clause that then names a
// const parameter of the caller must be one of the caller's own requires,
// token for token, else E0706 at the call with the clause to add as a fix.
// A clause whose arguments are all values was closed and is evaluated by
// constRequires; one that reads an argument that is neither a value, a
// const parameter of the caller nor SizeOf of a type parameter of the
// caller (genericSubs' unspelled) is left alone, and the call's other
// clauses are still judged. A clause that reads no open const generic
// (readsForwarded) is judged as at a concrete site (closedClause), after a
// forwarded const value parameter's restatement. e is the call, the
// operator a vector intrinsic records or a fn value; call and rest are the
// call and its parameters, nil for an operator and a value. A clause over
// const value parameters is forwardedRequires', or, when nothing is
// forwarded, judged over the caller's finite const generics
// (finiteRequires), or folded at a same-module callee. A clause that reads
// an open argument cannot be evaluated, so it is classified by what it
// names (runtimeClause): one naming a var or a parameter that is not const
// is a run-time contract and never restated. Inside a fn literal the
// caller's clauses are those of the function it is written in (ownerSig).
func (u *unit) restatedRequires(e ast.Node, call *ast.CallExpr, rest []types.Param, sig *types.Signature, gargs []types.GenericArg) {
	sub, gfwd, unspelled := u.genericSubs(sig, gargs)
	rejected := false // one unjudgeable E0706 per call or value
	for _, ct := range sig.Contracts {
		if ct.Kind != types.Requires {
			continue
		}
		if u.readsForwarded(ct, unspelled) {
			// It reads an argument no clause can spell (`F[SizeOf[W[T]]()]`
			// with `requires N > 0`): it can be neither judged nor restated.
			// An unspelled argument that is a closed value (an imported
			// callee's enum value) does not stop the finite judgment or
			// a closed site's, which bind it as it is (finiteOverValues);
			// only a clause neither judges is left. That clause keeps
			// the exit to the entry check only when it is the callee's
			// sole clause over the one const generic it names (soleClause,
			// `requires M == 3`): then no implied set or skipped
			// combination of the finite judgment can rest on it, and the
			// entry check judges it in dev. Any other such clause may be
			// what the callee's body relies on (`M == N + 1`; or
			// `M == 3 || M == 24` beside `M < 4`, which skips M = 24), which
			// a release build never judges: the call is E0706. Beside a
			// forwarded const value argument no sole clause keeps that exit
			// (unspelledForwarded).
			if u.unspelledForwarded(e, call, rest, sig, gargs, ct, u.c.cfg.Items.Get(ct.Body), sub, &rejected) {
				continue
			}
			if u.closedUnspelled(ct, gargs, sig, unspelled) && u.finiteOverValues(e, call, rest, sig, gargs, ct, sub, gfwd, &rejected) {
				continue
			}
			if !rejected && !u.soleClause(sig, ct) {
				u.unjudgeable(e, call, ct)
				rejected = true
			}
			continue
		}
		pred := u.c.cfg.Items.Get(ct.Body)
		u.m.c.declOfItem(pred)
		pd, ok := pred.Decl.(*types.FuncDecl)
		if !ok {
			continue
		}
		if !u.readsForwarded(ct, gfwd) {
			// Every generic the clause reads is closed here (`G2[T, 0]`
			// with `requires N > 0`, `simd.Vec[T, 3].Splat`): judged as at
			// a concrete site, after a forwarded const value
			// parameter's restatement.
			if call != nil && len(pd.Sig.Params) > 0 && !u.runtimeClause(sig, ct) && u.forwardedRequires(call, rest, sig, gargs, ct, pred, sub) {
				continue
			}
			if !u.closedClause(e, call, rest, sig, gargs, ct, pred, pd) && u.readsOpenParam(sig, gargs, ct) && !u.runtimeClause(sig, ct) {
				// A clause over a type generic bound to the caller's own type
				// parameter (`K[U]` calling `G[U]` with `requires SizeOf[T]()
				// < 4`) is restated, so it is judged where U meets a closed
				// type, not left to the entry check.
				// A const value parameter the clause reads is spelled as the
				// call's folded argument (`3 < SizeOf[U]()`); at a value,
				// which supplies none, the clause stays unchecked.
				// Inside `if U is X` a U written in the call is X, so such
				// a clause reads only parameters no branch narrows.
				if vs, ok := u.foldedValueSubs(call, rest, pd, sub); ok {
					u.requireRestated(e, u.demandOf(ct, sig, vs))
				}
			}
			continue
		}
		if u.runtimeClause(sig, ct) {
			continue
		}
		if len(pd.Sig.Params) > 0 {
			if call != nil && !u.forwardedRequires(call, rest, sig, gargs, ct, pred, sub) && !u.finiteRequires(call, call, rest, sig, gargs, ct, pred, sub, nil) && pred.Module == u.m.ID {
				u.foldedRequires(call, call, sig, rest, gargs, pred)
			}
			continue
		}
		// The clause from the signature's Contract.Text, so a callee
		// in a module Load decoded, which has no AST, restates the same.
		if ct.Text == "" {
			continue
		}
		// The finite judgment holds for a clause over const generics
		// alone too (std.simd's lane clause on Vec[T, N].Zero), at a call
		// and at a vector operator alike: judged over the caller's
		// finite set before it is restated.
		if u.finiteRequires(e, call, rest, sig, gargs, ct, pred, sub, nil) {
			continue
		}
		u.requireRestated(e, u.demandOf(ct, sig, sub))
	}
}

// foldedValueSubs is sub with each const value parameter that predicate pd
// reads spelled as call's argument (argText), every one of which folds.
// False at a value (call nil) when pd reads one, and when an argument is
// not a const one, does not fold or cannot be spelled.
func (u *unit) foldedValueSubs(call *ast.CallExpr, rest []types.Param, pd *types.FuncDecl, sub map[source.Symbol]string) (map[source.Symbol]string, bool) {
	if len(pd.Sig.Params) == 0 {
		return sub, true
	}
	if call == nil {
		return nil, false
	}
	out := make(map[source.Symbol]string, len(sub)+len(pd.Sig.Params))
	for k, v := range sub {
		out[k] = v
	}
	for _, pp := range pd.Sig.Params {
		arg, isConst := constArgExpr(call, rest, pp.Name)
		if !isConst {
			return nil, false
		}
		if _, folds := u.requiresArg(call, rest, pp.Name); !folds {
			return nil, false
		}
		text, ok := u.argText(arg)
		if !ok {
			return nil, false
		}
		out[pp.Name] = text
	}
	return out, true
}

// readsOpenParam reports whether clause ct, spelled in full, names a type
// generic of sig whose argument in gargs is open, and every such argument
// is a bare type parameter of the caller (`G[U]` in `K[U]`), which the
// restated clause spells as the caller writes it (`SizeOf[U]() < 4`) and
// which the same-module fold and the evaluator read at a concrete caller.
// A compound open argument (`G[W[U]]`) keeps the exit to the entry check:
// the same-module fold reads no layout of a compound type over a
// generic, so a restatement would not be judged there either.
func (u *unit) readsOpenParam(sig *types.Signature, gargs []types.GenericArg, ct types.Contract) bool {
	if ct.Text == "" || strings.Contains(ct.Text, "...") {
		return false
	}
	found := false
	for i, g := range sig.Generics {
		if g.Const || i >= len(gargs) || gargs[i].IsConst || gargs[i].Type == types.NoType || gargs[i].Type == types.Poison || u.c.closed(gargs[i].Type) {
			continue
		}
		if !namesWord(ct.Text, u.c.cfg.Interner.Str(g.Name)) {
			continue
		}
		if u.tab.Kind(gargs[i].Type) != types.KParam {
			return false
		}
		found = true
	}
	return found
}

// soleClause reports whether requires ct of sig names exactly one const
// generic G of sig and no other requires of sig names G: a
// clause with no text or a form exprText cannot spell ("...") may name
// any, so it is never sole and stops any other clause being sole.
func (u *unit) soleClause(sig *types.Signature, ct types.Contract) bool {
	unknown := func(c types.Contract) bool { return c.Text == "" || strings.Contains(c.Text, "...") }
	if unknown(ct) {
		return false
	}
	var g source.Symbol
	n := 0
	for _, gp := range sig.Generics {
		if gp.Const && namesWord(ct.Text, u.c.cfg.Interner.Str(gp.Name)) {
			g = gp.Name
			n++
		}
	}
	if n != 1 {
		return false
	}
	for _, o := range sig.Contracts {
		if o.Kind != types.Requires || o.Body == ct.Body {
			continue
		}
		if unknown(o) || namesWord(o.Text, u.c.cfg.Interner.Str(g)) {
			return false
		}
	}
	return true
}

// closedUnspelled reports whether every unspelled generic clause ct names
// has a closed value as its argument (an enum value of an imported callee,
// genericSubs), and the clause's text names its generics.
func (u *unit) closedUnspelled(ct types.Contract, gargs []types.GenericArg, sig *types.Signature, unspelled []source.Symbol) bool {
	if ct.Text == "" || strings.Contains(ct.Text, "...") {
		return false
	}
	for _, name := range unspelled {
		if !namesWord(ct.Text, u.c.cfg.Interner.Str(name)) {
			continue
		}
		i := genericIndex(sig, name)
		if i < 0 || i >= len(gargs) || !gargs[i].IsConst || gargs[i].Const.Kind != types.CAValue || !u.c.closedConst(gargs[i].Const) {
			return false
		}
	}
	return true
}

// finiteOverValues judges clause ct whose unspelled arguments are closed
// values (closedUnspelled): over the caller's finite sets (finiteRequires)
// when it reads a forwarded const generic, else as at a concrete site
// (closedClause), which at a call binds a const value parameter to the
// call's folded argument (`lib.At[T, lib.MA](t, 24)` with `requires k < 4
// && D == Mode.A`). False when the clause was not judged so (a
// clause over value parameters at a fn value or an operator, an argument
// that does not fold): restatedRequires then rejects it unless it is
// soleClause. Over the caller's sets, a clause they do not bound is
// E0706 "cannot be judged here", never a demand to restate text that keeps
// the callee's names for the value; rejected is restatedRequires'
// once-per-call flag.
func (u *unit) finiteOverValues(e ast.Node, call *ast.CallExpr, rest []types.Param, sig *types.Signature, gargs []types.GenericArg, ct types.Contract, sub map[source.Symbol]string, gfwd []source.Symbol, rejected *bool) bool {
	pred := u.c.cfg.Items.Get(ct.Body)
	u.m.c.declOfItem(pred)
	pd, ok := pred.Decl.(*types.FuncDecl)
	if !ok || len(pd.Sig.Params) > 0 && call == nil || u.runtimeClause(sig, ct) {
		return false
	}
	if u.readsForwarded(ct, gfwd) {
		return u.finiteRequires(e, call, rest, sig, gargs, ct, pred, sub, rejected)
	}
	return u.closedClause(e, call, rest, sig, gargs, ct, pred, pd)
}

// unjudgeable is E0706 at e (call nil for a fn value or an
// operator): clause ct reads an argument no clause can spell, so it can be
// neither judged nor restated here, and its fix is to pass an argument a
// clause can spell.
func (u *unit) unjudgeable(e ast.Node, call *ast.CallExpr, ct types.Contract) {
	u.unjudgeableWhy(e, call, ct, "an argument it reads cannot be written in a clause (pass a const parameter, a literal or a named const)")
}

// unjudgeableWhy is unjudgeable with why saying what stops the judgment.
func (u *unit) unjudgeableWhy(e ast.Node, call *ast.CallExpr, ct types.Contract, why string) {
	what := "the callee requires"
	if call == nil && u.naming != nil && e == u.naming {
		what = "the function named here requires"
	}
	u.err(diag.E0706, e.Span(), what+" `"+ct.Text+"`, which cannot be judged here: "+why).Text("clause", ct.Text)
}

// unspelledForwarded is the rule for a forwarded const value argument:
// clause ct, which reads an argument no clause can spell
// (an imported callee's enum value: `lib.At[lib.MB](k, t)` under `requires
// k >= 0 && k < 4 && D == Mode.A`), at call, where a const value argument
// forwards the caller's own consts (valueForwarded). A restatement would
// keep the callee's names for the value, and the evaluation has the
// forwarded value unknown, so neither judges it, and its closed conjuncts
// may be false at every argument. A forwarded const
// parameter is E0706 "cannot be judged here"; a forwarded const generic is
// judged over the caller's finite sets (finiteRequires), and is
// E0706 so where they do not bound it. True when ct was handled so;
// rejected is the once-per-call flag. A run-time clause is the entry
// check's, and gives false.
func (u *unit) unspelledForwarded(at ast.Node, call *ast.CallExpr, rest []types.Param, sig *types.Signature, gargs []types.GenericArg, ct types.Contract, pred *types.Item, sub map[source.Symbol]string, rejected *bool) bool {
	if call == nil || u.sig == nil {
		return false
	}
	u.m.c.declOfItem(pred)
	pd, ok := pred.Decl.(*types.FuncDecl)
	if !ok || len(pd.Sig.Params) == 0 || u.runtimeClause(sig, ct) {
		return false
	}
	kind := u.valueForwarded(call, rest, pd)
	if kind == fwdNone {
		return false
	}
	if kind == fwdGeneric && u.finiteRequires(at, call, rest, sig, gargs, ct, pred, sub, rejected) {
		return true
	}
	if !*rejected {
		u.unjudgeableWhy(at, call, ct, "it reads an argument no clause can spell beside a const the caller passes on, which no clause of the caller can restate")
		*rejected = true
	}
	return true
}

// readsForwarded reports whether clause ct can read a const generic whose
// argument is open (fwd, or unspelled, from genericSubs): it names one, or
// it has no text or a form exprText cannot spell ("..."), which may read
// any.
func (u *unit) readsForwarded(ct types.Contract, fwd []source.Symbol) bool {
	if len(fwd) == 0 {
		return false
	}
	if ct.Text == "" || strings.Contains(ct.Text, "...") {
		return true
	}
	for _, g := range fwd {
		if namesBareWord(ct.Text, u.c.cfg.Interner.Str(g)) {
			return true
		}
	}
	return false
}

// namesBareWord is namesWord for readsForwarded, except that a match
// preceded by '.' is a qualified name's member (`lib.Lvl.D`), not the
// generic, as substClause reads it. namesWord
// stays as it is, since standIns and finiteRequires share it.
func namesBareWord(text, word string) bool {
	isWord := func(r byte) bool {
		return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r >= 0x80
	}
	for i := 0; i+len(word) <= len(text); i++ {
		if text[i:i+len(word)] != word {
			continue
		}
		if i > 0 && (text[i-1] == '.' || isWord(text[i-1])) {
			continue
		}
		if i+len(word) < len(text) && isWord(text[i+len(word)]) || labelAt(text, i+len(word)) {
			continue
		}
		return true
	}
	return false
}

// closedClause judges, inside a generic, a clause that reads no open const
// generic: a same-module one is folded with the closed arguments,
// where a type generic it reads must be closed too (foldedRequires); an
// imported one is evaluated with stand-ins for the open type arguments it
// cannot read (standIns), else it stays unchecked. False is E0706. An
// evaluation failure (a panic: E0901; fuel) is the evaluator's diagnostic
// at a call, as requiresAt reports it at a concrete call, and
// nothing at a value. judged is false when the clause stayed unchecked.
func (u *unit) closedClause(at ast.Node, call *ast.CallExpr, rest []types.Param, sig *types.Signature, gargs []types.GenericArg, ct types.Contract, pred *types.Item, pd *types.FuncDecl) (judged bool) {
	if pred.Module == u.m.ID {
		return u.foldedRequires(at, call, sig, rest, gargs, pred)
	}
	if ct.Text == "" {
		return false
	}
	args := append([]types.GenericArg(nil), gargs...)
	if !u.standIns(args, sig, ct, pd) || !u.closedArgs(args) {
		return false
	}
	holds, known, d, _ := u.evalClause(at, call, rest, ct, pd, args)
	if d != nil {
		if call == nil {
			return false
		}
		u.b.Diags = append(u.b.Diags, d)
		return true
	}
	if known && !holds {
		u.requiresFalse(at, call, u.c.clauseText(pred))
	}
	return known
}

// genericSubs spells each const generic argument of a call for a restated
// clause: a const parameter of the caller by its name (listed in fwd), an
// integer value by its digits, a bool value as true or false, and
// `SizeOf[T]()` of a type parameter T of the caller as written (listed in
// fwd too). An argument of another form (`SizeOf[W[T]]()`) or a
// value of another type is listed in unspelled, with no entry in sub: only
// a clause that reads it is then left unchecked (restatedRequires), not
// every clause of the call.
func (u *unit) genericSubs(sig *types.Signature, gargs []types.GenericArg) (sub map[source.Symbol]string, fwd, unspelled []source.Symbol) {
	sub = map[source.Symbol]string{}
	same := len(sig.Contracts) > 0 && u.c.cfg.Items.Get(sig.Contracts[0].Body).Module == u.m.ID
	for i, g := range sig.Generics {
		if !g.Const && i < len(gargs) && !gargs[i].IsConst && gargs[i].Type != types.NoType && gargs[i].Type != types.Poison {
			// A type generic is spelled as the call has it, so a clause
			// over SizeOf[U]() is restated with SizeOf[u16]().
			sub[g.Name] = u.str(gargs[i].Type)
			continue
		}
		if !g.Const || i >= len(gargs) || !gargs[i].IsConst {
			continue
		}
		switch a := gargs[i].Const; a.Kind {
		case types.CAParam:
			if int(a.Param) >= len(u.x.gens) {
				unspelled = append(unspelled, g.Name)
				continue
			}
			sub[g.Name] = u.c.cfg.Interner.Str(u.x.gens[a.Param].Name)
			fwd = append(fwd, g.Name)
		case types.CAValue:
			fv, ok := u.c.fvalOf(a.Value)
			switch {
			case !ok:
				// A value of a plain enum (`At[N, Mode.A]`, `At[N, MA]`,
				// `At[N, lib.MA]`) of a same-module callee is spelled by its
				// variant, as this file names its type, so the clause is
				// judged over the caller's finite sets or restated,
				// not left to the entry check. An imported callee's clause
				// text names the enum as its own module does, which no
				// restatement here can match: it stays unspelled.
				if same {
					if s, isEnum := u.spellVariant(a.Value); isEnum {
						sub[g.Name] = s
						continue
					}
				}
				unspelled = append(unspelled, g.Name)
			case fv.isBool:
				sub[g.Name] = strconv.FormatBool(fv.b)
			default:
				sub[g.Name] = fv.n.String()
			}
		case types.CASizeOf:
			// requiresAt closed a SizeOf of a closed type (closeSizeOf);
			// only a bare type parameter is spelled as the caller states it.
			if a.Type == types.NoType || a.Type == types.Poison || u.tab.Kind(a.Type) != types.KParam {
				unspelled = append(unspelled, g.Name)
				continue
			}
			sub[g.Name] = "SizeOf[" + u.str(a.Type) + "]()"
			fwd = append(fwd, g.Name)
		default:
			unspelled = append(unspelled, g.Name)
		}
	}
	return sub, fwd, unspelled
}

// spellVariant spells value id of a plainEnum as `E.A` (`lib.E.A` for an
// enum of a module this file imports as lib), A the variant whose
// discriminant the value holds (variantConst's form); the layout is a
// shape read, as variantConst's is. False for any other value, and for an
// enum of a module this file has no import of.
func (u *unit) spellVariant(id types.ConstID) (string, bool) {
	c := u.c
	cv := c.cfg.Consts.Get(id)
	if !c.plainEnum(cv.Type) || c.sizeOnCycle(cv.Type) {
		return "", false
	}
	it := c.cfg.Items.Get(c.cfg.Types.Get(cv.Type).Item)
	name := u.str(cv.Type)
	if it.Module != u.m.ID {
		q := ""
		if u.fx != nil {
			for _, b := range u.fx.info.Imports {
				if b.Module == it.Module {
					q = c.cfg.Interner.Str(b.Name)
					break
				}
			}
		}
		if q == "" {
			return "", false
		}
		name = q + "." + name
	}
	c.shapeReads(nil, cv.Type, u.x.rec)
	lay := c.Layout(cv.Type)
	if !lay.HasTag || lay.Tag.Niche || lay.Tag.Offset+uint64(lay.Tag.Size) > uint64(len(cv.Bytes)) {
		return "", false
	}
	discr := types.LoadUint(cv.Bytes[lay.Tag.Offset:], int(lay.Tag.Size))
	d, ok := c.declOfItem(it).(*types.EnumDecl)
	if !ok {
		return "", false
	}
	u.x.rec.signature(it.Key) // the variants' names, which the shape does not hash
	for i, v := range lay.Variants {
		if v.Discr == discr && i < len(d.Variants) {
			return name + "." + c.cfg.Interner.Str(d.Variants[i].Name), true
		}
	}
	return "", false
}

// requiresArg is the folded value of the call's argument for the callee's
// const parameter named name; false when it is not a const parameter or
// does not fold. A recorded value of another type than a closed parameter
// type (`a.Load(0)` for `const o: Order`, E0301 at the argument) does not
// fold either, once the body has a diagnostic: the call is already wrong,
// and the evaluator binds a contract's &P only to a value of type P (an
// ICE otherwise). In a body with no diagnostic the value is used as is, so
// a mismatch in a well-typed program still reaches that ICE.
func (u *unit) requiresArg(e *ast.CallExpr, rest []types.Param, name source.Symbol) (types.ConstID, bool) {
	for i, p := range rest {
		if p.Name != name {
			continue
		}
		if !p.Const || i >= len(e.Args) || e.Args[i] == nil || e.Args[i].Value == nil {
			return types.NoConst, false
		}
		if id := u.b.Consts; id != nil {
			if v := id[u.b.slot(e.Args[i].Value.ID())]; v != types.NoConst {
				if len(u.b.Diags) > 0 && u.c.closed(p.Type) && u.c.cfg.Consts.Get(v).Type != p.Type {
					return types.NoConst, false
				}
				return v, true
			}
		}
		// enums: a variant or a same-module enum const as the argument
		// of a const value parameter (`V(Mode.A)`, `V(MA)`) folds too;
		// Body.Consts has no value at that slot.
		// argGens: the caller's const generics at one combination of its
		// finite sets (finiteRequires).
		f := &folder{m: u.m, fx: u.fx, rec: u.x.rec, enums: true, gens: u.x.argGens}
		fv, ok, _ := f.fold(e.Args[i].Value)
		if !ok {
			return types.NoConst, false
		}
		return u.c.internFolded(fv, p.Type)
	}
	return types.NoConst, false
}

// typeCycle is E0909 for a cycle of untyped consts met while typing them
// (`const A = B + 1`, `const B = A + 1`, `const A = A`): no member has a
// type, so its uses would be Poison with no diagnostic (lower's invariant),
// and the evaluator never sees the cycle. Reported once, at the name of the
// cycle's least-key const with the spelling evalDiag gives an EvalCycle,
// so the driver's own evaluation of that const, if
// it runs, gives an equal diagnostic that diag.Order drops.
func (m *Module) typeCycle(at types.ItemID) {
	i := len(m.d.typing) - 1
	for i >= 0 && m.d.typing[i] != at {
		i--
	}
	if i < 0 {
		return
	}
	cycle := append([]types.ItemID(nil), m.d.typing[i:]...)
	items := m.c.cfg.Items
	least := 0
	for k := range cycle {
		if items.Get(cycle[k]).Key.Compare(items.Get(cycle[least]).Key) < 0 {
			least = k
		}
	}
	cycle = append(cycle[least:], cycle[:least]...)
	if m.d.constCycled == nil {
		m.d.constCycled = map[types.ItemID]bool{}
	}
	if m.d.constCycled[cycle[0]] {
		return
	}
	m.d.constCycled[cycle[0]] = true
	var names []string
	for _, id := range cycle {
		names = append(names, m.c.cfg.Interner.Str(items.Get(id).Name))
	}
	site := items.Get(cycle[0]).Span
	if n, _ := m.nodeOf(cycle[0]); n != nil {
		if cd, ok := n.(*ast.ConstDecl); ok && cd.Name != nil {
			site = cd.Name.Span()
		}
	}
	chain := strings.Join(append(append([]string(nil), names...), names[0]), " -> ")
	m.Diags = append(m.Diags, diag.New(diag.E0909, site, "const cycle: "+chain).Fact("cycle", diag.TextList(names...)))
}
