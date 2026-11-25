// Why: gen lines add items to a module after Finish, and those items can
// answer names that missed before; generated code also resolves its names
// by where each token came from, not where the output lands.
//
// What: AddGenerated (declare and resolve generated files by their
// NameOrigins), the quote binding helpers (unionBindings, unionOf, takenIn,
// selfBinding, literalBindings, quoteBindings), and Settle with retry,
// retryName and retryArgs.
//
// How: a literal quote token resolves through its generator module's import
// bindings, then that module itself by the last segment of its path.
// Settle re-resolves the handwritten items a generated name now answers,
// retries every pending name, and reports what still misses.

package resolve

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// unionBindings is the union of the files' import bindings, a name bound
// to two different modules left out.
func unionBindings(files []*fileState) []Binding {
	return unionOf(fileImports(files))
}

// fileImports lists each file's import bindings, implicit ones included.
func fileImports(files []*fileState) [][]Binding {
	out := make([][]Binding, len(files))
	for i, fs := range files {
		out[i] = fs.fi.Imports
	}
	return out
}

// unionOf is unionBindings over per-file binding lists, in file order then
// source order. Begin and Session.LoadWith both build modRecord.lit with it,
// so a cache-loaded module's quotes see what a whole load's see.
func unionOf(files [][]Binding) []Binding {
	var out []Binding
	bad := map[source.Symbol]bool{}
	for _, bs := range files {
		for _, b := range bs {
			found := false
			for _, o := range out {
				if o.Name == b.Name {
					found = true
					if o.Module != b.Module {
						bad[b.Name] = true
					}
				}
			}
			if !found {
				out = append(out, Binding{Name: b.Name, Module: b.Module, Path: b.Path})
			}
		}
	}
	kept := out[:0]
	for _, b := range out {
		if !bad[b.Name] {
			kept = append(kept, b)
		}
	}
	return kept
}

// takenIn reports whether a binding of any file takes sym, a name unionOf
// left out as bound to two modules included: that name is still taken by
// an import.
func takenIn(files [][]Binding) func(source.Symbol) bool {
	return func(sym source.Symbol) bool {
		for _, bs := range files {
			for _, b := range bs {
				if b.Name == sym {
					return true
				}
			}
		}
		return false
	}
}

// selfBinding is the binding by which a quote of a generator in a module with
// this path names the module itself: the last segment of the path, as an
// importer names it, unless taken(name), when one of the module's imports
// takes that name. A name with no Module (an unknown import) still takes it.
func selfBinding(in *source.Interner, id types.ModuleID, path string, taken func(source.Symbol) bool) Binding {
	last := path[strings.LastIndexByte(path, '.')+1:]
	if last == "" {
		return Binding{}
	}
	sym := in.Intern(last)
	if taken(sym) {
		return Binding{}
	}
	return Binding{Name: sym, Module: id, Path: path}
}

// moduleSelf is selfBinding over the import bindings of every file of the
// module, implicit ones included, a name unionBindings left out as bound
// to two modules included: that name is still taken by an import.
func (m *Module) moduleSelf() Binding {
	return selfBinding(m.s.in, m.mod.ID, m.mod.Path, takenIn(fileImports(m.files)))
}

// quoteBindings is lit followed by self when self has a name: after the
// imports, so an import that takes the name would win (it never shares it:
// selfBinding is empty then).
func quoteBindings(lit []Binding, self Binding) []Binding {
	if !self.Name.IsValid() {
		return lit
	}
	return append(lit[:len(lit):len(lit)], self)
}

// literalBindings are the bindings a NameLiteral token of a generator in
// module mod sees: the union of the module's import bindings, then the
// module itself by the last segment of its path. A record Load
// made has none to give: that is an ICE, never other bindings.
func (m *Module) literalBindings(mod types.ModuleID) []Binding {
	if rec := m.recordOf(mod); rec != nil {
		if rec.noLines {
			source.ICEf(source.Span{}, "resolve: a quote of a generator of %s, loaded by Session.Load with no import lines; load it with LoadWith", rec.path)
		}
		return rec.quote
	}
	return nil
}

// AddGenerated declares the items of generated files of this module and
// resolves them by their NameOrigins. Valid only after Finish.
func (m *Module) AddGenerated(files []GeneratedFile) {
	if !m.finished || m.settled {
		source.ICEf(source.Span{}, "resolve: AddGenerated on %s outside Finish..Settle", m.mod.Path)
	}
	var fss []*fileState
	for _, gf := range files {
		if gf.File == nil {
			continue
		}
		fs := &fileState{idx: len(m.files), fi: newFileInfo(gf.File, gf.File.Span().File), generated: true}
		fs.fi.Generated = true
		fs.origins = map[uint32]NameOrigin{}
		for _, o := range gf.Names {
			fs.origins[o.Offset] = o
		}
		m.files = append(m.files, fs)
		m.info.Files = append(m.info.Files, fs.fi)
		fss = append(fss, fs)
	}
	nRecs := len(m.recs)
	nNames := m.rec.names.Len()
	m.adding = true
	if ie := source.Guard("resolve.AddGenerated "+m.mod.Path, source.Span{}, func() {
		m.declareFiles(fss)
	}); ie != nil {
		m.genDiags = append(m.genDiags, diag.FromInternal(ie))
	}
	m.adding = false
	for i, gf := range files {
		if i >= len(fss) {
			break
		}
		for _, o := range gf.Origins {
			if r := m.byNode(fss[i], nodeByID(gf.File, o.Node)); r != nil {
				org := o.Origin
				r.item.Origin = &org
			}
		}
	}
	for i := nNames; i < m.rec.names.Len(); i++ {
		m.genNames = append(m.genNames, m.rec.names.KeyAt(i))
	}
	tasks := m.newTasks(m.recs[nRecs:])
	m.run(tasks, 1)
	for _, t := range tasks {
		sortedTaskDiags(t.diags)
		m.genDiags = append(m.genDiags, t.diags...)
		t.diags = nil
	}
	m.merge(tasks)
	m.tasks = append(m.tasks, tasks...)
	m.applyFixups()
	m.sortModuleItems()
	m.sortInfoItems()
}

// nodeByID finds a top-level item node by ID.
func nodeByID(f *ast.File, id ast.NodeID) ast.Node {
	for _, it := range f.Items {
		if it.ID() == id {
			return it
		}
	}
	return &ast.BadItem{Base: ast.Base{NID: id}}
}

// Settle ends the module's resolution and generation: it re-resolves the
// handwritten items whose lookups a generated module-level name now answers,
// retries every pending name against the scopes that now hold the generated
// items and the settled imports, reports the names still missing (and a
// missing Main), then sorts Module.Items by key again. It returns the new
// diagnostics, split by whether the anchor lies in generated output (the
// driver wraps those in E0905). The driver calls it once for every module,
// after Finish and after the module's AddGenerated calls, in depth order.
// After it, no RefPending is left in Info.
func (m *Module) Settle() (diags, generated []*diag.Diagnostic) {
	if !m.finished || m.settled {
		source.ICEf(source.Span{}, "resolve: Settle on %s before Finish or twice", m.mod.Path)
	}
	m.settled = true
	// The prelude module settled first (depth order) and may have rebuilt
	// its scope with generated names.
	if m.in.Prelude != types.NoModule {
		m.prelude = m.s.preludeFor(m.in.Prelude)
	}
	m.adding = true
	owners := m.settleOwners()
	m.adding = false
	out := append([]*diag.Diagnostic(nil), m.genDiags...)
	m.genDiags = nil
	rerun := map[*task]bool{}
	for _, t := range owners {
		rerun[t] = true
	}
	for _, t := range m.tasks {
		if m.files[t.file].generated {
			continue
		}
		for _, n := range m.genNames {
			if t.pastModule[n] {
				rerun[t] = true
				break
			}
		}
		for _, p := range t.casePend {
			_, inModule := m.rec.names.Get(p.sym)
			_, inPrelude := m.preludeName(p.sym)
			if inModule || inPrelude {
				rerun[t] = true
				break
			}
		}
	}
	var again []*task
	for _, t := range m.tasks {
		if rerun[t] {
			again = append(again, t)
		}
	}
	for _, t := range again {
		old := map[diagKey]bool{}
		for _, d := range t.diags {
			old[keyOf(d)] = true
		}
		m.runTask(t)
		for _, d := range t.diags {
			if !old[keyOf(d)] {
				out = append(out, d)
			}
		}
		m.mergeUsed(t)
		m.adopt(t)
		m.fixups = append(m.fixups, t.fixups...)
	}
	causes := map[causeKey]source.Span{}
	var retried, habits []*diag.Diagnostic
	for _, t := range m.tasks {
		if rerun[t] || t.failed {
			continue
		}
		for _, p := range t.casePend {
			out = append(out, p.d)
		}
		t.casePend = nil
		pend := t.pending
		t.pending = nil
		for _, p := range pend {
			ds, hs := m.retry(t, p, causes)
			retried = append(retried, ds...)
			habits = append(habits, hs...)
		}
		m.mergeUsed(t)
	}
	out = append(out, m.mergeHabits(retried, habits)...)
	m.applyFixups()
	out = append(out, m.importE0208()...)
	if d := m.missingMain(); d != nil {
		out = append(out, d)
	}
	m.sortModuleItems()
	m.sortInfoItems()
	if len(m.genNames) > 0 {
		m.s.rebuildPrelude(m.rec)
	}
	for _, d := range out {
		if m.inGenerated(d.At) {
			generated = append(generated, d)
		} else {
			diags = append(diags, d)
		}
	}
	return diags, generated
}

type diagKey struct {
	code diag.Code
	at   source.Span
	msg  string
}

func keyOf(d *diag.Diagnostic) diagKey { return diagKey{d.Code, d.At, d.Message} }

// inGenerated reports whether a span lies in a generated file.
func (m *Module) inGenerated(s source.Span) bool {
	for _, fs := range m.files {
		if fs.generated && fs.fi.FileID == s.File {
			return true
		}
	}
	return false
}

// retry resolves one pending name with the full lookup rule
// against the completed scopes, and returns its diagnostics and which of
// them are habits (merged per line by Settle).
func (m *Module) retry(t *task, p pendingName, causes map[causeKey]source.Span) (diags, habits []*diag.Diagnostic) {
	fs := m.files[p.file]
	scratch := &task{root: t.root, file: t.file, used: make([]bool, len(fs.fi.Imports)), pastModule: map[source.Symbol]bool{}}
	w := newWalker(m, scratch, fs)
	w.rec = p.item
	w.causes = causes
	var r Ref
	ie := source.Guard("resolve.Settle "+p.item.item.Key.String(), identSpan(p.id), func() {
		w.setOrigin(p.id)
		if p.mod != types.NoModule {
			r = w.member(p.mod, p.id, p.pos, nil)
		} else {
			r = w.retryName(p)
		}
		w.clearOrigin()
		if r.Kind == RefModule && p.tail != nil {
			// A root that folds to a module binding continues into its
			// tail, as in Finish.
			r = w.member(types.ModuleID(r.A), p.tail, p.pos, nil)
		}
		if p.node != p.id.ID() {
			fs.setRef(p.node, r)
		}
		for i, n := range p.outer {
			fs.setRef(n, holderRef(r, p.covers[i]))
		}
		if p.args != nil {
			w.retryArgs(p, r)
		}
	})
	if ie != nil {
		scratch.diags = append(scratch.diags, diag.FromInternal(ie))
		fs.setRef(p.id.ID(), poisonRef)
	}
	m.mergeUsed(scratch)
	m.fixups = append(m.fixups, scratch.fixups...)
	return scratch.diags, scratch.habits
}

// retryName looks a pending bare name up past the scopes that cannot have
// changed (locals, type parameters, import bindings, the universe): the
// module scope, which now holds the generated items, then the prelude,
// which the prelude module's Settle may have rebuilt, then the rest of
// the miss path.
func (w *walker) retryName(p pendingName) Ref {
	m, fs := w.m, w.fs
	var r Ref
	if id, ok := m.rec.names.Get(p.id.Name); ok && !w.literalName() {
		r = itemRef(id)
		w.read(types.Read{Kind: types.ReadSignature, Key: m.s.cfg.Items.Get(id).Key})
	} else if id, ok := m.preludeName(p.id.Name); ok {
		r = itemRef(id)
		w.read(types.Read{Kind: types.ReadNoItem, Key: types.ModuleKey(m.mod.Path), Name: m.str(p.id.Name)})
		w.read(types.Read{Kind: types.ReadSignature, Key: m.s.cfg.Items.Get(id).Key})
	} else {
		w.read(types.Read{Kind: types.ReadNoItem, Key: types.ModuleKey(m.mod.Path), Name: m.str(p.id.Name)})
		r = w.missWith(p.id, p.pos, p.folded)
	}
	fs.setRef(p.id.ID(), r)
	return r
}

// retryArgs decides the readings of the bracket a pending name heads, by
// the bracket rule (decide) over the Ref of the bracket's whole X, as Finish
// would have. The arguments' names were resolved in Finish; an argument
// that now reads as a type but has no type shape is the E0101 typeSlot
// reports.
func (w *walker) retryArgs(p pendingName, r Ref) {
	reading, kinds := w.decide(holderRef(r, p.argsCovers))
	if p.pathType {
		reading, kinds = pathTypeReading(reading, kinds)
	} else {
		w.fs.setReading(p.argsNode, reading)
	}
	for i, a := range p.args {
		rd := argReading(i, a, reading, kinds)
		w.fs.setReading(a.ID(), rd)
		if rd == ReadType && !typeShaped(a) {
			w.notType(a)
		}
	}
}
