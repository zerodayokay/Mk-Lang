// Why: places and rvalues carry the types every later stage trusts; a
// projection the type does not admit, or an assignment whose rvalue has
// another type, must be caught where MIR is written.
//
// What: the validator's place, downcast, field, scopeField and payload
// checks (invariants M2 and M3), rvalue and aggregate (M4), origin (M9's
// loop origins) and constArgType.
//
// How: each projection step is admitted by the type before it and yields
// the next type; an assignment's place must have the rvalue's type.
//
// Downcast convention: a downcast yields its base type, except SelErr, which
// yields the error-set value ErrorSetType(Errors) of the E!T; the payload is
// reached by the ProjField that follows. Field types of a generic KNamed
// instance are not substituted here (substitution is check's logic): its field
// steps get index checks only.

package mir

import (
	"slices"

	"mk/internal/types"
)

// place checks M2 and M3: each step is admitted by the type before it, and
// returns the place's type.
func (v *validator) place(p Place) types.TypeID {
	b := v.b
	v.local(p.Local)
	v.rng(p.Proj, len(b.Projections), "projection")
	cur := b.Locals[p.Local].Type
	var down *Projection // the step before, when it was a downcast
	for i := range b.Proj(p) {
		pr := &b.Proj(p)[i]
		v.typeOK(pr.Type)
		ty := v.tab.Get(cur)
		switch pr.Kind {
		case ProjDeref:
			if ty.Kind != types.KRef && ty.Kind != types.KMut && ty.Kind != types.KRawPtr {
				v.fail("M3", "deref of %s", v.ts(cur))
			}
			if pr.Type != ty.Elem {
				v.fail("M3", "deref of %s yields %s", v.ts(cur), v.ts(pr.Type))
			}
		case ProjIndex:
			switch ty.Kind {
			case types.KArray, types.KSlice, types.KMutSlice:
				if pr.Type != ty.Elem {
					v.fail("M3", "index of %s yields %s", v.ts(cur), v.ts(pr.Type))
				}
			case types.KStr:
				if pr.Type != types.U8 {
					v.fail("M3", "index of str yields %s", v.ts(pr.Type))
				}
			default:
				v.fail("M3", "index of %s", v.ts(cur))
			}
			v.local(LocalID(pr.A))
			if !v.tab.Get(b.Locals[pr.A].Type).IsInteger() {
				v.fail("M3", "index local _%d is %s", pr.A, v.ts(b.Locals[pr.A].Type))
			}
		case ProjField:
			v.field(cur, ty, pr, down)
		case ProjDowncast:
			v.downcast(cur, ty, pr)
		default:
			v.fail("M3", "projection kind %d", pr.Kind)
		}
		down = nil
		if pr.Kind == ProjDowncast {
			down = pr
		}
		cur = pr.Type
	}
	return cur
}

// downcast checks one ProjDowncast step on a value of type cur.
func (v *validator) downcast(cur types.TypeID, ty *types.Type, pr *Projection) {
	want := cur
	switch ty.Kind {
	case types.KOptional:
		if pr.Sel != SelSome {
			v.fail("M3", "downcast of %s to %s", v.ts(cur), v.sel(pr))
		}
	case types.KErrorUnion:
		switch pr.Sel {
		case SelOk:
		case SelErr:
			want = v.tab.ErrorSetType(ty.Errors)
		default:
			v.fail("M3", "downcast of %s to %s", v.ts(cur), v.sel(pr))
		}
	case types.KErrorSet:
		if pr.Sel != SelCase {
			v.fail("M3", "downcast of %s to %s", v.ts(cur), v.sel(pr))
		}
		ms := v.tab.Members(ty.Errors)
		generic := slices.ContainsFunc(ms, func(m types.ErrorMember) bool { return m.IsParam })
		if !generic && !slices.Contains(ms, types.ErrorMember{Item: types.ItemID(pr.A)}) {
			v.fail("M3", "downcast of %s to error item %d, not a member", v.ts(cur), pr.A)
		}
	case types.KNamed:
		it := v.tab.Items().Get(ty.Item)
		if pr.Sel != SelEnum || it.Kind != types.KindEnum {
			v.fail("M3", "downcast of %s to %s", v.ts(cur), v.sel(pr))
		}
		if d, ok := it.Decl.(*types.EnumDecl); ok && int(pr.A) >= len(d.Variants) {
			v.fail("M3", "variant %d of %s, which has %d", pr.A, v.ts(cur), len(d.Variants))
		}
	default:
		v.fail("M3", "downcast of %s", v.ts(cur))
	}
	if pr.Type != want {
		v.fail("M3", "downcast of %s yields %s, want %s", v.ts(cur), v.ts(pr.Type), v.ts(want))
	}
}

func (v *validator) sel(pr *Projection) string {
	return [...]string{"variant", "some", "ok", "err", "case", "?"}[min(int(pr.Sel), 5)]
}

// field checks one ProjField step on a value of type cur; down is the
// downcast just before it, or nil.
func (v *validator) field(cur types.TypeID, ty *types.Type, pr *Projection, down *Projection) {
	if pr.FieldLoop {
		if v.b.GenericCount == 0 {
			v.fail("M2", "fields-loop step in an instance body")
		}
		switch ty.Kind {
		case types.KNamed, types.KRecord, types.KErrorSet, types.KParam:
		default:
			v.fail("M3", "fields-loop step on %s", v.ts(cur))
		}
		v.loop(LoopID(pr.A))
		return
	}
	var fields []types.FieldDecl // the declared fields, when known
	known, generic := false, false
	switch {
	case down != nil && down.Sel == SelSome:
		v.payload(cur, ty, pr)
		return
	case down != nil && down.Sel == SelOk:
		v.payload(cur, ty, pr)
		return
	case down != nil && down.Sel == SelEnum:
		if d, ok := v.tab.Items().Get(ty.Item).Decl.(*types.EnumDecl); ok {
			fields, known = d.Variants[down.A].Fields, true
		}
		generic = len(ty.Args) > 0
	case down != nil && down.Sel == SelCase:
		fields, known = v.errorFields(types.ItemID(down.A))
	case down != nil:
		v.fail("M3", "field of %s after a downcast to %s", v.ts(cur), v.sel(down))
	case ty.Kind == types.KArray:
		// A constant element step: element A of [N]T yields T.
		if n, ok := v.constLen(ty.Len); ok && uint64(pr.A) >= n {
			v.fail("M3", "element %d of %s", pr.A, v.ts(cur))
		}
		if ty.Elem != pr.Type {
			v.fail("M3", "element %d of %s yields %s", pr.A, v.ts(cur), v.ts(pr.Type))
		}
		return
	case ty.Kind == types.KRecord:
		if int(pr.A) >= len(ty.Fields) {
			v.fail("M3", "field %d of %s", pr.A, v.ts(cur))
		}
		if ty.Fields[pr.A].Type != pr.Type {
			v.fail("M3", "field %d of %s yields %s", pr.A, v.ts(cur), v.ts(pr.Type))
		}
		return
	case ty.Kind == types.KNamed && v.scopeOfErrors(ty):
		v.scopeField(cur, ty, pr)
		return
	case ty.Kind == types.KNamed:
		it := v.tab.Items().Get(ty.Item)
		if it.Kind != types.KindStruct {
			v.fail("M3", "field of %s %s without a downcast", it.Kind, v.ts(cur))
		}
		if d, ok := it.Decl.(*types.StructDecl); ok {
			fields, known = d.Fields, true
		}
		generic = len(ty.Args) > 0
	case ty.Kind == types.KErrorSet:
		// A single error is the set of one: its fields need no downcast.
		ms := v.tab.Members(ty.Errors)
		if len(ms) != 1 || ms[0].IsParam {
			v.fail("M3", "field of %s without a downcast to one error", v.ts(cur))
		}
		fields, known = v.errorFields(ms[0].Item)
	default:
		v.fail("M3", "field of %s", v.ts(cur))
	}
	if !known {
		return
	}
	if int(pr.A) >= len(fields) {
		v.fail("M3", "field %d of %s, which has %d", pr.A, v.ts(cur), len(fields))
	}
	if !generic && fields[pr.A].Type != pr.Type {
		v.fail("M3", "field %d of %s yields %s, declared %s", pr.A, v.ts(cur), v.ts(pr.Type), v.ts(fields[pr.A].Type))
	}
}

// scopeOfErrors reports whether ty is Scope[E] as check spells it: the
// compiler-known Scope item, declared with no generics, applied to one type
// argument that is an error set. Without Env.Known it is never recognized, and
// M3 reads the declared fields.
func (v *validator) scopeOfErrors(ty *types.Type) bool {
	if v.env.Known == nil || v.env.Known.Scope == types.NoItem || ty.Item != v.env.Known.Scope ||
		len(ty.Args) != 1 || ty.Args[0].IsConst || v.tab.Kind(ty.Args[0].Type) != types.KErrorSet {
		return false
	}
	if d, ok := v.tab.Items().Get(ty.Item).Decl.(*types.StructDecl); ok && len(d.Generics) > 0 {
		return false
	}
	return true
}

// scopeField checks a field step on Scope[E], whose layout is the two fields
// {hdr: Scope, err: E!void} (check's Layout): field 0 has the plain Scope
// type, field 1 the type E!void over the argument's error set. Every consumer
// that reads a struct's fields from its declaration (mono, iface, borrow's
// place paths) reads these two.
func (v *validator) scopeField(cur types.TypeID, ty *types.Type, pr *Projection) {
	got := v.tab.Get(pr.Type)
	switch pr.A {
	case 0:
		if got.Kind != types.KNamed || got.Item != ty.Item || len(got.Args) != 0 {
			v.fail("M3", "field 0 of %s yields %s, want the plain Scope", v.ts(cur), v.ts(pr.Type))
		}
	case 1:
		es := v.tab.Get(ty.Args[0].Type).Errors
		if got.Kind != types.KErrorUnion || got.Errors != es || got.Elem != types.Void {
			v.fail("M3", "field 1 of %s yields %s, want its E!void", v.ts(cur), v.ts(pr.Type))
		}
	default:
		v.fail("M3", "field %d of %s, which has 2 (hdr, err)", pr.A, v.ts(cur))
	}
}

// payload checks the field step after a some or ok downcast: field 0, the
// optional's or union's element.
func (v *validator) payload(cur types.TypeID, ty *types.Type, pr *Projection) {
	if pr.A != 0 || pr.Type != ty.Elem {
		v.fail("M3", "payload of %s is field 0 of %s, not field %d of %s", v.ts(cur), v.ts(ty.Elem), pr.A, v.ts(pr.Type))
	}
}

func (v *validator) errorFields(item types.ItemID) ([]types.FieldDecl, bool) {
	if d, ok := v.tab.Items().Get(item).Decl.(*types.ErrorDecl); ok {
		return d.Fields, true
	}
	return nil, false
}

// rvalue checks an rvalue assigned to a place of type dest and returns its
// type (M4). For rvalues whose type the destination decides (aggregates
// without a type of their own, fn and const references), it checks them
// against dest and returns dest.
func (v *validator) rvalue(r *Rvalue, holders []int, destPlace Place, dest types.TypeID) types.TypeID {
	tab := v.tab
	switch r.Kind {
	case RvUse:
		return v.operand(r.A)
	case RvRef:
		pt := v.place(r.Place)
		v.origin(r, holders, destPlace)
		if r.Borrow == Exclusive {
			return tab.Mut(pt)
		}
		return tab.Ref(pt)
	case RvSlice:
		pt := v.place(r.Place)
		v.origin(r, holders, destPlace)
		if r.HasLo && !tab.Get(v.operand(r.A)).IsInteger() {
			v.fail("M4", "slice bound is not an integer")
		}
		if r.HasHi && !tab.Get(v.operand(r.B)).IsInteger() {
			v.fail("M4", "slice bound is not an integer")
		}
		ty := tab.Get(pt)
		switch ty.Kind {
		case types.KStr:
			return types.Str
		case types.KArray, types.KSlice, types.KMutSlice:
			if r.Borrow == Exclusive {
				return tab.MutSlice(ty.Elem)
			}
			return tab.Slice(ty.Elem)
		}
		v.fail("M4", "slice of %s", v.ts(pt))
	case RvUnary:
		return v.operand(r.A)
	case RvBinary:
		a, bt := v.operand(r.A), v.operand(r.B)
		if r.Bin == Shl || r.Bin == Shr {
			return a
		}
		if a != bt {
			v.fail("M4", "binary operands %s and %s", v.ts(a), v.ts(bt))
		}
		if r.Bin > MulNoWrap {
			v.fail("M4", "unknown binary operator %d", r.Bin)
		}
		if r.Bin >= AddNoWrap && r.Bin <= MulNoWrap { // the no-wrap forms
			if k := tab.Kind(a); k != types.KInt && k != types.KIntPtr {
				v.fail("M4", "%s on %s, not an integer", binNames[r.Bin], v.ts(a))
			}
		}
		if r.Bin.IsCompare() {
			return types.Bool
		}
		return a
	case RvCast:
		v.operand(r.A)
		v.typeOK(r.Type)
		return r.Type
	case RvAggregate:
		return v.aggregate(r, dest)
	case RvRepeat:
		el := v.operand(r.A)
		v.constArgOK(r.Len)
		dt := tab.Get(dest)
		if dt.Kind != types.KArray || dt.Elem != el || dt.Len != r.Len {
			v.fail("M4", "repeat of %s into %s", v.ts(el), v.ts(dest))
		}
		return dest
	case RvLen:
		v.place(r.Place)
		return types.Int
	case RvDiscriminant:
		v.place(r.Place)
		return types.U64
	case RvFnRef:
		v.rng(r.Fn.Args, len(v.b.GenericArgs), "generic argument")
		v.genericArgs(r.Fn.Args)
		if tab.Kind(dest) != types.KFn {
			v.fail("M4", "fn reference into %s", v.ts(dest))
		}
		if v.env.Callee != nil && r.Fn.Args.Len == 0 {
			if sig := v.env.Callee(r.Fn.Item); sig != nil && len(sig.Generics) == 0 && !v.fits(sig.FnType(tab), dest) {
				v.fail("M4", "fn reference of type %s into %s", v.ts(sig.FnType(tab)), v.ts(dest))
			}
		}
		return dest
	case RvGlobalRef, RvConstRef:
		if r.Item == types.NoItem {
			v.fail("M2", "no item")
		}
		switch d := tab.Items().Get(r.Item).Decl.(type) {
		case *types.GlobalDecl:
			if r.Kind != RvGlobalRef || dest != tab.Ref(d.Type) {
				v.fail("M4", "global of %s into %s", v.ts(d.Type), v.ts(dest))
			}
		case *types.ConstDecl:
			// &C is RvGlobalRef of the const, a static reference.
			if r.Kind == RvConstRef && dest != d.Type || r.Kind == RvGlobalRef && dest != tab.Ref(d.Type) {
				v.fail("M4", "const of %s into %s", v.ts(d.Type), v.ts(dest))
			}
		}
		return dest
	case RvSizeOf, RvAlignOf, RvFieldCount:
		v.typeOK(r.Type)
		return types.Int
	case RvTypeIs:
		if v.b.GenericCount == 0 {
			v.fail("M2", "TypeIs in an instance body")
		}
		v.typeOK(r.Type)
		return types.Bool
	case RvFieldName:
		if r.Field.Loop && v.b.GenericCount == 0 {
			v.fail("M2", "fields-loop name in an instance body")
		}
		return types.Str
	case RvConstArg:
		v.constArgOK(r.Len)
		v.typeOK(r.Type)
		v.constArgType(r)
		return r.Type
	default:
		v.fail("M2", "rvalue kind %d", r.Kind)
	}
	return types.NoType
}

// aggregate checks an RvAggregate against its destination type (M4). An
// operand fits its field, element or payload by v.fits.
func (v *validator) aggregate(r *Rvalue, dest types.TypeID) types.TypeID {
	tab := v.tab
	v.rng(r.Ops, len(v.b.Operands), "operand")
	var ops []types.TypeID
	for _, o := range v.b.Operands[r.Ops.Start:r.Ops.End()] {
		ops = append(ops, v.operand(o))
	}
	dt := tab.Get(dest)
	one := func(what string) types.TypeID {
		if len(ops) != 1 {
			v.fail("M4", "%s of %d operands", what, len(ops))
		}
		return ops[0]
	}
	// fieldsOK checks operand count and, when the owner is not generic,
	// operand types against declared fields.
	fieldsOK := func(fields []types.FieldDecl, generic bool) {
		if len(ops) != len(fields) {
			v.fail("M4", "%d operands for %d fields", len(ops), len(fields))
		}
		for i, f := range fields {
			if !generic && !v.fits(ops[i], f.Type) {
				v.fail("M4", "operand %d is %s, field is %s", i, v.ts(ops[i]), v.ts(f.Type))
			}
		}
	}
	switch r.Agg {
	case AggRecord:
		v.typeOK(r.Type)
		rt := tab.Get(r.Type)
		if rt.Kind != types.KRecord || len(ops) != len(rt.Fields) {
			v.fail("M4", "record aggregate of %s with %d operands", v.ts(r.Type), len(ops))
		}
		for i, f := range rt.Fields {
			if !v.fits(ops[i], f.Type) {
				v.fail("M4", "operand %d is %s, field is %s", i, v.ts(ops[i]), v.ts(f.Type))
			}
		}
		return r.Type
	case AggArray:
		v.typeOK(r.Type)
		at := tab.Get(r.Type)
		if at.Kind == types.KSlice {
			// The static slice of a const initializer, its
			// operands the elements.
			for i, o := range ops {
				if !v.fits(o, at.Elem) {
					v.fail("M4", "element %d is %s, want %s", i, v.ts(o), v.ts(at.Elem))
				}
			}
			return r.Type
		}
		if at.Kind != types.KArray {
			v.fail("M4", "array aggregate of %s", v.ts(r.Type))
		}
		if n, ok := v.constLen(at.Len); ok && n != uint64(len(ops)) {
			v.fail("M4", "array aggregate of %s with %d operands", v.ts(r.Type), len(ops))
		}
		for i, o := range ops {
			if !v.fits(o, at.Elem) {
				v.fail("M4", "element %d is %s, want %s", i, v.ts(o), v.ts(at.Elem))
			}
		}
		return r.Type
	case AggSome:
		if o := one("some"); dt.Kind != types.KOptional || !v.fits(o, dt.Elem) {
			v.fail("M4", "some(%s) into %s", v.ts(o), v.ts(dest))
		}
	case AggOk:
		if len(ops) == 0 { // the success of E!void
			if dt.Kind != types.KErrorUnion || dt.Elem != types.Void {
				v.fail("M4", "ok() into %s", v.ts(dest))
			}
			break
		}
		if o := one("ok"); dt.Kind != types.KErrorUnion || !v.fits(o, dt.Elem) {
			v.fail("M4", "ok(%s) into %s", v.ts(o), v.ts(dest))
		}
	case AggNone:
		if len(ops) != 0 || dt.Kind != types.KOptional {
			v.fail("M4", "none of %d operands into %s", len(ops), v.ts(dest))
		}
	case AggErr:
		o := one("err")
		ot := tab.Get(o)
		if dt.Kind != types.KErrorUnion || ot.Kind != types.KErrorSet || !tab.Subset(ot.Errors, dt.Errors) {
			v.fail("M4", "err(%s) into %s", v.ts(o), v.ts(dest))
		}
	case AggStruct, AggEnum:
		if dt.Kind != types.KNamed || dt.Item != r.Item {
			v.fail("M4", "aggregate of item %d into %s", r.Item, v.ts(dest))
		}
		switch d := tab.Items().Get(r.Item).Decl.(type) {
		case *types.StructDecl:
			if r.Agg != AggStruct {
				v.fail("M4", "enum aggregate of struct %s", v.ts(dest))
			}
			fieldsOK(d.Fields, len(dt.Args) > 0)
		case *types.EnumDecl:
			if r.Agg != AggEnum || int(r.Variant) >= len(d.Variants) {
				v.fail("M4", "variant %d of %s", r.Variant, v.ts(dest))
			}
			fieldsOK(d.Variants[r.Variant].Fields, len(dt.Args) > 0)
		}
	case AggError:
		if dt.Kind != types.KErrorSet || !slices.Contains(tab.Members(dt.Errors), types.ErrorMember{Item: r.Item}) {
			v.fail("M4", "error aggregate of item %d into %s", r.Item, v.ts(dest))
		}
		if fields, ok := v.errorFields(r.Item); ok {
			fieldsOK(fields, false)
		}
	default:
		v.fail("M2", "aggregate kind %d", r.Agg)
	}
	return dest
}

// constLen is the value of a CAValue length, when the table has consts.
func (v *validator) constLen(c types.ConstArg) (uint64, bool) {
	ct := v.tab.Consts()
	if c.Kind != types.CAValue || ct == nil {
		return 0, false
	}
	cv := ct.Get(c.Value)
	if n := len(cv.Bytes); n == 1 || n == 2 || n == 4 || n == 8 {
		return types.LoadUint(cv.Bytes, n), true
	}
	return 0, false
}

// origin checks M9 for a borrow: a holder loan is the loop's holder's one
// assignment; an element loan lies inside its loop's blocks.
func (v *validator) origin(r *Rvalue, holders []int, dest Place) {
	switch r.Origin {
	case OriginPlain:
	case OriginLoopHolder:
		v.loop(r.Loop)
		if h := v.b.Loops[r.Loop].Holder; h == NoLocal || dest.Local != h || dest.Proj.Len != 0 {
			v.fail("M9", "holder loan of loop %d not assigned to its holder", r.Loop)
		}
		holders[r.Loop]++
	case OriginLoopElement:
		v.loop(r.Loop)
		if !v.inLoop(r.Loop, v.curBlock) {
			v.fail("M9", "element loan of loop %d outside its blocks", r.Loop)
		}
	default:
		v.fail("M2", "borrow origin %d", r.Origin)
	}
}

// inLoop reports whether block bl lies in loop l: reachable from its header
// without passing through its exit (memoized per loop).
func (v *validator) inLoop(l LoopID, bl BlockID) bool {
	if v.loopBlocks == nil {
		v.loopBlocks = make(map[LoopID][]bool)
	}
	in, ok := v.loopBlocks[l]
	if !ok {
		lp := v.b.Loops[l]
		in = make([]bool, len(v.b.Blocks))
		work := []BlockID{lp.Header}
		var buf []BlockID
		for len(work) > 0 {
			x := work[len(work)-1]
			work = work[:len(work)-1]
			if x == lp.Exit || int(x) >= len(in) || in[x] {
				continue
			}
			in[x] = true
			buf = v.b.Successors(x, buf[:0])
			work = append(work, buf...)
		}
		v.loopBlocks[l] = in
	}
	return int(bl) < len(in) && in[bl]
}

// constArgType checks RvConstArg's Type against the declared type of the
// const parameter it reads (with the body's signature) or of the value.
func (v *validator) constArgType(r *Rvalue) {
	switch r.Len.Kind {
	case types.CAParam:
		if s := v.env.Sig; s != nil {
			if int(r.Len.Param) >= len(s.Generics) || !s.Generics[r.Len.Param].Const {
				v.fail("M4", "const argument $%d is not a const parameter", r.Len.Param)
			}
			if g := s.Generics[r.Len.Param]; g.Type != r.Type {
				v.fail("M4", "const parameter $%d is %s, read as %s", r.Len.Param, v.ts(g.Type), v.ts(r.Type))
			}
		}
	case types.CAValue:
		if ct := v.tab.Consts(); ct != nil && ct.Get(r.Len.Value).Type != r.Type {
			v.fail("M4", "const value of %s read as %s", v.ts(ct.Get(r.Len.Value).Type), v.ts(r.Type))
		}
	case types.CASizeOf:
	default:
		v.fail("M2", "const argument kind %d", r.Len.Kind)
	}
}
