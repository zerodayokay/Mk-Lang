// Why: dl.Present(io, f) asks whether f can be called; for a lazily bound
// extern that is a run-time question (load the DLL, find the name), for
// any other extern the answer is true when the program is built.
//
// What: presentKeys, presentCall (the lowering), presentTarget (which
// extern the call names) and presentFacts (its pack-key part).
//
// How: the checker makes the argument a direct reference to a bodiless
// extern (internal/check present.go), so the argument is an RvFnRef temp,
// or a fn const once mono folded it. A lazily bound f (Config.Lazy) calls
// std.sys's MkLazyPresent with f's descriptor, the slot __imp_f of the
// lazy-binding object (lazy.go); any other f stores true.

package codegen

import (
	"mk/internal/llvm"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// presentKeys are the Present intrinsics: std.dl's, and std.sys's own
// spelling for std.sys (which std.dl imports).
var presentKeys = map[string]bool{"std.dl.Present": true, "std.sys.LazyPresent": true}

// presentCall lowers a Present call, reporting false for any other key.
func (fg *fnGen) presentCall(key string, t *mir.Terminator) bool {
	if !presentKeys[key] {
		return false
	}
	g, m := fg.g, fg.g.m
	item, args, ok := presentTarget(fg.b, g.env.Consts(), t)
	if !ok {
		source.ICEf(fg.spanOf(t.Span), "codegen: %s's argument is not a function reference", key)
	}
	sym := g.u.Funcs[g.funcIndex(item, args)].Symbol
	var v llvm.Value
	if g.cfg.Lazy[sym] {
		d, have := g.lazyDescs[sym]
		if !have {
			if g.lazyDescs == nil {
				g.lazyDescs = map[string]*llvm.Global{}
			}
			d = m.AddGlobal(LazySlot(sym), llvm.GlobalAttrs{Linkage: llvm.External, Type: m.I(8)})
			g.lazyDescs[sym] = d
		}
		f := m.DeclareAs(SymLazyPresent, llvm.FuncSig{Linkage: llvm.External, Ret: m.I(1), Params: []llvm.Type{m.Ptr()},
			FnAttrs: llvm.Attrs{}.Flag("nounwind")})
		v = fg.cur.CallFunc(f, []llvm.Value{m.GlobalRef(d)})
	} else {
		v = m.ConstInt(m.I(1), 1)
	}
	destT := fg.b.PlaceType(t.Dest)
	if g.reprOf(destT).kind != rZero {
		d, _ := fg.placeAddr(t.Dest)
		fg.store(d, value{t: destT, kind: rScalar, v: v})
	}
	fg.cur.Br(fg.blocks[t.Target])
	return true
}

// presentTarget is the function a Present call names: its last argument,
// a fn const or a local whose one definition is an RvFnRef, a fn const or
// a copy of another such local.
func presentTarget(b *mir.Body, consts *types.ConstTable, t *mir.Terminator) (types.ItemID, []types.GenericArg, bool) {
	if t.Args.Len == 0 {
		return types.NoItem, nil, false
	}
	op := b.Args[t.Args.End()-1].Operand
	for range len(b.Locals) + 1 {
		switch op.Kind {
		case mir.OpConst:
			cv := consts.Get(op.Const)
			if cv == nil || len(cv.Relocs) != 1 || cv.Relocs[0].Kind != types.RelocFn {
				return types.NoItem, nil, false
			}
			return cv.Relocs[0].Fn.Item, cv.Relocs[0].Fn.Args, true
		case mir.OpCopy, mir.OpMove:
			if op.Place.Proj.Len != 0 {
				return types.NoItem, nil, false
			}
			rv, ok := soleDef(b, op.Place.Local)
			if !ok {
				return types.NoItem, nil, false
			}
			switch rv.Kind {
			case mir.RvFnRef:
				return rv.Fn.Item, b.GenericArgs[rv.Fn.Args.Start:rv.Fn.Args.End()], true
			case mir.RvUse:
				op = rv.A
				continue
			}
		}
		return types.NoItem, nil, false
	}
	return types.NoItem, nil, false
}

// soleDef is the rvalue of local l's one whole-local assignment in b.
func soleDef(b *mir.Body, l mir.LocalID) (*mir.Rvalue, bool) {
	var rv *mir.Rvalue
	for bi := range b.Blocks {
		for si := range b.Blocks[bi].Stmts {
			st := &b.Blocks[bi].Stmts[si]
			if st.Kind != mir.StAssign || st.Place.Local != l {
				continue
			}
			if st.Place.Proj.Len != 0 || rv != nil {
				return nil, false
			}
			rv = &st.Rvalue
		}
	}
	return rv, rv != nil
}

// presentFacts hashes, per Present call of pack p's members, the symbol
// it names and whether it binds lazily: Config.Lazy changes the pack's IR
// only there.
func presentFacts(g *packGen, u *Unit, p int, env Env, cfg Config, h *source.Hasher) {
	items := env.Items()
	h.String("present")
	for _, fi := range u.Packs[p] {
		b := u.Funcs[fi].Body
		if b == nil {
			continue
		}
		for bi := range b.Blocks {
			t := &b.Blocks[bi].Term
			if !isCall(t) || t.Callee.Kind != mir.CallDirect || !presentKeys[items.Get(t.Callee.Fn.Item).Key.String()] {
				continue
			}
			item, args, ok := presentTarget(b, env.Consts(), t)
			j := -1
			if ok {
				j, ok = g.findFunc(item, args)
			}
			if !ok {
				h.Bool(false)
				continue
			}
			h.Bool(true)
			h.String(u.Funcs[j].Symbol)
			h.Bool(cfg.Lazy[u.Funcs[j].Symbol])
		}
	}
}
