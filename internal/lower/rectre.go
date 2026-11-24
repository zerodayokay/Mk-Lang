// Why: a self-recursive function whose tail call feeds a checked addition
// (like `return 1 + Check(l) + Check(r)`) or an associative-commutative
// wrapping/bitwise operator cannot be turned into a loop by LLVM's
// TailCallElim because Mk's checked `+` traps on overflow.
//
// What: elimAccTailRec, a codegen-only rewrite called from VersionLoops
// (rangeversion.go) that turns direct and accumulator self-tail-recursion
// into a loop.
//
// How: for wrapping and bitwise associative-commutative operators (+%, *%,
// |, &, ^) the rewrite maintains an accumulator initialized to the operator's
// identity. For checked + (Add / AddNoWrap) on unsigned integers, or on
// signed integers where an inductive non-negativity analysis proves every
// base return and every accumulated term is >= 0, the loop accumulates with
// wrapping AddWrap and records overflow branchlessly in a sticky bitmask
// (mask |= acc for signed; ovf |= (next < acc) for unsigned). Because every
// term is non-negative, any prefix sum that overflows signed N-bit first
// lands in [2^(N-1), 2^N - 2], setting the sign bit of acc and therefore of
// mask permanently; checking mask < 0 (or ovf) at base returns traps with
// PanicOverflow at the exact add span while preserving all panic and
// side-effect ordering of deeper recursive steps.

package lower

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

type accOp uint8

const (
	accNone accOp = iota
	accAddChecked
	accAddWrap
	accMulWrap
	accBitOr
	accBitAnd
	accBitXor
)

type tailRecSite struct {
	callBlk mir.BlockID
	op      accOp
	terms   []mir.Operand
	addSpan mir.SpanID
	hasSpan bool
}

// elimAccTailRec turns eligible direct and accumulator self-tail-recursion in
// b into a loop, returning a cloned body (leaving b untouched) or b itself
// when no self-tail-call qualifies.
func elimAccTailRec(cfg *Config, b *mir.Body, opt VersionOptions) *mir.Body {
	if b == nil || b.GenericCount != 0 || b.Item == types.NoItem ||
		len(b.Blocks) == 0 || len(b.Blocks) > verMaxBlocks || len(b.Defers) != 0 {
		return b
	}
	tab := cfg.Types
	if !hasSelfCallAndSafeFrame(b, tab) {
		return b
	}

	retT := b.Locals[mir.ReturnLocal].Type
	retIn := intOf(tab, retT)
	isInt := retIn.ok && retT != types.Bool

	g := newVcfg(b)
	var sites []tailRecSite
	for _, bi := range g.rpo {
		if s, ok := matchTailRecSite(b, bi, retT, isInt, opt.NoReduce); ok {
			sites = append(sites, s)
		}
	}
	if len(sites) == 0 {
		return b
	}

	// Choose the accumulator operator with the most matching sites (or accNone
	// for pure direct tail recursion).
	counts := [accBitXor + 1]int{}
	var order []accOp
	for _, s := range sites {
		if s.op != accNone {
			if counts[s.op] == 0 {
				order = append(order, s.op)
			}
			counts[s.op]++
		}
	}
	chosen := accNone
	best := 0
	for _, op := range order {
		if counts[op] > best {
			best = counts[op]
			chosen = op
		}
	}

	// Keep sites compatible with chosen (exact op match, or direct tail calls
	// which accumulate the identity).
	var active []tailRecSite
	var ovfSpan mir.SpanID
	hasOvfSpan := false
	spanConflict := false
	for _, s := range sites {
		if s.op != accNone && s.op != chosen {
			continue
		}
		if s.op == accAddChecked {
			if !hasOvfSpan {
				ovfSpan, hasOvfSpan = s.addSpan, true
			} else if s.addSpan != ovfSpan {
				spanConflict = true
			}
		}
		active = append(active, s)
	}

	// For checked addition, require a single panic span across all checked
	// tail-add sites (so the exact panic location is preserved) and, for
	// signed return types, an inductive proof that all base returns and
	// accumulated terms are non-negative.
	if chosen == accAddChecked {
		ok := hasOvfSpan && !spanConflict
		if ok && retIn.signed {
			ok = proveNonNeg(cfg, b, g, active)
		}
		if !ok {
			// Fall back to direct tail calls only, if any exist.
			chosen = accNone
			active = active[:0]
			for _, s := range sites {
				if s.op == accNone {
					active = append(active, s)
				}
			}
		}
	}
	if len(active) == 0 {
		return b
	}
	if writesImmutAggregateParam(b, tab, active) {
		return b
	}

	return rewriteAccTailRec(b, active, chosen, retT, retIn, ovfSpan)
}

// writesImmutAggregateParam reports whether any active tail-call site updates
// a non-var parameter whose type may travel Indirect in the ABI. Codegen marks
// such parameters readonly (immutarg.go) and rejects direct writes to them.
func writesImmutAggregateParam(b *mir.Body, tab *types.Table, sites []tailRecSite) bool {
	for _, s := range sites {
		t := &b.Blocks[s.callBlk].Term
		args := b.Args[t.Args.Start:t.Args.End()]
		for i := range int(b.ParamCount) {
			p := mir.LocalID(i + 1)
			l := b.Locals[p]
			if l.Var || isDirectScalarParam(tab, l.Type) {
				continue
			}
			if !argUnchanged(args[i], p) {
				return true
			}
		}
	}
	return false
}

func isDirectScalarParam(tab *types.Table, tid types.TypeID) bool {
	switch tab.Kind(tid) {
	case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KRef, types.KMut, types.KRawPtr, types.KVoid:
		return true
	default:
		return false
	}
}

// isDirectSelfCall reports whether t is a direct non-fallible, non-spawned
// self-call writing a whole destination local with matching arity.
func isDirectSelfCall(b *mir.Body, t *mir.Terminator) bool {
	return t.Kind == mir.TermCall &&
		t.Callee.Kind == mir.CallDirect &&
		t.Callee.Fn.Item == b.Item &&
		t.Callee.Fn.Args.Len == 0 &&
		!t.HasErr &&
		t.Spawn == mir.NoLocal &&
		t.Dest.Proj.Len == 0 &&
		int(t.Args.Len) == int(b.ParamCount)
}

// hasSelfCallAndSafeFrame is a cheap single pass over b checking that:
//   - at least one direct self-call exists,
//   - no unsupported terminators/statements/scopes exist, and
//   - no pointer into this function's own stack frame is ever formed (so
//     reusing the frame across loop iterations cannot alias across frames).
func hasSelfCallAndSafeFrame(b *mir.Body, tab *types.Table) bool {
	for i := range b.Locals {
		if b.Locals[i].Kind == mir.LocalScope {
			return false
		}
	}
	hasSelf := false
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		if blk.Origin != mir.OriginBody {
			return false
		}
		t := &blk.Term
		switch t.Kind {
		case mir.TermTailCall, mir.TermOnerrGuard, mir.TermTypeTest:
			return false
		case mir.TermReturn:
			if t.Exit != mir.ExitNormal || t.NewError {
				return false
			}
		case mir.TermCall:
			if isDirectSelfCall(b, t) {
				hasSelf = true
			}
		}
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			switch st.Kind {
			case mir.StJoin, mir.StAsm:
				return false
			case mir.StAssign:
				rv := &st.Rvalue
				switch rv.Kind {
				case mir.RvRef:
					if !placeHasDeref(b, rv.Place) {
						return false
					}
				case mir.RvSlice:
					if !placeHasDeref(b, rv.Place) && tab.Kind(b.PlaceType(rv.Place)) == types.KArray {
						return false
					}
				}
			}
		}
	}
	return hasSelf
}

func placeHasDeref(b *mir.Body, pl mir.Place) bool {
	for _, pr := range b.Proj(pl) {
		if pr.Kind == mir.ProjDeref {
			return true
		}
	}
	return false
}

func classifyAccOp(op mir.BinOp, noChecked bool) accOp {
	switch op {
	case mir.Add, mir.AddNoWrap:
		if noChecked {
			return accNone
		}
		return accAddChecked
	case mir.AddWrap:
		return accAddWrap
	case mir.MulWrap:
		return accMulWrap
	case mir.BitOr:
		return accBitOr
	case mir.BitAnd:
		return accBitAnd
	case mir.BitXor:
		return accBitXor
	default:
		return accNone
	}
}

func accBinOp(op accOp) mir.BinOp {
	switch op {
	case accAddChecked, accAddWrap:
		return mir.AddWrap
	case accMulWrap:
		return mir.MulWrap
	case accBitOr:
		return mir.BitOr
	case accBitAnd:
		return mir.BitAnd
	case accBitXor:
		return mir.BitXor
	default:
		return 0
	}
}

func accIdentity(op accOp, in intInfo) uint64 {
	switch op {
	case accMulWrap:
		return 1
	case accBitAnd:
		return uint64(1)<<(in.bits-1)<<1 - 1
	default:
		return 0
	}
}

// argUnchanged reports whether call argument a simply passes parameter p back
// to itself unchanged.
func argUnchanged(a mir.Arg, p mir.LocalID) bool {
	return (a.Operand.Kind == mir.OpCopy || a.Operand.Kind == mir.OpMove) &&
		a.Operand.Place.Proj.Len == 0 && a.Operand.Place.Local == p
}

// matchTailRecSite checks whether block callBlk ends in an eligible self-call
// whose continuation performs only copies and zero or more associative-
// commutative combines with already-evaluated terms, then returns normally.
func matchTailRecSite(b *mir.Body, callBlk mir.BlockID, retT types.TypeID, isInt, noChecked bool) (tailRecSite, bool) {
	t := &b.Blocks[callBlk].Term
	if !isDirectSelfCall(b, t) {
		return tailRecSite{}, false
	}
	// Every changed parameter must be assignable in MIR without violating M10
	// (an ArgReborrow of a different mut local stays out of StAssign).
	args := b.Args[t.Args.Start:t.Args.End()]
	for i := range int(b.ParamCount) {
		p := mir.LocalID(i + 1)
		a := args[i]
		if argUnchanged(a, p) {
			continue
		}
		switch a.Mode {
		case mir.ArgCopy:
		case mir.ArgMove:
			if a.Operand.Kind == mir.OpCopy && a.Operand.Place.Proj.Len == 0 {
				return tailRecSite{}, false
			}
		default:
			return tailRecSite{}, false
		}
	}

	cur := t.Dest.Local
	written := map[mir.LocalID]bool{cur: true}
	constAlias := map[mir.LocalID]mir.Operand{}
	site := tailRecSite{callBlk: callBlk}

	var visited [8]mir.BlockID
	hops := 0
	blkID := t.Target
	for {
		if hops >= len(visited) {
			return tailRecSite{}, false
		}
		for i := range hops {
			if visited[i] == blkID {
				return tailRecSite{}, false
			}
		}
		visited[hops] = blkID
		hops++

		blk := &b.Blocks[blkID]
		for si := range blk.Stmts {
			st := &blk.Stmts[si]
			switch st.Kind {
			case mir.StNop, mir.StScopeEnd:
				continue
			case mir.StAssign:
			default:
				return tailRecSite{}, false
			}
			if st.Place.Proj.Len != 0 {
				return tailRecSite{}, false
			}
			dst := st.Place.Local
			rv := &st.Rvalue
			switch {
			case rv.Kind == mir.RvUse && wholeOf(rv.A, cur):
				cur = dst
				written[dst] = true
				delete(constAlias, dst)
			case rv.Kind == mir.RvUse && (rv.A.Kind == mir.OpImm || rv.A.Kind == mir.OpConst) && dst != cur && dst != mir.ReturnLocal:
				constAlias[dst] = rv.A
				written[dst] = true
			case isInt && rv.Kind == mir.RvBinary && rv.Type == retT:
				op := classifyAccOp(rv.Bin, noChecked)
				if op == accNone || (site.op != accNone && site.op != op) {
					return tailRecSite{}, false
				}
				aCur := wholeOf(rv.A, cur)
				bCur := wholeOf(rv.B, cur)
				if aCur == bCur {
					return tailRecSite{}, false
				}
				other := rv.B
				if bCur {
					other = rv.A
				}
				switch other.Kind {
				case mir.OpImm, mir.OpConst:
				case mir.OpCopy, mir.OpMove:
					if other.Place.Proj.Len != 0 {
						return tailRecSite{}, false
					}
					v := other.Place.Local
					if written[v] {
						alias, ok := constAlias[v]
						if !ok {
							return tailRecSite{}, false
						}
						other = alias
					} else {
						// Plain integer local computed before the call.
						other = mir.Copy(other.Place)
					}
				default:
					return tailRecSite{}, false
				}
				if op == accAddChecked {
					if !site.hasSpan {
						site.addSpan, site.hasSpan = st.Span, true
					} else if site.addSpan != st.Span {
						return tailRecSite{}, false
					}
				}
				site.op = op
				site.terms = append(site.terms, other)
				cur = dst
				written[dst] = true
				delete(constAlias, dst)
			default:
				return tailRecSite{}, false
			}
		}

		switch blk.Term.Kind {
		case mir.TermGoto:
			blkID = blk.Term.Target
		case mir.TermReturn:
			if blk.Term.Exit != mir.ExitNormal || blk.Term.NewError || cur != mir.ReturnLocal {
				return tailRecSite{}, false
			}
			return site, true
		default:
			return tailRecSite{}, false
		}
	}
}

// nnVal is the 3-point lattice for non-negativity of integer locals.
type nnVal uint8

const (
	nnBot nnVal = iota
	nnNonNeg
	nnTop
)

func nnJoin(a, b nnVal) nnVal {
	if a == nnBot {
		return b
	}
	if b == nnBot {
		return a
	}
	if a == nnNonNeg && b == nnNonNeg {
		return nnNonNeg
	}
	return nnTop
}

type nnCmp struct {
	local mir.LocalID
	op    mir.BinOp // normalized as `local op imm`
	imm   wide
}

// proveNonNeg runs an inductive forward dataflow analysis assuming every
// direct self-call returns >= 0. It returns true iff:
//  1. at every reachable TermReturn, _0 is proven >= 0, and
//  2. at every checked-add tail site, every accumulated term is proven >= 0.
//
// By strong induction on recursion depth, both conditions together imply that
// every terminating call returns >= 0 and every accumulated term is >= 0.
func proveNonNeg(cfg *Config, b *mir.Body, g *vcfg, sites []tailRecSite) bool {
	tab := cfg.Types
	retT := b.Locals[mir.ReturnLocal].Type
	nl := len(b.Locals)
	nb := len(b.Blocks)

	in := make([][]nnVal, nb)
	in[mir.EntryBlock] = make([]nnVal, nl)
	for i := range nl {
		info := intOf(tab, b.Locals[i].Type)
		if info.ok && b.Locals[i].Type != types.Bool && !info.signed {
			in[mir.EntryBlock][i] = nnNonNeg
		} else {
			in[mir.EntryBlock][i] = nnTop
		}
	}

	propagate := func(dst mir.BlockID, st []nnVal) bool {
		if in[dst] == nil {
			in[dst] = slices.Clone(st)
			return true
		}
		changed := false
		for i := range nl {
			j := nnJoin(in[dst][i], st[i])
			if j != in[dst][i] {
				in[dst][i] = j
				changed = true
			}
		}
		return changed
	}

	evalOp := func(st []nnVal, o mir.Operand) nnVal {
		t := b.OperandType(o)
		info := intOf(tab, t)
		if !info.ok {
			return nnTop
		}
		if !info.signed {
			return nnNonNeg
		}
		switch o.Kind {
		case mir.OpImm:
			if !info.immValue(o.Bits).neg0() {
				return nnNonNeg
			}
			return nnTop
		case mir.OpConst:
			if cfg.Consts != nil {
				cv := cfg.Consts.Get(o.Const)
				if !info.immValue(types.LoadUint(cv.Bytes, len(cv.Bytes))).neg0() {
					return nnNonNeg
				}
			}
			return nnTop
		case mir.OpCopy, mir.OpMove:
			if o.Place.Proj.Len == 0 {
				return st[o.Place.Local]
			}
			return nnTop
		}
		return nnTop
	}

	evalRvalue := func(st []nnVal, rv *mir.Rvalue, dstT types.TypeID) nnVal {
		info := intOf(tab, dstT)
		if !info.ok {
			return nnTop
		}
		if !info.signed {
			return nnNonNeg
		}
		switch rv.Kind {
		case mir.RvUse:
			return evalOp(st, rv.A)
		case mir.RvLen, mir.RvSizeOf, mir.RvAlignOf, mir.RvFieldCount:
			return nnNonNeg
		case mir.RvCast:
			if rv.Cast == mir.CastLossless || rv.Cast == mir.CastChecked {
				srcT := b.OperandType(rv.A)
				if srcIn := intOf(tab, srcT); srcIn.ok && evalOp(st, rv.A) == nnNonNeg {
					return nnNonNeg
				}
			}
			return nnTop
		case mir.RvBinary:
			va := evalOp(st, rv.A)
			vb := evalOp(st, rv.B)
			switch rv.Bin {
			case mir.Add, mir.AddNoWrap, mir.Mul, mir.MulNoWrap, mir.Div, mir.Rem, mir.BitOr, mir.BitXor:
				if va == nnNonNeg && vb == nnNonNeg {
					return nnNonNeg
				}
			case mir.BitAnd:
				if va == nnNonNeg || vb == nnNonNeg {
					return nnNonNeg
				}
			case mir.Shr:
				if va == nnNonNeg {
					return nnNonNeg
				}
			}
			return nnTop
		}
		return nnTop
	}

	stepBlock := func(bi mir.BlockID, st []nnVal) map[mir.LocalID]nnCmp {
		blk := &b.Blocks[bi]
		var cmps map[mir.LocalID]nnCmp
		for si := range blk.Stmts {
			s := &blk.Stmts[si]
			if s.Kind != mir.StAssign || s.Place.Proj.Len != 0 {
				continue
			}
			v := s.Place.Local
			if cmps != nil {
				delete(cmps, v)
				for k, c := range cmps {
					if c.local == v {
						delete(cmps, k)
					}
				}
			}
			rv := &s.Rvalue
			if rv.Kind == mir.RvBinary && rv.Bin.IsCompare() {
				if cmp, ok := extractNNCompare(b, tab, rv); ok {
					if cmps == nil {
						cmps = make(map[mir.LocalID]nnCmp)
					}
					cmps[v] = cmp
				}
			}
			st[v] = evalRvalue(st, rv, b.Locals[v].Type)
		}
		return cmps
	}

	buf := make([]nnVal, nl)
	for changed := true; changed; {
		changed = false
		for _, bi := range g.rpo {
			if in[bi] == nil {
				continue
			}
			copy(buf, in[bi])
			cmps := stepBlock(bi, buf)
			t := &b.Blocks[bi].Term
			switch t.Kind {
			case mir.TermGoto:
				if propagate(t.Target, buf) {
					changed = true
				}
			case mir.TermCall:
				if t.Dest.Proj.Len == 0 {
					dl := t.Dest.Local
					dt := b.Locals[dl].Type
					dIn := intOf(tab, dt)
					switch {
					case isDirectSelfCall(b, t) && dt == retT:
						buf[dl] = nnNonNeg // inductive hypothesis
					case dIn.ok && !dIn.signed:
						buf[dl] = nnNonNeg
					default:
						buf[dl] = nnTop
					}
				}
				if propagate(t.Target, buf) {
					changed = true
				}
			case mir.TermSwitch:
				var cmp nnCmp
				hasCmp := false
				if cmps != nil && (t.On.Kind == mir.OpCopy || t.On.Kind == mir.OpMove) && t.On.Place.Proj.Len == 0 && b.OperandType(t.On) == types.Bool {
					cmp, hasCmp = cmps[t.On.Place.Local]
				}
				cases := b.Cases[t.Cases.Start:t.Cases.End()]
				var ref []nnVal
				propEdge := func(dst mir.BlockID, truth int) {
					st := buf
					if hasCmp && truth != 0 && cmpProvesNonNeg(cmp, truth > 0) {
						if ref == nil {
							ref = make([]nnVal, nl)
						}
						copy(ref, buf)
						ref[cmp.local] = nnNonNeg
						st = ref
					}
					if propagate(dst, st) {
						changed = true
					}
				}
				for _, c := range cases {
					truth := 0
					if hasCmp {
						if c.Value&1 == 1 {
							truth = 1
						} else {
							truth = -1
						}
					}
					propEdge(c.Target, truth)
				}
				if t.Default != mir.NoBlock {
					truth := 0
					if hasCmp && len(cases) == 1 {
						if cases[0].Value&1 == 0 {
							truth = 1
						} else {
							truth = -1
						}
					}
					propEdge(t.Default, truth)
				}
			}
		}
	}

	// Verify condition 1: every reachable TermReturn has _0 >= 0.
	for _, bi := range g.rpo {
		if in[bi] == nil || b.Blocks[bi].Term.Kind != mir.TermReturn {
			continue
		}
		copy(buf, in[bi])
		stepBlock(bi, buf)
		if buf[mir.ReturnLocal] != nnNonNeg {
			return false
		}
	}

	// Verify condition 2: every accumulated term at every checked-add tail
	// site is proven >= 0 right before the self-call.
	for _, s := range sites {
		if s.op != accAddChecked {
			continue
		}
		if in[s.callBlk] == nil {
			continue
		}
		copy(buf, in[s.callBlk])
		stepBlock(s.callBlk, buf)
		for _, term := range s.terms {
			if evalOp(buf, term) != nnNonNeg {
				return false
			}
		}
	}
	return true
}

func extractNNCompare(b *mir.Body, tab *types.Table, rv *mir.Rvalue) (nnCmp, bool) {
	aLocal := (rv.A.Kind == mir.OpCopy || rv.A.Kind == mir.OpMove) && rv.A.Place.Proj.Len == 0
	bLocal := (rv.B.Kind == mir.OpCopy || rv.B.Kind == mir.OpMove) && rv.B.Place.Proj.Len == 0
	switch {
	case aLocal && rv.B.Kind == mir.OpImm:
		t := b.Locals[rv.A.Place.Local].Type
		in := intOf(tab, t)
		if !in.ok || !in.signed {
			return nnCmp{}, false
		}
		return nnCmp{local: rv.A.Place.Local, op: rv.Bin, imm: in.immValue(rv.B.Bits)}, true
	case bLocal && rv.A.Kind == mir.OpImm:
		t := b.Locals[rv.B.Place.Local].Type
		in := intOf(tab, t)
		if !in.ok || !in.signed {
			return nnCmp{}, false
		}
		return nnCmp{local: rv.B.Place.Local, op: flipCompare(rv.Bin), imm: in.immValue(rv.A.Bits)}, true
	default:
		return nnCmp{}, false
	}
}

func flipCompare(op mir.BinOp) mir.BinOp {
	switch op {
	case mir.Lt:
		return mir.Gt
	case mir.Le:
		return mir.Ge
	case mir.Gt:
		return mir.Lt
	case mir.Ge:
		return mir.Le
	default:
		return op
	}
}

func invertCompare(op mir.BinOp) mir.BinOp {
	switch op {
	case mir.Eq:
		return mir.Ne
	case mir.Ne:
		return mir.Eq
	case mir.Lt:
		return mir.Ge
	case mir.Le:
		return mir.Gt
	case mir.Gt:
		return mir.Le
	case mir.Ge:
		return mir.Lt
	default:
		return op
	}
}

func cmpProvesNonNeg(c nnCmp, isTrue bool) bool {
	op := c.op
	if !isTrue {
		op = invertCompare(op)
	}
	switch op {
	case mir.Ge, mir.Eq:
		return !c.imm.neg0() // x >= k or x == k with k >= 0
	case mir.Gt:
		return c.imm.cmp(wInt(-1)) >= 0 // x > k with k >= -1 => x >= 0
	default:
		return false
	}
}

// rewriteAccTailRec clones b and transforms the matched self-tail-call sites
// into a loop rooted at a new loop header block.
func rewriteAccTailRec(b *mir.Body, sites []tailRecSite, chosen accOp, retT types.TypeID, retIn intInfo, ovfSpan mir.SpanID) *mir.Body {
	out := cloneForVersion(b)
	out.Loops = slices.Clone(b.Loops)

	// Move original EntryBlock to loopHdr so EntryBlock becomes the prologue.
	loopHdr := mir.BlockID(len(out.Blocks))
	origEntry := out.Blocks[mir.EntryBlock]
	origEntry.Stmts = slices.Clone(origEntry.Stmts)
	out.Blocks = append(out.Blocks, origEntry)

	// Redirect any existing edge or loop metadata targeting EntryBlock to loopHdr.
	for bi := 1; bi < len(out.Blocks); bi++ {
		t := &out.Blocks[bi].Term
		switch t.Kind {
		case mir.TermGoto:
			if t.Target == mir.EntryBlock {
				t.Target = loopHdr
			}
		case mir.TermCall:
			if t.Target == mir.EntryBlock {
				t.Target = loopHdr
			}
		case mir.TermSwitch:
			for ci := t.Cases.Start; ci < t.Cases.End(); ci++ {
				if out.Cases[ci].Target == mir.EntryBlock {
					out.Cases[ci].Target = loopHdr
				}
			}
			if t.Default == mir.EntryBlock {
				t.Default = loopHdr
			}
		}
	}
	for i := range out.Loops {
		if out.Loops[i].Header == mir.EntryBlock {
			out.Loops[i].Header = loopHdr
		}
		if out.Loops[i].Exit == mir.EntryBlock {
			out.Loops[i].Exit = loopHdr
		}
	}

	entrySpan := out.Blocks[loopHdr].Term.Span
	newTemp := func(t types.TypeID, sp mir.SpanID) mir.LocalID {
		return out.AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop, Span: sp})
	}

	// Build prologue in EntryBlock.
	var acc, mask, ovf mir.LocalID
	var proStmts []mir.Statement
	if chosen != accNone {
		acc = newTemp(retT, entrySpan)
		proStmts = append(proStmts, mir.Statement{
			Kind:   mir.StAssign,
			Place:  mir.LocalPlace(acc),
			Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(retT, accIdentity(chosen, retIn)), Type: retT},
			Span:   entrySpan,
		})
		if chosen == accAddChecked {
			if retIn.signed {
				mask = newTemp(retT, entrySpan)
				proStmts = append(proStmts, mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(mask),
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(retT, 0), Type: retT},
					Span:   entrySpan,
				})
			} else {
				ovf = newTemp(types.Bool, entrySpan)
				proStmts = append(proStmts, mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(ovf),
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Bool, 0), Type: types.Bool},
					Span:   entrySpan,
				})
			}
		}
	}
	out.Blocks[mir.EntryBlock] = mir.Block{
		Origin: mir.OriginBody,
		Stmts:  proStmts,
		Term:   mir.Terminator{Kind: mir.TermGoto, Target: loopHdr, Span: entrySpan},
	}

	// Rewrite each tail-call site to update the accumulator and parameters,
	// then jump back to loopHdr.
	binOp := accBinOp(chosen)
	for _, s := range sites {
		bi := s.callBlk
		if bi == mir.EntryBlock {
			bi = loopHdr
		}
		blk := &out.Blocks[bi]
		blk.Stmts = slices.Clone(blk.Stmts)
		callSpan := blk.Term.Span
		args := out.Args[blk.Term.Args.Start:blk.Term.Args.End()]

		// Step A: accumulate terms before overwriting parameters.
		for _, term := range s.terms {
			switch {
			case chosen == accAddChecked && retIn.signed:
				blk.Stmts = append(blk.Stmts,
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(acc),
						Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(mir.LocalPlace(acc)), B: term, Type: retT},
						Span:   s.addSpan,
					},
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(mask),
						Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitOr, A: mir.Copy(mir.LocalPlace(mask)), B: mir.Copy(mir.LocalPlace(acc)), Type: retT},
						Span:   s.addSpan,
					},
				)
			case chosen == accAddChecked && !retIn.signed:
				next := newTemp(retT, s.addSpan)
				carry := newTemp(types.Bool, s.addSpan)
				blk.Stmts = append(blk.Stmts,
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(next),
						Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(mir.LocalPlace(acc)), B: term, Type: retT},
						Span:   s.addSpan,
					},
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(carry),
						Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(mir.LocalPlace(next)), B: mir.Copy(mir.LocalPlace(acc)), Type: types.Bool},
						Span:   s.addSpan,
					},
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(ovf),
						Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitOr, A: mir.Copy(mir.LocalPlace(ovf)), B: mir.Copy(mir.LocalPlace(carry)), Type: types.Bool},
						Span:   s.addSpan,
					},
					mir.Statement{
						Kind:   mir.StAssign,
						Place:  mir.LocalPlace(acc),
						Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Copy(mir.LocalPlace(next)), Type: retT},
						Span:   s.addSpan,
					},
				)
			default:
				blk.Stmts = append(blk.Stmts, mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(acc),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: binOp, A: mir.Copy(mir.LocalPlace(acc)), B: term, Type: retT},
					Span:   callSpan,
				})
			}
		}

		// Step B & C: parallel update of changed parameters.
		type paramUpd struct {
			param mir.LocalID
			arg   mir.Arg
			tmp   mir.LocalID
		}
		var upds []paramUpd
		for i := range int(out.ParamCount) {
			p := mir.LocalID(i + 1)
			a := args[i]
			if argUnchanged(a, p) {
				continue
			}
			upds = append(upds, paramUpd{param: p, arg: a})
		}
		if len(upds) == 1 {
			u := upds[0]
			pt := out.Locals[u.param].Type
			blk.Stmts = append(blk.Stmts, mir.Statement{
				Kind:   mir.StAssign,
				Place:  mir.LocalPlace(u.param),
				Rvalue: mir.Rvalue{Kind: mir.RvUse, A: u.arg.Operand, Type: pt},
				Span:   callSpan,
			})
		} else if len(upds) > 1 {
			for i := range upds {
				pt := out.Locals[upds[i].param].Type
				upds[i].tmp = newTemp(pt, callSpan)
				blk.Stmts = append(blk.Stmts, mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(upds[i].tmp),
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: upds[i].arg.Operand, Type: pt},
					Span:   callSpan,
				})
			}
			for _, u := range upds {
				pt := out.Locals[u.param].Type
				op := mir.Copy(mir.LocalPlace(u.tmp))
				if u.arg.Mode == mir.ArgMove {
					op = mir.Move(mir.LocalPlace(u.tmp))
				}
				blk.Stmts = append(blk.Stmts, mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(u.param),
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: op, Type: pt},
					Span:   callSpan,
				})
			}
		}

		blk.Term = mir.Terminator{Kind: mir.TermGoto, Target: loopHdr, Span: callSpan}
	}

	// Patch all reachable base TermReturn blocks to combine with acc (and
	// check the sticky overflow flag for checked +).
	ng := newVcfg(out)
	exitBlk := mir.NoBlock
	var panicBlk mir.BlockID
	if chosen == accAddChecked {
		panicBlk = mir.BlockID(len(out.Blocks))
		out.Blocks = append(out.Blocks, mir.Block{
			Origin: mir.OriginBody,
			Term:   mir.Terminator{Kind: mir.TermPanic, Panic: types.PanicOverflow, Span: ovfSpan},
		})
	}
	for _, bi := range ng.rpo {
		if out.Blocks[bi].Term.Kind != mir.TermReturn {
			continue
		}
		if exitBlk == mir.NoBlock {
			exitBlk = bi
		}
		if chosen == accNone {
			continue
		}
		blk := &out.Blocks[bi]
		blk.Stmts = slices.Clone(blk.Stmts)
		retSpan := blk.Term.Span
		switch {
		case chosen == accAddChecked && retIn.signed:
			bad := newTemp(types.Bool, ovfSpan)
			blk.Stmts = append(blk.Stmts,
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(mir.ReturnLocal),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(mir.LocalPlace(acc)), B: mir.Copy(mir.LocalPlace(mir.ReturnLocal)), Type: retT},
					Span:   ovfSpan,
				},
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(mask),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitOr, A: mir.Copy(mir.LocalPlace(mask)), B: mir.Copy(mir.LocalPlace(mir.ReturnLocal)), Type: retT},
					Span:   ovfSpan,
				},
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(bad),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(mir.LocalPlace(mask)), B: mir.Imm(retT, 0), Type: types.Bool},
					Span:   ovfSpan,
				},
			)
			okBlk := mir.BlockID(len(out.Blocks))
			out.Blocks = append(out.Blocks, mir.Block{
				Origin: mir.OriginBody,
				Term:   mir.Terminator{Kind: mir.TermReturn, Exit: mir.ExitNormal, Span: retSpan},
			})
			cs := mir.Range{Start: uint32(len(out.Cases)), Len: 1}
			out.Cases = append(out.Cases, mir.SwitchCase{Value: 0, Target: okBlk})
			out.Blocks[bi].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(bad)), Cases: cs, Default: panicBlk, Span: ovfSpan}
		case chosen == accAddChecked && !retIn.signed:
			sum := newTemp(retT, ovfSpan)
			carry := newTemp(types.Bool, ovfSpan)
			blk.Stmts = append(blk.Stmts,
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(sum),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.AddWrap, A: mir.Copy(mir.LocalPlace(acc)), B: mir.Copy(mir.LocalPlace(mir.ReturnLocal)), Type: retT},
					Span:   ovfSpan,
				},
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(carry),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(mir.LocalPlace(sum)), B: mir.Copy(mir.LocalPlace(acc)), Type: types.Bool},
					Span:   ovfSpan,
				},
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(ovf),
					Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: mir.BitOr, A: mir.Copy(mir.LocalPlace(ovf)), B: mir.Copy(mir.LocalPlace(carry)), Type: types.Bool},
					Span:   ovfSpan,
				},
				mir.Statement{
					Kind:   mir.StAssign,
					Place:  mir.LocalPlace(mir.ReturnLocal),
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Copy(mir.LocalPlace(sum)), Type: retT},
					Span:   ovfSpan,
				},
			)
			okBlk := mir.BlockID(len(out.Blocks))
			out.Blocks = append(out.Blocks, mir.Block{
				Origin: mir.OriginBody,
				Term:   mir.Terminator{Kind: mir.TermReturn, Exit: mir.ExitNormal, Span: retSpan},
			})
			cs := mir.Range{Start: uint32(len(out.Cases)), Len: 1}
			out.Cases = append(out.Cases, mir.SwitchCase{Value: 0, Target: okBlk})
			out.Blocks[bi].Term = mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(mir.LocalPlace(ovf)), Cases: cs, Default: panicBlk, Span: ovfSpan}
		default:
			blk.Stmts = append(blk.Stmts, mir.Statement{
				Kind:   mir.StAssign,
				Place:  mir.LocalPlace(mir.ReturnLocal),
				Rvalue: mir.Rvalue{Kind: mir.RvBinary, Bin: binOp, A: mir.Copy(mir.LocalPlace(acc)), B: mir.Copy(mir.LocalPlace(mir.ReturnLocal)), Type: retT},
				Span:   retSpan,
			})
		}
	}

	if exitBlk == mir.NoBlock {
		exitBlk = mir.EntryBlock
	}
	out.Loops = append(out.Loops, mir.LoopInfo{
		Header:   loopHdr,
		Exit:     exitBlk,
		Holder:   mir.NoLocal,
		FieldsOf: types.NoType,
		Span:     entrySpan,
	})
	return out
}
