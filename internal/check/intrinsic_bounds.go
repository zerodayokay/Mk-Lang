// Why: some std.raw intrinsics (std/raw/unsafe13.mk) and mem.OffsetOf take
// type arguments that only a narrow set of types can fill; codegen relies on
// them.
//
// What: the implicit bounds of AtomicCopy[T] and AtomicFill[T] (a plain T of
// 1, 2, 4 or 8 bytes, aligned to its size) and FnFromAddr[F] (a fn type),
// with offsetOfCall and offsetOfValue for mem.OffsetOf.
//
// How: the bounds are checked with zerovalid.go's other implicit bounds at
// every call, fn value and instantiation, else E0701. The atomic size and
// alignment rule exists because LLVM's element-wise unordered memcpy,
// memmove and memset copy each element whole whatever it holds.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

// The implicit bounds' texts, after implicitFailure's "an ".
const (
	boundFnType     = "`fn` or `?fn` type, as FnFromAddr's F"
	boundAtomicElem = "element of 1, 2, 4 or 8 bytes, aligned to its size, that is plain"
	boundByteView   = "unpadded plain bytes (bool, char, numbers, arrays, or unpadded plain structs with no references or interior mutability)"
)

// rawItem reports whether it is std.raw's top-level function named one of
// names.
func (c *Checker) rawItem(it types.ItemID, names ...string) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	if mod == nil || !mod.Std || mod.Path != "std.raw" {
		return false
	}
	n := c.cfg.Interner.Str(item.Name)
	for _, w := range names {
		if n == w {
			return true
		}
	}
	return false
}

// rawAtomicElems reports whether it is raw.AtomicCopy or raw.AtomicFill.
func (c *Checker) rawAtomicElems(it types.ItemID) bool {
	return c.rawItem(it, "AtomicCopy", "AtomicFill")
}

// rawFnFromAddr reports whether it is raw.FnFromAddr.
func (c *Checker) rawFnFromAddr(it types.ItemID) bool {
	return c.rawItem(it, "FnFromAddr")
}

// fnTypeArg reports whether t (narrowed in env) is a fn or ?fn type.
func (c *Checker) fnTypeArg(env *Env, t types.TypeID) bool {
	ty := c.cfg.Types.Get(narrowed(c.cfg.Types, env, t))
	if ty.Kind == types.KFn {
		return true
	}
	return ty.Kind == types.KOptional && c.cfg.Types.Get(narrowed(c.cfg.Types, env, ty.Elem)).Kind == types.KFn
}

// atomicElemArg reports whether t (narrowed in env) is a plain closed type
// of 1, 2, 4 or 8 bytes aligned to its size; an open type parameter is not
// (its size is the instance's).
func (c *Checker) atomicElemArg(env *Env, t types.TypeID, rec recorder) bool {
	n := narrowed(c.cfg.Types, env, t)
	if !c.closed(n) || c.sizeOnCycle(n) {
		return false
	}
	c.shapeReads(env, n, rec)
	if c.FactsIn(env, n).Kind != types.Plain {
		return false
	}
	// Aligned to its size, too: an atomic access of N bytes at a lower
	// alignment is an __atomic_* call no Mk link provides.
	sh := c.Shape(n)
	switch sh.Size {
	case 1, 2, 4, 8:
		return uint64(sh.Align) == sh.Size
	}
	return false
}

// memOffsetOf reports whether it is std.mem's OffsetOf.
func (c *Checker) memOffsetOf(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType || c.cfg.Interner.Str(item.Name) != "OffsetOf" {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.mem"
}

// memAsBytes reports whether it is std.mem's AsBytes or SliceAsBytes.
func (c *Checker) memAsBytes(it types.ItemID) bool {
	if it == types.NoItem {
		return false
	}
	item := c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner != types.NoItem || item.OwnerPrim != types.NoType {
		return false
	}
	n := c.cfg.Interner.Str(item.Name)
	if n != "AsBytes" && n != "SliceAsBytes" {
		return false
	}
	mod := c.cfg.Items.Module(item.Module)
	return mod != nil && mod.Std && mod.Path == "std.mem"
}

// byteViewArg reports whether t (narrowed in env) is safe to view as raw
// bytes: bool, char, numbers, void, arrays of such, or unpadded plain structs
// or records with no references, raw pointers, or interior mutability.
func (c *Checker) byteViewArg(env *Env, t types.TypeID, rec recorder, seen map[types.TypeID]bool) (bool, string) {
	tab := c.cfg.Types
	n := narrowed(tab, env, t)
	ty := tab.Get(n)
	switch ty.Kind {
	case types.KPoison, types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KVoid:
		return true, ""
	case types.KParam, types.KSelf, types.KInfer:
		return false, "type parameter is not known"
	case types.KNever, types.KStr, types.KRef, types.KMut, types.KRawPtr, types.KSlice, types.KMutSlice,
		types.KFn, types.KOptional, types.KErrorUnion, types.KErrorSet, types.KNone:
		return false, "must be bool, char, a number, an array, or an unpadded plain struct with no references"
	}
	if !c.closed(n) {
		return false, "type parameter is not known"
	}
	if seen[n] {
		return true, ""
	}
	seen[n] = true
	c.shapeReads(env, n, rec)
	if ty.Kind == types.KNamed {
		it := c.cfg.Items.Get(ty.Item)
		switch it.Decl.(type) {
		case *types.StructDecl, *types.EnumDecl:
			rec.fields(it.Key, true)
			rec.signature(it.Key)
		}
	}
	if c.sizeOnCycle(n) {
		return false, "has a recursive by-value layout"
	}
	f := c.FactsIn(env, n)
	if f.Kind != types.Plain || f.Carrier || !f.Freeze {
		return false, "must be plain, freeze-safe, and hold no references"
	}
	switch ty.Kind {
	case types.KArray:
		return c.byteViewArg(env, ty.Elem, rec, seen)
	case types.KRecord:
		lay := c.Layout(n)
		var sum uint64
		for _, fd := range ty.Fields {
			ok, why := c.byteViewArg(env, fd.Type, rec, seen)
			if !ok {
				return false, why
			}
			sum += c.Shape(fd.Type).Size
		}
		if sum != lay.Shape.Size {
			return false, "has padding bytes"
		}
		return true, ""
	case types.KNamed:
		sd, ok := c.cfg.Items.Get(ty.Item).Decl.(*types.StructDecl)
		if !ok {
			return false, "enums cannot be viewed as bytes"
		}
		lay := c.Layout(n)
		if lay.Shape.Opaque {
			return false, "opaque layout cannot be viewed as bytes"
		}
		var sum uint64
		for _, fd := range sd.Fields {
			ft := c.subst(fd.Type, ty.Args, types.NoType)
			ok, why := c.byteViewArg(env, ft, rec, seen)
			if !ok {
				return false, why
			}
			sum += c.Shape(ft).Size
		}
		if sum != lay.Shape.Size {
			return false, "has padding bytes"
		}
		return true, ""
	}
	return false, "must be bool, char, a number, an array, or an unpadded plain struct with no references"
}

// offsetOfCall: `mem.OffsetOf[T]("f")` is a compile-time offset wherever
// it is called, so its field is a string literal (E0705 otherwise: codegen
// folds it) and, for a closed T, a field T lacks
// is E0901 at the call, as the evaluator reports it in a const. T's
// fields are a record's or a struct's own (every field, exported or not:
// the offset reads only the layout). A T built on a type parameter is
// E0701 (an instance could lack the field, and codegen has no diagnostic
// to give), and OffsetOf as a fn value is E0705 (offsetOfValue): so codegen
// sees only closed T and literal fields.
func (u *unit) offsetOfCall(call *ast.CallExpr, item types.ItemID, args []types.GenericArg) {
	if call == nil || !u.c.memOffsetOf(item) || len(args) != 1 || args[0].IsConst || len(call.Args) != 1 || call.Args[0] == nil {
		return
	}
	arg := unparenExpr(call.Args[0].Value)
	lit, ok := arg.(*ast.BasicLit)
	var name []byte
	if ok {
		name, ok = litString(lit.Kind, lit.Raw)
	}
	if !ok {
		u.err(diag.E0705, arg.Span(), "`mem.OffsetOf`'s field is a string literal: the offset is a compile-time constant")
		return
	}
	t := narrowed(u.tab, u.env, args[0].Type)
	if t == types.Poison {
		return
	}
	if !u.c.closed(t) {
		ts := u.str(t)
		u.err(diag.E0701, call.Span(), "`"+ts+"` does not satisfy `T`'s implicit bound: a type whose fields are known at the call, not one built on a type parameter (`mem.OffsetOf` is a compile-time offset)").
			Text("param", "T").Text("bound", "closed").Text("type", ts)
		return
	}
	for _, f := range u.c.offsetFieldNames(t) {
		if f == string(name) {
			return
		}
	}
	ts := u.str(t)
	u.err(diag.E0901, lit.Span(), "`"+ts+"` has no field `"+string(name)+"`: `mem.OffsetOf` reads a field's offset").
		Text("panic", "explicit").Text("message", "mem.OffsetOf: "+ts+" has no field `"+string(name)+"`")
}

// offsetFieldNames are a record's or a struct's field names, in order; none for
// any other type.
func (c *Checker) offsetFieldNames(t types.TypeID) []string {
	ty := c.cfg.Types.Get(t)
	var out []string
	switch ty.Kind {
	case types.KRecord:
		for _, f := range ty.Fields {
			out = append(out, c.cfg.Interner.Str(f.Name))
		}
	case types.KNamed:
		if sd, ok := c.declOfItem(c.cfg.Items.Get(ty.Item)).(*types.StructDecl); ok {
			for _, f := range sd.Fields {
				out = append(out, c.cfg.Interner.Str(f.Name))
			}
		}
	}
	return out
}

// offsetOfValue is E0705 for mem.OffsetOf used as a fn value: a call
// through it could not give its field as a compile-time string (the rule
// for a function with a const parameter, applied to OffsetOf's field).
func (u *unit) offsetOfValue(at ast.Node, s *types.Signature) bool {
	if s == nil || !u.c.memOffsetOf(s.Item) {
		return false
	}
	u.err(diag.E0705, at.Span(), "`OffsetOf`'s field is a compile-time string, which a call through a fn value cannot supply; call it directly").
		Text("param", "field")
	return true
}
