// Why: every later check of a call or a body reads the callee's checked
// signature, and a diagnostic prints a signature the way source writes it.
//
// What: signature (an ast.Signature to types.Signature), declFunc, declSig,
// declContract, declInvariant and declLiteral (functions, methods, externs,
// interface methods, fn literals, contract predicates and invariants),
// runFlows (every signature's flows, flows.go), entry and mainRules (Main),
// and the printers sigString, sigWithClauses, writtenFlows and flowText,
// and spelled (Signature.Spelled: the types as an alias writes them).
//
// How: generics are the prefix, the owning type's, then the function's own;
// a contract predicate's signature is its function's, plus the captures its
// fn literals name. Bodiless functions are allowed only where the parser
// admits them. Step 5 (flows) runs after every signature is declared.

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// signature reads an ast.Signature: generics (prefix, the owning item's,
// first), the receiver as Params[0], parameters, result and error set.
// Flows are step 5's (flows); contracts are attached by the caller.
func (x *tctx) signature(item types.ItemID, name source.Symbol, s *ast.Signature, prefix []types.GenericParam, extern, exported bool) types.Signature {
	tab := x.tab()
	spelling, aliases := x.spelling, x.aliases
	x.spelling, x.aliases = true, nil
	gens := x.generics(s.TypeParams, prefix)
	sig := types.Signature{Item: item, Name: name, Generics: gens, OwnGenerics: uint32(len(gens) - len(prefix)),
		Exported: exported, Extern: extern, Span: s.Span()}
	if s.Name != nil && s.Name.Name != nil {
		sig.Span = s.Name.Name.Span()
	}
	var spelled []string // Spelled, set by spell
	spell := func(i int, n ast.Node, t types.TypeID) {
		if len(x.aliases) == 0 || t == types.Poison {
			return
		}
		if w := x.spelled(n, t, gens); w != tab.TypeString(t, gens) {
			spelled = append(spelled, make([]string, i+1-len(spelled))...)
			spelled[i] = w
		}
	}
	if s.Self != nil {
		self := x.self
		if self == types.NoType {
			self = types.Poison
		}
		p := types.Param{Name: source.SymSelf, Type: self, Span: s.Self.Span()}
		switch s.Self.Mode {
		case ast.ByValue:
			sig.Receiver = types.ByValue
		case ast.ByShared:
			sig.Receiver = types.ByShared
			p.Type = x.wrap(tab.Ref, self)
		case ast.ByMut:
			sig.Receiver = types.ByMut
			p.Type = x.wrap(tab.Mut, self)
		}
		if s.Self.Into != nil {
			if r := x.ref(s.Self.Into); r.Kind == resolve.RefParam {
				p.Into = types.ParamIndex(r.A)
			}
		}
		sig.Params = append(sig.Params, p)
	}
	for _, p := range s.Params {
		if p == nil {
			continue
		}
		pr := types.Param{Type: x.typeOf(p.Type, posPlain), Const: p.Const, Var: p.Var, Span: p.Span()}
		if p.Name != nil {
			pr.Name = p.Name.Name
		}
		if p.Into != nil {
			if r := x.ref(p.Into); r.Kind == resolve.RefParam {
				pr.Into = types.ParamIndex(r.A)
			}
		}
		spell(len(sig.Params), p.Type, pr.Type)
		sig.Params = append(sig.Params, pr)
	}
	var resOK bool
	sig.Result, sig.Errors, resOK = x.result(s.Result)
	if !resOK {
		// An error set that did not resolve (its E0201 is resolve's) leaves
		// the result Poison, so no use of the function and no try, fail or
		// return in its body is judged against a partial set.
		sig.Result, sig.Errors = types.Poison, types.EmptyErrors
	}
	if s.Result != nil {
		spell(len(sig.Params), s.Result, tab.ReturnType(sig.Result, sig.Errors))
	}
	if spelled != nil {
		sig.Spelled = append(spelled, make([]string, len(sig.Params)+1-len(spelled))...)
	}
	x.spelling, x.aliases = spelling, aliases
	sig.Variadic = s.Ellipsis != (source.Span{})
	return sig
}

// spelled spells type expression n, whose type is t, as TypeString spells
// t, but each alias n names (x.aliases) by its name and arguments, as the
// declaration writes it (MKH-303). The walk follows TypeString's prefix
// operators, a named type's arguments and E!T; any other shape, a fn type
// or a record, is spelled resolved, aliases and all.
func (x *tctx) spelled(n ast.Node, t types.TypeID, gs []types.GenericParam) string {
	var b strings.Builder
	x.spell(&b, n, t, gs)
	return b.String()
}

func (x *tctx) spell(b *strings.Builder, n ast.Node, t types.TypeID, gs []types.GenericParam) {
	tab := x.tab()
	if u, ok := x.aliases[n]; ok {
		b.WriteString(x.m.c.cfg.Interner.Str(u.item.Name))
		x.spellArgs(b, typeArgsOf(n), u.args, gs)
		return
	}
	ty := tab.Get(t)
	prefix := func(op string, k types.TypeKind, elem ast.Node) bool {
		if ty.Kind != k {
			return false
		}
		b.WriteString(op)
		x.spellOperand(b, elem, ty.Elem, gs, k == types.KMut)
		return true
	}
	done := false
	switch n := n.(type) {
	case *ast.ParenType:
		x.spell(b, n.Type, t, gs)
		return
	case *ast.ParenExpr:
		x.spell(b, n.X, t, gs)
		return
	case *ast.RecordLit:
		if len(n.Fields) == 1 && n.Fields[0] != nil && n.Fields[0].Value == nil && n.Fields[0].Name != nil {
			x.spell(b, n.Fields[0].Name, t, gs) // (T) is T in a bracket, as typeOf reads it
			return
		}
	case *ast.RefType:
		done = prefix("&", types.KRef, n.Elem)
	case *ast.MutType:
		done = prefix("mut ", types.KMut, n.Elem)
	case *ast.MutSliceType:
		done = prefix("mut []", types.KMutSlice, n.Elem)
	case *ast.SliceType:
		done = prefix("[]", types.KSlice, n.Elem)
	case *ast.OptionalType:
		done = prefix("?", types.KOptional, n.Elem)
	case *ast.RawPtrType:
		done = prefix("*", types.KRawPtr, n.Elem)
	case *ast.UnaryExpr:
		switch n.Op {
		case ast.TokAmp:
			done = prefix("&", types.KRef, n.X)
		case ast.TokMut:
			done = prefix("mut ", types.KMut, n.X)
		case ast.TokStar:
			done = prefix("*", types.KRawPtr, n.X)
		}
	case *ast.ArrayType:
		done = prefix("["+tab.ConstArgString(ty.Len, gs)+"]", types.KArray, n.Elem)
	case *ast.ErrorType:
		done = x.spellUnion(b, n.Errors, n.Ok, ty, gs)
	case *ast.BinaryExpr:
		if n.Op == ast.TokBang {
			done = x.spellUnion(b, n.X, n.Y, ty, gs)
		}
	case *ast.PathType, *ast.IndexExpr:
		if args := typeArgsOf(n); ty.Kind == types.KNamed && len(args) > 0 {
			b.WriteString(x.m.c.cfg.Interner.Str(x.m.c.cfg.Items.Get(ty.Item).Name))
			x.spellArgs(b, args, ty.Args, gs)
			done = true
		}
	}
	if !done {
		b.WriteString(tab.TypeString(t, gs))
	}
}

// spellOperand is writeOperand's: the operand of a prefix operator, in
// parentheses where TypeString writes them, but not around an alias.
func (x *tctx) spellOperand(b *strings.Builder, n ast.Node, t types.TypeID, gs []types.GenericParam, afterMut bool) {
	tab := x.tab()
	k := tab.Kind(t)
	union := k == types.KErrorSet && len(tab.Members(tab.Get(t).Errors)) > 1
	if _, alias := x.aliases[n]; !alias && (k == types.KErrorUnion || union || afterMut && k == types.KSlice) {
		b.WriteByte('(')
		x.spell(b, n, t, gs)
		b.WriteByte(')')
		return
	}
	x.spell(b, n, t, gs)
}

// spellUnion spells E!T of type ty: an alias of an error set by its name.
func (x *tctx) spellUnion(b *strings.Builder, es, ok ast.Node, ty *types.Type, gs []types.GenericParam) bool {
	if ty.Kind != types.KErrorUnion {
		return false
	}
	for p, isParen := es.(*ast.ParenType); isParen; p, isParen = es.(*ast.ParenType) {
		es = p.Type
	}
	if u, alias := x.aliases[es]; alias && len(u.args) == 0 {
		b.WriteString(x.m.c.cfg.Interner.Str(u.item.Name))
	} else {
		b.WriteString(x.tab().TypeString(x.tab().ErrorSetType(ty.Errors), gs))
	}
	b.WriteByte('!')
	x.spell(b, ok, ty.Elem, gs)
	return true
}

// spellArgs spells [A, B] for written arguments ns of values args.
func (x *tctx) spellArgs(b *strings.Builder, ns []ast.TypeOrExpr, args []types.GenericArg, gs []types.GenericParam) {
	if len(args) == 0 {
		return
	}
	tab := x.tab()
	b.WriteByte('[')
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		switch {
		case a.IsConst:
			b.WriteString(tab.ConstArgString(a.Const, gs))
		case len(ns) == len(args):
			x.spell(b, ns[i], a.Type, gs)
		default:
			b.WriteString(tab.TypeString(a.Type, gs))
		}
	}
	b.WriteByte(']')
}

// typeArgsOf is the written type arguments of a named type node.
func typeArgsOf(n ast.Node) []ast.TypeOrExpr {
	switch n := n.(type) {
	case *ast.PathType:
		if n.Args != nil {
			return n.Args.Args
		}
	case *ast.IndexExpr:
		return n.Args
	}
	return nil
}

// declFunc declares a function-like item: a function, method, extern, fn
// literal, contract predicate or invariant.
func (m *Module) declFunc(it *types.Item, x *tctx, n ast.Node) {
	switch n := n.(type) {
	case *ast.FuncDecl:
		m.declSig(it, x, n.Sig, n.Body, false)
		m.nakedDecl(it, x, n.Sig, n.Naked)
	case *ast.ExternDecl:
		m.declSig(it, x, n.Sig, n.Body, true)
		m.nakedDecl(it, x, n.Sig, n.Naked)
	case *ast.Contract:
		m.declContract(it, x, n)
	case *ast.InvariantMember:
		m.declInvariant(it, x)
	case *ast.FnLit:
		m.declLiteral(it, x, n)
	}
}

// ownerContext sets Self and returns the owning item's generics for a method: an
// impl's for its methods, the type's (renamed by the owning type's arguments) for a
// Type.Name function, none for a primitive owner.
func (m *Module) ownerContext(it *types.Item, x *tctx, s *ast.Signature) []types.GenericParam {
	c := m.c
	tab := c.cfg.Types
	if it.OwnerPrim != types.NoType {
		x.self = it.OwnerPrim
		return nil
	}
	if it.Owner == types.NoItem {
		if s.Name != nil && s.Name.OwnerArgs != nil {
			// `A[T].M` with an owner resolve could not find (its E0201):
			// the written parameters, opaque, and Self Poison.
			x.self = types.Poison
			return writtenOwnerGens(s.Name.OwnerArgs.Args)
		}
		return nil
	}
	o := c.cfg.Items.Get(it.Owner)
	switch o.Kind {
	case types.KindImpl:
		m.ensureGuarded(o)
		d, _ := o.Decl.(*types.ImplDecl)
		if d == nil {
			x.self = types.Poison
			return nil
		}
		x.self = d.For
		return d.Generics
	case types.KindStruct, types.KindEnum, types.KindInterface:
		gens := append([]types.GenericParam(nil), c.genericsOf(o)...)
		if s.Name != nil && s.Name.OwnerArgs != nil && len(s.Name.OwnerArgs.Args) != len(gens) {
			// `A[T].M` for an A with another number of parameters: resolve
			// numbered the written names, so the unit gets exactly those, as
			// opaque parameters, and Self is Poison.
			x.errorf(diag.E0304, s.Name.OwnerArgs.Span(), "`"+c.cfg.Interner.Str(o.Name)+"` takes a different number of type arguments").
				Text("reason", "type-arguments").Int("expected", int64(len(gens))).Int("found", int64(len(s.Name.OwnerArgs.Args)))
			x.self = types.Poison
			return writtenOwnerGens(s.Name.OwnerArgs.Args)
		}
		if s.Name != nil && s.Name.OwnerArgs != nil {
			for i, a := range s.Name.OwnerArgs.Args {
				if id, ok := a.(*ast.Ident); ok && i < len(gens) {
					gens[i].Name, gens[i].Span = id.Name, id.Span()
				}
			}
		}
		x.self = tab.Named(o.ID, c.identity(gens))
		return gens
	case types.KindAlias:
		d := c.aliasDecl(o, m)
		if d == nil {
			x.self = types.Poison
			return nil
		}
		x.self = d.Type
		return d.Generics
	}
	x.self = types.Poison
	return nil
}

// writtenOwnerGens are opaque parameters named as written in `A[T, U].M`.
func writtenOwnerGens(args []ast.TypeOrExpr) []types.GenericParam {
	gens := make([]types.GenericParam, len(args))
	for i, a := range args {
		if id, ok := a.(*ast.Ident); ok {
			gens[i].Name, gens[i].Span = id.Name, id.Span()
		}
	}
	return gens
}

// declSig declares a function, method or extern with an ast.Signature.
func (m *Module) declSig(it *types.Item, x *tctx, s *ast.Signature, body *ast.Block, extern bool) {
	bodiless := body == nil
	if s == nil {
		it.Decl = placeholderDecl(it)
		return
	}
	prefix := m.ownerContext(it, x, s)
	fd := &types.FuncDecl{}
	m.d.gens[it.ID] = skeleton(s.TypeParams, prefix)
	fd.Sig = x.signature(it.ID, it.Name, s, prefix, extern, it.Exported)
	m.d.gens[it.ID] = fd.Sig.Generics
	// Bodiless functions: the parser admits them in
	// std (intrinsics), the test prelude and as externs; anywhere else it
	// reported E0101 "function needs a body" and still built the FuncDecl,
	// so check sees the recovered tree and adds nothing (an ICE would make
	// every recovered file an E0000).
	if bodiless {
		fd.Bodiless = true
		fd.Intrinsic = !extern && m.mod.Std
	}
	if extern {
		m.c.vectorABI(x, &fd.Sig, s) // E1107
		if !bodiless && it.Exported && m.in.Library {
			m.c.cFormBoundary(x, &fd.Sig, s) // E1109 (e1109.go)
		}
	}
	if s.Cpu != nil {
		m.cpuClause(it, x, &fd.Sig, s, bodiless && extern) // E1106
	}
	fd.Section = m.sectionClause(x, s.Section)
	for _, c := range s.Contracts {
		if ci := m.itemAt(x.fx, c); ci != types.NoItem {
			k := types.Requires
			if c.Kind == ast.Ensures {
				k = types.Ensures
			}
			fd.Sig.Contracts = append(fd.Sig.Contracts, types.Contract{Kind: k, Body: ci, Span: c.Span(), Text: m.c.contractText(c)})
		}
	}
	it.Decl = fd
	x.gens = fd.Sig.Generics
	j := flowJob{it: it, sig: &fd.Sig, x: x, cl: x.clauses(s.Flows, &fd.Sig)}
	if s.Result != nil && len(s.Flows) == 0 {
		// A clause goes right after the result type; with other clauses
		// written, E0601 offers alternatives only.
		end := s.Result.Span()
		j.fix = fromFix{ok: true, at: source.Span{File: end.File, Start: end.End, End: end.End}, body: body, item: it.ID, x: x}
	}
	m.d.flows = append(m.d.flows, j)
}

// declContract is a requires or ensures predicate's signature: one
// &P per function parameter the predicate names, in parameter order, then
// result: &R for ensures; it returns bool.
func (m *Module) declContract(it *types.Item, x *tctx, n *ast.Contract) {
	tab := m.c.cfg.Types
	fd := &types.FuncDecl{Sig: types.Signature{Item: it.ID, Name: it.Name, Result: types.Bool, Span: n.Span()}}
	it.Decl = fd
	owner := m.c.cfg.Items.Get(it.Owner)
	m.ensureGuarded(owner)
	pd, ok := owner.Decl.(*types.FuncDecl)
	if !ok {
		return
	}
	fd.Sig.Generics = pd.Sig.Generics
	named := map[uint32]bool{}
	ast.Inspect(n.X, func(c ast.Node) bool {
		if c == nil {
			return false
		}
		r := x.ref(c)
		if r.Kind == resolve.RefParam && types.ItemID(r.B) == owner.ID {
			named[r.A] = true
		}
		// A parameter named only inside a fn literal of the predicate is a
		// capture of it, whose source is the predicate's &P: the
		// predicate names it too. A nested literal's chain starts
		// at an outer literal's capture, which this walk also visits.
		if _, ok := c.(*ast.FnLit); ok && r.Kind == resolve.RefItem {
			if li := m.byItem[types.ItemID(r.A)]; li != nil {
				for _, cp := range li.Captures {
					if cp.Of.Kind == resolve.RefParam && types.ItemID(cp.Of.B) == owner.ID {
						named[cp.Of.A] = true
					}
				}
			}
		}
		return true
	})
	for i, p := range pd.Sig.Params {
		if named[uint32(i+1)] {
			fd.Sig.Params = append(fd.Sig.Params, types.Param{Name: p.Name, Type: x.wrap(tab.Ref, p.Type), Span: p.Span})
		}
	}
	if n.Kind == ast.Ensures {
		fd.Sig.Params = append(fd.Sig.Params, types.Param{Name: source.SymResult, Type: x.wrap(tab.Ref, pd.Sig.Result), Span: n.Span()})
	}
}

// declInvariant is an invariant's signature: self: &Self, returning bool.
func (m *Module) declInvariant(it *types.Item, x *tctx) {
	tab := m.c.cfg.Types
	fd := &types.FuncDecl{Sig: types.Signature{Item: it.ID, Name: it.Name, Result: types.Bool, Span: it.Span, Receiver: types.ByShared}}
	it.Decl = fd
	owner := m.c.cfg.Items.Get(it.Owner)
	gens := m.c.genericsOf(owner)
	fd.Sig.Generics = gens
	self := tab.Named(owner.ID, m.c.identity(gens))
	fd.Sig.Params = []types.Param{{Name: source.SymSelf, Type: tab.Ref(self), Span: it.Span}}
}

// declLiteral is a fn literal's signature: never generic of its
// own; the enclosing item's parameters stay in scope.
func (m *Module) declLiteral(it *types.Item, x *tctx, n *ast.FnLit) {
	var gens []types.GenericParam
	if it.Owner != types.NoItem {
		owner := m.c.cfg.Items.Get(it.Owner)
		m.ensureGuarded(owner)
		gens = allGenerics(owner.Decl)
	}
	x.gens = gens
	fd := &types.FuncDecl{Sig: types.Signature{Item: it.ID, Name: it.Name, Generics: gens, Span: n.Span()}}
	for _, p := range n.Params {
		if p == nil {
			continue
		}
		pr := types.Param{Type: x.typeOf(p.Type, posPlain), Const: p.Const, Var: p.Var, Span: p.Span()}
		if p.Name != nil {
			pr.Name = p.Name.Name
		}
		if p.Into != nil {
			if r := x.ref(p.Into); r.Kind == resolve.RefParam {
				pr.Into = types.ParamIndex(r.A)
			}
		}
		fd.Sig.Params = append(fd.Sig.Params, pr)
	}
	var resOK bool
	fd.Sig.Result, fd.Sig.Errors, resOK = x.result(n.Result)
	if !resOK {
		fd.Sig.Result, fd.Sig.Errors = types.Poison, types.EmptyErrors
	}
	it.Decl = fd
	m.d.flows = append(m.d.flows, flowJob{it: it, sig: &fd.Sig, x: x, cl: &flowClauses{}, lit: true})
}

// runFlows is step 5 of Signatures: the flows of every signature, in
// item order.
func (m *Module) runFlows() {
	for i := range m.d.flows {
		j := &m.d.flows[i]
		m.guard(j.it, func() {
			for _, p := range j.sig.Params {
				if p.Type == types.Poison {
					return
				}
			}
			if j.sig.Result == types.Poison {
				return
			}
			fl, ds := m.c.signatureFlows(flowFn{sig: j.sig, mod: m.ID, env: m.c.NewEnv(j.sig.Generics), at: j.sig.Span, fix: &j.fix}, j.cl)
			j.sig.Flows = fl
			if j.lit && fl.Ambiguous {
				// A literal takes the sources of the fn type it is
				// written for; only its enclosing body knows that type, so
				// it reports these when the type gives none (fnLit).
				if m.litE0601 == nil {
					m.litE0601 = map[types.ItemID][]*diag.Diagnostic{}
				}
				m.litE0601[j.it.ID] = ds
				ds = nil
			}
			m.Diags = append(m.Diags, ds...)
			m.c.recordFieldClause(j)
		})
	}
}

// entry is the Main check for a bin root: Main must be an
// exported, non-generic function taking &System and returning void, int,
// E!void or E!int. A missing Main is resolve's E0201, so check reports
// none.
func (m *Module) entry() {
	if !m.in.BinRoot {
		return
	}
	c := m.c
	for _, ii := range m.info.Items {
		it := c.cfg.Items.Get(ii.Item)
		if it.Name != source.SymMain || it.Kind != types.KindFunc || it.Owner != types.NoItem || it.OwnerPrim != types.NoType {
			continue
		}
		m.main = it.ID
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok {
			return
		}
		m.guard(it, func() { m.mainRules(it, fd) })
		return
	}
}

func (m *Module) mainRules(it *types.Item, fd *types.FuncDecl) {
	c := m.c
	tab := c.cfg.Types
	s := &fd.Sig
	for _, p := range s.Params {
		if p.Type == types.Poison {
			return
		}
	}
	if s.Result == types.Poison {
		return
	}
	ok := it.Exported && len(s.Generics) == 0 && len(s.Params) == 1 && (s.Result == types.Void || s.Result == types.Int)
	if ok && c.known.System != types.NoItem {
		ok = s.Params[0].Type == tab.Ref(tab.Named(c.known.System, nil))
	}
	if ok {
		return
	}
	m.Diags = append(m.Diags, diag.New(diag.E0301, s.Span, "`Main` must be `export Main(sys: &System)` returning void, int, E!void or E!int").
		Text("expected", "export Main(sys: &System)").Text("found", c.sigString(s)).
		Alt("export Main(sys: &System)").Alt("export Main(sys: &System): int").
		Alt("export Main(sys: &System): E!void").Alt("export Main(sys: &System): E!int"))
}

// sigString spells a signature's parameter and result types for a message,
// with its declared clauses once step 5 (flows) has settled them (flowText).
func (c *Checker) sigString(s *types.Signature) string {
	out := c.sigHead(s)
	if f := c.flowText(s); f != "" {
		out += " " + f
	}
	return out
}

// sigWithClauses is sigString with the clauses flows, as written in source,
// in place of the settled ones: for a signature judged before step 5 (flows).
func (c *Checker) sigWithClauses(s *types.Signature, flows []ast.Flow) string {
	out := c.sigHead(s)
	if f := c.writtenFlows(flows); f != "" {
		out += " " + f
	}
	return out
}

// writtenFlows spells written clauses as flowText spells settled ones:
// `from p, q.f`, `*x into y, *z into w`, `ends p`, in source order. A run of
// stores is one list joined with ", ", as flowText joins them and as the
// language reference writes Swap's `*a into b, *b into a`, so a
// signature prints the same from its own module and from another.
func (c *Checker) writtenFlows(flows []ast.Flow) string {
	in := c.cfg.Interner
	name := func(id *ast.Ident) string {
		if id == nil {
			return "?"
		}
		return in.Str(id.Name)
	}
	var parts []string
	inStores := false // the last part is a run of stores
	for _, f := range flows {
		switch f := f.(type) {
		case *ast.FromFlow:
			inStores = false
			srcs := func(ss []*ast.Source) string {
				var from []string
				for _, src := range ss {
					if src == nil {
						continue
					}
					var steps []string
					for _, id := range src.Names {
						steps = append(steps, name(id))
					}
					from = append(from, strings.Join(steps, "."))
				}
				return strings.Join(from, ", ")
			}
			if len(f.Fields) > 0 {
				// A per-field clause: `from (f: a, g: b)`.
				var fs []string
				for _, x := range f.Fields {
					if x != nil {
						fs = append(fs, name(x.Name)+": "+srcs(x.Sources))
					}
				}
				parts = append(parts, "from ("+strings.Join(fs, ", ")+")")
				continue
			}
			parts = append(parts, "from "+srcs(f.Sources))
		case *ast.StoreFlow:
			st := "*" + name(f.From) + " into " + name(f.Into)
			if inStores && len(parts) > 0 {
				parts[len(parts)-1] += ", " + st
			} else {
				parts = append(parts, st)
			}
			inStores = true
			continue
		case *ast.EndsFlow:
			inStores = false
			parts = append(parts, "ends "+name(f.Name))
		}
	}
	return strings.Join(parts, " ")
}

// sigHead is sigString without the clauses after the result.
func (c *Checker) sigHead(s *types.Signature) string {
	tab := c.cfg.Types
	out := c.cfg.Interner.Str(s.Name) + "("
	for i, p := range s.Params {
		if i > 0 {
			out += ", "
		}
		if i == 0 && s.Receiver != types.NoReceiver {
			out += [...]string{"", "self", "&self", "mut self"}[min(int(s.Receiver), 3)]
		} else {
			if p.Const {
				// sameSig compares const-ness, so E0707's two lines must show it.
				out += "const "
			}
			out += c.cfg.Interner.Str(p.Name) + ": " + tab.TypeString(p.Type, s.Generics)
		}
		if p.Into != types.NoParam {
			out += " into " + c.sigParam(s, p.Into)
		}
	}
	out += ")"
	if r := tab.ReturnType(s.Result, s.Errors); r != types.Void {
		out += ": " + tab.TypeString(r, s.Generics)
	}
	return out
}

// sigParam is parameter p of s as a clause names it: self, or its name.
func (c *Checker) sigParam(s *types.Signature, p types.ParamIndex) string {
	if p == types.NoParam || int(p) > len(s.Params) {
		return "?"
	}
	if int(p) == 1 && s.Receiver != types.NoReceiver {
		return "self"
	}
	return c.cfg.Interner.Str(s.Params[p-1].Name)
}

// flowText spells s's declared clauses as the reference writes them
// (a signature in a diagnostic prints with its from and into clauses):
// `from p, q.f`, `*x into y`, `ends p`. An
// automatic answer is written nowhere in source, so it is not printed.
func (c *Checker) flowText(s *types.Signature) string {
	var parts []string
	srcName := func(src types.Source) string {
		n := c.sigParam(s, src.Param)
		if src.Param != types.NoParam && int(src.Param) <= len(s.Params) {
			t := s.Params[src.Param-1].Type
			for _, st := range src.Path {
				n += "." + c.stepName(t, st.Field)
				t = st.Type
			}
		}
		return n
	}
	if s.Flows.Declared && len(s.Flows.Fields) > 0 {
		parts = append(parts, c.fieldFlowText(s, srcName)) // a per-field clause
	} else if s.Flows.Declared {
		var from []string
		for _, src := range s.Flows.Sources {
			from = append(from, srcName(src))
		}
		if len(from) > 0 {
			parts = append(parts, "from "+strings.Join(from, ", "))
		}
	}
	var st []string
	for _, x := range s.Flows.Stores {
		st = append(st, "*"+c.sigParam(s, x.From)+" into "+c.sigParam(s, x.Into))
	}
	if len(st) > 0 {
		parts = append(parts, strings.Join(st, ", "))
	}
	var ends []string
	for _, p := range s.Flows.Ends {
		ends = append(ends, c.sigParam(s, p))
	}
	if len(ends) > 0 {
		parts = append(parts, "ends "+strings.Join(ends, ", "))
	}
	return strings.Join(parts, " ")
}

// stepName is field i of the struct t names, references removed.
func (c *Checker) stepName(t types.TypeID, i uint32) string {
	tab := c.cfg.Types
	for tab.Kind(t) == types.KRef || tab.Kind(t) == types.KMut {
		t = tab.Get(t).Elem
	}
	if ty := tab.Get(t); ty.Kind == types.KNamed {
		if d, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl); ok && int(i) < len(d.Fields) {
			return c.cfg.Interner.Str(d.Fields[i].Name)
		}
	}
	return "?"
}
