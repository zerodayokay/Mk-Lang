// Why: a const item lives in static memory, and a store through a reference
// into it is legal only when the memory can take one; judging by the const's
// type alone would reject consts whose value points nowhere writable.
//
// What: checker.constValue (the evaluated value of a const, with its read
// recorded), walker.constTakes and inlineTakes.
//
// How: a const's static node takes stores only when a reference in its
// evaluated value reaches memory that does. The value is a public fact
// (iface hashes it into its module's API, and the read is ReadValue), so a
// cached result follows it. The value is walked on an explicit stack.

package borrow

import "mk/internal/types"

// constValue returns the evaluated value of const item, and records the
// ReadValue its judgement rests on, when the value is settled: a
// const of another module, whose module wrote every value before its
// importers started (driver stages.go declEvals). A const of the body's own
// module has no value yet while the module's bodies are borrow-checked
// (its values are written after them, and a value check folded early
// depends on which declarations needed it), so it is judged by its type, as
// a global is.
func (c *checker) constValue(item types.ItemID) (types.ConstID, bool) {
	it := c.w.items.Get(item)
	d, ok := it.Decl.(*types.ConstDecl)
	if !ok || !d.HasValue || d.Value == types.NoConst {
		return types.NoConst, false
	}
	if c.env.Module != nil && it.Module == c.env.Module.ID {
		return types.NoConst, false
	}
	if c.w.reads != nil {
		c.w.reads.Add(types.Read{Kind: types.ReadValue, Key: it.Key})
	}
	return d.Value, true
}

// constTakes reports whether const value v may point into memory that takes a
// store of a loan, judged on the value as storesFirst judges a type: a
// relocated pointer word whose pointee is a carrier writable by type (a
// Mutex[?&i32] or mut view the initializer allocated in EvalArena), or a
// pointee that holds such a pointer, at any depth; or a mut view held inline,
// or a plain view whose type arguments are writable, in the value or in a
// pointee. Whether the const's own bytes are writable by type is staticOf's,
// as for any static. A pointer word that is none, or a slice of no element,
// points nowhere, so DefaultClientLimits (`proxy: none`, `decoders: []`) takes
// no store, though its `?&url.Url` field's type would. A function word holds
// no memory. The values are walked on an explicit stack, depth first in
// relocation order, so a deep value (a 2,000,000-node list) costs heap, not
// Go stack.
func (w *walker) constTakes(v types.ConstID) bool {
	consts := w.tab.Consts()
	seen := map[types.ConstID]bool{v: true}
	type at struct {
		id  types.ConstID
		top bool
	}
	stack := []at{{v, true}}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cv := consts.Get(x.id)
		if !x.top && w.carrier(cv.Type) && w.writableByType(cv.Type) {
			return true
		}
		if w.inlineTakes(cv.Type, map[types.TypeID]bool{}) {
			return true
		}
		for i := len(cv.Relocs) - 1; i >= 0; i-- {
			if r := cv.Relocs[i]; r.Kind == types.RelocData && !seen[r.Data] {
				seen[r.Data] = true
				stack = append(stack, at{r.Data, false})
			}
		}
	}
	return false
}

// inlineTakes is firstTakes's view rule over t's inline bytes only, behind a
// shared access (a const value of a view is never mutated by value): a mut
// view held inline, or a plain view whose type arguments are writable or take
// stores inline. The reference levels are left to the value's relocations
// (constTakes). A type parameter or Self counts; a const's type has none.
func (w *walker) inlineTakes(t types.TypeID, seen map[types.TypeID]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KRef, types.KSlice, types.KMut, types.KMutSlice, types.KStr:
		return false
	case types.KParam, types.KSelf:
		return true
	}
	if ty.Kind == types.KNamed {
		f := w.factsOf(t)
		if f.MutView {
			return true
		}
		if f.View {
			for _, a := range ty.Args {
				if !a.IsConst && w.carrier(a.Type) && (w.writableByType(a.Type) || w.inlineTakes(a.Type, seen)) {
					return true
				}
			}
		}
	}
	for _, f := range w.inlineFields(t) {
		if w.inlineTakes(f, seen) {
			return true
		}
	}
	return false
}
