// Why: a package's `links` (DLLs, import lists, static archives) change the
// link line, the import table and extern resolution; a manifest with no
// links must link exactly as hello world always has.
// What: TestFFIM_Validate, TestFFIM_NoLink, TestFFIM_Hello, the TestFFI_*
// tests of dynamic links and static archives, and the C helpers
// cObject and cArchive.
// How: C is written to t.TempDir() and built there with the discovered clang
// and lld or llvm-lib; the repository holds no C file (TestNoCSource).

package driver

import (
	"context"
	"debug/elf"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"mk/internal/toolchain"
	"mk/internal/types"
)

// linkLit is a pkg.Link literal.
func linkLit(name, kind, path, target string) string {
	return `pkg.Link(name: "` + name + `", kind: "` + kind + `", path: "` + path + `", target: "` + target + `")`
}

// ffiManifest is a package "ffi" with one bin "ffi" rooted at app, app
// listed in unsafe_modules, and the given links and c_sources.
func ffiManifest(links []string, csources ...string) string {
	return manifest(map[string]string{"name": `"ffi"`,
		"bins":           `[pkg.Bin(name: "ffi", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
		"unsafe_modules": `["app"]`, "links": "[" + strings.Join(links, ", ") + "]",
		"c_sources": "[" + strings.Join(csources, ", ") + "]"})
}

// ffiMain is an app/main.mk whose Main prints the int expression expr,
// computed inside unsafe, after the declarations decls.
func ffiMain(decls, expr string) string {
	return decls + `

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    var n := 0
    unsafe "a C function of this test" {
        n = int(` + expr + `)
    }
    try Print(mut out, "{n}\n", (n: n))
}
`
}

// ffiBuild builds the package at root for target and profile into a new
// directory and returns the result and the executable ("" on failure).
func ffiBuild(t *testing.T, root string, target types.Target, prof types.Profile, hooks TestHooks) (Result, string) {
	t.Helper()
	tools := needTools(t)
	s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: target, Profile: prof, Tools: tools, CacheDir: t.TempDir(), Jobs: 4, TestHooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "ffi"+exeSuffix(target)))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		return r, ""
	}
	return r, paths[0]
}

// ffiStd is MK_FFI_STD, a std snapshot the FFI tests build against while
// a std tree in the worktree is mid-edit (as std's MK_DL_BASE); "" is the embedded
// std, which the gate uses.
func ffiStd() string { return os.Getenv("MK_FFI_STD") }

// mustBuild is ffiBuild that fails the test on a diagnostic.
func mustBuild(t *testing.T, root string, target types.Target, prof types.Profile) string {
	t.Helper()
	r, exe := ffiBuild(t, root, target, prof, TestHooks{})
	if exe == "" {
		t.Fatalf("build %s %v:\n%s", target.Name, prof, r.Report.Text(r.Sources))
	}
	return exe
}

// imports is an executable's import table as "dll:name" pairs, sorted
// (debug/pe's ImportedSymbols writes "name:dll").
func imports(t *testing.T, exe string) []string {
	t.Helper()
	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range syms {
		name, dll, _ := strings.Cut(s, ":")
		out = append(out, dll+":"+name)
	}
	slices.Sort(out)
	return out
}

// importsFrom is the names an executable imports from dll (exact file
// name).
func importsFrom(t *testing.T, exe, dll string) []string {
	var out []string
	for _, s := range imports(t, exe) {
		if d, n, _ := strings.Cut(s, ":"); d == dll {
			out = append(out, n)
		}
	}
	return out
}

// dlls is the DLL file names of an import table, sorted, as written.
func dlls(t *testing.T, exe string) []string {
	var out []string
	for _, s := range imports(t, exe) {
		d, _, _ := strings.Cut(s, ":")
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// needWindows skips a test that runs a Windows executable or reads
// System32 on another host.
func needWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("runs a Windows executable")
	}
}

// cTool runs one LLVM tool of tools' directory with args in dir.
func cTool(t *testing.T, tools *toolchain.Tools, dir, tool string, args ...string) {
	t.Helper()
	exe := filepath.Join(tools.Dir, tool)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		notRun(t, "no %s beside the discovered clang (the gate fails on this skip)", tool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %q: %v\n%s", tool, args, err, out)
	}
}

// cObject compiles the C text src for triple into dir/<name>.o with no C
// runtime (-ffreestanding -fno-stack-protector) plus extra flags.
func cObject(t *testing.T, tools *toolchain.Tools, dir, name, triple, src string, extra ...string) string {
	t.Helper()
	c := filepath.Join(dir, name+".c")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	o := filepath.Join(dir, name+".o")
	args := append([]string{"--target=" + triple, "-O2", "-ffreestanding", "-fno-stack-protector", "-c", c, "-o", o}, extra...)
	cTool(t, tools, dir, "clang", args...)
	return o
}

// cDLL builds a DLL named name from the C text src (each exported function
// marked __declspec(dllexport)) into dir, with no entry point and no C
// runtime, and returns its path.
func cDLL(t *testing.T, tools *toolchain.Tools, dir, name, src string) string {
	t.Helper()
	stem := strings.TrimSuffix(name, ".dll")
	o := cObject(t, tools, dir, stem, "x86_64-pc-windows-msvc", src)
	out := filepath.Join(dir, name)
	cTool(t, tools, dir, "lld-link", "/dll", "/noentry", "/nodefaultlib", "/machine:x64", "/out:"+out, o)
	return out
}

// cArchive builds a static archive at out from the C texts srcs (one
// member each, named m0, m1, ...) for target: llvm-lib for a .lib,
// llvm-ar for a .a.
func cArchive(t *testing.T, tools *toolchain.Tools, out string, target types.Target, srcs []string, extra ...string) {
	t.Helper()
	dir := t.TempDir()
	triple := "x86_64-pc-windows-msvc"
	if target.OS == types.OSLinux {
		triple = "x86_64-unknown-linux-gnu"
	}
	var objs []string
	for i, src := range srcs {
		objs = append(objs, cObject(t, tools, dir, "m"+string(rune('0'+i)), triple, src, extra...))
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if target.OS == types.OSWindows {
		cTool(t, tools, dir, "llvm-lib", append([]string{"/out:" + out}, objs...)...)
	} else {
		cTool(t, tools, dir, "llvm-ar", append([]string{"rcs", out}, objs...)...)
	}
}

// runOut runs a Windows executable and returns its stdout, failing on a
// non-zero status.
func runOut(t *testing.T, exe string, args ...string) string {
	t.Helper()
	out, errOut, code := runExe(t, types.X86_64Windows, exe, "", args...)
	if code != 0 {
		t.Fatalf("%s: exit %d, stdout %q, stderr %q", filepath.Base(exe), code, out, errOut)
	}
	return out
}

// TestFFIM_Validate: each fault of a Link's fields is E0202 reason link at
// the Link's field; good links check clean.
func TestFFIM_Validate(t *testing.T) {
	const w, l = "x86_64-windows", "x86_64-linux"
	for _, tc := range []struct {
		name, link, field string
	}{
		{"empty name", linkLit("", "dynamic", "", w), "links[0].name"},
		{"separator", linkLit(`lib/x.dll`, "dynamic", "", w), "links[0].name"},
		{"backslash", linkLit(`lib\\x.dll`, "dynamic", "", w), "links[0].name"},
		{"kind", linkLit("x.dll", "shared", "", w), "links[0].kind"},
		{"empty target", linkLit("x.dll", "dynamic", "", ""), "links[0].target"},
		{"other target", linkLit("x.dll", "dynamic", "", "x86_64-freestanding"), "links[0].target"},
		{"static no path", linkLit("x", "static", "", w), "links[0].path"},
		{"missing file", linkLit("x", "static", "lib/none.lib", w), "links[0].path"},
		{"import lib as dynamic", linkLit("x.dll", "dynamic", "lib/x.lib", w), "links[0].path"},
		{"so path", linkLit("libx.so", "dynamic", "lib/libx.so", l), "links[0].path"},
		{"other target's archive", linkLit("x", "static", "lib/x.lib", l), "links[0].path"},
		{"absolute", linkLit("x", "static", "/x.a", l), "links[0].path"},
		{"dotdot", linkLit("x", "static", "../x.a", l), "links[0].path"},
		{"bad def", linkLit("x.dll", "dynamic", "lib/bad.def", w), "links[0].path"},
		{"lazy on linux", linkLit("libx.so", "lazy", "", l), "links[0].kind"},
		{"lazy on aarch64", linkLit("libx.so", "lazy", "", "aarch64-linux"), "links[0].kind"},
		{"lazy on macos", linkLit("libx.dylib", "lazy", "", "x86_64-macos"), "links[0].kind"},
		{"lazy separator", linkLit(`lib/x.dll`, "lazy", "", w), "links[0].name"},
		{"lazy lib path", linkLit("x.dll", "lazy", "lib/x.lib", w), "links[0].path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{
				"pkg.mk":      ffiManifest([]string{tc.link}),
				"app/main.mk": "export Main(sys: &System) {\n}\n",
				"lib/x.lib":   "!<arch>\n",
				"lib/bad.def": "Good\nnot one\n",
			})
			r := check(t, Config{Root: root, StdDir: fakeStd(t)})
			got := lines(r)
			if len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") || !strings.HasPrefix(got[0], "pkg.mk:") {
				t.Fatalf("want one E0202 in pkg.mk, got %q\n%s", got, r.Report.Text(r.Sources))
			}
			d := r.Report.Diags[0].Diag
			if fact(d, "reason") != "link" || fact(d, "manifest") != tc.field {
				t.Errorf("reason %q at %q, want link at %s: %s", fact(d, "reason"), fact(d, "manifest"), tc.field, d.Message)
			}
		})
	}
	root := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest([]string{linkLit("version.dll", "dynamic", "", w), linkLit("x.dll", "dynamic", "lib/x.def", w), linkLit("z.dll", "lazy", "lib/x.def", w),
			linkLit("x", "static", "lib/x.lib", w), linkLit("libm.so.6", "dynamic", "lib/m.def", l), linkLit("y", "static", "lib/y.a", l)}),
		"app/main.mk": "export Main(sys: &System) {\n}\n",
		"lib/x.def":   "Add\r\nSub\n",
		"lib/x.lib":   "!<arch>\n",
		"lib/m.def":   "cos@GLIBC_2.2.5\nhypot\n",
		"lib/y.a":     "!<arch>\n",
	})
	clean(t, check(t, Config{Root: root, StdDir: fakeStd(t)}))
}

// Hello world's link lines from before links existed, captured with the
// driver of tag v1.1.0 (the same normalization, Jobs 4): a path is <ext>,
// a run of objects <o>.... The Windows lines gain /osversion:10.0 (so
// Windows reports system file versions as 10.0), and the dev Windows line
// /opt:noicf (a dev link skips identical-code folding).
var helloLinkGolden = map[string]string{
	"x86_64-windows dev":     "/nologo /nodefaultlib /entry:mk_start /subsystem:console /osversion:10.0 /machine:x64 /Brepro /out:<path> <o>... <lib> /opt:noicf /stack:8388608",
	"x86_64-windows release": "/nologo /nodefaultlib /entry:mk_start /subsystem:console /osversion:10.0 /machine:x64 /Brepro /out:<path> <o>... <lib> /mllvm:-align-loops=32 /mllvm:-unroll-threshold=400 /opt:ref /opt:icf /stack:8388608 /opt:lldlto=3 /lldltocache:<path> /lldltocachepolicy:cache_size_bytes=536870912:prune_after=48h /opt:lldltojobs=4",
	"x86_64-linux dev":       "-static -nostdlib -e _start --build-id=none -z noexecstack -o <path> <o>...",
	"x86_64-linux release":   "-static -nostdlib -e _start --build-id=none -z noexecstack -o <path> <o>... -mllvm -align-loops=32 -mllvm -unroll-threshold=400 --gc-sections --strip-all --lto-O3 --thinlto-cache-dir=<path> --thinlto-cache-policy=cache_size_bytes=536870912:prune_after=48h --thinlto-jobs=4",
}

// Hello world's import table from before links existed (kernel32.dll
// only), dev and release, read with llvm-readobj --coff-imports from the
// mk build of tag v1.1.0, with TerminateProcess since (sys.FatalExit).
var (
	helloImportsDev = []string{"AddVectoredExceptionHandler", "CloseHandle", "CreateEventW", "CreateFileW", "CreateSemaphoreW",
		"DeleteFileW", "ExitProcess", "FreeEnvironmentStringsW", "GetCommandLineW", "GetConsoleMode", "GetCurrentProcessId",
		"GetCurrentThreadId", "GetCurrentThreadStackLimits", "GetEnvironmentStringsW", "GetEnvironmentVariableW",
		"GetFileAttributesW", "GetFileType", "GetFullPathNameW", "GetLastError", "GetModuleFileNameW", "GetModuleHandleW",
		"GetNamedPipeInfo", "GetOverlappedResult", "GetStdHandle", "MoveFileExW", "QueryPerformanceCounter",
		"QueryPerformanceFrequency", "ReadFile", "RemoveDirectoryW", "RtlLookupFunctionEntry", "RtlVirtualUnwind",
		"SetFileInformationByHandle", "SetFilePointerEx", "SetThreadStackGuarantee", "SetUnhandledExceptionFilter", "Sleep",
		"SwitchToThread", "TerminateProcess", "VirtualAlloc", "VirtualFree", "WaitForSingleObject", "WriteConsoleW",
		"WriteFile"}
	helloImportsRelease = []string{"AddVectoredExceptionHandler", "CloseHandle", "CreateEventW", "CreateSemaphoreW",
		"ExitProcess", "FreeEnvironmentStringsW", "GetCommandLineW", "GetConsoleMode", "GetCurrentThreadId",
		"GetCurrentThreadStackLimits", "GetEnvironmentStringsW", "GetFileType", "GetLastError", "GetModuleHandleW",
		"GetNamedPipeInfo", "GetOverlappedResult", "GetStdHandle", "ReadFile", "RtlLookupFunctionEntry", "RtlVirtualUnwind",
		"SetFilePointerEx", "SetThreadStackGuarantee", "SetUnhandledExceptionFilter", "Sleep", "SwitchToThread",
		"TerminateProcess", "VirtualAlloc", "VirtualFree", "WaitForSingleObject", "WriteConsoleW", "WriteFile"}
)

// normLinkArg is one link argument with its paths replaced as the golden
// strings write them.
func normLinkArg(a string) string {
	for _, p := range []string{"/out:", "/lldltocache:", "--thinlto-cache-dir="} {
		if strings.HasPrefix(a, p) {
			return p + "<path>"
		}
	}
	// A lld-link option such as /nologo is absolute to filepath on a
	// Unix host: a path that starts with '/' needs a second '/'.
	if filepath.IsAbs(a) && !strings.HasPrefix(a, "/") || strings.HasPrefix(a, "/") && strings.Count(a, "/") > 1 {
		ext := filepath.Ext(a)
		if ext == "" {
			return "<path>"
		}
		return "<" + ext[1:] + ">"
	}
	return a
}

// normLinkLine is a link's arguments normalized, a run of one path kind
// written once with "...".
func normLinkLine(args []string) string {
	var out []string
	for _, a := range args {
		a = normLinkArg(a)
		n := len(out)
		switch {
		case n > 0 && out[n-1] == a+"...":
		case n > 0 && out[n-1] == a && strings.HasPrefix(a, "<"):
			out[n-1] = a + "..."
		default:
			out = append(out, a)
		}
	}
	return strings.Join(out, " ")
}

// helloRoot is tests/programs/hello.mk.
func helloRoot(t *testing.T) string {
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "programs", "hello.mk"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestFFIM_NoLink: with no links, hello world's link line on both targets,
// dev and release, is the one from before links existed: the archives and
// named DLLs add nothing to a link that has none, and std.libc is not
// linked.
func TestFFIM_NoLink(t *testing.T) {
	for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			key := tg.Name + " " + prof.String()
			t.Run(key, func(t *testing.T) {
				var job toolchain.LinkJob
				var lls []string
				r, exe := ffiBuild(t, helloRoot(t), tg, prof, TestHooks{OnLink: func(j toolchain.LinkJob) { job = j },
					OnLL: func(_, ll string) { lls = append(lls, ll) }})
				if exe == "" {
					t.Fatalf("build:\n%s", r.Report.Text(r.Sources))
				}
				args, err := toolchain.LinkArgs(job)
				if err != nil {
					t.Fatal(err)
				}
				if got := normLinkLine(args); got != helloLinkGolden[key] {
					t.Errorf("link line\n got %s\nwant %s", got, helloLinkGolden[key])
				}
				if len(job.Archives) != 0 {
					t.Errorf("archives %q", job.Archives)
				}
				for _, ll := range lls {
					for _, line := range strings.Split(ll, "\n") {
						if strings.HasPrefix(line, "define ") && strings.Contains(line, "@malloc(") {
							t.Errorf("std.libc's malloc is defined with no C source and no static link: %s", line)
						}
					}
				}
			})
		}
	}
}

// TestFFIM_Hello: hello world's import table is helloImportsDev or
// helloImportsRelease exactly; a Linux hello world is static, with no PT_INTERP and no
// PT_DYNAMIC. Its size is TestHelloWorldWithinBudget's.
func TestFFIM_Hello(t *testing.T) {
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run("windows "+prof.String(), func(t *testing.T) {
			exe := mustBuild(t, helloRoot(t), types.X86_64Windows, prof)
			want := helloImportsDev
			if prof == types.Release {
				want = helloImportsRelease
			}
			if got := dlls(t, exe); !slices.Equal(got, []string{"kernel32.dll"}) {
				t.Errorf("DLLs %q, want kernel32.dll only", got)
			}
			if got := importsFrom(t, exe, "kernel32.dll"); !slices.Equal(got, want) {
				t.Errorf("kernel32 imports\n got %q\nwant %q", got, want)
			}
		})
		t.Run("linux "+prof.String(), func(t *testing.T) {
			exe := mustBuild(t, helloRoot(t), types.X86_64Linux, prof)
			f, err := elf.Open(exe)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			for _, p := range f.Progs {
				if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
					t.Errorf("Linux hello has %v", p.Type)
				}
			}
		})
	}
}

// systemPath is %SystemRoot%\System32\<name> as an Mk string literal's
// text.
func systemPath(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.ReplaceAll(filepath.Join(root, "System32", name), `\`, `\\`)
}

// versionMain calls version.dll's GetFileVersionInfoSizeA on kernel32.dll.
func versionMain() string {
	return ffiMain(`import std.raw

extern GetFileVersionInfoSizeA(name: *u8, handle: *u32): u32`,
		`GetFileVersionInfoSizeA(raw.StrData("`+systemPath("kernel32.dll")+`\0"), raw.OfMut(mut h))`)
}

// withHandle declares the u32 out parameter versionMain's call takes.
func withHandle(src string) string {
	return strings.Replace(src, "    var n := 0\n", "    var n := 0\n    var h := u32(0)\n", 1)
}

// TestFFI_System: a link to version.dll, outside the implicit ten, with
// path "": the bin calls GetFileVersionInfoSizeA (result > 0) and imports
// it from version.dll under exactly the name the link gives (the name is
// written unchanged, not as VERSION.dll), in dev and release.
func TestFFI_System(t *testing.T) {
	needWindows(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("version.dll", "dynamic", "", "x86_64-windows")}),
		"app/main.mk": withHandle(versionMain()),
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := importsFrom(t, exe, "version.dll"); !slices.Equal(got, []string{"GetFileVersionInfoSizeA"}) {
			t.Errorf("%v: version.dll imports %q (all: %q)", prof, got, imports(t, exe))
		}
		if out := strings.TrimSpace(runOut(t, exe)); out == "0" || out == "" {
			t.Errorf("%v: GetFileVersionInfoSizeA gave %q", prof, out)
		}
	}
}

// TestFFI_Def: the same through a .def the package ships, with the host's
// System32 out of reach of the links and the implicit set empty
// (testNoSystem32, as systemExports is off Windows), so it builds as on a
// host that is not Windows; a "" path is then E0202 reason link.
func TestFFI_Def(t *testing.T) {
	needWindows(t)
	testNoSystem32 = true
	defer func() { testNoSystem32 = false }()
	root := writeTree(t, map[string]string{
		"pkg.mk":          ffiManifest([]string{linkLit("version.dll", "dynamic", "lib/version.def", "x86_64-windows")}),
		"lib/version.def": "GetFileVersionInfoSizeA\nGetFileVersionInfoSizeW\n",
		"app/main.mk":     withHandle(versionMain()),
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := importsFrom(t, exe, "version.dll"); !slices.Equal(got, []string{"GetFileVersionInfoSizeA"}) {
			t.Errorf("%v: version.dll imports %q", prof, got)
		}
		if out := strings.TrimSpace(runOut(t, exe)); out == "0" {
			t.Errorf("%v: GetFileVersionInfoSizeA gave 0", prof)
		}
	}
	writeFiles(t, root, map[string]string{"pkg.mk": ffiManifest([]string{linkLit("version.dll", "dynamic", "", "x86_64-windows")})})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		r, _ := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{})
		if got := lines(r); len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") || fact(r.Report.Diags[0].Diag, "reason") != "link" {
			t.Errorf("%v: a \"\" path off Windows: want E0202 reason link, got %q\n%s", prof, got, r.Report.Text(r.Sources))
		}
	}
}

// addDLL is the C of a DLL exporting Add.
const addDLL = "__declspec(dllexport) int Add(int a, int b) { return a + b; }\n"

// TestFFI_Dll: a DLL built here from C, named by its .dll path: mk build
// copies it beside the executable, which runs; mk run (the cache's
// executable directory) works too; the import table takes Add from it.
func TestFFI_Dll(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("add.dll", "dynamic", "lib/add.dll", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern Add(a: i32, b: i32): i32", "Add(40, 2)"),
	})
	cDLL(t, tools, filepath.Join(root, "lib"), "add.dll", addDLL)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "add.dll")); err != nil {
			t.Fatalf("%v: add.dll not beside the executable: %v", prof, err)
		}
		if got := importsFrom(t, exe, "add.dll"); !slices.Equal(got, []string{"Add"}) {
			t.Errorf("%v: add.dll imports %q", prof, got)
		}
		if out := runOut(t, exe); out != "42\n" {
			t.Errorf("%v: stdout %q", prof, out)
		}
	}
	s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r, code, err := s.Run(nil, RunIO{Stdout: &out})
	if err != nil || code != 0 || out.String() != "42\n" {
		t.Errorf("mk run: err %v, exit %d, stdout %q\n%s", err, code, out.String(), r.Report.Text(r.Sources))
	}
	// A path whose base name differs from the link's name: the copy takes
	// the name the import table holds, so the loader finds it.
	if err := os.Rename(filepath.Join(root, "lib", "add.dll"), filepath.Join(root, "lib", "add-1.2.dll")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"pkg.mk": ffiManifest([]string{linkLit("add.dll", "dynamic", "lib/add-1.2.dll", "x86_64-windows")})})
	exe := mustBuild(t, root, types.X86_64Windows, types.Dev)
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "add-1.2.dll")); err == nil {
		t.Errorf("copied under the path's base name")
	}
	if out := runOut(t, exe); out != "42\n" {
		t.Errorf("renamed path: stdout %q", out)
	}
}

// TestFFI_PullAcross: a member of the first archive that only a member
// of the second references is pulled, so its call into a named DLL
// resolves there (the pull runs to a fixed point over every archive).
func TestFFI_PullAcross(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest([]string{linkLit("inner", "static", "lib/inner.lib", "x86_64-windows"),
			linkLit("outer", "static", "lib/outer.lib", "x86_64-windows"), linkLit("add.dll", "dynamic", "lib/add.dll", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern Measure(): i32", "Measure()"),
	})
	cDLL(t, tools, filepath.Join(root, "lib"), "add.dll", addDLL)
	cArchive(t, tools, filepath.Join(root, "lib", "inner.lib"), types.X86_64Windows,
		[]string{"__declspec(dllimport) int Add(int a, int b);\nint Inner(void) { return Add(40, 2); }\n"})
	cArchive(t, tools, filepath.Join(root, "lib", "outer.lib"), types.X86_64Windows,
		[]string{"int Inner(void);\nint Measure(void) { return Inner() + 1; }\n"})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if out := runOut(t, exe); out != "43\n" {
			t.Errorf("%v: stdout %q", prof, out)
		}
	}
}

// TestFFI_Order: extern resolution order. A name a named DLL and an implicit system
// DLL both export (MulDiv, kernel32's) is taken from the named one; of two
// named links exporting one name, the first in manifest order wins; a
// dependency's link applies to the root's bin.
func TestFFI_Order(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	ws := t.TempDir()
	lib := filepath.Join(ws, "app", "lib")
	cDLL(t, tools, lib, "mul.dll", "__declspec(dllexport) int MulDiv(int a, int b, int c) { return 7; }\n")
	cDLL(t, tools, lib, "first.dll", "__declspec(dllexport) int Pick(void) { return 1; }\n")
	cDLL(t, tools, lib, "second.dll", "__declspec(dllexport) int Pick(void) { return 2; }\n")
	cDLL(t, tools, filepath.Join(ws, "dep", "lib"), "deplib.dll", "__declspec(dllexport) int DepFn(void) { return 300; }\n")
	const w = "x86_64-windows"
	appMan := manifest(map[string]string{"name": `"ffi"`,
		"bins":           `[pkg.Bin(name: "ffi", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
		"unsafe_modules": `["app"]`, "deps": "[" + pathDep("dep", "../dep") + "]",
		"links": "[" + strings.Join([]string{linkLit("mul.dll", "dynamic", "lib/mul.dll", w),
			linkLit("first.dll", "dynamic", "lib/first.dll", w), linkLit("second.dll", "dynamic", "lib/second.dll", w)}, ", ") + "]"})
	writeFiles(t, ws, map[string]string{
		"app/pkg.mk": appMan,
		"app/app/main.mk": ffiMain("import dep.src\n\nextern MulDiv(a: i32, b: i32, c: i32): i32\nextern Pick(): i32",
			"int(MulDiv(6, 7, 1))*1000 + int(Pick())*10000 + src.Dep()"),
		"dep/pkg.mk": manifest(map[string]string{"name": `"dep"`, "unsafe_modules": `["src"]`,
			"links": "[" + linkLit("deplib.dll", "dynamic", "lib/deplib.dll", w) + "]"}),
		"dep/src/d.mk": "extern DepFn(): i32\n\nexport Dep(): int {\n    var n := 0\n    unsafe \"a C function\" {\n        n = int(DepFn())\n    }\n    return n\n}\n",
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, filepath.Join(ws, "app"), types.X86_64Windows, prof)
		if out := runOut(t, exe); out != "17300\n" {
			t.Errorf("stdout %q, want 17300 (MulDiv 7 from mul.dll, Pick 1 from first.dll, DepFn 300)", out)
		}
		if got := importsFrom(t, exe, "mul.dll"); !slices.Equal(got, []string{"MulDiv"}) {
			t.Errorf("mul.dll imports %q", got)
		}
		if got := importsFrom(t, exe, "second.dll"); len(got) != 0 {
			t.Errorf("second.dll imports %q, want none", got)
		}
		if slices.Contains(importsFrom(t, exe, "kernel32.dll"), "MulDiv") {
			t.Errorf("MulDiv imported from kernel32")
		}
		for _, d := range []string{"mul.dll", "first.dll", "deplib.dll"} {
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), d)); err != nil {
				t.Errorf("%s not copied: %v", d, err)
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "second.dll")); err == nil {
			t.Errorf("second.dll, unused, was copied")
		}
	}
}

// TestFFI_Unused: a link nothing uses adds no import, no import library
// and no archive; the link line is hello's.
func TestFFI_Unused(t *testing.T) {
	needWindows(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":        ffiManifest([]string{linkLit("version.dll", "dynamic", "", "x86_64-windows"), linkLit("winmm.dll", "dynamic", "lib/winmm.def", "x86_64-windows")}),
		"lib/winmm.def": "timeGetTime\n",
		"app/main.mk":   ffiMain("", "6 * 7"),
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		var job toolchain.LinkJob
		r, exe := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{OnLink: func(j toolchain.LinkJob) { job = j }})
		if exe == "" {
			t.Fatalf("%v: build:\n%s", prof, r.Report.Text(r.Sources))
		}
		if got := dlls(t, exe); !slices.Equal(got, []string{"kernel32.dll"}) {
			t.Errorf("%v: DLLs %q, want kernel32.dll only", prof, got)
		}
		if len(job.Libs) != 1 || len(job.Archives) != 0 {
			t.Errorf("%v: libs %q, archives %q: want kernel32's only", prof, job.Libs, job.Archives)
		}
	}
}

// TestFFI_Errors: an extern var a named DLL exports is E0001 (only
// functions resolve to a library); an extern no library supplies is
// E0001 naming the symbol and the libraries searched.
func TestFFI_Errors(t *testing.T) {
	needWindows(t)
	root := writeTree(t, map[string]string{
		"pkg.mk":       ffiManifest([]string{linkLit("data.dll", "dynamic", "lib/data.def", "x86_64-windows")}),
		"lib/data.def": "Counter\nGetCount\n",
		"app/main.mk":  ffiMain("extern var Counter: i32", "*(&Counter)"),
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		r, _ := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{})
		if got := lines(r); len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") || !strings.Contains(r.Report.Diags[0].Diag.Message, "extern var Counter") {
			t.Errorf("%v: extern var: want one E0001 naming it, got %q\n%s", prof, got, r.Report.Text(r.Sources))
		}
	}
	writeFiles(t, root, map[string]string{"app/main.mk": ffiMain("extern NoSuchFunction(): i32", "NoSuchFunction()")})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		r, _ := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{})
		got := lines(r)
		if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") {
			t.Fatalf("%v: unresolved: want one E0001, got %q\n%s", prof, got, r.Report.Text(r.Sources))
		}
		d := r.Report.Diags[0].Diag
		if fact(d, "symbol") != "NoSuchFunction" || !strings.Contains(d.Message, "data.dll (links[0])") {
			t.Errorf("%v: unresolved: %s (symbol %q)", prof, d.Message, fact(d, "symbol"))
		}
	}
}

// strlenMalloc is the C of an archive member calling strlen and malloc,
// std.libc's.
const strlenMalloc = `typedef unsigned long long size_t;
size_t strlen(const char *s);
void *malloc(size_t n);
void free(void *p);
int Measure(void) {
    char *p = malloc(64);
    if (!p) return -1;
    p[0] = 'h'; p[1] = 'e'; p[2] = 'l'; p[3] = 'l'; p[4] = 'o'; p[5] = 0;
    int n = (int)strlen(p);
    free(p);
    return n;
}
`

// archiveRoot is a package whose static link names lib/<file>, built from
// srcs for target, and whose Main prints Measure().
func archiveRoot(t *testing.T, target types.Target, srcs []string, extra ...string) string {
	t.Helper()
	tools := needTools(t)
	file, ext := "m.lib", "lib/m.lib"
	if target.OS == types.OSLinux {
		file, ext = "libm1.a", "lib/libm1.a"
	}
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("measure", "static", ext, target.Name)}),
		"app/main.mk": ffiMain("extern Measure(): i32", "Measure()"),
	})
	cArchive(t, tools, filepath.Join(root, "lib", file), target, srcs, extra...)
	return root
}

// TestFFI_Good: an archive whose member calls strlen and malloc links
// against std.libc and runs, on Windows in dev and release (the release
// link goes through thinLink).
func TestFFI_Good(t *testing.T) {
	needWindows(t)
	root := archiveRoot(t, types.X86_64Windows, []string{strlenMalloc})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		var job toolchain.LinkJob
		r, exe := ffiBuild(t, root, types.X86_64Windows, prof, TestHooks{OnLink: func(j toolchain.LinkJob) { job = j }})
		if exe == "" {
			t.Fatalf("%v:\n%s", prof, r.Report.Text(r.Sources))
		}
		if len(job.Archives) != 1 || filepath.Base(job.Archives[0]) != "m.lib" {
			t.Errorf("%v: archives %q", prof, job.Archives)
		}
		if out := runOut(t, exe); out != "5\n" {
			t.Errorf("%v: stdout %q", prof, out)
		}
	}
	// --debug: pdbLink stages the archives beside the objects.
	s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: types.X86_64Windows, Tools: needTools(t), CacheDir: t.TempDir(), Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "ffi.exe"))
	if err != nil || len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("--debug: %v\n%s", err, r.Report.Text(r.Sources))
	}
	if out := runOut(t, paths[0]); out != "5\n" {
		t.Errorf("--debug: stdout %q", out)
	}
	if _, err := os.Stat(strings.TrimSuffix(paths[0], ".exe") + ".pdb"); err != nil {
		t.Errorf("--debug: no PDB: %v", err)
	}
}

// TestFFI_GoodLinux: the Linux half of TestFFI_Good, run from the
// distribution's /tmp under the WSL lock (MK_LINUX=1, go test -p 1).
func TestFFI_GoodLinux(t *testing.T) {
	root := archiveRoot(t, types.X86_64Linux, []string{strlenMalloc})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Linux, prof)
		f, err := elf.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
				t.Errorf("%v: a static archive made the binary dynamic (%v)", prof, p.Type)
			}
		}
		f.Close()
		if out, code := runLinuxTmp(t, exe); code != 0 || out != "5\n" {
			t.Errorf("%v: exit %d, stdout %q", prof, code, out)
		}
	}
}

// runLinuxTmp copies a Linux executable to the distribution's /tmp, runs it
// there and removes it (Linux programs of these tests run from /tmp).
// Skips without MK_LINUX=1; wsl.exe must run under the lock.
func runLinuxTmp(t *testing.T, exe string, args ...string) (string, int) {
	t.Helper()
	prefix := runner(t, types.X86_64Linux)
	tmp := "/tmp/mk-ffi8-" + strings.ReplaceAll(filepath.Base(filepath.Dir(exe)), " ", "_") + "-" + filepath.Base(exe)
	sh := `src="$1"; dst="$2"; shift 2; cp "$src" "$dst" && chmod +x "$dst" && "$dst" "$@"; rc=$?; rm -f "$dst"; exit $rc`
	argv := append(append([]string(nil), prefix...), "sh", "-c", sh, "sh", toolchain.HostPathFor(prefix, exe), tmp)
	argv = append(argv, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var o strings.Builder
	cmd.Stdout = &o
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%q: %v", argv, err)
		}
		code = ee.ExitCode()
	}
	return o.String(), code
}

// TestFFI_Printf: an archive member calling printf, which std.libc lacks,
// is E0001 naming the archive, the member and printf, on both targets
// (only the link runs, so no WSL).
func TestFFI_Printf(t *testing.T) {
	src := "int printf(const char *f, ...);\nint Measure(void) { return printf(\"x\"); }\n"
	for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		t.Run(tg.Name, func(t *testing.T) {
			root := archiveRoot(t, tg, []string{src})
			r, _ := ffiBuild(t, root, tg, types.Dev, TestHooks{})
			got := lines(r)
			if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") {
				t.Fatalf("want one E0001, got %q\n%s", got, r.Report.Text(r.Sources))
			}
			d := r.Report.Diags[0].Diag
			if fact(d, "symbol") != "printf" || !strings.Contains(fact(d, "archive"), "measure") ||
				!strings.Contains(fact(d, "member"), "m0") || !strings.Contains(d.Message, "printf") {
				t.Errorf("%s (symbol %q archive %q member %q)", d.Message, fact(d, "symbol"), fact(d, "archive"), fact(d, "member"))
			}
		})
	}
}

// TestFFI_GS: a Windows member built with a stack protector asks for
// __security_cookie: E0001 with the /GS- hint.
func TestFFI_GS(t *testing.T) {
	src := "void Fill(char *p, int n);\nint Measure(void) { char b[64]; Fill(b, 64); return b[3]; }\nvoid Fill(char *p, int n) { for (int i = 0; i < n; i++) p[i] = (char)i; }\n"
	root := archiveRoot(t, types.X86_64Windows, []string{src}, "-fstack-protector-all")
	r, _ := ffiBuild(t, root, types.X86_64Windows, types.Dev, TestHooks{})
	got := lines(r)
	// One E0001 per undefined symbol: __security_cookie and
	// __security_check_cookie, each with the hint.
	if len(got) == 0 {
		t.Fatalf("want E0001s, got none\n%s", r.Report.Text(r.Sources))
	}
	for i, l := range got {
		d := r.Report.Diags[i].Diag
		if !strings.HasSuffix(l, ":E0001") || !strings.Contains(d.Message, "/GS-") || !strings.HasPrefix(fact(d, "symbol"), "__security") {
			t.Errorf("%s: %s (symbol %q)", l, d.Message, fact(d, "symbol"))
		}
	}
}

// TestFFI_Wins: a name an archive defines and a named DLL exports is
// taken from the archive.
func TestFFI_Wins(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest([]string{linkLit("pick.dll", "dynamic", "lib/pick.dll", "x86_64-windows"),
			linkLit("pick", "static", "lib/pick.lib", "x86_64-windows")}),
		"app/main.mk": ffiMain("extern Pick(): i32", "Pick()"),
	})
	cDLL(t, tools, filepath.Join(root, "lib"), "pick.dll", "__declspec(dllexport) int Pick(void) { return 1; }\n")
	cArchive(t, tools, filepath.Join(root, "lib", "pick.lib"), types.X86_64Windows, []string{"int Pick(void) { return 2; }\n"})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if out := runOut(t, exe); out != "2\n" {
			t.Errorf("%v: stdout %q, want 2 (the archive's Pick)", prof, out)
		}
		if got := dlls(t, exe); slices.Contains(got, "pick.dll") {
			t.Errorf("%v: pick.dll imported: %q", prof, got)
		}
	}
}

// TestFFI_LinuxDynamicRefused: with system-libc mode built, a Linux
// dynamic link whose path is "" supplies no names of its own, so an extern
// only it could hold is E0001 at link naming the symbol, with the link
// among the libraries searched. While libcModeBuilt is false the link is
// E0202 reason link when the bin is built, never a dynamic binary on the
// static entry; mk check accepts it either way.
func TestFFI_LinuxDynamicRefused(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg.mk":      ffiManifest([]string{linkLit("libm.so.6", "dynamic", "", "x86_64-linux")}),
		"app/main.mk": ffiMain("extern ilogb(x: f64): i32", "ilogb(8.0)"),
	})
	r, _ := ffiBuild(t, root, types.X86_64Linux, types.Dev, TestHooks{})
	got := lines(r)
	if !libcModeBuilt {
		if len(got) != 1 || !strings.HasSuffix(got[0], ":E0202") || fact(r.Report.Diags[0].Diag, "reason") != "link" {
			t.Errorf("want E0202 reason link, got %q\n%s", got, r.Report.Text(r.Sources))
		}
	} else if len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") || fact(r.Report.Diags[0].Diag, "symbol") != "ilogb" ||
		!strings.Contains(r.Report.Text(r.Sources), "libm.so.6") {
		t.Errorf("want E0001 for ilogb naming libm.so.6 among the libraries searched, got %q\n%s", got, r.Report.Text(r.Sources))
	}
	// Building for Windows, the Linux link does not apply.
	if runtime.GOOS == "windows" {
		r, _ = ffiBuild(t, root, types.X86_64Windows, types.Dev, TestHooks{})
		if got := lines(r); len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") {
			t.Errorf("Windows build: want the unresolved ilogb E0001, got %q", got)
		}
	}
}

// TestFFI_Winmm: tests/programs/ffi/winmm, the cookbook's "Call a function
// in a Windows DLL" with a Main, builds and runs in dev and release, and
// takes timeGetTime from winmm.dll, the named link, not from kernel32,
// which exports it too (a named link comes before the system DLLs).
func TestFFI_Winmm(t *testing.T) {
	needWindows(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "programs", "ffi", "winmm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := importsFrom(t, exe, "winmm.dll"); !slices.Equal(got, []string{"timeGetTime"}) {
			t.Errorf("%v: winmm.dll imports %q", prof, got)
		}
		if slices.Contains(importsFrom(t, exe, "kernel32.dll"), "timeGetTime") {
			t.Errorf("%v: timeGetTime imported from kernel32", prof)
		}
		if out := strings.TrimSpace(runOut(t, exe)); out == "" || out == "0" || strings.HasPrefix(out, "-") {
			t.Errorf("%v: UptimeMillis printed %q", prof, out)
		}
	}
}
