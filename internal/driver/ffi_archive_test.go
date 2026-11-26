// Why: linking foreign code has many quiet failure modes (a pulled member
// that needs a constructor, a DLL planted in the current directory, a
// misleading undefined-symbol message); each needs a pinned test.
// What: TestFFIM_HelloSize and TestFFI_* cases (archives, DLLs, std.dl) for
// those failure modes.
// How: C is written to t.TempDir() and built there, as in ffi_link_test.go.

package driver

import (
	"bytes"
	"context"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"mk/internal/diag"
	"mk/internal/types"
)

// TestFFIM_HelloSize: hello world, which names no link and does not import
// std.dl, has the same size with std/dl in std as without it, on both
// targets in dev and release, and holds none of std.dl's error names.
func TestFFIM_HelloSize(t *testing.T) {
	tools := needTools(t)
	with, without := ffiStdCopy(t), ffiStdCopy(t)
	// Keep the Linux resolver in both trees for the system-libc pthread code.
	for _, p := range []string{"dl", "sys/windows/dl.mk", "sys/nutshell/dl.mk"} {
		if err := os.RemoveAll(filepath.Join(without, filepath.FromSlash(p))); err != nil {
			t.Fatal(err)
		}
	}
	// One cache per std copy: its four builds have keys of their own
	// target and profile.
	caches := map[string]string{with: t.TempDir(), without: t.TempDir()}
	build := func(std string, tg types.Target, prof types.Profile) []byte {
		t.Helper()
		s, err := NewSession(Config{Root: helloRoot(t), StdDir: std, Target: tg, Profile: prof, Tools: tools, CacheDir: caches[std], Jobs: 4})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "hello"+exeSuffix(tg)))
		if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("hello %s %v: %v\n%s", tg.Name, prof, err, r.Report.Text(r.Sources))
		}
		b, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			a, b := build(with, tg, prof), build(without, tg, prof)
			if len(a) != len(b) {
				t.Errorf("%s %v: hello is %d bytes with std.dl in std, %d without", tg.Name, prof, len(a), len(b))
			}
			if bytes.Contains(a, []byte("dl.NotFound")) {
				t.Errorf("%s %v: hello holds std.dl's error names", tg.Name, prof)
			}
		}
	}
}

// TestFFI_VarPull: an archive member that Mk reaches only through an
// extern var is pulled, so its own call into a named DLL is imported:
// the bin reads Counter, and the member's Helper calls Add from
// add.dll.
func TestFFI_VarPull(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest([]string{linkLit("vp", "static", "lib/vp.lib", "x86_64-windows"),
			linkLit("add.dll", "dynamic", "lib/add.dll", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern var Counter: i32", "Counter"),
	})
	cDLL(t, tools, filepath.Join(root, "lib"), "add.dll", addDLL)
	cArchive(t, tools, filepath.Join(root, "lib", "vp.lib"), types.X86_64Windows, []string{
		"__declspec(dllimport) int Add(int a, int b);\nint Counter = 503;\nint Helper(void) { return Add(Counter, 1); }\n"})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if out := runOut(t, exe); out != "503\n" {
			t.Errorf("%v: stdout %q", prof, out)
		}
	}
}

// TestFFI_ImportLibAsStatic: a static link whose .lib is an import
// library (lld-link's, written beside add.dll) is E0202 reason link at
// its path (a vendor's import .lib is not a static link).
func TestFFI_ImportLibAsStatic(t *testing.T) {
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("add", "static", "lib/add.lib", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern Add(a: i32, b: i32): i32", "Add(40, 2)"),
	})
	o := cObject(t, tools, t.TempDir(), "add", "x86_64-pc-windows-msvc", addDLL)
	lib := filepath.Join(root, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	cTool(t, tools, lib, "lld-link", "/dll", "/noentry", "/nodefaultlib", "/machine:x64", "/out:"+filepath.Join(lib, "add.dll"),
		"/implib:"+filepath.Join(lib, "add.lib"), o)
	r := check(t, Config{Root: root})
	got := lines(r)
	if len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") {
		t.Fatalf("want one E0202, got %q\n%s", got, r.Report.Text(r.Sources))
	}
	d := r.Report.Diags[0].Diag
	if fact(d, "reason") != "link" || fact(d, "manifest") != "links[0].path" || !strings.Contains(d.Message, "import library for add.dll") {
		t.Errorf("%s (reason %q at %q)", d.Message, fact(d, "reason"), fact(d, "manifest"))
	}
}

// TestFFI_ExternVarStd: an extern var named like a function of std's own
// kernel32 list is E0001 at its declaration, with no link and with one;
// otherwise it would link to the import thunk's code. An extern var
// naming a data export of a named .dll is E0001 too, not lld's
// undefined symbol. Neither diagnostic carries an empty command fact.
func TestFFI_ExternVarStd(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	for _, links := range [][]string{nil, {linkLit("version.dll", "dynamic", "", "x86_64-windows")}} {
		root := writeTree(t, map[string]string{
			"pkg.mk":      ffiManifest(links),
			"app/main.mk": ffiMain("extern var ExitProcess: i64", "ExitProcess"),
		})
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			r, _ := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{})
			got := lines(r)
			if len(got) != 1 || got[0] != "app/main.mk:1:12:E0001" {
				t.Fatalf("links %q %v: want one E0001 at the var, got %q\n%s", links, prof, got, r.Report.Text(r.Sources))
			}
			d := r.Report.Diags[0].Diag
			if !strings.Contains(d.Message, "extern var ExitProcess would resolve to kernel32.dll") || fact(d, "symbol") != "ExitProcess" {
				t.Errorf("links %q %v: %s", links, prof, d.Message)
			}
			if _, ok := factOK(d, "command"); ok {
				t.Errorf("links %q %v: a command fact on a check that runs no command", links, prof)
			}
		}
	}
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("cnt.dll", "dynamic", "lib/cnt.dll", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern var Count: i32", "Count"),
	})
	cDLL(t, tools, filepath.Join(root, "lib"), "cnt.dll", "__declspec(dllexport) int Count = 5;\n__declspec(dllexport) int Get(void) { return Count; }\n")
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		r, _ := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{})
		got := lines(r)
		if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") || !strings.Contains(r.Report.Diags[0].Diag.Message, "extern var Count would resolve to cnt.dll (links[0])") {
			t.Errorf("%v: a data export: %q\n%s", prof, got, r.Report.Text(r.Sources))
		}
	}
}

// ffiStdCopy is a copy of the std the FFI tests build against (ffiStd).
func ffiStdCopy(t *testing.T) string {
	t.Helper()
	if base := ffiStd(); base != "" {
		dir := filepath.Join(t.TempDir(), "std")
		if err := os.CopyFS(dir, os.DirFS(base)); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	return copyStd(t, nil, nil)
}

// factOK is a fact's text and whether the diagnostic has the fact.
func factOK(d *diag.Diagnostic, name string) (string, bool) {
	f, ok := d.FactValue(name)
	return f.Str, ok
}

// runExeIn runs a Windows executable with dir as its working directory and
// returns its stdout, stderr and exit status.
func runExeIn(t *testing.T, exe, dir string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Dir = dir
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", exe, err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

// TestFFI_CtorTLS: a pulled archive member with a static constructor or
// with thread-local storage is E0001 naming the archive and the member,
// with a hint, on both targets, before the link: an Mk program
// runs no constructor and sets up no TLS, so the member would build and
// misbehave. The four cases run in parallel, each in its own directories,
// and a case's dev and release builds share one cache.
func TestFFI_CtorTLS(t *testing.T) {
	for _, tc := range []struct{ name, src, what string }{
		{"ctor", "static volatile int seed = 41;\nint ready = 1;\n__attribute__((constructor)) static void Init(void) { ready = seed + 1; }\nint Measure(void) { return ready; }\n", "constructors"},
		{"tls", "_Thread_local int t = 5;\nint Measure(void) { return t; }\n", "thread-local storage"},
	} {
		for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
			t.Run(tc.name+" "+tg.Name, func(t *testing.T) {
				t.Parallel()
				root := archiveRoot(t, tg, []string{tc.src})
				cacheDir := t.TempDir()
				for _, prof := range []types.Profile{types.Dev, types.Release} {
					s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: tg, Profile: prof, Tools: needTools(t), CacheDir: cacheDir, Jobs: 4})
					if err != nil {
						t.Fatal(err)
					}
					r, _, err := s.Build(filepath.Join(t.TempDir(), "ffi"+exeSuffix(tg)))
					if err != nil {
						t.Fatal(err)
					}
					got := lines(r)
					if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") {
						t.Fatalf("%v: want one E0001, got %q\n%s", prof, got, r.Report.Text(r.Sources))
					}
					d := r.Report.Diags[0].Diag
					if fact(d, "archive") != "measure (links[0])" || !strings.Contains(fact(d, "member"), "m0") ||
						!strings.Contains(d.Message, tc.what) || !strings.Contains(d.Message, "an Mk program") {
						t.Errorf("%v: %s (archive %q member %q)", prof, d.Message, fact(d, "archive"), fact(d, "member"))
					}
				}
			})
		}
	}
}

// TestFFI_ImpHint: a member calling printf through __declspec(dllimport)
// gives the symbol printf, not lld-link's demangled text, and the C
// runtime hint.
func TestFFI_ImpHint(t *testing.T) {
	root := archiveRoot(t, types.X86_64Windows, []string{
		"__declspec(dllimport) int printf(const char *f, ...);\nint Measure(void) { return printf(\"x\"); }\n"})
	r, _ := ffiBuild(t, root, types.X86_64Windows, types.Dev, TestHooks{})
	got := lines(r)
	if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") {
		t.Fatalf("want one E0001, got %q\n%s", got, r.Report.Text(r.Sources))
	}
	d := r.Report.Diags[0].Diag
	if fact(d, "symbol") != "printf" || fact(d, "archive") != "measure (links[0])" || !strings.Contains(d.Message, "the archive needs a C runtime") {
		t.Errorf("%s (symbol %q archive %q)", d.Message, fact(d, "symbol"), fact(d, "archive"))
	}
}

// TestFFI_PrintfDebug: with --debug, whose link stages the archives
// under fixed names, the E0001 still names the link.
func TestFFI_PrintfDebug(t *testing.T) {
	root := archiveRoot(t, types.X86_64Windows, []string{"int printf(const char *f, ...);\nint Measure(void) { return printf(\"x\"); }\n"})
	s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: types.X86_64Windows, Tools: needTools(t), CacheDir: t.TempDir(), Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.Build(filepath.Join(t.TempDir(), "ffi.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(r); len(got) != 1 || fact(r.Report.Diags[0].Diag, "archive") != "measure (links[0])" {
		t.Errorf("--debug: %q\n%s", got, r.Report.Text(r.Sources))
	}
}

// TestFFI_EachSymbol: a member calling printf and puts, neither of which
// std.libc has, gives one E0001 per symbol, each naming the archive and
// the member, on both targets.
func TestFFI_EachSymbol(t *testing.T) {
	src := "int printf(const char *f, ...);\nint puts(const char *s);\nint Measure(void) { return printf(\"x\") + puts(\"y\"); }\n"
	for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		t.Run(tg.Name, func(t *testing.T) {
			root := archiveRoot(t, tg, []string{src})
			r, _ := ffiBuild(t, root, tg, types.Dev, TestHooks{})
			var syms []string
			for _, e := range r.Report.Diags {
				d := e.Diag
				if d.Code != diag.E0001 || fact(d, "archive") != "measure (links[0])" || !strings.Contains(fact(d, "member"), "m0") {
					t.Errorf("%s (archive %q member %q)", d.Message, fact(d, "archive"), fact(d, "member"))
				}
				syms = append(syms, fact(d, "symbol"))
			}
			slices.Sort(syms)
			if !slices.Equal(syms, []string{"printf", "puts"}) {
				t.Errorf("symbols %q, want printf and puts\n%s", syms, r.Report.Text(r.Sources))
			}
		})
	}
}

// TestFFI_SearchOrder pins the search order for a DLL in the import table
// (no current directory, no PATH, against DLL planting): a bin whose graph
// holds a dynamic Windows link is linked with DependentLoadFlags 0xA00, so a
// DLL named by a .def and found only in the current directory fails to load,
// and the same DLL beside the executable loads, in dev and release. A bin
// with no link has no load configuration (hello world is unchanged;
// TestFFIM_HelloSize).
func TestFFI_SearchOrder(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("zzffiadd.dll", "dynamic", "lib/add.def", "x86_64-windows")}),
		"lib/add.def": "Add\n",
		"app/main.mk": ffiMain("extern Add(a: i32, b: i32): i32", "Add(40, 2)"),
	})
	dlls := t.TempDir()
	dll := cDLL(t, tools, dlls, "zzffiadd.dll", addDLL)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "zzffiadd.dll")); err == nil {
			t.Fatal("a .def link copied a DLL beside the executable")
		}
		if out, _, code := runExeIn(t, exe, dlls); code == 0 {
			t.Errorf("%v: the DLL only in the current directory loaded: exit 0, stdout %q (the search refuses the current directory)", prof, out)
		}
		beside := t.TempDir()
		copyFileT(t, exe, filepath.Join(beside, filepath.Base(exe)))
		copyFileT(t, dll, filepath.Join(beside, "zzffiadd.dll"))
		if out, _, code := runExeIn(t, filepath.Join(beside, filepath.Base(exe)), t.TempDir()); code != 0 || out != "42\n" {
			t.Errorf("%v: the DLL beside the executable: exit %#x, stdout %q", prof, code, out)
		}
	}
	hello := mustBuild(t, writeTree(t, map[string]string{"pkg.mk": ffiManifest(nil), "app/main.mk": ffiMain("", "42")}), types.X86_64Windows, types.Dev)
	if f, err := pe.Open(hello); err != nil {
		t.Fatal(err)
	} else {
		defer f.Close()
		if oh, ok := f.OptionalHeader.(*pe.OptionalHeader64); !ok || oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_LOAD_CONFIG].VirtualAddress != 0 {
			t.Error("a bin with no link has a load configuration")
		}
	}
}

// copyFileT copies src to dst.
func copyFileT(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestFFI_LinuxLibcExample: the reference manual's own system-libc example, the
// libc.so.6 link with a Main that uses std.dl, builds for the host's Linux
// target (linuxT) in system-libc mode (PT_INTERP, DT_NEEDED libc.so.6 only) and, on Linux,
// opens libm.so.6 and closes it, in dev and release. While libcModeBuilt
// is false it is E0202 reason link. If system-libc mode is removed, the
// reference's example goes and so does this test.
func TestFFI_LinuxLibcExample(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{`pkg.Link(name: "libc.so.6", kind: "dynamic", path: "", target: "` + linuxT.Name + `")`}),
		"app/main.mk": "import std.dl\n\nexport Main(sys: &System): WriteError!void {\n    lib := dl.Open(sys.io, \"libm.so.6\", dl.Search.System) else e {\n        try sys.Print(\"{e}\\n\", (e))\n        return\n    }\n    lib.Close(sys.io)\n    try sys.Print(\"open ok\\n\")\n}\n",
	})
	if libcModeBuilt {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			exe := mustBuild(t, root, linuxT, prof)
			if interp, needed := elfDyn(t, exe); interp == "" || !slices.Equal(needed, []string{"libc.so.6"}) {
				t.Errorf("%v: PT_INTERP %q, DT_NEEDED %q", prof, interp, needed)
			}
			if os.Getenv("MK_LINUX") == "1" {
				if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != "open ok\n" {
					t.Errorf("%v: exit %d, stdout %q", prof, code, out)
				}
			}
		}
		return
	}
	r, _ := ffiBuild(t, root, linuxT, types.Dev, TestHooks{})
	got := lines(r)
	if len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") || fact(r.Report.Diags[0].Diag, "reason") != "link" ||
		!strings.Contains(r.Report.Diags[0].Diag.Message, "system-libc mode") {
		t.Errorf("want E0202 reason link naming system-libc mode, got %q\n%s", got, r.Report.Text(r.Sources))
	}
}

// TestFFI_Recipe: tests/programs/ffi/dl, the cookbook's "Load a library
// at run time" with a Main, prints a positive screen width on Windows in
// dev and release; for x86_64-linux it builds static and, run from the
// distribution's /tmp (MK_LINUX=1, under the WSL lock), prints
// Unsupported and exits 3.
func TestFFI_Recipe(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "programs", "ffi", "dl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		if runtime.GOOS == "windows" {
			exe := mustBuild(t, root, types.X86_64Windows, prof)
			if out := strings.TrimSpace(runOut(t, exe)); out == "" || out == "0" || strings.HasPrefix(out, "-") || strings.Contains(out, ".") {
				t.Errorf("%v: ScreenWidth printed %q", prof, out)
			}
		}
		exe := mustBuild(t, root, types.X86_64Linux, prof)
		if os.Getenv("MK_LINUX") == "1" {
			if out, code := runLinuxTmp(t, exe); code != 3 || !strings.Contains(out, "Unsupported") {
				t.Errorf("%v linux: exit %d, stdout %q", prof, code, out)
			}
		}
	}
}

// TestFFI_InitFailed: a DLL whose DllMain returns FALSE, built here from
// C, opened by absolute Path, is dl.InitFailed (Windows error 1114),
// in dev and release.
func TestFFI_InitFailed(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	dir := t.TempDir()
	o := cObject(t, tools, dir, "bad", "x86_64-pc-windows-msvc",
		"int DllMain(void *h, unsigned long reason, void *p) { return 0; }\n__declspec(dllexport) int Add(int a, int b) { return a + b; }\n")
	dll := filepath.Join(dir, "bad.dll")
	cTool(t, tools, dir, "lld-link", "/dll", "/entry:DllMain", "/nodefaultlib", "/machine:x64", "/noimplib", "/out:"+dll, o)
	root := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest(nil),
		"app/main.mk": "import std.dl\n\nexport Main(sys: &System): WriteError!void {\n" +
			"    lib := dl.Open(sys.io, \"" + strings.ReplaceAll(dll, `\`, `\\`) + "\", dl.Search.Path) else e {\n" +
			"        try sys.Print(\"{e}\\n\", (e))\n        return\n    }\n    lib.Close(sys.io)\n    try sys.Print(\"opened\\n\")\n}\n",
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if out := runOut(t, exe); !strings.Contains(out, "InitFailed") {
			t.Errorf("%v: stdout %q, want InitFailed", prof, out)
		}
	}
}
