// Why: mk corpus judges many small units against one std; checking
// std once and sharing its tables across every unit is what makes that
// fast, and the judge needs to know how far std lets a unit be judged.
//
// What: CorpusSession (NewCorpusSession, Std, StdState, StdMissing,
// Through, CheckUnit), the unit types UnitInput, UnitFile, UnitMode and
// UnitOutcome (BlockResult), and the unit pipeline checkUnit and parseOnly.
//
// How: each unit is one module (or one package of modules) in the
// session's shared tables, under a distinct name, so units check in
// parallel. Through is the last phase std's readiness allows, and only the
// unit's diagnostics up to it are reported. The corpus protocol itself
// lives in internal/corpus.

package driver

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/syntax"
	"mk/internal/toolchain"
)

// CorpusSession loads and checks std once and then checks many small units
// against it concurrently. Std is read-only after NewCorpusSession returns;
// each unit gets modules of its own in the shared tables.
type CorpusSession struct {
	w       *world
	std     *build
	stdRes  Result
	through diag.Phase
	missing []string // prelude names std.core does not export, in the prelude list's order
	state   StdState
	// ms is the manifest step: std.pkg checked once, std's manifest, then each
	// unit's pkg.mk.
	ms *manifestStep

	mu    sync.Mutex
	names map[string]bool // unit names already used

	// The toolchain for UnitBuild and UnitRun (corpus_build.go), found once.
	toolsOnce sync.Once
	tl        *toolchain.Tools
	tlErr     *diag.Diagnostic

	// sound is RunUnit's record of each checked unit (sound.go).
	sound soundUnits
}

// NewCorpusSession loads and checks the standard library once, through every
// implemented stage, for cfg.Target. cfg.Root is ignored. The error is a
// *UsageError.
func NewCorpusSession(cfg Config) (*CorpusSession, error) {
	cfg, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	w := newWorld(cfg)
	c := &CorpusSession{w: w, names: map[string]bool{}}
	w.sysviews = &sysViews{dir: c.cacheDir(), stdOnly: true}
	p := &pipeline{w: w}
	stdT := w.stdTree()
	var stdMan *Manifest
	stdB := newBuild(w, nil)
	c.std = stdB
	c.ms = w.newManifestStep(stdT)
	// One Guard over std's sequential steps, as in Session.Check: an ICE
	// there is an E0000 in Std(), never a Go panic.
	p.guard("corpus std", func() {
		if !p.step(StageManifest, func() []*diag.Diagnostic {
			var ds []*diag.Diagnostic
			stdMan, ds = c.ms.load(stdT, stdLayout.Manifest, "")
			if d := stdTargetMissing(w.cfg.Target, stdMan); d != nil && len(ds) == 0 {
				return []*diag.Diagnostic{d}
			}
			return ds
		}) {
			return
		}
		if err := stdB.listStd(stdT, stdMan); err != nil {
			p.diags = append(p.diags, listFailure(stdT, err))
		} else {
			p.builds = []*build{stdB}
			if p.front() {
				core := stdB.byName[preludeModule]
				stdB.core = core
				w.startResolve(core)
				stdB.register()
				stdB.preludeFor(core)
				w.cat.add(stdB)
				stdB.resolveClosure(core)
				p.modules(true, true, stdB)
			}
		}
	})
	if w.rs == nil { // std stopped before stage 3: units still resolve, with no prelude
		stdB.core = nil
		w.startResolve(nil)
	}
	p.manifest = c.ms.checked
	c.stdRes = p.result(w, diag.NoCap)
	c.missing = missingPrelude(stdB, w)
	c.state = StdState{Report: c.stdRes.Report, Missing: stdMissingAll(c.missing, stdB)}
	c.through = c.computeThrough()
	// Units run no stage past Through: stage 7 only once it reaches
	// flow. Written before any CheckUnit, which the caller starts after
	// NewCorpusSession returns.
	w.noFlow = c.through < diag.PhaseFlow
	return c, nil
}

// Std is the report of checking std and std's manifest, for the corpus
// report to print when it is not clean.
func (c *CorpusSession) Std() Result { return c.stdRes }

// StdMissing lists the prelude names (the list doc/reference.md gives) that
// std's prelude module does not declare as exported top-level items, in the
// list's order; all of them while std.core is absent. Empty once the
// prelude is complete. (The prelude part of StdState.Missing.)
func (c *CorpusSession) StdMissing() []string { return slices.Clone(c.missing) }

// StdState is what a corpus session knows about std's readiness, read by
// the corpus judge.
type StdState struct {
	Report diag.Report // std's own report through every available stage (Std().Report)
	// Missing holds the prelude names std.core does not export
	// and the std.<m> module paths of doc/std/README.md's
	// layers 0 to 2 that this std does not form; byte-sorted. Empty once std
	// is complete.
	Missing []string
}

// StdState returns the session's StdState; the caller may keep it.
func (c *CorpusSession) StdState() StdState {
	return StdState{Report: c.state.Report, Missing: slices.Clone(c.state.Missing)}
}

// stdLayerModules are the module paths of doc/std/README.md's layers 0 to 2, in the
// table's order.
var stdLayerModules = []string{"std.target", "std.core", "std.math", "std.simd", "std.bits", "std.atomic",
	"std.mem", "std.coll", "std.text",
	"std.rt", "std.sys", "std.task", "std.fs", "std.net", "std.time", "std.proc", "std.sync", "std.log"}

// stdMissingAll is StdState.Missing: the missing prelude names plus the
// layer 0 to 2 modules stdB does not form, sorted.
func stdMissingAll(prelude []string, stdB *build) []string {
	out := slices.Clone(prelude)
	for _, m := range stdLayerModules {
		if stdB.byName[m] == nil {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// Through is the last phase a unit can be judged through in this session:
// the earliest of Implemented(), the phase before the earliest phase of any
// std diagnostic (parse for an E0000), and PhaseParse while std's prelude
// module does not declare every name of the prelude list other than the
// reserved Path. CheckUnit runs no stage beyond it.
func (c *CorpusSession) Through() diag.Phase { return c.through }

func (c *CorpusSession) computeThrough() diag.Phase {
	t := Implemented()
	r := c.stdRes.Report
	for _, e := range r.Diags {
		if e.Diag.Phase == diag.PhaseInternal {
			return diag.PhaseParse
		}
	}
	if r.HasPhase {
		t = min(t, max(r.Phase-1, diag.PhaseParse))
	}
	if len(c.missing) > 0 {
		t = diag.PhaseParse
	}
	return t
}

// missingPrelude is the prelude names std.core does not export.
func missingPrelude(stdB *build, w *world) []string {
	declared := map[string]bool{}
	if core := stdB.byName[preludeModule]; core != nil {
		for _, f := range core.files {
			if f.sum == nil {
				continue
			}
			for _, n := range f.sum.Exports {
				declared[n] = true
			}
		}
	}
	var out []string
	for _, n := range preludeNames {
		if !declared[n] {
			out = append(out, n)
		}
	}
	return out
}

// UnitFile is one source file of a unit, LF-normalized, path relative to the
// unit's virtual root ("block.mk", "prelude.mk", "util/util.mk").
type UnitFile struct{ Path, Text string }

// UnitInput is one corpus unit: internal/corpus's unit, copied
// field for field by corpus.DriverCompiler (driver cannot import corpus).
type UnitInput struct {
	Name      string     // "doc/reference.md#17": the virtual root and the module path
	Files     []UnitFile // in byte order of Path
	Prelude   string     // the Path of the test prelude among Files, or ""
	ParseOnly bool       // verdict parse: stage 2 only, no prelude, no std
	Mode      UnitMode
	// Design marks a block from the documentation (doc/*.md): with no pkg.mk
	// of its own, the unit is judged as the one module of a package whose
	// pkg.mk lists it in unsafe_modules, so its unsafe, section and extern
	// var draw no E1102. A unit with a pkg.mk follows that manifest.
	Design bool
}

// UnitMode says how far past checking a unit goes.
type UnitMode uint8

const (
	UnitCheck UnitMode = iota // check only
	UnitBuild                 // also objects through clang -c (corpus_build.go)
	UnitRun                   // also link and run Main (corpus_build.go)
)

// UnitOutcome is what the corpus judge's outcome is filled from.
type UnitOutcome struct {
	Report     diag.Report              // after diag.Order, no cap
	Sources    *source.SourceMap        // the session's map; std files included
	Paths      map[source.FileID]string // unit-relative path of every unit file; read by key only
	Through    diag.Phase               // c.Through() when the unit ran
	Built      bool                     // UnitBuild, UnitRun: objects produced
	Ran        bool                     // UnitRun: a Main was linked and started
	RunStatus  int                      // the exit status; -1 when killed
	TimedOut   bool                     // the run exceeded 10 s and was killed
	RunSkipped string                   // why a unit with Main was built but not run
	// Skipped holds the stages this unit entered that this build does not
	// have. Every stage exists: a compile-time evaluation of a generic gets
	// mono's instance body, and the instance-limit walk runs over the
	// unit's limit roots once Through reaches types (walk.go), so it is
	// never set by the driver.
	Skipped StageSet
}

// BlockResult is the corpus judge's name for a unit's outcome
// (its JudgeUnit and Judge take a driver.BlockResult); it is UnitOutcome.
type BlockResult = UnitOutcome

// CheckUnit compiles one unit against the session's std through stage
// min(7b, Through()), plus the instance-limit walk once Through() reaches
// types (with Through types, stage 7 runs silently for the walk's guard
// decisions; world.noFlow). A ParseOnly unit is parsed alone, with no std and no
// prelude. Otherwise the unit's files form a package rooted at u.Name/: its
// root directory is the module u.Name, a subdirectory d the module
// u.Name/d, imported inside the unit as d with '/' written '.'. No
// unit module is a bin root. Only the unit's own diagnostics are reported,
// and only those of phases up to Through, plus any E0000 and the build's
// E0001. Safe for concurrent use; each call needs a distinct u.Name.
func (c *CorpusSession) CheckUnit(u UnitInput) UnitOutcome {
	out := UnitOutcome{Sources: c.w.sm, Paths: map[source.FileID]string{}, Through: c.through, RunStatus: -1}
	var ds []*diag.Diagnostic
	if ie := source.Guard("corpus unit "+u.Name, source.Span{}, func() {
		if !c.claim(u.Name) {
			source.ICEf(source.Span{}, "unit name %s used twice in one corpus session", u.Name)
		}
		if u.ParseOnly {
			ds = c.parseOnly(u, out.Paths)
		} else {
			ds = c.checkUnit(u, &out)
		}
	}); ie != nil {
		ds = append(ds, diag.FromInternal(ie))
	}
	// An E0000 (PhaseInternal) or an E0001 (default phase native, past any
	// Through) is kept whatever the phase: "An E0000 or E0001 from the build
	// part appears in Report like any other diagnostic". The build part runs
	// only on a unit with no other diagnostic, so its E0001 is alone in the
	// report.
	ds = slices.DeleteFunc(ds, func(d *diag.Diagnostic) bool {
		return d.Phase != diag.PhaseInternal && d.Code != diag.E0001 && d.Phase > c.through
	})
	out.Report = diag.Order(ds, c.w.sm, diag.NoCap)
	c.sound.note(u, &out)
	return out
}

func (c *CorpusSession) claim(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names[name] {
		return false
	}
	c.names[name] = true
	return true
}

// parseOnly is a verdict parse unit: the parse step alone (names may be
// undefined), bodiless functions accepted.
func (c *CorpusSession) parseOnly(u UnitInput, paths map[source.FileID]string) []*diag.Diagnostic {
	var ds []*diag.Diagnostic
	for _, f := range u.Files {
		src := c.w.sm.Add(u.Name+"/"+f.Path, []byte(f.Text))
		paths[src.ID] = f.Path
		_, pd := syntax.ParseFile(src, c.w.in, syntax.Options{AllowBodiless: true})
		ds = append(ds, pd...)
	}
	return ds
}

// checkUnit runs a unit's pipeline as far as Through.
func (c *CorpusSession) checkUnit(u UnitInput, out *UnitOutcome) []*diag.Diagnostic {
	w := c.w
	paths := out.Paths
	t := &srcTree{prefix: u.Name + "/", mem: map[string]string{}}
	for _, f := range u.Files {
		t.mem[f.Path] = f.Text
	}
	p := &pipeline{w: w}
	var man *Manifest
	if _, ok := t.mem["pkg.mk"]; ok {
		if !p.step(StageManifest, func() []*diag.Diagnostic {
			var ds []*diag.Diagnostic
			man, ds = c.ms.load(t, "pkg.mk", u.Name)
			return ds
		}) {
			c.notePaths(u, paths)
			return p.diags
		}
	}
	b := newBuild(w, c.std)
	b.unit = true
	if err := b.listTree(t, man, u.Name); err != nil {
		return []*diag.Diagnostic{listFailure(t, err)}
	}
	for _, m := range b.mods {
		m.path = unitModulePath(u.Name, m.dir)
		for _, f := range m.files {
			if f.rel == u.Prelude && u.Prelude != "" {
				f.prelude, f.bodiless = true, true
			}
		}
	}
	b.permitUnsafe(man)
	b.markLibrary(man)          // E1109 judges its export externs
	if man == nil && u.Design { // a design block: its module may use unsafe
		for _, m := range b.mods {
			m.unsafe = true
		}
	}
	b.findCaseDups()
	var depBs []*build
	if man != nil && len(man.Deps)+len(man.TestDeps) > 0 {
		// The unit's dependencies (corpus_deps.go).
		var ds []*diag.Diagnostic
		if depBs, ds = c.unitDeps(u, t, man, b); len(ds) > 0 {
			c.notePaths(u, paths)
			return ds
		}
	}
	p.builds = append(depBs, b)
	defer c.notePaths(u, paths)
	if c.through == diag.PhaseParse {
		// Through parse: read and parse only; nothing past the parse step is judged.
		var tooLarge []*diag.Diagnostic
		if p.step(StageLoad, func() []*diag.Diagnostic {
			var fails []*diag.Diagnostic
			fails, tooLarge = b.readAll()
			return fails
		}) {
			p.step(StageParse, func() []*diag.Diagnostic { return append(tooLarge, b.parseAll()...) })
		}
		return p.diags
	}
	if !p.front() || c.through < diag.PhaseNames {
		return p.diags
	}
	for _, pb := range p.builds {
		pb.core = c.std.core
		pb.register()
	}
	for _, pb := range p.builds {
		pb.preludeFor(c.std.core)
	}
	p.modules(true, c.through >= diag.PhaseTypes && w.sem5 != nil, p.builds...)
	if u.Mode >= UnitBuild && c.through == diag.PhaseFlow && len(p.diags) == 0 {
		p.diags = append(p.diags, c.buildUnit(u, b, out)...)
	}
	return p.diags
}

// notePaths maps every unit file the source map holds back to its
// unit-relative path.
func (c *CorpusSession) notePaths(u UnitInput, paths map[source.FileID]string) {
	for _, f := range u.Files {
		if src, ok := c.w.sm.ByPath(u.Name + "/" + f.Path); ok {
			paths[src.ID] = f.Path
		}
	}
}

// String names the unit mode.
func (m UnitMode) String() string {
	switch m {
	case UnitCheck:
		return "check"
	case UnitBuild:
		return "build"
	case UnitRun:
		return "run"
	}
	return fmt.Sprintf("mode%d", uint8(m))
}

// unitModulePath is the module path of directory dir of unit name.
func unitModulePath(name, dir string) string {
	if dir == "" {
		return name
	}
	return name + "/" + strings.TrimSuffix(dir, "/")
}
