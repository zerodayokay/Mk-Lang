// Why: a pack's cached object is reused by its pre-emission key, so a
// Config field or table row that the key misses gives a stale object.
// What: tests that every Config field codegen reads reaches PackFacts, that
// a configuration whose reads it does not cover forms no key, that the rows
// and solecall bits move it, and that PackConstTypes reports the layouts a
// pack reads.
// How: pfHash keys small units before and after one change each.

package codegen

import (
	"reflect"
	"testing"

	"mk/internal/mir"
	"mk/internal/pgo"
	"mk/internal/source"
	"mk/internal/types"
)

// pfUnit is two packs: pack 0 holds Callee and Caller (Caller calls
// Callee once), pack 1 holds Other, which calls Callee when twice is set.
func pfUnit(e *testEnv, twice bool, callerLine uint32) *Unit {
	callee := e.body(e.fn("Callee", nil, types.Int, 0))
	callee.assign(place(0), use(mir.Imm(types.Int, 7)), callee.at(2, 5))
	callee.ret(callee.at(2, 5))
	call := func(f *fnb, line uint32) {
		next := f.newBlock()
		f.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(callee.item), Dest: place(0), Target: next,
			Spawn: mir.NoLocal, Span: f.at(line, 5)})
		f.cur = next
		f.ret(f.at(line, 9))
	}
	caller := e.body(e.fn("Caller", nil, types.Int, 0))
	call(caller, callerLine)
	other := e.body(e.fn("Other", nil, types.Int, 0))
	if twice {
		call(other, 9)
	} else {
		other.assign(place(0), use(mir.Imm(types.Int, 1)), other.at(9, 5))
		other.ret(other.at(9, 5))
	}
	u := unitOf(e, []*mir.Body{callee.b, caller.b, other.b})
	u.Packs = [][]int{{0, 1}, {2}}
	u.Funcs[2].Pack = 1
	return u
}

func pfHash(u *Unit, p int, env Env, cfg Config) (source.Hash, bool) {
	h := source.NewHasher("test")
	ok := PackFacts(u, p, env, cfg, h)
	return h.Sum(), ok
}

// TestPackFactsConfigFields: changing any one Config field changes
// pack 0's facts, except Debug, PGO and PGOGen, which make PackFacts
// refuse a key. A new Config field fails this test until PackFacts covers
// it (or refuses it).
func TestPackFactsConfigFields(t *testing.T) {
	e := newTestEnv()
	u := pfUnit(e, false, 5)
	base := Config{Target: types.X86_64Windows, Profile: types.Dev, CPU: "x86-64-v2"}
	h0, ok := pfHash(u, 0, e, base)
	if !ok {
		t.Fatal("PackFacts refused a plain dev configuration")
	}
	refuse := map[string]bool{"Debug": true, "PGO": true, "PGOGen": true}
	rt := reflect.TypeOf(base)
	for i := range rt.NumField() {
		f := rt.Field(i)
		cfg := base
		v := reflect.ValueOf(&cfg).Elem().Field(i)
		switch f.Name {
		case "Target":
			cfg.Target = types.X86_64Linux
		case "Profile":
			cfg.Profile = types.Release
		case "Main":
			cfg.Main = types.ItemKey("main", "Wrapper")
		case "PGO":
			cfg.PGO = &pgo.Profile{}
		case "Display":
			cfg.Display = map[string]string{"lib_00000000": "lib"}
		case "Lazy":
			// Read only at a dl.Present call (presentFacts), which pfUnit
			// has none of: no pack moves. internal/driver's
			// TestFFI_LazyPresentKey moves a pack that has one.
			cfg.Lazy = map[string]bool{"Callee": true}
			if now, _ := pfHash(u, 0, e, cfg); now != h0 {
				t.Errorf("Config.Lazy moved a pack with no dl.Present call")
			}
			continue
		case "Frames":
			// A member's frame: only the packs holding a named member move.
			cfg.Frames = map[string]string{e.Items().Get(u.Funcs[0].Item).Key.String(): ""}
			was, _ := pfHash(u, 1, e, base)
			if now, _ := pfHash(u, 1, e, cfg); now != was {
				t.Errorf("Config.Frames naming a pack 0 member moved pack 1's facts")
			}
		default:
			switch v.Kind() {
			case reflect.String:
				v.SetString(v.String() + "x")
			case reflect.Bool:
				v.SetBool(!v.Bool())
			default:
				t.Fatalf("Config.%s: kind %v; extend this test and PackFacts", f.Name, v.Kind())
			}
		}
		h, ok := pfHash(u, 0, e, cfg)
		switch {
		case refuse[f.Name] && ok:
			t.Errorf("Config.%s set: PackFacts formed a key, want a refusal", f.Name)
		case !refuse[f.Name] && !ok:
			t.Errorf("Config.%s changed: PackFacts refused", f.Name)
		case !refuse[f.Name] && h == h0:
			t.Errorf("Config.%s changed: pack facts unchanged", f.Name)
		}
	}
}

// TestPackFactsRowsAndSoleCalls: a body's position moves its pack's
// facts and no other pack's; a second call of Callee from pack 1 moves
// pack 0's facts in release (Caller's call is no longer the only one, so
// solecall's alwaysinline mark goes) and not in dev.
func TestPackFactsRowsAndSoleCalls(t *testing.T) {
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		cfg := Config{Target: types.X86_64Windows, Profile: prof}
		e := newTestEnv()
		base := pfUnit(e, false, 5)
		e2 := newTestEnv()
		moved := pfUnit(e2, false, 6)
		e3 := newTestEnv()
		twice := pfUnit(e3, true, 5)
		h0, _ := pfHash(base, 0, e, cfg)
		h1, _ := pfHash(base, 1, e, cfg)
		if h, _ := pfHash(moved, 0, e2, cfg); h == h0 {
			t.Errorf("%v: Caller moved a line, pack 0's facts unchanged", prof)
		}
		if h, _ := pfHash(moved, 1, e2, cfg); h != h1 {
			t.Errorf("%v: Caller moved a line, pack 1's facts changed", prof)
		}
		h, _ := pfHash(twice, 0, e3, cfg)
		if prof == types.Release && h == h0 {
			t.Error("release: a second call site in pack 1 left pack 0's facts unchanged")
		}
		if prof == types.Dev && h != h0 {
			t.Error("dev: a second call site in pack 1 changed pack 0's facts (the rule is release only)")
		}
	}
}

// TestPackConstTypesBodyReads: PackConstTypes reports the layouts pack
// 0's IR reads through a member body that names no such type itself. Stub
// takes &C of a const item (globalRef emits C's storage in pack 0: C's
// type), and its asm names H, a function of pack 1 (the declare reads H's
// parameter and result), Ext, a bodiless function (its declared signature),
// and KernelEnd, an extern var (externGlobal reads its size and alignment).
// Boot, a plain var global of pack 1 named by sym, adds nothing: its
// declaration reads only its size, which its value's bytes in the unit
// digest carry. Stub also takes &P, a const whose value relocates to
// another const N: N's type is reached only through the data relocation. A
// mutant that drops any one of these rows fails it.
func TestPackConstTypesBodyReads(t *testing.T) {
	e := newTestEnv()
	sC := e.structT("SC", types.I64, types.I64)
	sH := e.structT("SH", types.I64, types.I64, types.I64)
	rH := e.structT("RH", types.I32)
	sE := e.structT("SE", types.I16)
	sX := e.structT("SX", types.U8)
	sV := e.structT("SV", types.U16)
	sN := e.structT("SN", types.I32, types.I32)
	c := e.item("C", types.KindConst, &types.ConstDecl{Type: sC, HasValue: true,
		Value: e.consts.Intern(types.ConstValue{Type: sC, Align: 8, Bytes: make([]byte, 16)})})
	// P is a const pointer whose value relocates to N's
	// bytes (a RelocData), so N's type is read only through P's value.
	n := e.consts.Intern(types.ConstValue{Type: sN, Align: 4, Bytes: make([]byte, 8)})
	pT := e.tt.Ref(sN)
	pc := e.item("P", types.KindConst, &types.ConstDecl{Type: pT, HasValue: true,
		Value: e.consts.Intern(types.ConstValue{Type: pT, Align: 8, Bytes: make([]byte, 8), Relocs: []types.Reloc{{Kind: types.RelocData, Data: n}}})})
	x := e.item("KernelEnd", types.KindGlobal, &types.GlobalDecl{Type: sX, Extern: true})
	v := e.item("Boot", types.KindGlobal, &types.GlobalDecl{Type: sV, HasValue: true,
		Value: e.consts.Intern(types.ConstValue{Type: sV, Align: 2, Bytes: make([]byte, 2)})})
	ext := e.fnIn("main", "", "Ext", []types.TypeID{sE}, types.Void, false)
	h := e.body(e.fn("H", []types.TypeID{sH}, rH, 0))
	h.ret(h.at(1, 1))
	f := e.body(e.fn("Stub", nil, types.Void, 0))
	r := f.local(e.tt.Ref(sC))
	f.assign(place(r), mir.Rvalue{Kind: mir.RvGlobalRef, Item: c, Type: e.tt.Ref(sC)}, f.at(2, 1))
	rp := f.local(e.tt.Ref(pT))
	f.assign(place(rp), mir.Rvalue{Kind: mir.RvGlobalRef, Item: pc, Type: e.tt.Ref(pT)}, f.at(2, 5))
	f.asmBody(mir.Asm{Template: "call {0}\ncall {1}\nlea rax, [rip + {2}]\nlea rax, [rip + {3}]", Operands: []mir.AsmOperand{
		{Kind: mir.AsmSym, Sym: h.item}, {Kind: mir.AsmSym, Sym: ext}, {Kind: mir.AsmSym, Sym: x}, {Kind: mir.AsmSym, Sym: v}}}, f.at(3, 1))
	f.ret(f.at(4, 1))
	u := unitOf(e, []*mir.Body{f.b, h.b}, ext)
	u.Packs = [][]int{{0}, {1}}
	u.Funcs[1].Pack = 1
	// Both globals' rows are pack 1's, so pack 0's u.Globals walk does not
	// reach them: only the sym operands do.
	u.Globals = []Global{{Item: v, Pack: 1}, {Item: x, Pack: 1}}
	got := map[types.TypeID]bool{}
	PackConstTypes(u, 0, e, Config{Target: types.X86_64Windows, Profile: types.Dev}, func(t types.TypeID) { got[t] = true })
	for _, w := range []struct {
		name string
		t    types.TypeID
	}{{"&C's const type", sC}, {"&P's data relocation to N", sN}, {"sym H's parameter", sH}, {"sym H's result", rH}, {"sym Ext's parameter", sE}, {"sym KernelEnd's type", sX}} {
		if !got[w.t] {
			t.Errorf("pack 0: %s (%s) not reported", w.name, e.tt.TypeString(w.t, nil))
		}
	}
	if got[sV] {
		t.Error("pack 0: sym Boot's type reported; a plain var's declare reads only its size, keyed by its value")
	}
}
