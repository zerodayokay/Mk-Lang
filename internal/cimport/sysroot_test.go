// Why: the header triple and the system directories of a Windows import
// follow the sysroot's flavour, and nothing may change for a package that
// does not name a Microsoft SDK: its cache key and its Clang command line
// must stay what they were.
// What: TestCImport_KeyUnchanged, _SysrootFlavour, _MicrosoftHeaders,
// _FlavourTriple, _MSVCVersion and _MicrosoftTypes.
// How: a fixed request over fixed paths, its key and its shared Clang
// flags compared with the values the importer gave before the flavour
// existed; fake SDK and mingw-w64 trees written by the test (no SDK is
// needed) for the flavour, the triple and the Clang command line, and a
// real translation of the fake headers where Clang is present.

package cimport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/toolchain"
)

// TestCImport_KeyUnchanged: for a package whose headers are its own, with
// no sysroot and with a sysroot of plain headers, the triple, the cache key
// and the flags every Clang run shares match the plain ParseTriple request
// without SystemFor.
func TestCImport_KeyUnchanged(t *testing.T) {
	id := source.HashBytes("test.identity", []byte("clang 21"))
	for _, tc := range [][]string{nil, {`C:\sys\include`}} {
		sys, err := SystemFor("x86_64-windows", tc, "")
		if err != nil {
			t.Fatal(err)
		}
		r := Request{Target: "x86_64-windows", Triple: sys.Triple,
			Imports: []Import{{Name: "k", Headers: []string{"k.h"}, Defines: []string{"K=1"}, Macros: []string{"int M(int a)"}}},
			Include: []string{`C:\pkg`}, Sysroot: sys.Dirs}
		plain := Request{Target: "x86_64-windows", Triple: ParseTriple("x86_64-windows"),
			Imports: r.Imports, Include: r.Include, Sysroot: tc}
		if r.Triple != plain.Triple {
			t.Errorf("triple %q, want %q", r.Triple, plain.Triple)
		}
		if Key(id, `C:\clang\lib\clang\21`, r) != Key(id, `C:\clang\lib\clang\21`, plain) {
			t.Errorf("sysroot %q: cache key changed", tc)
		}
		want := []string{toolchain.ClangTargetArg(plain.Triple), "-nostdinc", "-x", "c", "-std=gnu11", "-fno-caret-diagnostics",
			"-fmacro-backtrace-limit=0", "-ferror-limit=0", "-Wno-everything", "-fno-color-diagnostics"}
		want = append(want, TripleFlags(plain.Triple)...)
		if len(tc) > 0 {
			want = append(want, "--sysroot="+tc[0])
		}
		want = append(want, "-I", `C:\pkg`, "-isystem", filepath.Join(`C:\clang\lib\clang\21`, "include"))
		for _, d := range tc {
			want = append(want, "-isystem", d)
		}
		if got := baseArgs(r, `C:\clang\lib\clang\21`); !slices.Equal(got, want) {
			t.Errorf("sysroot %q: args\n%q\nwant\n%q", tc, got, want)
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
// with windows.h beside winnt.h, shared and ucrt, and no MSVC toolset
// header. Under the MSVC triple its windows.h includes the toolset headers
// the SDK lacks (mk's own must be found) and says which compiler family
// the triple makes Clang claim; vc is a decoy toolset directory.
var fakeSDK = map[string]string{
	"um/windows.h": "#ifdef _MSC_VER\n#include <excpt.h>\n#include <string.h>\n#define FAKE_MSVC 1\n#endif\n" +
		"#ifdef __MINGW32__\n#define FAKE_MINGW 1\n#endif\n#include <winnt.h>\nint fake_window(SIZE_T n);\n",
	"um/winnt.h":            "typedef unsigned long long ULONG_PTR;\ntypedef ULONG_PTR SIZE_T;\ntypedef unsigned long FAKE_DWORD;\n",
	"shared/sal.h":          "#define _In_\n",
	"ucrt/string.h":         "#include <vcruntime_string.h>\nsize_t strlen(const char *s);\n",
	"vc/vcruntime.h":        "#error the toolset's vcruntime.h was read\n",
	"vc/vadefs.h":           "#error the toolset's vadefs.h was read\n",
	"vc/excpt.h":            "#error the toolset's excpt.h was read\n",
	"vc/vcruntime_string.h": "#error the toolset's vcruntime_string.h was read\n",
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
		{"x86_64-windows", FlavourMicrosoft, "x86_64-pc-windows-msvc"}, {"x86_64-windows", FlavourMinGW, ParseTriple("x86_64-windows")},
		{"x86_64-windows", FlavourNone, ParseTriple("x86_64-windows")}, {"x86_64-linux", FlavourMicrosoft, ParseTriple("x86_64-linux")},
	} {
		if got := HeaderTriple(tc.target, tc.f); got != tc.want {
			t.Errorf("HeaderTriple(%s, %v) = %s, want %s", tc.target, tc.f, got, tc.want)
		}
	}
}

// isystemArgs are the -isystem directories of a Clang command line.
func isystemArgs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "-isystem" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// TestCImport_MicrosoftHeaders: for a Microsoft SDK, mk's four headers are
// written into a directory named by their content, which comes first among
// the system directories (after Clang's own on the command line), before
// the SDK's and before a toolset directory the list names; writing them
// again gives the same directory, other headers another one and so another
// cache key. No other flavour writes anything.
func TestCImport_MicrosoftHeaders(t *testing.T) {
	sdk := fakeTree(t, fakeSDK)
	dirs := append(sdkDirs(sdk), filepath.Join(sdk, "vc"))
	base := t.TempDir()
	sys, err := SystemFor("x86_64-windows", dirs, base)
	if err != nil {
		t.Fatal(err)
	}
	if sys.Flavour != FlavourMicrosoft || sys.Triple != "x86_64-pc-windows-msvc" || len(sys.Dirs) != len(dirs)+1 ||
		!slices.Equal(sys.Dirs[1:], dirs) || filepath.Dir(sys.Dirs[0]) != base {
		t.Fatalf("system %+v", sys)
	}
	want := MicrosoftHeaders()
	for _, n := range MicrosoftHeaderNames {
		data, err := os.ReadFile(filepath.Join(sys.Dirs[0], n))
		if err != nil || string(data) != want[n] {
			t.Errorf("%s: %v\n%s", n, err, data)
		}
	}
	if len(want) != 4 || !strings.Contains(want["vcruntime.h"], "typedef unsigned long long size_t;") ||
		!strings.Contains(want["vcruntime.h"], "#define _VCRTIMP\n") || !strings.Contains(want["excpt.h"], "EXCEPTION_DISPOSITION;") {
		t.Errorf("headers %q", want)
	}
	again, err := SystemFor("x86_64-windows", dirs, base)
	if err != nil || again.Dirs[0] != sys.Dirs[0] {
		t.Errorf("a second write gave %q (%v), want %q", again.Dirs, err, sys.Dirs[0])
	}
	other := map[string]string{}
	for n, s := range want {
		other[n] = s
	}
	other["excpt.h"] += "/* changed */\n"
	od, err := WriteHeaderDir(base, "mk.cimport.msvch.v1", MicrosoftHeaderNames, other)
	if err != nil || od == sys.Dirs[0] {
		t.Errorf("changed headers in %q (%v), the same directory as before", od, err)
	}
	r := Request{Target: "x86_64-windows", Triple: sys.Triple, Imports: []Import{{Name: "w", Headers: []string{"windows.h"}}}, Sysroot: sys.Dirs}
	args := baseArgs(r, `C:\clang`)
	if args[0] != toolchain.ClangTargetArg("x86_64-pc-windows-msvc") || !slices.Equal(isystemArgs(args), append([]string{filepath.Join(`C:\clang`, "include")}, sys.Dirs...)) {
		t.Errorf("args %q", args)
	}
	id := source.HashBytes("test.identity", []byte("clang"))
	changed := r
	changed.Sysroot = append([]string{od}, dirs...)
	if Key(id, "rd", r) == Key(id, "rd", changed) {
		t.Error("the cache key does not follow mk's headers")
	}
	for _, d := range [][]string{nil, {filepath.Join(sdk, "ucrt")}} {
		empty := t.TempDir()
		if s, err := SystemFor("x86_64-windows", d, empty); err != nil || !slices.Equal(s.Dirs, d) {
			t.Errorf("sysroot %q gave %+v, %v", d, s, err)
		}
		if ents, _ := os.ReadDir(empty); len(ents) != 0 {
			t.Errorf("sysroot %q wrote %d entries", d, len(ents))
		}
	}
}

// TestCImport_FlavourTriple: with Clang present, the fake SDK translates
// under the MSVC triple with mk's headers, never the decoy toolset's (it
// would be an error): Clang claims _MSC_VER, not __MINGW32__, size_t is the
// view's SizeT as on every other target and the SDK's SIZE_T SizeTType,
// and the headers' C11 string functions and exception disposition bind.
// The same tree with a _mingw.h is read as mingw-w64 always was.
func TestCImport_FlavourTriple(t *testing.T) {
	tl, rd := clangFor(t)
	sdk := fakeTree(t, fakeSDK)
	sys, err := SystemFor("x86_64-windows", append(sdkDirs(sdk), filepath.Join(sdk, "vc")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Target: "x86_64-windows", Triple: sys.Triple, Imports: []Import{{Name: "w", Headers: []string{"windows.h"}}},
		Include: []string{t.TempDir()}, Sysroot: sys.Dirs, WorkDir: filepath.Join(t.TempDir(), "w")}
	res, err := Translate(context.Background(), tl, rd, r)
	if err != nil {
		if ce, ok := err.(*Error); ok {
			t.Fatalf("%v\n%s", ce, ce.Stderr)
		}
		t.Fatal(err)
	}
	wantLines(t, res.Modules[0], "export type SizeT = uint", "export type ExceptionDisposition = i32", "export extern memcpy(d: *void, s: *void, n: uint): *void",
		"export type SizeTType = UlongPtr", "export type FakeDword = u32",
		"export extern fake_window(n: SizeTType): i32", "export const FakeMsvc: i32 = 1")
	if strings.Contains(res.Modules[0], "FakeMingw") {
		t.Errorf("the MSVC triple claims mingw:\n%s", res.Modules[0])
	}
	mingw := fakeTree(t, map[string]string{"um/_mingw.h": "", "um/windows.h": "#define FAKE_PLAIN 1\n"})
	ms, err := SystemFor("x86_64-windows", append([]string{filepath.Join(mingw, "um")}, sdkDirs(sdk)...), t.TempDir())
	if err != nil || ms.Flavour != FlavourMinGW || ms.Triple != ParseTriple("x86_64-windows") || len(ms.Dirs) != 4 {
		t.Fatalf("mingw system %+v, %v", ms, err)
	}
	r.Triple, r.Sysroot, r.WorkDir = ms.Triple, ms.Dirs, filepath.Join(t.TempDir(), "w")
	if res, err = Translate(context.Background(), tl, rd, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Modules[0], "export const FakePlain: i32 = 1") || strings.Contains(res.Modules[0], "FakeMsvc") {
		t.Errorf("mingw-w64 flavour:\n%s", res.Modules[0])
	}
}

// newerClang stands in for a Clang that would compile for a newer Visual
// C++, as it does when it finds a newer Visual Studio installed: it names
// 19.50 first on every command line, so only a version mk names after it
// decides.
type newerClang struct{ Clang }

func (c newerClang) RunClang(ctx context.Context, args []string, dir string) ([]byte, []byte, error) {
	return c.Clang.RunClang(ctx, append([]string{"-fms-compatibility-version=19.50"}, args...), dir)
}

func (c newerClang) StreamClang(ctx context.Context, args []string, dir string, read func(io.Reader) error) ([]byte, error) {
	return c.Clang.StreamClang(ctx, append([]string{"-fms-compatibility-version=19.50"}, args...), dir, read)
}

// TestCImport_MSVCVersion: under the MSVC triple every Clang run of the
// importer names Visual C++ 19.33, the version Clang assumes with no Visual
// Studio, and no other triple gets a flag; so a Clang that would compile
// for a newer Visual C++ still reads a header that tests _MSC_VER >= 1950
// (as the SDK's ucrt does to stop making its inline functions static) as
// 19.33, on every machine.
func TestCImport_MSVCVersion(t *testing.T) {
	if got := TripleFlags("x86_64-pc-windows-msvc"); !slices.Equal(got, []string{"-fms-compatibility-version=19.33"}) {
		t.Errorf("MSVC triple flags %q", got)
	}
	for _, tr := range []string{"x86_64-w64-windows-gnu", "x86_64-linux-musl", "arm64-apple-macos"} {
		if got := TripleFlags(tr); got != nil {
			t.Errorf("%s flags %q, want none", tr, got)
		}
	}
	if args := baseArgs(Request{Triple: "x86_64-pc-windows-msvc"}, `C:\clang`); !slices.Contains(args, "-fms-compatibility-version=19.33") {
		t.Errorf("MSVC args %q", args)
	}
	tl, rd := clangFor(t)
	sdk := fakeTree(t, map[string]string{
		"um/windows.h": "#include <winnt.h>\n#if _MSC_VER >= 1950\n#define FAKE_NEW 1\n#else\n#define FAKE_OLD _MSC_VER\n#endif\n",
		"um/winnt.h":   "typedef unsigned long FAKE_DWORD;\n",
	})
	sys, err := SystemFor("x86_64-windows", []string{filepath.Join(sdk, "um")}, t.TempDir())
	if err != nil || sys.Triple != "x86_64-pc-windows-msvc" {
		t.Fatalf("system %+v, %v", sys, err)
	}
	res, err := Translate(context.Background(), newerClang{tl}, rd, Request{Target: "x86_64-windows", Triple: sys.Triple,
		Imports: []Import{{Name: "w", Headers: []string{"windows.h"}}}, Include: []string{t.TempDir()}, Sysroot: sys.Dirs,
		WorkDir: filepath.Join(t.TempDir(), "w")})
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, res.Modules[0], "export const FakeOld: i32 = 1933")
	if strings.Contains(res.Modules[0], "FakeNew") {
		t.Errorf("the headers were read for Visual C++ 19.50:\n%s", res.Modules[0])
	}
}

// TestCImport_MicrosoftTypes: mk's toolset headers declare each
// pointer-sized and wide-character type as Clang's own headers do for the
// MSVC triple (a typedef that differs from stddef.h's or stdint.h's is a
// Clang error), so they bind as on every other target, and the exception
// disposition and filter results have their documented values. A wrong
// typedef here would change thousands of SDK bindings; this runs without
// an SDK.
func TestCImport_MicrosoftTypes(t *testing.T) {
	tl, rd := clangFor(t)
	sdk := fakeTree(t, map[string]string{
		"um/windows.h": "#include <vcruntime.h>\n#include <excpt.h>\n#include <stdarg.h>\n#include <stddef.h>\n#include <stdint.h>\n#include <winnt.h>\n",
		"um/winnt.h":   "int fake_va(va_list ap);\n",
		"shared/sal.h": "",
	})
	sys, err := SystemFor("x86_64-windows", []string{filepath.Join(sdk, "um"), filepath.Join(sdk, "shared")}, t.TempDir())
	if err != nil || sys.Flavour != FlavourMicrosoft {
		t.Fatalf("system %+v, %v", sys, err)
	}
	res, err := Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: sys.Triple,
		Imports: []Import{{Name: "w", Headers: []string{"windows.h"}}}, Include: []string{t.TempDir()}, Sysroot: sys.Dirs,
		WorkDir: filepath.Join(t.TempDir(), "w")})
	if err != nil {
		if ce, ok := err.(*Error); ok {
			t.Fatalf("%v\n%s", ce, ce.Stderr)
		}
		t.Fatal(err)
	}
	view := res.Modules[0]
	for _, want := range []string{"export type SizeT = uint\n", "export type PtrdiffT = int\n", "export type IntptrT = int\n",
		"export type UintptrT = uint\n", "export type WcharT = u16\n",
		"export const ExceptionContinueExecution: ExceptionDisposition = 0\n", "export const ExceptionContinueSearch: ExceptionDisposition = 1\n",
		"export const ExceptionNestedException: ExceptionDisposition = 2\n", "export const ExceptionCollidedUnwind: ExceptionDisposition = 3\n",
		"export const ExceptionExecuteHandler: i32 = 1\n", "export const ExceptionContinueSearchConst: i32 = 0\n",
		"export const ExceptionContinueExecutionConst: i32 = -1\n"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view lacks %q:\n%s", want, view)
		}
	}
}
