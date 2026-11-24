// Why: the borrow tests run on hand-built MIR, without parsing or checking,
// so they need types, facts and signatures made by hand.
// What: the kit fixture: a types.Table with the test prelude and the library
// types the cases need, their facts as the shape pass computes them,
// signatures with their flows written out by hand, and mb, a small MIR
// builder that emits the shapes lower emits.
// How: newKit declares the prelude in module main; mb gives every span a
// unique offset (span n at byte 10n of file 1), so a diagnostic's anchor
// maps back to the SpanID that made it.

package borrow

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// kit is one fixture world: module "main" holds the test prelude and the
// test's own items; module "std.core" holds the library types.
type kit struct {
	in    *source.Interner
	items *types.Items
	tab   *types.Table
	known *types.KnownItems
	main  *types.Module
	std   *types.Module
	sigs  map[types.ItemID]*types.Signature
	byNm  map[string]types.ItemID
	// plainParams marks generic indices bounded by Plain in the current
	// test's generic world (facts of KParam).
	plainParams map[uint32]bool
	sub         *walker
	memo        map[types.TypeID]types.Facts

	// Named prelude types.
	Point, Res, Alloc, Heap, Arena, Scope, Io types.TypeID
}

func newKit(t testing.TB) *kit {
	t.Helper()
	in := source.NewInterner()
	items := types.NewItems()
	tab := types.NewTable(in, items, types.NewConstTable())
	k := &kit{in: in, items: items, tab: tab, known: &types.KnownItems{}, sigs: map[types.ItemID]*types.Signature{},
		byNm: map[string]types.ItemID{}, plainParams: map[uint32]bool{}, memo: map[types.TypeID]types.Facts{}}
	k.main = &types.Module{Path: "main"}
	items.AddModule(k.main)
	k.std = &types.Module{Path: "std.core", Std: true, UnsafePermitted: true}
	items.AddModule(k.std)
	k.sub = newWalker(&Env{Types: tab}, nil)

	i32 := types.I32
	p0 := tab.Param(0)
	raw := tab.RawPtr(types.U8)
	// Library types (std.core). Their fields are what std declares, cut to
	// what a walk can see.
	k.known.Cell = k.structItem(k.std, "Cell", 1, false, false, field("value", p0))
	k.known.Mutex = k.structItem(k.std, "Mutex", 1, false, false, field("state", types.U32), field("value", p0))
	k.known.SpinCore = k.structItem(k.std, "SpinCore", 1, false, false, field("state", types.U32), field("value", p0))
	k.known.Chan = k.structItem(k.std, "Chan", 1, true, false, field("p", raw))
	k.known.Arena = k.structItem(k.std, "Arena", 0, true, true, field("hdr", raw))
	k.known.Alloc = k.structItem(k.std, "Alloc", 0, false, false, field("ctx", raw))
	k.Alloc = tab.Named(k.known.Alloc, nil)
	k.known.Heap = k.structItem(k.std, "Heap", 0, false, false, field("own", k.Alloc))
	k.known.Scope = k.structItem(k.std, "Scope", 0, true, false, field("words", tab.Array(k.lenArg(16), types.U64)))
	k.known.Io = k.structItem(k.std, "Io", 0, false, false, field("table", raw))
	k.known.Box = k.structItem(k.std, "Box", 1, true, true, field("ptr", tab.RawPtr(p0)))
	k.Heap = tab.Named(k.known.Heap, nil)
	k.Arena = tab.Named(k.known.Arena, nil)
	k.Scope = tab.Named(k.known.Scope, nil)
	k.Io = tab.Named(k.known.Io, nil)
	k.structItem(k.std, "List", 1, true, false, field("ptr", tab.RawPtr(p0)), field("n", types.Int), field("arena", tab.Ref(k.Arena)))
	k.structItem(k.std, "HeapList", 1, true, true, field("ptr", tab.RawPtr(p0)), field("n", types.Int))
	k.mutViewStruct(k.std, "Guard", 1, true, field("m", tab.RawPtr(k.named("Mutex", p0))))
	k.structItem(k.std, "Text", 0, true, false, field("ptr", raw), field("arena", tab.Ref(k.Arena)))

	// The test prelude, module main.
	k.structItem(k.main, "Point", 0, false, false, field("x", i32), field("y", i32))
	k.structItem(k.main, "Res", 0, false, true, field("id", i32))
	k.Point = k.named("Point")
	k.Res = k.named("Res")
	k.method("Res", "Close", types.ByValue, k.Res, nil, types.Void, types.EmptyErrors)
	k.fn("Acquire", []types.Param{k.param("name", types.Str)}, k.Res, types.EmptyErrors)
	k.fn("MakePoint", nil, k.Point, types.EmptyErrors)
	k.fn("Cond", nil, types.Bool, types.EmptyErrors)
	k.genFn("Use", []types.Param{k.param("x", tab.Ref(p0))}, types.Void)
	k.genFn("UseMut", []types.Param{k.param("x", tab.Mut(p0))}, types.Void)
	k.genFn("Take", []types.Param{k.param("x", p0)}, types.Void)
	ex := k.genFn("Exchange", []types.Param{k.param("a", tab.Mut(p0)), k.param("b", tab.Mut(p0))}, types.Void)
	ex.Flows.Stores = []types.Store{{From: 1, Into: 2}, {From: 2, Into: 1}}
	return k
}

func field(name string, t types.TypeID) fieldSpec { return fieldSpec{name, t} }

type fieldSpec struct {
	name string
	t    types.TypeID
}

func (k *kit) lenArg(n uint64) types.ConstArg {
	var b [8]byte
	types.StoreUint(b[:], 8, n)
	c := k.tab.Consts().Intern(types.ConstValue{Type: types.Int, Align: 8, Bytes: b[:]})
	return types.ConstArg{Kind: types.CAValue, Value: c, Type: types.Int}
}

// structItem declares a struct with n generics.
func (k *kit) structItem(m *types.Module, name string, generics int, view, linear bool, fs ...fieldSpec) types.ItemID {
	d := &types.StructDecl{View: view, Linear: linear}
	for i := 0; i < generics; i++ {
		d.Generics = append(d.Generics, types.GenericParam{Name: k.in.Intern(fmt.Sprintf("T%d", i))})
	}
	for _, f := range fs {
		d.Fields = append(d.Fields, types.FieldDecl{Name: k.in.Intern(f.name), Type: f.t})
	}
	id := k.items.AddItem(&types.Item{Module: m.ID, Name: k.in.Intern(name), Kind: types.KindStruct, Exported: true,
		Key: types.ItemKey(m.Path, name), Decl: d})
	k.byNm[name] = id
	return id
}

// mutViewStruct declares a `mut view struct` (linear when linear is set) with n generics.
func (k *kit) mutViewStruct(m *types.Module, name string, generics int, linear bool, fs ...fieldSpec) types.ItemID {
	id := k.structItem(m, name, generics, true, linear, fs...)
	k.items.Get(id).Decl.(*types.StructDecl).MutView = true
	return id
}

// enumItem declares an enum; each variant is a list of payload fields.
func (k *kit) enumItem(name string, generics int, variants ...[]fieldSpec) types.ItemID {
	d := &types.EnumDecl{}
	for i := 0; i < generics; i++ {
		d.Generics = append(d.Generics, types.GenericParam{Name: k.in.Intern(fmt.Sprintf("T%d", i))})
	}
	for i, v := range variants {
		vd := types.VariantDecl{Name: k.in.Intern(fmt.Sprintf("V%d", i)), Discr: uint64(i)}
		for _, f := range v {
			vd.Fields = append(vd.Fields, types.FieldDecl{Name: k.in.Intern(f.name), Type: f.t})
		}
		d.Variants = append(d.Variants, vd)
	}
	id := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern(name), Kind: types.KindEnum, Exported: true,
		Key: types.ItemKey("main", name), Decl: d})
	k.byNm[name] = id
	return id
}

// errorItem declares an error with no payload and returns its set of one.
func (k *kit) errorItem(name string) types.ErrorSetID {
	key := types.ItemKey("main", name)
	id := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern(name), Kind: types.KindError, Exported: true,
		Key: key, Decl: &types.ErrorDecl{Code: types.ErrorCode(key)}})
	k.byNm[name] = id
	return k.tab.ErrorSet([]types.ErrorMember{{Item: id}})
}

// named applies a declared struct or enum to type arguments.
func (k *kit) named(name string, args ...types.TypeID) types.TypeID {
	id, ok := k.byNm[name]
	if !ok {
		panic("kit: no item " + name)
	}
	as := make([]types.GenericArg, len(args))
	for i, a := range args {
		as[i] = types.TypeArg(a)
	}
	return k.tab.Named(id, as)
}

func (k *kit) param(name string, t types.TypeID) types.Param {
	return types.Param{Name: k.in.Intern(name), Type: t}
}

// fn declares a non-generic function in main.
func (k *kit) fn(name string, ps []types.Param, result types.TypeID, errs types.ErrorSetID) *types.Signature {
	return k.declare(name, types.ItemKey("main", name), types.NoReceiver, nil, ps, result, errs)
}

// genFn declares Name[T] with one type generic.
func (k *kit) genFn(name string, ps []types.Param, result types.TypeID) *types.Signature {
	g := []types.GenericParam{{Name: k.in.Intern("T")}}
	s := k.declare(name, types.ItemKey("main", name), types.NoReceiver, g, ps, result, types.EmptyErrors)
	s.OwnGenerics = 1
	return s
}

// method declares Owner.Name with a receiver of the given mode on type self.
func (k *kit) method(owner, name string, recv types.Receiver, self types.TypeID, ps []types.Param, result types.TypeID, errs types.ErrorSetID) *types.Signature {
	rt := self
	switch recv {
	case types.ByShared:
		rt = k.tab.Ref(self)
	case types.ByMut:
		rt = k.tab.Mut(self)
	}
	all := append([]types.Param{{Name: source.SymSelf, Type: rt}}, ps...)
	s := k.declare(owner+"."+name, types.MemberKey("main", owner, name), recv, nil, all, result, errs)
	it := k.items.Get(s.Item)
	it.Name = k.in.Intern(name)
	it.Owner = k.byNm[owner]
	// A method lives in its type's module, and resolve lists it in that
	// module's Items, which consumers scans.
	if m := k.items.Module(k.items.Get(it.Owner).Module); m != nil {
		m.Items = append(m.Items, s.Item)
	}
	return s
}

func (k *kit) declare(name string, key types.Key, recv types.Receiver, g []types.GenericParam, ps []types.Param, result types.TypeID, errs types.ErrorSetID) *types.Signature {
	sig := &types.Signature{Name: k.in.Intern(name), Receiver: recv, Generics: g, Params: ps, Result: result, Errors: errs}
	id := k.items.AddItem(&types.Item{Module: k.main.ID, Name: sig.Name, Kind: types.KindFunc, Key: key,
		Decl: &types.FuncDecl{Sig: *sig}})
	sig.Item = id
	k.items.Get(id).Decl.(*types.FuncDecl).Sig = *sig
	k.sigs[id] = sig
	k.byNm[name] = id
	return sig
}

// item finds a declared item by the name it was declared with.
func (k *kit) item(name string) types.ItemID {
	id, ok := k.byNm[name]
	if !ok {
		panic("kit: no item " + name)
	}
	return id
}

// facts computes the ownership facts of a type the way check's shape pass does
// for the fixture: Plain < Unique < Linear by the worst field; references and
// slices are carriers; a view is a unique carrier; Cell, Mutex, Chan, Arena
// and Scope are interior; Arena, Alloc and Heap are allocators; a type
// parameter is a linear carrier unless bounded by Plain.
func (k *kit) facts(t types.TypeID) types.Facts {
	if f, ok := k.memo[t]; ok {
		return f
	}
	k.memo[t] = types.Facts{Kind: types.Plain} // in progress
	f := k.computeFacts(t)
	k.memo[t] = f
	return f
}

func (k *kit) computeFacts(t types.TypeID) types.Facts {
	ty := k.tab.Get(t)
	plain := types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}
	switch ty.Kind {
	case types.KStr, types.KRef, types.KSlice:
		return types.Facts{Kind: types.Plain, Carrier: true, Send: true, Share: true, Freeze: true}
	case types.KMut, types.KMutSlice:
		return types.Facts{Kind: types.Unique, Carrier: true, Send: true, Share: true, Freeze: true}
	case types.KRawPtr:
		return types.Facts{Kind: types.Plain}
	case types.KFn:
		return types.Facts{Kind: types.Plain, IsFn: true, Send: true, Share: true, Freeze: true}
	case types.KParam:
		if k.plainParams[ty.Index] {
			return types.Facts{Kind: types.Plain, Carrier: true}
		}
		return types.Facts{Kind: types.Linear, Carrier: true}
	case types.KSelf:
		return types.Facts{Kind: types.Linear, Carrier: true}
	case types.KArray, types.KOptional:
		f := k.facts(ty.Elem)
		f.View, f.MutView, f.PlainView, f.Allocator, f.IsFn = false, false, false, false, false
		return f
	case types.KErrorUnion:
		if ty.Elem == types.Void {
			return plain
		}
		f := k.facts(ty.Elem)
		f.View, f.MutView, f.PlainView, f.Allocator, f.IsFn = false, false, false, false, false
		return f
	case types.KRecord:
		f := plain
		for _, fl := range ty.Fields {
			f = combine(f, k.facts(fl.Type))
		}
		return f
	case types.KNamed:
		return k.namedFacts(t, ty)
	}
	return plain
}

func combine(a, b types.Facts) types.Facts {
	a.Kind = max(a.Kind, b.Kind)
	a.Carrier = a.Carrier || b.Carrier
	a.Interior = a.Interior || b.Interior
	a.LoanInterior = a.LoanInterior || b.LoanInterior
	a.Send = a.Send && b.Send
	a.Share = a.Share && b.Share
	a.Freeze = a.Freeze && b.Freeze
	return a
}

func (k *kit) namedFacts(t types.TypeID, ty *types.Type) types.Facts {
	f := types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}
	for _, ft := range k.sub.inlineFields(t) {
		f = combine(f, k.facts(ft))
	}
	it := k.items.Get(ty.Item)
	if _, isIface := it.Decl.(*types.InterfaceDecl); isIface {
		return types.Facts{Kind: types.Linear, Carrier: true}
	}
	if sd, ok := it.Decl.(*types.StructDecl); ok {
		if sd.View {
			f.View, f.Carrier = true, true
			if !sd.PlainView {
				f.Kind = max(f.Kind, types.Unique)
			}
		}
		if sd.PlainView {
			f.PlainView = true
		} else if sd.MutView {
			f.MutView, f.Interior, f.LoanInterior, f.Freeze = true, true, true, false
		} else if sd.View {
			for _, a := range ty.Args {
				if !a.IsConst && k.facts(a.Type).LoanInterior {
					f.LoanInterior = true
				}
			}
		}
		if sd.Linear {
			f.Kind = types.Linear
		}
	}
	switch ty.Item {
	case k.known.Cell:
		f.Interior, f.Share, f.Freeze = true, false, false
		f.Kind = max(f.Kind, types.Unique)
		if len(ty.Args) > 0 && !ty.Args[0].IsConst && k.facts(ty.Args[0].Type).Carrier {
			f.LoanInterior = true
		}
	case k.known.Mutex, k.known.SpinCore, k.known.Chan:
		f.Interior, f.Freeze = true, false
		f.Kind = max(f.Kind, types.Unique)
		if len(ty.Args) > 0 && !ty.Args[0].IsConst && k.facts(ty.Args[0].Type).Carrier {
			f.LoanInterior = true
		}
	case k.known.Arena:
		f.MutView, f.Interior, f.LoanInterior, f.Allocator, f.Freeze = true, true, true, true, false
	case k.known.Alloc, k.known.Heap:
		f.Allocator = true
	case k.known.Scope:
		f.MutView, f.Interior, f.LoanInterior, f.Freeze = true, true, true, false
	case k.known.Io:
		f = types.Facts{Kind: types.Plain, Send: true, Share: true, Freeze: true}
	}
	return f
}

// env builds the Env for a body of sig.
func (k *kit) env(sig *types.Signature) *Env {
	return &Env{
		Types:  k.tab,
		Known:  k.known,
		Sig:    sig,
		Module: k.main,
		Callee: func(id types.ItemID) *types.Signature { return k.sigs[id] },
		Method: func(types.ItemID, uint32) *types.Signature { return nil },
		Facts:  k.facts,
	}
}

// mirEnv adapts the fixture to mir.Validate's Env.
func (k *kit) mirEnv(sig *types.Signature) *mir.Env {
	return &mir.Env{
		Types:  k.tab,
		Sig:    sig,
		Callee: func(id types.ItemID) *types.Signature { return k.sigs[id] },
		Facts:  func(t types.TypeID) (types.Facts, bool) { return k.facts(t), true },
		Known:  k.known,
	}
}

// mb builds one MIR body in the shapes lower emits. Every span
// it makes is unique: span n starts at byte 10n of file 1, so a diagnostic's
// anchor maps back to the SpanID that made it.
type mb struct {
	k   *kit
	b   *mir.Body
	sig *types.Signature
	cur mir.BlockID
}

const kitFile source.FileID = 1

// body starts a body for sig: local 0, a parameter per signature parameter
// and the entry block.
func (k *kit) body(sig *types.Signature) *mb {
	m := &mb{k: k, sig: sig}
	m.b = mir.NewBody(sig.Item, source.Span{File: kitFile, Start: 0, End: 1}, sig.ReturnType(k.tab))
	for _, p := range sig.Params {
		m.b.AddParam(p.Type, p.Name, m.sp())
	}
	m.cur = m.b.AddBlock(mir.OriginBody, mir.NoDefer)
	return m
}

// sp adds a fresh span.
func (m *mb) sp() mir.SpanID {
	n := uint32(len(m.b.Spans))
	return m.b.AddSpan(source.Span{File: kitFile, Start: 10 * n, End: 10*n + 5})
}

// spanOf maps a diagnostic anchor back to its SpanID.
func spanOf(s source.Span) mir.SpanID { return mir.SpanID(s.Start / 10) }

func (m *mb) user(name string, t types.TypeID, isVar bool) mir.LocalID {
	return m.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalUser, Name: m.k.in.Intern(name), Var: isVar, Loop: mir.NoLoop, Span: m.sp()})
}

func (m *mb) temp(t types.TypeID) mir.LocalID {
	return m.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop, Span: m.sp()})
}

func (m *mb) scopeLocal(t types.TypeID) mir.LocalID {
	return m.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalScope, Name: m.k.in.Intern("s"), Loop: mir.NoLoop, Span: m.sp()})
}

func (m *mb) block() mir.BlockID { return m.b.AddBlock(mir.OriginBody, mir.NoDefer) }

func (m *mb) at(b mir.BlockID) { m.cur = b }

// pl is a place; steps build its projections.
func (m *mb) pl(l mir.LocalID, steps ...mir.Projection) mir.Place { return m.b.Place(l, steps...) }

func fld(i uint32, t types.TypeID) mir.Projection {
	return mir.Projection{Kind: mir.ProjField, A: i, Type: t}
}
func deref(t types.TypeID) mir.Projection { return mir.Projection{Kind: mir.ProjDeref, Type: t} }
func index(l mir.LocalID, t types.TypeID) mir.Projection {
	return mir.Projection{Kind: mir.ProjIndex, A: uint32(l), Type: t}
}
func down(sel mir.VariantSel, a uint32, t types.TypeID) mir.Projection {
	return mir.Projection{Kind: mir.ProjDowncast, Sel: sel, A: a, Type: t}
}

func (m *mb) local(l mir.LocalID) mir.Place { return mir.LocalPlace(l) }

// Rvalue helpers.
func use(o mir.Operand) mir.Rvalue { return mir.Rvalue{Kind: mir.RvUse, A: o} }
func ref(p mir.Place) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: p, Loop: mir.NoLoop}
}
func mutRef(p mir.Place) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: p, Loop: mir.NoLoop}
}
func imm(t types.TypeID, v uint64) mir.Operand { return mir.Imm(t, v) }
func cp(p mir.Place) mir.Operand               { return mir.Copy(p) }
func mv(p mir.Place) mir.Operand               { return mir.Move(p) }

// assign appends `dst = rv` and returns its span.
func (m *mb) assign(dst mir.Place, rv mir.Rvalue) mir.SpanID {
	s := m.sp()
	blk := &m.b.Blocks[m.cur]
	blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StAssign, Place: dst, Rvalue: rv, Span: s})
	return s
}

// set is assign to a whole local.
func (m *mb) set(l mir.LocalID, rv mir.Rvalue) mir.SpanID { return m.assign(mir.LocalPlace(l), rv) }

// agg builds an aggregate rvalue.
func (m *mb) agg(kind mir.AggKind, item types.ItemID, t types.TypeID, ops ...mir.Operand) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvAggregate, Agg: kind, Item: item, Type: t, Ops: m.b.AddOperands(ops...)}
}

// end appends StScopeEnd of l.
func (m *mb) end(ls ...mir.LocalID) mir.SpanID {
	s := m.sp()
	blk := &m.b.Blocks[m.cur]
	for _, l := range ls {
		blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StScopeEnd, Local: l, Span: s})
	}
	return s
}

func (m *mb) term(t mir.Terminator) mir.SpanID {
	t.Span = m.sp()
	m.b.Blocks[m.cur].Term = t
	return t.Span
}

func (m *mb) jump(b mir.BlockID) mir.SpanID {
	return m.term(mir.Terminator{Kind: mir.TermGoto, Target: b})
}

func (m *mb) ret(exit mir.ExitKind) mir.SpanID {
	return m.term(mir.Terminator{Kind: mir.TermReturn, Exit: exit})
}

// branch is a switch on a bool operand: 1 goes to yes, else no.
func (m *mb) branch(on mir.Operand, yes, no mir.BlockID) mir.SpanID {
	return m.term(mir.Terminator{Kind: mir.TermSwitch, On: on, Cases: m.b.AddCases(mir.SwitchCase{Value: 1, Target: yes}), Default: no})
}

// call is a direct call of fn with generic arguments gargs, into dest, then
// next. The returned span is the call's.
func (m *mb) call(fn types.ItemID, gargs []types.TypeID, args []mir.Arg, dest mir.Place, next mir.BlockID) mir.SpanID {
	ga := make([]types.GenericArg, len(gargs))
	for i, g := range gargs {
		ga[i] = types.TypeArg(g)
	}
	return m.term(mir.Terminator{Kind: mir.TermCall,
		Callee: mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: fn, Args: m.b.AddGenericArgs(ga...)}},
		Args:   m.b.AddArgs(args...), Dest: dest, Target: next, Spawn: mir.NoLocal})
}

// callErr is call with an error edge into errBlock, binding the error in errLocal.
func (m *mb) callErr(fn types.ItemID, gargs []types.TypeID, args []mir.Arg, dest mir.Place, next mir.BlockID,
	kind mir.ErrorEdgeKind, errBlock mir.BlockID, errLocal mir.LocalID) mir.SpanID {
	s := m.call(fn, gargs, args, dest, next)
	t := &m.b.Blocks[m.cur].Term
	t.HasErr = true
	t.Err = mir.ErrorEdge{Kind: kind, Block: errBlock, Error: errLocal}
	return s
}

func argCopy(p mir.Place) mir.Arg     { return mir.Arg{Operand: mir.Copy(p), Mode: mir.ArgCopy} }
func argMove(p mir.Place) mir.Arg     { return mir.Arg{Operand: mir.Move(p), Mode: mir.ArgMove} }
func argReborrow(p mir.Place) mir.Arg { return mir.Arg{Operand: mir.Copy(p), Mode: mir.ArgReborrow} }

// validate checks the body with the full mir.Env.
func (m *mb) validate(t testing.TB) {
	t.Helper()
	if err := m.b.Validate(m.k.mirEnv(m.sig)); err != nil {
		t.Fatalf("Validate: %v\n%s", err, m.print())
	}
}

// check validates the body, runs Check and returns its result.
func (m *mb) check(t testing.TB) *Result {
	t.Helper()
	m.validate(t)
	return Check(m.b, m.k.env(m.sig))
}

// got renders a result's diagnostics as "E0401@12" (code at span ID), sorted.
func got(r *Result) []string {
	var out []string
	for _, d := range r.Diags {
		out = append(out, fmt.Sprintf("%v@%d", d.Code, spanOf(d.At)))
	}
	sort.Strings(out)
	return out
}

// want renders expected (code, span) pairs the same way.
func want(pairs ...any) []string {
	var out []string
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, fmt.Sprintf("%v@%d", pairs[i].(diag.Code), pairs[i+1].(mir.SpanID)))
	}
	sort.Strings(out)
	return out
}

// expect fails unless the result's diagnostics are exactly the pairs.
func (m *mb) expect(t *testing.T, r *Result, pairs ...any) {
	t.Helper()
	g, w := got(r), want(pairs...)
	if !slices.Equal(g, w) {
		var msgs []string
		for _, d := range r.Diags {
			msgs = append(msgs, fmt.Sprintf("  %v@%d %s", d.Code, spanOf(d.At), d.Message))
		}
		t.Fatalf("diagnostics: got %v, want %v\n%s\n%s", g, w, strings.Join(msgs, "\n"), m.print())
	}
}

// print renders the body for a failure message.
func (m *mb) print() string {
	p := &mir.Printer{Types: m.k.tab, Items: m.k.items}
	return p.Print(m.b)
}

// str is a const str operand (its bytes do not matter to borrow).
func (k *kit) str() mir.Operand {
	c := k.tab.Consts().Intern(types.ConstValue{Type: types.Str, Align: 8, Bytes: make([]byte, 16)})
	return mir.Const(types.Str, c)
}

// acquire appends `x = Acquire("a")` as a call into the local x and
// continues in a new block; it returns the call's span.
func (m *mb) acquire(x mir.LocalID) mir.SpanID {
	next := m.block()
	s := m.call(m.k.item("Acquire"), nil, []mir.Arg{{Operand: m.k.str(), Mode: mir.ArgCopy}}, m.local(x), next)
	m.at(next)
	return s
}

// callv appends a call of a void function into a fresh void temporary and
// continues in a new block; it returns the call's span.
func (m *mb) callv(fn string, gargs []types.TypeID, args ...mir.Arg) mir.SpanID {
	next := m.block()
	d := m.temp(types.Void)
	s := m.call(m.k.item(fn), gargs, args, m.local(d), next)
	m.at(next)
	return s
}

// close appends `x.Close()`, moving x.
func (m *mb) close(x mir.LocalID) mir.SpanID {
	return m.callv("Res.Close", nil, argMove(m.local(x)))
}

// use appends `Use(&x)` through a reference temporary, and ends the temporary.
func (m *mb) useRef(p mir.Place, t types.TypeID) mir.SpanID {
	r := m.temp(m.k.tab.Ref(t))
	m.set(r, ref(p))
	s := m.callv("Use", []types.TypeID{t}, argCopy(m.local(r)))
	m.end(r)
	return s
}

// sig is a signature for a test body that is not a declared item.
func (k *kit) sig(name string, ps []types.Param, result types.TypeID, errs types.ErrorSetID) *types.Signature {
	return k.fn(name, ps, result, errs)
}
