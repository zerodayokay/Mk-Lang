// Why: installing or naming no Microsoft SDK must change no existing
// build: the translation request of a package with its own headers, and so
// its cache key, must be the one mk always built.
// What: TestCImport_RequestUnchanged and TestCImport_RequestMicrosoft.
// How: cRequest for a package written in a temporary directory, with no
// sysroot variable, with each spelling of MK_SYSROOT_x86_64-windows naming
// plain headers, and with a fake SDK tree; compared field by field and by
// key with the request built the way mk built it before the flavour.

package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// requestSession is a session over a package with one C import of its own
// header, and the import.
func requestSession(t *testing.T) (*Session, *Manifest, string, []cimport.Import) {
	t.Helper()
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"rq"`,
			"c_imports": `[pkg.CImport(name: "k", headers: ["k.h"], defines: ["K=1"], macros: [], vendor: false)]`}),
		"k.h":      "int k_one(void);\n",
		"src/a.mk": "export F(): int {\n    return 1\n}\n",
	})
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir(), StdDir: writeTree(t, fakeStdFiles())})
	if err != nil {
		t.Fatal(err)
	}
	man := &Manifest{CImports: []CImport{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}}}}
	return s, man, dir, []cimport.Import{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}}}
}

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
			got := s.cRequest(imports, man, dir, "x86_64-windows")
			before := cimport.Request{Target: "x86_64-windows", Triple: cimport.ParseTriple("x86_64-windows"), Imports: imports,
				Include: cIncludeDirs(man, dir), Sysroot: sysrootFor("x86_64-windows", os.Getenv)}
			if !reflect.DeepEqual(got, before) {
				t.Errorf("request\n%+v\nwant\n%+v", got, before)
			}
			if got.Triple != "x86_64-w64-windows-gnu" {
				t.Errorf("triple %s", got.Triple)
			}
			if cimport.Key(id, "rd", got) != cimport.Key(id, "rd", before) {
				t.Error("the cache key changed")
			}
			if tc.variable != "" && len(got.Sysroot) == 0 {
				t.Errorf("%s=%s gave no sysroot", tc.variable, tc.value)
			}
		})
	}
}

// TestCImport_RequestMicrosoft: a fake SDK tree named by the underscored
// variable gives the MSVC triple, and its directories in the variable's
// order.
func TestCImport_RequestMicrosoft(t *testing.T) {
	clearSysroots(t)
	sdk := writeTree(t, map[string]string{"um/windows.h": "", "um/winnt.h": "", "shared/sal.h": "", "ucrt/stdlib.h": ""})
	dirs := []string{filepath.Join(sdk, "um"), filepath.Join(sdk, "shared"), filepath.Join(sdk, "ucrt")}
	t.Setenv("MK_SYSROOT_x86_64_windows", dirs[0]+string(os.PathListSeparator)+dirs[1]+string(os.PathListSeparator)+dirs[2])
	s, man, dir, imports := requestSession(t)
	got := s.cRequest(imports, man, dir, "x86_64-windows")
	if got.Triple != "x86_64-pc-windows-msvc" {
		t.Errorf("triple %s", got.Triple)
	}
	if !slices.Equal(got.Sysroot[len(got.Sysroot)-3:], dirs) {
		t.Errorf("sysroot %q, want it to end with %q", got.Sysroot, dirs)
	}
	// The Linux target ignores a Windows SDK.
	if lin := s.cRequest(imports, man, dir, "x86_64-linux"); lin.Triple != cimport.ParseTriple("x86_64-linux") {
		t.Errorf("linux triple %s", lin.Triple)
	}
}
