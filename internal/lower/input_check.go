// Why: lower reads check's results only through the Input interface; this
// file is the one place that knows where check keeps each fact.
//
// What: FromCheck and checkInput, the adapter from a *check.Body and the
// Checker's facts API to Input (Use, Call, Pattern, Literal, Binding,
// Convert, Sig and the rest); ImplsOf; OnerrConsumes; losslessByType.
//
// How: facts are read from the body's own tables, or from the tables of the
// fields-loop copy currently entered (EnterCopy, LeaveCopy), and check's
// kinds are mapped to lower's. A test's signature, which check does not
// synthesize, is built here (testErrors).

package lower

import (
	"mk/internal/ast"
	"mk/internal/check"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// FromCheck adapts one checked unit to Input. cfg supplies the tables.
func FromCheck(cfg *Config, c *check.Checker, b *check.Body) Input {
	in := &checkInput{cfg: cfg, c: c, b: b, tab: cfg.Types}
	in.top = tables{base: b.Base, types: b.Types, uses: b.Uses, adjust: b.Adjust, consts: b.Consts, bind: b.Bind, diverges: b.Diverges, copy: -1}
	in.gens = in.sigOf().Generics
	if len(b.Generics) == len(in.gens) && b.Generics != nil {
		in.gens = b.Generics // the bounds check saw (implicit ones included)
	}
	all := append(append([]types.GenericParam(nil), in.gens...), b.Hidden...)
	in.env = c.NewEnv(all)
	in.nodes = make([]ast.Node, len(b.Types))
	ast.Inspect(b.Node, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if i := int(n.ID()) - int(b.Base); i >= 0 && i < len(in.nodes) {
			in.nodes[i] = n
		}
		return true
	})
	return in
}

// ImplsOf is Config.Impls over a Checker: the impl of iface for the closed
// type t, NoItem for a type that mentions a parameter or has none.
func ImplsOf(c *check.Checker) func(types.ItemID, []types.GenericArg, types.TypeID) types.ItemID {
	return func(iface types.ItemID, args []types.GenericArg, t types.TypeID) types.ItemID {
		var rs types.ReadSet
		var id types.ItemID
		if ie := source.Guard("ImplOf", source.Span{}, func() {
			if x, ok := c.ImplOf(iface, args, t, &rs); ok {
				id = x
			}
		}); ie != nil {
			return types.NoItem
		}
		return id
	}
}

type checkInput struct {
	cfg  *Config
	c    *check.Checker
	b    *check.Body
	tab  *types.Table
	env  *check.Env
	gens []types.GenericParam
	sig  *types.Signature
	// nodes indexes the unit's AST by NodeID - b.Base.
	nodes []ast.Node
	// top is the body's own tables; copies the stack of entered fields-loop
	// copies, innermost last.
	top    tables
	copies []tables
}

// tables is one set of check's per-node tables over [base, base+len(types)).
type tables struct {
	base     ast.NodeID
	types    []types.TypeID
	uses     []check.Use
	adjust   []check.Adjust
	consts   []types.ConstID
	bind     []check.LocalID
	diverges check.Bits
	copy     int // index into Body.Unrolled, -1 for the body's own
}

// at is the innermost tables covering id and id's slot in them, or nil.
func (in *checkInput) at(id ast.NodeID) (*tables, int) {
	for k := len(in.copies) - 1; k >= 0; k-- {
		t := &in.copies[k]
		if i := int(id) - int(t.base); i >= 0 && i < len(t.types) {
			return t, i
		}
	}
	if i := int(id) - int(in.top.base); i >= 0 && i < len(in.top.types) {
		return &in.top, i
	}
	return nil, -1
}

func (in *checkInput) FieldCopies(loop ast.NodeID) []FieldCopy {
	parent := int32(-1)
	if n := len(in.copies); n > 0 {
		parent = int32(in.copies[n-1].copy)
	}
	var out []FieldCopy
	for i := range in.b.Unrolled {
		if c := &in.b.Unrolled[i]; c.Loop == loop && c.Parent == parent {
			out = append(out, FieldCopy{ID: i, Field: c.Field})
		}
	}
	return out
}

func (in *checkInput) EnterCopy(fc FieldCopy) {
	c := &in.b.Unrolled[fc.ID]
	in.copies = append(in.copies, tables{base: c.Base, types: c.Types, uses: c.Uses, adjust: c.Adjust, consts: c.Consts, bind: c.Bind, diverges: c.Diverges, copy: fc.ID})
}

func (in *checkInput) LeaveCopy() { in.copies = in.copies[:len(in.copies)-1] }

// ImplicitRecord is check's Body.ImplicitRecord; the call node lies
// in the body's own range whether or not a fields-loop copy is entered.
func (in *checkInput) ImplicitRecord(call ast.NodeID) bool {
	return in.b.ImplicitRecord.Has(call, in.b.Base)
}

// iterCall is the Next call check recorded on the source of a for over an
// iterator, or Call{} for any other source.
func (in *checkInput) iterCall(rs *ast.RefSource) Call {
	u := in.use(rs.ID())
	c := Call{Args: in.generic(u.Inst), Errors: types.EmptyErrors}
	switch u.Kind {
	case check.UseMethod:
		c.Kind, c.Item, c.Sig = CallFunc, u.Item, in.cfg.callee(u.Item)
	case check.UseIfaceMethod:
		c.Kind, c.Iface, c.Method = CallIface, u.Item, u.Index
		if d, ok := in.cfg.Items.Get(u.Item).Decl.(*types.InterfaceDecl); ok && int(u.Index) < len(d.Methods) {
			c.Sig = &d.Methods[u.Index]
		}
		t := in.Type(rs.X.ID())
		for n := in.Adjust(rs.ID()).Deref; n > 0; n-- {
			t = in.tab.Get(t).Elem
		}
		c.SelfType = t
	default:
		return Call{}
	}
	return c
}

// IsValue is check's Body.Values; like ImplicitRecord, the node
// lies in the body's own range whether or not a fields-loop copy is entered.
func (in *checkInput) IsValue(id ast.NodeID) bool {
	return in.b.Values.Has(id, in.b.Base)
}

// AsmTemplate is check's canonical template and option bits; with
// options(raw) the literal's text as written.
func (in *checkInput) AsmTemplate(s *ast.AsmStmt) (string, uint16) {
	opts := check.AsmOptionBits(in.cfg.Interner, s)
	if mir.AsmOptions(opts)&mir.AsmRaw != 0 { // no hole is read
		return check.AsmLiteral(s), opts
	}
	text, _, _ := check.AsmTemplate(in.cfg.Interner, in.b, s)
	return text, opts
}

// EnvRef is check's Body.EnvRef: a capturing literal's hidden parameter
// type, NoType for every other body.
func (in *checkInput) EnvRef() types.TypeID { return in.b.EnvRef }

// Env is check's Body.Envs entry for FnLit lit, its sources mapped as
// every other local use is.
func (in *checkInput) Env(lit ast.NodeID) (Env, bool) {
	for _, e := range in.b.Envs {
		if e.Lit != lit {
			continue
		}
		srcs := make([]Use, len(e.Sources))
		for i, s := range e.Sources {
			if s.Kind != check.UseLocal || int(s.Local) >= len(in.b.Locals) {
				source.ICEf(in.b.Node.Span(), "lower: an environment source of kind %d", s.Kind)
			}
			srcs[i] = in.localUse(s.Local)
		}
		return Env{Item: e.Item, Ref: e.Ref, Sources: srcs, Modes: e.Modes}, true
	}
	return Env{}, false
}

// literalArgs are the generic arguments a fn literal is named with inside
// the body that writes it: its generics are its owner's (resolve's
// declLiteral), so the arguments are those generics themselves, each a
// parameter of the body. Check records no instantiation for a FnLit; with
// none, a literal of a generic body was named at no arguments and mono's
// instance of it had none (an ICE for a literal passed as a const argument
// in a generic function). Nil for any other
// item and for a literal of a non-generic owner.
func (in *checkInput) literalArgs(item types.ItemID) []types.GenericArg {
	it := in.cfg.Items.Get(item)
	if it.Kind != types.KindLiteral {
		return nil
	}
	fd, ok := it.Decl.(*types.FuncDecl)
	if !ok || len(fd.Sig.Generics) == 0 {
		return nil
	}
	args := make([]types.GenericArg, len(fd.Sig.Generics))
	for i, g := range fd.Sig.Generics {
		if g.Const {
			args[i] = types.ConstGenericArg(types.ConstArg{Kind: types.CAParam, Param: uint32(i), Type: g.Type})
		} else {
			args[i] = types.TypeArg(in.tab.Param(uint32(i)))
		}
	}
	return args
}

// localUse is the Use of check's local l: a parameter (a test's sys
// included), a capture of this literal unit (Local.Capture), or a binding.
func (in *checkInput) localUse(l check.LocalID) Use {
	loc := in.b.Locals[l]
	if loc.Param != types.NoParam {
		return Use{Kind: UseParam, Param: loc.Param}
	}
	if t, ok := in.b.Node.(*ast.TestItem); ok && t.Param != nil && l == 1 {
		return Use{Kind: UseParam, Param: 1}
	}
	if loc.Capture != 0 {
		ci := loc.Capture - 1
		direct := int(ci) < len(in.b.Captures) && in.b.Captures[ci].Mode == check.CapReborrow
		return Use{Kind: UseCapture, Capture: ci, Direct: direct}
	}
	return Use{Kind: UseLocal, Binding: loc.Node}
}

func (in *checkInput) Item() types.ItemID { return in.b.Item }
func (in *checkInput) File() *ast.File    { return in.b.File }

func (in *checkInput) Kind() BodyKind {
	switch n := in.b.Node.(type) {
	case *ast.FnLit:
		return BodyLiteral
	case *ast.TestItem:
		return BodyTest
	case *ast.ConstDecl:
		return BodyConst
	case *ast.GlobalDecl:
		return BodyGlobal
	case *ast.Contract:
		if n.Kind == ast.Ensures {
			return BodyEnsures
		}
		return BodyRequires
	case *ast.InvariantMember:
		return BodyInvariant
	case *ast.ErrorDecl:
		return BodyErrorFormat
	}
	return BodyFunc
}

// ErrorFormat is the plan of a provided Format method's body.
func (in *checkInput) ErrorFormat() *check.ErrorFormat { return in.b.ErrorFormat }

func (in *checkInput) Root() ast.Node {
	switch n := in.b.Node.(type) {
	case *ast.FuncDecl:
		return n.Body
	case *ast.ExternDecl:
		return n.Body
	case *ast.FnLit:
		return n.Body
	case *ast.TestItem:
		return n.Body
	case *ast.ConstDecl:
		return n.Value
	case *ast.GlobalDecl:
		return n.Value
	case *ast.Contract:
		return n.X
	case *ast.InvariantMember:
		return n.X
	}
	if n, ok := in.b.Node.(*ast.ErrorDecl); ok {
		return n // a provided Format: no body, the error's span
	}
	source.ICEf(in.b.Node.Span(), "lower: unit node %T", in.b.Node)
	return nil
}

// sigOf is the unit's signature, synthesized for a test, const or global.
func (in *checkInput) sigOf() *types.Signature {
	if in.sig != nil {
		return in.sig
	}
	if in.b.Sig != nil {
		in.sig = in.b.Sig
		return in.sig
	}
	it := in.cfg.Items.Get(in.b.Item)
	s := &types.Signature{Item: it.ID, Name: it.Name, Result: types.Void, Span: it.Span}
	switch n := in.b.Node.(type) {
	case *ast.ConstDecl, *ast.GlobalDecl:
		if len(in.b.Locals) > 0 {
			s.Result = in.b.Locals[0].Type
		}
		if s.Result == types.NoType {
			s.Result = in.b.TypeOf(in.Root().ID())
		}
	case *ast.TestItem:
		if n.Param != nil && len(in.b.Locals) > 1 {
			s.Params = []types.Param{{Name: n.Param.Name.Name, Type: in.b.Locals[1].Type, Span: n.Param.Span()}}
		}
		s.Errors = in.testErrors(n)
	}
	in.sig = s
	return s
}

// testErrors is the union of the error sets of every try and fail in a test
// body (check does not synthesize the test's signature).
func (in *checkInput) testErrors(n *ast.TestItem) types.ErrorSetID {
	var es types.ErrorSetID
	if n.Body == nil {
		return es
	}
	ast.Inspect(n.Body, func(x ast.Node) bool {
		switch x := x.(type) {
		case nil:
			return false
		case *ast.UnaryExpr:
			if x.Op == ast.TokTry {
				if t := in.tab.Get(in.Type(x.X.ID())); t.Kind == types.KErrorUnion {
					es = in.tab.Union(es, t.Errors)
				}
			}
		case *ast.FailStmt:
			if t := in.tab.Get(in.Type(x.Value.ID())); t.Kind == types.KErrorSet {
				es = in.tab.Union(es, t.Errors)
			}
		}
		return true
	})
	return es
}

func (in *checkInput) Sig() *types.Signature { return in.sigOf() }

func (in *checkInput) GenericCount() uint32 { return uint32(len(in.gens) + len(in.b.Hidden)) }

func (in *checkInput) node(id ast.NodeID) ast.Node {
	if i := int(id) - int(in.b.Base); i >= 0 && i < len(in.nodes) {
		return in.nodes[i]
	}
	return nil
}

func (in *checkInput) Nodes() (ast.NodeID, int) { return in.b.Base, len(in.b.Types) }

func (in *checkInput) Type(id ast.NodeID) types.TypeID {
	t, i := in.at(id)
	if t == nil {
		return types.NoType
	}
	if ty := t.types[i]; ty != types.NoType {
		return ty
	}
	if i < len(t.bind) {
		if l := t.bind[i]; l != 0 {
			return in.b.Locals[l].Type
		}
	}
	return types.NoType
}

func (in *checkInput) use(id ast.NodeID) check.Use {
	t, i := in.at(id)
	if t == nil || i >= len(t.uses) {
		return check.Use{}
	}
	return t.uses[i]
}

func (in *checkInput) adjust(id ast.NodeID) check.Adjust {
	t, i := in.at(id)
	if t == nil || i >= len(t.adjust) {
		return check.Adjust{}
	}
	return t.adjust[i]
}

func (in *checkInput) generic(inst uint32) []types.GenericArg {
	if inst == 0 || int(inst) >= len(in.b.Generic) {
		return nil
	}
	return in.b.Generic[inst]
}

func (in *checkInput) Use(id ast.NodeID) Use {
	u := in.use(id)
	switch u.Kind {
	case check.UseLocal:
		return in.localUse(u.Local)
	case check.UseConst:
		return Use{Kind: UseConst, Item: u.Item}
	case check.UseGlobal:
		return Use{Kind: UseGlobal, Item: u.Item}
	case check.UseItem:
		args := in.generic(u.Inst)
		if args == nil {
			args = in.literalArgs(u.Item)
		}
		return Use{Kind: UseFunc, Item: u.Item, Args: args}
	case check.UseVariant:
		return Use{Kind: UseVariant, Item: u.Item, Variant: u.Index, Args: in.generic(u.Inst)}
	case check.UseError:
		return Use{Kind: UseError, Item: u.Item}
	case check.UseField:
		if _, ok := in.node(id).(*ast.Ident); ok {
			if u.Local != 0 && int(u.Local) < len(in.b.Locals) && in.b.Locals[u.Local].Capture != 0 {
				// A bare invariant field inside a fn literal: self is the
				// literal's capture.
				return Use{Kind: UseSelfField, Item: u.Item, Field: u.Index, Capture: in.b.Locals[u.Local].Capture}
			}
			return Use{Kind: UseSelfField, Item: u.Item, Field: u.Index}
		}
		return Use{Kind: UseField, Item: u.Item, Field: u.Index}
	case check.UseBuiltin:
		if check.Builtin(u.Index) == check.BuiltinTarget {
			return Use{Kind: UseTarget}
		}
	case check.UseNone:
		if x, ok := in.node(id).(*ast.Ident); ok {
			if x.Name == source.SymNone {
				return Use{Kind: UseNoneLit}
			}
			for k, g := range in.gens {
				if g.Const && g.Name == x.Name {
					return Use{Kind: UseConstParam, Generic: uint32(k)}
				}
			}
		}
	}
	return Use{}
}

func (in *checkInput) Adjust(id ast.NodeID) Adjust {
	a := in.adjust(id)
	switch a.Kind {
	case check.AdjRefShared:
		return Adjust{Kind: AdjRefShared, Deref: a.Deref}
	case check.AdjRefMut:
		return Adjust{Kind: AdjRefMut, Deref: a.Deref}
	case check.AdjDeref:
		return Adjust{Kind: AdjDeref, Deref: a.Deref}
	case check.AdjCopyThrough:
		return Adjust{Kind: AdjCopyThrough, Deref: a.Deref}
	}
	return Adjust{Deref: a.Deref}
}

func (in *checkInput) Convert(id ast.NodeID) Conversion {
	if a := in.adjust(id); a.Kind == check.AdjWiden {
		return Conversion{Kind: ConvWiden, To: a.To}
	}
	return Conversion{}
}

var builtinMap = map[check.Builtin]Builtin{
	check.BuiltinAssert: BAssert, check.BuiltinPanic: BPanic, check.BuiltinCast: BCast,
	check.BuiltinTrunc: BTrunc, check.BuiltinSizeOf: BSizeOf, check.BuiltinAlignOf: BAlignOf,
	check.BuiltinTarget: BTarget, check.BuiltinEvalArena: BEvalArena, check.BuiltinConvert: BConvert,
	check.BuiltinOptMust: BOptMust, check.BuiltinOptIsSome: BOptIsSome,
	check.BuiltinOptIsNone: BOptIsNone, check.BuiltinErrMust: BResMust, check.BuiltinLen: BLen,
}

func (in *checkInput) Call(id ast.NodeID) Call {
	n := in.node(id)
	switch n.(type) {
	case *ast.BinaryExpr, *ast.UnaryExpr, *ast.AssignStmt:
		// Check records a vector operator, and a vector compound
		// assignment, as a call of its std.simd intrinsic (check/simd.go).
		if u := in.use(id); u.Kind == check.UseMethod && u.Item != types.NoItem {
			return Call{Kind: CallFunc, Item: u.Item, Args: in.generic(u.Inst), Sig: in.cfg.callee(u.Item), Errors: types.EmptyErrors}
		}
		return Call{}
	}
	if rs, ok := n.(*ast.RefSource); ok {
		return in.iterCall(rs)
	}
	e, ok := n.(*ast.CallExpr)
	if !ok {
		return Call{}
	}
	u := in.use(id)
	c := Call{Args: in.generic(u.Inst)}
	if t := in.tab.Get(in.Type(id)); t.Kind == types.KErrorUnion {
		c.Errors = t.Errors
	}
	switch u.Kind {
	case check.UseItem, check.UseMethod:
		if u.Item == types.NoItem {
			// A const fn parameter (check: UseItem with the parameter's
			// index): a call through its value, which mono makes direct
			// per instance.
			c.Kind = CallValue
			break
		}
		c.Kind, c.Item = CallFunc, u.Item
		c.Sig = in.cfg.callee(u.Item)
	case check.UseIfaceMethod:
		c.Kind, c.Iface, c.Method = CallIface, u.Item, u.Index
		if d, ok := in.cfg.Items.Get(u.Item).Decl.(*types.InterfaceDecl); ok && int(u.Index) < len(d.Methods) {
			c.Sig = &d.Methods[u.Index]
		}
		// x.M(..) or x.M[A](..): with explicit generic arguments the
		// callee is an IndexExpr over the member, whose receiver still
		// gives Self (else SelfType would be left NoType, which
		// mir.Validate rejects).
		if fe, ok := methodField(e.Fun); ok {
			t := in.Type(fe.X.ID())
			for n := in.Adjust(fe.X.ID()).Deref; n > 0; n-- {
				t = in.tab.Get(t).Elem
			}
			c.SelfType = t
		}
	case check.UseFnValueCall:
		c.Kind = CallValue
	case check.UseConstruct:
		c.Item, c.Variant = u.Item, u.Index
		switch in.cfg.Items.Get(u.Item).Kind {
		case types.KindEnum:
			c.Kind = CallVariant
		case types.KindError:
			c.Kind = CallError
		default:
			c.Kind = CallStruct
		}
		for _, a := range e.Args {
			c.Fields = append(c.Fields, in.use(a.ID()).Index)
		}
	case check.UseBuiltin:
		c.Kind, c.Builtin = CallBuiltin, builtinMap[check.Builtin(u.Index)]
		if c.Builtin == BCast && len(e.Args) == 1 {
			c.Lossless = losslessByType(in.tab, in.Type(e.Args[0].Value.ID()), in.Type(id))
		}
	default:
		return Call{}
	}
	if c.Errors != types.EmptyErrors && !in.calleeFails(c, e) {
		// The call fails only when its callee can: a function or method
		// whose result is a type parameter bound to an error union
		// (raw.Read[E!int], that function as a fn value, Src[E!int]'s
		// Get(&self): T) returns that E!T as a value, which
		// try, else, match and Must then take as a stored E!T, not through
		// an error edge the callee has none of.
		c.Errors = types.EmptyErrors
	}
	if (c.Kind == CallFunc || c.Kind == CallIface) && c.Sig != nil && c.Sig.Receiver != types.NoReceiver {
		if fe, ok := methodField(e.Fun); ok && in.use(fe.ID()).Kind == u.Kind {
			switch in.Adjust(fe.X.ID()).Kind {
			case AdjRefShared:
				c.Recv = RecvRef
			case AdjRefMut:
				c.Recv = RecvMut
			case AdjCopyThrough:
				c.Recv = RecvValue
			default:
				c.Recv = RecvPass
			}
		}
	}
	return c
}

// calleeFails reports whether c's callee declares errors (mir.Validate's
// M5): a function's or interface method's signature's set (check's E0707
// keeps an impl from failing where its interface method cannot), or a fn
// value's type's. Any other kind (a builtin, a construction) keeps the call
// type's set.
func (in *checkInput) calleeFails(c Call, e *ast.CallExpr) bool {
	switch c.Kind {
	case CallFunc, CallIface:
		return c.Sig == nil || c.Sig.Errors != types.EmptyErrors
	case CallValue:
		if t := in.tab.Get(in.Type(e.Fun.ID())); t.Kind == types.KFn {
			return t.Fn.Errors != types.EmptyErrors
		}
	}
	return true
}

func (in *checkInput) Pattern(id ast.NodeID) Pat {
	n := in.node(id)
	u := in.use(id)
	switch p := n.(type) {
	case *ast.WildcardPattern:
		return Pat{Kind: PWild}
	case *ast.NamePattern:
		switch u.Kind {
		case check.UseVariant:
			return Pat{Kind: PVariant, Item: u.Item, Variant: u.Index}
		case check.UseError:
			return Pat{Kind: PErrorCase, Item: u.Item}
		case check.UseConst:
			return Pat{Kind: PLit} // a named const: its value, as a literal
		}
		if p.Name.Name == source.SymNone {
			return Pat{Kind: PNone}
		}
		if p.Name.Name == source.SymTrue || p.Name.Name == source.SymFalse {
			return Pat{Kind: PLit}
		}
		if p.Name.Name == source.SymUnderscore {
			return Pat{Kind: PWild}
		}
		return Pat{Kind: PBind}
	case *ast.LiteralPattern:
		return Pat{Kind: PLit}
	case *ast.RangePattern:
		return Pat{Kind: PRange}
	case *ast.VariantPattern:
		return Pat{Kind: PVariant, Item: u.Item, Variant: u.Index}
	case *ast.PathPattern:
		switch u.Kind {
		case check.UseError:
			return Pat{Kind: PErrorCase, Item: u.Item}
		case check.UseConst:
			return Pat{Kind: PLit} // a qualified const: its value, as a literal
		case check.UseNone:
			// A struct pattern `P(x)`: check sets no Use on it,
			// only UseField on its fields (a fuzz crasher,
			// lower_struct_pattern).
			if t := in.tab.Get(in.Type(id)); t.Kind == types.KNamed {
				if _, ok := in.cfg.Items.Get(t.Item).Decl.(*types.StructDecl); ok {
					return Pat{Kind: PStruct, Item: t.Item}
				}
			}
		}
		return Pat{Kind: PVariant, Item: u.Item, Variant: u.Index}
	case *ast.SomePattern:
		return Pat{Kind: PSome}
	case *ast.NonePattern:
		return Pat{Kind: PNone}
	case *ast.OkPattern:
		return Pat{Kind: POk}
	case *ast.ErrPattern:
		return Pat{Kind: PErr}
	case *ast.AltPattern:
		return Pat{Kind: PAlt}
	case *ast.PatField:
		if u.Kind != check.UseField {
			return Pat{}
		}
		return Pat{Kind: PBind, Field: u.Index}
	case *ast.RecordPat:
		if p.Path != nil {
			return Pat{Kind: PStruct, Item: in.tab.Get(in.Type(id)).Item}
		}
		return Pat{Kind: PRecord}
	}
	return Pat{}
}

func (in *checkInput) Literal(id ast.NodeID) Lit {
	t, i := in.at(id)
	if t == nil || i >= len(t.consts) || t.consts[i] == types.NoConst {
		return Lit{}
	}
	cid := t.consts[i]
	v := in.cfg.Consts.Get(cid)
	switch k := in.tab.Kind(v.Type); k {
	case types.KInt, types.KIntPtr, types.KBool, types.KChar:
		if len(v.Relocs) == 0 && len(v.Bytes) <= 8 {
			return Lit{Kind: LitScalar, Type: v.Type, Bits: types.LoadUint(v.Bytes, len(v.Bytes))}
		}
	case types.KNamed:
		if _, ok := in.cfg.Items.Get(in.tab.Get(v.Type).Item).Decl.(*types.EnumDecl); ok && len(v.Relocs) == 0 && len(v.Bytes) <= 8 {
			return Lit{Kind: LitConst, Type: v.Type, Const: cid}
		}
	}
	return Lit{Kind: LitConst, Type: v.Type, Const: cid}
}

func (in *checkInput) Binding(id ast.NodeID) Binding {
	t, i := in.at(id)
	if t == nil || i >= len(t.bind) {
		return Binding{}
	}
	l := t.bind[i]
	if l == 0 {
		return Binding{}
	}
	loc := in.b.Locals[l]
	return Binding{Name: loc.Name, Type: loc.Type, Var: loc.Var, OK: loc.Type != types.NoType}
}

func (in *checkInput) Diverges(id ast.NodeID) bool {
	t, _ := in.at(id)
	if t == nil {
		return false
	}
	return t.diverges.Has(id, t.base)
}

// OnerrConsumes finds the one linear local or parameter the onerr body
// moves (E0807 guarantees at most one): named, with no borrow adjustment,
// in a consuming position, which is a by-value argument or receiver, the
// value of a binding, destructure, discard or assignment, the scrutinee of
// a by-value match, if x := e or for x := e, the operand of an else, or a
// field or element of a record or array literal. The language rule: an
// onerr "is skipped where that value has moved", whichever way it moves it.
// A local the body declares itself is not one (onerr { g := f; g.Abandon() }
// consumes f, not g).
func (in *checkInput) OnerrConsumes(id ast.NodeID) Consumed {
	d, ok := in.node(id).(*ast.DeferStmt)
	if !ok || !d.Onerr {
		return Consumed{}
	}
	var root ast.Node = d.X
	if d.Body != nil {
		root = d.Body
	}
	inside := func(b ast.NodeID) bool {
		n := in.node(b)
		if n == nil {
			return false
		}
		rs, bs := root.Span(), n.Span()
		return bs.File == rs.File && bs.Start >= rs.Start && bs.End <= rs.End
	}
	var out Consumed
	found := false
	consider := func(x ast.Expr) {
		if x == nil || found {
			return
		}
		x = unparen(x)
		if _, ok := x.(*ast.Ident); !ok {
			return
		}
		u := in.Use(x.ID())
		if u.Kind != UseLocal && u.Kind != UseParam {
			return
		}
		if u.Kind == UseLocal && inside(u.Binding) {
			return
		}
		if f, ok := in.Facts(in.Type(x.ID())); !ok || f.Kind != types.Linear {
			return
		}
		if a := in.Adjust(x.ID()); a.Kind == AdjRefShared || a.Kind == AdjRefMut || a.Kind == AdjCopyThrough {
			return
		}
		if u.Kind == UseParam {
			out = Consumed{Param: u.Param}
		} else {
			out = Consumed{Binding: u.Binding}
		}
		found = true
	}
	ast.Inspect(root, func(x ast.Node) bool {
		switch n := x.(type) {
		case *ast.CallExpr:
			if fe, ok := methodField(n.Fun); ok { // x.M or x.M[A]
				consider(fe.X)
			}
			for _, a := range n.Args {
				consider(a.Value)
			}
		case *ast.BindStmt:
			consider(n.Value)
		case *ast.DestructureStmt:
			consider(n.Value)
		case *ast.DiscardStmt:
			consider(n.Value)
		case *ast.AssignStmt:
			if n.Op == ast.TokAssign {
				consider(n.Value)
			}
		case *ast.MatchStmt:
			consider(n.X)
		case *ast.BindCond:
			consider(n.Value)
		case *ast.ElseExpr:
			consider(n.X)
		case *ast.RecordLit:
			for _, f := range n.Fields {
				if f.Value == nil {
					consider(f.Name)
				} else {
					consider(f.Value)
				}
			}
		case *ast.ArrayLit:
			for _, e := range n.Elems {
				consider(e)
			}
		}
		return x != nil
	})
	return out
}

func (in *checkInput) Facts(t types.TypeID) (types.Facts, bool) {
	if t == types.NoType || t == types.Poison {
		return types.Facts{}, false
	}
	return in.c.FactsIn(in.env, t), true
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// losslessByType is check's lossless T(x) table by type pair: an
// integer into a wider one of the same signedness, or an unsigned into a
// wider signed one; f32 into f64; any integer of at most 24 (f32) or 53
// (f64) bits into a float.
func losslessByType(tab *types.Table, from, to types.TypeID) bool {
	f, t := tab.Get(from), tab.Get(to)
	bits := func(x *types.Type) uint8 {
		if x.Kind == types.KIntPtr {
			return 64
		}
		return x.Bits
	}
	switch {
	case from == to:
		return true
	case f.IsInteger() && t.IsInteger():
		fb, tb := bits(f), bits(t)
		if f.Signed == t.Signed {
			return tb >= fb
		}
		return !f.Signed && t.Signed && tb > fb
	case f.Kind == types.KFloat && t.Kind == types.KFloat:
		return t.Bits >= f.Bits
	case f.IsInteger() && t.Kind == types.KFloat:
		return (t.Bits == 32 && bits(f) <= 16) || (t.Bits == 64 && bits(f) <= 32)
	}
	return false
}
