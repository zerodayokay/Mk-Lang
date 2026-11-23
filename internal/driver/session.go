// Why: every command (check, build, run, test, query) runs the same
// front of the pipeline; one Session holds the configuration, the caches
// and the checked world they share.
//
// What: Session and NewSession, Check, Result with ReportJSON and
// WriteReport, the pipeline type (step, front, guard, modules, result) and
// BuildID.
//
// How: the manifest step, then the read, parse and import steps, are global
// barriers, then the modules run per module (schedule.go); diag.Order
// applies the phase rule once over the whole build. Every step runs under
// source.Guard, so an ICE is an E0000 in the report, never a Go panic.

package driver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/std"
)

// Session is one compiler run over one package (or one file). It owns the
// source map, the interner, the type and item tables and every stage
// result. Use it from one goroutine; it starts and joins its own workers.
type Session struct {
	cfg  Config
	root rootInfo
	w    *world // the last Check's tables
	// p, pkgB and pkgMan are the last Check's pipeline, package build and
	// manifest (nil in single-file and directory mode), for Build and Run.
	p      *pipeline
	stdB   *build
	pkgB   *build
	pkgMan *Manifest
	// ms is the last Check's manifest step, whose tables hold std.pkg
	// (mk apidiff --std reads them).
	ms *manifestStep
	// deps are the package's dependencies in build order (deps.go), and
	// depBs their builds, in the same order.
	deps  []*depPkg
	depBs []*build
	// depRoot maps the root manifest's local names to their packages
	// (deps_walk.go).
	depRoot map[string]*depPkg
	// walked and walkedRoot are the last dependency walk's build list and
	// root names, kept when only a grant is missing (mk query perimeter).
	walked     []*depPkg
	walkedRoot map[string]*depPkg
	walkedMan  *Manifest
	// walkedGrants are the sources and urls the grant manifest names.
	walkedGrants map[string]bool
	// walkedSum is the walk's pkg.sum (with the lines it used), walkedChosen
	// its version choice, wsDir the workspace root's directory (pkgcmd.go).
	walkedSum    *pkgSum
	walkedChosen map[string]*selected
	wsDir        string
	walkedSelf   *Manifest // the package's own manifest
	// noVendor: the dependency walk ignores vendor/ (mk vendor).
	noVendor bool
	// forBuild: the session builds (build, run): a single file is a bin
	// root whether or not it declares Main.
	forBuild bool
	// deferWalk: the instance-limit walk runs beside the native stages
	// (mk build only, walktail.go).
	deferWalk bool
	// testing: the session is mk test (testexec.go): the package build gains
	// the runner module, and no module is a bin root unless it declares Main.
	testing bool
	runner  *modNode
	// prop: the session is mk test --prop or one of its rounds (prop.go):
	// as testing for the Main rule, with no runner.
	prop bool
	// disc is the toolchain discovery a native command starts before its
	// check (startTools).
	disc discovery
	// cres is clang's resource directory, once asked (cimport.go).
	cres string
	// lsp is lspapi.go's memo of the last Check's answers, dropped when a
	// Check makes a new world.
	lsp *lspMemo
	// fullStd forces all std modules and std test files to load (apidiff,
	// doc --std, query, StdTests, BrowseStd; stdlazy.go). lastFullStd records
	// whether the last Check ran with fullStd.
	fullStd     bool
	lastFullStd bool
	// customReadFile is true when the caller supplied a non-nil Config.ReadFile
	// before normalize filled the os.ReadFile default (fastbuild.go).
	customReadFile bool
}

// NewSession validates the config and finds the root. The error is a
// *UsageError (exit 2), never a diagnostic.
func NewSession(cfg Config) (*Session, error) {
	customReadFile := cfg.ReadFile != nil
	cfg, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	if cfg.StdTests != "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, usagef("cannot use the working directory: %v", err)
		}
		return &Session{cfg: cfg, root: rootInfo{kind: rootNone, dir: wd, name: "mk-test"}, customReadFile: customReadFile}, nil
	}
	root, err := findRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	return &Session{cfg: cfg, root: root, customReadFile: customReadFile}, nil
}

// Result is a command's compile outcome.
type Result struct {
	Report  diag.Report // already ordered by diag.Order with the session's cap
	Sources *source.SourceMap
	Reached Stage  // the last stage that ran over the whole build
	Trace   *Trace // what ran
	// Timings are the stages' wall times, filled only with Config.Timings.
	Timings []StageTime
	// SlowFuncs are the functions whose check plus borrow time exceeded
	// 10 ms, by key; filled only with Config.Timings.
	SlowFuncs []FuncTime
	// PGOStale are the functions of a --pgo build that had no usable
	// profile data (no record, another CFG checksum, or another counter
	// count; codegen.Report.Stale), sorted. Printed under --timings; never
	// part of --json.
	PGOStale []string
	// Notes are mk check's notes, printed after the diagnostics: one per
	// package that imports an Experimental module (classes.go).
	// They change no exit status; --json carries them as "notes" only when
	// there are some.
	Notes []string
}

// WriteReport renders r as the --json document or as text.
// Text of a clean report is empty.
// ReportJSONVersion is the --json schema version.
const ReportJSONVersion = 1

// ReportJSON is the --json document: {"version": 1, "phase": ...,
// "diagnostics": [...], "omitted": n} and a newline. diag's WriteJSON
// writes the object from "phase" on (the corpus snapshots keep that form),
// and always opens it with {"phase", so the version is spliced in front.
func (r Result) ReportJSON() []byte {
	body := r.Report.WriteJSON(r.Sources)
	out := append([]byte(`{"version":`+strconv.Itoa(ReportJSONVersion)+`,`), body[1:]...)
	if len(r.Notes) == 0 {
		return out
	}
	// A field added within version 1: before the closing brace of the
	// object.
	end := bytes.LastIndexByte(out, '}')
	notes, _ := json.Marshal(r.Notes)
	return append(append(append(out[:end:end], `,"notes":`...), notes...), out[end:]...)
}

func (r Result) WriteReport(w io.Writer, json bool) error {
	if json {
		_, err := w.Write(r.ReportJSON())
		return err
	}
	if len(r.Report.Diags) > 0 {
		if _, err := io.WriteString(w, r.Report.Text(r.Sources)); err != nil {
			return err
		}
	}
	for _, n := range r.Notes {
		if _, err := io.WriteString(w, n+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// stdTree is the standard library's source tree: MK_STD (Config.StdDir) or
// the embedded std.FS. Its files are cited as std/<dir>/<file>.mk.
func (w *world) stdTree() *srcTree {
	if w.cfg.StdDir != "" {
		t := w.diskTree(w.cfg.StdDir, "std/")
		t.sitePrefix = "std/"
		return t
	}
	return w.wrapTree(&srcTree{fsys: std.FS, prefix: "std/", sitePrefix: "std/"})
}

// pipeline is the shared front of every build: std, then the package or a
// unit, through the global barriers and stage 3.
type pipeline struct {
	w      *world
	builds []*build
	// manifest: the module paths the manifest step checked
	// (Trace.ManifestChecked)
	manifest []string
	// notes are the manifest step's notes, carried by every result
	// (Result.Notes).
	notes   []string
	reached Stage
	diags   []*diag.Diagnostic
	times   []StageTime // --timings, in run order
	// gro are the general-regs-only bins the check judges after the
	// instance-limit walk (free.go groWalk), so mk check and mk build
	// report one E1108 set.
	gro []groJob
	// deferWalk and tail: the walk started beside the native stages
	// (walktail.go).
	deferWalk bool
	tail      *walkTail
	walkBad   bool // the joined walk reported something
	// lazyStd loads only transitively reached std modules (stdlazy.go).
	lazyStd bool
	testing bool
}

// step runs one barrier: f's diagnostics are recorded, and the pipeline
// stops when there are any.
func (p *pipeline) step(st Stage, f func() []*diag.Diagnostic) bool {
	return p.stepNamed(st.String(), st, f)
}

// stepNamed is step with the timing line's name given: a step that is not
// a stage of its own (the import step) still has a line of its own.
func (p *pipeline) stepNamed(name string, st Stage, f func() []*diag.Diagnostic) bool {
	var ds []*diag.Diagnostic
	p.timed(name, func() { ds = f() })
	p.reached = st
	p.diags = append(p.diags, ds...)
	return len(ds) == 0
}

// front runs the read, parse and import steps over p's builds, in order. It
// returns false when a barrier stopped the build.
func (p *pipeline) front() bool {
	var tooLarge []*diag.Diagnostic
	if !p.step(StageLoad, func() []*diag.Diagnostic {
		if p.lazyStd && len(p.builds) > 1 && p.builds[0].isStd {
			var fails []*diag.Diagnostic
			fails, tooLarge = p.loadLazyStd(p.testing)
			return fails
		}
		var fails []*diag.Diagnostic
		for _, b := range p.builds {
			for _, ci := range p.w.allCImports() {
				if b == ci.b {
					ci.prune(p.w, p.builds)
					fails = append(fails, ci.mismatch...)
				}
			}
			f, big := b.readAll()
			fails = append(fails, f...)
			tooLarge = append(tooLarge, big...)
		}
		return fails
	}) {
		return false
	}
	if !p.step(StageParse, func() []*diag.Diagnostic {
		ds := tooLarge
		for _, b := range p.builds {
			ds = append(ds, b.parseAll()...)
		}
		return ds
	}) {
		return false
	}
	// The import step belongs to stage 1 (Load) of doc/compiler.md's stage
	// table but runs after parsing, so Reached stays StageParse until stage
	// 3 starts. Its timing line is "imports", never a second "parse".
	return p.stepNamed("imports", StageParse, func() []*diag.Diagnostic {
		var ds []*diag.Diagnostic
		for _, b := range p.builds {
			ds = append(ds, b.link()...)
		}
		// The late std modules an importer reached, after every importer
		// is linked (cstd.go); depths need every module's imports.
		ds = append(ds, p.addLate()...)
		for _, b := range p.builds {
			b.depths()
		}
		return ds
	})
}

// Check runs the manifest step, stages 1 to 7b and the instance-limit walk,
// applies the phase rule and returns the report.
// It starts no tool process, except clang for the package's C imports:
// typing a binding needs its translation (cimport.go).
//
// Every step runs under source.Guard: the parallel parse and stage 3 guard
// each file and module, and the sequential steps between them (the manifest
// step, listing, the read step, the parse step's bookkeeping, the import
// step) run under one Guard here, so an ICE in any of them is an E0000 in the
// report (exit 3), never a Go panic.
func (s *Session) Check() Result {
	r := s.runCheck()
	r.Notes = append(r.Notes, s.experimentalNotes()...)
	for i, n := range r.Notes {
		r.Notes[i] = localizeText(n, s.displayNames(nil))
	}
	s.w.maybeGC() // once at command end
	return r
}

// runCheck is Check without the command-end MaybeGC, for Build and Run,
// which call it at their own end.
func (s *Session) runCheck() Result {
	defer enterMemBudget()() // memlimit.go
	w := newWorld(s.cfg)
	s.w = w
	p := &pipeline{w: w, deferWalk: s.deferWalk}
	s.p, s.pkgB, s.pkgMan, s.deps, s.depBs = p, nil, nil, nil, nil
	p.guard("check "+s.root.name, func() {
		w.startFront(s.cacheDir())
		s.check(p)
	})
	s.localizeDiags(p.diags) // a dependency by its local name (deps_walk.go)
	// The manifest step's notes (an older-layout vendor/, an ignored members
	// or replace) are the pipeline's, so every result of it carries them:
	// build, run and test too.
	for _, n := range s.manifestNotes() {
		p.notes = append(p.notes, localizeText(n, s.displayNames(nil)))
	}
	return p.result(w, s.cfg.MaxDiags)
}

// guard runs f under source.Guard; a fault becomes an E0000 of the
// pipeline, which then stops.
func (p *pipeline) guard(where string, f func()) {
	if ie := source.Guard(where, source.Span{}, f); ie != nil {
		p.diags = append(p.diags, diag.FromInternal(ie))
	}
}

// check is Check's body.
func (s *Session) check(p *pipeline) {
	w := p.w
	stdT := w.stdTree()
	pkgT := w.diskTree(s.root.dir, s.root.prefix).withOverlay(s.cfg.Overlay)
	var stdMan, pkgMan *Manifest
	ms := w.newManifestStep(stdT)
	s.ms = ms
	ok := p.step(StageManifest, func() []*diag.Diagnostic {
		var ds []*diag.Diagnostic
		stdMan, ds = ms.load(stdT, stdLayout.Manifest, "")
		if d := stdTargetMissing(w.cfg.Target, stdMan); d != nil && len(ds) == 0 {
			return []*diag.Diagnostic{d}
		}
		if s.root.kind == rootPackage {
			var pds []*diag.Diagnostic
			pkgMan, pds = ms.load(pkgT, "pkg.mk", "main")
			ds = append(ds, pds...)
			if len(ds) == 0 {
				s.deps, ds = s.resolveDeps(ms, pkgT, pkgMan, !s.noVendor)
			}
		}
		return ds
	})
	p.manifest = ms.checked
	if ok {
		s.run(p, stdT, pkgT, stdMan, pkgMan)
	}
}

// run is Check after a clean manifest step.
func (s *Session) run(p *pipeline, stdT, pkgT *srcTree, stdMan, pkgMan *Manifest) {
	w := p.w
	stdB := newBuild(w, nil)
	pkgB := newBuild(w, stdB)
	fullStd := s.fullStd || s.cfg.StdTests != "" || s.cfg.BrowseStd || s.cfg.KeepSyntax || s.root.kind == rootNone || (!s.forBuild && s.cfg.StdDir != "")
	s.lastFullStd = fullStd
	stdB.skipStdTests = !fullStd
	p.lazyStd = !fullStd
	p.testing = s.testing
	if err := stdB.listStd(stdT, stdMan); err != nil {
		p.diags = append(p.diags, listFailure(stdT, err))
		return
	}
	if err := pkgB.listPackage(pkgT, s.root, pkgMan); err != nil {
		p.diags = append(p.diags, listFailure(pkgT, err))
		return
	}
	s.runner = nil
	if s.testing {
		s.runner = pkgB.addTestRunner()
	}
	depBs, err := s.depBuilds(stdB, pkgB, pkgMan)
	if err != nil {
		p.diags = append(p.diags, err...)
		return
	}
	if s.root.kind == rootPackage {
		// Each dependency's C fields, then the package's; a C build comes
		// just before the build whose modules import it.
		w.depCImports = nil
		var withC []*build
		for i, dp := range s.deps {
			if hasC(dp.man) {
				dir, d := s.depCDir(dp)
				if d != nil {
					p.diags = append(p.diags, d)
					return
				}
				ci, ds := s.cImportsOf(stdB, dp.man, dir, dp.cid, dp.tree.prefix)
				if len(ds) > 0 {
					p.diags = append(p.diags, ds...)
					return
				}
				w.depCImports = append(w.depCImports, ci)
				if ci.b != nil {
					depBs[i].cBuild = ci.b
					withC = append(withC, ci.b)
				}
			}
			withC = append(withC, depBs[i])
		}
		depBs = withC
		links, lds := s.collectLinks(pkgMan, pkgT.osRoot)
		if len(lds) > 0 {
			p.diags = append(p.diags, lds...)
			return
		}
		w.links = links
		ci, ds := s.cImportsOf(stdB, pkgMan, pkgT.osRoot, "", "")
		if len(ds) > 0 {
			p.diags = append(p.diags, ds...)
			return
		}
		if ci != nil {
			w.cimports = ci
			if ci.b != nil {
				pkgB.cBuild, stdB.cBuild = ci.b, ci.b
				depBs = append(depBs, ci.b)
			}
		}
	}
	p.builds = append(append([]*build{stdB}, depBs...), pkgB)
	s.stdB, s.pkgB, s.pkgMan, s.depBs = stdB, pkgB, pkgMan, depBs
	if !p.front() {
		return
	}
	if w.front != nil {
		w.front.anchorFiles(w, p.builds)
	}
	if s.root.kind == rootFile {
		// Single-file check: the module is a bin root only when it declares
		// Main.
		m := pkgB.byName["main"]
		m.binRoot = s.forBuild || m.files[0].sum != nil && m.files[0].sum.Main
	} else if (s.testing || s.prop) && s.root.kind == rootDir {
		// mk test of a directory: its tests need no Main, nor do its
		// contracts under --prop.
		m := pkgB.byName["main"]
		m.binRoot = false
		for _, f := range m.files {
			m.binRoot = m.binRoot || f.sum != nil && f.sum.Main
		}
	}
	if isFree(w.cfg.Target) {
		// A freestanding bin has no Main and no System; its entry
		// is the function it exports as `export extern MkEntry`.
		for _, m := range pkgB.mods {
			m.binRoot = false
		}
	}
	core := stdB.byName[preludeModule]
	w.startResolve(core)
	for _, b := range p.builds {
		b.core = core
		b.register()
	}
	for _, b := range p.builds {
		b.preludeFor(core)
		w.cat.add(b)
	}
	p.timed("resolve", func() { stdB.resolveClosure(core) })
	p.gro = s.groJobs()
	p.modules(false, true, p.builds...)
	p.diags = append(p.diags, s.genViews()...)
}

// modules runs the per-module pipeline over the builds, in order
// (pipelineAll): stage 3 for every module, and with checkOn stages 5 to 7.
// The semantics are started first (the prelude's closure is resolved by
// then). A module whose names are not clean is not checked (namesClean),
// which changes no byte (TestPhaseRuleSameBytes): with a diagnostic
// of a phase before types anywhere, the phase rule drops every one of
// stages 5 to 7, so they are reported only when every module checked. An
// E0000 is reported whatever the phase. The instance-limit walk runs over
// the last build, the package being checked, unless a diagnostic before
// types exists. A corpus session whose Through is types walks too; its
// E0708s are types diagnostics.
func (p *pipeline) modules(testPrelude, checkOn bool, builds ...*build) {
	w := p.w
	if checkOn {
		w.startSemantics(testPrelude)
	}
	if w.front != nil {
		// A Build's walk runs beside native and joins the stores' file
		// writes first (walktail.go), so they need not end the stage.
		w.front.deferWrites = p.deferWalk
	}
	p.timed("signatures, bodies, flow", func() {
		for _, b := range builds {
			b.pipelineAll(checkOn)
		}
		// Every store is encoded by now, and written unless deferWrites
		// (lazybody.go); a hit's function results are decoded when read.
		p.diags = append(p.diags, w.front.joinCompute()...)
	})
	for _, b := range builds {
		p.diags = append(p.diags, b.stageDiags()...)
	}
	p.reached = StageResolve
	if checkOn && (!earlierThanTypes(p.diags) || w.cfg.TestHooks.CheckPastNames) {
		for _, b := range builds {
			p.diags = append(p.diags, b.checkDiags()...)
		}
		p.reached = StageBodies
		if !w.noFlow {
			p.reached = StageFlow
		}
		if p.deferWalk && len(p.diags) == 0 && len(p.gro) == 0 {
			p.startWalk(builds[len(builds)-1]) // joined by Build (walktail.go)
		} else {
			p.timed("instance-limit walk", func() {
				w.front.joinWrites() // the walk keys read the stores' ifKey
				p.diags = append(p.diags, w.walk(builds[len(builds)-1], p.diags)...)
				p.diags = append(p.diags, w.groWalk(builds[len(builds)-1], p.gro, p.diags)...)
				// The walk's prefetch (prefetchWalked) ends with the walk.
				p.diags = append(p.diags, w.front.join()...)
			})
		}
	}
	if p.tail == nil {
		// No walk beside native: no store write outlives the stage.
		p.diags = append(p.diags, w.front.join()...)
	}
	for _, b := range builds {
		b.finish()
	}
}

// earlierThanTypes reports whether ds holds a user diagnostic of a phase
// before PhaseTypes, the first phase stages 5 and 6 emit in.
func earlierThanTypes(ds []*diag.Diagnostic) bool {
	for _, d := range ds {
		if d.Phase != diag.PhaseInternal && d.Phase < diag.PhaseTypes {
			return true
		}
	}
	return false
}

// listFailure is E0001 for a tree that cannot be listed (a machine problem,
// like an unreadable file). The directory is cited by its SourceMap form,
// package-relative or std/..., never by an absolute OS path: the
// directory that failed when the error names one inside the tree, else the
// tree's root ("." for a package).
func listFailure(t *srcTree, err error) *diag.Diagnostic {
	dir := ""
	var pe *fs.PathError
	if errors.As(err, &pe) && fs.ValidPath(pe.Path) && pe.Path != "." &&
		!filepath.IsAbs(pe.Path) && !strings.ContainsAny(pe.Path, `\:`) {
		dir = pe.Path
	}
	label := strings.TrimSuffix(t.path(dir), "/")
	if label == "" {
		label = "."
	}
	return diag.Toolchain("fs", "list "+label, pathErrText(err), "cannot list source directory "+label)
}

// result orders the pipeline's diagnostics once and merges the traces.
func (p *pipeline) result(w *world, max int) Result {
	tr := &Trace{}
	for _, b := range p.builds {
		tr.FilesParsed = append(tr.FilesParsed, b.trace.FilesParsed...)
		tr.ModulesResolved = append(tr.ModulesResolved, b.trace.ModulesResolved...)
		tr.ItemsChecked = append(tr.ItemsChecked, b.trace.ItemsChecked...)
		tr.ItemsFromCache = append(tr.ItemsFromCache, b.trace.ItemsFromCache...)
		tr.Evaluations = append(tr.Evaluations, b.trace.Evaluations...)
		tr.GeneratorRuns = append(tr.GeneratorRuns, b.trace.GeneratorRuns...)
		tr.ModulesFromCache = append(tr.ModulesFromCache, b.trace.ModulesFromCache...)
		tr.ModulesWalked = append(tr.ModulesWalked, b.trace.ModulesWalked...)
	}
	if w.front != nil {
		tr.NotStored = append(tr.NotStored, w.front.notStored...)
		p.diags = append(p.diags, w.front.takeVerify()...)
	}
	w.packs.into(tr)
	p.diags = append(p.diags, w.packs.takeVerify()...)
	tr.ManifestChecked = append(tr.ManifestChecked, p.manifest...)
	tr.sort()
	return Result{Report: diag.Order(p.diags, w.sm, max), Sources: w.sm, Reached: p.reached, Trace: tr, Timings: p.times,
		SlowFuncs: w.slowFuncs(), PGOStale: w.packs.takeStale(), Notes: append([]string(nil), p.notes...)}
}

var buildID struct {
	once sync.Once
	id   source.Hash
}

// BuildID is the SHA-256 of the running executable, computed once per
// process; part of every cache key. When the executable cannot
// be read it is the hash of the empty input.
func BuildID() source.Hash {
	buildID.once.Do(func() {
		h := sha256.New()
		if exe, err := os.Executable(); err == nil {
			if f, err := os.Open(exe); err == nil {
				_, _ = io.Copy(h, f)
				f.Close()
			}
		}
		copy(buildID.id[:], h.Sum(nil))
	})
	return buildID.id
}
