// Why: some same-module consts are needed as values before bodies are
// lowered (an array length, a repeat count, a generic argument, a fuel or
// format argument, a match pattern); their initializers must be checked and
// made runnable in stage 5, unless their type needs a same-module layout.
//
// What: earlySet and initEarly, checkEarly and checkEarlyOne (checking
// and handing to Config.Prepare), Deferred, constValue and localConstArg
// (a named const's value), declConstArg and sameModuleCallee (E0908).
//
// How: the early set is the consts named where a value is needed, closed
// over the consts their initializers name, by names only. A value is folded
// when possible, else requested from the evaluator at StageSignatures; from
// initEarly on, an early const is checked and Prepared on demand.

package check

import (
	"slices"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// constValue is the value of const it for a fold: its decl's value when it
// has one, else, for a const of this module, its initializer folded (and,
// on the stage-5 goroutine, written to the decl). False when
// the value is not known without the evaluator.
func (m *Module) constValue(it *types.Item, f *folder) (types.ConstID, bool) {
	if it.Module == m.ID && m.d != nil {
		m.ensureGuarded(it)
	}
	d, ok := it.Decl.(*types.ConstDecl)
	if !ok {
		return types.NoConst, false
	}
	if d.HasValue {
		f.rec.value(it.Key)
		return d.Value, true
	}
	if it.Module != m.ID || f.depth >= maxFoldDepth {
		return types.NoConst, false
	}
	n, fx := m.nodeOf(it.ID)
	cd, ok := n.(*ast.ConstDecl)
	if !ok || cd.Value == nil {
		return types.NoConst, false
	}
	// The initializer's own panics are the const's fault, reported by its
	// own evaluation, never at the use.
	sub := &folder{m: m, fx: fx, rec: f.rec, depth: f.depth + 1, write: f.write, enums: f.enums}
	v, ok, _ := sub.fold(cd.Value)
	if !ok {
		return types.NoConst, false
	}
	want := d.Type
	if want == types.NoType && cd.Type != nil {
		return types.NoConst, false // a typed const whose decl is not ready
	}
	id, ok := m.c.internFolded(v, want)
	if !ok {
		return types.NoConst, false
	}
	if f.write && m.d != nil {
		d.Value, d.HasValue = id, true
	}
	return id, true
}

// localConstArg is the const argument of a same-module const named at at:
// folded when its initializer folds, a variant of an enum with no
// fields among the operands (`const MA: Mode = Mode.A`: check
// builds the value from the layout, so nothing is evaluated and nothing of
// the module is lowered); otherwise E0908 when its type
// needs a same-module layout, else a use request at StageSignatures (the
// evaluator runs the initializer Prepare made runnable). In stage 5 before
// bodies are checked an unfoldable value is Poison; from
// initEarly on, an untyped const's typing (typeConsts) checks, Prepares and
// evaluates the early const on demand.
func (m *Module) localConstArg(x *tctx, it *types.Item, at source.Span) (types.ConstArg, bool) {
	f := &folder{m: m, fx: x.fx, rec: x.rec, write: m.d != nil, enums: true}
	if id, ok := m.constValue(it, f); ok {
		return types.ConstArg{Kind: types.CAValue, Value: id}, true
	}
	if blocker := m.localLayoutItem(it); blocker != types.NoItem {
		x.emit(m.c.stageDiag(at, []types.ItemID{it.ID, blocker}, diag.ListFact()))
		return types.ConstArg{}, false
	}
	if x.sink == nil {
		// A declaration's type in stage 5: the initializer is not checked and
		// Prepared for it; declConstArg decides it.
		return m.declConstArg(x, it, at)
	}
	if !m.checkEarlyOne(it) {
		return types.ConstArg{}, false
	}
	x.rec.value(it.Key)
	req := m.c.evalRequest(m.ID, types.EvalConst, it.ID, types.StageSignatures, at)
	v, d := m.c.evaluate(req, x.rec, faultConstUse)
	if d != nil {
		x.emit(d)
	}
	if v == types.NoConst {
		if d == nil && x.poisoned != nil {
			x.poisoned(it.ID, at) // the const's own evaluation reports why
		}
		return types.ConstArg{}, false
	}
	return types.ConstArg{Kind: types.CAValue, Value: v}, true
}

// localLayoutItem is the same-module struct or enum whose layout the
// value of const it needs (a by-value walk of its type, references
// stopping), or NoItem: such a const cannot be lowered in stage 5, so its
// StageSignatures uses are E0908.
func (m *Module) localLayoutItem(it *types.Item) types.ItemID {
	d, ok := it.Decl.(*types.ConstDecl)
	if !ok || d.Type == types.NoType {
		return types.NoItem
	}
	tab := m.c.cfg.Types
	var walk func(t types.TypeID, depth int) types.ItemID
	walk = func(t types.TypeID, depth int) types.ItemID {
		if depth > 64 {
			return types.NoItem
		}
		ty := tab.Get(t)
		switch ty.Kind {
		case types.KNamed:
			if m.c.cfg.Items.Get(ty.Item).Module == m.ID {
				return ty.Item
			}
			for _, a := range ty.Args {
				if a.Type != types.NoType {
					if r := walk(a.Type, depth+1); r != types.NoItem {
						return r
					}
				}
			}
		case types.KArray, types.KOptional, types.KErrorUnion:
			return walk(ty.Elem, depth+1)
		case types.KRecord:
			for _, f := range ty.Fields {
				if r := walk(f.Type, depth+1); r != types.NoItem {
					return r
				}
			}
		}
		return types.NoItem
	}
	return walk(d.Type, 0)
}

// earlySet is the early set: the same-module consts named where check needs
// a value (an array length, a repeat count, a generic argument bracket, a
// fuel operand, a pattern), closed over the consts their initializers name,
// in item order. It reads names only.
func (m *Module) earlySet() []types.ItemID {
	in := map[types.ItemID]bool{}
	var work []types.ItemID
	add := func(fx *fileIndex, n ast.Node) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(c ast.Node) bool {
			switch c.(type) {
			case *ast.Ident, *ast.FieldExpr:
				id := c.ID()
				if int(id) < len(fx.info.Refs) {
					if r := fx.info.Refs[id]; r.Kind == resolve.RefItem {
						it := m.c.cfg.Items.Get(types.ItemID(r.A))
						if it.Kind == types.KindConst && it.Module == m.ID && !in[it.ID] {
							in[it.ID] = true
							work = append(work, it.ID)
						}
					}
				}
			}
			return true
		})
	}
	for _, fx := range m.files {
		for _, n := range fx.early { // the walk's nodes of these kinds, in walk order
			switch n := n.(type) {
			case *ast.ArrayType:
				add(fx, n.Len)
			case *ast.RepeatLit:
				add(fx, n.Count)
			case *ast.ConstDecl:
				add(fx, n.Fuel)
			case *ast.CallExpr:
				// A Print or Arena.Fmt format argument.
				if fa := m.formatArgOf(fx, n); fa != nil {
					add(fx, fa)
				}
				// A conversion T(x) to a primitive judges a named const x by
				// its value; so does one through an alias
				// (`Byte(K)` with `type Byte = u8`).
				if id, ok := n.Fun.(*ast.Ident); ok && len(n.Args) == 1 && n.Args[0] != nil && int(id.ID()) < len(fx.info.Refs) &&
					m.convCallee(fx.info.Refs[id.ID()]) {
					// Operators over consts are judged by value too.
					if v := n.Args[0].Value; m.constShaped(fx, v) {
						add(fx, v)
					}
				}
			case *ast.NamePattern:
				// A const pattern is matched by its value (constPat).
				add(fx, n)
			case *ast.PathPattern:
				if len(n.Fields) == 0 {
					add(fx, n)
				}
			case *ast.Contract:
				// A requires clause may be compile-time: the consts it
				// names are evaluated at each call.
				if n.Kind == ast.Requires {
					add(fx, n.X)
				}
			case *ast.IndexExpr:
				if id := n.X.ID(); int(id) < len(fx.info.Refs) && fx.info.Refs[id].Kind == resolve.RefItem {
					r := fx.info.Refs[id]
					switch m.c.cfg.Items.Get(types.ItemID(r.A)).Kind {
					case types.KindFunc, types.KindStruct, types.KindEnum:
						for _, a := range n.Args {
							add(fx, a)
						}
					}
				}
			}
		}
	}
	for i := 0; i < len(work); i++ {
		n, fx := m.nodeOf(work[i])
		if cd, ok := n.(*ast.ConstDecl); ok {
			add(fx, cd.Value)
		}
	}
	var out []types.ItemID
	for _, ii := range m.info.Items {
		if in[ii.Item] {
			out = append(out, ii.Item)
		}
	}
	return out
}

// initEarly builds the early set, after runFlows and before typeConsts:
// from then on an early const is checked, Prepared and
// evaluated on demand, by an untyped const's typing as by another early
// initializer. Untyped consts typed earlier with no type are typed
// again (untypeProvisional).
func (m *Module) initEarly() {
	set := m.earlySet()
	for _, fx := range m.files {
		fx.early = nil // read once, here; the index outlives stage 5
	}
	m.d.early = make(map[types.ItemID]constTyping, len(set))
	for _, id := range set {
		m.d.early[id] = ctNone
	}
	m.d.earlyList = set
	m.untypeProvisional()
}

// checkEarly checks the early consts no demand has checked yet:
// each early initializer is checked as a body after typeConsts, in key
// order, its diagnostics joining the module's, and handed to Prepare
// unless its type needs a same-module layout.
func (m *Module) checkEarly() {
	set := m.d.earlyList
	for _, id := range set {
		m.checkEarlyOne(m.c.cfg.Items.Get(id))
	}
	// Early and Deferred are in key order whatever order demand checked them.
	pos := make(map[types.ItemID]int, len(set))
	for i, id := range set {
		pos[id] = i
	}
	slices.SortFunc(m.early, func(a, b *Body) int { return pos[a.Item] - pos[b.Item] })
	slices.SortFunc(m.deferred, func(a, b types.ItemID) int { return pos[a] - pos[b] })
}

// Deferred lists the early consts that were not handed to Prepare because
// their type needs a same-module layout, in key order: the
// driver lowers them with the stage-6 bodies.
func (m *Module) Deferred() []types.ItemID { return append([]types.ItemID(nil), m.deferred...) }

// checkEarlyOne checks one early const's initializer the first time it is
// needed: in the sweep of checkEarly, on demand when a use request needs
// its MIR first (from another early initializer, or from an untyped
// const's typing in typeConsts), or because an early const being Prepared
// names it (a use of a const being checked is a cycle: poison, E0909 is
// its own evaluation's).
func (m *Module) checkEarlyOne(it *types.Item) bool {
	id := it.ID
	if m.d == nil {
		// Stage 6: only an early const was prepared, so only it may be
		// requested; any other has no MIR before the bodies are lowered.
		return m.isEarly(id) && !slices.Contains(m.deferred, id)
	}
	if m.d.early == nil {
		return false // stage 5 before initEarly (a declaration's type): no MIR yet
	}
	st, in := m.d.early[id]
	if !in {
		return false // not early: never prepared
	}
	if st == ctDone {
		return true
	}
	if st == ctBusy {
		return false
	}
	m.d.early[id] = ctBusy
	// On the typing stack too, so a typing cycle through this initializer
	// names it (`const C = [1; N][0]`, `const N = C`: C -> N -> C).
	m.d.typing = append(m.d.typing, id)
	defer func() {
		m.d.early[id] = ctDone
		m.d.typing = m.d.typing[:len(m.d.typing)-1]
	}()
	{
		m.guard(it, func() {
			u := m.newUnit(it)
			u.run()
			b := u.b
			m.Diags = append(m.Diags, b.Diags...)
			if b.Reads.Len() > 0 {
				rs := m.d.reads[id]
				if rs == nil {
					rs = &types.ReadSet{}
					m.d.reads[id] = rs
				}
				rs.Merge(&b.Reads)
			}
			m.early = append(m.early, b)
			if m.localLayoutItem(it) != types.NoItem {
				m.deferred = append(m.deferred, id)
			} else if m.c.cfg.Prepare != nil {
				m.c.cfg.Prepare(b)
			}
		})
	}
	// The early consts its initializer names are Prepared too before the
	// evaluation that may run it: since typeConsts runs after initEarly, a
	// use request can come before the sweep reached them (`const N = J +
	// math.OnesCount64(7)`, `const J = 1` after it).
	if n, fx := m.nodeOf(id); n != nil {
		if cd, ok := n.(*ast.ConstDecl); ok && cd.Value != nil {
			ast.Inspect(cd.Value, func(c ast.Node) bool {
				if c == nil {
					return false
				}
				if cid := c.ID(); int(cid) < len(fx.info.Refs) {
					if r := fx.info.Refs[cid]; r.Kind == resolve.RefItem {
						if ref := m.c.cfg.Items.Get(types.ItemID(r.A)); ref.Kind == types.KindConst && ref.Module == m.ID {
							m.checkEarlyOne(ref)
						}
					}
				}
				return true
			})
		}
	}
	return true
}

// convCallee reports whether a callee's ref names a conversion target that
// judges a constant argument by value: a primitive, or an alias
// (names only: an alias of a struct over-includes, which only evaluates a
// const earlier).
func (m *Module) convCallee(r resolve.Ref) bool {
	switch r.Kind {
	case resolve.RefPrim:
		return true
	case resolve.RefItem:
		return m.c.cfg.Items.Get(types.ItemID(r.A)).Kind == types.KindAlias
	}
	return false
}

// formatArgOf is the format argument of a call of Print or of a method
// named Fmt (Arena.Fmt; names only, so a same-named method over-includes),
// or nil.
func (m *Module) formatArgOf(fx *fileIndex, e *ast.CallExpr) ast.Expr {
	k := m.c.known
	var n int
	switch f := e.Fun.(type) {
	case *ast.Ident:
		if id := f.ID(); k.Print == types.NoItem || int(id) >= len(fx.info.Refs) || fx.info.Refs[id].Kind != resolve.RefItem || types.ItemID(fx.info.Refs[id].A) != k.Print {
			return nil
		}
		n = 3
	case *ast.FieldExpr:
		if k.ArenaFmt == types.NoItem || m.c.cfg.Interner.Str(f.Name.Name) != "Fmt" {
			return nil
		}
		n = 2
	default:
		return nil
	}
	if len(e.Args) != n || e.Args[n-2] == nil {
		return nil
	}
	return e.Args[n-2].Value
}

// declConstArg is the value of a same-module const named in a declaration's
// type in stage 5 whose initializer does not fold: never a silent Poison
// (a false accept). When the initializer
// reaches a same-module function (directly or through same-module consts),
// the use is E0908 at the type, items [the const, the function]: a
// same-module const may call same-module functions, but it cannot then
// appear in a type. Otherwise its initializer is read as a
// computed const argument (operators over values, a call of an imported
// function: EvalCall at StageSignatures), whose failures are reported
// where they arise.
func (m *Module) declConstArg(x *tctx, it *types.Item, at source.Span) (types.ConstArg, bool) {
	if fn := m.sameModuleCallee(it, map[types.ItemID]bool{}); fn != types.NoItem {
		x.rec.value(it.Key)
		x.emit(m.c.stageDiag(at, []types.ItemID{it.ID, fn}, diag.ListFact()))
		return types.ConstArg{}, false
	}
	if ty := m.sameModuleLayoutRead(it, map[types.ItemID]bool{}); ty != types.NoItem {
		x.rec.value(it.Key)
		d := m.c.stageDiag(at, []types.ItemID{it.ID, ty}, diag.ListFact())
		if call := m.bareSizeOf(it); call != "" {
			// The type itself takes `SizeOf[T]()` of a same-module T.
			d.Alt("write `" + call + "` in the type itself, not through a const")
		}
		x.emit(d)
		return types.ConstArg{}, false
	}
	n, fx := m.nodeOf(it.ID)
	cd, ok := n.(*ast.ConstDecl)
	if !ok || cd.Value == nil {
		return types.ConstArg{}, false
	}
	want := m.typeConst(it)
	if want == types.NoType || want == types.Poison {
		return types.ConstArg{}, false // the const's own check reports its type
	}
	x.rec.value(it.Key)
	sub := *x
	sub.fx, sub.item, sub.gens, sub.constParam = fx, it, nil, ""
	return sub.computedArg(cd.Value, want)
}

// sameModuleCallee is the first same-module function item the initializer
// of same-module const it calls or names, directly or through the
// initializers of the same-module consts it names, in source order; NoItem
// when there is none.
func (m *Module) sameModuleCallee(it *types.Item, seen map[types.ItemID]bool) types.ItemID {
	if seen[it.ID] || it.Module != m.ID {
		return types.NoItem
	}
	seen[it.ID] = true
	n, fx := m.nodeOf(it.ID)
	cd, ok := n.(*ast.ConstDecl)
	if !ok || cd.Value == nil {
		return types.NoItem
	}
	found := types.NoItem
	ast.Inspect(cd.Value, func(n ast.Node) bool {
		if found != types.NoItem || n == nil {
			return false
		}
		id := n.ID()
		if int(id) >= len(fx.info.Refs) {
			return true
		}
		r := fx.info.Refs[id]
		if r.Kind != resolve.RefItem {
			return true
		}
		ref := m.c.cfg.Items.Get(types.ItemID(r.A))
		if ref.Module != m.ID {
			return true
		}
		switch ref.Kind {
		case types.KindFunc:
			found = ref.ID
		case types.KindConst:
			found = m.sameModuleCallee(ref, seen)
		}
		return found == types.NoItem
	})
	return found
}

// sameModuleLayoutRead is the first same-module struct, enum or error whose
// layout a `SizeOf` or `AlignOf` in the initializer of same-module const it
// reads, directly, through a same-module alias, or through the
// initializers of the same-module consts it names; NoItem when there is
// none. A declaration's type in stage 5 cannot read that layout yet, so
// such a const in a type is E0908.
func (m *Module) sameModuleLayoutRead(it *types.Item, seen map[types.ItemID]bool) types.ItemID {
	if seen[it.ID] || it.Module != m.ID {
		return types.NoItem
	}
	seen[it.ID] = true
	n, fx := m.nodeOf(it.ID)
	cd, ok := n.(*ast.ConstDecl)
	if !ok || cd.Value == nil {
		return types.NoItem
	}
	found := types.NoItem
	ast.Inspect(cd.Value, func(n ast.Node) bool {
		if found != types.NoItem || n == nil {
			return false
		}
		if ix, ok := n.(*ast.IndexExpr); ok {
			if r, ok := refAt(fx, ix.X); ok && r.Kind == resolve.RefUniverse &&
				(source.Symbol(r.A) == source.SymSizeOf || source.Symbol(r.A) == source.SymAlignOf) {
				for _, a := range ix.Args {
					if found == types.NoItem {
						found = m.localTypeNamed(fx, a, seen)
					}
				}
				return false
			}
		}
		if r, ok := refAt(fx, n); ok && r.Kind == resolve.RefItem {
			if ref := m.c.cfg.Items.Get(types.ItemID(r.A)); ref.Kind == types.KindConst && ref.Module == m.ID {
				found = m.sameModuleLayoutRead(ref, seen)
			}
		}
		return found == types.NoItem
	})
	return found
}

// localTypeNamed is the first same-module struct, enum or error that type
// expression n of file fx names, looking through same-module aliases; NoItem
// when there is none.
func (m *Module) localTypeNamed(fx *fileIndex, n ast.Node, seen map[types.ItemID]bool) types.ItemID {
	found := types.NoItem
	ast.Inspect(n, func(c ast.Node) bool {
		if found != types.NoItem || c == nil {
			return false
		}
		r, ok := refAt(fx, c)
		if !ok || r.Kind != resolve.RefItem {
			return true
		}
		ref := m.c.cfg.Items.Get(types.ItemID(r.A))
		if ref.Module != m.ID {
			return true
		}
		switch ref.Kind {
		case types.KindStruct, types.KindEnum, types.KindError:
			found = ref.ID
		case types.KindAlias:
			if !seen[ref.ID] {
				seen[ref.ID] = true
				if an, afx := m.nodeOf(ref.ID); an != nil {
					found = m.localTypeNamed(afx, an, seen)
				}
			}
		}
		return found == types.NoItem
	})
	return found
}

// bareSizeOf is the initializer of same-module const it as written when it
// is exactly `SizeOf[T]()` with T one name, "" otherwise.
func (m *Module) bareSizeOf(it *types.Item) string {
	n, fx := m.nodeOf(it.ID)
	cd, ok := n.(*ast.ConstDecl)
	if !ok || cd.Value == nil {
		return ""
	}
	ce, ok := cd.Value.(*ast.CallExpr)
	if !ok || len(ce.Args) != 0 {
		return ""
	}
	ix, ok := ce.Fun.(*ast.IndexExpr)
	if !ok || len(ix.Args) != 1 || ix.Args[0] == nil {
		return ""
	}
	if r, ok := refAt(fx, ix.X); !ok || r.Kind != resolve.RefUniverse || source.Symbol(r.A) != source.SymSizeOf {
		return ""
	}
	id, ok := ix.Args[0].(*ast.Ident)
	if !ok {
		return ""
	}
	return "SizeOf[" + m.c.cfg.Interner.Str(id.Name) + "]()"
}

// refAt is the resolved ref of node n of file fx; false when n has none.
func refAt(fx *fileIndex, n ast.Node) (resolve.Ref, bool) {
	if id := n.ID(); int(id) < len(fx.info.Refs) {
		return fx.info.Refs[id], true
	}
	return resolve.Ref{}, false
}
