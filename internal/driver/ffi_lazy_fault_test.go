// Why: the lazy stub and resolver run inside the loader, on whatever stack
// and thread the first call arrives from: a library initializer that
// faults, fails or calls back into Mk, a callee C calls first, a struct
// returned through a hidden pointer, an unwind stopped inside the stub and
// std's own lists must each behave as documented.
// What: TestFFI_LazyInit, TestFFI_LazyUnwindEpilogue, TestFFI_LazyPresentForms
// and TestFFI_LazyStdSearch, with lazyInitDLLs.
// How: small C DLLs built with the discovered clang, lld-link and llvm-lib
// (no C runtime); the programs print what they saw, and the tests compare
// stdout, stderr and the exit status.

package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/toolchain"
	"mk/internal/types"
)

// lazyInitC are the DLLs of TestFFI_LazyInit: initializers that fault, that
// fail, and that call the Mk function whose address MK_CB holds (hex),
// keeping its result; a function that calls its fn argument; a plain one;
// and one that returns a 24-byte struct through a hidden pointer.
var lazyInitC = map[string]string{
	"mkav": `int __stdcall DllMain(void *h, unsigned reason, void *r) { if (reason == 1) *(volatile int *)0 = 1; return 1; }
__declspec(dllexport) int MkAv(void) { return 1; }
`,
	"mkfalse": `int __stdcall DllMain(void *h, unsigned reason, void *r) { return reason == 1 ? 0 : 1; }
__declspec(dllexport) int MkFalse(void) { return 1; }
`,
	"mkcbinit": `__declspec(dllimport) unsigned long __stdcall GetEnvironmentVariableA(const char *n, char *b, unsigned long s);
static int got;
int __stdcall DllMain(void *h, unsigned reason, void *r) {
	if (reason == 1) {
		char buf[32]; unsigned long n = GetEnvironmentVariableA("MK_CB", buf, sizeof buf);
		unsigned long long a = 0;
		for (unsigned long i = 0; i < n && i < sizeof buf; i++) a = a * 16 + (buf[i] <= '9' ? buf[i] - '0' : buf[i] - 'a' + 10);
		if (a) got = ((int (*)(int))a)(5);
	}
	return 1;
}
__declspec(dllexport) int MkCbInit(void) { return got; }
`,
	"mkcbcall": `__declspec(dllexport) int MkCall(int (*cb)(int), int x) { return cb(x) + 1; }
`,
	"mkft": `__declspec(dllexport) int MkFt(int x) { return x * 11; }
`,
	"mkret": `typedef struct { long long a, b, c; } S24;
__declspec(dllexport) S24 MkRet(long long a, int b, int c, int d, int e) { S24 s = { a * 100 + b, c * 10 + d, e }; return s; }
`,
}

// lazyInitDLLs builds lazyInitC's DLLs into dir; mkcbinit imports
// GetEnvironmentVariableA through an import library the toolchain makes
// (Tools.ImportLib), the others import nothing.
func lazyInitDLLs(t *testing.T, tools *toolchain.Tools, dir string) {
	t.Helper()
	if err := tools.ImportLib(context.Background(), "kernel32.dll", []string{"GetEnvironmentVariableA"}, "x64", filepath.Join(dir, "k32.lib")); err != nil {
		t.Fatal(err)
	}
	for stem, src := range lazyInitC {
		o := cObject(t, tools, dir, stem, "x86_64-pc-windows-msvc", src)
		args := []string{"/dll", "/nodefaultlib", "/machine:x64", "/out:" + filepath.Join(dir, stem+".dll"), o}
		if strings.Contains(src, "DllMain") {
			args = append(args, "/entry:DllMain", filepath.Join(dir, "k32.lib"))
		} else {
			args = append(args, "/noentry")
		}
		cTool(t, tools, dir, "lld-link", args...)
	}
}

// lazyInitMain runs one case, named by its first argument.
const lazyInitMain = `import std.dl
import std.raw

struct S24 {
    a: i64
    b: i64
    c: i64
}

extern MkAv(): i32
extern MkFalse(): i32
extern MkCbInit(): i32
extern MkCall(cb: fn(i32): i32, x: i32): i32
extern MkFt(x: i32): i32
extern MkRet(a: i64, b: i32, c: i32, d: i32, e: i32): S24
extern SetEnvironmentVariableA(name: *u8, value: *u8): i32

CbPanic(x: i32): i32 {
    Panic("the callback panics")
}

CbNest(x: i32): i32 {
    unsafe "mkft.dll's function, first called here, inside mkcbinit.dll's initializer" {
        return MkFt(x)
    }
}

// SetCb puts f's address, in hex, in MK_CB, where mkcbinit.dll's
// initializer reads it.
SetCb(f: fn(i32): i32) {
    var g := f
    var a: u64 = 0
    unsafe "a fn value is one word, its address" {
        a = raw.Read(raw.PtrCast[fn(i32): i32, u64](raw.Of(&g)))
    }
    var buf: [24]u8 = [0; 24]
    var tmp: [16]u8 = [0; 16]
    var n := 0
    for a != 0 {
        d := Trunc[u8](a & 15)
        tmp[n] = if d < 10 { 48 + d } else { 87 + d }
        a = a >> 4
        n += 1
    }
    for i in 0..n {
        buf[i] = tmp[n - 1 - i]
    }
    unsafe "kernel32; both strings are NUL-terminated" {
        _ = SetEnvironmentVariableA(raw.Data("MK_CB\u{0}".AsBytes()), raw.Data(buf[..]))
    }
}

// FirstByValue makes mkcbinit.dll's first call through a fn value on a task.
FirstByValue(out: mut int) {
    var n := 0
    unsafe "the test DLL's function" {
        f := MkCbInit
        n = int(f())
    }
    *out = n
}

export Main(sys: &System): WriteError!void {
    mode := if sys.args.Len() > 1 { sys.args[1] } else { "" }
    var n := 0
    if mode == "av" || mode == "false" {
        p := if mode == "av" { dl.Present(sys.io, MkAv) } else { dl.Present(sys.io, MkFalse) }
        try sys.Print("{p}\n", (p))
        unsafe "the test DLLs' functions" {
            n = if mode == "av" { int(MkAv()) } else { int(MkFalse()) }
        }
    } else if mode == "cbpanic" || mode == "cbnest" {
        SetCb(if mode == "cbpanic" { CbPanic } else { CbNest })
        scope s := sys.io.Scope() {
            s.go FirstByValue(mut n)
        }
    } else if mode == "fromc" {
        unsafe "the test DLLs' functions: C makes MkFt's first call, through its fn value" {
            n = int(MkCall(MkFt, 5))
        }
    } else if mode == "ret" {
        var r := S24(a: 0, b: 0, c: 0)
        unsafe "the test DLL's function" {
            r = MkRet(1, 2, 3, 4, 5)
        }
        ra := r.a
        rb := r.b
        rc := r.c
        try sys.Print("{ra} {rb} {rc}\n", (ra, rb, rc))
        return
    }
    try sys.Print("{n}\n", (n))
}
`

// TestFFI_LazyInit: a lazy DLL whose initializer faults (the loader takes
// the access violation) or returns FALSE is a failed load: dl.Present is
// false and the call panics with unresolved function, exit 101. An
// initializer that calls back into Mk runs the callback to its end: a
// panic there is the callback's own report, exit 101, with no hang under
// the loader lock; a callback that makes the first call of another lazy
// DLL's function gets its result. A lazy function passed to C as a fn
// value before its first call resolves when C calls it, and a 24-byte
// struct comes back through the hidden pointer the first call passes in
// rcx. Dev and release.
func TestFFI_LazyInit(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyInitDLLs(t, tools, root)
	w := types.X86_64Windows.Name
	var links []string
	for _, stem := range []string{"mkav", "mkfalse", "mkcbinit", "mkcbcall", "mkft", "mkret"} {
		links = append(links, linkLit(stem+".dll", "lazy", stem+".dll", w))
	}
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(links...))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), lazyInitMain)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		for _, tc := range []struct {
			mode, out, errHas string
			code              int
		}{
			{"av", "false\n", "panic: unresolved function: MkAv of mkav.dll: the library could not be loaded", 101},
			{"false", "false\n", "panic: unresolved function: MkFalse of mkfalse.dll: the library could not be loaded", 101},
			{"cbpanic", "", "panic: the callback panics", 101},
			{"cbnest", "55\n", "", 0},
			{"fromc", "56\n", "", 0},
			{"ret", "102 34 5\n", "", 0},
		} {
			out, errOut, code := runExe(t, types.X86_64Windows, exe, "", tc.mode)
			if out != tc.out || code != tc.code || tc.errHas != "" && !strings.HasPrefix(errOut, tc.errHas) {
				t.Errorf("%v %s: exit %d, stdout %q, stderr %q; want exit %d, %q and %q", prof, tc.mode, code, out, errOut, tc.code, tc.out, tc.errHas)
			}
		}
	}
}

// lazyUnwindMain unwinds, with RtlVirtualUnwind, a context stopped on each
// of mk_lazy_stub's three epilogue instructions (lea, pop, jmp), with a fake
// stack and a fake frame, and prints whether each came back to the return
// address the unwind must find there. The stub is found from MkAdd's first
// stub (its slot before the first call): lea (7 bytes), then jmp rel32.
const lazyUnwindMain = `import std.raw

extern MkAdd(a: i32, b: i32): i32
extern var __imp_MkAdd: u64
extern RtlLookupFunctionEntry(pc: uint, base: *uint, table: *u8): *u8
extern RtlVirtualUnwind(kind: u32, base: uint, pc: uint, entry: *u8, ctx: *u8, data: *u64, frame: *uint, ptrs: *u8): *u8

// Unwound is the rip and rsp RtlVirtualUnwind gives for a context at pc
// with rsp stack and rbp frame.
Unwound(pc: uint, stack: uint, frame: uint): (rip: uint, rsp: uint) {
    var buf: [200]u64 = [0; 200]
    unsafe "c is buf's 16-aligned CONTEXT (1,232 bytes); kernel32" {
        c := raw.FromAddr[u8]((raw.Addr(raw.DataMut(mut buf[..])) +% 15) & ~Trunc[uint](15))
        raw.Write(raw.PtrCast[u8, u32](raw.Offset(c, 0x30)), 0x10_001F)
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(c, 0x98)), stack)
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(c, 0xA0)), frame)
        raw.Write(raw.PtrCast[u8, uint](raw.Offset(c, 0xF8)), pc)
        var base: uint = 0
        e := RtlLookupFunctionEntry(pc, raw.OfMut(mut base), raw.Null[u8]())
        if raw.IsNull(e) {
            return (rip: 0, rsp: 0)
        }
        var data: u64 = 0
        var est: uint = 0
        _ = RtlVirtualUnwind(0, base, pc, e, c, raw.OfMut(mut data), raw.OfMut(mut est), raw.Null[u8]())
        return (rip: raw.Read(raw.PtrCast[u8, uint](raw.Offset(c, 0xF8))), rsp: raw.Read(raw.PtrCast[u8, uint](raw.Offset(c, 0x98))))
    }
}

export Main(sys: &System): WriteError!void {
    var stub: uint = 0
    var lea: uint = 0
    var jmp: uint = 0
    unsafe "the lazy object's first stub and mk_lazy_stub, read as bytes" {
        first := Trunc[uint](__imp_MkAdd)
        r := uint(raw.ReadUnaligned(raw.PtrCast[u8, u32](raw.FromAddr[u8](first + 8))))
        stub = if r >= 0x8000_0000 { first + 12 - (0x1_0000_0000 - r) } else { first + 12 + r }
        for i in 0..256 {
            p := stub + Trunc[uint](i)
            b0 := raw.Read(raw.FromAddr[u8](p))
            b1 := raw.Read(raw.FromAddr[u8](p + 1))
            b2 := raw.Read(raw.FromAddr[u8](p + 2))
            if lea == 0 && b0 == 0x48 && b1 == 0x8D && b2 == 0x65 {
                lea = p
            }
            if lea != 0 && jmp == 0 && p > lea && (b0 == 0xFF && b1 == 0x20 || b0 == 0x48 && b1 == 0xFF && b2 == 0x20) {
                jmp = p
            }
        }
    }
    // The stack holds the return address 0x1111; the frame the saved rbp
    // 0x2222 and the return address 0x3333.
    st: [2]u64 = [0x1111, 0]
    fr: [2]u64 = [0x2222, 0x3333]
    sa := raw.Addr(raw.Data(st[..]))
    fa := raw.Addr(raw.Data(fr[..]))
    a := Unwound(lea, sa, fa)
    b := Unwound(lea + 4, fa, fa)
    c := Unwound(jmp, sa, fa)
    ok_lea := lea != 0 && a.rip == 0x3333 && a.rsp == fa + 16
    ok_pop := b.rip == 0x3333 && b.rsp == fa + 16
    ok_jmp := jmp != 0 && c.rip == 0x1111 && c.rsp == sa + 8
    var n := 0
    unsafe "the test DLL's function" {
        n = int(MkAdd(1, 2))
    }
    try sys.Print("lea {ok_lea} pop {ok_pop} jmp {ok_jmp} {n}\n", (ok_lea, ok_pop, ok_jmp, n))
}
`

// TestFFI_LazyUnwindEpilogue: Windows' own unwinder steps out of
// mk_lazy_stub from each instruction of its epilogue: at the lea and the
// pop through the frame rbp holds, at the last jump (after the pop) as a
// tail call, to the return address at rsp. A sampling profiler or a
// debugger that stops a thread there gets the caller's frame.
func TestFFI_LazyUnwindEpilogue(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", types.X86_64Windows.Name)))
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), lazyUnwindMain)
	exe := mustBuild(t, root, types.X86_64Windows, types.Dev)
	if got := runOut(t, exe); got != "lea true pop true jmp true 3\n" {
		t.Errorf("printed %q", got)
	}
}

// lazyFormsMain asks dl.Present in a generic function (two instances), as
// a spawned task's argument and in a defer.
const lazyFormsMain = `import std.atomic
import std.dl

extern MkAdd(a: i32, b: i32): i32
extern MkGone(): i32

var Seen: atomic.AtomicInt = atomic.AtomicInt.New(0)

Gen[T](io: Io): bool {
    return dl.Present(io, MkAdd)
}

Show(p: bool, out: mut int) {
    *out = if p { 1 } else { 2 }
}

Deferred(io: Io) {
    defer {
        Seen.Store(if dl.Present(io, MkGone) { 10 } else { 20 })
    }
    Seen.Store(1)
}

export Main(sys: &System): WriteError!void {
    a := Gen[int](sys.io)
    b := Gen[str](sys.io)
    var v := 0
    scope s := sys.io.Scope() {
        s.go Show(dl.Present(sys.io, MkGone), mut v)
    }
    Deferred(sys.io)
    d := Seen.Load()
    try sys.Print("{a} {b} {v} {d}\n", (a, b, v, d))
}
`

// TestFFI_LazyPresentForms: dl.Present answers in a generic function's
// instances, as a spawned task's argument and inside a defer, dev and
// release (where the bodies are inlined and the defer is lowered at each
// exit): mklazy.dll is beside the executable, mkgone.dll nowhere.
func TestFFI_LazyPresentForms(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	lazyDLL(t, tools, root, "mklazy.dll", lazyCalls, false)
	w := types.X86_64Windows.Name
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest(linkLit("mklazy.dll", "lazy", "mklazy.dll", w), linkLit("mkgone.dll", "lazy", "mkgone.def", w)))
	lazyWrite(t, filepath.Join(root, "mkgone.def"), "MkGone\n")
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), lazyFormsMain)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		exe := mustBuild(t, root, types.X86_64Windows, prof)
		if got := runOut(t, exe); got != "true true 2 20\n" {
			t.Errorf("%v: printed %q", prof, got)
		}
	}
}

// TestFFI_LazyStdSearch: a name of std's crypt32.lazy.def that the program
// declares itself binds lazily, from System32 only: a crypt32.dll beside
// the executable, whose initializer fails, is never loaded, so dl.Present
// is true (System32's crypt32 loads).
func TestFFI_LazyStdSearch(t *testing.T) {
	needWindows(t)
	tools := needTools(t)
	root := t.TempDir()
	planted := lazyDLL(t, tools, t.TempDir(), "crypt32.dll", "int __stdcall DllMain(void *h, unsigned reason, void *r) { return reason == 1 ? 0 : 1; }\n__declspec(dllexport) int CertCloseStore(void *s, unsigned f) { return 7; }\n", true)
	lazyWrite(t, filepath.Join(root, "pkg.mk"), lazyManifest())
	lazyWrite(t, filepath.Join(root, "app", "main.mk"), "import std.dl\n\nextern CertCloseStore(store: uint, flags: u32): i32\n\nexport Main(sys: &System): WriteError!void {\n    p := dl.Present(sys.io, CertCloseStore)\n    try sys.Print(\"{p}\\n\", (p))\n}\n")
	exe := mustBuild(t, root, types.X86_64Windows, types.Dev)
	b, err := os.ReadFile(planted)
	if err != nil {
		t.Fatal(err)
	}
	lazyWrite(t, filepath.Join(filepath.Dir(exe), "crypt32.dll"), string(b))
	if got := runOut(t, exe); got != "true\n" {
		t.Errorf("printed %q: the crypt32.dll beside the executable was loaded", got)
	}
	if ds := dlls(t, exe); strings.Contains(strings.ToLower(strings.Join(ds, " ")), "crypt32") {
		t.Errorf("the import table names crypt32: %q", ds)
	}
}
