// Why: every bound check asks "does type T implement interface I", so the
// impls of a program must be legal, unique and indexed before any body is
// checked.
//
// What: implPass (duplicate blocks, coherence and the table, impl
// signatures, name collisions), coherence (where an impl may live, E0704,
// built-in bound assertions such as `unsafe impl Send`), implSignatures and
// sameSig (E0707), implFlowChecks (E0608), implRequires, implCollisions
// (E0206), and the lookup index implState.
//
// How: an impl is keyed by its type constructor (ctorKey). It reads
// declarations only, never facts. Lookups go through a per-module index
// built on first use and read-only after; its answers equal the plain scans
// (impls_index_test.go).

package check

import (
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// ctorKey names a type constructor for the impl table: a struct, enum
// or error item; a scalar or str by its fixed TypeID; any other builtin form
// (&T, []T, ?T, [N]T, E!T, records, fn) by its kind.
type ctorKey struct {
	item types.ItemID
	kind types.TypeKind
	prim types.TypeID
}

// implKey is one (interface, constructor) pair; coherence allows one impl
// per pair in the whole program.
type implKey struct {
	iface types.ItemID
	ctor  ctorKey
}

// ctorOf is t's constructor, false for a type parameter, Self, an inference
// variable or Poison (a blanket target, or no target at all).
func (c *Checker) ctorOf(t types.TypeID) (ctorKey, bool) {
	tab := c.cfg.Types
	if t == types.Poison || t == types.NoType {
		return ctorKey{}, false
	}
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KNamed:
		return ctorKey{item: ty.Item}, true
	case types.KParam, types.KSelf, types.KInfer, types.KPoison, types.KNone:
		return ctorKey{}, false
	case types.KErrorSet:
		if ms := tab.Members(ty.Errors); len(ms) == 1 && !ms[0].IsParam {
			return ctorKey{item: ms[0].Item}, true
		}
		return ctorKey{kind: ty.Kind}, true
	case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KVoid, types.KNever, types.KStr:
		return ctorKey{prim: t}, true
	}
	return ctorKey{kind: ty.Kind}, true
}

// ctorModule is the module that declares a constructor, NoModule for a
// builtin one.
func (c *Checker) ctorModule(k ctorKey) types.ModuleID {
	if k.item == types.NoItem {
		return types.NoModule
	}
	return c.cfg.Items.Get(k.item).Module
}

// implFlowJob is one impl method whose flows step 5 (flows) compares with its
// interface method's (E0608, after runFlows).
type implFlowJob struct {
	it          *types.Item
	impl, iface *types.Signature
}

// implPass is step 3 of Signatures: duplicate impl blocks, coherence and
// the table, impl signatures, impl-method name collisions. It reads
// declarations only, never facts.
func (m *Module) implPass() {
	if m.info != nil {
		m.duplicateImpls()
	}
	var impls []*types.Item
	for _, iid := range m.itemIDs() {
		if it := m.c.cfg.Items.Get(iid); it.Kind == types.KindImpl {
			impls = append(impls, it)
		}
	}
	for _, it := range impls {
		m.guard(it, func() { m.coherence(it) })
	}
	if m.info != nil { // E0707 only; a loaded module's are the cache's
		for _, id := range m.implList {
			it := m.c.cfg.Items.Get(id)
			m.guard(it, func() { m.implSignatures(it) })
		}
	}
	m.implCollisions()
}

// duplicateImpls is E0704 `reason: duplicate` for every block resolve gave
// no item; nothing inside such a block is checked.
func (m *Module) duplicateImpls() {
	for _, di := range m.info.DuplicateImpls {
		if di.File < 0 || di.File >= len(m.files) {
			continue
		}
		fx := m.files[di.File]
		n := fx.node(di.Node)
		at := keywordSpan(fx, n.Span().Start, ast.TokImpl)
		if di.First != types.NoItem && m.synthOf(di.First) {
			m.writtenErrorFormat(at, di.First)
			continue
		}
		d := diag.New(diag.E0704, at, "a second impl of the same interface for the same type").Text("reason", "duplicate")
		if di.First != types.NoItem {
			m.citeFirst(d, di.First)
		}
		m.Diags = append(m.Diags, d.Alt("delete this impl").Alt("merge its methods into the first impl"))
	}
}

// keywordSpan is the span of the first token of kind at or after off in
// file fx; an empty span at off when there is none.
func keywordSpan(fx *fileIndex, off uint32, kind ast.TokenKind) source.Span {
	toks := fx.file.Tokens
	i := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= off })
	for ; i < len(toks) && toks[i].Kind != ast.TokEOF; i++ {
		if toks[i].Kind == kind {
			return source.Span{File: fx.info.FileID, Start: toks[i].Start, End: toks[i].Start + toks[i].Len}
		}
	}
	return source.Span{File: fx.info.FileID, Start: off, End: off}
}

// implConflict reports one E0704 at an impl.
func (m *Module) implConflict(it *types.Item, reason, msg string, first types.ItemID) *diag.Diagnostic {
	d := diag.New(diag.E0704, it.Span, msg).Text("reason", reason)
	if first != types.NoItem {
		m.citeFirst(d, first)
	}
	m.Diags = append(m.Diags, d)
	return d
}

// citeFirst adds the fact `first` for the impl a conflict collides with:
// its declaration, or for a generated impl its `gen` line (two `gen`
// lines emitting one impl are E0704 citing both; gen.Wrap anchors the
// diagnostic itself at the second impl's line), with the fact
// `first_output` naming the generated text.
func (m *Module) citeFirst(d *diag.Diagnostic, first types.ItemID) {
	it := m.c.cfg.Items.Get(first)
	if it.Origin == nil {
		d.Loc("first", it.Span)
		return
	}
	d.Loc("first", it.Origin.Gen).Loc("first_output", it.Span)
}

// coherence judges one impl and, when it is legal, adds it to the table.
func (m *Module) coherence(it *types.Item) {
	c := m.c
	d, _ := it.Decl.(*types.ImplDecl)
	if d == nil || d.For == types.Poison || (d.Bound.Kind == types.BoundInterface && d.Bound.Item == types.NoItem) {
		return // the head did not resolve: reported already
	}
	switch d.Bound.Kind {
	case types.BoundPlain, types.BoundFields:
		m.implConflict(it, "built-in bound", "`Plain` and `Fields` are computed from the fields and cannot be implemented", types.NoItem).
			Alt("delete this impl")
		return
	case types.BoundSend, types.BoundShare:
		name := "Send"
		if d.Bound.Kind == types.BoundShare {
			name = "Share"
		}
		if !d.Unsafe { // E1101
			m.Diags = append(m.Diags, diag.New(diag.E1101, it.Span, "`"+name+"` can only be asserted by `unsafe impl`").
				Text("operation", "assert "+name).Alt("write `unsafe impl` in a module listed in pkg.mk's unsafe_modules"))
			return
		}
		ck, ok := c.ctorOf(d.For)
		if !ok {
			m.implConflict(it, "blanket", "an impl must name a type constructor, not a type parameter", types.NoItem)
			return
		}
		if c.ctorModule(ck) != m.ID {
			// A built-in bound has no module: the assertion lives with C.
			m.implConflict(it, "orphan", "`unsafe impl "+name+"` must be in the module that declares the type", types.NoItem).
				Alt("move the impl to the type's module")
			return
		}
		if ck.item != types.NoItem {
			if sd, ok := c.cfg.Items.Get(ck.item).Decl.(*types.StructDecl); ok && sd.PlainView {
				m.implConflict(it, "plain view", "`unsafe impl "+name+"` cannot be asserted on a `plain view struct`", types.NoItem).
					Alt("remove `unsafe impl " + name + "`")
			}
		}
		return
	}
	if d.Bound.Kind == types.BoundInterface && c.isLaneItem(d.Bound.Item) {
		// Lane is computed (the integer types, int, uint, f32, f64),
		// never implemented.
		m.implConflict(it, "computed", "`simd.Lane` is computed: the integer types, `int`, `uint`, `f32` and `f64` are lanes, and nothing else can be one", types.NoItem).
			Alt("delete this impl")
		return
	}
	if d.Bound.Kind == types.BoundInterface && c.isIntegerItem(d.Bound.Item) {
		// math.Integer is computed as Lane is.
		m.implConflict(it, "computed", "`math.Integer` is computed: the integer types, `int` and `uint` are integers, and nothing else can be one", types.NoItem).
			Alt("delete this impl")
		return
	}
	if d.Unsafe {
		m.implConflict(it, "unsafe on an ordinary impl", "only `Send` and `Share` are asserted with `unsafe impl`", types.NoItem).
			Alt("remove `unsafe`")
		return
	}
	ck, ok := c.ctorOf(d.For)
	if !ok {
		m.implConflict(it, "blanket", "an impl must name a type constructor, not a type parameter", types.NoItem).
			Alt("implement the interface for each type")
		return
	}
	iface := c.cfg.Items.Get(d.Bound.Item)
	cm := c.ctorModule(ck)
	// A builtin constructor belongs to std, so std or the interface's module
	// may implement for it.
	if iface.Module != m.ID && cm != m.ID && !(cm == types.NoModule && m.mod.Std) {
		m.implConflict(it, "orphan", "an impl must be in the module of the interface or of the type", types.NoItem).
			Alt("move the impl to the interface's module").Alt("move the impl to the type's module")
		return
	}
	key := implKey{iface: iface.ID, ctor: ck}
	if first, dup := m.implTab[key]; dup {
		m.implConflict(it, "duplicate", "a second impl of the same interface for the same type", first).
			Alt("delete this impl").Alt("merge its methods into the first impl")
		return
	}
	other := iface.Module
	if other == m.ID {
		other = cm
	}
	if other != types.NoModule && other != m.ID {
		if om := c.module(other); om != nil {
			if first, dup := om.implTab[key]; dup {
				m.implConflict(it, "duplicate", "the type already implements this interface in another module", first).
					Alt("delete this impl")
				return
			}
		}
	}
	m.implTab[key] = it.ID
	m.implList = append(m.implList, it.ID)
}

// implSignatures compares every method of an impl with its interface's
// (E0707): the interface signature with Self := For and the
// interface's parameters := the impl's arguments. The flow comparison waits
// for step 5 (flows) (E0608).
func (m *Module) implSignatures(it *types.Item) {
	c := m.c
	d := it.Decl.(*types.ImplDecl)
	ifaceIt := c.cfg.Items.Get(d.Bound.Item)
	idecl, _ := c.declOfItem(ifaceIt).(*types.InterfaceDecl)
	if idecl == nil {
		return
	}
	byName := map[source.Symbol]*types.Item{}
	for _, mid := range d.Methods {
		mi := c.cfg.Items.Get(mid)
		if _, seen := byName[mi.Name]; !seen {
			byName[mi.Name] = mi
		}
	}
	want := map[source.Symbol]bool{}
	for k := range idecl.Methods {
		im := &idecl.Methods[k]
		want[im.Name] = true
		mi := byName[im.Name]
		exp := m.expectedSig(d, idecl, im)
		if mi == nil {
			m.Diags = append(m.Diags, diag.New(diag.E0707, it.Span, "the impl has no method `"+c.cfg.Interner.Str(im.Name)+"`").
				Text("expected", m.ifaceSigText(ifaceIt, exp)).Text("found", "nothing"))
			continue
		}
		fd, _ := mi.Decl.(*types.FuncDecl)
		if fd == nil || sigHasPoison(&fd.Sig) || sigHasPoison(exp) {
			continue
		}
		if !c.sameSig(exp, &fd.Sig, len(d.Generics)) {
			m.Diags = append(m.Diags, diag.New(diag.E0707, mi.Span, "this method's signature differs from the interface's").
				Text("expected", m.ifaceSigText(ifaceIt, exp)).Text("found", m.methodSigText(mi, &fd.Sig)))
			continue
		}
		if im.Errors == types.EmptyErrors && fd.Sig.Errors != types.EmptyErrors {
			// A result type parameter bound to an error union (Src[E!int]'s
			// Get(&self): T) is a value the method returns: the interface
			// method cannot fail, so a call through a bound, which handles
			// no error, would drop this method's.
			m.Diags = append(m.Diags, diag.New(diag.E0707, mi.Span, "`"+c.cfg.Interner.Str(ifaceIt.Name)+"`'s `"+c.cfg.Interner.Str(im.Name)+
				"` cannot fail, and this method can: an error union its result's type parameter stands for is a value the method returns, never an error it fails with").
				Text("expected", m.ifaceSigText(ifaceIt, exp)+" (cannot fail)").Text("found", m.methodSigText(mi, &fd.Sig)))
			continue
		}
		m.implFlows = append(m.implFlows, implFlowJob{it: mi, impl: &fd.Sig, iface: im})
	}
	for _, mid := range d.Methods {
		if mi := c.cfg.Items.Get(mid); !want[mi.Name] {
			m.Diags = append(m.Diags, diag.New(diag.E0707, mi.Span, "`"+c.cfg.Interner.Str(ifaceIt.Name)+"` has no method `"+
				c.cfg.Interner.Str(mi.Name)+"`").Text("expected", "nothing").Text("found", m.methodSigText(mi, &mi.Decl.(*types.FuncDecl).Sig)))
		}
	}
}

// methodSigText is an impl method's signature for E0707's found fact, with
// its clauses as written in source: E0707 is judged in step 3 (impls),
// before step 5 (flows) settles the flows, so the method's own Flows are
// still empty, and an inferred clause is never printed. A method with no
// node here (a provided Format) prints
// what its signature holds.
func (m *Module) methodSigText(mi *types.Item, s *types.Signature) string {
	if m.byItem[mi.ID] != nil {
		if n, _ := m.nodeOf(mi.ID); n != nil {
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Sig != nil {
				return m.c.sigWithClauses(s, fd.Sig.Flows)
			}
		}
	}
	return m.c.sigString(s)
}

// ifaceSigText is the interface method's signature as the impl must write
// it (exp, expectedSig's), with the interface's clauses as written: from
// this module's source when the interface is declared here, since its flows
// are not settled yet; another module's interface was settled before this
// module began, so its declared Flows print the same clauses (flowText).
func (m *Module) ifaceSigText(ifaceIt *types.Item, exp *types.Signature) string {
	if ifaceIt.Module == m.ID && m.byItem[ifaceIt.ID] != nil {
		if n, _ := m.nodeOf(ifaceIt.ID); n != nil {
			if d, ok := n.(*ast.InterfaceDecl); ok {
				for _, s := range d.Methods {
					if s != nil && s.Name != nil && s.Name.Name != nil && s.Name.Name.Name == exp.Name {
						return m.c.sigWithClauses(exp, s.Flows)
					}
				}
			}
		}
	}
	return m.c.sigString(exp)
}

// expectedSig is an interface method's signature as the impl must write it:
// Self := For, the interface's parameters := the impl's bound arguments, the
// method's own parameters renumbered after the impl's generics.
func (m *Module) expectedSig(d *types.ImplDecl, idecl *types.InterfaceDecl, im *types.Signature) *types.Signature {
	c := m.c
	tab := c.cfg.Types
	nIface := len(idecl.Generics)
	args := append([]types.GenericArg(nil), d.Bound.Args...)
	for len(args) < nIface {
		args = append(args, types.TypeArg(types.Poison))
	}
	own := im.Generics[min(nIface, len(im.Generics)):]
	for k, g := range own {
		idx := uint32(len(d.Generics) + k)
		if g.Const {
			args = append(args, types.ConstGenericArg(types.ConstArg{Kind: types.CAParam, Param: idx, Type: g.Type}))
		} else {
			args = append(args, types.TypeArg(tab.Param(idx)))
		}
	}
	s := *im
	s.Generics = append(append([]types.GenericParam(nil), d.Generics...), own...)
	for i := len(d.Generics); i < len(s.Generics); i++ {
		g := s.Generics[i]
		g.Bounds = c.substBounds(g.Bounds, args, d.For)
		if g.Const {
			g.Type = c.subst(g.Type, args, d.For)
		}
		s.Generics[i] = g
	}
	s.Params = append([]types.Param(nil), im.Params...)
	for i := range s.Params {
		s.Params[i].Type = c.subst(s.Params[i].Type, args, d.For)
	}
	rt := c.subst(tab.ReturnType(im.Result, im.Errors), args, d.For)
	s.Result, s.Errors = rt, types.EmptyErrors
	if ty := tab.Get(rt); ty.Kind == types.KErrorUnion {
		s.Result, s.Errors = ty.Elem, ty.Errors
	}
	return &s
}

// substBounds substitutes generic arguments into bounds' arguments.
func (c *Checker) substBounds(bs []types.Bound, args []types.GenericArg, self types.TypeID) []types.Bound {
	if len(bs) == 0 {
		return nil
	}
	out := make([]types.Bound, len(bs))
	for i, b := range bs {
		out[i] = b
		if len(b.Args) > 0 {
			out[i].Args = make([]types.GenericArg, len(b.Args))
			s := substituter{c: c, args: args, self: self}
			for j, a := range b.Args {
				out[i].Args[j] = s.genericArg(a)
			}
		}
	}
	return out
}

// sameSig is E0707's equality: receiver, parameter types, const
// markers, own type parameters with equal bounds, result and error set.
// `var` on a parameter is the body's business and not part of the
// comparison; a parameter's `into` is a flow,
// judged by implFlowChecks as E0608.
func (c *Checker) sameSig(want, got *types.Signature, nOwner int) bool {
	tab := c.cfg.Types
	if want.Receiver != got.Receiver || len(want.Params) != len(got.Params) || len(want.Generics) != len(got.Generics) {
		return false
	}
	for i := range want.Params {
		w, g := want.Params[i], got.Params[i]
		if w.Type != g.Type || w.Const != g.Const {
			return false
		}
	}
	for i := nOwner; i < len(want.Generics); i++ {
		w, g := want.Generics[i], got.Generics[i]
		if w.Const != g.Const || (w.Const && w.Type != g.Type) || !sameBounds(w.Bounds, g.Bounds) {
			return false
		}
	}
	return tab.ReturnType(want.Result, want.Errors) == tab.ReturnType(got.Result, got.Errors)
}

// sameBounds compares two bound lists as sets.
func sameBounds(a, b []types.Bound) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			found = found || boundEq(x, y)
		}
		if !found {
			return false
		}
	}
	return true
}

func boundEq(x, y types.Bound) bool {
	if x.Kind != y.Kind || x.Item != y.Item || len(x.Args) != len(y.Args) {
		return false
	}
	for i := range x.Args {
		if x.Args[i] != y.Args[i] {
			return false
		}
	}
	return true
}

func sigHasPoison(s *types.Signature) bool {
	for _, p := range s.Params {
		if p.Type == types.Poison {
			return true
		}
	}
	return s.Result == types.Poison
}

// implFlowChecks is the E0608 half of the impl check, after step 5 (flows)
// computed every flow: an impl method's flows, compared before
// substitution, must be covered by its interface method's (H covers B).
func (m *Module) implFlowChecks() {
	for _, j := range m.implFlows {
		m.implRequires(j)
		if j.impl.Flows.Ambiguous || j.impl.Flows.ClauseError || j.iface.Flows.Ambiguous || j.iface.Flows.ClauseError {
			continue
		}
		if flowsCovered(j.impl.Flows, j.iface.Flows, sigInto(j.impl), sigInto(j.iface)) {
			continue
		}
		m.Diags = append(m.Diags, diag.New(diag.E0608, j.it.Span, "this method's flows are not covered by the interface method's").
			Text("expected", m.c.flowsString(j.iface)).Text("found", m.c.flowsString(j.impl)))
	}
}

// implRequires is the impl rule for contracts: a compile-time `requires` of an impl
// method that names a generic of the impl or of the method, const or type,
// or a const parameter of the method, is E0706 at the clause. A caller
// through a bound (`H[T: I]` calling `x.M()` or `x.M[24]()`) sees only the
// interface method, which has no contracts (the parser's E0101), so the
// clause could never be checked at compile time there (`impl[const N:
// int] I for S[N]` with `M requires N > 0`, reached as `H[S[0]]`). The
// method's own const generics are included, and every type generic
// (`requires SizeOf[U]() < 4`, reached as `x.M[Big](t)` or through
// `impl[T]` as `H[W[Big]]`): the body may rely on such a clause through
// finiteRequires' skipped, implied or listed sets or a restated
// `SizeOf[U]()`, and a release build judges nothing at
// a bound call. A run-time clause (one naming a `var` or a parameter that
// is not const) is the entry check's and stays allowed, as does a clause
// over consts and literals alone, which is the same at every call. The
// impl's own module is the only one with its AST, and the diagnostic is
// the module's, so a cached module replays it.
func (m *Module) implRequires(j implFlowJob) {
	c := m.c
	sig := j.impl
	for _, ct := range sig.Contracts {
		if ct.Kind != types.Requires || m.byItem[ct.Body] == nil {
			continue
		}
		pred := c.cfg.Items.Get(ct.Body)
		n, fx := m.nodeOf(pred.ID)
		cn, ok := n.(*ast.Contract)
		if !ok || cn.X == nil || fx == nil {
			continue
		}
		runtime, names := false, false
		ast.Inspect(cn.X, func(x ast.Node) bool {
			if x == nil || runtime {
				return false
			}
			if id := x.ID(); int(id) < len(fx.info.Refs) {
				r := fx.info.Refs[id]
				own := types.ItemID(r.B) == pred.Owner || types.ItemID(r.B) == pred.ID
				switch {
				case r.Kind == resolve.RefItem:
					if it := c.cfg.Items.Get(types.ItemID(r.A)); it != nil && it.Kind == types.KindGlobal {
						runtime = true
					}
				case r.Kind == resolve.RefParam && own && r.A >= 1 && int(r.A) <= len(sig.Params):
					if sig.Params[r.A-1].Const {
						names = true
					} else {
						runtime = true
					}
				case r.Kind == resolve.RefTypeParam && own && int(r.A) < len(sig.Generics):
					names = true // a const generic, or a type generic (`SizeOf[U]() < 4`)
				}
			}
			return !runtime
		})
		if runtime || !names {
			continue
		}
		text := c.clauseText(pred)
		if text == "" {
			continue
		}
		m.Diags = append(m.Diags, diag.New(diag.E0706, ct.Span, "an impl method cannot require `"+text+"` at compile time: a caller through the interface bound cannot check it").
			Text("clause", text).
			Alt("check it in the method's body, or take the value as a parameter that is not const, or make the method inherent"))
	}
}

// ifaceImplMethod reports whether id is a method of an interface impl of
// this module whose signature matched its interface method's (implFlows,
// the methods implRequires judges).
func (m *Module) ifaceImplMethod(id types.ItemID) bool {
	for _, j := range m.implFlows {
		if j.it.ID == id {
			return true
		}
	}
	return false
}

// flowsString spells a signature's sources and stores for a message:
// "from self", "B(s)", "*q into p"; a per-field clause by field in place of
// its union, "from (r: a, s: B(b))".
func (c *Checker) flowsString(s *types.Signature) string {
	name := func(p types.ParamIndex) string {
		if int(p) < 1 || int(p) > len(s.Params) {
			return "?"
		}
		return c.cfg.Interner.Str(s.Params[p-1].Name)
	}
	out := ""
	add := func(t string) {
		if out != "" {
			out += ", "
		}
		out += t
	}
	if len(s.Flows.Fields) > 0 {
		add(c.fieldClauseText(s.Result, s.Flows.Fields, func(src types.Source) string {
			if src.Behind {
				return "B(" + name(src.Param) + ")"
			}
			return name(src.Param)
		}))
	} else {
		for _, src := range s.Flows.Sources {
			if src.Behind {
				add("B(" + name(src.Param) + ")")
			} else {
				add("from " + name(src.Param))
			}
		}
	}
	for _, st := range s.Flows.Stores {
		add("*" + name(st.From) + " into " + name(st.Into))
	}
	for i, p := range s.Params {
		if p.Into != types.NoParam {
			add(name(types.ParamIndex(i+1)) + " into " + name(p.Into))
		}
	}
	for _, e := range s.Flows.Ends {
		add("ends " + name(e))
	}
	if out == "" {
		return "none"
	}
	return out
}

// implCollisions is the declaration-time collision rule: an impl
// method whose folded name equals a variant or Type.Name function of C, or
// a method of another impl for C visible from this module, is E0206 at the
// impl method of this module; two such impls here: at the later. A field
// is not in that class (resolve.MemberKey): field `cpu` and method `Cpu`
// are two members.
func (m *Module) implCollisions() {
	c := m.c
	for _, id := range m.implList {
		it := c.cfg.Items.Get(id)
		m.guard(it, func() {
			d := it.Decl.(*types.ImplDecl)
			ck, _ := c.ctorOf(d.For)
			seen := map[string]types.ItemID{}
			for _, o := range m.visibleImpls(ck, id) {
				for _, mid := range c.cfg.Items.Get(o).Decl.(*types.ImplDecl).Methods {
					f := resolve.Fold(c.cfg.Interner.Str(c.cfg.Items.Get(mid).Name))
					if _, ok := seen[f]; !ok {
						seen[f] = mid
					}
				}
			}
			for _, mid := range d.Methods {
				mi := c.cfg.Items.Get(mid)
				f := resolve.MemberKey(c.cfg.Interner.Str(mi.Name), false)
				first := source.Span{}
				if sp, ok := m.ownMemberSpan(ck, f); ok {
					first = sp
				} else if o, ok := seen[f]; ok {
					first = c.cfg.Items.Get(o).Span
				} else {
					continue
				}
				m.Diags = append(m.Diags, diag.New(diag.E0206, mi.Span, "`"+c.cfg.Interner.Str(mi.Name)+
					"` is already a member of `"+c.ctorName(ck)+"`").In(diag.PhaseTypes).Loc("first", first))
				m.shadowed[mid] = true
			}
		})
	}
}

// visibleImpls are the impls for constructor ck that this module sees
// before impl self: those of the modules it imports, directly or not, in
// module order, then this module's earlier ones in item order.
func (m *Module) visibleImpls(ck ctorKey, self types.ItemID) []types.ItemID {
	c := m.c
	var out []types.ItemID
	for _, dep := range m.depList() {
		if om := c.module(dep); om != nil {
			out = append(out, om.implsFor(ck, types.NoItem)...)
		}
	}
	return append(out, m.implsFor(ck, self)...)
}

// implsFor lists this module's table impls for ck, in item order, stopping
// at stop. Once the module's table is final (m.final: implList sorted,
// shadowed written) a lookup without a stop reads the index by constructor
// (implIndexOf), which holds the same impls in the same order; before that,
// and with a stop (implCollisions, during step 3), it scans implList.
func (m *Module) implsFor(ck ctorKey, stop types.ItemID) []types.ItemID {
	if stop == types.NoItem {
		if x := m.implIndexOf(); x != nil {
			return x[ck]
		}
	}
	return m.implsForScan(ck, stop)
}

// implsForScan is implsFor by a scan of implList: the answer the index must
// match (impls_index_test.go keeps the two side by side).
func (m *Module) implsForScan(ck ctorKey, stop types.ItemID) []types.ItemID {
	var out []types.ItemID
	n := 0
	for _, id := range m.implList {
		if id == stop {
			break
		}
		n++
		if k, ok := m.c.ctorOf(m.c.cfg.Items.Get(id).Decl.(*types.ImplDecl).For); ok && k == ck {
			out = append(out, id)
		}
	}
	m.state().scans.Add(int64(n))
	return out
}

// implIndex is a module's table impls by constructor, each list in item
// order (implList's). Only impls whose For has a constructor are in it, so
// the zero ctorKey (implCollisions' key for a blanket For) finds none, as
// the scan finds none. Lists are clipped: a caller's append copies.
type implIndex map[ctorKey][]types.ItemID

// implState is the lookup state derived from one module's final tables (so
// lookups need not rescan every impl on large programs): the impl index,
// the sorted dependency list, the closure's indexes and visibleMembers'
// answers by constructor. Every part is built on first use and read-only
// after; the answers equal the scans' (impls_index_test.go).
//
// It is the Module's field impls; its zero value is ready.
type implState struct {
	mu    sync.Mutex
	index atomic.Pointer[implIndex]
	deps  atomic.Pointer[[]types.ModuleID]
	// closure is closureIndex's answer (members.go).
	closure atomic.Pointer[[]closureModule]
	// members holds visibleMembers' answers by ctorKey ([]Member, clipped).
	members sync.Map
	// scans counts the implList entries visited by scans and by building
	// the index (impls_index_test.go's counting test).
	scans atomic.Int64
}

// state is m's implState (the Module field impls).
func (m *Module) state() *implState { return &m.impls }

// implIndexOf is m's impl index, built on first use; nil while m's table is
// not final (step 3 still running), when only the scan is right.
func (m *Module) implIndexOf() implIndex {
	if !m.final {
		return nil
	}
	s := m.state()
	if x := s.index.Load(); x != nil {
		return *x
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if x := s.index.Load(); x != nil {
		return *x
	}
	x := implIndex{}
	for _, id := range m.implList {
		if k, ok := m.c.ctorOf(m.c.cfg.Items.Get(id).Decl.(*types.ImplDecl).For); ok {
			x[k] = append(x[k], id)
		}
	}
	for k, l := range x {
		x[k] = slices.Clip(l)
	}
	s.scans.Add(int64(len(m.implList)))
	s.index.Store(&x)
	return x
}

// ctorName spells a constructor for a message.
func (c *Checker) ctorName(k ctorKey) string {
	switch {
	case k.item != types.NoItem:
		return c.cfg.Interner.Str(c.cfg.Items.Get(k.item).Name)
	case k.prim != types.NoType:
		return c.cfg.Types.TypeString(k.prim, nil)
	}
	return k.kind.String()
}
