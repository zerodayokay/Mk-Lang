// Why: holding every stage's output for every function at once makes the
// memory of mk check grow with the whole package, so each stage's output is
// released once no later stage of the command reads it.
//
// What: world.keepsSyntax and loadsWhole, which say what a command must keep,
// and build.release and releaseStored, which drop the rest per module.
//
// How: a module that missed keeps its MIR (the walk, eval, mono and codegen
// read it), its check index without syntax (importers' member and impl
// lookups, check.Module.Release), its Decls and part hashes, its diagnostics
// and its parse summaries without per-item token lists unless the cache
// needs them. Once its store (if any) has run, it drops its ASTs and token
// lists, resolve's Info, check's node index, and the unit records with their
// read sets.

package driver

import "mk/internal/types"

// keepsSyntax reports whether the session may answer queries over a
// module's syntax after the check, so nothing of a module's front end may be
// dropped: always under Config.KeepSyntax (mk lsp's exact answers), and in
// every session that is not a command's (the API sessions of lspapi.go,
// query.go and the tests answer UsesOf and TypeAt over missed modules). A
// command sets ReleaseFront, or GenView, which cmd/mk's check, build, run
// and test set and no API session does.
func (w *world) keepsSyntax() bool {
	return w.cfg.KeepSyntax || !w.cfg.ReleaseFront && !w.cfg.GenView
}

// loadsWhole reports whether m may not be a module hit, so its syntax,
// resolution and check index are the session's after the check: a package
// module under Config.KeepSyntax, or one with a file KeepSyntaxFor names,
// or under KeepSyntax a std module Config.StdTests selects.
func (w *world) loadsWhole(m *modNode) bool {
	if m.std && w.cfg.KeepSyntax && w.cfg.StdTests != "" && stdSelects(w.cfg.StdTests, m.path) {
		// mk fix --std: the std modules being rewritten are the
		// subject, so they keep their syntax and typed bodies.
		return true
	}
	if m.std || m.cimport {
		return false
	}
	if w.cfg.KeepSyntax {
		return true
	}
	if keep := w.cfg.KeepSyntaxFor; keep != nil {
		for _, f := range m.files {
			if f.src != nil && keep(f.src.Path) {
				return true
			}
		}
	}
	return false
}

// release is called on m's worker once m's check, lowering and store
// (store's synchronous half) are through, before any importer starts.
// It drops what no later stage reads: check's syntax side and the files'
// ASTs now; resolve's Info and the unit records when the cache is off, else
// at the end of the background store, which reads them (releaseStored).
func (b *build) release(m *modNode) {
	w := b.w
	if w.keepsSyntax() || b.keepSyntax || w.loadsWhole(m) {
		return
	}
	if m.cmod != nil {
		m.cmod.Release()
	}
	if m.info != nil {
		m.genKnown, m.genLines = true, len(m.info.Gens)
	}
	for _, f := range m.files {
		f.ast = nil
	}
	if w.front == nil {
		b.releaseStored(m)
	}
}

// releaseStored drops what only the module's store reads: resolve's Info
// (the function prefixes' imports and item records), the unit records with
// their read sets, check's per-item reads and the evaluations' reads. It
// runs on the store's goroutine, after it, or on m's worker with no store;
// no other goroutine reads these fields of m once m's work is through.
func (b *build) releaseStored(m *modNode) {
	if b.w.keepsSyntax() || b.keepSyntax || b.w.loadsWhole(m) {
		return
	}
	m.infoOnce.Do(func() {}) // a later itemInfo must not rebuild from a nil Info
	m.info, m.infoBy = nil, nil
	if m.cmod != nil {
		m.cmod.Reads = nil
	}
	m.readsMu.Lock()
	m.units = nil
	m.reads = types.ReadSet{}
	m.readsMu.Unlock()
}

// noGenLines reports whether stage 3 ran on m and found no gen line (the
// gen view's removal rule, genview.go), before or after release.
func (m *modNode) noGenLines() bool {
	if m.genKnown {
		return m.genLines == 0
	}
	return m.info != nil && len(m.info.Gens) == 0
}
