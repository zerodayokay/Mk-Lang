// Why: a lazily bound DLL (pkg.Link kind "lazy") must leave the import
// table, load on the first call from any stack and thread, keep every
// argument through the resolver stub, and panic by name when it cannot be
// found; dl.Present must answer without calling.
// What: the TestFFI_Lazy* tests and their helpers (lazyDLL, lazyRun).
// How: each test writes a small C DLL into t.TempDir(), built with the
// discovered clang and lld-link (no C runtime), and a package that links
// it lazily; the programs print what they saw and the test compares.

package driver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"mk/internal/toolchain"
	"mk/internal/types"
)

// lazyDLL builds the DLL name from the C text src into dir: with entry, a
// DllMain the C defines, else none (cDLL's form). extra are clang flags.
func lazyDLL(t *testing.T, tools *toolchain.Tools, dir, name, src string, entry bool, extra ...string) string {
	t.Helper()
	if !entry {
		return cDLL(t, tools, dir, name, src)
	}
	stem := strings.TrimSuffix(name, ".dll")
	o := cObject(t, tools, dir, stem, "x86_64-pc-windows-msvc", src, extra...)
	out := filepath.Join(dir, name)
	cTool(t, tools, dir, "lld-link", "/dll", "/entry:DllMain", "/nodefaultlib", "/machine:x64", "/out:"+out, o)
	return out
}

// lazyWrite writes s to path, making its directory.
func lazyWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lazyRun runs exe in dir with the extra environment env and returns its
// stdout, stderr and exit status.
func lazyRun(t *testing.T, exe, dir string, env ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%s: %v", exe, err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

// lazyCalls is the C of TestFFI_LazyCall's DLL: a counted add, the
// address of that add, a nine-argument mix of integers and floats, a
// 24-byte struct by value, two variadic sums and a function first called
// through a fn value.
const lazyCalls = `typedef struct { long long a, b, c; } S24;
int _fltused;
static int count;
__declspec(dllexport) int MkAdd(int a, int b) { count++; return a + b; }
__declspec(dllexport) int MkCount(void) { return count; }
__declspec(dllexport) unsigned long long MkAddr(void) { return (unsigned long long)&MkAdd; }
__declspec(dllexport) double MkMix(int a, double b, long long c, float d, int e, double f, int g, double h, int i) {
	return a + 2*b + 3*c + 4*d + 5*e + 6*f + 7*g + 8*h + 9*i;
}
__declspec(dllexport) long long MkS24(S24 s, int k) { return s.a*100 + s.b*10 + s.c + k*1000; }
__declspec(dllexport) long long MkVar(int n, ...) {
	__builtin_va_list ap; __builtin_va_start(ap, n);
	long long s = 0; for (int i = 1; i <= n; i++) s += i * __builtin_va_arg(ap, long long);
	__builtin_va_end(ap); return s;
}
__declspec(dllexport) double MkVarF(int n, ...) {
	__builtin_va_list ap; __builtin_va_start(ap, n);
	double s = 0; for (int i = 1; i <= n; i++) s += i * __builtin_va_arg(ap, double);
	__builtin_va_end(ap); return s;
}
__declspec(dllexport) int MkLater(int x) { return x * 3; }
`

// lazyCallsMain calls every function of lazyCalls, reading MkAdd's slot
// before the first call and after each of two.
const lazyCallsMain = `struct S24 {
    a: i64
    b: i64
    c: i64
}

extern MkAdd(a: i32, b: i32): i32
extern MkCount(): i32
extern MkAddr(): u64
extern MkMix(a: i32, b: f64, c: i64, d: f32, e: i32, f: f64, g: i32, h: f64, i: i32): f64
extern MkS24(s: S24, k: i32): i64
extern MkVar(n: i32, ...): i64
extern MkVarF(n: i32, ...): f64
extern MkLater(x: i32): i32
extern var __imp_MkAdd: u64

export Main(sys: &System): WriteError!void {
    var s0: u64 = 0
    var s1: u64 = 0
    var s2: u64 = 0
    var real: u64 = 0
    var n := 0
    var c := 0
    var mix: f64 = 0.0
    var st: i64 = 0
    var v: i64 = 0
    var vf: f64 = 0.0
    var l1 := 0
    var l2 := 0
    var l3 := 0
    unsafe "the test DLL's functions, declared with their C signatures; the slot is the lazy object's" {
        f := MkLater
        s0 = __imp_MkAdd
        n = int(MkAdd(40, 2))
        s1 = __imp_MkAdd
        n += int(MkAdd(1, 1))
        s2 = __imp_MkAdd
        real = MkAddr()
        c = int(MkCount())
        mix = MkMix(1, 2.5, 3, 4.5, 5, 6.25, 7, 8.75, 9)
        st = MkS24(S24(a: 1, b: 2, c: 3), 4)
        v = MkVar(5, i64(1), i64(2), i64(3), i64(4), i64(5))
        vf = MkVarF(2, 1.5, 2.25)
        l1 = int(f(1))
        l2 = int(f(2))
        l3 = int(MkLater(3))
    }
    try sys.Print("{n} {c}\n", (n, c))
    unbound := s0 != real
    bound := s1 == real && s2 == real
    try sys.Print("{unbound} {bound}\n", (unbound, bound))
    try sys.Print("{mix} {st} {v} {vf}\n", (mix, st, v, vf))
    try sys.Print("{l1} {l2} {l3}\n", (l1, l2, l3))
}
`

// lazyManifest is ffiManifest with an app module allowed unsafe.
func lazyManifest(links ...string) string { return ffiManifest(links) }

// TestFFI_LazyCall: a lazily bound DLL beside the executable, dev and
// release. MkAdd's slot holds something else before the first call and
// MkAdd's address after it and after the second, which takes the slot
// directly; the counter sees both calls; nine mixed arguments, a 24-byte
// struct by value, integer and float variadic arguments and a fn value
// taken before the first call arrive intact; the import table names no
// mklazy.dll.
func TestFFI_LazyCall(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	for p, s := range map[string]string{"pkg.mk": lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", types.X86_64Windows.Name)),
		"app/main.mk": lazyCallsMain} {
		lazyWrite(t, filepath.Join(root, filepath.FromSlash(p)), s)
	}
	// 1 + 5 + 9 + 18 + 25 + 37.5 + 49 + 70 + 81; 123 + 4000; the
	// weighted sums 55 and 1.5 + 4.5.
	want := "44 2\ntrue true\n295.5 4123 55 6\n3 6 9\n"
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := runOut(t, exe); got != want {
			t.Errorf("%v: printed %q, want %q", prof, got, want)
		}
		if slices.Contains(dlls(t, exe), "mklazy.dll") {
			t.Errorf("%v: the import table names mklazy.dll: %q", prof, dlls(t, exe))
		}
	}
}

// TestFFI_LazyImports: the same program linked "dynamic" imports
// mklazy.dll by name, and "lazy" does not.
func TestFFI_LazyImports(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	main := ffiMain("extern MkAdd(a: i32, b: i32): i32", "MkAdd(40, 2)")
	for _, kind := range []string{"dynamic", "lazy"} {
		root := t.TempDir()
		lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", kind, "mklazy.dll", types.X86_64Windows.Name)))
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), main)
		exe := mustBuild(t, root, types.X86_64Windows, types.Release)
		if got := runOut(t, exe); got != "42\n" {
			t.Errorf("%s: printed %q", kind, got)
		}
		has := slices.Contains(importsFrom(t, exe, "mklazy.dll"), "MkAdd")
		if has != (kind == "dynamic") {
			t.Errorf("%s: import table %q", kind, imports(t, exe))
		}
	}
}

// TestFFI_LazyUnused: a lazy link no function of reaches builds the same
// bytes as an unused dynamic link of that DLL, with no slot, stub or name
// of it: neither its ASCII nor its UTF-16 file name is in the image.
func TestFFI_LazyUnused(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	var images [][]byte
	for _, kind := range []string{"dynamic", "lazy"} {
		root := t.TempDir()
		lazyDLL(t, tools, root, "mkunused.dll", lazyCalls, false)
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkunused.dll", kind, "mkunused.dll", types.X86_64Windows.Name)))
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), ffiMain("extern MkAdd(a: i32, b: i32): i32", "0"))
		b, err := os.ReadFile(mustBuild(t, root, types.X86_64Windows, types.Release))
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, b)
	}
	if !bytes.Equal(images[0], images[1]) {
		t.Errorf("the unused lazy link's image differs from the unused dynamic link's (%d and %d bytes)", len(images[0]), len(images[1]))
	}
	wide := utf16.Encode([]rune("mkunused"))
	w := make([]byte, 0, 2*len(wide))
	for _, u := range wide {
		w = append(w, byte(u), byte(u>>8))
	}
	if bytes.Contains(images[1], []byte("mkunused")) || bytes.Contains(images[1], w) || bytes.Contains(images[1], []byte("mk_lazy")) {
		t.Errorf("the image of an unused lazy link holds its name or the lazy stub")
	}
}

// lazyAbsentMain asks dl.Present of a function of an absent DLL, of a
// symbol the present DLL lacks, of one it has and of a kernel32 function,
// then makes the call its argument names (the first of args: a, the
// absent DLL's; b, the missing symbol's).
const lazyAbsentMain = `import std.dl

extern MkGone(): i32
extern MkNope(): i32
extern MkAdd(a: i32, b: i32): i32
extern GetTickCount(): u32

export Main(sys: &System): WriteError!void {
    a := dl.Present(sys.io, MkGone)
    b := dl.Present(sys.io, MkNope)
    c := dl.Present(sys.io, MkAdd)
    d := dl.Present(sys.io, GetTickCount)
    try sys.Print("{a} {b} {c} {d}\n", (a, b, c, d))
    var n := 0
    unsafe "the test DLLs' functions" {
        if sys.args.Len() > 1 && sys.args[1] == "a" {
            n = int(MkGone())
        } else {
            n = int(MkNope())
        }
    }
    try sys.Print("{n}\n", (n))
}
`

// TestFFI_LazyAbsent: the program starts without the lazy mkgone.dll;
// dl.Present is false for its function and for a name mklazy.dll does not
// export, true for one it does and for kernel32's GetTickCount (eagerly
// bound); calling either missing function panics with code 17 (exit 101),
// naming the function, the DLL and which was not found. Dev and release.
func TestFFI_LazyAbsent(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	w := types.X86_64Windows.Name
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkgone.dll", "lazy", "mkgone.def", w), linkLit("mklazy.dll", "lazy", "mklazy.def", w)))
	lazyWrite(t, filepath.Join(root, "mkgone.def"), "MkGone\n")
	lazyWrite(t, filepath.Join(root, "mklazy.def"), "MkAdd\nMkNope\n")
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), lazyAbsentMain)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		// A .def path copies nothing: put the DLL beside the executable.
		b, err := os.ReadFile(filepath.Join(root, "mklazy.dll"))
		if err != nil {
			t.Fatal(err)
		}
		lazyWrite(t, filepath.Join(filepath.Dir(exe), "mklazy.dll"), string(b))
		for _, tc := range []struct{ arg, want string }{
			{"a", "panic: unresolved function: MkGone of mkgone.dll: the library could not be loaded"},
			{"b", "panic: unresolved function: MkNope of mklazy.dll: the library does not export it"},
		} {
			out, errOut, code := runExe(t, types.X86_64Windows, exe, "", tc.arg)
			if out != "false false true true\n" || code != 101 || !strings.Contains(errOut, tc.want) {
				t.Errorf("%v %s: exit %d, stdout %q, stderr %q; want exit 101 and %q", prof, tc.arg, code, out, errOut, tc.want)
			}
		}
	}
}

// TestFFI_LazySearch: a lazy DLL loads as a hard import of it does, from
// the executable's directory and never from the current directory or
// PATH: with the DLL only in the current directory, or only on PATH, the
// lazy program's dl.Present is false and the dynamic program does not
// start (STATUS_DLL_NOT_FOUND); beside the executable both run.
func TestFFI_LazySearch(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	elsewhere := t.TempDir()
	dll := lazyDLL(t, tools, elsewhere, "mksearch.dll", "__declspec(dllexport) int MkFound(void) { return 7; }\n", false)
	const notFound = 0xC0000135
	for _, kind := range []string{"lazy", "dynamic"} {
		root := t.TempDir()
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mksearch.dll", kind, "mksearch.def", types.X86_64Windows.Name)))
		lazyWrite(t, filepath.Join(root, "mksearch.def"), "MkFound\n")
		main := "import std.dl\n\nextern MkFound(): i32\n\nexport Main(sys: &System): WriteError!void {\n    p := dl.Present(sys.io, MkFound)\n    try sys.Print(\"{p}\\n\", (p))\n}\n"
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), main)
		exe := mustBuild(t, root, types.X86_64Windows, types.Dev)
		check := func(how string, out string, code int, found bool) {
			t.Helper()
			switch {
			case kind == "lazy" && (code != 0 || out != map[bool]string{true: "true\n", false: "false\n"}[found]):
				t.Errorf("lazy, %s: exit %d, printed %q, want found %v", how, code, out, found)
			case kind == "dynamic" && found && (code != 0 || out != "true\n"):
				t.Errorf("dynamic, %s: exit %#x, printed %q", how, code, out)
			case kind == "dynamic" && !found && uint32(code) != notFound:
				t.Errorf("dynamic, %s: exit %#x, printed %q, want STATUS_DLL_NOT_FOUND", how, code, out)
			}
		}
		out, _, code := lazyRun(t, exe, elsewhere)
		check("in the current directory", out, code, false)
		out, _, code = lazyRun(t, exe, t.TempDir(), "PATH="+elsewhere+string(os.PathListSeparator)+os.Getenv("PATH"))
		check("on PATH", out, code, false)
		b, err := os.ReadFile(dll)
		if err != nil {
			t.Fatal(err)
		}
		lazyWrite(t, filepath.Join(filepath.Dir(exe), "mksearch.dll"), string(b))
		out, _, code = lazyRun(t, exe, t.TempDir())
		check("beside the executable", out, code, true)
	}
}

// lazyDeep is a DLL whose initializer keeps a 512 KiB frame, touched one
// page at a time from the top as a stack probe would: on a 64 KiB task
// stack it faults; Name returns how many pages it touched (128).
const lazyDeep = `static int pages;
__attribute__((noinline)) static int deep(void) {
	volatile char buf[512 * 1024];
	int n = 0;
	for (int i = (int)sizeof(buf) - 1; i >= 0; i -= 4096) { buf[i] = 1; n += buf[i]; }
	return n;
}
int __stdcall DllMain(void *h, unsigned reason, void *r) { if (reason == 1) pages = deep(); return 1; }
__declspec(dllexport) int NAME(void) { return pages; }
`

// TestFFI_LazySmallStack: from a task with the smallest stack (64 KiB), a
// dl.Present and a first call through a fn value (which never switches
// stacks itself) each load a DLL whose initializer needs a 512 KiB frame:
// the load runs on the carrier's system stack, and both return 128.
func TestFFI_LazySmallStack(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	w := types.X86_64Windows.Name
	for _, n := range []string{"1", "2"} {
		lazyDLL(t, tools, root, "mkdeep"+n+".dll", strings.ReplaceAll(lazyDeep, "NAME", "MkDeep"+n), true, "-mno-stack-arg-probe")
	}
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkdeep1.dll", "lazy", "mkdeep1.dll", w), linkLit("mkdeep2.dll", "lazy", "mkdeep2.dll", w)))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), `import std.dl

extern MkDeep1(): i32
extern MkDeep2(): i32

ViaPresent(io: Io, out: mut bool) {
    *out = dl.Present(io, MkDeep1)
}

ViaValue(out: mut int) {
    var n := 0
    unsafe "the test DLL's function" {
        f := MkDeep2
        n = int(f())
    }
    *out = n
}

export Main(sys: &System): WriteError!void {
    var p := false
    var v := 0
    small := Limits(tasks: 1, stack: StackSize.Bytes(n: 65536))
    scope s := sys.io.ScopeWith(small) {
        s.go ViaPresent(sys.io, mut p)
    }
    scope s := sys.io.ScopeWith(small) {
        s.go ViaValue(mut v)
    }
    var d := 0
    unsafe "the test DLL's function" {
        d = int(MkDeep1())
    }
    try sys.Print("{p} {v} {d}\n", (p, v, d))
}
`)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := runOut(t, exe); got != "true 128 128\n" {
			t.Errorf("%v: printed %q", prof, got)
		}
	}
}

// TestFFI_LazyForeignThread: the first call of a lazy function made on a
// thread Mk did not start (the program's own CreateThread) resolves there.
func TestFFI_LazyForeignThread(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mkft.dll", "__declspec(dllexport) int MkFt(int x) { return x * 11; }\n", false)
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkft.dll", "lazy", "mkft.dll", types.X86_64Windows.Name)))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), `import std.atomic

extern CreateThread(attrs: uint, stack: uint, start: fn(*u8): u32, param: uint, flags: u32, id: uint): uint
extern WaitForSingleObject(h: uint, ms: u32): u32
extern CloseHandle(h: uint): i32
extern MkFt(x: i32): i32

var FtResult: atomic.AtomicInt = atomic.AtomicInt.New(0)

Callback(p: *u8): u32 {
    var n := 0
    unsafe "the test DLL's function" {
        n = int(MkFt(5))
    }
    FtResult.Store(i64(n))
    return 0
}

export Main(sys: &System): WriteError!void {
    unsafe "kernel32: a thread of this program that runs Callback, waited for before Main reads its result" {
        h := CreateThread(0, 0, Callback, 0, 0, 0)
        _ = WaitForSingleObject(h, 0xFFFF_FFFF)
        _ = CloseHandle(h)
    }
    n := FtResult.Load()
    try sys.Print("{n}\n", (n))
}
`)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := runOut(t, exe); got != "55\n" {
			t.Errorf("%v: printed %q", prof, got)
		}
	}
}

// TestFFI_LazyRace: eight tasks on eight carriers (MK_CARRIERS=8) wait for
// each other, then each makes the first calls of two functions of one
// unloaded DLL at once, while its initializer runs for milliseconds; every
// result is right (no function runs before the initializer finished), run
// after run, and some runs make them on more than one thread (the tasks'
// thread ids differ).
func TestFFI_LazyRace(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	// The initializer spins (milliseconds) before it marks the DLL ready,
	// so racing first calls wait in the loader; a function called before
	// its DLL's initializer finished answers wrong.
	lazyDLL(t, tools, root, "mkrace.dll", `static volatile int ready;
int __stdcall DllMain(void *h, unsigned reason, void *r) {
	if (reason == 1) { for (volatile long long i = 0; i < 5000000; i++) {} ready = 1; }
	return 1;
}
__declspec(dllexport) int RaceA(int x) { return ready ? x + 1 : -1000; }
__declspec(dllexport) int RaceB(int x) { return ready ? x * 2 : -1000; }
`, true)
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkrace.dll", "lazy", "mkrace.dll", types.X86_64Windows.Name)))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), `import std.atomic

extern RaceA(x: i32): i32
extern RaceB(x: i32): i32
extern GetCurrentThreadId(): u32

var First: atomic.AtomicInt = atomic.AtomicInt.New(0)
var Threads: atomic.AtomicInt = atomic.AtomicInt.New(1)

Job(io: Io, ready: &atomic.AtomicInt, bad: &atomic.AtomicInt, k: i32) {
    var tid: i64 = 0
    unsafe "kernel32" {
        tid = i64(GetCurrentThreadId())
    }
    if First.Load() == 0 {
        First.Store(tid)
    } else if First.Load() != tid {
        Threads.Store(2)
    }
    _ = ready.Add(1)
    // Spin a while before each yield, so idle carriers take the other
    // tasks rather than this carrier running them one after another.
    for ready.Load() < 8 {
        for _ in 0..20_000 {
            atomic.Pause()
        }
        io.Yield() else {}
    }
    var a: i32 = 0
    var b: i32 = 0
    unsafe "the test DLL's functions" {
        if k % 2 == 0 {
            a = RaceA(k)
            b = RaceB(k)
        } else {
            b = RaceB(k)
            a = RaceA(k)
        }
    }
    if a != k + 1 || b != k * 2 {
        _ = bad.Add(1)
    }
}

export Main(sys: &System): WriteError!void {
    ready := atomic.AtomicInt.New(0)
    bad := atomic.AtomicInt.New(0)
    scope s := sys.io.Scope() {
        for k in 0..8 {
            s.go Job(sys.io, &ready, &bad, Trunc[i32](k))
        }
    }
    n := bad.Load()
    m := Threads.Load() > 1
    try sys.Print("bad {n} {m}\n", (n, m))
}
`)
	runs := 300
	if testing.Short() {
		runs = 30
	}
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		spread := 0 // runs whose first calls ran on more than one thread
		for i := range runs {
			out, errOut, code := lazyRun(t, exe, t.TempDir(), "MK_CARRIERS=8")
			if code != 0 || out != "bad 0 true\n" && out != "bad 0 false\n" {
				t.Fatalf("%v run %d: exit %d, stdout %q, stderr %q", prof, i, code, out, errOut)
			}
			if out == "bad 0 true\n" {
				spread++
			}
		}
		// The race is real only when the calls ran on several carriers.
		if spread == 0 {
			t.Errorf("%v: in no run did the tasks run on more than one thread", prof)
		}
		t.Logf("%v: %d of %d runs made their first calls on more than one thread", prof, spread, runs)
	}
}

// TestFFI_LazyKinds: one DLL linked "lazy" by one link and "dynamic" by
// another is E0202 reason link at the second link's kind.
func TestFFI_LazyKinds(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	w := types.X86_64Windows.Name
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", w), linkLit("mklazy.dll", "dynamic", "mklazy.dll", w)))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), ffiMain("extern MkAdd(a: i32, b: i32): i32", "MkAdd(1, 2)"))
	r, exe := ffiBuild(t, root, types.X86_64Windows, types.Dev, TestHooks{})
	if exe != "" || len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0202, got:\n%s", r.Report.Text(r.Sources))
	}
	d := r.Report.Diags[0].Diag
	if d.Code.String() != "E0202" || fact(d, "reason") != "link" || fact(d, "manifest") != "links[1].kind" {
		t.Errorf("got %s %q at %q: %s", d.Code, fact(d, "reason"), fact(d, "manifest"), d.Message)
	}
}

// TestFFI_LazyPresentArgument: dl.Present's argument must name a bodiless
// extern directly: a local holding one, a function with a body and a call
// are each E0705 reason present-argument, and so is Present named as a
// value; the direct names check clean without unsafe.
func TestFFI_LazyPresentArgument(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"local", "unsafe \"x\" {\n        f := MkAdd\n        _ = dl.Present(sys.io, f)\n    }"},
		{"body", "_ = dl.Present(sys.io, Summed)"},
		{"call", "_ = dl.Present(sys.io, Pick())"},
		{"value", "g := dl.Present[fn(i32, i32): i32]\n    _ = g"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{
				"pkg.mk": lazyManifest(),
				"app/main.mk": "import std.dl\n\nextern MkAdd(a: i32, b: i32): i32\n\nSummed(a: i32, b: i32): i32 {\n    return a + b\n}\n\n" +
					"Pick(): fn(i32, i32): i32 {\n    return Summed\n}\n\nexport Main(sys: &System) {\n    " + tc.body + "\n}\n",
			})
			r := check(t, Config{Root: root})
			var codes []string
			for _, d := range r.Report.Diags {
				codes = append(codes, d.Diag.Code.String()+":"+fact(d.Diag, "reason"))
			}
			if !slices.Contains(codes, "E0705:present-argument") {
				t.Errorf("want E0705 present-argument, got %q\n%s", codes, r.Report.Text(r.Sources))
			}
		})
	}
	root := writeTree(t, map[string]string{
		"pkg.mk":      lazyManifest(),
		"app/main.mk": "import std.dl\n\nextern MkAdd(a: i32, b: i32): i32\n\nexport Main(sys: &System) {\n    _ = dl.Present(sys.io, MkAdd)\n}\n",
	})
	clean(t, check(t, Config{Root: root}))
}

// TestFFI_LazyPresentKey: a lazy link's .def that stops naming MkPk moves
// it to the dynamic link after it, which changes dl.Present's lowering
// (codegen.Config.Lazy): the pack that holds the call is emitted again,
// the program answers for the new binding, and a verify build, which emits
// and compares every pack, finds the reused objects right. The link file's
// bytes also move the front end's context here; codegen's TestPresentFacts
// checks the pack key's own part.
func TestFFI_LazyPresentKey(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mkpk.dll", "__declspec(dllexport) int MkPk(void) { return 4; }\n", false)
	lazyDLL(t, tools, root, "mkpk2.dll", "__declspec(dllexport) int MkPk(void) { return 5; }\n", false)
	w := types.X86_64Windows.Name
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mkpk.dll", "lazy", "mkpk.def", w), linkLit("mkpk2.dll", "dynamic", "mkpk2.dll", w)))
	main := "import std.dl\n\nextern MkPk(): i32\n\nexport Main(sys: &System): WriteError!void {\n    p := dl.Present(sys.io, MkPk)\n    try sys.Print(\"{p}\\n\", (p))\n}\n"
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), main)
	cfg := Config{Root: root, Target: types.X86_64Windows, Profile: types.Dev, Tools: tools, CacheDir: t.TempDir(), Jobs: 4}
	out := filepath.Join(t.TempDir(), "ffi.exe")
	var runs []pkRun
	for _, def := range []string{"MkPk\n", "Other\n"} {
		lazyWrite(t, filepath.Join(root, "mkpk.def"), def)
		run := pkBuild(t, cfg, out)
		// mkpk.dll is absent, so Present is false while MkPk binds lazily.
		if got, want := runOut(t, out), map[string]string{"MkPk\n": "false\n", "Other\n": "true\n"}[def]; got != want {
			t.Errorf("def %q: printed %q, want %q", def, got, want)
		}
		runs = append(runs, run)
	}
	if !slices.Contains(runs[1].emittedMods(), "app") {
		t.Errorf("the app pack was not emitted again after MkPk left the lazy list: %v", runs[1].emittedMods())
	}
	pkVerify(t, cfg, out, runs[1].exe)
}

// TestFFI_LazyQualified: dl.Present takes a module-qualified extern
// (ext.MkQ, declared in another module of the package) without unsafe,
// and answers in a loop, in a helper called twice and in an if
// expression, dev and release (where the helpers are inlined).
func TestFFI_LazyQualified(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	w := types.X86_64Windows.Name
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", w), linkLit("mkq.dll", "lazy", "mkq.def", w)))
	lazyWrite(t, filepath.Join(root, "mkq.def"), "MkQ\n")
	lazyWrite(t, filepath.Join(root, "ext", "ext.mk"), "/// MkQ is mkq.dll's, which the test never builds.\nexport extern MkQ(): i32\n")
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), `import std.dl
import ext

extern MkAdd(a: i32, b: i32): i32

Has(io: Io): bool {
    return dl.Present(io, MkAdd)
}

Count(io: Io): int {
    var n := 0
    for _ in 0..3 {
        if dl.Present(io, ext.MkQ) {
            n += 1
        }
        if dl.Present(io, MkAdd) {
            n += 10
        }
    }
    return n
}

export Main(sys: &System): WriteError!void {
    a := Has(sys.io)
    b := Has(sys.io)
    c := Count(sys.io)
    x := if dl.Present(sys.io, ext.MkQ) { 1 } else { 2 }
    try sys.Print("{a} {b} {c} {x}\n", (a, b, c, x))
}
`)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := runOut(t, exe); got != "true true 30 2\n" {
			t.Errorf("%v: printed %q", prof, got)
		}
	}
}

// TestFFI_LazyFromC: C calls a lazily bound function too, by a plain call
// (the thunk) and through __declspec(dllimport) (the slot __imp_MkAdd),
// when a Mk extern declares it; a lazily listed name only C calls is E0001
// naming it; and a lazily listed name an archive defines takes the
// archive's definition, with dl.Present true and no import of the DLL.
func TestFFI_LazyFromC(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	w := types.X86_64Windows.Name
	t.Run("calls", func(t *testing.T) {
		root := t.TempDir()
		lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
		cArchive(t, tools, filepath.Join(root, "lib", "via.lib"), types.X86_64Windows, []string{
			"__declspec(dllimport) int MkAdd(int a, int b);\nint ViaImp(void) { return MkAdd(40, 2); }\n",
			"int MkAdd(int a, int b);\nint ViaCall(void) { return MkAdd(1, 1); }\n"})
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", w), linkLit("via", "static", "lib/via.lib", w)))
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), ffiMain("extern MkAdd(a: i32, b: i32): i32\nextern ViaImp(): i32\nextern ViaCall(): i32", "ViaImp() + ViaCall() + MkAdd(0, 0)"))
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			exe := mustBuild(t, root, types.X86_64Windows, prof)
			if got := runOut(t, exe); got != "44\n" {
				t.Errorf("%v: printed %q", prof, got)
			}
			if slices.Contains(dlls(t, exe), "mklazy.dll") {
				t.Errorf("%v: mklazy.dll imported", prof)
			}
		}
	})
	t.Run("c-only", func(t *testing.T) {
		root := t.TempDir()
		lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
		cArchive(t, tools, filepath.Join(root, "lib", "via.lib"), types.X86_64Windows, []string{"int MkCount(void);\nint ViaCount(void) { return MkCount(); }\n"})
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", w), linkLit("via", "static", "lib/via.lib", w)))
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), ffiMain("extern ViaCount(): i32", "ViaCount()"))
		r, exe := ffiBuild(t, root, types.X86_64Windows, types.Dev, TestHooks{})
		if exe != "" || len(r.Report.Diags) != 1 {
			t.Fatalf("want one E0001, got:\n%s", r.Report.Text(r.Sources))
		}
		if d := r.Report.Diags[0].Diag; d.Code.String() != "E0001" || fact(d, "symbol") != "MkCount" || !strings.Contains(d.Message, "binds lazily") {
			t.Errorf("got %s %q: %s", d.Code, fact(d, "symbol"), d.Message)
		}
	})
	t.Run("archive wins", func(t *testing.T) {
		root := t.TempDir()
		cDLL(t, tools, filepath.Join(root, "lib"), "pick.dll", "__declspec(dllexport) int Pick(void) { return 1; }\n")
		cArchive(t, tools, filepath.Join(root, "lib", "pick.lib"), types.X86_64Windows, []string{"int Pick(void) { return 2; }\n"})
		lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("pick.dll", "lazy", "lib/pick.dll", w), linkLit("pick", "static", "lib/pick.lib", w)))
		lazyWrite(t, filepath.Join(root, "app", "main.mk"), "import std.dl\n\nextern Pick(): i32\n\nexport Main(sys: &System): WriteError!void {\n    var n := 0\n    unsafe \"the archive's function\" {\n        n = int(Pick())\n    }\n    p := dl.Present(sys.io, Pick)\n    try sys.Print(\"{n} {p}\\n\", (n, p))\n}\n")
		for _, prof := range []types.Profile{types.Dev, types.Release} {
			exe := mustBuild(t, root, types.X86_64Windows, prof)
			if got := runOut(t, exe); got != "2 true\n" {
				t.Errorf("%v: printed %q, want the archive's Pick and true", prof, got)
			}
			if slices.Contains(dlls(t, exe), "pick.dll") {
				t.Errorf("%v: pick.dll imported", prof)
			}
		}
	})
}
