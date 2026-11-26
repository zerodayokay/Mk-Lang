// Why: generic code branches on its type parameters (`if T is X`) and takes
// compile-time `const` parameters; both need rules of their own in the
// checker.
//
// What: isBranch (the `if T is X` construct), nt, narrowTo and
// narrowOrIdentity (the narrowing), constArgs, constParamFnValue, notConst
// and callNotConst (the `const` parameter rules).
//
// How: `if T is X` is E0701 at the `is` when X cannot meet T's bounds; the
// then-branch is checked with T narrowed to X, the else-branch with T opaque.
// Every argument of a `const` parameter must be a compile-time constant, else
// E0705 at the argument.

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/types"
)

// nt is t as the current branch sees it: every narrowed type parameter
// substituted.
func (u *unit) nt(t types.TypeID) types.TypeID {
	if u.narrow == nil || t == types.NoType {
		return t
	}
	return u.c.subst(t, u.narrow, types.NoType)
}

// isBranch checks `if T is X { A } else { B }`: A with T narrowed to X, B
// with T opaque. It reports whether both branches diverge.
func (u *unit) isBranch(s *ast.IfStmt, c *ast.IsCond, v *valueCtx) bool {
	*u.b.use(c.ID()) = Use{Kind: UseBuiltin, Index: uint32(BuiltinTypeIs)}
	x := u.nt(u.x.typeOf(c.Type, posPlain))
	u.setType(c.Type, x)
	idx := -1
	if c.Name != nil {
		if r := u.ref(c.Name); r.Kind == resolve.RefTypeParam && int(r.A) < len(u.env.gen) && !u.env.gen[r.A].Const {
			idx = int(r.A)
		}
	}
	then, els, elsDone := false, false, false
	if v != nil && v.want == types.NoType && v.got == types.NoType {
		// A value `if T is X` with no expected type: the arm under the
		// narrowing gives a value of X where the construct's is read as T,
		// so the arms outside it decide first; the narrowed arm then
		// adapts, seeing the decided type with T read as X. It
		// decides only when no other arm does.
		els, elsDone = u.elseArm(s, v), true
	}
	if idx >= 0 && x != types.Poison {
		ok := true
		g := u.env.gen[idx]
		for _, b := range u.c.substBounds(g.Bounds, u.narrowOrIdentity(), types.NoType) {
			if !u.c.satisfies(u.env, x, b, u.x.rec) {
				d := u.c.boundFailure(u.env, keywordSpan(u.fx, c.Name.Span().End, ast.TokIs), g.Name, b, x, u.x.gens)
				d.Message = "`" + u.str(x) + "` cannot meet `" + u.c.cfg.Interner.Str(g.Name) + "`'s bounds, so the branch never runs"
				u.b.Diags = append(u.b.Diags, d)
				ok = false
			}
		}
		if ok {
			savedEnv, savedNarrow := u.env, u.narrow
			u.narrowTo(idx, x)
			then = u.armBlock(s.Then, v)
			u.env, u.narrow = savedEnv, savedNarrow
		}
	}
	if !elsDone {
		els = u.elseArm(s, v) // always checked: no short circuit
	}
	return then && els
}

// narrowTo installs the then-branch narrowing of parameter idx to x (x is
// already read under the enclosing narrowing). Every parameter an
// enclosing branch narrowed to a type naming idx (`if T is U { if U is
// u32 {`) is narrowed through x as well, in the values' substitution
// (u.narrow) and the type expressions' Env alike, so a T is u32 in both
// and the two never disagree.
func (u *unit) narrowTo(idx int, x types.TypeID) {
	u.narrow = u.narrowOrIdentity()
	u.narrow[idx] = types.TypeArg(x)
	env := u.env.Narrow(uint32(idx), x)
	for j := range env.narrow {
		if j == idx || env.narrow[j] == types.NoType || j >= len(u.narrow) || u.narrow[j].IsConst {
			continue
		}
		t := u.c.subst(env.narrow[j], u.narrow, types.NoType)
		env.narrow[j], u.narrow[j] = t, types.TypeArg(t)
	}
	u.env = env
}

// narrowOrIdentity is a copy of the current narrowing, or the identity
// arguments of the unit's generics.
func (u *unit) narrowOrIdentity() []types.GenericArg {
	if u.narrow != nil {
		return append([]types.GenericArg(nil), u.narrow...)
	}
	return identityArgs(u.tab, u.env.gen)
}

// constArgs is the rule for `const` value parameters: every argument
// must be a compile-time constant, else E0705 at the argument, and
// one mono can make a constant per instance (constArgForms).
func (u *unit) constArgs(e *ast.CallExpr, params []types.Param) {
	for i, a := range e.Args {
		if a == nil || a.Value == nil || i >= len(params) || !params[i].Const {
			continue
		}
		if why := u.notConst(a.Value); why != "" {
			pn := u.c.cfg.Interner.Str(params[i].Name)
			u.err(diag.E0705, a.Value.Span(), "`"+pn+"` is a const parameter; "+why+" is not a compile-time constant").Text("param", pn)
			continue
		}
		u.constArgForms(a.Value, params[i])
	}
}

// constParamFnValue: a function with a `const` value parameter
// used as a `fn` value is E0705 at the use, because a call through a fn
// value cannot supply a compile-time argument (mono's ICE is the
// backstop). It reports whether it
// fired, so the use is Poison.
func (u *unit) constParamFnValue(at ast.Node, s *types.Signature) bool {
	if u.offsetOfValue(at, s) { // OffsetOf's field is compile-time too
		return true
	}
	for _, p := range s.Params {
		if !p.Const {
			continue
		}
		pn := u.c.cfg.Interner.Str(p.Name)
		u.err(diag.E0705, at.Span(), "`"+u.c.cfg.Interner.Str(s.Name)+"` has the const parameter `"+pn+"`, which a call through a fn value cannot supply; call it directly").
			Text("param", pn)
		return true
	}
	return false
}

// notConst names what makes e not a compile-time constant: a local, a
// parameter that is not const, a var global, or a call of a function, a
// method or a fn value (callNotConst). A fn literal's body is its own unit
// and is not looked into.
func (u *unit) notConst(e ast.Expr) string {
	why := ""
	ast.Inspect(e, func(n ast.Node) bool {
		if why != "" || n == nil {
			return false
		}
		if _, ok := n.(*ast.FnLit); ok {
			return false
		}
		if ce, ok := n.(*ast.CallExpr); ok {
			why = u.callNotConst(ce)
			return why == ""
		}
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		switch r := u.ref(id); r.Kind {
		case resolve.RefLocal:
			why = "the local `" + u.c.cfg.Interner.Str(id.Name) + "`"
		case resolve.RefParam:
			if u.sig == nil || r.A == 0 || int(r.A) > len(u.sig.Params) || !u.sig.Params[r.A-1].Const {
				why = "the parameter `" + u.c.cfg.Interner.Str(id.Name) + "`"
			}
		case resolve.RefCapture:
			// A literal is compiled once, whatever its enclosing
			// function's const parameters are, so a capture (of a const
			// parameter or a local) is a value there; it reached mono as
			// "const parameter argument is not a constant".
			why = "the captured `" + u.c.cfg.Interner.Str(id.Name) + "` (a `fn` literal is compiled once)"
		case resolve.RefItem:
			if u.c.cfg.Items.Get(types.ItemID(r.A)).Kind == types.KindGlobal {
				why = "the global `" + u.c.cfg.Interner.Str(id.Name) + "`"
			}
		}
		return true
	})
	return why
}

// callNotConst is the rule for a call inside a const value argument: the
// language lists no call among the forms of a constant argument, and none is
// evaluated there (`Lane(Id(3))` would run with 0), so a call of a function,
// a method or a fn value names what is not a constant. A struct or error
// literal (UseConstruct) and a builtin (`SizeOf[T]()`, a conversion) are
// not calls of a function and stay to the other rules.
func (u *unit) callNotConst(ce *ast.CallExpr) string {
	switch u.useOf(ce.ID()).Kind {
	case UseItem, UseMethod, UseIfaceMethod, UseFnValueCall:
		var b strings.Builder
		u.c.exprText(&b, ce.Fun)
		return "the call of `" + b.String() + "`"
	}
	return ""
}
