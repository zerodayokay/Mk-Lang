// Why: implicit bounds must hold on every path that instantiates
// `Arena.Slice`, `raw.ReadUnaligned` and `raw.Atomic`, not only at a call,
// and a type or interface head with arguments must be a value only where the
// language allows it.
// What: TestArenaSliceZeroValidFnValue and TestNarrowedTypeParameter (zero-valid T as a fn value and when narrowed),
// TestGenericHeadFnValue, TestE0301_* (heads with arguments as values),
// TestRawImplicitBounds, TestIncrZeroValidReads.
// How: checks programs and compares codes; the last test reads the read set
// a zero-valid judgement records.

package check

import (
	"sort"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// TestArenaSliceZeroValidFnValue: `Arena.Slice` passed as
// a fn value instantiates it as a call does, so its T must be zero-valid
// there too (E0701 at the value). Its T is written,
// `Arena.Slice[R]`, never inferred from the expected type; a written
// argument on a module function, `raw.ReadUnaligned[f64]`, is in
// TestRawImplicitBounds.
func TestArenaSliceZeroValidFnValue(t *testing.T) {
	ap := func(ty string) string {
		return zvDecls + "Ap(f: fn(&Arena, int): OutOfMemory!mut []" + ty + ", a: &Arena): OutOfMemory!void {\n\t_ = try f(a, 1)\n}\n\n"
	}
	runM4Cases(t, []bodyCase{
		{"argument", ap("R") + "F(a: &Arena): OutOfMemory!void {\n\ttry Ap(Arena.Slice[R], a)\n}\n", "E0701@39:9"},
		{"argument zero-valid", ap("Pt") + "F(a: &Arena): OutOfMemory!void {\n\ttry Ap(Arena.Slice[Pt], a)\n}\n", ""},
		{"declared", zvDecls + "F(a: &Arena): OutOfMemory!void {\n\tg: fn(&Arena, int): OutOfMemory!mut []R = Arena.Slice[R]\n\t_ = try g(a, 1)\n}\n", "E0701@35:44"},
		{"declared zero-valid", zvDecls + "F(a: &Arena): OutOfMemory!void {\n\tg: fn(&Arena, int): OutOfMemory!mut []Pt = Arena.Slice[Pt]\n\t_ = try g(a, 1)\n}\n", ""},
	})
}

// TestNarrowedTypeParameter: inside `if T is X` T is X,
// so a zero-valid X is accepted and a reference is not.
func TestNarrowedTypeParameter(t *testing.T) {
	body := func(x string) string {
		return zvDecls + "F[T](a: &Arena): OutOfMemory!void {\n\tif T is " + x + " {\n\t\t_ = try a.Slice[T](1)\n\t}\n}\n"
	}
	runM4Cases(t, []bodyCase{
		{"narrowed to int", body("int"), ""},
		{"narrowed to a struct of integers", body("Pt"), ""},
		{"narrowed to a reference", body("&int"), "E0701@36:13"},
		{"narrowed to a struct with a reference", body("R"), "E0701@36:13"},
	})
}

// pDecls declares a generic struct with a Type.Name function; F starts on
// line 9.
const pDecls = "struct P[T] {\n\tv: T\n}\n\nP[T].Size(x: T): int {\n\treturn 1\n}\n\n"

// TestGenericHeadFnValue: `P[int].Size` as a fn
// value is the instance with the head's written arguments, as the call
// `P[int].Size(3)` is; an open head `P.Size` takes them from the expected
// type. (Size has no flows, so no E0608 enters the comparison.)
func TestGenericHeadFnValue(t *testing.T) {
	runBodyCases(t, []bodyCase{
		{"head arguments", pDecls + "F(): int {\n\tg := P[int].Size\n\treturn g(3)\n}\n", ""},
		{"head arguments against the expected type", pDecls + "F(): int {\n\tg: fn(str): int = P[int].Size\n\treturn g(\"abc\")\n}\n", "E0301@10:20"},
		{"head arguments matching the expected type", pDecls + "F(): int {\n\tg: fn(int): int = P[int].Size\n\treturn g(3)\n}\n", ""},
		// The expected type does not fix an open head.
		{"open head with an expected type", pDecls + "F(): int {\n\tg: fn(int): int = P.Size\n\treturn g(3)\n}\n", "E0711@10:20"},
		{"open head without an expected type", pDecls + "F(): int {\n\tg := P.Size\n\treturn 0\n}\n", "E0711@10:7"},
	})
	w := newWorld(t, true)
	b := checkBody(t, w, "", pDecls+"F(): int {\n\tg := P[int].Size\n\treturn g(3)\n}\n")
	body := w.c.Check(b.m, b.item("F"))
	if bad := silentPoison(w, body); len(bad) > 0 {
		t.Fatalf("Poison with no diagnostic: %v", bad)
	}
}

// headSrcs are programs with an interface or a generic type applied to
// arguments where a value is wanted.
var headSrcs = []bodyCase{
	{"interface namespace call", "F(xs: mut []int) {\n\tSlice[int].Fill(xs, 0)\n}\n", "E0301@2:2"},
	{"interface namespace call, wrong arguments", "F(xs: mut []int) {\n\tSlice[int].Fill(mut xs[..], \"nonsense\", 1, 2)\n}\n", "E0301@2:2"},
	{"interface namespace call, missing member", "F(xs: mut []int) {\n\tSlice[int].Nope(xs)\n}\n", "E0301@2:2"},
	{"user interface call", "interface Get[T] {\n\tValue(&self): T\n}\n\nF(): int {\n\treturn Get[int].Value(3)\n}\n", "E0301@6:9"},
	{"user interface fn value", "interface Get[T] {\n\tValue(&self): T\n}\n\nF() {\n\tg := Get[int].Value\n}\n", "E0301@6:7"},
	{"interface with arguments as a value", "interface Get[T] {\n\tValue(&self): T\n}\n\nF() {\n\tg := Get[int]\n}\n", "E0301@6:7"},
	{"struct with arguments as a value", pDecls + "F() {\n\tg := P[int]\n}\n", "E0301@10:7"},
}

// TestE0301_TypeHeadWithArgumentsNotValue: an interface is no namespace
// head, bare (`Slice.Fill`, E0301) or with arguments (`Slice[int].Fill`,
// the same E0301), and a type with arguments in value position is E0301
// too; never a silent Poison, which lower turns into an ICE. The E0301 is
// in F's own Body.Diags, the list that keeps the body from lower (before
// the fix F checked clean, and lower's "Poison with no diagnostic (check
// gap)" was E0000, exit 3).
func TestE0301_TypeHeadWithArgumentsNotValue(t *testing.T) {
	runBodyCases(t, headSrcs)
	for _, tc := range headSrcs {
		t.Run(tc.name+"/in the body", func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			b := checkBody(t, w, tc.want, tc.src)
			body := w.c.Check(b.m, b.item("F"))
			var ds []*diag.Diagnostic
			for _, d := range body.Diags {
				if d.Code != diag.E0208 {
					ds = append(ds, d)
				}
			}
			if got := codes(w, ds); got != tc.want {
				t.Fatalf("F's Body.Diags %q, want %q", got, tc.want)
			}
		})
	}
}

// TestE0301_ForeignHeadNotValue: the same over an import: a
// module-qualified interface or type with arguments in value position is
// E0301 in the body, and `lib.P[int].Size` as a fn value or a call takes the
// head's arguments.
func TestE0301_ForeignHeadNotValue(t *testing.T) {
	const lib = "export struct P[T] {\n\tv: T\n}\n\nexport P[T].Size(x: T): int {\n\treturn 1\n}\n\nexport interface Get[T] {\n\tValue(&self): T\n}\n"
	for _, tc := range []bodyCase{
		{"type as a value", "import lib\n\nF() {\n\tx := lib.P[int]\n}\n", "E0301@4:7"},
		{"interface as a value", "import lib\n\nF() {\n\tx := lib.Get[int]\n}\n", "E0301@4:7"},
		{"interface namespace call", "import lib\n\nF(): int {\n\treturn lib.Get[int].Value(3)\n}\n", "E0301@4:9"},
		{"fn value of a closed head", "import lib\n\nF(): int {\n\tg := lib.P[int].Size\n\treturn g(3)\n}\n", ""},
		{"call of a closed head", "import lib\n\nF(): int {\n\treturn lib.P[int].Size(3)\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, true)
			w.add("lib", opt{}, lib)
			b := w.add("m", opt{}, tc.src)
			body := w.c.Check(b.m, b.item("F"))
			var ds []*diag.Diagnostic
			for _, d := range append(b.diags(), body.Diags...) {
				if d.Code != diag.E0208 {
					ds = append(ds, d)
				}
			}
			if got := codes(w, ds); got != tc.want {
				t.Fatalf("diagnostics %q, want %q\n%s", got, tc.want, dump(w, ds))
			}
			if tc.want == "" {
				if bad := silentPoison(w, body); len(bad) > 0 {
					t.Fatalf("Poison with no diagnostic: %v", bad)
				}
			}
		})
	}
}

// rawDecls is a std.raw stub with the declarations the integer
// bounds key on, as std/raw/raw.mk declares them.
const rawDecls = "export ReadUnaligned[T: Plain](p: *T): T\n\nexport WriteUnaligned[T: Plain](p: *T, x: T)\n\nexport struct Atomic[T: Plain] {\n\tv: T\n}\n\nexport Atomic[T].New(x: T): Atomic[T]\n\nexport Atomic[T].Load(&self): T\n"

// TestRawImplicitBounds: `raw.Atomic[T]` needs an integer T of 1, 2,
// 4 or 8 bytes or a raw pointer (TestAtomicBounds has more cases)
// and `raw.ReadUnaligned[T]` and `raw.WriteUnaligned[T]` an integer T, else E0701 at the instantiation
// (a type) or the call (an inferred or fn-value instance).
func TestRawImplicitBounds(t *testing.T) {
	w := newWorld(t, true)
	raw := w.add("std.raw", opt{std: true}, rawDecls)
	if len(raw.diags()) > 0 {
		t.Fatalf("std.raw:\n%s", dump(w, raw.diags()))
	}
	w.c.known = w.rs.Known()
	if w.c.known.Atomic == types.NoItem {
		t.Fatal("Known.Atomic not set")
	}
	const imp = "import std.raw\n\n"
	for i, tc := range []bodyCase{
		{"atomic fields", imp + "export struct S {\n\ta: raw.Atomic[u8]\n\tb: raw.Atomic[u32]\n\tc: raw.Atomic[i64]\n\td: raw.Atomic[int]\n}\n", ""},
		{"atomic f64 field", imp + "export struct S {\n\ta: raw.Atomic[f64]\n}\n", "E0701@4:5"},
		{"atomic array field", imp + "export struct S {\n\ta: raw.Atomic[[3]u8]\n}\n", "E0701@4:5"},
		{"atomic u16 field", imp + "export struct S {\n\ta: raw.Atomic[u16]\n}\n", ""},
		{"atomic bool field", imp + "export struct S {\n\ta: raw.Atomic[bool]\n}\n", "E0701@4:5"},
		{"atomic generic field", imp + "export struct S[T] {\n\ta: raw.Atomic[T]\n}\n", "E0701@4:5"},
		{"atomic head", imp + "F(): u32 {\n\ta := raw.Atomic[u32].New(1)\n\treturn a.Load()\n}\n", ""},
		{"atomic f64 head", imp + "F(): f64 {\n\ta := raw.Atomic[f64].New(1.5)\n\treturn a.Load()\n}\n", "E0701@4:7"},
		{"atomic inferred", imp + "F(): u64 {\n\tx: u64 = 1\n\ta := raw.Atomic.New(x)\n\treturn a.Load()\n}\n", ""},
		{"atomic f64 inferred", imp + "F(): f64 {\n\ta := raw.Atomic.New(1.5)\n\treturn a.Load()\n}\n", "E0701@4:18"},
		{"atomic f64 fn value", imp + "F() {\n\tg: fn(x: f64): raw.Atomic[f64] from x = raw.Atomic[f64].New\n}\n", "E0701@4:17 E0701@4:42"},
		{"atomic narrowed", imp + "F[T](x: T) {\n\tif T is u32 {\n\t\ta := raw.Atomic.New(x)\n\t\t_ = a.Load()\n\t}\n}\n", ""},
		// A type expression in an if T is X branch is judged narrowed.
		{"atomic narrowed type", imp + "F[T](x: T) {\n\tif T is u32 {\n\t\tvar a: raw.Atomic[T] = raw.Atomic.New(x)\n\t\t_ = a.Load()\n\t}\n}\n", ""},
		{"atomic narrowed to f64 type", imp + "F[T](x: T) {\n\tif T is f64 {\n\t\tvar a: raw.Atomic[T] = raw.Atomic.New(x)\n\t\t_ = a.Load()\n\t}\n}\n", "E0701@5:10 E0701@5:37"},
		// The namespace form over a formed Atomic[f64] is judged once,
		// at the type; New's T comes from no Atomic, so it is judged there.
		{"atomic namespace load over f64", imp + "F(): f64 {\n\tvar a: raw.Atomic[f64]\n\treturn raw.Atomic.Load(&a)\n}\n", "E0701@4:9"},
		{"atomic namespace load over u32", imp + "F(): u32 {\n\tvar a: raw.Atomic[u32]\n\treturn raw.Atomic.Load(&a)\n}\n", ""},
		{"atomic namespace new then load", imp + "F(): f64 {\n\ta := raw.Atomic.New(1.5)\n\treturn raw.Atomic.Load(&a)\n}\n", "E0701@4:18"},
		{"unaligned u64", imp + "F(p: *u64): u64 {\n\tunsafe \"test\" {\n\t\treturn raw.ReadUnaligned(p)\n\t}\n}\n", ""},
		{"unaligned u16", imp + "F(p: *u16): u16 {\n\tunsafe \"test\" {\n\t\treturn raw.ReadUnaligned(p)\n\t}\n}\n", ""},
		{"unaligned record", imp + "F(p: *(a: u8, b: u16)): u8 {\n\tunsafe \"test\" {\n\t\treturn raw.ReadUnaligned(p).a\n\t}\n}\n", "E0701@5:10"},
		{"unaligned f64", imp + "F(p: *f64): f64 {\n\tunsafe \"test\" {\n\t\treturn raw.ReadUnaligned[f64](p)\n\t}\n}\n", "E0701@5:10"},
		{"unaligned fn value", imp + "F() {\n\tg: fn(p: *f32): f32 from p = raw.ReadUnaligned[f32]\n}\n", "E0701@4:31"},
		{"unaligned fn value u32", imp + "F() {\n\tg: fn(p: *u32): u32 from p = raw.ReadUnaligned[u32]\n}\n", ""},
		{"unaligned written fn value", imp + "F() {\n\tg := raw.ReadUnaligned[f64]\n}\n", "E0701@4:7"},
		{"unaligned written fn value u8", imp + "F() {\n\tg := raw.ReadUnaligned[u8]\n}\n", ""},
		{"unaligned store u64", imp + "F(p: *u64, x: u64) {\n\tunsafe \"test\" {\n\t\traw.WriteUnaligned(p, x)\n\t}\n}\n", ""},
		{"unaligned store u16", imp + "F(p: *u16) {\n\tunsafe \"test\" {\n\t\traw.WriteUnaligned[u16](p, 7)\n\t}\n}\n", ""},
		{"unaligned store record", imp + "F(p: *(a: u8, b: u16), x: (a: u8, b: u16)) {\n\tunsafe \"test\" {\n\t\traw.WriteUnaligned(p, x)\n\t}\n}\n", "E0701@5:3"},
		{"unaligned store f64", imp + "F(p: *f64) {\n\tunsafe \"test\" {\n\t\traw.WriteUnaligned[f64](p, 1.5)\n\t}\n}\n", "E0701@5:3"},
		{"unaligned store fn value", imp + "F() {\n\tg: fn(p: *f32, x: f32) = raw.WriteUnaligned[f32]\n}\n", "E0701@4:27"},
		{"unaligned store fn value u32", imp + "F() {\n\tg: fn(p: *u32, x: u32) = raw.WriteUnaligned[u32]\n}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := w.add("std.m"+string(rune('a'+i)), opt{std: true}, tc.src)
			// Left out: unused locals and the unsafe-call rule of std.raw's
			// free functions (E1101): neither is this rule. E0608 is
			// compared (instFlows): the fn values here are clean.
			var got []string
			for _, d := range append(b.diags(), b.bodyDiags(t)...) {
				if s := d.Code.String(); s != "E0208" && s != "E1101" {
					got = append(got, s+"@"+w.lc(d.At))
				}
			}
			if g := sortStrings(got); g != tc.want {
				t.Fatalf("diagnostics %q, want %q\n%s", g, tc.want, dump(w, append(b.diags(), b.bodyDiags(t)...)))
			}
		})
	}
}

// TestIncrZeroValidReads: a zero-valid judgement reads the shape, every
// field (private ones too) and the declaration of each type it walks, at any
// depth, and an enum's declaration (its discriminants), so an edit that
// changes the answer changes a read hash. A struct's invariants are part of
// its declaration hash (mk.decl.v1), so the ReadSignature recorded here
// covers them too.
func TestIncrZeroValidReads(t *testing.T) {
	const lib = "export struct Inner {\n\texport n: int\n}\n\nexport struct R {\n\texport i: Inner\n\te: E\n}\n\nexport enum E {\n\tA\n\tB\n}\n"
	w := newWorldCore(t, true, generatedCore())
	w.add("lib", opt{}, lib)
	b := w.add("m", opt{}, "import lib\n\nF(a: &Arena): OutOfMemory!void {\n\t_ = try a.Slice[lib.R](1)\n}\n")
	b.wantClean(t)
	var reads []types.Read
	for _, id := range b.m.Units() {
		if w.items.Get(id).Key.String() == "m.F" {
			reads = w.c.Check(b.m, id).Reads.Sorted()
		}
	}
	var all []string
	has := map[string]bool{}
	for _, r := range reads {
		s := r.Kind.String() + " " + r.Key.String() + " " + r.Name
		all = append(all, s)
		has[s] = true
	}
	for _, want := range []struct {
		k         types.ReadKind
		key, name string
	}{
		{types.ReadShape, "lib.R", ""},
		{types.ReadFields, "lib.R", "all"},
		{types.ReadSignature, "lib.R", ""},
		{types.ReadShape, "lib.Inner", ""},
		{types.ReadFields, "lib.Inner", "all"},
		{types.ReadSignature, "lib.Inner", ""},
		{types.ReadFields, "lib.E", "all"},
	} {
		if s := want.k.String() + " " + want.key + " " + want.name; !has[s] {
			t.Errorf("no %q in\n%s", s, strings.Join(all, "\n"))
		}
	}
}

func sortStrings(s []string) string {
	sort.Strings(s)
	return strings.Join(s, " ")
}
