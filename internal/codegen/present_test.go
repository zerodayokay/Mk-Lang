// Why: dl.Present's lowering reads Config.Lazy, which no member's content
// holds, so the pack key must; and its two lowerings must be the constant
// and the resolver call.
// What: TestPresentLowering and TestPresentFacts.
// How: one pack whose Caller passes an RvFnRef of the extern MkPk to
// std.dl.Present, emitted and keyed with MkPk lazy and not.

package codegen

import (
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// presentUnit is Caller(): bool { return dl.Present(MkPk) } over a
// bodiless extern MkPk(): int.
func presentUnit(e *testEnv) *Unit {
	ext := e.fnIn("app", "", "MkPk", nil, types.Int, false)
	e.items.Get(ext).Decl.(*types.FuncDecl).Sig.Extern = true
	fnT := e.tt.Fn(types.FnType{Result: types.Int})
	pres := e.fnIn("std.dl", "", "Present", []types.TypeID{fnT}, types.Bool, true)
	f := e.body(e.fn("Caller", nil, types.Bool, 0))
	l := f.local(fnT)
	f.assign(place(l), mir.Rvalue{Kind: mir.RvFnRef, Fn: mir.FnRef{Item: ext}, Type: fnT}, f.at(2, 5))
	next := f.newBlock()
	f.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(pres), Args: f.b.AddArgs(mir.Arg{Operand: mir.Copy(place(l))}),
		Dest: place(0), Target: next, Spawn: mir.NoLocal, Span: f.at(2, 9)})
	f.cur = next
	f.ret(f.at(3, 1))
	return unitOf(e, []*mir.Body{f.b}, ext, pres)
}

// TestPresentLowering: with MkPk eager the call stores true; lazy, it
// calls MkLazyPresent with MkPk's descriptor, the slot __imp_MkPk.
func TestPresentLowering(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		e := newTestEnv()
		u := presentUnit(e)
		var b strings.Builder
		cfg := Config{Target: types.X86_64Windows, Profile: types.Dev}
		if lazy {
			cfg.Lazy = map[string]bool{"MkPk": true}
		}
		if _, err := EmitPack(u, 0, e, cfg, &b); err != nil {
			t.Fatal(err)
		}
		ll := b.String()
		call := strings.Contains(ll, "call i1 @"+SymLazyPresent+"(ptr @"+LazySlot("MkPk")+")")
		if call != lazy || strings.Contains(ll, "@"+LazySlot("MkPk")) != lazy {
			t.Errorf("lazy %v: the resolver call is there: %v\n%s", lazy, call, ll)
		}
	}
}

// TestPresentFacts: the pack's facts move when MkPk's lazy bit does, and
// not for another name's.
func TestPresentFacts(t *testing.T) {
	e := newTestEnv()
	u := presentUnit(e)
	cfg := Config{Target: types.X86_64Windows, Profile: types.Dev}
	h0, _ := pfHash(u, 0, e, cfg)
	cfg.Lazy = map[string]bool{"Other": true}
	if h, _ := pfHash(u, 0, e, cfg); h != h0 {
		t.Error("another name's lazy bit moved the pack's facts")
	}
	cfg.Lazy = map[string]bool{"MkPk": true}
	if h, _ := pfHash(u, 0, e, cfg); h == h0 {
		t.Error("MkPk's lazy bit did not move the pack's facts")
	}
}
