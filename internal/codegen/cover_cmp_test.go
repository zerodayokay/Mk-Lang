// Why: live CoverCmp instrumentation under Config.Cover must instrument
// 2/4/8-byte integer ==/!= comparisons and non-boolean integer switches in
// user modules while skipping 1-byte/std comparisons and keeping PackFacts
// exact across cross-pack changes.
//
// What: TestCoverCmpIREmission and TestCoverCmpPackFacts.
//
// How: builds small MIR units containing std.rt.CoverCmp plus covered user
// functions (OpEq u32, OpNe u16, TermSwitch u64, OpEq u8) and a std module
// function, verifying emitted LLVM IR and PackFacts hashes.

package codegen

import (
	"bytes"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

func TestCoverCmpIREmission(t *testing.T) {
	e := newTestEnv()
	e.mods["std.rt"] = e.items.AddModule(&types.Module{Path: "std.rt", Std: true})
	rtItem := e.itemIn("std.rt", types.ItemKey("std.rt", "CoverCmp"), "CoverCmp", types.KindFunc,
		&types.FuncDecl{Sig: types.Signature{
			Name:   e.in.Intern("CoverCmp"),
			Params: []types.Param{{Name: e.in.Intern("a"), Type: types.U64}, {Name: e.in.Intern("b"), Type: types.U64}, {Name: e.in.Intern("w"), Type: types.U8}},
			Result: types.Void,
		}})
	rtBody := e.body(rtItem)
	rtBody.ret(rtBody.at(1, 1))

	cmpEq32 := e.fn("CmpEq32", []types.TypeID{types.U32}, types.Bool, 0)
	f32 := e.body(cmpEq32)
	sp32 := f32.at(10, 3)
	f32.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.U32, 0xCAFEBABE), Type: types.Bool}, sp32)
	f32.ret(sp32)

	cmpNe16 := e.fn("CmpNe16", []types.TypeID{types.U16}, types.Bool, 0)
	f16 := e.body(cmpNe16)
	sp16 := f16.at(20, 3)
	f16.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Ne, A: mir.Copy(place(1)), B: mir.Imm(types.U16, 0x1234), Type: types.Bool}, sp16)
	f16.ret(sp16)

	cmpSw64 := e.fn("CmpSw64", []types.TypeID{types.U64}, types.I32, 0)
	f64 := e.body(cmpSw64)
	sp64 := f64.at(30, 3)
	hit, miss := f64.newBlock(), f64.newBlock()
	f64.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(1)),
		Cases: f64.b.AddCases(mir.SwitchCase{Value: 0x1122334455667788, Target: hit}), Default: miss, Span: sp64})
	f64.cur = hit
	f64.assign(place(0), use(mir.Imm(types.I32, 1)), sp64)
	f64.ret(sp64)
	f64.cur = miss
	f64.assign(place(0), use(mir.Imm(types.I32, 0)), sp64)
	f64.ret(sp64)

	cmpByte := e.fn("CmpByte", []types.TypeID{types.U8}, types.Bool, 0)
	f8 := e.body(cmpByte)
	sp8 := f8.at(40, 3)
	f8.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.U8, 42), Type: types.Bool}, sp8)
	f8.ret(sp8)

	u := unitOf(e, []*mir.Body{rtBody.b, f32.b, f16.b, f64.b, f8.b})
	var buf bytes.Buffer
	if _, err := EmitPack(u, 0, e, Config{Target: types.X86_64Linux, Profile: types.Dev, Cover: true}, &buf); err != nil {
		t.Fatalf("EmitPack: %v", err)
	}
	ll := buf.String()

	for _, fn := range []struct {
		sym       string
		wantWidth string
	}{
		{"mk.main.CmpEq32", "i8 noundef zeroext 4"},
		{"mk.main.CmpNe16", "i8 noundef zeroext 2"},
		{"mk.main.CmpSw64", "i8 noundef zeroext 8"},
	} {
		body := fnText(ll, fn.sym)
		if !strings.Contains(body, "call void @mk.std.rt.CoverCmp(") || !strings.Contains(body, fn.wantWidth) {
			t.Errorf("%s missing CoverCmp (%s):\n%s", fn.sym, fn.wantWidth, body)
		}
	}
	for _, fn := range []string{"mk.main.CmpByte", "mk.std.rt.CoverCmp"} {
		if body := fnText(ll, fn); strings.Contains(body, "call void @mk.std.rt.CoverCmp(") {
			t.Errorf("%s must not emit CoverCmp:\n%s", fn, body)
		}
	}
}

func TestCoverCmpPackFacts(t *testing.T) {
	hashWith := func(withCmp bool) source.Hash {
		e := newTestEnv()
		cmpEq := e.fn("CmpEq", []types.TypeID{types.U32}, types.Bool, 0)
		fb := e.body(cmpEq)
		sp := fb.at(10, 3)
		fb.assign(place(0), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(place(1)), B: mir.Imm(types.U32, 0xCAFEBABE), Type: types.Bool}, sp)
		fb.ret(sp)
		bodies := []*mir.Body{fb.b}
		if withCmp {
			e.mods["std.rt"] = e.items.AddModule(&types.Module{Path: "std.rt", Std: true})
			rtItem := e.itemIn("std.rt", types.ItemKey("std.rt", "CoverCmp"), "CoverCmp", types.KindFunc,
				&types.FuncDecl{Sig: types.Signature{
					Name:   e.in.Intern("CoverCmp"),
					Params: []types.Param{{Name: e.in.Intern("a"), Type: types.U64}, {Name: e.in.Intern("b"), Type: types.U64}, {Name: e.in.Intern("w"), Type: types.U8}},
					Result: types.Void,
				}})
			rb := e.body(rtItem)
			rb.ret(rb.at(1, 1))
			bodies = append(bodies, rb.b)
		}
		u := unitOf(e, bodies)
		h := source.NewHasher("test")
		if !PackFacts(u, 0, e, Config{Target: types.X86_64Linux, Profile: types.Dev, Cover: true}, h) {
			t.Fatal("PackFacts returned false")
		}
		return h.Sum()
	}
	if hashWith(true) == hashWith(false) {
		t.Error("PackFacts did not distinguish presence vs absence of std.rt.CoverCmp")
	}
}
