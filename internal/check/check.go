// Why: stage 5 builds the checked index of every module and stage 6 checks
// each body; both need one shared session, a fixed unit order and a guard
// that turns one internal error into one E0000 without stopping the rest.
//
// What: Config, Checker, ModuleInput and Module; Signatures (stage 5 on one
// module, in steps: declare, members, impls, formats, queue, flows, early
// consts, entry), Load (the same index rebuilt from cached Decls), Units,
// Early, Main and Check (stage 6 on one unit).
//
// How: each item runs under source.Guard. The module map is copy-on-write,
// so readers take no lock. fileIndex walks each file once and keeps, per
// node, the least and greatest NodeID of its subtree, so a unit's table
// range needs no second walk.

package check

import (
	"sync"
	"sync/atomic"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// Config is what every unit of checking reads. It is immutable after New.
type Config struct {
	Interner *source.Interner
	Types    *types.Table
	Items    *types.Items
	Consts   *types.ConstTable
	// Known names the library items that rules key on (Arena, Cell, Scope,
	// Slice, Format, Eq, ...). Zero fields mean "not loaded" (a test without
	// the prelude), and the rules keyed on them do not fire. Nil is all zero.
	Known *types.KnownItems
	// Eval is the only way check runs code (types.Evaluator). Nil makes
	// every compile-time evaluation E0901 with refused "no-evaluator".
	Eval    types.Evaluator
	Target  types.Target  // the build's target: Target facts, int width
	Profile types.Profile // contracts are type-checked in every profile
	// TestPrelude says a non-std module may hold bodiless non-extern
	// functions: the corpus's test prelude file. With it
	// false such a function reaching check is an ICE.
	TestPrelude bool
	// Prepare makes a same-module const initializer that a type needs
	// runnable in stage 5. Nil in tests.
	Prepare func(b *Body)
	// ParamClass is the C ABI class of a parameter of type t on target
	// (the driver's abi.Classify, which check cannot import), over check's
	// layouts. become's register rule asks it for every built target and
	// passes in registers only what each answers Direct (E0313, become.go).
	// Nil in tests: the rule then judges by size alone.
	ParamClass func(tt *types.Table, layout func(types.TypeID) *types.Layout, target types.Target, t types.TypeID) types.AbiClass
	// ParamByval reports whether target's C ABI lowering of a signature
	// with parameter types params and result type result passes parameter
	// i as a byval copy in memory (the driver's abi.Lower, which accounts
	// for registers running out). A `become` through a fn value cannot pass
	// such a parameter on (E0313, become.go). Nil in tests: more than 16
	// bytes is byval.
	ParamByval func(tt *types.Table, layout func(types.TypeID) *types.Layout, target types.Target, params []types.TypeID, result types.TypeID, i int) bool
	// Imports is unused: Load reads types.Module.Imports, which the driver
	// sets before any stage reads it. Kept only so the driver, which still
	// sets it, builds; it may stop setting it.
	Imports func(types.ModuleID) []types.ModuleID
	// CImport reports a module that is a C import's binding (c.<name>):
	// reading a function pointer out of its structs needs unsafe (E1101
	// c-fn-pointer). Nil: none is.
	CImport func(types.ModuleID) bool
}

// Checker holds the per-module results of one build. Modules are added in
// import order; a module's imports must be present (checked or loaded) before
// Signatures runs on it.
type Checker struct {
	cfg   *Config
	known types.KnownItems
	// freeItem caches std.core's interface Free once found (freebound.go);
	// 0 until then, never a cached miss.
	freeItem atomic.Uint32

	// mods maps a module to its index; lookup only, never iterated into
	// output. Readers (every member lookup of every worker) take no lock:
	// a write, once per module, copies the map under mu and publishes the
	// copy, and a published map is never written again.
	mu   sync.Mutex
	mods atomic.Pointer[map[types.ModuleID]*Module]

	memo factsMemo

	// genMemo caches genType per TypeID; answers depend only on
	// declarations, so concurrent units may share it.
	genMemo sync.Map

	// hookItem, when set by a test, runs at the start of every item's
	// stage-5 work, inside its guard (TestGuardTurnsICEIntoE0000).
	hookItem func(types.ItemID)

	// fieldClauses are E0501's `from p.f` fixes by function (fromfix.go
	// FieldClause), written once per function at stage 5.
	fieldClauses sync.Map
}

// New makes a Checker. It does not copy cfg's tables.
func New(cfg *Config) *Checker {
	c := &Checker{cfg: cfg}
	c.mods.Store(&map[types.ModuleID]*Module{})
	if cfg.Known != nil {
		c.known = *cfg.Known
	}
	return c
}

// ModuleInput is one module ready for stage 5: its parsed files in byte
// order of their names and resolve's answer for them.
type ModuleInput struct {
	Module types.ModuleID
	Files  []*ast.File
	Info   *resolve.Info
	// BinRoot is set by the driver for a bin root: check then requires Main
	// and judges its signature. A library module has no entry rule.
	BinRoot bool
	// Library is set by the driver for a module of a package with a
	// library bin: each export extern function there crosses the library
	// boundary, and a parameter or result with no C form is E1109
	// (cform.go's HasCForm).
	Library bool
}

// Module is the checked index of one module. Everything in it is derived
// from types.Items and the module's Decls, so it is never encoded by iface.
type Module struct {
	ID    types.ModuleID
	Diags []*diag.Diagnostic // stage 5 diagnostics, unsorted (diag.Order sorts)
	// Reads holds each item's stage-5 read set, by item, in item order.
	Reads []ItemReads

	c    *Checker
	mod  *types.Module
	in   *ModuleInput
	info *resolve.Info
	// files indexes every node of each of Info.Files by NodeID.
	files []*fileIndex
	// byItem finds an item's resolution; lookup only.
	byItem map[types.ItemID]*resolve.ItemInfo

	units []types.ItemID
	early []*Body
	// caps are the capturing literals' handoff records (captures.go), made
	// on first use by the enclosing or the literal unit.
	capMu sync.Mutex
	caps  map[types.ItemID]*captureRec
	// litE0601 are the E0601s of fn literals whose result has several
	// possible sources, by literal item: written in stage 5 (runFlows),
	// read only by the literal's enclosing unit (fnLit).
	litE0601 map[types.ItemID][]*diag.Diagnostic
	// deferred are the early consts not handed to Prepare (early.go).
	deferred []types.ItemID
	main     types.ItemID

	// d is the stage-5 working state (decl.go), nil once Signatures returns.
	d *declIndex

	// final is set once step 3 of Signatures is done: from then on the
	// facts of the module's type constructors are final and may be memoized.
	final bool
	// asserts are the unsafe impl Send and Share items per constructor of
	// this module; sliceImpls the constructors with impl Slice[T].
	// Both are filled in step 3 (impls.go) and read-only after.
	asserts    map[types.ItemID]assertPair
	sliceImpls map[types.ItemID]bool

	// deps are the modules this one imports, directly or not, and the
	// prelude's (members.go); lookup only.
	deps map[types.ModuleID]bool
	// open holds the scans of the modules its matches read for open
	// error-set aliases (open.go), one per module, under openMu.
	openMu sync.Mutex
	open   map[types.ModuleID]*openScan
	// members are the declared member sets of the module's constructors and,
	// in a std module, of builtin owner types (step 2, members.go).
	members     map[types.ItemID]*memberSet
	primMembers map[types.TypeID]*memberSet
	// implTab is the module's impl table, one impl per (interface,
	// constructor) pair, and implList its impls in item order (step 3,
	// impls.go). shadowed are impl methods reported E0206 at their
	// declaration; implFlows the E0608 comparisons step 5 runs.
	implTab  map[implKey]types.ItemID
	implList []types.ItemID
	// errFmtGap is, for an error of this module whose provided Format was
	// dropped, the index of the first field lacking Format;
	// written in stage 5 (errfmt.go), read-only after.
	errFmtGap map[types.ItemID]int
	// writtenFmt are the E0704s of written `impl Format` for an error of
	// this module, completed once errorFormats decides.
	writtenFmt []writtenFmt
	shadowed   map[types.ItemID]bool
	implFlows  []implFlowJob
	// impls is the lookup state derived from the final tables (impls.go
	// implState: the impl index, the dependency list, member memos).
	impls implState
	// ices are the E0000s guard recorded, also in Diags (Load keeps only
	// these).
	ices []*diag.Diagnostic
	// done is set when Signatures returns; modulesInOrder reads only done
	// modules, so no query reads a sibling's tables while they are written.
	done atomic.Bool
	// ids are resolve's item list, kept by Release (release.go) when it
	// drops the Info; nil before.
	ids []types.ItemID
}

// assertPair is the unsafe impl Send and unsafe impl Share of one
// constructor, NoItem when absent.
type assertPair struct{ send, share types.ItemID }

// ItemReads is one item's stage-5 read set.
type ItemReads struct {
	Item  types.ItemID
	Reads types.ReadSet
}

// fileIndex is one file's nodes by NodeID (slot 0 unused), built by one
// walk of the file (newFileIndex). lo and hi are, per node, the least and
// the greatest NodeID in its subtree, so a unit's table range (newBody)
// needs no second walk. early holds, in walk order, the nodes earlySet
// inspects, so stage 5 needs no second walk for it either.
type fileIndex struct {
	file  *ast.File
	info  *resolve.FileInfo
	nodes []ast.Node
	lo    []ast.NodeID
	hi    []ast.NodeID
	early []ast.Node
}

func newFileIndex(fi *resolve.FileInfo) *fileIndex {
	n := fi.File.NodeCount + 1
	x := &fileIndex{file: fi.File, info: fi, nodes: make([]ast.Node, n), lo: make([]ast.NodeID, n), hi: make([]ast.NodeID, n)}
	v := &indexer{x: x, stack: make([]ast.NodeID, 0, 64)}
	ast.Walk(v, fi.File)
	return x
}

// indexer is newFileIndex's visitor: stack holds the IDs of the open
// nodes, and a closing node folds its range into its parent's.
type indexer struct {
	x     *fileIndex
	stack []ast.NodeID
}

func (v *indexer) Visit(n ast.Node) ast.Visitor {
	x := v.x
	if n == nil {
		top := v.stack[len(v.stack)-1]
		v.stack = v.stack[:len(v.stack)-1]
		if k := len(v.stack); k > 0 {
			p := v.stack[k-1]
			x.lo[p], x.hi[p] = min(x.lo[p], x.lo[top]), max(x.hi[p], x.hi[top])
		}
		return nil
	}
	id := n.ID()
	if int(id) >= len(x.nodes) {
		source.ICEf(n.Span(), "check: node %d past NodeCount %d", id, x.file.NodeCount)
	}
	x.nodes[id] = n
	x.lo[id], x.hi[id] = id, id
	v.stack = append(v.stack, id)
	switch n.(type) {
	case *ast.ArrayType, *ast.RepeatLit, *ast.ConstDecl, *ast.CallExpr, *ast.Contract, *ast.IndexExpr,
		*ast.NamePattern, *ast.PathPattern:
		x.early = append(x.early, n)
	}
	return v
}

// rangeOf is the least and greatest NodeID of the subtree under n, a node
// of this file.
func (x *fileIndex) rangeOf(n ast.Node) (lo, hi ast.NodeID) {
	id := n.ID()
	if int(id) >= len(x.nodes) || x.nodes[id] != n {
		source.ICEf(n.Span(), "check: node %d is not indexed in %s", id, x.file.Path)
	}
	return x.lo[id], x.hi[id]
}

// node returns the node with this ID, or an ICE.
func (x *fileIndex) node(id ast.NodeID) ast.Node {
	if id == 0 || int(id) >= len(x.nodes) || x.nodes[id] == nil {
		source.ICEf(x.file.Span(), "check: no node %d in %s", id, x.file.Path)
	}
	return x.nodes[id]
}

// Signatures runs stage 5 on one module. Each item runs under
// source.Guard, so one ICE becomes one E0000 and the other items still
// report.
func (c *Checker) Signatures(in *ModuleInput) *Module {
	m := c.newModule(in)
	m.initDecls()
	m.forEachItem(func(it *types.Item) { m.declare(it) }) // step 1
	m.memberPass()                                        // step 2
	m.implPass()                                          // step 3
	m.errorFormats()                                      // provided Format methods
	m.final = true
	m.runQueue() // step 4
	m.shapePass()
	m.exportChecks()
	m.runFlows() // step 5
	m.implFlowChecks()
	m.initEarly() // before typeConsts, so typing evaluates early consts
	m.typeConsts()
	m.checkEarly()
	m.entry() // step 6
	m.computeUnits()
	for _, ii := range m.info.Items {
		if rs := m.d.reads[ii.Item]; rs != nil && rs.Len() > 0 {
			m.Reads = append(m.Reads, ItemReads{Item: ii.Item, Reads: *rs})
		}
	}
	m.d = nil
	m.done.Store(true)
	return m
}

// Load registers a module whose stage 3 to 7b results came from the cache:
// its Items and Decls are already in Config.Items (iface decoded them). It
// rebuilds the derived index (deps, member sets, impl table, assertions,
// Slice impls, the impl methods shadowed by E0206) from the decoded Decls
// and reads no source; the module's imports must be present first. Its
// diagnostics, read sets, Units, Early and Main are the cache's, so the
// returned Module has none of them (Diags holds only an E0000 should the
// rebuild hit an ICE). Its imports are types.Module.Imports.
// TestLoadEqualsSignatures pins that an importer
// checks the same against Load as against Signatures.
func (c *Checker) Load(id types.ModuleID) *Module {
	m := &Module{ID: id, c: c, mod: c.cfg.Items.Module(id), byItem: map[types.ItemID]*resolve.ItemInfo{},
		members: map[types.ItemID]*memberSet{}, primMembers: map[types.TypeID]*memberSet{},
		implTab: map[implKey]types.ItemID{}, shadowed: map[types.ItemID]bool{},
		asserts: map[types.ItemID]assertPair{}, sliceImpls: map[types.ItemID]bool{}}
	source.Assertf(m.mod != nil, source.Span{}, "check.Load: no module %d", id)
	m.computeDeps()
	c.addModule(id, m)
	for _, iid := range m.itemIDs() {
		it := c.cfg.Items.Get(iid)
		if d, ok := it.Decl.(*types.ImplDecl); ok {
			m.guard(it, func() { m.registerImpl(it, d) })
		}
	}
	m.memberPass()
	m.implPass()
	m.final = true
	// The rebuild re-derives the module's own stage-5 diagnostics (orphan
	// methods, coherence, collisions); those are the cache's to replay.
	m.Diags = m.ices
	m.done.Store(true)
	return m
}

// itemIDs are the module's items in key order: resolve's list for a
// checked module, the decoded table's for a loaded one.
func (m *Module) itemIDs() []types.ItemID {
	if m.ids != nil {
		return m.ids
	}
	info := m.info // read once: Release clears it (release.go)
	if info == nil {
		return m.mod.Items
	}
	out := make([]types.ItemID, len(info.Items))
	for i, ii := range info.Items {
		out[i] = ii.Item
	}
	return out
}

func (c *Checker) newModule(in *ModuleInput) *Module {
	source.Assertf(in.Info != nil && in.Info.Module == in.Module, source.Span{}, "check: module input without its resolve.Info")
	m := &Module{ID: in.Module, c: c, mod: c.cfg.Items.Module(in.Module), in: in, info: in.Info,
		byItem:  make(map[types.ItemID]*resolve.ItemInfo, len(in.Info.Items)),
		members: map[types.ItemID]*memberSet{}, primMembers: map[types.TypeID]*memberSet{},
		implTab: map[implKey]types.ItemID{}, shadowed: map[types.ItemID]bool{}}
	for _, fi := range in.Info.Files {
		m.files = append(m.files, newFileIndex(fi))
	}
	for _, ii := range in.Info.Items {
		m.byItem[ii.Item] = ii
	}
	m.computeDeps()
	c.addModule(in.Module, m)
	return m
}

// addModule publishes a copy of the module map with id bound to m.
func (c *Checker) addModule(id types.ModuleID, m *Module) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := *c.mods.Load()
	next := make(map[types.ModuleID]*Module, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[id] = m
	c.mods.Store(&next)
}

// module returns a module already given to Signatures, or nil.
func (c *Checker) module(id types.ModuleID) *Module {
	return (*c.mods.Load())[id]
}

// forEachItem runs f on every item of the module in item (key) order, each
// under its own guard.
func (m *Module) forEachItem(f func(it *types.Item)) {
	for _, ii := range m.info.Items {
		it := m.c.cfg.Items.Get(ii.Item)
		m.guard(it, func() { f(it) })
	}
}

// guard runs f for item it under source.Guard and records an ICE as E0000.
func (m *Module) guard(it *types.Item, f func()) {
	if ie := source.Guard(it.Key.String(), it.Span, f); ie != nil {
		d := diag.FromInternal(ie)
		for _, o := range m.ices {
			if o.At == d.At && o.Message == d.Message {
				// The same fault met again: an early const's typing and its
				// check in the sweep both evaluate the use that faults
				// (TestEarlyICEReportedOnce).
				return
			}
		}
		m.Diags = append(m.Diags, d)
		m.ices = append(m.ices, d)
	}
}

// nodeOf returns an item's declaring node and its file.
func (m *Module) nodeOf(id types.ItemID) (ast.Node, *fileIndex) {
	ii := m.byItem[id]
	if ii == nil {
		source.ICEf(source.Span{}, "check: item %d is not in module %s", id, m.mod.Path)
	}
	fx := m.files[ii.File]
	return fx.node(ii.Node), fx
}

// unitGroup is an item's place in the Units order, or -1 when the item
// is not a stage-6 unit.
func (m *Module) unitGroup(it *types.Item) int {
	if m.synthOf(it.ID) {
		// A kept provided Format method is a function unit; the
		// impl is none, and a dropped pair is in no list.
		if fd, ok := it.Decl.(*types.FuncDecl); ok && it.Kind == types.KindFunc && !fd.Bodiless {
			return 0
		}
		return -1
	}
	switch it.Kind {
	case types.KindFunc:
		n, _ := m.nodeOf(it.ID)
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				return 0
			}
		case *ast.ExternDecl:
			if d.Body != nil {
				return 0
			}
		case *ast.Contract, *ast.InvariantMember:
			return 4
		}
		return -1
	case types.KindTest:
		return 1
	case types.KindConst:
		// An early const was checked in stage 5, also when that check was
		// an ICE (its E0000 is reported once).
		if m.isEarly(it.ID) || (m.d != nil && m.d.early[it.ID] == ctDone) {
			return -1
		}
		return 2
	case types.KindGlobal:
		// An extern var has no initializer: the linker defines it.
		if n, _ := m.nodeOf(it.ID); n != nil {
			if g, ok := n.(*ast.GlobalDecl); ok && g.Extern {
				return -1
			}
		}
		return 3
	case types.KindLiteral:
		return 5
	}
	return -1
}

// isEarly reports whether a const was checked in stage 5.
func (m *Module) isEarly(id types.ItemID) bool {
	for _, b := range m.early {
		if b.Item == id {
			return true
		}
	}
	return false
}

func (m *Module) computeUnits() {
	var groups [6][]types.ItemID
	for _, ii := range m.info.Items {
		it := m.c.cfg.Items.Get(ii.Item)
		var g int
		ie := source.Guard(it.Key.String(), it.Span, func() { g = m.unitGroup(it) })
		if ie != nil {
			m.Diags = append(m.Diags, diag.FromInternal(ie))
			continue
		}
		if g >= 0 {
			groups[g] = append(groups[g], it.ID)
		}
	}
	m.units = m.units[:0]
	for _, g := range groups {
		m.units = append(m.units, g...)
	}
}

// Units lists the stage-6 work of the module in a fixed order: functions and
// methods with bodies in item order, then tests, consts, globals, contract
// predicates and fn literals, each in item order. Bodiless functions are not
// units, and neither are the consts checked in stage 5 (Early).
func (m *Module) Units() []types.ItemID { return append([]types.ItemID(nil), m.units...) }

// Early returns the bodies stage 5 checked: the same-module consts a type
// needs, in key order.
func (m *Module) Early() []*Body { return append([]*Body(nil), m.early...) }

// Main returns the module's entry function, or NoItem.
func (m *Module) Main() types.ItemID { return m.main }

// Check runs stage 6 on one unit (Module.Units). Run under source.Guard by
// the caller. Body.Reads starts with the unit's own ReadSignature (a
// function's read set includes ReadSignature of itself).
func (c *Checker) Check(m *Module, unit types.ItemID) *Body {
	it := c.cfg.Items.Get(unit)
	if ii := m.byItem[it.ID]; ii != nil && len(ii.Captures) > 0 && it.Kind == types.KindLiteral {
		rec := m.waitCaptureRec(it)
		if rec.ice != nil {
			panic(rec.ice)
		}
		if rec.ok && rec.body != nil {
			return rec.body
		}
		u := m.newUnitWithFallbackCaptures(it, ii, rec)
		u.x.rec.signature(it.Key)
		u.run()
		return u.b
	}
	u := m.newUnit(it)
	u.x.rec.signature(it.Key)
	u.run()
	return u.b
}
