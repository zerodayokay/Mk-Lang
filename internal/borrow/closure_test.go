// Why: a type walk over a polymorphically recursive type stops at a bound,
// and the answer there must not miss a type that can take a store.
// What: tests of closureTakes and TypeClosure, firstTakes's answer at recur's
// bound.
// How: declares recursive generic structs (G, H, V) and checks which
// instances count as taking a store.

package borrow

import (
	"testing"

	"mk/internal/types"
)

// TestClosureTakesFromHeads: at the bound, a polymorphically
// recursive instance counts only when some node of its closure can make
// an instance take a store: a Cell, Mutex or view in an argument or in a
// declaration it reaches, or (storesFirstGen) a type parameter of the
// walk's own. G[int], G[?&i32] and G[T] in storesFirst take none; G[T] in
// storesFirstGen, G[?&Cell[i32]] and an H[T] whose declaration holds a
// Mutex do.
func TestClosureTakesFromHeads(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	k.structItem(k.main, "H", 1, false, false, field("m", k.tab.Optional(k.tab.Ref(k.tab.Named(k.known.Mutex, []types.GenericArg{types.TypeArg(p0)})))))
	k.structItem(k.main, "V", 1, true, false, field("p", k.tab.Ref(p0)))
	rec := func(name string, extra ...types.FieldDecl) types.ItemID {
		g := k.structItem(k.main, name, 1, false, false)
		next := k.tab.Optional(k.tab.Ref(k.tab.Named(g, []types.GenericArg{types.TypeArg(k.tab.Optional(p0))})))
		k.items.Get(g).Decl.(*types.StructDecl).Fields = append([]types.FieldDecl{
			{Name: k.in.Intern("v"), Type: p0},
			{Name: k.in.Intern("next"), Type: next},
		}, extra...)
		return g
	}
	rec("G")
	rec("GH", types.FieldDecl{Name: k.in.Intern("h"), Type: k.tab.Optional(k.tab.Ref(k.named("H", p0)))})
	rec("GV", types.FieldDecl{Name: k.in.Intern("w"), Type: k.tab.Optional(k.tab.Ref(k.named("V", p0)))})
	cellI32 := k.tab.Named(k.known.Cell, []types.GenericArg{types.TypeArg(types.I32)})
	cellRef := k.tab.Named(k.known.Cell, []types.GenericArg{types.TypeArg(refI32(k))})
	w := walkerOf(k, nil)
	for _, c := range []struct {
		name string
		t    types.TypeID
		gen  genMode
		want bool
	}{
		{"G[int]", k.named("G", types.Int), genNone, false},
		{"G[?&i32]", k.named("G", k.tab.Optional(k.tab.Ref(types.I32))), genNone, false},
		{"G[T]", k.named("G", p0), genNone, false},
		{"G[T] gen", k.named("G", p0), genAll, true},
		{"G[T] bare", k.named("G", p0), genBare, true},
		{"G[?&Cell[i32]]", k.named("G", k.tab.Optional(k.tab.Ref(cellI32))), genNone, false},
		{"G[?&Cell[&i32]]", k.named("G", k.tab.Optional(k.tab.Ref(cellRef))), genNone, true},
		{"GH[int]", k.named("GH", types.Int), genNone, true},
		{"GV[int]", k.named("GV", types.Int), genNone, true},
	} {
		if got := w.closureTakes(c.t, c.gen); got != c.want {
			t.Errorf("closureTakes(%s, %d) = %v, want %v", c.name, c.gen, got, c.want)
		}
	}
	// storesFirst reaches the bound on G[int] and answers from the closure.
	if w.storesFirst(k.named("G", types.Int)) {
		t.Error("storesFirst(G[int]) = true, want false (G[?int] takes no store)")
	}
	if !w.storesFirst(k.named("GH", types.Int)) {
		t.Error("storesFirst(GH[int]) = false, want true (H's &Mutex)")
	}
}
