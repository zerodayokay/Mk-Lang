// Why: modules of a build depend on each other, and running each one's
// pipeline as soon as its imports are done keeps every worker busy without a
// barrier between stages.
//
// What: build.register, preludeFor and setImports (the final import graph),
// pipelineAll and schedule (the dependency-driven run), resolveModule (stage
// 3), namesClean (the phase rule), and implicitRT.
//
// How: after the imports barrier each module runs stage 3 (resolve: Begin,
// Finish, Settle) then stages 5 to 7 (stages.go) as soon as every module it
// depends on is Done, on the shared worker bound. The prelude's import
// closure is resolved first (resolveClosure): check.New needs resolve's
// KnownItems, which name prelude items (startSemantics).

package driver

import (
	"slices"
	"strings"
	"sync"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// register adds the build's modules to the item table in slot order (depth,
// path), so module IDs are a function of the source tree.
func (b *build) register() {
	for _, m := range b.mods {
		m.mod = &types.Module{Path: m.path, Std: m.std, UnsafePermitted: m.unsafe}
		for _, f := range m.files {
			m.mod.Files = append(m.mod.Files, f.src.ID)
		}
		m.id = b.w.items.AddModule(m.mod)
	}
}

// preludeFor sets which modules see the prelude: every module except std.core
// and the std modules std.core imports, directly or not (they cannot see it).
// The graph is final then, so it sets every module's Imports.
func (b *build) preludeFor(core *modNode) {
	defer b.setImports()
	if core == nil {
		return
	}
	below := map[*modNode]bool{core: true}
	var mark func(m *modNode)
	mark = func(m *modNode) {
		for _, d := range m.deps {
			if !below[d] {
				below[d] = true
				mark(d)
			}
		}
	}
	mark(core)
	for _, m := range b.mods {
		m.seesCore = !below[m]
		if m.seesCore && !slices.Contains(m.deps, core) {
			m.deps = append(m.deps, core)
		}
	}
	b.implicitRT()
}

// setImports sets types.Module.Imports of every module of b from the final
// import graph: m.deps after the import cycle removal and preludeFor, so a
// module that sees the prelude lists std.core. check reads it (Load, the
// member index); Config has no import list. Each once, ordered by path,
// never by ID. It runs after register (every dep has its ID) and before any
// stage starts, and nothing writes Imports after it, so stages read it with
// no lock. iface encodes it, and a module decoded into this one is stale
// when its stored imports differ.
func (b *build) setImports() {
	for _, m := range b.mods {
		if m.mod == nil {
			continue
		}
		deps := slices.Clone(m.deps)
		slices.SortFunc(deps, func(x, y *modNode) int { return strings.Compare(x.path, y.path) })
		deps = slices.CompactFunc(deps, func(x, y *modNode) bool { return x.path == y.path })
		ids := make([]types.ModuleID, 0, len(deps))
		for _, d := range deps {
			if d.id != types.NoModule {
				ids = append(ids, d.id)
			}
		}
		m.mod.Imports = ids
	}
}

// stage3 runs stage 3 of one module once, under its own source.Guard: a
// fault is an E0000 of the module, which is then failed with no Info.
func (b *build) stage3(m *modNode) {
	if m.resolved {
		return
	}
	m.resolved = true
	if ie := source.Guard("resolve "+m.path, b.moduleSpan(m), func() { b.resolveModule(m) }); ie != nil {
		m.diags = append(m.diags, diag.FromInternal(ie))
		m.failed = true
		// A fault may leave a half-built Info (one in Settle, say): check
		// skips a module with none (checkModule), so a check fault is
		// never a consequence of this one.
		m.info = nil
	}
}

// resolveClosure runs stage 3 over m and every module of the build it
// imports, directly or not, dependency-driven. Session and corpus run it
// on the prelude module before startSemantics.
func (b *build) resolveClosure(m *modNode) {
	if m == nil {
		return
	}
	in := map[*modNode]bool{}
	var mark func(x *modNode)
	mark = func(x *modNode) {
		if in[x] {
			return
		}
		in[x] = true
		for _, d := range x.deps {
			mark(d)
		}
	}
	mark(m)
	var set []*modNode
	for _, x := range b.mods {
		if in[x] {
			set = append(set, x)
		}
	}
	fc := b.w.front
	b.schedule("resolve", set, func(x *modNode) {
		if fc != nil && !b.w.loadsWhole(x) && fc.lookup(b.w, x) {
			x.phase1 = true
			return
		}
		b.stage3(x)
	}, nil)
}

// pipelineAll is the per-module pipeline over the build: each module,
// once every module it depends on is Done, runs stage 3 (unless
// resolveClosure did), then, when checkOn, stages 5 to 7 (checkModule).
// A module whose own stage 3, or a dependency's, holds a diagnostic of a
// phase before types is not checked: the phase rule drops every
// diagnostic its check could give, and check would run over
// poisoned names. TestHooks.CheckPastNames checks it anyway. Modules of an
// earlier build (std, for a corpus unit) are Done already.
func (b *build) pipelineAll(checkOn bool) {
	if checkOn {
		for _, m := range b.mods {
			b.w.sem5.noteModule(m)
		}
	}
	fc := b.w.front
	if !checkOn {
		fc = nil // a hit needs stages 5 to 7's half (loadHit)
	}
	b.schedule("check", b.mods, func(m *modNode) {
		if fc != nil && m.hit == nil && !m.resolved && !b.w.loadsWhole(m) && fc.lookup(b.w, m) {
			// A module hit: no file of it is resolved.
		}
		if m.hit != nil {
			b.loadHit(m)
			b.w.refreshKnown(m)
			return
		}
		// A module slot from stage 3 to release (world.mods): no worker
		// slot is held here, and a module's work never waits on another
		// module's, so a full set of slots always drains.
		// Read once (std-rewrite's request: a hang at a send on a nil
		// channel here was seen with a mid-edit tree).
		if slots := b.w.mods; slots != nil {
			slots <- struct{}{}
			defer func() { <-slots }()
		}
		b.stage3(m)
		if !checkOn {
			return
		}
		// std.simd's KnownItems fields, before its importers start.
		b.w.refreshKnown(m)
		if !b.namesClean(m) {
			m.namesBad = true
			m.state.Store(modDoneFailed)
			return
		}
		b.checkModule(m)
		m.dropNames()
		if m.info != nil {
			m.genKnown, m.genLines = true, len(m.info.Gens)
		}
		if fc != nil {
			b.store(m)
		}
		b.release(m)
	}, func(m *modNode, d *diag.Diagnostic) {
		m.cdiags = append(m.cdiags, d)
		m.state.Store(modDoneFailed)
	})
	for _, m := range b.mods {
		if m.resolved {
			b.trace.ModulesResolved = append(b.trace.ModulesResolved, m.path)
		}
		if m.fromCache {
			b.trace.ModulesFromCache = append(b.trace.ModulesFromCache, m.path)
		}
		b.trace.ItemsChecked = append(b.trace.ItemsChecked, m.checked...)
		b.trace.ItemsFromCache = append(b.trace.ItemsFromCache, m.reused...)
		b.trace.Evaluations = append(b.trace.Evaluations, m.evals...)
		b.trace.GeneratorRuns = append(b.trace.GeneratorRuns, m.genRuns...)
		b.trace.FilesParsed = append(b.trace.FilesParsed, m.parsed...)
	}
}

// namesClean reports whether m may run stages 5 to 7: its stage 3 gave an
// Info and no diagnostic of a phase before types, and no module it
// depends on is namesBad.
func (b *build) namesClean(m *modNode) bool {
	if b.w.cfg.TestHooks.CheckPastNames {
		return true
	}
	if m.info == nil {
		return false
	}
	for _, d := range m.diags {
		if d.Phase != diag.PhaseInternal && d.Phase < diag.PhaseTypes {
			return false
		}
	}
	for _, d := range m.deps {
		if d.namesBad {
			return false
		}
	}
	return true
}

// schedule runs work over mods (modules of b), dependency-driven: a module
// starts when every module of mods it depends on (its imports and the prelude
// module) has finished its work; a dependency outside mods is finished already
// (an earlier build, or an earlier schedule). fault, when not nil, receives an
// ICE of work as an E0000. Each module's work runs under source.Guard (an ICE
// is an E0000 of the module, which is then failed), and a module's importers
// start whatever happened, so a fault never blocks them. An importer is started
// only after its imports' work returned, through mu: everything an import wrote
// (Decls, const values, its state) happens before the importer runs.
func (b *build) schedule(stage string, mods []*modNode, work func(m *modNode), fault func(m *modNode, d *diag.Diagnostic)) {
	waiting := map[*modNode]int{}
	importers := map[*modNode][]*modNode{}
	own := map[*modNode]bool{}
	for _, m := range mods {
		own[m] = true
	}
	var ready []*modNode
	for _, m := range mods {
		n := 0
		for _, d := range append(slices.Clone(m.deps), m.after...) {
			if own[d] {
				n++
				importers[d] = append(importers[d], m)
			}
		}
		waiting[m] = n
		if n == 0 {
			ready = append(ready, m)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var start func(m *modNode)
	start = func(m *modNode) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ie := source.Guard(stage+" "+m.path, b.moduleSpan(m), func() { work(m) })
			if ie != nil && fault != nil {
				fault(m, diag.FromInternal(ie))
			}
			mu.Lock()
			var now []*modNode
			for _, x := range importers[m] {
				waiting[x]--
				if waiting[x] == 0 {
					now = append(now, x)
				}
			}
			mu.Unlock()
			for _, x := range now {
				start(x)
			}
		}()
	}
	for _, m := range ready {
		start(m)
	}
	wg.Wait()
}

// finish closes every module's done channel: the build's last stage is
// through.
func (b *build) finish() {
	for _, m := range b.mods {
		select {
		case <-m.done:
		default:
			close(m.done)
		}
	}
}

// moduleSpan is the span of the module's first file, for an ICE with none.
func (b *build) moduleSpan(m *modNode) source.Span {
	if len(m.files) == 0 || m.files[0].src == nil {
		return source.Span{}
	}
	return source.Span{File: m.files[0].src.ID}
}

// resolveModule is stage 3 of one module: Begin, Finish, the gen lines
// (stage4), Settle, then settleGen. It holds one worker slot.
func (b *build) resolveModule(m *modNode) {
	if hook := b.w.cfg.TestHooks.PanicIn; hook != "" && hook == m.path {
		source.ICEf(b.moduleSpan(m), "test hook: panic in %s", m.path)
	}
	b.ensureParsed(m) // before m's slot: each file takes its own
	b.w.sem <- struct{}{}
	defer func() { <-b.w.sem }()
	in := &resolve.ModuleInput{Module: m.id, Imports: map[string]types.ModuleID{}, BinRoot: m.binRoot, Implicit: m.implicit}
	for _, f := range m.files {
		in.Files = append(in.Files, f.ast)
		if f.prelude {
			in.TestPrelude = f.src.ID
		}
	}
	for p, d := range m.imports {
		if d != nil {
			in.Imports[p] = d.id
		} else {
			in.Imports[p] = types.NoModule
		}
	}
	if m.seesCore {
		in.Prelude = b.core.id
	}
	rm := b.w.rs.Begin(in)
	m.info = rm.Finish(1)
	b.stage4(m, rm)
	ds, gs := rm.Settle()
	gs = b.settleGen(m, rm, gs)
	for _, d := range m.info.Diags {
		m.noteGen(d)
	}
	for _, d := range ds {
		m.noteGen(d)
	}
	m.diags = append(m.diags, m.info.Diags...)
	m.diags = append(m.diags, ds...)
	m.diags = append(m.diags, gs...)
	m.failed = len(m.diags) > 0
}

// stageDiags collects every module's stage 3 diagnostics in slot order, and
// the error a poisoned gen line follows from (genRoot), of a later phase and
// another module: the E0201s it causes stop the build before checkDiags, and
// the report keeps it as their cause. diag.Order drops it when no kept
// diagnostic names it, and as a duplicate when checkDiags reports it too.
func (b *build) stageDiags() []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for _, m := range b.mods {
		out = append(out, m.diags...)
		if m.genRoot != nil {
			out = append(out, m.genRoot)
		}
	}
	return out
}

// checkDiags collects every module's stage 5, 6 and 7 diagnostics in slot
// order.
func (b *build) checkDiags() []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for _, m := range b.mods {
		out = append(out, m.genWrap(m.cdiags)...)
		out = append(out, m.genWrap(m.fdiags)...)
	}
	return out
}

// declaresMain reports whether a file declares a module-level Main.
func declaresMain(f *ast.File, in *source.Interner) bool {
	for _, it := range f.Items {
		if fd, ok := it.(*ast.FuncDecl); ok && fd.Sig != nil && fd.Sig.Name != nil && fd.Sig.Name.Owner == nil &&
			fd.Sig.Name.Name != nil && in.Str(fd.Sig.Name.Name.Name) == "Main" {
			return true
		}
	}
	return false
}

// rtModule is the module whose items lowering calls with no import: the
// error-return trace hooks and std.rt.Site (internal/lower trace.go).
const rtModule = "std.rt"

// implicitRT gives every module outside std.rt's import closure (std.rt
// excluded) the edge after = std.rt. Lowering looks std.rt.Site and the
// trace hooks up by key, and is inert when they are absent, so a std
// module that neither imports std.rt nor lies below it raced std.rt: it
// traced or not by timing, and could read Site's facts before std.rt's
// impls were registered (the ICE "facts of std.rt.Site asked before its
// module's impls are registered", std-compress, std-regex). With the edge
// it is scheduled after std.rt, so it always traces, and its cache key
// covers std.rt's closure, so a hit's function results never name a hook
// the current std.rt lacks (TestFrontWarmCheckEqualsCold; "iface: entry
// refers to something that changed: item std.rt.TraceTry"). A module
// below std.rt never traces (std.rt is not resolved before it), as
// before. A package's modules run after the whole std build, so for them
// the edge only adds std.rt to the key.
func (b *build) implicitRT() {
	std := b
	if !b.isStd {
		std = b.std
	}
	if std == nil {
		return
	}
	rt := std.byName[rtModule]
	if rt == nil {
		return
	}
	below := map[*modNode]bool{rt: true}
	var mark func(m *modNode)
	mark = func(m *modNode) {
		for _, d := range m.deps {
			if !below[d] {
				below[d] = true
				mark(d)
			}
		}
	}
	mark(rt)
	for _, m := range b.mods {
		if !below[m] && !slices.Contains(m.deps, rt) && !slices.Contains(m.after, rt) {
			m.after = append(m.after, rt)
		}
	}
}
