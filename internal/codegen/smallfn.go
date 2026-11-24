// Why: ThinLTO imports a callee into another pack only under an instruction
// limit, so small or once-called Mk functions stay calls across packs.
//
// What: smallSetOf and smallSet (the unit's small-function and cross-pack
// sole-call marks), smallFn, crossSole, smallEligible, crossEligible,
// smallCalls, ctModules and codegenCalled.
//
// How: this widens solecall.go's rule with two marks that put alwaysinline
// on a function's definition in a release build, so its calls are inlined
// in every pack, not only its own.
//
//   - The small-function rule: a small function (below) is marked, called
//     from any number of sites in any pack.
//   - The cross-pack sole-call rule: a function whose one direct call site
//     in the unit is in another pack is marked when soleCallAttr's
//     conditions hold between it and that caller (crossEligible). One site
//     duplicates no code, so this mark has no size bound beyond
//     soleCallOps. Its loop condition reads the loops that end up in the
//     function's body (smallSet.loops), not its effect's: a looping
//     callee of a function that is not thin stays a call there, so a
//     dispatcher such as std.hash.Sip13 (SipShort inlined, SipKeyed's loop
//     a call) may go into a caller's loop.
//
// Why the definition: ThinLTO imports a callee into another pack's module
// only while its summary's instruction count stays under
// -import-instr-limit (100), scaled by 0.7 per level of the import chain,
// and the backend's inliner then still weighs it against its threshold
// (250 at -O3). A summary flagged alwaysinline is imported whatever its
// size, and the always-inliner takes it whatever its cost. A call-site
// attribute does neither for a callee in another pack, which is why
// soleCallAttr keeps to the caller's pack. Two probes with tools/bench ir,
// each marking one definition by hand, showed both parts: std.core's
// Hasher.U64 (46 IR instructions) fails its import into mappair's packs as
// TooLarge at the thresholds 34 and 23, and is imported and inlined when
// marked; HeapList[u64].Push, called once from the fixed row's Round, is
// imported into Round's pack but not inlined by the cost model, and is
// inlined when marked.
//
// A function's effect is its operations (statements plus terminators,
// bodyOps) plus, once per call, the effect of each callee that may be
// inlined into it: every small callee, and every callee with one direct
// call site in the unit (soleAnyIn: in any pack, not AddrTaken, Spawned,
// naked, wrappedMain or codegenCalled), whatever its size, so a thin
// wrapper of a large function called once is not small. A loop in the
// function or in a callee it counts is a loop of the effect. The effects
// are computed over the strongly connected components of the unit's
// direct calls, callees first (smallSetOf), so they do not depend on the
// order in which functions are asked about, and a component that is a
// cycle (a call of itself included) gets soleHuge.
//
// A function is small when its effect is at most smallOps with no loop,
// and it is eligible:
//
//   - it has a body and is not an intrinsic;
//   - it has no cpu clause and inherits none: an alwaysinline callee skips
//     LLVM's feature check (solecall.go), so a cpu body would be inlined
//     into a baseline caller; a baseline body inlined into a cpu caller is
//     what LLVM does anyway;
//   - it is not naked, not the Main the report wrapper calls (wrappedMain),
//     not a std.libc function (its no-builtins attribute is the C
//     library's), not in a section, not Spawned, holds no asm statement,
//     does not return never (a panic path stays one cold call), and is not
//     a function codegen calls by itself (codegenCalled: the panic hooks
//     and runtime entries), whose MIR call sites are not all its calls.
//
// Neither mark goes on a function of std.crypto or std.simd.kernel, or on
// one they reach by direct calls (ctModules): std's constant-time tests
// read those functions' conditional jumps one by one against a recorded
// reading, and the readings were taken without these rules.
//
// Both marks read other packs' bodies (a callee's effect; the cross mark
// the caller's loops, blocks, features and outside bit), so they are a
// unit-wide read: PackFacts writes each member's marks and, per direct
// call, its callee's facts and effect, and internal/driver's prekey tests
// cover both. The rules are a function of the unit's MIR alone, so equal
// units give equal IR. The out-of-line definition stays only while
// something still uses it (a fn value, a call LLVM could not inline);
// otherwise the linker drops it with its FuncTable row, as for any
// function inlined everywhere, and a reader of the executable by symbol
// (bench ir -func, TestSTDCRYPTO_ConstantTimeDisassembly) finds it inlined
// into its callers.

package codegen

import (
	"strings"
	"sync"

	"mk/internal/mir"
	"mk/internal/types"
)

// smallOps is the largest effect a small function may have: half of
// inlineOps, the bound of a hot inline candidate under --pgo (pgo_use.go),
// because without a profile every call is inlined, cold ones too. Read on
// the release hello world (tests/programs/hello.mk, the 65,536-byte budget
// of TestHelloWorldWithinBudget), with a build of these rules whose bound
// came from the environment (mk build --release --no-cache, file sizes,
// measured when the rules were written): x86_64-windows is 63,488 bytes
// without either rule, with the cross-pack rule alone, and at every bound
// up to 40, then 67,072 at 48 and 69,120 at 64 (inlineOps itself);
// x86_64-linux is 51,992 without either rule and with the cross-pack rule
// alone, 51,872 at 24, 51,784 at 32, 52,432 at 40 and 53,104 at 48 and 64.
// Past 32 the growth is in the runtime's report and trace writers, which
// call many small functions once each.
const smallOps = 32

// smallSet is the unit's inlining facts of this file, computed once
// (smallSetOf), indexed by Funcs index.
type smallSet struct {
	once sync.Once
	// ok is set when the computation finished. One stopped by an ICE (a
	// panic source.Guard catches, as PackFacts' does) is dropped from the
	// unit, so the next reader computes again rather than reading an empty
	// set: an empty set silently strips every mark from the unit's IR
	// (TestConstImmArgPackKeyed).
	ok bool
	// fx is each function's effect, small whether it is small, cross
	// whether it gets the cross-pack sole-call mark.
	fx    []soleEffect
	small []bool
	cross []bool
	// loops is set when a function's body holds a loop once the marks
	// inline what they may into it: its own, or, in a thin function (no
	// loop, at most thinCallerBlocks blocks), one of a callee its effect
	// counts. Both sole-call rules inline a looping callee only into a
	// thin caller, so a looping callee of any other function stays a call.
	// fx.loops counts that callee too. crossEligible reads this, and
	// soleEffectOf for a marked callee (smallLoops).
	loops []bool
	// calls is each direct call site's callee, by the index emission calls
	// (callIndex), one entry per site; callsTo counts them per callee and
	// site is a callee's last caller.
	calls   [][]int
	callsTo []int32
	site    []int
	// ctReach is set on a function of a constant-time module and on every
	// function its direct calls reach (ctModule): neither mark goes on
	// one.
	ctReach []bool
}

// ctModules are the key prefixes of the modules whose functions' machine
// code std's constant-time tests read jump by jump against a recorded
// reading (TestSTDCRYPTO_ConstantTimeDisassembly,
// TestSTDCRYPTO_TlsConstantTimeDisassembly). Neither
// mark goes on a function they reach by direct calls, so the rules change
// no instruction LLVM can inline into them and a reading stays valid.
var ctModules = []string{"std.crypto.", "std.simd.kernel."}

// codegenCalled reports whether Funcs[fi] is a std function codegen itself
// calls, outside any MIR call: a check's panic hook (check.go), a runtime
// entry of s.go, a scope or a deadline (spawn.go), the system-stack pair
// of an extern call (sysstack.go) and the float remainder (rvalue.go).
// Their MIR call sites are not all their calls, so neither mark goes on
// them: the cross-pack rule's one site would not be one, and a small one
// would be copied into every check, spawn or extern call.
func (g *packGen) codegenCalled(fi int) bool {
	k := g.items.Get(g.u.Funcs[fi].Item).Key
	switch k {
	case keyEnter, keySpawnAlloc, keySpawn, keyJoin, keyCancel, keySystemStack, keySystemStackDone, keyRem64, keyRem32, keyCoverCmp,
		types.ItemKey(g.hookModule(), hookPanic), types.ItemKey(g.hookModule(), hookPanicMsg), types.ItemKey(g.hookModule(), hookPanicErr):
		return true
	}
	for _, d := range deadlineEntries {
		if k == d {
			return true
		}
	}
	return false
}

// ctModule reports whether Funcs[fi] belongs to a module of ctModules.
func (g *packGen) ctModule(fi int) bool {
	k := g.items.Get(g.u.Funcs[fi].Item).Key.String()
	for _, m := range ctModules {
		if strings.HasPrefix(k, m) {
			return true
		}
	}
	return false
}

// smallFn reports whether Funcs[fi] is small: release only. Each of the
// three readers answers "no" for an index the set does not hold, which is
// only possible when its computation stopped at an internal error (the
// error's own report then stands, not an index panic).
func (g *packGen) smallFn(fi int) bool {
	if !g.cfg.Profile.Optimized() || fi < 0 {
		return false
	}
	s := g.smallSetOf()
	return fi < len(s.small) && s.small[fi]
}

// crossSole reports whether Funcs[fi] gets the cross-pack sole-call mark:
// release only.
func (g *packGen) crossSole(fi int) bool {
	if !g.cfg.Profile.Optimized() || fi < 0 {
		return false
	}
	s := g.smallSetOf()
	return fi < len(s.cross) && s.cross[fi]
}

// smallFx is Funcs[fi]'s effect under the rule (zero outside release).
func (g *packGen) smallFx(fi int) soleEffect {
	if !g.cfg.Profile.Optimized() || fi < 0 {
		return soleEffect{}
	}
	s := g.smallSetOf()
	if fi >= len(s.fx) {
		return soleEffect{}
	}
	return s.fx[fi]
}

// smallLoops is whether a loop ends up in Funcs[fi]'s body once the marks
// inline what they may (smallSet.loops; false outside release).
func (g *packGen) smallLoops(fi int) bool {
	if !g.cfg.Profile.Optimized() || fi < 0 {
		return false
	}
	s := g.smallSetOf()
	return fi < len(s.loops) && s.loops[fi]
}

// soleAnyIn reports whether Funcs[j] has one direct call site in the unit
// (by s's counts) and none of the uses that keep the sole-call rules away
// (AddrTaken, Spawned, naked, wrappedMain, or a function codegen calls by
// itself, codegenCalled): a caller's effect counts such a callee whatever
// its size and pack, since one of the two rules may inline it there.
func (g *packGen) soleAnyIn(s *smallSet, j int) bool {
	f := &g.u.Funcs[j]
	return s.callsTo[j] == 1 && f.Body != nil && !f.AddrTaken && !f.Spawned && !g.nakedFn(f) && !g.wrappedMain(f) && !g.codegenCalled(j)
}

// smallEligible reports whether Funcs[fi] may be small, by the file
// comment's list.
func (g *packGen) smallEligible(fi int) bool {
	f := &g.u.Funcs[fi]
	if f.Body == nil || f.Spawned || len(f.Body.Asms) > 0 || g.nakedFn(f) || g.wrappedMain(f) || g.libcFn(f) || g.codegenCalled(fi) {
		return false
	}
	fd, ok := g.items.Get(f.Item).Decl.(*types.FuncDecl)
	if !ok || fd.Intrinsic || fd.Section != "" || len(g.cpuOf(fi)) > 0 {
		return false
	}
	return f.Body.Locals[mir.ReturnLocal].Type != types.Never
}

// smallCalls is each direct call of Funcs[fi] to a function with a body,
// intrinsics and spawns aside, one entry per call site, by the Funcs index
// emission calls (callIndex: the variant inheriting a cpu caller's
// features).
func (g *packGen) smallCalls(fi int) []int {
	f := &g.u.Funcs[fi]
	b := f.Body
	cg := &fnGen{g: g, f: f, fi: fi, b: b}
	var out []int
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if !isCall(t) || t.Callee.Kind != mir.CallDirect || t.Spawn != mir.NoLocal {
			continue
		}
		it := g.items.Get(t.Callee.Fn.Item)
		if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Intrinsic {
			continue
		}
		if j := cg.callIndex(t); g.u.Funcs[j].Body != nil {
			out = append(out, j)
		}
	}
	return out
}

// crossEligible reports whether the cross-pack sole-call mark may go on
// Funcs[j], whose one call is in Funcs[c] in another pack: soleCallAttr's
// conditions seen from the callee (solecall.go), plus two the same-pack
// rule never needed, since alwaysinline skips LLVM's attribute check and a
// call in one pack never crossed a module before: no section, and a
// std.libc callee (no-builtins) only into a std.libc caller; and, as for
// small, no never result (soleAnyIn already leaves out what codegen calls
// itself).
func (g *packGen) crossEligible(s *smallSet, j, c int) bool {
	f, cf := &g.u.Funcs[j], &g.u.Funcs[c]
	if s.small[j] || c == j || cf.Pack == f.Pack || cf.Body == nil || g.funcSection(f.Item) != "" || g.libcFn(f) && !g.libcFn(cf) {
		return false
	}
	if f.Body.Locals[mir.ReturnLocal].Type == types.Never {
		return false // a panic path stays one cold call, as for small
	}
	if !featureSubset(g.featuresOf(j), g.featuresOf(c)) {
		return false
	}
	if s.fx[j].ops > soleCallOps || s.loops[j] && (len(cf.Body.Loops) > 0 || len(cf.Body.Blocks) > thinCallerBlocks) {
		return false
	}
	if callInColdBlock(cf.Body, f.Item, g.items) {
		return false
	}
	return !(g.u.soleCallsOf().outside[cf.Item] && len(g.cpuOf(c)) == 0)
}

func callInColdBlock(b *mir.Body, callee types.ItemID, items *types.Items) bool {
	cold := findColdBlocks(b, items)
	if len(cold) == 0 {
		return false
	}
	for bi := range b.Blocks {
		if !cold[mir.BlockID(bi)] {
			continue
		}
		t := &b.Blocks[bi].Term
		if t.Kind == mir.TermCall && t.Callee.Kind == mir.CallDirect && t.Spawn == mir.NoLocal && t.Callee.Fn.Item == callee {
			return true
		}
	}
	return false
}

// smallSetOf computes the unit's facts once. Every function's direct calls
// are resolved first, so each callee's count of call sites is known. Then
// Tarjan's algorithm over those calls, in Funcs order, emits each strongly
// connected component after every component it calls, so each effect is
// final when a caller reads it: its operations plus, per call, the effect
// of a callee that is soleAny or small. A component of more than one
// function, or one that calls itself, is a cycle: its functions get
// soleHuge and a loop. Last, the cross-pack sole-call marks, which read
// the effects.
func (g *packGen) smallSetOf() *smallSet {
	for {
		if s := g.smallSetTry(); s.ok {
			return s
		}
		// Another reader's computation stopped at an ICE (its panic is
		// its own): compute again, in this reader's packGen.
	}
}

// smallSetTry is one attempt of smallSetOf: the unit's set, computed by
// this call or another, and not ok when that computation stopped.
func (g *packGen) smallSetTry() *smallSet {
	u := g.u
	u.idxMu.Lock()
	if u.small == nil || u.smallN != len(u.Funcs) {
		// Appending to Funcs recomputes the set, as index() rebuilds
		// (tests extend a unit between emissions).
		u.small, u.smallN = &smallSet{}, len(u.Funcs)
	}
	s := u.small
	u.idxMu.Unlock()
	s.once.Do(func() {
		defer func() {
			if !s.ok {
				u.idxMu.Lock()
				if u.small == s {
					u.small = nil
				}
				u.idxMu.Unlock()
			}
		}()
		n := len(u.Funcs)
		s.fx = make([]soleEffect, n)
		s.small = make([]bool, n)
		s.cross = make([]bool, n)
		s.loops = make([]bool, n)
		s.calls = make([][]int, n)
		s.callsTo = make([]int32, n)
		s.site = make([]int, n)
		for i := range u.Funcs {
			if u.Funcs[i].Body == nil {
				continue
			}
			s.calls[i] = g.smallCalls(i)
			for _, j := range s.calls[i] {
				s.callsTo[j]++
				s.site[j] = i
			}
		}
		s.ctReach = make([]bool, n)
		var work []int
		for i := range u.Funcs {
			if u.Funcs[i].Body != nil && g.ctModule(i) {
				s.ctReach[i] = true
				work = append(work, i)
			}
		}
		for len(work) > 0 {
			v := work[len(work)-1]
			work = work[:len(work)-1]
			for _, w := range s.calls[v] {
				if !s.ctReach[w] {
					s.ctReach[w] = true
					work = append(work, w)
				}
			}
		}
		index := make([]int, n) // 0: not visited; else the visit number
		low := make([]int, n)
		onStack := make([]bool, n)
		var stack []int
		next := 1
		var visit func(v int)
		visit = func(v int) {
			index[v], low[v] = next, next
			next++
			stack = append(stack, v)
			onStack[v] = true
			for _, w := range s.calls[v] {
				if index[w] == 0 {
					visit(w)
					low[v] = min(low[v], low[w])
				} else if onStack[w] {
					low[v] = min(low[v], index[w])
				}
			}
			if low[v] != index[v] {
				return
			}
			var comp []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			cyclic := len(comp) > 1
			for _, w := range s.calls[v] {
				cyclic = cyclic || w == v
			}
			if cyclic {
				for _, w := range comp {
					s.fx[w] = soleEffect{ops: soleHuge, loops: true}
					s.loops[w] = true
				}
				return
			}
			b := u.Funcs[v].Body
			fx := soleEffect{ops: bodyOps(b), loops: len(b.Loops) > 0}
			thin := len(b.Loops) == 0 && len(b.Blocks) <= thinCallerBlocks
			s.loops[v] = len(b.Loops) > 0
			for _, w := range s.calls[v] {
				if s.small[w] || g.soleAnyIn(s, w) {
					fx.ops = min(fx.ops+s.fx[w].ops, soleHuge)
					fx.loops = fx.loops || s.fx[w].loops
					s.loops[v] = s.loops[v] || thin && s.loops[w]
				}
			}
			s.fx[v] = fx
			s.small[v] = !fx.loops && fx.ops <= smallOps && !s.ctReach[v] && g.smallEligible(v)
		}
		for i := range u.Funcs {
			if u.Funcs[i].Body != nil && index[i] == 0 {
				visit(i)
			}
		}
		for j := range u.Funcs {
			if g.soleAnyIn(s, j) && !s.ctReach[j] {
				s.cross[j] = g.crossEligible(s, j, s.site[j])
			}
		}
		s.ok = true
	})
	return s
}
