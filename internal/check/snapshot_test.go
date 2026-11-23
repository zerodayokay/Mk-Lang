// Why: the JSON form of each diagnostic is a public format, so a change to
// it must be seen.
// What: TestSnapshots, TestSnapshotsCoverEveryCode and
// TestTwoStoresPrintAsWritten.
// How: one JSON diagnostic per code check emits, from its minimal trigger,
// in testdata/<code>.golden, compared with diag.Report.WriteJSON and
// updated only with -update.

package check

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

var update = flag.Bool("update", false, "rewrite golden files")

// snap is one code's minimal trigger: Mk source for module m, or a setup
// that builds the world itself and returns the diagnostics to search.
type snap struct {
	code  diag.Code
	src   string
	setup func(t *testing.T, w *world) []*diag.Diagnostic
	// name is the golden's base name when a code has a second form
	// (testdata/<name>.golden); empty means the code's own name.
	name string
}

const evalSrc = "Boom(): int {\n\treturn 1\n}\n\nconst P = Boom()\n"

var snaps = []snap{
	{code: diag.E0101, src: "error E\n\nG(): E!bool {\n\treturn true\n}\n\nF(a: int): int\n\trequires try G()\n{\n\treturn a\n}\n"},
	{code: diag.E0203, src: "struct P {\n\tx: int\n}\n\nF(p: P): int {\n\treturn p.nope\n}\n"},
	{code: diag.E0204, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		w.add("lib", opt{}, "export struct P {\n\tsecret: int\n}\n")
		return checkAll(t, w, "import lib\n\nF(p: lib.P): int {\n\treturn p.secret\n}\n")
	}},
	{code: diag.E0206, src: "struct C {\n\tn: int\n}\n\ninterface I {\n\tN(&self): int\n}\n\nimpl I for C {\n\tN(&self): int {\n\t\treturn 1\n\t}\n}\n"},
	{code: diag.E0207, src: "struct P {\n\tx: int\n}\n\nF(p: P): int {\n\treturn p.X\n}\n"},
	{code: diag.E0209, src: "F(x: int) {\n\tx + 1\n}\n"},
	{code: diag.E0301, src: "F(): int {\n\treturn \"s\"\n}\n"},
	{code: diag.E0302, src: "F(x: int): bool {\n\treturn bool(x)\n}\n"},
	{code: diag.E0303, src: "F() {\n\tx: u8 = 300\n}\n"},
	{code: diag.E0304, src: "G(a: int) {\n}\n\nF() {\n\tG(1, 2)\n}\n"},
	{code: diag.E0305, src: "struct P {\n\tx: int\n\ty: int\n}\n\nF(): P {\n\treturn P(x: 1)\n}\n"},
	{code: diag.E0306, src: "enum S {\n\tA\n\tB\n}\n\nF(s: S): int {\n\tmatch s {\n\t\t.A {\n\t\t\treturn 1\n\t\t}\n\t}\n\treturn 0\n}\n"},
	{code: diag.E0307, src: "F(n: int) {\n\tif x := n {\n\t}\n}\n"},
	{code: diag.E0308, src: "F(s: str, t: str): str {\n\treturn s + t\n}\n"},
	{code: diag.E0309, src: "F(r: (a: int, b: int)): int {\n\t(a, c) := r\n\treturn a\n}\n"},
	{code: diag.E0408, src: "F(c: Cell[int]) {\n\txs := [c; 2]\n}\n"},
	{code: diag.E0410, src: "export struct H {\n\tname: str\n}\n"},
	{code: diag.E0412, src: "struct Hidden {\n\tn: int\n}\n\nexport F(h: Hidden) {\n}\n"},
	{code: diag.E0507, src: "Put(p: &int, q: &int)\n\t*q into p\n{\n}\n"},
	{code: diag.E0508, name: "E0508_capture_immutable", src: "Each(const f: fn(x: &int): bool): bool {\n\tx := 1\n\treturn f(&x)\n}\n\nF(k: int): bool {\n\tn := k\n\treturn Each(fn(a: &int): bool {\n\t\tp := mut n\n\t\treturn *p > *a\n\t})\n}\n"},
	{code: diag.E0601, src: "Longer(a: &str, b: &str): &str {\n\treturn a\n}\n"},
	{code: diag.E0607, src: "struct Sink {\n\tbuf: mut []u8\n}\n\nSink.Out(mut self): mut []u8 from self.buf {\n\treturn mut *self.buf\n}\n"},
	{code: diag.E0608, src: `interface I {
	Get(&self, t: &Tok): str from t.s
}

struct Tok {
	s: str
}

impl I for Tok {
	Get(&self, t: &Tok): str from t {
		return t.s
	}
}
`},
	{code: diag.E0609, src: "struct P {\n\tx: int\n}\n\nP.Get(&self): int {\n\treturn self.x\n}\n\nF(p: P) {\n\t_ = p.Get\n}\n"},
	{code: diag.E0609, name: "E0609_capture", src: "Keep(xs: []i32, keep: fn(&i32): bool): int {\n\treturn 0\n}\n\nF(xs: []i32, limit: i32): int {\n\treturn Keep(xs, fn(x: &i32): bool { return *x > limit })\n}\n"},
	{code: diag.E0701, src: "struct Hold[T: Plain] {\n\tv: T\n}\n\nstruct Use {\n\th: Hold[mut i32]\n}\n"},
	{code: diag.E0702, src: "interface Shape {\n\tArea(&self): f64\n}\n\nstruct C {\n\tr: f64\n}\n\nF(c: C): f64 {\n\treturn c.Area()\n}\n"},
	{code: diag.E0703, src: "Biggest[T](a: T, b: T): T {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n"},
	{code: diag.E0704, src: "str.Shout(self): str {\n\treturn self\n}\n"},
	{code: diag.E0705, src: "G[const N: int]() {\n}\n\nF(n: int) {\n\tG[n]()\n}\n"},
	{code: diag.E0609, name: "E0609_capture_move", src: "Each(const f: fn(x: &int): bool): bool {\n\tx := 1\n\treturn f(&x)\n}\n\nF(zs: mut []int): bool {\n\treturn Each(fn(a: &int): bool {\n\t\tm := zs\n\t\treturn m.Len() > *a\n\t})\n}\n"},
	{code: diag.E0609, name: "E0609_capture_mut", src: "Each(const f: fn(): bool): bool {\n\treturn f()\n}\n\nF(var k: int): int\n\trequires Each(fn(): bool {\n\t\tk += 1\n\t\treturn k > 0\n\t})\n{\n\treturn k\n}\n"},
	{code: diag.E0705, name: "E0705_const_fn_use", src: "Loose(f: fn(&int): bool): bool {\n\treturn true\n}\n\nG(const less: fn(&int): bool): bool {\n\treturn Loose(less)\n}\n"},
	{code: diag.E0609, name: "E0609_capture_const_not_fn", src: "Keep[F](const f: F): int {\n\treturn 0\n}\n\nF(k: int): int {\n\treturn Keep(fn(x: int): int { return x + k })\n}\n"},
	{code: diag.E0705, name: "E0705_const_fn_to_const_not_fn", src: "Keep[F](const f: F): int {\n\treturn 0\n}\n\nG(const less: fn(&int): bool): int {\n\treturn Keep(less)\n}\n"},
	{code: diag.E0706, src: "F(w: mut Writer): WriteError!void {\n\ttry Print(w, \"{x} {y}\\n\", (x: 1))\n}\n"},
	{code: diag.E0706, name: "E0706_no_record", src: "F(w: mut Writer, n: int): WriteError!void {\n\ttry Print(w, \"{n} items\\n\")\n}\n"},
	{code: diag.E0707, src: "interface Shape {\n\tArea(&self): f64\n}\n\nstruct Sq {\n\ts: f64\n}\n\nimpl Shape for Sq {\n}\n"},
	{code: diag.E0708, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		old := fieldsNodeCap
		fieldsNodeCap = 8
		defer func() { fieldsNodeCap = old }()
		return checkAll(t, w, "struct S {\n\ta: int\n\tb: int\n\tc: int\n}\n\nF(s: S) {\n\tfor name, value in fields(&s) {\n\t\tx := *value + 1\n\t\ty := x + 2\n\t}\n}\n")
	}},
	// E0707 prints each signature's clauses as written.
	{code: diag.E0707, name: "E0707_clauses", src: e0707Iface + "impl Keep for Tok {\n\tGet(&self, t: &Tok, n: i64): str from t.s {\n\t\treturn t.s\n\t}\n\n" +
		"\tPut(&self, c: &Cell[&str], v: &str)\n\t\t*v into c\n\t{\n\t}\n}\n"},
	{code: diag.E0707, name: "E0707_clauses_missing", src: e0707Iface + "impl Keep for Tok {\n\tGet(&self, t: &Tok, n: int): str from t.s {\n\t\treturn t.s\n\t}\n}\n"},
	{code: diag.E0707, name: "E0707_clauses_extra", src: e0707Iface + "impl Keep for Tok {\n\tGet(&self, t: &Tok, n: int): str from t.s {\n\t\treturn t.s\n\t}\n\n" +
		"\tPut(&self, c: &Cell[&str], v: &str)\n\t\t*v into c\n\t{\n\t}\n\n\tTake(&self, t: &Tok): str from t {\n\t\treturn t.s\n\t}\n}\n"},
	{code: diag.E0707, name: "E0707_clauses_import", setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		lib := strings.Replace(strings.Replace(e0707Iface, "interface Keep", "export interface Keep", 1), "struct Tok", "export struct Tok", 1)
		w.add("lib", opt{}, lib)
		return checkAll(t, w, "import lib\n\nstruct Mine {\n\tn: int\n}\n\nimpl lib.Keep for Mine {\n\tGet(&self, t: &lib.Tok, n: i64): str {\n\t\treturn \"\"\n\t}\n\n"+
			"\tPut(&self, c: &Cell[&str], v: &str)\n\t\t*v into c\n\t{\n\t}\n}\n")
	}},
	// Two stores print as one list, `*v into c, *w into d`, as
	// written and as the other-module route prints them.
	{code: diag.E0707, name: "E0707_clauses_two_stores", src: e0707Two + "impl Two for Tok {\n\t" + e0707TwoImpl + "\n}\n"},
	{code: diag.E0707, name: "E0707_clauses_two_stores_import", setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		w.add("lib", opt{}, e0707TwoLib)
		return checkAll(t, w, "import lib\n\nstruct Mine {\n\tn: int\n}\n\nimpl lib.Two for Mine {\n\t"+e0707TwoImpl+"\n}\n")
	}},
	{code: diag.E0709, src: "F[const N: int]() {\n\tx: [N - 1]u8 = [0; N - 1]\n}\n"},
	{code: diag.E0711, src: "Parse[T](s: str): ?T {\n\treturn none\n}\n\nF() {\n\tx := Parse(\"1\")\n}\n"},
	{code: diag.E0711, name: "E0711_generic_value", src: "Same[T](x: T): T {\n\treturn x\n}\n\nF(): i32 {\n\tf: fn(i32): i32 = Same\n\treturn f(1)\n}\n"},
	{code: diag.E0801, src: "error E\n\nG(): E!int {\n\treturn 1\n}\n\nF(): int {\n\treturn try G()\n}\n"},
	{code: diag.E0802, src: "error E\n\nG(): E!int {\n\treturn 1\n}\n\nF() {\n\tG()\n}\n"},
	{code: diag.E0310, src: "F(c: bool): int {\n\tx := if c {\n\t\t1\n\t} else {\n\t\tz := 2\n\t}\n\treturn x\n}\n"},
	{code: diag.E0311, src: "F(c: bool): int {\n\tx := if c { 1 }\n\treturn x\n}\n"},
	{code: diag.E0312, src: "Add(a: int, b: int): int {\n\treturn a + b\n}\n\nF(n: int): int {\n\tbecome Add(n, 1)\n}\n"},
	{code: diag.E0313, src: "struct Big {\n\ta: int\n\tb: int\n\tc: int\n}\n\nF(b: Big, n: int): int {\n\tbecome F(Big(a: 1, b: 2, c: 3), n - 1)\n}\n"},
	{code: diag.E0314, src: "interface Shape {\n\tArea(&self): f64\n}\n\nM[T: Shape](s: &T): f64 {\n\tbecome s.Area()\n}\n"},
	{code: diag.E0315, src: "F(x: int): int {\n\treturn match x {\n\t\t5..5 { 1 }\n\t\t_ { 0 }\n\t}\n}\n"},
	{code: diag.E0406, src: "Nop() {\n}\n\nF(n: int): int {\n\tdefer Nop()\n\tbecome F(n)\n}\n"},
	{code: diag.E0704, name: "E0704_compiler_provided", src: "error Denied\n\nimpl Format for Denied {\n\tFormat(&self, w: mut Writer): WriteError!void {\n\t\ttry w.Str(\"no\")\n\t}\n}\n"},
	{code: diag.E0803, src: "error E\n\nG(): E!int {\n\treturn 1\n}\n\nF(): int {\n\tx := G() else {\n\t}\n\treturn x\n}\n"},
	{code: diag.E0804, src: "F(x: int): int {\n\treturn try x\n}\n"},
	{code: diag.E0805, src: "F(x: int): int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n}\n"},
	{code: diag.E0806, src: "error E\n\nF(): E!void {\n\tdefer {\n\t\treturn\n\t}\n}\n"},
	{code: diag.E0807, src: "linear struct H {\n\tn: int\n}\n\nH.Close(self) {\n}\n\nerror E\n\nF(a: H, b: H): E!void {\n\tonerr {\n\t\ta.Close()\n\t\tb.Close()\n\t}\n\tfail E\n}\n"},
	{code: diag.E0901, src: "const A: u8 = 200\n\nstruct S {\n\tx: [A + 100]u8\n}\n"},
	// E0217 (testdeps.go): the driver says which import paths are
	// a package's test_deps; here lib is one, used outside a test block.
	{code: diag.E0217, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		w.add("lib", opt{}, "export H(): int {\n\treturn 1\n}\n")
		b := w.add("m", opt{}, "import lib\n\nF(): int {\n\treturn lib.H()\n}\n")
		return TestDepUses(b.info, w.items, func(p string) bool { return p == "lib" })
	}},
	{code: diag.E0902, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		fe := w.withEval()
		b := w.add("m", opt{}, evalSrc)
		fe.answers["m.P"] = types.EvalResult{Err: &types.EvalError{Kind: types.EvalFuel, Steps: 4000001, Items: []types.ItemID{b.item("P")},
			Trace: []source.Span{{File: b.srcs[0].ID, Start: 21, End: 22}}}}
		_, ds := w.c.EvalConst(b.m, b.item("P"))
		return ds
	}},
	{code: diag.E0907, src: "struct A {\n\tb: B\n}\n\nstruct B {\n\ta: A\n}\n"},
	{code: diag.E0908, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		fe := w.withEval()
		fe.fn = func(req *types.EvalRequest) (types.EvalResult, bool) {
			if req.Stage == types.StageSignatures {
				return types.EvalResult{Err: &types.EvalError{Kind: types.EvalStage, Items: []types.ItemID{req.Item, computeID(w)},
					Trace: []source.Span{req.Site, {}}}}, true
			}
			return types.EvalResult{}, false
		}
		return checkAll(t, w, "Compute(): int {\n\treturn 4\n}\n\nconst K = Compute()\n\nF() {\n\tx: [K]u8 = [0; 4]\n}\n")
	}},
	{code: diag.E0909, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		fe := w.withEval()
		b := w.add("m", opt{}, "const B = A + 1\n\nconst A = B + 1\n")
		cyc := &types.EvalError{Kind: types.EvalCycle, Items: []types.ItemID{b.item("A"), b.item("B")}}
		fe.fn = func(*types.EvalRequest) (types.EvalResult, bool) { return types.EvalResult{Err: cyc}, true }
		_, ds := w.c.EvalConst(b.m, b.item("A"))
		return ds
	}},
	{code: diag.E0910, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		// A const's build.File read failed (EvalDataFile).
		fe := w.withEval()
		b := w.add("m", opt{}, "const B: int = 1\n")
		bad := &types.EvalError{Kind: types.EvalDataFile, File: types.DataFilePath, Path: "../x.txt"}
		fe.fn = func(*types.EvalRequest) (types.EvalResult, bool) { return types.EvalResult{Err: bad}, true }
		_, ds := w.c.EvalConst(b.m, b.item("B"))
		return ds
	}},
	{code: diag.E1001, src: "struct R {\n\tp: *int\n}\n\nTake(r: R) {\n}\n\nF(s: &Scope, r: R) {\n\ts.go Take(r)\n}\n"},
	{code: diag.E1002, src: "var Counter: Cell[i32] = Cell.New(0)\n"},
	{code: diag.E1003, src: "F(io: Io) {\n\tx := io.Scope()\n}\n"},
	{code: diag.E1101, src: "extern Puts(s: *u8): i32\n\nF(p: *u8): i32 {\n\treturn Puts(p)\n}\n"},
	{code: diag.E1103, src: "F(p: *int): int {\n\treturn *p\n}\n"},
	{code: diag.E0404, name: "E0404-extern-var", src: "extern var Ticks: u64\n\nReset() {\n\tunsafe \"r\" {\n\t\tTicks = 0\n\t}\n}\n"},
	{code: diag.E1102, name: "E1102-section", src: "const Tag: u32 = 7 section \".boot\"\n"},
	{code: diag.E1104, src: "FirstByte(xs: []u8): u8 {\n\tvar b: u8 = 0\n\tunsafe \"r\" {\n\t\tasm(\"mov {b}, byte ptr [{p}]\", b: out(reg_byte) b, p: in(reg) xs)\n\t}\n\treturn b\n}\n"},
	{code: diag.E1105, src: "export extern naked Halt() {\n\treturn\n}\n"},
	{code: diag.E1106, src: "Count(xs: []u32): int cpu \"avx9\" {\n\treturn xs.Len()\n}\n"},
	{code: diag.E1107, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		w.add("std.simd", opt{std: true}, vecText(t)).wantClean(t)
		return checkAll(t, w, "import std.simd\n\nexport extern Total(v: simd.Vec[f32, 4]): f32 {\n\treturn 0.0\n}\n")
	}},
	// E1109 (e1109.go): an export extern of a library package whose
	// parameter has no C form.
	{code: diag.E1109, setup: func(t *testing.T, w *world) []*diag.Diagnostic {
		b := w.add("m", opt{library: true}, "export extern Sum(xs: []i32): i32 {\n\treturn 0\n}\n")
		return append(append([]*diag.Diagnostic(nil), b.m.Diags...), b.bodyDiags(t)...)
	}},
	{code: diag.E1202, src: "Double(x: &i32): i32 {\n\treturn *x * 2\n}\n\nAll(xs: []i32): int {\n\tys := xs.map(Double)\n\treturn ys.Len()\n}\n"},
	{code: diag.E1204, src: "Wait(n: int): int {\n\treturn n.await\n}\n"},
	{code: diag.E1221, src: "First(x: ?int): int {\n\treturn x.unwrap()\n}\n"},
}

// e0707Iface is an interface whose methods carry clauses.
const e0707Iface = "struct Tok {\n\ts: str\n}\n\ninterface Keep {\n\tGet(&self, t: &Tok, n: int): str from t.s\n" +
	"\tPut(&self, c: &Cell[&str], v: &str)\n\t\t*v into c\n}\n\n"

// e0707Two is an interface method with two stores, the shape
// of Swap's `*a into b, *b into a`; e0707TwoImpl differs in w's type.
const (
	e0707Two = "struct Tok {\n\ts: str\n}\n\ninterface Two {\n" +
		"\tPut2(&self, c: &Cell[&str], d: &Cell[&str], v: &str, w: &str)\n\t\t*v into c, *w into d\n}\n\n"
	e0707TwoLib  = "export interface Two {\n\tPut2(&self, c: &Cell[&str], d: &Cell[&str], v: &str, w: &str)\n\t\t*v into c, *w into d\n}\n"
	e0707TwoImpl = "Put2(&self, c: &Cell[&str], d: &Cell[&str], v: &str, w: &int)\n\t\t*v into c, *w into d\n\t{\n\t}"
)

// TestTwoStoresPrintAsWritten: E0707's expected and found
// facts print a run of stores joined with ", ", the same from the impl's
// module and from another.
func TestTwoStoresPrintAsWritten(t *testing.T) {
	const want = "Put2(&self, c: &Cell[&str], d: &Cell[&str], v: &str, w: &str) *v into c, *w into d"
	const found = "Put2(&self, c: &Cell[&str], d: &Cell[&str], v: &str, w: &int) *v into c, *w into d"
	same := checkAll(t, newWorld(t, true), e0707Two+"impl Two for Tok {\n\t"+e0707TwoImpl+"\n}\n")
	wi := newWorld(t, true)
	wi.add("lib", opt{}, e0707TwoLib)
	other := checkAll(t, wi, "import lib\n\nstruct Mine {\n\tn: int\n}\n\nimpl lib.Two for Mine {\n\t"+e0707TwoImpl+"\n}\n")
	for name, ds := range map[string][]*diag.Diagnostic{"same module": same, "imported": other} {
		n := 0
		for _, d := range ds {
			if d.Code != diag.E0707 {
				continue
			}
			n++
			if fact(d, "expected") != want || fact(d, "found") != found {
				t.Errorf("%s: expected %q found %q", name, fact(d, "expected"), fact(d, "found"))
			}
		}
		if n != 1 {
			t.Errorf("%s: %d E0707, want 1", name, n)
		}
	}
}

// checkAll adds module m with src and returns check's stage-5 and stage-6
// diagnostics (resolve's are left out: the snapshots are check's).
func checkAll(t *testing.T, w *world, src string) []*diag.Diagnostic {
	t.Helper()
	b := w.add("m", opt{}, src)
	ds := append([]*diag.Diagnostic(nil), b.m.Diags...)
	return append(ds, b.bodyDiags(t)...)
}

func TestSnapshots(t *testing.T) {
	for _, s := range snaps {
		name := s.code.String()
		if s.name != "" {
			name = s.name
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, true)
			var ds []*diag.Diagnostic
			if s.setup != nil {
				ds = s.setup(t, w)
			} else {
				ds = checkAll(t, w, s.src)
			}
			var hit *diag.Diagnostic
			for _, d := range ds {
				if d.Code == s.code {
					hit = d
					break
				}
			}
			if hit == nil {
				t.Fatalf("no %v in:\n%s", s.code, dump(w, ds))
			}
			got := diag.Order([]*diag.Diagnostic{hit}, w.sm, diag.NoCap).WriteJSON(w.sm)
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update and read the diff)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s changed:\n%s\nwant\n%s", path, got, want)
			}
		})
	}
}

// TestSnapshotsCoverEveryCode: every code check's sources emit has a
// snapshot.
func TestSnapshotsCoverEveryCode(t *testing.T) {
	have := map[diag.Code]bool{}
	for _, s := range snaps {
		have[s.code] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for i := strings.Index(text, "diag.E"); i >= 0; {
			name := text[i+5 : min(i+10, len(text))]
			if code, ok := diag.ParseCode(name); ok && !have[code] {
				t.Errorf("%s emits %s with no snapshot", f, name)
				have[code] = true
			}
			j := strings.Index(text[i+1:], "diag.E")
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
}
