// Why: the tagged mode checks at compile time that a body obeys the memory
// model's borrow rules, and each rule needs a case that breaks it and a case
// that keeps it.
// What: TestSB_ tests: each body that must violate beside its fixed twin that
// must run clean, plus equal steps and unchanged plain mode.
// How: MIR built by hand in eval's test world, run with tcall in the tagged
// mode at StageBodies; the tests read the violation kind and its spans.

package eval

import (
	"testing"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// tcall runs fn in the tagged mode at StageBodies.
func (w *world) tcall(e *Evaluator, fn types.ItemID, opt *TagOptions, args ...types.ConstID) TaggedResult {
	return e.RunTagged(&types.EvalRequest{What: types.EvalCall, Item: fn, Args: args, Stage: types.StageBodies,
		Module: w.module("main"), Site: source.Span{File: 1, Start: 1, End: 2}, Target: types.X86_64Windows}, opt)
}

func refOf(p mir.Place) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: p}
}

func mutOf(p mir.Place) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: p}
}

// deref is *l for a reference local l to a value of type t.
func (x *bb) deref(l mir.LocalID, t types.TypeID) mir.Place {
	return x.b.Place(l, mir.Projection{Kind: mir.ProjDeref, Type: t})
}

// scopeEnd appends StScopeEnd l to blk.
func (x *bb) scopeEnd(blk mir.BlockID, l mir.LocalID) mir.SpanID {
	s := x.sp()
	x.b.Blocks[blk].Stmts = append(x.b.Blocks[blk].Stmts, mir.Statement{Kind: mir.StScopeEnd, Local: l, Span: s})
	return s
}

// wantViolation requires r to have stopped at a violation of kind k.
func wantViolation(t *testing.T, r TaggedResult, k ViolationKind) *Violation {
	t.Helper()
	if len(r.Violations) == 0 {
		t.Fatalf("no violation (want %s): err %+v skipped %q", k, r.Err, r.Skipped)
	}
	if v := &r.Violations[0]; v.Kind != k {
		t.Fatalf("violation %s (%s), want %s", v.Kind, v.Where, k)
	}
	return &r.Violations[0]
}

// wantClean requires a completed run with no violation.
func wantClean(t *testing.T, r TaggedResult) {
	t.Helper()
	if len(r.Violations) > 0 {
		v := r.Violations[0]
		t.Fatalf("violation %s (%s), want a clean run", v.Kind, v.Where)
	}
	if r.Err != nil || r.Skipped != "" || r.Value == types.NoConst {
		t.Fatalf("run did not complete: err %+v skipped %q", r.Err, r.Skipped)
	}
}

// spanOf is the source span of MIR span id s of body b.
func spanOf(x *bb, s mir.SpanID) source.Span { return x.b.Span(s) }

// refRead builds F(): i64 { x := 1; r := &x; [x = 2 when popFirst]; return *r [; x = 2] }.
func refRead(w *world, name string, popFirst bool) (types.ItemID, *bb, mir.SpanID, mir.SpanID) {
	f := w.fn("lib", name, types.I64)
	x := w.body(f, types.I64)
	v, r := x.local(types.I64), x.local(w.tab.Ref(types.I64))
	x.assign(0, lp(v), use(imm(types.I64, 1)))
	made := x.assign(0, lp(r), refOf(lp(v)))
	var removed mir.SpanID
	if popFirst {
		removed = x.assign(0, lp(v), use(imm(types.I64, 2)))
	}
	x.assign(0, lp(0), use(mir.Copy(x.deref(r, types.I64))))
	if !popFirst {
		x.assign(0, lp(v), use(imm(types.I64, 2)))
	}
	x.ret(0)
	x.done()
	return f, x, made, removed
}

func TestSB_RemovedTag(t *testing.T) {
	w := newWorld(t)
	f, x, made, removed := refRead(w, "Bad", true)
	v := wantViolation(t, w.tcall(w.evaluator(0), f, nil), VRemovedTag)
	if v.Made != spanOf(x, made) || v.Removed != spanOf(x, removed) {
		t.Errorf("made %v removed %v, want %v and %v", v.Made, v.Removed, spanOf(x, made), spanOf(x, removed))
	}
	if v.Tag == 0 || v.In != f || v.Std {
		t.Errorf("violation %+v", v)
	}
	g, _, _, _ := refRead(w, "Good", false)
	wantClean(t, w.tcall(w.evaluator(0), g, nil))
}

// TestSB_OwnerWritePopsShared: an owner write pops the shared item above
// it; the fixed twin reads through the reference first.
func TestSB_OwnerWritePopsShared(t *testing.T) {
	w := newWorld(t)
	f, _, _, _ := refRead(w, "Bad", true)
	wantViolation(t, w.tcall(w.evaluator(0), f, nil), VRemovedTag)
	g, _, _, _ := refRead(w, "Good", false)
	r := w.tcall(w.evaluator(0), g, nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 1 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
}

// TestSB_OwnerReadKeepsShared: an owner read keeps a shared item and
// removes a Unique one.
func TestSB_OwnerReadKeepsShared(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, unique bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		v, h := x.local(types.I64), x.local(types.I64)
		rt := w.tab.Ref(types.I64)
		if unique {
			rt = w.tab.Mut(types.I64)
		}
		r := x.local(rt)
		x.assign(0, lp(v), use(imm(types.I64, 1)))
		if unique {
			x.assign(0, lp(r), mutOf(lp(v)))
		} else {
			x.assign(0, lp(r), refOf(lp(v)))
		}
		x.assign(0, lp(h), use(cp(v))) // the owner reads
		if unique {
			x.assign(0, x.deref(r, types.I64), use(imm(types.I64, 3)))
		}
		x.assign(0, lp(0), use(mir.Copy(x.deref(r, types.I64))))
		x.ret(0)
		x.done()
		return f
	}
	wantClean(t, w.tcall(w.evaluator(0), mk("Shared", false), nil))
	wantViolation(t, w.tcall(w.evaluator(0), mk("Unique", true), nil), VRemovedTag)
}

// rawOf declares std.raw.Of[T](r: &T): *T and OfMut[T](r: mut T): *T and
// Write[T](p: *T, v: T), instantiated at T by callG's type arguments.
func (w *world) rawFns() (of, ofMut, write, read types.ItemID) {
	tp := w.tab.Param(0)
	of = w.raw("Of", w.tab.RawPtr(tp), w.tab.Ref(tp))
	ofMut = w.raw("OfMut", w.tab.RawPtr(tp), w.tab.Mut(tp))
	write = w.raw("Write", types.Void, w.tab.RawPtr(tp), tp)
	read = w.raw("Read", tp, w.tab.RawPtr(tp))
	return
}

// TestSB_SharedWrite and TestSB_RawInheritsTag: a raw pointer from a shared
// reference keeps its tag; writing through it on frozen bytes is
// VSharedWrite with Std false in a test body, and on interior bytes clean.
func TestSB_SharedWrite(t *testing.T) {
	w := newWorld(t)
	of, _, write, _ := w.rawFns()
	f := w.fn("lib", "Bad", types.I64)
	x := &seq{bb: w.body(f, types.I64)}
	v, r, p, u := x.local(types.I64), x.local(w.tab.Ref(types.I64)), x.local(w.tab.RawPtr(types.I64)), x.local(types.Void)
	x.assign(0, lp(v), use(imm(types.I64, 1)))
	x.assign(0, lp(r), refOf(lp(v)))
	x.call(of, []types.TypeID{types.I64}, lp(p), cp(r))
	x.call(write, []types.TypeID{types.I64}, lp(u), cp(p), imm(types.I64, 5))
	x.assign(x.cur, lp(0), use(cp(v)))
	x.ret(x.cur)
	x.done()
	vv := wantViolation(t, w.tcall(w.evaluator(0), f, nil), VSharedWrite)
	if vv.Std {
		t.Errorf("Std set for a test body")
	}
}

func TestSB_RawInheritsTag(t *testing.T) {
	w := newWorld(t)
	of, _, write, _ := w.rawFns()
	cell, cellT := w.structType("std.core", "Cell", types.I64)
	w.known.Cell = cell
	f := w.fn("lib", "Good", types.I64)
	x := &seq{bb: w.body(f, types.I64)}
	c, r, p, u := x.local(cellT), x.local(w.tab.Ref(cellT)), x.local(w.tab.RawPtr(cellT)), x.local(types.Void)
	x.assign(0, lp(c), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: cell, Ops: x.b.AddOperands(imm(types.I64, 1))})
	x.assign(0, lp(r), refOf(lp(c)))
	x.call(of, []types.TypeID{cellT}, lp(p), cp(r))
	cv := x.local(cellT)
	x.assign(x.cur, lp(cv), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: cell, Ops: x.b.AddOperands(imm(types.I64, 7))})
	x.call(write, []types.TypeID{cellT}, lp(u), cp(p), cp(cv))
	x.assign(x.cur, lp(0), use(mir.Copy(x.b.Place(r, mir.Projection{Kind: mir.ProjDeref, Type: cellT}, mir.Projection{Kind: mir.ProjField, A: 0, Type: types.I64}))))
	x.ret(x.cur)
	x.done()
	res := w.tcall(w.evaluator(0), f, nil)
	wantClean(t, res)
	if w.intOf(res.Value) != 7 {
		t.Errorf("value %d", w.intOf(res.Value))
	}
}

// TestSB_UniqueReborrowChain: mut **p under mut *p; the inner write keeps the
// outer tag, and a write through the outer one then a use of the inner one is
// VRemovedTag.
func TestSB_UniqueReborrowChain(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, useInner bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		mi := w.tab.Mut(types.I64)
		v, m1, p, y := x.local(types.I64), x.local(mi), x.local(w.tab.Mut(mi)), x.local(mi)
		x.assign(0, lp(v), use(imm(types.I64, 1)))
		x.assign(0, lp(m1), mutOf(lp(v)))
		x.assign(0, lp(p), mutOf(lp(m1)))
		pp := x.b.Place(p, mir.Projection{Kind: mir.ProjDeref, Type: mi}, mir.Projection{Kind: mir.ProjDeref, Type: types.I64})
		x.assign(0, lp(y), mutOf(pp))
		x.assign(0, x.deref(y, types.I64), use(imm(types.I64, 6)))
		x.assign(0, pp, use(imm(types.I64, 7)))
		if useInner {
			x.assign(0, x.deref(y, types.I64), use(imm(types.I64, 8)))
		}
		x.assign(0, lp(0), use(cp(v)))
		x.ret(0)
		x.done()
		return f
	}
	r := w.tcall(w.evaluator(0), mk("Good", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 7 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Bad", true), nil), VRemovedTag)
}

// TestSB_DeadFrame: a reference to a callee's local, returned and read, is
// VDead with the callee's return as Removed; a reference the callee was
// given is clean.
func TestSB_DeadFrame(t *testing.T) {
	w := newWorld(t)
	ri := w.tab.Ref(types.I64)
	g := w.fn("lib", "Escape", ri)
	gx := w.body(g, ri)
	y := gx.local(types.I64)
	gx.assign(0, lp(y), use(imm(types.I64, 1)))
	gx.assign(0, lp(0), refOf(lp(y)))
	retSpan := gx.ret(0)
	gx.done()
	id := w.fn("lib", "Id", ri, ri)
	ix := w.body(id, ri, ri)
	ix.assign(0, lp(0), use(cp(1)))
	ix.ret(0)
	ix.done()
	mk := func(name string, escape bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		v, r := x.local(types.I64), x.local(ri)
		b1 := x.block()
		x.assign(0, lp(v), use(imm(types.I64, 4)))
		if escape {
			x.call(0, g, lp(r), b1)
		} else {
			a := x.local(ri)
			x.assign(0, lp(a), refOf(lp(v)))
			x.call(0, id, lp(r), b1, cp(a))
		}
		x.assign(b1, lp(0), use(mir.Copy(x.deref(r, types.I64))))
		x.ret(b1)
		x.done()
		return f
	}
	v := wantViolation(t, w.tcall(w.evaluator(0), mk("Bad", true), nil), VDead)
	if v.Removed != spanOf(gx, retSpan) {
		t.Errorf("removed %v, want the callee's return %v", v.Removed, spanOf(gx, retSpan))
	}
	r := w.tcall(w.evaluator(0), mk("Good", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 4 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
}

// TestSB_ScopeEnd: a use after StScopeEnd is VDeadScope.
func TestSB_ScopeEnd(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, end bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		v, r := x.local(types.I64), x.local(w.tab.Ref(types.I64))
		x.assign(0, lp(v), use(imm(types.I64, 1)))
		x.assign(0, lp(r), refOf(lp(v)))
		if end {
			x.scopeEnd(0, v)
		}
		x.assign(0, lp(0), use(mir.Copy(x.deref(r, types.I64))))
		x.ret(0)
		x.done()
		return f
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Bad", true), nil), VDeadScope)
	wantClean(t, w.tcall(w.evaluator(0), mk("Good", false), nil))
}

// TestSB_LoopActivation: a tag from activation 1 used in activation 2,
// after the local is written again, is VDeadScope; a tag of activation 2 is
// clean.
func TestSB_LoopActivation(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, stale bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		v, r := x.local(types.I64), x.local(w.tab.Ref(types.I64))
		x.assign(0, lp(v), use(imm(types.I64, 1)))
		x.assign(0, lp(r), refOf(lp(v)))
		x.scopeEnd(0, v)
		x.assign(0, lp(v), use(imm(types.I64, 2))) // the next iteration's x
		if !stale {
			x.assign(0, lp(r), refOf(lp(v)))
		}
		x.assign(0, lp(0), use(mir.Copy(x.deref(r, types.I64))))
		x.ret(0)
		x.done()
		return f
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Bad", true), nil), VDeadScope)
	r := w.tcall(w.evaluator(0), mk("Good", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 2 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
}

// TestSB_Uninit: a read of a never-written local is VUninit.
func TestSB_Uninit(t *testing.T) {
	w := newWorld(t)
	f := w.fn("lib", "Bad", types.I64)
	x := w.body(f, types.I64)
	v := x.local(types.I64)
	x.assign(0, lp(0), use(cp(v)))
	x.ret(0)
	x.done()
	wantViolation(t, w.tcall(w.evaluator(0), f, nil), VUninit)
}

// shaped declares a struct of one i64 field whose layout has kind k.
func (w *world) shaped(name string, k types.Kind) (types.ItemID, types.TypeID) {
	it, ty := w.structType("lib", name, types.I64)
	l := w.structOf([]types.TypeID{types.I64}, 0)
	l.Shape.Kind = k
	w.layouts[ty] = l
	return it, ty
}

// TestSB_MovedOut: a read of a moved-out non-plain local is VUninit; a
// plain one stays readable.
func TestSB_MovedOut(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, k types.Kind) types.ItemID {
		s, st := w.shaped("S"+name, k)
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		a, b, c := x.local(st), x.local(st), x.local(st)
		x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: s, Ops: x.b.AddOperands(imm(types.I64, 3))})
		x.assign(0, lp(b), use(mir.Move(lp(a))))
		x.assign(0, lp(c), use(cp(a)))
		x.assign(0, lp(0), use(mir.Copy(x.b.Place(c, mir.Projection{Kind: mir.ProjField, A: 0, Type: types.I64}))))
		x.ret(0)
		x.done()
		return f
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Unique", types.Unique), nil), VUninit)
	wantClean(t, w.tcall(w.evaluator(0), mk("Plain", types.Plain), nil))
}

// TestSB_SelfMove: `a = a` on a non-plain local moves the value into its
// own place, so a is still written afterwards and a read of it is clean.
func TestSB_SelfMove(t *testing.T) {
	w := newWorld(t)
	s, st := w.shaped("SSelf", types.Unique)
	f := w.fn("lib", "SelfMove", types.I64)
	x := w.body(f, types.I64)
	a := x.local(st)
	x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: s, Ops: x.b.AddOperands(imm(types.I64, 3))})
	x.assign(0, lp(a), use(mir.Move(lp(a))))
	x.assign(0, lp(0), use(mir.Copy(x.b.Place(a, mir.Projection{Kind: mir.ProjField, A: 0, Type: types.I64}))))
	x.ret(0)
	x.done()
	wantClean(t, w.tcall(w.evaluator(0), f, nil))
}

// TestSB_Linear: a linear local still owned at its scope end is VLinear;
// consumed (raw.Forget of a move) it is clean.
func TestSB_Linear(t *testing.T) {
	w := newWorld(t)
	tp := w.tab.Param(0)
	forget := w.raw("Forget", types.Void, tp)
	s, st := w.shaped("L", types.Linear)
	mk := func(name string, consume bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := &seq{bb: w.body(f, types.I64)}
		a, u := x.local(st), x.local(types.Void)
		x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: s, Ops: x.b.AddOperands(imm(types.I64, 3))})
		if consume {
			x.call(forget, []types.TypeID{st}, lp(u), mir.Move(lp(a)))
		}
		x.scopeEnd(x.cur, a)
		x.assign(x.cur, lp(0), use(imm(types.I64, 1)))
		x.ret(x.cur)
		x.done()
		return f
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Leak", false), nil), VLinear)
	wantClean(t, w.tcall(w.evaluator(0), mk("Consume", true), nil))
}

// TestSB_LinearEnumNarrow checks both sides of a discriminant switch: only
// the variant with a plain payload can leave the temporary unowned.
func TestSB_LinearEnumNarrow(t *testing.T) {
	w := newWorld(t)
	ticket, tt := w.shaped("Ticket", types.Linear)
	enum, et := w.enumType("lib", "Choice", []types.TypeID{tt}, []types.TypeID{types.I64})
	w.layoutLocked(et).Shape.Kind = types.Linear
	for _, tc := range []struct {
		name        string
		plain, test bool
	}{
		{"Plain", true, true},
		{"Untested", true, false},
		{"Linear", false, true},
	} {
		f := w.fn("lib", tc.name, types.Void)
		x := w.body(f, types.Void)
		a := x.local(et)
		var variant uint32
		op := imm(types.I64, 1)
		if tc.plain {
			variant = 1
		} else {
			payload := x.local(tt)
			x.assign(0, lp(payload), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: ticket, Ops: x.b.AddOperands(op)})
			op = mir.Move(lp(payload))
		}
		x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggEnum, Item: enum, Variant: variant, Ops: x.b.AddOperands(op)})
		var end mir.BlockID
		if tc.test {
			d := x.local(types.U64)
			end = x.block()
			x.assign(0, lp(d), mir.Rvalue{Kind: mir.RvDiscriminant, Place: lp(a), Type: types.U64})
			x.branch(0, cp(d), end, end)
		}
		x.ret(end)
		x.done()
		got := w.tcall(w.evaluator(0), f, nil)
		if tc.plain && tc.test {
			wantClean(t, got)
		} else {
			wantViolation(t, got, VLinear)
		}
	}
}

// TestSB_ProtectorMut and TestSB_ProtectorShared: during a call holding a
// reference parameter, an access by another path that pops the parameter's
// item is VProtected naming the call; the same access after the call
// returns is clean (lazy).
func TestSB_ProtectorMut(t *testing.T) {
	w := newWorld(t)
	_, ofMut, write, _ := w.rawFns()
	mi, pi := w.tab.Mut(types.I64), w.tab.RawPtr(types.I64)
	callee := w.fn("lib", "Clobber", types.Void, mi, pi)
	cx := &seq{bb: w.body(callee, types.Void, mi, pi)}
	cu := cx.local(types.Void)
	cx.call(write, []types.TypeID{types.I64}, lp(cu), cp(2), imm(types.I64, 2))
	cx.ret(cx.cur)
	cx.done()
	idle := w.fn("lib", "Idle", types.Void, mi, pi)
	ix := w.body(idle, types.Void, mi, pi)
	ix.ret(0)
	ix.done()
	mk := func(name string, during bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := &seq{bb: w.body(f, types.I64)}
		v, m1, y, u := x.local(types.I64), x.local(mi), x.local(pi), x.local(types.Void)
		x.assign(0, lp(v), use(imm(types.I64, 1)))
		x.assign(0, lp(m1), mutOf(lp(v)))
		x.call(ofMut, []types.TypeID{types.I64}, lp(y), cp(m1))
		if during {
			x.call(callee, nil, lp(u), cp(m1), cp(y))
		} else {
			x.call(idle, nil, lp(u), cp(m1), cp(y))
			x.call(write, []types.TypeID{types.I64}, lp(u), cp(y), imm(types.I64, 2))
		}
		x.assign(x.cur, lp(0), use(cp(v)))
		x.ret(x.cur)
		x.done()
		return f
	}
	v := wantViolation(t, w.tcall(w.evaluator(0), mk("During", true), nil), VProtected)
	if v.Holder != callee {
		t.Errorf("holder %v, want Clobber %v", v.Holder, callee)
	}
	r := w.tcall(w.evaluator(0), mk("After", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 2 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
}

func TestSB_ProtectorShared(t *testing.T) {
	w := newWorld(t)
	_, ofMut, write, _ := w.rawFns()
	ri, mi, pi := w.tab.Ref(types.I64), w.tab.Mut(types.I64), w.tab.RawPtr(types.I64)
	callee := w.fn("lib", "Clobber", types.Void, ri, pi)
	cx := &seq{bb: w.body(callee, types.Void, ri, pi)}
	cu := cx.local(types.Void)
	cx.call(write, []types.TypeID{types.I64}, lp(cu), cp(2), imm(types.I64, 2))
	cx.ret(cx.cur)
	cx.done()
	f := w.fn("lib", "During", types.I64)
	x := &seq{bb: w.body(f, types.I64)}
	v, m1, y, r, u := x.local(types.I64), x.local(mi), x.local(pi), x.local(ri), x.local(types.Void)
	x.assign(0, lp(v), use(imm(types.I64, 1)))
	x.assign(0, lp(m1), mutOf(lp(v)))
	x.call(ofMut, []types.TypeID{types.I64}, lp(y), cp(m1))
	x.assign(x.cur, lp(r), refOf(x.deref(m1, types.I64)))
	x.call(callee, nil, lp(u), cp(r), cp(y))
	x.assign(x.cur, lp(0), use(cp(v)))
	x.ret(x.cur)
	x.done()
	vv := wantViolation(t, w.tcall(w.evaluator(0), f, nil), VProtected)
	if vv.Holder != callee {
		t.Errorf("holder %v, want Clobber", vv.Holder)
	}
}

// arrayLocal assigns [1, 2, 3, 4] to a fresh [4]i64 local in block 0.
func arrayLocal(w *world, x *bb) (mir.LocalID, types.TypeID) {
	at := w.array(4, types.I64)
	xs := x.local(at)
	x.assign(0, lp(xs), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggArray, Type: at,
		Ops: x.b.AddOperands(imm(types.I64, 1), imm(types.I64, 2), imm(types.I64, 3), imm(types.I64, 4))})
	return xs, at
}

// TestSB_ForMutDisjoint: element borrows of a for-x-in-mut-xs holder kept
// across iterations are disjoint; a borrow of the whole array while
// one is kept is VRemovedTag.
func TestSB_ForMutDisjoint(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, whole bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		xs, at := arrayLocal(w, x)
		ms, mi := w.tab.MutSlice(types.I64), w.tab.Mut(types.I64)
		h, i0, i1, e0, e1 := x.local(ms), x.local(types.Int), x.local(types.Int), x.local(mi), x.local(mi)
		// The holder (its origin changes nothing in the tagged mode).
		x.assign(0, lp(h), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: lp(xs)})
		x.assign(0, lp(i0), use(imm(types.Int, 0)))
		x.assign(0, lp(i1), use(imm(types.Int, 1)))
		x.assign(0, lp(e0), mutOf(x.b.Place(h, mir.Projection{Kind: mir.ProjIndex, A: uint32(i0), Type: types.I64})))
		x.assign(0, lp(e1), mutOf(x.b.Place(h, mir.Projection{Kind: mir.ProjIndex, A: uint32(i1), Type: types.I64})))
		x.assign(0, x.deref(e0, types.I64), use(imm(types.I64, 10)))
		x.assign(0, x.deref(e1, types.I64), use(imm(types.I64, 20)))
		if whole {
			wr := x.local(w.tab.Ref(at))
			x.assign(0, lp(wr), refOf(lp(xs)))
		}
		x.assign(0, x.deref(e0, types.I64), use(imm(types.I64, 30)))
		x.assign(0, lp(0), use(mir.Copy(x.b.Place(xs, mir.Projection{Kind: mir.ProjIndex, A: uint32(i1), Type: types.I64}))))
		x.ret(0)
		x.done()
		return f
	}
	r := w.tcall(w.evaluator(0), mk("Disjoint", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 20 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Whole", true), nil), VRemovedTag)
}

// TestSB_SliceRanges: two live mut sub-slices of disjoint ranges are clean;
// overlapping ones are VRemovedTag (the dynamic twin of the static check).
func TestSB_SliceRanges(t *testing.T) {
	w := newWorld(t)
	mk := func(name string, split int64) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		xs, _ := arrayLocal(w, x)
		ms := w.tab.MutSlice(types.I64)
		s1, s2, k := x.local(ms), x.local(ms), x.local(types.Int)
		x.assign(0, lp(s1), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: lp(xs), HasLo: true, A: imm(types.Int, 0), HasHi: true, B: imm(types.Int, split)})
		x.assign(0, lp(s2), mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Exclusive, Place: lp(xs), HasLo: true, A: imm(types.Int, 2), HasHi: true, B: imm(types.Int, 4)})
		x.assign(0, lp(k), use(imm(types.Int, 0)))
		x.assign(0, x.b.Place(s2, mir.Projection{Kind: mir.ProjIndex, A: uint32(k), Type: types.I64}), use(imm(types.I64, 9)))
		x.assign(0, lp(k), use(imm(types.Int, split-1)))
		x.assign(0, x.b.Place(s1, mir.Projection{Kind: mir.ProjIndex, A: uint32(k), Type: types.I64}), use(imm(types.I64, 8)))
		x.assign(0, lp(0), use(mir.Copy(x.b.Place(xs, mir.Projection{Kind: mir.ProjIndex, A: uint32(k), Type: types.I64}))))
		x.ret(0)
		x.done()
		return f
	}
	r := w.tcall(w.evaluator(0), mk("Disjoint", 2), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 8 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Overlap", 3), nil), VRemovedTag)
}

// TestSB_CellInterior: a Cell shared twice and written through both is
// clean (a raw write to a Cell's memory is defined while only shared loans
// cover it); a stored reference into a dead local read through the cell is
// VDeadScope.
func TestSB_CellInterior(t *testing.T) {
	w := newWorld(t)
	ri := w.tab.Ref(types.I64)
	cell, cellT := w.structType("std.core", "Cell", ri)
	w.known.Cell = cell
	rc := w.tab.Ref(cellT)
	field := func(x *bb, r mir.LocalID) mir.Place {
		return x.b.Place(r, mir.Projection{Kind: mir.ProjDeref, Type: cellT}, mir.Projection{Kind: mir.ProjField, A: 0, Type: ri})
	}
	mk := func(name string, dead bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := w.body(f, types.I64)
		a, y, ra, ry, c, c1, c2, out := x.local(types.I64), x.local(types.I64), x.local(ri), x.local(ri), x.local(cellT), x.local(rc), x.local(rc), x.local(ri)
		x.assign(0, lp(a), use(imm(types.I64, 1)))
		x.assign(0, lp(y), use(imm(types.I64, 2)))
		x.assign(0, lp(ra), refOf(lp(a)))
		x.assign(0, lp(ry), refOf(lp(y)))
		x.assign(0, lp(c), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: cell, Ops: x.b.AddOperands(cp(ra))})
		x.assign(0, lp(c1), refOf(lp(c)))
		x.assign(0, lp(c2), refOf(lp(c)))
		x.assign(0, field(x, c1), use(cp(ry))) // a write through one shared tag
		if dead {
			x.scopeEnd(0, y)
		} else {
			x.assign(0, field(x, c2), use(cp(ra))) // and through the other
		}
		x.assign(0, lp(out), use(mir.Copy(field(x, c1))))
		x.assign(0, lp(0), use(mir.Copy(x.deref(out, types.I64))))
		x.ret(0)
		x.done()
		return f
	}
	r := w.tcall(w.evaluator(0), mk("Shared", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 1 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Dead", true), nil), VDeadScope)
}

// TestSB_FactsDisagree: a type with a Cell field whose layout says Freeze is
// VFacts when it is borrowed.
func TestSB_FactsDisagree(t *testing.T) {
	w := newWorld(t)
	cell, cellT := w.structType("std.core", "Cell", types.I64)
	w.known.Cell = cell
	h, ht := w.structType("lib", "Holder", cellT, types.I64)
	l := w.structOf([]types.TypeID{cellT, types.I64}, 0)
	l.Shape.Freeze = true // the fact under test: wrong
	w.layouts[ht] = l
	f := w.fn("lib", "Bad", types.I64)
	x := w.body(f, types.I64)
	c, hv, r := x.local(cellT), x.local(ht), x.local(w.tab.Ref(ht))
	x.assign(0, lp(c), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: cell, Ops: x.b.AddOperands(imm(types.I64, 1))})
	x.assign(0, lp(hv), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: h, Ops: x.b.AddOperands(cp(c), imm(types.I64, 2))})
	x.assign(0, lp(r), refOf(lp(hv)))
	x.assign(0, lp(0), use(imm(types.I64, 0)))
	x.ret(0)
	x.done()
	wantViolation(t, w.tcall(w.evaluator(0), f, nil), VFacts)
}

// TestSB_MutViewInterior: a mut view struct is interior for tagged mode —
// a bogus Freeze=true layout reports VFacts, and Freeze=false permits raw
// writes through a shared reference without VSharedWrite.
func TestSB_MutViewInterior(t *testing.T) {
	w := newWorld(t)
	_, of, write, _ := w.rawFns()
	mv, mvt := w.structType("lib", "MV", types.I64)
	w.items.Get(mv).Decl.(*types.StructDecl).View = true
	w.items.Get(mv).Decl.(*types.StructDecl).MutView = true
	// Bogus Freeze=true layout must report VFacts on borrow.
	badLay := w.structOf([]types.TypeID{types.I64}, 0)
	badLay.Shape.Freeze = true
	w.layouts[mvt] = badLay
	fBad := w.fn("lib", "BadFreeze", types.I64)
	xb := w.body(fBad, types.I64)
	lv, lr := xb.local(mvt), xb.local(w.tab.Ref(mvt))
	xb.assign(0, lp(lv), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: mv, Ops: xb.b.AddOperands(imm(types.I64, 1))})
	xb.assign(0, lp(lr), refOf(lp(lv)))
	xb.assign(0, lp(0), use(imm(types.I64, 0)))
	xb.ret(0)
	xb.done()
	wantViolation(t, w.tcall(w.evaluator(0), fBad, nil), VFacts)

	// Correct Freeze=false layout permits a raw write under a shared borrow.
	okLay := w.structOf([]types.TypeID{types.I64}, 0)
	okLay.Shape.Freeze = false
	w.layouts[mvt] = okLay
	fOk := w.fn("lib", "WriteUnderShared", types.I64)
	s := &seq{bb: w.body(fOk, types.I64)}
	v, r, p, u := s.local(mvt), s.local(w.tab.Ref(mvt)), s.local(w.tab.RawPtr(mvt)), s.local(types.Void)
	s.assign(s.cur, lp(v), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: mv, Ops: s.b.AddOperands(imm(types.I64, 1))})
	s.assign(s.cur, lp(r), refOf(lp(v)))
	s.call(of, []types.TypeID{mvt}, lp(p), cp(r))
	s.call(write, []types.TypeID{mvt}, lp(u), cp(p), cp(v))
	s.assign(s.cur, lp(0), use(imm(types.I64, 0)))
	s.ret(s.cur)
	s.done()
	wantClean(t, w.tcall(w.evaluator(0), fOk, nil))
}

// TestSB_PageMemoryOwner: memory from the page override accessed through
// its own pointer after other tags is clean; freed then read is VDead.
func TestSB_PageMemoryOwner(t *testing.T) {
	w := newWorld(t)
	_, _, write, read := w.rawFns()
	pu8 := w.tab.RawPtr(types.U8)
	w.pageAlloc = w.fn("std.rt", "PageAlloc", pu8, types.Int)
	w.pageFree = w.fn("std.rt", "PageFree", types.Void, pu8, types.Int)
	tp, tq := w.tab.Param(0), w.tab.Param(1)
	cast := w.raw("PtrCast", w.tab.RawPtr(tq), w.tab.RawPtr(tp))
	pi, ri := w.tab.RawPtr(types.I64), w.tab.Ref(types.I64)
	mk := func(name string, free bool) types.ItemID {
		f := w.fn("lib", name, types.I64)
		x := &seq{bb: w.body(f, types.I64)}
		p8, p, r, u, h := x.local(pu8), x.local(pi), x.local(ri), x.local(types.Void), x.local(types.I64)
		x.call(w.pageAlloc, nil, lp(p8), imm(types.Int, 64))
		x.call(cast, []types.TypeID{types.U8, types.I64}, lp(p), cp(p8))
		x.call(write, []types.TypeID{types.I64}, lp(u), cp(p), imm(types.I64, 1))
		x.assign(x.cur, lp(r), refOf(x.deref(p, types.I64)))
		x.assign(x.cur, lp(h), use(mir.Copy(x.deref(r, types.I64))))
		x.call(write, []types.TypeID{types.I64}, lp(u), cp(p), imm(types.I64, 5)) // the owner, after r's tag
		if free {
			x.call(w.pageFree, nil, lp(u), cp(p8), imm(types.Int, 64))
		}
		x.call(read, []types.TypeID{types.I64}, lp(0), cp(p))
		x.ret(x.cur)
		x.done()
		return f
	}
	r := w.tcall(w.evaluator(0), mk("Owner", false), nil)
	wantClean(t, r)
	if w.intOf(r.Value) != 5 {
		t.Errorf("value %d", w.intOf(r.Value))
	}
	wantViolation(t, w.tcall(w.evaluator(0), mk("Freed", true), nil), VDead)
}

// TestSB_StepsEqual: the tagged mode charges exactly the plain mode's steps
// and gives the same values on every evaluation of the step golden.
func TestSB_StepsEqual(t *testing.T) {
	for _, c := range goldenCases {
		w := newWorld(t)
		f, args := c.mk(w)
		e := w.evaluator(0)
		p := w.call(e, f, 0, args...)
		r := w.tcall(e, f, nil, args...)
		if p.Err != nil || r.Err != nil || len(r.Violations) > 0 || r.Skipped != "" {
			t.Fatalf("%s: plain %+v, tagged %+v %v %q", c.name, p.Err, r.Err, r.Violations, r.Skipped)
		}
		if p.Value != r.Value || p.Steps != r.Steps {
			t.Errorf("%s: plain %v in %d steps, tagged %v in %d", c.name, p.Value, p.Steps, r.Value, r.Steps)
		}
	}
}

// TestSB_PlainModeUnchanged: plain runs make no tagged state and use the
// shared flat code; tagged runs use their own compiled form, and a plain run
// after a tagged one is still the same.
func TestSB_PlainModeUnchanged(t *testing.T) {
	w := newWorld(t)
	f := w.loopFn("lib", "Loop")
	e := w.evaluator(0)
	n := w.intVal(types.Int, 50)
	p1 := w.call(e, f, 0, n)
	if len(e.tcode) != 0 {
		t.Fatalf("a plain run made tagged code")
	}
	before := e.preEntries.Load()
	r := w.tcall(e, f, nil, n)
	if e.preEntries.Load() != before {
		t.Errorf("a tagged run charged a block once (the flat path ran)")
	}
	p2 := w.call(e, f, 0, n)
	if p1.Value != p2.Value || p1.Steps != p2.Steps || r.Value != p1.Value || r.Steps != p1.Steps {
		t.Errorf("plain %v/%d, tagged %v/%d, plain again %v/%d", p1.Value, p1.Steps, r.Value, r.Steps, p2.Value, p2.Steps)
	}
	if len(e.tcode) == 0 {
		t.Errorf("the tagged run made no tagged code")
	}
	for b, cd := range e.code {
		if cd.slow {
			t.Errorf("plain code of %v is the tagged form", b.Item)
		}
	}
}

// TestSB_RejectedOnerrGuard: under Rejected, an onerr guard with no
// decision ends the run Skipped, never an ICE.
func TestSB_RejectedOnerrGuard(t *testing.T) {
	w := newWorld(t)
	f := w.fn("lib", "Guarded", types.I64)
	x := w.body(f, types.I64)
	yes, no := x.block(), x.block()
	x.term(0, mir.Terminator{Kind: mir.TermOnerrGuard, Yes: yes, No: no})
	x.assign(yes, lp(0), use(imm(types.I64, 1)))
	x.ret(yes)
	x.assign(no, lp(0), use(imm(types.I64, 2)))
	x.ret(no)
	w.bodies[f] = &Body{MIR: x.b, State: BodyReady}
	r := w.tcall(w.evaluator(0), f, &TagOptions{Rejected: true})
	if r.Skipped != "onerr guard" {
		t.Fatalf("skipped %q, err %+v", r.Skipped, r.Err)
	}
}

// TestSB_ViolationKindNames pins the record spellings.
func TestSB_ViolationKindNames(t *testing.T) {
	for k := VDead; k <= VFacts; k++ {
		back, ok := ParseViolationKind(k.String())
		if !ok || back != k {
			t.Errorf("%d: %q parses to %v %v", k, k.String(), back, ok)
		}
	}
	if _, ok := ParseViolationKind("VNone"); ok {
		t.Errorf("unknown name parsed")
	}
}
