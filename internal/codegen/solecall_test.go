// Why: two IR facts make release code faster: a writable, dereferenceable
// sret, and a callee called from one site inlined there.
// What: TestSretWritable, the sole-call tests (SoleCallInlines,
// SoleCallFeatures, SoleCallKeepsImportedCallerSmall, SizeCap, Exclusions,
// WrapperOfLoopStaysOutOfLoop), TestLibcNoBuiltins and
// TestSubMulNoWrap.
// How: soleLeaf and soleCaller build callee and caller shapes, and
// soleMarked reads whether the one call carries the inlining mark.

package codegen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// soleCallUnit is Inner(x: i32, y: i64): Point (sret on Win64), Outer calling
// it once and returning its result, and Twice calling Shared twice.
func soleCallUnit(e *testEnv) *Unit {
	pt := e.structT("Point", types.I32, types.I64)
	in := e.fn("Inner", []types.TypeID{types.I32, types.I64}, pt, 0)
	f := e.body(in)
	f.assign(place(0), mir.Rvalue{Kind: mir.RvAggregate, Agg: mir.AggStruct, Item: e.tt.Get(pt).Item,
		Ops: f.b.AddOperands(mir.Copy(place(1)), mir.Copy(place(2)))}, f.at(2, 3))
	f.ret(f.at(2, 3))
	o := e.body(e.fn("Outer", []types.TypeID{types.I32, types.I64}, pt, 0))
	v := o.call(in, nil, pt, mir.Copy(place(1)), mir.Copy(place(2)))
	o.assign(place(0), use(mir.Copy(place(v))), o.at(4, 3))
	o.ret(o.at(4, 3))
	sh := e.fn("Shared", []types.TypeID{types.I64}, types.I64, 0)
	s := e.body(sh)
	for range smallOps { // past the small-function rule's bound (smallfn.go)
		s.assign(place(0), use(mir.Copy(place(1))), s.at(6, 3))
	}
	s.ret(s.at(6, 3))
	tw := e.body(e.fn("Twice", []types.TypeID{types.I64}, types.I64, 0))
	a := tw.call(sh, nil, types.I64, mir.Copy(place(1)))
	b := tw.call(sh, nil, types.I64, mir.Copy(place(a)))
	tw.assign(place(0), use(mir.Copy(place(b))), tw.at(8, 3))
	tw.ret(tw.at(8, 3))
	return unitOf(e, []*mir.Body{f.b, o.b, s.b, tw.b})
}

// TestSretWritable: an sret parameter is writable and
// dereferenceable(size), as clang gives a C sret, so LLVM's call-slot rule
// writes Inner's result straight into Outer's own sret: at -O2 Outer has no
// memcpy and no alloca left.
func TestSretWritable(t *testing.T) {
	e := newTestEnv()
	u := cCallable(soleCallUnit(e)) // C's sret (internalabi_test.go)
	// Dev: no alwaysinline mark (TestSoleCallInlines), so the call
	// stays under -fno-inline and the copy's fate is MemCpyOpt's alone.
	ll := emit(t, e, u, types.X86_64Windows, types.Dev)
	for _, sym := range []string{"mk.main.Inner", "mk.main.Outer"} {
		line, _, _ := strings.Cut(fnText(ll, sym), "\n")
		if !strings.Contains(line, "sret([16 x i8])") || !strings.Contains(line, "writable") || !strings.Contains(line, "dereferenceable(16)") {
			t.Errorf("%s: sret without writable dereferenceable(16): %s", sym, line)
		}
	}
	bin := needLLVM(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "sret.ll")
	clangC(t, types.X86_64Windows, dir, "sret", ll, "-O0") // writes and verifies sret.ll
	// -O1: MemCpyOpt without the inliner, so the call stays.
	out, err := exec.Command(tool(bin, "clang"), "--target="+types.X86_64Windows.LLVMTriple, "-S", "-emit-llvm", "-O1",
		"-Wno-override-module", "-mllvm", "-inline-threshold=-100000", "-o", "-", src).CombinedOutput()
	if err != nil {
		t.Fatalf("clang: %v\n%s", err, out)
	}
	outer := fnText(strings.ReplaceAll(string(out), "\r\n", "\n"), "mk.main.Outer")
	if outer == "" || strings.Contains(outer, "memcpy") || strings.Contains(outer, "alloca") || !strings.Contains(outer, "@mk.main.Inner(") {
		t.Errorf("Outer still copies Inner's result:\n%s\n%s", outer, out)
	}
}

// TestSoleCallInlines: in release the one call of Inner (its only
// site in the unit) carries alwaysinline; the two calls of Shared and every
// dev call carry none; and a call in a function to itself never does.
func TestSoleCallInlines(t *testing.T) {
	e := newTestEnv()
	u := soleCallUnit(e)
	rel := emit(t, e, u, types.X86_64Linux, types.Release)
	callLine := func(ll, caller, callee string) []string {
		var out []string
		for _, l := range strings.Split(fnText(ll, caller), "\n") {
			if strings.Contains(l, "call ") && strings.Contains(l, "@"+callee+"(") {
				out = append(out, l)
			}
		}
		return out
	}
	attrsOf := func(ll, line string) string {
		i := strings.LastIndex(line, "#")
		if i < 0 {
			return ""
		}
		j := i + 1
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		k := strings.Index(ll, "attributes "+line[i:j]+" = {")
		if k < 0 {
			return ""
		}
		end := strings.Index(ll[k:], "}")
		return ll[k : k+end]
	}
	inner := callLine(rel, "mk.main.Outer", "mk.main.Inner")
	if len(inner) != 1 || !strings.Contains(inner[0]+attrsOf(rel, inner[0]), "alwaysinline") {
		t.Errorf("the sole call of Inner is not alwaysinline: %q", inner)
	}
	shared := callLine(rel, "mk.main.Twice", "mk.main.Shared")
	if len(shared) != 2 {
		t.Fatalf("Twice: %d calls of Shared", len(shared))
	}
	for _, l := range shared {
		if strings.Contains(l+attrsOf(rel, l), "alwaysinline") {
			t.Errorf("a call of the twice-called Shared is alwaysinline: %s", l)
		}
	}
	dev := emit(t, e, u, types.X86_64Linux, types.Dev)
	if strings.Contains(dev, "alwaysinline") {
		t.Errorf("a dev build marks a call alwaysinline")
	}
	clangC(t, types.X86_64Linux, t.TempDir(), "sole", rel, "-O2")

	// A function calling itself once is never marked.
	e2 := newTestEnv()
	rec := e2.fn("Rec", []types.TypeID{types.I64}, types.I64, 0)
	r := e2.body(rec)
	x := r.call(rec, nil, types.I64, mir.Copy(place(1)))
	r.assign(place(0), use(mir.Copy(place(x))), r.at(2, 3))
	r.ret(r.at(2, 3))
	ll := emit(t, e2, unitOf(e2, []*mir.Body{r.b}), types.X86_64Linux, types.Release)
	for _, l := range callLine(ll, "mk.main.Rec", "mk.main.Rec") {
		if strings.Contains(l+attrsOf(ll, l), "alwaysinline") {
			t.Errorf("a self call is alwaysinline: %s", l)
		}
	}
}

// TestLibcNoBuiltins: a function of std.libc carries "no-builtins", so
// LLVM cannot turn its loop into a call of the C name it defines; a
// function elsewhere carries none.
func TestLibcNoBuiltins(t *testing.T) {
	e := newTestEnv()
	lib := e.itemIn("std.libc", types.ItemKey("std.libc", "Len"), "Len", types.KindFunc,
		&types.FuncDecl{Sig: types.Signature{Name: e.in.Intern("Len"), Result: types.Int,
			Params: []types.Param{{Name: e.in.Intern("pa"), Type: types.Int}}}})
	e.items.Get(lib).Decl.(*types.FuncDecl).Sig.Item = lib
	l := e.body(lib)
	l.assign(place(0), use(mir.Copy(place(1))), l.at(2, 3))
	l.ret(l.at(2, 3))
	o := e.body(e.fn("Other", []types.TypeID{types.Int}, types.Int, 0))
	o.assign(place(0), use(mir.Copy(place(1))), o.at(4, 3))
	o.ret(o.at(4, 3))
	ll := emit(t, e, unitOf(e, []*mir.Body{l.b, o.b}), types.X86_64Linux, types.Release)
	if line, _, _ := strings.Cut(fnText(ll, "mk.std.libc.Len"), "\n"); !strings.Contains(line, `"no-builtins"`) {
		t.Errorf("std.libc's function lacks no-builtins: %s", line)
	}
	if line, _, _ := strings.Cut(fnText(ll, "mk.main.Other"), "\n"); strings.Contains(line, `"no-builtins"`) {
		t.Errorf("a function outside std.libc has no-builtins: %s", line)
	}
}

// TestSubMulNoWrap: mir.SubNoWrap and MulNoWrap print sub and mul with the
// one flag of the operand type's signedness, no overflow intrinsic, on
// every target, and clang accepts the module.
func TestSubMulNoWrap(t *testing.T) {
	for _, target := range types.Targets() {
		t.Run(target.Name, func(t *testing.T) {
			e := newTestEnv()
			var bodies []*mir.Body
			want := map[string]string{}
			for _, c := range []struct {
				name string
				op   mir.BinOp
				ty   types.TypeID
				text string
			}{
				{"SubI", mir.SubNoWrap, types.I64, "sub nsw i64"}, {"SubU", mir.SubNoWrap, types.U32, "sub nuw i32"},
				{"MulI", mir.MulNoWrap, types.I16, "mul nsw i16"}, {"MulU", mir.MulNoWrap, types.U64, "mul nuw i64"},
			} {
				f := e.body(e.fn(c.name, []types.TypeID{c.ty, c.ty}, c.ty, 0))
				f.assign(place(0), bin(c.op, mir.Copy(place(1)), mir.Copy(place(2))), f.at(2, 3))
				f.ret(f.at(2, 3))
				bodies = append(bodies, f.b)
				want["mk.main."+c.name] = c.text
			}
			ll := emit(t, e, unitOf(e, bodies), target, types.Release)
			for fn, w := range want {
				body := fnText(ll, fn)
				if strings.Count(body, w+" ") != 1 || strings.Contains(body, "with.overflow") || strings.Contains(body, "nuw nsw") {
					t.Errorf("%s: want one %q and no check in\n%s", fn, w, body)
				}
			}
			clangC(t, target, t.TempDir(), "c59", ll, "-O2")
		})
	}
}

// TestSoleCallFeatures: the rule never inlines a callee with features
// its caller lacks (an alwaysinline call skips LLVM's own check), and keeps
// general-regs-only's negative features equal.
func TestSoleCallFeatures(t *testing.T) {
	for _, c := range []struct {
		callee, caller string
		ok             bool
	}{
		{"", "", true},
		{"", "+avx,+avx2", true},
		{"+avx,+avx2", "", false},
		{"+avx,+avx2,+avx512bw,+avx512f,+evex512", "+avx,+avx2,+avx512bw,+avx512f,+evex512,+sse,+sse2", true},
		{"+avx,+avx2,+avx512bw", "+avx,+avx2,+sse", false},
		{"-x87,-mmx,-sse", "-x87,-mmx,-sse", true},
		{"-x87,-mmx,-sse", "+sse", false},
		{"", "-x87,-mmx,-sse", false},
	} {
		if got := featureSubset(c.callee, c.caller); got != c.ok {
			t.Errorf("featureSubset(%q, %q) = %v, want %v", c.callee, c.caller, got, c.ok)
		}
	}
}

// TestSoleCallKeepsImportedCallerSmall: when another pack calls the
// caller, the rule leaves the caller's one call alone (so ThinLTO can still
// import the small caller there: the hash-sip13-short row,
// tests/bench-lanes/std-hashmod/sip13-short).
func TestSoleCallKeepsImportedCallerSmall(t *testing.T) {
	e := newTestEnv()
	in := e.fn("Leaf", []types.TypeID{types.I64}, types.I64, 0)
	l := e.body(in)
	for range smallOps { // past the small-function rule's bound (smallfn.go)
		l.assign(place(0), use(mir.Copy(place(1))), l.at(2, 3))
	}
	l.ret(l.at(2, 3))
	mid := e.fn("Mid", []types.TypeID{types.I64}, types.I64, 0)
	m := e.body(mid)
	v := m.call(in, nil, types.I64, mir.Copy(place(1)))
	m.assign(place(0), use(mir.Copy(place(v))), m.at(4, 3))
	m.ret(m.at(4, 3))
	o := e.body(e.fn("Far", []types.TypeID{types.I64}, types.I64, 0))
	w := o.call(mid, nil, types.I64, mir.Copy(place(1)))
	o.assign(place(0), use(mir.Copy(place(w))), o.at(6, 3))
	o.ret(o.at(6, 3))
	u := unitOf(e, []*mir.Body{l.b, m.b, o.b})
	ll := emit(t, e, u, types.X86_64Linux, types.Release)
	if !strings.Contains(fnText(ll, "mk.main.Mid"), "alwaysinline") {
		t.Fatalf("one pack: Mid's sole call of Leaf is not marked:\n%s", fnText(ll, "mk.main.Mid"))
	}
	e2 := newTestEnv()
	in = e2.fn("Leaf", []types.TypeID{types.I64}, types.I64, 0)
	l = e2.body(in)
	for range smallOps {
		l.assign(place(0), use(mir.Copy(place(1))), l.at(2, 3))
	}
	l.ret(l.at(2, 3))
	mid = e2.fn("Mid", []types.TypeID{types.I64}, types.I64, 0)
	m = e2.body(mid)
	v = m.call(in, nil, types.I64, mir.Copy(place(1)))
	m.assign(place(0), use(mir.Copy(place(v))), m.at(4, 3))
	m.ret(m.at(4, 3))
	// Far calls Mid twice, so Mid takes neither mark of smallfn.go (it is
	// past the small bound with Leaf counted, and has two call sites).
	o = e2.body(e2.fn("Far", []types.TypeID{types.I64}, types.I64, 0))
	w = o.call(mid, nil, types.I64, mir.Copy(place(1)))
	w2 := o.call(mid, nil, types.I64, mir.Copy(place(w)))
	o.assign(place(0), use(mir.Copy(place(w2))), o.at(6, 3))
	o.ret(o.at(6, 3))
	u = unitOf(e2, []*mir.Body{l.b, m.b, o.b})
	u.Packs = [][]int{{0, 1}, {2}}
	u.Funcs[2].Pack = 1
	ll = emit(t, e2, u, types.X86_64Linux, types.Release)
	if smallMarked(t, ll, "Mid") {
		t.Fatalf("Mid, called twice from another pack, carries a definition mark: %s", defLine(ll, "mk.main.Mid"))
	}
	for _, cl := range soleCallLines(ll, "mk.main.Mid", "mk.main.Leaf") {
		if strings.Contains(cl, "alwaysinline") {
			t.Errorf("Mid, called from another pack, had its call of Leaf marked:\n%s", fnText(ll, "mk.main.Mid"))
		}
	}
}

// TestCrossSoleDispatcherIntoLoop: the std.hash.Sip13 shape
// (std-hashmod/hash-sip13-short). Loop's loop, in pack 1, calls Disp once;
// Disp, in pack 0 and not thin, calls Short (loop-free, past the small
// bound) and Long (a loop) once each. Long stays a call in Disp, so no loop
// ends up in Disp: Disp takes the cross-pack sole-call mark into Loop's
// loop although its effect counts Long's loop. With that mark ThinLTO
// imports Disp whatever its size, so its call of Short is marked although
// another pack calls Disp; its call of Long is not.
func TestCrossSoleDispatcherIntoLoop(t *testing.T) {
	e := newTestEnv()
	short, sh := soleLeaf(e, "Short", smallOps+8, false)
	long, lo := soleLeaf(e, "Long", 1, true)
	d := e.body(e.fn("Disp", []types.TypeID{types.Int}, types.Int, 0))
	for range thinCallerBlocks {
		nb := d.newBlock()
		d.term(mir.Terminator{Kind: mir.TermGoto, Target: nb, Span: d.at(5, 3)})
		d.cur = nb
	}
	v := d.call(short, nil, types.Int, mir.Copy(place(1)))
	w := d.call(long, nil, types.Int, mir.Copy(place(v)))
	d.assign(place(0), use(mir.Copy(place(w))), d.at(6, 3))
	d.ret(d.at(7, 3))
	m := soleCaller(e, "Loop", d.item, true, 0)
	u := unitOf(e, []*mir.Body{sh.b, lo.b, d.b, m.b})
	u.Packs = [][]int{{0, 1, 2}, {3}}
	u.Funcs[3].Pack = 1
	cfg := Config{Target: types.X86_64Linux, Profile: types.Release}
	ll := emitCfg(t, e, u, 0, cfg)
	if !smallMarked(t, ll, "Disp") {
		t.Errorf("Disp, whose looping callee stays a call, lacks the cross-pack mark: %s", defLine(ll, "mk.main.Disp"))
	}
	if !soleMarked(t, ll, "Disp", "Short") {
		t.Errorf("cross-marked Disp: its sole call of Short is not marked:\n%s", fnText(ll, "mk.main.Disp"))
	}
	if soleMarked(t, ll, "Disp", "Long") {
		t.Errorf("Disp is not thin, but its call of the looping Long is marked:\n%s", fnText(ll, "mk.main.Disp"))
	}
	clangC(t, types.X86_64Linux, t.TempDir(), "d0", ll, "-O2")
}

// soleCallLines are the calls of callee in caller's definition, each with
// the text of its attribute group appended.
func soleCallLines(ll, caller, callee string) []string {
	var out []string
	for _, l := range strings.Split(fnText(ll, caller), "\n") {
		if !strings.Contains(l, "call ") || !strings.Contains(l, "@"+callee+"(") {
			continue
		}
		if i := strings.LastIndex(l, "#"); i >= 0 {
			j := i + 1
			for j < len(l) && l[j] >= '0' && l[j] <= '9' {
				j++
			}
			if k := strings.Index(ll, "attributes "+l[i:j]+" = {"); k >= 0 {
				l += " " + ll[k:k+strings.Index(ll[k:], "}")]
			}
		}
		out = append(out, l)
	}
	return out
}

// soleMarked reports whether caller's one call of callee is alwaysinline;
// it fails the test when caller has not exactly one such call.
func soleMarked(t *testing.T, ll, caller, callee string) bool {
	t.Helper()
	ls := soleCallLines(ll, "mk.main."+caller, "mk.main."+callee)
	if len(ls) != 1 {
		t.Fatalf("%s: %d calls of %s, want 1:\n%s", caller, len(ls), callee, fnText(ll, "mk.main."+caller))
	}
	return strings.Contains(ls[0], "alwaysinline")
}

// countLoop appends `for i in 0..n { inside() }` to f, n an int local, and
// records it in f.b.Loops as lower does.
func countLoop(f *fnb, n mir.LocalID, inside func()) {
	i, c := f.local(types.Int), f.local(types.Bool)
	f.assign(place(i), use(mir.Imm(types.Int, 0)), f.at(30, 1))
	head, loop, done := f.newBlock(), f.newBlock(), f.newBlock()
	f.term(mir.Terminator{Kind: mir.TermGoto, Target: head, Span: f.at(30, 1)})
	f.cur = head
	f.assign(place(c), bin(mir.Lt, mir.Copy(place(i)), mir.Copy(place(n))), f.at(30, 5))
	f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(c)),
		Cases: f.b.AddCases(mir.SwitchCase{Value: 0, Target: done}), Default: loop, Span: f.at(30, 1)})
	f.cur = loop
	inside()
	f.assign(place(i), bin(mir.AddWrap, mir.Copy(place(i)), mir.Imm(types.Int, 1)), f.at(30, 9))
	f.term(mir.Terminator{Kind: mir.TermGoto, Target: head, Span: f.at(30, 1)})
	f.cur = done
	f.b.Loops = append(f.b.Loops, mir.LoopInfo{Header: head, Exit: done, Holder: mir.NoLocal, Span: f.at(30, 1), FieldsOf: types.NoType})
}

// soleLeaf is Name(n: int): int, n assignments of its parameter to its
// result (bodyOps n + 1), with a loop when loop is set.
func soleLeaf(e *testEnv, name string, n int, loop bool) (types.ItemID, *fnb) {
	it := e.fn(name, []types.TypeID{types.Int}, types.Int, 0)
	f := e.body(it)
	for range n {
		f.assign(place(0), use(mir.Copy(place(1))), f.at(2, 3))
	}
	if loop {
		countLoop(f, 1, func() {})
	}
	f.ret(f.at(3, 3))
	return it, f
}

// soleCaller is Name(n: int): int calling callee once with n, inside a loop
// when loop is set, after extra empty blocks.
func soleCaller(e *testEnv, name string, callee types.ItemID, loop bool, extra int) *fnb {
	f := e.body(e.fn(name, []types.TypeID{types.Int}, types.Int, 0))
	for range extra {
		nb := f.newBlock()
		f.term(mir.Terminator{Kind: mir.TermGoto, Target: nb, Span: f.at(5, 3)})
		f.cur = nb
	}
	call := func() {
		v := f.call(callee, nil, types.Int, mir.Copy(place(1)))
		f.assign(place(0), use(mir.Copy(place(v))), f.at(6, 3))
	}
	if loop {
		f.assign(place(0), use(mir.Imm(types.Int, 0)), f.at(6, 3))
		countLoop(f, 1, call)
	} else {
		call()
	}
	f.ret(f.at(7, 3))
	return f
}

// TestWrapperOfLoopStaysOutOfLoop: Main's loop calls
// Step, whose one statement calls Advance, which loops. Advance goes into
// the thin Step, but Step then carries Advance's loop and stays a call in
// Main's loop: the nbody shape the thin-caller condition exists for.
func TestWrapperOfLoopStaysOutOfLoop(t *testing.T) {
	e := newTestEnv()
	adv, a := soleLeaf(e, "Advance", 1, true)
	s := soleCaller(e, "Step", adv, false, 0)
	m := soleCaller(e, "Main", s.item, true, 0)
	ll := emit(t, e, unitOf(e, []*mir.Body{a.b, s.b, m.b}), types.X86_64Linux, types.Release)
	if !soleMarked(t, ll, "Step", "Advance") {
		t.Errorf("Step's sole call of the looping Advance is not marked (Step is thin)")
	}
	if soleMarked(t, ll, "Main", "Step") {
		t.Errorf("Main's loop has Step, which carries Advance's loop, marked alwaysinline")
	}
	clangC(t, types.X86_64Linux, t.TempDir(), "r1", ll, "-O2")
}

// TestSoleCallSizeCap: a sole callee larger than
// soleCallOps stays a call, one at the cap is inlined, and the size is the
// effect's: Outer -> Mid -> Big2 -> Big3, with Big2 and Big3 each over
// half the cap, marks only Big2's call of Big3.
func TestSoleCallSizeCap(t *testing.T) {
	for _, c := range []struct {
		ops  int
		want bool
	}{{soleCallOps, true}, {soleCallOps + 1, false}, {5000, false}} {
		e := newTestEnv()
		big, b := soleLeaf(e, "Big", c.ops-1, false)
		o := soleCaller(e, "Outer", big, false, 0)
		ll := emit(t, e, unitOf(e, []*mir.Body{b.b, o.b}), types.X86_64Linux, types.Release)
		if got := soleMarked(t, ll, "Outer", "Big"); got != c.want {
			t.Errorf("callee of %d ops: marked %v, want %v", c.ops, got, c.want)
		}
	}
	e := newTestEnv()
	half := soleCallOps/2 + 10
	b3it, b3 := soleLeaf(e, "Big3", half, false)
	b2 := soleCaller(e, "Big2", b3it, false, 0)
	for range half {
		b2.b.Blocks[0].Stmts = append(b2.b.Blocks[0].Stmts, mir.Statement{Kind: mir.StAssign, Place: place(0),
			Rvalue: use(mir.Copy(place(1))), Span: b2.at(2, 3)})
	}
	mid := soleCaller(e, "Mid", b2.item, false, 0)
	o := soleCaller(e, "Outer", mid.item, false, 0)
	ll := emit(t, e, unitOf(e, []*mir.Body{b3.b, b2.b, mid.b, o.b}), types.X86_64Linux, types.Release)
	if !soleMarked(t, ll, "Big2", "Big3") {
		t.Errorf("Big2's call of Big3 (%d ops) is not marked", half+1)
	}
	if soleMarked(t, ll, "Mid", "Big2") || soleMarked(t, ll, "Outer", "Mid") {
		t.Errorf("a call whose callee's effect exceeds %d ops is marked", soleCallOps)
	}
}

// TestSoleCallExclusions: each of the rule's exclusion
// conditions, alone, keeps the one call unmarked; the same unit without it
// marks the call.
func TestSoleCallExclusions(t *testing.T) {
	for _, c := range []struct {
		name       string
		loopCallee bool
		loopCaller bool
		extra      int
		edit       func(e *testEnv, u *Unit)
		want       bool
	}{
		{name: "plain", want: true},
		{name: "loop-into-thin", loopCallee: true, extra: 1, want: true},
		{name: "flat-loop-into-leaf-spine", loopCallee: true, loopCaller: true, want: true},
		{name: "loop-into-wide", loopCallee: true, extra: thinCallerBlocks, want: false},
		{name: "loop-free-into-loop", loopCaller: true, want: true},
		{name: "loop-free-into-wide", extra: thinCallerBlocks, want: true},
		{name: "addr-taken", edit: func(e *testEnv, u *Unit) { u.Funcs[0].AddrTaken = true }, want: false},
		{name: "spawned", edit: func(e *testEnv, u *Unit) { u.Funcs[0].Spawned = true }, want: false},
		{name: "naked", edit: func(e *testEnv, u *Unit) {
			e.items.Get(u.Funcs[0].Item).Decl.(*types.FuncDecl).Naked = true
		}, want: false},
		{name: "other-pack", edit: func(e *testEnv, u *Unit) {
			u.Packs = [][]int{{1}, {0}}
			u.Funcs[0].Pack = 1
		}, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv()
			leaf, l := soleLeaf(e, "Leaf", smallOps, c.loopCallee) // not small (smallfn.go)
			f := soleCaller(e, "Caller", leaf, c.loopCaller, c.extra)
			u := unitOf(e, []*mir.Body{l.b, f.b})
			if c.edit != nil {
				c.edit(e, u)
			}
			ll := emit(t, e, u, types.X86_64Linux, types.Release)
			if got := soleMarked(t, ll, "Caller", "Leaf"); got != c.want {
				t.Errorf("marked %v, want %v", got, c.want)
			}
		})
	}
}

// TestSoleCallFlatLoopGuards tests each guard of the flat single-loop leaf-spine
// exception (smallFlatLoop, onLeafLoopSpine): a small single-loop helper on the
// unconditional spine of a leaf loop is inlined, while a conditional call in a
// loop, a call in an outer (non-leaf) loop, a nested-loop callee, an oversized
// looping callee, and a callee taking a large mutable state reference stay out
// of line.
func TestSoleCallFlatLoopGuards(t *testing.T) {
	t.Run("conditional-branch-in-loop", func(t *testing.T) {
		e := newTestEnv()
		leaf, l := soleLeaf(e, "Leaf", smallOps, true)
		f := e.body(e.fn("Caller", []types.TypeID{types.Int}, types.Int, 0))
		f.assign(place(0), use(mir.Imm(types.Int, 0)), f.at(6, 3))
		countLoop(f, 1, func() {
			cond := f.local(types.Bool)
			f.assign(place(cond), bin(mir.Eq, mir.Copy(place(1)), mir.Imm(types.Int, 7)), f.at(6, 5))
			thenB, joinB := f.newBlock(), f.newBlock()
			f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
				Cases: f.b.AddCases(mir.SwitchCase{Value: 1, Target: thenB}), Default: joinB, Span: f.at(6, 5)})
			f.cur = thenB
			v := f.call(leaf, nil, types.Int, mir.Copy(place(1)))
			f.assign(place(0), use(mir.Copy(place(v))), f.at(6, 9))
			f.term(mir.Terminator{Kind: mir.TermGoto, Target: joinB, Span: f.at(6, 9)})
			f.cur = joinB
		})
		f.ret(f.at(7, 3))
		ll := emit(t, e, unitOf(e, []*mir.Body{l.b, f.b}), types.X86_64Linux, types.Release)
		if soleMarked(t, ll, "Caller", "Leaf") {
			t.Errorf("conditional call inside a loop was marked alwaysinline")
		}
	})

	t.Run("outer-non-leaf-loop", func(t *testing.T) {
		e := newTestEnv()
		leaf, l := soleLeaf(e, "Leaf", smallOps, true)
		f := e.body(e.fn("Caller", []types.TypeID{types.Int}, types.Int, 0))
		f.assign(place(0), use(mir.Imm(types.Int, 0)), f.at(6, 3))
		countLoop(f, 1, func() {
			countLoop(f, 1, func() {})
			v := f.call(leaf, nil, types.Int, mir.Copy(place(1)))
			f.assign(place(0), use(mir.Copy(place(v))), f.at(6, 9))
		})
		f.ret(f.at(7, 3))
		ll := emit(t, e, unitOf(e, []*mir.Body{l.b, f.b}), types.X86_64Linux, types.Release)
		if soleMarked(t, ll, "Caller", "Leaf") {
			t.Errorf("call in an outer (non-leaf) loop was marked alwaysinline")
		}
	})

	t.Run("nested-loop-callee", func(t *testing.T) {
		e := newTestEnv()
		it := e.fn("Nested", []types.TypeID{types.Int}, types.Int, 0)
		l := e.body(it)
		l.assign(place(0), use(mir.Copy(place(1))), l.at(2, 3))
		countLoop(l, 1, func() { countLoop(l, 1, func() {}) })
		l.ret(l.at(3, 3))
		f := soleCaller(e, "Caller", it, true, 0)
		ll := emit(t, e, unitOf(e, []*mir.Body{l.b, f.b}), types.X86_64Linux, types.Release)
		if soleMarked(t, ll, "Caller", "Nested") {
			t.Errorf("nested-loop callee was marked alwaysinline in a caller loop")
		}
	})

	t.Run("oversized-flat-loop-callee", func(t *testing.T) {
		e := newTestEnv()
		leaf, l := soleLeaf(e, "BigLoop", smallLoopSoleOps, true)
		f := soleCaller(e, "Caller", leaf, true, 0)
		ll := emit(t, e, unitOf(e, []*mir.Body{l.b, f.b}), types.X86_64Linux, types.Release)
		if soleMarked(t, ll, "Caller", "BigLoop") {
			t.Errorf("oversized looping callee (%d ops) was marked alwaysinline", bodyOps(l.b))
		}
	})

	t.Run("large-pointee-param", func(t *testing.T) {
		e := newTestEnv()
		arr := e.tt.Array(types.ConstArg{Kind: types.CAValue, Value: e.intConst(types.Int, 32)}, types.F64) // 256 bytes > 128
		mutArr := e.tt.Mut(arr)
		it := e.fn("BigState", []types.TypeID{mutArr, types.Int}, types.Int, 0)
		l := e.body(it)
		l.assign(place(0), use(mir.Copy(place(2))), l.at(2, 3))
		countLoop(l, 2, func() {})
		l.ret(l.at(3, 3))
		f := e.body(e.fn("Caller", []types.TypeID{mutArr, types.Int}, types.Int, 0))
		f.assign(place(0), use(mir.Imm(types.Int, 0)), f.at(6, 3))
		countLoop(f, 2, func() {
			v := f.call(it, nil, types.Int, mir.Copy(place(1)), mir.Copy(place(2)))
			f.assign(place(0), use(mir.Copy(place(v))), f.at(6, 9))
		})
		f.ret(f.at(7, 3))
		ll := emit(t, e, unitOf(e, []*mir.Body{l.b, f.b}), types.X86_64Linux, types.Release)
		if soleMarked(t, ll, "Caller", "BigState") {
			t.Errorf("looping callee with 256-byte mut pointee was marked alwaysinline")
		}
	})
}
