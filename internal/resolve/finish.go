// Why: once every declaration is known, the names inside declarations and
// bodies can be resolved in parallel, but the result must not depend on
// which worker finished first.
//
// What: Module.Finish (task creation, the bounded pool, runTask, merge),
// poisonTask, applyFixups (the recase fixes), importE0208 (unused imports),
// missingMain (E0201 for a bin root with no Main) and Module.Lookup.
//
// How: one task per item record, run on a bounded pool, each under
// source.Guard so an ICE poisons only its own item; results are merged in
// key order, never completion order. Names that may still be generated are
// left pending for Settle.

package resolve

import (
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// Finish resolves every name of every declaration and body, in parallel over
// tasks with at most workers goroutines (0 means GOMAXPROCS), each under
// source.Guard. It ends by merging the per-task results in key order and
// sorting Module.Items by key, lifted literals included. It returns the
// module's Info. A name that misses in a module with unsettled gen lines, or a
// m.X whose m has them, is pending, not an error. Calling Finish twice is an
// ICE.
func (m *Module) Finish(workers int) *Info {
	if m.finished {
		source.ICEf(source.Span{}, "resolve: Finish called twice on %s", m.mod.Path)
	}
	m.finished = true
	tasks := m.newTasks(m.recs)
	m.run(tasks, workers)
	m.merge(tasks)
	m.tasks = append(m.tasks, tasks...)
	m.applyFixups()
	m.sortModuleItems()
	m.sortInfoItems()
	return m.info
}

// newTasks makes one task per record that is not a dependent and has no
// task yet, in key order.
func (m *Module) newTasks(recs []*itemRec) []*task {
	var out []*task
	for _, r := range recs {
		if r.parent != nil || r.task != nil || r.item.Kind == types.KindLiteral {
			continue
		}
		t := &task{root: r, file: r.file}
		r.task = t
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].root.item.Key.Compare(out[j].root.item.Key) < 0 })
	return out
}

// reset prepares a task for a (re)run.
func (m *Module) reset(t *task) {
	t.diags, t.pending, t.literals, t.fixups, t.habits, t.failed = nil, nil, nil, nil, nil, false
	t.casePend = nil
	t.used = make([]bool, len(m.files[t.file].fi.Imports))
	t.pastModule = map[source.Symbol]bool{}
	reset := func(r *itemRec) {
		r.info.Reads = types.ReadSet{}
		r.info.Literals = nil
		r.litN = 0
	}
	reset(t.root)
	for _, d := range t.root.deps {
		reset(d)
		for _, dd := range d.deps {
			reset(dd)
		}
	}
}

// run executes tasks on a bounded pool.
func (m *Module) run(tasks []*task, workers int) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers <= 1 {
		for _, t := range tasks {
			m.runTask(t)
		}
		return
	}
	var wg sync.WaitGroup
	var next atomic.Int64
	faults := make([]*source.InternalError, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			faults[slot] = source.Guard("resolve.Finish "+m.mod.Path, source.Span{}, func() {
				for {
					k := int(next.Add(1)) - 1
					if k >= len(tasks) {
						return
					}
					m.runTask(tasks[k])
				}
			})
		}(i)
	}
	wg.Wait()
	for _, f := range faults {
		if f != nil {
			m.info.Diags = append(m.info.Diags, diag.FromInternal(f))
		}
	}
}

// runTask resolves one task under source.Guard: an ICE becomes one E0000
// naming the task's key, and every node of the task becomes RefPoison
// (its pending names and fixes are dropped).
func (m *Module) runTask(t *task) {
	m.reset(t)
	fs := m.files[t.file]
	ie := source.Guard(t.root.item.Key.String(), t.root.item.Span, func() {
		w := newWalker(m, t, fs)
		w.walkTask()
	})
	if ie != nil {
		t.failed = true
		t.pending, t.fixups, t.habits = nil, nil, nil
		t.diags = append(t.diags, diag.FromInternal(ie))
		m.poisonTask(fs, t.root.node)
		return
	}
	t.diags = m.mergeHabits(t.diags, t.habits)
	t.habits = nil
}

// poisonTask sets RefPoison on the root node and on every node of the file
// that lies inside it (its dependents and literals included), so later
// passes accept the failed item silently instead of meeting RefNone. The
// file's tree is walked, not the root's own (which may be what broke), and
// under a Guard of its own: a tree that panics again leaves the rest as is.
func (m *Module) poisonTask(fs *fileState, root ast.Node) {
	fs.setRef(root.ID(), poisonRef)
	span := root.Span()
	source.Guard("resolve.poison", span, func() {
		ast.Inspect(fs.fi.File, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			s := n.Span()
			if s.End < span.Start || s.Start > span.End {
				return false
			}
			if _, file := n.(*ast.File); !file && s.Start >= span.Start && s.End <= span.End {
				fs.setRef(n.ID(), poisonRef)
			}
			return true
		})
	})
}

// merge folds the task slots into the module, in task (key) order.
func (m *Module) merge(tasks []*task) {
	for _, t := range tasks {
		sortedTaskDiags(t.diags)
		m.info.Diags = append(m.info.Diags, t.diags...)
		m.mergeUsed(t)
		m.adopt(t)
		m.fixups = append(m.fixups, t.fixups...)
	}
}

// mergeUsed ORs a task's used set into its file's bindings.
func (m *Module) mergeUsed(t *task) {
	imps := m.files[t.file].fi.Imports
	for i, u := range t.used {
		if u && i < len(imps) {
			imps[i].Used = true
		}
	}
	// A quote's root name resolves through the union of the files'
	// bindings: through its own file's binding when that file has
	// one, else through the files that bind it.
	for _, sym := range t.quoted {
		if markUsed(imps, sym) {
			continue
		}
		for _, fs := range m.files {
			if !fs.generated {
				markUsed(fs.fi.Imports, sym)
			}
		}
	}
}

// markUsed marks the bindings of sym used and reports whether one exists.
func markUsed(imps []Binding, sym source.Symbol) bool {
	found := false
	for i := range imps {
		if imps[i].Name == sym {
			imps[i].Used = true
			found = true
		}
	}
	return found
}

// adopt registers the literals a task lifted.
func (m *Module) adopt(t *task) {
	for _, r := range t.literals {
		if m.byID[r.item.ID] != nil {
			continue
		}
		m.recs = append(m.recs, r)
		m.byID[r.item.ID] = r
		m.mod.Items = append(m.mod.Items, r.item.ID)
	}
}

// sortInfoItems rebuilds Info.Items in key order.
func (m *Module) sortInfoItems() {
	m.info.Items = m.info.Items[:0]
	for _, r := range m.recs {
		m.info.Items = append(m.info.Items, r.info)
	}
	items := m.s.cfg.Items
	slices.SortFunc(m.info.Items, func(a, b *ItemInfo) int { return items.Get(a.Item).Key.Compare(items.Get(b.Item).Key) })
	slices.SortFunc(m.info.Gens, func(a, b GenLine) int { return items.Get(a.Item).Key.Compare(items.Get(b.Item).Key) })
}

// applyFixups completes the recase fixes: the declaration's edit plus one
// per Ident whose Ref is the target (in its file, or every file).
func (m *Module) applyFixups() {
	if len(m.fixups) == 0 {
		return
	}
	for _, fs := range m.files {
		var mine []int
		for i, fx := range m.fixups {
			if fx.file == -1 || fx.file == fs.idx {
				mine = append(mine, i)
			}
		}
		if len(mine) == 0 {
			continue
		}
		ast.Inspect(fs.fi.File, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return n != nil
			}
			r := fs.fi.Refs[id.ID()]
			for _, i := range mine {
				fx := &m.fixups[i]
				if sameTarget(r, fx.target) && id.Span() != fx.decl {
					fx.uses = append(fx.uses, id.Span())
				}
			}
			return false
		})
	}
	for _, fx := range m.fixups {
		if fx.blocked != nil && fx.blocked() {
			fx.d.Alt(fx.alt)
			continue
		}
		edits := []diag.Edit{{Span: fx.decl, Text: fx.text}}
		for _, u := range fx.uses {
			edits = append(edits, diag.Edit{Span: u, Text: fx.text})
		}
		sort.Slice(edits, func(i, j int) bool {
			if edits[i].Span.File != edits[j].Span.File {
				return edits[i].Span.File < edits[j].Span.File
			}
			return edits[i].Span.Start < edits[j].Span.Start
		})
		fx.d.Fix(diag.Fix{Class: diag.Proven, Edits: edits, Why: fx.why})
	}
	m.fixups = nil
}

func sameTarget(r, t Ref) bool {
	return r.Kind == t.Kind && r.A == t.A && r.B == t.B && r.Kind != RefNone
}

// Lookup finds a top-level item of this module by exact name, generated
// items included once added. The driver uses it for gen's deferred
// arguments (Context.Finish).
func (m *Module) Lookup(name source.Symbol) (types.ItemID, bool) { return m.rec.names.Get(name) }

// importE0208 reports the unused import bindings of every handwritten file:
// anchored at the import line, fix delete the line.
func (m *Module) importE0208() []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for _, fs := range m.files {
		if fs.generated || fs.testPrelude {
			continue
		}
		for _, b := range fs.fi.Imports {
			if b.Used || b.Node == ast.NoNode || b.Module == types.NoModule {
				continue
			}
			im := findImport(fs.fi.File, b.Node)
			if im == nil {
				continue
			}
			name := m.str(b.Name)
			d := diag.New(diag.E0208, im.Span(), "import `"+b.Path+"` is never used").Text("name", name)
			d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: lineSpan(fs.fi.File, im.Span())}}, Why: "nothing in the file uses `" + name + "`"})
			out = append(out, d)
		}
	}
	return out
}

// missingMain reports a bin root with no module-level Main.
func (m *Module) missingMain() *diag.Diagnostic {
	if !m.in.BinRoot {
		return nil
	}
	if _, ok := m.rec.names.Get(source.SymMain); ok {
		return nil
	}
	var at source.Span
	if len(m.files) > 0 {
		at = source.Span{File: m.files[0].fi.FileID}
	}
	d := diag.New(diag.E0201, at, "a program needs a `Main` function").Text("name", "Main").Text("expected", "function")
	d.Alt("declare `export Main(sys: &System)`")
	return d
}
