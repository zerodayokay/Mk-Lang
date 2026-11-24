// Why: bundled mutable context structs (`cx: mut CallCx { heap: mut GcHeap,
// roots: &Roots, realm: &Realm }`) previously conflated inner loans on p#2
// across unrelated reference fields or polluted disjoint fields when storing
// through `mut *cx.heap`, and ignored `Source.Path` at call sites.
//
// What: unit tests verifying isolated depth-2 field tagging (`n2F`), `outerKeep`
// store isolation, `directWrite` field tagging, call-result field precision across
// split locals and abstract context parameters, and soundness negative bounds.
//
// How: constructs MIR bodies in `internal/borrow` covering positive multi-call
// and store-then-read patterns alongside negative aliasing, escape, clause, and
// double-mut checks.

package borrow

import (
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

func addPlainViewStruct(k *kit, name string, fs ...fieldSpec) types.ItemID {
	id := k.structItem(k.main, name, 0, true, false, fs...)
	k.items.Get(id).Decl.(*types.StructDecl).PlainView = true
	return id
}

func makeCallCxEnv(t *testing.T, inlineHeap bool, allocHeap bool) (*kit, types.TypeID, types.TypeID, types.TypeID, types.TypeID, types.TypeID) {
	t.Helper()
	k := newKit(t)
	i32 := types.I32
	if allocHeap {
		k.structItem(k.main, "GcHeap", 0, false, false, field("ar", k.tab.Ref(k.Arena)))
	} else {
		k.structItem(k.main, "GcHeap", 0, false, false, field("tag", i32))
	}
	k.structItem(k.main, "Roots", 0, false, false, field("n", i32))
	k.structItem(k.main, "Realm", 0, false, false, field("id", i32))
	k.structItem(k.main, "Extra", 0, false, false, field("x", i32))
	addPlainViewStruct(k, "LocalRef", field("idx", i32))

	gcHeap := k.named("GcHeap")
	roots := k.named("Roots")
	realm := k.named("Realm")
	lref := k.named("LocalRef")

	heapFieldType := k.tab.Mut(gcHeap)
	if inlineHeap {
		heapFieldType = gcHeap
	}
	k.structItem(k.main, "CallCx", 0, false, false,
		field("heap", heapFieldType),
		field("roots", k.tab.Ref(roots)),
		field("realm", k.tab.Ref(realm)),
	)
	cx := k.named("CallCx")

	allocSig := k.fn("Alloc", []types.Param{
		k.param("h", k.tab.Mut(gcHeap)),
		k.param("r", k.tab.Ref(roots)),
	}, lref, types.EmptyErrors)
	allocSig.Flows.Sources = []types.Source{{Param: 2}}

	return k, cx, gcHeap, roots, realm, lref
}

func TestBundledCtx_MutHeapAndRootsTwice(t *testing.T) {
	for _, allocHeap := range []bool{false, true} {
		k, cx, gcHeap, roots, _, lref := makeCallCxEnv(t, false, allocHeap)
		rRoots := k.tab.Ref(roots)
		mHeap := k.tab.Mut(gcHeap)

		useLocal := k.fn("UseLocal", []types.Param{k.param("v", lref)}, types.Void, types.EmptyErrors)
		_ = useLocal

		sig := k.fn("Run", []types.Param{k.param("cx", k.tab.Mut(cx))}, types.Void, types.EmptyErrors)
		m := k.body(sig)

		a := m.user("a", lref, false)
		b := m.user("b", lref, false)

		h1 := m.temp(mHeap)
		m.set(h1, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		b1 := m.block()
		m.call(k.item("Alloc"), nil, []mir.Arg{
			argMove(m.local(h1)),
			argCopy(m.pl(1, deref(cx), fld(1, rRoots))),
		}, m.local(a), b1)
		m.at(b1)
		m.end(h1)

		h2 := m.temp(mHeap)
		m.set(h2, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		b2 := m.block()
		m.call(k.item("Alloc"), nil, []mir.Arg{
			argMove(m.local(h2)),
			argCopy(m.pl(1, deref(cx), fld(1, rRoots))),
		}, m.local(b), b2)
		m.at(b2)
		m.end(h2)

		m.callv("UseLocal", nil, argCopy(m.local(a)))
		m.callv("UseLocal", nil, argCopy(m.local(b)))
		m.end(b, a)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	}
}

func TestBundledCtx_InlineHeapAndRootsTwice(t *testing.T) {
	k, cx, gcHeap, roots, _, lref := makeCallCxEnv(t, true, false)
	rRoots := k.tab.Ref(roots)
	mHeap := k.tab.Mut(gcHeap)

	k.fn("UseLocal", []types.Param{k.param("v", lref)}, types.Void, types.EmptyErrors)

	sig := k.fn("RunInline", []types.Param{k.param("cx", k.tab.Mut(cx))}, types.Void, types.EmptyErrors)
	m := k.body(sig)

	a := m.user("a", lref, false)
	b := m.user("b", lref, false)

	h1 := m.temp(mHeap)
	m.set(h1, mutRef(m.pl(1, deref(cx), fld(0, gcHeap))))
	b1 := m.block()
	m.call(k.item("Alloc"), nil, []mir.Arg{
		argMove(m.local(h1)),
		argCopy(m.pl(1, deref(cx), fld(1, rRoots))),
	}, m.local(a), b1)
	m.at(b1)
	m.end(h1)

	h2 := m.temp(mHeap)
	m.set(h2, mutRef(m.pl(1, deref(cx), fld(0, gcHeap))))
	b2 := m.block()
	m.call(k.item("Alloc"), nil, []mir.Arg{
		argMove(m.local(h2)),
		argCopy(m.pl(1, deref(cx), fld(1, rRoots))),
	}, m.local(b), b2)
	m.at(b2)
	m.end(h2)

	m.callv("UseLocal", nil, argCopy(m.local(a)))
	m.callv("UseLocal", nil, argCopy(m.local(b)))
	m.end(b, a)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

func TestBundledCtx_StoreIntoHeapDoesNotPoisonRealm(t *testing.T) {
	k := newKit(t)
	i32 := types.I32
	k.structItem(k.main, "Extra", 0, false, false, field("x", i32))
	extra := k.named("Extra")
	rExtra := k.tab.Ref(extra)
	k.structItem(k.main, "GcHeap", 0, false, false, field("slot", rExtra))
	k.structItem(k.main, "Roots", 0, false, false, field("n", i32))
	k.structItem(k.main, "Realm", 0, false, false, field("id", i32))
	gcHeap := k.named("GcHeap")
	roots := k.named("Roots")
	realm := k.named("Realm")
	k.structItem(k.main, "CallCx", 0, false, false,
		field("heap", k.tab.Mut(gcHeap)),
		field("roots", k.tab.Ref(roots)),
		field("realm", k.tab.Ref(realm)),
	)
	cx := k.named("CallCx")
	rRealm := k.tab.Ref(realm)
	mHeap := k.tab.Mut(gcHeap)

	// MutGcHeap actually takes a Store(h: mut GcHeap, x: &Extra) x into h.
	storeSig := k.fn("Store", []types.Param{k.param("h", mHeap), k.param("x", rExtra)}, types.Void, types.EmptyErrors)
	storeSig.Params[1].Into = 1

	getRealm := k.fn("GetRealm", []types.Param{k.param("r", rRealm)}, k.tab.Ref(i32), types.EmptyErrors)
	getRealm.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 0, Type: k.tab.Ref(i32)}}}}

	// Store(mut *cx.heap, extra) with extra into cx, then return cx.realm from cx.realm.
	sig := k.fn("StoreAndRealm", []types.Param{
		k.param("cx", k.tab.Mut(cx)),
		k.param("ex", rExtra),
	}, rRealm, types.EmptyErrors)
	sig.Params[1].Into = 1
	sig.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 2, Type: rRealm}}}}

	m := k.body(sig)
	h := m.temp(mHeap)
	m.set(h, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
	m.callv("Store", nil, argMove(m.local(h)), argCopy(m.local(2)))
	m.end(h)
	m.set(0, use(cp(m.pl(1, deref(cx), fld(2, rRealm)))))
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))

	// Also verify reading through a helper GetRealm(cx.realm) works cleanly under outerKeep.
	sig2 := k.fn("StoreAndGetRealmID", []types.Param{
		k.param("cx", k.tab.Mut(cx)),
		k.param("ex", rExtra),
	}, k.tab.Ref(i32), types.EmptyErrors)
	sig2.Params[1].Into = 1
	sig2.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 2, Type: rRealm}}}}

	m2 := k.body(sig2)
	h2 := m2.temp(mHeap)
	m2.set(h2, mutRef(m2.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
	m2.callv("Store", nil, argMove(m2.local(h2)), argCopy(m2.local(2)))
	m2.end(h2)
	bNext := m2.block()
	m2.call(k.item("GetRealm"), nil, []mir.Arg{argCopy(m2.pl(1, deref(cx), fld(2, rRealm)))}, mir.LocalPlace(0), bNext)
	m2.at(bNext)
	m2.ret(mir.ExitNormal)
	m2.expect(t, m2.check(t))
}

func TestBundledCtx_LocalSplitBehindSourceTwice(t *testing.T) {
	k, cx, gcHeap, roots, realm, lref := makeCallCxEnv(t, true, false)
	rRoots := k.tab.Ref(roots)
	rRealm := k.tab.Ref(realm)
	mCx := k.tab.Mut(cx)

	allocCx := k.fn("AllocFromCx", []types.Param{k.param("c", mCx)}, lref, types.EmptyErrors)
	allocCx.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 1, Type: rRoots}}}}
	k.fn("UseLocal", []types.Param{k.param("v", lref)}, types.Void, types.EmptyErrors)

	sig := k.fn("Caller", nil, types.Void, types.EmptyErrors)
	m := k.body(sig)
	hl := m.user("h", gcHeap, true)
	rl := m.user("r", roots, false)
	rlm := m.user("rlm", realm, false)
	cxl := m.user("cx", cx, true)
	rr := m.temp(rRoots)
	rrl := m.temp(rRealm)
	a := m.user("a", lref, false)
	b := m.user("b", lref, false)

	m.set(hl, m.agg(mir.AggStruct, k.item("GcHeap"), gcHeap, imm(types.I32, 0)))
	m.set(rl, m.agg(mir.AggStruct, k.item("Roots"), roots, imm(types.I32, 1)))
	m.set(rlm, m.agg(mir.AggStruct, k.item("Realm"), realm, imm(types.I32, 2)))
	m.set(rr, ref(m.local(rl)))
	m.set(rrl, ref(m.local(rlm)))
	m.set(cxl, m.agg(mir.AggStruct, k.item("CallCx"), cx, mv(m.local(hl)), mv(m.local(rr)), mv(m.local(rrl))))
	m.end(rrl, rr)

	c1 := m.temp(mCx)
	m.set(c1, mutRef(m.local(cxl)))
	b1 := m.block()
	m.call(k.item("AllocFromCx"), nil, []mir.Arg{argMove(m.local(c1))}, m.local(a), b1)
	m.at(b1)
	m.end(c1)

	c2 := m.temp(mCx)
	m.set(c2, mutRef(m.local(cxl)))
	b2 := m.block()
	m.call(k.item("AllocFromCx"), nil, []mir.Arg{argMove(m.local(c2))}, m.local(b), b2)
	m.at(b2)
	m.end(c2)

	m.callv("UseLocal", nil, argCopy(m.local(a)))
	m.callv("UseLocal", nil, argCopy(m.local(b)))
	m.end(b, a, cxl, rlm, rl, hl)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

func TestBundledCtx_SoundnessNegatives(t *testing.T) {
	t.Run("AliasCx store into a leaks to reading b", func(t *testing.T) {
		k := newKit(t)
		ri := k.tab.Ref(types.I32)
		cell := k.named("Cell", ri)
		rCell := k.tab.Ref(cell)
		k.structItem(k.main, "AliasCx", 0, false, false, field("a", rCell), field("b", rCell))
		aliasCx := k.named("AliasCx")
		put := k.fn("PutCell", []types.Param{k.param("c", rCell), k.param("v", ri)}, types.Void, types.EmptyErrors)
		put.Params[1].Into = 1
		read := k.fn("ReadCell", []types.Param{k.param("c", rCell)}, ri, types.EmptyErrors)
		read.Flows.Sources = []types.Source{{Param: 1, Behind: true}}

		sig := k.fn("Bad", []types.Param{k.param("cx", k.tab.Ref(aliasCx)), k.param("ex", ri)}, ri, types.EmptyErrors)
		sig.Params[1].Into = 1
		sig.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 1, Type: rCell}}}}
		m := k.body(sig)
		m.callv("PutCell", nil, argCopy(m.pl(1, deref(aliasCx), fld(0, rCell))), argCopy(m.local(2)))
		next := m.block()
		callSp := m.call(k.item("ReadCell"), nil, []mir.Arg{argCopy(m.pl(1, deref(aliasCx), fld(1, rCell)))}, mir.LocalPlace(0), next)
		m.at(next)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0602, callSp)
	})

	t.Run("returning cx.realm under from cx.roots is E0602", func(t *testing.T) {
		k, cx, _, roots, realm, _ := makeCallCxEnv(t, false, false)
		rRoots := k.tab.Ref(roots)
		rRealm := k.tab.Ref(realm)
		sig := k.fn("BadRealm", []types.Param{k.param("cx", k.tab.Ref(cx))}, rRealm, types.EmptyErrors)
		sig.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 1, Type: rRoots}}}}
		m := k.body(sig)
		retSp := m.set(0, use(cp(m.pl(1, deref(cx), fld(2, rRealm)))))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0602, retSp)
	})

	t.Run("returning &*cx.heap under from cx.roots is E0602", func(t *testing.T) {
		k, cx, gcHeap, roots, _, _ := makeCallCxEnv(t, false, false)
		rRoots := k.tab.Ref(roots)
		rHeap := k.tab.Ref(gcHeap)
		mHeap := k.tab.Mut(gcHeap)
		sig := k.fn("BadHeap", []types.Param{k.param("cx", k.tab.Mut(cx))}, rHeap, types.EmptyErrors)
		sig.Flows.Sources = []types.Source{{Param: 1, Behind: true, Path: []types.FieldStep{{Field: 1, Type: rRoots}}}}
		m := k.body(sig)
		retSp := m.set(0, ref(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0602, retSp)
	})

	t.Run("undeclared store of cx.roots into mut *cx.heap is E0604", func(t *testing.T) {
		k := newKit(t)
		i32 := types.I32
		k.structItem(k.main, "Roots", 0, false, false, field("n", i32))
		roots := k.named("Roots")
		rRoots := k.tab.Ref(roots)
		k.structItem(k.main, "GcHeap", 0, false, false, field("slot", rRoots))
		k.structItem(k.main, "Realm", 0, false, false, field("id", i32))
		gcHeap := k.named("GcHeap")
		realm := k.named("Realm")
		k.structItem(k.main, "CallCx", 0, false, false,
			field("heap", k.tab.Mut(gcHeap)),
			field("roots", rRoots),
			field("realm", k.tab.Ref(realm)),
		)
		cx := k.named("CallCx")
		mHeap := k.tab.Mut(gcHeap)
		storeR := k.fn("StoreR", []types.Param{k.param("h", mHeap), k.param("r", rRoots)}, types.Void, types.EmptyErrors)
		storeR.Params[1].Into = 1

		sig := k.fn("Undeclared", []types.Param{k.param("cx", k.tab.Mut(cx))}, types.Void, types.EmptyErrors)
		m := k.body(sig)
		h := m.temp(mHeap)
		m.set(h, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		callSp := m.callv("StoreR", nil, argMove(m.local(h)), argCopy(m.pl(1, deref(cx), fld(1, rRoots))))
		m.end(h)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0604, callSp)
	})

	t.Run("double mut *cx.heap is E0501", func(t *testing.T) {
		k, cx, gcHeap, _, _, _ := makeCallCxEnv(t, false, false)
		mHeap := k.tab.Mut(gcHeap)
		pair := k.fn("Pair", []types.Param{k.param("a", mHeap), k.param("b", mHeap)}, types.Void, types.EmptyErrors)
		_ = pair

		sig := k.fn("Double", []types.Param{k.param("cx", k.tab.Mut(cx))}, types.Void, types.EmptyErrors)
		m := k.body(sig)
		h1 := m.temp(mHeap)
		h2 := m.temp(mHeap)
		m.set(h1, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		dupSp := m.set(h2, mutRef(m.pl(1, deref(cx), fld(0, mHeap), deref(gcHeap))))
		m.callv("Pair", nil, argMove(m.local(h1)), argMove(m.local(h2)))
		m.end(h2, h1)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0501, dupSp)
	})

	t.Run("local escaping into mut *cx.heap fails scope end on local cx", func(t *testing.T) {
		k := newKit(t)
		i32 := types.I32
		k.structItem(k.main, "Extra", 0, false, false, field("x", i32))
		extra := k.named("Extra")
		rExtra := k.tab.Ref(extra)
		k.structItem(k.main, "GcHeap", 0, false, false, field("slot", rExtra))
		k.structItem(k.main, "Roots", 0, false, false, field("n", i32))
		k.structItem(k.main, "Realm", 0, false, false, field("id", i32))
		gcHeap := k.named("GcHeap")
		roots := k.named("Roots")
		realm := k.named("Realm")
		k.structItem(k.main, "CallCx", 0, false, false,
			field("heap", k.tab.Mut(gcHeap)),
			field("roots", k.tab.Ref(roots)),
			field("realm", k.tab.Ref(realm)),
		)
		cx := k.named("CallCx")
		rRoots := k.tab.Ref(roots)
		rRealm := k.tab.Ref(realm)
		mHeap := k.tab.Mut(gcHeap)
		storeSig := k.fn("Store", []types.Param{k.param("h", mHeap), k.param("x", rExtra)}, types.Void, types.EmptyErrors)
		storeSig.Params[1].Into = 1
		k.fn("UseH", []types.Param{k.param("h", k.tab.Ref(gcHeap))}, types.Void, types.EmptyErrors)

		sig := k.fn("EscapeLocal", nil, types.Void, types.EmptyErrors)
		m := k.body(sig)
		hl := m.user("h", gcHeap, true)
		rl := m.user("r", roots, false)
		rlm := m.user("rlm", realm, false)
		cxl := m.user("cx", cx, true)
		mh := m.temp(mHeap)
		rr := m.temp(rRoots)
		rrl := m.temp(rRealm)
		loc := m.user("loc", extra, false)
		rloc := m.temp(rExtra)
		glob := m.user("glob", extra, false)
		rglob := m.temp(rExtra)

		m.set(glob, m.agg(mir.AggStruct, k.item("Extra"), extra, imm(i32, 1)))
		m.set(rglob, ref(m.local(glob)))
		m.set(hl, m.agg(mir.AggStruct, k.item("GcHeap"), gcHeap, cp(m.local(rglob))))
		m.end(rglob)
		m.set(rl, m.agg(mir.AggStruct, k.item("Roots"), roots, imm(i32, 1)))
		m.set(rlm, m.agg(mir.AggStruct, k.item("Realm"), realm, imm(i32, 2)))
		m.set(mh, mutRef(m.local(hl)))
		m.set(rr, ref(m.local(rl)))
		m.set(rrl, ref(m.local(rlm)))
		m.set(cxl, m.agg(mir.AggStruct, k.item("CallCx"), cx, mv(m.local(mh)), mv(m.local(rr)), mv(m.local(rrl))))
		m.end(rrl, rr, mh)

		m.set(loc, m.agg(mir.AggStruct, k.item("Extra"), extra, imm(i32, 7)))
		borrowSp := m.set(rloc, ref(m.local(loc)))
		ht := m.temp(mHeap)
		m.set(ht, mutRef(m.pl(cxl, fld(0, mHeap), deref(gcHeap))))
		m.callv("Store", nil, argMove(m.local(ht)), argCopy(m.local(rloc)))
		m.end(rloc, ht)
		m.end(loc)

		useH := m.temp(k.tab.Ref(gcHeap))
		m.set(useH, ref(m.pl(cxl, fld(0, mHeap), deref(gcHeap))))
		m.callv("UseH", nil, argCopy(m.local(useH)))
		m.end(useH, cxl, rlm, rl, hl, glob)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0505, borrowSp)
	})
}
