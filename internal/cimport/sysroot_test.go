// Why: the header triple and the system directories of a Windows import
// follow the sysroot's flavour, and nothing may change for a package that
// does not name a Microsoft SDK: its cache key and its Clang command line
// must stay what they were.
// What: TestCImport_KeyUnchanged.
// How: a fixed request over fixed paths, its key and its shared Clang
// flags compared with the values the importer gave before the flavour
// existed.

package cimport

import (
	"encoding/hex"
	"path/filepath"
	"slices"
	"testing"

	"mk/internal/source"
)

// TestCImport_KeyUnchanged: for a package whose headers are its own, with
// no sysroot and with a sysroot of plain headers, the triple, the cache key
// and the flags every Clang run shares are byte-identical to the
// importer's before the Microsoft flavour (the hex values were taken from
// that code).
func TestCImport_KeyUnchanged(t *testing.T) {
	id := source.HashBytes("test.identity", []byte("clang 21"))
	for _, tc := range []struct {
		sysroot []string
		key     string
	}{
		{nil, "323536cb16577819f8f34ad6eaf3aabd5ab958f23851973c0387087bdb4c11be"},
		{[]string{`C:\sys\include`}, "13ee79969e4de6d3071cde9194c2bfe0c7b9e040ad3a8d640f32e5250d82ebb3"},
	} {
		r := Request{Target: "x86_64-windows", Triple: ParseTriple("x86_64-windows"),
			Imports: []Import{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}, Macros: []string{"int M(int a)"}}},
			Include: []string{`C:\pkg`}, Sysroot: tc.sysroot}
		if r.Triple != "x86_64-w64-windows-gnu" {
			t.Errorf("triple %q", r.Triple)
		}
		k := Key(id, `C:\clang\lib\clang\21`, r)
		if got := hex.EncodeToString(k[:]); got != tc.key {
			t.Errorf("sysroot %q: key %s, want %s", tc.sysroot, got, tc.key)
		}
		want := []string{"--target=x86_64-w64-windows-gnu", "-nostdinc", "-x", "c", "-std=gnu11", "-fno-caret-diagnostics",
			"-fmacro-backtrace-limit=0", "-ferror-limit=0", "-Wno-everything", "-fno-color-diagnostics"}
		if len(tc.sysroot) > 0 {
			want = append(want, "--sysroot="+tc.sysroot[0])
		}
		want = append(want, "-I", `C:\pkg`, "-isystem", filepath.Join(`C:\clang\lib\clang\21`, "include"))
		for _, d := range tc.sysroot {
			want = append(want, "-isystem", d)
		}
		if got := baseArgs(r, `C:\clang\lib\clang\21`); !slices.Equal(got, want) {
			t.Errorf("sysroot %q: args\n%q\nwant\n%q", tc.sysroot, got, want)
		}
	}
}
