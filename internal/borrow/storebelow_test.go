// Why: the writable walk stops at a holder whose type storesBelow answers
// no, so a wrong no is a store the checker never judges.
// What: TestStoresBelow, storesBelow's answer on the holder shapes the rule
// names: references to carriers and non-carriers, views by type argument,
// raw pointers in views and outside them.
// How: declares the types with the test kit and asks one walker.

package borrow

import (
	"testing"

	"mk/internal/types"
)

// TestStoresBelow: a view over a mut reference to a non-carrier and a view
// over its own type argument's storage of non-carriers answer no, also
// behind a shared reference; a mut reference to a carrier, a carrier type
// argument, a raw-pointer type argument, a view's raw pointer to anything
// but a type parameter, and a raw pointer outside a view, by value or behind
// a reference (to a carrier or not), answer yes; an Alloc behind a shared
// reference answers no.
func TestStoresBelow(t *testing.T) {
	k := newKit(t)
	tab := k.tab
	p0 := tab.Param(0)
	k.structItem(k.main, "Inner", 0, false, false, field("n", types.I32))
	k.structItem(k.main, "Slot", 0, false, false, field("r", tab.Ref(types.I32)))
	k.structItem(k.main, "D", 0, true, false, field("a", tab.Mut(k.named("Inner"))))
	k.structItem(k.main, "W", 0, true, false, field("s", tab.Mut(k.named("Slot"))))
	k.structItem(k.main, "Vec", 1, true, false, field("ptr", tab.RawPtr(p0)), field("n", types.Int))
	k.structItem(k.main, "Box1", 1, false, false, field("v", p0))
	k.structItem(k.main, "PoolV", 1, true, false, field("slots", tab.RawPtr(k.named("Box1", p0))))
	k.structItem(k.main, "S", 0, false, false, field("p", tab.RawPtr(k.Point)), field("t", tab.Ref(types.U8)))
	k.structItem(k.main, "HoldS", 0, false, false, field("r", tab.Ref(k.named("S"))))
	k.structItem(k.main, "Bare", 0, false, false, field("p", tab.RawPtr(k.Point)))
	k.structItem(k.main, "HoldBare", 0, false, false, field("r", tab.Ref(k.named("Bare"))), field("t", tab.Ref(types.U8)))
	k.structItem(k.main, "HoldRaws", 0, false, false, field("ps", tab.Slice(tab.RawPtr(k.Point))), field("t", tab.Ref(types.U8)))
	k.structItem(k.main, "HoldAlloc", 0, false, false, field("a", tab.Ref(k.Alloc)), field("t", tab.Ref(types.U8)))
	k.structItem(k.main, "HoldVec", 0, false, false, field("v", tab.Ref(k.named("Vec", types.U64))))
	k.structItem(k.main, "CV", 0, true, false, field("c", tab.Ref(k.named("Cell", tab.Optional(tab.Ref(types.I32))))))
	w := walkerOf(k, nil)
	for _, c := range []struct {
		name string
		t    types.TypeID
		want bool
	}{
		{"D { a: mut Inner }", k.named("D"), false},
		{"Vec[u64]", k.named("Vec", types.U64), false},
		{"HoldVec { v: &Vec[u64] }", k.named("HoldVec"), false},
		{"W { s: mut Slot }", k.named("W"), true},
		{"CV { c: &Cell[?&i32] }", k.named("CV"), true},
		{"Vec[&i32]", k.named("Vec", tab.Ref(types.I32)), true},
		{"Vec[*u8]", k.named("Vec", tab.RawPtr(types.U8)), true},
		{"PoolV[u64] { slots: *Box1[T] }", k.named("PoolV", types.U64), true},
		{"S { p: *Point, t: &u8 }", k.named("S"), true},
		{"HoldAlloc { a: &Alloc, t: &u8 }", k.named("HoldAlloc"), false},
		{"HoldS { r: &S }", k.named("HoldS"), true},
		{"HoldBare { r: &Bare, t: &u8 }", k.named("HoldBare"), true},
		{"HoldRaws { ps: []*Point, t: &u8 }", k.named("HoldRaws"), true},
	} {
		if got := w.storesBelow(c.t); got != c.want {
			t.Errorf("storesBelow(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
