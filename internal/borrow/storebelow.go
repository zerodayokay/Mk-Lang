// Why: a local often holds a loan only by provenance: a reborrow `mut
// *h.a` holds what every reference on its path holds (R12), so a view built
// over one field of a parameter holds the parameter's own loan. The writable
// walk hops from a holder through each loan it holds, so without a look at
// the holder's type a store into the view reached the parameter itself, even
// when nothing the view's type can write below its own bytes holds a loan.
//
// What: walker.storesBelow, whether a write through a value of a type, below
// that value's own bytes, can put a loan anywhere.
//
// How: a walk of the type's own bytes. An exclusive reference to a carrier,
// a shared reference to memory that takes loan stores through `&`, a type
// parameter, Self, fn or interface value, an allocator held by value, a `mut
// view` or a loan-interior-writable type, a view type argument that holds
// loans, and a raw pointer, even behind a reference to a type that holds no
// loan (rawBehind), each answer yes. A raw pointer is opaque to the walk, so
// only two are exempt: a view's own pointer to one of its type parameters,
// whose storage the type argument stands for (R18), and, behind a shared
// step, a read-only view's own pointers (R19 makes a view that writes loans
// through `&self` a `mut view`) and the context of an Alloc or Heap, which
// takes no store through a reference. Behind a shared step only memory
// that takes loan stores through `&`, raw pointers and opaque types answer
// yes, as in the writable walk. The walk keeps a seen set per query, stops
// at recur's bound on a polymorphically recursive type with the answer an
// ANY step gives, yes, and memoizes only the top-level answer, so a cycle
// never caches a partial answer.

package borrow

import "mk/internal/types"

// storesBelow reports whether a write through a value of type t, below t's
// own bytes, may store a loan: the writable walk passes no store on through
// the loans a root of such a type holds when it does not, except under
// csrConst (the self-reach and spawn walks do not trust a holder's type). A
// type whose answer is no holds no raw pointer but the exempt ones (above),
// so a store can reach memory only through its references, and every one of
// them leads to memory with no carrier slot, or past a shared step to memory
// that takes no store through `&`.
func (w *walker) storesBelow(t types.TypeID) bool {
	if b, ok := w.below[t]; ok {
		return b
	}
	r := w.belowWalk(t, false, map[belowKey]bool{}, w.newRecur())
	w.below[t] = r
	return r
}

type belowKey struct {
	t      types.TypeID
	shared bool
}

// belowWalk walks t's own bytes. shared is set once the walk has stepped
// through a shared reference: from there only memory that takes loan stores
// through `&` can be written, and what raw pointers reach.
func (w *walker) belowWalk(t types.TypeID, shared bool, seen map[belowKey]bool, r *recur) bool {
	if t == types.NoType || t == types.Void {
		return false
	}
	k := belowKey{t, shared}
	if seen[k] {
		return false
	}
	seen[k] = true
	if stop, leave := r.enter(t); stop {
		return true // past the bound, as an ANY step answers (recur.go)
	} else if leave != nil {
		defer leave()
	}
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KParam, types.KSelf, types.KInfer:
		return true
	case types.KFn:
		// A fn value in a holder's own bytes may be replaced by one that
		// holds loans; behind a shared step it cannot be written, and a call
		// stores only what its flows declare.
		return !shared
	case types.KRawPtr:
		// A raw pointer holds no loans (R19), but the walk cannot see what
		// it reaches, and a loan its holder holds by a signature (`from n`)
		// may cover that memory. A view's own pointers are judged by
		// viewBelow instead.
		return true
	case types.KStr:
		return false
	case types.KMut, types.KMutSlice:
		if !w.carrier(ty.Elem) {
			return w.rawBehind(ty.Elem)
		}
		return !shared || w.belowWalk(ty.Elem, true, seen, r)
	case types.KRef, types.KSlice:
		if !w.carrier(ty.Elem) {
			return w.rawBehind(ty.Elem)
		}
		return w.writableByType(ty.Elem) || w.belowWalk(ty.Elem, true, seen, r)
	case types.KNamed:
		if w.isInterfaceType(t) {
			return true
		}
		f := w.factsOf(t)
		if f.MutView || f.LoanInterior {
			return true
		}
		if f.Allocator {
			// An Arena is a mut view, above. Alloc and Heap may be replaced
			// by value; behind `&` they take no loan store, whatever their
			// context pointer reaches.
			return !shared
		}
		if f.View {
			if w.viewBelow(t, ty, shared, seen, r) {
				return true
			}
			for _, ft := range w.inlineFields(t) {
				if w.tab.Kind(ft) != types.KRawPtr && w.belowWalk(ft, shared, seen, r) {
					return true
				}
			}
			return false
		}
	}
	for _, ft := range w.inlineFields(t) {
		if w.belowWalk(ft, shared, seen, r) {
			return true
		}
	}
	return false
}

// rawBehind reports whether a referent that is no carrier still answers yes:
// it holds no loan, but a raw pointer in its bytes (holdsRaw) reaches what
// the walk cannot see. An allocator's context pointer does not count (an
// Alloc or Heap takes no store through a reference).
func (w *walker) rawBehind(t types.TypeID) bool {
	return !w.factsOf(t).Allocator && w.holdsRaw(t, map[types.TypeID]bool{})
}

// viewBelow is the part of a read-only or plain view that its fields do not
// show: its storage holds values of its type arguments (R18), so a type
// argument that is a carrier, or through which a store can be written,
// answers yes (behind a shared step, only one through which a store can be
// written). Its own raw-pointer fields are judged here: one whose declared
// pointee is a type parameter points at that storage, and any other may
// point at anything, so it answers yes. Behind a shared step none does: a
// view whose code writes loans through `&self` behind raw pointers must be a
// `mut view` (R19).
func (w *walker) viewBelow(t types.TypeID, ty *types.Type, shared bool, seen map[belowKey]bool, r *recur) bool {
	for _, a := range ty.Args {
		if a.IsConst {
			continue
		}
		if !shared && w.carrier(a.Type) || w.belowWalk(a.Type, shared, seen, r) {
			return true
		}
	}
	if shared {
		return false
	}
	if w.items == nil || ty.Item == types.NoItem {
		return true // its fields cannot be read
	}
	_ = w.inlineFields(t) // records the declaration read
	d, ok := w.items.Get(ty.Item).Decl.(*types.StructDecl)
	if !ok {
		return true
	}
	for _, fd := range d.Fields {
		if p := w.tab.Get(fd.Type); p.Kind == types.KRawPtr && w.tab.Kind(p.Elem) != types.KParam {
			return true
		}
	}
	return false
}
