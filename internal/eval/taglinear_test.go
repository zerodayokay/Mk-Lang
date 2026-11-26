// Why: the tagged evaluator tracks linear obligations by MIR place, even
// when two places overlap or have no bytes.
// What: regressions for narrowing enum fields and for empty linear locals.
// How: small MIR bodies run clean or stop at the expected scope end.

package eval

import (
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

func TestSB_ProjectedEnumNarrow(t *testing.T) {
	w := newWorld(t)
	ticket, tt := w.shaped("Ticket", types.Linear)
	choice, ct := w.enumType("lib", "Choice", []types.TypeID{tt}, nil)
	single, st := w.structType("lib", "Single", ct)
	nested, nt := w.structType("lib", "Nested", st)
	pair, pt := w.structType("lib", "Pair", ct, ct)
	for _, ty := range []types.TypeID{ct, st, nt, pt} {
		w.layoutLocked(ty).Shape.Kind = types.Linear
	}
	for _, tc := range []struct {
		name                                  string
		fields                                []uint32
		nested, owning, take, takeFirst, leak bool
	}{
		{name: "Nested", fields: []uint32{0}, nested: true},
		{name: "BothPlain", fields: []uint32{0, 1}},
		{name: "UntestedSibling", fields: []uint32{0}, leak: true},
		{name: "TestSameFieldTwice", fields: []uint32{0, 0}, leak: true},
		{name: "OwningSibling", fields: []uint32{0, 1}, owning: true, leak: true},
		{name: "TakeSiblingAfterNarrow", fields: []uint32{0, 1}, owning: true, take: true},
		{name: "TakeSiblingBeforeNarrow", fields: []uint32{1, 0}, owning: true, take: true, takeFirst: true},
		{name: "TakeLeavesUntestedSibling", fields: []uint32{1}, owning: true, take: true, takeFirst: true, leak: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w.fn("lib", "Run"+tc.name, types.Void)
			x := w.body(f, types.Void)
			a := x.local(ct)
			x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggEnum, Item: choice, Variant: 1})
			r := mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggEnum, Item: choice, Variant: 1}
			if tc.owning {
				payload := x.local(tt)
				x.assign(0, lp(payload), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: ticket, Ops: x.b.AddOperands(imm(types.I64, 1))})
				r.Variant, r.Ops = 0, x.b.AddOperands(mir.Move(lp(payload)))
			}

			var p mir.LocalID
			if tc.nested {
				s := x.local(st)
				x.assign(0, lp(s), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: single, Ops: x.b.AddOperands(mir.Move(lp(a)))})
				p = x.local(nt)
				x.assign(0, lp(p), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: nested, Ops: x.b.AddOperands(mir.Move(lp(s)))})

			} else {
				b := x.local(ct)
				x.assign(0, lp(b), r)
				p = x.local(pt)
				x.assign(0, lp(p), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: pair, Ops: x.b.AddOperands(mir.Move(lp(a)), mir.Move(lp(b)))})
			}
			d := x.local(types.U64)
			var blk mir.BlockID
			take := func(blk mir.BlockID) {
				id := x.b.Place(p, mir.Projection{Kind: mir.ProjField, A: 1, Type: ct},
					mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelEnum, Type: ct},
					mir.Projection{Kind: mir.ProjField, Type: tt}, mir.Projection{Kind: mir.ProjField, Type: types.I64})
				x.assign(blk, lp(x.local(types.I64)), use(mir.Move(id)))
			}
			for _, i := range tc.fields {
				path := []mir.Projection{{Kind: mir.ProjField, A: i, Type: ct}}
				if tc.nested {
					path = append([]mir.Projection{{Kind: mir.ProjField, Type: st}}, path...)
				}
				next := x.block()
				x.assign(blk, lp(d), mir.Rvalue{Kind: mir.RvDiscriminant, Place: x.b.Place(p, path...), Type: types.U64})
				x.branch(blk, cp(d), next, next)
				blk = next
				if tc.take && tc.takeFirst && i == 1 {
					take(blk)
				}
			}
			if tc.take && !tc.takeFirst {
				take(blk)
			}
			end := x.scopeEnd(blk, p)
			x.ret(blk)
			x.done()
			got := w.tcall(w.evaluator(0), f, nil)
			if tc.leak {
				if v := wantViolation(t, got, VLinear); v.At != spanOf(x, end) {
					t.Errorf("violation at %v, want scope end %v", v.At, spanOf(x, end))
				}
			} else {
				wantClean(t, got)
			}
		})
	}
}

func TestSB_EmptyLinearLocal(t *testing.T) {
	w := newWorld(t)
	token, tt := w.structType("lib", "Token")
	w.layoutLocked(tt).Shape.Kind = types.Linear
	for _, tc := range []struct {
		name                                      string
		move, consume, selfMove, uninit, frameEnd bool
	}{
		{name: "Uninitialized", uninit: true},
		{name: "ScopeLeak"},
		{name: "FrameLeak", frameEnd: true},
		{name: "Consume", consume: true},
		{name: "MoveLeak", move: true},
		{name: "MoveAndConsume", move: true, consume: true},
		{name: "SelfMove", selfMove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w.fn("lib", "Run"+tc.name, types.Void)
			x := w.body(f, types.Void)
			a, b := x.local(tt), x.local(tt)
			if !tc.uninit {
				x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: token})
			}
			active := a
			if tc.move {
				x.assign(0, lp(b), use(mir.Move(lp(a))))
				x.scopeEnd(0, a)
				active = b
			}
			if tc.selfMove {
				x.assign(0, lp(a), use(mir.Move(lp(a))))
			}
			if tc.consume {
				x.b.Blocks[0].Stmts = append(x.b.Blocks[0].Stmts, mir.Statement{Kind: mir.StConsume, Place: lp(active), Span: x.sp()})
			}
			end := mir.SpanID(0)
			if !tc.frameEnd {
				end = x.scopeEnd(0, active)
			}
			ret := x.ret(0)
			x.done()
			got := w.tcall(w.evaluator(0), f, nil)
			if tc.consume || tc.uninit {
				wantClean(t, got)
			} else {
				if tc.frameEnd {
					end = ret
				}
				if v := wantViolation(t, got, VLinear); v.At != spanOf(x, end) || v.Len != 0 {
					t.Errorf("violation = %+v, want zero-byte local at %v", v, spanOf(x, end))
				}
			}
		})
	}
}

func TestSB_ZeroByteFieldTake(t *testing.T) {
	w := newWorld(t)
	marker, mt := w.structType("lib", "Marker")
	token, tt := w.structType("lib", "Token", mt)
	pair, pt := w.structType("lib", "Pair", tt, tt)
	w.layoutLocked(tt).Shape.Kind = types.Linear
	w.layoutLocked(pt).Shape.Kind = types.Linear
	for _, tc := range []struct {
		name   string
		second uint32
	}{
		{name: "Both", second: 1},
		{name: "SameFieldTwice", second: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := w.fn("lib", "Run"+tc.name, types.Void)
			x := w.body(f, types.Void)
			m, a, b, p, d := x.local(mt), x.local(tt), x.local(tt), x.local(pt), x.local(mt)
			x.assign(0, lp(m), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: marker})
			for _, z := range []mir.LocalID{a, b} {
				x.assign(0, lp(z), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: token, Ops: x.b.AddOperands(cp(m))})
			}
			x.assign(0, lp(p), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: pair, Ops: x.b.AddOperands(mir.Move(lp(a)), mir.Move(lp(b)))})
			for _, i := range []uint32{0, tc.second} {
				field := x.b.Place(p, mir.Projection{Kind: mir.ProjField, A: i, Type: tt}, mir.Projection{Kind: mir.ProjField, Type: mt})
				x.assign(0, lp(d), use(mir.Move(field)))
			}
			end := x.scopeEnd(0, p)
			x.ret(0)
			x.done()
			got := w.tcall(w.evaluator(0), f, nil)
			if tc.second == 1 {
				wantClean(t, got)
			} else if v := wantViolation(t, got, VLinear); v.At != spanOf(x, end) {
				t.Errorf("violation at %v, want the pair's scope end %v", v.At, spanOf(x, end))
			}
		})
	}
}

func TestSB_EmptyLinearErrorEdge(t *testing.T) {
	w := newWorld(t)
	token, tt := w.structType("lib", "Token")
	w.layoutLocked(tt).Shape.Kind = types.Linear
	full := w.errorItem("lib", "Full")
	errors := w.tab.ErrorSet([]types.ErrorMember{{Item: full}})
	errType, resultType := w.tab.ErrorSetType(errors), w.tab.ErrorUnion(errors, types.Void)
	fail := w.fn("lib", "Fail", types.Void, tt)
	w.items.Get(fail).Decl.(*types.FuncDecl).Sig.Errors = errors
	y := w.body(fail, resultType, tt)
	e := y.local(errType)
	y.assign(0, lp(e), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggError, Item: full})
	y.assign(0, lp(0), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggErr, Ops: y.b.AddOperands(mir.Move(lp(e)))})
	y.ret(0)
	y.done()

	f := w.fn("lib", "Caller", types.Void)
	x := w.body(f, types.Void)
	a, slot, errLocal := x.local(tt), x.local(tt), x.local(errType)
	ok, err := x.block(), x.block()
	x.assign(0, lp(a), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: token})
	x.assign(0, lp(slot), use(mir.Move(lp(a))))
	x.term(0, mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: fail}},
		Args:   x.b.AddArgs(mir.Arg{Mode: mir.ArgMove, Operand: mir.Move(lp(slot))}),
		Target: ok, HasErr: true, Err: mir.ErrorEdge{Kind: mir.EdgeElse, Block: err, Error: errLocal}, Spawn: mir.NoLocal})
	x.ret(ok)
	x.scopeEnd(err, slot)
	end := x.scopeEnd(err, a)
	x.ret(err)
	x.done()
	got := w.tcall(w.evaluator(0), f, nil)
	if v := wantViolation(t, got, VLinear); v.At != spanOf(x, end) {
		t.Errorf("violation at %v, want the restored argument's scope end %v", v.At, spanOf(x, end))
	}
}
