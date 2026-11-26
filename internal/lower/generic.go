// Why: generic bodies are lowered once, not per instance, so the constructs
// that depend on a type parameter (if T is X, for over fields) need a
// generic MIR form that mono later resolves.
//
// What: typeTest (if T is X as a TermTypeTest with narrowing copies and
// write-backs), narrowReborrow, substArgs and substParam, fieldsLoop with
// fieldsGeneric and fieldsUnrolled, and trim, the pass that drops needless
// scope ends.
//
// How: inside a type-test branch, the locals the branch names are copied
// (or reborrowed, for mut references) at the narrowed type and written
// back after. A fields loop is a generic loop over FieldRef, or one unrolled
// copy of the body per field when the type is concrete.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func (bl *builder) remapped(l mir.LocalID) mir.LocalID {
	if n, ok := bl.remap[l]; ok {
		return n
	}
	return l
}

// typeTest is if T is X { A } else { B }.
func (bl *builder) typeTest(s *ast.IfStmt, c *ast.IsCond) {
	param := -1
	for i, g := range bl.sig.Generics {
		if !g.Const && g.Name == c.Name.Name {
			param = i
		}
	}
	x := bl.in.Type(c.Type.ID())
	if param < 0 || x == types.NoType || x == types.Poison {
		source.ICEf(c.Span(), "lower: if T is X with no parameter or type")
	}
	yes, no, join := bl.newBlock(), bl.newBlock(), bl.newBlock()
	bl.term(mir.Terminator{Kind: mir.TermTypeTest, Param: uint32(param), Type: x, Yes: yes, No: no}, c.Span())
	bl.cur = yes
	bl.openScope(scopeTypeTestYes)
	bl.scopes[len(bl.scopes)-1].ttParam, bl.scopes[len(bl.scopes)-1].ttType = uint32(param), x
	saved := bl.remap
	bl.remap = map[mir.LocalID]mir.LocalID{}
	for l := range saved {
		bl.remap[l] = saved[l]
	}
	bl.scopes[len(bl.scopes)-1].remap = bl.remap
	writes := writtenNames(s.Then)
	var named map[source.Symbol]bool
	var cands []mir.LocalID
	for i := 1; i <= int(bl.b.ParamCount); i++ {
		cands = append(cands, mir.LocalID(i))
	}
	for _, sc := range bl.scopes {
		cands = append(cands, sc.locals...)
	}
	for _, l := range cands {
		if bl.valueDests[l] > 0 {
			// A value construct's destination, unassigned until an arm
			// assigns it: narrowing it would read it first (L7R-5). The
			// arm's value is retyped to it by armAssign instead.
			continue
		}
		orig := l
		l = bl.remapped(l)
		lt := bl.b.Locals[l].Type
		nt := bl.substParam(lt, uint32(param), x)
		if nt == lt {
			continue
		}
		loc := bl.b.Locals[l]
		if !bl.mentionsOutsideFn(lt, uint32(param)) {
			// T is only in fn types of lt: the value is copied at X only
			// when the branch names it, as one it never calls needs none.
			if named == nil {
				named = namesIn(s.Then)
			}
			if !named[loc.Name] {
				continue
			}
		}
		plainBefore := bl.plain(lt, c.Span())
		plainAfter := bl.plain(nt, c.Span())
		if !plainBefore && !plainAfter {
			if named == nil {
				named = namesIn(s.Then)
			}
			// Only a reference the branch names is reborrowed: an
			// exclusive reborrow of one it never touches would conflict
			// with a loan of its referent the source holds.
			if bl.tab.Kind(lt) == types.KMut && named[loc.Name] {
				bl.narrowReborrow(l, lt, nt, c.Span())
				bl.remap[orig] = bl.remap[l]
			}
			continue
		}
		n := bl.b.AddLocal(mir.Local{Type: nt, Kind: mir.LocalUser, Name: loc.Name, Var: loc.Var, Loop: mir.NoLoop, Span: loc.Span})
		src := mir.Move(bl.b.Place(l))
		if plainBefore {
			src = mir.Copy(bl.b.Place(l))
		}
		bl.assign(bl.b.Place(n), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: src, Type: nt}, c.Span())
		sc := &bl.scopes[len(bl.scopes)-1]
		sc.locals = append(sc.locals, n)
		sc.narrowed = append(sc.narrowed, narrowRec{orig: l, narrowed: n, writeBack: plainBefore && loc.Var && writes[loc.Name]})
		bl.remap[l] = n
		bl.remap[orig] = n // a name reads orig: a chain of narrowings ends at n
	}
	bl.armStmts(s.Then)
	bl.closeScope(closeBrace(s.Then))
	bl.remap = saved
	reached := bl.joinTo(join, s.Span())
	bl.cur = no
	if s.Else != nil {
		bl.elseBranch(s.Else)
	}
	reached = bl.joinTo(join, s.Span()) || reached
	bl.continueAt(join, reached)
}

// narrowReborrow narrows a `mut` reference local l (`a: mut T`) inside
// `if T is X` through a reborrow, `n = mut *l` cast to nt: the reference is
// not plain, so it is neither copied nor moved, and writes through n reach
// l's referent with no write-back. Without it `**a` kept the type `T` and
// lower failed with "deref of T".
func (bl *builder) narrowReborrow(l mir.LocalID, lt, nt types.TypeID, at source.Span) {
	elem := bl.tab.Get(lt).Elem
	tmp := bl.newTemp(lt, at)
	bl.assign(bl.b.Place(tmp), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive,
		Place: bl.b.Place(l, mir.Projection{Kind: mir.ProjDeref, Type: elem}), Origin: mir.OriginPlain, Type: lt}, at)
	loc := bl.b.Locals[l]
	n := bl.b.AddLocal(mir.Local{Type: nt, Kind: mir.LocalUser, Name: loc.Name, Loop: mir.NoLoop, Span: loc.Span})
	bl.assign(bl.b.Place(n), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastNarrow, A: mir.Move(bl.b.Place(tmp)), Type: nt}, at)
	sc := &bl.scopes[len(bl.scopes)-1]
	sc.locals = append(sc.locals, n)
	bl.remap[l] = n
}

// namesIn are the names a block mentions anywhere, read or written.
func namesIn(b *ast.Block) map[source.Symbol]bool {
	out := map[source.Symbol]bool{}
	ast.Inspect(b, func(n ast.Node) bool {
		if x, ok := n.(*ast.Ident); ok {
			out[x.Name] = true
		}
		return n != nil
	})
	return out
}

// writtenNames are the names a block may write: assignment targets' roots,
// mut borrows and receivers' roots (a syntactic scan).
func writtenNames(b *ast.Block) map[source.Symbol]bool {
	out := map[source.Symbol]bool{}
	root := func(e ast.Expr) {
		for {
			switch x := e.(type) {
			case *ast.Ident:
				out[x.Name] = true
				return
			case *ast.FieldExpr:
				e = x.X
			case *ast.IndexExpr:
				e = x.X
			case *ast.ParenExpr:
				e = x.X
			case *ast.UnaryExpr:
				e = x.X
			default:
				return
			}
		}
	}
	ast.Inspect(b, func(n ast.Node) bool {
		switch x := n.(type) {
		case nil:
			return false
		case *ast.AssignStmt:
			root(x.Target)
		case *ast.AsmStmt:
			for _, op := range x.Operands { // an asm output writes its place
				if op.Kind.IsDirection() && op.Kind != ast.AsmIn && op.Value != nil {
					root(op.Value)
				}
			}
		case *ast.UnaryExpr:
			if x.Op == ast.TokMut {
				root(x.X)
			}
		case *ast.CallExpr:
			if fe, ok := methodField(x.Fun); ok { // x.M or x.M[A]
				root(fe.X)
			}
		}
		return true
	})
	return out
}

// substArgs replaces every KParam(i) of a declaration's type t by args[i]
// at once (a field type under its owner's generic arguments). A const
// argument leaves its parameter in place.
func (bl *builder) substArgs(t types.TypeID, args []types.GenericArg) types.TypeID {
	if len(args) == 0 {
		return t
	}
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		if int(ty.Index) < len(args) && !args[ty.Index].IsConst {
			return args[ty.Index].Type
		}
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KArray, types.KErrorUnion:
		if e := bl.substArgs(ty.Elem, args); e != ty.Elem {
			n := *ty
			n.Elem = e
			return bl.tab.Intern(n)
		}
	case types.KNamed:
		var out []types.GenericArg
		for k, a := range ty.Args {
			if a.IsConst {
				continue
			}
			if na := bl.substArgs(a.Type, args); na != a.Type {
				if out == nil {
					out = append([]types.GenericArg(nil), ty.Args...)
				}
				out[k].Type = na
			}
		}
		if out != nil {
			return bl.tab.Named(ty.Item, out)
		}
	case types.KRecord:
		var fs []types.RecordField
		for k, f := range ty.Fields {
			if nf := bl.substArgs(f.Type, args); nf != f.Type {
				if fs == nil {
					fs = append([]types.RecordField(nil), ty.Fields...)
				}
				fs[k].Type = nf
			}
		}
		if fs != nil {
			return bl.tab.Record(fs)
		}
	}
	return t
}

// mentionsOutsideFn reports whether KParam(i) occurs in t outside every fn
// type, where substParam changes t without looking into a fn type.
func (bl *builder) mentionsOutsideFn(t types.TypeID, i uint32) bool {
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		return ty.Index == i
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KArray, types.KErrorUnion:
		return bl.mentionsOutsideFn(ty.Elem, i)
	case types.KNamed:
		for _, a := range ty.Args {
			if !a.IsConst && bl.mentionsOutsideFn(a.Type, i) {
				return true
			}
		}
	case types.KRecord:
		for _, f := range ty.Fields {
			if bl.mentionsOutsideFn(f.Type, i) {
				return true
			}
		}
	}
	return false
}

// substParam replaces KParam(i) by x throughout t.
func (bl *builder) substParam(t types.TypeID, i uint32, x types.TypeID) types.TypeID {
	ty := bl.tab.Get(t)
	switch ty.Kind {
	case types.KParam:
		if ty.Index == i {
			return x
		}
	case types.KRef, types.KMut, types.KSlice, types.KMutSlice, types.KRawPtr, types.KOptional, types.KArray, types.KErrorUnion:
		e := bl.substParam(ty.Elem, i, x)
		if e != ty.Elem {
			n := *ty
			n.Elem = e
			return bl.tab.Intern(n)
		}
	case types.KNamed:
		var args []types.GenericArg
		for k, a := range ty.Args {
			if !a.IsConst {
				if na := bl.substParam(a.Type, i, x); na != a.Type {
					if args == nil {
						args = append([]types.GenericArg(nil), ty.Args...)
					}
					args[k].Type = na
				}
			}
		}
		if args != nil {
			return bl.tab.Named(ty.Item, args)
		}
	case types.KRecord:
		var fs []types.RecordField
		for k, f := range ty.Fields {
			if nf := bl.substParam(f.Type, i, x); nf != f.Type {
				if fs == nil {
					fs = append([]types.RecordField(nil), ty.Fields...)
				}
				fs[k].Type = nf
			}
		}
		if fs != nil {
			return bl.tab.Record(fs)
		}
	case types.KFn:
		fn := *ty.Fn
		changed := false
		fn.Params = append([]types.FnParam(nil), ty.Fn.Params...)
		for k, p := range fn.Params {
			if np := bl.substParam(p.Type, i, x); np != p.Type {
				fn.Params[k].Type, changed = np, true
			}
		}
		if r := bl.substParam(fn.Result, i, x); r != fn.Result {
			fn.Result, changed = r, true
		}
		if changed {
			return bl.tab.Fn(fn)
		}
	}
	return t
}

// fieldsLoop is for name, value in fields(&x). The base place is
// the struct or record value itself, never a deref of it; it is computed
// from the body's own tables, before any copy is entered. In a
// generic body (value: &KParam(hidden)) one copy of the body in generic
// form, which mono unrolls; otherwise check's copies, one per field.
func (bl *builder) fieldsLoop(s *ast.ForStmt, h *ast.InHead, src *ast.FieldsSource) {
	hs := headerSpan(s)
	if h.Value == nil {
		source.ICEf(hs, "lower: fields loop with one name")
	}
	var base mir.Place
	if u, ok := unparen(src.X).(*ast.UnaryExpr); ok && u.Op == ast.TokAmp && bl.isPlaceExpr(u.X) {
		base = bl.place(u.X)
	} else {
		r := bl.newTemp(bl.typeOf(src.X), hs)
		bl.assign(bl.b.Place(r), use(bl.operand(src.X)), hs)
		base = bl.b.Place(r)
		for bl.tab.Kind(bl.b.PlaceType(base)) == types.KRef || bl.tab.Kind(bl.b.PlaceType(base)) == types.KMut {
			base = bl.deref(base, hs)
		}
	}
	vtype := types.NoType
	if vb := bl.in.Binding(h.Value.ID()); vb.OK {
		vtype = vb.Type
	} else if blank(h.Value) {
		vtype = bl.in.Type(h.Value.ID()) // check types a blank value of the generic form too
	}
	if vtype != types.NoType && vtype != types.Poison {
		vt := bl.tab.Get(vtype)
		if vt.Kind == types.KRef && bl.tab.Kind(vt.Elem) == types.KParam && bl.tab.Get(vt.Elem).Index >= uint32(len(bl.sig.Generics)) {
			bl.fieldsGeneric(s, h, base, vt)
			return
		}
	}
	bl.fieldsUnrolled(s, h, base)
}

// fieldsGeneric is the generic form: blocks exactly [Header, Exit), the
// value FieldRef{Loop}, the name RvFieldName.
func (bl *builder) fieldsGeneric(s *ast.ForStmt, h *ast.InHead, base mir.Place, vt *types.Type) {
	hs := headerSpan(s)
	name, value := h.Key, h.Value
	hidden := bl.tab.Get(vt.Elem).Index
	of := bl.b.PlaceType(base)
	const pendingExit = mir.NoBlock - 1
	header := bl.newBlock()
	bl.gotoBlock(header, hs)
	body, latch := bl.newBlock(), bl.newBlock()
	id := bl.beginLoop(s, header, latch, pendingExit)
	bl.b.Loops[id].FieldsOf, bl.b.Loops[id].FieldsParam = of, hidden
	bl.cur = header
	bl.gotoBlock(body, hs)
	bl.cur = body
	bl.openScope(scopeLoopBody)
	fp := bl.extend(base, mir.Projection{Kind: mir.ProjField, FieldLoop: true, A: uint32(id), Type: vt.Elem})
	if !blank(name) {
		nl := bl.declare(name)
		bl.assign(bl.b.Place(nl), mir.Rvalue{Kind: mir.RvFieldName, Field: mir.FieldRef{Loop: true, Index: uint32(id)}, Type: types.Str}, name.Span())
	}
	if !blank(value) {
		vl := bl.declare(value)
		bl.assign(bl.b.Place(vl), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: fp, Type: bl.b.Locals[vl].Type}, value.Span())
	}
	bl.blockStmts(s.Body)
	bl.closeScope(closeBrace(s.Body))
	bl.joinTo(latch, closeBrace(s.Body))
	bl.loops = bl.loops[:len(bl.loops)-1]
	exit := bl.newBlock()
	bl.cur = latch
	bl.gotoBlock(exit, hs)
	for i := int(header); i < int(exit); i++ {
		t := &bl.b.Blocks[i].Term
		if t.Kind == mir.TermGoto && t.Target == pendingExit {
			t.Target = exit
		}
	}
	bl.b.Loops[id].Exit = exit
	bl.cur = exit
}

// fieldsUnrolled is the concrete form (a struct in its own module, or a
// record): the body lowered once per copy check made, in field order, with
// name the field's name (a str const) and value &base.Field(k). Each copy
// is a loop of its own (one LoopInfo, so labels work): continue goes to the
// next copy's start, break to one exit past the last copy. A copy no edge
// reaches is not lowered, nor is any after it.
func (bl *builder) fieldsUnrolled(s *ast.ForStmt, h *ast.InHead, base mir.Place) {
	hs := headerSpan(s)
	copies := bl.in.FieldCopies(s.ID())
	starts := make([]mir.BlockID, len(copies)+1)
	for i := range copies {
		starts[i] = bl.newBlock()
	}
	exit := bl.newBlock()
	starts[len(copies)] = exit
	reached := bl.joinTo(starts[0], hs)
	broken := false
	for i, fc := range copies {
		if !reached {
			break
		}
		bl.in.EnterCopy(fc)
		bl.cur = starts[i]
		bl.beginLoop(s, starts[i], starts[i+1], exit)
		bl.openScope(scopeLoopBody)
		if !blank(h.Key) {
			nl := bl.declare(h.Key)
			op, ok := bl.literal(h.Key)
			if !ok {
				source.ICEf(h.Key.Span(), "lower: a fields loop copy's name has no value")
			}
			bl.assign(bl.b.Place(nl), use(op), h.Key.Span())
		}
		if !blank(h.Value) {
			vl := bl.declare(h.Value)
			vt := bl.b.Locals[vl].Type
			fp := bl.field(base, fc.Field, bl.tab.Get(vt).Elem)
			bl.assign(bl.b.Place(vl), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: fp, Type: vt}, h.Value.Span())
		}
		bl.blockStmts(s.Body)
		bl.closeScope(closeBrace(s.Body))
		reached = bl.joinTo(starts[i+1], closeBrace(s.Body))
		lc := bl.loops[len(bl.loops)-1]
		bl.loops = bl.loops[:len(bl.loops)-1]
		reached = reached || lc.continued
		broken = broken || lc.broken
		bl.in.LeaveCopy()
	}
	bl.continueAt(exit, reached || broken)
}

// blank reports whether a loop name is absent or _ (it binds nothing).
func blank(id *ast.Ident) bool { return id == nil || id.Name == source.SymUnderscore }

// trim removes the scope ends of plain locals that are never the root of a
// borrow, when Config.Trim is set.
func trim(bl *builder) {
	b := bl.b
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		out := blk.Stmts[:0]
		for _, st := range blk.Stmts {
			if st.Kind == mir.StScopeEnd {
				l := st.Local
				borrowed := int(l) < len(bl.borrowed) && bl.borrowed[l]
				if f, ok := bl.in.Facts(b.Locals[l].Type); ok && f.Kind == types.Plain && !borrowed {
					continue
				}
			}
			out = append(out, st)
		}
		blk.Stmts = out
	}
}
