// Why: a deep recursion must reach the stack guard, so the optimizer must not
// turn a recursion that keeps a local alive across the call into a loop.
// What: TestStackOverflowPanics: such a recursion, emitted in dev
// (-O0) and release (-O2), stays a recursion and faults.
// How: the harness has a stub runtime with no fault handler, so the run ends
// with the OS's stack fault (Windows STATUS_STACK_OVERFLOW, Linux SIGSEGV).
// Turning that fault into `panic: stack limit at <site>` is std/rt's, tested
// by the driver's TestStackOverflowIsPanic and by
// tests/programs/panics/stack_main.mk and stack_frame.mk.

package codegen

import (
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// deepBody is tests/programs/panics/stack_main.mk's Deep:
//
//	Deep(n: int): int {
//	    if n < 0 { return 0 }
//	    var pad: [256]u8 = [0; 256]
//	    pad[n % 256] = Trunc[u8](n)
//	    r := Deep(n + 1)
//	    return r + int(pad[(n + 1) % 256])
//	}
//
// The index checks lower would emit are left out: both indexes are in
// bounds for n >= 0, and bounds checks are not what this test is about.
func deepBody(e *testEnv) *mir.Body {
	n256 := types.ConstArg{Kind: types.CAValue, Value: e.intConst(types.Int, 256)}
	arr := e.tt.Array(n256, types.U8)
	item := e.fn("Deep", []types.TypeID{types.Int}, types.Int, 0)
	f := e.body(item)
	c, pad, i1, n1, r, i2, v, w := f.local(types.Bool), f.local(arr), f.local(types.Int), f.local(types.Int),
		f.local(types.Int), f.local(types.Int), f.local(types.U8), f.local(types.Int)
	f.assign(place(c), bin(mir.Lt, mir.Copy(place(1)), mir.Imm(types.Int, 0)), f.at(6, 8))
	base, body, after := f.newBlock(), f.newBlock(), f.newBlock()
	f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(c)),
		Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: base}), Default: body, Span: f.at(6, 5)})
	f.cur = base
	f.assign(place(0), use(mir.Imm(types.Int, 0)), f.at(7, 9))
	f.ret(f.at(7, 9))
	f.cur = body
	f.assign(place(pad), mir.Rvalue{Kind: mir.RvRepeat, A: mir.Imm(types.U8, 0), Len: n256}, f.at(9, 5))
	f.assign(place(i1), bin(mir.Rem, mir.Copy(place(1)), mir.Imm(types.Int, 256)), f.at(10, 9))
	f.assign(f.b.Place(pad, mir.Projection{Kind: mir.ProjIndex, A: uint32(i1), Type: types.U8}),
		mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastTrunc, A: mir.Copy(place(1)), Type: types.U8}, f.at(10, 5))
	f.assign(place(n1), bin(mir.Add, mir.Copy(place(1)), mir.Imm(types.Int, 1)), f.at(11, 15))
	f.term(mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: item}},
		Args: f.b.AddArgs(mir.Arg{Operand: mir.Copy(place(n1))}), Dest: place(r), Target: after,
		Spawn: mir.NoLocal, Span: f.at(11, 10)})
	f.cur = after
	f.assign(place(i2), bin(mir.Rem, mir.Copy(place(n1)), mir.Imm(types.Int, 256)), f.at(12, 20))
	f.assign(place(v), use(mir.Copy(f.b.Place(pad, mir.Projection{Kind: mir.ProjIndex, A: uint32(i2), Type: types.U8}))), f.at(12, 16))
	f.assign(place(w), mir.Rvalue{Kind: mir.RvCast, Cast: mir.CastLossless, A: mir.Copy(place(v)), Type: types.Int}, f.at(12, 16))
	f.assign(place(0), bin(mir.Add, mir.Copy(place(r)), mir.Copy(place(w))), f.at(12, 12))
	f.ret(f.at(12, 5))
	return f.b
}

// TestStackOverflowPanics: Deep(1), emitted in dev and release and
// compiled at -O0 and -O2, faults on the stack guard of a 512 KiB stack
// (Windows exit status 0xC00000FD, Linux 139), and never reaches the exit
// status 42 that follows the call. A recursion turned into a loop would
// hang until runExe's deadline; one folded away would exit 42.
func TestStackOverflowPanics(t *testing.T) {
	for _, target := range runTargets {
		for _, p := range []struct {
			profile types.Profile
			opt     string
		}{{types.Dev, "-O0"}, {types.Release, "-O2"}} {
			t.Run(target.Name+p.opt, func(t *testing.T) {
				if _, why := hostRunner(target); why != "" {
					skipRunner(t, why)
				}
				e := newTestEnv()
				ll := emit(t, e, unitOf(e, []*mir.Body{deepBody(e)}), target, p.profile)
				dir := t.TempDir()
				osIR, kernel := harnessOS(target)
				extraDecl, setGuarantee := "", ""
				if target.OS == types.OSWindows {
					// Reserve stack for Windows's default unhandled-exception
					// dispatcher so hitting the 512 KiB guard page exits with
					// STATUS_STACK_OVERFLOW (0xC00000FD) rather than a secondary
					// 0xC0000005 fault inside UnhandledExceptionFilter.
					kernel = append(kernel, "SetThreadStackGuarantee")
					extraDecl = "declare i32 @SetThreadStackGuarantee(ptr) nounwind\n"
					setGuarantee = "  %g = alloca i32, align 4\n  store i32 65536, ptr %g, align 4\n  %sg = call i32 @SetThreadStackGuarantee(ptr %g)\n"
				}
				h := osIR + harnessRT + extraDecl + `
declare i64 @mk.main.Deep(i64)

define void @"mk.std.task.Start"(ptr %sp) noreturn nounwind {
` + setGuarantee + `  %r = call i64 @mk.main.Deep(i64 1)
  call void @h_exit(i32 42)
  unreachable
}
`
				objs := []string{
					clangC(t, target, dir, "support", supportLL(t, target), "-O2"),
					clangC(t, target, dir, "pack", ll, p.opt),
					clangC(t, target, dir, "harness", h, "-O0"),
				}
				if target.OS == types.OSWindows {
					exe := linkExe(t, target, dir, objs, linkOpts{stack: 512 << 10, kernel: kernel, release: p.profile == types.Release})
					if _, code := runExe(t, target, exe, ""); uint32(code) != 0xC00000FD {
						t.Fatalf("exit status %#x, want STATUS_STACK_OVERFLOW 0xC00000FD", uint32(code))
					}
					return
				}
				exe := linkExe(t, target, dir, objs, linkOpts{kernel: kernel, release: p.profile == types.Release})
				if _, code := runExe(t, target, exe, "ulimit -s 512;"); code != 139 {
					t.Fatalf("exit status %d, want 139 (SIGSEGV on the stack guard)", code)
				}
			})
		}
	}
}
