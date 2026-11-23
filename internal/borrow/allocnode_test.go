// Why: an allocator a parameter reaches below its first level once merged
// with what a mut reference reaches, so a callee's store through the
// reference was never judged and use-after-free programs were accepted.
// What: tests that such an allocator is an abstract node of its own, p#A, so
// the deep node p#2 never merges an allocator with what a mut reference or a
// view reaches, and that the writable walk stops at p#A alone (an allocator
// counts only as the first hop); plus the same split at the first level,
// p#1A.
// How: structs with List, &Arena, &Heap and &Alloc fields beside mut
// references, checked for E0604 and E0505, and the node model read directly.

package borrow

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// f1Deep is r12Deep (Deep { a: &i32, b: mut &i32, extra }, Deep.Mix(mut
// self) { Put(mut self.a, mut self.b) }) with the clause when clause is set:
// E0604 at the call without it, clean with it, whatever allocator field
// stands beside a and b.
func f1Deep(t *testing.T, extra func(k *kit) fieldSpec) {
	t.Helper()
	for _, clause := range []bool{false, true} {
		k := newKit(t)
		m, call := r12Deep(t, k, clause, extra(k))
		if clause {
			m.expect(t, m.check(t))
		} else {
			m.expect(t, m.check(t), diag.E0604, call)
		}
	}
}

// A field `l: List[i32]` beside a and b. List's `arena: &Arena` puts Arena in
// p#A, and p#2 (i32, &i32 and List's view ANY) is walked: E0604 at the call,
// as without the field (TestR12_ControlNoAllocatorField).
func TestListField(t *testing.T) {
	f1Deep(t, func(k *kit) fieldSpec { return field("l", k.named("List", types.I32)) })
}

// With `ar: &Arena` beside a and b: the same verdicts.
func TestArenaRefField(t *testing.T) {
	f1Deep(t, func(k *kit) fieldSpec { return field("ar", k.tab.Ref(k.Arena)) })
}

// With `h: &Heap` and with `al: &Alloc`: every allocator gets p#A.
func TestHeapAndAllocFields(t *testing.T) {
	f1Deep(t, func(k *kit) fieldSpec { return field("h", k.tab.Ref(k.Heap)) })
	f1Deep(t, func(k *kit) fieldSpec { return field("al", k.tab.Ref(k.Alloc)) })
}

// The node model: for Deep { a, b, l: List[i32] }, Arena is in p#A only,
// p#2 is no allocator, p#A is, and p#1 is unchanged.
func TestAllocatorNodeOfItsOwn(t *testing.T) {
	k := newKit(t)
	m, _ := r12Deep(t, k, false, field("l", k.named("List", types.I32)))
	m.validate(t)
	c := newChecker(m.b, k.env(m.sig))
	c.setup()
	pi := c.params[1]
	if pi.nA == noRoot {
		t.Fatalf("Deep with a List field: no p#A")
	}
	if c.roots[pi.n2].allocator || !c.roots[pi.n2].carrier || c.roots[pi.n2].writable {
		t.Fatalf("p#2: allocator %v carrier %v writable %v; want false, true, false (plain List[i32] is read-only)",
			c.roots[pi.n2].allocator, c.roots[pi.n2].carrier, c.roots[pi.n2].writable)
	}
	if a := &c.roots[pi.nA]; !a.allocator || a.kind != rkAbstract || a.depth != 2 || a.param != 1 {
		t.Fatalf("p#A: %+v", *a)
	}
	if c.roots[pi.n1].allocator {
		t.Fatalf("p#1 (Deep) is no allocator")
	}
	n1, n2, nA := c.w.abstractNodes(k.tab.Mut(k.named("Deep")))
	for _, ty := range n2.types {
		if ty == k.Arena {
			t.Fatalf("p#2 types %v hold Arena", n2.types)
		}
	}
	if len(nA.types) != 1 || nA.types[0] != k.Arena || len(n1.types) != 1 {
		t.Fatalf("p#1 %v, p#A %v; want [Deep], [Arena]", n1.types, nA.types)
	}
	// A parameter with no allocator below its first level has no p#A.
	k2 := newKit(t)
	m2, _ := r12Deep(t, k2, false, field("n", types.I32))
	m2.validate(t)
	c2 := newChecker(m2.b, k2.env(m2.sig))
	c2.setup()
	if c2.params[1].nA != noRoot {
		t.Fatalf("Deep without an allocator field has a p#A")
	}
}

// The first-hop side of p#A: a loan read from an allocator field targets
// p#A, and every other tagged loan p#2. Both keep p.inner as their base, so
// the first level still holds them.
func TestAllocatorFieldLoanTargetsNode(t *testing.T) {
	k := newKit(t)
	m, _ := r12Deep(t, k, false, field("ar", k.tab.Ref(k.Arena)))
	m.validate(t)
	c := newChecker(m.b, k.env(m.sig))
	c.setup()
	deep := k.named("Deep")
	read := func(i uint32, ty types.TypeID) loan {
		p := c.paths.ofPlace(m.b, k.tab, m.pl(1, deref(deep), fld(i, ty)))
		s := c.tag(c.sets.of(uint32(c.params[1].inner)), p)
		ls := c.sets.get(s)
		if len(ls) != 1 {
			t.Fatalf("tag: %v", ls)
		}
		return c.loans[ls[0]]
	}
	ar := read(2, k.tab.Ref(k.Arena))
	if ar.target != c.params[1].nA || ar.base != c.params[1].inner || !ar.inner {
		t.Fatalf("self.ar's loan: target %d base %d; want p#A %d, base %d", ar.target, ar.base, c.params[1].nA, c.params[1].inner)
	}
	a := read(0, refI32(k))
	if a.target != c.params[1].n2 {
		t.Fatalf("self.a's loan: target %d; want p#2 %d", a.target, c.params[1].n2)
	}
}

// The second shape (it was accepted too): S { l: List[mut &i32], a: &i32 },
// G(l: mut List[mut &i32], v: &i32 into l) and S.M(mut self) { G(mut
// self.l, self.a) }. G may store v through an element of l into what the
// element refers to, outside self; p#2 stands for that, and is judged: E0604
// at the call, clean with `*self into self`.
func TestListOfMutRefs(t *testing.T) {
	for _, clause := range []bool{false, true} {
		k := newKit(t)
		ri := refI32(k)
		lt := k.named("List", k.tab.Mut(ri))
		g := k.fn("G", []types.Param{k.param("l", k.tab.Mut(lt)), k.param("v", ri)}, types.Void, types.EmptyErrors)
		g.Params[1].Into = 1
		k.structItem(k.main, "S", 0, false, false, field("l", lt), field("a", ri))
		st := k.named("S")
		s := k.method("S", "M", types.ByMut, st, nil, types.Void, types.EmptyErrors)
		if clause {
			s.Flows.Stores = []types.Store{{From: 1, Into: 1}}
		}
		m := k.body(s)
		tl := m.temp(k.tab.Mut(lt))
		m.set(tl, mutRef(m.pl(1, deref(st), fld(0, lt))))
		call := m.callv("G", nil, argMove(m.local(tl)), argCopy(m.pl(1, deref(st), fld(1, ri))))
		m.end(tl)
		m.ret(mir.ExitNormal)
		if clause {
			m.expect(t, m.check(t))
		} else {
			m.expect(t, m.check(t), diag.E0604, call)
		}
	}
}

// At the first level: F(var s: Deep) { Put(mut s.a, mut s.b) } with
// `ar: &Arena` in Deep. s is by value, so p#1 merges i32, &i32 and Arena;
// a node that merges an allocator with anything else is no allocator, so
// the walk from s reaches p#1 and judges it: E0604, as without the field.
func TestByValueParamMergedFirstLevel(t *testing.T) {
	for _, withArena := range []bool{false, true} {
		k := newKit(t)
		ri := refI32(k)
		mri := k.tab.Mut(ri)
		r12Put(k)
		fs := []fieldSpec{field("a", ri), field("b", mri)}
		if withArena {
			fs = append(fs, field("ar", k.tab.Ref(k.Arena)))
		}
		k.structItem(k.main, "Deep", 0, false, false, fs...)
		deep := k.named("Deep")
		s := k.fn("F", []types.Param{k.param("s", deep)}, types.Void, types.EmptyErrors)
		s.Params[0].Var = true
		m := k.body(s)
		ta, tb := m.temp(mri), m.temp(k.tab.Mut(mri))
		m.set(ta, mutRef(m.pl(1, fld(0, ri))))
		m.set(tb, mutRef(m.pl(1, fld(1, mri))))
		call := m.callv("Put", nil, argMove(m.local(ta)), argMove(m.local(tb)))
		m.end(tb, ta)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0604, call)
	}
}

// The guard on the other side: a loan read from an allocator field is not
// free to go anywhere. D { ar: &Arena, b: mut str } with D.Mix(mut self, s:
// str) { *self.b = DupA(self.ar, s) } writes an arena string into what b
// refers to, outside self; the arena may die first. E0604 at the write.
func TestAllocatorLoanIntoDeepNode(t *testing.T) {
	k := newKit(t)
	ra := k.tab.Ref(k.Arena)
	ms := k.tab.Mut(types.Str)
	dup := k.fn("DupA", []types.Param{k.param("a", ra), k.param("s", types.Str)}, types.Str, types.EmptyErrors)
	dup.Flows.Sources = []types.Source{{Param: 1}}
	k.structItem(k.main, "D", 0, false, false, field("ar", ra), field("b", ms))
	d := k.named("D")
	m := k.body(k.method("D", "Mix", types.ByMut, d, []types.Param{k.param("s", types.Str)}, types.Void, types.EmptyErrors))
	x := m.temp(types.Str)
	next := m.block()
	m.call(k.item("DupA"), nil, []mir.Arg{argCopy(m.pl(1, deref(d), fld(0, ra))), argCopy(m.local(2))}, m.local(x), next)
	m.at(next)
	w := m.assign(m.pl(1, deref(d), fld(1, ms), deref(types.Str)), use(mv(m.local(x))))
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0604, w)
}

// The first-hop rule kept (std/proc supervise.mk and std/core tests_map.mk): a
// local List made in a parameter's arena holds a loan of p#A, so a store into
// the list lands in the list and stops at the arena. Args(sys: &Sys) { var
// argv := NewL(sys.arena); PushL(mut argv, sys.nm); PushL(mut argv, sys.nm)
// } with Sys { nm: &str, arena: &Arena }: clean. The second push walks from
// argv to what it holds: p#A (an allocator, not the first hop: stop) and
// p#2 (str and u8, not writable, reached by a shared loan: no store).
func TestAllocatorFieldStaysWithNode(t *testing.T) {
	k := newKit(t)
	ra := k.tab.Ref(k.Arena)
	rs := k.tab.Ref(types.Str)
	lt := k.named("List", rs)
	nl := k.fn("NewL", []types.Param{k.param("a", ra)}, lt, types.EmptyErrors)
	nl.Flows.Sources = []types.Source{{Param: 1}}
	pl := k.fn("PushL", []types.Param{k.param("l", k.tab.Mut(lt)), k.param("x", rs)}, types.Void, types.EmptyErrors)
	pl.Params[1].Into = 1
	k.structItem(k.main, "Sys", 0, false, false, field("nm", rs), field("arena", ra))
	sys := k.named("Sys")
	m := k.body(k.fn("Args", []types.Param{k.param("sys", k.tab.Ref(sys))}, types.Void, types.EmptyErrors))
	argv := m.user("argv", lt, true)
	next := m.block()
	m.call(k.item("NewL"), nil, []mir.Arg{argCopy(m.pl(1, deref(sys), fld(1, ra)))}, m.local(argv), next)
	m.at(next)
	for i := 0; i < 2; i++ {
		tl := m.temp(k.tab.Mut(lt))
		m.set(tl, mutRef(m.local(argv)))
		m.callv("PushL", nil, argMove(m.local(tl)), argCopy(m.pl(1, deref(sys), fld(0, rs))))
		m.end(tl)
	}
	m.end(argv)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

// f1PutA declares PutA(a: &Arena, x: &i32 into a): a store into the arena
// itself, which counts when the argument holds the arena's loan directly
// (the first hop).
func f1PutA(k *kit) {
	s := k.fn("PutA", []types.Param{k.param("a", k.tab.Ref(k.Arena)), k.param("x", refI32(k))}, types.Void, types.EmptyErrors)
	s.Params[1].Into = 1
}

// The split loosens nothing: a loan of p.inner that was not read from an
// allocator field still reaches p#A on its first hop. H { w: W }, W { ar:
// &Arena, n: &i32 }, H.M(mut self, v: &i32) { w := self.w; PutA(w.ar, v) }:
// w holds self.w's loan, which targets p#2 (W is no allocator reference),
// yet w.ar refers to self's arena. E0604 at the call.
func TestLocalCopyReachesAllocator(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	ra := k.tab.Ref(k.Arena)
	f1PutA(k)
	k.structItem(k.main, "W", 0, false, false, field("ar", ra), field("n", ri))
	w := k.named("W")
	k.structItem(k.main, "H", 0, false, false, field("w", w))
	h := k.named("H")
	m := k.body(k.method("H", "M", types.ByMut, h, []types.Param{k.param("v", ri)}, types.Void, types.EmptyErrors))
	lw := m.user("w", w, false)
	m.set(lw, use(cp(m.pl(1, deref(h), fld(0, w)))))
	call := m.callv("PutA", nil, argCopy(m.pl(lw, fld(0, ra))), argCopy(m.local(2)))
	m.end(lw)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0604, call)
}

// The same through a result from *q: GetA(q: &Q): &Arena from *q, Keep(q:
// &Q, v: &i32) { r := GetA(q); PutA(r, v) }. r holds q.inner, untagged, so
// its target is q#2, and q#A is reached with it: E0604 at the store.
func TestResultFromBehindReachesAllocator(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	ra := k.tab.Ref(k.Arena)
	f1PutA(k)
	k.structItem(k.main, "Q", 0, false, false, field("ar", ra))
	q := k.named("Q")
	g := k.fn("GetA", []types.Param{k.param("q", k.tab.Ref(q))}, ra, types.EmptyErrors)
	g.Flows.Sources = []types.Source{{Param: 1, Behind: true}}
	m := k.body(k.fn("Keep", []types.Param{k.param("q", k.tab.Ref(q)), k.param("v", ri)}, types.Void, types.EmptyErrors))
	r := m.user("r", ra, false)
	next := m.block()
	m.call(k.item("GetA"), nil, []mir.Arg{argCopy(m.local(1))}, m.local(r), next)
	m.at(next)
	call := m.callv("PutA", nil, argCopy(m.local(r)), argCopy(m.local(2)))
	m.end(r)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0604, call)
}

// f1DeepAs is r12Deep with Deep declared a view struct when view is set.
func f1DeepAs(t *testing.T, k *kit, view, clause bool, extra ...fieldSpec) (*mb, mir.SpanID) {
	t.Helper()
	if !view {
		return r12Deep(t, k, clause, extra...)
	}
	ri := refI32(k)
	mri := k.tab.Mut(ri)
	r12Put(k)
	k.structItem(k.main, "Deep", 0, true, false, append([]fieldSpec{field("a", ri), field("b", mri)}, extra...)...)
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

// Every shape of the hole (several allocators, nested, optional arena, box,
// generic instance, view) is judged: E0604 at Put's call without `*self
// into self`, clean with it. Each was once accepted: an allocator
// anywhere below the first level made the merged p#2 an allocator node.
func TestShapes(t *testing.T) {
	cases := []struct {
		name  string
		view  bool
		extra func(k *kit) []fieldSpec
	}{
		{"multi", false, func(k *kit) []fieldSpec {
			return []fieldSpec{field("ar", k.tab.Ref(k.Arena)), field("h", k.tab.Ref(k.Heap))}
		}},
		{"nested", false, func(k *kit) []fieldSpec {
			k.structItem(k.main, "In", 0, false, false, field("l", k.named("List", types.I32)))
			return []fieldSpec{field("in", k.named("In"))}
		}},
		{"optarena", false, func(k *kit) []fieldSpec {
			return []fieldSpec{field("oa", k.tab.Optional(k.tab.Ref(k.Arena))),
				field("as", k.tab.Array(k.lenArg(2), k.tab.Ref(k.Alloc)))}
		}},
		{"box", false, func(k *kit) []fieldSpec {
			return []fieldSpec{field("bx", k.named("Box", k.Arena))}
		}},
		{"geninst", false, func(k *kit) []fieldSpec {
			k.structItem(k.main, "G", 1, false, false, field("x", k.tab.Param(0)), field("ar", k.tab.Ref(k.Arena)))
			return []fieldSpec{field("g", k.named("G", k.named("List", types.I32)))}
		}},
		{"view", true, func(k *kit) []fieldSpec {
			return []fieldSpec{field("ar", k.tab.Ref(k.Arena))}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, clause := range []bool{false, true} {
				k := newKit(t)
				m, call := f1DeepAs(t, k, tc.view, clause, tc.extra(k)...)
				if clause {
					m.expect(t, m.check(t))
				} else {
					m.expect(t, m.check(t), diag.E0604, call)
				}
			}
		})
	}
}

// f4Ctx declares struct Ctx { a: &Arena, second } (by value at the first
// level), NewL(a: &Arena): List[&i32] from a and PushL(l: mut List[&i32],
// x: &i32 into l).
func f4Ctx(k *kit, second fieldSpec) (ctx, lt types.TypeID) {
	ra := k.tab.Ref(k.Arena)
	lt = k.named("List", refI32(k))
	nl := k.fn("NewL", []types.Param{k.param("a", ra)}, lt, types.EmptyErrors)
	nl.Flows.Sources = []types.Source{{Param: 1}}
	pl := k.fn("PushL", []types.Param{k.param("l", k.tab.Mut(lt)), k.param("x", refI32(k))}, types.Void, types.EmptyErrors)
	pl.Params[1].Into = 1
	k.structItem(k.main, "Ctx", 0, false, false, field("a", ra), second)
	return k.named("Ctx"), lt
}

// The split at the first level. Keep(c: Ctx, x: &i32) { var l := NewL(c.a);
// PushL(mut l, x) } with Ctx { a: &Arena, name: str }: the List made in c's
// arena holds c.outerA, which targets p#1A, so the push lands in the list
// and, on the later hop, in the arena p#1A itself (the list's storage lives
// in the arena): E0604 for x, E0505 for &z, a local. When the walk stopped at
// the arena this was clean; with the allocators left in p#1 ({Arena, u8}, no
// allocator alone) it was E0604 too.
func TestByValueCtxListStaysWithList(t *testing.T) {
	for _, local := range []bool{false, true} {
		k := newKit(t)
		ctx, lt := f4Ctx(k, field("name", types.Str))
		m := k.body(k.fn("Keep", []types.Param{k.param("c", ctx), k.param("x", refI32(k))}, types.Void, types.EmptyErrors))
		l := m.user("l", lt, true)
		next := m.block()
		m.call(k.item("NewL"), nil, []mir.Arg{argCopy(m.pl(1, fld(0, k.tab.Ref(k.Arena))))}, m.local(l), next)
		m.at(next)
		x := m.local(2)
		var z mir.LocalID
		if local {
			z = m.user("z", types.I32, false)
			m.set(z, use(mir.Imm(types.I32, 1)))
			rz := m.temp(refI32(k))
			m.set(rz, ref(m.local(z)))
			x = m.local(rz)
		}
		tl := m.temp(k.tab.Mut(lt))
		m.set(tl, mutRef(m.local(l)))
		m.callv("PushL", nil, argMove(m.local(tl)), argCopy(x))
		m.end(tl)
		m.end(l)
		m.ret(mir.ExitNormal)
		if local {
			m.expect(t, m.check(t), diag.E0505, mir.SpanID(8))
		} else {
			m.expect(t, m.check(t), diag.E0604, mir.SpanID(8))
		}
	}
}

// The first-level node model: c: Ctx { a: &Arena, name: str } has p#1 {u8}, no
// allocator, not writable and no carrier, and p#1A {Arena}; a parameter whose p#1 is
// allocators alone (a: &Arena) or has none keeps one first-level node.
func TestFirstLevelAllocatorNode(t *testing.T) {
	k := newKit(t)
	ctx, _ := f4Ctx(k, field("name", types.Str))
	m := k.body(k.fn("F", []types.Param{k.param("c", ctx), k.param("a", k.tab.Ref(k.Arena)), k.param("p", k.Point)},
		types.Void, types.EmptyErrors))
	m.ret(mir.ExitNormal)
	m.validate(t)
	c := newChecker(m.b, k.env(m.sig))
	c.setup()
	pc := c.params[1]
	if pc.n1A == noRoot || pc.outerA == noLoan {
		t.Fatalf("c: Ctx: no p#1A")
	}
	if r := &c.roots[pc.n1]; r.allocator || r.writable || r.carrier {
		t.Fatalf("c#1: allocator %v writable %v carrier %v; want false, false, false (u8 holds nothing)", r.allocator, r.writable, r.carrier)
	}
	if r := &c.roots[pc.n1A]; !r.allocator || r.depth != 1 {
		t.Fatalf("c#1A: %+v", *r)
	}
	if lo := c.loans[pc.outerA]; lo.target != pc.n1A || lo.inner || !lo.abstract || lo.param != 1 {
		t.Fatalf("c.outerA: %+v", lo)
	}
	if pa := c.params[2]; pa.n1A != noRoot || !c.roots[pa.n1].allocator {
		t.Fatalf("a: &Arena: p#1 must stay one allocator node")
	}
}

// The first-level split loosens nothing: a store into the allocator itself
// still counts on the first hop, and a loan of p.outer that was not read from
// the allocator field still reaches p#1A. With Ctx { a: &Arena, n: &i32 }:
//   - direct: PutA(c.a, v);
//   - copy: w := c; PutA(w.a, v) (w holds c.outer, target p#1);
//   - result: r := GetA(c) (&Arena from c); PutA(r, v).
//
// Each is E0604 with a parameter v and E0505 with &z, as before the split
// (the first-level analogues of the local copy and getter shapes). The copy and
// result shapes are judged twice over: by allocOf's p#1 to p#1A step, and at
// p#2, which holds the Arena's view ANY (the layer-2 walk runs before the
// split). Of the kit's allocators only Arena is a carrier, so no unit test
// can tell the two apart.
func TestFirstLevelAllocatorStillJudged(t *testing.T) {
	for _, shape := range []string{"direct", "copy", "result"} {
		for _, local := range []bool{false, true} {
			k := newKit(t)
			ri := refI32(k)
			ra := k.tab.Ref(k.Arena)
			s := k.fn("PutA", []types.Param{k.param("a", ra), k.param("x", ri)}, types.Void, types.EmptyErrors)
			s.Params[1].Into = 1
			ctx, _ := f4Ctx(k, field("n", ri))
			g := k.fn("GetA", []types.Param{k.param("q", ctx)}, ra, types.EmptyErrors)
			g.Flows.Sources = []types.Source{{Param: 1}}
			m := k.body(k.fn("Keep", []types.Param{k.param("c", ctx), k.param("v", ri)}, types.Void, types.EmptyErrors))
			v := m.local(2)
			if local {
				z := m.user("z", types.I32, false)
				m.set(z, use(mir.Imm(types.I32, 1)))
				rz := m.temp(ri)
				m.set(rz, ref(m.local(z)))
				v = m.local(rz)
			}
			var arena mir.Place
			switch shape {
			case "direct":
				arena = m.pl(1, fld(0, ra))
			case "copy":
				w := m.user("w", ctx, false)
				m.set(w, use(cp(m.local(1))))
				arena = m.pl(w, fld(0, ra))
			case "result":
				r := m.user("r", ra, false)
				next := m.block()
				m.call(k.item("GetA"), nil, []mir.Arg{argCopy(m.local(1))}, m.local(r), next)
				m.at(next)
				arena = m.local(r)
			}
			call := m.callv("PutA", nil, argCopy(arena), argCopy(v))
			m.ret(mir.ExitNormal)
			got := m.check(t)
			if local {
				if !hasCode(got, diag.E0505) {
					t.Fatalf("%s with &z: %v; want E0505", shape, got.Diags)
				}
			} else {
				m.expect(t, got, diag.E0604, call)
			}
		}
	}
}

// hasCode reports whether r has a diagnostic with code c.
func hasCode(r *Result, c diag.Code) bool {
	for _, d := range r.Diags {
		if d.Code == c {
			return true
		}
	}
	return false
}
