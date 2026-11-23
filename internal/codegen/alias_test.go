// Why: when a target's ABI does not pass a slice's data pointer as an LLVM
// ptr parameter (Win64 C ABI Indirect, AArch64 [2 x i64] array, or System V
// byval), LLVM cannot attach parameter-level noalias to it; scoped alias
// metadata replaces it.
//
// What: TestAliasScopes, TestAliasMarkDropped and TestAliasScopesAarch64Loop.
//
// How: the accesses through a mut []T, and through the str and Freeze []T
// parameters beside it, must carry what LLVM's inliner writes for a noalias
// argument: one domain, one scope per parameter declared in the entry block
// (llvm.experimental.noalias.scope.decl), !alias.scope of its own scope and
// !noalias of the scopes it is paired with (a mut []T against every other;
// two shared slices never). Clang -O2/-O3 verifies that the metadata removes
// dead stores and loop runtime alias checks and vectorizes AArch64 loops.

package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// aliasKern is Name(b: B, c: mut []E, i: int, j: int): E {
// c[i] = 1; t := b[j]; c[i] = 2; return t }, with E the element of B
// (u8 for a str), or E its first field when B's element is a struct
// (b[j].0): the first store is dead exactly when the load of b[j] cannot
// read c[i]. cVar makes c a var parameter, which is never tagged.
func aliasKern(e *testEnv, name string, bt types.TypeID, cVar bool) *mir.Body {
	el := types.TypeID(types.U8)
	bty := e.tt.Get(bt)
	var fieldIdx bool
	if bty.Kind != types.KStr {
		el = bty.Elem
		if ety := e.tt.Get(bty.Elem); ety.Kind == types.KNamed {
			el = e.Layout(bty.Elem).Fields[0].Type
			fieldIdx = true
		}
	}
	ms := e.tt.MutSlice(el)
	f := e.body(e.fn(name, []types.TypeID{bt, ms, types.Int, types.Int}, el, 0))
	f.b.Locals[2].Var = cVar
	ci := f.b.Place(2, mir.Projection{Kind: mir.ProjIndex, A: 3, Type: el})
	var bj mir.Place
	if fieldIdx {
		bj = f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: bty.Elem},
			mir.Projection{Kind: mir.ProjField, A: 0, Type: el})
	} else {
		bj = f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: el})
	}
	f.assign(ci, use(mir.Imm(el, 1)), f.at(2, 3))
	f.assign(place(0), use(mir.Copy(bj)), f.at(3, 3))
	f.assign(ci, use(mir.Imm(el, 2)), f.at(4, 3))
	f.ret(f.at(5, 3))
	return f.b
}

// aliasTestUnit builds the positive and negative kernels of TestAliasScopes.
func aliasTestUnit(e *testEnv) *Unit {
	cell := e.structT("CellInt", types.Int)
	e.noFreeze[cell] = true
	cell32 := e.structT("CellI32", types.I32)
	e.noFreeze[cell32] = true
	pair := e.structT("PairInt", types.Int, types.Int)
	bodies := []*mir.Body{
		aliasKern(e, "KernPair", e.tt.Slice(pair), false),
		aliasKern(e, "Kern", e.tt.Slice(types.Int), false),
		aliasKern(e, "KernStr", types.Str, false),
		aliasKern(e, "KernVar", e.tt.Slice(types.Int), true),
		aliasKern(e, "KernCell", e.tt.Slice(cell), false),
		aliasKern(e, "KernCellI32", e.tt.Slice(cell32), false),
	}
	// Two shared slices only: Shared(a: []int, b: []int, i: int): int {
	// return a[i]; return b[i] } is never tagged.
	sl := e.tt.Slice(types.Int)
	sf := e.body(e.fn("Shared", []types.TypeID{sl, sl, types.Int}, types.Int, 0))
	sf.assign(place(0), use(mir.Copy(sf.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 3, Type: types.Int}))), sf.at(2, 3))
	sf.assign(place(0), use(mir.Copy(sf.b.Place(2, mir.Projection{Kind: mir.ProjIndex, A: 3, Type: types.Int}))), sf.at(3, 3))
	sf.ret(sf.at(4, 3))
	bodies = append(bodies, sf.b)
	// An export extern body is never tagged (C may call it with any
	// views): KernX is Kern's body with Sig.Extern set.
	kx := aliasKern(e, "KernX", e.tt.Slice(types.Int), false)
	e.items.Get(kx.Item).Decl.(*types.FuncDecl).Sig.Extern = true
	bodies = append(bodies, kx)
	// Two shared slices beside a mut one: Three(a: []int, b: []int, c: mut
	// []int, i: int): int { c[i] = 1; return a[i]; return b[i] }. a and b
	// are each paired with c only, never with each other.
	ms := e.tt.MutSlice(types.Int)
	tf := e.body(e.fn("Three", []types.TypeID{sl, sl, ms, types.Int}, types.Int, 0))
	tf.assign(tf.b.Place(3, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: types.Int}), use(mir.Imm(types.Int, 1)), tf.at(2, 3))
	tf.assign(place(0), use(mir.Copy(tf.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: types.Int}))), tf.at(3, 3))
	tf.assign(place(0), use(mir.Copy(tf.b.Place(2, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: types.Int}))), tf.at(4, 3))
	tf.ret(tf.at(5, 3))
	bodies = append(bodies, tf.b)
	u := unitOf(e, bodies)
	for i := range u.Funcs {
		if u.Funcs[i].Item == kx.Item {
			u.Funcs[i].Symbol = "KernX"
		}
	}
	return u
}

func TestAliasScopes(t *testing.T) {
	decl := regexp.MustCompile(`call void @llvm\.experimental\.noalias\.scope\.decl\(metadata (![0-9]+)\)`)
	tagged := regexp.MustCompile(`(store i(?:64|32|8) [12]|load i(?:64|32|8)), ptr %v[0-9]+, align [148](?:, !noundef ![0-9]+)?, !alias\.scope (![0-9]+), !noalias (![0-9]+)`)
	tagged3 := regexp.MustCompile(`(store i64 1|load i64), ptr %v[0-9]+, align 8(?:, !noundef ![0-9]+)?, !alias\.scope (![0-9]+), !noalias (![0-9]+)`)
	stores := regexp.MustCompile(`store i(?:64|32|8) [12], ptr`)

	checkScoped := func(t *testing.T, target types.Target, ll string) {
		t.Helper()
		md := map[string]string{}
		for _, m := range regexp.MustCompile(`(?m)^(![0-9]+) = (.*)$`).FindAllStringSubmatch(ll, -1) {
			md[m[1]] = m[2]
		}
		scopeName := func(list string) string { // !{!s} -> the scope's name
			s := strings.TrimSuffix(strings.TrimPrefix(md[list], "!{"), "}")
			m := regexp.MustCompile(`"([^"]*)"\}$`).FindStringSubmatch(md[s])
			if m == nil || !strings.HasPrefix(md[s], "distinct !{"+s+", ") {
				t.Fatalf("%s = %s is not a distinct self-referential scope", s, md[s])
			}
			return m[1]
		}
		members := func(list string) []string {
			return strings.Split(strings.TrimSuffix(strings.TrimPrefix(md[list], "!{"), "}"), ", ")
		}
		for _, k := range []string{"Kern", "KernStr", "KernPair"} {
			fn := fnText(ll, "mk.main."+k)
			ds := decl.FindAllStringSubmatch(fn, -1)
			if len(ds) != 2 {
				t.Fatalf("%s: want 2 scope declarations (b and c), got %d:\n%s", k, len(ds), fn)
			}
			b, c := ds[0][1], ds[1][1]
			if scopeName(b) != "mk.main."+k+": param 0" || scopeName(c) != "mk.main."+k+": param 1" {
				t.Errorf("%s: scope names %q, %q", k, scopeName(b), scopeName(c))
			}
			acc := tagged.FindAllStringSubmatch(fn, -1)
			if len(acc) != 3 {
				t.Fatalf("%s: want 3 tagged accesses (two stores to c, a load of b), got %d:\n%s", k, len(acc), fn)
			}
			for _, a := range acc {
				own, other := c, b
				if strings.HasPrefix(a[1], "load") {
					own, other = b, c
				}
				if a[2] != own || a[3] != other {
					t.Errorf("%s: %s has !alias.scope %s !noalias %s, want %s and %s", k, a[1], a[2], a[3], own, other)
				}
			}
			if !strings.Contains(ll, "declare void @llvm.experimental.noalias.scope.decl(metadata)") {
				t.Errorf("the intrinsic is not declared")
			}
		}
		for _, k := range []string{"mk.main.KernVar", "mk.main.KernCell", "mk.main.KernCellI32", "mk.main.Shared", "KernX"} {
			fn := fnText(ll, k)
			if fn == "" {
				t.Fatalf("no %s in the pack", k)
			}
			if strings.Contains(fn, "alias.scope") || strings.Contains(fn, "noalias.scope.decl") {
				t.Errorf("%s is tagged, but its pair is not proved disjoint:\n%s", k, fn)
			}
		}
		// Three: a's and b's loads carry !noalias of c's scope alone, and c's
		// store !noalias of a's and b's.
		three := fnText(ll, "mk.main.Three")
		ds := decl.FindAllStringSubmatch(three, -1)
		if len(ds) != 3 {
			t.Fatalf("Three: want 3 scope declarations (a, b and c), got %d:\n%s", len(ds), three)
		}
		sa, sb, sc := members(ds[0][1])[0], members(ds[1][1])[0], members(ds[2][1])[0]
		acc3 := tagged3.FindAllStringSubmatch(three, -1)
		if len(acc3) != 3 {
			t.Fatalf("Three: want 3 tagged accesses, got %d:\n%s", len(acc3), three)
		}
		want3 := []struct{ own, noalias string }{{sc, sa + ", " + sb}, {sa, sc}, {sb, sc}}
		for i, a := range acc3 {
			if own, na := strings.Join(members(a[2]), ", "), strings.Join(members(a[3]), ", "); own != want3[i].own || na != want3[i].noalias {
				t.Errorf("Three: %s has scope %s and noalias {%s}, want %s and {%s}", a[1], own, na, want3[i].own, want3[i].noalias)
			}
		}
		// The fact moves code: after clang -O2 the first store to c[i] is dead
		// in Kern, KernStr and KernPair (the load of b[j] cannot read it), and
		// both stay in KernVar, KernCell and KernCellI32, which carry no fact.
		dir := t.TempDir()
		clangC(t, target, dir, "alias", ll, "-O2 -S -emit-llvm")
		opt, err := os.ReadFile(filepath.Join(dir, "alias.o"))
		if err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]int{"Kern": 1, "KernStr": 1, "KernPair": 1, "KernVar": 2, "KernCell": 2, "KernCellI32": 2} {
			if n := len(stores.FindAllString(fnText(string(opt), "mk.main."+k), -1)); n != want {
				t.Errorf("%s after clang -O2: %d stores to c[i], want %d:\n%s", k, n, want, fnText(string(opt), "mk.main."+k))
			}
		}
	}

	// Win64 C ABI (Indirect slices) and AArch64 (Direct [2 x i64] slices) both
	// emit scoped alias metadata.
	for _, tc := range []struct {
		name     string
		target   types.Target
		callable bool
	}{
		{"x86_64-windows-cabi", types.X86_64Windows, true},
		{"aarch64-linux", types.Aarch64Linux, false},
		{"aarch64-macos", types.Aarch64Macos, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv()
			u := aliasTestUnit(e)
			if tc.callable {
				u = cCallable(u)
			}
			checkScoped(t, tc.target, emit(t, e, u, tc.target, types.Release))
		})
	}

	// System V in registers and Win64's internal ABI (abi.LowerInternal)
	// already pass the data pointers as noalias LLVM ptr parameters, so they
	// emit no redundant scoped alias metadata.
	for _, target := range []types.Target{types.X86_64Linux, types.X86_64Macos, types.X86_64Windows} {
		t.Run(target.Name+"-direct-ptr", func(t *testing.T) {
			e := newTestEnv()
			u := aliasTestUnit(e)
			ll := emit(t, e, u, target, types.Release)
			if strings.Contains(ll, "alias.scope") || strings.Contains(ll, "noalias.scope.decl") {
				t.Errorf("%s: scoped alias metadata beside the noalias parameters", target.Name)
			}
			fn := fnText(ll, "mk.main.Kern")
			if !strings.Contains(fn, "ptr noundef nonnull noalias readonly %p0") || !strings.Contains(fn, "ptr noundef nonnull noalias %p2") {
				t.Errorf("%s Kern: expected direct noalias ptr parameters:\n%s", target.Name, fn)
			}
		})
	}

	// System V stack-spilled (byval) slice parameters lack parameter-level
	// noalias on their data pointers and get scoped alias metadata.
	t.Run("x86_64-linux-byval", func(t *testing.T) {
		e := newTestEnv()
		sl, ms := e.tt.Slice(types.Int), e.tt.MutSlice(types.Int)
		f := e.body(e.fn("Spilled", []types.TypeID{sl, sl, sl, sl, ms, types.Int, types.Int}, types.Int, 0))
		ci := f.b.Place(5, mir.Projection{Kind: mir.ProjIndex, A: 6, Type: types.Int})
		bj := f.b.Place(4, mir.Projection{Kind: mir.ProjIndex, A: 7, Type: types.Int})
		f.assign(ci, use(mir.Imm(types.Int, 1)), f.at(2, 3))
		f.assign(place(0), use(mir.Copy(bj)), f.at(3, 3))
		f.assign(ci, use(mir.Imm(types.Int, 2)), f.at(4, 3))
		f.ret(f.at(5, 3))
		ll := emit(t, e, unitOf(e, []*mir.Body{f.b}), types.X86_64Linux, types.Release)
		fn := fnText(ll, "mk.main.Spilled")
		if ds := decl.FindAllStringSubmatch(fn, -1); len(ds) != 2 {
			t.Fatalf("Spilled: want 2 scope declarations for the two byval slices, got %d:\n%s", len(ds), fn)
		}
		dir := t.TempDir()
		clangC(t, types.X86_64Linux, dir, "spilled", ll, "-O2 -S -emit-llvm")
		opt, err := os.ReadFile(filepath.Join(dir, "spilled.o"))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(stores.FindAllString(fnText(string(opt), "mk.main.Spilled"), -1)); n != 1 {
			t.Errorf("Spilled after clang -O2: %d stores to c[i], want 1:\n%s", n, fnText(string(opt), "mk.main.Spilled"))
		}
	})
}

// TestAliasMarkDropped is the codegen stand-in for TestPC_MutationCaught
// on the metadata's other half: the access rules, not the parameter rules. Each
// body is Name(b: B, c: mut []int, i, j): int { c[i] = 1; t := <read of
// b>; c[i] = 2; return t }, and in each the read must not carry b's scope:
//   - KernDeref, *b[j] with b: []&int: b's element (the pointer) is b's
//     memory and is tagged; the int behind it is not;
//   - KernNested, b[j][j] with b: [][]int: the inner slice's elements are
//     not b's memory;
//   - KernRef, a body that borrows b whole (&b): b is not tagged at all;
//   - KernAsm, a body with asm: nothing is tagged.
//
// After clang -O2 both stores to c[i] stay in every one, because the read
// may alias c[i] as far as LLVM is told.
func TestAliasMarkDropped(t *testing.T) {
	decl := regexp.MustCompile(`noalias\.scope\.decl\(metadata`)
	scoped := regexp.MustCompile(`(?m)^ +(?:%v[0-9]+ = )?(load|store) ([a-z0-9]+).*!alias\.scope`)
	stores := regexp.MustCompile(`store i64 [12], ptr`)

	for _, tc := range []struct {
		name     string
		target   types.Target
		callable bool
	}{
		{"x86_64-windows", types.X86_64Windows, true},
		{"aarch64-linux", types.Aarch64Linux, false},
		{"aarch64-macos", types.Aarch64Macos, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv()
			ms := e.tt.MutSlice(types.Int)
			kern := func(name string, bt types.TypeID, pre func(f *fnb), read func(f *fnb) mir.Place) *mir.Body {
				f := e.body(e.fn(name, []types.TypeID{bt, ms, types.Int, types.Int}, types.Int, 0))
				if pre != nil {
					pre(f)
				}
				ci := f.b.Place(2, mir.Projection{Kind: mir.ProjIndex, A: 3, Type: types.Int})
				f.assign(ci, use(mir.Imm(types.Int, 1)), f.at(2, 3))
				f.assign(place(0), use(mir.Copy(read(f))), f.at(3, 3))
				f.assign(ci, use(mir.Imm(types.Int, 2)), f.at(4, 3))
				f.ret(f.at(5, 3))
				return f.b
			}
			plain := func(f *fnb) mir.Place {
				return f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: types.Int})
			}
			refInt, sl := e.tt.Ref(types.Int), e.tt.Slice(types.Int)
			naked := e.body(e.fn("KernNaked", []types.TypeID{sl, ms}, types.Void, 0))
			naked.asmBody(mir.Asm{Template: "nop"}, naked.at(1, 3))
			naked.term(mir.Terminator{Kind: mir.TermUnreachable, Span: naked.at(1, 3)})
			e.items.Get(naked.item).Decl.(*types.FuncDecl).Naked = true
			bodies := []*mir.Body{
				kern("KernDeref", e.tt.Slice(refInt), nil, func(f *fnb) mir.Place {
					return f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: refInt},
						mir.Projection{Kind: mir.ProjDeref, Type: types.Int})
				}),
				kern("KernNested", e.tt.Slice(sl), nil, func(f *fnb) mir.Place {
					return f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: 4, Type: sl},
						mir.Projection{Kind: mir.ProjIndex, A: 4, Type: types.Int})
				}),
				kern("KernRef", sl, func(f *fnb) {
					r := f.local(e.tt.Ref(sl))
					f.assign(place(r), mir.Rvalue{Kind: mir.RvRef, Borrow: mir.Shared, Place: place(1)}, f.at(1, 3))
				}, plain),
				kern("KernAsm", sl, func(f *fnb) { f.asmBody(mir.Asm{Template: "nop"}, f.at(1, 3)) }, plain),
				naked.b,
			}
			u := unitOf(e, bodies)
			if tc.callable {
				u = cCallable(u)
			}
			ll := emit(t, e, u, tc.target, types.Release)
			for name, want := range map[string]struct {
				decls  int
				tagged []string // the tagged accesses, in order: kind and type
			}{
				"KernDeref":  {2, []string{"store i64", "load ptr", "store i64"}},
				"KernNested": {2, []string{"store i64", "store i64"}},
				"KernRef":    {0, nil},
				"KernAsm":    {0, nil},
				"KernNaked":  {0, nil},
			} {
				fn := fnText(ll, "mk.main."+name)
				if n := len(decl.FindAllString(fn, -1)); n != want.decls {
					t.Errorf("%s: %d scope declarations, want %d:\n%s", name, n, want.decls, fn)
				}
				var got []string
				for _, m := range scoped.FindAllStringSubmatch(fn, -1) {
					got = append(got, m[1]+" "+m[2])
				}
				if strings.Join(got, ", ") != strings.Join(want.tagged, ", ") {
					t.Errorf("%s: tagged accesses %q, want %q:\n%s", name, got, want.tagged, fn)
				}
			}
			dir := t.TempDir()
			clangC(t, tc.target, dir, "dropped", ll, "-O2 -S -emit-llvm")
			opt, err := os.ReadFile(filepath.Join(dir, "dropped.o"))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"KernDeref", "KernNested", "KernRef", "KernAsm"} {
				if n := len(stores.FindAllString(fnText(string(opt), "mk.main."+name), -1)); n != 2 {
					t.Errorf("%s after clang -O2: %d stores to c[i], want 2:\n%s", name, n, fnText(string(opt), "mk.main."+name))
				}
			}
		})
	}
}

// axpyBody builds Name(a: []f64, b: []f64, c: mut []f64) {
// for i in 0..c.Len() { c[i] = c[i] + a[i] * b[i] } }, matching lower's
// bounds-elided for-loop shape over three f64 slices. cVar marks c as a
// var parameter so aliasScopes skips it (the control case).
func axpyBody(e *testEnv, name string, cVar bool) *mir.Body {
	sl, ms := e.tt.Slice(types.F64), e.tt.MutSlice(types.F64)
	f := e.body(e.fn(name, []types.TypeID{sl, sl, ms}, types.Void, 0))
	f.b.Locals[3].Var = cVar
	i, n, cond := f.local(types.Int), f.local(types.Int), f.local(types.Bool)
	av, bv, cv, prod, sum := f.local(types.F64), f.local(types.F64), f.local(types.F64), f.local(types.F64), f.local(types.F64)
	f.assign(place(i), use(mir.Imm(types.Int, 0)), f.at(2, 3))
	f.assign(place(n), mir.Rvalue{Kind: mir.RvLen, Place: place(3)}, f.at(2, 12))
	head, loop, done := f.newBlock(), f.newBlock(), f.newBlock()
	f.term(mir.Terminator{Kind: mir.TermGoto, Target: head, Span: f.at(2, 3)})
	f.cur = head
	f.assign(place(cond), bin(mir.Lt, mir.Copy(place(i)), mir.Copy(place(n))), f.at(2, 7))
	f.term(mir.Terminator{Kind: mir.TermSwitch, On: mir.Copy(place(cond)),
		Cases: f.b.AddCases(mir.SwitchCase{Value: 0, Target: done}), Default: loop, Span: f.at(2, 3)})
	f.cur = loop
	ai := f.b.Place(1, mir.Projection{Kind: mir.ProjIndex, A: uint32(i), Type: types.F64})
	bi := f.b.Place(2, mir.Projection{Kind: mir.ProjIndex, A: uint32(i), Type: types.F64})
	ci := f.b.Place(3, mir.Projection{Kind: mir.ProjIndex, A: uint32(i), Type: types.F64})
	f.assign(place(av), use(mir.Copy(ai)), f.at(3, 5))
	f.assign(place(bv), use(mir.Copy(bi)), f.at(3, 12))
	f.assign(place(prod), bin(mir.Mul, mir.Copy(place(av)), mir.Copy(place(bv))), f.at(3, 10))
	f.assign(place(cv), use(mir.Copy(ci)), f.at(3, 19))
	f.assign(place(sum), bin(mir.Add, mir.Copy(place(cv)), mir.Copy(place(prod))), f.at(3, 17))
	f.assign(ci, use(mir.Copy(place(sum))), f.at(3, 5))
	f.assign(place(i), bin(mir.AddNoWrap, mir.Copy(place(i)), mir.Imm(types.Int, 1)), f.at(2, 7))
	f.term(mir.Terminator{Kind: mir.TermGoto, Target: head, Span: f.at(2, 3)})
	f.cur = done
	f.ret(f.at(5, 1))
	return f.b
}

// TestAliasScopesAarch64Loop verifies that an axpy/matmul-like loop over
// (a: []f64, b: []f64, c: mut []f64) on AArch64 (Linux and macOS) emits
// entry-block scope declarations after the [2 x i64] parameter stores,
// tags every element load/store with the right scopes, compiles cleanly at
// clang -O3 -S without runtime alias checks, and vectorizes with NEON.
func TestAliasScopesAarch64Loop(t *testing.T) {
	decl := regexp.MustCompile(`call void @llvm\.experimental\.noalias\.scope\.decl\(metadata (![0-9]+)\)`)
	taggedF64 := regexp.MustCompile(`(load double|store double %v[0-9]+), ptr %v[0-9]+, align 8(?:, !noundef ![0-9]+)?, !alias\.scope (![0-9]+), !noalias (![0-9]+)`)
	neon2d := regexp.MustCompile(`(?:\bv[0-9]+|f(?:mul|add))\.2d\b`)

	for _, target := range []types.Target{types.Aarch64Linux, types.Aarch64Macos} {
		t.Run(target.Name, func(t *testing.T) {
			e := newTestEnv()
			u := unitOf(e, []*mir.Body{axpyBody(e, "Axpy", false), axpyBody(e, "AxpyVar", true)})
			ll := emit(t, e, u, target, types.Release)
			fn := fnText(ll, "mk.main.Axpy")
			if fn == "" {
				t.Fatalf("no mk.main.Axpy in IR")
			}
			// Control: AxpyVar has a var mut slice, so no alias scopes are emitted.
			if fnVar := fnText(ll, "mk.main.AxpyVar"); strings.Contains(fnVar, "alias.scope") || strings.Contains(fnVar, "noalias.scope.decl") {
				t.Errorf("AxpyVar emitted alias scopes despite var mut slice:\n%s", fnVar)
			}
			// AArch64 passes all three slices as [2 x i64] parameters.
			if !strings.Contains(fn, "([2 x i64] %p0, [2 x i64] %p1, [2 x i64] %p2)") {
				t.Fatalf("Axpy does not take three [2 x i64] slice params:\n%s", fn)
			}
			// The three parameter stores precede the three scope decls in entry.
			lastStore := strings.LastIndex(fn, "store [2 x i64]")
			firstDecl := strings.Index(fn, "llvm.experimental.noalias.scope.decl")
			if lastStore < 0 || firstDecl < 0 || lastStore > firstDecl {
				t.Errorf("entry ordering: store [2 x i64] at %d, scope.decl at %d:\n%s", lastStore, firstDecl, fn)
			}
			md := map[string]string{}
			for _, m := range regexp.MustCompile(`(?m)^(![0-9]+) = (.*)$`).FindAllStringSubmatch(ll, -1) {
				md[m[1]] = m[2]
			}
			members := func(list string) []string {
				return strings.Split(strings.TrimSuffix(strings.TrimPrefix(md[list], "!{"), "}"), ", ")
			}
			ds := decl.FindAllStringSubmatch(fn, -1)
			if len(ds) != 3 {
				t.Fatalf("Axpy: want 3 scope declarations (a, b, c), got %d:\n%s", len(ds), fn)
			}
			sa, sb, sc := members(ds[0][1])[0], members(ds[1][1])[0], members(ds[2][1])[0]
			acc := taggedF64.FindAllStringSubmatch(fn, -1)
			if len(acc) != 4 {
				t.Fatalf("Axpy: want 4 tagged f64 accesses (a[i], b[i], c[i] load, c[i] store), got %d:\n%s", len(acc), fn)
			}
			want := []struct{ own, noalias string }{
				{sa, sc},
				{sb, sc},
				{sc, sa + ", " + sb},
				{sc, sa + ", " + sb},
			}
			for k, a := range acc {
				own := strings.Join(members(a[2]), ", ")
				na := strings.Join(members(a[3]), ", ")
				if own != want[k].own || na != want[k].noalias {
					t.Errorf("Axpy access %d (%s): scope %s noalias {%s}, want %s {%s}", k, a[1], own, na, want[k].own, want[k].noalias)
				}
			}

			// Verify LLVM -O3 eliminates runtime alias checks and vectorizes.
			bin := needLLVM(t)
			dir := t.TempDir()
			src := filepath.Join(dir, "axpy.ll")
			if err := os.WriteFile(src, []byte(ll), 0o644); err != nil {
				t.Fatal(err)
			}
			optPath := filepath.Join(dir, "axpy.opt.ll")
			runTool(t, tool(bin, "clang"), "--target="+target.LLVMTriple, "-O3", "-S", "-emit-llvm",
				"-Wno-override-module", "-fverify-intermediate-code", "-o", optPath, src)
			optBytes, err := os.ReadFile(optPath)
			if err != nil {
				t.Fatal(err)
			}
			optFn := fnText(string(optBytes), "mk.main.Axpy")
			if strings.Contains(optFn, "vector.memcheck") || strings.Contains(optFn, "found.conflict") {
				t.Errorf("Axpy at -O3 kept a runtime alias check:\n%s", optFn)
			}
			if !strings.Contains(optFn, "<2 x double>") {
				t.Errorf("Axpy at -O3 did not vectorize to <2 x double>:\n%s", optFn)
			}
			// Without the alias scopes, LLVM must emit a runtime alias check.
			optVar := fnText(string(optBytes), "mk.main.AxpyVar")
			if !strings.Contains(optVar, "vector.memcheck") {
				t.Errorf("AxpyVar control at -O3 did not emit vector.memcheck:\n%s", optVar)
			}

			// Verify clang -O3 -S compiles cleanly to AArch64 NEON assembly.
			asmPath := filepath.Join(dir, "axpy.s")
			runTool(t, tool(bin, "clang"), "--target="+target.LLVMTriple, "-O3", "-S",
				"-Wno-override-module", "-fverify-intermediate-code", "-o", asmPath, src)
			asmBytes, err := os.ReadFile(asmPath)
			if err != nil {
				t.Fatal(err)
			}
			if !neon2d.Match(asmBytes) {
				t.Errorf("Axpy AArch64 assembly lacks NEON .2d vector instructions:\n%s", string(asmBytes))
			}

			// Verify --debug (DWARF + #dbg_declare + NoAliasScopeDecl) verifies cleanly.
			var dbgBuf bytes.Buffer
			if _, err := EmitPack(u, 0, e, Config{Target: target, Profile: types.Dev, Debug: true}, &dbgBuf); err != nil {
				t.Fatal(err)
			}
			clangC(t, target, dir, "axpy_dbg", dbgBuf.String(), "-O0")
		})
	}
}
