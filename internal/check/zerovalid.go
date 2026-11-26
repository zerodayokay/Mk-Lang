// Why: some std functions and types need more of a type argument than a
// declared bound can say (every byte zero is a value, an atomic-sized
// integer, not linear); without a check, a wrong instance would compile and
// misbehave.
//
// What: implicitBounds and atomicTypeBound (the E0701 of a call, fn value or
// type expression), the recognizers (arenaSlice, memZeroed, poolFree,
// rawUnaligned, atomicType, atomicFunc, atomicArith), and the predicates
// zeroValid and integerArg.
//
// How: `Arena.Slice[T]` and `Arena.SliceAligned[T]` zero-fill their
// elements, and `std.mem.Zeroed[T]` is a T of zero bytes, so `T` must be
// zero-valid; Zeroed's T is its result, so it is also not an error union
// (whose call would read as fallible); `raw.Atomic[T]` and `atomic.Atomic[T]`
// need an integer `T` of 1, 2, 4 or 8 bytes or a raw pointer, and their
// arithmetic (Add, AddOrd to XorOrd, FetchAdd to FetchXor) an integer;
// `raw.ReadUnaligned[T]` and `raw.WriteUnaligned[T]` an integer `T`;
// `Pool[T].Free` a T that is not linear. zeroValid records every type item
// its answer depends on.

package check

import (
	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// arenaSlice reports whether item it is `Arena.Slice` or `Arena.SliceAligned`,
// the std functions whose generic carries the zero-valid bound.
func (c *Checker) arenaSlice(it types.ItemID) bool {
	if c.known.Arena == types.NoItem || it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	return item.Owner == c.known.Arena && item.Kind == types.KindFunc && (c.cfg.Interner.Str(item.Name) == "Slice" || c.cfg.Interner.Str(item.Name) == "SliceAligned")
}

// memZeroed reports whether item it is the free function `std.mem.Zeroed`,
// whose result is all zero bytes.
func (c *Checker) memZeroed(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType || c.cfg.Interner.Str(item.Name) != "Zeroed" {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.mem"
}

// poolFree reports whether item it is `std.mem.Pool[T].Free`, which drops
// the slot's value, so its T must not be linear. types.Bound has no kind
// for "not linear", so the rule is an
// implicit bound, as Arena.Slice's zero-valid one is.
func (c *Checker) poolFree(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner == types.NoItem || c.cfg.Interner.Str(item.Name) != "Free" {
		return false
	}
	owner := c.cfg.Items.Get(item.Owner)
	if owner.Kind != types.KindStruct || c.cfg.Interner.Str(owner.Name) != "Pool" {
		return false
	}
	mod := c.cfg.Items.Module(owner.Module)
	return mod != nil && mod.Std && mod.Path == "std.mem"
}

// rawUnaligned reports whether item it is the free function
// `std.raw.ReadUnaligned` or `std.raw.WriteUnaligned` (an align 1 load or
// store of an integer).
func (c *Checker) rawUnaligned(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType {
		return false
	}
	if name := c.cfg.Interner.Str(item.Name); name != "ReadUnaligned" && name != "WriteUnaligned" {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.raw"
}

// atomicFunc reports whether item it is a Type.Name function of
// `raw.Atomic` (KnownItems.Atomic) or of `atomic.Atomic`, whose first
// generic is the owner's T.
func (c *Checker) atomicFunc(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	return item.Kind == types.KindFunc && item.Owner != types.NoItem && c.atomicType(item.Owner)
}

// atomicType reports whether type item it is `raw.Atomic` or std.atomic's
// `Atomic`, found by module path and name, as rawUnaligned is,
// since KnownItems names only the first.
func (c *Checker) atomicType(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	if c.known.Atomic != types.NoItem && it == c.known.Atomic {
		return true
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindStruct || c.cfg.Interner.Str(item.Name) != "Atomic" {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.atomic"
}

// atomicArith reports whether atomic function it is one of the read-
// modify-writes that compute (add, sub, and, or, xor), which need an
// integer T even when the owner's T was judged elsewhere.
func (c *Checker) atomicArith(it types.ItemID) bool {
	switch c.cfg.Interner.Str(c.cfg.Items.Get(it).Name) {
	case "Add", "AddOrd", "SubOrd", "AndOrd", "OrOrd", "XorOrd",
		"FetchAdd", "FetchSub", "FetchAnd", "FetchOr", "FetchXor":
		return true
	}
	return false
}

// atomicHome reports whether module m is std.atomic, whose own generic
// code over `raw.Atomic[T]` and `Atomic[T]` is their definition: the
// implicit bound is judged at the instances other modules write.
func (c *Checker) atomicHome(m types.ModuleID) bool {
	mod := c.cfg.Items.Module(m)
	return mod != nil && mod.Std && mod.Path == "std.atomic"
}

// takesOwner reports whether a Type.Name function's signature takes a value
// of its owner type (self, or the owner named, behind references, pointers,
// optionals, slices or arrays). Its type arguments then come from a value
// whose type was formed, and judged, elsewhere (atomicTypeBound at the type
// expression, implicitBounds at the call that made it), so the call does
// not judge the owner's implicit bound again.
func (c *Checker) takesOwner(sig *types.Signature, it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	owner := c.cfg.Items.Get(it).Owner
	if owner == types.NoItem {
		return false
	}
	tab := c.cfg.Types
	for _, p := range sig.Params {
		t := p.Type
		for {
			ty := tab.Get(t)
			switch ty.Kind {
			case types.KRef, types.KMut, types.KRawPtr, types.KOptional, types.KSlice, types.KMutSlice, types.KArray:
				t = ty.Elem
				continue
			case types.KSelf:
				return true
			case types.KNamed:
				if ty.Item == owner {
					return true
				}
			}
			break
		}
	}
	return false
}

// Bound texts of the implicit bounds (the E0701 `bound` fact).
const (
	boundZeroValid   = "zero-valid"
	boundInteger     = "integer"
	boundAtomicInt   = "integer of 1, 2, 4 or 8 bytes or a raw pointer"
	boundNotLinear   = "not linear"
	boundNotErrUnion = "not an error union"
)

// implicitBounds is the E0701 of an instance inst of item (a call, or a fn
// value with its arguments inferred or written) whose type argument misses
// an implicit bound, at at. owner says whether the owner's arguments were
// bound by this instance (an open head, `raw.Atomic.New(x)`) rather than
// taken from a type already formed, whose type expression or open-head call
// checked them where it was written.
func (u *unit) implicitBounds(item types.ItemID, at source.Span, inst uint32, owner bool) {
	if inst == 0 || int(inst) >= len(u.b.Generic) {
		return
	}
	g := u.b.Generic[inst]
	c := u.c
	var t types.TypeID
	var bound string
	switch {
	case c.poolFree(item):
		if len(g) == 0 || g[0].IsConst {
			return
		}
		t = g[0].Type
		// A generic is judged once, from its signature: an open T reads as
		// linear unless a bound says otherwise (kinds.go param, `T: Plain`),
		// so Free of one is E0701 in the generic, as a closed linear T is at
		// its call.
		n := narrowed(c.cfg.Types, u.env, t)
		if t == types.Poison || c.FactsIn(u.env, n).Kind != types.Linear {
			return
		}
		closed := c.closed(n)
		if closed {
			c.shapeReads(u.env, t, u.x.rec)
		}
		ts := u.str(t)
		why := "is linear"
		if !closed {
			why = "may be linear"
		}
		u.b.Diags = append(u.b.Diags, diag.New(diag.E0701, at, "`"+ts+"` "+why+": `Pool.Free` drops the value").
			Text("param", "T").Text("bound", boundNotLinear).Text("type", ts))
		return
	case c.listCopies(c.cfg.Items.Get(item)): // T: Plain, the elements are copied
		d, ok := c.declOfItem(c.cfg.Items.Get(item)).(*types.FuncDecl)
		if !ok || len(g) == 0 || g[0].IsConst || len(d.Sig.Generics) == 0 {
			return
		}
		b := types.Bound{Kind: types.BoundPlain}
		if t = g[0].Type; t == types.Poison || c.satisfies(u.env, t, b, u.x.rec) {
			return
		}
		u.b.Diags = append(u.b.Diags, c.boundFailure(u.env, at, d.Sig.Generics[0].Name, b, t, u.x.gens))
		return
	case c.releaseRowOf(item) != nil: // the release bounds (freebound.go)
		u.releaseBound(c.releaseRowOf(item), item, at, g)
		return
	case c.arenaSlice(item) || c.memZeroed(item):
		if len(g) == 0 || g[len(g)-1].IsConst {
			return
		}
		t = g[len(g)-1].Type
		if t == types.Poison {
			return
		}
		if n := narrowed(c.cfg.Types, u.env, t); c.memZeroed(item) && c.cfg.Types.Kind(n) == types.KErrorUnion {
			// The call's result is its T: an error union there would make a
			// call whose signature has no errors read as fallible.
			ts := u.str(t)
			u.b.Diags = append(u.b.Diags, diag.New(diag.E0701, at, "`"+ts+"` is an error union, and `mem.Zeroed` never fails: zero its value type, `"+u.str(c.cfg.Types.Get(n).Elem)+"`").
				Text("param", "T").Text("bound", boundNotErrUnion).Text("type", ts))
			return
		}
		if c.zeroValid(u.env, t, u.x.rec, map[types.TypeID]bool{}) {
			return
		}
		ts := u.str(t)
		why := "`mem.Zeroed` makes a value of zero bytes"
		if c.arenaSlice(item) {
			why = "`Arena." + c.cfg.Interner.Str(c.cfg.Items.Get(item).Name) + "` zero-fills its elements"
		}
		u.b.Diags = append(u.b.Diags, diag.New(diag.E0701, at, "`"+ts+"` is not zero-valid: "+why).
			Text("param", "T").Text("bound", boundZeroValid).Text("type", ts))
		return
	case c.rawUnaligned(item):
		if len(g) != 1 || g[0].IsConst {
			return
		}
		t, bound = g[0].Type, boundInteger
		if t == types.Poison || c.integerArg(u.env, t, false) {
			return
		}
	case c.rawAtomicElems(item): // intrinsic_bounds.go
		if len(g) != 1 || g[0].IsConst {
			return
		}
		t, bound = g[0].Type, boundAtomicElem
		if t == types.Poison || c.atomicElemArg(u.env, t, u.x.rec) {
			return
		}
	case c.rawFnFromAddr(item): // intrinsic_bounds.go
		if len(g) != 1 || g[0].IsConst {
			return
		}
		t, bound = g[0].Type, boundFnType
		if t == types.Poison || c.fnTypeArg(u.env, t) {
			return
		}
	case c.atomicFunc(item) && c.atomicArith(item) && !c.atomicHome(u.m.ID):
		if len(g) == 0 || g[0].IsConst {
			return
		}
		t, bound = g[0].Type, boundInteger
		if t == types.Poison || c.integerArg(u.env, t, false) {
			return
		}
	case owner && c.atomicFunc(item) && !c.atomicHome(u.m.ID):
		if len(g) == 0 || g[0].IsConst {
			return
		}
		t, bound = g[0].Type, boundAtomicInt
		if t == types.Poison || c.integerArg(u.env, t, true) {
			return
		}
	default:
		return
	}
	u.b.Diags = append(u.b.Diags, c.implicitFailure(at, t, bound, u.x.gens))
}

// implicitFailure is the E0701 of an implicit bound: facts param, bound,
// type, as boundFailure's.
func (c *Checker) implicitFailure(at source.Span, t types.TypeID, bound string, gens []types.GenericParam) *diag.Diagnostic {
	ts := c.cfg.Types.TypeString(t, gens)
	return diag.New(diag.E0701, at, "`"+ts+"` does not satisfy `T`'s implicit bound: an "+bound).
		Text("param", "T").Text("bound", bound).Text("type", ts)
}

// atomicTypeBound is the E0701 of the type expression `raw.Atomic[T]` or
// `atomic.Atomic[T]` with T not an integer of 1, 2, 4 or 8 bytes or a raw
// pointer, beside the declared bounds queueBounds checks; nil
// when it holds. The defining module's own declarations over Atomic's T,
// and std.atomic's over either, are their definition, not instances.
func (m *Module) atomicTypeBound(env *Env, at source.Span, it *types.Item, args []types.GenericArg, gens []types.GenericParam) *diag.Diagnostic {
	c := m.c
	if !c.atomicType(it.ID) || it.Module == m.ID || c.atomicHome(m.ID) || len(args) == 0 || args[0].IsConst {
		return nil
	}
	t := args[0].Type
	if t == types.Poison || c.integerArg(env, t, true) {
		return nil
	}
	return c.implicitFailure(at, t, boundAtomicInt, gens)
}

// narrowed is t, or X when t is a type parameter narrowed by an enclosing
// `if T is X` (env).
func narrowed(tab *types.Table, env *Env, t types.TypeID) types.TypeID {
	for range maxFoldDepth {
		ty := tab.Get(t)
		if ty.Kind != types.KParam || env == nil || int(ty.Index) >= len(env.narrow) || env.narrow[ty.Index] == types.NoType {
			return t
		}
		t = env.narrow[ty.Index]
	}
	return t
}

// integerArg reports whether t (narrowed in env) is an integer type; with
// atomic, one of 1, 2, 4 or 8 bytes (int and uint, types.KIntPtr, are 8
// bytes on every supported target) or a raw pointer (every
// supported target has atomics of each, and LLVM's load, store, xchg and
// cmpxchg take ptr).
func (c *Checker) integerArg(env *Env, t types.TypeID, atomic bool) bool {
	ty := c.cfg.Types.Get(narrowed(c.cfg.Types, env, t))
	switch ty.Kind {
	case types.KIntPtr:
		return true
	case types.KInt:
		return !atomic || ty.Bits == 8 || ty.Bits == 16 || ty.Bits == 32 || ty.Bits == 64
	case types.KRawPtr:
		return atomic
	}
	return false
}

// zeroValid reports whether every byte zero is a value of t, by the type
// layouts: scalars, `void`, raw pointers, every optional (`none` is
// zero), arrays, records and structs of zero-valid parts, an enum whose
// discriminant 0 names a variant with zero-valid fields, and `E!T` with a
// zero-valid `T` (code 0 is success). Not: references and `fn` values
// (never null), `str` and slices (an empty one holds a non-null pointer),
// error-set values (codes start at 1), `never`, interfaces, open type
// parameters, a struct with an invariant, and anything that is not Plain
// (`Box`, `Cell`, `Mutex`, `Arena`, linear and unique types). A type
// parameter narrowed by `if T is X` is X.
//
// Every type item the answer depends on is recorded in rec: its shape (the
// facts), all its fields (private ones too: the zero fill writes them), and
// its declaration (an enum's discriminants are in mk.decl.v1; a struct's
// invariants are not).
func (c *Checker) zeroValid(env *Env, t types.TypeID, rec recorder, seen map[types.TypeID]bool) bool {
	tab := c.cfg.Types
	t = narrowed(tab, env, t)
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KPoison, types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KVoid, types.KRawPtr:
		return true
	case types.KNever, types.KStr, types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KFn,
		types.KErrorSet, types.KParam, types.KSelf, types.KInfer, types.KNone:
		return false
	}
	if seen[t] {
		return true // a by-value cycle is E0907 at its declaration
	}
	seen[t] = true
	c.shapeReads(env, t, rec)
	if ty.Kind == types.KNamed {
		// Only a struct or an enum has a fields part a read can name
		// (iface's Fields); a read no part answers would keep the unit
		// out of the cache.
		it := c.cfg.Items.Get(ty.Item)
		switch it.Decl.(type) {
		case *types.StructDecl, *types.EnumDecl:
			rec.fields(it.Key, true)
			rec.signature(it.Key)
		}
	}
	if c.FactsIn(env, t).Kind != types.Plain {
		return false
	}
	switch ty.Kind {
	case types.KOptional:
		return true
	case types.KArray, types.KErrorUnion:
		return c.zeroValid(env, ty.Elem, rec, seen)
	case types.KRecord:
		for _, f := range ty.Fields {
			if !c.zeroValid(env, f.Type, rec, seen) {
				return false
			}
		}
		return true
	case types.KNamed:
		switch d := c.cfg.Items.Get(ty.Item).Decl.(type) {
		case *types.StructDecl:
			if len(d.Invariants) > 0 {
				return false
			}
			for _, f := range d.Fields {
				if !c.zeroValid(env, c.subst(f.Type, ty.Args, types.NoType), rec, seen) {
					return false
				}
			}
			return true
		case *types.EnumDecl:
			for _, v := range d.Variants {
				if v.Discr != 0 {
					continue
				}
				for _, f := range v.Fields {
					if !c.zeroValid(env, c.subst(f.Type, ty.Args, types.NoType), rec, seen) {
						return false
					}
				}
				return true
			}
			return false
		}
	}
	return false
}
