// Why: generators run at stage 4, before bodies are checked, and still need
// to see the declarations of the program they generate for.
//
// What: GenView with Decl and TypeOf, and its records GenDecl, GenField,
// GenVariant and GenMethod; the driver adapts them into gen.World and
// gen.Line's type arguments.
//
// How: field names and field types are translated to TypeIDs where the view
// resolves them, else kept as the bare name; interface method signatures
// use KSelf; enum variants are listed; there are no layouts. A declaration
// of the view's own module is read from its AST (fromAST), one of another
// module from its checked Decl (fromDecl).

package check

import (
	"mk/internal/ast"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// GenView is the stage-4 view of one module, the module of a set of gen
// lines, before its stage 5: built from resolve's answers and the AST, it
// never writes an Item.Decl, never registers a Module and reports nothing
// (stage 5 reports every error of these declarations). Items of other
// modules, which are through stage 5, are read from their Decl. Safe for
// concurrent use once built (it only reads, and Table interns under its
// own lock).
type GenView struct {
	c      *Checker
	mod    types.ModuleID
	files  []*fileIndex
	byItem map[types.ItemID]*resolve.ItemInfo
}

// GenDecl is one declaration as a generator sees it (gen.DeclView).
type GenDecl struct {
	Item     types.ItemID
	Kind     types.ItemKind // KindStruct, KindEnum, KindError, KindInterface
	Exported bool
	// Generics have names, Const and a const parameter's Type; bounds are
	// not translated at stage 4 (none of the generator API reads them).
	Generics []types.GenericParam
	Fields   []GenField   // struct and error, declaration order
	Variants []GenVariant // enum, declaration order
	Methods  []GenMethod  // interface, declaration order
}

// GenField is one field. Type is NoType when the view cannot translate the
// written type: then Unresolved is the bare name when the type is a name that
// does not resolve with gen/ empty (E0906's input), and zero for anything else
// the view does not translate (a computed array length, for instance).
type GenField struct {
	Name       source.Symbol
	Type       types.TypeID
	Unresolved source.Symbol
	Exported   bool
	Span       source.Span
}

// GenVariant is one enum variant.
type GenVariant struct {
	Name   source.Symbol
	Fields []GenField
	Span   source.Span
}

// GenMethod is one interface method: receiver first as `self` of type Self,
// &Self or mut Self; Flows.Declared is set when the method writes any flow
// clause; Into from `into` names. Flow sources and stores are stage 5's.
type GenMethod struct {
	Sig  types.Signature
	Span source.Span
	// Flows are the clauses the signature declares (stage4_funcs.go).
	Flows []GenFlow
}

// GenView builds the stage-4 view of in's module. in.Info is resolve's
// first pass (Finish, before Settle): a name that may be declared by gen
// output is RefPending or RefPoison there, and the view gives it back as a
// bare name.
func (c *Checker) GenView(in *ModuleInput) *GenView {
	source.Assertf(in.Info != nil && in.Info.Module == in.Module, source.Span{}, "check: GenView without the module's resolve.Info")
	v := &GenView{c: c, mod: in.Module, byItem: make(map[types.ItemID]*resolve.ItemInfo, len(in.Info.Items))}
	for _, fi := range in.Info.Files {
		v.files = append(v.files, newFileIndex(fi))
	}
	for _, ii := range in.Info.Items {
		v.byItem[ii.Item] = ii
	}
	return v
}

// Decl describes a struct, enum, error or interface item as seen from
// module from: an item of another module shows its exported fields only, an
// item of from itself all of them. False for any other kind, or an item of
// this view's module the view has no node for.
func (v *GenView) Decl(item types.ItemID, from types.ModuleID) (*GenDecl, bool) {
	it := v.c.cfg.Items.Get(item)
	switch it.Kind {
	case types.KindStruct, types.KindEnum, types.KindError, types.KindInterface:
	default:
		return nil, false
	}
	var d *GenDecl
	if it.Module == v.mod {
		ii := v.byItem[item]
		if ii == nil || ii.File < 0 || ii.File >= len(v.files) {
			return nil, false
		}
		d = v.fromAST(it, v.files[ii.File], ii.Node)
	} else {
		d = v.fromDecl(it)
	}
	if d == nil {
		return nil, false
	}
	if it.Module != from {
		d.Fields = exportedOnly(d.Fields)
		for i := range d.Variants {
			d.Variants[i].Fields = exportedOnly(d.Variants[i].Fields)
		}
	}
	return d, true
}

func exportedOnly(fs []GenField) []GenField {
	var out []GenField
	for _, f := range fs {
		if f.Exported {
			out = append(out, f)
		}
	}
	return out
}

// TypeOf translates a type expression of file (an index of the module's
// resolve.Info.Files), such as a gen line's type argument: the TypeID, or
// NoType with the bare name of an unresolved name.
func (v *GenView) TypeOf(file int, n ast.Node) (types.TypeID, source.Symbol) {
	if file < 0 || file >= len(v.files) {
		return types.NoType, 0
	}
	g := &gctx{v: v, fx: v.files[file], self: types.NoType}
	return g.typ(n)
}

// fromDecl is the view of a declaration through stage 5.
func (v *GenView) fromDecl(it *types.Item) *GenDecl {
	d := &GenDecl{Item: it.ID, Kind: it.Kind, Exported: it.Exported}
	fields := func(fs []types.FieldDecl) []GenField {
		out := make([]GenField, 0, len(fs))
		for _, f := range fs {
			out = append(out, GenField{Name: f.Name, Type: f.Type, Exported: f.Exported, Span: f.Span})
		}
		return out
	}
	switch x := it.Decl.(type) {
	case *types.StructDecl:
		d.Generics, d.Fields = x.Generics, fields(x.Fields)
	case *types.ErrorDecl:
		d.Fields = fields(x.Fields)
	case *types.EnumDecl:
		d.Generics = x.Generics
		for _, vr := range x.Variants {
			gv := GenVariant{Name: vr.Name, Span: vr.Span, Fields: fields(vr.Fields)}
			for i := range gv.Fields {
				gv.Fields[i].Exported = true // a variant's fields are as visible as the enum
			}
			d.Variants = append(d.Variants, gv)
		}
	case *types.InterfaceDecl:
		d.Generics = x.Generics
		for _, s := range x.Methods {
			d.Methods = append(d.Methods, GenMethod{Sig: s, Span: s.Span, Flows: v.flowsSig(&s)})
		}
	default:
		return nil
	}
	return d
}

// fromAST is the view of a declaration of this module, from its node.
func (v *GenView) fromAST(it *types.Item, fx *fileIndex, id ast.NodeID) *GenDecl {
	g := &gctx{v: v, fx: fx, self: types.NoType}
	d := &GenDecl{Item: it.ID, Kind: it.Kind, Exported: it.Exported}
	switch n := fx.node(id).(type) {
	case *ast.StructDecl:
		d.Generics = g.generics(n.TypeParams)
		for _, m := range n.Members {
			if f, ok := m.(*ast.FieldMember); ok && f.Name != nil {
				d.Fields = append(d.Fields, g.field(f.Name, f.Type, f.Export, f.Span()))
			}
		}
	case *ast.ErrorDecl:
		for _, f := range n.Fields {
			if f != nil && f.Name != nil {
				d.Fields = append(d.Fields, g.field(f.Name, f.Type, true, f.Span()))
			}
		}
	case *ast.EnumDecl:
		d.Generics = g.generics(n.TypeParams)
		for _, vr := range n.Variants {
			if vr == nil || vr.Name == nil {
				continue
			}
			gv := GenVariant{Name: vr.Name.Name, Span: vr.Span()}
			for _, f := range vr.Fields {
				if f != nil && f.Name != nil {
					gv.Fields = append(gv.Fields, g.field(f.Name, f.Type, true, f.Span()))
				}
			}
			d.Variants = append(d.Variants, gv)
		}
	case *ast.InterfaceDecl:
		d.Generics = g.generics(n.TypeParams)
		g.self = v.c.cfg.Types.SelfType()
		for _, s := range n.Methods {
			if s != nil {
				d.Methods = append(d.Methods, GenMethod{Sig: g.method(it, s, d.Generics), Span: s.Span(), Flows: v.flowsAST(s)})
			}
		}
	default:
		return nil
	}
	return d
}

// gctx translates type expressions of one file for the view.
type gctx struct {
	v    *GenView
	fx   *fileIndex
	self types.TypeID
}

func (g *gctx) ref(n ast.Node) resolve.Ref {
	if id := n.ID(); int(id) < len(g.fx.info.Refs) {
		return g.fx.info.Refs[id]
	}
	return resolve.Ref{}
}

func (g *gctx) field(name *ast.Ident, t ast.Node, exported bool, at source.Span) GenField {
	id, un := g.typ(t)
	return GenField{Name: name.Name, Type: id, Unresolved: un, Exported: exported, Span: at}
}

func (g *gctx) generics(tp *ast.TypeParams) []types.GenericParam {
	if tp == nil {
		return nil
	}
	var out []types.GenericParam
	for _, p := range tp.Params {
		if p == nil || p.Name == nil {
			continue
		}
		gp := types.GenericParam{Const: p.Const, Name: p.Name.Name, Span: p.Span()}
		if p.Const {
			gp.Type, _ = g.typ(p.Type)
		}
		out = append(out, gp)
	}
	return out
}

// method is an interface method's signature at stage 4.
func (g *gctx) method(it *types.Item, s *ast.Signature, owner []types.GenericParam) types.Signature {
	tab := g.v.c.cfg.Types
	own := g.generics(s.TypeParams)
	sig := types.Signature{Item: it.ID, Result: types.Void, Errors: types.EmptyErrors, Span: s.Span(),
		Generics: append(append([]types.GenericParam(nil), owner...), own...), OwnGenerics: uint32(len(own))}
	if s.Name != nil && s.Name.Name != nil {
		sig.Name = s.Name.Name.Name
	}
	if s.Self != nil {
		self := tab.SelfType()
		switch s.Self.Mode {
		case ast.ByValue:
			sig.Receiver = types.ByValue
		case ast.ByShared:
			sig.Receiver, self = types.ByShared, tab.Ref(self)
		default:
			sig.Receiver, self = types.ByMut, tab.Mut(self)
		}
		sig.Params = append(sig.Params, types.Param{Name: source.SymSelf, Type: self, Span: s.Self.Span()})
	}
	for _, p := range s.Params {
		if p == nil || p.Name == nil {
			continue
		}
		t, _ := g.typ(p.Type)
		sig.Params = append(sig.Params, types.Param{Name: p.Name.Name, Type: t, Const: p.Const, Var: p.Var, Span: p.Span()})
	}
	for i, p := range s.Params {
		if p == nil || p.Into == nil || i >= len(sig.Params) {
			continue
		}
		for k, q := range sig.Params {
			if q.Name == p.Into.Name {
				sig.Params[len(sig.Params)-len(s.Params)+i].Into = types.ParamIndex(k + 1)
			}
		}
	}
	if s.Self != nil && s.Self.Into != nil {
		for k, q := range sig.Params {
			if q.Name == s.Self.Into.Name {
				sig.Params[0].Into = types.ParamIndex(k + 1)
			}
		}
	}
	sig.Variadic = s.Ellipsis.End > s.Ellipsis.Start
	sig.Flows.Declared = len(s.Flows) > 0
	if s.Result != nil {
		sig.Result, sig.Errors, _ = g.result(s.Result)
	}
	return sig
}

// result translates a result type as check's tctx.result reads it: a
// written E!T (parenthesized or not) splits into T and its set, a failing
// result; any other type, an alias of an E!T or a type parameter
// included, is the result as a value, with no errors.
func (g *gctx) result(n ast.Type) (types.TypeID, types.ErrorSetID, source.Symbol) {
	r, un := g.typ(n)
	if r == types.NoType {
		return r, types.EmptyErrors, un
	}
	e := n
	if p, ok := e.(*ast.ParenType); ok {
		e = p.Type
	}
	if _, written := e.(*ast.ErrorType); written {
		if ty := g.v.c.cfg.Types.Get(r); ty.Kind == types.KErrorUnion {
			return ty.Elem, ty.Errors, 0
		}
	}
	return r, types.EmptyErrors, 0
}

// typ translates one type expression: the TypeID, or NoType with the bare
// name of an unresolved name (zero when the view cannot translate it).
func (g *gctx) typ(n ast.Node) (types.TypeID, source.Symbol) {
	tab := g.v.c.cfg.Types
	wrap := func(f func(types.TypeID) types.TypeID, e ast.Node) (types.TypeID, source.Symbol) {
		t, un := g.typ(e)
		if t == types.NoType {
			return types.NoType, un
		}
		return f(t), 0
	}
	switch n := n.(type) {
	case nil:
		return types.NoType, 0
	case *ast.ParenType:
		return g.typ(n.Type)
	case *ast.ParenExpr:
		return g.typ(n.X)
	case *ast.PathType:
		var args []ast.TypeOrExpr
		if n.Args != nil {
			args = n.Args.Args
		}
		return g.named(n, args)
	case *ast.Ident, *ast.FieldExpr:
		return g.named(n, nil)
	case *ast.IndexExpr:
		return g.namedAt(n.X, n.Args)
	case *ast.RefType:
		return wrap(tab.Ref, n.Elem)
	case *ast.MutType:
		return wrap(tab.Mut, n.Elem)
	case *ast.MutSliceType:
		return wrap(tab.MutSlice, n.Elem)
	case *ast.SliceType:
		return wrap(tab.Slice, n.Elem)
	case *ast.OptionalType:
		return wrap(tab.Optional, n.Elem)
	case *ast.RawPtrType:
		return wrap(tab.RawPtr, n.Elem)
	case *ast.ArrayType:
		lit, ok := n.Len.(*ast.BasicLit)
		if !ok || lit.Kind != ast.TokInt {
			return types.NoType, 0 // a computed length is stage 5's
		}
		v, ok := parseIntLit(lit.Raw)
		if !ok {
			return types.NoType, 0
		}
		elem, un := g.typ(n.Elem)
		if elem == types.NoType {
			return types.NoType, un
		}
		return tab.Array(types.ConstArg{Kind: types.CAValue, Value: g.v.c.scalarConst(types.Int, 8, v)}, elem), 0
	case *ast.ErrorType:
		return g.errorUnion(n.Errors, n.Ok)
	case *ast.BinaryExpr:
		if n.Op == ast.TokBang {
			return g.errorUnion(n.X, n.Y)
		}
		if n.Op == ast.TokPipe {
			return g.errorSet(n)
		}
	case *ast.UnionType:
		return g.errorSet(n)
	case *ast.RecordType:
		var fs []types.RecordField
		for _, f := range n.Fields {
			if f == nil || f.Name == nil {
				continue
			}
			t, un := g.typ(f.Type)
			if t == types.NoType {
				return types.NoType, un
			}
			fs = appendField(fs, f.Name.Name, t)
		}
		return tab.Record(fs), 0
	case *ast.FnType:
		return g.fnType(n)
	}
	return types.NoType, 0
}

func (g *gctx) named(n ast.Node, args []ast.TypeOrExpr) (types.TypeID, source.Symbol) {
	return g.resolved(n, g.ref(n), args)
}

func (g *gctx) namedAt(head ast.Node, args []ast.TypeOrExpr) (types.TypeID, source.Symbol) {
	return g.resolved(head, g.ref(head), args)
}

func (g *gctx) resolved(n ast.Node, r resolve.Ref, args []ast.TypeOrExpr) (types.TypeID, source.Symbol) {
	tab := g.v.c.cfg.Types
	switch r.Kind {
	case resolve.RefPrim:
		return types.TypeID(r.A), 0
	case resolve.RefTypeParam:
		return tab.Param(r.A), 0
	case resolve.RefSelfType:
		if g.self == types.NoType {
			return types.NoType, 0
		}
		return g.self, 0
	case resolve.RefItem:
		it := g.v.c.cfg.Items.Get(types.ItemID(r.A))
		ga := make([]types.GenericArg, 0, len(args))
		for _, a := range args {
			t, un := g.typ(a)
			if t == types.NoType {
				return types.NoType, un
			}
			ga = append(ga, types.TypeArg(t))
		}
		switch it.Kind {
		case types.KindStruct, types.KindEnum:
			return tab.Named(it.ID, ga), 0
		case types.KindError:
			return tab.ErrorSetType(tab.ErrorSet([]types.ErrorMember{{Item: it.ID}})), 0
		case types.KindAlias:
			return g.alias(it, ga)
		}
	case resolve.RefPending, resolve.RefPoison, resolve.RefNone:
		switch x := n.(type) {
		case *ast.Ident:
			return types.NoType, x.Name // perhaps declared by gen output
		case *ast.PathType:
			if x.Path != nil && len(x.Path.Names) == 1 && x.Path.Names[0] != nil {
				return types.NoType, x.Path.Names[0].Name
			}
		}
	}
	return types.NoType, 0
}

// alias follows an alias to its target with args substituted: a
// same-module alias from its node, another module's from its Decl.
func (g *gctx) alias(it *types.Item, args []types.GenericArg) (types.TypeID, source.Symbol) {
	var t types.TypeID
	var un source.Symbol
	if it.Module == g.v.mod {
		ii := g.v.byItem[it.ID]
		if ii == nil || ii.File < 0 || ii.File >= len(g.v.files) {
			return types.NoType, 0
		}
		ad, ok := g.v.files[ii.File].node(ii.Node).(*ast.AliasDecl)
		if !ok {
			return types.NoType, 0
		}
		t, un = (&gctx{v: g.v, fx: g.v.files[ii.File], self: types.NoType}).typ(ad.Type)
	} else if ad, ok := it.Decl.(*types.AliasDecl); ok {
		t = ad.Type
	}
	if t == types.NoType || t == types.Poison {
		return types.NoType, un
	}
	if len(args) > 0 {
		t = g.v.c.subst(t, args, types.NoType)
	}
	return t, 0
}

// errorSet is a `|` set of errors standing as a type, the target of an
// alias (`type Failure = Bad | OutOfMemory`, DF-lox-07: a method result
// naming it had no type at stage 4, and gen dyn.Of emitted nothing).
func (g *gctx) errorSet(es ast.Node) (types.TypeID, source.Symbol) {
	ms, ok := g.errorMembers(es)
	if !ok {
		return types.NoType, 0
	}
	tab := g.v.c.cfg.Types
	return tab.ErrorSetType(tab.ErrorSet(ms)), 0
}

// errorUnion is E!T with E one error or a `|` set of errors.
func (g *gctx) errorUnion(es, ok ast.Node) (types.TypeID, source.Symbol) {
	ms, good := g.errorMembers(es)
	if !good {
		return types.NoType, 0
	}
	tab := g.v.c.cfg.Types
	elem := types.Void
	if ok != nil {
		var un source.Symbol
		if elem, un = g.typ(ok); elem == types.NoType {
			return types.NoType, un
		}
	}
	return tab.ErrorUnion(tab.ErrorSet(ms), elem), 0
}

// errorMembers are the errors of es, one error, an error-set alias or a
// `|` set of them; false when es names anything else.
func (g *gctx) errorMembers(es ast.Node) ([]types.ErrorMember, bool) {
	tab := g.v.c.cfg.Types
	var ms []types.ErrorMember
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ParenType:
			return walk(x.Type)
		case *ast.ParenExpr:
			return walk(x.X)
		case *ast.BinaryExpr:
			return x.Op == ast.TokPipe && walk(x.X) && walk(x.Y)
		case *ast.UnionType:
			for _, m := range x.Types {
				if !walk(m) {
					return false
				}
			}
			return true
		}
		if n == nil {
			return false
		}
		r := g.ref(n)
		if r.Kind != resolve.RefItem {
			return false
		}
		it := g.v.c.cfg.Items.Get(types.ItemID(r.A))
		switch it.Kind {
		case types.KindError:
			ms = append(ms, types.ErrorMember{Item: it.ID})
			return true
		case types.KindAlias:
			// A same-module alias has no Decl yet at stage 4: follow its
			// node, as a type position does (DF-lox-03: `type Failure =
			// Bad` in a method result left the method with no result, and
			// gen dyn.Of emitted nothing).
			if t, _ := g.alias(it, nil); t != types.NoType && tab.Kind(t) == types.KErrorSet {
				ms = append(ms, tab.Members(tab.Get(t).Errors)...)
				return true
			}
		}
		return false
	}
	return ms, walk(es)
}

// fnType is fn(..): R with no flows (stage 5 adds them).
func (g *gctx) fnType(n *ast.FnType) (types.TypeID, source.Symbol) {
	tab := g.v.c.cfg.Types
	ft := types.FnType{Result: types.Void, Errors: types.EmptyErrors}
	for _, p := range n.Params {
		if p == nil {
			return types.NoType, 0
		}
		t, un := g.typ(p.Type)
		if t == types.NoType {
			return types.NoType, un
		}
		ft.Params = append(ft.Params, types.FnParam{Type: t})
	}
	if n.Result != nil {
		r, errs, un := g.result(n.Result)
		if r == types.NoType {
			return types.NoType, un
		}
		ft.Result, ft.Errors = r, errs
	}
	return tab.Fn(ft), 0
}
