// Why: an instance body still holds tests and calls whose answer is fixed
// once the instance's arguments and the build profile are known; codegen
// should see only the branch that runs.
//
// What: the per-instance resolution of `if T is X` (TermTypeTest, RvTypeIs,
// CastNarrow), of onerr guards from borrow's facts, of the build-constant
// folds (UsesTasks, LibcBuild, HasFree), the requires clauses true for the
// instance (reqfold.go), the drop of contract checks and trace hooks in
// release, and prune.
//
// How: each answer becomes a constant branch; prune then drops the blocks
// unreachable from the entry and renumbers the rest densely in their
// original order.

package mono

import (
	"mk/internal/mir"
	"mk/internal/types"
)

// Keys of the trace hooks release drops.
var traceHooks = map[string]bool{"std.rt.TraceFail": true, "std.rt.TraceTry": true}

// needsRewrite reports whether a non-generic body still holds a construct
// mono resolves per instance: an onerr guard, or in release a contract check
// or trace hook call.
func (bl *builder) needsRewrite() bool {
	for i := range bl.gb.Blocks {
		t := &bl.gb.Blocks[i].Term
		switch t.Kind {
		case mir.TermOnerrGuard, mir.TermTypeTest:
			return true
		case mir.TermCall, mir.TermTailCall:
			if _, ok := bl.constFoldOf(t); ok {
				return true
			}
			if t.Callee.Kind == mir.CallDirect && bl.freeCallOf(t.Callee.Fn.Item) != freeNone {
				return true // HasFree (release.go)
			}
			if bl.opt.Profile == types.Release && t.Callee.Kind == mir.CallDirect && bl.droppedInRelease(t.Callee.Fn.Item) != dropNone {
				return true
			}
		}
	}
	return false
}

type dropKind uint8

const (
	dropNone dropKind = iota
	// dropContract: a requires, ensures or invariant predicate; its
	// destination becomes true.
	dropContract
	// dropTrace: TraceFail or TraceTry; nothing is assigned.
	dropTrace
)

// droppedInRelease classifies a callee: a contract predicate (an item named
// by its owner's Signature.Contracts or StructDecl.Invariants), a trace
// hook, or neither.
func (bl *builder) droppedInRelease(item types.ItemID) dropKind {
	it := bl.env.Items().Get(item)
	if traceHooks[it.Key.String()] {
		return dropTrace
	}
	if it.Owner == types.NoItem {
		return dropNone
	}
	switch d := bl.env.Items().Get(it.Owner).Decl.(type) {
	case *types.FuncDecl:
		for _, c := range d.Sig.Contracts {
			if c.Body == item {
				return dropContract
			}
		}
	case *types.StructDecl:
		for _, inv := range d.Invariants {
			if inv == item {
				return dropContract
			}
		}
	}
	return dropNone
}

// resolve rewrites, in place and with the generic body's block numbers, the
// per-instance constructs of steps 3, 5 and 6. It reports whether a branch
// was folded, so the reachability pass must run.
func (bl *builder) resolve() bool {
	b := bl.b
	folded := false
	for bi := range b.Blocks {
		blk := &b.Blocks[bi]
		for si := range blk.Stmts {
			rv := &blk.Stmts[si].Rvalue
			switch {
			case rv.Kind == mir.RvTypeIs:
				// Generic Param is Type, now both concrete.
				var bit uint64
				if bl.paramIs(rv.Param, rv.Type) {
					bit = 1
				}
				*rv = mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Bool, bit)}
			case rv.Kind == mir.RvCast && rv.Cast == mir.CastNarrow:
				*rv = mir.Rvalue{Kind: mir.RvUse, A: rv.A}
			}
		}
		t := &blk.Term
		switch t.Kind {
		case mir.TermTypeTest:
			to := t.No
			if bl.paramIs(t.Param, t.Type) {
				to = t.Yes
			}
			*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: to}
			folded = true
		case mir.TermOnerrGuard:
			// Borrow's one static answer per guard, by the generic
			// body's block number (this one: no renumbering has run yet).
			to := t.No
			if bl.env.OnerrRuns(bl.item, mir.BlockID(bi)) {
				to = t.Yes
			}
			*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: to}
			folded = true
		case mir.TermTailCall:
			// A tail call's value is the body's result, never a fold or a
			// dropped check.
			if _, ok := bl.constFoldOf(t); ok {
				noTail(t, "a folded fact")
			}
			if t.Callee.Kind == mir.CallDirect && bl.freeCallOf(t.Callee.Fn.Item) != freeNone {
				noTail(t, "a std.raw Free intrinsic")
			}
			if bl.opt.Profile == types.Release && t.Callee.Kind == mir.CallDirect && bl.droppedInRelease(t.Callee.Fn.Item) != dropNone {
				noTail(t, "a contract predicate or trace hook")
			}
		case mir.TermCall:
			if f, ok := bl.constFoldOf(t); ok {
				// UsesTasks (tasks.go) and LibcBuild (libcfold.go): the fact is a
				// constant of the build.
				blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StAssign, Place: t.Dest,
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Bool, b2u(f.value))}, Span: t.Span})
				*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: t.Target}
				if foldSwitchesOn(b, blk.Stmts[len(blk.Stmts)-1].Place, b2u(f.value)) {
					folded = true
					bl.tasksFolded = true
				}
				continue
			}
			if t.Callee.Kind == mir.CallDirect {
				switch bl.freeCallOf(t.Callee.Fn.Item) {
				case freeHasFree:
					// T's impl of std.core.Free is a fact of the instance.
					if bl.foldHasFree(blk) {
						folded = true
						bl.freeFolded = true
					}
					continue
				case freeRelease:
					bl.releases = true // resolved after the prune (release.go)
					continue
				}
				if bl.isRequires(t.Callee.Fn.Item) && bl.requiresHolds(t) {
					// A requires clause true for every run of the instance
					// (reqfold.go): no entry check, in every profile.
					blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StAssign, Place: t.Dest,
						Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Bool, 1)}, Span: t.Span})
					*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: t.Target}
					if foldSwitchesOn(b, blk.Stmts[len(blk.Stmts)-1].Place, 1) {
						folded = true
					}
					continue
				}
			}
			if bl.opt.Profile != types.Release || t.Callee.Kind != mir.CallDirect {
				continue
			}
			switch bl.droppedInRelease(t.Callee.Fn.Item) {
			case dropContract:
				// The check's result is true; LLVM folds the dead panic.
				blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StAssign, Place: t.Dest,
					Rvalue: mir.Rvalue{Kind: mir.RvUse, A: mir.Imm(types.Bool, 1)}, Span: t.Span})
				*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: t.Target}
			case dropTrace:
				*t = mir.Terminator{Kind: mir.TermGoto, Span: t.Span, Target: t.Target}
			}
		}
	}
	return folded
}

// paramIs reports whether generic p of the instance is the (substituted)
// type x.
func (bl *builder) paramIs(p uint32, x types.TypeID) bool {
	if int(p) >= len(bl.s.args) || bl.s.args[p].IsConst {
		ice("if T is X on generic %d of %d", p, len(bl.s.args))
	}
	return bl.s.args[p].Type == x
}

// prune drops the blocks unreachable from the entry and renumbers the rest
// densely in their original order. A loop whose header or exit
// is unreachable keeps that block as an empty, unreachable anchor, because
// LoopInfo, locals and loans name loops by ID.
func prune(b *mir.Body) {
	n := len(b.Blocks)
	live := make([]bool, n)
	var stack, buf []mir.BlockID
	mark := func(id mir.BlockID) {
		if !live[id] {
			live[id] = true
			stack = append(stack, id)
		}
	}
	mark(mir.EntryBlock)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		buf = b.Successors(id, buf[:0])
		for _, s := range buf {
			mark(s)
		}
	}
	anchor := make([]bool, n)
	for _, lp := range b.Loops {
		for _, id := range []mir.BlockID{lp.Header, lp.Exit} {
			if !live[id] {
				anchor[id] = true
			}
		}
	}
	remap := make([]mir.BlockID, n)
	var out []mir.Block
	for i := range b.Blocks {
		if !live[i] && !anchor[i] {
			remap[i] = mir.NoBlock
			continue
		}
		remap[i] = mir.BlockID(len(out))
		blk := b.Blocks[i]
		if anchor[i] {
			blk.Stmts = nil
			blk.Term = mir.Terminator{Kind: mir.TermUnreachable, Span: blk.Term.Span}
		}
		out = append(out, blk)
	}
	if len(out) == n {
		return
	}
	to := func(id mir.BlockID) mir.BlockID {
		if id == mir.NoBlock {
			return id
		}
		if remap[id] == mir.NoBlock {
			ice("live edge to dead block bb%d", id)
		}
		return remap[id]
	}
	for i := range out {
		t := &out[i].Term
		switch t.Kind {
		case mir.TermGoto:
			t.Target = to(t.Target)
		case mir.TermSwitch:
			for ci := t.Cases.Start; ci < t.Cases.End(); ci++ {
				b.Cases[ci].Target = to(b.Cases[ci].Target)
			}
			t.Default = to(t.Default)
		case mir.TermCall:
			t.Target = to(t.Target)
			if t.HasErr {
				t.Err.Block = to(t.Err.Block)
			}
		case mir.TermTypeTest, mir.TermOnerrGuard:
			t.Yes, t.No = to(t.Yes), to(t.No)
		case mir.TermTailCall, mir.TermReturn, mir.TermPanic, mir.TermUnreachable:
			// No successor to renumber.
		}
	}
	for i := range b.Loops {
		b.Loops[i].Header = to(b.Loops[i].Header)
		b.Loops[i].Exit = to(b.Loops[i].Exit)
	}
	b.Blocks = out
}
