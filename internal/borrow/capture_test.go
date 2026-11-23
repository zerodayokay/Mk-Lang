// Why: a capturing literal reaches its captures through a hidden environment
// parameter, and the loan rules must treat that parameter specially.
// What: tests that at the call an environment argument is a read with no
// flow whose loans are the call's statement temporaries; that in a capturing
// literal's body a result or stored loan on a capture is E0609; and that a
// write, mut borrow or move of a capture's place is E0609 (the backstop),
// never E0602, E0604, E0507 or E0409.
// How: hand-built MIR in the shapes lower emits, with the capKit builders
// env, buildEnv, capturePlace and literalBody.

package borrow

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/types"
)

// capKit adds an environment of the named captures of the given types.
type capKit struct {
	*kit
}

func (k capKit) env(names []string, ts []types.TypeID) (rec, ref types.TypeID) {
	var fs []types.RecordField
	for i, n := range names {
		fs = append(fs, types.RecordField{Name: k.in.Intern(n), Type: k.tab.Ref(ts[i])})
	}
	rec = k.tab.Record(fs)
	return rec, k.tab.Ref(rec)
}

// fnConst is a fn constant naming item (borrow does not follow it).
func (k capKit) fnConst(t types.TypeID, item types.ItemID) mir.Operand {
	c := k.tab.Consts().Intern(types.ConstValue{Type: t, Align: 8, Bytes: make([]byte, 8),
		Relocs: []types.Reloc{{Offset: 0, Kind: types.RelocFn, Fn: types.FnRef{Item: item}}}})
	return mir.Const(t, c)
}

// buildEnv is lower's pass 3 for sources srcs: t_i := &src_i, e := record,
// r := &e; it returns the argument and the temporaries in creation order.
func (k capKit) buildEnv(m *mb, rec, ref types.TypeID, srcs ...mir.Place) (mir.Arg, []mir.LocalID) {
	fs := k.tab.Get(rec).Fields
	var temps []mir.LocalID
	var ops []mir.Operand
	for i, p := range srcs {
		t := m.temp(fs[i].Type)
		m.set(t, mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: p, Type: fs[i].Type})
		temps = append(temps, t)
		ops = append(ops, cp(m.local(t)))
	}
	e := m.temp(rec)
	m.set(e, m.agg(mir.AggRecord, types.NoItem, rec, ops...))
	r := m.temp(ref)
	m.set(r, mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: m.local(e), Type: ref})
	temps = append(temps, e, r)
	return argCopy(m.local(r)), temps
}

// capturePlace is *((*env).field i) of a literal body's hidden parameter env.
func (k capKit) capturePlace(m *mb, env mir.LocalID, rec types.TypeID, i uint32) mir.Place {
	ft := k.tab.Get(rec).Fields[i].Type
	return m.pl(env, deref(rec), fld(i, ft), deref(k.tab.Get(ft).Elem))
}

// TestCaptureConflictsAtCall: a capture of ys beside the receiver
// reborrow of xs is clean, and so is a capture read beside &n; a capture of xs
// beside the reborrow of xs is E0501 at the call, as xs.Sort(fn(..) { return
// xs.Len() > 1 }) is.
func TestCaptureConflictsAtCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  mir.LocalID // the captured parameter: 1 is xs, 2 is ys
		bad  bool
	}{
		{"capture-other", 2, false},
		{"capture-receiver", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := capKit{newKit(t)}
			ms := k.tab.MutSlice(types.I32)
			less := k.tab.Fn(types.FnType{Params: []types.FnParam{{Type: k.tab.Ref(types.I32)}, {Type: k.tab.Ref(types.I32)}}, Result: types.Bool})
			sort := k.fn("Sort", []types.Param{k.param("xs", ms), {Name: k.in.Intern("less"), Type: less, Const: true}}, types.Void, types.EmptyErrors)
			m := k.body(k.sig("F", []types.Param{k.param("xs", ms), k.param("ys", ms)}, types.Void, types.EmptyErrors))
			rec, ref := k.env([]string{"c"}, []types.TypeID{ms})
			envArg, temps := k.buildEnv(m, rec, ref, m.local(tc.src))
			next := m.block()
			at := m.call(sort.Item, nil, []mir.Arg{argReborrow(m.local(1)), {Operand: k.fnConst(less, sort.Item), Mode: mir.ArgCopy}, envArg}, m.local(0), next)
			m.at(next)
			m.end(temps[len(temps)-1], temps[len(temps)-2], temps[0])
			m.ret(mir.ExitNormal)
			r := m.check(t)
			if tc.bad {
				m.expect(t, r, diag.E0501, at)
			} else {
				m.expect(t, r)
			}
		})
	}
	// A capture read beside &n: two shared loans, clean.
	k := capKit{newKit(t)}
	ri := k.tab.Ref(types.Int)
	f1 := k.tab.Fn(types.FnType{Params: []types.FnParam{{Type: ri}}, Result: types.Bool})
	look := k.fn("Look", []types.Param{k.param("x", ri), {Name: k.in.Intern("f"), Type: f1, Const: true}}, types.Bool, types.EmptyErrors)
	m := k.body(k.sig("G", []types.Param{k.param("k", types.Int)}, types.Bool, types.EmptyErrors))
	n := m.user("n", types.Int, true)
	m.set(n, use(cp(m.local(1))))
	x := m.temp(ri)
	m.set(x, ref(m.local(n)))
	rec, rf := k.env([]string{"n"}, []types.TypeID{types.Int})
	envArg, temps := k.buildEnv(m, rec, rf, m.local(n))
	next := m.block()
	m.call(look.Item, nil, []mir.Arg{argCopy(m.local(x)), {Operand: k.fnConst(f1, look.Item), Mode: mir.ArgCopy}, envArg}, m.local(0), next)
	m.at(next)
	m.end(temps[2], temps[1], temps[0], x)
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

// TestEnvLoansEndWithStatement: after the call and the end of its
// temporaries, mut n and a write of n are accepted.
func TestEnvLoansEndWithStatement(t *testing.T) {
	k := capKit{newKit(t)}
	ri := k.tab.Ref(types.Int)
	f1 := k.tab.Fn(types.FnType{Params: []types.FnParam{{Type: ri}}, Result: types.Bool})
	each := k.fn("Each", []types.Param{{Name: k.in.Intern("f"), Type: f1, Const: true}}, types.Bool, types.EmptyErrors)
	m := k.body(k.sig("G", []types.Param{k.param("k", types.Int)}, types.Void, types.EmptyErrors))
	n := m.user("n", types.Int, true)
	m.set(n, use(cp(m.local(1))))
	rec, rf := k.env([]string{"n"}, []types.TypeID{types.Int})
	envArg, temps := k.buildEnv(m, rec, rf, m.local(n))
	res := m.temp(types.Bool)
	next := m.block()
	m.call(each.Item, nil, []mir.Arg{{Operand: k.fnConst(f1, each.Item), Mode: mir.ArgCopy}, envArg}, m.local(res), next)
	m.at(next)
	m.end(res, temps[2], temps[1], temps[0])
	p := m.user("p", k.tab.Mut(types.Int), false)
	m.set(p, mutRef(m.local(n)))
	m.assign(m.pl(p, deref(types.Int)), use(imm(types.Int, 2)))
	m.end(p)
	m.set(n, use(imm(types.Int, 3)))
	m.ret(mir.ExitNormal)
	m.expect(t, m.check(t))
}

// literalBody starts a capturing literal's body: its declared parameters,
// then the hidden environment parameter; it returns the builder and
// the hidden local.
func (k capKit) literalBody(name string, ps []types.Param, result types.TypeID, envRef types.TypeID) (*mb, mir.LocalID) {
	m := k.body(k.sig(name, ps, result, types.EmptyErrors))
	env := m.b.AddParam(envRef, k.in.Intern("env"), m.sp())
	return m, env
}

// TestE0609_ResultLoan: a literal returning a captured str, or
// &x of a capture, or a slice of a captured slice, is E0609 at the return;
// returning a bool computed from a capture, or a loan from its own
// parameter that its signature names, is clean.
func TestE0609_ResultLoan(t *testing.T) {
	ri := func(k capKit) types.TypeID { return k.tab.Ref(types.Int) }
	t.Run("str", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"prefix"}, []types.TypeID{types.Str})
		m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, types.Str, rf)
		at := m.set(0, use(cp(k.capturePlace(m, env, rec, 0))))
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "result-loan" || factText(d, "captured") != "[prefix]" || d.Phase != diag.PhaseFlow {
			t.Fatalf("E0609 facts: reason %q captured %q phase %v", factText(d, "reason"), factText(d, "captured"), d.Phase)
		}
	})
	t.Run("ref-of-capture", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"k"}, []types.TypeID{types.Int})
		m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, ri(k), rf)
		at := m.set(0, ref(k.capturePlace(m, env, rec, 0)))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0609, at)
	})
	t.Run("slice-of-capture", func(t *testing.T) {
		k := capKit{newKit(t)}
		sl := k.tab.Slice(types.Int)
		rec, rf := k.env([]string{"xs"}, []types.TypeID{sl})
		m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, sl, rf)
		at := m.set(0, mir.Rvalue{Kind: mir.RvSlice, Borrow: mir.Shared, Place: k.capturePlace(m, env, rec, 0), Type: sl})
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t), diag.E0609, at)
	})
	t.Run("bool", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"k"}, []types.TypeID{types.Int})
		m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, types.Bool, rf)
		v := m.temp(types.Int)
		m.set(v, use(cp(k.capturePlace(m, env, rec, 0))))
		m.set(0, mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Lt, A: cp(m.local(v)), B: cp(m.pl(1, deref(types.Int))), Type: types.Bool})
		m.end(v)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})
	t.Run("own-parameter", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"k"}, []types.TypeID{types.Int})
		m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, ri(k), rf)
		m.sig.Flows.Sources = []types.Source{{Param: 1}}
		v := m.temp(types.Int)
		m.set(v, use(cp(k.capturePlace(m, env, rec, 0))))
		m.end(v)
		m.set(0, use(cp(m.local(1))))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})
}

// TestE0609_StoredLoan: a literal storing a captured
// reference into its mut parameter is E0609 (store), not E0604; storing a
// plain copy of a capture is clean.
func TestE0609_StoredLoan(t *testing.T) {
	ri := func(k capKit) types.TypeID { return k.tab.Ref(types.Int) }
	t.Run("store-loan", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"k"}, []types.TypeID{types.Int})
		m, env := k.literalBody("L", []types.Param{k.param("q", k.tab.Mut(ri(k)))}, types.Void, rf)
		ft := k.tab.Get(rec).Fields[0].Type
		at := m.assign(m.pl(1, deref(ri(k))), use(cp(m.pl(env, deref(rec), fld(0, ft)))))
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "store" || factText(d, "captured") != "[k]" {
			t.Fatalf("E0609 facts: reason %q captured %q", factText(d, "reason"), factText(d, "captured"))
		}
	})
	t.Run("store-copy", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.env([]string{"k"}, []types.TypeID{types.Int})
		m, env := k.literalBody("L", []types.Param{k.param("q", k.tab.Mut(types.Int))}, types.Void, rf)
		m.assign(m.pl(1, deref(types.Int)), use(cp(k.capturePlace(m, env, rec, 0))))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})
}

// TestE0609_Backstop: MIR that writes, mut-borrows or
// moves through the hidden parameter is E0609 (write, mut-borrow, move),
// never E0507 or E0409; a read and a shared borrow are clean.
func TestE0609_Backstop(t *testing.T) {
	ri := func(k capKit) types.TypeID { return k.tab.Ref(types.Int) }
	for _, tc := range []struct {
		name, reason string
		build        func(k capKit, m *mb, env mir.LocalID, rec types.TypeID) mir.SpanID
	}{
		{"write", "write", func(k capKit, m *mb, env mir.LocalID, rec types.TypeID) mir.SpanID {
			return m.assign(k.capturePlace(m, env, rec, 0), use(imm(types.Int, 1)))
		}},
		{"mut-borrow", "mut-borrow", func(k capKit, m *mb, env mir.LocalID, rec types.TypeID) mir.SpanID {
			p := m.temp(k.tab.Mut(types.Int))
			s := m.set(p, mutRef(k.capturePlace(m, env, rec, 0)))
			m.end(p)
			return s
		}},
		{"move", "move", func(k capKit, m *mb, env mir.LocalID, rec types.TypeID) mir.SpanID {
			v := m.temp(k.Res)
			s := m.set(v, use(mv(k.capturePlace(m, env, rec, 1))))
			m.set(0, mir.Rvalue{Kind: mir.RvUse, A: imm(types.Bool, 1)})
			next := m.block()
			m.call(k.item("Res.Close"), nil, []mir.Arg{argMove(m.local(v))}, m.local(m.temp(types.Void)), next)
			m.at(next)
			return s
		}},
		{"read", "", func(k capKit, m *mb, env mir.LocalID, rec types.TypeID) mir.SpanID {
			v := m.temp(types.Int)
			m.set(v, use(cp(k.capturePlace(m, env, rec, 0))))
			r := m.temp(ri(k))
			m.set(r, ref(k.capturePlace(m, env, rec, 0)))
			m.end(r, v)
			return 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := capKit{newKit(t)}
			rec, rf := k.env([]string{"n", "res"}, []types.TypeID{types.Int, k.Res})
			m, env := k.literalBody("L", []types.Param{k.param("a", ri(k))}, types.Void, rf)
			at := tc.build(k, m, env, rec)
			m.ret(mir.ExitNormal)
			r := Check(m.b, k.kit.env(m.sig)) // a write through & fails no Validate rule
			if tc.reason == "" {
				m.expect(t, r)
				return
			}
			m.expect(t, r, diag.E0609, at)
			if d := r.Diags[0]; factText(d, "reason") != tc.reason || d.Phase != diag.PhaseFlow {
				t.Fatalf("E0609 reason %q phase %v, want %q in flow", factText(d, "reason"), d.Phase, tc.reason)
			}
		})
	}
}

// factText is the text of a diagnostic's fact key ("" when absent); a list
// fact prints as [a b].
func factText(d *diag.Diagnostic, key string) string {
	for _, f := range d.Facts {
		if f.Key != key {
			continue
		}
		switch f.Value.Kind {
		case diag.FactText:
			return f.Value.Str
		case diag.FactList:
			s := "["
			for i, e := range f.Value.List {
				if i > 0 {
					s += " "
				}
				s += e.Str
			}
			return s + "]"
		}
	}
	return ""
}

// mutEnv builds a mut environment record whose fields are &T (when muts[i] is
// false) or mut T (when muts[i] is true), returning (rec, mut rec).
func (k capKit) mutEnv(names []string, ts []types.TypeID, muts []bool) (rec, ref types.TypeID) {
	var fs []types.RecordField
	for i, n := range names {
		ft := k.tab.Ref(ts[i])
		if muts[i] {
			ft = k.tab.Mut(ts[i])
		}
		fs = append(fs, types.RecordField{Name: k.in.Intern(n), Type: ft})
	}
	rec = k.tab.Record(fs)
	return rec, k.tab.Mut(rec)
}

// buildMutEnv is lower's pass 3 for a mut environment: t_i := &src_i or
// mut src_i per field kind, e := record, r := mut e, passed by ArgMove.
func (k capKit) buildMutEnv(m *mb, rec, ref types.TypeID, srcs ...mir.Place) (mir.Arg, []mir.LocalID) {
	fs := k.tab.Get(rec).Fields
	var temps []mir.LocalID
	var ops []mir.Operand
	for i, p := range srcs {
		t := m.temp(fs[i].Type)
		if k.tab.Kind(fs[i].Type) == types.KMut {
			m.set(t, mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: p, Type: fs[i].Type})
			ops = append(ops, mv(m.local(t)))
		} else {
			m.set(t, mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: p, Type: fs[i].Type})
			ops = append(ops, cp(m.local(t)))
		}
		temps = append(temps, t)
	}
	e := m.temp(rec)
	m.set(e, m.agg(mir.AggRecord, types.NoItem, rec, ops...))
	r := m.temp(ref)
	m.set(r, mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: m.local(e), Type: ref})
	temps = append(temps, e, r)
	return argMove(m.local(r)), temps
}

// TestMutCaptureConflictsAtCall checks R5/R13/R14 for mut captures:
//   - mut capture beside &x or another mut capture of x is E0501;
//   - mut captures of two distinct locals are clean;
//   - a mut capture's loan ends at the call's statement end (R14).
func TestMutCaptureConflictsAtCall(t *testing.T) {
	t.Run("mut-capture-beside-shared-arg", func(t *testing.T) {
		k := capKit{newKit(t)}
		ri := k.tab.Ref(types.Int)
		f1 := k.tab.Fn(types.FnType{Params: []types.FnParam{{Type: ri}}, Result: types.Void})
		look := k.fn("Look", []types.Param{k.param("x", ri), {Name: k.in.Intern("f"), Type: f1, Const: true}}, types.Void, types.EmptyErrors)
		m := k.body(k.sig("G", []types.Param{k.param("k", types.Int)}, types.Void, types.EmptyErrors))
		n := m.user("n", types.Int, true)
		m.set(n, use(cp(m.local(1))))
		x := m.temp(ri)
		m.set(x, ref(m.local(n)))
		rec, rf := k.mutEnv([]string{"n"}, []types.TypeID{types.Int}, []bool{true})
		envArg, temps := k.buildMutEnv(m, rec, rf, m.local(n))
		next := m.block()
		m.call(look.Item, nil, []mir.Arg{argCopy(m.local(x)), {Operand: k.fnConst(f1, look.Item), Mode: mir.ArgCopy}, envArg}, m.local(0), next)
		m.at(next)
		m.end(temps[2], temps[1], temps[0], x)
		m.ret(mir.ExitNormal)
		r := m.check(t)
		if len(r.Diags) != 1 || r.Diags[0].Code != diag.E0501 {
			t.Fatalf("want one E0501, got %v", r.Diags)
		}
	})
	t.Run("two-mut-captures-same-and-disjoint", func(t *testing.T) {
		for _, same := range []bool{true, false} {
			k := capKit{newKit(t)}
			f0 := k.tab.Fn(types.FnType{Result: types.Void})
			two := k.fn("Two", []types.Param{
				{Name: k.in.Intern("f"), Type: f0, Const: true},
				{Name: k.in.Intern("g"), Type: f0, Const: true},
			}, types.Void, types.EmptyErrors)
			m := k.body(k.sig("G", nil, types.Void, types.EmptyErrors))
			a := m.user("a", types.Int, true)
			b := m.user("b", types.Int, true)
			m.set(a, use(imm(types.Int, 0)))
			m.set(b, use(imm(types.Int, 0)))
			recA, rfA := k.mutEnv([]string{"a"}, []types.TypeID{types.Int}, []bool{true})
			envA, tA := k.buildMutEnv(m, recA, rfA, m.local(a))
			second := b
			if same {
				second = a
			}
			recB, rfB := k.mutEnv([]string{"b"}, []types.TypeID{types.Int}, []bool{true})
			envB, tB := k.buildMutEnv(m, recB, rfB, m.local(second))
			next := m.block()
			m.call(two.Item, nil, []mir.Arg{
				{Operand: k.fnConst(f0, two.Item), Mode: mir.ArgCopy},
				{Operand: k.fnConst(f0, two.Item), Mode: mir.ArgCopy},
				envA, envB,
			}, m.local(0), next)
			m.at(next)
			m.end(tB[2], tB[1], tB[0], tA[2], tA[1], tA[0])
			// After the call statement ends, a and b are writable again (R14).
			m.set(a, use(imm(types.Int, 1)))
			m.set(b, use(imm(types.Int, 2)))
			m.ret(mir.ExitNormal)
			r := m.check(t)
			if same {
				if len(r.Diags) != 1 || r.Diags[0].Code != diag.E0501 {
					t.Fatalf("same local: want one E0501, got %v", r.Diags)
				}
			} else {
				m.expect(t, r)
			}
		}
	})
}

// TestMutCaptureBody checks mutations and borrows inside a mut-capturing
// literal: writing a mut capture and disjoint borrows of two mut captures are
// clean; overlapping mut borrows of one capture are E0501 naming the capture;
// writing through a captured &int is E0507 naming *p (not E0609); moving a
// var linear capture is E0609 (move).
func TestMutCaptureBody(t *testing.T) {
	t.Run("disjoint-mut-captures-and-write", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"a", "b"}, []types.TypeID{types.Int, types.Int}, []bool{true, true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		pa := m.user("pa", k.tab.Mut(types.Int), false)
		pb := m.user("pb", k.tab.Mut(types.Int), false)
		m.set(pa, mutRef(k.capturePlace(m, env, rec, 0)))
		m.set(pb, mutRef(k.capturePlace(m, env, rec, 1)))
		m.assign(m.pl(pa, deref(types.Int)), use(imm(types.Int, 1)))
		m.assign(m.pl(pb, deref(types.Int)), use(imm(types.Int, 2)))
		m.end(pb, pa)
		m.assign(k.capturePlace(m, env, rec, 0), use(imm(types.Int, 3)))
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})
	t.Run("overlapping-mut-borrow-is-E0501", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"a"}, []types.TypeID{types.Int}, []bool{true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		p1 := m.user("p1", k.tab.Mut(types.Int), false)
		p2 := m.user("p2", k.tab.Mut(types.Int), false)
		m.set(p1, mutRef(k.capturePlace(m, env, rec, 0)))
		at := m.set(p2, mutRef(k.capturePlace(m, env, rec, 0)))
		m.assign(m.pl(p1, deref(types.Int)), use(imm(types.Int, 1)))
		m.assign(m.pl(p2, deref(types.Int)), use(imm(types.Int, 2)))
		m.end(p2, p1)
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0501, at)
		if msg := r.Diags[0].Message; !strings.Contains(msg, "a") || strings.Contains(msg, "env") {
			t.Errorf("E0501 message %q should name capture `a`, not `env`", msg)
		}
	})
	t.Run("write-through-captured-shared-ref-is-E0507", func(t *testing.T) {
		k := capKit{newKit(t)}
		ri := k.tab.Ref(types.Int)
		rec, rf := k.env([]string{"p"}, []types.TypeID{ri})
		m, env := k.literalBody("L", nil, types.Void, rf)
		ft := k.tab.Get(rec).Fields[0].Type
		at := m.assign(m.pl(env, deref(rec), fld(0, ft), deref(ri), deref(types.Int)), use(imm(types.Int, 1)))
		m.ret(mir.ExitNormal)
		r := Check(m.b, k.kit.env(m.sig))
		m.expect(t, r, diag.E0507, at)
		if got := factText(r.Diags[0], "place"); got != "*p" {
			t.Errorf("E0507 place %q, want \"*p\"", got)
		}
	})
	t.Run("move-mut-linear-capture-is-E0609", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"res"}, []types.TypeID{k.Res}, []bool{true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		v := m.temp(k.Res)
		at := m.set(v, use(mv(k.capturePlace(m, env, rec, 0))))
		next := m.block()
		m.call(k.item("Res.Close"), nil, []mir.Arg{argMove(m.local(v))}, m.local(m.temp(types.Void)), next)
		m.at(next)
		m.ret(mir.ExitNormal)
		r := Check(m.b, k.kit.env(m.sig))
		m.expect(t, r, diag.E0609, at)
		if factText(r.Diags[0], "reason") != "move" {
			t.Errorf("E0609 reason %q, want move", factText(r.Diags[0], "reason"))
		}
	})
}

// TestE0609_StoreIntoCapture checks soundness when storing loans into a
// mutably-captured carrier variable:
//   - a parameter's loan stored into a capture is E0609 (store);
//   - another capture's loan stored into a capture is E0609 (store);
//   - a literal-local loan stored into a capture is E0505;
//   - a non-writable static loan stored into a capture is clean.
func TestE0609_StoreIntoCapture(t *testing.T) {
	ri := func(k capKit) types.TypeID { return k.tab.Ref(types.Int) }
	t.Run("param-loan-into-capture", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"count", "dst"}, []types.TypeID{types.Int, ri(k)}, []bool{true, true})
		m, env := k.literalBody("L", []types.Param{k.param("p", ri(k))}, types.Void, rf)
		at := m.assign(k.capturePlace(m, env, rec, 1), use(cp(m.local(1))))
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "store" || factText(d, "captured") != "[dst]" || d.Phase != diag.PhaseFlow {
			t.Fatalf("E0609 facts: reason %q captured %q phase %v", factText(d, "reason"), factText(d, "captured"), d.Phase)
		}
	})
	t.Run("capture-loan-into-other-capture", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"dst", "src"}, []types.TypeID{ri(k), types.Int}, []bool{true, false})
		m, env := k.literalBody("L", nil, types.Void, rf)
		ft := k.tab.Get(rec).Fields[1].Type
		at := m.assign(k.capturePlace(m, env, rec, 0), use(cp(m.pl(env, deref(rec), fld(1, ft)))))
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "store" || factText(d, "captured") != "[dst]" {
			t.Fatalf("E0609 facts: reason %q captured %q", factText(d, "reason"), factText(d, "captured"))
		}
	})
	t.Run("local-loan-into-capture-is-E0505", func(t *testing.T) {
		k := capKit{newKit(t)}
		rec, rf := k.mutEnv([]string{"dst"}, []types.TypeID{ri(k)}, []bool{true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		loc := m.user("loc", types.Int, false)
		m.set(loc, use(imm(types.Int, 7)))
		rref := m.temp(ri(k))
		m.set(rref, ref(m.local(loc)))
		at := m.assign(k.capturePlace(m, env, rec, 0), use(cp(m.local(rref))))
		m.end(rref, loc)
		m.ret(mir.ExitNormal)
		r := m.check(t)
		if len(r.Diags) != 1 || r.Diags[0].Code != diag.E0505 {
			t.Fatalf("want one E0505 at %v, got %v", at, r.Diags)
		}
	})
	t.Run("static-into-capture-is-clean", func(t *testing.T) {
		k := capKit{newKit(t)}
		cItem := k.items.AddItem(&types.Item{Module: k.main.ID, Key: types.ItemKey("t", "K"), Name: k.in.Intern("K"),
			Kind: types.KindConst, Decl: &types.ConstDecl{Type: types.Str}})
		rec, rf := k.mutEnv([]string{"dst"}, []types.TypeID{types.Str}, []bool{true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		s := m.temp(types.Str)
		m.set(s, mir.Rvalue{Kind: mir.RvConstRef, Item: cItem, Type: types.Str})
		m.assign(k.capturePlace(m, env, rec, 0), use(cp(m.local(s))))
		m.end(s)
		m.ret(mir.ExitNormal)
		m.expect(t, m.check(t))
	})
	t.Run("writable-global-into-capture-is-E0609", func(t *testing.T) {
		k := capKit{newKit(t)}
		cellT := k.tab.Named(k.known.Cell, []types.GenericArg{types.TypeArg(ri(k))})
		rcell := k.tab.Ref(cellT)
		gItem := k.items.AddItem(&types.Item{Module: k.main.ID, Key: types.ItemKey("t", "G"), Name: k.in.Intern("G"),
			Kind: types.KindGlobal, Decl: &types.GlobalDecl{Type: cellT}})
		rec, rf := k.mutEnv([]string{"dst"}, []types.TypeID{rcell}, []bool{true})
		m, env := k.literalBody("L", nil, types.Void, rf)
		g := m.temp(rcell)
		m.set(g, mir.Rvalue{Kind: mir.RvGlobalRef, Item: gItem, Type: rcell})
		at := m.assign(k.capturePlace(m, env, rec, 0), use(cp(m.local(g))))
		m.end(g)
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "store" || factText(d, "captured") != "[dst]" {
			t.Fatalf("E0609 facts: reason %q captured %q", factText(d, "reason"), factText(d, "captured"))
		}
	})
	t.Run("reborrow-beside-mut-env-move-is-E0501", func(t *testing.T) {
		k := capKit{newKit(t)}
		mi := k.tab.Mut(types.Int)
		f0 := k.tab.Fn(types.FnType{Result: types.Void})
		callMut := k.fn("CallMut", []types.Param{
			k.param("r", mi),
			{Name: k.in.Intern("f"), Type: f0, Const: true},
		}, types.Void, types.EmptyErrors)
		m := k.body(k.sig("G", []types.Param{k.param("p", mi)}, types.Void, types.EmptyErrors))
		rec, rf := k.mutEnv([]string{"p"}, []types.TypeID{types.Int}, []bool{true})
		// Reborrow *p into the mut environment, passed by ArgMove alongside ArgReborrow p.
		envArg, temps := k.buildMutEnv(m, rec, rf, m.pl(1, deref(types.Int)))
		next := m.block()
		m.call(callMut.Item, nil, []mir.Arg{
			argReborrow(m.local(1)),
			{Operand: k.fnConst(f0, callMut.Item), Mode: mir.ArgCopy},
			envArg,
		}, m.local(0), next)
		m.at(next)
		m.end(temps[2], temps[1], temps[0])
		m.ret(mir.ExitNormal)
		r := m.check(t)
		if len(r.Diags) != 1 || r.Diags[0].Code != diag.E0501 {
			t.Fatalf("want one E0501, got %v", r.Diags)
		}
	})
	t.Run("capture-loan-into-captured-arena-is-E0609", func(t *testing.T) {
		k := capKit{newKit(t)}
		ra := k.tab.Ref(k.Arena)
		storeA := k.fn("StoreA", []types.Param{
			k.param("a", ra),
			{Name: k.in.Intern("x"), Type: ri(k), Into: 1},
		}, types.Void, types.EmptyErrors)
		rec := k.tab.Record([]types.RecordField{
			{Name: k.in.Intern("a"), Type: k.tab.Ref(ra)},
			{Name: k.in.Intern("short"), Type: ri(k)},
		})
		rf := k.tab.Ref(rec)
		m, env := k.literalBody("L", nil, types.Void, rf)
		aVal := m.temp(ra)
		m.set(aVal, use(cp(m.pl(env, deref(rec), fld(0, k.tab.Ref(ra)), deref(ra)))))
		sVal := m.temp(ri(k))
		m.set(sVal, use(cp(m.pl(env, deref(rec), fld(1, ri(k))))))
		next := m.block()
		at := m.call(storeA.Item, nil, []mir.Arg{argCopy(m.local(aVal)), argCopy(m.local(sVal))}, m.local(0), next)
		m.at(next)
		m.end(sVal, aVal)
		m.ret(mir.ExitNormal)
		r := m.check(t)
		m.expect(t, r, diag.E0609, at)
		if d := r.Diags[0]; factText(d, "reason") != "store" || factText(d, "captured") != "[a]" {
			t.Fatalf("E0609 facts: reason %q captured %q", factText(d, "reason"), factText(d, "captured"))
		}
	})
}
