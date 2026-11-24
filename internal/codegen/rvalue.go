// Why: an assignment must evaluate in Mk's order and give every operator
// its exact semantics: overflow, division and shift checks, float rules and
// string comparison.
//
// What: assign, mayAlias, scalarRvalue, the const and global references,
// unary and binary (with divChecks and the overflow intrinsics),
// floatBinary and floatRem, and strCompare.
//
// How: the rvalue first, then the destination address; aggregates are built
// in place unless an operand may alias the destination. Integer arithmetic
// uses llvm.*.with.overflow and traps; a sum the producer proved in range
// gets one nsw or nuw flag. Float % calls std.math.Rem32 or Rem64, never
// frem.

package codegen

import (
	"math"

	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func (fg *fnGen) assign(p mir.Place, rv *mir.Rvalue) {
	g := fg.g
	if fg.pnoneTableLocal(int(p.Local)) != types.NoItem {
		return // the local is its preserve_nonecc table global (pnone.go)
	}
	if fg.promoted != nil && fg.promoted[p.Local] != 0 {
		return // the local is its constant's global (promote.go)
	}
	dt := fg.b.PlaceType(p)
	switch rv.Kind {
	case mir.RvAggregate:
		fg.aggregate(p, dt, rv)
		return
	case mir.RvRepeat:
		fg.repeat(p, dt, rv)
		return
	case mir.RvSlice:
		ptr, n := fg.slice(rv)
		d, _ := fg.placeAddr(p)
		fg.cur.Store(ptr, d.v, alignAt(d.align, 0))
		fg.cur.Store(n, fg.gep(d.v, 8), alignAt(d.align, 8))
		return
	case mir.RvConstRef:
		v := fg.constItem(rv.Item, dt)
		d, _ := fg.placeAddr(p)
		fg.store(d, v)
		return
	case mir.RvCast:
		if rv.Cast == mir.CastWidenErrors {
			src := fg.operand(rv.A)
			d, _ := fg.placeAddr(p)
			fg.widenErrors(d, dt, src)
			return
		}
	}
	var v value
	if rv.Kind == mir.RvUse {
		v = fg.operand(rv.A)
		if v.kind == rAgg {
			// The source may overlap the destination only when equal
			// (llvm.memcpy allows that); a projection through a deref may not
			// be proven equal, so copy through a temporary then.
			if fg.mayAlias(p, rv.A) {
				tmp := fg.temp(v.t)
				fg.store(tmp, v)
				v.a = tmp
			}
		}
	} else {
		v = value{t: dt, kind: g.reprOf(dt).kind, v: fg.scalarRvalue(rv, dt)}
	}
	d, _ := fg.placeAddr(p)
	fg.store(d, v)
}

// mayAlias reports whether operand o's place may overlap destination p
// without being p: same local with different projections, or either goes
// through a deref.
func (fg *fnGen) mayAlias(p mir.Place, o mir.Operand) bool {
	if o.Kind != mir.OpCopy && o.Kind != mir.OpMove {
		return false
	}
	q := o.Place
	if q == p {
		return false
	}
	deref := func(pl mir.Place) bool {
		for _, s := range fg.b.Proj(pl) {
			if s.Kind == mir.ProjDeref {
				return true
			}
		}
		return false
	}
	return q.Local == p.Local || deref(p) || deref(q)
}

// scalarRvalue computes a scalar-valued rvalue.
func (fg *fnGen) scalarRvalue(rv *mir.Rvalue, dt types.TypeID) llvm.Value {
	g, m := fg.g, fg.g.m
	dr := g.reprOf(dt)
	switch rv.Kind {
	case mir.RvRef:
		if rv.Borrow == mir.Shared {
			a, _ := fg.readAddr(rv.Place)
			return a.v
		}
		a, _ := fg.placeAddr(rv.Place)
		return a.v
	case mir.RvUnary:
		return fg.unary(rv.Un, fg.operand(rv.A))
	case mir.RvBinary:
		return fg.binary(rv, fg.operand(rv.A), fg.operand(rv.B))
	case mir.RvCast:
		return fg.cast(rv.Cast, fg.operand(rv.A), dt)
	case mir.RvLen:
		pt := fg.b.PlaceType(rv.Place)
		ty := g.tt.Get(pt)
		if ty.Kind == types.KArray {
			return m.ConstInt(m.I(64), g.constLen(ty.Len))
		}
		a, _ := fg.readAddr(rv.Place)
		return fg.loadLen(fg.gep(a.v, 8), alignAt(a.align, 8), pt)
	case mir.RvDiscriminant:
		return fg.discriminant(rv.Place)
	case mir.RvFnRef:
		args := fg.b.GenericArgs[rv.Fn.Args.Start:rv.Fn.Args.End()]
		return g.fnRef(rv.Fn.Item, args, fg.fi)
	case mir.RvGlobalRef:
		return g.globalRef(rv.Item)
	case mir.RvSizeOf:
		s, _ := g.sizeAlign(rv.Type)
		return m.ConstInt(dr.ssa, s)
	case mir.RvAlignOf:
		_, a := g.sizeAlign(rv.Type)
		return m.ConstInt(dr.ssa, uint64(a))
	case mir.RvFieldCount:
		return m.ConstInt(dr.ssa, uint64(len(g.layout(rv.Type).Fields)))
	}
	source.ICEf(fg.spanOf(fg.span), "codegen: rvalue %s yielding %s", rv.Kind, g.tt.TypeString(dt, nil))
	return llvm.Value{}
}

// constLen is the value of an array length (a CAValue after substitution).
func (g *packGen) constLen(c types.ConstArg) uint64 {
	if c.Kind != types.CAValue {
		source.ICEf(source.Span{}, "codegen: array length of kind %d in an instance", c.Kind)
	}
	cv := g.env.Consts().Get(c.Value)
	return types.LoadUint(cv.Bytes, len(cv.Bytes))
}

// constItem is the value of const item (RvConstRef).
func (fg *fnGen) constItem(item types.ItemID, t types.TypeID) value {
	g := fg.g
	cd, ok := g.items.Get(item).Decl.(*types.ConstDecl)
	if !ok || !cd.HasValue {
		source.ICEf(fg.spanOf(fg.span), "codegen: const %s has no value", g.items.Get(item).Key)
	}
	return fg.operand(mir.Const(t, cd.Value))
}

// globalRef is the address of a var global (or of a const's storage).
func (g *packGen) globalRef(item types.ItemID) llvm.Value {
	it := g.items.Get(item)
	switch d := it.Decl.(type) {
	case *types.ConstDecl:
		if !d.HasValue {
			source.ICEf(it.Span, "codegen: const %s has no value", it.Key)
		}
		return g.constGlobal(d.Value)
	case *types.GlobalDecl:
		return g.varGlobal(item, d)
	}
	source.ICEf(it.Span, "codegen: %s is not a global", it.Key)
	return llvm.Value{}
}

// unary is RvUnary: Neg traps on overflow (unsigned: on any value but
// 0), floats fneg; Not on bool; BitNot.
func (fg *fnGen) unary(op mir.UnOp, a value) llvm.Value {
	g, m := fg.g, fg.g.m
	r := g.reprOf(a.t)
	switch op {
	case mir.Neg:
		if r.class == sFloat {
			return fg.cur.Bin(llvm.FNeg, a.v, a.v, 0)
		}
		zero := m.ConstInt(r.ssa, 0)
		if g.cfg.TestNoOverflowChecks {
			return fg.cur.Bin(llvm.Sub, zero, a.v, cFlags(r.signed))
		}
		if !r.signed {
			fg.trap(fg.cur.ICmp(llvm.INE, a.v, zero), types.PanicOverflow)
			return zero
		}
		return fg.overflowOp("ssub.with.overflow", zero, a.v)
	case mir.Not:
		return fg.cur.Bin(llvm.Xor, a.v, m.ConstInt(r.ssa, 1), 0)
	case mir.BitNot:
		return fg.cur.Bin(llvm.Xor, a.v, m.ConstInt(r.ssa, math.MaxUint64), 0)
	}
	source.ICEf(fg.spanOf(fg.span), "codegen: unary operator %d", op)
	return llvm.Value{}
}

// cFlags is the flag C gives + - * of a type of that signedness: nsw on a
// signed one, none on an unsigned one (Config.TestNoOverflowChecks only).
func cFlags(signed bool) llvm.Flags {
	if signed {
		return llvm.NSW
	}
	return 0
}

// overflowOp calls llvm.<name>.iN and traps on its overflow flag.
func (fg *fnGen) overflowOp(name string, x, y llvm.Value) llvm.Value {
	f := fg.g.m.IntrinsicFn(name, x.Ty)
	res := fg.cur.CallFunc(f, []llvm.Value{x, y})
	fg.trap(fg.cur.ExtractValue(res, 1), types.PanicOverflow)
	return fg.cur.ExtractValue(res, 0)
}

// constDivisor reports a constant divisor, which needs no zero or
// MIN / -1 test unless it is 0 or -1.
func constDivisor(o mir.Operand, r *repr) (uint64, bool) {
	if o.Kind != mir.OpImm {
		return 0, false
	}
	bits := o.Bits
	if w := r.ssa.Bits(); w < 64 {
		bits &= 1<<uint(w) - 1
	}
	return bits, true
}

// binary is RvBinary with its run-time checks.
func (fg *fnGen) binary(rv *mir.Rvalue, a, b value) llvm.Value {
	g, m := fg.g, fg.g.m
	op := rv.Bin
	ar := g.reprOf(a.t)
	if g.tt.Kind(a.t) == types.KStr {
		return fg.strCompare(op, a, b)
	}
	if ar.class == sFloat {
		return fg.floatBinary(op, a, b, ar)
	}
	if op.IsCompare() {
		if (op == mir.Eq || op == mir.Ne) && fg.covered() && ar.class == sInt &&
			(ar.size == 2 || ar.size == 4 || ar.size == 8) &&
			(rv.A.Kind != mir.OpImm || rv.B.Kind != mir.OpImm) {
			fg.coverCmp(a.v, b.v, uint8(ar.size))
		}
		return fg.cur.ICmp(intPred(op, ar.signed && ar.class == sInt), a.v, b.v)
	}
	x, y := a.v, b.v
	if g.cfg.TestNoOverflowChecks && (op == mir.Add || op == mir.Sub || op == mir.Mul) {
		bo := map[mir.BinOp]llvm.BinOp{mir.Add: llvm.Add, mir.Sub: llvm.Sub, mir.Mul: llvm.Mul}[op]
		return fg.cur.Bin(bo, x, y, cFlags(ar.signed))
	}
	switch op {
	case mir.Add:
		return fg.overflowOp(ovName("add", ar.signed), x, y)
	case mir.Sub:
		return fg.overflowOp(ovName("sub", ar.signed), x, y)
	case mir.Mul:
		return fg.overflowOp(ovName("mul", ar.signed), x, y)
	case mir.AddWrap:
		return fg.cur.Bin(llvm.Add, x, y, 0)
	case mir.AddNoWrap:
		// The producer proved the sum in range of the operand type,
		// so one flag by signedness: nsw on a signed type, nuw on an
		// unsigned one, never both (a signed -1 + 1 wraps as unsigned; an
		// unsigned 2^63-1 + 1 overflows as signed).
		if ar.signed {
			return fg.cur.Bin(llvm.Add, x, y, llvm.NSW)
		}
		return fg.cur.Bin(llvm.Add, x, y, llvm.NUW)
	case mir.SubNoWrap, mir.MulNoWrap:
		// AddNoWrap's rule for a proved - and *.
		bo := llvm.Sub
		if op == mir.MulNoWrap {
			bo = llvm.Mul
		}
		if ar.signed {
			return fg.cur.Bin(bo, x, y, llvm.NSW)
		}
		return fg.cur.Bin(bo, x, y, llvm.NUW)
	case mir.SubWrap:
		return fg.cur.Bin(llvm.Sub, x, y, 0)
	case mir.MulWrap:
		return fg.cur.Bin(llvm.Mul, x, y, 0)
	case mir.BitAnd:
		return fg.cur.Bin(llvm.And, x, y, 0)
	case mir.BitOr:
		return fg.cur.Bin(llvm.Or, x, y, 0)
	case mir.BitXor:
		return fg.cur.Bin(llvm.Xor, x, y, 0)
	case mir.Div, mir.Rem:
		fg.divChecks(rv.B, x, y, ar)
		ops := map[[2]bool]llvm.BinOp{{true, true}: llvm.SDiv, {true, false}: llvm.UDiv,
			{false, true}: llvm.SRem, {false, false}: llvm.URem}
		return fg.cur.Bin(ops[[2]bool{op == mir.Div, ar.signed}], x, y, 0)
	case mir.Shl, mir.Shr:
		br := g.reprOf(b.t)
		amt := fg.intTo(y, br.signed, m.I(64))
		fg.trap(fg.cur.ICmp(llvm.IUGE, amt, m.ConstInt(m.I(64), uint64(ar.ssa.Bits()))), types.PanicShiftAmount)
		amt = fg.intTo(y, br.signed, ar.ssa)
		switch {
		case op == mir.Shl:
			return fg.cur.Bin(llvm.Shl, x, amt, 0)
		case ar.signed:
			return fg.cur.Bin(llvm.AShr, x, amt, 0)
		}
		return fg.cur.Bin(llvm.LShr, x, amt, 0)
	}
	source.ICEf(fg.spanOf(fg.span), "codegen: binary operator %d on %s", op, g.tt.TypeString(a.t, nil))
	return llvm.Value{}
}

func ovName(op string, signed bool) string {
	if signed {
		return "s" + op + ".with.overflow"
	}
	return "u" + op + ".with.overflow"
}

func intPred(op mir.BinOp, signed bool) llvm.IPred {
	switch op {
	case mir.Eq:
		return llvm.IEQ
	case mir.Ne:
		return llvm.INE
	case mir.Lt:
		if signed {
			return llvm.ISLT
		}
		return llvm.IULT
	case mir.Le:
		if signed {
			return llvm.ISLE
		}
		return llvm.IULE
	case mir.Gt:
		if signed {
			return llvm.ISGT
		}
		return llvm.IUGT
	}
	if signed {
		return llvm.ISGE
	}
	return llvm.IUGE
}

// divChecks traps on a zero divisor and, for signed types, on MIN / -1
// (PanicDivideByZero), unless the divisor is a constant other than
// 0 and -1.
func (fg *fnGen) divChecks(bop mir.Operand, x, y llvm.Value, r *repr) {
	m := fg.g.m
	w := r.ssa.Bits()
	all := uint64(math.MaxUint64)
	if w < 64 {
		all = 1<<uint(w) - 1
	}
	if c, ok := constDivisor(bop, r); ok && c != 0 && (!r.signed || c != all) {
		return
	}
	fg.trap(fg.cur.ICmp(llvm.IEQ, y, m.ConstInt(r.ssa, 0)), types.PanicDivideByZero)
	if r.signed {
		minV := m.ConstInt(r.ssa, 1<<uint(w-1))
		bad := fg.cur.Bin(llvm.And, fg.cur.ICmp(llvm.IEQ, x, minV), fg.cur.ICmp(llvm.IEQ, y, m.ConstInt(r.ssa, all)), 0)
		fg.trap(bad, types.PanicDivideByZero)
	}
}

// floatBinary: fadd fsub fmul fdiv, ordered compares except une,
// and % through std.math.Rem32 or Rem64 (never frem).
func (fg *fnGen) floatBinary(op mir.BinOp, a, b value, r *repr) llvm.Value {
	x, y := a.v, b.v
	switch op {
	case mir.Add:
		return fg.cur.Bin(llvm.FAdd, x, y, 0)
	case mir.Sub:
		return fg.cur.Bin(llvm.FSub, x, y, 0)
	case mir.Mul:
		return fg.cur.Bin(llvm.FMul, x, y, 0)
	case mir.Div:
		return fg.cur.Bin(llvm.FDiv, x, y, 0)
	case mir.Rem:
		return fg.floatRem(a, b, r)
	case mir.Eq:
		return fg.cur.FCmp(llvm.FOEQ, x, y)
	case mir.Ne:
		return fg.cur.FCmp(llvm.FUNE, x, y)
	case mir.Lt:
		return fg.cur.FCmp(llvm.FOLT, x, y)
	case mir.Le:
		return fg.cur.FCmp(llvm.FOLE, x, y)
	case mir.Gt:
		return fg.cur.FCmp(llvm.FOGT, x, y)
	case mir.Ge:
		return fg.cur.FCmp(llvm.FOGE, x, y)
	}
	source.ICEf(fg.spanOf(fg.span), "codegen: float operator %d", op)
	return llvm.Value{}
}

// Float % calls std.math.Rem64 or Rem32 (never frem, which
// becomes a libm fmod call). They are ordinary std functions that mono
// roots as RootRuntime; the call goes through funcDecl, so the pack that
// defines one calls its definition and every other pack shares one
// declaration. A build without the instance is the ICE
// "missing runtime entry", never an undefined symbol at link.
var (
	keyRem64 = types.ItemKey("std.math", "Rem64")
	keyRem32 = types.ItemKey("std.math", "Rem32")
)

func (fg *fnGen) floatRem(a, b value, r *repr) llvm.Value {
	k, t := keyRem64, types.F64
	if r.size == 4 {
		k, t = keyRem32, types.F32
	}
	fi := fg.g.runtimeFn(k)
	if ls := fg.g.funcSigOf(fi); len(ls.params) != 2 || ls.params[0] != t || ls.params[1] != t || ls.result != t {
		source.ICEf(fg.spanOf(fg.span), "codegen: %s is not (a: %s, b: %s): %s", k, fg.g.tt.TypeString(t, nil), fg.g.tt.TypeString(t, nil), fg.g.tt.TypeString(t, nil))
	}
	tmp := fg.temp(t)
	fg.callRuntimeVals(fi, []value{a, b}, &tmp)
	return fg.loadScalar(tmp, r, false)
}

// strCompare: == and != are equal lengths and memcmp of the common length
// equal to 0; orderings compare memcmp over the shorter length, then the
// lengths.
//
// Equality branches on the lengths and calls memcmp only when they are
// equal: unequal lengths cost no call,
// and against a literal the length is a constant on the call's path, so
// LLVM's memcmp expansion turns `s == "GET"` into two loads and a compare
// (clang's code for `n == 3 && memcmp(p, "GET", 3) == 0`). Reading la bytes
// of both is in bounds there, as before, since la == lb.
func (fg *fnGen) strCompare(op mir.BinOp, a, b value) llvm.Value {
	m := fg.g.m
	i64 := m.I(64)
	ld := func(v value) (llvm.Value, llvm.Value) {
		p := fg.cur.Load(m.Ptr(), v.a.v, alignAt(v.a.align, 0), m.Metadata().NonNull(), m.Metadata().NoUndef())
		n := fg.cur.Load(i64, fg.gep(v.a.v, 8), alignAt(v.a.align, 8), m.Metadata().NoUndef())
		return p, n
	}
	pa, la := ld(a)
	pb, lb := ld(b)
	// DeclareAs: a Mk extern may declare memcmp with another type.
	memcmp := m.DeclareAs(symMemcmp, llvm.FuncSig{Linkage: llvm.External, Ret: m.I(32), Params: []llvm.Type{m.Ptr(), m.Ptr(), i64},
		FnAttrs: llvm.Attrs{}.Flag("nounwind")})
	zero32 := m.ConstInt(m.I(32), 0)
	if op == mir.Eq || op == mir.Ne {
		i8 := m.I(8)
		r := fg.tempBytes(1, 1)
		fg.cur.Store(m.ConstInt(i8, 0), r.v, 1)
		cmpB, done := fg.newBlock(), fg.newBlock()
		fg.cur.CondBr(fg.cur.ICmp(llvm.IEQ, la, lb), cmpB, done)
		fg.cur = cmpB
		c := fg.cur.CallFunc(memcmp, []llvm.Value{pa, pb, la})
		fg.cur.Store(fg.cur.Cast(llvm.ZExt, fg.cur.ICmp(llvm.IEQ, c, zero32), i8), r.v, 1)
		fg.cur.Br(done)
		fg.cur = done
		eq := fg.cur.Cast(llvm.Trunc, fg.cur.Load(i8, r.v, 1), m.I(1))
		if op == mir.Eq {
			return eq
		}
		return fg.cur.Bin(llvm.Xor, eq, m.ConstInt(m.I(1), 1), 0)
	}
	n := fg.cur.Select(fg.cur.ICmp(llvm.IULT, la, lb), la, lb)
	c := fg.cur.CallFunc(memcmp, []llvm.Value{pa, pb, n})
	byLen := fg.cur.ICmp(intPred(op, false), la, lb)
	byBytes := fg.cur.ICmp(intPred(op, true), c, zero32)
	return fg.cur.Select(fg.cur.ICmp(llvm.IEQ, c, zero32), byLen, byBytes)
}
