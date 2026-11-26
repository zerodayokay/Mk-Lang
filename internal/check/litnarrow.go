// Why: a fn literal written inside `if T is X` is its own item and unit,
// typed apart from the body that holds it; unless it sees the branch's
// narrowing, its signature says T where the enclosing unit reads X, and its
// body reads a captured value of type T as the opaque parameter.
//
// What: narrowLiteral (step 4: the literal's narrowing and its signature
// under it), litNarrowing (the narrowing of the branches that hold a
// literal) and literalNarrow (a literal unit's starting narrowing).
//
// How: the branches are found in the owner's syntax tree, outermost first,
// after the narrowing of an owner that is itself a literal; each X is typed
// under the narrowing so far, quietly (the enclosing unit reports its
// errors), and a branch whose X cannot meet T's bounds narrows nothing, as
// the enclosing unit then checks no branch. The literal's signature and its
// unit both read types under the narrowing.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/types"
)

// narrowLiteral reads the literal's signature in step 4, when bounds can be
// judged at the type narrowed by the enclosing branch.
func (m *Module) narrowLiteral(it *types.Item, x tctx, n *ast.FnLit, sig *types.Signature) {
	if narrow := m.litNarrowing(it, x); narrow != nil {
		env := m.c.NewEnv(x.gens)
		env.view = m
		copy(env.narrow, narrow)
		x.unitEnv = func() *Env { return env }
	}
	x.sink = &m.Diags
	x.literalSignature(n, sig)
}

// litNarrowing is the narrowing in force where literal it is written
// (narrow[i] the type parameter i is narrowed to, NoType for none), nil
// when no branch narrows it. Memoized per literal.
func (m *Module) litNarrowing(it *types.Item, x tctx) []types.TypeID {
	if n, ok := m.litNarrow[it.ID]; ok {
		return n
	}
	var out []types.TypeID
	defer func() {
		if m.litNarrow == nil {
			m.litNarrow = map[types.ItemID][]types.TypeID{}
		}
		m.litNarrow[it.ID] = out
	}()
	if it.Owner == types.NoItem || len(x.gens) == 0 || m.byItem[it.Owner] == nil {
		return nil
	}
	env := m.c.NewEnv(x.gens)
	env.view = m
	owner := m.c.cfg.Items.Get(it.Owner)
	on, _ := m.nodeOf(owner.ID)
	narrowed := false
	if _, ok := on.(*ast.FnLit); ok {
		if outer := m.litNarrowing(owner, x); len(outer) == len(env.narrow) {
			copy(env.narrow, outer)
			narrowed = true
		}
	}
	lit, _ := m.nodeOf(it.ID)
	var path []ast.Node
	found := false
	ast.Inspect(on, func(n ast.Node) bool {
		switch {
		case found:
			return false
		case n == nil:
			path = path[:len(path)-1]
			return false
		case n == lit:
			found = true
			return false
		}
		path = append(path, n)
		return true
	})
	if !found {
		return out
	}
	var scratch []*diag.Diagnostic
	quiet := x
	quiet.sink = &scratch
	quiet.unitEnv = func() *Env { return env }
	for i, n := range path {
		s, ok := n.(*ast.IfStmt)
		if !ok || i+1 >= len(path) || path[i+1] != ast.Node(s.Then) {
			continue
		}
		c, ok := s.Cond.(*ast.IsCond)
		if !ok || c.Name == nil {
			continue
		}
		r := quiet.ref(c.Name)
		if r.Kind != resolve.RefTypeParam || int(r.A) >= len(x.gens) || x.gens[r.A].Const {
			continue
		}
		t := quiet.typeOf(c.Type, posPlain)
		if t == types.Poison {
			continue
		}
		args := narrowArgs(m.c.cfg.Types, x.gens, env.narrow)
		ok = true
		for _, b := range m.c.substBounds(x.gens[r.A].Bounds, args, types.NoType) {
			if !m.c.satisfies(env, t, b, quiet.rec) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		env = env.Narrow(r.A, t)
		args[r.A] = types.TypeArg(t)
		for j := range env.narrow {
			if j != int(r.A) && env.narrow[j] != types.NoType {
				env.narrow[j] = m.c.subst(env.narrow[j], args, types.NoType)
			}
		}
		narrowed = true
	}
	if narrowed {
		out = env.narrow
	}
	return out
}

// narrowArgs are the generic arguments of narrow over gens: the identity
// but for the narrowed type parameters.
func narrowArgs(tab *types.Table, gens []types.GenericParam, narrow []types.TypeID) []types.GenericArg {
	args := identityArgs(tab, gens)
	for i, t := range narrow {
		if i < len(args) && t != types.NoType && !args[i].IsConst {
			args[i] = types.TypeArg(t)
		}
	}
	return args
}

// literalNarrow starts literal unit u under the narrowing of the branches
// that hold it, so its values and types written T read as X there.
func (u *unit) literalNarrow() {
	narrow := u.m.litNarrow[u.it.ID]
	if narrow == nil || len(narrow) > len(u.env.narrow) {
		return
	}
	copy(u.env.narrow, narrow)
	u.narrow = narrowArgs(u.tab, u.env.gen, u.env.narrow)
}
