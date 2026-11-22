// Why: most borrow rules depend on what a type holds: whether it carries
// loans, holds a view, can be written through a shared step, or may point
// into global memory that takes stores. These answers are properties of
// the type alone and must be computed once per body.
//
// What: walker (the memoized walks of one body), refStep and refSteps,
// factsOf, inlineFields and subst, holdsView, writableByType, the global
// memory tests (storesFirst, storesFirstGen, firstTakes, viewKeeps,
// holdStores, fnResultSupplied), walkExtra, holdsRaw, holdsMutexNonPlain
// and byValueParam.
//
// How: each walk is memoized by TypeID; a type met again while its walk is
// in progress adds nothing, so recursive types terminate, and recur bounds
// polymorphically recursive ones. Every declaration or shape a walk reads
// is recorded in the result's read set, so a cached result follows it.

package borrow

import (
	"mk/internal/source"
	"mk/internal/types"
)

// walker holds the memoized type walks of one body. Every result is a
// property of the type alone (and of the Env), so memoizing by TypeID is
// exact. A type met again while its walk is in progress contributes nothing
// more, so recursive types terminate.
type walker struct {
	tab   *types.Table
	items *types.Items
	known *types.KnownItems
	envF  func(types.TypeID) types.Facts
	reads *types.ReadSet

	facts  map[types.TypeID]types.Facts
	fields map[types.TypeID][]types.TypeID
	view   map[types.TypeID]memoBit
	keeps  map[viewKey]memoBit
	// keepsBusy is set when viewKeeps met a view still being scanned.
	keepsBusy bool
	mutexNP   map[types.TypeID]memoBit
	walk      map[types.TypeID]memoBit
	refs      map[types.TypeID]*refInfo
	readDecl  map[types.ItemID]bool
	// mod is the body's module, whose import closure holds the impls a
	// view's scan reads (implsFor); implShared indexes them by the view's
	// item, taken at the first scan from impls, the session's cache
	// (Env.Impls, or the body's own when nil; typewalk_view.go).
	mod        *types.Module
	implShared *sharedImpls
	impls      *ImplCache
	// holds memoizes holdStores by the view's item.
	holds map[types.ItemID]bool
}

type memoBit uint8

const (
	memoNone memoBit = iota
	memoBusy
	memoFalse
	memoTrue
)

// refStep is one reference stored directly in a type's bytes: its mode and
// its referent. An "any" step stands for every type (a view, Self or a type
// parameter); it has no referent.
type refStep struct {
	excl     bool
	any      bool
	referent types.TypeID
}

// refInfo is refSteps(t), deduplicated, in first-found order.
type refInfo struct {
	steps []refStep
}

func newWalker(env *Env, reads *types.ReadSet) *walker {
	w := &walker{
		tab:      env.Types,
		known:    env.Known,
		envF:     env.Facts,
		reads:    reads,
		facts:    make(map[types.TypeID]types.Facts),
		fields:   make(map[types.TypeID][]types.TypeID),
		view:     make(map[types.TypeID]memoBit),
		keeps:    make(map[viewKey]memoBit),
		mutexNP:  make(map[types.TypeID]memoBit),
		walk:     make(map[types.TypeID]memoBit),
		refs:     make(map[types.TypeID]*refInfo),
		readDecl: make(map[types.ItemID]bool),
		mod:      env.Module,
		impls:    env.Impls,
	}
	if w.impls == nil {
		w.impls = &ImplCache{}
	}
	if env.Types != nil {
		w.items = env.Types.Items()
	}
	return w
}

// factsOf is Env.Facts(t), memoized; it records a ReadShape for a named type
// (nested ones too, as each is first consulted).
func (w *walker) factsOf(t types.TypeID) types.Facts {
	if f, ok := w.facts[t]; ok {
		return f
	}
	if w.envF == nil {
		source.ICEf(source.Span{}, "borrow: Env.Facts is nil")
	}
	f := w.envF(t)
	w.facts[t] = f
	if ty := w.tab.Get(t); ty.Kind == types.KNamed && w.items != nil && w.reads != nil {
		w.reads.Add(types.Read{Kind: types.ReadShape, Key: w.items.Get(ty.Item).Key})
	}
	return f
}

// carrier, plain and linear read the kind and carrier facts (borrow never
// recomputes them).
func (w *walker) carrier(t types.TypeID) bool { return w.factsOf(t).Carrier }
func (w *walker) plain(t types.TypeID) bool   { return w.factsOf(t).Kind == types.Plain }
func (w *walker) linear(t types.TypeID) bool  { return w.factsOf(t).Kind == types.Linear }

// isKnown reports whether t is the named type of known item it.
func (w *walker) isKnown(t types.TypeID, it func(*types.KnownItems) types.ItemID) bool {
	if w.known == nil {
		return false
	}
	ty := w.tab.Get(t)
	return ty.Kind == types.KNamed && ty.Item != types.NoItem && ty.Item == it(w.known)
}

// inlineFields is the types held directly in t's own bytes, one level down:
// the element of [N]T, ?T and E!T; the fields of a record; a struct's fields
// and every enum variant's payload fields, with t's generic arguments
// substituted. A reference, slice, str, raw pointer, fn, parameter or error
// set has none. Scope[E] is {Scope, E!void}, never its declaration.
func (w *walker) inlineFields(t types.TypeID) []types.TypeID {
	if fs, ok := w.fields[t]; ok {
		return fs
	}
	var out []types.TypeID
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KArray, types.KOptional, types.KErrorUnion:
		if ty.Elem != types.Void && ty.Elem != types.NoType {
			out = append(out, ty.Elem)
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			out = append(out, f.Type)
		}
	case types.KNamed:
		out = w.namedFields(t, ty)
	}
	w.fields[t] = out
	return out
}

// namedFields reads a struct's or enum's declaration (recording ReadFields
// "all") and substitutes the type's arguments.
func (w *walker) namedFields(t types.TypeID, ty *types.Type) []types.TypeID {
	if w.items == nil {
		return nil
	}
	if w.known != nil && w.known.Scope != types.NoItem && ty.Item == w.known.Scope && len(ty.Args) == 1 &&
		!ty.Args[0].IsConst && w.tab.Kind(ty.Args[0].Type) == types.KErrorSet {
		// Scope[E] is {hdr: Scope, err: E!void}.
		plainScope := w.tab.Named(ty.Item, nil)
		errs := w.tab.Get(ty.Args[0].Type).Errors
		return []types.TypeID{plainScope, w.tab.ErrorUnion(errs, types.Void)}
	}
	it := w.items.Get(ty.Item)
	if _, isIface := it.Decl.(*types.InterfaceDecl); isIface || it.Decl == nil {
		// An interface value shows no fields, so nothing is read: a
		// ReadFields of an interface has no answer (iface Hasher.Fields),
		// which left every module whose body the module scan's fn-type
		// walk led through one never stored (fnStoresInto over the
		// `&self` of std.core's `Slice[T].Len` and its siblings, in std.fs,
		// std.net and std.http among others). An interface
		// replaced by a struct of the same name is still seen: the module
		// namespace holdStores reads spells each item's kind
		// (TestViewKeepsColdWarm's iface-to-struct).
		return nil
	}
	if !w.readDecl[ty.Item] {
		w.readDecl[ty.Item] = true
		if w.reads != nil {
			// The walk reads the declaration, private fields
			// included, and the shape of the type it walks into.
			w.reads.Add(types.Read{Kind: types.ReadFields, Key: it.Key, Name: "all"})
			w.reads.Add(types.Read{Kind: types.ReadShape, Key: it.Key})
		}
	}
	var out []types.TypeID
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		for _, f := range d.Fields {
			out = append(out, w.subst(f.Type, ty.Args))
		}
	case *types.EnumDecl:
		for _, v := range d.Variants {
			for _, f := range v.Fields {
				out = append(out, w.subst(f.Type, ty.Args))
			}
		}
	}
	return out
}

// subst replaces every KParam index i in t by args[i] (a type argument) and
// every CAParam i by args[i]'s const argument. internal/types has no
// substitution, so this one is local.
func (w *walker) subst(t types.TypeID, args []types.GenericArg) types.TypeID {
	if len(args) == 0 {
		return t
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		if int(ty.Index) < len(args) && !args[ty.Index].IsConst {
			return args[ty.Index].Type
		}
		return t
	case types.KRef:
		return w.tab.Ref(w.subst(ty.Elem, args))
	case types.KMut:
		return w.tab.Mut(w.subst(ty.Elem, args))
	case types.KSlice:
		return w.tab.Slice(w.subst(ty.Elem, args))
	case types.KMutSlice:
		return w.tab.MutSlice(w.subst(ty.Elem, args))
	case types.KRawPtr:
		return w.tab.RawPtr(w.subst(ty.Elem, args))
	case types.KOptional:
		return w.tab.Optional(w.subst(ty.Elem, args))
	case types.KArray:
		n := ty.Len
		if n.Kind == types.CAParam && int(n.Param) < len(args) && args[n.Param].IsConst {
			n = args[n.Param].Const
		}
		return w.tab.Array(n, w.subst(ty.Elem, args))
	case types.KErrorUnion:
		return w.tab.ErrorUnion(ty.Errors, w.subst(ty.Elem, args))
	case types.KRecord:
		fs := make([]types.RecordField, len(ty.Fields))
		for i, f := range ty.Fields {
			fs[i] = types.RecordField{Name: f.Name, Type: w.subst(f.Type, args)}
		}
		return w.tab.Record(fs)
	case types.KNamed:
		if len(ty.Args) == 0 {
			return t
		}
		as := make([]types.GenericArg, len(ty.Args))
		for i, a := range ty.Args {
			if a.IsConst {
				c := a.Const
				if c.Kind == types.CAParam && int(c.Param) < len(args) && args[c.Param].IsConst {
					c = args[c.Param].Const
				}
				as[i] = types.ConstGenericArg(c)
			} else {
				as[i] = types.TypeArg(w.subst(a.Type, args))
			}
		}
		return w.tab.Named(ty.Item, as)
	case types.KFn:
		fn := *ty.Fn
		fn.Params = make([]types.FnParam, len(ty.Fn.Params))
		for i, p := range ty.Fn.Params {
			fn.Params[i] = types.FnParam{Type: w.subst(p.Type, args), Into: p.Into}
		}
		fn.Result = w.subst(ty.Fn.Result, args)
		return w.tab.Fn(fn)
	}
	return t
}

// holdsView reports whether some type in inline(t) is a view.
func (w *walker) holdsView(t types.TypeID) bool {
	switch w.view[t] {
	case memoTrue:
		return true
	case memoFalse, memoBusy:
		return false
	}
	w.view[t] = memoBusy
	r := w.factsOf(t).View
	if !r {
		for _, f := range w.inlineFields(t) {
			if w.holdsView(f) {
				r = true
				break
			}
		}
	}
	w.view[t] = bitOf(r)
	return r
}

// writableByType is the rule "a step through Cell, Mutex, Chan, Arena or a
// view restores writing" as a property of the type stepped into.
func (w *walker) writableByType(t types.TypeID) bool {
	return w.factsOf(t).Interior || w.holdsView(t)
}

// storesFirst reports whether a value of t may point into global memory that
// takes a store of a loan: some reference stored in t's bytes refers to a
// carrier that a step into restores writing (writableByType: a Cell, Mutex,
// Chan, Arena or view), or to a carrier that itself holds such a reference, at
// any depth; or t holds a view inline whose type arguments hold loans (its
// hidden storage may be a global's: a SpinGuard[?&i32] of a global Spin), or
// whose own signatures, its impls' methods included, declare that it keeps or
// hands out such a value (viewKeeps). Such a value pointing into a global is
// where a store of a frame's loan would launder it. t's own bytes do not
// count: a by-value Mutex[?&i32] result is the caller's own storage.
//
// Every level counts. Global memory below a first level that takes no store
// may still be written at run time: an Interior global's payload is (a global
// sync.Once[&Mutex[?&i32]] keeps the &Mutex OncePut wrote), so
// `?&&Mutex[?&i32]` read out of it counts. What a global's compile-time
// evaluation allocates (EvalArena) is emitted read-only, so a store through it
// faults rather than lands. No known route rests on that: a call's result
// counts here at every level, and a named global whose memory takes stores
// below its first level counts as writable (setup.go staticOf). A type
// parameter or Self counts only in storesFirstGen and storesFirstBare: a
// generic body is judged once, as written, so what an instance may make of it
// is judged there.
func (w *walker) storesFirst(t types.TypeID) bool {
	return w.firstTakes(t, genNone, map[types.TypeID]bool{}, w.newRecur())
}

// storesFirstGen is storesFirst for some instance of t: a first-level referent
// that may be writable by type once t's type parameters are substituted
// (mayWritable), a bare type parameter or Self in t's bytes (it may be a
// reference to such a type), or a view whose type arguments may hold loans. On
// a type that mentions no parameter it may count more than storesFirst (a
// referent writable by type that is no carrier, such as &Mutex[int]), so the
// generic rules use it only on a type that mentions one (paramFrom).
func (w *walker) storesFirstGen(t types.TypeID) bool {
	return w.firstTakes(t, genAll, map[types.TypeID]bool{}, w.newRecur())
}

// storesFirstBare is storesFirstGen without the referent rule: a bare type
// parameter or Self in t's bytes, or a view whose type arguments may hold
// loans, counts, but `&T` and `mut []T` count only as storesFirst counts
// them (resultGlobal's interface-method row: their `&U` is judged at the
// impl, boundaryStatic).
func (w *walker) storesFirstBare(t types.TypeID) bool {
	return w.firstTakes(t, genBare, map[types.TypeID]bool{}, w.newRecur())
}

type genMode uint8

const (
	genNone genMode = iota
	genBare
	genAll
)

// firstTakes walks t's bytes and, through a reference, its referent's. On
// a polymorphically recursive type it answers from t's closure
// (closureTakes) at the first instance that grew when nothing below it can
// take a store, so the walk below it is not made, and otherwise at recur's
// bound, where the closure counts the instance as an ANY step would.
func (w *walker) firstTakes(t types.TypeID, gen genMode, seen map[types.TypeID]bool, r *recur) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if stop, leave := r.enter(t); stop {
		return w.closureTakes(t, gen)
	} else if leave != nil {
		defer leave()
		if r.grown(t) && !w.closureTakes(t, gen) {
			return false
		}
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef, types.KSlice, types.KMut, types.KMutSlice:
		// Every level counts, not through ANY: a referent that holds a
		// reference may be an Interior global's payload, written at run time
		// (`?&&Mutex[?&i32]` read out of a global sync.Once). The deeper
		// levels are judged as written, so `&&T` in storesFirstBare counts
		// only as storesFirst counts `&T`.
		e := ty.Elem
		deep := gen
		if deep == genBare {
			deep = genNone
		}
		return w.carrier(e) && (w.writableByType(e) || w.firstTakes(e, deep, seen, r)) ||
			gen == genAll && w.mayWritable(e, map[types.TypeID]bool{})
	case types.KStr:
		return false
	case types.KParam, types.KSelf:
		// A Plain bound does not exempt it: &T, []T, str and fn are Plain,
		// so T: Plain admits &Mutex[?&i32].
		return gen != genNone
	}
	if ty.Kind == types.KNamed && w.factsOf(t).View {
		for _, a := range ty.Args {
			if !a.IsConst && (w.carrier(a.Type) || gen != genNone && w.mayWritable(a.Type, map[types.TypeID]bool{})) {
				return true
			}
		}
		if w.viewKeeps(t, ty, gen) {
			return true
		}
	}
	for _, f := range w.inlineFields(t) {
		if w.firstTakes(f, gen, seen, r) {
			return true
		}
	}
	return false
}

// viewKeeps reports whether view t's own functions declare that its hidden
// storage may point into global memory that takes stores, counted as a
// reference in its bytes would be (a view's fields are hidden, so its
// signatures stand for them). Its own functions are its namespace's and the
// methods of every impl for it, or for a reference to it, that the body
// sees (implsFor), and, for the store, every function of its module that
// declares a store into a parameter holding it (holdStores):
//   - a method hands out such a value read from self (`Get(&self):
//     &Mutex[?&i32] from self`), or
//   - the view keeps such a value, by a parameter stored into self (`m:
//     &Mutex[?&i32] into self`, `*q into self`), by a namespace constructor
//     (`V.Of(m): V from m`), or by a non-generic free constructor of the
//     view's module, and some
//     method stores into self, so a store may reach what it keeps.
//
// A view that keeps such a value but neither hands it out nor takes a store
// (time.Zone, made `from a` of an &Arena) does not count. Each type is
// judged with t's arguments substituted; one that mentions the method's own
// type parameter or Self counts when kept, since any instance may be such a
// value, and not when handed out (Arena.Put[T], ErasedBox.Ref[T]: their T
// is the caller's); an impl method's types are judged with the impl's
// parameters bound by its For (implArgs). The scan reads the type's member
// list and each method's signature.
//
// A view whose raw pointer reaches memory no signature names (`V.Global():
// V` setting it to &GM in safe code of the view's unsafe module) is not
// seen: its unsafe blocks' stated obligation is broken by its module's own
// code, not by the checker.
func (w *walker) viewKeeps(t types.TypeID, ty *types.Type, gen genMode) bool {
	if w.items == nil {
		return false
	}
	key := viewKey{t, gen}
	switch w.keeps[key] {
	case memoTrue:
		return true
	case memoFalse:
		return false
	case memoBusy:
		w.keepsBusy = true // a cycle: false for now, not remembered
		return false
	}
	w.keeps[key] = memoBusy
	outer := w.keepsBusy
	w.keepsBusy = false
	r := w.viewKeepsScan(ty, gen)
	switch {
	case r:
		w.keeps[key] = memoTrue
	case w.keepsBusy:
		// A false that met a view still being scanned may be true once that
		// one finishes, so it is not remembered (memoNone: judged again).
		w.keeps[key] = memoNone
	default:
		w.keeps[key] = memoFalse
	}
	w.keepsBusy = outer || w.keepsBusy
	return r
}

type viewKey struct {
	t   types.TypeID
	gen genMode
}

func (w *walker) viewKeepsScan(ty *types.Type, gen genMode) bool {
	vit := w.items.Get(ty.Item)
	if w.reads != nil {
		w.reads.Add(types.Read{Kind: types.ReadExports, Key: types.KeyFromString(vit.Key.String() + ":")})
	}
	m := w.items.Module(vit.Module)
	if m == nil {
		return false
	}
	if w.reads != nil {
		// The view module's keeping methods and free constructors, private ones included
		// (iface.ModuleKeepsKey: m's path and "#keeps"): the loop below
		// counts a private `V.Stow(mut self, x: &i32 into self)`, a keep
		// or a hand-out for a body of any module, but neither the API hash
		// nor the module-level reads a foreign body is validated against
		// show one added or removed.
		w.reads.Add(types.Read{Kind: types.ReadExports, Key: types.KeyFromString(m.Path + "#keeps")})
	}
	s := keepScan{w: w, view: ty.Item, gen: gen}
	for _, id := range m.Items {
		it := w.items.Get(id)
		if it.Owner == ty.Item && s.method(it, len(ty.Args), ty.Args, nil) {
			return true
		}
		if it.Owner == types.NoItem && it.BorrowedConstructorResult(w.tab) == ty.Item {
			s.method(it, 0, nil, nil)
		}
	}
	// The view's impl methods count as its own do (`impl Keep[&Mutex[?&i32]]
	// for V { Put(mut self, m: &Mutex[?&i32] into self) }`, `impl Src[...] for
	// V { Get(&self): &Mutex[?&i32] from self.p }`), each judged with the
	// impl's parameters bound by matching its For against t (implArgs).
	for _, iid := range w.implsFor(ty.Item, vit.Module) {
		iit := w.items.Get(iid)
		im := iit.Decl.(*types.ImplDecl)
		// The impl's signature (its For, generics and method list) is read
		// before the bind test, so an impl retargeted from another instance
		// to t's (`impl Sink for &G[i64]` to `&G[i32]`) invalidates the
		// result even where it did not bind: iface's memberNS lists only its
		// methods' names, which a retarget leaves as they were.
		if w.reads != nil {
			w.reads.Add(types.Read{Kind: types.ReadSignature, Key: iit.Key})
		}
		args, free, ok := w.implArgs(im, ty)
		if !ok {
			continue
		}
		for _, mid := range im.Methods {
			if s.method(w.items.Get(mid), len(im.Generics), args, free) {
				return true
			}
		}
	}
	// A store into the view declared by another function of its module counts
	// as one by its own methods: `PutInto(v: &V, io: Io, x: &i32 into v)`, a
	// receiver-less `V.PutInto`, or `H.Put(&self, ... into self)` of an H
	// holding V inline may write what V keeps through its unsafe blocks
	// (holdStores).
	if s.keepsIn && !s.storeIn && w.holdStores(ty.Item, m) {
		s.storeIn = true
	}
	return s.keepsIn && s.storeIn
}

// holdStores reports whether a function of view item v's module m declares
// a store into a parameter whose type is, refers to or holds v (holdsItem):
// `x: &i32 into v` or `*q into v`, v's own methods included when the store
// is into a parameter other than self (`V.PutOther(&self, o: &V, ... into
// o)`), and so is a fn type in its signature that declares such a store
// (an exported `GetKeep(): fn(v: &V, io: Io, x: &i32 into v)` handing out
// a private function), and so does a const or global of such a fn type
// (`const KeepC: fn(v: &V, ... into v) = Keep`). Only m's functions, consts
// and globals count; a function of another module storing through v's
// exported raw accessor or a fn value handed across modules is not seen.
//
// A body of another module counts m's exported functions and methods and
// its impls' methods only: m's API part, which is all a foreign body's
// cached result is validated against (a private function added to m
// leaves m's API hash, and so the foreign body's result, as it was). A
// private function of m is reached from another module only through an
// exported one, whose body storing into its own parameter must declare that
// store (E0604), so the exported signature shows it, EXCEPT where the
// exported function's `into`-declared parameter is a bare type parameter:
// holdsItem does not count one as the view, since counting it rejects std
// code (a known gap: an exported `Put[T](t: &T, ... into t)` calling a
// private storing function under `if T is V` is not seen). A body of m
// counts every function of m.
//
// It reads m's namespace, the namespace of each type of m and m's function
// list (every function, const, global and impl of m the body counts, by key),
// so a function, method or impl added, removed or retargeted there invalidates
// the result, and the signature of every function it judges too. Memoized per
// view item: nothing here depends on v's arguments or the mode.
func (w *walker) holdStores(v types.ItemID, m *types.Module) bool {
	if r, ok := w.holds[v]; ok {
		return r
	}
	if w.holds == nil {
		w.holds = map[types.ItemID]bool{}
	}
	if w.reads != nil {
		w.reads.Add(types.Read{Kind: types.ReadExports, Key: types.ModuleKey(m.Path)})
		// m's function list (iface.ModuleFuncsKey: m's path and "#fns"):
		// every function, const, global and impl of m by key, for a foreign
		// body its API's, so an impl added, removed or retargeted in m, whose
		// For may have no type of m to list it (`impl Sink2 for i32`, `for
		// ?V`, `for []V`), invalidates the result.
		w.reads.Add(types.Read{Kind: types.ReadExports, Key: types.KeyFromString(m.Path + "#fns")})
	}
	foreign := w.mod != nil && w.mod.ID != m.ID
	r := false
	seen := map[types.ItemID]bool{}
	judge := func(id types.ItemID) {
		if seen[id] {
			return
		}
		seen[id] = true
		it := w.items.Get(id)
		fd, ok := it.Decl.(*types.FuncDecl)
		if !ok || it.Kind != types.KindFunc {
			return
		}
		if foreign && !it.Exported && (it.Owner == types.NoItem || w.items.Get(it.Owner).Kind != types.KindImpl) {
			return
		}
		if w.reads != nil {
			w.reads.Add(types.Read{Kind: types.ReadSignature, Key: it.Key})
		}
		if !r && w.storesIntoHolder(&fd.Sig, v) {
			r = true
		}
	}
	for _, id := range m.Items {
		it := w.items.Get(id)
		switch it.Kind {
		case types.KindFunc:
			judge(id)
		case types.KindImpl:
			if im, ok := it.Decl.(*types.ImplDecl); ok {
				for _, mid := range im.Methods {
					judge(mid)
				}
			}
		case types.KindConst, types.KindGlobal:
			// A const or global of a fn type that declares such a store is a
			// handle to one (`const KeepC: fn(v: &V, ... into v) = Keep`). A
			// foreign body counts only an exported one, as m's API hash
			// carries it (APIHash hashes an exported const or global's Decl).
			if foreign && !it.Exported {
				continue
			}
			var ct types.TypeID
			switch d := it.Decl.(type) {
			case *types.ConstDecl:
				ct = d.Type
			case *types.GlobalDecl:
				ct = d.Type
			}
			if ct == types.NoType {
				continue
			}
			if w.reads != nil {
				w.reads.Add(types.Read{Kind: types.ReadSignature, Key: it.Key})
			}
			if !r && w.fnStoresInto(ct, v, map[types.TypeID]bool{}, w.newRecur()) {
				r = true
			}
		case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface:
			// The type's member namespace (iface.TypeNamespaceKey): a
			// method added to it is a function of m.
			if w.reads != nil {
				w.reads.Add(types.Read{Kind: types.ReadExports, Key: types.KeyFromString(it.Key.String() + ":")})
			}
		}
	}
	w.holds[v] = r
	return r
}

// storesIntoHolder reports whether sig declares a store, by a parameter's
// `into` or a `*q into p` clause, into a parameter whose type holds view
// item v (holdsItem), or names a fn type, in a parameter or the result, that
// declares one (fnStoresInto). The receiver's own store into self is
// keepScan.method's, and holdStores runs only when none was found.
func (w *walker) storesIntoHolder(sig *types.Signature, v types.ItemID) bool {
	ps := make([]types.TypeID, len(sig.Params))
	into := make([]types.ParamIndex, len(sig.Params))
	for i, p := range sig.Params {
		ps[i], into[i] = p.Type, p.Into
	}
	if w.declaresHolderStore(ps, into, sig.Flows.Stores, v) {
		return true
	}
	seen, rc := map[types.TypeID]bool{}, w.newRecur()
	for _, t := range append(ps, sig.Result) {
		if w.fnStoresInto(t, v, seen, rc) {
			return true
		}
	}
	return false
}

// implFor is an impl's For with one reference stripped: an impl for &V or
// mut V is read as one for V, as its methods' receiver reaches V's
// storage just as an impl for V's does.
func (w *walker) implFor(t types.TypeID) types.TypeID {
	if k := w.tab.Kind(t); k == types.KRef || k == types.KMut {
		return w.tab.Get(t).Elem
	}
	return t
}

// implArgs binds impl im's generics by matching its For (implFor) against
// the view type t: For's parameter at a position binds t's argument there, and a
// type parameter of the body in t matches anything, since an instance may
// be any type. ok is false when For names another instance (`impl ... for
// V[int]` and t = V[&i32]). A parameter For does not bind stays itself and
// is marked free.
func (w *walker) implArgs(im *types.ImplDecl, t *types.Type) (args []types.GenericArg, free []bool, ok bool) {
	n := len(im.Generics)
	args = make([]types.GenericArg, n)
	set := make([]bool, n)
	ft := w.tab.Get(w.implFor(im.For))
	if ft.Item != t.Item || len(ft.Args) != len(t.Args) {
		return nil, nil, false
	}
	for i, a := range ft.Args {
		if !w.bindArg(a, t.Args[i], args, set) {
			return nil, nil, false
		}
	}
	for i := range set {
		if set[i] {
			continue
		}
		if free == nil {
			free = make([]bool, n)
		}
		free[i] = true
		if im.Generics[i].Const {
			args[i] = types.ConstGenericArg(types.ConstArg{Kind: types.CAParam, Param: uint32(i)})
		} else {
			args[i] = types.TypeArg(w.tab.Param(uint32(i)))
		}
	}
	return args, free, true
}

// fnResultSupplied reports whether a parameter of fn type t holds its result
// type at every instance: the parameter's type is the result's, or holds it
// under references, slices, optionals, arrays or the fields of a struct,
// record or enum that is no view (`fn(T): T`, `fn(x: &T): &T`,
// `fn(b: &Bx[T]): T`). A function given as such a type has a parameter of
// that type too (E0608 compares types equal but for flows), which the
// automatic flows make a source of its result wherever the result can hold a
// loan, so its own boundary refuses a static (E0602): its result is trusted
// as its declared sources say. The automatic flows read visible fields only,
// so a view, and an exported struct with a private field, is not walked
// into: a function outside its module may not see the field and so name no
// source (`export struct Bx[T: Plain] { v: T }`, whose v E0410 does not make
// a view). An unexported struct is named only in its own module, where its fields are visible. A phantom
// argument (`&Wrap[T]` holding no T) supplies nothing. A `fn(T): T` at T =
// &Heap or &Alloc gives G no automatic source (the automatic flows skip an
// allocator handle), but no static Heap or Alloc exists to return (a global's
// reference is E0901, and neither has a constructor a global can call).
func (w *walker) fnResultSupplied(t types.TypeID) bool {
	ty := w.tab.Get(t)
	if ty.Kind != types.KFn || ty.Fn == nil {
		return false
	}
	r := ty.Fn.Result
	seen := map[types.TypeID]bool{}
	var holds func(x types.TypeID) bool
	holds = func(x types.TypeID) bool {
		if x == r {
			return true
		}
		if seen[x] {
			return false
		}
		seen[x] = true
		xt := w.tab.Get(x)
		switch xt.Kind {
		case types.KRef, types.KMut, types.KSlice, types.KMutSlice:
			return holds(xt.Elem)
		case types.KParam, types.KSelf, types.KFn, types.KRawPtr, types.KStr:
			return false
		}
		if xt.Kind == types.KNamed && (w.factsOf(x).View || w.hiddenField(xt.Item)) {
			return false
		}
		for _, f := range w.inlineFields(x) {
			if holds(f) {
				return true
			}
		}
		return false
	}
	for _, p := range ty.Fn.Params {
		if holds(p.Type) {
			return true
		}
	}
	return false
}

// hiddenField reports whether item is an exported struct with a field that
// is not exported (fnResultSupplied). It records the declaration read.
func (w *walker) hiddenField(item types.ItemID) bool {
	if w.items == nil || item == types.NoItem {
		return true
	}
	it := w.items.Get(item)
	d, ok := it.Decl.(*types.StructDecl)
	if !ok || !it.Exported {
		return false
	}
	if w.reads != nil {
		w.reads.Add(types.Read{Kind: types.ReadFields, Key: it.Key, Name: "all"})
	}
	for _, f := range d.Fields {
		if !f.Exported {
			return true
		}
	}
	return false
}

// paramFrom reports whether t mentions Self or a type parameter at index n
// or above: with n = 0 any, with a type's argument count n a method's own.
func (w *walker) paramFrom(t types.TypeID, n int) bool {
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		return int(ty.Index) >= n
	case types.KSelf, types.KInfer:
		return true
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KOptional, types.KArray, types.KErrorUnion, types.KRawPtr:
		return w.paramFrom(ty.Elem, n)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && w.paramFrom(a.Type, n) {
				return true
			}
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			if w.paramFrom(f.Type, n) {
				return true
			}
		}
	case types.KFn:
		for _, p := range ty.Fn.Params {
			if w.paramFrom(p.Type, n) {
				return true
			}
		}
		return w.paramFrom(ty.Fn.Result, n)
	}
	return false
}

// mayWritable reports whether t is writable by type at some instance: it
// is, or it is a type parameter or Self, or it holds one inline (S[T] with
// a field of type T is writable at T = Cell[...]).
func (w *walker) mayWritable(t types.TypeID, seen map[types.TypeID]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if k := w.tab.Kind(t); k == types.KParam || k == types.KSelf {
		// Every instance counts, a Plain one too: the conservative reading,
		// as for firstTakes.
		return true
	}
	if w.writableByType(t) {
		return true
	}
	for _, f := range w.inlineFields(t) {
		if w.mayWritable(f, seen) {
			return true
		}
	}
	return false
}

// holdsRaw reports whether t is a raw pointer or holds one inline (a
// reference made from it holds nothing, and its author ties it down). Io is
// not counted: it holds its table's raw pointer inline, but it is
// compiler-known, supplies nothing to the automatic flows and makes
// no reference in safe code, so a function taking one is no raw helper.
func (w *walker) holdsRaw(t types.TypeID, seen map[types.TypeID]bool) bool {
	if w.tab.Kind(t) == types.KRawPtr {
		return true
	}
	if seen[t] || w.isKnown(t, func(k *types.KnownItems) types.ItemID { return k.Io }) {
		return false
	}
	seen[t] = true
	for _, f := range w.inlineFields(t) {
		if w.holdsRaw(f, seen) {
			return true
		}
	}
	return false
}

// rawParam reports whether some parameter type of ps holds a raw pointer.
func (w *walker) rawParam(ps []types.TypeID) bool {
	seen := map[types.TypeID]bool{}
	for _, p := range ps {
		if w.holdsRaw(p, seen) {
			return true
		}
	}
	return false
}

// walkExtra reports whether the direct write rule walks on from a root of type
// t that is no view: a write through a place that holds a loan of such a root
// reaches past it, as the writable walk does at a call, because a reference
// stored directly in t's bytes keeps writing possible. That is an exclusive
// reference (mut U, mut []U) at any depth, a type parameter or Self (judged
// before substitution, an instance may be either), or a shared reference whose
// referent is writable by type and no allocator: a Cell, Mutex or Chan, or a
// view, through which a guard or a Shared clone writes (&Shared[Mutex[?&i32]]
// and &Box[Mutex[?&i32]] held in a struct). A shared reference to anything
// else is followed into its referent's own references. Memoized at the top
// level; a type met again in one walk adds nothing (a cycle reaches nothing
// new). On a polymorphically recursive type (struct G[T] { next: ?&G[?T] })
// the walk stops at recur's bound and answers as an ANY step would, that it
// walks on.
func (w *walker) walkExtra(t types.TypeID) bool {
	if t == types.NoType {
		return false
	}
	if b, ok := w.walk[t]; ok {
		return b == memoTrue
	}
	r := w.walkFrom(t, map[types.TypeID]bool{}, w.newRecur())
	w.walk[t] = bitOf(r)
	return r
}

func (w *walker) walkFrom(t types.TypeID, seen map[types.TypeID]bool, r *recur) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if stop, leave := r.enter(t); stop {
		return true
	} else if leave != nil {
		defer leave()
	}
	for _, s := range w.refSteps(t).steps {
		if s.excl || s.any {
			return true
		}
		if w.factsOf(s.referent).Allocator {
			continue
		}
		if w.writableByType(s.referent) {
			return true
		}
		if seen[s.referent] {
			continue
		}
		if w.walkFrom(s.referent, seen, r) {
			return true
		}
	}
	return false
}

// holdsMutexNonPlain reports whether some type in inline(t) is Mutex[T] with
// T not plain (a shared borrow of it is an E0411 site).
func (w *walker) holdsMutexNonPlain(t types.TypeID) bool {
	switch w.mutexNP[t] {
	case memoTrue:
		return true
	case memoFalse, memoBusy:
		return false
	}
	w.mutexNP[t] = memoBusy
	r := false
	if w.isKnown(t, func(k *types.KnownItems) types.ItemID { return k.Mutex }) {
		ty := w.tab.Get(t)
		if len(ty.Args) == 1 && !ty.Args[0].IsConst && !w.plain(ty.Args[0].Type) {
			r = true
		}
	}
	if !r {
		for _, f := range w.inlineFields(t) {
			if w.holdsMutexNonPlain(f) {
				r = true
				break
			}
		}
	}
	w.mutexNP[t] = bitOf(r)
	return r
}

func bitOf(b bool) memoBit {
	if b {
		return memoTrue
	}
	return memoFalse
}

// refSteps is the references stored directly in t's bytes, as (mode,
// referent): &U and []U give (shared, U); str gives (shared, u8); mut U and
// mut []U give (exclusive, U); the walk recurses through [N], ?, E!, records,
// struct fields and enum payloads; a view struct, a KParam or KSelf adds
// (exclusive, ANY), and a view still recurses its fields.
func (w *walker) refSteps(t types.TypeID) *refInfo {
	if r, ok := w.refs[t]; ok {
		return r
	}
	r := &refInfo{}
	w.refs[t] = r // in progress: a recursive occurrence adds nothing more
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef, types.KSlice:
		r.add(refStep{referent: ty.Elem})
	case types.KStr:
		r.add(refStep{referent: types.U8})
	case types.KMut, types.KMutSlice:
		r.add(refStep{excl: true, referent: ty.Elem})
	case types.KParam, types.KSelf:
		r.add(refStep{excl: true, any: true})
	default:
		if ty.Kind == types.KNamed && w.factsOf(t).View {
			r.add(refStep{excl: true, any: true})
		}
		for _, f := range w.inlineFields(t) {
			for _, s := range w.refSteps(f).steps {
				r.add(s)
			}
		}
	}
	return r
}

func (r *refInfo) add(s refStep) {
	for _, o := range r.steps {
		if o == s {
			return
		}
	}
	r.steps = append(r.steps, s)
}

// byValueParam reports a by-value parameter: one whose declared type is not
// &U, mut U, []U, mut []U, str or fn.
func (w *walker) byValueParam(t types.TypeID) bool {
	switch w.tab.Kind(t) {
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KStr, types.KFn:
		return false
	}
	return true
}
