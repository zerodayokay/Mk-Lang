// Why: overflow-checked and wide arithmetic, float-to-int wrapping, field
// offsets, prefetch, cycle counters and element-wise atomic copies each map
// to one instruction or a short branch-free sequence that only codegen can
// emit.
//
// What: extraSigs and extraLowerings, registered in init, and one lowering per
// intrinsic: std.math's overflow, wrapping, saturating and wide forms,
// std.mem's OffsetOf and Prefetch, std.runtime's Cycles, std.atomic's
// Pause, and std.raw's FnFromAddr, AtomicCopy and AtomicFill.
//
// How: each key is registered with the signature its lowering assumes
// (TestIntrinsicTableMatchesStd), and each lowering is inline:
//
//   - Add/Sub/MulOverflow: llvm.{s,u}{add,sub,mul}.with.overflow.iN, both
//     results used.
//   - WrapI32, WrapU32: a fast path, fptosi when |x| < 2^31, and a slow
//     path through the exponent and mantissa, chosen by select, so no
//     branch and no poison reaches the result.
//   - CastSat, CastSatF32: llvm.fpto{s,u}i.sat (NaN gives 0).
//   - MulWide: through i128, as raw.Mul128. DivWide: the two panics, then
//     `divq` on x86_64 (LLVM's i128 udiv calls compiler-rt's __udivti3,
//     which Mk does not link) and a 64-step shift-subtract elsewhere.
//   - AddCarry, SubBorrow: raw.AddCarry64's and SubBorrow64's lowering.
//   - OffsetOf: the layout's offset as a constant (check has refused a
//     field a closed T lacks).
//   - Prefetch: llvm.prefetch (read or write by the argument, locality 3).
//   - Cycles: llvm.readcyclecounter. Pause: `pause` (x86_64), `yield`
//     (aarch64), as inline asm so no feature is needed.
//   - FnFromAddr: inttoptr.
//   - AtomicCopy, AtomicFill: a loop of atomic loads and stores of the
//     element's width, forward or (when dst is above src) backward, as
//     memmove copies. Each access is monotonic (relaxed), which is
//     stronger than unordered and costs the same
//     plain move on x86_64 and aarch64: LLVM's loop-idiom pass turns a
//     loop of unordered accesses into its element-wise unordered-atomic
//     memcpy, which lowers to a __llvm_memcpy_element_unordered_atomic_N
//     call no Mk link provides (seen at -O2), and leaves a relaxed loop
//     alone. The intrinsics themselves are not used for the same reason,
//     and their memset form fills with a byte only.

package codegen

import (
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// extraSigs are this file's intrinsic keys and their intrinsicSigs forms.
var extraSigs = map[string]string{
	"std.math.AddOverflow": "[T: Integer](T, T): (value: T, overflow: bool)",
	"std.math.SubOverflow": "[T: Integer](T, T): (value: T, overflow: bool)",
	"std.math.MulOverflow": "[T: Integer](T, T): (value: T, overflow: bool)",
	"std.math.WrapI32":     "(f64): i32",
	"std.math.WrapU32":     "(f64): u32",
	"std.math.CastSat":     "[T: Integer](f64): T",
	"std.math.CastSatF32":  "[T: Integer](f32): T",
	"std.math.MulWide":     "(u64, u64): (hi: u64, lo: u64)",
	"std.math.DivWide":     "(u64, u64, u64): (q: u64, r: u64)",
	"std.math.AddCarry":    "(u64, u64, u64): (sum: u64, carry: u64)",
	"std.math.SubBorrow":   "(u64, u64, u64): (diff: u64, borrow: u64)",
	"std.mem.OffsetOf":     "[T](str): int",
	"std.mem.Prefetch":     "[T](&T, bool)",
	"std.runtime.Cold":     "()",
	"std.runtime.Cycles":   "(): u64",
	"std.runtime.Likely":   "(bool): bool",
	"std.runtime.Unlikely": "(bool): bool",
	"std.atomic.Pause":     "()",
	"std.raw.FnFromAddr":   "[F](uint): F",
	"std.raw.AtomicCopy":   "[T](*T, *T, int)",
	"std.raw.AtomicFill":   "[T](*T, T, int)",
}

// extraLowerings are the lowerings by key.
var extraLowerings = map[string]targetLowering{
	"std.math.AddOverflow": func(fg *fnGen, t *mir.Terminator, a []value) { fg.overflowFlag(t, a, "add") },
	"std.math.SubOverflow": func(fg *fnGen, t *mir.Terminator, a []value) { fg.overflowFlag(t, a, "sub") },
	"std.math.MulOverflow": func(fg *fnGen, t *mir.Terminator, a []value) { fg.overflowFlag(t, a, "mul") },
	"std.math.WrapI32":     func(fg *fnGen, t *mir.Terminator, a []value) { fg.wrap32(t, a, "std.math.WrapI32") },
	"std.math.WrapU32":     func(fg *fnGen, t *mir.Terminator, a []value) { fg.wrap32(t, a, "std.math.WrapU32") },
	"std.math.CastSat":     func(fg *fnGen, t *mir.Terminator, a []value) { fg.castSatOp(t, a, "std.math.CastSat") },
	"std.math.CastSatF32":  func(fg *fnGen, t *mir.Terminator, a []value) { fg.castSatOp(t, a, "std.math.CastSatF32") },
	"std.math.MulWide":     (*fnGen).mulWide,
	"std.math.DivWide":     (*fnGen).divWide,
	"std.math.AddCarry": func(fg *fnGen, t *mir.Terminator, a []value) {
		fg.carryOp(t, a, "std.math.AddCarry", "uadd.with.overflow", "sum", "carry")
	},
	"std.math.SubBorrow": func(fg *fnGen, t *mir.Terminator, a []value) {
		fg.carryOp(t, a, "std.math.SubBorrow", "usub.with.overflow", "diff", "borrow")
	},
	"std.mem.OffsetOf":   (*fnGen).offsetOf,
	"std.mem.Prefetch":   (*fnGen).prefetch,
	"std.runtime.Cold":   func(fg *fnGen, t *mir.Terminator, _ []value) { fg.targetDone(t) },
	"std.runtime.Cycles": (*fnGen).cycles,
	"std.runtime.Likely": func(fg *fnGen, t *mir.Terminator, a []value) {
		fg.expectBool(t, a, 1, "std.runtime.Likely")
	},
	"std.runtime.Unlikely": func(fg *fnGen, t *mir.Terminator, a []value) {
		fg.expectBool(t, a, 0, "std.runtime.Unlikely")
	},
	"std.atomic.Pause":   (*fnGen).pause,
	"std.raw.FnFromAddr": (*fnGen).fnFromAddr,
	"std.raw.AtomicCopy": (*fnGen).atomicCopy,
	"std.raw.AtomicFill": (*fnGen).atomicFill,
}

func init() {
	for key, sig := range extraSigs {
		registerTarget(key, sig, extraLowerings[key])
	}
}

// retScalar stores v, a scalar of Dest's type, and continues.
func (fg *fnGen) retScalar(t *mir.Terminator, v llvm.Value) {
	destT := fg.b.PlaceType(t.Dest)
	if fg.g.reprOf(destT).kind != rZero {
		d, _ := fg.placeAddr(t.Dest)
		fg.store(d, value{t: destT, kind: rScalar, v: v})
	}
	fg.targetDone(t)
}

// retFields stores Dest's record fields by name and continues.
func (fg *fnGen) retFields(t *mir.Terminator, key string, vals map[string]llvm.Value) {
	g := fg.g
	destT := fg.b.PlaceType(t.Dest)
	d, _ := fg.placeAddr(t.Dest)
	fs := g.layout(destT).Fields
	for i, fd := range g.tt.Get(destT).Fields {
		v, ok := vals[g.tt.Interner().Str(fd.Name)]
		if !ok || i >= len(fs) {
			source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s returns %s, its lowering writes other fields", key, g.tt.TypeString(destT, nil))
		}
		fg.store(addr{v: fg.gep(d.v, fs[i].Offset), align: alignAt(d.align, fs[i].Offset)}, value{t: fd.Type, kind: rScalar, v: v})
	}
	fg.targetDone(t)
}

// scalarArg is argument i as a scalar, or an internal error naming key.
func (fg *fnGen) scalarArg(t *mir.Terminator, key string, args []value, i int) value {
	if i >= len(args) || args[i].kind != rScalar {
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s argument %d is no scalar", key, i)
	}
	return args[i]
}

// overflowFlag is AddOverflow, SubOverflow or MulOverflow over T.
func (fg *fnGen) overflowFlag(t *mir.Terminator, args []value, op string) {
	key := "std.math." + map[string]string{"add": "Add", "sub": "Sub", "mul": "Mul"}[op] + "Overflow"
	a, b := fg.scalarArg(t, key, args, 0), fg.scalarArg(t, key, args, 1)
	r := fg.g.reprOf(a.t)
	pre := "u"
	if r.signed {
		pre = "s"
	}
	f := fg.g.m.IntrinsicFn(pre+op+".with.overflow", r.ssa)
	res := fg.cur.CallFunc(f, []llvm.Value{a.v, b.v})
	fg.retFields(t, key, map[string]llvm.Value{"value": fg.cur.ExtractValue(res, 0), "overflow": fg.cur.ExtractValue(res, 1)})
}

// wrap32 is ECMA-262's ToInt32 or ToUint32 of an f64: both are the same
// 32 bits.
func (fg *fnGen) wrap32(t *mir.Terminator, args []value, key string) {
	m := fg.g.m
	i32, i64 := m.I(32), m.I(64)
	x := fg.loadArg(key, t, args, 0, m.Double())
	c := func(v uint64) llvm.Value { return m.ConstInt(i64, v) }
	bits := fg.cur.Cast(llvm.BitCast, x, i64)
	// Slow path: the integer part's low 64 bits from the mantissa.
	exp := fg.cur.Bin(llvm.And, fg.cur.Bin(llvm.LShr, bits, c(52), 0), c(0x7ff), 0)
	mant := fg.cur.Bin(llvm.Or, fg.cur.Bin(llvm.And, bits, c(1<<52-1), 0), c(1<<52), 0)
	sh := fg.cur.Bin(llvm.Sub, exp, c(1075), 0) // left shift when >= 0
	left := fg.cur.ICmp(llvm.ISGE, sh, c(0))
	lsh := fg.cur.Select(fg.cur.ICmp(llvm.ISLT, sh, c(64)), sh, c(0))
	shl := fg.cur.Select(fg.cur.ICmp(llvm.ISLT, sh, c(64)), fg.cur.Bin(llvm.Shl, mant, lsh, 0), c(0))
	rsh := fg.cur.Bin(llvm.Sub, c(0), sh, 0)
	rshC := fg.cur.Select(fg.cur.ICmp(llvm.IULT, rsh, c(64)), rsh, c(63))
	shr := fg.cur.Select(fg.cur.ICmp(llvm.IULT, rsh, c(64)), fg.cur.Bin(llvm.LShr, mant, rshC, 0), c(0))
	v := fg.cur.Cast(llvm.Trunc, fg.cur.Select(left, shl, shr), i32)
	neg := fg.cur.ICmp(llvm.ISLT, bits, c(0))
	v = fg.cur.Select(neg, fg.cur.Bin(llvm.Sub, m.ConstInt(i32, 0), v, 0), v)
	none := fg.cur.Bin(llvm.Or, fg.cur.ICmp(llvm.IEQ, exp, c(0x7ff)), fg.cur.ICmp(llvm.IULT, exp, c(1023)), 0)
	slow := fg.cur.Select(none, m.ConstInt(i32, 0), v)
	// Fast path: |x| < 2^31 converts directly (the saturating form, so an
	// unchosen out-of-range value is no poison).
	fast := fg.cur.CallFunc(m.IntrinsicFn("fptosi.sat", i32, m.Double()), []llvm.Value{x})
	fits := fg.cur.FCmp(llvm.FOLT, fg.cur.CallFunc(m.IntrinsicFn("fabs", m.Double()), []llvm.Value{x}), m.ConstF64(0x41e0000000000000))
	fg.retScalar(t, fg.cur.Select(fits, fast, slow))
}

// castSatOp is CastSat or CastSatF32 to T.
func (fg *fnGen) castSatOp(t *mir.Terminator, args []value, key string) {
	x := fg.scalarArg(t, key, args, 0)
	r := fg.g.reprOf(fg.b.PlaceType(t.Dest))
	name := "fptoui.sat"
	if r.signed {
		name = "fptosi.sat"
	}
	fg.retScalar(t, fg.cur.CallFunc(fg.g.m.IntrinsicFn(name, r.ssa, x.v.Ty), []llvm.Value{x.v}))
}

// mulWide is MulWide through i128.
func (fg *fnGen) mulWide(t *mir.Terminator, args []value) {
	m := fg.g.m
	i64, i128 := m.I(64), m.I(128)
	a, b := fg.loadArg("std.math.MulWide", t, args, 0, i64), fg.loadArg("std.math.MulWide", t, args, 1, i64)
	p := fg.cur.Bin(llvm.Mul, fg.cur.Cast(llvm.ZExt, a, i128), fg.cur.Cast(llvm.ZExt, b, i128), 0)
	fg.retFields(t, "std.math.MulWide", map[string]llvm.Value{
		"hi": fg.cur.Cast(llvm.Trunc, fg.cur.Bin(llvm.LShr, p, m.ConstInt(i128, 64), 0), i64),
		"lo": fg.cur.Cast(llvm.Trunc, p, i64),
	})
}

// divWide is DivWide: divide by zero when d is 0, overflow when hi >= d,
// then the 128-by-64 division.
func (fg *fnGen) divWide(t *mir.Terminator, args []value) {
	const key = "std.math.DivWide"
	m := fg.g.m
	i64 := m.I(64)
	hi, lo, d := fg.loadArg(key, t, args, 0, i64), fg.loadArg(key, t, args, 1, i64), fg.loadArg(key, t, args, 2, i64)
	fg.trap(fg.cur.ICmp(llvm.IEQ, d, m.ConstInt(i64, 0)), types.PanicDivideByZero)
	fg.trap(fg.cur.ICmp(llvm.IUGE, hi, d), types.PanicOverflow)
	var q, r llvm.Value
	if fg.archOf() == types.ArchX64 {
		res := fg.cur.InlineAsm(m.FuncType(m.Struct(false, i64, i64), []llvm.Type{i64, i64, i64}, false),
			"divq $4", "={rax},={rdx},{rax},{rdx},r,~{dirflag},~{fpsr},~{flags}", false, []llvm.Value{lo, hi, d})
		q, r = fg.cur.ExtractValue(res, 0), fg.cur.ExtractValue(res, 1)
	} else {
		// Restoring division, one quotient bit per step: rem < d holds
		// throughout (hi < d), so rem*2 + bit fits 65 bits, its top bit
		// carried apart.
		rem, quo := hi, m.ConstInt(i64, 0)
		one := m.ConstInt(i64, 1)
		for i := 63; i >= 0; i-- {
			top := fg.cur.ICmp(llvm.ISLT, rem, m.ConstInt(i64, 0))
			bit := fg.cur.Bin(llvm.And, fg.cur.Bin(llvm.LShr, lo, m.ConstInt(i64, uint64(i)), 0), one, 0)
			rem = fg.cur.Bin(llvm.Or, fg.cur.Bin(llvm.Shl, rem, one, 0), bit, 0)
			ge := fg.cur.Bin(llvm.Or, top, fg.cur.ICmp(llvm.IUGE, rem, d), 0)
			rem = fg.cur.Select(ge, fg.cur.Bin(llvm.Sub, rem, d, 0), rem)
			quo = fg.cur.Bin(llvm.Or, fg.cur.Bin(llvm.Shl, quo, one, 0), fg.cur.Cast(llvm.ZExt, ge, i64), 0)
		}
		q, r = quo, rem
	}
	fg.retFields(t, key, map[string]llvm.Value{"q": q, "r": r})
}

// offsetOf is OffsetOf[T]("field"): the layout's offset, a constant.
func (fg *fnGen) offsetOf(t *mir.Terminator, _ []value) {
	const key = "std.mem.OffsetOf"
	g := fg.g
	if t.Callee.Fn.Args.Len != 1 {
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s without its type argument", key)
	}
	ty := fg.b.GenericArgs[t.Callee.Fn.Args.Start].Type
	name := fg.constStrArg(t, key, 0)
	var names []string
	switch tt := g.tt.Get(ty); tt.Kind {
	case types.KRecord:
		for _, f := range tt.Fields {
			names = append(names, g.tt.Interner().Str(f.Name))
		}
	case types.KNamed:
		if sd, ok := g.items.Get(tt.Item).Decl.(*types.StructDecl); ok {
			for _, f := range sd.Fields {
				names = append(names, g.tt.Interner().Str(f.Name))
			}
		}
	}
	fs := g.layout(ty).Fields
	for i, n := range names {
		if n == name && i < len(fs) {
			fg.retScalar(t, g.m.ConstInt(g.m.I(64), fs[i].Offset))
			return
		}
	}
	// check refuses a field a closed T lacks (E0901); a generic instance
	// whose T lacks it is a known limit, reported as an ICE.
	source.ICEf(fg.spanOf(t.Span), "codegen: %s: %s has no field %q", key, g.tt.TypeString(ty, nil), name)
}

// constStrArg is the str const argument i of call t.
func (fg *fnGen) constStrArg(t *mir.Terminator, key string, i int) string {
	op := fg.b.Args[t.Args.Start+uint32(i)].Operand
	if op.Kind != mir.OpConst {
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s argument %d is no constant str", key, i)
	}
	cs := fg.g.env.Consts()
	cv := cs.Get(op.Const)
	if len(cv.Bytes) < 16 {
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s argument %d is no str", key, i)
	}
	n := types.LoadUint(cv.Bytes[8:], 8)
	if n == 0 || len(cv.Relocs) == 0 {
		return ""
	}
	data := cs.Get(cv.Relocs[0].Data).Bytes
	if uint64(len(data)) < n {
		source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s argument %d: str of %d bytes over %d", key, i, n, len(data))
	}
	return string(data[:n])
}

// prefetch is llvm.prefetch of p for a read, or a write when write is
// true: an immediate, so a write flag that is no constant picks between
// the two calls.
func (fg *fnGen) prefetch(t *mir.Terminator, args []value) {
	const key = "std.mem.Prefetch"
	m := fg.g.m
	i32 := m.I(32)
	p, w := fg.scalarArg(t, key, args, 0), fg.scalarArg(t, key, args, 1)
	f := m.IntrinsicFn("prefetch", m.Ptr())
	emit := func(rw uint64) {
		fg.cur.CallFunc(f, []llvm.Value{p.v, m.ConstInt(i32, rw), m.ConstInt(i32, 3), m.ConstInt(i32, 1)})
	}
	if op := fg.b.Args[t.Args.Start+1].Operand; op.Kind == mir.OpImm || op.Kind == mir.OpConst {
		emit(fg.constArgBits(t, key, 1) & 1)
		fg.targetDone(t)
		return
	}
	wb, rb := fg.newBlock(), fg.newBlock()
	fg.cur.CondBr(w.v, wb, rb)
	fg.cur = wb
	emit(1)
	fg.targetDone(t)
	fg.cur = rb
	emit(0)
	fg.targetDone(t)
}

// cycles is llvm.readcyclecounter.
func (fg *fnGen) cycles(t *mir.Terminator, _ []value) {
	fg.retScalar(t, fg.cur.CallFunc(fg.g.m.IntrinsicFn("readcyclecounter"), nil))
}

// expectBool is llvm.expect.i1(cond, expected).
func (fg *fnGen) expectBool(t *mir.Terminator, args []value, expected uint64, key string) {
	cond := fg.scalarArg(t, key, args, 0)
	m := fg.g.m
	i1 := m.I(1)
	fg.retScalar(t, fg.cur.CallFunc(m.IntrinsicFn("expect", i1), []llvm.Value{cond.v, m.ConstInt(i1, expected)}))
}

// pause is the spin-wait hint.
func (fg *fnGen) pause(t *mir.Terminator, _ []value) {
	asm := "pause"
	if fg.archOf() == types.ArchArm64 {
		asm = "yield"
	}
	m := fg.g.m
	fg.cur.InlineAsm(m.FuncType(m.Void(), nil, false), asm, "~{memory}", true, nil)
	fg.targetDone(t)
}

// fnFromAddr is the address as the fn type F.
func (fg *fnGen) fnFromAddr(t *mir.Terminator, args []value) {
	a := fg.scalarArg(t, "std.raw.FnFromAddr", args, 0)
	r := fg.g.reprOf(fg.b.PlaceType(t.Dest))
	if r.kind != rScalar || !r.ssa.IsPtr() {
		source.ICEf(fg.spanOf(t.Span), "codegen: std.raw.FnFromAddr to %s", fg.g.tt.TypeString(fg.b.PlaceType(t.Dest), nil))
	}
	fg.retScalar(t, fg.cur.Cast(llvm.IntToPtr, a.v, r.ssa))
}

// elemInt is the integer type of an atomic element of size bytes.
func (fg *fnGen) elemInt(t *mir.Terminator, key string, size uint64) llvm.Type {
	switch size {
	case 1, 2, 4, 8:
		return fg.g.m.I(int(size * 8))
	}
	source.ICEf(fg.spanOf(t.Span), "codegen: intrinsic %s of an element of %d bytes", key, size)
	return llvm.Type{}
}

// elemLoop emits a loop over the n elements of size bytes, forward or
// backward by back (i1), calling body with the byte offset of each; it
// continues at t's Target afterwards.
func (fg *fnGen) elemLoop(t *mir.Terminator, n llvm.Value, size uint64, back llvm.Value, body func(off llvm.Value)) {
	m := fg.g.m
	i64 := m.I(64)
	slot := fg.entryAlloca(i64, 8)
	fg.cur.Store(m.ConstInt(i64, 0), slot, 8)
	head, step, exit := fg.newBlock(), fg.newBlock(), fg.newBlock()
	fg.cur.Br(head)
	fg.cur = head
	i := fg.cur.Load(i64, slot, 8)
	fg.cur.CondBr(fg.cur.ICmp(llvm.ISLT, i, n), step, exit)
	fg.cur = step
	idx := i
	if back != (llvm.Value{}) {
		idx = fg.cur.Select(back, fg.cur.Bin(llvm.Sub, fg.cur.Bin(llvm.Sub, n, i, 0), m.ConstInt(i64, 1), 0), i)
	}
	body(fg.cur.Bin(llvm.Mul, idx, m.ConstInt(i64, size), 0))
	fg.cur.Store(fg.cur.Bin(llvm.Add, i, m.ConstInt(i64, 1), 0), slot, 8)
	fg.cur.Br(head)
	fg.cur = exit
	fg.targetDone(t)
}

// atomicCopy copies n elements, each a relaxed atomic load and store,
// backward when dst is above src (memmove's overlap rule).
func (fg *fnGen) atomicCopy(t *mir.Terminator, args []value) {
	const key = "std.raw.AtomicCopy"
	g := fg.g
	dst, src, n := fg.scalarArg(t, key, args, 0), fg.scalarArg(t, key, args, 1), fg.scalarArg(t, key, args, 2)
	elem := g.tt.Get(dst.t).Elem
	size, _ := g.sizeAlign(elem)
	it := fg.elemInt(t, key, size)
	back := fg.cur.ICmp(llvm.IUGT, fg.cur.Cast(llvm.PtrToInt, dst.v, g.m.I(64)), fg.cur.Cast(llvm.PtrToInt, src.v, g.m.I(64)))
	fg.elemLoop(t, n.v, size, back, func(off llvm.Value) {
		x := fg.cur.AtomicLoad(it, fg.cur.GEPBytes(src.v, off, false), llvm.Monotonic, uint32(size))
		fg.cur.AtomicStore(x, fg.cur.GEPBytes(dst.v, off, false), llvm.Monotonic, uint32(size))
	})
}

// atomicFill writes x to n elements, each a relaxed atomic store of
// x's bits.
func (fg *fnGen) atomicFill(t *mir.Terminator, args []value) {
	const key = "std.raw.AtomicFill"
	g := fg.g
	dst, n := fg.scalarArg(t, key, args, 0), fg.scalarArg(t, key, args, 2)
	elem := g.tt.Get(dst.t).Elem
	size, _ := g.sizeAlign(elem)
	it := fg.elemInt(t, key, size)
	// x's bits as the element's integer: x is stored to a slot of its own
	// type, then read back whole, whatever its kind (an integer, a float, a
	// pointer, a bool or a small plain aggregate).
	tmp := fg.temp(elem)
	fg.store(tmp, args[1])
	bitsV := fg.cur.Load(it, tmp.v, max(tmp.align, uint32(1)))
	fg.elemLoop(t, n.v, size, llvm.Value{}, func(off llvm.Value) {
		fg.cur.AtomicStore(bitsV, fg.cur.GEPBytes(dst.v, off, false), llvm.Monotonic, uint32(size))
	})
}
