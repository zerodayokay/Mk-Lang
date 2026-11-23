// Why: a store from one part of a parameter into another part of it is legal
// only when it stays inside the parameter; the walk that decides it once
// accepted unsound shapes.
// What: tests of such stores, through a call's `*a into b` or a direct write,
// with and without a view or an allocator beside them, with the r12Put,
// r12Deep and r12Field builders.
// How: hand-built Deep, PairR and view structs, checked for E0604 and E0505.

package borrow

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// r12Put declares Put(src: mut &i32, dst: mut mut &i32) *src into dst, whose
// body is `**dst = *src`: a store at the second level below dst.
func r12Put(k *kit) {
	ri := refI32(k)
	s := k.fn("Put", []types.Param{k.param("src", k.tab.Mut(ri)), k.param("dst", k.tab.Mut(k.tab.Mut(ri)))},
		types.Void, types.EmptyErrors)
	s.Flows.Stores = []types.Store{{From: 1, Into: 2}}
}

// r12Deep declares struct Deep { a: &i32, b: mut &i32, extra... } and
// Deep.Mix(mut self) { Put(mut self.a, mut self.b) }, with the clause
// `*self into self` when clause is set; it returns the body and the call's
// span.
func r12Deep(t *testing.T, k *kit, clause bool, extra ...fieldSpec) (*mb, mir.SpanID) {
	t.Helper()
	ri := refI32(k)
	mri := k.tab.Mut(ri)
	r12Put(k)
	k.structItem(k.main, "Deep", 0, false, false, append([]fieldSpec{field("a", ri), field("b", mri)}, extra...)...)
	deep := k.named("Deep")
	s := k.method("Deep", "Mix", types.ByMut, deep, nil, types.Void, types.EmptyErrors)
	if clause {
		s.Flows.Stores = []types.Store{{From: 1, Into: 1}}
	}
	m := k.body(s)
	ta, tb := m.temp(mri), m.temp(k.tab.Mut(mri))
	m.set(ta, mutRef(m.pl(1, deref(deep), fld(0, ri))))
	m.set(tb, mutRef(m.pl(1, deref(deep), fld(1, mri))))
	call := m.callv("Put", nil, argMove(m.local(ta)), argMove(m.local(tb)))
	m.end(tb, ta)
	m.ret(mir.ExitNormal)
	return m, call
}

// The guard against the unsound reading: Deep.Mix(mut self) { Put(mut self.a,
// mut self.b) } has the same state at the call as doc/style.md's Buffers.Flip
// (two disjoint first-level fields of self), and Put writes *self.a through
// self.b into memory outside self. Without `*self into self` it is E0604 at
// the call; with it, clean.
func TestR12_PutStillRejected(t *testing.T) {
	for _, clause := range []bool{false, true} {
		k := newKit(t)
		m, call := r12Deep(t, k, clause)
		if clause {
			m.expect(t, m.check(t))
		} else {
			m.expect(t, m.check(t), diag.E0604, call)
		}
	}
}

// The clause twin's caller. Inner(r: mut &i32) { z := 7; var d :=
// Deep(a: &z, b: r); d.Mix() // stores &z into what r refers to }: E0505
// at &z, since z dies before the caller's r. The call is also E0604: Mix's
// `*self into self` stores all of d's contents, r's own loan among them,
// into what r refers to, which Inner's signature does not declare. mk check
// gives the same two on the source form.
func TestR12_PutClauseCallerE0505(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	mri := k.tab.Mut(ri)
	r12Deep(t, k, true)
	deep := k.named("Deep")
	m := k.body(k.sig("Inner", []types.Param{k.param("r", mri)}, types.Void, types.EmptyErrors))
	z := m.user("z", types.I32, false)
	d := m.user("d", deep, true)
	m.set(z, use(imm(types.I32, 7)))
	tz := m.temp(ri)
	bad := m.set(tz, ref(m.local(z)))
	m.set(d, m.agg(mir.AggStruct, k.item("Deep"), deep, cp(m.local(tz)), mv(m.local(1))))
	td := m.temp(k.tab.Mut(deep))
	m.set(td, mutRef(m.local(d)))
	call := m.callv("Deep.Mix", nil, argMove(m.local(td)))
	m.end(td, tz, d, z)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0505, bad, diag.E0604, call)
}

// The cross-root case: SwapX(p: mut PairR, q: mut PairR) {
// Exchange(mut p.a, mut q.a) } with PairR { a: &i32 } stores p's contents
// into q and q's into p: E0604 at the call. With `*p into q, *q into p`,
// clean.
func TestR12_CrossRoot(t *testing.T) {
	for _, clause := range []bool{false, true} {
		k := newKit(t)
		ri := refI32(k)
		mri := k.tab.Mut(ri)
		k.structItem(k.main, "PairR", 0, false, false, field("a", ri))
		pr := k.named("PairR")
		s := k.fn("SwapX", []types.Param{k.param("p", k.tab.Mut(pr)), k.param("q", k.tab.Mut(pr))}, types.Void, types.EmptyErrors)
		if clause {
			s.Flows.Stores = []types.Store{{From: 1, Into: 2}, {From: 2, Into: 1}}
		}
		m := k.body(s)
		ta, tb := m.temp(mri), m.temp(mri)
		m.set(ta, mutRef(m.pl(1, deref(pr), fld(0, ri))))
		m.set(tb, mutRef(m.pl(2, deref(pr), fld(0, ri))))
		call := m.callv("Exchange", []types.TypeID{ri}, argMove(m.local(ta)), argMove(m.local(tb)))
		m.end(tb, ta)
		m.ret(mir.ExitNormal)
		if clause {
			m.expect(t, m.check(t))
		} else {
			m.expect(t, m.check(t), diag.E0604, call)
		}
	}
}

// The allocator control: without the allocator field the same store is
// judged and is E0604 (the TestR12_PutStillRejected shape). With a List
// or &Arena field it is the same verdict (allocnode_test.go's tests).
func TestR12_ControlNoAllocatorField(t *testing.T) {
	k := newKit(t)
	m, call := r12Deep(t, k, false, field("n", types.I32))
	m.expect(t, m.check(t), diag.E0604, call)
}

// r12Field declares struct name { x: &i32, y: &i32, extra... } and
// name.M(mut self) { self.x = self.y }, and returns the body and the write.
func r12Field(t *testing.T, k *kit, name string, extra ...fieldSpec) (*mb, mir.SpanID) {
	t.Helper()
	ri := refI32(k)
	k.structItem(k.main, name, 0, false, false, append([]fieldSpec{field("x", ri), field("y", ri)}, extra...)...)
	st := k.named(name)
	m := k.body(k.method(name, "M", types.ByMut, st, nil, types.Void, types.EmptyErrors))
	w := m.assign(m.pl(1, deref(st), fld(0, ri)), use(cp(m.pl(1, deref(st), fld(1, ri)))))
	m.ret(mir.ExitNormal)
	return m, w
}

// VL.M(mut self) { self.x = self.y } writes the first level of self (that
// level already holds everything below it), and the written place is
// not inside the view field t, so the direct write rule does not walk on
// into t's contents: clean, as for a struct with no view, and with an
// allocator field beside the view.
func TestR12_FieldWriteBesideView(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra func(k *kit) []fieldSpec
	}{
		{"NoView", func(k *kit) []fieldSpec { return nil }},
		{"HeapList", func(k *kit) []fieldSpec { return []fieldSpec{field("t", k.named("HeapList", types.I32))} }},
		{"HeapListRefs", func(k *kit) []fieldSpec { return []fieldSpec{field("t", k.named("HeapList", refI32(k)))} }},
		{"TextArena", func(k *kit) []fieldSpec {
			return []fieldSpec{field("t", k.named("Text")), field("ar", k.tab.Ref(k.Arena))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKit(t)
			m, _ := r12Field(t, k, "V"+tc.name, tc.extra(k)...)
			m.expect(t, m.check(t))
		})
	}
}

// The guard: with a view beside it, a write that goes on through a second
// deref into the deep node (Deep.Mix's `*self.b = self.a`) is still
// E0604 without the clause.
func TestR12_DeepWriteBesideViewStillRejected(t *testing.T) {
	for _, clause := range []bool{false, true} {
		k := newKit(t)
		ri := refI32(k)
		mri := k.tab.Mut(ri)
		k.structItem(k.main, "DV", 0, false, false, field("a", ri), field("b", mri), field("t", k.named("HeapList", ri)))
		dv := k.named("DV")
		s := k.method("DV", "Mix", types.ByMut, dv, nil, types.Void, types.EmptyErrors)
		if clause {
			s.Flows.Stores = []types.Store{{From: 1, Into: 1}}
		}
		m := k.body(s)
		w := m.assign(m.pl(1, deref(dv), fld(1, mri), deref(ri)), use(cp(m.pl(1, deref(dv), fld(0, ri)))))
		m.ret(mir.ExitNormal)
		if clause {
			m.expect(t, m.check(t))
		} else {
			m.expect(t, m.check(t), diag.E0604, w)
		}
	}
}

// The guard: a parameter that is itself a view keeps the walk. G(g: mut
// Guard[&i32], v: &i32) { *g... } is not expressible on the kit's Guard (its
// one field is a raw pointer), so the guard is the store of another
// parameter's loan through a view-holding parameter's field: still E0604,
// whichever node judges it.
func TestR12_OtherParamBesideViewStillRejected(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	k.structItem(k.main, "VO", 0, false, false, field("x", ri), field("t", k.named("HeapList", ri)))
	vo := k.named("VO")
	m := k.body(k.method("VO", "Set", types.ByMut, vo, []types.Param{k.param("v", ri)}, types.Void, types.EmptyErrors))
	w := m.assign(m.pl(1, deref(vo), fld(0, ri)), use(cp(m.local(2))))
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0604, w)
}

// The guard: a struct declared `mut view` keeps the walk, since *self is then
// itself a mut view (a deref that lands on a view walks on): VW.M(mut self)
// { self.x = self.y } is still E0604. It fails if ownInline stops checking
// that the type after the deref is not a view.
func TestR12_ViewStructStillWalks(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	k.mutViewStruct(k.main, "VW", 0, false, field("x", ri), field("y", ri))
	vw := k.named("VW")
	m := k.body(k.method("VW", "M", types.ByMut, vw, nil, types.Void, types.EmptyErrors))
	w := m.assign(m.pl(1, deref(vw), fld(0, ri)), use(cp(m.pl(1, deref(vw), fld(1, ri)))))
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0604, w)
}

// The guard: a generic struct whose selected field is a type parameter
// keeps the walk, since an instance may make the field a view: in GM[T](p:
// mut GV[T]) with GV[T] { g: T, t: HeapList[i32] }, a write of p.g, or of
// the view field p.t, is judged at both nodes. It asks ownInline directly,
// as the kit cannot copy a T.
func TestR12_TypeParamFieldKeepsWalk(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	k.structItem(k.main, "GV", 1, false, false, field("g", p0), field("t", k.named("HeapList", types.I32)), field("x", refI32(k)))
	gv := k.named("GV", p0)
	m := k.body(k.genFn("GM", []types.Param{k.param("p", k.tab.Mut(gv))}, types.Void))
	m.b.GenericCount = 1
	m.ret(mir.ExitNormal)
	m.validate(t)
	c := newChecker(m.b, k.env(m.sig))
	c.setup()
	pg := c.paths.ofPlace(m.b, k.tab, m.pl(1, deref(gv), fld(0, p0)))
	if c.ownInline(c.paths.get(pg), 0, c.params[1].n1) {
		t.Fatalf("ownInline: a type-parameter field must keep the walk")
	}
	pt := c.paths.ofPlace(m.b, k.tab, m.pl(1, deref(gv), fld(1, k.named("HeapList", types.I32))))
	if c.ownInline(c.paths.get(pt), 0, c.params[1].n1) {
		t.Fatalf("ownInline: a write of the view field must keep the walk")
	}
	// The control: p.x (an &i32 beside the view) takes the skip.
	px := c.paths.ofPlace(m.b, k.tab, m.pl(1, deref(gv), fld(2, refI32(k))))
	if !c.ownInline(c.paths.get(px), 0, c.params[1].n1) {
		t.Fatalf("ownInline: p.x beside the view must take the skip")
	}
}
