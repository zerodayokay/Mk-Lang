// Why: most borrow rules depend on what a type holds: whether it carries
// loans, holds a view, can be written through a shared step, or may point
// into global memory that takes stores. These answers are properties of
// the type alone and must be computed once per body.
//
// What: walker (the memoized walks of one body), refStep and refSteps,
// factsOf, inlineFields and subst, holdsView, writableByType, the global
// memory tests (storesFirst, storesFirstGen, storesFirstBare, firstTakes,
// fnResultSupplied), walkExtra, holdsRaw, holdsMutexNonPlain and
// byValueParam.
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

	facts        map[types.TypeID]types.Facts
	fields       map[types.TypeID][]types.TypeID
	view         map[types.TypeID]memoBit
	nonPlainView map[types.TypeID]memoBit
	mutexNP      map[types.TypeID]memoBit
	walk         map[types.TypeID]memoBit
	// walkExact holds walkFrom's answers that hold in every walk: t's walk
	// met no type recur counts and every successor walked was itself exact
	// (answered here or by an exact walk), so the answer is plain
	// reachability and skipping t's closure changes no other walk's bound.
	walkExact map[types.TypeID]bool
	// walkCalls counts walkFrom calls (read by tests).
	walkCalls  int
	refs       map[types.TypeID]*refInfo
	splitMerge map[types.TypeID][][]bool
	readDecl   map[types.ItemID]bool
	// below holds storesBelow's answers (storebelow.go).
	below map[types.TypeID]bool
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
		tab:          env.Types,
		known:        env.Known,
		envF:         env.Facts,
		reads:        reads,
		facts:        make(map[types.TypeID]types.Facts),
		fields:       make(map[types.TypeID][]types.TypeID),
		view:         make(map[types.TypeID]memoBit),
		nonPlainView: make(map[types.TypeID]memoBit),
		mutexNP:      make(map[types.TypeID]memoBit),
		walk:         make(map[types.TypeID]memoBit),
		walkExact:    make(map[types.TypeID]bool),
		refs:         make(map[types.TypeID]*refInfo),
		splitMerge:   make(map[types.TypeID][][]bool),
		readDecl:     make(map[types.ItemID]bool),
		below:        make(map[types.TypeID]bool),
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
		// An interface value shows no fields, so nothing is read.
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

// holdsNonPlainView reports whether some type in inline(t) is a non-plain view
// (`view struct` or `mut view struct`, excluding `plain view struct`).
func (w *walker) holdsNonPlainView(t types.TypeID) bool {
	switch w.nonPlainView[t] {
	case memoTrue:
		return true
	case memoFalse, memoBusy:
		return false
	}
	w.nonPlainView[t] = memoBusy
	f := w.factsOf(t)
	r := f.View && !f.PlainView
	if !r {
		for _, fd := range w.inlineFields(t) {
			if w.holdsNonPlainView(fd) {
				r = true
				break
			}
		}
	}
	w.nonPlainView[t] = bitOf(r)
	return r
}

// writableByType reports whether stepping into t through a shared reference
// restores loan-store writability: t is or holds inline a `mut view struct`,
// Arena, Scope, or a Cell/Mutex/Chan whose payload is a carrier.
func (w *walker) writableByType(t types.TypeID) bool {
	return w.factsOf(t).LoanInterior
}

// storesFirst reports whether a value of t may point into global memory that
// takes a store of a loan: some reference stored in t's bytes refers to a
// carrier that a step into restores writing (writableByType: a mut view,
// Arena, Scope, or carrier Cell/Mutex/Chan), or to a carrier that itself holds
// such a reference, at any depth; or t holds a mut view inline (its hidden
// storage may be global and interior-writable through `&`), or t holds a plain
// view by value whose type arguments hold loans (mutating it by value writes
// into its backing storage; behind a shared reference `&`, a plain view only
// counts when a type argument itself is writable or reaches writable memory).
// t's own bytes do not count unless writable or a mut view: a by-value
// Mutex[?&i32] result is the caller's own storage.
//
// Every level counts. Global memory below a first level that takes no store
// may still be written at run time: an Interior global's payload is (a global
// sync.Once[&Mutex[?&i32]] keeps the &Mutex OncePut wrote), so
// `?&&Mutex[?&i32]` read out of it counts. A type parameter or Self counts only
// in storesFirstGen and storesFirstBare: a generic body is judged once, as
// written, so what an instance may make of it is judged there.
func (w *walker) storesFirst(t types.TypeID) bool {
	return w.firstTakes(t, genNone, map[firstKey]bool{}, w.newRecur(), false)
}

// storesFirstGen is storesFirst for some instance of t: a first-level referent
// that may be writable by type once t's type parameters are substituted
// (mayWritable), a bare type parameter or Self in t's bytes (it may be a
// reference to such a type), or a view whose type arguments may hold loans.
func (w *walker) storesFirstGen(t types.TypeID) bool {
	return w.firstTakes(t, genAll, map[firstKey]bool{}, w.newRecur(), false)
}

// storesFirstBare is storesFirstGen without the referent rule: a bare type
// parameter or Self in t's bytes, or a view whose type arguments may hold
// loans, counts, but `&T` and `mut []T` count only as storesFirst counts
// them (resultGlobal's interface-method row: their `&U` is judged at the
// impl, boundaryStatic).
func (w *walker) storesFirstBare(t types.TypeID) bool {
	return w.firstTakes(t, genBare, map[firstKey]bool{}, w.newRecur(), false)
}

type genMode uint8

const (
	genNone genMode = iota
	genBare
	genAll
)

type firstKey struct {
	t      types.TypeID
	shared bool
}

// firstTakes walks t's bytes and, through a reference, its referent's.
// behindShared is true once the walk has stepped through a shared reference or
// slice, where plain (read-only) view structs cannot be mutated by value. On a
// polymorphically recursive type it answers from t's closure (closureTakes) at
// the first instance that grew when nothing below it can take a store, and
// otherwise at recur's bound.
func (w *walker) firstTakes(t types.TypeID, gen genMode, seen map[firstKey]bool, r *recur, behindShared bool) bool {
	key := firstKey{t, behindShared}
	if seen[key] {
		return false
	}
	seen[key] = true
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
	case types.KRef, types.KSlice:
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
		return w.carrier(e) && (w.writableByType(e) || w.firstTakes(e, deep, seen, r, true)) ||
			gen == genAll && w.mayWritable(e, map[types.TypeID]bool{})
	case types.KMut, types.KMutSlice:
		e := ty.Elem
		deep := gen
		if deep == genBare {
			deep = genNone
		}
		return w.carrier(e) && (w.writableByType(e) || w.firstTakes(e, deep, seen, r, false)) ||
			gen == genAll && w.mayWritable(e, map[types.TypeID]bool{})
	case types.KStr:
		return false
	case types.KParam, types.KSelf:
		// A Plain bound does not exempt it: &T, []T, str and fn are Plain,
		// so T: Plain admits &Mutex[?&i32].
		return gen != genNone
	}
	if ty.Kind == types.KNamed {
		f := w.factsOf(t)
		// Scope's 128-byte record lives inline on its owner's stack frame
		// (like by-value Mutex/Cell), never pointing into hidden global memory;
		// `&Scope` still counts via KRef -> writableByType.
		if f.MutView && !w.isKnown(t, func(k *types.KnownItems) types.ItemID { return k.Scope }) {
			return true
		}
		if f.View && !f.PlainView {
			if behindShared {
				for _, a := range ty.Args {
					if !a.IsConst && (w.carrier(a.Type) && (w.writableByType(a.Type) || w.firstTakes(a.Type, gen, seen, r, true)) ||
						gen == genAll && w.mayWritable(a.Type, map[types.TypeID]bool{})) {
						return true
					}
				}
			} else {
				for _, a := range ty.Args {
					if !a.IsConst && (w.carrier(a.Type) || gen != genNone && w.mayWritable(a.Type, map[types.TypeID]bool{})) {
						return true
					}
				}
			}
		}
	}
	for _, f := range w.inlineFields(t) {
		if w.firstTakes(f, gen, seen, r, behindShared) {
			return true
		}
	}
	return false
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
	if ty := w.tab.Get(t); ty.Kind == types.KNamed && w.factsOf(t).View && !w.factsOf(t).PlainView {
		for _, a := range ty.Args {
			if !a.IsConst && w.mayWritable(a.Type, seen) {
				return true
			}
		}
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
// level (walk) and, inside a walk, for every type whose answer holds in every
// walk (walkExact); a type met again in one walk adds nothing (a cycle reaches
// nothing new). On a polymorphically recursive type
// (struct G[T] { next: ?&G[?T] }) the walk stops at recur's bound and answers
// as an ANY step would, that it walks on.
func (w *walker) walkExtra(t types.TypeID) bool {
	if t == types.NoType {
		return false
	}
	if b, ok := w.walk[t]; ok {
		return b == memoTrue
	}
	r, _ := w.walkFrom(t, map[types.TypeID]bool{}, w.newRecur())
	w.walk[t] = bitOf(r)
	return r
}

// walkFrom is walkExtra's walk from t; exact reports that the answer is
// t's in every walk (walkExact).
func (w *walker) walkFrom(t types.TypeID, seen map[types.TypeID]bool, r *recur) (res, exact bool) {
	w.walkCalls++
	if b, ok := w.walkExact[t]; ok {
		return b, true
	}
	if seen[t] {
		return false, false
	}
	seen[t] = true
	stop, leave := r.enter(t)
	if stop {
		return true, false
	} else if leave != nil {
		defer leave()
	}
	exact = leave == nil
	for _, s := range w.refSteps(t).steps {
		if s.excl || s.any {
			res = true
			break
		}
		if w.factsOf(s.referent).Allocator {
			continue
		}
		if w.writableByType(s.referent) {
			res = true
			break
		}
		b, ex := w.walkFrom(s.referent, seen, r)
		exact = exact && ex
		if b {
			res = true
			break
		}
	}
	if exact {
		w.walkExact[t] = res
	}
	return res, exact
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
// struct fields and enum payloads; a `mut view struct`, a loan-interior-writable
// plain view, a KParam or KSelf adds (exclusive, ANY), while a read-only plain
// view adds its type arguments and their refSteps, and every view still
// recurses its fields.
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
		if ty.Kind == types.KNamed {
			f := w.factsOf(t)
			if f.MutView {
				r.add(refStep{excl: true, any: true})
			} else if f.View && !f.PlainView {
				if f.LoanInterior {
					r.add(refStep{excl: true, any: true})
				} else {
					for _, a := range ty.Args {
						if !a.IsConst {
							r.add(refStep{referent: a.Type})
							for _, s := range w.refSteps(a.Type).steps {
								r.add(s)
							}
						}
					}
				}
			}
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
