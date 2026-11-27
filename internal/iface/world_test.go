// Why: SessionWorld answers every read a cached result depends on, and a
// wrong answer reuses a stale result.
// What: round trips of diagnostics and read sets, and one accepting and one
// rejecting case per read kind over SessionWorld.
// How: worldFixture builds geo with a reader module "app" and an outside module
// "other"; each test changes one thing and checks which reads go invalid.

package iface

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

const diagText = "fn F(a: int): int {\n  b := a / 0\n  return b\n}\n"
const diagShifted = "// header\n\nfn F(a: int): int {\n  // why\n  b := a / 0\n  return b\n}\n"

func fixtureDiags(f *source.File) []diag.Diagnostic {
	at := func(sub string) source.Span {
		i := strings.Index(diagText, sub)
		return source.NewSpan(f.ID, uint32(i), uint32(i+len(sub)))
	}
	d1 := diag.New(diag.E0501, at("a / 0"), "cannot divide").
		Text("name", "b").Int("limit", -64).Fact("ok", diag.BoolFact(true)).Loc("declared_at", at("b :=")).
		Fact("code", diag.CodeFact(diag.E0402)).Fact("loan", diag.LoanFact("a.x", diag.LoanExclusive, at("a"))).
		Fact("list", diag.ListFact(diag.TextFact("x"), diag.IntFact(3), diag.ListFact(diag.AtFact(at("return"))))).
		Fact("obj", diag.ObjectFact(diag.Field{Key: "k", Value: diag.TextFact("v")}, diag.Field{Key: "n", Value: diag.IntFact(1)})).
		Alt("use a guard").Alt("divide by one").
		Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{{Span: at("0"), Text: "1"}}, Why: "fixes are never stored"})
	d2 := diag.New(diag.E0402, at("return b"), "consequence").In(diag.PhaseFlow).ConsequenceOf(diag.E0501, at("a / 0"))
	d3 := diag.New(diag.E0201, source.Span{}, "no location")
	return []diag.Diagnostic{*d1, *d2, *d3}
}

func diagTrip(s *session, env *Env, ds []diag.Diagnostic) ([]diag.Diagnostic, error) {
	var got []diag.Diagnostic
	_, err := storeTrip(s, env, func(w *walker) { w.writeDiags(ds) }, func(r *reader, d *Decoder) { got = r.readDiags(d) })
	return got, err
}

func reportJSON(ds []diag.Diagnostic, sm *source.SourceMap) []byte {
	ps := make([]*diag.Diagnostic, len(ds))
	for i := range ds {
		d := ds[i]
		d.Fixes = nil
		ps[i] = &d
	}
	return diag.Order(ps, sm, diag.NoCap).WriteJSON(sm)
}

func TestRoundTripDiagnostics(t *testing.T) {
	a := newGeo(false)
	f := a.addFile("geo/f.mk", diagText, "geo.F")
	ds := fixtureDiags(f)
	got, err := diagTrip(a, a.env, ds)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]diag.Diagnostic, len(ds))
	copy(want, ds)
	for i := range want {
		want[i].Fixes = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("same-session diagnostics:\n got %+v\nwant %+v", got, want)
	}
	if !bytes.Equal(reportJSON(got, a.sm), reportJSON(ds, a.sm)) {
		t.Errorf("JSON differs:\n%s\n%s", reportJSON(got, a.sm), reportJSON(ds, a.sm))
	}
	// Shifted by comments above and inside: same tokens.
	b := newGeo(true)
	b.addFile("geo/f.mk", diagShifted, "geo.F")
	got, err = diagTrip(a, b.env, ds)
	if err != nil {
		t.Fatal(err)
	}
	if b.sm.Text(got[0].At) != "a / 0" || b.sm.Text(got[1].At) != "return b" {
		t.Errorf("rebased onto %q and %q", b.sm.Text(got[0].At), b.sm.Text(got[1].At))
	}
	loan, _ := got[0].FactValue("loan")
	if b.sm.Text(loan.At) != "a" || b.sm.Text(got[1].Cause.At) != "a / 0" {
		t.Error("fact and cause spans not rebased")
	}
	if !Storable([]*diag.Diagnostic{&ds[0]}) || Storable([]*diag.Diagnostic{diag.New(diag.E0000, source.Span{}, "ice")}) {
		t.Error("Storable")
	}
	if !catchICE(func() { diagTrip(a, a.env, []diag.Diagnostic{*diag.Toolchain("clang", "clang", "", "x")}) }) {
		t.Error("storing E0001 did not raise an ICE")
	}
}

func TestRoundTripReadSets(t *testing.T) {
	var rs []types.Read
	for k := types.ReadSignature; k.String() != "read?"; k++ {
		rs = append(rs, types.Read{Kind: k, Key: types.KeyFromString("m.X"), Name: k.String(), Hash: source.HashString("t", k.String())})
	}
	rs = append(rs, rs[0]) // a duplicate is dropped
	enc := EncodeReads(rs)
	got, err := DecodeReads(enc)
	if err != nil {
		t.Fatal(err)
	}
	var set types.ReadSet
	for _, r := range rs {
		set.Add(r)
	}
	if !reflect.DeepEqual(got, set.Sorted()) {
		t.Errorf("reads: got %v", got)
	}
	if len(got) != 14 {
		t.Errorf("%d reads, want one per kind (14, ReadData included)", len(got))
	}
	gs := readSetOf(got)
	if gs.Hash() != set.Hash() {
		t.Error("ReadSet.Hash differs after a round trip")
	}
	// Reversed input encodes the same.
	rev := make([]types.Read, len(rs))
	for i := range rs {
		rev[i] = rs[len(rs)-1-i]
	}
	if !bytes.Equal(EncodeReads(rev), enc) {
		t.Error("read encoding depends on input order")
	}
	bad := bytes.Clone(enc)
	bad[1] = 99
	if _, err := DecodeReads(bad); err == nil {
		t.Error("an unknown read kind decoded")
	}
}

func TestWriteCanonicalReads(t *testing.T) {
	a := types.Read{Kind: types.ReadValue, Key: types.KeyFromString("m.X")}
	b := a
	b.Hash[31] = 1
	for _, reads := range [][]types.Read{nil, {a}, {a, b}} {
		var e Encoder
		writeReads(&e, reads)
		got, err := DecodeReads(e.Out())
		if err != nil || !slices.Equal(got, reads) {
			t.Fatalf("round trip of %v: got %v, %v", reads, got, err)
		}
		// With room for the output, an already canonical list needs no scratch space.
		if n := testing.AllocsPerRun(10, func() {
			e.buf = e.buf[:0]
			writeReads(&e, reads)
		}); n != 0 {
			t.Errorf("writing %d canonical reads allocated %g times", len(reads), n)
		}
	}
	mixed := []types.Read{b, a, b}
	original := slices.Clone(mixed)
	if !bytes.Equal(EncodeReads(mixed), EncodeReads([]types.Read{a, b})) || !slices.Equal(mixed, original) {
		t.Error("normalizing reads changed the encoding or the caller's input")
	}
}

func TestEncodeReadsRejectsInvalidKind(t *testing.T) {
	for _, k := range []types.ReadKind{0, 255} {
		if !catchICE(func() { EncodeReads([]types.Read{{Kind: k}}) }) {
			t.Errorf("encoding read kind %d did not raise an ICE", k)
		}
	}
}

// worldFixture is the geo session with a reader module "app" whose closure
// is {geo}, a module "other" outside it, impls, a const and bodies.
type worldFixture struct {
	*session
	app, other types.ModuleID
	impls      map[string]types.ItemID
	layouts    map[string]*types.Layout
	pkg, files map[string]source.Hash
	// closure: modules admitted to app's closure besides geo.
	closure map[string]bool
}

func newWorldFixture() *worldFixture {
	s := withDecls(newGeo(false))
	f := &worldFixture{session: s, impls: map[string]types.ItemID{}, layouts: map[string]*types.Layout{},
		pkg: map[string]source.Hash{}, files: map[string]source.Hash{}}
	f.app = s.items.AddModule(&types.Module{Path: "app"})
	f.other = s.items.AddModule(&types.Module{Path: "other"})
	k := s.item(s.mod, types.KeyFromString("geo.K"), "K", types.KindConst, true)
	s.items.Get(k).Decl = &types.ConstDecl{Type: types.Uint, Value: s.uintConst(7), HasValue: true}
	s.bodies[s.id("geo.Add")] = retBody(s, s.id("geo.Add"), types.Void)
	return f
}

func (f *worldFixture) world(reader string, target types.Target) *SessionWorld {
	return &SessionWorld{H: f.h, Env: f.env, Target: target, Reader: reader,
		Body: func(id types.ItemID) *mir.Body { return f.bodies[id] },
		Layout: func(k types.Key, args string) (*types.Layout, bool) {
			l, ok := f.layouts[k.String()+args]
			return l, ok
		},
		Impl: func(i types.Key, tgt string) (types.ItemID, bool) {
			id, ok := f.impls[i.String()+" for "+tgt]
			return id, ok
		},
		Pkg:     func(n string) (source.Hash, bool) { h, ok := f.pkg[n]; return h, ok },
		File:    func(p string) (source.Hash, bool) { h, ok := f.files[p]; return h, ok },
		Closure: func(m string) bool { return reader == "app" && (m == "geo" || f.closure[m]) },
	}
}

// record is a read as a producer records it now.
func (f *worldFixture) record(t testing.TB, kind types.ReadKind, key, name string) types.Read {
	t.Helper()
	r := types.Read{Kind: kind, Key: types.KeyFromString(key), Name: name}
	h, _ := f.h.ItemPart(f.world("app", types.X86_64Windows), kind, r.Key, name)
	r.Hash = h
	return r
}

// check asserts Valid's verdict for one read with a fresh world.
func (f *worldFixture) check(t *testing.T, what string, r types.Read, want bool) {
	t.Helper()
	f.checkOn(t, what, r, want, types.X86_64Windows)
}

func (f *worldFixture) checkOn(t *testing.T, what string, r types.Read, want bool, target types.Target) {
	t.Helper()
	ok, failed := Valid(f.world("app", target), []types.Read{r})
	if ok != want {
		t.Errorf("%s: Valid = %v, want %v", what, ok, want)
	}
	if !ok && failed != r {
		t.Errorf("%s: failed read %v, want %v", what, failed, r)
	}
}

func TestReadSignature(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadSignature, "geo.Add", "")
	f.check(t, "unchanged", r, true)
	f.bodies[f.id("geo.Add")].Blocks[0].Term.Exit = mir.ExitFail
	f.check(t, "body edit", r, true)
	f.items.Get(f.id("geo.Add")).Decl.(*types.FuncDecl).Sig.Params[0].Type = types.I64
	f.check(t, "parameter type changed", r, false)
}

func TestReadNoItemAppears(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadNoItem, "geo", "Sub")
	f.check(t, "still absent", r, true)
	f.item(f.mod, types.KeyFromString("geo.Mul"), "Mul", types.KindFunc, true)
	f.check(t, "unrelated item added", r, true)
	f.item(f.mod, types.KeyFromString("geo.Sub"), "Sub", types.KindFunc, false)
	f.check(t, "the item appears", r, false)
}

func TestReadNoItemFoldedNeighbour(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadNoItem, "geo", "ad_d")
	if r.Hash.IsZero() {
		t.Fatal("ad_d has Add as a folded neighbour; the hash should not be zero")
	}
	f.check(t, "unchanged", r, true)
	f.item(f.mod, types.KeyFromString("geo.ADD"), "ADD", types.KindFunc, true)
	f.check(t, "a new near miss", r, false)
}

func (f *worldFixture) addImpl(mod types.ModuleID, iface, target string) types.ItemID {
	key := types.ImplKey(f.items.Module(mod).Path, iface, target)
	id := f.item(mod, key, "", types.KindImpl, false)
	m := f.item(mod, types.ImplMemberKey(key, "Eq"), "Eq", types.KindFunc, true)
	f.items.Get(m).Owner = id
	f.items.Get(m).Decl = &types.FuncDecl{Sig: types.Signature{Item: m, Name: f.in.Intern("Eq"), Result: types.Bool}}
	f.items.Get(id).Decl = &types.ImplDecl{Bound: types.Bound{Kind: types.BoundInterface, Item: f.id("geo.Wrap")},
		For: f.tab.Named(f.id(target), nil), Methods: []types.ItemID{m}}
	f.impls[iface+" for "+target] = id
	return id
}

func TestReadNoImplAppears(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadNoImpl, "geo.Wrap", "geo.Point")
	f.check(t, "still absent", r, true)
	f.addImpl(f.mod, "geo.Wrap", "geo.Shape")
	f.check(t, "an impl for another type", r, true)
	f.addImpl(f.app, "geo.Wrap", "geo.Point")
	f.check(t, "the impl appears", r, false)
}

func TestReadImplChanged(t *testing.T) {
	f := newWorldFixture()
	id := f.addImpl(f.mod, "geo.Wrap", "geo.Point")
	r := f.record(t, types.ReadImpl, "geo.Wrap", "geo.Point")
	f.check(t, "unchanged", r, true)
	m := f.items.Get(id).Decl.(*types.ImplDecl).Methods[0]
	f.items.Get(m).Decl.(*types.FuncDecl).Sig.Result = types.I32
	f.check(t, "a method signature changed", r, false)
	delete(f.impls, "geo.Wrap for geo.Point")
	f.check(t, "the impl is gone", r, false)
}

func TestReadFieldsExportedOnly(t *testing.T) {
	f := newWorldFixture()
	d := f.items.Get(f.id("geo.Point")).Decl.(*types.StructDecl)
	d.Fields = append(d.Fields, types.FieldDecl{Name: f.in.Intern("hidden"), Type: types.U8})
	r := f.record(t, types.ReadFields, "geo.Point", "")
	all := f.record(t, types.ReadFields, "geo.Point", "all")
	d.Fields[2].Type = types.U16
	f.check(t, "private field type changed, foreign view", r, true)
	f.check(t, "private field type changed, same-module view", all, false)
	d.Fields[0].Type = types.F32
	f.check(t, "exported field type changed", r, false)
}

func TestReadFieldsPrivateBit(t *testing.T) {
	f := newWorldFixture()
	d := f.items.Get(f.id("geo.Point")).Decl.(*types.StructDecl)
	r := f.record(t, types.ReadFields, "geo.Point", "")
	d.Fields = append(d.Fields, types.FieldDecl{Name: f.in.Intern("c"), Type: types.U8})
	f.check(t, "the first private field added", r, false)
	r = f.record(t, types.ReadFields, "geo.Point", "")
	d.Fields[2].Name = f.in.Intern("renamed")
	f.check(t, "a private field renamed", r, true)
}

func TestReadMembers(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadExports, "geo.Point:", "")
	f.addImpl(f.other, "geo.Wrap", "geo.Point")
	f.check(t, "a method added outside the reader's closure", r, true)
	f.addImpl(f.app, "geo.Wrap", "geo.Point")
	f.check(t, "a method added in the reader's own module", r, false)
	g := newWorldFixture()
	r = g.record(t, types.ReadExports, "geo.Point:", "")
	m := g.item(g.mod, types.MemberKey("geo", "Point", "Norm"), "Norm", types.KindFunc, true)
	g.items.Get(m).Owner = g.id("geo.Point")
	g.check(t, "a Type.Name method added in the type's module", r, false)
	// A member miss records ReadNoItem with the type's namespace key.
	h := newWorldFixture()
	miss := h.record(t, types.ReadNoItem, TypeNamespaceKey(types.KeyFromString("geo.Point")).String(), "z")
	h.check(t, "member still absent", miss, true)
	h.items.Get(h.id("geo.Point")).Decl.(*types.StructDecl).Fields = append(
		h.items.Get(h.id("geo.Point")).Decl.(*types.StructDecl).Fields, types.FieldDecl{Name: h.in.Intern("z"), Type: types.F64, Exported: true})
	h.check(t, "member appears", miss, false)
}

// TestReadMembersRefImpl: an impl for &T or mut T is in T's member list
// (refImplsOf), as borrow's view scan reads it as T's (typewalk.go
// implsFor), so adding one in the reader's closure invalidates a ReadExports
// of T's namespace. Without it a warm cache kept a body that a cold check
// refuses. It is not in the shape part: an unsafe impl Send for &T asserts
// nothing about T.
func TestReadMembersRefImpl(t *testing.T) {
	for _, mut := range []bool{false, true} {
		f := newWorldFixture()
		r := f.record(t, types.ReadExports, "geo.Point:", "")
		shape := f.record(t, types.ReadShape, "geo.Wrap", "")
		id := f.addImpl(f.other, "geo.Wrap", "geo.Point")
		ref := func(x types.TypeID) types.TypeID {
			if mut {
				return f.tab.Mut(x)
			}
			return f.tab.Ref(x)
		}
		f.items.Get(id).Decl.(*types.ImplDecl).For = ref(f.tab.Named(f.id("geo.Point"), nil))
		f.check(t, "an impl for a reference added outside the reader's closure", r, true)
		id = f.addImpl(f.app, "geo.Wrap", "geo.Point")
		f.items.Get(id).Decl.(*types.ImplDecl).For = ref(f.tab.Named(f.id("geo.Point"), nil))
		f.check(t, "an impl for a reference added in the reader's own module", r, false)
		s := f.addImpl(f.mod, "geo.Wrap", "geo.Wrap")
		d := f.items.Get(s).Decl.(*types.ImplDecl)
		d.Unsafe, d.Bound = true, types.Bound{Kind: types.BoundSend}
		d.For = ref(f.tab.Named(f.id("geo.Wrap"), nil))
		f.check(t, "an unsafe impl Send for a reference: the referent's shape part", shape, true)
	}
}

// TestReadShapeMutView: toggling MutView or LoanInterior on a struct's shape
// invalidates a recorded ReadShape, and toggling MutView on a StructDecl
// invalidates a recorded ReadSignature (Decl hash).
func TestReadShapeMutView(t *testing.T) {
	f := newWorldFixture()
	point := f.id("geo.Point")
	d := f.items.Get(point).Decl.(*types.StructDecl)
	d.View = true
	rec := func(kind types.ReadKind) types.Read {
		r := types.Read{Kind: kind, Key: types.KeyFromString("geo.Point")}
		h, ok := f.world("app", types.X86_64Windows).Part(r)
		if !ok {
			t.Fatalf("no part for %v", r)
		}
		r.Hash = h
		return r
	}
	valid := func(r types.Read) bool {
		ok, _ := Valid(f.world("app", types.X86_64Windows), []types.Read{r})
		return ok
	}
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 16, Align: 8, View: true}}
	rDecl := rec(types.ReadSignature)
	rShape := rec(types.ReadShape)
	d.MutView = true
	if valid(rDecl) {
		t.Error("toggling StructDecl.MutView left ReadSignature valid")
	}
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 16, Align: 8, View: true, MutView: true, LoanInterior: true}}
	if valid(rShape) {
		t.Error("toggling Shape.MutView/LoanInterior left ReadShape valid")
	}
}

// TestReadShapePlainView: toggling PlainView on a struct's shape or term
// invalidates a recorded ReadShape, and toggling PlainView on a StructDecl
// invalidates a recorded ReadSignature (Decl hash).
func TestReadShapePlainView(t *testing.T) {
	f := newWorldFixture()
	point := f.id("geo.Point")
	d := f.items.Get(point).Decl.(*types.StructDecl)
	d.View = true
	rec := func(key string, kind types.ReadKind) types.Read {
		r := types.Read{Kind: kind, Key: types.KeyFromString(key)}
		h, ok := f.world("app", types.X86_64Windows).Part(r)
		if !ok {
			t.Fatalf("no part for %v", r)
		}
		r.Hash = h
		return r
	}
	valid := func(r types.Read) bool {
		ok, _ := Valid(f.world("app", types.X86_64Windows), []types.Read{r})
		return ok
	}
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 4, Align: 4, Kind: types.Plain, Carrier: true, View: true, Freeze: true}}
	rDecl := rec("geo.Point", types.ReadSignature)
	rShape := rec("geo.Point", types.ReadShape)
	rTerm := rec("geo.Wrap", types.ReadShape)
	d.PlainView = true
	if valid(rDecl) {
		t.Error("toggling StructDecl.PlainView left ReadSignature valid")
	}
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 4, Align: 4, Kind: types.Plain, Carrier: true, View: true, PlainView: true, Freeze: true}}
	if valid(rShape) {
		t.Error("toggling Shape.PlainView left ReadShape valid")
	}
	f.items.Get(f.id("geo.Wrap")).Decl.(*types.StructDecl).Shape.PlainView = true
	if valid(rTerm) {
		t.Error("toggling ShapeTerm.PlainView left ReadShape valid")
	}
}

func TestReadShape(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadShape, "geo.Wrap", "")
	f.check(t, "unchanged", r, true)
	f.items.Get(f.id("geo.Wrap")).Decl.(*types.StructDecl).Shape.Fields[0] = types.ShapeTerm{Kind: types.TermParam, Param: 1}
	f.check(t, "term changed", r, false)
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 16, Align: 8}}
	p := f.record(t, types.ReadShape, "geo.Point", "")
	f.check(t, "non-generic shape from the layout", p, true)
	f.layouts["geo.Point"] = &types.Layout{Shape: types.Shape{Size: 24, Align: 8}}
	f.check(t, "non-generic shape changed", p, false)
}

// TestShapePartCoversUnsafeImpls: a type's shape part holds what its unsafe
// impls Send and Share assert, which its term cannot show. An item with none
// keeps the plain term hash; adding one, changing its bounds, or removing it
// changes the part; an ordinary impl for the type changes nothing.
func TestShapePartCoversUnsafeImpls(t *testing.T) {
	f := newWorldFixture()
	wrap := f.id("geo.Wrap")
	plain := f.record(t, types.ReadShape, "geo.Wrap", "")
	if want := f.h.ShapeTerm(f.items.Get(wrap).Decl.(*types.StructDecl).Shape); plain.Hash != want {
		t.Fatalf("no unsafe impl: part %v, want the term hash %v", plain.Hash, want)
	}
	f.addImpl(f.mod, "geo.Wrap", "geo.Wrap")
	f.check(t, "an ordinary impl for the type", plain, true)
	key := types.ImplKey("geo", "Send", "geo.Wrap")
	id := f.item(f.mod, key, "", types.KindImpl, false)
	gens := []types.GenericParam{{Name: f.in.Intern("T"), Bounds: []types.Bound{{Kind: types.BoundSend}}}}
	f.items.Get(id).Decl = &types.ImplDecl{Generics: gens, Bound: types.Bound{Kind: types.BoundSend},
		For: f.tab.Named(wrap, []types.GenericArg{types.TypeArg(f.tab.Param(0))}), Unsafe: true}
	f.check(t, "unsafe impl Send added", plain, false)
	sent := f.record(t, types.ReadShape, "geo.Wrap", "")
	f.check(t, "unchanged", sent, true)
	f.items.Get(id).Decl.(*types.ImplDecl).Generics[0].Bounds = []types.Bound{{Kind: types.BoundShare}}
	f.check(t, "the impl's bound changed", sent, false)
	f.items.Get(id).Decl.(*types.ImplDecl).Generics[0].Bounds = []types.Bound{{Kind: types.BoundSend}}
	f.check(t, "the bound restored", sent, true)
	f.items.Get(id).Decl.(*types.ImplDecl).Bound = types.Bound{Kind: types.BoundShare}
	f.check(t, "Send became Share", sent, false)
}

func TestReadLayout(t *testing.T) {
	f := newWorldFixture()
	args := []types.GenericArg{types.TypeArg(types.I32)}
	name := f.h.LayoutName(args)
	if name != "["+f.h.GenericArgs(args).String()+"]" || f.h.LayoutName(nil) != "" {
		t.Errorf("LayoutName = %q", name)
	}
	f.layouts["geo.Wrap"+name] = &types.Layout{Shape: types.Shape{Size: 16, Align: 8},
		Fields: []types.FieldLayout{{Offset: 0, Type: types.I32}, {Offset: 8, Type: f.tab.Ref(types.I32)}}}
	r := f.record(t, types.ReadLayout, "geo.Wrap", name)
	f.check(t, "unchanged", r, true)
	f.layouts["geo.Wrap"+name].Fields[1].Offset = 4
	f.check(t, "offset changed", r, false)
}

func TestReadValueComparedByValue(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadValue, "geo.K", "")
	d := f.items.Get(f.id("geo.K")).Decl.(*types.ConstDecl)
	d.Value = f.uintConst(7) // a re-evaluation that yields the same value
	f.check(t, "same value", r, true)
	d.Value = f.uintConst(8)
	f.check(t, "value changed", r, false)
}

func TestReadBody(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadBody, "geo.Add", "")
	f.check(t, "unchanged", r, true)
	f.bodies[f.id("geo.Add")].Blocks[0].Term.Exit = mir.ExitFail
	f.check(t, "body edited", r, false)
	delete(f.bodies, f.id("geo.Add"))
	f.check(t, "body not available", r, false)
}

func TestReadTarget(t *testing.T) {
	f := newWorldFixture()
	pb := f.record(t, types.ReadTarget, "", "pointer_bytes")
	abi := f.record(t, types.ReadTarget, "", "abi")
	os := f.record(t, types.ReadTarget, "", "os")
	f.checkOn(t, "pointer_bytes on Linux", pb, true, types.X86_64Linux)
	f.checkOn(t, "abi on Linux", abi, false, types.X86_64Linux)
	f.checkOn(t, "os on Linux", os, false, types.X86_64Linux)
	f.checkOn(t, "abi on Windows", abi, true, types.X86_64Windows)
	if _, ok := TargetReadHash(types.X86_64Linux, "endian"); ok {
		t.Error("an unknown target field hashed")
	}
}

func TestReadPkg(t *testing.T) {
	f := newWorldFixture()
	f.pkg["unsafe_modules"] = source.HashString("t", "[]")
	r := f.record(t, types.ReadPkg, "", "unsafe_modules")
	f.check(t, "unchanged", r, true)
	f.pkg["version"] = source.HashString("t", "2")
	f.check(t, "another field changed", r, true)
	f.pkg["unsafe_modules"] = source.HashString("t", "[geo]")
	f.check(t, "the field changed", r, false)
}

func TestReadFileAbsent(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadFile, "", "data/table.txt")
	if !r.Hash.IsZero() {
		t.Fatal("an absent file recorded a hash")
	}
	f.check(t, "still absent", r, true)
	f.files["data/table.txt"] = source.HashString("t", "x")
	f.check(t, "the file appears", r, false)
	p := f.record(t, types.ReadFile, "", "data/table.txt")
	f.check(t, "present and unchanged", p, true)
	f.files["data/table.txt"] = source.HashString("t", "y")
	f.check(t, "content changed", p, false)
}

func TestReadExports(t *testing.T) {
	f := newWorldFixture()
	r := f.record(t, types.ReadExports, "geo", "")
	f.bodies[f.id("geo.Add")].Blocks[0].Term.Exit = mir.ExitFail
	f.check(t, "a body change", r, true)
	f.item(f.mod, types.KeyFromString("geo.Sub"), "Sub", types.KindFunc, true)
	f.check(t, "an item added", r, false)
	g := newWorldFixture()
	r = g.record(t, types.ReadExports, "geo", "")
	g.items.Get(g.id("geo.Add")).Exported = false
	g.check(t, "an item made private", r, false)
	if ok, _ := Valid(g.world("app", types.X86_64Windows), []types.Read{{Kind: types.ReadExports, Key: types.KeyFromString("nowhere")}}); ok {
		t.Error("exports of a missing module held")
	}
}
