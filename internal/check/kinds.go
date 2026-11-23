// Why: the memory model sorts every type by facts (its kind: Plain, Unique
// or Linear; whether it carries a loan; whether it may cross threads), and
// every bound, move and borrow check reads them.
//
// What: Facts and FactsIn (the facts of a type: kind, carrier, view,
// allocator, interior, SliceImpl, Send, Share, Freeze, IsFn), Env and
// Narrow (a unit's generic environment), factsWalk, isCarrier and
// assertionHolds.
//
// How: a structural walk that combines parts into the whole (the worst
// kind, any carrier, every part Send, Share and Freeze), with identity rules
// for compiler-known types (Cell, Mutex, Chan, Atomic, Scope; Io, Heap and
// Alloc are handles) and unsafe impl assertions. Recursive types get a
// coinductive answer; polymorphically recursive ones are cut by recurs.
// Poison satisfies every fact.

package check

import (
	"math"
	"slices"

	"mk/internal/source"
	"mk/internal/types"
)

// Facts returns the facts of a closed type (no KParam, KSelf or KInfer),
// memoized per TypeID; safe for concurrent use. ICE on a type that is not
// closed or that reaches a constructor whose module has not finished its
// step 3 (impls).
func (c *Checker) Facts(t types.TypeID) types.Facts {
	if p := c.memo.facts(t); p != nil {
		return *p
	}
	if !c.closed(t) {
		source.ICEf(source.Span{}, "check.Facts: %s mentions a type parameter; use FactsIn", c.cfg.Types.TypeString(t, nil))
	}
	w := factsWalk{c: c}
	f, _ := w.facts(t)
	return f
}

// Env is the generic environment of one unit: the list KParam indexes
// (Signature.Generics, then Body.Hidden), with the narrowing of an enclosing
// `if T is X`. It memoizes the answers for types that mention a parameter,
// for that unit only. Not safe for concurrent use.
type Env struct {
	gen []types.GenericParam
	// narrow[i] is the type parameter i is narrowed to, NoType for none.
	narrow []types.TypeID
	memo   map[types.TypeID]types.Facts
	// view is the module whose point of view impl lookups for builtin
	// constructors take (bounds.go): its own and its imports' std modules.
	// Nil for a viewer-less query, which sees only finished modules.
	view *Module
}

// NewEnv makes the environment of a unit whose KParam i means gen[i].
func (c *Checker) NewEnv(gen []types.GenericParam) *Env {
	return &Env{gen: gen, narrow: make([]types.TypeID, len(gen)), memo: map[types.TypeID]types.Facts{}}
}

// Narrow returns a copy of env in which parameter i is narrowed to t (the
// then-branch of `if T is X`); the copy has its own memo.
func (e *Env) Narrow(i uint32, t types.TypeID) *Env {
	n := &Env{gen: e.gen, narrow: append([]types.TypeID(nil), e.narrow...), memo: map[types.TypeID]types.Facts{}, view: e.view}
	n.narrow[i] = t
	return n
}

// FactsIn answers for any type of the unit: a type parameter answers from
// its bounds in env (unbounded: Linear, carrier, not Send, not Share, not
// Freeze), Self as Linear and a carrier, closed parts through Facts.
func (c *Checker) FactsIn(env *Env, t types.TypeID) types.Facts {
	if c.closed(t) {
		return c.Facts(t)
	}
	if f, ok := env.memo[t]; ok {
		return f
	}
	w := factsWalk{c: c, env: env}
	f, _ := w.facts(t)
	return f
}

// noLow is the "no in-progress type was assumed" depth.
const noLow = math.MaxInt

// assumed is the answer for a type met again while its own facts are being
// computed: the coinductive answer for Send and Share through references
// (struct Node { next: ?&Node }); a by-value cycle is E0907, and
// these values only keep the walk finite.
var assumed = types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}

// factsWalk is one structural recursion over a type. An answer that
// relied on the assumption about a type still on the stack below it is not
// memoized: only the cycle's root is, and the others are recomputed from it
// when asked on their own.
type factsWalk struct {
	c     *Checker
	env   *Env
	stack []frame
}

// frame is one type on the walk's stack. A named type keeps the facts of
// its type arguments, taken before it was pushed, for recurs to compare: a
// later instance of the same constructor is compared with them, never by
// walking the entry's arguments again.
type frame struct {
	t    types.TypeID
	args []types.Facts
	// low is the least stack depth the answers in args assumed.
	low int
}

func (w *factsWalk) facts(t types.TypeID) (types.Facts, int) {
	if f, ok := w.memoized(t); ok {
		return f, noLow
	}
	for d, s := range w.stack {
		if s.t == t {
			return assumed, d
		}
	}
	// A named type's arguments are walked first, as the subterms they are,
	// so each instance of a nest is walked once, innermost first. Walking
	// them may answer t itself, through a field that names it.
	ty := w.c.cfg.Types.Get(t)
	var args []types.Facts
	argLow := noLow
	if ty.Kind == types.KNamed {
		args, argLow = w.argFacts(ty)
		if f, ok := w.memoized(t); ok {
			return f, noLow
		}
		if d, ok := w.recurs(ty, args, argLow); ok {
			return assumed, d
		}
	}
	depth := len(w.stack)
	w.stack = append(w.stack, frame{t: t, args: args, low: argLow})
	f, low := w.compute(t)
	w.stack = w.stack[:depth]
	if low < depth {
		return f, low
	}
	if w.c.closed(t) {
		return *w.c.memo.putFacts(t, f), noLow
	}
	w.env.memo[t] = f
	return f, noLow
}

// memoized is the kept answer for t: the checker's for a closed type, the
// unit's for one that mentions a parameter.
func (w *factsWalk) memoized(t types.TypeID) (types.Facts, bool) {
	c := w.c
	if c.closed(t) {
		if p := c.memo.facts(t); p != nil {
			return *p, true
		}
		return types.Facts{}, false
	}
	if w.env == nil {
		source.ICEf(source.Span{}, "check: facts of %s outside a unit environment", c.cfg.Types.TypeString(t, nil))
	}
	f, ok := w.env.memo[t]
	return f, ok
}

// maxRecur bounds how often one constructor may be on the stack: the walk
// ends there whatever the arguments (E0708's depth).
const maxRecur = 64

// recurs stops a polymorphically recursive walk: a constructor met
// again under arguments that grow at each level (struct G[T] { v: T, next:
// ?&G[?T] }) mints a new TypeID every time, so the TypeID test above never
// fires. A named type whose constructor is on the stack with arguments of
// the same facts answers as that entry would, so it is assumed as a cycle
// at that entry's depth: a constructor's facts follow from its arguments'
// facts (its fields, its identity, and its assertions' bounds, which are
// facts), and the sequence of those facts along the recursion repeats
// within the finite set of answers. maxRecur is a backstop that ends any
// walk the repetition has not ended by then.
//
// args are the facts of ty's arguments and argLow the least depth they
// assumed; an entry's are the ones its frame kept, so the comparison walks
// nothing. Walking the entry's arguments again at each comparison led back
// to the type being decided (P3 = Pair[Pair[Pair[Slot]]] under the entry
// Pair[P3]) and, even with that cut, would walk each level of a nest
// twice with nothing kept.
func (w *factsWalk) recurs(ty *types.Type, args []types.Facts, argLow int) (int, bool) {
	tab := w.c.cfg.Types
	n := 0
	for d, s := range w.stack {
		st := tab.Get(s.t)
		if st.Kind != types.KNamed || st.Item != ty.Item {
			continue
		}
		if n++; n >= maxRecur {
			return d, true
		}
		// The match rests on both argument lists' answers: an assumption
		// they made below d keeps the walk from memoizing up to there.
		if slices.Equal(args, s.args) {
			return min(d, argLow, s.low), true
		}
	}
	return 0, false
}

// argFacts are the facts of a named type's type arguments, in order, and
// the least stack depth their answers assumed; a const argument changes no
// fact and is left out.
func (w *factsWalk) argFacts(ty *types.Type) ([]types.Facts, int) {
	out := make([]types.Facts, 0, len(ty.Args))
	low := noLow
	for _, a := range ty.Args {
		if !a.IsConst {
			f, l := w.facts(a.Type)
			out = append(out, f)
			low = min(low, l)
		}
	}
	return out, low
}

// compute answers the facts of one type constructor.
func (w *factsWalk) compute(t types.TypeID) (types.Facts, int) {
	c := w.c
	ty := c.cfg.Types.Get(t)
	plain := types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}
	switch ty.Kind {
	case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KVoid, types.KNever,
		types.KErrorSet, types.KPoison:
		return plain, noLow
	case types.KStr:
		plain.Carrier = true
		return plain, noLow
	case types.KFn:
		plain.IsFn = true
		return plain, noLow
	case types.KRawPtr:
		return types.Facts{Kind: types.Plain}, noLow
	case types.KRef, types.KSlice, types.KMut, types.KMutSlice:
		// A reference is a by-value pointer: Freeze whatever it points at
		// &C is Send and Share exactly when C is Share; mut C and
		// mut []C are Send when C is Send and Share when C is Share.
		e, low := w.facts(ty.Elem)
		f := types.Facts{Kind: types.Plain, Carrier: true, Freeze: true, Send: e.Share, Share: e.Share}
		if ty.Kind == types.KMut || ty.Kind == types.KMutSlice {
			f.Kind = types.Unique
			f.Send = e.Send
		}
		return f, low
	case types.KArray, types.KOptional, types.KErrorUnion:
		e, low := w.facts(ty.Elem)
		e.Allocator, e.SliceImpl, e.IsFn, e.View, e.MutView = false, false, false, false, false
		return e, low
	case types.KRecord:
		f, low := plain, noLow
		for _, fd := range ty.Fields {
			x, l := w.facts(fd.Type)
			f = combine(f, x)
			low = min(low, l)
		}
		return f, low
	case types.KNamed:
		return w.named(t, ty)
	case types.KParam:
		return w.param(ty.Index)
	case types.KSelf:
		return types.Facts{Kind: types.Linear, Carrier: true}, noLow
	}
	source.ICEf(source.Span{}, "check: facts of a %s type", ty.Kind)
	return types.Facts{}, noLow
}

// combine is the by-value combination of a part into a whole: the worst
// kind, any carrier or interior, every part Send, Share and Freeze.
func combine(f, x types.Facts) types.Facts {
	f.Kind = max(f.Kind, x.Kind)
	f.Carrier = f.Carrier || x.Carrier
	f.Interior = f.Interior || x.Interior
	f.LoanInterior = f.LoanInterior || x.LoanInterior
	f.Send = f.Send && x.Send
	f.Share = f.Share && x.Share
	f.Freeze = f.Freeze && x.Freeze
	return f
}

// param answers a type parameter from its bounds in the unit's environment:
// Plain only through a Plain bound, Send or Share only
// through those bounds; always a carrier, never Freeze.
func (w *factsWalk) param(i uint32) (types.Facts, int) {
	if w.env == nil || int(i) >= len(w.env.gen) {
		source.ICEf(source.Span{}, "check: type parameter %d outside the unit's %d generics", i, envLen(w.env))
	}
	if n := w.env.narrow[i]; n != types.NoType {
		return w.facts(n)
	}
	f := types.Facts{Kind: types.Linear, Carrier: true}
	for _, b := range w.env.gen[i].Bounds {
		switch b.Kind {
		case types.BoundPlain:
			f.Kind = types.Plain
		case types.BoundSend:
			f.Send = true
		case types.BoundShare:
			f.Share = true
		}
	}
	return f, noLow
}

func envLen(e *Env) int {
	if e == nil {
		return 0
	}
	return len(e.gen)
}

// named is a struct or enum applied to arguments: the worst of its keyword
// floor, its identity and its instantiated fields.
func (w *factsWalk) named(t types.TypeID, ty *types.Type) (types.Facts, int) {
	c := w.c
	it := c.cfg.Items.Get(ty.Item)
	c.requireFinal(it)
	k := &c.known
	is := func(id types.ItemID) bool { return id != types.NoItem && id == it.ID }
	f, low := types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}, noLow
	field := func(ft types.TypeID) {
		x, l := w.facts(c.subst(ft, ty.Args, types.NoType))
		f = combine(f, x)
		low = min(low, l)
	}
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		for _, fd := range d.Fields {
			field(fd.Type)
		}
		if d.Linear {
			f.Kind = types.Linear
		} else if d.View {
			f.Kind = max(f.Kind, types.Unique)
		}
		f.View = d.View
		f.MutView = d.MutView
		f.Carrier = f.Carrier || d.View
		if d.MutView {
			f.Interior = true
			f.LoanInterior = true
			f.Freeze = false
		} else if d.View {
			// A plain view struct cannot accept loan stores into its own
			// storage through `&self`, but its elements (type arguments) may
			// be loan-interior-writable (e.g. `Box[Mutex[?&i32]]`,
			// `List[Cell[&i32]]`). Both true and false depend on the argument
			// facts, so any cycle assumption in fr.args must stop memoization.
			fr := &w.stack[len(w.stack)-1]
			for _, af := range fr.args {
				if af.LoanInterior {
					f.LoanInterior = true
				}
			}
			if len(fr.args) > 0 {
				low = min(low, fr.low)
			}
		}
	case *types.EnumDecl:
		for _, v := range d.Variants {
			for _, fd := range v.Fields {
				field(fd.Type)
			}
		}
	case *types.InterfaceDecl:
		// The self of a function an interface owns: any
		// implementer, so the worst answer, as for Self (KSelf).
		return types.Facts{Kind: types.Linear, Carrier: true}, noLow
	case nil:
		source.ICEf(it.Span, "check: facts of %s before its declaration is checked", it.Key)
	default:
		source.ICEf(it.Span, "check: facts of %s, a %s", it.Key, it.Kind)
	}
	// Identity: Cell, Mutex, Chan, SpinCore are Unique at least and not Freeze;
	// a Cell is never Share. LoanInterior tracks whether a shared reference can
	// store a loan: for Cell[T], Mutex[T], Chan[T] and SpinCore[T] that holds
	// iff T is a carrier; raw.Atomic never holds a carrier; Arena and Scope
	// always take loan stores. Interior and Freeze still track data mutability
	// for codegen.
	if is(k.Cell) || is(k.Mutex) || is(k.Chan) || is(k.SpinCore) {
		f.Kind = max(f.Kind, types.Unique)
		f.Freeze = false
		if len(ty.Args) > 0 && !ty.Args[0].IsConst {
			fr := &w.stack[len(w.stack)-1]
			if len(fr.args) > 0 && fr.args[0].Carrier {
				f.LoanInterior = true
			}
			low = min(low, fr.low)
		}
	}
	// std.raw.Atomic is unique by identity like Cell: an atomic has an
	// address and is never copied.
	if is(k.Atomic) {
		f.Kind = max(f.Kind, types.Unique)
		// An atomic word is Send and Share whatever its T, a raw
		// pointer included: the word moves and is
		// read only atomically, and reading through the pointer it holds
		// is unsafe code's to justify, as Rust's AtomicPtr is Send + Sync.
		f.Send, f.Share = true, true
	}
	if is(k.Cell) {
		f.Share = false
	}
	if is(k.Atomic) || is(k.Scope) {
		f.Freeze = false // a Scope is not Freeze
	}
	if is(k.Arena) || is(k.Scope) {
		f.MutView = true
		f.LoanInterior = true
		f.Freeze = false
	}
	// internal/borrow/closure.go's closureTakes mirrors the loan-interior
	// heads here (Cell, Mutex, Chan, SpinCore, Arena, Scope, plus any mut view struct).
	if is(k.Cell) || is(k.Mutex) || is(k.Chan) || is(k.SpinCore) || is(k.Arena) || is(k.Atomic) || is(k.Scope) {
		f.Interior = true
	}
	f.Allocator = is(k.Arena) || is(k.Alloc) || is(k.Heap)
	f.SliceImpl = c.sliceImpl(it)
	// Assertions: an unsafe impl Send or Share answers for the
	// instances meeting its bounds, and makes the constructor Unique; an
	// asserted Share makes it not Freeze.
	if send, share := c.assertions(it); send != types.NoItem || share != types.NoItem {
		f.Kind = max(f.Kind, types.Unique)
		if send != types.NoItem {
			ok, l := w.assertionHolds(send, ty.Args)
			f.Send, low = ok, min(low, l)
		}
		if share != types.NoItem {
			ok, l := w.assertionHolds(share, ty.Args)
			f.Share, low = ok, min(low, l)
			f.Freeze = false
		}
	}
	// Compiler-known handles: Io, Heap and Alloc.
	switch {
	case is(k.Io):
		f.Kind, f.Carrier, f.Send, f.Share = types.Plain, false, true, true
	case is(k.Heap), is(k.Scope):
		f.Send, f.Share = true, true
	case is(k.Alloc):
		// `Alloc` is unique (`&Alloc` plain), by identity whatever its
		// private fields.
		f.Kind = max(f.Kind, types.Unique)
		f.Send, f.Share = false, false
	case is(k.System):
		f.Send = false
	}
	return f, low
}

// requireFinal raises an ICE when the constructor's facts are not final yet:
// its module has not finished its step 3 (impls).
func (c *Checker) requireFinal(it *types.Item) {
	if m := c.module(it.Module); m == nil || !m.final {
		source.ICEf(it.Span, "check: facts of %s asked before its module's impls are registered (4.3.5)", it.Key)
	}
}

// assertions returns the unsafe impl Send and Share items naming the
// constructor of it, NoItem for none. Coherence keeps every
// assertion in the constructor's module.
func (c *Checker) assertions(it *types.Item) (send, share types.ItemID) {
	m := c.module(it.Module)
	if m == nil {
		return types.NoItem, types.NoItem
	}
	a := m.asserts[it.ID]
	return a.send, a.share
}

// sliceImpl reports whether the constructor has an impl Slice[T].
func (c *Checker) sliceImpl(it *types.Item) bool {
	m := c.module(it.Module)
	return m != nil && m.sliceImpls[it.ID]
}

// assertionHolds reports whether an instance with these arguments meets the
// bounds of an unsafe impl: each impl parameter is matched to the argument
// in its position of the impl's For type, and must satisfy its Plain, Send
// and Share bounds.
func (w *factsWalk) assertionHolds(impl types.ItemID, args []types.GenericArg) (bool, int) {
	d, ok := w.c.cfg.Items.Get(impl).Decl.(*types.ImplDecl)
	if !ok {
		return false, noLow
	}
	forTy := w.c.cfg.Types.Get(d.For)
	low := noLow
	for gi, g := range d.Generics {
		var arg types.TypeID
		for ai, a := range forTy.Args {
			if !a.IsConst && w.c.cfg.Types.Kind(a.Type) == types.KParam && w.c.cfg.Types.Get(a.Type).Index == uint32(gi) && ai < len(args) {
				arg = args[ai].Type
			}
		}
		if arg == types.NoType {
			continue
		}
		x, l := w.facts(arg)
		low = min(low, l)
		for _, b := range g.Bounds {
			switch {
			case b.Kind == types.BoundPlain && x.Kind != types.Plain,
				b.Kind == types.BoundSend && !x.Send,
				b.Kind == types.BoundShare && !x.Share:
				return false, low
			}
		}
	}
	return true, low
}

// isCarrier is the carrier fact computed from declarations alone,
// without the facts memo: no impl changes it, so it is final before step
// 3 (impls) and serves the flows of fn types read in step 1 and of
// signatures. It equals FactsIn(env, t).Carrier for every type of a
// signature (no narrowing applies there).
func (c *Checker) isCarrier(t types.TypeID) bool { return c.carrierSeen(t, map[types.TypeID]bool{}) }

func (c *Checker) carrierSeen(t types.TypeID, seen map[types.TypeID]bool) bool {
	ty := c.cfg.Types.Get(t)
	switch ty.Kind {
	case types.KStr, types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KParam, types.KSelf:
		return true
	case types.KArray, types.KOptional, types.KErrorUnion:
		return c.carrierSeen(ty.Elem, seen)
	case types.KRecord:
		for _, f := range ty.Fields {
			if c.carrierSeen(f.Type, seen) {
				return true
			}
		}
	case types.KNamed:
		if seen[t] {
			return false
		}
		seen[t] = true
		it := c.cfg.Items.Get(ty.Item)
		if it.ID == c.known.Io && it.ID != types.NoItem {
			return false
		}
		switch d := c.declOfItem(it).(type) {
		case *types.StructDecl:
			if d.View {
				return true
			}
			for _, f := range d.Fields {
				if c.carrierSeen(c.subst(f.Type, ty.Args, types.NoType), seen) {
					return true
				}
			}
		case *types.EnumDecl:
			for _, v := range d.Variants {
				for _, f := range v.Fields {
					if c.carrierSeen(c.subst(f.Type, ty.Args, types.NoType), seen) {
						return true
					}
				}
			}
		}
	}
	return false
}

// declOfItem is an item's declaration, declaring it first when it belongs
// to a module in the middle of stage 5 (its own goroutine is the only one
// that can reach it).
func (c *Checker) declOfItem(it *types.Item) types.Decl {
	if it.Decl == nil {
		if m := c.module(it.Module); m != nil && m.d != nil && !m.final {
			m.ensureGuarded(it)
		}
	}
	return it.Decl
}
