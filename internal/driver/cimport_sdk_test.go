// Why: a program must be able to call the Windows API through a C import
// of the real Windows SDK, with no declaration written by hand; only the
// real SDK shows the view checks whole and the link and the calls work.
// What: TestCImport_WindowsSDKWindow, run only when MK_CIMPORT_WINDOWS_SDK
// names the SDK's Include\<version> directory (CI's runners have none).
// How: MK_SYSROOT_x86_64-windows lists the SDK's um, shared and ucrt; a
// package imports windows.h, d3d12.h, dxgi1_6.h and d3dcompiler.h in one
// C import; the whole view is checked (every item, not only the used
// ones), then a program registers a window class, creates a hidden window
// and destroys it, and its output is compared.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/types"
)

// sdkWindowMain registers a window class, creates a window it never shows,
// destroys it and prints whether each step succeeded.
const sdkWindowMain = `import c.dx
import std.mem
import std.raw

WndProc(hwnd: *dx.Hwnd, msg: u32, wp: u64, lp: i64): i64 {
    unsafe "DefWindowProcW is the default handler for any message" {
        return dx.DefWindowProcW(hwnd, msg, wp, lp)
    }
}

export Main(sys: &System): WriteError!void {
    cls: [8]u16 = [0x6d, 0x6b, 0x74, 0x65, 0x73, 0x74, 0x77, 0]
    var atom: u16 = 0
    var made := false
    var destroyed: i32 = 0
    unsafe "the class name is a NUL-terminated UTF-16 array that outlives the window; WndProc only forwards" {
        inst := dx.GetModuleHandleW(raw.Null[u16]())
        var wc := mem.Zeroed[dx.Wndclassexw]()
        wc.cb_size = Cast[u32](SizeOf[dx.Wndclassexw]())
        wc.lpfn_wnd_proc = some(WndProc)
        wc.h_instance = inst
        wc.lpsz_class_name = raw.Data(cls[..])
        atom = dx.RegisterClassExW(raw.OfMut(mut wc))
        h := dx.CreateWindowExW(0, raw.Data(cls[..]), raw.Data(cls[..]), 0, 0, 0, 64, 64, raw.Null[dx.Hwnd](), raw.Null[dx.Hmenu](), inst, raw.Null[void]())
        made = !raw.IsNull(h)
        destroyed = dx.DestroyWindow(h)
    }
    registered := atom != 0
    try sys.Print("registered {registered} created {made} destroyed {destroyed}\n", (registered, made, destroyed))
}
`

// TestCImport_WindowsSDKWindow: the Windows SDK's four headers in one C
// import give a view that checks whole with no diagnostic (each IID_ the
// headers declare twice binds once), and a program that creates and
// destroys a hidden window through it builds, links and runs.
func TestCImport_WindowsSDKWindow(t *testing.T) {
	root := os.Getenv("MK_CIMPORT_WINDOWS_SDK")
	if root == "" {
		t.Skip(`MK_CIMPORT_WINDOWS_SDK is not set (the Windows SDK's Include\<version>, e.g. C:\Program Files (x86)\Windows Kits\10\Include\10.0.28000.0)`)
	}
	if HostTarget() != types.X86_64Windows {
		t.Skip("the program runs on a Windows host")
	}
	var dirs []string
	for _, sub := range []string{"um", "shared", "ucrt"} {
		d := filepath.Join(root, sub)
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Skipf("MK_CIMPORT_WINDOWS_SDK=%s has no %s directory", root, sub)
		}
		dirs = append(dirs, d)
	}
	tools := needTools(t)
	clearSysroots(t)
	t.Setenv("MK_SYSROOT_x86_64-windows", strings.Join(dirs, string(os.PathListSeparator)))
	dir := writeTree(t, map[string]string{
		"pkg.mk": manifest(map[string]string{"name": `"sdkwin"`,
			"bins":           `[pkg.Bin(name: "sdkwin", root: "app", link_script: "", cpu: "", subsystem: "console")]`,
			"unsafe_modules": `["app"]`,
			"c_imports":      `[pkg.CImport(name: "dx", headers: ["windows.h", "d3d12.h", "dxgi1_6.h", "d3dcompiler.h"], defines: [], macros: [], vendor: false)]`,
			"links":          `[pkg.Link(name: "user32.dll", kind: "dynamic", path: "", target: "x86_64-windows")]`}),
		"app/main.mk": sdkWindowMain,
	})
	cache := t.TempDir()
	cfg := Config{Root: dir, Target: types.X86_64Windows, Tools: tools, CacheDir: cache}
	cfg.TestHooks.CImportWhole = true
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) > 0 {
		text := r.Report.Text(r.Sources)
		t.Fatalf("whole view: %d diagnostics\n%.4000s", len(r.Report.Diags), text)
	}
	st := s.w.cimports.stats[0]
	t.Logf("whole view checks; %d of %d declarations bound", st.Bound(), st.Decls)
	s, err = NewSession(Config{Root: dir, Target: types.X86_64Windows, Tools: tools, CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "sdkwin.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build: %d paths\n%.4000s", len(paths), r.Report.Text(r.Sources))
	}
	if !s.w.cimports.hit {
		t.Error("the build missed the translation the check cached")
	}
	out, errOut, code := runExe(t, types.X86_64Windows, paths[0], "")
	if want := "registered true created true destroyed 1\n"; code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, want)
	}
}
