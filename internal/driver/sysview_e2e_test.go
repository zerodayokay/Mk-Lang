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
	"slices"
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

// sysZeroConfigMain creates a hidden window and a D3D12 WARP device using
// only system views (c.windows, c.dxgi, c.d3d12): no pkg.CImport, no
// pkg.Link, no dxguid.lib (uses INITGUID consts and COM methods).
const sysZeroConfigMain = `import c.d3d12
import c.dxgi
import c.windows
import std.mem
import std.raw

ZeroWndProc(hwnd: windows.Hwnd, msg: u32, wp: u64, lp: i64): i64 {
    unsafe "DefWindowProcW is the default handler for any message" {
        return windows.DefWindowProcW(hwnd, msg, wp, lp)
    }
}

export Main(sys: &System): WriteError!void {
    cls: [8]u16 = [0x6d, 0x6b, 0x7a, 0x65, 0x72, 0x6f, 0x77, 0]
    var atom: u16 = 0
    var made := false
    var destroyed: i32 = 0
    var hr: i32 = 0
    var nodes: u32 = 0
    var factory_iid := dxgi.IidIdxgiFactory6
    var adapter_iid := dxgi.IidIdxgiAdapter1
    var device_iid := d3d12.IidID3D12Device
    var factory: *void = raw.Null[void]()
    var adapter: *void = raw.Null[void]()
    var device: *void = raw.Null[void]()
    unsafe "class name outlives the window; COM out pointers are written by each call and released once" {
        inst := windows.GetModuleHandleW(raw.Null[u16]())
        var wc := mem.Zeroed[windows.Wndclassexw]()
        wc.cb_size = Cast[u32](SizeOf[windows.Wndclassexw]())
        wc.lpfn_wnd_proc = some(ZeroWndProc)
        wc.h_instance = inst
        wc.lpsz_class_name = raw.Data(cls[..])
        atom = windows.RegisterClassExW(raw.OfMut(mut wc))
        hwnd := windows.CreateWindowExW(0, raw.Data(cls[..]), raw.Data(cls[..]), 0, 0, 0, 64, 64, raw.Null[windows.HwndStruct](), raw.Null[windows.HmenuStruct](), inst, raw.Null[void]())
        made = !raw.IsNull(hwnd)

        hr = dxgi.CreateDXGIFactory2(0, raw.OfMut(mut factory_iid), raw.OfMut(mut factory))
        if hr >= 0 {
            f := raw.PtrCast[void, dxgi.IdxgiFactory6](factory)
            hr = (*f).EnumWarpAdapter(raw.OfMut(mut adapter_iid), raw.OfMut(mut adapter))
        }
        if hr >= 0 {
            hr = d3d12.D3D12CreateDevice(raw.PtrCast[void, windows.IUnknown](adapter), d3d12.D3dFeatureLevel110, raw.OfMut(mut device_iid), raw.OfMut(mut device))
        }
        if hr >= 0 {
            dv := raw.PtrCast[void, d3d12.ID3D12Device](device)
            nodes = (*dv).GetNodeCount()
            _ = (*dv).Release()
        }
        if !raw.IsNull(adapter) {
            u := raw.PtrCast[void, windows.IUnknown](adapter)
            _ = (*u).Release()
        }
        if !raw.IsNull(factory) {
            u := raw.PtrCast[void, windows.IUnknown](factory)
            _ = (*u).Release()
        }
        destroyed = windows.DestroyWindow(hwnd)
    }
    registered := atom != 0
    try sys.Print("registered {registered} created {made} destroyed {destroyed} hr {hr} nodes {nodes}\n", (registered, made, destroyed, hr, nodes))
}
`

// TestSysView_ZeroConfigWindowAndWarp verifies zero-config FFI: hidden
// window + D3D12 WARP device using only c.windows, c.dxgi, c.d3d12 with no
// pkg.CImport, no pkg.Link (auto-linked lazily), no dxguid.lib (INITGUID
// consts), and COM method calls.
func TestSysView_ZeroConfigWindowAndWarp(t *testing.T) {
	realViews(t, "windows", "dxgi", "d3d12")
	exe := filepath.Join(t.TempDir(), "zero_warp.exe")
	cfg := Config{Root: writeTree(t, sysPkg(sysZeroConfigMain, nil)), Target: types.X86_64Windows, Tools: needTools(t), CacheDir: t.TempDir()}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build:\n%.4000s", r.Report.Text(r.Sources))
	}
	dlls := importedDLLs(t, exe)
	for _, eager := range []string{"kernel32.dll", "user32.dll"} {
		if !slices.Contains(dlls, eager) {
			t.Errorf("PE imports %v missing eager %s", dlls, eager)
		}
	}
	for _, lazy := range []string{"dxgi.dll", "d3d12.dll"} {
		if slices.Contains(dlls, lazy) {
			t.Errorf("PE imports %v should bind %s lazily, not eagerly", dlls, lazy)
		}
	}
	out, errOut, code := runExe(t, types.X86_64Windows, exe, "")
	if code != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if want := "registered true created true destroyed 1 hr 0 nodes 1\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// TestSysView_VulkanWindowsAndCrossTarget verifies c.vulkan binds
// PFN_vk*, VK_HEADER_VERSION, VK_PIPELINE_STAGE_2_*, and
// VkWin32SurfaceCreateInfoKHR cleanly on x86_64-windows and checks/compiles
// across x86_64-windows and x86_64-linux.
func TestSysView_VulkanWindowsAndCrossTarget(t *testing.T) {
	_, ok := sysview.Embedded("x86_64-windows", "vulkan")
	if !ok {
		t.Skip("this mk carries no system view c.vulkan (go run ./tools/sysview -vulkan ... makes it)")
	}
	if _, ok := sysview.Embedded("x86_64-windows", "windows"); !ok {
		t.Skip("this mk carries no system view c.windows")
	}

	main := `import c.vulkan
import std.mem
import std.raw

export Main(sys: &System): WriteError!void {
    ver := vulkan.VkHeaderVersion
    s0 := vulkan.VkPipelineStage2None
    s1 := vulkan.VkPipelineStage2CopyBit
    var pfn: vulkan.PfnVkGetInstanceProcAddr = none
    var pfn2: vulkan.PfnVkCreateInstance = none
    var info := mem.Zeroed[vulkan.VkWin32SurfaceCreateInfoKhr]()
    info.s_type = vulkan.VkStructureTypeWin32SurfaceCreateInfoKhr
    _ = raw.Of(&info)
    _ = pfn
    _ = pfn2
    try sys.Print("ver {x} s0 {s0} s1 {s1}\n", (x: ver > 0, s0, s1))
}
`
	root := writeTree(t, sysPkg(main, nil))
	out, _ := realBuild(t, sysPkg(main, nil), false)
	if out != "ver true s0 0 s1 4294967296\n" {
		t.Errorf("stdout %q", out)
	}

	// Cross-target check & compile on x86_64-windows and x86_64-linux using
	// the embedded x86_64-windows view served for both targets (and verify
	// that without a Linux archive, x86_64-linux reports E0202 no-sysview).
	tools := needTools(t)
	cfgNoLinux := Config{Root: root, Target: types.X86_64Linux, Tools: tools, CacheDir: t.TempDir()}
	sNoLinux, err := NewSession(cfgNoLinux)
	if err != nil {
		t.Fatal(err)
	}
	rNoLinux := sNoLinux.Check()
	noSysview := false
	for _, e := range rNoLinux.Report.Diags {
		if e.Diag.Code.String() == "E0202" && fact(e.Diag, "reason") == "no-sysview" {
			noSysview = true
		}
	}
	if !noSysview {
		t.Fatalf("expected E0202 no-sysview on x86_64-linux without archive, got:\n%s", rNoLinux.Report.Text(rNoLinux.Sources))
	}

	serveBoth := func(target, name string) ([]byte, bool) {
		if target == "x86_64-linux" {
			return sysview.Embedded("x86_64-windows", name)
		}
		return sysview.Embedded(target, name)
	}
	for _, tg := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		cfg := Config{Root: root, Target: tg, Tools: tools, CacheDir: t.TempDir()}
		cfg.TestHooks.SysViews = serveBoth
		s, err := NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r, paths, err := s.Build(filepath.Join(t.TempDir(), "vk"+exeSuffix(tg)))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Report.Diags) > 0 || len(paths) != 1 {
			t.Fatalf("build %s:\n%.4000s", tg.Name, r.Report.Text(r.Sources))
		}
	}
	cfgAll := Config{Root: root, Target: types.X86_64Windows, AllTargets: true, Tools: tools, CacheDir: t.TempDir()}
	cfgAll.TestHooks.SysViews = serveBoth
	sAll, err := NewSession(cfgAll)
	if err != nil {
		t.Fatal(err)
	}
	rAll := sAll.Check()
	clean(t, rAll)
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
