// Why: a loan from an argument through which nothing can be written, and
// whose referents hold no loan, cannot carry a store anywhere; treating it
// as deep makes the writable walk report stores that cannot happen.
//
// What: walker.shallow (the shallow predicate), opaqueKind and
// opaqueInline.
//
// How: a structural test over the type's inline references (refSteps):
// every one shared, not ANY, and to a type that is no carrier, not writable
// by type, no allocator and not opaque. The call rule marks a loan shallow
// when its argument has such a type (marks.go), and the writable walk does
// not go on through a shallow loan.

package borrow

import "mk/internal/types"

// shallow reports whether a value of type t is shallow: every
// reference inline in t (refSteps: through [N], ?, E!, records, struct
// fields and enum payloads) is shared and not ANY, and refers to a type that
// is no carrier, not writable by type, no allocator and not opaque; and no
// fn, type parameter or Self is inline in t.
//
// So str, []u8, &i32, ?str, [N]str, E!str, a struct or record of shallow
// fields, and a type with no reference at all are shallow. A mut reference,
// a view (ANY, by value or inline), a type parameter or Self (ANY), a fn
// inline (a closure's captures are not in refSteps), and a shared reference
// to a carrier (&str, &[]u8, &List[T]), to a type writable by type (&Cell,
// &Mutex, a struct holding one inline), to an allocator (&Arena, &Alloc,
// &Heap) or to an opaque type (&T, &fn) are not.
//
// A raw pointer is no reference here, as in refSteps: it holds no loan, and
// writing through it is unsafe code's own obligation. An inference variable
// or the poison type never reaches borrow; either is not shallow.
//
// The result is a property of t and the Env's facts alone. refSteps,
// inlineFields and factsOf memoize and record the reads a walk makes; this
// adds no state of its own.
func (w *walker) shallow(t types.TypeID) bool {
	// Nothing writable by type or a view inline: a shared path to a writable
	// holder could store a new reference, and a view's hidden backing storage
	// (such as HeapList's *Alloc) carries provenance loans that stay deep.
	if w.writableByType(t) || w.holdsView(t) || w.opaqueInline(t, map[types.TypeID]bool{}) {
		return false
	}
	for _, s := range w.refSteps(t).steps {
		if s.excl || s.any {
			return false
		}
		r := s.referent
		if w.opaqueKind(r) || w.carrier(r) || w.writableByType(r) || w.factsOf(r).Allocator {
			return false
		}
	}
	return true
}

// opaqueKind reports whether t is a type whose contents no walk shows: a
// fn (its captures), a type parameter or Self (any instance), a named type
// whose declaration borrow cannot read (inlineFields would show no field),
// or a kind that never reaches borrow (an inference variable, poison).
// Scope[E] is read from its known facts, not from a declaration.
func (w *walker) opaqueKind(t types.TypeID) bool {
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KFn, types.KParam, types.KSelf, types.KInfer, types.KPoison:
		return true
	case types.KNamed:
		if w.known != nil && w.known.Scope != types.NoItem && ty.Item == w.known.Scope {
			return false
		}
		return w.items == nil || ty.Item == types.NoItem || w.items.Get(ty.Item).Decl == nil
	}
	return false
}

// opaqueInline reports whether t, or a type inline in it, is opaque. A
// recursive type terminates: a type met again adds nothing.
func (w *walker) opaqueInline(t types.TypeID, seen map[types.TypeID]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if w.opaqueKind(t) {
		return true
	}
	for _, f := range w.inlineFields(t) {
		if w.opaqueInline(f, seen) {
			return true
		}
	}
	return false
}
