// Why: the views mk carries must let a program create a window and a
// Direct3D 12 device with no pkg.CImport and no header, and a regenerated
// view must equal the committed one; only the real views and a Windows host
// show it.
//
// What: TestSysView_RealWindow, TestSysView_RealWarpDevice (both on a
// Windows host with the embedded views), TestSysView_RealHelloSize (a
// program calling one function of c.windows is about hello's size) and
// TestSysView_EmbeddedMatchesSDK (only when MK_CIMPORT_WINDOWS_SDK names
// the SDK the views were generated from).
//
// How: packages with no C import; the DLLs outside the system set mk
// links on a Windows host (dxgi.dll, d3d12.dll) are named by pkg.Link; the
// interface IDs are Mk values, so no dxguid.lib is linked.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/sysview"
	"mk/internal/types"
)

// realViews skips unless the host can run x86_64-windows programs and this
// mk carries the views names.
func realViews(t *testing.T, names ...string) {
	t.Helper()
	if HostTarget() != types.X86_64Windows {
		t.Skip("the program runs on a Windows host")
	}
	for _, n := range names {
		if _, ok := sysview.Embedded("x86_64-windows", n); !ok {
			t.Skipf("this mk carries no system view c.%s (go run ./tools/sysview makes it)", n)
		}
	}
}

// realBuild builds and runs the package files, returning its output.
func realBuild(t *testing.T, files map[string]string, release bool) (string, int64) {
	t.Helper()
	cfg := Config{Root: writeTree(t, files), Target: types.X86_64Windows, Tools: needTools(t), CacheDir: t.TempDir()}
	if release {
		cfg.Profile = types.Release
	}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "p.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build:\n%.4000s", r.Report.Text(r.Sources))
	}
	out, errOut, code := runExe(t, types.X86_64Windows, paths[0], "")
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	fi, err := os.Stat(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return out, fi.Size()
}

// TestSysView_RealWindow registers a class, creates a hidden window and
// destroys it through c.windows alone: user32 and kernel32 are system
// DLLs, so the package names no library.
func TestSysView_RealWindow(t *testing.T) {
	realViews(t, "windows")
	main := strings.ReplaceAll(strings.ReplaceAll(sdkWindowMain, "import c.dx\n", "import c.windows\n"), "dx.", "windows.")
	out, _ := realBuild(t, sysPkg(main, nil), false)
	if want := "registered true created true destroyed 1\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// sysWarpMain creates a DXGI factory, its WARP adapter and a D3D12 device
// on it through the COM tables, and prints the device's node count.
const sysWarpMain = `import c.d3d12
import c.dxgi
import c.windows
import std.raw

Iid(a: u32, b: u16, c: u16, d: [8]u8): windows.Iid {
    return windows.Guid(data1: a, data2: b, data3: c, data4: d)
}

export Main(sys: &System): WriteError!void {
    var factory_iid := Iid(0xc1b6694f, 0xff09, 0x44a9, [0xb0, 0x3c, 0x77, 0x90, 0x0a, 0x0a, 0x1d, 0x17])
    var adapter_iid := Iid(0x29038f61, 0x3839, 0x4626, [0x91, 0xfd, 0x08, 0x68, 0x79, 0x01, 0x1a, 0x05])
    var device_iid := Iid(0x189819f1, 0x1db6, 0x4b57, [0xbe, 0x54, 0x18, 0x21, 0x33, 0x9b, 0x85, 0xf7])
    var hr: i32 = 0
    var nodes: u32 = 0
    var factory: *void = raw.Null[void]()
    var adapter: *void = raw.Null[void]()
    var device: *void = raw.Null[void]()
    unsafe "each out pointer is written by the call it is passed to; each COM pointer is released once" {
        hr = dxgi.CreateDXGIFactory2(0, raw.OfMut(mut factory_iid), raw.OfMut(mut factory))
        if hr >= 0 {
            f := raw.PtrCast[void, dxgi.IdxgiFactory6](factory)
            hr = (*(*f).lp_vtbl).enum_warp_adapter.Must()(f, raw.OfMut(mut adapter_iid), raw.OfMut(mut adapter))
        }
        if hr >= 0 {
            hr = d3d12.D3D12CreateDevice(raw.PtrCast[void, windows.IUnknown](adapter), d3d12.D3dFeatureLevel110, raw.OfMut(mut device_iid), raw.OfMut(mut device))
        }
        if hr >= 0 {
            dv := raw.PtrCast[void, d3d12.ID3D12Device](device)
            nodes = (*(*dv).lp_vtbl).get_node_count.Must()(dv)
            _ = (*(*dv).lp_vtbl).release.Must()(dv)
        }
        if !raw.IsNull(adapter) {
            u := raw.PtrCast[void, windows.IUnknown](adapter)
            _ = (*(*u).lp_vtbl).release.Must()(u)
        }
        if !raw.IsNull(factory) {
            u := raw.PtrCast[void, windows.IUnknown](factory)
            _ = (*(*u).lp_vtbl).release.Must()(u)
        }
    }
    try sys.Print("hr {hr} nodes {nodes}\n", (hr, nodes))
}
`

// TestSysView_RealWarpDevice: c.dxgi and c.d3d12 make a WARP device. The
// two pkg.Link lines name the DLLs outside the system set.
func TestSysView_RealWarpDevice(t *testing.T) {
	realViews(t, "windows", "dxgi", "d3d12")
	files := sysPkg(sysWarpMain, map[string]string{"links": `[pkg.Link(name: "dxgi.dll", kind: "dynamic", path: "", target: "x86_64-windows"), ` +
		`pkg.Link(name: "d3d12.dll", kind: "dynamic", path: "", target: "x86_64-windows")]`})
	out, _ := realBuild(t, files, false)
	if want := "hr 0 nodes 1\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// TestSysView_RealHelloSize: a release program that calls one function of
// c.windows carries nothing of the rest of the view: it is within a page of
// the same program without the call.
func TestSysView_RealHelloSize(t *testing.T) {
	realViews(t, "windows")
	hello := "export Main(sys: &System): WriteError!void {\n    var pid: u32 = 1\n    try sys.Print(\"{x}\\n\", (x: pid > 0))\n}\n"
	_, plain := realBuild(t, sysPkg(hello, nil), true)
	out, with := realBuild(t, sysPkg(sysGuardedMain, nil), true)
	if out != "true\n" {
		t.Errorf("stdout %q", out)
	}
	t.Logf("release: %d bytes without the view, %d with GetCurrentProcessId", plain, with)
	if with > plain+4096 {
		t.Errorf("the program with one view function is %d bytes, %d more than without", with, with-plain)
	}
}

// TestSysView_EmbeddedMatchesSDK: the views generated from the SDK at
// MK_CIMPORT_WINDOWS_SDK are the views mk carries, when it is the version
// they were generated from.
func TestSysView_EmbeddedMatchesSDK(t *testing.T) {
	root := os.Getenv("MK_CIMPORT_WINDOWS_SDK")
	if root == "" {
		t.Skip(`MK_CIMPORT_WINDOWS_SDK is not set (the Windows SDK's Include\<version>, e.g. C:\Program Files (x86)\Windows Kits\10\Include\10.0.28000.0)`)
	}
	res, err := GenerateSystemViews(SysGenOptions{WindowsSDK: root, Tools: needTools(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Views {
		m := v.View.Meta
		old, ok := sysview.Embedded(m.Target, m.Name)
		if !ok {
			t.Errorf("c.%s for %s: mk carries no such view", m.Name, m.Target)
			continue
		}
		oa, err := sysview.Open(old)
		if err != nil {
			t.Fatal(err)
		}
		if oa.Meta().Inputs[string(sysview.InputWindowsSDK)] != m.Inputs[string(sysview.InputWindowsSDK)] {
			t.Skipf("the SDK at %s is %s; the views were generated from %s", root, m.Inputs[string(sysview.InputWindowsSDK)], oa.Meta().Inputs[string(sysview.InputWindowsSDK)])
		}
		na, _ := sysview.Open(v.Archive)
		if oa.ID() != na.ID() {
			t.Errorf("c.%s for %s differs from the view generated at HEAD: regenerate it with go run ./tools/sysview", m.Name, m.Target)
		}
	}
}
