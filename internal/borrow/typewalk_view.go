// Why: whether a view type keeps loans or takes stores depends on the
// methods its impls declare, which live across modules; typewalk.go's view
// scans (viewKeepsScan, holdStores) need those found fast and reproducibly.
//
// What: declaresHolderStore, fnStoresInto and holdsItem (the store tests),
// keepScan, the impl index (implsFor, implClosure, implIndexBuild,
// ImplCache, modImplsOf, sharedImplsFor), and bindArg and bindType (binding
// an impl's generics).
//
// How: per-module impl indexes are built once per session and shared; a
// body's answer comes from them in a fixed module order. Every scan records
// its reads, so an impl added or removed invalidates cached results.

package borrow

import (
	"slices"
	"sync"

	"mk/internal/types"
)

// declaresHolderStore reports whether parameters ps, with their `into`
// targets into and the clauses stores, declare a store into a parameter
// whose type holds view item v.
func (w *walker) declaresHolderStore(ps []types.TypeID, into []types.ParamIndex, stores []types.Store, v types.ItemID) bool {
	n := len(ps)
	for i, p := range into {
		if p != types.NoParam && int(p) <= n && int(p) != i+1 && w.holdsItem(ps[p-1], v, map[types.TypeID]bool{}, w.newRecur()) {
			return true
		}
	}
	for _, st := range stores {
		if st.Into != types.NoParam && int(st.Into) <= n && st.From != st.Into && w.holdsItem(ps[st.Into-1], v, map[types.TypeID]bool{}, w.newRecur()) {
			return true
		}
	}
	return false
}

// fnStoresInto reports whether t is or holds (behind references, slices,
// optionals, arrays, error unions, type arguments or fields) a fn type that
// declares a store into a parameter holding view item v. On a
// polymorphically recursive type it stops at recur's bound and answers true,
// the side that refuses more.
func (w *walker) fnStoresInto(t types.TypeID, v types.ItemID, seen map[types.TypeID]bool, r *recur) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if stop, leave := r.enter(t); stop {
		return true
	} else if leave != nil {
		defer leave()
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KFn:
		ps := make([]types.TypeID, len(ty.Fn.Params))
		into := make([]types.ParamIndex, len(ty.Fn.Params))
		for i, p := range ty.Fn.Params {
			ps[i], into[i] = p.Type, p.Into
		}
		if w.declaresHolderStore(ps, into, ty.Fn.Stores, v) {
			return true
		}
		for _, p := range ps {
			if w.fnStoresInto(p, v, seen, r) {
				return true
			}
		}
		return w.fnStoresInto(ty.Fn.Result, v, seen, r)
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr:
		return w.fnStoresInto(ty.Elem, v, seen, r)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && w.fnStoresInto(a.Type, v, seen, r) {
				return true
			}
		}
	}
	for _, f := range w.inlineFields(t) {
		if w.fnStoresInto(f, v, seen, r) {
			return true
		}
	}
	return false
}

// holdsItem reports whether t is, refers to or holds the named type of
// view item v: behind references, slices, raw pointers, optionals, arrays
// and error unions, as a type argument (`&Vec[V]`, `&Box[V]`), or in the
// fields of a struct, record or enum. A type parameter does not count:
// counting it, as an instance may be v, would reject many functions of
// std.http, std.net and std.websocket with E0602, E0604 and E0605 (an open
// question). On a polymorphically recursive type it stops at recur's bound
// and answers true, the side that refuses more.
func (w *walker) holdsItem(t types.TypeID, v types.ItemID, seen map[types.TypeID]bool, r *recur) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if stop, leave := r.enter(t); stop {
		return true
	} else if leave != nil {
		defer leave()
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr:
		return w.holdsItem(ty.Elem, v, seen, r)
	case types.KNamed:
		if ty.Item == v {
			return true
		}
		for _, a := range ty.Args {
			if !a.IsConst && w.holdsItem(a.Type, v, seen, r) {
				return true
			}
		}
	}
	for _, f := range w.inlineFields(t) {
		if w.holdsItem(f, v, seen, r) {
			return true
		}
	}
	return false
}

// keepScan is one viewKeepsScan: the view's item, the mode, and what the
// methods read so far keep and store.
type keepScan struct {
	w                *walker
	view             types.ItemID
	gen              genMode
	keepsIn, storeIn bool
}

// method reads one function of the view's namespace or of an impl for it,
// and reports whether it hands out from self a value that may point into
// global memory that takes stores. n is how many of its generics are the
// owner's (the view type's or the impl's), args their arguments, and free
// marks an impl's parameter its For does not bind (`impl[R] Src[R] for
// V`): its instance is any type, so it counts both when kept and when
// handed out, where a method's own type parameter counts only when kept.
func (s *keepScan) method(it *types.Item, n int, args []types.GenericArg, free []bool) bool {
	w := s.w
	if it.Kind != types.KindFunc {
		return false
	}
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok {
		return false
	}
	if w.reads != nil {
		w.reads.Add(types.Read{Kind: types.ReadSignature, Key: it.Key})
	}
	judge := func(x types.TypeID) bool {
		return w.mentionsFree(x, free) || w.firstTakes(w.subst(x, args), s.gen, map[types.TypeID]bool{}, w.newRecur())
	}
	takes := func(x types.TypeID) bool { return w.paramFrom(x, n) || judge(x) }
	sig := &fd.Sig
	if sig.Receiver != types.NoReceiver {
		for i, p := range sig.Params {
			if i > 0 && p.Into == 1 {
				s.storeIn = true
				if takes(p.Type) {
					s.keepsIn = true
				}
			}
		}
		for _, st := range sig.Flows.Stores {
			if st.Into == 1 && st.From != 1 && int(st.From) <= len(sig.Params) {
				s.storeIn = true
				q := sig.Params[st.From-1].Type
				if takes(q) {
					s.keepsIn = true
				} else if k := w.tab.Kind(q); (k == types.KRef || k == types.KMut || k == types.KSlice || k == types.KMutSlice) && takes(w.tab.Get(q).Elem) {
					s.keepsIn = true
				}
			}
		}
		for _, src := range sig.Flows.Sources {
			if src.Param == 1 && !w.paramFrom(sig.Result, n) && judge(sig.Result) {
				return true
			}
		}
		return false
	}
	if it.BorrowedConstructorResult(w.tab) != s.view {
		return false
	}
	for _, src := range sig.Flows.Sources {
		if int(src.Param) >= 1 && int(src.Param) <= len(sig.Params) && takes(sig.Params[src.Param-1].Type) {
			s.keepsIn = true
		}
	}
	return false
}

// implsFor lists the impls whose For names view item v, or a reference to it
// (`impl Sink for &V`, `for mut V`: implFor), in the modules whose impls a
// member lookup in the body sees: v's module, the body's module and its
// transitive imports. iface's memberNS hashes the methods of the same impls
// (its refImpls included) into the namespace read viewKeepsScan records, so an
// impl added or removed there invalidates the result. An impl lives in its
// type's module or its interface's, and code that calls one is in a module
// that imports the interface's. The module order is fixed once per body, at
// the first view scan, and reads only modules whose resolution is finished (as
// fixes.go consumers does).
//
// The answer is the per-body index's (implIndexBuild over implClosure, kept
// as the cross-check's reference in typewalk_index_test.go): modules
// breadth first from the first scan's view module then the body's, items
// in each module's order. It is assembled from per-module indexes built
// once per session (modImplsOf, in Env.Impls) and shared, with the
// answers, by every body whose first scan starts from the same two modules
// (a per-body build over the whole closure allocated too much on large
// programs).
func (w *walker) implsFor(v types.ItemID, vmod types.ModuleID) []types.ItemID {
	if w.implShared == nil {
		w.implShared = w.sharedImplsFor(vmod)
	}
	x := w.implShared
	if l, ok := x.lists.Load(v); ok {
		return l.([]types.ItemID)
	}
	var out []types.ItemID
	for _, m := range x.mods {
		out = append(out, m.byFor[v]...)
	}
	out = slices.Clip(out)
	x.lists.Store(v, out)
	return out
}

// implClosure is the breadth-first module order of implsFor's index: vmod,
// then the body's module, then imports as met.
func (w *walker) implClosure(vmod types.ModuleID) []*types.Module {
	seen := map[types.ModuleID]bool{}
	var work []types.ModuleID
	add := func(id types.ModuleID) {
		if id != types.NoModule && !seen[id] {
			seen[id] = true
			work = append(work, id)
		}
	}
	add(vmod)
	if w.mod != nil {
		add(w.mod.ID)
	}
	var out []*types.Module
	for len(work) > 0 {
		m := w.items.Module(work[0])
		work = work[1:]
		out = append(out, m)
		for _, d := range m.Imports {
			add(d)
		}
	}
	return out
}

// implIndexBuild builds implsFor's index over the modules mods, in order.
func (w *walker) implIndexBuild(mods []*types.Module) map[types.ItemID][]types.ItemID {
	out := map[types.ItemID][]types.ItemID{}
	for _, m := range mods {
		for _, id := range m.Items {
			it := w.items.Get(id)
			if it.Kind != types.KindImpl {
				continue
			}
			im, ok := it.Decl.(*types.ImplDecl)
			if !ok || im.For == types.NoType || w.tab.Kind(w.implFor(im.For)) != types.KNamed {
				continue
			}
			f := w.tab.Get(w.implFor(im.For)).Item
			out[f] = append(out[f], id)
		}
	}
	return out
}

// modImpls is one module's impls by the view item their For names, each
// list in item order, and the item list it was built from (so a replaced
// list is seen: a filtered list, a reused Module).
type modImpls struct {
	id    types.ModuleID
	items []types.ItemID
	byFor map[types.ItemID][]types.ItemID
}

// current reports whether m still has the item list x was built from (the
// same slice: same length and first element).
func (x *modImpls) current(w *walker) bool {
	cur := w.items.Module(x.id).Items
	return len(cur) == len(x.items) && (len(cur) == 0 || &cur[0] == &x.items[0])
}

// sharedImpls is implsFor's state shared by the bodies of one (first
// scan's view module, body module) pair: the closure's per-module indexes
// in breadth-first order, and the answers by view item ([]ItemID).
type sharedImpls struct {
	mods  []*modImpls
	lists sync.Map
}

// implPair keys sharedImpls: the first scan's view module and the body's.
type implPair struct {
	v, body types.ModuleID
}

// ImplCache holds the view scans' impl indexes of one session (Env.Impls):
// each module's, and each (view module, body module) pair's closure and
// answers. Its zero value is ready; it is safe for concurrent bodies. It
// holds module IDs and item lists, never a Module.
type ImplCache struct {
	mu    sync.Mutex
	mods  map[types.ModuleID]*modImpls
	pairs map[implPair]*sharedImpls
}

// modImplsOf is m's per-module index, built on first use or when m's item
// list changed. The caller holds c.mu.
func (w *walker) modImplsOf(c *ImplCache, m *types.Module) *modImpls {
	if x := c.mods[m.ID]; x != nil && x.current(w) {
		return x
	}
	x := &modImpls{id: m.ID, items: m.Items, byFor: w.implIndexBuild([]*types.Module{m})}
	c.mods[m.ID] = x
	return x
}

// sharedImplsFor is the shared state for a body whose first view scan
// names a view of module vmod, rebuilt when a module of its closure has
// another item list than its index was built from.
func (w *walker) sharedImplsFor(vmod types.ModuleID) *sharedImpls {
	c := w.impls
	key := implPair{v: vmod, body: types.NoModule}
	if w.mod != nil {
		key.body = w.mod.ID
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pairs == nil {
		c.mods, c.pairs = map[types.ModuleID]*modImpls{}, map[implPair]*sharedImpls{}
	}
	if x := c.pairs[key]; x != nil {
		fresh := true
		for _, m := range x.mods {
			if !m.current(w) {
				fresh = false
				break
			}
		}
		if fresh {
			return x
		}
	}
	x := &sharedImpls{}
	for _, m := range w.implClosure(vmod) {
		x.mods = append(x.mods, w.modImplsOf(c, m))
	}
	c.pairs[key] = x
	return x
}

// bindArg matches one generic argument of an impl's For against t's.
func (w *walker) bindArg(pat, a types.GenericArg, args []types.GenericArg, set []bool) bool {
	if pat.IsConst != a.IsConst {
		return false
	}
	if !pat.IsConst {
		return w.bindType(pat.Type, a.Type, args, set)
	}
	c := pat.Const
	if c.Kind == types.CAParam && int(c.Param) < len(args) {
		if !set[c.Param] {
			args[c.Param], set[c.Param] = a, true
		}
		return true
	}
	if a.Const.Kind == types.CAParam {
		return true // a const parameter of the body: any value
	}
	return c == a.Const
}

// bindType matches type pat of an impl's For against type x of the view
// type: a parameter of the impl binds x; otherwise the two must have one
// shape, with a type parameter, Self or an inferred type in x matching
// anything. A kind it does not take apart (a record, a fn type) matches.
func (w *walker) bindType(pat, x types.TypeID, args []types.GenericArg, set []bool) bool {
	if pat == x {
		return true
	}
	p := w.tab.Get(pat)
	if p.Kind == types.KParam && int(p.Index) < len(args) {
		if !set[p.Index] {
			args[p.Index], set[p.Index] = types.TypeArg(x), true
		}
		return true
	}
	xt := w.tab.Get(x)
	switch xt.Kind {
	case types.KParam, types.KSelf, types.KInfer:
		return true
	}
	if p.Kind != xt.Kind {
		return false
	}
	switch p.Kind {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KErrorUnion:
		return w.bindType(p.Elem, xt.Elem, args, set)
	case types.KArray:
		return w.bindArg(types.ConstGenericArg(p.Len), types.ConstGenericArg(xt.Len), args, set) && w.bindType(p.Elem, xt.Elem, args, set)
	case types.KNamed:
		if p.Item != xt.Item || len(p.Args) != len(xt.Args) {
			return false
		}
		for i := range p.Args {
			if !w.bindArg(p.Args[i], xt.Args[i], args, set) {
				return false
			}
		}
		return true
	case types.KRecord, types.KFn:
		return true
	}
	return false
}

// mentionsFree reports whether t mentions a type parameter that free marks.
func (w *walker) mentionsFree(t types.TypeID, free []bool) bool {
	if free == nil {
		return false
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		return int(ty.Index) < len(free) && free[ty.Index]
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KArray, types.KErrorUnion, types.KRawPtr:
		return w.mentionsFree(ty.Elem, free)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && w.mentionsFree(a.Type, free) {
				return true
			}
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			if w.mentionsFree(f.Type, free) {
				return true
			}
		}
	case types.KFn:
		for _, p := range ty.Fn.Params {
			if w.mentionsFree(p.Type, free) {
				return true
			}
		}
		return w.mentionsFree(ty.Fn.Result, free)
	}
	return false
}
