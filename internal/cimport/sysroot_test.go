// Why: the header triple and the system directories of a Windows import
// follow the sysroot's flavour, and nothing may change for a package that
// does not name a Microsoft SDK: its cache key and its Clang command line
// must stay what they were.
// What: TestCImport_KeyUnchanged, _SysrootFlavour and _FlavourTriple.
// How: a fixed request over fixed paths, its key and its shared Clang
// flags compared with the values the importer gave before the flavour
// existed; fake SDK and mingw-w64 trees written by the test (no SDK is
// needed) for the flavour, the triple and the Clang command line, and a
// real translation of the fake headers where Clang is present.

package cimport

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		sys := SystemFor("x86_64-windows", tc.sysroot)
		r := Request{Target: "x86_64-windows", Triple: sys.Triple,
			Imports: []Import{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}, Macros: []string{"int M(int a)"}}},
			Include: []string{`C:\pkg`}, Sysroot: sys.Dirs}
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

// fakeTree writes files (slash paths to text) under a new temporary
// directory and returns it.
func fakeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeSDK is a tree shaped like the Windows SDK's Include\<version>: um
// with windows.h beside winnt.h, shared and ucrt. Its windows.h says which
// compiler family the triple makes Clang claim.
var fakeSDK = map[string]string{
	"um/windows.h":   "#include <winnt.h>\n#include <sal.h>\n#ifdef _MSC_VER\n#define FAKE_MSVC 1\n#endif\n#ifdef __MINGW32__\n#define FAKE_MINGW 1\n#endif\nint fake_window(void);\n",
	"um/winnt.h":     "typedef unsigned long FAKE_DWORD;\n",
	"shared/sal.h":   "#define _In_\n",
	"ucrt/corecrt.h": "#define FAKE_UCRT 1\n",
}

// sdkDirs are fakeSDK's include directories under root, in the SDK's order.
func sdkDirs(root string) []string {
	return []string{filepath.Join(root, "um"), filepath.Join(root, "shared"), filepath.Join(root, "ucrt")}
}

// TestCImport_SysrootFlavour: the flavour of a sysroot list. windows.h
// found first beside winnt.h is the Microsoft SDK, in any order of its
// directories; a _mingw.h anywhere makes it mingw-w64; a windows.h without
// winnt.h beside it, a directory named windows.h, no sysroot or another
// target is no flavour.
func TestCImport_SysrootFlavour(t *testing.T) {
	sdk := fakeTree(t, fakeSDK)
	mingw := fakeTree(t, map[string]string{"include/_mingw.h": "", "include/windows.h": "", "include/winnt.h": ""})
	lone := fakeTree(t, map[string]string{"w/windows.h": "", "w/winntx.h": "", "d/windows.h/x": "", "d/winnt.h": ""})
	plain := fakeTree(t, map[string]string{"inc/x.h": ""})
	um, shared, ucrt := sdkDirs(sdk)[0], sdkDirs(sdk)[1], sdkDirs(sdk)[2]
	for _, tc := range []struct {
		name   string
		target string
		dirs   []string
		want   Flavour
	}{
		{"sdk", "x86_64-windows", sdkDirs(sdk), FlavourMicrosoft},
		{"sdk reordered", "x86_64-windows", []string{ucrt, shared, um}, FlavourMicrosoft},
		{"sdk after plain", "x86_64-windows", []string{filepath.Join(plain, "inc"), um, shared}, FlavourMicrosoft},
		{"first windows.h wins", "x86_64-windows", []string{filepath.Join(lone, "w"), um}, FlavourNone},
		{"windows.h is a directory", "x86_64-windows", []string{filepath.Join(lone, "d")}, FlavourNone},
		{"mingw", "x86_64-windows", []string{filepath.Join(mingw, "include")}, FlavourMinGW},
		{"sdk and a mingw tree", "x86_64-windows", []string{um, shared, filepath.Join(mingw, "include")}, FlavourMinGW},
		{"plain headers", "x86_64-windows", []string{filepath.Join(plain, "inc")}, FlavourNone},
		{"no sysroot", "x86_64-windows", nil, FlavourNone},
		{"linux target", "x86_64-linux", sdkDirs(sdk), FlavourNone},
	} {
		if got := SysrootFlavour(tc.target, tc.dirs); got != tc.want {
			t.Errorf("%s: flavour %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		target string
		f      Flavour
		want   string
	}{
		{"x86_64-windows", FlavourMicrosoft, "x86_64-pc-windows-msvc"}, {"x86_64-windows", FlavourMinGW, "x86_64-w64-windows-gnu"},
		{"x86_64-windows", FlavourNone, "x86_64-w64-windows-gnu"}, {"x86_64-linux", FlavourMicrosoft, "x86_64-linux-musl"},
	} {
		if got := HeaderTriple(tc.target, tc.f); got != tc.want {
			t.Errorf("HeaderTriple(%s, %v) = %s, want %s", tc.target, tc.f, got, tc.want)
		}
	}
}

// TestCImport_FlavourTriple: a Microsoft SDK as the sysroot gives the MSVC
// triple and the sysroot's directories, in order, as the system
// directories of every Clang run; with Clang present, the fake headers
// translate under it (Clang claims _MSC_VER, not __MINGW32__), and the
// same headers marked as mingw-w64 translate as before.
func TestCImport_FlavourTriple(t *testing.T) {
	sdk := fakeTree(t, fakeSDK)
	sys := SystemFor("x86_64-windows", sdkDirs(sdk))
	if sys.Flavour != FlavourMicrosoft || sys.Triple != "x86_64-pc-windows-msvc" || !slices.Equal(sys.Dirs, sdkDirs(sdk)) {
		t.Fatalf("system %+v", sys)
	}
	r := Request{Target: "x86_64-windows", Triple: sys.Triple, Imports: []Import{{Name: "w", Headers: []string{"windows.h"}}},
		Include: []string{t.TempDir()}, Sysroot: sys.Dirs}
	args := baseArgs(r, `C:\clang`)
	if args[0] != "--target=x86_64-pc-windows-msvc" {
		t.Errorf("args %q", args)
	}
	var isys []string
	for i, a := range args {
		if a == "-isystem" {
			isys = append(isys, args[i+1])
		}
	}
	if want := append([]string{filepath.Join(`C:\clang`, "include")}, sdkDirs(sdk)...); !slices.Equal(isys, want) {
		t.Errorf("-isystem %q, want %q", isys, want)
	}
	tl, rd := clangFor(t)
	r.WorkDir = filepath.Join(t.TempDir(), "w")
	res, err := Translate(context.Background(), tl, rd, r)
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, res.Modules[0], "export type FakeDword = u32", "export extern fake_window(): i32", "export const FakeMsvc: i32 = 1")
	if strings.Contains(res.Modules[0], "FakeMingw") {
		t.Errorf("the MSVC triple claims mingw:\n%s", res.Modules[0])
	}
	// The same tree with a _mingw.h is read as mingw-w64 always was.
	mingw := fakeTree(t, map[string]string{"um/_mingw.h": ""})
	dirs := append([]string{filepath.Join(mingw, "um")}, sdkDirs(sdk)...)
	ms := SystemFor("x86_64-windows", dirs)
	if ms.Flavour != FlavourMinGW || ms.Triple != ParseTriple("x86_64-windows") {
		t.Fatalf("mingw system %+v", ms)
	}
	r.Triple, r.Sysroot, r.WorkDir = ms.Triple, ms.Dirs, filepath.Join(t.TempDir(), "w")
	res, err = Translate(context.Background(), tl, rd, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Modules[0], "export const FakeMingw: i32 = 1") || strings.Contains(res.Modules[0], "FakeMsvc") {
		t.Errorf("mingw-w64 triple:\n%s", res.Modules[0])
	}
}
