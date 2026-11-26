// Why: every name lookup needs the module's complete set of declarations
// first, since every file of a module sees all of it, and each entity needs
// a stable key that the cache and other modules can name it by.
//
// What: Begin and its two passes (pass1, pass2), the declare functions for
// imports, named items, members, Type.Name methods, impls, tests and
// contracts, headText (impl keys), settleOwners, and the declaration-level
// diagnostics: E0206 (folded duplicates), E0207 (b) (the case rule), E0201
// for a Type.Name owner that is no type, E0101 for a non-name owner argument.
//
// How: pass 1 declares module-level names, their members and invariants;
// pass 2 declares what needs a lookup (methods, impls, tests, gen lines,
// contract predicates) against the complete scope. In a module with gen
// lines a missing method owner is left pending until Settle.

package resolve

import (
	"slices"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// Begin declares every entity of in's files, in two passes
// over the files: module-level names first, then everything that looks a
// name up. It adds one types.Item per declaration to the session's Items,
// builds the module scope with its folded index, the per-file import
// bindings and each item's generic kinds, and reports the declaration-level
// diagnostics. It resolves no name inside a declaration body. When Begin
// returns, the module's scope is complete and is never written again except
// by this module's AddGenerated.
func (s *Session) Begin(in *ModuleInput) *Module {
	mod := s.cfg.Items.Module(in.Module)
	m := &Module{
		s: s, in: in, mod: mod,
		rec:   newRecord(mod),
		info:  &Info{Module: in.Module},
		byID:  map[types.ItemID]*itemRec{},
		ns:    map[types.ItemID]map[string]nsEntry{},
		keys:  map[types.Key]source.Span{},
		tests: map[string]source.Span{},
	}
	if in.Prelude != types.NoModule {
		m.prelude = s.preludeFor(in.Prelude)
		m.preludeGen = in.HasGen != nil && in.HasGen(in.Prelude)
	}
	for i, f := range in.Files {
		fs := &fileState{idx: i, fi: newFileInfo(f, m.fileID(i, f))}
		fs.testPrelude = in.TestPrelude != source.NoFile && fs.fi.FileID == in.TestPrelude
		m.files = append(m.files, fs)
		m.info.Files = append(m.info.Files, fs.fi)
	}
	whole := source.Span{}
	if len(m.files) > 0 {
		whole = m.files[0].fi.File.Span()
	}
	// Whether the module has gen lines is known before pass 2, so a method
	// or an impl declared before the gen line sees it too.
	for _, f := range in.Files {
		for _, it := range f.Items {
			if _, ok := it.(*ast.GenItem); ok {
				m.hasGen = true
			}
		}
	}
	if ie := source.Guard("resolve.Begin "+mod.Path, whole, func() {
		m.declareFiles(m.files)
	}); ie != nil {
		m.info.Diags = append(m.info.Diags, diag.FromInternal(ie))
	}
	m.sortModuleItems()
	m.rec.lit = unionBindings(m.files)
	m.rec.quote = quoteBindings(m.rec.lit, m.moduleSelf())
	s.register(m.rec)
	m.cacheRecords()
	return m
}

// cacheRecords fetches, once and under the session lock, the records of
// every module the files bind and of the prelude; workers then read them
// lock-free (an imported module's record pointer is fetched once).
// Records of imports are registered before the importer's
// Begin (depth order), and a record pointer never changes.
func (m *Module) cacheRecords() {
	m.imported = map[types.ModuleID]*modRecord{m.mod.ID: m.rec}
	want := []types.ModuleID{m.in.Prelude}
	for _, fs := range m.files {
		for _, b := range fs.fi.Imports {
			want = append(want, b.Module)
		}
	}
	m.s.mu.Lock()
	defer m.s.mu.Unlock()
	for _, id := range want {
		if r := m.s.recs[id]; r != nil && id != types.NoModule {
			m.imported[id] = r
		}
	}
}

// recordOf returns a module's scope record: from the cache when this
// module binds it, else from the session under its lock (a pinned item of
// generated code, an item reached through another module).
func (m *Module) recordOf(id types.ModuleID) *modRecord {
	if r, ok := m.imported[id]; ok {
		return r
	}
	return m.s.record(id)
}

// declareFiles runs both passes over files (Begin, AddGenerated).
func (m *Module) declareFiles(files []*fileState) {
	for _, fs := range files {
		m.declareImports(fs)
	}
	for _, fs := range files {
		m.pass1(fs)
	}
	for _, fs := range files {
		m.pass2(fs)
	}
}

// declareImports binds the file's implicit and written imports.
func (m *Module) declareImports(fs *fileState) {
	type seen struct {
		exact source.Symbol
		at    source.Span
		path  string // a written import's path, "" for an implicit one
	}
	byFold := map[string]seen{}
	for _, im := range m.in.Implicit {
		fs.fi.Imports = append(fs.fi.Imports, Binding{Name: im.Name, Module: im.Module, Path: m.s.cfg.Items.Module(im.Module).Path})
		byFold[Fold(m.str(im.Name))] = seen{im.Name, source.Span{}, ""}
	}
	if fs.generated {
		return
	}
	ims := ImportsOf(fs.fi.File, m.s.in)
	// An offered alias must be free of every binding of the file, a later
	// import's included, and of the aliases offered before it, so two
	// E0206 never offer one name.
	bound := map[string]bool{}
	for f := range byFold {
		bound[f] = true
	}
	for _, il := range ims {
		bound[Fold(m.str(il.Binding))] = true
	}
	taken := func(n string) bool { return bound[Fold(n)] }
	for _, il := range ims {
		name := m.str(il.Binding)
		node := findImport(fs.fi.File, il.Node)
		bindAt := il.PathSpan
		if node != nil && node.Alias != nil {
			bindAt = node.Alias.Span()
		} else if node != nil {
			bindAt = node.Path.Names[len(node.Path.Names)-1].Span()
		}
		if prev, ok := byFold[Fold(name)]; ok {
			d := dup(bindAt, prev.at, name, "import binding")
			// Offer a rename of either import.
			for _, p := range []string{prev.path, il.Path} {
				if p != "" {
					alias := importAlias(p, taken)
					bound[Fold(alias)] = true
					d.Alt("import " + alias + " = " + p)
				}
			}
			m.emit(d)
			continue
		}
		byFold[Fold(name)] = seen{il.Binding, bindAt, il.Path}
		if node != nil && node.Alias != nil && !IsSnake(name) {
			want := ToSnake(name)
			d := naming(bindAt, name, want, "snake_case")
			m.emit(d)
			m.fixups = append(m.fixups, fixup{d: d, file: fs.idx, target: Ref{Kind: RefModule, Segs: 1, A: uint32(m.in.Imports[il.Path])}, text: want, decl: bindAt, why: "recase the import alias"})
		}
		fs.fi.Imports = append(fs.fi.Imports, Binding{Name: il.Binding, Module: m.in.Imports[il.Path], Path: il.Path, Node: il.Node})
	}
}

// importAlias is a snake_case binding for an import of path that taken
// does not hold: its last two segments joined (`std.http` is std_http),
// else the last segment with the next free digit.
func importAlias(path string, taken func(string) bool) string {
	segs := strings.Split(path, ".")
	last := ToSnake(segs[len(segs)-1])
	if len(segs) > 1 {
		if c := ToSnake(segs[len(segs)-2]) + "_" + last; !taken(c) {
			return c
		}
	}
	return nextFree(last, taken)
}

func findImport(f *ast.File, id ast.NodeID) *ast.Import {
	for _, it := range f.Items {
		if im, ok := it.(*ast.Import); ok && im.ID() == id {
			return im
		}
	}
	return nil
}

// emit records a declaration diagnostic: Begin's go to Info.Diags,
// AddGenerated's to Settle's result.
func (m *Module) emit(d *diag.Diagnostic) {
	if m.adding {
		m.genDiags = append(m.genDiags, d)
		return
	}
	m.info.Diags = append(m.info.Diags, d)
}

// pass1 declares the module-level names, their members and invariants.
func (m *Module) pass1(fs *fileState) {
	for _, it := range fs.fi.File.Items {
		exp := it.Info().Export
		switch d := it.(type) {
		case *ast.StructDecl:
			r := m.declareNamed(fs, d, d.Name, types.KindStruct, exp, false, d.TypeParams)
			if r == nil {
				continue
			}
			ns := m.namespace(r.item.ID)
			inv := 0
			for _, mem := range d.Members {
				switch mm := mem.(type) {
				case *ast.FieldMember:
					m.member(fs, ns, mm.Name, "field", false)
				case *ast.InvariantMember:
					key := types.InvariantKey(r.item.Key, inv)
					inv++
					at := keywordSpan(fs.fi.File, fs.fi.FileID, mm.Span().Start, ast.TokInvariant)
					dep := m.declareItem(fs, mm, key, types.KindFunc, source.NoSymbol, false, at, r)
					if dep != nil {
						dep.gens = r.gens
					}
				}
			}
		case *ast.EnumDecl:
			r := m.declareNamed(fs, d, d.Name, types.KindEnum, exp, false, d.TypeParams)
			if r == nil {
				continue
			}
			ns := m.namespace(r.item.ID)
			for _, v := range d.Variants {
				m.member(fs, ns, v.Name, "variant", true)
				pns := map[string]nsEntry{}
				for _, f := range v.Fields {
					m.member(fs, pns, f.Name, "field", false)
				}
			}
		case *ast.ErrorDecl:
			r := m.declareNamed(fs, d, d.Name, types.KindError, exp, false, nil)
			if r == nil {
				continue
			}
			ns := m.namespace(r.item.ID)
			for _, f := range d.Fields {
				m.member(fs, ns, f.Name, "field", false)
			}
			m.declareErrorFormat(fs, d, r)
		case *ast.InterfaceDecl:
			r := m.declareNamed(fs, d, d.Name, types.KindInterface, exp, false, d.TypeParams)
			if r == nil {
				continue
			}
			ns := m.namespace(r.item.ID)
			for _, sig := range d.Methods {
				if sig != nil && sig.Name != nil {
					m.member(fs, ns, sig.Name.Name, "method", true)
				}
			}
		case *ast.AliasDecl:
			m.declareNamed(fs, d, d.Name, types.KindAlias, exp, false, d.TypeParams)
		case *ast.ConstDecl:
			m.declareNamed(fs, d, d.Name, types.KindConst, exp, false, nil)
		case *ast.GlobalDecl:
			// An extern var's name is its link symbol, used as written, as
			// an extern function's is.
			m.declareNamed(fs, d, d.Name, types.KindGlobal, exp, d.Extern, nil)
		case *ast.FuncDecl:
			if d.Sig != nil && d.Sig.Name != nil && d.Sig.Name.Owner == nil {
				m.declareNamed(fs, d, d.Sig.Name.Name, types.KindFunc, exp, false, d.Sig.TypeParams)
			}
		case *ast.ExternDecl:
			if d.Sig != nil && d.Sig.Name != nil && d.Sig.Name.Owner == nil {
				// An extern's or export extern's name is its C
				// symbol, used as written, so the case rule does not apply.
				m.declareNamed(fs, d, d.Sig.Name.Name, types.KindFunc, exp, true, d.Sig.TypeParams)
			}
		}
	}
}

// pass2 declares Type.Name methods, impls, tests, gen lines and contract
// predicates, against the complete module scope.
func (m *Module) pass2(fs *fileState) {
	for _, it := range fs.fi.File.Items {
		switch d := it.(type) {
		case *ast.FuncDecl:
			if d.Sig == nil || d.Sig.Name == nil {
				continue
			}
			var r *itemRec
			if d.Sig.Name.Owner != nil {
				r = m.declareMethod(fs, d, d.Sig, d.Info().Export)
			} else {
				r = m.byNode(fs, d)
			}
			m.declareContracts(fs, r, d.Sig)
		case *ast.ExternDecl:
			if d.Sig == nil || d.Sig.Name == nil {
				continue
			}
			var r *itemRec
			if d.Sig.Name.Owner != nil {
				r = m.declareMethod(fs, d, d.Sig, d.Info().Export)
			} else {
				r = m.byNode(fs, d)
			}
			m.declareContracts(fs, r, d.Sig)
		case *ast.ImplDecl:
			m.declareImpl(fs, d)
		case *ast.TestItem:
			m.declareTest(fs, d)
		case *ast.GenItem:
			key := types.GenKey(m.mod.Path, m.genCount)
			m.genCount++
			at := keywordSpan(fs.fi.File, fs.fi.FileID, d.Span().Start, ast.TokGen)
			if r := m.declareItem(fs, d, key, types.KindGen, source.NoSymbol, false, at, nil); r != nil {
				m.hasGen = true
				m.info.Gens = append(m.info.Gens, GenLine{Item: r.item.ID, File: fs.idx, Node: d.ID()})
			}
		}
	}
}

// byNode finds the record declared for a node in pass 1.
func (m *Module) byNode(fs *fileState, n ast.Node) *itemRec {
	r := fs.fi.Refs[n.ID()]
	if r.Kind != RefItem {
		return nil
	}
	return m.byID[types.ItemID(r.A)]
}

// namespace returns a type item's member namespace.
func (m *Module) namespace(t types.ItemID) map[string]nsEntry {
	ns := m.ns[t]
	if ns == nil {
		ns = map[string]nsEntry{}
		m.ns[t] = ns
	}
	return ns
}

// member inserts a field, variant or method name into a type namespace:
// folded duplicates within one class (fields; variants and methods) are
// E0206, a field spelled exactly as a member of the other class too, and
// the case rule is E0207 (b).
func (m *Module) member(fs *fileState, ns map[string]nsEntry, id *ast.Ident, what string, pascal bool) bool {
	if id == nil || !id.Name.IsValid() {
		return false
	}
	name := m.str(id.Name)
	field := what == "field"
	key := MemberKey(name, field)
	first, ok := ns[key]
	if !ok {
		first, ok = ns[MemberKey(name, !field)]
		ok = ok && first.name == name
	}
	if ok {
		m.emit(dup(id.Span(), first.at, name, what))
		return false
	}
	ns[key] = nsEntry{name: name, at: id.Span(), file: fs.idx}
	m.checkCase(id, name, pascal)
	return true
}

// MemberKey is a member's key in a type namespace: its folded name, with
// fields kept apart from variants and methods. A field is snake_case and a
// method PascalCase, so a field `cpu` and a method `Cpu` are two members;
// a lookup is exact, so each spelling finds its own.
func MemberKey(name string, field bool) string {
	if field {
		return "_" + Fold(name) // a folded name never starts with '_'
	}
	return Fold(name)
}

// checkCase reports E0207 (b) for a declaration (alternatives only: the
// references of a member are check's to find).
func (m *Module) checkCase(id *ast.Ident, name string, pascal bool) {
	if pascal && !IsPascal(name) {
		m.emit(naming(id.Span(), name, ToPascal(name), "PascalCase"))
	} else if !pascal && !IsSnake(name) {
		m.emit(naming(id.Span(), name, ToSnake(name), "snake_case"))
	}
}

// declareNamed declares a module-level named item. It returns nil
// when the declaration gets no item.
func (m *Module) declareNamed(fs *fileState, n ast.Node, id *ast.Ident, kind types.ItemKind, exported, caseExempt bool, tps *ast.TypeParams) *itemRec {
	if id == nil || !declarable(id.Name) {
		fs.setRef(n.ID(), poisonRef)
		return nil
	}
	name := m.str(id.Name)
	key := types.ItemKey(m.mod.Path, name)
	taken := func(c string) bool { return len(m.rec.folded[Fold(c)]) > 0 }
	// The E0206 rename starts from the PascalCase form, so the renamed
	// declaration is no E0207 either.
	base := name
	if !caseExempt && !IsPascal(name) {
		base = ToPascal(name)
	}
	if first, ok := m.rec.names.Get(id.Name); ok {
		d := dup(id.Span(), m.s.cfg.Items.Get(first).Span, name, "name")
		if exported {
			d.Alt("rename one of the two declarations")
		} else {
			d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: id.Span(), Text: nextFree(base, taken)}}, Why: "rename the later declaration"})
		}
		m.emit(d)
		fs.setRef(n.ID(), poisonRef)
		return nil
	}
	if fold := m.rec.folded[Fold(name)]; len(fold) > 0 {
		d := dup(id.Span(), m.s.cfg.Items.Get(fold[0]).Span, name, "name")
		m.emit(d)
		if exported {
			d.Alt("rename one of the two declarations")
		} else {
			text := nextFree(base, taken)
			defer func() {
				if r := m.byNode(fs, n); r != nil {
					m.fixups = append(m.fixups, fixup{d: d, file: -1, target: itemRef(r.item.ID), text: text, decl: id.Span(), why: "rename the later declaration and its uses"})
				}
			}()
		}
	}
	if !caseExempt && !IsPascal(name) {
		want := ToPascal(name)
		d := naming(id.Span(), name, want, "PascalCase")
		m.emit(d)
		if exported {
			d.Alt("recase `" + name + "` and every use in the importers")
		} else {
			defer func() {
				if r := m.byNode(fs, n); r != nil {
					m.fixups = append(m.fixups, fixup{d: d, file: -1, target: itemRef(r.item.ID), text: want, decl: id.Span(), why: "recase the declaration and its uses",
						blocked: func() bool { return m.recaseTaken(want) }, alt: "rename `" + name + "` to a free PascalCase name"})
				}
			}()
		}
	}
	r := m.declareItem(fs, n, key, kind, id.Name, exported, id.Span(), nil)
	if r == nil {
		return nil
	}
	fs.setRef(id.ID(), itemRef(r.item.ID))
	m.rec.add(m.s.in, id.Name, r.item.ID)
	r.gens = m.ownGenerics(r, nil, tps)
	m.recordKinds(r)
	return r
}

// recaseTaken reports whether the recased name of a module item is taken
// once every declaration is known (E0207 (b): alternatives when
// the recased name is taken): another module-level name folds equal
// (the recase would repeat or keep an E0206), a prelude name has it (every
// use of the prelude's would change meaning), or it is undeclarable.
func (m *Module) recaseTaken(want string) bool {
	if len(m.rec.folded[Fold(want)]) > 1 {
		return true
	}
	sym, ok := m.s.in.Lookup(want)
	if !ok {
		return false
	}
	if !declarable(sym) {
		return true
	}
	_, p := m.preludeName(sym)
	return p
}

// declareItem adds one item under key, refusing a repeated key with E0206: the
// later declaration gets no item and its node is RefPoison. dependentOf makes
// it a dependent of an item's task.
func (m *Module) declareItem(fs *fileState, n ast.Node, key types.Key, kind types.ItemKind, name source.Symbol, exported bool, at source.Span, dependentOf *itemRec) *itemRec {
	if first, ok := m.keys[key]; ok {
		m.emit(dup(at, first, keyName(key), "declaration"))
		fs.setRef(n.ID(), poisonRef)
		return nil
	}
	if firstID, ok := m.s.cfg.Items.ByKey(key); ok {
		m.emit(dup(at, m.s.cfg.Items.Get(firstID).Span, keyName(key), "declaration"))
		fs.setRef(n.ID(), poisonRef)
		return nil
	}
	m.keys[key] = at
	it := &types.Item{Module: m.mod.ID, Name: name, Kind: kind, Exported: exported, Span: at, Key: key}
	if dependentOf != nil {
		it.Owner = dependentOf.item.ID
	}
	m.s.cfg.Items.AddItem(it)
	r := &itemRec{
		info: &ItemInfo{Item: it.ID, File: fs.idx, Node: n.ID()},
		item: it, node: n, file: fs.idx,
	}
	if dependentOf != nil {
		r.parent = dependentOf
		r.info.Parent = dependentOf.item.ID
		dependentOf.deps = append(dependentOf.deps, r)
	}
	m.recs = append(m.recs, r)
	m.byID[it.ID] = r
	m.mod.Items = append(m.mod.Items, it.ID)
	fs.setRef(n.ID(), itemRef(it.ID))
	return r
}

// keyName is the last part of a key, for messages.
func keyName(k types.Key) string {
	s := k.String()
	if i := strings.LastIndexAny(s, ".:#"); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// ownGenerics lists a declaration's generics: owner's first, then its own.
func (m *Module) ownGenerics(r *itemRec, owner []genParam, tps *ast.TypeParams) []genParam {
	var gs []genParam
	for _, g := range owner {
		g.idx = len(gs)
		g.owner = true
		gs = append(gs, g)
	}
	if tps != nil {
		for _, tp := range tps.Params {
			if tp == nil || tp.Name == nil {
				continue
			}
			gs = append(gs, genParam{sym: tp.Name.Name, idx: len(gs), item: r.item.ID, at: tp.Name.Span(), konst: tp.Const})
		}
	}
	for i := range gs {
		gs[i].item = r.item.ID
	}
	return gs
}

// recordKinds writes an item's generic kinds into the module's record.
func (m *Module) recordKinds(r *itemRec) {
	if len(r.gens) == 0 {
		return
	}
	k := make([]bool, len(r.gens))
	for i, g := range r.gens {
		k[i] = g.konst
	}
	m.rec.kinds[r.item.ID] = k
}

// declareMethod declares a Type.Name method.
func (m *Module) declareMethod(fs *fileState, n ast.Node, sig *ast.Signature, exported bool) *itemRec {
	fn := sig.Name
	ownerName := m.str(fn.Owner.Name)
	name := m.str(fn.Name.Name)
	var owner types.ItemID
	prim := types.NoType
	sameModule, pending := false, false
	if id, ok := m.rec.names.Get(fn.Owner.Name); ok && isTypeKind(m.s.cfg.Items.Get(id).Kind) {
		owner, sameModule = id, true
	} else if ok {
		m.emit(diag.New(diag.E0201, fn.Owner.Span(), "`"+ownerName+"` is not a type").Text("name", ownerName).Text("expected", "type"))
	} else if id, ok := m.preludeName(fn.Owner.Name); ok && isTypeKind(m.s.cfg.Items.Get(id).Kind) {
		owner = id
	} else if t, ok := primType(fn.Owner.Name); ok {
		prim = t
	} else if (m.hasGen || m.preludeGen) && !m.adding {
		// In a module with gen lines a missing owner may be generated;
		// Settle decides it.
		pending = true
	} else {
		m.emit(diag.New(diag.E0201, fn.Owner.Span(), "unknown type `"+ownerName+"`").Text("name", ownerName).Text("expected", "type"))
	}
	switch {
	case owner != types.NoItem:
		fs.setRef(fn.Owner.ID(), itemRef(owner))
	case prim != types.NoType:
		fs.setRef(fn.Owner.ID(), Ref{Kind: RefPrim, Segs: 1, A: uint32(prim)})
	case pending:
		fs.setRef(fn.Owner.ID(), Ref{Kind: RefPending, Segs: 1})
	default:
		fs.setRef(fn.Owner.ID(), poisonRef)
	}
	if sameModule {
		m.methodMember(owner, fs, fn.Name, name)
	}
	if !IsPascal(name) {
		m.emit(naming(fn.Name.Span(), name, ToPascal(name), "PascalCase"))
	}
	key := types.MemberKey(m.mod.Path, ownerName, name)
	r := m.declareItem(fs, n, key, types.KindFunc, fn.Name.Name, exported, fn.Name.Span(), nil)
	if pending {
		m.pendOwners = append(m.pendOwners, pendOwner{fs: fs, fn: fn, rec: r})
	}
	if r == nil {
		return nil
	}
	r.item.Owner = owner
	r.item.OwnerPrim = prim
	r.info.PrimOwner = prim
	fs.setRef(fn.Name.ID(), itemRef(r.item.ID))
	if sameModule {
		m.s.addMethod(owner, r.item.ID)
	} else if prim != types.NoType && m.mod.Std {
		m.s.addPrimMethod(prim, r.item.ID)
	}
	var ownerArgs []genParam
	if fn.OwnerArgs != nil {
		for _, a := range fn.OwnerArgs.Args {
			id := bareName(a)
			if id == nil {
				m.emit(diag.New(diag.E0101, a.Span(), "an owner argument must be a type parameter name").In(diag.PhaseNames).Text("expected", "name"))
				continue
			}
			// An owner argument mirrors the owner's generic: its kind (const
			// or type) is the owner's (MinHeap[T, less].New).
			konst := false
			if k := m.ownerKinds(owner); len(ownerArgs) < len(k) {
				konst = k[len(ownerArgs)]
			}
			ownerArgs = append(ownerArgs, genParam{sym: id.Name, at: id.Span(), konst: konst, kindPending: pending})
		}
	}
	r.gens = m.ownGenerics(r, ownerArgs, sig.TypeParams)
	m.recordKinds(r)
	return r
}

// methodMember inserts a Type.Name method's name into its same-module
// owner's namespace. A folded collision with a variant or another method
// of a different exact name, or a field of the same exact name, is E0206 at
// whichever of the two comes later in (file order, offset), first the
// other.
func (m *Module) methodMember(owner types.ItemID, fs *fileState, id *ast.Ident, name string) {
	ns := m.namespace(owner)
	f := MemberKey(name, false)
	first, ok := ns[f]
	if !ok {
		if fd, has := ns[MemberKey(name, true)]; has && fd.name == name {
			first, ok = fd, true
		}
	}
	switch {
	case !ok:
		ns[f] = nsEntry{name: name, at: id.Span(), method: true, file: fs.idx}
	case first.name == name && first.method:
		// The repeated-key case, reported by declareItem.
	case m.earlier(fs.idx, id.Span(), first.file, first.at):
		m.emit(dup(first.at, id.Span(), first.name, "member"))
	default:
		m.emit(dup(id.Span(), first.at, name, "member"))
	}
}

// earlier reports whether span a of file index fa precedes span b of file
// index fb in (file order, offset).
func (m *Module) earlier(fa int, a source.Span, fb int, b source.Span) bool {
	if fa != fb {
		return fa < fb
	}
	return a.Start < b.Start
}

// pendOwner is a Type.Name method whose owner missed in a module with gen
// lines; Settle looks the owner up again.
type pendOwner struct {
	fs  *fileState
	fn  *ast.FuncName
	rec *itemRec // nil when the method got no item
}

// settleOwners decides the pending Type.Name owners against the module
// scope that now holds the generated items, then the prelude. It
// returns the tasks whose walk depends on the owner (its generic kinds), to
// be re-resolved.
func (m *Module) settleOwners() []*task {
	var again []*task
	for _, po := range m.pendOwners {
		fn, fs := po.fn, po.fs
		ownerName := m.str(fn.Owner.Name)
		owner := types.NoItem
		sameModule := false
		if id, ok := m.rec.names.Get(fn.Owner.Name); ok && isTypeKind(m.s.cfg.Items.Get(id).Kind) {
			owner, sameModule = id, true
		} else if ok {
			m.emit(diag.New(diag.E0201, fn.Owner.Span(), "`"+ownerName+"` is not a type").Text("name", ownerName).Text("expected", "type"))
		} else if id, ok := m.preludeName(fn.Owner.Name); ok && isTypeKind(m.s.cfg.Items.Get(id).Kind) {
			owner = id
		} else {
			m.emit(diag.New(diag.E0201, fn.Owner.Span(), "unknown type `"+ownerName+"`").Text("name", ownerName).Text("expected", "type"))
		}
		if owner == types.NoItem {
			fs.setRef(fn.Owner.ID(), poisonRef)
		} else {
			fs.setRef(fn.Owner.ID(), itemRef(owner))
		}
		if sameModule {
			m.methodMember(owner, fs, fn.Name, m.str(fn.Name.Name))
		}
		r := po.rec
		if r == nil {
			continue
		}
		r.item.Owner = owner
		if sameModule {
			m.s.addMethod(owner, r.item.ID)
		}
		k := m.ownerKinds(owner)
		for i := range r.gens {
			if r.gens[i].kindPending {
				r.gens[i].kindPending = false
				r.gens[i].konst = i < len(k) && k[i]
			}
		}
		m.recordKinds(r)
		if r.task != nil {
			again = append(again, r.task)
		}
	}
	m.pendOwners = nil
	return again
}

// ownerKinds returns a Type.Name owner's generic kinds.
func (m *Module) ownerKinds(owner types.ItemID) []bool {
	if owner == types.NoItem {
		return nil
	}
	it := m.s.cfg.Items.Get(owner)
	if it.Module == m.mod.ID {
		return m.rec.kinds[owner]
	}
	if rec := m.s.record(it.Module); rec != nil {
		return rec.kinds[owner]
	}
	return nil
}

// bareName returns the Ident of a bare name argument, nil otherwise.
func bareName(a ast.TypeOrExpr) *ast.Ident {
	switch a := a.(type) {
	case *ast.Ident:
		return a
	case *ast.PathType:
		if a.Args == nil && a.Path != nil && len(a.Path.Names) == 1 {
			return a.Path.Names[0]
		}
	}
	return nil
}

// preludeName looks a name up in the prelude scope.
func (m *Module) preludeName(s source.Symbol) (types.ItemID, bool) {
	if m.prelude == nil {
		return types.NoItem, false
	}
	id, ok := m.prelude.names[s]
	return id, ok
}

// declareContracts declares a function's requires and ensures predicates
// as items of their own.
func (m *Module) declareContracts(fs *fileState, r *itemRec, sig *ast.Signature) {
	if r == nil {
		return
	}
	nReq, nEns := 0, 0
	for _, c := range sig.Contracts {
		var key types.Key
		at := keywordSpan(fs.fi.File, fs.fi.FileID, c.Span().Start, ast.TokRequires)
		if c.Kind == ast.Requires {
			key = types.ContractKey(r.item.Key, "requires", nReq)
			nReq++
		} else {
			key = types.ContractKey(r.item.Key, "ensures", nEns)
			nEns++
			at = keywordSpan(fs.fi.File, fs.fi.FileID, c.Span().Start, ast.TokEnsures)
		}
		if dep := m.declareItem(fs, c, key, types.KindFunc, source.NoSymbol, false, at, r); dep != nil {
			dep.gens = r.gens
		}
	}
}

// declareImpl declares an impl and its methods.
func (m *Module) declareImpl(fs *fileState, d *ast.ImplDecl) {
	key := types.ImplKey(m.mod.Path, m.headText(fs, d.Interface, d.TypeParams), m.headText(fs, d.For, d.TypeParams))
	at := keywordSpan(fs.fi.File, fs.fi.FileID, d.Span().Start, ast.TokImpl)
	if id, ok := m.s.cfg.Items.ByKey(key); ok {
		m.info.DuplicateImpls = append(m.info.DuplicateImpls, DuplicateImpl{File: fs.idx, Node: d.ID(), First: id})
		fs.setRef(d.ID(), poisonRef)
		return
	}
	r := m.declareItem(fs, d, key, types.KindImpl, source.NoSymbol, false, at, nil)
	if r == nil {
		return
	}
	r.gens = m.ownGenerics(r, nil, d.TypeParams)
	ns := map[string]nsEntry{}
	for _, fd := range d.Methods {
		if fd == nil || fd.Sig == nil || fd.Sig.Name == nil {
			continue
		}
		id := fd.Sig.Name.Name
		name := m.str(id.Name)
		f := Fold(name)
		if first, ok := ns[f]; ok && first.name != name {
			m.emit(dup(id.Span(), first.at, name, "method"))
		} else if !ok {
			ns[f] = nsEntry{name: name, at: id.Span(), method: true, file: fs.idx}
		}
		if !IsPascal(name) {
			m.emit(naming(id.Span(), name, ToPascal(name), "PascalCase"))
		}
		dep := m.declareItem(fs, fd, types.ImplMemberKey(key, name), types.KindFunc, id.Name, false, id.Span(), r)
		if dep == nil {
			continue
		}
		fs.setRef(id.ID(), itemRef(dep.item.ID))
		dep.gens = m.ownGenerics(dep, r.gens, fd.Sig.TypeParams)
		for i := range r.gens {
			dep.gens[i].inherited = true
		}
		m.recordKinds(dep)
		m.declareContracts(fs, dep, fd.Sig)
	}
}

// headText spells an impl head for its key: the found item's key text, a
// primitive or universe name, or a constructor. In a module
// with gen lines a single name that is no impl type parameter and is not
// declared yet is spelled as this module's item, the key a generated item
// of that name has, so the key does not depend on whether the type was
// handwritten or generated.
func (m *Module) headText(fs *fileState, t ast.Type, tps *ast.TypeParams) string {
	switch t := t.(type) {
	case nil:
		return "?"
	case *ast.PathType:
		if t.Path == nil || len(t.Path.Names) == 0 {
			return "?"
		}
		names := t.Path.Names
		first := names[0].Name
		if len(names) == 1 {
			if id, ok := m.rec.names.Get(first); ok {
				return m.s.cfg.Items.Get(id).Key.String()
			}
			if id, ok := m.preludeName(first); ok {
				return m.s.cfg.Items.Get(id).Key.String()
			}
			if m.hasGen && !isTypeParam(tps, first) && declarable(first) {
				return types.ItemKey(m.mod.Path, m.str(first)).String()
			}
			return m.str(first)
		}
		bs := fs.fi.Imports
		if o, ok := fs.origins[names[0].Span().Start]; ok && o.Kind == NameLiteral {
			// A quote's qualified head is spelled through the generator's
			// bindings, its own module's name binding included, so the key
			// is the one a handwritten impl of that interface has.
			bs = m.literalBindings(o.Module)
		}
		for _, b := range bs {
			if b.Name == first && b.Module != types.NoModule {
				if rec := m.s.record(b.Module); rec != nil {
					if id, ok := rec.names.Get(names[1].Name); ok {
						return m.s.cfg.Items.Get(id).Key.String()
					}
				}
			}
		}
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = m.str(n.Name)
		}
		return strings.Join(parts, ".")
	case *ast.RefType:
		return "&"
	case *ast.MutType:
		return "mut"
	case *ast.MutSliceType:
		return "mut []"
	case *ast.SliceType:
		return "[]"
	case *ast.OptionalType:
		return "?"
	case *ast.RawPtrType:
		return "*"
	case *ast.ArrayType:
		return "[N]"
	case *ast.FnType:
		return "fn"
	case *ast.RecordType:
		return "()"
	case *ast.ParenType:
		return m.headText(fs, t.Type, tps)
	case *ast.ErrorType:
		return "!"
	case *ast.UnionType:
		return "|"
	}
	return "?"
}

// isTypeParam reports whether a name is one of the type parameters.
func isTypeParam(tps *ast.TypeParams, s source.Symbol) bool {
	if tps == nil {
		return false
	}
	for _, tp := range tps.Params {
		if tp != nil && tp.Name != nil && tp.Name.Name == s {
			return true
		}
	}
	return false
}

// declareTest declares a test block.
func (m *Module) declareTest(fs *fileState, d *ast.TestItem) {
	at := keywordSpan(fs.fi.File, fs.fi.FileID, d.Span().Start, ast.TokTest)
	name := ""
	if d.Name != nil {
		name = decodeString(d.Name.Raw)
	}
	if first, ok := m.tests[name]; ok {
		m.emit(dup(at, first, name, "test"))
		fs.setRef(d.ID(), poisonRef)
		return
	}
	m.tests[name] = at
	m.declareItem(fs, d, types.TestKey(m.mod.Path, name), types.KindTest, source.NoSymbol, false, at, nil)
}

// sortModuleItems keeps Module.Items in key order (contract).
func (m *Module) sortModuleItems() {
	items := m.s.cfg.Items
	slices.SortFunc(m.mod.Items, func(a, b types.ItemID) int { return items.Get(a).Key.Compare(items.Get(b).Key) })
	m.mod.Items = slices.Compact(m.mod.Items)
}

// decodeString decodes a "..." literal with the language's escape set.
// The parser validated it, so a malformed escape is copied as written.
func decodeString(raw string) string {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return raw
	}
	s := raw[1 : len(raw)-1]
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case '\\', '"', '\'':
			b.WriteByte(s[i])
		case 'x':
			if i+2 < len(s) {
				if v, ok := hexVal(s[i+1 : i+3]); ok {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		case 'u':
			if j := strings.IndexByte(s[i:], '}'); i+1 < len(s) && s[i+1] == '{' && j > 0 {
				if v, ok := hexVal(s[i+2 : i+j]); ok {
					b.WriteRune(rune(v))
					i += j
					continue
				}
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func hexVal(s string) (uint32, bool) {
	if s == "" || len(s) > 6 {
		return 0, false
	}
	var v uint32
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			v = v*16 + uint32(c-'0')
		case c >= 'a' && c <= 'f':
			v = v*16 + uint32(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v*16 + uint32(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}
