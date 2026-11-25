// Why: names in expressions and types come in shapes the parser cannot
// decide alone: a qualified name may be a module member or a value's field,
// and a bracket after a name may be generic arguments or an index.
//
// What: the walker's expression and type methods: qualified, member,
// memberMiss (and MissedIn, which tells its E0201 from others), fieldChain,
// path, the bracket rule decide with index, args and the slot walkers
// (typeSlot, slotExpr, slotAny), typAt, fnType, expr, recordField and
// quoteRoots.
//
// How: a module binding's items resolve here (E0204 for a private one,
// E0201 with a folded fallback over the module's exports); member tails are
// left to check as RefDeferred. A bracket reads by the generic kinds in the
// declaring module's record, so no type is needed. Pending names in a
// module with gen lines carry what Settle needs to finish them.

package resolve

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// holder is a node whose Ref covers the first covers segments of a chain.
type holder struct {
	node   ast.NodeID
	covers int
}

// holderRef is the Ref of a holder given the chain's resolution.
func holderRef(r Ref, covers int) Ref {
	switch {
	case r.Kind == RefPoison || r.Kind == RefPending:
		return Ref{Kind: r.Kind, Segs: uint8(covers)}
	case covers <= int(r.Segs):
		r.Segs = uint8(covers)
		return r
	}
	return Ref{Kind: RefDeferred, Segs: r.Segs, A: r.A, B: r.B}
}

// setHolders records the Refs of a chain's holders.
func (w *walker) setHolders(r Ref, hs []holder) {
	for _, h := range hs {
		w.fs.setRef(h.node, holderRef(r, h.covers))
	}
}

// qualified resolves segs[0] and, through a module binding, segs[1]. It
// sets the segment idents' Refs and returns the resolution of the longest
// resolved prefix (Segs 1 or 2). hs are the holders a pending retry must
// update.
func (w *walker) qualified(segs []*ast.Ident, p pos, hs []holder) Ref {
	root := segs[0]
	if len(segs) == 1 {
		r := w.name(root, p)
		w.patchPending(r, hs, nil)
		return r
	}
	w.setOrigin(root)
	if r, ok := w.pinned(root); ok {
		w.clearOrigin()
		w.fs.setRef(root.ID(), r)
		if r.Kind == RefModule {
			return w.member(types.ModuleID(r.A), segs[1], p, hs)
		}
		return r
	}
	h, ok := w.find(root.Name)
	w.recordReads(root.Name, h, ok)
	w.clearOrigin()
	switch {
	case ok && h.step == 4:
		w.useBinding(h.binding)
		w.fs.setRef(root.ID(), h.ref)
		return w.member(types.ModuleID(h.ref.A), segs[1], p, hs)
	case ok && h.step == 1:
		r := w.useLocal(root, h)
		w.fs.setRef(root.ID(), r)
		return r
	case ok:
		w.fs.setRef(root.ID(), h.ref)
		return h.ref
	}
	if w.m.str(root.Name) == "fmt" && w.m.str(segs[1].Name) == "Println" {
		syn, _ := synonymFor("fmt.Println")
		w.synonym(root.Span().To(segs[1].Span()), "fmt.Println", syn, root.Name)
		w.fs.setRef(root.ID(), poisonRef)
		return poisonRef
	}
	r := w.miss(root, p, ast.NoNode)
	w.fs.setRef(root.ID(), r)
	if r.Kind == RefModule {
		// On a miss the use resolves to the folded match, so a
		// misspelled module root continues into its tail.
		return w.member(types.ModuleID(r.A), segs[1], p, hs)
	}
	w.patchPending(r, hs, segs[1])
	return r
}

// patchPending gives the pending entry just recorded its holders and, for
// the root of a qualified chain, the next segment, which Settle looks up in
// the module the root may fold to.
func (w *walker) patchPending(r Ref, hs []holder, tail *ast.Ident) {
	if r.Kind != RefPending || len(w.t.pending) == 0 {
		return
	}
	pn := &w.t.pending[len(w.t.pending)-1]
	for _, h := range hs {
		pn.outer = append(pn.outer, h.node)
		pn.covers = append(pn.covers, h.covers)
	}
	if tail != nil {
		pn.tail = tail
	}
}

// patchArgs gives the pending entry of a bracket's X its arguments, so
// Settle decides their readings with the bracket rule (decide) over the Ref
// of all of X. node is the IndexExpr (NoNode for a PathType's TypeArgs); r
// is X's Ref, whose Segs is how many segments X spans. Called before the
// arguments are walked, so the pending entry is still the last one.
func (w *walker) patchArgs(r Ref, node ast.NodeID, args []ast.TypeOrExpr, pathType bool) {
	if r.Kind != RefPending || len(w.t.pending) == 0 {
		return
	}
	pn := &w.t.pending[len(w.t.pending)-1]
	pn.args, pn.argsNode, pn.argsCovers, pn.pathType = args, node, int(r.Segs), pathType
}

// member resolves X of m.X among module m's top-level items.
func (w *walker) member(mod types.ModuleID, x *ast.Ident, p pos, hs []holder) Ref {
	rec := w.m.recordOf(mod)
	if mod == types.NoModule || rec == nil {
		w.fs.setRef(x.ID(), poisonRef)
		return Ref{Kind: RefPoison, Segs: 2}
	}
	items := w.m.s.cfg.Items
	spell := w.m.str(x.Name)
	if id, ok := rec.names.Get(x.Name); ok {
		it := items.Get(id)
		w.read(types.Read{Kind: types.ReadSignature, Key: it.Key})
		if it.Exported {
			r := itemRef(id)
			w.fs.setRef(x.ID(), r)
			r.Segs = 2
			return r
		}
		d := diag.New(diag.E0204, x.Span(), "`"+spell+"` is not exported by `"+rec.path+"`").
			Text("item", spell).Loc("declared_at", it.Span).Text("module", rec.path)
		d.Alt("export `" + spell + "` from `" + rec.path + "`").Alt("use an exported function of `" + rec.path + "`")
		w.report(d, source.NoSymbol)
		w.fs.setRef(x.ID(), poisonRef)
		return Ref{Kind: RefPoison, Segs: 2}
	}
	if w.in().HasGen != nil && w.in().HasGen(mod) && !w.m.settled {
		w.t.pending = append(w.t.pending, pendingName{file: w.fs.idx, node: x.ID(), id: x, pos: p, mod: mod, item: w.rec})
		w.fs.setRef(x.ID(), Ref{Kind: RefPending, Segs: 1})
		w.patchPending(Ref{Kind: RefPending}, hs, nil)
		return Ref{Kind: RefPending, Segs: 2}
	}
	r := w.memberMiss(rec, x, p)
	w.fs.setRef(x.ID(), r)
	if r.Kind == RefItem {
		r.Segs = 2
	}
	return r
}

func (w *walker) in() *ModuleInput { return w.m.in }

// memberMiss is the miss path of m.X: the negative read, the folded
// fallback over m's exports, then E0201 with fact module.
func (w *walker) memberMiss(rec *modRecord, x *ast.Ident, p pos) Ref {
	spell := w.m.str(x.Name)
	items := w.m.s.cfg.Items
	w.read(types.Read{Kind: types.ReadNoItem, Key: types.ModuleKey(rec.path), Name: spell})
	// The folded match or the alternatives below come from m's names, and
	// so does their absence.
	w.read(types.Read{Kind: types.ReadExports, Key: types.ModuleKey(rec.path)})
	var cands []types.ItemID
	for _, id := range rec.folded[Fold(spell)] {
		if it := items.Get(id); it.Exported && (!p.isType() || isTypeKind(it.Kind)) {
			cands = append(cands, id)
		}
	}
	if len(cands) == 1 {
		it := items.Get(cands[0])
		name := w.m.str(it.Name)
		d := diag.New(diag.E0207, x.Span(), "`"+spell+"` is written `"+name+"`").Text("declared", name).Loc("declared_at", it.Span)
		d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: x.Span(), Text: name}}, Why: "names are exact; this is the declared spelling"})
		w.report(d, source.NoSymbol)
		return itemRef(cands[0])
	}
	d := diag.New(diag.E0201, x.Span(), missMessage(rec.path, spell)).Text("name", spell).Text("module", rec.path)
	for _, id := range cands {
		d.Alt("`" + w.m.str(items.Get(id).Name) + "`")
	}
	w.report(d, x.Name)
	return poisonRef
}

func missMessage(path, spell string) string { return "`" + path + "` has no item `" + spell + "`" }

// MissedIn is the module a member miss's E0201 looked its name up in (`m`
// of `m.X`), "" for any other diagnostic. The `module` fact alone does not
// tell: an unknown bare name's is the one module that exports the spelling,
// offered as an import.
func MissedIn(d *diag.Diagnostic) string {
	mod, ok := d.FactValue("module")
	name, named := d.FactValue("name")
	if d.Code != diag.E0201 || !ok || !named || d.Message != missMessage(mod.Str, name.Str) {
		return ""
	}
	return mod.Str
}

// fieldChain resolves a FieldExpr chain whose root is a name.
func (w *walker) fieldChain(fe *ast.FieldExpr, p pos) Ref {
	var segs []*ast.Ident
	var nodes []ast.NodeID
	var x ast.Expr = fe
	for {
		f, ok := x.(*ast.FieldExpr)
		if !ok {
			break
		}
		segs = append(segs, f.Name)
		nodes = append(nodes, f.ID())
		x = f.X
	}
	root, ok := x.(*ast.Ident)
	if !ok {
		if p.isType() {
			w.typeSlot(x, p)
		} else {
			w.expr(x)
		}
		// `Pair[i32].Map[u8]`: a member of a generic type applied to arguments
		// is check's to find, as `Holder.Get`'s is, so a bracket after it is
		// undecided, not an index. Only index reads this Ref.
		if ie, ok := x.(*ast.IndexExpr); ok && int(ie.ID()) < len(w.fs.fi.Readings) && w.fs.fi.Readings[ie.ID()] == ReadGeneric {
			return Ref{Kind: RefDeferred}
		}
		// A member of any other value that is no name, `F(x).M[T]` (as in
		// `Vec[i8, 16].Splat(-1).WidenLo[i16, 8]()`), is check's to find as
		// `x.M[T]` is: undecided.
		if !p.isType() {
			return Ref{Kind: RefDeferred}
		}
		return Ref{}
	}
	all := make([]*ast.Ident, 0, len(segs)+1)
	all = append(all, root)
	for i := len(segs) - 1; i >= 0; i-- {
		all = append(all, segs[i])
	}
	hs := make([]holder, len(nodes))
	for i := range nodes {
		hs[i] = holder{node: nodes[len(nodes)-1-i], covers: i + 2}
	}
	r := w.qualified(all, p, hs)
	w.setHolders(r, hs)
	return holderRef(r, len(all))
}

// path resolves a Path (in a PathType, a pattern, a RecordPat, a gen line);
// extra holders (the PathType) cover the whole path too.
func (w *walker) path(pt *ast.Path, p pos, extra ...ast.NodeID) Ref {
	if pt == nil || len(pt.Names) == 0 {
		return poisonRef
	}
	n := len(pt.Names)
	hs := []holder{{node: pt.ID(), covers: n}}
	for _, e := range extra {
		hs = append(hs, holder{node: e, covers: n})
	}
	r := w.qualified(pt.Names, p, hs)
	w.setHolders(r, hs)
	return holderRef(r, n)
}

// exprRef resolves an IndexExpr's X and returns what it named; RefNone
// means a value expression that is no name.
func (w *walker) exprRef(x ast.Expr, p pos) Ref {
	switch x := x.(type) {
	case *ast.Ident:
		return w.name(x, p)
	case *ast.FieldExpr:
		return w.fieldChain(x, p)
	case *ast.IndexExpr:
		w.index(x, p)
		return Ref{}
	}
	w.expr(x)
	return Ref{}
}

// kindsOf returns an item's generic kinds from its module's record.
func (w *walker) kindsOf(id types.ItemID) []bool {
	it := w.m.s.cfg.Items.Get(id)
	if it.Module == w.m.mod.ID {
		return w.m.rec.kinds[id]
	}
	if rec := w.m.recordOf(it.Module); rec != nil {
		return rec.kinds[id]
	}
	return nil
}

// decide is the bracket rule: what an IndexExpr or TypeArgs over a name reads.
func (w *walker) decide(r Ref) (Reading, []bool) {
	switch r.Kind {
	case RefItem:
		it := w.m.s.cfg.Items.Get(types.ItemID(r.A))
		k := w.kindsOf(it.ID)
		if isTypeKind(it.Kind) || (it.Kind == types.KindFunc && len(k) > 0) {
			return ReadGeneric, k
		}
		return ReadIndex, nil
	case RefUniverse:
		switch source.Symbol(r.A) {
		case source.SymCast, source.SymTrunc, source.SymSizeOf, source.SymAlignOf, source.SymFieldsBound:
			return ReadGeneric, nil
		}
		return ReadIndex, nil
	case RefTypeParam, RefSelfType, RefPrim:
		return ReadGeneric, nil
	case RefLocal, RefParam, RefField, RefResult, RefNone:
		return ReadIndex, nil
	}
	return ReadUndecided, nil
}

// index resolves an IndexExpr and decides its reading.
func (w *walker) index(ie *ast.IndexExpr, p pos) {
	r := w.exprRef(ie.X, p)
	w.patchArgs(r, ie.ID(), ie.Args, false)
	reading, kinds := w.decide(r)
	w.fs.setReading(ie.ID(), reading)
	w.args(ie.Args, reading, kinds)
}

// argReading is the bracket rule's reading of argument i under the bracket's
// reading: ReadingNone when the bracket is undecided (check's).
func argReading(i int, a ast.TypeOrExpr, reading Reading, kinds []bool) Reading {
	switch {
	case reading == ReadGeneric && i < len(kinds) && kinds[i]:
		return ReadExpr
	case reading == ReadGeneric && (i < len(kinds) || len(kinds) == 0 || typeShaped(a)):
		return ReadType
	case reading == ReadGeneric:
		// Past the arity and not type-shaped: check reports the arity.
		return ReadExpr
	case reading == ReadIndex:
		return ReadExpr
	}
	return ReadingNone
}

// pathTypeReading is the reading of a PathType's TypeArgs: always generic
// arguments, by the head's kinds when it has them.
func pathTypeReading(reading Reading, kinds []bool) (Reading, []bool) {
	if reading != ReadGeneric && reading != ReadUndecided {
		return ReadGeneric, nil
	}
	return reading, kinds
}

// args walks bracket arguments under a reading.
func (w *walker) args(args []ast.TypeOrExpr, reading Reading, kinds []bool) {
	for i, a := range args {
		rd := argReading(i, a, reading, kinds)
		w.fs.setReading(a.ID(), rd)
		switch rd {
		case ReadExpr:
			w.slotExpr(a)
		case ReadType:
			w.typeSlot(a, posType)
		default:
			w.slotAny(a)
		}
	}
}

// slotExpr walks a slot read as a value; a type node there is walked as a
// type (check reports the kind).
func (w *walker) slotExpr(a ast.TypeOrExpr) {
	switch a := a.(type) {
	case ast.Type:
		w.typ(a)
	case ast.Expr:
		w.expr(a)
	}
}

// slotAny walks an undecided slot: names resolve the same in either
// reading; check decides the reading.
func (w *walker) slotAny(a ast.TypeOrExpr) {
	if typeShaped(a) {
		w.typeSlot(a, posType)
		return
	}
	w.slotExpr(a)
}

// typeShaped reports whether a slot has a type reading at all.
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

// typeSlot walks a slot read as a type: an expression shape is
// reinterpreted as the type it spells; a shape with no type reading is
// E0101 "expected a type" in the names phase.
func (w *walker) typeSlot(a ast.TypeOrExpr, p pos) {
	switch a := a.(type) {
	case nil:
	case ast.Type:
		w.typAt(a, p)
	case *ast.Ident:
		w.name(a, p)
	case *ast.FieldExpr:
		w.fieldChain(a, p)
	case *ast.IndexExpr:
		w.index(a, p)
	case *ast.UnaryExpr:
		if a.Op == ast.TokAmp || a.Op == ast.TokMut || a.Op == ast.TokStar {
			w.typeSlot(a.X, posType)
			return
		}
		w.notType(a)
	case *ast.BinaryExpr:
		if a.Op == ast.TokPipe || a.Op == ast.TokBang {
			w.typeSlot(a.X, posType)
			w.typeSlot(a.Y, posType)
			return
		}
		w.notType(a)
	case *ast.ParenExpr:
		w.typeSlot(a.X, p)
	case *ast.RecordLit:
		seen := map[string]source.Span{}
		for _, f := range a.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			if f.Value == nil && len(a.Fields) == 1 {
				w.name(f.Name, p)
				continue
			}
			w.recordField(seen, f.Name)
			if f.Value == nil {
				w.name(f.Name, posType)
			} else {
				w.typeSlot(f.Value, posType)
			}
		}
	case *ast.BadExpr:
	default:
		w.notType(a)
	}
}

// notType reports a non-type shape in a type slot.
func (w *walker) notType(a ast.Node) {
	d := diag.New(diag.E0101, a.Span(), "expected a type").In(diag.PhaseNames).Text("expected", "type")
	w.t.diags = append(w.t.diags, d)
}

// recordField checks one record field label: snake_case, folded unique.
func (w *walker) recordField(seen map[string]source.Span, id *ast.Ident) {
	name := w.m.str(id.Name)
	f := Fold(name)
	if first, ok := seen[f]; ok {
		w.t.diags = append(w.t.diags, dup(id.Span(), first, name, "field"))
		return
	}
	seen[f] = id.Span()
	if !IsSnake(name) && declarable(id.Name) {
		w.t.diags = append(w.t.diags, naming(id.Span(), name, ToSnake(name), "snake_case"))
	}
}

func (w *walker) typ(t ast.Type)   { w.typAt(t, posType) }
func (w *walker) bound(t ast.Type) { w.typAt(t, posBound) }

// typAt walks a type node; root names use position p.
func (w *walker) typAt(t ast.Type, p pos) {
	switch t := t.(type) {
	case nil:
	case *ast.PathType:
		r := w.path(t.Path, p, t.ID())
		if t.Args != nil {
			w.patchArgs(r, ast.NoNode, t.Args.Args, true)
			reading, kinds := pathTypeReading(w.decide(r))
			w.args(t.Args.Args, reading, kinds)
		}
	case *ast.RefType:
		w.typ(t.Elem)
	case *ast.MutType:
		w.typ(t.Elem)
	case *ast.MutSliceType:
		w.typ(t.Elem)
	case *ast.OptionalType:
		w.typ(t.Elem)
	case *ast.RawPtrType:
		w.typ(t.Elem)
	case *ast.SliceType:
		w.typ(t.Elem)
	case *ast.ArrayType:
		w.expr(t.Len)
		w.typ(t.Elem)
	case *ast.ErrorType:
		w.typ(t.Errors)
		w.typ(t.Ok)
	case *ast.UnionType:
		for _, x := range t.Types {
			w.typ(x)
		}
	case *ast.FnType:
		w.fnType(t)
	case *ast.RecordType:
		seen := map[string]source.Span{}
		for _, f := range t.Fields {
			if f == nil {
				continue
			}
			if f.Name != nil {
				w.recordField(seen, f.Name)
			}
			w.typ(f.Type)
		}
	case *ast.ParenType:
		w.typAt(t.Type, p)
	case *ast.BadType:
	}
}

// fnType walks a fn type; its flows name its own parameters, as
// RefParam with B = NoItem and A the 1-based parameter position.
func (w *walker) fnType(t *ast.FnType) {
	seen := map[string]source.Span{}
	for _, fp := range t.Params {
		if fp == nil {
			continue
		}
		if fp.Name != nil {
			name := w.m.str(fp.Name.Name)
			if first, ok := seen[Fold(name)]; ok {
				w.t.diags = append(w.t.diags, dup(fp.Name.Span(), first, name, "parameter"))
			} else {
				seen[Fold(name)] = fp.Name.Span()
			}
		}
		w.typ(fp.Type)
	}
	w.typ(t.Result)
	bind := func(id *ast.Ident) {
		if id == nil {
			return
		}
		for i, fp := range t.Params {
			if fp != nil && fp.Name != nil && fp.Name.Name == id.Name {
				w.fs.setRef(id.ID(), Ref{Kind: RefParam, Segs: 1, A: uint32(i + 1)})
				return
			}
		}
		spell := w.m.str(id.Name)
		d := diag.New(diag.E0201, id.Span(), "`"+spell+"` is not a named parameter of this `fn` type").Text("name", spell).Text("expected", "parameter")
		w.report(d, id.Name)
		w.fs.setRef(id.ID(), poisonRef)
	}
	for _, fp := range t.Params {
		if fp != nil {
			bind(fp.Into)
		}
	}
	for _, f := range t.Flows {
		switch f := f.(type) {
		case *ast.FromFlow:
			for _, s := range f.Sources {
				if s != nil && len(s.Names) > 0 {
					bind(s.Names[0])
				}
			}
			// A per-field clause's sources; the field name is
			// check's (E0203), never a parameter.
			for _, fs := range f.Fields {
				if fs == nil {
					continue
				}
				for _, s := range fs.Sources {
					if s != nil && len(s.Names) > 0 {
						bind(s.Names[0])
					}
				}
			}
		case *ast.EndsFlow:
			bind(f.Name)
		case *ast.StoreFlow:
			bind(f.From)
			bind(f.Into)
		}
	}
}

// expr walks an expression in value position.
func (w *walker) expr(e ast.Expr) {
	switch e := e.(type) {
	case nil, *ast.BasicLit, *ast.BadExpr:
	case *ast.Ident:
		w.name(e, posValue)
	case *ast.IfStmt:
		// A value if or match: walked as the statement, so its arms
		// scope their bindings.
		w.stmt(e)
	case *ast.MatchStmt:
		w.stmt(e)
	case *ast.ElseExpr:
		w.expr(e.X)
		mark := w.mark()
		if e.Name != nil {
			w.declare(e.Name, LocalElse, false, nil)
		}
		w.block(e.Handler)
		w.release(mark)
	case *ast.BinaryExpr:
		w.expr(e.X)
		w.expr(e.Y)
	case *ast.UnaryExpr:
		w.expr(e.X)
	case *ast.FieldExpr:
		w.fieldChain(e, posValue)
	case *ast.GoExpr:
		w.expr(e.Scope)
		w.expr(e.Call)
	case *ast.IndexExpr:
		w.index(e, posValue)
	case *ast.SliceExpr:
		w.expr(e.X)
		w.expr(e.Lo)
		w.expr(e.Hi)
	case *ast.CallExpr:
		w.expr(e.Fun)
		for _, a := range e.Args {
			if a != nil {
				w.expr(a.Value)
			}
		}
	case *ast.ParenExpr:
		w.expr(e.X)
	case *ast.RecordLit:
		seen := map[string]source.Span{}
		for _, f := range e.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			name := w.m.str(f.Name.Name)
			if first, ok := seen[Fold(name)]; ok {
				w.t.diags = append(w.t.diags, dup(f.Name.Span(), first, name, "field"))
			} else {
				seen[Fold(name)] = f.Name.Span()
			}
			if f.Value == nil {
				w.name(f.Name, posValue) // (x) means (x: x)
			} else {
				w.expr(f.Value)
			}
		}
	case *ast.ArrayLit:
		for _, x := range e.Elems {
			w.expr(x)
		}
	case *ast.RepeatLit:
		w.expr(e.Value)
		w.expr(e.Count)
	case *ast.FnLit:
		w.literal(e)
	case *ast.QuoteExpr:
		w.quoteRoots(e)
		for _, s := range e.Splices {
			if s != nil {
				w.expr(s.X)
			}
		}
	}
}

// quoteRoots records the literal names of a quote, outside its splices,
// that stand first in a chain (no `.` before them) and are bound by an
// import the module's quotes see (modRecord.lit): wherever the output lands
// they resolve through that import, so the import is used by this
// module. A name the emitted code itself declares is counted too;
// that only spares an E0208.
func (w *walker) quoteRoots(q *ast.QuoteExpr) {
	lit := w.m.rec.lit
	src := w.m.sourceFile(w.fs.fi.FileID)
	toks := w.fs.fi.File.Tokens
	if len(lit) == 0 || src == nil || q.Toks.End > uint32(len(toks)) {
		return
	}
	k := 0
	prev := ast.TokInvalid
	for _, tk := range toks[q.Toks.First:q.Toks.End] {
		if tk.Kind.IsTrivia() || tk.Kind == ast.TokTerm {
			continue
		}
		for k < len(q.Splices) && q.Splices[k] != nil && tk.Start >= q.Splices[k].Span().End {
			k++
		}
		inSplice := k < len(q.Splices) && q.Splices[k] != nil && tk.Start >= q.Splices[k].Span().Start
		if tk.Kind == ast.TokIdent && !inSplice && prev != ast.TokDot && tk.End() <= src.Len() {
			if sym, ok := w.m.s.in.Lookup(src.Text[tk.Start:tk.End()]); ok {
				for _, b := range lit {
					if b.Name == sym {
						w.t.quoted = append(w.t.quoted, sym)
						break
					}
				}
			}
		}
		prev = tk.Kind
	}
}
