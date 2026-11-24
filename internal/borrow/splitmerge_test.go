// Why: mut p (and mut self calls) on concrete non-view structs/records must
// preserve per-field loan sets across store-disjoint fields while still merging
// every transitive depth-1 and depth-2 store-compatible pair cleanly.
//
// What: unit and matrix tests for internal/borrow/splitmerge.go and
// collapseFor in internal/borrow/locals.go.
//
// How: exercises single-carrier + scalar, two disjoint refs, asymmetric
// str/slice vs struct, &Arena allocation into &Graph, same-referent inline
// structs, sub-field refs, &T vs &&T, &Cell[&Bar] under *s into s, plain view
// fields, &Interface fields, generic structs, and view structs.

package borrow

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

func callMut(m *mb, fn string, v mir.LocalID, vt types.TypeID, gargs ...types.TypeID) mir.SpanID {
	tmp := m.temp(m.k.tab.Mut(vt))
	m.set(tmp, mutRef(m.local(v)))
	s := m.callv(fn, gargs, argMove(m.local(tmp)))
	m.end(tmp)
	return s
}

func (k *kit) plainViewStruct(name string, generics int, fs ...fieldSpec) types.TypeID {
	id := k.structItem(k.main, name, generics, true, false, fs...)
	k.items.Get(id).Decl.(*types.StructDecl).PlainView = true
	return k.named(name)
}

func (k *kit) ifaceItem(name string) types.TypeID {
	id := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern(name), Kind: types.KindInterface, Exported: true,
		Key: types.ItemKey("main", name), Decl: &types.InterfaceDecl{}})
	k.byNm[name] = id
	return k.tab.Named(id, nil)
}

func TestSplitMerge_SingleCarrierWithScalar(t *testing.T) {
	k := newKit(t)
	k.structItem(k.main, "Parser", 0, false, false, field("src", types.Str), field("pos", types.Int))
	pt := k.named("Parser")
	k.method("Parser", "Advance", types.ByMut, pt, nil, types.Void, types.EmptyErrors)

	m := k.body(k.sig("Run", []types.Param{k.param("cond", types.Bool)}, types.Void, types.EmptyErrors))
	p := m.user("p", pt, true)
	long := m.user("long", types.Str, false)
	m.set(long, use(k.str()))
	m.set(p, m.agg(mir.AggStruct, k.item("Parser"), pt, cp(m.local(long)), imm(types.Int, 0)))

	thenB := m.block()
	joinB := m.block()
	m.branch(cp(m.local(1)), thenB, joinB)
	m.at(thenB)
	short := m.user("short", types.Str, false)
	m.set(short, use(k.str()))
	m.assign(m.pl(p, fld(0, types.Str)), use(cp(m.local(short))))
	callMut(m, "Parser.Advance", p, pt)
	m.end(short)
	m.jump(joinB)

	m.at(joinB)
	m.useRef(m.pl(p, fld(1, types.Int)), types.Int)
	m.end(p, long)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

func TestSplitMerge_DisjointRefsPreserveSplit(t *testing.T) {
	for _, dropScratch := range []bool{true, false} {
		k := newKit(t)
		k.structItem(k.main, "Graph", 0, false, false, field("nodes", types.Int))
		k.structItem(k.main, "ScratchBuf", 0, false, false, field("cap", types.U32))
		gt, st := k.named("Graph"), k.named("ScratchBuf")
		rg, rs := k.tab.Ref(gt), k.tab.Ref(st)
		k.structItem(k.main, "Pass", 0, false, false, field("graph", rg), field("scratch", rs), field("steps", types.Int))
		pt := k.named("Pass")
		k.method("Pass", "Advance", types.ByMut, pt, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("Run", nil, types.Void, types.EmptyErrors))
		g := m.user("g", gt, false)
		s := m.user("s", st, false)
		m.set(g, m.agg(mir.AggStruct, k.item("Graph"), gt, imm(types.Int, 1)))
		m.set(s, m.agg(mir.AggStruct, k.item("ScratchBuf"), st, imm(types.U32, 8)))
		r0 := m.temp(rg)
		r1 := m.temp(rs)
		m.set(r0, ref(m.local(g)))
		m.set(r1, ref(m.local(s)))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Pass"), pt, cp(m.local(r0)), cp(m.local(r1)), imm(types.Int, 0)))
		callMut(m, "Pass.Advance", p, pt)
		if dropScratch {
			m.end(r1, s)
			m.useRef(m.pl(p, fld(0, rg)), rg)
			m.useRef(m.pl(p, fld(2, types.Int)), types.Int)
			m.end(p, r0, g)
		} else {
			m.end(r0, g)
			m.useRef(m.pl(p, fld(1, rs)), rs)
			m.useRef(m.pl(p, fld(2, types.Int)), types.Int)
			m.end(p, r1, s)
		}
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	}
}

func TestSplitMerge_AsymmetricStrAndInterner(t *testing.T) {
	k := newKit(t)
	bytesT := k.tab.Slice(types.U8)
	k.structItem(k.main, "Interner", 0, false, false, field("buf", bytesT))
	it := k.named("Interner")
	ri := k.tab.Ref(it)
	k.structItem(k.main, "Parser", 0, false, false, field("src", types.Str), field("intern", ri), field("pos", types.Int))
	pt := k.named("Parser")
	k.method("Parser", "Step", types.ByMut, pt, nil, types.Void, types.EmptyErrors)

	t.Run("src ends before p.intern read is allowed", func(t *testing.T) {
		m := k.body(k.sig("RunGood", []types.Param{k.param("b", bytesT)}, types.Void, types.EmptyErrors))
		s := m.user("s", types.Str, false)
		m.set(s, use(k.str()))
		in := m.user("in", it, false)
		m.set(in, m.agg(mir.AggStruct, k.item("Interner"), it, cp(m.local(1))))
		rin := m.temp(ri)
		m.set(rin, ref(m.local(in)))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Parser"), pt, cp(m.local(s)), cp(m.local(rin)), imm(types.Int, 0)))
		callMut(m, "Parser.Step", p, pt)
		m.end(s)
		m.useRef(m.pl(p, fld(1, ri)), ri)
		m.end(p, rin, in)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})

	t.Run("intern ends before p.src read is rejected", func(t *testing.T) {
		m := k.body(k.sig("RunBad", []types.Param{k.param("b", bytesT)}, types.Void, types.EmptyErrors))
		s := m.user("s", types.Str, false)
		m.set(s, use(k.str()))
		in := m.user("in", it, false)
		m.set(in, m.agg(mir.AggStruct, k.item("Interner"), it, cp(m.local(1))))
		rin := m.temp(ri)
		bad := m.set(rin, ref(m.local(in)))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Parser"), pt, cp(m.local(s)), cp(m.local(rin)), imm(types.Int, 0)))
		callMut(m, "Parser.Step", p, pt)
		m.end(rin, in)
		m.useRef(m.pl(p, fld(0, types.Str)), types.Str)
		m.end(p, s)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})
}

func TestSplitMerge_ArenaIntoGraphRejected(t *testing.T) {
	k := newKit(t)
	k.structItem(k.main, "Graph", 0, false, false, field("nodes", types.Int))
	gt := k.named("Graph")
	rg, ra := k.tab.Ref(gt), k.tab.Ref(k.Arena)
	k.structItem(k.main, "Pass", 0, false, false, field("graph", rg), field("scratch", ra))
	pt := k.named("Pass")
	k.method("Pass", "Advance", types.ByMut, pt, nil, types.Void, types.EmptyErrors)
	k.fn("NewArena", nil, k.Arena, types.EmptyErrors)
	k.fn("FreeArena", []types.Param{k.param("a", k.Arena)}, types.Void, types.EmptyErrors)

	m := k.body(k.sig("Run", []types.Param{k.param("g", rg)}, types.Void, types.EmptyErrors))
	ar := m.user("ar", k.Arena, false)
	next := m.block()
	m.call(k.item("NewArena"), nil, nil, m.local(ar), next)
	m.at(next)
	r := m.temp(ra)
	bad := m.set(r, ref(m.local(ar)))
	p := m.user("p", pt, true)
	m.set(p, m.agg(mir.AggStruct, k.item("Pass"), pt, cp(m.local(1)), cp(m.local(r))))
	callMut(m, "Pass.Advance", p, pt)
	freeCall := m.callv("FreeArena", nil, argMove(m.local(ar)))
	m.end(r, ar)
	m.useRef(m.pl(p, fld(0, rg)), rg)
	m.end(p)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t), diag.E0503, freeCall, diag.E0505, bad)
}

func TestSplitMerge_CompatibleAndSubfieldTransfersRejected(t *testing.T) {
	t.Run("same referent inline A and B", func(t *testing.T) {
		k := newKit(t)
		k.structItem(k.main, "A", 0, false, false, field("s", types.Str))
		k.structItem(k.main, "B", 0, false, false, field("s", types.Str))
		at, bt := k.named("A"), k.named("B")
		k.structItem(k.main, "Pair", 0, false, false, field("a", at), field("b", bt))
		pt := k.named("Pair")
		k.method("Pair", "Mix", types.ByMut, pt, nil, types.Void, types.EmptyErrors)
		ru := k.tab.Ref(types.U8)
		mkStr := k.fn("MakeStr", []types.Param{k.param("r", ru)}, types.Str, types.EmptyErrors)
		mkStr.Flows.Sources = []types.Source{{Param: 1}}

		m := k.body(k.sig("Run", []types.Param{k.param("s0", types.Str)}, types.Void, types.EmptyErrors))
		b := m.user("b", types.U8, false)
		m.set(b, use(imm(types.U8, 1)))
		sb := m.temp(ru)
		bad := m.set(sb, ref(m.local(b)))
		bs := m.temp(types.Str)
		nxt := m.block()
		m.call(k.item("MakeStr"), nil, []mir.Arg{argCopy(m.local(sb))}, m.local(bs), nxt)
		m.at(nxt)
		a0 := m.temp(at)
		b0 := m.temp(bt)
		m.set(a0, m.agg(mir.AggStruct, k.item("A"), at, cp(m.local(1))))
		m.set(b0, m.agg(mir.AggStruct, k.item("B"), bt, cp(m.local(bs))))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Pair"), pt, mv(m.local(a0)), mv(m.local(b0))))
		m.end(b0, a0)
		callMut(m, "Pair.Mix", p, pt)
		m.end(bs, sb, b)
		m.useRef(m.pl(p, fld(0, at)), at)
		m.end(p)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("sub-field &str from &B", func(t *testing.T) {
		k := newKit(t)
		k.structItem(k.main, "B", 0, false, false, field("s", types.Str))
		bt := k.named("B")
		rs, rb := k.tab.Ref(types.Str), k.tab.Ref(bt)
		k.structItem(k.main, "Pair", 0, false, false, field("a", rs), field("b", rb))
		pt := k.named("Pair")
		k.method("Pair", "Mix", types.ByMut, pt, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("Run", []types.Param{k.param("ls", rs), k.param("s0", types.Str)}, types.Void, types.EmptyErrors))
		bx := m.user("bx", bt, false)
		m.set(bx, m.agg(mir.AggStruct, k.item("B"), bt, cp(m.local(2))))
		r := m.temp(rb)
		bad := m.set(r, ref(m.local(bx)))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Pair"), pt, cp(m.local(1)), cp(m.local(r))))
		callMut(m, "Pair.Mix", p, pt)
		m.end(r, bx)
		m.useRef(m.pl(p, fld(0, rs)), rs)
		m.end(p)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("&i32 and &&i32 merges p.a into p.b", func(t *testing.T) {
		k := newKit(t)
		ri := refI32(k)
		rri := k.tab.Ref(ri)
		k.structItem(k.main, "Two", 0, false, false, field("a", ri), field("b", rri))
		tt := k.named("Two")
		k.method("Two", "Mix", types.ByMut, tt, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("Run", []types.Param{k.param("lr", rri)}, types.Void, types.EmptyErrors))
		y := m.user("y", types.I32, false)
		m.set(y, use(imm(types.I32, 2)))
		ry := m.temp(ri)
		bad := m.set(ry, ref(m.local(y)))
		tvar := m.user("t", tt, true)
		m.set(tvar, m.agg(mir.AggStruct, k.item("Two"), tt, cp(m.local(ry)), cp(m.local(1))))
		callMut(m, "Two.Mix", tvar, tt)
		m.end(ry, y)
		m.useRef(m.pl(tvar, fld(1, rri)), rri)
		m.end(tvar)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("&Cell[&Bar] and &Bar merges b into cell", func(t *testing.T) {
		k := newKit(t)
		k.structItem(k.main, "Bar", 0, false, false, field("x", types.I32))
		bt := k.named("Bar")
		rb := k.tab.Ref(bt)
		ct := k.named("Cell", rb)
		rc := k.tab.Ref(ct)
		k.structItem(k.main, "Pair", 0, false, false, field("cell", rc), field("b", rb))
		pt := k.named("Pair")
		msig := k.method("Pair", "Mix", types.ByMut, pt, nil, types.Void, types.EmptyErrors)
		msig.Flows.Stores = []types.Store{{From: 1, Into: 1}}
		nc := k.fn("NewCell", []types.Param{k.param("v", rb)}, ct, types.EmptyErrors)
		nc.Flows.Sources = []types.Source{{Param: 1}}

		m := k.body(k.sig("Run", []types.Param{k.param("initB", rb)}, types.Void, types.EmptyErrors))
		cl := m.user("cl", ct, false)
		nxt := m.block()
		m.call(k.item("NewCell"), nil, []mir.Arg{argCopy(m.local(1))}, m.local(cl), nxt)
		m.at(nxt)
		rCl := m.temp(rc)
		m.set(rCl, ref(m.local(cl)))
		bar := m.user("bar", bt, false)
		m.set(bar, m.agg(mir.AggStruct, k.item("Bar"), bt, imm(types.I32, 7)))
		r := m.temp(rb)
		bad := m.set(r, ref(m.local(bar)))
		p := m.user("p", pt, true)
		m.set(p, m.agg(mir.AggStruct, k.item("Pair"), pt, cp(m.local(rCl)), cp(m.local(r))))
		callMut(m, "Pair.Mix", p, pt)
		m.end(r, bar)
		m.useRef(m.pl(p, fld(0, rc)), rc)
		m.end(p, rCl, cl)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})
}

func TestSplitMerge_PlainViewAndInterfaceAndGenerics(t *testing.T) {
	t.Run("PlainView merges other carrier into Sym while keeping other disjoint from Sym", func(t *testing.T) {
		k := newKit(t)
		sym := k.plainViewStruct("Sym", 0, field("id", types.U32))
		k.structItem(k.main, "Scratch", 0, false, false, field("n", types.U32))
		sc := k.named("Scratch")
		rsc := k.tab.Ref(sc)
		k.structItem(k.main, "State", 0, false, false, field("sym", sym), field("scratch", rsc))
		st := k.named("State")
		k.method("State", "Step", types.ByMut, st, nil, types.Void, types.EmptyErrors)

		// Ending Scratch while State.sym is live is rejected because Sym can borrow Scratch.
		m1 := k.body(k.sig("RunBad", []types.Param{k.param("ls", sym)}, types.Void, types.EmptyErrors))
		s := m1.user("s", sc, false)
		m1.set(s, m1.agg(mir.AggStruct, k.item("Scratch"), sc, imm(types.U32, 1)))
		rs := m1.temp(rsc)
		bad := m1.set(rs, ref(m1.local(s)))
		v := m1.user("v", st, true)
		m1.set(v, m1.agg(mir.AggStruct, k.item("State"), st, cp(m1.local(1)), cp(m1.local(rs))))
		callMut(m1, "State.Step", v, st)
		m1.end(rs, s)
		m1.useRef(m1.pl(v, fld(0, sym)), sym)
		m1.end(v)
		m1.ret(mir.ExitNormal)
		m1.expect(t, m1.check(t), diag.E0505, bad)

		// Ending Sym's backing loan while Scratch is live fails E0505: ReadScratch(&Sym)
		// may extract a &Scratch from a Sym whose backing storage is not in its fields.
		k.structItem(k.main, "Intern", 0, false, false, field("m", types.I32))
		in := k.named("Intern")
		rin := k.tab.Ref(in)
		mk := k.fn("MakeSym", []types.Param{k.param("i", rin)}, sym, types.EmptyErrors)
		mk.Flows.Sources = []types.Source{{Param: 1}}

		m2 := k.body(k.sig("RunBadViewToRef", []types.Param{k.param("lsc", rsc)}, types.Void, types.EmptyErrors))
		iv := m2.user("iv", in, false)
		m2.set(iv, m2.agg(mir.AggStruct, k.item("Intern"), in, imm(types.I32, 2)))
		ri := m2.temp(rin)
		bad2 := m2.set(ri, ref(m2.local(iv)))
		sval := m2.user("sval", sym, false)
		nxt := m2.block()
		m2.call(k.item("MakeSym"), nil, []mir.Arg{argCopy(m2.local(ri))}, m2.local(sval), nxt)
		m2.at(nxt)
		v2 := m2.user("v2", st, true)
		m2.set(v2, m2.agg(mir.AggStruct, k.item("State"), st, cp(m2.local(sval)), cp(m2.local(1))))
		callMut(m2, "State.Step", v2, st)
		m2.end(sval, ri, iv)
		m2.useRef(m2.pl(v2, fld(1, rsc)), rsc)
		m2.end(v2)
		m2.ret(mir.ExitNormal)
		m2.expect(t, m2.check(t), diag.E0505, bad2)

		// Plain view field beside non-carrier fields stays split across mut self.
		k.structItem(k.main, "Cursor", 0, false, false, field("sym", sym), field("pos", types.Int))
		ct := k.named("Cursor")
		k.method("Cursor", "Bump", types.ByMut, ct, nil, types.Void, types.EmptyErrors)
		m3 := k.body(k.sig("RunGoodScalar", nil, types.Void, types.EmptyErrors))
		iv3 := m3.user("iv", in, false)
		m3.set(iv3, m3.agg(mir.AggStruct, k.item("Intern"), in, imm(types.I32, 2)))
		ri3 := m3.temp(rin)
		m3.set(ri3, ref(m3.local(iv3)))
		s3 := m3.user("sval", sym, false)
		nxt3 := m3.block()
		m3.call(k.item("MakeSym"), nil, []mir.Arg{argCopy(m3.local(ri3))}, m3.local(s3), nxt3)
		m3.at(nxt3)
		cur := m3.user("c", ct, true)
		m3.set(cur, m3.agg(mir.AggStruct, k.item("Cursor"), ct, cp(m3.local(s3)), imm(types.Int, 0)))
		callMut(m3, "Cursor.Bump", cur, ct)
		m3.end(s3, ri3, iv3)
		pv := m3.temp(types.Int)
		m3.set(pv, use(cp(m3.pl(cur, fld(1, types.Int)))))
		m3.end(cur)
		m3.ret(mir.ExitNormal)
		m3.expect(t, m3.check(t))
	})

	t.Run("raw pointer field collapses source and dest directions", func(t *testing.T) {
		k := newKit(t)
		k.structItem(k.main, "Scratch", 0, false, false, field("n", types.I32))
		sc := k.named("Scratch")
		rsc := k.tab.Ref(sc)
		// Source direction: Table { ptr: *Scratch } can yield &Scratch from &self.
		k.structItem(k.main, "TblSrc", 0, false, false, field("ptr", k.tab.RawPtr(sc)))
		ts := k.named("TblSrc")
		rts := k.tab.Ref(ts)
		k.structItem(k.main, "StateSrc", 0, false, false, field("tbl", rts), field("out", rsc))
		sts := k.named("StateSrc")
		k.method("StateSrc", "Step", types.ByMut, sts, nil, types.Void, types.EmptyErrors)

		m1 := k.body(k.sig("RunSrc", []types.Param{k.param("sc0", rsc)}, types.Void, types.EmptyErrors))
		lv := m1.user("lv", ts, false)
		m1.set(lv, m1.agg(mir.AggStruct, k.item("TblSrc"), ts, imm(k.tab.RawPtr(sc), 0)))
		rt := m1.temp(rts)
		bad1 := m1.set(rt, ref(m1.local(lv)))
		s1 := m1.user("s", sts, true)
		m1.set(s1, m1.agg(mir.AggStruct, k.item("StateSrc"), sts, cp(m1.local(rt)), cp(m1.local(1))))
		callMut(m1, "StateSrc.Step", s1, sts)
		m1.end(rt, lv)
		m1.useRef(m1.pl(s1, fld(1, rsc)), rsc)
		m1.end(s1)
		m1.ret(mir.ExitNormal)
		m1.expect(t, m1.check(t), diag.E0505, bad1)

		// Dest direction: Table { ptr: *u8, tag: &u8 } can receive &i32 into its raw storage.
		ru8 := k.tab.Ref(types.U8)
		k.structItem(k.main, "TblDst", 0, false, false, field("ptr", k.tab.RawPtr(types.U8)), field("tag", ru8))
		td := k.named("TblDst")
		ri := refI32(k)
		k.structItem(k.main, "StateDst", 0, false, false, field("tbl", td), field("r", ri))
		stdT := k.named("StateDst")
		k.method("StateDst", "Step", types.ByMut, stdT, nil, types.Void, types.EmptyErrors)

		m2 := k.body(k.sig("RunDst", []types.Param{k.param("tu", ru8)}, types.Void, types.EmptyErrors))
		v := m2.user("v", types.I32, false)
		m2.set(v, use(imm(types.I32, 7)))
		rv := m2.temp(ri)
		bad2 := m2.set(rv, ref(m2.local(v)))
		tbl := m2.user("tbl", td, false)
		m2.set(tbl, m2.agg(mir.AggStruct, k.item("TblDst"), td, imm(k.tab.RawPtr(types.U8), 0), cp(m2.local(1))))
		s2 := m2.user("s", stdT, true)
		m2.set(s2, m2.agg(mir.AggStruct, k.item("StateDst"), stdT, mv(m2.local(tbl)), cp(m2.local(rv))))
		callMut(m2, "StateDst.Step", s2, stdT)
		m2.end(rv, v)
		m2.useRef(m2.pl(s2, fld(0, td)), td)
		m2.end(s2)
		m2.ret(mir.ExitNormal)
		m2.expect(t, m2.check(t), diag.E0505, bad2)
	})

	t.Run("&Interface field merges concrete carrier field", func(t *testing.T) {
		k := newKit(t)
		ifT := k.ifaceItem("Reader")
		rif := k.tab.Ref(ifT)
		k.structItem(k.main, "Impl", 0, false, false, field("n", types.I32))
		implT := k.named("Impl")
		rimpl := k.tab.Ref(implT)
		k.structItem(k.main, "Holder", 0, false, false, field("r", rif), field("impl", rimpl))
		ht := k.named("Holder")
		k.method("Holder", "Mix", types.ByMut, ht, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("Run", []types.Param{k.param("r0", rif)}, types.Void, types.EmptyErrors))
		im := m.user("im", implT, false)
		m.set(im, m.agg(mir.AggStruct, k.item("Impl"), implT, imm(types.I32, 5)))
		rm := m.temp(rimpl)
		bad := m.set(rm, ref(m.local(im)))
		h := m.user("h", ht, true)
		m.set(h, m.agg(mir.AggStruct, k.item("Holder"), ht, cp(m.local(1)), cp(m.local(rm))))
		callMut(m, "Holder.Mix", h, ht)
		m.end(rm, im)
		m.useRef(m.pl(h, fld(0, rif)), rif)
		m.end(h)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("generic struct still collapses whole on mut self", func(t *testing.T) {
		k := newKit(t)
		p0 := k.tab.Param(0)
		ri := refI32(k)
		k.structItem(k.main, "Box", 1, false, false, field("r", ri), field("x", p0))
		bt := k.named("Box", p0)
		k.genFn("BoxStep", []types.Param{k.param("b", k.tab.Mut(bt))}, types.Void)

		m := k.body(k.genFn("Run", []types.Param{k.param("x", p0)}, types.Void))
		m.b.GenericCount = 1
		n := m.user("n", types.I32, false)
		m.set(n, use(imm(types.I32, 1)))
		rn := m.temp(ri)
		bad := m.set(rn, ref(m.local(n)))
		bx := m.user("bx", bt, true)
		m.set(bx, m.agg(mir.AggStruct, k.item("Box"), bt, cp(m.local(rn)), mv(m.local(1))))
		callMut(m, "BoxStep", bx, bt, p0)
		m.end(rn, n)
		// Reading x after n ends is E0505 because Box[T] collapsed whole on mut BoxStep.
		m.useRef(m.pl(bx, fld(1, p0)), p0)
		m.callv("Take", []types.TypeID{bt}, argMove(m.local(bx)))
		m.end(bx)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("non-plain view struct still collapses whole on mut self", func(t *testing.T) {
		k := newKit(t)
		k.structItem(k.main, "Graph", 0, false, false, field("nodes", types.Int))
		k.structItem(k.main, "Scratch", 0, false, false, field("cap", types.U32))
		gt, st := k.named("Graph"), k.named("Scratch")
		rg, rs := k.tab.Ref(gt), k.tab.Ref(st)
		k.structItem(k.main, "PassView", 0, true, false, field("graph", rg), field("scratch", rs))
		vt := k.named("PassView")
		k.method("PassView", "Step", types.ByMut, vt, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("Run", []types.Param{k.param("g", rg)}, types.Void, types.EmptyErrors))
		sc := m.user("sc", st, false)
		m.set(sc, m.agg(mir.AggStruct, k.item("Scratch"), st, imm(types.U32, 4)))
		rsLocal := m.temp(rs)
		bad := m.set(rsLocal, ref(m.local(sc)))
		v := m.user("v", vt, true)
		m.set(v, m.agg(mir.AggStruct, k.item("PassView"), vt, cp(m.local(1)), cp(m.local(rsLocal))))
		callMut(m, "PassView.Step", v, vt)
		m.end(rsLocal, sc)
		m.useRef(m.pl(v, fld(0, rg)), rg)
		m.end(v)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})

	t.Run("SpinCore field merges mut element in splitMergeMatrix", func(t *testing.T) {
		k := newKit(t)
		ri := refI32(k)
		mi := k.tab.Mut(ri)
		scT := k.named("SpinCore", ri)
		rscT := k.tab.Ref(scT)
		k.structItem(k.main, "SpinState", 0, false, false, field("sc", rscT), field("slot", mi))
		stT := k.named("SpinState")
		k.method("SpinState", "Step", types.ByMut, stT, nil, types.Void, types.EmptyErrors)

		m := k.body(k.sig("SpinBad", nil, types.Void, types.EmptyErrors))
		long := m.user("long", types.I32, false)
		m.set(long, use(imm(types.I32, 1)))
		rlong := m.temp(ri)
		m.set(rlong, ref(m.local(long)))

		// Allocate scLong: SpinCore[&i32] referencing &long, and target: &i32 = &long.
		scLong := m.user("scLong", scT, true)
		m.set(scLong, m.agg(mir.AggStruct, k.item("SpinCore"), scT, imm(types.U32, 0), cp(m.local(rlong))))
		rScLong := m.temp(rscT)
		m.set(rScLong, ref(m.local(scLong)))

		target := m.user("target", ri, true)
		m.set(target, use(cp(m.local(rlong))))
		mTarget := m.temp(mi)
		m.set(mTarget, mutRef(m.local(target)))

		stVar := m.user("st", stT, true)
		m.set(stVar, m.agg(mir.AggStruct, k.item("SpinState"), stT, cp(m.local(rScLong)), mv(m.local(mTarget))))

		short := m.user("short", types.I32, false)
		m.set(short, use(imm(types.I32, 7)))
		rshort := m.temp(ri)
		m.set(rshort, ref(m.local(short)))
		scShort := m.user("scShort", scT, true)
		m.set(scShort, m.agg(mir.AggStruct, k.item("SpinCore"), scT, imm(types.U32, 0), cp(m.local(rshort))))
		rScShort := m.temp(rscT)
		bad := m.set(rScShort, ref(m.local(scShort)))
		m.assign(m.pl(stVar, fld(0, rscT)), use(cp(m.local(rScShort))))

		callMut(m, "SpinState.Step", stVar, stT)
		// Overwrite st.sc with long-lived rScLong so only st.slot retains &scShort via splitMergeMatrix.
		m.assign(m.pl(stVar, fld(0, rscT)), use(cp(m.local(rScLong))))
		m.end(rScShort, scShort, rshort, short)
		m.useRef(m.pl(stVar, fld(1, mi), deref(ri)), ri)
		m.end(stVar, mTarget, target, rScLong, scLong, rlong, long)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, bad)
	})
}
