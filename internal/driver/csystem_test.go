// Why: installing or naming no Microsoft SDK must change no existing
// build: the translation request of a package with its own headers, and so
// its cache key, must be the one mk always built.
// What: TestCImport_RequestUnchanged and TestCImport_RequestMicrosoft.
// How: cRequest for a package written in a temporary directory, with no
// sysroot variable, with each spelling of MK_SYSROOT_x86_64-windows naming
// plain headers, and with a fake SDK tree; compared field by field and by
// key with the request built the way mk built it before the flavour; and
// the flags the shim and the C sources get beside it (cImportsOf).

package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"mk/internal/cimport"
	"mk/internal/source"
)

// clearSysroots blanks every variable a sysroot is read from, for both
// spellings of the Windows target's name.
func clearSysroots(t *testing.T) {
	t.Setenv("MK_EVIDENCE", "")
	for _, v := range []string{"MK_SYSROOT_x86_64-windows", "MK_SYSROOT_x86_64_windows"} {
		t.Setenv(v, "")
	}
}

// requestSession is a checked session over a package (the check makes
// its world; it needs no Clang), and a manifest with one C import of the
// package's own header, with the import.
func requestSession(t *testing.T) (*Session, *Manifest, string, []cimport.Import) {
	t.Helper()
	dir := writeTree(t, map[string]string{
		"pkg.mk":   manifest(map[string]string{"name": `"rq"`}),
		"k.h":      "int k_one(void);\n",
		"src/a.mk": "export F(): int {\n    return 1\n}\n",
	})
	win, _, ok := TargetNamed("x86_64-windows")
	if !ok {
		t.Fatal("no x86_64-windows target")
	}
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir(), StdDir: writeTree(t, fakeStdFiles()), Target: win})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) > 0 {
		t.Fatalf("check:\n%s", r.Report.Text(r.Sources))
	}
	man := &Manifest{CImports: []CImport{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}}}}
	return s, man, dir, []cimport.Import{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}}}
}

// cSourcesOnly is a manifest with one C source and no C import: cImportsOf
// gives its system setup without running Clang.
var cSourcesOnly = &Manifest{CSources: []CSource{{Path: "k.c"}}}

// TestCImport_RequestUnchanged: with no sysroot, and with a sysroot of
// plain headers named by either spelling of the variable, the request
// (triple, search directories) and its cache key are byte-identical to the
// request mk built before the flavour (ParseTriple, sysrootFor).
func TestCImport_RequestUnchanged(t *testing.T) {
	plain := writeTree(t, map[string]string{"include/x.h": "#define X 1\n", "more/y.h": ""})
	id := source.HashBytes("test.identity", []byte("clang"))
	for _, tc := range []struct{ name, variable, value string }{
		{"none", "", ""},
		{"dashed", "MK_SYSROOT_x86_64-windows", plain + string(os.PathListSeparator) + filepath.Join(plain, "more")},
		{"underscored", "MK_SYSROOT_x86_64_windows", plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSysroots(t)
			if tc.variable != "" {
				t.Setenv(tc.variable, tc.value)
			}
			s, man, dir, imports := requestSession(t)
			got, d := s.cRequest(imports, man, dir, "x86_64-windows")
			if d != nil {
				t.Fatal(d)
			}
			before := cimport.Request{Target: "x86_64-windows", Triple: cimport.ParseTriple("x86_64-windows"), Imports: imports,
				Include: cIncludeDirs(man, dir), Sysroot: sysrootFor("x86_64-windows", os.Getenv)}
			if !reflect.DeepEqual(got, before) {
				t.Errorf("request\n%+v\nwant\n%+v", got, before)
			}
			if got.Triple != cimport.ParseTriple("x86_64-windows") {
				t.Errorf("triple %s", got.Triple)
			}
			if cimport.Key(id, "rd", got) != cimport.Key(id, "rd", before) {
				t.Error("the cache key changed")
			}
			if tc.variable != "" && len(got.Sysroot) == 0 {
				t.Errorf("%s=%s gave no sysroot", tc.variable, tc.value)
			}
			if m, _ := filepath.Glob(filepath.Join(s.cacheDir(), "*", "msvch")); len(m) > 0 {
				t.Error("mk's Windows SDK headers were written for a sysroot that is no SDK")
			}
			if ci, ds := s.cImportsOf(nil, cSourcesOnly, dir, "", ""); len(ds) > 0 || !slices.Equal(ci.sysFlags, cimport.TripleFlags(got.Triple)) {
				t.Errorf("C sources' system flags %q (%v), want %q", ci.sysFlags, ds, cimport.TripleFlags(got.Triple))
			}
		})
	}
}

// TestCImport_RequestMicrosoft: a fake SDK tree named by the underscored
// variable gives the MSVC triple, and its directories in the variable's
// order after mk's own headers, which are written under the cache.
func TestCImport_RequestMicrosoft(t *testing.T) {
	clearSysroots(t)
	sdk := writeTree(t, map[string]string{"um/windows.h": "", "um/winnt.h": "", "shared/sal.h": "", "ucrt/stdlib.h": ""})
	dirs := []string{filepath.Join(sdk, "um"), filepath.Join(sdk, "shared"), filepath.Join(sdk, "ucrt")}
	t.Setenv("MK_SYSROOT_x86_64_windows", dirs[0]+string(os.PathListSeparator)+dirs[1]+string(os.PathListSeparator)+dirs[2])
	s, man, dir, imports := requestSession(t)
	got, d := s.cRequest(imports, man, dir, "x86_64-windows")
	if d != nil {
		t.Fatal(d)
	}
	if got.Triple != "x86_64-pc-windows-msvc" {
		t.Errorf("triple %s", got.Triple)
	}
	if len(got.Sysroot) != 4 || !slices.Equal(got.Sysroot[1:], dirs) || filepath.Base(filepath.Dir(got.Sysroot[0])) != "msvch" ||
		!strings.HasPrefix(got.Sysroot[0], s.cacheDir()) {
		t.Errorf("sysroot %q, want mk's headers under the cache, then %q", got.Sysroot, dirs)
	}
	for _, n := range cimport.MicrosoftHeaderNames {
		if _, err := os.Stat(filepath.Join(got.Sysroot[0], n)); err != nil {
			t.Error(err)
		}
	}
	// The shim and the C sources compile for the same Visual C++ as the
	// headers are read for.
	if ci, ds := s.cImportsOf(nil, cSourcesOnly, dir, "", ""); len(ds) > 0 || !slices.Equal(ci.sysFlags, cimport.TripleFlags(got.Triple)) ||
		len(ci.sysFlags) == 0 || !slices.Equal(ci.sysdir, got.Sysroot) {
		t.Errorf("C sources' system flags %q and directories %q (%v)", ci.sysFlags, ci.sysdir, ds)
	}
	// The Linux target ignores a Windows SDK.
	if lin, _ := s.cRequest(imports, man, dir, "x86_64-linux"); lin.Triple != cimport.ParseTriple("x86_64-linux") {
		t.Errorf("linux triple %s", lin.Triple)
	}
}
