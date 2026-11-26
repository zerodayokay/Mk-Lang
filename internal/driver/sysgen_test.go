// Why: a shipped system view may hold only items that check, and its
// archives must be a function of its inputs; both need Clang, so they run
// on a small fake SDK wherever Clang is, and on the real SDK only when
// MK_CIMPORT_WINDOWS_SDK names it.
//
// What: TestSysGen_FakeSDK (generation twice, same bytes; requires; shim
// externs dropped), TestSysGen_DropsWhatDoesNotCheck (a planted bad item
// and its dependent are dropped, the rest kept) and TestSysGen_SDKVersion.
//
// How: a fake Windows Kits tree whose windows.h sits beside a winnt.h, so it
// reads as the Microsoft flavour, with one small header per table row.

package driver

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/sysview"
)

// fakeKits writes a Windows Kits tree with SDK version 10.0.1.0 (and an
// older 10.0.0.9 without um\windows.h) and returns its root.
func fakeKits(t *testing.T) string {
	inc := "Include/10.0.1.0/"
	return writeTree(t, map[string]string{
		"Include/10.0.0.9/um/readme.txt": "an incomplete SDK\n",
		inc + "ucrt/readme.txt":          "the ucrt\n",
		inc + "shared/readme.txt":        "shared\n",
		inc + "um/winnt.h":               "#pragma once\ntypedef void *HANDLE;\nint CloseHandle(HANDLE h);\n",
		inc + "um/windows.h": "#pragma once\n#include <winnt.h>\ntypedef unsigned long DWORD;\n" +
			"typedef struct HWND__ { int unused; } *HWND;\nDWORD GetCurrentProcessId(void);\nint DestroyWindow(HWND h);\n" +
			"static inline int MkFakeInline(void) { return 1; }\n#define WM_PAINT 0x000F\n",
		inc + "um/windowsx.h":       "#pragma once\n#define MK_FAKE_X 3\n",
		inc + "um/dxgidebug.h":      "#pragma once\n#define MK_DXGI_DEBUG 1\n",
		inc + "um/dxgi1_6.h":        "#pragma once\n#include <windows.h>\ntypedef struct IDXGIFactory { DWORD refs; } IDXGIFactory;\nint CreateDXGIFactory1(HWND h, IDXGIFactory **f);\n",
		inc + "um/d3d12sdklayers.h": "#pragma once\n#define MK_D3D12_LAYERS 2\n",
		inc + "um/d3d12.h":          "#pragma once\n#include <dxgi1_6.h>\nint D3D12CreateDevice(IDXGIFactory *f, HWND h);\n",
		inc + "um/d3dcompiler.h":    "#pragma once\n#include <d3d12.h>\nint D3DCompile(const void *src, DWORD n);\n",
	})
}

func TestSysGen_SDKVersion(t *testing.T) {
	root := fakeKits(t)
	dir, v, err := windowsSDKInclude(root)
	if err != nil || v != "10.0.1.0" || dir != filepath.Join(root, "Include", "10.0.1.0") {
		t.Fatalf("windowsSDKInclude = %s %s %v", dir, v, err)
	}
	if dir2, v2, _ := windowsSDKInclude(dir); dir2 != dir || v2 != v {
		t.Fatalf("an Include\\<version> directory itself: %s %s", dir2, v2)
	}
	if _, _, err := windowsSDKInclude(t.TempDir()); err == nil {
		t.Fatal("an empty directory is an SDK")
	}
	for _, c := range [][2]string{{"10.0.9.0", "10.0.10.0"}, {"10.0.1", "10.0.1.0"}, {"9", "10"}, {"10.0.1.0", "10.0.x"}} {
		if !versionLess(c[0], c[1]) || versionLess(c[1], c[0]) {
			t.Errorf("versionLess(%s, %s)", c[0], c[1])
		}
	}
}

func TestSysGen_FakeSDK(t *testing.T) {
	tools := needTools(t)
	root := fakeKits(t)
	gen := func() *SysGenResult {
		r, err := GenerateSystemViews(SysGenOptions{WindowsSDK: root, Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := gen(), gen()
	if len(a.Views) != 4 || len(b.Views) != 4 {
		t.Fatalf("%d and %d views; skipped %v", len(a.Views), len(b.Views), a.Skipped)
	}
	for i := range a.Views {
		if !bytes.Equal(a.Views[i].Archive, b.Views[i].Archive) {
			t.Errorf("c.%s: two generations differ", a.Views[i].View.Meta.Name)
		}
	}
	if !slices.ContainsFunc(a.Skipped, func(s string) bool { return strings.HasPrefix(s, "vulkan for x86_64-windows: skipped") }) {
		t.Errorf("skipped %v", a.Skipped)
	}
	got := map[string]sysview.Meta{}
	for _, v := range a.Views {
		got[v.View.Meta.Name] = v.View.Meta
	}
	w := got["windows"]
	if w.Target != "x86_64-windows" || w.Inputs["windows-sdk"] != "10.0.1.0" || w.Inputs["toolset-headers"] == "" || w.Inputs["clang"] == "" {
		t.Errorf("windows meta %+v", w)
	}
	for _, in := range w.Inputs {
		if strings.ContainsAny(in, `\/:`) {
			t.Errorf("an input names a path: %q", in)
		}
	}
	if !slices.Equal(got["dxgi"].Requires, []string{"windows"}) || !slices.Equal(got["d3d12"].Requires, []string{"windows", "dxgi"}) {
		t.Errorf("requires: dxgi %v, d3d12 %v", got["dxgi"].Requires, got["d3d12"].Requires)
	}
	// The static inline is in no DLL: dropped and listed.
	var dropped []string
	for _, d := range a.Views[0].Dropped {
		dropped = append(dropped, d.Name)
	}
	if !slices.Contains(dropped, "MkFakeInline") {
		t.Errorf("windows dropped %v", dropped)
	}
	arch, err := sysview.Open(a.Views[0].Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(arch.Lookup("MkFakeInline")) != 0 || len(arch.Lookup("GetCurrentProcessId")) != 1 || len(arch.Lookup("Hwnd")) == 0 {
		t.Error("the windows archive's items are wrong")
	}
}

// TestSysGen_DropsWhatDoesNotCheck: an item whose Mk does not check, and an
// item that needs it, are reported by the whole check; the rest is clean.
func TestSysGen_DropsWhatDoesNotCheck(t *testing.T) {
	tools := needTools(t)
	v := sysview.View{Meta: sysview.Meta{Name: "base", Target: "x86_64-windows"}, Items: []sysview.Item{
		{Name: "Good", Kind: "type", Defs: []string{"Good"}, Text: "// C: typedef int GOOD\nexport type Good = i32"},
		{Name: "Bad", Kind: "type", Defs: []string{"Bad"}, Text: "// C: typedef MISSING BAD\nexport type Bad = Missing"},
		{Name: "UsesBad", Kind: "func", Defs: []string{"UsesBad"}, Deps: []sysview.Ref{{View: 0, Item: 1}},
			Text: "// C: int UsesBad(BAD)\nexport extern UsesBad(b: Bad): i32"},
	}}
	bad, err := checkWholeViews(tools, "x86_64-windows", []sysview.View{v})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad["base"]) != 1 || bad["base"][1] == "" {
		t.Fatalf("bad %v", bad)
	}
	views, dropped, err := sysview.DropItems([]sysview.View{v}, bad)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 2 || len(views[0].Items) != 1 {
		t.Fatalf("dropped %+v", dropped)
	}
	if bad, err := checkWholeViews(tools, "x86_64-windows", views); err != nil || len(bad) != 0 {
		t.Fatalf("after the drop: %v %v", bad, err)
	}
}

// TestSysGen_ImplibLinks: import libraries populate Item.Link (classic
// DLLs eager, everything else Lazy: true), functions in no library stay
// empty, and still-ambiguous functions stay unlinked and are listed.
func TestSysGen_ImplibLinks(t *testing.T) {
	tools := needTools(t)
	inc := "Include/10.0.1.0/"
	libDir := "Lib/10.0.1.0/um/x64/"
	root := writeTree(t, map[string]string{
		inc + "ucrt/readme.txt":   "the ucrt\n",
		inc + "shared/readme.txt": "shared\n",
		inc + "um/winnt.h":        "#pragma once\ntypedef void *HANDLE;\nint CloseHandle(HANDLE h);\n",
		inc + "um/windows.h": "#pragma once\n#include <winnt.h>\ntypedef unsigned long DWORD;\n" +
			"typedef struct HWND__ { int unused; } *HWND;\nDWORD GetCurrentProcessId(void);\n" +
			"int BCryptGenRandom(void);\nint AmbigFn(void);\nint CrtOnly(void);\n",
		inc + "um/windowsx.h":       "#pragma once\n",
		inc + "um/dxgidebug.h":      "#pragma once\n",
		inc + "um/dxgi1_6.h":        "#pragma once\n#include <windows.h>\nint CreateDXGIFactory2(unsigned int f);\n",
		inc + "um/d3d12sdklayers.h": "#pragma once\n",
		inc + "um/d3d12.h":          "#pragma once\n#include <dxgi1_6.h>\nint D3D12CreateDevice(void);\n",
		inc + "um/d3dcompiler.h":    "#pragma once\n#include <d3d12.h>\nint D3DCompile(void);\n",
	})
	writeLib := func(name string, specs []shortImportSpec) {
		p := filepath.Join(root, filepath.FromSlash(libDir+name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, makeShortImportLib(specs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLib("kernel32.lib", []shortImportSpec{
		{memberName: "k1", sym: "CloseHandle", dll: "KERNEL32.dll", nameType: 1},
		{memberName: "k2", sym: "GetCurrentProcessId", dll: "KERNEL32.dll", nameType: 1},
	})
	writeLib("dxgi.lib", []shortImportSpec{
		{memberName: "dx1", sym: "CreateDXGIFactory2", dll: "dxgi.dll", nameType: 1},
	})
	writeLib("d3d12.lib", []shortImportSpec{
		{memberName: "d1", sym: "D3D12CreateDevice", dll: "d3d12.dll", nameType: 0, ordinal: 101},
	})
	writeLib("d3dcompiler.lib", []shortImportSpec{
		{memberName: "dc1", sym: "D3DCompile", dll: "D3DCOMPILER_47.dll", nameType: 1},
	})
	writeLib("bcrypt.lib", []shortImportSpec{
		{memberName: "b1", sym: "BCryptGenRandom", dll: "bcrypt.dll", nameType: 1},
	})
	writeLib("ncrypt.lib", []shortImportSpec{
		{memberName: "nc1", sym: "BCryptGenRandom", dll: "ncrypt.dll", nameType: 1},
	})
	writeLib("foo.lib", []shortImportSpec{{memberName: "f1", sym: "AmbigFn", dll: "foo.dll", nameType: 1}})
	writeLib("bar.lib", []shortImportSpec{{memberName: "b1", sym: "AmbigFn", dll: "bar.dll", nameType: 1}})

	res, err := GenerateSystemViews(SysGenOptions{WindowsSDK: root, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SysGenView{}
	for _, v := range res.Views {
		byName[v.View.Meta.Name] = v
	}
	linkOf := func(view, fn string) sysview.Link {
		a, err := sysview.Open(byName[view].Archive)
		if err != nil {
			t.Fatal(err)
		}
		idx := a.Lookup(fn)
		if len(idx) != 1 {
			t.Fatalf("%s.%s lookup = %v", view, fn, idx)
		}
		lk, err := a.ItemLink(idx[0])
		if err != nil {
			t.Fatal(err)
		}
		return lk
	}
	for _, tc := range []struct {
		view, fn string
		want     sysview.Link
	}{
		{"windows", "GetCurrentProcessId", sysview.Link{Lib: "kernel32.dll", Lazy: false}},
		{"windows", "BCryptGenRandom", sysview.Link{Lib: "bcrypt.dll", Lazy: true}},
		{"windows", "AmbigFn", sysview.Link{}},
		{"windows", "CrtOnly", sysview.Link{}},
		{"dxgi", "CreateDXGIFactory2", sysview.Link{Lib: "dxgi.dll", Lazy: true}},
		{"d3d12", "D3D12CreateDevice", sysview.Link{Lib: "d3d12.dll", Lazy: true}},
		{"d3dcompiler", "D3DCompile", sysview.Link{Lib: "d3dcompiler_47.dll", Lazy: true}},
	} {
		if got := linkOf(tc.view, tc.fn); got != tc.want {
			t.Errorf("%s.%s Link = %+v, want %+v", tc.view, tc.fn, got, tc.want)
		}
	}
	win := byName["windows"]
	if len(win.Ambiguous) != 1 || win.Ambiguous[0].Name != "AmbigFn" || !slices.Equal(win.Ambiguous[0].Libs, []string{"bar.dll", "foo.dll"}) {
		t.Fatalf("windows Ambiguous = %+v", win.Ambiguous)
	}
	a, _ := sysview.Open(win.Archive)
	txt := sysview.ManifestTextUnlinked("x86_64-windows", []sysview.Meta{win.View.Meta}, [][32]byte{a.ID()},
		[][]sysview.Dropped{win.Dropped}, [][]sysview.Ambiguous{win.Ambiguous})
	if !strings.Contains(txt, "  unlinked  AmbigFn: ambiguous (bar.dll, foo.dll)\n") {
		t.Errorf("views.txt missing unlinked entry:\n%s", txt)
	}
}
