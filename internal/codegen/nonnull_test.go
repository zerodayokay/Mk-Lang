// Why: a const word codegen loads with !nonnull (a str or slice data pointer,
// &T, mut T) must never be null, or the load is poison: a "" const of
// sixteen zero bytes would let LLVM delete System.Env's loop exit.
// What: TestNonnullConstRepairsEmptyViews and
// TestNonnullConstRefusesNullData, over nonnull.go.
// How: hand-built const reads are emitted and the const lines checked;
// TestPerfBack_EnvAbsentLoopExits in internal/driver runs the program
// natively.

package codegen

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// constFn is `fn name(): t { return <c> }`.
func constFn(e *testEnv, name string, t types.TypeID, c types.ConstID) *mir.Body {
	f := e.body(e.fn(name, nil, t, 0))
	f.assign(place(0), use(mir.Const(t, c)), f.at(1, 1))
	f.ret(f.at(1, 1))
	return f.b
}

// emitErr emits one pack of bodies for x86_64-linux at release and returns
// the IR or the error.
func emitErr(e *testEnv, bodies ...*mir.Body) (string, error) {
	var b bytes.Buffer
	_, err := EmitPack(unitOf(e, bodies), 0, e, Config{Target: types.X86_64Linux, Profile: types.Release}, &b)
	return b.String(), err
}

// constLines are the IR's private const globals (mk.c.N).
func constLines(ll string) []string {
	return regexp.MustCompile(`(?m)^@mk\.c\.\d+ = .*$`).FindAllString(ll, -1)
}

func TestNonnullConstRepairsEmptyViews(t *testing.T) {
	e := newTestEnv()
	zero16 := make([]byte, 16)
	emptyStr := e.consts.Intern(types.ConstValue{Type: types.Str, Align: 8, Bytes: zero16})
	ints := e.tt.Slice(types.I64)
	emptyInts := e.consts.Intern(types.ConstValue{Type: ints, Align: 8, Bytes: zero16})
	// struct S { n: i64, s: str } with s empty and null: the field is
	// repaired at byte 8.
	st := e.structT("S", types.I64, types.Str)
	sb := make([]byte, 24)
	sb[0] = 5
	emptyField := e.consts.Intern(types.ConstValue{Type: st, Align: 8, Bytes: sb})
	// ?str none: all zero, and none's zero words are not views.
	none := e.consts.Intern(types.ConstValue{Type: e.tt.Optional(types.Str), Align: 8, Bytes: make([]byte, 24)})
	// *u8 from raw.FromAddr(8): a raw pointer keeps its address.
	raw := e.tt.RawPtr(types.U8)
	rb := make([]byte, 8)
	rb[0] = 8
	rawAddr := e.consts.Intern(types.ConstValue{Type: raw, Align: 8, Bytes: rb})
	ll, err := emitErr(e, constFn(e, "EmptyStr", types.Str, emptyStr), constFn(e, "EmptyInts", ints, emptyInts),
		constFn(e, "EmptyField", st, emptyField), constFn(e, "None", e.tt.Optional(types.Str), none),
		constFn(e, "Raw", raw, rawAddr))
	if err != nil {
		t.Fatal(err)
	}
	cs := strings.Join(constLines(ll), "\n")
	for _, want := range []string{
		`c"\01\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00"`,                         // "": data 1 (str's alignment), length 0
		`c"\08\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00"`,                         // []i64: data 8
		`c"\05\00\00\00\00\00\00\00\01\00\00\00\00\00\00\00\00\00\00\00\00\00\00\00"`, // S{5, ""}
		`[24 x i8] zeroinitializer`,                                                   // ?str none, untouched
	} {
		if !strings.Contains(cs, want) && !(strings.HasPrefix(want, "[24") && strings.Contains(cs, strings.Repeat(`\00`, 24))) {
			t.Errorf("no const %s in\n%s", want, cs)
		}
	}
	if strings.Contains(cs, `c"`+strings.Repeat(`\00`, 16)+`"`) || strings.Contains(cs, `[16 x i8] zeroinitializer`) {
		t.Errorf("a 16-byte all-zero view const is still emitted:\n%s", cs)
	}
	if !strings.Contains(fnText(ll, "mk.main.Raw"), "inttoptr (i64 8 to ptr)") {
		t.Errorf("Raw does not return address 8:\n%s", fnText(ll, "mk.main.Raw"))
	}
}

func TestNonnullConstRefusesNullData(t *testing.T) {
	for _, c := range []struct {
		name string
		mk   func(e *testEnv) (types.TypeID, types.ConstID)
		want string
	}{
		{"non-empty str", func(e *testEnv) (types.TypeID, types.ConstID) {
			b := make([]byte, 16)
			b[8] = 3
			return types.Str, e.consts.Intern(types.ConstValue{Type: types.Str, Align: 8, Bytes: b})
		}, "non-empty str with a null data pointer"},
		{"null &i64", func(e *testEnv) (types.TypeID, types.ConstID) {
			r := e.tt.Ref(types.I64)
			return r, e.consts.Intern(types.ConstValue{Type: r, Align: 8, Bytes: make([]byte, 8)})
		}, "null reference"},
		{"null &i64 field", func(e *testEnv) (types.TypeID, types.ConstID) {
			st := e.structT("R", types.I64, e.tt.Ref(types.I64))
			return st, e.consts.Intern(types.ConstValue{Type: st, Align: 8, Bytes: make([]byte, 16)})
		}, "a null &i64"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv()
			ty, id := c.mk(e)
			_, err := emitErr(e, constFn(e, "F", ty, id))
			var ie *InternalError
			if !errors.As(err, &ie) || !strings.Contains(ie.IE.What, c.want) {
				t.Fatalf("err = %v, want an ICE with %q", err, c.want)
			}
		})
	}
}
