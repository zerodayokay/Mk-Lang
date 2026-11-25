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
// parameter, Self, fn or interface value, an allocator, a `mut view` or a
// loan-interior-writable type, a view type argument that holds loans, and a
// view's raw pointer to anything but its type arguments each answer yes.
// Behind a shared step only loan-interior-writable memory and opaque types
// do, as in the writable walk. The walk keeps a seen set per query and only
// the top-level answer is memoized, so a cycle never caches a partial answer.

package borrow

import "mk/internal/types"

// storesBelow reports whether a write through a value of type t, below t's
// own bytes, may store a loan: the writable walk passes no store on through
// the loans a root of such a type holds when it does not. A type whose
// answer is no is one through which no checked body, and so (R19) no unsafe
// body with the same signatures, can reach a loan-holding slot: every
// reference in it leads to memory with no carrier slot, or past a shared
// step to memory that takes no store through `&`.
func (w *walker) storesBelow(t types.TypeID) bool {
	if b, ok := w.below[t]; ok {
		return b
	}
	r := w.belowWalk(t, false, map[belowKey]bool{})
	w.below[t] = r
	return r
}

type belowKey struct {
	t      types.TypeID
	shared bool
}

// belowWalk walks t's own bytes. shared is set once the walk has stepped
// through a shared reference: from there only memory that takes loan stores
// through `&` can be written.
func (w *walker) belowWalk(t types.TypeID, shared bool, seen map[belowKey]bool) bool {
	if t == types.NoType || t == types.Void {
		return false
	}
	k := belowKey{t, shared}
	if seen[k] {
		return false
	}
	seen[k] = true
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
		// A raw pointer holds no loans (R19); a view's raw pointers are
		// judged by the view below.
		return false
	case types.KStr:
		return false
	case types.KMut, types.KMutSlice:
		if shared {
			return w.carrier(ty.Elem) && w.belowWalk(ty.Elem, true, seen)
		}
		return w.carrier(ty.Elem)
	case types.KRef, types.KSlice:
		return w.carrier(ty.Elem) && (w.writableByType(ty.Elem) || w.belowWalk(ty.Elem, true, seen))
	case types.KNamed:
		if w.isInterfaceType(t) {
			return true
		}
		f := w.factsOf(t)
		if f.MutView || f.LoanInterior {
			return true
		}
		if f.Allocator && !shared {
			return true
		}
		if f.View && w.viewBelow(t, ty, shared, seen) {
			return true
		}
	}
	for _, ft := range w.inlineFields(t) {
		if w.belowWalk(ft, shared, seen) {
			return true
		}
	}
	return false
}

// viewBelow is the part of a read-only or plain view that its fields do not
// show: its storage holds values of its type arguments (R18), so a type
// argument that is a carrier answers yes (behind a shared step, one that
// takes loan stores through `&`); and a raw-pointer field whose declared
// pointee mentions none of the view's type parameters erases what it points
// at, so in the view's own bytes it answers yes.
func (w *walker) viewBelow(t types.TypeID, ty *types.Type, shared bool, seen map[belowKey]bool) bool {
	for _, a := range ty.Args {
		if a.IsConst || !w.carrier(a.Type) {
			continue
		}
		if !shared || w.writableByType(a.Type) || w.belowWalk(a.Type, true, seen) {
			return true
		}
	}
	if shared || w.items == nil || ty.Item == types.NoItem {
		return false
	}
	_ = w.inlineFields(t) // records the declaration read
	d, ok := w.items.Get(ty.Item).Decl.(*types.StructDecl)
	if !ok {
		return true
	}
	for _, fd := range d.Fields {
		if w.erasedRaw(fd.Type, map[types.TypeID]bool{}) {
			return true
		}
	}
	return false
}

// erasedRaw reports whether a declared field type holds, in its own bytes, a
// raw pointer whose pointee mentions no type parameter.
func (w *walker) erasedRaw(t types.TypeID, seen map[types.TypeID]bool) bool {
	if w.tab.Kind(t) == types.KRawPtr {
		return !w.paramFrom(t, 0)
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	for _, f := range w.inlineFields(t) {
		if w.erasedRaw(f, seen) {
			return true
		}
	}
	return false
}
