// Why: a compile-time requires is checked at the call site in every
// profile; inside a generic, where a `const` argument is the caller's own
// parameter and does not fold, the enclosing function must state the same
// clause, so the check still happens at each concrete call.
//
// What: forwardedRequires (the restate demand for a clause over `const`
// value parameters), valueForwarded and ownConstGenerics (how arguments
// forward), ownerSig, the requireRestated family (E0706 with a fix),
// restateAt, methodCall (the same-module fold of a clause that calls an
// imported method on a const parameter, `requires o.CanLoad()`) and
// stdForwardsRawOrder.
//
// How: the clause is spelled with each parameter replaced by the call's
// argument as the caller wrote it and must be one of the caller's own
// requires, token for token (restateMet).

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// forwardedRequires is the restated clause for a callee's compile-time
// requires over const value parameters: when the argument of a const
// parameter the clause names does not fold and names a const parameter of
// the caller (a forwarded `const o: Order`, a field of one), the clause is
// spelled with each parameter replaced by the call's argument as the caller
// wrote it (gsub for the const generics) and must be one of the caller's
// own requires, token for token, else E0706 at the call with the clause to
// add (requireRestated). The caller's clause is then checked at each of its
// concrete calls, so an order that does not apply never reaches an instance.
// True when the clause was judged so; false leaves it to the
// concrete-call checks (every argument folds) or unchecked (an argument
// neither folds nor forwards, or cannot be spelled). A clause whose value
// arguments all fold but that names a const generic of the caller
// (std.simd's `w.With(0, x)` in a body generic over N) is finiteRequires'.
// Nothing can be evaluated with a forwarded argument, so a
// run-time contract by what it names (runtimeClause; sig is the callee's) is
// judged so with no demand: the entry check judges it.
//
// The caller's own const generic passed as a const value argument (`F(k,
// t)` in `G[const k: int]`, or an impl's `n` in a method of `impl[const
// n]`) is forwarded too: before the demand, the
// clause is judged over the caller's finite sets (finiteRequires;
// gargs are the call's generic arguments), and accepted when it holds at
// every value they allow. An interface impl method can state no such
// clause, so there the demand is E0706 "cannot be judged
// here" (requireRestatedWhy).
func (u *unit) forwardedRequires(e *ast.CallExpr, rest []types.Param, sig *types.Signature, gargs []types.GenericArg, ct types.Contract, pred *types.Item, gsub map[source.Symbol]string) bool {
	if ct.Text == "" || u.sig == nil {
		return false
	}
	u.m.c.declOfItem(pred)
	pd, ok := pred.Decl.(*types.FuncDecl)
	if !ok || len(pd.Sig.Params) == 0 {
		return false
	}
	kind := u.valueForwarded(e, rest, pd)
	if kind == fwdNone {
		return false
	}
	if kind == fwdParam && u.stdForwardsRawOrder(pred, sig) {
		return true
	}
	sub := make(map[source.Symbol]string, len(gsub)+len(pd.Sig.Params))
	for k, v := range gsub {
		sub[k] = v
	}
	for _, pp := range pd.Sig.Params {
		arg, _ := constArgExpr(e, rest, pp.Name)
		text, ok := u.argText(arg)
		if !ok {
			return false
		}
		sub[pp.Name] = text
	}
	if u.runtimeClause(sig, ct) {
		return true
	}
	if kind == fwdGeneric && u.finiteRequires(e, e, rest, sig, gargs, ct, pred, gsub, nil) {
		return true
	}
	u.requireRestated(e, u.demandOf(ct, sig, sub))
	return true
}

// fwdKind is how a call's const value arguments forward the caller's own
// consts to a callee's clause (valueForwarded).
type fwdKind uint8

const (
	fwdNone    fwdKind = iota // every argument folds, or one forwards nothing a clause can name
	fwdGeneric                // an argument reads the caller's const generics, and none names a const parameter
	fwdParam                  // an argument names a const parameter of the caller
)

// valueForwarded classifies the arguments of the const value parameters
// that predicate pd reads at call e: fwdParam when one that does not fold
// names a const parameter of the caller (namesOwnConst), fwdGeneric
// when every one that does not fold reads the caller's const generics
// (ownConstGenerics), fwdNone when all fold, when one forwards
// neither (it stays unchecked), or when pd reads a parameter that is not
// const (a run-time contract).
func (u *unit) valueForwarded(e *ast.CallExpr, rest []types.Param, pd *types.FuncDecl) fwdKind {
	kind := fwdNone
	for _, pp := range pd.Sig.Params {
		arg, isConst := constArgExpr(e, rest, pp.Name)
		if !isConst {
			return fwdNone
		}
		if _, folds := u.requiresArg(e, rest, pp.Name); folds {
			continue
		}
		switch {
		case u.namesOwnConst(arg):
			kind = fwdParam
		case u.ownConstGenerics(arg) != nil:
			if kind == fwdNone {
				kind = fwdGeneric
			}
		default:
			return fwdNone
		}
	}
	return kind
}

// ownConstGenerics are the indexes, in the Generics of the function whose
// requires a restatement must be among (ownerSig), of that function's
// const generics that x reads (an impl's const generics come first).
// Nil when x reads none, or reads a const generic of another owner, which
// no binding of the caller's sets reaches.
func (u *unit) ownConstGenerics(x ast.Expr) []uint32 {
	owner, sig := u.ownerSig()
	if owner == nil || sig == nil || u.fx == nil {
		return nil
	}
	var out []uint32
	foreign := false
	ast.Inspect(x, func(n ast.Node) bool {
		if n == nil || foreign {
			return false
		}
		if id := n.ID(); int(id) < len(u.fx.info.Refs) {
			r := u.fx.info.Refs[id]
			if r.Kind == resolve.RefTypeParam {
				switch {
				case types.ItemID(r.B) != owner.ID || int(r.A) >= len(sig.Generics):
					foreign = true
				case sig.Generics[r.A].Const:
					out = append(out, r.A)
				}
			}
		}
		return !foreign
	})
	if foreign {
		return nil
	}
	return out
}

// constArgExpr is the call's argument for the callee's const parameter
// named name; false when name is not a const parameter of rest or has no
// argument.
func constArgExpr(e *ast.CallExpr, rest []types.Param, name source.Symbol) (ast.Expr, bool) {
	for i, p := range rest {
		if p.Name != name {
			continue
		}
		if !p.Const || i >= len(e.Args) || e.Args[i] == nil || e.Args[i].Value == nil {
			return nil, false
		}
		return e.Args[i].Value, true
	}
	return nil, false
}

// namesOwnConst reports whether x names a const parameter of the function
// whose body u checks.
func (u *unit) namesOwnConst(x ast.Expr) bool {
	if u.sig == nil || u.it == nil || u.fx == nil {
		return false
	}
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		if n == nil || found {
			return false
		}
		if id := n.ID(); int(id) < len(u.fx.info.Refs) {
			r := u.fx.info.Refs[id]
			if r.Kind == resolve.RefParam && types.ItemID(r.B) == u.it.ID && r.A >= 1 && int(r.A) <= len(u.sig.Params) && u.sig.Params[r.A-1].Const {
				found = true
			}
		}
		return !found
	})
	return found
}

// argText spells a call argument as the restated clause holds it: exprText,
// in parentheses unless it is a name, a field, a literal, a call or already
// parenthesised. False for a form exprText cannot spell.
func (u *unit) argText(x ast.Expr) (string, bool) {
	var b strings.Builder
	u.c.exprText(&b, x)
	s := b.String()
	if strings.Contains(s, "...") {
		return "", false
	}
	switch x.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.BasicLit, *ast.CallExpr, *ast.ParenExpr:
		return s, true
	}
	return "(" + s + ")", true
}

// ownerSig is the function whose requires a clause restated in u's body must
// be one of: u's own function, or for a fn literal (nested or not) the
// function it is written in, whose const generics the literal sees (the
// literal's own signature has no requires).
//
// It also climbs from a requires or ensures predicate to the function the
// clause belongs to, whose requires state what the clause's body needs
// (`F[const N] requires N > 0 requires Apply(G[N]) > 0`), and from a fn
// literal inside one. A struct invariant has no requires to state anything
// in: ownerSig gives the struct and a nil signature, and the restate demand
// is dropped there (requireRestatedWhy).
func (u *unit) ownerSig() (*types.Item, *types.Signature) {
	items := u.c.cfg.Items
	root := u.it
	for root != nil && root.Owner != types.NoItem {
		if root.Kind != types.KindLiteral && !u.c.contractOf(root) {
			break
		}
		root = items.Get(root.Owner)
	}
	if root == nil || root == u.it {
		return u.it, u.sig
	}
	switch d := root.Decl.(type) {
	case *types.FuncDecl:
		return root, &d.Sig
	case *types.StructDecl:
		return root, nil
	}
	return u.it, u.sig
}

// contractOf reports whether it is a requires or ensures predicate of the
// function that owns it, or an invariant of the struct that owns it.
func (c *Checker) contractOf(it *types.Item) bool {
	if it.Kind != types.KindFunc || it.Owner == types.NoItem {
		return false
	}
	switch d := c.cfg.Items.Get(it.Owner).Decl.(type) {
	case *types.FuncDecl:
		for _, ct := range d.Sig.Contracts {
			if ct.Body == it.ID {
				return true
			}
		}
	case *types.StructDecl:
		for _, inv := range d.Invariants {
			if inv == it.ID {
				return true
			}
		}
	}
	return false
}

// requireRestated is E0706 at e unless clause is one of the caller's own
// requires (ownerSig, restateMet), with the fix that adds it before the
// body.
func (u *unit) requireRestated(e ast.Node, clause demand) {
	u.requireRestatedWhy(e, clause, "")
}

// requireRestatedWhy is requireRestated with why, when not empty, saying in
// the message why the clause could not be judged here (finiteRequires'
// limit). A demand no caller can write (demand.why) is E0706 "cannot be
// judged here", with no fix.
func (u *unit) requireRestatedWhy(e ast.Node, dm demand, why string) {
	if u.restateMet(dm) {
		return
	}
	clause := dm.text
	owner, _ := u.ownerSig()
	if dm.why != "" {
		// No clause this function can write means the callee's, so no fix
		// is offered.
		what := "the callee requires"
		if u.naming != nil && e == u.naming {
			what = "the function named here requires"
		}
		u.err(diag.E0706, e.Span(), what+" `"+clause+"`, which cannot be judged here: "+dm.why+", so no clause of this function can restate it").Text("clause", clause)
		return
	}
	if owner != nil && u.m.ifaceImplMethod(owner.ID) {
		// The clause would be E0706 at an impl method, so no fix states
		// it (`F(n, t)` in a method of `impl[const n]`).
		what := "the callee requires"
		if u.naming != nil && e == u.naming {
			what = "the function named here requires"
		}
		u.err(diag.E0706, e.Span(), what+" `"+clause+"`, which cannot be judged here: a method of an interface impl cannot state a compile-time clause over its consts or generics").Text("clause", clause)
		return
	}
	msg := "the callee requires `" + clause + "` here: state it on this function"
	if u.naming != nil && e == u.naming {
		msg = "the function named here requires `" + clause + "`: state it on this function"
	}
	if why != "" {
		msg += " (" + why + ")"
	}
	d := u.err(diag.E0706, e.Span(), msg).Text("clause", clause)
	node := u.b.Node
	if owner != nil && owner != u.it && u.m.byItem[owner.ID] != nil {
		node, _ = u.m.nodeOf(owner.ID) // a fn literal: the fix goes on its function
	}
	if fd, ok := node.(*ast.FuncDecl); ok && fd.Body != nil && !dm.noFix {
		if at, ok := restateAt(fd.Sig); ok {
			// Proven when the clause keeps no name of the callee: it is
			// then the call's own arguments, as the caller wrote them,
			// under the callee's operators and methods, so stating it
			// meets this demand token for token (restateMet) and reads
			// nothing this function cannot name.
			class := diag.Compiles
			if len(dm.kept) == 0 && !dm.byText {
				class = diag.Proven
			}
			d.Fix(diag.Fix{Class: class, Edits: []diag.Edit{{Span: at, Text: "\n    requires " + clause}},
				Why: "a caller that passes its own const parameter on restates the clause"})
		}
	}
}

// restateAt is where a restated requires goes in sig: after its last
// contract, so it stays before a cpu or section clause, else at the end
// of a signature that has neither. A signature with a cpu or section
// clause and no contract gets no fix (the clause's keyword has no span).
// The fix never goes before the body's `{` with a newline after it: that
// would leave the `{` on a line of its own (E0101).
func restateAt(sig *ast.Signature) (source.Span, bool) {
	if n := len(sig.Contracts); n > 0 {
		end := sig.Contracts[n-1].Span()
		return source.Span{File: end.File, Start: end.End, End: end.End}, true
	}
	if sig.Cpu != nil || sig.Section != nil {
		return source.Span{}, false
	}
	end := sig.Span()
	return source.Span{File: end.File, Start: end.End, End: end.End}, true
}

// methodCall folds `p.M(args)` in a same-module predicate (foldedRequires):
// p a const parameter of the predicate's function bound to the call's
// argument recv (a struct const, which an fval cannot hold), M an exported,
// non-generic method of recv's type declared in another module, taking self
// by value and returning bool with no error set, each further argument a
// scalar that folds. The method runs through the evaluator (EvalCall at
// StageSignatures, as callArg's imported call); a same-module method enters
// a body of this module, so the clause stays unchecked (a false accept,
// never a false diagnostic). panicked reports a diagnostic already added.
func (u *unit) methodCall(f *folder, at ast.Node, ce *ast.CallExpr, opaque map[uint32]types.ConstID, owner types.ItemID) (fval, bool, bool) {
	fe, ok := ce.Fun.(*ast.FieldExpr)
	if !ok {
		return f.notConst(ce)
	}
	r := f.ref(fe.X)
	if r.Kind != resolve.RefParam || types.ItemID(r.B) != owner {
		return f.notConst(ce)
	}
	recv, ok := opaque[r.A]
	if !ok {
		return f.notConst(ce)
	}
	c := u.c
	rt := c.cfg.Consts.Get(recv).Type
	cands, folded := u.m.lookup(rt, fe.Name.Name)
	if folded || len(cands) != 1 || cands[0].Kind != MemberFunc {
		return f.notConst(ce)
	}
	it := c.cfg.Items.Get(cands[0].Item)
	if it.Module == u.m.ID || !it.Exported {
		return f.notConst(ce)
	}
	fd, ok := c.declOfItem(it).(*types.FuncDecl)
	if !ok || len(fd.Sig.Generics) > 0 || len(fd.Sig.Params) != len(ce.Args)+1 || fd.Sig.Params[0].Type != rt ||
		fd.Sig.Result != types.Bool || fd.Sig.Errors != types.EmptyErrors {
		return f.notConst(ce)
	}
	u.x.rec.signature(it.Key)
	req := c.evalRequest(u.m.ID, types.EvalCall, it.ID, types.StageSignatures, at.Span())
	req.Args = append(req.Args, recv)
	for i, a := range ce.Args {
		if a == nil || a.Name != nil || a.Value == nil {
			return f.notConst(ce)
		}
		v, ok, p := f.fold(a.Value)
		if !ok {
			return v, false, p
		}
		id, ok := c.internFolded(v, fd.Sig.Params[i+1].Type)
		if !ok {
			return f.notConst(ce)
		}
		req.Args = append(req.Args, id)
	}
	// A refused run-time operation (the method reads a var) does not fold:
	// the clause is left to run time, with no E0901, as an imported clause
	// is (evaluateContract).
	v, d, refused := c.evaluateContract(req, u.x.rec)
	if refused {
		return f.notConst(ce)
	}
	if d != nil {
		u.b.Diags = append(u.b.Diags, d)
		return fval{}, false, true
	}
	if v == types.NoConst {
		return f.notConst(ce)
	}
	fv, ok := c.fvalOf(v)
	if !ok || !fv.isBool {
		return f.notConst(ce)
	}
	return fv, true, false
}

// rawOrdered are std.raw's ordered atomics, whose clauses bound the order.
var rawOrdered = map[string]bool{"LoadOrd": true, "StoreOrd": true, "SwapOrd": true, "AddOrd": true, "SubOrd": true,
	"AndOrd": true, "OrOrd": true, "XorOrd": true, "CasOrd": true, "FenceOrd": true}

// stdForwardsRawOrder reports whether this call is std.atomic passing its
// own const order on to one of std.raw's ordered atomics (sig, whose
// clause pred bounds the bare u8 order). std.atomic states
// its own clause over the order instead, `o.CanLoad()`, `CanStore()`,
// `CanFence()` or `CanModify()`, which implies raw's at each of the six
// orders an Order can hold (TestAtomicClausesImplyRaw holds the two
// sets of texts to that), and which a forwarder outside std.atomic
// restates. Demanding raw's clause too would put a clause over the
// private `Order.v` into std.atomic's public signatures, which no
// forwarder outside std.atomic could restate. Calls whose order folds are
// still judged against raw's clause, every other forwarder (any other
// module, any other std.raw clause) is demanded as before, and codegen's
// atomicOrder stays the backstop.
func (u *unit) stdForwardsRawOrder(pred *types.Item, sig *types.Signature) bool {
	if !u.m.mod.Std || u.m.mod.Path != "std.atomic" || sig == nil || !rawOrdered[u.c.cfg.Interner.Str(sig.Name)] {
		return false
	}
	return u.c.cfg.Items.Module(pred.Module).Path == "std.raw"
}
