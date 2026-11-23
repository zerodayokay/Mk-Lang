// Why: kinds, carriers, Send, Share and Freeze are facts every later rule
// reads, and a wrong one breaks safety silently.
// What: TestKinds, TestSendShare, TestFreeze*, TestFactsMemoConcurrent,
// TestFacts_ParamNotSharedAcrossGenerics, TestNotFinalIsICE.
// How: builds types by hand (hand) and reads their facts, also concurrently.

package check

import (
	"fmt"
	"sync"
	"testing"

	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

// hand is a checker with one hand-built module whose declarations are
// written directly, standing in for std where the rules key on Known items.
type hand struct {
	t     *testing.T
	in    *source.Interner
	items *types.Items
	tab   *types.Table
	c     *Checker
	mod   types.ModuleID
	m     *Module
	n     int
}

func newHand(t *testing.T) *hand {
	h := &hand{t: t, in: source.NewInterner(), items: types.NewItems()}
	consts := types.NewConstTable()
	h.tab = types.NewTable(h.in, h.items, consts)
	h.c = New(&Config{Interner: h.in, Types: h.tab, Items: h.items, Consts: consts, Target: types.X86_64Linux})
	h.mod = h.items.AddModule(&types.Module{Path: "h", Std: true})
	h.m = &Module{ID: h.mod, c: h.c, final: true, asserts: map[types.ItemID]assertPair{}, sliceImpls: map[types.ItemID]bool{},
		info: &resolve.Info{Module: h.mod}}
	h.c.addModule(h.mod, h.m)
	k := &h.c.known
	k.Cell = h.strct("Cell", 1, "", h.tab.Param(0))
	k.Mutex = h.strct("Mutex", 1, "", types.U32, h.tab.Param(0))
	k.Chan = h.strct("Chan", 1, "", h.tab.RawPtr(h.tab.Param(0)))
	k.SpinCore = h.strct("SpinCore", 1, "", types.U32, h.tab.Param(0))
	k.Arena = h.strct("Arena", 0, "linear mut view", h.tab.RawPtr(types.U8), types.Int)
	k.Io = h.strct("Io", 0, "", h.tab.RawPtr(types.U8))
	k.Heap = h.strct("Heap", 0, "", h.tab.RawPtr(types.U8))
	k.Alloc = h.strct("Alloc", 0, "", types.Int)
	k.System = h.strct("System", 0, "")
	k.Scope = h.strct("Scope", 1, "", types.Int)
	k.Atomic = h.strct("Atomic", 0, "", types.Int)
	return h
}

// item adds an item of this module.
func (h *hand) item(name string, kind types.ItemKind, d types.Decl) types.ItemID {
	h.n++
	id := h.items.AddItem(&types.Item{Module: h.mod, Name: h.in.Intern(name), Kind: kind,
		Key: types.ItemKey("h", name), Decl: d, Span: source.Span{Start: uint32(h.n)}})
	h.m.info.Items = append(h.m.info.Items, &resolve.ItemInfo{Item: id})
	return id
}

// strct adds a struct with gens type parameters; kw holds "linear", "view", "mut view".
func (h *hand) strct(name string, gens int, kw string, fields ...types.TypeID) types.ItemID {
	mv := contains(kw, "mut view")
	d := &types.StructDecl{Linear: contains(kw, "linear"), View: contains(kw, "view") || mv, MutView: mv}
	for i := 0; i < gens; i++ {
		d.Generics = append(d.Generics, types.GenericParam{Name: h.in.Intern(fmt.Sprintf("T%d", i))})
	}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: h.in.Intern(fmt.Sprintf("f%d", i)), Type: f})
	}
	return h.item(name, types.KindStruct, d)
}

func contains(s, w string) bool {
	for i := 0; i+len(w) <= len(s); i++ {
		if s[i:i+len(w)] == w {
			return true
		}
	}
	return false
}

// enum adds a non-generic enum with one variant per payload list.
func (h *hand) enum(name string, variants ...[]types.TypeID) types.ItemID {
	d := &types.EnumDecl{}
	for i, v := range variants {
		vd := types.VariantDecl{Name: h.in.Intern(fmt.Sprintf("V%d", i)), Discr: uint64(i)}
		for j, f := range v {
			vd.Fields = append(vd.Fields, types.FieldDecl{Name: h.in.Intern(fmt.Sprintf("f%d", j)), Type: f})
		}
		d.Variants = append(d.Variants, vd)
	}
	return h.item(name, types.KindEnum, d)
}

func (h *hand) named(it types.ItemID, args ...types.TypeID) types.TypeID {
	var as []types.GenericArg
	for _, a := range args {
		as = append(as, types.TypeArg(a))
	}
	return h.tab.Named(it, as)
}

// assert registers an unsafe impl of a built-in bound for a constructor;
// bounds are those of the impl's one parameter (T in impl[T: Send]).
func (h *hand) assert(ctor types.ItemID, bound types.BoundKind, gens int, bounds ...types.BoundKind) types.ItemID {
	d := &types.ImplDecl{Bound: types.Bound{Kind: bound}, Unsafe: true}
	var args []types.TypeID
	for i := 0; i < gens; i++ {
		g := types.GenericParam{Name: h.in.Intern(fmt.Sprintf("T%d", i))}
		for _, b := range bounds {
			g.Bounds = append(g.Bounds, types.Bound{Kind: b})
		}
		d.Generics = append(d.Generics, g)
		args = append(args, h.tab.Param(uint32(i)))
	}
	d.For = h.named(ctor, args...)
	h.n++
	impl := h.items.AddItem(&types.Item{Module: h.mod, Kind: types.KindImpl, Key: types.ImplKey("h", fmt.Sprint(bound), fmt.Sprint(ctor, h.n)), Decl: d})
	p := h.m.asserts[ctor]
	if bound == types.BoundSend {
		p.send = impl
	} else {
		p.share = impl
	}
	h.m.asserts[ctor] = p
	return impl
}

func (h *hand) str(t types.TypeID) string { return h.tab.TypeString(t, nil) }

func TestKinds(t *testing.T) {
	h := newHand(t)
	tab, k := h.tab, &h.c.known
	lin := h.strct("Stream", 0, "linear", types.Int)
	words := h.strct("Words", 0, "view", types.Str, types.Int)
	en := h.enum("E", nil, []types.TypeID{tab.Mut(types.Int)})
	errs := tab.ErrorSet(nil)
	_ = errs
	rows := []struct {
		t       types.TypeID
		kind    types.Kind
		carrier bool
	}{
		{types.Int, types.Plain, false},
		{types.Bool, types.Plain, false},
		{types.Void, types.Plain, false},
		{tab.Ref(types.Int), types.Plain, true},
		{tab.Mut(types.Int), types.Unique, true},
		{tab.Slice(types.Int), types.Plain, true},
		{tab.MutSlice(types.Int), types.Unique, true},
		{types.Str, types.Plain, true},
		{tab.RawPtr(types.Int), types.Plain, false},
		{tab.Fn(types.FnType{Result: types.Int}), types.Plain, false},
		{tab.Array(h.c.intArg(4), tab.Mut(types.Int)), types.Unique, true},
		{tab.Optional(tab.Mut(types.Int)), types.Unique, true},
		{tab.Record([]types.RecordField{{Name: h.in.Intern("a"), Type: types.Int}, {Name: h.in.Intern("b"), Type: tab.Mut(types.Int)}}), types.Unique, true},
		{h.named(lin), types.Linear, false},
		{h.named(words), types.Unique, true},
		{h.named(k.Cell, types.Int), types.Unique, false},
		{h.named(k.Mutex, types.Int), types.Unique, false},
		{h.named(k.Mutex, h.named(k.Arena)), types.Linear, true},
		{h.named(k.Chan, h.named(lin)), types.Unique, false},
		{h.named(k.Arena), types.Linear, true},
		{h.named(k.Io), types.Plain, false},
		{h.named(en), types.Unique, true},
		{types.Poison, types.Plain, false},
	}
	for _, r := range rows {
		f := h.c.Facts(r.t)
		if f.Kind != r.kind || f.Carrier != r.carrier {
			t.Errorf("%s: kind %v carrier %v, want %v %v", h.str(r.t), f.Kind, f.Carrier, r.kind, r.carrier)
		}
	}
	if f := h.c.Facts(h.named(k.Arena)); !f.Allocator || !f.Interior || !f.View {
		t.Errorf("Arena: %+v, want allocator, interior, view", f)
	}
	if f := h.c.Facts(h.named(h.strct("HoldsCell", 0, "", h.named(k.Cell, types.Int)))); !f.Interior {
		t.Errorf("a struct holding a Cell inline is interior: %+v", f)
	}
	if f := h.c.Facts(h.named(h.strct("HoldsRefCell", 0, "", tab.Ref(h.named(k.Cell, types.Int))))); f.Interior {
		t.Errorf("a Cell behind a reference is not interior: %+v", f)
	}
	if f := h.c.Facts(tab.Fn(types.FnType{Result: types.Void})); !f.IsFn {
		t.Errorf("fn: IsFn false")
	}
	// Assertion makes the constructor Unique.
	p := h.strct("P", 0, "", types.Int)
	h.assert(p, types.BoundSend, 0)
	if f := h.c.Facts(h.named(p)); f.Kind != types.Unique {
		t.Errorf("P with unsafe impl Send: kind %v, want unique", f.Kind)
	}
}

func TestSendShare(t *testing.T) {
	h := newHand(t)
	tab, k := h.tab, &h.c.known
	cellInt := h.named(k.Cell, types.Int)
	rows := []struct {
		name        string
		t           types.TypeID
		send, share bool
	}{
		{"int", types.Int, true, true},
		{"&int", tab.Ref(types.Int), true, true},
		{"*int", tab.RawPtr(types.Int), false, false},
		{"Cell[int]", cellInt, true, false},
		{"&Cell[int]", tab.Ref(cellInt), false, false},
		{"[]Cell[int]", tab.Slice(cellInt), false, false},
		{"mut Cell[int]", tab.Mut(cellInt), true, false},
		{"Io", h.named(k.Io), true, true},
		{"&Heap", tab.Ref(h.named(k.Heap)), true, true},
		{"Alloc", h.named(k.Alloc), false, false},
		{"System", h.named(k.System), false, true},
		{"&Scope", tab.Ref(h.named(k.Scope, types.Int)), true, true},
		{"str", types.Str, true, true},
		{"fn", tab.Fn(types.FnType{Result: types.Void}), true, true},
	}
	for _, r := range rows {
		f := h.c.Facts(r.t)
		if f.Send != r.send || f.Share != r.share {
			t.Errorf("%s: send %v share %v, want %v %v", r.name, f.Send, f.Share, r.send, r.share)
		}
	}
	// Assertions: Arena is Send only; Mutex[T] is Share when T: Send.
	h.assert(k.Arena, types.BoundSend, 0)
	if f := h.c.Facts(h.named(k.Arena)); !f.Send || f.Share {
		t.Errorf("Arena: send %v share %v, want true false", f.Send, f.Share)
	}
	h.assert(k.Mutex, types.BoundShare, 1, types.BoundSend)
	h.assert(k.Mutex, types.BoundSend, 1, types.BoundSend)
	if f := h.c.Facts(h.named(k.Mutex, cellInt)); !f.Send || !f.Share || f.Freeze {
		t.Errorf("Mutex[Cell[int]]: %+v, want Send, Share, not Freeze", f)
	}
	if f := h.c.Facts(h.named(k.Mutex, tab.Ref(cellInt))); f.Send || f.Share {
		t.Errorf("Mutex[&Cell[int]]: %+v, want neither", f)
	}
	// Recursion through references (coinductive) and the memo of a cycle.
	node := h.items.AddItem(&types.Item{Module: h.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "Node")})
	h.items.Get(node).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: tab.Optional(tab.Ref(h.named(node)))}, {Type: types.Int}}}
	if f := h.c.Facts(h.named(node)); !f.Send || !f.Share {
		t.Errorf("Node: %+v, want Send and Share", f)
	}
	a := h.items.AddItem(&types.Item{Module: h.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "A")})
	b := h.items.AddItem(&types.Item{Module: h.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "B")})
	h.items.Get(a).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: tab.Ref(h.named(b))}, {Type: cellInt}}}
	h.items.Get(b).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: tab.Ref(h.named(a))}}}
	if fa, fb := h.c.Facts(h.named(a)), h.c.Facts(h.named(b)); fa.Share || fb.Share || fb.Send {
		t.Errorf("A holds a Cell, B holds &A: A share %v, B share %v send %v, want all false", fa.Share, fb.Share, fb.Send)
	}
	// The same cycle asked from B first, in a fresh memo.
	h2 := newHand(t)
	cell2 := h2.named(h2.c.known.Cell, types.Int)
	a2 := h2.items.AddItem(&types.Item{Module: h2.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "A")})
	b2 := h2.items.AddItem(&types.Item{Module: h2.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "B")})
	h2.items.Get(a2).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: h2.tab.Ref(h2.named(b2))}, {Type: cell2}}}
	h2.items.Get(b2).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: h2.tab.Ref(h2.named(a2))}}}
	if fb, fa := h2.c.Facts(h2.named(b2)), h2.c.Facts(h2.named(a2)); fa.Share || fb.Share || fb.Send {
		t.Errorf("B first: A share %v, B share %v send %v, want all false", fa.Share, fb.Share, fb.Send)
	}
}

func TestFreeze(t *testing.T) {
	h := newHand(t)
	tab, k := h.tab, &h.c.known
	cellInt := h.named(k.Cell, types.Int)
	share := h.strct("S", 0, "", types.Int)
	h.assert(share, types.BoundShare, 0)
	rows := []struct {
		name   string
		t      types.TypeID
		freeze bool
	}{
		{"int", types.Int, true},
		{"Cell[int]", cellInt, false},
		{"Mutex[int]", h.named(k.Mutex, types.Int), false},
		{"Chan[int]", h.named(k.Chan, types.Int), false},
		{"Atomic", h.named(k.Atomic), false},
		{"*int", tab.RawPtr(types.Int), false},
		{"&Cell[int]", tab.Ref(cellInt), true},
		{"struct holding &Cell", h.named(h.strct("R", 0, "", tab.Ref(cellInt))), true},
		{"struct holding a Cell", h.named(h.strct("C", 0, "", cellInt)), false},
		{"?Cell[int]", tab.Optional(cellInt), false},
		{"unsafe impl Share", h.named(share), false},
	}
	for _, r := range rows {
		if f := h.c.Facts(r.t); f.Freeze != r.freeze {
			t.Errorf("%s: freeze %v, want %v", r.name, f.Freeze, r.freeze)
		}
	}
}

func TestFreeze_ReferenceView(t *testing.T) {
	h := newHand(t)
	words := h.strct("Words2", 0, "view", types.Str, types.Int)
	if f := h.c.Facts(h.named(words)); !f.Freeze || !f.View || !f.Carrier {
		t.Errorf("Words2: %+v, want Freeze, view, carrier", f)
	}
	if f := h.c.Facts(h.named(h.c.known.Arena)); f.Freeze {
		t.Errorf("Arena: Freeze true, want false (raw pointer fields)")
	}
}

func TestLoanInteriorAndMutView(t *testing.T) {
	h := newHand(t)
	k, tab := &h.c.known, h.tab
	refInt := tab.Ref(types.Int)
	optRef := tab.Optional(refInt)
	roView := h.strct("RoView", 0, "view", types.Str)
	roBox := h.strct("RoBox", 1, "view", tab.RawPtr(tab.Param(0)))
	mutView := h.strct("MutView", 0, "mut view", types.Int)
	rows := []struct {
		name         string
		t            types.TypeID
		mutView      bool
		loanInterior bool
		interior     bool
		freeze       bool
	}{
		{"Cell[int]", h.named(k.Cell, types.Int), false, false, true, false},
		{"Cell[&int]", h.named(k.Cell, refInt), false, true, true, false},
		{"Mutex[int]", h.named(k.Mutex, types.Int), false, false, true, false},
		{"Mutex[?&int]", h.named(k.Mutex, optRef), false, true, true, false},
		{"Chan[int]", h.named(k.Chan, types.Int), false, false, true, false},
		{"Chan[str]", h.named(k.Chan, types.Str), false, true, true, false},
		{"SpinCore[int]", h.named(k.SpinCore, types.Int), false, false, true, false},
		{"SpinCore[?&int]", h.named(k.SpinCore, optRef), false, true, true, false},
		{"Atomic", h.named(k.Atomic), false, false, true, false},
		{"Arena", h.named(k.Arena), true, true, true, false},
		{"Scope", h.named(k.Scope), true, true, true, false},
		{"RoView", h.named(roView), false, false, false, true},
		{"MutView", h.named(mutView), true, true, true, false},
		{"RoBox[int]", h.named(roBox, types.Int), false, false, false, false},
		{"RoBox[Cell[int]]", h.named(roBox, h.named(k.Cell, types.Int)), false, false, false, false},
		{"RoBox[Mutex[?&int]]", h.named(roBox, h.named(k.Mutex, optRef)), false, true, false, false},
	}
	for _, r := range rows {
		f := h.c.Facts(r.t)
		if f.MutView != r.mutView || f.LoanInterior != r.loanInterior || f.Interior != r.interior || f.Freeze != r.freeze {
			t.Errorf("%s: got (mutView=%v loanInt=%v int=%v freeze=%v), want (%v %v %v %v)",
				r.name, f.MutView, f.LoanInterior, f.Interior, f.Freeze,
				r.mutView, r.loanInterior, r.interior, r.freeze)
		}
	}
}

func TestLoanInterior_RecursivePlainView(t *testing.T) {
	// struct Node { next: RoList[Node]; c: Cell[?&int] }
	// Asking Facts(Node) first must not memoize RoList[Node].LoanInterior=false
	// while Node is assumed false on the cycle stack.
	for _, nodeFirst := range []bool{true, false} {
		h := newHand(t)
		roList := h.strct("RoList", 1, "view", h.tab.RawPtr(h.tab.Param(0)))
		node := h.items.AddItem(&types.Item{Module: h.mod, Kind: types.KindStruct, Key: types.ItemKey("h", "Node")})
		nodeT := h.named(node)
		listNodeT := h.named(roList, nodeT)
		cellRef := h.named(h.c.known.Cell, h.tab.Optional(h.tab.Ref(types.Int)))
		h.items.Get(node).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: listNodeT}, {Type: cellRef}}}
		var fn, fl types.Facts
		if nodeFirst {
			fn = h.c.Facts(nodeT)
			fl = h.c.Facts(listNodeT)
		} else {
			fl = h.c.Facts(listNodeT)
			fn = h.c.Facts(nodeT)
		}
		if !fn.LoanInterior || !fl.LoanInterior {
			t.Errorf("nodeFirst=%v: Node.LoanInterior=%v RoList[Node].LoanInterior=%v, want both true",
				nodeFirst, fn.LoanInterior, fl.LoanInterior)
		}
	}
}

func TestFactsMemoConcurrent(t *testing.T) {
	h := newHand(t)
	pair := h.strct("Pair", 2, "", h.tab.Param(0), h.tab.Param(1))
	var ts []types.TypeID
	scalars := []types.TypeID{types.Int, types.Str, types.Bool, h.tab.Mut(types.Int), h.named(h.c.known.Cell, types.Int)}
	for _, x := range scalars {
		for _, y := range scalars {
			ts = append(ts, h.named(pair, x, h.tab.Optional(y)))
		}
	}
	want := make([]types.Facts, len(ts))
	ref := newHand(t)
	_ = ref
	var wg sync.WaitGroup
	got := make([][]types.Facts, 8)
	for g := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[g] = make([]types.Facts, len(ts))
			for i, x := range ts {
				got[g][i] = h.c.Facts(x)
			}
		}()
	}
	wg.Wait()
	for i, x := range ts {
		want[i] = h.c.Facts(x)
		for g := range got {
			if got[g][i] != want[i] {
				t.Errorf("goroutine %d: %s: %+v, want %+v", g, h.str(x), got[g][i], want[i])
			}
		}
	}
}

func TestFacts_ParamNotSharedAcrossGenerics(t *testing.T) {
	h := newHand(t)
	p0 := h.tab.Param(0)
	envF := h.c.NewEnv([]types.GenericParam{{Name: h.in.Intern("T"), Bounds: []types.Bound{{Kind: types.BoundPlain}}}})
	envG := h.c.NewEnv([]types.GenericParam{{Name: h.in.Intern("T")}})
	if f := h.c.FactsIn(envF, p0); f.Kind != types.Plain || !f.Carrier {
		t.Errorf("T: Plain under F: %+v", f)
	}
	if f := h.c.FactsIn(envG, p0); f.Kind != types.Linear || !f.Carrier || f.Send || f.Share || f.Freeze {
		t.Errorf("T under G: %+v, want Linear carrier, nothing else", f)
	}
	if f := h.c.FactsIn(envF, h.tab.Ref(p0)); f.Kind != types.Plain {
		t.Errorf("&T: %+v", f)
	}
	if f := h.c.FactsIn(envF.Narrow(0, h.tab.Mut(types.Int)), p0); f.Kind != types.Unique {
		t.Errorf("T narrowed to mut int: %+v", f)
	}
	if f := h.c.FactsIn(envF, h.tab.SelfType()); f.Kind != types.Linear || !f.Carrier {
		t.Errorf("Self: %+v", f)
	}
	if ie := source.Guard("t", source.Span{}, func() { h.c.Facts(p0) }); ie == nil {
		t.Errorf("Facts(Param(0)) is not an ICE")
	}
}

func TestNotFinalIsICE(t *testing.T) {
	h := newHand(t)
	p := h.strct("P", 0, "", types.Int)
	h.m.final = false
	if ie := source.Guard("t", source.Span{}, func() { h.c.Facts(h.named(p)) }); ie == nil {
		t.Errorf("Facts of a type of a module before step 3 is not an ICE")
	}
	h.m.final = true
	if f := h.c.Facts(h.named(p)); f.Kind != types.Plain {
		t.Errorf("P: %+v", f)
	}
}
