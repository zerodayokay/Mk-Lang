// Why: sdk: true must read the host's SDK only when asked, refuse to guess
// when there is none, and give a C type the program also takes from a
// system view one identity, the view's.
// What: TestSDKImport_SharesViewTypes, TestSDKImport_NoSDK and
// TestSDKImport_LayoutMismatch.
// How: a package C import whose header includes <windows.h> from a fake
// host SDK (TestHooks.HostSDK), beside the fake c.windows view of
// sysviews_test.go; the program passes a windows.Hwnd to the library.

package driver

import (
	"strings"
	"testing"

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
