// Why: mk sdk is how a user learns which system views mk carries and what
// they were generated from; its output and exit codes are its interface.
// What: TestMkSdkCommand.
// How: mk sdk views, mk sdk, a bad argument and mk version, run in
// process; the Windows views are embedded in this mk.

package main

import (
	"strings"
	"testing"
)

func TestMkSdkCommand(t *testing.T) {
	st, out, _ := mk("sdk", "views")
	if st != 0 {
		t.Fatalf("mk sdk views: status %d", st)
	}
	for _, want := range []string{"system views", "c.windows", "x86_64-windows", "windows-sdk 10.0.28000.0", "headers  windows.h windowsx.h",
		"c.dxgi", "requires windows", "c.vulkan", "not carried: generate it with go run ./tools/sysview -vulkan"} {
		if !strings.Contains(out, want) {
			t.Errorf("mk sdk views lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "system headers of a pkg.CImport") {
		t.Errorf("mk sdk views prints the sysroots:\n%s", out)
	}
	st, all, _ := mk("sdk")
	if st != 0 || !strings.HasPrefix(all, out) || !strings.Contains(all, "system headers of a pkg.CImport, by target:") ||
		!strings.Contains(all, "  x86_64-linux\n") || !strings.Contains(all, "with sdk: true") {
		t.Errorf("mk sdk: status %d\n%s", st, all)
	}
	if st, _, errOut := mk("sdk", "bogus"); st != 2 || !strings.Contains(errOut, "usage: mk sdk") {
		t.Errorf("mk sdk bogus: status %d, stderr %q", st, errOut)
	}
	if st, v, _ := mk("version"); st != 0 || !strings.Contains(v, "\nsystem views: 5 carried, from vulkan-headers unversioned, windows-sdk 10.0.28000.0 (mk sdk") {
		t.Errorf("mk version: status %d\n%s", st, v)
	}
}
