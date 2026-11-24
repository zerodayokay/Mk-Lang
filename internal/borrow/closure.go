// Why: a type walk that recurses through a polymorphically recursive type
// never ends, so recur stops it, and the stopped walk still needs a sound
// answer to "can this type take a store?".
//
// What: TypeClosure (every type node of t and of the declarations it
// reaches, a finite set) and walker.closureTakes.
//
// How: closureTakes answers true only when some node of t's closure has a
// head that a store rule needs (a type parameter or Self, a view, or one of
// the six Interior items); otherwise no instance below t takes a store.

package borrow

import (
	"mk/internal/types"
)

// TypeClosure calls visit on every type node of t and of the declarations
// it reaches, at any depth of substitution: t's own nodes (elements, type
// arguments, record fields, fn parameters and results, with written
// false), and for every named type or error-set member met, its struct
// fields, enum variants' payload fields or error payload fields as
// declared (written true; a type parameter there stands for an argument
// met elsewhere). Generic substitution only puts argument nodes in the
// places of a declaration's parameters, so every node of every instance a
// walk from t can meet, however deep a polymorphic recursion grows
// (`next: ?&G[?T]`), has the head of some node visited here: a walk that
// answers from heads alone answers soundly for the infinite walk from this
// finite set. visit returns true to stop; TypeClosure then returns true.
func TypeClosure(tab *types.Table, items *types.Items, t types.TypeID, visit func(x types.TypeID, written bool) bool) bool {
	return typeClosure(tab, items, t, nil, visit)
}

func typeClosure(tab *types.Table, items *types.Items, t types.TypeID, onDecl func(types.ItemID), visit func(x types.TypeID, written bool) bool) bool {
	type at struct {
		t       types.TypeID
		written bool
	}
	seen := map[at]bool{}
	decls := map[types.ItemID]bool{}
	var stack []at
	push := func(x types.TypeID, written bool) {
		if x == types.NoType || seen[at{x, written}] {
			return
		}
		seen[at{x, written}] = true
		stack = append(stack, at{x, written})
	}
	fields := func(fs []types.FieldDecl) {
		for _, f := range fs {
			push(f.Type, true)
		}
	}
	decl := func(it types.ItemID) {
		if items == nil || it == types.NoItem || decls[it] {
			return
		}
		decls[it] = true
		if onDecl != nil {
			onDecl(it)
		}
		switch d := items.Get(it).Decl.(type) {
		case *types.StructDecl:
			fields(d.Fields)
		case *types.EnumDecl:
			for _, v := range d.Variants {
				fields(v.Fields)
			}
		case *types.ErrorDecl:
			fields(d.Fields)
		}
	}
	members := func(set types.ErrorSetID) {
		for _, m := range tab.Members(set) {
			if !m.IsParam {
				decl(m.Item)
			}
		}
	}
	push(t, false)
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visit(x.t, x.written) {
			return true
		}
		ty := tab.Get(x.t)
		switch ty.Kind {
		case types.KNamed:
			for _, a := range ty.Args {
				if !a.IsConst {
					push(a.Type, x.written)
				}
			}
			decl(ty.Item)
		case types.KRecord:
			for _, f := range ty.Fields {
				push(f.Type, x.written)
			}
		case types.KFn:
			for _, p := range ty.Fn.Params {
				push(p.Type, x.written)
			}
			push(ty.Fn.Result, x.written)
		case types.KErrorSet:
			members(ty.Errors)
		case types.KErrorUnion:
			members(ty.Errors)
			push(ty.Elem, x.written)
		default:
			push(ty.Elem, x.written)
		}
	}
	return false
}

// closureTakes is firstTakes's answer where recur stops it at t (without it, a
// static of a polymorphically recursive type always took stores). Every rule
// by which firstTakes counts a type needs, somewhere below t, a node whose
// head is a type parameter or Self (in gen modes other than genNone), a mut
// view, a generic plain view, or one of the five loan-interior items (Cell,
// Mutex, Chan with a carrier or parametric payload, Arena or Scope):
// writableByType, mayWritable and the view rules. So when no node of t's
// closure (TypeClosure) has such a head, no instance below t takes a store,
// and the answer is false; otherwise it is true, as an ANY step. A
// declaration's own parameters stand for nodes visited elsewhere, so they do
// not count; a parameter of the walk's own counts in genNone only when
// writableByType holds for it (a parameter narrowed to such a type); anything
// closureTakes does not know counts. The five items mirror internal/check's
// LoanInterior heads (kinds.go); raw.Atomic never holds a carrier.
func (w *walker) closureTakes(t types.TypeID, gen genMode) bool {
	if w.items == nil || w.known == nil {
		return true
	}
	k := w.known
	interior := []types.ItemID{k.Cell, k.Mutex, k.Chan, k.SpinCore, k.Arena, k.Scope}
	onDecl := func(it types.ItemID) {
		if w.reads == nil {
			return
		}
		// As namedFields records: the closure's answer depends on every
		// declaration it reads, private fields included (without these a warm
		// cache kept a stale "takes no store"). An error's payload fields are
		// read too.
		switch w.items.Get(it).Decl.(type) {
		case *types.StructDecl, *types.EnumDecl:
			key := w.items.Get(it).Key
			w.reads.Add(types.Read{Kind: types.ReadFields, Key: key, Name: "all"})
			w.reads.Add(types.Read{Kind: types.ReadShape, Key: key})
		case *types.ErrorDecl:
			w.reads.Add(types.Read{Kind: types.ReadFields, Key: w.items.Get(it).Key, Name: "all"})
		}
	}
	return typeClosure(w.tab, w.items, t, onDecl, func(x types.TypeID, written bool) bool {
		ty := w.tab.Get(x)
		switch ty.Kind {
		case types.KParam:
			return !written && (gen != genNone || w.writableByType(x))
		case types.KSelf:
			return written || gen != genNone
		case types.KNamed:
			for _, it := range interior {
				if it != types.NoItem && ty.Item == it {
					if (it == k.Cell || it == k.Mutex || it == k.Chan || it == k.SpinCore) && !w.paramFrom(x, 0) {
						return w.factsOf(x).LoanInterior
					}
					return true
				}
			}
			if ty.Item == types.NoItem {
				return true
			}
			switch d := w.items.Get(ty.Item).Decl.(type) {
			case *types.StructDecl:
				return d.MutView || (d.View && !d.PlainView && len(d.Generics) > 0)
			case *types.EnumDecl:
				return false
			}
			return true
		}
		return false
	})
}
