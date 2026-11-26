// Why: a Linux bin with a dynamic link runs in system-libc mode (PT_INTERP,
// glibc's loader, pthread carriers); that mode must link, version, thread and
// cache correctly without leaking std's own runtime into glibc's.
// What: TestFFI_Entry to TestFFI_DirectRefs, with ELF readers (elfDyn,
// definedDynsyms, threadStarts) and C sources (cmixC, fabsC).
// How: builds run on any host; runs need MK_LINUX=1 under tools/wsl-lock.ps1
// with -p 1 and copy the binary to the distribution's /tmp.

package driver

import (
	"context"
	"debug/elf"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mk/internal/source"
	"mk/internal/toolchain"
	"mk/internal/types"
)

// libcLink is the reference manual's own libc.so.6 link, no names of its own.
var libcLink = linkLit("libc.so.6", "dynamic", "", linuxT.Name)

// linuxT is the Linux target these tests build for and run on: the host's
// own on an AArch64 Linux host, so they run natively there, else
// x86_64-linux (natively, or under WSL). linuxBase is glibc's base symbol
// version for it, linuxInterp its dynamic linker and linuxMultiarch its
// library directory under /lib; linuxMemcpy is memcpy's default version.
var (
	linuxT         = hostLinux()
	linuxBase      = libcSpecs[linuxT.Name].base
	linuxInterp    = libcSpecs[linuxT.Name].interp
	linuxMemcpy    = map[string]string{types.X86_64Linux.Name: "GLIBC_2.14", types.Aarch64Linux.Name: "GLIBC_2.17"}[linuxT.Name]
	linuxMultiarch = map[string]string{types.X86_64Linux.Name: "x86_64-linux-gnu", types.Aarch64Linux.Name: "aarch64-linux-gnu"}[linuxT.Name]
)

func hostLinux() types.Target {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		return types.Aarch64Linux
	}
	return types.X86_64Linux
}

// elfDyn is an executable's PT_INTERP ("" for none) and DT_NEEDED list.
func elfDyn(t *testing.T, exe string) (string, []string) {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	interp := ""
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			b := make([]byte, p.Filesz)
			if _, err := p.ReadAt(b, 0); err != nil {
				t.Fatal(err)
			}
			interp = strings.TrimRight(string(b), "\x00")
		}
	}
	needed, _ := f.DynString(elf.DT_NEEDED)
	return interp, needed
}

// importVersion is the version and library of the executable's reference
// to name (.gnu.version_r), or "" when it has none.
func importVersion(t *testing.T, exe, name string) (string, string) {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range syms {
		if s.Name == name {
			return s.Version, s.Library
		}
	}
	return "", ""
}

// runLinuxEnv is runLinuxTmp with environment assignments (K=V, no
// quoting needed) given to the program.
func runLinuxEnv(t *testing.T, exe string, env []string, args ...string) (string, int) {
	t.Helper()
	prefix := runner(t, linuxT)
	tmp := "/tmp/mk-ffi4-" + strings.ReplaceAll(filepath.Base(filepath.Dir(exe)), " ", "_") + "-" + filepath.Base(exe)
	sh := `src="$1"; dst="$2"; shift 2; cp "$src" "$dst" && chmod +x "$dst" && env ` + strings.Join(env, " ") +
		` "$dst" "$@"; rc=$?; rm -f "$dst"; exit $rc`
	argv := append(append([]string(nil), prefix...), "sh", "-c", sh, "sh", toolchain.HostPathFor(prefix, exe), tmp)
	argv = append(argv, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

// TestFFI_Entry: a libc-mode bin has PT_INTERP
// the target's dynamic linker (linuxInterp) and DT_NEEDED libc.so.6 only, and runs with
// its arguments, environment and exit status intact, in dev and release,
// at 1 and 16 carriers. The same package with links: [] is static.
func TestFFI_Entry(t *testing.T) {
	main := "export Main(sys: &System): WriteError!int {\n" +
		"    for a in sys.args {\n        try sys.Print(\"arg {a}\\n\", (a))\n    }\n" +
		"    if v := sys.Env(\"FFI4_VAR\") {\n        try sys.Print(\"env {v}\\n\", (v))\n    }\n" +
		"    return 5\n}\n"
	static := writeTree(t, map[string]string{"pkg.mk": ffiManifest(nil), "app/main.mk": main})
	root := writeTree(t, map[string]string{"pkg.mk": ffiManifest([]string{libcLink}), "app/main.mk": main})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		if interp, needed := elfDyn(t, mustBuild(t, static, linuxT, prof)); interp != "" || len(needed) > 0 {
			t.Errorf("%v: links: [] gave PT_INTERP %q, DT_NEEDED %q", prof, interp, needed)
		}
		exe := mustBuild(t, root, linuxT, prof)
		interp, needed := elfDyn(t, exe)
		if interp != linuxInterp || !slices.Equal(needed, []string{"libc.so.6"}) {
			t.Errorf("%v: PT_INTERP %q, DT_NEEDED %q", prof, interp, needed)
		}
		if d := definedDynsyms(t, exe); len(d) > 0 {
			t.Errorf("%v: .dynsym defines %q, want nothing exported", prof, d)
		}
		if os.Getenv("MK_LINUX") != "1" {
			continue
		}
		for _, c := range []string{"1", "16"} {
			out, code := runLinuxEnv(t, exe, []string{"MK_CARRIERS=" + c, "FFI4_VAR=hello"}, "x", "y z")
			want := "arg " + "/tmp/mk-ffi4-" + filepath.Base(filepath.Dir(exe)) + "-" + filepath.Base(exe) + "\narg x\narg y z\nenv hello\n"
			if code != 5 || out != want {
				t.Errorf("%v carriers %s: exit %d, stdout %q, want 5, %q", prof, c, code, out, want)
			}
		}
	}
}

// realpathMain calls realpath with a null buffer: realpath@GLIBC_2.2.5
// refuses it (EINVAL, 22), realpath@GLIBC_2.3 allocates the answer.
const realpathMain = `import std.raw

extern realpath(p: *u8, r: *u8): *u8
extern __errno_location(): *i32

export Main(sys: &System): WriteError!void {
    var e: i32 = -1
    unsafe "realpath reads a NUL-terminated constant; errno is the thread's word" {
        p := realpath(raw.StrData("/tmp\u{0}"), raw.Null[u8]())
        if raw.IsNull(p) {
            e = *__errno_location()
        }
    }
    if e < 0 {
        try sys.Print("allocated\n")
    } else {
        try sys.Print("null {e}\n", (e))
    }
}
`

// TestFFI_Versions: a list name with @VERSION binds to that version: the
// executable's .gnu.version_r names it for libc.so.6, and the call behaves
// as that version does. A name with no version gets no version need, and
// glibc 2.39's loader then binds the oldest version, not the default one
// (measured on that loader), so std's list versions every name.
//
// The six builds share one cache: between them only the .def, the profile
// and so the link change, and std's and the program's packs are reused. A
// link that kept another .def's versions would show as a wrong version need.
func TestFFI_Versions(t *testing.T) {
	if linuxT != types.X86_64Linux {
		t.Skip("SKIP: x86_64's glibc has two realpath versions (GLIBC_2.2.5 and GLIBC_2.3); aarch64's has one")
	}
	tools := needTools(t)
	cache := t.TempDir()
	build := func(root string, prof types.Profile) string {
		t.Helper()
		s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: linuxT, Profile: prof, Tools: tools, CacheDir: cache, Jobs: 4})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "ffi"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build x86_64-linux %v:\n%s", prof, r.Report.Text(r.Sources))
		}
		return paths[0]
	}
	for _, v := range []struct{ def, ver, out string }{
		{"realpath@GLIBC_2.2.5\n", "GLIBC_2.2.5", "null 22\n"},
		{"realpath@GLIBC_2.3\n", "GLIBC_2.3", "allocated\n"},
		{"realpath\n", "", "null 22\n"},
	} {
		root := writeTree(t, map[string]string{
			"pkg.mk":       ffiManifest([]string{linkLit("libc.so.6", "dynamic", "lib/libc.def", linuxT.Name)}),
			"lib/libc.def": v.def,
			"app/main.mk":  realpathMain,
		})
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			exe := build(root, prof)
			ver, lib := importVersion(t, exe, "realpath")
			if ver != v.ver || v.ver != "" && lib != "libc.so.6" {
				t.Errorf("%q %v: realpath needs version %q of %q, want %q", v.def, prof, ver, lib, v.ver)
			}
			if os.Getenv("MK_LINUX") == "1" {
				if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != v.out {
					t.Errorf("%q %v: exit %d, stdout %q, want %q", v.def, prof, code, out, v.out)
				}
			}
		}
	}
}

// programDir is tests/programs/ffi/<name>.
func programDir(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "tests", "programs", "ffi", name))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestFFI_Libm: cos and hypot from libm.so.6 named in links match Go's math
// within one unit in the last place, and cos again through std.dl;
// arguments, environment and exit status 7, dev and release, 1 and 16
// carriers. DT_NEEDED is libc.so.6 and libm.so.6 and hypot needs glibc's
// base version (linuxBase).
func TestFFI_Libm(t *testing.T) {
	root := programDir(t, "libm")
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		if _, needed := elfDyn(t, exe); !slices.Equal(needed, []string{"libc.so.6", "libm.so.6"}) {
			t.Errorf("%v: DT_NEEDED %q", prof, needed)
		}
		if ver, _ := importVersion(t, exe, "hypot"); ver != linuxBase {
			t.Errorf("%v: hypot needs %q", prof, ver)
		}
		if d := definedDynsyms(t, exe); len(d) > 0 {
			t.Errorf("%v: .dynsym defines %q, want nothing exported", prof, d)
		}
		if os.Getenv("MK_LINUX") != "1" {
			continue
		}
		for _, c := range []string{"1", "16"} {
			out, code := runLinuxEnv(t, exe, []string{"MK_CARRIERS=" + c, "FFI4_VAR=hello"}, "x")
			got := map[string]string{}
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				k, v, _ := strings.Cut(l, " ")
				if k == "dl" {
					k, v, _ = strings.Cut(l, " cos ")
					k = "dl"
				}
				if k != "arg" {
					got[k] = v
				}
			}
			near := func(s string, want float64) bool {
				f, err := strconv.ParseFloat(s, 64)
				return err == nil && math.Abs(f-want) <= math.Abs(want)*0x1p-52
			}
			if code != 7 || !near(got["cos"], math.Cos(0.5)) || !near(got["hypot"], 5) || !near(got["dl"], math.Cos(0.5)) ||
				got["env"] != "hello" || !strings.Contains(out, "arg x\n") {
				t.Errorf("%v carriers %s: exit %d, stdout %q", prof, c, code, out)
			}
		}
	}
}

// TestFFI_Threads: 64 tasks call close(-1), errno, malloc and free on
// whatever carrier runs them; every errno is EBADF; at 16 carriers some ran
// off the main thread (a carrier pthread_create made); getpid, strlen and
// qsort with an Mk callback; dev and release, 1 and 16 carriers. Both
// profiles' bins hold the pthread branch and not the clone path (mono folds
// LibcBuild() to the build's mode); release is checked on a -debug build,
// which keeps the symbols a plain release bin drops.
func TestFFI_Threads(t *testing.T) {
	root := programDir(t, "libc")
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		// LibcBuild() folds to true, so the clone path's ThreadMain is not
		// compiled in.
		if got := buildStarts(t, root, prof, exe); !startsAre(got, prof, "PthreadMain", "ThreadMain") {
			t.Errorf("%v: thread starts %v, want PthreadMain and not ThreadMain", prof, got)
		}
		if os.Getenv("MK_LINUX") != "1" {
			continue
		}
		for _, c := range []string{"1", "16"} {
			out, code := runLinuxEnv(t, exe, []string{"MK_CARRIERS=" + c})
			want := "errno ok 12800 of 12800\noff main thread " + strconv.FormatBool(c == "16") +
				"\ngetpid equals the main tid true\nstrlen 11\nqsort -8 -3 0 2 5 9\n"
			if code != 0 || out != want {
				t.Errorf("%v carriers %s: exit %d, stdout %q, want %q", prof, c, code, out, want)
			}
		}
	}
}

// TestFFI_JoinedThread joins successive pinned threads in system-libc mode.
func TestFFI_JoinedThread(t *testing.T) {
	const main = `import std.raw
import std.runtime

var Finished: raw.Atomic[u32] = raw.Atomic[u32].New(0)

Worker(io: Io) {
    _ = io
    _ = Finished.Add(1)
}

Join(j: runtime.Joined, io: Io) {
    j.Join(io) else {
        Join(j, io.NoCancel())
    }
}

export Main(system: &System): int {
    for i in 0..4 {
        Join(runtime.Thread(system.io, Worker), system.io)
        if int(Finished.Load()) != i + 1 {
            return 1
        }
    }
    return 0
}
`
	for _, target := range []types.Target{types.X86_64Linux, types.Aarch64Linux} {
		t.Run(target.Name, func(t *testing.T) {
			root := writeTree(t, map[string]string{
				"pkg.mk":      ffiManifest([]string{linkLit("libc.so.6", "dynamic", "", target.Name)}),
				"app/main.mk": main,
			})
			exe := mustBuild(t, root, target, types.Dev)
			if target.Name == linuxT.Name && (runtime.GOOS == "linux" || os.Getenv("MK_LINUX") == "1") {
				if out, code := runLinuxEnv(t, exe, nil); code != 0 {
					t.Errorf("exit %d, stdout %q, want exit 0", code, out)
				}
			}
		})
	}
}

// TestFFI_Runtime: std's own runtime test blocks, std.rt and std.task (the
// fault, stack-overflow and cancellation blocks among them), built in
// system-libc mode through testLinks and run on x86_64-linux in dev and
// release at 1 and 16 carriers, each as its name asks; a block that asks for
// an environment runs alone with it, as in TestStdRuntimeTests (its own
// MK_CARRIERS wins). Each profile's links are checked to be libc-mode ones.
func TestFFI_Runtime(t *testing.T) {
	prefix := runner(t, linuxT)
	// MK_FFI4_BASELINE=1 runs the same blocks static (links: []), the comparison
	// a failure here is judged against.
	baseline := os.Getenv("MK_FFI4_BASELINE") == "1"
	if !baseline {
		testLinks = []Link{{Name: "libc.so.6", Kind: linkDynamic, Target: linuxT.Name, Field: "links (FFI4-Runtime)"}}
	}
	t.Cleanup(func() { testLinks = nil })
	wslenv := func(t *testing.T, keys []string) {
		if len(prefix) > 0 {
			t.Setenv("WSLENV", strings.Trim(os.Getenv("WSLENV")+":"+strings.Join(keys, ":"), ":"))
		}
	}
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		cache := t.TempDir()
		links, libc := 0, 0
		hooks := TestHooks{OnLink: func(j toolchain.LinkJob) {
			links++
			if j.Interp != "" {
				libc++
			}
		}}
		for _, c := range []string{"1", "16"} {
			t.Run(prof.String()+"-carriers-"+c, func(t *testing.T) {
				t.Setenv("MK_CARRIERS", c)
				wslenv(t, []string{"MK_CARRIERS"})
				for _, mod := range []string{"std.rt", "std.task"} {
					cfg := Config{StdTests: mod, StdDir: ffiStd(), Target: linuxT, Profile: prof, CacheDir: cache, TestHooks: hooks}
					rep := mkTest(t, cfg, TestOptions{})
					if len(rep.Tests) == 0 {
						t.Errorf("%s: no test blocks", mod)
					}
					for _, tc := range rep.Tests {
						timed := stdTimingBlock(tc.Name) && !timingRun()
						m := stdEnvTag.FindStringSubmatch(tc.Name)
						if timed {
							continue
						}
						if m == nil {
							judgeStdTest(t, tc, len(prefix) > 0)
							continue
						}
						t.Run(tc.Name, func(t *testing.T) {
							var keys []string
							for _, kv := range strings.Fields(m[1]) {
								k, v, _ := strings.Cut(kv, "=")
								t.Setenv(k, v)
								keys = append(keys, k)
							}
							wslenv(t, keys)
							one := mkTest(t, cfg, TestOptions{Run: tc.Name})
							if len(one.Tests) != 1 {
								t.Fatalf("-run %q selected %d tests", tc.Name, len(one.Tests))
							}
							judgeStdTest(t, one.Tests[0], len(prefix) > 0)
						})
					}
				}
			})
		}
		if !baseline && (links == 0 || libc != links) {
			t.Errorf("%v: %d of %d links in system-libc mode", prof, libc, links)
		}
	}
}

// cmixC is TestFFI_CMix's C source: memory from the system libc's malloc.
const cmixC = "void *malloc(unsigned long n);\nvoid *CmixAlloc(unsigned long n) { char *p = malloc(n); if (p) p[0] = 1; return p; }\n"

// cmixMain frees 100,000 blocks the C source malloc'd through an Mk
// extern free, then runs 32 tasks that each open libm.so.6 with std.dl,
// call its cos and close it, and counts the tasks that ran off the main
// thread.
const cmixMain = `import std.atomic
import std.dl
import std.raw

extern CmixAlloc(n: uint): *u8
extern free(p: *u8)
extern gettid(): i32

DlCos(io: Io, main_tid: i32, ok: &atomic.AtomicInt, off: &atomic.AtomicInt) {
    var spin := 0
    for k in 0..200000 {
        spin = spin +% k
    }
    lib := dl.Open(io, "libm.so.6", dl.Search.System) else {
        return
    }
    if p := lib.Symbol("cos") {
        var w := u64(raw.Addr(p))
        unsafe "cos takes and returns an f64, and lib is open; gettid reads nothing" {
            f := raw.Read(raw.PtrCast[u64, fn(f64): f64](raw.OfMut(mut w)))
            if f(0.0) == 1.0 && spin != 1 {
                _ = ok.Add(1)
            }
            if gettid() != main_tid {
                _ = off.Add(1)
            }
        }
    }
    lib.Close(io)
}

export Main(sys: &System): WriteError!void {
    var n := 0
    for i in 0..100000 {
        unsafe "the C source's malloc'd block, freed once by libc's free" {
            p := CmixAlloc(Trunc[uint](16 + i % 512))
            if !raw.IsNull(p) && raw.Read(p) == 1 {
                n += 1
            }
            free(p)
        }
    }
    try sys.Print("cmix {n}\n", (n))
    ok := atomic.AtomicInt.New(0)
    off := atomic.AtomicInt.New(0)
    var main_tid: i32 = 0
    unsafe "gettid reads nothing" {
        main_tid = gettid()
    }
    scope s := sys.io.Scope() {
        for _ in 0..32 {
            s.go DlCos(sys.io, main_tid, &ok, &off)
        }
    }
    try sys.Print("dl {n} off main thread {b}\n", (n: ok.Load(), b: off.Load() > 0))
}
`

// TestFFI_CMix: a C source in system-libc mode returns malloc'd memory that
// an Mk extern free frees, 100,000 times, so C and Mk share the system
// allocator and std.libc is not linked; then 32 tasks open libm.so.6 through
// std.dl, call cos and close it, on whatever carrier runs them (some off the
// main thread at 16 carriers); dev and release, 1 and 16 carriers.
func TestFFI_CMix(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg.mk":       ffiManifest([]string{linkLit("libc.so.6", "dynamic", "lib/libc.def", linuxT.Name)}, `pkg.CSource(path: "csrc/cmix.c", flags: [])`),
		"lib/libc.def": "gettid@GLIBC_2.30\n",
		"csrc/cmix.c":  cmixC,
		"app/main.mk":  cmixMain,
	})
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		f, err := elf.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		syms, _ := f.Symbols()
		f.Close()
		for _, s := range syms {
			if s.Name == "malloc" && s.Section != elf.SHN_UNDEF {
				t.Errorf("%v: the executable defines malloc (std.libc linked in system-libc mode)", prof)
			}
		}
		if ver, _ := importVersion(t, exe, "malloc"); ver != linuxBase {
			t.Errorf("%v: malloc needs %q, want %s from libc.so.6", prof, ver, linuxBase)
		}
		if os.Getenv("MK_LINUX") != "1" {
			continue
		}
		for _, c := range []string{"1", "16"} {
			out, code := runLinuxEnv(t, exe, []string{"MK_CARRIERS=" + c})
			want := "cmix 100000\ndl 32 off main thread " + strconv.FormatBool(c == "16") + "\n"
			if code != 0 || out != want {
				t.Errorf("%v carriers %s: exit %d, stdout %q, want %q", prof, c, code, out, want)
			}
		}
	}
}

// TestFFI_ExternVar (only functions resolve to a library): an extern var whose name a
// Linux link's .def lists is E0001 at its declaration before the link,
// naming the shared object; ld.lld would bind it to the stub's function.
func TestFFI_ExternVar(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg.mk":       ffiManifest([]string{linkLit("libdata.so.1", "dynamic", "lib/data.def", linuxT.Name)}),
		"lib/data.def": "Counter\n",
		"app/main.mk":  ffiMain("extern var Counter: i32", "int(Counter)"),
	})
	r, exe := ffiBuild(t, root, linuxT, types.Dev, TestHooks{})
	got := lines(r)
	if exe != "" || len(got) != 1 || !strings.HasSuffix(got[0], ":E0001") || fact(r.Report.Diags[0].Diag, "library") != "libdata.so.1" {
		t.Errorf("want one E0001 naming libdata.so.1, got %q\n%s", got, r.Report.Text(r.Sources))
	}
}

// dlSearchMain opens names under each Search and prints what each gives.
const dlSearchMain = `import std.dl

Try(sys: &System, name: str, search: dl.Search): WriteError!void {
    lib := dl.Open(sys.io, name, search) else e {
        try sys.Print("{e}\n", (e))
        return
    }
    found := lib.Symbol("cos").IsSome()
    missing := lib.Symbol("no_such_symbol_zz").IsNone()
    lib.Close(sys.io)
    try sys.Print("open {f} {m}\n", (f: found, m: missing))
}

export Main(sys: &System): WriteError!void {
    try Try(sys, "/lib/@MULTI@/libm.so.6", dl.Search.Path)
    try Try(sys, "/nonexistent-zz/libm.so.6", dl.Search.Path)
    try Try(sys, "/etc/passwd", dl.Search.Path)
    try Try(sys, "libm.so.6", dl.Search.Path)
    try Try(sys, "libnope-zz.so.9", dl.Search.System)
    try Try(sys, "libnope-zz.so.9", dl.Search.Application)
    try Try(sys, "@MULTI@/libm.so.6", dl.Search.System)
    try Try(sys, "", dl.Search.System)
}
`

// TestFFI_DlSearch (std.dl's Linux search rules in system-libc mode): Path opens
// an absolute path (Symbol finds cos, a missing name is none); an absent
// absolute path is NotFound (stat); a file that is no library is Invalid;
// a relative Path, a '/' under System and an empty name are BadName; a
// bare name ld.so does not find is NotFound under System and under
// Application (the executable's directory, stat). Dev and release.
func TestFFI_DlSearch(t *testing.T) {
	root := writeTree(t, map[string]string{"pkg.mk": ffiManifest([]string{libcLink}), "app/main.mk": strings.ReplaceAll(dlSearchMain, "@MULTI@", linuxMultiarch)})
	want := "open true true\nNotFound\nInvalid\nBadName\nNotFound\nNotFound\nBadName\nBadName\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		if os.Getenv("MK_LINUX") != "1" {
			continue
		}
		if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != want {
			t.Errorf("%v: exit %d, stdout %q, want %q", prof, code, out, want)
		}
	}
}

// definedDynsyms are the names an executable's .dynsym defines: what it
// exports, which in system-libc mode is nothing.
func definedDynsyms(t *testing.T, exe string) []string {
	t.Helper()
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, err := f.DynamicSymbols()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range syms {
		if s.Section != elf.SHN_UNDEF {
			out = append(out, s.Name)
		}
	}
	return out
}

// libcFaults are the E0001s of a failed build as "symbol library", sorted;
// it fails the test when the build wrote an executable or gave another
// code.
func libcFaults(t *testing.T, r Result, exe string) []string {
	t.Helper()
	if exe != "" {
		t.Fatalf("built %s, want E0001", exe)
	}
	var out []string
	for _, e := range r.Report.Diags {
		if e.Diag.Code.String() != "E0001" {
			t.Fatalf("got %s\n%s", e.Diag.Code, r.Report.Text(r.Sources))
		}
		out = append(out, fact(e.Diag, "symbol")+" "+fact(e.Diag, "library"))
	}
	slices.Sort(out)
	return out
}

// noExportsMain calls close through the bin's list and opens libz.so.1,
// whose own memcpy and memset must bind to glibc's.
const noExportsMain = `import std.dl

extern close(fd: i32): i32

export Main(sys: &System): WriteError!int {
    var r: i32 = 0
    unsafe "close of an invalid fd" {
        r = close(-1)
    }
    try sys.Print("close {r}\n", (r))
    lib := dl.Open(sys.io, "libz.so.1", dl.Search.System) else e {
        try sys.Print("dl {e}\n", (e))
        return 3
    }
    lib.Close(sys.io)
    try sys.Print("libz opened\n", ())
    return 0
}
`

// TestFFI_NoExports: a libc-mode executable exports nothing, and nothing it
// defines can stand in for the system libc. Refused with E0001 before the
// link: a bin that reaches std.libc (which crashed in std.rt's heap once
// glibc took its malloc); a list naming what every executable defines
// (memcpy and memset, which replaced glibc's for libz); a C source defining
// a name of std's list (strlen); an export extern a list names (close). The
// same program with a clean list builds in dev and release with a .dynsym
// that defines nothing, and on Linux runs.
func TestFFI_NoExports(t *testing.T) {
	withList := func(def, main string, extra ...string) string {
		files := map[string]string{
			"pkg.mk":       ffiManifest([]string{linkLit("libc.so.6", "dynamic", "lib/libc.def", linuxT.Name)}, extra...),
			"lib/libc.def": def,
			"app/main.mk":  main,
		}
		if len(extra) > 0 {
			files["csrc/s.c"] = "unsigned long strlen(const char *s) { unsigned long n = 0; while (s[n]) n++; return n; }\nint CStrlen(void) { return (int)strlen(\"four\"); }\n"
		}
		return writeTree(t, files)
	}
	useLibc := writeTree(t, map[string]string{
		"pkg.mk": ffiManifest([]string{linkLit("libm.so.6", "dynamic", "", linuxT.Name)}),
		"app/main.mk": "import std.libc\n\nexport Main(sys: &System): WriteError!void {\n    var n: uint = 0\n" +
			"    unsafe \"std.libc's malloc and free\" {\n        p := libc.malloc(32)\n        n = libc.malloc_usable_size(p)\n        libc.free(p)\n    }\n" +
			"    try sys.Print(\"usable {n}\\n\", (n))\n}\n",
	})
	for _, c := range []struct {
		name, root string
		want       []string
	}{
		{"std.libc", useLibc, []string{"free std.libc"}},
		{"memcpy", withList("close@"+linuxBase+"\nmemcpy@"+linuxMemcpy+"\nmemset@"+linuxBase+"\n", noExportsMain), []string{"memcpy libc.so.6", "memset libc.so.6"}},
		{"C strlen", withList("close@"+linuxBase+"\n", ffiMain("extern CStrlen(): i32", "CStrlen()"), `pkg.CSource(path: "csrc/s.c", flags: [])`), []string{"strlen libc.so.6"}},
		{"export extern", withList("close@"+linuxBase+"\n", ffiMain("export extern close(fd: i32): i32 {\n    return fd\n}", "close(3)")), []string{"close libc.so.6"}},
	} {
		r, exe := ffiBuild(t, c.root, linuxT, types.Dev, TestHooks{})
		if got := libcFaults(t, r, exe); !slices.Equal(got, c.want) {
			t.Errorf("%s: E0001 for %q, want %q\n%s", c.name, got, c.want, r.Report.Text(r.Sources))
		}
	}
	root := withList("close@"+linuxBase+"\n", noExportsMain)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		if d := definedDynsyms(t, exe); len(d) > 0 {
			t.Errorf("%v: .dynsym defines %q", prof, d)
		}
		if os.Getenv("MK_LINUX") == "1" {
			if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != "close -1\nlibz opened\n" {
				t.Errorf("%v: exit %d, stdout %q", prof, code, out)
			}
		}
	}
}

// cAddrC reads two glibc data objects and keeps free's address.
const cAddrC = `extern void *stderr;
extern char **environ;
int fileno(void *f);
void free(void *);
static void (*keep)(void *);
int CAddrFd(void) { return fileno(stderr); }
int CAddrEnv(void) { return environ[0] != 0 && environ[0][0] != 0; }
int CAddrKeep(void) { keep = free; return keep != 0; }
`

// TestFFI_CAddr (only functions resolve to a library): a list cannot say
// whether a name is data, so a C source that is not position-independent
// and takes a listed name's address directly (R_X86_64_32S, or
// R_AARCH64_ADR_PREL_PG_HI21 on aarch64) is E0001
// for each name, data (stderr, environ) or function (free): ld.lld would
// give it a canonical PLT entry, which reads code for data. A call
// (fileno) is not refused. Built position-independent, clang's default,
// the same source reaches each through the GOT, which the loader binds by
// name: on Linux fileno(stderr) is 2. Dev and release.
func TestFFI_CAddr(t *testing.T) {
	tree := func(flags string) string {
		return writeTree(t, map[string]string{
			"pkg.mk":       ffiManifest([]string{linkLit("libc.so.6", "dynamic", "lib/libc.def", linuxT.Name)}, `pkg.CSource(path: "csrc/a.c", flags: [`+flags+`])`),
			"lib/libc.def": "stderr@" + linuxBase + "\nenviron@" + linuxBase + "\nfileno@" + linuxBase + "\n",
			"csrc/a.c":     cAddrC,
			"app/main.mk":  ffiMain("extern CAddrFd(): i32\nextern CAddrEnv(): i32\nextern CAddrKeep(): i32", "CAddrFd() * 100 + CAddrEnv() * 10 + CAddrKeep()"),
		})
	}
	r, exe := ffiBuild(t, tree(`"-fno-pic"`), linuxT, types.Dev, TestHooks{})
	if got, want := libcFaults(t, r, exe), []string{"environ libc.so.6", "free libc.so.6", "stderr libc.so.6"}; !slices.Equal(got, want) {
		t.Errorf("-fno-pic: E0001 for %q, want %q\n%s", got, want, r.Report.Text(r.Sources))
	}
	root := tree("")
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		if ver, lib := importVersion(t, exe, "stderr"); ver != linuxBase || lib != "libc.so.6" {
			t.Errorf("%v: stderr needs %q of %q", prof, ver, lib)
		}
		if os.Getenv("MK_LINUX") == "1" {
			if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != "211\n" {
				t.Errorf("%v: exit %d, stdout %q, want 211", prof, code, out)
			}
		}
	}
}

// fabsC calls fabs, which std.libc supplies outside system-libc mode
// and glibc keeps in libm.so.6.
const fabsC = "double fabs(double);\nvoid *malloc(unsigned long);\nvoid free(void *);\nint CFabs(void) { void *p = malloc(8); free(p); return (int)fabs(-2.0); }\n"

// TestFFI_FabsHint: a C source that calls fabs links statically, where
// std.libc supplies it; in system-libc mode std.libc is not linked, so the
// undefined fabs is E0001 with a hint naming libm.so.6 and fabs at glibc's
// base version. With that link the bin builds, imports that fabs from
// libm.so.6,
// and on Linux prints 2. Dev and release.
func TestFFI_FabsHint(t *testing.T) {
	tree := func(links ...string) string {
		return writeTree(t, map[string]string{
			"pkg.mk":       ffiManifest(links, `pkg.CSource(path: "csrc/f.c", flags: [])`),
			"lib/libm.def": "fabs@" + linuxBase + "\n",
			"csrc/f.c":     fabsC,
			"app/main.mk":  ffiMain("extern CFabs(): i32", "CFabs()"),
		})
	}
	mustBuild(t, tree(), linuxT, types.Dev)
	r, exe := ffiBuild(t, tree(libcLink), linuxT, types.Dev, TestHooks{})
	if got := libcFaults(t, r, exe); !slices.Equal(got, []string{"fabs "}) ||
		!strings.Contains(r.Report.Diags[0].Diag.Message, "libm.so.6") || !strings.Contains(r.Report.Diags[0].Diag.Message, "fabs@"+linuxBase) {
		t.Errorf("want one E0001 for fabs naming libm.so.6 and fabs@"+linuxBase+"\n%s", r.Report.Text(r.Sources))
	}
	root := tree(libcLink, linkLit("libm.so.6", "dynamic", "lib/libm.def", linuxT.Name))
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, linuxT, prof)
		if ver, lib := importVersion(t, exe, "fabs"); ver != linuxBase || lib != "libm.so.6" {
			t.Errorf("%v: fabs needs %q of %q", prof, ver, lib)
		}
		if os.Getenv("MK_LINUX") == "1" {
			if out, code := runLinuxEnv(t, exe, nil); code != 0 || out != "2\n" {
				t.Errorf("%v: exit %d, stdout %q, want 2", prof, code, out)
			}
		}
	}
}

// TestFFI_StaticThreads (a bin with no dynamic link keeps the static
// link): with links: [] a Linux bin that starts 64 tasks and hello
// world are static in dev and release, with no PT_INTERP and no
// PT_DYNAMIC, and neither holds the pthread branch: hello starts no
// carrier, and in tasks thread.mk's branch is std.sys.LibcBuild(), which
// mono folds to the build's mode, so PthreadStart and PthreadMain are not
// compiled in while the clone path's ThreadMain is. A run-time LibcMode()
// test there would keep the dead branch in every static bin.
func TestFFI_StaticThreads(t *testing.T) {
	tasks := "import std.atomic\n\nWork(n: &atomic.AtomicInt) {\n    _ = n.Add(1)\n}\n\n" +
		"export Main(sys: &System): WriteError!void {\n    n := atomic.AtomicInt.New(0)\n" +
		"    scope s := sys.io.Scope() {\n        for _ in 0..64 {\n            s.go Work(&n)\n        }\n    }\n" +
		"    try sys.Print(\"tasks {k}\\n\", (k: n.Load()))\n}\n"
	hello := "export Main(sys: &System): WriteError!void {\n    try sys.Print(\"hello\\n\", ())\n}\n"
	for _, c := range []struct{ name, main string }{{"tasks", tasks}, {"hello", hello}} {
		root := writeTree(t, map[string]string{"pkg.mk": ffiManifest(nil), "app/main.mk": c.main})
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			exe := mustBuild(t, root, linuxT, prof)
			f, err := elf.Open(exe)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range f.Progs {
				if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
					t.Errorf("%s %v: has %v", c.name, prof, p.Type)
				}
			}
			f.Close()
			if st, err := os.Stat(exe); err == nil {
				t.Logf("%s %v: %d bytes", c.name, prof, st.Size())
			}
			got := buildStarts(t, root, prof, exe)
			if c.name == "tasks" && !startsAre(got, prof, "ThreadMain", "PthreadMain") {
				t.Errorf("tasks %v: thread starts %v, want ThreadMain and not PthreadMain", prof, got)
			}
			if c.name == "hello" && len(got) != 0 {
				t.Errorf("hello %v: thread starts %v, want none", prof, got)
			}
			if os.Getenv("MK_LINUX") == "1" && c.name == "tasks" {
				if out, code := runLinuxEnv(t, exe, []string{"MK_CARRIERS=16"}); code != 0 || out != "tasks 64\n" {
					t.Errorf("tasks %v: exit %d, stdout %q", prof, code, out)
				}
			}
		}
	}
}

// TestFFI_LibcBuildCache: a static and a system-libc build of
// one tasks program, alternating on one cache, each get their own mode's
// thread start: the libc-mode build's module keys and walk are keyed apart
// (frontcache.go), and its objects' keys are their IR's.
func TestFFI_LibcBuildCache(t *testing.T) {
	tools := needTools(t)
	cache := t.TempDir()
	tasks := "export Main(sys: &System): WriteError!void {\n    scope s := sys.io.Scope() {\n        s.go Work()\n    }\n}\n\nWork() {\n}\n"
	static := writeTree(t, map[string]string{"pkg.mk": ffiManifest(nil), "app/main.mk": tasks})
	libc := writeTree(t, map[string]string{"pkg.mk": ffiManifest([]string{libcLink}), "app/main.mk": tasks})
	pth := []string{"PthreadMain", "PthreadStart"}
	var walkKeys []source.Hash // app's, static then libc
	for i, c := range []struct {
		root string
		want []string
		hit  bool // std.sys from the cache: only a build of the same mode stored it
	}{{static, []string{"ThreadMain"}, false}, {libc, pth, false}, {static, []string{"ThreadMain"}, true}, {libc, pth, true}} {
		if i == 2 {
			// A comment-only edit bypasses the whole-build fingerprint fast path
			// while keeping every token/anchor hash identical, so this white-box
			// test still exercises frontCache and walkCache directly.
			if err := os.WriteFile(filepath.Join(static, "app", "main.mk"), []byte(tasks+"// warm\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		s, err := NewSession(Config{Root: c.root, StdDir: ffiStd(), Target: linuxT, Profile: types.Dev, Tools: tools, CacheDir: cache, Jobs: 4})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "ffi"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build %d:\n%s", i, r.Report.Text(r.Sources))
		}
		if hit := slices.Contains(r.Trace.ModulesFromCache, "std.sys"); hit != c.hit {
			t.Errorf("build %d: std.sys from the cache %v, want %v (%v)", i, hit, c.hit, r.Trace.ModulesFromCache)
		}
		// The walk markers (walkcache.go): the walk's roots are app's. The
		// interface contents are the same in both modes, but the libc
		// context in fc.ctx (frontcache.go) keys app's module prefix, and
		// so its interface entry and its walk key. Without it, a static
		// build's clean-walk marker would let a libc build skip the walk
		// through the pthread branch (and the other way round).
		m := s.pkgB.byName["app"]
		if m == nil {
			t.Fatalf("build %d: no module app", i)
		}
		k, ok := s.w.front.walkKey(m)
		if !ok {
			t.Fatalf("build %d: app has no walk key", i)
		}
		if i < 2 {
			walkKeys = append(walkKeys, k)
		} else if k != walkKeys[i-2] {
			t.Errorf("build %d: app's walk key is not build %d's", i, i-2)
		}
		if i == 1 && k == walkKeys[0] {
			t.Errorf("build 1: the libc build's walk key for app is the static build's")
		}
		if !s.w.front.walkedClean(m) {
			t.Errorf("build %d: app not marked walked clean", i)
		}
		f, err := elf.Open(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		syms, _ := f.Symbols()
		f.Close()
		if got := threadStarts(syms); !slices.Equal(got, c.want) {
			t.Errorf("build %d: thread starts %v, want %v", i, got, c.want)
		}
	}
}

// buildStarts are the thread starts (threadStarts) of exe, root's
// x86_64-linux bin at prof. A release bin keeps no symbols, so for release
// root is built again with Debug (-release -debug), which keeps them; the
// fold does not read the profile or Debug, so that bin's branch is exe's.
func buildStarts(t *testing.T, root string, prof types.Profile, exe string) []string {
	t.Helper()
	if prof == types.Release {
		s, err := NewSession(Config{Root: root, StdDir: ffiStd(), Target: linuxT, Profile: prof, Debug: true, Tools: needTools(t), CacheDir: t.TempDir(), Jobs: 4})
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "ffi"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("release -debug build:\n%s", r.Report.Text(r.Sources))
		}
		exe = paths[0]
	}
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syms, _ := f.Symbols()
	return threadStarts(syms)
}

// startsAre reports whether got, a bin's thread starts (threadStarts) at
// prof, are one mode's: has is that mode's entry and not the other mode's,
// both address-taken (thread.mk ThreadMain, pthread.mk PthreadMain). In
// dev nothing is inlined, so got is exactly the mode's set: [ThreadMain]
// for the clone path, [PthreadMain PthreadStart] for the pthread branch. In
// release only the two address-taken entries are asserted: ThinLTO may
// inline PthreadStart, which is only called directly, and that is no
// regression.
func startsAre(got []string, prof types.Profile, has, not string) bool {
	if prof == types.Dev {
		want := []string{"ThreadMain"}
		if has == "PthreadMain" {
			want = []string{"PthreadMain", "PthreadStart"}
		}
		return slices.Equal(got, want)
	}
	return slices.Contains(got, has) && !slices.Contains(got, not)
}

// threadStarts are the std.sys thread entry functions syms defines, sorted:
// ThreadMain is the clone path's, PthreadStart and PthreadMain the
// system-libc branch's.
func threadStarts(syms []elf.Symbol) []string {
	var out []string
	for _, s := range syms {
		if n, ok := strings.CutPrefix(s.Name, "mk.std.sys."); ok && (n == "ThreadMain" || n == "PthreadStart" || n == "PthreadMain") {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestFFI_DirectRefs: directRefs reports, for listed names
// only, the references ld.lld would turn into a canonical PLT entry: an
// absolute R_X86_64_32S, a PC32 that is not after a call or jmp opcode,
// and a 64-bit word in a read-only section. A PC32 call (0xE8), a PC32
// tail jump (0xE9), a PLT32 call, a GOT load, a 64-bit word in a writable
// section and an unlisted name are not reported.
func TestFFI_DirectRefs(t *testing.T) {
	tools := needTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "r.s")
	text := "\t.file \"r.c\"\n\t.text\n\t.globl f\nf:\n" +
		"\t.byte 0xe8\n\t.long callee - . - 4\n" + // call, PC32
		"\tcall callee@PLT\n" +
		"\tmovq gotname@GOTPCREL(%rip), %rax\n" +
		"\tmovq absname, %rax\n" + // R_X86_64_32S
		"\tleaq pcname(%rip), %rax\n" + // PC32, not a call
		"\tmovq other, %rax\n" + // unlisted
		"\t.byte 0xe9\n\t.long tail - . - 4\n" + // jmp, PC32
		"\t.data\n\t.quad dataword\n" +
		"\t.section .rodata\n\t.quad roword\n" +
		"\t.section .note.GNU-stack,\"\",@progbits\n"
	if err := os.WriteFile(src, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(dir, "r.o")
	if err := tools.CompileAsm(context.Background(), toolchain.CJob{Triple: types.X86_64Linux.LLVMTriple, Input: src, Output: obj}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(obj)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{"callee": true, "gotname": true, "absname": true, "pcname": true, "tail": true, "dataword": true, "roword": true}
	file, refs := directRefs(b, func(n string) bool { return listed[n] })
	var got []string
	for _, r := range refs {
		got = append(got, r.sym+" "+r.typ)
	}
	slices.Sort(got)
	want := []string{"absname R_X86_64_32S", "pcname R_X86_64_PC32", "roword R_X86_64_64"}
	if file != "r.c" || !slices.Equal(got, want) {
		t.Errorf("file %q, refs %q; want r.c, %q", file, got, want)
	}
}

// TestFFI_DirectRefsAArch64: directRefs on an AArch64 object reports, for
// listed names only, an ADRP to the name's page (ADR_PREL_PG_HI21, once:
// its ADD half is not counted again), an absolute MOVW group and a 64-bit
// word in a read-only section. A BL call (CALL26), a B tail jump (JUMP26),
// a GOT load (ADR_GOT_PAGE, LD64_GOT_LO12_NC), a 64-bit word in a writable
// section and an unlisted name are not reported.
func TestFFI_DirectRefsAArch64(t *testing.T) {
	tools := needTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "r.s")
	text := "\t.file \"r.c\"\n\t.text\n\t.globl f\nf:\n" +
		"\tbl callee\n" +
		"\tadrp x0, :got:gotname\n\tldr x0, [x0, :got_lo12:gotname]\n" +
		"\tadrp x1, pcname\n\tadd x1, x1, :lo12:pcname\n" +
		"\tmovz x2, #:abs_g1:absname\n\tmovk x2, #:abs_g0_nc:absname\n" +
		"\tadrp x3, other\n" + // unlisted
		"\tb tail\n" +
		"\t.data\n\t.quad dataword\n" +
		"\t.section .rodata\n\t.quad roword\n" +
		"\t.section .note.GNU-stack,\"\",@progbits\n"
	if err := os.WriteFile(src, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(dir, "r.o")
	if err := tools.CompileAsm(context.Background(), toolchain.CJob{Triple: types.Aarch64Linux.LLVMTriple, Input: src, Output: obj}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(obj)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{"callee": true, "gotname": true, "absname": true, "pcname": true, "tail": true, "dataword": true, "roword": true}
	file, refs := directRefs(b, func(n string) bool { return listed[n] })
	var got []string
	for _, r := range refs {
		got = append(got, r.sym+" "+r.typ)
	}
	slices.Sort(got)
	want := []string{"absname R_AARCH64_MOVW_UABS_G0_NC", "absname R_AARCH64_MOVW_UABS_G1", "pcname R_AARCH64_ADR_PREL_PG_HI21", "roword R_AARCH64_ABS64"}
	if file != "r.c" || !slices.Equal(got, want) {
		t.Errorf("file %q, refs %q; want r.c, %q", file, got, want)
	}
}
