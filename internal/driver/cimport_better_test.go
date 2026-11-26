// Why: end-to-end verification that the better bindings (static consts,
// INITGUID struct consts, pointer macros, wide-string macros, COM vtable
// methods, DECLARE_HANDLE Hwnd naming) and raw.FnFromAddr[?fn(...)] compile,
// link and run on Windows.
//
// What: TestCImport_BetterBindingsEndToEnd and TestFFI_FnFromAddrOptional.
//
// How: builds and runs small packages with C imports and C sources via the
// driver, and verifies E1101 on COM vtable method calls outside unsafe.

package driver

import (
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

func TestCImport_BetterBindingsEndToEnd(t *testing.T) {
	tools := needTools(t)
	target := types.X86_64Windows
	files := map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":           `"better"`,
			"bins":           `[pkg.Bin(name: "better", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_sources":      `[pkg.CSource(path: "lib/api.c", flags: [])]`,
			"c_imports":      `[pkg.CImport(name: "api", headers: ["lib/api.h"], defines: ["INITGUID"], macros: [], vendor: false)]`,
			"unsafe_modules": `["app"]`,
		}),
		"lib/api.h": `#include <stdint.h>
typedef unsigned short wchar_t;
struct HWND__ { int unused; };
typedef struct HWND__ *HWND;
#define HWND_MESSAGE ((HWND)-3)
#define WIDE_TAG L"ok"

typedef struct _GUID {
    uint32_t Data1;
    uint16_t Data2;
    uint16_t Data3;
    uint8_t  Data4[8];
} GUID;
extern const GUID IID_IApi;
#ifdef INITGUID
extern const GUID IID_IApi = { 0x11223344, 0x5566, 0x7788, { 1, 2, 3, 4, 5, 6, 7, 8 } };
#endif
static const uint64_t FLAG_HI = 0x100000000ULL;

typedef struct IApi IApi;
typedef struct IApiVtbl {
    int (*Add)(IApi *This, int a, int b);
    void (*Bump)(IApi *This, int delta);
} IApiVtbl;
struct IApi {
    const IApiVtbl *lpVtbl;
};

IApi *make_api(int init);
int read_api(IApi *p);
int check_hwnd(HWND h);
`,
		"lib/api.c": `#include "api.h"
#include <stdlib.h>
typedef struct ApiImpl {
    const IApiVtbl *lpVtbl;
    int val;
} ApiImpl;
static int impl_add(IApi *This, int a, int b) {
    return ((ApiImpl*)This)->val + a + b;
}
static void impl_bump(IApi *This, int delta) {
    ((ApiImpl*)This)->val += delta;
}
static const IApiVtbl g_vtbl = { impl_add, impl_bump };
IApi *make_api(int init) {
    ApiImpl *p = (ApiImpl*)malloc(sizeof(ApiImpl));
    p->lpVtbl = &g_vtbl;
    p->val = init;
    return (IApi*)p;
}
int read_api(IApi *p) { return ((ApiImpl*)p)->val; }
int check_hwnd(HWND h) { return h == HWND_MESSAGE ? 42 : 0; }
`,
		"app/main.mk": `import c.api
import std.raw

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    unsafe "calling C api and COM vtable methods" {
        obj := api.make_api(10)
        (*obj).Bump(5)
        sum := (*obj).Add(3, 4)
        h: api.Hwnd = raw.FromAddr[api.HwndStruct](api.HwndMessage)
        hw := api.check_hwnd(h)
        g := api.IidIApi
        try Print(mut out, "{sum} {hw} {d1} {f} {w0} {w2}\n",
            (sum: sum, hw: hw, d1: g.data1, f: api.FlagHi, w0: api.WideTag[0], w2: api.WideTag[2]))
    }
}
`,
	}
	dir := writeTree(t, files)
	s, err := NewSession(Config{Root: dir, Target: target, Profile: types.Dev, Tools: tools, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "better.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
	}
	out, errOut, code := runExe(t, target, paths[0], "")
	if want := "22 42 287454020 4294967296 111 0\n"; code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, want)
	}
}

func TestFFI_FnFromAddrOptional(t *testing.T) {
	tools := needTools(t)
	target := types.X86_64Windows
	files := map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":           `"fnfromaddr"`,
			"bins":           `[pkg.Bin(name: "fnfromaddr", root: "src", link_script: "", cpu: "", subsystem: "console")]`,
			"unsafe_modules": `["src"]`,
		}),
		"src/main.mk": `import std.raw

type OptF = ?fn(i32): i32

Double(x: i32): i32 {
    return x * 2
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    unsafe "FnFromAddr with optional fn type" {
        null_fn := raw.FnFromAddr[OptF](0)
        is_none := match null_fn {
            none { 1 }
            some(_) { 0 }
        }
        p := Double
        addr := raw.Addr(raw.PtrCast[fn(i32): i32, u8](raw.FromAddr[fn(i32): i32](0)))
        _ = p
        _ = addr
        try Print(mut out, "{is_none}\n", (is_none: is_none))
    }
}
`,
	}
	dir := writeTree(t, files)
	s, err := NewSession(Config{Root: dir, Target: target, Profile: types.Dev, Tools: tools, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "fnfromaddr.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
	}
	out, errOut, code := runExe(t, target, paths[0], "")
	if code != 0 || out != "1\n" {
		t.Errorf("exit %d, stdout %q, stderr %q; want \"1\\n\"", code, out, errOut)
	}

	// Verify COM vtable call outside unsafe is E1101 in driver too.
	comFiles := map[string]string{
		"pkg.mk": manifest(map[string]string{
			"name":      `"comchk"`,
			"bins":      `[pkg.Bin(name: "comchk", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"c_imports": `[pkg.CImport(name: "api", headers: ["api.h"], defines: [], macros: [], vendor: false)]`,
		}),
		"api.h": `typedef struct IObj IObj;
typedef struct IObjVtbl { int (*Ping)(IObj *This); } IObjVtbl;
struct IObj { const IObjVtbl *lpVtbl; };
`,
		"app/main.mk": `import c.api
import std.mem

export Main(sys: &System): WriteError!void {
    d := mem.Zeroed[api.IObj]()
    _ = d.Ping()
}
`,
	}
	s2, err := NewSession(Config{Root: writeTree(t, comFiles), Target: target, Profile: types.Dev, Tools: tools, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	r2 := s2.Check()
	found := false
	for _, d := range r2.Report.Diags {
		if d.Diag.Code == diag.E1101 && strings.Contains(d.Diag.Message, "vtable") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected E1101 for COM method outside unsafe, got:\n%s", r2.Report.Text(r2.Sources))
	}
}
