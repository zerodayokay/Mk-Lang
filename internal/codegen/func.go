// Why: this is the core of codegen: one pack's functions, each turned from
// MIR into an LLVM function, with the linkage that lets packs call each
// other and lets release internalize what no other pack needs.
//
// What: packGen (one pack's emitter) and fnGen (one function's), with
// defineHeaders, linkage, emitFunc, prologue, emitBlocks, stmt, the
// parameter part helpers and finish.
//
// How: every function is declared first (defineHeaders), then each body is
// emitted: a prologue that allocates every local in the entry block and
// stores the Direct parameters, one LLVM block per MIR block, then the
// statements. Places live in memory, and LLVM promotes them to registers.

package codegen

import (
	"strconv"
	"strings"

	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/pgo"
	"mk/internal/source"
	"mk/internal/types"
)

// packGen emits one pack's functions into one module. Not shared between
// goroutines: one per pack per worker.
type packGen struct {
	// deadStub is the pack's trap for uncollected IoTable entries (deadentry.go).
	deadStub *llvm.Function
	// cpuCacheV is the pack's Cpu() cache global (intrinsic_simd.go cpu).
	cpuCacheV llvm.Value
	// cpuInitFn is the pack's cold mk.cpu.detect (intrinsic_simd.go).
	cpuInitFn *llvm.Function
	u         *Unit
	p         int
	env       Env
	cfg       *Config
	m         *llvm.Module
	tt        *types.Table
	items     *types.Items
	stale     []string

	reprs map[types.TypeID]*repr
	// nonnull memoizes holdsNonnull (nonnull.go).
	nonnull map[types.TypeID]bool
	// siteRef memoizes siteRefType (&std.rt.Site).
	siteRef types.TypeID
	sigs    map[string]*lsig
	// defs are the LLVM functions of this pack's members by Funcs index;
	// for a split member (tailbody.go or pnone.go) its C ABI wrapper, with
	// tailDefs its $tail body, tailsigs the $tail lowered signature,
	// pnoneDefs its $pnone preserve_nonecc body, and pnoneTables this pack's
	// companion $pnone dispatch tables by const item.
	defs        map[int]*llvm.Function
	tailDefs    map[int]*llvm.Function
	tailsigs    map[int]*lsig
	pnoneDefs   map[int]*llvm.Function
	pnoneTables map[types.ItemID]llvm.Value
	// soleFx memoizes soleEffectOf by Funcs index (solecall.go); a packGen
	// is used by one goroutine, so it needs no lock.
	soleFx map[int]soleEffect
	// fnsigs are the lowered signatures of Funcs by index.
	fnsigs  map[int]*lsig
	syms    *symIndex
	consts  map[types.ConstID]llvm.Value
	files   map[string]*llvm.Global
	sites   map[siteKey]llvm.Value
	hooks   map[string]*hook
	globals map[types.ItemID]llvm.Value
	nconst  int
	// constDepth is constGlobal's nesting (const.go).
	constDepth int
	// staticMax is staticAlignOf the unit once bigStatic needed it, and
	// staticDone says it was computed.
	staticMax  uint32
	staticDone bool
	// thunks are the pack's spawn thunks (spawn.go), nthunk their ordinal.
	thunks map[thunkKey]*llvm.Function
	nthunk int
	// adapters are the pack's system-stack adapters (sysstack.go).
	adapters map[adapterKey]*llvm.Function
	nadapter map[string]int
	// wrappers are the pack's intrinsic wrappers by argsKey (wrapper.go),
	// nwrap the ordinal of the generic ones.
	wrappers map[string]wrapperFn
	nwrap    int
	// targetHelpers are the private helpers of target intrinsics called
	// from functions without their features (intrinsic_target.go).
	targetHelpers map[string]*llvm.Function
	// extraFns are the thunks, adapters and wrappers in creation order:
	// FuncTable rows of kind 2 (functable.go).
	extraFns []*llvm.Function
	// cthunks are the pack's thunked exports (exports.go), created with
	// the headers and emitted after them; cthunkFn is each one's thunk by
	// Funcs index, which a function value of the export names (fnRef).
	cthunks  []thunkRec
	cthunkFn map[int]*llvm.Function
	// dbgCU is the pack's compile unit, dbgFiles its DIFiles (debug.go).
	dbgCU    llvm.MD
	dbgFiles map[string]llvm.MD
	// dbgTypes are the pack's debug types by TypeID, dbgNS its namespaces
	// by module path (dityp.go, debug_full.go; Config.Debug only).
	dbgTypes map[types.TypeID]llvm.MD
	dbgNS    map[string]llvm.MD
	// pgo is each member's PGO state by Funcs index (pgo_gen.go), pgoSum
	// the profile's summary, computed on first use (pgo_use.go).
	pgo    map[int]*fnPGO
	pgoSum *pgo.Summary
}

func newPackGen(u *Unit, p int, env Env, cfg *Config, m *llvm.Module) *packGen {
	var syms *symIndex
	if u.Lookup == nil {
		syms = u.index()
	}
	return &packGen{u: u, p: p, env: env, cfg: cfg, m: m, tt: env.Table(), items: env.Items(),
		reprs: map[types.TypeID]*repr{}, sigs: map[string]*lsig{}, defs: map[int]*llvm.Function{},
		fnsigs: map[int]*lsig{}, syms: syms, consts: map[types.ConstID]llvm.Value{},
		files: map[string]*llvm.Global{}, sites: map[siteKey]llvm.Value{}, hooks: map[string]*hook{},
		globals: map[types.ItemID]llvm.Value{}, thunks: map[thunkKey]*llvm.Function{}, dbgFiles: map[string]llvm.MD{},
		adapters: map[adapterKey]*llvm.Function{}, nadapter: map[string]int{}, pgo: map[int]*fnPGO{},
		wrappers: map[string]wrapperFn{}}
}

// funcSigOf is the lowered signature of Funcs[fi]: from its instance body's
// locals (concrete), or for a bodiless extern or intrinsic from its declared
// signature, which is never generic; under abi.LowerInternal where
// internalABI allows, else the C ABI.
func (g *packGen) funcSigOf(fi int) *lsig {
	if s, ok := g.fnsigs[fi]; ok {
		return s
	}
	f := &g.u.Funcs[fi]
	var params []types.TypeID
	var result types.TypeID
	fixed, variadic := 0, false
	if f.Body != nil {
		for i := 1; i <= int(f.Body.ParamCount); i++ {
			params = append(params, f.Body.Locals[i].Type)
		}
		result = f.Body.Locals[mir.ReturnLocal].Type
		fixed = len(params)
	} else {
		sig := g.declSig(f.Item)
		for _, p := range sig.Params {
			params = append(params, p.Type)
		}
		result = sig.ReturnType(g.tt)
		fixed, variadic = len(params), sig.Variadic
	}
	var s *lsig
	if g.internalABI(fi) { // sig.go
		s = g.lowerSigInternal(params, result)
	} else {
		s = g.lowerSig(params, fixed, variadic, result)
	}
	s = g.withImmutParams(fi, s) // immutarg.go
	g.fnsigs[fi] = s
	return s
}

// declSig is the checked signature of a function item.
func (g *packGen) declSig(item types.ItemID) *types.Signature {
	it := g.items.Get(item)
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok {
		source.ICEf(it.Span, "codegen: %s is not a function", it.Key)
	}
	return &fd.Sig
}

// linkage is the linkage of a function in every profile: export
// extern and support names external with default visibility; a function
// whose every reference is in its own pack (Func.Local, not AddrTaken)
// internal; every other Mk function hidden (ELF) or dso_local (COFF)
// external, so another pack may call it (ThinLTO internalizes it in
// release).
func (g *packGen) linkage(f *Func) (llvm.Linkage, llvm.Visibility, bool) {
	if it := g.items.Get(f.Item); it != nil {
		if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Sig.Extern {
			return llvm.External, llvm.Default, g.cfg.Target.OS == types.OSWindows
		}
	}
	if f.Local && !f.AddrTaken && f.Body != nil && g.funcSection(f.Item) == "" {
		return llvm.Internal, llvm.Default, false // a section function is never internal
	}
	if g.cfg.Target.OS == types.OSWindows {
		return llvm.External, llvm.Default, true
	}
	return llvm.External, llvm.Hidden, true
}

// libcFn reports whether f is a function of std.libc, whose
// bodies implement the C library's names.
func (g *packGen) libcFn(f *Func) bool {
	it := g.items.Get(f.Item)
	return it != nil && strings.HasPrefix(it.Key.String(), "std.libc.")
}

// wrappedMain reports whether f is the root module's Main entered through
// the driver's report wrapper (Config.Main). Main stays out of line:
// it runs once, so inlining it saves nothing, and once release inlined it
// into the wrapper its symbol left the build, where a trace, a profile or
// a disassembly read by symbol (std's TestSTDCRYPTO_ConstantTimeDisassembly)
// found it while CallMain called it from the support pack.
func (g *packGen) wrappedMain(f *Func) bool {
	if g.cfg.Main.IsZero() {
		return false
	}
	it := g.items.Get(f.Item)
	return it != nil && len(f.Args) == 0 && it.Key == types.ItemKey(g.cfg.RootModule, "Main") && it.Key != g.cfg.Main
}

// defineHeaders creates every member's LLVM function before any body, so a
// call to a later member of the pack names its definition.
func (g *packGen) defineHeaders() {
	for _, fi := range g.u.Packs[g.p] {
		f := &g.u.Funcs[fi]
		if f.Body == nil {
			source.ICEf(source.Span{}, "codegen: %s in pack %d has no body", f.Symbol, g.p)
		}
		ls := g.funcSigOf(fi)
		lk, vis, dso := g.linkage(f)
		if g.nakedFn(f) {
			// Naked: noinline, no probe, no counter.
			fn := g.m.AddFunction(f.Symbol, g.funcSig(ls, lk, vis, dso, g.nakedAttrs()))
			g.sectionOf(f, fn)
			g.defs[fi] = fn
			continue
		}
		attrs := g.fnAttrs(ls.result)
		if tf := g.featuresOf(fi); tf != "" {
			attrs = withFeatures(attrs, tf) // its cpu features (general-regs-only included)
		}
		if g.libcFn(f) {
			// std.libc's bodies are the C library, so, as the support pack's memory functions,
			// "no-builtins" keeps LLVM from turning strlen's loop into a
			// call of strlen (clang -fno-builtin's attribute).
			attrs = attrs.Str("no-builtins", "")
		}
		if g.wrappedMain(f) {
			attrs = attrs.Flag("noinline")
		}
		if g.smallFn(fi) || g.crossSole(fi) {
			attrs = attrs.Flag("alwaysinline") // smallfn.go, release only
		}
		sym := f.Symbol
		thunk := g.thunked(fi) // exports.go: the C name is the thunk's
		var clk llvm.Linkage
		var cvis llvm.Visibility
		var cdso bool
		if thunk {
			clk, cvis, cdso = lk, vis, dso
			sym, lk, vis, dso = thunkBody(sym), llvm.Internal, llvm.Default, false
		}
		fn := g.m.AddFunction(sym, g.funcSig(ls, lk, vis, dso, g.pgoAttrs(fi, attrs)))
		if thunk {
			g.addThunk(fi, fn, clk, cvis, cdso)
		}
		g.sectionOf(f, fn)
		if g.tailBody(fi) {
			// tailbody.go: fn is the C ABI wrapper, the body is $tail's.
			tlk, tvis, tdso := g.tailLinkage(f)
			body := g.m.AddFunction(f.Symbol+tailSuffix, g.funcSig(g.tailSigOf(fi), tlk, tvis, tdso,
				g.pgoAttrs(fi, attrs.Without("alwaysinline"))))
			g.sectionOf(f, body)
			if g.tailDefs == nil {
				g.tailDefs = map[int]*llvm.Function{}
			}
			g.tailDefs[fi] = body
		} else if g.pnoneBody(fi) {
			// pnone.go: fn is the C ABI wrapper, the body is $pnone's.
			plk, pvis, pdso := g.tailLinkage(f)
			psig := g.funcSig(ls, plk, pvis, pdso, g.pgoAttrs(fi, attrs.Without("alwaysinline")))
			psig.CC = llvm.PreserveNoneCC
			body := g.m.AddFunction(f.Symbol+pnoneSuffix, psig)
			if g.pnoneDefs == nil {
				g.pnoneDefs = map[int]*llvm.Function{}
			}
			g.pnoneDefs[fi] = body
		}
		if body, ok := g.tailDefs[fi]; ok {
			g.entryCount(fi, body)
		} else if body, ok := g.pnoneDefs[fi]; ok {
			g.entryCount(fi, body)
		} else {
			g.entryCount(fi, fn)
		}
		if p := g.pgoOf(fi); p != nil && p.gen {
			g.defineCounters(fi, p)
		}
		g.defs[fi] = fn
	}
}

// fnGen emits one function.
type fnGen struct {
	g      *packGen
	f      *Func
	fi     int
	b      *mir.Body
	fn     *llvm.Function
	sig    *lsig
	locals []addr
	blocks []*llvm.Block
	cur    *llvm.Block
	span   mir.SpanID
	nextra int
	nsite  int
	entry  *llvm.Block
	// dbgSP is the function's DISubprogram, dbgLoc the current location
	// (debug.go); zero without debug info.
	dbgSP  llvm.MD
	dbgLoc llvm.MD
	// dbgScope is the scope of the current MIR block's locations, and
	// dbgScopes each block's (debug_full.go); zero without Config.Debug,
	// when locations take dbgSP.
	dbgScope  llvm.MD
	dbgScopes []llvm.MD
	// dbgHome is each local's home block (debug_full.go), nil without
	// Config.Debug.
	dbgHome []int
	// reading is set while readAddr computes a place that is only read.
	reading bool
	// cover is the function's counter array under Config.Cover (cover.go).
	cover llvm.Value
	// promoted[l] is the constant local l is bound to (constLocals): its
	// address is that constant's global and its assignments emit nothing.
	// nil when no local is promoted.
	promoted []types.ConstID
	// bi is the MIR block being emitted; pgo the function's PGO state, nil
	// without --pgo-gen or --pgo= (and for support-pack bodies).
	bi  int
	pgo *fnPGO
	// alias is the slice parameters whose element accesses carry scoped
	// alias metadata (aliasscope.go); nil when none.
	alias []aliasParam
	// probed: frameProbe gave the function the aligned stack probe.
	probed bool
	// pnone is set when emitting a preserve_nonecc body (pnone.go), with
	// pnoneLocals mapping each pure-tail table local to its const item.
	pnone       bool
	pnoneLocals []types.ItemID
	// loopBlks memoizes natural-loop membership per MIR block (solecall.go).
	loopBlks []bool
	// coldGuard maps each loop-versioning guard block to its cold fallback header
	// so unprofiled codegen can attach cold branch weights (term.go).
	coldGuard map[int]mir.BlockID
	// coldBlock marks blocks on std.runtime.Cold() paths for cold branch
	// weights and cold call attributes (term.go, call.go).
	coldBlock map[mir.BlockID]bool
}

// addr is a memory address and the alignment known for it.
type addr struct {
	v     llvm.Value
	align uint32
	// scope is the aliasParam number + 1 (aliasscope.go) when the
	// address is an element of that slice parameter, else 0.
	scope int32
}

func (g *packGen) emitFunc(fi int) {
	f := &g.u.Funcs[fi]
	if g.nakedFn(f) {
		g.emitNaked(fi)
		return
	}
	fg := &fnGen{g: g, f: f, fi: fi, b: f.Body, fn: g.defs[fi], sig: g.funcSigOf(fi), pgo: g.pgoOf(fi)}
	if body, ok := g.tailDefs[fi]; ok {
		g.emitTailEntry(fi) // tailbody.go
		fg.fn, fg.sig = body, g.tailSigOf(fi)
	} else if body, ok := g.pnoneDefs[fi]; ok {
		g.emitPnoneEntry(fi) // pnone.go
		fg.fn = body
		fg.pnone = true
		fg.pnoneLocals = g.pnoneBodyLocals(fi)
	}
	if g.cfg.Debug {
		fg.fullSubprogram() // debug_full.go
	} else {
		fg.subprogram(f.Key, bodySpan(f))
	}
	fg.checkImmutParams() // immutarg.go
	if !g.cfg.Debug && !g.ctModule(fi) {
		fg.coldBlock = findColdBlocks(fg.b, g.items)
		if fg.pgo == nil {
			fg.coldGuard = findColdGuards(fg.b)
		}
	}
	fg.emitBlocks()
}

// emitBlocks emits fg.b into fg.fn: the prologue, then every MIR block.
func (fg *fnGen) emitBlocks() {
	fg.prologue()
	start := fg.entryCounter()
	for i := range fg.b.Blocks {
		fg.bi = i
		fg.enter(fg.blocks[i])
		fg.coverBlock(i) // --cover's counter (cover.go)
		blk := &fg.b.Blocks[i]
		for si := range blk.Stmts {
			fg.stmt(&blk.Stmts[si])
		}
		fg.term(&blk.Term)
	}
	fg.entry.Br(start)
}

// newBlock is an extra block (trap, split, continuation), labelled x<n>.
func (fg *fnGen) newBlock() *llvm.Block {
	fg.nextra++
	b := fg.fn.NewBlock("x" + strconv.Itoa(fg.nextra))
	if !fg.dbgLoc.IsZero() {
		b.SetDebugLoc(fg.dbgLoc)
	}
	return b
}

// prologue allocates every non-zero-sized local in the entry block and
// stores the Direct parameters into theirs. An Indirect
// parameter's pointer is its local's address (a fresh caller copy); sret
// is local 0's address.
func (fg *fnGen) prologue() {
	g, m, b := fg.g, fg.g.m, fg.b
	fg.entry = fg.fn.NewBlock("entry")
	for i := range b.Blocks {
		fg.blocks = append(fg.blocks, fg.fn.NewBlock("b"+strconv.Itoa(i)))
	}
	fg.cur = fg.entry
	fg.setLoc(0)
	fg.locals = make([]addr, len(b.Locals))
	fg.promoted = fg.constLocals()
	sig := fg.sig
	for li := range b.Locals {
		t := b.Locals[li].Type
		r := g.reprOf(t)
		if r.kind == rZero {
			fg.locals[li] = addr{v: m.ConstIntToPtr(uint64(r.align)), align: r.align}
			continue
		}
		if ti := fg.pnoneTableLocal(li); ti != types.NoItem {
			fg.locals[li] = addr{v: g.pnoneTableGlobal(ti), align: r.align} // pnone.go
			continue
		}
		if fg.promoted != nil && fg.promoted[li] != 0 {
			fg.locals[li] = addr{v: g.constGlobal(fg.promoted[li]), align: r.align} // promote.go
			continue
		}
		if li == int(mir.ReturnLocal) && sig.sret {
			fg.locals[li] = addr{v: fg.fn.Param(0), align: r.align}
			continue
		}
		if li >= 1 && li <= int(b.ParamCount) && sig.s.Params[li-1].Kind == abi.Indirect {
			fg.locals[li] = addr{v: fg.fn.Param(sig.first[li-1]), align: r.align}
			continue
		}
		size := r.size
		if li == int(mir.ReturnLocal) && sig.s.Ret.Kind == abi.Direct {
			size = max(size, partBytes(sig.s.Ret))
		} else if li >= 1 && li <= int(b.ParamCount) {
			size = max(size, partBytes(sig.s.Params[li-1]))
		}
		fg.locals[li] = addr{v: fg.entryAlloca(m.Array(size, m.I(8)), r.align), align: r.align}
	}
	if fg.dbgHome != nil {
		fg.declareLocals() // debug_full.go
	}
	for pi := 0; pi < int(b.ParamCount); pi++ {
		sl := sig.s.Params[pi]
		if sl.Kind != abi.Direct {
			continue
		}
		fg.storeParts(fg.locals[pi+1], b.Locals[pi+1].Type, sl, sig.array[pi], fg.paramValues(pi))
	}
	fg.aliasScopes() // aliasscope.go
}

// partStride is the distance between a Direct slot's parts in memory: an
// eightbyte on x86_64 (System V puts part k at 8k), the element size of an
// aarch64 array (an HFA's floats are contiguous).
func partStride(sl abi.Slot) uint64 {
	var w uint64
	for _, p := range sl.Parts {
		w = max(w, uint64(p.Bytes))
	}
	return w
}

// partBytes is the bytes a Direct slot's parts cover in memory.
func partBytes(sl abi.Slot) uint64 {
	n := len(sl.Parts)
	if n == 0 {
		return 0
	}
	return partStride(sl)*uint64(n-1) + uint64(sl.Parts[n-1].Bytes)
}

// paramValues are the LLVM parameters of Mk parameter pi.
func (fg *fnGen) paramValues(pi int) []llvm.Value {
	sig := fg.sig
	n := 1
	if !sig.array[pi] {
		n = len(fg.g.partTypes(sig.params[pi], sig.s.Params[pi], false))
	}
	out := make([]llvm.Value, n)
	for k := range out {
		out[k] = fg.fn.Param(sig.first[pi] + k)
	}
	return out
}

// storeParts stores the parts of a Direct value (parameters, call results)
// into memory at a: a scalar as its memory type, an aarch64 array whole,
// else part k at the sum of the earlier parts' sizes.
func (fg *fnGen) storeParts(a addr, t types.TypeID, sl abi.Slot, array bool, vals []llvm.Value) {
	r := fg.g.reprOf(t)
	if r.kind == rScalar && len(vals) == 1 && !array {
		fg.storeScalar(a, r, vals[0])
		return
	}
	if array {
		fg.cur.Store(vals[0], a.v, a.align)
		return
	}
	stride := partStride(sl)
	for k, v := range vals {
		off := stride * uint64(k)
		fg.cur.Store(v, fg.gep(a.v, off), alignAt(a.align, off))
	}
}

// loadParts loads a Direct value's parts from memory at a, the inverse of
// storeParts.
func (fg *fnGen) loadParts(a addr, t types.TypeID, sl abi.Slot, array bool, result bool) []llvm.Value {
	g := fg.g
	r := g.reprOf(t)
	parts := g.partTypes(t, sl, result)
	if r.kind == rScalar && len(parts) == 1 && !array {
		return []llvm.Value{fg.loadScalar(a, r, false)}
	}
	if array {
		elem := parts[0]
		for _, p := range parts {
			if p != elem {
				elem = g.m.I(64)
			}
		}
		return []llvm.Value{fg.cur.Load(g.m.Array(uint64(len(parts)), elem), a.v, a.align)}
	}
	out := make([]llvm.Value, len(parts))
	stride := partStride(sl)
	for k, pt := range parts {
		off := stride * uint64(k)
		out[k] = fg.cur.Load(pt, fg.gep(a.v, off), alignAt(a.align, off))
	}
	return out
}

// gep is base plus a constant byte offset (inbounds).
func (fg *fnGen) gep(base llvm.Value, off uint64) llvm.Value {
	if off == 0 {
		return base
	}
	return fg.cur.GEPBytes(base, fg.g.m.ConstInt(fg.g.m.I(64), off), true)
}

// alignAt is the alignment known at base+off for a base of alignment a.
func alignAt(a uint32, off uint64) uint32 {
	a = max(a, 1)
	if lb := off & -off; off != 0 && lb < uint64(a) {
		return uint32(lb)
	}
	return a
}

// stmt emits one statement.
func (fg *fnGen) stmt(s *mir.Statement) {
	fg.setLoc(s.Span)
	switch s.Kind {
	case mir.StAssign:
		fg.assign(s.Place, &s.Rvalue)
	case mir.StScopeEnd, mir.StNop:
		// no lifetime markers
	case mir.StJoin:
		fg.join(s)
	case mir.StAsm:
		fg.asmStmt(&fg.b.Asms[s.Asm])
	default:
		source.ICEf(fg.spanOf(s.Span), "codegen: statement kind %d", s.Kind)
	}
}

// spanOf is the source span of a MIR span.
func (fg *fnGen) spanOf(id mir.SpanID) source.Span {
	if int(id) < len(fg.b.Spans) {
		return fg.b.Spans[id]
	}
	return fg.b.Spans[0]
}

// finish defines the var globals the driver assigned to this pack, whether
// or not a member referenced them.
func (g *packGen) finish() {
	for _, gl := range g.u.Globals {
		if gl.Pack != g.p {
			continue
		}
		switch d := g.items.Get(gl.Item).Decl.(type) {
		case *types.GlobalDecl:
			g.defineGlobal(gl.Item, d)
		case *types.ConstDecl:
			g.sectionConst(gl.Item, d) // emitted read or not
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
