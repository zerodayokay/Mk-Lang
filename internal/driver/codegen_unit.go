// Why: monomorphization and code generation (stages 8 and 9) need the
// build's roots and a codegen.Unit made from mono's output; the driver is
// where both packages meet, since codegen does not import mono.
//
// What: runtimeRoots and buildRoots (what mono starts from), noBody (the
// E0001 of a reachable function with no body), unitOf and globalsOf (the
// codegen.Unit), definesVar, forGlobals and cgEnv (the codegen.Env).
//
// How: mono's instances and packs are copied field for field into the
// Unit, in instance-table and Partition order, so the IR is deterministic.

package driver

import (
	"slices"

	"mk/internal/codegen"
	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/mono"
	"mk/internal/source"
	"mk/internal/types"
)

// runtimeRoots are the std functions codegen calls by symbol or by key:
// the entry hook, the panic hooks, the scope, spawn and system-stack entries,
// and the float remainder functions. Each that std declares is a RootRuntime root; one that std does
// not declare is left out, and codegen raises "missing runtime entry" only
// when a program reaches the construct that needs it.
var runtimeRoots = []types.Key{
	types.ItemKey("std.task", "Start"),
	types.ItemKey("std.rt", "PanicAt"),
	types.ItemKey("std.rt", "PanicMsg"),
	types.ItemKey("std.rt", "PanicErr"),
	types.ItemKey("std.task", "Enter"),
	types.ItemKey("std.task", "SpawnAlloc"),
	types.ItemKey("std.task", "Spawn"),
	types.ItemKey("std.task", "Join"),
	types.ItemKey("std.task", "Cancel"),
	// The deadline intrinsics' entries (internal/codegen spawn.go).
	types.ItemKey("std.task", "CancelAfter"),
	types.ItemKey("std.task", "Expired"),
	types.ItemKey("std.task", "Remaining"),
	types.ItemKey("std.task", "SystemStack"),
	types.ItemKey("std.task", "SystemStackDone"),
	// A float %: codegen calls these by key, never frem.
	types.ItemKey("std.math", "Rem64"),
	types.ItemKey("std.math", "Rem32"),
}

// buildRoots are the roots of one bin's collection: its Main (or Main's
// report wrapper, mainEntry), every export extern of the package's own
// modules (with allFuncs, every non-generic function of them: a corpus unit
// with no Main), and the runtime entries std declares for the target
// (rootsFor; gro marks a general-regs-only bin). Sorted by kind then key, so
// the list depends on the source tree only.
func (w *world) buildRoots(mainMod string, pkg []*modNode, allFuncs, gro bool) []mono.Root {
	var roots []mono.Root
	if id := w.mainEntry(mainMod); id != types.NoItem {
		roots = append(roots, mono.Root{Kind: mono.RootMain, Item: id})
	}
	var exports []types.ItemID
	for _, m := range pkg {
		if m.mod == nil {
			continue
		}
		for _, id := range m.mod.Items {
			it := w.items.Get(id)
			// A function with a section clause is emitted though no Mk code
			// calls it (a boot entry).
			if fd, ok := it.Decl.(*types.FuncDecl); ok && (fd.Sig.Extern && it.Exported || allFuncs && it.Kind == types.KindFunc && !fd.Sig.Extern ||
				fd.Section != "" && it.Kind == types.KindFunc) &&
				!fd.Bodiless && len(fd.Sig.Generics) == 0 && !hasConstParam(&fd.Sig) && w.sem5.body(id) != nil {
				exports = append(exports, id)
			}
		}
	}
	slices.SortFunc(exports, func(a, b types.ItemID) int { return cmpKey(w.items.Get(a).Key, w.items.Get(b).Key) })
	for _, id := range exports {
		roots = append(roots, mono.Root{Kind: mono.RootExport, Item: id})
	}
	for _, k := range rootsFor(w.cfg.Target, gro) {
		if id, ok := w.items.ByKey(k); ok {
			roots = append(roots, mono.Root{Kind: mono.RootRuntime, Item: id})
		}
	}
	if w.cfg.Cover && !isFree(w.cfg.Target) {
		if id, ok := w.items.ByKey(types.ItemKey("std.rt", "CoverCmp")); ok {
			roots = append(roots, mono.Root{Kind: mono.RootRuntime, Item: id})
		}
	}
	return roots
}

func cmpKey(a, b types.Key) int {
	switch x, y := a.String(), b.String(); {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// noBody: every reachable instance with no body that is neither an
// extern nor a std intrinsic is one E0001 with facts symbol and reason
// no-body, at the function's declaration, before any tool runs. A body the
// build rejected never gets here (the report was not clean).
func (w *world) noBody(in *mono.Instances) []*diag.Diagnostic {
	var out []*diag.Diagnostic
	for i := range in.List {
		inst := &in.List[i]
		if inst.Body != nil {
			continue
		}
		it := w.items.Get(inst.Item)
		fd, ok := it.Decl.(*types.FuncDecl)
		if ok && (fd.Sig.Extern || fd.Intrinsic) {
			continue
		}
		out = append(out, diag.New(diag.E0001, it.Span, "`"+inst.Name+"` is reached but has no body").
			Text("symbol", inst.Symbol).Text("reason", "no-body").Alt(altGiveBody).Alt(altNoCall))
	}
	return out
}

// unitOf copies mono's instances and packs into codegen's Unit field for
// field (codegen does not import mono): Funcs in instance-table order, each
// with its pack, Packs in Partition order. Lookup is nil (codegen's exact
// (item, args) match; fn-literal canonicalization is a request to mono).
// Local is false everywhere, which is always correct. Each Body is the
// instance's, except that a release build's is codegenBody's copy with the
// inlined leaves and the loop versions.
func (w *world) unitOf(in *mono.Instances, packs []mono.Pack, pkg []*modNode) *codegen.Unit {
	u := &codegen.Unit{Funcs: make([]codegen.Func, len(in.List)), Packs: make([][]int, len(packs))}
	packOf := make([]int, len(in.List))
	for i := range packOf {
		packOf[i] = -1
	}
	for p, pk := range packs {
		for _, fi := range pk.Members {
			packOf[fi] = p
		}
	}
	for i := range in.List {
		inst := &in.List[i]
		u.Funcs[i] = codegen.Func{Symbol: inst.Symbol, Key: inst.Key.Text, Item: inst.Item, Args: inst.Args,
			Body: w.codegenBody(in, i, packOf), Spawned: inst.Spawned, AddrTaken: inst.AddrTaken, Pack: -1}
	}
	for p, pk := range packs {
		u.Packs[p] = append([]int(nil), pk.Members...)
		for _, fi := range pk.Members {
			u.Funcs[fi].Pack = p
		}
	}
	u.Globals = w.globalsOf(in, packs, pkg)
	return u
}

// globalsOf assigns every var global an instance body references to the
// first pack of its module (the driver's choice, codegen.Unit.Globals), or
// to the first pack that references it when its module has no pack (a global
// of a module whose functions were all inlined away). A var or extern var an
// asm sym operand names is referenced as well, and every const and var with
// a section clause in the package's own modules (pkg) is listed whether or
// not a body reads it, in its module's first pack or else pack 0, as is
// every export extern var the build defines there (C or a debugger reads it
// unseen). Sorted by item key.
func (w *world) globalsOf(in *mono.Instances, packs []mono.Pack, pkg []*modNode) []codegen.Global {
	firstOf := map[types.ModuleID]int{} // lookup only
	for p, pk := range packs {
		for _, fi := range pk.Members {
			mod := in.List[fi].Owner
			if _, ok := firstOf[mod]; !ok {
				firstOf[mod] = p
			}
		}
	}
	refBy := map[types.ItemID]int{} // global -> first referencing pack; lookup only
	var ids []types.ItemID
	for p, pk := range packs {
		for _, fi := range pk.Members {
			b := in.List[fi].Body
			if b == nil {
				continue
			}
			forGlobals(b, func(id types.ItemID) {
				if _, ok := w.items.Get(id).Decl.(*types.GlobalDecl); !ok {
					return
				}
				if _, ok := refBy[id]; !ok {
					refBy[id] = p
					ids = append(ids, id)
				}
			})
		}
	}
	if len(packs) > 0 {
		for _, m := range pkg {
			if m.mod == nil {
				continue
			}
			for _, id := range m.mod.Items {
				section := ""
				switch d := w.items.Get(id).Decl.(type) {
				case *types.ConstDecl:
					section = d.Section
				case *types.GlobalDecl:
					section = d.Section
				}
				if _, ok := refBy[id]; (section != "" || w.definesVar(id)) && !ok {
					refBy[id] = 0
					ids = append(ids, id)
				}
			}
		}
	}
	slices.SortFunc(ids, func(a, b types.ItemID) int { return cmpKey(w.items.Get(a).Key, w.items.Get(b).Key) })
	out := make([]codegen.Global, 0, len(ids))
	for _, id := range ids {
		p, ok := firstOf[w.items.Get(id).Module]
		if !ok {
			p = refBy[id]
		}
		out = append(out, codegen.Global{Item: id, Pack: p, Define: w.definesVar(id)})
	}
	return out
}

// definesVar reports an export extern var that the build defines
// (codegen.Global.Define): exported, extern, and not in a C
// import's binding, whose `export extern var` declares what C defines
// (cimport's externVar). A plain extern var is a linker script's,
// a .s file's or C's.
func (w *world) definesVar(id types.ItemID) bool {
	it := w.items.Get(id)
	gd, ok := it.Decl.(*types.GlobalDecl)
	if !ok || !gd.Extern || !it.Exported {
		return false
	}
	if w.sem5 == nil {
		return true
	}
	// Every module of a checked build is noted (pipelineAll), C imports'
	// bindings included; std has none.
	m := w.sem5.module(it.Module)
	return m == nil || !m.cimport
}

// forGlobals calls f with the item of every RvGlobalRef in b, and of every
// asm sym operand (a function's item too; the caller keeps the globals).
func forGlobals(b *mir.Body, f func(types.ItemID)) {
	for _, blk := range b.Blocks {
		for _, st := range blk.Stmts {
			if st.Rvalue.Kind == mir.RvGlobalRef {
				f(st.Rvalue.Item)
			}
		}
	}
	for _, a := range b.Asms {
		for _, op := range a.Operands {
			if op.Kind == mir.AsmSym {
				f(op.Sym)
			}
		}
	}
}

// cgEnv is codegen.Env over the session: check's layouts and facts, the
// files' package-relative paths (world.sitePath, not the SourceMap's
// diagnostic paths) and the source map's positions.
type cgEnv struct{ w *world }

func (e cgEnv) Table() *types.Table                 { return e.w.tab }
func (e cgEnv) Items() *types.Items                 { return e.w.items }
func (e cgEnv) Consts() *types.ConstTable           { return e.w.tab.Consts() }
func (e cgEnv) Facts(t types.TypeID) types.Facts    { return e.w.sem5.chk.Facts(t) }
func (e cgEnv) Path(id source.FileID) string        { return displaySitePath(e.w.sitePath(id), e.w.display) }
func (e cgEnv) LineCol(p source.Pos) source.LineCol { return e.w.sm.LineCol(p) }
func (e cgEnv) Layout(t types.TypeID) *types.Layout {
	l := e.w.sem5.chk.Layout(t)
	return &l
}
