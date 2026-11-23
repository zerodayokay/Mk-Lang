// Why: Mk's guaranteed tail calls must never grow the stack, in every
// profile, so they cannot be left to LLVM's optional tail-call
// optimization.
//
// What: tailCallTerm (a TermTailCall), isCall and sameSig.
//
// How: a TermTailCall is `musttail call` followed by `ret` of its value.
// The callee's lowered signature is the caller's own (check's E0312
// compares the declared signatures; an instance keeps them equal), so a
// result returned through a hidden pointer forwards the caller's own sret
// pointer, and LLVM reuses the incoming argument area. The argument rule is
// asserted: every argument is passed in registers, or is a parameter of the
// caller passed on unchanged at its own position, whose incoming pointer is
// forwarded; anything else is an internal error, since check refused it
// (E0313). A callee with a byval parameter is called at its $tail body,
// from the caller's own (tailbody.go): LLVM 21 cannot lower a musttail
// call with a byval argument on x86-64. A lowered signature that differs from the caller's is an
// internal error too, before LLVM's verifier would refuse the musttail.

package codegen

import (
	"mk/internal/abi"
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// isCall reports whether t calls a function: a TermCall or a TermTailCall,
// whose callee and arguments read the same; a tail call's Dest is _0 and it
// has no successor.
func isCall(t *mir.Terminator) bool {
	return t.Kind == mir.TermCall || t.Kind == mir.TermTailCall
}

// tailCallTerm emits a TermTailCall.
func (fg *fnGen) tailCallTerm(t *mir.Terminator) {
	g := fg.g
	if t.Spawn != mir.NoLocal || t.HasErr {
		source.ICEf(fg.spanOf(t.Span), "codegen: a tail call with a spawn or an error edge")
	}
	argOps := fg.b.Args[t.Args.Start:t.Args.End()]
	args := make([]value, len(argOps))
	for i, a := range argOps {
		args[i] = fg.operand(a.Operand)
	}
	var callee llvm.Value
	var fnTy llvm.Type
	var ls *lsig
	attrs := llvm.Attrs{}.Flag("nounwind")
	switch t.Callee.Kind {
	case mir.CallDirect:
		fi := fg.callIndex(t)
		f := &g.u.Funcs[fi]
		if fd, ok := g.items.Get(f.Item).Decl.(*types.FuncDecl); ok && (fd.Intrinsic || fd.Sig.Extern && f.Body == nil) {
			source.ICEf(fg.spanOf(t.Span), "codegen: a tail call of %s, an intrinsic or extern (check's E0314)", g.items.Get(f.Item).Key)
		}
		fn := g.funcDecl(fi)
		ls = g.callSig(fi, args)
		if fg.pnone {
			// A preserve_nonecc body must tail-call another preserve_nonecc
			// body (pnone.go); check before LLVM's verifier would reject it.
			if !g.pnoneBody(fi) {
				source.ICEf(fg.spanOf(t.Span), "codegen: preserve_nonecc tail call to non-preserve_nonecc %s", f.Symbol)
			}
			fn = g.pnoneDecl(fi)
		} else if g.tailBody(fi) {
			// A byval parameter: body to body (tailbody.go).
			fn, ls = g.tailDecl(fi), g.tailSigOf(fi)
		}
		callee, fnTy, attrs = fn.Ref(), fn.Type(), callAttrs(fn)
	case mir.CallValue:
		fv := fg.operand(t.Callee.Value)
		ft := g.tt.Get(fv.t)
		if ft.Kind != types.KFn {
			source.ICEf(fg.spanOf(t.Span), "codegen: a tail call through %s", g.tt.TypeString(fv.t, nil))
		}
		var params []types.TypeID
		for _, p := range ft.Fn.Params {
			params = append(params, p.Type)
		}
		ls = g.lowerSig(params, len(params), false, g.tt.ReturnType(ft.Fn.Result, ft.Fn.Errors))
		callee, fnTy = fv.v, g.m.FuncType(ls.ret, ls.ltypes, false)
		for i, sl := range ls.s.Params {
			if sl.Kind == abi.Indirect && sl.Byval {
				source.ICEf(fg.spanOf(t.Span), "codegen: a tail call through a fn value passes argument %d byval (check's E0313)", i+1)
			}
		}
	default:
		source.ICEf(fg.spanOf(t.Span), "codegen: a tail call of callee kind %d", t.Callee.Kind)
	}
	fg.checkArgs(ls, args)
	fg.sameSig(t, ls)
	var largs []llvm.Value
	if ls.sret {
		largs = append(largs, fg.fn.Param(0)) // the caller's own result memory
	}
	for i, v := range args {
		sl := ls.s.Params[i]
		switch sl.Kind {
		case abi.Ignore:
		case abi.Indirect:
			// Rule 4: memory this frame made would be gone; only a
			// parameter passed on at its own position, not var (the body
			// may have written it), has memory the caller's caller owns,
			// its incoming pointer.
			o := argOps[i].Operand
			if o.Kind != mir.OpCopy && o.Kind != mir.OpMove || o.Place.Proj.Len != 0 || int(o.Place.Local) != i+1 ||
				fg.b.Locals[i+1].Var || fg.sig.s.Params[i].Kind != abi.Indirect {
				source.ICEf(fg.spanOf(t.Span), "codegen: tail call argument %d travels in memory and is not the caller's parameter %d (check's E0313)", i+1, i+1)
			}
			largs = append(largs, fg.fn.Param(fg.sig.first[i]))
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
	cc := llvm.CCC
	if fg.pnone {
		cc = llvm.PreserveNoneCC
	}
	res := fg.cur.MustTailCallCC(cc, fnTy, callee, largs, ls.lattrs, ls.rattrs, attrs)
	if ls.ret.IsVoid() {
		fg.cur.Ret(nil)
		return
	}
	fg.cur.Ret(&res)
}

// sameSig asserts that the callee's lowered signature is the caller's: the
// LLVM return type, the sret slot, and each parameter's LLVM types and ABI
// slot. check's E0312 makes the declared signatures equal and instantiation
// keeps them so; a difference here is a compiler defect, reported
// before LLVM's verifier refuses the musttail.
func (fg *fnGen) sameSig(t *mir.Terminator, ls *lsig) {
	own := fg.sig
	ok := ls.ret == own.ret && ls.sret == own.sret && len(ls.ltypes) == len(own.ltypes) &&
		len(ls.s.Params) == len(own.s.Params) && ls.variadic == own.variadic
	for i := 0; ok && i < len(ls.ltypes); i++ {
		ok = ls.ltypes[i] == own.ltypes[i]
	}
	for i := 0; ok && i < len(ls.s.Params); i++ {
		a, b := ls.s.Params[i], own.s.Params[i]
		ok = a.Kind == b.Kind && a.Byval == b.Byval && a.Size == b.Size && ls.array[i] == own.array[i]
	}
	if !ok {
		source.ICEf(fg.spanOf(t.Span), "codegen: a tail call whose callee's lowered signature differs from the caller's (check's E0312)")
	}
}
