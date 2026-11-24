// Why: every Mk call follows the C ABI, so an argument's IR form depends on
// how internal/abi classifies its type, and a fallible call's result needs
// a branch on its error code; this file turns a TermCall into that IR.
//
// What: callTerm (a TermCall), callLowered (the call itself under an lsig),
// calleeArgs, checkArgs, finishCall (the ok and error edges) and copyErr.
//
// How: a Direct argument's parts are loaded from it; an Indirect argument
// is a fresh copy per call, never the caller's own storage; byval passes an
// address; sret writes into the destination. A fallible call returns E!T
// into a temporary and branches on the code to Target or Err.Block.

package codegen

import (
	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// callLowered calls callee of LLVM type fnTy under the lowered signature ls
// with Mk argument values, storing the result into dest (nil discards it).
func (fg *fnGen) callLowered(callee llvm.Value, fnTy llvm.Type, ls *lsig, args []value, dest *addr, fnAttrs llvm.Attrs) {
	g := fg.g
	var largs []llvm.Value
	var sretDst addr
	if ls.sret {
		if dest != nil {
			sretDst = *dest
		} else {
			sretDst = fg.temp(ls.result)
		}
		largs = append(largs, sretDst.v)
	}
	if len(args) != len(ls.s.Params) {
		source.ICEf(fg.spanOf(fg.span), "codegen: call with %d arguments for %d parameters", len(args), len(ls.s.Params))
	}
	for i, v := range args {
		sl := ls.s.Params[i]
		switch sl.Kind {
		case abi.Ignore:
		case abi.Indirect:
			if sl.Byval {
				largs = append(largs, fg.addrOf(v).v)
				continue
			}
			// A fresh copy per call, never the caller's own storage.
			tmp := fg.tempBytes(max(sl.Size, 1), sl.Align)
			if v.kind == rAgg && g.tt.Get(v.t).IsSliceLike() {
				// A []T, mut []T or str is copied by an llvm.memcpy of
				// its two words, not storeSlice's two stores, because
				// MemCpyOpt's processImmutArgument matches only a memcpy
				// that fills the whole argument alloca. Where the callee's
				// parameter is readonly and captures(none) (immutarg.go)
				// and LLVM proves the source unchanged until
				// the call returns, it then passes the source instead of
				// the copy; elsewhere the copy stays.
				size, _ := g.sizeAlign(v.t)
				fg.memcpyAligned(tmp, v.a, size)
			} else {
				fg.store(tmp, v)
			}
			largs = append(largs, tmp.v)
		case abi.Direct:
			r := g.reprOf(v.t)
			if r.kind == rScalar && len(sl.Parts) == 1 && !ls.array[i] {
				largs = append(largs, v.v)
				continue
			}
			a := fg.addrOf(v)
			if pb := partBytes(sl); pb > r.size {
				tmp := fg.tempBytes(pb, r.align)
				fg.store(tmp, v)
				a = tmp
			}
			largs = append(largs, fg.loadParts(a, ls.params[i], sl, ls.array[i], false)...)
		}
	}
	res := fg.cur.Call(fnTy, callee, largs, ls.lattrs, ls.rattrs, fnAttrs)
	if ls.s.Ret.Kind != abi.Direct || dest == nil {
		return
	}
	var parts []llvm.Value
	n := len(g.partTypes(ls.result, ls.s.Ret, true))
	switch {
	case ls.retArray || n == 1:
		parts = []llvm.Value{res}
	default:
		for k := 0; k < n; k++ {
			parts = append(parts, fg.cur.ExtractValue(res, k))
		}
	}
	r := g.reprOf(ls.result)
	if pb := partBytes(ls.s.Ret); pb > r.size && r.kind != rScalar {
		tmp := fg.tempBytes(pb, r.align)
		fg.storeParts(tmp, ls.result, ls.s.Ret, ls.retArray, parts)
		fg.memcpy(*dest, tmp, r.size)
		return
	}
	fg.storeParts(*dest, ls.result, ls.s.Ret, ls.retArray, parts)
}

// addrOf is the address of a value, spilling a scalar to a temporary.
func (fg *fnGen) addrOf(v value) addr {
	if v.kind == rAgg {
		return v.a
	}
	tmp := fg.temp(v.t)
	fg.store(tmp, v)
	return tmp
}

// callTerm emits a TermCall.
func (fg *fnGen) callTerm(t *mir.Terminator) {
	g := fg.g
	if t.Spawn != mir.NoLocal {
		fg.spawn(t)
		return
	}
	var args []value
	for _, a := range fg.b.Args[t.Args.Start:t.Args.End()] {
		args = append(args, fg.operand(a.Operand))
	}
	destT := fg.b.PlaceType(t.Dest)
	switch t.Callee.Kind {
	case mir.CallDirect:
		// An intrinsic is recognized by its item, so a generic one
		// (Io.ScopeOf[E], raw.Read[T]) needs no Funcs entry per instance;
		// the support-pack bodies are called by symbol.
		if fd, ok := g.items.Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl); ok && fd.Intrinsic &&
			!supportBodies[g.items.Get(t.Callee.Fn.Item).Key.String()] {
			fg.intrinsicCall(t.Callee.Fn.Item, t, args)
			return
		}
		if fg.hwMathCall(t, args) { // the instruction under its feature
			return
		}
		fi := fg.callIndex(t) // the variant for this function's features
		if f := &g.u.Funcs[fi]; f.Body == nil {
			if fd, ok := g.items.Get(f.Item).Decl.(*types.FuncDecl); ok && fd.Intrinsic {
				if !g.cfg.PGOGen && g.items.Get(f.Item).Key == keyProfileTable {
					fg.emptyProfileTable(t) // the table is empty outside --pgo-gen (deadentry.go)
					return
				}
				fg.supportCall(fi, t, args)
				return
			}
		}
		fn := g.funcDecl(fi)
		ls := g.callSig(fi, args)
		fg.checkArgs(ls, args)
		if g.externOutsideStd(&g.u.Funcs[fi]) {
			if ss, ok := g.systemStackItem(); ok {
				fg.systemStackCall(t, fi, ls, args, destT, ss)
				return
			}
		}
		if fg.trapCallable(fi, ls) { // trapform.go: a call of a never function with no parameter
			fg.trapCall(fn)
			return
		}
		fg.finishCall(t, fn.Ref(), fn.Type(), ls, args, destT, fg.coldCallAttrs(fg.soleCallAttr(t, fi, callAttrs(fn))))
	case mir.CallValue:
		fv := fg.operand(t.Callee.Value)
		ft := g.tt.Get(fv.t)
		if ft.Kind != types.KFn && !(ft.Kind == types.KOptional) {
			source.ICEf(fg.spanOf(fg.span), "codegen: call through %s", g.tt.TypeString(fv.t, nil))
		}
		var params []types.TypeID
		for _, p := range ft.Fn.Params {
			params = append(params, p.Type)
		}
		ls := g.lowerSig(params, len(params), false, g.tt.ReturnType(ft.Fn.Result, ft.Fn.Errors))
		fnTy := g.m.FuncType(ls.ret, ls.ltypes, false)
		fg.finishCall(t, fv.v, fnTy, ls, args, destT, fg.coldCallAttrs(llvm.Attrs{}.Flag("nounwind")))
	case mir.CallBuiltin:
		if t.Callee.Builtin == mir.BuiltinEvalArena {
			msg := fg.strValue("EvalArena outside compile-time evaluation")
			fg.panicCall(types.PanicExplicit, &msg)
			return
		}
		source.ICEf(fg.spanOf(fg.span), "codegen: builtin %d at run time", t.Callee.Builtin)
	default:
		source.ICEf(fg.spanOf(fg.span), "codegen: callee kind %d in an instance body", t.Callee.Kind)
	}
}

// calleeArgs are the arguments mono keyed the callee with: the FnRef's
// generic arguments, then one CAValue per const value parameter.
func (fg *fnGen) calleeArgs(t *mir.Terminator) []types.GenericArg {
	g := fg.g
	f := t.Callee.Fn
	args := append([]types.GenericArg(nil), fg.b.GenericArgs[f.Args.Start:f.Args.End()]...)
	sig := g.declSig(f.Item)
	cargs := fg.b.Args[t.Args.Start:t.Args.End()]
	for pi, p := range sig.Params {
		if !p.Const {
			continue
		}
		o := cargs[pi].Operand
		var c types.ConstID
		switch o.Kind {
		case mir.OpConst:
			c = o.Const
		case mir.OpImm:
			size, align := g.immSizeAlign(o.Type)
			b := make([]byte, size)
			types.StoreUint(b, int(size), o.Bits)
			c = g.env.Consts().Intern(types.ConstValue{Type: o.Type, Align: align, Bytes: b})
		default:
			source.ICEf(fg.spanOf(fg.span), "codegen: const parameter %d of %s is not a constant (the driver's Unit.Lookup over mono resolves this form, M-3)",
				pi+1, g.items.Get(f.Item).Key)
		}
		args = append(args, types.ConstGenericArg(types.ConstArg{Kind: types.CAValue, Value: c}))
	}
	return args
}

// immSizeAlign is the size and alignment of an immediate's type (mir's
// OpImm: an integer, bool or char), as reprOf gives them, read without
// the LLVM module: calleeArgs also runs under PackFacts (packfacts.go,
// through smallfn.go's smallCalls and callIndex), whose packGen has none,
// and reprOf there dereferenced the nil module. That panic stopped the
// unit's small-function set inside its sync.Once, so a build with a cache
// lost every alwaysinline mark a --no-cache build kept
// (TestConstImmArgPackKeyed). Any other type reads its layout, as
// computeRepr does for a named type (a tag-only enum's tag fills it).
func (g *packGen) immSizeAlign(t types.TypeID) (uint64, uint32) {
	ty := g.tt.Get(t)
	switch ty.Kind {
	case types.KBool:
		return 1, 1
	case types.KChar:
		return 4, 4
	case types.KInt:
		return uint64(ty.Bits) / 8, uint32(ty.Bits) / 8
	case types.KIntPtr:
		return 8, 8
	}
	l := g.layout(t)
	return l.Shape.Size, max(l.Shape.Align, 1)
}

// checkArgs raises an ICE when an argument's ABI differs from the callee
// parameter's: a type mismatch lower or mono let through would otherwise
// miscompile silently (see lower's note on receivers).
func (fg *fnGen) checkArgs(ls *lsig, args []value) {
	g := fg.g
	for i, v := range args {
		if i >= len(ls.params) {
			return
		}
		as, aa := g.sizeAlign(v.t)
		ps, pa := g.sizeAlign(ls.params[i])
		if as != ps || aa != pa || g.reprOf(v.t).kind != g.reprOf(ls.params[i]).kind {
			source.ICEf(fg.spanOf(fg.span), "codegen: argument %d is %s, the parameter %s", i+1,
				g.tt.TypeString(v.t, nil), g.tt.TypeString(ls.params[i], nil))
		}
	}
}

func (fg *fnGen) coldCallAttrs(attrs llvm.Attrs) llvm.Attrs {
	if !fg.coldBlock[mir.BlockID(fg.bi)] {
		return attrs
	}
	attrs = attrs.Without("alwaysinline")
	if !attrs.Has("cold") {
		attrs = attrs.Flag("cold")
	}
	return attrs
}

// finishCall makes the call and continues at the target. A fallible call
// returns E!T into a temporary and branches on the code: ok copies the
// payload into Dest and goes to Target, an error copies the code and the
// error payload into Err.Error and goes to Err.Block. A never call
// is followed by unreachable.
func (fg *fnGen) finishCall(t *mir.Terminator, callee llvm.Value, fnTy llvm.Type, ls *lsig, args []value, destT types.TypeID, attrs llvm.Attrs) {
	g, m := fg.g, fg.g.m
	if ls.result == types.Never {
		fg.callLowered(callee, fnTy, ls, args, nil, attrs)
		fg.cur.Unreachable()
		return
	}
	if !t.HasErr {
		d, _ := fg.placeAddr(t.Dest)
		if g.reprOf(ls.result).kind == rZero {
			fg.callLowered(callee, fnTy, ls, args, nil, attrs)
		} else {
			fg.callLowered(callee, fnTy, ls, args, &d, attrs)
		}
		fg.cur.Br(fg.blocks[t.Target])
		return
	}
	tmp := fg.temp(ls.result)
	fg.callLowered(callee, fnTy, ls, args, &tmp, attrs)
	l := g.layout(ls.result)
	code := fg.cur.Load(m.I(32), fg.gep(tmp.v, l.Tag.Offset), alignAt(tmp.align, l.Tag.Offset), m.Metadata().NoUndef())
	okB, errB := fg.newBlock(), fg.newBlock()
	// The error split's successors are Target (ok), then Err.Block.
	fg.cur.CondBr(fg.cur.ICmp(llvm.IEQ, code, m.ConstInt(m.I(32), 0)), okB, errB, fg.weights(fg.edgeCounts(2))...)
	fg.cur = okB
	fg.bumpEdge(0)
	if g.reprOf(destT).kind != rZero {
		d, _ := fg.placeAddr(t.Dest)
		ok := g.variant(ls.result, 0)
		if len(ok.Fields) == 1 {
			off := ok.Fields[0].Offset
			fg.store(d, fg.loadAt(addr{v: fg.gep(tmp.v, off), align: alignAt(tmp.align, off)}, destT))
		}
	}
	fg.cur.Br(fg.blocks[t.Target])
	fg.cur = errB
	fg.bumpEdge(1)
	ea := fg.locals[t.Err.Error]
	et := fg.b.Locals[t.Err.Error].Type
	fg.copyErr(ea, et, tmp, ls.result)
	fg.cur.Br(fg.blocks[t.Err.Block])
}

// loadAt reads a value of type t at a.
func (fg *fnGen) loadAt(a addr, t types.TypeID) value {
	r := fg.g.reprOf(t)
	v := value{t: t, kind: r.kind}
	switch r.kind {
	case rScalar:
		v.v = fg.loadScalar(a, r, false)
	case rAgg:
		v.a = a
	}
	return v
}

// copyErr copies the error side of the union at src (type u) into the
// error-set value at dst (type set): the code and the present error's
// payload, by one copy when every error sits at the same offsets in both,
// else per error after a switch on the code.
func (fg *fnGen) copyErr(dst addr, set types.TypeID, src addr, u types.TypeID) {
	fg.widenErrorSide(dst, set, value{t: u, kind: rAgg, a: src})
}
