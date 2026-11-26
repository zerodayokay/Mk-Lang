// Why: the driver looks a pack up in the cache before emitting it, so the
// pack key must cover everything the pack's IR depends on, including what
// the members' content hashes do not.
//
// What: PackFacts (the extra inputs, hashed) and PackConstTypes (the types
// whose layouts the pack's consts read), with walkPackConsts, asmSymTypes
// and relocFnTypes.
//
// How: PackFacts hashes every Config field and every unit-wide read codegen
// makes for the pack, and refuses a key when the configuration reads more
// (--debug, --pgo, --pgo-gen). Any new read of the unit must be added here.

package codegen

import (
	"slices"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// PackFacts writes into h what pack p's IR reads beyond the driver's part
// of the pack key, and reports whether a key may be formed at all. The
// driver's part (internal/driver/prekey.go) is the build ID and toolchain,
// the compile job, the front end's context (target, profile, the std
// tree), every Func's symbol, key, flags and, outside std, declaration,
// every Global's declaration and value, and each member's
// iface.InstanceContentHash over layouts by identity and deep (a named
// type's layout, declaration and facts, and those of every named type it
// reaches), and the deep layout of every type PackConstTypes reports (the
// layouts the pack's consts read). PackFacts adds:
//
//   - every Config field codegen prints or branches on;
//   - each member, in pack order: its symbol, key, item key, flags, the
//     solecall bits its calls read (the caller's outside bit, and per
//     direct call whether its callee key has one call site in the unit:
//     a call site in another pack changes them), its small-function
//     and cross-pack sole-call marks and, per direct call, the callee's
//     two marks and its effect (smallfn.go: they read the
//     bodies of callees, and the cross mark the body of the caller, in
//     other packs), and its rows: the
//     position (path, line, column) of its item and of every span of its
//     body, which check.go's site rows and debug.go's line table print;
//   - per direct call, in every profile, the parameters the callee's body
//     passes on through a tail call (immutarg.go tailForwards), which
//     drop the readonly mark from the call's attributes; and the same
//     bits of every function a const or asm operand of the pack names,
//     whose declaration carries the marks (walkPackConsts);
//   - per member, whether its item ends a `become` or is the direct callee
//     of one anywhere in the unit (tailbody.go tailItems), which splits a
//     member with a byval parameter into a wrapper and a $tail body;
//   - per member in optimized non-debug non-size builds on x86_64 and
//     aarch64, whether it is split into a preserve_nonecc $pnone body and
//     which locals are bound to companion $pnone dispatch tables (pnone.go);
//   - per member and per direct call, in every profile, whether the
//     function has the internal ABI (sig.go internalABI), which reads its
//     tailItems bit and whether any pack takes its address (valueFns);
//   - the entries the pack's import hint calls (importhint.go), which come
//     from the bodies of callees in other packs and from the tables they
//     name; walkPackConsts adds the entries' declaration types and their
//     tail-forward bits;
//   - every Global row: which pack defines it, relative to p, and Local.
//
// It reports false, so the pack is emitted and keyed by its IR as before,
// when the configuration reads more than this: --debug (names, scopes and
// types of the debug info), --pgo (profile counts by key, staleness) and
// --pgo-gen (the counter table spans every pack).
//
// Rule for every change of codegen: a new read of the unit outside the
// pack's members (another Func's body, a unit-wide table such as
// soleCalls) or a new Config field must be added here or in the driver's
// part, with a test in internal/driver/prekey_test.go that changes it and
// sees a miss. The driver checks the rule at run time only under
// MK_CACHE_VERIFY, which emits every pack and compares.
func PackFacts(u *Unit, p int, env Env, cfg Config, h *source.Hasher) bool {
	if cfg.Debug || cfg.PGO != nil || cfg.PGOGen || p < 0 || p >= len(u.Packs) {
		return false
	}
	h.String("mk.codegen.packfacts.v1")
	h.String(cfg.Target.Name)
	h.String(cfg.Target.LLVMTriple)
	h.Uvarint(uint64(cfg.Profile))
	h.String(cfg.CPU)
	h.String(cfg.DataLayout)
	h.String(cfg.triple())
	h.String(cfg.RootModule)
	h.String(cfg.Main.String())
	h.Bool(cfg.GeneralRegsOnly)
	h.Bool(cfg.TestNoOverflowChecks)
	h.Bool(cfg.TestNoBoundsChecks)
	// Library and Display are hashed into every pack's key. Library
	// changes only the support pack; Display names a dependency in any
	// pack's error and panic text, so adding or renaming a dependency
	// recompiles every pack, std's included (correct, if coarse).
	h.Bool(cfg.Library)
	h.String(cfg.LibName)
	fpSizeFacts(cfg, h) // frame pointers and --profile size (fpsize.go)
	// The trapped call (trapform.go).
	trapFormFacts(u, env, cfg, h)
	ids := make([]string, 0, len(cfg.Display))
	for id := range cfg.Display {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	h.Uvarint(uint64(len(ids)))
	for _, id := range ids {
		h.String(id)
		h.String(cfg.Display[id])
	}
	items := env.Items()
	// The solecall rule runs in release only (soleCallAttr), so a dev key
	// does not move with a call count.
	var sc *soleCalls
	var sg *packGen // the small-function rule's reader (smallfn.go)
	if cfg.Profile.Optimized() {
		sc = u.soleCallsOf()
		sg = newPackGen(u, p, env, &cfg, nil)
	}
	pos := func(sp source.Span) {
		if !sp.File.IsValid() {
			h.Bool(false)
			return
		}
		h.Bool(true)
		lc := env.LineCol(source.Pos{File: sp.File, Off: sp.Start})
		h.String(env.Path(sp.File))
		h.Uvarint(uint64(lc.Line))
		h.Uvarint(uint64(lc.Col))
	}
	var dg *fnGen                          // the tail-forward reader, in every profile
	ag := newPackGen(u, p, env, &cfg, nil) // the internal-ABI reader (sig.go), in every profile
	h.Uvarint(uint64(len(u.Packs[p])))
	for _, fi := range u.Packs[p] {
		f := &u.Funcs[fi]
		it := items.Get(f.Item)
		h.String(f.Symbol)
		h.String(f.Key)
		h.String(it.Key.String())
		h.Bool(f.Body == nil)
		h.Bool(f.AddrTaken)
		h.Bool(f.Spawned)
		h.Bool(f.Local)
		h.Bool(u.tailItems()[f.Item]) // split by tailbody.go: another pack's become reaches it
		h.Bool(ag.internalABI(fi))    // its own lowering, which other packs' consts and fn values move
		h.Bool(sc != nil && sc.outside[f.Item])
		h.Bool(sg != nil && sg.smallFn(fi))
		h.Bool(sg != nil && sg.crossSole(fi))
		pos(it.Span)
		frame, named := cfg.Frames[it.Key.String()] // its row's name (functable.go)
		h.Bool(named)
		h.String(frame)
		b := f.Body
		if b == nil {
			continue
		}
		h.Uvarint(uint64(len(b.Spans)))
		for _, sp := range b.Spans {
			pos(sp)
		}
		cg := &fnGen{g: sg, f: f, fi: fi, b: b}
		for bi := range b.Blocks {
			t := &b.Blocks[bi].Term
			if isCall(t) && t.Callee.Kind == mir.CallDirect {
				// The callee's tail-forwarded parameters, which lose the
				// readonly mark on the call's and the declaration's
				// attributes in every profile (immutarg.go): a fact of
				// the callee's body, maybe in another pack.
				if fd, isFn := items.Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl); !isFn || !fd.Intrinsic {
					if dg == nil {
						dg = &fnGen{g: newPackGen(u, p, env, &cfg, nil)}
					}
					dg.f, dg.fi, dg.b = f, fi, b
					j := dg.callIndex(t)
					h.Uvarint(tailForwards(u.Funcs[j].Body))
					// Whether the callee has the internal ABI (sig.go),
					// which reads its tailItems bit: a `become` in
					// another pack's body changes it.
					h.Bool(dg.g.internalABI(j))
				}
			}
			if sc != nil && isCall(t) && t.Callee.Kind == mir.CallDirect {
				h.Bool(sc.count[soleKey(b, t)] == 1)
				// The callee's two marks and its effect, which may come
				// from another pack's body: soleEffectOf adds them
				// (smallfn.go).
				j := -1
				if fd, isFn := items.Get(t.Callee.Fn.Item).Decl.(*types.FuncDecl); !isFn || !fd.Intrinsic {
					j = cg.callIndex(t)
				}
				h.Bool(sg.smallFn(j))
				h.Bool(sg.crossSole(j))
				fx := sg.smallFx(j)
				h.Uvarint(uint64(fx.ops))
				h.Bool(fx.loops)
				h.Bool(sg.smallLoops(j))
			}
		}
	}
	// The tail-forward bits of every function a const or an asm operand of
	// the pack names: its declaration's readonly marks (immutarg.go).
	h.String("tail-forward refs")
	fg := newPackGen(u, p, env, &cfg, nil)
	presentFacts(fg, u, p, env, cfg, h) // dl.Present's lazy bits (present.go)
	// The import hint's entries (importhint.go), read from the bodies of
	// other packs' functions and of the consts they name.
	hints := fg.importHints()
	h.Uvarint(uint64(len(hints)))
	for _, e := range hints {
		h.String(u.Funcs[e].Symbol)
	}
	walkPackConsts(u, p, env, cfg, func(types.TypeID) {}, func(f types.FnRef) {
		if fi, ok := fg.findFunc(f.Item, f.Args); ok {
			h.Uvarint(tailForwards(u.Funcs[fi].Body))
		} else {
			h.Uvarint(0)
		}
	})
	// The preserve_nonecc tail-dispatch split and table bindings (pnone.go).
	pnonePackFacts(ag, p, h)
	if cfg.Cover {
		hasCmp := false
		if id, ok := items.ByKey(keyCoverCmp); ok {
			if cmpFI, ok := ag.findFunc(id, nil); ok {
				hasCmp = true
				h.Bool(true)
				h.String(u.Funcs[cmpFI].Symbol)
				h.Bool(ag.internalABI(cmpFI))
				h.Uvarint(tailForwards(u.Funcs[cmpFI].Body))
			}
		}
		if !hasCmp {
			h.Bool(false)
		}
	}
	h.Uvarint(uint64(len(u.Globals)))
	for _, gl := range u.Globals {
		h.String(items.Get(gl.Item).Key.String())
		h.Bool(gl.Pack == p)
		h.Bool(gl.Local)
		h.Bool(gl.Define)
	}
	return true
}

// PackConstTypes calls visit, once per type in a deterministic order, with
// every type whose layout pack p's IR reads through the consts it emits,
// so that the driver folds each one's deep layout into the pack's key.
// The consts are the initial values of the var
// globals and section consts the pack defines (finish) and every const a
// member's body names (an OpConst operand, an RvConstRef, an RvGlobalRef
// of a const item, whose storage the pack emits, an asm operand), with
// every const they reach through a data relocation. Per const it
// visits the const's type (constInit's byte runs and nonnullBytes walk
// its layout); per defined global its declared type (its size and
// alignment). Per function relocation (fnRef) it visits every type
// argument, and the lowered signature's types: for an inline intrinsic,
// whose wrapper the pack defines with a body that lowers over the
// arguments' layouts (raw.Offset[S] strides by S's size), the
// intrinsic's parameters and result with the arguments substituted; for
// another function, the instance's own parameter and result types, from
// which its declare line is lowered. Per asm sym operand it visits the
// same declare's signature types for a function, and an extern var's type
// (asmSymTypes). None of this is in a member's
// content hash or the unit digest, which hash a const by value and its
// types by name. A reloc codegen would refuse (an ICE) is skipped: the
// pack's codegen then fails anyway.
func PackConstTypes(u *Unit, p int, env Env, cfg Config, visit func(types.TypeID)) {
	walkPackConsts(u, p, env, cfg, visit, nil)
}

// walkPackConsts is PackConstTypes' walk; fnRef, when set, is called too
// with every function a const reloc, an RvFnRef or an asm sym operand
// names, in walk
// order (PackFacts keys their tail-forward bits).
func walkPackConsts(u *Unit, p int, env Env, cfg Config, visit func(types.TypeID), fnRef func(types.FnRef)) {
	if p < 0 || p >= len(u.Packs) {
		return
	}
	g := newPackGen(u, p, env, &cfg, nil)
	consts := env.Consts()
	seenT := map[types.TypeID]bool{}
	ty := func(t types.TypeID) {
		if t == types.NoType || seenT[t] {
			return
		}
		seenT[t] = true
		visit(t)
	}
	seenC := map[types.ConstID]bool{}
	var walk func(id types.ConstID)
	walk = func(id types.ConstID) {
		if id == types.NoConst || seenC[id] {
			return
		}
		seenC[id] = true
		cv := consts.Get(id)
		if cv == nil {
			return
		}
		ty(cv.Type)
		for _, rl := range cv.Relocs {
			switch rl.Kind {
			case types.RelocData:
				walk(rl.Data)
			case types.RelocFn:
				g.relocFnTypes(rl.Fn, ty)
				if fnRef != nil {
					fnRef(rl.Fn)
				}
			}
		}
	}
	for _, gl := range u.Globals {
		if gl.Pack != p {
			continue
		}
		switch d := g.items.Get(gl.Item).Decl.(type) {
		case *types.GlobalDecl:
			ty(d.Type)
			if d.HasValue {
				walk(d.Value)
			}
		case *types.ConstDecl:
			if d.Section != "" {
				ty(d.Type)
				if d.HasValue {
					walk(d.Value)
				}
			}
		}
	}
	op := func(o *mir.Operand) {
		if o.Kind == mir.OpConst {
			walk(o.Const)
		}
	}
	for _, fi := range u.Packs[p] {
		b := u.Funcs[fi].Body
		if b == nil {
			continue
		}
		for i := range b.Operands {
			op(&b.Operands[i])
		}
		for i := range b.Args {
			op(&b.Args[i].Operand)
		}
		for i := range b.Blocks {
			bl := &b.Blocks[i]
			for j := range bl.Stmts {
				rv := &bl.Stmts[j].Rvalue
				op(&rv.A)
				op(&rv.B)
				// A const read by value (constItem) or by address (&C:
				// globalRef emits its storage here through constGlobal).
				// A var global's own pack keys its value (the u.Globals
				// loop), so only a ConstDecl is walked.
				if rv.Kind == mir.RvFnRef && fnRef != nil {
					fnRef(types.FnRef{Item: rv.Fn.Item, Args: b.GenericArgs[rv.Fn.Args.Start:rv.Fn.Args.End()]})
				}
				if rv.Kind == mir.RvConstRef || rv.Kind == mir.RvGlobalRef {
					if d, ok := g.items.Get(rv.Item).Decl.(*types.ConstDecl); ok && d.HasValue {
						walk(d.Value)
					}
				}
			}
			t := &bl.Term
			op(&t.On)
			op(&t.Msg)
			op(&t.Callee.Value)
		}
		for i := range b.Asms {
			for k := range b.Asms[i].Operands {
				a := &b.Asms[i].Operands[k]
				op(&a.In)
				walk(a.Const)
				if a.Kind == mir.AsmSym {
					g.asmSymTypes(a.Sym, ty)
					if _, isFn := g.items.Get(a.Sym).Decl.(*types.FuncDecl); isFn && fnRef != nil {
						fnRef(types.FnRef{Item: a.Sym})
					}
				}
			}
		}
	}
	// The entries the pack's import hint declares and calls (importhint.go),
	// as a function relocation's.
	for _, e := range g.importHints() {
		f := types.FnRef{Item: g.u.Funcs[e].Item, Args: g.u.Funcs[e].Args}
		g.relocFnTypes(f, ty)
		if fnRef != nil {
			fnRef(f)
		}
	}
}

// asmSymTypes visits the types a sym operand naming item makes the pack
// read (asmSymbol): for a function, the lowered signature of its declare
// (funcDecl), as relocFnTypes does for a declared instance; for an extern
// var, its type, whose size and alignment externGlobal prints. A var
// global's declaration reads only its size, which its value's bytes in the
// unit digest carry. findFunc, not funcIndex: an ICE here would drop the
// key, and codegen then fails on the operand anyway.
func (g *packGen) asmSymTypes(item types.ItemID, ty func(types.TypeID)) {
	switch d := g.items.Get(item).Decl.(type) {
	case *types.FuncDecl:
		g.relocFnTypes(types.FnRef{Item: item}, ty)
	case *types.GlobalDecl:
		if d.Extern {
			ty(d.Type)
		}
	}
}

// relocFnTypes visits the types fnRef's IR for f reads (PackConstTypes).
func (g *packGen) relocFnTypes(f types.FnRef, ty func(types.TypeID)) {
	for _, a := range f.Args {
		if !a.IsConst {
			ty(a.Type)
		} else if a.Const.Kind == types.CASizeOf {
			ty(a.Const.Type)
		}
	}
	if g.inlineIntrinsic(f.Item) {
		sig := g.declSig(f.Item)
		if len(f.Args) != len(sig.Generics) {
			return
		}
		for _, p := range sig.Params {
			ty(g.subst(p.Type, f.Args))
		}
		ty(g.subst(sig.ReturnType(g.tt), f.Args))
		return
	}
	fi, ok := g.findFunc(f.Item, f.Args)
	if !ok {
		return // a dead IoTable entry (deadIoEntry): the trap stub
	}
	if b := g.u.Funcs[fi].Body; b != nil {
		for i := 0; i <= int(b.ParamCount); i++ {
			ty(b.Locals[i].Type)
		}
		return
	}
	sig := g.declSig(f.Item)
	for _, p := range sig.Params {
		ty(p.Type)
	}
	ty(sig.ReturnType(g.tt))
}
