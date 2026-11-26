// Why: system view link entries (lazy outside the classic set) and sdk: true
// import-library resolution must work end-to-end without pkg.Link, never
// guess on ambiguity, and obey resolution precedence; CI runs without a
// Windows SDK, so fake-SDK tests run everywhere and a real-SDK test skips
// when MK_CIMPORT_WINDOWS_SDK is absent.
//
// What: TestSysView_AutoLinkAndLazy, TestSysView_LinkPrecedence,
// TestSDKImport_AutoLink, TestSDKImport_AmbiguousError, and
// TestSysView_RealSDKAutoLinkWarp.
//
// How: fake Windows Kits trees with Include/<ver>/um and Lib/<ver>/um/x64
// archives built by makeShortImportLib drive GenerateSystemViews and
// sdk: true builds; PE import tables verify eager vs lazy binding.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/types"
)

// fakeAutoLinkKits writes a Windows Kits tree with headers and import
// libraries for windows (kernel32, user32), dxgi, d3d12 (IMPORT_ORDINAL
// like the real SDK), plus optional extra libs.
func fakeAutoLinkKits(t *testing.T, extraLibs map[string][]shortImportSpec) string {
	t.Helper()
	inc := "Include/10.0.1.0/"
	libDir := "Lib/10.0.1.0/um/x64/"
	root := writeTree(t, map[string]string{
		inc + "ucrt/readme.txt":   "the ucrt\n",
		inc + "shared/readme.txt": "shared\n",
		inc + "um/winnt.h":        "#pragma once\ntypedef void *HANDLE;\nint CloseHandle(HANDLE h);\n",
		inc + "um/windows.h": "#pragma once\n#include <winnt.h>\ntypedef unsigned long DWORD;\n" +
			"DWORD GetCurrentProcessId(void);\nint DestroyWindow(void *h);\n",
		inc + "um/windowsx.h":       "#pragma once\n",
		inc + "um/dxgidebug.h":      "#pragma once\n",
		inc + "um/dxgi1_6.h":        "#pragma once\n#include <windows.h>\nint CreateDXGIFactory2(unsigned int flags);\n",
		inc + "um/d3d12sdklayers.h": "#pragma once\n",
		inc + "um/d3d12.h":          "#pragma once\n#include <dxgi1_6.h>\nint D3D12CreateDevice(void *h);\n",
		inc + "um/d3dcompiler.h":    "#pragma once\n#include <d3d12.h>\nint D3DCompile(void);\n",
	})
	libs := map[string][]shortImportSpec{
		"kernel32.lib": {
			{memberName: "k1", sym: "CloseHandle", dll: "KERNEL32.dll", nameType: 1},
			{memberName: "k2", sym: "GetCurrentProcessId", dll: "KERNEL32.dll", nameType: 1},
		},
		"user32.lib": {
			{memberName: "u1", sym: "DestroyWindow", dll: "USER32.dll", nameType: 1},
		},
		"dxgi.lib": {
			{memberName: "dx1", sym: "CreateDXGIFactory2", dll: "dxgi.dll", nameType: 1},
		},
		"d3d12.lib": {
			{memberName: "d1", sym: "D3D12CreateDevice", dll: "d3d12.dll", nameType: 0, ordinal: 101},
		},
		"d3dcompiler.lib": {
			{memberName: "dc1", sym: "D3DCompile", dll: "D3DCOMPILER_47.dll", nameType: 1},
		},
	}
	for k, v := range extraLibs {
		libs[k] = v
	}
	for name, specs := range libs {
		p := filepath.Join(root, filepath.FromSlash(libDir+name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, makeShortImportLib(specs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// importedDLLs returns the lowercased unique DLL names in exe's PE import table.
func importedDLLs(t *testing.T, exe string) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	for _, s := range imports(t, exe) {
		dll, _, _ := strings.Cut(s, ":")
		dll = strings.ToLower(dll)
		if dll != "" && !seen[dll] {
			seen[dll] = true
			out = append(out, dll)
		}
	}
	slices.Sort(out)
	return out
}

func TestSysView_AutoLinkAndLazy(t *testing.T) {
	tools := needTools(t)
	sdk := fakeAutoLinkKits(t, nil)
	res, err := GenerateSystemViews(SysGenOptions{WindowsSDK: sdk, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{}
	for _, v := range res.Views {
		archives[v.View.Meta.Name] = v.Archive
	}
	main := `import c.d3d12
import c.dxgi
import c.windows
import std.dl
import std.raw

export Main(sys: &System): WriteError!void {
    var pid: windows.Dword = 0
    var p1 := false
    var p2 := false
    var fac: i32 = 0
    var dev: i32 = 0
    unsafe "test" {
        pid = windows.GetCurrentProcessId()
        p1 = dl.Present(sys.io, dxgi.CreateDXGIFactory2)
        p2 = dl.Present(sys.io, d3d12.D3D12CreateDevice)
        if p1 {
            fac = dxgi.CreateDXGIFactory2(0)
        }
        if p2 {
            dev = d3d12.D3D12CreateDevice(raw.Null[void]())
        }
    }
    try sys.Print("pid {x} p1 {p1} p2 {p2} fac {fac} dev {dev}\n", (x: pid > 0, p1, p2, fac, dev))
}
`
	root := writeTree(t, sysPkg(main, nil))
	outDir := t.TempDir()
	exe := filepath.Join(outDir, "app.exe")
	cfg := Config{Root: root, Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.SysViews = func(target, name string) ([]byte, bool) {
		if target != "x86_64-windows" {
			return nil, false
		}
		b, ok := archives[name]
		return b, ok
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
	}
	dlls := importedDLLs(t, exe)
	if !slices.Contains(dlls, "kernel32.dll") {
		t.Errorf("PE imports %v missing kernel32.dll", dlls)
	}
	if slices.Contains(dlls, "dxgi.dll") || slices.Contains(dlls, "d3d12.dll") {
		t.Errorf("PE imports %v should bind dxgi.dll and d3d12.dll lazily, not eagerly", dlls)
	}
	lb := s.w.lazyBinds()
	if lb == nil || lb.of["CreateDXGIFactory2"] == nil || lb.of["CreateDXGIFactory2"].file != "dxgi.dll" ||
		lb.of["D3D12CreateDevice"] == nil || lb.of["D3D12CreateDevice"].file != "d3d12.dll" {
		t.Fatalf("lazyBinds = %+v", lb)
	}
	if HostTarget() == types.X86_64Windows {
		// Place fake dxgi.dll and d3d12.dll beside exe to test actual lazy
		// loading and calling through the generated slots.
		cDLL(t, tools, outDir, "dxgi.dll", "__declspec(dllexport) int CreateDXGIFactory2(unsigned int flags) { return 42; }\n")
		cDLL(t, tools, outDir, "d3d12.dll", "__declspec(dllexport) int D3D12CreateDevice(void *h) { return 99; }\n")
		out, errOut, code := runExe(t, types.X86_64Windows, exe, "")
		if code != 0 || out != "pid true p1 true p2 true fac 42 dev 99\n" {
			t.Fatalf("run: code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	}
}

func TestSysView_LinkPrecedence(t *testing.T) {
	tools := needTools(t)
	sdk := fakeAutoLinkKits(t, nil)
	res, err := GenerateSystemViews(SysGenOptions{WindowsSDK: sdk, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{}
	for _, v := range res.Views {
		archives[v.View.Meta.Name] = v.Archive
	}
	main := `import c.dxgi
import c.windows
import std.raw

export Main(sys: &System): WriteError!void {
    var fac: i32 = 0
    var d: i32 = 0
    unsafe "test" {
        fac = dxgi.CreateDXGIFactory2(0)
        d = windows.DestroyWindow(raw.Null[void]())
    }
    try sys.Print("{fac} {d}\n", (fac, d))
}
`
	// 1. Explicit pkg.Link kind: "dynamic" overrides c.dxgi's Lazy: true.
	// 2. testNoSystem32 simulates a non-Windows host so DestroyWindow
	//    resolves from c.windows' Link entry (user32.dll, eager).
	old := testNoSystem32
	testNoSystem32 = true
	defer func() { testNoSystem32 = old }()

	files := sysPkg(main, map[string]string{
		"links": `[pkg.Link(name: "dxgi.dll", kind: "dynamic", path: "dxgi.def", target: "x86_64-windows")]`,
	})
	files["dxgi.def"] = "CreateDXGIFactory2\n"
	exe := filepath.Join(t.TempDir(), "p.exe")
	cfg := Config{Root: writeTree(t, files), Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.SysViews = func(target, name string) ([]byte, bool) {
		if target != "x86_64-windows" {
			return nil, false
		}
		b, ok := archives[name]
		return b, ok
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
	}
	dlls := importedDLLs(t, exe)
	for _, want := range []string{"dxgi.dll", "kernel32.dll", "user32.dll"} {
		if !slices.Contains(dlls, want) {
			t.Errorf("PE imports %v missing %s", dlls, want)
		}
	}
}

func TestSDKImport_AutoLink(t *testing.T) {
	tools := needTools(t)
	clearSysroots(t)
	sdk := fakeAutoLinkKits(t, map[string][]shortImportSpec{
		"mkfake.lib": {{memberName: "m1", sym: "MkFakeMul", dll: "mkfake.dll", nameType: 1}},
	})
	sdkDirs := windowsSDKDirs(filepath.Join(sdk, "Include", "10.0.1.0"))

	main := `import c.ext

export Main(sys: &System): WriteError!void {
    var x: i32 = 0
    var y: i32 = 0
    unsafe "test" {
        x = ext.MkFakeMul(6, 7)
        y = ext.MkShimCall(5)
    }
    try sys.Print("{x} {y}\n", (x, y))
}
`
	files := sysPkg(main, map[string]string{
		"c_imports": `[pkg.CImport(name: "ext", headers: ["ext.h"], defines: [], macros: [], vendor: false, include_dirs: [], sdk: true)]`,
	})
	files["ext.h"] = "#include <windows.h>\nint MkFakeMul(int a, int b);\nstatic inline int MkShimCall(int v) { return MkFakeMul(v, 3); }\n"

	outDir := t.TempDir()
	exe := filepath.Join(outDir, "sdk.exe")
	cfg := Config{Root: writeTree(t, files), Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.HostSDK = func(target string) ([]string, string) {
		if target != "x86_64-windows" {
			return nil, ""
		}
		return sdkDirs, "fake SDK"
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build failed:\n%s", r.Report.Text(r.Sources))
	}
	imps := imports(t, exe)
	if !slices.Contains(imps, "mkfake.dll:MkFakeMul") {
		t.Errorf("PE imports %v missing mkfake.dll:MkFakeMul", imps)
	}
	if HostTarget() == types.X86_64Windows {
		cDLL(t, tools, outDir, "mkfake.dll", "__declspec(dllexport) int MkFakeMul(int a, int b) { return a * b; }\n")
		out, errOut, code := runExe(t, types.X86_64Windows, exe, "")
		if code != 0 || out != "42 15\n" {
			t.Fatalf("run: code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	}
}

func TestSDKImport_AmbiguousError(t *testing.T) {
	tools := needTools(t)
	clearSysroots(t)
	sdk := fakeAutoLinkKits(t, map[string][]shortImportSpec{
		"mkambig1.lib": {{memberName: "a1", sym: "MkAmbigFn", dll: "mkambig1.dll", nameType: 1}},
		"mkambig2.lib": {{memberName: "a2", sym: "MkAmbigFn", dll: "mkambig2.dll", nameType: 1}},
	})
	sdkDirs := windowsSDKDirs(filepath.Join(sdk, "Include", "10.0.1.0"))

	main := `import c.ext

export Main(sys: &System): WriteError!void {
    var x: i32 = 0
    unsafe "test" {
        x = ext.MkAmbigFn()
    }
    try sys.Print("{x}\n", (x))
}
`
	files := sysPkg(main, map[string]string{
		"c_imports": `[pkg.CImport(name: "ext", headers: ["ext.h"], defines: [], macros: [], vendor: false, include_dirs: [], sdk: true)]`,
	})
	files["ext.h"] = "#include <windows.h>\nint MkAmbigFn(void);\n"

	cfg := Config{Root: writeTree(t, files), Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.HostSDK = func(target string) ([]string, string) {
		if target != "x86_64-windows" {
			return nil, ""
		}
		return sdkDirs, "fake SDK"
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.Build(filepath.Join(t.TempDir(), "ambig.exe"))
	if err != nil {
		t.Fatal(err)
	}
	txt := r.Report.Text(r.Sources)
	for _, want := range []string{"E0001", "MkAmbigFn", "mkambig1.dll", "mkambig2.dll", "pkg.Link("} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %q in diagnostic:\n%s", want, txt)
		}
	}

	// Adding pkg.Link settles the ambiguity and builds cleanly.
	files["pkg.mk"] = manifest(map[string]string{
		"name":           `"p"`,
		"bins":           `[pkg.Bin(name: "p", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
		"unsafe_modules": `["app"]`,
		"c_imports":      `[pkg.CImport(name: "ext", headers: ["ext.h"], defines: [], macros: [], vendor: false, include_dirs: [], sdk: true)]`,
		"links":          `[pkg.Link(name: "mkambig1.dll", kind: "dynamic", path: "mkambig1.def", target: "x86_64-windows")]`,
	})
	files["mkambig1.def"] = "MkAmbigFn\n"
	cfg.Root = writeTree(t, files)
	cfg.CacheDir = t.TempDir()
	s2, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "resolved.exe")
	r2, paths, err := s2.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("resolved build failed:\n%s", r2.Report.Text(r2.Sources))
	}
	if imps := imports(t, exe); !slices.Contains(imps, "mkambig1.dll:MkAmbigFn") {
		t.Errorf("PE imports %v missing mkambig1.dll:MkAmbigFn", imps)
	}
}

func TestSysView_RealSDKAutoLinkWarp(t *testing.T) {
	root := os.Getenv("MK_CIMPORT_WINDOWS_SDK")
	if root == "" {
		t.Skip("MK_CIMPORT_WINDOWS_SDK is not set")
	}
	if HostTarget() != types.X86_64Windows {
		t.Skip("the program runs on a Windows host")
	}
	tools := needTools(t)
	res, err := GenerateSystemViews(SysGenOptions{WindowsSDK: root, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{}
	for _, v := range res.Views {
		archives[v.View.Meta.Name] = v.Archive
	}
	exe := filepath.Join(t.TempDir(), "warp.exe")
	cfg := Config{Root: writeTree(t, sysPkg(sysWarpMain, nil)), Target: types.X86_64Windows, Tools: tools, CacheDir: t.TempDir()}
	cfg.TestHooks.SysViews = func(target, name string) ([]byte, bool) {
		if target != "x86_64-windows" {
			return nil, false
		}
		b, ok := archives[name]
		return b, ok
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("warp build failed:\n%s", r.Report.Text(r.Sources))
	}
	dlls := importedDLLs(t, exe)
	if !slices.Contains(dlls, "kernel32.dll") {
		t.Errorf("PE imports %v missing kernel32.dll", dlls)
	}
	if slices.Contains(dlls, "dxgi.dll") || slices.Contains(dlls, "d3d12.dll") {
		t.Errorf("PE imports %v should bind dxgi.dll and d3d12.dll lazily", dlls)
	}
	out, errOut, code := runExe(t, types.X86_64Windows, exe, "")
	if code != 0 || out != "hr 0 nodes 1\n" {
		t.Fatalf("warp run: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
