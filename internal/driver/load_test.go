// Why: the driver decides which files form which modules, reads std's
// layout and the manifest, and selects files per target; each step's
// errors must be exact and its order deterministic.
// What: tests of loading (single-file and directory mode, file order,
// case-only names, file size, unreadable files), std's module table, the
// evaluated manifest and its faults, and target selection.
// How: writes small package trees, checks them, and compares diagnostics,
// the session's trace and the decoded Manifest.

package driver

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

func TestSingleFileModeReadsOneFile(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"one.mk":   "export F(): int {\n    return 1\n}\n",
		"other.mk": "this does not parse (\n",
	})
	r := check(t, Config{Root: filepath.Join(dir, "one.mk")})
	clean(t, r)
	// The trace also lists the standard library and generated system views.
	if got := pkgPaths(r.Trace.FilesParsed); len(got) != 1 || !strings.HasSuffix(got[0], "one.mk") {
		t.Errorf("FilesParsed = %q, want only one.mk", got)
	}
	if got := pkgModules(r.Trace.ModulesResolved); !slices.Equal(got, []string{"main"}) {
		t.Errorf("ModulesResolved = %q, want [main]", got)
	}
}

// pkgPaths keeps the package's source paths in order, excluding std and
// generated system view sources.
func pkgPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !isStdOrViewSource(p) {
			out = append(out, p)
		}
	}
	return out
}

func isStdOrViewSource(p string) bool {
	return strings.HasPrefix(p, "std/") || strings.HasPrefix(p, sysViewSourcePrefix)
}

// pkgModules drops std and generated system view modules from a list of paths.
func pkgModules(mods []string) []string {
	var out []string
	for _, m := range mods {
		if m != "std" && !strings.HasPrefix(m, "std.") && !strings.HasPrefix(m, sysViewPrefix) {
			out = append(out, m)
		}
	}
	return out
}

func TestDirectoryModeIsOneModule(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.mk":     "export Main(sys: &System) {\n    G()\n}\n",
		"b.mk":     "G() {\n}\n",
		"sub/c.mk": "this is not read (\n",
	})
	r := check(t, Config{Root: dir})
	clean(t, r)
	if got := pkgPaths(r.Trace.FilesParsed); !slices.Equal(got, []string{"a.mk", "b.mk"}) {
		t.Errorf("FilesParsed = %q", got)
	}
	if got := pkgModules(r.Trace.ModulesResolved); !slices.Equal(got, []string{"main"}) {
		t.Errorf("ModulesResolved = %q", got)
	}
}

func TestDirectoryModeIsABinRoot(t *testing.T) {
	dir := writeTree(t, map[string]string{"a.mk": "G() {\n}\n"})
	expectLines(t, check(t, Config{Root: dir}), "a.mk:1:1:E0201")
}

func TestFileOrderIsByteOrder(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk":    manifest(nil),
		"m/b.mk":    "export B(): int {\n    return 1\n}\n",
		"m/A.mk":    "export A(): int {\n    return 1\n}\n",
		"m/_c.mk":   "export C(): int {\n    return 1\n}\n",
		"Z/z.mk":    "export Z(): int {\n    return 1\n}\n",
		"a/a.mk":    "export Y(): int {\n    return 1\n}\n",
		".hid/h.mk": "hidden (\n",
		"gen/g.mk":  "generated view (\n",
	})
	s, err := NewSession(Config{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Check()
	clean(t, r)
	var paths []string // in FileID order, the order files were added
	for id := 1; id <= r.Sources.Len(); id++ {
		paths = append(paths, r.Sources.File(source.FileID(id)).Path)
	}
	// The source map also contains std and generated system view files.
	paths = pkgPaths(paths)
	want := []string{"pkg.mk", "Z/z.mk", "a/a.mk", "m/A.mk", "m/_c.mk", "m/b.mk"}
	if !slices.Equal(paths, want) {
		t.Errorf("file order %q, want %q", paths, want)
	}
}

func TestCaseOnlyFileNames(t *testing.T) {
	// From the listing alone: a memory tree holds both names even where the
	// file system folds case.
	ds := checkMemPackage(t, map[string]string{
		"pkg.mk":    manifest(nil),
		"m/Util.mk": "export A(): int {\n    return 1\n}\n",
		"m/util.mk": "export B(): int {\n    return 1\n}\n",
	})
	if len(ds.Diags) != 1 || ds.line(0) != "m/util.mk:1:1:E0206" {
		t.Fatalf("got %q", ds.lines())
	}
	d := ds.Diags[0].Diag
	if d.Phase != diag.PhaseImports || fact(d, "first") != "m/Util.mk" {
		t.Errorf("phase %v first %q", d.Phase, fact(d, "first"))
	}
}

func TestCaseOnlyModuleDirs(t *testing.T) {
	ds := checkMemPackage(t, map[string]string{
		"pkg.mk":    manifest(nil),
		"Util/a.mk": "export A(): int {\n    return 1\n}\n",
		"util/b.mk": "export B(): int {\n    return 1\n}\n",
	})
	if len(ds.Diags) != 1 || ds.line(0) != "util/b.mk:1:1:E0206" || fact(ds.Diags[0].Diag, "first") != "Util" {
		t.Fatalf("got %q", ds.lines())
	}
}

func TestE0101_FileTooLarge(t *testing.T) {
	dir := writeTree(t, map[string]string{"big.mk": strings.Repeat("// x\n", 100)})
	// A synthetic limit, so no 4 GiB file is needed. It applies to std's
	// files too, so std here is m1Std's, which holds no .mk file.
	r := check(t, Config{Root: filepath.Join(dir, "big.mk"), StdDir: m1Std(t), TestHooks: TestHooks{MaxFileSize: 64}})
	got := lines(r)
	if len(got) != 1 || !strings.HasSuffix(got[0], "big.mk:1:1:E0101") ||
		fact(r.Report.Diags[0].Diag, "reason") != "file-too-large" {
		t.Errorf("got %q\n%s", got, r.Report.Text(r.Sources))
	}
}

func TestE0001_UnreadableFileReported(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.mk": "export Main(sys: &int) {\n    F()\n}\n",
		"b.mk": "F() {\n}\n",
	})
	read := func(p string) ([]byte, error) {
		if filepath.Base(p) == "b.mk" {
			return nil, &os.PathError{Op: "open", Path: p, Err: errors.New("access denied")}
		}
		return os.ReadFile(p)
	}
	r := check(t, Config{Root: dir, ReadFile: read})
	if got := codes(r); !slices.Equal(got, []string{"E0001"}) {
		t.Fatalf("codes %q, want exactly one E0001\n%s", got, r.Report.Text(r.Sources))
	}
	d := r.Report.Diags[0].Diag
	if fact(d, "tool") != "fs" || fact(d, "command") != "read b.mk" || fact(d, "stderr") != "access denied" {
		t.Errorf("facts tool %q command %q stderr %q", fact(d, "tool"), fact(d, "command"), fact(d, "stderr"))
	}
	if st := ExitStatus(r.Report); st != 2 {
		t.Errorf("exit status %d, want 2", st)
	}
	if len(r.Trace.FilesParsed) != 0 {
		t.Errorf("parsed %q after a read failure", r.Trace.FilesParsed)
	}
}

func TestStdModuleTable(t *testing.T) {
	stdDir := fakeStd(t)
	user := writeTree(t, map[string]string{"main.mk": "export F(): int {\n    return 1\n}\n"})
	for _, tc := range []struct {
		target types.Target
		sys    string
	}{{types.X86_64Windows, "std/sys/windows/sys.mk"}, {types.X86_64Linux, "std/sys/linux/sys.mk"}} {
		r := check(t, Config{Root: filepath.Join(user, "main.mk"), StdDir: stdDir, Target: tc.target})
		clean(t, r)
		want := []string{"main", "std.core", "std.fs", "std.raw", "std.sys"}
		if got := r.Trace.ModulesResolved; !slices.Equal(got, want) {
			t.Errorf("%s: modules %q, want %q", tc.target.Name, got, want)
		}
		if !slices.Contains(r.Trace.FilesParsed, tc.sys) {
			t.Errorf("%s: %s not parsed: %q", tc.target.Name, tc.sys, r.Trace.FilesParsed)
		}
		for _, f := range r.Trace.FilesParsed {
			if strings.HasPrefix(f, "std/corpus/") || strings.HasPrefix(f, "std/pkg/") {
				t.Errorf("%s: %s parsed as a module file", tc.target.Name, f)
			}
		}
	}
}

func TestE1102_StdManifestPermits(t *testing.T) {
	// The stand-in std's sys/windows holds an unsafe block; std/pkg.mk's
	// unsafe_modules lists the directory.
	user := writeTree(t, map[string]string{"main.mk": "export F(): int {\n    return 1\n}\n"})
	clean(t, check(t, Config{Root: filepath.Join(user, "main.mk"), StdDir: fakeStd(t), Target: types.X86_64Windows}))
}

// TestManifestEvaluated: pkg.mk is a const computed with no Io: a
// field read from another const, a static slice built in a const
// initializer, and a directory computed by a function all reach the decoded
// Manifest, which the session loads by. std.pkg is checked once, then
// std/pkg.mk and pkg.mk (Trace.ManifestChecked). Things the literal reader
// refused (a call, an operator) evaluate; what does not type is check's
// diagnostic, reported alone.
func TestManifestEvaluated(t *testing.T) {
	text := `const Root = "src"

const Bins: []pkg.Bin = [pkg.Bin(name: "notes", root: Root, link_script: "", cpu: "", subsystem: "console"), pkg.Bin(name: "admin", root: "admin", link_script: "", cpu: "", subsystem: "console")]

Native(windows: bool): str {
    if windows {
        return "src/os/windows"
    }
    return "src/os/linux"
}

const Package = pkg.Package(
    name: "notes",
    bins: Bins,
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [
        pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: Native(true))]),
        pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: Native(false))]),
    ],
    unsafe_modules: ["src/ffi"],
    links: [],
)
`
	dir := writeTree(t, map[string]string{
		"pkg.mk":              text,
		"src/main.mk":         "import os.native\n\nexport Main(sys: &System) {\n    _ = native.N()\n}\n",
		"admin/main.mk":       "export Main(sys: &System) {\n}\n",
		"src/ffi/f.mk":        "export F(): int {\n    var n := 0\n    unsafe \"test\" {\n        n = 1\n    }\n    return n\n}\n",
		"src/os/windows/n.mk": "export N(): int {\n    return 1\n}\n",
		"src/os/linux/n.mk":   "export N(): int {\n    return 2\n}\n",
	})
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir(), Target: types.X86_64Windows})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Check()
	clean(t, r)
	m := s.pkgMan
	if m == nil {
		t.Fatal("no manifest")
	}
	if m.Name != "notes" || !slices.Equal(m.Bins, []Bin{{Name: "admin", Root: "admin", Subsystem: "console"}, {Name: "notes", Root: "src", Subsystem: "console"}}) {
		t.Errorf("name %q bins %v", m.Name, m.Bins)
	}
	if !slices.Equal(m.UnsafeModules, []string{"src/ffi"}) {
		t.Errorf("unsafe %v", m.UnsafeModules)
	}
	if len(m.Targets) != 2 || m.Targets[0].OS != "windows" || m.Targets[0].Modules[0] != (ModuleSel{"os.native", "src/os/windows"}) ||
		m.Targets[1].Modules[0] != (ModuleSel{"os.native", "src/os/linux"}) {
		t.Errorf("targets %v", m.Targets)
	}
	if want := len(manifestFields) + len(manifestFieldsExt); len(m.Fields) != want {
		t.Errorf("field hashes %d, want %d", len(m.Fields), want)
	}
	if want := []string{"pkg.mk", "std.pkg", "std/pkg.mk"}; !slices.Equal(r.Trace.ManifestChecked, want) {
		t.Errorf("ManifestChecked %q, want %q (std.pkg exactly once)", r.Trace.ManifestChecked, want)
	}
	if !slices.Contains(r.Trace.ModulesResolved, "os.native") || slices.Contains(r.Trace.ModulesResolved, "std.pkg") {
		t.Errorf("ModulesResolved %q: os.native selected, std.pkg never a build module", r.Trace.ModulesResolved)
	}
	for _, bad := range []struct {
		name, text, code, reason string
	}{
		// A missing field is supplied empty; a positional call is
		// still E0305.
		{"a positional call", "const Package = pkg.Package(\"x\")\n", "", ""},
		{"no Package", "const Other = 1\n", "E0202", "invalid"},
		{"not a pkg.Package", "const Package = 1\n", "E0202", "invalid"},
		{"an import", "import util\n\n" + manifest(nil), "E0202", "not-found"},
		{"an empty name", manifest(map[string]string{"name": `""`}), "E0202", "invalid"},
		{"a field misnamed", strings.Replace(manifest(nil), `name:`, `nome:`, 1), "", ""},
	} {
		d := writeTree(t, map[string]string{"pkg.mk": bad.text, "src/a.mk": "this would not parse (\n"})
		r := check(t, Config{Root: d})
		if len(r.Report.Diags) == 0 {
			t.Errorf("%s: no diagnostic", bad.name)
			continue
		}
		for _, e := range r.Report.Diags {
			if p := r.Sources.Position(e.Diag.At.StartPos()).Path; p != "pkg.mk" {
				t.Errorf("%s: a diagnostic in %s, not pkg.mk alone: %q", bad.name, p, lines(r))
			}
		}
		if bad.code != "" {
			d0 := r.Report.Diags[0].Diag
			if d0.Code.String() != bad.code || fact(d0, "reason") != bad.reason {
				t.Errorf("%s: got %q reason %q, want %s %s", bad.name, lines(r), fact(d0, "reason"), bad.code, bad.reason)
			}
		}
		if len(r.Trace.FilesParsed) != 0 {
			t.Errorf("%s: the build was parsed after a manifest error: %q", bad.name, r.Trace.FilesParsed)
		}
	}
}

func firstReason(ds []*diag.Diagnostic) string {
	if len(ds) == 0 {
		return ""
	}
	return fact(ds[0], "reason")
}

func TestEmptyDeferredFieldsAccepted(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"pkg.mk":   manifest(nil),
		"src/a.mk": "export A(): int {\n    return 1\n}\n",
	})
	clean(t, check(t, Config{Root: dir}))
	// The stand-in std's own pkg.mk names every field with empty deferred lists.
	clean(t, check(t, Config{Root: dir, StdDir: fakeStd(t)}))
}

// TestCImport_ManifestFaults: a fault in a C field is E0202 invalid at the
// field (cimport.go validateC), before any module is read.
func TestCImport_ManifestFaults(t *testing.T) {
	for _, tc := range []struct{ field, value, at string }{
		{"c_sources", `[pkg.CSource(path: "lib/x.cpp", flags: [])]`, "c_sources[0].path"},
		{"c_sources", `[pkg.CSource(path: "lib/missing.c", flags: [])]`, "c_sources[0].path"},
		{"c_imports", `[pkg.CImport(name: "s", headers: [], defines: [], macros: [], vendor: false)]`, "c_imports[0].headers"},
		{"c_imports", `[pkg.CImport(name: "a.b", headers: ["h.h"], defines: [], macros: [], vendor: false)]`, "c_imports[0].name"},
		{"c_imports", `[pkg.CImport(name: "s", headers: ["<h.h>"], defines: [], macros: [], vendor: false)]`, "c_imports[0].headers[0]"},
		{"c_imports", `[pkg.CImport(name: "s", headers: ["h.h"], defines: ["A B"], macros: [], vendor: false)]`, "c_imports[0].defines[0]"},
		{"asm_sources", `[pkg.AsmSource(path: "a.asm", target: "")]`, "asm_sources[0].path"},
	} {
		dir := writeTree(t, map[string]string{
			"pkg.mk":   manifest(map[string]string{tc.field: tc.value}),
			"src/a.mk": "this would not parse (\n",
		})
		r := check(t, Config{Root: dir})
		if got := lines(r); len(got) != 1 || !strings.HasPrefix(got[0], "pkg.mk:") || !strings.HasSuffix(got[0], ":E0202") {
			t.Errorf("%s: got %q", tc.value, got)
			continue
		}
		d := r.Report.Diags[0].Diag
		if fact(d, "reason") != "invalid" || fact(d, "manifest") != tc.at {
			t.Errorf("%s: reason %q manifest %q, want invalid %s", tc.value, fact(d, "reason"), fact(d, "manifest"), tc.at)
		}
	}
	two := `[pkg.CImport(name: "s", headers: ["h.h"], defines: [], macros: [], vendor: false), ` +
		`pkg.CImport(name: "s", headers: ["g.h"], defines: [], macros: [], vendor: false)]`
	r := check(t, Config{Root: writeTree(t, map[string]string{"pkg.mk": manifest(map[string]string{"c_imports": two}), "src/a.mk": "x (\n"})})
	if got := lines(r); len(got) != 1 || fact(r.Report.Diags[0].Diag, "manifest") != "c_imports[1].name" {
		t.Errorf("duplicate name: %q", got)
	}
}

func TestManifestBeforeLoad(t *testing.T) {
	sel := `[pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "src/os/windows")]), ` +
		`pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: "src/os/linux")])]`
	dir := writeTree(t, map[string]string{
		"pkg.mk":                 manifest(map[string]string{"targets": sel}),
		"src/main.mk":            "import os.native\n\nexport F(): int {\n    return native.N()\n}\n",
		"src/os/windows/n.mk":    "export N(): int {\n    return 1\n}\n",
		"src/os/linux/n.mk":      "export N(): int {\n    return (\n}\n",
		"src/os/linux/deep/d.mk": "nor this (\n",
	})
	r := check(t, Config{Root: dir, Target: types.X86_64Windows})
	clean(t, r)
	// The evaluated manifest: std.pkg was checked exactly once in the
	// session, before std/pkg.mk and pkg.mk, and is no module of the build.
	if want := []string{"pkg.mk", "std.pkg", "std/pkg.mk"}; !slices.Equal(r.Trace.ManifestChecked, want) {
		t.Errorf("ManifestChecked %q, want %q", r.Trace.ManifestChecked, want)
	}
	if slices.Contains(r.Trace.ModulesResolved, "std.pkg") || slices.Contains(r.Trace.ModulesResolved, "src.os.linux") {
		t.Errorf("ModulesResolved %q", r.Trace.ModulesResolved)
	}
	// A manifest error is reported alone: nothing else is listed or parsed.
	bad := writeTree(t, map[string]string{
		"pkg.mk":      manifest(map[string]string{"bins": `[pkg.Bin(name: "x", root: "nowhere", link_script: "", cpu: "", subsystem: "console")]`}),
		"src/main.mk": "this would not parse (\n",
	})
	r = check(t, Config{Root: bad})
	if got := lines(r); len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") {
		t.Errorf("got %q, want the manifest's E0202 alone", got)
	}
	if len(r.Trace.FilesParsed) != 0 {
		t.Errorf("parsed %q after a manifest error", r.Trace.FilesParsed)
	}
}

// TestManifestErrorsAlone checks, under the evaluated manifest,
// that a diagnostic of the manifest step, in std/pkg.mk or pkg.mk,
// of whatever stage of the manifest's own pipeline (parse, names, types,
// decoding, validation), is the whole report even when the package holds an
// error of an earlier phase, the envelope's phase is the manifest's own, and
// nothing of the module pipeline runs. Positions are the emitter's, so only
// the file, the code and the manifest step's facts are compared.
func TestManifestErrorsAlone(t *testing.T) {
	type want struct{ file, code, manifest, reason string }
	broken := "src/main.mk"
	for _, tc := range []struct {
		name, pkg string
		std       map[string]string // nil: the embedded std
		want      []want
		phase     diag.Phase
	}{
		// pkg.mk does not parse: its parse error alone.
		{"pkg parse", "const Package = pkg.Package(\n", nil, []want{{"pkg.mk", "E0101", "", ""}}, diag.PhaseParse},
		// Two validation errors: both reported, each at its field, and the
		// package's parse error (an earlier phase) is not.
		{"pkg validate", manifest(map[string]string{
			"bins":           `[pkg.Bin(name: "x", root: "nowhere", link_script: "", cpu: "", subsystem: "console")]`,
			"unsafe_modules": `["missing"]`,
		}), nil, []want{{"pkg.mk", "E0202", "bins[0].root", "invalid"}, {"pkg.mk", "E0202", "unsafe_modules[0]", "invalid"}}, diag.PhaseImports},
		// std's manifest is broken: reported alone, under its std path.
		{"std manifest", manifest(nil), func() map[string]string {
			f := fakeStdFiles()
			f["pkg.mk"] = "const Package = Make()\n"
			return f
		}(), []want{{"std/pkg.mk", "E0201", "", ""}}, diag.PhaseNames},
		// A type error in pkg.mk: check's, alone, at the types phase.
		{"pkg types", manifest(map[string]string{"name": "1"}), nil, []want{{"pkg.mk", "E0301", "", ""}}, diag.PhaseTypes},
		// No const Package: E0202 invalid, alone.
		{"pkg no Package", "const Other = 1\n", nil, []want{{"pkg.mk", "E0202", "Package", "invalid"}}, diag.PhaseImports},
		// A C field fault: E0202 at the field, fact manifest its path.
		{"pkg c field", manifest(map[string]string{"c_sources": `[pkg.CSource(path: "a.cc", flags: [])]`}), nil,
			[]want{{"pkg.mk", "E0202", "c_sources[0].path", "invalid"}}, diag.PhaseImports},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Root: writeTree(t, map[string]string{"pkg.mk": tc.pkg, broken: "this would not parse (\n"})}
			if tc.std != nil {
				cfg.StdDir = writeTree(t, tc.std)
			}
			r := check(t, cfg)
			var got []want
			for _, e := range r.Report.Diags {
				d := e.Diag
				file := r.Sources.Position(d.At.StartPos()).Path
				got = append(got, want{file, d.Code.String(), fact(d, "manifest"), fact(d, "reason")})
			}
			if c := tc.want[0].code; (c == "E0101" || c == "E0201" || c == "E0301") && len(got) == 1 {
				got[0].manifest, got[0].reason = "", "" // syntax's, resolve's or check's facts, not the driver's
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %+v, want %+v (lines %q)", got, tc.want, lines(r))
			}
			if !r.Report.HasPhase || r.Report.Phase != tc.phase {
				t.Errorf("phase %v (has %v), want %v", r.Report.Phase, r.Report.HasPhase, tc.phase)
			}
			if len(r.Trace.FilesParsed) != 0 || len(r.Trace.ModulesResolved) != 0 {
				t.Errorf("load ran after a manifest error: parsed %q resolved %q", r.Trace.FilesParsed, r.Trace.ModulesResolved)
			}
			if ExitStatus(r.Report) != 1 {
				t.Errorf("exit %d, want 1", ExitStatus(r.Report))
			}
		})
	}
}

func TestTargetSelectionExcludesOtherOS(t *testing.T) {
	sel := `[pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "src/os/windows")]), ` +
		`pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: "src/os/linux")])]`
	files := map[string]string{
		"pkg.mk":              manifest(map[string]string{"targets": sel}),
		"src/main.mk":         "import os.native\n\nexport F(): int {\n    return native.N()\n}\n",
		"src/os/windows/n.mk": "export N(): int {\n    return 1\n}\n",
		"src/os/linux/n.mk":   "export N(): int {\n    return Undefined()\n}\n",
		"src/os/linux/t.mk":   "export T(): bool {\n    return 1\n}\n",
	}
	dir := writeTree(t, files)
	r := check(t, Config{Root: dir, Target: types.X86_64Windows})
	clean(t, r)
	if slices.Contains(r.Trace.ModulesResolved, "src.os.linux") || !slices.Contains(r.Trace.ModulesResolved, "os.native") {
		t.Errorf("ModulesResolved %q", r.Trace.ModulesResolved)
	}
	expectLines(t, check(t, Config{Root: dir, Target: types.X86_64Linux}), "src/os/linux/n.mk:2:12:E0201")
	// The linux directory's type error (E0301) is reported only on its
	// own target, once its names resolve.
	files["src/os/linux/n.mk"] = "export N(): int {\n    return 2\n}\n"
	dir = writeTree(t, files)
	clean(t, check(t, Config{Root: dir, Target: types.X86_64Windows}))
	expectLines(t, check(t, Config{Root: dir, Target: types.X86_64Linux}), "src/os/linux/t.mk:2:12:E0301")
	files["src/os/linux/n.mk"] = "export N(): int {\n    return Undefined()\n}\n"
	files["src/main.mk"] = "import src.os.windows\n\nexport F(): int {\n    return windows.N()\n}\n"
	r = check(t, Config{Root: writeTree(t, files), Target: types.X86_64Windows})
	expectLines(t, r, "src/main.mk:1:8:E0202")
	if len(r.Report.Diags) == 1 {
		d := r.Report.Diags[0].Diag
		if fact(d, "reason") != "target-selected" || !slices.Equal(d.Alternatives, []string{"import os.native"}) {
			t.Errorf("reason %q alternatives %q", fact(d, "reason"), d.Alternatives)
		}
	}
}
