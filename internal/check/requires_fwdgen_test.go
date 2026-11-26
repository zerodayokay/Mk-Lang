// Why: a caller's own const generic passed as a callee's const value
// argument is forwarded, and a clause over it must be restated or judged,
// never left to the entry check a release build skips.
// What: TestGenericForwardedValue, TestImplGenericForwardedValue,
// TestImplGenericCallee, TestTypeRestatedValueArg,
// TestImportedForwardedValue, TestNarrowedBranch.
// How: the clause is restated or judged over the caller's finite sets;
// beside an argument no clause can spell, a forwarded const is E0706
// "cannot be judged here".

package check

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// fwdF is F with a compile-time clause over its const value parameter n
// (lines 1-4), as rr5's n01 has it with an unsafe read.
const fwdF = "F(const n: int): int\n\trequires n >= 0 && n < 4 {\n\treturn n\n}\n\n"

// TestGenericForwardedValue: a caller's const generic passed as a
// value argument, from a function, a method, an impl method of
// `impl[const n]` (which cannot restate), with a listed set at which the
// clause holds or does not, as both a const generic and a value argument,
// inside a fn literal and in an expression over it. Each row but the
// accepted ones once gave no diagnostic (G[24] read past a table in
// release).
func TestGenericForwardedValue(t *testing.T) {
	t.Parallel()
	const s = "struct S {\n\ta: int\n}\n\nS.F(&self, const n: int): int\n\trequires n >= 0 && n < 4 {\n\treturn n\n}\n\n" // lines 1-9
	runBodyCases(t, []bodyCase{
		{"not restated (n01)", fwdF + "G[const k: int](): int {\n\treturn F(k)\n}\n", "E0706@7:9"},
		{"restated (n01b)", fwdF + "G[const k: int](): int\n\trequires k >= 0 && k < 4 {\n\treturn F(k)\n}\n\nM(): int {\n\treturn G[24]()\n}\n", "E0706@12:9"},
		{"a method (n01e)", s + "G[const k: int](x: &S): int {\n\treturn x.F(k)\n}\n", "E0706@11:9"},
		{"listed, holds at each (n01g)", fwdF + "G[const k: int](): int\n\trequires k == 1 || k == 3 {\n\treturn F(k)\n}\n", ""},
		{"listed, false at one (n01h)", fwdF + "G[const k: int](): int\n\trequires k == 1 || k == 24 {\n\treturn F(k)\n}\n", "E0706@8:9"},
		{"const generic and value argument (n01j)", "At[const K: int](const n: int): int\n\trequires K == n && n >= 0 && n < 4 {\n\treturn n\n}\n\n" +
			"G[const k: int](): int {\n\treturn At[k](k)\n}\n", "E0706@7:9"},
		{"fn literal (n01k)", fwdF + "G[const k: int](): int {\n\th := fn(): int {\n\t\treturn F(k)\n\t}\n\treturn h()\n}\n", "E0706@8:10"},
		{"an expression, restated (n01l)", fwdF + "G[const k: int](): int\n\trequires (k - 1) >= 0 && (k - 1) < 4 {\n\treturn F(k - 1)\n}\n", ""},
		{"a const generic of the owner type, restated", fwdF + "struct W[const n: int] {\n\tx: int\n}\n\nW[n].Get(&self): int\n\trequires n >= 0 && n < 4 {\n\treturn F(n)\n}\n", ""},
		{"a const generic of the owner type, not restated", fwdF + "struct W[const n: int] {\n\tx: int\n}\n\nW[n].Get(&self): int {\n\treturn F(n)\n}\n", "E0706@11:9"},
		{"a run-time clause is not restated", "F(const n: int, m: int): int\n\trequires n < m {\n\treturn n\n}\n\nG[const k: int](): int {\n\treturn F(k, 4)\n}\n", ""},
	})
}

// TestImplGenericForwardedValue: a method of `impl[const n]
// Getter for W[n]` passing n to F cannot state F's clause, so
// the call is E0706 "cannot be judged here", with no fix to restate it.
func TestImplGenericForwardedValue(t *testing.T) {
	t.Parallel()
	const src = fwdF + "interface Getter {\n\tGet(&self): int\n}\n\nstruct W[const n: int] {\n\tx: int\n}\n\n" + // lines 1-13
		"impl[const n: int] Getter for W[n] {\n\tGet(&self): int {\n\t\treturn F(n)\n\t}\n}\n"
	b := checkBody(t, newWorld(t, true), "E0706@16:10", src)
	d := b.bodyDiags(t)[0]
	if !strings.Contains(d.Message, "cannot be judged here") || strings.Contains(d.Message, "state it on this function") || len(d.Fixes) != 0 {
		t.Fatalf("E0706 %q with %d fixes, want \"cannot be judged here\" and no fix", d.Message, len(d.Fixes))
	}
}

// TestImplGenericCallee: a method of an interface impl over
// `impl[T]` calling G[T] under `requires SizeOf[T]() < 4` cannot state the
// clause, so the bare-parameter restatement is E0706 "cannot be judged
// here" with no fix. A valid program is refused: the rule's cost, which
// also closes a read past a table. std.simd's form,
// `self.v.WidenLo[T, 4]()`, is the conformance unit
// E0706_impl_generic_simd.
func TestImplGenericCallee(t *testing.T) {
	t.Parallel()
	const src = "G[T](): int\n\trequires SizeOf[T]() < 4 {\n\treturn 7\n}\n\n" + // lines 1-5
		"interface Get {\n\tGet(&self): int\n}\n\nstruct Box[T] {\n\tv: T\n}\n\n" + // lines 6-13
		"impl[T] Get for Box[T] {\n\tGet(&self): int {\n\t\treturn G[T]()\n\t}\n}\n"
	b := checkBody(t, newWorld(t, true), "E0706@16:10", src)
	d := b.bodyDiags(t)[0]
	if !strings.Contains(d.Message, "cannot be judged here") || strings.Contains(d.Message, "state it on this function") || len(d.Fixes) != 0 {
		t.Fatalf("E0706 %q with %d fixes, want \"cannot be judged here\" and no fix", d.Message, len(d.Fixes))
	}
}

// TestTypeRestatedValueArg: the restatement of a
// clause that also reads a const value parameter: the parameter is spelled
// as the call's folded argument, never by the callee's own name, which the
// caller could match by text while meaning its own parameter. Inside `if T
// is u32`, H[T] is H[u32], judged at its closed argument.
func TestTypeRestatedValueArg(t *testing.T) {
	t.Parallel()
	const v = "V[T](const n: int): int\n\trequires n < SizeOf[T]() {\n\treturn n\n}\n\n" // lines 1-5
	const h = "H[T](): int\n\trequires SizeOf[T]() > 0 {\n\treturn 1\n}\n\n"             // lines 6-10
	for _, tc := range []struct{ name, src, want, clause string }{
		{"folded argument", v + "K[U](): int {\n\treturn V[U](3)\n}\n", "E0706@7:9", "3 < SizeOf[U]()"},
		{"own n, matched by the callee's name", v + "K[U](const n: int): int\n\trequires n < SizeOf[U]() {\n\treturn V[U](3)\n}\n", "E0706@8:9", "3 < SizeOf[U]()"},
		{"stated", v + "K[U](): int\n\trequires 3 < SizeOf[U]() {\n\treturn V[U](3)\n}\n", "", ""},
		{"narrowed", v + h + "K[T](): int {\n\tif T is u32 {\n\t\treturn H[T]()\n\t}\n\treturn 0\n}\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := checkBody(t, newWorld(t, true), tc.want, tc.src)
			if tc.clause != "" {
				if got := fact(b.bodyDiags(t)[0], "clause"); got != tc.clause {
					t.Fatalf("clause %q, want %q", got, tc.clause)
				}
			}
		})
	}
}

// TestImportedForwardedValue: the same at an imported callee (lib.F,
// and lib.GV with an enum value): the clause is evaluated at each value of
// the caller's listed set; without a set lib.F's clause is restated, and
// lib.GV's, which reads lib's enum value D beside the forwarded k, is
// E0706 "cannot be judged here", never a demand to restate.
func TestImportedForwardedValue(t *testing.T) {
	t.Parallel()
	const lib = "export enum Mode {\n\tA\n\tB\n}\n\nexport const MA: Mode = Mode.A\n\nexport const MB: Mode = Mode.B\n\n" +
		"export F(const n: int): int\n\trequires n >= 0 && n < 4 {\n\treturn n\n}\n\n" +
		"export GV[const D: Mode](const k: int): int\n\trequires k >= 0 && k < 4 && D == Mode.A {\n\treturn k\n}\n"
	for _, tc := range []struct{ name, src, want string }{
		{"F, not restated (n01d)", "import lib\n\nG[const k: int](): int {\n\treturn lib.F(k)\n}\n", "E0706@4:9"},
		{"F, restated", "import lib\n\nG[const k: int](): int\n\trequires k >= 0 && k < 4 {\n\treturn lib.F(k)\n}\n", ""},
		{"F, listed, holds at each (n01m G)", "import lib\n\nG[const k: int](): int\n\trequires k == 0 || k == 2 {\n\treturn lib.F(k)\n}\n", ""},
		{"F, listed, false at one (n01m H)", "import lib\n\nG[const k: int](): int\n\trequires k == 2 || k == 24 {\n\treturn lib.F(k)\n}\n", "E0706@5:9"},
		{"GV, const parameter (t9)", "import lib\n\nW(const k: int): int\n\trequires k >= 0 && k < 4 {\n\treturn lib.GV[lib.MB](k)\n}\n", "E0706@5:9"},
		{"GV, const parameter, honest (t9b)", "import lib\n\nW(const k: int): int\n\trequires k >= 0 && k < 4 {\n\treturn lib.GV[lib.MA](k)\n}\n", "E0706@5:9"},
		{"GV, const generic, listed, false at each (t9d)", "import lib\n\nG[const k: int](): int\n\trequires k == 0 || k == 1 {\n\treturn lib.GV[lib.MB](k)\n}\n", "E0706@5:9"},
		{"GV, const generic, listed, holds at each (t9e)", "import lib\n\nG[const k: int](): int\n\trequires k == 0 || k == 1 {\n\treturn lib.GV[lib.MA](k)\n}\n", ""},
		{"GV, const generic, no set (t9f)", "import lib\n\nG[const k: int](): int\n\trequires k >= 0 && k < 4 {\n\treturn lib.GV[lib.MA](k)\n}\n", "E0706@5:9"},
		{"GV, folded argument", "import lib\n\nW(): int {\n\treturn lib.GV[lib.MA](2)\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			fe := w.withEval()
			// lib's clauses, evaluated as mk's evaluator would: F's n (or
			// GV's k) in 0..3, and GV's D the tag of Mode.A (0).
			fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
				if req.What != types.EvalContract || len(req.Args) != 1 {
					return types.EvalResult{}, false
				}
				k, _ := w.c.constInt(req.Args[0])
				ok := k >= 0 && k < 4
				if len(req.GenericArgs) == 1 {
					d := w.consts.Get(req.GenericArgs[0].Const.Value).Bytes
					ok = ok && len(d) > 0 && d[0] == 0
				}
				var v uint64
				if ok {
					v = 1
				}
				return types.EvalResult{Value: w.c.scalarConst(types.Bool, 1, v)}, true
			}
			w.add("lib", opt{}, lib)
			for name, tag := range map[string]uint64{"MA": 0, "MB": 1} {
				id, ok := w.items.ByKey(types.ItemKey("lib", name))
				if !ok {
					t.Fatal("no lib." + name)
				}
				d := w.items.Get(id).Decl.(*types.ConstDecl)
				d.Value, d.HasValue = w.c.scalarConst(d.Type, 1, tag), true
			}
			b := checkBody(t, w, tc.want, tc.src)
			if !strings.Contains(tc.name, "GV") {
				return
			}
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if d.Code == diag.E0706 && (!strings.Contains(d.Message, "cannot be judged here") || strings.Contains(d.Message, "state it")) {
					t.Errorf("E0706 message %q, want \"cannot be judged here\" and no demand to restate", d.Message)
				}
			}
		})
	}
}

// TestNarrowedBranch: inside `if U is u16`, U written in a call is u16, so
// G[U] is judged at its closed argument as G[u16] is; a clause that also
// reads another open parameter is restated over it. Outside a branch, and
// in a fn literal in one, the demand to state the clause and its fix are
// unchanged.
func TestNarrowedBranch(t *testing.T) {
	t.Parallel()
	const g = "G[T](): int\n\trequires SizeOf[T]() < 4 {\n\treturn 1\n}\n\n" // lines 1-5
	const g2 = "G[T, S](): int\n\trequires SizeOf[T]() + SizeOf[S]() < 4 {\n\treturn 7\n}\n\n"
	const gk = "G[T](const k: int): int\n\trequires k < SizeOf[T]() {\n\treturn k\n}\n\n"
	for _, tc := range []struct {
		name, src, want, msg string
		fixes                int
	}{
		{"narrowed (c2)", g + "K[U](): int {\n\tif U is u16 {\n\t\treturn G[U]()\n\t}\n\treturn 0\n}\n", "", "", 0},
		{"narrowed, false at the narrowed argument", g + "K[U](): int {\n\tif U is u64 {\n\t\treturn G[U]()\n\t}\n\treturn 0\n}\n", "E0706@8:10", "", 0},
		{"narrowed, a fn value", g + "K[U](): int {\n\tif U is u16 {\n\t\tf := G[U]\n\t\treturn f()\n\t}\n\treturn 0\n}\n", "", "", 0},
		{"spelled (c2s)", g + "K[U](): int {\n\tif U is u16 {\n\t\treturn G[u16]()\n\t}\n\treturn 0\n}\n", "", "", 0},
		{"spelled, false at the closed argument", g + "K[U](): int {\n\tif U is u64 {\n\t\treturn G[u64]()\n\t}\n\treturn 0\n}\n", "E0706@8:10", "", 0},
		{"narrowed, stated", g + "K[U](): int\n\trequires SizeOf[U]() < 4 {\n\tif U is u16 {\n\t\treturn G[U]()\n\t}\n\treturn 0\n}\n", "", "", 0},
		{"not narrowed", g + "K[U](): int {\n\treturn G[U]()\n}\n", "E0706@7:9",
			"the callee requires `SizeOf[U]() < 4` here: state it on this function", 1},
		{"narrowed, another parameter", g + "K[U, V](): int {\n\tif V is u16 {\n\t\treturn G[U]()\n\t}\n\treturn 0\n}\n", "E0706@8:10",
			"the callee requires `SizeOf[U]() < 4` here: state it on this function", 1},
		// The clause also reads V, which no branch narrows: it is restated
		// over u16 and V, as for G[u16, V].
		{"narrowed, a second open parameter (p8)", g2 + "K[U, V](): int {\n\tif U is u16 {\n\t\treturn G[U, V]()\n\t}\n\treturn 1\n}\n", "E0706@8:10",
			"the callee requires `SizeOf[u16]() + SizeOf[V]() < 4` here: state it on this function", 1},
		{"spelled, a second open parameter (p9)", g2 + "K[U, V](): int {\n\tif U is u16 {\n\t\treturn G[u16, V]()\n\t}\n\treturn 1\n}\n", "E0706@8:10",
			"the callee requires `SizeOf[u16]() + SizeOf[V]() < 4` here: state it on this function", 1},
		// A forwarded const value is restated over the narrowed argument; a
		// call inside a fn literal in the branch is judged at the narrowed
		// argument, as one outside the literal is.
		{"narrowed, a forwarded const value (p2)", gk + "K[U](const j: int): int {\n\tif U is u16 {\n\t\treturn G[U](j)\n\t}\n\treturn 0\n}\n", "E0706@8:10",
			"the callee requires `j < SizeOf[u16]()` here: state it on this function", 1},
		{"narrowed, in a fn literal (p5)", g + "K[U](): int {\n\tif U is u16 {\n\t\tf := fn(): int { return G[U]() }\n\t\treturn f()\n\t}\n\treturn 1\n}\n", "", "", 0},
		{"narrowed, in a fn literal, false at the narrowed argument", g + "K[U](): int {\n\tif U is u64 {\n\t\tf := fn(): int { return G[U]() }\n\t\treturn f()\n\t}\n\treturn 1\n}\n", "E0706@8:27", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := checkBody(t, newWorld(t, true), tc.want, tc.src)
			if tc.msg == "" {
				return
			}
			d := b.bodyDiags(t)[0]
			if d.Message != tc.msg || len(d.Fixes) != tc.fixes {
				t.Fatalf("E0706 %q with %d fixes, want %q with %d", d.Message, len(d.Fixes), tc.msg, tc.fixes)
			}
		})
	}
}
