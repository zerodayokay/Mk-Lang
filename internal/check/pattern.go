// Why: a match arm's pattern decides which names it binds, with what type
// and binding mode, and what the match must still cover.
//
// What: matchStmt and matchStmtV (a match as a statement or a value), pat
// and its cases (namePat, literalPat, constPat, variantPat, pathPat,
// errorTest, patFields), leftover (E0408) and notPattern (E0301).
//
// How: each pattern is typed against the scrutinee's type: variants, error
// tests, some/none, ok/err, literals, named consts, ranges, names and
// alternatives, each under a binding mode (bindAs); the errors are E0203,
// E0207, E0301, E0303, E0308, E0309 and E0408. exhaust.go then checks the
// arms cover every value.

package check

import (
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// matchStmt checks a match statement: the scrutinee and its binding mode (`match
// &e`, `match mut e`, or a reference value, which binds references), each
// arm's pattern and body, then exhaustiveness. A match diverges when every
// arm does (an inexhaustive one is E0306).
func (u *unit) matchStmt(s *ast.MatchStmt) bool { return u.matchStmtV(s, nil) }

// matchStmtV checks a match as a statement (v nil) or as a value construct
// whose arms go through valueArm.
func (u *unit) matchStmtV(s *ast.MatchStmt, v *valueCtx) bool {
	mode := ast.ByCopy
	x := s.X
	if un, ok := x.(*ast.UnaryExpr); ok && (un.Op == ast.TokAmp || un.Op == ast.TokMut) {
		mode = ast.ByRef
		if un.Op == ast.TokMut {
			mode = ast.ByMutRef
		}
		x = un.X
	}
	t := u.expr(x, types.NoType)
	if t != types.Poison && mode == ast.ByMutRef {
		u.constMutation(x, "borrowed mut")
	}
	if x != s.X {
		u.setType(s.X, types.Poison)
	}
	if v, n := u.deref(t); n > 0 {
		if mode == ast.ByCopy {
			mode = ast.ByRef
			if u.tab.Kind(t) == types.KMut {
				mode = ast.ByMutRef
			}
		}
		*u.b.adjust(x.ID()) = Adjust{Kind: AdjDeref, Deref: uint8(n)}
		t = v
	}
	all := len(s.Arms) > 0
	for _, a := range s.Arms {
		if a == nil {
			all = false
			continue
		}
		if a.Pat != nil {
			u.pat(a.Pat, t, mode)
		}
		if !u.armBlock(a.Body, v) {
			all = false
		}
	}
	if t != types.Poison {
		u.exhaustive(s, t)
	}
	return all
}

// bindAs is a binding's type under the match's mode.
func (u *unit) bindAs(t types.TypeID, mode ast.RefMode) types.TypeID {
	if t == types.Poison {
		return t
	}
	switch mode {
	case ast.ByRef:
		return u.tab.Ref(t)
	case ast.ByMutRef:
		return u.tab.Mut(t)
	}
	return t
}

// pat types pattern p against scrutinee type t.
func (u *unit) pat(p ast.Pattern, t types.TypeID, mode ast.RefMode) {
	u.setType(p, t)
	tab := u.tab
	ty := tab.Get(t)
	switch p := p.(type) {
	case *ast.WildcardPattern, *ast.BadPattern:
	case *ast.NamePattern:
		u.namePat(p, t, mode)
	case *ast.LiteralPattern:
		u.literalPat(p, t)
	case *ast.RangePattern:
		u.rangePat(p, t)
	case *ast.SomePattern, *ast.NonePattern:
		if t == types.Poison {
			u.poisonPattern(p)
			return
		}
		if ty.Kind != types.KOptional {
			u.patMismatch(p, "an optional", t)
			u.poisonPattern(p)
			return
		}
		if sp, ok := p.(*ast.SomePattern); ok && sp.Pat != nil {
			u.pat(sp.Pat, ty.Elem, mode)
		}
	case *ast.OkPattern, *ast.ErrPattern:
		if t == types.Poison {
			u.poisonPattern(p)
			return
		}
		if ty.Kind != types.KErrorUnion {
			u.patMismatch(p, "an error union", t)
			u.poisonPattern(p)
			return
		}
		switch p := p.(type) {
		case *ast.OkPattern:
			if p.Pat != nil {
				u.pat(p.Pat, ty.Elem, mode)
			}
		case *ast.ErrPattern:
			if p.Pat != nil {
				u.pat(p.Pat, tab.ErrorSetType(ty.Errors), mode)
			}
		}
	case *ast.AltPattern:
		for _, a := range p.Alts {
			if a != nil {
				u.pat(a, t, mode)
			}
		}
	case *ast.VariantPattern:
		if p.Name == nil {
			return
		}
		u.variantPat(p, p.Name, p.Fields, t, mode)
	case *ast.PathPattern:
		u.pathPat(p, t, mode)
	}
}

// patMismatch is E0301 for a pattern whose form does not fit the
// scrutinee.
func (u *unit) patMismatch(p ast.Pattern, want string, t types.TypeID) {
	if t == types.Poison {
		return
	}
	u.err(diag.E0301, p.Span(), "the pattern needs "+want+", the value is `"+u.str(t)+"`").Text("expected", want).Text("found", u.str(t))
}

// namePat is a binding, an error test (resolve's decision) or true/false.
func (u *unit) namePat(p *ast.NamePattern, t types.TypeID, mode ast.RefMode) {
	r := u.ref(p)
	switch r.Kind {
	case resolve.RefItem:
		it := u.c.cfg.Items.Get(types.ItemID(r.A))
		switch it.Kind {
		case types.KindError:
			u.errorTest(p, it, nil, false, t, mode)
		case types.KindConst:
			u.constPat(p, it, t)
		}
		return
	case resolve.RefUniverse:
		if s := source.Symbol(r.A); s == source.SymTrue || s == source.SymFalse {
			if t != types.Bool && t != types.Poison {
				u.patMismatch(p, "bool", t)
			}
		}
		return
	case resolve.RefPoison:
		u.bindName(p.Name, types.Poison)
		return
	}
	u.bindName(p.Name, u.bindAs(t, mode))
}

// literalPat adapts a literal to the scrutinee: integers (negative
// too), char, u8, str by content, bool; a float is E0308.
func (u *unit) literalPat(p *ast.LiteralPattern, t types.TypeID) {
	if p.Lit == nil {
		return
	}
	if p.Lit.Kind == ast.TokFloat {
		u.err(diag.E0308, p.Span(), "a float literal is not a pattern; compare with `if`").Text("operator", "pattern").Text("type", "float")
		return
	}
	want := t
	if t != types.Poison && !u.isInt(t) {
		want = types.NoType
	}
	lt := u.literal(p.Lit, p.Neg, want)
	u.setType(p.Lit, lt)
	if c := u.b.Consts; c != nil && p.Neg {
		u.b.setConst(p.ID(), c[u.b.slot(p.Lit.ID())])
	}
	if t != types.Poison && lt != types.Poison && lt != t {
		u.patMismatch(p, "`"+u.str(lt)+"`", t)
	}
}

// constPat is a named const, `K` or `lib.K`, as a pattern: a value test
// against the const's value, as a literal of that value would be. Its type
// is one a literal pattern takes (an integer, char, bool or str; a float is
// E0308, anything else E0301) and must be the scrutinee's (E0301, as for
// `==`). The value is check's (constValueArg: folded, else evaluated; a
// same-module const named in a pattern is early), so lower and the
// exhaustiveness check read it as a literal's.
func (u *unit) constPat(p ast.Pattern, it *types.Item, t types.TypeID) {
	d, ok := u.c.declOfItem(it).(*types.ConstDecl)
	if !ok {
		return
	}
	*u.b.use(p.ID()) = Use{Kind: UseConst, Item: it.ID}
	u.x.rec.signature(it.Key)
	ct := d.Type
	if ct == types.NoType {
		u.b.poisonRead(it.ID, p.Span()) // as an expression's read of it
		return
	}
	if ct == types.Poison {
		return
	}
	name := u.c.cfg.Interner.Str(it.Name)
	switch u.tab.Kind(ct) {
	case types.KInt, types.KIntPtr, types.KChar, types.KBool, types.KStr:
	case types.KFloat:
		u.err(diag.E0308, p.Span(), "`"+name+"` is a float const, not a pattern; compare with `if`").Text("operator", "pattern").Text("type", "float")
		return
	default:
		u.err(diag.E0301, p.Span(), "`"+name+"` is a const of `"+u.str(ct)+"`, not a pattern; a const pattern is an integer, char, bool or str").
			Text("expected", "pattern").Text("found", u.str(ct)).Alt("match its value's shape, such as a variant `.V`, or compare with `if`")
		return
	}
	if t != types.Poison && ct != t {
		u.patMismatch(p, "`"+u.str(ct)+"`", t)
		return
	}
	a, ok := u.m.constValueArg(u.x, it, p.Span())
	if !ok || a.Kind != types.CAValue {
		// Reported by the const's own check or evaluation; lower's
		// E0000 at a value-less pattern is judged at this read.
		u.b.poisonRead(it.ID, p.Span())
		return
	}
	u.b.setConst(p.ID(), a.Value)
}

// constPatKey is a const pattern's value as check recorded it (constPat),
// NoConst when it has none.
func (u *unit) constPatKey(p ast.Pattern) types.ConstID {
	if c := u.b.Consts; c != nil {
		return c[u.b.slot(p.ID())]
	}
	return types.NoConst
}

// variantPat is `.V`, `.V(f, g: p)` or `E.V(..)` against an enum:
// V by name (E0207 folded, E0203 otherwise), fields by name (E0309), the
// fields left out of a by-value match Plain (E0408).
func (u *unit) variantPat(p ast.Pattern, name *ast.Ident, fields []*ast.PatField, t types.TypeID, mode ast.RefMode) {
	ty := u.tab.Get(t)
	if t == types.Poison {
		u.poisonFields(fields)
		return
	}
	var d *types.EnumDecl
	if ty.Kind == types.KNamed {
		d, _ = u.c.declOfItem(u.c.cfg.Items.Get(ty.Item)).(*types.EnumDecl)
	}
	if d == nil {
		u.patMismatch(p, "an enum", t)
		u.poisonFields(fields)
		return
	}
	mb, ok := u.memberOf(t, name)
	if !ok || mb.Kind != MemberVariant || int(mb.Field) >= len(d.Variants) {
		if ok {
			u.memberMiss(t, name)
		}
		u.poisonFields(fields)
		return
	}
	*u.b.use(p.ID()) = Use{Kind: UseVariant, Item: ty.Item, Index: mb.Field}
	fs := make([]types.FieldDecl, len(d.Variants[mb.Field].Fields))
	for i, f := range d.Variants[mb.Field].Fields {
		fs[i] = f
		fs[i].Type = u.c.subst(f.Type, ty.Args, types.NoType)
	}
	u.patFields(p, fields, fs, t, mode, false)
}

// patFields matches named fields against declared ones.
// foreign marks a struct of another module: a field it does not export is
// E0204 at the field's name, as a destructure would be.
func (u *unit) patFields(p ast.Pattern, fields []*ast.PatField, fs []types.FieldDecl, t types.TypeID, mode ast.RefMode, foreign bool) {
	in := u.c.cfg.Interner
	seen := make([]bool, len(fs))
	for _, f := range fields {
		if f == nil || f.Name == nil {
			continue
		}
		k := -1
		for i := range fs {
			if fs[i].Name == f.Name.Name {
				k = i
			}
		}
		if k < 0 {
			names := make([]string, len(fs))
			for i, x := range fs {
				names[i] = in.Str(x.Name)
			}
			u.err(diag.E0309, f.Name.Span(), "`"+u.str(t)+"` has no field `"+in.Str(f.Name.Name)+"` here").Text("type", u.str(t)).Fact("fields", diag.TextList(names...))
			u.poisonFields([]*ast.PatField{f})
			continue
		}
		seen[k] = true
		if foreign && !fs[k].Exported {
			nm := in.Str(fs[k].Name)
			u.err(diag.E0204, f.Name.Span(), "field `"+nm+"` of `"+u.str(t)+"` is private to its module").In(diag.PhaseTypes).
				Text("field", nm).Text("type", u.str(t))
			u.poisonFields([]*ast.PatField{f})
			continue
		}
		*u.b.use(f.ID()) = Use{Kind: UseField, Index: uint32(k)}
		if f.Pat == nil {
			u.bindName(f.Name, u.bindAs(fs[k].Type, mode))
			continue
		}
		u.pat(f.Pat, fs[k].Type, mode)
		if _, wild := f.Pat.(*ast.WildcardPattern); wild && mode == ast.ByCopy {
			u.leftover(f.Pat.Span(), in.Str(fs[k].Name), fs[k].Type)
		}
	}
	if mode != ast.ByCopy {
		return
	}
	for i, f := range fs {
		if !seen[i] && len(fields) > 0 {
			u.leftover(p.Span(), in.Str(f.Name), f.Type)
		}
	}
}

// leftover is E0408 for a field a by-value pattern leaves out or matches by
// `_` that is not Plain: it would be dropped unseen.
func (u *unit) leftover(at source.Span, field string, t types.TypeID) {
	if t == types.Poison || !u.c.closed(t) || u.c.satisfies(u.env, t, types.Bound{Kind: types.BoundPlain}, u.x.rec) {
		return
	}
	u.err(diag.E0408, at, "field `"+field+"` is not Plain; a by-value match must bind it").Text("reason", "pattern-leftover").
		Text("type", u.str(t)).Text("field", field).Alt("bind it by name").Alt("match `&e` instead")
}

// pathPat is a PathPattern: `E.V(..)` (resolve's RefDeferred on an enum,
// or on an alias of one), an error test with payload fields, or a struct
// pattern. Any other path is E0301 at the path, never a silent Poison: lower
// reads every accepted PathPattern's Use (the FuzzCheck crasher
// 54056f522b943918, `net.Refused.A {}`, and its relatives `E.X.A`, `P.Q`,
// `T.X`, `e.X`, `C.X`, `F(x)`).
func (u *unit) pathPat(p *ast.PathPattern, t types.TypeID, mode ast.RefMode) {
	r := u.ref(p)
	switch r.Kind {
	case resolve.RefPoison:
		u.poisonFields(p.Fields)
		return
	case resolve.RefDeferred:
		// RefDeferred keeps the A and B of the resolved prefix but not its
		// kind (a primitive, a type parameter or a local carries no item in
		// A), so the kind is read from the prefix's last segment.
		root := u.pathRoot(p, r)
		if it, ok := u.rootEnum(p, root); ok && int(r.Segs) == len(p.Path.Names)-1 {
			ty := u.tab.Get(t)
			if t != types.Poison && (ty.Kind != types.KNamed || ty.Item != it.ID) {
				u.patMismatch(p, "`"+u.c.cfg.Interner.Str(it.Name)+"`", t)
				u.poisonFields(p.Fields)
				return
			}
			u.variantPat(p, p.Path.Names[len(p.Path.Names)-1], p.Fields, t, mode)
			return
		}
		if root.Kind == resolve.RefPoison {
			u.poisonFields(p.Fields)
			return
		}
		u.notPattern(p, "member path")
		u.poisonFields(p.Fields)
		return
	case resolve.RefItem:
		it := u.c.cfg.Items.Get(types.ItemID(r.A))
		switch it.Kind {
		case types.KindError:
			u.errorTest(p, it, p.Fields, p.HasFields, t, mode)
			return
		case types.KindConst:
			if !p.HasFields && len(p.Fields) == 0 {
				u.constPat(p, it, t)
				return
			}
		case types.KindStruct:
			ty := u.tab.Get(t)
			d, _ := u.c.declOfItem(it).(*types.StructDecl)
			if t != types.Poison && (ty.Kind != types.KNamed || ty.Item != it.ID || d == nil) {
				u.patMismatch(p, "`"+u.c.cfg.Interner.Str(it.Name)+"`", t)
				u.poisonFields(p.Fields)
				return
			}
			if t == types.Poison || d == nil {
				u.poisonFields(p.Fields)
				return
			}
			fs := make([]types.FieldDecl, len(d.Fields))
			for i, f := range d.Fields {
				fs[i] = f
				fs[i].Type = u.c.subst(f.Type, ty.Args, types.NoType)
			}
			u.patFields(p, p.Fields, fs, t, mode, it.Module != u.m.ID)
			return
		}
		u.notPattern(p, it.Kind.String())
		u.poisonFields(p.Fields)
		return
	}
	u.notPattern(p, refNoun(r.Kind))
	u.poisonFields(p.Fields)
}

// pathRoot is the Ref of the last segment of a RefDeferred path's resolved
// prefix: what the member tail hangs off. RefNone for a malformed path.
func (u *unit) pathRoot(p *ast.PathPattern, r resolve.Ref) resolve.Ref {
	if p.Path == nil || r.Segs == 0 || int(r.Segs) > len(p.Path.Names) || p.Path.Names[r.Segs-1] == nil {
		return resolve.Ref{}
	}
	return u.ref(p.Path.Names[r.Segs-1])
}

// rootEnum is the enum a deferred path's root names: an enum item, or an
// alias whose type is an instance of one (`type Q = E`, then `Q.X`, as
// `Q.X` is a value).
func (u *unit) rootEnum(p *ast.PathPattern, root resolve.Ref) (*types.Item, bool) {
	if root.Kind != resolve.RefItem {
		return nil, false
	}
	it := u.c.cfg.Items.Get(types.ItemID(root.A))
	switch it.Kind {
	case types.KindEnum:
		return it, true
	case types.KindAlias:
		h, ok := u.typeHead(p.Path.Names[u.ref(p).Segs-1])
		if ok && h.it != nil && h.it.Kind == types.KindEnum {
			return h.it, true
		}
	}
	return nil, false
}

// notPattern is E0301 for a path that names no enum variant, error or
// struct: the pattern can match nothing.
func (u *unit) notPattern(p *ast.PathPattern, found string) {
	var b strings.Builder
	if p.Path != nil {
		for i, id := range p.Path.Names {
			if i > 0 {
				b.WriteByte('.')
			}
			if id != nil {
				b.WriteString(u.c.cfg.Interner.Str(id.Name))
			}
		}
	}
	a := "a "
	if strings.IndexByte("aeiou", found[0]) >= 0 {
		a = "an "
	}
	u.err(diag.E0301, p.Span(), "`"+b.String()+"` is "+a+found+", not a pattern").Text("expected", "pattern").Text("found", found).
		Alt("match a variant `.V`, an error type, or bind a name")
}

// refNoun names what a Ref of kind k is, for notPattern.
func refNoun(k resolve.RefKind) string {
	switch k {
	case resolve.RefLocal, resolve.RefParam, resolve.RefResult, resolve.RefField:
		return "value"
	case resolve.RefModule:
		return "module"
	case resolve.RefTypeParam:
		return "type parameter"
	case resolve.RefPrim:
		return "primitive type"
	case resolve.RefSelfType:
		return "type"
	case resolve.RefUniverse:
		return "universe name"
	}
	return "name"
}

// errorTest is an error-type pattern: the scrutinee is an error set
// (or the error side of err(p)) that contains the error, else E0301.
func (u *unit) errorTest(p ast.Pattern, it *types.Item, fields []*ast.PatField, hasFields bool, t types.TypeID, mode ast.RefMode) {
	*u.b.use(p.ID()) = Use{Kind: UseError, Item: it.ID}
	if t == types.Poison {
		u.poisonFields(fields)
		return
	}
	ty := u.tab.Get(t)
	member := false
	if ty.Kind == types.KErrorSet {
		for _, m := range u.tab.Members(ty.Errors) {
			if !m.IsParam && m.Item == it.ID {
				member = true
			}
		}
	}
	if !member {
		name := u.c.cfg.Interner.Str(it.Name)
		u.err(diag.E0301, p.Span(), "`"+name+"` is not in `"+u.str(t)+"`").Text("expected", u.str(t)).Text("found", name)
		u.poisonFields(fields)
		return
	}
	if d, ok := u.c.declOfItem(it).(*types.ErrorDecl); ok && hasFields {
		u.patFields(p, fields, d.Fields, t, mode, false)
	}
}

// poisonFields gives every binding under fields Poison.
func (u *unit) poisonFields(fields []*ast.PatField) {
	for _, f := range fields {
		if f == nil {
			continue
		}
		if f.Pat == nil {
			u.bindName(f.Name, types.Poison)
			continue
		}
		u.poisonPattern(f.Pat)
	}
}

// poisonPattern gives every binding under p Poison.
func (u *unit) poisonPattern(p ast.Pattern) {
	ast.Inspect(p, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.NamePattern:
			u.bindName(n.Name, types.Poison)
		case *ast.PatField:
			if n.Pat == nil {
				u.bindName(n.Name, types.Poison)
			}
		}
		return n != nil
	})
}
