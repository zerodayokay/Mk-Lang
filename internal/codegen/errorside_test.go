// Why: a read, a write or a borrow of the error side of an E!T must address
// the union's own storage, or a reference into an error's payload points at
// a temporary that dies before it does.
// What: TestErrorSideLayout, TestErrorSideBorrow and TestErrorSideRun.
// How: check's layout places each error's fields by the error alone
// (Small!i64 puts Small's u8 at 4, as the set Small does, though the i64
// ok payload sits at 8), so the SelErr view is the union's address: a
// shared or mut borrow and a write through it are a getelementptr off the
// union with no frame slot, and a call's error edge and AggErr copy whole.

package codegen

import (
	"regexp"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// r1Bodies:
//
//	MkErr(n: u8): Small!i64 { return Small(n) }
//	EdgePay(n: u8): u8 { _ := MkErr(n) else e { return e.Small.0 }; return 0 }
//	ViewPay(x: Small!i64): u8 { return x.err.Small.0 }   (x's error side read through SelErr)
//
// errView is the place of Small's payload through the error side of the
// E!T at base.
func r1Bodies(e *testEnv) ([]*mir.Body, types.TypeID, types.TypeID, types.ItemID) {
	small := e.errItem("Small", types.U8)
	set := e.tt.ErrorSet([]types.ErrorMember{{Item: small}})
	st := e.tt.ErrorSetType(set)
	ut := e.tt.ErrorUnion(set, types.I64)

	mk := e.body(e.fn("MkErr", []types.TypeID{types.U8}, types.I64, set))
	el := mk.local(st)
	mk.assign(place(el), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggError, Item: small, Ops: mk.b.AddOperands(mir.Copy(place(1)))}, mk.at(2, 3))
	mk.assign(place(0), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggErr, Ops: mk.b.AddOperands(mir.Move(place(el)))}, mk.at(2, 3))
	mk.ret(mk.at(2, 3))

	ep := e.body(e.fn("EdgePay", []types.TypeID{types.U8}, types.U8, 0))
	v, er := ep.local(types.I64), ep.local(st)
	okB, errB := ep.newBlock(), ep.newBlock()
	ep.term(mir.Terminator{Kind: mir.TermCall, Callee: direct(mk.item), Args: ep.b.AddArgs(mir.Arg{Operand: mir.Copy(place(1))}),
		Dest: place(v), Target: okB, HasErr: true, Err: mir.ErrorEdge{Kind: mir.EdgeElse, Block: errB, Error: er},
		Spawn: mir.NoLocal, Span: ep.at(4, 8)})
	ep.cur = errB
	ep.assign(place(0), use(mir.Copy(ep.b.Place(er, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelCase, A: uint32(small), Type: st},
		mir.Projection{Kind: mir.ProjField, A: 0, Type: types.U8}))), ep.at(4, 30))
	ep.ret(ep.at(4, 30))
	ep.cur = okB
	ep.assign(place(0), use(mir.Imm(types.U8, 0)), ep.at(5, 3))
	ep.ret(ep.at(5, 3))

	vp := e.body(e.fn("ViewPay", []types.TypeID{ut}, types.U8, 0))
	vp.assign(place(0), use(mir.Copy(vp.b.Place(1, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelErr, Type: st},
		mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelCase, A: uint32(small), Type: st},
		mir.Projection{Kind: mir.ProjField, A: 0, Type: types.U8}))), vp.at(7, 3))
	vp.ret(vp.at(7, 3))
	return []*mir.Body{mk.b, ep.b, vp.b}, ut, st, small
}

func errView(base []mir.Projection, st types.TypeID, small types.ItemID) []mir.Projection {
	return append(base, mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelErr, Type: st},
		mir.Projection{Kind: mir.ProjDowncast, Sel: mir.SelCase, A: uint32(small), Type: st},
		mir.Projection{Kind: mir.ProjField, A: 0, Type: types.U8})
}

// TestErrorSideLayout: an error's fields sit at the same offsets in its set
// and in every E!T that holds it, whatever the ok type's alignment, and the
// set is never larger than the union.
func TestErrorSideLayout(t *testing.T) {
	e := newTestEnv()
	_, ut, st, _ := r1Bodies(e)
	lu, ls := e.Layout(ut), e.Layout(st)
	if lu.Variants[1].Fields[0].Offset != 4 || ls.Variants[0].Fields[0].Offset != 4 || lu.Variants[0].Fields[0].Offset != 8 {
		t.Fatalf("Small!i64: Small's u8 at %d, the set's at %d, the ok i64 at %d; want 4, 4, 8",
			lu.Variants[1].Fields[0].Offset, ls.Variants[0].Fields[0].Offset, lu.Variants[0].Fields[0].Offset)
	}
	g := &packGen{tt: e.tt, env: e, items: e.items}
	if !g.errViewOK(ut, st) {
		t.Fatal("errViewOK(Small!i64, Small) is false")
	}
}

var (
	gep4    = regexp.MustCompile(`getelementptr inbounds i8, ptr (%[\w.]+), i64 4,`)
	setSlot = regexp.MustCompile(`alloca [^\n]*align 4,`)
)

// inPlace reports whether body takes a getelementptr of 4 off x itself or
// off the pointer loaded from x's home.
func inPlace(body string) bool {
	m := gep4.FindStringSubmatch(body)
	return m != nil && (m[1] == "%p0" || strings.Contains(body, m[1]+" = load ptr, "))
}

// TestErrorSideBorrow: &(*x).err.Small.0, mut (*x).err.Small.0 and a store
// through the view address the union behind x: a getelementptr of 4 off the
// parameter, with no slot of the set and no copy, in every profile.
func TestErrorSideBorrow(t *testing.T) {
	e := newTestEnv()
	_, ut, st, small := r1Bodies(e)
	deref := func(t types.TypeID) []mir.Projection { return []mir.Projection{{Kind: mir.ProjDeref, Type: t}} }
	sh := e.body(e.fn("RefView", []types.TypeID{e.tt.Ref(ut)}, e.tt.Ref(types.U8), 0))
	sh.assign(place(0), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: sh.b.Place(1, errView(deref(ut), st, small)...)}, sh.at(1, 3))
	sh.ret(sh.at(1, 3))
	mu := e.body(e.fn("MutView", []types.TypeID{e.tt.Mut(ut)}, e.tt.Mut(types.U8), 0))
	mu.assign(place(0), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Exclusive, Place: mu.b.Place(1, errView(deref(ut), st, small)...)}, mu.at(2, 3))
	mu.ret(mu.at(2, 3))
	w := e.body(e.fn("WriteView", []types.TypeID{e.tt.Mut(ut)}, types.Void, 0))
	w.assign(w.b.Place(1, errView(deref(ut), st, small)...), use(mir.Imm(types.U8, 1)), w.at(3, 3))
	w.ret(w.at(3, 3))
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		ll := emit(t, e, unitOf(e, []*mir.Body{sh.b, mu.b, w.b}), types.X86_64Linux, prof)
		for _, fn := range []string{"mk.main.RefView", "mk.main.MutView", "mk.main.WriteView"} {
			body := fnText(ll, fn)
			if body == "" {
				t.Fatalf("%v: no %s in\n%s", prof, fn, ll)
			}
			// The parameter's own home is a pointer slot (align 8); a copy
			// of the error side would be a slot of the set (align 4).
			if setSlot.MatchString(body) || strings.Contains(body, "memcpy") || !inPlace(body) {
				t.Errorf("%v: %s does not address the union in place:\n%s", prof, fn, body)
			}
		}
	}
}

// TestErrorSideRun builds the bodies for every target and runs EdgePay and
// ViewPay where a runner exists.
func TestErrorSideRun(t *testing.T) {
	e := newTestEnv()
	bodies, _, _, _ := r1Bodies(e)
	for _, target := range types.Targets() {
		ll := emit(t, e, unitOf(e, bodies), target, types.Dev)
		clangC(t, target, t.TempDir(), "r1", ll, "-O2")
	}
	for _, target := range runTargets {
		for _, opt := range []string{"-O0", "-O2"} {
			t.Run(target.Name+opt, func(t *testing.T) {
				if _, why := hostRunner(target); why != "" {
					skipRunner(t, why)
				}
				ll := emit(t, e, cCallable(unitOf(e, bodies)), target, types.Dev) // the harness calls them
				dir := t.TempDir()
				osIR, kernel := harnessOS(target)
				// ViewPay's argument: code of Small at 0, payload 77 at 4.
				code := types.ErrorCode(types.ItemKey("main", "Small"))
				view := "  %r1 = call i8 @mk.main.ViewPay(i64 " + itoa(int(code)|77<<32) + ", i64 0)\n"
				decl := "declare i8 @mk.main.ViewPay(i64, i64)\n"
				if target.OS == types.OSWindows {
					view = "  %x = alloca [16 x i8], align 8\n  store i32 " + itoa(int(code)) + ", ptr %x, align 8\n" +
						"  %xp = getelementptr inbounds i8, ptr %x, i64 4\n  store i8 77, ptr %xp, align 4\n" +
						"  %r1 = call i8 @mk.main.ViewPay(ptr %x)\n"
					decl = "declare i8 @mk.main.ViewPay(ptr)\n"
				}
				// A u8 argument or result carries zeroext where C extends one
				// EdgePay(u8): u8 and ViewPay's u8 result.
				edge := "declare i8 @mk.main.EdgePay(i8)\n"
				edgeCall := "  %r0 = call i8 @mk.main.EdgePay(i8 42)\n"
				if cExtends(target) {
					edge = "declare zeroext i8 @mk.main.EdgePay(i8 zeroext)\n"
					edgeCall = "  %r0 = call zeroext i8 @mk.main.EdgePay(i8 zeroext 42)\n"
					decl = strings.Replace(decl, "declare i8", "declare zeroext i8", 1)
					view = strings.Replace(view, "call i8", "call zeroext i8", 1)
				}
				h := osIR + harnessRT + decl + edge + `
define void @"mk.std.task.Start"(ptr %sp) noreturn nounwind {
` + edgeCall + `  %w0 = zext i8 %r0 to i64
  call void @h_num(i64 %w0)
  call void @h_write(ptr @s.nl, i64 1)
` + view + `  %w1 = zext i8 %r1 to i64
  call void @h_num(i64 %w1)
  call void @h_write(ptr @s.nl, i64 1)
  call void @h_exit(i32 0)
  unreachable
}
`
				objs := []string{clangC(t, target, dir, "support", supportLL(t, target), "-O2"),
					clangC(t, target, dir, "pack", ll, opt), clangC(t, target, dir, "harness", h, "-O0")}
				out, code2 := runExe(t, target, linkExe(t, target, dir, objs, linkOpts{kernel: kernel}), "")
				if code2 != 0 || out != "42\n77\n" {
					t.Fatalf("exit %d, output %q; want 42 and 77", code2, out)
				}
			})
		}
	}
}
