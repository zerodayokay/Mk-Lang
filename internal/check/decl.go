// Why: stage 5 must give every declaration its checked form (types.Decl)
// before any body is checked, in an order that lets a declaration read
// another's while catching cycles, and must queue the checks that need
// facts not yet known.
//
// What: declIndex, ensure and ensureGuarded (the memoized walk),
// declareItem and its rows (declStruct, declEnum, declError, declInterface,
// declImpl, declAlias, consts, globals, tests, gen lines), generics, bound,
// queueBounds, later and runQueue.
//
// How: each item is declared once, with an in-progress state; a named type
// met while in progress is legal (it is named, not laid out), an alias
// detects its own cycle (E0907). A panic leaves a placeholder decl so one
// ICE is not mistaken for a cycle.

package check

import (
	"math/big"
	"strconv"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// declState is an item's place in the memoized walk of step 1.
type declState uint8

const (
	declNone declState = iota
	declBusy
	declDone
)

// nodeKey names a node of the module: its file index and NodeID.
type nodeKey struct {
	file int
	node ast.NodeID
}

// declIndex is the module's stage-5 working state; it lives only until
// Signatures returns.
type declIndex struct {
	state map[types.ItemID]declState
	order map[types.ItemID]int     // item order (key order)
	at    map[nodeKey]types.ItemID // an item's declaring node
	gens  map[types.ItemID][]types.GenericParam
	reads map[types.ItemID]*types.ReadSet
	// queue holds the fact-needing checks of step 1, run in order after the
	// impls are registered.
	queue []queued
	// flows are the signatures whose flows step 5 computes.
	flows []flowJob
	// aliases is the stack of aliases being expanded (E0907), and
	// cycled the aliases already reported on a cycle.
	aliases []types.ItemID
	cycled  map[types.ItemID]bool
	// consts is the stage-5 typing state of untyped consts (consts.go).
	consts map[types.ItemID]constTyping
	// typing is the stack of untyped consts being typed and early consts
	// being checked, for E0909 on a typing cycle (consts.go typeCycle).
	typing []types.ItemID
	// constCycled are the least-key consts of the typing cycles reported.
	constCycled map[types.ItemID]bool
	// early is the early set and its checking state (early.go);
	// earlyList the set in key order.
	early     map[types.ItemID]constTyping
	earlyList []types.ItemID
}

// flowJob is one signature whose flows step 5 computes from its clauses.
type flowJob struct {
	it  *types.Item
	sig *types.Signature
	x   *tctx
	cl  *flowClauses
	// fix is where E0601's `from` fix would go; fix.ok false when none is
	// offered. Held by value: no allocation per signature.
	fix fromFix
	// lit marks a fn literal's signature: its E0601 waits for the
	// enclosing body, which may give it the expected type's sources.
	lit bool
}

func (m *Module) initDecls() {
	m.d = &declIndex{state: map[types.ItemID]declState{}, order: map[types.ItemID]int{}, at: map[nodeKey]types.ItemID{},
		gens: map[types.ItemID][]types.GenericParam{}, reads: map[types.ItemID]*types.ReadSet{}, cycled: map[types.ItemID]bool{}}
	for i, ii := range m.info.Items {
		m.d.order[ii.Item] = i
		if ii.Synth == resolve.SynthNone { // a synthesized item has its error's node
			m.d.at[nodeKey{ii.File, ii.Node}] = ii.Item
		}
	}
	if m.asserts == nil {
		m.asserts = map[types.ItemID]assertPair{}
	}
	if m.sliceImpls == nil {
		m.sliceImpls = map[types.ItemID]bool{}
	}
}

// itemAt is the item declared by a node of file fx, or NoItem.
func (m *Module) itemAt(fx *fileIndex, n ast.Node) types.ItemID {
	if n == nil {
		return types.NoItem
	}
	for i, f := range m.files {
		if f == fx {
			return m.d.at[nodeKey{i, n.ID()}]
		}
	}
	return types.NoItem
}

// queued is one fact-needing check of an item.
type queued struct {
	it *types.Item
	f  func()
}

// later queues a check of item it that needs facts.
func (m *Module) later(it *types.Item, f func()) { m.d.queue = append(m.d.queue, queued{it, f}) }

// runQueue is step 4's first half: the queued checks in queue order.
func (m *Module) runQueue() {
	for _, q := range m.d.queue {
		m.guard(q.it, q.f)
	}
}

// rec is the recorder of an item's stage-5 reads.
func (m *Module) rec(it *types.Item) recorder {
	rs := m.d.reads[it.ID]
	if rs == nil {
		rs = &types.ReadSet{}
		m.d.reads[it.ID] = rs
	}
	return recorder{rs: rs}
}

// declare is the stage-5 work of one item.
func (m *Module) declare(it *types.Item) {
	if m.c.hookItem != nil {
		m.c.hookItem(it.ID)
	}
	m.ensure(it)
}

// ensure fills it.Decl once. An item met while it is in progress is legal
// for a named type (it is named, not laid out); aliases detect their own
// cycles. A panic leaves the item done with a placeholder decl, so one ICE
// is not mistaken for a cycle by later readers.
func (m *Module) ensure(it *types.Item) {
	if it.Module != m.ID || m.d == nil || m.d.state[it.ID] != declNone {
		return
	}
	m.d.state[it.ID] = declBusy
	ok := false
	defer func() {
		m.d.state[it.ID] = declDone
		if !ok && it.Decl == nil {
			it.Decl = placeholderDecl(it)
		}
	}()
	m.declareItem(it)
	ok = true
}

// ensureGuarded is ensure for an item reached from another one: under its
// own guard, so its ICE is reported once, at it.
func (m *Module) ensureGuarded(it *types.Item) {
	if it.Module != m.ID || m.d == nil || m.d.state[it.ID] != declNone {
		return
	}
	m.guard(it, func() { m.ensure(it) })
}

func placeholderDecl(it *types.Item) types.Decl {
	switch it.Kind {
	case types.KindStruct:
		return &types.StructDecl{}
	case types.KindEnum:
		return &types.EnumDecl{}
	case types.KindError:
		return &types.ErrorDecl{Code: types.ErrorCode(it.Key)}
	case types.KindInterface:
		return &types.InterfaceDecl{}
	case types.KindImpl:
		return &types.ImplDecl{For: types.Poison}
	case types.KindAlias:
		return &types.AliasDecl{Type: types.Poison}
	case types.KindConst:
		return &types.ConstDecl{Type: types.Poison, Fuel: types.DefaultFuel}
	case types.KindGlobal:
		return &types.GlobalDecl{Type: types.Poison}
	case types.KindTest:
		return &types.TestDecl{}
	case types.KindGen:
		return &types.GenDecl{}
	}
	return &types.FuncDecl{Sig: types.Signature{Item: it.ID, Name: it.Name, Result: types.Poison}, Bodiless: true}
}

// genericsOf is an item's generic parameters: its decl's once declared, a
// skeleton from the AST (names and kinds, no bounds) while it is in
// progress (step 1).
func (c *Checker) genericsOf(it *types.Item) []types.GenericParam {
	if m := c.module(it.Module); m != nil && m.d != nil && !m.final {
		if g, ok := m.d.gens[it.ID]; ok {
			return g
		}
		m.ensureGuarded(it)
		if g, ok := m.d.gens[it.ID]; ok {
			return g
		}
		return m.skeletonGenerics(it)
	}
	return allGenerics(it.Decl)
}

// allGenerics is the generic list of any declaration.
func allGenerics(d types.Decl) []types.GenericParam {
	switch d := d.(type) {
	case *types.StructDecl:
		return d.Generics
	case *types.EnumDecl:
		return d.Generics
	case *types.InterfaceDecl:
		return d.Generics
	case *types.ImplDecl:
		return d.Generics
	case *types.AliasDecl:
		return d.Generics
	case *types.FuncDecl:
		return d.Sig.Generics
	}
	return nil
}

// typeParamsOf is the type parameter list written on a declaration node.
func typeParamsOf(n ast.Node) *ast.TypeParams {
	switch n := n.(type) {
	case *ast.StructDecl:
		return n.TypeParams
	case *ast.EnumDecl:
		return n.TypeParams
	case *ast.InterfaceDecl:
		return n.TypeParams
	case *ast.ImplDecl:
		return n.TypeParams
	case *ast.AliasDecl:
		return n.TypeParams
	}
	return nil
}

func (m *Module) skeletonGenerics(it *types.Item) []types.GenericParam {
	n, _ := m.nodeOf(it.ID)
	return skeleton(typeParamsOf(n), nil)
}

// skeleton lists type parameters by name and kind only, after prefix.
func skeleton(tps *ast.TypeParams, prefix []types.GenericParam) []types.GenericParam {
	out := append([]types.GenericParam(nil), prefix...)
	if tps == nil {
		return out
	}
	for _, tp := range tps.Params {
		if tp == nil || tp.Name == nil {
			continue
		}
		out = append(out, types.GenericParam{Const: tp.Const, Name: tp.Name.Name, Span: tp.Name.Span(), Type: types.Int})
	}
	return out
}

// generics reads a declaration's own type parameters after prefix (the
// owner's): bounds and const parameter types, with the whole list in scope.
func (x *tctx) generics(tps *ast.TypeParams, prefix []types.GenericParam) []types.GenericParam {
	gs := skeleton(tps, prefix)
	x.gens = gs
	if tps == nil {
		return gs
	}
	i := len(prefix)
	for _, tp := range tps.Params {
		if tp == nil || tp.Name == nil {
			continue
		}
		g := &gs[i]
		i++
		if tp.Const {
			g.Type = x.typeOf(tp.Type, posPlain)
			continue
		}
		for _, b := range tp.Bounds {
			if bd, ok := x.bound(b); ok {
				g.Bounds = append(g.Bounds, bd)
			}
		}
	}
	return gs
}

// noConstFnParam refuses a struct's or enum's `const` parameter of `fn`
// type, E0705 at its type: a bracket argument is never a `fn` literal, and
// a named function is no const argument of a type, so such a type could
// never be instantiated. A function's `const` `fn` parameter is
// unaffected: its argument is a hidden argument of the call, which a type
// has no call for.
func (x *tctx) noConstFnParam(tps *ast.TypeParams, gs []types.GenericParam) {
	if tps == nil {
		return
	}
	for i, tp := range tps.Params {
		if tp == nil || !tp.Const || tp.Type == nil || i >= len(gs) {
			continue
		}
		if x.m.c.cfg.Types.Kind(gs[i].Type) == types.KFn {
			x.errorf(diag.E0705, tp.Type.Span(), "a type's `const` parameter cannot be a `fn` at 1.0: take `T: Ord` or another interface bound, or make the function the generic one").Text("param", x.m.c.cfg.Interner.Str(tp.Name.Name))
		}
	}
}

// bound reads one bound or impl head: Plain, Send, Share, Fields[I], or an
// interface with its arguments.
func (x *tctx) bound(n ast.Node) (types.Bound, bool) {
	var head ast.Node = n
	var args []ast.TypeOrExpr
	switch t := n.(type) {
	case nil:
		return types.Bound{}, false
	case *ast.PathType:
		if t.Args != nil {
			args = t.Args.Args
		}
	case *ast.IndexExpr:
		head, args = t.X, t.Args
	case *ast.ParenType:
		return x.bound(t.Type)
	}
	r := x.ref(head)
	if pt, ok := head.(*ast.PathType); ok && r.Kind == resolve.RefNone {
		r = x.ref(pt.Path)
	}
	switch r.Kind {
	case resolve.RefPoison, resolve.RefNone:
		return types.Bound{}, false
	case resolve.RefUniverse:
		switch source.Symbol(r.A) {
		case source.SymPlain:
			return types.Bound{Kind: types.BoundPlain}, true
		case source.SymSend:
			return types.Bound{Kind: types.BoundSend}, true
		case source.SymShare:
			return types.Bound{Kind: types.BoundShare}, true
		case source.SymFieldsBound:
			if len(args) != 1 {
				x.errorf(diag.E0304, n.Span(), "`Fields` takes one bound").Text("reason", "type-arguments").Int("expected", 1).Int("found", int64(len(args)))
				return types.Bound{}, false
			}
			inner, ok := x.bound(args[0])
			if !ok {
				return types.Bound{}, false
			}
			if inner.Kind != types.BoundInterface {
				// Fields[Plain] and the like: no interface to name.
				return types.Bound{Kind: types.BoundFields}, true
			}
			return types.Bound{Kind: types.BoundFields, Item: inner.Item, Args: inner.Args}, true
		}
	case resolve.RefItem:
		it := x.m.c.cfg.Items.Get(types.ItemID(r.A))
		if it.Kind == types.KindInterface {
			ga, ok := x.genericArgs(n, it, args)
			if !ok {
				return types.Bound{}, false
			}
			x.rec.signature(it.Key)
			return types.Bound{Kind: types.BoundInterface, Item: it.ID, Args: ga}, true
		}
	}
	x.errorf(diag.E0301, n.Span(), "a bound must be an interface, Plain, Send, Share or Fields[I]").Text("expected", "bound").Text("found", "type")
	return types.Bound{}, false
}

// ctxFor is the type context of an item's declaration node.
func (m *Module) ctxFor(it *types.Item, fx *fileIndex) *tctx {
	return &tctx{m: m, fx: fx, item: it, rec: m.rec(it)}
}

// declareItem dispatches on the item's declaring node.
func (m *Module) declareItem(it *types.Item) {
	if m.synthOf(it.ID) {
		// Decided after the impl table is built (errorFormats); until then
		// the impl heads nothing and the method has no body.
		it.Decl = placeholderDecl(it)
		return
	}
	n, fx := m.nodeOf(it.ID)
	x := m.ctxFor(it, fx)
	switch n := n.(type) {
	case *ast.StructDecl:
		m.declStruct(it, x, n)
	case *ast.EnumDecl:
		m.declEnum(it, x, n)
	case *ast.ErrorDecl:
		m.declError(it, x, n)
	case *ast.InterfaceDecl:
		m.declInterface(it, x, n)
	case *ast.ImplDecl:
		m.declImpl(it, x, n)
	case *ast.AliasDecl:
		m.declAlias(it, x, n)
	case *ast.ConstDecl:
		d := &types.ConstDecl{Type: types.NoType, Fuel: types.DefaultFuel}
		if n.Type != nil {
			d.Type = x.typeOf(n.Type, posPlain)
		}
		d.Fuel = m.fuel(x, n.Fuel)
		d.Section = m.sectionClause(x, n.Section)
		it.Decl = d
		if d.Type != types.NoType { // an untyped const waits for its initializer
			m.later(it, func() { m.constPlain(it, d.Type) })
		}
	case *ast.GlobalDecl:
		d := &types.GlobalDecl{Type: x.typeOf(n.Type, posPlain), Extern: n.Extern}
		it.Decl = d
		if n.Extern {
			m.externVar(it, x, n, d) // never Share-checked, it is no Mk var
			return
		}
		d.Section = m.sectionClause(x, n.Section)
		m.later(it, func() { m.globalShare(it, d.Type) })
	case *ast.TestItem:
		d := &types.TestDecl{TakesSystem: n.Param != nil}
		if n.Name != nil {
			d.Name = strLit(n.Name.Raw)
		}
		it.Decl = d
	case *ast.GenItem:
		// gen wrote the line's checked declaration at stage 4 and
		// reported its codes (E0301, E0304, E0705, E0908 at gen lines):
		// keep it, Args included.
		if gd, ok := it.Decl.(*types.GenDecl); ok && gd.Generator != types.NoItem {
			return
		}
		d := &types.GenDecl{Fuel: m.fuel(x, n.Fuel)}
		for _, g := range m.info.Gens {
			if g.Item == it.ID {
				d.Generator = g.Generator
			}
		}
		it.Decl = d
	case *ast.FuncDecl, *ast.ExternDecl, *ast.Contract, *ast.InvariantMember, *ast.FnLit:
		m.declFunc(it, x, n)
	default:
		source.ICEf(it.Span, "check: item %s declared by a %T", it.Key, n)
	}
}

// fuel is a const's or gen line's fuel operand: a non-negative integer
// literal, or a named const read as a u64 const argument (E0301 or E0303
// when it is no such value), DefaultFuel when absent. Any other form is
// E0705, as on a gen line.
func (m *Module) fuel(x *tctx, e ast.Expr) uint64 {
	switch e := e.(type) {
	case nil:
		return types.DefaultFuel
	case *ast.BasicLit:
		if v, ok := parseIntLit(e.Raw); ok && e.Kind == ast.TokInt {
			return v
		}
		x.errorf(diag.E0705, e.Span(), "fuel is a non-negative integer literal or a named const").Text("param", "fuel")
	case *ast.Ident, *ast.FieldExpr:
		sub := *x
		sub.constParam = "fuel"
		if a, ok := sub.constArg(e, types.U64); ok && a.Kind == types.CAValue {
			if v, ok := m.c.constInt(a.Value); ok {
				return uint64(v)
			}
		}
	default:
		x.errorf(diag.E0705, e.Span(), "fuel is a non-negative integer literal or a named const").Text("param", "fuel")
	}
	return types.DefaultFuel
}

// strLit is the content of a plain string literal (a test name).
func strLit(raw string) string {
	if s, err := strconv.Unquote(raw); err == nil {
		return s
	}
	if len(raw) >= 2 {
		return raw[1 : len(raw)-1]
	}
	return raw
}

// declStruct is the struct row.
func (m *Module) declStruct(it *types.Item, x *tctx, n *ast.StructDecl) {
	d := &types.StructDecl{View: n.View || n.MutView || n.PlainView, MutView: n.MutView, PlainView: n.PlainView, Linear: n.Linear}
	d.Align = m.structAlign(x, n)
	if n.Packed != nil {
		d.Packed = 1
		if n.Packed.Align != nil {
			// Any n but 1, 2, 4, 8 is the parser's E0101 on a recovered tree;
			// check reads it as bare packed and adds nothing.
			if v, ok := parseIntLit(n.Packed.Align.Raw); ok && (v == 1 || v == 2 || v == 4 || v == 8) {
				d.Packed = uint32(v)
			}
		}
	}
	m.d.gens[it.ID] = skeleton(n.TypeParams, nil)
	it.Decl = d
	d.Generics = x.generics(n.TypeParams, nil)
	m.d.gens[it.ID] = d.Generics
	x.noConstFnParam(n.TypeParams, d.Generics)
	for _, mem := range n.Members {
		switch f := mem.(type) {
		case *ast.FieldMember:
			if f.Name == nil {
				continue
			}
			d.Fields = append(d.Fields, types.FieldDecl{Name: f.Name.Name, Type: x.typeOf(f.Type, posPlain), Exported: f.Export, Span: f.Name.Span()})
		case *ast.InvariantMember:
			if inv := m.itemAt(x.fx, f); inv != types.NoItem {
				d.Invariants = append(d.Invariants, inv)
			}
		}
	}
	if d.PlainView {
		m.later(it, func() { m.plainViewFields(x, d) })
	}
}

// plainViewFields enforces Invariant A on a `plain view struct`: every field
// must be Kind == Plain and Freeze == true under the struct's own generic
// bounds, because plain views are bitwise copied on use.
func (m *Module) plainViewFields(x *tctx, d *types.StructDecl) {
	env := m.c.NewEnv(d.Generics)
	for _, fd := range d.Fields {
		if fd.Type == types.Poison {
			continue
		}
		m.c.shapeReads(env, fd.Type, x.rec)
		f := m.c.FactsIn(env, fd.Type)
		if f.Kind == types.Plain && f.Freeze {
			continue
		}
		name := m.c.cfg.Interner.Str(fd.Name)
		x.errorf(diag.E0408, fd.Span, "field `"+name+"` of a `plain view struct` must be plain, freeze data (it is copied on use)").
			Text("field", name).Text("type", x.str(fd.Type)).Text("reason", "plain-view-field").
			Alt("remove `plain` to declare a unique `view struct`, or store a `&T` / `[]T` handle instead")
	}
}

// declEnum is the enum row: the tag, variants and discriminants
// (E0303 out of range, E0206 for a repeated value, at the later variant).
func (m *Module) declEnum(it *types.Item, x *tctx, n *ast.EnumDecl) {
	tab := m.c.cfg.Types
	d := &types.EnumDecl{Open: n.Open}
	m.d.gens[it.ID] = skeleton(n.TypeParams, nil)
	it.Decl = d
	d.Generics = x.generics(n.TypeParams, nil)
	m.d.gens[it.ID] = d.Generics
	x.noConstFnParam(n.TypeParams, d.Generics)
	tag := types.U32
	if n.Tag != nil {
		t := x.typeOf(n.Tag, posPlain)
		switch {
		case t == types.Poison:
		case !tab.Get(t).IsInteger():
			x.errorf(diag.E0301, n.Tag.Span(), "an enum representation must be an integer type").Text("expected", "integer type").Text("found", x.str(t))
		default:
			d.Tag, tag = t, t
		}
	}
	tt := tab.Get(tag)
	bits := uint(tt.Bits)
	if tt.Kind == types.KIntPtr {
		bits = 64
	}
	mask := ^uint64(0)
	if bits < 64 {
		mask = 1<<bits - 1
	}
	// The largest representable discriminant, as a masked value: a default
	// (the previous value plus 1) past it does not fit, E0303.
	// One report per run of overflowing defaults; an explicit
	// value starts a new run.
	maxDiscr := mask
	if tt.Signed {
		maxDiscr = mask >> 1
	}
	var next uint64
	overflow, reported := false, false
	first := map[uint64]source.Span{}
	for _, v := range n.Variants {
		if v == nil || v.Name == nil {
			continue
		}
		vd := types.VariantDecl{Name: v.Name.Name, Span: v.Name.Span(), Discr: next & mask}
		for _, f := range v.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			vd.Fields = append(vd.Fields, types.FieldDecl{Name: f.Name.Name, Type: x.typeOf(f.Type, posPlain), Exported: true, Span: f.Name.Span()})
		}
		if v.Value != nil {
			overflow, reported = false, false
			if val, ok := x.discriminant(v.Value, tag, bits, tt.Signed); ok {
				vd.Discr = val & mask
			}
		} else if overflow {
			if !reported {
				val := new(big.Int).SetUint64(maxDiscr)
				val.Add(val, big.NewInt(1))
				x.errorf(diag.E0303, v.Name.Span(), "the discriminant "+val.String()+" (the previous one plus 1) does not fit in `"+x.str(tag)+"`").
					Text("value", val.String()).Text("type", x.str(tag))
				reported = true
			}
			d.Variants = append(d.Variants, vd)
			continue
		}
		overflow = vd.Discr == maxDiscr
		if at, dup := first[vd.Discr]; dup {
			x.errorf(diag.E0206, v.Name.Span(), "two variants have the same discriminant").In(diag.PhaseTypes).
				Text("name", m.c.cfg.Interner.Str(v.Name.Name)).Loc("first", at)
		} else {
			first[vd.Discr] = v.Name.Span()
		}
		next = vd.Discr + 1
		d.Variants = append(d.Variants, vd)
	}
}

// discriminant folds an explicit `= Expr`: an integer literal of the
// tag type. A computed value is not folded here.
func (x *tctx) discriminant(e ast.Expr, tag types.TypeID, bits uint, signed bool) (uint64, bool) {
	lit, neg := e, false
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == ast.TokMinus {
		lit, neg = u.X, true
	}
	b, ok := lit.(*ast.BasicLit)
	if !ok || b.Kind != ast.TokInt {
		x.errorf(diag.E0705, e.Span(), "a discriminant must be an integer constant").Text("param", "discriminant")
		return 0, false
	}
	v, ok := parseIntLit(b.Raw)
	if !ok || !intFits(v, neg, bits, signed) {
		s := b.Raw
		if neg {
			s = "-" + s
		}
		x.errorf(diag.E0303, e.Span(), "the discriminant `"+s+"` does not fit in `"+x.str(tag)+"`").Text("value", s).Text("type", x.str(tag))
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}

// declError is the error row; the payload check needs facts and is
// queued (E0408).
func (m *Module) declError(it *types.Item, x *tctx, n *ast.ErrorDecl) {
	d := &types.ErrorDecl{Code: types.ErrorCode(it.Key)}
	it.Decl = d
	for _, f := range n.Fields {
		if f == nil || f.Name == nil {
			continue
		}
		fd := types.FieldDecl{Name: f.Name.Name, Type: x.typeOf(f.Type, posPlain), Exported: true, Span: f.Name.Span()}
		d.Fields = append(d.Fields, fd)
		at := f.Type.Span()
		m.later(it, func() { m.errorPayload(x, fd, at) })
	}
}

// errorPayload is E0408 for a payload field that is not Plain or is a
// carrier; fn values are allowed, and so is a str, which the loan checker
// holds to static loans wherever it is stored (E0605).
func (m *Module) errorPayload(x *tctx, fd types.FieldDecl, at source.Span) {
	if fd.Type == types.Poison || fd.Type == types.Str {
		return
	}
	f := m.c.FactsIn(m.c.NewEnv(nil), fd.Type)
	if f.Kind == types.Plain && !f.Carrier {
		return
	}
	x.errorf(diag.E0408, at, "an error payload must be plain data with no references but a `str`").
		Text("type", x.str(fd.Type)).Text("reason", "error-payload")
}

// declInterface is an interface: method signatures over Self.
func (m *Module) declInterface(it *types.Item, x *tctx, n *ast.InterfaceDecl) {
	d := &types.InterfaceDecl{}
	m.d.gens[it.ID] = skeleton(n.TypeParams, nil)
	it.Decl = d
	d.Generics = x.generics(n.TypeParams, nil)
	m.d.gens[it.ID] = d.Generics
	x.self = m.c.cfg.Types.SelfType()
	d.Methods = make([]types.Signature, 0, len(n.Methods))
	for _, s := range n.Methods {
		if s == nil || s.Name == nil || s.Name.Name == nil {
			continue
		}
		mx := *x
		d.Methods = append(d.Methods, mx.signature(types.NoItem, s.Name.Name.Name, s, d.Generics, false, it.Exported))
		sig := &d.Methods[len(d.Methods)-1] // cap is fixed above: the pointer stays valid
		m.d.flows = append(m.d.flows, flowJob{it: it, sig: sig, x: &mx, cl: mx.clauses(s.Flows, sig)})
	}
}

// declImpl fills an ImplDecl; coherence and signatures are judged
// after every impl is registered.
func (m *Module) declImpl(it *types.Item, x *tctx, n *ast.ImplDecl) {
	d := &types.ImplDecl{Unsafe: n.Unsafe, For: types.Poison}
	m.d.gens[it.ID] = skeleton(n.TypeParams, nil)
	it.Decl = d
	d.Generics = x.generics(n.TypeParams, nil)
	m.d.gens[it.ID] = d.Generics
	if b, ok := x.bound(n.Interface); ok {
		d.Bound = b
	} else {
		d.Bound = types.Bound{Kind: types.BoundInterface}
	}
	d.For = x.typeOf(n.For, posPlain)
	for _, fd := range n.Methods {
		if mi := m.itemAt(x.fx, fd); mi != types.NoItem {
			d.Methods = append(d.Methods, mi)
		}
	}
	m.registerImpl(it, d)
}

// registerImpl records the facts an impl changes: an unsafe
// impl Send or Share of a constructor of this module, and impl Slice[T].
func (m *Module) registerImpl(it *types.Item, d *types.ImplDecl) {
	tab := m.c.cfg.Types
	if d.For == types.Poison || tab.Kind(d.For) != types.KNamed {
		return
	}
	ctor := tab.Get(d.For).Item
	if m.c.cfg.Items.Get(ctor).Module != m.ID {
		return
	}
	sd, isStruct := m.c.cfg.Items.Get(ctor).Decl.(*types.StructDecl)
	plainView := isStruct && sd.PlainView
	switch {
	case d.Unsafe && d.Bound.Kind == types.BoundSend && !plainView:
		p := m.asserts[ctor]
		p.send = it.ID
		m.asserts[ctor] = p
	case d.Unsafe && d.Bound.Kind == types.BoundShare && !plainView:
		p := m.asserts[ctor]
		p.share = it.ID
		m.asserts[ctor] = p
	case d.Bound.Kind == types.BoundInterface && d.Bound.Item != types.NoItem && d.Bound.Item == m.c.known.Slice:
		m.sliceImpls[ctor] = true
	}
}

// declAlias is an alias: the body over the alias's parameters.
func (m *Module) declAlias(it *types.Item, x *tctx, n *ast.AliasDecl) {
	d := &types.AliasDecl{Type: types.Poison}
	gens := x.generics(n.TypeParams, nil)
	d.Generics = gens
	m.d.gens[it.ID] = gens
	m.d.aliases = append(m.d.aliases, it.ID)
	t := x.typeOf(n.Type, posOk)
	m.d.aliases = m.d.aliases[:len(m.d.aliases)-1]
	if !m.d.cycled[it.ID] {
		d.Type = t
	}
	if n.Open {
		d.Open = m.openAlias(x, n, d)
	}
	it.Decl = d
}

// aliasDecl returns an alias's declaration, expanding it first when it is
// of module cur and not declared yet. Reaching an alias that is being
// expanded is E0907 at the cycle's first alias in item order; the
// aliases of the cycle become Poison.
func (c *Checker) aliasDecl(it *types.Item, cur *Module) *types.AliasDecl {
	if it.Module == cur.ID && cur.d != nil {
		switch cur.d.state[it.ID] {
		case declNone:
			cur.ensureGuarded(it)
		case declBusy:
			cur.aliasCycle(it)
			return nil
		}
	}
	d, _ := it.Decl.(*types.AliasDecl)
	return d
}

func (m *Module) aliasCycle(it *types.Item) {
	start := -1
	for i, a := range m.d.aliases {
		if a == it.ID {
			start = i
		}
	}
	if start < 0 || m.d.cycled[it.ID] {
		return
	}
	chain := append([]types.ItemID(nil), m.d.aliases[start:]...)
	first := chain[0]
	for _, a := range chain {
		m.d.cycled[a] = true
		if m.d.order[a] < m.d.order[first] {
			first = a
		}
	}
	var names []string
	for _, a := range append(chain, chain[0]) {
		names = append(names, m.c.cfg.Interner.Str(m.c.cfg.Items.Get(a).Name))
	}
	m.Diags = append(m.Diags, diag.New(diag.E0907, m.c.cfg.Items.Get(first).Span, "this alias expands to itself").
		Fact("cycle", diag.TextList(names...)))
}

// constValueArg is a named const used as a const argument in a type:
// an imported const's value. A same-module const is an early const
// (localConstArg, early.go).
func (m *Module) constValueArg(x *tctx, it *types.Item, at source.Span) (types.ConstArg, bool) {
	if it.Module == m.ID {
		return m.localConstArg(x, it, at)
	}
	d, ok := it.Decl.(*types.ConstDecl)
	if !ok || !d.HasValue {
		return types.ConstArg{}, false
	}
	x.rec.value(it.Key)
	return types.ConstArg{Kind: types.CAValue, Value: d.Value}, true
}

// queueBounds queues the bound check of a type expression's arguments:
// it needs facts and the impl table, so it runs after step 3.
// A bound's own arguments are substituted first (`U: I[T]`).
func (m *Module) queueBounds(x *tctx, at source.Span, it *types.Item, args []types.GenericArg) {
	gens := x.gens
	rec := x.rec
	run := func() {
		ps := allGenerics(it.Decl)
		env := m.c.NewEnv(gens)
		env.view = m
		if x.unitEnv != nil {
			if ue := x.unitEnv(); ue != nil && len(ue.narrow) == len(env.narrow) {
				copy(env.narrow, ue.narrow) // the enclosing if T is X
				if len(ue.gen) == len(gens) {
					env.gen = ue.gen // a Print-shaped body's record lacks Fields[Format]
				}
			}
		}
		failed := false
		for i, p := range ps {
			if i >= len(args) || p.Const {
				continue
			}
			for _, b := range m.c.substBounds(p.Bounds, args, types.NoType) {
				if !m.c.satisfies(env, args[i].Type, b, rec) {
					failed = true
					if x.fmtMisuse != nil && x.fmtMisuse(at, args[i].Type, b) {
						continue
					}
					x.emit(m.c.boundFailure(env, at, p.Name, b, args[i].Type, gens))
				}
			}
		}
		if d := m.atomicTypeBound(env, at, it, args, gens); d != nil && !failed {
			x.emit(d) // one E0701 per argument
		}
	}
	if x.sink != nil {
		run()
		return
	}
	m.later(x.item, run)
}

// boundString spells a bound for a message.
func (c *Checker) boundString(b types.Bound, gens []types.GenericParam) string {
	switch b.Kind {
	case types.BoundPlain:
		return "Plain"
	case types.BoundSend:
		return "Send"
	case types.BoundShare:
		return "Share"
	}
	if b.Item == types.NoItem {
		return "?"
	}
	s := c.cfg.Interner.Str(c.cfg.Items.Get(b.Item).Name)
	if b.Kind == types.BoundFields {
		return "Fields[" + s + "]"
	}
	return s
}
