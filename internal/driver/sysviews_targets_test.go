// Why: a system view is plain Mk, so a build for any target may import
// it; what reaches a view of another system must fail at the link with a
// message that says so, and --all-targets must judge per-target views.
//
// What: TestSysView_AnyTargetGuarded (a Windows call behind Target.os
// builds for Linux, and runs on Windows), TestSysView_AnyTargetReached
// (E0001 naming the function and the view) and TestSysView_AllTargets.
//
// How: the fake views of sysviews_test.go; a fake vulkan view generated
// for two targets with one item that differs.

package driver

import (
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/cimport"
	"mk/internal/sysview"
	"mk/internal/types"
)

const sysGuardedMain = `import c.windows

export Main(sys: &System): WriteError!void {
    var pid: windows.Dword = 0
    if Target.os == Os.Windows {
        unsafe "GetCurrentProcessId takes nothing and cannot fail" {
            pid = windows.GetCurrentProcessId()
        }
    }
    ok := pid > 0 || Target.os != Os.Windows
    try sys.Print("{ok}\n", (ok))
}
`

// sysBuild builds root for target with the fake views.
func sysBuild(t *testing.T, root string, target types.Target) (Result, []string) {
	t.Helper()
	cfg := Config{Root: root, Target: target, Tools: needTools(t), CacheDir: t.TempDir()}
	cfg.TestHooks.SysViews = fakeSysViews(t)
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, paths, err := s.Build(filepath.Join(t.TempDir(), "p"+exeSuffix(target)))
	if err != nil {
		t.Fatal(err)
	}
	return r, paths
}

func TestSysView_AnyTargetGuarded(t *testing.T) {
	dir := writeTree(t, sysPkg(sysGuardedMain, nil))
	r, paths := sysBuild(t, dir, types.X86_64Linux)
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build for x86_64-linux:\n%s", r.Report.Text(r.Sources))
	}
	if HostTarget() != types.X86_64Windows {
		return
	}
	r, paths = sysBuild(t, dir, types.X86_64Windows)
	if len(r.Report.Diags) > 0 || len(paths) != 1 {
		t.Fatalf("build for x86_64-windows:\n%s", r.Report.Text(r.Sources))
	}
	if out, errOut, code := runExe(t, types.X86_64Windows, paths[0], ""); code != 0 || out != "true\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestSysView_AnyTargetReached(t *testing.T) {
	main := strings.Replace(sysGuardedMain, "if Target.os == Os.Windows {", "if true {", 1)
	r, _ := sysBuild(t, writeTree(t, sysPkg(main, nil)), types.X86_64Linux)
	found := false
	for _, e := range r.Report.Diags {
		d := e.Diag
		if fact(d, "view") == "c.windows" && fact(d, "symbol") == "GetCurrentProcessId" &&
			strings.Contains(d.Message, "GetCurrentProcessId is a function of the system view c.windows, an API of x86_64-windows") &&
			strings.Contains(d.Message, "Target.os == Os.Windows") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no E0001 naming the view:\n%s", r.Report.Text(r.Sources))
	}
}

// fakeVulkan serves a vulkan view for two targets, whose VkFlags is u32 on
// Windows and differ on Linux when differ is set, plus the fake Windows
// views.
func fakeVulkan(t *testing.T, differ bool) func(target, name string) ([]byte, bool) {
	win := fakeSysViews(t)
	text := func(flags string) []byte {
		v := "// The C import c.vulkan.\n// headers: vulkan/vulkan.h\n\n// C: typedef uint32_t VkFlags\nexport type VkFlags = " + flags +
			"\n\n// C: VkResult vkEnumerate(VkFlags)\nexport extern vkEnumerate(f: VkFlags): i32\n"
		views, _ := cimport.ViewItems([]string{"vulkan"}, []string{v}, nil)
		views[0].Meta.Headers = []string{"vulkan/vulkan.h"}
		b, err := sysview.Write(views[0].Meta, views[0].Items, sysview.DefaultOptions)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	linux := "u32"
	if differ {
		linux = "u64"
	}
	return func(target, name string) ([]byte, bool) {
		switch {
		case name != "vulkan":
			return win(target, name)
		case target == "x86_64-windows":
			return text("u32"), true
		case target == "x86_64-linux":
			return text(linux), true
		}
		return nil, false
	}
}

func TestSysView_AllTargets(t *testing.T) {
	main := "import c.vulkan\nimport c.windows\n\nexport Main(sys: &System): WriteError!void {\n    var n: i32 = 0\n" +
		"    var pid: windows.Dword = 0\n    if Target.os == Os.Windows {\n        unsafe \"test\" {\n            pid = windows.GetCurrentProcessId()\n        }\n    }\n" +
		"    if Target.os == Os.Linux {\n        unsafe \"test\" {\n            n = vulkan.vkEnumerate(0)\n        }\n    }\n    try sys.Print(\"{n} {pid}\\n\", (n, pid))\n}\n"
	dir := writeTree(t, sysPkg(main, nil))
	for _, differ := range []bool{false, true} {
		cfg := Config{Root: dir, Target: types.X86_64Windows, AllTargets: true, CacheDir: t.TempDir()}
		cfg.TestHooks.SysViews = fakeVulkan(t, differ)
		s, err := NewSession(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		if !differ {
			clean(t, r)
			continue
		}
		if len(r.Report.Diags) != 1 {
			t.Fatalf("differ: %s", r.Report.Text(r.Sources))
		}
		d := r.Report.Diags[0].Diag
		if d.Code.String() != "E0213" || !strings.Contains(d.Message, "c.vulkan differs between x86_64-windows and x86_64-linux") ||
			!strings.Contains(fact(d, "items"), "VkFlags") {
			t.Errorf("%s %s (items %q)", d.Code, d.Message, fact(d, "items"))
		}
	}
}
