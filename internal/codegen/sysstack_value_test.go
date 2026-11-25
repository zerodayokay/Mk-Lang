// Why: a stack-free assembly stub (one load, store or locked instruction and
// ret) is called through a fn value so that it runs on the task's stack:
// a direct call of the bodiless extern goes through mk_call_system, about
// 125 instructions (MKH-480). The reference documents the fn value as the
// way; this pins it.
// What: TestSystemStackFnValue: a call of an extern through a fn value never
// names SystemStack or mk_call_system, in dev and release, while a direct
// call of the same extern does; at -O2 the value call is folded to a direct
// call or jump to the stub's own symbol.
// How: hand-built bodies emitted for x86_64-windows and x86_64-linux; the
// release IR is compiled with clang -O2 -S and CallValue's assembly read.

package codegen

import (
	"fmt"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// stubBodies, with std.task.SystemStack in the build:
//
//	extern Stub(p: *u32): u32                                 (outside std)
//	CallDirect(p: *u32): u32 { return Stub(p) }               (system stack)
//	CallValue(p: *u32): u32 { f := Stub; return f(p) }        (task stack)
func stubBodies(e *testEnv) ([]*mir.Body, []types.ItemID) {
	sysStack := e.structIn("std.task", "SysStack", e.tt.RawPtr(types.U8), types.U64, types.U64, types.U64)
	ss := e.fnIn("std.task", "", "SystemStack", nil, e.tt.RawPtr(sysStack), false)
	done := e.fnIn("std.task", "", "SystemStackDone", nil, types.Void, false)
	pu := e.tt.RawPtr(types.U32)
	stub := e.fn("Stub", []types.TypeID{pu}, types.U32, 0)
	fd := e.items.Get(stub).Decl.(*types.FuncDecl)
	fd.Sig.Extern, fd.Bodiless = true, true
	stubT := e.tt.Fn(types.FnType{Params: []types.FnParam{{Type: pu}}, Result: types.U32})

	cd := e.body(e.fn("CallDirect", []types.TypeID{pu}, types.U32, 0))
	cd.callTo(direct(stub), place(0), mir.NoLocal, cd.at(2, 10), mir.Copy(place(1)))
	cd.ret(cd.at(2, 3))

	cv := e.body(e.fn("CallValue", []types.TypeID{pu}, types.U32, 0))
	f := cv.local(stubT)
	cv.assign(place(f), mir.Rvalue{Kind: mir.RvFnRef, Fn: mir.FnRef{Item: stub}, Type: stubT}, cv.at(4, 5))
	cv.callTo(mir.Callee{Kind: mir.CallValue, Value: mir.Copy(place(f))}, place(0), mir.NoLocal, cv.at(5, 12), mir.Copy(place(1)))
	cv.ret(cv.at(5, 5))
	return []*mir.Body{cd.b, cv.b}, []types.ItemID{ss, done, stub}
}

// TestSystemStackFnValue: the direct call of Stub switches stacks in both
// profiles; the call through the fn value never does, and in release clang
// -O2 turns it into a direct call or tail jump to Stub with no
// mk_call_system and no indirect call.
func TestSystemStackFnValue(t *testing.T) {
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			t.Run(fmt.Sprintf("%s/%d", target.Name, prof), func(t *testing.T) {
				e := newTestEnv()
				bodies, bodiless := stubBodies(e)
				ll := emit(t, e, unitOf(e, bodies, bodiless...), target, prof)
				if txt := fnText(ll, "mk.main.CallDirect"); !strings.Contains(txt, "@mk_call_system(") ||
					!strings.Contains(txt, "@mk.std.task.SystemStack()") {
					t.Errorf("the direct call of the extern does not switch to the system stack:\n%s", txt)
				}
				txt := fnText(ll, "mk.main.CallValue")
				if txt == "" || strings.Contains(txt, "SystemStack") || strings.Contains(txt, "mk_call_system") {
					t.Errorf("the call through the fn value switches stacks:\n%s", txt)
				}
				if prof != types.Release {
					return
				}
				asm := runTool(t, tool(needLLVM(t), "clang"), "--target="+target.LLVMTriple, "-S", "-O2",
					"-Wno-override-module", "-o", "-", writeLL(t, ll))
				body := asmFunc(asm, "mk.main.CallValue")
				if body == "" {
					t.Fatalf("no CallValue in the assembly:\n%s", asm)
				}
				direct := false
				for _, line := range strings.Split(body, "\n") {
					f := strings.Fields(line)
					if len(f) < 2 || !strings.HasPrefix(f[0], "call") && !strings.HasPrefix(f[0], "jmp") {
						continue
					}
					if strings.HasPrefix(f[1], "*") {
						t.Errorf("CallValue calls indirectly at -O2: %s\n%s", line, body)
					}
					if strings.TrimSuffix(f[1], "@PLT") == "Stub" {
						direct = true
					}
				}
				if !direct || strings.Contains(body, "mk_call_system") {
					t.Errorf("CallValue at -O2 is not a direct call of Stub on the task stack:\n%s", body)
				}
			})
		}
	}
}

// asmFunc is the assembly of the function whose label is sym (quoted or
// not), up to the next .Lfunc_end, "" when it is not there.
func asmFunc(asm, sym string) string {
	i := strings.Index(asm, "\n\""+sym+"\":")
	if i < 0 {
		i = strings.Index(asm, "\n"+sym+":")
	}
	if i < 0 {
		return ""
	}
	end := strings.Index(asm[i:], ".Lfunc_end")
	if end < 0 {
		return asm[i:]
	}
	return asm[i : i+end]
}
