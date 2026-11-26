// Why: an integer const of another module reaches lower as its value at
// its own width, and every use of it (a conversion folded by lower, an
// operator, a pattern, a const initializer run by the evaluator, a const
// argument) must read it with its type's signedness, as a same-module const
// is read; a wrong reading is a silent wrong value.
// What: TestImportedConstMatchesLocal, a table over source widths and
// signedness, aliases, enum tags, a const re-exported through a third
// module, and the forms that use the value (operators, conversions, const
// initializers, a const argument, a match pattern, loop bounds), with
// narrowing conversions `T(m.C)` that check judges by value and lower
// folds; TestCImportNegativeConstWidens, the same for a C import's
// negative macro and enum constant; TestCImportConstNarrowing, a C
// import's constants narrowed, and refused when they do not fit.
// How: one generated package with the consts in module consts, copies of
// them in main and consts re-declared from them in relay; each row prints
// every form over the local copy, the imported const and the relay, in the
// dev and the release profile, and the lines must agree. The C import's
// values are compared with what C gives them.

package driver

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// widenForm is one use of a const: t is the type of the form's value, text
// the form with @ for the const.
type widenForm struct {
	t, text string
}

// widenRow is one const: its name, declared type ("" for untyped), the
// integer type its value has, its initializer, whether relay re-exports
// it, and the forms to print. A narrowing row names in narrow the type
// its first form converts to, which a match matches against that form's
// const; loop adds a loop bounded by that conversion.
type widenRow struct {
	name, decl, st, value string
	relay                 bool
	forms                 []widenForm
	narrow                string
	loop                  bool
}

// intForms are the forms over an integer const of type st; signed adds one
// for a signed st, small adds f64(@) (lossless only up to 32 bits), and
// wider lists the lossless conversions to wider types. Narrowing `T(C)` by
// value is narrowForms'.
func intForms(st string, signed, small bool, wider ...string) []widenForm {
	fs := []widenForm{{"i8", "Trunc[i8](@)"}, {"u16", "Trunc[u16](@)"},
		{st, "@ >> 1"}, {st, "@ / 3"}, {st, "@ % 3"}, {"bool", "@ < 0"}, {"bool", "@ == 0"},
		{"bool", "@ > 1"}, {st, "@ | 1"}, {st, "@ + 0"},
	}
	if st != "u64" && st != "uint" {
		fs = append(fs, widenForm{"i64", "Cast[i64](@)"}, widenForm{"i64", "i64(@)"}, widenForm{"int", "int(@)"},
			widenForm{"i64", "i64(@) >> 1"}, widenForm{"bool", "i64(@) < 0"}, widenForm{"i64", "i64(@) / 3"},
			widenForm{"i64", "Scale(i64(@))"}, widenForm{"i64", "Sum(i64(@))"})
	}
	if small {
		fs = append(fs, widenForm{"f64", "f64(@)"})
	}
	if signed {
		fs = append(fs, widenForm{"i64", "Cast[i64](@) - 1"})
	}
	for _, w := range wider {
		fs = append(fs, widenForm{w, w + "(@)"})
	}
	return fs
}

// narrowForms are the narrowing conversions `T(@)` whose value fits T, each
// also widened back with Cast, an i8 one also as a const argument, then
// extra.
func narrowForms(to []string, extra ...widenForm) []widenForm {
	var fs []widenForm
	for _, t := range to {
		fs = append(fs, widenForm{t, t + "(@)"}, widenForm{"i64", "Cast[i64](" + t + "(@))"})
		if t == "i8" {
			fs = append(fs, widenForm{"i64", "Scale8(i8(@))"})
		}
	}
	return append(fs, extra...)
}

var widenRows = []widenRow{
	{name: "S8", decl: "i8", st: "i8", value: "-4", relay: true, forms: intForms("i8", true, true, "i16", "i32", "i64", "int")},
	{name: "M8", decl: "i8", st: "i8", value: "-128", forms: intForms("i8", true, true, "i16", "i32")},
	{name: "S16", decl: "i16", st: "i16", value: "-300", relay: true, forms: intForms("i16", true, true, "i32")},
	{name: "S32", decl: "i32", st: "i32", value: "-4", relay: true, forms: intForms("i32", true, true)},
	{name: "M32", decl: "i32", st: "i32", value: "-2147483648", forms: intForms("i32", true, true)},
	{name: "P32", decl: "i32", st: "i32", value: "2147483647", forms: intForms("i32", true, true)},
	{name: "S64", decl: "i64", st: "i64", value: "-4", forms: intForms("i64", true, false, "int")},
	{name: "SI", decl: "int", st: "int", value: "-4", forms: intForms("int", true, false, "i64")},
	{name: "UN", decl: "", st: "int", value: "-4", relay: true, forms: intForms("int", true, false)},
	{name: "U8", decl: "u8", st: "u8", value: "200", forms: intForms("u8", false, true, "u16", "u32", "u64", "i16")},
	{name: "U16", decl: "u16", st: "u16", value: "65000", forms: intForms("u16", false, true, "u32", "i32", "u64")},
	{name: "U32", decl: "u32", st: "u32", value: "4000000000", forms: intForms("u32", false, false, "u64", "uint")},
	{name: "U64", decl: "u64", st: "u64", value: "18446744073709551615", forms: intForms("u64", false, false)},
	{name: "AL", decl: "Off", st: "i32", value: "-7", relay: true, forms: intForms("i32", true, true)},
	// Narrowing conversions, judged by value: signed and unsigned sources
	// and targets of every width, at the targets' bounds.
	{name: "N32", decl: "i32", st: "i32", value: "-4", relay: true, narrow: "i8", loop: true,
		forms: narrowForms([]string{"i8", "i16"}, widenForm{"f32", "f32(@)"}, widenForm{"i64", "i64(i8(@)) - 1"})},
	{name: "N32L", decl: "i32", st: "i32", value: "-128", narrow: "i8", loop: true, forms: narrowForms([]string{"i8"})},
	{name: "N32H", decl: "i32", st: "i32", value: "127", narrow: "i8", forms: narrowForms([]string{"i8", "u8", "u32", "u64"})},
	{name: "N16", decl: "i16", st: "i16", value: "255", narrow: "u8", forms: narrowForms([]string{"u8"})},
	{name: "N64", decl: "i64", st: "i64", value: "-4", narrow: "i8", loop: true, forms: narrowForms([]string{"i8", "i16", "i32"})},
	{name: "N64L", decl: "i64", st: "i64", value: "-2147483648", narrow: "i32", forms: narrowForms([]string{"i32"})},
	{name: "NI", decl: "int", st: "int", value: "100", narrow: "i8", loop: true, forms: narrowForms([]string{"i8", "u8", "i32", "u32", "uint"})},
	{name: "NU32", decl: "u32", st: "u32", value: "200", relay: true, narrow: "u8", loop: true,
		forms: narrowForms([]string{"u8", "i16", "i32"}, widenForm{"char", "char(@)"})},
	{name: "NU64", decl: "u64", st: "u64", value: "255", narrow: "u8", forms: narrowForms([]string{"u8", "i16", "u32", "i64", "int"})},
	{name: "NUI", decl: "uint", st: "uint", value: "9223372036854775807", narrow: "i64", forms: narrowForms([]string{"i64", "int"})},
	{name: "NAL", decl: "Off", st: "i32", value: "-4", relay: true, narrow: "i8", loop: true, forms: narrowForms([]string{"i8"})},
	{name: "EN", decl: "Lv", st: "Lv", value: "Lv.Lo", forms: []widenForm{
		{"i8", "i8(@)"}, {"i16", "i16(@)"}, {"i64", "i64(@)"}, {"int", "int(@)"}}},
}

// widenPackage is the package of the table: consts, relay and main.
func widenPackage() map[string]string {
	var cs, rs, ms strings.Builder
	cs.WriteString("export type Off = i32\n\nexport enum Lv: i8 {\n    Lo = -2\n    Hi = 3\n}\n\n")
	ms.WriteString("import consts\nimport relay\n\ntype Off = i32\n\nenum Lv: i8 {\n    Lo = -2\n    Hi = 3\n}\n\n")
	ms.WriteString("Scale(const k: i64): i64 {\n    return k * 2\n}\n\n")
	ms.WriteString("Scale8(const k: i8): i64 {\n    return i64(k) * 2\n}\n\n")
	ms.WriteString("Sum(lo: i64): i64 {\n    var s: i64 = 0\n    for i in lo..lo + 3 {\n        s += i\n    }\n    return s\n}\n\n")
	rs.WriteString("import consts\n\n")
	decl := func(r widenRow) string {
		if r.decl == "" {
			return ""
		}
		return ": " + r.decl
	}
	var calls strings.Builder
	for _, r := range widenRows {
		cs.WriteString(fmt.Sprintf("export const %s%s = %s\n", r.name, decl(r), r.value))
		ms.WriteString(fmt.Sprintf("const %s%s = %s\n", r.name, decl(r), r.value))
		views := [][2]string{{"same", r.name}, {"cross", "consts." + r.name}}
		if r.relay {
			rs.WriteString(fmt.Sprintf("export const %s%s = consts.%s\n", r.name, strings.Replace(decl(r), "Off", "consts.Off", 1), r.name))
			views = append(views, [2]string{"relay", "relay." + r.name})
		}
		for _, v := range views {
			fn := v[0] + r.name
			for i, f := range r.forms {
				ms.WriteString(fmt.Sprintf("const K%s%d: %s = %s\n", fn, i, f.t, strings.ReplaceAll(f.text, "@", v[1])))
			}
			if r.narrow != "" {
				// The narrowed const as a match pattern, through the form's const.
				ms.WriteString(fmt.Sprintf("\nPatN%s(x: %s): int {\n    return match x {\n        K%s0 { 1 }\n        _ { 0 }\n    }\n}\n", fn, r.narrow, fn))
			}
			if r.st != "Lv" {
				ms.WriteString(fmt.Sprintf("\nPat%s(x: %s): int {\n    return match x {\n        %s { 1 }\n        _ { 0 }\n    }\n}\n", fn, r.st, v[1]))
				// The const as a loop's bounds, as itself and widened.
				c := v[1]
				ms.WriteString(fmt.Sprintf("\nLoop%s(): i64 {\n    var s: i64 = 0\n    for i in %s..(%s | 1) {\n        s += Cast[i64](i)\n    }\n", fn, c, c))
				if r.st != "u64" && r.st != "uint" {
					ms.WriteString(fmt.Sprintf("    for i in i64(%s)..i64(%s) + 2 {\n        s += i\n    }\n", c, c))
				}
				if r.loop {
					ms.WriteString(fmt.Sprintf("    for i in %s(%s)..%s(%s) + 2 {\n        s += Cast[i64](i)\n    }\n", r.narrow, c, r.narrow, c))
				}
				ms.WriteString("    return s\n}\n")
			}
			ms.WriteString(fmt.Sprintf("\nPrint%s(sys: &System): WriteError!void {\n    try sys.Print(\"%s %s:\", ())\n", fn, v[0], r.name))
			for i, f := range r.forms {
				ms.WriteString(fmt.Sprintf("    r%d := %s\n    k%d := K%s%d\n    try sys.Print(\" {r%d} {k%d}\", (r%d, k%d))\n",
					i, strings.ReplaceAll(f.text, "@", v[1]), i, fn, i, i, i, i, i))
			}
			if r.st != "Lv" {
				ms.WriteString(fmt.Sprintf("    x: %s = %s\n    y: %s = 0\n", r.st, v[1], r.st))
				ms.WriteString(fmt.Sprintf("    p := Pat%s(x)\n    q := Pat%s(y)\n    l := Loop%s()\n", fn, fn, fn))
				ms.WriteString("    try sys.Print(\" {p} {q} {l}\", (p, q, l))\n")
			}
			if r.narrow != "" {
				ms.WriteString(fmt.Sprintf("    xn: %s = %s(%s)\n    yn: %s = 0\n", r.narrow, r.narrow, v[1], r.narrow))
				ms.WriteString(fmt.Sprintf("    pn := PatN%s(xn)\n    qn := PatN%s(yn)\n", fn, fn))
				ms.WriteString("    try sys.Print(\" {pn} {qn}\", (pn, qn))\n")
			}
			ms.WriteString("    try sys.Print(\"\\n\", ())\n}\n\n")
			calls.WriteString(fmt.Sprintf("    try Print%s(sys)\n", fn))
		}
	}
	ms.WriteString("export Main(sys: &System): WriteError!void {\n" + calls.String() + "}\n")
	return map[string]string{
		"pkg.mk":           manifest(map[string]string{"name": `"widen"`, "bins": `[pkg.Bin(name: "widen", root: "app", link_script: "", cpu: "", subsystem: "console")]`}),
		"app/main.mk":      ms.String(),
		"consts/consts.mk": cs.String(),
		"relay/relay.mk":   rs.String(),
	}
}

// TestImportedConstMatchesLocal: every form over another module's const
// (directly, and re-exported through a third module) prints what the same
// form over a same-module const of the same type and value prints, in the
// dev and the release profile.
func TestImportedConstMatchesLocal(t *testing.T) {
	t.Parallel()
	tools := needTools(t)
	dir := writeTree(t, widenPackage())
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			s, err := NewSession(Config{Root: dir, Tools: tools, CacheDir: t.TempDir(), Profile: prof})
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(t.TempDir(), "widen"+exeSuffix(HostTarget())))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Report.Diags) > 0 || len(paths) != 1 {
				t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
			}
			out, errOut, code := runExe(t, HostTarget(), paths[0], "")
			if code != 0 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			lines := map[string]string{}
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				view, rest, _ := strings.Cut(strings.TrimSpace(l), " ")
				name, _, _ := strings.Cut(rest, ":")
				lines[view+" "+name] = rest
			}
			for _, row := range widenRows {
				same := lines["same "+row.name]
				if same == "" {
					t.Fatalf("%s: no line for the same-module const\n%s", row.name, out)
				}
				views := []string{"cross"}
				if row.relay {
					views = append(views, "relay")
				}
				for _, v := range views {
					if got := lines[v+" "+row.name]; got != same {
						t.Errorf("%s %s:\n got %s\nwant %s", v, row.name, got, same)
					}
				}
			}
			if strings.Contains(lines["same S32"], "4294967292") || !strings.Contains(lines["same S32"], " -4 -4 -4 -4") {
				t.Errorf("same S32 = %q: i64(S32) of S32 = -4 is not -4", lines["same S32"])
			}
			// A narrowing `T(m.C)` is folded by lower from the value check
			// judged: its first form, at run time and as a const, widened back.
			for name, want := range map[string]string{"N32": "-4", "N32L": "-128", "N32H": "127", "N16": "255",
				"N64": "-4", "N64L": "-2147483648", "NI": "100", "NU32": "200", "NU64": "255",
				"NUI": "9223372036854775807", "NAL": "-4"} {
				w := name + ": " + strings.Repeat(want+" ", 3) + want
				if got := lines["cross "+name]; !strings.HasPrefix(got, w) {
					t.Errorf("cross %s = %q, want it to begin %q", name, got, w)
				}
			}
		})
	}
}

// TestCImportNegativeConstWidens: a C import's negative macro and enum
// constant are i32 consts of another module, so widening them is the same
// fold: `i64(k.Neg)` of `#define NEG (-4)` is -4.
func TestCImportNegativeConstWidens(t *testing.T) {
	needTools(t)
	dir := writeTree(t, cBin("cw", "#define NEG (-4)\nenum { ENEG = -5 };\n", "",
		cMain("", "        a := i64(k.Neg)\n        b := int(k.Eneg)\n        c := Cast[i64](k.Neg)\n",
			"{a} {b} {c}", "a: a, b: b, c: c")))
	if out := buildRun(t, dir, t.TempDir()); out != "-4 -5 -4\n" {
		t.Errorf("printed %q, want -4 -5 -4", out)
	}
}

// TestCImportConstNarrowing: a C import's macro and enum constant are
// judged by value in a narrowing `T(k.X)`, as a Mk module's const is: one
// that fits compiles and keeps its value, one that does not is E0303.
func TestCImportConstNarrowing(t *testing.T) {
	needTools(t)
	const h = "#define NEG (-4)\n#define SMALL 200\n#define BIG 300\nenum { ENEG = -5 };\n"
	dir := writeTree(t, cBin("cn", h, "",
		cMain("", "        a := i8(k.Neg)\n        b := i8(k.Eneg)\n        c := u8(k.Small)\n        d := Cast[i64](i8(k.Neg))\n",
			"{a} {b} {c} {d}", "a: a, b: b, c: c, d: d")))
	if out := buildRun(t, dir, t.TempDir()); out != "-4 -5 200 -4\n" {
		t.Errorf("printed %q, want -4 -5 200 -4", out)
	}
	dir = writeTree(t, cBin("cb", h, "", cMain("", "        a := u8(k.Big)\n        b := u8(k.Neg)\n", "{a} {b}", "a: a, b: b")))
	r, _ := buildAt(t, dir, t.TempDir())
	n := 0
	for _, d := range r.Report.Diags {
		if d.Diag.Code == diag.E0303 {
			n++
		}
	}
	if n != 2 || len(r.Report.Diags) != 2 {
		t.Errorf("u8(k.Big), u8(k.Neg): want two E0303\n%s", r.Report.Text(r.Sources))
	}
}
