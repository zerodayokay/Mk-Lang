// Why: sdk: true must read the host's SDK only when asked, refuse to guess
// when there is none, and give a C type the program also takes from a
// system view one identity, the view's.
// What: TestSDKImport_SharesViewTypes, TestSDKImport_NoSDK,
// TestSDKImport_LayoutMismatch and TestSDKImport_ForeignViewNotShared.
// How: a package C import whose header includes <windows.h> from a fake
// host SDK (TestHooks.HostSDK), beside the fake c.windows view of
// sysviews_test.go; the program passes a windows.Hwnd to the library. For
// Linux, fake vulkan views with and without a Linux archive.

package driver

import (
	"strings"
	"testing"

	"mk/internal/cimport"
	"mk/internal/sysview"
	"mk/internal/types"
)

// sdkPkg is a package importing c.lib (sdk: true when sdk) and c.windows.
func sdkPkg(sdk bool) map[string]string {
	flag := ""
	if sdk {
		flag = ", sdk: true"
	}
	main := "import c.lib\nimport c.windows\nimport std.raw\n\nNull(): windows.Hwnd {\n    unsafe \"test\" {\n" +
		"        return raw.Null[windows.HwndStruct]()\n    }\n}\n\nexport Main(sys: &System): WriteError!void {\n" +
		"    var n: i32 = 0\n    unsafe \"test\" {\n        n = lib.LibUse(Null())\n    }\n    try sys.Print(\"{n}\\n\", (n))\n}\n"
	files := sysPkg(main, map[string]string{"c_imports": `[pkg.CImport(name: "lib", headers: ["lib.h"], defines: [], macros: [], vendor: false, include_dirs: []` + flag + `)]`})
	files["lib.h"] = "#include <windows.h>\nint LibUse(HWND h);\n"
	return files
}

// fakeHostSDK is a host SDK directory whose windows.h matches the fake view.
func fakeHostSDK(t *testing.T, unused string) string {
	return writeTree(t, map[string]string{"windows.h": "#pragma once\ntypedef struct HWND__ { " + unused + " } *HWND;\nunsigned long GetCurrentProcessId(void);\n"})
}

func sdkCheck(t *testing.T, files map[string]string, host string) Result {
	tools := needTools(t)
	clearSysroots(t)
	_, r := sysSession(t, writeTree(t, files), types.X86_64Windows, func(c *Config) {
		c.Tools = tools
		c.TestHooks.HostSDK = func(string) ([]string, string) {
			if host == "" {
				return nil, ""
			}
			return []string{host}, "a test's SDK"
		}
	})
	return r
}

func TestSDKImport_SharesViewTypes(t *testing.T) {
	host := fakeHostSDK(t, "int unused;")
	clean(t, sdkCheck(t, sdkPkg(true), host))
	// Without sdk: true the host's headers are not read: windows.h is not
	// found (E0001), as before.
	if r := sdkCheck(t, sdkPkg(false), host); len(r.Report.Diags) == 0 {
		t.Error("without sdk: true the host SDK was read")
	}
}

func TestSDKImport_NoSDK(t *testing.T) {
	r := sdkCheck(t, sdkPkg(true), "")
	if len(r.Report.Diags) != 1 || fact(r.Report.Diags[0].Diag, "reason") != "no-sysroot" ||
		!strings.Contains(r.Report.Diags[0].Diag.Message, "mk sdk") {
		t.Fatalf("%s", r.Report.Text(r.Sources))
	}
}

func TestSDKImport_LayoutMismatch(t *testing.T) {
	r := sdkCheck(t, sdkPkg(true), fakeHostSDK(t, "long long unused;"))
	if !strings.Contains(r.Report.Text(r.Sources), "struct HWND__ is 8 bytes, align 8 here and 4 bytes, align 4 in the system view c.windows") {
		t.Fatalf("%s", r.Report.Text(r.Sources))
	}
}

// foreignWindowsView is the fake windows view with labs, whose C long is
// 32 bits there and 64 bits on Linux.
const foreignWindowsView = fakeWindowsView + `
// C: long labs(long)
export extern labs(n: i32): i32
`

// foreignVulkanView is a vulkan view whose Windows form requires windows
// (as the shipped one does) and whose Linux form does not.
func foreignVulkanView(windows bool) string {
	v := "// The C import c.vulkan.\n// headers: vulkan.h\n\n"
	if windows {
		v += "import c.windows\n\n"
	}
	v += "// C: struct VkThing, 4 bytes, align 4\nexport struct VkThing {\n    export x: i32\n}\n"
	if windows {
		v += "\n// C: struct VkWin32Info, 8 bytes, align 8\nexport struct VkWin32Info {\n    export hwnd: windows.Hwnd\n}\n"
	}
	return v
}

// foreignViews serves windows and vulkan for x86_64-windows, and vulkan
// for x86_64-linux when linux is set.
func foreignViews(t *testing.T, linux bool) func(target, name string) ([]byte, bool) {
	write := func(target string, names, texts []string) map[string][]byte {
		views, drop := cimport.ViewItems(names, texts, nil)
		if len(drop) != 0 {
			t.Fatalf("drop %v", drop)
		}
		out := map[string][]byte{}
		for _, v := range views {
			v.Meta.Target = target
			v.Meta.Headers = []string{v.Meta.Name + ".h"}
			b, err := sysview.Write(v.Meta, v.Items, sysview.DefaultOptions)
			if err != nil {
				t.Fatal(err)
			}
			out[v.Meta.Name] = b
		}
		return out
	}
	byTarget := map[string]map[string][]byte{
		"x86_64-windows": write("x86_64-windows", []string{"windows", "vulkan"}, []string{foreignWindowsView, foreignVulkanView(true)}),
	}
	if linux {
		byTarget["x86_64-linux"] = write("x86_64-linux", []string{"vulkan"}, []string{foreignVulkanView(false)})
	}
	return func(target, name string) ([]byte, bool) {
		b, ok := byTarget[target][name]
		return b, ok
	}
}

// TestSDKImport_ForeignViewNotShared: in a build for Linux, sdk: true
// shares nothing with a view read from Windows' declarations (c.vulkan
// with no Linux archive, and the c.windows it requires), whose long is not
// Linux's: lib.labs stays the Linux binding's, i64. A view generated for
// Linux is shared as on Windows: lib's VkThing is vulkan.VkThing.
func TestSDKImport_ForeignViewNotShared(t *testing.T) {
	tools := needTools(t)
	clearSysroots(t)
	host := writeTree(t, map[string]string{
		"stdlib.h": "#pragma once\nlong labs(long n);\n",
		"vulkan.h": "#pragma once\nstruct VkThing { int x; };\n",
	})
	files := func(body string) map[string]string {
		main := "import c.lib\nimport c.vulkan\n\n" + body
		f := sysPkg(main, map[string]string{"c_imports": `[pkg.CImport(name: "lib", headers: ["lib.h"], defines: [], macros: [], vendor: false, include_dirs: [], sdk: true)]`})
		f["lib.h"] = "#include <stdlib.h>\n#include <vulkan.h>\nint LibThing(struct VkThing *t);\n"
		return f
	}
	check := func(linux bool, body string) Result {
		_, r := sysSession(t, writeTree(t, files(body)), types.X86_64Linux, func(c *Config) {
			c.Tools = tools
			c.TestHooks.SysViews = foreignViews(t, linux)
			c.TestHooks.HostSDK = func(string) ([]string, string) { return []string{host}, "a test's SDK" }
		})
		return r
	}
	labs := "Abs(n: i64): i64 {\n    unsafe \"test\" {\n        return lib.labs(n)\n    }\n}\n\n"
	thing := func(mod string) string {
		return "Thing(p: *" + mod + ".VkThing): i32 {\n    unsafe \"test\" {\n        return lib.LibThing(p)\n    }\n}\n\n"
	}
	tail := "export Main(sys: &System): WriteError!void {\n    v := vulkan.VkThing(x: 1)\n    try sys.Print(\"{a} {x}\\n\", (a: Abs(-5), x: v.x))\n}\n"
	// No Linux vulkan: the Windows declarations are checked, never shared.
	clean(t, check(false, labs+thing("lib")+tail))
	// A Linux vulkan: its declarations are the binding's; labs is not one.
	clean(t, check(true, labs+thing("vulkan")+tail))
}
