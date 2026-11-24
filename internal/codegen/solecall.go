// Why: LLVM inlines a C static helper called once (InlineCost's
// last-call-to-static bonus of 15000, under the threshold of 225 at -O2 and
// 250 at -O3), but no Mk function ever qualifies: every function has a
// second use, its FuncTable row (functable.go), so a large helper called
// once stays a call (std.simd's IndexByteAligned[64], cost 465 against 438,
// called only from IndexByteAvx512).
//
// What: soleCalls and soleCallsOf (the unit's direct call sites per callee
// key, soleKey),
// soleCallAttr (the call-site mark), soleEffectOf and featureSubset, with
// the bounds thinCallerBlocks, soleCallOps and soleHuge.
//
// How: the rule restores LLVM's decision from MIR: in a release build, a direct
// call is marked alwaysinline (a call-site attribute) when its callee, keyed
// by item and generic arguments, is called from exactly one site in the
// whole unit, is defined in the caller's pack (so it needs no import), has
// a body, and nothing else references it (not AddrTaken, not Spawned, not
// the caller itself, not naked, not the Main the report wrapper calls,
// which stays out of line, func.go's wrappedMain). More conditions:
//
//   - the callee's target features are a subset of the caller's. An
//     alwaysinline call skips LLVM's own feature check (InlineCost's
//     attribute decision returns before functionsHaveCompatibleAttributes),
//     so without this a cpu "avx512bw" body would be inlined into a
//     baseline caller and run its instructions there;
//   - a caller that another pack calls is not grown, unless it has a cpu
//     clause or the cross-pack sole-call mark (smallfn.go), so ThinLTO can
//     still import it (the hash-sip13-short benchmark);
//   - the callee is at most soleCallOps operations, so the rule stays
//     inside LLVM's bound instead of inlining whatever the size;
//   - a callee with a loop is inlined only into a thin caller (no loop, at
//     most thinCallerBlocks blocks), the cpu-wrapper shape: inlining nbody's
//     Advance, called once, into Main let SROA keep the 35 state values in
//     registers across the steps loop, which spilled (nbody 1.274 [1.237,
//     1.283] slower, measured with tools/bench compare).
//
// The last two read the callee's effect (soleEffectOf): its body with every
// call the rule could mark in it inlined, recursively, so a loop-free
// wrapper of a looping function counts as looping (Main's loop
// calling Step, Step calling Advance's loop, would otherwise have Advance
// inlined into Step and Step into Main's loop).
//
// Codegen's own calls (traps, hooks) carry no such mark. The rule is a
// function of the unit's MIR alone, so equal units give equal IR.
//
// smallfn.go widens it with two marks on a
// definition, which ThinLTO imports whatever its size: a small function,
// called from any number of sites in any pack, and a function whose one
// call is in another pack, under this rule's conditions seen from the
// callee. The effect here counts such callees at each of their calls.

package codegen

import (
	"strconv"
	"strings"
	"sync"

	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/types"
)

// thinCallerBlocks is the most blocks a caller may have for a looping
// callee to be inlined into it by the rule.
const thinCallerBlocks = 4

// smallLoopSoleOps is the largest own-plus-loop-free-callee operation count
// of a flat single-loop sole callee that soleCallAttr inlines into a call
// site inside a caller's loop (e.g. a hash table's probing helper). Larger
// or nested-loop callees (like nbody's Advance) stay out of line so SROA
// does not spill caller state across an inner loop.
const smallLoopSoleOps = 256

// smallLoopMaxPointee is the largest pointee size of a reference parameter
// allowed on a flat single-loop sole callee inlined into a caller's loop
// (guarding against SROA spilling large mutable state structs across loops).
const smallLoopMaxPointee = 128

// soleCallOps is the largest effect (statements plus terminators,
// bodyOps) a callee may have for the rule to inline it. LLVM's bound is a
// cost under 15000 plus the threshold, at 5 per IR instruction about 3050
// instructions; codegen emits at least one IR instruction per operation,
// so a body past about 3050 operations is certainly over it, and 1000
// keeps the rule inside it while an operation averages at most 3 (an
// estimate from the lowering of checked arithmetic and bounds checks, not
// measured).
const soleCallOps = 1000

// soleHuge is the effect size of a function on a cycle of sole calls, so
// no call of the cycle is marked.
const soleHuge = 1 << 30

// soleCalls counts the direct call sites of each callee key over a unit.
type soleCalls struct {
	once  sync.Once
	count map[string]int32
	// outside[item] is set when a call to item sits in a pack other than
	// the one of a function of that item.
	outside map[types.ItemID]bool
}

// soleKey is a call's callee key: its item and its FnRef's generic
// arguments. Instances that differ only by a const value argument share a
// key, which only undercounts the calls that are alone.
func soleKey(b *mir.Body, t *mir.Terminator) string {
	var sb strings.Builder
	sb.WriteString(strconv.FormatUint(uint64(t.Callee.Fn.Item), 36))
	for _, a := range b.GenericArgs[t.Callee.Fn.Args.Start:t.Callee.Fn.Args.End()] {
		sb.WriteByte(',')
		if a.IsConst {
			sb.WriteString("c" + strconv.FormatUint(uint64(a.Const.Kind), 36) + "." + strconv.FormatUint(uint64(a.Const.Param), 36) +
				"." + strconv.FormatUint(uint64(a.Const.Value), 36) + "." + strconv.FormatUint(uint64(a.Const.Type), 36))
			continue
		}
		sb.WriteString(strconv.FormatUint(uint64(a.Type), 36))
	}
	return sb.String()
}

func (u *Unit) soleCallsOf() *soleCalls {
	u.idxMu.Lock()
	if u.sole == nil {
		u.sole = &soleCalls{}
	}
	s := u.sole
	u.idxMu.Unlock()
	s.once.Do(func() {
		s.count = map[string]int32{}
		s.outside = map[types.ItemID]bool{}
		home := map[types.ItemID]int{}
		for i := range u.Funcs {
			if u.Funcs[i].Body != nil {
				if p, ok := home[u.Funcs[i].Item]; ok && p != u.Funcs[i].Pack {
					s.outside[u.Funcs[i].Item] = true
				}
				home[u.Funcs[i].Item] = u.Funcs[i].Pack
			}
		}
		for i := range u.Funcs {
			b := u.Funcs[i].Body
			if b == nil {
				continue
			}
			for bi := range b.Blocks {
				t := &b.Blocks[bi].Term
				if isCall(t) && t.Callee.Kind == mir.CallDirect {
					s.count[soleKey(b, t)]++
					if p, ok := home[t.Callee.Fn.Item]; !ok || p != u.Funcs[i].Pack {
						s.outside[t.Callee.Fn.Item] = true
					}
				}
			}
		}
	})
	return s
}

// soleCallAttr adds alwaysinline to attrs when the call t to fi meets the
// file comment's rule.
func (fg *fnGen) soleCallAttr(t *mir.Terminator, fi int, attrs llvm.Attrs) llvm.Attrs {
	g := fg.g
	if !g.cfg.Profile.Optimized() || fi == fg.fi || fg.fi < 0 || t.Spawn != mir.NoLocal || attrs.Has("alwaysinline") || fg.coldBlock[mir.BlockID(fg.bi)] {
		return attrs // the last: a small callee's definition mark, repeated by callAttrs
	}
	f := &g.u.Funcs[fi]
	if f.Body == nil || f.AddrTaken || f.Spawned || f.Pack != g.p || g.nakedFn(f) || g.wrappedMain(f) {
		return attrs
	}
	if !featureSubset(g.featuresOf(fi), g.featuresOf(fg.fi)) {
		return attrs
	}
	sc := g.u.soleCallsOf()
	if sc.count[soleKey(fg.b, t)] != 1 {
		return attrs
	}
	fx := g.soleEffectOf(fi)
	if fx.ops > soleCallOps {
		return attrs
	}
	if fx.loops && (len(fg.b.Loops) > 0 || len(fg.b.Blocks) > thinCallerBlocks) {
		if !fg.onLeafLoopSpine(fg.bi) || !g.smallFlatLoop(fi) {
			return attrs
		}
	}
	// A caller that another pack calls stays small enough for ThinLTO to
	// import it there (-import-instr-limit 100), unless it has a cpu clause
	// and so is never inlined into a baseline caller anyway, or it has the
	// cross-pack sole-call mark (smallfn.go), which ThinLTO imports
	// whatever its size into its one call, so growing it duplicates
	// nothing: inlining SipShort into an unmarked hash.Sip13 slowed
	// std-hashmod/hash-sip13-short, because Sip13's summary then passed the
	// limit and the benchmark's loop called Sip13. A function codegen calls
	// by itself (codegenCalled) never takes that mark, since its MIR call
	// sites are not all its calls; the waiver checks that again rather than
	// lean on how smallSet chose the mark.
	if sc.outside[g.u.Funcs[fg.fi].Item] && len(g.cpuOf(fg.fi)) == 0 && (!g.crossSole(fg.fi) || g.codegenCalled(fg.fi)) {
		return attrs
	}
	return attrs.Flag("alwaysinline")
}

// soleEffect is a function's size and loops once the calls the rule could
// mark in it are inlined.
type soleEffect struct {
	ops   int  // bodyOps, plus each such callee's ops; soleHuge on a cycle
	loops bool // the body or such a callee has a loop
	busy  bool // being computed (a cycle when met again)
}

// soleEffectOf is Funcs[fi]'s effect, fi in this pack. It counts a direct
// call as inlined when the callee key has one call in the unit and the
// callee is another function of this pack with a body: a superset of the
// calls soleCallAttr marks (it ignores features, the cross-pack caller
// condition and its own size and loop conditions), so the effect is never
// smaller than what inlining adds. A call of a small function (smallfn.go),
// in any pack, or of one with the cross-pack mark adds that function's
// effect (smallFx), since either is inlined.
func (g *packGen) soleEffectOf(fi int) soleEffect {
	if g.soleFx == nil {
		g.soleFx = map[int]soleEffect{}
	}
	if fx, ok := g.soleFx[fi]; ok {
		if fx.busy {
			return soleEffect{ops: soleHuge, loops: true}
		}
		return fx
	}
	g.soleFx[fi] = soleEffect{busy: true}
	f := &g.u.Funcs[fi]
	b := f.Body
	fx := soleEffect{ops: bodyOps(b), loops: len(b.Loops) > 0}
	sc := g.u.soleCallsOf()
	cg := &fnGen{g: g, f: f, fi: fi, b: b}
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if !isCall(t) || t.Callee.Kind != mir.CallDirect || t.Spawn != mir.NoLocal {
			continue
		}
		it := g.items.Get(t.Callee.Fn.Item)
		if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Intrinsic && !supportBodies[it.Key.String()] {
			continue
		}
		// A small callee (smallfn.go) is inlined wherever it is, and one
		// with the cross-pack mark is inlined here, its one call, so
		// either counts with its whole effect, and with a loop when one
		// ends up in its body (smallLoops: a looping callee of a function
		// that is not thin stays a call there).
		if v := cg.callIndex(t); v != fi && g.u.Funcs[v].Body != nil && (g.smallFn(v) || g.crossSole(v)) {
			fx.ops = min(fx.ops+g.smallFx(v).ops, soleHuge)
			fx.loops = fx.loops || g.smallLoops(v)
			continue
		}
		j, ok := g.findFunc(t.Callee.Fn.Item, cg.calleeArgs(t))
		if !ok || j == fi || g.u.Funcs[j].Body == nil {
			continue
		}
		if sc.count[soleKey(b, t)] != 1 || g.u.Funcs[j].Pack != g.p {
			continue
		}
		e := g.soleEffectOf(j)
		fx.ops = min(fx.ops+e.ops, soleHuge)
		fx.loops = fx.loops || e.loops
	}
	g.soleFx[fi] = fx
	return fx
}

// smallFlatLoop reports whether Funcs[fi] (in this pack) is a small, flat
// single-loop helper safe to inline into an unconditional call site on a
// leaf loop's spine: not a constant-time module, exactly one loop of its own
// (no nested loops), no large mutable/shared pointee parameter (over
// smallLoopMaxPointee bytes), no call inside its loop to another looping
// callee, and own ops plus loop-free inlined callees at most smallLoopSoleOps.
func (g *packGen) smallFlatLoop(fi int) bool {
	f := &g.u.Funcs[fi]
	b := f.Body
	if b == nil || len(b.Loops) != 1 || g.ctModule(fi) {
		return false
	}
	for i := 1; i <= int(b.ParamCount); i++ {
		ty := g.tt.Get(b.Locals[i].Type)
		if ty.Kind == types.KRef || ty.Kind == types.KMut {
			if sz, _ := g.sizeAlign(ty.Elem); sz > smallLoopMaxPointee {
				return false
			}
		}
	}
	ops := bodyOps(b)
	if ops > smallLoopSoleOps {
		return false
	}
	sc := g.u.soleCallsOf()
	cg := &fnGen{g: g, f: f, fi: fi, b: b}
	var inAny []bool
	for bi := range b.Blocks {
		t := &b.Blocks[bi].Term
		if !isCall(t) || t.Callee.Kind != mir.CallDirect || t.Spawn != mir.NoLocal {
			continue
		}
		it := g.items.Get(t.Callee.Fn.Item)
		if fd, ok := it.Decl.(*types.FuncDecl); ok && fd.Intrinsic && !supportBodies[it.Key.String()] {
			continue
		}
		if v := cg.callIndex(t); v != fi && g.u.Funcs[v].Body != nil && (g.smallFn(v) || g.crossSole(v)) {
			if g.smallLoops(v) {
				return false
			}
			ops += g.smallFx(v).ops
			if ops > smallLoopSoleOps {
				return false
			}
			continue
		}
		j, ok := g.findFunc(t.Callee.Fn.Item, cg.calleeArgs(t))
		if !ok || j == fi || g.u.Funcs[j].Body == nil {
			continue
		}
		if sc.count[soleKey(b, t)] != 1 || g.u.Funcs[j].Pack != g.p {
			continue
		}
		e := g.soleEffectOf(j)
		if e.loops {
			// A looping callee called outside our loop (e.g. cold TableGrow)
			// stays out of line; one inside our loop would nest if inlined.
			if inAny == nil {
				_, inAny = computeLoopInfo(b)
			}
			if inAny[bi] {
				return false
			}
			continue
		}
		ops += e.ops
		if ops > smallLoopSoleOps {
			return false
		}
	}
	return true
}

// onLeafLoopSpine reports whether block bi lies on the unconditional spine
// (dominating every latch) of a leaf natural loop of fg.b, and fg is not in a
// constant-time module.
func (fg *fnGen) onLeafLoopSpine(bi int) bool {
	b := fg.b
	if b == nil || len(b.Loops) == 0 || bi < 0 || bi >= len(b.Blocks) || fg.g.ctModule(fg.fi) {
		return false
	}
	if fg.loopBlks == nil {
		fg.loopBlks, _ = computeLoopInfo(b)
	}
	return fg.loopBlks[bi]
}

// computeLoopInfo finds the natural loops of b using dominators over reachable
// blocks and returns two per-block masks:
//   - spine[bi]: bi belongs to a leaf natural loop (one containing no inner
//     loop header) and dominates every latch of that loop (so it executes on
//     every full iteration, never only on a cold conditional branch);
//   - inAny[bi]: bi belongs to any natural loop of b.
func computeLoopInfo(b *mir.Body) (spine, inAny []bool) {
	nb := len(b.Blocks)
	spine = make([]bool, nb)
	inAny = make([]bool, nb)
	if nb == 0 || len(b.Loops) == 0 {
		return spine, inAny
	}
	succ := make([][]mir.BlockID, nb)
	pred := make([][]mir.BlockID, nb)
	for i := range nb {
		succ[i] = b.Successors(mir.BlockID(i), nil)
	}
	pos := make([]int32, nb)
	for i := range pos {
		pos[i] = -1
	}
	seen := make([]bool, nb)
	type frame struct {
		blk mir.BlockID
		i   int
	}
	var post []mir.BlockID
	stack := []frame{{mir.EntryBlock, 0}}
	seen[mir.EntryBlock] = true
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.i < len(succ[f.blk]) {
			s := succ[f.blk][f.i]
			f.i++
			if !seen[s] {
				seen[s] = true
				stack = append(stack, frame{s, 0})
			}
			continue
		}
		post = append(post, f.blk)
		stack = stack[:len(stack)-1]
	}
	rpo := make([]mir.BlockID, len(post))
	for i := range post {
		blk := post[len(post)-1-i]
		rpo[i] = blk
		pos[blk] = int32(i)
	}
	for _, u := range rpo {
		for _, s := range succ[u] {
			pred[s] = append(pred[s], u)
		}
	}
	idom := make([]int32, nb)
	for i := range idom {
		idom[i] = -1
	}
	idom[mir.EntryBlock] = int32(mir.EntryBlock)
	inter := func(x, y int32) int32 {
		for x != y {
			for pos[x] > pos[y] {
				x = idom[x]
			}
			for pos[y] > pos[x] {
				y = idom[y]
			}
		}
		return x
	}
	for changed := true; changed; {
		changed = false
		for _, blk := range rpo[1:] {
			nd := int32(-1)
			for _, q := range pred[blk] {
				if idom[q] < 0 {
					continue
				}
				if nd < 0 {
					nd = int32(q)
				} else {
					nd = inter(int32(q), nd)
				}
			}
			if nd >= 0 && idom[blk] != nd {
				idom[blk] = nd
				changed = true
			}
		}
	}
	dom := func(h, u mir.BlockID) bool {
		x := int32(u)
		for {
			if x == int32(h) {
				return true
			}
			if x == int32(mir.EntryBlock) || idom[x] < 0 {
				return false
			}
			x = idom[x]
		}
	}
	type natLoop struct {
		header  mir.BlockID
		latches []mir.BlockID
		members []mir.BlockID
		has     []bool
	}
	at := make([]int, nb)
	for i := range at {
		at[i] = -1
	}
	var loops []natLoop
	var work []mir.BlockID
	for _, u := range rpo {
		for _, h := range succ[u] {
			if !dom(h, u) {
				continue
			}
			li := at[h]
			if li < 0 {
				li = len(loops)
				at[h] = li
				has := make([]bool, nb)
				has[h] = true
				loops = append(loops, natLoop{header: h, has: has, members: []mir.BlockID{h}})
				inAny[h] = true
			}
			lp := &loops[li]
			lp.latches = append(lp.latches, u)
			if !lp.has[u] {
				lp.has[u] = true
				lp.members = append(lp.members, u)
				inAny[u] = true
				work = append(work, u)
			}
			for len(work) > 0 {
				cur := work[len(work)-1]
				work = work[:len(work)-1]
				for _, p := range pred[cur] {
					if p != h && !lp.has[p] && dom(h, p) {
						lp.has[p] = true
						lp.members = append(lp.members, p)
						inAny[p] = true
						work = append(work, p)
					}
				}
			}
		}
	}
	for i := range loops {
		lp := &loops[i]
		leaf := true
		for j := range loops {
			if j != i && lp.has[loops[j].header] {
				leaf = false
				break
			}
		}
		if !leaf {
			continue
		}
		for _, blk := range lp.members {
			all := true
			for _, u := range lp.latches {
				if !dom(blk, u) {
					all = false
					break
				}
			}
			if all {
				spine[blk] = true
			}
		}
	}
	return spine, inAny
}

// featureSubset reports whether a "target-features" string callee enables
// nothing caller does not, and both disable the same features.
func featureSubset(callee, caller string) bool {
	if callee == caller {
		return true
	}
	have := map[string]bool{}
	var offCaller, offCallee []string
	for _, x := range strings.Split(caller, ",") {
		if strings.HasPrefix(x, "-") {
			offCaller = append(offCaller, x)
		}
		have[x] = true
	}
	for _, x := range strings.Split(callee, ",") {
		if x == "" {
			continue
		}
		if strings.HasPrefix(x, "-") {
			offCallee = append(offCallee, x)
			continue
		}
		if !have[x] {
			return false
		}
	}
	if len(offCaller) != len(offCallee) {
		return false
	}
	for _, x := range offCallee {
		if !have[x] {
			return false
		}
	}
	return true
}
