// Why: compile-time evaluation runs while modules are still being checked,
// so it needs a source of bodies and const values that never waits on
// another module's work and never reads a body that failed.
//
// What: semantics (startSemantics, the shared check, lower and eval
// configuration), the body store (store, body, prepare, lowerBody), the
// module states, refreshKnown, program (the driver's eval.Program: Body,
// Const, Layout, TargetHash) and evalShim (the types.Evaluator).
//
// How: a body is lowered, validated and borrow-checked once, then stored,
// clean or poisoned; a module's importers start only after its state is
// done, so every body an evaluation may enter is stored before the request
// reaches eval. Poison is transitive: an importer of a module that is not
// clean is not lowered.

package driver

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"mk/internal/abi"
	checker "mk/internal/check"
	"mk/internal/diag"
	"mk/internal/eval"
	"mk/internal/iface"
	"mk/internal/lower"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// Module check states (modNode.state): a module is running until its last
// stage of this build is through, then done clean, done tainted or done
// failed. Tainted is a module with no diagnostic of its own whose bodies
// were not lowered because a module it depends on is not clean (a unit that
// received poison is Tainted, and so is its module); like failed, it is not
// clean, so its importers are not lowerable either and poison is transitive.
// The state is written once, after the module's const values, and only then
// is the module's importer started, so a reader that sees a done state also
// sees every Decl the module wrote.
const (
	modRunning uint32 = iota
	modDoneClean
	modDoneFailed
	modDoneTainted
)

// bodyEntry is one item's MIR in the session's body store.
type bodyEntry struct {
	mir   *mir.Body
	state eval.BodyState
	// onerr is borrow's guard decisions indexed by BlockID (stage 7,
	// eval.Body.OnerrRuns, mono's OnerrRuns); nil when the body has no
	// TermOnerrGuard or stage 7 was skipped.
	onerr []bool
	// flowed: stage 7 ran on the body and accepted it.
	flowed bool
	// bodyHash is the precomputed iface.Hasher.Body hash of mir when cached
	// in a FuncResult's flow payload (hasBodyHash true), reused by
	// InstanceContentHash via packWorld.BodyHash on warm builds.
	bodyHash    source.Hash
	hasBodyHash bool
	// lazy, when not nil, fills the fields above on first use: a module
	// hit's function result, decoded on demand or by the prefetch
	// (lazybody.go). Read the fields only after force (semantics.body
	// forces).
	lazy *lazyBody
}

// semantics is the world's stage-5 and stage-6 machinery: one Checker, one
// lower configuration and one Evaluator per session, made once std.core is
// resolved (check.New copies Config.Known, which resolve fills from the
// prelude module's items).
type semantics struct {
	// known is resolve's KnownItems, read once the prelude's closure is
	// resolved; the fields of knownModules are set later, by refreshKnown.
	// Readers through &known (lower, borrow, mono, mir.Env) read a field
	// without knownMu; a copy of the whole struct takes knownMu.RLock
	// (knownCopy).
	known   types.KnownItems
	knownMu sync.RWMutex
	items   *types.Items
	chk     *checker.Checker
	lcfg    *lower.Config
	ev      *eval.Evaluator
	hash    *iface.Hasher

	mu     sync.RWMutex
	bodies map[types.ItemID]*bodyEntry // lookup only, never iterated into output
	// mods finds the modNode of a module ID: lookup only.
	mods map[types.ModuleID]*modNode

	// target is the synthesized Target const (target.go).
	target targetConst

	// menv is mono's Env (mono_env.go), made once.
	monoOnce sync.Once
	menv     *monoEnv
}

// startSemantics makes the Checker, the lower configuration and the
// Evaluator, once. testPrelude is set by the corpus session only (check
// accepts the test prelude's bodiless functions).
func (w *world) startSemantics(testPrelude bool) {
	if w.sem5 != nil {
		return
	}
	s := &semantics{bodies: map[types.ItemID]*bodyEntry{}, mods: map[types.ModuleID]*modNode{}, items: w.items}
	if w.rs != nil {
		// The Target item exists before Known is read, so resolve's
		// stable-key lookup names it in Known.Target.
		s.target.item = w.addTargetItem()
		s.known = w.rs.Known()
	}
	consts := w.tab.Consts()
	ccfg := &checker.Config{Interner: w.in, Types: w.tab, Items: w.items, Consts: consts, Known: &s.known,
		Target: w.cfg.Target, Profile: w.cfg.Profile, TestPrelude: testPrelude,
		// become's register-size rule (E0313) asks the C ABI classifier per
		// target.
		ParamClass: func(tt *types.Table, l func(types.TypeID) *types.Layout, tg types.Target, t types.TypeID) types.AbiClass {
			return abi.Classify(tt, l, tg, t)
		},
		// and its fn-value rule the lowering of the whole signature.
		ParamByval: func(tt *types.Table, l func(types.TypeID) *types.Layout, tg types.Target, params []types.TypeID, result types.TypeID, i int) bool {
			sl := abi.Lower(tt, l, tg, params, len(params), false, result).Params[i]
			return sl.Kind == abi.Indirect && sl.Byval
		},
		// A C import's binding, from the module graph (every
		// module of a build is noted before any is checked).
		CImport: func(id types.ModuleID) bool {
			m := s.module(id)
			return m != nil && m.cimport
		}}
	s.chk = checker.New(ccfg)
	s.lcfg = &lower.Config{Types: w.tab, Items: w.items, Consts: consts, Interner: w.in, Known: &s.known,
		Impls: lower.ImplsOf(s.chk), Sources: w.sm, Validate: true,
		// A trace site names its file as a panic site does:
		// package-relative, not the SourceMap path, which is absolute for a
		// package named by an absolute root. It keeps a dependency's
		// canonical id, so a module's stored bodies are the same whichever
		// package uses it; codegen shows the local name
		// (codegen.displaySite).
		SitePath: w.sitePath}
	// The Hasher reads contract bodies (mk.sig.v1) and fn literal
	// bodies (mk.fn.v1 tag 2) from the body store.
	s.hash = iface.NewHasher(w.tab, consts, s.hashBody)
	s.ev = eval.New(eval.Config{Types: w.tab, Items: w.items, Consts: consts, Interner: w.in,
		Program: &program{w: w, s: s}, Known: s.known, Target: w.cfg.Target, TargetItem: s.known.Target,
		// std.core's compile-time page source and arena.
		PageAlloc: w.stdItem("std.core", "EvalPageAlloc"), PageFree: w.stdItem("std.core", "EvalPageFree"),
		EvalArena: w.stdItem("std.core", "MakeEvalArena"), Evaluating: w.stdItem("std.core", "Evaluating")})
	ccfg.Eval = evalShim{s}
	ccfg.Prepare = func(b *checker.Body) { w.prepare(b) }
	w.sem5 = s
}

// knownModules are the std modules outside the prelude's import closure
// whose items KnownItems names (resolve/known.go): std.simd's Vec, Mask,
// Lane and CpuFeatures. std.raw, Atomic's module, lies inside the
// closure, so its field is set when startSemantics reads Known.
var knownModules = []string{"std.simd"}

// refreshKnown sets the KnownItems fields of m's items when m is one of
// knownModules. startSemantics reads Known when only the prelude's closure
// is resolved, so without it std.simd's fields would stay NoItem.
// pipelineAll calls it on m's own worker once m's items exist
// (after its stage 3, or the decode of a module hit) and before m's stage 5;
// the schedule starts every importer of m only after that work returns, so
// a body that names a std.simd type reads the fields set. Only those four
// fields are written, under knownMu: readers of the other fields through
// &known run concurrently and see no write. A reader of one of the four
// fields must be in std.simd or a module that imports it (mir's asm operand
// rule and check's lane rules are; eval, mono and codegen key the
// intrinsics by stable key). check.New and eval.New took their copies
// before: check matches std.simd's items by name and module (isLaneItem,
// simdItem), so neither depends on the refresh.
func (w *world) refreshKnown(m *modNode) {
	s := w.sem5
	if s == nil || w.rs == nil || !m.std || !slices.Contains(knownModules, m.path) {
		return
	}
	k := w.rs.Known()
	s.knownMu.Lock()
	s.known.Vec, s.known.Mask, s.known.Lane, s.known.CpuFeatures = k.Vec, k.Mask, k.Lane, k.CpuFeatures
	s.knownMu.Unlock()
}

// knownCopy is a copy of the session's KnownItems, for a configuration that
// takes them by value (gen's Context, the sound evaluator), safe beside
// refreshKnown.
func (s *semantics) knownCopy() types.KnownItems {
	s.knownMu.RLock()
	defer s.knownMu.RUnlock()
	return s.known
}

// stdItem is the item of module mod named name, or NoItem.
func (w *world) stdItem(mod, name string) types.ItemID {
	if id, ok := w.items.ByKey(types.ItemKey(mod, name)); ok {
		return id
	}
	return types.NoItem
}

// noteModule records a module's node so the body source can find its state.
func (s *semantics) noteModule(m *modNode) {
	s.mu.Lock()
	s.mods[m.id] = m
	s.mu.Unlock()
}

func (s *semantics) module(id types.ModuleID) *modNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mods[id]
}

func (s *semantics) body(id types.ItemID) *bodyEntry {
	s.mu.RLock()
	e := s.bodies[id]
	s.mu.RUnlock()
	e.force()
	return e
}

// hashBody is the lowered body of id for a hash that needs one (iface's
// body accessor): any lowered body, accepted by stage 7 or not, since a
// signature's hash names its contract bodies whatever borrow said of them;
// nil (an ICE in iface) when id was never lowered.
func (s *semantics) hashBody(id types.ItemID) *mir.Body {
	if b := s.body(id); b != nil {
		return b.mir
	}
	return nil
}

// store records an item's MIR or its poison. The first store wins: check
// may hand one const to Prepare and list it as a unit, and eval may
// already have compiled the first body.
func (s *semantics) store(id types.ItemID, e *bodyEntry) {
	s.mu.Lock()
	if s.bodies[id] == nil {
		s.bodies[id] = e
	}
	s.mu.Unlock()
}

// prepare is check.Config.Prepare:
// an early const's initializer, checked in stage 5, is lowered at once so
// the stage-5 evaluation that needs it finds it ready. A checked body with
// a diagnostic is stored poisoned (its diagnostics are in Module.Diags); an
// ICE while lowering is an E0000 of the module and poisons the body.
func (w *world) prepare(b *checker.Body) {
	s := w.sem5
	if s.body(b.Item) != nil {
		return
	}
	e := w.lowerBody(b)
	if m := s.module(w.items.Get(b.Item).Module); m != nil {
		if e.held != nil {
			m.addHeld(heldICE{d: e.held, reads: e.poisonReads})
		}
		if e.ice != nil {
			m.addPrepDiag(e.ice)
		}
		if len(e.flow) > 0 {
			m.addPrepFlow(e.flow)
		}
		if e.ice != nil || len(e.flow) > 0 {
			m.addPrepFailed(b.Item)
		}
	}
	s.store(b.Item, &e.entry)
}

// lowered is the outcome of lowering and borrow-checking one checked body.
type lowered struct {
	entry bodyEntry
	ice   *diag.Diagnostic
	flow  []*diag.Diagnostic // stage 7's diagnostics of the body
	// flowTook is stage 7's time on the body, measured only with --timings.
	flowTook time.Duration
	// reads are stage 7's reads of the body (borrow.Result.Reads).
	reads types.ReadSet
	// held is lower's E0000 on a body that reads a const with no type
	// (poisonReads, checker.Body.PoisonReads): reported only when those
	// consts do not account for it (build.heldICEs).
	held        *diag.Diagnostic
	poisonReads []types.ItemID
}

// lowerBody turns one checked body into MIR under source.Guard, with
// Validate on, then runs stage 7 on it under the same Guard (borrow only on
// a body whose check, lower and Validate were clean). A body with check
// diagnostics is not lowered and is poisoned; an ICE, Validate's and
// borrow's included, is an E0000 naming the item and poisons the body; a
// body borrow rejects is stored poisoned, so no evaluation enters it (a body
// with a diagnostic is BodyPoisoned), and its diagnostics are returned in
// flow. A body that reads a const with no type (Body.PoisonReads) is lowered
// like any other, so a read in a branch lower folds away costs nothing; an
// ICE in it is returned in held, not ice, for the module to judge.
func (w *world) lowerBody(b *checker.Body) lowered {
	if len(b.Diags) > 0 {
		return lowered{entry: bodyEntry{state: eval.BodyPoisoned}}
	}
	s := w.sem5
	it := w.items.Get(b.Item)
	var out lowered
	// lower's impl queries (the Slice implementer) go through a copy
	// of the configuration whose Impls records their reads, which
	// lower.ImplsOf drops, into the unit's (implreads.go).
	lcfg := *s.lcfg
	var lreads types.ReadSet
	lcfg.Impls = func(iface types.ItemID, args []types.GenericArg, t types.TypeID) types.ItemID {
		var rs types.ReadSet
		id := types.NoItem
		if ie := source.Guard("ImplOf", source.Span{}, func() {
			if x, ok := s.chk.ImplOf(iface, args, t, &rs); ok {
				id = x
			}
		}); ie != nil {
			return types.NoItem
		}
		lreads.Merge(&rs)
		return id
	}
	if ie := source.Guard("lower "+it.Key.String(), it.Span, func() {
		in := lower.FromCheck(&lcfg, s.chk, b)
		mb, proved := lower.LowerProved(&lcfg, in)
		out.entry = bodyEntry{mir: mb, state: eval.BodyReady}
		start := w.clock()
		res, onerr := w.flowCheck(mb, in, it)
		out.flowTook = w.since(start)
		out.reads = res.Reads
		out.reads.Merge(&lreads)
		if w.noFlow {
			// Through is before flow: the verdict is neither reported
			// nor poisons the body; an accepted body keeps its decisions for
			// the walk and eval (walkEnv).
			if len(res.Diags) == 0 {
				out.entry.onerr, out.entry.flowed = onerr, true
				proved.Fold(mb) // as below
			}
			return
		}
		if len(res.Diags) > 0 {
			out.flow = res.Diags
			out.entry = bodyEntry{mir: mb, state: eval.BodyPoisoned}
			return
		}
		out.entry.onerr, out.entry.flowed = onerr, true
		// A bounds check the range analysis proved in Lower becomes `goto
		// ok`, only now that borrow has accepted the unfolded body (borrow
		// reads a check by its shape and never changes the body). The block
		// count is unchanged, so onerr's guard decisions stay valid, and the
		// compares stay, so eval's step count is unchanged. This is
		// FoldProvedBounds without its second run of the analysis.
		proved.Fold(mb)
	}); ie != nil {
		l := lowered{entry: bodyEntry{state: eval.BodyPoisoned}, ice: diag.FromInternal(ie)}
		if poisonedAt(b.PoisonAt, l.ice.At) {
			// Lower met the Poison of a read of a const with no value: the
			// E0000 is held, and the module drops it when those consts
			// account for it (heldICE). An E0000 elsewhere in the body is
			// reported as any other.
			l.held, l.ice = l.ice, nil
			l.poisonReads = b.PoisonReads
		}
		return l
	}
	return out
}

// poisonedAt reports whether an E0000 at at starts from a poisoned const
// read: at and one of spans (checker.Body.PoisonAt) overlap, one holding
// the other (lower reports the read itself, or the expression or binding
// statement it makes Poison).
func poisonedAt(spans []source.Span, at source.Span) bool {
	for _, s := range spans {
		if s.File == at.File && (s.Start <= at.Start && at.End <= s.End || at.Start <= s.Start && s.End <= at.End) {
			return true
		}
	}
	return false
}

// program is eval.Program over the session. It never waits on another
// goroutine's work and never calls back into the Evaluator: every body an
// evaluation may enter is stored before the request reaches eval (a lower
// module is done; a same-module initializer was Prepared in stage 5).
type program struct {
	w *world
	s *semantics
}

// Body is the MIR of fn. With generic arguments it is mono's instance body
// (mono.Instantiate through monoEnv.instance), poisoned when the generic
// body is. An item of a module that is done and failed or tainted is
// poisoned; an item of the running module answers by its own body. A
// body stage 7 rejected is stored poisoned (lowerBody).
func (p *program) Body(fn types.ItemID, args []types.GenericArg) eval.Body {
	it := p.w.items.Get(fn)
	m := p.s.module(it.Module)
	if m != nil {
		if st := m.state.Load(); st == modDoneFailed || st == modDoneTainted {
			return eval.Body{State: eval.BodyPoisoned}
		}
	}
	if len(args) > 0 {
		e := p.s.body(fn)
		switch {
		case e != nil && e.state == eval.BodyReady && e.mir != nil:
			// An instance body has no TermOnerrGuard: mono resolved each
			// through borrow's decision on the generic body.
			if sv := p.s.monoEnv(p.w).instanceReads(fn, args); sv.b != nil {
				return eval.Body{MIR: sv.b, State: eval.BodyReady, Reads: sv.reads}
			}
			return eval.Body{State: eval.BodyPoisoned}
		case e != nil:
			return eval.Body{State: e.state}
		case m != nil && m.state.Load() == modDoneClean:
			return eval.Body{State: eval.BodyMissing}
		}
		return eval.Body{State: eval.BodyNotLowered}
	}
	e := p.s.body(fn)
	if e == nil {
		if m != nil && m.state.Load() == modDoneClean {
			return eval.Body{State: eval.BodyMissing}
		}
		return eval.Body{State: eval.BodyNotLowered}
	}
	if e.state == eval.BodyReady && e.mir != nil && callsSliceFn(p.w.items, e.mir) {
		// A caller of a Slice[T] namespace function passes its
		// receiver in its own type (mut [3]int, mut List[T]); mono makes it
		// the slice the callee's instance takes. So eval runs mono's body of
		// the non-generic caller, as codegen does, never the lowered one. Its
		// guards are resolved, as in any instance body.
		if sv := p.s.monoEnv(p.w).instanceReads(fn, nil); sv.b != nil {
			return eval.Body{MIR: sv.b, State: eval.BodyReady, Reads: sv.reads}
		}
		return eval.Body{State: eval.BodyPoisoned}
	}
	// Hash is not set. OnerrRuns: borrow's guard decisions (stage 7).
	return eval.Body{MIR: e.mir, State: e.state, OnerrRuns: e.onerr}
}

// Layout is check's layout of a closed type and its mk.layout.v1 hash, the
// hash a ReadLayout records (the module cache validates it, frontWorld).
func (p *program) Layout(t types.TypeID) (*types.Layout, source.Hash) {
	l := p.s.chk.Layout(t)
	return &l, p.s.hash.Layout(&l)
}

// LayoutName is iface's spelling of a ReadLayout's generic arguments.
func (p *program) LayoutName(args []types.GenericArg) string { return p.s.hash.LayoutName(args) }

// Const is a lower module's const or global value, final because that
// module is done; poisoned when the module failed or the value is
// absent.
func (p *program) Const(item types.ItemID) eval.Const {
	if item == p.s.target.item && item != types.NoItem {
		return p.targetProgramConst()
	}
	it := p.w.items.Get(item)
	if m := p.s.module(it.Module); m != nil && m.state.Load() != modDoneClean {
		return eval.Const{Poisoned: true}
	}
	switch d := it.Decl.(type) {
	case *types.ConstDecl:
		if d.HasValue {
			return eval.Const{Value: d.Value}
		}
	case *types.GlobalDecl:
		if d.HasValue {
			return eval.Const{Value: d.Value}
		}
	}
	return eval.Const{Poisoned: true}
}

// ConstHash returns the zero hash.
func (p *program) ConstHash(types.ConstID) source.Hash { return source.Hash{} }

// TargetHash is iface's hash of a ReadTarget read on the build's target.
func (p *program) TargetHash(f types.TargetField) source.Hash {
	h, _ := iface.TargetReadHash(p.w.cfg.Target, f.String())
	return h
}

// evalShim is check's types.Evaluator: the session's Evaluator. It holds no
// state of its own, so it is as safe for concurrent use as eval.Evaluator.
type evalShim struct{ s *semantics }

// Eval runs req on the session's Evaluator. Its reads are added to the
// requesting module's (the module cache's external reads), and a poison
// result marks the module, so it is not stored.
func (e evalShim) Eval(req *types.EvalRequest) types.EvalResult {
	res := e.s.ev.Eval(req)
	if m := e.s.module(req.Module); m != nil {
		if res.Value == types.NoConst && res.Err == nil {
			m.evalPoison.Store(true)
		}
		m.readsMu.Lock()
		if res.Reads.Len() > 0 {
			m.reads.Merge(&res.Reads)
		}
		// Trace.Evaluations: the evaluated item's key (a const, a
		// global, a called function, a contract predicate).
		m.evals = append(m.evals, e.s.items.Get(req.Item).Key.String())
		m.readsMu.Unlock()
	}
	return res
}

// modState is modNode's atomic check state (modRunning, modDoneClean,
// modDoneFailed, modDoneTainted).
type modState = atomic.Uint32
