// Why: match is the language's main branching form, over unions, enums,
// errors, literals, ranges and structs, and its lowering (scrutinee forms,
// binding modes, pattern tests) is large enough for a file of its own.
//
// What: matchStmt, matchUnion and matchCall (match F(x) with ok and err
// arms), unionArms for their two sides; test, rangeTest, subTests and
// altTests for pattern tests; bindPat for bindings, with takeWhole for a
// linear struct of plain fields and takeAlts for alternatives of struct
// patterns; arms for the sequential fallback, where an arm of alternatives
// takes each apart on its own edge.
//
// How: arms are compiled by the decision tree of decision.go or, past its
// size guard, by sequential testing: the first matching arm in source order,
// one test chain per arm. A non-place scrutinee is materialized once.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func (bl *builder) matchStmt(s *ast.MatchStmt) {
	if call, c, ok := bl.fallibleCall(s.X); ok {
		bl.matchCall(s, call, c)
		return
	}
	var p mir.Place
	mode := ByValue
	x := unparen(s.X)
	if u, ok := x.(*ast.UnaryExpr); ok && (u.Op == ast.TokAmp || u.Op == ast.TokMut) {
		mode = ByShared
		if u.Op == ast.TokMut {
			mode = ByMut
		}
		if bl.isPlaceExpr(u.X) {
			p = bl.place(u.X)
		} else {
			p = bl.tempOf(u.X)
		}
	} else if adj := bl.in.Adjust(x.ID()); adj.Deref > 0 {
		// A reference-typed scrutinee binds references.
		// A non-place (a call) is materialized once: placeProper
		// would fall back to tempOf itself, so it is never called first.
		var base mir.Place
		if bl.isPlaceExpr(x) {
			base = bl.placeProper(x)
		} else {
			base = bl.tempOf(x)
		}
		mode = ByShared
		for n := adj.Deref; n > 0; n-- {
			if bl.tab.Kind(bl.b.PlaceType(base)) == types.KMut {
				mode = ByMut
			}
			base = bl.deref(base, x.Span())
		}
		p = base
	} else if bl.isPlaceExpr(x) {
		src := bl.place(x)
		l := bl.newTemp(bl.b.PlaceType(src), x.Span())
		bl.assign(bl.b.Place(l), use(bl.valueOf(src, true, x.Span())), x.Span())
		p = bl.b.Place(l)
	} else {
		p = bl.tempOf(x)
	}
	if bl.tab.Kind(bl.b.PlaceType(p)) == types.KErrorUnion {
		bl.matchUnion(s, p, mode)
		return
	}
	join := bl.newBlock()
	reached := bl.arms(s.Arms, p, mode, join, func(a *ast.Arm) ast.Pattern { return a.Pat })
	bl.continueAt(join, reached)
}

// matchUnion is match e { ok(v) { } err(x) { } } on an E!T place (a
// parameter or local holding one, which check accepts): the discriminant
// (0 for ok) picks the ok arms, on the SelOk payload, or the err arms, on
// the SelErr error-set value; any other arm is in both groups, as matchCall
// does for a call. It was an ICE ("pattern *ast.OkPattern in a match").
func (bl *builder) matchUnion(s *ast.MatchStmt, p mir.Place, mode BindMode) {
	at := s.Span()
	oks, okPat := bl.unionArms(s.Arms, POk)
	errs, errPat := bl.unionArms(s.Arms, PErr)
	okB, errB, join := bl.newBlock(), bl.newBlock(), bl.newBlock()
	bl.unionSwitch(p, okB, errB, at)
	reached := false
	bl.cur = okB
	if len(oks) > 0 {
		reached = bl.arms(oks, bl.okPayload(p), mode, join, okPat) || reached
	} else {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, at)
	}
	bl.cur = errB
	if len(errs) > 0 {
		reached = bl.arms(errs, bl.errPayload(p), mode, join, errPat) || reached
	} else {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, at)
	}
	bl.continueAt(join, reached)
}

// unionSwitch switches on the discriminant of the E!T place p: 0 (ok) goes
// to okB, anything else to errB.
func (bl *builder) unionSwitch(p mir.Place, okB, errB mir.BlockID, at source.Span) {
	d := bl.discr(p, at)
	bl.switchOn(d, []mir.SwitchCase{{Value: 0, Target: okB}}, errB, at)
}

// okPayload is the E!T place p's ok payload: p.Downcast(ok).Field(0).
func (bl *builder) okPayload(p mir.Place) mir.Place {
	ut := bl.b.PlaceType(p)
	return bl.extend(p, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelOk, Type: ut},
		mir.Projection{Kind: mir.ProjField, A: 0, Type: bl.tab.Get(ut).Elem})
}

// errPayload is the E!T place p's error: p.Downcast(err), an error-set
// value.
func (bl *builder) errPayload(p mir.Place) mir.Place {
	return bl.extend(p, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelErr, Type: bl.tab.ErrorSetType(bl.tab.Get(bl.b.PlaceType(p)).Errors)})
}

// arms compiles the arms from the current block: a decision tree whose
// leaves jump to one entry block per arm, which declares the arm's bindings
// from their static places; past the size guard, each arm tested in order,
// the last failure unreachable (check proved exhaustiveness, E0306).
func (bl *builder) arms(arms []*ast.Arm, p mir.Place, mode BindMode, join mir.BlockID, pat func(*ast.Arm) ast.Pattern) bool {
	reached := false
	if entries, ok := bl.decisionTree(arms, p, pat); ok {
		for i, a := range arms {
			if entries[i] == mir.NoBlock {
				continue
			}
			bl.cur = entries[i]
			bl.openScope(scopePlain)
			bl.bindPat(pat(a), p, mode)
			bl.armStmts(a.Body)
			bl.closeScope(closeBrace(a.Body))
			reached = bl.joinTo(join, a.Span()) || reached
		}
		return reached
	}
	for _, a := range arms {
		if !bl.open() {
			break
		}
		next := bl.newBlock()
		q := pat(a)
		if alts := bl.flatAlts(q); alts != nil && mode == ByValue {
			// The arm has matched where one of its alternatives has: each
			// takes p apart there, as it would alone, before they join.
			bl.altTests(alts, p, next, q.Span(), func(x ast.Pattern) { bl.takeAlts([]ast.Pattern{x}, p) })
			bl.openScope(scopePlain)
		} else {
			bl.test(q, p, next)
			bl.openScope(scopePlain)
			bl.bindPat(q, p, mode)
		}
		bl.armStmts(a.Body)
		bl.closeScope(closeBrace(a.Body))
		reached = bl.joinTo(join, a.Span()) || reached
		bl.cur = next
	}
	if bl.open() {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, arms[len(arms)-1].Span())
	}
	return reached
}

// test emits the tests of pattern q against place p: control continues in
// the current block on a match, and goes to fail otherwise.
func (bl *builder) test(q ast.Pattern, p mir.Place, fail mir.BlockID) {
	at := q.Span()
	pk := bl.in.Pattern(q.ID())
	pass := func(cases []mir.SwitchCase, on mir.Operand) {
		ok := bl.newBlock()
		for i := range cases {
			cases[i].Target = ok
		}
		bl.switchOn(on, cases, fail, at)
		bl.cur = ok
	}
	switch pk.Kind {
	case PWild, PBind:
	case PLit:
		l := bl.patLit(q)
		switch l.Kind {
		case LitScalar:
			pass([]mir.SwitchCase{{Value: l.Bits}}, mir.Copy(p))
		case LitConst:
			k := bl.newTemp(types.Bool, at)
			bl.assign(bl.b.Place(k), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(p), B: mir.Const(l.Type, l.Const), Type: types.Bool}, at)
			ok := bl.newBlock()
			bl.boolSwitch(mir.Copy(bl.b.Place(k)), ok, fail, at)
			bl.cur = ok
		default:
			source.ICEf(at, "lower: literal pattern with no value")
		}
	case PRange:
		bl.rangeTest(q.(*ast.RangePattern), p, fail)
	case PVariant:
		ed := bl.enumDecl(pk, at)
		pass([]mir.SwitchCase{{Value: ed.Variants[pk.Variant].Discr}}, bl.discr(p, at))
		bl.subTests(q, bl.downcast(p, mir.SelEnum, pk.Variant), fail)
	case PErrorCase:
		pass([]mir.SwitchCase{{Value: uint64(bl.errorCode(pk.Item, at))}}, bl.discr(p, at))
		bl.subTests(q, bl.downcast(p, mir.SelCase, uint32(pk.Item)), fail)
	case PSome:
		pass([]mir.SwitchCase{{Value: 1}}, bl.discr(p, at))
		bl.test(q.(*ast.SomePattern).Pat, bl.somePayload(p), fail)
	case PNone:
		pass([]mir.SwitchCase{{Value: 0}}, bl.discr(p, at))
	case POk:
		// A nested ok(q) on an E!T place (an ok payload, a field, a some
		// payload of one): discriminant 0, then q on the payload.
		pass([]mir.SwitchCase{{Value: 0}}, bl.discr(p, at))
		if sub := q.(*ast.OkPattern).Pat; sub != nil {
			bl.test(sub, bl.okPayload(p), fail)
		}
	case PErr:
		// Any discriminant but 0 is an error (unionSwitch).
		ok := bl.newBlock()
		bl.switchOn(bl.discr(p, at), []mir.SwitchCase{{Value: 0, Target: fail}}, ok, at)
		bl.cur = ok
		if sub := q.(*ast.ErrPattern).Pat; sub != nil {
			bl.test(sub, bl.errPayload(p), fail)
		}
	case PStruct:
		for _, f := range patFields(q) {
			if f.Pat != nil {
				bl.test(f.Pat, bl.structField(p, f), fail)
			}
		}
	case PAlt:
		bl.altTests(q.(*ast.AltPattern).Alts, p, fail, at, nil)
	default:
		source.ICEf(at, "lower: pattern %T (kind %d) in a match", q, pk.Kind)
	}
}

// altTests tests the alternatives alts against p in order: control
// continues in the current block when one matches, and goes to fail when
// none does. on, when set, runs on each alternative's own success edge,
// before the edges join.
func (bl *builder) altTests(alts []ast.Pattern, p mir.Place, fail mir.BlockID, at source.Span, on func(ast.Pattern)) {
	ok := bl.newBlock()
	for i, a := range alts {
		nf := fail
		if i < len(alts)-1 {
			nf = bl.newBlock()
		}
		bl.test(a, p, nf)
		if on != nil {
			on(a)
		}
		bl.gotoBlock(ok, at)
		bl.cur = nf
		if i == len(alts)-1 {
			bl.cur = mir.NoBlock
		}
	}
	bl.cur = ok
}

// flatAlts is the alternatives of q, nested alternatives flattened in
// order, or nil when q is not an alternative.
func (bl *builder) flatAlts(q ast.Pattern) []ast.Pattern {
	if _, ok := q.(*ast.AltPattern); !ok || bl.in.Pattern(q.ID()).Kind != PAlt {
		return nil
	}
	var flat []ast.Pattern
	var walk func(q ast.Pattern)
	walk = func(q ast.Pattern) {
		if a, ok := q.(*ast.AltPattern); ok && bl.in.Pattern(q.ID()).Kind == PAlt {
			for _, x := range a.Alts {
				walk(x)
			}
			return
		}
		flat = append(flat, q)
	}
	walk(q)
	return flat
}

// rangeTest tests the integer or char place p against the range q:
// lo <= x < hi is one wrapping subtract and
// one unsigned compare, (x - lo) < (hi - lo) in the unsigned type of x's
// width; an open end is one compare in x's own type.
func (bl *builder) rangeTest(q *ast.RangePattern, p mir.Place, fail mir.BlockID) {
	at := q.Span()
	t := bl.b.PlaceType(p)
	x := mir.Copy(p)
	k := bl.newTemp(types.Bool, at)
	switch {
	case q.Lo != nil && q.Hi != nil:
		lo, hi := bl.patLit(q.Lo), bl.patLit(q.Hi)
		ut := bl.unsignedOf(t, at)
		ux := x
		if ut != t {
			c := bl.newTemp(ut, at)
			bl.assign(bl.b.Place(c), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastTrunc, A: x, Type: ut}, at)
			ux = mir.Copy(bl.b.Place(c))
		}
		mask := ^uint64(0)
		if w := bl.tab.Get(ut).Bits; w != 0 && w < 64 {
			mask = 1<<w - 1
		}
		d := bl.newTemp(ut, at)
		bl.assign(bl.b.Place(d), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.SubWrap, A: ux, B: mir.Imm(ut, lo.Bits&mask), Type: ut}, at)
		bl.assign(bl.b.Place(k), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: mir.Copy(bl.b.Place(d)), B: mir.Imm(ut, (hi.Bits-lo.Bits)&mask), Type: types.Bool}, at)
	case q.Lo != nil:
		bl.assign(bl.b.Place(k), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Ge, A: x, B: mir.Imm(t, bl.patLit(q.Lo).Bits), Type: types.Bool}, at)
	case q.Hi != nil:
		bl.assign(bl.b.Place(k), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: x, B: mir.Imm(t, bl.patLit(q.Hi).Bits), Type: types.Bool}, at)
	default:
		source.ICEf(at, "lower: a range pattern with no end")
	}
	ok := bl.newBlock()
	bl.boolSwitch(mir.Copy(bl.b.Place(k)), ok, fail, at)
	bl.cur = ok
}

// unsignedOf is the unsigned integer type of t's width: t itself when
// unsigned, u32 for char.
func (bl *builder) unsignedOf(t types.TypeID, at source.Span) types.TypeID {
	ty := bl.tab.Get(t)
	switch {
	case ty.Kind == types.KChar:
		return types.U32
	case ty.Kind == types.KIntPtr:
		return types.Uint
	case ty.Kind == types.KInt:
		switch ty.Bits {
		case 8:
			return types.U8
		case 16:
			return types.U16
		case 32:
			return types.U32
		case 64:
			return types.U64
		}
	}
	source.ICEf(at, "lower: a range pattern on %s", bl.tab.TypeString(t, nil))
	return t
}

// patLit is a literal pattern's value: check's (a literal's, a named
// const's), or true and false.
func (bl *builder) patLit(q ast.Pattern) Lit {
	l := bl.in.Literal(q.ID())
	if l.Kind == LitNone {
		switch lp := q.(type) {
		case *ast.LiteralPattern:
			l = bl.in.Literal(lp.Lit.ID())
		case *ast.NamePattern:
			if s := lp.Name.Name; s == source.SymTrue || s == source.SymFalse {
				l = Lit{Kind: LitScalar, Type: types.Bool}
				if s == source.SymTrue {
					l.Bits = 1
				}
			}
		}
	}
	if l.Kind == LitNone {
		if _, lit := q.(*ast.LiteralPattern); !lit {
			// A named const whose value check could not read: a poisoned
			// read (check.Body.PoisonAt), which the const's own
			// evaluation reports.
			source.ICEf(q.Span(), "lower: a const pattern with no value and no diagnostic (check gap)")
		}
		source.ICEf(q.Span(), "lower: literal pattern with no value")
	}
	return l
}

// enumDecl is a variant pattern's enum.
func (bl *builder) enumDecl(pk Pat, at source.Span) *types.EnumDecl {
	ed, ok := bl.cfg.Items.Get(pk.Item).Decl.(*types.EnumDecl)
	if !ok || int(pk.Variant) >= len(ed.Variants) {
		source.ICEf(at, "lower: variant pattern of no enum")
	}
	return ed
}

// errorCode is an error's identity, the case value of its pattern.
func (bl *builder) errorCode(item types.ItemID, at source.Span) uint32 {
	ed, ok := bl.cfg.Items.Get(item).Decl.(*types.ErrorDecl)
	if !ok {
		source.ICEf(at, "lower: error pattern of no error")
	}
	if ed.Code != 0 {
		return ed.Code
	}
	return types.ErrorCode(bl.cfg.Items.Get(item).Key)
}

// fieldOf is the payload field a variant or error sub-pattern names:
// check's Use, else by name in the declaration, else its position (the
// same answer payloadIndex gives on the downcast place).
func (bl *builder) fieldOf(pk Pat, f *ast.PatField, pos int, at source.Span) uint32 {
	if pf := bl.in.Pattern(f.ID()); pf.Kind != PInvalid {
		return pf.Field
	}
	var fields []types.FieldDecl
	switch pk.Kind {
	case PErrorCase:
		if d, ok := bl.cfg.Items.Get(pk.Item).Decl.(*types.ErrorDecl); ok {
			fields = d.Fields
		}
	case PVariant:
		fields = bl.enumDecl(pk, at).Variants[pk.Variant].Fields
	}
	for k, fd := range fields {
		if fd.Name == f.Name.Name {
			return uint32(k)
		}
	}
	return uint32(pos)
}

// downcast is p.Downcast(sel, a), of p's type.
func (bl *builder) downcast(p mir.Place, sel mir.VariantSel, a uint32) mir.Place {
	return bl.extend(p, mir.Projection{Kind: mir.ProjDowncast, Sel: sel, A: a, Type: bl.b.PlaceType(p)})
}

// patFields are a variant or error pattern's sub-patterns.
func patFields(q ast.Pattern) []*ast.PatField {
	switch q := q.(type) {
	case *ast.VariantPattern:
		return q.Fields
	case *ast.PathPattern:
		return q.Fields
	}
	return nil
}

// subField is the place of PatField f under a downcast place: of the
// declared payload type (a binding's type is a reference under & and mut
// match), else of the pattern's type.
func (bl *builder) subField(dc mir.Place, f *ast.PatField, i int) mir.Place {
	k := bl.payloadIndex(dc, f, uint32(i))
	t := bl.payloadFieldType(dc, k)
	if t == types.NoType && f.Pat != nil {
		t = bl.in.Type(f.Pat.ID())
	}
	if t == types.NoType || t == types.Poison {
		t = bl.in.Type(f.ID())
	}
	if t == types.NoType || t == types.Poison {
		t = bl.in.Binding(f.Name.ID()).Type
	}
	if t == types.NoType || t == types.Poison {
		t = bl.payloadFieldType(dc, k)
	}
	return bl.field(dc, k, t)
}

// payloadFieldType is the type of field k of the variant or error a
// downcast place selects, the enum's generic arguments substituted.
func (bl *builder) payloadFieldType(dc mir.Place, k uint32) types.TypeID {
	steps := bl.b.Proj(dc)
	if len(steps) == 0 {
		return types.NoType
	}
	last := steps[len(steps)-1]
	switch last.Sel {
	case mir.SelCase:
		if d, ok := bl.cfg.Items.Get(types.ItemID(last.A)).Decl.(*types.ErrorDecl); ok && int(k) < len(d.Fields) {
			return d.Fields[k].Type
		}
	case mir.SelEnum:
		t := bl.tab.Get(last.Type)
		if d, ok := bl.cfg.Items.Get(t.Item).Decl.(*types.EnumDecl); ok && int(last.A) < len(d.Variants) && int(k) < len(d.Variants[last.A].Fields) {
			return bl.substArgs(d.Variants[last.A].Fields[k].Type, t.Args)
		}
	}
	return types.NoType
}

func (bl *builder) subTests(q ast.Pattern, dc mir.Place, fail mir.BlockID) {
	for i, f := range patFields(q) {
		if f.Pat != nil {
			bl.test(f.Pat, bl.subField(dc, f, i), fail)
		}
	}
}

// bindPat declares the bindings of q from their places, by mode.
func (bl *builder) bindPat(q ast.Pattern, p mir.Place, mode BindMode) {
	pk := bl.in.Pattern(q.ID())
	switch pk.Kind {
	case PBind:
		if np, ok := q.(*ast.NamePattern); ok {
			if id := bindingIdent(bl, np); id != nil {
				bl.bindTo(id, p, mode, q.Span())
			}
		}
	case PVariant, PErrorCase:
		sel, a := mir.SelEnum, pk.Variant
		if pk.Kind == PErrorCase {
			sel, a = mir.SelCase, uint32(pk.Item)
		}
		dc := bl.downcast(p, sel, a)
		for i, f := range patFields(q) {
			fp := bl.subField(dc, f, i)
			if f.Pat == nil {
				bl.bindTo(f.Name, fp, mode, f.Span())
				continue
			}
			bl.bindPat(f.Pat, fp, mode)
		}
	case PSome:
		bl.bindPat(q.(*ast.SomePattern).Pat, bl.somePayload(p), mode)
	case POk:
		if sub := q.(*ast.OkPattern).Pat; sub != nil {
			bl.bindPat(sub, bl.okPayload(p), mode)
		}
	case PErr:
		if sub := q.(*ast.ErrPattern).Pat; sub != nil {
			bl.bindPat(sub, bl.errPayload(p), mode)
		}
	case PStruct:
		for _, f := range patFields(q) {
			fp := bl.structField(p, f)
			if f.Pat == nil {
				bl.bindTo(f.Name, fp, mode, f.Span())
				continue
			}
			bl.bindPat(f.Pat, fp, mode)
		}
		if mode == ByValue {
			bl.takeWhole(p, q.Span())
		}
	case PAlt:
		if mode == ByValue {
			bl.takeAlts(q.(*ast.AltPattern).Alts, p)
		}
	}
}

// takeAlts takes p apart as each alternative would alone, when every
// alternative has the same shape on it: an alternative binds nothing, so a
// linear struct of plain fields inside is consumed only by these takes. A
// struct pattern on every alternative takes p apart (`Ticket(id: 1) |
// Ticket(id: 2)`); `some`, `ok` or one variant on every alternative tested
// that variant on every path, so its payload fields are taken apart the
// same way, each field every alternative has a pattern for. Alternatives
// that differ (`some(..) | none`, a literal) leave p as it is: a payload
// may not be there on every path. An arm whose whole pattern is an
// alternative, tested arm by arm, calls takeAlts on each alternative alone
// on its own success edge instead (arms).
func (bl *builder) takeAlts(alts []ast.Pattern, p mir.Place) {
	var flat []ast.Pattern
	for _, a := range alts {
		if f := bl.flatAlts(a); f != nil {
			flat = append(flat, f...)
		} else {
			flat = append(flat, a)
		}
	}
	if len(flat) == 0 {
		return
	}
	k0 := bl.in.Pattern(flat[0].ID())
	for _, q := range flat {
		pk := bl.in.Pattern(q.ID())
		if pk.Kind != k0.Kind || pk.Kind == PVariant && (pk.Item != k0.Item || pk.Variant != k0.Variant) {
			return
		}
	}
	switch k0.Kind {
	case PSome:
		subs := make([]ast.Pattern, len(flat))
		for i, q := range flat {
			subs[i] = q.(*ast.SomePattern).Pat
		}
		bl.takeAlts(subs, bl.somePayload(p))
	case POk:
		subs := make([]ast.Pattern, len(flat))
		for i, q := range flat {
			if subs[i] = q.(*ast.OkPattern).Pat; subs[i] == nil {
				return
			}
		}
		bl.takeAlts(subs, bl.okPayload(p))
	case PVariant:
		dc := bl.downcast(p, mir.SelEnum, k0.Variant)
		bl.takeAltFields(flat, func(f *ast.PatField, i int) (uint32, mir.Place) {
			return bl.payloadIndex(dc, f, uint32(i)), bl.subField(dc, f, i)
		})
	case PStruct:
		bl.takeAltFields(flat, func(f *ast.PatField, _ int) (uint32, mir.Place) {
			return bl.in.Pattern(f.ID()).Field, bl.structField(p, f)
		})
		bl.takeWhole(p, flat[0].Span())
	}
}

// takeAltFields runs takeAlts on each field that every alternative of flat
// has a pattern for; at gives a field's index and place.
func (bl *builder) takeAltFields(flat []ast.Pattern, at func(*ast.PatField, int) (uint32, mir.Place)) {
	byField := map[uint32][]ast.Pattern{}
	place := map[uint32]mir.Place{}
	var order []uint32
	for _, q := range flat {
		for i, f := range patFields(q) {
			if f.Pat == nil {
				continue
			}
			k, fp := at(f, i)
			if _, ok := byField[k]; !ok {
				order = append(order, k)
				place[k] = fp
			}
			byField[k] = append(byField[k], f.Pat)
		}
	}
	for _, k := range order {
		if subs := byField[k]; len(subs) == len(flat) {
			bl.takeAlts(subs, place[k])
		}
	}
}

// takeWhole ends a by-value struct pattern on p whose fields are all plain
// while p is not (a linear struct of plain fields, as `Ticket(id)`): its
// bindings copy, so nothing is taken out of p, and borrow sees a part
// consumed only as a take. One field, a scalar where there is one, moves
// into a statement temporary, as a destructure that takes nothing does. A
// struct with no fields has nothing to move: StConsume ends p itself.
// The result reports whether a take was emitted.
func (bl *builder) takeWhole(p mir.Place, at source.Span) bool {
	t := bl.b.PlaceType(p)
	st := bl.tab.Get(t)
	d, ok := bl.cfg.Items.Get(st.Item).Decl.(*types.StructDecl)
	if st.Kind != types.KNamed || !ok || bl.plain(t, at) {
		return false
	}
	if bl.fieldless(t) {
		bl.emit(mir.Statement{Kind: mir.StConsume, Place: p, Span: bl.span(at)})
		return true
	}
	k, ft := -1, types.NoType
	for i, f := range d.Fields {
		ty := bl.substArgs(f.Type, st.Args)
		if !bl.plain(ty, at) {
			return false
		}
		if k < 0 || !scalarKind(bl.tab.Kind(ft)) && scalarKind(bl.tab.Kind(ty)) {
			k, ft = i, ty
		}
	}
	h := bl.newTemp(ft, at)
	bl.assign(bl.b.Place(h), use(mir.Move(bl.field(p, uint32(k), ft))), at)
	return true
}

// fieldless reports whether t is a struct with no fields, the one type a
// StConsume ends.
func (bl *builder) fieldless(t types.TypeID) bool {
	st := bl.tab.Get(t)
	if st.Kind != types.KNamed {
		return false
	}
	d, ok := bl.cfg.Items.Get(st.Item).Decl.(*types.StructDecl)
	return ok && len(d.Fields) == 0
}

// scalarKind reports a kind held in one register: takeWhole moves a field
// of one, where it has one, so the take copies no array or struct.
func scalarKind(k types.TypeKind) bool {
	switch k {
	case types.KInt, types.KIntPtr, types.KFloat, types.KBool, types.KChar, types.KRef, types.KRawPtr:
		return true
	}
	return false
}

// structField is struct pattern field f's place in the struct place p: the
// field check resolved (UseField), of its declared type with p's generic
// arguments.
func (bl *builder) structField(p mir.Place, f *ast.PatField) mir.Place {
	pf := bl.in.Pattern(f.ID())
	st := bl.tab.Get(bl.b.PlaceType(p))
	d, ok := bl.cfg.Items.Get(st.Item).Decl.(*types.StructDecl)
	if pf.Kind == PInvalid || st.Kind != types.KNamed || !ok || int(pf.Field) >= len(d.Fields) {
		source.ICEf(f.Span(), "lower: a struct pattern field with no field")
	}
	return bl.field(p, pf.Field, bl.substArgs(d.Fields[pf.Field].Type, st.Args))
}

// matchCall is match F(x) { ok(v) { } err(e) { } }: ok arms on the
// call's destination from its target, err arms on the edge's error local.
func (bl *builder) matchCall(s *ast.MatchStmt, call *ast.CallExpr, c Call) {
	e := bl.newTemp(bl.tab.ErrorSetType(c.Errors), call.Span())
	eb := bl.newBlock()
	dest := bl.emitCall(call, c, &errEdge{kind: mir.EdgeMatch, block: eb, err: e}, mir.NoLocal)
	oks, okPat := bl.unionArms(s.Arms, POk)
	errs, errPat := bl.unionArms(s.Arms, PErr)
	join := bl.newBlock()
	reached := false
	if bl.open() && len(oks) > 0 {
		reached = bl.arms(oks, dest, ByValue, join, okPat) || reached
	} else if bl.open() {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, s.Span())
	}
	bl.cur = eb
	if len(errs) > 0 {
		reached = bl.arms(errs, bl.b.Place(e), ByValue, join, errPat) || reached
	} else {
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, s.Span())
	}
	bl.continueAt(join, reached)
}

// unionArms are the arms of a match on an E!T that the given side (POk or
// PErr) can reach, and each one's pattern on that side's payload: an
// ok(q) or err(q) arm's q, any other arm whole on both sides. A top-level
// alternative keeps the alternatives of its side, ok(q) and err(q) taken
// apart as one arm is, in an alternative pattern of the original's node
// (alternatives bind nothing, and only its kind and span are read).
func (bl *builder) unionArms(arms []*ast.Arm, side PatKind) ([]*ast.Arm, func(*ast.Arm) ast.Pattern) {
	other := PErr
	if side == PErr {
		other = POk
	}
	of := func(q ast.Pattern) (ast.Pattern, bool) {
		switch k := bl.in.Pattern(q.ID()).Kind; {
		case k == other:
			return nil, false
		case k == side:
			switch q := q.(type) {
			case *ast.OkPattern:
				return q.Pat, true
			case *ast.ErrPattern:
				return q.Pat, true
			}
		}
		return q, true
	}
	var out []*ast.Arm
	pats := map[*ast.Arm]ast.Pattern{}
	for _, a := range arms {
		alt, ok := a.Pat.(*ast.AltPattern)
		if !ok {
			if q, ok := of(a.Pat); ok {
				out, pats[a] = append(out, a), q
			}
			continue
		}
		var keep []ast.Pattern
		for _, x := range alt.Alts {
			if q, ok := of(x); ok {
				keep = append(keep, q)
			}
		}
		switch {
		case len(keep) == 1:
			out, pats[a] = append(out, a), keep[0]
		case len(keep) > 1:
			out, pats[a] = append(out, a), &ast.AltPattern{Base: alt.Base, Alts: keep}
		}
	}
	return out, func(a *ast.Arm) ast.Pattern { return pats[a] }
}

// payloadIndex is the field a variant or error sub-pattern names: check's
// Use, else by name in the downcast's declaration, else its position.
func (bl *builder) payloadIndex(dc mir.Place, f *ast.PatField, pos uint32) uint32 {
	if pf := bl.in.Pattern(f.ID()); pf.Kind != PInvalid {
		return pf.Field
	}
	steps := bl.b.Proj(dc)
	if len(steps) == 0 {
		return pos
	}
	last := steps[len(steps)-1]
	var fields []types.FieldDecl
	switch last.Sel {
	case mir.SelCase:
		if d, ok := bl.cfg.Items.Get(types.ItemID(last.A)).Decl.(*types.ErrorDecl); ok {
			fields = d.Fields
		}
	case mir.SelEnum:
		if d, ok := bl.cfg.Items.Get(bl.tab.Get(last.Type).Item).Decl.(*types.EnumDecl); ok && int(last.A) < len(d.Variants) {
			fields = d.Variants[last.A].Fields
		}
	}
	for k, fd := range fields {
		if fd.Name == f.Name.Name {
			return uint32(k)
		}
	}
	return pos
}
