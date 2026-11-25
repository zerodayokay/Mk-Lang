// Why: every stage of a build reads and writes the same tables and module
// nodes, and std and every corpus unit share one set of them; this file
// holds those shapes in one place.
//
// What: world (the session's tables, shared by std and every corpus unit),
// fileNode and modNode (one file and one module of a build, with their
// per-stage results), heldICE, and world.parallel.
//
// How: work runs in bounded workers under source.Guard; each worker writes
// only its own slot, and the slots are merged in slot order, so the output
// does not depend on scheduling. Fields shared across workers say which
// mutex guards them.

package driver

import (
	"sync"
	"sync/atomic"

	"mk/internal/ast"
	checker "mk/internal/check"
	"mk/internal/diag"
	"mk/internal/iface"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// world is what every build of one session shares: the source map, the
// interner, the item table and the resolve session (all safe for concurrent
// adds), and the worker semaphore.
type world struct {
	cfg Config
	// cimports are the package's translated C imports (cimport.go), nil
	// when it declares none; the native step links their objects.
	cimports *cImports
	// depCImports are the dependencies' C fields, in build order.
	depCImports []*cImports
	sm          *source.SourceMap
	in          *source.Interner
	items       *types.Items
	tab         *types.Table // the session's type table (stage 5 on)
	rs          *resolve.Session
	sem5        *semantics // check, lower and eval; nil until stage 5 starts
	data        dataCache  // build.File and Gen.Input reads (datafile.go)
	sem         chan struct{}
	// links are the pkg.Link values of the package graph, the root's
	// first, then each dependency's in build order (links.go).
	links []pkgLink
	// mods bounds the modules between the start of their stage 3 and the
	// end of their release (release.go): Jobs of them, so the front-end
	// output of at most that many missed modules is live at once, not of
	// every module a dependency level makes ready (perf-memory).
	mods    chan struct{}
	maxFile uint64 // source.MaxFileSize; tests lower it (TestE0101_FileTooLarge)
	cat     *catalog
	// noFlow: stage 7's verdict is not reported and poisons no body (a
	// corpus session whose Through is before flow). Stage 7 still runs
	// on the units' bodies, silently, so that the instance-limit walk, which
	// Through types reaches, has borrow's guard decisions and treats a
	// body stage 7 rejects as a leaf, as a flow-clean session would. Written
	// once before the units run.
	noFlow bool

	// slow are the functions whose check plus borrow time passed the
	// --timings threshold (timings.go), in completion order; sorted by
	// key before they reach a Result.
	slowMu sync.Mutex
	slow   []FuncTime

	// sitePaths maps each build file to its package-relative path
	// (srcTree.sitePath), written by readAll and read by codegen's Env.Path
	// from many goroutines; corpus units read their files concurrently.
	siteMu    sync.RWMutex
	sitePaths map[source.FileID]string

	// probes caches toolchain.Probe by (identity, triple) (workdir.go);
	// probeRuns counts the clang probes actually run (tests).
	probeMu   sync.Mutex
	probes    map[probeKey]toolchain.TargetFacts
	probeRuns int

	// cpuName is --cpu resolved to an explicit name once per world
	// (buildCPU, workdir.go); "" until then. cpuRuns counts the clang
	// processes ResolveCPU ran (tests).
	cpuMu   sync.Mutex
	cpuName string
	cpuRuns int

	// store is the cache store and build ID (incremental.go), opened on the
	// first native need; packs is the native part of the work log.
	store storeState
	packs packLog
	// front is the module cache of a session's Check (frontcache.go); nil
	// when off.
	front *frontCache

	// reportMu serializes the making of Main's report wrapper (mainEntry),
	// which bins built at once may each ask for.
	reportMu sync.Mutex
	// testFrames names each test clone's frame in a backtrace, by the
	// clone's key text: `test "name"` (testMain writes it before the
	// runner's native steps; codegen.Config.Frames).
	testFrames map[string]string

	// display maps a dependency's canonical id to the root package's
	// local name for it (Session.displayNames), set before a bin's native
	// steps: panic sites and run-time error reports print it
	// (sitedisplay.go). nil with no dependencies.
	display map[string]string
	// nativeObjects compiles every pack to a native object, never ThinLTO
	// bitcode, for the bin being built: a static library's host links it
	// with its own linker (libout.go). Bins build one after another.
	nativeObjects bool
}

// noteSitePath records a build file's package-relative path.
func (w *world) noteSitePath(id source.FileID, p string) {
	w.siteMu.Lock()
	w.sitePaths[id] = p
	w.siteMu.Unlock()
}

// sitePath is a file's package-relative path, '/' separated: what a panic
// site and a debug file name carry. A file readAll did not record (a
// manifest, a parse-only unit; none reaches codegen) falls back to its
// SourceMap path.
func (w *world) sitePath(id source.FileID) string {
	w.siteMu.RLock()
	p, ok := w.sitePaths[id]
	w.siteMu.RUnlock()
	if ok {
		return p
	}
	return w.sm.File(id).Path
}

func newWorld(cfg Config) *world {
	w := &world{
		cfg:     cfg,
		sm:      source.NewSourceMap(),
		in:      source.NewInterner(),
		items:   types.NewItems(),
		sem:     make(chan struct{}, cfg.Jobs),
		mods:    make(chan struct{}, max(1, cfg.Jobs)),
		maxFile: source.MaxFileSize,
	}
	if cfg.TestHooks.MaxFileSize != 0 {
		w.maxFile = cfg.TestHooks.MaxFileSize
	}
	w.cat = &catalog{in: w.in}
	w.sitePaths = map[source.FileID]string{}
	w.tab = types.NewTable(w.in, w.items, types.NewConstTable())
	return w
}

// startResolve makes the resolve session once std's prelude module is known
// (core may be nil: no prelude in this build).
func (w *world) startResolve(core *modNode) {
	rc := &resolve.Config{Interner: w.in, Sources: w.sm, Items: w.items, Catalog: w.cat}
	if core != nil {
		rc.PreludePath = preludeModule
		rc.PreludeNames = preludeNames
	}
	w.rs = resolve.NewSession(rc)
}

// fileNode is one source file of a module.
type fileNode struct {
	rel      string // relative to the module's tree root
	src      *source.File
	ast      *ast.File           // nil until parsed: a file whose summary hit is parsed only when its module misses
	sum      *iface.ParseSummary // set in the parse step, from the AST or the cache (parsesum.go)
	bodiless bool                // parsed with AllowBodiless: std files and the test prelude
	prelude  bool                // the corpus unit's test prelude
	tooLarge bool                // E0101 file-too-large: added empty, never parsed
}

// modNode is one module of a build.
type modNode struct {
	name   string // the import name: "src.util", "std.core", "main", "util" in a unit
	path   string // types.Module.Path: equal to name, except corpus units
	dir    string // tree-relative directory, "" for the root
	tree   *srcTree
	std    bool
	unsafe bool // UnsafePermitted
	// cimport: a C import's binding, c.<name> (cimport.go): unsafe, and it
	// may import std.raw for its accessors.
	cimport bool
	binRoot bool // ModuleInput.BinRoot
	// libBoundary marks a module of a package with a library bin
	// (ModuleInput.Library: its export extern functions cross a library
	// boundary, E1109).
	libBoundary bool
	// implicit: ModuleInput.Implicit, a manifest module's `pkg` for std.pkg;
	// nil elsewhere.
	implicit []resolve.Implicit
	selected bool // formed by a target selection
	// late: a std module that imports a C import, or a std module that
	// does, directly or not (cstd.go). It leaves the std build at the import step and is
	// built only when the build reaches it, after the package's C build.
	late  bool
	files []*fileNode

	imports map[string]*modNode // import path as written to its module (import step); nil entry: poisoned
	deps    []*modNode          // imports, deduplicated, in path order, plus the prelude module
	// after holds std.rt for a module outside std.rt's import closure:
	// lowering calls std.rt's trace hooks and reads std.rt.Site with no
	// import (implicitRT), so the module is scheduled after std.rt and its
	// cache key covers std.rt's closure. Never in Imports.
	after    []*modNode
	depth    int
	seesCore bool // the module sees the prelude (it is not std.core nor below it)

	id    types.ModuleID
	mod   *types.Module
	info  *resolve.Info
	diags []*diag.Diagnostic // stage 3 diagnostics, written only by the module's own worker
	gen   *genState          // stage 4 (gen.go): nil when no gen line ran
	// genRuns: the gen lines stage 4 ran (Trace.GeneratorRuns)
	genRuns []types.Key
	// genPoison: the spans of the gen lines whose run ended in poison and
	// emitted nothing; an unknown name in the module points at them.
	genPoison []source.Span
	// genRoot: for the first poisoned line that reads a module with an
	// error of its own (poisonRoot), that error, the module's path and the
	// line; nil when no line does (a type of this module it cannot see
	// before checking). An unknown name a gen line may cause is its
	// consequence, and stageDiags reports it.
	genRoot   *diag.Diagnostic
	genRootIn string
	genRootAt source.Span
	// genFail: the first gen line whose own error made it emit nothing,
	// and that error (gen.LineResult.Fail); an unknown name in the module,
	// or an importer's `m has no item`, is its consequence (noteGen).
	genFailAt source.Span
	genFail   *diag.Diagnostic
	// parsed: files of the module parsed at stage 3 (ensureParsed), for
	// Trace.FilesParsed
	parsed []string
	failed bool
	// resolved: stage 3 ran (Trace.ModulesResolved); namesBad: the module
	// or a dependency has a diagnostic before types, so it is not checked
	// (pipelineAll). Both are written by the module's own worker before
	// its importers start.
	resolved bool
	namesBad bool

	// The module cache (frontcache.go). prefix is the module prefix
	// (prefixOK: computable); api and shape its part hashes once Done
	// (partsOK); hit what a module hit loaded; phase1 a hit of the
	// prelude's closure before the semantics started; fromCache a hit
	// (Trace.ModulesFromCache). reads are the reads of the evaluations the
	// module ran, under readsMu; evalPoison: one returned poison.
	prefix     source.Hash
	prefixOK   bool
	api, shape source.Hash
	partsOK    bool
	hit        *frontHit
	phase1     bool
	fromCache  bool
	readsMu    sync.Mutex
	reads      types.ReadSet
	evals      []string     // Trace.Evaluations of the module, under readsMu
	verify     *frontVerify // MK_CACHE_VERIFY: the candidate the lookup validated (frontverify.go)
	evalPoison atomic.Bool
	done       chan struct{}

	// Stages 5 and 6 (stages.go). cmod is check's index of the module;
	// cdiags its stage 5 and 6 diagnostics and the declaration evaluations',
	// written only by the module's own worker; prep the E0000s of lowering
	// an early const inside Signatures (check's Prepare callback), under
	// prepMu; state the atomic check state of evaluator.go.
	cmod    *checker.Module
	cdiags  []*diag.Diagnostic
	checked []types.Key // the units stage 6 ran, for Trace.ItemsChecked
	// reused are the units loaded from a function result instead
	// (funccache.go, Trace.ItemsFromCache); units the record of every unit
	// of a module that missed, under readsMu; infoBy resolve's item records
	// by item (infoOnce).
	reused []types.Key
	units  map[types.ItemID]*unitRec
	// fidx is the module's function index as this build first read it
	// (funcbundle.go), once.
	// ifKey is the module's interface entry this build (a hit's, or the one
	// store wrote), when ifKeyOK (walkcache.go).
	ifKey    source.Hash
	ifKeyOK  bool
	fidxOnce sync.Once
	fidx     *funcIndex
	// pidx is the module's parse index as this build first read it in the
	// parse step (parsesum.go), once.
	pidxOnce sync.Once
	pidx     *parseIndex
	// names answers the namespace reads (ReadNoItem, ReadExports) of m's
	// unit lookups during its check, so the closure's namespaces are built
	// once per module, not once per unit (lookupWorld); under namesMu,
	// dropped when the check ends.
	namesMu  sync.Mutex
	names    *iface.SessionWorld
	infoOnce sync.Once
	infoBy   map[types.ItemID]*resolve.ItemInfo
	// consts are the module's consts by name, for the function prefix's
	// same-module const part (funccache.go constPart), once; contractNames
	// are the identifiers of the module's requires and ensures clauses by
	// the name of the function that states them, for the same part.
	constOnce     sync.Once
	consts        map[string]*constTok
	contractNames map[string][]string
	prepMu        sync.Mutex
	prep          []*diag.Diagnostic
	// fdiags are the module's stage-7 diagnostics (borrow), written only by
	// its worker; prepFlow those of Prepared early consts, under prepMu.
	// They do not fail the module for the evaluator: a body borrow rejects is
	// poisoned on its own (lowerBody), and the module's types are sound, so
	// its importers are still lowered and checked.
	fdiags   []*diag.Diagnostic
	prepFlow []*diag.Diagnostic
	// prepHeld are lower's E0000s on Prepared early consts that read a
	// const with no type, under prepMu (lowered.held).
	prepHeld []heldICE
	// prepFailed are the Prepared early consts whose lowering or stage 7
	// reported (an E0000, or E0402 and its kind), under prepMu: they account
	// for a held E0000 on their readers (accounting).
	prepFailed []types.ItemID
	state      modState
	// genKnown and genLines: the number of gen lines resolve's Info held,
	// kept when release drops the Info (release.go noGenLines).
	genKnown bool
	genLines int
}

// addPrepDiag records an E0000 from lowering an early const (Prepare).
func (m *modNode) addPrepDiag(d *diag.Diagnostic) {
	m.prepMu.Lock()
	m.prep = append(m.prep, d)
	m.prepMu.Unlock()
}

// heldICE is lower's E0000 on a body that reads consts with no type
// (reads), held until the module knows whether those consts account for it.
type heldICE struct {
	d     *diag.Diagnostic
	reads []types.ItemID
}

// addHeld records a held E0000 from lowering an early const (Prepare).
func (m *modNode) addHeld(h heldICE) {
	m.prepMu.Lock()
	m.prepHeld = append(m.prepHeld, h)
	m.prepMu.Unlock()
}

// addPrepFailed records a Prepared early const whose lowering or stage 7
// reported (prepFailed).
func (m *modNode) addPrepFailed(id types.ItemID) {
	m.prepMu.Lock()
	m.prepFailed = append(m.prepFailed, id)
	m.prepMu.Unlock()
}

// addPrepFlow records the stage-7 diagnostics of a Prepared early const.
func (m *modNode) addPrepFlow(ds []*diag.Diagnostic) {
	m.prepMu.Lock()
	m.prepFlow = append(m.prepFlow, ds...)
	m.prepMu.Unlock()
}

// parallel runs f(0..n-1) on at most Jobs workers, each body under
// source.Guard. A fault in one item becomes an E0000 in its slot; the others
// run on. Results go to slots the callers index by i.
func (w *world) parallel(n int, where func(int) string, f func(int)) []*diag.Diagnostic {
	faults := make([]*source.InternalError, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		w.sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-w.sem }()
			faults[i] = source.Guard(where(i), source.Span{}, func() { f(i) })
		}(i)
	}
	wg.Wait()
	var out []*diag.Diagnostic
	for _, ie := range faults {
		if ie != nil {
			out = append(out, diag.FromInternal(ie))
		}
	}
	return out
}
