// Why: mk check reuses cached modules and functions; an edit must recheck
// exactly what it can change, and a warm check must equal a cold one.
// What: the TestInvalidation_* tests, one per edit kind of doc/compiler.md's
// invalidation table plus edits the table folds together, numbered rows 1
// to 15 here (incremental_test.go and invalidation_gen_test.go use the same
// numbers), over the harness invPkg and runInvRow.
// How: checks the harness cold, applies a row's edits, checks warm in a new
// Session, and asserts the missed modules, the rechecked functions, the
// files parsed and that the --json equals a cold check.

package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/types"
)

// The invalidation table of doc/compiler.md for mk check
// (TestInvalidation_*): each row asserts the package modules that miss their
// interface lookup and run stage 3, and the package functions whose body
// runs stage 6 again (the "Rechecks" column; the other functions of a missed
// module are loaded from their own results, funccache.go). A row also
// asserts that the warm --json equals a cold check of the edited source into
// a fresh cache, and that a third check with no edit resolves and checks
// nothing (the warm build's candidates were stored). A const or global of a
// missed module is always rechecked (b.K below): only plain functions are
// reused (funccache.go). Row 6 (generator) has its own harness
// (invalidation_gen_test.go). Every row also asserts Trace.FilesParsed: the
// edited files and the files of the missed modules, no std file
// (parsesum.go).

// invPkg is the harness: a imports b imports c, and g holds a const helper;
// each has exported and private items, b a generic, a const, an interface
// and a private type used in a generic body, a reads b.T's fields and shape.
// (c is reserved for C import, so module c is named cc.)
func invPkg() map[string]string {
	return map[string]string{
		"pkg.mk":   manifest(nil),
		"cc/cc.mk": "export struct U {\n    export v: u32\n}\n\nexport MakeU(x: u32): U {\n    return U(v: x)\n}\n",
		"b/b.mk": "import cc\n\n" +
			"export struct T {\n    export a: u32\n    hidden: u32\n}\n\n" +
			"export interface Show {\n    Show(&self): int\n}\n\n" +
			"export const K: int = 7\n\n" +
			"export F(x: int): int {\n    return x + 1\n}\n\n" +
			"export G(x: int): int {\n    return x * 3\n}\n\n" +
			"export Wrap[X](x: X): X {\n    return x\n}\n\n" +
			"struct Priv {\n    pv: u32\n}\n\n" +
			"export Keep[X](x: X): X {\n    p := Priv(pv: 1)\n    _ = p.pv\n    return x\n}\n\n" +
			"export NewT(): T {\n    return T(a: 1, hidden: 2)\n}\n\n" +
			"export UseU(u: cc.U): u32 {\n    return u.v\n}\n",
		"g/g.mk":  "Helper(): int {\n    return 4\n}\n\nexport const N: int = Helper()\n",
		"a/a.mk":  "import b\nimport g\n\nexport P(t: &b.T): u32 {\n    return t.a\n}\n\nexport Q(): int {\n    return b.F(1) + b.G(2) + b.K + b.Wrap(3) + b.Keep(4) + g.N\n}\n",
		"a/a2.mk": "R(): int {\n    return 1\n}\n",
	}
}

// invRow is one row: edits applied to the cold tree, the package modules
// that must miss (run stage 3), the package items whose body must run
// stage 6 again (the "Rechecks" column; nil: none), and the config change
// (row 15).
type invRow struct {
	edits   map[string]func(string) string
	missed  []string
	checked []string
	cfg     func(*Config)
}

func replace(old, new string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			panic("invalidation row: " + old + " not in the file")
		}
		return strings.Replace(s, old, new, 1)
	}
}

func edit(path string, f func(string) string) map[string]func(string) string {
	return map[string]func(string) string{path: f}
}

// pkgItems are the item keys of package modules, excluding std and system views.
func pkgItems(ks []types.Key) []string {
	var out []string
	for _, k := range ks {
		name := k.String()
		if !strings.HasPrefix(name, "std.") && !strings.HasPrefix(name, sysViewPrefix) {
			out = append(out, name)
		}
	}
	return out
}

// runInvRow checks the harness cold into a fresh cache, applies the row's
// edits, checks warm in a new Session and asserts the row; see the file
// comment. The exact checked sets are stable only while no reused function
// reads a same-module body or const value: such a read is validated while
// sibling units may still be checking, so it would miss or hit by timing.
func runInvRow(t *testing.T, row invRow) {
	t.Helper()
	files := invPkg()
	dir := writeTree(t, files)
	cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
	cold := frontCheck(t, cfg)
	if len(cold.Report.Diags) > 0 || len(cold.Trace.NotStored) > 0 {
		t.Fatalf("cold check: not stored %q\n%s", cold.Trace.NotStored, cold.Report.Text(cold.Sources))
	}
	for path, f := range row.edits {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(f(files[path])), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if row.cfg != nil {
		row.cfg(&cfg)
	}
	warm := frontCheck(t, cfg)
	if got := pkgOnly(warm.Trace.ModulesResolved); !slices.Equal(got, row.missed) {
		t.Errorf("missed %q, want %q", got, row.missed)
	}
	// The parse step parses only the edited files (their parse
	// summaries miss); stage 3 parses the other files of the missed
	// modules. Std or system view files only need parsing on a build ID change.
	var parsed, stdParsed []string
	for path := range files {
		_, edited := row.edits[path]
		dir, _, _ := strings.Cut(path, "/")
		if path != "pkg.mk" && (edited || slices.Contains(row.missed, dir)) {
			parsed = append(parsed, path)
		}
	}
	slices.Sort(parsed)
	var gotParsed []string
	for _, f := range warm.Trace.FilesParsed {
		if isStdOrViewSource(f) {
			stdParsed = append(stdParsed, f)
		} else {
			gotParsed = append(gotParsed, f)
		}
	}
	if !slices.Equal(gotParsed, parsed) {
		t.Errorf("parsed %q, want %q", gotParsed, parsed)
	}
	if row.cfg == nil && len(stdParsed) > 0 {
		t.Errorf("parsed std files %q", stdParsed)
	}
	if got := pkgItems(warm.Trace.ItemsChecked); !slices.Equal(got, row.checked) {
		t.Errorf("rechecked %q, want %q (from cache %q)", got, row.checked, pkgItems(warm.Trace.ItemsFromCache))
	}
	fresh := cfg
	fresh.CacheDir = t.TempDir()
	cold2 := frontCheck(t, fresh)
	if !bytes.Equal(jsonOf(t, warm), jsonOf(t, cold2)) {
		t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold2))
	}
	if len(warm.Report.Diags) > 0 {
		t.Errorf("the edited source does not check:\n%s", warm.Report.Text(warm.Sources))
	}
	third := frontCheck(t, cfg)
	if len(third.Trace.ModulesResolved) > 0 || len(third.Trace.ItemsChecked) > 0 || len(third.Trace.FilesParsed) > 0 {
		t.Errorf("third check resolved %q, checked %q, parsed %q", third.Trace.ModulesResolved, pkgItems(third.Trace.ItemsChecked),
			third.Trace.FilesParsed)
	}
}

// Row 1: comment and whitespace. Nothing.
func TestInvalidation_Comment(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("    return x + 1\n", "    return x + 1   // trailing\n\n// between\n\n"))})
}

// Row 2: a line added above a function. Nothing.
func TestInvalidation_LineAbove(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("export F(x: int)", "\nexport F(x: int)"))})
}

// Row 3: the body of a non-generic function. That function.
func TestInvalidation_NonGenericBody(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("return x + 1", "return x + 2")),
		missed: []string{"b"}, checked: []string{"b.F", "b.K"}})
}

// Row 4: the body of a generic. That function.
func TestInvalidation_GenericBody(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("export Wrap[X](x: X): X {\n    return x\n}", "export Wrap[X](x: X): X {\n    y := x\n    return y\n}")),
		missed: []string{"b"}, checked: []string{"b.K", "b.Wrap"}})
}

// Row 5: a body that ran in a const evaluation. The evaluation reruns;
// dependents only when the value changed.
func TestInvalidation_ConstEvalBody(t *testing.T) {
	t.Run("SameValue", func(t *testing.T) {
		runInvRow(t, invRow{edits: edit("g/g.mk", replace("return 4", "return 2 + 2")),
			missed: []string{"g"}, checked: []string{"g.Helper", "g.N"}})
	})
	t.Run("NewValue", func(t *testing.T) {
		runInvRow(t, invRow{edits: edit("g/g.mk", replace("return 4", "return 5")),
			missed: []string{"a", "g"}, checked: []string{"a.Q", "g.Helper", "g.N"}})
	})
}

// Row 7: a private field, shape unchanged. The module; importers whose loan
// check walked the field (a.P borrows a b.T: borrow's ReadFields "all",
// since the shape alone cannot tell whether the field holds a Cell).
func TestInvalidation_PrivateFieldShapeSame(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", func(s string) string { return strings.ReplaceAll(s, "hidden", "secret") }),
		missed: []string{"a", "b"}, checked: []string{"a.P", "b.K", "b.NewT"}})
}

// Row 8: a private field, shape changed. Importers using the type.
func TestInvalidation_PrivateFieldShapeChanged(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("hidden: u32", "hidden: u64")),
		missed: []string{"a", "b"}, checked: []string{"a.P", "b.K", "b.NewT"}})
}

// Row 9: a private type used in a generic body. The module's readers of
// the type; importers' functions are not rechecked (the private type is in
// neither b's API nor its shape part), only their instances recompile.
func TestInvalidation_PrivateTypeInGeneric(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("    pv: u32\n", "    pv: u64\n")),
		missed: []string{"b"}, checked: []string{"b.K", "b.Keep"}})
}

// Row 10: an exported signature. The functions that read it.
func TestInvalidation_ExportedSignature(t *testing.T) {
	runInvRow(t, invRow{edits: map[string]func(string) string{
		"b/b.mk": replace("export G(x: int): int {\n    return x * 3", "export G(x: int, y: int): int {\n    return x * y"),
		"a/a.mk": replace("b.G(2)", "b.G(2, 3)"),
	}, missed: []string{"a", "b"}, checked: []string{"a.Q", "b.G", "b.K"}})
}

// Row 11: an exported item added. The functions with a negative read of
// its name.
func TestInvalidation_AddExportedItem(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", func(s string) string { return s + "\nexport H(): int {\n    return 0\n}\n" }),
		missed: []string{"a", "b"}, checked: []string{"b.H", "b.K"}})
}

// Row 12: an impl added. The functions whose impl reads it changes
// (implreads.go): here none reads Show for T, so only the impl's own
// method and the const b.K are checked (TestFuncReuseImplReads has a
// reader that keeps its result).
func TestInvalidation_ImplAdded(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", func(s string) string {
		return s + "\nimpl Show for T {\n    Show(&self): int {\n        return 1\n    }\n}\n"
	}), missed: []string{"a", "b"}, checked: []string{"b#impl(b.Show for b.T):Show", "b.K"}})
}

// Row 13: a const value changed. Readers of the value.
func TestInvalidation_ConstValueChanged(t *testing.T) {
	runInvRow(t, invRow{edits: edit("b/b.mk", replace("export const K: int = 7", "export const K: int = 8")),
		missed: []string{"a", "b"}, checked: []string{"a.Q", "b.K"}})
}

// Row 14: pkg.mk. Whatever read the changed field: unsafe_modules is read
// by cc's context; the package name by nothing.
func TestInvalidation_PkgMk(t *testing.T) {
	t.Run("ReadField", func(t *testing.T) {
		runInvRow(t, invRow{edits: edit("pkg.mk", replace(`unsafe_modules: [],`, `unsafe_modules: ["cc"],`)),
			missed: []string{"cc"}, checked: []string{"cc.MakeU"}})
	})
	t.Run("UnreadField", func(t *testing.T) {
		runInvRow(t, invRow{edits: edit("pkg.mk", replace(`name: "p",`, `name: "q",`))})
	})
}

// Row 15: another compiler. Everything.
func TestInvalidation_CompilerVersion(t *testing.T) {
	runInvRow(t, invRow{cfg: func(c *Config) { c.BuildID = source.HashString("test", "another compiler") },
		missed: []string{"a", "b", "cc", "g"}, checked: []string{"a.P", "a.Q", "a.R", "b.F", "b.G", "b.K", "b.Keep",
			"b.NewT", "b.UseU", "b.Wrap", "cc.MakeU", "g.Helper", "g.N"}})
}

// constFoldSrc is a program whose Pick folds a same-module const in an if
// condition (check's foldCond lowers only the taken branch).
const constFoldSrc = "const Flag: bool = false\n\n" +
	"Pick(): int {\n    if Flag {\n        return 1\n    }\n    return 0\n}\n\n" +
	"export Main(sys: &System): WriteError!void {\n    n := Pick()\n    var out := sys.Stdout()\n" +
	"    try Print(mut out, \"{n}\\n\", (n))\n}\n"

// Row 13, same module: a const's initializer changes and a function of
// the same module folded it into its body. The function is rechecked
// (funccache.go sameModuleConstValues); reused, it would keep the old
// branch, and a build from MK_CACHE, dev or release, would run it.
func TestInvalidation_SameModuleConstFolded(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "flag.mk")
	if err := os.WriteFile(file, []byte(constFoldSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: file, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
	if cold := frontCheck(t, cfg); len(cold.Report.Diags) > 0 {
		t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
	}
	flip := strings.Replace(constFoldSrc, "Flag: bool = false", "Flag: bool = true", 1)
	if err := os.WriteFile(file, []byte(flip), 0o644); err != nil {
		t.Fatal(err)
	}
	warm := frontCheck(t, cfg)
	if got := pkgItems(warm.Trace.ItemsChecked); !slices.Contains(got, "main.Pick") {
		t.Errorf("rechecked %q, want main.Pick among them (from cache %q)", got, pkgItems(warm.Trace.ItemsFromCache))
	}
	if third := frontCheck(t, cfg); len(third.Trace.ItemsChecked) > 0 {
		t.Errorf("third check rechecked %q", pkgItems(third.Trace.ItemsChecked))
	}
}

// The same edit end to end: dev and release builds from one cache run the
// branch of the current value.
func TestInvalidation_SameModuleConstFoldedNative(t *testing.T) {
	tools := needTools(t)
	target := HostTarget()
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "flag.mk")
			cache := t.TempDir()
			for i, c := range []struct{ src, want string }{
				{constFoldSrc, "0\n"},
				{strings.Replace(constFoldSrc, "Flag: bool = false", "Flag: bool = true", 1), "1\n"},
				{constFoldSrc, "0\n"},
			} {
				if err := os.WriteFile(file, []byte(c.src), 0o644); err != nil {
					t.Fatal(err)
				}
				s, err := NewSession(Config{Root: file, Target: target, Profile: prof, Tools: tools, CacheDir: cache})
				if err != nil {
					t.Fatal(err)
				}
				out := filepath.Join(dir, fmt.Sprintf("flag%d%s", i, exeSuffix(target)))
				r, paths, err := s.Build(out)
				if err != nil {
					t.Fatal(err)
				}
				if len(r.Report.Diags) > 0 || len(paths) != 1 {
					t.Fatalf("build %d:\n%s", i, r.Report.Text(r.Sources))
				}
				if got, _, _ := runExe(t, target, paths[0], ""); got != c.want {
					t.Errorf("build %d prints %q, want %q", i, got, c.want)
				}
			}
		})
	}
}

// A function naming a same-module const is still reused when another
// function of its module changes (the value read validates against the
// const's value, which the module's always-checked const has by then).
func TestInvalidation_SameModuleConstReaderReused(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "flag.mk")
	src := constFoldSrc + "\nOther(): int {\n    return 2\n}\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: file, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
	if cold := frontCheck(t, cfg); len(cold.Report.Diags) > 0 {
		t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
	}
	if err := os.WriteFile(file, []byte(strings.Replace(src, "return 2", "return 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // the reuse must not depend on timing
		warm := frontCheck(t, cfg)
		if got := pkgItems(warm.Trace.ItemsChecked); slices.Contains(got, "main.Pick") {
			t.Fatalf("run %d rechecked %q: main.Pick should be reused", i, got)
		}
		cfg.CacheDir = t.TempDir()
		if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		frontCheck(t, cfg)
		if err := os.WriteFile(file, []byte(strings.Replace(src, "return 2", "return 3", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The const part is transitive: Pick folds Flag, whose initializer names
// Base; an edit of Base alone rechecks Pick.
func TestInvalidation_SameModuleConstTransitive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "flag.mk")
	src := strings.Replace(constFoldSrc, "const Flag: bool = false", "const Base: bool = false\n\nconst Flag: bool = Base", 1)
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Root: file, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
	if cold := frontCheck(t, cfg); len(cold.Report.Diags) > 0 {
		t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
	}
	if err := os.WriteFile(file, []byte(strings.Replace(src, "Base: bool = false", "Base: bool = true", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pkgItems(frontCheck(t, cfg).Trace.ItemsChecked); !slices.Contains(got, "main.Pick") {
		t.Errorf("rechecked %q, want main.Pick among them", got)
	}
}

// TestInvalidation_SameModuleClauseFolded: Pick folds the requires clause of
// a same-module callee, and the clause names a const of another file of the
// module. Each edit of that const is checked twice, warm from the cache of
// the previous steps and cold into a fresh cache, and both must give the
// same verdict; the warm check of a text no step had before rechecks a.Pick,
// and a check with no edit rechecks nothing. A stale warm check of Max = 0
// would be clean where the cold one is E0706.
func TestInvalidation_SameModuleClauseFolded(t *testing.T) {
	const g = "G(): int\n    requires Max > 0\n{\n    return 1\n}\n\n"
	for _, c := range []struct {
		name, main, file string
		vals             []string // the edited file's text per step
		bad              []bool   // E0706 at the step
	}{
		{"call", g + "Pick(): int {\n    return G()\n}\n", "a/max.mk",
			[]string{"const Max: int = 1\n", "const Max: int = 0\n", "const Max: int = 1\n", "const Max: int = -100\n"},
			[]bool{false, true, false, true}},
		{"method", "struct S {\n    a: int\n}\n\nS.G(self): int\n    requires Max > 0\n{\n    return self.a\n}\n\n" +
			"Pick(): int {\n    s := S(a: 1)\n    return s.G()\n}\n", "a/max.mk",
			[]string{"const Max: int = 1\n", "const Max: int = 0\n", "const Max: int = 1\n"},
			[]bool{false, true, false}},
		{"value", g + "Pick(): int {\n    f := G\n    return f()\n}\n", "a/max.mk",
			[]string{"const Max: int = 1\n", "const Max: int = 0\n", "const Max: int = 1\n"},
			[]bool{false, true, false}},
		{"generic", "G2[const N: int](): int\n    requires N < Max\n{\n    return N\n}\n\n" +
			"Pick(): int {\n    return G2[3]()\n}\n", "a/max.mk",
			[]string{"const Max: int = 5\n", "const Max: int = 3\n", "const Max: int = 4\n"},
			[]bool{false, true, false}},
		{"transitive", g + "Pick(): int {\n    return G()\n}\n\nconst Max: int = Base\n", "a/base.mk",
			[]string{"const Base: int = 1\n", "const Base: int = 0\n", "const Base: int = 1\n"},
			[]bool{false, true, false}},
		// A closed clause judged inside a generic: folded at the same
		// module, as at a concrete call.
		{"in a generic", "G2[T, const N: int](x: &T): int\n    requires N >= Min\n{\n    return N\n}\n\n" +
			"Pick[T](x: &T): int {\n    return G2[T, 0](x)\n}\n", "a/min.mk",
			[]string{"const Min: int = 0\n", "const Min: int = 1\n", "const Min: int = 0\n"},
			[]bool{false, true, false}},
		// An enum const argument and a clause over it:
		// the value is built from the layout and the clause folded.
		{"enum value", "GD[const D: Mode](): int\n    requires D == Mode.B\n{\n    return 1\n}\n\n" +
			"Pick(): int {\n    return GD[M]()\n}\n", "a/mode.mk",
			[]string{"enum Mode {\n    A\n    B\n}\n\nconst M: Mode = Mode.B\n", "enum Mode {\n    A\n    B\n}\n\nconst M: Mode = Mode.A\n",
				"enum Mode {\n    A\n    B\n}\n\nconst M: Mode = Mode.B\n"},
			[]bool{false, true, false}},
		// The variants' names swapped and M's bytes kept: the clause's
		// variant is read by name.
		{"enum names swapped", "GD[const D: Mode](): int\n    requires D == Mode.B\n{\n    return 1\n}\n\n" +
			"Pick(): int {\n    return GD[M]()\n}\n", "a/mode.mk",
			[]string{"enum Mode {\n    A\n    B\n}\n\nconst M: Mode = Mode.A\n", "enum Mode {\n    B\n    A\n}\n\nconst M: Mode = Mode.B\n",
				"enum Mode {\n    A\n    B\n}\n\nconst M: Mode = Mode.A\n"},
			[]bool{true, false, true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := writeTree(t, map[string]string{"pkg.mk": manifest(nil), "a/main.mk": c.main, c.file: c.vals[0]})
			cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
			for i, v := range c.vals {
				if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(c.file)), []byte(v), 0o644); err != nil {
					t.Fatal(err)
				}
				warm := frontCheck(t, cfg)
				coldCfg := cfg
				coldCfg.CacheDir = t.TempDir()
				cold := frontCheck(t, coldCfg)
				wc, cc := codes(warm), codes(cold)
				if !slices.Equal(wc, cc) {
					t.Errorf("step %d (%q): warm %q, cold %q\nwarm:\n%s", i, v, wc, cc, warm.Report.Text(warm.Sources))
				}
				if want := c.bad[i]; slices.Contains(cc, "E0706") != want || len(cc) > 1 {
					t.Errorf("step %d (%q): cold %q, want E0706 %v alone", i, v, cc, want)
				}
				// A text an earlier step had may reuse that step's entry,
				// whose inputs it has exactly.
				if i > 0 && !slices.Contains(c.vals[:i], v) && !slices.Contains(pkgItems(warm.Trace.ItemsChecked), "a.Pick") {
					t.Errorf("step %d: rechecked %q, want a.Pick among them", i, pkgItems(warm.Trace.ItemsChecked))
				}
			}
			if again := frontCheck(t, cfg); len(again.Trace.ItemsChecked) > 0 {
				t.Errorf("check with no edit rechecked %q", pkgItems(again.Trace.ItemsChecked))
			}
		})
	}
}

// TestInvalidation_SameModuleClauseVerify: with the front-end output
// released after each module (ReleaseFront, as mk check, build and run do),
// the clause part of a function prefix is the same at lookup and at store. A
// verify check, the first on its cache, must then recompute every stored
// entry as it is, and an edit of the clause's const must give the same
// verdict warm and cold. A store run after the ASTs were dropped must not
// take the no-AST fallback, which made the verify check report "cache verify
// ... the stored interface differs".
func TestInvalidation_SameModuleClauseVerify(t *testing.T) {
	main := "G(): int\n    requires Max > 0\n{\n    return 1\n}\n\nPick(): int {\n    return G()\n}\n"
	dir := writeTree(t, map[string]string{"pkg.mk": manifest(nil), "a/main.mk": main, "a/max.mk": "const Max: int = 1\n"})
	cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk"), ReleaseFront: true}
	if cold := frontCheck(t, cfg); len(cold.Report.Diags) > 0 {
		t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
	}
	vcfg := cfg
	vcfg.CacheVerify = true
	if v := frontCheck(t, vcfg); len(v.Report.Diags) > 0 {
		t.Fatalf("verify check:\n%s", v.Report.Text(v.Sources))
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "max.mk"), []byte("const Max: int = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warm := frontCheck(t, cfg)
	coldCfg := cfg
	coldCfg.CacheDir = t.TempDir()
	cold := frontCheck(t, coldCfg)
	if wc, cc := codes(warm), codes(cold); !slices.Equal(wc, cc) || !slices.Equal(cc, []string{"E0706"}) {
		t.Errorf("Max = 0: warm %q, cold %q, want E0706 alone on both", wc, cc)
	}
	if v := frontCheck(t, vcfg); !slices.Equal(codes(v), []string{"E0706"}) {
		t.Errorf("verify after the edit: %q, want E0706 alone\n%s", codes(v), v.Report.Text(v.Sources))
	}
}

// The clause part is no wider than the clauses: a const that no clause of
// a callee names, edited in the same file, leaves Pick reused, and so does
// a const named only in the callee's body.
func TestInvalidation_SameModuleClauseReaderReused(t *testing.T) {
	main := "G(): int\n    requires Max > 0\n{\n    return Body\n}\n\nPick(): int {\n    return G()\n}\n"
	consts := "const Max: int = 1\n\nconst Other: int = 1\n\nconst Body: int = 1\n"
	for _, ed := range []string{"Other: int = 2", "Body: int = 2"} {
		t.Run(ed, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"pkg.mk": manifest(nil), "a/main.mk": main, "a/max.mk": consts})
			cfg := Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows, BuildID: source.HashString("test", "mk")}
			if cold := frontCheck(t, cfg); len(cold.Report.Diags) > 0 {
				t.Fatalf("cold check:\n%s", cold.Report.Text(cold.Sources))
			}
			old := strings.SplitN(ed, " = ", 2)[0] + " = 1"
			if err := os.WriteFile(filepath.Join(dir, "a", "max.mk"), []byte(strings.Replace(consts, old, ed, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			warm := frontCheck(t, cfg)
			if len(warm.Report.Diags) > 0 {
				t.Fatalf("warm check:\n%s", warm.Report.Text(warm.Sources))
			}
			if got := pkgItems(warm.Trace.ItemsChecked); slices.Contains(got, "a.Pick") {
				t.Errorf("rechecked %q: a.Pick should be reused", got)
			}
		})
	}
}
