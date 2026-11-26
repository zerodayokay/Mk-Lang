// Why: the Windows SDK is the header set the Microsoft flavour exists for,
// and only the real one shows that windows.h, the Direct3D 12, DXGI and
// shader compiler headers parse and bind with mk's own toolset headers.
// What: TestCImport_WindowsSDK and _WindowsSDKHeaders, run only when
// MK_CIMPORT_WINDOWS_SDK names the SDK's Include\<version> directory (CI's
// runners have no SDK).
// How: imports of the SDK's headers for its um, shared and ucrt
// directories through SystemFor; Translate fails on any Clang error in a
// header, so success is 0 errors; then the bound share and the names the
// toolset headers fix.

package cimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdkInclude is the Windows SDK's include directories named by
// MK_CIMPORT_WINDOWS_SDK (its Include\<version>), or a skip.
func sdkInclude(t *testing.T) []string {
	t.Helper()
	root := os.Getenv("MK_CIMPORT_WINDOWS_SDK")
	if root == "" {
		t.Skip(`MK_CIMPORT_WINDOWS_SDK is not set (the Windows SDK's Include\<version>, e.g. C:\Program Files (x86)\Windows Kits\10\Include\10.0.28000.0)`)
	}
	var dirs []string
	for _, sub := range []string{"um", "shared", "ucrt"} {
		d := filepath.Join(root, sub)
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Skipf("MK_CIMPORT_WINDOWS_SDK=%s has no %s directory", root, sub)
		}
		dirs = append(dirs, d)
	}
	return dirs
}

// TestCImport_WindowsSDK: the four headers in one import translate with
// no Clang error under the MSVC triple, bind at least 94% of their
// declarations, map size_t to SizeT as every other target does, and bind
// each redeclared IID once.
func TestCImport_WindowsSDK(t *testing.T) {
	dirs := sdkInclude(t)
	tl, rd := clangFor(t)
	sys, err := SystemFor("x86_64-windows", dirs, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if sys.Flavour != FlavourMicrosoft || sys.Triple != "x86_64-pc-windows-msvc" {
		t.Fatalf("system %+v", sys)
	}
	res, err := Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: sys.Triple,
		Imports: []Import{{Name: "dx", Headers: []string{"windows.h", "d3d12.h", "dxgi1_6.h", "d3dcompiler.h"}}},
		Include: []string{t.TempDir()}, Sysroot: sys.Dirs, WorkDir: filepath.Join(t.TempDir(), "w")})
	if err != nil {
		if ce, ok := err.(*Error); ok {
			t.Fatalf("%v\n%.4000s", ce, ce.Stderr)
		}
		t.Fatal(err)
	}
	st := res.Stats[0]
	share := float64(st.Bound()) / float64(st.Decls)
	t.Logf("bound %d of %d (%s), %d through the shim's functions; skipped %v", st.Bound(), st.Decls, pct(st.Bound(), st.Decls), len(res.ShimFuncs), st.Skipped)
	if share < 0.94 {
		t.Errorf("bound %s, want at least 94%%", pct(st.Bound(), st.Decls))
	}
	view := res.Modules[0]
	dumpIf(t, "windows-sdk.mk", view)
	for _, want := range []string{"export type SizeT = uint\n", "export type SizeTType = UlongPtr\n", "export extern CreateWindowExW(",
		"export extern D3D12CreateDevice(", "export extern CreateDXGIFactory2(", "export extern D3DCompile("} {
		if !strings.Contains(view, want) {
			t.Errorf("the view lacks %q", want)
		}
	}
	if n := strings.Count(view, "export extern var IID_ID3D12Device:"); n != 1 {
		t.Errorf("IID_ID3D12Device bound %d times, want once", n)
	}
	if n := st.Skipped["extern var redeclared with another type"]; n != 0 {
		t.Errorf("%d redeclared extern vars disagree in type", n)
	}
}

// TestCImport_WindowsSDKHeaders: with mk's headers, every header the
// first system views name (windows.h, windowsx.h, d3d12.h,
// d3d12sdklayers.h, dxgi1_6.h, dxgidebug.h, d3dcompiler.h) translates in
// one import with no Clang error, and so does the ucrt's stdlib.h alone;
// wchar.h alone fails, naming the setjmp.h mk does not supply (Clang's
// intrin.h includes it).
func TestCImport_WindowsSDKHeaders(t *testing.T) {
	dirs := sdkInclude(t)
	tl, rd := clangFor(t)
	sys, err := SystemFor("x86_64-windows", dirs, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(headers ...string) (*Result, error) {
		return Translate(context.Background(), tl, rd, Request{Target: "x86_64-windows", Triple: sys.Triple,
			Imports: []Import{{Name: "h", Headers: headers}}, Include: []string{t.TempDir()}, Sysroot: sys.Dirs,
			WorkDir: filepath.Join(t.TempDir(), "w")})
	}
	for _, hs := range [][]string{
		{"windows.h", "windowsx.h", "d3d12.h", "d3d12sdklayers.h", "dxgi1_6.h", "dxgidebug.h", "d3dcompiler.h"},
		{"stdlib.h"},
	} {
		res, err := run(hs...)
		if err != nil {
			if ce, ok := err.(*Error); ok {
				t.Fatalf("%v: %v\n%.4000s", hs, ce, ce.Stderr)
			}
			t.Fatal(err)
		}
		st := res.Stats[0]
		t.Logf("%v: bound %d of %d (%s)", hs, st.Bound(), st.Decls, pct(st.Bound(), st.Decls))
	}
	_, err = run("wchar.h")
	ce, ok := err.(*Error)
	if !ok || !strings.Contains(ce.Stderr, "'setjmp.h' file not found") {
		t.Errorf("wchar.h alone: %v, want an error naming setjmp.h", err)
	}
}
