// Why: a generic type's layout depends on its arguments, and a type that
// contains itself by value has no layout at all; both are settled per
// declaration, before any body is checked.
//
// What: shapePass, termOf (a type's shape term over its item's parameters),
// marks (the term marks of compiler-known items), layoutCycle (E0907) and
// identity and declGenerics.
//
// How: nested generics are inlined into the term; a closed part is a leaf,
// and a part holding a parameter only behind a pointer is a leaf of the
// pointer's size. E0907 is reported at the first declaration of a cycle.

package check

import (
	"mk/internal/diag"
	"mk/internal/types"
)

// maxByValueDepth bounds the by-value nesting of named types the cycle walk
// follows: a type that keeps growing (struct S[T] { x: ?S[[1]T] }) never
// repeats exactly, yet is infinitely large. A proposed, unmeasured limit;
// past it the walk reports E0907 as a cycle.
const maxByValueDepth = 64

// identity is the generic arguments Param(0)..Param(n-1) of a declaration.
func (c *Checker) identity(gens []types.GenericParam) []types.GenericArg {
	as := make([]types.GenericArg, len(gens))
	for i, g := range gens {
		if g.Const {
			as[i] = types.ConstGenericArg(types.ConstArg{Kind: types.CAParam, Param: uint32(i)})
		} else {
			as[i] = types.TypeArg(c.cfg.Types.Param(uint32(i)))
		}
	}
	return as
}

// declGenerics returns a struct's or enum's generics and whether it is one.
func declGenerics(d types.Decl) ([]types.GenericParam, bool) {
	switch d := d.(type) {
	case *types.StructDecl:
		return d.Generics, true
	case *types.EnumDecl:
		return d.Generics, true
	}
	return nil, false
}

// layoutCycle walks the by-value parts of item's identity instance, type
// parameters opaque, and returns the items of the path back to it, start
// first, or nil when its layout is finite.
func (c *Checker) layoutCycle(item types.ItemID) []types.ItemID {
	it := c.cfg.Items.Get(item)
	gens, ok := declGenerics(it.Decl)
	if !ok {
		return nil
	}
	start := c.cfg.Types.Named(item, c.identity(gens))
	var stack []types.TypeID
	var items []types.ItemID
	var found []types.ItemID
	var walk func(t types.TypeID) bool
	walk = func(t types.TypeID) bool {
		ty := c.cfg.Types.Get(t)
		switch ty.Kind {
		case types.KArray, types.KOptional:
			return walk(ty.Elem)
		case types.KErrorUnion:
			return walk(ty.Elem)
		case types.KRecord:
			for _, f := range ty.Fields {
				if walk(f.Type) {
					return true
				}
			}
		case types.KNamed:
			if t == start || len(items) >= maxByValueDepth {
				found = append([]types.ItemID(nil), items...)
				return true
			}
			for k, s := range stack {
				if s != t {
					continue
				}
				// An instance repeats. Its cycle is this declaration's when an
				// instance of start's item is on it: `struct Buf[const N: int]
				// { x: Buf[-1] }` holds Buf[-1], which holds itself, though
				// Buf[N] never comes back. Any other cycle is reported at its
				// own declaration.
				for _, on := range stack[k:] {
					if c.cfg.Types.Get(on).Item == item {
						found = append([]types.ItemID(nil), items...)
						return true
					}
				}
				return false
			}
			d := c.cfg.Items.Get(ty.Item).Decl
			stack, items = append(stack, t), append(items, ty.Item)
			defer func() { stack, items = stack[:len(stack)-1], items[:len(items)-1] }()
			switch d := d.(type) {
			case *types.StructDecl:
				for _, f := range d.Fields {
					if walk(c.subst(f.Type, ty.Args, types.NoType)) {
						return true
					}
				}
			case *types.EnumDecl:
				for _, v := range d.Variants {
					for _, f := range v.Fields {
						if walk(c.subst(f.Type, ty.Args, types.NoType)) {
							return true
						}
					}
				}
			}
		}
		return false
	}
	stack, items = append(stack, start), append(items, item)
	switch d := it.Decl.(type) {
	case *types.StructDecl:
		for _, f := range d.Fields {
			if walk(f.Type) {
				return found
			}
		}
	case *types.EnumDecl:
		for _, v := range d.Variants {
			for _, f := range v.Fields {
				if walk(f.Type) {
					return found
				}
			}
		}
	}
	return nil
}

// shapePass is the module's shape step, after the queued checks: for every struct and
// enum of the module, in item order, the E0907 cycle check, the shape term,
// and for a non-generic one its memoized layout. A cycle is reported once,
// at its first declaration in item order.
func (m *Module) shapePass() {
	c := m.c
	reported := map[types.ItemID]bool{}
	cyclic := map[types.ItemID]bool{}
	m.forEachItem(func(it *types.Item) {
		gens, ok := declGenerics(it.Decl)
		if !ok {
			return
		}
		if chain := c.layoutCycle(it.ID); chain != nil {
			for _, x := range chain {
				cyclic[x] = true
			}
			first := true
			for _, x := range chain {
				if reported[x] {
					first = false
				}
			}
			if first {
				for _, x := range chain {
					reported[x] = true
				}
				var names []string
				for _, x := range append(chain, chain[0]) {
					names = append(names, c.cfg.Interner.Str(c.cfg.Items.Get(x).Name))
				}
				m.Diags = append(m.Diags, diag.New(diag.E0907, it.Span, "this type contains itself by value, so it has no finite size").
					Fact("cycle", diag.TextList(names...)))
			}
			return
		}
		if cyclic[it.ID] {
			return
		}
		term := c.termOf(c.cfg.Types.Named(it.ID, c.identity(gens)))
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			d.Shape = &term
		case *types.EnumDecl:
			d.Shape = &term
		}
		if len(gens) == 0 {
			c.Layout(c.cfg.Types.Named(it.ID, nil))
		}
	})
}

// termOf is the shape term of a type over its item's parameters (nested
// generics are inlined). A closed part is a leaf; a part that holds a
// parameter only behind a pointer is a leaf of the pointer's size, with the
// facts of a pointer to an unknown non-Share type.
func (c *Checker) termOf(t types.TypeID) types.ShapeTerm {
	tab := c.cfg.Types
	if c.closed(t) {
		return types.ShapeTerm{Kind: types.TermLeaf, Leaf: c.Shape(t)}
	}
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		return types.ShapeTerm{Kind: types.TermParam, Param: ty.Index}
	case types.KArray:
		e := c.termOf(ty.Elem)
		return types.ShapeTerm{Kind: types.TermArray, Len: ty.Len, Elem: &e}
	case types.KOptional:
		e := c.termOf(ty.Elem)
		return types.ShapeTerm{Kind: types.TermOptional, Elem: &e}
	case types.KErrorUnion:
		e := c.termOf(ty.Elem)
		return types.ShapeTerm{Kind: types.TermErrorUnion, Errors: ty.Errors, Elem: &e}
	case types.KRecord:
		out := types.ShapeTerm{Kind: types.TermRecord}
		for _, f := range ty.Fields {
			out.Fields = append(out.Fields, c.termOf(f.Type))
		}
		return out
	case types.KNamed:
		it := c.cfg.Items.Get(ty.Item)
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			out := types.ShapeTerm{Kind: types.TermStruct, Packed: d.Packed, Align: d.Align, View: d.View, MutView: d.MutView, PlainView: d.PlainView, Linear: d.Linear, Marks: c.marks(it)}
			for _, f := range d.Fields {
				out.Fields = append(out.Fields, c.termOf(c.subst(f.Type, ty.Args, types.NoType)))
			}
			return out
		case *types.EnumDecl:
			out := types.ShapeTerm{Kind: types.TermEnum, Tag: d.Tag}
			for _, v := range d.Variants {
				var vs []types.ShapeTerm
				for _, f := range v.Fields {
					vs = append(vs, c.termOf(c.subst(f.Type, ty.Args, types.NoType)))
				}
				out.Variants = append(out.Variants, vs)
				out.Discrs = append(out.Discrs, v.Discr)
			}
			return out
		}
	}
	// A pointer, slice or fn over a parameter: its size does not depend on
	// the argument. Lay it out with an opaque non-Share placeholder.
	ph := make([]types.GenericArg, 0, 8)
	for i := uint32(0); i < 64; i++ {
		ph = append(ph, types.TypeArg(tab.RawPtr(types.U8)))
	}
	s := substituter{c: c, args: ph, self: tab.RawPtr(types.U8), lenient: true}
	return types.ShapeTerm{Kind: types.TermLeaf, Leaf: c.Shape(s.typ(t))}
}

// marks are the facts a struct term of a compiler-known item carries that
// its fields do not show.
func (c *Checker) marks(it *types.Item) types.TermMarks {
	k := &c.known
	is := func(id types.ItemID) bool { return id != types.NoItem && id == it.ID }
	var mk types.TermMarks
	switch {
	case is(k.Cell):
		mk |= types.MarkInterior
	case is(k.Mutex), is(k.Chan), is(k.SpinCore), is(k.Atomic):
		mk |= types.MarkNoFreeze | types.MarkInteriorOnly
	case is(k.Arena), is(k.Scope):
		mk |= types.MarkInteriorOnly
	case is(k.Alloc):
		mk |= types.MarkNoSend | types.MarkNoShare
	case is(k.System):
		mk |= types.MarkNoSend
	}
	if _, share := c.assertions(it); share != types.NoItem {
		mk |= types.MarkNoFreeze
	}
	return mk
}
