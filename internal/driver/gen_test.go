// Why: gen lines run generators inside the driver; what they emit must
// check, be visible to importers and keep gen's diagnostics.
// What: TestGenExportUsedByImporter, TestGenDeriveEqChecks,
// TestE0704_GeneratedImplAtGenLine, TestGenCodes (E0903, E0905, E0906),
// TestGenFailRootOfUnknownNames, TestGenPoisonRootOfUnknownNames,
// TestGenDeriveEqGolden, TestGenBoolArgument and TestGenFlowDiagWrapped.
// How: checks small packages with the real std generators and compares
// diagnostics, and derive.Eq's output with internal/gen's golden.

package driver

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// libManifest is a package with no bins: every directory is a module.
func libManifest() string { return manifest(nil) }

// TestGenExportUsedByImporter: g holds a generator that emits an
// exported function, a imports g and has the gen line, b imports a and calls
// a.Helper(). mk check is clean, with no E0201 in b.
func TestGenExportUsedByImporter(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk": libManifest(),
		"g/g.mk": "export Make(g: mut Gen) {\n    g.Emit(quote {\n        export Helper(): int {\n            return 1\n        }\n    })\n}\n",
		"a/a.mk": "import g\n\ngen g.Make()\n",
		"b/b.mk": "import a\n\nexport Use(): int {\n    return a.Helper() + 1\n}\n",
	})
	r := check(t, Config{Root: dir})
	clean(t, r)
}

// TestGenDeriveEqChecks: std's derive.Eq over a struct of another module's
// importer, used through the generated impl; with the module cache on, a
// warm check equals the cold one, and the module with gen output is stored
// and hits with its generated file (GenRef, genref.go).
func TestGenDeriveEqChecks(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk":   libManifest(),
		"lib/l.mk": "import std.derive\n\nexport struct User {\n    export name: str\n    export age: int\n}\n\ngen derive.Eq(User)\n",
		"app/a.mk": "import lib\n\nexport Same(x: &lib.User, y: &lib.User): bool {\n    return x.Eq(y)\n}\n",
	})
	cfg := Config{Root: dir, CacheDir: t.TempDir()}
	cold := frontCheck(t, cfg)
	clean(t, cold)
	if slices.ContainsFunc(cold.Trace.NotStored, func(s string) bool { return strings.HasPrefix(s, "lib") }) {
		t.Errorf("not stored %q, want lib stored", cold.Trace.NotStored)
	}
	warm := frontCheck(t, cfg)
	clean(t, warm)
	if string(jsonOf(t, warm)) != string(jsonOf(t, cold)) {
		t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
	}
	if !slices.Contains(warm.Trace.ModulesFromCache, "lib") {
		t.Errorf("lib did not come from the cache: %q", warm.Trace.ModulesFromCache)
	}
}

// TestE0704_GeneratedImplAtGenLine: two gen lines emitting one
// impl give one E0704 at the second gen line, not an E0905, citing the
// first gen line and the first impl's place in the output.
func TestE0704_GeneratedImplAtGenLine(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk":   libManifest(),
		"lib/l.mk": "import std.derive\n\nexport struct User {\n    export name: str\n}\n\ngen derive.Eq(User)\n\ngen derive.Eq(User)\n",
	})
	r := check(t, Config{Root: dir})
	expectLines(t, r, "lib/l.mk:9:1:E0704")
	if len(r.Report.Diags) == 1 {
		d := r.Report.Diags[0].Diag
		at := func(key string) string {
			f, ok := d.FactValue(key)
			if !ok {
				return ""
			}
			return diag.Location(r.Sources, f.At)
		}
		if got := at("first"); got != "lib/l.mk:7:1" {
			t.Errorf("first = %q, want lib/l.mk:7:1", got)
		}
		if got := at("first_output"); !strings.HasPrefix(got, "lib/gen/") {
			t.Errorf("first_output = %q, want a place in lib's gen output", got)
		}
	}
}

// TestGenCodes: the gen conformance units' verdicts (tests/conformance/codes
// E0903, E0905, E0906), as packages.
func TestGenCodes(t *testing.T) {
	for _, c := range []struct {
		name, gens, app string
		want            string
	}{
		{"E0903", "export Refuse(g: mut Gen, t: Type) {\n    g.Fail(\"Refuse accepts no type\")\n}\n",
			"import gens\n\nstruct Point {\n    x: i32\n}\n\ngen gens.Refuse(Point)\n", "app/a.mk:7:1:E0903"},
		{"E0905", "export Broken(g: mut Gen, t: Type) {\n    g.Emit(quote {\n        $(t.Name()).Zero(): i32 {\n            return \"zero\"\n        }\n    })\n}\n",
			"import gens\n\nstruct Point {\n    x: i32\n}\n\ngen gens.Broken(Point)\n", "app/a.mk:7:1:E0905"},
		{"E0906", "export Make(g: mut Gen) {\n    g.Emit(quote {\n        struct Made {\n            n: int\n        }\n    })\n}\n\nexport Look(g: mut Gen, t: Type) {\n}\n",
			"import gens\n\ngen gens.Make()\n\ngen gens.Look(Made)\n", "app/a.mk:5:15:E0906"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"pkg.mk": libManifest(), "gens/g.mk": c.gens, "app/a.mk": c.app})
			r := check(t, Config{Root: dir})
			expectLines(t, r, c.want)
		})
	}
}

// TestGenFailRootOfUnknownNames: a gen line whose generator fails emits
// nothing, so what names its output gets E0201 (names): a file of its module,
// also when another module exports the spelling and the E0201 offers it; an
// importer's `out.Made`; and, as an E0905, another gen line's output. The
// E0903 (types) is reported with them, as the root of each, and an E0201
// cites the gen line. A member miss on a module whose lines all ran is not
// linked, nor is a label, which no gen line declares. With the module cache
// on, the warm report equals the cold one.
func TestGenFailRootOfUnknownNames(t *testing.T) {
	const gn = "export Make(g: mut Gen) {\n    g.Fail(\"planted\")\n    made := g.Ident(\"Made\")\n" +
		"    g.Emit(quote {\n        export const $made: int = 1\n    })\n}\n\n" +
		"export Use(g: mut Gen) {\n    g.Emit(quote {\n        export UseMade(): int {\n            return Made + 1\n        }\n    })\n}\n"
	const other = "export const Made: int = 2\n"
	for _, c := range []struct {
		name   string
		files  map[string]string
		want   []string
		linked bool
	}{
		{"same module", map[string]string{"out/use.mk": "export UseMade(): int {\n    return Made + 1\n}\n"},
			[]string{"out/out.mk:3:1:E0903", "out/use.mk:2:12:E0201"}, true},
		{"importer", map[string]string{"use/use.mk": "import out\n\nexport UseMade(): int {\n    return out.Made + 1\n}\n"},
			[]string{"out/out.mk:3:1:E0903", "use/use.mk:4:16:E0201"}, true},
		{"spelling exported elsewhere", map[string]string{"other/other.mk": other,
			"out/use.mk": "export UseMade(): int {\n    return Made + 1\n}\n"},
			[]string{"out/out.mk:3:1:E0903", "out/use.mk:2:12:E0201"}, true},
		{"spelling exported by an import", map[string]string{"other/other.mk": other,
			"out/use.mk": "import other\n\nexport UseMade(): int {\n    return Made + other.Made\n}\n"},
			[]string{"out/out.mk:3:1:E0903", "out/use.mk:4:12:E0201"}, true},
		{"another gen line", map[string]string{"out/out.mk": "import gn\n\ngen gn.Make()\n\ngen gn.Use()\n"},
			[]string{"out/out.mk:3:1:E0903", "out/out.mk:5:1:E0905"}, true},
		{"member miss elsewhere", map[string]string{"other/other.mk": other,
			"out/use.mk": "import other\n\nexport UseTypo(): int {\n    return other.Typo\n}\n"},
			[]string{"out/use.mk:4:18:E0201"}, false},
		{"local name", map[string]string{"out/use.mk": "export UseMade(): int {\n    outer: for i in 0..3 {\n" +
			"        if i == 1 {\n            break outr\n        }\n    }\n    return 1\n}\n"},
			[]string{"out/use.mk:4:19:E0201"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"pkg.mk": libManifest(), "gn/gn.mk": gn, "out/out.mk": "import gn\n\ngen gn.Make()\n"}
			for p, text := range c.files {
				files[p] = text
			}
			cfg := Config{Root: writeTree(t, files), CacheDir: t.TempDir()}
			cold := frontCheck(t, cfg)
			expectLines(t, cold, c.want...)
			for i, e := range cold.Report.Diags {
				root := i + 1
				if c.linked {
					root = 1
				}
				gl, cites := e.Diag.FactValue("gen_line")
				if e.Root != root || cites != (c.linked && e.Diag.Code == diag.E0201) ||
					(cites && diag.Location(cold.Sources, gl.At) != "out/out.mk:3:1") {
					t.Errorf("%v: root %d, gen_line %v; want root %d, and gen_line out/out.mk:3:1 on a linked E0201\n%s",
						e.Diag.Code, e.Root, cites, root, cold.Report.Text(cold.Sources))
				}
			}
			if warm := frontCheck(t, cfg); string(jsonOf(t, warm)) != string(jsonOf(t, cold)) {
				t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
			}
		})
	}
}

// TestGenPoisonRootOfUnknownNames: a gen line emits nothing because a
// module it depends on failed to check: the generator's own module, a module
// the generator imports, a module whose function the generator calls, the
// module of a type argument or of the fuel const, or a borrow error (stage
// 7) in the generator's module; or an error before types (E0206) in the
// generator's module, its import or a function argument's module, which
// leaves the generator or the function with no declaration (MKH-476: never
// an E0301 at the line). That module's first error, of a later
// phase, is reported with the E0201s on the names the output would have
// declared (in the module, or an importer's `out.Made`), as their root, and
// each E0201 cites the gen line and names the module. A borrow error, which
// leaves its module clean, is the root only when no module the line reads
// has another error. A second error of the module waits for the first to
// be fixed; a poisoned line with no failed module keeps the old note with
// no root. With the module cache on, the warm report equals the cold one.
func TestGenPoisonRootOfUnknownNames(t *testing.T) {
	const emit = "    made := g.Ident(\"Made\")\n    g.Emit(quote {\n        export const $made: int = 1\n    })\n}\n"
	const use = "export UseMade(): int {\n    return Made + 1\n}\n"
	// reflect reads its argument's method results: a run that meets one
	// that failed to check, or one the stage-4 view cannot translate,
	// ends in poison.
	const reflect = "export Make(g: mut Gen, t: Type) {\n    for m in t.Methods() {\n        _ = m.Result()\n    }\n"
	const flow = "export Twice() {\n    var n := 1\n    a := mut n\n    b := mut n\n    *a += 1\n    *b += 1\n}\n"
	// twice declares F twice: E0206 at its line 5, an error before types.
	const twice = "export F(): int {\n    return 1\n}\n\nF(): int {\n    return 2\n}\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  []string
		root  string // the E0201's root, "" for none
		in    string // the failed module the note names
		at    string // the gen line the E0201 cites, when not out/out.mk's first
	}{
		{"own module", map[string]string{"gn/gn.mk": "export Make(g: mut Gen) {\n    unused := 1\n" + emit},
			[]string{"gn/gn.mk:2:5:E0208", "out/use.mk:2:12:E0201"}, "gn/gn.mk:2:5", "gn", ""},
		{"generator's import", map[string]string{"gn/gn.mk": "import a\n\nexport Make(g: mut Gen) {\n    _ = a.One()\n" + emit,
			"a/a.mk": "import std.math\n\nexport One(): int {\n    return 1\n}\n"},
			[]string{"a/a.mk:1:1:E0208", "out/use.mk:2:12:E0201"}, "a/a.mk:1:1", "a", ""},
		{"type argument's module", map[string]string{"gn/gn.mk": reflect + emit,
			"a/a.mk":     "export interface Marker {\n    Get(&self): [1 + true]u8\n}\n",
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make(a.Marker)\n"},
			[]string{"a/a.mk:2:22:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:2:22", "a", ""},
		{"fuel const's module", map[string]string{"gn/gn.mk": "export Make(g: mut Gen) {\n" + emit,
			"a/a.mk":     "export const F: u64 = 1 + true\n",
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make() fuel a.F\n"},
			[]string{"a/a.mk:1:27:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:1:27", "a", ""},
		{"type error in a table read", map[string]string{"gn/gn.mk": "import tbl\n\nexport Make(g: mut Gen) {\n" +
			"    made := g.Ident(\"Made\")\n    g.Emit(quote {\n        export const $made: int = $(tbl.RowOf(1))\n    })\n}\n",
			"tbl/tbl.mk": "Mark(r: int): int {\n    return r\n}\n\nexport RowOf(k: int): int {\n    return Mark(true) + k\n}\n"},
			[]string{"out/use.mk:2:12:E0201", "tbl/tbl.mk:6:17:E0301"}, "tbl/tbl.mk:6:17", "tbl", ""},
		{"importer", map[string]string{"gn/gn.mk": "export Make(g: mut Gen) {\n    unused := 1\n" + emit,
			"use/use.mk": "import out\n\nexport UseMade(): int {\n    return out.Made + 1\n}\n"},
			[]string{"gn/gn.mk:2:5:E0208", "use/use.mk:4:16:E0201"}, "gn/gn.mk:2:5", "gn", ""},
		{"two errors", map[string]string{"gn/gn.mk": "export Make(g: mut Gen) {\n    unused := 1\n    unused2 := 2\n" + emit},
			[]string{"gn/gn.mk:2:5:E0208", "out/use.mk:2:12:E0201"}, "gn/gn.mk:2:5", "gn", ""},
		{"flow error", map[string]string{"gn/gn.mk": flow + "\nexport Make(g: mut Gen) {\n    Twice()\n" + emit},
			[]string{"gn/gn.mk:4:5:E0501", "out/use.mk:2:12:E0201"}, "gn/gn.mk:4:5", "gn", ""},
		// A borrow error leaves its module clean: the failed module is the
		// root, whatever the walk meets first.
		{"borrow error in a module before the failed one", map[string]string{"gn/gn.mk": "import a\nimport z\n\n" +
			"export Two(): int {\n    return a.One() + 1\n}\n\nexport Make(g: mut Gen) {\n    _ = z.Z()\n" + emit,
			"a/a.mk": "export One(): int {\n    return 1\n}\n\n" + flow, "z/z.mk": "export Z(): int {\n    return true\n}\n"},
			[]string{"out/use.mk:2:12:E0201", "z/z.mk:2:12:E0301"}, "z/z.mk:2:12", "z", ""},
		{"borrow error before the failed module's own", map[string]string{"gn/gn.mk": "import a\n\nexport Make(g: mut Gen) {\n    _ = a.One()\n" + emit,
			"a/a.mk": flow + "\nexport One(): int {\n    return true\n}\n"},
			[]string{"a/a.mk:10:12:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:10:12", "a", ""},
		{"borrow error below the generator, failed type argument", map[string]string{
			"gn/gn.mk":   "import b\n\nexport Two(): int {\n    return b.One() + 1\n}\n\n" + reflect + emit,
			"b/b.mk":     "export One(): int {\n    return 1\n}\n\n" + flow,
			"a/a.mk":     "export interface Marker {\n    Get(&self): [1 + true]u8\n}\n",
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make(a.Marker)\n"},
			[]string{"a/a.mk:2:22:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:2:22", "a", ""},
		{"type read from the generator's module", map[string]string{"gn/gn.mk": "import a\n\nexport struct Marker {\n    export n: int\n}\n\n" +
			"export Two(): int {\n    return a.One() + 1\n}\n\nexport Make(g: mut Gen, t: Type) {\n    _ = t\n" + emit,
			"a/a.mk":     "import std.math\n\nexport One(): int {\n    return 1\n}\n",
			"out/out.mk": "import gn\n\ngen gn.Make(gn.Marker)\n"},
			[]string{"a/a.mk:1:1:E0208", "out/use.mk:2:12:E0201"}, "a/a.mk:1:1", "a", ""},
		{"struct argument", map[string]string{"gn/gn.mk": reflect + emit,
			"a/a.mk":     "export struct S {\n    export n: int\n}\n\nexport S.Get(&self): [1 + true]u8 {\n    return [0; 2]\n}\n",
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make(a.S)\n"},
			[]string{"a/a.mk:5:27:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:5:27", "a", ""},
		{"element of a slice argument", map[string]string{"gn/gn.mk": strings.Replace(reflect, "t.Methods()", "t.Elem().Methods()", 1) + emit,
			"a/a.mk":     "export struct S {\n    export n: int\n}\n\nexport S.Get(&self): [1 + true]u8 {\n    return [0; 2]\n}\n",
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make([]a.S)\n"},
			[]string{"a/a.mk:5:27:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:5:27", "a", ""},
		{"errors in two files and stages", map[string]string{"gn/gn.mk": "import a\n\nexport Make(g: mut Gen) {\n    _ = a.One()\n" + emit,
			"a/a.mk": "export One(): int {\n    return true\n}\n", "a/b.mk": "export Two(x: [1 + true]u8): int {\n    return 2\n}\n"},
			[]string{"a/a.mk:2:12:E0301", "out/use.mk:2:12:E0201"}, "a/a.mk:2:12", "a", ""},
		{"error in the read module's gen output", map[string]string{"gn/gn.mk": "import a\n\nexport Make(g: mut Gen) {\n    _ = a.One()\n" + emit,
			"g2/g2.mk": "export Bad(g: mut Gen) {\n    g.Emit(quote {\n        export Wrong(): int {\n            return true\n        }\n    })\n}\n",
			"a/a.mk":   "import g2\n\ngen g2.Bad()\n\nexport One(): int {\n    return 1\n}\n"},
			[]string{"a/a.mk:3:1:E0905", "out/use.mk:2:12:E0201"}, "a/a.mk:3:1", "a", ""},
		{"a later line with a root", map[string]string{"gn/gn.mk": reflect + emit,
			"g2/g2.mk":   "export Make(g: mut Gen) {\n    unused := 1\n    made := g.Ident(\"Made2\")\n    g.Emit(quote {\n        export const $made: int = 1\n    })\n}\n",
			"out/out.mk": "import g2\nimport gn\n\ninterface Local {\n    Get(&self): [2 * 2]u8\n}\n\ngen gn.Make(Local)\n\ngen g2.Make()\n",
			"out/use.mk": "export UseMade(): int {\n    return Made2 + 1\n}\n"},
			[]string{"g2/g2.mk:2:5:E0208", "out/use.mk:2:12:E0201"}, "g2/g2.mk:2:5", "g2", "out/out.mk:10:1"},
		// An error before types (E0206) leaves its module and its importers
		// unchecked, so the generator or a function argument has no
		// declaration (MKH-476): the line is poison, never an E0301.
		{"declaration error in the generator's module", map[string]string{"gn/gn.mk": "export Make(g: mut Gen) {\n" + emit + "\n" + twice},
			[]string{"gn/gn.mk:12:1:E0206", "out/use.mk:2:12:E0201"}, "gn/gn.mk:12:1", "gn", ""},
		{"declaration error in the generator's import", map[string]string{"gn/gn.mk": "import a\n\nexport Make(g: mut Gen) {\n    _ = a.F()\n" + emit,
			"a/a.mk": twice},
			[]string{"a/a.mk:5:1:E0206", "out/use.mk:2:12:E0201"}, "a/a.mk:5:1", "a", ""},
		{"declaration error in a function argument's module", map[string]string{"gn/gn.mk": "export Make(g: mut Gen, m: Method) {\n    _ = m\n" + emit,
			"a/a.mk":     "export G(x: int): int {\n    return x\n}\n" + twice,
			"out/out.mk": "import a\nimport gn\n\ngen gn.Make(a.G)\n"},
			[]string{"a/a.mk:8:1:E0206", "out/use.mk:2:12:E0201"}, "a/a.mk:8:1", "a", ""},
		{"own type", map[string]string{"gn/gn.mk": reflect + emit,
			"out/out.mk": "import gn\n\ninterface Local {\n    Get(&self): [2 * 2]u8\n}\n\ngen gn.Make(Local)\n"},
			[]string{"out/use.mk:2:12:E0201"}, "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"pkg.mk": libManifest(), "out/out.mk": "import gn\n\ngen gn.Make()\n", "out/use.mk": use}
			for p, text := range c.files {
				files[p] = text
			}
			if _, ok := c.files["use/use.mk"]; ok {
				delete(files, "out/use.mk")
			}
			cfg := Config{Root: writeTree(t, files), CacheDir: t.TempDir()}
			cold := frontCheck(t, cfg)
			expectLines(t, cold, c.want...)
			for i, e := range cold.Report.Diags {
				d := e.Diag
				if at := diag.Location(cold.Sources, d.At); d.Code != diag.E0201 || !strings.HasSuffix(strings.Split(at, ":")[0], "use.mk") {
					continue
				}
				g, _ := d.FactValue("gen")
				gl, cites := d.FactValue("gen_line")
				if want := cmp.Or(c.at, "out/out.mk:"); !cites || !strings.HasPrefix(diag.Location(cold.Sources, gl.At), want) {
					t.Errorf("E0201 cites a gen line: %v, want %s", cites, want)
				}
				if c.root == "" {
					if e.Root != i+1 || d.Cause != nil || !strings.Contains(g.Str, "cannot see before checking") {
						t.Errorf("E0201 root %d, cause %v, gen note %q; want its own root and the old note\n%s",
							e.Root, d.Cause, g.Str, cold.Report.Text(cold.Sources))
					}
					continue
				}
				r := cold.Report.Diags[e.Root-1].Diag
				if got := diag.Location(cold.Sources, r.At); got != c.root || !strings.Contains(g.Str, "`"+c.in+"`") {
					t.Errorf("E0201's root at %s, gen note %q; want root %s naming `%s`\n%s", got, g.Str, c.root, c.in,
						cold.Report.Text(cold.Sources))
				}
			}
			if warm := frontCheck(t, cfg); string(jsonOf(t, warm)) != string(jsonOf(t, cold)) {
				t.Errorf("warm --json %s, cold %s", jsonOf(t, warm), jsonOf(t, cold))
			}
		})
	}
}

// TestGenGeneratorOfSameModule: a gen line naming a function of its own
// module is E0908 at the line, the stage rule's, never an E0301 that calls
// the function not a function (its declaration is not built in stage 4).
func TestGenGeneratorOfSameModule(t *testing.T) {
	files := map[string]string{"pkg.mk": libManifest(), "out/gn.mk": "export Make(g: mut Gen) {\n    _ = g\n}\n",
		"out/out.mk": "gen Make()\n"}
	cold := frontCheck(t, Config{Root: writeTree(t, files), CacheDir: t.TempDir()})
	expectLines(t, cold, "out/out.mk:1:1:E0908")
}

// TestGenDeriveEqGolden (gen derive.Eq output reads like handwritten
// code): std's derive.Eq over struct User { name: str, age: int }
// prints internal/gen/testdata/derive_eq.golden, the text gen's own
// TestDeriveEqShape checks, through the driver and the real std
// generator. The golden's header names its test module main; this one is lib.
func TestGenDeriveEqGolden(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk":   libManifest(),
		"lib/l.mk": "import std.derive\n\nexport struct User {\n    export name: str\n    export age: int\n}\n\ngen derive.Eq(User)\n",
	})
	r := check(t, Config{Root: dir, Target: types.X86_64Windows})
	clean(t, r)
	f, ok := r.Sources.ByPath("lib/gen/x86_64-windows/gen.mk")
	if !ok {
		var paths []string
		for _, f := range r.Sources.Files() {
			paths = append(paths, f.Path)
		}
		t.Fatalf("no lib/gen/x86_64-windows/gen.mk in the source map: %q", paths)
	}
	raw, err := os.ReadFile(filepath.Join("..", "gen", "testdata", "derive_eq.golden"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.ReplaceAll(string(raw), "\r\n", "\n"), "gen lines of main ", "gen lines of lib ", 1)
	if f.Text != want {
		t.Errorf("derive.Eq output differs from derive_eq.golden:\n--- got\n%s\n--- want\n%s", f.Text, want)
	}
}

// TestGenBoolArgument: true and false as gen arguments are universe names,
// not BasicLits; genArg turns them into literals, so each line folds into a
// bool parameter instead of failing with E0301. The two lines
// emit different functions, and the importer uses both.
func TestGenBoolArgument(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk": libManifest(),
		"g/g.mk": "export Pick(g: mut Gen, on: bool) {\n    if on {\n        g.Emit(quote {\n            export On(): int {\n                return 1\n            }\n        })\n    } else {\n        g.Emit(quote {\n            export Off(): int {\n                return 0\n            }\n        })\n    }\n}\n",
		"a/a.mk": "import g\n\ngen g.Pick(true)\n\ngen g.Pick(false)\n",
		"b/b.mk": "import a\n\nexport Use(): int {\n    return a.On() + a.Off()\n}\n",
	})
	r := check(t, Config{Root: dir})
	clean(t, r)
}

// TestGenFlowDiagWrapped: a borrow error (stage 7) inside generated code is
// E0905 at the gen line, phase flow, with the E0501 as its inner (genWrap
// over fdiags).
func TestGenFlowDiagWrapped(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk": libManifest(),
		"g/g.mk": "export Bad(g: mut Gen) {\n    g.Emit(quote {\n        export Twice() {\n            var n := 1\n            a := mut n\n            b := mut n\n            *a += 1\n            *b += 1\n        }\n    })\n}\n",
		"a/a.mk": "import g\n\ngen g.Bad()\n",
	})
	r := check(t, Config{Root: dir})
	expectLines(t, r, "a/a.mk:3:1:E0905")
	if len(r.Report.Diags) != 1 {
		return
	}
	d := r.Report.Diags[0].Diag
	if d.Phase != diag.PhaseFlow {
		t.Errorf("phase %v, want flow", d.Phase)
	}
	in, ok := d.FactValue("inner")
	if !ok {
		t.Fatalf("no inner fact:\n%s", r.Report.Text(r.Sources))
	}
	if i := slices.Index(in.Keys, "code"); i < 0 || in.List[i].Code != diag.E0501 {
		t.Errorf("inner %+v, want code E0501", in)
	}
	if got := diag.Location(r.Sources, mustAt(t, d, "output")); !strings.HasPrefix(got, "a/gen/") {
		t.Errorf("output = %q, want a place in a's gen output", got)
	}
}

// mustAt is the location of a FactAt fact.
func mustAt(t *testing.T, d *diag.Diagnostic, key string) source.Span {
	t.Helper()
	f, ok := d.FactValue(key)
	if !ok {
		t.Fatalf("no %s fact", key)
	}
	return f.At
}
