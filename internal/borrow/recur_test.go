// Why: walks that follow references into generic instances must stop on
// polymorphically recursive types, yet never on a merely wide or deep type.
// What: tests of recur, the bound of those walks.
// How: builds wide, deeply nested and recursive generic structs and checks
// storesFirst, walkExtra and storesBelow.

package borrow

import (
	"fmt"
	"testing"

	"mk/internal/types"
)

// TestWalksBoundGrowthNotWidth: a wide type (65 fields ?&B[[i]u8], each
// a different instance of B) and a nest that shrinks as the walk goes into its
// argument (B[B[...66 deep...[i64]]]) give main's answers, false, in
// storesFirst, walkExtra and storesBelow, where a bound on the instances met
// in a walk made them true (E0602 on valid code). A polymorphically recursive
// G[T] { v: T, next: ?&G[?T] } ends: walkExtra and storesBelow with the
// answer an ANY step gives, true, and storesFirst with its closure's, false,
// since nothing any instance holds takes a store (closure_test.go).
func TestWalksBoundGrowthNotWidth(t *testing.T) {
	k := newKit(t)
	k.structItem(k.main, "B", 1, false, false, field("p", k.tab.Ref(k.tab.Param(0))))
	var fs []fieldSpec
	for i := 1; i <= 65; i++ {
		b := k.named("B", k.tab.Array(k.lenArg(uint64(i)), types.U8))
		fs = append(fs, field(fmt.Sprintf("g%d", i), k.tab.Optional(k.tab.Ref(b))))
	}
	k.structItem(k.main, "Wide", 0, false, false, fs...)
	wide := k.named("Wide")
	nest := types.I64
	for range 66 {
		nest = k.named("B", nest)
	}
	k.structItem(k.main, "Deep", 0, false, false, field("d", k.tab.Optional(k.tab.Ref(nest))))
	deep := k.named("Deep")
	g := k.structItem(k.main, "G", 1, false, false)
	gArg := types.TypeArg(k.tab.Optional(k.tab.Param(0)))
	k.items.Get(g).Decl.(*types.StructDecl).Fields = []types.FieldDecl{
		{Name: k.in.Intern("v"), Type: k.tab.Param(0)},
		{Name: k.in.Intern("next"), Type: k.tab.Optional(k.tab.Ref(k.tab.Named(g, []types.GenericArg{gArg})))},
	}
	gInt := k.named("G", types.I32)

	w := walkerOf(k, nil)
	for _, c := range []struct {
		name string
		t    types.TypeID
	}{{"Wide", wide}, {"Deep", deep}} {
		if w.storesFirst(c.t) {
			t.Errorf("storesFirst(%s) = true, want false (no reference in it reaches memory that takes stores)", c.name)
		}
		if w.walkExtra(c.t) {
			t.Errorf("walkExtra(%s) = true, want false (no exclusive reference or writable referent in it)", c.name)
		}
		if w.storesBelow(c.t) {
			t.Errorf("storesBelow(%s) = true, want false (no reference in it leads to a carrier slot)", c.name)
		}
	}
	if w.storesFirst(gInt) || !w.walkExtra(gInt) || !w.storesBelow(gInt) {
		t.Errorf("G[int]: storesFirst %v, walkExtra %v, storesBelow %v; want false, true and true at the bound",
			w.storesFirst(gInt), w.walkExtra(gInt), w.storesBelow(gInt))
	}
}

// TestAbstractNodesBoundRecordRecursion: a parameter &G[int] of a
// polymorphically recursive G whose next hop goes through a record (`next:
// ?&(a: G[?T], b: int)`) reaches a new record at every hop, as `next:
// ?&G[?T]` reaches a new G. abstractNodes' layer-2 walk stops both at ANY
// after about as many referents, never after maxReferents of the records
// (each a deeper instance, which every later walk over p#2 must go into).
func TestAbstractNodesBoundRecordRecursion(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	rec := func(name string, viaRecord bool) types.TypeID {
		g := k.structItem(k.main, name, 1, false, false)
		next := k.tab.Named(g, []types.GenericArg{types.TypeArg(k.tab.Optional(p0))})
		if viaRecord {
			next = k.tab.Record([]types.RecordField{{Name: k.in.Intern("a"), Type: next}, {Name: k.in.Intern("b"), Type: types.Int}})
		}
		k.items.Get(g).Decl.(*types.StructDecl).Fields = []types.FieldDecl{
			{Name: k.in.Intern("v"), Type: p0},
			{Name: k.in.Intern("next"), Type: k.tab.Optional(k.tab.Ref(next))},
		}
		return k.tab.Ref(k.named(name, types.Int))
	}
	direct, record := rec("G", false), rec("R", true)
	w := walkerOf(k, nil)
	_, nd, _ := w.abstractNodes(direct)
	_, nr, _ := w.abstractNodes(record)
	t.Logf("p#2 types: direct %d, through a record %d", len(nd.types), len(nr.types))
	if !nd.any || !nr.any {
		t.Fatalf("p#2 any: direct %v, record %v; want both true at the bound", nd.any, nr.any)
	}
	if len(nd.types) == 0 || len(nr.types) > 2*len(nd.types)+2 {
		t.Errorf("p#2 types: through a record %d, direct %d; want at most twice the direct walk's", len(nr.types), len(nd.types))
	}
	// A wide type is no recursion: 30 referents ?&B[[i]u8], 40 records (x:
	// B[[j]u8]) holding other instances of B, and 30 records (p: &B[[j]u8])
	// whose B is a referent of its own stay under the bounds (60 instances
	// of B met, 40 records holding one); p#2 has no ANY.
	k.structItem(k.main, "B", 1, false, false, field("p", k.tab.Ref(p0)))
	b := func(n int) types.TypeID { return k.named("B", k.tab.Array(k.lenArg(uint64(n)), types.U8)) }
	var fs []fieldSpec
	for i := 1; i <= 30; i++ {
		behind := k.tab.Record([]types.RecordField{{Name: k.in.Intern("p"), Type: k.tab.Ref(b(200 + i))}})
		fs = append(fs, field(fmt.Sprintf("b%d", i), k.tab.Optional(k.tab.Ref(b(i)))),
			field(fmt.Sprintf("q%d", i), k.tab.Optional(k.tab.Ref(behind))))
	}
	for i := 1; i <= 40; i++ {
		inline := k.tab.Record([]types.RecordField{{Name: k.in.Intern("x"), Type: b(100 + i)}})
		fs = append(fs, field(fmt.Sprintf("r%d", i), k.tab.Optional(k.tab.Ref(inline))))
	}
	k.structItem(k.main, "Mixed", 0, false, false, fs...)
	if _, nm, _ := walkerOf(k, nil).abstractNodes(k.tab.Ref(k.named("Mixed"))); nm.any {
		t.Errorf("p#2 of &Mixed is ANY (%d types); want no ANY", len(nm.types))
	}
	// 69 distinct records that hold the same instance B[int] are no
	// recursion: the walk keeps every one of them, with no ANY.
	var shared []fieldSpec
	for i := 1; i <= 69; i++ {
		r := k.tab.Record([]types.RecordField{{Name: k.in.Intern("x"), Type: k.named("B", types.Int)}, {Name: k.in.Intern("k"), Type: k.tab.Array(k.lenArg(uint64(i)), types.U8)}})
		shared = append(shared, field(fmt.Sprintf("s%d", i), k.tab.Optional(k.tab.Ref(r))))
	}
	k.structItem(k.main, "Shared", 0, false, false, shared...)
	if _, ns, _ := walkerOf(k, nil).abstractNodes(k.tab.Ref(k.named("Shared"))); ns.any || len(ns.types) < 69 {
		t.Errorf("p#2 of &Shared: any %v, %d types; want no ANY and the 69 records", ns.any, len(ns.types))
	}
}

// TestAbstractNodesCutKeepsTakes: a referent the layer-2 walk stops at
// still counts for the node's takes (cutTakes). 69 referents ?&B[[i]u8]
// and then ?&B[Cell[?&Mutex[?&i32]]], or the same through records
// (x: B[..], ...): the walk stops at ANY before the last, whose Cell holds
// a reference to memory that takes stores, so p#2 takes stores. Without
// the last field nothing past the stop takes any.
func TestAbstractNodesCutKeepsTakes(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	k.structItem(k.main, "B", 1, false, false, field("v", p0))
	b := func(n int) types.TypeID { return k.named("B", k.tab.Array(k.lenArg(uint64(n)), types.U8)) }
	mutex := k.tab.Named(k.known.Mutex, []types.GenericArg{types.TypeArg(k.tab.Optional(k.tab.Ref(types.I32)))})
	cell := k.tab.Named(k.known.Cell, []types.GenericArg{types.TypeArg(k.tab.Optional(k.tab.Ref(mutex)))})
	rec := func(x, y types.TypeID) types.TypeID {
		return k.tab.Record([]types.RecordField{{Name: k.in.Intern("x"), Type: x}, {Name: k.in.Intern("c"), Type: y}})
	}
	for _, c := range []struct {
		name string
		hop  func(i int) types.TypeID
		last types.TypeID
	}{
		{"named", b, k.named("B", cell)},
		{"record", func(i int) types.TypeID { return rec(b(i), types.Int) }, rec(b(70), cell)},
	} {
		for _, withLast := range []bool{true, false} {
			var fs []fieldSpec
			for i := 1; i <= 69; i++ {
				fs = append(fs, field(fmt.Sprintf("r%d", i), k.tab.Optional(k.tab.Ref(c.hop(i)))))
			}
			if withLast {
				fs = append(fs, field("last", k.tab.Optional(k.tab.Ref(c.last))))
			}
			name := fmt.Sprintf("W%s%v", c.name, withLast)
			k.structItem(k.main, name, 0, false, false, fs...)
			_, n2, _ := walkerOf(k, nil).abstractNodes(k.tab.Ref(k.named(name)))
			if !n2.any || n2.cutTakes != withLast {
				t.Errorf("%s, last %v: p#2 any %v cutTakes %v; want ANY, cutTakes %v", c.name, withLast, n2.any, n2.cutTakes, withLast)
			}
		}
	}
}

// TestRecurCountsGrowthOnThePath: recur stops a nest that grows past
// maxRecurGrowth on one path, never siblings or a nest that shrinks, and
// stops a walk that meets more than maxRecurInstances instances of one
// constructor that grew from an instance above them.
func TestRecurCountsGrowthOnThePath(t *testing.T) {
	k := newKit(t)
	k.structItem(k.main, "B", 1, false, false, field("p", k.tab.Ref(k.tab.Param(0))))
	w := walkerOf(k, nil)

	r := w.newRecur()
	t0 := types.I64
	var leaves []func()
	stopped := -1
	for i := 0; i < 2*maxRecurGrowth; i++ {
		t0 = k.named("B", k.tab.Optional(t0))
		stop, leave := r.enter(t0)
		if stop {
			stopped = i
			break
		}
		leaves = append(leaves, leave)
	}
	if stopped != maxRecurGrowth+1 {
		t.Errorf("a growing nest stopped at depth %d, want %d", stopped, maxRecurGrowth+1)
	}
	for i := len(leaves) - 1; i >= 0; i-- {
		leaves[i]()
	}

	r = w.newRecur()
	for i := 1; i <= 2*maxRecurGrowth; i++ {
		stop, leave := r.enter(k.named("B", k.tab.Array(k.lenArg(uint64(i)), types.U8)))
		if stop {
			t.Fatalf("sibling %d stopped the walk", i)
		}
		leave()
	}

	r = w.newRecur()
	shrink := types.I64
	var nest []types.TypeID
	for range 2 * maxRecurGrowth {
		shrink = k.named("B", shrink)
		nest = append(nest, shrink)
	}
	for i := len(nest) - 1; i >= 0; i-- {
		if stop, _ := r.enter(nest[i]); stop {
			t.Fatalf("a shrinking nest stopped at %d", i)
		}
	}

	// Siblings never grow, so no number of them stops the walk; past
	// maxRecurInstances instances that grew from one above them, it stops.
	r = w.newRecur()
	for i := 1; i <= maxRecurInstances+1; i++ {
		stop, leave := r.enter(k.named("B", k.tab.Array(k.lenArg(uint64(i)), types.U8)))
		if stop {
			t.Fatalf("sibling %d stopped the walk", i)
		}
		leave()
	}
	r = w.newRecur()
	_, top := r.enter(k.named("B", types.I64))
	defer top()
	for i := 1; i <= maxRecurInstances+1; i++ {
		stop, leave := r.enter(k.named("B", k.tab.Array(k.lenArg(uint64(i)), types.I64)))
		if stop != (i > maxRecurInstances) {
			t.Fatalf("grown instance %d: stop %v", i, stop)
		}
		if leave != nil {
			leave()
		}
	}
}
