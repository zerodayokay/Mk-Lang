// Why: function literals take their environment as hidden trailing
// parameters and arguments, which the validator must accept and nothing else.
// What: TestHiddenEnvParams over litBody and callerBody.
// How: a body may end its parameters with hidden environment parameters and a
// direct or value call its arguments with environment arguments; anything else
// past the signature is still invariant M1 or M5.

package mir

import (
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/types"
)

// envWorld is the fixture plus a literal L(a: i64): i64 and a caller
// C(a: i64): i64, and the environment type &(x: &i64).
type envWorld struct {
	*fixture
	lit, caller types.ItemID
	env         types.TypeID
}

func newEnvWorld() *envWorld {
	f := newFixture()
	m := f.items.Get(f.show).Module
	add := func(name string) types.ItemID {
		return f.items.AddItem(&types.Item{Module: m, Name: f.in.Intern(name), Kind: types.KindFunc, Key: types.ItemKey("main", name)})
	}
	w := &envWorld{fixture: f, lit: add("L"), caller: add("C")}
	for _, it := range []types.ItemID{w.lit, w.caller} {
		f.sigs[it] = &types.Signature{Item: it, Params: []types.Param{{Name: f.in.Intern("a"), Type: types.I64}}, Result: types.I64}
	}
	w.env = w.envOf(f.tab.Ref(types.I64))
	return w
}

// envOf is &(x: t).
func (w *envWorld) envOf(t types.TypeID) types.TypeID {
	return w.tab.Ref(w.tab.Record([]types.RecordField{{Name: w.in.Intern("x"), Type: t}}))
}

func (w *envWorld) sigEnv(item types.ItemID) *Env {
	return &Env{Types: w.tab, Sig: w.sigs[item], Callee: func(id types.ItemID) *types.Signature { return w.sigs[id] }}
}

// litBody is L's body with one hidden parameter of type env:
//
//	_0: i64, _1: i64 (a), _2: env (hidden)
//	bb0: _0 = copy _1; return normal
func (w *envWorld) litBody(env types.TypeID) *Body {
	b := NewBody(w.lit, source.Span{File: 1, Start: 0, End: 10}, types.I64)
	sp := b.AddSpan(source.Span{File: 1, Start: 2, End: 8})
	a := b.AddParam(types.I64, w.in.Intern("a"), sp)
	b.AddParam(env, w.in.Intern("env"), sp)
	b0 := b.AddBlock(OriginBody, NoDefer)
	b.Blocks[b0].Stmts = []Statement{{Kind: StAssign, Place: LocalPlace(ReturnLocal), Span: sp,
		Rvalue: Rvalue{Kind: RvUse, A: Copy(LocalPlace(a))}}}
	b.Blocks[b0].Term = Terminator{Kind: TermReturn, Exit: ExitNormal, Span: sp}
	return b
}

// callerBody is C's body calling L directly with one trailing argument:
//
//	_0: i64, _1: i64 (a), _2: env, _3: fn(i64): i64, _4: scope
//	bb0: _0 = call L(copy _1, copy _2) -> bb1
//	bb1: return normal
func (w *envWorld) callerBody(extra Arg) *Body {
	b := NewBody(w.caller, source.Span{File: 1, Start: 0, End: 10}, types.I64)
	sp := b.AddSpan(source.Span{File: 1, Start: 2, End: 8})
	a := b.AddParam(types.I64, w.in.Intern("a"), sp)
	b.AddLocal(Local{Type: w.env, Kind: LocalTemp, Loop: NoLoop, Span: sp})
	b.AddLocal(Local{Type: w.tab.Fn(types.FnType{Params: []types.FnParam{{Type: types.I64, Into: types.NoParam}}, Result: types.I64}),
		Kind: LocalTemp, Loop: NoLoop, Span: sp})
	b.AddLocal(Local{Type: types.Void, Kind: LocalScope, Loop: NoLoop, Span: sp})
	b0, b1 := b.AddBlock(OriginBody, NoDefer), b.AddBlock(OriginBody, NoDefer)
	b.Blocks[b0].Term = Terminator{Kind: TermCall, Span: sp, Callee: Callee{Kind: CallDirect, Fn: FnRef{Item: w.lit}},
		Args: b.AddArgs(Arg{Operand: Copy(LocalPlace(a)), Mode: ArgCopy}, extra), Dest: LocalPlace(ReturnLocal),
		Target: b1, Spawn: NoLocal}
	b.Blocks[b1].Term = Terminator{Kind: TermReturn, Exit: ExitNormal, Span: sp}
	return b
}

func wantInv(t *testing.T, what, inv, frag string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), inv+": ") || !strings.Contains(err.Error(), frag) {
		t.Errorf("%s: want %s violation containing %q, got %v", what, inv, frag, err)
	}
}

// TestHiddenEnvParams: a body may end its parameters with hidden
// environment parameters, and a direct or value call may end its arguments
// with environment arguments; anything else past the signature stays M1 and
// M5.
func TestHiddenEnvParams(t *testing.T) {
	w := newEnvWorld()
	if !IsEnvType(w.tab, w.env) {
		t.Error("IsEnvType(&(x: &i64)) is false")
	}
	mutEnv := w.tab.Mut(w.tab.Record([]types.RecordField{{Name: w.in.Intern("x"), Type: w.tab.Mut(types.I64)}}))
	mixedEnv := w.tab.Mut(w.tab.Record([]types.RecordField{
		{Name: w.in.Intern("x"), Type: w.tab.Ref(types.I64)},
		{Name: w.in.Intern("y"), Type: w.tab.Mut(types.I64)},
	}))
	sliceEnv := w.tab.Mut(w.tab.Record([]types.RecordField{{Name: w.in.Intern("xs"), Type: w.tab.MutSlice(types.I64)}}))
	for name, ty := range map[string]types.TypeID{
		"mut (x: mut i64)":          mutEnv,
		"mut (x: &i64, y: mut i64)": mixedEnv,
		"mut (xs: mut []i64)":       sliceEnv,
	} {
		if !IsEnvType(w.tab, ty) {
			t.Errorf("IsEnvType(%s) is false", name)
		}
	}
	for name, ty := range map[string]types.TypeID{
		"none":                types.NoType,
		"i64":                 types.I64,
		"&i64":                w.tab.Ref(types.I64),
		"&()":                 w.tab.Ref(w.tab.Record(nil)),
		"mut ()":              w.tab.Mut(w.tab.Record(nil)),
		"&(x: mut i64)":       w.envOf(w.tab.Mut(types.I64)),
		"&(x: mut []i64)":     w.envOf(w.tab.MutSlice(types.I64)),
		"&(x: i64)":           w.envOf(types.I64),
		"&(x: []i64)":         w.envOf(w.tab.Slice(types.I64)),
		"mut (x: &i64)":       w.tab.Mut(w.tab.Get(w.env).Elem),
		"mut (x: i64)":        w.tab.Mut(w.tab.Record([]types.RecordField{{Name: w.in.Intern("x"), Type: types.I64}})),
		"mut (x: []i64)":      w.tab.Mut(w.tab.Record([]types.RecordField{{Name: w.in.Intern("x"), Type: w.tab.Slice(types.I64)}})),
		"(x: &i64) by value":  w.tab.Get(w.env).Elem,
		"*(x: &i64), raw ptr": w.tab.RawPtr(w.tab.Get(w.env).Elem),
	} {
		if IsEnvType(w.tab, ty) {
			t.Errorf("IsEnvType(%s) is true", name)
		}
	}

	// M1: one hidden parameter past the signature (&R or mut R).
	if err := w.litBody(w.env).Validate(w.sigEnv(w.lit)); err != nil {
		t.Fatalf("literal body with a hidden &R environment parameter refused: %v", err)
	}
	if err := w.litBody(mutEnv).Validate(w.sigEnv(w.lit)); err != nil {
		t.Fatalf("literal body with a hidden mut R environment parameter refused: %v", err)
	}
	if err := w.litBody(mixedEnv).Validate(w.sigEnv(w.lit)); err != nil {
		t.Fatalf("literal body with a hidden mixed mut R environment parameter refused: %v", err)
	}
	if err := w.litBody(sliceEnv).Validate(w.sigEnv(w.lit)); err != nil {
		t.Fatalf("literal body with a hidden mut []T environment parameter refused: %v", err)
	}
	wantInv(t, "a non-environment extra parameter", "M1", "not an environment type",
		w.litBody(types.I64).Validate(w.sigEnv(w.lit)))
	wantInv(t, "an extra parameter of &(x: mut i64)", "M1", "not an environment type",
		w.litBody(w.envOf(w.tab.Mut(types.I64))).Validate(w.sigEnv(w.lit)))
	b := w.litBody(w.env)
	b.Locals[2].Kind = LocalTemp
	wantInv(t, "a hidden parameter that is not a LocalParam", "M1", "must be parameter 2", b.Validate(w.sigEnv(w.lit)))
	e := w.sigEnv(w.lit)
	three := *e.Sig
	three.Params = []types.Param{e.Sig.Params[0], e.Sig.Params[0], e.Sig.Params[0]}
	e.Sig = &three
	wantInv(t, "fewer parameters than the signature", "M1", "2 param locals, signature has 3", w.litBody(w.env).Validate(e))

	// M5, direct call: one environment argument past L's parameter.
	envArg := Arg{Operand: Copy(LocalPlace(2)), Mode: ArgCopy}
	if err := w.callerBody(envArg).Validate(w.sigEnv(w.caller)); err != nil {
		t.Fatalf("direct call with an environment argument refused: %v", err)
	}
	wantInv(t, "an extra i64 argument", "M5", "not an environment type",
		w.callerBody(Arg{Operand: Copy(LocalPlace(1)), Mode: ArgCopy}).Validate(w.sigEnv(w.caller)))
	wantInv(t, "a shared environment argument by move", "M5", "not a copy of &R",
		w.callerBody(Arg{Operand: Move(LocalPlace(2)), Mode: ArgMove}).Validate(w.sigEnv(w.caller)))

	// M5, direct call with a mut R environment: ArgMove and ArgReborrow pass, ArgCopy fails.
	wMut := newEnvWorld()
	wMut.env = wMut.tab.Mut(wMut.tab.Record([]types.RecordField{{Name: wMut.in.Intern("x"), Type: wMut.tab.Mut(types.I64)}}))
	if err := wMut.callerBody(Arg{Operand: Move(LocalPlace(2)), Mode: ArgMove}).Validate(wMut.sigEnv(wMut.caller)); err != nil {
		t.Fatalf("direct call with a moved mut R environment refused: %v", err)
	}
	if err := wMut.callerBody(Arg{Operand: Copy(LocalPlace(2)), Mode: ArgReborrow}).Validate(wMut.sigEnv(wMut.caller)); err != nil {
		t.Fatalf("direct call with a reborrowed mut R environment refused: %v", err)
	}
	wantInv(t, "a mut R environment argument by copy", "M5", "not a move or reborrow of mut R",
		wMut.callerBody(Arg{Operand: Copy(LocalPlace(2)), Mode: ArgCopy}).Validate(wMut.sigEnv(wMut.caller)))

	b = w.callerBody(envArg)
	b.Blocks[0].Term.Args = b.AddArgs(envArg)
	// The environment argument alone fills L's i64 parameter: only arguments
	// past the declared parameters may be environments (the argument type
	// rule refuses it).
	wantInv(t, "the environment argument in place of a declared one", "M5", "argument 0 is", b.Validate(w.sigEnv(w.caller)))
	b = w.callerBody(envArg)
	b.Blocks[0].Term.Spawn = 4
	wantInv(t, "a spawned call with an environment argument", "M5", "a spawned call with 1 arguments", b.Validate(w.sigEnv(w.caller)))

	// M5, value call: the same rule through a fn value.
	b = w.callerBody(envArg)
	b.Blocks[0].Term.Callee = Callee{Kind: CallValue, Value: Copy(LocalPlace(3))}
	if err := b.Validate(w.sigEnv(w.caller)); err != nil {
		t.Fatalf("value call with an environment argument refused: %v", err)
	}
	b = w.callerBody(Arg{Operand: Copy(LocalPlace(1)), Mode: ArgCopy})
	b.Blocks[0].Term.Callee = Callee{Kind: CallValue, Value: Copy(LocalPlace(3))}
	wantInv(t, "a value call with an extra i64 argument", "M5", "not an environment type", b.Validate(w.sigEnv(w.caller)))
	b = w.callerBody(envArg)
	b.Blocks[0].Term.Callee = Callee{Kind: CallValue, Value: Copy(LocalPlace(3))}
	b.Blocks[0].Term.Args = Range{}
	wantInv(t, "a value call with too few arguments", "M5", "0 arguments, fn value has 1", b.Validate(w.sigEnv(w.caller)))
}
