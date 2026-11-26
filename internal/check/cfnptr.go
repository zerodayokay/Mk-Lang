// Why: a C import binds a C function pointer as an Mk fn type (`?fn` in a
// struct field), which safe code could unwrap and call, running unchecked C
// code. A call through a fn value cannot know where the value came from. A
// COM-style struct whose first field points to a vtable of function pointers
// taking a pointer to the struct also emits methods whose vtable pointer safe
// code could have forged (such as via mem.Zeroed).
//
// What: cFnPointer (E1101 c-fn-pointer), holdsFn, and cComMethod /
// isCComStruct (E1101 c-vtable-method).
//
// How: the obligation sits where the value leaves C: reading a field of a C
// import's struct whose type holds a fn (a fn, a ?fn, or an array of them)
// is E1101 outside unsafe, and calling or naming a method on a C-import
// struct of the COM vtable shape (decided from the struct's type alone, never
// a body) is E1101 outside unsafe. The other ways out of C need unsafe
// already: a bodiless extern's call, an extern var and a raw pointer's pointee
// (E1103). A store into such a field reads nothing out of C and stays safe.

package check

import (
	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/types"
)

// cFnPointer is E1101 c-fn-pointer for fe, a read of field type ft of the
// struct it, when it is a C import's struct and ft holds a fn, outside
// unsafe and outside the binding module itself.
func (u *unit) cFnPointer(fe *ast.FieldExpr, it *types.Item, ft types.TypeID) {
	cimp := u.c.cfg.CImport
	if u.unsafeDepth > 0 || cimp == nil || fe.ID() == u.cFnStore || (u.m != nil && it.Module == u.m.ID) || !cimp(it.Module) || !u.holdsFn(ft) {
		return
	}
	u.err(diag.E1101, fe.Span(), "reading a C function pointer needs an `unsafe \"reason\" { }` block: calling it runs C code").
		Text("operation", "c-fn-pointer")
}

// holdsFn reports a fn type, an optional one, or an array of either.
func (u *unit) holdsFn(t types.TypeID) bool {
	for {
		ty := u.tab.Get(t)
		switch ty.Kind {
		case types.KFn:
			return true
		case types.KOptional, types.KArray:
			t = ty.Elem
		default:
			return false
		}
	}
}

// cComMethod reports whether it is a method of a C-import struct whose first
// field points to a struct of function pointers that each take a pointer to
// the struct as their first parameter (decided from the struct's type alone).
func (u *unit) cComMethod(it types.ItemID) bool {
	cimp := u.c.cfg.CImport
	if cimp == nil || it == types.NoItem {
		return false
	}
	item := u.c.cfg.Items.Get(it)
	if item.Kind != types.KindFunc || item.Owner == types.NoItem {
		return false
	}
	owner := u.c.cfg.Items.Get(item.Owner)
	if !cimp(owner.Module) || (u.m != nil && (owner.Module == u.m.ID || item.Module == u.m.ID)) {
		return false
	}
	return u.isCComStruct(owner)
}

// isCComStruct reports whether owner is a struct whose first field points to
// a struct of function pointers (fn or ?fn) each taking *owner first.
func (u *unit) isCComStruct(owner *types.Item) bool {
	if owner.Kind != types.KindStruct {
		return false
	}
	sd, ok := u.c.declOfItem(owner).(*types.StructDecl)
	if !ok || len(sd.Fields) == 0 {
		return false
	}
	u.x.rec.fields(owner.Key, true)
	pt := u.tab.Get(sd.Fields[0].Type)
	if pt.Kind != types.KRawPtr {
		return false
	}
	vt := u.tab.Get(pt.Elem)
	if vt.Kind != types.KNamed {
		return false
	}
	vitem := u.c.cfg.Items.Get(vt.Item)
	if vitem.Kind != types.KindStruct {
		return false
	}
	vsd, ok := u.c.declOfItem(vitem).(*types.StructDecl)
	if !ok || len(vsd.Fields) == 0 {
		return false
	}
	u.x.rec.fields(vitem.Key, true)
	for _, vf := range vsd.Fields {
		vft := u.tab.Get(vf.Type)
		if vft.Kind == types.KOptional {
			vft = u.tab.Get(vft.Elem)
		}
		if vft.Kind != types.KFn || vft.Fn == nil || len(vft.Fn.Params) == 0 {
			return false
		}
		p0 := u.tab.Get(vft.Fn.Params[0].Type)
		if p0.Kind != types.KRawPtr {
			return false
		}
		ep := u.tab.Get(p0.Elem)
		if ep.Kind != types.KNamed || ep.Item != owner.ID {
			return false
		}
	}
	return true
}
