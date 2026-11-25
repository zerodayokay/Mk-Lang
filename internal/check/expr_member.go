// Why: `x.f`, `x.M()` and `T.Name` all go through one member lookup, which
// must auto-deref references but never raw pointers, see only what the
// module may see, and explain every miss.
//
// What: memberOf, valueMemberOf and memberLookup, ambiguous (use-site
// E0206), memberMiss (E0702, E0203), head and typeHead, field and fieldUse
// (E0204 for another module's private field), privateMethod (E0204 for
// another module's private method), notFieldValue, rawMember,
// builtinNames and builtinMember.
//
// How: the name is looked up exactly, then case-folded (a unique folded
// match is E0207 and still resolves). Reads are recorded per member
// namespace key, so adding a member rechecks the units that looked.

package check

import (
	"sort"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// deref removes every & and mut layer of t (never through *T),
// returning the value type and the number of layers.
func (u *unit) deref(t types.TypeID) (types.TypeID, int) {
	n := 0
	for {
		ty := u.tab.Get(t)
		if ty.Kind != types.KRef && ty.Kind != types.KMut {
			return t, n
		}
		t, n = ty.Elem, n+1
	}
}

// nsKey is the read key of t's member namespace: T's key plus ":"
// (types.MemberKey(module, T, "")). Zero for a builtin
// constructor, which no read names.
func (u *unit) nsKey(t types.TypeID) types.Key {
	ck, ok := u.c.ctorOf(t)
	if !ok || ck.item == types.NoItem {
		return types.Key{}
	}
	return types.KeyFromString(u.c.cfg.Items.Get(ck.item).Key.String() + ":")
}

// memberOf looks name up among the members of t (a constructor's value
// type) as this module sees them. A unique folded match is E0207
// and still resolves; a miss is E0702 or E0203; two impl methods with the
// name are the use-site E0206. ok is false after any diagnostic that does
// not resolve.
func (u *unit) memberOf(t types.TypeID, name *ast.Ident) (Member, bool) {
	return u.memberLookup(t, name, nil)
}

// valueMemberOf is memberOf for `x.f` on a value (a field read, or a method
// call when call is true), where a miss may be a habit (E1202, E1204;
// valueMiss).
func (u *unit) valueMemberOf(t types.TypeID, fe *ast.FieldExpr, call bool) (Member, bool) {
	return u.memberLookup(t, fe.Name, &valueSite{fe: fe, call: call})
}

// valueSite is a value member expression and whether it is a call's callee.
type valueSite struct {
	fe   *ast.FieldExpr
	call bool
}

// memberLookup is memberOf; v is the value member expression, nil for a
// type head or a pattern.
func (u *unit) memberLookup(t types.TypeID, name *ast.Ident, v *valueSite) (Member, bool) {
	in := u.c.cfg.Interner
	cands, folded := u.m.lookup(t, name.Name)
	if len(cands) == 0 {
		key := u.nsKey(t)
		u.x.rec.noItem(key, in.Str(name.Name))
		u.x.rec.exports(key)
		u.missAt(t, name, v)
		return Member{}, false
	}
	impls := 0
	for _, mb := range cands {
		if mb.Kind == MemberImplMethod {
			impls++
		}
	}
	if impls >= 2 {
		u.ambiguous(t, name, cands)
		return Member{}, false
	}
	if folded && len(cands) > 1 && v != nil {
		cands = foldedByUse(cands, v.call)
	}
	if folded && len(cands) != 1 {
		u.missAt(t, name, v)
		return Member{}, false
	}
	mb := cands[0]
	if folded {
		want := in.Str(mb.Name)
		got := in.Str(name.Name)
		u.err(diag.E0207, name.Span(), "`"+got+"` is written `"+want+"`").In(diag.PhaseTypes).
			Text("found", got).Text("expected", want).
			Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: name.Span(), Text: want}}, Why: "member names are exact; this is the declared spelling"})
	}
	if mb.Item != types.NoItem {
		u.x.rec.signature(u.c.cfg.Items.Get(mb.Item).Key)
	}
	u.privateMethod(name.Span(), mb)
	return mb, true
}

// foldedByUse narrows a folded miss on a value (field `cpu` and method `Cpu`
// both fold to `x.CPU`) by its use: a call keeps the members that are not
// fields, a read the fields.
func foldedByUse(cands []Member, call bool) []Member {
	var out []Member
	for _, mb := range cands {
		if (mb.Kind == MemberField) != call {
			out = append(out, mb)
		}
	}
	return out
}

// privateMethod is E0204 for a `Type.Name` function without `export` named
// from another module, by any form (`x.M()`, `T.M(..)`, `T.M` as a value,
// the loop's `Next`). An impl's methods are as visible as the impl, so
// only MemberFunc is checked. The lookup still resolves.
func (u *unit) privateMethod(at source.Span, mb Member) {
	if mb.Kind != MemberFunc || mb.Exported {
		return
	}
	items := u.c.cfg.Items
	it := items.Get(mb.Item)
	if it.Module == u.m.ID {
		return
	}
	owner := ""
	switch {
	case it.Owner != types.NoItem:
		owner = u.c.cfg.Interner.Str(items.Get(it.Owner).Name)
	case it.OwnerPrim != types.NoType:
		owner = u.str(it.OwnerPrim)
	}
	spell := owner + "." + u.c.cfg.Interner.Str(it.Name)
	mod := items.Module(it.Module).Path
	u.err(diag.E0204, at, "`"+spell+"` is not exported by `"+mod+"`").In(diag.PhaseTypes).
		Text("item", spell).Loc("declared_at", it.Span).Text("module", mod).
		Alt("export `" + spell + "` from `" + mod + "`").Alt("use an exported method of `" + mod + "`")
}

// ambiguous is the use-site E0206: impl methods of two impls
// that do not see each other meet at this use. Candidates are sorted by
// (module path, item key), so the message is the same on any schedule.
func (u *unit) ambiguous(t types.TypeID, name *ast.Ident, cands []Member) {
	items := u.c.cfg.Items
	var ms []*types.Item
	for _, mb := range cands {
		if mb.Kind == MemberImplMethod {
			ms = append(ms, items.Get(mb.Item))
		}
	}
	sort.SliceStable(ms, func(a, b int) bool {
		pa, pb := items.Module(ms[a].Module).Path, items.Module(ms[b].Module).Path
		if pa != pb {
			return pa < pb
		}
		return ms[a].Key.String() < ms[b].Key.String()
	})
	nm := u.c.cfg.Interner.Str(name.Name)
	d := u.err(diag.E0206, name.Span(), "`"+nm+"` names a method of more than one impl for `"+u.str(t)+"`").In(diag.PhaseTypes).
		Text("reason", "ambiguous-member")
	locs := make([]diag.Fact, len(ms))
	for i, it := range ms {
		locs[i] = diag.AtFact(it.Span)
		u.x.rec.signature(it.Key)
	}
	d.Fact("candidates", diag.ListFact(locs...))
	// Only a rename: an interface has no namespace (`Type.Name` functions
	// live in a type's namespace only), so `I.Name(&x)` is E0301 and is
	// not offered.
	d.Alt("rename one of the methods")
}

// missAt is valueMiss when v is a value member expression, else memberMiss.
func (u *unit) missAt(t types.TypeID, name *ast.Ident, v *valueSite) {
	if v != nil {
		u.valueMiss(t, v.fe, v.call)
		return
	}
	u.memberMiss(t, name)
}

// memberMiss is E0702 when an interface visible from this module has a
// method of that name (the type has no impl of it), else E0203 with the
// type's members.
func (u *unit) memberMiss(t types.TypeID, name *ast.Ident) {
	in := u.c.cfg.Interner
	nm := in.Str(name.Name)
	if iface := u.ifaceWithMethod(name.Name); iface != nil {
		u.err(diag.E0702, name.Span(), "`"+u.str(t)+"` has no impl of `"+in.Str(iface.Name)+"`, which declares `"+nm+"`").
			Text("interface", in.Str(iface.Name)).Text("type", u.str(t)).
			Alt("impl " + in.Str(iface.Name) + " for " + u.str(t))
		return
	}
	var names []string
	seen := map[source.Symbol]bool{}
	for _, mb := range u.m.visibleMembers(t) {
		if !seen[mb.Name] {
			seen[mb.Name] = true
			names = append(names, in.Str(mb.Name))
		}
	}
	for _, b := range u.builtinNames(t) {
		names = append(names, b)
	}
	sort.Strings(names)
	u.err(diag.E0203, name.Span(), "`"+u.str(t)+"` has no member `"+nm+"`").
		Text("type", u.str(t)).Fact("members", diag.TextList(names...))
}

// ifaceWithMethod finds an interface visible from this module (its own and
// its transitive imports', by module path then item key) that declares a
// method named n.
func (u *unit) ifaceWithMethod(n source.Symbol) *types.Item {
	c := u.c
	mods := append(u.m.depList(), u.m.ID)
	sort.Slice(mods, func(a, b int) bool {
		return c.cfg.Items.Module(mods[a]).Path < c.cfg.Items.Module(mods[b]).Path
	})
	for _, id := range mods {
		om := c.module(id)
		if om == nil {
			continue
		}
		for _, iid := range om.itemIDs() {
			it := c.cfg.Items.Get(iid)
			if it.Kind != types.KindInterface {
				continue
			}
			if d, ok := it.Decl.(*types.InterfaceDecl); ok {
				for _, s := range d.Methods {
					if s.Name == n {
						return it
					}
				}
			}
		}
	}
	return nil
}

// head is what the left side of `X.Name` names when it is a type
// (namespace form): the type with its written arguments, or the item with
// its arguments still to infer (open).
type head struct {
	t    types.TypeID
	it   *types.Item
	open bool
}

// typeHead reports whether x names a type: a struct, enum, error, alias or
// primitive, bare or with bracket arguments.
func (u *unit) typeHead(x ast.Expr) (head, bool) {
	tab := u.tab
	switch x := x.(type) {
	case *ast.Ident, *ast.FieldExpr:
		r := u.ref(x)
		switch r.Kind {
		case resolve.RefPrim:
			return head{t: types.TypeID(r.A)}, true
		case resolve.RefItem:
			it := u.c.cfg.Items.Get(types.ItemID(r.A))
			switch it.Kind {
			case types.KindStruct, types.KindEnum:
				u.x.rec.shape(it.Key)
				if len(u.c.genericsOf(it)) > 0 {
					return head{t: tab.Named(it.ID, nil), it: it, open: true}, true
				}
				return head{t: tab.Named(it.ID, nil), it: it}, true
			case types.KindError:
				return head{t: tab.ErrorSetType(tab.ErrorSet([]types.ErrorMember{{Item: it.ID}})), it: it}, true
			case types.KindAlias:
				t := u.x.typeOf(x, posPlain)
				return u.headOf(t), true
			}
		}
	case *ast.IndexExpr:
		if u.x.reading(x) != resolve.ReadGeneric {
			return head{}, false
		}
		r := u.ref(x.X)
		if r.Kind != resolve.RefItem {
			return head{}, false
		}
		switch u.c.cfg.Items.Get(types.ItemID(r.A)).Kind {
		case types.KindStruct, types.KindEnum, types.KindAlias:
			return u.headOf(u.x.typeOf(x, posPlain)), true
		}
	}
	return head{}, false
}

func (u *unit) headOf(t types.TypeID) head {
	h := head{t: t}
	if ty := u.tab.Get(t); ty.Kind == types.KNamed {
		h.it = u.c.cfg.Items.Get(ty.Item)
	}
	return h
}

// field types `x.f` where x is a value: auto-deref through & and
// mut, recorded as Adjust{AdjDeref, n} on x; a struct, error or record
// field. A method named without a call is not a value here (namespace form
// `T.Name` is): E0609 (notFieldValue), never a silent Poison.
func (u *unit) field(fe *ast.FieldExpr) types.TypeID {
	target := u.isTarget(fe.X)
	if target {
		u.targetOne = fe.X.ID()
	}
	xt := u.expr(fe.X, types.NoType)
	u.targetOne = 0
	if xt == types.Poison || fe.Name == nil {
		return types.Poison
	}
	if target {
		u.x.rec.target(u.c.cfg.Interner.Str(fe.Name.Name)) // Target.os reads os
	}
	v, n := u.deref(xt)
	if rp := u.tab.Get(v); rp.Kind == types.KRawPtr {
		// A field through a raw pointer dereferences it (E1103 outside
		// unsafe); the Adjust counts that layer too.
		u.rawDeref(fe, v)
		v, n = rp.Elem, n+1
	}
	if n > 0 {
		*u.b.adjust(fe.X.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n + u.predDeref(fe.X))}
	}
	vt := u.tab.Get(v)
	switch vt.Kind {
	case types.KPoison:
		return types.Poison
	case types.KRawPtr:
		// A second raw layer (`p: **S`): a field dereferences one raw
		// pointer only; inside unsafe rawDeref says nothing, so this
		// must (lower's invariant).
		u.rawMember(fe, xt)
		return types.Poison
	case types.KErrorUnion:
		u.unhandled(fe.X, v)
		return types.Poison
	case types.KParam:
		u.needsBound(fe.Name.Span(), v, "member `"+u.c.cfg.Interner.Str(fe.Name.Name)+"`")
		return types.Poison
	case types.KRecord:
		for i, f := range vt.Fields {
			if f.Name == fe.Name.Name {
				*u.b.use(fe.ID()) = Use{Kind: UseField, Index: uint32(i)}
				return f.Type
			}
		}
		u.valueMiss(v, fe, false)
		return types.Poison
	}
	mb, ok := u.valueMemberOf(v, fe, false)
	if !ok {
		return types.Poison
	}
	if mb.Kind != MemberField {
		u.notFieldValue(fe, v, mb)
		return types.Poison
	}
	return u.fieldUse(fe, v, mb)
}

// notFieldValue reports a member found through a value that is not a
// field there, so `x.Name` never types as a silent Poison (lower's
// invariant; FuzzNoSilentPoison's `A(c:Cell[i32]){(c.Load)}`):
//   - a method named without a call is E0609: a method value would capture
//     its receiver (method values are left out of the language), with the
//     call and the namespace form as alternatives;
//   - a `Type.Name` function without a receiver is E0203, as at a call;
//   - a variant is E0203: variants are named through the type, `E.A`.
func (u *unit) notFieldValue(fe *ast.FieldExpr, v types.TypeID, mb Member) {
	in := u.c.cfg.Interner
	nm := in.Str(fe.Name.Name)
	ty := u.str(v)
	switch mb.Kind {
	case MemberFunc, MemberImplMethod:
		d, ok := u.c.declOfItem(u.c.cfg.Items.Get(mb.Item)).(*types.FuncDecl)
		if ok && d.Sig.Receiver == types.NoReceiver {
			u.err(diag.E0203, fe.Name.Span(), "`"+nm+"` has no receiver; name it as `"+ty+"."+nm+"`").
				Text("type", ty).Fact("members", diag.TextList(nm))
			return
		}
		var b strings.Builder
		u.c.exprText(&b, fe.X)
		x := b.String()
		u.err(diag.E0609, fe.Name.Span(), "`"+x+"."+nm+"` names a method without calling it; a method value would capture its receiver"+CallbackRecipe).In(diag.PhaseTypes).
			Text("type", ty).Text("method", nm).
			Alt("call it: `" + x + "." + nm + "(..)`").
			Alt("pass `" + ty + "." + nm + "` and the receiver separately, or a struct plus a `fn`")
	case MemberVariant:
		form := ty + "." + nm
		if vt := u.tab.Get(v); vt.Kind == types.KNamed {
			ed, ok := u.c.declOfItem(u.c.cfg.Items.Get(vt.Item)).(*types.EnumDecl)
			if ok && int(mb.Field) < len(ed.Variants) && len(ed.Variants[mb.Field].Fields) > 0 {
				form += "(..)" // a variant with fields is constructed with them
			}
		}
		u.err(diag.E0203, fe.Name.Span(), "`"+nm+"` is a variant of `"+ty+"`, not a member of its values; write `"+form+"`").
			Text("type", ty).Fact("members", diag.TextList(nm))
	default:
		u.err(diag.E0203, fe.Name.Span(), "`"+ty+"` has no value member `"+nm+"`").
			Text("type", ty).Fact("members", diag.TextList(nm))
	}
}

// rawMember is E0203 for a member reached through a raw pointer the
// member rules do not dereference: a method call on `*T`, or a field
// through a second raw layer. Write the dereference out, `(*p).M()`.
func (u *unit) rawMember(fe *ast.FieldExpr, t types.TypeID) {
	nm := u.c.cfg.Interner.Str(fe.Name.Name)
	var b strings.Builder
	u.c.exprText(&b, fe.X)
	u.err(diag.E0203, fe.Name.Span(), "`"+u.str(t)+"` has no member `"+nm+"`; dereference the raw pointer first").
		Text("type", u.str(t)).Fact("members", diag.TextList()).
		Alt("write `(*" + b.String() + ")." + nm + "` inside an `unsafe \"reason\" { }` block")
}

// typeFieldMiss is E0203 for a field named through its type, `T.f` (a
// value or a call's callee): fields are read from values, so it is
// never a silent Poison.
func (u *unit) typeFieldMiss(fe *ast.FieldExpr, t types.TypeID) {
	nm := u.c.cfg.Interner.Str(fe.Name.Name)
	ty := u.str(t)
	u.err(diag.E0203, fe.Name.Span(), "`"+nm+"` is a field of `"+ty+"`; read it from a value of `"+ty+"`, not from the type").
		Text("type", ty).Fact("members", diag.TextList(nm))
}

// fieldUse records a struct or error field read and returns its type with
// the struct's arguments substituted; a private field of another module is
// E0204 in phase types.
func (u *unit) fieldUse(fe *ast.FieldExpr, v types.TypeID, mb Member) types.TypeID {
	ck, _ := u.c.ctorOf(v)
	it := u.c.cfg.Items.Get(ck.item)
	fields := declFields(u.c.declOfItem(it))
	if int(mb.Field) >= len(fields) {
		return types.Poison
	}
	f := fields[mb.Field]
	*u.b.use(fe.ID()) = Use{Kind: UseField, Item: it.ID, Index: mb.Field}
	u.cFnPointer(fe, it, f.Type) // E1101
	u.x.rec.fields(it.Key, !f.Exported)
	if !f.Exported && it.Module != u.m.ID && it.Kind == types.KindStruct {
		nm := u.c.cfg.Interner.Str(f.Name)
		u.err(diag.E0204, fe.Name.Span(), "field `"+nm+"` of `"+u.str(v)+"` is private to its module").In(diag.PhaseTypes).
			Text("field", nm).Text("type", u.str(v))
	}
	var args []types.GenericArg
	if ty := u.tab.Get(v); ty.Kind == types.KNamed {
		args = ty.Args
	}
	return u.c.subst(f.Type, args, types.NoType)
}

// declFields is the field list of a struct or error declaration.
func declFields(d types.Decl) []types.FieldDecl {
	switch d := d.(type) {
	case *types.StructDecl:
		return d.Fields
	case *types.ErrorDecl:
		return d.Fields
	}
	return nil
}

// builtinNames are the builtin members of t: Must, IsSome, IsNone
// on ?T, Must on E!T, Len on slices, arrays and str. `Or` is E0207
// (orremoved.go).
func (u *unit) builtinNames(t types.TypeID) []string {
	switch u.tab.Kind(t) {
	case types.KOptional:
		return []string{"IsNone", "IsSome", "Must"}
	case types.KErrorUnion:
		return []string{"Must"}
	case types.KSlice, types.KMutSlice, types.KArray, types.KStr:
		return []string{"Len"}
	}
	return nil
}

// builtinMember finds the builtin member of t named n, exactly or folded
// (E0207 at the name for a folded match).
func (u *unit) builtinMember(t types.TypeID, name *ast.Ident) (Builtin, bool) {
	in := u.c.cfg.Interner
	got := in.Str(name.Name)
	for _, b := range u.builtinNames(t) {
		if b != got && resolve.Fold(b) != resolve.Fold(got) {
			continue
		}
		if b != got {
			u.err(diag.E0207, name.Span(), "`"+got+"` is written `"+b+"`").In(diag.PhaseTypes).
				Text("found", got).Text("expected", b).
				Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: name.Span(), Text: b}}, Why: "member names are exact; this is the declared spelling"})
		}
		switch b {
		case "IsNone":
			return BuiltinOptIsNone, true
		case "IsSome":
			return BuiltinOptIsSome, true
		case "Len":
			return BuiltinLen, true
		case "Must":
			if u.tab.Kind(t) == types.KErrorUnion {
				return BuiltinErrMust, true
			}
			return BuiltinOptMust, true
		}
	}
	return BuiltinNone, false
}
